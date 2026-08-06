# AI CLI Gateway Open-Source Marketing Refresh Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the README and v0.1.0 release page immediately useful to developers validating AI-service MVPs with their existing locally authenticated AI CLIs, while preserving the project's exact compatibility and security boundaries.

**Architecture:** Add a compact product-introduction layer before the existing sealed README Quick Start. Keep the public v0.1.0 release body in `docs/releases/v0.1.0.md` as a reviewable source of truth, then publish only that body and a reviewed repository description after the documentation branch passes CI and merges.

**Tech Stack:** Markdown, existing Go 1.26.5 repository contract tests, Git, GitHub CLI, GitHub Releases.

## Global Constraints

- The controlling specification is `docs/superpowers/specs/2026-08-06-open-source-marketing-design.md`.
- Keep the exact identity sentence: “AI CLI Gateway turns locally authenticated AI CLIs into an OpenAI Responses-compatible API.”
- Lead with the exact hero: “Build AI MVPs with the AI CLI access you already have.”
- Describe compatibility as a “Responses API-compatible subset”; never claim complete OpenAI API or SDK compatibility.
- Do not use “free API,” “unlimited,” “subscription-to-API,” “billing bypass,” or equivalent cost-avoidance claims in public marketing copy.
- Do not claim that prompts stay local. The gateway and CLI credentials are local, but the selected CLI may send request data to its upstream provider.
- Do not imply that CLI authentication guarantees a model, quota, entitlement, or provider behavior.
- Preserve every existing Quick Start code fence byte-for-byte; the repository security tests seal their contents and semantics.
- Keep the README free of raw HTML. Badges use Markdown image-link syntax.
- Keep the gateway examples on `127.0.0.1`, use only environment-variable key placeholders, and disable SDK retries.
- Do not change API behavior, provider adapters, `internal/securitytest/repository_test.go`, `.github/workflows/release.yml`, the `v0.1.0` tag, or any of the seven immutable release assets.
- State that adapter integration tests use deterministic fake CLIs and that optional live checks do not prove every provider/account/model combination.
- Never add a real gateway key, provider credential, authentication file, prompt, model output, or private filesystem path.
- The final repository description is exactly: “Build and validate AI MVPs with locally authenticated Codex, Claude Code, and Gemini CLIs through an OpenAI Responses-compatible endpoint.”
- Human-facing marketing prose does not receive exact-string tests. Such tests are brittle change detectors; the existing executable README/security contracts, repository hygiene scan, rendered review, and link checks provide the verification boundary.
- Each documentation task ends in its own commit. Public metadata changes happen only after the branch is reviewed, green, and merged.

## File Structure

- `README.md` — hero, badges, SDK proof of life, use cases, capability boundary, and trust summary before the existing Quick Start.
- `docs/releases/v0.1.0.md` — reviewable source of truth for the public v0.1.0 GitHub release body.
- `internal/securitytest/repository_test.go` — unchanged executable README, security, release-workflow, and repository-hygiene contracts.
- `docs/superpowers/specs/2026-08-06-open-source-marketing-design.md` — approved design and messaging guardrails; no implementation edits expected.
- `docs/superpowers/plans/2026-08-06-open-source-marketing-refresh.md` — this execution plan.
- GitHub repository metadata — remotely managed repository description; updated only after merge.
- GitHub release `v0.1.0` body — published from `docs/releases/v0.1.0.md`; tag, title, flags, and assets remain unchanged.

---

### Task 1: Add the README Product Introduction

**Files:**
- Modify: `README.md:1`
- Verify unchanged: `internal/securitytest/repository_test.go`

**Interfaces:**
- Consumes: the existing exact first-two-prose-paragraph contract and the sealed `## Quick Start` section.
- Produces: a first-screen value proposition, SDK example, use cases, scope snapshot, and trust summary without changing executable behavior.

- [ ] **Step 1: Record the clean README contract baseline**

Run:

```bash
go test -count=1 ./internal/securitytest -run '^TestREADME'
```

Expected: PASS before the documentation edit. This is baseline evidence, not a TDD RED: the task changes human-facing prose, not application behavior.

- [ ] **Step 2: Replace the pre-Quick Start preamble with the approved introduction**

