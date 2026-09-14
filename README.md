[![Deploy on exe.dev](https://raw.githubusercontent.com/boldsoftware/exe.dev/main/assets/buttons/deploy-on-exe-dev.png)](https://exe.dev/new?repo=https://github.com/alexhughson/opencode-go-shelley-shell-open-go-code)

# opencode-go-shelley-shell-open-go-code

A single-endpoint Go proxy that lets you use [OpenCode Go](https://opencode.ai/zen/go)
models through an [exe.dev custom LLM provider](https://exe.dev/docs/integrations-llm.md).

## Why this exists

OpenCode Go exposes every model behind several API formats — OpenAI Responses
(`/v1/responses`), OpenAI-compatible Chat Completions (`/v1/chat/completions`),
and Anthropic Messages (`/v1/messages`) — but its `/v1/models` endpoint does
not say which format each model requires. Configured naively as an LLM
provider, the gateway claims every API format works for every model, and
requests fail.

This proxy fixes that with one base URL. Its `/models` endpoint rewrites each
model with the correct `api_type` and endpoint, and every route injects the
stable `x-opencode-session` header OpenCode Go expects. Your integration token
is passed straight through; nothing is stored or logged.

## Use

Point one custom LLM provider at the deployed proxy:

```text
https://<your-vm>.exe.xyz
```

Routes handled:

- `/models` → upstream models, rewritten with each model's correct `api_type`
  and endpoint
- `/responses` → OpenCode Go's OpenAI Responses API
- `/completions` or `/chat/completions` → Chat Completions API
- `/messages` → Anthropic Messages API (`Bearer` token becomes `x-api-key`)

Conventional `/v1/...` aliases are also accepted. The proxy forwards the
integration's bearer token without storing it, adds the session ID on every
upstream request (including model discovery), and streams responses through
unchanged.

## Deploy on exe.dev

[![Deploy on exe.dev](https://raw.githubusercontent.com/boldsoftware/exe.dev/main/assets/buttons/deploy-on-exe-dev.png)](https://exe.dev/new?repo=https://github.com/alexhughson/opencode-go-shelley-shell-open-go-code)

```sh
go test ./...
go build -o opencode-go-proxy .
sudo install -m 0755 opencode-go-proxy /usr/local/bin/opencode-go-proxy
sudo cp opencode-go-proxy.service /etc/systemd/system/opencode-go-proxy.service
# set SHELLEY_CONVERSATION_ID (or -session) to a stable session ID
sudo systemctl daemon-reload && sudo systemctl enable --now opencode-go-proxy.service
```

The service listens on port 8000. Point the VM's proxy at it:

```sh
ssh exe.dev share port <vmname> 8000
ssh exe.dev share set-public <vmname>
```

Then register the VM's public URL as a single custom HTTPS LLM provider.
Logs are metadata-only (presence flags, never token values, bodies, or query
strings).

## Development

```sh
go test ./...
go build ./...
SHELLEY_CONVERSATION_ID=test-session go run .
```
