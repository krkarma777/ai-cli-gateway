package app

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/configsource"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/doctor"
	"github.com/krkarma777/ai-cli-gateway/internal/gatewaykey"
	"github.com/krkarma777/ai-cli-gateway/internal/httpapi"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

func TestServeLoadsConfigurationBeforeValidatingDependencies(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "PLANTED_CONFIG_PATH_SECRET.toml")

	err := Serve(context.Background(), secretPath, Dependencies{})

	if err != ErrConfigInvalid { //nolint:errorlint // This boundary promises exact identity.
		t.Fatalf("Serve() error = %v, want exact %v", err, ErrConfigInvalid)
	}
}

func TestDoctorReportsOnlyFixedConfigurationFailureBeforeDependencies(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "PLANTED_DOCTOR_PATH_SECRET.toml")
	var stdout bytes.Buffer

	code := Doctor(context.Background(), secretPath, false, &stdout, Dependencies{})

	if code != 2 {
		t.Fatalf("Doctor() code = %d, want 2", code)
	}
	if got, want := stdout.String(), "configuration_invalid\n"; got != want {
		t.Fatalf("Doctor() output = %q, want %q", got, want)
	}
}

func TestProductionDependenciesAreCompleteAndLazy(t *testing.T) {
	deps := ProductionDependencies(panicWriter{})

	if deps.LookupEnv == nil || deps.LookupExecutable == nil || deps.NewRuntimeID == nil ||
		deps.GatewayExecutable == nil || deps.OpenRoot == nil ||
		deps.Janitor == nil || deps.CloseRoot == nil ||
		deps.NewProbeController == nil || deps.NewHTTPIDs == nil ||
		deps.Now == nil || deps.Listen == nil || deps.Logger == nil {
		t.Fatal("ProductionDependencies() returned an incomplete dependency graph")
	}
	names := make([]core.ProviderName, 0, len(deps.Adapters))
	for name, adapter := range deps.Adapters {
		if adapter == nil || adapter.Name() != name {
			t.Fatalf("adapter[%q] = %#v", name, adapter)
		}
		names = append(names, name)
	}
	slices.Sort(names)
	want := []core.ProviderName{core.ProviderClaude, core.ProviderCodex, core.ProviderGemini}
	slices.Sort(want)
	if !slices.Equal(names, want) {
		t.Fatalf("adapter names = %q, want %q", names, want)
	}
}

func TestServeTreatsPreCanceledContextAsCleanStopAfterConfiguration(t *testing.T) {
	configPath := writeStructurallyValidConfig(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Serve(ctx, configPath, Dependencies{})

	if err != nil {
		t.Fatalf("Serve() error = %v, want clean stop", err)
	}
}

func TestServeCallsGatewayExecutableOnceAfterSelectingConfiguredAdapters(t *testing.T) {
	configPath := writeStructurallyValidConfig(t)
	deps := ProductionDependencies(io.Discard)
	executableCalls := 0
	deps.GatewayExecutable = func() (string, error) {
		executableCalls++
		return "", errors.New("PLANTED_EXECUTABLE_SECRET")
	}

	err := Serve(context.Background(), configPath, deps)

	if err != ErrStartup { //nolint:errorlint // This boundary promises exact identity.
		t.Fatalf("Serve() error = %v, want exact %v", err, ErrStartup)
	}
	if executableCalls != 1 {
		t.Fatalf("GatewayExecutable calls = %d, want 1", executableCalls)
	}
}

func TestServeDoesNotMaskUnrelatedDoctorErrorWhenContextIsAlsoCanceled(t *testing.T) {
	configPath := writeStructurallyValidConfig(t)
	deps := ProductionDependencies(io.Discard)
	ctx, cancel := context.WithCancel(context.Background())
	deps.GatewayExecutable = func() (string, error) {
		cancel()
		return "relative-invalid-gateway", nil
	}

	err := Serve(ctx, configPath, deps)

	if err != ErrStartup { //nolint:errorlint // Unrelated doctor failure maps to exact startup sentinel.
		t.Fatalf("Serve() error = %v, want exact %v", err, ErrStartup)
	}
}

func TestServeTreatsMatchingDoctorCancellationAsCleanStop(t *testing.T) {
	fixture := newReadyAppFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	fixture.controller.selfTestHook = cancel

	err := Serve(ctx, fixture.configPath, fixture.deps)

	if err != nil {
		t.Fatalf("Serve() error = %v, want clean matching cancellation", err)
	}
	if fixture.openCalls != 1 || fixture.janitorCalls != 1 || fixture.closeCalls != 1 ||
		fixture.controller.shutdownCalls != 1 {
		t.Fatalf(
			"open/janitor/close/controller shutdown calls = %d/%d/%d/%d, want 1/1/1/1",
			fixture.openCalls,
			fixture.janitorCalls,
			fixture.closeCalls,
			fixture.controller.shutdownCalls,
		)
	}
	if fixture.httpIDCalls != 0 || fixture.listenCalls != 0 {
		t.Fatalf("HTTP ID/listen calls = %d/%d, want 0/0", fixture.httpIDCalls, fixture.listenCalls)
	}
}

func TestDoctorUsesRealDiagnosisWritesOneReportAndClosesTransferredRoot(t *testing.T) {
	fixture := newReadyAppFixture(t)
	var stdout bytes.Buffer

	code := Doctor(context.Background(), fixture.configPath, false, &stdout, fixture.deps)

	if code != 0 {
		t.Fatalf("Doctor() code = %d, want 0; output=%q", code, stdout.String())
	}
	want := "core:\n" +
		"listener\tpass\t-\t-\n" +
		"gateway_auth\tpass\t-\t-\n" +
		"scheduler\tpass\t-\t-\n" +
		"runtime_root\tpass\t-\t-\n" +
		"runtime_janitor\tpass\t-\t-\n" +
		"containment\tpass\t-\t-\n" +
		"probe_cleanup\tpass\t-\t-\n" +
		"providers:\n" +
		"codex\tready\t1.2.3\tauthenticated\t" +
		"ephemeral,feature_hardening,never_approve,read_only,schema_file,stdin_prompt\t-\n" +
		"models:\n" +
		"codex-test\n"
	if got := stdout.String(); got != want {
		t.Fatalf("Doctor() output = %q, want %q", got, want)
	}
	if fixture.executableCalls != 1 || fixture.openCalls != 1 ||
		fixture.janitorCalls != 1 || fixture.closeCalls != 1 {
		t.Fatalf("dependency calls executable/open/janitor/close = %d/%d/%d/%d, want 1/1/1/1",
			fixture.executableCalls, fixture.openCalls, fixture.janitorCalls, fixture.closeCalls)
	}
	if fixture.controller.selfTestCalls != 1 || fixture.controller.shutdownCalls != 1 {
		t.Fatalf("controller self-test/shutdown calls = %d/%d, want 1/1",
			fixture.controller.selfTestCalls, fixture.controller.shutdownCalls)
	}
}

func TestDoctorDoesNotRequireServeOnlyDependencies(t *testing.T) {
	fixture := newReadyAppFixture(t)
	fixture.deps.NewHTTPIDs = nil
	fixture.deps.Now = nil
	fixture.deps.Listen = nil
	fixture.deps.Logger = nil
	var stdout bytes.Buffer

	code := Doctor(context.Background(), fixture.configPath, true, &stdout, fixture.deps)

	if code != 0 {
		t.Fatalf("Doctor() code = %d, want 0; output=%q", code, stdout.String())
	}
	if fixture.closeCalls != 1 {
		t.Fatalf("CloseRoot calls = %d, want 1", fixture.closeCalls)
	}
}

func TestDoctorFixedExitOutputAndRootOwnershipMatrix(t *testing.T) {
	t.Run("ready JSON report is exact", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		var stdout bytes.Buffer

		code := Doctor(context.Background(), fixture.configPath, true, &stdout, fixture.deps)

		want := "{\"core\":[" +
			"{\"name\":\"listener\",\"status\":\"pass\"}," +
			"{\"name\":\"gateway_auth\",\"status\":\"pass\"}," +
			"{\"name\":\"scheduler\",\"status\":\"pass\"}," +
			"{\"name\":\"runtime_root\",\"status\":\"pass\"}," +
			"{\"name\":\"runtime_janitor\",\"status\":\"pass\"}," +
			"{\"name\":\"containment\",\"status\":\"pass\"}," +
			"{\"name\":\"probe_cleanup\",\"status\":\"pass\"}]," +
			"\"providers\":[{\"name\":\"codex\",\"status\":\"ready\"," +
			"\"version\":\"1.2.3\",\"auth\":\"authenticated\"," +
			"\"capabilities\":[\"ephemeral\",\"feature_hardening\"," +
			"\"never_approve\",\"read_only\",\"schema_file\",\"stdin_prompt\"]}]," +
			"\"models\":[\"codex-test\"]}\n"
		if code != 0 || stdout.String() != want {
			t.Fatalf("Doctor() = code %d output %q, want code 0 output %q", code, stdout.String(), want)
		}
		if fixture.closeCalls != 1 {
			t.Fatalf("CloseRoot calls = %d, want 1", fixture.closeCalls)
		}
	})

	t.Run("core unsafe writes report only without acquiring root", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		addFixtureGatewayKey(t, fixture.configPath, "APP_TEST_MISSING_GATEWAY_KEY")
		fixture.deps.LookupEnv = func(string) (string, bool) { return "", false }
		var stdout bytes.Buffer

		code := Doctor(context.Background(), fixture.configPath, false, &stdout, fixture.deps)

		if code != 1 || !strings.Contains(
			stdout.String(),
			"gateway_auth\tfail\tgateway_key_missing\tgateway authentication is unavailable\n",
		) || strings.Contains(stdout.String(), "doctor_failed") {
			t.Fatalf("Doctor() = code %d output %q", code, stdout.String())
		}
		if fixture.openCalls != 0 || fixture.closeCalls != 0 {
			t.Fatalf("open/close calls = %d/%d, want 0/0", fixture.openCalls, fixture.closeCalls)
		}
	})

	t.Run("zero ready writes report only and exits one", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		adapter := fixture.deps.Adapters[core.ProviderCodex].(*appTestAdapter)
		adapter.health = provider.Health{
			Provider:     core.ProviderCodex,
			Status:       provider.HealthNotReady,
			Version:      "1.2.3",
			Auth:         "missing",
			Capabilities: slices.Clone(adapter.health.Capabilities),
			Problems:     []string{provider.ProblemAuthMissing},
		}
		var stdout bytes.Buffer

		code := Doctor(context.Background(), fixture.configPath, false, &stdout, fixture.deps)

		if code != 1 || !strings.HasPrefix(stdout.String(), "core:\n") ||
			strings.Contains(stdout.String(), "doctor_failed") {
			t.Fatalf("Doctor() = code %d output %q", code, stdout.String())
		}
		if fixture.closeCalls != 1 || fixture.janitorCalls != 1 {
			t.Fatalf("close/janitor calls = %d/%d, want 1/1", fixture.closeCalls, fixture.janitorCalls)
		}
	})

	t.Run("short report write still closes root without fallback output", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		writer := &appShortWriter{}

		code := Doctor(context.Background(), fixture.configPath, true, writer, fixture.deps)

		if code != 1 || writer.calls != 1 {
			t.Fatalf("Doctor() code/writes = %d/%d, want 1/1", code, writer.calls)
		}
		if fixture.closeCalls != 1 {
			t.Fatalf("CloseRoot calls = %d, want 1", fixture.closeCalls)
		}
	})

	t.Run("writer error still closes root without fallback output", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		writer := &appErrorWriter{}

		code := Doctor(context.Background(), fixture.configPath, true, writer, fixture.deps)

		if code != 1 || writer.calls != 1 {
			t.Fatalf("Doctor() code/writes = %d/%d, want 1/1", code, writer.calls)
		}
		if fixture.closeCalls != 1 {
			t.Fatalf("CloseRoot calls = %d, want 1", fixture.closeCalls)
		}
	})

	t.Run("close failure changes only exit after valid report", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		originalClose := fixture.deps.CloseRoot
		fixture.deps.CloseRoot = func(root *process.Root) error {
			if err := originalClose(root); err != nil {
				return err
			}
			return errors.New("PLANTED_CLOSE_SECRET")
		}
		var stdout bytes.Buffer

		code := Doctor(context.Background(), fixture.configPath, false, &stdout, fixture.deps)

		if code != 1 || !strings.HasPrefix(stdout.String(), "core:\n") ||
			strings.Contains(stdout.String(), "PLANTED") ||
			strings.Contains(stdout.String(), "doctor_failed") {
			t.Fatalf("Doctor() = code %d output %q", code, stdout.String())
		}
	})

	t.Run("pre-canceled valid config writes one fixed line without dependencies", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		var stdout bytes.Buffer

		code := Doctor(ctx, writeStructurallyValidConfig(t), false, &stdout, Dependencies{})

		if code != 1 || stdout.String() != "doctor_failed\n" {
			t.Fatalf("Doctor() = code %d output %q", code, stdout.String())
		}
	})

	t.Run("post-start cancellation is an exceptional doctor result", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		ctx, cancel := context.WithCancel(context.Background())
		fixture.controller.selfTestHook = cancel
		var stdout bytes.Buffer

		code := Doctor(ctx, fixture.configPath, false, &stdout, fixture.deps)

		if code != 1 || stdout.String() != "doctor_failed\n" {
			t.Fatalf("Doctor() = code %d output %q", code, stdout.String())
		}
		if fixture.openCalls != 1 || fixture.closeCalls != 1 || fixture.controller.shutdownCalls != 1 {
			t.Fatalf(
				"open/close/controller shutdown calls = %d/%d/%d, want 1/1/1",
				fixture.openCalls,
				fixture.closeCalls,
				fixture.controller.shutdownCalls,
			)
		}
	})

	t.Run("exceptional doctor error writes only fixed line", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		fixture.deps.GatewayExecutable = func() (string, error) {
			return filepath.Join(t.TempDir(), "missing"), nil
		}
		var stdout bytes.Buffer

		code := Doctor(context.Background(), fixture.configPath, false, &stdout, fixture.deps)

		if code != 1 || stdout.String() != "doctor_failed\n" || fixture.openCalls != 0 {
			t.Fatalf("Doctor() = code %d output %q openCalls %d", code, stdout.String(), fixture.openCalls)
		}
	})
}

