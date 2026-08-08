//go:build integration

package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/doctor"
	"github.com/krkarma777/ai-cli-gateway/internal/httpapi"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

const appIntegrationOuterTimeout = 15 * time.Second

func TestApplicationRealFakeCLITextStructuredModelsAndShutdown(t *testing.T) {
	harness := newAppIntegrationHarness(t, "")
	var doctorOutput bytes.Buffer
	if code := Doctor(
		context.Background(),
		harness.configPath,
		true,
		&doctorOutput,
		harness.deps,
	); code != 0 {
		t.Fatalf("Doctor() matrix code = %d output=%s", code, doctorOutput.Bytes())
	}
	var matrix struct {
		Providers []doctor.Provider `json:"providers"`
		Models    []string          `json:"models"`
	}
	if err := json.Unmarshal(doctorOutput.Bytes(), &matrix); err != nil {
		t.Fatalf("decode Doctor matrix: %v; output=%s", err, doctorOutput.Bytes())
	}
	wantProviders := []doctor.Provider{
		{
			Name: core.ProviderClaude, Status: provider.HealthNotReady, Auth: "unknown",
			Problems: []string{provider.ProblemExecutableUnsafe},
		},
		{
			Name: core.ProviderCodex, Status: provider.HealthReady, Version: "1.2.3",
			Auth: "authenticated",
			Capabilities: []string{
				"ephemeral",
				"feature_hardening",
				"never_approve",
				"read_only",
				"schema_file",
				"stdin_prompt",
			},
		},
		{
			Name: core.ProviderGemini, Status: provider.HealthNotReady, Auth: "missing",
			Problems: []string{provider.ProblemCredentialMissing},
		},
	}
	if !appProviderRowsEqual(matrix.Providers, wantProviders) ||
		!slices.Equal(matrix.Models, []string{"claude-offline", "codex-test"}) {
		t.Fatalf("Doctor matrix providers/models = %+v/%q, want %+v/two aliases", matrix.Providers, matrix.Models, wantProviders)
	}
	harness.start(t)

	models := harness.request(t, http.MethodGet, "/v1/models", "", "")
	if models.status != http.StatusOK ||
		!bytes.Contains(models.body, []byte(`"id":"codex-test"`)) ||
		!bytes.Contains(models.body, []byte(`"id":"claude-offline"`)) {
		t.Fatalf("models response = %d %s", models.status, models.body)
	}
	notReady := harness.request(
		t,
		http.MethodPost,
		"/v1/responses",
		`{"model":"claude-offline","input":"must not execute"}`,
		"",
	)
	if notReady.status != http.StatusServiceUnavailable ||
		!bytes.Contains(notReady.body, []byte(`"code":"provider_not_ready"`)) {
		t.Fatalf("not-ready response = %d %s", notReady.status, notReady.body)
	}

	textResponse := harness.request(
		t,
		http.MethodPost,
		"/v1/responses",
		`{"model":"codex-test","input":"PLANTED_PROMPT_SECRET"}`,
		"",
	)
	if textResponse.status != http.StatusOK || responseOutputText(t, textResponse.body) != "hello\n" {
		t.Fatalf("text response = %d %s", textResponse.status, textResponse.body)
	}

	structuredBody := `{"model":"codex-test","input":"private structured",` +
		`"text":{"format":{"type":"json_schema","name":"answer","strict":true,` +
		`"schema":{"type":"object","properties":{"answer":{"const":"hello"}},` +
		`"required":["answer"],"additionalProperties":false}}}}`
	structured := harness.request(t, http.MethodPost, "/v1/responses", structuredBody, "")
	if structured.status != http.StatusOK ||
		responseOutputText(t, structured.body) != `{"answer":"hello"}`+"\n" {
		t.Fatalf("structured response = %d %s", structured.status, structured.body)
	}

	harness.stop(t)
	for _, secret := range []string{
		"PLANTED_PROMPT_SECRET",
		"private structured",
		harness.executable,
		harness.runtimeRoot,
	} {
		if strings.Contains(harness.logs.String(), secret) {
			t.Fatalf("application logs exposed %q: %s", secret, harness.logs.String())
		}
	}
	assertAppRuntimeNamespacesEmpty(t, harness.runtimeRoot)
}

func TestApplicationOptionalBearerAndMissingStartupKey(t *testing.T) {
	t.Run("configured key protects requests", func(t *testing.T) {
		harness := newAppIntegrationHarness(t, "SPAWNGATE_TEST_API_KEY")
		harness.start(t)
		if got := harness.gatewayKeyLookups.Load(); got != 1 {
			t.Fatalf("Gateway key lookups after startup = %d, want 1", got)
		}
		harness.gatewayKeyValue.Store("rotated-integration-gateway-key")

		missing := harness.request(t, http.MethodGet, "/v1/models", "", "")
		wrong := harness.request(t, http.MethodGet, "/v1/models", "", "wrong-key")
		rotated := harness.request(
			t,
			http.MethodGet,
			"/v1/models",
			"",
			"rotated-integration-gateway-key",
		)
		allowed := harness.request(
			t,
			http.MethodGet,
			"/v1/models",
			"",
			"integration-gateway-key",
		)
		for name, response := range map[string]appHTTPResult{
			"missing": missing,
			"wrong":   wrong,
			"rotated": rotated,
		} {
			if response.status != http.StatusUnauthorized ||
				!bytes.Contains(response.body, []byte(`"code":"invalid_bearer_key"`)) {
				t.Fatalf("%s bearer response = %d %s", name, response.status, response.body)
			}
		}
		if allowed.status != http.StatusOK {
			t.Fatalf("authorized response = %d %s", allowed.status, allowed.body)
		}
		if got := harness.gatewayKeyLookups.Load(); got != 1 {
			t.Fatalf("Gateway key lookups after requests = %d, want 1", got)
		}
		harness.stop(t)
		if strings.Contains(harness.logs.String(), "integration-gateway-key") {
			t.Fatalf("logs exposed gateway key: %s", harness.logs.String())
		}
	})

	t.Run("missing startup key never opens listener", func(t *testing.T) {
		harness := newAppIntegrationHarness(t, "SPAWNGATE_TEST_MISSING_KEY")
		harness.deps.LookupEnv = func(string) (string, bool) { return "", false }

		err := Serve(context.Background(), harness.configPath, harness.deps)

		if err != ErrNotReady { //nolint:errorlint // Missing startup key has no acquired cleanup.
			t.Fatalf("Serve() error = %v, want exact %v", err, ErrNotReady)
		}
		select {
		case <-harness.listenTaken:
			t.Fatal("listener opened despite missing startup key")
		default:
		}
	})
}

