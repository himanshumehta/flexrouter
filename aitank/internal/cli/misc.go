package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/himanshumehta/flexrouter/aitank"

	"github.com/himanshumehta/flexrouter/aitank/internal/bills"
	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/daemon"
	"github.com/himanshumehta/flexrouter/aitank/internal/paths"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
	"github.com/himanshumehta/flexrouter/aitank/internal/render"
	"github.com/himanshumehta/flexrouter/aitank/internal/secrets"
	"github.com/himanshumehta/flexrouter/aitank/internal/state"
	"github.com/himanshumehta/flexrouter/aitank/internal/update"
)

func configCmd(a *app) *cobra.Command {
	c := &cobra.Command{
		Use:   "config",
		Short: "Read or change settings (config.toml)",
		Long: `aitank keeps settings, tracked accounts and bills in one TOML file you can
edit directly (see 'aitank config path'). 'config get' and 'config set'
change single settings and validate them.`,
		Example: "  aitank config get refresh_interval\n  aitank config set refresh_interval 10m\n  aitank config set colors.left_amber 30\n  aitank config set recommend.ignore_model_limits true\n  aitank config set update.policy off",
	}
	c.AddCommand(&cobra.Command{
		Use:   "get [key]",
		Short: "Print a setting, or all settings",
		Args:  maxArgs(1),
		ValidArgsFunction: func(*cobra.Command, []string, string) ([]string, cobra.ShellCompDirective) {
			return config.Keys(), cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			keys := config.Keys()
			if len(args) == 1 {
				keys = args
			}
			vals := map[string]string{}
			for _, k := range keys {
				v, err := cfg.Get(k)
				if err != nil {
					return usageErr{err}
				}
				vals[k] = v
			}
			if a.jsonOut {
				return render.WriteJSON(a.out, "config", vals)
			}
			if len(args) == 1 {
				fmt.Fprintln(a.out, vals[args[0]])
				return nil
			}
			for _, k := range keys {
				a.printf("%-32s %s\n", k, vals[k])
			}
			return nil
		},
	}, &cobra.Command{
		Use:   "set <key> <value>",
		Short: "Change a setting",
		Args:  exactArgs(2),
		ValidArgsFunction: func(_ *cobra.Command, args []string, _ string) ([]string, cobra.ShellCompDirective) {
			if len(args) == 0 {
				return config.Keys(), cobra.ShellCompDirectiveNoFileComp
			}
			return nil, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			if err := cfg.Set(args[0], args[1]); err != nil {
				return usageErr{err}
			}
			if err := cfg.Save(); err != nil {
				return err
			}
			v, _ := cfg.Get(args[0])
			a.printf("%s = %s\n", args[0], v)
			if args[0] == "refresh_interval" && daemon.GetStatus().Installed {
				a.printf("Run `aitank daemon install` again to apply the new interval to the agent.\n")
			}
			return nil
		},
	}, &cobra.Command{
		Use:   "path",
		Short: "Print where aitank keeps its files",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprintln(a.out, paths.Config())
			return nil
		},
	})
	return c
}

func exportCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "export [file]",
		Short: "Back up settings, accounts and bills (never secrets)",
		Long: `Write the configuration (settings, tracked accounts, bills) as TOML to a file
or stdout. API keys are never included: they stay in the Keychain and must be
pasted again after an import on another Mac.`,
		Example: "  aitank export > aitank-backup.toml\n  aitank export ~/aitank-backup.toml",
		Args:    maxArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			data, err := cfg.Encode()
			if err != nil {
				return err
			}
			if len(args) == 0 {
				_, err = a.out.Write(data)
				return err
			}
			if err := os.WriteFile(args[0], data, 0o600); err != nil {
				return err
			}
			a.printf("Exported to %s (no secrets included).\n", args[0])
			return nil
		},
	}
}