func TestServeZeroReadyProvidersUnwindsTransferredRootWithoutServingResources(t *testing.T) {
	fixture := newReadyAppFixture(t)
	adapter := fixture.deps.Adapters[core.ProviderCodex].(*appTestAdapter)
	adapter.health = provider.Health{
		Provider:     core.ProviderCodex,
		Status:       provider.HealthNotReady,
		Version:      "1.2.3",
		Auth:         "missing",
		Capabilities: slices.Clone(adapter.health.Capabilities),
		Problems:     []string{provider.ProblemAuthMissing},
	}

	err := Serve(context.Background(), fixture.configPath, fixture.deps)

	if err != ErrNotReady { //nolint:errorlint // This no-cleanup path promises exact identity.
		t.Fatalf("Serve() error = %v, want exact %v", err, ErrNotReady)
	}
	if fixture.janitorCalls != 2 || fixture.closeCalls != 1 {
		t.Fatalf("janitor/close calls = %d/%d, want 2/1",
			fixture.janitorCalls, fixture.closeCalls)
	}
	if fixture.httpIDCalls != 0 || fixture.listenCalls != 0 {
		t.Fatalf("HTTP ID/listen calls = %d/%d, want 0/0",
			fixture.httpIDCalls, fixture.listenCalls)
	}
}

func TestServeListenerConstructionFailureUnwindsCompleteAssembly(t *testing.T) {
	fixture := newReadyAppFixture(t)
	fixture.deps.NewHTTPIDs = func() (httpapi.IDSource, error) {
		fixture.httpIDCalls++
		return httpapi.NewOpaqueIDSource(bytes.NewReader(make([]byte, 32)))
	}
	fixture.deps.Listen = func(network, address string) (net.Listener, error) {
		fixture.listenCalls++
		if network != "tcp" || address != "127.0.0.1:18080" {
			t.Fatalf("Listen args = %q/%q", network, address)
		}
		return nil, errors.New("PLANTED_LISTENER_SECRET")
	}

	err := Serve(context.Background(), fixture.configPath, fixture.deps)

	if err != ErrStartup { //nolint:errorlint // Successful cleanup promises exact identity.
		t.Fatalf("Serve() error = %v, want exact %v", err, ErrStartup)
	}
	if fixture.httpIDCalls != 1 || fixture.listenCalls != 1 {
		t.Fatalf("HTTP ID/listen calls = %d/%d, want 1/1",
			fixture.httpIDCalls, fixture.listenCalls)
	}
	if fixture.janitorCalls != 2 || fixture.closeCalls != 1 {
		t.Fatalf("janitor/close calls = %d/%d, want 2/1",
			fixture.janitorCalls, fixture.closeCalls)
	}
}

func TestServeHTTPIDFailureUnwindsBeforeListener(t *testing.T) {
	for name, factory := range map[string]func() (httpapi.IDSource, error){
		"factory error": func() (httpapi.IDSource, error) {
			return nil, errors.New("PLANTED_HTTP_ID_ERROR_SECRET")
		},
		"typed nil": func() (httpapi.IDSource, error) {
			var source *appTestIDSource
			return source, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			fixture := newReadyAppFixture(t)
			fixture.deps.NewHTTPIDs = func() (httpapi.IDSource, error) {
				fixture.httpIDCalls++
				return factory()
			}

			err := Serve(context.Background(), fixture.configPath, fixture.deps)

			if err != ErrStartup { //nolint:errorlint // Successful cleanup promises exact identity.
				t.Fatalf("Serve() error = %v, want exact %v", err, ErrStartup)
			}
			if fixture.httpIDCalls != 1 || fixture.listenCalls != 0 {
				t.Fatalf(
					"HTTP ID/listen calls = %d/%d, want 1/0",
					fixture.httpIDCalls,
					fixture.listenCalls,
				)
			}
			if fixture.janitorCalls != 2 || fixture.closeCalls != 1 {
				t.Fatalf(
					"janitor/close calls = %d/%d, want 2/1",
					fixture.janitorCalls,
					fixture.closeCalls,
				)
			}
			if strings.Contains(err.Error(), "PLANTED") {
				t.Fatalf("Serve() exposed raw HTTP ID failure: %q", err)
			}
		})
	}
}

func TestServeRejectsAdapterIdentityDriftAfterDiagnosis(t *testing.T) {
	fixture := newReadyAppFixture(t)
	base := fixture.deps.Adapters[core.ProviderCodex].(*appTestAdapter)
	fixture.deps.Adapters[core.ProviderCodex] = &appDriftingAdapter{
		stableNameCalls: 1,
		health:          base.health.Clone(),
	}

	err := Serve(context.Background(), fixture.configPath, fixture.deps)

	if err != ErrStartup { //nolint:errorlint // Successful cleanup promises exact identity.
		t.Fatalf("Serve() error = %v, want exact %v", err, ErrStartup)
	}
	if fixture.httpIDCalls != 0 || fixture.listenCalls != 0 {
		t.Fatalf("HTTP ID/listen calls = %d/%d, want 0/0", fixture.httpIDCalls, fixture.listenCalls)
	}
	if fixture.janitorCalls != 2 || fixture.closeCalls != 1 {
		t.Fatalf(
			"janitor/close calls = %d/%d, want 2/1",
			fixture.janitorCalls,
			fixture.closeCalls,
		)
	}
}

