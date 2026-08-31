# npm Distribution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use
> superpowers:subagent-driven-development (recommended) or
> superpowers:executing-plans to implement this plan task-by-task. Steps use
> checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish AI CLI Gateway as `ai-cli-gateway@0.2.1` with one exact
platform-native optional dependency and no install-time executable download.

**Architecture:** A dependency-free Node launcher selects one of five exact
native npm packages, validates its package and binary, and spawns it without a
shell. A release-triggered GitHub Actions workflow deterministically rebuilds
the immutable GitHub Release binaries, proves archive equality, stages and
inspects six npm tarballs, and publishes native packages before the launcher.

**Tech Stack:** Node.js 22.14.0/24.13.0 built-ins, npm 11.6.2, Go 1.26.5,
GitHub Actions, npm provenance, Go repository-security contract tests,
actionlint 1.7.12.

## Global Constraints

- Approved design:
  `docs/superpowers/specs/2026-08-28-npm-distribution-design.md`.
- First npm/GitHub version: `0.2.1` / `v0.2.1`.
- Public command and launcher package: `ai-cli-gateway`.
- Native packages:
  `ai-cli-gateway-darwin-x64`,
  `ai-cli-gateway-darwin-arm64`,
  `ai-cli-gateway-linux-x64`,
  `ai-cli-gateway-linux-arm64`, and
  `ai-cli-gateway-win32-x64`.
- Consumer Node floor: `>=22.14.0`.
- Publication Node/npm/Go: `24.13.0` / `11.6.2` / `1.26.5`.
- JavaScript code may import only Node built-ins. No runtime or development
  registry dependency may be added.
- Public package manifests have no `scripts` field. npm lifecycle scripts stay
  disabled in every install, pack, and CI command.
- No npm code downloads a binary, invokes a shell for the native process,
  searches `PATH` for a fallback, or reads provider credentials.
- GitHub Release remains the binary authority. npm native content must be
  proven identical by deterministic archive equality.
- Do not change or move the immutable `v0.2.0` tag or release.
- Local `.git` metadata is read-only in this workspace. Preserve the commit
  boundaries below by creating equivalent commits on the existing remote
  `feat/npm-distribution-v0.2.1` branch after each verified task.

---

### Task 1: Lock Package Metadata and Source Topology

**Files:**
- Create: `npm/package.json`
- Create: `npm/package-lock.json`
- Create: `npm/scripts/package-config.js`
- Create: `npm/test/package-contract.test.js`
- Create: `npm/launcher/package.json`
- Create: `npm/launcher/README.md`
- Create: `npm/platforms/darwin-x64/package.json`
- Create: `npm/platforms/darwin-x64/README.md`
- Create: `npm/platforms/darwin-arm64/package.json`
- Create: `npm/platforms/darwin-arm64/README.md`
- Create: `npm/platforms/linux-x64/package.json`
- Create: `npm/platforms/linux-x64/README.md`
- Create: `npm/platforms/linux-arm64/package.json`
- Create: `npm/platforms/linux-arm64/README.md`
- Create: `npm/platforms/win32-x64/package.json`
- Create: `npm/platforms/win32-x64/README.md`
- Modify: `.gitignore`

**Interfaces:**
- Produces: `PACKAGE_VERSION: "0.2.1"`,
  `LAUNCHER_NAME: "ai-cli-gateway"`, and immutable `TARGETS` records with
  `key`, `packageName`, `platform`, `arch`, `goos`, `goarch`,
  `stagingDirectory`, and `executable`.
- Consumes: repository `LICENSE` and the approved package table.

- [ ] **Step 1: Write the failing metadata contract**

Create `npm/test/package-contract.test.js` with a closed manifest comparison.
The target table must be literal so adding a sixth target cannot pass by
changing production configuration alone:

~~~js
import assert from "node:assert/strict";
import { readFile } from "node:fs/promises";
import path from "node:path";
import test from "node:test";
import { fileURLToPath } from "node:url";

const npmRoot = path.dirname(fileURLToPath(new URL("../package.json", import.meta.url)));
const version = "0.2.1";
const targets = [
  ["darwin-x64", "ai-cli-gateway-darwin-x64", "darwin", "x64"],
  ["darwin-arm64", "ai-cli-gateway-darwin-arm64", "darwin", "arm64"],
  ["linux-x64", "ai-cli-gateway-linux-x64", "linux", "x64"],
  ["linux-arm64", "ai-cli-gateway-linux-arm64", "linux", "arm64"],
  ["win32-x64", "ai-cli-gateway-win32-x64", "win32", "x64"],
];

async function manifest(relative) {
  return JSON.parse(await readFile(path.join(npmRoot, relative, "package.json"), "utf8"));
}

test("launcher has five exact native optional dependencies and no scripts", async () => {
  const value = await manifest("launcher");
  assert.equal(value.name, "ai-cli-gateway");
  assert.equal(value.version, version);
  assert.deepEqual(value.bin, { "ai-cli-gateway": "bin/ai-cli-gateway.js" });
  assert.deepEqual(value.engines, { node: ">=22.14.0" });
  assert.equal(Object.hasOwn(value, "scripts"), false);
  assert.deepEqual(
    value.optionalDependencies,
    Object.fromEntries(targets.map(([, name]) => [name, version])),
  );
});

for (const [directory, name, os, cpu] of targets) {
  test(name + " has one exact host constraint", async () => {
    const value = await manifest(path.join("platforms", directory));
    assert.equal(value.name, name);
    assert.equal(value.version, version);
    assert.deepEqual(value.os, [os]);
    assert.deepEqual(value.cpu, [cpu]);
    assert.equal(Object.hasOwn(value, "scripts"), false);
    assert.equal(Object.hasOwn(value, "bin"), false);
    assert.equal(Object.hasOwn(value, "dependencies"), false);
  });
}
~~~

