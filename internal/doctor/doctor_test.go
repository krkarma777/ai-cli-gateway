package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

func TestRunRejectsNilDependenciesExactly(t *testing.T) {
	diagnosis, err := Run(context.Background(), config.Config{}, Dependencies{})
	if diagnosis.RuntimeRoot != nil || diagnosis.Registry() != nil ||
		diagnosis.ResolvedProviders() != nil || diagnosis.Report().constructed {
		t.Fatalf("diagnosis = %+v, want zero value", diagnosis)
	}
	assertExactError(t, err, ErrInvalidDependencies)
}

func TestRunRejectsEachNilFunctionDependencyWithoutInvokingAnything(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Dependencies)
	}{
		{"LookupEnv", func(value *Dependencies) { value.LookupEnv = nil }},
		{"NewRuntimeID", func(value *Dependencies) { value.NewRuntimeID = nil }},
		{"OpenRoot", func(value *Dependencies) { value.OpenRoot = nil }},
		{"Janitor", func(value *Dependencies) { value.Janitor = nil }},
		{"CloseRoot", func(value *Dependencies) { value.CloseRoot = nil }},
		{"NewProbeController", func(value *Dependencies) { value.NewProbeController = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			dependencies := Dependencies{
				LookupEnv:    func(string) (string, bool) { calls++; return "", false },
				NewRuntimeID: func() (string, error) { calls++; return "id", nil },
				OpenRoot:     func(string) (*process.Root, error) { calls++; return nil, nil },
				Janitor:      func(context.Context, *process.Root) error { calls++; return nil },
				CloseRoot:    func(*process.Root) error { calls++; return nil },
				NewProbeController: func(
					*process.Root,
					process.Limits,
					func() (string, error),
				) (ProbeController, error) {
					calls++
					return nil, nil
				},
			}
			test.mutate(&dependencies)
			_, err := Run(context.Background(), config.Config{}, dependencies)
			assertExactError(t, err, ErrInvalidDependencies)
			if calls != 0 {
				t.Fatalf("dependency calls = %d, want 0", calls)
			}
		})
	}
}

func TestNewProcessProbeControllerRejectsInvalidConstruction(t *testing.T) {
	limits := process.Limits{
		Execution:   5 * time.Second,
		TermGrace:   time.Second,
		Cleanup:     time.Second,
		StdoutBytes: 64 << 10,
		StderrBytes: 64 << 10,
	}
	if controller, err := NewProcessProbeController(nil, limits, func() (string, error) {
		return "runtime-id", nil
	}); err == nil || controller != nil {
		t.Fatalf("NewProcessProbeController() = %#v, %v; want nil, error", controller, err)
	}
}

