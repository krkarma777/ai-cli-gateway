# npm Scoped Platform Packages v0.2.1 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Complete the existing v0.2.1 npm release as `ai-cli-gateway@0.2.1` backed by five public `@krkarma777` native packages, with searchable metadata, verified Windows launchers, immutable release binaries, and no abandoned unscoped packages left published.

**Architecture:** Keep the public launcher unscoped and pin five scoped native `optionalDependencies` at exact version `0.2.1`. Run npm packaging from the merged recovery commit while rebuilding and comparing binaries from a detached worktree at immutable tag `v0.2.1`; publish native packages first and the launcher last from the existing split-authority GitHub Actions workflow.

**Tech Stack:** Go 1.26.5, Node.js 22.14.0 and 24.13.0, npm 11.6.2, ECMAScript modules, `node:test`, PowerShell 7, GitHub Actions, npm public scoped packages, npm provenance.

## Global Constraints

- Product version remains exactly `0.2.1`; do not create, prepare, or document `0.2.2`.
- Git tag `v0.2.1`, its commit, the immutable GitHub Release, and its seven assets remain unchanged.
- Public launcher name remains exactly `ai-cli-gateway`.
- Native names are exactly `@krkarma777/ai-cli-gateway-darwin-x64`, `@krkarma777/ai-cli-gateway-darwin-arm64`, `@krkarma777/ai-cli-gateway-linux-x64`, `@krkarma777/ai-cli-gateway-linux-arm64`, and `@krkarma777/ai-cli-gateway-win32-x64`.
- Supported targets remain exactly `darwin-x64`, `darwin-arm64`, `linux-x64`, `linux-arm64`, and `win32-x64`; Windows ARM64 remains unsupported.
- Native packages remain exact optional dependencies, publish first in canonical target order, and the launcher publishes last.
- Package manifests contain no lifecycle scripts and installation performs no application-owned download.
- Launcher description remains exactly `Build AI MVPs with Codex CLI, Claude Code, and Gemini CLI through a local OpenAI Responses-compatible API.`
- Add no CI job and no new workflow; Windows verification extends the existing `npm-host-install` job.
- Package from the recovery commit, but build and compare native binaries only from immutable tag commit `7d5cf2911b3394e564842697b03b1fc9a1162630`.
- Publication uses `--ignore-scripts --access public --provenance` and the explicit registry `https://registry.npmjs.org/`.
- Use `/private/tmp` for task caches and staging; do not repair or mutate the user's root-owned default npm cache.
- Unpublish only the four exact unscoped `0.2.1` versions listed in Task 7; never use a wildcard or package-wide scope operation.
- Run focused tests after each task and one complete local gate before pushing, so CI receives one candidate update.

## File Structure

### Create

- `npm/scripts/package-name.js` — validate npm package identities and derive npm's scoped tarball filename.
- `npm/test/package-name.test.js` — independent scoped/unscoped filename and invalid-input contracts.
- `npm/scripts/package-copy.js` — canonical descriptions, keywords, and npm README renderers ported from reviewed commit `24ed915`.
- `npm/scripts/verify-windows-launcher.ps1` — Windows `.cmd`/PowerShell execution and installation-integrity verifier ported from reviewed commits `d0d9c26`, `f7ccc24`, and `f40c6df`.

### Modify

- `npm/scripts/package-config.js` — scoped canonical target package names at `0.2.1`.
- `npm/scripts/verify-packages.js` — exact searchable metadata and scoped tarball filenames.
- `npm/launcher/package.json` — searchable metadata and five exact scoped optional dependencies.
- `npm/launcher/lib/launcher.js` — runtime resolution of the five scoped package names.
- `npm/launcher/README.md` — product-first npm page and scoped internal topology.
- `npm/platforms/*/package.json` — scoped package identities and internal-package metadata.
- `npm/platforms/*/README.md` — scoped titles and launcher-only install direction.
- `npm/test/package-contract.test.js` — exact scoped manifests, descriptions, keywords, and README contracts.
- `npm/test/launcher.test.js` — scoped runtime target resolution.
- `npm/test/stage-packages.test.js` — scoped tarball filenames and descriptors.
- `.github/workflows/ci.yml` — existing Windows matrix launcher verification step.
- `.github/workflows/npm-release.yml` — main-only v0.2.1 recovery, detached tag build tree, scoped artifacts, scoped registry checks, native-first publication.
- `internal/securitytest/repository_test.go` — closed contracts for metadata, Windows verification, dual-source release behavior, scoped cohort, and exact workflow run hashes.
- `README.md`, `docs/getting-started.md`, `docs/releases/v0.2.1.md` — current scoped npm topology and `0.2.1` instructions.

### Preserve

