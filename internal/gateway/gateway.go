// Package gateway owns model routing and one-attempt provider orchestration.
package gateway

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
	providerscheduler "github.com/krkarma777/ai-cli-gateway/internal/scheduler"
	"github.com/krkarma777/ai-cli-gateway/internal/schema"
)

const problemRuntimeCleanup = "runtime_cleanup_failed"

var (
	errInvalidProviderRuntime = errors.New("gateway: invalid provider runtime")
	errInvalidConfiguration   = errors.New("gateway: invalid configuration")
	errResponseWork           = errors.New("gateway: response work failed")
	errShutdown               = errors.New("gateway: shutdown failed")
)

// Scheduler is the provider-local bounded admission contract.
type Scheduler interface {
	Do(context.Context, int64, func(context.Context) error) error
	Stats() providerscheduler.Stats
	Shutdown(context.Context) error
}

// Supervisor owns one request runtime and provider process tree.
type Supervisor interface {
	Prepare(string) (process.Runtime, error)
	Discard(context.Context, process.Runtime) error
	Execute(context.Context, process.Runtime, process.CommandSpec) (process.Result, error)
}

// ProviderRuntime binds one validated adapter to its provider-local runtime.
// Gateway.New snapshots these exported assembly fields before serving.
type ProviderRuntime struct {
	Adapter    provider.Adapter
	Config     provider.ProviderConfig
	Scheduler  Scheduler
	Supervisor Supervisor

	retainedName  core.ProviderName
	retainedRange provider.Range
	mu            sync.RWMutex
	health        provider.Health
}

// Config contains the final-output and portable-schema limits used by Gateway.
type Config struct {
	SchemaLimits schema.Limits
	FinalBytes   int
}

// Dependencies contains side-effect-free runtime seams supplied by the app.
type Dependencies struct {
	NewRuntimeID func() (string, error)
	Now          func() time.Time
}

// Gateway routes immutable requests to exactly one configured provider.
type Gateway struct {
	registry  *core.Registry
	models    []core.Model
	providers map[core.ProviderName]*ProviderRuntime
	config    Config
	closing   atomic.Bool
	id        func() (string, error)
	now       func() time.Time
}

// NewProviderRuntime validates and owns one provider runtime assembly.
func NewProviderRuntime(
	adapter provider.Adapter,
	cfg provider.ProviderConfig,
	scheduled Scheduler,
	supervisor Supervisor,
	health provider.Health,
) (*ProviderRuntime, error) {
	if nilLike(adapter) {
		return nil, errInvalidProviderRuntime
	}
	name := adapter.Name()
	interval := adapter.SupportedVersion()
	if !knownProvider(name) || !validVersionRange(interval) ||
		health.Provider != name || !canonicalInitialHealth(health, interval) {
		return nil, errInvalidProviderRuntime
	}

	configKind := classifyProviderConfig(cfg)
	ready := health.Status == provider.HealthReady
	if ready {
		if configKind != providerConfigResolved ||
			nilLike(scheduled) || nilLike(supervisor) ||
			!comparableInterface(scheduled) {
			return nil, errInvalidProviderRuntime
		}
	} else {
		if configKind == providerConfigInvalid || !nilLike(scheduled) || !nilLike(supervisor) {
			return nil, errInvalidProviderRuntime
		}
	}

	return &ProviderRuntime{
		Adapter:       adapter,
		Config:        cfg.Clone(),
		Scheduler:     scheduled,
		Supervisor:    supervisor,
		retainedName:  name,
		retainedRange: interval,
		health:        health.Clone(),
	}, nil
}

