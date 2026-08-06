//go:build integration && !windows

package sdkcontract

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"sync"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRealForcedGatewayKillRegistryCleanup(t *testing.T) {
	previousUmask := setProcessUmask(0o002)
	t.Cleanup(func() { setProcessUmask(previousUmask) })
	repository := moduleRootForIntegration(t)
	python := trustedIntegrationPython(t)
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatalf("test executable: %v", err)
	}
	options := Options{
		RepositoryRoot:       repository,
		PythonExecutable:     python,
		NodeExecutable:       privateIntegrationExecutable(t, testExecutable),
		JavaScriptEntrypoint: filepath.Join(repository, "examples/openai-sdk/javascript/main.mjs"),
	}
	sys := newForcedKillRunSystem(testExecutable)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var output bytes.Buffer
	runDone := make(chan error, 1)
	go func() {
		runDone <- runWithSystem(ctx, options, &output, sys, productionPolicy)
	}()

	const registryStartDeadline = time.Minute
	registryStartTimer := time.NewTimer(registryStartDeadline)
	defer registryStartTimer.Stop()
	var registry *forcedKillRegistry
	select {
	case registry = <-sys.registryStarted:
	case err := <-runDone:
		started, completed := sys.buildProgress()
		t.Fatalf("run ended before registry start: %v (builds started=%d completed=%d)", err, started, completed)
	case <-registryStartTimer.C:
		cancel()
		<-runDone
		t.Fatal("run did not start fixture registry")
	}
	readyTimer := time.NewTimer(productionPolicy.RegistryProtocol)
	select {
	case <-registry.ready:
		if !readyTimer.Stop() {
			<-readyTimer.C
		}
	case err := <-runDone:
		if !readyTimer.Stop() {
			<-readyTimer.C
		}
		t.Fatalf("run ended before fixture registry Ready: %v", err)
	case <-readyTimer.C:
		cancel()
		<-runDone
		t.Fatal("fixture registry did not become ready")
	}
	if err := verifyRegisteredGroupIgnoresTERM(registry.concrete); err != nil {
		cancel()
		<-runDone
		t.Fatalf("registered fixture TERM behavior: %v", err)
	}
	recorded := registry.records()

	var gateway *forcedKillChild
	select {
	case gateway = <-sys.gatewayStarted:
	default:
		cancel()
		<-runDone
		t.Fatal("gateway was not started before fixture readiness")
	}
	cancel()
	select {
	case err := <-runDone:
		if got := ErrorCategory(err); got != categoryCanceled {
			t.Fatalf("run category = %q", got)
		}
	case <-time.After(productionPolicy.GatewayGrace + productionPolicy.RegistryCleanup + time.Second):
		t.Fatal("runWithSystem finalizer did not finish")
	}
	if output.Len() != 0 {
		t.Fatalf("public output = %q", output.String())
	}
	if gateway.result.Err != nil || !gateway.result.SafeToRemove {
		t.Fatalf("gateway cleanup = %#v", gateway.result)
	}
	if !gateway.concrete.killSent {
		t.Fatal("gateway helper exited without exercising forced SIGKILL")
	}
	select {
	case <-gateway.Exited():
	default:
		t.Fatal("gateway Wait was not joined")
	}
	gateway.concrete.waitMu.Lock()
	waitErr := gateway.concrete.waitErr
	gateway.concrete.waitMu.Unlock()
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) {
		t.Fatalf("gateway Wait error = %T %v", waitErr, waitErr)
	}
	waitStatus, ok := exitErr.Sys().(syscall.WaitStatus)
	if !ok || !waitStatus.Signaled() || waitStatus.Signal() != syscall.SIGKILL {
		t.Fatalf("gateway Wait status = %#v", exitErr.Sys())
	}
	if !groupAbsent(gateway.concrete.pgid) || !errors.Is(unix.Kill(gateway.PID(), 0), unix.ESRCH) {
		t.Fatal("gateway process group or PID remains after finalizer")
	}
	if registry.result.Err != nil || !registry.result.SafeToRemove {
		t.Fatalf("registry cleanup = %#v", registry.result)
	}
	if !registryAbsent(recorded) {
		t.Fatal("registered fixture process remains after finalizer")
	}
	root := sys.rootSnapshot()
	if root == nil || root.removeErr != nil || root.removes != 1 || root.closes != 0 {
		t.Fatalf("root cleanup = %#v", root)
	}
	if got := sys.cleanupEvents(); !slices.Equal(got, []string{"gateway_stop", "registry_stop", "root_remove"}) {
		t.Fatalf("cleanup events = %#v", got)
	}
	if _, err := os.Lstat(root.Path()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("integration root remains after RemoveExact: %v", err)
	}
}