func TestServePreGatewayRuntimeFailureDrainsCreatedSchedulerAndSupervisor(t *testing.T) {
	fixture := newReadyAppFixture(t)
	base := fixture.deps.Adapters[core.ProviderCodex].(*appTestAdapter)
	lateDrift := &appDriftingAdapter{
		stableNameCalls: 2,
		health:          base.health.Clone(),
	}
	fixture.deps.Adapters[core.ProviderCodex] = lateDrift

	err := Serve(context.Background(), fixture.configPath, fixture.deps)

	if err != ErrStartup { //nolint:errorlint // Successful cleanup promises exact identity.
		t.Fatalf("Serve() error = %v, want exact %v", err, ErrStartup)
	}
	if lateDrift.nameCalls != 3 {
		t.Fatalf("adapter Name calls = %d, want doctor+membership+runtime validation", lateDrift.nameCalls)
	}
	if fixture.httpIDCalls != 0 || fixture.listenCalls != 0 {
		t.Fatalf("HTTP ID/listen calls = %d/%d, want 0/0", fixture.httpIDCalls, fixture.listenCalls)
	}
	if fixture.janitorCalls != 2 || fixture.closeCalls != 1 {
		t.Fatalf(
			"janitor/close calls = %d/%d, want 2/1 after scheduler/supervisor drain",
			fixture.janitorCalls,
			fixture.closeCalls,
		)
	}
	loaded, loadErr := config.Load(fixture.configPath)
	if loadErr != nil {
		t.Fatalf("reload fixture config: %v", loadErr)
	}
	reopened, openErr := process.OpenRoot(loaded.Runtime.Root)
	if openErr != nil {
		t.Fatalf("runtime root remained locked after partial unwind: %v", openErr)
	}
	if closeErr := reopened.Close(); closeErr != nil {
		t.Fatalf("close reopened runtime root: %v", closeErr)
	}
}

func TestServeClosedTransferredRootFailsSafeBeforeServing(t *testing.T) {
	fixture := newReadyAppFixture(t)
	fixture.deps.NewProbeController = func(
		root *process.Root,
		_ process.Limits,
		_ func() (string, error),
	) (doctor.ProbeController, error) {
		return &appRootClosingProbeController{
			appTestProbeController: fixture.controller,
			root:                   root,
		}, nil
	}

	err := Serve(context.Background(), fixture.configPath, fixture.deps)

	if !errors.Is(err, ErrStartup) || !errors.Is(err, ErrShutdown) {
		t.Fatalf("Serve() error = %v, want startup+shutdown sentinels", err)
	}
	if fixture.httpIDCalls != 0 || fixture.listenCalls != 0 {
		t.Fatalf("HTTP ID/listen calls = %d/%d, want 0/0", fixture.httpIDCalls, fixture.listenCalls)
	}
	if fixture.janitorCalls != 2 || fixture.closeCalls != 1 {
		t.Fatalf(
			"janitor/close calls = %d/%d, want 2/1",
			fixture.janitorCalls,
			fixture.closeCalls,
		)
	}
}

func TestServeGatewayAuthEnvironmentSnapshotIsLookedUpOnceAndTransferred(t *testing.T) {
	fixture := newReadyAppFixture(t)
	const environmentName = "APP_TEST_GATEWAY_KEY"
	addFixtureGatewayKey(t, fixture.configPath, environmentName)
	currentKey := "initial-gateway-key"
	keyLookups := 0
	fixture.deps.LookupEnv = func(name string) (string, bool) {
		if name != environmentName {
			return "", false
		}
		keyLookups++
		return currentKey, true
	}
	listener := newAppMemoryListener()
	configureFixtureHTTP(t, fixture, listener)
	observation := &appConfigSourceObservation{}
	startup := observation.dependencies(t, fixture.configPath)
	startup.postDiagnosis = func() {
		currentKey = "rotated-gateway-key"
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serve(ctx, fixture.configPath, fixture.deps, startup)
	}()

	awaitAppSignal(t, listener.acceptReady, "Gateway auth listener handoff")
	if status := appMemoryRequest(t, listener, "initial-gateway-key"); status != http.StatusOK {
		t.Fatalf("initial key status = %d, want %d", status, http.StatusOK)
	}
	if status := appMemoryRequest(t, listener, "rotated-gateway-key"); status != http.StatusUnauthorized {
		t.Fatalf("rotated key status = %d, want %d", status, http.StatusUnauthorized)
	}
	cancel()
	if err := awaitAppValue(t, result, "environment-auth Serve shutdown"); err != nil {
		t.Fatalf("Serve() error = %v, want clean shutdown", err)
	}

	if keyLookups != 1 {
		t.Fatalf("Gateway key lookups = %d, want 1", keyLookups)
	}
	observation.assertLifecycle(t, 1, 1, 1)
	if fixture.listenCalls != 1 || fixture.janitorCalls != 2 || fixture.closeCalls != 1 {
		t.Fatalf(
			"listen/janitor/root close calls = %d/%d/%d, want 1/2/1",
			fixture.listenCalls,
			fixture.janitorCalls,
			fixture.closeCalls,
		)
	}
}

func TestServeGatewayAuthFileSnapshotLoadsOnceWithRetainedConfigIdentity(t *testing.T) {
	fixture := newReadyAppFixture(t)
	initialKey := strings.Repeat("1", 64)
	rotatedKey := strings.Repeat("2", 64)
	keyPath := addFixtureGatewayKeyFile(t, fixture.configPath, initialKey)
	listener := newAppMemoryListener()
	configureFixtureHTTP(t, fixture, listener)
	observation := &appConfigSourceObservation{}
	startup := observation.dependencies(t, fixture.configPath)
	loaderCalls := 0
	var loaderConfigIdentity fs.FileInfo
	startup.LoadGatewayKey = func(path string, distinct []fs.FileInfo) (gatewaykey.Snapshot, error) {
		loaderCalls++
		if path != keyPath {
			t.Fatalf("LoadGatewayKey path = %q, want %q", path, keyPath)
		}
		if len(distinct) == 0 {
			t.Fatal("LoadGatewayKey received no config identity")
		}
		loaderConfigIdentity = distinct[0]
		return gatewaykey.LoadFile(path, distinct)
	}
	startup.postDiagnosis = func() {
		replacement := filepath.Join(filepath.Dir(keyPath), "replacement-gateway.key")
		// Both paths are confined to this test's owner-private fixture.
		//nolint:gosec
		if err := os.WriteFile(replacement, []byte(rotatedKey+"\n"), 0o600); err != nil {
			t.Fatalf("write replacement Gateway key: %v", err)
		}
		replaceAppFixturePath(t, replacement, keyPath)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serve(ctx, fixture.configPath, fixture.deps, startup)
	}()

	awaitAppSignal(t, listener.acceptReady, "file-auth listener handoff")
	if status := appMemoryRequest(t, listener, initialKey); status != http.StatusOK {
		t.Fatalf("initial file key status = %d, want %d", status, http.StatusOK)
	}
	if status := appMemoryRequest(t, listener, rotatedKey); status != http.StatusUnauthorized {
		t.Fatalf("replacement file key status = %d, want %d", status, http.StatusUnauthorized)
	}
	cancel()
	if err := awaitAppValue(t, result, "file-auth Serve shutdown"); err != nil {
		t.Fatalf("Serve() error = %v, want clean shutdown", err)
	}

	if loaderCalls != 1 {
		t.Fatalf("LoadGatewayKey calls = %d, want 1", loaderCalls)
	}
	if observation.source == nil || loaderConfigIdentity != observation.source.info {
		t.Fatal("key loader did not receive the exact retained config identity")
	}
	observation.assertLifecycle(t, 1, 1, 1)
}

func TestServeGatewayAuthDisabledSnapshotAcceptsRequests(t *testing.T) {
	fixture := newReadyAppFixture(t)
	listener := newAppMemoryListener()
	configureFixtureHTTP(t, fixture, listener)
	observation := &appConfigSourceObservation{}
	startup := observation.dependencies(t, fixture.configPath)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- serve(ctx, fixture.configPath, fixture.deps, startup)
	}()

	awaitAppSignal(t, listener.acceptReady, "disabled-auth listener handoff")
	if status := appMemoryRequest(t, listener, ""); status != http.StatusOK {
		t.Fatalf("disabled-auth status = %d, want %d", status, http.StatusOK)
	}
	cancel()
	if err := awaitAppValue(t, result, "disabled-auth Serve shutdown"); err != nil {
		t.Fatalf("Serve() error = %v, want clean shutdown", err)
	}
	observation.assertLifecycle(t, 1, 1, 1)
}

func TestServeGatewayAuthFailureReturnsNotReadyWithoutListener(t *testing.T) {
	fixture := newReadyAppFixture(t)
	addFixtureGatewayKey(t, fixture.configPath, "APP_TEST_MISSING_GATEWAY_KEY")
	lookupCalls := 0
	fixture.deps.LookupEnv = func(name string) (string, bool) {
		lookupCalls++
		if name != "APP_TEST_MISSING_GATEWAY_KEY" {
			t.Fatalf("LookupEnv name = %q", name)
		}
		return "", false
	}
	observation := &appConfigSourceObservation{}

	err := serve(
		context.Background(),
		fixture.configPath,
		fixture.deps,
		observation.dependencies(t, fixture.configPath),
	)

	if err != ErrNotReady { //nolint:errorlint // The readiness boundary promises exact identity.
		t.Fatalf("Serve() error = %v, want exact %v", err, ErrNotReady)
	}
	if lookupCalls != 1 || fixture.listenCalls != 0 || fixture.openCalls != 0 {
		t.Fatalf(
			"lookup/listen/root open calls = %d/%d/%d, want 1/0/0",
			lookupCalls,
			fixture.listenCalls,
			fixture.openCalls,
		)
	}
	observation.assertLifecycle(t, 1, 0, 1)
}

func TestDoctorGatewayAuthFailurePreservesClosedDiagnosticsAndClosesSource(t *testing.T) {
	fixture := newReadyAppFixture(t)
	addFixtureGatewayKey(t, fixture.configPath, "APP_TEST_MISSING_GATEWAY_KEY")
	fixture.deps.LookupEnv = func(string) (string, bool) { return "", false }
	observation := &appConfigSourceObservation{}
	var stdout bytes.Buffer

	code := runDoctor(
		context.Background(),
		fixture.configPath,
		false,
		&stdout,
		fixture.deps,
		observation.dependencies(t, fixture.configPath),
	)

	if code != 1 || !strings.Contains(
		stdout.String(),
		"gateway_auth\tfail\tgateway_key_missing\tgateway authentication is unavailable\n",
	) {
		t.Fatalf("Doctor() = code %d output %q, want closed Gateway auth failure", code, stdout.String())
	}
	for _, secret := range []string{
		"APP_TEST_MISSING_GATEWAY_KEY",
		fixture.configPath,
	} {
		if strings.Contains(stdout.String(), secret) {
			t.Fatalf("Doctor output exposed %q: %q", secret, stdout.String())
		}
	}
	observation.assertLifecycle(t, 1, 0, 1)
}

