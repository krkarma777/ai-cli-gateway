//go:build !windows

package sdkcontract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"golang.org/x/sys/unix"
)

func TestGeneratedConfigDecodesToClosedContract(t *testing.T) {
	data := generatedConfig(32123, "/safe/fake-codex", "/safe/config-home", "/safe/runtime")
	decoded, err := config.Decode(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("config.Decode() error = %v", err)
	}
	if decoded.Server.Listen != "127.0.0.1:32123" || time.Duration(decoded.Server.ShutdownTimeout) != 8*time.Second {
		t.Fatalf("server = %#v", decoded.Server)
	}
	if decoded.Runtime.Root != "/safe/runtime" || time.Duration(decoded.Runtime.TermGrace) != time.Second || time.Duration(decoded.Runtime.CleanupTimeout) != 2*time.Second {
		t.Fatalf("runtime = %#v", decoded.Runtime)
	}
	provider := decoded.Providers["codex"]
	if provider.Executable != "/safe/fake-codex" || provider.ConfigHome != "/safe/config-home" || len(provider.PrefixArgs) != 0 || len(provider.CredentialEnv) != 0 {
		t.Fatalf("provider = %#v", provider)
	}
	if len(decoded.Models) != 1 || decoded.Models[0].ID != "codex-sdk-test" || decoded.Models[0].ProviderModel != "sdk-contract-model" {
		t.Fatalf("models = %#v", decoded.Models)
	}
}

func TestDeferredScannerDrainsAndFindsSplitValueAfterLimit(t *testing.T) {
	scanner := newDeferredScanner([]string{"cross-boundary-secret"})
	first := append(bytes.Repeat([]byte{'x'}, 70<<10), []byte("cross-boundary-")...)
	if n, err := scanner.Write(first); err != nil || n != len(first) {
		t.Fatalf("first Write = %d, %v", n, err)
	}
	if n, err := scanner.Write([]byte("secret")); err != nil || n != 6 {
		t.Fatalf("second Write = %d, %v", n, err)
	}
	if scanner.retained.Len() != 64<<10 {
		t.Fatalf("retained = %d", scanner.retained.Len())
	}
	if ErrorCategory(scanner.Err()) != categoryFailed {
		t.Fatalf("scanner category = %q", ErrorCategory(scanner.Err()))
	}
	if !scanner.forbidden {
		t.Fatal("scanner did not record the split forbidden value after overflow")
	}
}

func TestDeferredScannerFindsSplitValueWithoutOverflow(t *testing.T) {
	scanner := newDeferredScanner([]string{"cross-boundary-secret"})
	_, _ = scanner.Write([]byte("cross-boundary-"))
	_, _ = scanner.Write([]byte("secret"))
	if !scanner.forbidden || scanner.overflow {
		t.Fatalf("forbidden=%t overflow=%t", scanner.forbidden, scanner.overflow)
	}
	if ErrorCategory(scanner.Err()) != categoryFailed {
		t.Fatalf("scanner category = %q", ErrorCategory(scanner.Err()))
	}
}

func TestDeferredScannerExactLimitIsNotOverflow(t *testing.T) {
	scanner := newDeferredScanner(nil)
	payload := bytes.Repeat([]byte{'x'}, 64<<10)
	if n, err := scanner.Write(payload); err != nil || n != len(payload) {
		t.Fatalf("Write = %d, %v", n, err)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("Err() = %v", err)
	}
	if n, err := scanner.Write([]byte{'x'}); err != nil || n != 1 {
		t.Fatalf("overflow Write = %d, %v", n, err)
	}
	if ErrorCategory(scanner.Err()) != categoryFailed {
		t.Fatalf("overflow category = %q", ErrorCategory(scanner.Err()))
	}
	if !scanner.overflow || scanner.forbidden {
		t.Fatalf("overflow=%t forbidden=%t", scanner.overflow, scanner.forbidden)
	}
}

func TestOwnedRootReplacementIsPreserved(t *testing.T) {
	parent := trustedSiblingFixture(t)
	root, err := createOwnedRoot(parent, ".sdk-contract-")
	if err != nil {
		t.Fatalf("createOwnedRoot() error = %v", err)
	}
	original := root.Path()
	moved := original + ".moved"
	if err := os.Rename(original, moved); err != nil {
		t.Fatalf("rename original root: %v", err)
	}
	if err := os.Mkdir(original, 0o700); err != nil {
		t.Fatalf("create replacement: %v", err)
	}
	if err := root.RemoveExact(); err == nil {
		t.Fatal("RemoveExact(replacement) error = nil")
	}
	if _, err := os.Stat(original); err != nil {
		t.Fatalf("replacement was removed: %v", err)
	}
	if err := os.Remove(original); err != nil {
		t.Fatalf("remove replacement: %v", err)
	}
	if err := os.RemoveAll(moved); err != nil {
		t.Fatalf("remove moved original: %v", err)
	}
}

