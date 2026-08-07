# Product-Centered README Simplification Design

## Goal

Make the repository front page useful for someone deciding whether to try AI CLI Gateway and taking the first step. Reduce `README.md` from 897 lines to roughly 200–300 lines without weakening the documented API or security contract.

## Public-document structure

### `README.md`

Keep only:

- the hero, badges, and one-sentence product definition;
- one short OpenAI SDK example;
- use cases and the v0.1.0 supported/unsupported scope;
- a short Quick Start that links installation and platform-specific setup to the full guide, then shows `doctor`, `serve`, and one request;
- the essential authentication, data-flow, logging, and same-OS-user boundaries;
- links to getting started, API reference, security, contributing, and the release.

The README target is 200–300 lines. It must remain useful without requiring readers to understand test architecture, release-internal terminology, historical provider transitions, or implementation invariants.

### `docs/getting-started.md`

Move the complete executable onboarding material here:

- release asset selection and checksum verification;
- POSIX installation, private directories, configuration, and terminal flow;
- Windows archive verification, ACL setup, configuration, and terminal flow;
- official SDK contract commands;
- retained SDK work-directory recovery guidance.

The existing security properties and copy-paste command semantics remain unchanged. This file becomes the repository contract test target for the sealed onboarding instructions.

### `docs/reference.md`

Move the detailed technical reference here:

- architecture and endpoint scope;
- request fields and portable JSON Schema profile;
- text, structured-output, success, models, and stable-error examples;
- command grammar and exit status;
- provider configuration and readiness rules;
- Unix Node launchers and Windows paths;
- operational limits, shutdown, containment, and security details;
- current upstream contract-source links when they remain useful.

Historical Gemini transition chronology is removed rather than moved. Current credential shapes and present compatibility requirements remain.

### `CONTRIBUTING.md`

Keep contributor-only information here, including build/test commands and opt-in live-test mechanics. Public product pages do not describe whether maintainers ran a live provider test.

### `docs/releases/v0.1.0.md`

Keep the product purpose, supported and unsupported scope, authentication/data boundary, concise release-integrity summary, and getting-started links. Remove internal fake-CLI/CI descriptions, live-verification disclaimers, and audit-style repetition.

## Copy rules

Remove:

- `live-verified`, `not run`, and optional-live-check status language;
- deterministic fake-CLI and CI implementation details from product copy;
- dated contract-baseline prose;
- historical Gemini transition narration;
- repeated compatibility, entitlement, billing, and provider-behavior disclaimers;
- repeated `exact`, `closed`, `authoritative`, and “not a claim” phrasing when the supported-scope table already establishes the boundary;
- low-level Doctor parsing and provider-probe algorithms from the README.

Retain once, in plain language:

- the gateway implements a Responses API-compatible subset, not the complete OpenAI API;
- unsupported fields return an error;
- users install and authenticate provider CLIs themselves;
- selected CLIs can send request data to upstream providers;
- the gateway does not log prompts, outputs, or credentials;
- this is not an isolation boundary for mutually untrusted users sharing one OS account.

## Verification

- Update repository security tests so executable onboarding validation reads `docs/getting-started.md` rather than requiring the full procedure in `README.md`.
- Keep mutation coverage for checksum, key-handling, ACL, process, and SDK recovery semantics.
- Replace brittle README prose expectations with small structural, link, and required-boundary checks.
- Verify every moved anchor and relative link.
- Run formatting, vet, lint, focused repository/security tests, unit tests, race tests, integration tests, release-package tests, and a static build.
- Confirm no API implementation, provider adapter, release workflow, tag, or release asset changes.

## Publication

Publish through a reviewed PR. After merge and green hosted CI, update the public v0.1.0 body from the simplified tracked release-note source. Do not change the tag, title, release flags, or seven release assets.