func TestServeGatewayAuthConfigRevalidationFailsClosedBeforeListener(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{
			name: "in-place modification",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				// path is confined to this test's owner-private fixture.
				//nolint:gosec
				payload, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read config for mutation: %v", err)
				}
				// path is confined to this test's owner-private fixture.
				//nolint:gosec
				if err := os.WriteFile(path, append(payload, '\n'), 0o600); err != nil {
					t.Fatalf("mutate config: %v", err)
				}
			},
		},
		{
			name: "path replacement",
			mutate: func(t *testing.T, path string) {
				t.Helper()
				// path is confined to this test's owner-private fixture.
				//nolint:gosec
				payload, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read config for replacement: %v", err)
				}
				replacement := filepath.Join(filepath.Dir(path), "replacement-config.toml")
				// replacement is confined to this test's owner-private fixture.
				//nolint:gosec
				if err := os.WriteFile(replacement, payload, 0o600); err != nil {
					t.Fatalf("write replacement config: %v", err)
				}
				replaceAppFixturePath(t, replacement, path)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReadyAppFixture(t)
			fixture.deps.NewHTTPIDs = func() (httpapi.IDSource, error) {
				fixture.httpIDCalls++
				return httpapi.NewOpaqueIDSource(bytes.NewReader(make([]byte, 32)))
			}
			observation := &appConfigSourceObservation{}
			startup := observation.dependencies(t, fixture.configPath)
			startup.postDiagnosis = func() { test.mutate(t, fixture.configPath) }

			err := serve(context.Background(), fixture.configPath, fixture.deps, startup)

			if err != ErrNotReady { //nolint:errorlint // Unstable startup evidence is not ready.
				t.Fatalf("Serve() error = %v, want exact %v", err, ErrNotReady)
			}
			if fixture.listenCalls != 0 || fixture.httpIDCalls != 1 {
				t.Fatalf(
					"HTTP ID/listen calls = %d/%d, want 1/0",
					fixture.httpIDCalls,
					fixture.listenCalls,
				)
			}
			if fixture.janitorCalls != 2 || fixture.closeCalls != 1 {
				t.Fatalf(
					"janitor/root close calls = %d/%d, want 2/1",
					fixture.janitorCalls,
					fixture.closeCalls,
				)
			}
			observation.assertLifecycle(t, 1, 1, 1)
		})
	}
}

func replaceAppFixturePath(t *testing.T, replacement, selected string) {
	t.Helper()
	if filepath.Dir(replacement) != filepath.Dir(selected) {
		t.Fatal("replacement and selected fixture paths are in different directories")
	}
	root, err := os.OpenRoot(filepath.Dir(selected))
	if err != nil {
		t.Fatalf("open fixture root: %v", err)
	}
	defer func() {
		if err := root.Close(); err != nil {
			t.Errorf("close fixture root: %v", err)
		}
	}()

	replacementName := filepath.Base(replacement)
	selectedName := filepath.Base(selected)
	displacedName := selectedName + ".displaced"
	if err := root.Rename(selectedName, displacedName); err != nil {
		t.Fatalf("displace selected fixture path: %v", err)
	}
	if err := root.Rename(replacementName, selectedName); err != nil {
		restoreErr := root.Rename(displacedName, selectedName)
		if restoreErr != nil {
			t.Fatalf("replace fixture path: %v (restore failed: %v)", err, restoreErr)
		}
		t.Fatalf("replace fixture path: %v", err)
	}
}

func TestServeGatewayAuthConfigMutationDuringHTTPIDCreationFailsClosedBeforeListener(t *testing.T) {
	fixture := newReadyAppFixture(t)
	observation := &appConfigSourceObservation{}
	fixture.deps.NewHTTPIDs = func() (httpapi.IDSource, error) {
		fixture.httpIDCalls++
		// configPath is confined to this test's owner-private fixture.
		//nolint:gosec
		payload, err := os.ReadFile(fixture.configPath)
		if err != nil {
			t.Fatalf("read config during HTTP ID creation: %v", err)
		}
		// configPath is confined to this test's owner-private fixture.
		//nolint:gosec
		if err := os.WriteFile(fixture.configPath, append(payload, '\n'), 0o600); err != nil {
			t.Fatalf("mutate config during HTTP ID creation: %v", err)
		}
		return httpapi.NewOpaqueIDSource(bytes.NewReader(make([]byte, 32)))
	}

	err := serve(
		context.Background(),
		fixture.configPath,
		fixture.deps,
		observation.dependencies(t, fixture.configPath),
	)

	if err != ErrNotReady { //nolint:errorlint // Unstable startup evidence is not ready.
		t.Fatalf("Serve() error = %v, want exact %v", err, ErrNotReady)
	}
	if fixture.httpIDCalls != 1 || fixture.listenCalls != 0 {
		t.Fatalf(
			"HTTP ID/listen calls = %d/%d, want 1/0",
			fixture.httpIDCalls,
			fixture.listenCalls,
		)
	}
	if fixture.janitorCalls != 2 || fixture.closeCalls != 1 {
		t.Fatalf(
			"janitor/root close calls = %d/%d, want 2/1",
			fixture.janitorCalls,
			fixture.closeCalls,
		)
	}
	observation.assertLifecycle(t, 1, 1, 1)
}

func TestDoctorGatewayAuthFileLoaderUsesRetainedConfigIdentityAndClosesSource(t *testing.T) {
	fixture := newReadyAppFixture(t)
	keyPath := addFixtureGatewayKeyFile(t, fixture.configPath, strings.Repeat("3", 64))
	observation := &appConfigSourceObservation{}
	startup := observation.dependencies(t, fixture.configPath)
	loaderCalls := 0
	var loaderConfigIdentity fs.FileInfo
	startup.LoadGatewayKey = func(path string, distinct []fs.FileInfo) (gatewaykey.Snapshot, error) {
		loaderCalls++
		if path != keyPath || len(distinct) == 0 {
			t.Fatalf("LoadGatewayKey path/distinct = %q/%d", path, len(distinct))
		}
		loaderConfigIdentity = distinct[0]
		return gatewaykey.LoadFile(path, distinct)
	}
	var stdout bytes.Buffer

	code := runDoctor(
		context.Background(),
		fixture.configPath,
		false,
		&stdout,
		fixture.deps,
		startup,
	)

	if code != 0 {
		t.Fatalf("Doctor() code = %d, want 0; output=%q", code, stdout.String())
	}
	if loaderCalls != 1 || observation.source == nil || loaderConfigIdentity != observation.source.info {
		t.Fatal("Doctor key loader did not receive the exact retained config identity once")
	}
	observation.assertLifecycle(t, 1, 0, 1)
}

func TestServeConfigSourceClosesExactlyOnceOnEveryPreHandoffExit(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *readyAppFixture, startupDependencies)
	}{
		{
			name: "pre-canceled",
			run: func(t *testing.T, fixture *readyAppFixture, startup startupDependencies) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				if err := serve(ctx, fixture.configPath, Dependencies{}, startup); err != nil {
					t.Fatalf("Serve() error = %v, want clean cancellation", err)
				}
			},
		},
		{
			name: "invalid dependencies",
			run: func(t *testing.T, fixture *readyAppFixture, startup startupDependencies) {
				if err := serve(context.Background(), fixture.configPath, Dependencies{}, startup); !errors.Is(err, ErrStartup) {
					t.Fatalf("Serve() error = %v, want exact %v", err, ErrStartup)
				}
			},
		},
		{
			name: "doctor invalid dependencies",
			run: func(t *testing.T, fixture *readyAppFixture, startup startupDependencies) {
				var stdout bytes.Buffer
				if code := runDoctor(
					context.Background(), fixture.configPath, false, &stdout, Dependencies{}, startup,
				); code != 1 || stdout.String() != "doctor_failed\n" {
					t.Fatalf("Doctor() = code %d output %q", code, stdout.String())
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReadyAppFixture(t)
			observation := &appConfigSourceObservation{}
			test.run(t, fixture, observation.dependencies(t, fixture.configPath))
			observation.assertLifecycle(t, 1, 0, 1)
		})
	}
}

func TestServeCleanCancellationClosesAdmissionBeforeOwnedRuntime(t *testing.T) {
	fixture := newReadyAppFixture(t)
	var listenConfig net.ListenConfig
	listener, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("pre-bind listener: %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	rewriteFixtureListen(t, fixture.configPath, listener.Addr().String())
	fixture.deps.NewHTTPIDs = func() (httpapi.IDSource, error) {
		fixture.httpIDCalls++
		return httpapi.NewOpaqueIDSource(bytes.NewReader(make([]byte, 32)))
	}
	listenCalled := make(chan struct{})
	fixture.deps.Listen = func(network, address string) (net.Listener, error) {
		fixture.listenCalls++
		if network != "tcp" || address != listener.Addr().String() {
			t.Fatalf("Listen args = %q/%q", network, address)
		}
		close(listenCalled)
		return listener, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- Serve(ctx, fixture.configPath, fixture.deps) }()

	awaitAppSignal(t, listenCalled, "listener construction")
	cancel()
	if serveErr := awaitAppValue(t, result, "Serve cancellation"); serveErr != nil {
		t.Fatalf("Serve() error = %v, want clean cancellation", serveErr)
	}
	if fixture.janitorCalls != 2 || fixture.closeCalls != 1 {
		t.Fatalf("janitor/close calls = %d/%d, want 2/1",
			fixture.janitorCalls, fixture.closeCalls)
	}
	acceptResult := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if connection != nil {
			_ = connection.Close()
		}
		acceptResult <- acceptErr
	}()
	if acceptErr := awaitAppValue(t, acceptResult, "listener close"); acceptErr == nil {
		t.Fatal("underlying listener still accepted after Serve returned")
	}
}

