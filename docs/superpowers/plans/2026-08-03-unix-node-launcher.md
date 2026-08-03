# Unix Node Launcher Resolution Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (- [ ]) syntax for tracking.

**Goal:** Make an ordinary absolute Unix CLI path backed by an exact <code>#!/usr/bin/env node</code> launcher pass Doctor without inheriting ambient child state.

**Architecture:** Doctor resolves a closed provider command before probing. On Unix, only the exact Node env shebang becomes a validated, pinned Node executable plus the validated launcher as the first prefix argument; native Unix commands and current Windows behavior remain unchanged. Production discovery uses <code>exec.LookPath</code> through an injected startup-only dependency, while every stored and executed path still passes the existing filesystem trust policy.

**Tech Stack:** Go 1.26.5, standard library only, build-tagged Unix/Windows files, existing Doctor/process/fake-CLI test harnesses.

## Global Constraints

- The approved design is <code>docs/superpowers/specs/2026-08-03-unix-node-launcher-design.md</code>.
- Recognize only <code>#!/usr/bin/env node</code> followed by LF or CRLF; reject env -S, shebang arguments, shells, and arbitrary interpreters.
- Never construct a shell command. Execute only a validated executable plus argv.
- Use startup PATH only for candidate discovery. Never forward ambient PATH or ambient environment values to provider children.
- Validate and pin Node and launcher absolute identities before any adapter probe.
- Missing, relative, unsafe, or replaced Node/launcher paths map to existing <code>executable_unsafe</code>; add no public error code.
- Keep configured Unix <code>prefix_args</code> closed and empty. The launcher prefix is derived internally, never from request data.
- Keep native Unix and existing Windows Node/native behavior unchanged.
- Do not read, copy, modify, or log provider authentication material.
- Add no dependency outside the Go standard library.
- Execute and verify in a clean isolated worktree so user-owned <code>config.toml</code> and <code>.idea/</code> remain untouched.

---

## File Map

- Create <code>internal/doctor/launcher.go</code>: shared closed resolved-command value.
- Create <code>internal/doctor/launcher_unix.go</code>: bounded shebang classification, Node discovery, validation, and identity recheck.
- Create <code>internal/doctor/launcher_windows.go</code>: current Windows node.exe/entrypoint policy moved without behavior change.
- Create <code>internal/doctor/launcher_unix_test.go</code>: Unix resolver and safe-PATH tests.
- Modify <code>internal/doctor/path.go</code>: validate clean and resolved entrypoint directories.
- Modify <code>internal/doctor/doctor.go</code>: resolve and store the closed command before probing.
- Modify <code>internal/doctor/doctor_test.go</code>: dependency, rejection, and resolved-provider assertions.
- Modify <code>internal/app/app.go</code> and <code>internal/app/app_test.go</code>: production lookup seam and dependency contract.
- Modify <code>internal/app/app_integration_test.go</code>: real gateway Doctor process with a fake Node runtime.
- Modify <code>README.md</code> and <code>internal/securitytest/repository_test.go</code>: tested operator contract.

---

### Task 1: Closed platform command resolver

**Files:**
- Create: <code>internal/doctor/launcher.go</code>
- Create: <code>internal/doctor/launcher_unix.go</code>
- Create: <code>internal/doctor/launcher_windows.go</code>
- Create: <code>internal/doctor/launcher_unix_test.go</code>
- Modify: <code>internal/doctor/path.go</code>

**Interfaces:**
- Consumes: <code>validatedPath</code>, <code>validateExecutablePath</code>, <code>validateEntrypointPath</code>, <code>sameValidatedIdentity</code>, and <code>buildSafePath</code>.
- Produces:

~~~go
type resolvedProviderCommand struct {
    Executable validatedPath
    Entrypoint *validatedPath
    PrefixArgs []string
}

func resolveProviderCommand(
    executable validatedPath,
    configuredPrefix []string,
    lookupExecutable func(string) (string, error),
) (resolvedProviderCommand, bool)
~~~

- [ ] **Step 1: Write the failing Unix resolver tests**

Create <code>internal/doctor/launcher_unix_test.go</code> with build tag <code>!windows</code>. Use <code>newSecureUnixTestTree</code>, private 0700 directories, executable 0700 files, and explicit lookup closures.

The success test must cover LF and CRLF and assert one exact lookup:

