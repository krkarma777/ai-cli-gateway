// Package gemini implements the pinned Gemini CLI final-output adapter.
package gemini

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
	errBuildProvider = errors.New("gemini build provider is invalid")
	errBuildModel    = errors.New("gemini build model is invalid")
	errBuildConfig   = errors.New("gemini build configuration is invalid")
	errBuildPrompt   = errors.New("gemini build prompt is invalid")
	errBuildRuntime  = errors.New("gemini build runtime is invalid")
	errBuildEnv      = errors.New("gemini build environment is invalid")
)

const (
	envelopeMaxDepth       = 16
	envelopeMaxNumberBytes = 20

	authConfigured = "configured"
	authMissing    = "missing"
	authUnknown    = "unknown"

	geminiAPIKeyName         = "GEMINI_API_KEY"                 //nolint:gosec // Public environment name, not a credential.
	googleAPIKeyName         = "GOOGLE_API_KEY"                 //nolint:gosec // Public environment name, not a credential.
	googleCredentialsName    = "GOOGLE_APPLICATION_CREDENTIALS" //nolint:gosec // Public environment name, not a credential.
	googleCloudProjectName   = "GOOGLE_CLOUD_PROJECT"
	googleCloudLocationName  = "GOOGLE_CLOUD_LOCATION"
	geminiAPIKeyAuthType     = "gemini-api-key"
	vertexAIAuthType         = "vertex-ai"
	geminiSettingsDirectory  = ".gemini"
	geminiSettingsFile       = "settings.json"
	geminiSystemDefaultsFile = "system-defaults.json"
	geminiSystemSettingsFile = "system-settings.json"
)

var fixedBuildArgs = [...]string{
	"--output-format",
	"json",
	"--approval-mode",
	"default",
	"-e",
	"none",
	"--model",
}

var requiredHelpTokens = [...]string{
	"--output-format",
	"--approval-mode",
	"-e",
	"--extensions",
	"--model",
}

var geminiCapabilities = [...]string{
	"stdin_prompt",
	"json_envelope",
	"disposable_home",
	"system_settings_isolated",
	"empty_core_tools",
	"extensions_disabled",
}

// Adapter implements the Gemini CLI adapter.
type Adapter struct{}

// New constructs a stateless Gemini CLI adapter.
func New() *Adapter {
	return &Adapter{}
}

// Name returns the fixed provider name.
func (*Adapter) Name() core.ProviderName {
	return core.ProviderGemini
}

// SupportedVersion returns the exact tested Gemini CLI interval.
func (*Adapter) SupportedVersion() provider.Range {
	return provider.Range{
		MinInclusive: provider.Version{Major: 0, Minor: 53, Patch: 0},
		MaxExclusive: provider.Version{Major: 0, Minor: 54, Patch: 0},
	}
}

// Build constructs one isolated non-interactive Gemini CLI invocation.
func (*Adapter) Build(
	req core.Request,
	model core.Model,
	cfg provider.ProviderConfig,
	rt process.Runtime,
) (process.CommandSpec, error) {
	cfg = cfg.Clone()
	if model.Provider != core.ProviderGemini {
		return process.CommandSpec{}, errBuildProvider
	}
	if err := core.ValidateProviderModel(model.ProviderModel); err != nil {
		return process.CommandSpec{}, errBuildModel
	}
	profile, ok := selectCredentialProfile(cfg.CredentialEnv)
	if !ok || profile == credentialProfileNone {
		return process.CommandSpec{}, errBuildConfig
	}
	if !validRuntimeDirectory(rt.Dir) {
		return process.CommandSpec{}, errBuildRuntime
	}
	credentials, ok := snapshotCredentials(profile, cfg.LookupEnv)
	if !ok {
		return process.CommandSpec{}, errBuildEnv
	}
	stdin := provider.BuildPrompt(req, provider.SchemaInline)
	if stdin == nil {
		return process.CommandSpec{}, errBuildPrompt
	}
	env, err := buildEnvironment(cfg, rt.Dir, credentials)
	if err != nil {
		return process.CommandSpec{}, errBuildEnv
	}
	settings, err := buildSettings(profile)
	if err != nil {
		return process.CommandSpec{}, errBuildConfig
	}

	args := append([]string(nil), cfg.PrefixArgs...)
	args = append(args, fixedBuildArgs[:]...)
	args = append(args, model.ProviderModel)
	return process.CommandSpec{
		Executable: cfg.Executable,
		Args:       args,
		Env:        env,
		Dir:        rt.Dir,
		Stdin:      stdin,
		Files: []process.FileSpec{{
			Name: filepath.Join(geminiSettingsDirectory, geminiSettingsFile),
			Data: settings,
			Mode: 0o600,
		}},
	}, nil
}

