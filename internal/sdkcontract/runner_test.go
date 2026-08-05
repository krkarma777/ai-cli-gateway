//go:build !windows

package sdkcontract

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
)

var testPolicy = lifecyclePolicy{
	PollInterval:      time.Millisecond,
	ReadinessDeadline: 20 * time.Millisecond,
	ProbeTimeout:      2 * time.Millisecond,
	GatewayGrace:      3 * time.Millisecond,
	HelperGrace:       4 * time.Millisecond,
	RegistryProtocol:  5 * time.Millisecond,
	RegistryCleanup:   6 * time.Millisecond,
}

func TestRunWithSystemSuccessOwnsOneRootRegistryAndGateway(t *testing.T) {
	sys := newFakeSystem()
	var output bytes.Buffer
	err := runWithSystem(context.Background(), fakeOptions(), &output, sys, testPolicy)
	if err != nil {
		t.Fatalf("runWithSystem() error = %v", err)
	}
	if got := output.String(); got != "python_sdk_contract_ok\njavascript_sdk_contract_ok\n" {
		t.Fatalf("output = %q", got)
	}
	if sys.mkdirTempCalls != 1 || sys.registryStarts != 1 || sys.registry.stops != 1 || sys.root.removes != 1 {
		t.Fatalf("ownership calls = root:%d registry:%d/%d remove:%d", sys.mkdirTempCalls, sys.registryStarts, sys.registry.stops, sys.root.removes)
	}
	if len(sys.children) != 1 || sys.children[0].stops != 1 {
		t.Fatalf("children = %#v, want one child stopped once", sys.children)
	}
	if got := sys.buildPackages; len(got) != 2 || got[0] != "./cmd/ai-cli-gateway" || got[1] != "./internal/testcli/cmd/fake-codex-cli" {
		t.Fatalf("build packages = %#v", got)
	}
	if sys.registry.readyCalls != 0 {
		t.Fatalf("ordinary run called registry Ready %d times", sys.registry.readyCalls)
	}
	if got := sys.clientNames; len(got) != 2 || got[0] != "/safe/python" || got[1] != "/safe/node" {
		t.Fatalf("clients = %#v", got)
	}
	if len(sys.clientEnvs) != 2 {
		t.Fatalf("client environments = %d", len(sys.clientEnvs))
	}
	key := "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	binRoot := "/safe/repository/.sdk-contract-test/bin"
	homeRoot := "/safe/repository/.sdk-contract-test/home"
	wantGatewayEnv := []string{"PATH=" + binRoot, "HOME=" + homeRoot, "AI_CLI_GATEWAY_API_KEY=" + key}
	if !slices.Equal(sys.gatewayEnvs[0], wantGatewayEnv) {
		t.Fatalf("gateway environment = %#v", sys.gatewayEnvs[0])
	}
	wantClientEnv := []string{
		"HOME=" + homeRoot,
		"PATH=" + binRoot,
		"AI_CLI_GATEWAY_BASE_URL=http://127.0.0.1:31001/v1",
		"AI_CLI_GATEWAY_API_KEY=" + key,
		"AI_CLI_GATEWAY_MODEL=codex-sdk-test",
		"AI_CLI_GATEWAY_TIMEOUT_SECONDS=5",
	}
	for _, env := range sys.clientEnvs {
		if !slices.Equal(env, wantClientEnv) {
			t.Fatalf("client environment = %#v", env)
		}
	}
	assertExactPolicyCalls(t, sys)
}