func TestNewProcessProbeControllerRealSupervisorLifecycle(t *testing.T) {
	testutil.AcquireRepositoryScanLock(t)
	parent, err := os.MkdirTemp(".", ".doctor-controller-integration-")
	if err != nil {
		t.Fatalf("create secure integration parent: %v", err)
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		t.Fatalf("absolute integration parent: %v", err)
	}
	//nolint:gosec // This is the required private directory mode, not a file mode.
	if err := os.Chmod(parent, 0o700); err != nil {
		t.Fatalf("chmod integration parent: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(parent) })
	t.Setenv("TMPDIR", parent)

	gateway := testutil.BuildGateway(t)
	root, err := process.OpenRoot(filepath.Join(parent, "runtime"))
	if err != nil {
		t.Fatalf("OpenRoot() error = %v", err)
	}
	t.Cleanup(func() { _ = root.Close() })
	sequence := 0
	lifecycleLimits := doctorProbeLimits()
	// Package tests run in parallel and this integration path launches a nested
	// gateway child. Keep the production five-second doctor policy covered by
	// the construction tests, but do not make this lifecycle proof depend on a
	// heavily loaded host scheduling both processes within that policy window.
	lifecycleLimits.Execution = 30 * time.Second
	controller, err := NewProcessProbeController(
		root,
		lifecycleLimits,
		func() (string, error) {
			sequence++
			return "integration-runtime-" + strconv.Itoa(sequence), nil
		},
	)
	if err != nil {
		t.Fatalf("NewProcessProbeController() error = %v", err)
	}
	if err := controller.SelfTest(context.Background(), gateway); err != nil {
		t.Fatalf("SelfTest() error = %v", err)
	}
	buildErr := errors.New("intentional builder failure")
	if _, err := controller.RunProbe(
		context.Background(),
		func(process.Runtime) (process.CommandSpec, error) {
			return process.CommandSpec{}, buildErr
		},
	); !errors.Is(err, buildErr) {
		t.Fatalf("builder-failure RunProbe() error = %v, want %v", err, buildErr)
	}
	result, err := controller.RunProbe(
		context.Background(),
		func(runtime process.Runtime) (process.CommandSpec, error) {
			return process.CommandSpec{
				Executable: gateway,
				Args:       []string{"__process-selftest", "parent"},
				Env:        []string{},
				Dir:        runtime.Dir,
				Files: []process.FileSpec{{
					Name: "materialized.txt", Data: []byte("owned"), Mode: 0o600,
				}},
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("successful RunProbe() error = %v", err)
	}
	if result.ExitCode != 0 || string(result.Stdout) != "ready\n" || len(result.Stderr) != 0 {
		t.Fatalf("RunProbe() result = %+v", result)
	}
	if controller.CleanupFailed() {
		t.Fatal("controller cleanup latch set on successful lifecycle")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatalf("Root.Close() error = %v", err)
	}
}

func TestCleanupCategoryRejectsTypedNilRunErrorWithoutPanic(t *testing.T) {
	var typedNil *process.RunError
	if cleanupCategory(typedNil) {
		t.Fatal("typed-nil RunError classified as cleanup")
	}
}

func TestProcessProbeControllerLatchesCleanupAndRejectsLaterProbeBeforeCalls(t *testing.T) {
	cleanupErr := &process.RunError{Kind: process.ErrorCleanup, Err: errors.New("cleanup")}
	supervisor := &doctorTestProbeSupervisor{executeErr: cleanupErr}
	idCalls := 0
	controller := &processProbeController{
		supervisor: supervisor,
		newRuntimeID: func() (string, error) {
			idCalls++
			return "runtime-id", nil
		},
	}
	buildCalls := 0
	build := func(process.Runtime) (process.CommandSpec, error) {
		buildCalls++
		return process.CommandSpec{}, nil
	}
	if _, err := controller.RunProbe(context.Background(), build); !errors.Is(err, cleanupErr) {
		t.Fatalf("first RunProbe() error = %v, want cleanup error", err)
	}
	if !controller.CleanupFailed() {
		t.Fatal("cleanup latch remained clear")
	}
	firstTrace := supervisor.traceCopy()
	if _, err := controller.RunProbe(context.Background(), build); !cleanupCategory(err) {
		t.Fatalf("post-latch RunProbe() error = %v, want cleanup category", err)
	}
	if got, want := idCalls, 1; got != want {
		t.Fatalf("runtime ID calls = %d, want %d", got, want)
	}
	if got, want := buildCalls, 1; got != want {
		t.Fatalf("builder calls = %d, want %d", got, want)
	}
	if got := supervisor.traceCopy(); !slices.Equal(got, firstTrace) {
		t.Fatalf("post-latch supervisor trace = %q, want unchanged %q", got, firstTrace)
	}
}

func TestProcessProbeControllerBuilderFailureDiscardsAndCleanupWins(t *testing.T) {
	buildErr := errors.New("build")
	cleanupErr := &process.RunError{Kind: process.ErrorCleanup, Err: errors.New("discard")}
	supervisor := &doctorTestProbeSupervisor{discardErr: cleanupErr}
	controller := &processProbeController{
		supervisor:   supervisor,
		newRuntimeID: func() (string, error) { return "runtime-id", nil },
	}
	_, err := controller.RunProbe(context.Background(), func(process.Runtime) (process.CommandSpec, error) {
		return process.CommandSpec{}, buildErr
	})
	if !errors.Is(err, cleanupErr) || !controller.CleanupFailed() {
		t.Fatalf("RunProbe() = %v, latch %v; want discard cleanup", err, controller.CleanupFailed())
	}
	if got, want := supervisor.traceCopy(), []string{"prepare", "discard"}; !slices.Equal(got, want) {
		t.Fatalf("trace = %q, want %q", got, want)
	}
}

func TestProcessProbeControllerFailureTraceAndCleanupClassification(t *testing.T) {
	t.Run("runtime ID failure", func(t *testing.T) {
		idErr := errors.New("id")
		supervisor := &doctorTestProbeSupervisor{}
		controller := &processProbeController{
			supervisor:   supervisor,
			newRuntimeID: func() (string, error) { return "", idErr },
		}
		if _, err := controller.RunProbe(context.Background(), func(process.Runtime) (process.CommandSpec, error) {
			return process.CommandSpec{}, nil
		}); !errors.Is(err, idErr) {
			t.Fatalf("RunProbe() error = %v, want ID error", err)
		}
		if trace := supervisor.traceCopy(); len(trace) != 0 {
			t.Fatalf("supervisor trace = %q, want empty", trace)
		}
	})

	t.Run("prepare failure", func(t *testing.T) {
		prepareErr := errors.New("prepare")
		supervisor := &doctorTestProbeSupervisor{prepareErr: prepareErr}
		controller := &processProbeController{
			supervisor:   supervisor,
			newRuntimeID: func() (string, error) { return "runtime-id", nil },
		}
		buildCalls := 0
		if _, err := controller.RunProbe(context.Background(), func(process.Runtime) (process.CommandSpec, error) {
			buildCalls++
			return process.CommandSpec{}, nil
		}); !errors.Is(err, prepareErr) {
			t.Fatalf("RunProbe() error = %v, want prepare error", err)
		}
		if got, want := supervisor.traceCopy(), []string{"prepare"}; !slices.Equal(got, want) || buildCalls != 0 {
			t.Fatalf("trace/build calls = %q/%d, want %q/0", got, buildCalls, want)
		}
	})

	t.Run("builder failure discard success", func(t *testing.T) {
		buildErr := errors.New("build")
		supervisor := &doctorTestProbeSupervisor{}
		controller := &processProbeController{
			supervisor:   supervisor,
			newRuntimeID: func() (string, error) { return "runtime-id", nil },
		}
		if _, err := controller.RunProbe(context.Background(), func(process.Runtime) (process.CommandSpec, error) {
			return process.CommandSpec{}, buildErr
		}); !errors.Is(err, buildErr) || controller.CleanupFailed() {
			t.Fatalf("RunProbe() error/latch = %v/%v", err, controller.CleanupFailed())
		}
		if got, want := supervisor.traceCopy(), []string{"prepare", "discard"}; !slices.Equal(got, want) {
			t.Fatalf("trace = %q, want %q", got, want)
		}
	})

	for _, kind := range []process.ErrorKind{
		process.ErrorCanceled,
		process.ErrorTimeout,
		process.ErrorOutputLimit,
		process.ErrorStart,
		process.ErrorCleanup,
	} {
		t.Run("execute "+string(kind), func(t *testing.T) {
			executeErr := &process.RunError{Kind: kind, Err: errors.New("execute")}
			supervisor := &doctorTestProbeSupervisor{executeErr: executeErr}
			controller := &processProbeController{
				supervisor:   supervisor,
				newRuntimeID: func() (string, error) { return "runtime-id", nil },
			}
			if _, err := controller.RunProbe(context.Background(), func(process.Runtime) (process.CommandSpec, error) {
				return process.CommandSpec{}, nil
			}); !errors.Is(err, executeErr) {
				t.Fatalf("RunProbe() error = %v, want %v", err, executeErr)
			}
			if got, want := controller.CleanupFailed(), kind == process.ErrorCleanup; got != want {
				t.Fatalf("cleanup latch = %v, want %v", got, want)
			}
		})
	}
}

func TestProcessProbeControllerSelfTestLatchAndShutdownRetry(t *testing.T) {
	cleanupErr := &process.RunError{Kind: process.ErrorCleanup, Err: errors.New("selftest")}
	shutdownCalls := 0
	supervisor := &doctorTestProbeSupervisor{
		selfTestErr: cleanupErr,
		shutdown: func(_ context.Context) error {
			shutdownCalls++
			if shutdownCalls == 1 {
				return &process.RunError{Kind: process.ErrorCleanup, Err: context.DeadlineExceeded}
			}
			return nil
		},
	}
	controller := &processProbeController{
		supervisor:   supervisor,
		newRuntimeID: func() (string, error) { return "runtime-id", nil },
	}
	if err := controller.SelfTest(context.Background(), "/trusted/gateway"); !errors.Is(err, cleanupErr) || !controller.CleanupFailed() {
		t.Fatalf("SelfTest() error/latch = %v/%v", err, controller.CleanupFailed())
	}
	firstCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := controller.Shutdown(firstCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first Shutdown() error = %v", err)
	}
	if err := controller.Shutdown(context.Background()); err != nil {
		t.Fatalf("retry Shutdown() error = %v", err)
	}
	if shutdownCalls != 2 {
		t.Fatalf("Shutdown calls = %d, want 2", shutdownCalls)
	}
}

func TestProcessProbeControllerShutdownIsNotSerializedBehindBlockingExecute(t *testing.T) {
	executeStarted := make(chan struct{})
	releaseExecute := make(chan struct{})
	supervisor := &doctorTestProbeSupervisor{
		executeStarted: executeStarted,
		releaseExecute: releaseExecute,
		shutdown: func(ctx context.Context) error {
			<-ctx.Done()
			return &process.RunError{Kind: process.ErrorCleanup, Err: ctx.Err()}
		},
	}
	controller := &processProbeController{
		supervisor:   supervisor,
		newRuntimeID: func() (string, error) { return "runtime-id", nil },
	}
	probeDone := make(chan struct{})
	go func() {
		defer close(probeDone)
		_, _ = controller.RunProbe(context.Background(), func(process.Runtime) (process.CommandSpec, error) {
			return process.CommandSpec{}, nil
		})
	}()
	select {
	case <-executeStarted:
	case <-time.After(time.Second):
		t.Fatal("execute did not start")
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := controller.Shutdown(shutdownCtx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Shutdown() error = %v, want deadline", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Shutdown() blocked behind Execute for %v", elapsed)
	}
	close(releaseExecute)
	select {
	case <-probeDone:
	case <-time.After(time.Second):
		t.Fatal("probe did not finish")
	}
}

func TestRunValidatesGatewayBeforeAdapterOrRootCalls(t *testing.T) {
	adapter := &doctorTestAdapter{name: core.ProviderCodex, interval: reportTestRange()}
	rootCalls := 0
	dependencies := doctorTestDependencies(map[core.ProviderName]provider.Adapter{
		core.ProviderCodex: adapter,
	})
	dependencies.GatewayExecutable = filepath.Join(t.TempDir(), "missing-gateway")
	dependencies.OpenRoot = func(string) (*process.Root, error) {
		rootCalls++
		return nil, errors.New("unexpected")
	}
	diagnosis, err := Run(context.Background(), doctorTestConfig(t, core.ProviderCodex), dependencies)
	assertExactError(t, err, ErrInvalidDependencies)
	if diagnosis.Report().constructed || diagnosis.Registry() != nil || diagnosis.RuntimeRoot != nil {
		t.Fatalf("diagnosis = %+v, want zero", diagnosis)
	}
	if adapter.nameCalls != 0 || adapter.rangeCalls != 0 || adapter.probeCalls != 0 || rootCalls != 0 {
		t.Fatalf("calls = name %d range %d probe %d root %d, want zero",
			adapter.nameCalls, adapter.rangeCalls, adapter.probeCalls, rootCalls)
	}
}

func TestRunRejectsUnsafeGatewayShapesBeforeAdapterCalls(t *testing.T) {
	directory := doctorTestPrivateDirectory(t)
	nonExecutable := filepath.Join(directory, "non-executable")
	if runtime.GOOS == "windows" {
		nonExecutable += ".cmd"
	}
	if err := os.WriteFile(nonExecutable, []byte("test"), 0o600); err != nil {
		t.Fatalf("write non-executable fixture: %v", err)
	}
	tests := map[string]string{
		"empty":          "",
		"relative":       "relative-gateway",
		"missing":        filepath.Join(directory, "missing"),
		"directory":      directory,
		"non-executable": nonExecutable,
	}
	for name, gateway := range tests {
		t.Run(name, func(t *testing.T) {
			adapter := &doctorTestAdapter{name: core.ProviderCodex, interval: reportTestRange()}
			dependencies := doctorTestDependencies(map[core.ProviderName]provider.Adapter{
				core.ProviderCodex: adapter,
			})
			dependencies.GatewayExecutable = gateway
			_, err := Run(context.Background(), doctorTestConfig(t, core.ProviderCodex), dependencies)
			assertExactError(t, err, ErrInvalidDependencies)
			if adapter.nameCalls != 0 || adapter.rangeCalls != 0 || adapter.probeCalls != 0 {
				t.Fatalf("adapter calls = %d/%d/%d", adapter.nameCalls, adapter.rangeCalls, adapter.probeCalls)
			}
		})
	}
}

func TestRunPreRootFailureReturnsCompleteDiagnosisAndSkipsRootAndProviders(t *testing.T) {
	gateway := doctorTestExecutable(t)
	cfg := doctorTestConfig(t, core.ProviderCodex)
	cfg.Server.Listen = "localhost:8080"
	adapter := &doctorTestAdapter{name: core.ProviderCodex, interval: reportTestRange()}
	rootCalls := 0
	dependencies := doctorTestDependencies(map[core.ProviderName]provider.Adapter{
		core.ProviderCodex: adapter,
	})
	dependencies.GatewayExecutable = gateway
	dependencies.OpenRoot = func(string) (*process.Root, error) {
		rootCalls++
		return nil, errors.New("unexpected")
	}
	diagnosis, err := Run(context.Background(), cfg, dependencies)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if diagnosis.RuntimeRoot != nil || diagnosis.ResolvedProviders() != nil {
		t.Fatalf("runtime/resolved = %#v/%#v, want nil", diagnosis.RuntimeRoot, diagnosis.ResolvedProviders())
	}
	if diagnosis.Registry() == nil {
		t.Fatal("registry is nil")
	}
	report := diagnosis.Report()
	if got, want := report.Models(), []string{"model-codex"}; !slices.Equal(got, want) {
		t.Fatalf("models = %q, want %q", got, want)
	}
	checks := report.Core()
	if len(checks) != 7 || checks[0] != (Check{
		Name: "listener", Status: "fail", Code: "listener_unsafe", Message: "listener is unsafe",
	}) {
		t.Fatalf("core checks = %+v", checks)
	}
	for _, index := range []int{3, 4, 5, 6} {
		if checks[index].Status != "skipped" {
			t.Fatalf("core check %d = %+v, want skipped", index, checks[index])
		}
	}
	rows := report.Providers()
	if len(rows) != 1 || !coreSkippedProviderRow(rows[0]) {
		t.Fatalf("providers = %+v, want one skipped", rows)
	}
	if rootCalls != 0 || adapter.nameCalls != 1 || adapter.rangeCalls != 1 || adapter.probeCalls != 0 {
		t.Fatalf("calls = root %d name %d range %d probe %d", rootCalls, adapter.nameCalls, adapter.rangeCalls, adapter.probeCalls)
	}
}

func TestRunAdapterPreflightRejectsExactSetNilNameAndRangeBeforeRoot(t *testing.T) {
	gateway := doctorTestExecutable(t)
	tests := []struct {
		name       string
		adapters   func(*doctorTestAdapter) map[core.ProviderName]provider.Adapter
		wantNames  int
		wantRanges int
	}{
		{
			name:     "missing",
			adapters: func(*doctorTestAdapter) map[core.ProviderName]provider.Adapter { return nil },
		},
		{
			name: "extra",
			adapters: func(adapter *doctorTestAdapter) map[core.ProviderName]provider.Adapter {
				return map[core.ProviderName]provider.Adapter{
					core.ProviderCodex: adapter,
					core.ProviderClaude: &doctorTestAdapter{
						name: core.ProviderClaude, interval: reportTestRange(),
					},
				}
			},
		},
		{
			name: "typed nil",
			adapters: func(*doctorTestAdapter) map[core.ProviderName]provider.Adapter {
				var typedNil *doctorTestAdapter
				return map[core.ProviderName]provider.Adapter{core.ProviderCodex: typedNil}
			},
		},
		{
			name: "name mismatch",
			adapters: func(adapter *doctorTestAdapter) map[core.ProviderName]provider.Adapter {
				adapter.name = core.ProviderClaude
				return map[core.ProviderName]provider.Adapter{core.ProviderCodex: adapter}
			},
			wantNames: 1,
		},
		{
			name: "zero range",
			adapters: func(adapter *doctorTestAdapter) map[core.ProviderName]provider.Adapter {
				adapter.interval = provider.Range{}
				return map[core.ProviderName]provider.Adapter{core.ProviderCodex: adapter}
			},
			wantNames: 1, wantRanges: 1,
		},
		{
			name: "reversed range",
			adapters: func(adapter *doctorTestAdapter) map[core.ProviderName]provider.Adapter {
				adapter.interval = provider.Range{
					MinInclusive: provider.Version{Major: 2},
					MaxExclusive: provider.Version{Major: 1},
				}
				return map[core.ProviderName]provider.Adapter{core.ProviderCodex: adapter}
			},
			wantNames: 1, wantRanges: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := doctorTestConfig(t, core.ProviderCodex)
			adapter := &doctorTestAdapter{name: core.ProviderCodex, interval: reportTestRange()}
			rootCalls := 0
			dependencies := doctorTestDependencies(test.adapters(adapter))
			dependencies.GatewayExecutable = gateway
			dependencies.OpenRoot = func(string) (*process.Root, error) {
				rootCalls++
				return nil, errors.New("unexpected")
			}
			diagnosis, err := Run(context.Background(), cfg, dependencies)
			assertExactError(t, err, ErrInvalidDependencies)
			if diagnosis.Report().constructed || rootCalls != 0 || adapter.probeCalls != 0 ||
				adapter.nameCalls != test.wantNames || adapter.rangeCalls != test.wantRanges {
				t.Fatalf("diagnosis/calls = constructed %v root %d name %d range %d probe %d",
					diagnosis.Report().constructed, rootCalls, adapter.nameCalls,
					adapter.rangeCalls, adapter.probeCalls)
			}
		})
	}
}

func TestRunAdapterPreflightStagesAllMultiProviderMethodsBeforeValidation(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(map[core.ProviderName]*doctorTestAdapter)
		wantRanges int
	}{
		{
			name: "early sorted name mismatch still snapshots every name",
			mutate: func(adapters map[core.ProviderName]*doctorTestAdapter) {
				adapters[core.ProviderClaude].name = core.ProviderCodex
			},
		},
		{
			name: "early sorted invalid range still snapshots every range",
			mutate: func(adapters map[core.ProviderName]*doctorTestAdapter) {
				adapters[core.ProviderClaude].interval = provider.Range{}
			},
			wantRanges: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := doctorTestConfig(
				t,
				core.ProviderGemini,
				core.ProviderCodex,
				core.ProviderClaude,
			)
			concrete := map[core.ProviderName]*doctorTestAdapter{}
			adapters := map[core.ProviderName]provider.Adapter{}
			for _, name := range []core.ProviderName{
				core.ProviderClaude,
				core.ProviderCodex,
				core.ProviderGemini,
			} {
				adapter := &doctorTestAdapter{name: name, interval: reportTestRange()}
				concrete[name] = adapter
				adapters[name] = adapter
			}
			test.mutate(concrete)
			rootCalls := 0
			dependencies := doctorTestDependencies(adapters)
			dependencies.GatewayExecutable = doctorTestExecutable(t)
			dependencies.OpenRoot = func(string) (*process.Root, error) {
				rootCalls++
				return nil, errors.New("unexpected")
			}
			_, err := Run(context.Background(), cfg, dependencies)
			assertExactError(t, err, ErrInvalidDependencies)
			for name, adapter := range concrete {
				if adapter.nameCalls != 1 || adapter.rangeCalls != test.wantRanges {
					t.Fatalf("%s name/range calls = %d/%d, want 1/%d",
						name, adapter.nameCalls, adapter.rangeCalls, test.wantRanges)
				}
			}
			if rootCalls != 0 {
				t.Fatalf("root calls = %d, want 0", rootCalls)
			}
		})
	}
}

func TestRunContradictoryOpenRootResultClosesOwnershipAndFailsSafely(t *testing.T) {
	cfg := doctorTestConfig(t, core.ProviderCodex)
	adapter := &doctorTestAdapter{name: core.ProviderCodex, interval: reportTestRange()}
	dependencies := doctorTestDependencies(map[core.ProviderName]provider.Adapter{
		core.ProviderCodex: adapter,
	})
	dependencies.GatewayExecutable = doctorTestExecutable(t)
	root := doctorTestOpenRoot(t, cfg.Runtime.Root)
	dependencies.OpenRoot = func(string) (*process.Root, error) {
		return root, errors.New("contradictory open result")
	}
	janitorCalls := 0
	dependencies.Janitor = func(context.Context, *process.Root) error {
		janitorCalls++
		return nil
	}
	controllerCalls := 0
	dependencies.NewProbeController = func(
		*process.Root,
		process.Limits,
		func() (string, error),
	) (ProbeController, error) {
		controllerCalls++
		return &doctorTestController{}, nil
	}
	closeCalls := 0
	dependencies.CloseRoot = func(actual *process.Root) error {
		closeCalls++
		return actual.Close()
	}
	diagnosis, err := Run(context.Background(), cfg, dependencies)
	assertExactError(t, err, ErrDiagnosis)
	if diagnosis.Report().constructed || diagnosis.RuntimeRoot != nil ||
		diagnosis.ResolvedProviders() != nil || closeCalls != 1 || janitorCalls != 0 ||
		controllerCalls != 0 || adapter.probeCalls != 0 {
		t.Fatalf("diagnosis/root/resolved/close/janitor/controller/probe = %+v/%p/%#v/%d/%d/%d/%d",
			diagnosis, diagnosis.RuntimeRoot, diagnosis.ResolvedProviders(), closeCalls,
			janitorCalls, controllerCalls, adapter.probeCalls)
	}
}

func TestRunCoreValidationCoversKeyNULSchedulerAndRootClassification(t *testing.T) {
	gateway := doctorTestExecutable(t)
	tests := []struct {
		name       string
		mutate     func(*config.Config, *Dependencies)
		wantIndex  int
		wantCode   string
		wantRoot   int
		wantLookup int
	}{
		{
			name: "gateway key NUL",
			mutate: func(cfg *config.Config, dependencies *Dependencies) {
				cfg.Server.APIKeyEnv = "GATEWAY_KEY"
				dependencies.LookupEnv = func(name string) (string, bool) {
					if name != "GATEWAY_KEY" {
						t.Fatalf("lookup name = %q", name)
					}
					return "secret\x00suffix", true
				}
			},
			wantIndex: 1, wantCode: "gateway_key_missing", wantLookup: 1,
		},
		{
			name: "scheduler invalid",
			mutate: func(cfg *config.Config, _ *Dependencies) {
				providerConfig := cfg.Providers["codex"]
				providerConfig.QueueBytes = 0
				cfg.Providers["codex"] = providerConfig
			},
			wantIndex: 2, wantCode: "scheduler_invalid",
		},
		{
			name: "root locked",
			mutate: func(_ *config.Config, dependencies *Dependencies) {
				dependencies.OpenRoot = func(string) (*process.Root, error) {
					return nil, process.ErrRootLocked
				}
			},
			wantIndex: 3, wantCode: "runtime_locked", wantRoot: 1,
		},
		{
			name: "nil root",
			mutate: func(_ *config.Config, dependencies *Dependencies) {
				dependencies.OpenRoot = func(string) (*process.Root, error) { return nil, nil }
			},
			wantIndex: 3, wantCode: "runtime_unsafe", wantRoot: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := doctorTestConfig(t, core.ProviderCodex)
			adapter := &doctorTestAdapter{name: core.ProviderCodex, interval: reportTestRange()}
			dependencies := doctorTestDependencies(map[core.ProviderName]provider.Adapter{
				core.ProviderCodex: adapter,
			})
			dependencies.GatewayExecutable = gateway
			rootCalls := 0
			lookupCalls := 0
			test.mutate(&cfg, &dependencies)
			selectedOpen := dependencies.OpenRoot
			dependencies.OpenRoot = func(path string) (*process.Root, error) {
				rootCalls++
				return selectedOpen(path)
			}
			if test.wantLookup != 0 {
				selectedLookup := dependencies.LookupEnv
				dependencies.LookupEnv = func(name string) (string, bool) {
					lookupCalls++
					return selectedLookup(name)
				}
			}
			diagnosis, err := Run(context.Background(), cfg, dependencies)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			checks := diagnosis.Report().Core()
			if checks[test.wantIndex].Status != "fail" || checks[test.wantIndex].Code != test.wantCode {
				t.Fatalf("core checks = %+v", checks)
			}
			if rootCalls != test.wantRoot || lookupCalls != test.wantLookup || adapter.probeCalls != 0 {
				t.Fatalf("root/lookup/probe calls = %d/%d/%d", rootCalls, lookupCalls, adapter.probeCalls)
			}
		})
	}
}

