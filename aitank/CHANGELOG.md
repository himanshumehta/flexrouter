# aitank changelog

## 0.1.0 (unreleased)

First version: a terminal-only macOS tracker for AI plan usage.

- `aitank init` finds signed-in Claude Code, Codex, Cursor, Kilo CLI and GitHub CLI sessions without any network request.
- Readers for Claude Pro/Max, ChatGPT/Codex, Cursor, GitHub Copilot, Kilo Code, Ollama Cloud (experimental), OpenRouter, DeepSeek, Moonshot/Kimi, xAI, and Anthropic/OpenAI organisation spend.
- "Use next" recommendations from the tightest window, time to reset and the last hour's burn rate, with warnings when a limit would run out within an hour.
- `list`, `next`, `watch` (TUI), `prompt`, and `status` for Claude Code's statusLine.
- Launch Claude Code or Codex in each account's own profile folder; switch Cursor's Claude panel.
- launchd agent, opt-in alerts through Notification Center, bills with renewal reminders.
- Pasted keys live only in the macOS Keychain.