func TestRunWithSystemRetriesAtMostEightAndRetainsOneRegistry(t *testing.T) {
	sys := newFakeSystem()
	sys.probeStatuses = []int{503, 200}
	sys.exitAttempts = 1
	if err := runWithSystem(context.Background(), fakeOptions(), io.Discard, sys, testPolicy); err != nil {
		t.Fatalf("retry run error = %v", err)
	}
	if len(sys.children) != 2 || sys.children[0].stops != 1 || sys.children[1].stops != 1 {
		t.Fatalf("retry child stops = %#v", sys.children)
	}
	if sys.registryStarts != 1 || sys.registry.stops != 1 {
		t.Fatalf("registry starts/stops = %d/%d", sys.registryStarts, sys.registry.stops)
	}

	exhausted := newFakeSystem()
	exhausted.probeStatuses = []int{503, 503, 503, 503, 503, 503, 503, 503, 200}
	exhausted.exitAttempts = maxStartupAttempts
	err := runWithSystem(context.Background(), fakeOptions(), io.Discard, exhausted, testPolicy)
	if got := ErrorCategory(err); got != "sdk_contract_failed" {
		t.Fatalf("ErrorCategory(exhausted) = %q", got)
	}
	if exhausted.allocateCalls != 8 || len(exhausted.children) != 8 {
		t.Fatalf("attempts = allocations:%d children:%d", exhausted.allocateCalls, len(exhausted.children))
	}
	for index, child := range exhausted.children {
		if child.stops != 1 {
			t.Fatalf("child %d stops = %d", index, child.stops)
		}
	}
	if exhausted.registryStarts != 1 || exhausted.registry.stops != 1 || exhausted.root.removes != 1 {
		t.Fatalf("exhausted cleanup registry=%d/%d root=%d", exhausted.registryStarts, exhausted.registry.stops, exhausted.root.removes)
	}
}

func TestRunWithSystemCancellationNeverRetries(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	sys := newFakeSystem()
	sys.onProbe = cancel
	err := runWithSystem(ctx, fakeOptions(), io.Discard, sys, testPolicy)
	if got := ErrorCategory(err); got != "sdk_contract_canceled" {
		t.Fatalf("ErrorCategory(canceled) = %q", got)
	}
	if len(sys.children) != 1 || sys.children[0].stops != 1 || sys.allocateCalls != 1 {
		t.Fatalf("canceled ownership children=%d stops=%d allocations=%d", len(sys.children), sys.children[0].stops, sys.allocateCalls)
	}
}

func TestRunWithSystemClientOrderingAndFailures(t *testing.T) {
	python := newFakeSystem()
	python.clientErrors = []error{errors.New("private python failure")}
	err := runWithSystem(context.Background(), fakeOptions(), io.Discard, python, testPolicy)
	if got := ErrorCategory(err); got != "sdk_contract_failed" || len(python.clientNames) != 1 {
		t.Fatalf("python failure category=%q clients=%#v", got, python.clientNames)
	}

	javascript := newFakeSystem()
	javascript.clientErrors = []error{nil, errors.New("private javascript failure")}
	err = runWithSystem(context.Background(), fakeOptions(), io.Discard, javascript, testPolicy)
	if got := ErrorCategory(err); got != "sdk_contract_failed" || len(javascript.clientNames) != 2 {
		t.Fatalf("javascript failure category=%q clients=%#v", got, javascript.clientNames)
	}
}

func TestRunWithSystemCleanupResultSafetyTable(t *testing.T) {
	tests := []struct {
		name       string
		gateway    cleanupResult
		registry   cleanupResult
		wantRemove int
		wantClose  int
	}{
		{"clean", cleanupResult{SafeToRemove: true}, cleanupResult{SafeToRemove: true}, 1, 0},
		{"absent with error", cleanupResult{SafeToRemove: true, Err: errors.New("private")}, cleanupResult{SafeToRemove: true}, 1, 0},
		{"live with error", cleanupResult{SafeToRemove: false, Err: errors.New("private")}, cleanupResult{SafeToRemove: true}, 0, 1},
		{"invalid false nil", cleanupResult{}, cleanupResult{SafeToRemove: true}, 0, 1},
		{"registry live", cleanupResult{SafeToRemove: true}, cleanupResult{SafeToRemove: false, Err: errors.New("private")}, 0, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			sys := newFakeSystem()
			sys.nextChildCleanup = test.gateway
			sys.registry.result = test.registry
			err := runWithSystem(context.Background(), fakeOptions(), io.Discard, sys, testPolicy)
			if test.name == "clean" {
				if err != nil {
					t.Fatalf("clean error = %v", err)
				}
			} else if got := ErrorCategory(err); got != "sdk_contract_cleanup_failed" {
				t.Fatalf("category = %q", got)
			}
			if sys.root.removes != test.wantRemove || sys.root.closes != test.wantClose {
				t.Fatalf("root remove/close = %d/%d, want %d/%d", sys.root.removes, sys.root.closes, test.wantRemove, test.wantClose)
			}
		})
	}
}

