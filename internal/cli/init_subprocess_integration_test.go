//go:build integration

package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

const guidedInitRawProviderOutput = "PLANTED_GUIDED_" + "RAW_PROVIDER_OUTPUT_6f71"
const guidedProtectedFileReadLimit int64 = 64 * 1024

type guidedInitFixture struct {
	commandInitFixture
	gateway                 string
	providerBin             string
	providerSecrets         []string
	allowedProviderBinaries map[string]struct{}
	repositoryBaseline      guidedRepositoryBaseline
}

type guidedRepositoryBaseline struct {
	root  string
	files map[string]guidedRepositoryFileState
	paths []string
}

type guidedRepositoryFileState struct {
	info    fs.FileInfo
	mode    fs.FileMode
	size    int64
	modTime time.Time
}

type guidedRepositoryMutation struct {
	path      string
	kind      string
	protected bool
}

func TestGuidedSetupFreshCodexUsesDiscoveredValuesAndRunsDoctor(t *testing.T) {
	fixture := newGuidedInitFixture(t)
	result := runGuidedInitPTY(t, fixture, []string{"init"}, strings.Join([]string{
		"1",        // Codex only.
		"1",        // Discovered Codex executable.
		"2",        // Dedicated Codex home.
		"",         // Default codex-local alias.
		"gpt-test", // Provider model is never guessed.
		"n",        // One model.
		"",         // Default file-backed Gateway auth.
		"",         // Default Gateway key path.
		"1",        // Final confirmation.
	}, "\n")+"\n")

	if result.code != 0 ||
		!strings.Contains(result.transcript, "Select providers") ||
		!strings.Contains(result.transcript, "+ provider codex") ||
		!strings.Contains(result.transcript, "+ model codex-local") ||
		!strings.Contains(result.transcript, "codex\tready") ||
		!strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_ready\n") {
		t.Fatalf("guided Codex result code=%d transcript=%q", result.code, result.transcript)
	}
	raw, err := os.ReadFile(fixture.configPath) //nolint:gosec // Exact test-owned default path.
	if err != nil {
		t.Fatalf("ReadFile generated config: %v", err)
	}
	cfg, err := config.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Decode generated config: %v", err)
	}
	wantExecutable := filepath.Join(fixture.providerBin, guidedProviderExecutableName("codex"))
	providerConfig, exists := cfg.Providers["codex"]
	if !exists || providerConfig.Executable != wantExecutable || len(cfg.Models) != 1 ||
		cfg.Models[0].ID != "codex-local" || cfg.Models[0].ProviderModel != "gpt-test" ||
		cfg.Server.APIKeyFile != filepath.Join(filepath.Dir(fixture.configPath), "gateway.key") {
		t.Fatalf("generated config=%+v", cfg)
	}
	doctorResult := runInitCommandProcess(t, fixture.gateway, []string{"doctor"}, fixture.environment)
	if doctorResult.code != 0 || !strings.Contains(doctorResult.stdout, "codex\tready") || doctorResult.stderr != "" {
		t.Fatalf("default doctor code=%d stdout=%q stderr=%q", doctorResult.code, doctorResult.stdout, doctorResult.stderr)
	}
	reserved, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve serve listener: %v", err)
	}
	t.Cleanup(func() { _ = reserved.Close() })
	listenAddress := reserved.Addr().String()
	serveConfig := bytes.Replace(
		raw,
		[]byte("[server]\n"),
		[]byte("[server]\nlisten = \""+listenAddress+"\"\n"),
		1,
	)
	if bytes.Equal(serveConfig, raw) {
		t.Fatal("generated config did not contain the server table")
	}
	// Exact private test-owned configuration path.
	//nolint:gosec
	if err := os.WriteFile(fixture.configPath, serveConfig, 0o600); err != nil {
		t.Fatalf("WriteFile ephemeral-listener config: %v", err)
	}
	runtimeLock := filepath.Join(cfg.Runtime.Root, ".lock")
	// Remove only the closed init Doctor's test-owned persistent lock file so
	// recreation proves the default-path serve reached runtime acquisition.
	if err := os.Remove(runtimeLock); err != nil && !errors.Is(err, fs.ErrNotExist) {
		_ = reserved.Close()
		t.Fatalf("Remove prior Doctor runtime lock: %v", err)
	}
	if err := reserved.Close(); err != nil {
		t.Fatalf("close reserved serve listener: %v", err)
	}
	serving := startInitCommandProcess(t, fixture.gateway, []string{"serve"}, fixture.environment)
	waitForGuidedTCPListener(t, listenAddress)
	if err := serving.command.Process.Signal(os.Interrupt); err != nil {
		t.Fatalf("signal default serve: %v", err)
	}
	serveErr := awaitInitCommand(t, serving)
	if code := commandInitExitCode(serveErr); code != 0 || serving.stdout.Len() != 0 || serving.stderr.Len() != 0 {
		t.Fatalf("default serve code=%d stdout=%q stderr=%q", code, serving.stdout.String(), serving.stderr.String())
	}
	for _, forbidden := range append(
		[]string{guidedInitRawProviderOutput, strings.TrimSpace(string(mustReadGuidedFile(t, filepath.Join(filepath.Dir(fixture.configPath), "gateway.key"))))},
		fixture.providerSecrets...,
	) {
		if forbidden != "" && strings.Contains(doctorResult.stdout+doctorResult.stderr+serving.stdout.String()+serving.stderr.String(), forbidden) {
			t.Fatalf("doctor/serve output exposed %q", forbidden)
		}
	}
	assertGuidedInitNoLeak(t, fixture, result)
}

func TestGuidedServeReadinessRequiresTCPListenerNotRuntimeLock(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve closed readiness address: %v", err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatalf("close readiness address: %v", err)
	}
	lockPath := filepath.Join(t.TempDir(), ".lock")
	if err := os.WriteFile(lockPath, []byte("lock\n"), 0o600); err != nil {
		t.Fatalf("write misleading runtime lock: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	err = probeGuidedTCPListener(ctx, address)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("closed listener probe error=%v, want deadline", err)
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("misleading runtime lock disappeared: %v", err)
	}
}

