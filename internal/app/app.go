// Package app assembles the diagnosed provider runtime and owns its lifecycle.
package app

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/doctor"
	"github.com/krkarma777/ai-cli-gateway/internal/gateway"
	"github.com/krkarma777/ai-cli-gateway/internal/httpapi"
	"github.com/krkarma777/ai-cli-gateway/internal/observability"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
	"github.com/krkarma777/ai-cli-gateway/internal/provider/claude"
	"github.com/krkarma777/ai-cli-gateway/internal/provider/codex"
	"github.com/krkarma777/ai-cli-gateway/internal/provider/gemini"
	"github.com/krkarma777/ai-cli-gateway/internal/scheduler"
	"github.com/krkarma777/ai-cli-gateway/internal/schema"
)

var (
	// ErrConfigInvalid identifies a rejected configuration without exposing it.
	ErrConfigInvalid = errors.New("configuration_invalid")
	// ErrNotReady identifies a safe diagnosis with no serving provider.
	ErrNotReady = errors.New("gateway_not_ready")
	// ErrStartup identifies a fixed startup assembly failure.
	ErrStartup = errors.New("startup_failed")
	// ErrServe identifies an unexpected HTTP serving failure.
	ErrServe = errors.New("serve_failed")
	// ErrShutdown identifies a failure in the owned shutdown sequence.
	ErrShutdown = errors.New("shutdown_failed")

	errRuntimeID = errors.New("runtime ID generation failed")
)

// Dependencies contains only primitive process-local side-effect seams.
type Dependencies struct {
	Adapters map[core.ProviderName]provider.Adapter

	LookupEnv          provider.LookupEnv
	LookupExecutable   func(string) (string, error)
	NewRuntimeID       func() (string, error)
	GatewayExecutable  func() (string, error)
	OpenRoot           func(string) (*process.Root, error)
	Janitor            func(context.Context, *process.Root) error
	CloseRoot          func(*process.Root) error
	NewProbeController func(
		*process.Root,
		process.Limits,
		func() (string, error),
	) (doctor.ProbeController, error)

	NewHTTPIDs func() (httpapi.IDSource, error)
	Now        func() time.Time
	Listen     func(network, address string) (net.Listener, error)
	Logger     *slog.Logger
}

// ProductionDependencies constructs lazy production seams without performing
// filesystem, environment, entropy, provider, or listener work.
func ProductionDependencies(logWriter io.Writer) Dependencies {
	var logger *slog.Logger
	if logWriter != nil {
		logger = slog.New(slog.NewJSONHandler(logWriter, nil))
	}
	return Dependencies{
		Adapters: map[core.ProviderName]provider.Adapter{
			core.ProviderCodex:  codex.New(),
			core.ProviderClaude: claude.New(),
			core.ProviderGemini: gemini.New(),
		},
		LookupEnv:         os.LookupEnv,
		LookupExecutable:  exec.LookPath,
		NewRuntimeID:      newProductionRuntimeID,
		GatewayExecutable: os.Executable,
		OpenRoot:          process.OpenRoot,
		Janitor: func(ctx context.Context, root *process.Root) error {
			return root.Janitor(ctx)
		},
		CloseRoot: func(root *process.Root) error {
			return root.Close()
		},
		NewProbeController: doctor.NewProcessProbeController,
		NewHTTPIDs: func() (httpapi.IDSource, error) {
			return httpapi.NewOpaqueIDSource(rand.Reader)
		},
		Now:    time.Now,
		Listen: net.Listen,
		Logger: logger,
	}
}

func newProductionRuntimeID() (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(rand.Reader, value[:]); err != nil {
		return "", errRuntimeID
	}
	return hex.EncodeToString(value[:]), nil
}

