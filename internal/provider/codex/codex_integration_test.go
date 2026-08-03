//go:build integration

package codex

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
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
	codexIntegrationExecutionBudget  = 10 * time.Second
	codexIntegrationTerminationGrace = 100 * time.Millisecond
	codexIntegrationCleanupBudget    = time.Second

	// The outer caller deadline must leave scheduling margin after execution,
	// termination grace, and cleanup complete.
	codexIntegrationOuterDeadline = 15 * time.Second
)

func TestCodexAdapterFakeCLIIntegration(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)

	t.Run("text prompt reaches stdin exactly", func(t *testing.T) {
		harness := newCodexIntegrationHarness(
			t,
			integrationLimits(),
		)
		requestRuntime := harness.prepare("codex-text-0001")
		instructions := "한국어\n--model attacker-model\nFAKE HEADER"
		request := core.Request{
			Instructions: &instructions,
			Input:        "-leading\nprivate input\n\"quoted\"",
			Format:       core.OutputFormat{Type: core.FormatText},
		}
		cfg := integrationProviderConfig(
			executable,
			"echo-stdin",
			filepath.Join(t.TempDir(), "codex-home"),
		)
		spec, err := New().Build(
			request,
			integrationModel(),
			cfg,
			requestRuntime,
		)
		if err != nil {
			t.Fatal(err)
		}
		wantPrompt := provider.BuildPrompt(request, provider.SchemaFile)
		if !bytes.Equal(spec.Stdin, wantPrompt) {
			t.Fatalf("Stdin=%q, want %q", spec.Stdin, wantPrompt)
		}
		joinedArgs := strings.Join(spec.Args, "\x00")
		for _, forbidden := range []string{
			instructions,
			request.Input,
			"--model attacker-model",
		} {
			if strings.Contains(joinedArgs, forbidden) {
				t.Fatalf("request changed argv with %q", forbidden)
			}
		}

		result, runErr := harness.execute(requestRuntime, spec)
		if runErr != nil {
			t.Fatal(runErr)
		}
		got, parseErr := New().Parse(request, result)
		if parseErr != nil || got != string(wantPrompt) {
			t.Fatalf(
				"Parse()=(%q,%v), want exact stdin",
				got,
				parseErr,
			)
		}
	})

	t.Run("structured response and exact schema FileSpec", func(t *testing.T) {
		harness := newCodexIntegrationHarness(
			t,
			integrationLimits(),
		)
		requestRuntime := harness.prepare("codex-json-0002")
		schema := []byte(
			`{"type":"object","properties":{"answer":{"type":"string"}}}`,
		)
		request := core.Request{
			Input: "private structured input",
			Format: core.OutputFormat{
				Type:   core.FormatJSONSchema,
				Name:   "answer",
				Schema: schema,
			},
		}
		cfg := integrationProviderConfig(
			executable,
			"codex-json",
			filepath.Join(t.TempDir(), "codex-home"),
		)
		spec, err := New().Build(
			request,
			integrationModel(),
			cfg,
			requestRuntime,
		)
		if err != nil {
			t.Fatal(err)
		}
		wantFile := process.FileSpec{
			Name: "output-schema.json",
			Data: schema,
			Mode: 0o600,
		}
		if len(spec.Files) != 1 ||
			!reflect.DeepEqual(spec.Files[0], wantFile) {
			t.Fatalf("Files=%+v, want %+v", spec.Files, wantFile)
		}
		wantSchemaPath := filepath.Join(
			requestRuntime.Dir,
			"output-schema.json",
		)
		if got := spec.Args[len(spec.Args)-3:]; !reflect.DeepEqual(
			got,
			[]string{"--output-schema", wantSchemaPath, "-"},
		) {
			t.Fatalf("schema argv tail=%q", got)
		}

		result, runErr := harness.execute(requestRuntime, spec)
		if runErr != nil {
			t.Fatal(runErr)
		}
		got, parseErr := New().Parse(request, result)
		if parseErr != nil || got != `{"answer":"hello"}`+"\n" {
			t.Fatalf("Parse()=(%q,%v)", got, parseErr)
		}
	})

	t.Run("schema materialization and stdin are consumed live", func(t *testing.T) {
		harness := newCodexIntegrationHarness(
			t,
			integrationLimits(),
		)
		requestRuntime := harness.prepare("codex-schema-0003")
		schema := []byte(
			`{"private_schema_marker_1003":{"type":"string"}}`,
		)
		request := core.Request{
			Input: "private prompt marker 1004",
			Format: core.OutputFormat{
				Type:   core.FormatJSONSchema,
				Name:   "private-name-1005",
				Schema: schema,
			},
		}
		cfg := integrationProviderConfig(
			executable,
			"codex-schema-probe",
			filepath.Join(t.TempDir(), "codex-home"),
		)
		spec, err := New().Build(
			request,
			integrationModel(),
			cfg,
			requestRuntime,
		)
		if err != nil {
			t.Fatal(err)
		}
		result, runErr := harness.execute(requestRuntime, spec)
		if runErr != nil {
			t.Fatal(runErr)
		}
		got, parseErr := New().Parse(request, result)
		want := fmt.Sprintf(
			"{\"stdin_bytes\":%d,\"schema_bytes\":%d}\n",
			len(spec.Stdin),
			len(schema),
		)
		if parseErr != nil || got != want {
			t.Fatalf("Parse()=(%q,%v), want %q", got, parseErr, want)
		}
		for _, forbidden := range []string{
			request.Input,
			string(schema),
			request.Format.Name,
		} {
			if strings.Contains(got, forbidden) {
				t.Fatalf("schema probe exposed %q: %q", forbidden, got)
			}
		}
	})

	t.Run("provider output classifications", func(t *testing.T) {
		tests := []struct {
			name     string
			mode     string
			category provider.ErrorCategory
		}{
			{
				name:     "empty success",
				mode:     "empty-success",
				category: provider.ProviderErrorProtocol,
			},
			{
				name:     "invalid UTF-8 success",
				mode:     "invalid-utf8",
				category: provider.ProviderErrorProtocol,
			},
			{
				name:     "nonzero",
				mode:     "exit-7",
				category: provider.ProviderErrorFailed,
			},
		}
		harness := newCodexIntegrationHarness(
			t,
			integrationLimits(),
		)
		for index, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				requestRuntime := harness.prepare(
					fmt.Sprintf("codex-error-%04d", index),
				)
				request := core.Request{
					Input:  "planted integration prompt",
					Format: core.OutputFormat{Type: core.FormatText},
				}
				cfg := integrationProviderConfig(
					executable,
					test.mode,
					filepath.Join(t.TempDir(), "codex-home"),
				)
				spec, err := New().Build(
					request,
					integrationModel(),
					cfg,
					requestRuntime,
				)
				if err != nil {
					t.Fatal(err)
				}
				result, runErr := harness.execute(requestRuntime, spec)
				if runErr != nil {
					t.Fatal(runErr)
				}
				_, parseErr := New().Parse(request, result)
				assertIntegrationProviderCategory(
					t,
					parseErr,
					test.category,
				)
			})
		}
	})

	t.Run("timeout remains a process error", func(t *testing.T) {
		limits := integrationLimits()
		limits.Execution = 100 * time.Millisecond
		harness := newCodexIntegrationHarness(t, limits)
		requestRuntime := harness.prepare("codex-timeout-0004")
		cfg := integrationProviderConfig(
			executable,
			"hang",
			filepath.Join(t.TempDir(), "codex-home"),
		)
		spec, err := New().Build(
			core.Request{Format: core.OutputFormat{Type: core.FormatText}},
			integrationModel(),
			cfg,
			requestRuntime,
		)
		if err != nil {
			t.Fatal(err)
		}
		_, runErr := harness.execute(requestRuntime, spec)
		assertIntegrationRunErrorKind(t, runErr, process.ErrorTimeout)
	})

	for _, flood := range []struct {
		name string
		mode string
	}{
		{name: "stdout overflow", mode: "flood-stdout"},
		{name: "stderr overflow", mode: "flood-stderr"},
	} {
		t.Run(flood.name+" remains a process error", func(t *testing.T) {
			limits := integrationLimits()
			limits.StdoutBytes = 1024
			limits.StderrBytes = 1024
			harness := newCodexIntegrationHarness(t, limits)
			requestRuntime := harness.prepare(
				"codex-" + strings.ReplaceAll(flood.name, " ", "-"),
			)
			cfg := integrationProviderConfig(
				executable,
				flood.mode,
				filepath.Join(t.TempDir(), "codex-home"),
			)
			spec, err := New().Build(
				core.Request{
					Format: core.OutputFormat{Type: core.FormatText},
				},
				integrationModel(),
				cfg,
				requestRuntime,
			)
			if err != nil {
				t.Fatal(err)
			}
			_, runErr := harness.execute(requestRuntime, spec)
			assertIntegrationRunErrorKind(
				t,
				runErr,
				process.ErrorOutputLimit,
			)
		})
	}

	t.Run("build failure is explicitly discarded", func(t *testing.T) {
		harness := newCodexIntegrationHarness(
			t,
			integrationLimits(),
		)
		requestRuntime := harness.prepare("codex-discard-0005")
		model := integrationModel()
		model.ProviderModel = "--attacker-model"
		_, err := New().Build(
			core.Request{Format: core.OutputFormat{Type: core.FormatText}},
			model,
			integrationProviderConfig(
				executable,
				"echo-stdin",
				filepath.Join(t.TempDir(), "codex-home"),
			),
			requestRuntime,
		)
		if err == nil {
			t.Fatal("Build unexpectedly succeeded")
		}
		ctx, cancel := context.WithTimeout(
			context.Background(),
			codexIntegrationOuterDeadline,
		)
		defer cancel()
		if discardErr := harness.supervisor.Discard(
			ctx,
			requestRuntime,
		); discardErr != nil {
			t.Fatal(discardErr)
		}
		assertRuntimeAbsent(t, requestRuntime.Dir)
	})

	t.Run("shutdown drains before root close", func(t *testing.T) {
		harness := newCodexIntegrationHarness(
			t,
			integrationLimits(),
		)
		requestRuntime := harness.prepare("codex-shutdown-0006")
		cfg := integrationProviderConfig(
			executable,
			"text",
			filepath.Join(t.TempDir(), "codex-home"),
		)
		spec, err := New().Build(
			core.Request{Format: core.OutputFormat{Type: core.FormatText}},
			integrationModel(),
			cfg,
			requestRuntime,
		)
		if err != nil {
			t.Fatal(err)
		}
		if _, runErr := harness.execute(requestRuntime, spec); runErr != nil {
			t.Fatal(runErr)
		}
		harness.close()
		if _, prepareErr := harness.supervisor.Prepare(
			"codex-after-shutdown",
		); prepareErr == nil {
			t.Fatal("supervisor accepted work after Shutdown")
		}
	})
}

