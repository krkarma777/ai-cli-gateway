# npm Package Discoverability Design

**Status:** Approved in conversation on 2026-09-01, including the Windows launcher extension

## Context

AI CLI Gateway turns locally authenticated Codex CLI, Claude Code, and Gemini
CLI installations into a focused OpenAI Responses-compatible local API. Its
primary use cases are AI MVPs, product validation, demos, hackathons,
structured-output prototypes, and local SDK integrations.

The repository README already communicates that value clearly. The npm
launcher metadata does not. Its current description only says that it runs a
matching native binary, it has no keywords, and its npm-specific README mainly
documents package topology. The platform packages have one-line descriptions
and one-paragraph READMEs that do not explain the product.

The initial `0.2.1` publication is also incomplete. Four native packages exist
in the npm registry with exact artifact integrity and provenance. Publication
of `ai-cli-gateway-win32-x64@0.2.1` was rejected by npm's automated spam
detection, so the launcher was correctly not attempted because it publishes
last. The missing packages are:

- `ai-cli-gateway-win32-x64@0.2.1`;
- `ai-cli-gateway@0.2.1`.

Published name-and-version pairs cannot be replaced, and npm package READMEs
only update when a new version is published. The discoverability correction
will therefore ship consistently across the six-package cohort as `0.2.2`.
The original verified `0.2.1` artifacts remain unchanged.

## Goals

- Make the npm search result immediately explain what AI CLI Gateway does.
- Lead with the user outcome: building AI MVPs with existing AI CLI access.
- Include the concrete mechanism in the same message: a local OpenAI
  Responses-compatible API over Codex CLI, Claude Code, and Gemini CLI.
- Give a new user an install-to-first-request path directly on the npm page.
- State the focused compatibility boundary without implying full OpenAI API
  compatibility.
- Add concise, relevant search keywords without keyword stuffing.
- Make every platform package clearly identify itself as an internal native
  dependency and direct users to the launcher package.
- Preserve the existing native-package topology, no-download installation,
  exact-version dependency pins, provenance, and launcher-last publication.
- Add metadata assertions to the existing npm test suite without creating new
  CI jobs, duplicated triggers, or a separate long-running workflow.

## Non-goals

- Change the gateway runtime, request contract, supported providers, or native
  targets.
- Claim full OpenAI API compatibility, streaming, tool calls, multimodal input,
  sessions, or conversation storage.
- Add a JavaScript SDK or JavaScript API to the npm launcher.
- Publish altered content under an already-published `0.2.1` name and version.
- Rename or migrate the native packages to an npm scope in this patch.
- Add lifecycle scripts, a runtime downloader, or a fallback binary lookup.
- Treat immediate appearance in npm search as a release gate; npm documents
  that indexing a newly published package can take up to two weeks.

## Chosen Positioning

The selected strategy combines a benefit-led message with an exact technical
explanation. The launcher description is:

> Build AI MVPs with Codex CLI, Claude Code, and Gemini CLI through a local
> OpenAI Responses-compatible API.

This sentence is intentionally ordered as:

1. user outcome: build AI MVPs;
2. supported inputs: Codex CLI, Claude Code, and Gemini CLI;
3. integration boundary: a local OpenAI Responses-compatible API.

The npm README will immediately qualify `Responses-compatible` as a focused
subset and link to the exact request contract. It will not use the broader
phrase `OpenAI-compatible API` without that qualifier.

### Alternatives considered

**Protocol-first positioning** would lead with "OpenAI Responses-compatible
local gateway." It is technically precise and search-friendly but hides the
reason a product developer would install it.

**Benefit-only positioning** would lead with "Turn your AI CLI access into AI
apps." It is easier to scan but omits the protocol, supported providers, and
local operating boundary that distinguish this project.

**Generic multi-provider gateway positioning** was rejected because the
project does not broker arbitrary provider APIs. It supervises three specific,
locally authenticated CLI programs behind a deliberately narrow API surface.

## Launcher Package Metadata

`npm/launcher/package.json` will retain its existing name, license, binary,
engine requirement, exact optional dependencies, repository, homepage, bugs,
and provenance configuration. It will receive the selected description and
this closed keyword set:

```json
[
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
  "json-schema"
]
```

Each term maps to a supported feature, provider, protocol boundary, or primary
use case. Variants that merely repeat the same words, unsupported features,
competitor product names, and high-volume unrelated terms are excluded.

No author, funding, or commercial URL will be invented. The existing
repository, homepage, issues, Apache-2.0 license, and provenance fields remain
the authoritative ownership and support metadata.

## Launcher npm README

`npm/launcher/README.md` will be an npm-focused entry point derived from the
repository README rather than a copy of the package topology. It will use this
order so the first visible screen answers what the tool is and how to try it:

1. project name and the selected positioning sentence;
2. a precise note that compatibility is a focused Responses API subset;
3. global installation and `ai-cli-gateway version`;
4. the three-step path: authenticate a provider CLI, run `init`, then `serve`;
5. a small OpenAI JavaScript SDK example pointing at the loopback endpoint;
6. supported providers, endpoints, text output, and strict JSON Schema output;
7. high-value MVP, validation, demo, and local integration use cases;
8. concise exclusions for streaming, tool-call round trips, sessions,
   multimodal input, and other OpenAI endpoints;
9. local trust boundary, credential ownership, no lifecycle downloads, and
   npm provenance;
10. links to Getting Started, the API reference, security policy, releases,
    and the GitHub repository.

The README will use the unpinned install command for normal discovery:

```console
npm install --global ai-cli-gateway
```

Release-specific documentation will continue to use exact versions where
reproducibility matters. The package page will not duplicate the complete
operations manual or advanced archive-verification procedure.

## Platform Package Metadata

The five platform packages are implementation details of the launcher. Their
descriptions will use friendly platform names and make the installation path
explicit. For example:

> Internal macOS Apple silicon binary for AI CLI Gateway. Install the
> `ai-cli-gateway` package instead.

Equivalent descriptions will identify macOS Intel, Linux x86-64, Linux ARM64,
and Windows x86-64. Each package will receive only four keywords:

```json
["ai-cli-gateway", "native-binary", "PLATFORM", "ARCHITECTURE"]
```

`PLATFORM` and `ARCHITECTURE` are replaced with the package's canonical npm
`os` and `cpu` values. This makes each page understandable without attempting
to compete with the launcher for generic AI searches.

Each platform README will contain:

- a prominent internal-package notice;
- the exact main-package install command;
- its npm and Go platform tuple;
- a statement that npm installs it through an exact optional dependency;
- a statement that it contains no standalone JavaScript API;
- links to the launcher package and repository.

## Package Topology Decision

The existing topology remains six packages: one Node launcher plus five native
packages selected through exact `optionalDependencies` and npm `os`/`cpu`
constraints. This is a standard pattern for distributing native executables
through npm. It avoids installing every platform binary and lets npm select
the host-compatible artifact.

It is especially appropriate here because the public packages contain no
lifecycle scripts. Installation and first execution do not download an
application-owned binary. A single package containing all five binaries would
waste download and disk space on every host. A single lightweight package that
downloads a binary during install or first execution would weaken offline
behavior, registry integrity coverage, and the project's fail-closed supply
chain design.

Many native npm projects place their platform packages in a scope. Migrating
this project to scoped native packages could reduce namespace clutter, but it
would create a second package family after four unscoped packages have already
been published. That migration is deferred to a separate compatibility and
operations decision; it is not required to correct the current metadata or
the npm spam-detection false positive.

## Windows Launcher Contract

Windows x86-64 is a first-class release target, not a cross-compile-only
artifact. `process.platform === "win32"` and `process.arch === "x64"` must
select `ai-cli-gateway-win32-x64` and execute
`bin/ai-cli-gateway.exe` from that exact matching package version.

The existing `npm-host-install` CI matrix will remain the authoritative native
launcher gate. Its `windows-2025` entry must build the real Go `.exe`, stage
the Windows native package and launcher, pack both tarballs, install them with
lifecycle scripts disabled, and execute the installed command. The gate will
be extended inside the same job, without another runner or workflow, to prove:

- the npm-generated `.cmd` shim starts the Node launcher and native `.exe`;
- the npm-generated PowerShell shim starts the same launcher and `.exe`;
- the exact version argument reaches the native command and stdout returns;
- a deliberately invalid command preserves native exit code `2`, keeps stdout
  empty, and returns only the documented usage on stderr.

The ordinary Windows Go jobs continue covering native Job Objects, ACLs,
reparse points, cancellation, cleanup, trimmed paths, and the Windows build.
The npm launcher gate covers packaging and command dispatch; it does not
duplicate the longer Go integration suite. A `0.2.2` release cannot proceed if
either the native Windows tests or the Windows npm launcher gate fails.

Windows ARM64 remains outside the supported five-target release matrix. No
emulation result substitutes for the real `windows-2025` x86-64 launcher run.

## Version and Publication Flow

The rollout has two ordered phases.

### Complete the original `0.2.1` cohort

Submit an npm support request for the false-positive spam restriction. The
request will ask npm to clear or review both names:

- `ai-cli-gateway-win32-x64`, which was rejected;
- `ai-cli-gateway`, which is the launcher and has not been attempted because
  the workflow stopped safely before publishing it.

After npm confirms the restriction is cleared, rerun the existing idempotent
`v0.2.1` npm workflow. It must accept the four existing native packages only
when registry SRI matches the verified tarballs, publish the original Windows
tarball, and publish the original launcher tarball last. No `0.2.1` artifact
or metadata is regenerated.

Once all six packages exist, configure each package to trust the repository's
exact `npm-release.yml` workflow and verify the relationships with a supported
npm CLI. The release workflow starts `npm-release.yml` as a separate
`workflow_dispatch` run rather than a reusable `workflow_call`, so the npm
publisher identity is the dispatched workflow itself. The npm publish job must
assert that exact GitHub OIDC `workflow_ref` before it contacts the registry.