func TestRunJanitorFailureClosesRootAndReportsCleanupIndependently(t *testing.T) {
	gateway := doctorTestExecutable(t)
	cfg := doctorTestConfig(t, core.ProviderCodex)
	adapter := &doctorTestAdapter{name: core.ProviderCodex, interval: reportTestRange()}
	dependencies := doctorTestDependencies(map[core.ProviderName]provider.Adapter{
		core.ProviderCodex: adapter,
	})
	dependencies.GatewayExecutable = gateway
	openedRoot := doctorTestOpenRoot(t, cfg.Runtime.Root)
	dependencies.OpenRoot = func(string) (*process.Root, error) { return openedRoot, nil }
	janitorCalls := 0
	dependencies.Janitor = func(context.Context, *process.Root) error {
		janitorCalls++
		return errors.New("janitor")
	}
	closeCalls := 0
	dependencies.CloseRoot = func(root *process.Root) error {
		closeCalls++
		return root.Close()
	}
	controllerCalls := 0
	dependencies.NewProbeController = func(
		*process.Root,
		process.Limits,
		func() (string, error),
	) (ProbeController, error) {
		controllerCalls++
		return &doctorTestController{}, nil
	}
	diagnosis, err := Run(context.Background(), cfg, dependencies)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if diagnosis.RuntimeRoot != nil || diagnosis.ResolvedProviders() != nil {
		t.Fatalf("runtime/resolved = %#v/%#v, want nil", diagnosis.RuntimeRoot, diagnosis.ResolvedProviders())
	}
	checks := diagnosis.Report().Core()
	if checks[3].Status != "pass" || checks[4] != (Check{
		Name: "runtime_janitor", Status: "fail", Code: "runtime_cleanup_failed", Message: "runtime cleanup failed",
	}) || checks[5].Status != "skipped" || checks[6].Status != "pass" {
		t.Fatalf("core checks = %+v", checks)
	}
	if janitorCalls != 1 || closeCalls != 1 || controllerCalls != 0 || adapter.probeCalls != 0 {
		t.Fatalf("calls = janitor %d close %d controller %d probe %d",
			janitorCalls, closeCalls, controllerCalls, adapter.probeCalls)
	}
}