type forcedKillRunSystem struct {
	*realSystem
	testExecutable  string
	registryStarted chan *forcedKillRegistry
	gatewayStarted  chan *forcedKillChild
	mu              sync.Mutex
	slowPath        string
	root            *forcedKillRoot
	events          []string
	buildStarted    int
	buildCompleted  int
}

func newForcedKillRunSystem(testExecutable string) *forcedKillRunSystem {
	return &forcedKillRunSystem{
		realSystem:      &realSystem{},
		testExecutable:  testExecutable,
		registryStarted: make(chan *forcedKillRegistry, 1),
		gatewayStarted:  make(chan *forcedKillChild, 1),
	}
}

func (s *forcedKillRunSystem) MkdirTemp(parent, pattern string) (ownedRoot, error) {
	root, err := s.realSystem.MkdirTemp(parent, pattern)
	if root == nil {
		return nil, err
	}
	wrapped := &forcedKillRoot{ownedRoot: root, system: s}
	s.mu.Lock()
	s.root = wrapped
	s.mu.Unlock()
	return wrapped, err
}

func (s *forcedKillRunSystem) Build(ctx context.Context, repositoryRoot, output, packagePath string, grace time.Duration) error {
	s.mu.Lock()
	s.buildStarted++
	s.mu.Unlock()
	if err := s.realSystem.Build(ctx, repositoryRoot, output, packagePath, grace); err != nil {
		return err
	}
	if packagePath != "./internal/testcli/cmd/fake-codex-cli" {
		s.mu.Lock()
		s.buildCompleted++
		s.mu.Unlock()
		return nil
	}
	goExecutable, err := resolveBuildTool(exec.LookPath)
	if err != nil {
		return err
	}
	result, err := runGroupCommand(ctx, goExecutable, repositoryRoot,
		[]string{"build", "-trimpath", "-o", output, "./internal/sdkcontract/testdata/slow-codex"},
		minimalBuildEnvironment(), grace, 0, 8<<10)
	if err != nil {
		return err
	}
	if len(result.stderr) != 0 {
		return newError(categoryFailed)
	}
	if err := normalizeBuiltExecutable(output); err != nil {
		return err
	}
	if _, err := validateExecutableIdentity(output); err != nil {
		return err
	}
	s.mu.Lock()
	s.slowPath = output
	s.buildCompleted++
	s.mu.Unlock()
	return nil
}

func (s *forcedKillRunSystem) buildProgress() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buildStarted, s.buildCompleted
}

func (s *forcedKillRunSystem) StartFixtureRegistry(path string, grace time.Duration) (fixtureRegistry, error) {
	registry, err := s.realSystem.StartFixtureRegistry(path, grace)
	if err != nil || registry == nil {
		return registry, err
	}
	concrete, ok := registry.(*unixFixtureRegistry)
	if !ok {
		return registry, newCleanupError(false)
	}
	wrapped := &forcedKillRegistry{
		fixtureRegistry: registry,
		concrete:        concrete,
		ready:           registry.Ready(),
		system:          s,
	}
	s.registryStarted <- wrapped
	return wrapped, nil
}