func TestApplicationExclusiveRuntimeRootRejectsSecondInstanceSafely(t *testing.T) {
	first := newAppIntegrationHarness(t, "")
	first.start(t)
	second := newAppIntegrationHarness(t, "")
	rewriteAppIntegrationConfig(t, second.configPath, second.runtimeRoot, first.runtimeRoot)

	err := Serve(context.Background(), second.configPath, second.deps)

	if err != ErrNotReady { //nolint:errorlint // Runtime-lock rejection has no app-owned cleanup.
		t.Fatalf("second Serve() error = %v, want exact %v", err, ErrNotReady)
	}
	select {
	case <-second.listenTaken:
		t.Fatal("second instance opened its listener while root was locked")
	default:
	}
	if strings.Contains(second.logs.String(), first.runtimeRoot) {
		t.Fatalf("second instance logs exposed root path: %s", second.logs.String())
	}
	first.stop(t)
}

func TestApplicationShutdownCancelsActiveFakeCLITreeAndQueuedRequests(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process-group fixture; native Windows Job coverage runs separately")
	}
	harness := newAppIntegrationHarness(t, "")
	treeExecutable := buildPIDTreeProviderFake(t, harness.configHome)
	rewriteAppIntegrationConfig(t, harness.configPath, harness.executable, treeExecutable)
	harness.executable = treeExecutable
	janitorResults := make(chan error, 2)
	originalJanitor := harness.deps.Janitor
	harness.deps.Janitor = func(ctx context.Context, root *process.Root) error {
		err := originalJanitor(ctx, root)
		janitorResults <- err
		return err
	}
	rootCloseResults := make(chan error, 1)
	originalCloseRoot := harness.deps.CloseRoot
	harness.deps.CloseRoot = func(root *process.Root) error {
		err := originalCloseRoot(root)
		rootCloseResults <- err
		return err
	}
	listenerCloseResults := make(chan error, 1)
	originalListen := harness.deps.Listen
	harness.deps.Listen = func(network, address string) (net.Listener, error) {
		listener, err := originalListen(network, address)
		if err != nil || listener == nil {
			return listener, err
		}
		return &appIntegrationRecordingListener{
			Listener: listener,
			results:  listenerCloseResults,
		}, nil
	}
	harness.start(t)
	active := make(chan appHTTPResult, 1)
	go func() {
		active <- harness.requestOnceWithTimeout(
			http.MethodPost,
			"/v1/responses",
			`{"model":"codex-test","input":"spawn-child"}`,
			"",
			appIntegrationOuterTimeout,
		)
	}()
	childPID := waitForAppRuntimePID(t, harness.runtimeRoot)
	disarmChildCleanup := installAppFailurePIDCleanup(t, childPID)

	const contenders = 8
	queuedResults := make(chan appHTTPResult, contenders)
	for index := range contenders {
		go func(value int) {
			queuedResults <- harness.requestOnceWithTimeout(
				http.MethodPost,
				"/v1/responses",
				fmt.Sprintf(`{"model":"codex-test","input":"queued-%d"}`, value),
				"",
				appIntegrationOuterTimeout,
			)
		}(index)
	}
	received := 0
	observed := make([]string, 0, contenders)
	deadline, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	for received < contenders-1 {
		select {
		case result := <-queuedResults:
			received++
			observed = append(observed, fmt.Sprintf("status=%d err=%v body=%s", result.status, result.err, result.body))
			if result.status != http.StatusTooManyRequests ||
				!bytes.Contains(result.body, []byte(`"code":"queue_full"`)) {
				t.Fatalf("unexpected pre-shutdown queue result: %q", observed)
			}
		case <-deadline.Done():
			t.Fatalf("bounded provider queue did not reject all excess contenders: %q", observed)
		}
	}

	harness.cancel()
	serveErr := awaitAppValue(t, harness.result, "integration Serve shutdown")
	harness.cancel = nil
	activeResult := awaitAppValue(t, active, "active fake CLI request cancellation")
	for received < contenders {
		_ = awaitAppValue(t, queuedResults, "queued request shutdown")
		received++
	}
	startupJanitorErr := awaitAppValue(t, janitorResults, "startup janitor result")
	shutdownJanitorErr := awaitAppValue(t, janitorResults, "shutdown janitor result")
	rootCloseErr := awaitAppValue(t, rootCloseResults, "root close result")
	listenerCloseErr := awaitAppValue(t, listenerCloseResults, "listener close result")
	if serveErr != nil {
		t.Fatalf(
			"Serve shutdown error: %v; active status=%d err=%v body=%s; "+
				"startup_janitor=%v shutdown_janitor=%v root_close=%v listener_close=%v",
			serveErr,
			activeResult.status,
			activeResult.err,
			activeResult.body,
			startupJanitorErr,
			shutdownJanitorErr,
			rootCloseErr,
			listenerCloseErr,
		)
	}
	if startupJanitorErr != nil || shutdownJanitorErr != nil ||
		rootCloseErr != nil || listenerCloseErr != nil {
		t.Fatalf(
			"unexpected cleanup errors: startup_janitor=%v shutdown_janitor=%v root_close=%v listener_close=%v",
			startupJanitorErr,
			shutdownJanitorErr,
			rootCloseErr,
			listenerCloseErr,
		)
	}
	waitForAppProcessExit(t, childPID)
	disarmChildCleanup()
	assertAppRuntimeNamespacesEmpty(t, harness.runtimeRoot)
	if strings.Contains(harness.logs.String(), "spawn-child") ||
		strings.Contains(harness.logs.String(), "queued-") {
		t.Fatalf("shutdown logs exposed request input: %s", harness.logs.String())
	}
}

