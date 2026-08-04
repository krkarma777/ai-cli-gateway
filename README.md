# AI CLI Gateway

AI CLI Gateway turns locally authenticated AI CLIs into an OpenAI Responses-compatible API.

It deliberately implements a small **Responses API-compatible subset**, not full OpenAI API compatibility. The gateway is a local, final-output bridge with strict validation; it is not a drop-in implementation of every OpenAI endpoint or feature.

The contract baseline is 2026-07-30, with the external provider transition notes below rechecked on 2026-08-02. The project supports locally prepared Codex CLI and Claude Code profiles, plus the three documented Gemini environment/external credential shapes; actual provider access remains an upstream decision.

## Architecture and scope

```text
Client
  -> POST /v1/responses
  -> AI CLI Gateway
  -> Codex / Claude Code / Gemini CLI adapter
  -> final text or locally validated JSON
```

Only these endpoints are implemented, and both are non-streaming:

- `POST /v1/responses` runs one configured provider adapter and returns a completed response.
- `GET /v1/models` returns an immutable configured alias snapshot and never starts a provider CLI. A listed alias can still return `503 provider_not_ready` when used.

There is no response retrieval endpoint, SSE streaming, tool-call round trip, session or conversation store, web UI, or database. Provider sessions are disabled where the pinned CLI exposes that control; the gateway itself stores no conversation state.

## Request contract

The top-level request subset is closed:

| Field | Supported subset |
|---|---|
| `model` | required nonempty configured alias string |
| `input` | required nonempty UTF-8 string |
| `instructions` | optional UTF-8 string or `null` |
| `text.format` | `text` or the strict `json_schema` profile below |
| `stream` | absent or exactly `false` |
| `store` | absent or exactly `false` |
| `tools` | absent or exactly `[]` |
| `tool_choice` | absent or exactly `"none"` |

Unknown fields and unsupported values return `400 unsupported_parameter`; they are never ignored. Duplicate keys, malformed JSON, a trailing JSON value, excessive data, and invalid field types are also rejected deterministically.

Unsupported inputs include array or multimodal `input`, nonempty `tools`, streaming, `previous_response_id` or other prior-response/conversation identifiers, `metadata`, `reasoning`, generation controls, provider-specific options, background execution, and stored responses. Setting `store` or `stream` to `true` is unsupported.

### Portable JSON Schema profile

A `json_schema` request has an object root and may use the seven types `object`, `array`, `string`, `number`, `integer`, `boolean`, and `null`. The closed keyword set is:

- `type`, `properties`, `required`, `additionalProperties`, and `items`;
- `enum` and `const`;
- `minLength`, `maxLength`, `minItems`, `maxItems`, `minProperties`, and `maxProperties`;
- `minimum`, `maximum`, `exclusiveMinimum`, and `exclusiveMaximum`; and
- `description` and `title`.

Every object schema must use `additionalProperties:false`, and every property must appear in `required`. There are no references, unions or combinators, patterns, formats, remote resolution, or Markdown-fence extraction. A provider must return exactly one JSON object that is duplicate-free. AI CLI Gateway validates it locally and performs no repair, fallback, or retry.

Structured JSON remains a validated string in `output_text.text`; AI CLI Gateway does not invent an `output_json` field.

## Requests and responses

The optional gateway key is read from the environment variable named by `server.api_key_env`. Put request data in a file so prompts and keys do not become command-line arguments.

### Text request

Save this synthetic body as `request.json`:

```json
{
  "model": "codex-local",
  "instructions": "Answer concisely.",
  "input": "Return a short greeting.",
  "text": {
    "format": {
      "type": "text"
    }
  },
  "stream": false,
  "store": false,
  "tools": [],
  "tool_choice": "none"
}
```

Then send it with the key supplied by the caller environment:

```bash
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \
  --data-binary @request.json \
  http://127.0.0.1:8080/v1/responses
```

### JSON Schema request

Replace `request.json` with this portable-profile body:

```json
{
  "model": "claude-local",
  "input": "Return one synthetic status value.",
  "text": {
    "format": {
      "type": "json_schema",
      "name": "status_result",
      "strict": true,
      "schema": {
        "type": "object",
        "properties": {
          "status": {
            "type": "string",
            "enum": ["ready", "waiting"]
          }
        },
        "required": ["status"],
        "additionalProperties": false
      }
    }
  }
}
```

Send it with the same safe invocation:

```bash
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \
  --data-binary @request.json \
  http://127.0.0.1:8080/v1/responses
```

### Complete success response

IDs and timestamps are generated per request. A text response has this complete stable shape:

```json
{
  "id": "resp_aaaaaaaaaaaaaaaaaaaaaaaaaa",
  "object": "response",
  "created_at": 1785369600,
  "completed_at": 1785369601,
  "status": "completed",
  "background": false,
  "error": null,
  "incomplete_details": null,
  "instructions": null,
  "model": "codex-local",
  "output": [
    {
      "id": "msg_bbbbbbbbbbbbbbbbbbbbbbbbbb",
      "type": "message",
      "status": "completed",
      "role": "assistant",
      "content": [
        {
          "type": "output_text",
          "annotations": [],
          "text": "Final provider output"
        }
      ]
    }
  ],
  "parallel_tool_calls": false,
  "previous_response_id": null,
  "store": false,
  "text": {
    "format": {
      "type": "text"
    }
  },
  "tools": [],
  "tool_choice": "none"
}
```

### Models

Use the same Bearer convention for the immutable model-alias snapshot:

```bash
curl --fail-with-body \
  -H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \
  http://127.0.0.1:8080/v1/models
```

A complete list response is:

```json
{
  "object": "list",
  "data": [
    {
      "id": "codex-local",
      "object": "model",
      "created": 0,
      "owned_by": "local"
    }
  ]
}
```

### Stable errors

Errors never include provider output or raw child-process diagnostics. For example, an unsupported value returns this exact envelope shape:

```json
{
  "error": {
    "message": "This parameter or value is not supported.",
    "type": "invalid_request_error",
    "param": "stream",
    "code": "unsupported_parameter"
  }
}
```

The stable catalog is:

| HTTP | Codes |
|---:|---|
| 400 | `invalid_json`, `invalid_request`, `unsupported_parameter`, `invalid_json_schema` |
| 401 | `invalid_bearer_key` |
| 404 | `not_found`, `model_not_found` |
| 405 | `method_not_allowed` |
| 408 | `request_timeout` |
| 413 | `request_too_large` |
| 415 | `unsupported_media_type` |
| 429 | `server_busy`, `queue_full`, `provider_rate_limited` |
| 500 | `process_cleanup_failed`, `internal_error` |
| 502 | `output_limit_exceeded`, `provider_protocol_error`, `structured_output_invalid`, `provider_failed` |
| 503 | `queue_timeout`, `provider_not_ready`, `provider_auth_required`, `service_shutting_down` |
| 504 | `provider_timeout` |

## Build and commands

Go 1.26.5 is required. A release-style local build writes outside the source tree:

```bash
CGO_ENABLED=0 go build -trimpath -o "${TMPDIR:-/tmp}/ai-cli-gateway" ./cmd/ai-cli-gateway
```

The public command grammar is exact:

```text
usage:
  ai-cli-gateway version
  ai-cli-gateway serve --config PATH
  ai-cli-gateway doctor --config PATH [--json]
```

Both JSON Doctor orders are accepted: `ai-cli-gateway doctor --config PATH --json` and `ai-cli-gateway doctor --json --config PATH`. The equals-sign form is intentionally not part of the grammar.

Help is available as `ai-cli-gateway --help`, `ai-cli-gateway version --help`, `ai-cli-gateway serve --help`, and `ai-cli-gateway doctor --help`.

The exit status is 0 for success or a clean handled shutdown, 1 for readiness, runtime, serve, or cleanup failure, and 2 for usage or configuration failure. Closed CLI diagnostics include `configuration_invalid`, `gateway_not_ready: run ai-cli-gateway doctor`, `doctor_failed`, and `serve_failed: run ai-cli-gateway doctor`. `doctor` performs no inference and emits deterministic, redacted text or JSON.

## Configuration and providers

Start from `config.example.toml`. It is a Unix/systemd deployment example with generic paths and all normalized defaults. Install and authenticate each official CLI yourself under a dedicated gateway OS user and dedicated config home; set each configured executable to an absolute path.

Provider compatibility guards are compiled in and are not configurable:

| Provider | Pinned range | Adapter status | Live status | Runtime readiness |
|---|---|---|---|---|
| Codex | Codex `>=0.146.0,<0.147.0` | `implemented` | `live-verified`: not run | `not-ready` or ready only as reported by Doctor; initially unassessed |
| Claude | Claude Code `>=2.1.208,<2.2.0` | `implemented` | `live-verified`: not run | `not-ready` or ready only as reported by Doctor; initially unassessed |
| Gemini | Gemini CLI `>=0.53.0,<0.54.0` | `implemented` | `live-verified`: not run | `not-ready` or ready only as reported by Doctor; initially unassessed |

Here, `implemented` means the adapter command/parser and fake integration passed. `live-verified` means a pinned official CLI passed the explicit opt-in inference contract; no such run is claimed above. `not-ready` means operator-specific version, authentication, path, or capability checks fail. Run Doctor on the deployment host to establish readiness. `serve` requires the core checks and at least one ready provider; a zero-ready startup fails closed and releases the runtime root.

