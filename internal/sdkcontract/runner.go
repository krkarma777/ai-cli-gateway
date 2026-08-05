// Package sdkcontract runs the checked-in OpenAI SDK examples against a real
// gateway process and a repository-owned fake Codex CLI.
package sdkcontract

import (
	"bytes"
	"context"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	categoryFailed      = "sdk_contract_failed"
	categoryCanceled    = "sdk_contract_canceled"
	categoryCleanup     = "sdk_contract_cleanup_failed"
	categoryInvalid     = "invalid_input"
	categoryUnsupported = "unsupported_platform"

	maxStartupAttempts = 8
	maxPolicyDuration  = 24 * time.Hour
)

type categorizedError struct{ category string }

func (e categorizedError) Error() string { return e.category }

func newError(category string) error { return categorizedError{category: category} }

// ErrorCategory returns the fixed public category for err.
func ErrorCategory(err error) string {
	if err == nil {
		return ""
	}
	var categorized categorizedError
	if errors.As(err, &categorized) {
		return categorized.category
	}
	return categoryFailed
}

// Options names the repository-owned contract inputs.
type Options struct {
	RepositoryRoot       string
	PythonExecutable     string
	NodeExecutable       string
	JavaScriptEntrypoint string
}

type child interface {
	PID() int
	Exited() <-chan struct{}
	StopAndWait(time.Duration) cleanupResult
}

type ownedRoot interface {
	Path() string
	RemoveExact() error
	Close() error
}

type cleanupResult struct {
	SafeToRemove bool
	Err          error
}

type cleanupSafetyError interface {
	error
	SafeToRemove() bool
}

type safetyError struct{ safe bool }

func (e safetyError) Error() string      { return categoryCleanup }
func (e safetyError) SafeToRemove() bool { return e.safe }
func newCleanupError(safe bool) error    { return safetyError{safe: safe} }

type timer interface {
	C() <-chan time.Time
	Stop() bool
}

type lifecyclePolicy struct {
	PollInterval      time.Duration
	ReadinessDeadline time.Duration
	ProbeTimeout      time.Duration
	GatewayGrace      time.Duration
	HelperGrace       time.Duration
	RegistryProtocol  time.Duration
	RegistryCleanup   time.Duration
}

type fixtureRegistry interface {
	Ready() <-chan struct{}
	StopAndVerify(time.Duration) cleanupResult
}

type system interface {
	Supported() bool
	MkdirTemp(parent, pattern string) (ownedRoot, error)
	MkdirAll(path string, mode fs.FileMode) error
	WriteFile(path string, data []byte, mode fs.FileMode) error
	ReadRandom(destination []byte) (int, error)
	NewTimer(time.Duration) timer
	Build(context.Context, string, string, string, time.Duration) error
	AllocatePort(context.Context, string, time.Duration) (uint16, error)
	StartFixtureRegistry(string, time.Duration) (fixtureRegistry, error)
	StartGateway(string, string, []string, []string, io.Writer) (child, error)
	ProbeModels(context.Context, string, string) (int, error)
	RunClient(context.Context, string, []string, []string, time.Duration) ([]byte, error)
}

var productionPolicy = lifecyclePolicy{
	PollInterval:      100 * time.Millisecond,
	ReadinessDeadline: 5 * time.Second,
	ProbeTimeout:      time.Second,
	GatewayGrace:      10 * time.Second,
	HelperGrace:       time.Second,
	RegistryProtocol:  2 * time.Second,
	RegistryCleanup:   2 * time.Second,
}

var productionSystemFactory func() system

// Run executes the black-box contract with production lifecycle bounds.
func Run(ctx context.Context, options Options, output io.Writer) error {
	if productionSystemFactory == nil {
		return newError(categoryUnsupported)
	}
	return runWithSystem(ctx, options, output, productionSystemFactory(), productionPolicy)
}

type runState struct {
	root       ownedRoot
	registry   fixtureRegistry
	gateway    child
	rootSafe   bool
	cleanupErr bool
	capture    *deferredScanner
}

