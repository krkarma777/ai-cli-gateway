// Package claude implements the pinned Claude Code CLI final-output adapter.
package claude

import (
	"context"
	"encoding/json"
	"errors"
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
	errBuildProvider = errors.New("claude build provider is invalid")
	errBuildModel    = errors.New("claude build model is invalid")
	errBuildConfig   = errors.New("claude build configuration is invalid")
	errBuildPrompt   = errors.New("claude build prompt is invalid")
	errBuildLimit    = errors.New("claude build prompt exceeds input limit")
	errBuildEnv      = errors.New("claude build environment is invalid")
)

const (
	maxPromptBytes         = 10_000_000
	envelopeMaxDepth       = 16
	envelopeMaxNumberBytes = 20
)

var fixedBuildArgs = [...]string{
	"-p",
	"--output-format",
	"json",
	"--no-session-persistence",
	"--safe-mode",
	"--setting-sources",
	"",
	"--tools",
	"",
	"--strict-mcp-config",
	"--permission-mode",
	"dontAsk",
	"--disable-slash-commands",
	"--no-chrome",
	"--model",
}

var requiredHelpTokens = [...]string{
	"--print",
	"--output-format",
	"--no-session-persistence",
	"--safe-mode",
	"--setting-sources",
	"--tools",
	"--strict-mcp-config",
	"--permission-mode",
	"--disable-slash-commands",
	"--no-chrome",
	"--model",
}

var claudeCapabilities = [...]string{
	"stdin_prompt",
	"json_envelope",
	"no_session_persistence",
	"empty_settings",
	"empty_tools",
	"safe_mode",
}

const (
	authAuthenticated = "authenticated"
	authMissing       = "missing"
	authUnknown       = "unknown"
)

// Adapter implements the Claude Code CLI adapter.
type Adapter struct{}

// New constructs a stateless Claude Code adapter.
func New() *Adapter {
	return &Adapter{}
}

// Name returns the fixed provider name.
func (*Adapter) Name() core.ProviderName {
	return core.ProviderClaude
}

// SupportedVersion returns the exact tested Claude Code CLI interval.
func (*Adapter) SupportedVersion() provider.Range {
	return provider.Range{
		MinInclusive: provider.Version{Major: 2, Minor: 1, Patch: 208},
		MaxExclusive: provider.Version{Major: 2, Minor: 2, Patch: 0},
	}
}

// Build constructs one isolated non-interactive Claude Code invocation.
func (*Adapter) Build(
	req core.Request,
	model core.Model,
	cfg provider.ProviderConfig,
	rt process.Runtime,
) (process.CommandSpec, error) {
	cfg = cfg.Clone()
	if model.Provider != core.ProviderClaude {
		return process.CommandSpec{}, errBuildProvider
	}
	if err := core.ValidateProviderModel(model.ProviderModel); err != nil {
		return process.CommandSpec{}, errBuildModel
	}
	withCredential, validCredentialConfig := credentialSelection(
		cfg.CredentialEnv,
	)
	if !validCredentialConfig {
		return process.CommandSpec{}, errBuildConfig
	}

	stdin := provider.BuildPrompt(req, provider.SchemaInline)
	if stdin == nil {
		return process.CommandSpec{}, errBuildPrompt
	}
	if len(stdin) > maxPromptBytes {
		return process.CommandSpec{}, errBuildLimit
	}
	env, err := buildEnvironment(cfg, rt.Dir, withCredential)
	if err != nil {
		return process.CommandSpec{}, errBuildEnv
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
	}, nil
}

// Parse returns only the final result string from one exact Claude envelope.
func (*Adapter) Parse(
	_ core.Request,
	result process.Result,
) (string, error) {
	outcome := parseEnvelope(result.Stdout)
	switch outcome.kind {
	case envelopeAuthRequired:
		return "", provider.NewProviderError(provider.ProviderErrorAuthRequired)
	case envelopeRateLimited:
		return "", provider.NewProviderError(provider.ProviderErrorRateLimited)
	case envelopeInvalid, envelopeSuccess, envelopeFailed:
	}

	if result.ExitCode != 0 {
		return "", provider.NewProviderError(provider.ProviderErrorFailed)
	}
	switch outcome.kind {
	case envelopeSuccess:
		return outcome.result, nil
	case envelopeFailed:
		return "", provider.NewProviderError(provider.ProviderErrorFailed)
	case envelopeInvalid, envelopeAuthRequired, envelopeRateLimited:
		return "", provider.NewProviderError(provider.ProviderErrorProtocol)
	}
	return "", provider.NewProviderError(provider.ProviderErrorProtocol)
}

