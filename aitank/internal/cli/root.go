// Package cli wires every aitank command.
package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/himanshumehta/flexrouter/aitank/internal/config"
	"github.com/himanshumehta/flexrouter/aitank/internal/engine"
	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
	"github.com/himanshumehta/flexrouter/aitank/internal/render"
)

// Build information, set with -ldflags at release time.
var (
	Version = "dev"
	Commit  = ""
	Date    = ""
)

// Exit codes (FR-18.4). Documented in `aitank help exit-codes`.
const (
	ExitOK        = 0
	ExitError     = 1 // any command error
	ExitUsage     = 2 // bad flags or arguments
	ExitNoAccount = 3 // no usable account to recommend
	ExitStale     = 4 // the data shown is stale
	ExitReadError = 5 // `refresh` could not read one or more accounts
)

// ExitErr carries an exit code out of a command.
type ExitErr struct {
	Code int
	Msg  string
}

func (e *ExitErr) Error() string { return e.Msg }

// app is shared state for one invocation.
type app struct {
	out, errOut io.Writer
	jsonOut     bool
	noColor     bool
	compact     bool
	cfg         *config.Config
	now         func() time.Time
}

func (a *app) style() render.Style {
	return render.Style{On: !a.jsonOut && render.ColorEnabled(a.out, a.noColor)}
}

func (a *app) load() (*config.Config, error) {
	if a.cfg != nil {
		return a.cfg, nil
	}
	c, err := config.Load()
	if err != nil {
		return nil, err
	}
	if engine.AutoResume(c, a.now()) {
		_ = c.Save()
	}
	a.cfg = c
	return c, nil
}

func (a *app) env() *provider.Env {
	return provider.DefaultEnv(a.cfg)
}

func (a *app) printf(format string, args ...any) { fmt.Fprintf(a.out, format, args...) }

// Execute runs the CLI and returns the process exit code.
func Execute(args []string) int {
	a := &app{out: os.Stdout, errOut: os.Stderr, now: time.Now}
	provider.UserAgent = "aitank/" + Version
	root := newRoot(a)
	root.SetArgs(args)
	err := root.Execute()
	if err == nil {
		return ExitOK
	}
	var ee *ExitErr
	if errors.As(err, &ee) {
		if ee.Msg != "" {
			fmt.Fprintln(a.errOut, ee.Msg)
		}
		return ee.Code
	}
	fmt.Fprintln(a.errOut, "aitank:", err)
	if isUsage(err) {
		return ExitUsage
	}
	return ExitError
}

type usageErr struct{ error }

func isUsage(err error) bool {
	var u usageErr
	return errors.As(err, &u)
}

func newRoot(a *app) *cobra.Command {
	root := &cobra.Command{
		Use:   "aitank",
		Short: "How much is left on each AI plan, and which account to use next",
		Long: `aitank shows how much usage is left on each AI plan you pay for (Claude,
ChatGPT/Codex, Cursor, Copilot, Kilo Code, Ollama, OpenRouter and API keys)
and recommends which account to use next. With no arguments it prints a
one-line summary from the local cache.`,
		Example: `  aitank                 one-line summary
  aitank init            find signed-in tools and pick accounts to track
  aitank list            one row per account
  aitank watch           full-screen dashboard
  aitank claude          start Claude Code on the account with room`,
		Version:       versionString(),
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return a.summary()
		},
	}
	root.SetFlagErrorFunc(func(c *cobra.Command, err error) error {
		return usageErr{fmt.Errorf("%v\nRun '%s --help' for usage", err, c.CommandPath())}
	})
	root.SetVersionTemplate("aitank {{.Version}}\n")
	pf := root.PersistentFlags()
	pf.BoolVar(&a.jsonOut, "json", false, "print JSON (stable, versioned schema)")
	pf.BoolVar(&a.noColor, "no-color", false, "disable colour (NO_COLOR is also respected)")
	root.Flags().BoolVar(&a.compact, "compact", false, "fit within 80 columns")

	root.AddGroup(&cobra.Group{ID: "view", Title: "Viewing usage:"},
		&cobra.Group{ID: "acct", Title: "Accounts:"},
		&cobra.Group{ID: "launch", Title: "Launching and switching:"},
		&cobra.Group{ID: "integ", Title: "Background agent, alerts and integrations:"},
		&cobra.Group{ID: "misc", Title: "Settings and maintenance:"})

	add := func(group string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = group
			root.AddCommand(c)
		}
	}
	add("view", listCmd(a), nextCmd(a), watchCmd(a), refreshCmd(a), promptCmd(a), statusCmd(a))
	add("acct", initCmd(a), accountsCmd(a), addCmd(a), renameCmd(a), removeCmd(a), pauseCmd(a), resumeCmd(a), billCmd(a), billsCmd(a))
	add("launch", launchCmd(a, "claude"), launchCmd(a, "codex"), cmdCmd(a), cursorSwitchCmd(a))
	add("integ", daemonCmd(a), alertsCmd(a), quietCmd(a), setupCmd(a))
	add("misc", configCmd(a), exportCmd(a), importCmd(a), resetCmd(a), doctorCmd(a), logCmd(a), privacyCmd(a), updateCmd(a), changelogCmd(a))
	root.AddCommand(hookCmd(a))
	for _, t := range helpTopics() {
		root.AddCommand(t)
	}
	return root
}

func versionString() string {
	v := Version
	if Commit != "" {
		v += " (" + Commit
		if Date != "" {
			v += ", " + Date
		}
		v += ")"
	}
	return v
}

// exactArgs wraps cobra.ExactArgs so wrong argument counts exit with 2.
func exactArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.ExactArgs(n)(cmd, args); err != nil {
			return usageErr{fmt.Errorf("%v\nRun '%s --help' for usage", err, cmd.CommandPath())}
		}
		return nil
	}
}

func maxArgs(n int) cobra.PositionalArgs {
	return func(cmd *cobra.Command, args []string) error {
		if err := cobra.MaximumNArgs(n)(cmd, args); err != nil {
			return usageErr{fmt.Errorf("%v\nRun '%s --help' for usage", err, cmd.CommandPath())}
		}
		return nil
	}
}