- `npm/launcher/bin/ai-cli-gateway.js` — dependency-free delegation entry.
- `.github/workflows/release.yml` — immutable release creation is already complete.
- All production Go gateway packages — npm recovery changes distribution only.

---

### Task 1: Port the Approved npm Discoverability Copy at v0.2.1

**Files:**

- Create: `npm/scripts/package-copy.js`
- Modify: `npm/test/package-contract.test.js`
- Modify: `npm/scripts/verify-packages.js`
- Modify: `npm/launcher/package.json`
- Modify: `npm/launcher/README.md`
- Modify: `npm/platforms/darwin-x64/package.json`
- Modify: `npm/platforms/darwin-arm64/package.json`
- Modify: `npm/platforms/linux-x64/package.json`
- Modify: `npm/platforms/linux-arm64/package.json`
- Modify: `npm/platforms/win32-x64/package.json`
- Modify: `npm/platforms/darwin-x64/README.md`
- Modify: `npm/platforms/darwin-arm64/README.md`
- Modify: `npm/platforms/linux-x64/README.md`
- Modify: `npm/platforms/linux-arm64/README.md`
- Modify: `npm/platforms/win32-x64/README.md`

**Interfaces:**

- Produces: `LAUNCHER_DESCRIPTION`, `LAUNCHER_KEYWORDS`, `nativeDescription(target)`, `nativeKeywords(target)`, `launcherReadme(nodeRange)`, and `nativeReadme(target)` from `npm/scripts/package-copy.js`.
- Consumes: the existing `PACKAGE_VERSION`, `LAUNCHER_NAME`, `NODE_RANGE`, and `TARGETS` exports without changing their version or names in this task.

- [ ] **Step 1: Apply only the already reviewed failing metadata contract tests**

Run:

```bash
git show --format= 24ed915 -- npm/test/package-contract.test.js | git apply
node --test npm/test/package-contract.test.js
```

Expected: the patch applies cleanly and the test fails because checked-in manifests and READMEs still contain the old minimal copy.

- [ ] **Step 2: Apply the reviewed implementation without its test patch**

Run:

```bash
git show --format= 24ed915 -- . ':!npm/test/package-contract.test.js' | git apply
```

Expected: `npm/scripts/package-copy.js` is created; package manifests, READMEs, and verification code change while every version remains `0.2.1`.

- [ ] **Step 3: Verify metadata and source-copy contracts**

Run:

```bash
node --test npm/test/package-contract.test.js npm/test/stage-packages.test.js
node npm/scripts/verify-packages.js --source-check
rg -n '0\.2\.2' npm
```

Expected: both test files and source check pass; `rg` prints nothing.

- [ ] **Step 4: Commit the independently testable metadata port**

Run:

```bash
git add npm
git commit -m "feat: improve npm package discoverability"
```

Expected: one commit containing only npm metadata, README, verifier, and tests.

---

### Task 2: Change Native Identity and Tarball Contracts to the npm Scope

**Files:**

- Create: `npm/scripts/package-name.js`
- Create: `npm/test/package-name.test.js`
- Modify: `npm/scripts/package-config.js`
- Modify: `npm/scripts/verify-packages.js`
- Modify: `npm/launcher/package.json`
- Modify: `npm/launcher/lib/launcher.js`
- Modify: `npm/launcher/README.md`
- Modify: `npm/platforms/*/package.json`
- Modify: `npm/platforms/*/README.md`
- Modify: `npm/test/package-contract.test.js`
- Modify: `npm/test/launcher.test.js`
- Modify: `npm/test/stage-packages.test.js`

**Interfaces:**

- Produces: `npmTarballFilename(name: string, version: string): string`.
- Produces: five `TARGETS[*].packageName` values under `@krkarma777`.
- Consumes: exact semantic version strings and npm package identities; invalid values throw `TypeError("invalid npm package identity")`.

- [ ] **Step 1: Add the failing filename contract**

Create `npm/test/package-name.test.js` with:

```js
import assert from "node:assert/strict";
import test from "node:test";
import { npmTarballFilename } from "../scripts/package-name.js";

test("npm tarball filenames encode the scope without @ or slash", () => {
  assert.equal(
    npmTarballFilename("@krkarma777/ai-cli-gateway-linux-x64", "0.2.1"),
    "krkarma777-ai-cli-gateway-linux-x64-0.2.1.tgz",
  );
  assert.equal(
    npmTarballFilename("ai-cli-gateway", "0.2.1"),
    "ai-cli-gateway-0.2.1.tgz",
  );
});

for (const [name, version] of [
  ["@krkarma777", "0.2.1"],
  ["@krkarma777/AI-CLI-Gateway", "0.2.1"],
  ["@krkarma777/ai-cli-gateway/linux", "0.2.1"],
  ["@krkarma777/ai-cli-gateway", "v0.2.1"],
  ["../ai-cli-gateway", "0.2.1"],
]) {
  test(`rejects invalid npm identity ${name}@${version}`, () => {
    assert.throws(
      () => npmTarballFilename(name, version),
      new TypeError("invalid npm package identity"),
    );
  });
}
```