- [ ] **Step 2: Run the metadata contract and verify RED**

Run:

~~~bash
node --test npm/test/package-contract.test.js
~~~

Expected: FAIL with `ENOENT` for `npm/launcher/package.json`. A syntax error is
not an acceptable RED result.

- [ ] **Step 3: Add the closed target configuration**

Create `npm/scripts/package-config.js` with this public interface and exact
data:

~~~js
export const PACKAGE_VERSION = "0.2.1";
export const LAUNCHER_NAME = "ai-cli-gateway";
export const NODE_RANGE = ">=22.14.0";
export const TARGETS = Object.freeze([
  Object.freeze({ key: "darwin-x64", packageName: "ai-cli-gateway-darwin-x64", platform: "darwin", arch: "x64", goos: "darwin", goarch: "amd64", stagingDirectory: "darwin_amd64", executable: "ai-cli-gateway" }),
  Object.freeze({ key: "darwin-arm64", packageName: "ai-cli-gateway-darwin-arm64", platform: "darwin", arch: "arm64", goos: "darwin", goarch: "arm64", stagingDirectory: "darwin_arm64", executable: "ai-cli-gateway" }),
  Object.freeze({ key: "linux-x64", packageName: "ai-cli-gateway-linux-x64", platform: "linux", arch: "x64", goos: "linux", goarch: "amd64", stagingDirectory: "linux_amd64", executable: "ai-cli-gateway" }),
  Object.freeze({ key: "linux-arm64", packageName: "ai-cli-gateway-linux-arm64", platform: "linux", arch: "arm64", goos: "linux", goarch: "arm64", stagingDirectory: "linux_arm64", executable: "ai-cli-gateway" }),
  Object.freeze({ key: "win32-x64", packageName: "ai-cli-gateway-win32-x64", platform: "win32", arch: "x64", goos: "windows", goarch: "amd64", stagingDirectory: "windows_amd64", executable: "ai-cli-gateway.exe" }),
]);

export function targetFor(platform, arch) {
  return TARGETS.find((target) => target.platform === platform && target.arch === arch);
}
~~~

- [ ] **Step 4: Add the private harness and exact public manifests**

Set `npm/package.json` to:

~~~json
{
  "name": "ai-cli-gateway-npm-contract",
  "version": "0.0.0",
  "private": true,
  "type": "module",
  "engines": {
    "node": ">=22.14.0"
  },
  "scripts": {
    "test": "node --test --test-concurrency=1 ./test/*.test.js"
  }
}
~~~

Generate and commit `npm/package-lock.json` with:

~~~bash
npm install --package-lock-only --ignore-scripts --prefix npm
~~~

The launcher manifest must contain the exact five optional dependency entries:

~~~json
{
  "name": "ai-cli-gateway",
  "version": "0.2.1",
  "description": "Run AI CLI Gateway through the matching native binary.",
  "license": "MIT",
  "type": "module",
  "bin": {
    "ai-cli-gateway": "bin/ai-cli-gateway.js"
  },
  "files": ["bin/ai-cli-gateway.js", "lib/launcher.js", "README.md", "LICENSE"],
  "engines": {
    "node": ">=22.14.0"
  },
  "optionalDependencies": {
    "ai-cli-gateway-darwin-x64": "0.2.1",
    "ai-cli-gateway-darwin-arm64": "0.2.1",
    "ai-cli-gateway-linux-x64": "0.2.1",
    "ai-cli-gateway-linux-arm64": "0.2.1",
    "ai-cli-gateway-win32-x64": "0.2.1"
  },
  "repository": {
    "type": "git",
    "url": "git+https://github.com/krkarma777/ai-cli-gateway.git",
    "directory": "npm/launcher"
  },
  "homepage": "https://github.com/krkarma777/ai-cli-gateway#readme",
  "bugs": {
    "url": "https://github.com/krkarma777/ai-cli-gateway/issues"
  },
  "publishConfig": {
    "access": "public",
    "provenance": true,
    "registry": "https://registry.npmjs.org/"
  }
}
~~~

Create each native manifest by applying this complete object function to every
record in `TARGETS` and serializing with two-space indentation plus one trailing
newline:

~~~js
function nativeManifest(target) {
  return {
    name: target.packageName,
    version: "0.2.1",
    description: `Native AI CLI Gateway binary for ${target.platform}-${target.arch}.`,
    license: "MIT",
    files: [`bin/${target.executable}`, "README.md", "LICENSE"],
    engines: { node: ">=22.14.0" },
    os: [target.platform],
    cpu: [target.arch],
    repository: {
      type: "git",
      url: "git+https://github.com/krkarma777/ai-cli-gateway.git",
      directory: `npm/platforms/${target.key}`,
    },
    homepage: "https://github.com/krkarma777/ai-cli-gateway#readme",
    bugs: { url: "https://github.com/krkarma777/ai-cli-gateway/issues" },
    publishConfig: {
      access: "public",
      provenance: true,
      registry: "https://registry.npmjs.org/",
    },
  };
}
~~~

The closed `TARGETS` array makes all five outputs exact. Each platform README
states its exact package name and target, that it is installed through
`ai-cli-gateway`, and that it must not be used directly. The launcher README
documents the global install command, five targets, Node floor, and absence of
lifecycle downloads.

Append these repository-local outputs to `.gitignore`:

~~~gitignore