func TestApplicationHarnessEarlyReturnCleanupDrainsChildTree(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix process-group fixture; native Windows Job coverage runs separately")
	}
	var childPID int
	var active <-chan appHTTPResult
	var disarmChildCleanup func()
	outerTest := t
	t.Run("return without explicit stop", func(t *testing.T) {
		harness := newAppIntegrationHarness(t, "")
		treeExecutable := buildPIDTreeProviderFake(t, harness.configHome)
		rewriteAppIntegrationConfig(t, harness.configPath, harness.executable, treeExecutable)
		harness.executable = treeExecutable
		harness.start(t)
		activeResults := make(chan appHTTPResult, 1)
		active = activeResults
		go func() {
			activeResults <- harness.requestOnceWithTimeout(
				http.MethodPost,
				"/v1/responses",
				`{"model":"codex-test","input":"spawn-child"}`,
				"",
				appIntegrationOuterTimeout,
			)
		}()
		childPID = waitForAppRuntimePID(t, harness.runtimeRoot)
		disarmChildCleanup = installAppFailurePIDCleanup(outerTest, childPID)
		// Returning without stop exercises the harness failure/early-return cleanup.
	})

	result := awaitAppValue(t, active, "early-return active request cancellation")
	if result.status != http.StatusServiceUnavailable ||
		!bytes.Contains(result.body, []byte(`"code":"service_shutting_down"`)) {
		t.Fatalf("early-return active response = %d err=%v body=%s", result.status, result.err, result.body)
	}
	waitForAppProcessExit(t, childPID)
	disarmChildCleanup()
}

func TestCommandSignalsExitCleanlyWithFakeCodexAndNoDescendants(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows interrupt coverage runs in the Windows CI gate")
	}
	for _, test := range []struct {
		name   string
		signal os.Signal
	}{
		{name: "SIGINT", signal: os.Interrupt},
		{name: "SIGTERM", signal: syscall.Signal(15)},
	} {
		t.Run(test.name, func(t *testing.T) {
			gatewayExecutable := testutil.BuildGateway(t)
			base := testutil.TrustedTempDir(t)
			// The command fixture parent intentionally requires owner-only access.
			//nolint:gosec
			if err := os.Chmod(base, 0o700); err != nil {
				t.Fatalf("chmod command fixture: %v", err)
			}
			fakeCodex := buildCommandProbeFake(t, base)
			configHome := testutil.TrustedTempDir(t)
			runtimeRoot := filepath.Join(base, "PLANTED_COMMAND_PATH_SECRET-runtime")
			var listenConfig net.ListenConfig
			reserved, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatalf("reserve command listener: %v", err)
			}
			address := reserved.Addr().String()
			if err := reserved.Close(); err != nil {
				t.Fatalf("release command listener: %v", err)
			}
			configPath := writeCommandIntegrationConfig(
				t,
				base,
				address,
				runtimeRoot,
				fakeCodex,
				configHome,
			)
			var stdout bytes.Buffer
			var stderr bytes.Buffer
			commandCtx, commandCancel := context.WithTimeout(
				context.Background(),
				appIntegrationOuterTimeout,
			)
			defer commandCancel()
			// Executable and argv are test-owned fixed values; no shell is involved.
			//nolint:gosec
			command := exec.CommandContext(
				commandCtx,
				gatewayExecutable,
				"serve",
				"--config",
				configPath,
			)
			command.Stdout = &stdout
			command.Stderr = &stderr
			if err := command.Start(); err != nil {
				t.Fatalf("start gateway command: %v", err)
			}
			waitResult := make(chan error, 1)
			go func() { waitResult <- command.Wait() }()
			waitForCommandModels(t, address, waitResult)
			if err := command.Process.Signal(test.signal); err != nil {
				t.Fatalf("signal gateway command: %v", err)
			}
			if err := awaitAppValue(t, waitResult, "gateway command signal exit"); err != nil {
				t.Fatalf("gateway command exit: %v; stdout=%q stderr=%q", err, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("serve stdout = %q, want empty", stdout.String())
			}
			for _, secret := range []string{
				configPath,
				runtimeRoot,
				fakeCodex,
				"PLANTED_COMMAND_PATH_SECRET",
			} {
				if strings.Contains(stderr.String(), secret) {
					t.Fatalf("command stderr exposed %q: %s", secret, stderr.String())
				}
			}
			assertAppRuntimeNamespacesEmpty(t, runtimeRoot)
		})
	}
}

