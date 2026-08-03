# AI CLI Gateway MVP Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> `superpowers:subagent-driven-development` (recommended) or
> `superpowers:executing-plans` to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a public Go gateway that runs locally authenticated Codex CLI and
Claude Code plus Gemini CLI with its three accepted environment/external
credential shapes behind a strict non-streaming OpenAI Responses-compatible HTTP
subset.

**Architecture:** Strict HTTP decoding produces provider-neutral requests, an
immutable alias registry selects a provider, and an independent bounded scheduler
admits work. Provider adapters create argv/stdin contracts while a shared
cross-platform supervisor owns temporary runtimes, output caps, cancellation, and
process containment; all JSON Schema output is validated locally before a
Responses-shaped response is encoded.

**Tech Stack:** Go 1.26.5; Go standard library; `golang.org/x/sys v0.47.0`;
`github.com/pelletier/go-toml/v2 v2.4.3`;
`github.com/santhosh-tekuri/jsonschema/v6 v6.0.2`; golangci-lint v2; GitHub
Actions.

## Global Constraints

- Project/repository name: `AI CLI Gateway` / `ai-cli-gateway`.
- Go toolchain: Go 1.26.5; module path:
  `github.com/krkarma777/ai-cli-gateway`.
- Release builds set `CGO_ENABLED=0` and produce one gateway executable; provider
  CLIs remain separate user-managed installations.
- License: Apache-2.0.
- API scope is exactly `POST /v1/responses` and `GET /v1/models`.
- Describe compatibility only as a **Responses API-compatible subset**.
- Input supports required string `model`, required non-empty string `input`,
  optional string/null `instructions`, and the approved `text.format` subset.
- `stream` and `store` accept only `false`; `tools` accepts only `[]`;
  `tool_choice` accepts only `"none"`.
- Unknown, duplicate, trailing, malformed, unsupported, or excessive request data
  is rejected; it is never silently ignored.
- No SSE, tool-call round trip, conversation/session store, UI, or database.
- Default listener is `127.0.0.1:8080`; MVP listeners are loopback-only.
- Optional gateway Bearer secret is referenced by environment-variable name and
  compared in constant time.
- Run provider processes using an absolute executable and argv array, never a
  shell; all `instructions` and `input` bytes go through stdin.
- Every admitted request receives a `0700` temporary runtime; request files are
  `0600`.
- Provider execution defaults: concurrency 1, queue 32, queued request bytes
  16 MiB, queue wait 30s, execution 300s, TERM grace 2s, cleanup 5s.
- Data limits: HTTP body 1 MiB, input 512 KiB, instructions 256 KiB, schema
  32 KiB, stdout 2 MiB, stderr 256 KiB, final output 1 MiB.
- Unix uses a new process group; Windows uses suspended creation plus a
  non-breakaway Job Object with `KILL_ON_JOB_CLOSE`.
- A scheduler permit is released only after process containment, root wait,
  bounded stream drain, and bounded runtime cleanup complete.
- Logs never receive prompt, output, schema, credential, raw stdout/stderr,
  provider envelope, full argv/environment, or auth identity fields.
- Gateway code never issues, extracts, refreshes, or persists provider tokens.
  Explicit credential environment values are relayed only in child memory.
- Gemini MVP readiness accepts exactly its three environment/external credential
  shapes with a disposable `GEMINI_CLI_HOME`; cached personal OAuth is unsupported.
- As of 2026-08-02, Google stopped the consumer Login-with-Google path for Gemini
  Code Assist for individuals, Google AI Pro, and Google AI Ultra on 2026-06-18
  and points those users to Antigravity. Google says Code Assist Standard and
  Enterprise plus paid API-key access remain, while current official docs also
  describe API-key and Vertex tiers; these are not exhaustive gateway access rules.
- Actual Gemini availability, billing tier, quota, entitlement, and live credential
  validity are exclusively upstream. `configured`, `implemented`, and readiness
  prove local checks only; provider execution is authoritative. Antigravity CLI is
  out of scope.
- Adapter versions: Codex `>=0.146.0,<0.147.0`; Claude
  `>=2.1.208,<2.2.0`; Gemini `>=0.53.0,<0.54.0`.
- TDD is mandatory: create a focused failing test, observe the expected failure,
  implement the smallest behavior, then rerun focused and package tests.
- The user explicitly required Git initialization only after all implementation
  and verification. Do not run `git init`, `git add`, or `git commit` before
  Task 19, even though the normal Superpowers template recommends intermediate
  commits.
- Do not upgrade or modify the user's installed provider CLIs unless a separate
  in-scope approval is obtained. Fake CLI tests must work without real CLIs.
- Do not place a secret value or real provider-authentication file anywhere in the
  repository.

---

## Planned File Map

### Entry point and application assembly

- `cmd/ai-cli-gateway/main.go`, `signals_unix.go`, `signals_windows.go` — process
  entry point and platform shutdown signals only.
- `internal/cli/cli.go` — `version`, `serve`, and `doctor` command dispatch.
- `internal/app/app.go` — construct config, health, schedulers, gateway, and HTTP
  server.
- `internal/buildinfo/buildinfo.go` — link-time version metadata.

### Provider-neutral contract

- `internal/core/types.go` — normalized request, format, result, model, provider
  names.
- `internal/core/errors.go` — stable HTTP status/type/code/message catalog.
- `internal/core/registry.go` — immutable alias validation, lookup, sorted model
  snapshot.
- `internal/safejson/parse.go` — duplicate-safe, depth-safe, exact-one-value JSON
  parser.
- `internal/schema/schema.go` — portable schema-profile checks, compilation, final
  validation.

### Configuration and admission

- `internal/config/types.go` — strict TOML structures and duration type.
- `internal/config/load.go`, `defaultroot_unix.go`, `defaultroot_windows.go` —
  decode, defaults, structural validation, per-user runtime default.
- `internal/scheduler/scheduler.go` — provider-local FIFO, byte/count bounds,
  cancellation, shutdown.

### Process lifecycle

- `internal/process/types.go` — command/result/limits/error types.
- `internal/process/root.go` — locked runtime root, request directories, file
  materialization, quarantine janitor.
- `internal/process/root_unix.go`, `root_windows.go` — exclusive platform lock.
- `internal/process/capture.go` — concurrent bounded stdout/stderr collectors.
- `internal/process/supervisor.go` — common lifecycle and cleanup orchestration.
- `internal/process/runner_unix.go` — PGID launch/TERM/KILL/no-member verification.
- `internal/process/runner_windows.go` — suspended CreateProcess, Job assignment,
  resume, termination, zero-active-process verification.
- `internal/testcli/fake.go`,
  `internal/testcli/spawn_unix.go`, `spawn_windows.go`,
  `internal/testcli/cmd/fake-ai-cli/main.go` — cross-platform fake CLI fixture.
- `internal/testutil/fakecli.go` — build fake/provider and gateway helper binaries
  only under test temp directories.
- `internal/selftest/selftest.go` — fixed child-tree helper embedded in the
  production binary for non-inference containment diagnostics.

### Providers, doctor, and gateway

- `internal/provider/adapter.go` — adapter, probe, health, provider error
  interfaces.
- `internal/provider/prompt.go` — byte-length-framed stdin envelope.
- `internal/provider/env.go` — minimal environment builder and credential
  isolation.
- `internal/provider/version.go` — numeric version parsing/range checks.
- `internal/provider/codex/codex.go` — Codex argv, parsing, health.
- `internal/provider/claude/claude.go` — Claude argv, envelope parsing, health.
- `internal/provider/gemini/gemini.go` — Gemini disposable-home argv, parsing,
  health.
- `internal/doctor/doctor.go`, `output.go`, `path.go`, `path_unix.go`,
  `path_windows.go`, `path_acl_policy.go` — redacted readiness checks, closed
  report validation, native path-evidence acquisition, portable ACL policy, and
  human/JSON output.
- `internal/gateway/gateway.go` — alias resolution, admission, execution, parsing,
  schema validation, error mapping.

### HTTP, documentation, and automation

- `internal/httpapi/request.go` — strict subset decoder.
- `internal/httpapi/response.go` — Responses/models/error encoders and opaque IDs.
- `internal/httpapi/auth.go` — optional constant-time Bearer validation.
- `internal/httpapi/server.go` — exact routes, global gates, timeouts, shutdown.
- `internal/observability/log.go` — closed metadata-only structured events.
- `config.example.toml`, `deploy/systemd/ai-cli-gateway.service` — safe examples.
- `README.md`, `CONTRIBUTING.md`, `SECURITY.md`, `LICENSE`,
  `THIRD_PARTY_NOTICES.md`, `.gitignore` — public project documentation.
- `internal/securitytest/repository_test.go` — repository secret/artifact guard.
- `.golangci.yml`, `Makefile`, `.github/workflows/ci.yml` — local and CI
  verification.

Every production file gets a sibling `_test.go` where behavior exists. Platform
integration tests use build tags and the fake CLI executable rather than mocks.

---

### Task 1: Toolchain and Executable Foundation

**Files:**

- Create: `go.mod`
- Create: `.go-version`
- Create: `cmd/ai-cli-gateway/main.go`
- Create: `internal/buildinfo/buildinfo.go`
- Create: `internal/buildinfo/buildinfo_test.go`
- Create: `internal/cli/cli.go`
- Create: `internal/cli/cli_test.go`
- Create: `Makefile`
- Create: `.golangci.yml`

**Interfaces:**

- Produces: `buildinfo.Version`, `buildinfo.Commit`, `buildinfo.Date`.
- Produces: `cli.Run(args []string, stdout, stderr io.Writer) int`.
- Later tasks extend `cli.Run` with `serve` and `doctor`; `version` remains stable.

- [ ] **Step 1: Verify or install the exact Go toolchain**

Run:

```bash
go version
```

Expected on the current machine: command missing. Request the scoped package-manager
escalation, then run:

```bash
brew install go
go version
```

Expected: `go version go1.26.5 darwin/arm64`. If Homebrew provides a different
1.26 patch, install the official Go 1.26.5 darwin-arm64 package from
`https://go.dev/dl/` and re-run `go version`; do not proceed with Go 1.27.

Verify the linter:

```bash
golangci-lint version
```

Expected major/minor: `v2.12.2`. If missing or different, install the pinned tool
outside the repository and use that absolute binary for all local lint commands:

```bash
mkdir -p "${TMPDIR:-/tmp}/ai-cli-gateway-tools/bin"
GOBIN="${TMPDIR:-/tmp}/ai-cli-gateway-tools/bin" \
  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
"${TMPDIR:-/tmp}/ai-cli-gateway-tools/bin/golangci-lint" version
```

Dependency downloads require the normal scoped network approval; no tool binary
is written into the repository.

- [ ] **Step 2: Create the module boundary, then write failing behavior tests**

The module/toolchain files are test infrastructure, so create them before the
first red run:

```go
// go.mod
module github.com/krkarma777/ai-cli-gateway

go 1.26.0

toolchain go1.26.5
```

Create `.go-version` containing `1.26.5`, then create the tests below without
their implementations.

Create `internal/buildinfo/buildinfo_test.go`:

```go
package buildinfo

import "testing"

func TestDefaultsAreSafe(t *testing.T) {
	if Version != "dev" || Commit != "none" || Date != "unknown" {
		t.Fatalf("unexpected defaults: %q %q %q", Version, Commit, Date)
	}
}
```

Create `internal/cli/cli_test.go`:

```go
package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"version"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "ai-cli-gateway dev") {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"unknown"}, &stdout, &stderr)
	if code != 2 || !strings.Contains(stderr.String(), "usage:") {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
}
```

- [ ] **Step 3: Run the tests and observe the intended failure**

Run:

```bash
go test ./internal/buildinfo ./internal/cli
```

Expected: FAIL on the missing implementation symbols, proving the tests—not
module discovery—are red.

- [ ] **Step 4: Create the minimal implementation and executable**

Create `internal/buildinfo/buildinfo.go`:

```go
package buildinfo

var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)
```

Create `internal/cli/cli.go`:

```go
package cli

import (
	"fmt"
	"io"

	"github.com/krkarma777/ai-cli-gateway/internal/buildinfo"
)

func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 1 && args[0] == "version" {
		fmt.Fprintf(stdout, "ai-cli-gateway %s (%s, %s)\n",
			buildinfo.Version, buildinfo.Commit, buildinfo.Date)
		return 0
	}
	fmt.Fprintln(stderr, "usage: ai-cli-gateway <version|serve|doctor>")
	return 2
}
```

Create `cmd/ai-cli-gateway/main.go`:

```go
package main

import (
	"os"

	"github.com/krkarma777/ai-cli-gateway/internal/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdout, os.Stderr))
}
```

This is the intentionally minimal Task 1 scaffold, not the retained command or
main-process contract. Task 17's exact CLI grammar, signal context, hidden
self-test precedence, fixed output, and exit semantics below supersede this
pseudocode.

Create a `Makefile` with targets `fmt-check`, `test`, `race`, `integration`,
`lint`, and `build`. Each target runs exactly the command used in the design:

```make
.PHONY: fmt-check test race integration lint build verify
GOLANGCI_LINT ?= golangci-lint

fmt-check:
	@unformatted_files="$$(gofmt -l .)" && { \
		test -z "$$unformatted_files" || { \
			printf '%s\n' "$$unformatted_files"; exit 1; \
		}; \
	}

test:
	go test ./...

race:
	go test -race ./...

integration:
	go test -tags=integration ./...

lint:
	$(GOLANGCI_LINT) run ./...

build:
	CGO_ENABLED=0 go build -o "$${TMPDIR:-/tmp}/ai-cli-gateway" \
		./cmd/ai-cli-gateway

verify: fmt-check test race integration lint build
```

Create `.golangci.yml`:

```yaml
version: "2"
run:
  timeout: 5m
linters:
  default: standard
  enable:
    - bodyclose
    - errcheck
    - errorlint
    - exhaustive
    - gosec
    - nilerr
    - noctx
    - revive
    - staticcheck
    - unconvert
formatters:
  enable:
    - gofmt
    - goimports
```

- [ ] **Step 5: Verify the foundation**

Run:

```bash
gofmt -w cmd internal
go test ./internal/buildinfo ./internal/cli
CGO_ENABLED=0 go build -o "${TMPDIR:-/tmp}/ai-cli-gateway" \
  ./cmd/ai-cli-gateway
"${TMPDIR:-/tmp}/ai-cli-gateway" version
```

Expected: tests PASS, build succeeds, and output begins
`ai-cli-gateway dev`. The generated binary is outside the repository.

- [ ] **Step 6: Record the no-commit checkpoint**

Run:

```bash
rg --files | sort
```

Expected: only Task 1 source/config files plus the approved design and this plan.
Do not initialize Git.

---

### Task 2: Core Types, Error Catalog, and Model Registry

**Files:**

- Create: `internal/core/types.go`
- Create: `internal/core/errors.go`
- Create: `internal/core/errors_test.go`
- Create: `internal/core/registry.go`
- Create: `internal/core/registry_test.go`

**Interfaces:**

- Produces: `core.ProviderName`, `core.FormatType`, `core.OutputFormat`,
  `core.Request`, `core.Model`, `core.Result`.
- Produces: `core.APIError`, its safe `error` implementation, and
  `core.Error(code string, param *string)`.
- Produces: `core.NewOutcomeError(error, core.ResultMeta)
  (*core.OutcomeError, error)` for closed failure metadata.
- Produces: `core.NewRegistry(models []core.Model) (*core.Registry, error)`,
  `core.ValidateProviderModel(string) error`, `Resolve(alias string)`, and
  `Models()`.
- Consumers must treat `Registry.Models()` as a defensive sorted copy.

- [ ] **Step 1: Write failing registry and error tests**

Create table tests covering valid aliases, sorting, duplicate aliases, leading
dash, path separators, whitespace, unknown model lookup, provider-model
empty/leading-dash/NUL/control/invalid-UTF-8/over-256-byte values, and every
public code. Mutate the caller's `param` string after constructing an error and
assert the error retains its defensive copy; an unknown internal code must
produce the fixed `internal_error` entry without panicking. Test that
`NewOutcomeError` unwraps only the allowed safe causes, defensively returns
metadata, and rejects unknown stop enums or an arbitrary raw error.
The central examples are:

```go
func TestRegistrySortsAndResolves(t *testing.T) {
	r, err := NewRegistry([]Model{
		{ID: "z-model", Provider: ProviderCodex, ProviderModel: "z"},
		{ID: "a-model", Provider: ProviderClaude, ProviderModel: "a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Models(); got[0].ID != "a-model" || got[1].ID != "z-model" {
		t.Fatalf("models=%v", got)
	}
	m, ok := r.Resolve("z-model")
	if !ok || m.ProviderModel != "z" {
		t.Fatalf("model=%v ok=%v", m, ok)
	}
}

func TestRegistryRejectsUnsafeAlias(t *testing.T) {
	for _, id := range []string{"", "-model", "../model", "a/b", "a b"} {
		if _, err := NewRegistry([]Model{{
			ID: id, Provider: ProviderCodex, ProviderModel: "safe",
		}}); err == nil {
			t.Fatalf("accepted %q", id)
		}
	}
}

func TestErrorCatalog(t *testing.T) {
	param := "stream"
	e := Error(CodeUnsupportedParameter, &param)
	if e.StatusCode() != 400 || e.TypeName() != "invalid_request_error" ||
		e.CodeValue() != CodeUnsupportedParameter || e.ParamValue() == nil {
		t.Fatalf("error=%+v", e)
	}
}
```

- [ ] **Step 2: Run the focused tests and observe failure**

Run:

```bash
go test ./internal/core
```

Expected: FAIL because `internal/core` has no implementation.

- [ ] **Step 3: Implement the exact core types**

Create `internal/core/types.go` with these public definitions:

```go
package core

import (
	"encoding/json"
	"time"
)

type ProviderName string

const (
	ProviderCodex  ProviderName = "codex"
	ProviderClaude ProviderName = "claude"
	ProviderGemini ProviderName = "gemini"
)

type FormatType string

const (
	FormatText       FormatType = "text"
	FormatJSONSchema FormatType = "json_schema"
)

type OutputFormat struct {
	Type        FormatType
	Name        string
	Description *string
	Schema      json.RawMessage
}

type Request struct {
	ModelAlias   string
	Instructions *string
	Input        string
	Format       OutputFormat
}

func (r Request) Weight() int64 {
	n := len(r.ModelAlias) + len(r.Input) + len(r.Format.Name) +
		len(r.Format.Schema)
	if r.Format.Description != nil {
		n += len(*r.Format.Description)
	}
	if r.Instructions != nil {
		n += len(*r.Instructions)
	}
	return int64(n)
}

type Model struct {
	ID            string
	Provider      ProviderName
	ProviderModel string
	Created       int64
}

type Result struct {
	Text string
	Meta ResultMeta
}

type ResultMeta struct {
	Provider        ProviderName
	StdoutBytes     int64
	StderrBytes     int64
	QueueDepth      int
	RunningCount    int
	QueueDuration   time.Duration
	ExecutionTime   time.Duration
	ProviderVersion string
	ExitCategory    string
	StopReason      string
	StopAction      string
}

type OutcomeError struct {
	cause error
	meta  ResultMeta
}
```

`NewOutcomeError(cause error, meta ResultMeta) (*OutcomeError, error)` accepts
only a `*APIError`, `context.Canceled`, or
`context.DeadlineExceeded`, returns the cause's already-safe text from `Error`,
implements `Unwrap`, and returns a defensive metadata copy through
`ResultMetadata()`. This gives the HTTP layer numeric/fixed-enum failure metadata
without exposing a raw process error. `StopReason` and `StopAction` are closed
validated enums such as `completed`, `client_canceled`, `gateway_shutdown`,
`execution_timeout`, `output_limit`, `cleanup_failed` and `none`, `term`,
`kill`, `terminate_job`; arbitrary strings are rejected. The constructor also
requires non-negative counts/durations, a known-or-empty provider, a canonical
bounded dotted numeric provider version, and a closed exit category. It rejects every
other string-bearing metadata value rather than carrying it toward HTTP or logs.

Create `internal/core/errors.go` with constants for every design code:

```go
package core

type APIError struct {
	status  int
	typ     string
	code    string
	param   *string
	message string
}

const (
	CodeInvalidJSON            = "invalid_json"
	CodeInvalidRequest         = "invalid_request"
	CodeUnsupportedParameter   = "unsupported_parameter"
	CodeInvalidJSONSchema      = "invalid_json_schema"
	CodeInvalidBearerKey       = "invalid_bearer_key"
	CodeNotFound               = "not_found"
	CodeModelNotFound          = "model_not_found"
	CodeMethodNotAllowed       = "method_not_allowed"
	CodeRequestTimeout         = "request_timeout"
	CodeRequestTooLarge        = "request_too_large"
	CodeUnsupportedMediaType   = "unsupported_media_type"
	CodeServerBusy             = "server_busy"
	CodeQueueFull              = "queue_full"
	CodeProviderRateLimited    = "provider_rate_limited"
	CodeQueueTimeout           = "queue_timeout"
	CodeProviderNotReady       = "provider_not_ready"
	CodeProviderAuthRequired   = "provider_auth_required"
	CodeServiceShuttingDown    = "service_shutting_down"
	CodeProviderTimeout        = "provider_timeout"
	CodeOutputLimitExceeded    = "output_limit_exceeded"
	CodeProviderProtocolError  = "provider_protocol_error"
	CodeStructuredOutputInvalid = "structured_output_invalid"
	CodeProviderFailed         = "provider_failed"
	CodeProcessCleanupFailed   = "process_cleanup_failed"
	CodeInternalError          = "internal_error"
)
```

Use an unexported `map[string]errorSpec` to assign the exact status, type, and safe
message. `Error` must copy `param`, reduce an unknown internal code to the fixed
`internal_error` entry rather than panic, and never accept a caller-provided
message. `(*APIError).Error()` returns only `Code + ": " + Message`, so it is
safe for internal wrapping and `errors.As`. Expose read-only `StatusCode`,
`TypeName`, `CodeValue`, `ParamValue`, and `MessageText` methods; `ParamValue`
returns a fresh copy. No package can mutate catalog-backed fields:

| Code | HTTP | Type | Exact message |
|---|---:|---|---|
| `invalid_json` | 400 | `invalid_request_error` | `The request body is not valid JSON.` |
| `invalid_request` | 400 | `invalid_request_error` | `The request is invalid.` |
| `unsupported_parameter` | 400 | `invalid_request_error` | `This parameter or value is not supported.` |
| `invalid_json_schema` | 400 | `invalid_request_error` | `The JSON Schema is not supported.` |
| `invalid_bearer_key` | 401 | `authentication_error` | `A valid gateway Bearer key is required.` |
| `not_found` | 404 | `invalid_request_error` | `The requested endpoint was not found.` |
| `model_not_found` | 404 | `invalid_request_error` | `The requested model alias was not found.` |
| `method_not_allowed` | 405 | `invalid_request_error` | `The HTTP method is not allowed for this endpoint.` |
| `request_timeout` | 408 | `invalid_request_error` | `The request was not received before its deadline.` |
| `request_too_large` | 413 | `invalid_request_error` | `The request exceeds a configured size limit.` |
| `unsupported_media_type` | 415 | `invalid_request_error` | `The request media type or content encoding is not supported.` |
| `server_busy` | 429 | `rate_limit_error` | `The gateway is at its global request limit.` |
| `queue_full` | 429 | `rate_limit_error` | `The provider queue is full.` |
| `provider_rate_limited` | 429 | `rate_limit_error` | `The provider rate-limited the request.` |
| `queue_timeout` | 503 | `server_error` | `The request expired while waiting for provider capacity.` |
| `provider_not_ready` | 503 | `server_error` | `The selected provider is not ready.` |
| `provider_auth_required` | 503 | `server_error` | `The selected provider requires authentication.` |
| `service_shutting_down` | 503 | `server_error` | `The gateway is shutting down.` |
| `provider_timeout` | 504 | `server_error` | `The provider did not finish before its deadline.` |
| `output_limit_exceeded` | 502 | `server_error` | `The provider output exceeded a configured limit.` |
| `provider_protocol_error` | 502 | `server_error` | `The provider returned an invalid response.` |
| `structured_output_invalid` | 502 | `server_error` | `The provider output did not match the requested JSON Schema.` |
| `provider_failed` | 502 | `server_error` | `The provider command failed.` |
| `process_cleanup_failed` | 500 | `server_error` | `The provider process could not be cleaned up safely.` |
| `internal_error` | 500 | `server_error` | `The gateway encountered an internal error.` |

```go
func Error(code string, param *string) *APIError {
	spec, ok := errorCatalog[code]
	if !ok {
		code = CodeInternalError
		spec = errorCatalog[code]
		param = nil
	}
	var copiedParam *string
	if param != nil {
		value := *param
		copiedParam = &value
	}
	return &APIError{
		status: spec.status, typ: spec.typ, code: code,
		param: copiedParam, message: spec.message,
	}
}
```

- [ ] **Step 4: Implement immutable alias validation**

Create `internal/core/registry.go`. Validate IDs with
`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`; require a known provider and non-empty
provider model; reject duplicates; copy maps/slices on construction and reads.
`ValidateProviderModel` permits printable valid UTF-8 values up to 256 bytes,
including provider punctuation and path separators, but rejects empty,
leading-dash, NUL/control, and invalid UTF-8 values. The registry calls it for
every model.