func TestOwnedRootOpenRollbackDoesNotCertifyFailedRemoval(t *testing.T) {
	parent := trustedSiblingFixture(t)
	var created string
	root, err := createOwnedRootWithOperations(
		parent,
		".sdk-contract-",
		func(parent, pattern string) (string, error) {
			path, mkdirErr := os.MkdirTemp(parent, pattern)
			created = path
			return path, mkdirErr
		},
		func(string) (*os.File, error) { return nil, os.ErrPermission },
		func(string) error { return os.ErrPermission },
	)
	if root == nil || !isCleanupSafety(err) || cleanupErrorSafe(err) {
		t.Fatalf("constructor result root=%#v err=%v safe=%t", root, err, cleanupErrorSafe(err))
	}
	if closeErr := root.Close(); closeErr != nil {
		t.Fatalf("close recovery owner: %v", closeErr)
	}
	if created == "" {
		t.Fatal("fixture directory was not created")
	}
	if removeErr := os.Remove(created); removeErr != nil {
		t.Fatalf("remove retained fixture: %v", removeErr)
	}
}

func TestSecureFileCreationDefeatsRestrictiveUmask(t *testing.T) {
	parent := trustedSiblingFixture(t)
	root, err := createOwnedRoot(parent, ".sdk-contract-")
	if err != nil {
		t.Fatalf("create root: %v", err)
	}
	defer func() { _ = root.RemoveExact() }()
	old := setProcessUmask(0o777)
	path := filepath.Join(root.Path(), "config.data")
	err = writePrivateFile(path, []byte("public fixture\n"), 0o600)
	setProcessUmask(old)
	if err != nil {
		t.Fatalf("writePrivateFile() error = %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat: %v", err)
	}
	if info.Mode().Perm() != 0o600 || !info.Mode().IsRegular() {
		t.Fatalf("mode = %v", info.Mode())
	}
}

func TestOwnedRootCreationDefeatsRestrictiveUmask(t *testing.T) {
	parent := trustedSiblingFixture(t)
	old := setProcessUmask(0o777)
	root, err := createOwnedRoot(parent, ".sdk-contract-")
	setProcessUmask(old)
	if err != nil {
		t.Fatalf("createOwnedRoot() error = %v", err)
	}
	defer func() { _ = root.RemoveExact() }()
	info, err := os.Lstat(root.Path())
	if err != nil {
		t.Fatalf("Lstat root: %v", err)
	}
	if !privateDirectory(info) {
		t.Fatalf("root mode = %v", info.Mode())
	}
}

func TestPrivateTreeCreationDefeatsRestrictiveUmask(t *testing.T) {
	parent := trustedSiblingFixture(t)
	root, err := createOwnedRoot(parent, ".sdk-contract-")
	if err != nil {
		t.Fatalf("createOwnedRoot() error = %v", err)
	}
	defer func() { _ = root.RemoveExact() }()
	target := filepath.Join(root.Path(), "one", "two")
	old := setProcessUmask(0o777)
	err = makePrivateTree(target)
	setProcessUmask(old)
	if err != nil {
		t.Fatalf("makePrivateTree() error = %v", err)
	}
	for current := target; current != root.Path(); current = filepath.Dir(current) {
		info, statErr := os.Lstat(current)
		if statErr != nil || !privateDirectory(info) {
			t.Fatalf("private directory %q info=%v err=%v", current, info, statErr)
		}
	}
}

func TestErrorCategoryNeverExposesUnderlyingText(t *testing.T) {
	if ErrorCategory(nil) != "" {
		t.Fatal("nil has a category")
	}
	if got := ErrorCategory(os.ErrPermission); got != categoryFailed || strings.Contains(got, "permission") {
		t.Fatalf("foreign category = %q", got)
	}
}