func TestDoctorCommandResolvesExactUnixEnvNodeLauncher(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix env-node launcher fixture; native Windows launcher coverage is separate")
	}
	testutil.AcquireRepositoryScanLock(t)
	gatewayExecutable := testutil.BuildGateway(t)
	base := testutil.TrustedTempDir(t)
	// The command fixture parent intentionally requires owner-only access.
	//nolint:gosec
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatalf("chmod command fixture: %v", err)
	}
	launcher := filepath.Join(base, "codex")
	fakeCodex := buildNodeCommandProbeFake(t, base, launcher)
	nodeBin := filepath.Join(base, "node-bin")
	if err := os.Mkdir(nodeBin, 0o700); err != nil {
		t.Fatalf("create fake Node directory: %v", err)
	}
	node := filepath.Join(nodeBin, "node")
	if err := os.Symlink(fakeCodex, node); err != nil {
		t.Fatalf("symlink fake Node: %v", err)
	}
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env node\n"), 0o700); err != nil {
		t.Fatalf("write exact Node launcher: %v", err)
	}
	if err := os.Chmod(launcher, 0o700); err != nil {
		t.Fatalf("chmod exact Node launcher: %v", err)
	}
	configHome := testutil.TrustedTempDir(t)
	runtimeRoot := filepath.Join(base, "PLANTED_COMMAND_PATH_SECRET-runtime")
	configPath := writeCommandIntegrationConfig(
		t,
		base,
		"127.0.0.1:8080",
		runtimeRoot,
		launcher,
		configHome,
	)
	priorPath := os.Getenv("PATH")
	startupPath := strings.Join([]string{nodeBin, priorPath}, string(os.PathListSeparator))

	runDoctor := func(t *testing.T, path string) (int, string, string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), appIntegrationOuterTimeout)
		defer cancel()
		command := exec.CommandContext(ctx, gatewayExecutable, "doctor", "--config", configPath)
		command.Env = make([]string, 0, len(os.Environ())+2)
		for _, environment := range os.Environ() {
			if !strings.HasPrefix(environment, "PATH=") {
				command.Env = append(command.Env, environment)
			}
		}
		command.Env = append(
			command.Env,
			"PATH="+path,
			"PLANTED_AMBIENT_SECRET=PLANTED_AMBIENT_VALUE",
		)
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		command.Stdout = &stdout
		command.Stderr = &stderr
		runErr := command.Run()
		if command.ProcessState == nil {
			t.Fatalf("run gateway doctor: %v", runErr)
		}
		return command.ProcessState.ExitCode(), stdout.String(), stderr.String()
	}

	t.Run("ready", func(t *testing.T) {
		exitCode, stdout, stderr := runDoctor(t, startupPath)
		if exitCode != 0 || !strings.Contains(stdout, "codex\tready\t0.146.0") {
			t.Fatalf("doctor exit/output = %d/%q stderr=%q", exitCode, stdout, stderr)
		}
		if strings.Contains(stdout+stderr, "PLANTED_AMBIENT_SECRET") {
			t.Fatalf("doctor exposed planted ambient name: stdout=%q stderr=%q", stdout, stderr)
		}
	})

	t.Run("missing Node", func(t *testing.T) {
		emptyPath := filepath.Join(base, "empty-path")
		if err := os.Mkdir(emptyPath, 0o700); err != nil {
			t.Fatalf("create empty startup PATH directory: %v", err)
		}
		exitCode, stdout, stderr := runDoctor(t, emptyPath)
		if exitCode != 1 || !strings.Contains(stdout, "executable_unsafe") ||
			strings.Contains(stdout, "version_unreadable") {
			t.Fatalf("doctor exit/output = %d/%q stderr=%q", exitCode, stdout, stderr)
		}
		for _, secret := range []string{
			"PLANTED_AMBIENT_SECRET",
			"PLANTED_AMBIENT_VALUE",
			base,
			configPath,
			launcher,
			fakeCodex,
			runtimeRoot,
		} {
			if strings.Contains(stdout+stderr, secret) {
				t.Fatalf("doctor exposed %q: stdout=%q stderr=%q", secret, stdout, stderr)
			}
		}
		if strings.Contains(stdout+stderr, string(os.PathSeparator)) {
			t.Fatalf("doctor exposed filesystem path: stdout=%q stderr=%q", stdout, stderr)
		}
	})
}

func TestApplicationRedactsDistinctProcessAndDependencySecrets(t *testing.T) {
	harness := newAppIntegrationHarness(t, "")
	buildRedactionProviderFake(t, harness.configHome)
	harness.start(t)

	response := harness.request(
		t,
		http.MethodPost,
		"/v1/responses",
		`{"model":"codex-test","input":"redaction-probe"}`,
		"",
	)
	if response.status != http.StatusOK || responseOutputText(t, response.body) != "safe-final" {
		t.Fatalf("redaction response = %d %s", response.status, response.body)
	}
	harness.stop(t)
	visible := string(response.body) + harness.logs.String()
	for _, secret := range []string{
		"PLANTED_STDOUT_SECRET",
		"PLANTED_STDERR_SECRET",
		"PLANTED_ARGV_SECRET",
		"PLANTED_CREDENTIAL_SECRET",
		"redaction-probe",
		harness.configPath,
		harness.executable,
		harness.runtimeRoot,
	} {
		if strings.Contains(visible, secret) {
			t.Fatalf("HTTP/log surface exposed %q: %s", secret, visible)
		}
	}
}

