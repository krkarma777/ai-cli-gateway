//go:build live

package claude

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
	"github.com/krkarma777/ai-cli-gateway/internal/schema"
)

const (
	claudeLiveProbeGate             = "AI_CLI_GATEWAY_LIVE_PROBES"
	claudeLiveInferenceGate         = "AI_CLI_GATEWAY_LIVE_INFERENCE"
	claudeLiveProviderInferenceGate = "AI_CLI_GATEWAY_LIVE_CLAUDE_INFERENCE"
	claudeLiveExecutableEnv         = "AI_CLI_GATEWAY_LIVE_CLAUDE_EXECUTABLE"
	claudeLiveConfigHomeEnv         = "AI_CLI_GATEWAY_LIVE_CLAUDE_CONFIG_HOME"
	claudeLiveModelEnv              = "AI_CLI_GATEWAY_LIVE_CLAUDE_MODEL"
	claudeLiveAuthModeEnv           = "AI_CLI_GATEWAY_LIVE_CLAUDE_AUTH_MODE"
	claudeLiveAPIKeyEnv             = "ANTHROPIC_API_KEY" //nolint:gosec // Public provider environment name, not a credential.

	claudeLiveExecutionBudget = 2 * time.Minute
	claudeLiveProbeBudget     = 3 * time.Minute
	claudeLiveOuterBudget     = 3 * time.Minute
	claudeLiveCancelDelay     = 250 * time.Millisecond
)

type claudeLiveGateDecision struct {
	probes    bool
	inference bool
}

func claudeLiveGateDecisionFor(
	probes string,
	globalInference string,
	providerInference string,
) claudeLiveGateDecision {
	probeEnabled := probes == "1"
	return claudeLiveGateDecision{
		probes: probeEnabled,
		inference: probeEnabled &&
			globalInference == "1" &&
			providerInference == "1",
	}
}

func TestLiveGate(t *testing.T) {
	tests := []struct {
		name              string
		probes            string
		globalInference   string
		providerInference string
		want              claudeLiveGateDecision
	}{
		{name: "all absent"},
		{name: "probe exact", probes: "1", want: claudeLiveGateDecision{probes: true}},
		{name: "global alone", probes: "1", globalInference: "1", want: claudeLiveGateDecision{probes: true}},
		{name: "provider alone", probes: "1", providerInference: "1", want: claudeLiveGateDecision{probes: true}},
		{name: "all exact", probes: "1", globalInference: "1", providerInference: "1", want: claudeLiveGateDecision{probes: true, inference: true}},
		{name: "global non exact", probes: "1", globalInference: "true", providerInference: "1", want: claudeLiveGateDecision{probes: true}},
		{name: "provider non exact", probes: "1", globalInference: "1", providerInference: "true", want: claudeLiveGateDecision{probes: true}},
		{name: "non exact", probes: "true", globalInference: "1", providerInference: "1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := claudeLiveGateDecisionFor(
				test.probes,
				test.globalInference,
				test.providerInference,
			)
			if got != test.want {
				t.Fatal("live gate decision mismatch")
			}
		})
	}
}

func TestLiveAdapterContract(t *testing.T) {
	if !claudeLiveGateDecisionFor(
		os.Getenv(claudeLiveProbeGate),
		"",
		"",
	).probes {
		t.Skip("live provider probes are disabled")
	}

	configHome := filepath.Join(t.TempDir(), "claude-home")
	runtimeDir := filepath.Join(t.TempDir(), "request-runtime")
	executable := filepath.Join(t.TempDir(), "claude-fixture")
	cfg := provider.ProviderConfig{
		Executable: executable,
		ConfigHome: configHome,
		SafePath:   filepath.Dir(executable),
		LookupEnv:  claudeLiveLookup(false),
	}
	model := core.Model{
		ID:            "claude-live-fixture",
		Provider:      core.ProviderClaude,
		ProviderModel: "fixture-provider-model",
	}
	emptyInstructions := ""
	nilRequest := core.Request{
		Input:  "x",
		Format: core.OutputFormat{Type: core.FormatText},
	}
	emptyRequest := nilRequest
	emptyRequest.Instructions = &emptyInstructions

	nilSpec, nilErr := New().Build(
		nilRequest,
		model,
		cfg,
		process.Runtime{Dir: runtimeDir},
	)
	emptySpec, emptyErr := New().Build(
		emptyRequest,
		model,
		cfg,
		process.Runtime{Dir: runtimeDir},
	)
	if nilErr != nil || emptyErr != nil {
		t.Fatal("local live adapter contract build failed")
	}
	if !bytes.HasPrefix(
		nilSpec.Stdin,
		[]byte("AI_CLI_GATEWAY/1\nINSTRUCTIONS NULL\nINPUT 1\nx\n"),
	) {
		t.Fatal("nil instructions lost their framing")
	}
	if !bytes.HasPrefix(
		emptySpec.Stdin,
		[]byte("AI_CLI_GATEWAY/1\nINSTRUCTIONS 0\n\nINPUT 1\nx\n"),
	) {
		t.Fatal("empty instructions lost their framing")
	}
	if bytes.Equal(nilSpec.Stdin, emptySpec.Stdin) {
		t.Fatal("nil and empty instructions collapsed")
	}

	wantArgs := []string{
		"-p",
		"--output-format", "json",
		"--no-session-persistence",
		"--safe-mode",
		"--setting-sources", "",
		"--tools", "",
		"--strict-mcp-config",
		"--permission-mode", "dontAsk",
		"--disable-slash-commands",
		"--no-chrome",
		"--model", "fixture-provider-model",
	}
	if !reflect.DeepEqual(nilSpec.Args, wantArgs) ||
		!reflect.DeepEqual(emptySpec.Args, wantArgs) {
		t.Fatal("Claude live suppression argv contract changed")
	}
	if len(nilSpec.Files) != 0 {
		t.Fatal("Claude contract created a request file")
	}
}