Change the target tuples in `npm/test/package-contract.test.js` and expected package names in `npm/test/launcher.test.js` to the five exact scoped names from Global Constraints. Change the local `npmFilename` helper in `npm/test/stage-packages.test.js` to import and call `npmTarballFilename`.

- [ ] **Step 2: Run the tests and require a red result**

Run:

```bash
node --test npm/test/package-name.test.js npm/test/package-contract.test.js npm/test/launcher.test.js npm/test/stage-packages.test.js
```

Expected: FAIL because `package-name.js` is absent and production manifests still contain unscoped names.

- [ ] **Step 3: Implement the closed filename helper**

Create `npm/scripts/package-name.js` with:

```js
const PACKAGE_PATTERN = /^(?:@[a-z0-9][a-z0-9._-]*\/)?[a-z0-9][a-z0-9._-]*$/u;
const VERSION_PATTERN = /^(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)\.(?:0|[1-9]\d*)$/u;

export function npmTarballFilename(name, version) {
  if (
    typeof name !== "string" ||
    typeof version !== "string" ||
    !PACKAGE_PATTERN.test(name) ||
    !VERSION_PATTERN.test(version)
  ) {
    throw new TypeError("invalid npm package identity");
  }
  const filenameName = name.startsWith("@")
    ? name.slice(1).replace("/", "-")
    : name;
  return `${filenameName}-${version}.tgz`;
}
```

Import it in `npm/scripts/verify-packages.js` and replace `expectedFilename` with:

```js
function expectedFilename(name, version) {
  return npmTarballFilename(name, version);
}
```

- [ ] **Step 4: Implement the exact scoped package topology**

Replace the `TARGETS` package names in `npm/scripts/package-config.js` and `npm/launcher/lib/launcher.js` with:

```js
"@krkarma777/ai-cli-gateway-darwin-x64"
"@krkarma777/ai-cli-gateway-darwin-arm64"
"@krkarma777/ai-cli-gateway-linux-x64"
"@krkarma777/ai-cli-gateway-linux-arm64"
"@krkarma777/ai-cli-gateway-win32-x64"
```

Set the five platform `package.json` `name` fields to the matching scoped value. Set `npm/launcher/package.json` optional dependencies to:

```json
{
  "@krkarma777/ai-cli-gateway-darwin-x64": "0.2.1",
  "@krkarma777/ai-cli-gateway-darwin-arm64": "0.2.1",
  "@krkarma777/ai-cli-gateway-linux-x64": "0.2.1",
  "@krkarma777/ai-cli-gateway-linux-arm64": "0.2.1",
  "@krkarma777/ai-cli-gateway-win32-x64": "0.2.1"
}
```

Regenerate the checked-in README strings from the package-copy renderers' expected output so every native title and launcher topology uses the scoped name. Keep user installation commands as `npm install --global ai-cli-gateway`.

Inspect the exact renderer output before applying the README patch:

```bash
node --input-type=module -e 'import { NODE_RANGE, TARGETS } from "./npm/scripts/package-config.js"; import { launcherReadme, nativeReadme } from "./npm/scripts/package-copy.js"; process.stdout.write(launcherReadme(NODE_RANGE)); for (const target of TARGETS) process.stdout.write("\n" + nativeReadme(target));'
```

- [ ] **Step 5: Verify real npm pack output and all package contracts**

Run:

```bash
node --test npm/test/package-name.test.js npm/test/package-contract.test.js npm/test/launcher.test.js npm/test/stage-packages.test.js
node npm/scripts/verify-packages.js --source-check
```

Expected: PASS; staged descriptors use `@krkarma777/...` names and `krkarma777-ai-cli-gateway-...-0.2.1.tgz` filenames.

- [ ] **Step 6: Commit the scoped identity change**

Run:

```bash
git add npm
git commit -m "feat: scope native npm packages"
```

Expected: one commit with no version bump.

---

### Task 3: Port and Verify the Complete Windows npm Launcher Path

**Files:**

- Create: `npm/scripts/verify-windows-launcher.ps1`
- Modify: `.github/workflows/ci.yml`
- Modify: `internal/securitytest/repository_test.go`

**Interfaces:**

- Produces: `verify-windows-launcher.ps1 -InstallPrefix C:\npm\install -ExpectedTag v0.2.1 -ExpectedCommit 7d5cf2911b3394e564842697b03b1fc9a1162630` with caller-supplied absolute prefix and immutable commit values.
- Consumes: npm-generated `ai-cli-gateway.cmd`, `ai-cli-gateway.ps1`, JavaScript launcher, and scoped Windows `.exe` beneath the installation prefix.

