package gateway

import (
	"context"
	"errors"
	"io/fs"
	"math"
	"reflect"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
	"github.com/krkarma777/ai-cli-gateway/internal/scheduler"
	"github.com/krkarma777/ai-cli-gateway/internal/schema"
)

type spyAdapter struct {
	name       core.ProviderName
	rangeValue *provider.Range
	build      func(core.Request, core.Model, provider.ProviderConfig, process.Runtime) (process.CommandSpec, error)
	parse      func(core.Request, process.Result) (string, error)
	nameCalls  atomic.Int32
	buildCalls atomic.Int32
	parseCalls atomic.Int32
}

func (a *spyAdapter) Name() core.ProviderName {
	a.nameCalls.Add(1)
	return a.name
}

func (a *spyAdapter) SupportedVersion() provider.Range {
	if a.rangeValue != nil {
		return *a.rangeValue
	}
	switch a.name {
	case core.ProviderCodex:
		return provider.Range{MinInclusive: provider.Version{Major: 0, Minor: 146, Patch: 0}, MaxExclusive: provider.Version{Major: 0, Minor: 147, Patch: 0}}
	case core.ProviderClaude:
		return provider.Range{MinInclusive: provider.Version{Major: 2, Minor: 1, Patch: 208}, MaxExclusive: provider.Version{Major: 2, Minor: 2, Patch: 0}}
	case core.ProviderGemini:
		return provider.Range{MinInclusive: provider.Version{Major: 0, Minor: 53, Patch: 0}, MaxExclusive: provider.Version{Major: 0, Minor: 54, Patch: 0}}
	default:
		return provider.Range{}
	}
}

func (*spyAdapter) Probe(context.Context, provider.ProviderConfig, provider.ProbeRunner) provider.Health {
	return provider.Health{}
}

func (a *spyAdapter) Build(
	req core.Request,
	model core.Model,
	cfg provider.ProviderConfig,
	runtime process.Runtime,
) (process.CommandSpec, error) {
	a.buildCalls.Add(1)
	if a.build != nil {
		return a.build(req, model, cfg, runtime)
	}
	return process.CommandSpec{Executable: cfg.Executable, Dir: runtime.Dir}, nil
}

func (a *spyAdapter) Parse(req core.Request, result process.Result) (string, error) {
	a.parseCalls.Add(1)
	if a.parse != nil {
		return a.parse(req, result)
	}
	return string(result.Stdout), nil
}

type spyScheduler struct {
	mu            sync.Mutex
	stats         scheduler.Stats
	do            func(context.Context, int64, func(context.Context) error) error
	shutdown      func(context.Context) error
	doCalls       int
	shutdownCalls int
	weights       []int64
}

func (s *spyScheduler) Do(
	ctx context.Context,
	weight int64,
	work func(context.Context) error,
) error {
	s.mu.Lock()
	s.doCalls++
	s.weights = append(s.weights, weight)
	fn := s.do
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, weight, work)
	}
	return work(ctx)
}

func (s *spyScheduler) Stats() scheduler.Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats
}

func (s *spyScheduler) Shutdown(ctx context.Context) error {
	s.mu.Lock()
	s.shutdownCalls++
	fn := s.shutdown
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx)
	}
	return nil
}

func (s *spyScheduler) counts() (int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.doCalls, s.shutdownCalls
}

type spySupervisor struct {
	mu           sync.Mutex
	prepare      func(string) (process.Runtime, error)
	discard      func(context.Context, process.Runtime) error
	execute      func(context.Context, process.Runtime, process.CommandSpec) (process.Result, error)
	prepareCalls int
	discardCalls int
	executeCalls int
	ids          []string
}

func (s *spySupervisor) Prepare(id string) (process.Runtime, error) {
	s.mu.Lock()
	s.prepareCalls++
	s.ids = append(s.ids, id)
	fn := s.prepare
	s.mu.Unlock()
	if fn != nil {
		return fn(id)
	}
	return process.Runtime{ID: id, Dir: "/runtime/" + id}, nil
}

func (s *spySupervisor) Discard(ctx context.Context, runtime process.Runtime) error {
	s.mu.Lock()
	s.discardCalls++
	fn := s.discard
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, runtime)
	}
	return nil
}

func (s *spySupervisor) Execute(
	ctx context.Context,
	runtime process.Runtime,
	spec process.CommandSpec,
) (process.Result, error) {
	s.mu.Lock()
	s.executeCalls++
	fn := s.execute
	s.mu.Unlock()
	if fn != nil {
		return fn(ctx, runtime, spec)
	}
	return successfulProcessResult("hello"), nil
}

func (s *spySupervisor) counts() (int, int, int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.prepareCalls, s.discardCalls, s.executeCalls
}

type nonComparableScheduler struct{ values []int }

func (nonComparableScheduler) Do(context.Context, int64, func(context.Context) error) error {
	return nil
}
func (nonComparableScheduler) Stats() scheduler.Stats         { return scheduler.Stats{} }
func (nonComparableScheduler) Shutdown(context.Context) error { return nil }

// deceptiveScheduler has a comparable outer type, but comparing a value whose
// interface field contains a slice panics.
type deceptiveScheduler struct{ payload any }

func (deceptiveScheduler) Do(context.Context, int64, func(context.Context) error) error {
	return nil
}
func (deceptiveScheduler) Stats() scheduler.Stats         { return scheduler.Stats{} }
func (deceptiveScheduler) Shutdown(context.Context) error { return nil }

type stepClock struct {
	mu     sync.Mutex
	values []time.Time
	index  int
}

func (c *stepClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.values) == 0 {
		return time.Unix(100, 0)
	}
	if c.index >= len(c.values) {
		return c.values[len(c.values)-1]
	}
	value := c.values[c.index]
	c.index++
	return value
}

func validProviderConfig() provider.ProviderConfig {
	return provider.ProviderConfig{
		Executable:    "/trusted/provider",
		PrefixArgs:    []string{"--mode", "text"},
		ConfigHome:    "/trusted/config",
		CredentialEnv: []string{"PROVIDER_TOKEN"},
		SafePath:      "/trusted/bin",
		LookupEnv:     func(string) (string, bool) { return "value", true },
	}
}

func validHealth(name core.ProviderName) provider.Health {
	version := map[core.ProviderName]string{
		core.ProviderCodex: "0.146.0", core.ProviderClaude: "2.1.208", core.ProviderGemini: "0.53.0",
	}[name]
	auth := "authenticated"
	if name == core.ProviderGemini {
		auth = "configured"
	}
	return provider.Health{
		Provider: name, Status: provider.HealthReady, Version: version, Auth: auth,
		Capabilities: readyCapabilitiesForTest(name),
	}
}

func readyCapabilitiesForTest(name core.ProviderName) []string {
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

func authMissingHealth(name core.ProviderName) provider.Health {
	health := validHealth(name)
	health.Status = provider.HealthNotReady
	health.Auth = "missing"
	if name == core.ProviderGemini {
		health.Problems = []string{provider.ProblemCredentialMissing}
	} else {
		health.Problems = []string{provider.ProblemAuthMissing}
	}
	return health
}

func authUnknownHealth(name core.ProviderName) provider.Health {
	health := validHealth(name)
	health.Status = provider.HealthUnknown
	health.Auth = "unknown"
	health.Problems = []string{provider.ProblemAuthUnknown}
	if name == core.ProviderGemini {
		health.Status = provider.HealthNotReady
	}
	return health
}

func validGatewayConfig(t *testing.T) Config {
	t.Helper()
	limits, err := schema.DefaultLimits(4096, 4096)
	if err != nil {
		t.Fatal(err)
	}
	return Config{SchemaLimits: limits, FinalBytes: 4096}
}

func validRegistry(t *testing.T, models ...core.Model) *core.Registry {
	t.Helper()
	if len(models) == 0 {
		models = []core.Model{{
			ID: "public-model", Provider: core.ProviderCodex, ProviderModel: "trusted-model", Created: 1,
		}}
	}
	registry, err := core.NewRegistry(models)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func validRuntime(t *testing.T, name core.ProviderName) (*ProviderRuntime, *spyAdapter, *spyScheduler, *spySupervisor) {
	t.Helper()
	adapter := &spyAdapter{name: name}
	scheduled := &spyScheduler{}
	supervisor := &spySupervisor{}
	runtime, err := NewProviderRuntime(adapter, validProviderConfig(), scheduled, supervisor, validHealth(name))
	if err != nil {
		t.Fatalf("NewProviderRuntime() error=%v", err)
	}
	return runtime, adapter, scheduled, supervisor
}

func validGateway(t *testing.T) (*Gateway, *spyAdapter, *spyScheduler, *spySupervisor) {
	t.Helper()
	runtime, adapter, scheduled, supervisor := validRuntime(t, core.ProviderCodex)
	gateway, err := New(
		validRegistry(t),
		map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: runtime},
		validGatewayConfig(t),
		Dependencies{
			NewRuntimeID: func() (string, error) { return "0123456789abcdef0123456789abcdef", nil },
			Now:          time.Now,
		},
	)
	if err != nil {
		t.Fatalf("New() error=%v", err)
	}
	return gateway, adapter, scheduled, supervisor
}

func successfulProcessResult(text string) process.Result {
	return process.Result{
		Stdout: []byte(text), StdoutTotal: int64(len(text)), ExitCode: 0,
		StopReason: process.StopReasonNormalExit, StopAction: process.StopActionNone,
	}
}

func requestFor(alias string) core.Request {
	return core.Request{ModelAlias: alias, Input: "hello", Format: core.OutputFormat{Type: core.FormatText}}
}

func apiCode(t *testing.T, err error) string {
	t.Helper()
	var apiErr *core.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("error %T does not contain *core.APIError: %v", err, err)
	}
	return apiErr.CodeValue()
}

