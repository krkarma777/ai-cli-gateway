package initconfig

import (
	"context"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

func claudeDiscoveryProfile() discoveryProfile {
	return discoveryProfile{
		commandName:      "claude",
		environmentHome:  "CLAUDE_CONFIG_DIR",
		conventionalHome: ".claude",
		authChoices:      claudeAuthChoices,
	}
}

func claudeAuthChoices(
	ctx context.Context,
	input ProviderInput,
	current config.Provider,
	exists bool,
	lookup provider.LookupEnv,
) ([]AuthID, error) {
	var choices []AuthID
	if input.AuthSet {
		appendAuthChoice(&choices, input.Auth)
	}
	if exists {
		switch {
		case len(current.CredentialEnv) == 0:
			appendAuthChoice(&choices, AuthConfigHome)
		case credentialProfileMatches(
			current.CredentialEnv,
			[]string{"ANTHROPIC_API_KEY"},
		):
			appendAuthChoice(&choices, AuthAnthropicAPIKey)
		}
	}
	present, err := credentialPresent(ctx, lookup, "ANTHROPIC_API_KEY")
	if err != nil {
		return nil, err
	}
	if present {
		appendAuthChoice(&choices, AuthAnthropicAPIKey)
	}
	appendAuthChoice(&choices, AuthConfigHome)
	appendAuthChoice(&choices, AuthAnthropicAPIKey)
	return choices, nil
}
