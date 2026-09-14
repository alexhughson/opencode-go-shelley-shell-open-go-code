# OpenCode Go format-aware proxy

One base endpoint for an exe.dev custom LLM provider:

```text
https://opencode-goer.exe.xyz
```

The exe.dev integration can call:

- `/models` — upstream models rewritten with each model's correct `api_type`
  and endpoint;
- `/responses` — forwarded to OpenCode Go's OpenAI Responses API;
- `/completions` or `/chat/completions` — forwarded to its OpenAI-compatible
  Chat Completions API; and
- `/messages` — forwarded to its Anthropic Messages API.

Conventional `/v1/...` aliases are also accepted. Every upstream request,
including model discovery, gets the stable `x-opencode-session` header. The
incoming integration token is forwarded without being stored or logged;
bearer auth is translated to `x-api-key` for Anthropic Messages.

The service listens on port 8000 and runs under systemd as
`opencode-go-proxy.service`.

## Development

```sh
go test ./...
go build ./...
SHELLEY_CONVERSATION_ID=test-session go run .
```