```go
type Registry struct {
	byID   map[string]Model
	models []Model
}

func (r *Registry) Resolve(alias string) (Model, bool) {
	m, ok := r.byID[alias]
	return m, ok
}

func (r *Registry) Models() []Model {
	return append([]Model(nil), r.models...)
}
```

Sort once with `sort.Slice(models, func(i, j int) bool {
return models[i].ID < models[j].ID })`.

- [ ] **Step 5: Run focused and package tests**

Run:

```bash
gofmt -w internal/core
go test ./internal/core
go test ./...
```

Expected: all tests PASS.

- [ ] **Step 6: Record the no-commit checkpoint**

Review `internal/core` with:

```bash
rg -n 'prompt|stderr|token|credential' internal/core
```

Expected: no sensitive runtime data added to `APIError` or the registry. Do not
initialize Git.

---

### Task 3: Exact JSON Parser and Responses Request Decoder

**Files:**

- Create: `internal/safejson/parse.go`
- Create: `internal/safejson/parse_test.go`
- Create: `internal/httpapi/request.go`
- Create: `internal/httpapi/request_test.go`

**Interfaces:**

- Consumes: `core.Request`, `core.OutputFormat`, `core.APIError`.
- Produces: `safejson.Parse(data []byte, limits safejson.Limits) (any, error)`.
- Produces: `httpapi.DecodeRequest(data []byte, limits RequestLimits)
  (core.Request, *core.APIError)`.
- `safejson.Parse` is reused by Task 4 for schemas and provider output.

Define the closed limit and parser-error contracts before the first tests:

```go
// internal/safejson
type Limits struct {
	MaxDepth       int
	MaxNumberBytes int
}

var (
	ErrEncoding   = errors.New("invalid string encoding")
	ErrNUL        = errors.New("NUL is not allowed")
	ErrSyntax     = errors.New("invalid JSON syntax")
	ErrDuplicate  = errors.New("duplicate object key")
	ErrTrailing   = errors.New("trailing JSON data")
	ErrRootObject = errors.New("root must be an object")
	ErrLimit      = errors.New("JSON limit exceeded")
)

// internal/httpapi
type RequestLimits struct {
	InputBytes        int
	InstructionsBytes int
	SchemaBytes       int
	MaxDepth          int
	MaxNumberBytes    int
}
```

`DefaultRequestLimits` returns 512 KiB, 256 KiB, 32 KiB, depth 64, and
128 numeric-token bytes. A constructor/validator rejects non-positive or
overflowing values. Production app assembly replaces the first three defaults
with their validated TOML values and keeps the fixed parser limits.

- [ ] **Step 1: Write failing safe-JSON tests**

Create table tests whose input and expected outcome include:

```go
tests := []struct {
	name string
	raw  string
	ok   bool
}{
	{"object", `{"a":1}`, true},
	{"duplicate root", `{"a":1,"a":2}`, false},
	{"duplicate nested", `{"a":{"x":1,"x":2}}`, false},
	{"trailing", `{"a":1} []`, false},
	{"bom", "\xef\xbb\xbf{}", false},
	{"nul", "{\"a\":\"\x00\"}", false},
	{"long number", `{"n":123456789}`, false},
}
```

Use `Limits{MaxDepth: 4, MaxNumberBytes: 8}`. Add generated nested arrays that
pass at depth 4 and fail at depth 5, invalid UTF-8 bytes, non-object roots, and a
string containing escaped `\u0000` (which must also fail after decoding). Assert
the exact sentinel category for invalid UTF-8, raw/escaped NUL, BOM, malformed
syntax, duplicate keys, trailing data, wrong root shape, depth, and number
limits; scan every error string for planted request fragments.

- [ ] **Step 2: Run the parser test and observe failure**

Run:

```bash
go test ./internal/safejson
```

Expected: FAIL because `safejson.Parse` is undefined.

- [ ] **Step 3: Implement one-value recursive token parsing**

Implement `safejson.Parse` with `json.Decoder.UseNumber()` and a recursive
`readValue`. Object parsing keeps a `map[string]struct{}` key set before reading
each value; arrays and objects increment depth; numbers are checked using their
lexical string. Reject invalid UTF-8, a leading BOM, raw NUL, decoded NUL in any
string/key, trailing tokens, and a root that is not `map[string]any`. Return only
the closed sentinels above (wrapped without request-derived text): invalid
UTF-8 uses `ErrEncoding`; raw-or-decoded NUL uses `ErrNUL`; BOM/JSON grammar uses `ErrSyntax`;
duplicates, trailing data, root shape, and limits use their dedicated sentinel.

The core shape is:

```go
func Parse(data []byte, limits Limits) (any, error) {
	if !utf8.Valid(data) {
		return nil, ErrEncoding
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, ErrNUL
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return nil, ErrSyntax
	}
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	value, err := readValue(dec, 0, limits)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, ErrRootObject
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, ErrTrailing
		}
		return nil, ErrSyntax
	}
	return value, nil
}
```

`readValue` must switch on `json.Delim('{')` and `json.Delim('[')`, call
`dec.More()`, require object keys to be strings, and consume the matching closing
delimiter. Do not use `map` unmarshalling before duplicate detection.

- [ ] **Step 4: Write failing request-subset tests**

Cover the accepted minimal request and every compatibility no-op:

```go
func TestDecodeRequestSubset(t *testing.T) {
	raw := []byte(`{
	  "model":"codex-default",
	  "instructions":"be concise",
	  "input":"hello",
	  "text":{"format":{"type":"text"}},
	  "stream":false,
	  "store":false,
	  "tools":[],
	  "tool_choice":"none"
	}`)
	got, apiErr := DecodeRequest(raw, DefaultRequestLimits())
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if got.ModelAlias != "codex-default" || got.Input != "hello" ||
		got.Instructions == nil || *got.Instructions != "be concise" {
		t.Fatalf("request=%+v", got)
	}
}
```

Add an accepted case where absent `text` defaults to `FormatText`. Add rejected
table cases for missing/empty `model`, missing/empty `input`, `input` array,
unknown top-level/nested field, `stream:true`, `store:true`, non-empty `tools`,
non-`none` tool choice, `text` without `format`, invalid format
name/strict/schema, and each byte limit. Assert exact status/code/param. Add
round-trip cases proving an absent JSON Schema `description` remains nil while an
explicit `description:""` remains present through decoding, request weighting,
prompt construction, and response echoing.

- [ ] **Step 5: Implement the closed request decoder**

`DecodeRequest` must call `safejson.Parse`, assert an allowed top-level key set,
and use typed helper functions (`requiredString`, `optionalStringOrNull`,
`optionalExactBool`, `optionalEmptyArray`) over `map[string]any`. Never unmarshal
unknown data directly into a permissive struct.

For a JSON Schema format:

1. require exactly `type`, `name`, `strict`, `schema`, with optional
   `description`;
2. require `type == "json_schema"` and `strict == true`;
3. validate the name with `^[A-Za-z0-9_-]{1,64}$`;
4. `json.Marshal` the already duplicate-checked schema object;
5. enforce `limits.SchemaBytes` before constructing `core.OutputFormat`; never
   use a hard-coded 32 KiB in the decoder.

Recognized fields with a wrong JSON type return `invalid_request`; recognized but
unsupported values return `unsupported_parameter`; unknown fields also return
`unsupported_parameter`; syntax/duplicates/trailing data return `invalid_json`.
`safejson.ErrEncoding`, `safejson.ErrNUL`, root/type failures, and request parser-limit failures
return `invalid_request`; no mapped error contains a token, key, value, decoder
error, or request fragment.
Use stable dotted `param` paths: top-level names (`model`, `stream`), nested names
(`text.format.type`, `text.format.schema`), and `nil` for whole-body syntax or
duplicate-key errors whose duplicate location is intentionally not exposed.

- [ ] **Step 6: Verify strict decoding**

Run:

```bash
gofmt -w internal/safejson internal/httpapi
go test ./internal/safejson ./internal/httpapi
go test ./...
```

Expected: all tests PASS, and rejected requests never call any provider code.

- [ ] **Step 7: Record the no-commit checkpoint**

Run:

```bash
rg -n 'DisallowUnknownFields|safejson.Parse|unsupported_parameter' internal
```

Expected: the request path visibly uses the closed decoder and has no permissive
fallback. Do not initialize Git.

---

### Task 4: Portable JSON Schema Profile and Output Validation

**Files:**

- Create: `internal/schema/schema.go`
- Create: `internal/schema/schema_test.go`
- Modify: `go.mod`
- Create/Modify: `go.sum`

**Interfaces:**

- Consumes: `core.OutputFormat`, `safejson.Parse`.
- Produces: `schema.Compile(format core.OutputFormat, limits schema.Limits)
  (*schema.Compiled, error)`.
- Produces: `(*schema.Compiled).Validate(raw []byte) (string, error)`.
- Task 15 maps compile errors to `invalid_json_schema` and output errors to
  `structured_output_invalid`.

- [ ] **Step 1: Pin the JSON Schema dependency**

Run:

```bash
go get github.com/santhosh-tekuri/jsonschema/v6@v6.0.2
go mod tidy
```

Expected: `go.mod` and `go.sum` record v6.0.2; no other direct dependency is
introduced.

- [ ] **Step 2: Write failing profile tests**

Create tests for the approved allowlist and limits. This valid schema must compile:

```go
valid := core.OutputFormat{
	Type: core.FormatJSONSchema,
	Name: "result",
	Schema: json.RawMessage(`{
	  "type":"object",
	  "properties":{
	    "name":{"type":"string","minLength":1,"maxLength":20},
	    "count":{"type":"integer","minimum":0,"maximum":10}
	  },
	  "required":["name","count"],
	  "additionalProperties":false
	}`),
}
```

Table-reject `$ref`, `$defs`, `anyOf`, `oneOf`, `allOf`, `pattern`, `format`,
`uniqueItems`, missing root type, unknown type names, type arrays, non-object root, missing
`additionalProperties:false`, optional properties, tuple items, 513 nodes, depth
33, 101 properties, and 257 enum entries.
Add exact numeric-bound cases beyond IEEE-754 precision
(`9007199254740992`/`9007199254740993`), equivalent and adjacent exponent versus
decimal spellings, positive/negative fractions, and reversed exclusive bounds.

Create output tests for valid JSON, schema mismatch, duplicate key, fenced JSON,
trailing prose, root array, depth 129, a 129-byte numeric token, and output larger
than 1 MiB. Compile the same schema under a smaller and a larger non-default
`SchemaBytes` limit to prove the configured cap—not a package constant—controls
both request decoding and compilation.

- [ ] **Step 3: Run the tests and observe failure**

Run:

```bash
go test ./internal/schema
```

Expected: FAIL because `Compile` and `Validate` are undefined.

- [ ] **Step 4: Implement profile preflight and no-fetch compilation**

Define:

```go
type Limits struct {
	SchemaBytes, MaxNodes, MaxDepth, MaxProperties, MaxEnum int
	OutputBytes, OutputDepth, NumberBytes                   int
}

type Compiled struct {
	schema *jsonschema.Schema
	limits Limits
}
```

`DefaultLimits(schemaBytes, outputBytes int)` returns the configured byte caps
plus 512 nodes, schema depth 32, 100 properties, 256 enum entries, output depth
128, and 128 numeric-token bytes. It rejects non-positive/configuration-overflow
values. `Compile` also validates every field so a partial zero-value limit set
cannot weaken enforcement, and independently rejects
`len(format.Schema) > limits.SchemaBytes` before parsing. The independent check
prevents direct internal callers from bypassing the HTTP decoder.

Parse the schema through `safejson.Parse` into `schemaValue` and walk with an
explicit context (`schema`, `properties map`, or literal data). Count every JSON
node/depth, but apply the keyword allowlist only to schema objects; property names
inside `properties` and literal object keys inside `enum`/`const` are data, not
schema keywords. Reject every schema keyword outside this exact set:

```go
var allowed = map[string]struct{}{
	"type": {}, "properties": {}, "required": {},
	"additionalProperties": {}, "items": {},
	"enum": {}, "const": {}, "minLength": {}, "maxLength": {},
	"minItems": {}, "maxItems": {}, "minProperties": {},
	"maxProperties": {}, "minimum": {}, "maximum": {},
	"exclusiveMinimum": {}, "exclusiveMaximum": {},
	"description": {}, "title": {},
}
```

Validate each keyword's JSON type. Length/item/property counts must be
non-negative integers and their min/max pairs ordered; numeric
minimum/maximum/exclusive bounds may be negative JSON numbers and must be
internally ordered. Convert each validated `json.Number` lexical value directly
to an exact `math/big.Rat` (or an equivalently exact decimal rational parser),
require full-string consumption, and compare rationals. Never call `Float64`,
`ParseFloat`, or compare through `float64`; exponent and >53-bit cases must retain
their exact ordering.
Require the root schema to contain exactly the string `type:"object"`. At every
schema node where `type` appears, require one string from `object`, `array`,
`string`, `number`, `integer`, `boolean`, or `null`; reject a missing root type,
type arrays, and unknown type names before library compilation.
For every schema whose `type` is `"object"`, require
`additionalProperties == false` and require the duplicate-free `required` set to
equal the `properties` key set. `properties` values and `items` are recursively
walked as schemas; `enum` and `const` values are walked only for complexity.
Compile only after preflight:

```go
compiler := jsonschema.NewCompiler()
compiler.DefaultDraft(jsonschema.Draft2020)
compiler.UseLoader(rejectLoader{})
if err := compiler.AddResource(
	"urn:ai-cli-gateway:schema",
	schemaValue,
); err != nil {
	return nil, err
}
compiled, err := compiler.Compile("urn:ai-cli-gateway:schema")
```

`rejectLoader` implements `jsonschema.URLLoader` as
`Load(string) (any, error)` and always returns a fixed internal error. The
preflight already rejects all reference/identifier keywords; the loader is an
independent no-network/no-filesystem backstop.

The error string is internal only; public mapping never includes it.

- [ ] **Step 5: Implement exact output validation**

`Validate` must:

1. reject `len(raw) > OutputBytes`;
2. parse via `safejson.Parse` with output depth/number limits;
3. require the parsed value to remain an object;
4. call `c.schema.Validate(value)`;
5. return `string(raw)` only after all checks pass.

Never strip Markdown fences, locate a substring, repair, or retry.

- [ ] **Step 6: Verify schema behavior and dependency closure**

Run:

```bash
gofmt -w internal/schema
go test ./internal/safejson ./internal/schema
go test ./...
go mod tidy
go mod verify
```

Expected: all tests PASS and module verification succeeds.

- [ ] **Step 7: Record the no-commit checkpoint**

Run:

```bash
go list -m all
```

Expected direct runtime dependency: jsonschema v6.0.2 only at this stage. Do not
initialize Git.

---

### Task 5: Strict TOML Configuration and Defaults

**Files:**

- Create: `internal/config/types.go`
- Create: `internal/config/load.go`
- Create: `internal/config/load_test.go`
- Create: `internal/config/defaultroot_unix.go`
- Create: `internal/config/defaultroot_windows.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**

- Consumes: `core.Model`, `core.NewRegistry`.
- Produces: `config.Load(path string) (config.Config, error)`.
- Produces: `config.Decode(r io.Reader) (config.Config, error)`.
- Produces exact `Server`, `Runtime`, `Provider`, `Queue`, `Process`, and `Model`
  settings consumed by Tasks 6-17.

- [ ] **Step 1: Pin the strict TOML decoder**

Run:

```bash
go get github.com/pelletier/go-toml/v2@v2.4.3
go mod tidy
```

Expected: go-toml v2.4.3 becomes a direct dependency.

- [ ] **Step 2: Write failing configuration tests**

Build a minimal valid document with a test helper that derives runtime,
executable, and config-home values from `t.TempDir()` and `filepath.Join`, then
TOML-quotes them. Do not hard-code Unix paths in tests that run on Windows. Its
logical shape is:

```toml
[server]
listen = "127.0.0.1:8080"
api_key_env = "AI_CLI_GATEWAY_API_KEY"

[runtime]
root = "/tmp/ai-cli-gateway-test"

[providers.codex]
executable = "/opt/bin/codex"
config_home = "/srv/ai-cli-gateway/codex"

[[models]]
id = "codex-default"
provider = "codex"
provider_model = "gpt-test"
```

Assert defaults exactly match the design. Add failures for unknown TOML keys,
relative runtime/executable/config paths, non-loopback or hostname listeners,
duplicate aliases, undeclared providers, empty provider model, invalid
concurrency/queue/timeout/limit values, and a non-empty `api_key_env` that does
not match `^[A-Z_][A-Z0-9_]*$`.
For every defaulted numeric/duration field, separately prove omission applies the
default while an explicitly encoded zero or negative value is rejected.
Reject duplicate or invalid credential environment names, the gateway API-key
name in any provider, and cross-provider credential names. Reject config input
above 1 MiB or more than 1,024 models. On Unix, production `prefix_args` must be
empty. On Windows, it must be empty for a native executable or contain exactly
one absolute `.js`/`.mjs` provider entrypoint when the executable is a verified
absolute `node.exe`; reject every other count, extension, or option-like value.
Adapter integration tests may construct a `ProviderConfig` directly with a
test-only fake-mode prefix, but no production TOML accepts that escape hatch.

- [ ] **Step 3: Run tests and observe failure**

Run:

```bash
go test ./internal/config
```

Expected: FAIL because the configuration package is absent.

- [ ] **Step 4: Implement exact structures and duration decoding**

`internal/config/types.go` must define:

```go
type Config struct {
	Server    Server              `toml:"server"`
	Runtime   Runtime             `toml:"runtime"`
	Providers map[string]Provider `toml:"providers"`
	Models    []Model             `toml:"models"`
}

type Server struct {
	Listen            string `toml:"listen"`
	APIKeyEnv         string `toml:"api_key_env"`
	HTTPBodyBytes     int64  `toml:"http_body_bytes"`
	InputBytes        int    `toml:"input_bytes"`
	InstructionsBytes int    `toml:"instructions_bytes"`
	SchemaBytes       int    `toml:"schema_bytes"`
	HandlerLimit      int    `toml:"handler_limit"`
	BodyReaderLimit   int    `toml:"body_reader_limit"`
	MaxHeaderBytes    int    `toml:"max_header_bytes"`
	ReadHeaderTimeout Duration `toml:"read_header_timeout"`
	BodyReadTimeout   Duration `toml:"body_read_timeout"`
	IdleTimeout       Duration `toml:"idle_timeout"`
	ShutdownTimeout   Duration `toml:"shutdown_timeout"`
}

type Runtime struct {
	Root           string   `toml:"root"`
	TermGrace      Duration `toml:"term_grace"`
	CleanupTimeout Duration `toml:"cleanup_timeout"`
	StdoutBytes    int64    `toml:"stdout_bytes"`
	StderrBytes    int64    `toml:"stderr_bytes"`
	FinalBytes     int64    `toml:"final_bytes"`
}

type Provider struct {
	Executable       string   `toml:"executable"`
	PrefixArgs       []string `toml:"prefix_args"`
	ConfigHome       string   `toml:"config_home"`
	CredentialEnv    []string `toml:"credential_env"`
	Concurrency      int      `toml:"concurrency"`
	QueueSize        int      `toml:"queue_size"`
	QueueBytes       int64    `toml:"queue_bytes"`
	QueueTimeout     Duration `toml:"queue_timeout"`
	ExecutionTimeout Duration `toml:"execution_timeout"`
}

type Model struct {
	ID            string `toml:"id"`
	Provider      string `toml:"provider"`
	ProviderModel string `toml:"provider_model"`
	Created       int64  `toml:"created"`
}
```

`Duration.UnmarshalText` calls `time.ParseDuration` and rejects non-positive
values. The exported normalized types remain concrete as above, but TOML must
decode first into an unexported presence-aware mirror whose defaulted
string/numeric/duration fields are pointers. Default only nil/absent fields;
explicit empty values that are not documented as optional, explicit zero, and
explicit negatives are validation errors. `Model.Created` deliberately remains
a concrete integer because explicit/default zero are both valid. Keep defaults
in one `normalize` function:

```text
listen                  127.0.0.1:8080
http_body_bytes         1048576
input_bytes             524288
instructions_bytes      262144
schema_bytes            32768
handler_limit           128
body_reader_limit       32
max_header_bytes        16384
read_header_timeout     5s
body_read_timeout       15s
idle_timeout            60s
shutdown_timeout        15s
term_grace              2s
cleanup_timeout         5s
stdout_bytes            2097152
stderr_bytes            262144
final_bytes             1048576
provider concurrency    1
provider queue_size     32
provider queue_bytes    16777216
provider queue_timeout  30s
provider execution      5m
model created           0
```

- [ ] **Step 5: Implement strict decode and structural validation**

Use:

```go
raw, err := io.ReadAll(io.LimitReader(r, (1<<20)+1))
if err != nil || len(raw) > 1<<20 {
	return Config{}, ErrConfigTooLarge
}
var wire wireConfig
dec := toml.NewDecoder(bytes.NewReader(raw))
dec.DisallowUnknownFields()
if err := dec.Decode(&wire); err != nil {
	return Config{}, fmt.Errorf("decode config: %w", err)
}
cfg, err := normalize(wire)
if err != nil {
	return Config{}, err
}
if err := validate(cfg); err != nil {
	return Config{}, err
}
```

Parse `Server.Listen` with `net.SplitHostPort`, then `net.ParseIP`; require
`ip.IsLoopback()` and a numeric port in 1-65535. Resolve the default runtime root below `os.TempDir()` when
omitted: `ai-cli-gateway-<effective-uid>` on Unix and `ai-cli-gateway` under the
already per-user Windows temp directory. Implement that helper behind
`//go:build !windows` and `//go:build windows`. Structural validation checks
syntax and references but does not stat executables or authenticate providers;
those are readiness checks in Task 14.
Validate every count, byte cap, and duration as positive and cap values before
converting between `int64` and `int`. Require `BodyReaderLimit <= HandlerLimit`
and `FinalBytes <= StdoutBytes`. Using checked duration arithmetic, require
`ShutdownTimeout >= TermGrace + 2*CleanupTimeout + 1s` so cancellation,
containment verification, and request-directory cleanup fit inside the
application drain deadline. Enforce absolute structural ceilings:

```text
HTTP body/input/instructions/final   16 MiB each
schema                              1 MiB
stdout                              64 MiB
stderr                              16 MiB
header bytes                        1 MiB
handler limit                       4096
body-reader limit                   256
provider concurrency                64
provider queue entries              4096
provider queued bytes               1 GiB
any configured duration             24h
```

Require `InputBytes`, `InstructionsBytes`, and `SchemaBytes` not to exceed
`HTTPBodyBytes`.

Require at least one known provider and one model. Provider map keys are exactly
`codex`, `claude`, or `gemini`; every provider requires absolute non-empty
`executable` and `config_home` strings, and every model references a declared
provider. Validate provider-model values with the Task 10 trusted-argument rules
through Task 2's `core.ValidateProviderModel`.
Validate the closed platform `prefix_args` shape, UTF-8/NUL/control/size here,
while entrypoint filesystem ownership, symlink resolution, version, and auth
checks remain operational doctor checks. Native Unix execution has no arbitrary
leading option injection surface.

Validate `CredentialEnv` against provider-specific closed sets:

```text
codex:  (none; use the user-authenticated dedicated CODEX_HOME)
claude: ANTHROPIC_API_KEY
gemini: GEMINI_API_KEY, GOOGLE_API_KEY, GOOGLE_APPLICATION_CREDENTIALS,
        GOOGLE_CLOUD_PROJECT, GOOGLE_CLOUD_LOCATION
```

Reject duplicates and the configured gateway `APIKeyEnv` for every provider.
Codex requires an empty list in this MVP: its readiness proof is the official
`login status` command against the user-authenticated dedicated config home,
and the gateway does not support one-off key override semantics. Claude may use
an empty list with its dedicated authenticated config home. Gemini is
structurally valid with an empty list so `doctor` can report `not_ready`, but
never becomes ready without exactly one supported explicit profile:
`GEMINI_API_KEY`; `GOOGLE_API_KEY`; or
`GOOGLE_APPLICATION_CREDENTIALS` + `GOOGLE_CLOUD_PROJECT` +
`GOOGLE_CLOUD_LOCATION`. Reject partial, mixed, duplicate, or unknown Gemini
profiles. The gateway owns auth selection in its request-local settings;
`GOOGLE_GENAI_USE_VERTEXAI` is not accepted as a user credential or selector.

- [ ] **Step 6: Verify config behavior**

Run:

```bash
gofmt -w internal/config
go test ./internal/config ./internal/core
go test ./...
go mod tidy
go mod verify
```

Expected: all tests PASS, unknown TOML keys fail, and defaults match the design.

- [ ] **Step 7: Record the no-commit checkpoint**

Run:

```bash
rg -n '(api_key|credential).*=' internal/config
```

Expected: configuration stores environment-variable names only; no secret values
or token fields exist. Do not initialize Git.

---

### Task 6: Cancelable Bounded FIFO Scheduler

**Files:**

- Create: `internal/scheduler/scheduler.go`
- Create: `internal/scheduler/scheduler_test.go`

**Interfaces:**

- Produces: `scheduler.New(limits scheduler.Limits)
  (*scheduler.Scheduler, error)`.
- Produces: `(*Scheduler).Do(ctx context.Context, weight int64,
  work func(context.Context) error) error`.
- Produces: `(*Scheduler).Stats() scheduler.Stats`, containing only queued count,
  queued bytes, and running count.