func TestExecutableIdentityRevalidationRejectsAliasReplacement(t *testing.T) {
	root := trustedSiblingFixture(t)
	target := filepath.Join(root, "runtime-bin")
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Chmod(target, 0o700); err != nil { // #nosec G302 -- the fixture must be executable.
		t.Fatalf("chmod target: %v", err)
	}
	alias := filepath.Join(root, "runtime")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}
	identity, err := validateExecutableIdentity(alias)
	if err != nil {
		t.Fatalf("validateExecutableIdentity() error = %v", err)
	}
	if err := revalidateExecutableIdentity(identity); err != nil {
		t.Fatalf("initial revalidation: %v", err)
	}
	if err := os.Remove(alias); err != nil {
		t.Fatalf("remove alias: %v", err)
	}
	other := filepath.Join(root, "other-bin")
	if err := os.WriteFile(other, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("write other: %v", err)
	}
	if err := os.Chmod(other, 0o700); err != nil { // #nosec G302 -- the fixture must be executable.
		t.Fatalf("chmod other: %v", err)
	}
	if err := os.Symlink(other, alias); err != nil {
		t.Fatalf("replace alias: %v", err)
	}
	if err := revalidateExecutableIdentity(identity); err == nil {
		t.Fatal("revalidateExecutableIdentity(replaced alias) error = nil")
	}
}

func TestExecutableIdentityRevalidationRejectsResolvedTargetReplacement(t *testing.T) {
	root := trustedSiblingFixture(t)
	target := filepath.Join(root, "runtime-bin")
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Chmod(target, 0o700); err != nil { // #nosec G302 -- the fixture must be executable.
		t.Fatalf("chmod target: %v", err)
	}
	alias := filepath.Join(root, "runtime")
	if err := os.Symlink(target, alias); err != nil {
		t.Fatalf("symlink alias: %v", err)
	}
	identity, err := validateExecutableIdentity(alias)
	if err != nil {
		t.Fatalf("validateExecutableIdentity() error = %v", err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatalf("remove target: %v", err)
	}
	if err := os.WriteFile(target, []byte("replacement"), 0o600); err != nil {
		t.Fatalf("replace target: %v", err)
	}
	if err := os.Chmod(target, 0o700); err != nil { // #nosec G302 -- the replacement must remain executable.
		t.Fatalf("chmod replacement: %v", err)
	}
	if err := revalidateExecutableIdentity(identity); err == nil {
		t.Fatal("revalidateExecutableIdentity(replaced target) error = nil")
	}
}

func TestExecutableIdentityRevalidationRejectsSameInodeContentReplacement(t *testing.T) {
	root := trustedSiblingFixture(t)
	target := filepath.Join(root, "runtime-bin")
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Chmod(target, 0o700); err != nil { // #nosec G302 -- the fixture must be executable.
		t.Fatalf("chmod target: %v", err)
	}
	identity, err := validateExecutableIdentity(target)
	if err != nil {
		t.Fatalf("validateExecutableIdentity() error = %v", err)
	}
	original, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("Lstat original: %v", err)
	}
	if err := os.WriteFile(target, []byte("changed"), 0o600); err != nil {
		t.Fatalf("overwrite target: %v", err)
	}
	if err := os.Chtimes(target, original.ModTime(), original.ModTime()); err != nil {
		t.Fatalf("restore target times: %v", err)
	}
	replacement, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("Lstat replacement: %v", err)
	}
	if !os.SameFile(original, replacement) || original.Size() != replacement.Size() || original.Mode() != replacement.Mode() || !original.ModTime().Equal(replacement.ModTime()) {
		t.Fatalf("fixture metadata changed: original=%#v replacement=%#v", original, replacement)
	}
	if err := revalidateExecutableIdentity(identity); err == nil {
		t.Fatal("revalidateExecutableIdentity(same-inode content replacement) error = nil")
	}
}