// New validates and snapshots the complete immutable routing graph.
func New(
	registry *core.Registry,
	providers map[core.ProviderName]*ProviderRuntime,
	cfg Config,
	deps Dependencies,
) (*Gateway, error) {
	if registry == nil || len(registry.Models()) == 0 ||
		deps.NewRuntimeID == nil || deps.Now == nil ||
		!validateGatewayConfig(cfg) {
		return nil, errInvalidConfiguration
	}

	ownedProviders := make(map[core.ProviderName]*ProviderRuntime, len(providers))
	for name, runtime := range providers {
		if !knownProvider(name) || runtime == nil {
			return nil, errInvalidConfiguration
		}
		owned, err := snapshotProviderRuntime(runtime, name)
		if err != nil {
			return nil, errInvalidConfiguration
		}
		ownedProviders[name] = owned
	}
	models := registry.Models()
	for _, model := range models {
		if _, ok := ownedProviders[model.Provider]; !ok {
			return nil, errInvalidConfiguration
		}
	}

	return &Gateway{
		registry: registry, models: slices.Clone(models), providers: ownedProviders,
		config: cfg, id: deps.NewRuntimeID, now: deps.Now,
	}, nil
}

// Models returns the immutable public alias list sorted by ID.
func (g *Gateway) Models() []core.Model {
	if g == nil {
		return nil
	}
	return slices.Clone(g.models)
}

// Health returns sorted, defensive provider-local readiness snapshots.
func (g *Gateway) Health() []provider.Health {
	if g == nil {
		return nil
	}
	health := make([]provider.Health, 0, len(g.providers))
	for _, runtime := range g.providers {
		health = append(health, runtime.healthSnapshot())
	}
	sort.Slice(health, func(i, j int) bool { return health[i].Provider < health[j].Provider })
	return health
}