- Produces: `(*Scheduler).Shutdown(ctx context.Context) error`.
- Produces sentinel errors `ErrQueueFull`, `ErrQueueTimeout`,
  `ErrCanceled`, and `ErrShuttingDown` for Task 15 mapping.

Define:

```go
type Limits struct {
	Concurrency int
	QueueSize   int
	QueueBytes  int64
	QueueTimeout time.Duration
}

type Stats struct {
	Queued      int
	QueuedBytes int64
	Running     int
}
```

`New` rejects non-positive fields and values above the absolute Task 5 ceilings;
it never silently substitutes defaults. Config owns the documented defaults and
passes a complete validated `Limits` value.

- [ ] **Step 1: Write failing scheduler tests**

Create deterministic tests using channels, never sleeps, for:

```go
func TestConcurrencyAndFIFO(t *testing.T) {
	s, err := New(Limits{
		Concurrency: 1, QueueSize: 2, QueueBytes: 100,
		QueueTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := s.Shutdown(context.Background()); err != nil {
			t.Fatal(err)
		}
	})

	block := make(chan struct{})
	started := make(chan int, 3)
	run := func(id int) func(context.Context) error {
		return func(context.Context) error {
			started <- id
			if id == 0 {
				<-block
			}
			return nil
		}
	}

	errs := make(chan error, 3)
	go func() { errs <- s.Do(context.Background(), 1, run(0)) }()
	if id := <-started; id != 0 {
		t.Fatalf("first=%d", id)
	}
	go func() { errs <- s.Do(context.Background(), 1, run(1)) }()
	waitForStats(t, s, Stats{Queued: 1, QueuedBytes: 1, Running: 1})
	go func() { errs <- s.Do(context.Background(), 1, run(2)) }()
	waitForStats(t, s, Stats{Queued: 2, QueuedBytes: 2, Running: 1})
	close(block)
	if id := <-started; id != 1 {
		t.Fatalf("second=%d", id)
	}
	if id := <-started; id != 2 {
		t.Fatalf("third=%d", id)
	}
}
```

Add focused tests for queue count full, queued byte full, canceled-before-enqueue,
canceled while queued, queue deadline, cancellation after dequeue but before work,
permit retention until work returns, shutdown rejecting new work, shutdown
canceling queued work, and 10,000 cancel/dequeue race iterations under the race
detector. Add active-work cause tests for caller-only cancellation,
shutdown-only cancellation, each ordered race, and a barrier case where both
contexts are already done; the documented both-ready precedence must be stable
for 10,000 race iterations.

`waitForStats` uses a bounded test deadline plus `runtime.Gosched`, not a timing
sleep, so enqueue order is established before the next goroutine starts.

- [ ] **Step 2: Run tests and observe failure**

Run:

```bash
go test ./internal/scheduler
```

Expected: FAIL because the scheduler package is absent.

- [ ] **Step 3: Implement explicit item states**

Use a mutex-protected `container/list` rather than a channel semaphore:

```go
type itemState uint8

const (
	stateQueued itemState = iota
	stateStarting
	stateRunning
	stateDone
)

type item struct {
	caller      context.Context
	queueCtx    context.Context
	queueCancel context.CancelFunc
	weight      int64
	work        func(context.Context) error
	done        chan error
	state       itemState
	elem        *list.Element
}

type Scheduler struct {
	mu          sync.Mutex
	cond        *sync.Cond
	queue       list.List
	queuedBytes int64
	running     int
	limits      Limits
	closed      bool
	stop        context.Context
	stopCancel  context.CancelCauseFunc
	workers     sync.WaitGroup
}
```

`Stats` locks, snapshots the three counters, and returns no request references or
data.

`Do` validates positive weight, retains the original caller context, creates a
separate context for the configured queue timeout, appends one item, and selects
among `done`, caller cancellation, and queue timeout. Those two contexts together
make the effective queue deadline the earlier one while preserving the error
category. Check `queuedBytes + weight` without integer overflow before enqueue.
A queue timeout applies only while `stateQueued`; after dequeue it is
canceled and can never terminate running work. On caller cancellation or queue
timeout, `Do` locks and removes/completes the item only when `stateQueued`; if a
worker has changed it to `stateStarting`/`stateRunning`, `Do` waits for `done`
instead of returning a stale queue error. The worker receives caller cancellation
through its run context, finishes containment/cleanup, and sends to the buffered
channel without blocking. Before returning any queued terminal result, classify
under the scheduler lock with fixed precedence: scheduler shutdown, then caller
cancellation, then queue timeout. Thus when both signals are already observable,
shutdown deterministically returns `ErrShuttingDown`; a caller-only cancellation
returns `ErrCanceled`.

- [ ] **Step 4: Implement workers and shutdown**

Each worker must:

1. wait on `cond` while queue empty and not closed;
2. pop only the front item;
3. decrement queued bytes exactly once;
4. set `stateStarting`, cancel the queue timer, and recheck caller and scheduler
   contexts;
5. derive a run context with `context.WithCancelCause`; two
   `context.AfterFunc` registrations cancel it with private
   `causeCallerCanceled` or `causeSchedulerShutdown`, and their stop functions
   are always called to avoid callback retention;
6. set `stateRunning`, increment `running`, invoke work, then decrement `running`
   and set `stateDone`;
7. translate the terminal cause under the scheduler lock with the same fixed
   precedence—shutdown if both are observable, otherwise caller cancellation,
   otherwise the work result—and send exactly one error to the buffered channel.

`Shutdown` marks closed, removes and completes every queued item with
`ErrShuttingDown`, calls `stopCancel(causeSchedulerShutdown)`, broadcasts, and
waits for worker completion or its own deadline. Gateway therefore never guesses
the cause from a plain `context.Canceled`.

- [ ] **Step 5: Verify focused, race, and package tests**

Run:

```bash
gofmt -w internal/scheduler
go test ./internal/scheduler
go test -race ./internal/scheduler
go test ./...
```

Expected: all tests PASS with no race, leaked goroutine, or negative byte count.

- [ ] **Step 6: Record the no-commit checkpoint**

Run the stress case three times:

```bash
go test -race -count=3 ./internal/scheduler
```

Expected: PASS three times. Do not initialize Git.

---

### Task 7: Locked Runtime Root, Request Files, Janitor, and Fake CLI

**Files:**

- Create: `internal/process/types.go`
- Create: `internal/process/root.go`
- Create: `internal/process/root_unix.go`
- Create: `internal/process/root_windows.go`
- Create: `internal/process/root_test.go`
- Create: `internal/testcli/fake.go`
- Create: `internal/testcli/spawn_unix.go`
- Create: `internal/testcli/spawn_windows.go`
- Create: `internal/testcli/cmd/fake-ai-cli/main.go`
- Create: `internal/testutil/fakecli.go`
- Modify: `go.mod`
- Modify: `go.sum`

**Interfaces:**

- Produces: `process.CommandSpec`, `FileSpec`, `Result`, `Limits`, `Runtime`,
  `RunError`, and `ErrorKind`.
- Produces: `process.OpenRoot(path string) (*process.Root, error)`.
- Produces: `(*Root).Prepare(id string) (Runtime, error)`,
  `Materialize(Runtime, []FileSpec) error`,
  `Cleanup(context.Context, Runtime) error`, `Janitor(context.Context) error`,
  and `Close() error`.
- Produces: `testutil.BuildFakeCLI(t testing.TB) string` and
  `testutil.BuildGateway(t testing.TB) string`.

- [ ] **Step 1: Pin the OS syscall module**

Run:

```bash
go get golang.org/x/sys@v0.47.0
go mod tidy
```

Expected: x/sys v0.47.0 is a direct dependency.

- [ ] **Step 2: Write failing runtime-root tests**

Test:

- creating a missing absolute root as `0700`;
- rejecting relative, symlinked, and non-directory roots; Unix-tagged tests also
  reject special bits and every existing root mode other than exact `0700`,
  including `0000`, `0500`, `0600`, and group/world-accessible modes, while
  Windows tests exercise equivalent unsafe DACLs;
- exclusive second-open failure;
- request ID validation;
- request directory mode `0700`;
- materialized file mode `0600`, relative-name-only, no symlink overwrite;
- cleanup success;
- locked-file cleanup entering quarantine without leaving the root;
- janitor removing stale `request-*` and `quarantine-*` names only;
- `Close` releasing the lock.

Representative test:

```go
func TestRootIsExclusive(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "runtime")
	first, err := OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	if _, err := OpenRoot(dir); err == nil {
		t.Fatal("second root unexpectedly acquired lock")
	}
}
```

- [ ] **Step 3: Run tests and observe failure**

Run:

```bash
go test ./internal/process
```

Expected: FAIL because runtime-root types do not exist.

- [ ] **Step 4: Implement process contract types**

Create `internal/process/types.go`:

```go
type FileSpec struct {
	Name string
	Data []byte
	Mode fs.FileMode
}

type CommandSpec struct {
	Executable string
	Args       []string
	Env        []string
	Dir        string
	Stdin      []byte
	Files      []FileSpec
}

type Result struct {
	Stdout      []byte
	Stderr      []byte
	StdoutTotal int64
	StderrTotal int64
	ExitCode    int
	StopReason  StopReason
	StopAction  StopAction
}

type Limits struct {
	Execution   time.Duration
	TermGrace   time.Duration
	Cleanup     time.Duration
	StdoutBytes int64
	StderrBytes int64
}

type Runtime struct {
	ID  string
	Dir string
}

type ErrorKind string
type StopReason string
type StopAction string

const (
	ErrorCanceled    ErrorKind = "canceled"
	ErrorTimeout     ErrorKind = "timeout"
	ErrorOutputLimit ErrorKind = "output_limit"
	ErrorStart       ErrorKind = "start"
	ErrorCleanup     ErrorKind = "cleanup"
)

type RunError struct {
	Kind ErrorKind
	Err  error
}
```

Define closed process-level stop reason/action constants for normal exit, caller
cancellation, supervisor timeout, output overflow, cleanup failure and
none/TERM/KILL/TerminateJob. The supervisor records only these constants;
gateway translates them to the corresponding closed `core.ResultMeta` values.

`RunError.Error` returns only the kind and wrapped internal error to internal
callers; it is never placed directly in an API response.

- [ ] **Step 5: Implement safe root ownership and locking**

`OpenRoot` cleans and requires an absolute path, uses `Lstat`, refuses symlinks,
creates with `0700`, and calls the platform ownership/access validator before
opening and locking `.lock`. Unix requires exact directory permission bits
`0700` with no special bits and an exact `0600` lock file; Windows requires the
DACL equivalent below rather than relying on synthesized POSIX mode bits.

Unix `lockFile` uses `unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)` and unlock uses
`LOCK_UN`. Windows uses `windows.LockFileEx`/`UnlockFileEx` with
`LOCKFILE_EXCLUSIVE_LOCK|LOCKFILE_FAIL_IMMEDIATELY`.
Use `//go:build !windows` on `root_unix.go` and
`//go:build windows` on `root_windows.go`.

Unix ownership requires `Stat_t.Uid == uint32(os.Geteuid())` and
the exact mode above. Windows obtains owner/DACL data with the
`x/sys/windows` security APIs, requires the owner SID to equal the process token
user, and rejects write/delete/owner/ACL rights granted by an allow ACE to any SID
other than that user, LocalSystem, or Builtin Administrators. Windows tests create
an unsafe temporary DACL and prove rejection.

`Prepare` accepts only IDs matching `^[A-Za-z0-9_-]{8,80}$`, then uses
`os.Mkdir` with a `request-` prefix and `0700`. `Materialize` accepts a base
filename only, opens with `O_CREATE|O_EXCL|O_WRONLY`, forces `0600`, writes, syncs,
and closes. It never follows a caller path.

`Cleanup` first attempts removal within the passed context. On failure it renames
to `quarantine-<id>` if possible and returns `RunError{Kind: ErrorCleanup}`.
`Janitor` uses `os.ReadDir`, acts only on the two trusted prefixes after root lock
ownership, calls `Lstat`, refuses symlinks, and removes entries within the cleanup
deadline.

- [ ] **Step 6: Implement the fake CLI fixture**

`internal/testcli/fake.go` exports `Main(args []string, stdin io.Reader,
stdout, stderr io.Writer) int`. Parse an explicit `--mode` value and implement
these fixed modes without a shell:

```text
text
echo-stdin
codex-json
claude-json
claude-auth-error
claude-rate-limit
gemini-json
gemini-error
invalid-json
duplicate-json
fenced-json
schema-mismatch
exit-7
flood-stdout
flood-stderr
hang
ignore-term
retry-until-canceled
spawn-child-hold
child-hold
spawn-session-escape
session-escape
```

`spawn-child-hold` uses `os.Executable()` and `exec.Command(exe,
"--mode=child-hold")`, attaches the same stdout/stderr, starts the child, prints
its PID to stderr, and exits. `ignore-term` installs a signal handler and blocks.
Flood modes write fixed 64 KiB blocks until the pipe closes.
`retry-until-canceled` emits no sensitive text and waits in a cancelable fixed
timer loop. The parser accepts `--mode VALUE` or `--mode=VALUE` anywhere in argv,
rejects duplicate/missing mode, and ignores remaining adapter flags so tests can
exercise real command builders without a shell.
Claude auth/rate-limit modes emit pinned `subtype:"success"`,
`is_error:true` result envelopes with `api_error_status` 401 or 429; a
separate Claude execution-error mode emits an `error_during_execution` arm
without `api_error_status` or `result`. Gemini error mode emits the documented
optional `error` object and exits 1.

The Unix-only `spawn-session-escape` helper starts `session-escape` with
`SysProcAttr.Setsid=true`, prints its PID, and exits; the integration harness owns
that escaped PID and always kills/waits it in `t.Cleanup`. The Windows companion
returns a fixed unsupported-mode exit. This fixture demonstrates the documented
Unix process-group boundary and must never leave the escaped test process alive.
Use `//go:build !windows` and `//go:build windows` on the two spawn helper files.

The command entry point is:

```go
package main

import (
	"os"

	"github.com/krkarma777/ai-cli-gateway/internal/testcli"
)

func main() {
	os.Exit(testcli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
```

`testutil.BuildFakeCLI` runs `go build -o <t.TempDir path>
./internal/testcli/cmd/fake-ai-cli`, fails the test with bounded combined output,
and returns the absolute executable path. `BuildGateway` does the same for
`./cmd/ai-cli-gateway`; both outputs live only under `t.TempDir()`.

- [ ] **Step 7: Verify runtime and fake fixture**

Run:

```bash
gofmt -w internal/process internal/testcli internal/testutil
go test ./internal/process ./internal/testcli
go test ./...
GOOS=windows GOARCH=amd64 go test -c \
  -o "${TMPDIR:-/tmp}/process-unit.test.exe" ./internal/process
```

Expected: local tests PASS and the Windows package compiles outside the repository.

- [ ] **Step 8: Record the no-commit checkpoint**

Run:

```bash
rg -n 'RemoveAll|EvalSymlinks|Flock|LockFileEx' internal/process
```

Expected: every recursive removal is rooted behind validated root ownership and
platform locking. Do not initialize Git.

---

### Task 8: Unix Process-group Supervisor and Bounded Capture

**Files:**

- Create: `internal/process/capture.go`
- Create: `internal/process/capture_test.go`
- Create: `internal/process/supervisor.go`
- Create: `internal/process/runner_unix.go`
- Create: `internal/process/supervisor_unix_test.go`
- Create: `internal/process/supervisor_integration_test.go`
- Create: `internal/selftest/selftest.go`
- Create: `internal/selftest/selftest_test.go`
- Modify: `cmd/ai-cli-gateway/main.go`

**Interfaces:**

- Consumes: Task 7 `Root`, `Runtime`, `CommandSpec`, `Result`, `Limits`.
- Produces: `process.NewSupervisor(root *Root, limits Limits)
  (*Supervisor, error)`.
- Produces: `(*Supervisor).Prepare(id string) (Runtime, error)`.
- Produces: `(*Supervisor).Discard(ctx context.Context, runtime Runtime) error`
  for pre-execution build failures.
- Produces: `(*Supervisor).Execute(ctx context.Context, runtime Runtime,
  spec CommandSpec) (Result, error)`.
- Produces: `(*Supervisor).SelfTest(ctx context.Context,
  gatewayExecutable string) error`.

- [ ] **Step 1: Write failing bounded-capture tests**

Verify a collector stores exactly its cap, counts all attempted bytes, signals
overflow once, and continues accepting/discarding bytes so a child cannot block:

```go
func TestCaptureCapsAndSignalsOnce(t *testing.T) {
	overflow := make(chan struct{}, 1)
	c := newCapture(4, overflow)
	if _, err := c.Write([]byte("abcdef")); err != nil {
		t.Fatal(err)
	}
	if got := string(c.Bytes()); got != "abcd" {
		t.Fatalf("bytes=%q", got)
	}
	if c.Total() != 6 {
		t.Fatalf("total=%d", c.Total())
	}
	select {
	case <-overflow:
	default:
		t.Fatal("missing overflow signal")
	}
}
```

- [ ] **Step 2: Write failing Unix integration tests**

Use `//go:build !windows` on `runner_unix.go` and Unix unit tests, and
`//go:build integration && !windows` on
`supervisor_integration_test.go`. With `testutil.BuildFakeCLI`, test:

- stdin echo and exit code zero;
- separate stdout/stderr capture;
- validated-but-vanished executable/start failure with every pipe closed;
- exit code 7;
- 200 ms timeout;
- caller cancellation;
- stdout and stderr overflow;
- TERM ignored then KILL;
- root exits while grandchild holds a pipe;
- 100 repeated cancel/timeout runs;
- request file materialization and cleanup;
- no remaining PGID via `unix.Kill(-pgid, 0) == ESRCH`;
- deliberate `setsid` escape proving that PGID cleanup does not contain the
  escaped process, followed by explicit harness-owned kill/wait cleanup;
- permit-facing `Execute` returns only after cleanup.

Also test `Supervisor.SelfTest` using the built gateway executable's fixed
`__process-selftest` mode; it must reap the self-test child tree
without contacting a provider.

Use channels and deadlines in the test harness; no test sleeps longer than the
configured 50 ms grace and 2 s outer deadline.

- [ ] **Step 3: Run focused tests and observe failure**

Run:

```bash
go test -tags=integration ./internal/process
```

Expected: FAIL because supervisor and Unix runner are absent.

- [ ] **Step 4: Implement concurrent bounded capture**

`capture.Write` locks, increments total, appends only remaining capacity, and uses
`sync.Once` for a non-blocking overflow notification. It always returns
`len(p), nil`, including after overflow, so `io.Copy` keeps draining until
containment termination closes the pipe. Copy the collector totals into
`Result.StdoutTotal` and `Result.StderrTotal`; these are numeric metadata only.

- [ ] **Step 5: Implement Unix launch and termination**

In `runner_unix.go`, validate the absolute executable and exact runtime directory,
then:

```go
// The executable/argv passed here were resolved and validated by doctor.
// The supervisor deliberately owns process-group cancellation.
//nolint:gosec,noctx
cmd := exec.Command(spec.Executable, spec.Args...)
cmd.Dir = spec.Dir
cmd.Env = append([]string{}, spec.Env...)
cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
```

The non-nil empty slice is deliberate: `exec.Cmd.Env == nil` inherits the entire
gateway environment. Validate every supplied entry and test nil/empty
`CommandSpec.Env` with planted gateway/provider secrets; the child must receive
no inherited values.

Create explicit `os.Pipe` pairs for stdin/stdout/stderr. Assign the child ends,
start, record PID as expected PGID, close the parent's copies of child ends, and
write `spec.Stdin` from one bounded goroutine that always closes the parent stdin
writer. Run two `io.Copy` goroutines into captures and `cmd.Wait` in one
goroutine. Cancellation closes the parent stdin writer immediately and joins the
writer; it never waits indefinitely for a child that stopped reading.

The select loop handles:

- normal root exit;
- caller cancellation;
- an internal execution timer;
- stdout or stderr overflow.

Before accepting normal exit/timeout/cancel, poll the captures' atomic overflow
flags; once an overflow flag is set, `ErrorOutputLimit` is immutable. Otherwise
the first committed terminal cause is stored once and later pipe/Wait events
cannot replace it. Add simultaneous exit/timeout/overflow tests to prove the
cause order `output limit` before `timeout`, `timeout` before ordinary non-zero
exit, while an already committed caller cancellation remains cancellation.

All terminal paths call one `sync.Once` termination function. It verifies PGID,
closes stdin, sends `SIGTERM` to `-pgid`, waits the grace timer, sends `SIGKILL`
when the group remains, waits for root `Wait`, waits bounded readers, and polls
`unix.Kill(-pgid, 0)` until `ESRCH` or cleanup deadline.

A successful root exit still terminates any remaining group member before return.
If a reader is still blocked at the cleanup deadline, close the parent read end,
join both reader goroutines, and return `ErrorCleanup`; never abandon a pipe
goroutine. Map only supervisor categories to `RunError`; retain exit status and
numeric stream totals in `Result`.

`internal/selftest.Main` accepts only the exact internal argv
`__process-selftest parent` or `__process-selftest child`. Parent starts the same
resolved gateway executable in child mode without a shell, writes the fixed line
`ready`, and exits immediately; child blocks until terminated.
`cmd/ai-cli-gateway/main.go` dispatches this mode before normal CLI parsing,
never reads provider credentials, and does not include it in public help.
`Supervisor.SelfTest` runs parent mode through the normal containment path,
requires exit zero and exact stdout `ready\n`, and verifies the remaining child,
runtime, and handles are gone.

- [ ] **Step 6: Integrate materialization and cleanup**

`Supervisor.Execute` validates `spec.Dir == runtime.Dir`, calls
`root.Materialize`, executes the platform runner, then always calls
`root.Cleanup` under an independent cleanup context. A cleanup error wins over a
successful or failed provider result and wraps as `ErrorCleanup` while joining the
earlier internal cause; an earlier execution error is retained when cleanup
succeeds. Public mapping never includes joined error text.

`Supervisor.Discard` validates that the runtime belongs to the locked root and
performs the same bounded cleanup without launching a child. Test that adapter
build failure paths cannot strand request directories.

- [ ] **Step 7: Verify Unix lifecycle under race detection**

Run:

```bash
gofmt -w cmd/ai-cli-gateway internal/process internal/selftest
go test ./internal/process ./internal/selftest
go test -race -tags=integration -count=3 ./internal/process
```

Expected: PASS three times with no process, FD, goroutine, temp directory, or data
race leak.

- [ ] **Step 8: Record the no-commit checkpoint**

Run:

```bash
set -euo pipefail
if pgrep -f '[f]ake-ai-cli|__process-[s]elftest'; then
  exit 1
else
  process_scan_status=$?
  test "$process_scan_status" -eq 1 || exit "$process_scan_status"
fi
```

Expected: no fake CLI process. Do not initialize Git.

---

### Task 9: Race-free Windows Job Object Supervisor

**Files:**

- Create: `internal/process/runner_windows.go`
- Create: `internal/process/runner_windows_test.go`
- Create: `internal/process/runner_windows_integration_test.go`
- Modify: `internal/process/supervisor.go`

**Interfaces:**

- Implements the same unexported platform runner used by Task 8.
- No Windows-specific type escapes `internal/process`.
- `Supervisor.Execute` has identical behavior on Unix and Windows.

- [ ] **Step 1: Write Windows-only failing tests**

Use `//go:build windows` on the runner/unit test and
`//go:build integration && windows` on the integration test. Test:

- Windows argument quoting preserves spaces, quotes, backslashes, and Unicode;
- environment block is case-insensitively sorted and double-NUL terminated;
- simultaneous launches inherit only their own three child stdio handles, never
  another launch's pipe or unrelated planted inheritable handles;
- a normal zero-exit child produces stdout/stderr EOF without waiting for cleanup,
  and 100 repeated/concurrent runs return the process handle count to baseline;
- `.cmd`/`.bat` executable rejection;
- child starts suspended, is assigned before resume, then exits;
- Job active process count reaches zero;
- child and grandchild termination, including a successful root exit while its
  descendant keeps a pipe open;
- timeout, cancellation, stdout/stderr overflow;
- locked request directory enters bounded quarantine;
- execution while the test runner is itself inside a Job.

Add an injectable `windowsAPI` interface so the unit test proves call order:

```text
CreateProcess(CREATE_SUSPENDED)
CreateJobObject
SetInformationJobObject(KILL_ON_JOB_CLOSE)
AssignProcessToJobObject
ResumeThread
```

- [ ] **Step 2: Cross-compile to observe the missing implementation**

Run:

```bash
GOOS=windows GOARCH=amd64 go test -c -tags=integration \
  -o "${TMPDIR:-/tmp}/process-integration.test.exe" ./internal/process
```

Expected: FAIL because the Windows runner is undefined.

- [ ] **Step 3: Implement Windows command line and environment helpers**

Use Windows-only `syscall.EscapeArg` for each executable/argument and join with one
space. Reject executable extensions `.cmd` and `.bat` case-insensitively.

Build the child environment from validated `KEY=value` entries, sort using
`strings.ToUpper(key)`, and encode each entry separately to UTF-16 after rejecting
embedded NUL. Build a `[]uint16` manually: append each encoded entry followed by
one zero code unit, then append the extra block terminator; the empty environment
is exactly two zero code units. Never pass an embedded-NUL string to
`windows.UTF16PtrFromString`, which would reject it. Duplicate variable names
ignoring case are an internal error.

