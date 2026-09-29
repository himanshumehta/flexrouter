package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
	"github.com/himanshumehta/flexrouter/aitank/internal/render"
	"github.com/himanshumehta/flexrouter/aitank/internal/state"
)

// detectionOrder is the fixed, documented list `init` scans (FR-2.1).
var detectionOrder = []string{"claude", "codex", "cursor", "kilo", "copilot"}

type found struct {
	provider.Detection
	tracked bool
}

func (a *app) detect(cfg *config.Config) []found {
	env := provider.DefaultEnv(cfg)
	var out []found
	for _, id := range detectionOrder {
		p, ok := provider.Get(id)
		if !ok {
			continue
		}
		for _, d := range p.Detect(env) {
			f := found{Detection: d}
			for _, acct := range cfg.Accounts {
				if acct.Provider == d.Provider && filepath.Clean(acct.ProfileDir) == filepath.Clean(d.ProfileDir) {
					f.tracked = true
				}
			}
			out = append(out, f)
		}
	}
	return out
}

func initCmd(a *app) *cobra.Command {
	var dryRun, all bool
	c := &cobra.Command{
		Use:   "init",
		Short: "Find signed-in tools and choose accounts to track",
		Long: `Scan a fixed list of local files for tools that are already signed in:
Claude Code, Codex, Cursor, Kilo CLI and GitHub CLI (see 'aitank privacy' for
each path). Discovery is read-only: no network requests, no vendor tools
launched, no passwords. Nothing is tracked until you tick it.`,
		Example: "  aitank init\n  aitank init --dry-run\n  aitank init --all",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			fs := a.detect(cfg)
			if a.jsonOut {
				type det struct {
					Provider   string `json:"provider"`
					Identity   string `json:"identity,omitempty"`
					Plan       string `json:"plan,omitempty"`
					ProfileDir string `json:"profile_dir,omitempty"`
					Source     string `json:"source"`
					Tracked    bool   `json:"tracked"`
				}
				ds := []det{}
				for _, f := range fs {
					ds = append(ds, det{f.Provider, f.Identity, f.Plan, f.ProfileDir, f.Source, f.tracked})
				}
				if dryRun || !all {
					return render.WriteJSON(a.out, "detect", ds)
				}
			}
			st := a.style()
			if len(fs) == 0 {
				a.printf("No signed-in tools found. Add accounts with `aitank add <provider>` (see `aitank help providers`).\n")
				return nil
			}
			a.printf("Found on this Mac (read-only scan, nothing sent anywhere):\n")
			for i, f := range fs {
				who := f.Identity
				if who == "" {
					who = render.Dash
				}
				extra := ""
				if f.ProfileDir != "" {
					extra = " " + st.Dim("["+f.ProfileDir+"]")
				}
				if f.tracked {
					extra += " " + st.Dim("(already tracked)")
				}
				if f.Note != "" {
					extra += " " + st.Dim(f.Note)
				}
				a.printf("  %d. %-8s %s%s\n", i+1, f.Provider, who, extra)
			}
			if dryRun {
				a.printf("\nDry run: nothing saved.\n")
				return nil
			}
			var pick []int
			if all {
				for i := range fs {
					pick = append(pick, i)
				}
			} else {
				if !isTerminal(os.Stdin) {
					return fmt.Errorf("no terminal to ask which accounts to track; rerun with --all or use `aitank add`")
				}
				a.printf("\nTrack which? Numbers separated by commas, 'all', or Enter for none: ")
				line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
				pick, err = parsePicks(strings.TrimSpace(line), len(fs))
				if err != nil {
					return usageErr{err}
				}
			}
			added := 0
			for _, i := range pick {
				f := fs[i]
				if f.tracked {
					continue
				}
				acct := config.Account{
					ID: cfg.NewAccountID(f.Provider), Provider: f.Provider, Identity: f.Identity, Plan: f.Plan,
					Auth: string(provider.AuthLocal), ProfileDir: f.ProfileDir, AddedAt: a.now().UTC(),
				}
				acct.Nickname = defaultNickname(cfg, f.Provider, f.Identity)
				cfg.Accounts = append(cfg.Accounts, acct)
				added++
				a.printf("  added %s\n", st.Bold(acct.Label()))
			}
			if added == 0 {
				a.printf("Nothing added.\n")
				return nil
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			a.printf("\nNext: `aitank refresh` to read them, `aitank daemon install` to keep them fresh.\n")
			return nil
		},
	}
	c.Flags().BoolVar(&dryRun, "dry-run", false, "list what would be detected without saving anything")
	c.Flags().BoolVar(&all, "all", false, "track everything found without asking")
	return c
}