func TestConstructorOutcomeTables(t *testing.T) {
	type outcome struct {
		name        string
		hasValue    bool
		err         error
		wantAccept  bool
		wantCleanup bool
		wantSafe    bool
	}
	outcomes := []outcome{
		{name: "owned success", hasValue: true, wantAccept: true, wantSafe: true},
		{name: "complete rollback", err: errors.New("private"), wantSafe: true},
		{name: "safe rollback error", err: newCleanupError(true), wantCleanup: true, wantSafe: true},
		{name: "owned uncertainty", hasValue: true, err: newCleanupError(false), wantAccept: true, wantCleanup: true},
		{name: "invalid owned plain error", hasValue: true, err: errors.New("private"), wantAccept: true, wantCleanup: true},
		{name: "invalid retained without owner", err: newCleanupError(false), wantCleanup: true},
		{name: "invalid nil nil", wantCleanup: true},
	}
	for _, kind := range []string{"root", "registry", "gateway"} {
		t.Run(kind, func(t *testing.T) {
			for _, test := range outcomes {
				t.Run(test.name, func(t *testing.T) {
					state := &runState{}
					accepted := false
					switch kind {
					case "root":
						var value ownedRoot
						if test.hasValue {
							value = &fakeRoot{path: "/safe/root"}
						}
						accepted = acceptRootConstructor(state, value, test.err)
					case "registry":
						var value fixtureRegistry
						if test.hasValue {
							value = &fakeRegistry{ready: make(chan struct{})}
						}
						accepted = acceptRegistryConstructor(state, value, test.err)
					case "gateway":
						var value child
						if test.hasValue {
							value = &fakeChild{exited: make(chan struct{})}
						}
						accepted = acceptGatewayConstructor(state, value, test.err)
					}
					if accepted != test.wantAccept || state.cleanupErr != test.wantCleanup || state.safety.safeToRemove() != test.wantSafe {
						t.Fatalf("accepted=%t cleanup=%t safe=%t", accepted, state.cleanupErr, state.safety.safeToRemove())
					}
				})
			}
		})
	}
}

func TestGatewayConstructorUncertaintyUsesFinalOwnerProof(t *testing.T) {
	tests := []struct {
		name              string
		gatewayCleanup    cleanupResult
		independentUnsafe bool
		wantRemove        int
		wantClose         int
	}{
		{
			name:           "final absence",
			gatewayCleanup: cleanupResult{SafeToRemove: true},
			wantRemove:     1,
		},
		{
			name:           "final absence with cleanup error",
			gatewayCleanup: cleanupResult{SafeToRemove: true, Err: errors.New("private cleanup failure")},
			wantRemove:     1,
		},
		{
			name:           "final absence not proven",
			gatewayCleanup: cleanupResult{SafeToRemove: false, Err: errors.New("private cleanup failure")},
			wantClose:      1,
		},
		{
			name:              "independent uncertainty remains",
			gatewayCleanup:    cleanupResult{SafeToRemove: true},
			independentUnsafe: true,
			wantClose:         1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := &fakeRoot{path: "/safe/root"}
			gateway := &fakeChild{exited: make(chan struct{}), result: test.gatewayCleanup}
			state := &runState{root: root}
			if test.independentUnsafe {
				if got := ErrorCategory(helperFailure(context.Background(), state, newCleanupError(false))); got != categoryCleanup {
					t.Fatalf("helper failure category = %q", got)
				}
			}
			if !acceptGatewayConstructor(state, gateway, newCleanupError(false)) {
				t.Fatal("gateway recovery owner was rejected")
			}

			err := finalizeRun(state, testPolicy, newError(categoryFailed))
			if got := ErrorCategory(err); got != categoryCleanup {
				t.Fatalf("category = %q", got)
			}
			if gateway.stops != 1 {
				t.Fatalf("gateway stops = %d", gateway.stops)
			}
			if root.removes != test.wantRemove || root.closes != test.wantClose {
				t.Fatalf("root remove/close = %d/%d, want %d/%d", root.removes, root.closes, test.wantRemove, test.wantClose)
			}
		})
	}
}