func TestRunSuccessTransfersLockedRootAndCanonicalResolvedProvider(t *testing.T) {
	gateway := doctorTestExecutable(t)
	cfg := doctorTestConfig(t, core.ProviderCodex)
	adapter := &doctorTestAdapter{
		name:     core.ProviderCodex,
		interval: reportTestRange(),
		health:   validReadyProviderHealth(core.ProviderCodex),
	}
	dependencies := doctorTestDependencies(map[core.ProviderName]provider.Adapter{
		core.ProviderCodex: adapter,
	})
	dependencies.GatewayExecutable = gateway
	openedRoot := doctorTestOpenRoot(t, cfg.Runtime.Root)
	dependencies.OpenRoot = func(path string) (*process.Root, error) {
		if path != cfg.Runtime.Root {
			t.Fatalf("OpenRoot path = %q, want configured root", path)
		}
		return openedRoot, nil
	}
	janitorCalls := 0
	dependencies.Janitor = func(ctx context.Context, root *process.Root) error {
		janitorCalls++
		if ctx == nil || root != openedRoot {
			t.Fatal("Janitor received wrong context/root")
		}
		return nil
	}
	closeCalls := 0
	dependencies.CloseRoot = func(root *process.Root) error {
		closeCalls++
		return root.Close()
	}
	controller := &doctorTestController{}
	controllerCalls := 0
	dependencies.NewProbeController = func(
		root *process.Root,
		limits process.Limits,
		newRuntimeID func() (string, error),
	) (ProbeController, error) {
		controllerCalls++
		if root != openedRoot || newRuntimeID == nil {
			t.Fatal("NewProbeController received wrong root/ID generator")
		}
		if limits != (process.Limits{
			Execution: 5 * time.Second, TermGrace: time.Second, Cleanup: time.Second,
			StdoutBytes: 64 << 10, StderrBytes: 64 << 10,
		}) {
			t.Fatalf("limits = %+v", limits)
		}
		return controller, nil
	}
	diagnosis, err := Run(context.Background(), cfg, dependencies)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if diagnosis.RuntimeRoot != openedRoot {
		t.Fatalf("RuntimeRoot = %p, want %p", diagnosis.RuntimeRoot, openedRoot)
	}
	if !diagnosis.Report().CoreReady() || diagnosis.Report().ReadyCount() != 1 {
		t.Fatalf("report = %+v", diagnosis.Report())
	}
	resolved := diagnosis.ResolvedProviders()
	entry, present := resolved[core.ProviderCodex]
	expectedExecutable, err := resolveGatewayExecutable(cfg.Providers["codex"].Executable)
	if err != nil {
		t.Fatalf("resolve configured executable: %v", err)
	}
	if !present || entry.Health.Status != provider.HealthReady || entry.Config.LookupEnv == nil ||
		entry.Config.Executable != expectedExecutable {
		t.Fatalf("resolved provider = %+v, present %v", entry, present)
	}
	if janitorCalls != 1 || controllerCalls != 1 || controller.selfTestCalls != 1 ||
		controller.shutdownCalls != 1 || closeCalls != 0 || adapter.probeCalls != 1 {
		t.Fatalf("calls = janitor %d controller %d selftest %d shutdown %d close %d probe %d",
			janitorCalls, controllerCalls, controller.selfTestCalls, controller.shutdownCalls,
			closeCalls, adapter.probeCalls)
	}
	if err := diagnosis.RuntimeRoot.Close(); err != nil {
		t.Fatalf("close transferred root: %v", err)
	}
}

func TestRunCancellationDuringShutdownClosesInsteadOfTransferringRoot(t *testing.T) {
	gateway := doctorTestExecutable(t)
	cfg := doctorTestConfig(t, core.ProviderCodex)
	adapter := &doctorTestAdapter{
		name: core.ProviderCodex, interval: reportTestRange(),
		health: validReadyProviderHealth(core.ProviderCodex),
	}
	dependencies := doctorTestDependencies(map[core.ProviderName]provider.Adapter{
		core.ProviderCodex: adapter,
	})
	dependencies.GatewayExecutable = gateway
	openedRoot := doctorTestOpenRoot(t, cfg.Runtime.Root)
	dependencies.OpenRoot = func(string) (*process.Root, error) { return openedRoot, nil }
	shutdownStarted := make(chan struct{})
	releaseShutdown := make(chan struct{})
	controller := &doctorTestController{
		shutdown: func(cleanupCtx context.Context) error {
			close(shutdownStarted)
			<-releaseShutdown
			return cleanupCtx.Err()
		},
	}
	dependencies.NewProbeController = func(
		*process.Root,
		process.Limits,
		func() (string, error),
	) (ProbeController, error) {
		return controller, nil
	}
	closeCalls := 0
	dependencies.CloseRoot = func(root *process.Root) error {
		closeCalls++
		return root.Close()
	}
	runCtx, cancel := context.WithCancel(context.Background())
	type runResult struct {
		diagnosis Diagnosis
		err       error
	}
	result := make(chan runResult, 1)
	go func() {
		diagnosis, err := Run(runCtx, cfg, dependencies)
		result <- runResult{diagnosis: diagnosis, err: err}
	}()
	select {
	case <-shutdownStarted:
	case <-time.After(time.Second):
		t.Fatal("Shutdown did not start")
	}
	cancel()
	close(releaseShutdown)
	var got runResult
	select {
	case got = <-result:
	case <-time.After(time.Second):
		t.Fatal("Run did not return")
	}
	assertExactError(t, got.err, context.Canceled)
	if got.diagnosis.RuntimeRoot != nil || got.diagnosis.ResolvedProviders() != nil {
		t.Fatalf("canceled diagnosis transferred runtime/resolved: %p/%#v",
			got.diagnosis.RuntimeRoot, got.diagnosis.ResolvedProviders())
	}
	if closeCalls != 1 || controller.shutdownCalls != 1 {
		t.Fatalf("close/shutdown calls = %d/%d, want 1/1", closeCalls, controller.shutdownCalls)
	}
}

func TestRunShutdownContextFailureDrainsUnboundedRetryBeforeReturn(t *testing.T) {
	gateway := doctorTestExecutable(t)
	cfg := doctorTestConfig(t, core.ProviderCodex)
	adapter := &doctorTestAdapter{
		name: core.ProviderCodex, interval: reportTestRange(),
		health: validReadyProviderHealth(core.ProviderCodex),
	}
	dependencies := doctorTestDependencies(map[core.ProviderName]provider.Adapter{
		core.ProviderCodex: adapter,
	})
	dependencies.GatewayExecutable = gateway
	openedRoot := doctorTestOpenRoot(t, cfg.Runtime.Root)
	dependencies.OpenRoot = func(string) (*process.Root, error) { return openedRoot, nil }
	unboundedStarted := make(chan struct{})
	releaseDrain := make(chan struct{})
	var releaseOnce sync.Once
	release := func() {
		releaseOnce.Do(func() { close(releaseDrain) })
	}
	t.Cleanup(release)
	controller := &doctorRetryShutdownController{
		unboundedStarted: unboundedStarted,
		releaseDrain:     releaseDrain,
		unboundedErr:     errors.New("PLANTED_UNBOUNDED_SHUTDOWN_SECRET"),
	}
	dependencies.NewProbeController = func(
		*process.Root,
		process.Limits,
		func() (string, error),
	) (ProbeController, error) {
		return controller, nil
	}
	closed := make(chan struct{})
	closeCalls := 0
	var closeMu sync.Mutex
	dependencies.CloseRoot = func(root *process.Root) error {
		closeMu.Lock()
		closeCalls++
		closeMu.Unlock()
		err := root.Close()
		close(closed)
		return err
	}
	type runResult struct {
		diagnosis Diagnosis
		err       error
	}
	results := make(chan runResult, 1)
	go func() {
		diagnosis, err := Run(context.Background(), cfg, dependencies)
		results <- runResult{diagnosis: diagnosis, err: err}
	}()
	select {
	case <-unboundedStarted:
	case <-time.After(time.Second):
		t.Fatal("unbounded Shutdown retry did not start")
	}
	select {
	case result := <-results:
		t.Fatalf("Run returned before unbounded drain released: diagnosis=%+v err=%v", result.diagnosis, result.err)
	case <-time.After(200 * time.Millisecond):
	}
	closeMu.Lock()
	beforeDrain := closeCalls
	closeMu.Unlock()
	if beforeDrain != 0 {
		t.Fatalf("close calls before drain = %d, want 0", beforeDrain)
	}
	select {
	case <-closed:
		t.Fatal("root closed before drain released")
	default:
	}
	release()
	var result runResult
	select {
	case result = <-results:
	case <-time.After(time.Second):
		t.Fatal("Run did not return after unbounded drain")
	}
	if result.err != nil {
		t.Fatalf("Run() error = %v", result.err)
	}
	if result.diagnosis.RuntimeRoot != nil || result.diagnosis.ResolvedProviders() != nil {
		t.Fatalf("cleanup-failed diagnosis transferred runtime/resolved: %p/%#v",
			result.diagnosis.RuntimeRoot, result.diagnosis.ResolvedProviders())
	}
	if got := result.diagnosis.Report().Core()[6]; got != (Check{
		Name: "probe_cleanup", Status: "fail", Code: "runtime_cleanup_failed", Message: "runtime cleanup failed",
	}) {
		t.Fatalf("probe cleanup row = %+v", got)
	}
	select {
	case <-closed:
	default:
		t.Fatal("root was not closed before Run returned")
	}
	closeMu.Lock()
	afterDrain := closeCalls
	closeMu.Unlock()
	if afterDrain != 1 || controller.calls() != 2 {
		t.Fatalf("close/shutdown calls = %d/%d, want 1/2", afterDrain, controller.calls())
	}
}

func TestRunControllerAndCloseFailuresProduceCompleteClosedDiagnosis(t *testing.T) {
	tests := []struct {
		name            string
		configure       func(*testing.T, *Dependencies, *doctorTestController, *process.Root)
		wantContainment string
		wantCleanup     string
		wantShutdown    int
		wantProbe       int
	}{
		{
			name: "controller construction",
			configure: func(_ *testing.T, dependencies *Dependencies, _ *doctorTestController, _ *process.Root) {
				dependencies.NewProbeController = func(
					*process.Root,
					process.Limits,
					func() (string, error),
				) (ProbeController, error) {
					return nil, errors.New("construction")
				}
			},
			wantContainment: "fail", wantCleanup: "pass",
		},
		{
			name: "partial controller construction ownership",
			configure: func(_ *testing.T, dependencies *Dependencies, controller *doctorTestController, _ *process.Root) {
				dependencies.NewProbeController = func(
					*process.Root,
					process.Limits,
					func() (string, error),
				) (ProbeController, error) {
					return controller, errors.New("construction")
				}
			},
			wantContainment: "fail", wantCleanup: "pass", wantShutdown: 1,
		},
		{
			name: "self-test non-cleanup",
			configure: func(_ *testing.T, _ *Dependencies, controller *doctorTestController, _ *process.Root) {
				controller.selfTestErr = errors.New("selftest")
			},
			wantContainment: "fail", wantCleanup: "pass", wantShutdown: 1,
		},
		{
			name: "self-test cleanup",
			configure: func(_ *testing.T, _ *Dependencies, controller *doctorTestController, _ *process.Root) {
				controller.selfTestErr = &process.RunError{
					Kind: process.ErrorCleanup, Err: errors.New("selftest cleanup"),
				}
			},
			wantContainment: "fail", wantCleanup: "fail", wantShutdown: 1,
		},
		{
			name: "shutdown non-context",
			configure: func(_ *testing.T, _ *Dependencies, controller *doctorTestController, _ *process.Root) {
				controller.shutdownErr = errors.New("shutdown")
			},
			wantContainment: "pass", wantCleanup: "fail", wantShutdown: 1, wantProbe: 1,
		},
		{
			name: "close",
			configure: func(_ *testing.T, dependencies *Dependencies, _ *doctorTestController, _ *process.Root) {
				dependencies.NewProbeController = func(
					*process.Root,
					process.Limits,
					func() (string, error),
				) (ProbeController, error) {
					return nil, errors.New("construction")
				}
				originalClose := dependencies.CloseRoot
				dependencies.CloseRoot = func(root *process.Root) error {
					_ = originalClose(root)
					return errors.New("close")
				}
			},
			wantContainment: "fail", wantCleanup: "fail",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := doctorTestConfig(t, core.ProviderCodex)
			adapter := &doctorTestAdapter{
				name: core.ProviderCodex, interval: reportTestRange(),
				health: validReadyProviderHealth(core.ProviderCodex),
			}
			controller := &doctorTestController{}
			dependencies, root, _ := doctorTestReadyDependencies(
				t,
				cfg,
				map[core.ProviderName]provider.Adapter{core.ProviderCodex: adapter},
				controller,
			)
			test.configure(t, &dependencies, controller, root)
			diagnosis, err := Run(context.Background(), cfg, dependencies)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			checks := diagnosis.Report().Core()
			if checks[5].Status != test.wantContainment || checks[6].Status != test.wantCleanup ||
				diagnosis.RuntimeRoot != nil || diagnosis.ResolvedProviders() != nil {
				t.Fatalf("core/runtime/resolved = %+v/%p/%#v",
					checks, diagnosis.RuntimeRoot, diagnosis.ResolvedProviders())
			}
			if controller.shutdownCalls != test.wantShutdown || adapter.probeCalls != test.wantProbe {
				t.Fatalf("shutdown/probe calls = %d/%d, want %d/%d",
					controller.shutdownCalls, adapter.probeCalls, test.wantShutdown, test.wantProbe)
			}
		})
	}
}