func TestRetryableOwnershipDrainContinuesUntilBackgroundSuccess(t *testing.T) {
	bounded, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	calls := 0
	failed := drainRetryable(bounded, func(ctx context.Context) error {
		calls++
		switch calls {
		case 1:
			if _, ok := ctx.Deadline(); !ok {
				t.Fatal("initial drain lacks configured deadline")
			}
			return context.DeadlineExceeded
		case 2:
			if _, ok := ctx.Deadline(); ok {
				t.Fatal("retry inherited bounded deadline")
			}
			return errors.New("PLANTED_RETRY_SECRET")
		case 3:
			if _, ok := ctx.Deadline(); ok {
				t.Fatal("final retry inherited bounded deadline")
			}
			return nil
		default:
			t.Fatalf("unexpected drain call %d", calls)
			return nil
		}
	})

	if !failed || calls != 3 {
		t.Fatalf("drain result/calls = %v/%d, want true/3", failed, calls)
	}
}

func TestUnwindRuntimeUsesGatewayOwnershipAndReversePhaseOrder(t *testing.T) {
	trace := make([]string, 0, 6)
	gatewayDrain := retryableShutdownFunc(func(context.Context) error {
		trace = append(trace, "gateway")
		return nil
	})
	schedulerCalls := 0
	schedulerCanary := retryableShutdownFunc(func(context.Context) error {
		schedulerCalls++
		trace = append(trace, "duplicate_scheduler")
		return nil
	})
	firstSupervisor := retryableShutdownFunc(func(context.Context) error {
		trace = append(trace, "supervisor_first")
		return nil
	})
	secondSupervisor := retryableShutdownFunc(func(context.Context) error {
		trace = append(trace, "supervisor_second")
		return nil
	})
	deps := Dependencies{
		Janitor: func(context.Context, *process.Root) error {
			trace = append(trace, "janitor")
			return nil
		},
		CloseRoot: func(*process.Root) error {
			trace = append(trace, "root_close")
			return nil
		},
	}
	cfg := config.Config{
		Server:  config.Server{ShutdownTimeout: config.Duration(time.Second)},
		Runtime: config.Runtime{CleanupTimeout: config.Duration(time.Second)},
	}
	result := make(chan bool, 1)
	go func() {
		result <- unwindRuntime(
			cfg,
			deps,
			gatewayDrain,
			[]retryableShutdown{schedulerCanary},
			[]retryableShutdown{firstSupervisor, secondSupervisor},
			nil,
		)
	}()

	if failed := awaitAppValue(t, result, "reverse runtime unwind"); failed {
		t.Fatal("clean reverse unwind reported failure")
	}
	want := []string{
		"gateway",
		"supervisor_second",
		"supervisor_first",
		"janitor",
		"root_close",
	}
	if schedulerCalls != 0 || !slices.Equal(trace, want) {
		t.Fatalf("scheduler calls/trace = %d/%q, want 0/%q", schedulerCalls, trace, want)
	}
}

func TestServeClosesListenerReturnedWithErrorAndRetainsCloseFailure(t *testing.T) {
	fixture := newReadyAppFixture(t)
	fixture.deps.NewHTTPIDs = func() (httpapi.IDSource, error) {
		fixture.httpIDCalls++
		return httpapi.NewOpaqueIDSource(bytes.NewReader(make([]byte, 32)))
	}
	returned := &appTestErrorListener{}
	fixture.deps.Listen = func(string, string) (net.Listener, error) {
		fixture.listenCalls++
		return returned, errors.New("PLANTED_LISTEN_AND_LISTENER_SECRET")
	}

	err := Serve(context.Background(), fixture.configPath, fixture.deps)

	if !errors.Is(err, ErrStartup) || !errors.Is(err, ErrShutdown) {
		t.Fatalf("Serve() error = %v, want startup+shutdown sentinels", err)
	}
	if returned.closeCalls != 1 {
		t.Fatalf("returned listener Close calls = %d, want 1", returned.closeCalls)
	}
	if bytes.Contains([]byte(err.Error()), []byte("PLANTED")) {
		t.Fatalf("Serve() exposed raw dependency text: %q", err)
	}
}

func TestServeUnexpectedAcceptFailureReturnsFixedServeErrorAfterCleanup(t *testing.T) {
	fixture := newReadyAppFixture(t)
	fixture.deps.NewHTTPIDs = func() (httpapi.IDSource, error) {
		fixture.httpIDCalls++
		return httpapi.NewOpaqueIDSource(bytes.NewReader(make([]byte, 32)))
	}
	trace := make([]string, 0, 1)
	fixture.deps.Listen = func(string, string) (net.Listener, error) {
		fixture.listenCalls++
		return &appTraceListener{trace: &trace}, nil
	}

	err := Serve(context.Background(), fixture.configPath, fixture.deps)

	if err != ErrServe { //nolint:errorlint // Clean cleanup leaves the exact primary sentinel.
		t.Fatalf("Serve() error = %v, want exact %v", err, ErrServe)
	}
	if !slices.Equal(trace, []string{"listener_close"}) {
		t.Fatalf("listener trace = %q, want one close", trace)
	}
	if fixture.janitorCalls != 2 || fixture.closeCalls != 1 {
		t.Fatalf("janitor/close calls = %d/%d, want 2/1", fixture.janitorCalls, fixture.closeCalls)
	}
}

func TestServingListenerCloseFailureRetainsFixedShutdownAndConsumesServe(t *testing.T) {
	trace := make([]string, 0, 5)
	listener := &appBlockingCloseErrorListener{
		trace:          &trace,
		acceptStarted:  make(chan struct{}),
		acceptReturned: make(chan struct{}),
		closed:         make(chan struct{}),
	}
	gatewayDrain := retryableShutdownFunc(func(context.Context) error {
		trace = append(trace, "gateway_shutdown")
		return nil
	})
	supervisorDrain := retryableShutdownFunc(func(context.Context) error {
		trace = append(trace, "supervisor_shutdown")
		return nil
	})
	deps := Dependencies{
		Janitor: func(context.Context, *process.Root) error {
			trace = append(trace, "janitor")
			return nil
		},
		CloseRoot: func(*process.Root) error {
			trace = append(trace, "root_close")
			return nil
		},
	}
	cfg := config.Config{
		Server:  config.Server{ShutdownTimeout: config.Duration(time.Second)},
		Runtime: config.Runtime{CleanupTimeout: config.Duration(time.Second)},
	}
	server := &http.Server{
		Handler:           http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}),
		ReadHeaderTimeout: time.Second,
	}
	ctx, cancel := context.WithCancel(context.Background())
	type serveOutcome struct {
		failed  bool
		primary error
	}
	result := make(chan serveOutcome, 1)
	go func() {
		failed, primary := serveOwnedRuntime(
			ctx,
			cfg,
			deps,
			server,
			listener,
			gatewayDrain,
			[]retryableShutdown{supervisorDrain},
			nil,
		)
		result <- serveOutcome{failed: failed, primary: primary}
	}()
	awaitAppSignal(t, listener.acceptStarted, "serving listener Accept")
	cancel()
	outcome := awaitAppValue(t, result, "serving listener close-error shutdown")
	err := joinShutdown(outcome.primary, outcome.failed)

	if outcome.primary != nil || !outcome.failed || !errors.Is(err, ErrShutdown) {
		t.Fatalf("serve primary/failed/error = %v/%v/%v, want nil/true/shutdown", outcome.primary, outcome.failed, err)
	}
	if strings.Contains(err.Error(), "PLANTED") {
		t.Fatalf("shutdown exposed raw listener close error: %q", err)
	}
	select {
	case <-listener.acceptReturned:
	default:
		t.Fatal("Serve result was not consumed before serving runtime returned")
	}
	want := []string{
		"listener_close",
		"gateway_shutdown",
		"supervisor_shutdown",
		"janitor",
		"root_close",
	}
	if listener.closeCalls != 1 || !slices.Equal(trace, want) {
		t.Fatalf("listener close calls/trace = %d/%q, want 1/%q", listener.closeCalls, trace, want)
	}
}

func TestServeZeroReadyCleanupFailureJoinsOnlyFixedSentinels(t *testing.T) {
	fixture := newReadyAppFixture(t)
	adapter := fixture.deps.Adapters[core.ProviderCodex].(*appTestAdapter)
	adapter.health = provider.Health{
		Provider:     core.ProviderCodex,
		Status:       provider.HealthNotReady,
		Version:      "1.2.3",
		Auth:         "missing",
		Capabilities: slices.Clone(adapter.health.Capabilities),
		Problems:     []string{provider.ProblemAuthMissing},
	}
	originalJanitor := fixture.deps.Janitor
	janitorCalls := 0
	fixture.deps.Janitor = func(ctx context.Context, root *process.Root) error {
		janitorCalls++
		if janitorCalls == 1 {
			return originalJanitor(ctx, root)
		}
		return errors.New("PLANTED_JANITOR_SECRET")
	}

	err := Serve(context.Background(), fixture.configPath, fixture.deps)

	if !errors.Is(err, ErrNotReady) || !errors.Is(err, ErrShutdown) {
		t.Fatalf("Serve() error = %v, want not-ready+shutdown", err)
	}
	if strings.Contains(err.Error(), "PLANTED") {
		t.Fatalf("Serve() exposed cleanup error: %q", err)
	}
	if janitorCalls != 2 || fixture.closeCalls != 1 {
		t.Fatalf("janitor/close calls = %d/%d, want 2/1", janitorCalls, fixture.closeCalls)
	}
}