# npm dependency and package output
/npm/node_modules/
/npm/**/*.tgz
/npm/.staging/
~~~

- [ ] **Step 5: Run the metadata contract and verify GREEN**

Run:

~~~bash
npm ci --ignore-scripts --prefix npm
node --test npm/test/package-contract.test.js
~~~

Expected: npm installs zero registry dependencies; all six manifest tests pass.

- [ ] **Step 6: Commit the metadata boundary**

Commit message:

~~~text
feat: define npm package topology
~~~

---

### Task 2: Implement the Native Launcher with Real-Process Tests

**Files:**
- Create: `npm/test/launcher.test.js`
- Create: `npm/launcher/lib/launcher.js`
- Create: `npm/launcher/bin/ai-cli-gateway.js`

**Interfaces:**
- Consumes: `targetFor(platform, arch)` and the exact source manifests.
- Produces:
  `resolveNative({ launcherRoot, platform, arch })` resolving an object with
  `binary` and `version`,
  `spawnNative(binary, args)` resolving an object with `code` and `signal`,
  and `run(argv, options)` resolving a numeric exit code.

- [ ] **Step 1: Write failing platform and filesystem tests**

In `npm/test/launcher.test.js`, import the missing launcher module and cover
these exact cases with `node:test` and real temporary files:

~~~js
test("selects all five exact platform packages", () => {
  assert.equal(targetFor("darwin", "x64").packageName, "ai-cli-gateway-darwin-x64");
  assert.equal(targetFor("darwin", "arm64").packageName, "ai-cli-gateway-darwin-arm64");
  assert.equal(targetFor("linux", "x64").packageName, "ai-cli-gateway-linux-x64");
  assert.equal(targetFor("linux", "arm64").packageName, "ai-cli-gateway-linux-arm64");
  assert.equal(targetFor("win32", "x64").packageName, "ai-cli-gateway-win32-x64");
  assert.equal(targetFor("freebsd", "x64"), undefined);
});

test("rejects a native package version mismatch", async () => {
  const fixture = await installedFixture({ nativeVersion: "0.2.0" });
  await assert.rejects(
    resolveNative({ launcherRoot: fixture.launcherRoot, platform: process.platform, arch: process.arch }),
    { code: "INVALID_NATIVE" },
  );
});

test("rejects a linked native executable", async (t) => {
  if (process.platform === "win32") t.skip("Windows link policy uses the real-file contract");
  const fixture = await installedFixture();
  await replaceBinaryWithSymlink(fixture.binary);
  await assert.rejects(
    resolveNative({ launcherRoot: fixture.launcherRoot, platform: process.platform, arch: process.arch }),
    { code: "INVALID_NATIVE" },
  );
});
~~~

The same file adds separate tests for missing optional package, wrong package
name, missing binary, directory in place of binary, containment escape, and
missing POSIX execute bits. `installedFixture` copies `process.execPath` to the
expected native executable path so tests exercise a real regular executable on
every host.

- [ ] **Step 2: Run the focused launcher test and verify RED**

Run:

~~~bash
node --test npm/test/launcher.test.js
~~~

Expected: FAIL with `ERR_MODULE_NOT_FOUND` for
`npm/launcher/lib/launcher.js`.

- [ ] **Step 3: Implement closed native resolution**

Implement `npm/launcher/lib/launcher.js` with these exact error codes and
messages:

~~~js
const MESSAGES = Object.freeze({
  INVALID_LAUNCHER: "ai-cli-gateway: launcher installation is invalid",
  INVALID_NATIVE: "ai-cli-gateway: native package installation is invalid",
  SPAWN_FAILED: "ai-cli-gateway: native executable could not be started",
});

export class LauncherError extends Error {
  constructor(code, message) {
    super(message);
    this.name = "LauncherError";
    this.code = code;
  }
}
~~~

`resolveNative` performs these exact operations:

1. Parse the launcher's `package.json` and require
   `name === "ai-cli-gateway"` plus canonical `MAJOR.MINOR.PATCH`.
2. Select a target from a duplicated frozen five-entry runtime map. Published
   launcher code cannot import `npm/scripts/package-config.js`.
3. Resolve `target.packageName + "/package.json"` with `createRequire` rooted
   at the installed launcher's `lib/launcher.js`.
4. Use `lstat` and `realpath` to require the executable is the exact non-link
   regular file `path.join(nativeRoot, "bin", target.executable)`.
5. Compare device, inode, size, type, name, version, and POSIX execute bits
   before returning.
6. Translate only module-not-found for the expected package into
   `MISSING_NATIVE`; all other validation failures become `INVALID_NATIVE`
   without exposing paths.

Sanitize runtime values with `^[a-z0-9]{1,16}$` and use the exact unsupported
message:

~~~text
ai-cli-gateway: unsupported platform "{sanitizedPlatform}-{sanitizedArch}"; supported: darwin-x64, darwin-arm64, linux-x64, linux-arm64, win32-x64
~~~

Use this exact missing-package message:

~~~text
ai-cli-gateway: native package {target.packageName}@{launcherVersion} is missing; reinstall with "npm install --global ai-cli-gateway@{launcherVersion}" without --omit=optional
~~~

- [ ] **Step 4: Add failing real-process behavior tests**

Stage a complete launcher fixture, copy `process.execPath` as the native
executable, and pass a fixture JavaScript file as the first gateway argument:

~~~js
test("preserves argument boundaries and numeric exit status", async () => {
  const fixture = await runnableFixture(
    'process.stdout.write(JSON.stringify(process.argv.slice(2))); process.exit(23);',
  );
  const args = ["space value", "quote'\"", "$(not-a-shell)"];
  const result = await runInstalled(fixture, args);
  assert.equal(result.status, 23);
  assert.equal(result.stdout, JSON.stringify(args));
  assert.equal(result.stderr, "");
});
~~~

