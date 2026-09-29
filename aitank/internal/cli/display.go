package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/model"
	"github.com/himanshumehta/flexrouter/aitank/internal/render"
)

func (a *app) view() (*engine.View, error) {
	cfg, err := a.load()
	if err != nil {
		return nil, err
	}
	return engine.BuildView(cfg, a.now()), nil
}

// summary is bare `aitank` (FR-9.1).
func (a *app) summary() error {
	v, err := a.view()
	if err != nil {
		return err
	}
	if a.jsonOut {
		if err := render.WriteJSON(a.out, "summary", render.ViewJSON(v)); err != nil {
			return err
		}
	} else if a.compact {
		render.List(a.out, v, a.style(), true)
	} else {
		fmt.Fprintln(a.out, render.Summary(v, a.style()))
	}
	if len(v.Rows) > 0 && v.Rec.Overall == nil {
		return &ExitErr{Code: ExitNoAccount}
	}
	return nil
}

func listCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "list",
		Short: "One row per account: plan, windows, resets and % left",
		Long: `List every tracked account from the local cache: provider, plan, nickname,
% used per window with a bar, reset countdowns, % left on the tightest window,
and a warning when the last hour's pace would empty a limit within an hour.
Makes no network calls; run 'aitank refresh' or install the agent to update.`,
		Example: "  aitank list\n  aitank list --compact\n  aitank list --json | jq '.data.accounts[] | {id, left_pct}'",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, err := a.view()
			if err != nil {
				return err
			}
			if a.jsonOut {
				return render.WriteJSON(a.out, "list", render.ViewJSON(v))
			}
			st := a.style()
			render.List(a.out, v, st, a.compact)
			if !v.RefreshedAt.IsZero() && !a.compact {
				fmt.Fprintln(a.out, st.Dim(fmt.Sprintf("Refreshed %s ago.", render.Countdown(a.now().Sub(v.RefreshedAt)))))
			}
			for _, r := range v.Rows {
				if r.Status == model.StatusStale {
					return &ExitErr{Code: ExitStale}
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&a.compact, "compact", false, "fit within 80 columns")
	return c
}

func nextCmd(a *app) *cobra.Command {
	var family string
	c := &cobra.Command{
		Use:   "next",
		Short: "Print the recommended account and why",
		Long: `Print the "use next" account overall, or for one provider family, and why it
was picked. Scoring uses the tightest window's % left, time to its reset and
the last hour's burn rate; paused, stale, errored and exhausted accounts are
left out. Exits 3 when no account is usable.`,
		Example: "  aitank next\n  aitank next --provider codex\n  aitank next --json",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, err := a.view()
			if err != nil {
				return err
			}
			pick := v.Rec.Overall
			if family != "" {
				family = strings.ToLower(family)
				if family != "claude" && family != "codex" {
					return usageErr{fmt.Errorf("--provider must be claude or codex")}
				}
				pick = v.Rec.ByFamily[family]
			}
			if a.jsonOut {
				data := map[string]any{"pick": pick, "excluded": v.Rec.Excluded}
				if err := render.WriteJSON(a.out, "next", data); err != nil {
					return err
				}
			} else if pick != nil {
				st := a.style()
				left := pick.Left
				a.printf("%s  %s left\n", st.Bold(pick.Label), st.LeftColor(v.Config.Colors, &left, render.Pct(&left)))
				a.printf("  why: %s\n", pick.Reason)
				for _, x := range v.Rec.Excluded {
					a.printf("  %s\n", st.Dim(fmt.Sprintf("skipped %s: %s", x.Label, x.Why)))
				}
			} else {
				a.printf("No usable account right now.\n")
				for _, x := range v.Rec.Excluded {
					a.printf("  %s: %s\n", x.Label, x.Why)
				}
			}
			if pick == nil {
				return &ExitErr{Code: ExitNoAccount}
			}
			return nil
		},
	}
	c.Flags().StringVar(&family, "provider", "", "limit to one family: claude or codex")
	return c
}