// Serve loads configuration before touching any injected dependency.
func Serve(ctx context.Context, configPath string, deps Dependencies) error {
	cfg, err := config.Load(configPath)
	if err != nil {
		return ErrConfigInvalid
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil {
		return nil //nolint:nilerr // Pre-start cancellation is an intentional clean stop.
	}
	if !validServeDependencies(deps) {
		return ErrStartup
	}
	selected, ok := selectAdapters(cfg, deps.Adapters)
	if !ok {
		return ErrStartup
	}
	executable, err := deps.GatewayExecutable()
	if err != nil {
		return ErrStartup
	}
	diagnosis, runErr := doctor.Run(ctx, cfg, doctor.Dependencies{
		Adapters:           selected,
		LookupEnv:          deps.LookupEnv,
		LookupExecutable:   deps.LookupExecutable,
		NewRuntimeID:       deps.NewRuntimeID,
		OpenRoot:           deps.OpenRoot,
		Janitor:            deps.Janitor,
		CloseRoot:          deps.CloseRoot,
		NewProbeController: deps.NewProbeController,
		GatewayExecutable:  executable,
	})
	if runErr != nil {
		if callerErr := ctx.Err(); callerErr != nil && errors.Is(runErr, callerErr) {
			return nil //nolint:nilerr // Matching caller cancellation is an intentional clean stop.
		}
		return ErrStartup
	}
	report := diagnosis.Report()
	if !report.CoreReady() {
		return ErrNotReady
	}
	root := diagnosis.RuntimeRoot
	diagnosis.RuntimeRoot = nil
	registry := diagnosis.Registry()
	resolved := diagnosis.ResolvedProviders()
	if root == nil || registry == nil {
		if root != nil {
			return joinShutdown(ErrStartup, cleanupRoot(cfg, deps, root))
		}
		return ErrStartup
	}
	if report.ReadyCount() == 0 {
		return joinShutdown(ErrNotReady, cleanupRoot(cfg, deps, root))
	}
	rows, ok := validateDiagnosisMembership(cfg, selected, report, resolved, registry)
	if !ok {
		return joinShutdown(ErrStartup, cleanupRoot(cfg, deps, root))
	}
	runtimes, supervisors, scheduled, ok := assembleProviderRuntimes(
		cfg,
		selected,
		rows,
		resolved,
		root,
	)
	if !ok {
		failed := unwindRuntime(cfg, deps, nil, scheduled, supervisors, root)
		return joinShutdown(ErrStartup, failed)
	}

	schemaLimits, err := schema.DefaultLimits(
		cfg.Server.SchemaBytes,
		int(cfg.Runtime.FinalBytes),
	)
	if err != nil {
		failed := unwindRuntime(cfg, deps, nil, scheduled, supervisors, root)
		return joinShutdown(ErrStartup, failed)
	}
	requestLimits, err := httpapi.NewRequestLimits(
		cfg.Server.InputBytes,
		cfg.Server.InstructionsBytes,
		cfg.Server.SchemaBytes,
	)
	if err != nil {
		failed := unwindRuntime(cfg, deps, nil, scheduled, supervisors, root)
		return joinShutdown(ErrStartup, failed)
	}
	applicationGateway, err := gateway.New(
		registry,
		runtimes,
		gateway.Config{
			SchemaLimits: schemaLimits,
			FinalBytes:   int(cfg.Runtime.FinalBytes),
		},
		gateway.Dependencies{
			NewRuntimeID: deps.NewRuntimeID,
			Now:          deps.Now,
		},
	)
	if err != nil {
		failed := unwindRuntime(cfg, deps, nil, scheduled, supervisors, root)
		return joinShutdown(ErrStartup, failed)
	}
	// Gateway owns every scheduler after successful construction; no later
	// unwind path references the pre-Gateway scheduler slice.

	ids, err := deps.NewHTTPIDs()
	if err != nil || nilLike(ids) {
		failed := unwindRuntime(cfg, deps, applicationGateway, nil, supervisors, root)
		return joinShutdown(ErrStartup, failed)
	}
	counters := &observability.Counters{}
	server, handler, err := httpapi.New(
		httpapi.Config{
			Listen:            cfg.Server.Listen,
			APIKeyEnv:         cfg.Server.APIKeyEnv,
			HTTPBodyBytes:     cfg.Server.HTTPBodyBytes,
			RequestLimits:     requestLimits,
			HandlerLimit:      cfg.Server.HandlerLimit,
			BodyReaderLimit:   cfg.Server.BodyReaderLimit,
			MaxHeaderBytes:    cfg.Server.MaxHeaderBytes,
			ReadHeaderTimeout: time.Duration(cfg.Server.ReadHeaderTimeout),
			BodyReadTimeout:   time.Duration(cfg.Server.BodyReadTimeout),
			IdleTimeout:       time.Duration(cfg.Server.IdleTimeout),
			FinalBytes:        int(cfg.Runtime.FinalBytes),
			MaxModels:         len(cfg.Models),
		},
		httpapi.Dependencies{
			Now:       deps.Now,
			LookupEnv: deps.LookupEnv,
			IDs:       ids,
			Counters:  counters,
		},
		applicationGateway,
		deps.Logger,
	)
	if err != nil || !validHTTPBoundary(server, handler) {
		failed := unwindRuntime(cfg, deps, applicationGateway, nil, supervisors, root)
		return joinShutdown(ErrStartup, failed)
	}
	listener, err := deps.Listen("tcp", cfg.Server.Listen)
	if err != nil || nilLike(listener) {
		listenerCloseFailed := false
		if listener != nil && !nilLike(listener) {
			listenerCloseFailed = listener.Close() != nil
		}
		failed := unwindRuntime(cfg, deps, applicationGateway, nil, supervisors, root)
		failed = listenerCloseFailed || failed
		return joinShutdown(ErrStartup, failed)
	}
	failed, primary := serveOwnedRuntime(
		ctx,
		cfg,
		deps,
		server,
		listener,
		applicationGateway,
		supervisors,
		root,
	)
	return joinShutdown(primary, failed)
}

func serveOwnedRuntime(
	ctx context.Context,
	cfg config.Config,
	deps Dependencies,
	server *http.Server,
	listener net.Listener,
	applicationGateway retryableShutdown,
	supervisors []retryableShutdown,
	root *process.Root,
) (bool, error) {
	tracked := newShutdownAwareListener(listener)
	serveResults := make(chan error, 1)
	go func() {
		serveResults <- server.Serve(tracked)
	}()

	serveReturned := false
	primary := error(nil)
	select {
	case <-serveResults:
		serveReturned = true
		primary = ErrServe
	case <-ctx.Done():
	}

	failed := shutdownServingRuntime(
		cfg,
		deps,
		server,
		tracked,
		applicationGateway,
		supervisors,
		root,
	)
	if !serveReturned {
		serveErr := <-serveResults
		if !errors.Is(serveErr, http.ErrServerClosed) {
			primary = ErrServe
		}
	}
	return failed, primary
}

func shutdownServingRuntime(
	cfg config.Config,
	deps Dependencies,
	server *http.Server,
	listener *shutdownAwareListener,
	applicationGateway retryableShutdown,
	supervisors []retryableShutdown,
	root *process.Root,
) bool {
	failed := false
	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.Server.ShutdownTimeout),
	)
	httpResults := make(chan error, 1)
	go func() {
		httpResults <- server.Shutdown(shutdownCtx)
	}()

	httpReturned := false
	var httpErr error
	closeFailed := false
	select {
	case closeFailed = <-listener.closeResult:
	case httpErr = <-httpResults:
		httpReturned = true
		_ = listener.Close()
		closeFailed = <-listener.closeResult
	}

	gatewayFailed := applicationGateway.Shutdown(shutdownCtx) != nil
	failed = gatewayFailed || failed
	if !httpReturned {
		httpErr = <-httpResults
	}
	if httpErr != nil {
		failed = true
		_ = server.Close()
	}
	if closeFailed {
		failed = true
	}
	if gatewayFailed {
		retryShutdownUntilSuccess(applicationGateway.Shutdown)
	}
	for index := len(supervisors) - 1; index >= 0; index-- {
		failed = drainRetryable(shutdownCtx, supervisors[index].Shutdown) || failed
	}
	cancel()
	if cleanupRoot(cfg, deps, root) {
		failed = true
	}
	return failed
}

