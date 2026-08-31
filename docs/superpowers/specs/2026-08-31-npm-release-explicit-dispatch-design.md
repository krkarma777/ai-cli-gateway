# Explicit npm Release Dispatch Design

**Status:** Approved for implementation planning on 2026-09-01

## Incident and Current State

The immutable GitHub Release `v0.2.1` exists and its release workflow completed
successfully, but none of the six npm packages at version `0.2.1` has been
published. The repository's npm workflow listens for the GitHub Release
`published` event. The release was published by `github-actions[bot]` with the
release workflow's `GITHUB_TOKEN`, and GitHub intentionally does not start a
new workflow from most events created by that token. `workflow_dispatch` and
`repository_dispatch` are the documented exceptions.

This means the release event was emitted but could not start
`.github/workflows/npm-release.yml`. Waiting or replaying the existing release
event cannot repair the missing workflow run. The immutable release and its tag
must not be replaced or modified.

Authoritative behavior:

- [GitHub token event behavior](https://docs.github.com/en/actions/concepts/security/github_token)
- [Triggering a workflow from a workflow](https://docs.github.com/en/actions/how-tos/write-workflows/choose-when-workflows-run/trigger-a-workflow)

## Goals

- Publish all six `0.2.1` packages from the existing immutable GitHub Release,
  retaining npm provenance and the existing byte-for-byte verification chain.
- Give later release workflows an explicit, deterministic way to start npm
  publication after the GitHub Release is proven immutable.
- Preserve least privilege, closed workflow structure, exact-tag validation,
  native-package-first ordering, launcher-last ordering, and safe idempotent
  retries.
- Make malformed, premature, cross-repository, or mismatched dispatches fail
  before package construction or publication.
- Keep the bootstrap npm token confined to the existing publish step until
  trusted publishing has been configured and verified.

## Non-goals

- Recreate, retag, edit, or delete the immutable `v0.2.1` release.
- Publish locally with `npm publish` or omit npm provenance.
- Add a personal access token, GitHub App credential, or other long-lived
  GitHub credential solely to chain the workflows.
- Replace the existing archive, checksum, attestation, deterministic rebuild,
  package inspection, artifact identity, registry integrity, or publication
  ordering controls.
- Generalize the version-locked `v0.2.1` packaging workflow beyond what is
  required for this recovery. A later release still updates its exact version,
  assets, and package contract in the normal release change.

## Chosen Architecture

### 1. Closed explicit npm trigger

`.github/workflows/npm-release.yml` will listen only to `workflow_dispatch`.
The trigger will expose exactly one required string input named `tag`, with no
default and no additional inputs. The previous `release: published` trigger
will be removed so that one release cannot start duplicate npm cohorts through
two trigger paths.

The concurrency key will use the repository and the dispatch input tag, and
`cancel-in-progress` will remain false. A retry for the same version therefore
waits for an active run instead of interrupting a possibly publishing cohort.

The first step will accept no release metadata from an event payload other than
the requested tag. It will independently validate all authority from GitHub:

1. Require `github.event_name` to be `workflow_dispatch`, the exact repository
   to be `krkarma777/ai-cli-gateway`, and the required input to be exactly
   `v0.2.1` for this version-locked workflow.
2. Require the workflow source ref to be either the same tag ref or the narrow
   `v0.2.1` recovery case on `refs/heads/main` described below.
3. Resolve the live Git tag through the GitHub API, including annotated-tag
   peeling, and obtain the exact 40-character commit SHA.
4. Query the release by the requested tag and require one non-draft,
   non-prerelease, published, immutable release with the exact tag and the
   closed set of seven assets.
5. Export only the validated tag, version, tag commit, and release ID for later
   steps.

All subsequent source checkout, release asset download, attestation
verification, deterministic rebuild, npm staging, tarball inspection, clean
installation, artifact transfer, registry query, and publish behavior remains
bound to those validated outputs. The workflow continues to check out and
rebuild the immutable tag commit, never the dispatching branch commit.

On the normal tag path, `github.ref` must equal `refs/tags/<input tag>` and
`github.sha` must equal the peeled live tag commit. The main-branch path is not
a general manual-release mechanism: it is version-scoped to the already-created
`v0.2.1` tag and must be removed when the workflow's version lock advances to a
later release.

### 2. Future release-to-npm handoff

The `publish` job in `.github/workflows/release.yml` will receive `actions:
write` in addition to its existing `contents: write`; no other job receives the
new permission. After `gh release edit ... --draft=false`, the same job will:

1. Re-query the live tag and require it still resolves to the packaged commit.
2. Re-query the release by tag, with a short bounded retry for GitHub's
   publication state to settle.
3. Require the release to be non-draft, non-prerelease, published, immutable,
   associated with the exact tag, and to expose exactly the expected seven
   assets.
4. Dispatch exactly `.github/workflows/npm-release.yml`, exactly at the release
   tag ref, with exactly `tag=<release tag>`.

The dispatch occurs only after the immutable-release checks succeed. The
release job must not accept a workflow name, ref, or input key from an external
parameter. Because `workflow_dispatch` is an explicit exception to GitHub's
normal `GITHUB_TOKEN` recursion prevention, this handoff needs no PAT or GitHub
App secret.

Running the npm workflow at the tag ref is important for future provenance: the
workflow definition that publishes npm packages is the reviewed definition in
the same release tag, while its build inputs continue to resolve to that exact
tag commit.

### 3. One-time `v0.2.1` recovery

The existing `v0.2.1` tag predates this fix, so the workflow file stored at that
tag does not declare `workflow_dispatch`. GitHub therefore cannot dispatch the
fixed workflow using `--ref v0.2.1`.

After this change is merged and its exact `main` commit passes required CI, the
maintainer will dispatch the fixed npm workflow once with:

```console
gh workflow run npm-release.yml --ref main -f tag=v0.2.1
```

The recovery branch path is deliberately narrow:

- it is allowed only for input `v0.2.1` and ref `refs/heads/main`;
- the workflow source SHA must equal the live default-branch head at validation
  time;
- the live `v0.2.1` tag must resolve to the already released commit and be an
  ancestor of that validated main head;
- the workflow queries and verifies the immutable release and its assets
  independently;
- checkout, rebuild, attestations, package contents, and npm integrity remain
  tied to the `v0.2.1` tag commit, not the main-branch workflow source.

This one recovery run has an explicit provenance trade-off: npm provenance will
identify the fixed workflow invocation on the reviewed main commit, while the
published package contents are independently proven to come from the immutable
`v0.2.1` tag. Future releases dispatch at their tag ref and do not use this
exception.

Before dispatch, operations will record the exact CI-green main SHA and require
it to remain the live main head. If main moves, the dispatch pauses until the
new head has passed the required checks.

## Security and Failure Behavior

- Missing, optional, extra, malformed, or noncanonical inputs fail closed.
- A repository, workflow source ref, workflow source SHA, tag, peeled tag
  commit, release ID, release state, immutable flag, asset count, asset name,
  asset digest, checksum, attestation, archive digest, package descriptor,
  tarball integrity, or registry integrity mismatch fails the run.
- The release workflow cannot dispatch npm publication before immutable state
  has been observed from the live API.
- `actions: write` exists only in the release publication job and is exercised
  only for the fixed npm workflow, tag ref, and tag input.
- `NPM_TOKEN` remains available only to the npm publish step and is never
  printed, copied into an artifact, or used by release packaging jobs.
- GitHub manual-dispatch authorization is not treated as package authority;
  the npm workflow re-establishes package authority from the live tag,
  immutable release, checksums, attestations, deterministic rebuild, and exact
  npm tarball integrity.
- If no package exists, native packages publish in the existing fixed order and
  the launcher publishes last. If a run stops partway through, a retry accepts
  an existing version only when its registry SRI equals the locally verified
  tarball and publishes only the missing packages. An integrity conflict fails
  without overwriting or unpublishing anything.

## Test-Driven Contract Changes

The repository security tests will be changed before the workflow
implementation so that the first focused run fails for the missing explicit
dispatch behavior. The closed YAML parsers and validators will then be updated
until that test passes.

The npm workflow contract will require and mutation-test:

- exactly one required `workflow_dispatch.tag` string input;
- absence of the old `release` trigger, defaults, extra input fields, and extra
  trigger types;
- concurrency keyed to the validated dispatch tag;
- exact event name, repository, version-locked tag, and permitted source-ref
  validation;
- the main-head equality and tag-ancestor checks for the `v0.2.1` recovery;
- live annotated-tag resolution and immutable release lookup by tag;
- the unchanged source, asset, attestation, rebuild, package, artifact,
  provenance, ordering, and SRI controls.

The GitHub release workflow contract will require and mutation-test:

- `actions: write` only on the publish job;
- live tag and immutable-release verification after publication;
- dispatch only after that verification;
- the exact npm workflow filename, release tag ref, and sole `tag` input;
- rejection of changed workflow names, branch refs, input names or values,
  reordered dispatch, omitted immutable checks, unbounded retries, or widened
  permissions.

Verification before merge will include the focused mutation suites, decoded
Bash syntax checks, the complete `internal/securitytest` package, `actionlint`,
`gofmt`, `go vet`, `golangci-lint`, all Go unit/race/integration/trimpath gates,
the npm test suite, source-package verification, and a clean-tree check. No npm
workflow will be dispatched as a test before the reviewed change is merged.

## Rollout

1. Implement the contract tests and workflow changes on an isolated branch.
2. Open a pull request, require all repository checks, review the exact diff,
   and squash-merge it.
3. Record the merged main SHA, require it to remain the live main head, and
   confirm the `NPM_TOKEN` secret exists while all six `0.2.1` package versions
   are still absent.
4. Manually dispatch `npm-release.yml` from that exact main head with input
   `v0.2.1`, record the numeric run ID, and monitor every job to completion.
5. Verify all six exact npm versions, their locally expected integrity values,
   npm provenance, and a clean real installation and execution contract.
6. Configure each npm package to trust
   `krkarma777/ai-cli-gateway/.github/workflows/npm-release.yml`, verify all six
   trust relationships, and enable the package policy that requires 2FA while
   disallowing traditional publish tokens.
7. Delete the GitHub `NPM_TOKEN` secret and revoke the short-lived token at npm.
8. In a separate reviewed change, remove the bootstrap token fallback only
   after trusted publishing has been demonstrated for all six packages.

## Alternatives Rejected

### `workflow_run`

Listening to completion of `release.yml` would avoid an explicit dispatch, but
the downstream workflow definition is selected from the default branch rather
than naturally binding future publication to the release tag. It also makes
the exact release-to-npm handoff and recovery exception less explicit.

### Local npm publication

Local publication could use the saved token immediately, but it would bypass
the reviewed GitHub workflow and lose the intended GitHub Actions npm
provenance. It also creates a separate, harder-to-reproduce publication path.

### PAT or GitHub App handoff

A PAT or GitHub App could emit an event that starts the npm workflow, but it
introduces another credential and lifecycle solely to work around behavior that
GitHub already supports safely through explicit `workflow_dispatch`.