type codexIntegrationHarness struct {
	t          *testing.T
	root       *process.Root
	supervisor *process.Supervisor
	closeOnce  sync.Once
}

func newCodexIntegrationHarness(
	t *testing.T,
	limits process.Limits,
) *codexIntegrationHarness {
	t.Helper()
	root, err := process.OpenRoot(
		filepath.Join(t.TempDir(), "runtime-root"),
	)
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := process.NewSupervisor(root, limits)
	if err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	harness := &codexIntegrationHarness{
		t:          t,
		root:       root,
		supervisor: supervisor,
	}
	t.Cleanup(harness.close)
	return harness
}

func (h *codexIntegrationHarness) prepare(id string) process.Runtime {
	h.t.Helper()
	requestRuntime, err := h.supervisor.Prepare(id)
	if err != nil {
		h.t.Fatal(err)
	}
	if _, err := os.Stat(requestRuntime.Dir); err != nil {
		h.t.Fatalf("prepared runtime unavailable: %v", err)
	}
	return requestRuntime
}

func (h *codexIntegrationHarness) execute(
	requestRuntime process.Runtime,
	spec process.CommandSpec,
) (process.Result, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(
		context.Background(),
		codexIntegrationOuterDeadline,
	)
	defer cancel()
	result, err := h.supervisor.Execute(ctx, requestRuntime, spec)
	assertRuntimeAbsent(h.t, requestRuntime.Dir)
	return result, err
}