func TestRegistryConstructorUncertaintyUsesFinalOwnerProof(t *testing.T) {
	tests := []struct {
		name            string
		registryCleanup cleanupResult
		wantRemove      int
		wantClose       int
	}{
		{
			name:            "final absence",
			registryCleanup: cleanupResult{SafeToRemove: true},
			wantRemove:      1,
		},
		{
			name:            "final absence with cleanup error",
			registryCleanup: cleanupResult{SafeToRemove: true, Err: errors.New("private cleanup failure")},
			wantRemove:      1,
		},
		{
			name:            "final absence not proven",
			registryCleanup: cleanupResult{SafeToRemove: false, Err: errors.New("private cleanup failure")},
			wantClose:       1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := &fakeRoot{path: "/safe/root"}
			registry := &fakeRegistry{ready: make(chan struct{}), result: test.registryCleanup}
			state := &runState{root: root}
			if !acceptRegistryConstructor(state, registry, newCleanupError(false)) {
				t.Fatal("registry recovery owner was rejected")
			}

			err := finalizeRun(state, testPolicy, newError(categoryFailed))
			if got := ErrorCategory(err); got != categoryCleanup {
				t.Fatalf("category = %q", got)
			}
			if registry.stops != 1 {
				t.Fatalf("registry stops = %d", registry.stops)
			}
			if root.removes != test.wantRemove || root.closes != test.wantClose {
				t.Fatalf("root remove/close = %d/%d, want %d/%d", root.removes, root.closes, test.wantRemove, test.wantClose)
			}
		})
	}
}

func TestRunWithSystemRejectsInvalidPolicyBeforeSideEffects(t *testing.T) {
	fields := []func(*lifecyclePolicy){
		func(p *lifecyclePolicy) { p.PollInterval = 0 },
		func(p *lifecyclePolicy) { p.ReadinessDeadline = -1 },
		func(p *lifecyclePolicy) { p.ProbeTimeout = 0 },
		func(p *lifecyclePolicy) { p.GatewayGrace = 0 },
		func(p *lifecyclePolicy) { p.HelperGrace = 0 },
		func(p *lifecyclePolicy) { p.RegistryProtocol = 0 },
		func(p *lifecyclePolicy) { p.RegistryCleanup = time.Duration(1<<63 - 1) },
	}
	for index, mutate := range fields {
		sys := newFakeSystem()
		policy := testPolicy
		mutate(&policy)
		err := runWithSystem(context.Background(), fakeOptions(), io.Discard, sys, policy)
		if got := ErrorCategory(err); got != "invalid_input" {
			t.Fatalf("case %d category = %q", index, got)
		}
		if sys.mkdirTempCalls != 0 || sys.randomCalls != 0 {
			t.Fatalf("case %d performed side effects", index)
		}
	}
}

func TestRunWithSystemRejectsShortEntropyBeforeProcesses(t *testing.T) {
	for _, randomErr := range []error{nil, errors.New("private entropy failure")} {
		sys := newFakeSystem()
		sys.randomN = 31
		sys.randomErr = randomErr
		err := runWithSystem(context.Background(), fakeOptions(), io.Discard, sys, testPolicy)
		if got := ErrorCategory(err); got != "sdk_contract_failed" {
			t.Fatalf("category = %q", got)
		}
		if len(sys.children) != 0 || sys.buildCalls != 0 {
			t.Fatal("entropy failure started a process")
		}
	}
}