func outcomeMeta(t *testing.T, err error) core.ResultMeta {
	t.Helper()
	var outcome *core.OutcomeError
	if !errors.As(err, &outcome) {
		t.Fatalf("error %T does not contain *core.OutcomeError: %v", err, err)
	}
	return outcome.ResultMetadata()
}

func TestNewProviderRuntimeValidatesAndOwnsInputs(t *testing.T) {
	cfg := validProviderConfig()
	health := validHealth(core.ProviderCodex)
	adapter := &spyAdapter{name: core.ProviderCodex}
	scheduled := &spyScheduler{}
	supervisor := &spySupervisor{}

	runtime, err := NewProviderRuntime(adapter, cfg, scheduled, supervisor, health)
	if err != nil {
		t.Fatalf("NewProviderRuntime() error=%v", err)
	}
	cfg.PrefixArgs[0] = "mutated"
	cfg.CredentialEnv[0] = "MUTATED_TOKEN"
	health.Capabilities[0] = "mutated"

	if runtime == nil || runtime.retainedName != core.ProviderCodex ||
		runtime.Config.PrefixArgs[0] != "--mode" ||
		runtime.Config.CredentialEnv[0] != "PROVIDER_TOKEN" ||
		runtime.health.Capabilities[0] != "ephemeral" || adapter.nameCalls.Load() != 1 {
		t.Fatalf("runtime did not retain validated owned values: %+v", runtime)
	}
}

func TestNewProviderRuntimeDependencyAndStateMatrix(t *testing.T) {
	ready := validHealth(core.ProviderCodex)
	notReady := authMissingHealth(core.ProviderCodex)
	unknown := authUnknownHealth(core.ProviderCodex)

	var nilAdapter *spyAdapter
	var nilScheduler *spyScheduler
	var nilSupervisor *spySupervisor
	tests := []struct {
		name       string
		adapter    provider.Adapter
		cfg        provider.ProviderConfig
		scheduler  Scheduler
		supervisor Supervisor
		health     provider.Health
		wantOK     bool
	}{
		{"ready", &spyAdapter{name: core.ProviderCodex}, validProviderConfig(), &spyScheduler{}, &spySupervisor{}, ready, true},
		{"not ready unresolved", &spyAdapter{name: core.ProviderCodex}, provider.ProviderConfig{}, nil, nil, notReady, true},
		{"unknown resolved", &spyAdapter{name: core.ProviderCodex}, validProviderConfig(), nil, nil, unknown, true},
		{"nil adapter", nil, validProviderConfig(), &spyScheduler{}, &spySupervisor{}, ready, false},
		{"typed nil adapter", nilAdapter, validProviderConfig(), &spyScheduler{}, &spySupervisor{}, ready, false},
		{"ready zero config", &spyAdapter{name: core.ProviderCodex}, provider.ProviderConfig{}, &spyScheduler{}, &spySupervisor{}, ready, false},
		{"ready nil scheduler", &spyAdapter{name: core.ProviderCodex}, validProviderConfig(), nil, &spySupervisor{}, ready, false},
		{"ready typed nil scheduler", &spyAdapter{name: core.ProviderCodex}, validProviderConfig(), nilScheduler, &spySupervisor{}, ready, false},
		{"ready nil supervisor", &spyAdapter{name: core.ProviderCodex}, validProviderConfig(), &spyScheduler{}, nil, ready, false},
		{"ready typed nil supervisor", &spyAdapter{name: core.ProviderCodex}, validProviderConfig(), &spyScheduler{}, nilSupervisor, ready, false},
		{"noncomparable scheduler", &spyAdapter{name: core.ProviderCodex}, validProviderConfig(), nonComparableScheduler{values: []int{1}}, &spySupervisor{}, ready, false},
		{"unknown status", &spyAdapter{name: core.ProviderCodex}, validProviderConfig(), nil, nil, provider.Health{Provider: core.ProviderCodex, Status: "other", Auth: "unknown"}, false},
		{"health name mismatch", &spyAdapter{name: core.ProviderCodex}, validProviderConfig(), nil, nil, provider.Health{Provider: core.ProviderClaude, Status: provider.HealthNotReady, Auth: "unknown"}, false},
		{"invalid version", &spyAdapter{name: core.ProviderCodex}, validProviderConfig(), nil, nil, provider.Health{Provider: core.ProviderCodex, Status: provider.HealthNotReady, Version: "v0.146.0", Auth: "unknown"}, false},
		{"partial config", &spyAdapter{name: core.ProviderCodex}, provider.ProviderConfig{Executable: "/only"}, nil, nil, notReady, false},
		{"unresolved with dependencies", &spyAdapter{name: core.ProviderCodex}, provider.ProviderConfig{}, &spyScheduler{}, &spySupervisor{}, notReady, false},
		{"resolved not ready with dependencies", &spyAdapter{name: core.ProviderCodex}, validProviderConfig(), &spyScheduler{}, &spySupervisor{}, notReady, false},
		{"one dependency", &spyAdapter{name: core.ProviderCodex}, validProviderConfig(), &spyScheduler{}, nil, notReady, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NewProviderRuntime(test.adapter, test.cfg, test.scheduler, test.supervisor, test.health)
			if test.wantOK && (err != nil || got == nil) {
				t.Fatalf("NewProviderRuntime()=(%v,%v), want value", got, err)
			}
			if !test.wantOK && (err == nil || got != nil) {
				t.Fatalf("NewProviderRuntime()=(%v,%v), want nil,error", got, err)
			}
		})
	}
}

func TestNewProviderRuntimeRequiresCanonicalFilteredHealth(t *testing.T) {
	valid := validHealth(core.ProviderCodex)
	mutations := []struct {
		name   string
		mutate func(*provider.Health)
	}{
		{"unknown provider", func(h *provider.Health) { h.Provider = "other" }},
		{"unknown status", func(h *provider.Health) { h.Status = "other" }},
		{"noncanonical version", func(h *provider.Health) { h.Version = "00.146.0" }},
		{"unsupported version", func(h *provider.Health) { h.Version = "0.147.0" }},
		{"unknown auth", func(h *provider.Health) { h.Auth = "account@example.test" }},
		{"partial capabilities", func(h *provider.Health) { h.Capabilities = []string{"stdin_prompt"} }},
		{"unsorted capabilities", func(h *provider.Health) { slices.Reverse(h.Capabilities) }},
		{"duplicate capabilities", func(h *provider.Health) {
			h.Capabilities = append(h.Capabilities, h.Capabilities[len(h.Capabilities)-1])
		}},
		{"unknown capability", func(h *provider.Health) { h.Capabilities[0] = "raw-health-secret" }},
		{"ready with problem", func(h *provider.Health) { h.Problems = []string{provider.ProblemAuthMissing} }},
		{"gateway live problem at construction", func(h *provider.Health) {
			h.Status = provider.HealthNotReady
			h.Problems = []string{"runtime_cleanup_failed"}
		}},
	}

	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			health := valid.Clone()
			test.mutate(&health)
			got, err := NewProviderRuntime(
				&spyAdapter{name: core.ProviderCodex}, validProviderConfig(), &spyScheduler{}, &spySupervisor{}, health,
			)
			if err == nil || got != nil {
				t.Fatalf("NewProviderRuntime()=(%v,%v), want nil,error", got, err)
			}
		})
	}

	t.Run("invalid adapter range", func(t *testing.T) {
		invalidRange := provider.Range{
			MinInclusive: provider.Version{Major: 1}, MaxExclusive: provider.Version{Major: 1},
		}
		got, err := NewProviderRuntime(
			&spyAdapter{name: core.ProviderCodex, rangeValue: &invalidRange},
			validProviderConfig(), &spyScheduler{}, &spySupervisor{}, valid,
		)
		if err == nil || got != nil {
			t.Fatalf("NewProviderRuntime()=(%v,%v), want nil,error", got, err)
		}
	})

	for _, test := range []struct {
		name   string
		health provider.Health
		cfg    provider.ProviderConfig
	}{
		{
			name: "unresolved executable preprobe",
			health: provider.Health{
				Provider: core.ProviderCodex, Status: provider.HealthNotReady, Auth: "unknown",
				Problems: []string{provider.ProblemExecutableMissing},
			},
		},
		{
			name: "resolved credential preprobe",
			health: provider.Health{
				Provider: core.ProviderClaude, Status: provider.HealthNotReady, Auth: "missing",
				Problems: []string{provider.ProblemCredentialMissing},
			},
			cfg: validProviderConfig(),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			name := test.health.Provider
			got, err := NewProviderRuntime(&spyAdapter{name: name}, test.cfg, nil, nil, test.health)
			if err != nil || got == nil {
				t.Fatalf("NewProviderRuntime()=(%v,%v), want value,nil", got, err)
			}
		})
	}
}