~~~go
func TestResolveUnixProviderCommandPinsExactEnvNodeLauncher(t *testing.T) {
    for _, ending := range []string{"\n", "\r\n"} {
        t.Run(strconv.Quote(ending), func(t *testing.T) {
            fixture := newUnixNodeLauncherFixture(t, ending)
            calls := 0
            command, ok := resolveProviderCommand(
                fixture.launcher,
                nil,
                func(name string) (string, error) {
                    calls++
                    if name != "node" {
                        t.Fatalf("lookup name=%q", name)
                    }
                    return fixture.node, nil
                },
            )
            if !ok || calls != 1 ||
                command.Executable.Resolved != fixture.node ||
                command.Entrypoint == nil ||
                command.Entrypoint.Clean != fixture.shim ||
                command.Entrypoint.Resolved != fixture.script ||
                !slices.Equal(command.PrefixArgs, []string{fixture.script}) {
                t.Fatalf("command=%+v calls=%d ok=%v", command, calls, ok)
            }
        })
    }
}
~~~

Define the fixture helper in the same test file:

~~~go
type unixNodeLauncherFixture struct {
    node     string
    shim     string
    script   string
    launcher validatedPath
}

func newUnixNodeLauncherFixture(
    t *testing.T,
    ending string,
) unixNodeLauncherFixture {
    t.Helper()
    root := newSecureUnixTestTree(t)
    nodeDir := filepath.Join(root, "node-bin")
    shimDir := filepath.Join(root, "shim-bin")
    packageDir := filepath.Join(root, "package-bin")
    for _, directory := range []string{nodeDir, shimDir, packageDir} {
        if err := os.Mkdir(directory, 0o700); err != nil {
            t.Fatal(err)
        }
    }
    node := filepath.Join(nodeDir, "node")
    writeUnixTestFile(t, node, 0o700)
    script := filepath.Join(packageDir, "codex.js")
    if err := os.WriteFile(
        script,
        []byte("#!/usr/bin/env node"+ending+"fixture"),
        0o700,
    ); err != nil {
        t.Fatal(err)
    }
    if err := os.Chmod(script, 0o700); err != nil {
        t.Fatal(err)
    }
    shim := filepath.Join(shimDir, "codex")
    if err := os.Symlink(script, shim); err != nil {
        t.Fatal(err)
    }
    launcher, disposition := validateExecutablePath(shim)
    if disposition != pathSafe {
        t.Fatalf("launcher disposition=%v", disposition)
    }
    return unixNodeLauncherFixture{
        node: node, shim: shim, script: script, launcher: launcher,
    }
}
~~~

Add a table test containing these exact non-matching payloads. The lookup closure must fail the test if called, and the native executable with empty prefix must be returned:

~~~go
[]string{
    "native fixture",
    "#!/usr/bin/env -S node\n",
    "#!/usr/bin/env node --flag\n",
    "#!/usr/bin/env Node\n",
    "#!/usr/bin/env node",
    "#!/bin/sh\n",
}
~~~

Add rejection subtests for nonempty configured prefix, lookup error, relative lookup result, non-executable Node, group/world-writable Node, unsafe Node ancestor, and launcher replacement. For replacement, remove and rewrite the validated launcher inside the lookup callback before returning Node; assert false so the identity recheck is proven.

Add <code>TestBuildSafePathIncludesLauncherAndInterpreterDirectoriesOnce</code>. Assert stable order: Node clean directory, distinct Node resolved directory, launcher clean directory, distinct launcher resolved directory, then fixed tails, with same-identity duplicates removed.

- [ ] **Step 2: Run focused tests and verify RED**

~~~bash
go test ./internal/doctor -run 'TestResolveUnixProviderCommand|TestBuildSafePathIncludesLauncherAndInterpreterDirectoriesOnce' -count=1
~~~

Expected: compile failure because the resolved-command type and function do not exist.

- [ ] **Step 3: Implement the shared closed command**

Create <code>internal/doctor/launcher.go</code>:

~~~go
package doctor

type resolvedProviderCommand struct {
    Executable validatedPath
    Entrypoint *validatedPath
    PrefixArgs []string
}

func nativeProviderCommand(executable validatedPath) resolvedProviderCommand {
    return resolvedProviderCommand{Executable: executable}
}
~~~

- [ ] **Step 4: Implement exact Unix launcher resolution**

