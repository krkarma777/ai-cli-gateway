package cli

import (
	"strings"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/initconfig"
)

func parseInitArgs(args []string) (initconfig.Options, error) {
	var options initconfig.Options
	seenSingleton := make(map[string]struct{})
	seenProvider := make(map[core.ProviderName]struct{})

	for index := 0; index < len(args); index++ {
		token := args[index]
		switch token {
		case "--non-interactive":
			if !markOnce(seenSingleton, token) {
				return initconfig.Options{}, initconfig.ErrUsage
			}
			options.NonInteractive = true
		case "--dry-run":
			if !markOnce(seenSingleton, token) {
				return initconfig.Options{}, initconfig.ErrUsage
			}
			options.DryRun = true
		case "--config":
			value, next, ok := initValue(args, index)
			if !ok || !markOnce(seenSingleton, token) {
				return initconfig.Options{}, initconfig.ErrUsage
			}
			options.ConfigPath = value
			index = next
		case "--provider", "--replace-provider":
			value, next, ok := initValue(args, index)
			if !ok {
				return initconfig.Options{}, initconfig.ErrUsage
			}
			name, ok := parseProviderName(value)
			if !ok {
				return initconfig.Options{}, initconfig.ErrUsage
			}
			if token == "--provider" {
				if _, duplicate := seenProvider[name]; duplicate {
					return initconfig.Options{}, initconfig.ErrUsage
				}
				seenProvider[name] = struct{}{}
				options.Providers = append(options.Providers, name)
			} else {
				options.ReplaceProviders = append(options.ReplaceProviders, name)
			}
			index = next
		case "--replace-model":
			value, next, ok := initValue(args, index)
			if !ok {
				return initconfig.Options{}, initconfig.ErrUsage
			}
			options.ReplaceModels = append(options.ReplaceModels, value)
			index = next
		case "--gateway-auth", "--gateway-key-file", "--gateway-key-env":
			value, next, ok := initValue(args, index)
			if !ok || !markOnce(seenSingleton, token) {
				return initconfig.Options{}, initconfig.ErrUsage
			}
			switch token {
			case "--gateway-auth":
				auth, valid := parseGatewayAuth(value)
				if !valid {
					return initconfig.Options{}, initconfig.ErrUsage
				}
				options.Gateway.Auth = auth
				options.Gateway.AuthSet = true
			case "--gateway-key-file":
				options.Gateway.KeyFile = initconfig.StringValue{Set: true, Value: value}
			case "--gateway-key-env":
				options.Gateway.KeyEnv = initconfig.StringValue{Set: true, Value: value}
			}
			index = next
		default:
			provider, field, ok := providerFlag(token)
			if !ok {
				return initconfig.Options{}, initconfig.ErrUsage
			}
			value, next, ok := initValue(args, index)
			if !ok {
				return initconfig.Options{}, initconfig.ErrUsage
			}
			if field != providerFieldModel && !markOnce(seenSingleton, token) {
				return initconfig.Options{}, initconfig.ErrUsage
			}
			if err := applyProviderFlag(&options, provider, field, value); err != nil {
				return initconfig.Options{}, err
			}
			index = next
		}
	}

	if err := initconfig.ValidateOptions(options); err != nil {
		return initconfig.Options{}, err
	}
	return options, nil
}

type providerField uint8

const (
	providerFieldExecutable providerField = iota + 1
	providerFieldEntrypoint
	providerFieldConfigHome
	providerFieldModel
	providerFieldAuth
)

