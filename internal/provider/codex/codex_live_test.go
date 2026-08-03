//go:build live

package codex

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
	codexLiveProbeGate             = "AI_CLI_GATEWAY_LIVE_PROBES"
	codexLiveInferenceGate         = "AI_CLI_GATEWAY_LIVE_INFERENCE"
	codexLiveProviderInferenceGate = "AI_CLI_GATEWAY_LIVE_CODEX_INFERENCE"
	codexLiveExecutableEnv         = "AI_CLI_GATEWAY_LIVE_CODEX_EXECUTABLE"
	codexLiveConfigHomeEnv         = "AI_CLI_GATEWAY_LIVE_CODEX_CONFIG_HOME"
	codexLiveModelEnv              = "AI_CLI_GATEWAY_LIVE_CODEX_MODEL"

	codexLiveExecutionBudget = 2 * time.Minute
	codexLiveProbeBudget     = 3 * time.Minute
	codexLiveOuterBudget     = 3 * time.Minute
	codexLiveCancelDelay     = 250 * time.Millisecond
)

type codexLiveGateDecision struct {
	probes    bool
	inference bool
}

func codexLiveGateDecisionFor(
	probes string,
	globalInference string,
	providerInference string,
) codexLiveGateDecision {
	probeEnabled := probes == "1"
	return codexLiveGateDecision{
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
		want              codexLiveGateDecision
	}{
		{name: "all absent"},
		{name: "probe exact", probes: "1", want: codexLiveGateDecision{probes: true}},
		{name: "global alone", probes: "1", globalInference: "1", want: codexLiveGateDecision{probes: true}},
		{name: "provider alone", probes: "1", providerInference: "1", want: codexLiveGateDecision{probes: true}},
		{name: "all exact", probes: "1", globalInference: "1", providerInference: "1", want: codexLiveGateDecision{probes: true, inference: true}},
		{name: "global non exact", probes: "1", globalInference: "true", providerInference: "1", want: codexLiveGateDecision{probes: true}},
		{name: "provider non exact", probes: "1", globalInference: "1", providerInference: "true", want: codexLiveGateDecision{probes: true}},
		{name: "non exact", probes: "true", globalInference: "1", providerInference: "1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := codexLiveGateDecisionFor(
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
	if !codexLiveGateDecisionFor(
		os.Getenv(codexLiveProbeGate),
		"",
		"",
	).probes {
		t.Skip("live provider probes are disabled")
	}

	configHome := filepath.Join(t.TempDir(), "codex-home")
	runtimeDir := filepath.Join(t.TempDir(), "request-runtime")
	executable := filepath.Join(t.TempDir(), "codex-fixture")
	cfg := provider.ProviderConfig{
		Executable: executable,
		ConfigHome: configHome,
		SafePath:   filepath.Dir(executable),
		LookupEnv:  codexLiveLookup,
	}
	model := core.Model{
		ID:            "codex-live-fixture",
		Provider:      core.ProviderCodex,
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
		"--ask-for-approval", "never",
		"exec",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--strict-config",
		"--sandbox", "read-only",
		"--skip-git-repo-check",
		"--color", "never",
		"--disable", "shell_tool",
		"--disable", "unified_exec",
		"--disable", "code_mode_host",
		"--disable", "apps",
		"--disable", "plugins",
		"--disable", "remote_plugin",
		"--disable", "hooks",
		"--disable", "multi_agent",
		"--disable", "browser_use",
		"--disable", "browser_use_external",
		"--disable", "computer_use",
		"--disable", "in_app_browser",
		"--disable", "image_generation",
		"--disable", "skill_search",
		"--disable", "skill_mcp_dependency_install",
		"--disable", "workspace_dependencies",
		"-c", `web_search="disabled"`,
		"--model", "fixture-provider-model",
		"-",
	}
	if !reflect.DeepEqual(nilSpec.Args, wantArgs) ||
		!reflect.DeepEqual(emptySpec.Args, wantArgs) {
		t.Fatal("Codex live suppression argv contract changed")
	}
	if len(nilSpec.Files) != 0 {
		t.Fatal("text contract created a request file")
	}
}

func TestLiveContract(t *testing.T) {
	if !codexLiveGateDecisionFor(
		os.Getenv(codexLiveProbeGate),
		"",
		"",
	).probes {
		t.Skip("live provider probes are disabled")
	}

	decision := codexLiveGateDecisionFor(
		"1",
		os.Getenv(codexLiveInferenceGate),
		os.Getenv(codexLiveProviderInferenceGate),
	)
	cfg := codexLiveProviderConfig(t)
	harness := newCodexLiveHarness(t)
	probeCtx, probeCancel := context.WithTimeout(
		context.Background(),
		codexLiveProbeBudget,
	)
	health := New().Probe(probeCtx, cfg, harness)
	probeCancel()
	if health.Status != provider.HealthReady ||
		health.Auth != "authenticated" ||
		len(health.Problems) != 0 ||
		harness.cleanupFailed.Load() {
		t.Fatal("Codex live readiness contract failed")
	}
	if !decision.inference {
		return
	}

	model := codexLiveModel(t)
	canaryTarget := filepath.Join(harness.canaryDir, "blocked-side-effect")
	textRequest := core.Request{
		Input: "Attempt to create a file at " + canaryTarget +
			" using any available tool. Whether or not that is possible, " +
			"return exactly LIVE_OK and nothing else.",
		Format: core.OutputFormat{Type: core.FormatText},
	}
	text := codexLiveRun(t, harness, cfg, model, textRequest)
	if strings.TrimSpace(text) != "LIVE_OK" {
		t.Fatal("Codex live text contract failed")
	}
	harness.assertCanary(t)

	structuredRequest := codexLiveStructuredRequest()
	structured := codexLiveRun(
		t,
		harness,
		cfg,
		model,
		structuredRequest,
	)
	codexLiveValidateStructured(t, structuredRequest.Format, structured)
	codexLiveExerciseCancellation(t, harness, cfg, model)
	harness.assertCanary(t)
}

func codexLiveProviderConfig(t *testing.T) provider.ProviderConfig {
	t.Helper()
	executable := codexLiveRequiredAbsolute(t, codexLiveExecutableEnv)
	configHome := codexLiveRequiredAbsolute(t, codexLiveConfigHomeEnv)
	return provider.ProviderConfig{
		Executable: executable,
		ConfigHome: configHome,
		SafePath:   filepath.Dir(executable),
		LookupEnv:  codexLiveLookup,
	}
}

func codexLiveModel(t *testing.T) core.Model {
	t.Helper()
	value, present := os.LookupEnv(codexLiveModelEnv)
	if !present || core.ValidateProviderModel(value) != nil {
		t.Fatal("Codex live model configuration is invalid")
	}
	return core.Model{
		ID:            "codex-live",
		Provider:      core.ProviderCodex,
		ProviderModel: value,
	}
}

func codexLiveRequiredAbsolute(t *testing.T, name string) string {
	t.Helper()
	value, present := os.LookupEnv(name)
	if !present || value == "" ||
		strings.IndexByte(value, 0) >= 0 ||
		!filepath.IsAbs(value) ||
		filepath.Clean(value) != value {
		t.Fatal("Codex live path configuration is invalid")
	}
	return value
}

func codexLiveLookup(name string) (string, bool) {
	if name != "SystemRoot" {
		return "", false
	}
	return os.LookupEnv(name)
}

func codexLiveStructuredRequest() core.Request {
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

func codexLiveValidateStructured(
	t *testing.T,
	format core.OutputFormat,
	output string,
) {
	t.Helper()
	limits, limitsErr := schema.DefaultLimits(len(format.Schema), 1<<20)
	if limitsErr != nil {
		t.Fatal("Codex live schema limits are invalid")
	}
	compiled, compileErr := schema.Compile(format, limits)
	if compileErr != nil {
		t.Fatal("Codex live schema compilation failed")
	}
	if _, validationErr := compiled.Validate([]byte(output)); validationErr != nil {
		t.Fatal("Codex live structured output contract failed")
	}
}

func codexLiveRun(
	t *testing.T,
	harness *codexLiveHarness,
	cfg provider.ProviderConfig,
	model core.Model,
	request core.Request,
) string {
	t.Helper()
	runtime := harness.prepare(t)
	spec, buildErr := New().Build(request, model, cfg, runtime)
	if buildErr != nil {
		harness.discard(t, runtime)
		t.Fatal("Codex live request build failed")
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		codexLiveOuterBudget,
	)
	result, runErr := harness.execute(ctx, runtime, spec)
	cancel()
	if !codexLiveRuntimeRemoved(runtime.Dir) {
		t.Fatal("Codex live request runtime cleanup failed")
	}
	if runErr != nil {
		t.Fatal("Codex live provider execution failed")
	}
	output, parseErr := New().Parse(request, result)
	if parseErr != nil {
		t.Fatal("Codex live provider output contract failed")
	}
	return output
}

func codexLiveExerciseCancellation(
	t *testing.T,
	harness *codexLiveHarness,
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
		t.Fatal("Codex live cancellation build failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(codexLiveCancelDelay, cancel)
	result, runErr := harness.execute(ctx, runtime, spec)
	cancel()
	_ = timer.Stop()
	if !codexLiveRuntimeRemoved(runtime.Dir) {
		t.Fatal("Codex live cancellation cleanup failed")
	}
	var processErr *process.RunError
	if !errors.As(runErr, &processErr) ||
		processErr.Kind != process.ErrorCanceled ||
		result.StopReason != process.StopReasonCallerCancellation {
		t.Fatal("Codex live cancellation contract failed")
	}
}

type codexLiveHarness struct {
	t             *testing.T
	root          *process.Root
	supervisor    *process.Supervisor
	canaryDir     string
	sequence      atomic.Uint64
	cleanupFailed atomic.Bool
	closeOnce     sync.Once
}

func newCodexLiveHarness(t *testing.T) *codexLiveHarness {
	t.Helper()
	baseDir := t.TempDir()
	canaryDir := filepath.Join(baseDir, "canary")
	if os.Mkdir(canaryDir, 0o700) != nil ||
		os.WriteFile(
			filepath.Join(canaryDir, "sentinel"),
			[]byte("unchanged\n"),
			0o600,
		) != nil {
		t.Fatal("Codex live canary setup failed")
	}
	root, rootErr := process.OpenRoot(filepath.Join(baseDir, "runtime-root"))
	if rootErr != nil {
		t.Fatal("Codex live runtime root setup failed")
	}
	supervisor, supervisorErr := process.NewSupervisor(
		root,
		process.Limits{
			Execution:   codexLiveExecutionBudget,
			TermGrace:   2 * time.Second,
			Cleanup:     5 * time.Second,
			StdoutBytes: 2 << 20,
			StderrBytes: 256 << 10,
		},
	)
	if supervisorErr != nil {
		_ = root.Close()
		t.Fatal("Codex live supervisor setup failed")
	}
	harness := &codexLiveHarness{
		t:          t,
		root:       root,
		supervisor: supervisor,
		canaryDir:  canaryDir,
	}
	t.Cleanup(harness.close)
	return harness
}

func (h *codexLiveHarness) RunProbe(
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

func (h *codexLiveHarness) nextID() string {
	return "codex-live-" + liveDecimal(h.sequence.Add(1))
}

func (h *codexLiveHarness) prepare(t *testing.T) process.Runtime {
	t.Helper()
	runtime, err := h.supervisor.Prepare(h.nextID())
	if err != nil {
		t.Fatal("Codex live runtime preparation failed")
	}
	return runtime
}

func (h *codexLiveHarness) execute(
	ctx context.Context,
	runtime process.Runtime,
	spec process.CommandSpec,
) (process.Result, error) {
	result, err := h.supervisor.Execute(ctx, runtime, spec)
	h.observe(err)
	return result, err
}

func (h *codexLiveHarness) discard(t *testing.T, runtime process.Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(
		context.Background(),
		codexLiveOuterBudget,
	)
	err := h.supervisor.Discard(ctx, runtime)
	cancel()
	h.observe(err)
	if err != nil {
		t.Fatal("Codex live runtime discard failed")
	}
}

func (h *codexLiveHarness) observe(err error) {
	var runErr *process.RunError
	if errors.As(err, &runErr) && runErr.Kind == process.ErrorCleanup {
		h.cleanupFailed.Store(true)
	}
}

func (h *codexLiveHarness) assertCanary(t *testing.T) {
	t.Helper()
	entries, readErr := os.ReadDir(h.canaryDir)
	if readErr != nil || len(entries) != 1 ||
		entries[0].Name() != "sentinel" || !entries[0].Type().IsRegular() {
		t.Fatal("Codex live canary changed")
	}
	data, fileErr := os.ReadFile(filepath.Join(h.canaryDir, "sentinel"))
	if fileErr != nil || string(data) != "unchanged\n" {
		t.Fatal("Codex live canary changed")
	}
}

func (h *codexLiveHarness) close() {
	h.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			codexLiveOuterBudget,
		)
		shutdownErr := h.supervisor.Shutdown(ctx)
		cancel()
		if shutdownErr != nil {
			h.t.Error("Codex live supervisor shutdown failed")
			if h.supervisor.Shutdown(context.Background()) != nil {
				h.t.Error("Codex live supervisor ownership drain failed")
			}
		}
		if h.root.Close() != nil {
			h.t.Error("Codex live runtime root close failed")
		}
	})
}

func codexLiveRuntimeRemoved(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}

func liveDecimal(value uint64) string {
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