Create <code>internal/doctor/launcher_unix.go</code> with build tag <code>!windows</code>:

~~~go
//go:build !windows

package doctor

import (
    "bytes"
    "os"
)

var (
    unixNodeEnvShebangLF   = []byte("#!/usr/bin/env node\n")
    unixNodeEnvShebangCRLF = []byte("#!/usr/bin/env node\r\n")
)

func resolveProviderCommand(
    executable validatedPath,
    configuredPrefix []string,
    lookupExecutable func(string) (string, error),
) (resolvedProviderCommand, bool) {
    if len(configuredPrefix) != 0 || lookupExecutable == nil {
        return resolvedProviderCommand{}, false
    }
    if !exactUnixNodeEnvLauncher(executable.Resolved) {
        return nativeProviderCommand(executable), true
    }
    candidate, err := lookupExecutable("node")
    if err != nil {
        return resolvedProviderCommand{}, false
    }
    node, disposition := validateExecutablePath(candidate)
    if disposition != pathSafe {
        return resolvedProviderCommand{}, false
    }
    launcher, disposition := validateExecutablePath(executable.Clean)
    if disposition != pathSafe ||
        launcher.Resolved != executable.Resolved ||
        !sameValidatedIdentity(launcher, executable) {
        return resolvedProviderCommand{}, false
    }
    return resolvedProviderCommand{
        Executable: node,
        Entrypoint: &launcher,
        PrefixArgs: []string{launcher.Resolved},
    }, true
}

func exactUnixNodeEnvLauncher(path string) bool {
    file, err := os.Open(path)
    if err != nil {
        return false
    }
    defer func() { _ = file.Close() }()
    payload := make([]byte, len(unixNodeEnvShebangCRLF))
    count, _ := file.Read(payload)
    payload = payload[:count]
    return bytes.HasPrefix(payload, unixNodeEnvShebangLF) ||
        bytes.HasPrefix(payload, unixNodeEnvShebangCRLF)
}
~~~

- [ ] **Step 5: Preserve Windows behavior**

Copy the current Windows branch from <code>resolveProviderEntrypoint</code> into
<code>launcher_windows.go</code>. Leave the old function in <code>doctor.go</code>
until Task 2 replaces its call, so the Task 1 commit remains buildable:

~~~go
//go:build windows

package doctor

import (
    "path/filepath"
    "strings"
)

func resolveProviderCommand(
    executable validatedPath,
    configuredPrefix []string,
    _ func(string) (string, error),
) (resolvedProviderCommand, bool) {
    if len(configuredPrefix) == 0 {
        return nativeProviderCommand(executable), true
    }
    if len(configuredPrefix) != 1 ||
        !strings.EqualFold(filepath.Base(executable.Clean), "node.exe") {
        return resolvedProviderCommand{}, false
    }
    extension := filepath.Ext(configuredPrefix[0])
    if extension != ".js" && extension != ".mjs" {
        return resolvedProviderCommand{}, false
    }
    entrypoint, disposition := validateEntrypointPath(configuredPrefix[0])
    if disposition != pathSafe {
        return resolvedProviderCommand{}, false
    }
    return resolvedProviderCommand{
        Executable: executable,
        Entrypoint: &entrypoint,
        PrefixArgs: []string{entrypoint.Resolved},
    }, true
}
~~~

- [ ] **Step 6: Extend safe-PATH candidates**

In <code>internal/doctor/path.go</code>:

~~~go
if entrypoint != nil {
    candidates = append(
        candidates,
        filepath.Dir(entrypoint.Clean),
        filepath.Dir(entrypoint.Resolved),
    )
}
~~~

Retain current validation and identity deduplication.

- [ ] **Step 7: Run GREEN and Windows compile**

~~~bash
gofmt -w internal/doctor/launcher.go internal/doctor/launcher_unix.go internal/doctor/launcher_windows.go internal/doctor/launcher_unix_test.go internal/doctor/path.go
go test ./internal/doctor -run 'TestResolveUnixProviderCommand|TestBuildSafePathIncludesLauncherAndInterpreterDirectoriesOnce|TestBuildSafePathValidatesEveryCandidateBeforeIdentityDedup' -count=1
GOOS=windows GOARCH=amd64 go test -c ./internal/doctor -o /tmp/ai-cli-gateway-doctor-windows.test.exe
~~~

