# Unix Node Launcher Resolution Design

**Date:** 2026-08-03

**Status:** Approved for implementation

## Summary

AI CLI Gateway will support Unix provider executables installed as trusted Node
launchers whose first line is exactly `#!/usr/bin/env node`. Doctor will resolve
`node` once at startup, validate and pin its absolute filesystem identity, and
execute the provider as an argv vector equivalent to `node /absolute/launcher.js
...`. The provider process will continue to receive only the gateway's rebuilt
environment and safe `PATH`; no shell or ambient environment inheritance is
introduced.

This fixes the installed Codex CLI case where `command -v codex` resolves through
an npm symlink to `codex.js`, while the gateway's safe `PATH` omits the separately
installed Node directory.

## Evidence and Root Cause

The configured Codex executable is:

```text
<configured-codex-path>
  -> <npm-package-root>/bin/codex.js
```

The resolved script starts with:

```text
#!/usr/bin/env node
```

Doctor currently executes the resolved script with a `PATH` containing only the
configured launcher directory, the resolved launcher directory, `/usr/bin`, and
`/bin`. The installed Node executable is `/usr/local/bin/node`. Reproducing the
version probe with Doctor's environment exits 127 with `env: node: No such file
or directory`; adding only `/usr/local/bin` makes `codex-cli 0.146.0` succeed.

Codex probing intentionally stops after an unreadable version. Consequently,
the reported `capability_missing` and `auth_unknown` are unverified follow-on
states, not evidence of a capability or login failure. Server startup correctly
fails closed while Doctor reports the provider as not ready.

## Goals

- Accept the ordinary absolute path returned by `command -v codex` for the
  official npm-distributed Codex CLI on Unix.
- Pin both the Node interpreter and launcher to validated absolute paths before
  any provider probe or request execution.
- Preserve shell-free argv execution, the minimal child environment, bounded
  subprocess behavior, and existing process-tree cleanup.
- Reject missing, relative, replaced, untrusted, or permission-unsafe interpreter
  paths before an adapter probe begins.
- Keep native Unix executables and Windows Node-entrypoint behavior unchanged.

## Non-goals

- General-purpose shebang parsing or arbitrary interpreter support.
- Support for `/usr/bin/env -S`, shebang arguments, shell launchers, `.cmd`, or
  `.bat` files.
- Inheriting the user's complete `PATH` or environment in provider children.
- Discovering, reading, copying, or modifying CLI authentication material.
- Resolving provider package-private native binaries.
- Adding a user-configurable arbitrary command prefix on Unix.

## Considered Approaches

### 1. Resolve and pin the Node launcher chain — selected

Doctor recognizes only the exact Unix shebang `#!/usr/bin/env node`, resolves
`node` using the gateway startup process's `PATH`, applies the existing executable
and ancestor safety policy, and converts the command internally to an absolute
Node executable plus the absolute launcher path as the first argv item.

This preserves the normal CLI path in configuration, avoids runtime interpreter
lookup, and keeps the existing trust policy. The one-time startup lookup is only
a candidate discovery mechanism; validation, resolution, and stored values use
the existing absolute-path rules.

### 2. Add the discovered Node directory to child `PATH`

This is smaller but leaves `/usr/bin/env` to perform a name lookup on every
execution. It is less deterministic than storing the validated interpreter
identity and exposes every executable in the added directory through `PATH`.

### 3. Configure the package-private native Codex binary

This avoids Node but depends on npm package internals and versioned platform
paths. It is unsuitable as a stable public configuration contract.

## Architecture

Provider startup resolution will produce one closed command description:

```text
configured executable
  -> validate and resolve launcher
  -> classify exact Unix Node env shebang
  -> resolve and validate Node candidate
  -> pinned executable + pinned prefix argv + safe PATH
  -> adapter Probe
```

For a native Unix executable, the result remains:

```text
Executable: resolved configured executable
PrefixArgs: []
```

For an exact Node launcher, the result becomes:

```text
Executable: resolved Node executable
PrefixArgs: [resolved configured launcher]
```

Adapters remain unaware of how the executable was distributed. They prepend the
trusted `PrefixArgs` before their fixed probe or request argv exactly as they do
for the existing Windows Node form.

Platform-specific launcher classification and interpreter discovery belong in a
Unix-only file. The untagged Doctor orchestration consumes a small resolved
command value, while existing filesystem validators remain the source of truth
for trust decisions. Windows behavior stays in its current platform-specific
implementation.

## Resolution Algorithm

