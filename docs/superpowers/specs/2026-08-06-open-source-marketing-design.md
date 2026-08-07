# AI CLI Gateway Open-Source Marketing Refresh Design

**Status:** Approved direction, pending written-spec review  
**Date:** 2026-08-06

## Context

AI CLI Gateway is technically well documented, but the current README opens with compatibility boundaries and then immediately enters a long secure installation procedure. A new visitor must read too far before learning why the project is useful. The v0.1.0 GitHub release body is also an automatically generated change list rather than a useful launch narrative.

This refresh will make the project's value clear without weakening its technical accuracy or its security posture.

## Audience and desired reaction

The primary audience is a developer using AI-assisted or “vibe coding” workflows to build and validate an AI-service MVP, proof of concept, demo, or hackathon project. They already have one or more supported AI CLIs installed and authenticated and want to connect application code to that access through a familiar HTTP API.

Within the first screen, the visitor should understand:

1. Their existing locally authenticated AI CLI can become an application-facing endpoint.
2. Existing OpenAI SDK-style code can call the gateway through `/v1/responses` with a local base URL.
3. The gateway is deliberately small, local/self-hosted, and suitable for fast product validation.
4. It is a Responses API-compatible subset, not a claim of complete OpenAI API compatibility.

## Messaging direction

The approved direction is a bold, developer-first hero. It should imply the practical benefit of reusing access the developer already has, without centering the copy on pricing or making claims about “free API access,” unlimited use, billing avoidance, or entitlement.

Recommended lead copy:

> **Build AI MVPs with the AI CLI access you already have.**
>
> AI CLI Gateway turns locally authenticated Codex CLI, Claude Code, and Gemini CLI into one OpenAI Responses-compatible endpoint—built for fast local prototyping, demos, and validation.

Supporting bridge:

> Your AI tools already work in the terminal. Your MVP expects an API. AI CLI Gateway bridges that gap locally.

The existing project sentence remains part of the identity and should appear prominently:

> AI CLI Gateway turns locally authenticated AI CLIs into an OpenAI Responses-compatible API.

Where precision matters, “Responses API-compatible subset” must replace any language that could imply full compatibility.

## README information architecture

The README will gain a compact product-introduction layer before the existing `Quick Start`. The validated installation and verification blocks will remain intact unless a test-backed correction is required.

### 1. Hero and proof of life

The first section will contain:

- the developer-first headline and one short supporting paragraph;
- badges for CI, latest release, license, and Go;
- direct links to Get Started, the API example, the v0.1.0 release, and architecture/scope;
- a compact JavaScript OpenAI SDK example using `baseURL: "http://127.0.0.1:8080/v1"`, a gateway Bearer key, and the configured `codex-local` alias;
- the corresponding response text access so the example shows a complete application-facing loop.

The example is illustrative, while the checked-in JavaScript and Python contract examples remain the canonical executable SDK validation.

### 2. Why it exists

A short problem-to-solution section will use the approved implicit framing:

- the provider CLI is already installed and authenticated;
- an MVP expects a stable HTTP interface;
- the gateway exposes one local Responses-compatible endpoint and routes model aliases to provider adapters;
- application code can switch configured aliases without learning each CLI's invocation details.

This section must not describe the project as a workaround for provider billing or terms.

### 3. Built-for use cases

Four concise use cases will be shown:

- vibe-coded AI-service MVPs;
- proof-of-concept product validation;
- demos and hackathons;
- local integration and SDK contract testing.

### 4. Capability and boundary snapshot

A compact table will show the supported providers and MVP contract:

- Codex CLI, Claude Code, and Gemini CLI adapters;
- `POST /v1/responses` and `GET /v1/models`;
- final non-streaming text and strict JSON Schema output;
- provider/model alias routing;
- explicit unsupported scope: SSE streaming, tool/function-call round trips, stored conversations/sessions, web UI, and external database.

The detailed request contract later in the README remains authoritative.

### 5. Trust layer

The introductory layer will include a short factual trust section:

