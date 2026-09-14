[![Deploy on exe.dev](https://raw.githubusercontent.com/boldsoftware/exe.dev/main/assets/buttons/deploy-on-exe-dev.png)](https://exe.dev/new?repo=https://github.com/alexhughson/opencode-go-shelley-shell-open-go-code)

# Use OpenCode Go models in Shelley

A tiny stateless proxy. It sits between Shelley and [OpenCode Go](https://opencode.ai/zen/go)
so you can pick OpenCode models from Shelley's model picker and just chat.
No keys live on the VM — your token passes straight through.

## The problem

OpenCode Go lists every model as working with every API format. It doesn't.
Each model only answers on one of `/responses`, `/completions`, or `/messages`,
so requests fail with no useful error.

The proxy fixes the listing: each model is tagged with the one endpoint that
actually works. It also stamps every request with the session ID OpenCode Go
wants.

## Use

Deploy, then add the proxy URL as a single custom LLM provider in exe.dev.
Shelley discovers the models and routes each one to its correct endpoint
automatically.
