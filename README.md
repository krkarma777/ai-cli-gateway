# AI CLI Gateway

[![CI](https://github.com/krkarma777/ai-cli-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/krkarma777/ai-cli-gateway/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/krkarma777/ai-cli-gateway)](https://github.com/krkarma777/ai-cli-gateway/releases/latest) [![License](https://img.shields.io/github/license/krkarma777/ai-cli-gateway)](LICENSE) [![Go](https://img.shields.io/github/go-mod/go-version/krkarma777/ai-cli-gateway)](go.mod)

## Build AI MVPs with the AI CLI access you already have.

AI CLI Gateway turns locally authenticated AI CLIs into an OpenAI Responses-compatible API.

It implements a focused **Responses API-compatible subset**, not full OpenAI API compatibility.

Your application calls one loopback endpoint. AI CLI Gateway routes the request to a configured Codex CLI, Claude Code, or Gemini CLI process and returns final text or locally validated JSON.

[Get started](#quick-start) · [Read the full setup guide](docs/getting-started.md) · [See the API reference](docs/reference.md) · [Download v0.1.0](https://github.com/krkarma777/ai-cli-gateway/releases/tag/v0.1.0)

## From SDK to local CLI

Point an OpenAI JavaScript SDK client at the gateway and use a configured model alias:

```javascript
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: process.env.AI_CLI_GATEWAY_API_KEY,
  baseURL: "http://127.0.0.1:8080/v1",
  timeout: 300_000,
  maxRetries: 0,
});

const response = await client.responses.create({
  model: "codex-local",
  instructions: "Answer concisely.",
  input: "Propose three names for my AI MVP.",
  text: { format: { type: "text" } },
  stream: false,
  store: false,
  tools: [],
  tool_choice: "none",
});

console.log(response.output_text);
```

The gateway must already be running, and `codex-local` must exist in your configuration. Checked-in [JavaScript](examples/openai-sdk/javascript/main.mjs) and [Python](examples/openai-sdk/python/main.py) clients provide complete runnable examples.

## What you can build

- **AI-service MVPs** — connect application code to an authenticated AI CLI without writing subprocess supervision.
- **Product validation** — test a workflow and response contract before committing to a larger architecture.
- **Demos and hackathons** — switch configured CLI backends without rewriting the client.
- **Structured-output prototypes** — request JSON that is validated locally against a strict schema.
- **Local SDK integrations** — keep a familiar Responses-style HTTP boundary around command-line providers.

AI CLI Gateway is designed for local or self-hosted use by one trusted OS identity. It is deliberately small: no web UI, database, or conversation store.

## What v0.1.0 supports

| Area | Included |
|---|---|
| Providers | Codex CLI, Claude Code, and Gemini CLI |
| HTTP | `POST /v1/responses` and `GET /v1/models` |
| Input | string `input` with optional string `instructions` |
| Output | final non-streaming text or strict JSON Schema output |
| Routing | operator-configured model aliases |
| Reliability | bounded queues, timeouts, cancellation, output limits, and process-tree cleanup |
| Diagnostics | `doctor` checks configuration and provider readiness without inference |

Not included in v0.1.0:

- SSE streaming;
- tool or function-call round trips;
- stored responses, gateway sessions, or conversation history;
- multimodal or array input;
- background execution;
- a web UI or external database; or
- other OpenAI endpoints.

Unsupported fields return a clear `400 unsupported_parameter` response instead of being ignored. See the [request contract](docs/reference.md#request-contract) for the accepted fields and the [stable error catalog](docs/reference.md#stable-errors) for failure shapes.

### Request path

```text
OpenAI SDK or HTTP client
  -> loopback POST /v1/responses
  -> configured model alias
  -> Codex / Claude Code / Gemini CLI
  -> completed text or validated JSON
```

`GET /v1/models` lists the configured aliases. Listing an alias does not bypass readiness checks when it is used for a response.

## Quick Start

The complete [Getting Started guide](docs/getting-started.md) covers release selection, SHA-256 verification, optional provenance checks, POSIX and Windows permissions, private key handling, configuration, and SDK setup. Use it for the secure copy-paste installation path.

Before starting the gateway:

1. Install and authenticate one supported provider CLI.
2. Download and verify the matching v0.1.0 archive.
3. Create private configuration and runtime directories.
4. Copy `config.example.toml` and replace its generic paths, model, and gateway settings.
5. Store the optional gateway key outside the repository.

### 1. Check readiness

Run Doctor with the absolute path to your configuration:

```bash
ai-cli-gateway doctor --config /absolute/path/to/config.toml
```

For machine-readable diagnostics:

```bash
ai-cli-gateway doctor --config /absolute/path/to/config.toml --json
```

Doctor checks the gateway configuration, executable identity and version, authentication readiness, provider capabilities, runtime containment, and configured aliases. It does not send an inference request.

### 2. Start the gateway

After completing the private key-loading step in the full guide, start the gateway:

```bash
ai-cli-gateway serve --config /absolute/path/to/config.toml
```

The listener defaults to `127.0.0.1:8080`. Keep this terminal running.

### 3. Send a request

In another terminal, use the guide's bounded key-loading step, then save the request as `request.json`:

```json
{
  "model": "codex-local",
  "instructions": "Answer concisely.",
  "input": "Suggest three names for an AI note-taking app.",
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

Send the file so the prompt and key do not become command-line arguments:

```bash
curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \
  --data-binary @request.json \
  http://127.0.0.1:8080/v1/responses
```

A successful request returns a completed Responses-style object. The final provider text is under `output[0].content[0].text`; OpenAI SDK clients also expose `response.output_text`.

### Try structured output

Set `text.format.type` to `json_schema` when your prototype needs a predictable object:

```json
{
  "model": "claude-local",
  "input": "Classify this synthetic ticket.",
  "text": {
    "format": {
      "type": "json_schema",
      "name": "ticket_result",
      "strict": true,
      "schema": {
        "type": "object",
        "properties": {
          "category": {
            "type": "string",
            "enum": ["question", "bug"]
          }
        },
        "required": ["category"],
        "additionalProperties": false
      }
    }
  }
}
```

The provider returns JSON as text. AI CLI Gateway parses it, rejects duplicate keys, validates it against the supported schema profile, and returns it without repair or fallback.

## Security boundaries

- **Provider credentials stay with provider tooling.** You install and authenticate each CLI. The gateway does not issue, discover, extract, copy, refresh, or store provider login tokens.
- **Requests avoid shell interpolation.** Provider processes start from argument arrays without a shell, and prompts are delivered through stdin rather than prompt arguments.
- **Sensitive content stays out of gateway logs.** The gateway does not log prompts, model output, or credentials. Stable errors omit raw provider stdout and stderr.
- **Local files are private.** Each admitted request receives a private temporary runtime and bounded request files; startup validates provider executables and sensitive directories.
- **Work is bounded.** Queues, request size, provider output, execution time, cancellation, and child-process cleanup have enforced limits.
- **Upstream processing still happens.** The selected CLI may send request data to its upstream provider. Provider availability, quota, billing, entitlement, and terms remain upstream concerns.
- **The trust boundary is one OS identity.** The gateway is not an isolation boundary between mutually untrusted users sharing an OS account. Use a dedicated service user for shared deployments.

The listener accepts loopback addresses only. Bearer authentication is optional but recommended when more than one local process can reach the port.

Read [SECURITY.md](SECURITY.md) for private vulnerability reporting and the [operations reference](docs/reference.md#security-details) for detailed containment behavior.

## Release integrity

The v0.1.0 release provides seven downloadable assets:

- five platform archives;
- `SHA256SUMS`, covering every archive and the SBOM; and
- one SPDX SBOM for the five shipped binaries.

GitHub stores build-provenance attestations separately for all seven assets. Follow the checksum and attestation commands in [Getting Started](docs/getting-started.md) before installing a binary.

## Documentation

| Need | Read |
|---|---|
| Install and send a first request | [Getting Started](docs/getting-started.md) |
| Check request fields, schemas, errors, providers, and operations | [API and Operations Reference](docs/reference.md) |
| Run the official SDK examples | [JavaScript](examples/openai-sdk/javascript/main.mjs) or [Python](examples/openai-sdk/python/main.py) |
| Report a vulnerability privately | [Security Policy](SECURITY.md) |
| Build, test, or contribute | [Contributing](CONTRIBUTING.md) |
| Review the launch scope | [v0.1.0 release notes](https://github.com/krkarma777/ai-cli-gateway/releases/tag/v0.1.0) |

AI CLI Gateway is Apache-2.0 licensed. You are responsible for using each provider CLI in accordance with its applicable terms.
