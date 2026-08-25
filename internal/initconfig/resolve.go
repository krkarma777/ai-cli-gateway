package initconfig

import (
	"path/filepath"
	"runtime"
	"strings"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

// ResolveNonInteractive resolves only explicit flags and already-decoded
// existing configuration. It has no discovery, terminal, environment, or
// filesystem seam.
func ResolveNonInteractive(
	options Options,
	existing *config.Config,
	defaultRuntimeRoot string,
	defaultKeyPath string,
) (DesiredState, error) {
	if !options.NonInteractive {
		return DesiredState{}, ErrUsage
	}
	if err := ValidateOptions(options); err != nil {
		return DesiredState{}, err
	}

	providers := make([]ProviderPatch, 0, len(options.Providers))
	for _, name := range options.Providers {
		input := options.Provider[name]
		current, exists := existingProvider(existing, name)
		command, err := resolveProviderCommand(input, current, exists)
		if err != nil {
			return DesiredState{}, err
		}
		configHome, err := resolveConfigHome(input, current, exists)
		if err != nil {
			return DesiredState{}, err
		}
		credentials, err := resolveCredentials(name, input, current, exists)
		if err != nil {
			return DesiredState{}, err
		}
		providers = append(providers, ProviderPatch{
			Name:          name,
			Command:       Optional[ProviderCommand]{Set: true, Value: command},
			ConfigHome:    Optional[string]{Set: true, Value: configHome},
			CredentialEnv: Optional[[]string]{Set: true, Value: credentials},
		})
	}

	if !selectedProvidersHaveModels(options.Providers, options.Models, existing) {
		return DesiredState{}, ErrUsage
	}
	gateway, err := resolveGatewayAuth(options.Gateway, existing, defaultKeyPath)
	if err != nil {
		return DesiredState{}, err
	}

	desired := DesiredState{
		NewRuntimeRoot:    defaultRuntimeRoot,
		Gateway:           gateway,
		SelectedProviders: append([]core.ProviderName(nil), options.Providers...),
		Providers:         providers,
		Models:            append([]ModelMapping(nil), options.Models...),
		ReplaceProviders:  providerSet(options.ReplaceProviders),
		ReplaceModels:     stringSet(options.ReplaceModels),
	}
	if err := ValidateDesiredState(desired); err != nil {
		return DesiredState{}, ErrUsage
	}
	return cloneDesiredState(desired), nil
}

// CredentialEnvironment maps a closed provider authentication identifier to
// the exact environment-name profile stored in configuration.
func CredentialEnvironment(
	name core.ProviderName,
	auth AuthID,
) ([]string, error) {
	switch name {
	case core.ProviderCodex:
		if auth == AuthConfigHome {
			return nil, nil
		}
	case core.ProviderClaude:
		switch auth {
		case AuthConfigHome:
			return nil, nil
		case AuthAnthropicAPIKey:
			return []string{"ANTHROPIC_API_KEY"}, nil
		case AuthGeminiAPIKey, AuthGoogleAPIKey, AuthVertexServiceAccount:
			return nil, ErrUsage
		default:
			return nil, ErrUsage
		}
	case core.ProviderGemini:
		switch auth {
		case AuthGeminiAPIKey:
			return []string{"GEMINI_API_KEY"}, nil
		case AuthGoogleAPIKey:
			return []string{"GOOGLE_API_KEY"}, nil
		case AuthVertexServiceAccount:
			return []string{
				"GOOGLE_APPLICATION_CREDENTIALS",
				"GOOGLE_CLOUD_PROJECT",
				"GOOGLE_CLOUD_LOCATION",
			}, nil
		case AuthConfigHome, AuthAnthropicAPIKey:
			return nil, ErrUsage
		default:
			return nil, ErrUsage
		}
	}
	return nil, ErrUsage
}

func existingProvider(
	existing *config.Config,
	name core.ProviderName,
) (config.Provider, bool) {
	if existing == nil {
		return config.Provider{}, false
	}
	provider, ok := existing.Providers[string(name)]
	return provider, ok
}

func resolveProviderCommand(
	input ProviderInput,
	existing config.Provider,
	exists bool,
) (ProviderCommand, error) {
	if !input.Executable.Set {
		if input.Entrypoint.Set || !exists {
			return ProviderCommand{}, ErrUsage
		}
		return ProviderCommand{
			Executable: existing.Executable,
			PrefixArgs: append([]string(nil), existing.PrefixArgs...),
		}, nil
	}

	command := ProviderCommand{Executable: input.Executable.Value}
	if runtime.GOOS != "windows" {
		if input.Entrypoint.Set {
			return ProviderCommand{}, ErrUsage
		}
		return command, nil
	}

	isNode := strings.EqualFold(filepath.Base(input.Executable.Value), "node.exe")
	if isNode != input.Entrypoint.Set {
		return ProviderCommand{}, ErrUsage
	}
	if input.Entrypoint.Set {
		extension := strings.ToLower(filepath.Ext(input.Entrypoint.Value))
		if !filepath.IsAbs(input.Entrypoint.Value) ||
			(extension != ".js" && extension != ".mjs") {
			return ProviderCommand{}, ErrUsage
		}
		command.PrefixArgs = []string{input.Entrypoint.Value}
	}
	return command, nil
}

func resolveConfigHome(
	input ProviderInput,
	existing config.Provider,
	exists bool,
) (string, error) {
	if input.ConfigHome.Set {
		return input.ConfigHome.Value, nil
	}
	if !exists {
		return "", ErrUsage
	}
	return existing.ConfigHome, nil
}

func resolveCredentials(
	name core.ProviderName,
	input ProviderInput,
	existing config.Provider,
	exists bool,
) ([]string, error) {
	if name == core.ProviderCodex {
		return nil, nil
	}
	if input.AuthSet {
		return CredentialEnvironment(name, input.Auth)
	}
	if !exists {
		return nil, ErrUsage
	}
	return append([]string(nil), existing.CredentialEnv...), nil
}

func selectedProvidersHaveModels(
	selected []core.ProviderName,
	requested []ModelMapping,
	existing *config.Config,
) bool {
	models := make(map[string]core.ProviderName)
	if existing != nil {
		for _, model := range existing.Models {
			models[model.ID] = core.ProviderName(model.Provider)
		}
	}
	for _, model := range requested {
		models[model.ID] = model.Provider
	}
	for _, name := range selected {
		found := false
		for _, provider := range models {
			if provider == name {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func resolveGatewayAuth(
	input GatewayInput,
	existing *config.Config,
	defaultKeyPath string,
) (GatewayAuthPatch, error) {
	if !input.AuthSet {
		if existing != nil {
			return GatewayAuthPatch{}, nil
		}
		return GatewayAuthPatch{Set: true, APIKeyFile: defaultKeyPath}, nil
	}
	switch input.Auth {
	case GatewayAuthFile:
		path := defaultKeyPath
		if existing != nil && existing.Server.APIKeyFile != "" {
			path = existing.Server.APIKeyFile
		}
		if input.KeyFile.Set {
			path = input.KeyFile.Value
		}
		return GatewayAuthPatch{
			Set:         true,
			APIKeyFile:  path,
			KeyExplicit: input.KeyFile.Set,
		}, nil
	case GatewayAuthEnvironment:
		return GatewayAuthPatch{Set: true, APIKeyEnv: input.KeyEnv.Value}, nil
	case GatewayAuthNone:
		return GatewayAuthPatch{Set: true}, nil
	default:
		return GatewayAuthPatch{}, ErrUsage
	}
}

func providerSet(values []core.ProviderName) map[core.ProviderName]struct{} {
	result := make(map[core.ProviderName]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func cloneDesiredState(desired DesiredState) DesiredState {
	cloned := DesiredState{
		NewRuntimeRoot:    desired.NewRuntimeRoot,
		Gateway:           desired.Gateway,
		SelectedProviders: append([]core.ProviderName(nil), desired.SelectedProviders...),
		Models:            append([]ModelMapping(nil), desired.Models...),
		ReplaceProviders:  make(map[core.ProviderName]struct{}, len(desired.ReplaceProviders)),
		ReplaceModels:     make(map[string]struct{}, len(desired.ReplaceModels)),
	}
	cloned.Providers = make([]ProviderPatch, len(desired.Providers))
	for index, provider := range desired.Providers {
		cloned.Providers[index] = provider
		cloned.Providers[index].Command.Value.PrefixArgs = append(
			[]string(nil),
			provider.Command.Value.PrefixArgs...,
		)
		cloned.Providers[index].CredentialEnv.Value = append(
			[]string(nil),
			provider.CredentialEnv.Value...,
		)
	}
	for name := range desired.ReplaceProviders {
		cloned.ReplaceProviders[name] = struct{}{}
	}
	for alias := range desired.ReplaceModels {
		cloned.ReplaceModels[alias] = struct{}{}
	}
	return cloned
}