Replace only the current pre-Quick Start preamble with the following structure. It reuses the current title, exact identity sentence, exact subset paragraph, and contract-baseline paragraph; do not edit any existing Quick Start fence:

````markdown
# AI CLI Gateway

[![CI](https://github.com/krkarma777/ai-cli-gateway/actions/workflows/ci.yml/badge.svg)](https://github.com/krkarma777/ai-cli-gateway/actions/workflows/ci.yml) [![Release](https://img.shields.io/github/v/release/krkarma777/ai-cli-gateway)](https://github.com/krkarma777/ai-cli-gateway/releases/latest) [![License](https://img.shields.io/github/license/krkarma777/ai-cli-gateway)](LICENSE) [![Go](https://img.shields.io/github/go-mod/go-version/krkarma777/ai-cli-gateway)](go.mod)

## Build AI MVPs with the AI CLI access you already have.

AI CLI Gateway turns locally authenticated AI CLIs into an OpenAI Responses-compatible API.

It deliberately implements a small **Responses API-compatible subset**, not full OpenAI API compatibility. The gateway is a local, final-output bridge with strict validation; it is not a drop-in implementation of every OpenAI endpoint or feature.

Your AI tools already work in the terminal. Your MVP expects an API. AI CLI Gateway bridges that gap locally.

[Get started](#quick-start) · [See the API](#from-sdk-to-local-cli) · [Check the scope](#what-v010-supports) · [Download v0.1.0](https://github.com/krkarma777/ai-cli-gateway/releases/tag/v0.1.0)

## From SDK to local CLI

Point an OpenAI JavaScript SDK client at the loopback gateway and use a configured model alias:

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

This illustrative call assumes the gateway is running and `codex-local` is configured. The checked-in [JavaScript](examples/openai-sdk/javascript/main.mjs) and [Python](examples/openai-sdk/python/main.py) examples are the executable SDK contracts.

## Built for fast validation

- **Vibe-coded AI-service MVPs** — connect application code to an AI CLI without writing provider-specific subprocess logic.
- **Product validation** — test the workflow and response contract before committing to a larger architecture.
- **Demos and hackathons** — expose one predictable local endpoint across supported CLIs.
- **Local SDK integration tests** — exercise the same non-streaming request shape with deterministic model aliases.

## What v0.1.0 supports

| Area | Included |
|---|---|
| Provider adapters | Codex CLI, Claude Code, and Gemini CLI |
| HTTP API | `POST /v1/responses` and `GET /v1/models` |
| Final output | final non-streaming text or strict JSON Schema output |
| Routing | Provider/model aliases configured by the operator |
| Intentionally out of scope | SSE streaming, tool/function-call round trips, stored conversations, web UI, and an external database |

The detailed [request contract](#request-contract) is closed and authoritative: unsupported fields fail with a clear `400` response instead of being ignored.

## Local control, explicit boundaries

- You install and authenticate each official CLI. The gateway does not issue, extract, copy, or store provider login tokens.
- Prompts reach the selected CLI through stdin, never through a shell command string or prompt argument.
- Each request gets a temporary working directory, timeouts, cancellation, process-tree cleanup, bounded queues, and bounded output.
- Operational logs carry request metadata and stable errors; the gateway does not log prompts, model output, or credentials.
- Release downloads include `SHA256SUMS`, an SPDX SBOM, and build-provenance attestations.

The gateway and CLI credential boundary are local. The selected CLI may send request data to its upstream provider. This is not an isolation boundary between mutually untrusted users sharing one OS account.

The contract baseline is 2026-07-30, with the external provider transition notes below rechecked on 2026-08-02. The project supports locally prepared Codex CLI and Claude Code profiles, plus the three documented Gemini environment/external credential shapes; actual provider access remains an upstream decision.
````

- [ ] **Step 3: Verify the README contracts and unchanged Quick Start**

Run:

```bash
go test -count=1 ./internal/securitytest -run '^TestREADME'
diff -u <(git show origin/main:README.md | sed -n '/^## Quick Start$/,$p') <(sed -n '/^## Quick Start$/,$p' README.md)
sed -n '1,100p' README.md
```

Expected: the Go suite passes, `diff` prints nothing, and the first 100 rendered source lines show the hero, SDK request, four use cases, scope, trust boundary, and transition into Quick Start.

- [ ] **Step 4: Commit Task 1**

Run:

```bash
git diff --check
git diff -- README.md
git add -- README.md
git commit -m "docs: sharpen README for AI MVP builders"
```

Expected: only `README.md` is committed.

---

### Task 2: Write the v0.1.0 Launch Notes

**Files:**
- Create: `docs/releases/v0.1.0.md`
- Verify unchanged: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: the approved public messaging and the immutable public `v0.1.0` release with five platform archives, one SPDX SBOM, and one `SHA256SUMS` asset.
- Produces: a Git-reviewed release-body source; `gh release edit --notes-file` consumes the Markdown file after merge.

- [ ] **Step 1: Record the current release-note baseline**

Run:

```bash
test ! -e docs/releases/v0.1.0.md
gh release view v0.1.0 --repo krkarma777/ai-cli-gateway --json body,url,assets
```

Expected: the tracked source does not exist and the public body contains only generated change/contributor notes while the release has seven assets.

- [ ] **Step 2: Create the exact reviewable release body**

Create `docs/releases/v0.1.0.md` with this content:

````markdown
# AI CLI Gateway v0.1.0

**Build AI MVPs with the AI CLI access you already have.**

AI CLI Gateway turns locally authenticated AI CLIs into an OpenAI Responses-compatible API. v0.1.0 is a focused, local/self-hosted **Responses API-compatible subset** for fast prototyping, demos, and product validation.

## Why this exists

Your AI tools already work in the terminal. Your MVP expects an API. AI CLI Gateway bridges that gap locally: application code calls one loopback endpoint, while configured provider/model aliases route each request to Codex CLI, Claude Code, or Gemini CLI.

## What you can build

- **Vibe-coded AI-service MVPs** with a familiar HTTP boundary.
- **Product validation** against final text or structured JSON output.
- **Demos and hackathons** that can switch configured CLI backends without rewriting the client.
- **Local integration testing** with OpenAI Python or JavaScript SDK request shapes.

## Highlights

- `POST /v1/responses` for one completed, non-streaming response.
- `GET /v1/models` for configured model aliases.
- `instructions`, string `input`, `model`, text output, and strict JSON Schema output.
- Codex CLI, Claude Code, and Gemini CLI adapters behind a provider-neutral interface.
- `doctor` diagnostics for binary, version, capability, authentication-readiness, containment, and runtime checks.
- Per-provider concurrency limits, bounded queues and output, request timeouts and cancellation, and child process-tree cleanup.
- Stable API errors for unsupported parameters, provider failures, timeouts, malformed output, and schema mismatch.

Adapters are exercised with deterministic fake CLIs in integration tests. Optional live checks are operator-triggered and this release does not claim live verification for every provider, account, model, or entitlement combination.

## Intentionally not in v0.1.0

- SSE streaming.
- Tool/function-call round trips.
- Stored conversations or gateway sessions.
- Web UI or external database.
- Complete OpenAI endpoint or SDK compatibility.

Unsupported request fields fail with an explicit `400` response instead of being silently ignored.

## Authentication and data boundary

You install and authenticate each official CLI. AI CLI Gateway does not issue, extract, copy, or store provider login tokens. Prompts are sent to the selected CLI over stdin, never through a shell command string or prompt argument. Sensitive prompts, model output, credentials, and authentication contents are excluded from gateway logs.

The gateway and CLI credential boundary are local. The selected CLI may send request data to its upstream provider. Users remain responsible for their provider access and applicable terms.

## Release integrity

The immutable release contains five platform archives plus:

- `SHA256SUMS` covering every archive and the SBOM.
- An SPDX SBOM for the five shipped binaries.
- GitHub build-provenance attestations produced by the pinned release workflow.

## Get started

- [Secure Quick Start](https://github.com/krkarma777/ai-cli-gateway#quick-start)
- [OpenAI SDK example](https://github.com/krkarma777/ai-cli-gateway#from-sdk-to-local-cli)
- [Architecture and supported scope](https://github.com/krkarma777/ai-cli-gateway#architecture-and-scope)
- [Security policy](https://github.com/krkarma777/ai-cli-gateway/blob/main/SECURITY.md)
- [Contributing](https://github.com/krkarma777/ai-cli-gateway/blob/main/CONTRIBUTING.md)
- [Full v0.1.0 changelog](https://github.com/krkarma777/ai-cli-gateway/commits/v0.1.0)
````

- [ ] **Step 3: Verify hygiene, copy boundaries, and public links**

Run:

```bash
go test -count=1 ./internal/securitytest -run '^TestRepositoryHygiene$'
sed -n '1,220p' docs/releases/v0.1.0.md
rg -n -i 'free api|unlimited|subscription-to-api|billing bypass|prompts stay local|full openai api compatibility|drop-in replacement' README.md docs/releases/v0.1.0.md
curl --disable --fail --silent --show-error --location --head https://github.com/krkarma777/ai-cli-gateway/releases/tag/v0.1.0
curl --disable --fail --silent --show-error --location --head https://github.com/krkarma777/ai-cli-gateway/blob/main/SECURITY.md
curl --disable --fail --silent --show-error --location --head https://github.com/krkarma777/ai-cli-gateway/blob/main/CONTRIBUTING.md
curl --disable --fail --silent --show-error --location --head https://github.com/krkarma777/ai-cli-gateway/commits/v0.1.0
```

Expected: hygiene passes; the rendered source contains every approved section; `rg` exits 1 with no matches; every `curl` exits zero.

- [ ] **Step 4: Commit Task 2**

Run:

```bash
git diff --check
git diff -- docs/releases/v0.1.0.md
git add -- docs/releases/v0.1.0.md
git commit -m "docs: write v0.1.0 launch notes"
```

Expected: only `docs/releases/v0.1.0.md` is committed.

---

### Task 3: Verify the Complete Documentation Change

**Files:**
- Verify: `README.md`
- Verify: `docs/releases/v0.1.0.md`
- Verify unchanged: `internal/securitytest/repository_test.go`
- Verify unchanged: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: the README and release-note source produced by Tasks 1 and 2.
- Produces: a review-ready branch with no formatting, security, unit, race, integration, lint, build, documentation-contract, or release-package regressions.

- [ ] **Step 1: Run focused repository documentation tests**

Run:

```bash
go test -count=1 ./internal/securitytest
go test -count=1 ./internal/releasepack/...
```

Expected: PASS.

- [ ] **Step 2: Run the complete local verification chain**

Run each command separately:

```bash
make fmt-check
go vet ./...
golangci-lint run ./...
go test -count=1 ./...
go test -race -count=1 ./...
go test -tags=integration -count=1 ./...
CGO_ENABLED=0 go build -trimpath -o /private/tmp/ai-cli-gateway-marketing ./cmd/ai-cli-gateway
git diff --check
```

Expected: every command exits zero. No real provider CLI or live inference is invoked.

- [ ] **Step 3: Verify the rendered hierarchy and unchanged sealed content**

Run:

```bash
sed -n '1,100p' README.md
sed -n '1,220p' docs/releases/v0.1.0.md
rg -n '^## (From SDK to local CLI|Built for fast validation|What v0\.1\.0 supports|Local control, explicit boundaries|Quick Start|Architecture and scope|Request contract)$' README.md
diff -u <(git show origin/main:README.md | sed -n '/^## Quick Start$/,$p') <(sed -n '/^## Quick Start$/,$p' README.md)
git diff --exit-code origin/main -- internal/securitytest/repository_test.go .github/workflows/release.yml
```

Expected: the hierarchy is complete, the Quick Start diff is empty, and the repository contract test plus release workflow are unchanged.

- [ ] **Step 4: Request independent specification and quality reviews**

Use `superpowers:requesting-code-review` twice:

- first reviewer checks exact coverage of `docs/superpowers/specs/2026-08-06-open-source-marketing-design.md` and public-claim accuracy;
- second reviewer checks readability, SDK example correctness, link targets, and preservation of the sealed Quick Start.

Expected: no unresolved critical, high, or medium findings. A valid copy or link correction receives the smallest documentation-only patch, its applicable focused tests, and a separate `docs: correct marketing documentation` commit.

---

### Task 4: Merge and Publish the Reviewed Marketing Copy

**Files:**
- Publish from: `docs/releases/v0.1.0.md`
- Remote metadata: `krkarma777/ai-cli-gateway` description
- Remote release body: `krkarma777/ai-cli-gateway` release `v0.1.0`

**Interfaces:**
- Consumes: the verified branch, protected GitHub `main`, the reviewable release-note source, and the existing immutable v0.1.0 release.
- Produces: merged README and release-note source, a matching public release body, and the exact reviewed repository description without changing the release tag or assets.

- [ ] **Step 1: Push the documentation branch and open the PR**

Run:

```bash
git push --set-upstream origin docs/marketing-readme-release-notes
gh pr create --repo krkarma777/ai-cli-gateway --base main --head docs/marketing-readme-release-notes --title "docs: sharpen AI CLI Gateway launch messaging" --body "Refresh the README for AI MVP builders, add reviewed v0.1.0 launch notes, and preserve the exact Responses-compatible subset and security boundaries."
```

Expected: one public PR containing the design, plan, README, and release-note source.

- [ ] **Step 2: Require green hosted checks and merge**

Run:

```bash
gh pr checks --repo krkarma777/ai-cli-gateway --watch --fail-fast
gh pr merge --repo krkarma777/ai-cli-gateway --merge
git fetch origin main
git merge-base --is-ancestor HEAD origin/main
```

Expected: all required checks pass, the PR is merged, and every local task commit is an ancestor of `origin/main`.

- [ ] **Step 3: Capture the immutable release boundary before editing the body**

In one terminal session, capture these two public JSON values in task-specific variables for the duration of the publication step:

```bash
AICLI_MARKETING_TAG_BEFORE="$(gh api repos/krkarma777/ai-cli-gateway/git/ref/tags/v0.1.0 --jq '{ref,object}')"
AICLI_MARKETING_ASSETS_BEFORE="$(gh api repos/krkarma777/ai-cli-gateway/releases/tags/v0.1.0 --jq '[.assets[] | {id,name,size,digest}] | sort_by(.name)')"
export AICLI_MARKETING_TAG_BEFORE AICLI_MARKETING_ASSETS_BEFORE
test "$(gh api repos/krkarma777/ai-cli-gateway/releases/tags/v0.1.0 --jq '.assets | length')" = 7
```

Expected: the tag reference resolves to the existing tag object and the release has exactly seven assets: five platform archives, one SPDX SBOM, and `SHA256SUMS`.

- [ ] **Step 4: Publish only the release body and repository description**

Run:

```bash
gh release edit v0.1.0 --repo krkarma777/ai-cli-gateway --notes-file docs/releases/v0.1.0.md
gh repo edit krkarma777/ai-cli-gateway --description 'Build and validate AI MVPs with locally authenticated Codex, Claude Code, and Gemini CLIs through an OpenAI Responses-compatible endpoint.'
```

Do not pass release flags for tag, target, title, draft, prerelease, or assets.

Expected: both commands exit zero.

- [ ] **Step 5: Verify public copy and prove the immutable boundary did not move**

Continue in the same terminal session and run:

```bash
gh release view v0.1.0 --repo krkarma777/ai-cli-gateway --json body --jq .body | diff -u docs/releases/v0.1.0.md -
test "$(gh repo view krkarma777/ai-cli-gateway --json description --jq .description)" = 'Build and validate AI MVPs with locally authenticated Codex, Claude Code, and Gemini CLIs through an OpenAI Responses-compatible endpoint.'
test "$(gh api repos/krkarma777/ai-cli-gateway/git/ref/tags/v0.1.0 --jq '{ref,object}')" = "${AICLI_MARKETING_TAG_BEFORE}"
test "$(gh api repos/krkarma777/ai-cli-gateway/releases/tags/v0.1.0 --jq '[.assets[] | {id,name,size,digest}] | sort_by(.name)')" = "${AICLI_MARKETING_ASSETS_BEFORE}"
gh api repos/krkarma777/ai-cli-gateway/releases/tags/v0.1.0 --jq '{tag_name,draft,prerelease,immutable,asset_count:(.assets|length)}'
```

Expected final release state:

```json
{
  "tag_name": "v0.1.0",
  "draft": false,
  "prerelease": false,
  "immutable": true,
  "asset_count": 7
}
```

- [ ] **Step 6: Report the public result**

Report:

- the merged PR URL and main commit;
- changed files and the exact messaging decision;
- focused and complete local verification results plus hosted CI status;
- the public repository and v0.1.0 release URLs;
- confirmation that the tag and seven release assets remained unchanged.