Add independent stdin/stdout/stderr inheritance and POSIX `SIGINT`/`SIGTERM`
tests. Signal tests send the signal to the wrapper PID only, require the child
to observe it, and require the wrapper result's `signal` to match. Skip only the
two signal assertions on Windows.

- [ ] **Step 5: Verify process tests fail for missing execution code**

Run:

~~~bash
node --test npm/test/launcher.test.js
~~~

Expected: filesystem-selection tests pass and process tests fail because
`spawnNative` or `run` is absent.

- [ ] **Step 6: Implement process forwarding and the bin entry**

`spawnNative` calls
`spawn(binary, args, { shell: false, stdio: "inherit", windowsHide: false })`.
It registers one-shot `SIGINT` and `SIGTERM` forwarding while the child lives,
removes every handler on error or exit, and resolves
`{ code, signal }`. `run` prints only `LauncherError.message` and preserves the
native numeric status or re-signals itself with the native signal.

Create the executable entry:

~~~js
#!/usr/bin/env node
import { main } from "../lib/launcher.js";

await main(process.argv.slice(2));
~~~

Set `npm/launcher/bin/ai-cli-gateway.js` mode to `0755`.

- [ ] **Step 7: Run launcher and complete npm tests GREEN**

Run:

~~~bash
node --test npm/test/launcher.test.js
npm test --prefix npm
~~~

Expected: every target, error, stream, argument, exit, and supported signal
test passes with no warning or stack trace.

- [ ] **Step 8: Commit the launcher**

Commit message:

~~~text
feat: add native npm launcher
~~~

---

### Task 3: Stage and Inspect Reproducible npm Tarballs

**Files:**
- Create: `npm/test/stage-packages.test.js`
- Create: `npm/scripts/stage-packages.js`
- Create: `npm/scripts/verify-packages.js`

**Interfaces:**
- Consumes: `PACKAGE_VERSION`, `TARGETS`, source package trees, repository
  `LICENSE`, and release staging directories.
- Produces:
  `stagePackages(options)` resolving an array of `StagedPackage` records and
  `packAndVerify(options)` resolving an array of `PackageDescriptor` records.
- `PackageDescriptor` contains only `name`, `version`, `filename`,
  `integrity`, `shasum`, `size`, and sorted `files`.

- [ ] **Step 1: Write failing staging security tests**

Create real temporary repository, binary, and output roots. Test one successful
all-target staging plan and separate failures for relative or linked roots,
pre-existing output, a group- or other-writable parent on POSIX (including a
sticky world-writable parent), missing or linked binaries, source version
mismatch, unexpected source files, duplicate targets, and output-root
replacement.

The successful assertion is:

~~~js
const staged = await stagePackages({
  repositoryRoot,
  binaryRoot,
  outputRoot,
  version: "0.2.1",
});
assert.deepEqual(
  staged.map(({ name }) => name),
  [
    "ai-cli-gateway-darwin-x64",
    "ai-cli-gateway-darwin-arm64",
    "ai-cli-gateway-linux-x64",
    "ai-cli-gateway-linux-arm64",
    "ai-cli-gateway-win32-x64",
    "ai-cli-gateway",
  ],
);
~~~

- [ ] **Step 2: Verify the staging test RED**

Run:

~~~bash
node --test npm/test/stage-packages.test.js
~~~

Expected: FAIL with `ERR_MODULE_NOT_FOUND` for
`npm/scripts/stage-packages.js`.

- [ ] **Step 3: Implement private staging**

> **Approved superseding decision — 2026-08-31.** This replaces the sibling
> `mkdtemp` plus final-directory-rename design below. Portable Node filesystem
> APIs do not provide an atomic, no-replace directory rename: an ordinary
> rename can replace an existing destination on supported platforms. Staging
> therefore acquires the absent final `outputRoot` directly with exclusive
> creation, fixes it to mode `0700`, and writes only beneath that captured root.
>
> Publication is marker-gated. `.complete` is acquired with exclusive creation
> and kept invalid (empty, with the final `0644` mode) while every fallible or
> asynchronous source/binary/root validation and file, descendant-directory,
> output-root, and output-parent sync runs. After the last validation, one
> synchronous commit writes the exact marker bytes and returns without another
> awaited or rejecting operation. A crash before or during that write can
> leave no marker or an empty/partial marker; consumers accept only the exact
> marker content, so those states fail closed. A crash may still lose marker
> durability, but it cannot turn an empty or partial marker into an accepted
> one. Failures retain the partial final root and invalid marker for inspection;
> the staging code never recursively cleans up an externally reachable path.
>
> On POSIX, the actual output parent must be a canonical directory owned by the
> current uid with neither group-write nor other-write permission. Sticky
> world-writable directories are not an exception. The release workflow must
> create this parent privately for the job before staging. On Windows, the
> caller must likewise provide a trusted private temporary root; staging still
> applies the canonical, non-link, stable-identity, and ownership checks that
> Node exposes, but it does not infer ACL isolation from POSIX mode bits.
> The verifier applies the same trust rule to the parent in which it creates
> the retained npm scratch-home sibling of `tarballRoot`: on POSIX that parent
> must be canonical, current-uid-owned, and have neither group-write nor
> other-write permission, with no sticky-directory exception. Its identity,
> type, ownership, and write-bit policy are rechecked before and after every npm
> child and in the final verification window. On Windows the caller must supply
> this trusted private parent because POSIX mode bits do not establish its ACL.
>
> Reviewer item 1 is accepted as a threat-boundary correction, not as a claim
> of impossible same-uid resistance. Node has no portable `mkdirat`/`openat`
> directory-fd-relative creation primitive or directory lock, so a hostile
> process running as the same uid can still race path replacement and is
> outside this release-job boundary. Synchronous `mkdir`/`open`/`fstat`/`lstat`
> sequences and held descriptors should be used where portable and practical
> to remove event-loop windows, but they narrow the race rather than changing
> that boundary. The completion marker specifically remains held by its file
> descriptor from invalid acquisition through the final synchronous commit.

