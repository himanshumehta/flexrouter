# flexrouter: remote free-model setup

This machine uses flexrouter from this repository. It is configured for
remote free-tier models, not Ollama or other local inference.

## Internet-only and keyless operation

The default high-priority routes use LLM7's internet-hosted turbo pool. A
keyless request was verified successfully on 2026-10-06; MiniMax answered and
the recorded cost was `$0.0000`. This runs remotely—nothing is downloaded or
inferred on this Mac.

LLM7's current documentation asks for a token, so anonymous access could
change. If it does, create a free token and save it with
`flexrouter keys add llm7`. OpenRouter always requires its own free key. A
provider token is authentication, not a payment method; never put one in this
repository or in `config.yaml`.

## Installed commands

The Python environment is `.venv/`. After installation, `flexrouter` and
`aitank` are also available through `~/.local/bin`.

To rebuild the Python environment:

```bash
cd /Users/himanshu/sai/hai/flexrouter
python3 -m venv --clear .venv
.venv/bin/pip install -e .
```

To rebuild aitank:

```bash
cd /Users/himanshu/sai/hai/flexrouter/aitank
GOCACHE=/tmp/flexrouter-go-cache go build -o bin/aitank ./cmd/aitank
```

## Start flexrouter

Run this in one terminal and leave it open:

```bash
flexrouter serve
```

Or start it and open the browser dashboard:

```bash
flexrouter dashboard
```

The dashboard and OpenAI-compatible API are both at
`http://localhost:4891`. The service binds to this computer only.

## Use the existing keyless pool

The shared configuration is at `~/.config/flexrouter/config.yaml`. It
contains LLM7 routes for DeepSeek, MiniMax, Codestral, and Mistral. It
currently works without a token. Test it after starting flexrouter:

```bash
curl http://localhost:4891/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"free","messages":[{"role":"user","content":"Say hi"}]}'
```

If anonymous access is later refused, get a free token from
<https://dash.llm7.io> and enter it at the hidden prompt:

```bash
flexrouter keys add llm7
flexrouter keys list
flexrouter doctor
```

OpenRouter supplies the larger remote pool in
`config.internet-free.yaml`. Create a free key with no subscription or card
at <https://openrouter.ai/keys> and save it with:

```bash
flexrouter keys add openrouter
```

The installed configuration only uses IDs ending in `:free` plus the dynamic
`openrouter/free` selector. This avoids silently using a route whose temporary
zero price later changes. Availability changes, so refresh the live catalog
instead of trusting old model pages.

As of 2026-10-06, MiMo text models are no longer free. Their earlier free
promotion ended, so they are intentionally not placed in a free bucket.
DeepSeek remains in this setup through LLM7's remote turbo tier.

## Use the API

See all configured buckets and models:

```bash
curl http://localhost:4891/v1/models
```

Send a request through the existing `smart` bucket:

```bash
curl http://localhost:4891/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"smart","messages":[{"role":"user","content":"Explain DNS in one sentence."}]}'
```

For an OpenAI-compatible app, use:

- Base URL: `http://localhost:4891/v1`
- Model: `free`, `smart`, `fast`, `code`, `vision`, `long`, or `auto`
- API key: any placeholder such as `x` unless a flexrouter app password was
  enabled in Settings

Python SDK example:

```python
from openai import OpenAI

client = OpenAI(base_url="http://localhost:4891/v1", api_key="x")
reply = client.chat.completions.create(
    model="smart",
    messages=[{"role": "user", "content": "Hello!"}],
)
print(reply.choices[0].message.content)
```

## Show individual models in aitank

`aitank` labels flexrouter as one account named `Free Tier Pool`. That is the
aggregate row, not the model list. Individual model rows are populated only
while flexrouter is running:

```bash
flexrouter serve       # terminal 1
aitank refresh         # terminal 2
aitank list
aitank watch
```

If only this appears:

```text
flexrouter local  Free Tier Pool  — left  OK
```

check the daemon first:

```bash
curl http://localhost:4891/api/status
curl http://localhost:4891/models
```

Then rerun `aitank refresh`. `— left` is expected when a provider does not
publish a numeric remaining-quota percentage; it does not mean the models
were removed.

## Troubleshooting

```bash
flexrouter doctor      # paths, providers, buckets, and missing keys
flexrouter status      # request totals and health
flexrouter tui         # full terminal interface
aitank doctor          # aitank installation and account checks
```

- `no key found`: add the provider's free token with `flexrouter keys add`.
- `connection refused`: start `flexrouter serve`.
- `model not found`: refresh the provider's live catalog in the dashboard and
  replace the obsolete model ID.
- `429`: that free provider's rate limit is exhausted; flexrouter tries the
  next available model in the same bucket.