- [ ] **Step 4: Implement suspended CreateProcess and Job assignment**

Create inheritable stdin/stdout/stderr pipes and clear inheritance on the parent
ends. Use `STARTUPINFOEX` plus a
`PROC_THREAD_ATTRIBUTE_HANDLE_LIST` containing exactly the three child stdio
handles; set `STARTF_USESTDHANDLES`, pass inheritance enabled only with that
allowlist, and delete the attribute list after creation. Include
`EXTENDED_STARTUPINFO_PRESENT` and use the same bounded stdin-writer lifecycle as
Unix. Call `windows.CreateProcess` with:

```go
flags := uint32(
	windows.CREATE_SUSPENDED |
		windows.CREATE_UNICODE_ENVIRONMENT |
		windows.EXTENDED_STARTUPINFO_PRESENT,
)
```

Immediately create a Job Object and set
`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE` in
`JOBOBJECT_EXTENDED_LIMIT_INFORMATION`. Assign the suspended process, and only
then call `windows.ResumeThread`.

Track every acquired HANDLE in a single all-path ownership ledger with idempotent
close operations. Immediately after `CreateProcess` returns—success or
failure—close the gateway's copies of the child stdin-read, stdout-write, and
stderr-write handles so they can never keep pipes alive. Delete the attribute
list after creation. After successful assignment/resume, close the thread handle;
the stdin writer owns and always closes the parent stdin-write handle; reader
goroutines close their parent read handles only after bounded joins; close the
process handle only after the root wait/exit-code read; close the Job only after
zero-active verification. Every failure unwinds the same ledger in reverse order,
terminating/waiting first when a process exists, with no double-close path.

Never fall back to `exec.Command` or a shell on Windows. The handle-list test
launches two providers concurrently with distinct pipes and a planted inheritable
event, closes each writer independently, and proves neither process inherited
the other handles. Unit tests inject close calls and assert each acquired handle
closes exactly once. Integration tests use `GetProcessHandleCount` before and
after 100 successful, failed-start, timeout, and cancellation cycles and require
return to the quiescent baseline.

- [ ] **Step 5: Implement cancellation and zero-active verification**

Wait for the root with `windows.WaitForSingleObject` in a goroutine. Route every
terminal cause through one idempotent termination routine. On cancel, timeout, or
capture overflow call `windows.TerminateJobObject`; after a normal root exit,
first query the Job and call `TerminateJobObject` when any descendant remains.
Then query `JobObjectBasicAccountingInformation` until `TotalProcesses > 0` and
`ActiveProcesses == 0`, and only then close the Job. Reader goroutines use the
same Task 8 capture implementation; at the cleanup deadline close parent read
handles, join both readers, and return `ErrorCleanup` rather than leaking a
goroutine or handle.

- [ ] **Step 6: Verify Windows compilation locally**

Run:

```bash
gofmt -w internal/process
GOOS=windows GOARCH=amd64 go test -c -tags=integration \
  -o "${TMPDIR:-/tmp}/process-integration.test.exe" ./internal/process
```

Expected: compilation succeeds outside the repository.

- [ ] **Step 7: Verify on a Windows GitHub Actions runner**

After Task 18 creates CI, the Windows matrix command is:

```powershell
go test -race -tags=integration ./internal/process
```

Expected: all Job Object tests PASS. If the runner does not support `-race`,
run the integration test without `-race` on Windows and retain race coverage on
Linux/macOS; do not skip Job integration tests.

- [ ] **Step 8: Record the no-commit checkpoint**

Run:

```bash
rg -n 'CREATE_SUSPENDED|AssignProcessToJobObject|ResumeThread|TerminateJobObject' internal/process
```

Expected: assignment visibly precedes resume and no shell fallback exists. Do not
initialize Git.

---

### Task 10: Provider Adapter Contract, Prompt Framing, Environment, and Versions

**Files:**

- Create: `internal/provider/adapter.go`
- Create: `internal/provider/prompt.go`
- Create: `internal/provider/prompt_test.go`
- Create: `internal/provider/env.go`
- Create: `internal/provider/env_test.go`
- Create: `internal/provider/version.go`
- Create: `internal/provider/version_test.go`

**Interfaces:**

- Consumes: `core.Request`, `core.Model`, `process.Runtime`,
  `process.CommandSpec`, `process.Result`.
- Produces: `provider.Adapter`, `ProviderConfig`, `Health`, `HealthStatus`,
  `ProviderError`, `ProbeRunner`.
- Produces: `BuildPrompt(core.Request, SchemaDelivery) []byte`,
  `BuildEnv(EnvSpec, LookupEnv) ([]string, error)`,
  `ParseVersion(string) (Version, error)` and `Range.Contains(Version)`.

- [ ] **Step 1: Write failing prompt-framing tests**

The wire format is byte-length based:

```text
AI_CLI_GATEWAY/1
INSTRUCTIONS <decimal-bytes-or-NULL>
<exact bytes>
INPUT <decimal-bytes>
<exact bytes>
OUTPUT_CONTRACT <decimal-bytes>
<exact bytes>
```

Test nil and empty instructions distinctly, multibyte Korean, embedded newlines,
text containing fake headers such as `INPUT 999`, leading dash input, and a JSON
Schema. Test both `SchemaInline` and `SchemaFile`: the inline contract contains
the compact schema, while the file contract contains only a fixed statement that
the gateway supplied the schema out-of-band. Assert exact bytes, nil versus empty
description handling, and that no prompt byte appears in an args slice.

- [ ] **Step 2: Write failing environment-isolation tests**

With a fake lookup map containing:

```text
AI_CLI_GATEWAY_API_KEY=<gateway-test-secret>
OPENAI_API_KEY=<unselected-openai-test-secret>
ANTHROPIC_API_KEY=<unselected-anthropic-test-secret>
GEMINI_API_KEY=<selected-gemini-test-secret>
PATH=/attacker-controlled/bin
```

assert a Gemini environment contains only the explicit base variables,
request-local HOME/config/temp variables, `NO_COLOR=1`, safe PATH, and
`GEMINI_API_KEY`; it must not contain the gateway or OpenAI/Anthropic values.
Treat those four angle-bracket values as an exact closed placeholder set, assert
they are pairwise distinct, and do not accept a generic angle-bracket pattern as
a placeholder.
Pass `/validated/provider/bin:/usr/bin:/bin` through `EnvSpec.SafePath` and assert
the lookup-map PATH is ignored. Assert missing explicitly required credential
variables is an error.

- [ ] **Step 3: Write failing version tests**

Test valid versions `0.146.0`, `2.1.220`, and `0.53.0`, decorated output such as
`claude 2.1.220 (Claude Code)`, malformed/pre-release text, and inclusive/exclusive
range boundaries for all three adapters. Reuse Task 2's provider-model tests;
adapters must not introduce a second, divergent argument validator.

- [ ] **Step 4: Run tests and observe failure**

Run:

```bash
go test ./internal/provider
```

Expected: FAIL because the provider package is absent.

- [ ] **Step 5: Define the stable adapter contract**

Create:

```go
type ProviderConfig struct {
	Executable    string
	PrefixArgs    []string
	ConfigHome    string
	CredentialEnv []string
	SafePath      string
	LookupEnv     LookupEnv
}

type LookupEnv func(string) (string, bool)

type SchemaDelivery uint8

const (
	SchemaInline SchemaDelivery = iota
	SchemaFile
)

type ProbeRunner interface {
	RunProbe(
		context.Context,
		func(process.Runtime) (process.CommandSpec, error),
	) (process.Result, error)
}

type Adapter interface {
	Name() core.ProviderName
	SupportedVersion() Range
	Probe(context.Context, ProviderConfig, ProbeRunner) Health
	Build(core.Request, core.Model, ProviderConfig, process.Runtime) (
		process.CommandSpec, error)
	Parse(core.Request, process.Result) (string, error)
}

type HealthStatus string

const (
	HealthReady    HealthStatus = "ready"
	HealthNotReady HealthStatus = "not_ready"
	HealthUnknown  HealthStatus = "unknown"
)

type Health struct {
	Provider     core.ProviderName `json:"provider"`
	Status       HealthStatus      `json:"status"`
	Version      string            `json:"version,omitempty"`
	Auth         string            `json:"auth"`
	Capabilities []string          `json:"capabilities"`
	Problems     []string          `json:"problems,omitempty"`
}
```

`ProviderError` contains an internal category enum
`auth_required`, `rate_limited`, `protocol`, `failed`; its `Error()` string does
not embed stdout/stderr. `ProbeRunner` owns a fresh runtime for each command and
passes it to the adapter's builder callback before execution; this lets probes
construct request-local HOME/temp variables without learning or reusing another
probe's directory. It always discards or cleans the runtime on builder,
execution, or parsing failure.

- [ ] **Step 6: Implement prompt framing**

For absent instructions write `INSTRUCTIONS NULL\n` with no following payload
line; for a present empty string write `INSTRUCTIONS 0\n\n`. This preserves the
API distinction while all present section lengths remain byte counts.

`BuildPrompt` creates fixed output-contract bytes:

- text: `Return only the final answer as plain text.`
- JSON Schema with `SchemaInline`: compact JSON containing
  `type:"json_schema"`, `name`, optional
  `description`, `strict:true`, and `schema`, followed by
  `Return exactly one JSON object and no surrounding text.`
- JSON Schema with `SchemaFile`: the fixed text
  `Follow the JSON Schema supplied in the gateway-owned schema file and return
  exactly one JSON object with no surrounding text.` It contains no schema,
  name, description, instructions, or input bytes.

For each present section, write
`fmt.Fprintf(&buf, "%s %d\n", name, len(value))`, then the exact bytes and one
framing newline. The length is the section byte count, not rune count.

- [ ] **Step 7: Implement minimal environment construction**

`EnvSpec` explicitly supplies runtime HOME/config/temp variables, the
doctor-produced `ProviderConfig.SafePath`,
platform-required variables (`SystemRoot` on Windows), and credential variable
names. `BuildEnv` looks up only those names, requires names to match
`^[A-Za-z_][A-Za-z0-9_]*$`, rejects NUL in names or values, permits `=` inside values,
de-duplicates, sorts by name, and returns new `KEY=value` strings. It never starts from
`os.Environ()`. `ProviderConfig.LookupEnv` is `os.LookupEnv` in production and a
map-backed function in tests; no credential value is stored in TOML, health, or
diagnostic types.

- [ ] **Step 8: Implement numeric version parsing**

`ParseVersion` finds exactly one `major.minor.patch` token using a bounded regexp,
parses each component with `strconv.ParseUint`, and rejects overflow or multiple
version tokens. `Range` stores inclusive minimum and exclusive maximum and
compares the three integer components lexicographically.

- [ ] **Step 9: Verify shared provider behavior**

Run:

```bash
gofmt -w internal/provider
go test ./internal/provider
go test -race ./internal/provider
go test ./...
```

Expected: all tests PASS and planted cross-provider secrets never appear.

- [ ] **Step 10: Record the no-commit checkpoint**

Run:

```bash
rg -n 'os.Environ|exec.Command.*sh|/bin/sh|cmd.exe' internal/provider
```

Expected: no match. Do not initialize Git.

---

### Task 11: Codex CLI Adapter

**Files:**

- Create: `internal/provider/codex/codex.go`
- Create: `internal/provider/codex/codex_test.go`
- Create: `internal/provider/codex/codex_integration_test.go`

**Interfaces:**

- Implements: `provider.Adapter`.
- Consumes: `provider.BuildPrompt`, `provider.BuildEnv`, `process.CommandSpec`,
  `process.FileSpec`, and the Codex version range from Task 10.
- Produces: a fixed `codex exec` invocation, optional
  `output-schema.json`, filtered health, and final stdout.

- [ ] **Step 1: Write failing argv and prompt-separation tests**

Construct a request containing newlines, leading dashes, Unicode, quotes, and the
marker `--model attacker-model`. Assert the executable, args, stdin, environment,
and files separately. The expected text invocation after configured prefix args
is exactly:

```text
--ask-for-approval
never
exec
--ephemeral
--ignore-user-config
--ignore-rules
--strict-config
--sandbox
read-only
--skip-git-repo-check
--color
never
--disable
shell_tool
--disable
unified_exec
--disable
code_mode_host
--disable
apps
--disable
plugins
--disable
remote_plugin
--disable
hooks
--disable
multi_agent
--disable
browser_use
--disable
browser_use_external
--disable
computer_use
--disable
in_app_browser
--disable
image_generation
--disable
skill_search
--disable
skill_mcp_dependency_install
--disable
workspace_dependencies
-c
web_search="disabled"
--model
<trusted configured provider model>
-
```

Assert no instruction, input, schema byte, gateway key, or other provider
credential occurs in `Executable`, `Args`, `Dir`, or a file name. Assert
`CODEX_HOME` is the configured dedicated home and the child HOME/temp variables
point at the request runtime.

- [ ] **Step 2: Write failing schema, parse, and probe tests**

For a JSON Schema request, assert `--output-schema` and the absolute trusted path
`<runtime>/output-schema.json` occur immediately before the final `-`, and the
single `FileSpec` contains only the compact schema with mode `0600`.

Table-test parsing:

- exit zero plus non-empty valid UTF-8 stdout returns stdout exactly;
- empty stdout or invalid UTF-8 returns a `provider.ProviderError` in category
  `protocol`;
- a non-zero exit returns category `failed`;
- stderr is never present in `Error()`.

Use a scripted `ProbeRunner` to assert the ordered probe commands are
`--version`, `--ask-for-approval never exec --help`, `features list`,
`login status`, and `doctor --json`. The second probe both proves the pinned
global approval-option placement is accepted and supplies the exec help used
for the remaining fixed flags. Accept only `>=0.146.0,<0.147.0`, reject a
missing hardening flag/feature, and ensure planted identity/credential values
from doctor output are absent from `Health`.

- [ ] **Step 3: Run tests and observe failure**

Run:

```bash
go test ./internal/provider/codex
```

Expected: FAIL because the adapter does not exist.

- [ ] **Step 4: Implement the fixed Codex adapter**

Define:

```go
type Adapter struct{}

func New() *Adapter
func (a *Adapter) Name() core.ProviderName
func (a *Adapter) SupportedVersion() provider.Range
func (a *Adapter) Build(
	req core.Request,
	model core.Model,
	cfg provider.ProviderConfig,
	rt process.Runtime,
) (process.CommandSpec, error)
func (a *Adapter) Parse(req core.Request, result process.Result) (string, error)
func (a *Adapter) Probe(
	ctx context.Context,
	cfg provider.ProviderConfig,
	runner provider.ProbeRunner,
) provider.Health
```

Keep the hardening args in one immutable package-level slice and copy it before
appending. Validate that `model.Provider == core.ProviderCodex`, validate trusted
`ProviderModel` with `core.ValidateProviderModel`, and append
request-independent values only.

For schema output add:

```go
schemaPath := filepath.Join(rt.Dir, "output-schema.json")
args = append(args, "--output-schema", schemaPath)
files = append(files, process.FileSpec{
	Name: "output-schema.json",
	Data: append([]byte(nil), req.Format.Schema...),
	Mode: 0o600,
})
```

Then append `"-"`. Set `Stdin` to
`provider.BuildPrompt(req, provider.SchemaFile)`, never to an argv value. Tests
assert every schema byte occurs only in `output-schema.json`, not stdin or argv.

- [ ] **Step 5: Implement filtered readiness probing**

Each probe uses small fixed process limits and no request prompt. Parse:

- `codex --version` with `provider.ParseVersion`;
- successful parsing of
  `codex --ask-for-approval never exec --help`, fixed exec flag names from that
  help output, and the exact hardening feature names from
  `codex features list`;
- exit success from `codex login status` as authenticated, without copying its
  text;
- exactly one duplicate-safe JSON object from `codex doctor --json`, retaining
  only the closed shape of `overallStatus`, `schemaVersion`, and the status of
  fixed checks `auth.credentials`, `config.load`, and `installation`; discard
  diagnostic messages and identities. Validate but do not gate readiness on
  aggregate `overallStatus`, because it includes unrelated support-report
  checks. Gate readiness only on the three fixed checks.

Return only provider name, status, numeric version, `auth` as
`authenticated`/`missing`/`unknown`, capabilities
`stdin_prompt`, `ephemeral`, `read_only`, `never_approve`, `schema_file`, and
`feature_hardening`, plus Task 14's fixed problem codes. A probe failure cannot
insert raw stdout, stderr, paths, or account identity into `Health`.

- [ ] **Step 6: Exercise the real adapter against the fake CLI**

The integration test configures the fake executable with trusted prefix args that
select `codex-json` or `echo-stdin`, then passes the resulting `CommandSpec`
through the real supervisor. Assert text and schema requests reach stdin, parse
correctly, clean their runtime, and cannot alter argv. Add non-zero, malformed
output, timeout, and output-flood cases.

Run:

```bash
gofmt -w internal/provider/codex
go test ./internal/provider/codex
go test -race -tags=integration ./internal/provider/codex
go test ./...
```

Expected: all tests PASS.

- [ ] **Step 7: Record the no-commit checkpoint**

Run:

```bash
rg -n 'req\.(Input|Instructions)|Format\.Schema' internal/provider/codex
if rg -n --glob '!**/*_test.go' \
  'exec\.Command.*(sh|cmd)|/bin/sh|cmd\.exe' internal/provider/codex; then
  exit 1
else
  scan_status=$?
  test "$scan_status" -eq 1 || exit "$scan_status"
fi
```

Expected: request bytes occur only in stdin/file construction and the second
search has no match. Do not initialize Git.

---

### Task 12: Claude Code Adapter

**Files:**

- Create: `internal/provider/claude/claude.go`
- Create: `internal/provider/claude/claude_test.go`
- Create: `internal/provider/claude/claude_integration_test.go`
- Modify: `internal/testcli/fake.go`
- Modify: `internal/testcli/fake_test.go`

**Interfaces:**

- Implements: `provider.Adapter`.
- Produces: fixed print-mode argv, `CLAUDE_CONFIG_DIR` isolation, filtered
  readiness, and one `.result` string.
- Does not use Claude's inline `--json-schema` option.

- [ ] **Step 1: Write failing argv and prompt tests**

Assert the exact args following configured prefix args are:

```text
-p
--output-format
json
--no-session-persistence
--safe-mode
--setting-sources
""
--tools
""
--strict-mcp-config
--permission-mode
dontAsk
--disable-slash-commands
--no-chrome
--model
<trusted configured provider model>
```

Each `""` line denotes one empty argv element. Assert instructions, input, and
schema occur only in the length-framed stdin prompt. Assert there is no
`--json-schema`, no request-created file, no prompt argv, and no environment
credential other than explicitly configured Claude names.

Assert the exact isolated environment contains only
`CLAUDE_CONFIG_DIR`, request-local `HOME`/`TEMP`/`TMP`/`TMPDIR` and
`CLAUDE_CODE_TMPDIR`, `NO_COLOR=1`,
`CLAUDE_CODE_SKIP_PROMPT_HISTORY=1`,
`CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`,
`CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1`,
`CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1`, the doctor-validated `PATH`, the
explicitly selected `ANTHROPIC_API_KEY` when configured, and explicitly looked
up `SystemRoot` on Windows. Never inherit the ambient environment.

Add a closed framed-stdin test using the conservative decimal interpretation of
the upstream 10 MB label: exactly `10,000,000` bytes succeeds and one byte over
fails before spawn with a fixed error containing no prompt bytes.

- [ ] **Step 2: Write failing envelope and readiness tests**

Use exact-one-value, duplicate-safe cases:

```json
{"type":"result","subtype":"success","is_error":false,"result":"hello"}
```

Accept only a root object with one string `result`, `is_error:false`, and a
success result type/subtype. Ignore but never expose session and usage metadata.
Reject duplicate `result`, trailing JSON, arrays, missing/wrong-type result,
invalid error-arm combinations, and invalid UTF-8. Exact structured
`is_error:true`/non-zero cases are classified by Step 4 rather than returned as
success. Assert public-facing provider errors never contain the planted prompt,
result, stderr, email, organization, or session ID.

Probe tests assert `--version`, the exact hardened argv followed by `--help`,
and `auth status`, the range `>=2.1.208,<2.2.0`, required fixed flag tokens,
and that auth is classified only by documented exit 0 (authenticated), exit 1
(missing), or any runner/other-exit condition (unknown). Discard auth stdout
and stderr entirely; do not depend on undocumented JSON fields.

- [ ] **Step 3: Run tests and observe failure**

Run:

```bash
go test ./internal/provider/claude
```

Expected: FAIL because the adapter is absent.

- [ ] **Step 4: Implement build and parse**

Create the same constructor and `provider.Adapter` methods as Task 11, returning
`core.ProviderClaude`. Require `model.Provider == core.ProviderClaude` and call
`core.ValidateProviderModel` before appending the fixed args. Set
`CLAUDE_CONFIG_DIR` to the configured dedicated home; set request HOME/temp
and `CLAUDE_CODE_TMPDIR` to the runtime; add the fixed suppression variables
from Step 1; set stdin to
`provider.BuildPrompt(req, provider.SchemaInline)`, and `Files` to nil.
Reject a framed prompt larger than the gateway's conservative decimal
interpretation of Claude's documented 10 MB piped-stdin cap
(`10,000,000` bytes) before returning a command.

Parse the stdout with `safejson.Parse` using fixed envelope depth/number limits.
Require exactly the fields needed to prove success and return only `.result`.
Reject unknown result type combinations as `protocol`. For the pinned result
contract, `api_error_status` exists only on the
`type:"result"`, `subtype:"success"` arm. A structurally proven
`success`, `is_error:true` result with integer status maps 429 to
`rate_limited`, 401/403 to `auth_required`, and every other/absent/null status
to `failed`, without exposing its `result`. Documented terminal subtypes
`error_during_execution`, `error_max_turns`, `error_max_budget_usd`, and
`error_max_structured_output_retries` carry an `errors` array rather than
`api_error_status` and map to `failed`. Never inspect human-readable result,
errors, or localized stderr text.

Use closed precedence: a structurally proven success-arm 401/403/429 may
provide its stable category even on nonzero exit; otherwise nonzero exit is
failed; zero-exit malformed/impossible output is protocol; valid error arms and
unrecognized `success + is_error:true` are failed; only
`success + is_error:false + string result` returns output.

- [ ] **Step 5: Implement filtered probing and fake integration**

`Probe` parses the numeric version, verifies the hardened `--help` flag
contract, then runs `claude auth status`. Because `provider.Adapter.Probe` is a
provider-level interface and does not receive a routed model, the help-only
probe uses the documented fixed alias `sonnet` as its `--model` value; it never
starts a model request. Retain only a coarse
`authenticated`/`missing`/`unknown` value from the documented exit code;
discard stdout/stderr without JSON parsing. On success, report only
capabilities `stdin_prompt`, `json_envelope`, `no_session_persistence`,
`empty_settings`, `empty_tools`, and `safe_mode`.

Correct the fake Claude auth/rate-limit envelopes to
`subtype:"success", is_error:true, api_error_status:<status>, result:<fixed>`
and add an `error_during_execution` fixture without `result` or
`api_error_status`. A narrow Claude stdin-byte-count fixture may be added to
prove the real supervisor path receives the framed prompt without echoing its
contents.

Run the real build/parse path through the fake CLI for text, JSON Schema text,
duplicate envelope, non-zero exit, hang, and stdout/stderr flood. Assert the
scheduler-facing execution returns only after runtime deletion.

Run:

```bash
gofmt -w internal/provider/claude
go test ./internal/provider/claude
go test -race -tags=integration ./internal/provider/claude
go test ./...
```

Expected: all tests PASS.

- [ ] **Step 6: Record the no-commit checkpoint**

Run:

```bash
if rg -n --glob '!**/*_test.go' -- \
  '--json-schema|session_id|os\.Environ|exec\.Command.*(sh|cmd)' \
  internal/provider/claude; then
  exit 1
else
  scan_status=$?
  test "$scan_status" -eq 1 || exit "$scan_status"
fi
```

Expected: `--json-schema`, environment inheritance, and shell execution have no
matches; session identifiers appear only in rejection/leakage tests. Do not
initialize Git.

---

### Task 13: Gemini CLI Adapter with Disposable Home

**Files:**

- Modify: `internal/config/load.go`
- Modify: `internal/config/load_test.go`
- Modify: `internal/process/root.go`
- Modify: `internal/process/root_test.go`
- Create: `internal/provider/gemini/gemini.go`
- Create: `internal/provider/gemini/gemini_test.go`
- Create: `internal/provider/gemini/gemini_integration_test.go`
- Modify: `internal/testcli/fake.go`
- Modify: `internal/testcli/fake_test.go`

**Interfaces:**

- Implements: `provider.Adapter`.
- Produces: fixed headless argv, one request-local Gemini settings file,
  environment/external-credential-only readiness, and one `.response` string.
- Never points `GEMINI_CLI_HOME` at a persistent authenticated profile.

This production contract remains unchanged by the 2026-08-02 external-drift
amendment. The gateway accepts only the three credential shapes below and never
reuses cached personal OAuth. Google stopped the consumer Login-with-Google path
for Gemini Code Assist for individuals, Google AI Pro, and Google AI Ultra on
2026-06-18; it says Code Assist Standard and Enterprise plus paid API-key access
remain, while current official docs also describe API-key and Vertex tiers. The
gateway does not infer availability, billing tier, quota, entitlement, or live
credential validity; provider execution is authoritative. Antigravity CLI is
outside this adapter.