// Respond routes and executes one request without retry or provider fallback.
func (g *Gateway) Respond(ctx context.Context, req core.Request) (core.Result, error) {
	if g == nil || ctx == nil {
		return core.Result{}, internalError()
	}
	if g.closing.Load() {
		return core.Result{}, core.Error(core.CodeServiceShuttingDown, nil)
	}

	request := cloneRequest(req)
	model, ok := g.registry.Resolve(request.ModelAlias)
	if !ok {
		return core.Result{}, core.Error(core.CodeModelNotFound, nil)
	}
	runtime, ok := g.providers[model.Provider]
	if !ok || runtime == nil {
		return core.Result{}, internalError()
	}

	var compiled *schema.Compiled
	switch request.Format.Type {
	case core.FormatText:
	case core.FormatJSONSchema:
		var err error
		compiled, err = schema.Compile(request.Format, g.config.SchemaLimits)
		if err != nil {
			return core.Result{}, core.Error(core.CodeInvalidJSONSchema, nil)
		}
	default:
		return core.Result{}, internalError()
	}

	initialHealth := runtime.healthSnapshot()
	if initialHealth.Status != provider.HealthReady {
		return core.Result{}, core.Error(core.CodeProviderNotReady, nil)
	}
	if nilLike(runtime.Scheduler) || nilLike(runtime.Supervisor) {
		return core.Result{}, internalError()
	}
	stats := runtime.Scheduler.Stats()
	if !validSchedulerStats(stats) {
		return core.Result{}, internalError()
	}
	queueStarted := g.now()
	attempt := responseAttempt{meta: core.ResultMeta{
		Provider: model.Provider, QueueDepth: stats.Queued, RunningCount: stats.Running,
		ProviderVersion: initialHealth.Version,
	}}

	scheduleErr := runtime.Scheduler.Do(ctx, request.Weight(), func(runCtx context.Context) error {
		attempt.admitted = true
		executionStarted := g.now()
		if executionStarted.Before(queueStarted) {
			attempt.invariant = true
			return errResponseWork
		}
		attempt.meta.QueueDuration = executionStarted.Sub(queueStarted)
		defer func() {
			executionEnded := g.now()
			if executionEnded.Before(executionStarted) {
				attempt.invariant = true
				return
			}
			attempt.meta.ExecutionTime = executionEnded.Sub(executionStarted)
		}()

		if runtime.healthSnapshot().Status != provider.HealthReady {
			attempt.cause = core.Error(core.CodeProviderNotReady, nil)
			return errResponseWork
		}
		id, err := g.id()
		if err != nil || !validRuntimeID(id) {
			attempt.invariant = true
			return errResponseWork
		}
		requestRuntime, err := runtime.Supervisor.Prepare(id)
		if err != nil {
			runtime.degrade(problemRuntimeCleanup)
			attempt.cause = core.Error(core.CodeProcessCleanupFailed, nil)
			attempt.meta.ExitCategory = "cleanup_failed"
			attempt.meta.StopReason = "cleanup_failed"
			attempt.meta.StopAction = "none"
			return errResponseWork
		}

		leaseOwned := true
		defer func() {
			if !leaseOwned {
				return
			}
			if discardErr := runtime.Supervisor.Discard(context.Background(), requestRuntime); discardErr != nil {
				runtime.degrade(problemRuntimeCleanup)
				attempt.invariant = false
				attempt.cause = core.Error(core.CodeProcessCleanupFailed, nil)
				attempt.meta.ExitCategory = "cleanup_failed"
				attempt.meta.StopReason = "cleanup_failed"
				attempt.meta.StopAction = "none"
			}
		}()

		spec, err := runtime.Adapter.Build(
			cloneRequest(request), model, runtime.Config.Clone(), requestRuntime,
		)
		if err != nil {
			attempt.invariant = true
			return errResponseWork
		}
		leaseOwned = false
		processResult, executeErr := runtime.Supervisor.Execute(runCtx, requestRuntime, spec)
		if !applyProcessMetadata(&attempt.meta, processResult, executeErr) {
			attempt.invariant = true
			return errResponseWork
		}
		if executeErr != nil {
			problem, valid, cause := classifyExecutionError(ctx, g.closing.Load(), executeErr)
			if !valid {
				attempt.invariant = true
				return errResponseWork
			}
			if problem != "" {
				runtime.degrade(problem)
			}
			normalizeGatewayShutdownStop(&attempt.meta, true, cause)
			attempt.cause = cause
			return errResponseWork
		}

		final, parseErr := runtime.Adapter.Parse(cloneRequest(request), processResult)
		if parseErr != nil {
			valid, cause := classifyProviderError(parseErr)
			if !valid {
				attempt.invariant = true
				return errResponseWork
			}
			attempt.cause = cause
			return errResponseWork
		}
		if processResult.ExitCode != 0 {
			attempt.cause = core.Error(core.CodeProviderFailed, nil)
			return errResponseWork
		}
		if len(final) > g.config.FinalBytes {
			attempt.cause = core.Error(core.CodeOutputLimitExceeded, nil)
			return errResponseWork
		}
		if final == "" || !utf8.ValidString(final) {
			attempt.cause = core.Error(core.CodeProviderProtocolError, nil)
			return errResponseWork
		}
		if compiled != nil {
			validated, validateErr := compiled.Validate([]byte(final))
			if validateErr != nil {
				attempt.cause = core.Error(core.CodeStructuredOutputInvalid, nil)
				return errResponseWork
			}
			final = validated
		}
		attempt.result = core.Result{Text: final}
		attempt.success = true
		return nil
	})

	if attempt.invariant {
		return core.Result{}, internalError()
	}
	if scheduleErr == nil {
		if !attempt.success || attempt.cause != nil {
			return core.Result{}, internalError()
		}
		attempt.result.Meta = attempt.meta
		if !validResultMetadata(attempt.result.Meta) {
			return core.Result{}, internalError()
		}
		return attempt.result, nil
	}

	if known, cause := classifySchedulerError(ctx, g.closing.Load(), scheduleErr); known {
		meta, valid := schedulerFailureMetadata(g, attempt, queueStarted)
		if !valid {
			return core.Result{}, internalError()
		}
		normalizeGatewayShutdownStop(&meta, attempt.admitted, cause)
		return core.Result{}, outcomeOrInternal(cause, meta)
	}
	// A cleanup guard can establish a stronger safe failure while a scheduler
	// contains an adapter panic and returns its own opaque sentinel.
	if attempt.cause != nil && errors.As(attempt.cause, new(*core.APIError)) {
		if publicErrorCode(attempt.cause) == core.CodeProcessCleanupFailed {
			return core.Result{}, outcomeOrInternal(attempt.cause, attempt.meta)
		}
	}
	// Exact identity rejects dependency wrappers that could obscure scheduler behavior.
	//nolint:errorlint
	if scheduleErr != errResponseWork || attempt.cause == nil {
		return core.Result{}, internalError()
	}
	return core.Result{}, outcomeOrInternal(attempt.cause, attempt.meta)
}