func runWithSystem(
	ctx context.Context,
	options Options,
	output io.Writer,
	sys system,
	policy lifecyclePolicy,
) (result error) {
	if sys == nil || !sys.Supported() {
		return newError(categoryUnsupported)
	}
	if !validPolicy(policy) || !validOptionsShape(options) || output == nil {
		return newError(categoryInvalid)
	}
	if validator, ok := sys.(interface{ ValidateOptions(Options) error }); ok {
		if err := validator.ValidateOptions(options); err != nil {
			return newError(categoryInvalid)
		}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return newError(categoryCanceled)
	}

	state := &runState{rootSafe: true}
	completed := false
	defer func() {
		result = finalizeRun(state, policy, result)
		if result == nil && completed {
			if _, err := io.WriteString(output, "python_sdk_contract_ok\njavascript_sdk_contract_ok\n"); err != nil {
				result = newError(categoryFailed)
			}
		}
	}()

	root, err := sys.MkdirTemp(filepath.Dir(options.RepositoryRoot), ".sdk-contract-")
	if !acceptRootConstructor(state, root, err) {
		return constructorCategory(ctx, err)
	}
	if err != nil {
		state.cleanupErr = true
		return newError(categoryCleanup)
	}

	var entropy [32]byte
	n, entropyErr := sys.ReadRandom(entropy[:])
	if entropyErr != nil || n != len(entropy) {
		return newError(categoryFailed)
	}
	key := hex.EncodeToString(entropy[:])
	if len(key) != 64 {
		return newError(categoryFailed)
	}

	binRoot := filepath.Join(root.Path(), "bin")
	homeRoot := filepath.Join(root.Path(), "home")
	if err := sys.MkdirAll(binRoot, 0o700); err != nil {
		return newError(categoryFailed)
	}
	if err := sys.MkdirAll(homeRoot, 0o700); err != nil {
		return newError(categoryFailed)
	}
	gatewayPath := filepath.Join(binRoot, "ai-cli-gateway")
	fakePath := filepath.Join(binRoot, "fake-codex-cli")
	if err := sys.Build(ctx, options.RepositoryRoot, gatewayPath, "./cmd/ai-cli-gateway", policy.HelperGrace); err != nil {
		return helperFailure(ctx, state, err)
	}
	if err := sys.Build(ctx, options.RepositoryRoot, fakePath, "./internal/testcli/cmd/fake-codex-cli", policy.HelperGrace); err != nil {
		return helperFailure(ctx, state, err)
	}

	registryPath := filepath.Join(binRoot, "fixture.registry")
	registry, registryErr := sys.StartFixtureRegistry(registryPath, policy.RegistryProtocol)
	if !acceptRegistryConstructor(state, registry, registryErr) {
		return constructorCategory(ctx, registryErr)
	}
	if registryErr != nil {
		state.cleanupErr = true
		return newError(categoryCleanup)
	}

	for attempt := 1; attempt <= maxStartupAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return newError(categoryCanceled)
		}
		port, err := sys.AllocatePort(ctx, options.PythonExecutable, policy.HelperGrace)
		if err != nil {
			return helperFailure(ctx, state, err)
		}
		attemptRoot := filepath.Join(root.Path(), fmt.Sprintf("attempt-%d", attempt))
		configHome := filepath.Join(attemptRoot, "config-home")
		runtimeRoot := filepath.Join(attemptRoot, "runtime")
		if err := sys.MkdirAll(configHome, 0o700); err != nil {
			return newError(categoryFailed)
		}
		if err := sys.MkdirAll(runtimeRoot, 0o700); err != nil {
			return newError(categoryFailed)
		}
		configPath := filepath.Join(attemptRoot, "config.toml")
		configBytes := generatedConfig(port, fakePath, configHome, runtimeRoot)
		if err := sys.WriteFile(configPath, configBytes, 0o600); err != nil {
			return newError(categoryFailed)
		}
		capture := newDeferredScanner([]string{
			key,
			"SDK contract instruction.",
			"SDK contract input.",
			"SDK_GATEWAY_OK",
			options.PythonExecutable,
			options.NodeExecutable,
			options.JavaScriptEntrypoint,
			root.Path(),
			binRoot,
			homeRoot,
			gatewayPath,
			fakePath,
			configPath,
			configHome,
			runtimeRoot,
		})
		gateway, gatewayErr := sys.StartGateway(
			gatewayPath,
			options.RepositoryRoot,
			[]string{"serve", "--config", configPath},
			[]string{"PATH=" + binRoot, "HOME=" + homeRoot, "AI_CLI_GATEWAY_API_KEY=" + key},
			capture,
		)
		if !acceptGatewayConstructor(state, gateway, gatewayErr) {
			return constructorCategory(ctx, gatewayErr)
		}
		state.capture = capture
		if gatewayErr != nil {
			state.cleanupErr = true
			return newError(categoryCleanup)
		}

		baseURL := fmt.Sprintf("http://127.0.0.1:%d/v1", port)
		ready, canceled := awaitReady(ctx, sys, gateway, baseURL, key, policy)
		if canceled {
			return newError(categoryCanceled)
		}
		if !ready {
			cleanup := stopGateway(state, policy.GatewayGrace)
			if capture.Err() != nil {
				return newError(categoryFailed)
			}
			if !cleanup.SafeToRemove || cleanup.Err != nil {
				return newError(categoryCleanup)
			}
			if attempt == maxStartupAttempts {
				return newError(categoryFailed)
			}
			continue
		}

		if !requireAuthChecks(ctx, sys, baseURL, key, policy.ProbeTimeout) {
			return contextCategory(ctx)
		}
		clientEnv := []string{
			"HOME=" + homeRoot,
			"PATH=" + binRoot,
			"AI_CLI_GATEWAY_BASE_URL=" + baseURL,
			"AI_CLI_GATEWAY_API_KEY=" + key,
			"AI_CLI_GATEWAY_MODEL=codex-sdk-test",
			"AI_CLI_GATEWAY_TIMEOUT_SECONDS=5",
		}
		pythonOutput, err := sys.RunClient(ctx, options.PythonExecutable,
			[]string{"-I", filepath.Join(options.RepositoryRoot, "examples/openai-sdk/python/main.py")}, clientEnv, policy.HelperGrace)
		if err != nil {
			return helperFailure(ctx, state, err)
		}
		if subtle.ConstantTimeCompare(pythonOutput, []byte("python_sdk_contract_ok\n")) != 1 {
			return newError(categoryFailed)
		}

		javascriptOutput, err := sys.RunClient(ctx, options.NodeExecutable,
			[]string{options.JavaScriptEntrypoint}, clientEnv, policy.HelperGrace)
		if err != nil {
			return helperFailure(ctx, state, err)
		}
		if subtle.ConstantTimeCompare(javascriptOutput, []byte("javascript_sdk_contract_ok\n")) != 1 {
			return newError(categoryFailed)
		}
		if capture.Err() != nil {
			return newError(categoryFailed)
		}
		completed = true
		return nil
	}
	return newError(categoryFailed)
}

