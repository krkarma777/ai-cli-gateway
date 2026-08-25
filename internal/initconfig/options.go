// Package initconfig plans guided gateway configuration without mutating the
// filesystem.
package initconfig

import (
	"errors"
	"regexp"
	"unicode"
	"unicode/utf8"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

const maxDesiredModels = 1_024

var (
	// ErrUsage reports invalid or incomplete command input without retaining
	// user-controlled values.
	ErrUsage = errors.New("init input is invalid")
	// ErrCollision reports a semantic replacement that was not authorized.
	ErrCollision = errors.New("init replacement is not authorized")
	// ErrPlan reports an invalid source, desired state, or planned candidate.
	ErrPlan = errors.New("init plan is invalid")

	environmentNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// StringValue distinguishes an omitted flag from an explicitly supplied
// string.
type StringValue struct {
	Set   bool
	Value string
}

// AuthID identifies one supported provider authentication shape.
type AuthID string

// Supported provider authentication shapes.
const (
	AuthConfigHome           AuthID = "config-home"
	AuthAnthropicAPIKey      AuthID = "anthropic-api-key" // #nosec G101 -- public auth-mode identifier, not a credential.
	AuthGeminiAPIKey         AuthID = "gemini-api-key"
	AuthGoogleAPIKey         AuthID = "google-api-key"
	AuthVertexServiceAccount AuthID = "vertex-service-account"
)

// GatewayAuthID identifies one supported Gateway authentication source.
type GatewayAuthID string

// Supported Gateway authentication sources.
const (
	GatewayAuthFile        GatewayAuthID = "file"
	GatewayAuthEnvironment GatewayAuthID = "environment"
	GatewayAuthNone        GatewayAuthID = "none"
)

// ProviderInput contains raw provider-specific flags.
type ProviderInput struct {
	Executable StringValue
	Entrypoint StringValue
	ConfigHome StringValue
	Auth       AuthID
	AuthSet    bool
}

// GatewayInput contains raw Gateway-auth flags.
type GatewayInput struct {
	Auth    GatewayAuthID
	AuthSet bool
	KeyFile StringValue
	KeyEnv  StringValue
}

// Options is the closed raw input contract shared by strict flags and the
// later interactive flow.
type Options struct {
	ConfigPath       string
	NonInteractive   bool
	DryRun           bool
	Providers        []core.ProviderName
	Provider         map[core.ProviderName]ProviderInput
	Models           []ModelMapping
	Gateway          GatewayInput
	ReplaceProviders []core.ProviderName
	ReplaceModels    []string
}

// ValidateOptions validates the closed raw-input shape. Interactive options
// may omit providers so the prompt flow can collect them; non-interactive
// options may not.
func ValidateOptions(options Options) error {
	if options.ConfigPath != "" &&
		(options.ConfigPath[0] == '-' || !safeText(options.ConfigPath)) {
		return ErrUsage
	}

	selected, ok := uniqueProviders(options.Providers)
	if !ok || options.NonInteractive && len(selected) == 0 {
		return ErrUsage
	}

	for name, input := range options.Provider {
		if !knownProvider(name) {
			return ErrUsage
		}
		if _, exists := selected[name]; !exists {
			return ErrUsage
		}
		if !validStringValue(input.Executable) ||
			!validStringValue(input.Entrypoint) ||
			!validStringValue(input.ConfigHome) ||
			!validProviderAuth(name, input.Auth, input.AuthSet) {
			return ErrUsage
		}
	}

	models, ok := validateModelMappings(options.Models, selected)
	if !ok {
		return ErrUsage
	}
	if !validGatewayInput(options.Gateway) {
		return ErrUsage
	}

	replacements, ok := uniqueProviders(options.ReplaceProviders)
	if !ok {
		return ErrUsage
	}
	for name := range replacements {
		if _, exists := selected[name]; !exists {
			return ErrUsage
		}
	}

	seenModels := make(map[string]struct{}, len(options.ReplaceModels))
	for _, alias := range options.ReplaceModels {
		if !safeText(alias) {
			return ErrUsage
		}
		if _, duplicate := seenModels[alias]; duplicate {
			return ErrUsage
		}
		seenModels[alias] = struct{}{}
		if _, requested := models[alias]; !requested {
			return ErrUsage
		}
	}
	return nil
}

func validStringValue(value StringValue) bool {
	if !value.Set {
		return value.Value == ""
	}
	return safeText(value.Value)
}

func validProviderAuth(name core.ProviderName, auth AuthID, set bool) bool {
	if !set {
		return auth == ""
	}
	switch name {
	case core.ProviderCodex:
		return false
	case core.ProviderClaude:
		return auth == AuthConfigHome || auth == AuthAnthropicAPIKey
	case core.ProviderGemini:
		return auth == AuthGeminiAPIKey ||
			auth == AuthGoogleAPIKey ||
			auth == AuthVertexServiceAccount
	default:
		return false
	}
}

func validGatewayInput(input GatewayInput) bool {
	if !validStringValue(input.KeyFile) || !validStringValue(input.KeyEnv) {
		return false
	}
	if !input.AuthSet {
		return input.Auth == "" && !input.KeyFile.Set && !input.KeyEnv.Set
	}
	switch input.Auth {
	case GatewayAuthFile:
		return !input.KeyEnv.Set
	case GatewayAuthEnvironment:
		return !input.KeyFile.Set && input.KeyEnv.Set &&
			environmentNamePattern.MatchString(input.KeyEnv.Value)
	case GatewayAuthNone:
		return !input.KeyFile.Set && !input.KeyEnv.Set
	default:
		return false
	}
}

func validateModelMappings(
	models []ModelMapping,
	selected map[core.ProviderName]struct{},
) (map[string]struct{}, bool) {
	if len(models) > maxDesiredModels {
		return nil, false
	}
	coreModels := make([]core.Model, 0, len(models))
	aliases := make(map[string]struct{}, len(models))
	for _, model := range models {
		if _, exists := selected[model.Provider]; !exists {
			return nil, false
		}
		coreModels = append(coreModels, core.Model{
			ID:            model.ID,
			Provider:      model.Provider,
			ProviderModel: model.ProviderModel,
		})
		aliases[model.ID] = struct{}{}
	}
	if _, err := core.NewRegistry(coreModels); err != nil {
		return nil, false
	}
	return aliases, true
}

func uniqueProviders(
	providers []core.ProviderName,
) (map[core.ProviderName]struct{}, bool) {
	seen := make(map[core.ProviderName]struct{}, len(providers))
	for _, name := range providers {
		if !knownProvider(name) {
			return nil, false
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, false
		}
		seen[name] = struct{}{}
	}
	return seen, true
}

func knownProvider(name core.ProviderName) bool {
	switch name {
	case core.ProviderCodex, core.ProviderClaude, core.ProviderGemini:
		return true
	default:
		return false
	}
}

func safeText(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if !unicode.IsPrint(character) || unicode.IsControl(character) {
			return false
		}
	}
	return true
}