// Shutdown rejects new work and concurrently drains each distinct scheduler.
// Supervisor and shared-root shutdown remain application-owned.
func (g *Gateway) Shutdown(ctx context.Context) error {
	if g == nil || ctx == nil {
		return errShutdown
	}
	g.closing.Store(true)
	distinct := make(map[Scheduler]struct{}, len(g.providers))
	for _, runtime := range g.providers {
		if runtime == nil || nilLike(runtime.Scheduler) {
			continue
		}
		if !comparableInterface(runtime.Scheduler) {
			return errShutdown
		}
		distinct[runtime.Scheduler] = struct{}{}
	}
	if len(distinct) == 0 {
		return nil
	}
	results := make(chan error, len(distinct))
	for scheduled := range distinct {
		go func(value Scheduler) { results <- value.Shutdown(ctx) }(scheduled)
	}
	for range len(distinct) {
		select {
		case err := <-results:
			if err != nil {
				return errShutdown
			}
		case <-ctx.Done():
			return errShutdown
		}
	}
	return nil
}

type responseAttempt struct {
	admitted  bool
	success   bool
	invariant bool
	cause     error
	meta      core.ResultMeta
	result    core.Result
}

type providerConfigKind uint8

const (
	providerConfigInvalid providerConfigKind = iota
	providerConfigZero
	providerConfigResolved
)

func snapshotProviderRuntime(source *ProviderRuntime, key core.ProviderName) (*ProviderRuntime, error) {
	if source == nil || !knownProvider(key) {
		return nil, errInvalidProviderRuntime
	}
	source.mu.RLock()
	health := source.health.Clone()
	retainedName := source.retainedName
	retainedRange := source.retainedRange
	source.mu.RUnlock()
	adapter := source.Adapter
	if nilLike(adapter) || adapter.Name() != retainedName || retainedName != key {
		return nil, errInvalidProviderRuntime
	}
	interval := adapter.SupportedVersion()
	if interval != retainedRange || !validVersionRange(interval) ||
		health.Provider != key || !canonicalInitialHealth(health, interval) {
		return nil, errInvalidProviderRuntime
	}
	configKind := classifyProviderConfig(source.Config)
	ready := health.Status == provider.HealthReady
	if ready {
		if configKind != providerConfigResolved || nilLike(source.Scheduler) ||
			nilLike(source.Supervisor) || !comparableInterface(source.Scheduler) {
			return nil, errInvalidProviderRuntime
		}
	} else if configKind == providerConfigInvalid ||
		!nilLike(source.Scheduler) || !nilLike(source.Supervisor) {
		return nil, errInvalidProviderRuntime
	}
	return &ProviderRuntime{
		Adapter: adapter, Config: source.Config.Clone(), Scheduler: source.Scheduler,
		Supervisor: source.Supervisor, retainedName: key, retainedRange: interval,
		health: health,
	}, nil
}

