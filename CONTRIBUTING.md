# Contributing to AI CLI Gateway

Thank you for helping keep AI CLI Gateway small, predictable, and provider-neutral. Ordinary bug reports, design discussions, and focused pull requests are welcome.

## Toolchain

Use Go 1.26.5 and golangci-lint v2.12.2. Keep generated binaries and test artifacts outside the repository.

## Development method

TDD is required for behavior changes. Write one focused semantic RED that fails for the intended reason before implementation, make the minimum change to reach GREEN, then refactor while the focused and package tests remain green.

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

## Pull requests

Keep each change scoped, explain its public contract impact, and include the focused RED/GREEN evidence plus relevant platform results. Update public documentation when behavior changes, but do not broaden the advertised Responses-compatible subset beyond tested implementation.

For a suspected vulnerability, use private security reporting as described in [SECURITY.md](SECURITY.md), not an ordinary issue or pull request.