func TestNewValidatesCompleteOwnedGraph(t *testing.T) {
	runtime, _, _, _ := validRuntime(t, core.ProviderCodex)
	registry := validRegistry(t)
	cfg := validGatewayConfig(t)
	deps := Dependencies{NewRuntimeID: func() (string, error) { return strings.Repeat("a", 32), nil }, Now: time.Now}

	tests := []struct {
		name      string
		registry  *core.Registry
		providers map[core.ProviderName]*ProviderRuntime
		cfg       Config
		deps      Dependencies
	}{
		{"nil registry", nil, map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: runtime}, cfg, deps},
		{"empty registry", mustRegistry(t, nil), map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: runtime}, cfg, deps},
		{"nil runtime", registry, map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: nil}, cfg, deps},
		{"missing model provider", registry, map[core.ProviderName]*ProviderRuntime{}, cfg, deps},
		{"wrong key", registry, map[core.ProviderName]*ProviderRuntime{core.ProviderClaude: runtime}, cfg, deps},
		{"nil id", registry, map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: runtime}, cfg, Dependencies{Now: time.Now}},
		{"nil clock", registry, map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: runtime}, cfg, Dependencies{NewRuntimeID: deps.NewRuntimeID}},
		{"zero final", registry, map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: runtime}, mutateConfig(cfg, func(value *Config) { value.FinalBytes = 0 }), deps},
		{"output mismatch", registry, map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: runtime}, mutateConfig(cfg, func(value *Config) { value.SchemaLimits.OutputBytes++ }), deps},
		{"zero schema field", registry, map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: runtime}, mutateConfig(cfg, func(value *Config) { value.SchemaLimits.MaxDepth = 0 }), deps},
		{"overflow limits", registry, map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: runtime}, mutateConfig(cfg, func(value *Config) { value.SchemaLimits.SchemaBytes = math.MaxInt }), deps},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := New(test.registry, test.providers, test.cfg, test.deps)
			if err == nil || got != nil {
				t.Fatalf("New()=(%v,%v), want nil,error", got, err)
			}
		})
	}
}

func mustRegistry(t *testing.T, models []core.Model) *core.Registry {
	t.Helper()
	registry, err := core.NewRegistry(models)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func mutateConfig(original Config, mutate func(*Config)) Config {
	mutate(&original)
	return original
}

func TestNewAcceptsRuntimeWithoutAliasAndSnapshotsCallerFields(t *testing.T) {
	codexRuntime, codexAdapter, codexScheduler, codexSupervisor := validRuntime(t, core.ProviderCodex)
	claudeRuntime, _, _, _ := validRuntime(t, core.ProviderClaude)
	providers := map[core.ProviderName]*ProviderRuntime{
		core.ProviderCodex: codexRuntime, core.ProviderClaude: claudeRuntime,
	}
	gateway, err := New(validRegistry(t), providers, validGatewayConfig(t), Dependencies{
		NewRuntimeID: func() (string, error) { return strings.Repeat("b", 32), nil }, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}

	delete(providers, core.ProviderCodex)
	codexRuntime.Adapter = &spyAdapter{name: core.ProviderGemini}
	codexRuntime.Scheduler = nil
	codexRuntime.Supervisor = nil
	codexRuntime.Config.PrefixArgs[0] = "mutated"
	codexRuntime.mu.Lock()
	codexRuntime.health.Status = provider.HealthNotReady
	codexRuntime.health.Capabilities[0] = "mutated"
	codexRuntime.mu.Unlock()

	result, respondErr := gateway.Respond(context.Background(), requestFor("public-model"))
	if respondErr != nil || result.Text != "hello" {
		t.Fatalf("Respond()=(%+v,%v)", result, respondErr)
	}
	if codexAdapter.buildCalls.Load() != 1 {
		t.Fatalf("owned adapter build calls=%d, want 1", codexAdapter.buildCalls.Load())
	}
	if calls, _ := codexScheduler.counts(); calls != 1 {
		t.Fatalf("owned scheduler calls=%d, want 1", calls)
	}
	if prepares, _, executes := codexSupervisor.counts(); prepares != 1 || executes != 1 {
		t.Fatalf("owned supervisor calls=%d/%d", prepares, executes)
	}
}

func TestModelsAndHealthAreSortedDefensiveSnapshots(t *testing.T) {
	codexRuntime, _, _, _ := validRuntime(t, core.ProviderCodex)
	claudeRuntime, _, _, _ := validRuntime(t, core.ProviderClaude)
	registry := validRegistry(t,
		core.Model{ID: "z", Provider: core.ProviderCodex, ProviderModel: "z"},
		core.Model{ID: "a", Provider: core.ProviderClaude, ProviderModel: "a"},
	)
	gateway, err := New(registry, map[core.ProviderName]*ProviderRuntime{
		core.ProviderCodex: codexRuntime, core.ProviderClaude: claudeRuntime,
	}, validGatewayConfig(t), Dependencies{NewRuntimeID: func() (string, error) { return strings.Repeat("c", 32), nil }, Now: time.Now})
	if err != nil {
		t.Fatal(err)
	}

	models := gateway.Models()
	health := gateway.Health()
	if len(models) != 2 || models[0].ID != "a" || models[1].ID != "z" ||
		len(health) != 2 || health[0].Provider != core.ProviderClaude || health[1].Provider != core.ProviderCodex {
		t.Fatalf("unexpected snapshots: models=%+v health=%+v", models, health)
	}
	models[0].ID = "mutated"
	health[0].Status = provider.HealthNotReady
	health[0].Capabilities[0] = "mutated"
	if gateway.Models()[0].ID != "a" || gateway.Health()[0].Status != provider.HealthReady ||
		gateway.Health()[0].Capabilities[0] != "empty_settings" {
		t.Fatal("Models or Health returned aliased state")
	}
}

func newGatewayWithRuntime(
	t *testing.T,
	runtime *ProviderRuntime,
	deps Dependencies,
) *Gateway {
	t.Helper()
	if deps.NewRuntimeID == nil {
		deps.NewRuntimeID = func() (string, error) { return strings.Repeat("d", 32), nil }
	}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	gateway, err := New(
		validRegistry(t),
		map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: runtime},
		validGatewayConfig(t),
		deps,
	)
	if err != nil {
		t.Fatalf("New() error=%v", err)
	}
	return gateway
}

func TestRespondRejectsShutdownBeforeRouting(t *testing.T) {
	gateway, adapter, scheduled, supervisor := validGateway(t)
	if err := gateway.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}

	_, err := gateway.Respond(context.Background(), core.Request{
		ModelAlias: "unknown-secret-alias",
		Format: core.OutputFormat{
			Type:   core.FormatJSONSchema,
			Schema: []byte(`{"not":"a schema"}`),
		},
	})
	if got := apiCode(t, err); got != core.CodeServiceShuttingDown {
		t.Fatalf("code=%q", got)
	}
	if adapter.buildCalls.Load() != 0 || adapter.parseCalls.Load() != 0 {
		t.Fatal("adapter called after shutdown")
	}
	if calls, _ := scheduled.counts(); calls != 0 {
		t.Fatal("scheduler Do called after shutdown")
	}
	if prepares, _, executes := supervisor.counts(); prepares != 0 || executes != 0 {
		t.Fatal("supervisor called after shutdown")
	}
}

func TestRespondOrderStopsAtEachPreAdmissionGate(t *testing.T) {
	t.Run("unknown alias before schema", func(t *testing.T) {
		gateway, adapter, scheduled, supervisor := validGateway(t)
		_, err := gateway.Respond(context.Background(), core.Request{
			ModelAlias: "unknown",
			Format:     core.OutputFormat{Type: core.FormatJSONSchema, Schema: []byte(`{"type":"bogus"}`)},
		})
		if got := apiCode(t, err); got != core.CodeModelNotFound {
			t.Fatalf("code=%q", got)
		}
		assertNoProviderActivity(t, adapter, scheduled, supervisor)
	})

	t.Run("invalid schema before health and admission", func(t *testing.T) {
		gateway, adapter, scheduled, supervisor := validGateway(t)
		_, err := gateway.Respond(context.Background(), core.Request{
			ModelAlias: "public-model",
			Format:     core.OutputFormat{Type: core.FormatJSONSchema, Schema: []byte(`{"$ref":"https://secret.invalid"}`)},
		})
		if got := apiCode(t, err); got != core.CodeInvalidJSONSchema {
			t.Fatalf("code=%q", got)
		}
		assertNoProviderActivity(t, adapter, scheduled, supervisor)
	})

	t.Run("initial non-ready before admission", func(t *testing.T) {
		health := authMissingHealth(core.ProviderCodex)
		runtime, err := NewProviderRuntime(
			&spyAdapter{name: core.ProviderCodex}, provider.ProviderConfig{}, nil, nil, health,
		)
		if err != nil {
			t.Fatal(err)
		}
		gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
		_, respondErr := gateway.Respond(context.Background(), requestFor("public-model"))
		if got := apiCode(t, respondErr); got != core.CodeProviderNotReady {
			t.Fatalf("code=%q", got)
		}
	})
}

func assertNoProviderActivity(
	t *testing.T,
	adapter *spyAdapter,
	scheduled *spyScheduler,
	supervisor *spySupervisor,
) {
	t.Helper()
	if adapter.buildCalls.Load() != 0 || adapter.parseCalls.Load() != 0 {
		t.Fatal("adapter was called")
	}
	if calls, _ := scheduled.counts(); calls != 0 {
		t.Fatal("scheduler was called")
	}
	if prepares, discards, executes := supervisor.counts(); prepares != 0 || discards != 0 || executes != 0 {
		t.Fatal("supervisor was called")
	}
}