type shutdownAwareListener struct {
	net.Listener
	once        sync.Once
	closeResult chan bool
	closeErr    error
}

func newShutdownAwareListener(listener net.Listener) *shutdownAwareListener {
	return &shutdownAwareListener{
		Listener:    listener,
		closeResult: make(chan bool, 1),
	}
}

func (listener *shutdownAwareListener) Close() error {
	listener.once.Do(func() {
		listener.closeErr = listener.Listener.Close()
		listener.closeResult <- listener.closeErr != nil
		close(listener.closeResult)
	})
	return listener.closeErr
}

func validateDiagnosisMembership(
	cfg config.Config,
	selected map[core.ProviderName]provider.Adapter,
	report doctor.Report,
	resolved map[core.ProviderName]doctor.ResolvedProvider,
	registry *core.Registry,
) ([]doctor.Provider, bool) {
	configured := make([]core.ProviderName, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		configured = append(configured, core.ProviderName(name))
	}
	sort.Slice(configured, func(left, right int) bool { return configured[left] < configured[right] })
	rows := report.Providers()
	if len(rows) != len(configured) || len(selected) != len(configured) {
		return nil, false
	}
	for index, name := range configured {
		row := rows[index]
		adapter, present := selected[name]
		resolvedProvider, hasResolved := resolved[name]
		if row.Name != name || !present || nilLike(adapter) || adapter.Name() != name ||
			(row.Status == provider.HealthReady && !hasResolved) {
			return nil, false
		}
		if hasResolved && !healthMatchesRow(resolvedProvider.Health, row) {
			return nil, false
		}
	}
	for name := range resolved {
		if _, configuredProvider := cfg.Providers[string(name)]; !configuredProvider {
			return nil, false
		}
	}
	models := registry.Models()
	if !registryMatchesConfig(registry, cfg.Models) ||
		!slices.Equal(report.Models(), modelAliases(models)) {
		return nil, false
	}
	for _, model := range models {
		if _, present := cfg.Providers[string(model.Provider)]; !present {
			return nil, false
		}
	}
	return rows, true
}

