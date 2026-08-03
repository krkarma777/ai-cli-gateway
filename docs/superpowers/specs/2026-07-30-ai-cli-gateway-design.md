# AI CLI Gateway MVP Design

Status: approved in conversation on 2026-07-30
Target repository: `ai-cli-gateway`
Target local path: `~/Dev/ai-cli-gateway`
Implementation language: Go
License: Apache-2.0

## 1. Summary

AI CLI Gateway is a small local/self-hosted HTTP gateway that runs AI command-line
clients already installed and authenticated by the user.

> AI CLI Gateway turns locally authenticated AI CLIs into an OpenAI
> Responses-compatible API.

The compatibility claim is deliberately narrow: this project implements a
**Responses API-compatible subset**, not the complete OpenAI API.

The MVP accepts a final-output request, selects a configured model alias, runs one
of Codex CLI, Claude Code, or Gemini CLI, validates the final output, and returns a
non-streaming Responses-shaped response. The gateway does not broker provider
credentials and does not persist conversations.

Here, "locally authenticated" is provider-neutral shorthand, not a claim about
upstream availability, billing tier, quota, entitlement, or live credential
validity. For Gemini, the gateway accepts exactly the three environment/external
credential shapes in Section 7.4, runs with a disposable home, and never reuses
cached personal OAuth. Google stopped serving the consumer Login-with-Google path
for Gemini Code Assist for individuals, Google AI Pro, and Google AI Ultra on
2026-06-18 and points those users to Antigravity. Google says Code Assist Standard
and Enterprise plus paid API-key access remain, while its current documentation
also describes API-key and Vertex tiers. Actual access is decided exclusively by
the upstream provider, and Antigravity CLI is outside this MVP.

## 2. Goals and Non-goals

### 2.1 Goals

- Implement `POST /v1/responses`.
- Implement `GET /v1/models`.
- Remain a standalone project with no source or runtime dependency on
  CLIProxyAPI, Voice to Minutes, or another gateway.
- Support final plain text and a portable JSON Schema output subset.
- Support configurable, case-sensitive model aliases.
- Keep provider-specific behavior behind a small adapter interface.
- Run every provider without a shell, with an explicit executable and argv list.
- Send all prompt content through stdin, never through command-line arguments.
- Bound requests, queues, subprocess concurrency, execution time, and output.
- Propagate client cancellation and guarantee cleanup of the process containment
  owned by a request for cancellation, timeout, output overflow, and graceful
  server shutdown.
- Make real-provider adapters testable with a cross-platform fake CLI.
- Diagnose executable presence, version compatibility, configured authentication,
  and required CLI capabilities without making a paid inference request.
- Avoid logging prompts, model output, schemas, credentials, raw provider stderr,
  or provider session identifiers.
- Ship as one gateway executable per OS/architecture.

### 2.2 Non-goals

- Complete OpenAI Responses API compatibility.
- SSE or any other streaming response.
- Function calls, tool-call exchange, or client-side tool execution.
- Images, files, audio, or other multimodal inputs.
- Multi-turn state, `previous_response_id`, Conversations API, or response lookup.
- Background responses, response cancellation endpoints, or idempotency storage.
- Automatic retry, schema repair, or provider fallback.
- Web UI, external database, or gateway-managed account system.
- Issuing, extracting, copying, refreshing, or storing provider login tokens.
- Acting as an OS sandbox. Provider CLIs retain the permissions of the OS account
  that runs the gateway.

Provider CLIs may maintain their own authentication or operational state. The
gateway prevents request conversation persistence using documented CLI controls
where available and a disposable runtime home for Gemini. It does not modify or
copy the user's authentication files.

## 3. Technology Decision

### 3.1 Recommendation

Use Go 1.26.x.

Go best matches this daemon's dominant concerns: a small native HTTP server,
bounded concurrency, context cancellation, subprocess I/O, OS-specific process
control, low dependency count, and one executable per release target.

The implementation should primarily use:

- `net/http` for the server;
- `context` for request and shutdown cancellation;
- `os/exec` plus platform-specific launch code for subprocesses;
- goroutines and bounded channels/queues for concurrent I/O and admission;
- `log/slog` for metadata-only structured logging;
- `crypto/rand` for request and response IDs;
- `golang.org/x/sys` for Unix and Windows process primitives;
- a TOML decoder for configuration;
- an in-process JSON Schema validator, preceded by the gateway's own keyword
  allowlist and JSON safety checks.

### 3.2 Compared Alternatives

| Criterion | Go | TypeScript/Node | Rust |
|---|---|---|---|
| Single executable | Straightforward | Node SEA remains a more complex packaging surface | Excellent |
| Subprocess control | Good, with a required OS-specific layer | Core APIs are usable; Windows tree control needs native help | Excellent, but more implementation surface |
| Bounded concurrency | Simple goroutines and queues | Straightforward, but cancellation plumbing is more manual | Strong with async runtime dependencies |
| HTTP server | Strong standard library | Strong | Requires a larger framework/dependency set |
| Contributor maintenance | Lowest for this scope | Fast iteration, higher packaging/runtime burden | Highest learning and implementation cost |

Rust becomes attractive if a later release evolves into a hardened resource
accounting daemon. Node is attractive for an internal prototype when single-file
native distribution is not important.

Java is viable, especially for a JVM-oriented team. `ProcessBuilder`,
`ProcessHandle`, and virtual threads fit the workload. It is not the first choice
here because `jpackage` produces a self-contained application bundle rather than
the simplest single file, GraalVM Native Image introduces closed-world build and
metadata constraints, and race-free Unix process-group/Windows Job Object control
still needs a native layer. Java does not create a material advantage for this
small dependency-light gateway.

### 3.3 Name

The public project name is **AI CLI Gateway** and the repository name is
`ai-cli-gateway`. Exact-name checks on 2026-07-30 found no matching GitHub
repository, npm package, or crates.io crate. This is a practical collision check,
not legal trademark clearance.

## 4. Contract Sources and Compatibility Baseline

This design was checked against current official documentation on 2026-07-30.
The implementation and README must link to these sources and date any compatibility
statements.

### 4.1 OpenAI Contract Sources