func TestRespondRechecksHealthAtAdmissionBeforeRuntimeID(t *testing.T) {
	runtime, adapter, scheduled, supervisor := validRuntime(t, core.ProviderCodex)
	var gateway *Gateway
	scheduled.do = func(ctx context.Context, _ int64, work func(context.Context) error) error {
		owned := gateway.providers[core.ProviderCodex]
		owned.mu.Lock()
		owned.health.Status = provider.HealthNotReady
		owned.health.Auth = "missing"
		owned.health.Problems = []string{provider.ProblemAuthMissing}
		owned.mu.Unlock()
		return work(ctx)
	}
	var idCalls atomic.Int32
	gateway = newGatewayWithRuntime(t, runtime, Dependencies{
		NewRuntimeID: func() (string, error) { idCalls.Add(1); return strings.Repeat("e", 32), nil },
	})

	_, err := gateway.Respond(context.Background(), requestFor("public-model"))
	if got := apiCode(t, err); got != core.CodeProviderNotReady {
		t.Fatalf("code=%q", got)
	}
	if idCalls.Load() != 0 {
		t.Fatalf("runtime ID calls=%d", idCalls.Load())
	}
	if adapter.buildCalls.Load() != 0 || adapter.parseCalls.Load() != 0 {
		t.Fatal("adapter called after queued health degradation")
	}
	if prepares, _, executes := supervisor.counts(); prepares != 0 || executes != 0 {
		t.Fatal("supervisor called after queued health degradation")
	}
}

func TestRespondRunsExactlyOneAttemptInFixedOrderAndPublishesMetadata(t *testing.T) {
	var eventsMu sync.Mutex
	var events []string
	add := func(value string) {
		eventsMu.Lock()
		events = append(events, value)
		eventsMu.Unlock()
	}
	adapter := &spyAdapter{name: core.ProviderCodex}
	adapter.build = func(_ core.Request, model core.Model, _ provider.ProviderConfig, runtime process.Runtime) (process.CommandSpec, error) {
		add("build")
		if model.ProviderModel != "trusted-model" || runtime.ID != strings.Repeat("f", 32) {
			t.Fatal("untrusted model or runtime reached Build")
		}
		return process.CommandSpec{Executable: "/trusted/provider", Dir: runtime.Dir}, nil
	}
	adapter.parse = func(_ core.Request, result process.Result) (string, error) {
		add("parse")
		return string(result.Stdout), nil
	}
	scheduled := &spyScheduler{stats: scheduler.Stats{Queued: 3, QueuedBytes: 99, Running: 2}}
	scheduled.do = func(ctx context.Context, _ int64, work func(context.Context) error) error {
		add("do")
		return work(ctx)
	}
	supervisor := &spySupervisor{}
	supervisor.prepare = func(id string) (process.Runtime, error) {
		add("prepare")
		return process.Runtime{ID: id, Dir: "/runtime/" + id}, nil
	}
	supervisor.execute = func(_ context.Context, _ process.Runtime, _ process.CommandSpec) (process.Result, error) {
		add("execute")
		return process.Result{
			Stdout: []byte("hello"), StdoutTotal: 5, StderrTotal: 7, ExitCode: 0,
			StopReason: process.StopReasonNormalExit, StopAction: process.StopActionNone,
		}, nil
	}
	runtime, err := NewProviderRuntime(adapter, validProviderConfig(), scheduled, supervisor, validHealth(core.ProviderCodex))
	if err != nil {
		t.Fatal(err)
	}
	clock := &stepClock{values: []time.Time{
		time.Unix(100, 0), time.Unix(102, 0), time.Unix(107, 0),
	}}
	gateway := newGatewayWithRuntime(t, runtime, Dependencies{
		NewRuntimeID: func() (string, error) { add("id"); return strings.Repeat("f", 32), nil },
		Now:          clock.Now,
	})

	result, respondErr := gateway.Respond(context.Background(), requestFor("public-model"))
	if respondErr != nil {
		t.Fatal(respondErr)
	}
	wantEvents := []string{"do", "id", "prepare", "build", "execute", "parse"}
	if !slices.Equal(events, wantEvents) {
		t.Fatalf("events=%q, want %q", events, wantEvents)
	}
	wantMeta := core.ResultMeta{
		Provider: core.ProviderCodex, StdoutBytes: 5, StderrBytes: 7,
		QueueDepth: 3, RunningCount: 2, QueueDuration: 2 * time.Second,
		ExecutionTime: 5 * time.Second, ProviderVersion: "0.146.0",
		ExitCategory: "completed", StopReason: "completed", StopAction: "none",
	}
	if result.Text != "hello" || result.Meta != wantMeta {
		t.Fatalf("result=%+v, want text hello meta %+v", result, wantMeta)
	}
	if adapter.buildCalls.Load() != 1 || adapter.parseCalls.Load() != 1 {
		t.Fatal("gateway retried adapter")
	}
}

func TestRespondSnapshotsRequestBeforeQueueAndUsesFreshAdapterCopies(t *testing.T) {
	originalInstructions := "original instructions"
	originalDescription := "original description"
	originalSchema := []byte(`{"type":"object","properties":{"answer":{"const":"yes"}},"required":["answer"],"additionalProperties":false}`)
	req := core.Request{
		ModelAlias: "public-model", Instructions: &originalInstructions, Input: "input",
		Format: core.OutputFormat{Type: core.FormatJSONSchema, Name: "answer", Description: &originalDescription, Schema: originalSchema},
	}
	wantWeight := req.Weight()
	entered := make(chan struct{})
	release := make(chan struct{})
	adapter := &spyAdapter{name: core.ProviderCodex}
	var seenBuild, seenParse core.Request
	var seenPrefixes [][]string
	adapter.build = func(got core.Request, _ core.Model, cfg provider.ProviderConfig, runtime process.Runtime) (process.CommandSpec, error) {
		seenBuild = got
		seenPrefixes = append(seenPrefixes, slices.Clone(cfg.PrefixArgs))
		cfg.PrefixArgs[0] = "adapter-mutated"
		if len(got.Format.Schema) > 0 {
			got.Format.Schema[0] = '['
		}
		return process.CommandSpec{Executable: cfg.Executable, Dir: runtime.Dir}, nil
	}
	adapter.parse = func(got core.Request, _ process.Result) (string, error) {
		seenParse = got
		return `{"answer":"yes"}`, nil
	}
	scheduled := &spyScheduler{}
	var admissionCalls atomic.Int32
	scheduled.do = func(ctx context.Context, weight int64, work func(context.Context) error) error {
		if admissionCalls.Add(1) == 1 {
			if weight != wantWeight {
				t.Errorf("weight=%d, want %d", weight, wantWeight)
			}
			close(entered)
			<-release
		}
		return work(ctx)
	}
	supervisor := &spySupervisor{execute: func(context.Context, process.Runtime, process.CommandSpec) (process.Result, error) {
		return successfulProcessResult("ignored"), nil
	}}
	runtime, err := NewProviderRuntime(adapter, validProviderConfig(), scheduled, supervisor, validHealth(core.ProviderCodex))
	if err != nil {
		t.Fatal(err)
	}
	gateway := newGatewayWithRuntime(t, runtime, Dependencies{})

	done := make(chan struct {
		result core.Result
		err    error
	}, 1)
	go func() {
		result, respondErr := gateway.Respond(context.Background(), req)
		done <- struct {
			result core.Result
			err    error
		}{result, respondErr}
	}()
	<-entered
	originalInstructions = "mutated instructions"
	originalDescription = "mutated description"
	copy(originalSchema, []byte(strings.Repeat("x", len(originalSchema))))
	close(release)
	outcome := <-done
	if outcome.err != nil || outcome.result.Text != `{"answer":"yes"}` {
		t.Fatalf("Respond()=(%+v,%v)", outcome.result, outcome.err)
	}
	if seenBuild.Instructions == nil || *seenBuild.Instructions != "original instructions" ||
		seenBuild.Format.Description == nil || *seenBuild.Format.Description != "original description" {
		t.Fatalf("Build saw mutated pointers: %+v", seenBuild)
	}
	if seenParse.Format.Schema[0] != '{' {
		t.Fatal("Build mutation reached Parse request snapshot")
	}

	_, secondErr := gateway.Respond(context.Background(), core.Request{
		ModelAlias: "public-model", Format: core.OutputFormat{Type: core.FormatText},
	})
	if secondErr != nil {
		t.Fatal(secondErr)
	}
	if len(seenPrefixes) != 2 || seenPrefixes[0][0] != "--mode" || seenPrefixes[1][0] != "--mode" {
		t.Fatalf("adapter config mutations escaped: %q", seenPrefixes)
	}
}

