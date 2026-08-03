//go:build integration

package gemini

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

const (
	geminiIntegrationExecutionBudget  = 10 * time.Second
	geminiIntegrationTerminationGrace = 100 * time.Millisecond
	geminiIntegrationCleanupBudget    = time.Second
	geminiIntegrationOuterDeadline    = 15 * time.Second
)

func TestGeminiAdapterFakeCLIIntegration(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	profiles := []struct {
		name        string
		credentials []string
		values      map[string]string
		authType    string
	}{
		{
			name:        "Gemini API key",
			credentials: []string{geminiAPIKeyName},
			values:      map[string]string{geminiAPIKeyName: "gemini-integration-secret"},
			authType:    geminiAPIKeyAuthType,
		},
		{
			name:        "Google API key",
			credentials: []string{googleAPIKeyName},
			values:      map[string]string{googleAPIKeyName: "google-integration-secret"},
			authType:    vertexAIAuthType,
		},
		{
			name: "service account",
			credentials: []string{
				googleCloudLocationName,
				googleCredentialsName,
				googleCloudProjectName,
			},
			values: map[string]string{
				googleCredentialsName:   filepath.Join(t.TempDir(), "not-opened.json"),
				googleCloudProjectName:  "integration-project",
				googleCloudLocationName: "integration-location",
			},
			authType: vertexAIAuthType,
		},
	}
	requests := []struct {
		name    string
		request core.Request
	}{
		{
			name: "text",
			request: core.Request{
				Instructions: integrationStringPointer("private text instructions"),
				Input:        "private text input\n--model attacker",
				Format:       core.OutputFormat{Type: core.FormatText},
			},
		},
		{
			name: "inline schema",
			request: core.Request{
				Instructions: integrationStringPointer("private schema instructions"),
				Input:        "private schema input",
				Format: core.OutputFormat{
					Type:        core.FormatJSONSchema,
					Name:        "private-schema-name",
					Description: integrationStringPointer("private schema description"),
					Schema:      []byte(`{"type":"object","properties":{"private":{"type":"string"}}}`),
				},
			},
		},
	}

	for profileIndex, profile := range profiles {
		for requestIndex, requestCase := range requests {
			t.Run(profile.name+"/"+requestCase.name, func(t *testing.T) {
				harness := newGeminiIntegrationHarness(t, geminiIntegrationLimits())
				requestRuntime := harness.prepare(fmt.Sprintf(
					"gemini-prompt-%d-%d",
					profileIndex,
					requestIndex,
				))
				cfg := geminiIntegrationProviderConfig(
					executable,
					"gemini-stdin-probe",
					profile.credentials,
					profile.values,
				)
				spec, err := New().Build(
					requestCase.request,
					geminiIntegrationModel(),
					cfg,
					requestRuntime,
				)
				if err != nil {
					t.Fatal(err)
				}
				wantPrompt := provider.BuildPrompt(requestCase.request, provider.SchemaInline)
				if !bytes.Equal(spec.Stdin, wantPrompt) {
					t.Fatalf("Stdin differs: got=%d want=%d", len(spec.Stdin), len(wantPrompt))
				}
				if len(spec.Files) != 1 ||
					spec.Files[0].Name != filepath.Join(".gemini", "settings.json") ||
					spec.Files[0].Mode != 0o600 {
					t.Fatalf("Files=%+v", spec.Files)
				}
				assertExactBuildEnvironment(
					t,
					spec.Env,
					cfg,
					requestRuntime.Dir,
					profile.values,
				)
				for _, entry := range spec.Env {
					for _, forbidden := range []string{
						"gateway-secret",
						"codex-secret",
						"claude-secret",
						"proxy-secret",
						"ca-secret",
						"selector-secret",
						"identity@example.test",
					} {
						if strings.Contains(entry, forbidden) {
							t.Fatal("unselected ambient value reached child environment")
						}
					}
				}
				result, runErr := harness.execute(requestRuntime, spec)
				if runErr != nil {
					t.Fatal(runErr)
				}
				got, parseErr := New().Parse(requestCase.request, result)
				want := fmt.Sprintf(
					"stdin_bytes=%d auth_type=%s settings_secure=true",
					len(wantPrompt),
					profile.authType,
				)
				if parseErr != nil || got != want {
					t.Fatalf("Parse()=(%q,%v), want (%q,nil)", got, parseErr, want)
				}
				for _, sensitive := range append(
					integrationRequestStrings(requestCase.request),
					integrationMapValues(profile.values)...,
				) {
					if sensitive != "" && strings.Contains(got, sensitive) {
						t.Fatalf("probe exposed sensitive value %q", sensitive)
					}
				}
			})
		}
	}

	t.Run("success and fenced response preserve only response", func(t *testing.T) {
		tests := []struct {
			mode string
			want string
		}{
			{mode: "gemini-json", want: "hello"},
			{mode: "gemini-fenced-response", want: "```json\n{\"answer\":\"hello\"}\n```"},
		}
		for index, test := range tests {
			harness := newGeminiIntegrationHarness(t, geminiIntegrationLimits())
			rt := harness.prepare(fmt.Sprintf("gemini-success-%d", index))
			request := core.Request{Input: "private", Format: core.OutputFormat{Type: core.FormatText}}
			spec := mustGeminiIntegrationBuild(t, executable, test.mode, rt, request)
			result, runErr := harness.execute(rt, spec)
			if runErr != nil {
				t.Fatal(runErr)
			}
			got, parseErr := New().Parse(request, result)
			if parseErr != nil || got != test.want {
				t.Fatalf("Parse()=(%q,%v), want (%q,nil)", got, parseErr, test.want)
			}
		}
	})

	t.Run("provider output classifications", func(t *testing.T) {
		tests := []struct {
			name     string
			mode     string
			category provider.ErrorCategory
		}{
			{name: "exit-zero explicit error", mode: "gemini-error", category: provider.ProviderErrorFailed},
			{name: "duplicate response", mode: "gemini-duplicate-json", category: provider.ProviderErrorProtocol},
			{name: "malformed", mode: "invalid-json", category: provider.ProviderErrorProtocol},
			{name: "invalid UTF-8", mode: "invalid-utf8", category: provider.ProviderErrorProtocol},
			{name: "generic nonzero", mode: "exit-7", category: provider.ProviderErrorFailed},
		}
		harness := newGeminiIntegrationHarness(t, geminiIntegrationLimits())
		for index, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				rt := harness.prepare(fmt.Sprintf("gemini-output-%d", index))
				request := core.Request{Format: core.OutputFormat{Type: core.FormatText}}
				spec := mustGeminiIntegrationBuild(t, executable, test.mode, rt, request)
				result, runErr := harness.execute(rt, spec)
				if runErr != nil {
					t.Fatal(runErr)
				}
				_, parseErr := New().Parse(request, result)
				assertProviderCategory(t, parseErr, test.category)
			})
		}
	})

	t.Run("timeout remains process error", func(t *testing.T) {
		limits := geminiIntegrationLimits()
		limits.Execution = 100 * time.Millisecond
		harness := newGeminiIntegrationHarness(t, limits)
		rt := harness.prepare("gemini-timeout")
		spec := mustGeminiIntegrationBuild(
			t,
			executable,
			"hang",
			rt,
			core.Request{Format: core.OutputFormat{Type: core.FormatText}},
		)
		_, runErr := harness.execute(rt, spec)
		assertIntegrationRunErrorKind(t, runErr, process.ErrorTimeout)
	})

	for _, flood := range []struct {
		name string
		mode string
	}{
		{name: "stdout", mode: "flood-stdout"},
		{name: "stderr", mode: "flood-stderr"},
	} {
		t.Run(flood.name+" overflow remains process error", func(t *testing.T) {
			limits := geminiIntegrationLimits()
			limits.StdoutBytes = 1024
			limits.StderrBytes = 1024
			harness := newGeminiIntegrationHarness(t, limits)
			rt := harness.prepare("gemini-flood-" + flood.name)
			spec := mustGeminiIntegrationBuild(
				t,
				executable,
				flood.mode,
				rt,
				core.Request{Format: core.OutputFormat{Type: core.FormatText}},
			)
			_, runErr := harness.execute(rt, spec)
			assertIntegrationRunErrorKind(t, runErr, process.ErrorOutputLimit)
		})
	}

	t.Run("build failure is explicitly discarded", func(t *testing.T) {
		harness := newGeminiIntegrationHarness(t, geminiIntegrationLimits())
		rt := harness.prepare("gemini-discard")
		model := geminiIntegrationModel()
		model.ProviderModel = "--attacker-model"
		_, err := New().Build(
			core.Request{Format: core.OutputFormat{Type: core.FormatText}},
			model,
			geminiIntegrationProviderConfig(
				executable,
				"gemini-json",
				[]string{geminiAPIKeyName},
				map[string]string{geminiAPIKeyName: "secret"},
			),
			rt,
		)
		if err == nil {
			t.Fatal("Build unexpectedly succeeded")
		}
		ctx, cancel := context.WithTimeout(context.Background(), geminiIntegrationOuterDeadline)
		defer cancel()
		if err := harness.supervisor.Discard(ctx, rt); err != nil {
			t.Fatal(err)
		}
		assertIntegrationRuntimeAbsent(t, rt.Dir)
	})

	t.Run("shutdown drains before root close", func(t *testing.T) {
		harness := newGeminiIntegrationHarness(t, geminiIntegrationLimits())
		rt := harness.prepare("gemini-shutdown")
		spec := mustGeminiIntegrationBuild(
			t,
			executable,
			"gemini-wait-release",
			rt,
			core.Request{Format: core.OutputFormat{Type: core.FormatText}},
		)
		type executionOutcome struct {
			result process.Result
			err    error
		}
		execution := make(chan executionOutcome, 1)
		executionCtx, executionCancel := context.WithTimeout(
			context.Background(),
			geminiIntegrationOuterDeadline,
		)
		defer executionCancel()
		go func() {
			result, err := harness.supervisor.Execute(executionCtx, rt, spec)
			execution <- executionOutcome{result: result, err: err}
		}()
		waitForIntegrationMarker(t, filepath.Join(rt.Dir, ".fake-ready"))

		shutdown := make(chan error, 1)
		shutdownCtx, shutdownCancel := context.WithTimeout(
			context.Background(),
			geminiIntegrationOuterDeadline,
		)
		defer shutdownCancel()
		go func() {
			shutdown <- harness.supervisor.Shutdown(shutdownCtx)
		}()
		select {
		case err := <-shutdown:
			t.Fatalf("Shutdown returned before active execution drained: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		if err := os.WriteFile(
			filepath.Join(rt.Dir, ".fake-release"),
			[]byte("release\n"),
			0o600,
		); err != nil {
			t.Fatal(err)
		}
		outcome := <-execution
		if outcome.err != nil {
			t.Fatal(outcome.err)
		}
		if got, err := New().Parse(core.Request{}, outcome.result); err != nil || got != "hello" {
			t.Fatalf("drained Parse()=(%q,%v), want (hello,nil)", got, err)
		}
		assertIntegrationRuntimeAbsent(t, rt.Dir)
		if err := <-shutdown; err != nil {
			t.Fatal(err)
		}
		harness.close()
		if _, err := harness.supervisor.Prepare("gemini-after-shutdown"); err == nil {
			t.Fatal("supervisor accepted work after shutdown")
		}
	})
}

