# API and Operations Reference

Detailed contracts and operating boundaries for AI CLI Gateway v0.1.0. For installation and a first request, start with [Getting Started](getting-started.md).

## Architecture and endpoint scope

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

The top-level request subset accepts these fields:

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

## Portable JSON Schema profile

A `json_schema` request has an object root and may use the seven types `object`, `array`, `string`, `number`, `integer`, `boolean`, and `null`. The closed keyword set is:

- `type`, `properties`, `required`, `additionalProperties`, and `items`;
- `enum` and `const`;
- `minLength`, `maxLength`, `minItems`, `maxItems`, `minProperties`, and `maxProperties`;
- `minimum`, `maximum`, `exclusiveMinimum`, and `exclusiveMaximum`; and
- `description` and `title`.

Every object schema must use `additionalProperties:false`, and every property must appear in `required`. There are no references, unions or combinators, patterns, formats, remote resolution, or Markdown-fence extraction. A provider must return exactly one JSON object that is duplicate-free. AI CLI Gateway validates it locally and performs no repair, fallback, or retry.

Structured JSON remains a validated string in `output_text.text`; AI CLI Gateway does not invent an `output_json` field.

## Requests and responses

The optional gateway key is configured with exactly one source: the backward-compatible environment variable named by `server.api_key_env`, or the absolute key-file path named by `server.api_key_file`. Omit both to disable Bearer authentication. Put request data in a file so prompts and keys do not become command-line arguments.

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

Command grammar:

```text
usage:
  ai-cli-gateway version
  ai-cli-gateway init [OPTIONS]
  ai-cli-gateway serve [--config PATH]
  ai-cli-gateway doctor [--config PATH] [--json]
```

Both JSON Doctor orders are accepted: `ai-cli-gateway doctor --config PATH --json` and `ai-cli-gateway doctor --json --config PATH`. The equals-sign form is intentionally not part of the grammar.

### Non-interactive initialization

This slice supports the strict non-interactive form. Bare interactive `ai-cli-gateway init` is reserved for the next guided-init slice and currently exits 2 with instructions to pass `--non-interactive` and every required value. Non-interactive init never reads stdin, searches `PATH`, guesses a provider home, runs a login command, starts a listener, or performs inference.

Select one or more providers with repeatable `--provider codex|claude|gemini`. A provider that is not already complete in the selected configuration needs its absolute executable and config home, plus at least one model mapping:

```text
--codex-executable PATH       --codex-config-home PATH
--codex-model ALIAS=PROVIDER_MODEL

--claude-executable PATH      --claude-config-home PATH
--claude-model ALIAS=PROVIDER_MODEL
--claude-auth config-home|anthropic-api-key

--gemini-executable PATH      --gemini-config-home PATH
--gemini-model ALIAS=PROVIDER_MODEL
--gemini-auth gemini-api-key|google-api-key|vertex-service-account
```

Each `--*-model` option is repeatable. On Windows, a Node-distributed provider also uses the matching `--codex-entrypoint`, `--claude-entrypoint`, or `--gemini-entrypoint` with an absolute `.js` or `.mjs` path. Native executables do not use an entrypoint.

Gateway authentication is selected with one of these exact forms:

```text
--gateway-auth file [--gateway-key-file ABSOLUTE_PATH]
--gateway-auth environment --gateway-key-env ENVIRONMENT_NAME
--gateway-auth none
```

A fresh configuration defaults to file authentication and a private `gateway.key` beside the configuration. Init generates a missing key without printing it. Its completion output is authentication-aware: file mode shows commands that load the private key for a client, environment mode maps the configured environment variable without reading or printing its value, and `none` mode shows requests without an `Authorization` header. An explicitly selected valid key file is reused; an unapproved orphan at the implicit path is not reused in non-interactive mode.

Existing TOML is merged without rewriting unrelated text. Omitted values for an already configured selected provider are preserved. Changing an existing provider requires the matching repeatable `--replace-provider NAME`; changing an existing model alias requires `--replace-model ALIAS`. Without that explicit authority, init prints the redacted semantic diff and exits 2 without mutation. `--dry-run` performs configuration validation and read-only filesystem preflight, prints the same redacted diff, and states that no files changed and post-write Doctor was not run.

