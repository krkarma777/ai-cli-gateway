# npm Distribution Design

**Status:** Approved for implementation planning on 2026-08-28

## Context

AI CLI Gateway v0.2.0 is available as an immutable GitHub Release with five
platform archives, checksums, an SPDX SBOM, and GitHub build-provenance
attestations. Installation currently requires users to select, verify, extract,
and place the correct archive on `PATH` themselves.

The npm distribution adds a shorter installation path without weakening the
existing release boundary. It will not download executable content during
installation or first execution. The first npm release will be `0.2.1` and will
use binaries produced from the matching `v0.2.1` Git tag.

## Goals

- Make `npm install --global ai-cli-gateway` install the supported native CLI.
- Install only the binary for the consumer's operating system and architecture.
- Preserve the Go CLI's arguments, standard streams, exit status, and signal
  behavior behind a small Node launcher.
- Keep npm installation and execution free of lifecycle scripts and network
  downloads.
- Prove that npm packages contain the same binaries as the matching immutable
  GitHub Release.
- Publish public npm packages with provenance and migrate immediately from the
  one-time bootstrap credential to npm trusted publishing.
- Keep every package version aligned with the GitHub release version.

## Non-goals

- Retrospectively publish an npm package for v0.2.0.
- Replace GitHub Releases, checksums, SBOMs, or GitHub attestations.
- Publish a JavaScript SDK or expose a JavaScript API from the launcher package.
- Install, authenticate, or update Codex CLI, Claude Code, or Gemini CLI.
- Install a service, edit `PATH`, or modify shell startup files.
- Support platforms beyond the existing five release targets.
- Add Homebrew, WinGet, Scoop, or other package managers in this change.

## User Experience

The public installation and execution contract is:

```console
npm install --global ai-cli-gateway
ai-cli-gateway version
ai-cli-gateway init
ai-cli-gateway serve --config /absolute/path/to/config.toml
```

The package requires Node.js `>=22.14.0`. The Node process is only a launcher;
all gateway behavior remains implemented by the native Go executable.

## Package Topology

Six unscoped public packages share the exact version `0.2.1` for the first
release and the exact corresponding version for every later release.

| Package | npm `os` | npm `cpu` | Native target |
| --- | --- | --- | --- |
| `ai-cli-gateway` | unrestricted | unrestricted | Node launcher |
| `ai-cli-gateway-darwin-x64` | `darwin` | `x64` | `darwin/amd64` |
| `ai-cli-gateway-darwin-arm64` | `darwin` | `arm64` | `darwin/arm64` |
| `ai-cli-gateway-linux-x64` | `linux` | `x64` | `linux/amd64` |
| `ai-cli-gateway-linux-arm64` | `linux` | `arm64` | `linux/arm64` |
| `ai-cli-gateway-win32-x64` | `win32` | `x64` | `windows/amd64` |

The public `ai-cli-gateway` package declares all five native packages under
`optionalDependencies`, with values pinned to the exact launcher version. npm's
`os` and `cpu` constraints select the compatible native package. No lifecycle
script repairs or downloads a missing optional dependency.

The repository layout is:

```text
npm/
  package.json
  package-lock.json
  launcher/
    package.json
    README.md
    bin/ai-cli-gateway.js
    lib/launcher.js
  platforms/
    darwin-x64/
      package.json
      README.md
    darwin-arm64/
      package.json
      README.md
    linux-x64/
      package.json
      README.md
    linux-arm64/
      package.json
      README.md
    win32-x64/
      package.json
      README.md
  scripts/
    stage-packages.js
    verify-packages.js
  test/
    launcher.test.js
    package-contract.test.js
    stage-packages.test.js
```

`npm/package.json` is a private, dependency-free test and packaging harness. It
is not a workspace and is never published. The public package manifests have no
`scripts` field. Their `files` fields are explicit allowlists. The native
package source directories do not contain built binaries; packaging copies
their manifests and the verified release binaries into a private temporary
staging root.

Each published package contains its package manifest, a package-specific
README, the repository license, and only its required launcher or native binary
files. Native packages do not export JavaScript code and do not declare a
`bin` command, so only the launcher owns the public `ai-cli-gateway` command.

## Launcher Contract

