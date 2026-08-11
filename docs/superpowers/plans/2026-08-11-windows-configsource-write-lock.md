# Windows Configuration Source Write Lock Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make a retained Windows configuration source deny in-place content writes while preserving atomic replacement and deterministic cross-platform tests.

**Architecture:** Keep the existing retained-handle and revalidation architecture. On Windows, remove only `FILE_SHARE_WRITE` from the long-lived source handle; leave the short-lived path metadata handle fully shared and leave Unix behavior unchanged. Reorganize tests so digest mismatch is injected portably, restored-mutation coverage is Unix-specific, and native Windows tests prove the actual sharing contract.

**Tech Stack:** Go, `golang.org/x/sys/windows`, Go build tags, GitHub Actions `windows-latest`, GitHub CLI.

## Global Constraints

- The approved design is `docs/superpowers/specs/2026-08-11-windows-configsource-write-lock-design.md`.
- The retained Windows source handle must use exactly `FILE_SHARE_READ | FILE_SHARE_DELETE`.
- The short-lived Windows path metadata handle must remain `FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE`.
- `FILE_SHARE_DELETE` must remain enabled so atomic rename/replacement continues to work.
- Unix open behavior and ctime-based restored-mutation detection must not change.
- Exported APIs, application error mapping, and the exact path-free `ErrUnavailable` sentinel contract must not change.
- Attribute-only requests such as `FILE_WRITE_ATTRIBUTES` are outside the lock
  guarantee because `CreateFileW` share flags do not govern them.
- Tests must not add sleeps, polling, retries, or forced timestamp advancement.
- Production code may be written only after the native Windows write-denial test has failed for the expected reason.

---

### Task 1: Make mutation coverage platform-correct and deterministic

**Files:**
- Modify: `internal/configsource/source_test.go:3-167`
- Modify: `internal/configsource/source_unix_test.go:14-70`
- Modify: `internal/configsource/source_windows_test.go:5-37`

**Interfaces:**
- Consumes: existing `loadWithOpenAndRead(path string, open sourceOpener, read sourceReader) (*Snapshot, error)` and `readSourceBytes(*os.File) ([]byte, bool)`.
- Produces: a platform-independent digest mismatch test and Unix-only restored-mutation helpers/tests; no production interface changes.

- [ ] **Step 1: Replace the common filesystem mutation test with a deterministic reader-driven digest mismatch**

Remove the `time` import and replace the three tests from
`TestSnapshotRevalidateRejectsInPlaceContentChangeWithoutReplacingConfig`
through `TestLoadRejectsMutationRestoredDuringInitialRetainedHandleRead` with:

```go
func TestSnapshotRevalidateRejectsDigestMismatchWithoutReplacingConfig(t *testing.T) {
	path := writeSourceConfig(t, "ORIGINAL_KEY")
	reads := 0
	snapshot, err := loadWithOpenAndRead(
		path,
		openSourceFile,
		func(file *os.File) ([]byte, bool) {
			raw, ok := readSourceBytes(file)
			if !ok {
				return nil, false
			}
			reads++
			if reads == 3 {
				raw[len(raw)/2] ^= 1
			}
			return raw, true
		},
	)
	if err != nil {
		t.Fatalf("loadWithOpenAndRead() error = %v", err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	if reads != 2 {
		t.Fatalf("reads after load = %d, want 2", reads)
	}
	want := snapshot.Config()

	assertSourceUnavailable(t, snapshot.Revalidate())
	if reads != 3 {
		t.Fatalf("reads after Revalidate = %d, want 3", reads)
	}
	if got := snapshot.Config(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Config() changed after failed revalidation: got %+v, want %+v", got, want)
	}
}
```

Delete `mutateAndRestoreSource` from the bottom of `source_test.go`. Keep
`TestSnapshotRevalidateRejectsPathReplacement` unchanged.

- [ ] **Step 2: Move restored-mutation behavior and its helper into the Unix test file**

Place these functions after `restoreSourceModTime` in
`internal/configsource/source_unix_test.go`:

```go
func mutateAndRestoreSource(
	t *testing.T,
	path string,
	original []byte,
	modTime time.Time,
) {
	t.Helper()
	mutated := append([]byte(nil), original...)
	mutated[len(mutated)/2] ^= 1
	if err := os.WriteFile(path, mutated, 0o600); err != nil { // #nosec G703 -- path is created by writeSourceConfig in this test's private TempDir.
		t.Fatalf("write mutated source: %v", err)
	}
	if err := os.WriteFile(path, original, 0o600); err != nil { // #nosec G703 -- path is created by writeSourceConfig in this test's private TempDir.
		t.Fatalf("restore original source: %v", err)
	}
	restoreSourceModTime(t, path, modTime)
}

func TestSnapshotRevalidateRejectsMutationRestoredToOriginalDigestAndMtime(t *testing.T) {
	path := writeSourceConfig(t, "ORIGINAL_KEY")
	original, err := os.ReadFile(path) // #nosec G304 -- path is created by writeSourceConfig in this test's private TempDir.
	if err != nil {
		t.Fatalf("read original source: %v", err)
	}
	snapshot, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	baseline, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat baseline source: %v", err)
	}

	mutateAndRestoreSource(t, path, original, baseline.ModTime())

	assertSourceUnavailable(t, snapshot.Revalidate())
}

func TestLoadRejectsMutationRestoredDuringInitialRetainedHandleRead(t *testing.T) {
	path := writeSourceConfig(t, "ORIGINAL_KEY")
	original, err := os.ReadFile(path) // #nosec G304 -- path is created by writeSourceConfig in this test's private TempDir.
	if err != nil {
		t.Fatalf("read original source: %v", err)
	}
	baseline, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat baseline source: %v", err)
	}
	opens := 0
	reads := 0
	snapshot, err := loadWithOpenAndRead(
		path,
		func(actual string) (*os.File, error) {
			opens++
			return openSourceFile(actual)
		},
		func(file *os.File) ([]byte, bool) {
			reads++
			if reads == 1 {
				mutateAndRestoreSource(t, path, original, baseline.ModTime())
			}
			return readSourceBytes(file)
		},
	)
	if snapshot != nil {
		_ = snapshot.Close()
		t.Fatalf("loadWithOpenAndRead() snapshot = %#v, want nil", snapshot)
	}
	assertSourceUnavailable(t, err)
	if opens != 1 || reads != 1 {
		t.Fatalf("open/read calls = %d/%d, want 1/1", opens, reads)
	}
}
```

- [ ] **Step 3: Remove the obsolete Windows restored-mtime helper**

Delete `restoreSourceModTime` from
`internal/configsource/source_windows_test.go` and remove its `time` import. Do
not change `TestWindowsSourcePathMetadataUsesCompatibleShareAndNativeIdentity`;
its assertion protects the permissive path metadata handle.

- [ ] **Step 4: Format and verify the platform split**

Run:

```bash
gofmt -w internal/configsource/source_test.go internal/configsource/source_unix_test.go internal/configsource/source_windows_test.go
go test -count=1 ./internal/configsource
GOOS=windows GOARCH=amd64 go test -exec=/usr/bin/true ./internal/configsource
```

Expected: the native macOS package tests pass, and the Windows test package
cross-compiles with `ok`. The Windows binary is not executed by the compile
check.

- [ ] **Step 5: Commit the deterministic test boundary**

```bash
git add internal/configsource/source_test.go internal/configsource/source_unix_test.go internal/configsource/source_windows_test.go
git commit -m "test: make config source mutation coverage platform-specific"
```

Expected: one tests-only commit; `git status --short` is empty.

---

### Task 2: Prove and enforce the native Windows write lock

**Files:**
- Modify: `internal/configsource/source_windows_test.go:5-120`
- Modify: `internal/configsource/source_windows.go:42-55`

**Interfaces:**
- Consumes: existing `Load(string) (*Snapshot, error)`, `Snapshot.Close() error`, `os.OpenFile`, and `windows.ERROR_SHARING_VIOLATION`.
- Produces: the observable contract that a new data-write handle fails with `ERROR_SHARING_VIOLATION` until `Snapshot.Close`.

- [ ] **Step 1: Write the native Windows failing contract test**