func parsePicks(s string, n int) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	if strings.EqualFold(s, "all") {
		out := make([]int, n)
		for i := range out {
			out[i] = i
		}
		return out, nil
	}
	seen := map[int]bool{}
	var out []int
	for _, part := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ' ' }) {
		k, err := strconv.Atoi(part)
		if err != nil || k < 1 || k > n {
			return nil, fmt.Errorf("%q is not a number from 1 to %d", part, n)
		}
		if !seen[k-1] {
			seen[k-1] = true
			out = append(out, k-1)
		}
	}
	sort.Ints(out)
	return out, nil
}

// defaultNickname uses the email's local part, else "personal", "2", ...
func defaultNickname(cfg *config.Config, prov, identity string) string {
	base := identity
	if i := strings.IndexByte(base, '@'); i > 0 {
		base = base[:i]
	}
	if base == "" {
		base = "personal"
	}
	nick := base
	for n := 2; ; n++ {
		taken := false
		for _, x := range cfg.Accounts {
			if x.Provider == prov && x.Nickname == nick {
				taken = true
			}
		}
		if !taken {
			return nick
		}
		nick = fmt.Sprintf("%s-%d", base, n)
	}
}

func isTerminal(f *os.File) bool { return term.IsTerminal(int(f.Fd())) }

func accountsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "accounts",
		Short:   "List tracked accounts: provider, plan, nickname, status",
		Example: "  aitank accounts\n  aitank accounts --json",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, err := a.view()
			if err != nil {
				return err
			}
			if a.jsonOut {
				type acc struct {
					ID         string `json:"id"`
					Provider   string `json:"provider"`
					Nickname   string `json:"nickname"`
					Plan       string `json:"plan,omitempty"`
					Identity   string `json:"identity,omitempty"`
					Auth       string `json:"auth"`
					KeyHint    string `json:"key_hint,omitempty"`
					ProfileDir string `json:"profile_dir,omitempty"`
					Status     string `json:"status"`
					Paused     bool   `json:"paused"`
					Alerts     bool   `json:"alerts"`
				}
				out := []acc{}
				for _, r := range v.Rows {
					x := r.Account
					out = append(out, acc{x.ID, x.Provider, x.Nickname, x.Plan, x.Identity, x.Auth, x.KeyHint, x.ProfileDir, string(r.Status), x.Paused, x.Alerts.Enabled})
				}
				return render.WriteJSON(a.out, "accounts", out)
			}
			if len(v.Rows) == 0 {
				a.printf("No accounts tracked yet. Run `aitank init` or `aitank add <provider>`.\n")
				return nil
			}
			st := a.style()
			a.printf("%s\n", st.Dim(fmt.Sprintf("%-12s %-14s %-14s %-18s %-26s %s", "ID", "PROVIDER", "NICKNAME", "PLAN", "IDENTITY", "STATUS")))
			for _, r := range v.Rows {
				x := r.Account
				plan := x.Plan
				if plan == "" {
					plan = render.Dash
				}
				who := x.Identity
				if who == "" {
					who = x.KeyHint
				}
				if who == "" {
					who = render.Dash
				}
				a.printf("%-12s %-14s %-14s %-18s %-26s %s\n", x.ID, x.Provider, render.Truncate(x.Nickname, 14), render.Truncate(plan, 18), render.Truncate(who, 26), render.StatusText(r, st))
			}
			return nil
		},
	}
}

