//go:build integration

package gateway

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
	"github.com/krkarma777/ai-cli-gateway/internal/provider/claude"
	"github.com/krkarma777/ai-cli-gateway/internal/provider/codex"
	"github.com/krkarma777/ai-cli-gateway/internal/provider/gemini"
	"github.com/krkarma777/ai-cli-gateway/internal/scheduler"
	"github.com/krkarma777/ai-cli-gateway/internal/schema"
	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

const integrationDeadline = 15 * time.Second

type integrationHarness struct {
	gateway     *Gateway
	root        *process.Root
	supervisor  *process.Supervisor
	schedulers  map[core.ProviderName]*scheduler.Scheduler
	runtimeRoot string
	closed      bool
}

type integrationProvider struct {
	name    core.ProviderName
	adapter provider.Adapter
	mode    string
}

func TestGatewayRealFakeCLITextAndStructuredRouting(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	harness := newIntegrationHarness(t, executable, []integrationProvider{
		{core.ProviderCodex, codex.New(), "text"},
		{core.ProviderClaude, claude.New(), "claude-json"},
		{core.ProviderGemini, gemini.New(), "gemini-json"},
	}, integrationProcessLimits(), integrationSchedulerLimits())
	defer harness.close(t)

	for _, alias := range []string{"codex-model", "claude-model", "gemini-model"} {
		result, err := harness.gateway.Respond(context.Background(), core.Request{
			ModelAlias: alias, Input: "private integration prompt",
			Format: core.OutputFormat{Type: core.FormatText},
		})
		if err != nil {
			t.Fatalf("Respond(%s) error=%v", alias, err)
		}
		want := "hello"
		if alias == "codex-model" {
			want = "hello\n"
		}
		if result.Text != want || result.Meta.Provider != providerForAlias(alias) ||
			result.Meta.ExitCategory != "completed" {
			t.Fatalf("Respond(%s)=%+v", alias, result)
		}
	}

	structuredHarness := newIntegrationHarness(t, executable, []integrationProvider{
		{core.ProviderCodex, codex.New(), "codex-json"},
	}, integrationProcessLimits(), integrationSchedulerLimits())
	defer structuredHarness.close(t)
	structured, err := structuredHarness.gateway.Respond(context.Background(), core.Request{
		ModelAlias: "codex-model", Input: "private structured prompt",
		Format: core.OutputFormat{
			Type: core.FormatJSONSchema, Name: "answer",
			Schema: []byte(`{"type":"object","properties":{"answer":{"const":"hello"}},"required":["answer"],"additionalProperties":false}`),
		},
	})
	if err != nil || structured.Text != `{"answer":"hello"}`+"\n" {
		t.Fatalf("structured Respond()=(%q,%v)", structured.Text, err)
	}
	// The current fixture has a native JSON-object success mode only for Codex.
	// Unit orchestration tests cover the same local validator after every adapter.
	assertNoRequestDirectories(t, harness.runtimeRoot)
	assertNoRequestDirectories(t, structuredHarness.runtimeRoot)
}