After a successful commit or semantic no-op, init runs the no-inference Doctor checks and prints the complete report. Readiness is determined by core checks and the providers selected in this invocation; an unselected, pre-existing provider remains visible in the report but does not change the init result.

Init exit codes are:

| Exit | Meaning |
|---:|---|
| 0 | selected providers ready, dry run complete, or a future interactive confirmation declined |
| 1 | saved but not ready, operational failure, indeterminate state, or backup recovery required |
| 2 | invalid/incomplete input, invalid existing configuration, or an unapproved replacement/key reuse |
| 130 | canceled before commit, or canceled after a commit that was already saved |

When `--config` is omitted, POSIX uses `$XDG_CONFIG_HOME/ai-cli-gateway/config.toml` when `XDG_CONFIG_HOME` is an absolute nonempty path; otherwise it uses `$HOME/.config/ai-cli-gateway/config.toml`. Windows uses `%LOCALAPPDATA%\AI CLI Gateway\config\config.toml`. If a safe default path is unavailable, the command exits 2 and writes `default_config_path_unavailable: pass --config PATH`; pass an explicit `--config PATH` to continue.

Help is available as `ai-cli-gateway --help`, `ai-cli-gateway version --help`, `ai-cli-gateway init --help`, `ai-cli-gateway serve --help`, and `ai-cli-gateway doctor --help`.

The exit status is 0 for success or a clean handled shutdown, 1 for readiness, runtime, serve, or cleanup failure, and 2 for usage or configuration failure. Stable CLI diagnostics include `configuration_invalid`, `default_config_path_unavailable: pass --config PATH`, `gateway_not_ready: run ai-cli-gateway doctor`, `doctor_failed`, and `serve_failed: run ai-cli-gateway doctor`. `doctor` performs no inference and emits redacted text or JSON.

## Configuration and providers

Start from [`config.example.toml`](../config.example.toml). It is a Unix/systemd deployment example with generic paths and normalized defaults. Install and authenticate each official CLI under a dedicated gateway OS user and config home, and configure every executable with an absolute path.

Provider compatibility guards are compiled in:

| Provider | Supported CLI range |
|---|---|
| Codex | Codex `>=0.146.0,<0.147.0` |
| Claude | Claude Code `>=2.1.208,<2.2.0` |
| Gemini | Gemini CLI `>=0.53.0,<0.54.0` |

Run `ai-cli-gateway doctor --config PATH` on the deployment host to check binary identity, version, authentication readiness, capabilities, and containment. `serve` requires the core checks and at least one ready provider.

Codex uses its prepared config home and accepts no credential environment relay. Claude can use its authenticated config home or the explicitly selected `ANTHROPIC_API_KEY` mode. Gemini accepts exactly one of these local credential shapes:

- `GEMINI_API_KEY`;
- `GOOGLE_API_KEY`; or
- the complete Vertex profile: `GOOGLE_APPLICATION_CREDENTIALS`, `GOOGLE_CLOUD_PROJECT`, and `GOOGLE_CLOUD_LOCATION`.

Every Gemini request gets a disposable `GEMINI_CLI_HOME`; cached personal OAuth reuse is unsupported. Configuration acceptance does not establish provider availability, quota, billing, entitlement, or credential validity.

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

## Operational defaults

Unless overridden within validated bounds, every provider has concurrency 1, queue 32, queued bytes 16 MiB, queue wait 30 seconds, and execution timeout 300 seconds. Process limits are TERM grace 2 seconds, cleanup 5 seconds, stdout 2 MiB, stderr 256 KiB, and final output 1 MiB. Request limits are HTTP body 1 MiB, input 512 KiB, instructions 256 KiB, and schema 32 KiB.

## Shutdown and containment