- [ ] **Step 1: Write failing build and credential-boundary tests**

Assert the exact args following configured prefix args are:

```text
--output-format
json
--approval-mode
default
-e
none
--model
<trusted configured provider model>
```

Assert `GEMINI_CLI_HOME` and HOME point into the request runtime. Also require
`GEMINI_CLI_SYSTEM_SETTINGS_PATH` and
`GEMINI_CLI_SYSTEM_DEFAULTS_PATH` to name two nonexistent files below the
request-local `.gemini` directory so host system settings cannot override the
adapter. Assert the single materialized request file is
`.gemini/settings.json`, mode `0600`, containing the deterministic hardened
shape:

```json
{
  "advanced": {"ignoreLocalEnv": true},
  "experimental": {"enableAgents": false},
  "hooksConfig": {"enabled": false},
  "mcp": {"allowed": []},
  "mcpServers": {},
  "privacy": {"usageStatisticsEnabled": false},
  "security": {
    "auth": {"selectedType": "<profile-selected type>"},
    "folderTrust": {"enabled": false}
  },
  "skills": {"enabled": false},
  "telemetry": {"enabled": false, "logPrompts": false},
  "tools": {"core": []}
}
```

The paths passed through environment are request-local and trusted. No schema,
prompt, or credential value occurs in argv or files. Replace arbitrary
credential subsets with exactly three closed profiles:

```text
GEMINI_API_KEY
  -> selectedType "gemini-api-key"
GOOGLE_API_KEY
  -> selectedType "vertex-ai"
GOOGLE_APPLICATION_CREDENTIALS + GOOGLE_CLOUD_PROJECT + GOOGLE_CLOUD_LOCATION
  -> selectedType "vertex-ai"
```

Config and adapter tests reject empty-for-readiness, partial, mixed, duplicate,
unknown, and `GOOGLE_GENAI_USE_VERTEXAI` profiles. Only the selected profile's
present values reach the child environment.

Add failing root tests that permit only the constant
`.gemini/settings.json`, create `.gemini` as `0700`, and keep rejecting absolute
paths, `..`, symlinks, alternate nested paths, and separators in every other
`FileSpec.Name`.

- [ ] **Step 2: Write failing envelope and readiness tests**

Accept one duplicate-safe closed envelope with a string `.response` and pinned
known metadata, for example:

```json
{"session_id":"fake-session","response":"hello","stats":{},"warnings":[]}
```

Known optional fields are `session_id` (string), `stats` (object), and
`warnings` (array); validate their basic types and discard them. A success
envelope has no `error` and requires a string `response`. The pinned formatter's
`formatError()` emits an `error` object without a response, so a present
non-null error object is provider failure even on exit zero; `response` is
optional on that arm but must be a string when present and is discarded. Reject
null or wrong-type `error`, unknown root fields, duplicate/trailing output, a
success envelope with missing/wrong-type response, invalid UTF-8, and non-zero
exit as success. For a structured request, the adapter returns a fenced
`.response` string unchanged and the gateway integration test must reject it
during exact JSON/schema validation. Ensure errors and health do not contain
response/error text, session IDs, or credential values.

Probe tests assert the exact ordered local commands `gemini --version` and
`gemini --help`, range `>=0.53.0,<0.54.0`, and required bounded help tokens
`--output-format`, `--approval-mode`, `--extensions`/`-e`, and `--model`.
Deleting each required token yields fixed `capability_missing`; no inference or
login command is permitted. Test each exact closed auth profile plus
partial/mixed/duplicate/unknown profiles. A structurally complete selected
profile whose values are all present is `auth:"configured"` and potentially
ready; a selected profile with a missing value is `auth:"missing"` and
`not_ready`; no selected supported source is `auth:"unknown"` and `not_ready`.
This proves configuration only, not live credential validity.

When `GOOGLE_APPLICATION_CREDENTIALS` is selected, the adapter treats its value
only as a path and requires absolute path syntax; it does not open, parse, copy,
resolve, or print the file or path. Task 14's platform-specific `doctor` gate
performs the authoritative non-symlinked, user-owned regular-file and safe
Unix-mode/Windows-DACL validation before application readiness. This keeps
provider protocol code independent from executable/credential filesystem
policy while still failing closed before serving the provider.

- [ ] **Step 3: Run tests and observe failure**

Run:

```bash
go test ./internal/process ./internal/provider/gemini
```

Expected: FAIL because the adapter and narrow nested-file materialization are
absent.

- [ ] **Step 4: Implement disposable-home build and strict parse**

Create the same `provider.Adapter` methods as Tasks 11-12, returning
`core.ProviderGemini`. Require `model.Provider == core.ProviderGemini`, call
`core.ValidateProviderModel`, append the fixed args, generate the settings bytes
from a fixed Go struct with `json.Marshal`, dynamically selecting only the
closed profile's `security.auth.selectedType`; set stdin to
`provider.BuildPrompt(req, provider.SchemaInline)`, and materialize:

```go
process.FileSpec{
	Name: filepath.Join(".gemini", "settings.json"),
	Data: settingsJSON,
	Mode: 0o600,
}
```

Extend `Root.Materialize` narrowly to allow only this fixed two-component path:
create `.gemini` as `0700` with exclusive `Lstat` checks, then create
`settings.json` with `O_EXCL` as `0600`. Arbitrary separators and all other nested
paths remain rejected. The adapter supplies the nested file constant; no request
data controls it.

Build only a minimal explicit environment. Point `GEMINI_CLI_HOME`, HOME/temp,
and both system-settings override variables to the request runtime, relay only
the exact selected credential profile, and include only doctor-validated PATH
plus explicit Windows `SystemRoot`. Assert both system settings targets remain
contained and nonexistent.

Parse stdout through `safejson.Parse`, validate the closed known metadata types,
and require one `.response` string only on the no-error success arm. The pinned
formatter's error arm may omit response. The official Gemini JSON contract does
not provide a stable machine-readable auth/rate-limit discriminator: any
present non-null `error` object (including on exit zero), a non-zero exit, or
exit codes 1/42/53 maps conservatively to `failed`; null/wrong-type error,
unknown root fields, and malformed success envelopes map to `protocol`. Never
classify by the human-readable error message or stderr.

- [ ] **Step 5: Implement readiness and fake integration**

`Probe` runs only the exact version/help checks above. Those non-model commands
use the disposable minimal environment without requiring credential values, so
missing credentials do not erase an otherwise valid version/capability
diagnosis. The probe separately validates the exact closed credential profile
and value presence, records no value, and requires absolute syntax for a
selected service-account credential path. Task 14 applies the external-file
safety gate before the resolved provider is usable. A configured persistent
`ConfigHome` is never used as `GEMINI_CLI_HOME`; document it internally as a
directory whose safety may be checked but whose auth files are not copied. On
success, report only capabilities `stdin_prompt`, `json_envelope`,
`disposable_home`, `system_settings_isolated`, `empty_core_tools`, and
`extensions_disabled`.

Exercise the real adapter/supervisor with fake text, valid structured JSON,
fenced/duplicate/malformed output, cancellation, retry-loop timeout, and flood
modes. After every run, assert the disposable `.gemini` tree is gone.
Update the fake success mode to a realistic envelope containing fixed
`session_id`, `response`, and `stats`, and add an exit-zero explicit-error mode.

Run:

```bash
gofmt -w internal/process internal/provider/gemini
go test ./internal/process ./internal/provider/gemini
go test -race -tags=integration ./internal/provider/gemini
go test ./...
```

Expected: all tests PASS.

- [ ] **Step 6: Record the no-commit checkpoint**

Run:

```bash
rg -n 'GEMINI_CLI_HOME|CredentialEnv|settings\.json' internal/provider/gemini internal/process
if rg -n --glob '!**/*_test.go' \
  'os\.Environ|exec\.Command.*(sh|cmd)|oauth|token' \
  internal/provider/gemini; then
  exit 1
else
  scan_status=$?
  test "$scan_status" -eq 1 || exit "$scan_status"
fi
```

Expected: the first search shows only disposable paths/name-based relay; the
second has no production match that reads or copies authentication state. Do not
initialize Git.

---

### Task 14: Redacted Doctor and Provider Readiness Snapshot

**Files:**

- Modify: `internal/provider/codex/codex.go`
- Modify: `internal/provider/codex/codex_test.go`
- Modify: `internal/provider/claude/claude.go`
- Modify: `internal/provider/claude/claude_test.go`
- Modify: `internal/provider/gemini/gemini.go`
- Modify: `internal/provider/gemini/gemini_test.go`
- Modify: `internal/process/root.go`
- Modify: `internal/process/root_unix.go`
- Modify: `internal/process/root_unix_test.go`
- Modify: `internal/process/root_windows.go`
- Modify: `internal/process/root_windows_test.go`
- Create: `internal/doctor/doctor.go`
- Create: `internal/doctor/doctor_test.go`
- Create: `internal/doctor/output.go`
- Create: `internal/doctor/output_test.go`
- Create: `internal/doctor/path.go`
- Create: `internal/doctor/path_unix.go`
- Create: `internal/doctor/path_unix_test.go`
- Create: `internal/doctor/path_acl_policy.go`
- Create: `internal/doctor/path_acl_policy_test.go`
- Create: `internal/doctor/path_windows.go`
- Create: `internal/doctor/path_windows_test.go`

**Interfaces:**

- Produces: `doctor.Run(ctx context.Context, cfg config.Config,
  deps doctor.Dependencies) (doctor.Diagnosis, error)`.
- Produces: `doctor.Report.ReadyCount() int`, `Report.CoreReady() bool`,
  `doctor.WriteText(io.Writer, Report) error`, and
  `doctor.WriteJSON(io.Writer, Report) error`.
- Produces: `Diagnosis.Report() Report`, `Diagnosis.Registry() *core.Registry`,
  and `Diagnosis.ResolvedProviders() map[core.ProviderName]ResolvedProvider`;
  every returned map, config, health value, and slice is a defensive clone.
- Produces: `ProbeController`, which embeds `provider.ProbeRunner` and adds
  `SelfTest(context.Context, string) error`, `Shutdown(context.Context) error`,
  and `CleanupFailed() bool`.
- Produces: `doctor.NewProcessProbeController(*process.Root, process.Limits,
  func() (string, error)) (ProbeController, error)`, the production factory that
  Task 17 can assign directly to `Dependencies.NewProbeController`.
- Produces an immutable provider-health snapshot, the one canonical registry,
  path-safe resolved provider configs, and an optionally transferred locked
  runtime root used by Task 17; diagnostics never perform inference.

Implement and review Task 14 in four independently gated TDD slices:

1. **14A — adapter version gate:** correct Codex, Claude, and Gemini probes so an
   unreadable or unsupported version stops after exactly one command.
2. **14B — platform path policy:** implement object-specific executable,
   config-home, service-credential, parent-chain, Windows SystemRoot, and SafePath
   validation, with portable pure ACL policy separated from native evidence
   acquisition.
3. **14C — closed report boundary:** define the private report storage and
   provenance, defensive accessors, canonical phase/state validation, and
   validated text/JSON writers.
4. **14D — ordered diagnosis:** validate the gateway executable and injected
   lifecycle functions, construct the canonical registry, run all core
   root/janitor/controller checks, resolve provider paths and frozen lookups,
   then probe and recompute provider readiness without leaking resolved state on
   unwind.

- [ ] **Step 1: Write the 14A–14D failing tests**

Use temporary files and injected probes to cover:

- each adapter's healthy exact command order and exactly one command for
  execution-error, nonzero, malformed, or unsupported `--version`; no help,
  features, doctor, or auth command may follow;
- missing executable, relative path, symlink resolution, non-regular file,
  untrusted writable permissions, special bits, owner/execute failures, and
  Windows `.cmd`/`.bat` or reparse paths;
- exact Unix `0700` config homes and `0400`/`0600` service credentials,
  rejecting symlinks, non-regular objects, special/execute bits, and group/other
  access;
- portable synthetic Windows ACL-policy tests for trusted-owner classification,
  generic-right expansion, explicit and applicable inherited ACEs,
  `INHERIT_ONLY_ACE` exclusion, integrity/confidentiality masks, ordered
  effective-token access, the rule that a deny cannot excuse an unsafe
  untrusted allow, and drive/UNC case canonicalization;
- thin native Windows evidence-acquisition tests for security-descriptor,
  reparse, final-identity, token-principal, and final-path normalization, with
  executable/PATH leaf and ancestor integrity distinct from config/credential
  leaf confidentiality plus integrity;
- SafePath empty/NUL/list-separator components, unsafe ancestors,
  broken/looped symlinks, identity duplicates, Windows case variants and
  drive/UNC forms, and absence of ambient PATH;
- supported version and required-help capability, with no provider command until
  that provider's path/config/SafePath checks pass;
- selected API-key environment name present with empty/missing value, without
  value output; ambient mutation after diagnosis cannot change a resolved
  provider lookup;
- Gemini service-account file and parent-chain safety at diagnosis time;
- missing, unsafe, symlink-resolved-to-unsafe, and non-regular
  `GatewayExecutable` leaves plus unsafe lexical/resolved parents, returning
  exactly `ErrInvalidDependencies` before any adapter method, root hook,
  controller hook, or provider command;
- runtime-root lock and exact calls to injected `Janitor`, `CloseRoot`, and
  `NewProbeController`; fake controllers cover `SelfTest`, `RunProbe`,
  bounded and timeout-triggered synchronous background-drain `Shutdown`, and
  latched `CleanupFailed`; a channel-gated fake proves `Run` cannot return or
  close the root before the second ownership drain completes, then closes exactly
  once; a fail-stop fake proves zero runtime ID, prepare,
  builder, execute, or later-provider calls after the latch, while one
  real-root/real-supervisor
  integration covers prepare/materialize/execute/discard/cleanup and containment
  self-test with the resolved `testutil.BuildGateway` executable below a safe
  repo-local Unix temp parent rather than `/tmp`; platform lock tests distinguish
  only true contention through `process.ErrRootLocked`;
- model/provider references and scheduler limits;
- zero, one, and multiple ready providers; every configured provider and model
  alias remains in the report/registry while only path-safe providers are
  resolved;
- missing/extra/mismatched adapter dependencies, malformed or lying adapter
  health, zero/reversed supported intervals, duplicate/out-of-order fixed values,
  one-time range snapshotting, and readiness recomputation;
- exact provider-row shapes for core-skipped, every ordered preprobe path or
  credential failure, malformed Health fallback, every valid status rule, and
  post-probe cleanup failure; missing/empty/NUL selected credentials retain a
  defensive resolved config with a frozen unusable lookup but run zero commands
  only when every present credential path is safe; a combined higher-precedence
  missing value plus unsafe present Gemini service file reports
  `credential_missing`/auth `missing` but remains unresolved;
- mixed provider rows after a cleanup latch: preserve earlier/current canonical
  rows as report-only evidence, assign every later provider the exact core-skipped
  unknown row, and prove zero later provider adapters or process commands;
- complete reports with nil error, nil `RuntimeRoot`, and zero resolved providers
  for every core failure, including per-probe discard/execute cleanup and
  controller shutdown; table tests combine successful/failing `CloseRoot` with
  janitor, controller-construction, and self-test failure and assert the exact
  independent `probe_cleanup` row, hook/close counts, cleared frozen maps/local
  resolved configs, report-only canonical probed rows after cleanup failure, and
  original context-error precedence; and
- zero/unconstructed, non-final-phase, internally corrupted, and
  secret-bearing-writer reports, plus mutation attempts against defensive
  `Core()`, `Providers()`, and `Models()` results; corruption cases independently
  drop/replace expected provider/model membership and alter supported-range
  provenance.

Plant unique prompt, credential, auth identity, stdout, stderr, argv, and path
secrets. Scan the complete `Report`, text output, JSON output, and all returned
errors to assert none occur.

- [ ] **Step 2: Run tests and observe failure**

Run:

```bash
go test ./internal/provider/codex ./internal/provider/claude \
  ./internal/provider/gemini ./internal/doctor
```

Expected: adapter early-stop tests FAIL because later probes still run, and
doctor tests FAIL because the doctor package is absent.

- [ ] **Step 3: Define closed diagnostic output types**

Create:

```go
type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

type Provider struct {
	Name         core.ProviderName   `json:"name"`
	Status       provider.HealthStatus `json:"status"`
	Version      string              `json:"version,omitempty"`
	Auth         string              `json:"auth"`
	Capabilities []string            `json:"capabilities"`
	Problems     []string            `json:"problems,omitempty"`
}

type reportPhase uint8

const (
	reportPhaseUnconstructed reportPhase = iota
	reportPhaseCore
	reportPhaseProviders
	reportPhaseComplete
)

type Report struct {
	core              []Check
	providers         []Provider
	models            []string
	expectedProviders []core.ProviderName
	expectedModels    []string
	expectedRanges    map[core.ProviderName]provider.Range
	constructed       bool
	phase             reportPhase
}

type ResolvedProvider struct {
	Config provider.ProviderConfig
	Health provider.Health
}

type Diagnosis struct {
	report      Report
	providers   map[core.ProviderName]ResolvedProvider
	registry    *core.Registry
	RuntimeRoot *process.Root
}

type ProbeController interface {
	provider.ProbeRunner
	SelfTest(context.Context, string) error
	Shutdown(context.Context) error
	CleanupFailed() bool
}

type Dependencies struct {
	Adapters           map[core.ProviderName]provider.Adapter
	LookupEnv          provider.LookupEnv
	NewRuntimeID       func() (string, error)
	OpenRoot           func(string) (*process.Root, error)
	Janitor            func(context.Context, *process.Root) error
	CloseRoot          func(*process.Root) error
	NewProbeController func(
		*process.Root,
		process.Limits,
		func() (string, error),
	) (ProbeController, error)
	GatewayExecutable string
}
```

Add `Report.Core() []Check`, `Report.Providers() []Provider`, and
`Report.Models() []string`, plus `ResolvedProvider.Clone`, `Diagnosis.Report`,
`Diagnosis.ResolvedProviders`, and `Diagnosis.Registry`. Every accessor except
the registry pointer deep-copies each map/config/health/slice; `Registry` returns
the same immutable registry pointer whose `Models` method already clones. No
exported field can replace a report slice. The internal report builder alone sets
the independent sorted `expectedProviders`/`expectedModels` snapshots and
`expectedRanges` map, sets `constructed`, and advances `phase`; it never rebuilds
provenance from actual rows. Only `reportPhaseComplete` is externally usable.
`Diagnosis.Report()` defensively clones the provenance slices/map as well as the
actual rows. `Diagnosis` fields that contain mutable snapshots remain private.
`WriteJSON` serializes a private DTO populated only after validation; `Report`,
`Diagnosis`, and `ResolvedProvider` have no exported serializable storage and are
never passed directly to an output encoder.
`Report.CoreReady()` returns false and `Report.ReadyCount()` returns zero if
the report is zero/unconstructed, is not in `reportPhaseComplete`, or fails the
same closed phase/state validation used by the writers.

The exact core check order is:

```text
listener
gateway_auth
scheduler
runtime_root
runtime_janitor
containment
probe_cleanup
```

`Check.Status` is exactly `pass`, `fail`, or `skipped`. A pass/skipped row
has empty code/message. A fail row has the one code valid for its name and the
exact package-owned message for that code. A complete report contains every row
once in that order, every configured provider once in provider-name order, and
every configured model alias once in alias order.

`WriteText` and `WriteJSON` call one shared `validateReport` before writing any
byte. Validation first requires `constructed == true` and
`phase == reportPhaseComplete`, then rejects unknown/duplicate/out-of-order check
names, statuses, codes, messages, provider names, model aliases,
auth/capability/problem values, noncanonical versions, and impossible
core/provider/readiness combinations. Actual provider names and model aliases
must equal the independent expected snapshots exactly. The expected-range map
must contain exactly the expected providers, and every readable,
`version_unsupported`, or ready version must classify consistently against that
provider's retained interval. Only after that check may a writer build its
private DTO from accessor clones. `WriteJSON` uses `json.Encoder` with HTML
escaping and one final newline. Zero, unconstructed, partial-phase, membership-
corrupted, or range-corrupted reports return exactly
`doctor.ErrInvalidReport`; any underlying writer failure returns exactly
`doctor.ErrReportWrite`, without wrapping or exposing the writer error.

Expected listener/key/scheduler/root/lock/janitor/controller-construction/
self-test/per-probe-cleanup/controller-shutdown/root-close failures return a
complete `Diagnosis` and nil error. The invariant is unconditional: if any core
row is not `pass`, `RuntimeRoot == nil` and `ResolvedProviders()` is empty. `Run`
clears every local resolved config and frozen lookup map, shuts down created
probe resources, and releases an acquired root through the injected closer. A
close or controller cleanup failure is represented only by
`runtime_cleanup_failed` on the `probe_cleanup` row. That row is `skipped` only
when no root was acquired; after root acquisition it is `pass` when every cleanup
action required on that path succeeds and `fail` when per-probe cleanup,
controller shutdown, or `CloseRoot` fails. It may therefore fail alongside
`runtime_janitor` or `containment`. A successful close after janitor or
non-cleanup containment failure leaves `probe_cleanup` pass only when the cleanup
latch is clear and synchronous shutdown succeeds. A cleanup-category `SelfTest`
error sets both `containment` and `probe_cleanup` to fail.

Never pass the Run context to controller cleanup. Create one fresh
`context.WithTimeout(context.Background(), time.Second)` cleanup context and call
`Shutdown(cleanupCtx)` synchronously exactly once. If it returns nil or a
non-context error, controller ownership is drained; on an unwind, call
`CloseRoot(root)` synchronously exactly once. If it returns
`context.Canceled`/`context.DeadlineExceeded`, ownership is not drained: call
`Shutdown(context.Background())` a second time synchronously and do not let `Run`
return until that ownership call completes. Only then may an unwind call
`CloseRoot(root)` exactly once. There is no background goroutine or
controller/root handoff. A channel-gated fake test proves `Run` and
the closer remain blocked before drain, then the closer runs once. Any first
shutdown error, second-drain error, cleanup latch, or close error remains the
fixed cleanup-failure result without logging raw details.
Canonical provider rows already produced before a cleanup failure may remain in
the report, but they are report-only. The locked root transfers to the caller
only at the final successful return after every core row passes, and the caller
closes it exactly once. Invalid dependencies return
`doctor.ErrInvalidDependencies` before root acquisition. Original Run
cancellation returns only that exact `context.Canceled` or
`context.DeadlineExceeded` after synchronous ownership cleanup;
it takes precedence over every cleanup result. Any other failure to construct a
safe diagnosis returns one fixed `doctor.ErrDiagnosis`; none wraps
provider/OS/writer text.

The closed diagnostic problem codes are:

```text
listener_unsafe
gateway_key_missing
runtime_unsafe
runtime_locked
runtime_cleanup_failed
containment_failed
scheduler_invalid
executable_missing
executable_unsafe
version_unreadable
version_unsupported
capability_missing
config_home_unsafe
auth_missing
auth_unknown
credential_missing
credential_file_unsafe
```

Messages are fixed one-line explanations keyed by these codes. Adding a provider
string, OS error, path, identity, or credential to a diagnostic message is
forbidden.

- [ ] **Step 4: Implement 14A version gating and 14B platform paths**

In each existing adapter `Probe`, run `--version`, parse it with
`provider.ParseVersion`, and apply that adapter's `SupportedVersion` before
building any later command. On execution error, nonzero exit, unreadable version,
or unsupported version, return canonical health immediately. Codex otherwise
continues with its four pinned help/features/login/doctor commands, Claude with
help then `auth status`, and Gemini with help. Keep `Adapter`,
`ProbeRunner`, and `ProviderConfig` unchanged.

Implement common path orchestration in `internal/doctor/path.go` and
object-specific primitives in the build-tagged files:

- Unix executable/Node entrypoint: absolute after resolution, regular, owned by
  root or the effective gateway UID, at least one execute bit, no group/other
  write, and no setuid/setgid/sticky bits.
- Unix config home: non-symlink directory, effective-UID owner, exact `0700`.
- Unix service credential: non-symlink regular file, effective-UID owner, exact
  `0400` or `0600`, with no special or execute bits.
- Every relevant Unix lexical/resolved parent chain: containing directories are
  root/effective-UID-owned with no group/other write; symlink components require
  safe containing and fully resolved target chains; broken/looped resolution
  fails.
- Windows executable/entrypoint/PATH leaf and every lexical/resolved ancestor:
  apply the integrity policy. Reject `.cmd`/`.bat`, every reparse point, wrong
  final type or changed identity, null/unsupported descriptors or ACE forms,
  owners outside token user/LocalSystem/Builtin Administrators/TrustedInstaller,
  and any applicable untrusted allow for write/append/add-child/delete/
  `DELETE_CHILD`/`WRITE_DAC`/`WRITE_OWNER`. Require effective token
  read-data/read-attributes/execute for an executable leaf and
  list/read-attributes/traverse for a directory.
- Windows config-home leaf: require the process-token user as owner and both
  confidentiality and integrity policy. Require effective token
  list/read-attributes/traverse plus create/write/append/add-subdirectory and
  delete-child access. Reject every applicable untrusted allow for
  read/list/traverse/execute or any integrity mutation right.
- Windows service-credential leaf: require the process-token user as owner and
  both confidentiality and integrity policy. Require effective token read-data
  and read-attributes access; reject every applicable untrusted allow for
  read/list/traverse/execute or any integrity mutation right.