Codex uses its prepared dedicated config home and accepts no credential environment relay. Claude can use its dedicated authenticated config home or the explicitly selected `ANTHROPIC_API_KEY` mode. Gemini accepts exactly one of these local credential shapes:

- `GEMINI_API_KEY`;
- `GOOGLE_API_KEY`; or
- the complete Vertex profile: `GOOGLE_APPLICATION_CREDENTIALS`, `GOOGLE_CLOUD_PROJECT`, and `GOOGLE_CLOUD_LOCATION`.

Every Gemini request gets a disposable `GEMINI_CLI_HOME`; cached personal OAuth reuse is unsupported. Naming one of these shapes proves only local configuration acceptance. It does not establish upstream availability, billing tier, quota, entitlement, or live credential validity.

When an exact Unix Node launcher is used, provider children run with the pinned absolute Node interpreter and launcher identities. The child environment is minimal: arbitrary proxy, custom-CA, keyring, shell, and other ambient variables are not inherited. A deployment that needs another value requires a future explicitly validated allowlist.

### Unix Node launchers

An absolute provider executable may resolve to a Node launcher whose first line
is exactly #!/usr/bin/env node with LF or CRLF. At startup, Doctor resolves node
once from the startup PATH, applies the same executable and ancestor safety
checks, and pins the absolute Node and launcher identities. Provider children
still receive a rebuilt safe path; the ambient PATH is not inherited. A missing
or unsafe Node candidate reports `executable_unsafe` before probing.

On Unix, every `config_home` must be an absolute non-symlink directory owned by
the gateway effective user with exact mode `0700`.

### Windows paths

On Windows, use drive-absolute or UNC paths for the executable, config home, external credential file, and runtime root. A native CLI executable uses empty `prefix_args`. A Node-distributed CLI uses an absolute `node.exe` executable and exactly one absolute `.js` or `.mjs` entrypoint in `prefix_args`. The committed example remains the Unix/systemd form.

## Gemini upstream transition boundary

