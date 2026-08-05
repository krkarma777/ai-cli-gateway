//go:build integration && !windows

package sdkcontract

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestRealForcedGatewayKillRegistryCleanup(t *testing.T) {
	repository := moduleRootForIntegration(t)
	parent := trustedSiblingFixture(t)
	owned, err := createOwnedRoot(parent, ".sdk-contract-")
	if err != nil {
		t.Fatalf("create integration root: %v", err)
	}
	root := owned.Path()
	slowPath := filepath.Join(root, "slow-codex")
	goExecutable, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("look up go: %v", err)
	}
	result, err := runGroupCommand(context.Background(), goExecutable, repository,
		[]string{"build", "-trimpath", "-o", slowPath, "./internal/sdkcontract/testdata/slow-codex"},
		minimalBuildEnvironment(), productionPolicy.HelperGrace, 0, 8<<10)
	if err != nil {
		t.Fatalf("build slow fixture: %v (%d bytes)", err, len(result.stderr))
	}
	registry, err := startPlatformRegistry(filepath.Join(root, "fixture.registry"), productionPolicy.RegistryProtocol)
	if err != nil {
		t.Fatalf("start registry: %v", err)
	}
	ready := registry.Ready()
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatalf("test executable: %v", err)
	}
	gateway, err := startUnixChild(testExecutable, repository,
		[]string{"-test.run=TestSDKContractGatewayHelperProcess"},
		[]string{"SDK_CONTRACT_TEST_GATEWAY=" + slowPath}, io.Discard)
	if err != nil {
		t.Fatalf("start gateway helper: %v", err)
	}
	readyTimer := time.NewTimer(productionPolicy.RegistryProtocol)
	select {
	case <-ready:
		if !readyTimer.Stop() {
			<-readyTimer.C
		}
	case <-readyTimer.C:
		_ = gateway.StopAndWait(productionPolicy.GatewayGrace)
		_ = registry.StopAndVerify(productionPolicy.RegistryCleanup)
		t.Fatal("fixture registry did not become ready")
	}
	if err := verifyRegisteredGroupIgnoresTERM(registry.(*unixFixtureRegistry)); err != nil {
		_ = gateway.StopAndWait(productionPolicy.GatewayGrace)
		_ = registry.StopAndVerify(productionPolicy.RegistryCleanup)
		t.Fatalf("registered fixture TERM behavior: %v", err)
	}
	registry.(*unixFixtureRegistry).mu.Lock()
	recorded := append([]registryRecord(nil), registry.(*unixFixtureRegistry).records...)
	registry.(*unixFixtureRegistry).mu.Unlock()
	select {
	case <-gateway.Exited():
		t.Log("gateway helper exited before terminal stop")
	default:
	}
	gatewayResult := gateway.StopAndWait(productionPolicy.GatewayGrace)
	if gatewayResult.Err != nil || !gatewayResult.SafeToRemove {
		t.Fatalf("gateway cleanup = %#v", gatewayResult)
	}
	if !gateway.(*unixChild).killSent {
		concrete := gateway.(*unixChild)
		concrete.waitMu.Lock()
		t.Logf("gateway helper wait result before missing KILL: %v", concrete.waitErr)
		concrete.waitMu.Unlock()
		t.Fatal("gateway helper exited without exercising forced SIGKILL")
	}
	registryResult := registry.StopAndVerify(productionPolicy.RegistryCleanup)
	if registryResult.Err != nil || !registryResult.SafeToRemove {
		t.Fatalf("registry cleanup = %#v", registryResult)
	}
	if !registryAbsent(recorded) {
		t.Fatal("registered fixture process remains before root removal")
	}
	if err := owned.RemoveExact(); err != nil {
		t.Fatalf("remove exact integration root: %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("integration root remains after RemoveExact: %v", err)
	}
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

func TestSDKContractGatewayHelperProcess(t *testing.T) {
	slowPath := os.Getenv("SDK_CONTRACT_TEST_GATEWAY")
	if slowPath == "" {
		return
	}
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	defer signal.Stop(term)
	cmd := exec.Command(slowPath, slowCodexFinalArgs()...) //nolint:gosec // test-owned executable and closed argv.
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