Add `errors` to the Windows test imports and place the following test
before `TestSameWindowsSourceIdentityUsesVolumeAndFileIndex`:

```go
func TestWindowsRetainedSourceDeniesDataWriteUntilClose(t *testing.T) {
	path := writeSourceConfig(t, "SOURCE_KEY")
	snapshot, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })

	writer, openErr := os.OpenFile(path, os.O_WRONLY, 0)
	if writer != nil {
		if err := writer.Close(); err != nil {
			t.Fatalf("close unexpectedly admitted writer: %v", err)
		}
	}
	if !errors.Is(openErr, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("open writer while retained error = %v, want ERROR_SHARING_VIOLATION", openErr)
	}

	if err := snapshot.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	writer, err = os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open writer after Close() error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer after Close(): %v", err)
	}
}
```

- [ ] **Step 2: Confirm the new Windows test compiles before the RED run**

Run:

```bash
gofmt -w internal/configsource/source_windows_test.go
GOOS=windows GOARCH=amd64 go test -exec=/usr/bin/true ./internal/configsource
go test -count=1 ./internal/configsource
```

Expected: cross-compilation and the host-platform tests pass. This does not count
as the RED observation because the native Windows behavior has not run.

- [ ] **Step 3: Commit and push the test-only RED state**

```bash
git add internal/configsource/source_windows_test.go
git commit -m "test: define Windows config source write-lock contract"
git push -u origin fix/windows-configsource-write-lock
```

Expected: GitHub Actions starts a `push` run for the branch.

- [ ] **Step 4: Watch the native Windows test fail for the intended reason**

Run:

```bash
windows_red_run="$(gh run list --workflow CI --branch fix/windows-configsource-write-lock --event push --limit 1 --json databaseId --jq '.[0].databaseId')"
gh run watch "$windows_red_run" --exit-status
```

Expected: the command exits nonzero and the Windows job reports
`TestWindowsRetainedSourceDeniesDataWriteUntilClose` with an admitted writer
(`openErr` is nil), proving the old `FILE_SHARE_WRITE` behavior. If the failure
is compilation, setup, or an unrelated flaky test, fix or rerun until this exact
contract failure is observed before touching production code.

- [ ] **Step 5: Apply the minimal production fix**

In `internal/configsource/source_windows.go`, change only the retained source
handle share mode:

```go
handle, err := windows.CreateFile(
	path16,
	windows.GENERIC_READ,
	windows.FILE_SHARE_READ|windows.FILE_SHARE_DELETE,
	nil,
	windows.OPEN_EXISTING,
	windows.FILE_FLAG_OPEN_REPARSE_POINT,
	0,
)
```

Do not alter `openWindowsSourcePathWith`.

- [ ] **Step 6: Verify the minimal change locally**

Run:

```bash
gofmt -w internal/configsource/source_windows.go
go test -count=1 ./internal/configsource
GOOS=windows GOARCH=amd64 go test -exec=/usr/bin/true ./internal/configsource
git diff --check
```

Expected: all commands pass and the diff contains one production-line behavior
change.

- [ ] **Step 7: Commit and push the GREEN state**

```bash
git add internal/configsource/source_windows.go
git commit -m "fix: deny writes to retained Windows config source"
git push origin fix/windows-configsource-write-lock
```

- [ ] **Step 8: Watch the native Windows contract turn green**

Run:

```bash
windows_green_run="$(gh run list --workflow CI --branch fix/windows-configsource-write-lock --event push --limit 1 --json databaseId --jq '.[0].databaseId')"
gh run watch "$windows_green_run" --exit-status
```

Expected: the Windows job passes its unit, native integration, trimpath, and
build steps. In particular, the data-write lock test and
`TestSnapshotRevalidateRejectsPathReplacement` both pass.

---

### Task 3: Document the Windows operational behavior

**Files:**
- Modify: `docs/reference.md:306-314`

**Interfaces:**
- Consumes: the immutable startup snapshot behavior documented in the existing security details.
- Produces: operator guidance for Windows in-place edits and atomic replacements.

- [ ] **Step 1: Add the exact Windows note after the immutable startup snapshot paragraph**