`stagePackages` uses only Node filesystem APIs and never a shell. Copies use
exclusive destination creation, metadata is mode `0644`, and POSIX
binaries/the launcher entry are mode `0755`. Before and after each copy it
compares `dev`, `ino`, size, file type, and link state. It recursively validates
the captured final root before publication and returns the fixed
`npm package staging failed` error on every pre-publication failure.

The exact command interface is:

~~~console
node npm/scripts/stage-packages.js \
  --repository-root /absolute/repository \
  --binary-root /absolute/release-staging \
  --output-root /absolute/npm-staging \
  --version 0.2.1
~~~

Optional `--target` accepts exactly one of `darwin-x64`, `darwin-arm64`,
`linux-x64`, `linux-arm64`, or `win32-x64` and stages the launcher plus that
native package. No other option is accepted.

- [ ] **Step 4: Add failing npm-pack inspection tests**

Extend `stage-packages.test.js` with a fake npm executable returning one
`npm pack --json` record. Reject extra records, wrong name/version, filename,
SRI, path, mode, file count, launcher scripts, or native dependencies.

Run one real `npm pack --ignore-scripts --json` against the staged launcher and
host-native package. Require these exact packed file sets:

~~~text
ai-cli-gateway:
  LICENSE
  README.md
  bin/ai-cli-gateway.js
  lib/launcher.js
  package.json

native POSIX:
  LICENSE
  README.md
  bin/ai-cli-gateway
  package.json

native Windows:
  LICENSE
  README.md
  bin/ai-cli-gateway.exe
  package.json
~~~

- [ ] **Step 5: Implement tarball verification**

`packAndVerify` spawns an absolute npm executable with:

~~~text
pack --ignore-scripts --json --pack-destination "${NPM_TARBALL_ROOT}"
~~~

Use `shell: false` and a closed environment. Parse exactly one JSON record,
recompute SHA-1 and SHA-512 SRI from the regular non-link tarball, validate
`files[].path` and `files[].mode`, and write canonical `packages.json`. Its
package order is the five native packages followed by the launcher.

> **Approved native-Windows pack-record qualification — 2026-08-31.** npm
> 11.6.2 on native Windows reports the launcher
> `bin/ai-cli-gateway.js` entry as mode `0644` in its `npm pack --json` file
> record. Only host-record validation models that exact Windows, launcher, and
> path-specific reporting boundary. The staged launcher entry and canonical
> release mode remain `0755`; no tarball is rewritten. Ubuntu publication is
> authoritative and must continue to enforce the `0755` launcher entry.

The verifier accepts exactly two command shapes:

~~~console
node npm/scripts/verify-packages.js --source-check
node npm/scripts/verify-packages.js \
  --staging-root /absolute/npm-staging \
  --tarball-root /absolute/npm-tarballs \
  --descriptor /absolute/npm-tarballs/packages.json \
  --version 0.2.1
~~~

`--source-check` validates tracked manifests, README files, launcher files, and
the absence of tracked native binaries without creating a tarball. The staging
form packs and verifies exact staged packages. Mixing forms or adding an option
fails.

- [ ] **Step 6: Run staging and pack tests GREEN**

Run:

~~~bash
node --test npm/test/stage-packages.test.js
npm test --prefix npm
~~~

Expected: adversarial fixtures fail closed; real host tarballs pass; two
independent roots produce identical `packages.json` except for absolute output
locations, which must not appear in the descriptor.

- [ ] **Step 7: Commit packaging**

Commit message:

~~~text
feat: stage verified npm packages
~~~

---

### Task 4: Add npm Runtime and Host-install CI Gates

**Files:**
- Modify: `internal/securitytest/repository_test.go`
- Modify: `.github/workflows/ci.yml`
- Modify: `Makefile`

**Interfaces:**
- Consumes: package tests and staging/verification CLIs from Tasks 1–3.
- Produces: CI jobs `npm-contract` and `npm-host-install`.

- [ ] **Step 1: Extend the closed CI contract first**

Change `TestWorkflowMultiPlatformReleaseContract` and
`expectedCIJobActions` to require exactly eight jobs:

~~~go
wantJobs := map[string]struct{}{
    "lint": {}, "linux": {}, "macos": {}, "windows": {},
    "cross-build": {}, "sdk-contract": {},
    "npm-contract": {}, "npm-host-install": {},
}
~~~

Require `npm-contract` to use checkout plus setup-node, Node
`22.14.0`/`24.13.0`, `npm ci --ignore-scripts --prefix npm`, and
`npm test --prefix npm`.

Require `npm-host-install` to use checkout, setup-go, and setup-node with these
three exact host records:

~~~text
ubuntu-24.04  linux-x64     linux/amd64    ai-cli-gateway
macos-15      darwin-arm64  darwin/arm64   ai-cli-gateway
windows-2025  win32-x64     windows/amd64  ai-cli-gateway.exe
~~~