Expected: focused tests pass and the Windows test binary compiles.

- [ ] **Step 8: Commit**

~~~bash
git add internal/doctor/launcher.go internal/doctor/launcher_unix.go internal/doctor/launcher_windows.go internal/doctor/launcher_unix_test.go internal/doctor/path.go
git commit -m "fix: resolve trusted Unix Node launchers"
~~~

---

### Task 2: Doctor wiring and real fake-Node regression

**Files:**
- Modify: <code>internal/doctor/doctor.go</code>
- Modify: <code>internal/doctor/doctor_test.go</code>
- Modify: <code>internal/app/app.go</code>
- Modify: <code>internal/app/app_test.go</code>
- Modify: <code>internal/app/app_integration_test.go</code>

**Interfaces:**
- Adds <code>LookupExecutable func(string) (string, error)</code> to app and Doctor dependencies.
- Production value is <code>exec.LookPath</code>.
- Doctor stores only resolved Node, resolved launcher prefix, and rebuilt safe PATH.

- [ ] **Step 1: Write failing lookup-dependency contract tests**

Add this case to <code>TestRunRejectsEachNilFunctionDependencyWithoutInvokingAnything</code>:

~~~go
{"LookupExecutable", func(value *Dependencies) {
    value.LookupExecutable = nil
}},
~~~

Add <code>deps.LookupExecutable == nil</code> to
<code>TestProductionDependenciesAreCompleteAndLazy</code>. Give
<code>doctorTestDependencies</code> a nonnil lookup closure so only the selected
nil case is invalid.

- [ ] **Step 2: Verify dependency RED**

~~~bash
go test ./internal/doctor -run TestRunRejectsEachNilFunctionDependency -count=1
go test ./internal/app -run TestProductionDependenciesAreCompleteAndLazy -count=1
~~~

Expected: compile failure because neither dependency struct has
<code>LookupExecutable</code>.

- [ ] **Step 3: Add and wire the lookup dependency**

Add this field to both dependency structs:

~~~go
LookupExecutable func(string) (string, error)
~~~

Require it in <code>doctor.Run</code> and <code>validDoctorDependencies</code>.
Set <code>LookupExecutable: exec.LookPath</code> in ProductionDependencies and
import standard-library os/exec. Forward it in both app calls to
<code>doctor.Run</code>. Do not call it from <code>resolveProvider</code> yet.

- [ ] **Step 4: Verify dependency GREEN**

~~~bash
gofmt -w internal/doctor/doctor.go internal/doctor/doctor_test.go internal/app/app.go internal/app/app_test.go
go test ./internal/doctor -run TestRunRejectsEachNilFunctionDependency -count=1
go test ./internal/app -run TestProductionDependenciesAreCompleteAndLazy -count=1
~~~

Expected: both dependency contract tests pass while provider behavior is still
unchanged.

- [ ] **Step 5: Write failing Doctor and public-command behavior tests**

Add Unix-only <code>TestRunResolvesUnixNodeLauncherBeforeProbe</code>: configure
an exact launcher, return trusted Node from the injected lookup, capture
ProviderConfig in the adapter probe, and assert:

~~~go
if resolved.Executable != validatedNode.Resolved ||
    !slices.Equal(resolved.PrefixArgs, []string{validatedLauncher.Resolved}) ||
    !strings.Contains(resolved.SafePath, filepath.Dir(validatedNode.Resolved)) {
    t.Fatalf("resolved provider command=%+v", resolved)
}
~~~

Assert lookup once with node, one probe, and a transferred ready provider. Extend
the existing path-problem precedence test with exact launchers whose lookup is
missing or unsafe; each must yield only <code>ProblemExecutableUnsafe</code>, nil
resolved providers, and zero probes.

Add Unix-only <code>TestDoctorCommandResolvesExactUnixEnvNodeLauncher</code> to
<code>app_integration_test.go</code>. Build the existing command probe fake,
symlink it as private node-bin/node, and write a separate 0700 launcher containing:

~~~go
[]byte("#!/usr/bin/env node\n")
~~~

Set the gateway subprocess startup PATH to node-bin followed by its prior PATH. Plant <code>PLANTED_AMBIENT_SECRET</code>. Make <code>buildCommandProbeFake</code> exit 91 if that variable reaches the provider child.