func validPolicy(policy lifecyclePolicy) bool {
	values := [...]time.Duration{
		policy.PollInterval, policy.ReadinessDeadline, policy.ProbeTimeout,
		policy.GatewayGrace, policy.HelperGrace, policy.RegistryProtocol,
		policy.RegistryCleanup,
	}
	for _, value := range values {
		if value <= 0 || value > maxPolicyDuration {
			return false
		}
	}
	return true
}

func validOptionsShape(options Options) bool {
	for _, value := range []string{options.RepositoryRoot, options.PythonExecutable, options.NodeExecutable, options.JavaScriptEntrypoint} {
		if value == "" || !filepath.IsAbs(value) || filepath.Clean(value) != value {
			return false
		}
	}
	return true
}

func acceptRootConstructor(state *runState, value ownedRoot, err error) bool {
	if value != nil {
		state.root = value
		if err != nil {
			state.cleanupErr = true
			state.rootSafe = cleanupErrorSafe(err)
		}
		return true
	}
	if err == nil || !cleanupErrorSafe(err) && isCleanupSafety(err) {
		state.cleanupErr = true
		state.rootSafe = false
		return false
	}
	if isCleanupSafety(err) {
		state.cleanupErr = true
	}
	return false
}

func acceptRegistryConstructor(state *runState, value fixtureRegistry, err error) bool {
	if value != nil {
		state.registry = value
		if err != nil {
			state.cleanupErr = true
			state.rootSafe = cleanupErrorSafe(err)
		}
		return true
	}
	if err == nil || isCleanupSafety(err) {
		state.cleanupErr = true
		if err == nil || !cleanupErrorSafe(err) {
			state.rootSafe = false
		}
	}
	return false
}

