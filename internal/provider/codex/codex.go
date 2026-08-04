// Package codex implements the pinned Codex CLI final-output adapter.
package codex

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
	"github.com/krkarma777/ai-cli-gateway/internal/safejson"
)

var (
	errBuildProvider = errors.New("codex build provider is invalid")
	errBuildModel    = errors.New("codex build model is invalid")
	errBuildConfig   = errors.New("codex build configuration is invalid")
	errBuildPrompt   = errors.New("codex build prompt is invalid")
	errBuildEnv      = errors.New("codex build environment is invalid")
)

var fixedBuildArgs = [...]string{
	"--ask-for-approval",
	"never",
	"exec",
	"--ephemeral",
	"--ignore-user-config",
	"--ignore-rules",
	"--strict-config",
	"--sandbox",
	"read-only",
	"--skip-git-repo-check",
	"--color",
	"never",
	"--disable",
	"shell_tool",
	"--disable",
	"unified_exec",
	"--disable",
	"code_mode_host",
	"--disable",
	"apps",
	"--disable",
	"plugins",
	"--disable",
	"remote_plugin",
	"--disable",
	"hooks",
	"--disable",
	"multi_agent",
	"--disable",
	"browser_use",
	"--disable",
	"browser_use_external",
	"--disable",
	"computer_use",
	"--disable",
	"in_app_browser",
	"--disable",
	"image_generation",
	"--disable",
	"skill_search",
	"--disable",
	"skill_mcp_dependency_install",
	"--disable",
	"workspace_dependencies",
	"-c",
	`web_search="disabled"`,
	"--model",
}

var requiredExecHelpTokens = [...]string{
	"PROMPT",
	"-",
	"--disable",
	"-c",
	"--strict-config",
	"--sandbox",
	"--model",
	"--output-schema",
	"--color",
	"--ephemeral",
	"--ignore-user-config",
	"--ignore-rules",
	"--skip-git-repo-check",
}

var requiredHardeningFeatures = [...]string{
	"shell_tool",
	"unified_exec",
	"code_mode_host",
	"apps",
	"plugins",
	"remote_plugin",
	"hooks",
	"multi_agent",
	"browser_use",
	"browser_use_external",
	"computer_use",
	"in_app_browser",
	"image_generation",
	"skill_search",
	"skill_mcp_dependency_install",
	"workspace_dependencies",
}

var codexCapabilities = [...]string{
	"stdin_prompt",
	"ephemeral",
	"read_only",
	"never_approve",
	"schema_file",
	"feature_hardening",
}

var requiredDoctorCheckNames = [...]string{
	"auth.credentials",
	"config.load",
}

const (
	doctorMaxDepth       = 8
	doctorMaxNumberBytes = 20

	authAuthenticated = "authenticated"
	authMissing       = "missing"
	authUnknown       = "unknown"
)

// Adapter implements provider.Adapter for Codex CLI 0.146.x.
type Adapter struct{}

// New constructs a stateless Codex adapter.
func New() *Adapter {
	return &Adapter{}
}

// Name returns the fixed provider name.
func (*Adapter) Name() core.ProviderName {
	return core.ProviderCodex
}

// SupportedVersion returns the exact tested Codex CLI interval.
func (*Adapter) SupportedVersion() provider.Range {
	return provider.Range{
		MinInclusive: provider.Version{Major: 0, Minor: 146, Patch: 0},
		MaxExclusive: provider.Version{Major: 0, Minor: 147, Patch: 0},
	}
}