func TestServeZeroReadyCloseFailureJoinsOnlyFixedSentinels(t *testing.T) {
	fixture := newReadyAppFixture(t)
	adapter := fixture.deps.Adapters[core.ProviderCodex].(*appTestAdapter)
	adapter.health = provider.Health{
		Provider:     core.ProviderCodex,
		Status:       provider.HealthNotReady,
		Version:      "1.2.3",
		Auth:         "missing",
		Capabilities: slices.Clone(adapter.health.Capabilities),
		Problems:     []string{provider.ProblemAuthMissing},
	}
	originalClose := fixture.deps.CloseRoot
	fixture.deps.CloseRoot = func(root *process.Root) error {
		if err := originalClose(root); err != nil {
			return err
		}
		return errors.New("PLANTED_ZERO_READY_CLOSE_SECRET")
	}

	err := Serve(context.Background(), fixture.configPath, fixture.deps)

	if !errors.Is(err, ErrNotReady) || !errors.Is(err, ErrShutdown) {
		t.Fatalf("Serve() error = %v, want not-ready+shutdown", err)
	}
	if strings.Contains(err.Error(), "PLANTED") {
		t.Fatalf("Serve() exposed close error: %q", err)
	}
	if fixture.janitorCalls != 2 || fixture.closeCalls != 1 {
		t.Fatalf(
			"janitor/close calls = %d/%d, want 2/1",
			fixture.janitorCalls,
			fixture.closeCalls,
		)
	}
}

func TestHTTPBoundaryValidationRejectsNilAndTypedNilHandlers(t *testing.T) {
	validHandler := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	server := &http.Server{Handler: validHandler, ReadHeaderTimeout: time.Second}
	if !validHTTPBoundary(server, validHandler) {
		t.Fatal("valid HTTP server/handler boundary was rejected")
	}
	var typedNil *appTestHTTPHandler
	for name, candidate := range map[string]struct {
		server  *http.Server
		handler http.Handler
	}{
		"nil server":             {server: nil, handler: validHandler},
		"nil returned handler":   {server: server, handler: nil},
		"typed returned handler": {server: server, handler: typedNil},
		"nil server handler": {
			server: &http.Server{ReadHeaderTimeout: time.Second}, handler: validHandler,
		},
		"typed server handler": {
			server:  &http.Server{Handler: typedNil, ReadHeaderTimeout: time.Second},
			handler: validHandler,
		},
	} {
		t.Run(name, func(t *testing.T) {
			if validHTTPBoundary(candidate.server, candidate.handler) {
				t.Fatal("invalid HTTP boundary was accepted")
			}
		})
	}
}

func TestRegistryComparisonRequiresEveryConfiguredModelField(t *testing.T) {
	cfg := config.Config{Models: []config.Model{{
		ID: "alias", Provider: "codex", ProviderModel: "gpt-test", Created: 7,
	}}}
	model := core.Model{
		ID: "alias", Provider: core.ProviderCodex, ProviderModel: "gpt-test", Created: 7,
	}
	registry, err := core.NewRegistry([]core.Model{model})
	if err != nil {
		t.Fatalf("construct exact registry: %v", err)
	}
	if !registryMatchesConfig(registry, cfg.Models) {
		t.Fatal("exact registry was rejected")
	}
	mutations := map[string]func(*core.Model){
		"id":             func(value *core.Model) { value.ID = "other" },
		"provider":       func(value *core.Model) { value.Provider = core.ProviderClaude },
		"provider model": func(value *core.Model) { value.ProviderModel = "other-model" },
		"created":        func(value *core.Model) { value.Created = 8 },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := model
			mutate(&changed)
			mismatched, registryErr := core.NewRegistry([]core.Model{changed})
			if registryErr != nil {
				t.Fatalf("construct mismatched registry: %v", registryErr)
			}
			if registryMatchesConfig(mismatched, cfg.Models) {
				t.Fatal("field-mismatched registry was accepted")
			}
		})
	}
}

func TestShutdownHandshakeClosesUntrackedListenerBeforeRuntimeDrain(t *testing.T) {
	trace := make([]string, 0, 5)
	listener := newShutdownAwareListener(&appTraceListener{trace: &trace})
	gatewayDrain := retryableShutdownFunc(func(context.Context) error {
		trace = append(trace, "gateway_shutdown")
		return nil
	})
	supervisorDrain := retryableShutdownFunc(func(context.Context) error {
		trace = append(trace, "supervisor_shutdown")
		return nil
	})
	deps := Dependencies{
		Janitor: func(context.Context, *process.Root) error {
			trace = append(trace, "janitor")
			return nil
		},
		CloseRoot: func(*process.Root) error {
			trace = append(trace, "root_close")
			return nil
		},
	}
	cfg := config.Config{
		Server:  config.Server{ShutdownTimeout: config.Duration(time.Second)},
		Runtime: config.Runtime{CleanupTimeout: config.Duration(time.Second)},
	}

	result := make(chan bool, 1)
	go func() {
		result <- shutdownServingRuntime(
			cfg,
			deps,
			&http.Server{ReadHeaderTimeout: time.Second},
			listener,
			gatewayDrain,
			[]retryableShutdown{supervisorDrain},
			nil,
		)
	}()
	failed := awaitAppValue(t, result, "listener shutdown handshake")

	if failed {
		t.Fatal("clean shutdown handshake reported failure")
	}
	want := []string{
		"listener_close",
		"gateway_shutdown",
		"supervisor_shutdown",
		"janitor",
		"root_close",
	}
	if !slices.Equal(trace, want) {
		t.Fatalf("shutdown trace = %q, want %q", trace, want)
	}
}

func TestHTTPShutdownDeadlineForcesConnectionsClosedBeforeProcessCleanup(t *testing.T) {
	var listenConfig net.ListenConfig
	underlying, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = underlying.Close() })
	entered := make(chan struct{})
	release := make(chan struct{})
	server := &http.Server{
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(entered)
			<-release
		}),
		ReadHeaderTimeout: time.Second,
	}
	tracked := newShutdownAwareListener(underlying)
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(tracked) }()
	clientResult := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 2 * time.Second}
		request, requestErr := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"http://"+underlying.Addr().String(),
			nil,
		)
		if requestErr != nil {
			clientResult <- requestErr
			return
		}
		response, requestErr := client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		clientResult <- requestErr
	}()
	awaitAppSignal(t, entered, "blocking HTTP handler")
	processCleanup := false
	deps := Dependencies{
		Janitor: func(context.Context, *process.Root) error {
			processCleanup = true
			return nil
		},
		CloseRoot: func(*process.Root) error { return nil },
	}
	cfg := config.Config{
		Server:  config.Server{ShutdownTimeout: config.Duration(50 * time.Millisecond)},
		Runtime: config.Runtime{CleanupTimeout: config.Duration(10 * time.Millisecond)},
	}
	gatewayDrain := retryableShutdownFunc(func(context.Context) error { return nil })

	shutdownResult := make(chan bool, 1)
	go func() {
		shutdownResult <- shutdownServingRuntime(
			cfg,
			deps,
			server,
			tracked,
			gatewayDrain,
			nil,
			nil,
		)
	}()
	failed := awaitAppValue(t, shutdownResult, "forced HTTP shutdown")

	if !failed || !processCleanup {
		t.Fatalf("shutdown failed/cleanup = %v/%v, want true/true", failed, processCleanup)
	}
	if clientErr := awaitAppValue(t, clientResult, "forced client connection close"); clientErr == nil {
		t.Fatal("client request completed successfully before blocked handler was released")
	}
	close(release)
	if serveErr := awaitAppValue(t, serveResult, "forced HTTP Serve return"); !errors.Is(serveErr, http.ErrServerClosed) {
		t.Fatalf("Serve error = %v, want http.ErrServerClosed", serveErr)
	}
}