func (h *codexIntegrationHarness) close() {
	h.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			codexIntegrationOuterDeadline,
		)
		defer cancel()
		if err := h.supervisor.Shutdown(ctx); err != nil {
			h.t.Errorf("Supervisor.Shutdown: %v", err)
		}
		if err := h.root.Close(); err != nil {
			h.t.Errorf("Root.Close: %v", err)
		}
	})
}

func integrationProviderConfig(
	executable string,
	mode string,
	configHome string,
) provider.ProviderConfig {
	return provider.ProviderConfig{
		Executable: executable,
		PrefixArgs: []string{"--mode=" + mode},
		ConfigHome: configHome,
		SafePath:   filepath.Dir(executable),
		LookupEnv:  os.LookupEnv,
	}
}

func integrationModel() core.Model {
	return core.Model{
		ID:            "codex-integration",
		Provider:      core.ProviderCodex,
		ProviderModel: "gpt-5.4-codex",
	}
}

func integrationLimits() process.Limits {
	return process.Limits{
		Execution:   codexIntegrationExecutionBudget,
		TermGrace:   codexIntegrationTerminationGrace,
		Cleanup:     codexIntegrationCleanupBudget,
		StdoutBytes: 1 << 20,
		StderrBytes: 1 << 20,
	}
}

func assertRuntimeAbsent(t *testing.T, runtimeDir string) {
	t.Helper()
	if _, err := os.Lstat(runtimeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime still exists: path=%q error=%v", runtimeDir, err)
	}
}

func assertIntegrationProviderCategory(
	t *testing.T,
	err error,
	want provider.ErrorCategory,
) {
	t.Helper()
	var providerErr *provider.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error=%v (%T), want provider error", err, err)
	}
	if providerErr.Category() != want {
		t.Fatalf("Category()=%q, want %q", providerErr.Category(), want)
	}
}

func assertIntegrationRunErrorKind(
	t *testing.T,
	err error,
	want process.ErrorKind,
) {
	t.Helper()
	var runErr *process.RunError
	if !errors.As(err, &runErr) {
		t.Fatalf("error=%v (%T), want process RunError", err, err)
	}
	if runErr.Kind != want {
		t.Fatalf("RunError.Kind=%q, want %q", runErr.Kind, want)
	}
	var providerErr *provider.ProviderError
	if errors.As(err, &providerErr) {
		t.Fatalf("process error was reclassified: %v", err)
	}
}