func TestGeminiAdapterSatisfiesProviderInterface(t *testing.T) {
	var adapter provider.Adapter = New()
	if adapter.Name() != core.ProviderGemini {
		t.Fatalf("Name()=%q", adapter.Name())
	}
}

type geminiIntegrationHarness struct {
	t          *testing.T
	root       *process.Root
	supervisor *process.Supervisor
	closeOnce  sync.Once
}

func newGeminiIntegrationHarness(t *testing.T, limits process.Limits) *geminiIntegrationHarness {
	t.Helper()
	root, err := process.OpenRoot(filepath.Join(t.TempDir(), "runtime-root"))
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := process.NewSupervisor(root, limits)
	if err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	harness := &geminiIntegrationHarness{t: t, root: root, supervisor: supervisor}
	t.Cleanup(harness.close)
	return harness
}

func (h *geminiIntegrationHarness) prepare(id string) process.Runtime {
	h.t.Helper()
	rt, err := h.supervisor.Prepare(id)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := os.Stat(rt.Dir); err != nil {
		h.t.Fatal(err)
	}
	return rt
}

func (h *geminiIntegrationHarness) execute(
	rt process.Runtime,
	spec process.CommandSpec,
) (process.Result, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), geminiIntegrationOuterDeadline)
	defer cancel()
	result, err := h.supervisor.Execute(ctx, rt, spec)
	assertIntegrationRuntimeAbsent(h.t, rt.Dir)
	return result, err
}