func TestRunWithSystemUnsupportedBeforeSideEffects(t *testing.T) {
	sys := newFakeSystem()
	sys.supported = false
	err := runWithSystem(context.Background(), fakeOptions(), io.Discard, sys, testPolicy)
	if got := ErrorCategory(err); got != "unsupported_platform" {
		t.Fatalf("category = %q", got)
	}
	if sys.mkdirTempCalls != 0 || sys.randomCalls != 0 {
		t.Fatal("unsupported host performed side effects")
	}
}

func TestRunWithSystemSuppressesForbiddenGatewayOutput(t *testing.T) {
	sys := newFakeSystem()
	sys.gatewayWrites = [][]byte{[]byte("prefix /safe/reposito"), []byte("ry/.sdk-contract-test suffix")}
	var output bytes.Buffer
	err := runWithSystem(context.Background(), fakeOptions(), &output, sys, testPolicy)
	if got := ErrorCategory(err); got != categoryFailed {
		t.Fatalf("category = %q", got)
	}
	if output.Len() != 0 {
		t.Fatalf("public output = %q", output.String())
	}
	if sys.root.removes != 1 {
		t.Fatalf("root removes = %d", sys.root.removes)
	}
}

func TestRunWithSystemSuppressesEveryForbiddenGatewayValueAcrossWrites(t *testing.T) {
	root := "/safe/repository/.sdk-contract-test"
	values := []string{
		"0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20",
		"SDK contract instruction.",
		"SDK contract input.",
		"SDK_GATEWAY_OK",
		"/safe/python",
		"/safe/node",
		"/safe/main.mjs",
		root,
		root + "/bin",
		root + "/home",
		root + "/bin/ai-cli-gateway",
		root + "/bin/fake-codex-cli",
		root + "/attempt-1/config.toml",
		root + "/attempt-1/config-home",
		root + "/attempt-1/runtime",
	}
	for index, value := range values {
		t.Run(fmt.Sprintf("value-%d", index), func(t *testing.T) {
			sys := newFakeSystem()
			split := len(value) / 2
			sys.gatewayWrites = [][]byte{[]byte("prefix " + value[:split]), []byte(value[split:] + " suffix")}
			var output bytes.Buffer
			err := runWithSystem(context.Background(), fakeOptions(), &output, sys, testPolicy)
			if ErrorCategory(err) != categoryFailed || output.Len() != 0 {
				t.Fatalf("category=%q output=%q", ErrorCategory(err), output.String())
			}
		})
	}
}

func TestRunWithSystemCreatesFreshExactConfigForEachRetry(t *testing.T) {
	sys := newFakeSystem()
	sys.exitAttempts = 1
	sys.probeStatuses = []int{503, 200}
	if err := runWithSystem(context.Background(), fakeOptions(), io.Discard, sys, testPolicy); err != nil {
		t.Fatalf("runWithSystem() error = %v", err)
	}
	if len(sys.filePaths) != 2 || sys.filePaths[0] == sys.filePaths[1] {
		t.Fatalf("config paths = %#v", sys.filePaths)
	}
	seenRuntime := map[string]bool{}
	seenHome := map[string]bool{}
	for index, data := range sys.fileData {
		decoded, err := config.Decode(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("config %d decode: %v", index, err)
		}
		if decoded.Server.Listen != "127.0.0.1:"+fmt.Sprint(31001+index) || time.Duration(decoded.Server.ShutdownTimeout) != 8*time.Second {
			t.Fatalf("config %d server = %#v", index, decoded.Server)
		}
		provider := decoded.Providers["codex"]
		if seenRuntime[decoded.Runtime.Root] || seenHome[provider.ConfigHome] || decoded.Runtime.Root == provider.ConfigHome {
			t.Fatalf("config %d reused roots", index)
		}
		seenRuntime[decoded.Runtime.Root], seenHome[provider.ConfigHome] = true, true
		if sys.fileModes[index] != 0o600 {
			t.Fatalf("file mode %d = %v", index, sys.fileModes[index])
		}
	}
	for index, mode := range sys.directoryModes {
		if mode != 0o700 {
			t.Fatalf("directory mode %d = %v", index, mode)
		}
	}
	if len(sys.events) < 2 || sys.events[0] != "root" || !slices.Contains(sys.events[:sys.allocateEventIndex], "registry") {
		t.Fatalf("event order = %#v", sys.events)
	}
}