func TestGuidedLeakScanChecksNonExecutableProviderBinArtifacts(t *testing.T) {
	root := t.TempDir()
	providerBin := filepath.Join(root, "provider-bin")
	if err := os.Mkdir(providerBin, 0o700); err != nil {
		t.Fatalf("create provider bin: %v", err)
	}
	leakPath := filepath.Join(providerBin, "argv.log")
	if err := os.WriteFile(
		leakPath,
		[]byte(guidedInitRawProviderOutput+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write provider-bin leak artifact: %v", err)
	}

	path, err := guidedInitFindProtectedFile(
		root,
		nil,
		nil,
		[]string{guidedInitRawProviderOutput},
		nil,
		nil,
	)

	if err != nil || path != leakPath {
		t.Fatalf("provider-bin leak path=%q error=%v, want exact artifact", path, err)
	}
}

func TestGuidedLeakScanRejectsOversizedProviderBinArtifact(t *testing.T) {
	root := t.TempDir()
	providerBin := filepath.Join(root, "provider-bin")
	if err := os.Mkdir(providerBin, 0o700); err != nil {
		t.Fatalf("create provider bin: %v", err)
	}
	artifact := filepath.Join(providerBin, "oversized.log")
	if err := os.WriteFile(
		artifact,
		bytes.Repeat([]byte{'x'}, int(guidedProtectedFileReadLimit)+1),
		0o600,
	); err != nil {
		t.Fatalf("write oversized provider-bin artifact: %v", err)
	}

	path, err := guidedInitFindProtectedFile(
		root,
		nil,
		nil,
		[]string{guidedInitRawProviderOutput},
		nil,
		nil,
	)

	if err != nil || path != artifact {
		t.Fatalf("oversized provider-bin path=%q error=%v, want fail-closed", path, err)
	}
}

func TestGuidedLeakScanRejectsProviderBinGatewayKeyDuplicate(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	providerBin := filepath.Join(root, "provider-bin")
	if err := os.Mkdir(providerBin, 0o700); err != nil {
		t.Fatalf("create provider bin: %v", err)
	}
	keyPayload := strings.Repeat("ab", 32) + "\n"
	owner := filepath.Join(root, "gateway.key")
	leak := filepath.Join(providerBin, "leak.key")
	for _, path := range []string{owner, leak} {
		if err := os.WriteFile(path, []byte(keyPayload), 0o600); err != nil {
			t.Fatalf("write Gateway key fixture: %v", err)
		}
	}
	fixture := guidedInitFixture{commandInitFixture: commandInitFixture{
		root: root, configPath: configPath,
	}}
	keys, keyFiles, err := guidedInitKeyMaterials(fixture)
	if err != nil {
		t.Fatalf("collect owned Gateway keys: %v", err)
	}

	path, err := guidedInitFindProtectedFile(
		root,
		nil,
		nil,
		nil,
		keys,
		keyFiles,
	)

	if err != nil || path != leak {
		t.Fatalf("duplicated Gateway key path=%q error=%v, want exact leak", path, err)
	}
}

func TestGuidedLeakScanRejectsOversizedProviderBinKeyArtifact(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.toml")
	providerBin := filepath.Join(root, "provider-bin")
	if err := os.Mkdir(providerBin, 0o700); err != nil {
		t.Fatalf("create provider bin: %v", err)
	}
	owner := filepath.Join(root, "gateway.key")
	if err := os.WriteFile(owner, []byte(strings.Repeat("cd", 32)+"\n"), 0o600); err != nil {
		t.Fatalf("write owned Gateway key: %v", err)
	}
	leak := filepath.Join(providerBin, "oversized-leak.key")
	if err := os.WriteFile(
		leak,
		bytes.Repeat([]byte{'x'}, int(guidedProtectedFileReadLimit)+1),
		0o600,
	); err != nil {
		t.Fatalf("write oversized key artifact: %v", err)
	}
	fixture := guidedInitFixture{commandInitFixture: commandInitFixture{
		root: root, configPath: configPath,
	}}
	keys, keyFiles, err := guidedInitKeyMaterials(fixture)
	if err != nil {
		t.Fatalf("collect owned Gateway keys: %v", err)
	}

	path, err := guidedInitFindProtectedFile(
		root,
		nil,
		nil,
		nil,
		keys,
		keyFiles,
	)

	if err != nil || path != leak {
		t.Fatalf("oversized key artifact path=%q error=%v, want fail-closed", path, err)
	}
}

func TestGuidedKeyMaterialsRejectsOversizedOwner(t *testing.T) {
	root := t.TempDir()
	owner := filepath.Join(root, "gateway.key")
	if err := os.WriteFile(
		owner,
		bytes.Repeat([]byte{'x'}, int(guidedProtectedFileReadLimit)+1),
		0o600,
	); err != nil {
		t.Fatalf("write oversized owned key: %v", err)
	}
	fixture := guidedInitFixture{commandInitFixture: commandInitFixture{
		root: root, configPath: filepath.Join(root, "config.toml"),
	}}

	if _, _, err := guidedInitKeyMaterials(fixture); err == nil {
		t.Fatal("oversized owned key collection succeeded, want bounded-read failure")
	}
}

func TestGuidedRepositoryAuditIgnoresPreexistingProtectedCache(t *testing.T) {
	root := t.TempDir()
	cache := filepath.Join(root, "go-cache")
	if err := os.Mkdir(cache, 0o700); err != nil {
		t.Fatalf("create preexisting repository cache: %v", err)
	}
	cacheEntry := filepath.Join(cache, "preexisting-cache-entry")
	if err := os.WriteFile(
		cacheEntry,
		[]byte(guidedInitRawProviderOutput+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write preexisting protected cache entry: %v", err)
	}
	gitDirectory := filepath.Join(root, ".git")
	if err := os.Mkdir(gitDirectory, 0o700); err != nil {
		t.Fatalf("create excluded git directory: %v", err)
	}
	baseline, err := captureGuidedRepositoryBaseline(root)
	if err != nil {
		t.Fatalf("capture repository baseline: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(gitDirectory, "new-protected-cache"),
		[]byte(guidedInitRawProviderOutput+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("write excluded git artifact: %v", err)
	}

	mutation, err := auditGuidedRepositoryChanges(
		baseline,
		[]string{guidedInitRawProviderOutput},
	)

	if err != nil || mutation.path != "" {
		t.Fatalf("unchanged baseline mutation=%+v error=%v, want ignored", mutation, err)
	}
}

func TestGuidedRepositoryAuditExcludesGitFilePath(t *testing.T) {
	root := t.TempDir()
	gitFile := filepath.Join(root, ".git")
	if err := os.WriteFile(gitFile, []byte("gitdir: baseline\n"), 0o600); err != nil {
		t.Fatalf("write baseline gitfile: %v", err)
	}
	baseline, err := captureGuidedRepositoryBaseline(root)
	if err != nil {
		t.Fatalf("capture gitfile repository baseline: %v", err)
	}
	if err := os.WriteFile(
		gitFile,
		[]byte("gitdir: changed "+guidedInitRawProviderOutput+"\n"),
		0o600,
	); err != nil {
		t.Fatalf("change excluded gitfile: %v", err)
	}

	mutation, err := auditGuidedRepositoryChanges(
		baseline,
		[]string{guidedInitRawProviderOutput},
	)

	if err != nil || mutation.path != "" {
		t.Fatalf("excluded gitfile mutation=%+v error=%v, want ignored", mutation, err)
	}
}

func TestGuidedRepositoryAuditRejectsNewChangedAndDeletedFiles(t *testing.T) {
	tests := []struct {
		name          string
		mutate        func(*testing.T, string, string) string
		wantKind      string
		wantProtected bool
	}{
		{
			name: "new protected file",
			mutate: func(t *testing.T, root string, _ string) string {
				t.Helper()
				path := filepath.Join(root, "new.log")
				if err := os.WriteFile(path, []byte(guidedInitRawProviderOutput+"\n"), 0o600); err != nil {
					t.Fatalf("write new repository file: %v", err)
				}
				return path
			},
			wantKind: "new", wantProtected: true,
		},
		{
			name: "changed protected file",
			mutate: func(t *testing.T, _ string, existing string) string {
				t.Helper()
				if err := os.WriteFile(existing, []byte(guidedInitRawProviderOutput+"\n"), 0o600); err != nil {
					t.Fatalf("change repository file: %v", err)
				}
				return existing
			},
			wantKind: "changed", wantProtected: true,
		},
		{
			name: "deleted file",
			mutate: func(t *testing.T, _ string, existing string) string {
				t.Helper()
				if err := os.Remove(existing); err != nil {
					t.Fatalf("delete repository file: %v", err)
				}
				return existing
			},
			wantKind: "deleted",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			existing := filepath.Join(root, "existing.txt")
			if err := os.WriteFile(existing, []byte("baseline\n"), 0o600); err != nil {
				t.Fatalf("write baseline repository file: %v", err)
			}
			baseline, err := captureGuidedRepositoryBaseline(root)
			if err != nil {
				t.Fatalf("capture repository baseline: %v", err)
			}
			wantPath := test.mutate(t, root, existing)

			mutation, err := auditGuidedRepositoryChanges(
				baseline,
				[]string{guidedInitRawProviderOutput},
			)

			if err != nil || mutation.path != wantPath ||
				mutation.kind != test.wantKind ||
				mutation.protected != test.wantProtected {
				t.Fatalf(
					"repository mutation=%+v error=%v, want path/kind/protected=%q/%q/%t",
					mutation,
					err,
					wantPath,
					test.wantKind,
					test.wantProtected,
				)
			}
		})
	}
}

func TestGuidedSetupCodexClaudeCanEditDiscoveredValues(t *testing.T) {
	fixture := newGuidedInitFixture(t)
	editedClaudeExecutable := filepath.Join(
		fixture.providerBin,
		guidedProviderExecutableName("fake-guided-claude-ready"),
	)
	editedClaudeHome := filepath.Join(fixture.root, "edited-claude-home")
	result := runGuidedInitPTY(t, fixture, []string{"init"}, strings.Join([]string{
		"1,2",      // Codex and Claude.
		"1",        // Discovered Codex executable.
		"2",        // Dedicated Codex home.
		"",         // Default Codex alias.
		"gpt-fast", // Codex model.
		"n",
		"2", // Enter another Claude executable path.
		editedClaudeExecutable,
		"3", // Enter another Claude config home.
		editedClaudeHome,
		"1", // ANTHROPIC_API_KEY environment auth.
		"",  // Default Claude alias.
		"sonnet",
		"n",
		"",  // Default file-backed Gateway auth.
		"",  // Default Gateway key path.
		"1", // Final confirmation.
	}, "\n")+"\n")

	if result.code != 0 || !strings.Contains(result.transcript, "claude\tready") ||
		!strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_ready\n") {
		t.Fatalf("guided Codex+Claude result code=%d transcript=%q", result.code, result.transcript)
	}
	raw, err := os.ReadFile(fixture.configPath) //nolint:gosec // Exact test-owned default path.
	if err != nil {
		t.Fatalf("ReadFile generated config: %v", err)
	}
	cfg, err := config.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Decode generated config: %v", err)
	}
	claudeConfig, exists := cfg.Providers["claude"]
	if !exists || claudeConfig.Executable != editedClaudeExecutable ||
		claudeConfig.ConfigHome != editedClaudeHome || len(cfg.Providers) != 2 || len(cfg.Models) != 2 {
		t.Fatalf("edited config=%+v", cfg)
	}
	assertGuidedInitNoLeak(t, fixture, result)
}

func TestGuidedSetupAllProvidersSupportsMultipleAliases(t *testing.T) {
	fixture := newGuidedInitFixture(t)
	result := runGuidedInitPTY(t, fixture, []string{"init"}, strings.Join([]string{
		"1,2,3",
		"1", "2", "", "gpt-fast", "y", "codex-deep", "gpt-deep", "n",
		"1", "2", "1", "", "sonnet", "y", "claude-opus", "opus", "n",
		"1", "2", "1", "", "gemini-fast", "y", "gemini-deep", "gemini-deep-model", "n",
		"", "", "1",
	}, "\n")+"\n")

	if result.code != 0 ||
		!strings.Contains(result.transcript, "codex\tready") ||
		!strings.Contains(result.transcript, "claude\tready") ||
		!strings.Contains(result.transcript, "gemini\tready") ||
		!strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_ready\n") {
		t.Fatalf("all-provider result code=%d transcript=%q", result.code, result.transcript)
	}
	raw, err := os.ReadFile(fixture.configPath) //nolint:gosec // Exact test-owned default path.
	if err != nil {
		t.Fatalf("ReadFile generated config: %v", err)
	}
	cfg, err := config.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("Decode generated config: %v", err)
	}
	models := make(map[string]string, len(cfg.Models))
	for _, model := range cfg.Models {
		models[model.ID] = model.ProviderModel
	}
	wantModels := map[string]string{
		"codex-local": "gpt-fast", "codex-deep": "gpt-deep",
		"claude-local": "sonnet", "claude-opus": "opus",
		"gemini-local": "gemini-fast", "gemini-deep": "gemini-deep-model",
	}
	if len(cfg.Providers) != 3 || len(models) != len(wantModels) {
		t.Fatalf("all-provider config providers/models=%d/%d", len(cfg.Providers), len(models))
	}
	for alias, want := range wantModels {
		if got := models[alias]; got != want {
			t.Fatalf("model %q=%q, want %q", alias, got, want)
		}
	}
	assertGuidedInitNoLeak(t, fixture, result)
}

