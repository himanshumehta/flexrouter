# Setting up flexrouter on a local machine

This file is written for a coding agent (opencode, Claude Code, Aider, …)
running on the owner's machine, and for the owner reading along. Follow the
steps in order. Each step ends with a check; do not move on until it passes.

## Rules for the agent

- **Never write an API key into `config.yaml`**, a script, a shell profile or
  any file in a repository. Keys go in only through `flexrouter keys add`.
- **Ask the owner for each key.** Do not invent, reuse or search for keys.
  Let them type the key themselves at the hidden prompt
  (`flexrouter keys add <provider>` with no `--secret`).
- flexrouter never rewrites `config.yaml`. Editing it by hand is the normal
  way to set it up, and comments in it are kept.
- Do not delete anything in the flexrouter home (below) without asking.

## 1. Install

Needs Python 3.11 or newer. flexrouter is not on PyPI yet, so it is installed
from GitHub.

```bash
python3 --version                 # must be 3.11+
python3 -m venv ~/.flexrouter-venv
~/.flexrouter-venv/bin/pip install git+https://github.com/himanshumehta/flexrouter.git
```

On Windows use `py -3.11 -m venv %USERPROFILE%\.flexrouter-venv` and
`%USERPROFILE%\.flexrouter-venv\Scripts\pip`.

Put the venv's `bin` (or `Scripts`) folder on `PATH`, or call `flexrouter` by
its full path in the steps below.

**Check:** `flexrouter doctor` prints a box headed "flexrouter doctor".

## 2. Find the flexrouter home

`flexrouter doctor` prints where everything lives. By default:

| OS | Folder |
|---|---|
| macOS / Linux | `~/.config/flexrouter` |
| Windows | `%LOCALAPPDATA%\flexrouter` |

Set `FLEXROUTER_HOME` to use another folder. Inside it:

| File | What it is |
|---|---|
| `config.yaml` | Providers, buckets and settings. Written by hand. |
| `keys.json` | API keys, owner-only permissions. Written by `flexrouter keys`. |
| `overrides.json` | Changes made from the dashboard, layered on top of `config.yaml`. |
| `state/` | Request log, statuses, spend. |

**Check:** `flexrouter doctor` shows the folder, and `config.yaml` exists in it
(the first run writes a starter one).

## 3. Choose providers

Built-in presets (base URLs are already known to flexrouter):

| Provider | `base_url` | Free tier |
|---|---|---|
| groq | `https://api.groq.com/openai/v1` | yes |
| cerebras | `https://api.cerebras.ai/v1` | yes |
| googleai | `https://generativelanguage.googleapis.com/v1beta/openai/` | yes |
| openrouter | `https://openrouter.ai/api/v1` | yes (`:free` models) |
| mistral | `https://api.mistral.ai/v1` | yes |
| nvidia | `https://integrate.api.nvidia.com/v1` | yes |
| llm7 | `https://api.llm7.io/v1` | yes |
| ollama | `http://localhost:11434/v1` | local, no key |
| deepseek | `https://api.deepseek.com/v1` | paid |
| siliconflow | `https://api.siliconflow.cn/v1` | paid |
| sambanova | `https://api.sambanova.ai/v1` | paid |

Any other OpenAI-compatible endpoint works too: give it a name and its
`base_url`.

## 4. Save the keys

For each provider except local ones such as Ollama, ask the owner to run:

```bash
flexrouter keys add groq          # prompts for the key without showing it
```

A second key for the same provider is added the same way; flexrouter rotates
to it when the first is rate-limited.

**Check:** `flexrouter keys list` shows each key masked (`…abcd`), and
`flexrouter doctor` shows "saved key" next to each provider.

## 5. Write `config.yaml`

A bucket is a named list of models. Apps ask for a bucket name, and
flexrouter picks the best available model in it. The highest `score` wins,
chosen at random among models within 20% of the best. When one model fails
or is rate-limited, flexrouter moves on to the next one.