func TestRunWithSystemReadinessDeadlineCancelsInFlightProbe(t *testing.T) {
	base := newFakeSystem()
	base.fireReadiness = true
	sys := blockingProbeSystem{fakeSystem: base}
	policy := testPolicy
	policy.ProbeTimeout = time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	err := runWithSystem(ctx, fakeOptions(), io.Discard, sys, policy)
	if got := ErrorCategory(err); got != categoryFailed {
		t.Fatalf("category = %q, want readiness failure before caller deadline", got)
	}
	if base.allocateCalls != maxStartupAttempts {
		t.Fatalf("attempts = %d", base.allocateCalls)
	}
}

func TestRunWithSystemRoutesPollAndProbePolicy(t *testing.T) {
	sys := newFakeSystem()
	sys.probeStatuses = []int{503, 200}
	sys.firePoll = true
	if err := runWithSystem(context.Background(), fakeOptions(), io.Discard, sys, testPolicy); err != nil {
		t.Fatalf("runWithSystem() error = %v", err)
	}
	if !slices.Contains(sys.timers, testPolicy.ReadinessDeadline) || !slices.Contains(sys.timers, testPolicy.PollInterval) {
		t.Fatalf("timers = %#v", sys.timers)
	}
	if len(sys.probeDeadlines) != 2 {
		t.Fatalf("probe deadlines = %#v", sys.probeDeadlines)
	}
	for _, remaining := range sys.probeDeadlines {
		if remaining <= 0 || remaining > testPolicy.ProbeTimeout {
			t.Fatalf("probe deadline remaining = %v", remaining)
		}
	}
	for index, timer := range sys.timerObjects {
		if timer.stopped == 0 {
			t.Fatalf("timer %d was not stopped", index)
		}
	}
}

func TestRunWithSystemScansGatewayShutdownOutputBeforeSuccess(t *testing.T) {
	sys := newFakeSystem()
	sys.gatewayWriteOnStop = []byte("SDK_GATEWAY_OK")
	var output bytes.Buffer
	err := runWithSystem(context.Background(), fakeOptions(), &output, sys, testPolicy)
	if got := ErrorCategory(err); got != categoryFailed {
		t.Fatalf("category = %q", got)
	}
	if output.Len() != 0 {
		t.Fatalf("public output = %q", output.String())
	}
}

type blockingProbeSystem struct{ *fakeSystem }

func (s blockingProbeSystem) ProbeModels(ctx context.Context, _, _ string) (int, error) {
	<-ctx.Done()
	return 0, ctx.Err()
}

func fakeOptions() Options {
	return Options{RepositoryRoot: "/safe/repository", PythonExecutable: "/safe/python", NodeExecutable: "/safe/node", JavaScriptEntrypoint: "/safe/main.mjs"}
}

type fakeTimer struct {
	channel chan time.Time
	stopped int
}

func (t *fakeTimer) C() <-chan time.Time { return t.channel }
func (t *fakeTimer) Stop() bool          { t.stopped++; return true }

type fakeRoot struct {
	path                string
	removes, closes     int
	removeErr, closeErr error
}

func (r *fakeRoot) Path() string       { return r.path }
func (r *fakeRoot) RemoveExact() error { r.removes++; return r.removeErr }
func (r *fakeRoot) Close() error       { r.closes++; return r.closeErr }

type fakeRegistry struct {
	ready             chan struct{}
	readyCalls, stops int
	result            cleanupResult
	stopGrace         []time.Duration
}

func (r *fakeRegistry) Ready() <-chan struct{} { r.readyCalls++; return r.ready }
func (r *fakeRegistry) StopAndVerify(grace time.Duration) cleanupResult {
	r.stops++
	r.stopGrace = append(r.stopGrace, grace)
	return r.result
}

type fakeChild struct {
	pid       int
	exited    chan struct{}
	stops     int
	result    cleanupResult
	stopGrace []time.Duration
	onStop    func()
}