func addCmd(a *app) *cobra.Command {
	var nickname, profile string
	var newProfile, useKey, keyStdin, device bool
	var options []string
	c := &cobra.Command{
		Use:   "add <provider>",
		Short: "Track an account (local sign-in, pasted key or device code)",
		Long: `Track a new account. How it is read depends on the provider:

  claude, codex     the vendor CLI's own sign-in, in a profile folder
  cursor, copilot   the Cursor app session / the gh CLI sign-in
  kilo              the Kilo CLI sign-in, or --key to paste a key
  openrouter, deepseek, moonshot, xai, ollama,
  anthropic-api, openai-api
                    a pasted key, stored only in the macOS Keychain

For Claude and Codex the first account uses the vendor's default folder
(~/.claude, ~/.codex). Each extra account gets its own profile folder that
aitank creates; sign in once with 'aitank claude <id>' (then /login) or
'aitank codex <id> login'.`,
		Example: `  aitank add claude                     # the default ~/.claude sign-in
  aitank add claude --new --nickname work
  aitank add claude --profile ~/.claude-work
  aitank add openrouter --nickname team
  pbpaste | aitank add deepseek --key-stdin
  aitank add moonshot --option region=cn`,
		Args: exactArgs(1),
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			var ids []string
			for _, p := range provider.All() {
				ids = append(ids, p.ID()+"\t"+p.Name())
			}
			return ids, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			p, ok := provider.Get(strings.ToLower(args[0]))
			if !ok {
				var ids []string
				for _, x := range provider.All() {
					ids = append(ids, x.ID())
				}
				return usageErr{fmt.Errorf("unknown provider %q; one of: %s", args[0], strings.Join(ids, ", "))}
			}
			caps := p.Capabilities()
			if device {
				if _, ok := p.(provider.DeviceLogin); !ok {
					return fmt.Errorf("%s has no device-code sign-in in this version; use its local sign-in or a pasted key", p.Name())
				}
			}
			acct := config.Account{ID: cfg.NewAccountID(p.ID()), Provider: p.ID(), AddedAt: a.now().UTC(), Options: map[string]string{}}
			for _, o := range options {
				k, v, ok := strings.Cut(o, "=")
				if !ok {
					return usageErr{fmt.Errorf("--option wants key=value, got %q", o)}
				}
				acct.Options[k] = v
			}
			hasAuth := func(m provider.AuthMethod) bool {
				for _, x := range caps.Auth {
					if x == m {
						return true
					}
				}
				return false
			}
			wantKey := useKey || keyStdin || !hasAuth(provider.AuthLocal)
			st := a.style()
			switch {
			case wantKey:
				if !hasAuth(provider.AuthKey) {
					return fmt.Errorf("%s is read from its local sign-in, not a pasted key", p.Name())
				}
				key, err := readKey(a, caps.KeyHelp, keyStdin)
				if err != nil {
					return err
				}
				acct.Auth, acct.KeyHint = string(provider.AuthKey), state.Mask(key)
				env := a.env()
				if err := env.Secrets.Set(acct.ID, "aitank: "+p.Name()+" ("+acct.ID+")", key); err != nil {
					return fmt.Errorf("could not store the key in the Keychain: %v", err)
				}
			case caps.Profiles:
				acct.Auth = string(provider.AuthLocal)
				switch {
				case profile != "":
					dir, err := filepath.Abs(expandTilde(profile))
					if err != nil {
						return err
					}
					acct.ProfileDir = dir
				case newProfile || defaultTracked(cfg, p.ID()):
					acct.ProfileDir = paths.Profile(acct.ID)
					if err := os.MkdirAll(acct.ProfileDir, 0o700); err != nil {
						return err
					}
					acct.ProfileCreated = true
				}
				for _, x := range cfg.Accounts {
					if x.Provider == acct.Provider && filepath.Clean(x.ProfileDir) == filepath.Clean(acct.ProfileDir) {
						return fmt.Errorf("%s already uses that profile folder; add --new for another account", x.Label())
					}
				}
			default:
				acct.Auth = string(provider.AuthLocal)
				for _, x := range cfg.Accounts {
					if x.Provider == acct.Provider && x.Auth == acct.Auth {
						return fmt.Errorf("%s already tracks the local %s sign-in", x.Label(), p.Name())
					}
				}
			}
			if nickname == "" {
				for _, d := range p.Detect(provider.DefaultEnv(cfg)) {
					if filepath.Clean(d.ProfileDir) == filepath.Clean(acct.ProfileDir) && acct.Auth == string(provider.AuthLocal) {
						acct.Identity, acct.Plan = d.Identity, d.Plan
					}
				}
				nickname = defaultNickname(cfg, acct.Provider, acct.Identity)
				if acct.ProfileCreated {
					nickname = defaultNickname(cfg, acct.Provider, "account-"+strings.TrimPrefix(acct.ID, acct.Provider+"-"))
				}
			}
			acct.Nickname = nickname
			cfg.Accounts = append(cfg.Accounts, acct)
			if err := cfg.Save(); err != nil {
				return err
			}
			a.printf("Added %s (id %s).\n", st.Bold(acct.Label()), acct.ID)
			if acct.ProfileCreated {
				a.printf("Its profile folder is %s\n", acct.ProfileDir)
				if p.ID() == "claude" {
					a.printf("Sign in once: aitank claude %s   (then type /login)\n", acct.ID)
				} else {
					a.printf("Sign in once: aitank codex %s -- login\n", acct.ID)
				}
				return nil
			}
			// First read, now that the account is added (FR-15.4).
			outs, err := engine.Refresh(context.Background(), cfg, a.env(), engine.RefreshOptions{Only: acct.ID, Force: true})
			if err == nil && len(outs) == 1 {
				if outs[0].Err != nil {
					a.printf("First read: %s: %v\n", st.Red(outs[0].Status.Label()), outs[0].Err)
				} else {
					_ = cfg.Save()
					a.printf("First read: %s\n", st.Green("OK"))
				}
			}
			return nil
		},
	}
	f := c.Flags()
	f.StringVar(&nickname, "nickname", "", "name to show, e.g. personal or work")
	f.StringVar(&profile, "profile", "", "bind to an existing Claude/Codex profile folder")
	f.BoolVar(&newProfile, "new", false, "create a new profile folder for another Claude/Codex account")
	f.BoolVar(&useKey, "key", false, "paste an API key (prompted without echo)")
	f.BoolVar(&keyStdin, "key-stdin", false, "read the API key from standard input")
	f.BoolVar(&device, "device", false, "sign in with a device code")
	f.StringArrayVar(&options, "option", nil, "provider option key=value (e.g. region=cn for moonshot)")
	return c
}

