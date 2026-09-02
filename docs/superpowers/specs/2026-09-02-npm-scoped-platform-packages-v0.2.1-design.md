# npm Scoped Platform Packages v0.2.1 Recovery Design

**Status:** Approved in conversation on 2026-09-02

## Context

AI CLI Gateway v0.2.1 has an immutable Git tag and GitHub Release containing
the five native archives, checksums, an SPDX SBOM, and GitHub build
attestations. The npm publication was only partially completed:

- `ai-cli-gateway-darwin-x64@0.2.1` was published on 2026-09-01;
- `ai-cli-gateway-darwin-arm64@0.2.1` was published on 2026-09-01;
- `ai-cli-gateway-linux-x64@0.2.1` was published on 2026-09-01;
- `ai-cli-gateway-linux-arm64@0.2.1` was published on 2026-09-01;
- `ai-cli-gateway-win32-x64@0.2.1` was rejected by npm's automated spam
  detection; and
- `ai-cli-gateway@0.2.1` was correctly not attempted because the launcher is
  published last.

The four published packages are internal implementation packages. No launcher
version points to them, and the user has confirmed that they have no intended
consumers. The chosen recovery is to remove those incomplete unscoped package
identities and publish the five internal packages under the owner's npm scope.

The launcher remains the searchable, user-facing, unscoped package. All six
new package identities at version `0.2.1` are currently absent from the npm
registry. The scoped platform packages can therefore use `0.2.1`; using
`0.2.2` would incorrectly imply a new gateway binary release when this work is
only completing distribution of the existing v0.2.1 binaries.

## Goals

- Complete the first npm distribution at version `0.2.1`.
- Keep `ai-cli-gateway` as the only package users are instructed to install.
- Move all five internal native packages under `@krkarma777`.
- Remove the four incomplete unscoped native packages while npm's new-package
  unpublish window is available.
- Reuse and independently verify the immutable v0.2.1 release binaries.
- Preserve launcher-last publication, exact-version dependencies, npm
  provenance, and fail-closed release verification.
- Include the already designed npm search metadata and Windows launcher
  verification without carrying unrelated v0.2.2 release preparation.

## Non-goals

- Change, recreate, retag, or delete Git tag `v0.2.1`.
- Change or delete the immutable GitHub Release or any release asset.
- Rebuild a different gateway runtime and call it v0.2.1.
- Publish or prepare v0.2.2.
- Reuse any unpublished unscoped `name@0.2.1` pair.
- Scope the public launcher as `@krkarma777/ai-cli-gateway`.
- Add another platform, JavaScript SDK, lifecycle downloader, or runtime
  fallback.
- Claim that a scope guarantees acceptance by npm's unpublished spam
  heuristic. A scoped-name rejection must still stop the release.

## Alternatives Considered

### 1. Focused v0.2.1 recovery from current main — selected

Create a focused branch from current `main`, change only the platform package
identities and the v0.2.1 recovery path, and selectively carry over the
discoverability metadata and Windows verification already reviewed in the
draft v0.2.2 branch. This keeps the binary version honest, minimizes release
surface area, and avoids duplicating the Windows work.

### 2. Convert the entire draft v0.2.2 branch back to v0.2.1

This would retain every draft change, but the branch already includes v0.2.2
release documentation, tag-only dispatch, OIDC-only publication, and unrelated
test hardening. Reversing those assumptions creates a larger and riskier diff
than the recovery requires.

### 3. Transform and publish tarballs manually

This is superficially fast, but it creates a separate publication path, loses
the reviewed artifact handoff, and cannot generate GitHub Actions npm
provenance from a local terminal. It is retained only as a diagnostic method,
not a release path.

## Package Topology

All package versions are exactly `0.2.1`:

| Role | Package |
| --- | --- |
| Public launcher | `ai-cli-gateway` |
| macOS Intel native package | `@krkarma777/ai-cli-gateway-darwin-x64` |
| macOS Apple silicon native package | `@krkarma777/ai-cli-gateway-darwin-arm64` |
| Linux x86-64 native package | `@krkarma777/ai-cli-gateway-linux-x64` |
| Linux ARM64 native package | `@krkarma777/ai-cli-gateway-linux-arm64` |
| Windows x86-64 native package | `@krkarma777/ai-cli-gateway-win32-x64` |