func TestJavaScriptIdentityRejectsLeafReplacement(t *testing.T) {
	root := trustedSiblingFixture(t)
	path := filepath.Join(root, "main.mjs")
	if err := os.WriteFile(path, []byte("export {};\n"), 0o600); err != nil {
		t.Fatalf("write entrypoint: %v", err)
	}
	identity, err := validateJavaScriptIdentity(path)
	if err != nil {
		t.Fatalf("validateJavaScriptIdentity() error = %v", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove entrypoint: %v", err)
	}
	if err := os.WriteFile(path, []byte("throw 1;\n"), 0o600); err != nil {
		t.Fatalf("replace entrypoint: %v", err)
	}
	if err := revalidateJavaScriptIdentity(identity); err == nil {
		t.Fatal("revalidateJavaScriptIdentity(replacement) error = nil")
	}
}

func TestUnixChildEscalatesIgnoreTERMAndJoinsWait(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	child, err := startUnixChild(executable, "", []string{"-test.run=TestSDKContractChildHelperProcess"}, []string{"SDK_CONTRACT_CHILD_HELPER=ignore-term"}, io.Discard)
	if err != nil {
		t.Fatalf("startUnixChild: %v", err)
	}
	result := child.StopAndWait(50 * time.Millisecond)
	if result.Err != nil || !result.SafeToRemove {
		t.Fatalf("StopAndWait = %#v", result)
	}
	select {
	case <-child.Exited():
	default:
		t.Fatal("Exited is not closed after StopAndWait")
	}
	second := child.StopAndWait(time.Nanosecond)
	if ErrorCategory(second.Err) != ErrorCategory(result.Err) || second.SafeToRemove != result.SafeToRemove {
		t.Fatalf("idempotent result = %#v, want %#v", second, result)
	}
}

func TestUnixChildStopsInheritedIgnoreTERMDescendantTree(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	output := &boundedBuffer{limit: 128}
	started, err := startUnixChild(executable, "", []string{"-test.run=TestSDKContractChildHelperProcess"},
		[]string{"SDK_CONTRACT_CHILD_HELPER=tree"}, output)
	if err != nil {
		t.Fatalf("startUnixChild: %v", err)
	}
	concrete := started.(*unixChild)
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	for !bytes.Contains(output.Bytes(), []byte("descendant_ready\n")) {
		select {
		case <-deadline.C:
			_ = concrete.StopAndWait(50 * time.Millisecond)
			t.Fatal("descendant did not report readiness")
		case <-time.After(time.Millisecond):
		}
	}
	result := concrete.StopAndWait(50 * time.Millisecond)
	if result.Err != nil || !result.SafeToRemove || !concrete.killSent {
		t.Fatalf("StopAndWait = %#v killSent=%t", result, concrete.killSent)
	}
}

func TestSDKContractChildHelperProcess(_ *testing.T) {
	switch os.Getenv("SDK_CONTRACT_CHILD_HELPER") {
	case "ignore-term":
		signal.Ignore(syscall.SIGTERM)
		select {}
	case "tree":
		term := make(chan os.Signal, 1)
		signal.Notify(term, syscall.SIGTERM)
		defer signal.Stop(term)
		executable, err := os.Executable()
		if err != nil {
			os.Exit(81)
		}
		reader, writer, err := os.Pipe()
		if err != nil {
			os.Exit(82)
		}
		command := exec.CommandContext(context.Background(), executable, "-test.run=TestSDKContractTreeDescendantHelperProcess") //nolint:gosec // current test executable and fixed argv.
		command.Env = []string{"SDK_CONTRACT_TREE_DESCENDANT=1"}
		command.Stdout = io.Discard
		command.Stderr = io.Discard
		command.ExtraFiles = []*os.File{writer}
		if err := command.Start(); err != nil {
			os.Exit(83)
		}
		_ = writer.Close()
		var ready [1]byte
		if _, err := io.ReadFull(reader, ready[:]); err != nil || ready[0] != 1 {
			_ = command.Process.Kill()
			_ = command.Wait()
			os.Exit(84)
		}
		_ = reader.Close()
		_, _ = io.WriteString(os.Stdout, "descendant_ready\n")
		for {
			<-term
		}
	default:
		return
	}
}

func TestSDKContractTreeDescendantHelperProcess(_ *testing.T) {
	if os.Getenv("SDK_CONTRACT_TREE_DESCENDANT") != "1" {
		return
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	defer signal.Stop(term)
	ready := os.NewFile(uintptr(3), "tree-ready")
	if ready == nil {
		os.Exit(85)
	}
	_, err := ready.Write([]byte{1})
	_ = ready.Close()
	if err != nil {
		os.Exit(86)
	}
	for {
		<-term
	}
}

func TestRunGroupCommandPreCanceledContextReturnsCanceledWithoutCleanupFailure(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = runGroupCommand(ctx, executable, "", []string{"-test.run=TestSDKContractChildHelperProcess"}, []string{"SDK_CONTRACT_CHILD_HELPER=ignore-term"}, 20*time.Millisecond, 32, 32)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runGroupCommand canceled error = %v", err)
	}
}

func TestRunGroupCommandPreservesExplicitEmptyEnvironment(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	t.Setenv("SDK_CONTRACT_INHERITED_SENTINEL", "must-not-be-inherited")
	_, err = runGroupCommand(context.Background(), executable, "",
		[]string{"-test.run=TestSDKContractEmptyEnvironmentHelperProcess"}, []string{}, 50*time.Millisecond, 32, 32)
	if err != nil {
		t.Fatalf("runGroupCommand with explicit empty environment: %v", err)
	}
}

func TestRealHelperBoundariesCancelInheritedDescendantTrees(t *testing.T) {
	repository := moduleRootForUnitTest(t)
	root := trustedSiblingFixture(t)
	goExecutable, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("look up go: %v", err)
	}
	helper := filepath.Join(root, "process-tree-helper")
	helperTest := filepath.Join(root, "process-tree-helper.test")
	if _, err := runGroupCommand(context.Background(), goExecutable, repository,
		[]string{"test", "-c", "-trimpath", "-o", helperTest, "./internal/sdkcontract/testdata/process-tree-helper"},
		minimalBuildEnvironment(), time.Second, 0, 8<<10); err != nil {
		t.Fatalf("compile process-tree helper tests: %v", err)
	}
	if _, err := runGroupCommand(context.Background(), helperTest, "",
		[]string{"-test.run=^TestPublishReadinessStagesCompletePrivateRecordBeforeRename$", "-test.count=1"},
		[]string{}, 50*time.Millisecond, 8<<10, 8<<10); err != nil {
		t.Fatalf("run process-tree helper tests: %v", err)
	}
	if _, err := runGroupCommand(context.Background(), goExecutable, repository,
		[]string{"build", "-trimpath", "-o", helper, "./internal/sdkcontract/testdata/process-tree-helper"},
		minimalBuildEnvironment(), time.Second, 0, 8<<10); err != nil {
		t.Fatalf("build process-tree helper: %v", err)
	}
	python := filepath.Join(root, "python")
	node := filepath.Join(root, "node")
	goAlias := filepath.Join(root, "go")
	for _, alias := range []string{python, node, goAlias} {
		if err := os.Symlink(helper, alias); err != nil {
			t.Fatalf("create helper alias %q: %v", alias, err)
		}
	}
	javascript := filepath.Join(root, "main.mjs")
	if err := os.WriteFile(javascript, []byte("export {};\n"), 0o600); err != nil {
		t.Fatalf("write JavaScript fixture: %v", err)
	}
	sys := &realSystem{}
	options := Options{
		RepositoryRoot: repository, PythonExecutable: python,
		NodeExecutable: node, JavaScriptEntrypoint: javascript,
	}
	if err := sys.ValidateOptions(options); err != nil {
		t.Fatalf("ValidateOptions: %v", err)
	}
	t.Setenv("PATH", root)
	readyPath := filepath.Join(root, "process-tree.ready")
	cases := []struct {
		name string
		run  func(context.Context) error
	}{
		{name: "Build", run: func(ctx context.Context) error {
			return sys.Build(ctx, repository, filepath.Join(root, "gateway-output"), "./cmd/ai-cli-gateway", 50*time.Millisecond)
		}},
		{name: "AllocatePort", run: func(ctx context.Context) error {
			_, allocateErr := sys.AllocatePort(ctx, python, 50*time.Millisecond)
			return allocateErr
		}},
		{name: "Python", run: func(ctx context.Context) error {
			_, clientErr := sys.RunClient(ctx, python,
				[]string{"-I", filepath.Join(repository, "examples/openai-sdk/python/main.py")}, []string{}, 50*time.Millisecond)
			return clientErr
		}},
		{name: "Node", run: func(ctx context.Context) error {
			_, clientErr := sys.RunClient(ctx, node, []string{javascript}, []string{}, 50*time.Millisecond)
			return clientErr
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			if err := os.Remove(readyPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("remove prior readiness: %v", err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			result := make(chan error, 1)
			done := make(chan struct{})
			go func() {
				defer close(done)
				result <- test.run(ctx)
			}()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(2 * time.Second):
					t.Error("boundary cleanup did not join helper")
				}
			})
			providerPID, descendantPID, pgid := awaitProcessTreeReady(t, readyPath)
			cancel()
			select {
			case runErr := <-result:
				if !errors.Is(runErr, context.Canceled) {
					t.Fatalf("boundary error = %v", runErr)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("boundary did not return after cancellation")
			}
			if err := unix.Kill(-pgid, 0); !errors.Is(err, unix.ESRCH) {
				t.Fatalf("process group remains: %v", err)
			}
			for _, pid := range []int{providerPID, descendantPID} {
				if err := unix.Kill(pid, 0); !errors.Is(err, unix.ESRCH) {
					t.Fatalf("process %d remains: %v", pid, err)
				}
			}
		})
	}
}