func TestGatewayRealFakeCLIStableFailures(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)
	tests := []struct {
		name       string
		mode       string
		process    process.Limits
		format     core.OutputFormat
		wantCode   string
		cancel     bool
		wantCtxErr error
	}{
		{"malformed JSON", "invalid-json", integrationProcessLimits(), integrationStructuredFormat(), core.CodeStructuredOutputInvalid, false, nil},
		{"duplicate JSON", "duplicate-json", integrationProcessLimits(), integrationStructuredFormat(), core.CodeStructuredOutputInvalid, false, nil},
		{"fenced JSON", "fenced-json", integrationProcessLimits(), integrationStructuredFormat(), core.CodeStructuredOutputInvalid, false, nil},
		{"schema mismatch", "schema-mismatch", integrationProcessLimits(), integrationStructuredFormat(), core.CodeStructuredOutputInvalid, false, nil},
		{"exit 7", "exit-7", integrationProcessLimits(), core.OutputFormat{Type: core.FormatText}, core.CodeProviderFailed, false, nil},
		{"stdout cap", "flood-stdout", processLimitsWith(2*time.Second, 1024, 1024), core.OutputFormat{Type: core.FormatText}, core.CodeOutputLimitExceeded, false, nil},
		{"stderr cap", "flood-stderr", processLimitsWith(2*time.Second, 1024, 1024), core.OutputFormat{Type: core.FormatText}, core.CodeOutputLimitExceeded, false, nil},
		{"provider timeout", "hang", processLimitsWith(50*time.Millisecond, 4096, 4096), core.OutputFormat{Type: core.FormatText}, core.CodeProviderTimeout, false, nil},
		{"caller cancellation", "hang", processLimitsWith(5*time.Second, 4096, 4096), core.OutputFormat{Type: core.FormatText}, "", true, context.Canceled},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			harness := newIntegrationHarness(t, executable, []integrationProvider{
				{core.ProviderCodex, codex.New(), test.mode},
			}, test.process, integrationSchedulerLimits())
			defer harness.close(t)
			ctx := context.Background()
			if test.cancel {
				cancelCtx, cancel := context.WithCancel(ctx)
				result := make(chan error, 1)
				go func() {
					_, respondErr := harness.gateway.Respond(cancelCtx, core.Request{
						ModelAlias: "codex-model", Input: "planted integration request secret", Format: test.format,
					})
					result <- respondErr
				}()
				waitForScheduler(t, harness.schedulers[core.ProviderCodex], func(stats scheduler.Stats) bool { return stats.Running == 1 })
				cancel()
				err := <-result
				if !errors.Is(err, test.wantCtxErr) {
					t.Fatalf("Respond() error=%v, want %v", err, test.wantCtxErr)
				}
				if err == nil || strings.Contains(err.Error(), "planted") || strings.Contains(err.Error(), "secret") {
					t.Fatalf("unsafe public error=%v", err)
				}
				assertNoRequestDirectories(t, harness.runtimeRoot)
				return
			}
			_, err := harness.gateway.Respond(ctx, core.Request{
				ModelAlias: "codex-model", Input: "planted integration request secret", Format: test.format,
			})
			if got := apiCode(t, err); got != test.wantCode {
				t.Fatalf("code=%q, want %q", got, test.wantCode)
			}
			if err == nil || strings.Contains(err.Error(), "planted") || strings.Contains(err.Error(), "secret") {
				t.Fatalf("unsafe public error=%v", err)
			}
			assertNoRequestDirectories(t, harness.runtimeRoot)
		})
	}
}