The launcher declares the five scoped packages as `optionalDependencies`, each
pinned to exact version `0.2.1`. Its runtime platform table uses the same
scoped names when resolving the installed native binary. Unsupported operating
systems and architectures retain the existing closed error path.

Each native manifest retains its exact `os`, `cpu`, executable, license,
repository, public-access, and provenance constraints. Its description and
README identify it as an internal package and direct users to install
`ai-cli-gateway` instead.

## Source and Artifact Separation

The immutable release commit and the packaging commit serve different roles:

1. The v0.2.1 tag commit remains the source of the native gateway binaries.
2. The merged recovery commit on `main` is the source of npm package names,
   manifests, READMEs, launcher code, staging code, and workflow policy.
3. The workflow checks out both commits into separate directories.
4. The tag checkout rebuilds every supported binary and release archive and
   compares it byte-for-byte with the immutable GitHub Release assets.
5. The verified native binaries are passed to the recovery commit's npm
   staging and verification code.
6. npm provenance records the recovery workflow and main commit, while the
   workflow evidence and GitHub attestations bind each native binary to the
   immutable v0.2.1 tag.

This separation is required because checking out only the immutable tag would
also restore the rejected unscoped npm manifests. Checking out only `main` and
rebuilding there could silently produce binaries different from the immutable
v0.2.1 release.

The workflow must require that the v0.2.1 tag commit is an ancestor of the
exact live `main` commit that triggered the dispatch. Neither checkout may
float after validation.

## Packaging Components

### Central package configuration

`npm/scripts/package-config.js` remains the authoritative target list. Each
`packageName` becomes the corresponding scoped name. The package version and
launcher name remain `0.2.1` and `ai-cli-gateway`.

The checked-in platform manifests, launcher manifest, launcher runtime table,
and tests must exactly match that configuration. Contract tests reject any
mixed scoped/unscoped cohort.

### Scoped tarball filenames

npm converts a scoped package such as
`@krkarma777/ai-cli-gateway-linux-x64` to a tarball filename such as
`krkarma777-ai-cli-gateway-linux-x64-0.2.1.tgz`. Package identity and tarball
filename must not be treated as the same string.

One closed helper will derive the expected npm tarball filename from a
validated package name and version. Staging tests, package descriptors,
artifact upload paths, artifact extraction checks, and publication checks all
use the same rule. The workflow continues to compare descriptor name,
filename, size, SHA-1 shasum, and SHA-512 integrity before publication.

### Discoverability metadata

The public launcher receives the previously approved benefit-led description,
closed keyword list, npm-focused README, repository metadata, and first-run
instructions. Platform packages receive only internal-package descriptions and
small platform-specific keyword sets. This content is brought over without
the draft branch's v0.2.2 version bump or v0.2.2 release documentation.

### Windows launcher verification

The recovery carries over the reviewed Windows checks that validate npm's
generated `.cmd` and PowerShell launchers, the JavaScript launcher, the
selected `.exe`, installation-root containment, and rejection of unsafe
reparse-point traversal. Windows verification remains part of the existing CI
shape rather than adding another long-running workflow.

## Registry Cleanup

Only these exact npm versions are removed:

```text
ai-cli-gateway-darwin-x64@0.2.1
ai-cli-gateway-darwin-arm64@0.2.1
ai-cli-gateway-linux-x64@0.2.1
ai-cli-gateway-linux-arm64@0.2.1
```

Before removal, the operator must require:

- `npm whoami` is exactly `krkarma777` against the public registry;
- each target exists only at version `0.2.1` and is owned by that account;
- the launcher, unscoped Windows package, and five scoped versions remain
  absent; and
- local commands target the explicit registry URL.

Removal occurs after the scoped recovery packages pass local staging and
contract tests, but before the final publication dispatch. Each removal uses
the exact `name@version`, never a scope-wide or wildcard target. Post-checks
must report all four exact versions absent.

