# aitank implementation plan

aitank is a terminal-only macOS rebuild of subtank: a CLI, a TUI dashboard and a
launchd agent that show how much usage is left on each AI plan and recommend
which account to use next. The functional requirements (FR-x.y) live in the
project thread; this file maps them to milestones and code.

Language: Go (single static binary, cross-compiles to darwin/arm64 and
darwin/amd64, starts fast enough for the 50 ms statusLine budget).
Location: `aitank/` inside the flexrouter repo until it gets its own repo.

## Milestones

| # | Milestone | FRs | Code |
|---|-----------|-----|------|
| M1 | Skeleton: paths (0600/0700 under `~/Library/Application Support/aitank`), TOML config, cache, history, read log, data model, `--json` schema v1, colours/NO_COLOR, countdowns | 1.1, 1.2, 5.x, 9.3–9.6, 15.5, 17.1–17.2 | `internal/paths`, `config`, `model`, `state`, `render` |
| M2 | Provider interface + registry, discovery (`init`, `init --dry-run`), account management (`add`, `accounts`, `rename`, `remove`, `pause`, `resume`), Keychain secrets | 2.x, 3.x, 4.11–4.12, 15.1–15.4 | `internal/provider`, `secrets`, `cli` |
| M3 | Provider readers: Claude, Codex, Cursor, Copilot, Ollama (experimental), Kilo, OpenRouter, DeepSeek, Moonshot/Kimi, xAI, Anthropic Admin, OpenAI Admin; version gates for Claude Code and Codex | 4.1–4.10 | `internal/provider/*` |
| M4 | Refresh engine with backoff and rate-limit respect, stale marking, last-good retention; burn-rate history and forecasts; recommendation engine | 6.3–6.8, 7.x, 8.x | `engine`, `forecast`, `recommend` |
| M5 | Display: `aitank`, `list`, `--compact`, `next`, `prompt`; TUI `watch` | 9.x, 10.x, 20.2 | `cli`, `tui` |
| M6 | Launching and switching: profile folders, `claude`, `codex`, `cmd --copy`, `cursor-switch` | 11.x | `launch` |
| M7 | Claude Code integration: `status` (cache-only, ingests statusLine rate limits), `setup claude-code [--apply]`, limit-hit hook | 12.x | `claudecode` |
| M8 | launchd agent, alerts (osascript), quiet, bills and renewal reminders | 6.1–6.2, 13.x, 14.x | `daemon`, `alerts`, `bills` |
| M9 | Diagnostics and housekeeping: `doctor`, `log`, `privacy`, `config get/set`, `export/import`, `reset`, exit codes | 16.3, 17.3–17.4, 18.x | `doctor`, `cli` |
| M10 | Distribution: `--version`, `changelog`, `update` (ed25519-verified, defers to Homebrew), completions, help topics, release workflow (universal binary, sign + notarize, signed checksums), Homebrew formula, CI | 1.3, 19.x, 20.1, 20.3 | `update`, `tools/relsign`, `packaging/`, `.github/workflows/aitank-*.yml` |

## What can and cannot be verified in the Linux build container

Verified here: everything that is plain Go logic (normalisation, scoring,
forecasting, alerts de-duplication, bills maths, config, cache, JSON schema,
TUI rendering, HTTP readers against recorded/fake servers, JSON-RPC client
against a fake `codex app-server`), plus a cross-compile for darwin/arm64 and
darwin/amd64.

Needs a Mac: Keychain (`security`), launchd (`launchctl`), notifications
(`osascript`), `pbcopy`, reading real Claude Code / Codex / Cursor / gh
sign-ins, and each vendor's live response shape.

Needs Apple developer credentials: code signing and notarization in
`.github/workflows/aitank-release.yml` (secrets listed in `docs/RELEASING.md`).

## Status by requirement

✅ built and tested here · 🍎 built, needs a Mac to verify · ⚠️ built with a
limitation · ❌ not built

