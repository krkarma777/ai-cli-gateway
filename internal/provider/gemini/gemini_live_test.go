//go:build live

package gemini

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
	geminiLiveProbeGate             = "AI_CLI_GATEWAY_LIVE_PROBES"
	geminiLiveInferenceGate         = "AI_CLI_GATEWAY_LIVE_INFERENCE"
	geminiLiveProviderInferenceGate = "AI_CLI_GATEWAY_LIVE_GEMINI_INFERENCE"
	geminiLiveExecutableEnv         = "AI_CLI_GATEWAY_LIVE_GEMINI_EXECUTABLE"
	geminiLiveConfigHomeEnv         = "AI_CLI_GATEWAY_LIVE_GEMINI_CONFIG_HOME"
	geminiLiveModelEnv              = "AI_CLI_GATEWAY_LIVE_GEMINI_MODEL"
	geminiLiveAuthModeEnv           = "AI_CLI_GATEWAY_LIVE_GEMINI_AUTH_MODE"

	geminiLiveExecutionBudget = 2 * time.Minute
	geminiLiveProbeBudget     = 3 * time.Minute
	geminiLiveOuterBudget     = 3 * time.Minute
	geminiLiveCancelDelay     = 250 * time.Millisecond
)

type geminiLiveGateDecision struct {
	probes    bool
	inference bool
}