func TestRunCleanupLatchStopsSortedProvidersAndClearsResolvedState(t *testing.T) {
	gateway := doctorTestExecutable(t)
	names := []core.ProviderName{core.ProviderGemini, core.ProviderCodex, core.ProviderClaude}
	cfg := doctorTestConfig(t, names...)
	controller := &doctorTestController{}
	adapters := make(map[core.ProviderName]provider.Adapter, len(names))
	for _, name := range names {
		name := name
		adapter := &doctorTestAdapter{
			name: name, interval: reportTestRange(), health: validReadyProviderHealth(name),
		}
		if name == core.ProviderClaude {
			adapter.probe = func(
				context.Context,
				provider.ProviderConfig,
				provider.ProbeRunner,
			) provider.Health {
				controller.mu.Lock()
				controller.cleanupFailed = true
				controller.mu.Unlock()
				return validReadyProviderHealth(core.ProviderClaude)
			}
		}
		adapters[name] = adapter
	}
	dependencies := doctorTestDependencies(adapters)
	dependencies.GatewayExecutable = gateway
	openedRoot := doctorTestOpenRoot(t, cfg.Runtime.Root)
	dependencies.OpenRoot = func(string) (*process.Root, error) { return openedRoot, nil }
	dependencies.NewProbeController = func(
		*process.Root,
		process.Limits,
		func() (string, error),
	) (ProbeController, error) {
		return controller, nil
	}
	closeCalls := 0
	dependencies.CloseRoot = func(root *process.Root) error {
		closeCalls++
		return root.Close()
	}
	diagnosis, err := Run(context.Background(), cfg, dependencies)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if diagnosis.RuntimeRoot != nil || diagnosis.ResolvedProviders() != nil || closeCalls != 1 {
		t.Fatalf("cleanup-latched runtime/resolved/close = %p/%#v/%d",
			diagnosis.RuntimeRoot, diagnosis.ResolvedProviders(), closeCalls)
	}
	rows := diagnosis.Report().Providers()
	if len(rows) != 3 || rows[0].Name != core.ProviderClaude ||
		rows[0].Status != provider.HealthReady || !coreSkippedProviderRow(rows[1]) ||
		!coreSkippedProviderRow(rows[2]) {
		t.Fatalf("provider rows = %+v", rows)
	}
	if adapters[core.ProviderClaude].(*doctorTestAdapter).probeCalls != 1 ||
		adapters[core.ProviderCodex].(*doctorTestAdapter).probeCalls != 0 ||
		adapters[core.ProviderGemini].(*doctorTestAdapter).probeCalls != 0 {
		t.Fatalf("probe calls = Claude %d Codex %d Gemini %d",
			adapters[core.ProviderClaude].(*doctorTestAdapter).probeCalls,
			adapters[core.ProviderCodex].(*doctorTestAdapter).probeCalls,
			adapters[core.ProviderGemini].(*doctorTestAdapter).probeCalls)
	}
}

func TestCanonicalizeHealthNormalizesRecognizedSetsAndFailsClosed(t *testing.T) {
	ready := validReadyProviderHealth(core.ProviderCodex)
	ready.Capabilities = append([]string{
		"stdin_prompt", "schema_file", "read_only", "never_approve",
		"feature_hardening", "ephemeral",
	}, "stdin_prompt")
	row, canonical := canonicalizeHealth(core.ProviderCodex, reportTestRange(), ready)
	if row.Status != provider.HealthReady ||
		!slices.Equal(row.Capabilities, readyCapabilities(core.ProviderCodex)) ||
		!slices.Equal(canonical.Capabilities, readyCapabilities(core.ProviderCodex)) {
		t.Fatalf("normalized ready health = %+v / %+v", row, canonical)
	}

	tests := []struct {
		name   string
		mutate func(*provider.Health)
	}{
		{"provider mismatch", func(value *provider.Health) { value.Provider = core.ProviderGemini }},
		{"unknown status", func(value *provider.Health) { value.Status = "planted" }},
		{"lying status", func(value *provider.Health) { value.Status = provider.HealthUnknown }},
		{"noncanonical version", func(value *provider.Health) { value.Version = "01.2.3" }},
		{"partial capabilities", func(value *provider.Health) { value.Capabilities = value.Capabilities[:1] }},
		{"unknown capability", func(value *provider.Health) { value.Capabilities[0] = "planted" }},
		{"unknown problem", func(value *provider.Health) { value.Problems = []string{"planted"} }},
		{"auth contradiction", func(value *provider.Health) { value.Auth = "missing" }},
	}
	want := malformedProviderRow(core.ProviderCodex)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			input := validReadyProviderHealth(core.ProviderCodex)
			test.mutate(&input)
			actual, health := canonicalizeHealth(core.ProviderCodex, reportTestRange(), input)
			if actual.Name != want.Name || actual.Status != want.Status || actual.Version != want.Version ||
				actual.Auth != want.Auth || !slices.Equal(actual.Capabilities, want.Capabilities) ||
				!slices.Equal(actual.Problems, want.Problems) ||
				health.Provider != want.Name || !slices.Equal(health.Problems, want.Problems) {
				t.Fatalf("canonical fallback = %+v / %+v, want %+v", actual, health, want)
			}
		})
	}
}

func TestCanonicalizeHealthAcceptsEveryClosedProviderRelationship(t *testing.T) {
	tests := []struct {
		name   string
		row    Provider
		accept bool
	}{
		{name: "Codex ready", row: validReadyProvider(core.ProviderCodex), accept: true},
		{name: "Claude ready", row: validReadyProvider(core.ProviderClaude), accept: true},
		{name: "Gemini ready", row: validReadyProvider(core.ProviderGemini), accept: true},
		{
			name: "Codex unknown auth",
			row: Provider{
				Name: core.ProviderCodex, Status: provider.HealthUnknown, Version: "1.2.3",
				Auth: "unknown", Capabilities: readyCapabilities(core.ProviderCodex),
				Problems: []string{provider.ProblemAuthUnknown},
			},
			accept: true,
		},
		{
			name: "Claude missing auth",
			row: Provider{
				Name: core.ProviderClaude, Status: provider.HealthNotReady, Version: "1.2.3",
				Auth: "missing", Capabilities: readyCapabilities(core.ProviderClaude),
				Problems: []string{provider.ProblemAuthMissing},
			},
			accept: true,
		},
		{
			name: "Gemini missing credential",
			row: Provider{
				Name: core.ProviderGemini, Status: provider.HealthNotReady, Version: "1.2.3",
				Auth: "missing", Capabilities: readyCapabilities(core.ProviderGemini),
				Problems: []string{provider.ProblemCredentialMissing},
			},
			accept: true,
		},
		{
			name: "Codex unsupported version",
			row: Provider{
				Name: core.ProviderCodex, Status: provider.HealthNotReady, Version: "2.0.0",
				Auth: "authenticated", Capabilities: readyCapabilities(core.ProviderCodex),
				Problems: []string{provider.ProblemVersionUnsupported},
			},
			accept: true,
		},
		{
			name: "Claude missing capability",
			row: Provider{
				Name: core.ProviderClaude, Status: provider.HealthNotReady, Version: "1.2.3",
				Auth: "authenticated", Problems: []string{provider.ProblemCapabilityMissing},
			},
			accept: true,
		},
		{
			name: "Gemini unreadable version",
			row: Provider{
				Name: core.ProviderGemini, Status: provider.HealthNotReady,
				Auth: "configured", Capabilities: readyCapabilities(core.ProviderGemini),
				Problems: []string{provider.ProblemVersionUnreadable},
			},
			accept: true,
		},
		{
			name: "Gemini unknown is forbidden",
			row: Provider{
				Name: core.ProviderGemini, Status: provider.HealthUnknown, Version: "1.2.3",
				Auth: "unknown", Capabilities: readyCapabilities(core.ProviderGemini),
				Problems: []string{provider.ProblemAuthUnknown},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			actual, _ := canonicalizeHealth(
				test.row.Name,
				reportTestRange(),
				healthFromProviderRow(test.row),
			)
			if test.accept {
				if actual.Name != test.row.Name || actual.Status != test.row.Status ||
					actual.Version != test.row.Version || actual.Auth != test.row.Auth ||
					!slices.Equal(actual.Capabilities, test.row.Capabilities) ||
					!slices.Equal(actual.Problems, test.row.Problems) {
					t.Fatalf("canonical row = %+v, want %+v", actual, test.row)
				}
				return
			}
			want := malformedProviderRow(test.row.Name)
			if actual.Status != want.Status || actual.Auth != want.Auth ||
				!slices.Equal(actual.Problems, want.Problems) {
				t.Fatalf("canonical row = %+v, want fallback %+v", actual, want)
			}
		})
	}
}

func TestRunFrozenLookupIsExactOnceIsolatedAndRetainedIndependently(t *testing.T) {
	cfg := doctorTestConfig(t, core.ProviderClaude)
	providerConfig := cfg.Providers["claude"]
	providerConfig.CredentialEnv = []string{"ANTHROPIC_API_KEY"}
	cfg.Providers["claude"] = providerConfig
	ambient := map[string]string{
		"ANTHROPIC_API_KEY": "secret-one",
		"PATH":              "planted-path",
		"HTTPS_PROXY":       "planted-proxy",
	}
	lookupCalls := make(map[string]int)
	var adapterLookup provider.LookupEnv
	adapter := &doctorTestAdapter{
		name: core.ProviderClaude, interval: reportTestRange(),
		probe: func(
			_ context.Context,
			resolved provider.ProviderConfig,
			_ provider.ProbeRunner,
		) provider.Health {
			adapterLookup = resolved.LookupEnv
			if value, present := resolved.LookupEnv("ANTHROPIC_API_KEY"); !present || value != "secret-one" {
				t.Fatalf("frozen selected lookup = %q/%v", value, present)
			}
			for _, name := range []string{"PATH", "HTTPS_PROXY", "GATEWAY_KEY", "GOOGLE_API_KEY"} {
				if value, present := resolved.LookupEnv(name); present || value != "" {
					t.Fatalf("unselected %s leaked as %q/%v", name, value, present)
				}
			}
			ambient["ANTHROPIC_API_KEY"] = "secret-two"
			return validReadyProviderHealth(core.ProviderClaude)
		},
	}
	controller := &doctorTestController{}
	dependencies, _, _ := doctorTestReadyDependencies(
		t,
		cfg,
		map[core.ProviderName]provider.Adapter{core.ProviderClaude: adapter},
		controller,
	)
	dependencies.LookupEnv = func(name string) (string, bool) {
		lookupCalls[name]++
		value, present := ambient[name]
		return value, present
	}
	diagnosis, err := Run(context.Background(), cfg, dependencies)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if lookupCalls["ANTHROPIC_API_KEY"] != 1 || len(lookupCalls) != 1 {
		t.Fatalf("ambient lookup calls = %#v, want selected exactly once", lookupCalls)
	}
	if adapterLookup == nil {
		t.Fatal("adapter did not capture lookup")
	}
	if value, present := adapterLookup("ANTHROPIC_API_KEY"); present || value != "" {
		t.Fatalf("adapter-local lookup survived unwind as %q/%v", value, present)
	}
	resolved := diagnosis.ResolvedProviders()[core.ProviderClaude]
	if value, present := resolved.Config.LookupEnv("ANTHROPIC_API_KEY"); !present || value != "secret-one" {
		t.Fatalf("transferred lookup = %q/%v, want frozen secret-one", value, present)
	}
	if err := diagnosis.RuntimeRoot.Close(); err != nil {
		t.Fatalf("close transferred root: %v", err)
	}
}

