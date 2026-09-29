package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/himanshumehta/flexrouter/aitank/internal/alerts"
	"github.com/himanshumehta/flexrouter/aitank/internal/claudecode"
	"github.com/himanshumehta/flexrouter/aitank/internal/daemon"
	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
	"github.com/himanshumehta/flexrouter/aitank/internal/render"
	"github.com/himanshumehta/flexrouter/aitank/internal/state"
	"github.com/himanshumehta/flexrouter/aitank/internal/update"
)

func selfPath() string {
	p, err := os.Executable()
	if err != nil {
		return "aitank"
	}
	return p
}

func daemonCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "daemon",
		Short: "Manage the background launchd agent",
		Long: `The agent is a per-user launchd job (com.aitank.agent) that refreshes every
account on the refresh interval (default 5 min), writes the cache that all
display commands read, and sends alerts you have turned on.`,
		Example: "  aitank daemon install\n  aitank daemon status\n  aitank daemon logs",
	}
	c.AddCommand(&cobra.Command{
		Use:   "install",
		Short: "Register and start the agent",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			if err := daemon.Install(selfPath(), cfg.RefreshInterval.Duration); err != nil {
				return err
			}
			a.printf("Agent installed: refreshes every %s. Plist: %s\n", cfg.RefreshInterval, daemon.PlistPath())
			return nil
		},
	}, &cobra.Command{
		Use:   "uninstall",
		Short: "Stop and remove the agent",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := daemon.Uninstall(); err != nil {
				return err
			}
			a.printf("Agent removed.\n")
			return nil
		},
	}, &cobra.Command{
		Use:   "status",
		Short: "Show whether the agent is installed and running",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := daemon.GetStatus()
			if a.jsonOut {
				return render.WriteJSON(a.out, "daemon_status", s)
			}
			switch {
			case !s.Installed:
				a.printf("Agent not installed. Run `aitank daemon install`.\n")
			case !s.Loaded:
				a.printf("Agent installed but not loaded (%s). Run `aitank daemon install` again.\n", s.Detail)
			default:
				a.printf("Agent running every %s (%s). Last exit code: %s\n", s.Interval, s.Binary, orDash(s.LastExit))
			}
			if s.Installed && s.Binary != "" && s.Binary != selfPath() {
				a.printf("Note: the agent runs %s, not this binary (%s).\n", s.Binary, selfPath())
			}
			c := state.LoadCache()
			if !c.RefreshedAt.IsZero() {
				a.printf("Last full refresh %s ago.\n", render.Countdown(a.now().Sub(c.RefreshedAt)))
			}
			return nil
		},
	}, &cobra.Command{
		Use:   "logs",
		Short: "Show the agent's recent log",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			s, err := daemon.Logs(50)
			if err != nil {
				return fmt.Errorf("no agent log yet (%s)", paths.AgentLog())
			}
			fmt.Fprintln(a.out, s)
			return nil
		},
	}, &cobra.Command{
		Use:    "run",
		Short:  "One agent cycle: refresh, alerts, exit (launchd runs this)",
		Hidden: true,
		Args:   exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.agentCycle(context.Background())
		},
	})
	return c
}

// agentCycle is one launchd run.
func (a *app) agentCycle(ctx context.Context) error {
	cfg, err := a.load()
	if err != nil {
		return err
	}
	now := a.now()
	daemon.TrimLog()
	stamp := now.Format("2006-01-02 15:04:05")
	outs, err := engine.Refresh(ctx, cfg, a.env(), engine.RefreshOptions{})
	if err != nil {
		fmt.Fprintf(a.out, "%s refresh failed: %v\n", stamp, err)
		return err
	}
	_ = cfg.Save()
	ok, failed, skipped := 0, 0, 0
	for _, o := range outs {
		switch {
		case o.Skipped != "":
			skipped++
		case o.Err != nil:
			failed++
			fmt.Fprintf(a.out, "%s %s: %s: %s\n", stamp, o.Account, o.Status.Label(), state.Redact(o.Err.Error()))
		default:
			ok++
		}
	}
	fmt.Fprintf(a.out, "%s refreshed %d ok, %d failed, %d backing off\n", stamp, ok, failed, skipped)
	a.sendAlerts(now)
	if msg := update.MaybeCheck(ctx, cfg, now, Version); msg != "" {
		fmt.Fprintf(a.out, "%s %s\n", stamp, msg)
	}
	return nil
}