func acceptGatewayConstructor(state *runState, value child, err error) bool {
	if value != nil {
		state.gateway = value
		if err != nil {
			state.cleanupErr = true
			state.rootSafe = cleanupErrorSafe(err)
		}
		return true
	}
	if err == nil || isCleanupSafety(err) {
		state.cleanupErr = true
		if err == nil || !cleanupErrorSafe(err) {
			state.rootSafe = false
		}
	}
	return false
}

func isCleanupSafety(err error) bool {
	var safety cleanupSafetyError
	return errors.As(err, &safety)
}

func cleanupErrorSafe(err error) bool {
	var safety cleanupSafetyError
	return errors.As(err, &safety) && safety.SafeToRemove()
}

func helperFailure(ctx context.Context, state *runState, err error) error {
	if isCleanupSafety(err) {
		state.cleanupErr = true
		if !cleanupErrorSafe(err) {
			state.rootSafe = false
		}
		return newError(categoryCleanup)
	}
	return contextCategory(ctx)
}

func constructorCategory(ctx context.Context, err error) error {
	if err == nil || isCleanupSafety(err) {
		return newError(categoryCleanup)
	}
	return contextCategory(ctx)
}

func contextCategory(ctx context.Context) error {
	if ctx != nil && ctx.Err() != nil {
		return newError(categoryCanceled)
	}
	return newError(categoryFailed)
}

func awaitReady(ctx context.Context, sys system, gateway child, baseURL, key string, policy lifecyclePolicy) (bool, bool) {
	deadline := sys.NewTimer(policy.ReadinessDeadline)
	defer deadline.Stop()
	for {
		probeCtx, cancel := context.WithTimeout(ctx, policy.ProbeTimeout)
		type probeResult struct {
			status int
			err    error
		}
		completed := make(chan probeResult, 1)
		go func() {
			status, err := sys.ProbeModels(probeCtx, baseURL, key)
			completed <- probeResult{status: status, err: err}
		}()
		var status int
		var err error
		select {
		case <-ctx.Done():
			cancel()
			<-completed
			return false, true
		case <-gateway.Exited():
			cancel()
			<-completed
			return false, false
		case <-deadline.C():
			cancel()
			<-completed
			return false, false
		case result := <-completed:
			status, err = result.status, result.err
			cancel()
		}
		if ctx.Err() != nil {
			return false, true
		}
		select {
		case <-deadline.C():
			return false, false
		default:
		}
		if err == nil && status == 200 {
			return true, false
		}
		poll := sys.NewTimer(policy.PollInterval)
		select {
		case <-ctx.Done():
			poll.Stop()
			return false, true
		case <-gateway.Exited():
			poll.Stop()
			return false, false
		case <-deadline.C():
			poll.Stop()
			return false, false
		case <-poll.C():
			poll.Stop()
		}
	}
}

func requireAuthChecks(ctx context.Context, sys system, baseURL, key string, timeout time.Duration) bool {
	for _, candidate := range []string{"", key + "-wrong"} {
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		status, err := sys.ProbeModels(probeCtx, baseURL, candidate)
		cancel()
		if err != nil || status != 401 {
			return false
		}
	}
	return true
}

func stopGateway(state *runState, grace time.Duration) cleanupResult {
	if state.gateway == nil {
		return cleanupResult{SafeToRemove: true}
	}
	result := normalizeCleanup(state.gateway.StopAndWait(grace))
	state.gateway = nil
	if result.Err != nil {
		state.cleanupErr = true
	}
	if !result.SafeToRemove {
		state.rootSafe = false
	}
	return result
}