func TestHTTPDeadlineForcesConnectionBeforeBackgroundOwnershipRetries(t *testing.T) {
	var listenConfig net.ListenConfig
	underlying, err := listenConfig.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = underlying.Close() })
	handlerEntered := make(chan struct{})
	handlerRelease := make(chan struct{}, 1)
	server := &http.Server{
		Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			close(handlerEntered)
			<-handlerRelease
		}),
		ReadHeaderTimeout: time.Second,
	}
	tracked := newShutdownAwareListener(underlying)
	serveResult := make(chan error, 1)
	go func() { serveResult <- server.Serve(tracked) }()
	clientResult := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 5 * time.Second}
		request, requestErr := http.NewRequestWithContext(
			context.Background(),
			http.MethodGet,
			"http://"+underlying.Addr().String(),
			nil,
		)
		if requestErr != nil {
			clientResult <- requestErr
			return
		}
		response, requestErr := client.Do(request)
		if response != nil {
			_ = response.Body.Close()
		}
		clientResult <- requestErr
	}()
	awaitAppSignal(t, handlerEntered, "combined-failure HTTP handler")

	trace := make([]string, 0, 10)
	gatewayFinalEntered := make(chan struct{})
	gatewayRelease := make(chan struct{}, 1)
	gatewayCalls := 0
	gatewayDrain := retryableShutdownFunc(func(ctx context.Context) error {
		gatewayCalls++
		switch gatewayCalls {
		case 1:
			if _, ok := ctx.Deadline(); !ok {
				trace = append(trace, "gateway_bounded_missing_deadline")
			} else {
				trace = append(trace, "gateway_bounded")
			}
			<-ctx.Done()
			return ctx.Err()
		case 2:
			if _, ok := ctx.Deadline(); ok {
				trace = append(trace, "gateway_retry_with_deadline")
			} else {
				trace = append(trace, "gateway_retry_error")
			}
			return errors.New("PLANTED_GATEWAY_RETRY_SECRET")
		case 3:
			trace = append(trace, "gateway_retry_wait")
			close(gatewayFinalEntered)
			<-gatewayRelease
			trace = append(trace, "gateway_success")
			return nil
		default:
			trace = append(trace, "gateway_extra_call")
			return nil
		}
	})

	supervisorFinalEntered := make(chan struct{})
	supervisorRelease := make(chan struct{}, 1)
	supervisorCalls := 0
	supervisorDrain := retryableShutdownFunc(func(ctx context.Context) error {
		supervisorCalls++
		switch supervisorCalls {
		case 1:
			if _, ok := ctx.Deadline(); !ok {
				trace = append(trace, "supervisor_bounded_missing_deadline")
			} else {
				trace = append(trace, "supervisor_bounded")
			}
			return context.DeadlineExceeded
		case 2:
			if _, ok := ctx.Deadline(); ok {
				trace = append(trace, "supervisor_retry_with_deadline")
			} else {
				trace = append(trace, "supervisor_retry_error")
			}
			return errors.New("PLANTED_SUPERVISOR_RETRY_SECRET")
		case 3:
			trace = append(trace, "supervisor_retry_wait")
			close(supervisorFinalEntered)
			<-supervisorRelease
			trace = append(trace, "supervisor_success")
			return nil
		default:
			trace = append(trace, "supervisor_extra_call")
			return nil
		}
	})
	rootClosed := make(chan struct{})
	deps := Dependencies{
		Janitor: func(context.Context, *process.Root) error {
			trace = append(trace, "janitor")
			return nil
		},
		CloseRoot: func(*process.Root) error {
			trace = append(trace, "root_close")
			close(rootClosed)
			return nil
		},
	}
	cfg := config.Config{
		Server:  config.Server{ShutdownTimeout: config.Duration(50 * time.Millisecond)},
		Runtime: config.Runtime{CleanupTimeout: config.Duration(10 * time.Millisecond)},
	}
	shutdownResult := make(chan bool, 1)
	go func() {
		shutdownResult <- shutdownServingRuntime(
			cfg,
			deps,
			server,
			tracked,
			gatewayDrain,
			[]retryableShutdown{supervisorDrain},
			nil,
		)
	}()
	t.Cleanup(func() {
		select {
		case gatewayRelease <- struct{}{}:
		default:
		}
		select {
		case supervisorRelease <- struct{}{}:
		default:
		}
		select {
		case handlerRelease <- struct{}{}:
		default:
		}
	})

	awaitAppSignal(t, gatewayFinalEntered, "Gateway background ownership retry")
	clientDeadline, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	select {
	case clientErr := <-clientResult:
		if clientErr == nil {
			t.Fatal("client request completed successfully during forced shutdown")
		}
	case <-clientDeadline.Done():
		t.Fatal("HTTP connection remained open behind blocked Gateway background retry")
	}
	select {
	case <-rootClosed:
		t.Fatal("root closed before Gateway ownership drained")
	default:
	}
	gatewayRelease <- struct{}{}

	awaitAppSignal(t, supervisorFinalEntered, "supervisor background ownership retry")
	select {
	case <-rootClosed:
		t.Fatal("root closed before supervisor ownership drained")
	default:
	}
	supervisorRelease <- struct{}{}
	if failed := awaitAppValue(t, shutdownResult, "combined-failure shutdown"); !failed {
		t.Fatal("combined bounded shutdown failures were not retained")
	}
	awaitAppSignal(t, rootClosed, "root close after ownership drain")
	handlerRelease <- struct{}{}
	if serveErr := awaitAppValue(t, serveResult, "combined-failure HTTP Serve return"); !errors.Is(serveErr, http.ErrServerClosed) {
		t.Fatalf("Serve error = %v, want http.ErrServerClosed", serveErr)
	}
	want := []string{
		"gateway_bounded",
		"gateway_retry_error",
		"gateway_retry_wait",
		"gateway_success",
		"supervisor_bounded",
		"supervisor_retry_error",
		"supervisor_retry_wait",
		"supervisor_success",
		"janitor",
		"root_close",
	}
	if gatewayCalls != 3 || supervisorCalls != 3 || !slices.Equal(trace, want) {
		t.Fatalf(
			"Gateway/supervisor calls and trace = %d/%d %q, want 3/3 %q",
			gatewayCalls,
			supervisorCalls,
			trace,
			want,
		)
	}
}

type panicWriter struct{}

func (panicWriter) Write([]byte) (int, error) {
	panic("ProductionDependencies performed eager output")
}

var _ io.Writer = panicWriter{}

type appShortWriter struct {
	calls int
}

func (writer *appShortWriter) Write(payload []byte) (int, error) {
	writer.calls++
	if len(payload) == 0 {
		return 0, nil
	}
	return len(payload) - 1, nil
}

type appErrorWriter struct {
	calls int
}

func (writer *appErrorWriter) Write([]byte) (int, error) {
	writer.calls++
	return 0, errors.New("PLANTED_WRITER_SECRET")
}

func writeStructurallyValidConfig(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	document := fmt.Sprintf(`[server]
listen = "127.0.0.1:18080"

[runtime]
root = %s

[providers.codex]
executable = %s
config_home = %s

[[models]]
id = "codex-test"
provider = "codex"
provider_model = "gpt-test"
`,
		strconv.Quote(filepath.Join(base, "runtime")),
		strconv.Quote(filepath.Join(base, "codex")),
		strconv.Quote(filepath.Join(base, "codex-home")),
	)
	path := filepath.Join(base, "config.toml")
	if err := os.WriteFile(path, []byte(document), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return path
}

func rewriteFixtureListen(t *testing.T, configPath, address string) {
	t.Helper()
	// configPath is returned by this test's private t.TempDir fixture.
	//nolint:gosec
	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read fixture config: %v", err)
	}
	updated := bytes.Replace(
		payload,
		[]byte(`listen = "127.0.0.1:18080"`),
		[]byte("listen = "+strconv.Quote(address)),
		1,
	)
	if bytes.Equal(updated, payload) {
		t.Fatal("fixture listen address was not replaced")
	}
	// configPath is returned by this test's private t.TempDir fixture.
	//nolint:gosec
	if err := os.WriteFile(configPath, updated, 0o600); err != nil {
		t.Fatalf("rewrite fixture config: %v", err)
	}
}

func addFixtureGatewayKey(t *testing.T, configPath, environmentName string) {
	t.Helper()
	// configPath is returned by this test's private t.TempDir fixture.
	//nolint:gosec
	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read fixture config: %v", err)
	}
	needle := []byte("listen = \"127.0.0.1:18080\"\n")
	replacement := []byte(
		"listen = \"127.0.0.1:18080\"\napi_key_env = " +
			strconv.Quote(environmentName) + "\n",
	)
	updated := bytes.Replace(payload, needle, replacement, 1)
	if bytes.Equal(updated, payload) {
		t.Fatal("fixture gateway key was not added")
	}
	// configPath is returned by this test's private t.TempDir fixture.
	//nolint:gosec
	if err := os.WriteFile(configPath, updated, 0o600); err != nil {
		t.Fatalf("rewrite fixture config: %v", err)
	}
}

func addFixtureGatewayKeyFile(t *testing.T, configPath, token string) string {
	t.Helper()
	keyPath := filepath.Join(filepath.Dir(configPath), "gateway.key")
	testutil.WriteTrustedFile(t, keyPath, []byte(token+"\n"), 0o600)
	// configPath is returned by this test's private trusted fixture.
	//nolint:gosec
	payload, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read fixture config: %v", err)
	}
	needle := []byte("listen = \"127.0.0.1:18080\"\n")
	replacement := []byte(
		"listen = \"127.0.0.1:18080\"\napi_key_file = " +
			strconv.Quote(keyPath) + "\n",
	)
	updated := bytes.Replace(payload, needle, replacement, 1)
	if bytes.Equal(updated, payload) {
		t.Fatal("fixture Gateway key file was not added")
	}
	// configPath is returned by this test's private trusted fixture.
	//nolint:gosec
	if err := os.WriteFile(configPath, updated, 0o600); err != nil {
		t.Fatalf("rewrite fixture config: %v", err)
	}
	return keyPath
}

type appConfigSourceObservation struct {
	loadCalls int
	source    *appTrackingConfigSource
}

func (observation *appConfigSourceObservation) dependencies(
	t *testing.T,
	wantPath string,
) startupDependencies {
	t.Helper()
	return startupDependencies{
		LoadConfigSource: func(path string) (ConfigSource, error) {
			observation.loadCalls++
			if path != wantPath {
				t.Fatalf("LoadConfigSource path = %q, want %q", path, wantPath)
			}
			snapshot, err := configsource.Load(path)
			if err != nil {
				return nil, err
			}
			observation.source = &appTrackingConfigSource{Snapshot: snapshot}
			return observation.source, nil
		},
		LoadGatewayKey: gatewaykey.LoadFile,
	}
}

func (observation *appConfigSourceObservation) assertLifecycle(
	t *testing.T,
	wantLoad, wantRevalidate, wantClose int,
) {
	t.Helper()
	if observation.source == nil {
		t.Fatal("LoadConfigSource did not return a retained source")
	}
	if observation.loadCalls != wantLoad ||
		observation.source.revalidateCalls != wantRevalidate ||
		observation.source.closeCalls != wantClose {
		t.Fatalf(
			"source load/revalidate/close calls = %d/%d/%d, want %d/%d/%d",
			observation.loadCalls,
			observation.source.revalidateCalls,
			observation.source.closeCalls,
			wantLoad,
			wantRevalidate,
			wantClose,
		)
	}
}

type appTrackingConfigSource struct {
	*configsource.Snapshot
	info            fs.FileInfo
	revalidateCalls int
	closeCalls      int
}

func (source *appTrackingConfigSource) FileInfo() fs.FileInfo {
	if source.info == nil {
		source.info = source.Snapshot.FileInfo()
	}
	return source.info
}

func (source *appTrackingConfigSource) Revalidate() error {
	source.revalidateCalls++
	return source.Snapshot.Revalidate()
}

func (source *appTrackingConfigSource) Close() error {
	source.closeCalls++
	return source.Snapshot.Close()
}

func configureFixtureHTTP(
	t *testing.T,
	fixture *readyAppFixture,
	listener *appMemoryListener,
) {
	t.Helper()
	fixture.deps.NewHTTPIDs = func() (httpapi.IDSource, error) {
		fixture.httpIDCalls++
		return &appGatewayAuthIDSource{}, nil
	}
	fixture.deps.Listen = func(network, address string) (net.Listener, error) {
		fixture.listenCalls++
		if network != "tcp" || address != "127.0.0.1:18080" {
			t.Fatalf("Listen args = %q/%q, want tcp/127.0.0.1:18080", network, address)
		}
		return listener, nil
	}
}

type appGatewayAuthIDSource struct{}

func (*appGatewayAuthIDSource) Next(string) string {
	return "req_aaaaaaaaaaaaaaaaaaaaaaaaaa"
}

type appMemoryListener struct {
	connections chan net.Conn
	closed      chan struct{}
	acceptReady chan struct{}
	acceptOnce  sync.Once
	closeOnce   sync.Once
}

