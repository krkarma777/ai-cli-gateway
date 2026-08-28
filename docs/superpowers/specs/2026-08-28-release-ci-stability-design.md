# Release CI Stability

Date: 2026-08-28

Status: Approved

## Context

Pushing `v0.2.0` started two copies of the same CI suite. The standalone CI
workflow listens to every `push`, including tag pushes, while the release
workflow also invokes that CI workflow through `workflow_call`.

The standalone tag CI intermittently failed
`TestConcurrentCommitSubprocesses` with `committed=0 lost=2`. The test stores
its ready, gate, and result files beside the authoritative configuration. A
losing subprocess can therefore create its result file while the winning
subprocess is performing strict directory revalidation. That test-owned
directory mutation correctly causes the production store to fail closed, but
it creates a false negative in the concurrency test.

The failure was reproduced locally in 10 isolated executions. Moving only the
coordination files to a separate private directory passed 1,000 isolated
executions.

## Goals

- Run standalone CI for branch pushes and pull requests.
- Keep the release workflow's reusable CI verification.
- Do not start a second standalone CI suite for tag pushes.
- Preserve the cross-process lock and lost-update coverage.
- Prevent test coordination artifacts from mutating the configuration
  transaction directory.
- Keep production locking and fail-closed path validation unchanged.

## Non-goals

- Removing CI verification from releases.
- Restricting branch CI to `main` only.
- Adding retries or accepting flaky outcomes.
- Relaxing directory identity or metadata checks.
- Pre-creating the lock file and thereby avoiding the first-creation race.
- Changing release artifacts, packaging, or publishing behavior.

## Chosen design

### Branch-only standalone push CI

Configure the reusable CI workflow with a branch filter:

```yaml
on:
  push:
    branches:
      - "**"
  pull_request:
  workflow_call:
```

GitHub does not run a `push` workflow for tag refs when only a `branches`
filter is defined. All branch names remain covered by `"**"`. Pull request
and reusable-workflow behavior remain unchanged.

The repository security contract will require this exact closed trigger block,
including the branch-only filter. Mutation tests will reject an unfiltered
`push`, tag filters, and changes to the existing `workflow_call` boundary.

### Isolated subprocess coordination

`TestConcurrentCommitSubprocesses` will create two independent private roots:

- configuration root: authoritative config, backup, lock, and transaction
  artifacts;
- coordination root: child ready files, the parent gate, and child result
  files.

Both child processes continue loading the same base and racing the same
production `Commit` path. Exactly one must commit and exactly one must return
the fail-closed lost-update result. The first lock-file creation remains part
of the exercised behavior.

No production hook, sleep, retry, or lock behavior changes.

## Alternatives considered

### Job-level tag guards

Adding an `if` expression to every CI job would leave a redundant workflow run
visible for tag pushes and make each new job responsible for repeating the
guard. Trigger-level filtering is smaller and closed by default.

### Rely on standalone tag CI from the release workflow

Separating release packaging from its reusable verification would require
cross-workflow coordination and weaken the release job dependency graph.

### Pre-create the lock or retry the integration test

Pre-creating the lock would remove useful first-creation concurrency coverage.
Retrying would hide the false-negative mechanism without removing it.

## Verification

- Observe the workflow contract test fail before changing `ci.yml`.
- Re-run the workflow contract and mutation tests after changing the trigger.
- Run the repaired subprocess test once and in a 1,000-execution stress loop.
- Run the complete integration suite from an isolated clean copy.
- Run unit tests, `go vet`, `golangci-lint`, workflow syntax validation, and
  `git diff --check`.
- Confirm the working tree contains only the intended workflow, test, contract,
  and design/plan documentation changes.

## Acceptance criteria

- Branch pushes still start standalone CI.
- Tag pushes do not start standalone CI.
- The release workflow still calls `.github/workflows/ci.yml`.
- Pull requests still start CI.
- The workflow security contract rejects removal or broadening of the
  branch-only push filter.
- Subprocess coordination files never share the configuration directory.
- The subprocess race produces exactly one commit and one lost update across
  1,000 isolated executions.
- Production code is unchanged.

## References

- [GitHub Actions branch and tag filters](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#onpushbranchestagsbranches-ignoretags-ignore)

