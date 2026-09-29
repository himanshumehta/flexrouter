# aitank

How much is left on each AI plan you pay for, and which account to use next,
from the terminal. aitank is a terminal-only macOS rebuild of the subtank
menu-bar app: a CLI, a full-screen dashboard and a background launchd agent.

```
$ aitank
Next: claude/work  62% left · 5-hour resets in 2h 54m

$ aitank list --compact
▶ claude/work            62% left  5h 38% 2h 54m  wk 21% 4d 6h  wk·opus 9% 4d 6h
  claude/personal        3% left   5h 97% 41m  wk 64% 2d 1h
  codex/me               71% left  5h 29% 3h 10m  wk 12% 5d 2h
  openrouter/team        —         bal $41.20
```

It reads Claude Pro/Max, ChatGPT/Codex, Cursor, GitHub Copilot, Kilo Code,
Ollama Cloud (experimental), OpenRouter, DeepSeek, Moonshot/Kimi, xAI and
Anthropic/OpenAI organisation spend.

## Install

Homebrew (once the tap is published, see [docs/RELEASING.md](docs/RELEASING.md)):

```
brew install himanshumehta/tap/aitank
```

Direct download: the signed, notarized universal binary is attached to each
`aitank-v*` release. From source (Go 1.24+):

```
go install github.com/himanshumehta/flexrouter/aitank/cmd/aitank@latest
```

Shell completions: `aitank completion zsh|bash|fish` (Homebrew installs them).

## Quick start

```
aitank init                      # find signed-in tools (read-only, offline), tick the ones to track
aitank add openrouter            # paste a key; it goes to the macOS Keychain only
aitank refresh                   # read everything once
aitank daemon install            # refresh every 5 minutes in the background
aitank setup claude-code --apply # Claude Code status line + limit-hit notification
aitank watch                     # dashboard: ↑↓/jk, Enter launch, c copy, r refresh, p pause, q quit
aitank claude                    # start Claude Code on the account with most room
```

A second Claude or Codex account gets its own profile folder:

```
aitank add claude --new --nickname work
aitank claude work               # then /login once inside Claude Code
aitank cmd work --copy           # CLAUDE_CONFIG_DIR='…/profiles/claude-2' claude
```

`aitank help setup`, `aitank help providers`, `aitank help exit-codes` and
`aitank help json` cover the rest, and every command has `--help` with
examples.

## Commands

| Area | Commands |
|---|---|
| View | `aitank`, `list [--compact]`, `next [--provider claude\|codex]`, `watch`, `refresh [id]`, `prompt`, `status` |
| Accounts | `init [--dry-run]`, `accounts`, `add <provider>`, `rename`, `remove`, `pause`, `resume`, `bill add\|remove`, `bills` |
| Launch | `claude [id] [-- args]`, `codex [id] [-- args]`, `cmd <id> [--copy]`, `cursor-switch <id>` |
| Background | `daemon install\|uninstall\|status\|logs`, `alerts [on\|off] [--at 20%]`, `quiet <id>`, `setup claude-code [--apply]` |
| Maintenance | `config get\|set\|path`, `export`, `import`, `reset`, `doctor`, `log`, `privacy`, `update`, `changelog`, `--version` |

All read commands take `--json` (versioned envelope, schema `aitank/v1`) and
`--no-color`; `NO_COLOR` and non-terminal output turn colour off.

## How it reads each account

| Provider | Reads through | What it shows |
|---|---|---|
| Claude Pro/Max | Claude Code's stored sign-in (read in place) and Anthropic's usage endpoint; also the limits Claude Code passes to `aitank status` | 5-hour, weekly, per-model weekly, usage credits |
| ChatGPT/Codex | `codex app-server` in the account's `CODEX_HOME` | 5-hour and weekly windows, credits, workspace limit, paused usage, saved resets |
| Cursor | Cursor app session in `state.vscdb` (read-only) | included usage, auto/API pools, on-demand spend, billing cycle |
| Copilot | `gh auth token`, one request per read | premium requests, chat/completions (free tier), overage |
| Kilo Code | Kilo CLI sign-in or a pasted key | credit balance, Kilo Pass, next billing date |
| OpenRouter | pasted key | balance (management key) or per-key spend and limit |
| DeepSeek, Moonshot/Kimi | pasted key | balance |
| xAI | pasted key | key check only; usage shows "—" |
| Anthropic API, OpenAI API | pasted admin key | month-to-date organisation spend |
| Ollama Cloud (experimental) | pasted key | key check only; limits show "—" (Ollama has no usage API) |

`aitank privacy` lists every file read, command run and endpoint contacted per
provider. The Claude and Codex readers use interfaces their vendors do not
document; each read checks the vendor CLI version against the range it was
built for ([docs/VENDORS.md](docs/VENDORS.md) has the details and sources).

## Honesty rules

- Unknown values are `—`, never 0.
- A failed read keeps the last good numbers and records the error; data over
  15 minutes old is marked stale with its age and left out of "use next".
- Alerts are off until you turn them on. There is no account, sign-up or
  telemetry. The only request to aitank's own project is the optional daily
  update check (`aitank config set update.policy off` to disable).
- aitank never renews, copies or exports a vendor sign-in. Launching an
  account only sets `CLAUDE_CONFIG_DIR` or `CODEX_HOME`.

## Files

Everything lives in `~/Library/Application Support/aitank/` with `0600`
files: `config.toml` (settings, accounts, bills; never secrets), `cache.json`,
`history.jsonl` (48 hours of readings for burn-rate forecasts),
`reads.jsonl` (read log, redacted), `state.json`, `agent.log`, `backups/` and
`profiles/`. Pasted keys are Keychain items with service `aitank`.

## Exit codes

`0` OK · `1` error · `2` usage error · `3` no usable account · `4` stale data
(`list`) · `5` a read failed (`refresh`).

## Development

```
cd aitank
go test ./...
go vet ./...
GOOS=darwin GOARCH=arm64 go build ./cmd/aitank
```

Set `AITANK_HOME` to a temporary folder to keep experiments away from your
real data. [docs/PLAN.md](docs/PLAN.md) maps every functional requirement to
code and says what still needs a Mac to verify.
