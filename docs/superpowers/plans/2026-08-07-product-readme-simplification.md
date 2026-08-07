# Product-Centered README Simplification Implementation Plan

> **For Codex:** REQUIRED SUB-SKILL: Use `superpowers:executing-plans` to implement this plan task by task. Use `superpowers:test-driven-development` for the repository contract-test changes and `superpowers:verification-before-completion` before every completion claim.

**Goal:** Replace the 897-line audit-style front page with a 200–300-line product README, move executable onboarding and detailed reference material to dedicated documents, and remove public live-verification status language without weakening tested security instructions.

**Architecture:** Keep `README.md` as the product decision and first-request page. Make `docs/getting-started.md` the sealed executable onboarding contract, `docs/reference.md` the detailed API/operations contract, and `CONTRIBUTING.md` the home of maintainer-only live-test mechanics. Repoint the existing high-value executable mutation tests to the onboarding guide, remove README prose change-detector tests, and publish the simplified tracked release note only after a reviewed merge and green hosted CI.

**Tech Stack:** Markdown, Go 1.26.5 repository tests, golangci-lint 2.12.2, Git/GitHub CLI, GitHub Actions.

---

## Guardrails

- Work on branch `docs/product-readme-simplification`, based on merge commit `cf60e83e619d894db9e52e24eb1943cefae88c61`.
- Preserve the user-owned untracked `.idea/` directory and ignored `config.toml`; do not stage, edit, move, or delete either one.
- Do not modify Go product code, provider adapters, workflows, release scripts, the `v0.1.0` tag, release title/flags, or any of the seven release assets.
- Keep all thirteen onboarding code fences byte-for-byte unchanged so their existing SHA-256 source seals and mutation tests retain their value.
- Historical plans and specs remain historical records. Do not rewrite the 2026-08-06 design/plan merely to remove old wording.
- The public product pages may describe supported behavior, but must not publish maintainer run status such as `live-verified`, `not run`, or an optional-live-check disclaimer.
- Do not add a new dependency or a generated documentation site.
- Use `apply_patch` for repository file edits. Formatting tools may perform mechanical rewrites after the semantic edit.
- Do not add persistent tests for human prose, line counts, heading order, or release-note wording. Verify those review decisions during this change with focused shell checks and code review; retain tests only where they execute or semantically parse security-sensitive instructions.

## Target document map

| File | Role | Target content |
|---|---|---|
| `README.md` | Product front page | Hero, SDK example, use cases, supported scope, short first request, essential boundaries, document/release links |
| `docs/getting-started.md` | Executable onboarding | Full POSIX/Windows release verification, private setup, Doctor/serve/request flow, SDK checks, SDK recovery |
| `docs/reference.md` | Technical reference | API subset, schemas, examples/errors, commands, provider configuration, current credentials, operations, containment, sources |
| `CONTRIBUTING.md` | Maintainer workflow | Build/test commands and opt-in live-test mechanics, without publishing whether any maintainer ran them |
| `docs/releases/v0.1.0.md` | Release body source | Product purpose/scope, concise boundaries/integrity, current getting-started links |

### Task 1: Move the sealed onboarding contract to `docs/getting-started.md`

**Files:**

- Create: `docs/getting-started.md`
- Modify: `internal/securitytest/repository_test.go`
- Source: `README.md:77-522`
- Source: `README.md:875-882`

- [ ] **Step 1: Repoint the onboarding tests before creating the guide**

Add this helper near the README contract tests:

```go
func readGettingStarted(t *testing.T) string {
	t.Helper()
	return string(readRepositoryFile(t, "docs/getting-started.md"))
}
```

Then make these focused changes:

- `TestSDKContractRecoveryGuidance` reads `readGettingStarted(t)` and reports `getting-started guide` rather than `README`.
- Rename the public test functions from `TestREADMEReleaseQuickStart`, `TestREADMEWindowsACLProgram`, `TestREADMEReleaseQuickStartRejectsMutations`, `TestREADMEQuickStartSemanticHelpersRejectBypasses`, `TestREADMEQuickStartWholeDocumentBoundaries`, `TestREADMEPOSIXChecksumCommands`, `TestREADMEPOSIXHostSelectorCommands`, `TestREADMEQuickStartTOMLSubstitutionValues`, `TestREADMEWindowsPowerShellFencesNative`, `TestREADMEWindowsChecksumCommandsNative`, `TestREADMEWindowsTOMLValidationFunctionNative`, and `TestREADMEWindowsACLCommandsNative` to the same names with `README` replaced by `GettingStarted`.
- Every one of those tests passes `readGettingStarted(t)` to the existing parser/validator instead of reading `README.md`.
- `extractREADMEQuickStartSource` uses `## SDK contract recovery` as its unique end boundary instead of `## Architecture and scope`; update its error text accordingly.
- Boundary mutation fixtures replace `## SDK contract recovery`, not `## Architecture and scope`.
- Remove the validator's dependency on the README's first two prose paragraphs.
- Remove the exact `subsetSentence` count, the `Responses API-compatible subset` root marker, and the `validateREADMECompatibilityClaims` call from the onboarding validator; the compatibility boundary will live once on the product page.
- Remove the three Quick Start mutation cases whose names begin `subset only` or `subset sentence`, the two standalone compatibility-helper subtests, and the now-unused `validateREADMECompatibilityClaims` helper.
- Remove the `five seconds` SDK-prose marker from the validator because CI fixture timing is not an onboarding requirement.
- Change the active systemd link expectation from `deploy/systemd/ai-cli-gateway.service` to `../deploy/systemd/ai-cli-gateway.service`.
- Update `moveREADMESystemdLink` to use that same `../deploy/systemd/ai-cli-gateway.service` path in its source and mutated Windows prose.
- Keep helper and digest names such as `validateREADMEQuickStartFenceSources` unchanged in this scoped change; renaming thousands of internal references adds no user value.

- [ ] **Step 2: Run the focused tests and confirm RED**

Run:

```bash
go test -count=1 ./internal/securitytest -run '^(TestGettingStarted.*|TestSDKContractRecoveryGuidance)$'
```

Expected: failure because `docs/getting-started.md` does not exist. A compile error or failure for another reason is not the intended RED.

- [ ] **Step 3: Create the onboarding guide**

Create `docs/getting-started.md` through one `apply_patch` edit. Its fixed prologue is:

```markdown
# Getting Started

Install AI CLI Gateway from the v0.1.0 release, connect one authenticated provider CLI, and send a first request.
```

After the prologue, copy the complete source from base commit `cf60e83e619d894db9e52e24eb1943cefae88c61` beginning with `## Quick Start` and ending immediately before `## Architecture and scope`. This is the deterministic source of the Quick Start prose and all thirteen fenced programs. Apply only these prose/link adjustments:

- remove the repeated sentence beginning `It exercises the documented non-streaming`;
- update the systemd link to `../deploy/systemd/ai-cli-gateway.service`;
- remove the sentence `The CI harness overrides the timeout to five seconds for its deterministic fake CLI.`;
- preserve release download URLs and code-block paths exactly;
- append `## SDK contract recovery` followed by the complete source from the base commit beginning after `### SDK contract recovery` and ending immediately before `## Security and terms`, without weakening its owner-only/exact-path warnings;
- finish with this exact tail:

```markdown
## Next steps

- [API and operations reference](reference.md)
- [Security policy](../SECURITY.md)
- [Contributing](../CONTRIBUTING.md)
```

- [ ] **Step 4: Run focused GREEN verification**

Run:

```bash
go test -count=1 ./internal/securitytest -run '^(TestGettingStarted.*|TestSDKContractRecoveryGuidance)$'
rg -n 'readRepositoryFile\(t, "README\.md"\)' internal/securitytest/repository_test.go
git diff --check
```

Expected: all onboarding/recovery tests pass. The `rg` output may contain only product-page/reference tests, never a Quick Start parser call.