Add:

```markdown
On Windows, the retained configuration handle denies in-place content writes until shutdown. Stop the gateway before editing the file in place, then restart it. Atomic replacement can still succeed, but the running process keeps its original startup snapshot and does not hot-reload the replacement.
```

- [ ] **Step 2: Verify the wording and whitespace**

Run:

```bash
rg -n "retained configuration handle|no hot reload" docs/reference.md
git diff --check
```

Expected: both immutable-snapshot statements are present and no whitespace
errors are reported.

- [ ] **Step 3: Commit the documentation**

```bash
git add docs/reference.md
git commit -m "docs: explain Windows config write locking"
```

---

### Task 4: Verify, review, open the PR, and merge

**Files:**
- Verify: `internal/configsource/source.go`
- Verify: `internal/configsource/source_windows.go`
- Verify: `internal/configsource/source_test.go`
- Verify: `internal/configsource/source_unix_test.go`
- Verify: `internal/configsource/source_windows_test.go`
- Verify: `docs/reference.md`

**Interfaces:**
- Consumes: all commits from Tasks 1-3 and the repository CI workflow.
- Produces: a reviewed, green pull request merged into `main`.

- [ ] **Step 1: Run the final local verification suite**

Run each command separately:

```bash
gofmt -w internal/configsource/source_test.go internal/configsource/source_unix_test.go internal/configsource/source_windows.go internal/configsource/source_windows_test.go
go test -count=1 ./internal/configsource
go test -count=1 ./...
go vet ./...
GOOS=windows GOARCH=amd64 go test -exec=/usr/bin/true ./internal/configsource
git diff --check origin/main...HEAD
git status --short --branch
```

Expected: tests and vet pass, the Windows package cross-compiles, the committed
diff has no whitespace errors, and the branch is clean.

- [ ] **Step 2: Review the complete branch diff against the approved design**

Run:

```bash
git diff --stat origin/main...HEAD
git diff origin/main...HEAD -- internal/configsource docs/reference.md docs/superpowers
```

Confirm every acceptance criterion from the design has a corresponding code,
test, or documentation change; confirm `openWindowsSourcePathWith` remains
fully shared; confirm no path-bearing error was added.

- [ ] **Step 3: Push the final documentation commit**

```bash
git push origin fix/windows-configsource-write-lock
```

Expected: the remote branch points at the clean reviewed HEAD.

- [ ] **Step 4: Create the pull request**

Run:

```bash
gh pr create --base main --head fix/windows-configsource-write-lock --title "fix: deny Windows config source writes" --body "## Summary
- deny in-place content writes while Windows retains the configuration source
- keep delete sharing so atomic replacement remains available
- replace ChangeTime-sensitive Windows coverage with native sharing contract tests

## Testing
- go test -count=1 ./internal/configsource
- go test -count=1 ./...
- go vet ./...
- Windows GitHub Actions unit, integration, trimpath, and build"
```

Expected: GitHub prints the new PR URL.

- [ ] **Step 5: Wait for every required PR check**

```bash
gh pr checks --watch
```

Expected: every required check is green. Do not merge while any check is
pending, skipped unexpectedly, or failing.

- [ ] **Step 6: Confirm merge readiness and merge**

Run:

```bash
gh pr view --json number,url,mergeable,mergeStateStatus,reviewDecision,statusCheckRollup
gh pr merge --merge
```

Expected: `mergeable` is `MERGEABLE`, `mergeStateStatus` is `CLEAN`, all checks
are successful, and GitHub reports the PR merged with a merge commit.

- [ ] **Step 7: Verify the merged main branch and post-merge CI**

Run:

```bash
git -C /Users/krkarma777/Dev/ai-cli-gateway pull --ff-only origin main
main_postmerge_run="$(gh run list --workflow CI --branch main --event push --limit 1 --json databaseId --jq '.[0].databaseId')"
gh run watch "$main_postmerge_run" --exit-status
git -C /Users/krkarma777/Dev/ai-cli-gateway status --short --branch
```

Expected: local `main` fast-forwards to the merge commit, the post-merge CI run
passes, and the main worktree is clean.