func registryMatchesConfig(registry *core.Registry, configured []config.Model) bool {
	if registry == nil {
		return false
	}
	models := registry.Models()
	if len(models) != len(configured) {
		return false
	}
	byID := make(map[string]config.Model, len(configured))
	for _, model := range configured {
		if _, duplicate := byID[model.ID]; duplicate {
			return false
		}
		byID[model.ID] = model
	}
	for _, model := range models {
		want, present := byID[model.ID]
		if !present || model.Provider != core.ProviderName(want.Provider) ||
			model.ProviderModel != want.ProviderModel || model.Created != want.Created {
			return false
		}
		delete(byID, model.ID)
	}
	return len(byID) == 0
}

func validHTTPBoundary(server *http.Server, handler http.Handler) bool {
	return server != nil && !nilLike(handler) && !nilLike(server.Handler)
}

func modelAliases(models []core.Model) []string {
	aliases := make([]string, len(models))
	for index, model := range models {
		aliases[index] = model.ID
	}
	return aliases
}

func healthMatchesRow(health provider.Health, row doctor.Provider) bool {
	return health.Provider == row.Name &&
		health.Status == row.Status &&
		health.Version == row.Version &&
		health.Auth == row.Auth &&
		slices.Equal(health.Capabilities, row.Capabilities) &&
		slices.Equal(health.Problems, row.Problems)
}

func reportHealth(row doctor.Provider) provider.Health {
	return provider.Health{
		Provider:     row.Name,
		Status:       row.Status,
		Version:      row.Version,
		Auth:         row.Auth,
		Capabilities: slices.Clone(row.Capabilities),
		Problems:     slices.Clone(row.Problems),
	}
}