func refreshCmd(a *app) *cobra.Command {
	var force, quiet bool
	c := &cobra.Command{
		Use:   "refresh [account]",
		Short: "Read one or all accounts now",
		Long: `Read accounts from their vendors now and update the local cache. Accounts in
backoff after repeated errors are skipped unless --force; a vendor's
rate-limit wait is always respected. Exits 5 when any read failed.`,
		Example: "  aitank refresh\n  aitank refresh claude-1\n  aitank refresh --force",
		Args:    maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			only := ""
			if len(args) == 1 {
				acct, err := cfg.Account(args[0])
				if err != nil {
					return err
				}
				only = acct.ID
			}
			if len(cfg.Accounts) == 0 {
				return fmt.Errorf("no accounts tracked yet; run `aitank init` or `aitank add <provider>`")
			}
			outs, err := engine.Refresh(context.Background(), cfg, a.env(), engine.RefreshOptions{Only: only, Force: force})
			if err != nil {
				return err
			}
			_ = cfg.Save() // plan/identity may have been learned
			failed := 0
			st := a.style()
			for _, o := range outs {
				if o.Err != nil {
					failed++
				}
				if a.jsonOut || quiet {
					continue
				}
				acct, _ := cfg.Account(o.Account)
				label := o.Account
				if acct != nil {
					label = acct.Label()
				}
				switch {
				case o.Skipped != "":
					a.printf("%s  %s\n", label, st.Dim("skipped: "+o.Skipped))
				case o.Err != nil:
					a.printf("%s  %s: %s\n", label, st.Red(o.Status.Label()), o.Err)
				default:
					a.printf("%s  %s\n", label, st.Green("OK"))
				}
			}
			if a.jsonOut {
				type res struct {
					Account string       `json:"account"`
					Status  model.Status `json:"status"`
					Error   string       `json:"error,omitempty"`
					Skipped string       `json:"skipped,omitempty"`
				}
				var rs []res
				for _, o := range outs {
					r := res{Account: o.Account, Status: o.Status, Skipped: o.Skipped}
					if o.Err != nil {
						r.Error = o.Err.Error()
					}
					rs = append(rs, r)
				}
				if err := render.WriteJSON(a.out, "refresh", rs); err != nil {
					return err
				}
			}
			if failed > 0 {
				return &ExitErr{Code: ExitReadError}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&force, "force", false, "ignore error backoff")
	c.Flags().BoolVarP(&quiet, "quiet", "q", false, "print nothing on success")
	return c
}

func promptCmd(a *app) *cobra.Command {
	var shell string
	c := &cobra.Command{
		Use:   "prompt",
		Short: "Minimal segment for shell prompts (cache only)",
		Long: `Print a short segment for Starship, Powerlevel10k or a plain PS1: the
recommended account's nickname and % left. Reads only the local cache.
Prints nothing when no account is tracked.`,
		Example: `  # Starship (~/.config/starship.toml)
  [custom.aitank]
  command = "aitank prompt"
  when = true
  # zsh: --shell zsh wraps colours in %{ %} so the prompt width is right
  PROMPT='$(aitank prompt --shell zsh) %~ %# '`,
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			v, err := a.view()
			if err != nil {
				return err
			}
			if len(v.Rows) == 0 {
				return nil
			}
			st := a.style()
			p := v.Rec.Overall
			var seg string
			if p == nil {
				seg = st.Red("ai:none")
			} else {
				name := p.Label
				if r := v.Row(p.AccountID); r != nil && r.Account.Nickname != "" {
					name = r.Account.Nickname
				}
				left := p.Left
				seg = name + " " + st.LeftColor(v.Config.Colors, &left, render.Pct(&left))
			}
			if shell == "zsh" {
				seg = strings.ReplaceAll(seg, "\x1b[", "%{\x1b[")
				seg = zshClose(seg)
			}
			fmt.Fprint(a.out, seg)
			if shell == "" {
				fmt.Fprintln(a.out)
			}
			return nil
		},
	}
	c.Flags().StringVar(&shell, "shell", "", "zsh: escape colour codes for PROMPT; omits the trailing newline")
	return c
}

// zshClose closes every %{ opened before an ANSI sequence with %}.
func zshClose(s string) string {
	var b strings.Builder
	open := false
	for i := 0; i < len(s); i++ {
		if strings.HasPrefix(s[i:], "%{") {
			open = true
		}
		b.WriteByte(s[i])
		if open && s[i] == 'm' {
			b.WriteString("%}")
			open = false
		}
	}
	return b.String()
}
