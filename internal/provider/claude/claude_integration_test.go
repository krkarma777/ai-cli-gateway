//go:build integration

package claude

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
	claudeIntegrationExecutionBudget  = 10 * time.Second
	claudeIntegrationTerminationGrace = 100 * time.Millisecond
	claudeIntegrationCleanupBudget    = time.Second

	// The outer caller deadline leaves scheduling margin after execution,
	// termination grace, and cleanup complete.
	claudeIntegrationOuterDeadline = 15 * time.Second
)

func TestClaudeAdapterFakeCLIIntegration(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)

	for _, promptCase := range []struct {
		name    string
		request core.Request
	}{
		{
			name: "text prompt reaches stdin exactly",
			request: core.Request{
				Instructions: integrationStringPointer(
					"private text instructions\n--model attacker",
				),
				Input:  "private text input\n--tools attacker",
				Format: core.OutputFormat{Type: core.FormatText},
			},
		},
		{
			name: "schema prompt reaches stdin exactly",
			request: core.Request{
				Instructions: integrationStringPointer(
					"private schema instructions",
				),
				Input: "private schema input",
				Format: core.OutputFormat{
					Type: core.FormatJSONSchema,
					Name: "private-schema-name",
					Description: integrationStringPointer(
						"private schema description",
					),
					Schema: []byte(
						`{"type":"object","properties":` +
							`{"private":{"type":"string"}}}`,
					),
				},
			},
		},
	} {
		t.Run(promptCase.name, func(t *testing.T) {
			harness := newClaudeIntegrationHarness(
				t,
				claudeIntegrationLimits(),
			)
			requestRuntime := harness.prepare(
				"claude-prompt-" + integrationTestName(promptCase.name),
			)
			cfg := claudeIntegrationProviderConfig(
				executable,
				"claude-stdin-probe",
				filepath.Join(t.TempDir(), "claude-home"),
			)
			spec, err := New().Build(
				promptCase.request,
				claudeIntegrationModel(),
				cfg,
				requestRuntime,
			)
			if err != nil {
				t.Fatal(err)
			}
			wantPrompt := provider.BuildPrompt(
				promptCase.request,
				provider.SchemaInline,
			)
			if !bytes.Equal(spec.Stdin, wantPrompt) {
				t.Fatalf(
					"Stdin differs: got=%d bytes want=%d",
					len(spec.Stdin),
					len(wantPrompt),
				)
			}
			if spec.Files != nil {
				t.Fatalf("Files=%+v, want nil", spec.Files)
			}
			joinedMetadata := strings.Join(
				append(
					append([]string(nil), spec.Args...),
					spec.Env...,
				),
				"\x00",
			)
			for _, forbidden := range integrationRequestStrings(
				promptCase.request,
			) {
				if forbidden != "" &&
					strings.Contains(joinedMetadata, forbidden) {
					t.Fatalf("request changed argv/environment with %q", forbidden)
				}
			}

			result, runErr := harness.execute(requestRuntime, spec)
			if runErr != nil {
				t.Fatal(runErr)
			}
			got, parseErr := New().Parse(promptCase.request, result)
			want := fmt.Sprintf("stdin_bytes=%d", len(wantPrompt))
			if parseErr != nil || got != want {
				t.Fatalf("Parse()=(%q,%v), want (%q,nil)", got, parseErr, want)
			}
			for _, forbidden := range integrationRequestStrings(
				promptCase.request,
			) {
				if forbidden != "" && strings.Contains(got, forbidden) {
					t.Fatalf("stdin probe exposed %q: %q", forbidden, got)
				}
			}
		})
	}

	t.Run("success returns only result string", func(t *testing.T) {
		harness := newClaudeIntegrationHarness(
			t,
			claudeIntegrationLimits(),
		)
		requestRuntime := harness.prepare("claude-success")
		request := core.Request{
			Input:  "private success prompt",
			Format: core.OutputFormat{Type: core.FormatText},
		}
		spec, err := New().Build(
			request,
			claudeIntegrationModel(),
			claudeIntegrationProviderConfig(
				executable,
				"claude-json",
				filepath.Join(t.TempDir(), "claude-home"),
			),
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
		if parseErr != nil || got != "hello" {
			t.Fatalf("Parse()=(%q,%v), want (hello,nil)", got, parseErr)
		}
	})

	t.Run("provider output classifications", func(t *testing.T) {
		tests := []struct {
			name     string
			mode     string
			category provider.ErrorCategory
		}{
			{
				name:     "auth narrows nonzero",
				mode:     "claude-auth-error",
				category: provider.ProviderErrorAuthRequired,
			},
			{
				name:     "rate limit narrows nonzero",
				mode:     "claude-rate-limit",
				category: provider.ProviderErrorRateLimited,
			},
			{
				name:     "documented error arm",
				mode:     "claude-execution-error",
				category: provider.ProviderErrorFailed,
			},
			{
				name:     "generic nonzero",
				mode:     "exit-7",
				category: provider.ProviderErrorFailed,
			},
			{
				name:     "duplicate envelope",
				mode:     "duplicate-json",
				category: provider.ProviderErrorProtocol,
			},
			{
				name:     "malformed envelope",
				mode:     "invalid-json",
				category: provider.ProviderErrorProtocol,
			},
			{
				name:     "invalid UTF-8 envelope",
				mode:     "invalid-utf8",
				category: provider.ProviderErrorProtocol,
			},
		}
		harness := newClaudeIntegrationHarness(
			t,
			claudeIntegrationLimits(),
		)
		for index, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				requestRuntime := harness.prepare(
					fmt.Sprintf("claude-error-%04d", index),
				)
				request := core.Request{
					Input:  "private integration prompt",
					Format: core.OutputFormat{Type: core.FormatText},
				}
				spec, err := New().Build(
					request,
					claudeIntegrationModel(),
					claudeIntegrationProviderConfig(
						executable,
						test.mode,
						filepath.Join(t.TempDir(), "claude-home"),
					),
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
				assertProviderCategory(t, parseErr, test.category)
			})
		}
	})

	t.Run("timeout remains a process error", func(t *testing.T) {
		limits := claudeIntegrationLimits()
		limits.Execution = 100 * time.Millisecond
		harness := newClaudeIntegrationHarness(t, limits)
		requestRuntime := harness.prepare("claude-timeout")
		spec, err := New().Build(
			core.Request{Format: core.OutputFormat{Type: core.FormatText}},
			claudeIntegrationModel(),
			claudeIntegrationProviderConfig(
				executable,
				"hang",
				filepath.Join(t.TempDir(), "claude-home"),
			),
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
			limits := claudeIntegrationLimits()
			limits.StdoutBytes = 1024
			limits.StderrBytes = 1024
			harness := newClaudeIntegrationHarness(t, limits)
			requestRuntime := harness.prepare(
				"claude-" + integrationTestName(flood.name),
			)
			spec, err := New().Build(
				core.Request{
					Format: core.OutputFormat{Type: core.FormatText},
				},
				claudeIntegrationModel(),
				claudeIntegrationProviderConfig(
					executable,
					flood.mode,
					filepath.Join(t.TempDir(), "claude-home"),
				),
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
		harness := newClaudeIntegrationHarness(
			t,
			claudeIntegrationLimits(),
		)
		requestRuntime := harness.prepare("claude-discard")
		model := claudeIntegrationModel()
		model.ProviderModel = "--attacker-model"
		_, err := New().Build(
			core.Request{Format: core.OutputFormat{Type: core.FormatText}},
			model,
			claudeIntegrationProviderConfig(
				executable,
				"claude-json",
				filepath.Join(t.TempDir(), "claude-home"),
			),
			requestRuntime,
		)
		if err == nil {
			t.Fatal("Build unexpectedly succeeded")
		}
		ctx, cancel := context.WithTimeout(
			context.Background(),
			claudeIntegrationOuterDeadline,
		)
		defer cancel()
		if discardErr := harness.supervisor.Discard(
			ctx,
			requestRuntime,
		); discardErr != nil {
			t.Fatal(discardErr)
		}
		assertIntegrationRuntimeAbsent(t, requestRuntime.Dir)
	})

	t.Run("shutdown drains before root close", func(t *testing.T) {
		harness := newClaudeIntegrationHarness(
			t,
			claudeIntegrationLimits(),
		)
		requestRuntime := harness.prepare("claude-shutdown")
		spec, err := New().Build(
			core.Request{Format: core.OutputFormat{Type: core.FormatText}},
			claudeIntegrationModel(),
			claudeIntegrationProviderConfig(
				executable,
				"claude-json",
				filepath.Join(t.TempDir(), "claude-home"),
			),
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
			"claude-after-shutdown",
		); prepareErr == nil {
			t.Fatal("supervisor accepted work after Shutdown")
		}
	})
}

type claudeIntegrationHarness struct {
	t          *testing.T
	root       *process.Root
	supervisor *process.Supervisor
	closeOnce  sync.Once
}

func newClaudeIntegrationHarness(
	t *testing.T,
	limits process.Limits,
) *claudeIntegrationHarness {
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
	harness := &claudeIntegrationHarness{
		t:          t,
		root:       root,
		supervisor: supervisor,
	}
	t.Cleanup(harness.close)
	return harness
}

func (h *claudeIntegrationHarness) prepare(id string) process.Runtime {
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

func (h *claudeIntegrationHarness) execute(
	requestRuntime process.Runtime,
	spec process.CommandSpec,
) (process.Result, error) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(
		context.Background(),
		claudeIntegrationOuterDeadline,
	)
	defer cancel()
	result, err := h.supervisor.Execute(ctx, requestRuntime, spec)
	assertIntegrationRuntimeAbsent(h.t, requestRuntime.Dir)
	return result, err
}

func (h *claudeIntegrationHarness) close() {
	h.closeOnce.Do(func() {
		ctx, cancel := context.WithTimeout(
			context.Background(),
			claudeIntegrationOuterDeadline,
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

func claudeIntegrationProviderConfig(
	executable string,
	mode string,
	configHome string,
) provider.ProviderConfig {
	return provider.ProviderConfig{
		Executable: executable,
		PrefixArgs: []string{"--mode=" + mode},
		ConfigHome: configHome,
		SafePath:   filepath.Dir(executable),
		LookupEnv: func(name string) (string, bool) {
			if name == "SystemRoot" {
				return `C:\Windows`, true
			}
			return "", false
		},
	}
}

func claudeIntegrationModel() core.Model {
	return core.Model{
		ID:            "claude-integration",
		Provider:      core.ProviderClaude,
		ProviderModel: "claude-sonnet-4-5-20250929",
	}
}

func claudeIntegrationLimits() process.Limits {
	return process.Limits{
		Execution:   claudeIntegrationExecutionBudget,
		TermGrace:   claudeIntegrationTerminationGrace,
		Cleanup:     claudeIntegrationCleanupBudget,
		StdoutBytes: 1 << 20,
		StderrBytes: 1 << 20,
	}
}

func integrationStringPointer(value string) *string {
	return &value
}

func integrationRequestStrings(request core.Request) []string {
	values := []string{
		request.ModelAlias,
		request.Input,
		request.Format.Name,
		string(request.Format.Schema),
	}
	if request.Instructions != nil {
		values = append(values, *request.Instructions)
	}
	if request.Format.Description != nil {
		values = append(values, *request.Format.Description)
	}
	return values
}

func integrationTestName(value string) string {
	return strings.ReplaceAll(value, " ", "-")
}

func assertIntegrationRuntimeAbsent(t *testing.T, runtimeDir string) {
	t.Helper()
	if _, err := os.Lstat(runtimeDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("runtime still exists: path=%q error=%v", runtimeDir, err)
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

func TestClaudeAdapterSatisfiesProviderInterface(t *testing.T) {
	var adapter provider.Adapter = New()
	if adapter.Name() != core.ProviderClaude {
		t.Fatalf("adapter Name()=%q", adapter.Name())
	}
}