// Parse returns only the final response from one closed Gemini JSON envelope.
func (*Adapter) Parse(_ core.Request, result process.Result) (string, error) {
	if result.ExitCode != 0 {
		return "", provider.NewProviderError(provider.ProviderErrorFailed)
	}
	value, err := safejson.Parse(result.Stdout, safejson.Limits{
		MaxDepth:       envelopeMaxDepth,
		MaxNumberBytes: envelopeMaxNumberBytes,
	})
	if err != nil {
		return "", provider.NewProviderError(provider.ProviderErrorProtocol)
	}
	root, ok := value.(map[string]any)
	if !ok {
		return "", provider.NewProviderError(provider.ProviderErrorProtocol)
	}
	for name, field := range root {
		switch name {
		case "response", "error":
		case "session_id":
			if _, ok := field.(string); !ok {
				return "", provider.NewProviderError(provider.ProviderErrorProtocol)
			}
		case "stats":
			if _, ok := field.(map[string]any); !ok {
				return "", provider.NewProviderError(provider.ProviderErrorProtocol)
			}
		case "warnings":
			if _, ok := field.([]any); !ok {
				return "", provider.NewProviderError(provider.ProviderErrorProtocol)
			}
		default:
			return "", provider.NewProviderError(provider.ProviderErrorProtocol)
		}
	}
	if errorValue, present := root["error"]; present {
		if _, ok := errorValue.(map[string]any); !ok {
			return "", provider.NewProviderError(provider.ProviderErrorProtocol)
		}
		if response, present := root["response"]; present {
			if _, ok := response.(string); !ok {
				return "", provider.NewProviderError(provider.ProviderErrorProtocol)
			}
		}
		return "", provider.NewProviderError(provider.ProviderErrorFailed)
	}
	response, ok := root["response"].(string)
	if !ok {
		return "", provider.NewProviderError(provider.ProviderErrorProtocol)
	}
	return response, nil
}

// Probe runs the exact two-command Gemini CLI readiness contract.
func (a *Adapter) Probe(
	ctx context.Context,
	cfg provider.ProviderConfig,
	runner provider.ProbeRunner,
) provider.Health {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = cfg.Clone()
	auth, authProblem := credentialHealth(cfg)
	if runner == nil {
		return buildHealth("", false, false, false, auth, authProblem)
	}

	versionResult, versionRunErr := runProbe(ctx, cfg, runner, []string{"--version"})
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
			auth,
			authProblem,
		)
	}
	helpResult, helpRunErr := runProbe(ctx, cfg, runner, []string{"--help"})
	helpOK := helpRunErr == nil &&
		helpResult.ExitCode == 0 &&
		validHelpOutput(helpResult.Stdout)
	return buildHealth(
		versionText,
		versionParsed,
		versionSupported,
		helpOK,
		auth,
		authProblem,
	)
}

