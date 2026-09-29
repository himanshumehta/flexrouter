package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/launch"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
	"github.com/himanshumehta/flexrouter/aitank/internal/render"
	"github.com/himanshumehta/flexrouter/aitank/internal/tui"
)

func watchCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "watch",
		Short: "Full-screen dashboard with bars, countdowns and forecasts",
		Long: `Open a full-screen dashboard that redraws every second from the local cache.
The "use next" account is marked with ▶.

Keys: ↑/↓ or j/k move · Enter launch the selected Claude/Codex account ·
c copy its launch command · r refresh now · p pause/resume · q quit`,
		Example: "  aitank watch",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if _, err := a.load(); err != nil {
				return err
			}
			reloadCfg := func() *config.Config {
				if c, err := config.Load(); err == nil {
					a.cfg = c
				}
				return a.cfg
			}
			act := tui.Actions{
				Load: func() *engine.View { return engine.BuildView(reloadCfg(), a.now()) },
				Refresh: func() string {
					cfg := reloadCfg()
					ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
					defer cancel()
					outs, err := engine.Refresh(ctx, cfg, a.env(), engine.RefreshOptions{})
					if err != nil {
						return "Refresh failed: " + err.Error()
					}
					_ = cfg.Save()
					failed := 0
					for _, o := range outs {
						if o.Err != nil {
							failed++
						}
					}
					if failed > 0 {
						return fmt.Sprintf("Refreshed; %d account(s) failed (see `aitank log`)", failed)
					}
					return "Refreshed"
				},
				Pause: func(id string) string {
					cfg := reloadCfg()
					acct, err := cfg.Account(id)
					if err != nil {
						return err.Error()
					}
					acct.Paused = !acct.Paused
					if acct.Options != nil {
						delete(acct.Options, "paused_until")
					}
					if err := cfg.Save(); err != nil {
						return err.Error()
					}
					if acct.Paused {
						return "Paused " + acct.Label()
					}
					return "Resumed " + acct.Label()
				},
				Copy: func(id string) string {
					acct, err := a.cfg.Account(id)
					if err != nil {
						return err.Error()
					}
					spec, err := launch.For(acct)
					if err != nil {
						return err.Error()
					}
					if err := launch.Copy(spec.Command(nil)); err != nil {
						return err.Error()
					}
					return "Copied: " + spec.Command(nil)
				},
				CanLaunch: func(id string) bool {
					acct, err := a.cfg.Account(id)
					if err != nil {
						return false
					}
					p, ok := provider.Get(acct.Provider)
					if !ok {
						return false
					}
					_, ok = p.(provider.Switcher)
					return ok
				},
			}
			res, err := tui.Run(act, render.ColorEnabled(a.out, a.noColor))
			if err != nil {
				return err
			}
			if res.LaunchID == "" {
				return nil
			}
			acct, err := a.cfg.Account(res.LaunchID)
			if err != nil {
				return err
			}
			spec, err := launch.For(acct)
			if err != nil {
				return err
			}
			fmt.Fprintf(a.errOut, "→ %s · folder %s\n", acct.Label(), orDash(spec.Dir))
			return spec.Exec(nil)
		},
	}
}
