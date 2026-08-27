//go:build integration

package cli

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

const initCommandTimeout = 20 * time.Second

func TestCommandInitFreshDefaultDoctorNoopAndNamedMerge(t *testing.T) {
	gateway := testutil.BuildGateway(t)
	fixture := newCommandInitFixture(t)
	fakeCodex := buildInitCodexFake(t, filepath.Join(fixture.root, "provider-bin"))
	codexHome := privateInitDirectory(t, filepath.Join(fixture.root, "codex-home"))
	args := codexCommandInitArgs(fakeCodex, codexHome)

	first := runInitCommandProcess(t, gateway, args, fixture.environment)
	if first.code != 0 || !strings.Contains(first.stdout, "saved_config:") ||
		!strings.Contains(first.stdout, "gateway_key_file:") ||
		!strings.Contains(first.stdout, "client_key_posix:") ||
		!strings.Contains(first.stdout, "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}") ||
		!strings.Contains(first.stdout, "core:\n") ||
		!strings.HasSuffix(first.stdout, "setup_ready\n") || first.stderr != "" {
		t.Fatalf("fresh init = code %d stdout %q stderr %q", first.code, first.stdout, first.stderr)
	}
	configPath := fixture.configPath
	keyPath := filepath.Join(filepath.Dir(configPath), "gateway.key")
	configBefore := statInitPath(t, configPath)
	keyBefore := statInitPath(t, keyPath)
	keyBytes, err := os.ReadFile(keyPath) // #nosec G304 -- exact test-owned generated key path.
	if err != nil {
		t.Fatalf("ReadFile generated key: %v", err)
	}
	keyValue := strings.TrimSuffix(string(keyBytes), "\n")
	if len(keyValue) != 64 || strings.Contains(first.stdout+first.stderr, keyValue) {
		t.Fatal("fresh init exposed or malformed the generated Gateway key")
	}
	if _, err := os.Lstat(filepath.Join(codexHome, ".inference-called")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("init invoked provider inference")
	}

	doctor := runInitCommandProcess(t, gateway, []string{"doctor"}, fixture.environment)
	if doctor.code != 0 || !strings.HasPrefix(doctor.stdout, "core:\n") || doctor.stderr != "" {
		t.Fatalf("default doctor = code %d stdout %q stderr %q", doctor.code, doctor.stdout, doctor.stderr)
	}

	second := runInitCommandProcess(t, gateway, args, fixture.environment)
	configAfter := statInitPath(t, configPath)
	keyAfter := statInitPath(t, keyPath)
	if second.code != 0 || !strings.Contains(second.stdout, "already_current:") ||
		!strings.HasSuffix(second.stdout, "setup_ready\n") || second.stderr != "" {
		t.Fatalf("no-op init = code %d stdout %q stderr %q", second.code, second.stdout, second.stderr)
	}
	if !os.SameFile(configBefore, configAfter) || !configBefore.ModTime().Equal(configAfter.ModTime()) ||
		!os.SameFile(keyBefore, keyAfter) || !keyBefore.ModTime().Equal(keyAfter.ModTime()) {
		t.Fatal("no-op init changed config or key identity/timestamp")
	}
	if _, err := os.Lstat(configPath + ".bak"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("no-op init backup state = %v, want absent", err)
	}

	current, err := os.ReadFile(configPath) // #nosec G304 -- exact test-owned generated config path.
	if err != nil {
		t.Fatalf("ReadFile generated config: %v", err)
	}
	withSentinel := append([]byte("# merge-sentinel\n"), current...)
	// configPath is an exact private test-owned path.
	//nolint:gosec
	if err := os.WriteFile(configPath, withSentinel, 0o600); err != nil {
		t.Fatalf("WriteFile merge sentinel: %v", err)
	}
	replacement := buildInitCodexFake(t, filepath.Join(fixture.root, "replacement-bin"))
	mergeArgs := []string{
		"init", "--non-interactive",
		"--provider", "codex",
		"--codex-executable", replacement,
		"--codex-config-home", codexHome,
		"--codex-model", "codex-local=gpt-replaced",
		"--codex-model", "codex-extra=gpt-extra",
		"--replace-provider", "codex",
		"--replace-model", "codex-local",
	}
	merged := runInitCommandProcess(t, gateway, mergeArgs, fixture.environment)
	if merged.code != 0 || !strings.Contains(merged.stdout, "backup_config:") ||
		!strings.HasSuffix(merged.stdout, "setup_ready\n") || merged.stderr != "" {
		t.Fatalf("merge init = code %d stdout %q stderr %q", merged.code, merged.stdout, merged.stderr)
	}
	mergedConfig, err := os.ReadFile(configPath) // #nosec G304 -- exact test-owned generated config path.
	if err != nil {
		t.Fatalf("ReadFile merged config: %v", err)
	}
	backup, err := os.ReadFile(configPath + ".bak") // #nosec G304 -- exact transaction-owned backup path.
	if err != nil {
		t.Fatalf("ReadFile config backup: %v", err)
	}
	decoded, err := config.Decode(bytes.NewReader(mergedConfig))
	if err != nil {
		t.Fatalf("Decode merged config: %v", err)
	}
	models := make(map[string]string, len(decoded.Models))
	for _, model := range decoded.Models {
		models[model.ID] = model.ProviderModel
	}
	if !bytes.Contains(mergedConfig, []byte("# merge-sentinel\n")) ||
		decoded.Providers["codex"].Executable != replacement ||
		models["codex-local"] != "gpt-replaced" || models["codex-extra"] != "gpt-extra" ||
		!bytes.Equal(backup, withSentinel) {
		t.Fatalf("merge/config backup contract failed\nconfig=%s\nbackup=%s", mergedConfig, backup)
	}
	keyFinal := statInitPath(t, keyPath)
	if !os.SameFile(keyBefore, keyFinal) || !keyBefore.ModTime().Equal(keyFinal.ModTime()) {
		t.Fatal("named merge rotated the existing Gateway key")
	}
	assertInitCommandRedacted(t, keyValue, first, doctor, second, merged)
}