func (a *Adapter) parseProbeVersion(
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
	return version.String(), true, a.SupportedVersion().Contains(version)
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
			if !validRuntimeDirectory(rt.Dir) {
				return process.CommandSpec{}, errBuildRuntime
			}
			env, err := buildEnvironment(cfg, rt.Dir, nil)
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

func validHelpOutput(output []byte) bool {
	if len(output) == 0 || !utf8.Valid(output) {
		return false
	}
	text := string(output)
	for _, token := range requiredHelpTokens {
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

func buildHealth(
	version string,
	versionParsed bool,
	versionSupported bool,
	capabilitiesProven bool,
	auth string,
	authProblem string,
) provider.Health {
	health := provider.Health{
		Provider: core.ProviderGemini,
		Version:  version,
		Auth:     auth,
	}
	if capabilitiesProven {
		health.Capabilities = append([]string(nil), geminiCapabilities[:]...)
	}
	switch {
	case !versionParsed:
		health.Problems = append(health.Problems, provider.ProblemVersionUnreadable)
	case !versionSupported:
		health.Problems = append(health.Problems, provider.ProblemVersionUnsupported)
	}
	if !capabilitiesProven {
		health.Problems = append(health.Problems, provider.ProblemCapabilityMissing)
	}
	if authProblem != "" {
		health.Problems = append(health.Problems, authProblem)
	}
	if len(health.Problems) == 0 {
		health.Status = provider.HealthReady
	} else {
		health.Status = provider.HealthNotReady
	}
	return health
}

type credentialProfile uint8

const (
	credentialProfileNone credentialProfile = iota
	credentialProfileGeminiAPIKey
	credentialProfileGoogleAPIKey
	credentialProfileServiceAccount
)

func selectCredentialProfile(names []string) (credentialProfile, bool) {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		switch name {
		case geminiAPIKeyName,
			googleAPIKeyName,
			googleCredentialsName,
			googleCloudProjectName,
			googleCloudLocationName:
		default:
			return credentialProfileNone, false
		}
		if _, duplicate := seen[name]; duplicate {
			return credentialProfileNone, false
		}
		seen[name] = struct{}{}
	}
	switch len(seen) {
	case 0:
		return credentialProfileNone, true
	case 1:
		if _, ok := seen[geminiAPIKeyName]; ok {
			return credentialProfileGeminiAPIKey, true
		}
		if _, ok := seen[googleAPIKeyName]; ok {
			return credentialProfileGoogleAPIKey, true
		}
	case 3:
		_, credentials := seen[googleCredentialsName]
		_, project := seen[googleCloudProjectName]
		_, location := seen[googleCloudLocationName]
		if credentials && project && location {
			return credentialProfileServiceAccount, true
		}
	}
	return credentialProfileNone, false
}

func credentialNames(profile credentialProfile) []string {
	switch profile {
	case credentialProfileGeminiAPIKey:
		return []string{geminiAPIKeyName}
	case credentialProfileGoogleAPIKey:
		return []string{googleAPIKeyName}
	case credentialProfileServiceAccount:
		return []string{
			googleCredentialsName,
			googleCloudProjectName,
			googleCloudLocationName,
		}
	case credentialProfileNone:
		return nil
	default:
		return nil
	}
}

func snapshotCredentials(
	profile credentialProfile,
	lookup provider.LookupEnv,
) ([]provider.EnvVar, bool) {
	names := credentialNames(profile)
	if len(names) == 0 || lookup == nil {
		return nil, false
	}
	values := make([]provider.EnvVar, 0, len(names))
	for _, name := range names {
		value, present := lookup(name)
		if !present || value == "" || strings.IndexByte(value, 0) >= 0 {
			return nil, false
		}
		if name == googleCredentialsName && !filepath.IsAbs(value) {
			return nil, false
		}
		values = append(values, provider.EnvVar{Name: name, Value: value})
	}
	return values, true
}

func credentialHealth(cfg provider.ProviderConfig) (string, string) {
	profile, ok := selectCredentialProfile(cfg.CredentialEnv)
	if !ok || profile == credentialProfileNone {
		return authUnknown, provider.ProblemAuthUnknown
	}
	if _, ok := snapshotCredentials(profile, cfg.LookupEnv); !ok {
		return authMissing, provider.ProblemCredentialMissing
	}
	return authConfigured, ""
}

func buildEnvironment(
	cfg provider.ProviderConfig,
	runtimeDir string,
	credentials []provider.EnvVar,
) ([]string, error) {
	settingsDir, defaultsPath, settingsPath, ok := disposableSettingsPaths(runtimeDir)
	if !ok {
		return nil, errBuildRuntime
	}
	_ = settingsDir
	fixed := []provider.EnvVar{
		{Name: "GEMINI_CLI_HOME", Value: runtimeDir},
		{Name: "GEMINI_CLI_SYSTEM_DEFAULTS_PATH", Value: defaultsPath},
		{Name: "GEMINI_CLI_SYSTEM_SETTINGS_PATH", Value: settingsPath},
		{Name: "HOME", Value: runtimeDir},
		{Name: "NO_COLOR", Value: "1"},
		{Name: "TEMP", Value: runtimeDir},
		{Name: "TMP", Value: runtimeDir},
		{Name: "TMPDIR", Value: runtimeDir},
	}
	fixed = append(fixed, credentials...)
	var requiredLookup []string
	if runtime.GOOS == "windows" {
		requiredLookup = []string{"SystemRoot"}
	}
	return provider.BuildEnv(provider.EnvSpec{
		Fixed:          fixed,
		SafePath:       cfg.SafePath,
		RequiredLookup: requiredLookup,
	}, cfg.LookupEnv)
}

func validRuntimeDirectory(path string) bool {
	return path != "" &&
		strings.IndexByte(path, 0) < 0 &&
		filepath.IsAbs(path) &&
		filepath.Clean(path) == path
}

func disposableSettingsPaths(runtimeDir string) (string, string, string, bool) {
	if !validRuntimeDirectory(runtimeDir) {
		return "", "", "", false
	}
	settingsDir := filepath.Join(runtimeDir, geminiSettingsDirectory)
	defaultsPath := filepath.Join(settingsDir, geminiSystemDefaultsFile)
	settingsPath := filepath.Join(settingsDir, geminiSystemSettingsFile)
	for _, path := range []string{defaultsPath, settingsPath} {
		relative, err := filepath.Rel(runtimeDir, path)
		if err != nil || relative == "." || filepath.IsAbs(relative) ||
			relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) ||
			filepath.Dir(path) != settingsDir {
			return "", "", "", false
		}
	}
	return settingsDir, defaultsPath, settingsPath, true
}

type settingsDocument struct {
	Advanced     advancedSettings     `json:"advanced"`
	Experimental experimentalSettings `json:"experimental"`
	HooksConfig  enabledSettings      `json:"hooksConfig"`
	MCP          mcpSettings          `json:"mcp"`
	MCPServers   emptySettings        `json:"mcpServers"`
	Privacy      privacySettings      `json:"privacy"`
	Security     securitySettings     `json:"security"`
	Skills       enabledSettings      `json:"skills"`
	Telemetry    telemetrySettings    `json:"telemetry"`
	Tools        toolsSettings        `json:"tools"`
}

type advancedSettings struct {
	IgnoreLocalEnv bool `json:"ignoreLocalEnv"`
}

type experimentalSettings struct {
	EnableAgents bool `json:"enableAgents"`
}

type enabledSettings struct {
	Enabled bool `json:"enabled"`
}

type mcpSettings struct {
	Allowed []string `json:"allowed"`
}

type emptySettings struct{}

type privacySettings struct {
	UsageStatisticsEnabled bool `json:"usageStatisticsEnabled"`
}

type securitySettings struct {
	Auth        authSettings    `json:"auth"`
	FolderTrust enabledSettings `json:"folderTrust"`
}

type authSettings struct {
	SelectedType string `json:"selectedType"`
}

type telemetrySettings struct {
	Enabled    bool `json:"enabled"`
	LogPrompts bool `json:"logPrompts"`
}

type toolsSettings struct {
	Core []string `json:"core"`
}

func buildSettings(profile credentialProfile) ([]byte, error) {
	authType := ""
	switch profile {
	case credentialProfileGeminiAPIKey:
		authType = geminiAPIKeyAuthType
	case credentialProfileGoogleAPIKey, credentialProfileServiceAccount:
		authType = vertexAIAuthType
	case credentialProfileNone:
		return nil, errBuildConfig
	default:
		return nil, errBuildConfig
	}
	return json.Marshal(settingsDocument{
		Advanced:     advancedSettings{IgnoreLocalEnv: true},
		Experimental: experimentalSettings{EnableAgents: false},
		HooksConfig:  enabledSettings{Enabled: false},
		MCP:          mcpSettings{Allowed: []string{}},
		MCPServers:   emptySettings{},
		Privacy:      privacySettings{UsageStatisticsEnabled: false},
		Security: securitySettings{
			Auth:        authSettings{SelectedType: authType},
			FolderTrust: enabledSettings{Enabled: false},
		},
		Skills:    enabledSettings{Enabled: false},
		Telemetry: telemetrySettings{Enabled: false, LogPrompts: false},
		Tools:     toolsSettings{Core: []string{}},
	})
}