func providerFlag(token string) (core.ProviderName, providerField, bool) {
	switch token {
	case "--codex-executable":
		return core.ProviderCodex, providerFieldExecutable, true
	case "--codex-entrypoint":
		return core.ProviderCodex, providerFieldEntrypoint, true
	case "--codex-config-home":
		return core.ProviderCodex, providerFieldConfigHome, true
	case "--codex-model":
		return core.ProviderCodex, providerFieldModel, true
	case "--claude-executable":
		return core.ProviderClaude, providerFieldExecutable, true
	case "--claude-entrypoint":
		return core.ProviderClaude, providerFieldEntrypoint, true
	case "--claude-config-home":
		return core.ProviderClaude, providerFieldConfigHome, true
	case "--claude-model":
		return core.ProviderClaude, providerFieldModel, true
	case "--claude-auth":
		return core.ProviderClaude, providerFieldAuth, true
	case "--gemini-executable":
		return core.ProviderGemini, providerFieldExecutable, true
	case "--gemini-entrypoint":
		return core.ProviderGemini, providerFieldEntrypoint, true
	case "--gemini-config-home":
		return core.ProviderGemini, providerFieldConfigHome, true
	case "--gemini-model":
		return core.ProviderGemini, providerFieldModel, true
	case "--gemini-auth":
		return core.ProviderGemini, providerFieldAuth, true
	default:
		return "", 0, false
	}
}

func applyProviderFlag(
	options *initconfig.Options,
	provider core.ProviderName,
	field providerField,
	value string,
) error {
	if options == nil {
		return initconfig.ErrUsage
	}
	if field == providerFieldModel {
		alias, providerModel, ok := strings.Cut(value, "=")
		if !ok || alias == "" || providerModel == "" {
			return initconfig.ErrUsage
		}
		options.Models = append(options.Models, initconfig.ModelMapping{
			ID:            alias,
			Provider:      provider,
			ProviderModel: providerModel,
		})
		return nil
	}

	if options.Provider == nil {
		options.Provider = make(map[core.ProviderName]initconfig.ProviderInput)
	}
	input := options.Provider[provider]
	switch field {
	case providerFieldExecutable:
		input.Executable = initconfig.StringValue{Set: true, Value: value}
	case providerFieldEntrypoint:
		input.Entrypoint = initconfig.StringValue{Set: true, Value: value}
	case providerFieldConfigHome:
		input.ConfigHome = initconfig.StringValue{Set: true, Value: value}
	case providerFieldAuth:
		auth, ok := parseProviderAuth(provider, value)
		if !ok {
			return initconfig.ErrUsage
		}
		input.Auth = auth
		input.AuthSet = true
	case providerFieldModel:
		return initconfig.ErrUsage
	default:
		return initconfig.ErrUsage
	}
	options.Provider[provider] = input
	return nil
}

func parseProviderName(value string) (core.ProviderName, bool) {
	switch core.ProviderName(value) {
	case core.ProviderCodex:
		return core.ProviderCodex, true
	case core.ProviderClaude:
		return core.ProviderClaude, true
	case core.ProviderGemini:
		return core.ProviderGemini, true
	default:
		return "", false
	}
}

func parseProviderAuth(
	provider core.ProviderName,
	value string,
) (initconfig.AuthID, bool) {
	auth := initconfig.AuthID(value)
	switch provider {
	case core.ProviderCodex:
		return "", false
	case core.ProviderClaude:
		return auth, auth == initconfig.AuthConfigHome ||
			auth == initconfig.AuthAnthropicAPIKey
	case core.ProviderGemini:
		return auth, auth == initconfig.AuthGeminiAPIKey ||
			auth == initconfig.AuthGoogleAPIKey ||
			auth == initconfig.AuthVertexServiceAccount
	default:
		return "", false
	}
}

func parseGatewayAuth(value string) (initconfig.GatewayAuthID, bool) {
	auth := initconfig.GatewayAuthID(value)
	switch auth {
	case initconfig.GatewayAuthFile,
		initconfig.GatewayAuthEnvironment,
		initconfig.GatewayAuthNone:
		return auth, true
	default:
		return "", false
	}
}

func initValue(args []string, index int) (string, int, bool) {
	next := index + 1
	if next >= len(args) || args[next] == "" || args[next][0] == '-' {
		return "", index, false
	}
	return args[next], next, true
}

func markOnce(seen map[string]struct{}, token string) bool {
	if _, exists := seen[token]; exists {
		return false
	}
	seen[token] = struct{}{}
	return true
}