func importCmd(a *app) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:     "import <file>",
		Short:   "Restore settings from an export",
		Example: "  aitank import aitank-backup.toml",
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			data, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			next := config.Default()
			if err := config.Decode(data, next); err != nil {
				return fmt.Errorf("%s: %w", args[0], err)
			}
			cur, err := a.load()
			if err != nil {
				return err
			}
			a.printf("This replaces the current configuration (%d accounts, %d bills) with %s (%d accounts, %d bills).\n",
				len(cur.Accounts), len(cur.Bills), args[0], len(next.Accounts), len(next.Bills))
			if !yes && !confirm(a, "Continue?") {
				return &ExitErr{Code: ExitError, Msg: "Cancelled."}
			}
			if _, err := os.Stat(paths.Config()); err == nil {
				if b, err := paths.Backup(paths.Config(), "config.toml"); err == nil {
					a.printf("Previous config backed up to %s\n", b)
				}
			}
			missing := 0
			for i := range next.Accounts {
				x := &next.Accounts[i]
				if x.Auth == string(provider.AuthKey) {
					if _, err := secrets.Default().Get(x.ID); err != nil {
						missing++
					}
				}
				if x.ProfileCreated && x.ProfileDir != "" {
					_ = os.MkdirAll(x.ProfileDir, 0o700)
				}
			}
			if err := next.Save(); err != nil {
				return err
			}
			a.cfg = next
			a.printf("Imported.")
			if missing > 0 {
				a.printf(" %d account(s) need their key pasted again: `aitank remove <id>` then `aitank add <provider>`.", missing)
			}
			a.printf("\n")
			return nil
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return c
}

func resetCmd(a *app) *cobra.Command {
	var yes bool
	c := &cobra.Command{
		Use:   "reset",
		Short: "Remove everything aitank created",
		Long: `Remove the launchd agent, aitank's Keychain items, its data folder (config,
cache, history, logs, backups) and the profile folders it created. Vendor
sign-ins (Claude Code, Codex, Cursor, gh) are not touched.`,
		Example: "  aitank reset",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			a.printf("This deletes:\n  the launchd agent (%s)\n  %d Keychain item(s) holding pasted keys\n  %s (config, cache, history, logs, backups, aitank-created profile folders)\n",
				daemon.PlistPath(), countKeyed(cfg), paths.Dir())
			var outside []string
			for _, x := range cfg.Accounts {
				if x.ProfileCreated && !strings.HasPrefix(x.ProfileDir, paths.Dir()) {
					outside = append(outside, x.ProfileDir)
				}
			}
			if !yes {
				if !isTerminal(os.Stdin) {
					return &ExitErr{Code: ExitError, Msg: "Refusing to reset without a terminal; pass --yes."}
				}
				fmt.Fprint(a.errOut, "Type 'reset' to confirm: ")
				var in string
				fmt.Fscanln(os.Stdin, &in)
				if in != "reset" {
					return &ExitErr{Code: ExitError, Msg: "Cancelled."}
				}
			}
			_ = daemon.Uninstall()
			for _, x := range cfg.Accounts {
				if x.Auth == string(provider.AuthKey) || x.Auth == string(provider.AuthDevice) {
					_ = secrets.Default().Delete(x.ID)
				}
			}
			for _, d := range outside {
				_ = os.RemoveAll(d)
			}
			if err := os.RemoveAll(paths.Dir()); err != nil {
				return err
			}
			a.printf("Everything aitank created is gone.\n")
			return nil
		},
	}
	c.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return c
}

func countKeyed(cfg *config.Config) int {
	n := 0
	for _, x := range cfg.Accounts {
		if x.Auth == string(provider.AuthKey) || x.Auth == string(provider.AuthDevice) {
			n++
		}
	}
	return n
}

func logCmd(a *app) *cobra.Command {
	var account string
	var n int
	c := &cobra.Command{
		Use:     "log",
		Short:   "Recent read attempts and errors (no secrets)",
		Example: "  aitank log\n  aitank log --account claude-1 -n 50",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			id := ""
			if account != "" {
				acct, err := cfg.Account(account)
				if err != nil {
					// Removed accounts can still appear in the log.
					id = account
				} else {
					id = acct.ID
				}
			}
			var es []state.LogEntry
			for _, e := range state.LoadLog() {
				if id == "" || e.Account == id {
					es = append(es, e)
				}
			}
			if len(es) > n {
				es = es[len(es)-n:]
			}
			if a.jsonOut {
				if es == nil {
					es = []state.LogEntry{}
				}
				return render.WriteJSON(a.out, "log", es)
			}
			st := a.style()
			for _, e := range es {
				s := st.Green(string(e.Status))
				if e.Status != "ok" {
					s = st.Red(string(e.Status))
				}
				line := fmt.Sprintf("%s  %-14s %-12s %5dms", e.T.Local().Format("Jan 02 15:04:05"), e.Account, s, e.Millis)
				if e.Error != "" {
					line += "  " + e.Error
				}
				fmt.Fprintln(a.out, line)
			}
			if len(es) == 0 {
				a.printf("No read attempts logged yet.\n")
			}
			return nil
		},
	}
	c.Flags().StringVar(&account, "account", "", "only this account")
	c.Flags().IntVarP(&n, "lines", "n", 30, "how many entries")
	return c
}

func privacyCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "privacy",
		Short: "Every file read and endpoint contacted, per provider",
		Long: `List every local file aitank reads, every command it runs and every endpoint
it contacts, per provider. A provider contacts its endpoints only after you
add an account for it. aitank has no account, sign-up or telemetry; its only
request to its own project is the optional update check.`,
		Args: exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			type prov struct {
				ID        string   `json:"id"`
				Name      string   `json:"name"`
				Tracked   bool     `json:"tracked"`
				FilesRead []string `json:"files_read"`
				Commands  []string `json:"commands"`
				Endpoints []string `json:"endpoints"`
				Keychain  bool     `json:"stores_key_in_keychain"`
			}
			var ps []prov
			for _, p := range provider.All() {
				c := p.Capabilities()
				tracked := false
				for _, x := range cfg.Accounts {
					tracked = tracked || x.Provider == p.ID()
				}
				key := false
				for _, m := range c.Auth {
					key = key || m == provider.AuthKey
				}
				ps = append(ps, prov{p.ID(), p.Name(), tracked, nonNil(c.FilesRead), nonNil(c.Commands), nonNil(c.Endpoints), key})
			}
			own := map[string]any{
				"data_folder":  paths.Dir(),
				"update_check": map[string]string{"policy": cfg.Update.Policy, "url": cfg.Update.URL},
				"telemetry":    "none",
			}
			if a.jsonOut {
				return render.WriteJSON(a.out, "privacy", map[string]any{"providers": ps, "aitank": own})
			}
			st := a.style()
			a.printf("%s\n  data folder: %s (0600 files)\n  telemetry: none\n  update check: %s (%s)\n\n", st.Bold("aitank itself"), paths.Dir(), cfg.Update.Policy, cfg.Update.URL)
			for _, p := range ps {
				t := st.Dim("not tracked: contacts nothing")
				if p.Tracked {
					t = st.Green("tracked")
				}
				a.printf("%s (%s) %s\n", st.Bold(p.Name), p.ID, t)
				for _, f := range p.FilesRead {
					a.printf("  reads    %s\n", f)
				}
				for _, c := range p.Commands {
					a.printf("  runs     %s\n", c)
				}
				for _, e := range p.Endpoints {
					a.printf("  contacts %s\n", e)
				}
				if p.Keychain {
					a.printf("  stores   a pasted key in the macOS Keychain (service \"aitank\")\n")
				}
			}
			return nil
		},
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func updateCmd(a *app) *cobra.Command {
	var check bool
	c := &cobra.Command{
		Use:   "update",
		Short: "Check for and install a new version (signature-verified)",
		Long: `Check the release list for a newer aitank and install it after verifying
the ed25519 signature on checksums.txt, the archive checksum and the macOS
code signature. When aitank was installed with Homebrew it defers to
'brew upgrade aitank'. Configure with 'aitank config set update.policy
auto|notify|off'.`,
		Example: "  aitank update --check\n  aitank update",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			rel, err := update.Latest(ctx, cfg.Update.URL)
			if err != nil {
				return err
			}
			if !update.Newer(rel.Version, Version) {
				a.printf("aitank %s is up to date (latest %s).\n", Version, rel.Version)
				return nil
			}
			a.printf("aitank %s is available (you have %s).\n", rel.Version, Version)
			if check {
				return nil
			}
			if update.BrewInstalled() {
				a.printf("Installed with Homebrew: run `brew upgrade aitank`.\n")
				return nil
			}
			path, err := update.Install(ctx, rel)
			if err != nil {
				return err
			}
			a.printf("Installed %s at %s.\n", rel.Version, path)
			return nil
		},
	}
	c.Flags().BoolVar(&check, "check", false, "only check, do not install")
	return c
}

func changelogCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "changelog",
		Short: "Show release notes",
		Args:  exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			fmt.Fprint(a.out, aitank.Changelog)
			return nil
		},
	}
}

func doctorCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "doctor",
		Short:   "Check vendor tools, sign-ins, Keychain, agent and cache",
		Example: "  aitank doctor\n  aitank doctor --json",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			type check struct {
				Name   string `json:"name"`
				OK     bool   `json:"ok"`
				Warn   bool   `json:"warn,omitempty"`
				Detail string `json:"detail"`
			}
			var cs []check
			add := func(name string, ok, warn bool, detail string) { cs = append(cs, check{name, ok, warn, detail}) }
			env := provider.DefaultEnv(cfg)
			tracked := map[string]bool{}
			for _, x := range cfg.Accounts {
				tracked[x.Provider] = true
			}
			for _, p := range provider.All() {
				caps := p.Capabilities()
				if caps.VendorTool == "" {
					continue
				}
				bin, err := provider.LookPath(caps.VendorTool)
				if err != nil {
					add(caps.VendorTool+" CLI", !tracked[p.ID()], true, "not found on PATH or in the usual install folders")
					continue
				}
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				g, err := env.Gate(ctx, caps, bin)
				cancel()
				switch {
				case err != nil:
					add(caps.VendorTool+" CLI", false, false, err.Error())
				case g.Untested:
					add(caps.VendorTool+" CLI", true, true, fmt.Sprintf("%s at %s: newer than tested (%s); reads still run", g.Version, bin, caps.TestedMax))
				default:
					add(caps.VendorTool+" CLI", true, false, fmt.Sprintf("%s at %s", g.Version, bin))
				}
			}
			if _, err := provider.LookPath("gh"); err == nil {
				add("gh CLI", true, false, "installed")
			} else if tracked["copilot"] {
				add("gh CLI", false, false, "not found; Copilot reads need it")
			}
			for _, f := range a.detect(cfg) {
				who := f.Identity
				if who == "" {
					who = "identity unknown"
				}
				add("sign-in: "+f.Provider, true, false, who+" ("+f.Source+")")
			}
			if err := secrets.Default().Check(); err != nil {
				add("Keychain", countKeyed(cfg) == 0, true, err.Error())
			} else {
				add("Keychain", true, false, "accessible")
			}
			ds := daemon.GetStatus()
			switch {
			case !ds.Installed:
				add("launchd agent", true, true, "not installed; data refreshes only when you run `aitank refresh` (`aitank daemon install`)")
			case !ds.Loaded:
				add("launchd agent", false, false, "installed but not loaded")
			default:
				add("launchd agent", ds.LastExit == "" || ds.LastExit == "0", false, "running every "+ds.Interval+", last exit "+orDash(ds.LastExit))
			}
			cache := state.LoadCache()
			if cache.RefreshedAt.IsZero() {
				add("cache", len(cfg.Accounts) == 0, true, "never refreshed")
			} else {
				age := a.now().Sub(cache.RefreshedAt)
				add("cache", age <= cfg.StaleAfter.Duration, false, "last full refresh "+render.Countdown(age)+" ago")
			}
			if fi, err := os.Stat(paths.Config()); err == nil && fi.Mode().Perm()&0o077 != 0 {
				add("file permissions", false, false, paths.Config()+" is readable by others; run chmod 600")
			}
			if a.jsonOut {
				return render.WriteJSON(a.out, "doctor", cs)
			}
			st := a.style()
			bad := 0
			for _, c := range cs {
				mark := st.Green("✓")
				if !c.OK {
					mark = st.Red("✗")
					bad++
				} else if c.Warn {
					mark = st.Amber("!")
				}
				a.printf("%s %-22s %s\n", mark, c.Name, c.Detail)
			}
			if bad > 0 {
				return &ExitErr{Code: ExitError}
			}
			return nil
		},
	}
}