func TestGuidedSetupNativeWindowsExplicitNodeEntrypoint(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows Node executable and entrypoint acceptance")
	}
	fixture := newGuidedInitFixture(t)
	scenarioExecutable := filepath.Join(
		fixture.providerBin,
		guidedProviderExecutableName("fake-guided-codex-ready"),
	)
	nodeExecutable := filepath.Join(fixture.providerBin, "node.exe")
	copyGuidedProviderExecutable(t, scenarioExecutable, nodeExecutable)
	fixture.allowedProviderBinaries[filepath.Clean(nodeExecutable)] = struct{}{}
	entrypoint := filepath.Join(fixture.providerBin, "fake-guided-codex-ready.js")
	testutil.WriteTrustedFile(t, entrypoint, []byte("closed fake Codex entrypoint\n"), 0o600)
	configHome := privateInitDirectory(t, filepath.Join(fixture.root, "windows-codex-home"))

	result := runInitCommandProcess(t, fixture.gateway, []string{
		"init", "--non-interactive",
		"--provider", "codex",
		"--codex-executable", nodeExecutable,
		"--codex-entrypoint", entrypoint,
		"--codex-config-home", configHome,
		"--codex-model", "codex-local=gpt-test",
	}, fixture.environment)
	if result.code != 0 ||
		!strings.Contains(result.stdout, "codex\tready") ||
		!strings.HasSuffix(result.stdout, "setup_ready\n") ||
		result.stderr != "" {
		t.Fatalf(
			"native Windows Node init code=%d stdout=%q stderr=%q",
			result.code,
			result.stdout,
			result.stderr,
		)
	}
	raw := mustReadGuidedFile(t, fixture.configPath)
	cfg, err := config.Decode(bytes.NewReader(raw))
	providerConfig := cfg.Providers["codex"]
	if err != nil || providerConfig.Executable != nodeExecutable ||
		len(providerConfig.PrefixArgs) != 1 ||
		providerConfig.PrefixArgs[0] != entrypoint {
		t.Fatalf("native Windows Node config=%+v error=%v", providerConfig, err)
	}
	doctor := runInitCommandProcess(t, fixture.gateway, []string{"doctor"}, fixture.environment)
	if doctor.code != 0 || !strings.Contains(doctor.stdout, "codex\tready") || doctor.stderr != "" {
		t.Fatalf(
			"native Windows Node doctor code=%d stdout=%q stderr=%q",
			doctor.code,
			doctor.stdout,
			doctor.stderr,
		)
	}
	assertGuidedInitNoLeak(t, fixture,
		guidedInitPTYResult{code: result.code, transcript: result.stdout + result.stderr},
		guidedInitPTYResult{code: doctor.code, transcript: doctor.stdout + doctor.stderr},
	)
}