- [ ] **Step 1: Port the reviewed Windows commits in order**

Run:

```bash
git cherry-pick d0d9c26
git cherry-pick f7ccc24
git cherry-pick f40c6df
```

Expected: all three commits apply without changing package version or npm package identities.

- [ ] **Step 2: Prove the port retained v0.2.1**

Run:

```bash
rg -n 'v0\.2\.2|"0\.2\.2"' .github/workflows/ci.yml npm/scripts/verify-windows-launcher.ps1 internal/securitytest/repository_test.go
```

Expected: no output. If a context-only `v0.2.2` value was carried by the cherry-pick, replace it with the exact `v0.2.1` expected tag before continuing.

- [ ] **Step 3: Run closed CI and Windows-verifier contracts**

Run:

```bash
go test -count=1 ./internal/securitytest -run 'TestWorkflowMultiPlatformReleaseContract|TestWorkflowMultiPlatformReleaseContractRejectsMutations'
node --test npm/test/*.test.js
```

Expected: PASS. The Windows job remains part of the existing host-install matrix and invokes both npm shims without changing installation contents.

---

### Task 4: Convert the v0.2.1 Recovery Workflow to Scoped Dual-Source Packaging

**Files:**

- Modify: `.github/workflows/npm-release.yml`
- Modify: `internal/securitytest/repository_test.go`

**Interfaces:**

- Consumes: workflow dispatch on exact `refs/heads/main` with input `tag=v0.2.1`.
- Produces: six verified tarballs plus `packages.json`; native binary bytes derive from the immutable tag while package metadata derives from the exact live main commit.
- Preserves: package job `contents: read`; publish job `contents: read`, `id-token: write`, and bootstrap `NPM_TOKEN` only.

- [ ] **Step 1: Change the closed security contract first**

Update `TestNPMReleaseWorkflowContractRejectsMutations`, `moveNPMReleaseLauncherFirst`, `validateNPMReleaseDispatchMetadataContract`, `validateNPMReleaseActions`, `validateNPMReleaseStepShapes`, and `validateNPMReleaseCohortAndE404Contracts` to require:

```text
EVENT_REF == refs/heads/main
package_commit == EVENT_SHA == live main
checkout ref == steps.metadata.outputs.package_commit
release_source == RUNNER_TEMP/npm-v0.2.1-release-source
git worktree add --detach release_source TAG_COMMIT
all go build and releasepack source operations run in release_source
all npm staging operations run in GITHUB_WORKSPACE
five @krkarma777 package identities precede ai-cli-gateway
five scoped tarball filenames begin krkarma777-ai-cli-gateway-
scoped npm E404 paths encode slash as %2f
```

Set the exact upload path contract to:

```text
${{ runner.temp }}/npm-package-work/tarballs/krkarma777-ai-cli-gateway-darwin-x64-0.2.1.tgz
${{ runner.temp }}/npm-package-work/tarballs/krkarma777-ai-cli-gateway-darwin-arm64-0.2.1.tgz
${{ runner.temp }}/npm-package-work/tarballs/krkarma777-ai-cli-gateway-linux-x64-0.2.1.tgz
${{ runner.temp }}/npm-package-work/tarballs/krkarma777-ai-cli-gateway-linux-arm64-0.2.1.tgz
${{ runner.temp }}/npm-package-work/tarballs/krkarma777-ai-cli-gateway-win32-x64-0.2.1.tgz
${{ runner.temp }}/npm-package-work/tarballs/ai-cli-gateway-0.2.1.tgz
${{ runner.temp }}/npm-package-work/tarballs/packages.json
```

- [ ] **Step 2: Run the workflow contract and require failure**

Run:

```bash
go test -count=1 ./internal/securitytest -run 'TestNPMReleaseWorkflow'
```

Expected: FAIL because the workflow still checks out the tag packaging tree and names the unscoped cohort.

- [ ] **Step 3: Bind metadata and checkout to the exact recovery commit**

In `Validate immutable release metadata`, reject every ref except main, verify live main equals `EVENT_SHA`, and emit:

```bash
printf 'tag=%s\n' "${INPUT_TAG}"
printf 'version=%s\n' "${INPUT_TAG#v}"
printf 'tag_commit=%s\n' "${tag_commit}"
printf 'package_commit=%s\n' "${EVENT_SHA}"
printf 'release_id=%s\n' "${release_id}"
```

Change checkout inputs to:

```yaml
with:
  persist-credentials: false
  fetch-depth: 0
  ref: ${{ steps.metadata.outputs.package_commit }}
```

Pass `PACKAGE_COMMIT` into source validation; require current `HEAD` equals it and `TAG_COMMIT` is its ancestor.

- [ ] **Step 4: Build only from a detached immutable-tag worktree**

At the start of `Rebuild and compare release archives`, add:

```bash
readonly release_source="${RUNNER_TEMP}/npm-v0.2.1-release-source"
test ! -e "${release_source}"
git worktree add --detach "${release_source}" "${TAG_COMMIT}"
test "$(git -C "${release_source}" rev-parse HEAD)" = "${TAG_COMMIT}"
test -z "$(git -C "${release_source}" status --porcelain)"
```

Build `releasepack` and all five binaries with `go -C "${release_source}" build`. Pass `--repository-root "${release_source}"` to releasepack. Keep archive checksum comparison against the downloaded immutable release unchanged.

- [ ] **Step 5: Replace the workflow cohort with exact scoped identities and filenames**

Use this closed publication order in both package and publish validation:

```js
const packages = [
  ["@krkarma777/ai-cli-gateway-darwin-x64", "krkarma777-ai-cli-gateway-darwin-x64-0.2.1.tgz"],
  ["@krkarma777/ai-cli-gateway-darwin-arm64", "krkarma777-ai-cli-gateway-darwin-arm64-0.2.1.tgz"],
  ["@krkarma777/ai-cli-gateway-linux-x64", "krkarma777-ai-cli-gateway-linux-x64-0.2.1.tgz"],
  ["@krkarma777/ai-cli-gateway-linux-arm64", "krkarma777-ai-cli-gateway-linux-arm64-0.2.1.tgz"],
  ["@krkarma777/ai-cli-gateway-win32-x64", "krkarma777-ai-cli-gateway-win32-x64-0.2.1.tgz"],
  ["ai-cli-gateway", "ai-cli-gateway-0.2.1.tgz"],
];
```

For structured E404 validation, derive the expected registry path with:

```js
const registryName = packageName.replace("/", "%2f");
```

and require the summary to start with `Not Found - GET https://registry.npmjs.org/${registryName} - `.

- [ ] **Step 6: Recompute exact reviewed run hashes without weakening structural checks**

Temporarily add this test beside `validateNPMReleaseRunHashes`:

```go
func TestPrintNPMReleaseRunHashes(t *testing.T) {
	workflow, err := parseClosedNPMReleaseWorkflow(readRepositoryFile(t, ".github/workflows/npm-release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for label, job := range map[string]releaseWorkflowJob{"package": workflow.Jobs["package"], "publish": workflow.Jobs["publish"]} {
		for _, step := range job.Steps {
			if step.Run == "" {
				continue
			}
			sum := sha256.Sum256([]byte(step.Run))
			t.Logf("%s/%s %s", label, step.Name, hex.EncodeToString(sum[:]))
		}
	}
}
```

Run `go test -count=1 -v ./internal/securitytest -run '^TestPrintNPMReleaseRunHashes$'`, copy its ten exact digests into `validateNPMReleaseRunHashes`, then delete the temporary test completely.

- [ ] **Step 7: Verify workflow syntax, closed contracts, and package tests**

Run:

```bash
go test -count=1 ./internal/securitytest -run 'TestNPMRelease'
node --test npm/test/*.test.js
git diff --check
```

Expected: PASS with ten Bash run steps, one pinned checkout action, exact split permissions, scoped native-first order, and launcher-last publication.

- [ ] **Step 8: Commit the recovery workflow**

Run:

```bash
git add .github/workflows/npm-release.yml internal/securitytest/repository_test.go
git commit -m "ci: publish scoped npm v0.2.1 packages"
```

---

### Task 5: Correct Current v0.2.1 Documentation and Historical Contracts

**Files:**

- Modify: `README.md`
- Modify: `docs/getting-started.md`
- Modify: `docs/releases/v0.2.1.md`
- Modify: `internal/securitytest/repository_test.go`

**Interfaces:**

- Produces: one public user command, `npm install --global ai-cli-gateway@0.2.1`.
- Produces: scoped platform topology only where internal packages are explicitly documented.
- Preserves: archive filenames, checksums, tag links, runtime support, and API behavior.

- [ ] **Step 1: Change documentation tests to require the completed topology**

Update current-document string contracts so the package table expects the five scoped names. Add a negative contract over only `README.md`, `docs/getting-started.md`, and `docs/releases/v0.2.1.md` that rejects each of these five unscoped strings:

```go
oldNames := []string{
	"ai-cli-gateway-darwin-x64",
	"ai-cli-gateway-darwin-arm64",
	"ai-cli-gateway-linux-x64",
	"ai-cli-gateway-linux-arm64",
	"ai-cli-gateway-win32-x64",
}
for _, filename := range []string{"README.md", "docs/getting-started.md", "docs/releases/v0.2.1.md"} {
	document := string(readRepositoryFile(t, filename))
	for _, oldName := range oldNames {
		if strings.Contains(document, "`"+oldName+"`") {
			t.Fatalf("%s retains abandoned npm package %q", filename, oldName)
		}
	}
}
```

