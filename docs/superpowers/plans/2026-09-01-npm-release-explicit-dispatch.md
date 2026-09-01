# Explicit npm Release Dispatch Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Recover the unpublished npm 0.2.1 cohort from the existing immutable v0.2.1 GitHub Release and make future GitHub Release workflows explicitly dispatch the npm workflow.

**Architecture:** Replace the suppressed release-published trigger with one closed workflow_dispatch(tag) input whose first job re-establishes authority from live GitHub tag and immutable-release data. Add a separately tested post-publication step to release.yml that observes immutable state and dispatches the npm workflow at the release tag; retain a version-scoped main exception only for the already-created v0.2.1 tag.

**Tech Stack:** GitHub Actions YAML, Bash, GitHub CLI and REST API, Go repository-security tests with yaml.v3, Node.js 24.13.0, npm 11.6.2 in CI, npm 11.19.1 for trust administration.

## Global Constraints

- Approved design: docs/superpowers/specs/2026-08-31-npm-release-explicit-dispatch-design.md.
- Do not recreate, move, edit, or delete tag v0.2.1 or its immutable GitHub Release.
- The npm workflow remains version-locked to tag v0.2.1 and package version 0.2.1 in this recovery.
- The npm workflow has exactly one trigger: workflow_dispatch with exactly one required string input, tag, and no default.
- The normal workflow source ref is refs/tags/<input tag>; the only branch exception is refs/heads/main with input v0.2.1.
- Package content authority comes from the peeled live tag, immutable release, seven closed assets, GitHub attestations, deterministic rebuild, package descriptor, and registry SRI—not from the dispatch caller.
- Preserve Ubuntu 24.04, Go 1.26.5, Node 24.13.0, npm 11.6.2, SHA-pinned actions, native-first publication, launcher-last publication, and --provenance.
- Add actions: write only to the GitHub Release publish job; keep all existing permissions unchanged elsewhere.
- NPM_TOKEN remains referenced only by the npm publish step until all six trust relationships are verified.
- Do not dispatch npm publication before the workflow fix is merged and the exact live main SHA is CI-green.
- Do not perform local npm publish; do not add a PAT or GitHub App credential.
- Use GitHub API version 2026-03-10 for immutable-release and Git-ref checks.

## File Structure

- Modify .github/workflows/npm-release.yml: declare the closed explicit trigger and validate dispatch source, live tag, and immutable release before existing packaging steps.
- Modify .github/workflows/release.yml: grant one job actions: write, then verify immutable publication and dispatch the npm workflow at the tag ref.
- Modify internal/securitytest/repository_test.go: enforce both workflow contracts, reject mutations, and execute the new shell boundaries against closed fake-GitHub fixtures.
- Modify docs/superpowers/specs/2026-08-31-npm-release-explicit-dispatch-design.md: record written-spec approval only.
- Create docs/superpowers/plans/2026-09-01-npm-release-explicit-dispatch.md: this implementation and rollout plan.

---

### Task 1: Close and implement the npm dispatch entry point

**Files:**
- Modify: internal/securitytest/repository_test.go:7029-7620
- Modify: .github/workflows/npm-release.yml:1-113

**Interfaces:**
- Consumes: github.event_name, github.repository, github.ref, github.sha, inputs.tag, and read-only github.token.
- Produces: step outputs tag, version, tag_commit, and release_id; downstream checkout continues to consume tag_commit.

- [ ] **Step 1: Change the closed parser and expectations before changing YAML**

Replace validateNPMReleaseTrigger with:

~~~go
func validateNPMReleaseTrigger(node *yaml.Node) error {
	trigger, err := closedYAMLMapping(node, "workflow_dispatch")
	if err != nil {
		return fmt.Errorf("trigger: %w", err)
	}
	dispatch, err := closedYAMLMapping(trigger["workflow_dispatch"], "inputs")
	if err != nil {
		return fmt.Errorf("workflow_dispatch trigger: %w", err)
	}
	inputs, err := closedYAMLMapping(dispatch["inputs"], "tag")
	if err != nil {
		return fmt.Errorf("workflow_dispatch inputs: %w", err)
	}
	tag, err := closedYAMLScalarMap(inputs["tag"], "description", "required", "type")
	if err != nil {
		return fmt.Errorf("workflow_dispatch tag input: %w", err)
	}
	want := map[string]string{
		"description": "Immutable release tag to publish",
		"required":    "true",
		"type":        "string",
	}
	if !reflect.DeepEqual(tag, want) {
		return fmt.Errorf("workflow_dispatch tag input = %v, want %v", tag, want)
	}
	return nil
}
~~~

Change validateNPMReleaseConcurrency to require:

~~~go
want := map[string]string{
	"group":              "npm-release-${{ github.repository }}-${{ inputs.tag }}",
	"cancel-in-progress": "false",
}
~~~

Change the first package-step environment in validateNPMReleaseStepShapes to:

~~~go
map[string]string{
	"GH_TOKEN":         "${{ github.token }}",
	"EVENT_NAME":       "${{ github.event_name }}",
	"EVENT_REPOSITORY": "${{ github.repository }}",
	"EVENT_REF":        "${{ github.ref }}",
	"EVENT_SHA":        "${{ github.sha }}",
	"INPUT_TAG":        "${{ inputs.tag }}",
}
~~~