- [ ] **Step 5: Commit the onboarding move**

```bash
git add -- docs/getting-started.md internal/securitytest/repository_test.go
git commit -m "docs: move secure onboarding into guide"
```

### Task 2: Create the detailed API and operations reference

**Files:**

- Create: `docs/reference.md`
- Modify: `internal/securitytest/repository_test.go`
- Source: `README.md:523-854`
- Source: `README.md:884-897`

- [ ] **Step 1: Remove README prose change-detector tests**

Delete `TestREADMEOpeningAndOfficialContractSources`, `TestREADMEExactAPISubsetExamplesAndErrors`, and `TestREADMECommandsOperationsSecurityAndGeminiBoundary`. Also remove helper functions used only by those tests after confirming their references with `rg`.

Do not replace them with reference-prose tests. The detailed examples remain reviewable documentation, while executable onboarding stays protected by Task 1's semantic and native execution tests.

- [ ] **Step 2: Run the security-test package after removing dead assertions**

```bash
go test -count=1 ./internal/securitytest -run '^(TestREADME|TestGettingStarted|TestSDKContractRecoveryGuidance)'
```

Expected: existing executable documentation tests pass; no test requires detailed prose to remain in README.

- [ ] **Step 3: Create `docs/reference.md` through `apply_patch`**

Use this exact top-level outline:

```markdown
# API and Operations Reference

## Architecture and endpoint scope
## Request contract
## Portable JSON Schema profile
## Requests and responses
## Build and commands
## Configuration and providers
## Operational defaults
## Shutdown and containment
## Security details
## Official contract sources
```

Move and edit the existing detailed material under those headings:

- preserve endpoint, field, JSON Schema, request/response, model-list, error-envelope, error-catalog, command grammar, version-range, credential-shape, limit, shutdown, and containment facts;
- keep one plain sentence that provider access, quota, billing, and entitlement are determined upstream;
- keep current Gemini credential shapes and disposable-home behavior, but remove consumer-transition history, dates, Antigravity narration, and status labels;
- keep Doctor readiness behavior but remove low-level audit narration that does not help an operator act;
- keep source links without a dated `implementation baseline` preamble;
- use `../config.example.toml`, `../SECURITY.md`, and other correct paths from `docs/`.

- [ ] **Step 4: Review the reference content and links**

```bash
rg -n -i 'live-verified|not run|unassessed|Gemini upstream transition|Antigravity|contract baseline|2026-07-30|2026-08-02' docs/reference.md
rg -n '^## ' docs/reference.md
test -f config.example.toml
test -f SECURITY.md
git diff --check
```

Expected: the forbidden-copy search produces no output, headings match the approved outline, referenced repository files exist, and the diff is clean.

- [ ] **Step 5: Commit the reference move**

```bash
git add -- docs/reference.md internal/securitytest/repository_test.go
git commit -m "docs: add focused API and operations reference"
```

### Task 3: Move maintainer-only live-test mechanics to `CONTRIBUTING.md`

**Files:**

- Modify: `CONTRIBUTING.md`
- Modify: `internal/securitytest/repository_test.go`
- Source: `README.md:855-874`

- [ ] **Step 1: Add a contributor-only `Opt-in provider tests` section**

Move the compile command, two-stage global gates, three provider inference gates, provider-specific executable/config/model/auth variables, disposable-canary warning, and Node24/self-hosted-runner note from README into `CONTRIBUTING.md`.

Use this framing, without recording anyone's execution status:

````markdown
## Opt-in provider tests

Default tests and CI do not use installed provider CLIs or credentials. Compile the opt-in sources without executing them with:

```bash
go test -tags=live -run '^$' ./internal/provider/...
```

Live probes and inference are explicit maintainer operations. Use a dedicated disposable canary; inference may incur provider usage and cost.
````

Keep the existing environment-variable details that an actual contributor needs. Do not include `live-verified`, `not run`, or any claim about whether a maintainer performed a run.