func assembleProviderRuntimes(
	cfg config.Config,
	selected map[core.ProviderName]provider.Adapter,
	rows []doctor.Provider,
	resolved map[core.ProviderName]doctor.ResolvedProvider,
	root *process.Root,
) (
	map[core.ProviderName]*gateway.ProviderRuntime,
	[]retryableShutdown,
	[]retryableShutdown,
	bool,
) {
	runtimes := make(map[core.ProviderName]*gateway.ProviderRuntime, len(rows))
	supervisors := make([]retryableShutdown, 0, len(rows))
	scheduled := make([]retryableShutdown, 0, len(rows))
	for _, row := range rows {
		adapter := selected[row.Name]
		resolvedProvider, hasResolved := resolved[row.Name]
		providerConfig := provider.ProviderConfig{}
		health := reportHealth(row)
		if hasResolved {
			providerConfig = resolvedProvider.Config.Clone()
			health = resolvedProvider.Health.Clone()
		}
		var supervisor *process.Supervisor
		var providerScheduler *scheduler.Scheduler
		if row.Status == provider.HealthReady {
			if !hasResolved {
				return runtimes, supervisors, scheduled, false
			}
			configured := cfg.Providers[string(row.Name)]
			var err error
			supervisor, err = process.NewSupervisor(root, process.Limits{
				Execution:   time.Duration(configured.ExecutionTimeout),
				TermGrace:   time.Duration(cfg.Runtime.TermGrace),
				Cleanup:     time.Duration(cfg.Runtime.CleanupTimeout),
				StdoutBytes: cfg.Runtime.StdoutBytes,
				StderrBytes: cfg.Runtime.StderrBytes,
			})
			if err != nil {
				return runtimes, supervisors, scheduled, false
			}
			supervisors = append(supervisors, supervisor)
			providerScheduler, err = scheduler.New(scheduler.Limits{
				Concurrency:  configured.Concurrency,
				QueueSize:    configured.QueueSize,
				QueueBytes:   configured.QueueBytes,
				QueueTimeout: time.Duration(configured.QueueTimeout),
			})
			if err != nil {
				return runtimes, supervisors, scheduled, false
			}
			scheduled = append(scheduled, providerScheduler)
		}
		runtime, err := gateway.NewProviderRuntime(
			adapter,
			providerConfig,
			providerScheduler,
			supervisor,
			health,
		)
		if err != nil {
			return runtimes, supervisors, scheduled, false
		}
		runtimes[row.Name] = runtime
	}
	return runtimes, supervisors, scheduled, len(runtimes) == len(cfg.Providers)
}

func unwindRuntime(
	cfg config.Config,
	deps Dependencies,
	applicationGateway retryableShutdown,
	scheduled []retryableShutdown,
	supervisors []retryableShutdown,
	root *process.Root,
) bool {
	failed := false
	shutdownCtx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.Server.ShutdownTimeout),
	)
	if applicationGateway != nil {
		failed = drainRetryable(shutdownCtx, applicationGateway.Shutdown) || failed
	} else {
		for index := len(scheduled) - 1; index >= 0; index-- {
			failed = drainRetryable(shutdownCtx, scheduled[index].Shutdown) || failed
		}
	}
	for index := len(supervisors) - 1; index >= 0; index-- {
		failed = drainRetryable(shutdownCtx, supervisors[index].Shutdown) || failed
	}
	cancel()
	if cleanupRoot(cfg, deps, root) {
		failed = true
	}
	return failed
}

type retryableShutdown interface {
	Shutdown(context.Context) error
}

func drainRetryable(
	bounded context.Context,
	shutdown func(context.Context) error,
) bool {
	if shutdown(bounded) == nil {
		return false
	}
	retryShutdownUntilSuccess(shutdown)
	return true
}

func retryShutdownUntilSuccess(shutdown func(context.Context) error) {
	for {
		if shutdown(context.Background()) == nil {
			return
		}
	}
}

