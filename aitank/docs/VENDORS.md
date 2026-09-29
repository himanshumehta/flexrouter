# aitank vendor research: how to read AI plan usage on macOS

Researched 2026-09-29. Tags:
- **VERIFIED**: seen in primary source. The source is cited.
- **INFERRED**: from memory, or from a secondary report I did not confirm. Test it before relying on it.

Primary sources used:
- **CodexBar** (steipete/CodexBar), commit `25bba9b` (2026-09-28). Cloned; docs and Swift sources read.
- **openai/codex**, commit `a7660cd` (2026-09-29). Cloned; `codex-rs` protocol and tests read.
- **Claude Code 2.1.284 native binary** (`@anthropic-ai/claude-code-darwin-arm64`). Inspected with `strings`, so these facts come from the shipping code.
- **Official Claude Code docs**: https://code.claude.com/docs/en/statusline, /authentication and /vs-code.

Web research ended partway through. Sections 5–8 therefore rely mostly on CodexBar source and my own memory.

---

## 1. Claude Code (Pro/Max/Team/Enterprise)

### 1a. statusLine stdin JSON: VERIFIED (https://code.claude.com/docs/en/statusline)
Yes, it includes rate limits:
```json
"rate_limits": {
  "five_hour":  { "used_percentage": 23.5, "resets_at": 1738425600 },
  "seven_day":  { "used_percentage": 41.2, "resets_at": 1738857600 },
  "spend_limit":{ "used_percentage": 62.8, "resets_at": 1740787200 }
}
```
- `used_percentage` runs from 0 to 100. `resets_at` is **Unix epoch seconds**, not ISO.
- `spend_limit` appears only behind a "Claude apps gateway" and needs Claude Code v2.1.251 or later. It can go above 100.
- Conditions for `rate_limits` to be present:
  - **Only claude.ai Pro and Max subscribers** (or a gateway with a spend limit).
  - **Only after the first API response in the session.**
  - Each window can be absent independently. Claude Code drops a window once its `resets_at` has passed.
- Other fields: `session_id`, `session_name`, `prompt_id`, `transcript_path`, `cwd`, `model{id,display_name}`, `workspace{current_dir,project_dir,added_dirs,git_worktree,repo{host,owner,name}}`, `version`, `output_style.name`, `cost{total_cost_usd,total_duration_ms,total_api_duration_ms,total_lines_added,total_lines_removed}`, `context_window{total_input_tokens,total_output_tokens,context_window_size,used_percentage,remaining_percentage,current_usage{input_tokens,output_tokens,cache_creation_input_tokens,cache_read_input_tokens}}`, `exceeds_200k_tokens`, `prompt_cache{...}`, `fast_mode`, `effort.level`, `thinking.enabled`, `vim.mode`, `agent.name`, `pr{number,url,review_state,kind}`, `worktree{...}`.
- **No account email or identity is included.**
- The script runs on each new assistant message, and also when a rate-limit window reaches `resets_at`. An optional `refreshInterval` timer adds more runs.
- **Implication:** a statusLine "tee" script can capture a usage snapshot for free, but only while a session is active.

### 1b. OAuth usage endpoint: VERIFIED
Confirmed from CodexBar `Sources/CodexBarCore/Providers/Claude/ClaudeOAuth/ClaudeOAuthUsageFetcher.swift` and from the Claude Code 2.1.284 binary, which calls the same endpoint.

**Request**
```
GET https://api.anthropic.com/api/oauth/usage
Authorization: Bearer <claudeAiOauth.accessToken>      (sk-ant-oat01-...)
anthropic-beta: oauth-2025-04-20
Accept: application/json
Content-Type: application/json
User-Agent: claude-code/<version>        (CodexBar mimics this; fallback "claude-code/2.1.0")
```
- Claude Code itself also uses the variants `?at_wall=1&skip_spend=1` and `?cedar_ember=1&skip_spend=1`. VERIFIED from strings: `g6={plain:"/api/oauth/usage",at_wall:...,cedar_ember:...}`.
- **Scope:** the token needs the `user:profile` scope. A `claude setup-token` token (`CLAUDE_CODE_OAUTH_TOKEN`, scope `user:inference` only) **cannot** call this endpoint. VERIFIED (CodexBar docs/claude.md).
- **Rate limiting:** the endpoint returns 429 with `Retry-After` (seconds or an HTTP date) if polled too often. CodexBar keeps a per-token backoff gate. Poll gently, about every 1–5 minutes.
- **Status codes:** 401 means the token is expired; refresh it or run `claude`. 403 means the scope is missing, among other causes.