type appIntegrationHarness struct {
	configPath  string
	executable  string
	configHome  string
	runtimeRoot string
	address     string
	listener    net.Listener
	deps        Dependencies
	logs        bytes.Buffer
	ctx         context.Context
	cancel      context.CancelFunc
	result      chan error
	listenTaken chan struct{}
	gatewayKey  string

	gatewayKeyValue   atomic.Value
	gatewayKeyLookups atomic.Int64
}

type appIntegrationRecordingListener struct {
	net.Listener
	results chan<- error
}

func (listener *appIntegrationRecordingListener) Close() error {
	err := listener.Listener.Close()
	listener.results <- err
	return err
}

func newAppIntegrationHarness(t *testing.T, apiKeyEnv string) *appIntegrationHarness {
	t.Helper()
	base := testutil.TrustedTempDir(t)
	// The integration fixture parent intentionally requires owner-only access.
	//nolint:gosec
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatalf("chmod integration parent: %v", err)
	}
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind application listener: %v", err)
	}
	executable := testutil.BuildFakeCLI(t)
	configHome := testutil.TrustedTempDir(t)
	geminiHome := testutil.TrustedTempDir(t)
	runtimeRoot := filepath.Join(base, "runtime")
	apiKeyLine := ""
	if apiKeyEnv != "" {
		apiKeyLine = "api_key_env = " + strconv.Quote(apiKeyEnv) + "\n"
	}
	document := fmt.Sprintf(`[server]
listen = %s
%sshutdown_timeout = "8s"

[runtime]
root = %s
term_grace = "100ms"
cleanup_timeout = "1s"
stdout_bytes = 65536
stderr_bytes = 65536
final_bytes = 8192

[providers.codex]
executable = %s
config_home = %s
concurrency = 1
queue_size = 1
queue_bytes = 1048576
queue_timeout = "1s"
execution_timeout = "10s"

[providers.claude]
executable = %s
config_home = %s
concurrency = 1
queue_size = 2
queue_bytes = 4096
queue_timeout = "1s"
execution_timeout = "10s"

[providers.gemini]
executable = %s
config_home = %s
credential_env = ["GEMINI_API_KEY"]
concurrency = 1
queue_size = 2
queue_bytes = 4096
queue_timeout = "1s"
execution_timeout = "10s"

[[models]]
id = "codex-test"
provider = "codex"
provider_model = "fake-model"
created = 11

[[models]]
id = "claude-offline"
provider = "claude"
provider_model = "offline-model"
created = 12
`,
		strconv.Quote(listener.Addr().String()),
		apiKeyLine,
		strconv.Quote(runtimeRoot),
		strconv.Quote(executable),
		strconv.Quote(configHome),
		strconv.Quote(base),
		strconv.Quote(filepath.Join(base, "missing-claude-home")),
		strconv.Quote(executable),
		strconv.Quote(geminiHome),
	)
	configPath := filepath.Join(base, "config.toml")
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatalf("write integration config: %v", err)
	}

	harness := &appIntegrationHarness{
		configPath:  configPath,
		executable:  executable,
		configHome:  configHome,
		runtimeRoot: runtimeRoot,
		address:     listener.Addr().String(),
		listener:    listener,
		result:      make(chan error, 1),
		listenTaken: make(chan struct{}),
	}
	if apiKeyEnv != "" {
		harness.gatewayKey = "integration-gateway-key"
		harness.gatewayKeyValue.Store(harness.gatewayKey)
	}
	deps := ProductionDependencies(&harness.logs)
	ambientLookup := deps.LookupEnv
	deps.LookupEnv = func(name string) (string, bool) {
		if name == "GEMINI_API_KEY" {
			return "", false
		}
		if apiKeyEnv != "" && name == apiKeyEnv {
			harness.gatewayKeyLookups.Add(1)
			return harness.gatewayKeyValue.Load().(string), true
		}
		return ambientLookup(name)
	}
	deps.Adapters[core.ProviderCodex] = &appIntegrationAdapter{}
	deps.GatewayExecutable = func() (string, error) { return executable, nil }
	deps.NewProbeController = func(
		*process.Root,
		process.Limits,
		func() (string, error),
	) (doctor.ProbeController, error) {
		return &appTestProbeController{}, nil
	}
	var runtimeSequence atomic.Uint64
	deps.NewRuntimeID = func() (string, error) {
		return fmt.Sprintf("%032x", runtimeSequence.Add(1)), nil
	}
	deps.NewHTTPIDs = func() (httpapi.IDSource, error) {
		return httpapi.NewOpaqueIDSource(bytes.NewReader(bytes.Repeat([]byte{7}, 32)))
	}
	deps.Listen = func(network, address string) (net.Listener, error) {
		if network != "tcp" || address != harness.address {
			t.Fatalf("Listen args = %q/%q, want tcp/%q", network, address, harness.address)
		}
		close(harness.listenTaken)
		return listener, nil
	}
	harness.deps = deps
	t.Cleanup(func() {
		if harness.cancel != nil {
			harness.cancel()
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 12*time.Second)
			select {
			case <-harness.result:
				harness.cancel = nil
			case <-cleanupCtx.Done():
				t.Errorf("timed out draining integration Serve during test cleanup")
			}
			cleanupCancel()
		}
		_ = listener.Close()
	})
	return harness
}