func awaitProcessTreeReady(t *testing.T, path string) (int, int, int) {
	t.Helper()
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-deadline.C:
			t.Fatal("process tree did not report readiness")
		case <-ticker.C:
			// #nosec G304 -- path is the fixed readiness file adjacent to the test-owned helper.
			data, err := os.ReadFile(path)
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				t.Fatalf("read process-tree readiness: %v", err)
			}
			var providerPID, descendantPID, pgid int
			if n, scanErr := fmt.Sscanf(string(data), "%d %d %d\n", &providerPID, &descendantPID, &pgid); scanErr != nil || n != 3 {
				t.Fatalf("parse process-tree readiness %q: %v", data, scanErr)
			}
			return providerPID, descendantPID, pgid
		}
	}
}

func TestCleanupStartedChildFailureUsesVerifiedFinalAbsence(t *testing.T) {
	for _, test := range []struct {
		name     string
		cleanup  cleanupResult
		wantSafe bool
	}{
		{name: "clean absence", cleanup: cleanupResult{SafeToRemove: true}, wantSafe: true},
		{name: "absence with cleanup error", cleanup: cleanupResult{SafeToRemove: true, Err: newError(categoryCleanup)}, wantSafe: true},
		{name: "unverified", cleanup: cleanupResult{SafeToRemove: false, Err: newError(categoryCleanup)}},
		{name: "invalid false nil", cleanup: cleanupResult{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			child := &fakeChild{exited: make(chan struct{}), result: test.cleanup}
			err := cleanupStartedChildFailure(child, newCleanupError(false), time.Millisecond)
			if !isCleanupSafety(err) || cleanupErrorSafe(err) != test.wantSafe || child.stops != 1 {
				t.Fatalf("error=%v safe=%t stops=%d", err, cleanupErrorSafe(err), child.stops)
			}
		})
	}
}