// Build constructs one isolated non-interactive Codex invocation.
func (*Adapter) Build(
	req core.Request,
	model core.Model,
	cfg provider.ProviderConfig,
	rt process.Runtime,
) (process.CommandSpec, error) {
	cfg = cfg.Clone()
	if model.Provider != core.ProviderCodex {
		return process.CommandSpec{}, errBuildProvider
	}
	if err := core.ValidateProviderModel(model.ProviderModel); err != nil {
		return process.CommandSpec{}, errBuildModel
	}
	if len(cfg.CredentialEnv) != 0 {
		return process.CommandSpec{}, errBuildConfig
	}

	stdin := provider.BuildPrompt(req, provider.SchemaFile)
	if stdin == nil {
		return process.CommandSpec{}, errBuildPrompt
	}
	env, err := buildEnvironment(cfg, rt.Dir)
	if err != nil {
		return process.CommandSpec{}, errBuildEnv
	}

	args := append([]string(nil), cfg.PrefixArgs...)
	args = append(args, fixedBuildArgs[:]...)
	args = append(args, model.ProviderModel)

	var files []process.FileSpec
	if req.Format.Type == core.FormatJSONSchema {
		schemaPath := filepath.Join(rt.Dir, "output-schema.json")
		args = append(args, "--output-schema", schemaPath)
		files = append(files, process.FileSpec{
			Name: "output-schema.json",
			Data: append([]byte(nil), req.Format.Schema...),
			Mode: 0o600,
		})
	}
	args = append(args, "-")

	return process.CommandSpec{
		Executable: cfg.Executable,
		Args:       args,
		Env:        env,
		Dir:        rt.Dir,
		Stdin:      stdin,
		Files:      files,
	}, nil
}

// Parse returns only a nonempty, valid UTF-8 final stdout.
func (*Adapter) Parse(
	_ core.Request,
	result process.Result,
) (string, error) {
	if result.ExitCode != 0 {
		return "", provider.NewProviderError(provider.ProviderErrorFailed)
	}
	if len(result.Stdout) == 0 || !utf8.Valid(result.Stdout) {
		return "", provider.NewProviderError(provider.ProviderErrorProtocol)
	}
	return string(result.Stdout), nil
}

// Probe runs the exact five-command Codex readiness contract.
func (a *Adapter) Probe(
	ctx context.Context,
	cfg provider.ProviderConfig,
	runner provider.ProbeRunner,
) provider.Health {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = cfg.Clone()
	if runner == nil {
		return buildHealth(
			"",
			false,
			false,
			false,
			authUnknown,
		)
	}

	versionResult, versionRunErr := runProbe(
		ctx,
		cfg,
		runner,
		[]string{"--version"},
	)
	versionText, versionParsed, versionSupported := a.parseProbeVersion(
		versionResult,
		versionRunErr,
	)
	if !versionParsed || !versionSupported {
		return buildHealth(
			versionText,
			versionParsed,
			versionSupported,
			false,
			authUnknown,
		)
	}
	helpResult, helpRunErr := runProbe(
		ctx,
		cfg,
		runner,
		[]string{"--ask-for-approval", "never", "exec", "--help"},
	)
	featuresResult, featuresRunErr := runProbe(
		ctx,
		cfg,
		runner,
		[]string{"features", "list"},
	)
	loginResult, loginRunErr := runProbe(
		ctx,
		cfg,
		runner,
		[]string{"login", "status"},
	)
	doctorResult, doctorRunErr := runProbe(
		ctx,
		cfg,
		runner,
		[]string{"doctor", "--json"},
	)

	helpOK := helpRunErr == nil &&
		helpResult.ExitCode == 0 &&
		validExecHelp(helpResult.Stdout)
	featuresOK := featuresRunErr == nil &&
		featuresResult.ExitCode == 0 &&
		validFeatureList(featuresResult.Stdout)

	auth := authUnknown
	switch {
	case loginRunErr != nil:
	case loginResult.ExitCode != 0:
		auth = authMissing
	default:
		auth = authAuthenticated
	}

	doctorOK := doctorRunErr == nil &&
		closedDoctorExitCode(doctorResult.ExitCode) &&
		validDoctorReport(doctorResult.Stdout)
	capabilitiesReady := helpOK && featuresOK && doctorOK

	return buildHealth(
		versionText,
		versionParsed,
		versionSupported,
		capabilitiesReady,
		auth,
	)
}