func billCmd(a *app) *cobra.Command {
	c := &cobra.Command{Use: "bill", Short: "Add or remove a subscription bill"}
	var name, currency, cycle, renewal string
	var price float64
	add := &cobra.Command{
		Use:     "add",
		Short:   "Record a subscription: name, price, currency, cycle, renewal date",
		Example: "  aitank bill add --name \"Claude Max\" --price 200 --currency USD --cycle monthly --renewal 2026-10-15",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			if name == "" || price <= 0 || renewal == "" {
				return usageErr{fmt.Errorf("--name, --price and --renewal are required")}
			}
			if !bills.ValidCycle(cycle) {
				return usageErr{fmt.Errorf("--cycle must be one of %s", strings.Join(bills.Cycles, ", "))}
			}
			t, err := bills.ParseDate(renewal)
			if err != nil {
				return usageErr{err}
			}
			id := ""
			for n := 1; ; n++ {
				id = "bill-" + strconv.Itoa(n)
				taken := false
				for _, b := range cfg.Bills {
					taken = taken || b.ID == id
				}
				if !taken {
					break
				}
			}
			b := config.Bill{ID: id, Name: name, Price: price, Currency: strings.ToUpper(currency), Cycle: cycle, Renewal: t}
			cfg.Bills = append(cfg.Bills, b)
			if err := cfg.Save(); err != nil {
				return err
			}
			a.printf("Added %s (%s): %.2f %s %s, next renewal %s.\n", b.Name, b.ID, b.Price, b.Currency, b.Cycle, bills.NextRenewal(b, a.now()).Format("Mon 2 Jan 2006"))
			return nil
		},
	}
	f := add.Flags()
	f.StringVar(&name, "name", "", "subscription name")
	f.Float64Var(&price, "price", 0, "price per cycle")
	f.StringVar(&currency, "currency", "USD", "currency code")
	f.StringVar(&cycle, "cycle", "monthly", "weekly, monthly, quarterly or yearly")
	f.StringVar(&renewal, "renewal", "", "a renewal date, YYYY-MM-DD")
	rm := &cobra.Command{
		Use:     "remove <bill-id>",
		Short:   "Delete a bill",
		Example: "  aitank bill remove bill-2",
		Args:    exactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			out := cfg.Bills[:0]
			found := false
			for _, b := range cfg.Bills {
				if b.ID == args[0] {
					found = true
					continue
				}
				out = append(out, b)
			}
			if !found {
				return fmt.Errorf("no bill %q (see `aitank bills`)", args[0])
			}
			cfg.Bills = out
			if err := cfg.Save(); err != nil {
				return err
			}
			a.printf("Removed %s.\n", args[0])
			return nil
		},
	}
	c.AddCommand(add, rm)
	return c
}

func billsCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:     "bills",
		Short:   "List bills with next renewal and monthly total",
		Example: "  aitank bills\n  aitank bills --json",
		Args:    exactArgs(0),
		RunE: func(cmd *cobra.Command, _ []string) error {
			cfg, err := a.load()
			if err != nil {
				return err
			}
			now := a.now()
			bs := append([]config.Bill(nil), cfg.Bills...)
			sort.Slice(bs, func(i, j int) bool { return bills.NextRenewal(bs[i], now).Before(bills.NextRenewal(bs[j], now)) })
			if a.jsonOut {
				type bj struct {
					config.Bill
					NextRenewal time.Time `json:"next_renewal"`
					Monthly     float64   `json:"monthly"`
				}
				out := []bj{}
				for _, b := range bs {
					out = append(out, bj{b, bills.NextRenewal(b, now), bills.Monthly(b)})
				}
				return render.WriteJSON(a.out, "bills", map[string]any{"bills": out, "monthly_total": bills.Totals(bs)})
			}
			if len(bs) == 0 {
				a.printf("No bills. Add one with `aitank bill add --name ... --price ... --renewal YYYY-MM-DD`.\n")
				return nil
			}
			st := a.style()
			for _, b := range bs {
				next := bills.NextRenewal(b, now)
				days := int(next.Sub(now).Hours() / 24)
				when := next.Format("Mon 2 Jan")
				if days <= cfg.Alerts.BillDaysBefore {
					when = st.Amber(when)
				}
				a.printf("%-8s %-24s %10.2f %-4s %-10s renews %s\n", b.ID, render.Truncate(b.Name, 24), b.Price, b.Currency, b.Cycle, when)
			}
			a.printf("Monthly total: %s\n", bills.TotalsText(bs))
			return nil
		},
	}
}
