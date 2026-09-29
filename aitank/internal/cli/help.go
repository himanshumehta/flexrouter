package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/himanshumehta/flexrouter/aitank/internal/provider"
)

// helpTopics are `aitank help <topic>` pages (FR-20.3). Cobra lists
// commands without a Run as additional help topics.
func helpTopics() []*cobra.Command {
	return []*cobra.Command{
		{Use: "providers", Short: "Supported providers and how each is read", Long: providersHelp()},
		{Use: "setup", Short: "Getting started, step by step", Long: setupHelp},
		{Use: "exit-codes", Short: "What aitank's exit codes mean", Long: exitCodesHelp},
		{Use: "json", Short: "The --json schema", Long: jsonHelp},
	}
}

func providersHelp() string {
	var b strings.Builder
	b.WriteString("Providers (add with `aitank add <id>`):\n\n")
	for _, p := range provider.All() {
		c := p.Capabilities()
		var auth []string
		for _, m := range c.Auth {
			auth = append(auth, map[provider.AuthMethod]string{
				provider.AuthLocal: "local sign-in", provider.AuthKey: "pasted key", provider.AuthDevice: "device code",
			}[m])
		}
		fmt.Fprintf(&b, "  %-14s %s — %s\n", p.ID(), p.Name(), strings.Join(auth, " or "))
		if c.KeyHelp != "" {
			fmt.Fprintf(&b, "  %-14s key: %s\n", "", c.KeyHelp)
		}
		if c.VendorTool != "" {
			tested := c.TestedMax
			if c.MinVersion != "" {
				tested = c.MinVersion + " to " + tested
			}
			fmt.Fprintf(&b, "  %-14s reads through `%s`; checked against versions %s\n", "", c.VendorTool, tested)
		}
	}
	b.WriteString(`
Claude and Codex readers use interfaces the vendors do not document. Each
read checks the vendor CLI's version; older than supported shows
"unsupported", newer than tested still reads but notes it. Set
allow_untested_vendor_versions = true to skip the minimum check.

'aitank privacy' lists every file and endpoint per provider.`)
	return b.String()
}

const setupHelp = `Getting started:

  1. aitank init                 find signed-in tools, tick the ones to track
  2. aitank add openrouter       add API-key accounts (key goes to Keychain)
  3. aitank refresh              read everything once
  4. aitank daemon install       keep the cache fresh every 5 minutes
  5. aitank setup claude-code --apply
                                 status line + limit-hit notification

Then:
  aitank                         one-line summary
  aitank list                    all accounts
  aitank watch                   dashboard (Enter launches, c copies, q quits)
  aitank claude                  Claude Code on the account with most room

Second Claude or Codex account:
  aitank add claude --new --nickname work
  aitank claude work             then /login once inside Claude Code

Alerts are off until you turn them on:
  aitank alerts on --at 20%

Shell prompt (Starship): add a [custom.aitank] module running 'aitank prompt'.`

const exitCodesHelp = `Exit codes:

  0  OK
  1  error (bad config, failed command, cancelled confirmation, doctor found a problem)
  2  usage error (unknown flag, wrong arguments)
  3  no usable account to recommend (aitank, next, claude/codex without an id)
  4  stale data (list: some account's data is older than stale_after)
  5  read errors (refresh: one or more accounts could not be read)`

const jsonHelp = `Every --json document has the same envelope:

  {"schema": "aitank/v1", "generated_at": "<RFC 3339>", "kind": "<command>", "data": ...}

Within schema aitank/v1 fields are only ever added, never renamed or removed.
Unknown values are null, never 0. Times are RFC 3339. Percentages are 0-100.
Keys and tokens never appear in JSON output.

'aitank list --json' data: {refreshed_at, accounts[], recommendation}
  account: id, provider, nickname, plan, identity, status, paused, left_pct,
           binding_window, windows[], balances[], spend, fetched_at,
           age_seconds, stale, source, error, forecasts[], use_next
  window:  kind (five_hour|weekly|model_weekly|monthly|billing_cycle|prepaid),
           name, model, used_pct, used, limit, unit, resets_at`