func (h *geminiIntegrationHarness) close() {
	h.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), geminiIntegrationOuterDeadline)
		defer cancel()
		if err := h.supervisor.Shutdown(ctx); err != nil {
			h.t.Errorf("Supervisor.Shutdown: %v", err)
		}
		if err := h.root.Close(); err != nil {
			h.t.Errorf("Root.Close: %v", err)
		}
	})
}

func geminiIntegrationProviderConfig(
	executable string,
	mode string,
	credentials []string,
	values map[string]string,
) provider.ProviderConfig {
	lookup := map[string]string{
		"SystemRoot":                `C:\Windows`,
		"AI_CLI_GATEWAY_API_KEY":    "gateway-secret",
		"OPENAI_API_KEY":            "codex-secret",
		"ANTHROPIC_API_KEY":         "claude-secret",
		"HTTPS_PROXY":               "proxy-secret",
		"SSL_CERT_FILE":             "ca-secret",
		"GOOGLE_GENAI_USE_VERTEXAI": "selector-secret",
		"USER_IDENTITY":             "identity@example.test",
	}
	for name, value := range values {
		lookup[name] = value
	}
	return provider.ProviderConfig{
		Executable:    executable,
		PrefixArgs:    []string{"--mode=" + mode},
		ConfigHome:    filepath.Join(filepath.Dir(executable), "persistent-home-must-not-be-used"),
		CredentialEnv: append([]string(nil), credentials...),
		SafePath:      filepath.Dir(executable),
		LookupEnv: func(name string) (string, bool) {
			value, ok := lookup[name]
			return value, ok
		},
	}
}