func (s *forcedKillRunSystem) StartGateway(executable, directory string, argv, environment []string, output io.Writer) (child, error) {
	if err := s.revalidateRepository(directory); err != nil {
		return nil, err
	}
	identity, err := validateExecutableIdentity(executable)
	if err != nil || revalidateExecutableIdentity(identity) != nil {
		return nil, newError(categoryInvalid)
	}
	s.mu.Lock()
	slowPath := s.slowPath
	root := s.root
	s.mu.Unlock()
	if slowPath == "" || root == nil {
		return nil, newError(categoryFailed)
	}
	rootPath := root.Path()
	wantArgv := []string{"serve", "--config", filepath.Join(rootPath, "attempt-1/config.toml")}
	wantEnvironment := []string{
		"PATH=" + filepath.Join(rootPath, "bin"),
		"HOME=" + filepath.Join(rootPath, "home"),
	}
	if executable != filepath.Join(rootPath, "bin/ai-cli-gateway") ||
		!slices.Equal(argv, wantArgv) || len(environment) != 3 ||
		!slices.Equal(environment[:2], wantEnvironment) {
		return nil, newError(categoryInvalid)
	}
	const keyPrefix = "AI_CLI_GATEWAY_API_KEY="
	if len(environment[2]) != len(keyPrefix)+64 || environment[2][:len(keyPrefix)] != keyPrefix {
		return nil, newError(categoryInvalid)
	}
	decodedKey, err := hex.DecodeString(environment[2][len(keyPrefix):])
	if err != nil || len(decodedKey) != 32 {
		return nil, newError(categoryFailed)
	}
	started, err := startUnixCommand(s.testExecutable, directory,
		[]string{"-test.run=^TestSDKContractGatewayHelperProcess$"},
		[]string{"SDK_CONTRACT_TEST_GATEWAY=" + slowPath}, output, output)
	if started == nil {
		return nil, err
	}
	wrapped := &forcedKillChild{concrete: started, system: s}
	s.gatewayStarted <- wrapped
	return wrapped, err
}

func (s *forcedKillRunSystem) record(event string) {
	s.mu.Lock()
	s.events = append(s.events, event)
	s.mu.Unlock()
}

func (s *forcedKillRunSystem) cleanupEvents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.events...)
}

func (s *forcedKillRunSystem) rootSnapshot() *forcedKillRoot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.root
}

type forcedKillRoot struct {
	ownedRoot
	system              *forcedKillRunSystem
	removes, closes     int
	removeErr, closeErr error
}

func (r *forcedKillRoot) RemoveExact() error {
	r.removes++
	r.removeErr = r.ownedRoot.RemoveExact()
	r.system.record("root_remove")
	return r.removeErr
}

func (r *forcedKillRoot) Close() error {
	r.closes++
	r.closeErr = r.ownedRoot.Close()
	r.system.record("root_close")
	return r.closeErr
}

type forcedKillRegistry struct {
	fixtureRegistry
	concrete *unixFixtureRegistry
	ready    <-chan struct{}
	system   *forcedKillRunSystem
	result   cleanupResult
}

func (r *forcedKillRegistry) StopAndVerify(grace time.Duration) cleanupResult {
	r.result = r.fixtureRegistry.StopAndVerify(grace)
	r.system.record("registry_stop")
	return r.result
}

func (r *forcedKillRegistry) records() []registryRecord {
	r.concrete.mu.Lock()
	defer r.concrete.mu.Unlock()
	return append([]registryRecord(nil), r.concrete.records...)
}

type forcedKillChild struct {
	concrete *unixChild
	system   *forcedKillRunSystem
	result   cleanupResult
}

func (c *forcedKillChild) PID() int                { return c.concrete.PID() }
func (c *forcedKillChild) Exited() <-chan struct{} { return c.concrete.Exited() }
func (c *forcedKillChild) StopAndWait(grace time.Duration) cleanupResult {
	c.result = c.concrete.StopAndWait(grace)
	c.system.record("gateway_stop")
	return c.result
}

func privateIntegrationRoot(t *testing.T) string {
	t.Helper()
	temporaryParent, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil || !filepath.IsAbs(temporaryParent) {
		t.Fatalf("resolve temporary parent: %v", err)
	}
	if err := validateSecureAncestors(temporaryParent); err != nil {
		t.Fatalf("temporary parent is not secure: %v", err)
	}
	root, err := os.MkdirTemp(temporaryParent, "sdk-contract-integration-")
	if err != nil {
		t.Fatalf("create private runtime root: %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatalf("protect private runtime root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove private runtime root: %v", err)
		}
	})
	return root
}