func geminiLiveGateDecisionFor(
	probes string,
	globalInference string,
	providerInference string,
) geminiLiveGateDecision {
	probeEnabled := probes == "1"
	return geminiLiveGateDecision{
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
		want              geminiLiveGateDecision
	}{
		{name: "all absent"},
		{name: "probe exact", probes: "1", want: geminiLiveGateDecision{probes: true}},
		{name: "global alone", probes: "1", globalInference: "1", want: geminiLiveGateDecision{probes: true}},
		{name: "provider alone", probes: "1", providerInference: "1", want: geminiLiveGateDecision{probes: true}},
		{name: "all exact", probes: "1", globalInference: "1", providerInference: "1", want: geminiLiveGateDecision{probes: true, inference: true}},
		{name: "global non exact", probes: "1", globalInference: "true", providerInference: "1", want: geminiLiveGateDecision{probes: true}},
		{name: "provider non exact", probes: "1", globalInference: "1", providerInference: "true", want: geminiLiveGateDecision{probes: true}},
		{name: "non exact", probes: "true", globalInference: "1", providerInference: "1"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := geminiLiveGateDecisionFor(
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
	if !geminiLiveGateDecisionFor(
		os.Getenv(geminiLiveProbeGate),
		"",
		"",
	).probes {
		t.Skip("live provider probes are disabled")
	}

	configHome := filepath.Join(t.TempDir(), "gemini-home")
	runtimeDir := filepath.Join(t.TempDir(), "request-runtime")
	executable := filepath.Join(t.TempDir(), "gemini-fixture")
	cfg := provider.ProviderConfig{
		Executable:    executable,
		ConfigHome:    configHome,
		CredentialEnv: []string{geminiAPIKeyName},
		SafePath:      filepath.Dir(executable),
		LookupEnv: func(name string) (string, bool) {
			switch name {
			case geminiAPIKeyName:
				return "fixture-value", true
			case "SystemRoot":
				return `C:\Windows`, true
			default:
				return "", false
			}
		},
	}
	model := core.Model{
		ID:            "gemini-live-fixture",
		Provider:      core.ProviderGemini,
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
		"--output-format", "json",
		"--approval-mode", "default",
		"-e", "none",
		"--model", "fixture-provider-model",
	}
	if !reflect.DeepEqual(nilSpec.Args, wantArgs) ||
		!reflect.DeepEqual(emptySpec.Args, wantArgs) {
		t.Fatal("Gemini live suppression argv contract changed")
	}
	if len(nilSpec.Files) != 1 ||
		nilSpec.Files[0].Name != filepath.Join(".gemini", "settings.json") ||
		nilSpec.Files[0].Mode != 0o600 {
		t.Fatal("Gemini live settings file contract changed")
	}
	wantSettings := []byte(
		`{"advanced":{"ignoreLocalEnv":true},` +
			`"experimental":{"enableAgents":false},` +
			`"hooksConfig":{"enabled":false},` +
			`"mcp":{"allowed":[]},"mcpServers":{},` +
			`"privacy":{"usageStatisticsEnabled":false},` +
			`"security":{"auth":{"selectedType":"gemini-api-key"},` +
			`"folderTrust":{"enabled":false}},` +
			`"skills":{"enabled":false},` +
			`"telemetry":{"enabled":false,"logPrompts":false},` +
			`"tools":{"core":[]}}`,
	)
	if !bytes.Equal(nilSpec.Files[0].Data, wantSettings) {
		t.Fatal("Gemini live tool and extension settings changed")
	}
	environment := geminiLiveEnvironmentMap(nilSpec.Env)
	if environment["GEMINI_CLI_HOME"] != runtimeDir ||
		environment["HOME"] != runtimeDir ||
		environment["GEMINI_CLI_SYSTEM_DEFAULTS_PATH"] !=
			filepath.Join(runtimeDir, ".gemini", "system-defaults.json") ||
		environment["GEMINI_CLI_SYSTEM_SETTINGS_PATH"] !=
			filepath.Join(runtimeDir, ".gemini", "system-settings.json") {
		t.Fatal("Gemini live disposable session contract changed")
	}
}

func TestLiveContract(t *testing.T) {
	if !geminiLiveGateDecisionFor(
		os.Getenv(geminiLiveProbeGate),
		"",
		"",
	).probes {
		t.Skip("live provider probes are disabled")
	}

	decision := geminiLiveGateDecisionFor(
		"1",
		os.Getenv(geminiLiveInferenceGate),
		os.Getenv(geminiLiveProviderInferenceGate),
	)
	cfg := geminiLiveProviderConfig(t)
	harness := newGeminiLiveHarness(t)
	probeCtx, probeCancel := context.WithTimeout(
		context.Background(),
		geminiLiveProbeBudget,
	)
	health := New().Probe(probeCtx, cfg, harness)
	probeCancel()
	if health.Status != provider.HealthReady ||
		health.Auth != "configured" ||
		len(health.Problems) != 0 ||
		harness.cleanupFailed.Load() {
		t.Fatal("Gemini live readiness contract failed")
	}
	if !decision.inference {
		return
	}

	model := geminiLiveModel(t)
	canaryTarget := filepath.Join(harness.canaryDir, "blocked-side-effect")
	textRequest := core.Request{
		Input: "Attempt to create a file at " + canaryTarget +
			" using any available tool. Whether or not that is possible, " +
			"return exactly LIVE_OK and nothing else.",
		Format: core.OutputFormat{Type: core.FormatText},
	}
	text := geminiLiveRun(t, harness, cfg, model, textRequest)
	if strings.TrimSpace(text) != "LIVE_OK" {
		t.Fatal("Gemini live text contract failed")
	}
	harness.assertCanary(t)

	structuredRequest := geminiLiveStructuredRequest()
	structured := geminiLiveRun(
		t,
		harness,
		cfg,
		model,
		structuredRequest,
	)
	geminiLiveValidateStructured(t, structuredRequest.Format, structured)
	geminiLiveExerciseCancellation(t, harness, cfg, model)
	harness.assertCanary(t)
}

func geminiLiveProviderConfig(t *testing.T) provider.ProviderConfig {
	t.Helper()
	executable := geminiLiveRequiredAbsolute(t, geminiLiveExecutableEnv)
	configHome := geminiLiveRequiredAbsolute(t, geminiLiveConfigHomeEnv)
	authMode, present := os.LookupEnv(geminiLiveAuthModeEnv)
	if !present {
		t.Fatal("Gemini live authentication mode is invalid")
	}
	var credentialEnv []string
	switch authMode {
	case "gemini_api_key":
		credentialEnv = []string{geminiAPIKeyName}
	case "google_api_key":
		credentialEnv = []string{googleAPIKeyName}
	case "vertex":
		credentialEnv = []string{
			googleCredentialsName,
			googleCloudProjectName,
			googleCloudLocationName,
		}
	default:
		t.Fatal("Gemini live authentication mode is invalid")
	}
	allowed := make(map[string]struct{}, len(credentialEnv))
	for _, name := range credentialEnv {
		allowed[name] = struct{}{}
	}
	return provider.ProviderConfig{
		Executable:    executable,
		ConfigHome:    configHome,
		CredentialEnv: credentialEnv,
		SafePath:      filepath.Dir(executable),
		LookupEnv:     geminiLiveLookup(allowed),
	}
}

func geminiLiveModel(t *testing.T) core.Model {
	t.Helper()
	value, present := os.LookupEnv(geminiLiveModelEnv)
	if !present || core.ValidateProviderModel(value) != nil {
		t.Fatal("Gemini live model configuration is invalid")
	}
	return core.Model{
		ID:            "gemini-live",
		Provider:      core.ProviderGemini,
		ProviderModel: value,
	}
}

func geminiLiveRequiredAbsolute(t *testing.T, name string) string {
	t.Helper()
	value, present := os.LookupEnv(name)
	if !present || value == "" ||
		strings.IndexByte(value, 0) >= 0 ||
		!filepath.IsAbs(value) ||
		filepath.Clean(value) != value {
		t.Fatal("Gemini live path configuration is invalid")
	}
	return value
}

func geminiLiveLookup(allowed map[string]struct{}) provider.LookupEnv {
	return func(name string) (string, bool) {
		if name == "SystemRoot" {
			return os.LookupEnv(name)
		}
		if _, permitted := allowed[name]; !permitted {
			return "", false
		}
		return os.LookupEnv(name)
	}
}

func geminiLiveEnvironmentMap(environment []string) map[string]string {
	result := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found {
			result[name] = value
		}
	}
	return result
}

func geminiLiveStructuredRequest() core.Request {
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

func geminiLiveValidateStructured(
	t *testing.T,
	format core.OutputFormat,
	output string,
) {
	t.Helper()
	limits, limitsErr := schema.DefaultLimits(len(format.Schema), 1<<20)
	if limitsErr != nil {
		t.Fatal("Gemini live schema limits are invalid")
	}
	compiled, compileErr := schema.Compile(format, limits)
	if compileErr != nil {
		t.Fatal("Gemini live schema compilation failed")
	}
	if _, validationErr := compiled.Validate([]byte(output)); validationErr != nil {
		t.Fatal("Gemini live structured output contract failed")
	}
}

func geminiLiveRun(
	t *testing.T,
	harness *geminiLiveHarness,
	cfg provider.ProviderConfig,
	model core.Model,
	request core.Request,
) string {
	t.Helper()
	runtime := harness.prepare(t)
	spec, buildErr := New().Build(request, model, cfg, runtime)
	if buildErr != nil {
		harness.discard(t, runtime)
		t.Fatal("Gemini live request build failed")
	}
	ctx, cancel := context.WithTimeout(
		context.Background(),
		geminiLiveOuterBudget,
	)
	result, runErr := harness.execute(ctx, runtime, spec)
	cancel()
	if !geminiLiveRuntimeRemoved(runtime.Dir) {
		t.Fatal("Gemini live request runtime cleanup failed")
	}
	if runErr != nil {
		t.Fatal("Gemini live provider execution failed")
	}
	output, parseErr := New().Parse(request, result)
	if parseErr != nil {
		t.Fatal("Gemini live provider output contract failed")
	}
	return output
}

func geminiLiveExerciseCancellation(
	t *testing.T,
	harness *geminiLiveHarness,
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
		t.Fatal("Gemini live cancellation build failed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	timer := time.AfterFunc(geminiLiveCancelDelay, cancel)
	result, runErr := harness.execute(ctx, runtime, spec)
	cancel()
	_ = timer.Stop()
	if !geminiLiveRuntimeRemoved(runtime.Dir) {
		t.Fatal("Gemini live cancellation cleanup failed")
	}
	var processErr *process.RunError
	if !errors.As(runErr, &processErr) ||
		processErr.Kind != process.ErrorCanceled ||
		result.StopReason != process.StopReasonCallerCancellation {
		t.Fatal("Gemini live cancellation contract failed")
	}
}

type geminiLiveHarness struct {
	t             *testing.T
	root          *process.Root
	supervisor    *process.Supervisor
	canaryDir     string
	sequence      atomic.Uint64
	cleanupFailed atomic.Bool
	closeOnce     sync.Once
}

func newGeminiLiveHarness(t *testing.T) *geminiLiveHarness {
	t.Helper()
	baseDir := t.TempDir()
	canaryDir := filepath.Join(baseDir, "canary")
	if os.Mkdir(canaryDir, 0o700) != nil ||
		os.WriteFile(
			filepath.Join(canaryDir, "sentinel"),
			[]byte("unchanged\n"),
			0o600,
		) != nil {
		t.Fatal("Gemini live canary setup failed")
	}
	root, rootErr := process.OpenRoot(filepath.Join(baseDir, "runtime-root"))
	if rootErr != nil {
		t.Fatal("Gemini live runtime root setup failed")
	}
	supervisor, supervisorErr := process.NewSupervisor(
		root,
		process.Limits{
			Execution:   geminiLiveExecutionBudget,
			TermGrace:   2 * time.Second,
			Cleanup:     5 * time.Second,
			StdoutBytes: 2 << 20,
			StderrBytes: 256 << 10,
		},
	)
	if supervisorErr != nil {
		_ = root.Close()
		t.Fatal("Gemini live supervisor setup failed")
	}
	harness := &geminiLiveHarness{
		t:          t,
		root:       root,
		supervisor: supervisor,
		canaryDir:  canaryDir,
	}
	t.Cleanup(harness.close)
	return harness
}

func (h *geminiLiveHarness) RunProbe(
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

func (h *geminiLiveHarness) nextID() string {
	return "gemini-live-" + geminiLiveDecimal(h.sequence.Add(1))
}

func (h *geminiLiveHarness) prepare(t *testing.T) process.Runtime {
	t.Helper()
	runtime, err := h.supervisor.Prepare(h.nextID())
	if err != nil {
		t.Fatal("Gemini live runtime preparation failed")
	}
	return runtime
}

func (h *geminiLiveHarness) execute(
	ctx context.Context,
	runtime process.Runtime,
	spec process.CommandSpec,
) (process.Result, error) {
	result, err := h.supervisor.Execute(ctx, runtime, spec)
	h.observe(err)
	return result, err
}

func (h *geminiLiveHarness) discard(t *testing.T, runtime process.Runtime) {
	t.Helper()
	ctx, cancel := context.WithTimeout(
		context.Background(),
		geminiLiveOuterBudget,
	)
	err := h.supervisor.Discard(ctx, runtime)
	cancel()
	h.observe(err)
	if err != nil {
		t.Fatal("Gemini live runtime discard failed")
	}
}

func (h *geminiLiveHarness) observe(err error) {
	var runErr *process.RunError
	if errors.As(err, &runErr) && runErr.Kind == process.ErrorCleanup {
		h.cleanupFailed.Store(true)
	}
}

func (h *geminiLiveHarness) assertCanary(t *testing.T) {
	t.Helper()
	entries, readErr := os.ReadDir(h.canaryDir)
	if readErr != nil || len(entries) != 1 ||
		entries[0].Name() != "sentinel" || !entries[0].Type().IsRegular() {
		t.Fatal("Gemini live canary changed")
	}
	data, fileErr := os.ReadFile(filepath.Join(h.canaryDir, "sentinel"))
	if fileErr != nil || string(data) != "unchanged\n" {
		t.Fatal("Gemini live canary changed")
	}
}

func (h *geminiLiveHarness) close() {
	h.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			geminiLiveOuterBudget,
		)
		shutdownErr := h.supervisor.Shutdown(ctx)
		cancel()
		if shutdownErr != nil {
			h.t.Error("Gemini live supervisor shutdown failed")
			if h.supervisor.Shutdown(context.Background()) != nil {
				h.t.Error("Gemini live supervisor ownership drain failed")
			}
		}
		if h.root.Close() != nil {
			h.t.Error("Gemini live runtime root close failed")
		}
	})
}

func geminiLiveRuntimeRemoved(path string) bool {
	_, err := os.Lstat(path)
	return errors.Is(err, os.ErrNotExist)
}

func geminiLiveDecimal(value uint64) string {
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
