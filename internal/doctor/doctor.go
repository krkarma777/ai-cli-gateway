// Package doctor validates gateway and provider readiness without exposing
// untrusted provider or operating-system details.
package doctor

import (
	"context"
	"errors"
	"io/fs"
	"net"
	"os"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/gatewaykey"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

// Check is one closed gateway readiness result.
type Check struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

// Provider is one filtered provider readiness result.
//
//nolint:revive // The controlling brief fixes this public read-view name.
type Provider struct {
	Name         core.ProviderName     `json:"name"`
	Status       provider.HealthStatus `json:"status"`
	Version      string                `json:"version,omitempty"`
	Auth         string                `json:"auth"`
	Capabilities []string              `json:"capabilities"`
	Problems     []string              `json:"problems,omitempty"`
}

type reportPhase uint8

const (
	reportPhaseUnconstructed reportPhase = iota
	reportPhaseCore
	reportPhaseProviders
	reportPhaseComplete
)

var (
	// ErrInvalidDependencies identifies an invalid doctor dependency graph.
	ErrInvalidDependencies = errors.New("doctor dependencies are invalid")
	// ErrDiagnosis identifies a failure to construct a closed safe diagnosis.
	ErrDiagnosis = errors.New("doctor diagnosis failed")
	// ErrInvalidReport identifies a report that is not a complete closed snapshot.
	ErrInvalidReport = errors.New("doctor report is invalid")
	// ErrReportWrite identifies a diagnostic output failure without exposing its cause.
	ErrReportWrite = errors.New("doctor report write failed")
)

// ProbeController owns all process runtimes used during diagnosis.
type ProbeController interface {
	provider.ProbeRunner
	SelfTest(context.Context, string) error
	Shutdown(context.Context) error
	CleanupFailed() bool
}

// Dependencies supplies the narrow side-effect seams used by Run.
type Dependencies struct {
	Adapters           map[core.ProviderName]provider.Adapter
	ConfigIdentity     fs.FileInfo
	LookupEnv          provider.LookupEnv
	LoadGatewayKey     func(string, []fs.FileInfo) (gatewaykey.Snapshot, error)
	LookupExecutable   func(string) (string, error)
	NewRuntimeID       func() (string, error)
	OpenRoot           func(string) (*process.Root, error)
	Janitor            func(context.Context, *process.Root) error
	CloseRoot          func(*process.Root) error
	NewProbeController func(
		*process.Root,
		process.Limits,
		func() (string, error),
	) (ProbeController, error)
	GatewayExecutable string
}

type probeSupervisor interface {
	Prepare(string) (process.Runtime, error)
	Discard(context.Context, process.Runtime) error
	Execute(context.Context, process.Runtime, process.CommandSpec) (process.Result, error)
	SelfTest(context.Context, string) error
	Shutdown(context.Context) error
}

type processProbeController struct {
	supervisor    probeSupervisor
	newRuntimeID  func() (string, error)
	cleanupFailed atomic.Bool
}

var errProbeCleanupLatched = errors.New("probe cleanup previously failed")

// NewProcessProbeController constructs the production controller over one
// locked runtime root.
func NewProcessProbeController(
	root *process.Root,
	limits process.Limits,
	newRuntimeID func() (string, error),
) (ProbeController, error) {
	if newRuntimeID == nil {
		return nil, errors.New("runtime ID generator is unavailable")
	}
	supervisor, err := process.NewSupervisor(root, limits)
	if err != nil {
		return nil, err
	}
	return &processProbeController{
		supervisor:   supervisor,
		newRuntimeID: newRuntimeID,
	}, nil
}

func (c *processProbeController) RunProbe(
	ctx context.Context,
	build func(process.Runtime) (process.CommandSpec, error),
) (process.Result, error) {
	if c == nil || c.supervisor == nil || c.newRuntimeID == nil {
		return process.Result{}, &process.RunError{
			Kind: process.ErrorCleanup,
			Err:  errProbeCleanupLatched,
		}
	}
	if c.cleanupFailed.Load() {
		return process.Result{}, &process.RunError{
			Kind: process.ErrorCleanup,
			Err:  errProbeCleanupLatched,
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	id, err := c.newRuntimeID()
	if err != nil {
		return process.Result{}, err
	}
	runtime, err := c.supervisor.Prepare(id)
	if err != nil {
		return process.Result{}, err
	}
	if build == nil {
		err = errors.New("probe command builder is unavailable")
	} else {
		var spec process.CommandSpec
		spec, err = build(runtime)
		if err == nil {
			result, executeErr := c.supervisor.Execute(ctx, runtime, spec)
			c.latchCleanup(executeErr)
			return result, executeErr
		}
	}
	discardErr := c.supervisor.Discard(ctx, runtime)
	c.latchCleanup(discardErr)
	if discardErr != nil {
		return process.Result{}, discardErr
	}
	return process.Result{}, err
}

func (c *processProbeController) SelfTest(ctx context.Context, executable string) error {
	if c == nil || c.supervisor == nil {
		return &process.RunError{Kind: process.ErrorCleanup, Err: errProbeCleanupLatched}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := c.supervisor.SelfTest(ctx, executable)
	c.latchCleanup(err)
	return err
}

func (c *processProbeController) Shutdown(ctx context.Context) error {
	if c == nil || c.supervisor == nil {
		return &process.RunError{Kind: process.ErrorCleanup, Err: errProbeCleanupLatched}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	err := c.supervisor.Shutdown(ctx)
	c.latchCleanup(err)
	return err
}

func (c *processProbeController) CleanupFailed() bool {
	return c != nil && c.cleanupFailed.Load()
}

func (c *processProbeController) latchCleanup(err error) {
	if cleanupCategory(err) {
		c.cleanupFailed.Store(true)
	}
}

func cleanupCategory(err error) bool {
	var runErr *process.RunError
	return errors.As(err, &runErr) && runErr != nil && runErr.Kind == process.ErrorCleanup
}

// Run validates dependencies before starting ordered diagnosis.
func Run(ctx context.Context, cfg config.Config, dependencies Dependencies) (Diagnosis, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = cloneConfig(cfg)
	if dependencies.LookupEnv == nil ||
		dependencies.LookupExecutable == nil ||
		dependencies.NewRuntimeID == nil ||
		dependencies.OpenRoot == nil ||
		dependencies.Janitor == nil ||
		dependencies.CloseRoot == nil ||
		dependencies.NewProbeController == nil ||
		(cfg.Server.APIKeyFile != "" && dependencies.LoadGatewayKey == nil) {
		return Diagnosis{}, ErrInvalidDependencies
	}
	resolvedGateway, err := resolveGatewayExecutable(dependencies.GatewayExecutable)
	if err != nil {
		return Diagnosis{}, ErrInvalidDependencies
	}

	names, adapters, ranges, ok := snapshotAdapters(cfg, dependencies.Adapters)
	if !ok {
		return Diagnosis{}, ErrInvalidDependencies
	}
	registry, models, err := buildRegistry(cfg.Models)
	if err != nil {
		return Diagnosis{}, ErrDiagnosis
	}
	builder := newReportBuilder(names, models, ranges)
	checks := initialCoreChecks()
	gatewayAuth, preflightFrozen, authReady := snapshotGatewayAuth(cfg, names, dependencies)
	if !authReady {
		checks[1] = gatewayAuthFailureCheck()
	}
	if !validListener(cfg.Server.Listen) {
		checks[0] = Check{
			Name: "listener", Status: checkStatusFail,
			Code: "listener_unsafe", Message: "listener is unsafe",
		}
	}
	if !validSchedulerConfig(cfg.Providers) {
		checks[2] = Check{
			Name: "scheduler", Status: checkStatusFail,
			Code: "scheduler_invalid", Message: "provider scheduler configuration is invalid",
		}
	}
	if checks[0].Status != checkStatusPass ||
		checks[1].Status != checkStatusPass ||
		checks[2].Status != checkStatusPass {
		rows := skippedProviderRows(names)
		clearFrozenLookups(preflightFrozen)
		diagnosis, finishErr := finishDiagnosis(
			builder, checks, rows, models, registry, nil, gatewayAuth, nil,
		)
		if finishErr != nil {
			return Diagnosis{}, ErrDiagnosis
		}
		if ctx != nil && ctx.Err() != nil {
			return diagnosis, ctx.Err()
		}
		return diagnosis, nil
	}

	root, openErr := dependencies.OpenRoot(cfg.Runtime.Root)
	if root != nil && openErr != nil {
		_ = dependencies.CloseRoot(root)
		clearFrozenLookups(preflightFrozen)
		return Diagnosis{}, ErrDiagnosis
	}
	if openErr != nil || root == nil {
		checks[3] = Check{
			Name: "runtime_root", Status: checkStatusFail,
			Code: "runtime_unsafe", Message: "runtime root is unsafe",
		}
		if errors.Is(openErr, process.ErrRootLocked) {
			checks[3].Code = "runtime_locked"
			checks[3].Message = "runtime root is already locked"
		}
		return finishRun(
			ctx, dependencies, builder, checks, skippedProviderRows(names), models,
			registry, nil, gatewayAuth, nil, nil, false, false, preflightFrozen,
		)
	}
	checks[3] = Check{Name: "runtime_root", Status: checkStatusPass}
	if err := dependencies.Janitor(ctx, root); err != nil {
		checks[4] = runtimeCleanupFailureCheck("runtime_janitor")
		return finishRun(
			ctx, dependencies, builder, checks, skippedProviderRows(names), models,
			registry, nil, gatewayAuth, root, nil, false, false, preflightFrozen,
		)
	}
	checks[4] = Check{Name: "runtime_janitor", Status: checkStatusPass}

	controller, controllerErr := dependencies.NewProbeController(
		root,
		doctorProbeLimits(),
		dependencies.NewRuntimeID,
	)
	if controllerErr != nil || nilInterface(controller) {
		checks[5] = containmentFailureCheck()
		if controllerErr != nil && !nilInterface(controller) {
			return finishRun(
				ctx, dependencies, builder, checks, skippedProviderRows(names), models,
				registry, nil, gatewayAuth, root, controller, false, false, preflightFrozen,
			)
		}
		return finishRun(
			ctx, dependencies, builder, checks, skippedProviderRows(names), models,
			registry, nil, gatewayAuth, root, nil, false, false, preflightFrozen,
		)
	}
	selfTestErr := controller.SelfTest(ctx, resolvedGateway)
	cleanupFailed := controller.CleanupFailed() || cleanupCategory(selfTestErr)
	if selfTestErr != nil || cleanupFailed {
		checks[5] = containmentFailureCheck()
		return finishRun(
			ctx, dependencies, builder, checks, skippedProviderRows(names), models,
			registry, nil, gatewayAuth, root, controller, false, cleanupFailed, preflightFrozen,
		)
	}
	checks[5] = Check{Name: "containment", Status: checkStatusPass}

	defaults, defaultsErr := platformPathDefaults()
	rows := skippedProviderRows(names)
	resolved := make(map[core.ProviderName]ResolvedProvider, len(names))
	frozen := preflightFrozen
	if frozen == nil {
		frozen = make(map[core.ProviderName]*frozenLookup, len(names))
	}
	for index, name := range names {
		row, resolvedProvider, lookup, resolvable, probed := resolveProvider(
			ctx,
			name,
			cfg.Providers[string(name)],
			adapters[name],
			ranges[name],
			controller,
			dependencies.LookupEnv,
			dependencies.LookupExecutable,
			defaults,
			defaultsErr,
			frozen[name],
		)
		rows[index] = row
		if lookup != nil {
			frozen[name] = lookup
		}
		if resolvable {
			resolved[name] = resolvedProvider
		}
		if probed && controller.CleanupFailed() {
			cleanupFailed = true
			break
		}
	}
	candidateTransfer := !cleanupFailed
	return finishRun(
		ctx, dependencies, builder, checks, rows, models, registry, resolved,
		gatewayAuth, root, controller, candidateTransfer, cleanupFailed, frozen,
	)
}

func gatewayAuthFailureCheck() Check {
	return Check{
		Name: "gateway_auth", Status: checkStatusFail,
		Code: "gateway_key_missing", Message: "gateway authentication is unavailable",
	}
}

func snapshotGatewayAuth(
	cfg config.Config,
	names []core.ProviderName,
	dependencies Dependencies,
) (gatewaykey.Snapshot, map[core.ProviderName]*frozenLookup, bool) {
	if cfg.Server.APIKeyEnv != "" && cfg.Server.APIKeyFile != "" {
		return gatewaykey.Snapshot{}, nil, false
	}
	if cfg.Server.APIKeyFile != "" {
		return snapshotFileGatewayAuth(cfg, names, dependencies)
	}
	if cfg.Server.APIKeyEnv != "" {
		snapshot, err := gatewaykey.FromEnvironment(
			cfg.Server.APIKeyEnv,
			gatewaykey.LookupEnv(dependencies.LookupEnv),
		)
		if err != nil || !snapshot.Valid() || !snapshot.Enabled() {
			return gatewaykey.Snapshot{}, nil, false
		}
		return snapshot, nil, true
	}
	return gatewaykey.Disabled(), nil, true
}

func snapshotFileGatewayAuth(
	cfg config.Config,
	names []core.ProviderName,
	dependencies Dependencies,
) (gatewaykey.Snapshot, map[core.ProviderName]*frozenLookup, bool) {
	if dependencies.ConfigIdentity == nil ||
		!dependencies.ConfigIdentity.Mode().IsRegular() ||
		dependencies.LoadGatewayKey == nil {
		return gatewaykey.Snapshot{}, nil, false
	}
	evidence := []fs.FileInfo{dependencies.ConfigIdentity}
	credentialEvidence := make([]fs.FileInfo, 0, len(names))
	frozen := make(map[core.ProviderName]*frozenLookup, len(names))
	for _, name := range names {
		configured, present := cfg.Providers[string(name)]
		if !present {
			clearFrozenLookups(frozen)
			return gatewaykey.Snapshot{}, nil, false
		}
		executable, disposition := validateExecutablePath(configured.Executable)
		if disposition != pathSafe {
			clearFrozenLookups(frozen)
			return gatewaykey.Snapshot{}, nil, false
		}
		command, valid := resolveProviderCommand(
			executable,
			slices.Clone(configured.PrefixArgs),
			dependencies.LookupExecutable,
		)
		if !valid || command.Executable.Info == nil {
			clearFrozenLookups(frozen)
			return gatewaykey.Snapshot{}, nil, false
		}
		evidence = appendDistinctIdentity(evidence, command.Executable.Info)
		if command.Entrypoint != nil {
			if command.Entrypoint.Info == nil {
				clearFrozenLookups(frozen)
				return gatewaykey.Snapshot{}, nil, false
			}
			evidence = appendDistinctIdentity(evidence, command.Entrypoint.Info)
		}

		lookup := freezeConfiguredEnvironment(configured.CredentialEnv, dependencies.LookupEnv)
		frozen[name] = lookup
		if name != core.ProviderGemini {
			continue
		}
		credential, present := lookup.values["GOOGLE_APPLICATION_CREDENTIALS"]
		if !present || !credential.present {
			continue
		}
		validated, disposition := validateCredentialPath(credential.value)
		if disposition != pathSafe || validated.Info == nil {
			continue
		}
		credential.value = validated.Resolved
		lookup.values["GOOGLE_APPLICATION_CREDENTIALS"] = credential
		credentialEvidence = appendDistinctIdentity(credentialEvidence, validated.Info)
	}
	for _, identity := range credentialEvidence {
		evidence = appendDistinctIdentity(evidence, identity)
	}
	snapshot, err := dependencies.LoadGatewayKey(cfg.Server.APIKeyFile, evidence)
	if err != nil || !snapshot.Valid() || !snapshot.Enabled() {
		clearFrozenLookups(frozen)
		return gatewaykey.Snapshot{}, nil, false
	}
	return snapshot, frozen, true
}

func freezeConfiguredEnvironment(
	names []string,
	lookup provider.LookupEnv,
) *frozenLookup {
	selected := slices.Clone(names)
	slices.Sort(selected)
	frozen := &frozenLookup{values: make(map[string]frozenValue, len(selected))}
	for _, name := range selected {
		value, present := lookup(name)
		if !present || value == "" || containsNUL(value) {
			frozen.values[name] = frozenValue{}
			continue
		}
		frozen.values[name] = frozenValue{value: value, present: true}
	}
	return frozen
}

func appendDistinctIdentity(values []fs.FileInfo, candidate fs.FileInfo) []fs.FileInfo {
	if candidate == nil {
		return values
	}
	for _, existing := range values {
		if existing != nil && os.SameFile(existing, candidate) {
			return values
		}
	}
	return append(values, candidate)
}

func doctorProbeLimits() process.Limits {
	return process.Limits{
		Execution:   5 * time.Second,
		TermGrace:   time.Second,
		Cleanup:     time.Second,
		StdoutBytes: 64 << 10,
		StderrBytes: 64 << 10,
	}
}

func containmentFailureCheck() Check {
	return Check{
		Name: "containment", Status: checkStatusFail,
		Code: "containment_failed", Message: "process containment self-test failed",
	}
}

func runtimeCleanupFailureCheck(name string) Check {
	return Check{
		Name: name, Status: checkStatusFail,
		Code: "runtime_cleanup_failed", Message: "runtime cleanup failed",
	}
}

func finishRun(
	ctx context.Context,
	dependencies Dependencies,
	builder *reportBuilder,
	checks []Check,
	rows []Provider,
	models []string,
	registry *core.Registry,
	resolved map[core.ProviderName]ResolvedProvider,
	gatewayAuth gatewaykey.Snapshot,
	root *process.Root,
	controller ProbeController,
	candidateTransfer bool,
	cleanupFailed bool,
	frozen map[core.ProviderName]*frozenLookup,
) (Diagnosis, error) {
	rootAcquired := root != nil
	if controller != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		shutdownErr := controller.Shutdown(cleanupCtx)
		cancel()
		if shutdownErr != nil {
			cleanupFailed = true
		}
		if errors.Is(shutdownErr, context.Canceled) ||
			errors.Is(shutdownErr, context.DeadlineExceeded) {
			if retryErr := controller.Shutdown(context.Background()); retryErr != nil {
				cleanupFailed = true
			}
		}
		if controller.CleanupFailed() {
			cleanupFailed = true
		}
	}
	runErr := ctx.Err()
	if runErr != nil {
		candidateTransfer = false
	}
	if cleanupFailed || !candidateTransfer {
		candidateTransfer = false
		if root != nil {
			if err := dependencies.CloseRoot(root); err != nil {
				cleanupFailed = true
			}
			root = nil
		}
	}
	if rootAcquired {
		if cleanupFailed {
			checks[6] = runtimeCleanupFailureCheck("probe_cleanup")
		} else {
			checks[6] = Check{Name: "probe_cleanup", Status: checkStatusPass}
		}
	}

	var transferred map[core.ProviderName]ResolvedProvider
	var transferredFrozen map[core.ProviderName]*frozenLookup
	if candidateTransfer && root != nil {
		transferred, transferredFrozen = transferResolvedProviders(resolved, frozen)
	} else {
		clearResolvedProviders(resolved)
		clearFrozenLookups(frozen)
		resolved = nil
	}
	diagnosis, finishErr := finishDiagnosis(
		builder, checks, rows, models, registry, transferred, gatewayAuth, root,
	)
	if finishErr != nil {
		if root != nil {
			_ = dependencies.CloseRoot(root)
		}
		clearResolvedProviders(transferred)
		clearFrozenLookups(transferredFrozen)
		clearFrozenLookups(frozen)
		return Diagnosis{}, ErrDiagnosis
	}
	clearResolvedProviders(resolved)
	clearFrozenLookups(frozen)
	for name := range transferredFrozen {
		delete(transferredFrozen, name)
	}
	if runErr != nil {
		return diagnosis, runErr
	}
	return diagnosis, nil
}

type frozenValue struct {
	value   string
	present bool
}

type frozenLookup struct {
	values map[string]frozenValue
}

func (f *frozenLookup) lookup(name string) (string, bool) {
	if f == nil {
		return "", false
	}
	value, present := f.values[name]
	if !present || !value.present {
		return "", false
	}
	return value.value, true
}

func (f *frozenLookup) clone() *frozenLookup {
	if f == nil {
		return nil
	}
	cloned := &frozenLookup{values: make(map[string]frozenValue, len(f.values))}
	for name, value := range f.values {
		cloned.values[name] = value
	}
	return cloned
}

func (f *frozenLookup) clear() {
	if f == nil {
		return
	}
	for name, value := range f.values {
		value.value = ""
		value.present = false
		f.values[name] = value
		delete(f.values, name)
	}
	f.values = nil
}

func resolveProvider(
	ctx context.Context,
	name core.ProviderName,
	configured config.Provider,
	adapter provider.Adapter,
	interval provider.Range,
	controller ProbeController,
	ambient provider.LookupEnv,
	lookupExecutable func(string) (string, error),
	defaults platformDefaults,
	defaultsErr error,
	seeded *frozenLookup,
) (Provider, ResolvedProvider, *frozenLookup, bool, bool) {
	executable, executableDisposition := validateExecutablePath(configured.Executable)
	executableMissing := executableDisposition == pathMissing
	executableUnsafe := executableDisposition == pathUnsafe

	command := nativeProviderCommand(executable)
	if executableDisposition == pathSafe {
		var valid bool
		command, valid = resolveProviderCommand(
			executable,
			slices.Clone(configured.PrefixArgs),
			lookupExecutable,
		)
		if !valid {
			executableUnsafe = true
		}
	}
	safePath := ""
	if executableDisposition == pathSafe && !executableUnsafe && defaultsErr == nil {
		var err error
		safePath, err = buildSafePath(
			command.Executable,
			command.Entrypoint,
			defaults,
		)
		if err != nil {
			executableUnsafe = true
		}
	} else if executableDisposition == pathSafe {
		executableUnsafe = true
	}

	configHome, configDisposition := validateConfigHomePath(configured.ConfigHome)
	configUnsafe := configDisposition != pathSafe
	baseSafe := !executableMissing && !executableUnsafe && !configUnsafe
	frozen := seeded
	credentialMissing := false
	credentialFileUnsafe := false
	credentialNames := slices.Clone(configured.CredentialEnv)
	slices.Sort(credentialNames)
	if baseSafe {
		if frozen == nil {
			frozen = freezeConfiguredEnvironment(credentialNames, ambient)
		}
		for _, environmentName := range credentialNames {
			value, present := frozen.values[environmentName]
			if !present || !value.present {
				credentialMissing = true
			}
		}
		if defaults.FrozenSystemRoot != "" {
			frozen.values["SystemRoot"] = frozenValue{
				value: defaults.FrozenSystemRoot, present: true,
			}
		}
		if name == core.ProviderGemini {
			if credential, present := frozen.values["GOOGLE_APPLICATION_CREDENTIALS"]; present && credential.present {
				validated, disposition := validateCredentialPath(credential.value)
				credentialFileUnsafe = disposition != pathSafe
				if disposition == pathSafe {
					credential.value = validated.Resolved
					frozen.values["GOOGLE_APPLICATION_CREDENTIALS"] = credential
				}
			}
		}
	}

	problem := ""
	switch {
	case executableMissing:
		problem = provider.ProblemExecutableMissing
	case executableUnsafe:
		problem = provider.ProblemExecutableUnsafe
	case configUnsafe:
		problem = provider.ProblemConfigHomeUnsafe
	case credentialMissing:
		problem = provider.ProblemCredentialMissing
	case credentialFileUnsafe:
		problem = provider.ProblemCredentialFileUnsafe
	}
	resolvable := baseSafe && !credentialFileUnsafe
	providerConfig := provider.ProviderConfig{}
	if resolvable {
		providerConfig = provider.ProviderConfig{
			Executable:    command.Executable.Resolved,
			PrefixArgs:    slices.Clone(command.PrefixArgs),
			ConfigHome:    configHome.Resolved,
			CredentialEnv: credentialNames,
			SafePath:      safePath,
			LookupEnv:     frozen.lookup,
		}
	}
	if problem != "" {
		auth := "unknown"
		if problem == provider.ProblemCredentialMissing {
			auth = "missing"
		}
		row := Provider{
			Name: name, Status: provider.HealthNotReady, Auth: auth,
			Problems: []string{problem},
		}
		resolvedProvider := ResolvedProvider{}
		if resolvable {
			resolvedProvider = ResolvedProvider{
				Config: providerConfig,
				Health: healthFromProviderRow(row),
			}
		}
		return row, resolvedProvider, frozen, resolvable, false
	}

	health := adapter.Probe(ctx, providerConfig.Clone(), controller)
	row, canonical := canonicalizeHealth(name, interval, health)
	return row, ResolvedProvider{Config: providerConfig, Health: canonical}, frozen, true, true
}

func canonicalizeHealth(
	name core.ProviderName,
	interval provider.Range,
	health provider.Health,
) (Provider, provider.Health) {
	fallback := malformedProviderRow(name)
	if health.Provider != name || !validHealthStatus(health.Status) {
		return fallback, healthFromProviderRow(fallback)
	}
	capabilities, ok := normalizedRecognized(
		health.Capabilities,
		func(value string) bool { return slices.Contains(readyCapabilities(name), value) },
	)
	if !ok {
		return fallback, healthFromProviderRow(fallback)
	}
	problems, ok := normalizedRecognized(
		health.Problems,
		func(value string) bool { return providerProblemAllowed(name, value) },
	)
	if !ok {
		return fallback, healthFromProviderRow(fallback)
	}
	row := Provider{
		Name: name, Status: health.Status, Version: health.Version, Auth: health.Auth,
		Capabilities: capabilities, Problems: problems,
	}
	expectedProblems := make([]string, 0, 3)
	versionReady, valid := validateProviderVersion(row.Version, interval, &expectedProblems)
	if !valid {
		return fallback, healthFromProviderRow(fallback)
	}
	capabilitiesReady, valid := validateProviderCapabilities(row, &expectedProblems)
	if !valid {
		return fallback, healthFromProviderRow(fallback)
	}
	authReady, authUnknown, valid := validateProviderAuth(row, &expectedProblems)
	if !valid {
		return fallback, healthFromProviderRow(fallback)
	}
	slices.Sort(expectedProblems)
	if !slices.Equal(row.Problems, expectedProblems) {
		return fallback, healthFromProviderRow(fallback)
	}
	expectedStatus := provider.HealthNotReady
	switch {
	case versionReady && capabilitiesReady && authReady:
		expectedStatus = provider.HealthReady
	case versionReady && capabilitiesReady && authUnknown && name != core.ProviderGemini:
		expectedStatus = provider.HealthUnknown
	}
	if row.Status != expectedStatus {
		return fallback, healthFromProviderRow(fallback)
	}
	return row, healthFromProviderRow(row)
}

func validHealthStatus(status provider.HealthStatus) bool {
	return status == provider.HealthReady || status == provider.HealthNotReady ||
		status == provider.HealthUnknown
}

func normalizedRecognized(values []string, recognized func(string) bool) ([]string, bool) {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !recognized(value) {
			return nil, false
		}
		unique[value] = struct{}{}
	}
	normalized := make([]string, 0, len(unique))
	for value := range unique {
		normalized = append(normalized, value)
	}
	slices.Sort(normalized)
	return normalized, true
}

func malformedProviderRow(name core.ProviderName) Provider {
	return Provider{
		Name: name, Status: provider.HealthNotReady, Auth: "unknown",
		Problems: []string{
			provider.ProblemAuthUnknown,
			provider.ProblemCapabilityMissing,
			provider.ProblemVersionUnreadable,
		},
	}
}

func healthFromProviderRow(row Provider) provider.Health {
	return provider.Health{
		Provider: row.Name, Status: row.Status, Version: row.Version, Auth: row.Auth,
		Capabilities: slices.Clone(row.Capabilities), Problems: slices.Clone(row.Problems),
	}
}

func transferResolvedProviders(
	resolved map[core.ProviderName]ResolvedProvider,
	frozen map[core.ProviderName]*frozenLookup,
) (map[core.ProviderName]ResolvedProvider, map[core.ProviderName]*frozenLookup) {
	if len(resolved) == 0 {
		return nil, nil
	}
	transferred := make(map[core.ProviderName]ResolvedProvider, len(resolved))
	transferredFrozen := make(map[core.ProviderName]*frozenLookup, len(resolved))
	for name, value := range resolved {
		value = value.Clone()
		if lookup := frozen[name].clone(); lookup != nil {
			value.Config.LookupEnv = lookup.lookup
			transferredFrozen[name] = lookup
		}
		transferred[name] = value
	}
	return transferred, transferredFrozen
}

func clearResolvedProviders(resolved map[core.ProviderName]ResolvedProvider) {
	for name, value := range resolved {
		value.Config.Executable = ""
		value.Config.ConfigHome = ""
		value.Config.SafePath = ""
		value.Config.LookupEnv = nil
		clear(value.Config.PrefixArgs)
		clear(value.Config.CredentialEnv)
		clear(value.Health.Capabilities)
		clear(value.Health.Problems)
		delete(resolved, name)
	}
}

func clearFrozenLookups(frozen map[core.ProviderName]*frozenLookup) {
	for name, lookup := range frozen {
		lookup.clear()
		delete(frozen, name)
	}
}

func cloneConfig(cfg config.Config) config.Config {
	providers := make(map[string]config.Provider, len(cfg.Providers))
	for name, providerConfig := range cfg.Providers {
		providerConfig.PrefixArgs = slices.Clone(providerConfig.PrefixArgs)
		providerConfig.CredentialEnv = slices.Clone(providerConfig.CredentialEnv)
		providers[name] = providerConfig
	}
	cfg.Providers = providers
	cfg.Models = slices.Clone(cfg.Models)
	return cfg
}

func snapshotAdapters(
	cfg config.Config,
	provided map[core.ProviderName]provider.Adapter,
) ([]core.ProviderName, map[core.ProviderName]provider.Adapter, map[core.ProviderName]provider.Range, bool) {
	if len(provided) != len(cfg.Providers) || len(provided) == 0 {
		return nil, nil, nil, false
	}
	names := make([]core.ProviderName, 0, len(cfg.Providers))
	for configured := range cfg.Providers {
		name := core.ProviderName(configured)
		if !knownReportProvider(name) {
			return nil, nil, nil, false
		}
		names = append(names, name)
	}
	slices.Sort(names)
	adapters := make(map[core.ProviderName]provider.Adapter, len(names))
	ranges := make(map[core.ProviderName]provider.Range, len(names))
	for _, name := range names {
		adapter, present := provided[name]
		if !present || nilInterface(adapter) {
			return nil, nil, nil, false
		}
		adapters[name] = adapter
	}
	retainedNames := make(map[core.ProviderName]core.ProviderName, len(names))
	for _, name := range names {
		retainedNames[name] = adapters[name].Name()
	}
	for _, name := range names {
		if retainedNames[name] != name {
			return nil, nil, nil, false
		}
	}
	for _, name := range names {
		ranges[name] = adapters[name].SupportedVersion()
	}
	for _, name := range names {
		interval := ranges[name]
		if !interval.Contains(interval.MinInclusive) {
			return nil, nil, nil, false
		}
	}
	for name := range provided {
		if _, configured := cfg.Providers[string(name)]; !configured {
			return nil, nil, nil, false
		}
	}
	return names, adapters, ranges, true
}

func nilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	kind := reflected.Kind()
	if kind != reflect.Chan && kind != reflect.Func && kind != reflect.Interface &&
		kind != reflect.Map && kind != reflect.Pointer && kind != reflect.Slice {
		return false
	}
	return reflected.IsNil()
}

func buildRegistry(models []config.Model) (*core.Registry, []string, error) {
	coreModels := make([]core.Model, len(models))
	for index, model := range models {
		coreModels[index] = core.Model{
			ID:            model.ID,
			Provider:      core.ProviderName(model.Provider),
			ProviderModel: model.ProviderModel,
			Created:       model.Created,
		}
	}
	registry, err := core.NewRegistry(coreModels)
	if err != nil {
		return nil, nil, err
	}
	canonical := registry.Models()
	aliases := make([]string, len(canonical))
	for index := range canonical {
		aliases[index] = canonical[index].ID
	}
	return registry, aliases, nil
}

func initialCoreChecks() []Check {
	return []Check{
		{Name: "listener", Status: checkStatusPass},
		{Name: "gateway_auth", Status: checkStatusPass},
		{Name: "scheduler", Status: checkStatusPass},
		{Name: "runtime_root", Status: checkStatusSkipped},
		{Name: "runtime_janitor", Status: checkStatusSkipped},
		{Name: "containment", Status: checkStatusSkipped},
		{Name: "probe_cleanup", Status: checkStatusSkipped},
	}
}

func validListener(value string) bool {
	host, port, err := net.SplitHostPort(value)
	if err != nil || !decimalASCII(port) {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return false
	}
	number, err := strconv.ParseUint(port, 10, 16)
	return err == nil && number != 0
}

func decimalASCII(value string) bool {
	if value == "" {
		return false
	}
	for index := range len(value) {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func validSchedulerConfig(providers map[string]config.Provider) bool {
	for _, providerConfig := range providers {
		if providerConfig.Concurrency <= 0 || providerConfig.Concurrency > 64 ||
			providerConfig.QueueSize <= 0 || providerConfig.QueueSize > 4096 ||
			providerConfig.QueueBytes <= 0 || providerConfig.QueueBytes > 1<<30 ||
			time.Duration(providerConfig.QueueTimeout) <= 0 ||
			time.Duration(providerConfig.QueueTimeout) > 24*time.Hour ||
			time.Duration(providerConfig.ExecutionTimeout) <= 0 ||
			time.Duration(providerConfig.ExecutionTimeout) > 24*time.Hour {
			return false
		}
	}
	return true
}

func skippedProviderRows(names []core.ProviderName) []Provider {
	rows := make([]Provider, len(names))
	for index, name := range names {
		rows[index] = Provider{Name: name, Status: provider.HealthUnknown, Auth: "unknown"}
	}
	return rows
}

func finishDiagnosis(
	builder *reportBuilder,
	checks []Check,
	rows []Provider,
	models []string,
	registry *core.Registry,
	resolved map[core.ProviderName]ResolvedProvider,
	gatewayAuth gatewaykey.Snapshot,
	root *process.Root,
) (Diagnosis, error) {
	if len(checks) < 2 ||
		(checks[1].Status == checkStatusPass && !gatewayAuth.Valid()) ||
		(checks[1].Status == checkStatusFail && gatewayAuth.Valid()) {
		return Diagnosis{}, ErrInvalidReport
	}
	if err := builder.setCore(checks); err != nil {
		return Diagnosis{}, err
	}
	if err := builder.setProviders(rows); err != nil {
		return Diagnosis{}, err
	}
	report, err := builder.complete(models)
	if err != nil {
		return Diagnosis{}, err
	}
	return Diagnosis{
		report: report, providers: resolved, registry: registry,
		gatewayAuth: gatewayAuth, RuntimeRoot: root,
	}, nil
}

func containsNUL(value string) bool {
	return strings.IndexByte(value, 0) >= 0
}

// Report is an immutable diagnostic view whose construction provenance remains
// private to this package.
type Report struct {
	core              []Check
	providers         []Provider
	models            []string
	expectedProviders []core.ProviderName
	expectedModels    []string
	expectedRanges    map[core.ProviderName]provider.Range
	constructed       bool
	phase             reportPhase
}

// ResolvedProvider retains a filtered provider configuration and health result.
type ResolvedProvider struct {
	Config provider.ProviderConfig
	Health provider.Health
}

// Diagnosis is the immutable output of gateway readiness diagnosis. RuntimeRoot
// is present only when all core checks pass and ownership transfers to the caller.
type Diagnosis struct {
	report      Report
	providers   map[core.ProviderName]ResolvedProvider
	registry    *core.Registry
	gatewayAuth gatewaykey.Snapshot
	RuntimeRoot *process.Root
}

type reportBuilder struct {
	report Report
}

func newReportBuilder(
	expectedProviders []core.ProviderName,
	expectedModels []string,
	expectedRanges map[core.ProviderName]provider.Range,
) *reportBuilder {
	providers := slices.Clone(expectedProviders)
	slices.Sort(providers)
	models := slices.Clone(expectedModels)
	slices.Sort(models)
	return &reportBuilder{report: Report{
		expectedProviders: providers,
		expectedModels:    models,
		expectedRanges:    cloneRanges(expectedRanges),
		constructed:       true,
		phase:             reportPhaseUnconstructed,
	}}
}

func (b *reportBuilder) setCore(checks []Check) error {
	if b == nil || !b.report.constructed || b.report.phase != reportPhaseUnconstructed {
		return ErrInvalidReport
	}
	b.report.core = slices.Clone(checks)
	b.report.phase = reportPhaseCore
	return nil
}

func (b *reportBuilder) setProviders(providers []Provider) error {
	if b == nil || !b.report.constructed || b.report.phase != reportPhaseCore {
		return ErrInvalidReport
	}
	b.report.providers = cloneProviders(providers)
	sort.Slice(b.report.providers, func(left, right int) bool {
		return b.report.providers[left].Name < b.report.providers[right].Name
	})
	b.report.phase = reportPhaseProviders
	return nil
}

func (b *reportBuilder) complete(models []string) (Report, error) {
	if b == nil || !b.report.constructed || b.report.phase != reportPhaseProviders {
		return Report{}, ErrInvalidReport
	}
	b.report.models = slices.Clone(models)
	slices.Sort(b.report.models)
	b.report.phase = reportPhaseComplete
	if err := validateReport(b.report); err != nil {
		return Report{}, err
	}
	return b.report.clone(), nil
}

// Core returns a defensive copy of the fixed-order gateway checks.
func (r Report) Core() []Check {
	return slices.Clone(r.core)
}

// Providers returns a defensive copy of the sorted provider rows.
func (r Report) Providers() []Provider {
	return cloneProviders(r.providers)
}

// Models returns a defensive copy of the sorted configured aliases.
func (r Report) Models() []string {
	return slices.Clone(r.models)
}

// CoreReady reports whether a valid complete report has only passing core checks.
func (r Report) CoreReady() bool {
	if validateReport(r) != nil {
		return false
	}
	for _, check := range r.core {
		if check.Status != "pass" {
			return false
		}
	}
	return true
}

// ReadyCount returns the number of ready providers in a valid complete report.
func (r Report) ReadyCount() int {
	if validateReport(r) != nil {
		return 0
	}
	ready := 0
	for _, result := range r.providers {
		if result.Status == provider.HealthReady {
			ready++
		}
	}
	return ready
}

// Clone returns a copy that owns every mutable config and health slice.
func (p ResolvedProvider) Clone() ResolvedProvider {
	p.Config = p.Config.Clone()
	p.Health = p.Health.Clone()
	return p
}

// Report returns a defensive copy including private construction provenance.
func (d Diagnosis) Report() Report {
	return d.report.clone()
}

// ResolvedProviders returns a defensive copy of every resolved provider value.
func (d Diagnosis) ResolvedProviders() map[core.ProviderName]ResolvedProvider {
	if d.providers == nil {
		return nil
	}
	providers := make(map[core.ProviderName]ResolvedProvider, len(d.providers))
	for name, resolved := range d.providers {
		providers[name] = resolved.Clone()
	}
	return providers
}

// Registry returns the canonical immutable model registry.
func (d Diagnosis) Registry() *core.Registry {
	return d.registry
}

// GatewayAuth returns the immutable Gateway authentication snapshot captured
// before runtime or provider probes.
func (d Diagnosis) GatewayAuth() gatewaykey.Snapshot {
	return d.gatewayAuth
}

func (r Report) clone() Report {
	r.core = slices.Clone(r.core)
	r.providers = cloneProviders(r.providers)
	r.models = slices.Clone(r.models)
	r.expectedProviders = slices.Clone(r.expectedProviders)
	r.expectedModels = slices.Clone(r.expectedModels)
	r.expectedRanges = cloneRanges(r.expectedRanges)
	return r
}

func cloneProviders(providers []Provider) []Provider {
	cloned := slices.Clone(providers)
	for index := range cloned {
		cloned[index].Capabilities = slices.Clone(cloned[index].Capabilities)
		cloned[index].Problems = slices.Clone(cloned[index].Problems)
	}
	return cloned
}

func cloneRanges(
	ranges map[core.ProviderName]provider.Range,
) map[core.ProviderName]provider.Range {
	if ranges == nil {
		return nil
	}
	cloned := make(map[core.ProviderName]provider.Range, len(ranges))
	for name, interval := range ranges {
		cloned[name] = interval
	}
	return cloned
}