func trustedIntegrationPython(t *testing.T) string {
	t.Helper()
	candidates := make([]string, 0, 3)
	if path, err := exec.LookPath("python3"); err == nil {
		candidates = append(candidates, cleanAbsolutePath(t, path))
	}
	candidates = append(candidates, "/usr/bin/python3", "/usr/local/bin/python3")
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		if _, duplicate := seen[candidate]; duplicate {
			continue
		}
		seen[candidate] = struct{}{}
		if _, err := validateExecutableIdentity(candidate); err == nil {
			return candidate
		}
	}
	t.Fatal("trusted Python runtime is not installed")
	return ""
}

func privateIntegrationExecutable(t *testing.T, source string) string {
	t.Helper()
	if !filepath.IsAbs(source) {
		t.Fatalf("test executable path is not absolute: %q", source)
	}
	root := privateIntegrationRoot(t)

	input, err := os.Open(source) //nolint:gosec // os.Executable returned this test-owned source.
	if err != nil {
		t.Fatalf("open test executable: %v", err)
	}
	defer func() { _ = input.Close() }()
	destination := filepath.Join(root, "runtime")
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700) //nolint:gosec // fixed child of the private test root.
	if err != nil {
		t.Fatalf("create private runtime identity: %v", err)
	}
	if err := output.Chmod(0o700); err != nil {
		_ = output.Close()
		t.Fatalf("protect private runtime identity: %v", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatalf("copy private runtime identity: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close private runtime identity: %v", err)
	}
	return destination
}

func cleanAbsolutePath(t *testing.T, path string) string {
	t.Helper()
	if !filepath.IsAbs(path) {
		absolute, err := filepath.Abs(path)
		if err != nil {
			t.Fatalf("make %q absolute: %v", path, err)
		}
		path = absolute
	}
	return filepath.Clean(path)
}

func verifyRegisteredGroupIgnoresTERM(registry *unixFixtureRegistry) error {
	registry.mu.Lock()
	records := append([]registryRecord(nil), registry.records...)
	registry.mu.Unlock()
	if len(records) != 2 {
		return newError(categoryFailed)
	}
	if err := unix.Kill(-records[0].pgid, unix.SIGTERM); err != nil {
		return newError(categoryFailed)
	}
	timer := time.NewTimer(20 * time.Millisecond)
	<-timer.C
	if registryAbsent(records) {
		return newError(categoryFailed)
	}
	for _, record := range records {
		if err := unix.Kill(record.pid, 0); err != nil {
			return newError(categoryFailed)
		}
	}
	return nil
}

func TestSDKContractGatewayHelperProcess(_ *testing.T) {
	slowPath := os.Getenv("SDK_CONTRACT_TEST_GATEWAY")
	if slowPath == "" {
		return
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	defer signal.Stop(term)
	cmd := exec.CommandContext(context.Background(), slowPath, slowCodexFinalArgs()...) //nolint:gosec // test-owned executable and closed argv.
	cmd.Env = []string{}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		os.Exit(91)
	}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		os.Exit(92)
	}
	_, _ = io.WriteString(stdin, "bounded fixture prompt")
	_ = stdin.Close()
	_ = cmd.Wait()
	os.Exit(93)
}

func slowCodexFinalArgs() []string {
	features := []string{"shell_tool", "unified_exec", "code_mode_host", "apps", "plugins", "remote_plugin", "hooks", "multi_agent", "browser_use", "browser_use_external", "computer_use", "in_app_browser", "image_generation", "skill_search", "skill_mcp_dependency_install", "workspace_dependencies"}
	args := []string{"--ask-for-approval", "never", "exec", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--strict-config", "--sandbox", "read-only", "--skip-git-repo-check", "--color", "never"}
	for _, feature := range features {
		args = append(args, "--disable", feature)
	}
	return append(args, "-c", `web_search="disabled"`, "--model", "sdk-contract-model", "-")
}

func moduleRootForIntegration(t *testing.T) string {
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
