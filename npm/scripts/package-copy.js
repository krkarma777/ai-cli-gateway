export const LAUNCHER_DESCRIPTION =
  "Build AI MVPs with Codex CLI, Claude Code, and Gemini CLI through a local OpenAI Responses-compatible API.";

export const LAUNCHER_KEYWORDS = Object.freeze([
  "ai",
  "ai-cli",
  "ai-gateway",
  "llm-gateway",
  "openai",
  "openai-compatible",
  "responses-api",
  "codex-cli",
  "claude-code",
  "gemini-cli",
  "local-ai",
  "ai-mvp",
  "structured-output",
  "json-schema",
]);

const PLATFORM_LABELS = Object.freeze({
  "darwin-x64": "macOS Intel",
  "darwin-arm64": "macOS Apple silicon",
  "linux-x64": "Linux x86-64",
  "linux-arm64": "Linux ARM64",
  "win32-x64": "Windows x86-64",
});

function platformLabel(target) {
  const label = PLATFORM_LABELS[target.key];
  if (label === undefined) {
    throw new Error("unknown npm package target");
  }
  return label;
}

export function nativeDescription(target) {
  return `Internal ${platformLabel(target)} binary for AI CLI Gateway. Install the ai-cli-gateway package instead.`;
}

export function nativeKeywords(target) {
  return ["ai-cli-gateway", "native-binary", target.platform, target.arch];
}

export function launcherReadme(nodeRange) {
  return `# AI CLI Gateway

[![npm version](https://img.shields.io/npm/v/ai-cli-gateway?logo=npm)](https://www.npmjs.com/package/ai-cli-gateway) [![npm downloads](https://img.shields.io/npm/dm/ai-cli-gateway?logo=npm)](https://www.npmjs.com/package/ai-cli-gateway) [![Node.js](https://img.shields.io/node/v/ai-cli-gateway?logo=node.js)](https://www.npmjs.com/package/ai-cli-gateway) [![CI](https://github.com/krkarma777/ai-cli-gateway/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/krkarma777/ai-cli-gateway/actions/workflows/ci.yml) [![License](https://img.shields.io/npm/l/ai-cli-gateway)](https://github.com/krkarma777/ai-cli-gateway/blob/main/LICENSE)

Build AI MVPs with Codex CLI, Claude Code, and Gemini CLI through a local OpenAI Responses-compatible API.

AI CLI Gateway turns locally authenticated AI CLIs into a focused Responses API-compatible subset. Your application calls one loopback endpoint; the gateway runs the configured CLI and returns final text or locally validated JSON. It is not a full OpenAI API implementation.

## Quick Start

\`\`\`console
npm install --global ai-cli-gateway
ai-cli-gateway init
ai-cli-gateway serve
\`\`\`

Node.js \`${nodeRange}\` is required. Install and authenticate Codex CLI, Claude Code, or Gemini CLI with the provider's own tooling before running init. Guided init creates the configuration, checks readiness without inference, and prints the exact client-key and request commands for your system.

Users install only \`ai-cli-gateway\`. Its five scoped platform packages are optional internal implementation packages, and npm selects the matching native binary automatically.

[Getting Started](https://github.com/krkarma777/ai-cli-gateway/blob/main/docs/getting-started.md) · [API and Operations Reference](https://github.com/krkarma777/ai-cli-gateway/blob/main/docs/reference.md) · [GitHub Releases](https://github.com/krkarma777/ai-cli-gateway/releases)

## Connect with the OpenAI JavaScript SDK

\`\`\`js
import OpenAI from "openai";

const client = new OpenAI({
  apiKey: process.env.AI_CLI_GATEWAY_API_KEY,
  baseURL: "http://127.0.0.1:8080/v1",
  timeout: 300_000,
  maxRetries: 0,
});

const response = await client.responses.create({
  model: "YOUR_ALIAS",
  input: "Propose three names for my AI MVP.",
  stream: false,
  store: false,
  tools: [],
  tool_choice: "none",
});

console.log(response.output_text);
\`\`\`

## What it supports

- Codex CLI, Claude Code, and Gemini CLI
- macOS Intel and Apple silicon, Linux x86-64 and ARM64, and Windows x86-64
- \`POST /v1/responses\` and \`GET /v1/models\`
- final non-streaming text or strict JSON Schema output validated locally
- operator-configured model aliases
- guided init, Doctor diagnostics, bounded queues, timeouts, cancellation, and process cleanup

It is useful for AI MVPs, product validation, demos, hackathons, structured-output prototypes, and local SDK integrations.

## Focused compatibility

This is not the full OpenAI API. It does not support SSE streaming, tool-call round trips, stored responses, gateway sessions, conversation history, multimodal input, background execution, or other OpenAI endpoints.

## Security and distribution

Provider credentials remain owned by provider tooling. The gateway listens on loopback, avoids shell interpolation, does not log prompts, model output, or credentials, and is designed for one trusted OS identity. The selected CLI may still send request data to its upstream provider.

The launcher installs one host-specific optional dependency. Public packages define no lifecycle scripts and perform no application-owned binary download. Each native npm executable is byte-for-byte identical to its matching GitHub Release archive.

Version \`0.2.1\` was manually published, and its six packages do not expose npm provenance attestations. The repository release workflow is configured to use npm Trusted Publishing for future releases; GitHub build-provenance attestations cover the \`v0.2.1\` release assets.

## Documentation

- [Getting Started](https://github.com/krkarma777/ai-cli-gateway/blob/main/docs/getting-started.md)
- [API and Operations Reference](https://github.com/krkarma777/ai-cli-gateway/blob/main/docs/reference.md)
- [Security Policy](https://github.com/krkarma777/ai-cli-gateway/blob/main/SECURITY.md)
- [GitHub Releases](https://github.com/krkarma777/ai-cli-gateway/releases)
- [GitHub repository](https://github.com/krkarma777/ai-cli-gateway)
`;
}

export function nativeReadme(target) {
  const label = platformLabel(target);
  return `# ${target.packageName}

> Internal platform package for ${label}. Install \`ai-cli-gateway\` instead.

\`\`\`console
npm install --global ai-cli-gateway
\`\`\`

Target: \`${target.key}\` (\`npm os=${target.platform}\`, \`npm cpu=${target.arch}\`, \`GOOS=${target.goos}\`, \`GOARCH=${target.goarch}\`).

This native binary is installed automatically through an exact optional dependency of the main launcher. Do not install or invoke this package directly.

No standalone JavaScript API is provided.

- [Main npm package](https://www.npmjs.com/package/ai-cli-gateway)
- [GitHub repository](https://github.com/krkarma777/ai-cli-gateway)
`;
}
