package initconfig

import (
	"context"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

var vertexCredentialProfile = []string{
	"GOOGLE_APPLICATION_CREDENTIALS",
	"GOOGLE_CLOUD_PROJECT",
	"GOOGLE_CLOUD_LOCATION",
}

func geminiDiscoveryProfile() discoveryProfile {
	return discoveryProfile{
		commandName:      "gemini",
		conventionalHome: ".gemini",
		authChoices:      geminiAuthChoices,
	}
}

func geminiAuthChoices(
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
		case credentialProfileMatches(current.CredentialEnv, []string{"GEMINI_API_KEY"}):
			appendAuthChoice(&choices, AuthGeminiAPIKey)
		case credentialProfileMatches(current.CredentialEnv, []string{"GOOGLE_API_KEY"}):
			appendAuthChoice(&choices, AuthGoogleAPIKey)
		case credentialProfileMatches(current.CredentialEnv, vertexCredentialProfile):
			appendAuthChoice(&choices, AuthVertexServiceAccount)
		}
	}

	geminiPresent, err := credentialPresent(ctx, lookup, "GEMINI_API_KEY")
	if err != nil {
		return nil, err
	}
	googlePresent, err := credentialPresent(ctx, lookup, "GOOGLE_API_KEY")
	if err != nil {
		return nil, err
	}
	applicationCredentialsPresent, err := credentialPresent(
		ctx,
		lookup,
		"GOOGLE_APPLICATION_CREDENTIALS",
	)
	if err != nil {
		return nil, err
	}
	projectPresent, err := credentialPresent(ctx, lookup, "GOOGLE_CLOUD_PROJECT")
	if err != nil {
		return nil, err
	}
	locationPresent, err := credentialPresent(ctx, lookup, "GOOGLE_CLOUD_LOCATION")
	if err != nil {
		return nil, err
	}

	if geminiPresent {
		appendAuthChoice(&choices, AuthGeminiAPIKey)
	}
	if googlePresent {
		appendAuthChoice(&choices, AuthGoogleAPIKey)
	}
	if applicationCredentialsPresent && projectPresent && locationPresent {
		appendAuthChoice(&choices, AuthVertexServiceAccount)
	}
	appendAuthChoice(&choices, AuthGeminiAPIKey)
	appendAuthChoice(&choices, AuthGoogleAPIKey)
	appendAuthChoice(&choices, AuthVertexServiceAccount)
	return choices, nil
}
