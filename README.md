# AI CLI Gateway

[![npm version](https://img.shields.io/npm/v/ai-cli-gateway?logo=npm)](https://www.npmjs.com/package/ai-cli-gateway) [![npm downloads](https://img.shields.io/npm/dm/ai-cli-gateway?logo=npm)](https://www.npmjs.com/package/ai-cli-gateway) [![Node.js](https://img.shields.io/node/v/ai-cli-gateway?logo=node.js)](https://www.npmjs.com/package/ai-cli-gateway) [![CI](https://github.com/krkarma777/ai-cli-gateway/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/krkarma777/ai-cli-gateway/actions/workflows/ci.yml) [![License](https://img.shields.io/npm/l/ai-cli-gateway)](LICENSE)

Build AI MVPs with Codex CLI, Claude Code, and Gemini CLI through one local OpenAI Responses-compatible endpoint.

AI CLI Gateway turns locally authenticated AI CLIs into a focused **Responses API-compatible subset**. It is not a full OpenAI API implementation.

## Quick Start

```console
npm install --global ai-cli-gateway
ai-cli-gateway init
ai-cli-gateway serve
```

The npm launcher requires Node.js `>=22.14.0`. Install and authenticate at least one supported provider CLI with its own tooling before running init. Guided init creates the configuration, runs readiness checks without inference, and prints the exact client-key and request commands for your system.

The five scoped platform packages are optional internal implementation packages; users install only `ai-cli-gateway`. npm selects the matching native binary for macOS, Linux, or Windows.

[npm package](https://www.npmjs.com/package/ai-cli-gateway) · [Getting Started](docs/getting-started.md) · [API and Operations Reference](docs/reference.md) · [v0.2.1 release](https://github.com/krkarma777/ai-cli-gateway/releases/tag/v0.2.1) · [All releases](https://github.com/krkarma777/ai-cli-gateway/releases)

## From SDK to local CLI

Point an OpenAI JavaScript SDK client at the loopback gateway and use a model alias created during init:

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

The gateway routes each request through the configured alias to a Codex CLI, Claude Code, or Gemini CLI process, then returns completed text or locally validated JSON. Checked-in [JavaScript](examples/openai-sdk/javascript/main.mjs) and [Python](examples/openai-sdk/python/main.py) clients provide runnable examples.

```text
OpenAI SDK or HTTP client
  -> loopback POST /v1/responses
  -> configured model alias
  -> Codex CLI / Claude Code / Gemini CLI
  -> completed text or validated JSON
```

## What it supports

| Area | Included |
|---|---|
| Providers | Codex CLI, Claude Code, and Gemini CLI |
| Platforms | macOS Intel and Apple silicon, Linux x86-64 and ARM64, and Windows x86-64 |
| HTTP | `POST /v1/responses` and `GET /v1/models` |
| Input | string `input` with optional string `instructions` |
| Output | final non-streaming text or strict JSON Schema output validated locally |
| Routing | operator-configured model aliases |
| Reliability | bounded queues, timeouts, cancellation, output limits, and process-tree cleanup |
| Setup | guided interactive init, strict flag-only automation, and Doctor diagnostics |

This is useful for AI MVPs, product validation, demos, hackathons, structured-output prototypes, and local SDK integrations. It is designed for local or self-hosted use by one trusted OS identity; it has no web UI, database, or conversation store.

## Focused compatibility

AI CLI Gateway intentionally does not support:

- SSE streaming;
- tool or function-call round trips;
- stored responses, gateway sessions, or conversation history;
- multimodal or array input;
- background execution; or
- other OpenAI endpoints.

Unsupported fields return a clear `400 unsupported_parameter` response instead of being ignored. See the [request contract](docs/reference.md#request-contract) and [stable error catalog](docs/reference.md#stable-errors) for the exact boundary.

## Security and distribution

- **Provider credentials stay with provider tooling.** The gateway never installs a provider CLI, runs provider login, or copies provider credentials.
- **The listener is local.** It accepts loopback addresses only and defaults to `127.0.0.1:8080`; bearer authentication is optional but recommended when multiple local processes can reach it.
- **Prompts avoid shell interpolation.** Provider processes start from argument arrays and receive prompts through stdin.
- **Sensitive content stays out of gateway logs.** Prompts, model output, credentials, and raw provider output are not logged.
- **Execution is bounded.** Request size, queues, output, runtime, cancellation, and process-tree cleanup have enforced limits.
- **Upstream processing still happens.** The selected CLI may send request data to its upstream provider, whose availability, quota, billing, entitlement, and terms still apply.
- **The trust boundary is one OS identity.** This is not an isolation boundary between mutually untrusted users sharing an account; use a dedicated service user for shared deployments.

The launcher has no lifecycle downloader and installs one exact host-specific optional dependency. The native executable in each npm package is byte-for-byte identical to its matching GitHub Release archive.

Version `0.2.1` was manually published, and its six npm packages do not expose npm provenance attestations. The repository release workflow is configured to use npm Trusted Publishing for future releases. GitHub build-provenance attestations still cover the five archives, SPDX SBOM, and checksum manifest for `v0.2.1`.

Read [SECURITY.md](SECURITY.md) for private vulnerability reporting and [Getting Started](docs/getting-started.md) for checksum, attestation, update, uninstall, and optional-dependency recovery procedures.

## Documentation

| Need | Read |
|---|---|
| Install and send a first request | [Getting Started](docs/getting-started.md) |
| Check request fields, schemas, errors, providers, and operations | [API and Operations Reference](docs/reference.md) |
| Run official SDK examples | [JavaScript](examples/openai-sdk/javascript/main.mjs) or [Python](examples/openai-sdk/python/main.py) |
| Report a vulnerability privately | [Security Policy](SECURITY.md) |
| Build, test, or contribute | [Contributing](CONTRIBUTING.md) |
| Review the current release | [v0.2.1 release notes](docs/releases/v0.2.1.md) |
| Review guided init | [historical v0.2.0 release notes](docs/releases/v0.2.0.md) |
| Review the original launch | [historical v0.1.0 release notes](https://github.com/krkarma777/ai-cli-gateway/releases/tag/v0.1.0) |

AI CLI Gateway is Apache-2.0 licensed. You are responsible for using each provider CLI in accordance with its applicable terms.