- [ ] **Step 2: Run documentation/security tests and require failure**

Run:

```bash
go test -count=1 ./internal/securitytest -run 'TestREADME|TestGettingStarted|TestRelease'
```

Expected: FAIL on current unscoped package tables.

- [ ] **Step 3: Update current documentation without changing release assets**

Use these exact public and internal forms:

```console
npm install --global ai-cli-gateway@0.2.1
```

```text
@krkarma777/ai-cli-gateway-darwin-x64
@krkarma777/ai-cli-gateway-darwin-arm64
@krkarma777/ai-cli-gateway-linux-x64
@krkarma777/ai-cli-gateway-linux-arm64
@krkarma777/ai-cli-gateway-win32-x64
```

State that platform packages are optional internal implementation packages and users install only `ai-cli-gateway`. Do not alter manual archive filenames or v0.2.1 binary claims.

- [ ] **Step 4: Verify docs and commit**

Run:

```bash
go test -count=1 ./internal/securitytest -run 'TestREADME|TestGettingStarted|TestRelease'
git diff --check
git add README.md docs/getting-started.md docs/releases/v0.2.1.md internal/securitytest/repository_test.go
git commit -m "docs: document scoped npm v0.2.1 packages"
```

Expected: PASS and one documentation-focused commit.

---

### Task 6: Run One Complete Local Gate and Merge One Candidate

**Files:**

- Verify only; no intended file changes.

**Interfaces:**

- Consumes: the complete candidate branch.
- Produces: one reviewed commit SHA on `main`, ready for exact registry cleanup and workflow dispatch.

- [ ] **Step 1: Verify branch identity and worktree cleanliness**

Run:

```bash
test "$(git branch --show-current)" = fix/npm-scoped-platform-packages-v0.2.1
test -z "$(git status --porcelain)"
git log --oneline --decorate origin/main..HEAD
```

Expected: only the design, plan, metadata, scope, Windows, workflow, and documentation commits are ahead of main.

- [ ] **Step 2: Build and pack all targets from the immutable tag locally**

Run:

```bash
candidate_root="$(pwd)"
tag_commit=7d5cf2911b3394e564842697b03b1fc9a1162630
source_date=2026-08-31T12:22:44Z
release_parent="$(mktemp -d /private/tmp/ai-cli-gateway-release-source.XXXXXX)"
release_source="${release_parent}/source"
binary_root="$(mktemp -d /private/tmp/ai-cli-gateway-binaries.XXXXXX)"
package_root="$(mktemp -d /private/tmp/ai-cli-gateway-packages.XXXXXX)"
staging_root="${package_root}/staging"
tarball_root="${package_root}/tarballs"
descriptor="${tarball_root}/packages.json"

git worktree add --detach "${release_source}" "${tag_commit}"
test "$(git -C "${release_source}" rev-parse HEAD)" = "${tag_commit}"
ldflags="-s -w -X github.com/krkarma777/ai-cli-gateway/internal/buildinfo.Version=v0.2.1 -X github.com/krkarma777/ai-cli-gateway/internal/buildinfo.Commit=${tag_commit} -X github.com/krkarma777/ai-cli-gateway/internal/buildinfo.Date=${source_date}"

while read -r goos goarch directory executable; do
  install -d -m 0700 "${binary_root}/${directory}"
  CGO_ENABLED=0 GOOS="${goos}" GOARCH="${goarch}" \
    go -C "${release_source}" build \
      -trimpath -buildvcs=false -mod=readonly -ldflags "${ldflags}" \
      -o "${binary_root}/${directory}/${executable}" \
      ./cmd/ai-cli-gateway
done <<'TARGETS'
linux amd64 linux_amd64 ai-cli-gateway
linux arm64 linux_arm64 ai-cli-gateway
darwin amd64 darwin_amd64 ai-cli-gateway
darwin arm64 darwin_arm64 ai-cli-gateway
windows amd64 windows_amd64 ai-cli-gateway.exe
TARGETS

install -d -m 0700 "${tarball_root}"
node npm/scripts/stage-packages.js \
  --repository-root "${candidate_root}" \
  --binary-root "${binary_root}" \
  --output-root "${staging_root}" \
  --version 0.2.1
node npm/scripts/verify-packages.js \
  --staging-root "${staging_root}" \
  --tarball-root "${tarball_root}" \
  --descriptor "${descriptor}" \
  --version 0.2.1

host_target="$(node -p '`${process.platform}-${process.arch}`')"
case "${host_target}" in
  darwin-x64|darwin-arm64|linux-x64|linux-arm64) ;;
  *) exit 1 ;;
esac
install_root="$(mktemp -d /private/tmp/ai-cli-gateway-local-install.XXXXXX)"
local_npm_cache="$(mktemp -d /private/tmp/ai-cli-gateway-local-npm-cache.XXXXXX)"
NPM_CONFIG_CACHE="${local_npm_cache}" npm install \
  --global --ignore-scripts --no-audit --no-fund \
  --prefix "${install_root}" \
  "${tarball_root}/krkarma777-ai-cli-gateway-${host_target}-0.2.1.tgz" \
  "${tarball_root}/ai-cli-gateway-0.2.1.tgz"
version_output="$("${install_root}/bin/ai-cli-gateway" version)"
test "${version_output}" = "ai-cli-gateway v0.2.1 (${tag_commit}, ${source_date})"
git worktree remove "${release_source}"
```