```yaml
providers:
  groq:
    base_url: https://api.groq.com/openai/v1
  cerebras:
    base_url: https://api.cerebras.ai/v1
  ollama:
    base_url: http://localhost:11434/v1

buckets:
  main:
    - provider: groq
      model: llama-3.3-70b-versatile
      score: 90
      rpm: 30
      tpm: 12000
      context_window: 131072
    - provider: cerebras
      model: gpt-oss-120b
      score: 85
      rpm: 30
      tpm: 60000
      context_window: 131072
    - provider: ollama
      model: llama3.2
      score: 40
      rpm: null            # null = unknown, learned from the provider
      tpm: null
      context_window: 8192

settings:
  port: 4891
  failover_budget_seconds: 30
  needs_you_recheck_minutes: 30   # retry used-up models every 30 min; 0 = never
  hooks:
    - detect_vision        # route image messages only to vision: true models
    - estimate_tokens      # skip models whose context_window is too small
```

- `model` must be the provider's exact model ID. Check it against the
  provider's model list (`GET <base_url>/models`) instead of guessing.
- Leave out the `hooks` if you don't need them. Without `estimate_tokens`,
  models that are too small for a long prompt are **not** skipped.
- Buckets never spill into each other.
- Every field is described in `README.md` under "Config reference".

**Check:** `flexrouter doctor` reports the right number of buckets, models and
providers, with no errors.

## 6. Start the service

```bash
flexrouter serve                 # API and dashboard on http://localhost:4891
flexrouter dashboard             # same, and opens the browser
```

It binds to `127.0.0.1` only. Leave it running in its own terminal, or run it
as a login service.

**Check:**

```bash
curl -s http://localhost:4891/v1/models
curl -s http://localhost:4891/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"main","messages":[{"role":"user","content":"Say hi"}]}'
```

The first lists your buckets and models. The second returns a normal OpenAI
chat completion. On failure, `error.flexrouter.attempts[]` lists every model
tried and what its provider said.

## 7. Point apps at it

Any OpenAI-compatible client works:

- Base URL: `http://localhost:4891/v1`
- Model: a bucket name (`main`), `auto` (the best bucket), or
  `provider/model` to pin one exact model with no failover
- API key: any value, unless an app password is set (see below)

Python:

```python
from openai import OpenAI
client = OpenAI(base_url="http://localhost:4891/v1", api_key="x")
print(client.chat.completions.create(
    model="main", messages=[{"role": "user", "content": "hi"}]
).choices[0].message.content)
```

opencode (`opencode.json` in a project, or `~/.config/opencode/opencode.json`):

```json
{
  "provider": {
    "flexrouter": {
      "npm": "@ai-sdk/openai-compatible",
      "options": { "baseURL": "http://localhost:4891/v1", "apiKey": "x" },
      "models": { "main": {} }
    }
  }
}
```

Then select `flexrouter/main` with `/models`. Coding agents call tools, so the
bucket you use for them should hold only models that support tool calling.

## 8. Optional: require a key on /v1

By default any program on the machine can call `/v1`. To require a key,
generate an app password on the dashboard's Settings page, or set
`auth_token` under `settings:`. Clients then send
`Authorization: Bearer <that value>`, which is the `apiKey` above.

## Everyday commands

| Command | Does |
|---|---|
| `flexrouter serve` | Start the service |
| `flexrouter dashboard` | Start it and open the dashboard |
| `flexrouter tui` | Live terminal view: overview, keys, requests |
| `flexrouter doctor` | Where things live, which key each provider uses |
| `flexrouter status` | Totals and today's spend |
| `flexrouter keys add/list/rm` | Manage keys |
| `flexrouter refresh` | Check providers for model and limit changes |
| `flexrouter config reset` | Undo dashboard changes that broke loading |

## Troubleshooting

| Symptom | Fix |
|---|---|
| `flexrouter isn't running` | Start `flexrouter serve`. |
| `There is no bucket named 'x'` | Use a name from `GET /v1/models`, or add the bucket to `config.yaml`. |
| A preferred model ran out of credit or quota | Nothing to do: it is tried again every `needs_you_recheck_minutes` (30) and used again as soon as it answers. |
| A model is "Needs you" / 401 | The key was rejected. Re-add it with `flexrouter keys add <provider>`, then press Retry on the dashboard's Status page. |
| 503 "busy for longer than the failover budget" | Every model in the bucket is rate-limited. Add models or keys, or raise `failover_budget_seconds`. |
| Long prompts fail on small models | Add the `estimate_tokens` hook (step 5). |
| Port 4891 is taken | `flexrouter serve --port 4892`, or set `settings.port`. |
| Dashboard or `/api` returns 403 | It only accepts requests from its own pages. Open it at `http://localhost:4891`, not through another site. |