| FR | Status | Notes |
|---|---|---|
| 1.1 macOS 13+, arm64 + Intel, one binary | 🍎 | Pure Go, no cgo; universal binary via `lipo` in the release workflow |
| 1.2 no GUI | ✅ | |
| 1.3 Homebrew + signed, notarized download | 🍎 | Release workflow signs, notarizes and updates a tap (`brew install himanshumehta/tap/aitank`); needs the secrets in RELEASING.md. homebrew-core needs a separate submission |
| 1.4 offline cached data | ✅ | Display commands read only local files |
| 2.1–2.5 discovery | ✅ | `init`, `init --dry-run`, `--json`; read-only (Cursor uses the system `sqlite3 -readonly`) |
| 3.1, 3.3–3.6 accounts | ✅ | `accounts`, `rename`, `remove`, `pause`/`resume`; multiple Claude/Codex accounts each bound to a profile folder |
| 3.2 add by sign-in, key or device code | ⚠️ | Local sign-in and pasted key done. No provider has a device-code flow yet: Kilo's device endpoints are undocumented (docs/VENDORS.md §5), so `--device` says so |
| 4.1 Claude | 🍎 | Stored sign-in (Keychain, read in place) + usage endpoint; per-model limits from both response shapes; never renews. First read shows a Keychain permission prompt |
| 4.2 Codex | 🍎 | `codex app-server` JSON-RPC; tested against a fake app-server |
| 4.3 Cursor | 🍎 | state.vscdb session + usage-summary; tested with fixtures |
| 4.4 Copilot | ⚠️ | Uses the gh token as specified; GitHub may reject gh tokens for the internal Copilot endpoint (unverified), which shows as "auth needed" with the reason |
| 4.5 Ollama (experimental) | ⚠️ | Ollama has no usage API for keys: key check only, limits shown as "—" |
| 4.6 Kilo | 🍎 | Kilo CLI sign-in or pasted key; tRPC endpoint is internal to kilo.ai |
| 4.7–4.10 key-based providers | ✅ | xAI's key-check endpoint is from memory (docs/VENDORS.md §8) |
| 4.11–4.12 pluggable providers | ✅ | `provider.Provider` interface; each provider is a package registered in `providers/all` |
| 5.1–5.5 data model | ✅ | Nulls render as "—"; resets come only from vendor timestamps |
| 6.1–6.2 launchd agent | 🍎 | `daemon install/uninstall/status/logs`; plist generation tested here |
| 6.3–6.8 cache, stale, backoff | ✅ | Backoff doubles per failure up to 1h; vendor Retry-After always respected |
| 7.1–7.6 recommendations | ✅ | Score = left% + up to 10 for capacity resetting within 24h − up to 20 when the pace would empty it before reset; close scores prefer the longer-lasting account |
| 8.1–8.4 forecasts | ✅ | 48h history, 60-minute least-squares pace, warning only within the hour and only while readings keep coming |
| 9.1–9.7 CLI output | ✅ | Colour thresholds configurable; `--json` envelope `aitank/v1` |
| 10.1–10.4 TUI | 🍎 | Frame rendering tested at several sizes; key handling and terminal compatibility need a real terminal |
| 11.1–11.4, 11.6–11.7 launching | 🍎 | Sets only `CLAUDE_CONFIG_DIR` / `CODEX_HOME`; `pbcopy` is macOS-only |
| 11.5 cursor-switch | 🍎 | Writes `claudeCode.environmentVariables`; the value format is inferred from the extension's conventions |
| 12.1–12.4 Claude Code status line | ✅ | `aitank status` ran in ~6 ms here; ingests `rate_limits` from stdin |
| 12.5 limit-hit hook | 🍎 | `StopFailure` hook with matcher `rate_limit`; that a plan limit arrives as `rate_limit` is inferred |
| 13.1–13.5 alerts | 🍎 | Once-per-cycle logic tested; delivery uses `osascript` |
| 14.1–14.3 bills | ✅ | Month-end clamping, per-currency monthly totals, reminders via the agent |
| 15.1 Keychain | 🍎 | `security -i` with the key on stdin, never argv |
| 15.2–15.5 secret handling | ✅ | Keys masked, errors redacted before logging, files 0600 in a 0700 folder |
| 16.1–16.3 privacy | ✅ | `aitank privacy` lists files, commands and endpoints per provider |
| 17.1–17.4 config | ✅ | TOML, `config get/set` with validation, `export/import`, `reset` |
| 18.1–18.4 diagnostics | ✅ | Statuses, `doctor`, `log`, documented exit codes |
| 19.1–19.3 updates | ⚠️ | ed25519-signed checksums + codesign check; defers to Homebrew. Installing needs a release built with the signing key |
| 20.1–20.3 shell | ✅ | Completions for zsh/bash/fish, `prompt` (with `--shell zsh`), help topics |