Add mutation cases that remove `--ignore-scripts`, loosen a Node version, omit
a host, add a secret, enable continue-on-error, use a moving action tag, skip
host execution, or change `v0.2.1`.

- [ ] **Step 2: Run the focused CI contract and verify RED**

Run:

~~~bash
go test -count=1 ./internal/securitytest \
  -run '^(TestWorkflowMultiPlatformReleaseContract|TestWorkflowMultiPlatformReleaseContractRejectsMutations)$'
~~~

Expected: FAIL because the two npm jobs are absent.

- [ ] **Step 3: Add the two CI jobs**

`npm-contract` runs on `ubuntu-24.04`, times out after 8 minutes, and has an
exact two-entry Node matrix. setup-node uses the existing SHA-pinned action,
the matrix version, and `package-manager-cache: false`.

`npm-host-install` runs on the exact three-entry matrix above and times out
after 15 minutes. It builds a host binary with `CGO_ENABLED=0` and release-style
v0.2.1 ldflags, stages only the host target, packs both packages, and installs:

~~~text
npm install --global --ignore-scripts --no-audit --no-fund \
  --prefix "${NPM_INSTALL_PREFIX}" "${NATIVE_TARBALL}" "${LAUNCHER_TARBALL}"
~~~

Execute npm's generated command shim and require:

~~~text
^ai-cli-gateway v0[.]2[.]1 [(][0-9a-f]{40}, [0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z[)]$
~~~

Use `shell: bash` and forward-slash paths on all hosts. No provider CLI or
credential is available.

- [ ] **Step 4: Add Makefile npm gates**

Add:

~~~make
.PHONY: npm-test npm-pack-check

npm-test:
	npm ci --ignore-scripts --prefix npm
	npm test --prefix npm

npm-pack-check:
	node npm/scripts/verify-packages.js --source-check
~~~

Include both targets in `verify` after lint and before Go tests.

- [ ] **Step 5: Run focused contracts and local npm gates GREEN**

Run:

~~~bash
make npm-test
make npm-pack-check
go test -count=1 ./internal/securitytest \
  -run '^(TestWorkflowMultiPlatformReleaseContract|TestWorkflowMultiPlatformReleaseContractRejectsMutations)$'
~~~

Expected: all pass. The current macOS host contract also executes the staged Go
binary through npm's shim.

- [ ] **Step 6: Commit CI gates**

Commit message:

~~~text
ci: verify npm package installation
~~~

---

### Task 5: Add the Closed npm Publication Workflow

**Files:**
- Create: `.github/workflows/npm-release.yml`
- Modify: `internal/securitytest/repository_test.go`

**Interfaces:**
- Consumes: immutable GitHub Release `v0.2.1`, releasepack, npm staging and
  verifier, and `NPM_TOKEN` only for first publication.
- Produces: one internal artifact containing six npm tarballs and
  `packages.json`; publishes native packages first and launcher last.

- [ ] **Step 1: Write a closed workflow parser and mutation tests**

Add `TestNPMReleaseWorkflowContract` and
`TestNPMReleaseWorkflowContractRejectsMutations`. Parse only known YAML fields,
following `parseClosedReleaseWorkflow`. Require:

~~~yaml
on:
  release:
    types:
      - published
permissions: {}
~~~

Require exact concurrency
`npm-release-${{ github.repository }}-${{ github.event.release.tag_name }}`,
`cancel-in-progress: false`, and exactly:

~~~text
package: ubuntu-24.04, 25 minutes, contents:read
publish: ubuntu-24.04, 10 minutes, contents:read + id-token:write, needs package
~~~

Allow only pinned checkout, setup-go, setup-node, upload-artifact, and
download-artifact actions with exact counts and inputs. Reject mutation of the
event, release validation, permissions, tool versions, asset allowlist,
deterministic rebuild, checksums, attestations, artifact identity, pack
inspection, publication order, SRI comparison, `--provenance`,
`--access public`, or launcher-last.

Also change `validateCIActionlintStep` and its mutation cases to require this
one exact target sequence:

~~~text
.github/workflows/ci.yml .github/workflows/release.yml .github/workflows/npm-release.yml
~~~

- [ ] **Step 2: Verify the npm workflow test RED**

Run:

~~~bash
go test -count=1 ./internal/securitytest \
  -run '^(TestNPMReleaseWorkflowContract|TestNPMReleaseWorkflowContractRejectsMutations|TestWorkflowMultiPlatformReleaseContract|TestWorkflowMultiPlatformReleaseContractRejectsMutations)$'
~~~

Expected: FAIL because `.github/workflows/npm-release.yml` and the third
actionlint target are missing.

- [ ] **Step 3: Implement the package job**

Create `npm-release.yml` with the exact event and two-job authority split. The
same change adds the third actionlint target to `ci.yml`. The package job:

1. Validates non-draft, non-prerelease, immutable release state, exact
   repository, canonical tag, tag commit, and manifest version.
2. Checks out the tag commit with `persist-credentials: false` and
   `fetch-depth: 0`.
3. Verifies Node `v24.13.0`, npm `11.6.2`, and Go `go1.26.5`.
4. Downloads exactly five archives, the SPDX SBOM, and `SHA256SUMS` into a
   private root with `gh release download`.
5. Checks API asset name, size, and the exact `sha256:` plus 64-lowercase-hex
   digest; runs strict checksums and
   `gh attestation verify` with exact repository, predicate, workflow, source
   digest, and source ref.
6. Repeats release.yml's five-target build and releasepack archive command,
   then compares recreated archive SHA-256 values with the published manifest.
7. Stages and verifies six npm packages, then installs and executes the Linux
   x64 pair.
8. Uploads exactly six tarballs and `packages.json` with compression level 0,
   one-day retention, no overwrite, no hidden files, and missing-file failure.
9. Exposes only validated numeric artifact ID and 64-lowercase-hex digest.

- [ ] **Step 4: Implement idempotent native-first publication**

The publish job downloads only the artifact ID from `package` and requires
artifact digest matching. It re-hashes every tarball before registry queries.

For each fixed-order descriptor, query
`npm view "${PACKAGE_NAME}@${PACKAGE_VERSION}" dist.integrity --json`. Treat
only npm E404 as
absent. If present, require exact SRI and skip. If absent, execute:

~~~bash
npm publish "${tarball}" --ignore-scripts --access public --provenance
~~~

Set `NODE_AUTH_TOKEN: ${{ secrets.NPM_TOKEN }}` only on this step. An absent
secret permits npm OIDC after trust is configured. Re-read and verify registry
SRI after every publish. Descriptor order is exactly five native packages,
then `ai-cli-gateway`.

- [ ] **Step 5: Validate YAML and workflow contracts GREEN**

Run:

~~~bash
go test -count=1 ./internal/securitytest \
  -run '^(TestNPMReleaseWorkflowContract|TestNPMReleaseWorkflowContractRejectsMutations|TestWorkflowMultiPlatformReleaseContract|TestWorkflowMultiPlatformReleaseContractRejectsMutations)$'
actionlint -config-file /dev/null -shellcheck= -pyflakes= -no-color \
  .github/workflows/ci.yml .github/workflows/release.yml .github/workflows/npm-release.yml
~~~

Expected: all pass. Do not execute `npm publish` locally.

- [ ] **Step 6: Commit publication automation**

Commit message:

~~~text
ci: publish verified npm packages
~~~

---

### Task 6: Document npm Installation and v0.2.1

**Files:**
- Modify: `README.md`
- Modify: `docs/getting-started.md`
- Modify: `docs/reference.md`
- Create: `docs/releases/v0.2.1.md`
- Modify: `internal/securitytest/repository_test.go`

**Interfaces:**
- Consumes: exact package names, targets, version, provenance, and failure
  behavior.
- Produces: current-version documentation without altering historical
  `docs/releases/v0.2.0.md`.

- [ ] **Step 1: Change documentation contracts first**

Add a v0.2.1 release-note contract requiring:

- `npm install --global ai-cli-gateway@0.2.1`;
- all five supported npm targets;
- Node `>=22.14.0`;
- no lifecycle download;
- native binary equality with GitHub Release;
- npm and GitHub provenance;
- five archives, SPDX SBOM, and `SHA256SUMS`;
- tag-pinned documentation links under `v0.2.1`.

Update current README/getting-started expectations from v0.2.0 to v0.2.1.
Retain a test proving the historical v0.2.0 release note remains tag-pinned and
unchanged.

- [ ] **Step 2: Run documentation contracts and verify RED**

Run:

~~~bash
go test -count=1 ./internal/securitytest \
  -run 'ReleaseNotes|Readme|GettingStarted|Documentation|Install'
~~~

Expected: FAIL because v0.2.1 documentation is absent.

- [ ] **Step 3: Update the current installation path**

README Quick Start begins with:

~~~console
npm install --global ai-cli-gateway@0.2.1
ai-cli-gateway version
~~~

Link the manual checksum path immediately after npm. Getting Started adds
supported platforms, Node floor, exact install, update, uninstall,
`npm audit signatures`, and the optional-dependency recovery command. Keep the
complete archive-verification procedure but change its current release and
assets to 0.2.1. Update the reference title to v0.2.1.

Create `docs/releases/v0.2.1.md` with sections: npm installation, supported
targets, supply-chain equivalence and provenance, unchanged gateway runtime,
downloads and verification, and exact tag-pinned links.

- [ ] **Step 4: Run documentation contracts GREEN**

Run:

~~~bash
go test -count=1 ./internal/securitytest \
  -run 'ReleaseNotes|Readme|GettingStarted|Documentation|Install'
~~~

Expected: current v0.2.1 and historical v0.2.0 contracts pass.

- [ ] **Step 5: Commit documentation**

Commit message:

~~~text
docs: add npm installation for v0.2.1
~~~

---

### Task 7: Run the Complete Pre-merge Verification

**Files:**
- Verify all task files.
- Update plan checkboxes only after each command produces fresh evidence.

**Interfaces:**
- Consumes: complete implementation.
- Produces: reviewable verification evidence and a PR-ready remote branch.

- [ ] **Step 1: Run source and npm checks**

Run:

~~~bash
test -z "$(gofmt -l .)"
npm ci --ignore-scripts --prefix npm
npm test --prefix npm
node npm/scripts/verify-packages.js --source-check
git diff --check
~~~

Expected: all exit 0; npm installs no registry dependency.

- [ ] **Step 2: Run focused security and release checks**

Run:

~~~bash
go test -count=1 ./internal/securitytest
go test -count=1 ./internal/releasepack ./internal/releasepack/cmd/releasepack
actionlint -config-file /dev/null -shellcheck= -pyflakes= -no-color \
  .github/workflows/ci.yml .github/workflows/release.yml .github/workflows/npm-release.yml
~~~

Expected: all pass.

- [ ] **Step 3: Run all Go gates**

Run:

~~~bash
go mod verify
go vet ./...
golangci-lint run ./...
go test -count=1 ./...
go test -race -timeout=20m -count=1 ./...
go test -tags=integration -count=1 ./...
go test -trimpath -count=1 ./...
GOFLAGS=-trimpath go test -count=1 ./internal/testutil ./internal/testcli
go test -tags=live -run '^$' ./internal/provider/...
CGO_ENABLED=0 go build -trimpath \
  -o /tmp/ai-cli-gateway-v0.2.1 ./cmd/ai-cli-gateway
~~~

Expected: every gate passes. Run repository hygiene from a clean independent
Git materialization if local IDE state recreates ignored developer files.

- [ ] **Step 4: Exercise the local global-install contract**

Build a release-style macOS binary, stage the current native target and
launcher, pack both, and install into a new private temporary npm prefix with
`--ignore-scripts`. Require `ai-cli-gateway version` to return exact v0.2.1
metadata, run `ai-cli-gateway --help`, and uninstall from that temporary prefix.
Do not modify the user's global npm prefix.

- [ ] **Step 5: Review the complete diff**

Run:

~~~bash
git status --short --branch
git diff --stat
git diff -- . ':!docs/superpowers/**'
git diff --check
~~~

Confirm no binary, tarball, `node_modules`, token, cache, IDE file, provider
credential, or unrelated edit appears.

- [ ] **Step 6: Push verified commits and open the PR**

Update remote `feat/npm-distribution-v0.2.1` without force, verify every remote
blob hash equals the local file, and open a PR titled:

~~~text
feat: distribute AI CLI Gateway through npm
~~~

Wait for every hosted CI job, including the Node and OS matrices. Do not merge,
create `v0.2.1`, or publish npm while any check is pending or failing.

---

### Task 8: Merge, Publish 0.2.1, and Remove Bootstrap Credentials

**Files:**
- Modify after publication: `.github/workflows/npm-release.yml`
- Modify after publication: `internal/securitytest/repository_test.go`

**Interfaces:**
- Consumes: approved PR, green hosted CI, six unclaimed npm names, and a
  user-created short-lived granular npm token.
- Produces: immutable GitHub v0.2.1, six npm 0.2.1 packages, trusted publishers,
  revoked bootstrap credentials, and token-free main.

- [ ] **Step 1: Recheck external preconditions without mutation**

Require each query in this closed loop to return npm E404:

~~~bash
npm_preflight_root="$(mktemp -d /tmp/ai-cli-gateway-npm-preflight.XXXXXX)"
for package_name in \
  ai-cli-gateway-darwin-x64 \
  ai-cli-gateway-darwin-arm64 \
  ai-cli-gateway-linux-x64 \
  ai-cli-gateway-linux-arm64 \
  ai-cli-gateway-win32-x64 \
  ai-cli-gateway
do
  error_file="${npm_preflight_root}/${package_name}.stderr"
  if npm --cache "${npm_preflight_root}/cache" \
    view "${package_name}@0.2.1" version --json \
    >/dev/null 2>"${error_file}"
  then
    printf 'package already exists: %s\n' "${package_name}" >&2
    exit 1
  fi
  grep -F 'E404' "${error_file}" >/dev/null
done
~~~

Also require npm account `krkarma777` to have account-level 2FA, GitHub
`main` to equal the approved merge commit, a clean detached release worktree,
and no local or remote `v0.2.1` tag or GitHub release.

- [ ] **Step 2: Create the one-time publication credential**

The user creates a shortest-practical-expiry granular npm token permitted to
publish new public packages and stores it as repository secret `NPM_TOKEN`.
Never print, read back, or copy the token into the workspace. Confirm only that
the GitHub secret name exists.

- [ ] **Step 3: Run the complete release preflight**

Repeat every Task 7 gate from a clean detached worktree at the exact merge
commit. Require local commit, `origin/main`, and remote main SHA equality.

- [ ] **Step 4: Create and push annotated v0.2.1**

Create the tag only at the verified merge commit and push only that tag. Wait
for the existing Release workflow to publish an immutable GitHub Release, then
wait for `npm-release.yml` to publish all six packages successfully.

- [ ] **Step 5: Verify public npm results**

For every package, require version 0.2.1, public visibility, exact repository,
expected host constraints or optional dependencies, exact `dist.integrity`
from the workflow descriptor, and npm provenance. Install
`ai-cli-gateway@0.2.1` into a clean temporary prefix and run `version` and
`--help`.

- [ ] **Step 6: Configure trusted publishing interactively**

Use npm `>=11.15.0` with account 2FA and run this closed loop:

~~~bash
for package_name in \
  ai-cli-gateway-darwin-x64 \
  ai-cli-gateway-darwin-arm64 \
  ai-cli-gateway-linux-x64 \
  ai-cli-gateway-linux-arm64 \
  ai-cli-gateway-win32-x64 \
  ai-cli-gateway
do
  npm trust github "${package_name}" \
    --file npm-release.yml \
    --repository krkarma777/ai-cli-gateway \
    --allow-publish
  npm trust list "${package_name}"
done
~~~

Require each list result to contain the exact repository, workflow filename,
and publish permission. In npm settings, select “Require two-factor
authentication and disallow tokens” for all six packages.

- [ ] **Step 7: Delete and revoke bootstrap authority**

Delete GitHub secret `NPM_TOKEN`, revoke the granular npm token, and verify both
are absent. Delete only after all six trusted relationships are confirmed.

- [ ] **Step 8: Remove token fallback from main**

Write a failing workflow contract assertion rejecting `NODE_AUTH_TOKEN` and
`secrets.NPM_TOKEN`. Remove that environment entry from
`npm-release.yml`, run workflow and security tests, create a focused hardening
commit, open a PR, wait for green CI, and merge.

Expected final state: v0.2.1 is immutable and installable; main's future npm
publication path uses only GitHub OIDC and automatic npm provenance.