func TestGuidedSetupCommentMergeResolvesProviderAndAliasCollisionsIndividually(t *testing.T) {
	fixture := newGuidedInitFixture(t)
	codexHome := privateInitDirectory(t, filepath.Join(fixture.root, "merge-codex-home"))
	originalExecutable := filepath.Join(fixture.providerBin, guidedProviderExecutableName("codex"))
	initial := runInitCommandProcess(t, fixture.gateway, []string{
		"init", "--non-interactive",
		"--provider", "codex",
		"--codex-executable", originalExecutable,
		"--codex-config-home", codexHome,
		"--codex-model", "a-replace=old-replace",
		"--codex-model", "z-keep=old-keep",
	}, fixture.environment)
	if initial.code != 0 {
		t.Fatalf("initial config code=%d stdout=%q stderr=%q", initial.code, initial.stdout, initial.stderr)
	}
	generated, err := os.ReadFile(fixture.configPath) //nolint:gosec // Exact test-owned default path.
	if err != nil {
		t.Fatalf("ReadFile initial config: %v", err)
	}
	commentHeavy := append([]byte("# guided-top-comment\n# keep-this-byte-for-byte\n"), generated...)
	commentHeavy = bytes.Replace(
		commentHeavy,
		[]byte("[runtime]\n"),
		[]byte("# untouched-runtime-comment\n[runtime]\n"),
		1,
	)
	// Exact private test-owned configuration path.
	//nolint:gosec
	if err := os.WriteFile(fixture.configPath, commentHeavy, 0o600); err != nil {
		t.Fatalf("WriteFile comment-heavy config: %v", err)
	}
	keyPath := filepath.Join(filepath.Dir(fixture.configPath), "gateway.key")
	keyBefore := statInitPath(t, keyPath)
	keyPayloadBefore, err := os.ReadFile(keyPath) //nolint:gosec // Exact generated key path.
	if err != nil {
		t.Fatalf("ReadFile key before merge: %v", err)
	}
	replacementExecutable := filepath.Join(
		fixture.providerBin,
		guidedProviderExecutableName("fake-guided-codex-ready"),
	)
	result := runGuidedInitPTY(t, fixture, []string{
		"init",
		"--provider", "codex",
		"--codex-executable", replacementExecutable,
		"--codex-config-home", codexHome,
		"--codex-model", "a-replace=new-replace",
		"--codex-model", "z-keep=new-ignored",
	}, strings.Join([]string{
		"1", // Explicit replacement executable.
		"1", // Explicit unchanged config home.
		"n", // Do not add another alias.
		"",  // Preserve file-backed Gateway auth.
		"",  // Preserve key path.
		"1", // Replace provider fields.
		"1", // Replace a-replace.
		"2", // Keep z-keep.
		"1", // Accept collision decisions.
		"1", // Final confirmation.
	}, "\n")+"\n")

	if result.code != 0 || !strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_ready\n") {
		t.Fatalf("collision merge code=%d transcript=%q", result.code, result.transcript)
	}
	providerPrompt := strings.Index(result.transcript, "Resolve provider codex")
	firstPreview := strings.Index(result.transcript, "~ provider codex")
	if firstPreview < 0 || providerPrompt < 0 || firstPreview > providerPrompt {
		t.Fatalf("collision preview order invalid: %q", result.transcript)
	}
	merged, err := os.ReadFile(fixture.configPath) //nolint:gosec // Exact test-owned default path.
	if err != nil {
		t.Fatalf("ReadFile merged config: %v", err)
	}
	backup, err := os.ReadFile(fixture.configPath + ".bak") //nolint:gosec // Exact transaction backup path.
	if err != nil {
		t.Fatalf("ReadFile merge backup: %v", err)
	}
	cfg, err := config.Decode(bytes.NewReader(merged))
	if err != nil {
		t.Fatalf("Decode merged config: %v", err)
	}
	models := make(map[string]string, len(cfg.Models))
	for _, model := range cfg.Models {
		models[model.ID] = model.ProviderModel
	}
	if !bytes.Contains(merged, []byte("# guided-top-comment\n# keep-this-byte-for-byte\n")) ||
		!bytes.Contains(merged, []byte("# untouched-runtime-comment\n[runtime]\n")) ||
		!bytes.Equal(backup, commentHeavy) ||
		cfg.Providers["codex"].Executable != replacementExecutable ||
		models["a-replace"] != "new-replace" || models["z-keep"] != "old-keep" {
		t.Fatalf("merge contract failed\nconfig=%s\nbackup=%s", merged, backup)
	}
	keyAfter := statInitPath(t, keyPath)
	keyPayloadAfter, err := os.ReadFile(keyPath) //nolint:gosec // Exact generated key path.
	if err != nil {
		t.Fatalf("ReadFile key after merge: %v", err)
	}
	if !os.SameFile(keyBefore, keyAfter) || !bytes.Equal(keyPayloadBefore, keyPayloadAfter) {
		t.Fatal("collision merge rotated the existing Gateway key")
	}
	assertGuidedInitNoLeak(t, fixture, result)
}