func TestRunMissingCredentialRemainsResolvedWithoutProbe(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		present bool
	}{
		{name: "missing"},
		{name: "empty", present: true},
		{name: "NUL", value: "secret\x00suffix", present: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := doctorTestConfig(t, core.ProviderClaude)
			providerConfig := cfg.Providers["claude"]
			providerConfig.CredentialEnv = []string{"ANTHROPIC_API_KEY"}
			cfg.Providers["claude"] = providerConfig
			adapter := &doctorTestAdapter{
				name: core.ProviderClaude, interval: reportTestRange(),
				health: validReadyProviderHealth(core.ProviderClaude),
			}
			dependencies, _, _ := doctorTestReadyDependencies(
				t,
				cfg,
				map[core.ProviderName]provider.Adapter{core.ProviderClaude: adapter},
				&doctorTestController{},
			)
			calls := 0
			dependencies.LookupEnv = func(name string) (string, bool) {
				calls++
				if name != "ANTHROPIC_API_KEY" {
					t.Fatalf("lookup name = %q", name)
				}
				return test.value, test.present
			}
			diagnosis, err := Run(context.Background(), cfg, dependencies)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			row := diagnosis.Report().Providers()[0]
			if row.Status != provider.HealthNotReady || row.Auth != "missing" ||
				!slices.Equal(row.Problems, []string{provider.ProblemCredentialMissing}) {
				t.Fatalf("provider row = %+v", row)
			}
			resolved, present := diagnosis.ResolvedProviders()[core.ProviderClaude]
			if !present || resolved.Health.Status != provider.HealthNotReady ||
				adapter.probeCalls != 0 || calls != 1 {
				t.Fatalf("resolved/probe/lookup = %+v/%d/%d, present %v",
					resolved, adapter.probeCalls, calls, present)
			}
			if value, present := resolved.Config.LookupEnv("ANTHROPIC_API_KEY"); present || value != "" {
				t.Fatalf("unusable lookup = %q/%v", value, present)
			}
			if err := diagnosis.RuntimeRoot.Close(); err != nil {
				t.Fatalf("close transferred root: %v", err)
			}
		})
	}
}

func TestRunGeminiEvaluatesUnsafeCredentialFileBehindMissingPrecedence(t *testing.T) {
	tests := []struct {
		name         string
		projectValue string
		projectOK    bool
		wantProblem  string
	}{
		{
			name: "unsafe file", projectValue: "project", projectOK: true,
			wantProblem: provider.ProblemCredentialFileUnsafe,
		},
		{
			name:        "missing project wins but remains unresolved",
			wantProblem: provider.ProblemCredentialMissing,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := doctorTestConfig(t, core.ProviderGemini)
			providerConfig := cfg.Providers["gemini"]
			providerConfig.CredentialEnv = []string{
				"GOOGLE_APPLICATION_CREDENTIALS",
				"GOOGLE_CLOUD_PROJECT",
				"GOOGLE_CLOUD_LOCATION",
			}
			cfg.Providers["gemini"] = providerConfig
			adapter := &doctorTestAdapter{
				name: core.ProviderGemini, interval: reportTestRange(),
				health: validReadyProviderHealth(core.ProviderGemini),
			}
			dependencies, _, _ := doctorTestReadyDependencies(
				t,
				cfg,
				map[core.ProviderName]provider.Adapter{core.ProviderGemini: adapter},
				&doctorTestController{},
			)
			calls := make(map[string]int)
			dependencies.LookupEnv = func(name string) (string, bool) {
				calls[name]++
				switch name {
				case "GOOGLE_APPLICATION_CREDENTIALS":
					return "relative-unsafe-credential.json", true
				case "GOOGLE_CLOUD_PROJECT":
					return test.projectValue, test.projectOK
				case "GOOGLE_CLOUD_LOCATION":
					return "location", true
				default:
					return "", false
				}
			}
			diagnosis, err := Run(context.Background(), cfg, dependencies)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			row := diagnosis.Report().Providers()[0]
			if !slices.Equal(row.Problems, []string{test.wantProblem}) ||
				diagnosis.ResolvedProviders() != nil || adapter.probeCalls != 0 {
				t.Fatalf("row/resolved/probes = %+v/%#v/%d",
					row, diagnosis.ResolvedProviders(), adapter.probeCalls)
			}
			for _, name := range providerConfig.CredentialEnv {
				if calls[name] != 1 {
					t.Fatalf("lookup calls[%s] = %d, want 1; all %#v", name, calls[name], calls)
				}
			}
			if err := diagnosis.RuntimeRoot.Close(); err != nil {
				t.Fatalf("close transferred root: %v", err)
			}
		})
	}
}

func TestRunGeminiFreezesOnlyResolvedSafeServiceCredentialPath(t *testing.T) {
	cfg := doctorTestConfig(t, core.ProviderGemini)
	providerConfig := cfg.Providers["gemini"]
	providerConfig.CredentialEnv = []string{
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT",
		"GOOGLE_CLOUD_LOCATION",
	}
	cfg.Providers["gemini"] = providerConfig
	credentialPath := filepath.Join(doctorTestPrivateDirectory(t), "service.json")
	testutil.WriteTrustedFile(
		t,
		credentialPath,
		[]byte("not-read-by-doctor"),
		0o600,
	)
	validatedCredential, disposition := validateCredentialPath(credentialPath)
	if disposition != pathSafe {
		t.Fatalf("credential fixture disposition = %v", disposition)
	}
	values := map[string]string{
		"GOOGLE_APPLICATION_CREDENTIALS": credentialPath,
		"GOOGLE_CLOUD_PROJECT":           "project",
		"GOOGLE_CLOUD_LOCATION":          "location",
	}
	lookupCalls := make(map[string]int)
	var adapterLookup provider.LookupEnv
	adapter := &doctorTestAdapter{
		name: core.ProviderGemini, interval: reportTestRange(),
		probe: func(
			_ context.Context,
			resolved provider.ProviderConfig,
			_ provider.ProbeRunner,
		) provider.Health {
			adapterLookup = resolved.LookupEnv
			value, present := resolved.LookupEnv("GOOGLE_APPLICATION_CREDENTIALS")
			if !present || value != validatedCredential.Resolved {
				t.Fatalf("service credential lookup = %q/%v, want resolved %q",
					value, present, validatedCredential.Resolved)
			}
			return validReadyProviderHealth(core.ProviderGemini)
		},
	}
	dependencies, _, _ := doctorTestReadyDependencies(
		t,
		cfg,
		map[core.ProviderName]provider.Adapter{core.ProviderGemini: adapter},
		&doctorTestController{},
	)
	dependencies.LookupEnv = func(name string) (string, bool) {
		lookupCalls[name]++
		value, present := values[name]
		return value, present
	}
	diagnosis, err := Run(context.Background(), cfg, dependencies)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	for _, name := range providerConfig.CredentialEnv {
		if lookupCalls[name] != 1 {
			t.Fatalf("lookup calls[%s] = %d, want 1; all %#v", name, lookupCalls[name], lookupCalls)
		}
	}
	if value, present := adapterLookup("GOOGLE_APPLICATION_CREDENTIALS"); present || value != "" {
		t.Fatalf("adapter-local service lookup survived unwind as %q/%v", value, present)
	}
	resolved := diagnosis.ResolvedProviders()[core.ProviderGemini]
	if value, present := resolved.Config.LookupEnv("GOOGLE_APPLICATION_CREDENTIALS"); !present || value != validatedCredential.Resolved {
		t.Fatalf("transferred service lookup = %q/%v", value, present)
	}
	if err := diagnosis.RuntimeRoot.Close(); err != nil {
		t.Fatalf("close transferred root: %v", err)
	}
}

func TestRunProviderPathProblemPrecedenceRunsNoProbeAndStaysUnresolved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix mode fixture; native Windows ACL policy is covered by path_windows tests")
	}
	tests := []struct {
		name        string
		mutate      func(*config.Provider)
		wantProblem string
	}{
		{
			name: "missing executable before unsafe config",
			mutate: func(value *config.Provider) {
				parent, err := filepath.EvalSymlinks(doctorTestPrivateDirectory(t))
				if err != nil {
					t.Fatalf("resolve missing-path parent: %v", err)
				}
				value.Executable = filepath.Join(parent, "missing")
				unsafeHome := doctorTestPrivateDirectory(t)
				//nolint:gosec // Deliberately unsafe directory fixture.
				if err := os.Chmod(unsafeHome, 0o755); err != nil {
					t.Fatalf("chmod unsafe home: %v", err)
				}
				value.ConfigHome = unsafeHome
			},
			wantProblem: provider.ProblemExecutableMissing,
		},
		{
			name: "unsafe executable before unsafe config",
			mutate: func(value *config.Provider) {
				value.Executable = doctorTestPrivateDirectory(t)
				unsafeHome := doctorTestPrivateDirectory(t)
				//nolint:gosec // Deliberately unsafe directory fixture.
				if err := os.Chmod(unsafeHome, 0o755); err != nil {
					t.Fatalf("chmod unsafe home: %v", err)
				}
				value.ConfigHome = unsafeHome
			},
			wantProblem: provider.ProblemExecutableUnsafe,
		},
		{
			name: "unsafe config",
			mutate: func(value *config.Provider) {
				unsafeHome := doctorTestPrivateDirectory(t)
				//nolint:gosec // Deliberately unsafe directory fixture.
				if err := os.Chmod(unsafeHome, 0o755); err != nil {
					t.Fatalf("chmod unsafe home: %v", err)
				}
				value.ConfigHome = unsafeHome
			},
			wantProblem: provider.ProblemConfigHomeUnsafe,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			cfg := doctorTestConfig(t, core.ProviderCodex)
			providerConfig := cfg.Providers["codex"]
			test.mutate(&providerConfig)
			cfg.Providers["codex"] = providerConfig
			adapter := &doctorTestAdapter{
				name: core.ProviderCodex, interval: reportTestRange(),
				health: validReadyProviderHealth(core.ProviderCodex),
			}
			dependencies, _, _ := doctorTestReadyDependencies(
				t,
				cfg,
				map[core.ProviderName]provider.Adapter{core.ProviderCodex: adapter},
				&doctorTestController{},
			)
			diagnosis, err := Run(context.Background(), cfg, dependencies)
			if err != nil {
				t.Fatalf("Run() error = %v", err)
			}
			row := diagnosis.Report().Providers()[0]
			if !slices.Equal(row.Problems, []string{test.wantProblem}) ||
				diagnosis.ResolvedProviders() != nil || adapter.probeCalls != 0 {
				t.Fatalf("row/resolved/probes = %+v/%#v/%d",
					row, diagnosis.ResolvedProviders(), adapter.probeCalls)
			}
			if err := diagnosis.RuntimeRoot.Close(); err != nil {
				t.Fatalf("close transferred root: %v", err)
			}
		})
	}
}