func TestCommandInitDryRunLeavesFilesystemTreeByteForByteUnchanged(t *testing.T) {
	gateway := testutil.BuildGateway(t)
	fixture := newCommandInitFixture(t)
	fakeCodex := buildInitCodexFake(t, testutil.TrustedTempDir(t))
	missingHome := filepath.Join(fixture.root, "missing-codex-home")
	before := snapshotInitTree(t, fixture.root)
	args := append(codexCommandInitArgs(fakeCodex, missingHome), "--dry-run")

	result := runInitCommandProcess(t, gateway, args, fixture.environment)
	after := snapshotInitTree(t, fixture.root)
	if result.code != 0 ||
		!strings.HasSuffix(result.stdout, "dry_run: no files changed; post-write doctor was not run\n") ||
		result.stderr != "" || !equalInitTrees(before, after) {
		t.Fatalf("dry run = code %d stdout %q stderr %q\nbefore=%v\nafter=%v", result.code, result.stdout, result.stderr, before, after)
	}
	if _, err := os.Lstat(fixture.configPath); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dry-run config state = %v, want absent", err)
	}
}

func TestCommandInitAllProvidersAndSelectedReadiness(t *testing.T) {
	gateway := testutil.BuildGateway(t)
	fixture := newCommandInitFixture(t)
	root := fixture.root
	fake := buildInitCodexFake(t, filepath.Join(root, "provider-bin"))
	configPath := filepath.Join(root, "all-providers.toml")
	homes := map[string]string{
		"codex":  privateInitDirectory(t, filepath.Join(root, "codex-home")),
		"claude": privateInitDirectory(t, filepath.Join(root, "claude-home")),
		"gemini": privateInitDirectory(t, filepath.Join(root, "gemini-home")),
	}
	const providerSecret = "PLANTED_GEMINI_CREDENTIAL_VALUE"
	environment := replaceInitEnvironment(fixture.environment, map[string]string{
		"GEMINI_API_KEY": providerSecret,
	})
	args := []string{
		"init", "--non-interactive", "--config", configPath,
		"--provider", "codex", "--provider", "claude", "--provider", "gemini",
		"--codex-executable", fake, "--codex-config-home", homes["codex"],
		"--codex-model", "codex-fast=gpt-fast", "--codex-model", "codex-deep=gpt-deep",
		"--claude-executable", fake, "--claude-config-home", homes["claude"],
		"--claude-auth", "config-home", "--claude-model", "claude-local=sonnet",
		"--gemini-executable", fake, "--gemini-config-home", homes["gemini"],
		"--gemini-auth", "gemini-api-key", "--gemini-model", "gemini-local=gemini-test",
		"--gateway-auth", "none",
	}

	all := runInitCommandProcess(t, gateway, args, environment)
	if all.code != 1 || !strings.Contains(all.stdout, "saved_config:") ||
		!strings.Contains(all.stdout, "codex\tready") ||
		!strings.Contains(all.stdout, "claude\tnot_ready") ||
		!strings.Contains(all.stdout, "gemini\tnot_ready") ||
		strings.Contains(all.stdout, "Authorization:") ||
		strings.Contains(all.stdout, "AI_CLI_GATEWAY_API_KEY") ||
		!strings.Contains(all.stdout, "request_posix: curl --fail-with-body '") ||
		!strings.HasSuffix(all.stdout, "setup_saved_but_not_ready\n") {
		t.Fatalf("all-provider init = code %d stdout %q stderr %q", all.code, all.stdout, all.stderr)
	}
	raw, err := os.ReadFile(configPath) // #nosec G304 -- exact test-owned generated config path.
	if err != nil {
		t.Fatalf("ReadFile all-provider config: %v", err)
	}
	cfg, err := config.Decode(bytes.NewReader(raw))
	if err != nil || len(cfg.Providers) != 3 || len(cfg.Models) != 4 {
		t.Fatalf("all-provider config = providers/models %d/%d error %v", len(cfg.Providers), len(cfg.Models), err)
	}

	codexOnly := runInitCommandProcess(t, gateway, []string{
		"init", "--non-interactive", "--config", configPath, "--provider", "codex",
	}, environment)
	if codexOnly.code != 0 || !strings.Contains(codexOnly.stdout, "already_current:") ||
		!strings.Contains(codexOnly.stdout, "claude\tnot_ready") ||
		!strings.Contains(codexOnly.stdout, "gemini\tnot_ready") ||
		!strings.HasSuffix(codexOnly.stdout, "setup_ready\n") {
		t.Fatalf("selected Codex init = code %d stdout %q stderr %q", codexOnly.code, codexOnly.stdout, codexOnly.stderr)
	}
	for _, output := range []string{all.stdout, all.stderr, codexOnly.stdout, codexOnly.stderr} {
		if strings.Contains(output, providerSecret) || strings.Contains(output, "PLANTED_RAW_FAKE_OUTPUT") {
			t.Fatalf("init output exposed provider credential/raw output: %q", output)
		}
	}
	for _, home := range homes {
		if _, err := os.Lstat(filepath.Join(home, ".inference-called")); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("init invoked provider inference in %q", home)
		}
	}
}