func (r *ProviderRuntime) healthSnapshot() provider.Health {
	if r == nil {
		return provider.Health{}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.health.Clone()
}

func (r *ProviderRuntime) degrade(problem string) {
	if r == nil || !liveProblem(problem) {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.health.Status = provider.HealthNotReady
	r.health.Problems = append(slices.Clone(r.health.Problems), problem)
	slices.Sort(r.health.Problems)
	r.health.Problems = slices.Compact(r.health.Problems)
}

func cloneRequest(req core.Request) core.Request {
	if req.Instructions != nil {
		value := *req.Instructions
		req.Instructions = &value
	}
	if req.Format.Description != nil {
		value := *req.Format.Description
		req.Format.Description = &value
	}
	req.Format.Schema = slices.Clone(req.Format.Schema)
	return req
}

func validateGatewayConfig(cfg Config) bool {
	if cfg.FinalBytes <= 0 || cfg.SchemaLimits.OutputBytes != cfg.FinalBytes {
		return false
	}
	total := 0
	for _, value := range []int{
		cfg.SchemaLimits.SchemaBytes, cfg.SchemaLimits.MaxNodes,
		cfg.SchemaLimits.MaxDepth, cfg.SchemaLimits.MaxProperties,
		cfg.SchemaLimits.MaxEnum, cfg.SchemaLimits.OutputBytes,
		cfg.SchemaLimits.OutputDepth, cfg.SchemaLimits.NumberBytes,
	} {
		if value <= 0 || total > math.MaxInt-value {
			return false
		}
		total += value
	}
	return true
}

func classifyProviderConfig(cfg provider.ProviderConfig) providerConfigKind {
	zero := cfg.Executable == "" && len(cfg.PrefixArgs) == 0 &&
		cfg.ConfigHome == "" && len(cfg.CredentialEnv) == 0 &&
		cfg.SafePath == "" && cfg.LookupEnv == nil
	if zero {
		return providerConfigZero
	}
	if cfg.Executable == "" || cfg.ConfigHome == "" || cfg.SafePath == "" ||
		cfg.LookupEnv == nil || unsafeString(cfg.Executable) ||
		unsafeString(cfg.ConfigHome) || unsafeString(cfg.SafePath) {
		return providerConfigInvalid
	}
	for _, arg := range cfg.PrefixArgs {
		if unsafeString(arg) {
			return providerConfigInvalid
		}
	}
	for index, name := range cfg.CredentialEnv {
		if !validEnvironmentName(name) || (index > 0 && cfg.CredentialEnv[index-1] >= name) {
			return providerConfigInvalid
		}
	}
	return providerConfigResolved
}

func unsafeString(value string) bool {
	return !utf8.ValidString(value) || strings.IndexByte(value, 0) >= 0
}

func validEnvironmentName(value string) bool {
	if value == "" || (!asciiUpper(value[0]) && value[0] != '_') {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !asciiUpper(value[index]) && !asciiDigit(value[index]) && value[index] != '_' {
			return false
		}
	}
	return true
}

func canonicalInitialHealth(health provider.Health, interval provider.Range) bool {
	if !knownProvider(health.Provider) ||
		(health.Status != provider.HealthReady && health.Status != provider.HealthNotReady && health.Status != provider.HealthUnknown) ||
		!core.IsCanonicalProviderVersion(health.Version) ||
		!strictlySortedUnique(health.Capabilities) || !strictlySortedUnique(health.Problems) ||
		!validAuth(health.Provider, health.Auth) {
		return false
	}
	for _, capability := range health.Capabilities {
		if !slices.Contains(readyCapabilities(health.Provider), capability) {
			return false
		}
	}
	for _, problem := range health.Problems {
		if !startupProblemAllowed(health.Provider, problem) {
			return false
		}
	}
	if canonicalPreprobeHealth(health) {
		return true
	}

	expectedProblems := make([]string, 0, 3)
	versionReady := false
	if health.Version == "" {
		expectedProblems = append(expectedProblems, provider.ProblemVersionUnreadable)
	} else {
		version, err := provider.ParseVersion(health.Version)
		if err != nil || version.String() != health.Version {
			return false
		}
		if interval.Contains(version) {
			versionReady = true
		} else {
			expectedProblems = append(expectedProblems, provider.ProblemVersionUnsupported)
		}
	}
	capabilitiesReady := slices.Equal(health.Capabilities, readyCapabilities(health.Provider))
	if !capabilitiesReady {
		if len(health.Capabilities) != 0 {
			return false
		}
		expectedProblems = append(expectedProblems, provider.ProblemCapabilityMissing)
	}
	authReady, authUnknown := canonicalAuth(health.Provider, health.Auth, &expectedProblems)
	slices.Sort(expectedProblems)
	if !slices.Equal(health.Problems, expectedProblems) {
		return false
	}
	wantStatus := provider.HealthNotReady
	if versionReady && capabilitiesReady && authReady {
		wantStatus = provider.HealthReady
	} else if versionReady && capabilitiesReady && authUnknown && health.Provider != core.ProviderGemini {
		wantStatus = provider.HealthUnknown
	}
	return health.Status == wantStatus
}

func canonicalPreprobeHealth(health provider.Health) bool {
	if health.Status != provider.HealthNotReady || health.Version != "" ||
		len(health.Capabilities) != 0 || len(health.Problems) != 1 {
		return false
	}
	problem := health.Problems[0]
	switch problem {
	case provider.ProblemExecutableMissing, provider.ProblemExecutableUnsafe,
		provider.ProblemConfigHomeUnsafe, provider.ProblemCredentialFileUnsafe:
		return health.Auth == "unknown"
	case provider.ProblemCredentialMissing:
		return health.Auth == "missing"
	default:
		return false
	}
}

func canonicalAuth(name core.ProviderName, auth string, problems *[]string) (ready, unknown bool) {
	switch name {
	case core.ProviderCodex, core.ProviderClaude:
		switch auth {
		case "authenticated":
			return true, false
		case "missing":
			*problems = append(*problems, provider.ProblemAuthMissing)
		case "unknown":
			*problems = append(*problems, provider.ProblemAuthUnknown)
			return false, true
		}
	case core.ProviderGemini:
		switch auth {
		case "configured":
			return true, false
		case "missing":
			*problems = append(*problems, provider.ProblemCredentialMissing)
		case "unknown":
			*problems = append(*problems, provider.ProblemAuthUnknown)
			return false, true
		}
	}
	return false, false
}

func validAuth(name core.ProviderName, auth string) bool {
	problems := []string{}
	ready, unknown := canonicalAuth(name, auth, &problems)
	return ready || unknown || auth == "missing"
}

func readyCapabilities(name core.ProviderName) []string {
	switch name {
	case core.ProviderCodex:
		return []string{"ephemeral", "feature_hardening", "never_approve", "read_only", "schema_file", "stdin_prompt"}
	case core.ProviderClaude:
		return []string{"empty_settings", "empty_tools", "json_envelope", "no_session_persistence", "safe_mode", "stdin_prompt"}
	case core.ProviderGemini:
		return []string{"disposable_home", "empty_core_tools", "extensions_disabled", "json_envelope", "stdin_prompt", "system_settings_isolated"}
	default:
		return nil
	}
}

func startupProblemAllowed(name core.ProviderName, problem string) bool {
	switch problem {
	case provider.ProblemExecutableMissing, provider.ProblemExecutableUnsafe,
		provider.ProblemVersionUnreadable, provider.ProblemVersionUnsupported,
		provider.ProblemCapabilityMissing, provider.ProblemConfigHomeUnsafe,
		provider.ProblemAuthUnknown:
		return true
	case provider.ProblemAuthMissing:
		return name == core.ProviderCodex || name == core.ProviderClaude
	case provider.ProblemCredentialMissing:
		return name == core.ProviderClaude || name == core.ProviderGemini
	case provider.ProblemCredentialFileUnsafe:
		return name == core.ProviderGemini
	default:
		return false
	}
}

func liveProblem(problem string) bool {
	switch problem {
	case provider.ProblemExecutableMissing, provider.ProblemExecutableUnsafe, problemRuntimeCleanup:
		return true
	default:
		return false
	}
}

func strictlySortedUnique(values []string) bool {
	for index, value := range values {
		if value == "" || unsafeString(value) || (index > 0 && values[index-1] >= value) {
			return false
		}
	}
	return true
}

func validVersionRange(interval provider.Range) bool {
	return compareVersion(interval.MinInclusive, interval.MaxExclusive) < 0
}

func compareVersion(left, right provider.Version) int {
	switch {
	case left.Major < right.Major:
		return -1
	case left.Major > right.Major:
		return 1
	case left.Minor < right.Minor:
		return -1
	case left.Minor > right.Minor:
		return 1
	case left.Patch < right.Patch:
		return -1
	case left.Patch > right.Patch:
		return 1
	default:
		return 0
	}
}

func knownProvider(name core.ProviderName) bool {
	return name == core.ProviderCodex || name == core.ProviderClaude || name == core.ProviderGemini
}

func nilLike(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	// All kinds that can be nil are enumerated; every other kind is non-nil.
	//nolint:exhaustive
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func comparableInterface(value any) bool {
	return value != nil && reflect.ValueOf(value).Comparable()
}

func validRuntimeID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for index := range len(id) {
		if !asciiDigit(id[index]) && !asciiLowerHexLetter(id[index]) {
			return false
		}
	}
	return true
}

func validSchedulerStats(stats providerscheduler.Stats) bool {
	return stats.Queued >= 0 && stats.QueuedBytes >= 0 && stats.Running >= 0
}

func applyProcessMetadata(meta *core.ResultMeta, result process.Result, runErr error) bool {
	if meta == nil || result.StdoutTotal < 0 || result.StderrTotal < 0 ||
		result.StdoutTotal < int64(len(result.Stdout)) || result.StderrTotal < int64(len(result.Stderr)) ||
		result.ExitCode < -1 {
		return false
	}
	meta.StdoutBytes = result.StdoutTotal
	meta.StderrBytes = result.StderrTotal
	switch result.StopReason {
	case process.StopReasonNormalExit:
		meta.StopReason = "completed"
	case process.StopReasonCallerCancellation:
		meta.StopReason = "client_canceled"
	case process.StopReasonSupervisorTimeout:
		meta.StopReason = "execution_timeout"
	case process.StopReasonOutputOverflow:
		meta.StopReason = "output_limit"
	case process.StopReasonCleanupFailure:
		meta.StopReason = "cleanup_failed"
	default:
		return false
	}
	switch result.StopAction {
	case process.StopActionNone:
		meta.StopAction = "none"
	case process.StopActionTERM:
		meta.StopAction = "term"
	case process.StopActionKILL:
		meta.StopAction = "kill"
	case process.StopActionTerminateJob:
		meta.StopAction = "terminate_job"
	default:
		return false
	}
	if runErr == nil {
		if result.ExitCode < 0 {
			return false
		}
		if result.ExitCode == 0 {
			meta.ExitCategory = "completed"
		} else {
			meta.ExitCategory = "nonzero_exit"
		}
		return true
	}
	var typed *process.RunError
	if !errors.As(runErr, &typed) || typed == nil {
		return false
	}
	switch typed.Kind {
	case process.ErrorCanceled:
		meta.ExitCategory = "canceled"
	case process.ErrorTimeout:
		meta.ExitCategory = "timeout"
	case process.ErrorOutputLimit:
		meta.ExitCategory = "output_limit"
	case process.ErrorStart:
		meta.ExitCategory = "start_failed"
	case process.ErrorCleanup:
		meta.ExitCategory = "cleanup_failed"
	default:
		return false
	}
	return true
}

func classifyExecutionError(
	requestContext context.Context,
	closing bool,
	runErr error,
) (healthProblem string, valid bool, cause error) {
	var typed *process.RunError
	if !errors.As(runErr, &typed) || typed == nil {
		return "", false, nil
	}
	switch typed.Kind {
	case process.ErrorCanceled:
		requestCause := context.Cause(requestContext)
		switch {
		case closing:
			return "", true, core.Error(core.CodeServiceShuttingDown, nil)
		// Exact identity prevents an arbitrary cancel-cause wrapper from
		// contributing text while preserving the two canonical context causes.
		//nolint:errorlint
		case requestCause == context.Canceled:
			return "", true, context.Canceled
		//nolint:errorlint // See the exact-identity rationale above.
		case requestCause == context.DeadlineExceeded:
			return "", true, context.DeadlineExceeded
		default:
			return "", false, nil
		}
	case process.ErrorTimeout:
		return "", true, core.Error(core.CodeProviderTimeout, nil)
	case process.ErrorOutputLimit:
		return "", true, core.Error(core.CodeOutputLimitExceeded, nil)
	case process.ErrorCleanup:
		return problemRuntimeCleanup, true, core.Error(core.CodeProcessCleanupFailed, nil)
	case process.ErrorStart:
		if errors.Is(runErr, process.ErrExecutableUnavailable) {
			switch {
			case errors.Is(runErr, fs.ErrNotExist):
				return provider.ProblemExecutableMissing, true, core.Error(core.CodeProviderNotReady, nil)
			case errors.Is(runErr, fs.ErrPermission):
				return provider.ProblemExecutableUnsafe, true, core.Error(core.CodeProviderNotReady, nil)
			}
		}
		return "", true, core.Error(core.CodeProviderFailed, nil)
	default:
		return "", false, nil
	}
}

func classifyProviderError(err error) (bool, error) {
	var typed *provider.ProviderError
	if !errors.As(err, &typed) || typed == nil {
		return false, nil
	}
	switch typed.Category() {
	case provider.ProviderErrorAuthRequired:
		return true, core.Error(core.CodeProviderAuthRequired, nil)
	case provider.ProviderErrorRateLimited:
		return true, core.Error(core.CodeProviderRateLimited, nil)
	case provider.ProviderErrorProtocol:
		return true, core.Error(core.CodeProviderProtocolError, nil)
	case provider.ProviderErrorFailed:
		return true, core.Error(core.CodeProviderFailed, nil)
	default:
		return false, nil
	}
}

func classifySchedulerError(ctx context.Context, closing bool, err error) (bool, error) {
	switch {
	case errors.Is(err, providerscheduler.ErrShuttingDown):
		return true, core.Error(core.CodeServiceShuttingDown, nil)
	case errors.Is(err, providerscheduler.ErrCanceled):
		if closing {
			return true, core.Error(core.CodeServiceShuttingDown, nil)
		}
		requestCause := context.Cause(ctx)
		// Exact identity rejects arbitrary cancel-cause wrappers.
		//nolint:errorlint
		if requestCause == context.Canceled {
			return true, context.Canceled
		}
		//nolint:errorlint // Exact canonical deadline identity is required.
		if requestCause == context.DeadlineExceeded {
			return true, context.DeadlineExceeded
		}
		return false, nil
	case errors.Is(err, providerscheduler.ErrQueueFull):
		return true, core.Error(core.CodeQueueFull, nil)
	case errors.Is(err, providerscheduler.ErrQueueTimeout):
		return true, core.Error(core.CodeQueueTimeout, nil)
	default:
		return false, nil
	}
}

func asciiUpper(value byte) bool { return value >= 'A' && value <= 'Z' }

func asciiDigit(value byte) bool { return value >= '0' && value <= '9' }

func asciiLowerHexLetter(value byte) bool { return value >= 'a' && value <= 'f' }

func schedulerFailureMetadata(
	g *Gateway,
	attempt responseAttempt,
	queueStarted time.Time,
) (core.ResultMeta, bool) {
	meta := attempt.meta
	if attempt.admitted {
		return meta, !attempt.invariant
	}
	ended := g.now()
	if ended.Before(queueStarted) {
		return core.ResultMeta{}, false
	}
	meta.QueueDuration = ended.Sub(queueStarted)
	return meta, true
}

func normalizeGatewayShutdownStop(meta *core.ResultMeta, admitted bool, cause error) {
	if meta != nil && admitted && publicErrorCode(cause) == core.CodeServiceShuttingDown {
		meta.StopReason = "gateway_shutdown"
	}
}

func outcomeOrInternal(cause error, meta core.ResultMeta) error {
	outcome, err := core.NewOutcomeError(cause, meta)
	if err != nil {
		return internalError()
	}
	return outcome
}

func validResultMetadata(meta core.ResultMeta) bool {
	_, err := core.NewOutcomeError(core.Error(core.CodeInternalError, nil), meta)
	return err == nil
}

func internalError() *core.APIError {
	return core.Error(core.CodeInternalError, nil)
}

func publicErrorCode(err error) string {
	var apiErr *core.APIError
	if errors.As(err, &apiErr) {
		return apiErr.CodeValue()
	}
	return ""
}