`npm/launcher/bin/ai-cli-gateway.js` contains the Node shebang and delegates to
the focused implementation in `npm/launcher/lib/launcher.js`.

The launcher performs these steps in order:

1. Read and validate its own package name and canonical semantic version.
2. Map the exact `process.platform` and `process.arch` pair to one native
   package and executable name.
3. Resolve the native package relative to the installed launcher package.
4. Read the native package manifest and require the expected package name and
   the exact launcher version.
5. Resolve the binary path and require a non-symlink regular file contained
   directly beneath the native package's `bin` directory. On POSIX, require at
   least one executable bit.
6. Spawn the binary without a shell, pass `process.argv.slice(2)` unchanged,
   and inherit stdin, stdout, and stderr.
7. Forward supported termination signals while the child is alive, then
   preserve the child's numeric exit status or signal termination.

The launcher never searches `PATH` for a fallback executable, reads provider
credentials, invokes a shell, downloads a file, or changes the native
executable.

### Fixed failures

All launcher-owned failures write one concise message to stderr and exit `1`.
They never include environment values, home paths, credentials, stack traces,
or resolved dependency paths.

- Unsupported platform: identify only the canonical platform/architecture pair
  and list the five supported pairs.
- Missing native package: explain that optional dependencies must not be
  omitted and provide `npm install --global ai-cli-gateway@<exact-version>`.
- Package mismatch: report an invalid native package installation.
- Missing, linked, non-regular, out-of-root, or non-executable binary: report an
  invalid native package installation.
- Spawn failure: report that the native executable could not be started.

## Release and Publication Flow

The existing `.github/workflows/release.yml` remains the authority that builds,
attests, verifies, and publishes the immutable GitHub Release. A new
`.github/workflows/npm-release.yml` listens only for a published, non-draft,
non-prerelease GitHub Release whose tag is canonical `vMAJOR.MINOR.PATCH`.

The npm workflow uses a GitHub-hosted Ubuntu runner, Node.js `24.13.0`, the
bundled npm `11.6.2`, Go `1.26.5`, SHA-pinned GitHub Actions, `contents: read`,
and `id-token: write`. It performs the following closed sequence:

1. Validate the repository, release state, tag, tag commit, and package version.
2. Check out the exact tag commit without persisted Git credentials.
3. Download the closed set of seven GitHub Release assets.
4. Require the exact asset names, regular-file types, nonzero bounded sizes,
   GitHub-reported SHA-256 digests, `SHA256SUMS`, and GitHub provenance tied to
   the tag commit, tag ref, repository, and release workflow.
5. Rebuild all five binaries with the same Go version, flags, metadata, source
   epoch, and repository-owned release packer used by `release.yml`.
6. Recreate all five deterministic release archives and require their SHA-256
   digests to equal the published `SHA256SUMS`. Equality of the deterministic
   archives proves that the staging binaries are the released binaries.
7. Stage six npm package directories under a private runner-temporary root.
8. Create all six npm tarballs with lifecycle scripts disabled and inspect every
   manifest, archive entry, mode, package name, version, target constraint,
   dependency, and binary.
9. Install the launcher and matching Linux package tarballs into a second clean
   temporary root, then require `ai-cli-gateway version` to report the exact
   release version and commit.
10. Query npm once for every exact package version. If absent, mark it for
    publication. If present with the exact locally computed tarball integrity,
    mark it complete. If present with different content, fail closed.
11. Publish missing native packages in a fixed order and publish the launcher
    last. Each successful publish is re-read from npm and integrity-checked
    before proceeding.

The launcher is published last so a partially failed first run cannot expose a
new public launcher whose exact native dependencies are unavailable. A rerun is
idempotent only when every already-published tarball is byte-for-byte identical;
npm versions are never overwritten or unpublished by automation.

## First-publication Bootstrap

npm trusted publishing cannot be configured until a package already exists.
The first `0.2.1` publication therefore uses one short-lived granular npm token
stored as the `NPM_TOKEN` GitHub Actions secret. The token is used only by the
GitHub-hosted npm workflow, and `npm publish --provenance --access public`
records provenance for all six initial packages.

Immediately after the initial packages exist, the maintainer will:

1. Configure each package to trust `krkarma777/ai-cli-gateway` and the exact
   `npm-release.yml` workflow for `npm publish`.
2. Verify all six exact relationships with `npm trust list` using npm
   `>=11.15.0` and in each package's npm settings.
3. Require two-factor authentication and disallow traditional publish tokens on
   all six packages.
4. Delete the GitHub `NPM_TOKEN` secret.
5. Revoke the bootstrap token at npm.

Later releases use npm OIDC trusted publishing and automatic npm provenance.
The workflow contains no permanent token fallback. Bootstrap support is removed
in the same release-hardening change once trusted publishers are configured.

## Local and Hosted Verification

The npm code uses only Node built-ins and has no development or runtime
JavaScript dependency beyond the exact optional native packages.

### Unit and contract tests

- Platform mapping accepts exactly the five supported pairs.
- Unsupported platform output is fixed and contains no sensitive values.
- Native resolution rejects missing, wrong-name, wrong-version, linked,
  non-regular, out-of-root, and non-executable binaries.
- Argument arrays are passed without shell interpretation.
- stdin, stdout, stderr, numeric exit status, `SIGINT`, and `SIGTERM` are
  preserved using real child processes.
- Every source and staged manifest matches an exact key/value allowlist.
- All public packages have explicit file allowlists and no lifecycle scripts.
- The launcher has exactly five exact-version optional dependencies.
- Each native package has exactly one expected `os` and `cpu` value.
- Package staging rejects duplicate targets, unsafe paths, unexpected files,
  changed sources, version mismatches, and output-root replacement.
- Packed tarballs contain only the allowed files with the required modes.

### CI integration

Normal CI runs the dependency-free launcher and package-contract tests on Node
`22.14.0` and `24.13.0`. It runs formatting checks and `npm pack --dry-run`
without network publication. Linux, macOS, and Windows runners use Node
`24.13.0` and Go `1.26.5`; each builds the native Go binary for its host, stages
local package tarballs, installs them in a clean temporary prefix with lifecycle
scripts disabled, and executes `ai-cli-gateway version` through npm's generated
command shim.

Repository security tests enforce the npm package topology, manifests, closed
file lists, absent lifecycle scripts, SHA-pinned workflow actions, exact event
filter, least permissions, immutable-release verification, deterministic binary
equivalence, package inspection, publication order, integrity-based retries,
and launcher-last rule. Mutation cases must demonstrate that weakening each
release invariant fails the contract test.

All existing Go formatting, vet, lint, unit, race, integration, trimpath,
releasepack, repository-hygiene, and static-build gates remain required.

## Documentation

The README Quick Start will present npm as the shortest installation path while
retaining the checksum-verified GitHub archive path. `docs/getting-started.md`
will explain supported npm platforms, Node requirements, exact-version
installation, npm provenance verification, upgrading, and uninstalling. Release
notes for v0.2.1 will identify npm as a distribution channel rather than a new
gateway runtime.

## Acceptance Criteria

- `npm install --global ai-cli-gateway@0.2.1` installs and runs the matching Go
  CLI on all five supported targets.
- A normal installation obtains only the launcher and one compatible native
  package.
- Installation and execution perform no lifecycle-script or application-owned
  network download.
- The launcher preserves arguments, standard streams, exit status, and supported
  termination signals.
- Unsupported, omitted, corrupt, or version-mismatched native packages fail
  closed with fixed actionable messages.
- Every published npm native binary is proven identical to the corresponding
  immutable GitHub Release binary.
- All six packages are public, exact-version aligned, provenance-bearing, and
  published native-first with the launcher last.
- The first-publication token is revoked and removed after trusted publishing is
  configured for all six packages.
- The full local and hosted Go and npm verification suites pass before the
  `v0.2.1` tag is created.

## References

- npm package metadata (`bin`, `optionalDependencies`, `os`, and `cpu`):
  <https://docs.npmjs.com/cli/v11/configuring-npm/package-json/>
- npm trusted publishing:
  <https://docs.npmjs.com/trusted-publishers/>
- npm trusted-publisher prerequisites:
  <https://docs.npmjs.com/cli/v11/commands/npm-trust/>
- npm provenance:
  <https://docs.npmjs.com/generating-provenance-statements/>
