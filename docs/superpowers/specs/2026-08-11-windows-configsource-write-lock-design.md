# Windows Configuration Source Write Lock

Date: 2026-08-11

Status: Design approved; written review pending

## Context

`configsource.Load` binds decoded configuration, a SHA-256 digest, native file
metadata, and the selected path to one retained file handle. The application
keeps that snapshot for its lifetime and performs a final `Revalidate` before
the HTTP server starts.

On Unix, ctime changes provide durable evidence for an in-place mutation even
when a test restores the original bytes and mtime. On Windows, the equivalent
tests currently assume that `FILE_BASIC_INFO.ChangeTime` must advance across an
immediate mutate-restore sequence. Windows CI has shown that this assumption is
nondeterministic: the original digest and timestamps can be observed again in
the same clock tick or through cached metadata. This makes the test flaky and
leaves Windows dependent on observation after an in-place write has already
occurred.

The retained Windows handle currently requests `GENERIC_READ` while sharing
read, write, and delete access. The Windows API therefore allows another handle
to request data or attribute write access while the snapshot remains live.

## Goals

- Prevent in-place data and timestamp mutation of a successfully retained
  Windows configuration source for the snapshot lifetime.
- Preserve atomic path replacement so deployment tools and editors that replace
  a file can still operate.
- Keep the existing retained-handle, metadata, path-identity, and digest checks.
- Replace timing-sensitive Windows tests with deterministic contract tests.
- Keep Unix mutation-restoration coverage based on ctime.
- Preserve the public API and the fixed, path-free `ErrUnavailable` error
  contract.

## Non-goals

- Adding configuration hot reload.
- Preventing atomic replacement of the selected path.
- Changing Unix open or mutation semantics.
- Adding polling, sleeps, retries, or forced timestamp advancement to tests.
- Introducing oplocks, directory watchers, or change-notification machinery.
- Changing application-level error mapping or exposing Windows error details.

## Chosen design

### Retained Windows handle

Change only the share mode used by `openSourceFile` on Windows:

```text
desired access: GENERIC_READ
before:         FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE
after:          FILE_SHARE_READ | FILE_SHARE_DELETE
```

Omitting `FILE_SHARE_WRITE` makes successful acquisition of the retained handle
the write-stability boundary. While that handle is open, later opens requesting
data-write or attribute-write access to the same file are rejected by Windows
sharing checks. If a conflicting writer is already open, `Load` cannot acquire
the retained handle and continues to fail closed as `ErrUnavailable`.

`FILE_SHARE_DELETE` remains present. A process may therefore rename or delete
the directory entry and create a replacement at the selected path. The retained
handle continues to refer to the original file object, so replacement cannot
change the decoded configuration or digest already held by the snapshot.

### Path metadata handle

Do not change `openWindowsSourcePathWith`. Its short-lived metadata handle keeps
the permissive `FILE_SHARE_READ | FILE_SHARE_WRITE | FILE_SHARE_DELETE` mode.
That handle only reads path identity and metadata; making it deny writes would
add a transient lock without strengthening the retained snapshot boundary.

### Revalidation flow

The existing flow remains:

1. Open and retain the selected source handle.
2. Capture stable handle and path metadata.
3. Read, decode, and digest bytes from that handle.
4. Re-read metadata and bytes from the same handle before returning `Snapshot`.
5. Before server startup, compare retained-handle metadata, current path
   identity, and digest again.
6. Release the retained handle only when `Snapshot.Close` runs.

The stronger share mode removes in-place Windows writes from the set of changes
that must be inferred after the fact. Existing identity checks still reject a
path replacement that occurs during startup. A replacement after startup does
not hot-reload or alter the immutable in-memory snapshot.

### Platform behavior

- Windows: a live configuration snapshot denies new in-place data and attribute
  writers; delete/rename sharing remains enabled.
- Unix: writers remain possible, and metadata plus digest revalidation continues
  to detect changes. Restoring bytes and mtime still changes ctime.
- All platforms: atomic replacement leaves the retained file object unchanged;
  path identity checks detect replacement during a revalidation boundary.

## Error behavior

No exported signatures or sentinel errors change.

- Failure to acquire the retained Windows handle, including a sharing conflict,
  maps to the exact `ErrUnavailable` sentinel.
- Revalidation failure continues to return the exact `ErrUnavailable` sentinel.
- Errors remain path-free and do not wrap native filesystem errors.
- A separate process attempting an in-place Windows edit receives its native
  sharing error from Windows; the gateway does not translate errors for other
  processes.

## Test design

### Common tests

Replace the common in-place filesystem mutation test with an injected
`sourceReader` that returns the original bytes during load and different bytes
during explicit revalidation. This deterministically exercises digest mismatch
handling on every platform without relying on an operation that Windows will
now deny.

Keep the common path-replacement test. It proves that retaining
`FILE_SHARE_DELETE` still permits replacement and that `Revalidate` rejects the
new path identity.

### Unix tests

Move both restored-mutation filesystem scenarios and their helpers to
`source_unix_test.go`:

- mutation restored to the original digest and mtime before `Revalidate`;
- mutation restored during the initial retained-handle read.

These tests intentionally assert the Unix ctime contract. No sleep or timestamp
padding is added.

### Windows tests

Remove the restored-mtime helper and add real filesystem contract tests that:

- load and retain a configuration snapshot;
- verify a new data-write handle fails with `ERROR_SHARING_VIOLATION`;
- verify a new `FILE_WRITE_ATTRIBUTES` handle also fails with
  `ERROR_SHARING_VIOLATION`;
- close the snapshot and verify each previously denied access can then be
  acquired; and
- continue to pass the existing path-replacement test, demonstrating that
  delete sharing was not removed.

The tests verify observable OS behavior rather than only checking a constant
passed to a mocked function.

## Documentation

Update `docs/reference.md` with one concise Windows-specific operational note:
while the process retains its startup configuration, in-place edits are denied;
stop/edit/restart is supported, and atomic replacement still does not hot-reload
the running process.

## Risks and mitigations

- Some editors save in place on Windows and will receive a sharing violation
  while the gateway runs. This is intentional and will be documented.
- Accidentally removing delete sharing would break atomic-save workflows. The
  cross-platform path-replacement test remains an acceptance test.
- A test that only inspects constants could miss actual Windows behavior. Native
  Windows tests will acquire conflicting handles and assert the system result.
- The change could accidentally alter the fixed error boundary. Existing
  path-free error tests remain required.

## Acceptance criteria

- `openSourceFile` on Windows shares read and delete access, but not write
  access.
- The short-lived Windows path metadata open remains fully shared.
- Data and attribute writes are denied while a snapshot is retained and succeed
  after `Close`.
- Atomic path replacement remains possible and is rejected at revalidation.
- Digest mismatch coverage is deterministic and platform-independent.
- Restored-mutation tests run only where the Unix ctime contract applies.
- No timing sleeps, retries, or timestamp manipulation are introduced to make
  Windows tests pass.
- Public APIs and exact path-free `ErrUnavailable` behavior are unchanged.
- `go test -count=1 ./...` passes locally.
- Windows CI unit, integration, trimpath, and build jobs pass without rerunning a
  timing-sensitive `configsource` failure.

## References

- [CreateFileW sharing semantics](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-createfilew)
- [FILE_BASIC_INFO fields](https://learn.microsoft.com/en-us/windows/win32/api/winbase/ns-winbase-file_basic_info)
- [SetFileTime](https://learn.microsoft.com/en-us/windows/win32/api/fileapi/nf-fileapi-setfiletime)
