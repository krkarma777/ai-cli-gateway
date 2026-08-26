package initconfig

import (
	"context"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

func codexDiscoveryProfile() discoveryProfile {
	return discoveryProfile{
		commandName:      "codex",
		environmentHome:  "CODEX_HOME",
		conventionalHome: ".codex",
		authChoices:      codexAuthChoices,
	}
}

func codexAuthChoices(
	ctx context.Context,
	_ ProviderInput,
	_ config.Provider,
	_ bool,
	_ provider.LookupEnv,
) ([]AuthID, error) {
	if err := discoveryContextError(ctx); err != nil {
		return nil, err
	}
	return []AuthID{AuthConfigHome}, nil
}
