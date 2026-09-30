[![Deploy on exe.dev](https://raw.githubusercontent.com/boldsoftware/exe.dev/main/assets/buttons/deploy-on-exe-dev.png)](https://exe.dev/new?repo=https://github.com/alexhughson/opencode-go-shelley-shell-open-go-code)

# Use OpenCode Go models in Shelley

This is a stateless proxy that wraps OpenCode Go's API so it can be used with
the LLM integration in exe.dev, and thus natively in Shelley on all your
machines.

## The problem

OpenCode Go's `/v1/models` endpoint reports every model as supporting
`/responses`. Most don't — each model only answers on one of `/responses`,
`/completions`, or `/messages`. OpenCode Go also requires a session ID header
on every request.

This proxy rewrites the models listing so each model carries the one endpoint
that actually works, and stamps every upstream request with a session ID taken
from Shelley's session ID when one is passed in.

## Use

Deploy the proxy, then add its URL as a single custom LLM provider in exe.dev.
Shelley picks up the models and routes each one to its correct endpoint.

The machine hosting the proxy has to be public, otherwise the LLM provider
can't reach it.

## Thinking-level discovery

The proxy advertises endpoint-specific `supports_reasoning` and
`reasoning_levels` alongside each model in `/models`. In the exe.dev LLM
integration's `/models.json`, these fields are preserved inside the model's
`upstream` object, not at the top level:

```json
{
  "id": "opencode/meta/muse-spark-1.3-contributor",
  "native_id": "meta/muse-spark-1.3-contributor",
  "upstream": {
    "api_type": "openai-responses",
    "supports_reasoning": true,
    "reasoning_levels": ["low", "medium", "high", "xhigh"]
  }
}
```

Consumers need to read that metadata explicitly. Looking up the proxy hostname
in models.dev cannot identify the upstream provider; matching a model name in
another provider's catalog may describe different controls. An advertised list
is endpoint-specific evidence, while missing metadata is unknown, not permission
to show every effort level.

The proxy only rewrites the request's model ID; it preserves reasoning fields.
Shelley must both expose the advertised list in its models API and use it when
serializing requests. Fixing only the picker is insufficient if a provider
adapter subsequently clamps the selected effort.

`none` / `thinking` are toggle controls, not generic effort tiers. Consumers must
not relabel `thinking` as `high` or advertise it without a corresponding request
encoder.

### Regression checks

Run `go test -race ./...`. `reasoning_contract_test.go` checks Muse discovery and
preservation of reasoning payloads, model rewriting, authentication, and session
identity across all three transports, using local fake upstreams (no paid calls).
These are transport checks, not proof that a provider accepts every test payload.

For an end-to-end Shelley check, verify the same exact list at each boundary:
proxy `/models` → integration `/models.json` (`upstream`) → Shelley `/api/models`
→ model picker/client → captured outgoing `reasoning.effort`. Use an arbitrary
proxy hostname in adapter tests so a hostname-specific shortcut cannot hide a
broken discovery path.