type doctorRetryShutdownController struct {
	mu               sync.Mutex
	shutdownCalls    int
	unboundedStarted chan struct{}
	releaseDrain     chan struct{}
	unboundedErr     error
}

func (*doctorRetryShutdownController) RunProbe(
	context.Context,
	func(process.Runtime) (process.CommandSpec, error),
) (process.Result, error) {
	return process.Result{}, nil
}

func (*doctorRetryShutdownController) SelfTest(context.Context, string) error { return nil }

func (c *doctorRetryShutdownController) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	c.shutdownCalls++
	call := c.shutdownCalls
	c.mu.Unlock()
	if call == 1 {
		if _, hasDeadline := ctx.Deadline(); !hasDeadline {
			return errors.New("synchronous shutdown lacked deadline")
		}
		return &process.RunError{Kind: process.ErrorCleanup, Err: context.DeadlineExceeded}
	}
	close(c.unboundedStarted)
	<-c.releaseDrain
	return c.unboundedErr
}

func (*doctorRetryShutdownController) CleanupFailed() bool { return false }

func (c *doctorRetryShutdownController) calls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.shutdownCalls
}

type doctorTestAdapter struct {
	name       core.ProviderName
	interval   provider.Range
	health     provider.Health
	nameCalls  int
	rangeCalls int
	probeCalls int
	probe      func(context.Context, provider.ProviderConfig, provider.ProbeRunner) provider.Health
}

type doctorTestController struct {
	mu            sync.Mutex
	selfTestCalls int
	shutdownCalls int
	runProbeCalls int
	cleanupFailed bool
	selfTestErr   error
	shutdownErr   error
	shutdown      func(context.Context) error
}

func (c *doctorTestController) RunProbe(
	context.Context,
	func(process.Runtime) (process.CommandSpec, error),
) (process.Result, error) {
	c.mu.Lock()
	c.runProbeCalls++
	c.mu.Unlock()
	return process.Result{}, nil
}

func (c *doctorTestController) SelfTest(context.Context, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.selfTestCalls++
	return c.selfTestErr
}

func (c *doctorTestController) Shutdown(ctx context.Context) error {
	c.mu.Lock()
	c.shutdownCalls++
	shutdown := c.shutdown
	err := c.shutdownErr
	c.mu.Unlock()
	if shutdown != nil {
		return shutdown(ctx)
	}
	return err
}

func (c *doctorTestController) CleanupFailed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.cleanupFailed
}

func (a *doctorTestAdapter) Name() core.ProviderName {
	a.nameCalls++
	return a.name
}

func (a *doctorTestAdapter) SupportedVersion() provider.Range {
	a.rangeCalls++
	return a.interval
}

func (a *doctorTestAdapter) Probe(
	ctx context.Context,
	cfg provider.ProviderConfig,
	runner provider.ProbeRunner,
) provider.Health {
	a.probeCalls++
	if a.probe != nil {
		return a.probe(ctx, cfg, runner)
	}
	return a.health.Clone()
}

func (*doctorTestAdapter) Build(
	core.Request,
	core.Model,
	provider.ProviderConfig,
	process.Runtime,
) (process.CommandSpec, error) {
	return process.CommandSpec{}, nil
}

func (*doctorTestAdapter) Parse(core.Request, process.Result) (string, error) {
	return "", nil
}

func doctorTestDependencies(adapters map[core.ProviderName]provider.Adapter) Dependencies {
	return Dependencies{
		Adapters:     adapters,
		LookupEnv:    func(string) (string, bool) { return "", false },
		NewRuntimeID: func() (string, error) { return "runtime-id", nil },
		OpenRoot:     func(string) (*process.Root, error) { return nil, errors.New("open") },
		Janitor:      func(context.Context, *process.Root) error { return nil },
		CloseRoot:    func(*process.Root) error { return nil },
		NewProbeController: func(
			*process.Root,
			process.Limits,
			func() (string, error),
		) (ProbeController, error) {
			return nil, errors.New("controller")
		},
	}
}

func doctorTestReadyDependencies(
	t *testing.T,
	cfg config.Config,
	adapters map[core.ProviderName]provider.Adapter,
	controller ProbeController,
) (Dependencies, *process.Root, *int) {
	t.Helper()
	root := doctorTestOpenRoot(t, cfg.Runtime.Root)
	t.Cleanup(func() { _ = root.Close() })
	closeCalls := new(int)
	dependencies := doctorTestDependencies(adapters)
	dependencies.GatewayExecutable = doctorTestExecutable(t)
	dependencies.OpenRoot = func(path string) (*process.Root, error) {
		if path != cfg.Runtime.Root {
			t.Fatalf("OpenRoot path = %q, want %q", path, cfg.Runtime.Root)
		}
		return root, nil
	}
	dependencies.CloseRoot = func(actual *process.Root) error {
		*closeCalls++
		return actual.Close()
	}
	dependencies.NewProbeController = func(
		actual *process.Root,
		limits process.Limits,
		newRuntimeID func() (string, error),
	) (ProbeController, error) {
		if actual != root || limits != doctorProbeLimits() || newRuntimeID == nil {
			t.Fatalf("controller construction args = %p/%+v/%v", actual, limits, newRuntimeID != nil)
		}
		return controller, nil
	}
	return dependencies, root, closeCalls
}

func doctorTestConfig(t *testing.T, names ...core.ProviderName) config.Config {
	t.Helper()
	providers := make(map[string]config.Provider, len(names))
	models := make([]config.Model, 0, len(names))
	for _, name := range names {
		providers[string(name)] = config.Provider{
			Executable:       doctorTestExecutable(t),
			ConfigHome:       doctorTestPrivateDirectory(t),
			Concurrency:      1,
			QueueSize:        1,
			QueueBytes:       1,
			QueueTimeout:     config.Duration(time.Second),
			ExecutionTimeout: config.Duration(time.Second),
		}
		models = append(models, config.Model{
			ID:            "model-" + string(name),
			Provider:      string(name),
			ProviderModel: "trusted-model",
		})
	}
	return config.Config{
		Server: config.Server{Listen: "127.0.0.1:8080"},
		Runtime: config.Runtime{
			Root: doctorTestPrivateDirectory(t),
		},
		Providers: providers,
		Models:    models,
	}
}

func doctorTestExecutable(t *testing.T) string {
	t.Helper()
	directory := doctorTestPrivateDirectory(t)
	path := filepath.Join(directory, "trusted-executable")
	//nolint:gosec // Executable fixture needs an execute bit.
	testutil.WriteTrustedFile(t, path, []byte("test"), 0o700)
	if _, err := resolveGatewayExecutable(path); err != nil {
		t.Fatalf("test executable failed doctor policy: %v", err)
	}
	return path
}

func doctorTestPrivateDirectory(t *testing.T) string {
	t.Helper()
	directory := testutil.TrustedTempDir(t)
	//nolint:gosec // This is the required private directory mode, not a file mode.
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatalf("chmod private directory: %v", err)
	}
	return directory
}

func doctorTestOpenRoot(t *testing.T, path string) *process.Root {
	t.Helper()
	root, err := process.OpenRoot(path)
	if err != nil {
		t.Fatalf("OpenRoot test fixture: %v", err)
	}
	return root
}

func validReadyProviderHealth(name core.ProviderName) provider.Health {
	row := validReadyProvider(name)
	return provider.Health{
		Provider: row.Name, Status: row.Status, Version: row.Version, Auth: row.Auth,
		Capabilities: slices.Clone(row.Capabilities), Problems: slices.Clone(row.Problems),
	}
}

type doctorTestProbeSupervisor struct {
	mu             sync.Mutex
	trace          []string
	discardErr     error
	prepareErr     error
	executeErr     error
	selfTestErr    error
	executeStarted chan struct{}
	releaseExecute chan struct{}
	shutdown       func(context.Context) error
}

func (s *doctorTestProbeSupervisor) record(value string) {
	s.mu.Lock()
	s.trace = append(s.trace, value)
	s.mu.Unlock()
}

func (s *doctorTestProbeSupervisor) traceCopy() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.trace)
}

func (s *doctorTestProbeSupervisor) Prepare(string) (process.Runtime, error) {
	s.record("prepare")
	return process.Runtime{}, s.prepareErr
}

func (s *doctorTestProbeSupervisor) Discard(context.Context, process.Runtime) error {
	s.record("discard")
	return s.discardErr
}

func (s *doctorTestProbeSupervisor) Execute(
	context.Context,
	process.Runtime,
	process.CommandSpec,
) (process.Result, error) {
	s.record("execute")
	if s.executeStarted != nil {
		close(s.executeStarted)
	}
	if s.releaseExecute != nil {
		<-s.releaseExecute
	}
	return process.Result{}, s.executeErr
}

func (s *doctorTestProbeSupervisor) SelfTest(context.Context, string) error {
	s.record("selftest")
	return s.selfTestErr
}

func (s *doctorTestProbeSupervisor) Shutdown(ctx context.Context) error {
	s.record("shutdown")
	if s.shutdown != nil {
		return s.shutdown(ctx)
	}
	return nil
}