Add validateNPMReleaseDispatchMetadataContract and call it from validateNPMReleaseWorkflowContract before the run-hash check. It must require unique executable lines for the event, repository, fixed tag, source-ref cases, live tag peeling, immutable release lookup by tag, recovery main-head equality, tag-source SHA equality, and all four output names.

- [ ] **Step 2: Add trigger and authority mutation cases**

Replace the obsolete event-widening mutation with exact mutations for:

~~~go
{name: "release trigger restored", mutate: replaceNPMReleaseOnce("  workflow_dispatch:\n", "  release:\n")},
{name: "tag input optional", mutate: replaceNPMReleaseOnce("        required: true\n", "        required: false\n")},
{name: "tag input defaulted", mutate: replaceNPMReleaseOnce("        required: true\n", "        required: true\n        default: v0.2.1\n")},
{name: "tag input type changed", mutate: replaceNPMReleaseOnce("        type: string\n", "        type: choice\n")},
{name: "extra dispatch input", mutate: replaceNPMReleaseOnce("      tag:\n", "      attacker:\n        description: attacker\n        required: false\n        type: string\n      tag:\n")},
{name: "dispatch event guard removed", mutate: replaceNPMReleaseOnce("          test \"${EVENT_NAME}\" = workflow_dispatch\n", "")},
{name: "dispatch repository guard removed", mutate: replaceNPMReleaseOnce("          test \"${EVENT_REPOSITORY}\" = \"${repository}\"\n", "")},
{name: "recovery branch widened", mutate: replaceNPMReleaseOnce("refs/heads/main)", "refs/heads/*)")},
{name: "recovery main head guard removed", mutate: replaceNPMReleaseOnce("          test \"${live_main}\" = \"${EVENT_SHA}\"\n", "")},
{name: "tag source SHA guard removed", mutate: replaceNPMReleaseOnce("          test \"${EVENT_SHA}\" = \"${tag_commit}\"\n", "")},
{name: "release lookup changed to id input", mutate: replaceNPMReleaseOnce("releases/tags/${INPUT_TAG}", "releases/${INPUT_TAG}")},
~~~

Keep all existing immutable flag, fixed tag, assets, attestations, archives, descriptor, native-first, launcher-last, provenance, and SRI mutations.

- [ ] **Step 3: Run focused tests and record RED**

~~~bash
go test -count=1 ./internal/securitytest \
  -run '^(TestNPMReleaseWorkflowContract|TestNPMReleaseWorkflowContractRejectsMutations|TestNPMReleaseWorkflowBashSyntax)$'
~~~

Expected: FAIL because the checked-in workflow still exposes release: published, uses github.event.release.tag_name for concurrency, and consumes release-event fields.

- [ ] **Step 4: Replace only the npm trigger, concurrency value, and metadata step**

Use this exact trigger:

~~~yaml
on:
  workflow_dispatch:
    inputs:
      tag:
        description: Immutable release tag to publish
        required: true
        type: string

permissions: {}

concurrency:
  group: npm-release-${{ github.repository }}-${{ inputs.tag }}
  cancel-in-progress: false
~~~

Use this exact first-step environment:

~~~yaml
      - name: Validate immutable release metadata
        id: metadata
        shell: bash
        env:
          GH_TOKEN: ${{ github.token }}
          EVENT_NAME: ${{ github.event_name }}
          EVENT_REPOSITORY: ${{ github.repository }}
          EVENT_REF: ${{ github.ref }}
          EVENT_SHA: ${{ github.sha }}
          INPUT_TAG: ${{ inputs.tag }}
~~~

The decoded Bash starts with this closed preflight:

~~~bash
set -euo pipefail
umask 077
readonly repository=krkarma777/ai-cli-gateway
readonly commit_pattern='^[0-9a-f]{40}$'
readonly release_id_pattern='^[1-9][0-9]*$'
test "${EVENT_NAME}" = workflow_dispatch
test "${EVENT_REPOSITORY}" = "${repository}"
test "${INPUT_TAG}" = v0.2.1
[[ "${EVENT_SHA}" =~ ${commit_pattern} ]]
case "${EVENT_REF}" in
  "refs/tags/${INPUT_TAG}") readonly source_mode=tag ;;
  refs/heads/main) readonly source_mode=recovery ;;
  *) exit 1 ;;
esac
command -v gh >/dev/null
command -v jq >/dev/null
gh --version >/dev/null
jq --version >/dev/null
~~~

Use this exact resolver, including one-level annotated-tag peeling:

~~~bash
resolve_live_tag() {
  local ref_response object_type object_sha tag_response resolved
  ref_response="$(gh api \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2026-03-10' \
    "repos/${repository}/git/ref/tags/${INPUT_TAG}")" || return 1
  jq -e --arg ref "refs/tags/${INPUT_TAG}" '
    type == "object" and
    .ref == $ref and
    (.object | type == "object") and
    (.object.type == "commit" or .object.type == "tag") and
    (.object.sha | type == "string" and test("^[0-9a-f]{40}$"))
  ' >/dev/null <<<"${ref_response}" || return 1
  object_type="$(jq -r '.object.type' <<<"${ref_response}")" || return 1
  object_sha="$(jq -r '.object.sha' <<<"${ref_response}")" || return 1
  if test "${object_type}" = commit; then
    printf '%s\n' "${object_sha}"
    return 0
  fi
  tag_response="$(gh api \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2026-03-10' \
    "repos/${repository}/git/tags/${object_sha}")" || return 1
  jq -e --arg requested "${object_sha}" '
    type == "object" and
    .sha == $requested and
    (.object | type == "object") and
    .object.type == "commit" and
    (.object.sha | type == "string" and test("^[0-9a-f]{40}$"))
  ' >/dev/null <<<"${tag_response}" || return 1
  resolved="$(jq -r '.object.sha' <<<"${tag_response}")" || return 1
  [[ "${resolved}" =~ ${commit_pattern} ]] || return 1
  printf '%s\n' "${resolved}"
}