type commandInitFixture struct {
	root        string
	configPath  string
	environment []string
}

func newCommandInitFixture(t *testing.T) commandInitFixture {
	t.Helper()
	root := testutil.TrustedTempDir(t)
	// The command fixture parent intentionally requires owner-only access.
	//nolint:gosec
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("Chmod command fixture: %v", err)
	}
	overrides := make(map[string]string)
	var configPath string
	if runtime.GOOS == "windows" {
		localAppData := filepath.Join(root, "LocalAppData")
		overrides["LOCALAPPDATA"] = localAppData
		configPath = filepath.Join(localAppData, "AI CLI Gateway", "config", "config.toml")
	} else {
		configBase := filepath.Join(root, "config-base")
		overrides["XDG_CONFIG_HOME"] = configBase
		overrides["XDG_STATE_HOME"] = filepath.Join(root, "state-base")
		overrides["HOME"] = filepath.Join(root, "home")
		configPath = filepath.Join(configBase, "ai-cli-gateway", "config.toml")
	}
	return commandInitFixture{
		root: root, configPath: configPath,
		environment: replaceInitEnvironment(os.Environ(), overrides),
	}
}

func codexCommandInitArgs(executable, configHome string) []string {
	return []string{
		"init", "--non-interactive",
		"--provider", "codex",
		"--codex-executable", executable,
		"--codex-config-home", configHome,
		"--codex-model", "codex-local=gpt-test",
	}
}