The ready subtest runs gateway <code>doctor --config</code>, exits 0, and reports
Codex ready at 0.146.0. The missing-Node subtest uses an empty startup PATH
directory, exits 1, reports <code>executable_unsafe</code>, and exposes neither
planted value nor filesystem paths.

- [ ] **Step 6: Verify behavior RED**

~~~bash
go test ./internal/doctor -run 'TestRunResolvesUnixNodeLauncherBeforeProbe|TestRunProviderPathProblemPrecedence|TestRunRejectsEachNilFunctionDependency' -count=1
go test -tags=integration ./internal/app -run TestDoctorCommandResolvesExactUnixEnvNodeLauncher -count=1
~~~

Expected: Doctor still passes the launcher itself, the ready case fails at version, and missing Node reports version_unreadable instead of executable_unsafe.

- [ ] **Step 7: Resolve the closed command before probing**

Add lookupExecutable to <code>resolveProvider</code>. Replace prefix resolution with:

~~~go
command := nativeProviderCommand(executable)
if executableDisposition == pathSafe {
    var valid bool
    command, valid = resolveProviderCommand(
        executable,
        slices.Clone(configured.PrefixArgs),
        lookupExecutable,
    )
    if !valid {
        executableUnsafe = true
    }
}

safePath := ""
if executableDisposition == pathSafe && !executableUnsafe && defaultsErr == nil {
    var err error
    safePath, err = buildSafePath(
        command.Executable,
        command.Entrypoint,
        defaults,
    )
    if err != nil {
        executableUnsafe = true
    }
} else if executableDisposition == pathSafe {
    executableUnsafe = true
}
~~~

Freeze:

~~~go
providerConfig = provider.ProviderConfig{
    Executable:    command.Executable.Resolved,
    PrefixArgs:    slices.Clone(command.PrefixArgs),
    ConfigHome:    configHome.Resolved,
    CredentialEnv: credentialNames,
    SafePath:      safePath,
    LookupEnv:     frozen.lookup,
}
~~~

After the new call is wired, remove <code>resolveProviderEntrypoint</code>, the
runtime alias, and the filepath import from <code>doctor.go</code>. Keep strings
because <code>unsafeString</code> still uses it.

- [ ] **Step 8: Run behavior GREEN**

~~~bash
gofmt -w internal/doctor/doctor.go internal/doctor/doctor_test.go internal/app/app.go internal/app/app_test.go internal/app/app_integration_test.go
go test ./internal/doctor -run 'TestResolveUnixProviderCommand|TestRunResolvesUnixNodeLauncherBeforeProbe|TestRunProviderPathProblemPrecedence|TestRunRejectsEachNilFunctionDependency' -count=1
go test ./internal/app -run TestProductionDependenciesAreCompleteAndLazy -count=1
go test ./internal/provider/codex -run 'TestBuildTextUsesExactFixedArgvAndIsolatedEnvironment|TestProbeBuildsExactlyFiveIsolatedCommandsInOrder' -count=1
go test -tags=integration ./internal/app -run TestDoctorCommandResolvesExactUnixEnvNodeLauncher -count=1
~~~

Expected: all pass; fake Node runs with launcher first, planted ambient state is absent, and missing Node stops before probing.

- [ ] **Step 9: Commit**

~~~bash
git add internal/doctor/doctor.go internal/doctor/doctor_test.go internal/app/app.go internal/app/app_test.go internal/app/app_integration_test.go
git commit -m "test: cover Unix Node launcher diagnosis"
~~~

---

### Task 3: Tested operator documentation

**Files:**
- Modify: <code>README.md</code>
- Modify: <code>internal/securitytest/repository_test.go</code>
- Modify: <code>docs/superpowers/specs/2026-08-03-unix-node-launcher-design.md</code>

**Interfaces:**
- Produces a public operational contract without API or configuration changes.

- [ ] **Step 1: Write the failing README assertion**

Add a separate requireContainsAll group requiring:

~~~go
requireContainsAll(t, "README Unix Node launcher boundary", readme,
    "#!/usr/bin/env node",
    "startup PATH",
    "validated",
    "pinned",
    "ambient PATH",
    "executable_unsafe",
    "config_home",
    "non-symlink",
    "0700",
)
~~~

- [ ] **Step 2: Verify RED**

~~~bash
go test ./internal/securitytest -run TestREADMECommandsOperationsSecurityAndGeminiBoundary -count=1
~~~