Expected: all five native binaries build from the immutable tag, six tarballs pass verification, and the host launcher executes the v0.2.1 binary.

- [ ] **Step 3: Run the complete local release gate once**

Run:

```bash
npm_cache_dir="$(mktemp -d /private/tmp/ai-cli-gateway-npm-test.XXXXXX)"
go_cache_dir="$(mktemp -d /private/tmp/ai-cli-gateway-go-cache.XXXXXX)"
NPM_CONFIG_CACHE="${npm_cache_dir}" node --test npm/test/*.test.js
gofmt_files="$(gofmt -l .)"
test -z "${gofmt_files}"
GOCACHE="${go_cache_dir}" go vet ./...
golangci-lint run ./...
GOCACHE="${go_cache_dir}" go test -count=1 ./...
GOCACHE="${go_cache_dir}" go test -tags=integration -count=1 ./...
git diff --check
test -z "$(git status --porcelain)"
```

Expected: all commands pass. Do not rerun the full gate unless code changes afterward.

- [ ] **Step 4: Push exactly one candidate and create the focused PR**

Run:

```bash
git push -u origin fix/npm-scoped-platform-packages-v0.2.1
gh pr create \
  --base main \
  --head fix/npm-scoped-platform-packages-v0.2.1 \
  --title "fix: complete npm v0.2.1 with scoped native packages" \
  --body "Completes npm v0.2.1 with an unscoped launcher and five scoped native packages. Reuses immutable v0.2.1 binaries, verifies Windows npm shims, removes the spam-blocked unscoped topology, and keeps native-first/launcher-last provenance publication."
```

Expected: one non-draft PR URL.

- [ ] **Step 5: Monitor required checks and merge without another branch update**

Run:

```bash
pr_number="$(gh pr view --json number --jq .number)"
gh pr checks "${pr_number}" --watch --fail-fast
gh pr merge "${pr_number}" --squash --delete-branch
git fetch origin main
main_commit="$(git rev-parse origin/main)"
test "$(gh api repos/krkarma777/ai-cli-gateway/git/ref/heads/main --jq .object.sha)" = "${main_commit}"
```

Expected: required checks pass and the squashed recovery commit is the live main head.

---

### Task 7: Remove the Partial Cohort, Publish Scoped v0.2.1, and Verify Installation

**Files:**

- External npm registry and GitHub Actions state only.

**Interfaces:**

- Deletes: four exact unscoped `name@0.2.1` records.
- Creates: five scoped native packages and `ai-cli-gateway`, all at `0.2.1`.
- Consumes: merged main commit, immutable v0.2.1 assets, stored `NPM_TOKEN`, and npm account `krkarma777`.

- [ ] **Step 1: Perform exact non-destructive registry prechecks**

Run:

```bash
registry=https://registry.npmjs.org/
npm_cache_dir="$(mktemp -d /private/tmp/ai-cli-gateway-npm-release.XXXXXX)"
test "$(NPM_CONFIG_CACHE="${npm_cache_dir}" npm whoami --registry="${registry}")" = krkarma777

old_packages=(
  ai-cli-gateway-darwin-x64
  ai-cli-gateway-darwin-arm64
  ai-cli-gateway-linux-x64
  ai-cli-gateway-linux-arm64
)
for package_name in "${old_packages[@]}"; do
  test "$(NPM_CONFIG_CACHE="${npm_cache_dir}" npm view "${package_name}@0.2.1" version --registry="${registry}")" = 0.2.1
  owner_output="$(NPM_CONFIG_CACHE="${npm_cache_dir}" npm owner ls "${package_name}" --registry="${registry}" 2>/dev/null)"
  test "$(wc -l <<<"${owner_output}" | tr -d ' ')" = 1
  test "$(awk '{print $1}' <<<"${owner_output}")" = krkarma777
done

new_packages=(
  @krkarma777/ai-cli-gateway-darwin-x64
  @krkarma777/ai-cli-gateway-darwin-arm64
  @krkarma777/ai-cli-gateway-linux-x64
  @krkarma777/ai-cli-gateway-linux-arm64
  @krkarma777/ai-cli-gateway-win32-x64
  ai-cli-gateway
)
assert_absent() {
  local package_name="$1" stdout_file stderr_file
  stdout_file="$(mktemp /private/tmp/ai-cli-gateway-npm-view.stdout.XXXXXX)"
  stderr_file="$(mktemp /private/tmp/ai-cli-gateway-npm-view.stderr.XXXXXX)"
  if NPM_CONFIG_CACHE="${npm_cache_dir}" npm view \
    "${package_name}@0.2.1" version --json --registry="${registry}" \
    >"${stdout_file}" 2>"${stderr_file}"; then
    return 1
  fi
  node -e 'const f=require("node:fs"),v=JSON.parse(f.readFileSync(process.argv[1],"utf8"));if(v?.error?.code!=="E404")process.exit(1);' "${stdout_file}"
  grep -Fx 'npm error code E404' "${stderr_file}" >/dev/null
}
for package_name in "${new_packages[@]}"; do
  assert_absent "${package_name}"
done
```