- the user authenticates with each official CLI;
- the gateway does not issue, extract, copy, or store provider login tokens;
- prompts are passed to the selected CLI on stdin, not in shell command strings or command-line prompt arguments;
- requests receive isolated temporary working directories, timeouts, cancellation, child-process cleanup, bounded concurrency/queues, and bounded output;
- sensitive prompt, response, and credential contents are not logged;
- release archives publish checksums and an SPDX SBOM; GitHub stores build-provenance attestations separately for all seven release assets.

The copy must not say that prompts “stay local”: the gateway and CLI credentials are local, but the selected CLI can send prompt data to its upstream provider.

### 6. Existing technical documentation

After the introductory layer, the current secure Quick Start, SDK checks, architecture, closed request contract, error model, operational behavior, live-test boundaries, security notice, terms notice, and official source links remain available in their existing depth.

## GitHub v0.1.0 release notes

The generated release body will be replaced with a hand-written launch note organized as follows:

1. A one-paragraph value proposition for MVP builders.
2. A “Why this exists” bridge from locally working CLIs to an application-facing API.
3. A concise list of use cases.
4. A small OpenAI SDK example or link to the README example.
5. Release highlights: three adapters, the Responses-compatible subset, JSON Schema output, routing, doctor diagnostics, and process/concurrency safeguards.
6. An explicit “Supported in v0.1.0” and “Not in v0.1.0” boundary.
7. Authentication and responsibility notice.
8. Release-integrity details: platform archives, `SHA256SUMS`, SPDX SBOM, immutable release, and provenance attestations.
9. Links to the Quick Start, Security policy, contribution guide, and full changelog.

The release note will distinguish deterministic fake-CLI adapter coverage from optional live provider checks. It will not claim that every provider/account combination was live-verified.

## Accuracy and safety guardrails

- Use “OpenAI Responses-compatible API” as the approachable description and “Responses API-compatible subset” at every compatibility boundary.
- Do not claim complete SDK or endpoint compatibility.
- Do not use “free API,” “unlimited,” “subscription-to-API,” “billing bypass,” or equivalent wording.
- Do not imply that CLI access guarantees a particular model, quota, entitlement, or provider behavior.
- Keep the terms note short: users install and authenticate their own CLIs and are responsible for applicable access and terms.
- Do not expose real gateway keys, CLI credentials, authentication files, prompts, or outputs in examples.
- Use `127.0.0.1` in examples and preserve the recommendation for optional local Bearer authentication.
- Do not describe local process controls as a security boundary between mutually untrusted users sharing one OS account.

## Scope and non-goals

This work includes:

- the README's top-level positioning and navigation;
- a concise SDK example and use-case/capability/trust summaries;
- the public v0.1.0 GitHub release body;
- optionally, the GitHub repository description if it can be improved without changing project identity.

This work does not include:

- product or API behavior changes;
- new streaming, tool-calling, session, or UI features;
- a logo, social-preview image, website, or launch campaign;
- renaming the project or repository;
- changing release assets or the immutable v0.1.0 tag.

## Implementation and publication flow

1. Update the README on a documentation branch while preserving the validated Quick Start command blocks.
2. Add or adjust documentation-focused tests only when they encode important marketing accuracy or security invariants.
3. Run formatting, documentation/security contract tests, the full Go test suite, lint, and build checks required by the repository.
4. Review the rendered README structure and verify all local and public links.
5. Draft the complete release body in a tracked local Markdown file so it can be reviewed and tested before publication.
6. Update the GitHub v0.1.0 release body without changing its tag, assets, or immutable status.
7. Update the repository description only if the final copy is more useful and remains accurate.

## Acceptance criteria

- The first 20 README lines answer what the project is, who it is for, and why it is useful.
- The first 80 README lines show an OpenAI SDK-shaped request and the primary MVP use cases.
- The copy clearly communicates reuse of existing local CLI authentication without explicit cost-avoidance claims.
- Compatibility-subset boundaries and intentionally unsupported features remain prominent.
- The existing secure installation and verification workflow remains correct and testable.
- Authentication ownership, prompt routing, and local security boundaries are described accurately.
- The v0.1.0 release body reads as a substantive launch note rather than an auto-generated commit list.
- No real secrets or authentication files are added.
- Documentation/security contract tests, unit tests, lint, and build checks pass before publication.