release_response="$(gh api \
  -H 'Accept: application/vnd.github+json' \
  -H 'X-GitHub-Api-Version: 2026-03-10' \
  "repos/${repository}/releases/tags/${INPUT_TAG}")"
~~~

Require the release response with:

~~~jq
type == "object" and
(.id | type == "number" and . > 0 and floor == .) and
(.tag_name == $tag) and
(.draft == false) and
(.prerelease == false) and
(.immutable == true) and
(.published_at | type == "string" and length > 0) and
(.assets | type == "array" and length == 7)
~~~

After resolving tag_commit, validate the invocation source exactly:

~~~bash
if test "${source_mode}" = tag; then
  test "${EVENT_SHA}" = "${tag_commit}"
else
  test "${INPUT_TAG}" = v0.2.1
  main_response="$(gh api \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2026-03-10' \
    "repos/${repository}/git/ref/heads/main")"
  jq -e --arg sha "${EVENT_SHA}" '
    type == "object" and
    .ref == "refs/heads/main" and
    (.object | type == "object") and
    .object.type == "commit" and
    .object.sha == $sha
  ' >/dev/null <<<"${main_response}"
  live_main="$(jq -r '.object.sha' <<<"${main_response}")"
  test "${live_main}" = "${EVENT_SHA}"
fi
release_id="$(jq -r '.id' <<<"${release_response}")"
[[ "${release_id}" =~ ${release_id_pattern} ]]
{
  printf 'tag=%s\n' "${INPUT_TAG}"
  printf 'version=%s\n' "${INPUT_TAG#v}"
  printf 'tag_commit=%s\n' "${tag_commit}"
  printf 'release_id=%s\n' "${release_id}"
} >> "${GITHUB_OUTPUT}"
~~~

Do not change later packaging or publication. The existing Validate toolchain and source step still fetches live main and requires the tag commit to be its ancestor.

- [ ] **Step 5: Recompute only the reviewed metadata digest and run GREEN**

Review the decoded script diff, then replace only the package/Validate immutable release metadata entry in validateNPMReleaseRunHashes with the SHA-256 emitted by the test for that reviewed script.

~~~bash
gofmt -w internal/securitytest/repository_test.go
go test -count=1 ./internal/securitytest \
  -run '^(TestNPMReleaseWorkflowContract|TestNPMReleaseWorkflowContractRejectsMutations|TestNPMReleaseWorkflowBashSyntax)$'
git diff --check
~~~

Expected: all three tests PASS and git diff --check emits no output.

- [ ] **Step 6: Commit the closed npm entry point**

~~~bash
git add .github/workflows/npm-release.yml internal/securitytest/repository_test.go
git commit -m "fix: add explicit npm release dispatch"
~~~

Expected: one commit containing only the npm workflow and its contract.

---

### Task 2: Dispatch npm only after a future release is immutable

**Files:**
- Modify: internal/securitytest/repository_test.go:7708-8170,9674-10540
- Modify: .github/workflows/release.yml:412-575

**Interfaces:**
- Consumes: needs.package.outputs.tag, needs.package.outputs.tag_commit, github.token, live Git refs, and live immutable-release metadata.
- Produces: exactly one workflow_dispatch for npm-release.yml at ref TAG with input tag=TAG.

- [ ] **Step 1: Add failing permission, step-shape, and ordering contracts**

Permit the actions key only in parseReleaseWorkflowJob for publish, and require:

~~~go
map[string]string{"contents": "write", "actions": "write"}
~~~

Change the release publish step count from four to five and append this exact shape to wantPublish:

~~~go
{
	name: "Dispatch immutable npm release",
	env: map[string]string{
		"GH_TOKEN":   "${{ github.token }}",
		"TAG":        "${{ needs.package.outputs.tag }}",
		"TAG_COMMIT": "${{ needs.package.outputs.tag_commit }}",
	},
}
~~~

Add validateReleaseNPMDispatchStep after validation of Publish verified release. Require exact ordered markers for the live-tag resolver, bounded attempts 1 through 5, immutable flag, seven-asset allowlist, second tag check, and final gh workflow run.

- [ ] **Step 2: Add mutation and executable fixture coverage**

Add mutations rejecting:

- removed or widened actions: write;
- a changed workflow filename;
- --ref main or another branch;
- a changed input name or value;
- dispatch before immutable validation;
- removed immutable, digest, uploaded-state, or post-query tag checks;
- a sixth retry or removal of the fifth-attempt failure.

Add TestReleaseNPMDispatchScript with a fake gh and a fake sleep logger:

~~~go
[]struct {
	name, fixture string
	wantDispatch int
	wantSleeps   int
	wantOK       bool
}{
	{name: "lightweight immutable success", fixture: "dispatch_lightweight", wantDispatch: 1, wantOK: true},
	{name: "annotated immutable success", fixture: "dispatch_annotated", wantDispatch: 1, wantOK: true},
	{name: "immutable after two retries", fixture: "dispatch_eventual", wantDispatch: 1, wantSleeps: 2, wantOK: true},
	{name: "never immutable", fixture: "dispatch_never_immutable", wantSleeps: 4},
	{name: "wrong asset set", fixture: "dispatch_wrong_assets"},
	{name: "missing asset digest", fixture: "dispatch_missing_digest"},
	{name: "tag changed before dispatch", fixture: "dispatch_tag_changed"},
}
~~~

For success, require the only non-help workflow call to be:

~~~go
[]string{"workflow", "run", "npm-release.yml", "--repo", "krkarma777/ai-cli-gateway", "--ref", "v0.1.0", "-f", "tag=v0.1.0"}
~~~

For failures, require no workflow run call. Fake sleep records calls and returns immediately.

- [ ] **Step 3: Run release tests and record RED**

~~~bash
go test -count=1 ./internal/securitytest \
  -run '^(TestReleaseWorkflowContract|TestReleaseWorkflowContractRejectsMutations|TestReleasePublicationScript|TestReleaseNPMDispatchScript)$'
~~~

Expected: FAIL because publish lacks actions: write and the fifth dispatch step.

- [ ] **Step 4: Add least privilege and the separate dispatch step**

Change only the publish permission block:

~~~yaml
    permissions:
      contents: write
      actions: write
~~~

Append Dispatch immutable npm release after Publish verified release. Its script must perform this exact state transition:

1. Validate canonical TAG, 40-hex TAG_COMMIT, fixed repository, gh, jq, sort, and sleep.
2. Resolve and peel the live tag; require TAG_COMMIT.
3. Poll exactly five times over repos/${repository}/releases/tags/${TAG}, sleeping two seconds only between invalid attempts.
4. Accept only non-draft, non-prerelease, published, immutable metadata with exactly seven uploaded, nonempty assets and sha256:<64 lowercase hex> digests.
5. Compare sorted API names with:

~~~text
SHA256SUMS
ai-cli-gateway_${VERSION}_darwin_amd64.tar.gz
ai-cli-gateway_${VERSION}_darwin_arm64.tar.gz
ai-cli-gateway_${VERSION}_linux_amd64.tar.gz
ai-cli-gateway_${VERSION}_linux_arm64.tar.gz
ai-cli-gateway_${VERSION}_sbom.spdx.json
ai-cli-gateway_${VERSION}_windows_amd64.zip
~~~

6. Resolve the live tag again and require TAG_COMMIT.
7. Dispatch exactly:

~~~bash
gh workflow run npm-release.yml \
  --repo "${repository}" \
  --ref "${TAG}" \
  -f "tag=${TAG}"
~~~

Derive VERSION="${TAG#v}" only after canonical tag validation. On the fifth invalid response, print only npm_dispatch_preflight_invalid and exit nonzero.

Implement the complete step with this closed shell body; use the same resolver
body as Task 1 with TAG in place of INPUT_TAG:

~~~bash
set -euo pipefail
umask 077
readonly repository=krkarma777/ai-cli-gateway
readonly commit_pattern='^[0-9a-f]{40}$'
[[ "${TAG}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
[[ "${TAG_COMMIT}" =~ ${commit_pattern} ]]
readonly VERSION="${TAG#v}"
[[ "${VERSION}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
command -v gh >/dev/null
command -v jq >/dev/null
command -v sort >/dev/null
command -v sleep >/dev/null
gh --version >/dev/null
jq --version >/dev/null
gh workflow run --help >/dev/null

resolve_live_tag() {
  local ref_response object_type object_sha tag_response resolved
  ref_response="$(gh api \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2026-03-10' \
    "repos/${repository}/git/ref/tags/${TAG}")" || return 1
  jq -e --arg ref "refs/tags/${TAG}" '
    type == "object" and
    .ref == $ref and
    (.object | type == "object") and
    (.object.type == "commit" or .object.type == "tag") and
    (.object.sha | type == "string" and test("^[0-9a-f]{40}$"))
  ' >/dev/null <<<"${ref_response}" || return 1
  object_type="$(jq -r '.object.type' <<<"${ref_response}")" || return 1
  object_sha="$(jq -r '.object.sha' <<<"${ref_response}")" || return 1
  if test "${object_type}" = commit; then
    printf '%s\n' "${object_sha}"
    return 0
  fi
  tag_response="$(gh api \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2026-03-10' \
    "repos/${repository}/git/tags/${object_sha}")" || return 1
  jq -e --arg requested "${object_sha}" '
    type == "object" and
    .sha == $requested and
    (.object | type == "object") and
    .object.type == "commit" and
    (.object.sha | type == "string" and test("^[0-9a-f]{40}$"))
  ' >/dev/null <<<"${tag_response}" || return 1
  resolved="$(jq -r '.object.sha' <<<"${tag_response}")" || return 1
  [[ "${resolved}" =~ ${commit_pattern} ]] || return 1
  printf '%s\n' "${resolved}"
}

live_commit="$(resolve_live_tag)"
test "${live_commit}" = "${TAG_COMMIT}"
release_response=
for attempt in 1 2 3 4 5; do
  if candidate="$(gh api \
    -H 'Accept: application/vnd.github+json' \
    -H 'X-GitHub-Api-Version: 2026-03-10' \
    "repos/${repository}/releases/tags/${TAG}" 2>/dev/null)" &&
    jq -e --arg tag "${TAG}" '
      type == "object" and
      (.id | type == "number" and . > 0 and floor == .) and
      (.tag_name == $tag) and
      (.draft == false) and
      (.prerelease == false) and
      (.immutable == true) and
      (.published_at | type == "string" and length > 0) and
      (.assets | type == "array" and length == 7) and
      (.assets | all(.[];
        (.name | type == "string" and length > 0) and
        (.size | type == "number" and . > 0 and floor == .) and
        (.digest | type == "string" and test("^sha256:[0-9a-f]{64}$")) and
        (.state == "uploaded")
      ))
    ' >/dev/null 2>&1 <<<"${candidate}"; then
    release_response="${candidate}"
    break
  fi
  if test "${attempt}" = 5; then
    printf '%s\n' npm_dispatch_preflight_invalid
    exit 1
  fi
  sleep 2
done
test -n "${release_response}"
expected_assets="$(LC_ALL=C sort <<ASSETS
SHA256SUMS
ai-cli-gateway_${VERSION}_darwin_amd64.tar.gz
ai-cli-gateway_${VERSION}_darwin_arm64.tar.gz
ai-cli-gateway_${VERSION}_linux_amd64.tar.gz
ai-cli-gateway_${VERSION}_linux_arm64.tar.gz
ai-cli-gateway_${VERSION}_sbom.spdx.json
ai-cli-gateway_${VERSION}_windows_amd64.zip
ASSETS
)"
api_assets="$(jq -r '.assets[].name' <<<"${release_response}" | LC_ALL=C sort)"
test "${api_assets}" = "${expected_assets}"
live_commit="$(resolve_live_tag)"
test "${live_commit}" = "${TAG_COMMIT}"
gh workflow run npm-release.yml \
  --repo "${repository}" \
  --ref "${TAG}" \
  -f "tag=${TAG}"
~~~

- [ ] **Step 5: Run GREEN and commit**

~~~bash
gofmt -w internal/securitytest/repository_test.go
go test -count=1 ./internal/securitytest \
  -run '^(TestReleaseWorkflowContract|TestReleaseWorkflowContractRejectsMutations|TestReleasePublicationScript|TestReleaseNPMDispatchScript)$'
git diff --check
git add .github/workflows/release.yml internal/securitytest/repository_test.go
git commit -m "fix: dispatch npm after immutable release"
~~~

Expected: tests PASS; the old publication fixture remains green; the new fixture proves ordering, retry bounds, and exact arguments.

---

### Task 3: Run the complete local verification chain

**Files:**
- Verify: .github/workflows/npm-release.yml
- Verify: .github/workflows/release.yml
- Verify: internal/securitytest/repository_test.go
- Verify: both approved design and implementation plan

**Interfaces:**
- Consumes: final branch tree and repository-pinned tools.
- Produces: fresh local evidence for review; no remote writes and no npm publication.

- [ ] **Step 1: Verify tools, formatting, dependencies, npm, workflows, and static checks**

~~~bash
test "$(go env GOVERSION)" = go1.26.5
test "$(node --version)" = v24.13.0
test "$(npm --version)" = 11.6.2
golangci-lint version | rg -F 'version 2.12.2'
test -z "$(gofmt -l .)"
git diff --check
go mod verify
npm ci --prefix npm --ignore-scripts
npm test --prefix npm
node npm/scripts/verify-packages.js --source-check
go vet ./...
golangci-lint run ./...
go test -count=1 ./internal/securitytest
~~~

Expected: npm reports 142 passing tests, package verification succeeds,
golangci-lint reports 0 issues, and all other commands exit zero. The complete
security-test package includes
TestWorkflowActionlintIsolationScript, which builds actionlint v1.7.12 in an
isolated environment and validates exactly ci.yml, release.yml, and
npm-release.yml with shellcheck and pyflakes integration disabled.

- [ ] **Step 2: Run all Go execution modes**

~~~bash
go test -count=1 ./...
go test -race -timeout=20m -count=1 ./...
go test -tags=integration -count=1 ./...
go test -trimpath -count=1 ./...
GOFLAGS=-trimpath go test -count=1 ./internal/testutil ./internal/testcli
go test -tags=live -run '^$' ./internal/provider/...
CGO_ENABLED=0 go build -trimpath -o /tmp/ai-cli-gateway-npm-dispatch ./cmd/ai-cli-gateway
~~~

Expected: every command exits zero. Diagnose any failure; a retry alone is not a pass.

- [ ] **Step 3: Inspect the final branch**

~~~bash
git diff origin/main...HEAD --check
git diff --stat origin/main...HEAD
git log --oneline --decorate origin/main..HEAD
git status --short --branch
~~~

Expected: only both docs, both workflows, and repository_test.go differ; the tree is clean.

---

### Task 4: Open, validate, and merge the recovery PR

**Files:**
- Remote branch: fix/npm-release-dispatch-v0.2.1
- Pull request base: krkarma777/ai-cli-gateway:main

**Interfaces:**
- Consumes: clean locally verified branch.
- Produces: one squash-merged, required-check-green main commit.

- [ ] **Step 1: Point the isolated clone to GitHub and push only the feature branch**

~~~bash
git remote set-url origin https://github.com/krkarma777/ai-cli-gateway.git
git fetch origin main
git merge-base --is-ancestor origin/main HEAD
git push --set-upstream origin fix/npm-release-dispatch-v0.2.1
~~~

Expected: no force push, no tag write, and one new remote branch.

- [ ] **Step 2: Create the PR**

~~~bash
gh pr create \
  --repo krkarma777/ai-cli-gateway \
  --base main \
  --head fix/npm-release-dispatch-v0.2.1 \
  --title 'fix: explicitly dispatch npm releases' \
  --body-file docs/superpowers/plans/2026-09-01-npm-release-explicit-dispatch.md
~~~

Expected: one PR URL and no npm workflow run.

- [ ] **Step 3: Require every check and squash-merge**

~~~bash
pr_number="$(gh pr view --repo krkarma777/ai-cli-gateway --json number --jq .number)"
gh pr checks "${pr_number}" --repo krkarma777/ai-cli-gateway --watch --fail-fast
gh pr view "${pr_number}" --repo krkarma777/ai-cli-gateway \
  --json mergeStateStatus,reviewDecision,statusCheckRollup
gh pr merge "${pr_number}" --repo krkarma777/ai-cli-gateway --squash --delete-branch
~~~

Expected: required checks successful and PR merged. Stop on any failed check or incompatible main movement.

- [ ] **Step 4: Record exact merge SHA and CI**

~~~bash
merge_sha="$(gh pr view "${pr_number}" --repo krkarma777/ai-cli-gateway --json mergeCommit --jq .mergeCommit.oid)"
test "${#merge_sha}" = 40
test "$(git ls-remote origin refs/heads/main | awk '{print $1}')" = "${merge_sha}"
gh run list --repo krkarma777/ai-cli-gateway --commit "${merge_sha}" \
  --json databaseId,workflowName,event,status,conclusion,url
~~~

Expected: live main equals merge_sha and required merge CI succeeded.

---

### Task 5: Recover and verify npm 0.2.1

**Files:**
- Remote workflow: npm-release.yml at merged main
- Immutable input: GitHub tag and Release v0.2.1
- Registry outputs: six public packages at 0.2.1

**Interfaces:**
- Consumes: exact green merge SHA, secret name NPM_TOKEN, immutable release, and absent versions.
- Produces: six provenance-bearing npm versions with descriptor-equal SRI and a passing real install.

- [ ] **Step 1: Recheck publication preconditions immediately before dispatch**

~~~bash
repository=krkarma777/ai-cli-gateway
tag=v0.2.1
main_sha="$(gh api "repos/${repository}/git/ref/heads/main" --jq .object.sha)"
test "${main_sha}" = "${merge_sha}"
test "$(gh secret list --repo "${repository}" --json name --jq '[.[] | select(.name == "NPM_TOKEN")] | length')" = 1
gh api -H 'X-GitHub-Api-Version: 2026-03-10' "repos/${repository}/releases/tags/${tag}" \
  --jq 'select(.tag_name == "v0.2.1" and .draft == false and .prerelease == false and .immutable == true and (.assets | length) == 7) | .id'
~~~

Require all six exact versions to be absent with npm's structured E404 shape,
not an authentication, network, or parsing failure:

~~~bash
packages=(
  ai-cli-gateway-darwin-x64
  ai-cli-gateway-darwin-arm64
  ai-cli-gateway-linux-x64
  ai-cli-gateway-linux-arm64
  ai-cli-gateway-win32-x64
  ai-cli-gateway
)
registry_preflight="$(mktemp -d)"
for index in "${!packages[@]}"; do
  package_spec="${packages[${index}]}@0.2.1"
  stdout_file="${registry_preflight}/${index}.stdout"
  stderr_file="${registry_preflight}/${index}.stderr"
  if npm view "${package_spec}" dist.integrity --json >"${stdout_file}" 2>"${stderr_file}"; then
    printf 'already published: %s\n' "${package_spec}" >&2
    exit 1
  fi
  node --input-type=module - "${stdout_file}" "${package_spec}" "${packages[${index}]}" <<'NODE'
import { readFileSync } from "node:fs";
const failure = JSON.parse(readFileSync(process.argv[2], "utf8"));
const packageSpec = process.argv[3];
const packageName = process.argv[4];
if (
  failure === null || typeof failure !== "object" || Array.isArray(failure) ||
  JSON.stringify(Object.keys(failure)) !== JSON.stringify(["error"]) ||
  failure.error === null || typeof failure.error !== "object" || Array.isArray(failure.error) ||
  JSON.stringify(Object.keys(failure.error).sort()) !== JSON.stringify(["code", "detail", "summary"]) ||
  failure.error.code !== "E404" ||
  !failure.error.summary.startsWith(`Not Found - GET https://registry.npmjs.org/${packageName} - `) ||
  !failure.error.detail.includes(`'${packageSpec}'`)
) process.exit(1);
NODE
  test "$(awk '/^npm error code / { count++ } END { print count + 0 }' "${stderr_file}")" = 1
  grep -Fx 'npm error code E404' "${stderr_file}" >/dev/null
done
~~~

- [ ] **Step 2: Dispatch from live main and capture one run ID**

~~~bash
dispatch_after="$(date -u '+%Y-%m-%dT%H:%M:%SZ')"
gh workflow run npm-release.yml \
  --repo "${repository}" \
  --ref main \
  -f tag=v0.2.1
~~~

Capture exactly one new run; ambiguity or a 60-second lookup timeout is a stop
condition:

~~~bash
npm_run_id=
for attempt in {1..30}; do
  runs="$(gh run list --repo "${repository}" \
    --workflow npm-release.yml \
    --event workflow_dispatch \
    --branch main \
    --limit 20 \
    --json databaseId,headSha,createdAt,status,conclusion,url)"
  matches="$(jq -c --arg sha "${main_sha}" --arg after "${dispatch_after}" \
    '[.[] | select(.headSha == $sha and .createdAt >= $after)]' <<<"${runs}")"
  match_count="$(jq 'length' <<<"${matches}")"
  test "${match_count}" -le 1
  if test "${match_count}" = 1; then
    npm_run_id="$(jq -r '.[0].databaseId' <<<"${matches}")"
    break
  fi
  test "${attempt}" -lt 30 || exit 1
  sleep 2
done
[[ "${npm_run_id}" =~ ^[1-9][0-9]*$ ]]
~~~

- [ ] **Step 3: Monitor the numeric run**

~~~bash
gh run watch "${npm_run_id}" --repo "${repository}" --exit-status
gh run view "${npm_run_id}" --repo "${repository}" \
  --json event,headBranch,headSha,status,conclusion,jobs,url
~~~

Expected: workflow_dispatch, branch main, exact head SHA, and successful package and publish jobs.

- [ ] **Step 4: Compare artifact, tarball, and registry SRI**

~~~bash
artifact_root="$(mktemp -d)"
gh run download "${npm_run_id}" --repo "${repository}" \
  --name npm-packages-v0.2.1 --dir "${artifact_root}"
~~~

Require descriptor, tarball, and registry integrity with one closed Node check:

~~~bash
node --input-type=module - "${artifact_root}" <<'NODE'
import { createHash } from "node:crypto";
import { execFileSync } from "node:child_process";
import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
const root = path.resolve(process.argv[2]);
const names = [
  "ai-cli-gateway-darwin-x64",
  "ai-cli-gateway-darwin-arm64",
  "ai-cli-gateway-linux-x64",
  "ai-cli-gateway-linux-arm64",
  "ai-cli-gateway-win32-x64",
  "ai-cli-gateway",
];
const descriptor = JSON.parse(readFileSync(path.join(root, "packages.json"), "utf8"));
if (!Array.isArray(descriptor) || descriptor.length !== names.length) process.exit(1);
const expectedFiles = [...names.map((name) => `${name}-0.2.1.tgz`), "packages.json"].sort();
if (JSON.stringify(readdirSync(root).sort()) !== JSON.stringify(expectedFiles)) process.exit(1);
for (let index = 0; index < names.length; index += 1) {
  const record = descriptor[index];
  if (record.name !== names[index] || record.version !== "0.2.1" ||
      record.filename !== `${names[index]}-0.2.1.tgz` ||
      !/^sha512-[A-Za-z0-9+/]{86}==$/.test(record.integrity)) process.exit(1);
  const local = `sha512-${createHash("sha512")
    .update(readFileSync(path.join(root, record.filename))).digest("base64")}`;
  const remote = JSON.parse(execFileSync("npm", [
    "view", `${record.name}@0.2.1`, "dist.integrity", "--json",
  ], { encoding: "utf8" }));
  if (local !== record.integrity || remote !== record.integrity) process.exit(1);
}
NODE
~~~

Open every npm package page and require the provenance indicator to link to
the recorded npm workflow run, repository, workflow file, and recovery main
commit. This recovery provenance source ref is main; package bytes remain bound
to the separately verified immutable v0.2.1 tag commit.

- [ ] **Step 5: Perform a clean real installation**

~~~bash
install_root="$(mktemp -d)"
npm install --global --ignore-scripts --no-audit --no-fund \
  --prefix "${install_root}" ai-cli-gateway@0.2.1
"${install_root}/bin/ai-cli-gateway" version
"${install_root}/bin/ai-cli-gateway" --help >/dev/null
npm uninstall --global --ignore-scripts --prefix "${install_root}" ai-cli-gateway
~~~

Also create a clean local project, install ai-cli-gateway@0.2.1 with scripts
disabled, and run npm audit signatures. It must verify registry signatures and
provenance attestations without warnings:

~~~bash
audit_root="$(mktemp -d)"
(
  cd "${audit_root}"
  npm init --yes >/dev/null
  npm install --ignore-scripts --no-audit --no-fund ai-cli-gateway@0.2.1
  npm audit signatures
)
~~~

Expected:

~~~text
ai-cli-gateway v0.2.1 (7d5cf2911b3394e564842697b03b1fc9a1162630, 2026-08-31T12:22:44Z)
~~~

---

### Task 6: Configure trusted publishing and retire the token

**Files:**
- npm trust settings for all six packages
- GitHub secret NPM_TOKEN
- Maintainer short-lived npm token

**Interfaces:**
- Consumes: six packages, npm write access with account 2FA, npm CLI 11.15.0 or newer, workflow filename npm-release.yml.
- Produces: six verified OIDC relationships, token-disallowing settings, no GitHub secret, and revoked token.

- [ ] **Step 1: Verify interactive npm authority without exposing credentials**

~~~bash
npm --version
npm whoami
~~~

Require npm 11.15.0 or newer. If project npm is 11.6.2, use a separately installed npm 11.19.1 only for administration; do not change CI.

- [ ] **Step 2: Create and verify six trust relationships**

For every package in Task 5:

~~~bash
npm trust github "${package}" \
  --repo krkarma777/ai-cli-gateway \
  --file npm-release.yml \
  --allow-publish \
  --yes
npm trust list "${package}" --json
~~~

Require exactly one relationship, repository krkarma777/ai-cli-gateway, workflow filename npm-release.yml, no environment restriction, and publish permission. The file is the filename only and resolves to .github/workflows/npm-release.yml.

- [ ] **Step 3: Disallow traditional publish tokens**

For each package, open npm Settings → Publishing access, select **Require two-factor authentication and disallow tokens**, save with interactive 2FA, reopen, and verify. Do not use the bootstrap token for governance.

- [ ] **Step 4: Remove both bootstrap credential copies**

~~~bash
gh secret delete NPM_TOKEN --repo krkarma777/ai-cli-gateway
test "$(gh secret list --repo krkarma777/ai-cli-gateway --json name --jq '[.[] | select(.name == "NPM_TOKEN")] | length')" = 0
~~~

Revoke the named short-lived token in npm account settings and confirm it is absent without printing its value.

Official references:

- https://docs.npmjs.com/cli/v11/commands/npm-trust/
- https://docs.npmjs.com/trusted-publishers/
- https://docs.npmjs.com/requiring-2fa-for-package-publishing-and-settings-modification/

---

### Task 7: Remove bootstrap and recovery fallbacks in a separate PR

**Files:**
- Modify: .github/workflows/npm-release.yml:519-525
- Modify: internal/securitytest/repository_test.go:7063-7120,7508-7565

**Interfaces:**
- Consumes: six verified trusted publishers and absent NPM_TOKEN secret.
- Produces: tokenless tag-only npm publication using the existing id-token: write permission.

- [ ] **Step 1: Start from the merged main commit and write the failing contracts**

Do not continue from the previously squash-merged feature branch:

~~~bash
git fetch origin main
git switch --create fix/remove-npm-release-fallbacks origin/main
test -z "$(git status --porcelain)"
~~~

Change the expected publish-step environment to:

~~~go
map[string]string{
	"EXPECTED_ARTIFACT_DIGEST": "${{ needs.package.outputs.artifact_digest }}",
	"NPM_CONFIG_REGISTRY":      "https://registry.npmjs.org/",
}
~~~

Replace bootstrap token renamed with a mutation that inserts NODE_AUTH_TOKEN: ${{ secrets.NPM_TOKEN }} and require rejection. Add a global assertion that npm-release.yml contains neither NODE_AUTH_TOKEN nor secrets.NPM_TOKEN.

Change the metadata contract to accept only
EVENT_REF="refs/tags/${INPUT_TAG}" with EVENT_SHA equal to tag_commit. Add a
mutation that restores refs/heads/main and require rejection. Remove the main
ref API query, source_mode branch, and recovery mutations; after the successful
0.2.1 dispatch there is no reason to retain a branch publication path.

- [ ] **Step 2: Run RED, remove one YAML line, and run GREEN**

~~~bash
go test -count=1 ./internal/securitytest \
  -run '^(TestNPMReleaseWorkflowContract|TestNPMReleaseWorkflowContractRejectsMutations)$'
~~~

Expected RED: the token entry and main recovery path remain. Remove the token
environment line:

~~~yaml
          NODE_AUTH_TOKEN: ${{ secrets.NPM_TOKEN }}
~~~

Replace the source-ref case with the exact tag-only checks:

~~~bash
test "${EVENT_REF}" = "refs/tags/${INPUT_TAG}"
tag_commit="$(resolve_live_tag)"
[[ "${tag_commit}" =~ ${commit_pattern} ]]
test "${EVENT_SHA}" = "${tag_commit}"
~~~

Review the shortened decoded metadata script and update only its entry in
validateNPMReleaseRunHashes to the newly computed SHA-256.

Then:

~~~bash
gofmt -w internal/securitytest/repository_test.go
go test -count=1 ./internal/securitytest \
  -run '^(TestNPMReleaseWorkflowContract|TestNPMReleaseWorkflowContractRejectsMutations|TestNPMReleaseWorkflowBashSyntax)$'
git diff --check
~~~

Expected: PASS with id-token: write and --provenance unchanged.

- [ ] **Step 3: Verify, commit, PR, and merge**

Repeat Task 3's complete verification chain, then:

~~~bash
git add .github/workflows/npm-release.yml internal/securitytest/repository_test.go
git commit -m "security: remove npm release fallbacks"
git push origin HEAD:fix/remove-npm-release-fallbacks
~~~

Open a separate PR, require every check, and squash-merge only after confirming all six trust relationships still exist and the GitHub secret remains absent.