The earliest package was published at 2026-09-01 01:32 UTC, so the normal
72-hour new-package window begins closing at 2026-09-04 01:32 UTC
(2026-09-04 10:32 Asia/Seoul). Implementation and local verification therefore
prioritize the cleanup-ready package path before unrelated repository work.

Unpublishing is irreversible and does not make the old `name@version` pairs
reusable. No Git tag, GitHub Release, release asset, or repository history is
deleted. If npm refuses a removal, the release stops for an explicit decision
between support review and deprecation; it must not improvise another name.

## Publication Flow

The existing one-time v0.2.1 recovery dispatch remains the release entry
point. The package job is unauthenticated and has only `contents: read`. It:

1. validates the exact repository, event, live main commit, v0.2.1 tag, and
   immutable seven-asset release;
2. checks out the recovery commit and tag commit separately;
3. verifies toolchain versions;
4. downloads and verifies checksums and GitHub attestations;
5. rebuilds from the tag checkout and compares immutable archives;
6. stages and verifies five scoped native packages plus the launcher from the
   recovery checkout;
7. installs and executes the Linux x86-64 launcher/native pair; and
8. uploads only the six verified tarballs plus `packages.json`.

The publish job receives the short-lived bootstrap npm credential already
stored for the initial release, plus `id-token: write` for provenance. It
revalidates the artifact digest, archive contents, package descriptors, and
registry state. It publishes missing native packages in closed target order,
verifies each remote SRI immediately, and publishes `ai-cli-gateway@0.2.1`
last. Every publish uses public access, the public npm registry, ignored
lifecycle scripts, and provenance.

The workflow is retry-safe only when an already-present package has exactly
the expected integrity. A present version with different integrity, malformed
registry response, authentication failure, spam rejection, or partial native
cohort stops before the launcher is published.

After the first successful publication, trusted-publisher configuration may be
added for the six existing package identities and the bootstrap credential
removed. That follow-up must not be mixed into the recovery publication.

## Documentation

Current-branch documentation must show only the public install command:

```console
npm install --global ai-cli-gateway@0.2.1
```

Where platform topology is documented, it must show the five scoped names.
The npm launcher README uses an unpinned command for discovery and an immediate
install-to-first-request path. Platform READMEs warn against direct install.

Historical Git objects and immutable release assets remain unchanged. The
current documentation records that the scoped identities are the completed
npm distribution for v0.2.1 so users do not follow the abandoned unscoped
topology.

The existing draft v0.2.2 pull request is not merged as-is. Its approved
discoverability and Windows changes are selectively carried into the focused
recovery branch. After those changes are accounted for, the draft is closed as
superseded or rebuilt later from the new `main`; it must not compete with the
v0.2.1 recovery for package identities or workflow behavior.

## Verification

Before registry cleanup:

- run the complete Node package test suite;
- run package contract, staging, tarball, launcher, and source-check tests;
- stage all six packages from verified v0.2.1 binaries;
- inspect every packed manifest and descriptor;
- install and execute the host launcher/native pair from local tarballs;
- execute the Windows launcher and installation-integrity checks on Windows;
- run workflow-security and repository-hygiene tests;
- run `git diff --check`; and
- require a clean worktree at the candidate commit.

After publication:

- require all six exact `0.2.1` versions to exist publicly;
- require their registry SRI values to match the verified descriptors;
- require public access and expected repository metadata;
- inspect npm provenance for all six packages;
- install `ai-cli-gateway@0.2.1` in a clean temporary prefix with optional
  dependencies enabled;
- run `ai-cli-gateway version` and require the immutable v0.2.1 tag commit;
- verify the selected executable remains inside the expected scoped optional
  dependency; and
- confirm all four abandoned unscoped versions remain absent.

The release is complete only after the launcher installs and executes from the
public registry. Publishing five native packages without the launcher remains
a failed partial release.

## References

- [npm Unpublish Policy](https://docs.npmjs.com/policies/unpublish/)
- [Creating and publishing scoped public packages](https://docs.npmjs.com/creating-and-publishing-scoped-public-packages/)
- [npm publish](https://docs.npmjs.com/commands/npm-publish/)
- [Generating provenance statements](https://docs.npmjs.com/generating-provenance-statements/)