- [ ] **Step 2: Verify the existing contribution/security contract and copy boundary**

```bash
go test -count=1 ./internal/securitytest -run '^TestPublicPolicyContributionSecurityAndIgnoreBoundary$'
rg -n -i 'live-verified|not run|has not been run' CONTRIBUTING.md
rg -n 'AI_CLI_GATEWAY_LIVE_(PROBES|INFERENCE|CODEX_INFERENCE|CLAUDE_INFERENCE|GEMINI_INFERENCE)' CONTRIBUTING.md
git diff --check
```

Expected: the existing policy test passes, the status search produces no output, and the contributor guide contains all five opt-in gates.

- [ ] **Step 3: Commit the contributor documentation**

```bash
git add -- CONTRIBUTING.md internal/securitytest/repository_test.go
git commit -m "docs: move live test mechanics to contributing"
```

### Task 4: Rewrite `README.md` as the product front page

**Files:**

- Modify: `README.md`

- [ ] **Step 1: Replace README with the approved product structure**

Use these top-level headings, in this order:

```markdown
# AI CLI Gateway
## Build AI MVPs with the AI CLI access you already have.
## From SDK to local CLI
## What you can build
## What v0.1.0 supports
## Quick Start
## Security boundaries
## Release integrity
## Documentation
```

Content requirements:

- retain the current badges, exact first product-definition sentence, and one adjacent compatibility boundary;
- retain one concise OpenAI JavaScript SDK example and direct links to the checked-in Python/JavaScript examples, but remove test-harness commentary;
- retain the use-case bullets and the supported/out-of-scope table;
- make Quick Start a short product path: link secure installation/configuration to `docs/getting-started.md`, show `doctor`, show `serve`, write one request body, and send one `curl` request;
- say unsupported fields return a `400` error instead of being ignored, then link the detailed request contract;
- retain authentication ownership, upstream data flow, no-sensitive-content logging, stdin/no-shell, process cleanup, and same-user isolation boundaries once each;
- retain the concise checksum/SBOM/attestation statement;
- finish with links to Getting Started, API/operations reference, security, contributing, and v0.1.0 release notes;
- land between 200 and 300 lines without padding or duplicating the reference.

Do not retain the full platform installers, full response/error catalog, provider version/status table, Doctor parser algorithm, Gemini transition history, operational limit catalog, shutdown internals, live-test mechanics, SDK recovery procedure, or dated source baseline on this page.

- [ ] **Step 2: Review the product page and moved-content separation**

```bash
rg -n -i 'live-verified|not run|optional live|deterministic fake|fake CLI|contract baseline|Gemini upstream transition|2026-07-30|2026-08-02|AI_CLI_GATEWAY_LIVE_' README.md
test "$(wc -l < README.md | tr -d ' ')" -ge 200
test "$(wc -l < README.md | tr -d ' ')" -le 300
rg -n '^## ' README.md
rg -n 'docs/getting-started.md|docs/reference.md|SECURITY.md|CONTRIBUTING.md|releases/tag/v0.1.0' README.md
rg -n 'Responses API-compatible subset|not full OpenAI API compatibility|Unsupported fields return|may send request data to its upstream provider|does not log prompts, model output, or credentials|not an isolation boundary' README.md
git diff --check
```

Expected: the forbidden-copy search produces no output; line count is 200–300; headings, links, and essential boundaries appear once in a readable product page.

- [ ] **Step 3: Commit the product README**

```bash
git add -- README.md
git commit -m "docs: focus README on the product"
```

### Task 5: Simplify the tracked v0.1.0 release body

**Files:**

- Modify: `docs/releases/v0.1.0.md`

- [ ] **Step 1: Simplify the release note**

Keep the current `Why this exists`, `What you can build`, supported/not-supported scope, authentication/data boundary, and release-integrity substance. Make only these content changes:

- delete the paragraph beginning `Adapters are exercised with deterministic fake CLIs`;
- state the unsupported-field behavior once;
- keep the release-integrity section concise while distinguishing seven downloadable assets from separately stored GitHub attestations;
- replace old README-anchor links with absolute GitHub links to `docs/getting-started.md` and `docs/reference.md` on `main`;
- retain security, contributing, release, and changelog links;
- do not add any personal validation record or maintainer test-status statement.

- [ ] **Step 2: Review the release copy and links**

```bash
rg -n -i 'live-verified|not run|optional live|deterministic fake|fake CLI|contract baseline|closed and authoritative|not a claim' docs/releases/v0.1.0.md
rg -n 'Responses API-compatible subset|POST /v1/responses|GET /v1/models|Unsupported request fields|SHA256SUMS|SPDX SBOM|seven assets|docs/getting-started.md|docs/reference.md' docs/releases/v0.1.0.md
git diff --check
```

Expected: the forbidden-copy search produces no output and the product scope, release integrity, and new documentation links remain present.

- [ ] **Step 3: Commit the release note**

```bash
git add -- docs/releases/v0.1.0.md internal/securitytest/repository_test.go
git commit -m "docs: simplify v0.1.0 release notes"
```

### Task 6: Whole-branch verification, review, merge, and publication

**Files:**

- Verify: `README.md`
- Verify: `docs/getting-started.md`
- Verify: `docs/reference.md`
- Verify: `CONTRIBUTING.md`
- Verify: `docs/releases/v0.1.0.md`
- Verify: `internal/securitytest/repository_test.go`
- Publish from: `docs/releases/v0.1.0.md`

- [ ] **Step 1: Run fast local checks in the feature checkout**

```bash
gofmt -w internal/securitytest/repository_test.go
make fmt-check
go vet ./...
GOCACHE=/private/tmp/aicli-product-readme-go-build \
  GOLANGCI_LINT_CACHE=/private/tmp/aicli-product-readme-golangci \
  golangci-lint run ./...
go test -count=1 ./internal/releasepack/...
CGO_ENABLED=0 go build -trimpath -o /private/tmp/ai-cli-gateway-product-readme ./cmd/ai-cli-gateway
git diff --check
```

Expected: all commands pass and lint reports `0 issues`.

- [ ] **Step 2: Audit product copy, links, and scope**

```bash
rg -n -i 'live-verified|not run|optional live|live verification has|deterministic fake|contract baseline|Gemini upstream transition' README.md docs/getting-started.md docs/reference.md docs/releases/v0.1.0.md
test -f docs/getting-started.md
test -f docs/reference.md
test -f SECURITY.md
test -f CONTRIBUTING.md
test -f config.example.toml
test -f deploy/systemd/ai-cli-gateway.service
git diff --name-only origin/main...HEAD
```

Expected: the forbidden-copy search produces no output. The diff contains only the approved design/plan, five public documentation files, and `internal/securitytest/repository_test.go`; it contains no product Go code or workflow changes.

- [ ] **Step 3: Verify the exact committed tree in a safe tracked-file snapshot**

The developer's ignored root `config.toml` intentionally makes `TestRepositoryHygiene` fail in the working checkout. Do not remove it. After all implementation commits, create a private directory under the repository, extract only tracked files from `HEAD`, and run the full matrix there:

```bash
VERIFY_ROOT="$(mktemp -d /Users/krkarma777/Dev/ai-cli-gateway/.product-readme-verify.XXXXXX)"
git archive HEAD | tar -x -C "${VERIFY_ROOT}"
cd "${VERIFY_ROOT}"
GOCACHE=/private/tmp/aicli-product-readme-go-build go test -count=1 ./...
GOCACHE=/private/tmp/aicli-product-readme-go-build go test -race -count=1 ./...
GOCACHE=/private/tmp/aicli-product-readme-go-build go test -tags=integration -count=1 ./...
GOCACHE=/private/tmp/aicli-product-readme-go-build go test -trimpath -count=1 ./...
```

Expected: all packages pass. Record the exact `git rev-parse HEAD` and command results before removing only the exact `${VERIFY_ROOT}` directory after returning to the feature checkout.