- Windows config-home and credential ancestor directories use the integrity-only
  executable/PATH policy, its trusted-owner set, and effective token traverse;
  do not apply the leaf confidentiality rule to ancestors. LocalSystem and
  Builtin Administrators remain trusted administrative principals.

Keep all policy decisions in untagged `path_acl_policy.go`. It consumes a closed
portable snapshot of owner, object class, normalized token principals, ordered
normalized ACEs, descriptor support/null state, reparse state, final identity,
and canonical spelling. It expands `GENERIC_*` by file/directory object type;
applies explicit and inherited allow/deny ACEs to the current object while
skipping `INHERIT_ONLY_ACE`; computes required effective token access in DACL
order, including deny-only group semantics; and separately rejects every unsafe
untrusted allow even when another ACE denies the same right. Its synthetic tests
run natively on every platform and exercise owner classification, generic
expansion, applicable inherited/`INHERIT_ONLY` behavior, both policy masks,
effective access, deny-versus-unsafe-allow, and case keys for drive and UNC
variants. Build-tagged `path_windows.go` contains only native handle/token/API
acquisition and normalization into that snapshot; it does not decide trust or
policy. Task 18 remains the native Windows execution gate.

Recheck Task 5's closed wrapper shape. Unix `prefix_args` is empty. Windows may
use a resolved `node.exe` plus exactly one resolved safe absolute `.js` or
`.mjs` entrypoint; no other prefix is accepted.

For every absolute executable input, including `GatewayExecutable`, and every
absolute Node entrypoint, config-home, or credential input, call
`filepath.Clean` immediately before any filesystem walk or use. Accept a
textually nonclean absolute input, but validate lexical and resolved chains from
the clean spelling, store only its resolved clean result, and pass only that
result to self-test or provider execution. Never walk, store, or execute the
original spelling after cleaning.

SafePath is newly constructed, never inherited. Candidate directories are the
configured executable parent, resolved target parent, resolved Windows entrypoint
parent when present, and exactly `/usr/bin`, `/bin` on Unix or the
platform-validated `SystemRoot\\System32` on Windows. Each component must be
nonempty, absolute, clean, NUL-free, and contain neither
`os.PathListSeparator` nor an empty PATH element. Resolve and validate the
directory and relevant parent authority before storing it. De-duplicate by final
directory identity, with case-insensitive canonical spelling on Windows, preserve
first safe order, and join only with `os.PathListSeparator`. Every enumerated
fixed tail is mandatory to validate: a missing or unsafe `/usr/bin`, `/bin`, or
API-derived `System32` makes that provider's SafePath unsafe and yields
`executable_unsafe`. Identity duplicates may collapse only after every duplicate
candidate independently passes validation. Tests plant nonclean absolute object
inputs and prove the original spelling is never walked, stored, self-tested, or
executed; they also make each fixed tail independently missing and unsafe and
expect `executable_unsafe` with zero provider commands.

Add one fixed `process.ErrRootLocked` sentinel. The Unix `lockFile` maps only
true `EWOULDBLOCK`/`EAGAIN` contention to it; the Windows `lockFile` maps only
`ERROR_LOCK_VIOLATION` contention to it. `OpenRoot` may wrap that sentinel, and
doctor classifies only `errors.Is(err, process.ErrRootLocked)` as
`runtime_locked`; every other open failure is `runtime_unsafe`. Tests prove
other platform lock errors are not misclassified.

- [ ] **Step 5: Implement 14C report validation and 14D ordered diagnosis**

`Run` first defensively clones the normalized config and validates the nonnil
shape of `LookupEnv`, `NewRuntimeID`, `OpenRoot`, `Janitor`, `CloseRoot`, and
`NewProbeController` without invoking them. Before `Adapter.Name`,
`Adapter.SupportedVersion`, root acquisition, controller construction, self-test,
or provider probing, resolve `GatewayExecutable` and validate its leaf plus both
lexical and resolved parent chains with the platform executable policy. Missing,
unsafe, symlink-resolved-to-unsafe, or non-regular input returns exactly
`ErrInvalidDependencies` and makes zero adapter-method, root-hook,
controller-hook, and provider-command calls.

After the gateway executable passes, call each adapter's `Name()` exactly once
and require the map to contain exactly the configured providers with key equal to
that retained name; missing, extra, nil, or
mismatched adapters also return `ErrInvalidDependencies` before root acquisition
or provider commands. Call each matching adapter's `SupportedVersion()` exactly
once, require a strict nonempty `MinInclusive < MaxExclusive` interval, and retain
that value in the report's independent expected-range provenance; an invalid
interval is also `ErrInvalidDependencies` before root acquisition. Construct one
`core.Registry` from the cloned models and retain it in `Diagnosis`; provider
membership still comes from `cfg.Providers`, including providers with zero
aliases. All later doctor canonicalization and writer validation use the retained
range rather than calling the adapter again.

Run core work before provider paths or commands: evaluate listener/key/scheduler
rows; call `OpenRoot` at most once; call `Janitor(ctx, root)` exactly once after a
successful open; call `NewProbeController(root, limits, NewRuntimeID)` exactly
once after a successful janitor; and call
`SelfTest(ctx, resolvedGatewayExecutable)` exactly once. The limits are fixed at
5-second execution, 1-second termination/cleanup, 64 KiB stdout, and 64 KiB
stderr. Production dependencies call `root.Janitor`, `root.Close`, and exported
`doctor.NewProcessProbeController`, which creates a real
`process.NewSupervisor`, embeds `provider.ProbeRunner`, and retains the supplied
runtime-ID function. Task 17 can assign this exported factory directly; no
package-private default or inaccessible closure is required.

Before cleanup failure, each `RunProbe` prepares one fresh runtime, calls the
builder, executes it, and consumes the runtime exactly once. A builder failure
calls `Supervisor.Discard`; a discard failure or an `Execute` cleanup error
monotonically latches `CleanupFailed() == true`, even when the adapter turns the
returned runner error into coarse health. Every later `RunProbe` checks the latch
first and returns one fixed cleanup-category runner error before `NewRuntimeID`,
`Prepare`, builder, or `Execute`. The controller also latches a cleanup-category
`SelfTest` error; doctor fails `containment` before probes. `Shutdown` delegates
to the real supervisor. Doctor never builds a helper or requires a Go toolchain. Unit
tests use exact-call fake functions/controllers for every branch; a separate
integration test uses a real locked root, real supervisor, and the test-built
real gateway returned by `testutil.BuildGateway` to exercise containment and
cleanup. On Unix, create a secure repo-local temp parent, point the test's
`TMPDIR` at it before `t.TempDir`/`BuildGateway`, and validate the complete parent
chain; `/tmp` itself is not an admissible executable ancestor under this policy.

If a pre-root core row fails, do not acquire the root and mark dependent runtime
rows `skipped`; if a root-stage row fails, stop later root work except resource
cleanup. Controller construction or self-test failure is the fixed
`containment_failed` core result. Per-probe cleanup, controller shutdown, or
root-close failure always sets `probe_cleanup` to fail with the fixed
`runtime_cleanup_failed` pair. `probe_cleanup` is skipped only if `OpenRoot`
never returned a root. Once a root exists, it passes when all applicable cleanup
actions succeed, even if `runtime_janitor` or `containment` already failed, and
fails independently alongside those rows if `CloseRoot` fails. Every complete
core-failure diagnosis has `RuntimeRoot == nil` and zero resolved providers.

Only after every core root/containment check passes, resolve each provider in
sorted name order. Before a probe, emit exactly one problem using this first-match
precedence:

1. missing executable: `executable_missing`;
2. unsafe executable/wrapper/SafePath: `executable_unsafe`;
3. unsafe config home: `config_home_unsafe`;
4. missing, empty, or NUL selected credential: `credential_missing`; and
5. unsafe service-credential leaf or parent chain:
   `credential_file_unsafe`.

Every such row is `not_ready` with empty version/capabilities. It uses auth
`missing` only for `credential_missing`; all other preprobe failures use auth
`unknown`. Every path/config/file-unsafe case runs zero commands and has no
`ResolvedProvider`. Evaluate every path/file safety fact before applying the
single-problem precedence; do not early-return on a missing selected value. If a
Gemini service-account profile has a missing project/location and a present but
unsafe credential file, report only higher-precedence `credential_missing` with
auth `missing`, but keep it unresolved because the independent path-safety fact
failed. A provider whose executable, config home, SafePath, and every present
credential path are safe but whose selected credential is missing/empty/NUL also
runs zero commands, but remains defensively resolved: its frozen lookup records
only the unusable selected name as absent/empty and Task 17 cannot schedule it
because the row is not ready.

Every path-safe resolved `ProviderConfig` contains resolved paths, cloned slices,
SafePath, and a frozen `LookupEnv`. The closure contains only values looked up
once for that provider's exact selected credential names plus the once-validated
Windows `SystemRoot`; it returns no gateway key, other-provider credential, or
unselected ambient value. Changing or unsetting ambient values after `Run`
cannot alter it.

Gemini service-account validation happens only at diagnosis time. Replacement
rechecks and hard-link-count restrictions remain optional future hardening under
the same-user/administrator trust boundary; do not add identity fields, a
pre-spawn guard, or another adapter/gateway interface.

Call `Adapter.Probe` only with a path-safe, credential-eligible frozen config.
Validate exact agreement among configured name, map key, the retained one-time
`Adapter.Name()` result, and `Health.Provider`. Accept
only the closed status set, canonical `major.minor.patch` version that
round-trips through `provider.ParseVersion`, the adapter's supported interval,
and these provider-specific values:

| Provider | Auth values | Required ready capabilities |
|---|---|---|
| Codex | `authenticated`, `missing`, `unknown` | `stdin_prompt`, `ephemeral`, `read_only`, `never_approve`, `schema_file`, `feature_hardening` |
| Claude | `authenticated`, `missing`, `unknown` | `stdin_prompt`, `json_envelope`, `no_session_persistence`, `empty_settings`, `empty_tools`, `safe_mode` |
| Gemini | `configured`, `missing`, `unknown` | `stdin_prompt`, `json_envelope`, `disposable_home`, `system_settings_isolated`, `empty_core_tools`, `extensions_disabled` |

Problem allowlists are exact: Codex accepts executable missing/unsafe, version
unreadable/unsupported, capability missing, config-home unsafe, and auth
missing/unknown; Claude accepts that set plus credential missing; Gemini accepts
executable missing/unsafe, version unreadable/unsupported, capability missing,
config-home unsafe, auth unknown, credential missing, and credential-file unsafe.
Use the existing `provider.Problem*` strings only. Normalize recognized
capabilities/problems by de-duplicating and ascending ASCII sort, then require all
of these relationships:

- version is exactly one of empty plus `version_unreadable`, canonical
  out-of-range plus `version_unsupported`, or canonical supported with neither
  version problem;
- capabilities are either the complete exact provider set with no
  `capability_missing`, or empty with `capability_missing`;
- Codex/Claude auth `authenticated`, `missing`, or `unknown` corresponds exactly
  to no auth problem, `auth_missing`, or `auth_unknown`; Gemini auth
  `configured`, `missing`, or `unknown` corresponds exactly to no auth problem,
  `credential_missing`, or `auth_unknown`;
- `ready` means supported canonical version, complete capabilities, ready auth,
  and no problems;
- Codex/Claude `unknown` means every version/capability proof is ready, auth is
  `unknown`, and `auth_unknown` is the sole problem; and
- every other internally consistent combination is `not_ready`; Gemini never
  supplies an adapter-valid `unknown` status.

An input `Health.Status` inconsistent with those proofs, an unknown fixed value,
a relational contradiction, or a provider/name mismatch makes the entire Health
malformed. Replace it exactly with status `not_ready`, auth `unknown`, empty
version/capabilities, and the ASCII-sorted problems `auth_unknown`,
`capability_missing`, `version_unreadable`. Never echo a rejected value. For a
valid Health, independently reapply the supported range and recompute the output
status from all path, credential, version, capability, and auth proofs; never use
the input status as readiness authority.

Immediately after each `Adapter.Probe` returns, canonicalize that provider's safe
Health and then inspect `CleanupFailed()`. If latched, stop the sorted provider
loop: do not call any later adapter, preserve earlier preprobe/canonical rows and
the current canonical row as report-only evidence, and fill every later provider
with status `unknown`, auth `unknown`, empty version/capabilities/problems, and no
resolved config. The controller's own fail-stop gate proves that any later
`RunProbe` attempted inside the current adapter performs no ID generation,
runtime prepare, builder, or execution. Continue directly to shutdown and the
ordinary core-failure unwind.

The report includes every configured provider and registry alias whether ready,
unresolved, or unprobed. A provider skipped because a core row failed is exactly
status `unknown`, auth `unknown`, empty version/capabilities/problems, and
unresolved. Resolved configs exist for path-safe providers, including a
path-safe provider with a frozen missing/empty credential, but never for an
unsafe credential file/path.

After probing, observe `CleanupFailed()` and make the one synchronous controller
shutdown attempt with the fresh one-second cleanup context. A latched cleanup
failure or any shutdown error makes `probe_cleanup` fail with
`runtime_cleanup_failed`. Retain any already canonical provider rows as report
evidence, but clear every local frozen lookup map, overwrite/delete every local
resolved config, and return zero resolved providers with `RuntimeRoot == nil`.
On an unwind after a nil/non-context shutdown result, close the root
synchronously exactly once; after a context shutdown result, make the one
synchronous `Shutdown(context.Background())` ownership-drain call, wait for it to
return, and only then close exactly once. Apply the same
resolved/frozen-state clearing and cleanup algorithm on every other core-failure
and exceptional unwind, including cancellation. Only when every core row passes,
the cleanup latch is false, and synchronous shutdown succeeds may the final
diagnosis retain resolved providers and transfer the root. Provider-ready count
does not control that transfer; application startup owns and cleans a transferred
core-ready root before rejecting `Report.ReadyCount() == 0`.

- [ ] **Step 6: Verify secret-free deterministic diagnostics**

Run:

```bash
gofmt -w internal/doctor internal/provider/codex internal/provider/claude internal/provider/gemini
go test ./internal/doctor ./internal/provider/codex ./internal/provider/claude ./internal/provider/gemini
go test -race ./internal/doctor ./internal/provider/codex ./internal/provider/claude ./internal/provider/gemini
go test ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/process-task14.exe ./internal/process
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/codex-task14.exe ./internal/provider/codex
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/claude-task14.exe ./internal/provider/claude
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/gemini-task14.exe ./internal/provider/gemini
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go test -c -o /tmp/doctor-task14.exe ./internal/doctor
```

Expected: all tests PASS, planted secret scans remain empty, and Windows
cross-compilation succeeds. Native Windows ACL/reparse execution remains a
mandatory Task 18 gate; cross-compilation is not execution evidence.

- [ ] **Step 7: Record the no-commit checkpoint**

Run:

```bash
rg -n 'Stdout|Stderr|Executable|ConfigHome|Credential|identity|email|token' \
  internal/doctor internal/provider/codex internal/provider/claude internal/provider/gemini
```

Expected: production diagnostic output types contain none of these raw fields;
occurrences are limited to internal path/config handling and leakage tests.
Behavioral planted-secret tests and closed writer validation are authoritative.
Do not initialize Git.

---

### Task 15: Gateway Orchestration and Stable Failure Mapping

**Files:**

- Modify: `internal/core/types.go`
- Modify: `internal/core/errors_test.go`
- Create: `internal/gateway/gateway.go`
- Create: `internal/gateway/gateway_test.go`
- Create: `internal/gateway/gateway_integration_test.go`

**Interfaces:**

- Produces: `gateway.New(registry *core.Registry,
  providers map[core.ProviderName]*gateway.ProviderRuntime,
  cfg gateway.Config, deps gateway.Dependencies) (*Gateway, error)`.
- Produces: `gateway.NewProviderRuntime(adapter provider.Adapter,
  cfg provider.ProviderConfig, scheduler gateway.Scheduler,
  supervisor gateway.Supervisor, health provider.Health)
  (*gateway.ProviderRuntime, error)`.
- Produces: `(*Gateway).Respond(ctx context.Context, req core.Request)
  (core.Result, error)`, `Models() []core.Model`,
  `Health() []provider.Health`, and `Shutdown(context.Context) error`.
- Owns alias routing and orchestration only; adapters own CLI contracts,
  schedulers own admission, and supervisors own process lifecycle.

- [ ] **Step 0: Fix canonical dotted provider metadata RED-first**

Before gateway work, add core tests that accept empty metadata where no provider
version is known and accept canonical bounded `major.minor.patch` values such as
`0.146.0`, `2.1.208`, and `0.53.0`. Reject leading-zero components,
decorations, prerelease/build suffixes, Unicode digits, whitespace/controls,
extra or missing components, components outside `uint64`, and total strings
longer than the existing 32-byte metadata bound.

Run:

```bash
go test ./internal/core -run 'ProviderVersion|OutcomeError'
```

Expected RED: the current digits-only validator rejects dotted versions. Replace
it with a cycle-free core-local exact three-component ASCII parser; `core`
must not import `provider`. Rerun the command and require GREEN before creating
`internal/gateway`. All Task 15 result/error metadata and logging use this same
canonical bounded dotted numeric provider-version contract.

- [ ] **Step 1: Write failing routing and isolation tests**

Use spy adapters/schedulers/supervisors to assert:

- the client alias resolves to exactly one provider and trusted provider model;
- unknown alias returns `404 model_not_found` without queue or adapter activity;
- a listed alias whose provider is not ready returns
  `503 provider_not_ready`;
- Codex saturation does not block a Claude request;
- an invalid/unsupported schema returns `400 invalid_json_schema` before provider
  admission or runtime creation;
- scheduler admission precedes runtime creation;
- runtime creation, adapter build, execution, parse, final-limit check, and schema
  validation occur in that order;
- schema validation happens for all providers, including Codex native schema
  output;
- no provider fallback or retry occurs after any start or failure;
- active shutdown maps to `service_shutting_down`, while an independently
  canceled client returns `context.Canceled`;
- ENOENT/EACCES after readiness degrades only that provider immediately, and a
  second request fails before scheduler admission;
- `Models` stays sorted and does not change when health changes.

- [ ] **Step 2: Write failing exhaustive error-mapping tests**

Table-test this closed mapping:

| Internal condition | Public code |
|---|---|
| scheduler queue count/bytes full | `queue_full` |
| scheduler queue deadline | `queue_timeout` |
| scheduler shutdown | `service_shutting_down` |
| active execution canceled by gateway shutdown | `service_shutting_down` |
| provider structured rate limit | `provider_rate_limited` |
| provider auth failure | `provider_auth_required` |
| process execution deadline | `provider_timeout` |
| stdout/stderr/final cap | `output_limit_exceeded` |
| start ENOENT/EACCES after readiness | `provider_not_ready` and degrade health |
| other process start failure | `provider_failed` |
| provider envelope/UTF-8 failure | `provider_protocol_error` |
| final exact-JSON/schema failure | `structured_output_invalid` |
| unclassified non-zero exit | `provider_failed` |
| containment/runtime cleanup failure | `process_cleanup_failed` |
| impossible invariant | `internal_error` |

Public failures return `*core.APIError` through the `error` interface so callers
use `errors.As`; when safe execution metadata exists, `core.OutcomeError` wraps
that API/context cause and exposes only numeric or closed-enum metadata.
Scheduler `ErrCanceled` caused by the request context is
normalized back to that context's `context.Canceled`; a gateway-owned
request/body deadline returns `context.DeadlineExceeded` for Task 16 to map to
`request_timeout` only when a response channel remains. It is never exposed as a
fabricated status 499. Assert every mapped message is the fixed catalog message
and contains none of the planted request/provider/error text.

- [ ] **Step 3: Run tests and observe failure**

Run:

```bash
go test ./internal/gateway
```

Expected: FAIL because the gateway package is absent.

- [ ] **Step 4: Define provider runtime ownership**

Create:

```go
type ProviderRuntime struct {
	Adapter    provider.Adapter
	Config     provider.ProviderConfig
	Scheduler  Scheduler
	Supervisor Supervisor

	mu     sync.RWMutex
	health provider.Health
}

type Scheduler interface {
	Do(context.Context, int64, func(context.Context) error) error
	Stats() scheduler.Stats
	Shutdown(context.Context) error
}

type Supervisor interface {
	Prepare(string) (process.Runtime, error)
	Discard(context.Context, process.Runtime) error
	Execute(context.Context, process.Runtime, process.CommandSpec) (
		process.Result, error)
}

type Config struct {
	SchemaLimits schema.Limits
	FinalBytes   int
}

type Dependencies struct {
	NewRuntimeID func() (string, error)
	Now          func() time.Time
}

type Gateway struct {
	registry  *core.Registry
	providers map[core.ProviderName]*ProviderRuntime
	config    Config
	closing   atomic.Bool
	id        func() (string, error)
	now       func() time.Time
}
```

`NewProviderRuntime` is the only cross-package construction path for the type
with private mutable health. It defensively copies the initial health, validates
adapter/config/dependency presence according to ready/not-ready status, and
returns no partially initialized runtime; `internal/app` never writes the private
field directly. For a configured provider that doctor could not resolve safely,
Task 17 passes the real matching adapter, a zero `provider.ProviderConfig{}`
(never the raw unsafe config), nil scheduler/supervisor, and canonical not-ready
health. No additional constructor or pre-spawn interface is added.

The gateway constructor copies the provider map, validates exact name agreement among map
key, adapter, health, configured models, and scheduler/supervisor presence, and
rejects nil dependencies for every ready provider. A `not_ready` provider may
have nil scheduler/supervisor because `Respond` rejects it before admission.
Require positive `FinalBytes`, complete schema limits, a non-nil runtime ID
source, and a non-nil clock. Health access returns defensive copies, including
capability/problem slices. The narrow scheduler/supervisor interfaces exist only
to make orchestration order and error mapping independently testable; the
production implementations are exactly Tasks 6, 8, and 9.

- [ ] **Step 5: Implement the single-attempt response flow**

`Respond` performs:

```text
reject shutdown
resolve alias
if json_schema: compile portable schema or return invalid_json_schema
read provider health
scheduler.Do(request weight)
  supervisor.Prepare(random request ID)
  adapter.Build
  supervisor.Execute
  adapter.Parse
  enforce UTF-8/non-empty/final byte cap
  if json_schema: safe exact JSON parse + schema.Validate
return core.Result
```

The runtime ID uses 128 bits from `crypto/rand`, not request/model text; generation
failure maps to `internal_error` before `Prepare`. Build failure after
prepare still calls `Supervisor.Discard` and maps a discard invariant failure to
`process_cleanup_failed`. A closure captures the result only after all steps
succeed. There is no loop, provider fallback, repair prompt, or retry.

Measure queue/execution intervals with an injected monotonic clock and copy only
numeric totals, the scheduler snapshot taken when the request starts, plus
validated provider/version/category identifiers into `core.Result.Meta`.
Populate the same safe fields through `core.NewOutcomeError` on every failure path
that has reached the relevant stage, including fixed stop reason/action derived
from the supervisor—not raw OS/provider error text.

For structured output, call the already compiled `schema.Validate` on the exact
provider text; do not
trim fences, prose, or whitespace into a new candidate. Preserve the validated
original JSON bytes as the `core.Result.Text` string.

- [ ] **Step 6: Implement health degradation and shutdown**

A containment invariant, runtime cleanup error, or post-readiness executable
ENOENT/EACCES start failure atomically changes that provider to `not_ready` with
the corresponding fixed problem code before mapping the current request.
Ordinary provider/network/other start failures do not mutate readiness. When a
running supervisor returns cancellation, map it from the explicit cause:
gateway/scheduler stop becomes `service_shutting_down`; caller cancellation
returns `context.Canceled`; neither is mislabeled a provider timeout. `Shutdown` flips
`closing` once and concurrently shuts down each distinct provider scheduler,
which rejects queued work and cancels active work through scheduler stop
contexts. The application owns the one shared runtime root and runs its janitor
and close exactly once after gateway shutdown; the gateway never closes a shared
root through each supervisor. Join errors without including sensitive values.

- [ ] **Step 7: Run fake end-to-end gateway tests**

Create all three real adapters with independent schedulers and one real
supervisor/fake CLI setup. Exercise text, structured JSON, model routing, queue
full/timeout, cancellation, provider timeout, malformed/duplicate/fenced output,
schema mismatch, exit 7, stdout/stderr caps, and runtime cleanup. Assert no
process, directory, permit, or goroutine remains.

Run:

```bash
gofmt -w internal/gateway
go test ./internal/gateway
go test -race -tags=integration -count=3 ./internal/gateway
go test ./...
```

Expected: all tests PASS without cross-provider blocking or retries.

- [ ] **Step 8: Record the no-commit checkpoint**

Run:

```bash
rg -n 'for .*Respond|retry|fallback|stderr|stdout' internal/gateway
```

Expected: no retry/fallback path and no raw process data in public errors or
health. Do not initialize Git.

---

### Task 16: Exact HTTP Surface, Authentication, Limits, and Safe Logging

**Files:**

- Create: `internal/httpapi/response.go`
- Create: `internal/httpapi/response_test.go`
- Create: `internal/httpapi/auth.go`
- Create: `internal/httpapi/auth_test.go`
- Create: `internal/httpapi/server.go`
- Create: `internal/httpapi/server_test.go`
- Create: `internal/httpapi/server_integration_test.go`
- Create: `internal/observability/log.go`
- Create: `internal/observability/log_test.go`