func TestReportBuilderOwnsProvenanceAndAdvancesOnlyInOrder(t *testing.T) {
	expectedProviders := []core.ProviderName{
		core.ProviderGemini,
		core.ProviderCodex,
		core.ProviderClaude,
	}
	expectedModels := []string{"model-z", "model-a"}
	ranges := reportTestRanges(expectedProviders)
	builder := newReportBuilder(expectedProviders, expectedModels, ranges)

	expectedProviders[0] = "planted-provider"
	expectedModels[0] = "planted-model"
	ranges[core.ProviderCodex] = provider.Range{}
	if !builder.report.constructed || builder.report.phase != reportPhaseUnconstructed {
		t.Fatalf("new builder state = constructed %v, phase %v", builder.report.constructed, builder.report.phase)
	}
	if got, want := builder.report.expectedProviders, []core.ProviderName{
		core.ProviderClaude,
		core.ProviderCodex,
		core.ProviderGemini,
	}; !slices.Equal(got, want) {
		t.Fatalf("expected providers = %q, want %q", got, want)
	}
	if got, want := builder.report.expectedModels, []string{"model-a", "model-z"}; !slices.Equal(got, want) {
		t.Fatalf("expected models = %q, want %q", got, want)
	}
	if got := builder.report.expectedRanges[core.ProviderCodex]; got != reportTestRange() {
		t.Fatalf("retained Codex range = %+v, want %+v", got, reportTestRange())
	}

	coreRows := validCoreChecks()
	if err := builder.setCore(coreRows); err != nil {
		t.Fatalf("setCore() error = %v", err)
	}
	coreRows[0] = Check{Name: "planted-core", Status: "fail"}
	if builder.report.phase != reportPhaseCore || builder.report.core[0].Name != "listener" {
		t.Fatalf("core phase/storage = %v/%+v", builder.report.phase, builder.report.core)
	}

	providerRows := []Provider{
		validReadyProvider(core.ProviderGemini),
		validReadyProvider(core.ProviderCodex),
		validReadyProvider(core.ProviderClaude),
	}
	if err := builder.setProviders(providerRows); err != nil {
		t.Fatalf("setProviders() error = %v", err)
	}
	providerRows[0].Capabilities[0] = "planted-capability"
	if builder.report.phase != reportPhaseProviders {
		t.Fatalf("provider phase = %v, want %v", builder.report.phase, reportPhaseProviders)
	}
	if got, want := builder.report.providers[0].Name, core.ProviderClaude; got != want {
		t.Fatalf("first provider = %q, want %q", got, want)
	}
	if builder.report.providers[2].Capabilities[0] == "planted-capability" {
		t.Fatal("provider capabilities alias caller storage")
	}

	actualModels := []string{"model-z", "model-a"}
	report, err := builder.complete(actualModels)
	if err != nil {
		t.Fatalf("complete() error = %v", err)
	}
	actualModels[0] = "planted-actual-model"
	if !report.constructed || report.phase != reportPhaseComplete {
		t.Fatalf("complete report state = constructed %v, phase %v", report.constructed, report.phase)
	}
	if got, want := report.Models(), []string{"model-a", "model-z"}; !slices.Equal(got, want) {
		t.Fatalf("models = %q, want %q", got, want)
	}

	returned := report.Providers()
	returned[0].Name = "planted-provider"
	returned[0].Capabilities[0] = "planted-capability"
	if again := report.Providers(); again[0].Name != core.ProviderClaude ||
		again[0].Capabilities[0] == "planted-capability" {
		t.Fatalf("Providers() did not clone: %+v", again[0])
	}
	checks := report.Core()
	checks[0] = Check{Name: "planted-core"}
	if report.Core()[0].Name != "listener" {
		t.Fatal("Core() did not clone")
	}
	models := report.Models()
	models[0] = "planted-model"
	if report.Models()[0] != "model-a" {
		t.Fatal("Models() did not clone")
	}
	problemReport := report.clone()
	problemReport.providers[0] = proofProvider(
		core.ProviderClaude,
		proofVersionUnreadable,
		false,
		proofAuthUnknown,
	)
	problemView := problemReport.Providers()
	problemView[0].Problems[0] = "planted-problem"
	if problemReport.Providers()[0].Problems[0] == "planted-problem" {
		t.Fatal("Providers() did not clone Problems")
	}
}

func TestReportBuilderRejectsOutOfOrderOrRepeatedPhases(t *testing.T) {
	newBuilder := func() *reportBuilder {
		providers := []core.ProviderName{core.ProviderCodex}
		return newReportBuilder(providers, []string{"model-a"}, reportTestRanges(providers))
	}

	tests := []struct {
		name string
		act  func(*reportBuilder) error
	}{
		{
			name: "providers before core",
			act: func(builder *reportBuilder) error {
				return builder.setProviders([]Provider{validReadyProvider(core.ProviderCodex)})
			},
		},
		{
			name: "core twice",
			act: func(builder *reportBuilder) error {
				if err := builder.setCore(validCoreChecks()); err != nil {
					t.Fatalf("first setCore() error = %v", err)
				}
				return builder.setCore(validCoreChecks())
			},
		},
		{
			name: "complete before providers",
			act: func(builder *reportBuilder) error {
				if err := builder.setCore(validCoreChecks()); err != nil {
					t.Fatalf("setCore() error = %v", err)
				}
				_, err := builder.complete([]string{"model-a"})
				return err
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertExactError(t, test.act(newBuilder()), ErrInvalidReport)
		})
	}
}

func TestDiagnosisAndResolvedProviderAccessorsDefensivelyClone(t *testing.T) {
	report := newCompleteReport(t, validCoreChecks(), []Provider{
		validReadyProvider(core.ProviderCodex),
	}, []string{"model-a"})
	registry, err := core.NewRegistry([]core.Model{{
		ID:            "model-a",
		Provider:      core.ProviderCodex,
		ProviderModel: "trusted-model",
	}})
	if err != nil {
		t.Fatalf("NewRegistry() error = %v", err)
	}
	resolved := ResolvedProvider{
		Config: provider.ProviderConfig{
			Executable:    "/trusted/codex",
			PrefixArgs:    []string{"trusted-prefix"},
			CredentialEnv: []string{"TRUSTED_ENV"},
		},
		Health: provider.Health{
			Provider:     core.ProviderCodex,
			Status:       provider.HealthReady,
			Version:      "1.2.3",
			Auth:         "authenticated",
			Capabilities: readyCapabilities(core.ProviderCodex),
			Problems:     []string{"trusted-problem"},
		},
	}
	diagnosis := Diagnosis{
		report:    report,
		providers: map[core.ProviderName]ResolvedProvider{core.ProviderCodex: resolved},
		registry:  registry,
	}

	cloned := resolved.Clone()
	cloned.Config.PrefixArgs[0] = "planted-prefix"
	cloned.Config.CredentialEnv[0] = "PLANTED_ENV"
	cloned.Health.Capabilities[0] = "planted-capability"
	cloned.Health.Problems[0] = "planted-problem"
	if resolved.Config.PrefixArgs[0] != "trusted-prefix" ||
		resolved.Config.CredentialEnv[0] != "TRUSTED_ENV" ||
		resolved.Health.Capabilities[0] == "planted-capability" ||
		resolved.Health.Problems[0] != "trusted-problem" {
		t.Fatal("ResolvedProvider.Clone() aliases mutable slices")
	}

	providers := diagnosis.ResolvedProviders()
	entry := providers[core.ProviderCodex]
	entry.Config.PrefixArgs[0] = "planted-prefix"
	entry.Health.Capabilities[0] = "planted-capability"
	providers[core.ProviderCodex] = entry
	delete(providers, core.ProviderCodex)
	again := diagnosis.ResolvedProviders()[core.ProviderCodex]
	if again.Config.PrefixArgs[0] != "trusted-prefix" ||
		again.Health.Capabilities[0] == "planted-capability" {
		t.Fatalf("ResolvedProviders() did not clone: %+v", again)
	}

	returnedReport := diagnosis.Report()
	returnedReport.expectedProviders[0] = core.ProviderGemini
	returnedReport.expectedModels[0] = "planted-provenance"
	returnedReport.expectedRanges[core.ProviderCodex] = provider.Range{}
	if got := diagnosis.Report(); got.expectedProviders[0] != core.ProviderCodex ||
		got.expectedModels[0] != "model-a" ||
		got.expectedRanges[core.ProviderCodex] != reportTestRange() {
		t.Fatal("Diagnosis.Report() did not clone private provenance")
	}
	if diagnosis.Registry() != registry {
		t.Fatal("Registry() did not preserve the canonical immutable pointer")
	}
	registryModels := diagnosis.Registry().Models()
	registryModels[0].ID = "planted-model"
	if diagnosis.Registry().Models()[0].ID != "model-a" {
		t.Fatal("Registry.Models() did not clone")
	}
}

func TestZeroValuesReturnEmptyDefensiveViews(t *testing.T) {
	var report Report
	if report.Core() != nil || report.Providers() != nil || report.Models() != nil {
		t.Fatal("zero Report accessors must return nil views")
	}
	if report.CoreReady() || report.ReadyCount() != 0 {
		t.Fatalf("zero readiness = %v/%d, want false/0", report.CoreReady(), report.ReadyCount())
	}

	var diagnosis Diagnosis
	zeroReport := diagnosis.Report()
	if zeroReport.constructed || zeroReport.phase != reportPhaseUnconstructed ||
		zeroReport.Core() != nil || zeroReport.Providers() != nil || zeroReport.Models() != nil ||
		diagnosis.ResolvedProviders() != nil || diagnosis.Registry() != nil {
		t.Fatal("zero Diagnosis accessors returned non-zero state")
	}
}

func newCompleteReport(
	t *testing.T,
	coreRows []Check,
	providerRows []Provider,
	models []string,
) Report {
	t.Helper()
	expectedProviders := make([]core.ProviderName, len(providerRows))
	for index := range providerRows {
		expectedProviders[index] = providerRows[index].Name
	}
	builder := newReportBuilder(
		expectedProviders,
		append([]string(nil), models...),
		reportTestRanges(expectedProviders),
	)
	if err := builder.setCore(coreRows); err != nil {
		t.Fatalf("setCore() error = %v", err)
	}
	if err := builder.setProviders(providerRows); err != nil {
		t.Fatalf("setProviders() error = %v", err)
	}
	report, err := builder.complete(models)
	if err != nil {
		t.Fatalf("complete() error = %v", err)
	}
	return report
}

func validCoreChecks() []Check {
	return []Check{
		{Name: "listener", Status: "pass"},
		{Name: "gateway_auth", Status: "pass"},
		{Name: "scheduler", Status: "pass"},
		{Name: "runtime_root", Status: "pass"},
		{Name: "runtime_janitor", Status: "pass"},
		{Name: "containment", Status: "pass"},
		{Name: "probe_cleanup", Status: "pass"},
	}
}

func validReadyProvider(name core.ProviderName) Provider {
	auth := "authenticated"
	capabilities := readyCapabilities(name)
	switch name {
	case core.ProviderClaude, core.ProviderCodex:
	case core.ProviderGemini:
		auth = "configured"
	default:
		panic("test helper received unknown provider")
	}
	return Provider{
		Name:         name,
		Status:       provider.HealthReady,
		Version:      "1.2.3",
		Auth:         auth,
		Capabilities: capabilities,
	}
}

func reportTestRanges(names []core.ProviderName) map[core.ProviderName]provider.Range {
	ranges := make(map[core.ProviderName]provider.Range, len(names))
	for _, name := range names {
		ranges[name] = reportTestRange()
	}
	return ranges
}

func reportTestRange() provider.Range {
	return provider.Range{
		MinInclusive: provider.Version{Major: 1},
		MaxExclusive: provider.Version{Major: 2},
	}
}

func assertExactError(t *testing.T, got, want error) {
	t.Helper()
	// Exact identity is part of the public redaction contract.
	//nolint:errorlint
	if got != want {
		t.Fatalf("error = %v, want exact %v", got, want)
	}
	if got != nil && errors.Unwrap(got) != nil {
		t.Fatalf("error %v unexpectedly wraps another error", got)
	}
}