func defaultTracked(cfg *config.Config, prov string) bool {
	for _, x := range cfg.Accounts {
		if x.Provider == prov && x.ProfileDir == "" {
			return true
		}
	}
	return false
}

func expandTilde(p string) string {
	if strings.HasPrefix(p, "~/") {
		return filepath.Join(paths.Home(), p[2:])
	}
	return p
}

// readKey reads a key without echo, or from stdin with --key-stdin. Keys
// are never accepted as command-line arguments, which end up in shell
// history.
func readKey(a *app, help string, fromStdin bool) (string, error) {
	var key string
	if fromStdin || !isTerminal(os.Stdin) {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 64*1024))
		if err != nil {
			return "", err
		}
		key = strings.TrimSpace(string(b))
	} else {
		if help != "" {
			fmt.Fprintf(a.errOut, "Paste %s.\n", help)
		}
		fmt.Fprint(a.errOut, "Key (hidden): ")
		b, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(a.errOut)
		if err != nil {
			return "", err
		}
		key = strings.TrimSpace(string(b))
	}
	if key == "" {
		return "", errors.New("no key given")
	}
	if strings.ContainsAny(key, " \t\n\"\\") {
		return "", errors.New("that does not look like an API key (it contains spaces, quotes or line breaks)")
	}
	return key, nil
}

func renameCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "rename <account> <nickname>",
		Short:   "Give an account a nickname",
		Example: "  aitank rename claude-2 work",
		Args:    exactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			acct, err := cfg.Account(args[0])
			if err != nil {
				return err
			}
			nick := strings.TrimSpace(args[1])
			if nick == "" || strings.ContainsAny(nick, "/ \t") {
				return usageErr{fmt.Errorf("nicknames cannot be empty or contain spaces or /")}
			}
			for _, x := range cfg.Accounts {
				if x.ID != acct.ID && x.Provider == acct.Provider && x.Nickname == nick {
					return fmt.Errorf("%s already uses that nickname", x.Label())
				}
			}
			acct.Nickname = nick
			if err := cfg.Save(); err != nil {
				return err
			}
			a.printf("Renamed %s to %s.\n", acct.ID, acct.Label())
			return nil
		},
	}
}