func normalizeCleanup(result cleanupResult) cleanupResult {
	if !result.SafeToRemove && result.Err == nil {
		return cleanupResult{SafeToRemove: false, Err: newError(categoryCleanup)}
	}
	return result
}

func finalizeRun(state *runState, policy lifecyclePolicy, primary error) error {
	if state == nil || state.root == nil {
		if state != nil && state.cleanupErr {
			return newError(categoryCleanup)
		}
		return primary
	}
	if state.gateway != nil {
		_ = stopGateway(state, policy.GatewayGrace)
	}
	if state.capture != nil && state.capture.Err() != nil && primary == nil {
		primary = newError(categoryFailed)
	}
	if state.registry != nil {
		registryResult := normalizeCleanup(state.registry.StopAndVerify(policy.RegistryCleanup))
		state.registry = nil
		if registryResult.Err != nil {
			state.cleanupErr = true
		}
		if !registryResult.SafeToRemove {
			state.rootSafe = false
		}
	}
	if state.rootSafe {
		if err := state.root.RemoveExact(); err != nil {
			state.cleanupErr = true
		}
	} else {
		if err := state.root.Close(); err != nil {
			state.cleanupErr = true
		}
	}
	state.root = nil
	if state.cleanupErr {
		return newError(categoryCleanup)
	}
	return primary
}

func generatedConfig(port uint16, fakePath, configHome, runtimeRoot string) []byte {
	return []byte(fmt.Sprintf(`[server]
listen = "127.0.0.1:%d"
api_key_env = "AI_CLI_GATEWAY_API_KEY"
shutdown_timeout = "8s"

[runtime]
root = %q
term_grace = "1s"
cleanup_timeout = "2s"

[providers.codex]
executable = %q
config_home = %q

[[models]]
id = "codex-sdk-test"
provider = "codex"
provider_model = "sdk-contract-model"
created = 0
`, port, runtimeRoot, fakePath, configHome))
}

type deferredScanner struct {
	mu        sync.Mutex
	values    []string
	retained  strings.Builder
	tail      string
	err       error
	total     int64
	maxTail   int
	forbidden bool
	overflow  bool
}

func newDeferredScanner(values []string) *deferredScanner {
	copyValues := make([]string, 0, len(values))
	maxTail := 0
	for _, value := range values {
		if value != "" {
			copyValues = append(copyValues, value)
			if len(value)-1 > maxTail {
				maxTail = len(value) - 1
			}
		}
	}
	return &deferredScanner{values: copyValues, maxTail: maxTail}
}

func (s *deferredScanner) Write(data []byte) (int, error) {
	if s == nil {
		return 0, io.ErrClosedPipe
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	prefixLength := len(data)
	if prefixLength > s.maxTail {
		prefixLength = s.maxTail
	}
	boundary := s.tail + string(data[:prefixLength])
	for _, forbidden := range s.values {
		if strings.Contains(boundary, forbidden) || bytes.Contains(data, []byte(forbidden)) {
			s.forbidden = true
			s.err = newError(categoryFailed)
		}
	}
	const limit = 64 << 10
	if s.retained.Len() < limit {
		remaining := limit - s.retained.Len()
		if remaining > len(data) {
			remaining = len(data)
		}
		_, _ = s.retained.Write(data[:remaining])
	}
	if s.total > limit || int64(len(data)) > int64(limit)-s.total {
		s.total = limit + 1
		s.overflow = true
		s.err = newError(categoryFailed)
	} else {
		s.total += int64(len(data))
	}
	if s.maxTail == 0 {
		s.tail = ""
	} else if len(data) >= s.maxTail {
		s.tail = string(data[len(data)-s.maxTail:])
	} else {
		combinedTail := s.tail + string(data)
		if len(combinedTail) > s.maxTail {
			combinedTail = combinedTail[len(combinedTail)-s.maxTail:]
		}
		s.tail = combinedTail
	}
	return len(data), nil
}

func (s *deferredScanner) Err() error {
	if s == nil {
		return newError(categoryFailed)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.err
}