func TestBuildFailureAndPanicAlwaysDiscardPreparedRuntime(t *testing.T) {
	tests := []struct {
		name  string
		build func(core.Request, core.Model, provider.ProviderConfig, process.Runtime) (process.CommandSpec, error)
	}{
		{"error", func(core.Request, core.Model, provider.ProviderConfig, process.Runtime) (process.CommandSpec, error) {
			return process.CommandSpec{}, errors.New("planted-build-secret")
		}},
		{"panic", func(core.Request, core.Model, provider.ProviderConfig, process.Runtime) (process.CommandSpec, error) {
			panic("planted-build-panic-secret")
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &spyAdapter{name: core.ProviderCodex, build: test.build}
			scheduled := &spyScheduler{do: func(ctx context.Context, _ int64, work func(context.Context) error) (result error) {
				defer func() {
					if recover() != nil {
						result = errors.New("scheduler-contained-panic-secret")
					}
				}()
				return work(ctx)
			}}
			supervisor := &spySupervisor{}
			runtime, err := NewProviderRuntime(adapter, validProviderConfig(), scheduled, supervisor, validHealth(core.ProviderCodex))
			if err != nil {
				t.Fatal(err)
			}
			gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
			_, respondErr := gateway.Respond(context.Background(), requestFor("public-model"))
			if got := apiCode(t, respondErr); got != core.CodeInternalError {
				t.Fatalf("code=%q", got)
			}
			if strings.Contains(respondErr.Error(), "secret") {
				t.Fatalf("public error leaked secret: %v", respondErr)
			}
			if prepares, discards, executes := supervisor.counts(); prepares != 1 || discards != 1 || executes != 0 {
				t.Fatalf("supervisor calls=%d/%d/%d, want 1/1/0", prepares, discards, executes)
			}
		})
	}
}

func TestExecuteAndParseFailureNeverRetryOrAdvance(t *testing.T) {
	t.Run("execute", func(t *testing.T) {
		runtime, adapter, _, supervisor := validRuntime(t, core.ProviderCodex)
		supervisor.execute = func(context.Context, process.Runtime, process.CommandSpec) (process.Result, error) {
			return process.Result{ExitCode: -1, StopReason: process.StopReasonSupervisorTimeout, StopAction: process.StopActionKILL},
				&process.RunError{Kind: process.ErrorTimeout, Err: errors.New("secret")}
		}
		gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
		_, err := gateway.Respond(context.Background(), requestFor("public-model"))
		if got := apiCode(t, err); got != core.CodeProviderTimeout {
			t.Fatalf("code=%q", got)
		}
		if adapter.parseCalls.Load() != 0 {
			t.Fatal("Parse called after Execute error")
		}
		if prepares, discards, executes := supervisor.counts(); prepares != 1 || discards != 0 || executes != 1 {
			t.Fatalf("supervisor calls=%d/%d/%d", prepares, discards, executes)
		}
	})

	t.Run("parse", func(t *testing.T) {
		runtime, adapter, _, supervisor := validRuntime(t, core.ProviderCodex)
		adapter.parse = func(core.Request, process.Result) (string, error) {
			return "", provider.NewProviderError(provider.ProviderErrorProtocol)
		}
		gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
		_, err := gateway.Respond(context.Background(), requestFor("public-model"))
		if got := apiCode(t, err); got != core.CodeProviderProtocolError {
			t.Fatalf("code=%q", got)
		}
		if adapter.parseCalls.Load() != 1 || adapter.buildCalls.Load() != 1 {
			t.Fatal("adapter did not run exactly once")
		}
		if prepares, discards, executes := supervisor.counts(); prepares != 1 || discards != 0 || executes != 1 {
			t.Fatalf("supervisor calls=%d/%d/%d", prepares, discards, executes)
		}
	})

	t.Run("unclassified nonzero despite parse success", func(t *testing.T) {
		runtime, adapter, _, supervisor := validRuntime(t, core.ProviderCodex)
		supervisor.execute = func(context.Context, process.Runtime, process.CommandSpec) (process.Result, error) {
			result := successfulProcessResult("provider claimed success")
			result.ExitCode = 7
			return result, nil
		}
		adapter.parse = func(core.Request, process.Result) (string, error) { return "provider claimed success", nil }
		gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
		_, err := gateway.Respond(context.Background(), requestFor("public-model"))
		if got := apiCode(t, err); got != core.CodeProviderFailed {
			t.Fatalf("code=%q", got)
		}
		if adapter.parseCalls.Load() != 1 {
			t.Fatalf("Parse calls=%d, want 1", adapter.parseCalls.Load())
		}
	})

	t.Run("unknown parse error", func(t *testing.T) {
		runtime, adapter, _, _ := validRuntime(t, core.ProviderCodex)
		adapter.parse = func(core.Request, process.Result) (string, error) {
			return "", errors.New("planted-parse-secret")
		}
		gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
		_, err := gateway.Respond(context.Background(), requestFor("public-model"))
		if got := apiCode(t, err); got != core.CodeInternalError {
			t.Fatalf("code=%q", got)
		}
		if strings.Contains(err.Error(), "secret") {
			t.Fatalf("parse error leaked: %v", err)
		}
	})
}

func TestActiveProcessCancellationPreservesCallerCause(t *testing.T) {
	started := make(chan struct{})
	runtime, _, scheduled, supervisor := validRuntime(t, core.ProviderCodex)
	scheduled.do = func(ctx context.Context, _ int64, work func(context.Context) error) error {
		return work(ctx)
	}
	supervisor.execute = func(ctx context.Context, _ process.Runtime, _ process.CommandSpec) (process.Result, error) {
		close(started)
		<-ctx.Done()
		return process.Result{
			ExitCode: -1, StopReason: process.StopReasonCallerCancellation, StopAction: process.StopActionKILL,
		}, &process.RunError{Kind: process.ErrorCanceled, Err: errors.New("planted-cancel-secret")}
	}
	gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := gateway.Respond(ctx, requestFor("public-model"))
		done <- err
	}()
	<-started
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Respond() error=%v, want context.Canceled", err)
	}
	if strings.Contains(err.Error(), "secret") {
		t.Fatalf("cancellation leaked: %v", err)
	}
	if meta := outcomeMeta(t, err); meta.StopReason != "client_canceled" {
		t.Fatalf("StopReason=%q, want client_canceled", meta.StopReason)
	}
}

func TestExecutionCancellationUsesGatewayShutdownMetadataBeforeSchedulerNormalization(t *testing.T) {
	runtime, _, scheduled, supervisor := validRuntime(t, core.ProviderCodex)
	var gateway *Gateway
	scheduled.do = func(_ context.Context, _ int64, work func(context.Context) error) error {
		runCtx, cancel := context.WithCancel(context.Background())
		cancel()
		gateway.closing.Store(true)
		return work(runCtx)
	}
	supervisor.execute = func(context.Context, process.Runtime, process.CommandSpec) (process.Result, error) {
		return process.Result{
			ExitCode: -1, StopReason: process.StopReasonCallerCancellation, StopAction: process.StopActionKILL,
		}, &process.RunError{Kind: process.ErrorCanceled, Err: context.Canceled}
	}
	gateway = newGatewayWithRuntime(t, runtime, Dependencies{})

	_, err := gateway.Respond(context.Background(), requestFor("public-model"))
	if got := apiCode(t, err); got != core.CodeServiceShuttingDown {
		t.Fatalf("code=%q", got)
	}
	if meta := outcomeMeta(t, err); meta.StopReason != "gateway_shutdown" {
		t.Fatalf("StopReason=%q, want gateway_shutdown", meta.StopReason)
	}
}