**Response** (CodexBar test fixtures; "real shape observed 2026-07-03")
```json
{
  "five_hour":  { "utilization": 11.0, "resets_at": "2026-07-03T00:30:00.282668+00:00" },
  "seven_day":  { "utilization": 9.0,  "resets_at": "2026-07-08T09:00:00.282694+00:00" },
  "seven_day_opus": null,
  "seven_day_sonnet": null,
  "seven_day_oauth_apps": null,
  "seven_day_routines": { "utilization": 18, "resets_at": "..." },
  "extra_usage": { "is_enabled": true, "monthly_limit": 2050, "used_credits": 325, "utilization": 15.8, "currency": "USD" },
  "limits": [
    { "kind": "session",       "group": "session", "percent": 11, "resets_at": "...", "scope": null, "is_active": true },
    { "kind": "weekly_all",    "group": "weekly",  "percent": 9,  "resets_at": "...", "scope": null, "is_active": false },
    { "kind": "weekly_scoped", "group": "weekly",  "percent": 5,  "resets_at": "...",
      "scope": { "model": { "id": null, "display_name": "Fable" }, "surface": null }, "is_active": false }
  ]
}
```
Field notes:
- `utilization` is a percent from 0 to 100 (a float).
- `resets_at` is an **ISO-8601 string**. The statusLine uses epoch seconds instead.
- `extra_usage.monthly_limit` and `used_credits` are in **cents**. CodexBar divides by 100.
- Windows can be `null` or absent. `five_hour` can be missing, in which case CodexBar promotes `seven_day`.
- **Newer accounts report model-scoped weekly caps in the `limits[]` array rather than in `seven_day_opus`/`seven_day_sonnet`.** Parse both forms.
- Other keys seen and ignored by CodexBar: `iguana_necktie`, `seven_day_design`, and alternate names `seven_day_cowork`, `claude_routines`, etc.

**Profile/identity endpoint** (VERIFIED, same file)
```
GET https://api.anthropic.com/api/oauth/profile
Authorization: Bearer <token>     (CodexBar sends no beta header here)
→ { "account": { "uuid": "...", "email_address"|"emailAddress"|"email": "..." },
    "organization": { "uuid": "..." , ...} }
```
CodexBar decodes this tolerantly. The exact extra fields are INFERRED; commonly seen ones are `account.display_name`, `account.has_claude_max`, `account.has_claude_pro`, `organization.organization_type`, `organization.rate_limit_tier`.