type initCommandResult struct {
	code   int
	stdout string
	stderr string
}

func runInitCommandProcess(
	t *testing.T,
	executable string,
	args []string,
	environment []string,
) initCommandResult {
	t.Helper()
	running := startInitCommandProcess(t, executable, args, environment)
	err := awaitInitCommand(t, running)
	return initCommandResult{
		code:   commandInitExitCode(err),
		stdout: running.stdout.String(),
		stderr: running.stderr.String(),
	}
}

type runningInitProcess struct {
	command     *exec.Cmd
	stdout      initCommandBuffer
	stderr      initCommandBuffer
	stdinWriter *os.File
	wait        <-chan error
	cancel      context.CancelFunc
}

type initCommandBuffer struct {
	mu         sync.Mutex
	contents   bytes.Buffer
	firstWrite chan struct{}
	wrote      sync.Once
}

func newInitCommandBuffer() initCommandBuffer {
	return initCommandBuffer{firstWrite: make(chan struct{})}
}

func (buffer *initCommandBuffer) Write(payload []byte) (int, error) {
	buffer.mu.Lock()
	written, err := buffer.contents.Write(payload)
	buffer.mu.Unlock()
	if written > 0 {
		buffer.wrote.Do(func() { close(buffer.firstWrite) })
	}
	return written, err
}

func (buffer *initCommandBuffer) String() string {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.contents.String()
}

func (buffer *initCommandBuffer) Len() int {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.contents.Len()
}

func startInitCommandProcess(
	t *testing.T,
	executable string,
	args []string,
	environment []string,
) *runningInitProcess {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), initCommandTimeout)
	// Executable and argv are exact test-owned values; no shell is involved.
	//nolint:gosec
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = slices.Clone(environment)
	stdinReader, stdinWriter, err := os.Pipe()
	if err != nil {
		cancel()
		t.Fatalf("create blocking stdin pipe: %v", err)
	}
	running := &runningInitProcess{
		command: command, stdout: newInitCommandBuffer(), stderr: newInitCommandBuffer(),
		stdinWriter: stdinWriter, cancel: cancel,
	}
	command.Stdin = stdinReader
	command.Stdout = &running.stdout
	command.Stderr = &running.stderr
	if err := command.Start(); err != nil {
		_ = stdinReader.Close()
		_ = stdinWriter.Close()
		cancel()
		t.Fatalf("start init command: %v", err)
	}
	if err := stdinReader.Close(); err != nil {
		_ = stdinWriter.Close()
		cancel()
		t.Fatalf("close parent stdin reader: %v", err)
	}
	wait := make(chan error, 1)
	running.wait = wait
	go func() { wait <- command.Wait() }()
	return running
}

func awaitInitCommand(t *testing.T, running *runningInitProcess) error {
	t.Helper()
	select {
	case err := <-running.wait:
		_ = running.stdinWriter.Close()
		running.cancel()
		return err
	case <-time.After(initCommandTimeout + time.Second):
		_ = running.stdinWriter.Close()
		running.cancel()
		t.Fatalf("init command timed out; stdout=%q stderr=%q", running.stdout.String(), running.stderr.String())
		return context.DeadlineExceeded
	}
}

func commandInitExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return -1
}

func replaceInitEnvironment(environment []string, overrides map[string]string) []string {
	result := make([]string, 0, len(environment)+len(overrides))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		replaced := false
		for override := range overrides {
			if strings.EqualFold(name, override) {
				replaced = true
				break
			}
		}
		if !replaced {
			result = append(result, entry)
		}
	}
	names := make([]string, 0, len(overrides))
	for name := range overrides {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		result = append(result, name+"="+overrides[name])
	}
	return result
}

func privateInitDirectory(t *testing.T, path string) string {
	t.Helper()
	testutil.CreateTrustedDirectory(t, path)
	return path
}