func TestSDKContractEmptyEnvironmentHelperProcess(t *testing.T) {
	if value := os.Getenv("SDK_CONTRACT_INHERITED_SENTINEL"); value != "" {
		t.Fatalf("inherited host sentinel %q", value)
	}
}

func TestRunSDKClientCommandRejectsAnyStderr(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	output, err := runSDKClientCommand(context.Background(), executable, "",
		[]string{"-test.run=TestSDKContractNoisyClientHelperProcess"},
		[]string{"SDK_CONTRACT_NOISY_CLIENT=1"}, 50*time.Millisecond)
	if ErrorCategory(err) != categoryFailed || len(output) != 0 {
		t.Fatalf("runSDKClientCommand = %q, %v", output, err)
	}
}

func TestSDKContractNoisyClientHelperRequiresExplicitOptIn(t *testing.T) {
	executable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}
	command := exec.CommandContext( //nolint:gosec // current test executable and fixed argv.
		context.Background(),
		executable,
		"-test.run=^TestSDKContractNoisyClientHelperProcess$",
	)
	command.Env = []string{}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("run unarmed noisy helper: %v", err)
	}
	if bytes.Contains(output, []byte("python_sdk_contract_ok")) ||
		bytes.Contains(output, []byte("sensitive debug output")) {
		t.Fatalf("unarmed noisy helper emitted guarded payload: %q", output)
	}
}

func TestSDKContractNoisyClientHelperProcess(_ *testing.T) {
	if os.Getenv("SDK_CONTRACT_NOISY_CLIENT") != "1" {
		return
	}
	_, _ = io.WriteString(os.Stdout, "python_sdk_contract_ok\n")
	_, _ = io.WriteString(os.Stderr, "sensitive debug output")
}

func TestParseAllocatedPortUsesClosedDecimalGrammar(t *testing.T) {
	for _, test := range []struct {
		input string
		want  uint16
		valid bool
	}{
		{"1\n", 1, true}, {"65535\n", 65535, true},
		{"0\n", 0, false}, {"65536\n", 0, false}, {"01\n", 0, false},
		{"1", 0, false}, {"1\n2\n", 0, false}, {" 1\n", 0, false},
	} {
		got, err := parseAllocatedPort([]byte(test.input))
		if test.valid && (err != nil || got != test.want) {
			t.Fatalf("parse %q = %d, %v", test.input, got, err)
		}
		if !test.valid && err == nil {
			t.Fatalf("parse %q accepted %d", test.input, got)
		}
	}
}