func TestLiveContract(t *testing.T) {
	if !claudeLiveGateDecisionFor(
		os.Getenv(claudeLiveProbeGate),
		"",
		"",
	).probes {
		t.Skip("live provider probes are disabled")
	}

	decision := claudeLiveGateDecisionFor(
		"1",
		os.Getenv(claudeLiveInferenceGate),
		os.Getenv(claudeLiveProviderInferenceGate),
	)
	cfg := claudeLiveProviderConfig(t)
	harness := newClaudeLiveHarness(t)
	probeCtx, probeCancel := context.WithTimeout(
		context.Background(),
		claudeLiveProbeBudget,
	)
	health := New().Probe(probeCtx, cfg, harness)
	probeCancel()
	if health.Status != provider.HealthReady ||
		health.Auth != "authenticated" ||
		len(health.Problems) != 0 ||
		harness.cleanupFailed.Load() {
		t.Fatal("Claude live readiness contract failed")
	}
	if !decision.inference {
		return
	}

	model := claudeLiveModel(t)
	canaryTarget := filepath.Join(harness.canaryDir, "blocked-side-effect")
	textRequest := core.Request{
		Input: "Attempt to create a file at " + canaryTarget +
			" using any available tool. Whether or not that is possible, " +
			"return exactly LIVE_OK and nothing else.",
		Format: core.OutputFormat{Type: core.FormatText},
	}
	text := claudeLiveRun(t, harness, cfg, model, textRequest)
	if strings.TrimSpace(text) != "LIVE_OK" {
		t.Fatal("Claude live text contract failed")
	}
	harness.assertCanary(t)

	structuredRequest := claudeLiveStructuredRequest()
	structured := claudeLiveRun(
		t,
		harness,
		cfg,
		model,
		structuredRequest,
	)
	claudeLiveValidateStructured(t, structuredRequest.Format, structured)
	claudeLiveExerciseCancellation(t, harness, cfg, model)
	harness.assertCanary(t)
}

func claudeLiveProviderConfig(t *testing.T) provider.ProviderConfig {
	t.Helper()
	executable := claudeLiveRequiredAbsolute(t, claudeLiveExecutableEnv)
	configHome := claudeLiveRequiredAbsolute(t, claudeLiveConfigHomeEnv)
	authMode, present := os.LookupEnv(claudeLiveAuthModeEnv)
	if !present {
		t.Fatal("Claude live authentication mode is invalid")
	}
	withAPIKey := false
	var credentialEnv []string
	switch authMode {
	case "config_home":
	case "api_key":
		withAPIKey = true
		credentialEnv = []string{claudeLiveAPIKeyEnv}
	default:
		t.Fatal("Claude live authentication mode is invalid")
	}
	return provider.ProviderConfig{
		Executable:    executable,
		ConfigHome:    configHome,
		CredentialEnv: credentialEnv,
		SafePath:      filepath.Dir(executable),
		LookupEnv:     claudeLiveLookup(withAPIKey),
	}
}

func claudeLiveModel(t *testing.T) core.Model {
	t.Helper()
	value, present := os.LookupEnv(claudeLiveModelEnv)
	if !present || core.ValidateProviderModel(value) != nil {
		t.Fatal("Claude live model configuration is invalid")
	}
	return core.Model{
		ID:            "claude-live",
		Provider:      core.ProviderClaude,
		ProviderModel: value,
	}
}