func (h *appIntegrationHarness) start(t *testing.T) {
	t.Helper()
	h.ctx, h.cancel = context.WithCancel(context.Background())
	go func() { h.result <- Serve(h.ctx, h.configPath, h.deps) }()
	awaitAppIntegrationSignal(t, h.listenTaken, "integration listener handoff")
	deadline, cancel := context.WithTimeout(context.Background(), appIntegrationOuterTimeout)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		response := h.requestOnceWithTimeout(
			http.MethodGet,
			"/v1/models",
			"",
			h.gatewayKey,
			250*time.Millisecond,
		)
		if response.err == nil && response.status == http.StatusOK {
			return
		}
		select {
		case <-deadline.Done():
			t.Fatalf("application did not become ready: last=%v %d %s", response.err, response.status, response.body)
		case serveErr := <-h.result:
			h.cancel = nil
			t.Fatalf("Serve returned before readiness: %v", serveErr)
		case <-ticker.C:
		}
	}
}

func (h *appIntegrationHarness) stop(t *testing.T) {
	t.Helper()
	h.cancel()
	err := awaitAppValue(t, h.result, "integration Serve shutdown")
	h.cancel = nil
	if err != nil {
		t.Fatalf("Serve shutdown error: %v", err)
	}
}

type appHTTPResult struct {
	status int
	body   []byte
	err    error
}

func (h *appIntegrationHarness) request(
	t *testing.T,
	method, path, body, bearer string,
) appHTTPResult {
	t.Helper()
	result := h.requestOnce(method, path, body, bearer)
	if result.err != nil {
		t.Fatalf("HTTP %s %s: %v", method, path, result.err)
	}
	return result
}

func (h *appIntegrationHarness) requestOnce(method, path, body, bearer string) appHTTPResult {
	return h.requestOnceWithTimeout(method, path, body, bearer, appIntegrationOuterTimeout)
}

func (h *appIntegrationHarness) requestOnceWithTimeout(
	method, path, body, bearer string,
	timeout time.Duration,
) appHTTPResult {
	requestCtx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(
		requestCtx,
		method,
		"http://"+h.address+path,
		strings.NewReader(body),
	)
	if err != nil {
		return appHTTPResult{err: err}
	}
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
	if err != nil {
		return appHTTPResult{err: err}
	}
	defer func() { _ = response.Body.Close() }()
	payload, err := io.ReadAll(response.Body)
	return appHTTPResult{status: response.StatusCode, body: payload, err: err}
}

func responseOutputText(t *testing.T, payload []byte) string {
	t.Helper()
	var response struct {
		Output []struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := json.Unmarshal(payload, &response); err != nil ||
		len(response.Output) != 1 || len(response.Output[0].Content) != 1 {
		t.Fatalf("decode Responses payload: %v; body=%s", err, payload)
	}
	return response.Output[0].Content[0].Text
}

func appProviderRowsEqual(left, right []doctor.Provider) bool {
	return slices.EqualFunc(left, right, func(a, b doctor.Provider) bool {
		return a.Name == b.Name && a.Status == b.Status && a.Version == b.Version &&
			a.Auth == b.Auth && slices.Equal(a.Capabilities, b.Capabilities) &&
			slices.Equal(a.Problems, b.Problems)
	})
}

func assertAppRuntimeNamespacesEmpty(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read runtime root: %v", err)
	}
	for _, entry := range entries {
		if entry.Name() != ".lock" {
			t.Fatalf("runtime artifact remained after shutdown: %s", entry.Name())
		}
	}
}

func rewriteAppIntegrationConfig(t *testing.T, path, oldValue, newValue string) {
	t.Helper()
	// path is returned by this test's private trusted fixture.
	//nolint:gosec
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read integration config: %v", err)
	}
	updated := bytes.Replace(
		payload,
		[]byte(strconv.Quote(oldValue)),
		[]byte(strconv.Quote(newValue)),
		1,
	)
	if bytes.Equal(updated, payload) {
		t.Fatal("integration config value was not replaced")
	}
	// path is returned by this test's private trusted fixture.
	//nolint:gosec
	if err := os.WriteFile(path, updated, 0o600); err != nil {
		t.Fatalf("rewrite integration config: %v", err)
	}
}

func awaitAppIntegrationSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), appIntegrationOuterTimeout)
	defer cancel()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s", name)
	}
}

func waitForAppRuntimePID(t *testing.T, root string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), appIntegrationOuterTimeout)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	pattern := filepath.Join(root, "request-*", ".fake-child-ready")
	for {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatalf("glob runtime PID marker: %v", err)
		}
		if len(matches) > 1 {
			t.Fatalf("multiple runtime PID markers: %d", len(matches))
		}
		if len(matches) == 1 {
			pidPath := filepath.Join(filepath.Dir(matches[0]), ".fake-child-pid")
			// pidPath is confined to this test's private validated runtime root.
			//nolint:gosec
			payload, readErr := os.ReadFile(pidPath)
			if readErr == nil {
				pid, parseErr := strconv.Atoi(strings.TrimSpace(string(payload)))
				if parseErr != nil || pid <= 0 || pid == os.Getpid() {
					t.Fatalf("invalid runtime child PID %q: %v", payload, parseErr)
				}
				process, findErr := os.FindProcess(pid)
				if findErr != nil || process.Signal(syscall.Signal(0)) != nil {
					t.Fatalf("recorded runtime child PID %d is not alive", pid)
				}
				return pid
			}
			if !os.IsNotExist(readErr) {
				t.Fatalf("read runtime PID marker: %v", readErr)
			}
		}
		select {
		case <-ctx.Done():
			t.Fatal("runtime ready/PID markers did not become live")
		case <-ticker.C:
		}
	}
}

