# OpenCode Go format-aware proxy

A small reverse proxy for using OpenCode Go as an exe.dev custom LLM provider.
It:

- forwards bearer/API-key authentication without storing it;
- adds a stable `x-opencode-session` derived from the Shelley conversation;
- preserves streaming responses;
- rejects a model sent to the wrong API format; and
- exposes separate, filtered model catalogs for each supported API format.

## Endpoints for exe.dev LLM integrations

Configure these as **three custom HTTPS providers**, with one supported API per provider:

| Provider | Base URL | Supported API |
|---|---|---|
| `opencode-go-responses` | `https://opencode-goer.exe.xyz/responses/v1` | OpenAI Responses |
| `opencode-go-chat` | `https://opencode-goer.exe.xyz/chat/v1` | OpenAI Chat Completions |
| `opencode-go-anthropic` | `https://opencode-goer.exe.xyz/anthropic/v1` | Anthropic Messages |

Use bearer-token authentication for all three providers. For Anthropic
Messages, the proxy converts the incoming bearer token to OpenCode Go's
required `x-api-key` header. It can also accept `x-api-key` directly. The
proxy never logs or persists either credential.

The public root also offers `/v1/models`, `/v1/responses`,
`/v1/chat/completions`, and `/v1/messages`. Root model results include the
nonstandard `api_type` and `endpoint` fields for inspection, but the scoped
provider URLs above are what prevent model discovery from claiming every API
format works for every model.

## Development

```sh
go test ./...
go build ./...
SHELLEY_CONVERSATION_ID=test-session go run .
```