- [Create a model response](https://developers.openai.com/api/reference/resources/responses/methods/create)
- [Text generation with the Responses API](https://developers.openai.com/api/docs/guides/text)
- [Structured model outputs](https://developers.openai.com/api/docs/guides/structured-outputs)
- [List models](https://developers.openai.com/api/reference/resources/models/methods/list)

The official contract allows much more than this project. Unsupported official
fields are rejected rather than silently ignored.

### 4.2 Provider Contract Sources

- Codex:
  [non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode),
  [`codex exec`](https://learn.chatgpt.com/docs/developer-commands?surface=cli#cli-codex-exec),
  [authentication](https://learn.chatgpt.com/docs/auth)
- Claude Code:
  [headless mode](https://code.claude.com/docs/en/headless),
  [CLI reference](https://code.claude.com/docs/en/cli-usage),
  [Agent SDK result types](https://code.claude.com/docs/en/agent-sdk/typescript),
  [environment variables](https://code.claude.com/docs/en/env-vars),
  [authentication](https://code.claude.com/docs/en/authentication),
  [changelog](https://code.claude.com/docs/en/changelog)
- Gemini CLI:
  [headless mode](https://geminicli.com/docs/cli/headless/),
  [CLI reference](https://geminicli.com/docs/cli/cli-reference/),
  [configuration](https://geminicli.com/docs/reference/configuration/),
  [authentication](https://geminicli.com/docs/get-started/authentication/),
  [session management](https://geminicli.com/docs/cli/session-management/),
  [2026-05-19 transition announcement](https://developers.googleblog.com/an-important-update-transitioning-gemini-cli-to-antigravity-cli/),
  [2026-06-18 individual-account shutdown announcement](https://github.com/google-gemini/gemini-cli/discussions/28017),
  [consumer Login-with-Google deprecation notice](https://developers.google.com/gemini-code-assist/docs/deprecations/code-assist-individuals),
  and current
  [quota and pricing documentation](https://geminicli.com/docs/resources/quota-and-pricing/)

### 4.3 Locally Inspected Baseline

The installed executables and their `--help` output were inspected without making
a model request.

| Provider | Installed executable | Installed version | Current version checked |
|---|---|---:|---:|
| Codex | `/Users/krkarma777/.npm-global/bin/codex` | `0.146.0` | `0.146.0` |
| Claude Code | `/Users/krkarma777/.npm-global/bin/claude` | `2.1.169` | `2.1.220` |
| Gemini CLI | `/opt/homebrew/bin/gemini` | `0.27.3` | `0.53.0` |

Initial tested adapter ranges are:

- Codex: `>=0.146.0, <0.147.0`
- Claude Code: `>=2.1.208, <2.2.0`
- Gemini CLI: `>=0.53.0, <0.54.0`

The local Claude and Gemini installations therefore require an upgrade before
live-adapter validation. Fake CLI tests do not require any provider installation.
Versions outside the tested range are `not_ready` by default. A future project
release can widen the range after contract tests pass; the MVP has no unsafe
"ignore version" switch.

These inspected versions and adapter ranges are retained compatibility provenance;
they do not establish current upstream account entitlement or live credential
validity.

## 5. Architecture

```text
Client
  -> HTTP ingress and optional Bearer authentication
  -> Strict request decoder
  -> Model alias registry
  -> Per-provider admission scheduler
  -> Provider adapter
  -> Process supervisor
  -> Codex / Claude Code / Gemini CLI
  <- Provider envelope parser
  <- Exact JSON parser and JSON Schema validator
  <- Responses-compatible encoder
```

### 5.1 Components

| Component | Sole responsibility |
|---|---|
| `httpapi` | Listener safety, authentication, HTTP limits, strict request decoding, response encoding |
| `core` | Provider-neutral request/result/error types and immutable model registry |
| `scheduler` | Provider-specific FIFO queue, concurrency permits, cancellation, shutdown |
| `provider` | Adapter interface and Codex, Claude, Gemini, and fake implementations |
| `process` | Temporary runtime, stdin/stdout/stderr, timeout, process-tree termination, reap |
| `schema` | Schema-profile validation, safe JSON parsing, local output validation |
| `doctor` | Executable, version, authentication, capability, and containment diagnostics |
| `config` | Startup-only TOML parsing and fail-closed semantic validation |
| `telemetry` | Metadata-only structured logs and counters |

The HTTP layer never constructs CLI argv. Adapters never own process lifecycle.
The process supervisor never interprets provider output. This separation keeps
provider changes from weakening cancellation, queue, or logging guarantees.

### 5.2 Core Adapter Contract

Conceptually, each adapter exposes:

```text
Name() provider identifier
Probe(context, provider config) -> redacted provider health
Build(normalized request, trusted model config, runtime paths) -> command spec
Parse(process result, requested format) -> final bytes or stable provider error
```

A command spec contains only:

- a validated absolute executable;
- a fixed argv list plus trusted configured model identifier;
- an explicit minimal environment;
- a request-specific working directory;
- stdin bytes;
- optional request-local files such as a Codex schema file.

It never contains a shell string. Client-supplied values cannot become an
executable, option name, path, environment-variable name, or model argv directly.
The client model is resolved through the immutable alias registry first.

## 6. HTTP API Contract

### 6.1 General Rules

- Listen on `127.0.0.1:8080` by default.
- Only literal loopback listener addresses are accepted in the MVP.
- `POST /v1/responses` requires `application/json`; `charset=utf-8` is accepted.
- Every response produced after the application handler is invoked returns
  `application/json`.
- The optional gateway Bearer key protects both endpoints.
- Every application response includes a gateway-generated `X-Request-ID`.
- Malformed connections, header-read timeouts, and headers rejected by
  `net/http` before handler dispatch are transport failures outside the JSON API
  envelope; the gateway still bounds them with server timeouts and header limits.
- Unknown query parameters are rejected.
- Wrong methods return `405`.
- Exact paths are used; implicit trailing-slash redirects are disabled.
- Request `Content-Encoding` must be absent or `identity`.
- The server uses a 5-second header timeout, 15-second body-read timeout,
  60-second idle timeout, and 16 KiB maximum header size.
- A global gate permits at most 128 live request handlers and 32 concurrent body
  reads before provider routing. Requests rejected by this gate never allocate a
  provider queue entry.

### 6.2 `POST /v1/responses`

#### Supported top-level fields

| Field | Contract |
|---|---|
| `model` | Required, non-empty, case-sensitive configured alias |
| `input` | Required, non-empty UTF-8 string |
| `instructions` | Optional UTF-8 string or `null` |
| `text` | Optional object containing only `format` |
| `stream` | Optional; only `false` is accepted |
| `store` | Optional; only `false` is accepted |
| `tools` | Optional; only an empty array is accepted |
| `tool_choice` | Optional; only `"none"` is accepted |

If `text` is present, `format` is required. If `text` is absent, the format
defaults to `{"type":"text"}`.

`text.format` is either:

```json
{"type":"text"}
```

or:

```json
{
  "type": "json_schema",
  "name": "result",
  "description": "Optional output description",
  "strict": true,
  "schema": {
    "type": "object",
    "properties": {},
    "required": [],
    "additionalProperties": false
  }
}
```

For `json_schema`:

- `type`, `name`, `strict`, and `schema` are required.
- `description` is optional.
- `strict` must be exactly `true`.
- `name` is 1-64 ASCII letters, digits, underscores, or hyphens.
- No other format fields are accepted.

#### Explicitly unsupported

The following are examples, not an exhaustive fallback list; the decoder is a
closed allowlist.

- input item arrays, roles, images, files, or content parts;
- `stream:true` and `stream_options`;
- non-empty `tools` or any functional `tool_choice`;
- `previous_response_id`, `conversation`, `background`, and `prompt`;
- `metadata`, `include`, `reasoning`, and service-tier options;
- sampling and generation controls such as `temperature`, `top_p`,
  `max_output_tokens`, and `truncation`;
- provider-specific options.

An unsupported or unknown field is a `400 unsupported_parameter`, not a no-op.

#### Strict JSON decoding

Before typed decoding, the gateway validates the complete body:

- valid UTF-8 only;
- no UTF-8 BOM or NUL;
- exactly one JSON value followed by EOF;
- root value must be an object;
- duplicate keys are rejected at every nesting level;
- nesting and token lengths are bounded;
- unknown fields are rejected at every gateway-defined object level.

The body limit is applied before parsing. Parse errors never include request
fragments in logs or client messages.

### 6.3 Portable JSON Schema Profile

The profile is intentionally smaller than the OpenAI Structured Outputs schema
surface so all three providers share one predictable contract.

Rules:

- The root schema must have `type: "object"`.
- `type` must be one string, not a union array.
- Supported types are `object`, `array`, `string`, `number`, `integer`,
  `boolean`, and `null`.
- Supported structural keywords are `type`, `properties`, `required`,
  `additionalProperties`, and `items`.
- Supported value keywords are `enum` and `const`.
- Supported bounds are `minLength`, `maxLength`, `minItems`, `maxItems`,
  `minProperties`, `maxProperties`, `minimum`, `maximum`,
  `exclusiveMinimum`, and `exclusiveMaximum`.
- `description` and `title` are accepted as annotations.
- Every object must use `additionalProperties:false`.
- Every property of an object must appear in that object's `required` array.
- `items` must be one schema; tuple validation is unsupported.

All unlisted keywords are rejected, including `$ref`, `$dynamicRef`, `$defs`,
`$id`, remote or file references, `anyOf`, `oneOf`, `allOf`, `not`,
conditionals, unevaluated keywords, `pattern`, `patternProperties`, `format`,
and `uniqueItems`. The validator never performs network or filesystem schema
resolution.

Limits:

| Limit | Default |
|---|---:|
| Encoded schema size | 32 KiB |
| Schema nodes | 512 |
| Schema nesting depth | 32 |
| Properties in one object | 100 |
| Entries in one enum | 256 |
| Output JSON nesting depth | 128 |
| One numeric token | 128 bytes |
| Validated final output | 1 MiB |

Provider-native structured output is an aid, never the trust boundary. The
gateway always requires exactly one JSON object followed by EOF, rejects duplicate
keys, preserves number precision for validation, and validates locally. It never
extracts JSON from Markdown fences or surrounding prose.

There is no automatic repair or retry. A provider output that does not conform
returns `502 structured_output_invalid`.

### 6.4 Prompt Semantics

All `instructions` and `input` content is sent through stdin. Schema content is
never placed directly in argv: it is either included in the stdin output-contract
section or written to a request-local file whose trusted path is passed to Codex.

The gateway builds a deterministic, length-delimited prompt envelope with separate
instruction, user input, and, when required by the adapter, output-contract
sections. Provider adapters may add a fixed gateway-owned preamble but may not
interpolate prompt content into argv.

Because these CLIs generally expose one non-interactive prompt channel,
`instructions` is represented as a separately labeled instruction section. Its
priority is provider-dependent and is not enforceable against adversarial text in
`input`; it does not provide the isolation semantics of an OpenAI developer
message. Length framing prevents structural ambiguity in the gateway protocol, not
prompt injection. This is part of the documented compatibility boundary.

Codex may receive the JSON Schema through a request-local `0600` file referenced
by `--output-schema`; the file contains only the schema, never instructions,
input, or credentials, and is deleted with the request directory. Claude and
Gemini receive their output contract through stdin and are locally validated,
avoiding schema content in the OS process list.

### 6.5 Successful Response

Plain text and structured JSON use the same Responses-shaped message. Structured
JSON remains a validated JSON string in `output_text.text`; the project does not
invent an `output_json` field.

```json
{
  "id": "resp_01...",
  "object": "response",
  "created_at": 1785369600,
  "completed_at": 1785369601,
  "status": "completed",
  "background": false,
  "error": null,
  "incomplete_details": null,
  "instructions": null,
  "model": "codex-default",
  "output": [
    {
      "id": "msg_01...",
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

- `instructions` echoes the accepted request value or `null`.
- `model` echoes the client alias, not the provider's internal model slug.
- `text.format` echoes the accepted format, including `json_schema` when used.
- IDs are random opaque identifiers generated for this response only.
- IDs do not imply that the response can later be retrieved.
- Provider session IDs and provider envelopes are discarded.
- `usage` is omitted unless equivalent, trustworthy provider usage becomes
  available for every adapter. It is never fabricated as zero.
- Failure responses use the HTTP error envelope, not a `status:"failed"` response
  object.

### 6.6 `GET /v1/models`

The endpoint returns a sorted immutable snapshot of configured aliases and never
runs a provider CLI:

```json
{
  "object": "list",
  "data": [
    {
      "id": "codex-default",
      "object": "model",
      "created": 0,
      "owned_by": "local"
    }
  ]
}
```

`created` is an optional configured Unix timestamp and defaults to the stable value
`0`; it is never the current request time. Provider health does not make the list
flap. A listed model whose provider is unavailable returns
`503 provider_not_ready` when executed.

Doctor constructs the immutable `core.Registry` once from the normalized
configuration and transfers that same registry into application assembly. Its
model snapshot always contains every configured alias, including aliases backed
by a provider that is not ready or whose executable could not be resolved safely.

## 7. Provider Adapters

### 7.1 Shared Rules

- Executable paths are absolute after symlink resolution.
- Executables must exist, be regular files, and not be writable by untrusted
  users according to platform checks.
- Doctor validates containment and every provider path before calling an adapter.
  Each adapter then runs only its version command first. An unreadable or
  unsupported version returns immediately without version-specific help,
  feature, doctor, or authentication commands.
- Windows `.cmd` and `.bat` shims are rejected because they require a shell.
  Node-based CLIs on Windows must be configured as an absolute `node.exe` plus a
  trusted script path in provider `prefix_args`.
- The configured provider model is appended from trusted configuration only.
- `stdout` and `stderr` are read concurrently into separate bounded buffers.
- Exit code zero is insufficient; each adapter must validate its documented
  envelope and final-output cardinality.
- Provider stderr is never returned, logged, or used as a locale-dependent public
  protocol. It is held only in a bounded in-memory buffer until the process result
  is classified, then discarded.
- Locale-dependent stderr strings are not a stable protocol. Unknown failures map
  to `provider_failed`.
- Provider-internal tools, extensions, MCP servers, hooks, and interactive
  approvals are disabled as far as the documented CLI supports. This reduces
  capability but is not advertised as a security sandbox.

The API contract excludes client-supplied tools and tool-call round trips. That is
distinct from proving that an agent CLI has no internal capability. AI CLI Gateway
assumes loopback/API-key callers are trusted at the same OS-user boundary as the
gateway. It must not be exposed to untrusted callers as a privilege boundary.
Readiness verifies that the pinned adapter's documented suppression controls are
accepted, but managed provider or machine policy remains outside the gateway's
control. The README recommends a dedicated restricted OS account for stronger
isolation.

### 7.2 Codex

Baseline command shape:

```text
codex
  --ask-for-approval never
  exec
  --ephemeral
  --ignore-user-config
  --ignore-rules
  --strict-config
  --sandbox read-only
  --skip-git-repo-check
  --color never
  --model <trusted-provider-model>
  -
```

For pinned Codex CLI `0.146.x`, `--ask-for-approval` is a global option and
must precede `exec`. The installed `0.146.0` parser rejects
`codex exec --ask-for-approval never`; readiness probes the accepted global
placement before the adapter is marked ready.

The adapter also applies the pinned version's documented feature disables for
shell execution, web search, plugins, hooks, browser/computer use, subagents, and
other non-final-output capabilities. Startup capability probing fails closed if
the required controls are unavailable.

For the initial Codex range, the required hardening set is:

```text
--disable shell_tool
--disable unified_exec
--disable code_mode_host
--disable apps
--disable plugins
--disable remote_plugin
--disable hooks
--disable multi_agent
--disable browser_use
--disable browser_use_external
--disable computer_use
--disable in_app_browser
--disable image_generation
--disable skill_search
--disable skill_mcp_dependency_install
--disable workspace_dependencies
-c web_search="disabled"
```

- Prompt: stdin through the final `-`.
- Text result: final stdout.
- JSON Schema: request-local schema path via `--output-schema`, followed by local
  validation.
- Session control: `--ephemeral`.
- Config/auth root: configured `CODEX_HOME`, created and authenticated by the user.
- Credential environment: unsupported for Codex in the MVP. Readiness requires
  the official `login status` proof against the dedicated `CODEX_HOME`; the
  gateway does not offer one-off API-key override semantics.
- Health: `codex --version`, `codex login status`, and filtered
  `codex doctor --json`. Raw doctor output is not logged.

Codex does not expose a universal, version-stable "remove every internal tool"
switch. It runs in a clean empty working directory, with read-only/never-approve
settings and pinned feature controls. Documentation must state that the gateway
process account remains the ultimate filesystem/network boundary.

The individual hardening feature names and the `doctor --json` field layout are
observed contracts pinned to tested Codex `0.146.x`, not universal stable CLI
contracts. Official documentation guarantees `features list` and a redacted
machine-readable doctor report but does not publish every feature name or the
complete doctor JSON schema. Readiness therefore version-gates and fail-closes
on missing observed controls, while retaining only a small fixed doctor-check
allowlist.

### 7.3 Claude Code

Baseline command shape:

```text
claude
  -p
  --output-format json
  --no-session-persistence
  --safe-mode
  --setting-sources ""
  --tools ""
  --strict-mcp-config
  --permission-mode dontAsk
  --disable-slash-commands
  --no-chrome
  --model <trusted-provider-model>
```

- Prompt: stdin.
- Text result: `.result` from the single JSON envelope.
- JSON Schema: output contract in stdin; parse `.result` as exact JSON and validate
  locally.
- Session control: `--no-session-persistence`.
- Config/auth root: configured `CLAUDE_CONFIG_DIR`, authenticated by the user.
- Environment: request-local HOME/temp paths plus `CLAUDE_CODE_TMPDIR`,
  prompt-history/nonessential-traffic/marketplace/terminal-title suppression,
  and only an explicitly configured `ANTHROPIC_API_KEY` when selected.
- Health: `claude --version`, a fail-closed help-token probe, and
  `claude auth status` exit code only. The provider-level health interface has
  no routed model, so the non-executing help probe supplies Anthropic's
  documented `sonnet` model alias solely to exercise the complete argv parser;
  request execution always uses the routed provider model. Auth stdout/stderr
  is discarded because its JSON fields are not a documented compatibility
  contract.

The native `--json-schema` argument is not used in the MVP because it places the
inline schema in the process list and older versions could silently ignore invalid
schemas. The gateway's local validator is always authoritative.

Claude's documented piped-stdin limit is 10 MB but the documentation does not
define whether that label is decimal or binary. The adapter therefore uses the
conservative decimal limit of exactly `10,000,000` bytes for the fully framed
prompt and rejects one byte more before spawn with a fixed error. Version
`2.1.208` is the minimum because it fixes truncated JSON and missing final
result messages from large piped `claude -p` responses.

Only a `type:"result"`, `subtype:"success"`, `is_error:false` envelope with a
string `result` is successful. The official result union places
`api_error_status` on the success arm, so a structurally proven
`success` + `is_error:true` result may map 401/403 to auth-required and 429 to
rate-limited. The `error_*` subtypes carry `errors`, not
`api_error_status`, and map conservatively to provider failure. Human-readable
result/error strings and stderr are never used for classification.

`--safe-mode` and an empty `--setting-sources` remove user/project/local
customizations, but documented managed policy still applies. The machine or
organization administrator and OS credential store are therefore part of the
trusted deployment boundary; AI CLI Gateway does not claim OS-user credential
isolation.

### 7.4 Gemini CLI

Baseline command shape:

```text
gemini
  --output-format json
  --approval-mode default
  -e none
  --model <trusted-provider-model>
```

- Prompt: stdin in non-TTY headless mode.
- Text result: `.response` from the single JSON envelope.
- JSON Schema: output contract in stdin; parse `.response` as exact JSON and
  validate locally.
- Tool reduction: a request-local JSON settings file disables core tools,
  extensions, agents, hooks, MCP, skills, folder trust, telemetry, prompt
  logging, usage statistics, and local `.env` loading; the process also uses a
  clean working directory and no interactive approval mode.
- Settings isolation: `GEMINI_CLI_HOME` is request-local and both
  `GEMINI_CLI_SYSTEM_SETTINGS_PATH` and
  `GEMINI_CLI_SYSTEM_DEFAULTS_PATH` point to nonexistent request-local files,
  preventing higher-precedence host system settings from overriding the
  hardened file.
- Health: bounded `gemini --version` and `gemini --help` probes plus closed
  credential-profile structure and value-presence checks. For the
  service-account profile the adapter requires an absolute credential path but
  never opens it; the platform-specific `doctor` safety gate verifies the
  external file's ownership, non-symlink identity, regular-file type, and
  permissions/DACL before the provider can become application-ready.

Gemini has no documented non-interactive auth-status command and no documented
no-session-persistence switch. Its runtime therefore uses a disposable
`GEMINI_CLI_HOME` that is deleted after the process tree is reaped. The adapter
only becomes ready with exactly one closed environment-backed auth profile:
Gemini API key (`GEMINI_API_KEY`, selected type `gemini-api-key`); Vertex
express (`GOOGLE_API_KEY`, selected type `vertex-ai`); or Vertex service
account (`GOOGLE_APPLICATION_CREDENTIALS`, `GOOGLE_CLOUD_PROJECT`, and
`GOOGLE_CLOUD_LOCATION`, selected type `vertex-ai`). Partial, mixed, duplicate,
or unknown profiles fail closed. The gateway owns
`security.auth.selectedType` in the request-local settings and does not accept
`GOOGLE_GENAI_USE_VERTEXAI` as a competing selector. It relays only the
selected profile's values in memory and never interprets, logs, copies, or
retains them.

If the user's Gemini authentication requires a writable persistent profile, the
MVP reports Gemini as `not_ready` rather than silently allowing provider session
persistence. Accordingly, Gemini support in the MVP accepts only the three
environment-backed or external credential shapes compatible with the disposable
home; cached interactive OAuth is not advertised as supported.

That local credential-shape boundary is not an upstream access rule. As of
2026-08-02, Google stopped the consumer Login-with-Google path for Gemini Code
Assist for individuals, Google AI Pro, and Google AI Ultra on 2026-06-18 and
points those users to Antigravity. Google explicitly says Code Assist Standard
and Enterprise access remains unchanged and paid API-key access remains, while
its current quota documentation also describes unpaid Gemini API-key and Vertex
Express tiers. The gateway does not treat any such list as exhaustive and does not
infer availability, billing tier, quota, entitlement, or live credential
validity; provider execution is authoritative. The adapter does not reuse cached
personal OAuth and does not implement Antigravity CLI. A configured,
implemented, or locally ready Gemini adapter proves only the gateway's closed
configuration and capability checks.

The pinned JSON output is a closed envelope with known optional `session_id`,
`stats`, and `warnings` metadata. A success envelope has no `error` and requires
a string `response`. The upstream `JsonFormatter.formatError()` can emit an
`error` object without a `response`; that error envelope is provider failure,
including on exit zero, while an optional response on the error arm must still
be a string and is discarded. Known metadata is type-checked then discarded.
Null/wrong-type error, unknown root fields, a success envelope with
missing/wrong-type response, malformed/duplicate/trailing JSON, and invalid
UTF-8 are protocol errors. Human-readable error text and stderr never determine
classification.

### 7.5 Fake CLI

The repository contains a small cross-platform Go fake CLI fixture compiled into a
temporary executable by integration tests. It supports deterministic modes for:

- final text;
- valid structured JSON;
- malformed, duplicate-key, fenced, or schema-invalid JSON;
- non-zero exit;
- authentication and rate-limit envelopes;
- stdout and stderr flooding;
- hanging and ignoring graceful termination;
- parent exit while a child or grandchild holds an output pipe;
- platform-specific child-tree and file-lock behavior.

Adapters are exercised against the fake executable using their real argv-building
and parsing paths. The fake never becomes a production provider.

## 8. Admission, Process Lifecycle, and Shutdown

### 8.1 Per-provider Scheduler

Each provider owns an independent bounded FIFO scheduler.

```text
queued -> starting -> running -> stopping -> reaped
```

- Default concurrency: 1 running process per provider.
- Default queue length: 32 requests per provider.
- Default aggregate queued normalized request bytes: 16 MiB per provider.
- Default maximum queue wait: 30 seconds.
- A full request-count or byte queue fails immediately with `429 queue_full`.
- Queue timeout fails with `503 queue_timeout`.
- The queue deadline is the earlier of the configured queue timeout and the
  request context deadline.
- Cancellation is checked before enqueue, while queued, immediately after dequeue,
  and immediately before process start.
- A concurrency permit is returned only after the containment mechanism reports no
  remaining members, the root `Wait` completes, streams are drained within their
  bound, and a bounded request-local cleanup attempt finishes.
- One saturated provider does not block another provider.

There is no automatic provider fallback or retry because the request may already
have incurred usage and is not safely idempotent.

Provider CLIs may implement their own network retries. The adapter disables them
where a documented flag exists; otherwise the execution deadline and cancellation
bound their duration but cannot guarantee one upstream attempt. This cost/attempt
boundary is documented.

### 8.2 Request Runtime

After admission, the supervisor creates a gateway-owned `0700` request directory.
It becomes the CLI working directory and contains only request-local runtime files.
Files are `0600`. Cleanup targets are derived from the supervisor's trusted root,
not from request input or unresolved environment variables.

The server acquires an exclusive platform file lock for its `0700` runtime root
before serving or running the janitor. A second gateway instance configured with
the same root fails startup, preventing one instance from deleting another
instance's active request directory.

Default limits:

| Resource | Default |
|---|---:|
| HTTP body | 1 MiB |
| `input` | 512 KiB |
| `instructions` | 256 KiB |
| JSON Schema | 32 KiB |
| Request JSON nesting depth | 64 |
| Provider execution | 300 seconds |
| Graceful termination | 2 seconds |
| Hard cleanup/reap | 5 seconds |
| stdout | 2 MiB |
| stderr | 256 KiB |
| final decoded output | 1 MiB |

All sizes are byte counts. stdout and stderr are consumed concurrently. Hitting
either cap fixes the failure cause as `output_limit_exceeded`, initiates the same
idempotent tree-cancel path, and continues only bounded drain/reap work.

### 8.3 Process-tree Control

`exec.CommandContext` killing a root process is not considered process-tree
cleanup.

Unix baseline:

- start the provider in a new process group;
- close stdin on cancellation;
- send `SIGTERM` to the verified process group;
- after two seconds, send `SIGKILL` to the group;
- always call `Wait` and close every pipe;
- verify that the recorded process group has no remaining members before releasing
  admission state;
- never signal a group until the recorded child PID/PGID invariants are checked.

Windows baseline:

- create the provider suspended;
- assign it to a non-breakaway Job Object configured with
  `KILL_ON_JOB_CLOSE`;
- resume it only after successful assignment;
- cancel with `TerminateJobObject`;
- wait until the Job reports zero active processes and close all handles.

The race-free Windows launch path uses `golang.org/x/sys/windows`; it does not
start a `.cmd` file or shell.

The cleanup guarantee covers request cancellation, HTTP disconnect, queue/server
shutdown after process start, timeout, output overflow, and handled termination
signals for the pinned CLIs and descendants that remain in their assigned
containment. A Unix descendant that deliberately calls `setsid` can escape a
process group; preventing that on macOS requires a stronger OS sandbox or VM and
is outside this MVP. Power loss and an uncatchable Unix `SIGKILL` of the gateway
are also outside the guarantee. These limits are documented rather than hidden.

### 8.4 Graceful Shutdown

On a handled platform shutdown signal, or after an unexpected serving-loop
failure, the application derives the configured HTTP shutdown context from
`context.Background` and starts `http.Server.Shutdown` in one owned goroutine.
The listener is wrapped by an idempotent close observer. Application shutdown
waits for either the wrapper's completed listener-close classification or the
HTTP shutdown result. If the HTTP result arrives first, the application calls the
wrapper's idempotent `Close` itself and waits for that completed classification.
Only that observation—not merely starting the HTTP goroutine—establishes that
listener admission has begun closing before Gateway drain starts.

After the listener-close handshake, shutdown proceeds in this order:

1. make one bounded `Gateway.Shutdown` call, rejecting queued work and canceling
   active scheduler contexts;
2. consume the HTTP shutdown result; every non-nil result remains a shutdown
   failure and triggers `http.Server.Close` to force outstanding body readers and
   connections closed;
3. if bounded Gateway shutdown failed, synchronously retry it with
   `context.Background()` until every scheduler worker has returned;
4. drain retained supervisors in reverse construction order; after any bounded
   error, synchronously retry that supervisor with `context.Background()` until
   every prepared lease, process wait, and containment owner has returned;
5. run the final janitor once with a fresh cleanup context; and
6. close the exclusive runtime root exactly once, after all supervisors drain.

The configured HTTP grace period is a hard network deadline, not a process
ownership deadline. Forced HTTP close bounds the network-facing phase, while
Gateway, supervisor, and process-tree ownership may continue draining beyond
that grace. The application never closes the root, returns to `main`, starts an
ownership-handoff goroutine, or permits `os.Exit` while a scheduler or supervisor
still owns work. A listener-close, HTTP, Gateway, supervisor, janitor,
or root invariant failure is retained as a fixed redacted shutdown failure and
causes a non-zero server exit.

Request-directory deletion gets the configured five-second cleanup budget. If it
still fails after containment is empty, the gateway attempts an atomic rename to a
quarantine name. If the platform also rejects the rename, the original verified
request path remains registered for cleanup. The provider is marked not ready, the
request returns `process_cleanup_failed`, and the permit is released. A bounded
janitor scans both request and quarantine names at startup and shutdown. It never
follows symlinks, never deletes outside the verified runtime root, and never
blocks a permit indefinitely. Any such directory remaining at shutdown causes a
non-zero exit.

## 9. Stable Error Model

All HTTP errors use:

```json
{
  "error": {
    "message": "Safe, stable explanation",
    "type": "invalid_request_error",
    "param": "stream",
    "code": "unsupported_parameter"
  }
}
```

`param` is `null` when no request field is responsible. Messages never include
provider stderr, prompt/output content, credentials, environment values, or
temporary paths.

| HTTP | Code | Meaning |
|---:|---|---|
| 400 | `invalid_json` | Invalid, duplicate-key, or trailing request JSON |
| 400 | `invalid_request` | Missing field, invalid type/value, UTF-8, or NUL |
| 400 | `unsupported_parameter` | Field or value outside the documented subset |
| 400 | `invalid_json_schema` | Unsupported or excessive schema |
| 401 | `invalid_bearer_key` | Missing, malformed, or incorrect configured key |
| 404 | `not_found` | Unknown HTTP route |
| 404 | `model_not_found` | Alias is not configured |
| 405 | `method_not_allowed` | Method is not supported by the exact route |
| 408 | `request_timeout` | Request body was not received within its deadline |
| 413 | `request_too_large` | Body, instructions, input, or schema size exceeded |
| 415 | `unsupported_media_type` | Content-Type or Content-Encoding is unsupported |
| 429 | `server_busy` | Global handler/body admission capacity reached |
| 429 | `queue_full` | Provider queue count or byte limit reached |
| 429 | `provider_rate_limited` | Rate limit proven by a structured provider contract |
| 503 | `queue_timeout` | Admission deadline expired before start |
| 503 | `provider_not_ready` | Executable, version, capability, or config unavailable |
| 503 | `provider_auth_required` | Provider authentication absent or expired |
| 503 | `service_shutting_down` | New work rejected during drain |
| 504 | `provider_timeout` | Provider execution deadline expired |
| 502 | `output_limit_exceeded` | stdout, stderr, or final-output cap exceeded |
| 502 | `provider_protocol_error` | Missing/malformed provider envelope or invalid UTF-8 |
| 502 | `structured_output_invalid` | Final JSON parse or local schema validation failed |
| 502 | `provider_failed` | Other non-zero or unclassified CLI/provider failure |
| 500 | `process_cleanup_failed` | Process containment or request runtime cleanup failed |
| 500 | `internal_error` | Gateway invariant or unexpected internal failure |

The stable `type` values are `invalid_request_error` for request, route, method,
media, model, and size errors; `authentication_error` for the gateway Bearer
failure;
`rate_limit_error` for global/provider admission and provider rate limits, and
`server_error` for provider and gateway 5xx failures.

A disconnected client usually has no response channel, so the gateway records a
`client_canceled` counter rather than inventing a public HTTP 499 contract.

## 10. Security and Privacy Boundary

### 10.1 Listener and Gateway Authentication

- Default listener: `127.0.0.1:8080`.
- IPv4 or IPv6 loopback literals only; a non-loopback bind is a startup error.
- CORS headers are not emitted and browser preflight is not enabled.
- `Host` must match the configured loopback listener or its localhost form.
- The Bearer key is optional for a loopback listener.
- If configured, exactly one `Authorization: Bearer ...` value is required and
  compared in constant time.
- Query-string credentials are unsupported.
- The key value is read from the environment variable named by configuration,
  never stored in TOML and never printed.

Omitting `api_key_env` disables gateway authentication. If `api_key_env` is
present, a missing or empty referenced value is a startup error; it never silently
falls back to unauthenticated mode.

Remote access should be provided later through a separately secured same-host
reverse proxy or tunnel; the MVP does not expose a plaintext remote listener or
claim built-in TLS.

### 10.2 Credential Boundary

- Users install and authenticate official provider CLIs themselves.
- Each provider receives a configured dedicated HOME/config location.
- The gateway does not discover and import credentials from the user's normal
  home.
- The gateway contains no token-management code: it does not issue, extract,
  semantically parse, refresh, or persist login tokens. Explicitly allowlisted
  credential environment values are relayed transiently to the selected child.
- A provider CLI may refresh or mutate credentials in its own user-prepared
  dedicated profile; that provider behavior is outside gateway token management.
- Health commands are parsed only for coarse state such as ready/not-ready and
  auth method; identity fields and raw results are discarded.
- Provider environment starts from a small allowlist rather than `os.Environ()`.
- The gateway Bearer key and other providers' credential variables are never
  inherited by a provider process.
- Executables, provider config homes, and external credential files use separate
  startup policies. On Unix an executable or Node entrypoint is a regular file
  owned by root or the effective gateway UID, has at least one execute bit, has no
  group/other write bit, and has no setuid, setgid, or sticky bit. A config home is
  a non-symlink directory owned by the effective UID with exact mode `0700`. A
  service credential is a non-symlink regular file owned by the effective UID
  with exact mode `0400` or `0600` and no special or execute bits. Relevant
  lexical/resolved parent directories must be owned by root or the effective UID
  and must not grant group/other mutation authority.
- On Windows, executables, Node entrypoints, and PATH directories apply one
  integrity policy to both the leaf and every lexical/resolved ancestor: reject
  shell shims and reparse points, require a final regular file or directory of
  the expected identity, accept only the token user, LocalSystem, Builtin
  Administrators, or TrustedInstaller as owner, reject every applicable allow ACE
  that gives an untrusted SID mutation/delete/owner/DACL authority, and require
  the token effective read/execute or list/traverse access needed for use.
- A Windows config-home or service-credential leaf must instead be owned by the
  token user and pass both confidentiality and integrity policy. The config home
  must give the token effective list/traverse/read and child-maintenance access;
  the credential must give the token effective data/attribute read access. Any
  applicable allow ACE that gives an untrusted SID read/list/traverse/execute or
  mutation/delete/owner/DACL authority fails the leaf. Their ancestor directories
  use only the executable/PATH integrity policy and trusted-owner set, plus the
  token's required traverse access; ancestor confidentiality is not required.
  Null DACLs, unsupported descriptors or ACE forms, and unsupported token state
  fail closed for every object class. Generic rights are expanded, inherited
  ACEs that apply to the object are evaluated, and `INHERIT_ONLY_ACE` entries do
  not apply to that object. A deny ACE never excuses an unsafe untrusted allow;
  effective token access and unsafe-authority detection are separate checks.
- Windows-native path code only acquires handles and normalizes security
  descriptors, reparse state, final identity, token principals, and canonical
  path spelling. An untagged pure policy layer performs trusted-owner
  classification, generic expansion, ACE applicability, integrity and
  confidentiality masks, effective-token access, and case-key canonicalization,
  so synthetic ACL policy tests run on every development platform.
- OS keychains and machine-managed configuration remain part of the trusted
  provider/OS boundary and are called out by `doctor`.

For each path-safe provider, doctor replaces ambient lookup with a frozen closure
containing only that provider's selected credential names and values plus the one
validated Windows `SystemRoot` value when applicable. The gateway key, other
providers' credentials, and every unselected environment value are absent. Gemini
service-account readiness validates the absolute credential file and its safe
parent chain at startup. Holding a native handle for a replacement check or
rejecting hard links is optional future hardening under the documented trusted
same-user/administrator boundary; the MVP adds no pre-spawn credential interface.

The documentation includes only a short notice that users are responsible for
their CLI authentication and applicable provider terms. Provider policy is not a
central project feature.

### 10.3 Data Handling

- Prompts and responses exist only in request memory and pipes.
- The gateway has no conversation database or response store.
- Request-local schema files and the deterministic hardened Gemini
  `.gemini/settings.json` are the only request artifacts created by the gateway;
  files are `0600`, the Gemini directory is `0700`, neither contains provider
  credentials, and all are removed after containment shutdown.
- Codex uses ephemeral execution.
- Claude uses no-session persistence.
- Gemini uses a disposable runtime home and is fail-closed when authentication
  cannot work under it.
- Core dumps should be disabled in the systemd example.
- Logs and metrics accept metadata fields only; raw prompt/output/schema/stderr
  values are not passed to the logging API at all.

These are gateway-owned storage guarantees. Provider CLIs may maintain
implementation-defined authentication caches or operational metadata in their
dedicated profile. The adapters use the documented ephemeral/no-session controls
and disposable Gemini runtime specifically to prevent request conversation
history from remaining there.

### 10.4 Public Repository Hygiene

One reviewed Go scanner is authoritative for both worktree bytes and a temporary
materialization of the staged Git index. It rejects symlinks, private-key headers,
non-placeholder secret assignments, auth/cache artifacts, binary magic or build
artifacts, invalid path text, and high-confidence raw credentials. It reports only
the category and repository-relative path, never matched content. The closed token
catalog applies token-boundary and exact alphabet/length checks for:

- OpenAI legacy `sk-` plus exactly 48 base62 characters, and current `sk-proj-`,
  `sk-svcacct-`, and `sk-admin-` plus 20–256 base62/underscore/hyphen characters;
- Anthropic `sk-ant-apiNN-` plus 40–256 base62/underscore/hyphen characters;
- Google/Gemini `AIza` plus exactly 35 base62/underscore/hyphen characters;
- GitHub `ghp_`, `gho_`, `ghu_`, and independently evidenced `ghr_` plus exactly
  36 alphanumeric characters;
- legacy GitHub App installation `ghs_` plus exactly 36 alphanumeric characters;
- current stateless GitHub App installation `ghs_` plus a bounded 36–768-character
  `[A-Za-z0-9._-]` candidate, accepted only with exactly two dots and three
  nonempty base64url segments; and
- fine-grained `github_pat_` with an exact 22-character segment, underscore, and
  exact 59-character segment.

Every family has positive and conservative near-miss fixtures. Generic JWT
matching is deliberately out of scope. The scanner skips Git metadata and, only
while implementation is active, the temporary uncommitted `.superpowers` ledger.
That ledger is removed before the final pre-Git scan; it is never ignored or
staged as public product content.

## 11. Configuration

Configuration is read once at startup from TOML. There is no hot reload in the
MVP. Structural errors—invalid aliases, unsafe listeners, duplicate models, an
alias referencing an undeclared provider, or syntactically invalid/non-absolute
paths—fail configuration validation before health probing.

Operational failures—an executable that is missing at probe time, an unsupported
installed version, missing authentication, or unavailable required
capabilities—mark only that declared provider `not_ready`. The listener starts
when at least one configured provider is ready, so healthy providers remain
usable. A core-ready diagnosis transfers the locked runtime root regardless of
the provider-ready count, and application assembly takes that ownership before
checking the count. If zero providers are ready, it constructs no scheduler,
supervisor, Gateway, HTTP ID source, HTTP server, or listener; it runs one fresh
bounded final janitor, closes the transferred root exactly once, and returns the
fixed `gateway_not_ready` result. The CLI writes only
`gateway_not_ready: run ai-cli-gateway doctor` and no raw or generated diagnostic
summary. A cleanup failure can add only the fixed shutdown classification. A
core-unsafe diagnosis transfers no root.

The normalized model list is converted to one canonical immutable registry during
diagnosis. The diagnostic report still lists every configured provider and every
configured alias. Resolved provider configs exist only when executable,
prefix/config-home, SafePath, and any selected external credential path pass the
platform policy; application assembly creates a fixed not-ready runtime entry for
every configured provider without a resolved config.

Illustrative shape:

```toml
[server]
listen = "127.0.0.1:8080"
api_key_env = "AI_CLI_GATEWAY_API_KEY"
http_body_bytes = 1048576
input_bytes = 524288
schema_bytes = 32768

[runtime]
root = "/absolute/path/to/ai-cli-gateway-runtime"

[providers.codex]
executable = "/absolute/path/to/codex"
config_home = "/absolute/path/to/codex-gateway-home"
concurrency = 1
queue_size = 32
queue_bytes = 16777216
queue_timeout = "30s"
execution_timeout = "5m"

[providers.claude]
executable = "/absolute/path/to/claude"
config_home = "/absolute/path/to/claude-gateway-home"
concurrency = 1
queue_size = 32

[providers.gemini]
executable = "/absolute/path/to/gemini"
config_home = "/absolute/path/to/gemini-runtime-root"
credential_env = ["GEMINI_API_KEY"]
concurrency = 1
queue_size = 32

[[models]]
id = "codex-default"
provider = "codex"
provider_model = "configured-provider-model"
created = 0
```

Provider `prefix_args` is available only for a trusted runtime wrapper such as an
absolute `node.exe` plus an absolute provider JS entrypoint on Windows. It is
startup configuration, never request data.

If `runtime.root` is omitted, the gateway resolves a user-specific
`ai-cli-gateway` directory below the platform temporary directory. The resolved
root must still be absolute, owned by the gateway OS user (or, on Windows, by
the already-trusted Builtin Administrators principal, commonly the default owner
for an administrator token), non-symlinked, and locked exclusively.

Alias rules:

- 1-128 ASCII letters, digits, `.`, `_`, `:`, or `-`;
- first character must be a letter or digit;
- case-sensitive and unique;
- no whitespace, slash, backslash, control character, or leading dash;
- provider and provider model must exist in trusted startup configuration.

The committed `config.example.toml` is explicitly the Unix/systemd deployment
example. It contains no real home paths, API keys, token files, emails, account
IDs, or provider authentication artifacts. Every platform validates its unchanged
bytes for TOML syntax, the complete documented field/model/provider set, exact
safe defaults, and the closed placeholder vocabulary. Unix additionally passes
the unchanged file to `config.Decode`.

Native Windows validation never pretends that Unix `/opt`, `/var/lib`, and `/run`
paths are native Windows paths. It creates a test-local copy, replaces every exact
known Unix placeholder exactly once with a deterministic drive-absolute
equivalent, verifies that no Unix placeholder remains in a path-valued field, and
only then calls the unchanged production decoder. This proves the same
field/default contract without weakening production Windows path validation.
Windows operators use drive-absolute or UNC paths; a native executable has empty
`prefix_args`, while a Node-distributed CLI uses absolute `node.exe` plus exactly
one absolute `.js` or `.mjs` entrypoint. No second committed Windows example is
part of the MVP.

## 12. Commands and Health Diagnostics

The complete accepted public command grammar is:

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

`PATH` is one nonempty following token that does not start with `-`; the config
file path itself may be relative even though paths inside the file are validated
separately. Only separate-token `--config PATH` is accepted. `--config=PATH`,
`-h`, a `help` command, bare `doctor`, missing or duplicate flags, unknown flags,
positional values, and extra arguments are usage errors.

Help writes the following exact text to stdout and exits 0. A usage error writes
the same text to stderr and exits 2.

```text
usage:
  ai-cli-gateway version
  ai-cli-gateway serve --config PATH
  ai-cli-gateway doctor --config PATH [--json]
```

`version` writes exactly
`ai-cli-gateway <version> (<commit>, <date>)` followed by one newline to stdout. A
clean `serve`, including handled pre-start or signal cancellation, writes nothing
to stdout or stderr and exits 0. Configuration failure writes
`configuration_invalid` to stderr; readiness failure writes
`gateway_not_ready: run ai-cli-gateway doctor` to stderr; every other startup,
serving, or cleanup failure writes
`serve_failed: run ai-cli-gateway doctor` to stderr. Each fixed line has one
trailing newline; readiness/runtime/serve/cleanup failures exit 1 and
configuration failures exit 2. Arbitrary error strings are never printed.

`doctor` writes only one deterministic validating text or JSON report, or one
fixed `configuration_invalid`/`doctor_failed` line, to the supplied stdout. The
CLI adds no stdout or stderr output and never appends a hint or fallback line
after report output. A valid core-ready report with at least one ready provider
exits 0. A core-unsafe or zero-ready report, exceptional diagnosis, writer
failure, or transferred-root close failure exits 1. Configuration failure exits
2. Doctor performs no inference.

The default diagnostic does not perform inference. It executes these fail-closed
phases in order:

1. defensively clone and validate normalized configuration and the nonnil
   dependency-function shape;
2. resolve and validate the configured gateway executable leaf plus every
   lexical/resolved parent with the executable policy, before invoking an adapter
   method or any root/process hook;
3. validate the exact configured adapter set, snapshot each adapter's one
   nonempty supported-version interval for report provenance, and evaluate
   listener, gateway-auth, and scheduler checks;
4. acquire and lock the runtime root through the injected opener, call the
   injected janitor, construct one bounded injected `ProbeController`, and pass
   its containment `SelfTest` with the resolved gateway executable;
5. validate each provider's executable/wrapper, config home, external credential
   path, and newly constructed SafePath;
6. call each path-safe and credential-eligible adapter, whose first and only
   initial command is `--version`;
7. only for a canonical supported version, run the pinned help/feature and coarse
   auth commands; and
8. canonicalize those filtered proofs with the frozen credential snapshot,
   recompute readiness, shut down the controller, and inspect its latched cleanup
   state before transferring the root.

A missing, unsafe, or non-regular gateway executable is an invalid injected
dependency, not a diagnostic row: `Run` returns the one fixed dependency error
with zero root, process-controller, adapter, or provider-command calls. The
controller embeds `provider.ProbeRunner` and adds `SelfTest`, `Shutdown`, and a
monotonic `CleanupFailed` observation. Production dependency assembly wraps the
real locked `process.Root` and `process.Supervisor`; each probe gets a fresh
runtime, and any discard or execution-cleanup failure is latched even when the
adapter converts the runner error into coarse health. Doctor exports the real
controller factory with root, limits, and runtime-ID generator parameters so
application assembly can assign it directly to the injected factory. Janitor,
root close, and controller construction are injected separately so exact
call/ownership tests do not depend on filesystem failure tricks; a
real-root/real-supervisor integration test remains mandatory. That test uses the
real test-built gateway from `testutil.BuildGateway`, because `SelfTest` requires
the hidden gateway self-test protocol, and places Unix temp/build artifacts below
a repo-local parent whose complete ancestor chain passes the executable policy;
it does not rely on `/tmp`.

The cleanup latch is fail-stop. Before the latch, each `RunProbe` gets one fresh
runtime. After it is set, the production controller rejects every subsequent
`RunProbe` with one fixed internal runner error before runtime-ID generation,
prepare, builder, or execution; an adapter may make the call, but no later
process command or runtime action occurs. Doctor canonicalizes the current
adapter's returned row, checks `CleanupFailed` immediately, stops the sorted
provider loop, and never calls a later adapter. Earlier canonical/preprobe rows
and the current canonical row remain report-only; every later provider receives
the exact core-skipped unknown row. Controller shutdown and the ordinary
resolved/frozen-state unwind then run. A containment `SelfTest` cleanup-category
error also sets the latch and fails `containment` before any provider probe, so
cleanup state cannot be hidden behind the containment result.

Only after the gateway executable passes does doctor call each configured
adapter's `Name` and `SupportedVersion` exactly once. It snapshots the supported
interval, requires strict `MinInclusive < MaxExclusive`, and stores that value as
private report provenance; a zero or reversed interval is the same fixed invalid
dependency result before root acquisition. Doctor canonicalization and writers
use the retained interval rather than re-querying an adapter.

The checks cover config semantics, loopback listener and Bearer environment,
runtime locking and janitor cleanup, temporary-runtime containment, model aliases,
scheduler limits, provider paths, versions, capabilities, coarse authentication,
and configured credential sources. A core containment failure causes zero
provider commands; an unsafe provider path causes zero commands for that provider.

Codex and Claude use their documented status commands. Gemini authentication is
reported as `configured`, `missing`, or `unknown`; it is never reported valid
without a supported non-inference proof.

Doctor does not trust an adapter's `Health.Status`. It requires exact agreement
between the configured provider, adapter-map key, the retained one-time
`Adapter.Name()` result, and returned health provider; accepts only canonical
dotted versions, closed status/problem
values, and the exact provider-specific auth/capability sets; de-duplicates and
sorts fixed values; independently reapplies `SupportedVersion`; and recomputes
the final status from executable, config-home, credential, version, capability,
and auth proofs. A valid adapter row is `ready` only with a supported canonical
version, the complete exact capability set, ready auth, and no problems. Codex or
Claude may be `unknown` only when every non-auth proof is ready, auth is
`unknown`, and `auth_unknown` is the sole problem; every other valid non-ready
combination is `not_ready`, and Gemini never uses adapter-derived `unknown`.
An input status inconsistent with those proofs is malformed. Rejected adapter
data is discarded without echoing it and becomes `not_ready`, auth `unknown`,
empty version/capabilities, and the ASCII-sorted problems `auth_unknown`,
`capability_missing`, `version_unreadable`.

Provider rows have fixed phase shapes. A provider skipped because any core row
failed is `unknown` with auth `unknown`, empty version/capabilities/problems,
and no resolved config. Before probing, the first failing condition in this
precedence is the row's only problem: missing executable
`executable_missing`; unsafe executable/wrapper/SafePath `executable_unsafe`;
unsafe config home `config_home_unsafe`; missing, empty, or NUL selected
credential `credential_missing`; unsafe service-credential file or chain
`credential_file_unsafe`. These rows are `not_ready`, have empty
version/capabilities, and use auth `missing` only for `credential_missing`,
otherwise auth `unknown`. Unsafe path or credential-file state produces no
resolved config and no probe. Resolution safety is evaluated independently of
problem precedence: doctor inspects every present service-credential path even
when another selected value is missing. Thus a missing project/location plus an
unsafe present service file still reports only higher-precedence
`credential_missing` but is unresolved. A provider whose executable/config paths
and every present credential path are safe, but whose selected credential is
missing, empty, or contains NUL, remains a resolved provider with that unusable
value normalized to a frozen absent/empty lookup; it is not scheduled because
its canonical row is not ready.

The report contains exactly every configured provider and alias in deterministic
order. Its core/provider/model slices, construction provenance, and build phase
are private. Construction provenance independently retains the sorted expected
provider names, sorted expected model aliases, and each expected provider's
supported version interval; it is never derived back from the mutable actual
rows during validation. Exported `Core()`, `Providers()`, and `Models()`
accessors return defensive copies, and no exported report field can be replaced
with a forged slice. `Diagnosis.Report()` also clones every private provenance
slice/map. `Diagnosis` retains the one canonical registry and likewise exposes
its resolved-provider map, configs, health values, and slices only through
defensive-copy accessors. Only path-safe providers have resolved configs.
`WriteText` and `WriteJSON` reject a zero, unconstructed, or non-final-phase
report, validate the complete closed phase/state relationship, and only then
copy it into a private serialization DTO. Validation requires actual provider
names and aliases to equal their independent expected snapshots exactly and
checks every readable/unsupported/ready version against the retained interval.
Invalid names, statuses, codes, messages, providers, aliases, versions, auth
values, capabilities, or problems are rejected. Underlying writer errors are
mapped to one fixed safe sentinel and never wrapped into public text.

Root-lock, janitor, containment-self-test, per-probe cleanup, controller shutdown,
and other expected core check failures return a complete report with a nil
exceptional error. The invariant is exact: if any core row is not `pass`,
`Diagnosis.RuntimeRoot` is nil and `Diagnosis.ResolvedProviders()` is empty.
Doctor clears every local resolved config and frozen credential map on every
core-failure or exceptional unwind, then synchronously drains probe resources
before closing an acquired root. If cleanup fails after probes, already
canonicalized provider rows may remain as report-only evidence, but no resolved
provider or frozen lookup escapes. Every per-probe cleanup,
controller-shutdown, or root-close error sets `probe_cleanup` to
`runtime_cleanup_failed`. That row is skipped only if no root was acquired; after
acquisition it passes when every cleanup action required on that path succeeds and
can fail independently alongside `runtime_janitor` or `containment`.

Controller shutdown never receives the possibly canceled Run context. Doctor
creates one fresh one-second context from `context.Background` and makes one
synchronous bounded `Shutdown` call. A nil or non-context result means that call
has returned ownership according to the controller contract. A
`context.Canceled` or `context.DeadlineExceeded` result means drain is incomplete,
so Doctor immediately calls `Shutdown(context.Background())` synchronously and
does not return until that ownership call completes. Only then may an unwind close
the root exactly once. There is no background goroutine or root handoff. Any first
shutdown error, second-drain error, cleanup latch, or root
close error keeps the fixed cleanup-failure result. An original Run
cancellation/deadline error still takes precedence after cleanup completes.
Platform lock calls expose one fixed `process.ErrRootLocked` only for true lock
contention; doctor maps that sentinel to `runtime_locked` and maps every other
root-open failure to `runtime_unsafe`.
The locked root transfers exactly-once close ownership to the caller only after
all core root/containment/cleanup checks pass; provider-ready count is not a
transfer condition. Cancellation, invalid injected dependencies, and inability to
construct a safe diagnosis use fixed exceptional errors and return only after
synchronously draining owned controller resources and closing any non-transferred
root.

Every absolute executable input, including `GatewayExecutable`, and every
absolute Node entrypoint, config-home, or credential input may be textually
nonclean. Doctor applies `filepath.Clean` immediately before any filesystem
walk, resolution, validation, storage, or execution; it validates from that
clean spelling and stores and executes only the resolved clean path. The
original spelling is never reused after cleaning.

SafePath never inherits ambient PATH. Each component is nonempty, absolute,
clean, NUL-free, and free of the platform list separator; its resolved directory
and parent authority pass the platform policy. Components are de-duplicated by
canonical identity (and case-insensitive canonical spelling on Windows), joined
only with `os.PathListSeparator`, and contain the validated configured executable
parent, resolved target parent, validated Windows entrypoint parent when present,
and only `/usr/bin` and `/bin` on Unix or validated `System32` on Windows. Every
enumerated fixed tail is mandatory to validate before de-duplication: a missing
or unsafe `/usr/bin`, `/bin`, or API-derived `System32` fails that provider's
SafePath as `executable_unsafe`; an identity duplicate may collapse only after
both candidates independently pass. Every stored SafePath component is itself
clean.

Server startup reuses these probes. Provider-specific readiness failures do not
prevent healthy providers from serving, but the failed provider returns
`provider_not_ready`. Application assembly creates not-ready entries without a
scheduler or supervisor for configured providers whose paths could not be
resolved. Core containment or listener-safety failure prevents the server from
starting. Login, credential, or configuration changes require a server restart
in the MVP.

## 13. Observability

Allowed request log fields:

- gateway request ID;
- endpoint and HTTP status;
- configured model alias and provider name;
- queue and execution duration;
- input, stdout, stderr, and final-output byte counts;
- stable error code, exit category, timeout/cancel/kill reason;
- canonical bounded dotted numeric provider CLI version;
- queue depth and running count.

The metadata validator accepts only empty pre-provider version state or canonical
`major.minor.patch`. Task 15 begins RED-first by correcting the current
digits-only core validator before gateway or logging work.

Forbidden fields:

- instructions, input, output, or schema;
- Authorization header or gateway key;
- provider credential values or auth files;
- full argv, environment, HOME/config paths, or temporary paths;
- raw stdout, raw stderr, provider envelope, provider session ID;
- email, organization, subscription, or account identity returned by health
  commands.

Log values are structured rather than concatenated, with control characters and
ANSI sequences rejected or escaped. Tests plant unique secrets in every forbidden
surface and scan logs, errors, and doctor output for leakage.

## 14. Testing Strategy

Implementation follows test-driven development. Every behavior starts with a
failing test, receives the smallest implementation, and is refactored only after
the test passes.

### 14.1 Unit Tests

- duplicate-key, trailing-value, UTF-8, BOM, NUL, unknown-field, and size handling;
- request-field compatibility matrix and stable `param` paths;
- alias validation, ordering, resolution, and request-to-argv separation;
- schema keyword allowlist, complexity limits, exact JSON parsing, and validation;
- public error mapping and secret-free messages;
- scheduler FIFO behavior, count/byte limits, cancellation races, and permit
  accounting;
- adapter argv, environment, prompt envelope, provider envelope parsing, and
  version checks;
- provider probes stop after one version command when that version is unreadable
  or unsupported;
- delimiter-like and adversarial instruction/input bytes preserve exact
  length-framed sections without being promoted to argv;
- environment allowlisting and cross-provider credential isolation;
- object-specific Unix/Windows path, ACL, SafePath, frozen-environment, canonical
  health, report-forgery, writer-failure, root-ownership, and registry-membership
  diagnostics;
- metadata-only logging.

### 14.2 Fake CLI Integration Tests

The fake CLI drives the real supervisor and adapters through text, valid JSON,
malformed output, exit failures, floods, hangs, signal resistance, and descendant
processes. Tests assert:

- no shell process appears;
- prompt bytes are present only on stdin;
- provider/model request data cannot alter argv structure;
- caps stay bounded while both output streams are drained;
- cancellation and timeouts leave no process, pipe, goroutine, file descriptor,
  temp directory, or scheduler permit;
- a simulated CLI-internal retry loop is still stopped by cancellation and the
  execution deadline;
- persistent request-directory deletion failure is quarantined within the bounded
  cleanup deadline and cannot deadlock a provider permit;
- invalid structured output is never returned with 2xx.

### 14.3 HTTP Black-box Tests

Run an in-process server with fake providers and verify both endpoints, optional
Bearer authentication, Content-Type and Host rules, request cancellation, queue
saturation, exact success shapes, error envelopes, request IDs, and absence of
sensitive logs. Slow headers, slow/oversized bodies, unsupported content encoding,
global handler/body-read saturation, and concurrent maximum-size bodies must stay
within the documented time and memory bounds.

### 14.4 Platform Process Tests

CI runs real helper executables on Linux, macOS, and Windows:

- parent, child, and grandchild lifetimes;
- descendant holding stdout/stderr after the parent exits;
- graceful-signal ignore followed by forced termination;
- cancellation immediately before and after process start;
- repeated timeout and output-overflow races;
- Unix process group validation;
- deliberate Unix session escape as a documented containment-limit test whose
  helper is explicitly cleaned up by the test harness;
- Windows suspended creation, Job assignment, nested Job environment, handle
  closure, and zero active-process assertion;
- Windows locked request directory cleanup behavior.

Mock-only process tests do not satisfy the release gate.

Generic fake-process success and cleanup tests use conservative scheduling
budgets: 30 seconds for execution, cleanup, and ordinary event waits, and 60
seconds for their outer ownership context. Tests whose purpose is execution
timeout, context deadline, TERM-to-KILL escalation, cleanup timeout, or bounded
failure construct and assert explicit short test-local limits instead. The same
split applies on native Windows. No production process limit changes for test
portability. Native Windows Job Object, ACL, reparse-point, and handle-lifecycle
execution is mandatory; cross-compilation is only preflight evidence.

### 14.5 Optional Live Contract Tests

Live adapters have opt-in tests gated by explicit environment variables. Without
`AI_CLI_GATEWAY_LIVE_PROBES=1`, each test skips before reading provider
configuration or credentials, creating a runtime, or invoking a command. That
global gate permits only version, help, and coarse login-status probes. Inference
additionally requires `AI_CLI_GATEWAY_LIVE_INFERENCE=1` and the matching
`AI_CLI_GATEWAY_LIVE_CODEX_INFERENCE=1`,
`AI_CLI_GATEWAY_LIVE_CLAUDE_INFERENCE=1`, or
`AI_CLI_GATEWAY_LIVE_GEMINI_INFERENCE=1` gate. A maintainer may explicitly enable
one minimal inference contract outside normal CI; its content and output are never
logged.

The opt-in live suite also uses a disposable canary directory to test stdin
framing, structured output, cancellation, and the documented tool-suppression
controls without targeting real user files. It asks the provider to attempt shell,
file, web, hook, MCP, plugin, and extension actions and records only pass/fail.
README's provider matrix distinguishes:

- `implemented`: adapter command/parser and fake integration tests pass;
- `live-verified`: the pinned official CLI passed the opt-in inference contract;
- `not-ready`: local version/auth/capability requirements are not met.

For Gemini, these labels describe gateway implementation and local evidence only.
They do not establish upstream availability, billing tier, quota, entitlement,
or live credential validity; provider execution is authoritative. They also do
not override Google's 2026-06-18 consumer Login-with-Google cutoff.

An adapter is never described as live-verified when that test was not run. Default
verification and CI compile the live-tag files without executing them using
`go test -tags=live -run '^$' ./internal/provider/...`. Task 19 release
verification, Task 18 default work, and CI do not invoke or inspect an installed
Codex, Claude, or Gemini CLI.

### 14.6 Required Verification

Before the first public push:

```text
gofmt -l .                         # must produce no output
go vet ./...
golangci-lint run ./...
go test -count=1 ./...
go test -race -count=1 ./...
go test -tags=integration -count=1 ./...
go test -trimpath -count=1 ./...
GOFLAGS=-trimpath go test -count=1 ./internal/testutil ./internal/testcli
go test -tags=live -run '^$' ./internal/provider/...
CGO_ENABLED=0 go build -trimpath ./cmd/ai-cli-gateway
```

The test executable builder locates the repository safely under trimmed caller
paths: it accepts a caller-derived root only when it is absolute and contains a
regular `go.mod` with the exact module declaration, otherwise it performs a
bounded upward search from the package working directory with the same checks.
Only that nested test build receives a 60-second deadline and retains its 64 KiB
output cap.

The final local verification also runs the applicable macOS process tests on the
development machine. CI runs the same unit, integration, lint, trimpath, live
compile-only, and build checks without provider credentials. Linux, macOS, and
Windows execute their native process layer; a native Windows containment failure
or skip is a release failure and cannot be replaced by `GOOS=windows` compilation.

## 15. Documentation and Delivery

The repository contains:

- `README.md` with the exact opening sentence, subset disclaimer, architecture,
  provider setup and dated Gemini consumer transition, local credential boundary,
  security boundary,
  configuration, doctor usage, and text/JSON curl examples;
- `LICENSE` containing Apache License 2.0;
- `THIRD_PARTY_NOTICES.md` covering the frozen compiled third-party module union;
- `CONTRIBUTING.md` with development, TDD, fake CLI, and verification commands;
- `SECURITY.md` with supported-version and private vulnerability-report guidance;
- `config.example.toml` with placeholders only;
- a hardened example systemd service and environment-file guidance;
- `.gitignore` covering binaries, temporary files, local configs, secrets, and
  provider auth artifacts without ignoring committed examples;
- the approved design and implementation plan under `docs/superpowers/`;
- CI for supported platforms.

After lifecycle reconciliation is independently approved and trimpath/process
test-budget repairs pass, Task 18 runs the only mutating `go mod tidy`. It verifies
the module graph, computes the sorted union of non-standard modules compiled for
Linux amd64/arm64, Darwin amd64/arm64, and Windows amd64, and freezes that union
plus the SHA-256 values of `go.mod` and `go.sum` before notices or remaining public
release work. The current expected union includes
`github.com/pelletier/go-toml/v2` (MIT),
`github.com/santhosh-tekuri/jsonschema/v6` (Apache-2.0), `golang.org/x/sys`
(BSD-3-Clause), and indirect `golang.org/x/text` (BSD-3-Clause); exact
versions and license files are reconfirmed from the frozen graph. No dependency,
module, or notice mutation is allowed after that freeze. Task 19 uses only
non-mutating `go mod tidy -diff`, or an equivalent clean-copy comparison, and
requires no diff plus the exact frozen hashes and union. Any delta returns to Task
18 review.

The README gives only a short provider-terms notice:

> You are responsible for installing and authenticating each provider CLI and for
> using it in accordance with its applicable terms.

The gateway executable and project never include real authentication files or
secret values.

After implementation and all verification gates pass:

1. move the completed workspace to `~/Dev/ai-cli-gateway`;
2. initialize Git with `main`;
3. review the exact staged file set for secrets and generated artifacts;
4. create one intentional initial commit;
5. create the public `ai-cli-gateway` repository on the connected personal GitHub
   account;
6. push `main`;
7. report the final file summary, design decisions, verification results, commit,
   and GitHub URL.

Git initialization is intentionally deferred until final verification because that
sequence was explicitly requested.

## 16. Acceptance Criteria

The MVP is complete only when all of the following are true:

- both HTTP endpoints implement only the documented closed subset;
- every unsupported or unknown field produces a deterministic 400 response;
- final text and locally schema-validated JSON work through fake providers;
- Codex, Claude, and Gemini adapters build documented non-shell invocations and
  expose redacted readiness diagnostics;
- Gemini readiness enforces the documented environment/external-credential-only
  MVP boundary;
- prompts are demonstrably sent through stdin, never argv;
- cancellation, timeout, overflow, and graceful shutdown pass containment torture
  tests without resources or permits left in the assigned process group/Job;
- listener closure is observed before Gateway drain, HTTP force close respects the
  hard network grace, and all process ownership drains before exit;
- Doctor's context-limited cleanup falls through to a synchronous ownership drain
  and never returns with controller/root ownership outstanding;
- per-provider concurrency and bounded queues behave independently;
- logs, errors, diagnostics, worktree bytes, and a materialized staged index pass
  the shared closed token and artifact scanner;
- configuration, systemd, curl, contribution, license, and security documentation
  are present;
- unit, race, fake integration, trimpath, lint, native Windows containment, and
  supported-platform builds pass;
- the frozen compiled-module notice inventory includes every indirect compiled
  module, and final tidy verification has no diff;
- default release verification invokes no installed provider CLI;
- no secret, local auth file, or generated binary is committed;
- the verified `main` branch is pushed to the public connected GitHub repository.