func TestGuidedSetupGatewayKeyConfirmationPaths(t *testing.T) {
	const validKey = "abababababababababababababababab" +
		"abababababababababababababababab\n"

	t.Run("explicit valid key reuse", func(t *testing.T) {
		fixture := newGuidedInitFixture(t)
		orphan := filepath.Join(privateInitDirectory(t, filepath.Join(fixture.root, "keys")), "orphan.key")
		testutil.WriteTrustedFile(t, orphan, []byte(validKey), 0o600)
		before := statInitPath(t, orphan)
		result := runGuidedInitPTY(t, fixture, []string{
			"init", "--gateway-auth", "file", "--gateway-key-file", orphan,
		}, strings.Join([]string{
			"1", "1", "2", "", "gpt-test", "n", "", "", "1",
		}, "\n")+"\n")

		if result.code != 0 || strings.Contains(result.transcript, "Reuse the existing unreferenced Gateway key") ||
			!strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_ready\n") {
			t.Fatalf("explicit reuse code=%d transcript=%q", result.code, result.transcript)
		}
		after := statInitPath(t, orphan)
		payload, err := os.ReadFile(orphan) //nolint:gosec // Exact test-owned key path.
		if err != nil || !os.SameFile(before, after) || string(payload) != validKey {
			t.Fatalf("orphan key changed: same=%t payload=%q err=%v", os.SameFile(before, after), payload, err)
		}
		raw, err := os.ReadFile(fixture.configPath) //nolint:gosec // Exact test-owned default path.
		if err != nil {
			t.Fatalf("ReadFile reused-key config: %v", err)
		}
		cfg, err := config.Decode(bytes.NewReader(raw))
		if err != nil || cfg.Server.APIKeyFile != orphan {
			t.Fatalf("reused-key config path=%q error=%v", cfg.Server.APIKeyFile, err)
		}
		assertGuidedInitNoLeak(t, fixture, result)
	})

	t.Run("default orphan requires reuse confirmation", func(t *testing.T) {
		fixture := newGuidedInitFixture(t)
		keyPath := filepath.Join(filepath.Dir(fixture.configPath), "gateway.key")
		privateInitDirectory(t, filepath.Dir(keyPath))
		testutil.WriteTrustedFile(t, keyPath, []byte(validKey), 0o600)
		before := statInitPath(t, keyPath)
		result := runGuidedInitPTY(t, fixture, []string{"init"}, strings.Join([]string{
			"1", "1", "2", "", "gpt-test", "n", "", "", "y", "1",
		}, "\n")+"\n")

		if result.code != 0 || !strings.Contains(result.transcript, "Reuse the existing unreferenced Gateway key") ||
			!strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_ready\n") {
			t.Fatalf("orphan reuse code=%d transcript=%q", result.code, result.transcript)
		}
		after := statInitPath(t, keyPath)
		payload, err := os.ReadFile(keyPath) //nolint:gosec // Exact test-owned orphan path.
		if err != nil || !os.SameFile(before, after) || string(payload) != validKey {
			t.Fatal("orphan reuse changed the key")
		}
		assertGuidedInitNoLeak(t, fixture, result)
	})

	t.Run("missing configured key creation", func(t *testing.T) {
		fixture := newGuidedInitFixture(t)
		codexHome := privateInitDirectory(t, filepath.Join(fixture.root, "configured-home"))
		initial := runInitCommandProcess(t, fixture.gateway, codexCommandInitArgs(
			filepath.Join(fixture.providerBin, guidedProviderExecutableName("codex")),
			codexHome,
		), fixture.environment)
		if initial.code != 0 {
			t.Fatalf("initial config code=%d stdout=%q stderr=%q", initial.code, initial.stdout, initial.stderr)
		}
		keyPath := filepath.Join(filepath.Dir(fixture.configPath), "gateway.key")
		// Remove only this test-owned generated key to model a configured missing key.
		if err := os.Remove(keyPath); err != nil {
			t.Fatalf("Remove generated key fixture: %v", err)
		}
		result := runGuidedInitPTY(t, fixture, []string{
			"init", "--provider", "codex",
		}, strings.Join([]string{"1", "1", "n", "", "", "y", "1"}, "\n")+"\n")

		if result.code != 0 || !strings.Contains(result.transcript, "Create the missing configured Gateway key") ||
			!strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_ready\n") {
			t.Fatalf("missing-key creation code=%d transcript=%q", result.code, result.transcript)
		}
		payload, err := os.ReadFile(keyPath) //nolint:gosec // Exact regenerated key path.
		if err != nil || len(strings.TrimSpace(string(payload))) != 64 {
			t.Fatalf("regenerated key length=%d error=%v", len(strings.TrimSpace(string(payload))), err)
		}
		assertGuidedInitNoLeak(t, fixture, result)
	})

	t.Run("decline leaves orphan and config unchanged", func(t *testing.T) {
		fixture := newGuidedInitFixture(t)
		orphan := filepath.Join(filepath.Dir(fixture.configPath), "gateway.key")
		privateInitDirectory(t, filepath.Dir(orphan))
		testutil.WriteTrustedFile(t, orphan, []byte(validKey), 0o600)
		before := statInitPath(t, orphan)
		result := runGuidedInitPTY(t, fixture, []string{"init"}, strings.Join([]string{
			"1", "1", "2", "", "gpt-test", "n", "", "", "n",
		}, "\n")+"\n")

		if result.code != 0 || !strings.Contains(result.transcript, "Reuse the existing unreferenced Gateway key") {
			t.Fatalf("orphan decline code=%d transcript=%q", result.code, result.transcript)
		}
		if _, err := os.Lstat(fixture.configPath); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("declined config state=%v, want absent", err)
		}
		after := statInitPath(t, orphan)
		payload, err := os.ReadFile(orphan) //nolint:gosec // Exact test-owned key path.
		if err != nil || !os.SameFile(before, after) || string(payload) != validKey {
			t.Fatal("decline changed orphan key")
		}
		assertGuidedInitNoLeak(t, fixture, result)
	})

	t.Run("back selects another key path", func(t *testing.T) {
		fixture := newGuidedInitFixture(t)
		orphan := filepath.Join(filepath.Dir(fixture.configPath), "gateway.key")
		privateInitDirectory(t, filepath.Dir(orphan))
		newKey := filepath.Join(filepath.Dir(orphan), "new.key")
		testutil.WriteTrustedFile(t, orphan, []byte(validKey), 0o600)
		orphanBefore := statInitPath(t, orphan)
		result := runGuidedInitPTY(t, fixture, []string{"init"}, strings.Join([]string{
			"1", "1", "2", "", "gpt-test", "n", "", "", "back", "1", newKey, "1",
		}, "\n")+"\n")

		if result.code != 0 || !strings.Contains(result.transcript, "Reuse the existing unreferenced Gateway key") ||
			!strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_ready\n") {
			t.Fatalf("key back code=%d transcript=%q", result.code, result.transcript)
		}
		orphanAfter := statInitPath(t, orphan)
		if !os.SameFile(orphanBefore, orphanAfter) {
			t.Fatal("Back changed original orphan identity")
		}
		newPayload, err := os.ReadFile(newKey) //nolint:gosec // Exact selected key path.
		if err != nil || len(strings.TrimSpace(string(newPayload))) != 64 {
			t.Fatalf("new key length=%d error=%v", len(strings.TrimSpace(string(newPayload))), err)
		}
		raw, err := os.ReadFile(fixture.configPath) //nolint:gosec // Exact test-owned default path.
		if err != nil {
			t.Fatalf("ReadFile Back config: %v", err)
		}
		cfg, err := config.Decode(bytes.NewReader(raw))
		if err != nil || cfg.Server.APIKeyFile != newKey {
			t.Fatalf("Back key path=%q error=%v", cfg.Server.APIKeyFile, err)
		}
		assertGuidedInitNoLeak(t, fixture, result)
	})

	t.Run("invalid existing key fails without overwrite", func(t *testing.T) {
		fixture := newGuidedInitFixture(t)
		invalid := filepath.Join(privateInitDirectory(t, filepath.Join(fixture.root, "invalid-keys")), "invalid.key")
		const invalidPayload = "not-a-valid-" + "gateway-key\n"
		testutil.WriteTrustedFile(t, invalid, []byte(invalidPayload), 0o600)
		before := statInitPath(t, invalid)
		result := runGuidedInitPTY(t, fixture, []string{
			"init", "--gateway-auth", "file", "--gateway-key-file", invalid,
		}, strings.Join([]string{
			"1", "1", "2", "", "gpt-test", "n", "", "",
		}, "\n")+"\n")

		if result.code != 1 || !strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_failed\n") {
			t.Fatalf("invalid-key code=%d transcript=%q", result.code, result.transcript)
		}
		if _, err := os.Lstat(fixture.configPath); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("invalid-key config state=%v, want absent", err)
		}
		after := statInitPath(t, invalid)
		payload, err := os.ReadFile(invalid) //nolint:gosec // Exact test-owned key path.
		if err != nil || !os.SameFile(before, after) || string(payload) != invalidPayload {
			t.Fatal("invalid key was overwritten")
		}
		assertGuidedInitNoLeak(t, fixture, result)
	})
}

func TestGuidedSetupUnauthenticatedProviderSavesThenReturnsNotReady(t *testing.T) {
	fixture := newGuidedInitFixture(t)
	executable := filepath.Join(
		fixture.providerBin,
		guidedProviderExecutableName("fake-guided-codex-unauthenticated"),
	)
	home := filepath.Join(fixture.root, "unauthenticated-codex-home")
	result := runGuidedInitPTY(t, fixture, []string{
		"init",
		"--provider", "codex",
		"--codex-executable", executable,
		"--codex-config-home", home,
		"--codex-model", "codex-local=gpt-test",
	}, strings.Join([]string{"1", "1", "n", "", "", "1"}, "\n")+"\n")

	if result.code != 1 ||
		!strings.Contains(result.transcript, "saved_config:") ||
		!strings.Contains(result.transcript, "codex\tnot_ready") ||
		!strings.Contains(result.transcript, "auth_missing") ||
		!strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_saved_but_not_ready\n") {
		t.Fatalf("unauthenticated init code=%d transcript=%q", result.code, result.transcript)
	}
	if _, err := os.Stat(fixture.configPath); err != nil {
		t.Fatalf("unauthenticated init did not save config: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(fixture.configPath), "gateway.key")); err != nil {
		t.Fatalf("unauthenticated init did not save key: %v", err)
	}
	assertGuidedInitNoLeak(t, fixture, result)
}