SIGINT or SIGTERM stops admission. The HTTP listener is closed and that closure is observed before Gateway shutdown and scheduler/supervisor drain begins. HTTP handling has a hard HTTP graceful-shutdown period; expiry triggers a bounded force close. The process containment ownership is then drained before exit even when that safety-first drain exceeds the network grace, followed by the final janitor and runtime-root release.

Unix starts each provider in a new process group. A descendant that deliberately calls `setsid`, power loss, or an uncatchable `SIGKILL` of the gateway is outside that Unix guarantee. Windows uses a non-breakaway, kill-on-close Job Object. Under systemd, `KillMode=control-group` adds a service-manager boundary; it does not replace gateway cleanup. A cleanup invariant failure is redacted, makes the provider unavailable, and produces a nonzero result or exit.

## Security details

AI CLI Gateway makes one adapter attempt: there is no gateway retry, fallback, or provider switching. A provider CLI may perform provider-internal network retries that the gateway cannot observe or eliminate. A provider request may incur provider usage and cost.

The listener accepts loopback literals only and defaults to `127.0.0.1:8080`. Bearer authentication is optional. Configure either `server.api_key_env` (including the compatible explicit empty value) or `server.api_key_file`, never both. The configuration parser recognizes drive-absolute and UNC forms as absolute on Windows, but runtime loading of `server.api_key_file` requires a drive-qualified, drive-absolute path on a fixed local drive; UNC, network, mapped, removable, and reparse locations are rejected. When enabled, the value is read from the configured source and compared without timing-sensitive string equality. Callers are trusted at the same-OS-user boundary, so a dedicated service OS user is recommended.

Provider binaries are absolute validated paths. Processes are started from argv arrays without a shell, and the prompt is passed through stdin. Each admitted request receives a `0700` temporary runtime and `0600` request files. One process owns the runtime root exclusively, and configuration, aliases, provider readiness, and the key are immutable startup snapshots; there is no hot reload.

On Windows, the retained configuration handle denies in-place content writes until shutdown. Stop the gateway before editing the file in place, then restart it. Atomic replacement can still succeed, but the running process keeps its original startup snapshot and does not hot-reload the replacement.

The gateway does not issue, discover, extract, copy, parse, refresh, or store login tokens. It only relays an explicitly allowlisted credential value in child memory. It does not log any prompt, output, schema, credentials, raw stdout or stderr, full argv, environment, config path, or authentication identity.

`instructions` is a separately length-framed prompt section. Its priority is provider-dependent; it is not an enforceable OpenAI-style developer-message isolation boundary against adversarial `input`.

See [SECURITY.md](../SECURITY.md) for private vulnerability reporting. The gateway reduces accidental exposure and owns child cleanup, but it is not an isolation boundary between mutually untrusted users sharing one OS account.

You are responsible for installing and authenticating each provider CLI and for using it in accordance with its applicable terms.

## Official contract sources

The implementation follows these upstream contracts:

- OpenAI: [create a response](https://developers.openai.com/api/reference/resources/responses/methods/create), [text generation](https://developers.openai.com/api/docs/guides/text), [Structured Outputs](https://developers.openai.com/api/docs/guides/structured-outputs), and [list models](https://developers.openai.com/api/reference/resources/models/methods/list).
- Codex: [non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode), [`codex exec`](https://learn.chatgpt.com/docs/developer-commands?surface=cli#cli-codex-exec), and [authentication](https://learn.chatgpt.com/docs/auth).
- Claude Code: [headless mode](https://code.claude.com/docs/en/headless), [CLI reference](https://code.claude.com/docs/en/cli-usage), [result types](https://code.claude.com/docs/en/agent-sdk/typescript), [environment variables](https://code.claude.com/docs/en/env-vars), [authentication](https://code.claude.com/docs/en/authentication), and [changelog](https://code.claude.com/docs/en/changelog).
- Gemini CLI: [headless mode](https://geminicli.com/docs/cli/headless/), [CLI reference](https://geminicli.com/docs/cli/cli-reference/), [configuration](https://geminicli.com/docs/reference/configuration/), [authentication](https://geminicli.com/docs/get-started/authentication/), and [session management](https://geminicli.com/docs/cli/session-management/).