func buildInitCodexFake(t *testing.T, directory string) string {
	t.Helper()
	privateInitDirectory(t, directory)
	source := `package main
import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)
func main() {
	args := strings.Join(os.Args[1:], " ")
	home := os.Getenv("CODEX_HOME")
	switch {
	case strings.HasSuffix(args, "--version"):
		fmt.Println("codex-cli 0.146.0")
	case strings.Contains(args, "exec --help"):
		fmt.Println("PROMPT - --disable -c --strict-config --sandbox --model --output-schema --color --ephemeral --ignore-user-config --ignore-rules --skip-git-repo-check")
	case strings.HasSuffix(args, "features list"):
		for _, feature := range []string{"shell_tool", "unified_exec", "code_mode_host", "apps", "plugins", "remote_plugin", "hooks", "multi_agent", "browser_use", "browser_use_external", "computer_use", "in_app_browser", "image_generation", "skill_search", "skill_mcp_dependency_install", "workspace_dependencies"} {
			fmt.Println(feature + " stable false")
		}
	case strings.HasSuffix(args, "login status"):
		fmt.Println("Logged in")
	case strings.HasSuffix(args, "doctor --json"):
		if _, err := os.Lstat(filepath.Join(home, ".block-doctor")); err == nil {
			_ = os.WriteFile(filepath.Join(home, ".doctor-blocked"), []byte("blocked\n"), 0600)
			for { time.Sleep(100 * time.Millisecond) }
		}
		fmt.Println(` + "`" + `{"schemaVersion":1,"overallStatus":"ok","checks":{"auth.credentials":{"id":"auth.credentials","status":"ok"},"config.load":{"id":"config.load","status":"ok"}}}` + "`" + `)
	default:
		_ = os.WriteFile(filepath.Join(home, ".inference-called"), []byte("called\n"), 0600)
		fmt.Fprintln(os.Stderr, "PLANTED_RAW_FAKE_OUTPUT")
		os.Exit(2)
	}
}
`
	sourcePath := filepath.Join(directory, "fake-init-codex.go")
	// The source is an exact test-owned fixture below a private directory.
	//nolint:gosec
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatalf("WriteFile fake Codex source: %v", err)
	}
	name := "fake-init-codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(directory, name)
	ctx, cancel := context.WithTimeout(context.Background(), initCommandTimeout)
	defer cancel()
	// Fixed go/build argv with exact test-owned source and output paths.
	//nolint:gosec
	command := exec.CommandContext(ctx, "go", "build", "-o", executable, sourcePath)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("build fake Codex: %v: %s", err, output.String())
	}
	return executable
}

func statInitPath(t *testing.T, path string) fs.FileInfo {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat %q: %v", path, err)
	}
	return info
}

type initTreeEntry struct {
	mode    fs.FileMode
	content string
}

func snapshotInitTree(t *testing.T, root string) map[string]initTreeEntry {
	t.Helper()
	result := make(map[string]initTreeEntry)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := initTreeEntry{mode: info.Mode()}
		if info.Mode().IsRegular() {
			contents, err := os.ReadFile(path) //nolint:gosec // WalkDir supplied an exact regular path below the fixture.
			if err != nil {
				return err
			}
			value.content = string(contents)
		}
		result[relative] = value
		return nil
	})
	if err != nil {
		t.Fatalf("snapshot fixture tree: %v", err)
	}
	return result
}

func equalInitTrees(left, right map[string]initTreeEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for path, leftEntry := range left {
		if rightEntry, present := right[path]; !present || rightEntry != leftEntry {
			return false
		}
	}
	return true
}

func assertInitCommandRedacted(t *testing.T, key string, results ...initCommandResult) {
	t.Helper()
	for _, result := range results {
		for _, forbidden := range []string{key, "PLANTED_RAW_FAKE_OUTPUT"} {
			if forbidden != "" && strings.Contains(result.stdout+result.stderr, forbidden) {
				t.Fatalf("command output exposed %q: stdout=%q stderr=%q", forbidden, result.stdout, result.stderr)
			}
		}
	}
}