**Credential JSON** (VERIFIED: CodexBar `ClaudeOAuthCredentialModels.swift`, plus `{"claudeAiOauth": ...}` wrapper in `ClaudeOAuthCredentials.swift`)
```json
{ "claudeAiOauth": { "accessToken": "sk-ant-oat01-...", "refreshToken": "sk-ant-ort01-...",
                     "expiresAt": 1759999999999, "scopes": ["user:inference","user:profile",...],
                     "subscriptionType": "max", "rateLimitTier": "default_claude_max_20x" },
  "mcpOAuth": { ... } }
```
- `expiresAt` is in epoch **ms**. INFERRED from common knowledge; CodexBar decodes it to a Date.
- Plan: use `subscriptionType` (`pro`/`max`/`team`/`enterprise`) first, then `rateLimitTier` (`default_claude_max_5x` / `_20x` → "Max 5x"/"Max 20x"). VERIFIED (docs/claude.md).
- **Gotcha:** on 2.1.x the Keychain item can hold only `mcpOAuth` with no `claudeAiOauth`. Treat that as signed out. VERIFIED (docs/claude.md, #1844).
- Token refresh: CodexBar refreshes via a form-encoded POST. The endpoint is `https://console.anthropic.com/v1/oauth/token` (INFERRED; newer builds may use `platform.claude.com`), with client_id `9d1c250a-e61b-44d9-88ed-5944d1962f5e`. VERIFIED that this CLIENT_ID is in the binary's prod config. **Recommendation:** do not refresh. Claude Code rotates tokens and rewrites the item, so a third-party refresh can invalidate Claude Code's own refresh token. Let `claude` refresh, and treat 401 as "open Claude Code".

### 1c. Local identity and credentials

**Global config / identity file** (VERIFIED from binary: `Us(process.env.CLAUDE_CONFIG_DIR||homedir(), ".claude"+suffix+".json")`)
- Default location: `~/.claude.json`, which sits in HOME, **not** in `~/.claude/`.
- With `CLAUDE_CONFIG_DIR=X`: `X/.claude.json`.
- CodexBar also checks `<configRoot>/.config.json` first when it exists. VERIFIED (`ClaudeConfigPaths.accountConfigURL`).
- Relevant keys: `oauthAccount.{accountUuid, emailAddress, organizationUuid, organizationName, ...}`. VERIFIED from binary: code reads `o.emailAddress`, `o.accountUuid`, `o.organizationUuid`, and `authStatus` reads `C?.emailAddress`, `C?.organizationUuid`, `C?.organizationName`.
- Also in `oauthAccount`: `hasExtraUsageEnabled` (VERIFIED in binary), plus `displayName`, `organizationRole`, `billingType` (INFERRED).
- If no `oauthAccount` is present, the profile is signed out.

**Credentials** (VERIFIED: docs + binary)
- macOS: Keychain generic password.
  - Service: `Claude Code-credentials` (plus a suffix, see below).
  - Account attribute: `$USER` (falls back to `os.userInfo().username`; if that is not `[a-zA-Z0-9._-]+` it becomes `claude-code-user`).
  - Read it with: `security find-generic-password -s "<service>" -a "$USER" -w`. The first read triggers a Keychain ACL prompt.
- Fallback when the Keychain write fails (SSH, locked keychain), and always on Linux: `<configDir>/.credentials.json`, mode 0600. `<configDir>` is `~/.claude`, or `$CLAUDE_CONFIG_DIR` when that is set.

**Keychain service naming** (VERIFIED from the 2.1.284 binary, de-minified)
```js
function serviceName(n = "-credentials") {
  const sec = process.env.CLAUDE_SECURESTORAGE_CONFIG_DIR;
  const unsuffixed = sec !== undefined ? !sec : !process.env.CLAUDE_CONFIG_DIR;
  const dir = sec !== undefined ? sec.normalize("NFC") : configDir(); // configDir = CLAUDE_CONFIG_DIR || ~/.claude (NFC)
  const suffix = unsuffixed ? "" : "-" + sha256(dir).hex().substring(0, 8);
  return `Claude Code${OAUTH_FILE_SUFFIX}${n}${suffix}`;   // OAUTH_FILE_SUFFIX = "" in prod
}
```
- `CLAUDE_CONFIG_DIR` unset (and no `CLAUDE_SECURESTORAGE_CONFIG_DIR`): service is `Claude Code-credentials`.
- `CLAUDE_CONFIG_DIR` set to anything, **including `~/.claude` itself**: service is `Claude Code-credentials-<first 8 hex of sha256(dir)>`.
- **The hash input is the literal env string** after NFC normalization. It is not tilde-expanded, not realpath'd, and a trailing slash is kept, so a trailing `/` gives a different hash. Hash the exact value you export.
- `CLAUDE_SECURESTORAGE_CONFIG_DIR` overrides both the file location and the Keychain key. If it is set but empty, the service is unsuffixed.
- Third-party reports disagree about whether current builds suffix the default profile (quota-axi #170, openusage #423, claude-mem #4149). The binary code above answers this: **unsuffixed only when the var is unset.**
- Stale unsuffixed items can remain from older versions. Prefer the newest-modified item, as CodexBar does.
- For the dir → keychain mapping, use `claude auth status --json` (below): it prints `configDirectory`.

### 1d. Non-interactive CLI commands
- **`claude auth status [--json|--text]`**: JSON is the default. VERIFIED from the binary's commander definitions. Output shape:
  ```json
  { "loggedIn": true, "authMethod": "claude.ai", "apiProvider": "firstParty",
    "email": "...", "orgId": "...", "orgName": "...", "subscriptionType": "max",
    "analyticsDisabled": false, "projectsDirectory": "...", "configDirectory": "/Users/x/.claude" }
  ```
  - It can also include `forcedLoginMethod` and `apiKeySource`.
  - Exit code is 0 when logged in and 1 otherwise.
  - **No usage numbers.** It respects `CLAUDE_CONFIG_DIR`.
- **There is no subcommand that prints usage.** VERIFIED: the full `.command(...)` list in 2.1.284 has no usage command. Usage is only available through:
  - the interactive `/usage` slash command. CodexBar drives it in a PTY and scrapes "Current session" / "Current week". Its non-PTY fallback runs `claude --settings '{"remoteControlAtStartup":false}' /usage`.
  - the HTTP endpoint in 1b.
  - the statusLine in 1a.

### 1e. Hook fired on usage limit (VERIFIED from 2.1.284 binary hook schemas)
- **`StopFailure`** fires when the turn ends because of an API error. Its stdin carries the common fields plus:
  ```json
  { "session_id": "...", "transcript_path": "...", "cwd": "...", "prompt_id": "...(optional)",
    "permission_mode": "...(optional)", "agent_id": "...(optional)",
    "hook_event_name": "StopFailure",
    "error": "rate_limit",
    "error_details": "...(optional string)",
    "last_assistant_message": "...(optional)" }
  ```
- `error` enum: `authentication_failed | oauth_org_not_allowed | account_on_hold | verification_required | billing_error | rate_limit | overloaded | invalid_request | model_not_found | server_error | unknown | max_output_tokens | cloud_credential_error`.
- The hook matcher runs against the `error` value, so use `"matcher": "rate_limit"`.
- **INFERRED:** subscription usage-limit exhaustion (the "5-hour limit reached" case) arrives as `rate_limit`, with `error_details` holding the human message and reset time. Confirm this with a real hit.
- `StopFailure` is in the observe-only set, so its output is ignored.
- `Notification` (`message`, `title?`, `notification_type`) exists, but no usage-limit `notification_type` value was confirmed. `Stop` does not fire on API failure (INFERRED).

---

## 2. Codex (`codex app-server`)

### Launch
- CodexBar launches `codex -s read-only -a never app-server` with `CODEX_HOME` in the environment. VERIFIED (CodexBar docs/codex.md).
- Transport is JSON-RPC over stdio, newline-delimited.
- **Messages omit the `"jsonrpc":"2.0"` field.** VERIFIED from the serialization tests in `codex-rs/app-server-protocol/src/protocol/common.rs`. The server accepts messages without it.

### Handshake (VERIFIED: common.rs tests; v1.rs `InitializeResponse`)
```json
→ {"method":"initialize","id":1,"params":{"clientInfo":{"name":"aitank","title":"aitank","version":"0.1.0"}}}
← {"id":1,"result":{"userAgent":"...","codexHome":"/Users/x/.codex","platformFamily":"unix","platformOs":"macos"}}
→ {"method":"initialized"}                      (notification, no params, no id)
```
- `capabilities` is optional: `experimentalApi`, `optOutNotificationMethods`, etc.
- `account/read.workspaceRouting` needs `experimentalApi: true`.

### account/read (VERIFIED: v2/account.rs)
```json
→ {"method":"account/read","id":2,"params":{}}          (optional "refreshToken": true)
← {"id":2,"result":{"account":{"type":"chatgpt","email":"a@b.com","planType":"pro"},
                    "requiresOpenaiAuth":true}}
```
- `account` is a union tagged by `type`: `{"type":"apiKey"}`, `{"type":"chatgpt","email":string|null,"planType":PlanType}`, or `{"type":"amazonBedrock","usesCodexManagedCredentials":bool}`. It is `null` when signed out.
- `PlanType` values (snake_case): `free, go, plus, pro, pro_lite, pro_max, team, self_serve_business_prolite, self_serve_business_usage_based, business, ent26, enterprise_cbp_automation, enterprise_cbp_usage_based, enterprise, edu, edu_plus, edu_pro, ...`, plus unknown values. Keep the field a string.

### account/rateLimits/read (VERIFIED: v2/account.rs + `app-server/tests/suite/v2/rate_limits.rs`)
```json
→ {"method":"account/rateLimits/read","id":3}      (optional params {"excludeResetCreditDetails":true})
← {"id":3,"result":{
    "ordinaryUsageAllowed": true,
    "accountId": "account-123",
    "rateLimits": {
      "limitId":"codex","limitName":null,"normalModelSlug":null,
      "primary":   {"usedPercent":42,"windowDurationMins":300,"resetsAt":1735693200},
      "secondary": {"usedPercent":5, "windowDurationMins":10080,"resetsAt":1736200000},
      "credits":   {"hasCredits":true,"unlimited":false,"balance":"12.50"},
      "individualLimit": {"limit":"25000","used":"8000","remainingPercent":68,"resetsAt":...},
      "spendControlReached": false,
      "planType":"pro",
      "rateLimitReachedType": null
    },
    "rateLimitsByLimitId": { "codex": {...same shape...}, "codex_other": {...} },
    "rateLimitResetCredits": {"availableCount":3,"credits":[...]|null},
    "rateLimitUpsell": null
}}
```
- `usedPercent` is an integer (rounded).
- `windowDurationMins` and `resetsAt` (**epoch seconds**) are nullable.
- `balance` is a **string**.
- `primary` and `secondary` are both nullable. Typically primary is the 5 h window (300) and secondary the weekly window (10080). Use `windowDurationMins` rather than assuming.
- Other methods:
  - `account/usage/read` (token usage).
  - `account/rateLimits/updated` server notification: a sparse update to merge.
  - `account/updated` notification.
- The deprecated names (`getAccountRateLimits`) are gone from v2. The method is `account/rateLimits/read`.

### Underlying HTTP (alternative to spawning codex) (VERIFIED: CodexBar docs/codex.md, codex-rs backend-client and tests)
- `GET https://chatgpt.com/backend-api/wham/usage`
- Headers: `Authorization: Bearer <tokens.access_token>`, `chatgpt-account-id: <tokens.account_id>`.
- Raw snake_case response:
  - `plan_type`
  - `rate_limit{allowed,limit_reached,primary_window{used_percent,limit_window_seconds,reset_after_seconds,reset_at},secondary_window{...}}`
  - `credits{has_credits,unlimited,balance}`
  - `additional_rate_limits[{limit_name,metered_feature,rate_limit{...}}]`
  - `spend_control`
  - `rate_limit_reset_credits{available_count}`
- Custom or non-ChatGPT base URLs use `/api/codex/usage` instead.

### Local auth (VERIFIED: codex-rs/login/src/auth/storage.rs, token_data.rs)
- Location: `$CODEX_HOME/auth.json`, default `~/.codex/auth.json`.
- Shape: `{ "auth_mode": "chatgpt"|"apikey"|..., "OPENAI_API_KEY": null|"sk-...", "tokens": { "id_token": "<JWT>", "access_token": "<JWT>", "refresh_token": "...", "account_id": "..." }, "last_refresh": "<RFC3339>" }`.
- id_token claims to read:
  - `email`, falling back to `["https://api.openai.com/profile"].email`.
  - `["https://api.openai.com/auth"].chatgpt_plan_type`, `.chatgpt_account_id`, `.chatgpt_user_id`, `.user_id`.
- Credentials can instead live in the OS keyring when `cli_auth_credentials_store = "keyring"` (or `auto`) in `config.toml`. The keyring entry is:
  - service `"Codex Auth"`
  - account `cli|<first 16 hex of sha256(canonicalized CODEX_HOME path)>`
  - In that case auth.json may be absent. The app-server route handles both cases.
- CodexBar also falls back to legacy `~/.config/codex/auth.json` and OpenCode's `~/.local/share/opencode/auth.json` (VERIFIED, docs/codex.md).

---

## 3. Cursor

### Local session (VERIFIED: CodexBar docs/cursor.md, CursorAppAuth.swift)
- Database: `~/Library/Application Support/Cursor/User/globalStorage/state.vscdb`, SQLite in WAL mode with `-wal` and `-shm` sidecars.
  - Query: `SELECT value FROM ItemTable WHERE key = 'cursorAuth/accessToken'`.
  - Open it read-only. If it is idle in WAL mode with no sidecars, use `?immutable=1`.
  - The value can be UTF-16LE.
- The access token is a JWT. `sub` looks like `auth0|user_XXXX`; **userId is the part after the last `|`**. `exp` is honored, and CodexBar does not refresh.
- Other keys, INFERRED as common knowledge and not seen in CodexBar: `cursorAuth/refreshToken`, `cursorAuth/cachedEmail`, `cursorAuth/cachedSignUpType`, `cursorAuth/stripeMembershipType` (e.g. `free`/`pro`/`business`), `cursorAuth/stripeSubscriptionStatus`. Run `SELECT key FROM ItemTable WHERE key LIKE 'cursorAuth/%'` to confirm.

### Auth to cursor.com (VERIFIED: CursorAppAuth.swift `cookieHeader()`)
```
Cookie: WorkosCursorSessionToken=<userId>%3A%3A<accessToken>
```
- The `::` separator is URL-encoded.
- Browser cookies `WorkosCursorSessionToken`, `__Secure-next-auth.session-token` and `next-auth.session-token` also work.

### Endpoints (VERIFIED: docs/cursor.md, CursorStatusProbe.swift models)
- `GET https://cursor.com/api/usage-summary` → `{ billingCycleStart, billingCycleEnd, membershipType, limitType, isUnlimited, autoModelSelectedDisplayMessage, namedModelSelectedDisplayMessage, individualUsage{ plan{enabled,used,limit,remaining,breakdown{included,bonus,total},autoPercentUsed,apiPercentUsed,totalPercentUsed}, onDemand{enabled,used,limit,remaining}, overall{enabled,used,limit,remaining} }, teamUsage{ onDemand{...}, pooled{...} } }`. **All money values are in cents.**
- `GET https://cursor.com/api/auth/me` → `{ email, email_verified?, name, sub, created_at, updated_at, picture }`. CodexBar models the keys as `email, emailVerified, name, sub, createdAt, ...`.
- `GET https://cursor.com/api/usage?user=<userId>` (legacy request-based plans) → `{ "gpt-4": { numRequests, numRequestsTotal, numTokens, maxRequestUsage, maxTokenUsage }, ..., startOfMonth }`.
- `POST https://cursor.com/api/dashboard/get-sand-usage-status` (with `Origin: https://cursor.com`) → `usagePercent, nextResetTimestampUtc` (Grok Bot weekly).
- `POST https://cursor.com/api/dashboard/get-filtered-usage-events` (with `Origin`) → per-event cost (`tokenUsage.totalCents`, `totalUsageEventsCount`).
- Team: `/api/dashboard/teams`, `/api/dashboard/get-team-spend` (`overallSpendCents`, `effectivePerUserLimitDollars`, `monthlyLimitDollars`).
- `api2.cursor.sh` endpoints (e.g. `/auth/full_stripe_profile`, `/auth/usage`) are INFERRED and unverified. CodexBar does not use them.

### Cursor's "Claude panel" (Claude Code extension in Cursor)
- VERIFIED (https://code.claude.com/docs/en/vs-code, "Extension settings"): extension setting `claudeCode.environmentVariables`, default `[]`, "Set environment variables for the Claude process". Also `claudeCode.claudeProcessWrapper`, `claudeCode.useTerminal`, `claudeCode.disableLoginPrompt`.
- Cursor user settings file: `~/Library/Application Support/Cursor/User/settings.json` (INFERRED, standard VS Code-fork layout).
- Value format, INFERRED from the extension's package.json convention (verify):
  ```json
  "claudeCode.environmentVariables": [ { "name": "CLAUDE_CONFIG_DIR", "value": "/Users/x/.claude-work" } ]
  ```
  To "switch the panel", rewrite this entry and reload the window or restart the extension host.
- With `CLAUDE_CONFIG_DIR` set, the IDE lock file moves to `$CLAUDE_CONFIG_DIR/ide/<port>.lock`. VERIFIED (vs-code docs).
- Settings in `~/.claude/settings.json` `env` also apply to the extension, but they are global, so they are the wrong place for a per-panel switch.
- Remember that setting `CLAUDE_CONFIG_DIR`, **even to ~/.claude**, changes the Keychain service name (see 1c). Pointing the panel at `~/.claude` explicitly therefore signs it out of the default login.

---

## 4. GitHub Copilot (VERIFIED: CodexBar docs/copilot.md, CopilotUsageModels.swift + tests)

**Request**
```
GET https://api.github.com/copilot_internal/user          (GHE: https://api.<host>/copilot_internal/user)
Authorization: token <github oauth token>     (gho_/ghu_ from device flow, scope read:user)
Accept: application/json
Editor-Version: vscode/1.96.2
Editor-Plugin-Version: copilot-chat/0.26.7
User-Agent: GitHubCopilotChat/0.26.7
X-Github-Api-Version: 2025-04-01
```

**Paid-plan response**
```json
{ "copilot_plan": "individual", "assigned_date": "2025-01-01", "quota_reset_date": "2025-02-01",
  "token_based_billing": false,
  "quota_snapshots": {
    "premium_interactions": { "entitlement": 300, "remaining": 250, "percent_remaining": 83.3,
                              "quota_id": "premium_interactions", "unlimited": false,
                              "overage_count": 0, "overage_permitted": false, "credits_used": 0 },
    "chat":        { "entitlement": 0, "remaining": 0, "percent_remaining": 100, "unlimited": true, "quota_id":"chat" },
    "completions": { ...same shape... } } }
```
- `overage_count` and `overage_permitted` are INFERRED and not modeled by CodexBar. The other names are VERIFIED.

**Free tier** (VERIFIED from tests)
- There is no `quota_snapshots`. Instead: `"monthly_quotas": {"chat": 500, "completions": 4000}` (entitlement) and `"limited_user_quotas": {"chat": 125, "completions": 75}` (**remaining**). Numbers may be strings.
- Free-tier reset key: `limited_user_reset_date` (INFERRED).

**Gotchas**
- Business and token-billed seats return `entitlement: 0, remaining: 0, percent_remaining: 100`. That is a placeholder, not "0% used".
- Reset dates are often missing.

**Local token** (INFERRED)
- `~/.config/github-copilot/apps.json` (newer) or `hosts.json` (older): `{ "github.com:<client_id>": { "user": "...", "oauth_token": "gho_..." } }`.
- CodexBar does its own device flow instead: `POST https://github.com/login/device/code`, then poll `POST https://github.com/login/oauth/access_token` (VERIFIED). Client id Iv1.b507a08c87ecfe98 (the VS Code Copilot app) is INFERRED.
- `gh auth token` output generally does **not** work for `copilot_internal` (INFERRED).

---

## 5. Kilo Code / Kilo CLI

**VERIFIED** (CodexBar docs/kilo.md, KiloUsageFetcher.swift, KiloSettingsReader.swift)
- Kilo has been rebranded to **kilo.ai** from kilocode.ai.
- CLI auth file: `~/.local/share/kilo/auth.json`. The token is at `.kilo.access`. The CLI is an opencode fork, so the file is shaped like opencode auth.json: `{ "kilo": { "type": "oauth", "access": "...", "refresh": "...", "expires": ... } }`. The inner fields other than `access` are INFERRED.
- Env var: `KILO_API_KEY`.
- Usage comes from a tRPC batch:
  ```
  GET https://app.kilo.ai/api/trpc/user.getCreditBlocks,kiloPass.getState,user.getAutoTopUpPaymentMethod?batch=1&input={"0":{"json":null},"1":{"json":null},"2":{"json":null}}
  Authorization: Bearer <token>
  [X-KILOCODE-ORGANIZATIONID: <orgId>]   (optional org scope)
  ```
  - The response is an array `[{result:{data:{json:...}}}, ...]`.
  - Credit blocks have `amount_mUsd` and `balance_mUsd` (**micro-USD**, ÷1,000,000) and a `totalBalance_mUsd` total.
- Orgs: `user.getOrganizations` over tRPC.
- Profile: `GET https://api.kilo.ai/api/profile` (Bearer).

**INFERRED** (older Kilo Code VS Code extension)
- `GET https://api.kilocode.ai/api/profile` and `GET https://api.kilocode.ai/api/profile/balance`. The balance response is `{ balance: <USD number> }`; the profile response has `user.email` and `user.name`.
- The extension stores its token as `kilocodeToken` in VS Code secret storage or settings.
- Device-code endpoints for Kilo CLI were **not verified**. Plausibly `POST https://api.kilo.ai/api/device-auth/codes` and poll `GET .../device-auth/codes/<code>`. Treat these as unknown and reuse the CLI's auth.json instead.

---

## 6. Ollama Cloud (VERIFIED: CodexBar docs/ollama.md)
- **No documented usage or limits API with an API key.**
- An API key (`OLLAMA_API_KEY`, sent as `Authorization: Bearer`) can only be *validated*. CodexBar probes `https://ollama.com/api/web_search`, which needs auth, without doing a search. `https://ollama.com/api/tags` is public.
- Quota comes only from HTML scraping of `https://ollama.com/settings` with the browser session cookie (`wos-session`, WorkOS AuthKit, or legacy/NextAuth names):
  - "Included usage" `$7.50 of $60 used`.
  - Legacy "Session/Hourly/Weekly usage" percentages.
  - `data-time` attribute on "Resets in …" elements.
- For aitank: API-key mode yields only "key valid" plus plan-unknown.

---

## 7. OpenRouter (VERIFIED: CodexBar docs/openrouter.md, Plugins/openrouter.js; field names cross-checked with memory of the OpenRouter docs)
- **`GET https://openrouter.ai/api/v1/key`**, `Authorization: Bearer sk-or-v1-...`. **Any regular key works** and describes that key.
  ```json
  {"data":{"label":"sk-or-v1-abc...","limit":30,"limit_remaining":28.5,"limit_reset":"monthly"|null,
           "usage":1.5,"usage_daily":0.2,"usage_weekly":1.0,"usage_monthly":1.5,
           "byok_usage":0, "is_free_tier":false,"is_management_key":false,"is_provisioning_key":false}}
  ```
  - `limit` is a per-key spending cap in USD and is not the account balance. It is `null` when there is no cap.
  - `is_provisioning_key` and `byok_*` are INFERRED. `rate_limit{requests,interval}` is deprecated (INFERRED).
- **`GET https://openrouter.ai/api/v1/credits`** → `{"data":{"total_credits":5.0,"total_usage":3.1}}`, so balance = total_credits − total_usage.
  - Current OpenRouter docs say it **requires a Management (provisioning) key**. CodexBar tries it with the regular key and tolerates rejection ("when OpenRouter permits"). Treat access with a normal key as not guaranteed.
- `GET /api/v1/activity` (Management key only) returns the last 30 completed UTC days of activity.

---

## 8. Pay-as-you-go balance and cost APIs

### DeepSeek (VERIFIED: CodexBar docs/deepseek.md)
- `GET https://api.deepseek.com/user/balance`, with headers `Authorization: Bearer sk-...` and `Accept: application/json`.
- Response:
  ```json
  {"is_available":true,"balance_infos":[{"currency":"USD","total_balance":"50.00","granted_balance":"10.00","topped_up_balance":"40.00"}]}
  ```
  Amounts are strings. `currency` is `CNY` or `USD`.

### Moonshot / Kimi (VERIFIED: CodexBar docs/moonshot.md)
- `GET https://api.moonshot.ai/v1/users/me/balance` (international, **USD**).
- `GET https://api.moonshot.cn/v1/users/me/balance` (China, **CNY**).
- Keys are region-bound; do not send a .ai key to .cn.
- Header: `Authorization: Bearer <key>`.
- Response, with the envelope INFERRED:
  ```json
  {"code":0,"data":{"available_balance":49.58,"voucher_balance":46.58,"cash_balance":3.00},"scode":"0x0","status":true}
  ```
  `cash_balance` can be negative, meaning a deficit.
- Kimi For Coding / Kimi Code subscription quota is a separate surface; see CodexBar docs/kimi.md, not read in depth.

### xAI
- **Key validation** (INFERRED; not in CodexBar, from memory of the xAI docs): `GET https://api.x.ai/v1/api-key` with `Authorization: Bearer xai-...`. It returns `{ redacted_api_key, user_id, name, create_time, modify_time, modified_by, team_id, acls[], api_key_id, team_blocked, api_key_blocked, api_key_disabled }`. It shows no balance, but `team_id` is useful.
- **Balance** (VERIFIED, CodexBar docs/xai.md): requires a **Management API key** plus team ID.
  - `GET https://management-api.x.ai/v1/billing/teams/{team_id}/prepaid/balance`. The ledger is inverted and in string USD **cents**: a $10 top-up is `"-1000"`. The posted ledger lags live spend within a billing cycle.
  - `POST .../teams/{team_id}/usage` returns daily spend analytics.

### Anthropic Admin API cost report (VERIFIED: CodexBar ClaudeAdminAPIUsageFetcher.swift)
- Request:
  ```
  GET https://api.anthropic.com/v1/organizations/cost_report?starting_at=<RFC3339>&ending_at=<RFC3339>&bucket_width=1d&limit=<n>&group_by[]=description[&group_by[]=workspace_id]
  x-api-key: sk-ant-admin01-...
  anthropic-version: 2023-06-01
  ```
- Response: `{ "data":[ { "starting_at", "ending_at", "results":[ { "currency":"USD", "amount":"123.45", "description", "cost_type", "workspace_id", "model?", ... } ] } ], "has_more":bool, "next_page":string|null }`.
- **`amount` is a decimal string in cents** (lowest USD units): "123.45" means $1.2345.
- Paginate with `page=<next_page>`.
- `bucket_width` for cost_report accepts only `1d` (INFERRED).
- Companion endpoint: `/v1/organizations/usage_report/messages` with `group_by[]=model`. It returns `uncached_input_tokens`, `cache_creation{ephemeral_5m_input_tokens,ephemeral_1h_input_tokens}`, `cache_read_input_tokens`, `output_tokens`.
- Subscription (Pro/Max) usage is **not** included.

### OpenAI organization costs (VERIFIED: CodexBar docs/openai.md, Plugins/openai.js)
- Request:
  ```
  GET https://api.openai.com/v1/organization/costs?start_time=<unix s>&end_time=<unix s>&bucket_width=1d&limit=<≤31>&group_by=line_item[&project_ids=proj_..][&page=<next_page>]
  Authorization: Bearer <Admin key sk-admin-...>
  ```
- Response: `{ "object":"page", "data":[ { "object":"bucket", "start_time", "end_time", "results":[ { "object":"organization.costs.result", "amount":{"value":0.06,"currency":"usd"}, "line_item", "project_id" } ] } ], "has_more":bool, "next_page":string|null }`.
- `amount.value` is a **dollars float**, unlike Anthropic's cents.
- `limit` is at most 31 daily buckets per page (CodexBar chunks by 31).
- Needs an **organization Admin key**. Project and service-account keys are rejected.
- Companion: `/v1/organization/usage/completions` with `group_by=model`.

---

## Surprises / design notes for aitank
1. **Timestamp formats differ across Claude sources.** The statusLine gives `resets_at` in epoch **seconds**, while the OAuth endpoint gives an **ISO string** and calls the percent `utilization` (statusLine: `used_percentage`). Normalize both.
2. **Setting `CLAUDE_CONFIG_DIR` renames the Keychain item, even when it points at `~/.claude`.** The hash covers the literal, NFC-normalized, un-expanded string, so trailing slashes and `~` matter. `CLAUDE_SECURESTORAGE_CONFIG_DIR` overrides this.
3. **`~/.claude.json` lives in HOME** in the default profile, but inside the dir when `CLAUDE_CONFIG_DIR` is set.
4. **The Claude Keychain item can lack `claudeAiOauth`** (only `mcpOAuth`), and `setup-token` tokens lack the `user:profile` scope needed for usage.
5. **Claude model-scoped weekly caps moved into `limits[]`** (`kind: weekly_scoped`, `scope.model.display_name`). `seven_day_opus` and `seven_day_sonnet` are now often `null`.
6. **Codex app-server omits `"jsonrpc":"2.0"`** and needs an `initialized` notification after `initialize`. Rate-limit `balance` is a string, and `resetsAt` is in epoch seconds.
7. **Codex auth may live in the keyring**, not auth.json (service "Codex Auth", account `cli|sha256(path)[:16]`). Prefer app-server to cover both.
8. **Cursor needs no browser cookie.** Build `WorkosCursorSessionToken=<sub-after-|>%3A%3A<jwt>` from state.vscdb. Money fields are in cents.
9. **Copilot placeholders:** `entitlement 0 / remaining 0 / percent_remaining 100` is a placeholder. The free tier uses a different shape (`monthly_quotas` plus `limited_user_quotas`, where the latter is *remaining*).
10. **Units vary by vendor:**
    - Anthropic Admin `amount`: cents, as a string.
    - OpenAI `amount.value`: dollars, as a float.
    - xAI balance: negated cents, as a string.
    - Kilo: micro-USD.
    - DeepSeek: strings.
    - Moonshot: currency depends on the host.
11. **Claude usage-limit hook:** `StopFailure` with `error: "rate_limit"`. Mapping the subscription cap to that value is INFERRED.
12. **Ollama Cloud has no usage API.** A key can only be validated.
