package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/launch"
	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
	"github.com/himanshumehta/flexrouter/aitank/internal/providers/cursor"
	"github.com/himanshumehta/flexrouter/aitank/internal/render"
	"github.com/himanshumehta/flexrouter/aitank/internal/state"
)

// pickAccount resolves an explicit account, or the family's "use next".
func (a *app) pickAccount(family string, ref string) (*config.Account, *engine.View, error) {
	cfg, err := a.load()
	if err != nil {
		return nil, nil, err
	}
	v := engine.BuildView(cfg, a.now())
	if ref != "" {
		acct, err := cfg.Account(ref)
		if err != nil {
			return nil, nil, err
		}
		if family != "" && acct.Provider != family {
			return nil, nil, fmt.Errorf("%s is a %s account, not %s", acct.Label(), acct.Provider, family)
		}
		return acct, v, nil
	}
	pick := v.Rec.ByFamily[family]
	if pick == nil {
		return nil, nil, &ExitErr{Code: ExitNoAccount, Msg: fmt.Sprintf("No usable %s account to recommend (see `aitank next --provider %s`). Name one: aitank %s <id>", family, family, family)}
	}
	acct, err := cfg.Account(pick.AccountID)
	return acct, v, err
}

func launchCmd(a *app, family string) *cobra.Command {
	name := map[string]string{"claude": "Claude Code", "codex": "Codex"}[family]
	env := map[string]string{"claude": "CLAUDE_CONFIG_DIR", "codex": "CODEX_HOME"}[family]
	return &cobra.Command{
		Use:   family + " [account] [-- vendor args...]",
		Short: "Start " + name + " on an account (default: the one with most room)",
		Long: fmt.Sprintf(`Start your own %s binary with the account's profile folder (%s).
With no account it uses the "use next" %s account. It first prints the
account, folder and %% left. Arguments after -- go to %s unchanged. aitank
only sets the folder; it never reads or copies the sign-in.`, "`"+family+"`", env, family, family),
		Example:            fmt.Sprintf("  aitank %[1]s\n  aitank %[1]s %[1]s-2\n  aitank %[1]s -- --resume", family),
		Args:               cobra.ArbitraryArgs,
		DisableFlagParsing: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			dash := cmd.ArgsLenAtDash()
			var ref string
			var extra []string
			if dash >= 0 {
				extra = args[dash:]
				args = args[:dash]
			}
			if len(args) > 1 {
				return usageErr{fmt.Errorf("give at most one account; put %s arguments after --", family)}
			}
			if len(args) == 1 {
				ref = args[0]
			}
			acct, v, err := a.pickAccount(family, ref)
			if err != nil {
				return err
			}
			spec, err := launch.For(acct)
			if err != nil {
				return err
			}
			st := render.Style{On: render.ColorEnabled(a.errOut, a.noColor)}
			folder := spec.Dir
			if folder == "" {
				folder = "default (" + map[string]string{"claude": "~/.claude", "codex": "~/.codex"}[family] + ")"
			}
			left := render.Dash
			if r := v.Row(acct.ID); r != nil {
				left = st.LeftColor(v.Config.Colors, r.Left, render.Pct(r.Left))
				if r.Status != "ok" {
					left += " " + render.StatusText(r, st)
				}
			}
			fmt.Fprintf(a.errOut, "%s %s · folder %s · %s left\n", st.Dim("→"), st.Bold(acct.Label()), folder, left)
			return spec.Exec(extra)
		},
	}
}

func cmdCmd(a *app) *cobra.Command {
	var copy bool
	c := &cobra.Command{
		Use:   "cmd <account> [-- vendor args...]",
		Short: "Print the one-line command that starts an account",
		Long: `Print a one-line shell command that starts the vendor CLI on an account. It
contains only the profile folder path, never a secret. --copy also puts it
on the clipboard with pbcopy.`,
		Example: "  aitank cmd claude-2\n  aitank cmd codex-1 --copy",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var extra []string
			if d := cmd.ArgsLenAtDash(); d >= 0 {
				extra = args[d:]
				args = args[:d]
			}
			if len(args) != 1 {
				return usageErr{errors.New("give exactly one account")}
			}
			cfg, err := a.load()
			if err != nil {
				return err
			}
			acct, err := cfg.Account(args[0])
			if err != nil {
				return err
			}
			spec, err := launch.For(acct)
			if err != nil {
				return err
			}
			line := spec.Command(extra)
			if a.jsonOut {
				return render.WriteJSON(a.out, "cmd", map[string]string{"account": acct.ID, "command": line})
			}
			fmt.Fprintln(a.out, line)
			if copy {
				if err := launch.Copy(line); err != nil {
					return err
				}
				fmt.Fprintln(a.errOut, "Copied to the clipboard.")
			}
			return nil
		},
	}
	c.Flags().BoolVar(&copy, "copy", false, "also copy to the clipboard (pbcopy)")
	return c
}