func TestGuidedSetupNavigationAndCancellation(t *testing.T) {
	t.Run("final decline leaves files unchanged", func(t *testing.T) {
		fixture := newGuidedInitFixture(t)
		result := runGuidedInitPTY(t, fixture, []string{"init"}, strings.Join([]string{
			"1", "1", "2", "", "gpt-test", "n", "", "", "3",
		}, "\n")+"\n")
		if result.code != 0 || !strings.Contains(result.transcript, "Review action") {
			t.Fatalf("final decline code=%d transcript=%q", result.code, result.transcript)
		}
		assertGuidedInitFilesAbsent(t, fixture)
		assertGuidedInitNoLeak(t, fixture, result)
	})

	t.Run("final back returns to provider selection", func(t *testing.T) {
		fixture := newGuidedInitFixture(t)
		result := runGuidedInitPTY(t, fixture, []string{"init"}, strings.Join([]string{
			"1", "1", "2", "", "gpt-first", "n", "", "", "2",
			"2", "1", "2", "1", "", "sonnet", "n", "", "", "1",
		}, "\n")+"\n")
		if result.code != 0 || strings.Count(result.transcript, "Select providers") != 2 ||
			!strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_ready\n") {
			t.Fatalf("final Back code=%d transcript=%q", result.code, result.transcript)
		}
		raw := mustReadGuidedFile(t, fixture.configPath)
		cfg, err := config.Decode(bytes.NewReader(raw))
		_, hasClaude := cfg.Providers["claude"]
		_, hasCodex := cfg.Providers["codex"]
		if err != nil || !hasClaude || hasCodex || len(cfg.Providers) != 1 || len(cfg.Models) != 1 ||
			cfg.Models[0].Provider != "claude" {
			t.Fatalf("Back config=%+v error=%v", cfg, err)
		}
		assertGuidedInitNoLeak(t, fixture, result)
	})

	t.Run("typed cancel exits 130 before mutation", func(t *testing.T) {
		fixture := newGuidedInitFixture(t)
		result := runGuidedInitPTY(t, fixture, []string{"init"}, "cancel\n")
		assertGuidedCanceledBeforeCommit(t, fixture, result)
	})

	t.Run("EOF exits 130 before mutation", func(t *testing.T) {
		fixture := newGuidedInitFixture(t)
		result := runGuidedInitPTYWithAction(
			t,
			fixture,
			[]string{"init"},
			"",
			guidedPTYAction{kind: guidedPTYCloseAtNextPrompt},
		)
		assertGuidedCanceledBeforeCommit(t, fixture, result)
	})

	t.Run("Ctrl-C exits 130 before mutation", func(t *testing.T) {
		fixture := newGuidedInitFixture(t)
		result := runGuidedInitPTYWithAction(
			t,
			fixture,
			[]string{"init"},
			"",
			guidedPTYAction{kind: guidedPTYInterruptAtNextPrompt},
		)
		assertGuidedCanceledBeforeCommit(t, fixture, result)
	})

	t.Run("post-commit Ctrl-C reports saved setup", func(t *testing.T) {
		fixture := newGuidedInitFixture(t)
		home := privateInitDirectory(t, filepath.Join(fixture.root, "blocked-doctor-home"))
		block := filepath.Join(home, ".block-doctor")
		testutil.WriteTrustedFile(t, block, []byte("block\n"), 0o600)
		blocked := filepath.Join(home, ".doctor-blocked")
		executable := filepath.Join(
			fixture.providerBin,
			guidedProviderExecutableName("fake-guided-codex-ready"),
		)
		result := runGuidedInitPTYWithAction(
			t,
			fixture,
			[]string{
				"init",
				"--provider", "codex",
				"--codex-executable", executable,
				"--codex-config-home", home,
				"--codex-model", "codex-local=gpt-test",
			},
			strings.Join([]string{"1", "1", "n", "", "", "1"}, "\n")+"\n",
			guidedPTYAction{kind: guidedPTYInterruptAtPath, path: blocked},
		)
		if result.code != 130 ||
			!strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_saved_before_cancellation\n") {
			t.Fatalf("post-commit cancel code=%d transcript=%q", result.code, result.transcript)
		}
		if _, err := os.Stat(fixture.configPath); err != nil {
			t.Fatalf("post-commit config missing: %v", err)
		}
		if _, err := os.Stat(filepath.Join(filepath.Dir(fixture.configPath), "gateway.key")); err != nil {
			t.Fatalf("post-commit key missing: %v", err)
		}
		assertGuidedInitNoLeak(t, fixture, result)
	})
}

func assertGuidedCanceledBeforeCommit(
	t *testing.T,
	fixture guidedInitFixture,
	result guidedInitPTYResult,
) {
	t.Helper()
	if result.code != 130 ||
		!strings.HasSuffix(normalizeGuidedTranscript(result.transcript), "setup_not_saved\n") {
		t.Fatalf("pre-commit cancellation code=%d transcript=%q", result.code, result.transcript)
	}
	assertGuidedInitFilesAbsent(t, fixture)
	assertGuidedInitNoLeak(t, fixture, result)
}

func assertGuidedInitFilesAbsent(t *testing.T, fixture guidedInitFixture) {
	t.Helper()
	for _, path := range []string{
		fixture.configPath,
		filepath.Join(filepath.Dir(fixture.configPath), "gateway.key"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("guided setup path %q state=%v, want absent", path, err)
		}
	}
}

type guidedInitPTYResult struct {
	code       int
	transcript string
}

type guidedPTYActionKind uint8

const (
	guidedPTYKeepOpen guidedPTYActionKind = iota
	guidedPTYCloseAtNextPrompt
	guidedPTYInterruptAtNextPrompt
	guidedPTYInterruptAtPath
)

type guidedPTYAction struct {
	kind guidedPTYActionKind
	path string
}

func runGuidedInitPTY(
	t *testing.T,
	fixture guidedInitFixture,
	args []string,
	input string,
) guidedInitPTYResult {
	t.Helper()
	return runGuidedInitPTYWithAction(
		t, fixture, args, input, guidedPTYAction{kind: guidedPTYKeepOpen},
	)
}

func runGuidedInitPTYWithAction(
	t *testing.T,
	fixture guidedInitFixture,
	args []string,
	input string,
	action guidedPTYAction,
) guidedInitPTYResult {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skip("production accessible subprocess PTY currently uses the native macOS script utility")
	}
	if _, err := os.Stat("/usr/bin/script"); err != nil {
		t.Skipf("script utility unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	commandArgs := []string{"-q", "-e", "/dev/null", fixture.gateway}
	commandArgs = append(commandArgs, args...)
	// Every command and argument is a fixed value or an exact test-owned path;
	// no shell parses this invocation.
	//nolint:gosec
	command := exec.CommandContext(ctx, "/usr/bin/script", commandArgs...)
	command.Env = append([]string(nil), fixture.environment...)
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("create PTY feeder: %v", err)
	}
	command.Stdin = reader
	transcript := newInitCommandBuffer()
	command.Stdout = &transcript
	command.Stderr = &transcript
	if err := command.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		t.Fatalf("start guided init PTY: %v", err)
	}
	_ = reader.Close()
	waitResult := make(chan error, 1)
	go func() { waitResult <- command.Wait() }()
	feedCtx, stopFeeding := context.WithCancel(ctx)
	defer stopFeeding()
	writeResult := make(chan error, 1)
	go func() {
		var answers []string
		if input != "" {
			answers = strings.Split(strings.TrimSuffix(input, "\n"), "\n")
		}
		for index, answer := range answers {
			if err := waitForGuidedPrompt(feedCtx, &transcript, index+1); err != nil {
				writeResult <- err
				return
			}
			if _, err := io.WriteString(writer, answer+"\n"); err != nil {
				writeResult <- err
				return
			}
		}
		switch action.kind {
		case guidedPTYKeepOpen:
		case guidedPTYCloseAtNextPrompt:
			if err := waitForGuidedPrompt(feedCtx, &transcript, len(answers)+1); err != nil {
				writeResult <- err
				return
			}
			if err := writer.Close(); err != nil {
				writeResult <- err
				return
			}
		case guidedPTYInterruptAtNextPrompt:
			if err := waitForGuidedPrompt(feedCtx, &transcript, len(answers)+1); err != nil {
				writeResult <- err
				return
			}
			if _, err := writer.Write([]byte{3}); err != nil {
				writeResult <- err
				return
			}
		case guidedPTYInterruptAtPath:
			if err := waitForGuidedPathContext(feedCtx, action.path); err != nil {
				writeResult <- err
				return
			}
			if _, err := writer.Write([]byte{3}); err != nil {
				writeResult <- err
				return
			}
		default:
			writeResult <- errors.New("invalid guided PTY action")
			return
		}
		writeResult <- nil
	}()
	var waitErr, writeErr error
	processExitedBeforeFeed := false
	select {
	case writeErr = <-writeResult:
		select {
		case waitErr = <-waitResult:
		case <-ctx.Done():
			waitErr = ctx.Err()
		}
	case waitErr = <-waitResult:
		processExitedBeforeFeed = true
		stopFeeding()
		_ = writer.Close()
		writeErr = <-writeResult
	case <-ctx.Done():
		waitErr = ctx.Err()
	}
	_ = writer.Close()
	if errors.Is(writeErr, context.Canceled) && processExitedBeforeFeed {
		writeErr = nil
	}
	if ctx.Err() != nil {
		t.Fatalf("guided init PTY timed out: %v transcript=%q", ctx.Err(), transcript.String())
	}
	if writeErr != nil && !errors.Is(writeErr, os.ErrClosed) {
		t.Fatalf("feed guided init PTY: %v transcript=%q", writeErr, transcript.String())
	}
	return guidedInitPTYResult{
		code: commandInitExitCode(waitErr), transcript: transcript.String(),
	}
}

