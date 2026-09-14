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