func (*Adapter) parseProbeVersion(
	result process.Result,
	runErr error,
) (string, bool, bool) {
	if runErr != nil || result.ExitCode != 0 {
		return "", false, false
	}
	version, err := provider.ParseVersion(string(result.Stdout))
	if err != nil {
		return "", false, false
	}
	return version.String(), true, (&Adapter{}).SupportedVersion().Contains(version)
}

func buildEnvironment(
	cfg provider.ProviderConfig,
	runtimeDir string,
) ([]string, error) {
	var requiredLookup []string
	if runtime.GOOS == "windows" {
		requiredLookup = []string{"SystemRoot"}
	}
	return provider.BuildEnv(provider.EnvSpec{
		Fixed: []provider.EnvVar{
			{Name: "CODEX_HOME", Value: cfg.ConfigHome},
			{Name: "HOME", Value: runtimeDir},
			{Name: "NO_COLOR", Value: "1"},
			{Name: "TEMP", Value: runtimeDir},
			{Name: "TMP", Value: runtimeDir},
			{Name: "TMPDIR", Value: runtimeDir},
		},
		SafePath:       cfg.SafePath,
		RequiredLookup: requiredLookup,
	}, cfg.LookupEnv)
}

func runProbe(
	ctx context.Context,
	cfg provider.ProviderConfig,
	runner provider.ProbeRunner,
	command []string,
) (process.Result, error) {
	return runner.RunProbe(
		ctx,
		func(rt process.Runtime) (process.CommandSpec, error) {
			if len(cfg.CredentialEnv) != 0 {
				return process.CommandSpec{}, errBuildConfig
			}
			env, err := buildEnvironment(cfg, rt.Dir)
			if err != nil {
				return process.CommandSpec{}, errBuildEnv
			}
			args := append([]string(nil), cfg.PrefixArgs...)
			args = append(args, command...)
			return process.CommandSpec{
				Executable: cfg.Executable,
				Args:       args,
				Env:        env,
				Dir:        rt.Dir,
			}, nil
		},
	)
}

func validExecHelp(output []byte) bool {
	if len(output) == 0 || !utf8.Valid(output) {
		return false
	}
	text := string(output)
	for _, token := range requiredExecHelpTokens {
		if !containsExactHelpToken(text, token) {
			return false
		}
	}
	return true
}

func containsExactHelpToken(output, token string) bool {
	searchFrom := 0
	for searchFrom <= len(output) {
		offset := strings.Index(output[searchFrom:], token)
		if offset < 0 {
			return false
		}
		start := searchFrom + offset
		end := start + len(token)
		beforeOK := start == 0
		if !beforeOK {
			previous, _ := utf8.DecodeLastRuneInString(output[:start])
			beforeOK = !helpIdentifierRune(previous)
		}
		afterOK := end == len(output)
		if !afterOK {
			next, _ := utf8.DecodeRuneInString(output[end:])
			afterOK = !helpIdentifierRune(next)
		}
		if beforeOK && afterOK {
			return true
		}
		searchFrom = start + 1
	}
	return false
}

func helpIdentifierRune(value rune) bool {
	return unicode.IsLetter(value) ||
		unicode.IsNumber(value) ||
		unicode.IsMark(value) ||
		unicode.Is(unicode.Pc, value) ||
		unicode.Is(unicode.Cf, value) ||
		value == '-'
}