func (c *fakeChild) PID() int                { return c.pid }
func (c *fakeChild) Exited() <-chan struct{} { return c.exited }
func (c *fakeChild) StopAndWait(grace time.Duration) cleanupResult {
	c.stops++
	if c.onStop != nil {
		c.onStop()
	}
	c.stopGrace = append(c.stopGrace, grace)
	return c.result
}

type fakeSystem struct {
	supported                                                              bool
	root                                                                   *fakeRoot
	registry                                                               *fakeRegistry
	mkdirTempCalls, randomCalls, buildCalls, allocateCalls, registryStarts int
	randomN                                                                int
	randomErr                                                              error
	children                                                               []*fakeChild
	exitAttempts                                                           int
	nextChildCleanup                                                       cleanupResult
	probeStatuses                                                          []int
	probeCalls                                                             int
	onProbe                                                                func()
	clientNames                                                            []string
	clientEnvs                                                             [][]string
	clientErrors                                                           []error
	timers                                                                 []time.Duration
	timerObjects                                                           []*fakeTimer
	buildGrace, allocateGrace                                              []time.Duration
	buildPackages                                                          []string
	clientGrace                                                            []time.Duration
	registryProtocol                                                       []time.Duration
	probeDeadlines                                                         []time.Duration
	gatewayWrites                                                          [][]byte
	gatewayWriteOnStop                                                     []byte
	gatewayEnvs                                                            [][]string
	directoryPaths                                                         []string
	directoryModes                                                         []fs.FileMode
	filePaths                                                              []string
	fileData                                                               [][]byte
	fileModes                                                              []fs.FileMode
	events                                                                 []string
	allocateEventIndex                                                     int
	fireReadiness                                                          bool
	firePoll                                                               bool
}

func newFakeSystem() *fakeSystem {
	return &fakeSystem{
		supported:        true,
		root:             &fakeRoot{path: "/safe/repository/.sdk-contract-test"},
		registry:         &fakeRegistry{ready: make(chan struct{}), result: cleanupResult{SafeToRemove: true}},
		randomN:          32,
		nextChildCleanup: cleanupResult{SafeToRemove: true},
		probeStatuses:    []int{200},
	}
}