func TestProbeModelsUsesNoProxyAndBearerOnlyWhenSupplied(t *testing.T) {
	requests := make(chan string, 2)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests <- request.Header.Get("Authorization")
		writer.WriteHeader(http.StatusUnauthorized)
	}))
	defer server.Close()
	sys := &realSystem{}
	for _, key := range []string{"", "fixed-key"} {
		status, err := sys.ProbeModels(context.Background(), server.URL, key)
		if err != nil || status != http.StatusUnauthorized {
			t.Fatalf("ProbeModels = %d, %v", status, err)
		}
	}
	if got := <-requests; got != "" {
		t.Fatalf("missing-key Authorization = %q", got)
	}
	if got := <-requests; got != "Bearer fixed-key" {
		t.Fatalf("key Authorization = %q", got)
	}
}

func TestResolveBuildToolDoesNotApplyProviderAliasAuthorityPolicy(t *testing.T) {
	root := trustedSiblingFixture(t)
	if err := os.Chmod(root, 0o770); err != nil { // #nosec G302 -- group-writable ancestor is the regression condition.
		t.Fatalf("chmod fixture root: %v", err)
	}
	tool := filepath.Join(root, "go")
	if err := os.WriteFile(tool, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write build tool: %v", err)
	}
	for _, mode := range []os.FileMode{0o755, 0o777} {
		if err := os.Chmod(tool, mode); err != nil { // #nosec G302 -- caller-authorized host toolchains can use either mode.
			t.Fatalf("chmod build tool to %04o: %v", mode, err)
		}
		got, err := resolveBuildTool(func(name string) (string, error) {
			if name != "go" {
				t.Fatalf("lookup name = %q", name)
			}
			return tool, nil
		})
		if err != nil || got != tool {
			t.Fatalf("resolveBuildTool(mode %04o) = %q, %v", mode, got, err)
		}
	}
}

func TestValidBuildToolInfoRejectsNonExecutableOrSpecialBitLeaf(t *testing.T) {
	for _, test := range []struct {
		name  string
		mode  os.FileMode
		valid bool
	}{
		{name: "ordinary executable", mode: 0o755, valid: true},
		{name: "hosted writable executable", mode: 0o777, valid: true},
		{name: "not executable", mode: 0o600},
		{name: "directory", mode: os.ModeDir | 0o755},
		{name: "symlink", mode: os.ModeSymlink | 0o777},
		{name: "setuid", mode: 0o755 | os.ModeSetuid},
		{name: "setgid", mode: 0o755 | os.ModeSetgid},
		{name: "sticky", mode: 0o755 | os.ModeSticky},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := validBuildToolInfo(buildToolFileInfo{mode: test.mode}); got != test.valid {
				t.Fatalf("validBuildToolInfo(mode %v) = %t, want %t", test.mode, got, test.valid)
			}
		})
	}
	if validBuildToolInfo(nil) {
		t.Fatal("validBuildToolInfo(nil) = true")
	}
}

type buildToolFileInfo struct{ mode os.FileMode }

func (info buildToolFileInfo) Name() string       { return "go" }
func (info buildToolFileInfo) Size() int64        { return 1 }
func (info buildToolFileInfo) Mode() os.FileMode  { return info.mode }
func (info buildToolFileInfo) ModTime() time.Time { return time.Time{} }
func (info buildToolFileInfo) IsDir() bool        { return info.mode.IsDir() }
func (info buildToolFileInfo) Sys() any           { return nil }

func TestRealSystemBuildNormalizesPermissiveUmask(t *testing.T) {
	repository := moduleRootForUnitTest(t)
	repositoryInfo, moduleInfo, err := validateRepository(repository)
	if err != nil {
		t.Fatalf("validate repository: %v", err)
	}
	sys := &realSystem{
		options:    Options{RepositoryRoot: repository},
		repository: repositoryInfo,
		module:     moduleInfo,
		validated:  true,
	}
	root := trustedSiblingFixture(t)
	for attempt := 1; attempt <= 2; attempt++ {
		output := filepath.Join(root, fmt.Sprintf("fake-codex-cli-%d", attempt))
		err = func() error {
			previousUmask := setProcessUmask(0o002)
			defer setProcessUmask(previousUmask)
			return sys.Build(
				context.Background(),
				repository,
				output,
				"./internal/testcli/cmd/fake-codex-cli",
				30*time.Second,
			)
		}()
		if err != nil {
			t.Fatalf("Build() attempt %d error = %v", attempt, err)
		}
		info, err := os.Lstat(output)
		if err != nil {
			t.Fatalf("inspect build output %d: %v", attempt, err)
		}
		if !info.Mode().IsRegular() || info.Mode().Perm() != 0o700 ||
			info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
			t.Fatalf("build output %d mode = %v", attempt, info.Mode())
		}
	}
}