func TestGatewayRealSchedulersQueueBoundsAndProviderIsolation(t *testing.T) {
	executable := testutil.BuildFakeCLI(t)

	t.Run("queue full", func(t *testing.T) {
		limits := integrationSchedulerLimits()
		limits.QueueSize = 1
		harness := newIntegrationHarness(t, executable, []integrationProvider{
			{core.ProviderCodex, codex.New(), "hang"},
		}, processLimitsWith(5*time.Second, 4096, 4096), limits)
		defer harness.close(t)
		firstCtx, firstCancel := context.WithCancel(context.Background())
		secondCtx, secondCancel := context.WithCancel(context.Background())
		firstDone := respondAsync(firstCtx, harness.gateway, "codex-model")
		waitForScheduler(t, harness.schedulers[core.ProviderCodex], func(stats scheduler.Stats) bool { return stats.Running == 1 })
		secondDone := respondAsync(secondCtx, harness.gateway, "codex-model")
		waitForScheduler(t, harness.schedulers[core.ProviderCodex], func(stats scheduler.Stats) bool { return stats.Queued == 1 })
		_, thirdErr := harness.gateway.Respond(context.Background(), core.Request{
			ModelAlias: "codex-model", Format: core.OutputFormat{Type: core.FormatText},
		})
		if got := apiCode(t, thirdErr); got != core.CodeQueueFull {
			t.Fatalf("third code=%q", got)
		}
		firstCancel()
		secondCancel()
		<-firstDone
		<-secondDone
	})

	t.Run("queue timeout", func(t *testing.T) {
		limits := integrationSchedulerLimits()
		limits.QueueTimeout = 30 * time.Millisecond
		harness := newIntegrationHarness(t, executable, []integrationProvider{
			{core.ProviderCodex, codex.New(), "hang"},
		}, processLimitsWith(5*time.Second, 4096, 4096), limits)
		defer harness.close(t)
		activeCtx, activeCancel := context.WithCancel(context.Background())
		activeDone := respondAsync(activeCtx, harness.gateway, "codex-model")
		waitForScheduler(t, harness.schedulers[core.ProviderCodex], func(stats scheduler.Stats) bool { return stats.Running == 1 })
		_, queuedErr := harness.gateway.Respond(context.Background(), core.Request{
			ModelAlias: "codex-model", Format: core.OutputFormat{Type: core.FormatText},
		})
		if got := apiCode(t, queuedErr); got != core.CodeQueueTimeout {
			t.Fatalf("queued code=%q", got)
		}
		activeCancel()
		<-activeDone
	})

	t.Run("provider-local isolation", func(t *testing.T) {
		harness := newIntegrationHarness(t, executable, []integrationProvider{
			{core.ProviderCodex, codex.New(), "hang"},
			{core.ProviderClaude, claude.New(), "claude-json"},
		}, processLimitsWith(5*time.Second, 4096, 4096), integrationSchedulerLimits())
		defer harness.close(t)
		codexCtx, codexCancel := context.WithCancel(context.Background())
		codexDone := respondAsync(codexCtx, harness.gateway, "codex-model")
		waitForScheduler(t, harness.schedulers[core.ProviderCodex], func(stats scheduler.Stats) bool { return stats.Running == 1 })
		result, err := harness.gateway.Respond(context.Background(), core.Request{
			ModelAlias: "claude-model", Format: core.OutputFormat{Type: core.FormatText},
		})
		if err != nil || result.Text != "hello" {
			t.Fatalf("Claude Respond()=(%+v,%v)", result, err)
		}
		codexCancel()
		<-codexDone
	})
}