func cursorSwitchCmd(a *app) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "cursor-switch <claude-account>",
		Short: "Point Cursor's Claude panel at a Claude account",
		Long: `Set the Claude Code extension's claudeCode.environmentVariables in Cursor's
user settings so its panel uses the account's profile folder. The settings
file is backed up first. The default account removes the override instead
of pointing at ~/.claude, because naming ~/.claude explicitly makes Claude
Code look for a different Keychain item and sign the panel out. Reload the
Cursor window afterwards. Asks for confirmation the first time.`,
		Example: "  aitank cursor-switch claude-2",
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			acct, err := cfg.Account(args[0])
			if err != nil {
				return err
			}
			if acct.Provider != "claude" {
				return fmt.Errorf("cursor-switch works with Claude accounts; %s is %s", acct.Label(), acct.Provider)
			}
			st := state.LoadAlerts()
			if !st.Flags["cursor_switch_confirmed"] && !yes {
				a.printf("This edits %s (a backup is kept in %s).\n", cursor.SettingsPath(paths.Home()), paths.Backups())
				if !confirm(a, "Allow aitank to change Cursor's Claude panel setting? You will not be asked again.") {
					return &ExitErr{Code: ExitError, Msg: "Cancelled."}
				}
				st.Flags["cursor_switch_confirmed"] = true
				_ = st.Save(a.now())
			}
			path := cursor.SettingsPath(paths.Home())
			backup, lostComments, err := SwitchCursor(path, acct.ProfileDir)
			if err != nil {
				return err
			}
			if backup != "" {
				a.printf("Backed up settings to %s\n", backup)
			}
			if lostComments {
				a.printf("Note: comments in settings.json were not kept; they are in the backup.\n")
			}
			a.printf("Cursor's Claude panel now uses %s. Reload the Cursor window (Developer: Reload Window).\n", acct.Label())
			return nil
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "skip the one-time confirmation")
	return c
}

// SwitchCursor rewrites claudeCode.environmentVariables in Cursor's
// settings.json. It returns the backup path and whether comments were lost.
func SwitchCursor(path, profileDir string) (backup string, lostComments bool, err error) {
	settings := map[string]any{}
	data, err := os.ReadFile(path)
	if err == nil {
		clean := StripJSONC(data)
		lostComments = len(stripComments(data)) != len(data)
		if len(strings.TrimSpace(string(clean))) > 0 {
			if err := json.Unmarshal(clean, &settings); err != nil {
				return "", false, fmt.Errorf("could not parse %s: %v", path, err)
			}
		}
		if backup, err = paths.Backup(path, "cursor-settings.json"); err != nil {
			return "", false, fmt.Errorf("backup failed, nothing changed: %v", err)
		}
	} else if !os.IsNotExist(err) {
		return "", false, err
	}
	const key = "claudeCode.environmentVariables"
	var vars []any
	if cur, ok := settings[key].([]any); ok {
		for _, v := range cur {
			if m, ok := v.(map[string]any); ok && m["name"] == "CLAUDE_CONFIG_DIR" {
				continue
			}
			vars = append(vars, v)
		}
	}
	if profileDir != "" {
		vars = append(vars, map[string]any{"name": "CLAUDE_CONFIG_DIR", "value": profileDir})
	}
	if vars == nil {
		vars = []any{}
	}
	settings[key] = vars
	out, err := json.MarshalIndent(settings, "", "    ")
	if err != nil {
		return backup, lostComments, err
	}
	return backup, lostComments, os.WriteFile(path, append(out, '\n'), 0o644)
}

// StripJSONC removes // and /* */ comments and trailing commas outside
// strings, turning VS Code-style settings into plain JSON.
func StripJSONC(in []byte) []byte {
	return stripTrailingCommas(stripComments(in))
}

func stripComments(in []byte) []byte {
	var out []byte
	inStr, esc := false, false
	for i := 0; i < len(in); i++ {
		c := in[i]
		if inStr {
			out = append(out, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		switch {
		case c == '"':
			inStr = true
			out = append(out, c)
		case c == '/' && i+1 < len(in) && in[i+1] == '/':
			for i < len(in) && in[i] != '\n' {
				i++
			}
			if i < len(in) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(in) && in[i+1] == '*':
			i += 2
			for i+1 < len(in) && !(in[i] == '*' && in[i+1] == '/') {
				i++
			}
			i++
		default:
			out = append(out, c)
		}
	}
	return out
}

// stripTrailingCommas drops "," before "}" or "]".
func stripTrailingCommas(out []byte) []byte {
	var res []byte
	inStr, esc := false, false
	for i := 0; i < len(out); i++ {
		c := out[i]
		if inStr {
			res = append(res, c)
			switch {
			case esc:
				esc = false
			case c == '\\':
				esc = true
			case c == '"':
				inStr = false
			}
			continue
		}
		if c == '"' {
			inStr = true
		}
		if c == ',' {
			j := i + 1
			for j < len(out) && (out[j] == ' ' || out[j] == '\n' || out[j] == '\t' || out[j] == '\r') {
				j++
			}
			if j < len(out) && (out[j] == '}' || out[j] == ']') {
				continue
			}
		}
		res = append(res, c)
	}
	return res
}