- [ ] **Step 4: Request a whole-branch documentation review**

Use `superpowers:requesting-code-review` over `origin/main...HEAD`. Require the reviewer to check:

- product-page clarity and 200–300-line target;
- completeness and correctness of moved onboarding/reference material;
- preservation of all thirteen sealed executable fences;
- absence of public maintainer run-status language;
- current links and relative paths;
- no weakening of authentication, data-flow, logging, unsupported-field, or same-user boundaries;
- no changes to product code, workflow, tag, or assets.

Address findings through `superpowers:receiving-code-review`, rerun the affected focused and whole-tree verification, and commit any accepted fixes separately.

- [ ] **Step 5: Push, open the PR, and wait for hosted CI**

```bash
git push --set-upstream origin docs/product-readme-simplification
gh pr create \
  --repo krkarma777/ai-cli-gateway \
  --base main \
  --head docs/product-readme-simplification \
  --title "docs: focus the README on the product" \
  --body "Make the front page product-centered, move executable onboarding and detailed reference material into dedicated docs, and remove maintainer live-verification status from public product copy without weakening the tested security contract."
gh pr checks --repo krkarma777/ai-cli-gateway --watch --fail-fast
```

Expected: every required Linux, macOS, Windows, lint, cross-build, SDK-contract, and release check passes.

- [ ] **Step 6: Snapshot remote release identity before merging**

Record these values without mutating the release:

```bash
git ls-remote origin refs/tags/v0.1.0
gh release view v0.1.0 --repo krkarma777/ai-cli-gateway \
  --json tagName,name,isDraft,isPrerelease,isImmutable,assets
```

Save the tag object and, for all seven assets, the name, size, digest, and download URL for the post-publication audit.

- [ ] **Step 7: Merge the reviewed PR**

```bash
gh pr merge --repo krkarma777/ai-cli-gateway --merge --delete-branch
gh pr view --repo krkarma777/ai-cli-gateway --json state,mergedAt,mergeCommit,url
```

Expected: PR state is `MERGED` and the merge commit is reachable from `origin/main`.

- [ ] **Step 8: Publish only the tracked release body**

Fetch merged `main`, read the release-note source from that exact remote commit, and update only the release body:

```bash
git fetch origin main
git show origin/main:docs/releases/v0.1.0.md \
  | gh release edit v0.1.0 \
      --repo krkarma777/ai-cli-gateway \
      --notes-file -
```

Do not pass title, tag, latest, draft, prerelease, discussion, or asset flags.

- [ ] **Step 9: Audit public state**

```bash
diff -u \
  <(git show origin/main:docs/releases/v0.1.0.md) \
  <(gh release view v0.1.0 --repo krkarma777/ai-cli-gateway --json body --jq .body)
git ls-remote origin refs/tags/v0.1.0
gh release view v0.1.0 --repo krkarma777/ai-cli-gateway \
  --json tagName,name,isDraft,isPrerelease,isImmutable,assets
curl --disable --fail --silent --show-error --location --head \
  https://github.com/krkarma777/ai-cli-gateway/blob/main/README.md
curl --disable --fail --silent --show-error --location --head \
  https://github.com/krkarma777/ai-cli-gateway/blob/main/docs/getting-started.md
curl --disable --fail --silent --show-error --location --head \
  https://github.com/krkarma777/ai-cli-gateway/blob/main/docs/reference.md
```

Expected: public body matches the tracked source byte-for-byte; tag, title, flags, immutable state, and all seven asset identities match the pre-merge snapshot; all three public document URLs return success.

## Completion evidence

Report:

- PR URL and merge commit;
- final README line count;
- focused documentation/security test results;
- exact-HEAD unit, race, integration, trimpath, lint, vet, releasepack, and build results;
- confirmation that the public release body matches `origin/main:docs/releases/v0.1.0.md`;
- confirmation that the tag and seven release assets are unchanged;
- confirmation that `.idea/` and ignored `config.toml` were untouched.