func newIntegrationHarness(
	t *testing.T,
	executable string,
	entries []integrationProvider,
	processLimits process.Limits,
	schedulerLimits scheduler.Limits,
) *integrationHarness {
	t.Helper()
	runtimeRoot := filepath.Join(t.TempDir(), "runtime")
	root, err := process.OpenRoot(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	supervisor, err := process.NewSupervisor(root, processLimits)
	if err != nil {
		_ = root.Close()
		t.Fatal(err)
	}
	models := make([]core.Model, 0, len(entries))
	runtimes := make(map[core.ProviderName]*ProviderRuntime, len(entries))
	schedulers := make(map[core.ProviderName]*scheduler.Scheduler, len(entries))
	for index, entry := range entries {
		scheduled, schedulerErr := scheduler.New(schedulerLimits)
		if schedulerErr != nil {
			t.Fatal(schedulerErr)
		}
		runtime, runtimeErr := NewProviderRuntime(
			entry.adapter,
			integrationProviderConfig(executable, entry.mode, entry.name, t.TempDir()),
			scheduled,
			supervisor,
			validHealth(entry.name),
		)
		if runtimeErr != nil {
			t.Fatal(runtimeErr)
		}
		alias := string(entry.name) + "-model"
		models = append(models, core.Model{
			ID: alias, Provider: entry.name, ProviderModel: "trusted-provider-model", Created: int64(index + 1),
		})
		runtimes[entry.name] = runtime
		schedulers[entry.name] = scheduled
	}
	registry, err := core.NewRegistry(models)
	if err != nil {
		t.Fatal(err)
	}
	limits, err := schema.DefaultLimits(4096, 4096)
	if err != nil {
		t.Fatal(err)
	}
	var sequence atomic.Uint64
	gateway, err := New(registry, runtimes, Config{SchemaLimits: limits, FinalBytes: 4096}, Dependencies{
		NewRuntimeID: func() (string, error) { return fmt.Sprintf("%032x", sequence.Add(1)), nil },
		Now:          time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &integrationHarness{
		gateway: gateway, root: root, supervisor: supervisor,
		schedulers: schedulers, runtimeRoot: runtimeRoot,
	}
}

func (h *integrationHarness) close(t *testing.T) {
	t.Helper()
	if h == nil || h.closed {
		return
	}
	h.closed = true
	ctx, cancel := context.WithTimeout(context.Background(), integrationDeadline)
	defer cancel()
	if err := h.gateway.Shutdown(ctx); err != nil {
		t.Errorf("gateway Shutdown: %v", err)
	}
	for name, scheduled := range h.schedulers {
		if stats := scheduled.Stats(); stats != (scheduler.Stats{}) {
			t.Errorf("scheduler %s not drained: %+v", name, stats)
		}
	}
	if err := h.supervisor.Shutdown(ctx); err != nil {
		t.Errorf("supervisor Shutdown: %v", err)
	}
	assertNoRequestDirectories(t, h.runtimeRoot)
	if err := h.root.Close(); err != nil {
		t.Errorf("root Close: %v", err)
	}
}

func integrationProviderConfig(
	executable, mode string,
	name core.ProviderName,
	configHome string,
) provider.ProviderConfig {
	credentials := []string(nil)
	if name == core.ProviderGemini {
		credentials = []string{"GEMINI_API_KEY"}
	}
	return provider.ProviderConfig{
		Executable: executable, PrefixArgs: []string{"--mode", mode},
		ConfigHome: configHome, CredentialEnv: credentials,
		SafePath: filepath.Dir(executable),
		LookupEnv: func(environmentName string) (string, bool) {
			switch environmentName {
			case "GEMINI_API_KEY":
				return "fake-integration-credential", true
			case "SystemRoot":
				value, present := os.LookupEnv("SystemRoot")
				return value, present
			default:
				return "", false
			}
		},
	}
}

func integrationProcessLimits() process.Limits {
	return processLimitsWith(5*time.Second, 64*1024, 64*1024)
}

func processLimitsWith(execution time.Duration, stdout, stderr int64) process.Limits {
	return process.Limits{
		Execution: execution, TermGrace: 100 * time.Millisecond, Cleanup: time.Second,
		StdoutBytes: stdout, StderrBytes: stderr,
	}
}

func integrationSchedulerLimits() scheduler.Limits {
	return scheduler.Limits{Concurrency: 1, QueueSize: 4, QueueBytes: 1 << 20, QueueTimeout: time.Second}
}

func integrationStructuredFormat() core.OutputFormat {
	return core.OutputFormat{
		Type: core.FormatJSONSchema, Name: "answer",
		Schema: []byte(`{"type":"object","properties":{"answer":{"const":"hello"}},"required":["answer"],"additionalProperties":false}`),
	}
}

func providerForAlias(alias string) core.ProviderName {
	return core.ProviderName(strings.TrimSuffix(alias, "-model"))
}

func respondAsync(ctx context.Context, gateway *Gateway, alias string) <-chan error {
	done := make(chan error, 1)
	go func() {
		_, err := gateway.Respond(ctx, core.Request{
			ModelAlias: alias, Format: core.OutputFormat{Type: core.FormatText},
		})
		done <- err
	}()
	return done
}

func waitForScheduler(
	t *testing.T,
	scheduled *scheduler.Scheduler,
	ready func(scheduler.Stats) bool,
) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if ready(scheduled.Stats()) {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("scheduler state did not arrive: %+v", scheduled.Stats())
}

func assertNoRequestDirectories(t *testing.T, runtimeRoot string) {
	t.Helper()
	entries, err := os.ReadDir(runtimeRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".lock" {
			t.Fatalf("runtime artifact remained: %s", entry.Name())
		}
	}
}