func waitForAppProcessExit(t *testing.T, pid int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		process, err := os.FindProcess(pid)
		if err != nil || process.Signal(syscall.Signal(0)) != nil {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("fixture child PID %d remained alive after shutdown", pid)
		case <-ticker.C:
		}
	}
}

func installAppFailurePIDCleanup(t *testing.T, pid int) func() {
	t.Helper()
	armed := true
	t.Cleanup(func() {
		if !armed {
			return
		}
		process, err := os.FindProcess(pid)
		if err != nil || process.Signal(syscall.Signal(0)) != nil {
			return
		}
		if err := process.Kill(); err != nil {
			t.Errorf("failure cleanup kill exact fixture child PID %d: %v", pid, err)
			return
		}
		waitForAppProcessExitDuringCleanup(t, pid)
	})
	return func() {
		armed = false
	}
}

func waitForAppProcessExitDuringCleanup(t *testing.T, pid int) {
	t.Helper()
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		process, err := os.FindProcess(pid)
		if err != nil || process.Signal(syscall.Signal(0)) != nil {
			return
		}
		select {
		case <-timer.C:
			t.Errorf("exact fixture child PID %d remained alive after failure cleanup kill", pid)
			return
		case <-ticker.C:
		}
	}
}

func buildCommandProbeFake(t *testing.T, directory string) string {
	t.Helper()
	return buildCommandProbeFakeWithArgSetup(
		t,
		directory,
		`args := strings.Join(os.Args[1:], " ")`,
	)
}

func buildNodeCommandProbeFake(t *testing.T, directory, launcher string) string {
	t.Helper()
	return buildCommandProbeFakeWithArgSetup(
		t,
		directory,
		fmt.Sprintf(`if len(os.Args) < 2 || os.Args[1] != %q {
		os.Exit(92)
	}
	args := strings.Join(os.Args[2:], " ")`, launcher),
	)
}

func buildCommandProbeFakeWithArgSetup(t *testing.T, directory, argSetup string) string {
	t.Helper()
	source := `package main
import (
	"fmt"
	"os"
	"strings"
)
func main() {
	if os.Getenv("PLANTED_AMBIENT_SECRET") != "" {
		os.Exit(91)
	}
` + argSetup + `
	switch {
	case strings.HasSuffix(args, "--version"):
		fmt.Println("codex 0.146.0")
	case strings.Contains(args, "exec --help"):
		fmt.Println("PROMPT - --disable -c --strict-config --sandbox --model --output-schema --color --ephemeral --ignore-user-config --ignore-rules --skip-git-repo-check")
	case strings.HasSuffix(args, "features list"):
		for _, feature := range []string{
			"shell_tool", "unified_exec", "code_mode_host", "apps", "plugins",
			"remote_plugin", "hooks", "multi_agent", "browser_use",
			"browser_use_external", "computer_use", "in_app_browser",
			"image_generation", "skill_search", "skill_mcp_dependency_install",
			"workspace_dependencies",
		} {
			fmt.Println(feature + " stable false")
		}
	case strings.HasSuffix(args, "login status"):
		return
	case strings.HasSuffix(args, "doctor --json"):
		fmt.Println(` + "`" + `{"schemaVersion":1,"overallStatus":"ok","checks":{"auth.credentials":{"id":"auth.credentials","status":"ok"},"config.load":{"id":"config.load","status":"ok"},"installation":{"id":"installation","status":"ok"}}}` + "`" + `)
default:
		fmt.Println("hello")
	}
}
`
	sourcePath := filepath.Join(directory, "fake-codex.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatalf("write command fake source: %v", err)
	}
	name := "fake-codex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(directory, name)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Paths are test-owned and the command is invoked directly without a shell.
	//nolint:gosec
	command := exec.CommandContext(ctx, "go", "build", "-o", executable, sourcePath)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("build command fake: %v: %s", err, output.String())
	}
	return executable
}

func buildRedactionProviderFake(t *testing.T, directory string) string {
	t.Helper()
	source := `package main
import (
	"fmt"
	"os"
)
func main() {
	_, _ = fmt.Fprint(os.Stdout, "PLANTED_STDOUT_SECRET")
	_, _ = fmt.Fprint(os.Stderr, "PLANTED_STDERR_SECRET")
}
`
	sourcePath := filepath.Join(directory, "redaction-fake.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatalf("write redaction fake source: %v", err)
	}
	name := "redaction-fake"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(directory, name)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Paths are test-owned and the command is invoked directly without a shell.
	//nolint:gosec
	command := exec.CommandContext(ctx, "go", "build", "-o", executable, sourcePath)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("build redaction fake: %v: %s", err, output.String())
	}
	return executable
}