func (a *app) sendAlerts(now time.Time) {
	v := engine.BuildView(a.cfg, now)
	_ = paths.WithLock(func() error {
		st := state.LoadAlerts()
		for _, n := range alerts.Evaluate(v, st, now) {
			if err := alerts.Deliver(n, a.cfg.Alerts.Sound); err != nil {
				fmt.Fprintf(a.out, "notification failed: %v\n", err)
			}
		}
		return st.Save(now)
	})
}

func orDash(s string) string {
	if s == "" {
		return render.Dash
	}
	return s
}

func alertsCmd(a *app) *cobra.Command {
	var at string
	var account string
	c := &cobra.Command{
		Use:   "alerts [on|off]",
		Short: "Turn notifications on or off per account (off by default)",
		Long: `Alerts are off until you turn them on. When on for an account, the agent
notifies through Notification Center when its tightest window crosses the
threshold, when the pace would empty a window within an hour, when it hits
its limit, and when it refills. Bill reminders fire for bills you entered.
Each alert fires once per window cycle. With no argument, lists settings.`,
		Example: "  aitank alerts\n  aitank alerts on --at 20%\n  aitank alerts on --account claude-2 --at 10\n  aitank alerts off --account codex-1",
		Args:    maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				if a.jsonOut {
					type al struct {
						Account string     `json:"account"`
						Enabled bool       `json:"enabled"`
						At      float64    `json:"at_pct_left"`
						Quiet   *time.Time `json:"quiet_until"`
					}
					var out []al
					for _, x := range cfg.Accounts {
						at := x.Alerts.At
						if at == 0 {
							at = cfg.Alerts.DefaultAt
						}
						var q *time.Time
						if !x.QuietUntil.IsZero() {
							t := x.QuietUntil
							q = &t
						}
						out = append(out, al{x.ID, x.Alerts.Enabled, at, q})
					}
					return render.WriteJSON(a.out, "alerts", out)
				}
				for _, x := range cfg.Accounts {
					txt := "off"
					if x.Alerts.Enabled {
						at := x.Alerts.At
						if at == 0 {
							at = cfg.Alerts.DefaultAt
						}
						txt = fmt.Sprintf("on at %.0f%% left", at)
					}
					if !x.QuietUntil.IsZero() && a.now().Before(x.QuietUntil) {
						txt += " (quiet until " + x.QuietUntil.Local().Format("Mon 15:04") + ")"
					}
					a.printf("%-24s %s\n", x.Label(), txt)
				}
				a.printf("Forecast alerts: %v · refill alerts: %v · sound: %v · bill reminders: %d days before\n",
					cfg.Alerts.Forecast, cfg.Alerts.Refilled, cfg.Alerts.Sound, cfg.Alerts.BillDaysBefore)
				return nil
			}
			on := false
			switch args[0] {
			case "on":
				on = true
			case "off":
			default:
				return usageErr{fmt.Errorf("want on or off")}
			}
			var threshold float64
			if at != "" {
				f, err := strconv.ParseFloat(strings.TrimSuffix(at, "%"), 64)
				if err != nil || f <= 0 || f >= 100 {
					return usageErr{fmt.Errorf("--at wants a percentage left between 0 and 100")}
				}
				threshold = f
			}
			n := 0
			for i := range cfg.Accounts {
				x := &cfg.Accounts[i]
				if account != "" {
					match, err := cfg.Account(account)
					if err != nil {
						return err
					}
					if match.ID != x.ID {
						continue
					}
				}
				x.Alerts.Enabled = on
				if threshold > 0 {
					x.Alerts.At = threshold
				}
				n++
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			word := "off"
			if on {
				word = "on"
			}
			a.printf("Alerts %s for %d account(s).", word, n)
			if on && daemon.GetStatus().Installed == false {
				a.printf(" Alerts are sent by the agent; run `aitank daemon install`.")
			}
			a.printf("\n")
			return nil
		},
	}
	c.Flags().StringVar(&at, "at", "", "threshold in % left, e.g. 20%")
	c.Flags().StringVar(&account, "account", "", "only this account")
	return c
}

func quietCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "quiet <account>",
		Short:   "Mute an account's alerts until its tightest window refills",
		Example: "  aitank quiet claude-1",
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
			until := a.now().Add(24 * time.Hour)
			how := "for 24 hours (its reset time is unknown)"
			if e := state.LoadCache().Accounts[acct.ID]; e != nil && e.LastGood != nil {
				if w, _, ok := e.LastGood.Binding(cfg.Recommend.IgnoreModelLimits); ok && w.ResetsAt != nil {
					until = *w.ResetsAt
					how = "until its " + w.Name + " window refills (" + until.Local().Format("Mon 15:04") + ")"
				}
			}
			acct.QuietUntil = until.UTC()
			if err := cfg.Save(); err != nil {
				return err
			}
			a.printf("Muted %s %s.\n", acct.Label(), how)
			return nil
		},
	}
}

func setupCmd(a *app) *cobra.Command {
	c := &cobra.Command{Use: "setup", Short: "Set up integrations (claude-code)"}
	var apply, force bool
	var account string
	cc := &cobra.Command{
		Use:   "claude-code",
		Short: "Print or merge the statusLine and limit-hit hook settings",
		Long: `Print the settings.json blocks that make Claude Code show aitank's status
line and notify you when a session hits a usage limit. With --apply they are
merged into ~/.claude/settings.json (or the account's profile folder with
--account), after a backup. An existing non-aitank statusLine is only
replaced with --force.`,
		Example: "  aitank setup claude-code\n  aitank setup claude-code --apply\n  aitank setup claude-code --apply --account claude-2",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			dir := ""
			if account != "" {
				acct, err := cfg.Account(account)
				if err != nil {
					return err
				}
				dir = acct.ProfileDir
			}
			path := claudecode.SettingsPath(paths.Home(), dir)
			if !apply {
				b, _ := json.MarshalIndent(claudecode.Blocks(selfPath()), "", "  ")
				a.printf("Add these blocks to %s (or rerun with --apply):\n\n%s\n", path, b)
				return nil
			}
			res, err := claudecode.Apply(path, selfPath(), force)
			if err != nil {
				return err
			}
			if !res.Changed {
				a.printf("%s already has aitank's statusLine and hook.\n", path)
				return nil
			}
			if res.Backup != "" {
				a.printf("Backed up %s to %s\n", path, res.Backup)
			}
			a.printf("Updated %s. New Claude Code sessions will show aitank's status line.\n", path)
			return nil
		},
	}
	cc.Flags().BoolVar(&apply, "apply", false, "merge into settings.json (backs it up first)")
	cc.Flags().BoolVar(&force, "force", false, "replace an existing non-aitank statusLine")
	cc.Flags().StringVar(&account, "account", "", "a Claude account whose profile folder to configure")
	c.AddCommand(cc)
	return c
}

// readStdinQuick reads piped stdin without ever blocking the statusLine
// for long.
func readStdinQuick(max time.Duration) []byte {
	if isTerminal(os.Stdin) {
		return nil
	}
	ch := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(io.LimitReader(os.Stdin, 1<<20))
		ch <- b
	}()
	select {
	case b := <-ch:
		return b
	case <-time.After(max):
		return nil
	}
}

func statusCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Short line for Claude Code's statusLine (cache only, fast)",
		Long: `Print one short line for Claude Code's statusLine: the current session's
account and % left, plus the "use next" account when it differs. It reads
only the local cache, makes no network calls, and stores the rate limits
Claude Code passes on stdin so the cache stays current between full reads.
Set it up with 'aitank setup claude-code'.`,
		Example: `  echo '{"rate_limits":{"five_hour":{"used_percentage":40,"resets_at":1790000000}}}' | aitank status`,
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			now := a.now()
			acct := claudecode.SessionAccount(cfg, os.Getenv("CLAUDE_CONFIG_DIR"))
			if raw := readStdinQuick(40 * time.Millisecond); len(raw) > 0 {
				var in claudecode.StatusInput
				if json.Unmarshal(raw, &in) == nil {
					_ = claudecode.Ingest(cfg, acct, &in, now)
				}
			}
			v := engine.BuildView(cfg, now)
			st := a.style()
			var parts []string
			if acct != nil {
				r := v.Row(acct.ID)
				seg := acct.Nickname
				if seg == "" {
					seg = acct.ID
				}
				if r != nil {
					seg += " " + st.LeftColor(cfg.Colors, r.Left, render.Pct(r.Left)+" left")
					if r.Reading != nil {
						if w, _, ok := r.Reading.Binding(cfg.Recommend.IgnoreModelLimits); ok && w.ResetsAt != nil {
							seg += st.Dim(" · " + render.WindowShort(w) + " resets " + render.Countdown(w.ResetsAt.Sub(now)))
						}
					}
					if r.Stale {
						seg += " " + st.Amber(render.Age(r.Age))
					}
					for _, f := range r.Forecasts {
						if f.Warn {
							seg += " " + st.Amber(fmt.Sprintf("⚠ ~%dm", int(f.EmptyIn.Minutes())))
							break
						}
					}
				}
				parts = append(parts, seg)
			}
			if p := v.Rec.ByFamily["claude"]; p != nil && (acct == nil || p.AccountID != acct.ID) {
				left := p.Left
				name := p.Label
				if r := v.Row(p.AccountID); r != nil && r.Account.Nickname != "" {
					name = r.Account.Nickname
				}
				parts = append(parts, "next: "+name+" "+st.LeftColor(cfg.Colors, &left, render.Pct(&left)))
			}
			if len(parts) == 0 {
				parts = append(parts, "aitank: no Claude account tracked")
			}
			fmt.Fprintln(a.out, strings.Join(parts, st.Dim(" | ")))
			return nil
		},
	}
}

func hookCmd(a *app) *cobra.Command {
	c := &cobra.Command{Use: "hook", Short: "Claude Code hook handlers", Hidden: true}
	c.AddCommand(&cobra.Command{
		Use:   "limit-hit",
		Short: "StopFailure hook: notify which account has room",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			raw := readStdinQuick(500 * time.Millisecond)
			var in claudecode.HookInput
			_ = json.Unmarshal(raw, &in)
			if !in.IsLimitHit() {
				return nil
			}
			cfg, err := a.load()
			if err != nil {
				return nil // never fail the user's session
			}
			now := a.now()
			acct := claudecode.SessionAccount(cfg, os.Getenv("CLAUDE_CONFIG_DIR"))
			if acct != nil {
				ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
				_, _ = engine.Refresh(ctx, cfg, a.env(), engine.RefreshOptions{Only: acct.ID, Force: true})
				cancel()
			}
			v := engine.BuildView(cfg, now)
			who := "This Claude account"
			if acct != nil {
				who = acct.Label()
			}
			msg := "No other Claude account has room right now."
			if p := v.Rec.ByFamily["claude"]; p != nil && (acct == nil || p.AccountID != acct.ID) {
				msg = fmt.Sprintf("%s has %.0f%% left. Switch: aitank claude %s", p.Label, p.Left, p.AccountID)
			}
			n := alerts.Notice{Title: who + " hit its limit", Message: msg}
			_ = alerts.Deliver(n, cfg.Alerts.Sound)
			fmt.Fprintln(a.errOut, "aitank: "+n.Title+". "+msg)
			return nil
		},
	})
	return c
}