func validFeatureList(output []byte) bool {
	if len(output) == 0 || !utf8.Valid(output) {
		return false
	}
	seen := make(map[string]string)
	for _, line := range strings.Split(string(output), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 3 || !validFeatureName(fields[0]) {
			return false
		}
		maturity := strings.Join(fields[1:len(fields)-1], " ")
		enabled := fields[len(fields)-1]
		if !validFeatureMaturity(maturity) ||
			(enabled != "true" && enabled != "false") {
			return false
		}
		if _, duplicate := seen[fields[0]]; duplicate {
			return false
		}
		seen[fields[0]] = maturity
	}
	for _, required := range requiredHardeningFeatures {
		maturity, present := seen[required]
		if !present || maturity == "removed" {
			return false
		}
	}
	return true
}

func validFeatureMaturity(maturity string) bool {
	switch maturity {
	case "stable", "experimental", "under development", "deprecated", "removed":
		return true
	default:
		return false
	}
}

func validFeatureName(name string) bool {
	if name == "" || name[0] < 'a' || name[0] > 'z' {
		return false
	}
	for index := 1; index < len(name); index++ {
		value := name[index]
		if (value >= 'a' && value <= 'z') ||
			(value >= '0' && value <= '9') ||
			value == '_' {
			continue
		}
		return false
	}
	return true
}

func validDoctorReport(output []byte) bool {
	parsed, err := safejson.Parse(output, safejson.Limits{
		MaxDepth:       doctorMaxDepth,
		MaxNumberBytes: doctorMaxNumberBytes,
	})
	if err != nil {
		return false
	}
	root, ok := parsed.(map[string]any)
	if !ok {
		return false
	}
	schemaVersion, ok := root["schemaVersion"].(json.Number)
	if !ok || schemaVersion.String() != "1" {
		return false
	}
	overallStatus, ok := root["overallStatus"].(string)
	if !ok || !closedDoctorStatus(overallStatus) {
		return false
	}
	checks, ok := root["checks"].(map[string]any)
	if !ok {
		return false
	}
	for _, name := range requiredDoctorCheckNames {
		check, ok := checks[name].(map[string]any)
		if !ok {
			return false
		}
		id, idOK := check["id"].(string)
		status, statusOK := check["status"].(string)
		if !idOK ||
			id != name ||
			!statusOK ||
			!closedDoctorStatus(status) ||
			status != "ok" {
			return false
		}
	}
	return true
}

func closedDoctorStatus(status string) bool {
	switch status {
	case "ok", "warn", "fail":
		return true
	default:
		return false
	}
}

func closedDoctorExitCode(exitCode int) bool {
	return exitCode == 0 || exitCode == 1
}

func buildHealth(
	version string,
	versionParsed bool,
	versionSupported bool,
	capabilitiesReady bool,
	auth string,
) provider.Health {
	health := provider.Health{
		Provider: core.ProviderCodex,
		Version:  version,
		Auth:     auth,
	}
	if capabilitiesReady {
		health.Capabilities = append(
			[]string(nil),
			codexCapabilities[:]...,
		)
	}

	switch {
	case !versionParsed:
		health.Problems = append(
			health.Problems,
			provider.ProblemVersionUnreadable,
		)
	case !versionSupported:
		health.Problems = append(
			health.Problems,
			provider.ProblemVersionUnsupported,
		)
	}
	if !capabilitiesReady {
		health.Problems = append(
			health.Problems,
			provider.ProblemCapabilityMissing,
		)
	}
	switch auth {
	case authAuthenticated:
	case authMissing:
		health.Problems = append(
			health.Problems,
			provider.ProblemAuthMissing,
		)
	default:
		health.Auth = authUnknown
		health.Problems = append(
			health.Problems,
			provider.ProblemAuthUnknown,
		)
	}

	switch {
	case hasKnownNotReadyProblem(health.Problems):
		health.Status = provider.HealthNotReady
	case health.Auth == authUnknown:
		health.Status = provider.HealthUnknown
	default:
		health.Status = provider.HealthReady
	}
	return health
}

func hasKnownNotReadyProblem(problems []string) bool {
	for _, problem := range problems {
		if problem != provider.ProblemAuthUnknown {
			return true
		}
	}
	return false
}