**Interfaces:**

- Produces: `httpapi.New(httpapi.Config, httpapi.Dependencies, Backend,
  *slog.Logger) (*http.Server, http.Handler, error)`.
- Produces: `httpapi.NewOpaqueIDSource(io.Reader) (httpapi.IDSource, error)`.
- Consumes a small `Backend` interface with
  `Respond(context.Context, core.Request) (core.Result, error)` and
  `Models() []core.Model`, implemented by Task 15.
- Produces exact Responses-compatible success, model-list, and error JSON.
- Produces metadata-only structured request logging.
- Produces an in-memory `observability.Counters` with atomic
  `ClientCanceled()` and snapshot methods; it stores counts only and is emitted
  through the same closed shutdown metadata path.

- [ ] **Step 1: Write failing success-envelope tests**

Inject a fixed clock and deterministic ID generator. Assert byte-decoded equality
for the full response shape:

```json
{
  "id":"resp_TEST",
  "object":"response",
  "created_at":1785369600,
  "completed_at":1785369601,
  "status":"completed",
  "background":false,
  "error":null,
  "incomplete_details":null,
  "instructions":null,
  "model":"codex-default",
  "output":[{
    "id":"msg_TEST",
    "type":"message",
    "status":"completed",
    "role":"assistant",
    "content":[{
      "type":"output_text",
      "annotations":[],
      "text":"Final provider output"
    }]
  }],
  "parallel_tool_calls":false,
  "previous_response_id":null,
  "store":false,
  "text":{"format":{"type":"text"}},
  "tools":[],
  "tool_choice":"none"
}
```

For JSON Schema, assert `text.format` echoes type, name, optional description,
`strict:true`, and the original schema object, while the validated JSON remains a
string in `output_text.text`. Assert `usage` and invented fields are absent.

`GET /v1/models` must produce sorted aliases with `object:"list"`, each
`object:"model"`, configured `created` or zero, and `owned_by:"local"` without
calling a provider.

- [ ] **Step 2: Write failing auth and route-matrix tests**

With a fixed gateway secret, cover absent, empty, Basic, extra-space, duplicate,
wrong, and correct Authorization values. Both endpoints are protected. Compare
fixed-size SHA-256 digests using `crypto/subtle.ConstantTimeCompare`; tests assert
the raw secret is absent from errors and logs.
Return an arbitrary planted backend error and a custom forged metadata error;
assert the client receives only canonical `internal_error`, the unsafe metadata
is not logged, and no caller can mutate a catalog-backed `APIError`.

Cover exact routes:

| Request | Expected |
|---|---|
| `POST /v1/responses` | routed |
| `GET /v1/models` | routed |
| wrong method on either exact path | `405 method_not_allowed` |
| trailing slash or any other path | `404 not_found` |
| any query parameter | `400 unsupported_parameter` |

Every response, including auth/route errors, gets a fresh valid `X-Request-ID`
and `Content-Type: application/json`. Query errors use the fixed
`param:"query"`; decoded query names are never reflected because they are
attacker-controlled and may contain sensitive data. Route, method, Host, media,
body-size, and authentication errors use `param:null`. “Every response” here means every
response produced after `net/http` has invoked the application handler; malformed
connections, header timeouts, and oversized headers rejected by `net/http`
before routing are transport failures outside the JSON API envelope.

- [ ] **Step 3: Write failing transport-limit and cancellation tests**

Using `httptest` plus real loopback listeners where deadlines matter, verify:

- POST accepts `application/json` and `application/json; charset=utf-8` only;
- absent/wrong content type is 415;
- only absent or `identity` content encoding is accepted;
- Host equals the configured literal loopback address or its `localhost` form
  with the same port; every other Host is `400 invalid_request`;
- oversized and exactly-at-limit bodies return 413 and decode respectively;
- body-read deadline returns `408 request_timeout`;
- handler gate 128 and body-reader gate 32 reject overflow immediately with
  `429 server_busy`;
- request disconnect cancels the backend context;
- no body/queue slot leaks after parse error or cancellation;
- slow header, idle connection, and header-size behavior matches server config;
- 100 concurrent maximum bodies stay within the designed bounded allocation.

Use synchronization channels and controllable readers instead of timing sleeps.

- [ ] **Step 4: Run focused tests and observe failure**

Run:

```bash
go test ./internal/httpapi ./internal/observability
```

Expected: FAIL because response/auth/server implementations are absent.

- [ ] **Step 5: Implement explicit response structures and opaque IDs**

Define closed response structs with JSON tags; do not marshal `core.Request`
directly. Define the exact construction boundary:

```go
type Config struct {
	Listen            string
	APIKeyEnv         string
	HTTPBodyBytes     int64
	RequestLimits     RequestLimits
	HandlerLimit      int
	BodyReaderLimit   int
	MaxHeaderBytes    int
	ReadHeaderTimeout time.Duration
	BodyReadTimeout   time.Duration
	IdleTimeout       time.Duration
	FinalBytes        int
	MaxModels         int
}

type IDSource interface {
	Next(prefix string) string
}

type CounterSink interface {
	ClientCanceled()
}

type Dependencies struct {
	Now       func() time.Time
	LookupEnv func(string) (string, bool)
	IDs       IDSource
	Counters  CounterSink
}
```

`New` validates every bound including the complete `RequestLimits`, the literal
loopback listen address, non-nil dependencies including the closed counter sink,
and model-list ceiling. App
assembly copies configured input/instructions/schema limits into this value and
uses the fixed depth-64/number-128 parser bounds; every POST passes exactly
`cfg.RequestLimits` to `DecodeRequest`. Tests inject non-default smaller values
and prove all three configured byte limits are honored. The application constructs the production
ID source at startup with `NewOpaqueIDSource(crypto/rand.Reader)`; that
constructor reads a 256-bit seed or fails startup. Tests inject a fixed ID source
and clock. For each production ID, atomically increment a 64-bit counter, compute
HMAC-SHA-256 over the prefix and counter, take 128 bits, encode lower-case
unpadded base32, and prefix with `resp_`, `msg_`, or `req_`. This gives every
response a non-failing, process-local unique, opaque ID after startup without
using request data.

Error JSON is exactly:

```go
type errorEnvelope struct {
	Error struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Param   *string `json:"param"`
		Code    string  `json:"code"`
	} `json:"error"`
}
```

Build this envelope only through the read-only `core.APIError` getters. The
backend cannot construct or mutate catalog fields. If `errors.As` cannot find a
catalog-backed `*core.APIError`, discard the original error and encode a fresh
fixed `core.Error(core.CodeInternalError, nil)`.

Encode through `json.Encoder` with HTML escaping. Never pass provider error text
to the encoder. Encode into a bounded in-memory buffer before sending headers so
an encoding failure can still produce the fixed `internal_error` envelope and
never leaves a partial success response. Derive the success-buffer ceiling with
checked arithmetic as
`6 * (FinalBytes + HTTPBodyBytes) + 128 KiB`, which covers worst-case JSON
escaping of the final string and every request field that can be echoed plus the
fixed envelope. Do not preallocate the full ceiling. Tests use maximum accepted
instructions/description/schema and escape-heavy output to prove a valid response
is never rejected by the encoder cap. Error and model-list buffers are
separately bounded by fixed catalog/configured-model limits.

- [ ] **Step 6: Implement constant-time optional Bearer authentication**

At startup, when `APIKeyEnv` is configured, look up a non-empty value or fail.
Store only its SHA-256 digest in the auth middleware. Require exactly one header
whose bytes match `Bearer ` plus a non-empty token, hash the presented token, and
compare equal-sized digests in constant time. Do not log the header, token,
length, or digest.

- [ ] **Step 7: Implement exact routing and bounded HTTP handling**

Use a custom `http.Handler`, not a redirecting `ServeMux`. Validate request ID,
handler gate, Host, query, auth, exact path/method, media/encoding, then acquire
the body gate. Apply:

```go
body := http.MaxBytesReader(w, r.Body, cfg.HTTPBodyBytes)
controller := http.NewResponseController(w)
_ = controller.SetReadDeadline(clock.Now().Add(cfg.BodyReadTimeout))
raw, err := io.ReadAll(body)
_ = controller.SetReadDeadline(time.Time{})
```

Distinguish `*http.MaxBytesError`, deadline errors, and malformed JSON without
including raw data. Always clear the per-request read deadline after the body
attempt so it cannot poison a keep-alive connection. Release the body gate before
model resolution/queueing. Call
`DecodeRequest(raw, cfg.RequestLimits)` while the gate is held. For
backend errors, encode only `*core.APIError`; map a gateway-owned
`context.DeadlineExceeded` to `request_timeout`; when the client context itself is
canceled, stop writing, increment `ClientCanceled` exactly once, and record no
response. Extract optional failure metadata only through
`core.OutcomeError.ResultMetadata`; never inspect a raw wrapped process/provider
error. Hold the body gate through `DecodeRequest`, then set the raw slice to nil
and release it before model resolution or provider queueing.

Construct `http.Server` with:

```go
ReadHeaderTimeout: cfg.ReadHeaderTimeout,
IdleTimeout:       cfg.IdleTimeout,
MaxHeaderBytes:    cfg.MaxHeaderBytes,
ErrorLog:          log.New(io.Discard, "", 0),
```

Set the default 15-second `cfg.BodyReadTimeout` as an explicit body-read deadline
per request. Do not set a response
write timeout that would kill legitimate five-minute provider executions; request
context, execution timeout, shutdown, and client disconnect bound them.
The raw `net/http` error logger is discarded because its free-form connection
text is outside the closed observability schema; `Serve` failures are reported
only through fixed application codes.

- [ ] **Step 8: Implement metadata-only logging**

`internal/observability` exposes one function taking a closed metadata struct:

```go
type RequestEvent struct {
	RequestID       string
	Endpoint        string
	Status          int
	ModelAlias      string
	Provider        core.ProviderName
	InputBytes      int
	StdoutBytes     int64
	StderrBytes     int64
	FinalBytes      int
	QueueDepth      int
	RunningCount    int
	QueueDuration   time.Duration
	ExecutionTime   time.Duration
	ErrorCode       string
	ExitCategory    string
	ProviderVersion string
	StopReason      string
	StopAction      string
}
```

Log only these fields with `slog` key/value arguments. Validate or escape control
characters in alias/version/category before logging. Validation requires the
configured-alias grammar, known provider, canonical bounded dotted numeric version, catalog error
code, closed exit/stop enums, fixed endpoint, valid generated request ID, and
non-negative metrics. If validation fails, drop the unsafe event and emit only a
fixed `log_metadata_invalid` counter/event with no rejected value. Do not offer a generic
`map[string]any`, formatted message, raw error, argv, environment, or body field.
Set `ModelAlias`/`Provider` only after successful registry resolution, so an
attacker-controlled unknown model string is never logged. Populate process counts
and durations only from `core.Result.Meta` or
`core.OutcomeError.ResultMetadata`; failed requests omit unavailable metrics.
Accept stop fields only from the fixed enum set. A fake `CounterSink` test proves
client disconnect increments exactly once, attempts no body/header write after
cancellation, and does not double-count a simultaneous backend return. Plant a
distinct secret in every forbidden surface and scan captured logs.

- [ ] **Step 9: Verify black-box HTTP behavior**

Run:

```bash
gofmt -w internal/httpapi internal/observability
go test ./internal/httpapi ./internal/observability
go test -race -tags=integration -count=3 ./internal/httpapi
go test ./...
```

Expected: all tests PASS with exact envelopes, limits, cancellation, and no secret
leakage.

- [ ] **Step 10: Record the no-commit checkpoint**

Run:

```bash
rg -n 'Authorization|Request\.Body|Stdout|Stderr|Args|Env' internal/httpapi internal/observability
```

Expected: production matches are limited to header validation, bounded body
reading, and numeric metadata; no sensitive value reaches logging/encoding. Do
not initialize Git.

---

### Task 17: Application Assembly, Commands, Signals, and Shutdown

**Files:**

- Create: `internal/app/app.go`
- Create: `internal/app/app_test.go`
- Create: `internal/app/app_integration_test.go`
- Modify: `internal/cli/cli.go`
- Modify: `internal/cli/cli_test.go`
- Modify: `cmd/ai-cli-gateway/main.go`
- Create: `cmd/ai-cli-gateway/signals_unix.go`
- Create: `cmd/ai-cli-gateway/signals_windows.go`

**Interfaces:**

- Produces: `app.Serve(ctx context.Context, configPath string,
  deps app.Dependencies) error`.
- Produces: `app.Doctor(ctx context.Context, configPath string, jsonOutput bool,
  stdout io.Writer, deps app.Dependencies) int`.
- Produces: `cli.RunContext(context.Context, []string, io.Writer, io.Writer) int`;
  the original `cli.Run` remains a background-context wrapper for unit callers.
- Finalizes: `ai-cli-gateway version`, `serve --config PATH`, and
  `doctor --config PATH [--json]`.

- [ ] **Step 1: Write failing CLI parsing tests**

Cover exact success/error behavior:

```text
ai-cli-gateway version
ai-cli-gateway serve --config PATH
ai-cli-gateway doctor --config PATH
ai-cli-gateway doctor --config PATH --json
ai-cli-gateway doctor --json --config PATH
ai-cli-gateway --help
ai-cli-gateway version --help
ai-cli-gateway serve --help
ai-cli-gateway doctor --help
```

`PATH` is one nonempty following token and may be relative, but cannot start with
`-`. Reject `--config=PATH`, `-h`, bare `doctor`, a `help` command, missing or
duplicate flags, `--json` on serve, help combined with other flags, unknown flags,
positional arguments, extra version arguments, and unknown commands. Help writes
the exact three-line command usage to stdout and returns 0; every syntax error
writes the same usage to stderr and returns 2. `version` retains
`ai-cli-gateway <version> (<commit>, <date>)` plus one newline on stdout.

Serve maps nil to 0 with no stdout or stderr output, exact config invalid to
`configuration_invalid\n` on stderr/2, not-ready with or without cleanup failure
to `gateway_not_ready: run ai-cli-gateway doctor\n` on stderr/1, and every other
result to `serve_failed: run ai-cli-gateway doctor\n` on stderr/1. Doctor writes
its sole text, JSON, `configuration_invalid\n`, or `doctor_failed\n` output to the
supplied stdout and returns the exact app code; CLI adds no stdout or stderr
output and no hint.
Tests inject app functions so CLI unit tests never open listeners or run a real
provider. No path, TOML, probe, process, stdout, stderr, or arbitrary error text is
printed.

- [ ] **Step 2: Write failing assembly and shutdown tests**

With injected doctor dependencies, listener, adapters, and signal context,
assert:

- structural config failure occurs before any executable probe;
- core diagnostic failure prevents listening;
- zero ready providers creates no serving resource, cleans the transferred root,
  and returns only the fixed not-ready result and CLI hint;
- one ready plus two not-ready providers starts and serves all model aliases;
- requests for a not-ready provider return `provider_not_ready`;
- the configured gateway key is required at startup when named;
- two instances cannot own one runtime root;
- cancellation first closes the listener, then rejects/cancels queue work, reaps
  process trees, cleans runtimes, runs janitor, and closes the root lock;
- cleanup failure yields a non-zero exit;
- no output contains planted config, credential, prompt, stdout, or stderr data.

- [ ] **Step 3: Run focused tests and observe failure**

Run:

```bash
go test ./internal/app ./internal/cli
```

Expected: FAIL because app assembly and final command dispatch are absent.

- [ ] **Step 4: Assemble immutable startup state**

`Serve` performs this exact order:

1. strict `config.Load`;
2. construct real adapters filtered to exactly the configured provider set and
   production doctor dependencies: `process.OpenRoot`, thin `Root.Janitor` and
   `Root.Close` functions, the production runtime-ID function, and exported
   `doctor.NewProcessProbeController` assigned directly to
   `Dependencies.NewProbeController`; obtain the running gateway executable for
   `GatewayExecutable`, which doctor resolves and validates before any adapter or
   process call;
3. run core/provider doctor checks, which open and lock the runtime root, run the
   startup janitor and bounded probes, and return resolved safe provider configs
   plus the one canonical registry and ownership of that locked root;
4. fail directly if core is unsafe, whose diagnosis transfers no root; otherwise
   take the core-ready root and immutable registry/resolved snapshots before
   checking provider-ready count. If zero providers are ready, construct no
   scheduler, supervisor, Gateway, HTTP ID source, server, or listener; run one
   fresh bounded final janitor, close the root exactly once, and return fixed
   `ErrNotReady`, joined only with fixed `ErrShutdown` when cleanup fails;
5. for a nonzero ready count, call `Diagnosis.ResolvedProviders()` and construct
   schedulers/supervisors only
   for ready resolved providers, preserve resolved not-ready entries without
   schedulers/supervisors, and create a zero-config, nil-scheduler/supervisor,
   canonical not-ready runtime entry for every configured unresolved provider;
6. reuse `Diagnosis.Registry()` rather than rebuilding it, then construct
   `schema.DefaultLimits` with configured schema/final caps,
   `httpapi.RequestLimits` with configured input/instructions/schema caps and
   fixed parser bounds, gateway, opaque HTTP ID source, counter sink, and HTTP
   server;
7. create the already-validated loopback listener;
8. serve until context cancellation or server error.

Each failure unwinds already-created schedulers, roots, and handles in reverse
order. Provider processes use only the defensive resolved-provider snapshot; they
do not return to raw configured executable symlinks. The runtime map contains
exactly every configured provider, while the canonical registry keeps every
configured alias even when its provider is not ready. Do not mutate configuration
or readiness after startup except the containment-failure degradation defined in
Task 15.

- [ ] **Step 5: Implement graceful signal handling**

The executable entry point calls a platform helper that returns interrupt and
termination signals. Before normal CLI parsing it gives the exact internal
`__process-selftest` argv to the existing `selftest.Main`; all other argv continues
to:

```go
ctx, stop := signal.NotifyContext(
	context.Background(),
	shutdownSignals()...,
)
code := cli.RunContext(ctx, os.Args[1:], os.Stdout, os.Stderr)
stop()
os.Exit(code)
```

The `//go:build !windows` helper returns `os.Interrupt` and
`syscall.SIGTERM`; the `//go:build windows` helper returns `os.Interrupt`. On
cancellation, derive `shutdownCtx` with the configured timeout from
`context.Background()` rather than the already canceled serve context. Wrap the
listener with one idempotent close observer, begin
`http.Server.Shutdown(shutdownCtx)` in a goroutine, and wait for either its
completed listener-close classification or the HTTP result. If the HTTP result
arrives first, call the wrapper's `Close` synchronously and wait for that
classification. This observed completion—not goroutine creation—is the required
happens-before edge.

Only after that handshake call bounded `gateway.Shutdown(shutdownCtx)` to reject
queued work and cancel active executions. Consume the HTTP result; every non-nil
result retains fixed shutdown failure and calls `http.Server.Close` to force body
readers and connections closed. A listener-close failure is also retained. If
bounded Gateway shutdown failed, retry it synchronously with
`context.Background()` until every scheduler worker returns. Drain retained
supervisors in reverse construction order; after a bounded error, retry that
supervisor synchronously with `context.Background()` until all runtime leases,
process waits, and containment ownership drain. Then run one final janitor with a
fresh cleanup context and close the shared root exactly once.

The configured HTTP grace is a hard network deadline; it is not a process
ownership deadline. Gateway and supervisor drain may exceed it after forced HTTP
close. Never return to `main`, transfer ownership to a background goroutine, close
the root, or permit `os.Exit` while ownership remains. Join only fixed safe errors and
return non-zero if any listener, HTTP, Gateway, supervisor, janitor, or root
invariant fails.

- [ ] **Step 6: Implement doctor command exit semantics**

`doctor` uses the same strict config, adapter set, root-acquiring diagnosis path,
and non-inference probes but does not listen. It obtains a defensive
`Diagnosis.Report()`, encodes it through the validating writer, and closes
`Diagnosis.RuntimeRoot` exactly once when that field is non-nil. Expected core
failures already return a nil root. Human and JSON output are deterministic.
Exact results are:

- valid core-ready report with at least one provider ready: report only, exit 0;
- valid core-unsafe or zero-ready report: report only, exit 1;
- report writer or transferred-root close failure: no appended text, exit 1;
- pre-canceled context or any non-nil diagnosis error:
  `doctor_failed\n` when writable, exit 1; and
- config open, decode, or semantic failure: `configuration_invalid\n` when
  writable, exit 2.

CLI usage failure also exits 2 with the exact usage on stderr. Doctor output is
owned by `app.Doctor`; CLI never appends a hint. It closes a returned root exactly
once. Task 14 has already made the bounded controller shutdown attempt and, after
a context result, synchronously waited for the background-context ownership drain
before returning. No undrained root reaches `os.Exit`. Raw child output is
discarded after adapter filtering.

- [ ] **Step 7: Verify application integration**

Run:

```bash
gofmt -w cmd internal/app internal/cli
go test ./internal/app ./internal/cli
go test -race -tags=integration ./internal/app
go test ./...
CGO_ENABLED=0 go build -o "${TMPDIR:-/tmp}/ai-cli-gateway-app-check" \
  ./cmd/ai-cli-gateway
```

Expected: all tests and build PASS; the generated binary is outside the
repository.

- [ ] **Step 8: Record the no-commit checkpoint**

Run:

```bash
set -euo pipefail
test ! -e .git
if pgrep -f '[f]ake-ai-cli|__process-[s]elftest'; then
  exit 1
else
  process_scan_status=$?
  test "$process_scan_status" -eq 1 || exit "$process_scan_status"
fi
```

Expected: Git is still absent and no fixture process remains.

---

### Task 18: Public Documentation, Examples, Repository Hygiene, and CI

**Files:**

- Create: `README.md`
- Create: `LICENSE`
- Create: `THIRD_PARTY_NOTICES.md`
- Create: `CONTRIBUTING.md`
- Create: `SECURITY.md`
- Create: `config.example.toml`
- Create: `deploy/systemd/ai-cli-gateway.service`
- Create: `.gitignore`
- Create: `.github/workflows/ci.yml`
- Create: `internal/securitytest/repository_test.go`
- Create: `internal/provider/codex/codex_live_test.go`
- Create: `internal/provider/claude/claude_live_test.go`
- Create: `internal/provider/gemini/gemini_live_test.go`
- Modify: `Makefile`
- Modify: `docs/superpowers/specs/2026-07-30-ai-cli-gateway-design.md`
- Modify: `docs/superpowers/plans/2026-07-30-ai-cli-gateway.md`
- Modify: `internal/testutil/fakecli.go`
- Modify: `internal/testutil/fakecli_test.go`
- Modify: `internal/process/supervisor_unix_test.go`
- Modify: `internal/process/supervisor_integration_test.go`
- Modify: `internal/process/runner_windows_integration_test.go`

**Interfaces:**

- Documents only the implemented Responses API-compatible subset.
- Provides placeholder-only configuration and a hardened systemd example.
- CI verifies Linux, macOS, and Windows without provider credentials.

Task 18 has one controlling mutation and review order:

1. reconcile this retained design and plan against final Task 17 production code
   and obtain a focused independent APPROVED review;
2. repair the trimpath test helper and generic Unix/Windows process-integration
   scheduling budgets RED-first, preserve every short semantic timeout, and pass
   the complete trimpath/integration gates;
3. reconcile current external provider and GitHub Actions contracts in the two
   retained public documents and obtain a focused independent APPROVED review;
4. run the sole mutating `go mod tidy`, verify and freeze the module files plus
   cross-platform compiled-module union, then derive the complete license notice
   inventory;
5. implement all remaining documentation, repository security, live-tag, and CI
   slices and run complete verification; and
6. obtain one final aggregate independent Task 18 review before removing the
   temporary `.superpowers` ledger and running the final pre-Git scan.

No module, dependency, or notice mutation is allowed after step 4. Historical
official-document and installed-`--help` research remains provenance and is not
rewritten; any retained earlier scaffold is explicitly superseded by the adjacent
final contract.

- [ ] **Step 0A: Reconcile the durable lifecycle and release contract**

Replace every obsolete Doctor background-ownership clause with the bounded first
attempt followed, on a context result, by the synchronous background-context
ownership drain. Record listener-close observation before Gateway drain, hard
HTTP grace and force close versus unbounded safety ownership drain, core-ready
zero-ready root
cleanup, exact CLI/output/exit behavior, and the no-installed-provider Task 19
path. Carry the platform config split, notice/token catalogs, trimpath/test-budget
gate, and tidy freeze rules into both public files. Obtain an independent
code-to-document APPROVED review before any later Task 18 mutation.

- [ ] **Step 0B: Repair trimpath and process-test scheduling portability**

RED-first, make `testutil` accept a caller-derived repository root only when the
path is absolute and has a regular `go.mod` with the exact module declaration;
otherwise search upward from the current package working directory with a fixed
bound and the same validation. Reject roots, unrelated modules, symlinks,
unreadable files, and ambiguity. Raise only the nested test `go build` deadline to
60 seconds and retain its 64 KiB output cap.

Generic Unix and Windows fake-process success/cleanup limits and ordinary event
waits become 30 seconds; their outer ownership contexts become 60 seconds. Every
test whose subject is execution timeout, context deadline, TERM/KILL escalation,
cleanup timeout/quarantine, deferred wait, or bounded failure constructs and
asserts an explicit short local limit. Add a guard test for that distinction; do
not change production limits. Verify with focused repetitions, full integration
and race runs, native Windows CI, `go test -trimpath -count=1 ./...`, and
`GOFLAGS=-trimpath go test -count=1 ./internal/testutil ./internal/testcli`.