// Probe runs the exact three-command Claude Code readiness contract.
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
		return buildHealth("", false, false, false, authUnknown)
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
	helpCommand := append([]string(nil), fixedBuildArgs[:]...)
	helpCommand = append(helpCommand, "sonnet", "--help")
	helpResult, helpRunErr := runProbe(
		ctx,
		cfg,
		runner,
		helpCommand,
	)
	authResult, authRunErr := runProbe(
		ctx,
		cfg,
		runner,
		[]string{"auth", "status"},
	)

	helpOK := helpRunErr == nil &&
		helpResult.ExitCode == 0 &&
		validHelpOutput(helpResult.Stdout)

	auth := authUnknown
	if authRunErr == nil {
		switch authResult.ExitCode {
		case 0:
			auth = authAuthenticated
		case 1:
			auth = authMissing
		}
	}
	return buildHealth(
		versionText,
		versionParsed,
		versionSupported,
		helpOK,
		auth,
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
			withCredential, ok := credentialSelection(cfg.CredentialEnv)
			if !ok {
				return process.CommandSpec{}, errBuildConfig
			}
			env, err := buildEnvironment(cfg, rt.Dir, withCredential)
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
) provider.Health {
	health := provider.Health{
		Provider: core.ProviderClaude,
		Version:  version,
		Auth:     auth,
	}
	if capabilitiesProven {
		health.Capabilities = append(
			[]string(nil),
			claudeCapabilities[:]...,
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
	if !capabilitiesProven {
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

type envelopeKind uint8

const (
	envelopeInvalid envelopeKind = iota
	envelopeSuccess
	envelopeFailed
	envelopeAuthRequired
	envelopeRateLimited
)

type envelopeOutcome struct {
	kind   envelopeKind
	result string
}

func parseEnvelope(stdout []byte) envelopeOutcome {
	value, err := safejson.Parse(stdout, safejson.Limits{
		MaxDepth:       envelopeMaxDepth,
		MaxNumberBytes: envelopeMaxNumberBytes,
	})
	if err != nil {
		return envelopeOutcome{}
	}
	root, ok := value.(map[string]any)
	if !ok {
		return envelopeOutcome{}
	}
	messageType, typeOK := root["type"].(string)
	subtype, subtypeOK := root["subtype"].(string)
	isError, isErrorOK := root["is_error"].(bool)
	if !typeOK || messageType != "result" ||
		!subtypeOK || !isErrorOK {
		return envelopeOutcome{}
	}

	if subtype == "success" {
		return parseSuccessEnvelope(root, isError)
	}
	if !terminalErrorSubtype(subtype) || !isError {
		return envelopeOutcome{}
	}
	if _, present := root["result"]; present {
		return envelopeOutcome{}
	}
	if _, present := root["api_error_status"]; present {
		return envelopeOutcome{}
	}
	if _, ok := root["errors"].([]any); !ok {
		return envelopeOutcome{}
	}
	return envelopeOutcome{kind: envelopeFailed}
}

func parseSuccessEnvelope(
	root map[string]any,
	isError bool,
) envelopeOutcome {
	result, resultOK := root["result"].(string)
	if !resultOK {
		return envelopeOutcome{}
	}
	if _, present := root["errors"]; present {
		return envelopeOutcome{}
	}

	status, statusPresent := root["api_error_status"]
	if !isError {
		if statusPresent {
			return envelopeOutcome{}
		}
		return envelopeOutcome{
			kind:   envelopeSuccess,
			result: result,
		}
	}
	if !statusPresent || status == nil {
		return envelopeOutcome{kind: envelopeFailed}
	}
	number, ok := status.(json.Number)
	if !ok || !isIntegerNumber(number.String()) {
		return envelopeOutcome{}
	}
	switch number.String() {
	case "401", "403":
		return envelopeOutcome{kind: envelopeAuthRequired}
	case "429":
		return envelopeOutcome{kind: envelopeRateLimited}
	default:
		return envelopeOutcome{kind: envelopeFailed}
	}
}

func terminalErrorSubtype(subtype string) bool {
	switch subtype {
	case "error_during_execution",
		"error_max_turns",
		"error_max_budget_usd",
		"error_max_structured_output_retries":
		return true
	default:
		return false
	}
}

func isIntegerNumber(value string) bool {
	if value == "" {
		return false
	}
	start := 0
	if value[0] == '-' {
		start = 1
	}
	if start == len(value) {
		return false
	}
	for index := start; index < len(value); index++ {
		if value[index] < '0' || value[index] > '9' {
			return false
		}
	}
	return true
}

func credentialSelection(names []string) (bool, bool) {
	switch {
	case len(names) == 0:
		return false, true
	case len(names) == 1 && names[0] == "ANTHROPIC_API_KEY":
		return true, true
	default:
		return false, false
	}
}

func buildEnvironment(
	cfg provider.ProviderConfig,
	runtimeDir string,
	withCredential bool,
) ([]string, error) {
	var requiredLookup []string
	if withCredential {
		requiredLookup = append(requiredLookup, "ANTHROPIC_API_KEY")
	}
	if runtime.GOOS == "windows" {
		requiredLookup = append(requiredLookup, "SystemRoot")
	}
	return provider.BuildEnv(provider.EnvSpec{
		Fixed: []provider.EnvVar{
			{
				Name:  "CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC",
				Value: "1",
			},
			{
				Name: "CLAUDE_CODE_DISABLE_OFFICIAL_" +
					"MARKETPLACE_AUTOINSTALL",
				Value: "1",
			},
			{
				Name:  "CLAUDE_CODE_DISABLE_TERMINAL_TITLE",
				Value: "1",
			},
			{Name: "CLAUDE_CODE_SKIP_PROMPT_HISTORY", Value: "1"},
			{Name: "CLAUDE_CODE_TMPDIR", Value: runtimeDir},
			{Name: "CLAUDE_CONFIG_DIR", Value: cfg.ConfigHome},
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