func TestRespondClosedErrorMappingAndSecretRedaction(t *testing.T) {
	tests := []struct {
		name      string
		setup     func(*spyAdapter, *spyScheduler, *spySupervisor)
		wantCode  string
		wantCause error
	}{
		{"queue full", func(_ *spyAdapter, scheduled *spyScheduler, _ *spySupervisor) {
			scheduled.do = func(context.Context, int64, func(context.Context) error) error { return scheduler.ErrQueueFull }
		}, core.CodeQueueFull, nil},
		{"queue timeout", func(_ *spyAdapter, scheduled *spyScheduler, _ *spySupervisor) {
			scheduled.do = func(context.Context, int64, func(context.Context) error) error { return scheduler.ErrQueueTimeout }
		}, core.CodeQueueTimeout, nil},
		{"scheduler shutdown", func(_ *spyAdapter, scheduled *spyScheduler, _ *spySupervisor) {
			scheduled.do = func(context.Context, int64, func(context.Context) error) error { return scheduler.ErrShuttingDown }
		}, core.CodeServiceShuttingDown, nil},
		{"provider rate limit", func(adapter *spyAdapter, _ *spyScheduler, _ *spySupervisor) {
			adapter.parse = func(core.Request, process.Result) (string, error) {
				return "", provider.NewProviderError(provider.ProviderErrorRateLimited)
			}
		}, core.CodeProviderRateLimited, nil},
		{"provider auth", func(adapter *spyAdapter, _ *spyScheduler, _ *spySupervisor) {
			adapter.parse = func(core.Request, process.Result) (string, error) {
				return "", provider.NewProviderError(provider.ProviderErrorAuthRequired)
			}
		}, core.CodeProviderAuthRequired, nil},
		{"provider protocol", func(adapter *spyAdapter, _ *spyScheduler, _ *spySupervisor) {
			adapter.parse = func(core.Request, process.Result) (string, error) {
				return "", provider.NewProviderError(provider.ProviderErrorProtocol)
			}
		}, core.CodeProviderProtocolError, nil},
		{"provider failed", func(adapter *spyAdapter, _ *spyScheduler, supervisor *spySupervisor) {
			supervisor.execute = func(context.Context, process.Runtime, process.CommandSpec) (process.Result, error) {
				result := successfulProcessResult("discarded")
				result.ExitCode = 7
				return result, nil
			}
			adapter.parse = func(core.Request, process.Result) (string, error) {
				return "", provider.NewProviderError(provider.ProviderErrorFailed)
			}
		}, core.CodeProviderFailed, nil},
		{"execution timeout", func(_ *spyAdapter, _ *spyScheduler, supervisor *spySupervisor) {
			supervisor.execute = runFailure(
				process.ErrorTimeout, process.StopReasonSupervisorTimeout, process.StopActionKILL,
				errors.New("planted-process-timeout-secret"),
			)
		}, core.CodeProviderTimeout, nil},
		{"output limit", func(_ *spyAdapter, _ *spyScheduler, supervisor *spySupervisor) {
			supervisor.execute = runFailure(
				process.ErrorOutputLimit, process.StopReasonOutputOverflow, process.StopActionKILL,
				errors.New("planted-output-secret"),
			)
		}, core.CodeOutputLimitExceeded, nil},
		{"ordinary start", func(_ *spyAdapter, _ *spyScheduler, supervisor *spySupervisor) {
			supervisor.execute = runFailure(
				process.ErrorStart, process.StopReasonNormalExit, process.StopActionNone,
				errors.New("planted-start-secret"),
			)
		}, core.CodeProviderFailed, nil},
		{"cleanup", func(_ *spyAdapter, _ *spyScheduler, supervisor *spySupervisor) {
			supervisor.execute = runFailure(
				process.ErrorCleanup, process.StopReasonCleanupFailure, process.StopActionKILL,
				errors.New("planted-cleanup-secret"),
			)
		}, core.CodeProcessCleanupFailed, nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, adapter, scheduled, supervisor := validRuntime(t, core.ProviderCodex)
			scheduled.stats = scheduler.Stats{Queued: 4, QueuedBytes: 19, Running: 2}
			test.setup(adapter, scheduled, supervisor)
			clock := &stepClock{values: []time.Time{
				time.Unix(10, 0), time.Unix(11, 0), time.Unix(13, 0),
			}}
			gateway := newGatewayWithRuntime(t, runtime, Dependencies{Now: clock.Now})
			request := requestFor("public-model")
			secretInstructions := "planted-request-secret"
			request.Instructions = &secretInstructions
			_, err := gateway.Respond(context.Background(), request)
			if got := apiCode(t, err); got != test.wantCode {
				t.Fatalf("code=%q, want %q (err=%v)", got, test.wantCode, err)
			}
			for _, secret := range []string{"planted", "secret", request.Input} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("public error leaked %q: %v", secret, err)
				}
			}
			meta := outcomeMeta(t, err)
			if meta.Provider != core.ProviderCodex || meta.QueueDepth != 4 || meta.RunningCount != 2 ||
				meta.ProviderVersion != "0.146.0" || meta.QueueDuration < 0 || meta.ExecutionTime < 0 {
				t.Fatalf("metadata=%+v", meta)
			}
		})
	}
}

func runFailure(
	kind process.ErrorKind,
	reason process.StopReason,
	action process.StopAction,
	cause error,
) func(context.Context, process.Runtime, process.CommandSpec) (process.Result, error) {
	return func(context.Context, process.Runtime, process.CommandSpec) (process.Result, error) {
		return process.Result{
			ExitCode: -1, StopReason: reason, StopAction: action,
		}, &process.RunError{Kind: kind, Err: cause}
	}
}

func TestRespondPreservesCallerCancellationCauses(t *testing.T) {
	for _, test := range []struct {
		name  string
		cause error
	}{
		{"canceled", context.Canceled},
		{"deadline", context.DeadlineExceeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime, _, scheduled, _ := validRuntime(t, core.ProviderCodex)
			ctx, cancel := context.WithCancelCause(context.Background())
			cancel(test.cause)
			scheduled.do = func(context.Context, int64, func(context.Context) error) error {
				return scheduler.ErrCanceled
			}
			gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
			_, err := gateway.Respond(ctx, requestFor("public-model"))
			if !errors.Is(err, test.cause) {
				t.Fatalf("Respond() error=%v, want cause %v", err, test.cause)
			}
			outcomeMeta(t, err)
		})
	}
}

func TestRespondFinalOutputPrecedenceAndExactSchema(t *testing.T) {
	tests := []struct {
		name     string
		final    string
		finalCap int
		format   core.OutputFormat
		wantCode string
		wantText string
	}{
		{"oversize before UTF8", string([]byte{0xff, 'x'}), 1, core.OutputFormat{Type: core.FormatText}, core.CodeOutputLimitExceeded, ""},
		{"empty", "", 4096, core.OutputFormat{Type: core.FormatText}, core.CodeProviderProtocolError, ""},
		{"invalid UTF8", string([]byte{0xff}), 4096, core.OutputFormat{Type: core.FormatText}, core.CodeProviderProtocolError, ""},
		{"duplicate JSON", `{"answer":"yes","answer":"no"}`, 4096, structuredFormat(), core.CodeStructuredOutputInvalid, ""},
		{"fenced JSON", "```json\n{\"answer\":\"yes\"}\n```", 4096, structuredFormat(), core.CodeStructuredOutputInvalid, ""},
		{"schema mismatch", `{"answer":"no"}`, 4096, structuredFormat(), core.CodeStructuredOutputInvalid, ""},
		{"leading trailing whitespace preserved", " \n{\"answer\":\"yes\"}\t", 4096, structuredFormat(), "", " \n{\"answer\":\"yes\"}\t"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, adapter, _, _ := validRuntime(t, core.ProviderCodex)
			adapter.parse = func(core.Request, process.Result) (string, error) { return test.final, nil }
			cfg := validGatewayConfig(t)
			cfg.FinalBytes = test.finalCap
			cfg.SchemaLimits.OutputBytes = test.finalCap
			gateway, err := New(validRegistry(t), map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: runtime}, cfg, Dependencies{
				NewRuntimeID: func() (string, error) { return strings.Repeat("1", 32), nil }, Now: time.Now,
			})
			if err != nil {
				t.Fatal(err)
			}
			request := requestFor("public-model")
			request.Format = test.format
			result, respondErr := gateway.Respond(context.Background(), request)
			if test.wantCode == "" {
				if respondErr != nil || result.Text != test.wantText {
					t.Fatalf("Respond()=(%q,%v), want %q,nil", result.Text, respondErr, test.wantText)
				}
				return
			}
			if got := apiCode(t, respondErr); got != test.wantCode {
				t.Fatalf("code=%q, want %q", got, test.wantCode)
			}
		})
	}
}

func structuredFormat() core.OutputFormat {
	return core.OutputFormat{
		Type: core.FormatJSONSchema, Name: "answer",
		Schema: []byte(`{"type":"object","properties":{"answer":{"const":"yes"}},"required":["answer"],"additionalProperties":false}`),
	}
}

func TestRespondInvalidRuntimeIDStopsBeforePrepare(t *testing.T) {
	tests := []struct {
		name string
		id   string
		err  error
	}{
		{"source error", "", errors.New("planted-id-secret")},
		{"short", strings.Repeat("a", 31), nil},
		{"uppercase", strings.Repeat("A", 32), nil},
		{"nonhex", strings.Repeat("z", 32), nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, _, _, supervisor := validRuntime(t, core.ProviderCodex)
			gateway := newGatewayWithRuntime(t, runtime, Dependencies{NewRuntimeID: func() (string, error) { return test.id, test.err }})
			_, err := gateway.Respond(context.Background(), requestFor("public-model"))
			if got := apiCode(t, err); got != core.CodeInternalError {
				t.Fatalf("code=%q", got)
			}
			if prepares, _, _ := supervisor.counts(); prepares != 0 {
				t.Fatalf("Prepare calls=%d", prepares)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatal("runtime ID error leaked")
			}
		})
	}
}

func TestInvariantFailuresCollapseToFreshInternalError(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*spyScheduler, *spySupervisor, *stepClock)
	}{
		{"negative queued", func(s *spyScheduler, _ *spySupervisor, _ *stepClock) { s.stats.Queued = -1 }},
		{"negative queued bytes", func(s *spyScheduler, _ *spySupervisor, _ *stepClock) { s.stats.QueuedBytes = -1 }},
		{"negative running", func(s *spyScheduler, _ *spySupervisor, _ *stepClock) { s.stats.Running = -1 }},
		{"clock backwards", func(_ *spyScheduler, _ *spySupervisor, clock *stepClock) {
			clock.values = []time.Time{time.Unix(10, 0), time.Unix(9, 0), time.Unix(8, 0)}
		}},
		{"negative stdout total", func(_ *spyScheduler, s *spySupervisor, _ *stepClock) {
			s.execute = func(context.Context, process.Runtime, process.CommandSpec) (process.Result, error) {
				result := successfulProcessResult("hello")
				result.StdoutTotal = -1
				return result, nil
			}
		}},
		{"unknown stop reason", func(_ *spyScheduler, s *spySupervisor, _ *stepClock) {
			s.execute = func(context.Context, process.Runtime, process.CommandSpec) (process.Result, error) {
				result := successfulProcessResult("hello")
				result.StopReason = "secret_reason"
				return result, nil
			}
		}},
		{"unknown run kind", func(_ *spyScheduler, s *spySupervisor, _ *stepClock) {
			s.execute = runFailure("secret_kind", process.StopReasonNormalExit, process.StopActionNone, errors.New("secret"))
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, _, scheduled, supervisor := validRuntime(t, core.ProviderCodex)
			clock := &stepClock{values: []time.Time{time.Unix(1, 0), time.Unix(2, 0), time.Unix(3, 0)}}
			test.setup(scheduled, supervisor, clock)
			gateway := newGatewayWithRuntime(t, runtime, Dependencies{Now: clock.Now})
			_, err := gateway.Respond(context.Background(), requestFor("public-model"))
			if got := apiCode(t, err); got != core.CodeInternalError {
				t.Fatalf("code=%q, want internal_error", got)
			}
			// Exact concrete type proves invariant failures are fresh catalog errors,
			// never OutcomeError wrappers carrying rejected metadata.
			//nolint:errorlint
			if _, ok := err.(*core.APIError); !ok {
				t.Fatalf("internal invariant error type=%T, want fresh *core.APIError", err)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("internal error leaked: %v", err)
			}
		})
	}
}