- [ ] **Step 0C: Reconcile external contract drift**

Without changing production code, tests, provider version ranges, module files, or
historical local research, amend both retained public documents for current
external contracts. Preserve the exact required README opening sentence and public
repository description. Add the dated Gemini consumer transition from Google's
[2026-05-19 transition announcement](https://developers.googleblog.com/an-important-update-transitioning-gemini-cli-to-antigravity-cli/)
and
[2026-06-18 shutdown announcement](https://github.com/google-gemini/gemini-cli/discussions/28017).
Also cite Google's
[consumer Login-with-Google deprecation notice](https://developers.google.com/gemini-code-assist/docs/deprecations/code-assist-individuals)
and current
[Gemini CLI quota and pricing documentation](https://geminicli.com/docs/resources/quota-and-pricing/).
Keep the adapter environment/external-credential only with exactly its three
accepted shapes and a disposable home. State that Google stopped the consumer
Login-with-Google path for Gemini Code Assist for individuals, Google AI Pro, and
Google AI Ultra on 2026-06-18 and points those users to Antigravity; Google says
Code Assist Standard and Enterprise plus paid API-key access remain, while current
official docs also describe API-key and Vertex tiers. Do not present these paths
as exhaustive. Availability, billing tier, quota, entitlement, and live credential
validity are exclusively upstream; configured/implemented/readiness proves local
checks only and provider execution is authoritative. Cached personal OAuth is
unsupported, and Antigravity CLI is out of scope.

Replace the four synthetic Task 10 environment assignment values with their exact,
pairwise-distinct angle-bracket placeholders without widening the placeholder
grammar. Update only the stale official CI majors to `actions/checkout@v7` and
`actions/setup-go@v7`; retain `golangci/golangci-lint-action@v9`. Record that all
three use Node24, so self-hosted runners require `actions/runner` `v2.327.1` or
later, and use the normal `pull_request` event rather than `pull_request_target`.

Run focused official-link, active-pin, assignment, forbidden-old-clause, Markdown
fence, and trailing-whitespace scans. Record the superseded and new public-document
hashes plus unchanged production/module fingerprints in a new external-drift
implementer report, then obtain an independent focused APPROVED review before any
module or license-surface mutation.

- [ ] **Step 0D: Freeze the module and license surface**

After Steps 0A, 0B, and 0C are approved/GREEN, run Task 18's only mutating
`go mod tidy`, inspect any change, and run `go mod verify`. For Linux amd64/arm64,
Darwin amd64/arm64, and Windows amd64 with `CGO_ENABLED=0`, compute the sorted
union of non-standard modules compiled by `./cmd/ai-cli-gateway`. Freeze SHA-256
values for `go.mod`, `go.sum`, and that union before notices or remaining public
work. Any later graph change returns to this step and independent review.

- [ ] **Step 1: Write the failing repository-hygiene test**

`internal/securitytest/repository_test.go` locates the repository without assuming
Git exists, walks without following symlinks, rejects every symlink entry, and
rejects:

- PEM private-key headers;
- common committed secret assignments with non-placeholder values;
- provider token/auth database filenames;
- absolute paths containing the current developer username;
- repository paths containing control characters, newlines, or invalid UTF-8;
- generated gateway/fake-CLI binaries;
- local `config.toml`, `.env`, coverage, test, or profiler artifacts.

Explicitly allow documented environment-variable names, placeholder values, and
`config.example.toml`. Historical local executable paths recorded in the approved
design/plan research are the sole username-path exception; secrets and auth-file
contents remain forbidden there too. Assert the test fails against a temporary
planted secret fixture under `t.TempDir()`, which test cleanup removes. Failures
report only category and repository-relative path, never matched content.

Use one closed, token-boundary-aware scanner catalog for both worktree bytes and a
temporary materialization of the staged index:

- OpenAI legacy `sk-` plus exactly 48 base62 characters, and `sk-proj-`,
  `sk-svcacct-`, or `sk-admin-` plus 20–256
  base62/underscore/hyphen characters;
- Anthropic `sk-ant-apiNN-` plus 40–256
  base62/underscore/hyphen characters;
- Google/Gemini exact `AIza` plus 35 base62/underscore/hyphen characters;
- GitHub `ghp_`, `gho_`, `ghu_`, and independently evidenced `ghr_` plus exactly
  36 alphanumeric characters;
- legacy GitHub App installation `ghs_` plus exactly 36 alphanumeric characters;
- current stateless GitHub App installation `ghs_` plus a 36–768-character
  `[A-Za-z0-9._-]` candidate accepted only with exactly two dots and three
  nonempty base64url segments; and
- fine-grained `github_pat_` with exact 22- and 59-character segments separated
  by one underscore.

Every family has positive and conservative length/alphabet/boundary near-miss
fixtures; stateless `ghs_` additionally covers empty segments and wrong dot counts.
Do not add generic JWT heuristics. The scanner skips only Git metadata and the
temporary uncommitted `.superpowers` ledger during implementation. Remove that
ledger before the final pre-Git scan; never add it to `.gitignore` or the index.

- [ ] **Step 2: Write README with an exact compatibility boundary**

The first sentence must be exactly:

> AI CLI Gateway turns locally authenticated AI CLIs into an OpenAI Responses-compatible API.

Immediately state that it implements a small **Responses API-compatible subset**,
not full OpenAI API compatibility. In the same opening section, qualify the
provider-neutral first sentence: Gemini accepts exactly its three documented
environment/external credential shapes and uses a disposable home; Google stopped
the consumer Login-with-Google path for Gemini Code Assist for individuals,
Google AI Pro, and Google AI Ultra on 2026-06-18 and points those users to
Antigravity; Google says Code Assist Standard and Enterprise plus paid API-key
access remain, while current official docs also describe API-key and Vertex tiers;
actual availability, billing tier, quota, entitlement, and live validity are
exclusively upstream; cached personal OAuth is unsupported; Antigravity CLI is
out of scope; and configured/implemented/readiness proves local checks only.
Include:

- the client → gateway → adapter → final-output architecture;
- dated links to the official OpenAI Responses contract and each pinned
  provider CLI contract used by the design;
- dated links to Google's Gemini CLI transition, consumer deprecation, and current
  quota/pricing sources used to separate upstream access from the local credential
  boundary;
- supported/unsupported request matrix and exact endpoints;
- text and JSON Schema curl examples;
- response and stable error examples;
- build, exact `version`, `serve --config PATH`, both accepted Doctor `--json`
  orders, help/usage text, exit status, and configuration instructions;
- provider binary/version/config-home setup;
- Gemini disposable-home and external/environment credential limitation;
- implementation/live-verification/readiness status terminology;
- queue, timeout, output-cap, observed listener-close-before-Gateway shutdown,
  hard HTTP grace/force close, and safety-first process-containment drain;
- loopback/API-key trust boundary and dedicated-OS-user recommendation;
- Unix deliberate-`setsid`, power loss, and gateway `SIGKILL` containment limits;
- provider-internal retry/cost boundary and no gateway retry/fallback;
- no token extraction/copy/storage and no prompt/output logging;
- the fact that `instructions` is a separately framed prompt section, not an
  enforceable developer-message isolation boundary against adversarial input;
- the exact short terms notice:

> You are responsible for installing and authenticating each provider CLI and for
> using it in accordance with its applicable terms.

Do not claim complete compatibility, universal internal-tool elimination, valid
Gemini OAuth reuse, Gemini personal-account availability, upstream entitlement,
or live verification that was not run.

- [ ] **Step 3: Write safe configuration and curl examples**

`config.example.toml` is the Unix/systemd deployment example and includes only
generic absolute placeholders such as
`/opt/ai-cli-gateway/bin/codex` and `/var/lib/ai-cli-gateway/codex-home`; it names
`AI_CLI_GATEWAY_API_KEY` and `GEMINI_API_KEY` without values. It defines the
model aliases, queue/process defaults, server gates, and loopback listener.
Comments document the three compiled-in supported provider version ranges; no
config key can override those tested compatibility guards. The Gemini comments
also state that naming `GEMINI_API_KEY` proves only a locally accepted credential
shape, not upstream availability, billing tier, quota, entitlement, or key
validity; the gateway neither restricts nor infers the upstream tier.

Every OS reads the committed bytes unchanged and checks valid TOML, the complete
field/model/provider set, exact safe defaults, the closed placeholder vocabulary,
and absence of identity, secret, auth, control, or invalid UTF-8 data. Unix also
passes that unchanged file to `config.Decode`. Native Windows creates a test-local
copy, deterministically replaces every exact known Unix placeholder exactly once
with a safe drive-absolute equivalent, verifies no Unix deployment path remains
in a path-valued field, and only then calls the unchanged production decoder.
This split must not weaken production Windows validation or claim the committed
Unix example is directly usable there. README documents drive-absolute/UNC paths
and the native-executable versus absolute `node.exe` plus one absolute JS
entrypoint forms.

Every curl example uses:

```bash
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \
  --data-binary @request.json \
  http://127.0.0.1:8080/v1/responses
```

The JSON body lives in a separately shown file/heredoc; no real key, home path,
provider model, or auth material is embedded.

- [ ] **Step 4: Add license, contribution, and security policy**

`LICENSE` is the unmodified Apache License, Version 2.0 text.
`THIRD_PARTY_NOTICES.md` is derived from the already frozen multi-platform
compiled-module union, not only the direct `require` block. Reconfirm exact
versions and upstream license files for `github.com/pelletier/go-toml/v2` (MIT),
`github.com/santhosh-tekuri/jsonschema/v6` (Apache-2.0), `golang.org/x/sys`
(BSD-3-Clause), and indirect `golang.org/x/text` (BSD-3-Clause); include every
module in the frozen union and its required copyright/license notice. Verify
module license files rather than copying package descriptions from memory. A dependency,
module-file, compiled-union, or notice delta after the freeze returns to Step 0D
and independent review. `CONTRIBUTING.md` requires TDD, fake CLI coverage,
formatting, vet, lint, unit, race, integration, and cross-platform process tests.
`SECURITY.md` points reporters to GitHub private vulnerability reporting for the
repository and tells them not to include real tokens, prompts, outputs, or auth
files in reports.

`.gitignore` covers:

```text
/ai-cli-gateway
/fake-ai-cli
*.test
*.exe
*.prof
coverage*.out
/config.toml
.env
.env.*
!.env.example
```

Add provider auth/cache filenames narrowly without ignoring the committed docs or
examples.

- [ ] **Step 5: Add hardened systemd guidance**

The example uses a dedicated `ai-cli-gateway` user, an `0600` root-owned
environment file, loopback networking, `NoNewPrivileges=true`,
`PrivateTmp=true`, `PrivateDevices=true`, `ProtectSystem=strict`,
`ProtectHome=true`, `UMask=0077`, empty capability sets, an explicit writable
runtime/state directory, `LimitCORE=0`, `KillMode=control-group`,
restart-on-failure, and a
bounded `TimeoutStopSec` longer than the gateway shutdown timeout. Explain that Node-based
CLIs may be incompatible with `MemoryDenyWriteExecute`, so it is not enabled
blindly.

Use placeholder paths only. The service never places a secret directly in
`ExecStart`, `Environment=`, command arguments, or the repository.

- [ ] **Step 6: Add exact local verification targets**

Finalize `Makefile` targets:

```make
GOLANGCI_LINT ?= golangci-lint

fmt-check:
	@unformatted_files="$$(gofmt -l .)" && { \
		test -z "$$unformatted_files" || { \
			printf '%s\n' "$$unformatted_files"; exit 1; \
		}; \
	}

vet:
	go vet ./...

lint:
	$(GOLANGCI_LINT) run ./...

test:
	go test ./...

race:
	go test -race ./...

integration:
	go test -tags=integration ./...

build:
	CGO_ENABLED=0 go build -trimpath -o "$${TMPDIR:-/tmp}/ai-cli-gateway" \
		./cmd/ai-cli-gateway

verify: fmt-check vet lint test race integration build
```

No target initializes Git, upgrades provider CLIs, or contacts a provider model.
When the pinned temporary linter from Task 1 is used, invoke Make as
`make GOLANGCI_LINT="${TMPDIR:-/tmp}/ai-cli-gateway-tools/bin/golangci-lint"
lint`.

- [ ] **Step 7: Add an opt-in live contract harness**

Each provider live test has `//go:build live` and two independent explicit gates:
`AI_CLI_GATEWAY_LIVE_PROBES=1` enables only version/help/auth-status probes, while
inference additionally requires `AI_CLI_GATEWAY_LIVE_INFERENCE=1` and the
matching `AI_CLI_GATEWAY_LIVE_CODEX_INFERENCE=1`,
`AI_CLI_GATEWAY_LIVE_CLAUDE_INFERENCE=1`, or
`AI_CLI_GATEWAY_LIVE_GEMINI_INFERENCE=1` variable. Absent gates call `t.Skip`;
the default unit, integration, Make, and CI commands never contact a provider
service.

Default verification and CI nevertheless compile the live-tag sources without
executing a test:

```text
go test -tags=live -run '^$' ./internal/provider/...
```

The first executable live-test action checks the global probe gate before reading
provider config or credentials, creating a runtime, or invoking any command.
Task 18 default work, Task 19 release verification, and CI never execute or inspect
an installed provider binary; they use fake executables and this compile-only gate.

The harness uses a dedicated disposable alias/canary selected entirely through
documented environment-variable names. It covers the pinned version/help
contract, one minimal text result, one minimal schema result, cancellation,
nil-versus-empty instruction framing, and the adapter's declared
session/tool/extension suppression controls. It never prints a prompt, output,
credential, identity, or full child error, and its cleanup runs even on failure.
README warns that inference checks may incur usage and are operator-triggered
only.

- [ ] **Step 8: Add multi-platform GitHub Actions**

Pin current major official actions:

```yaml
- uses: actions/checkout@v7
- uses: actions/setup-go@v7
  with:
    go-version-file: .go-version
    cache: true
```

As of 2026-08-02, `actions/checkout@v7`, `actions/setup-go@v7`, and the retained
`golangci/golangci-lint-action@v9` all declare the Node24 action runtime. GitHub-
hosted runners satisfy this automatically; self-hosted runners require
`actions/runner` `v2.327.1` or later. Trigger untrusted contribution CI with the
normal `pull_request` event, never the privileged `pull_request_target` event.

Create:

- Linux lint job using `golangci/golangci-lint-action@v9`;
- Linux unit/race/integration/live-compile/trimpath-build job, including unchanged
  Unix config-example decode;
- macOS unit/race/integration/trimpath-build job, including unchanged Unix
  config-example decode;
- Windows unit/integration/trimpath-build job, running the real Job Object
  descendant/cancellation/cleanup/handle-quiescence tests plus native ACL,
  reparse-point, and deterministic config-copy substitution/decode tests;
- a cross-compile job for `linux/amd64`, `linux/arm64`, `darwin/amd64`,
  `darwin/arm64`, and `windows/amd64`, with outputs under the runner temp
  directory rather than uploaded artifacts and `CGO_ENABLED=0`.

Grant only `contents: read`, set no provider secrets, and fail rather than skip
the platform containment tests. If Windows race is unsupported, keep Windows
integration and rely on Linux/macOS race runs as specified. Native Windows
execution is a mandatory post-push acceptance gate; cross-compilation never
substitutes for it or turns a skip/failure into success.

- [ ] **Step 9: Verify documentation and hygiene**

Run:

```bash
go test ./internal/securitytest
go test -trimpath -count=1 ./...
GOFLAGS=-trimpath go test -count=1 ./internal/testutil ./internal/testcli
go test -tags=live -run '^$' ./internal/provider/...
make fmt-check
make vet
make lint
make test
make race
make integration
make build
```

Expected: all commands PASS; README opening sentence is exact; no provider network
inference occurs. Freeze final file/evidence hashes and obtain one aggregate
independent Task 18 review covering the earlier reconciliation, test-budget
classification, module/inventory hashes, notices, security fixtures, docs, live
compile gate, CI, and all verification. Every finding must be closed before the
next step.

- [ ] **Step 10: Record the final pre-Git checkpoint**

Run:

```bash
set -euo pipefail
test ! -e .git
test ! -e .superpowers
go test -count=1 ./internal/securitytest
find . -type f -perm -111 -print
```

Remove exactly the temporary `.superpowers` ledger only after the focused Task 17,
focused reconciliation, and final aggregate Task 18 reviews are APPROVED and
their findings/hashes are recorded elsewhere. The authoritative scanner now sees
the whole retained public tree with no temporary-tree exception present. Expected:
the scanner passes and executable files are only intentional scripts, if any. Do
not initialize Git yet.

---

### Task 19: Final Verification, Path Transition, Git Initialization, and Public Push

**Files:**

- Verify: every file listed in the Planned File Map and Tasks 1-18.
- Move only after verification:
  `/Users/krkarma777/Dev/spawngate` →
  `/Users/krkarma777/Dev/ai-cli-gateway`.
- Create after verification: `.git/` metadata and one intentional initial commit.
- Create externally: public GitHub repository `ai-cli-gateway`.

**Interfaces:**

- Produces a verified single-binary Go source repository on `main`.
- Produces the public connected GitHub URL.
- Does not publish generated binaries, secrets, local auth files, or local
  verification configuration.

- [ ] **Step 1: Run the complete release gate from a clean process state**

First assert no fixture process is alive. Reproduce, without mutation, the module
and license surface frozen during Task 18:

```bash
set -euo pipefail
if pgrep -f '[f]ake-ai-cli|__process-[s]elftest'; then
  exit 1
else
  process_scan_status=$?
  test "$process_scan_status" -eq 1 || exit "$process_scan_status"
fi
go mod tidy -diff
go mod verify
```

If the installed Go version lacks `tidy -diff`, perform an equivalent tidy in a
clean temporary copy and compare module files. Require an empty diff, the exact
frozen `go.mod`/`go.sum` hashes, and the exact frozen cross-platform compiled-module
union. Any delta stops Task 19 and returns the complete graph/notices change to
Task 18 TDD, license verification, and independent review; Task 19 never mutates
the approved dependency surface. Then run:

```bash
set -euo pipefail
unformatted_files="$(gofmt -l .)"
test -z "$unformatted_files"
go vet ./...
golangci-lint run ./...
go test -count=1 ./...
go test -race -count=1 ./...
go test -tags=integration -count=1 ./...
go test -trimpath -count=1 ./...
GOFLAGS=-trimpath go test -count=1 ./internal/testutil ./internal/testcli
go test -tags=live -run '^$' ./internal/provider/...
CGO_ENABLED=0 go build -trimpath \
  -o "${TMPDIR:-/tmp}/ai-cli-gateway-release-check" \
  ./cmd/ai-cli-gateway
go version -m "${TMPDIR:-/tmp}/ai-cli-gateway-release-check"
```

Expected: `gofmt -l` emits nothing; every command exits 0; the module/version
metadata is correct and the inspected module files remain unchanged throughout
the release gate.

- [ ] **Step 2: Run platform and behavior-specific release checks**

On the development macOS host run the real process-group integration tests three
times under the race detector. Cross-build without writing into the repository:

```bash
set -euo pipefail
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
  -o "${TMPDIR:-/tmp}/ai-cli-gateway-linux-amd64" ./cmd/ai-cli-gateway
CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath \
  -o "${TMPDIR:-/tmp}/ai-cli-gateway-linux-arm64" ./cmd/ai-cli-gateway
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath \
  -o "${TMPDIR:-/tmp}/ai-cli-gateway-darwin-amd64" ./cmd/ai-cli-gateway
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath \
  -o "${TMPDIR:-/tmp}/ai-cli-gateway-darwin-arm64" ./cmd/ai-cli-gateway
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath \
  -o "${TMPDIR:-/tmp}/ai-cli-gateway-windows-amd64.exe" ./cmd/ai-cli-gateway
```

Run HTTP black-box and fake-provider tests for text, valid schema JSON, duplicate
JSON, schema mismatch, queue saturation, cancellation, timeout, flood, descendant
cleanup, and graceful shutdown. Run canonical `doctor --config PATH --json`
through the fake integration harness. Task 19 does not execute, upgrade,
authenticate, inspect, or make a
request through any installed Codex, Claude, or Gemini binary. Unless a separate
explicitly gated Task 18 live run produced retained evidence, report all three
adapters as `implemented`, `live-verified: not run`, and operator readiness as
`unassessed`; never infer readiness from historical installed-version research.

- [ ] **Step 3: Audit repository contents and secret boundaries**

Run:

```bash
set -euo pipefail
test ! -e .superpowers
go test -count=1 ./internal/securitytest
find . -type f -print | sort
find . -type l -print
find . -type f -perm -111 -print
```

The reviewed Go scanner—not a second drifting regular expression—is authoritative
for token catalog, assignments, auth basenames, developer paths, invalid names,
symlinks, binary magic, and generated artifacts. Expected: no secret, local auth
artifact, generated binary, unexpected symlink, or developer-specific path outside
the historical design/plan research record. Review every executable file.

- [ ] **Step 4: Confirm the destination before moving**

Resolve both paths explicitly:

```bash
pwd -P
test "$(pwd -P)" = "/Users/krkarma777/Dev/spawngate"
test ! -e "/Users/krkarma777/Dev/ai-cli-gateway"
test ! -e .git
```

Expected: source is exact, destination is absent, and Git is absent. If the
destination or Git exists, stop and inspect; do not overwrite, merge, or delete
anything. Moving outside the current writable root requires the scoped filesystem
approval.

- [ ] **Step 5: Move the verified tree and initialize Git once**

After the destination check succeeds:

```bash
mv /Users/krkarma777/Dev/spawngate /Users/krkarma777/Dev/ai-cli-gateway
cd /Users/krkarma777/Dev/ai-cli-gateway
git init -b main
git status --short
```

Expected: every intended source/document file is untracked and no generated or
secret file appears.

- [ ] **Step 6: Stage intentionally and audit the exact commit**

Stage only the reviewed top-level paths:

```bash
git add .github .gitignore .go-version .golangci.yml CONTRIBUTING.md LICENSE \
  Makefile README.md SECURITY.md THIRD_PARTY_NOTICES.md cmd config.example.toml \
  deploy docs go.mod go.sum internal
git diff --cached --check
git diff --cached --stat
git diff --cached --name-status
git status --short
```

Inspect the full staged diff and run an index-authoritative scan:

```bash
set -euo pipefail
git diff --quiet
test -z "$(git ls-files --others --exclude-standard)"
staged_tree="$(mktemp -d "${TMPDIR:-/tmp}/ai-cli-gateway-index.XXXXXX")"
trap 'rm -rf -- "$staged_tree"' EXIT
git checkout-index --all --prefix="$staged_tree/"
go test -count=1 ./internal/securitytest -run '^TestRepositoryHygiene$' \
  -args -scan-root="$staged_tree"
git ls-files -s
```

The test-only `-scan-root` seam requires an explicit absolute directory; ordinary
tests have no ambient override and always scan the repository. Review every
non-regular index mode from `git ls-files -s` and confirm `.gitignore` does not
conceal an expected source file. Only then create the one initial commit:

```bash
git commit -m "feat: initial AI CLI Gateway implementation"
git status --short
git log -1 --oneline
```

Expected: clean working tree and one commit on `main`.

- [ ] **Step 7: Resolve the connected GitHub account and collision**

Use the connected GitHub account. If exactly one personal account is connected
and it is `krkarma777`, proceed. If multiple accounts are connected, the owner
differs from the module path, or `ai-cli-gateway` now exists, stop and ask the
user because account/collision choices materially change the result. Apache-2.0
is already approved and must not be changed.

- [ ] **Step 8: Create the public repository and push `main`**

Create a public, non-template repository named `ai-cli-gateway`, with description:

```text
Turn locally authenticated AI CLIs into an OpenAI Responses-compatible API.
```

Do not auto-create a README, license, `.gitignore`, issue, release, or default
file. Enable GitHub private vulnerability reporting so `SECURITY.md` has a
working confidential channel. Add the returned HTTPS repository URL as `origin`,
then:

```bash
git remote -v
git push -u origin main
```

The user explicitly authorized repository creation and push, so no additional
approval is needed when the account and collision checks are unambiguous.

- [ ] **Step 9: Verify the remote result and report**

Read back the GitHub repository metadata and `main` head. Confirm:

- visibility is public;
- default branch is `main`;
- remote commit equals local `HEAD`;
- README renders with the exact opening sentence;
- license is detected as Apache-2.0;
- CI workflows are present and every required Linux, macOS, Windows, lint, and
  cross-build job reaches a successful terminal state; and
- the native Windows log visibly executes, without skip or allowed failure, the
  Job Object descendant/cancellation/cleanup/handle-quiescence plus ACL,
  reparse-point, and config-substitution tests. Cross-compilation is not accepted
  as this evidence. Poll through the connected
  GitHub integration with bounded status updates; if a required job fails,
  inspect it, fix through the same TDD/reverification path, push the correction,
  and wait again. Do not declare acceptance while any required job is queued,
  running, canceled, or failed.

Report the final path, GitHub URL, commit, changed-file summary, key architecture
and security decisions, every verification command/result, provider
implemented/live/readiness status, and final CI results. Do not include
authentication identities, credential values, prompts, or provider outputs.