func waitForGuidedPathContext(ctx context.Context, path string) error {
	if path == "" {
		return errors.New("guided path is empty")
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Stat(path); err == nil {
			return nil
		} else if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForGuidedPrompt(
	ctx context.Context,
	transcript *initCommandBuffer,
	want int,
) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		if guidedPromptCount(transcript.String()) >= want {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func waitForGuidedTCPListener(t *testing.T, address string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := probeGuidedTCPListener(ctx, address); err != nil {
		t.Fatalf("wait for default serve TCP listener: %v", err)
	}
}

func probeGuidedTCPListener(ctx context.Context, address string) error {
	if ctx == nil || address == "" {
		return errors.New("guided TCP listener probe is invalid")
	}
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	var dialer net.Dialer
	for {
		connection, err := dialer.DialContext(ctx, "tcp", address)
		if err == nil {
			_ = connection.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func guidedPromptCount(transcript string) int {
	normalized := normalizeGuidedTranscript(transcript)
	count := 0
	for _, line := range strings.Split(normalized, "\n") {
		if strings.Contains(line, "back; cancel):") ||
			strings.Contains(line, "Review action (cancel aborts setup):") {
			count++
		}
	}
	return count
}

func newGuidedInitFixture(t *testing.T) guidedInitFixture {
	t.Helper()
	commandFixture := newCommandInitFixture(t)
	providerBin := privateInitDirectory(t, filepath.Join(commandFixture.root, "guided-provider-bin"))
	baseFake := testutil.BuildFakeCLI(t)
	allowedProviderBinaries := map[string]struct{}{}
	for _, name := range []string{
		"codex",
		"claude",
		"gemini",
		"fake-guided-codex-ready",
		"fake-guided-codex-unauthenticated",
		"fake-guided-claude-ready",
		"fake-guided-claude-unauthenticated",
		"fake-guided-gemini-ready",
	} {
		destination := filepath.Join(providerBin, guidedProviderExecutableName(name))
		copyGuidedProviderExecutable(
			t,
			baseFake,
			destination,
		)
		allowedProviderBinaries[filepath.Clean(destination)] = struct{}{}
	}
	const anthropicSecret = "PLANTED_ANTHROPIC_" + "CREDENTIAL_f224"
	const geminiSecret = "PLANTED_GEMINI_" + "CREDENTIAL_40d9"
	baseEnvironment := removeGuidedInitEnvironment(
		commandFixture.environment,
		"CODEX_HOME",
		"CLAUDE_CONFIG_DIR",
	)
	environment := replaceInitEnvironment(baseEnvironment, map[string]string{
		"AI_CLI_GATEWAY_ACCESSIBLE": "1",
		"ANTHROPIC_API_KEY":         anthropicSecret,
		"GEMINI_API_KEY":            geminiSecret,
		"PATH":                      providerBin,
		"TERM":                      "xterm-256color",
	})
	commandFixture.environment = environment
	gateway := testutil.BuildGateway(t)
	repositoryRoot, err := guidedInitRepositoryRoot()
	if err != nil {
		t.Fatalf("locate guided test repository for baseline: %v", err)
	}
	repositoryBaseline, err := captureGuidedRepositoryBaseline(repositoryRoot)
	if err != nil {
		t.Fatalf("capture guided test repository baseline: %v", err)
	}
	return guidedInitFixture{
		commandInitFixture:      commandFixture,
		gateway:                 gateway,
		providerBin:             providerBin,
		providerSecrets:         []string{anthropicSecret, geminiSecret},
		allowedProviderBinaries: allowedProviderBinaries,
		repositoryBaseline:      repositoryBaseline,
	}
}

func removeGuidedInitEnvironment(environment []string, names ...string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, present := strings.Cut(entry, "=")
		if !present {
			continue
		}
		remove := false
		for _, candidate := range names {
			if strings.EqualFold(name, candidate) {
				remove = true
				break
			}
		}
		if !remove {
			result = append(result, entry)
		}
	}
	return result
}

func guidedProviderExecutableName(providerName string) string {
	if runtime.GOOS == "windows" {
		return providerName + ".exe"
	}
	return providerName
}

func copyGuidedProviderExecutable(t *testing.T, source, destination string) {
	t.Helper()
	payload, err := os.ReadFile(source) //nolint:gosec // Exact test-owned build output.
	if err != nil {
		t.Fatalf("ReadFile fake provider: %v", err)
	}
	testutil.WriteTrustedFile(t, destination, payload, 0o700)
	if runtime.GOOS != "windows" {
		// The path is an exact private test-owned executable.
		//nolint:gosec
		if err := os.Chmod(destination, 0o700); err != nil {
			t.Fatalf("Chmod fake provider: %v", err)
		}
	}
}

func normalizeGuidedTranscript(value string) string {
	return strings.ReplaceAll(value, "\r\n", "\n")
}

func mustReadGuidedFile(t *testing.T, path string) []byte {
	t.Helper()
	payload, err := os.ReadFile(path) //nolint:gosec // Caller supplies an exact test-owned path.
	if err != nil {
		t.Fatalf("ReadFile %q: %v", path, err)
	}
	return payload
}

func assertGuidedInitNoLeak(
	t *testing.T,
	fixture guidedInitFixture,
	results ...guidedInitPTYResult,
) {
	t.Helper()
	protected := append(
		[]string{guidedInitRawProviderOutput},
		fixture.providerSecrets...,
	)
	keys, keyFiles, err := guidedInitKeyMaterials(fixture)
	if err != nil {
		t.Fatalf("collect owned Gateway keys: %v", err)
	}
	for _, result := range results {
		for _, forbidden := range append(append([]string(nil), protected...), keys...) {
			if strings.Contains(result.transcript, forbidden) {
				t.Fatal("guided init stdout/stderr exposed a protected value")
			}
		}
	}
	unexpectedMarker := ""
	err = filepath.WalkDir(fixture.providerBin, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".unexpected-call") {
			unexpectedMarker = path
			return fs.SkipAll
		}
		return nil
	})
	if err != nil {
		t.Fatalf("scan provider request markers: %v", err)
	}
	if unexpectedMarker != "" {
		t.Fatalf("init invoked a provider request/inference path: %q", unexpectedMarker)
	}
	if path, err := guidedInitFindProtectedFile(
		fixture.root,
		nil,
		fixture.allowedProviderBinaries,
		protected,
		keys,
		keyFiles,
	); err != nil {
		t.Fatalf("scan guided fixture files: %v", err)
	} else if path != "" {
		t.Fatalf("guided setup file retained a protected value: %q", path)
	}
	mutation, err := auditGuidedRepositoryChanges(
		fixture.repositoryBaseline,
		append(append([]string(nil), protected...), keys...),
	)
	if err != nil {
		t.Fatalf("audit guided test repository: %v", err)
	}
	if mutation.path != "" {
		if mutation.protected {
			t.Fatalf("changed repository file retained a runtime protected value: %q", mutation.path)
		}
		t.Fatalf("guided setup mutated repository file (%s): %q", mutation.kind, mutation.path)
	}
}

func guidedInitKeyMaterials(
	fixture guidedInitFixture,
) ([]string, map[string]struct{}, error) {
	values := []string{}
	seenValues := map[string]struct{}{}
	paths := map[string]struct{}{}
	owners := []string{
		filepath.Join(filepath.Dir(fixture.configPath), "gateway.key"),
	}

	configInfo, err := os.Stat(fixture.configPath)
	if err == nil {
		payload, oversized, readErr := readGuidedBoundedRegularFile(
			fixture.configPath,
			configInfo,
		)
		if readErr != nil {
			return nil, nil, fmt.Errorf("read guided config %q: %w", fixture.configPath, readErr)
		}
		if oversized {
			return nil, nil, fmt.Errorf("guided config %q exceeds the protected read limit", fixture.configPath)
		}
		cfg, decodeErr := config.Decode(bytes.NewReader(payload))
		if decodeErr != nil {
			return nil, nil, fmt.Errorf("decode guided config %q: %w", fixture.configPath, decodeErr)
		}
		if cfg.Server.APIKeyFile != "" {
			owners = append(owners, cfg.Server.APIKeyFile)
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, nil, fmt.Errorf("stat guided config %q: %w", fixture.configPath, err)
	}

	seenPaths := map[string]struct{}{}
	for _, path := range owners {
		cleanPath := filepath.Clean(path)
		if _, duplicate := seenPaths[cleanPath]; duplicate {
			continue
		}
		seenPaths[cleanPath] = struct{}{}
		info, statErr := os.Stat(cleanPath)
		if errors.Is(statErr, fs.ErrNotExist) {
			continue
		}
		if statErr != nil {
			return nil, nil, fmt.Errorf("stat owned Gateway key %q: %w", cleanPath, statErr)
		}
		payload, oversized, readErr := readGuidedBoundedRegularFile(cleanPath, info)
		if readErr != nil {
			return nil, nil, fmt.Errorf("read owned Gateway key %q: %w", cleanPath, readErr)
		}
		if oversized {
			return nil, nil, fmt.Errorf("owned Gateway key %q exceeds the protected read limit", cleanPath)
		}
		paths[cleanPath] = struct{}{}
		value := strings.TrimSpace(string(payload))
		if value == "" {
			continue
		}
		if _, duplicate := seenValues[value]; !duplicate {
			seenValues[value] = struct{}{}
			values = append(values, value)
		}
	}
	return values, paths, nil
}

func guidedInitFindProtectedFile(
	root string,
	skipDirectories map[string]struct{},
	allowedFiles map[string]struct{},
	protected []string,
	keys []string,
	keyFiles map[string]struct{},
) (string, error) {
	root = filepath.Clean(root)
	leakedPath := ""
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		cleanPath := filepath.Clean(path)
		if entry.IsDir() {
			if _, skipped := skipDirectories[cleanPath]; skipped {
				return fs.SkipDir
			}
			return nil
		}
		if _, allowed := allowedFiles[cleanPath]; allowed {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		payload, oversized, err := readGuidedBoundedRegularFile(cleanPath, info)
		if err != nil {
			return err
		}
		if oversized {
			leakedPath = cleanPath
			return fs.SkipAll
		}
		for _, value := range protected {
			if value != "" && bytes.Contains(payload, []byte(value)) {
				leakedPath = cleanPath
				return fs.SkipAll
			}
		}
		if _, allowedKeyFile := keyFiles[cleanPath]; allowedKeyFile {
			return nil
		}
		for _, value := range keys {
			if value != "" && bytes.Contains(payload, []byte(value)) {
				leakedPath = cleanPath
				return fs.SkipAll
			}
		}
		return nil
	})
	return leakedPath, err
}

func captureGuidedRepositoryBaseline(
	root string,
) (guidedRepositoryBaseline, error) {
	root = filepath.Clean(root)
	baseline := guidedRepositoryBaseline{
		root:  root,
		files: map[string]guidedRepositoryFileState{},
	}
	gitDirectory := filepath.Join(root, ".git")
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		cleanPath := filepath.Clean(path)
		if cleanPath == gitDirectory {
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		baseline.files[cleanPath] = guidedRepositoryFileState{
			info: info, mode: info.Mode(), size: info.Size(), modTime: info.ModTime(),
		}
		baseline.paths = append(baseline.paths, cleanPath)
		return nil
	})
	if err != nil {
		return guidedRepositoryBaseline{}, err
	}
	return baseline, nil
}

func auditGuidedRepositoryChanges(
	baseline guidedRepositoryBaseline,
	protected []string,
) (guidedRepositoryMutation, error) {
	if baseline.root == "" || baseline.files == nil {
		return guidedRepositoryMutation{}, errors.New("guided repository baseline is invalid")
	}
	seen := make(map[string]struct{}, len(baseline.files))
	mutation := guidedRepositoryMutation{}
	gitDirectory := filepath.Join(baseline.root, ".git")
	err := filepath.WalkDir(
		baseline.root,
		func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			cleanPath := filepath.Clean(path)
			if cleanPath == gitDirectory {
				if entry.IsDir() {
					return fs.SkipDir
				}
				return nil
			}
			if entry.IsDir() {
				return nil
			}
			seen[cleanPath] = struct{}{}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			current := guidedRepositoryFileState{
				info: info, mode: info.Mode(), size: info.Size(), modTime: info.ModTime(),
			}
			prior, existed := baseline.files[cleanPath]
			if existed && sameGuidedRepositoryFile(prior, current) {
				return nil
			}
			containsProtected, err := inspectGuidedChangedRepositoryFile(
				cleanPath,
				current,
				protected,
			)
			if err != nil {
				return err
			}
			kind := "new"
			if existed {
				kind = "changed"
			}
			mutation = guidedRepositoryMutation{
				path: cleanPath, kind: kind, protected: containsProtected,
			}
			return fs.SkipAll
		},
	)
	if err != nil || mutation.path != "" {
		return mutation, err
	}
	for _, path := range baseline.paths {
		if _, exists := seen[path]; !exists {
			return guidedRepositoryMutation{path: path, kind: "deleted"}, nil
		}
	}
	return guidedRepositoryMutation{}, nil
}

func sameGuidedRepositoryFile(
	left guidedRepositoryFileState,
	right guidedRepositoryFileState,
) bool {
	return left.mode == right.mode &&
		left.size == right.size &&
		left.modTime.Equal(right.modTime) &&
		os.SameFile(left.info, right.info)
}

func inspectGuidedChangedRepositoryFile(
	path string,
	state guidedRepositoryFileState,
	protected []string,
) (bool, error) {
	if !state.mode.IsRegular() {
		return false, nil
	}
	payload, oversized, err := readGuidedBoundedRegularFile(path, state.info)
	if err != nil {
		return false, err
	}
	if oversized {
		return false, nil
	}
	for _, value := range protected {
		if value != "" && bytes.Contains(payload, []byte(value)) {
			return true, nil
		}
	}
	return false, nil
}

func readGuidedBoundedRegularFile(
	path string,
	info fs.FileInfo,
) ([]byte, bool, error) {
	if info == nil || !info.Mode().IsRegular() || info.Size() < 0 {
		return nil, false, errors.New("guided protected file is invalid")
	}
	if info.Size() > guidedProtectedFileReadLimit {
		return nil, true, nil
	}
	file, err := os.Open(path) //nolint:gosec // Exact WalkDir candidate below a fixed test root.
	if err != nil {
		return nil, false, err
	}
	payload, readErr := io.ReadAll(io.LimitReader(
		file,
		guidedProtectedFileReadLimit+1,
	))
	closeErr := file.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, false, err
	}
	if int64(len(payload)) > guidedProtectedFileReadLimit {
		return nil, true, nil
	}
	return payload, false, nil
}

func guidedInitRepositoryRoot() (string, error) {
	current, err := os.Getwd()
	if err != nil {
		return "", errors.New("guided test repository is unavailable")
	}
	for range 8 {
		module, readErr := os.ReadFile(filepath.Join(current, "go.mod")) //nolint:gosec // Fixed ancestor search from the package directory.
		if readErr == nil && bytes.Contains(
			module,
			[]byte("module github.com/krkarma777/ai-cli-gateway"),
		) {
			return current, nil
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return "", errors.New("guided test repository is unavailable")
}