func TestMarkedExecutableFailuresDegradeSelectedProviderOnly(t *testing.T) {
	for _, test := range []struct {
		name        string
		cause       error
		wantProblem string
	}{
		{"missing", errors.Join(process.ErrExecutableUnavailable, fs.ErrNotExist), provider.ProblemExecutableMissing},
		{"permission", errors.Join(process.ErrExecutableUnavailable, fs.ErrPermission), provider.ProblemExecutableUnsafe},
	} {
		t.Run(test.name, func(t *testing.T) {
			codexRuntime, _, codexScheduler, codexSupervisor := validRuntime(t, core.ProviderCodex)
			claudeRuntime, _, _, _ := validRuntime(t, core.ProviderClaude)
			codexSupervisor.execute = runFailure(
				process.ErrorStart, process.StopReasonNormalExit, process.StopActionNone, test.cause,
			)
			registry := validRegistry(t,
				core.Model{ID: "codex", Provider: core.ProviderCodex, ProviderModel: "codex-model"},
				core.Model{ID: "claude", Provider: core.ProviderClaude, ProviderModel: "claude-model"},
			)
			gateway, err := New(registry, map[core.ProviderName]*ProviderRuntime{
				core.ProviderCodex: codexRuntime, core.ProviderClaude: claudeRuntime,
			}, validGatewayConfig(t), Dependencies{
				NewRuntimeID: func() (string, error) { return strings.Repeat("2", 32), nil }, Now: time.Now,
			})
			if err != nil {
				t.Fatal(err)
			}

			_, firstErr := gateway.Respond(context.Background(), requestFor("codex"))
			if got := apiCode(t, firstErr); got != core.CodeProviderNotReady {
				t.Fatalf("first code=%q", got)
			}
			health := gateway.Health()
			if len(health) != 2 || health[0].Provider != core.ProviderClaude || health[0].Status != provider.HealthReady ||
				health[1].Provider != core.ProviderCodex || health[1].Status != provider.HealthNotReady ||
				!slices.Equal(health[1].Problems, []string{test.wantProblem}) {
				t.Fatalf("health=%+v", health)
			}
			_, secondErr := gateway.Respond(context.Background(), requestFor("codex"))
			if got := apiCode(t, secondErr); got != core.CodeProviderNotReady {
				t.Fatalf("second code=%q", got)
			}
			if calls, _ := codexScheduler.counts(); calls != 1 {
				t.Fatalf("scheduler calls=%d, want 1", calls)
			}
			if result, claudeErr := gateway.Respond(context.Background(), requestFor("claude")); claudeErr != nil || result.Text != "hello" {
				t.Fatalf("unselected provider result=%+v err=%v", result, claudeErr)
			}
		})
	}
}

func TestUnmarkedStartFilesystemErrorsDoNotDegrade(t *testing.T) {
	for _, cause := range []error{fs.ErrNotExist, fs.ErrPermission, errors.New("network-like secret")} {
		runtime, _, _, supervisor := validRuntime(t, core.ProviderCodex)
		supervisor.execute = runFailure(
			process.ErrorStart, process.StopReasonNormalExit, process.StopActionNone, cause,
		)
		gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
		_, err := gateway.Respond(context.Background(), requestFor("public-model"))
		if got := apiCode(t, err); got != core.CodeProviderFailed {
			t.Fatalf("cause=%v code=%q", cause, got)
		}
		if health := gateway.Health(); len(health) != 1 || health[0].Status != provider.HealthReady {
			t.Fatalf("unmarked start degraded health: %+v", health)
		}
	}
}

func TestCleanupFailuresDegradeWithGatewayLocalProblem(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*spyAdapter, *spySupervisor)
	}{
		{"prepare", func(_ *spyAdapter, supervisor *spySupervisor) {
			supervisor.prepare = func(string) (process.Runtime, error) { return process.Runtime{}, errors.New("prepare-secret") }
		}},
		{"discard", func(adapter *spyAdapter, supervisor *spySupervisor) {
			adapter.build = func(core.Request, core.Model, provider.ProviderConfig, process.Runtime) (process.CommandSpec, error) {
				return process.CommandSpec{}, errors.New("build-secret")
			}
			supervisor.discard = func(context.Context, process.Runtime) error { return errors.New("discard-secret") }
		}},
		{"execute", func(_ *spyAdapter, supervisor *spySupervisor) {
			supervisor.execute = runFailure(
				process.ErrorCleanup, process.StopReasonCleanupFailure, process.StopActionKILL, errors.New("cleanup-secret"),
			)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, adapter, _, supervisor := validRuntime(t, core.ProviderCodex)
			test.setup(adapter, supervisor)
			gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
			_, err := gateway.Respond(context.Background(), requestFor("public-model"))
			if got := apiCode(t, err); got != core.CodeProcessCleanupFailed {
				t.Fatalf("code=%q", got)
			}
			if strings.Contains(err.Error(), "secret") {
				t.Fatalf("cleanup error leaked: %v", err)
			}
			health := gateway.Health()
			if len(health) != 1 || health[0].Status != provider.HealthNotReady ||
				!slices.Equal(health[0].Problems, []string{"runtime_cleanup_failed"}) {
				t.Fatalf("health=%+v", health)
			}
		})
	}
}

func TestConcurrentCleanupDegradationIsIdempotentAndRaceFree(t *testing.T) {
	runtime, _, _, supervisor := validRuntime(t, core.ProviderCodex)
	supervisor.execute = runFailure(
		process.ErrorCleanup, process.StopReasonCleanupFailure, process.StopActionKILL, errors.New("cleanup-secret"),
	)
	gateway := newGatewayWithRuntime(t, runtime, Dependencies{})

	const workers = 32
	start := make(chan struct{})
	var wait sync.WaitGroup
	for range workers {
		wait.Add(2)
		go func() {
			defer wait.Done()
			<-start
			_, _ = gateway.Respond(context.Background(), requestFor("public-model"))
		}()
		go func() {
			defer wait.Done()
			<-start
			_ = gateway.Health()
		}()
	}
	close(start)
	wait.Wait()

	health := gateway.Health()
	if len(health) != 1 || health[0].Status != provider.HealthNotReady ||
		!slices.Equal(health[0].Problems, []string{"runtime_cleanup_failed"}) {
		t.Fatalf("health=%+v", health)
	}
}

func TestProviderFailuresThatAreNotLivenessProofDoNotDegrade(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*spyAdapter, *spySupervisor)
	}{
		{"auth", func(adapter *spyAdapter, _ *spySupervisor) {
			adapter.parse = func(core.Request, process.Result) (string, error) {
				return "", provider.NewProviderError(provider.ProviderErrorAuthRequired)
			}
		}},
		{"rate", func(adapter *spyAdapter, _ *spySupervisor) {
			adapter.parse = func(core.Request, process.Result) (string, error) {
				return "", provider.NewProviderError(provider.ProviderErrorRateLimited)
			}
		}},
		{"protocol", func(adapter *spyAdapter, _ *spySupervisor) {
			adapter.parse = func(core.Request, process.Result) (string, error) {
				return "", provider.NewProviderError(provider.ProviderErrorProtocol)
			}
		}},
		{"timeout", func(_ *spyAdapter, supervisor *spySupervisor) {
			supervisor.execute = runFailure(process.ErrorTimeout, process.StopReasonSupervisorTimeout, process.StopActionKILL, errors.New("secret"))
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runtime, adapter, _, supervisor := validRuntime(t, core.ProviderCodex)
			test.setup(adapter, supervisor)
			gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
			_, _ = gateway.Respond(context.Background(), requestFor("public-model"))
			if health := gateway.Health(); len(health) != 1 || health[0].Status != provider.HealthReady || len(health[0].Problems) != 0 {
				t.Fatalf("health degraded for %s: %+v", test.name, health)
			}
		})
	}
}