func mustGeminiIntegrationBuild(
	t *testing.T,
	executable string,
	mode string,
	rt process.Runtime,
	request core.Request,
) process.CommandSpec {
	t.Helper()
	spec, err := New().Build(
		request,
		geminiIntegrationModel(),
		geminiIntegrationProviderConfig(
			executable,
			mode,
			[]string{geminiAPIKeyName},
			map[string]string{geminiAPIKeyName: "integration-secret"},
		),
		rt,
	)
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func geminiIntegrationModel() core.Model {
	return core.Model{ID: "gemini-integration", Provider: core.ProviderGemini, ProviderModel: testModel}
}

func geminiIntegrationLimits() process.Limits {
	return process.Limits{
		Execution:   geminiIntegrationExecutionBudget,
		TermGrace:   geminiIntegrationTerminationGrace,
		Cleanup:     geminiIntegrationCleanupBudget,
		StdoutBytes: 1 << 20,
		StderrBytes: 1 << 20,
	}
}

func integrationStringPointer(value string) *string {
	return &value
}

func integrationRequestStrings(request core.Request) []string {
	values := []string{request.ModelAlias, request.Input, request.Format.Name, string(request.Format.Schema)}
	if request.Instructions != nil {
		values = append(values, *request.Instructions)
	}
	if request.Format.Description != nil {
		values = append(values, *request.Format.Description)
	}
	return values
}

func integrationMapValues(values map[string]string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value)
	}
	return result
}

func assertIntegrationRuntimeAbsent(t *testing.T, runtimeDir string) {
	t.Helper()
	if _, err := os.Lstat(runtimeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime still exists: path=%q error=%v", runtimeDir, err)
	}
}

func assertIntegrationRunErrorKind(t *testing.T, err error, want process.ErrorKind) {
	t.Helper()
	var runErr *process.RunError
	if !errors.As(err, &runErr) || runErr.Kind != want {
		t.Fatalf("error=%v (%T), want process kind %q", err, err, want)
	}
	var providerErr *provider.ProviderError
	if errors.As(err, &providerErr) {
		t.Fatalf("process error was reclassified: %v", err)
	}
}

func waitForIntegrationMarker(t *testing.T, path string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Lstat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-deadline.C:
			t.Fatal("timed out waiting for fixed fake readiness marker")
		case <-ticker.C:
		}
	}
}