Expected: missing Unix launcher strings.

- [ ] **Step 3: Add the README subsection**

Insert before Windows paths:

~~~markdown
### Unix Node launchers

An absolute provider executable may resolve to a Node launcher whose first line
is exactly #!/usr/bin/env node with LF or CRLF. At startup, Doctor resolves node
once from the startup PATH, applies the same executable and ancestor safety
checks, and pins the absolute Node and launcher identities. Provider children
still receive a rebuilt safe path; the ambient PATH is not inherited. A missing
or unsafe Node candidate reports executable_unsafe before probing.

On Unix, every config_home must be an absolute non-symlink directory owned by
the gateway effective user with exact mode 0700.
~~~

Update the provider-process paragraph to include a pinned interpreter and launcher. Keep Windows text unchanged. Keep only generic paths in the new design; do not expand the developer-path allowlist.

- [ ] **Step 4: Run GREEN and hygiene**

~~~bash
go test ./internal/securitytest -run 'TestREADMECommandsOperationsSecurityAndGeminiBoundary|TestRepositoryHygiene|TestScannerRejectsDeveloperHomePathsExceptApprovedResearchDocs' -count=1
git diff --check
~~~

Expected: all pass with no developer-path, secret, artifact, or whitespace finding.

- [ ] **Step 5: Commit**

~~~bash
git add README.md internal/securitytest/repository_test.go docs/superpowers/specs/2026-08-03-unix-node-launcher-design.md
git commit -m "docs: explain Unix Node launcher safety"
~~~

---

### Task 4: Verification, review, and live handoff

**Files:**
- Verify all changed files; do not alter user-owned files in the original checkout.

**Interfaces:**
- Produces reviewed commits ready for main and the exact non-inference live command.

- [ ] **Step 1: Run the repository verification chain**

~~~bash
make verify
~~~

Expected in order: fmt-check, vet, lint, test, race, integration, build.

- [ ] **Step 2: Mirror remaining CI**

~~~bash
go mod verify
go test -trimpath -count=1 ./...
GOFLAGS=-trimpath go test -count=1 ./internal/testutil ./internal/testcli
go test -tags=live -run '^$' ./internal/provider/...
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/ai-cli-gateway-linux-amd64 ./cmd/ai-cli-gateway
GOOS=linux GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o /tmp/ai-cli-gateway-linux-arm64 ./cmd/ai-cli-gateway
GOOS=darwin GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/ai-cli-gateway-darwin-amd64 ./cmd/ai-cli-gateway
GOOS=darwin GOARCH=arm64 CGO_ENABLED=0 go build -trimpath -o /tmp/ai-cli-gateway-darwin-arm64 ./cmd/ai-cli-gateway
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -o /tmp/ai-cli-gateway-windows-amd64.exe ./cmd/ai-cli-gateway
~~~

Expected: all tests/builds pass; live contracts compile without inference.

- [ ] **Step 3: Review**

Invoke superpowers:requesting-code-review with the design, this plan, base commit 2f144c5, and implementation HEAD. Require checks for exact shebang allowlist, one-time lookup and pinning, no ambient child environment, replacement rejection, unchanged Windows/native behavior, redacted error mapping, and RED/GREEN evidence. Fix only verified findings and rerun focused tests.

- [ ] **Step 4: Verify completion evidence**

Invoke superpowers:verification-before-completion. Rerun final commands after any review fix, then inspect:

~~~bash
git status --short
git log --oneline --decorate -5
~~~

Expected: no uncommitted tracked changes in the clean worktree.

- [ ] **Step 5: Integrate and push**

Invoke superpowers:finishing-a-development-branch. Integrate reviewed commits into main without adding .idea, local config.toml, credentials, or binaries. Push main and confirm remote commit and CI.

- [ ] **Step 6: Live non-inference Doctor handoff**

After the fix is present in the original checkout, the user runs in the shell where the gateway key is exported:

~~~bash
go run ./cmd/ai-cli-gateway doctor --config ./config.toml
~~~

Expected: version_unreadable is gone. Diagnose any later capability/auth result independently because the earlier probe short-circuited.

Only after Codex is ready:

~~~bash
go run ./cmd/ai-cli-gateway serve --config ./config.toml
~~~

Expected: server remains running on configured loopback until interrupted.