func claudeLiveRequiredAbsolute(t *testing.T, name string) string {
	t.Helper()
	value, present := os.LookupEnv(name)
	if !present || value == "" ||
		strings.IndexByte(value, 0) >= 0 ||
		!filepath.IsAbs(value) ||
		filepath.Clean(value) != value {
		t.Fatal("Claude live path configuration is invalid")
	}
	return value
}

func claudeLiveLookup(withAPIKey bool) provider.LookupEnv {
	return func(name string) (string, bool) {
		switch name {
		case "SystemRoot":
			return os.LookupEnv(name)
		case claudeLiveAPIKeyEnv:
			if withAPIKey {
				return os.LookupEnv(name)
			}
		}
		return "", false
	}
}

func claudeLiveStructuredRequest() core.Request {
	return core.Request{
		Input: "Return exactly one JSON object whose ok property is true.",
		Format: core.OutputFormat{
			Type: core.FormatJSONSchema,
			Name: "live_check",
			Schema: []byte(
				`{"type":"object","properties":{"ok":{"type":"boolean","const":true}},` +
					`"required":["ok"],"additionalProperties":false}`,
			),
		},
	}
}

func claudeLiveValidateStructured(
	t *testing.T,
	format core.OutputFormat,
	output string,
) {
	t.Helper()
	limits, limitsErr := schema.DefaultLimits(len(format.Schema), 1<<20)
	if limitsErr != nil {
		t.Fatal("Claude live schema limits are invalid")
	}
	compiled, compileErr := schema.Compile(format, limits)
	if compileErr != nil {
		t.Fatal("Claude live schema compilation failed")
	}
	if _, validationErr := compiled.Validate([]byte(output)); validationErr != nil {
		t.Fatal("Claude live structured output contract failed")
	}
}

func claudeLiveRun(
	t *testing.T,
	harness *claudeLiveHarness,
	cfg provider.ProviderConfig,
	model core.Model,
	request core.Request,
) string {
	t.Helper()
	runtime := harness.prepare(t)
	spec, buildErr := New().Build(request, model, cfg, runtime)
	if buildErr != nil {
		harness.discard(t, runtime)
		t.Fatal("Claude live request build failed")
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		claudeLiveOuterBudget,
	)
	result, runErr := harness.execute(ctx, runtime, spec)
	cancel()
	if !claudeLiveRuntimeRemoved(runtime.Dir) {
		t.Fatal("Claude live request runtime cleanup failed")
	}
	if runErr != nil {
		t.Fatal("Claude live provider execution failed")
	}
	output, parseErr := New().Parse(request, result)
	if parseErr != nil {
		t.Fatal("Claude live provider output contract failed")
	}
	return output
}