func TestNormalizeBuiltExecutableRejectsSymlink(t *testing.T) {
	root := trustedSiblingFixture(t)
	target := filepath.Join(root, "target")
	if err := os.WriteFile(target, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	if err := os.Chmod(target, 0o700); err != nil { // #nosec G302 -- executable fixture mode is intentional.
		t.Fatalf("chmod target: %v", err)
	}
	path := filepath.Join(root, "build-output")
	if err := os.Symlink(target, path); err != nil {
		t.Fatalf("create build-output symlink: %v", err)
	}

	if err := normalizeBuiltExecutable(path); ErrorCategory(err) != categoryFailed {
		t.Fatalf("normalizeBuiltExecutable(symlink) error = %v", err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatalf("inspect target: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("symlink target mode changed to %v", info.Mode())
	}
}

func TestNormalizeBuiltExecutableRejectsIdentityReplacement(t *testing.T) {
	root := trustedSiblingFixture(t)
	path := filepath.Join(root, "build-output")
	replacement := filepath.Join(root, "replacement")
	for _, candidate := range []string{path, replacement} {
		if err := os.WriteFile(candidate, []byte("fixture"), 0o600); err != nil {
			t.Fatalf("write %s: %v", filepath.Base(candidate), err)
		}
		if err := os.Chmod(candidate, 0o775); err != nil { // #nosec G302 -- permissive mode is the regression condition.
			t.Fatalf("chmod %s: %v", filepath.Base(candidate), err)
		}
	}
	openReplacingPath := func(candidate string) (builtExecutableHandle, error) {
		file, err := os.Open(candidate) //nolint:gosec // test-owned fixed path.
		if err != nil {
			return nil, err
		}
		if err := os.Rename(replacement, candidate); err != nil {
			_ = file.Close()
			return nil, err
		}
		return file, nil
	}

	err := normalizeBuiltExecutableWith(path, os.Lstat, openReplacingPath)
	if ErrorCategory(err) != categoryFailed {
		t.Fatalf("normalizeBuiltExecutableWith(replacement) error = %v", err)
	}
	info, statErr := os.Lstat(path)
	if statErr != nil {
		t.Fatalf("inspect replacement: %v", statErr)
	}
	if info.Mode().Perm() != 0o775 {
		t.Fatalf("replacement mode changed to %v", info.Mode())
	}
}

func TestNormalizeBuiltExecutableFailsClosedOnCloseError(t *testing.T) {
	root := trustedSiblingFixture(t)
	path := filepath.Join(root, "build-output")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("write build output: %v", err)
	}
	if err := os.Chmod(path, 0o775); err != nil { // #nosec G302 -- permissive mode is the regression condition.
		t.Fatalf("chmod build output: %v", err)
	}
	openWithCloseError := func(candidate string) (builtExecutableHandle, error) {
		file, err := os.Open(candidate) //nolint:gosec // test-owned fixed path.
		if err != nil {
			return nil, err
		}
		return &closeErrorBuiltExecutable{File: file}, nil
	}

	err := normalizeBuiltExecutableWith(path, os.Lstat, openWithCloseError)
	if ErrorCategory(err) != categoryFailed {
		t.Fatalf("normalizeBuiltExecutableWith(close error) error = %v", err)
	}
}

type closeErrorBuiltExecutable struct{ *os.File }

func (f *closeErrorBuiltExecutable) Close() error {
	_ = f.File.Close()
	return os.ErrPermission
}

func trustedSiblingFixture(t *testing.T) string {
	t.Helper()
	parent, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatalf("resolve temporary parent: %v", err)
	}
	fixture, err := os.MkdirTemp(parent, ".sdkcontract-test-")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	if err := os.Chmod(fixture, 0o700); err != nil { // #nosec G302 -- the fixture must be a private directory.
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(fixture) })
	return fixture
}

func moduleRootForUnitTest(t *testing.T) string {
	t.Helper()
	root, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			return root
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatal("module root not found")
		}
		root = parent
	}
}