func removeCmd(a *app) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "remove <account>",
		Short: "Stop tracking an account and delete what aitank created for it",
		Long: `Stop tracking an account. aitank deletes only what it created: the account's
config entry, cached readings, its Keychain item for a pasted key, and the
profile folder if aitank created it. The vendor's own sign-in (Claude Code,
Codex, Cursor, gh) is never touched.`,
		Example: "  aitank remove claude-2\n  aitank remove openrouter-1 --yes",
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
			x := *acct
			a.printf("This removes %s: its settings and cached readings", x.Label())
			if x.Auth == string(provider.AuthKey) {
				a.printf(", its key in the Keychain")
			}
			ownDir := x.ProfileCreated && strings.HasPrefix(filepath.Clean(x.ProfileDir), filepath.Clean(paths.Profiles())+string(filepath.Separator))
			if ownDir {
				a.printf(", and the profile folder aitank created (%s)", x.ProfileDir)
			}
			a.printf(".\n")
			if !yes && !confirm(a, "Remove it?") {
				return &ExitErr{Code: ExitError, Msg: "Cancelled."}
			}
			if x.Auth == string(provider.AuthKey) || x.Auth == string(provider.AuthDevice) {
				if err := a.env().Secrets.Delete(x.ID); err != nil {
					a.printf("warning: could not delete the Keychain item: %v\n", err)
				}
			}
			if ownDir {
				if err := os.RemoveAll(x.ProfileDir); err != nil {
					a.printf("warning: could not delete %s: %v\n", x.ProfileDir, err)
				}
			}
			cfg.RemoveAccount(x.ID)
			if err := cfg.Save(); err != nil {
				return err
			}
			_ = paths.WithLock(func() error {
				c := state.LoadCache()
				delete(c.Accounts, x.ID)
				return c.Save()
			})
			a.printf("Removed %s.\n", x.Label())
			return nil
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return c
}

func confirm(a *app, q string) bool {
	if !isTerminal(os.Stdin) {
		return false
	}
	fmt.Fprintf(a.errOut, "%s [y/N] ", q)
	line, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

func pauseCmd(a *app) *cobra.Command {
	var indefinitely bool
	c := &cobra.Command{
		Use:   "pause <account>",
		Short: "Leave an account out of recommendations until it refills",
		Long: `Exclude an account from "use next" until its tightest window resets, or
until 'aitank resume' with --indefinitely.`,
		Example: "  aitank pause claude-1\n  aitank pause codex-2 --indefinitely",
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
			acct.Paused = true
			if acct.Options == nil {
				acct.Options = map[string]string{}
			}
			delete(acct.Options, "paused_until")
			msg := "until you run `aitank resume " + acct.ID + "`"
			if !indefinitely {
				if e := state.LoadCache().Accounts[acct.ID]; e != nil && e.LastGood != nil {
					if w, _, ok := e.LastGood.Binding(cfg.Recommend.IgnoreModelLimits); ok && w.ResetsAt != nil {
						acct.Options["paused_until"] = w.ResetsAt.UTC().Format(time.RFC3339)
						msg = "until its " + w.Name + " window refills (" + w.ResetsAt.Local().Format("Mon 15:04") + ") or you resume it"
					}
				}
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			a.printf("Paused %s %s.\n", acct.Label(), msg)
			return nil
		},
	}
	c.Flags().BoolVar(&indefinitely, "indefinitely", false, "stay paused until resumed, even after a refill")
	return c
}

func resumeCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "resume <account>",
		Short:   "Undo pause",
		Example: "  aitank resume claude-1",
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
			acct.Paused = false
			delete(acct.Options, "paused_until")
			if err := cfg.Save(); err != nil {
				return err
			}
			a.printf("Resumed %s.\n", acct.Label())
			return nil
		},
	}
}
