# Contributing to AI CLI Gateway

Thank you for helping keep AI CLI Gateway small, predictable, and provider-neutral. Ordinary bug reports, design discussions, and focused pull requests are welcome.

## Toolchain

Use Go 1.26.5 and golangci-lint v2.12.2. Keep generated binaries and test artifacts outside the repository.

## Development method

TDD is expected for behavior changes. Add a failing test that pins the behavior first, and check it fails for the reason you expect rather than an unrelated one. Then implement the smallest change that makes it pass, and refactor while the package tests stay green.

Provider and process behavior needs fake CLI integration coverage. Default tests use no real provider CLI, credentials, or inference. Never add provider authentication material, sensitive prompts, or provider outputs to a fixture.

Before requesting review, run the applicable full verification set:

```text
gofmt -l .
go vet ./...
golangci-lint run ./...
go test ./...
go test -race ./...
go test -tags=integration ./...
go test -trimpath -count=1 ./...
CGO_ENABLED=0 go build -trimpath -o "${TMPDIR:-/tmp}/ai-cli-gateway" ./cmd/ai-cli-gateway
```

Process containment changes require cross-platform evidence, not only cross-compilation. Include native Unix process-group coverage and native Windows Job Object tests for descendant termination, cancellation, cleanup, and handle quiescence as applicable.

## Opt-in provider tests

Default tests and CI do not use installed provider CLIs or credentials. Compile the opt-in sources without executing them with:

```bash
go test -tags=live -run '^$' ./internal/provider/...
```

Live probes and inference are explicit maintainer operations. Use a dedicated disposable canary; inference may incur provider usage and cost.

Probe execution requires `AI_CLI_GATEWAY_LIVE_PROBES=1`. Inference also requires `AI_CLI_GATEWAY_LIVE_INFERENCE=1` and one matching provider gate:

- `AI_CLI_GATEWAY_LIVE_CODEX_INFERENCE=1`;
- `AI_CLI_GATEWAY_LIVE_CLAUDE_INFERENCE=1`; or
- `AI_CLI_GATEWAY_LIVE_GEMINI_INFERENCE=1`.

Each selected provider needs its canary configuration:

- Codex: `AI_CLI_GATEWAY_LIVE_CODEX_EXECUTABLE`, `AI_CLI_GATEWAY_LIVE_CODEX_CONFIG_HOME`, and `AI_CLI_GATEWAY_LIVE_CODEX_MODEL`.
- Claude: `AI_CLI_GATEWAY_LIVE_CLAUDE_EXECUTABLE`, `AI_CLI_GATEWAY_LIVE_CLAUDE_CONFIG_HOME`, `AI_CLI_GATEWAY_LIVE_CLAUDE_MODEL`, and `AI_CLI_GATEWAY_LIVE_CLAUDE_AUTH_MODE=config_home|api_key`.
- Gemini: `AI_CLI_GATEWAY_LIVE_GEMINI_EXECUTABLE`, `AI_CLI_GATEWAY_LIVE_GEMINI_CONFIG_HOME`, `AI_CLI_GATEWAY_LIVE_GEMINI_MODEL`, and `AI_CLI_GATEWAY_LIVE_GEMINI_AUTH_MODE=gemini_api_key|google_api_key|vertex`.

The selected API-key or Vertex mode also needs its corresponding provider environment values outside the repository. The harness redacts failures and cleans up its canary state.

GitHub Actions uses Node24-based official actions. A self-hosted runner needs `actions/runner v2.327.1` or later.

## Pull requests

Keep each change scoped, explain its public contract impact, and include the new test plus relevant platform results. Update public documentation when behavior changes, but do not broaden the advertised Responses-compatible subset beyond tested implementation.

For a suspected vulnerability, use private security reporting as described in [SECURITY.md](SECURITY.md), not an ordinary issue or pull request.
