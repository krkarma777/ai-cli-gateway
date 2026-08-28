# Release CI Stability Implementation Plan

> **For agentic workers:** Implement each task in order and preserve the RED,
> GREEN, and verification evidence described below.

**Goal:** Prevent duplicate CI on tag pushes and remove the false-negative
cross-process configstore test race without changing production behavior.

**Architecture:** Make standalone `push` CI branch-only while preserving
`pull_request` and `workflow_call`. Keep configstore's strict transaction
directory isolated from subprocess coordination files.

**Tech Stack:** GitHub Actions YAML, Go integration tests, Go repository
security contract tests, actionlint.

## Constraints

- Approved design:
  `docs/superpowers/specs/2026-08-28-release-ci-stability-design.md`.
- Do not change production configstore code.
- Do not add retries, sleeps, job-level skip guards, or a pre-created lock.
- Preserve every branch push, pull request, and reusable CI invocation.
- Keep the release workflow unchanged.
- Use an isolated repository copy for tests that create trusted fixtures beside
  the repository because the active sandbox cannot write to the workspace
  parent.

### Task 1: Close the CI trigger contract

**Files:**
- Modify: `internal/securitytest/repository_test.go`
- Modify: `.github/workflows/ci.yml`

- [x] Change the expected CI trigger block to require `push.branches: ["**"]`,
  `pull_request`, and `workflow_call` exactly.
- [x] Add mutation coverage that rejects removal of the branch filter and the
  introduction of a tag filter.
- [x] Run the workflow contract tests and record the expected RED failure
  against the still-unfiltered workflow.
- [x] Add the branch-only filter to `.github/workflows/ci.yml`.
- [x] Re-run the workflow contract and mutation tests and record GREEN.

### Task 2: Isolate subprocess test coordination

**Files:**
- Modify: `internal/configstore/store_integration_test.go`

- [x] Preserve the reproduced RED evidence: the unmodified test produced
  `committed=0 lost=2` on isolated execution 10.
- [x] Create a separate private coordination root in
  `TestConcurrentCommitSubprocesses`.
- [x] Place ready, gate, and result files under that root while leaving config,
  backup, lock, and transaction artifacts under the configuration root.
- [x] Run the focused integration test once.
- [x] Build the integration test binary once and run the focused test in 1,000
  isolated executions, stopping at the first failure.

### Task 3: Verify the complete change

**Files:**
- Verify all modified files and both new documentation files.

- [x] Run `gofmt` on the modified Go files and confirm no formatting diff.
- [x] Run focused configstore integration and workflow contract tests.
- [x] Validate both workflows with actionlint v1.7.12.
- [x] Run `go vet ./...` and `golangci-lint run ./...`.
- [x] Run `go test -count=1 ./...`.
- [x] Run `go test -tags=integration -count=1 ./...` from an isolated clean
  repository copy.
- [x] Run `git diff --check` and inspect `git diff` plus `git status`.
- [x] Hand off exact `git add`, commit, and push commands; do not create a new
  release tag for this post-release maintenance change.