func (s *fakeSystem) Supported() bool { return s.supported }
func (s *fakeSystem) MkdirTemp(_, _ string) (ownedRoot, error) {
	s.mkdirTempCalls++
	s.events = append(s.events, "root")
	return s.root, nil
}
func (s *fakeSystem) MkdirAll(path string, mode fs.FileMode) error {
	s.directoryPaths = append(s.directoryPaths, path)
	s.directoryModes = append(s.directoryModes, mode)
	return nil
}
func (s *fakeSystem) WriteFile(path string, data []byte, mode fs.FileMode) error {
	s.filePaths = append(s.filePaths, path)
	s.fileData = append(s.fileData, append([]byte(nil), data...))
	s.fileModes = append(s.fileModes, mode)
	return nil
}
func (s *fakeSystem) ReadRandom(destination []byte) (int, error) {
	s.randomCalls++
	for i := range destination {
		destination[i] = byte(i + 1)
	}
	return s.randomN, s.randomErr
}
func (s *fakeSystem) NewTimer(duration time.Duration) timer {
	s.timers = append(s.timers, duration)
	ch := make(chan time.Time, 1)
	if s.fireReadiness && duration == testPolicy.ReadinessDeadline {
		ch <- time.Now()
	}
	if s.firePoll && duration == testPolicy.PollInterval {
		ch <- time.Now()
	}
	timer := &fakeTimer{channel: ch}
	s.timerObjects = append(s.timerObjects, timer)
	return timer
}
func (s *fakeSystem) Build(_ context.Context, _, _ string, pkg string, grace time.Duration) error {
	s.buildCalls++
	s.buildGrace = append(s.buildGrace, grace)
	s.buildPackages = append(s.buildPackages, pkg)
	return nil
}
func (s *fakeSystem) AllocatePort(_ context.Context, _ string, grace time.Duration) (uint16, error) {
	s.allocateEventIndex = len(s.events)
	s.events = append(s.events, "allocate")
	s.allocateCalls++
	s.allocateGrace = append(s.allocateGrace, grace)
	return uint16(31000 + s.allocateCalls), nil // #nosec G115 -- bounded by maxStartupAttempts in the runner.
}
func (s *fakeSystem) StartFixtureRegistry(_ string, grace time.Duration) (fixtureRegistry, error) {
	s.events = append(s.events, "registry")
	s.registryStarts++
	s.registryProtocol = append(s.registryProtocol, grace)
	return s.registry, nil
}
func (s *fakeSystem) StartGateway(_, _ string, _ []string, env []string, output io.Writer) (child, error) {
	s.gatewayEnvs = append(s.gatewayEnvs, append([]string(nil), env...))
	for _, write := range s.gatewayWrites {
		_, _ = output.Write(write)
	}
	exited := make(chan struct{})
	if len(s.children) < s.exitAttempts {
		close(exited)
	}
	child := &fakeChild{pid: 100 + len(s.children), exited: exited, result: s.nextChildCleanup}
	if len(s.gatewayWriteOnStop) != 0 {
		child.onStop = func() { _, _ = output.Write(s.gatewayWriteOnStop) }
	}
	s.children = append(s.children, child)
	return child, nil
}
func (s *fakeSystem) ProbeModels(ctx context.Context, _ string, key string) (int, error) {
	if key == "" || strings.HasSuffix(key, "-wrong") {
		return 401, nil
	}
	s.probeCalls++
	if deadline, ok := ctx.Deadline(); ok {
		s.probeDeadlines = append(s.probeDeadlines, time.Until(deadline))
	}
	if s.onProbe != nil {
		s.onProbe()
	}
	index := s.probeCalls - 1
	if index >= len(s.probeStatuses) {
		index = len(s.probeStatuses) - 1
	}
	return s.probeStatuses[index], nil
}
func (s *fakeSystem) RunClient(_ context.Context, executable string, _ []string, env []string, grace time.Duration) ([]byte, error) {
	s.clientNames = append(s.clientNames, executable)
	s.clientEnvs = append(s.clientEnvs, append([]string(nil), env...))
	s.clientGrace = append(s.clientGrace, grace)
	index := len(s.clientNames) - 1
	if index < len(s.clientErrors) && s.clientErrors[index] != nil {
		return nil, s.clientErrors[index]
	}
	if strings.Contains(executable, "python") {
		return []byte("python_sdk_contract_ok\n"), nil
	}
	return []byte("javascript_sdk_contract_ok\n"), nil
}

func assertExactPolicyCalls(t *testing.T, sys *fakeSystem) {
	t.Helper()
	if len(sys.buildGrace) != 2 || sys.buildGrace[0] != testPolicy.HelperGrace || sys.buildGrace[1] != testPolicy.HelperGrace {
		t.Fatalf("build grace = %#v", sys.buildGrace)
	}
	if len(sys.allocateGrace) != 1 || sys.allocateGrace[0] != testPolicy.HelperGrace {
		t.Fatalf("allocate grace = %#v", sys.allocateGrace)
	}
	if len(sys.clientGrace) != 2 || sys.clientGrace[0] != testPolicy.HelperGrace || sys.clientGrace[1] != testPolicy.HelperGrace {
		t.Fatalf("client grace = %#v", sys.clientGrace)
	}
	if len(sys.registryProtocol) != 1 || sys.registryProtocol[0] != testPolicy.RegistryProtocol {
		t.Fatalf("registry protocol = %#v", sys.registryProtocol)
	}
	if len(sys.children) != 1 || len(sys.children[0].stopGrace) != 1 || sys.children[0].stopGrace[0] != testPolicy.GatewayGrace {
		t.Fatalf("gateway grace = %#v", sys.children[0].stopGrace)
	}
	if len(sys.registry.stopGrace) != 1 || sys.registry.stopGrace[0] != testPolicy.RegistryCleanup {
		t.Fatalf("registry cleanup = %#v", sys.registry.stopGrace)
	}
}