func newAppMemoryListener() *appMemoryListener {
	return &appMemoryListener{
		connections: make(chan net.Conn),
		closed:      make(chan struct{}),
		acceptReady: make(chan struct{}),
	}
}

func (listener *appMemoryListener) Accept() (net.Conn, error) {
	listener.acceptOnce.Do(func() { close(listener.acceptReady) })
	select {
	case connection := <-listener.connections:
		return connection, nil
	case <-listener.closed:
		return nil, net.ErrClosed
	}
}

func (listener *appMemoryListener) Close() error {
	listener.closeOnce.Do(func() { close(listener.closed) })
	return nil
}

func (*appMemoryListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 18080}
}

func (listener *appMemoryListener) dial(t *testing.T) net.Conn {
	t.Helper()
	server, client := net.Pipe()
	select {
	case listener.connections <- server:
		return client
	case <-listener.closed:
		_ = server.Close()
		_ = client.Close()
		t.Fatal("memory listener closed before request")
		return nil
	}
}

func appMemoryRequest(t *testing.T, listener *appMemoryListener, bearer string) int {
	t.Helper()
	connection := listener.dial(t)
	defer func() { _ = connection.Close() }()
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodGet,
		"http://127.0.0.1:18080/v1/models",
		nil,
	)
	if err != nil {
		t.Fatalf("construct memory HTTP request: %v", err)
	}
	request.Close = true
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	if err := request.Write(connection); err != nil {
		t.Fatalf("write memory HTTP request: %v", err)
	}
	response, err := http.ReadResponse(bufio.NewReader(connection), request)
	if err != nil {
		t.Fatalf("read memory HTTP response: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if _, err := io.Copy(io.Discard, response.Body); err != nil {
		t.Fatalf("drain memory HTTP response: %v", err)
	}
	return response.StatusCode
}

func awaitAppSignal(t *testing.T, signal <-chan struct{}, name string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	select {
	case <-signal:
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s", name)
	}
}

func awaitAppValue[T any](t *testing.T, values <-chan T, name string) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	select {
	case value := <-values:
		return value
	case <-ctx.Done():
		t.Fatalf("timed out waiting for %s", name)
		var zero T
		return zero
	}
}

type readyAppFixture struct {
	configPath      string
	deps            Dependencies
	controller      *appTestProbeController
	executableCalls int
	openCalls       int
	janitorCalls    int
	closeCalls      int
	httpIDCalls     int
	listenCalls     int
	openedRoot      *process.Root
}

func newReadyAppFixture(t *testing.T) *readyAppFixture {
	t.Helper()
	base := testutil.TrustedTempDir(t)
	// The fixture parent intentionally requires owner-only directory access.
	//nolint:gosec
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatalf("chmod fixture parent: %v", err)
	}
	executable := filepath.Join(base, "fake-gateway")
	// The owner-only fixture must also be executable for path diagnosis.
	//nolint:gosec
	testutil.WriteTrustedFile(t, executable, []byte("fixture"), 0o700)
	configHome := testutil.TrustedTempDir(t)
	runtimeRoot := filepath.Join(base, "runtime")
	document := fmt.Sprintf(`[server]
listen = "127.0.0.1:18080"

[runtime]
root = %s

[providers.codex]
executable = %s
config_home = %s
concurrency = 1
queue_size = 2
queue_bytes = 4096
queue_timeout = "1s"
execution_timeout = "1s"

[[models]]
id = "codex-test"
provider = "codex"
provider_model = "gpt-test"
created = 7
`, strconv.Quote(runtimeRoot), strconv.Quote(executable), strconv.Quote(configHome))
	configPath := filepath.Join(base, "config.toml")
	if err := os.WriteFile(configPath, []byte(document), 0o600); err != nil {
		t.Fatalf("write ready config: %v", err)
	}

	fixture := &readyAppFixture{
		configPath: configPath,
		controller: &appTestProbeController{},
	}
	deps := ProductionDependencies(io.Discard)
	deps.Adapters[core.ProviderCodex] = &appTestAdapter{
		name: core.ProviderCodex,
		health: provider.Health{
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
		},
	}
	deps.GatewayExecutable = func() (string, error) {
		fixture.executableCalls++
		return executable, nil
	}
	deps.OpenRoot = func(path string) (*process.Root, error) {
		fixture.openCalls++
		root, err := process.OpenRoot(path)
		if err == nil {
			fixture.openedRoot = root
		}
		return root, err
	}
	deps.Janitor = func(ctx context.Context, root *process.Root) error {
		fixture.janitorCalls++
		return root.Janitor(ctx)
	}
	deps.CloseRoot = func(root *process.Root) error {
		fixture.closeCalls++
		return root.Close()
	}
	deps.NewProbeController = func(
		root *process.Root,
		limits process.Limits,
		newRuntimeID func() (string, error),
	) (doctor.ProbeController, error) {
		if root == nil || newRuntimeID == nil || limits.Execution != 5*time.Second {
			t.Fatalf("probe constructor args root=%p limits=%+v id=%v", root, limits, newRuntimeID != nil)
		}
		return fixture.controller, nil
	}
	deps.NewHTTPIDs = func() (httpapi.IDSource, error) {
		fixture.httpIDCalls++
		return nil, errors.New("PLANTED_HTTP_ID_SECRET")
	}
	deps.Listen = func(string, string) (net.Listener, error) {
		fixture.listenCalls++
		return nil, errors.New("PLANTED_LISTENER_SECRET")
	}
	fixture.deps = deps
	t.Cleanup(func() {
		if fixture.openedRoot != nil {
			_ = fixture.openedRoot.Close()
		}
	})
	return fixture
}

type appTestProbeController struct {
	selfTestCalls int
	shutdownCalls int
	selfTestHook  func()
}

func (*appTestProbeController) RunProbe(
	context.Context,
	func(process.Runtime) (process.CommandSpec, error),
) (process.Result, error) {
	return process.Result{}, nil
}

func (c *appTestProbeController) SelfTest(context.Context, string) error {
	c.selfTestCalls++
	if c.selfTestHook != nil {
		c.selfTestHook()
	}
	return nil
}

func (c *appTestProbeController) Shutdown(context.Context) error {
	c.shutdownCalls++
	return nil
}

func (*appTestProbeController) CleanupFailed() bool { return false }

type appTestAdapter struct {
	name   core.ProviderName
	health provider.Health
}

type appTestErrorListener struct {
	closeCalls int
}

type appTestHTTPHandler struct{}

func (*appTestHTTPHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

type appTestIDSource struct{}

func (*appTestIDSource) Next(string) string { return "unused" }

type appDriftingAdapter struct {
	nameCalls       int
	stableNameCalls int
	health          provider.Health
}

func (adapter *appDriftingAdapter) Name() core.ProviderName {
	adapter.nameCalls++
	if adapter.nameCalls <= adapter.stableNameCalls {
		return core.ProviderCodex
	}
	return core.ProviderClaude
}

func (*appDriftingAdapter) SupportedVersion() provider.Range {
	return provider.Range{
		MinInclusive: provider.Version{Major: 1},
		MaxExclusive: provider.Version{Major: 2},
	}
}

func (adapter *appDriftingAdapter) Probe(
	context.Context,
	provider.ProviderConfig,
	provider.ProbeRunner,
) provider.Health {
	return adapter.health.Clone()
}

func (*appDriftingAdapter) Build(
	core.Request,
	core.Model,
	provider.ProviderConfig,
	process.Runtime,
) (process.CommandSpec, error) {
	return process.CommandSpec{}, nil
}

func (*appDriftingAdapter) Parse(core.Request, process.Result) (string, error) {
	return "fixture", nil
}

type appRootClosingProbeController struct {
	*appTestProbeController
	root *process.Root
}

func (controller *appRootClosingProbeController) Shutdown(context.Context) error {
	controller.shutdownCalls++
	return controller.root.Close()
}

type retryableShutdownFunc func(context.Context) error

func (function retryableShutdownFunc) Shutdown(ctx context.Context) error {
	return function(ctx)
}

type appTraceListener struct {
	trace *[]string
}

type appBlockingCloseErrorListener struct {
	trace          *[]string
	acceptStarted  chan struct{}
	acceptReturned chan struct{}
	closed         chan struct{}
	closeCalls     int
}

func (listener *appBlockingCloseErrorListener) Accept() (net.Conn, error) {
	close(listener.acceptStarted)
	<-listener.closed
	close(listener.acceptReturned)
	return nil, net.ErrClosed
}

func (listener *appBlockingCloseErrorListener) Close() error {
	listener.closeCalls++
	*listener.trace = append(*listener.trace, "listener_close")
	close(listener.closed)
	return errors.New("PLANTED_SERVING_LISTENER_CLOSE_SECRET")
}

func (*appBlockingCloseErrorListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 18080}
}

func (*appTraceListener) Accept() (net.Conn, error) {
	return nil, net.ErrClosed
}

func (listener *appTraceListener) Close() error {
	*listener.trace = append(*listener.trace, "listener_close")
	return nil
}

func (*appTraceListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 18080}
}

func (*appTestErrorListener) Accept() (net.Conn, error) {
	return nil, errors.New("PLANTED_ACCEPT_SECRET")
}

func (l *appTestErrorListener) Close() error {
	l.closeCalls++
	return errors.New("PLANTED_LISTENER_CLOSE_SECRET")
}

func (*appTestErrorListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 18080}
}

func (a *appTestAdapter) Name() core.ProviderName { return a.name }

func (*appTestAdapter) SupportedVersion() provider.Range {
	return provider.Range{
		MinInclusive: provider.Version{Major: 1},
		MaxExclusive: provider.Version{Major: 2},
	}
}

func (a *appTestAdapter) Probe(
	context.Context,
	provider.ProviderConfig,
	provider.ProbeRunner,
) provider.Health {
	return a.health.Clone()
}

func (*appTestAdapter) Build(
	core.Request,
	core.Model,
	provider.ProviderConfig,
	process.Runtime,
) (process.CommandSpec, error) {
	return process.CommandSpec{}, nil
}

func (*appTestAdapter) Parse(core.Request, process.Result) (string, error) {
	return "fixture", nil
}