1. Clean, resolve, and validate the configured executable using the existing
   double-checked Unix path walk.
2. Reject configured Unix `prefix_args` exactly as today.
3. Read only a small bounded first-line prefix from the already validated
   executable.
4. If the first line is not exactly `#!/usr/bin/env node` with LF or CRLF line
   termination, retain native-executable behavior.
5. If it matches, look up `node` using the startup process's current `PATH`.
6. Require the candidate to be absolute after lookup, then run it through the
   existing executable validator, including ownership, mode, symlink, ancestor,
   and stable-identity checks.
7. Revalidate the launcher identity after classification so a replacement during
   inspection fails closed.
8. Store only the resolved Node path and resolved launcher path in the immutable
   provider configuration passed to the adapter.
9. Construct safe `PATH` from the validated configured path, resolved launcher
   path, pinned Node path, and the fixed Unix tails `/usr/bin` and `/bin`, with
   existing identity-based deduplication.

The launcher remains a trusted executable object: it must be a regular file owned
by root or the gateway effective UID, have an execute bit, have no group/other
write bit, and have no setuid, setgid, or sticky bit. The Node executable and all
new path directories meet the same applicable policies.

## Error Model

- A non-Node native executable follows existing behavior.
- An exact Node launcher whose interpreter is missing, relative, unsafe, changes
  identity during resolution, or produces an invalid safe path fails before
  probing with the existing `executable_unsafe` problem.
- No filesystem path, interpreter lookup detail, stderr, or environment value is
  added to public Doctor output.
- Later adapter failures retain their current stable problem codes.
- No new API error code or HTTP response shape is introduced.

## Security Boundary

- No shell string is constructed; execution remains an executable plus argv.
- Ambient `PATH` is used only to discover a candidate at startup. It is not
  forwarded to children.
- A discovered candidate is never trusted by name alone. The existing absolute
  path, ownership, permissions, ancestor, symlink, and identity validation gates
  its use.
- Only the exact no-argument Node env shebang is recognized. Arbitrary shebangs,
  options, additional arguments, and non-Node interpreters are not transformed.
- Authentication files and config contents are not inspected by launcher
  resolution. The configured provider home remains the only credential boundary.
- Existing stdout/stderr limits, timeouts, cancellation, process-group cleanup,
  concurrency limits, and bounded queue behavior remain unchanged.

## Testing Strategy

### Unit tests

- Exact LF and CRLF Node shebangs resolve to a validated Node executable plus the
  launcher prefix.
- Native executables remain executable-with-empty-prefix.
- Missing, relative, non-executable, untrusted-owner, group/world-writable, and
  identity-changing Node candidates fail closed.
- Arbitrary interpreters, `/usr/bin/env -S node`, shebang arguments, malformed or
  overlong first lines, and non-exact casing are not auto-transformed.
- Safe `PATH` contains validated launcher and interpreter directories without
  duplicates.
- Existing Windows Node and native executable tests remain unchanged and pass.

### Integration tests

- A fake executable launcher with `#!/usr/bin/env node` reaches the version probe
  through a fake validated Node runtime without inheriting ambient child state.
- Probe argv starts with the pinned launcher and then the adapter's fixed command.
- A missing interpreter yields a pre-probe not-ready result and never invokes the
  adapter.

### Verification

- Focused Doctor and Unix path tests.
- `go test ./...` on the development platform.
- Repository lint and formatting checks.
- Cross-platform build and existing native Windows contract tests.
- Full project build.
- User reruns the real Codex Doctor with the unchanged provider executable path;
  no inference request is required to prove launcher resolution.

## Documentation

README troubleshooting will explain:

- Unix npm launchers using the exact Node env shebang are resolved and pinned by
  Doctor.
- The gateway startup environment must be able to locate `node` once.
- Doctor still rejects an interpreter or path that fails filesystem safety checks.
- `config_home` must be a private non-symlink directory with mode `0700` on Unix.

The project continues to describe itself as a Responses API-compatible subset;
this change does not expand the HTTP API contract.

## Acceptance Criteria

- An existing absolute executable setting obtained from `command -v codex`
  passes the version probe when a validated Node executable is discoverable at
  startup.
- Child provider execution never receives the ambient `PATH`.
- Unsafe or unavailable Node resolution prevents probing and server startup.
- Native Unix providers and Windows providers retain current behavior.
- New regression tests fail on the current implementation and pass only after
  the launcher-resolution change.
- Unit tests, fake CLI integration tests, lint, and build all pass before commit
  and push.