Google announced a Gemini CLI transition on [2026-05-19](https://developers.googleblog.com/an-important-update-transitioning-gemini-cli-to-antigravity-cli/) and then announced the consumer change effective [2026-06-18](https://github.com/google-gemini/gemini-cli/discussions/28017). Its [consumer deprecation notice](https://developers.google.com/gemini-code-assist/docs/deprecations/code-assist-individuals) says Google stopped the consumer Login-with-Google path for Gemini Code Assist for individuals, Google AI Pro, and Google AI Ultra and points those users to Antigravity.

Google says Code Assist Standard and Enterprise plus paid API-key access remain, while the current [API-key and Vertex tiers documentation](https://geminicli.com/docs/resources/quota-and-pricing/) also describes other paid and unpaid quota shapes. Those descriptions are not exhaustive gateway access rules. As of 2026-08-02, actual availability, billing tier, quota, entitlement, and live credential validity are exclusively upstream; provider execution is authoritative. The gateway's `configured`, `implemented`, and readiness states prove local checks only. Antigravity CLI is out of scope.

## Operational defaults

Unless overridden within validated bounds, every provider has concurrency 1, queue 32, queued bytes 16 MiB, queue wait 30 seconds, and execution timeout 300 seconds. Process limits are TERM grace 2 seconds, cleanup 5 seconds, stdout 2 MiB, stderr 256 KiB, and final output 1 MiB. Request limits are HTTP body 1 MiB, input 512 KiB, instructions 256 KiB, and schema 32 KiB.

AI CLI Gateway makes exactly one adapter attempt: there is no gateway retry, no fallback, and no provider switching. A provider CLI may perform provider-internal network retries that the gateway cannot observe or eliminate. Cancellation and the execution deadline bound local duration, but they cannot prove one upstream billable attempt; opt-in inference can incur usage and cost.

The listener accepts loopback literals only and defaults to `127.0.0.1:8080`. Bearer authentication is optional; when enabled, the value is read only from the configured environment name and compared without timing-sensitive string equality. Callers are trusted at the same-OS-user boundary, so a dedicated service OS user is recommended.

Provider binaries are absolute validated paths. Processes are started from argv arrays without a shell, and the prompt is passed through stdin. Each admitted request receives a `0700` temporary runtime and `0600` request files. One process owns the runtime root exclusively, and configuration, aliases, provider readiness, and the key are immutable startup snapshots; there is no hot reload.

The gateway does not issue, discover, extract, copy, parse, refresh, or store login tokens. It only relays an explicitly allowlisted credential value in child memory. It does not log any prompt, output, schema, credentials, raw stdout or stderr, full argv, environment, config path, or authentication identity.

`instructions` is a separately length-framed prompt section. Its priority is provider-dependent; it is not an enforceable OpenAI-style developer-message isolation boundary against adversarial `input`.

### Shutdown and containment

SIGINT or SIGTERM stops admission. The HTTP listener is closed and that closure is observed before Gateway shutdown and scheduler/supervisor drain begins. HTTP handling has a hard HTTP graceful-shutdown period; expiry triggers a bounded force close. The process containment ownership is then drained before exit even when that safety-first drain exceeds the network grace, followed by the final janitor and runtime-root release.

Unix starts each provider in a new process group. A descendant that deliberately calls `setsid`, power loss, or an uncatchable `SIGKILL` of the gateway is outside that Unix guarantee. Windows uses a non-breakaway, kill-on-close Job Object. Under systemd, `KillMode=control-group` adds a service-manager boundary; it does not replace gateway cleanup. A cleanup invariant failure is redacted, makes the provider unavailable, and produces a nonzero result or exit.

## Opt-in live contract tests

Default tests and CI use fake executables. They neither inspect nor invoke an installed provider CLI. The live sources can be compiled without execution with:

```bash
go test -tags=live -run '^$' ./internal/provider/...
```

Live tests are operator-triggered and may incur provider usage and cost. Probe execution first requires `AI_CLI_GATEWAY_LIVE_PROBES` with value `1`. Inference additionally requires `AI_CLI_GATEWAY_LIVE_INFERENCE` with value `1` and exactly the matching provider gate with value `1`: `AI_CLI_GATEWAY_LIVE_CODEX_INFERENCE`, `AI_CLI_GATEWAY_LIVE_CLAUDE_INFERENCE`, or `AI_CLI_GATEWAY_LIVE_GEMINI_INFERENCE`.

Each selected provider also needs its dedicated canary configuration:

- Codex: `AI_CLI_GATEWAY_LIVE_CODEX_EXECUTABLE`, `AI_CLI_GATEWAY_LIVE_CODEX_CONFIG_HOME`, and `AI_CLI_GATEWAY_LIVE_CODEX_MODEL`.
- Claude: `AI_CLI_GATEWAY_LIVE_CLAUDE_EXECUTABLE`, `AI_CLI_GATEWAY_LIVE_CLAUDE_CONFIG_HOME`, `AI_CLI_GATEWAY_LIVE_CLAUDE_MODEL`, and `AI_CLI_GATEWAY_LIVE_CLAUDE_AUTH_MODE=config_home|api_key`.
- Gemini: `AI_CLI_GATEWAY_LIVE_GEMINI_EXECUTABLE`, `AI_CLI_GATEWAY_LIVE_GEMINI_CONFIG_HOME`, `AI_CLI_GATEWAY_LIVE_GEMINI_MODEL`, and `AI_CLI_GATEWAY_LIVE_GEMINI_AUTH_MODE=gemini_api_key|google_api_key|vertex`.

The selected API-key or Vertex mode also requires its corresponding provider environment values outside the repository. Use a dedicated disposable canary; the harness redacts failures and cleans up even when a check fails. Live verification has not been run for this README and remains `not run`.

GitHub Actions uses Node24-based official actions. GitHub-hosted runners meet that runtime automatically; a self-hosted runner needs `actions/runner` v2.327.1 or later.

## Security and terms

See [SECURITY.md](SECURITY.md) for private vulnerability reporting. The gateway reduces accidental exposure and owns child cleanup, but it is not an isolation boundary between mutually untrusted users sharing an OS account.

You are responsible for installing and authenticating each provider CLI and for using it in accordance with its applicable terms.

## Official contract sources

The 2026-07-30 implementation baseline was checked against these official contracts, with the dated Gemini transition rechecked on 2026-08-02:

- OpenAI: [create a response](https://developers.openai.com/api/reference/resources/responses/methods/create), [text generation](https://developers.openai.com/api/docs/guides/text), [Structured Outputs](https://developers.openai.com/api/docs/guides/structured-outputs), and [list models](https://developers.openai.com/api/reference/resources/models/methods/list).
- Codex: [non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode), [`codex exec`](https://learn.chatgpt.com/docs/developer-commands?surface=cli#cli-codex-exec), and [authentication](https://learn.chatgpt.com/docs/auth).
- Claude Code: [headless mode](https://code.claude.com/docs/en/headless), [CLI reference](https://code.claude.com/docs/en/cli-usage), [result types](https://code.claude.com/docs/en/agent-sdk/typescript), [environment variables](https://code.claude.com/docs/en/env-vars), [authentication](https://code.claude.com/docs/en/authentication), and [changelog](https://code.claude.com/docs/en/changelog).
- Gemini CLI: [headless mode](https://geminicli.com/docs/cli/headless/), [CLI reference](https://geminicli.com/docs/cli/cli-reference/), [configuration](https://geminicli.com/docs/reference/configuration/), [authentication](https://geminicli.com/docs/get-started/authentication/), and [session management](https://geminicli.com/docs/cli/session-management/).