func buildPIDTreeProviderFake(t *testing.T, directory string) string {
	t.Helper()
	source := `package main
import (
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"syscall"
	"time"
)
func main() {
	if len(os.Args) == 2 && os.Args[1] == "--pid-child" {
		signal.Ignore(syscall.SIGTERM)
		_ = os.WriteFile(".fake-child-pid", []byte(strconv.Itoa(os.Getpid())+"\n"), 0600)
		_ = os.WriteFile(".fake-child-ready", []byte("ready\n"), 0600)
		for { time.Sleep(time.Hour) }
	}
	executable, err := os.Executable()
	if err != nil { os.Exit(1) }
	command := exec.Command(executable, "--pid-child")
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil { os.Exit(1) }
	deadline := time.Now().Add(10*time.Second)
	for {
		if _, err := os.Lstat(".fake-child-ready"); err == nil { break }
		if err != nil && !errors.Is(err, os.ErrNotExist) { os.Exit(1) }
		if time.Now().After(deadline) { os.Exit(1) }
		time.Sleep(time.Millisecond)
	}
	for { time.Sleep(time.Hour) }
}
`
	sourcePath := filepath.Join(directory, "pid-tree-fake.go")
	if err := os.WriteFile(sourcePath, []byte(source), 0o600); err != nil {
		t.Fatalf("write PID tree fake source: %v", err)
	}
	name := "pid-tree-fake"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	executable := filepath.Join(directory, name)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Paths are test-owned and the command is invoked directly without a shell.
	//nolint:gosec
	command := exec.CommandContext(ctx, "go", "build", "-o", executable, sourcePath)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Run(); err != nil {
		t.Fatalf("build PID tree fake: %v: %s", err, output.String())
	}
	return executable
}

func writeCommandIntegrationConfig(
	t *testing.T,
	directory, address, runtimeRoot, executable, configHome string,
) string {
	t.Helper()
	document := fmt.Sprintf(`[server]
listen = %s
shutdown_timeout = "2s"

[runtime]
root = %s
term_grace = "100ms"
cleanup_timeout = "100ms"
stdout_bytes = 65536
stderr_bytes = 65536
final_bytes = 8192

[providers.codex]
executable = %s
config_home = %s
concurrency = 1
queue_size = 1
queue_bytes = 4096
queue_timeout = "1s"
execution_timeout = "2s"

[[models]]
id = "codex-command"
provider = "codex"
provider_model = "fake-model"
created = 21
`,
		strconv.Quote(address),
		strconv.Quote(runtimeRoot),
		strconv.Quote(executable),
		strconv.Quote(configHome),
	)
	path := filepath.Join(directory, "PLANTED_CONFIG_SECRET.toml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write command config: %v", err)
	}
	return path
}

func waitForCommandModels(t *testing.T, address string, waitResult <-chan error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+address+"/v1/models", nil)
		if err != nil {
			t.Fatalf("construct command readiness request: %v", err)
		}
		response, requestErr := (&http.Client{Timeout: 250 * time.Millisecond}).Do(request)
		if response != nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
		}
		if requestErr == nil && response != nil && response.StatusCode == http.StatusOK {
			return
		}
		select {
		case err := <-waitResult:
			t.Fatalf("gateway command returned before readiness: %v", err)
		case <-ctx.Done():
			t.Fatal("gateway command readiness deadline exceeded")
		case <-ticker.C:
		}
	}
}

type appIntegrationAdapter struct{}

func (*appIntegrationAdapter) Name() core.ProviderName { return core.ProviderCodex }

func (*appIntegrationAdapter) SupportedVersion() provider.Range {
	return provider.Range{
		MinInclusive: provider.Version{Major: 1},
		MaxExclusive: provider.Version{Major: 2},
	}
}

func (*appIntegrationAdapter) Probe(
	context.Context,
	provider.ProviderConfig,
	provider.ProbeRunner,
) provider.Health {
	return provider.Health{
		Provider: core.ProviderCodex,
		Status:   provider.HealthReady,
		Version:  "1.2.3",
		Auth:     "authenticated",
		Capabilities: []string{
			"ephemeral",
			"feature_hardening",
			"never_approve",
			"read_only",
			"schema_file",
			"stdin_prompt",
		},
	}
}

func (*appIntegrationAdapter) Build(
	request core.Request,
	model core.Model,
	cfg provider.ProviderConfig,
	runtimeState process.Runtime,
) (process.CommandSpec, error) {
	if model.Provider != core.ProviderCodex {
		return process.CommandSpec{}, provider.NewProviderError(provider.ProviderErrorFailed)
	}
	mode := "text"
	switch {
	case request.Input == "redaction-probe":
		name := "redaction-fake"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		environment := []string{"PLANTED_CREDENTIAL=PLANTED_CREDENTIAL_SECRET"}
		if systemRoot, present := cfg.LookupEnv("SystemRoot"); present {
			environment = append(environment, "SystemRoot="+systemRoot)
		}
		return process.CommandSpec{
			Executable: filepath.Join(cfg.ConfigHome, name),
			Args:       []string{"PLANTED_ARGV_SECRET"},
			Env:        environment,
			Dir:        runtimeState.Dir,
		}, nil
	case request.Input == "spawn-child":
		mode = "spawn-ignore-term-child"
	case request.Format.Type == core.FormatJSONSchema:
		mode = "codex-json"
	}
	return process.CommandSpec{
		Executable: cfg.Executable,
		Args:       []string{"--mode", mode},
		Env:        []string{},
		Dir:        runtimeState.Dir,
		Stdin:      []byte(request.Input),
	}, nil
}

func (*appIntegrationAdapter) Parse(
	request core.Request,
	result process.Result,
) (string, error) {
	if result.ExitCode != 0 {
		return "", provider.NewProviderError(provider.ProviderErrorFailed)
	}
	if request.Input == "redaction-probe" {
		if string(result.Stdout) != "PLANTED_STDOUT_SECRET" ||
			string(result.Stderr) != "PLANTED_STDERR_SECRET" {
			return "", provider.NewProviderError(provider.ProviderErrorProtocol)
		}
		return "safe-final", nil
	}
	return string(result.Stdout), nil
}
