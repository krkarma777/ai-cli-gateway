//go:build !windows

package initconfig

import "github.com/krkarma777/ai-cli-gateway/internal/core"

func resolvePlatformCommandCandidate(
	_ core.ProviderName,
	path string,
	explicitEntrypoint string,
	deps DiscoveryDependencies,
) (ProviderCommand, error) {
	if explicitEntrypoint != "" {
		return ProviderCommand{}, ErrPlan
	}
	inspections := commandInspectionSet{}
	defer inspections.close() //nolint:errcheck // Explicit finalization reports close failures.
	if _, err := inspections.open(
		path,
		CommandIdentityOnly,
		0,
		deps,
	); err != nil {
		return ProviderCommand{}, ErrPlan
	}
	if err := inspections.revalidateAndClose(); err != nil {
		return ProviderCommand{}, ErrPlan
	}
	return ProviderCommand{Executable: path}, nil
}