Expected: identity is `krkarma777`, all four old versions exist, and all six new versions are absent.

- [ ] **Step 2: Unpublish only the four approved exact versions**

Run:

```bash
for package_name in "${old_packages[@]}"; do
  NPM_CONFIG_CACHE="${npm_cache_dir}" npm unpublish \
    "${package_name}@0.2.1" \
    --force \
    --registry="${registry}"
done
```

Expected: npm confirms removal of each exact version. If npm requests browser or OTP authentication, complete that account challenge and rerun only the exact command that did not report success.

- [ ] **Step 3: Require all abandoned versions to be absent**

Run:

```bash
for package_name in "${old_packages[@]}"; do
  assert_absent "${package_name}"
done
```

Expected: all four lookups fail with absence; do not delete any GitHub object.

- [ ] **Step 4: Dispatch the exact merged-main recovery workflow and watch its numeric run**

Run:

```bash
run_url="$(gh workflow run npm-release.yml --ref main -f tag=v0.2.1 | tail -n 1)"
run_id="${run_url##*/}"
[[ "${run_id}" =~ ^[1-9][0-9]*$ ]]
gh run watch "${run_id}" --exit-status
```

Expected: package and publish jobs pass; all five scoped natives publish before the launcher.

- [ ] **Step 5: Verify exact public versions, provenance fields, and clean installation**

Run:

```bash
for package_name in "${new_packages[@]}"; do
  test "$(NPM_CONFIG_CACHE="${npm_cache_dir}" npm view "${package_name}@0.2.1" version --registry="${registry}")" = 0.2.1
  NPM_CONFIG_CACHE="${npm_cache_dir}" npm view "${package_name}@0.2.1" dist.integrity repository.url --json --registry="${registry}"
  attestations="$(NPM_CONFIG_CACHE="${npm_cache_dir}" npm view "${package_name}@0.2.1" dist.attestations --json --registry="${registry}")"
  node -e 'const v=JSON.parse(process.argv[1]);if(typeof v?.url!=="string"||!v.url.startsWith("https://registry.npmjs.org/-/npm/v1/attestations/")||v?.provenance?.predicateType!=="https://slsa.dev/provenance/v1")process.exit(1);' "${attestations}"
done

install_root="$(mktemp -d /private/tmp/ai-cli-gateway-npm-install.XXXXXX)"
NPM_CONFIG_CACHE="${npm_cache_dir}" npm install \
  --global \
  --include=optional \
  --ignore-scripts \
  --no-audit \
  --no-fund \
  --prefix "${install_root}" \
  ai-cli-gateway@0.2.1 \
  --registry="${registry}"
version_output="$("${install_root}/bin/ai-cli-gateway" version)"
[[ "${version_output}" = "ai-cli-gateway v0.2.1 (7d5cf2911b3394e564842697b03b1fc9a1162630, "*')' ]]
```

Expected: six public versions exist, every SRI is a SHA-512 value, repository points to `krkarma777/ai-cli-gateway`, and the installed launcher executes the immutable v0.2.1 binary.

- [ ] **Step 6: Close the superseded v0.2.2 draft after accounting for its work**

Run:

```bash
gh pr close 12 --comment "Superseded by the completed scoped-native v0.2.1 recovery. The approved discoverability and Windows launcher work was carried into the focused recovery; no v0.2.2 package cohort was published."
```

Expected: PR #12 is closed without merging; the completed npm release remains `0.2.1`.

- [ ] **Step 7: Record final clean state**

Run:

```bash
git -C /Users/krkarma777/Dev/ai-cli-gateway fetch origin main
git -C /Users/krkarma777/Dev/ai-cli-gateway status --short --branch
gh run view "${run_id}" --json conclusion,url,headSha,event,workflowName
```

Expected: npm release conclusion is `success`; main points at the merged recovery commit; no uncommitted repository changes were created by publication.