func cleanupRoot(cfg config.Config, deps Dependencies, root *process.Root) bool {
	failed := false
	cleanupCtx, cancel := context.WithTimeout(
		context.Background(),
		time.Duration(cfg.Runtime.CleanupTimeout),
	)
	if err := deps.Janitor(cleanupCtx, root); err != nil {
		failed = true
	}
	cancel()
	if err := deps.CloseRoot(root); err != nil {
		failed = true
	}
	return failed
}

func joinShutdown(primary error, failed bool) error {
	if !failed {
		return primary
	}
	return errors.Join(primary, ErrShutdown)
}

func validDoctorDependencies(deps Dependencies) bool {
	return deps.LookupEnv != nil &&
		deps.LookupExecutable != nil &&
		deps.NewRuntimeID != nil &&
		deps.GatewayExecutable != nil &&
		deps.OpenRoot != nil &&
		deps.Janitor != nil &&
		deps.CloseRoot != nil &&
		deps.NewProbeController != nil
}

func validServeDependencies(deps Dependencies) bool {
	return validDoctorDependencies(deps) &&
		deps.NewHTTPIDs != nil &&
		deps.Now != nil &&
		deps.Listen != nil &&
		deps.Logger != nil
}

func selectAdapters(
	cfg config.Config,
	provided map[core.ProviderName]provider.Adapter,
) (map[core.ProviderName]provider.Adapter, bool) {
	selected := make(map[core.ProviderName]provider.Adapter, len(cfg.Providers))
	for configured := range cfg.Providers {
		name := core.ProviderName(configured)
		adapter, ok := provided[name]
		if !ok || nilLike(adapter) {
			return nil, false
		}
		selected[name] = adapter
	}
	return selected, len(selected) != 0
}

func nilLike(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	// Only the nil-capable kinds need explicit handling.
	//nolint:exhaustive
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

// Doctor loads configuration and emits only fixed failure text at this boundary.
func Doctor(
	ctx context.Context,
	configPath string,
	jsonOutput bool,
	stdout io.Writer,
	deps Dependencies,
) int {
	cfg, err := config.Load(configPath)
	if err != nil {
		writeFixed(stdout, "configuration_invalid\n")
		return 2
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || !validDoctorDependencies(deps) {
		writeFixed(stdout, "doctor_failed\n")
		return 1
	}
	adapters, ok := selectAdapters(cfg, deps.Adapters)
	if !ok {
		writeFixed(stdout, "doctor_failed\n")
		return 1
	}
	executable, err := deps.GatewayExecutable()
	if err != nil {
		writeFixed(stdout, "doctor_failed\n")
		return 1
	}
	diagnosis, runErr := doctor.Run(ctx, cfg, doctor.Dependencies{
		Adapters:           adapters,
		LookupEnv:          deps.LookupEnv,
		LookupExecutable:   deps.LookupExecutable,
		NewRuntimeID:       deps.NewRuntimeID,
		OpenRoot:           deps.OpenRoot,
		Janitor:            deps.Janitor,
		CloseRoot:          deps.CloseRoot,
		NewProbeController: deps.NewProbeController,
		GatewayExecutable:  executable,
	})
	if runErr != nil {
		if diagnosis.RuntimeRoot != nil {
			_ = deps.CloseRoot(diagnosis.RuntimeRoot)
		}
		writeFixed(stdout, "doctor_failed\n")
		return 1
	}

	report := diagnosis.Report()
	var writeErr error
	if jsonOutput {
		writeErr = doctor.WriteJSON(stdout, report)
	} else {
		writeErr = doctor.WriteText(stdout, report)
	}
	var closeErr error
	if diagnosis.RuntimeRoot != nil {
		closeErr = deps.CloseRoot(diagnosis.RuntimeRoot)
	}
	if writeErr != nil || closeErr != nil || !report.CoreReady() || report.ReadyCount() == 0 {
		return 1
	}
	return 0
}

func writeFixed(writer io.Writer, value string) {
	if writer != nil {
		_, _ = io.WriteString(writer, value)
	}
}