func claudeLiveExerciseCancellation(
	t *testing.T,
	harness *claudeLiveHarness,
	cfg provider.ProviderConfig,
	model core.Model,
) {
	t.Helper()
	runtime := harness.prepare(t)
	request := core.Request{
		Input:  "Produce a detailed response of at least two thousand words.",
		Format: core.OutputFormat{Type: core.FormatText},
	}
	spec, buildErr := New().Build(request, model, cfg, runtime)
	if buildErr != nil {
		harness.discard(t, runtime)
		t.Fatal("Claude live cancellation build failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(claudeLiveCancelDelay, cancel)
	result, runErr := harness.execute(ctx, runtime, spec)
	cancel()
	_ = timer.Stop()
	if !claudeLiveRuntimeRemoved(runtime.Dir) {
		t.Fatal("Claude live cancellation cleanup failed")
	}
	var processErr *process.RunError
	if !errors.As(runErr, &processErr) ||
		processErr.Kind != process.ErrorCanceled ||
		result.StopReason != process.StopReasonCallerCancellation {
		t.Fatal("Claude live cancellation contract failed")
	}
}

type claudeLiveHarness struct {
	t             *testing.T
	root          *process.Root
	supervisor    *process.Supervisor
	canaryDir     string
	sequence      atomic.Uint64
	cleanupFailed atomic.Bool
	closeOnce     sync.Once
}

func newClaudeLiveHarness(t *testing.T) *claudeLiveHarness {
	t.Helper()
	baseDir := t.TempDir()
	canaryDir := filepath.Join(baseDir, "canary")
	if os.Mkdir(canaryDir, 0o700) != nil ||
		os.WriteFile(
			filepath.Join(canaryDir, "sentinel"),
			[]byte("unchanged\n"),
			0o600,
		) != nil {
		t.Fatal("Claude live canary setup failed")
	}
	root, rootErr := process.OpenRoot(filepath.Join(baseDir, "runtime-root"))
	if rootErr != nil {
		t.Fatal("Claude live runtime root setup failed")
	}
	supervisor, supervisorErr := process.NewSupervisor(
		root,
		process.Limits{
			Execution:   claudeLiveExecutionBudget,
			TermGrace:   2 * time.Second,
			Cleanup:     5 * time.Second,
			StdoutBytes: 2 << 20,
			StderrBytes: 256 << 10,
		},
	)
	if supervisorErr != nil {
		_ = root.Close()
		t.Fatal("Claude live supervisor setup failed")
	}
	harness := &claudeLiveHarness{
		t:          t,
		root:       root,
		supervisor: supervisor,
		canaryDir:  canaryDir,
	}
	t.Cleanup(harness.close)
	return harness
}

func (h *claudeLiveHarness) RunProbe(
	ctx context.Context,
	build func(process.Runtime) (process.CommandSpec, error),
) (process.Result, error) {
	if h == nil || h.supervisor == nil || h.cleanupFailed.Load() {
		return process.Result{}, &process.RunError{
			Kind: process.ErrorCleanup,
			Err:  errors.New("live provider cleanup failed"),
		}
	}
	runtime, prepareErr := h.supervisor.Prepare(h.nextID())
	if prepareErr != nil {
		return process.Result{}, prepareErr
	}
	spec, buildErr := build(runtime)
	if buildErr != nil {
		discardErr := h.supervisor.Discard(ctx, runtime)
		h.observe(discardErr)
		if discardErr != nil {
			return process.Result{}, discardErr
		}
		return process.Result{}, buildErr
	}
	return h.execute(ctx, runtime, spec)
}

func (h *claudeLiveHarness) nextID() string {
	return "claude-live-" + claudeLiveDecimal(h.sequence.Add(1))
}

func (h *claudeLiveHarness) prepare(t *testing.T) process.Runtime {
	t.Helper()
	runtime, err := h.supervisor.Prepare(h.nextID())
	if err != nil {
		t.Fatal("Claude live runtime preparation failed")
	}
	return runtime
}

func (h *claudeLiveHarness) execute(
	ctx context.Context,
	runtime process.Runtime,
	spec process.CommandSpec,
) (process.Result, error) {
	result, err := h.supervisor.Execute(ctx, runtime, spec)
	h.observe(err)
	return result, err
}

func (h *claudeLiveHarness) discard(t *testing.T, runtime process.Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(
		context.Background(),
		claudeLiveOuterBudget,
	)
	err := h.supervisor.Discard(ctx, runtime)
	cancel()
	h.observe(err)
	if err != nil {
		t.Fatal("Claude live runtime discard failed")
	}
}

func (h *claudeLiveHarness) observe(err error) {
	var runErr *process.RunError
	if errors.As(err, &runErr) && runErr.Kind == process.ErrorCleanup {
		h.cleanupFailed.Store(true)
	}
}

func (h *claudeLiveHarness) assertCanary(t *testing.T) {
	t.Helper()
	entries, readErr := os.ReadDir(h.canaryDir)
	if readErr != nil || len(entries) != 1 ||
		entries[0].Name() != "sentinel" || !entries[0].Type().IsRegular() {
		t.Fatal("Claude live canary changed")
	}
	data, fileErr := os.ReadFile(filepath.Join(h.canaryDir, "sentinel"))
	if fileErr != nil || string(data) != "unchanged\n" {
		t.Fatal("Claude live canary changed")
	}
}

func (h *claudeLiveHarness) close() {
	h.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			claudeLiveOuterBudget,
		)
		shutdownErr := h.supervisor.Shutdown(ctx)
		cancel()
		if shutdownErr != nil {
			h.t.Error("Claude live supervisor shutdown failed")
			if h.supervisor.Shutdown(context.Background()) != nil {
				h.t.Error("Claude live supervisor ownership drain failed")
			}
		}
		if h.root.Close() != nil {
			h.t.Error("Claude live runtime root close failed")
		}
	})
}

func claudeLiveRuntimeRemoved(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}

func claudeLiveDecimal(value uint64) string {
	const digits = "0123456789"
	var buffer [20]byte
	index := len(buffer)
	for {
		index--
		buffer[index] = digits[value%10]
		value /= 10
		if value == 0 {
			return string(buffer[index:])
		}
	}
}