The `v0.2.2` workflow contains no token environment and must prove all six
publishes through OIDC while the bootstrap token still exists only as an
unused recovery option. After the OIDC cohort and provenance verify, require
two-factor authentication while disallowing token publication, remove the
GitHub bootstrap secret, and revoke the bootstrap token. This follows npm's
safe migration order without making the next release depend on the one-time
token.

### Publish the discoverability correction as `0.2.2`

Update all six manifests, exact optional-dependency pins, package contract,
release documentation, and version-locked workflows together. Build and
verify the standard immutable `v0.2.2` GitHub Release, then publish all six npm
packages through the tagged trusted workflow with native packages first and
the launcher last.

The default `latest` tag will point to the launcher `0.2.2` only after every
exact native dependency exists and verifies. Existing `0.2.1` native packages
remain available for integrity and historical reproducibility.

## npm Support Request

The support request will use this factual body:

```text
Subject: False-positive spam detection blocks legitimate package publication

Publishing ai-cli-gateway-win32-x64@0.2.1 from GitHub Actions fails with:

E403 Package name triggered spam detection.

This is a legitimate open-source platform package for AI CLI Gateway.

Repository:
https://github.com/krkarma777/ai-cli-gateway

Immutable release:
https://github.com/krkarma777/ai-cli-gateway/releases/tag/v0.2.1

Provenance workflow run:
https://github.com/krkarma777/ai-cli-gateway/actions/runs/33458990165

Four sibling platform packages at 0.2.1 were accepted with npm provenance:
- ai-cli-gateway-darwin-x64
- ai-cli-gateway-darwin-arm64
- ai-cli-gateway-linux-x64
- ai-cli-gateway-linux-arm64

Please review and clear the false-positive name restriction for:
- ai-cli-gateway-win32-x64
- ai-cli-gateway

The second name is the main launcher. It has not yet been attempted because
the publication workflow intentionally stops before publishing the launcher
when any exact native dependency is unavailable.
```

No npm token, authentication detail, secret, or unpublished tarball is
included in the request.

## Verification

Implementation begins with failing package-contract tests for the selected
launcher description, exact keyword set, platform descriptions, minimal
platform keywords, ownership links, and required README content. The changes
then pass:

- the complete npm Node test suite;
- source-package and staged-package verification;
- `npm pack --json` inspection for all six packages;
- assertions that all public manifests remain script-free;
- assertions that launcher optional dependencies are the exact matching
  version and that every platform keeps one exact `os`/`cpu` constraint;
- README checks for the install command, provider names, focused compatibility
  qualifier, supported endpoints, and important exclusions;
- `go test -count=1 ./...`, `go vet ./...`, `golangci-lint run ./...`, formatting,
  and repository hygiene gates affected by release/version changes;
- deterministic release asset and package verification in the tagged release
  workflows.

After publication, operations will verify with a dedicated clean npm cache:

- all six `0.2.2` name/version pairs exist;
- launcher description, keywords, repository, homepage, bugs, and README are
  the expected values;
- native package descriptions clearly redirect to the launcher;
- registry SRI and provenance match the locally verified tarballs;
- a clean global-style installation runs `ai-cli-gateway version` on a
  supported host.

npm search visibility will be checked after publication and again after the
documented indexing window. Search indexing delay is recorded as external
state, not treated as evidence that verified metadata is missing.

## Acceptance Criteria

- The latest `ai-cli-gateway` npm page explains the product before package
  topology.
- The launcher search result includes AI MVP, the three supported CLIs, and the
  local Responses-compatible API boundary.
- The launcher exposes the exact approved keyword set.
- A new user can install, initialize, serve, and identify the SDK connection
  path from the npm README.
- The README clearly states that compatibility is a focused subset and lists
  the highest-impact unsupported features.
- Each native package tells users to install `ai-cli-gateway` instead.
- Package names, launcher behavior, runtime behavior, native targets,
  lifecycle-script policy, provenance, and publication ordering are unchanged.
- No published `0.2.1` name/version content is replaced or regenerated.
- All six `0.2.2` packages publish with verified integrity and provenance.

## References

- [npm package.json fields](https://docs.npmjs.com/files/package.json/)
- [npm package search behavior](https://docs.npmjs.com/searching-for-and-choosing-packages-to-download/)
- [npm package README files](https://docs.npmjs.com/about-package-readme-files/)
- [npm trusted publishing](https://docs.npmjs.com/trusted-publishers/)
- [GitHub OIDC workflow claims](https://docs.github.com/en/actions/reference/security/oidc)
- [setup-node trusted-publisher guidance](https://github.com/actions/setup-node/blob/main/docs/advanced-usage.md#publishing-to-npm-with-trusted-publisher-oidc)
- [esbuild platform optional dependencies](https://github.com/evanw/esbuild/blob/main/npm/esbuild/package.json)
- [Rollup native package architecture](https://github.com/rollup/rollup/blob/master/ARCHITECTURE.md)