func TestShutdownInvokesDistinctSchedulersConcurrentlyAndSharedOnce(t *testing.T) {
	started := make(chan string, 2)
	release := make(chan struct{})
	first := &spyScheduler{shutdown: func(context.Context) error { started <- "first"; <-release; return nil }}
	second := &spyScheduler{shutdown: func(context.Context) error { started <- "second"; <-release; return nil }}
	makeRuntime := func(name core.ProviderName, scheduled Scheduler) *ProviderRuntime {
		runtime, err := NewProviderRuntime(&spyAdapter{name: name}, validProviderConfig(), scheduled, &spySupervisor{}, validHealth(name))
		if err != nil {
			t.Fatal(err)
		}
		return runtime
	}
	providers := map[core.ProviderName]*ProviderRuntime{
		core.ProviderCodex:  makeRuntime(core.ProviderCodex, first),
		core.ProviderClaude: makeRuntime(core.ProviderClaude, second),
		core.ProviderGemini: makeRuntime(core.ProviderGemini, first),
	}
	gateway, err := New(validRegistry(t), providers, validGatewayConfig(t), Dependencies{
		NewRuntimeID: func() (string, error) { return strings.Repeat("3", 32), nil }, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- gateway.Shutdown(context.Background()) }()

	seen := []string{<-started, <-started}
	if !slices.Contains(seen, "first") || !slices.Contains(seen, "second") {
		t.Fatalf("shutdown did not start both distinct schedulers: %q", seen)
	}
	close(release)
	if shutdownErr := <-done; shutdownErr != nil {
		t.Fatal(shutdownErr)
	}
	if _, calls := first.counts(); calls != 1 {
		t.Fatalf("shared scheduler shutdown calls=%d", calls)
	}
	if _, calls := second.counts(); calls != 1 {
		t.Fatalf("second scheduler shutdown calls=%d", calls)
	}
}

func TestShutdownCanContinueDrainAndRedactsErrors(t *testing.T) {
	var calls atomic.Int32
	scheduled := &spyScheduler{shutdown: func(ctx context.Context) error {
		if calls.Add(1) == 1 {
			<-ctx.Done()
			return errors.New("planted-shutdown-secret")
		}
		return nil
	}}
	runtime, err := NewProviderRuntime(
		&spyAdapter{name: core.ProviderCodex}, validProviderConfig(), scheduled, &spySupervisor{}, validHealth(core.ProviderCodex),
	)
	if err != nil {
		t.Fatal(err)
	}
	gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	firstErr := gateway.Shutdown(ctx)
	if firstErr == nil || strings.Contains(firstErr.Error(), "planted") || strings.Contains(firstErr.Error(), "secret") {
		t.Fatalf("first Shutdown error=%v", firstErr)
	}
	if secondErr := gateway.Shutdown(context.Background()); secondErr != nil {
		t.Fatalf("second Shutdown error=%v", secondErr)
	}
	if calls.Load() != 2 {
		t.Fatalf("Shutdown calls=%d, want 2", calls.Load())
	}
}

func TestShutdownIgnoresNotReadyRuntimeWithoutScheduler(t *testing.T) {
	health := authMissingHealth(core.ProviderCodex)
	runtime, err := NewProviderRuntime(&spyAdapter{name: core.ProviderCodex}, provider.ProviderConfig{}, nil, nil, health)
	if err != nil {
		t.Fatal(err)
	}
	gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
	if err := gateway.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestActiveSchedulerShutdownTakesPrecedenceOverProcessCancellation(t *testing.T) {
	realScheduler, err := scheduler.New(scheduler.Limits{
		Concurrency: 1, QueueSize: 2, QueueBytes: 4096, QueueTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	started := make(chan struct{})
	supervisor := &spySupervisor{execute: func(ctx context.Context, _ process.Runtime, _ process.CommandSpec) (process.Result, error) {
		close(started)
		<-ctx.Done()
		return process.Result{
			ExitCode: -1, StopReason: process.StopReasonCallerCancellation, StopAction: process.StopActionKILL,
		}, &process.RunError{Kind: process.ErrorCanceled, Err: context.Canceled}
	}}
	runtime, err := NewProviderRuntime(
		&spyAdapter{name: core.ProviderCodex}, validProviderConfig(), realScheduler, supervisor, validHealth(core.ProviderCodex),
	)
	if err != nil {
		t.Fatal(err)
	}
	gateway := newGatewayWithRuntime(t, runtime, Dependencies{})
	result := make(chan error, 1)
	go func() {
		_, respondErr := gateway.Respond(context.Background(), requestFor("public-model"))
		result <- respondErr
	}()
	<-started
	if shutdownErr := gateway.Shutdown(context.Background()); shutdownErr != nil {
		t.Fatal(shutdownErr)
	}
	respondErr := <-result
	if got := apiCode(t, respondErr); got != core.CodeServiceShuttingDown {
		t.Fatalf("code=%q", got)
	}
	if meta := outcomeMeta(t, respondErr); meta.StopReason != "gateway_shutdown" {
		t.Fatalf("StopReason=%q, want gateway_shutdown", meta.StopReason)
	}
}

func TestProviderSchedulersRemainIsolated(t *testing.T) {
	blocked := make(chan struct{})
	codexScheduler := &spyScheduler{do: func(context.Context, int64, func(context.Context) error) error {
		<-blocked
		return scheduler.ErrQueueFull
	}}
	codexRuntime, err := NewProviderRuntime(
		&spyAdapter{name: core.ProviderCodex}, validProviderConfig(), codexScheduler, &spySupervisor{}, validHealth(core.ProviderCodex),
	)
	if err != nil {
		t.Fatal(err)
	}
	claudeRuntime, _, claudeScheduler, _ := validRuntime(t, core.ProviderClaude)
	registry := validRegistry(t,
		core.Model{ID: "codex", Provider: core.ProviderCodex, ProviderModel: "codex"},
		core.Model{ID: "claude", Provider: core.ProviderClaude, ProviderModel: "claude"},
	)
	gateway, err := New(registry, map[core.ProviderName]*ProviderRuntime{
		core.ProviderCodex: codexRuntime, core.ProviderClaude: claudeRuntime,
	}, validGatewayConfig(t), Dependencies{
		NewRuntimeID: func() (string, error) { return strings.Repeat("4", 32), nil }, Now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	codexDone := make(chan error, 1)
	go func() {
		_, respondErr := gateway.Respond(context.Background(), requestFor("codex"))
		codexDone <- respondErr
	}()
	result, claudeErr := gateway.Respond(context.Background(), requestFor("claude"))
	if claudeErr != nil || result.Text != "hello" {
		t.Fatalf("Claude result=%+v err=%v", result, claudeErr)
	}
	if calls, _ := claudeScheduler.counts(); calls != 1 {
		t.Fatalf("Claude scheduler calls=%d", calls)
	}
	close(blocked)
	if got := apiCode(t, <-codexDone); got != core.CodeQueueFull {
		t.Fatalf("Codex code=%q", got)
	}
}

func TestSchedulerInterfaceComparabilityCheckDoesNotPanic(t *testing.T) {
	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("constructor panicked on noncomparable scheduler: %v", recovered)
		}
	}()
	_, err := NewProviderRuntime(
		&spyAdapter{name: core.ProviderCodex}, validProviderConfig(),
		nonComparableScheduler{values: []int{1}}, &spySupervisor{}, validHealth(core.ProviderCodex),
	)
	if err == nil {
		t.Fatal("constructor accepted noncomparable scheduler")
	}
}

func TestDynamicSchedulerComparabilityIsRejectedAtEveryMapBoundary(t *testing.T) {
	deceptive := deceptiveScheduler{payload: []int{1}}

	t.Run("provider runtime constructor", func(t *testing.T) {
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("NewProviderRuntime panicked: %v", recovered)
			}
		}()
		got, err := NewProviderRuntime(
			&spyAdapter{name: core.ProviderCodex}, validProviderConfig(),
			deceptive, &spySupervisor{}, validHealth(core.ProviderCodex),
		)
		if err == nil || got != nil {
			t.Fatalf("NewProviderRuntime()=(%v,%v), want nil,error", got, err)
		}
	})

	t.Run("gateway snapshot", func(t *testing.T) {
		runtime, _, _, _ := validRuntime(t, core.ProviderCodex)
		runtime.Scheduler = deceptive
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("New panicked: %v", recovered)
			}
		}()
		got, err := New(
			validRegistry(t),
			map[core.ProviderName]*ProviderRuntime{core.ProviderCodex: runtime},
			validGatewayConfig(t),
			Dependencies{
				NewRuntimeID: func() (string, error) { return strings.Repeat("5", 32), nil },
				Now:          time.Now,
			},
		)
		if err == nil || got != nil {
			t.Fatalf("New()=(%v,%v), want nil,error", got, err)
		}
	})

	t.Run("shutdown defense in depth", func(t *testing.T) {
		gateway := &Gateway{providers: map[core.ProviderName]*ProviderRuntime{
			core.ProviderCodex: {Scheduler: deceptive},
		}}
		defer func() {
			if recovered := recover(); recovered != nil {
				t.Fatalf("Shutdown panicked: %v", recovered)
			}
		}()
		if err := gateway.Shutdown(context.Background()); err == nil {
			t.Fatal("Shutdown accepted dynamically non-comparable scheduler")
		}
	})
}

func TestTestFakesHaveExpectedInterfaceShapes(t *testing.T) {
	if reflect.TypeOf((*Scheduler)(nil)).Elem().NumMethod() != 3 ||
		reflect.TypeOf((*Supervisor)(nil)).Elem().NumMethod() != 3 {
		t.Fatal("gateway dependency interface unexpectedly changed")
	}
}
