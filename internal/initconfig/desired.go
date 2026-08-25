package initconfig

import (
	"bytes"
	"strconv"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/pelletier/go-toml/v2"
)

// Optional distinguishes an omitted semantic patch from a set zero value.
type Optional[T any] struct {
	Value T
	Set   bool
}

// ProviderCommand is the complete configured provider command. PrefixArgs is
// empty for a native executable and contains the supported Node entrypoint on
// Windows.
type ProviderCommand struct {
	Executable string
	PrefixArgs []string
}

// ProviderPatch is one complete selected-provider patch. Admission tuning is
// intentionally absent so a merge cannot overwrite it.
type ProviderPatch struct {
	Name          core.ProviderName
	Command       Optional[ProviderCommand]
	ConfigHome    Optional[string]
	CredentialEnv Optional[[]string]
}

// GatewayAuthPatch changes the Gateway authentication source when Set is true.
type GatewayAuthPatch struct {
	Set         bool
	APIKeyEnv   string
	APIKeyFile  string
	KeyExplicit bool
}

// DesiredState is the complete, validated semantic input to merge planning.
type DesiredState struct {
	NewRuntimeRoot    string
	Gateway           GatewayAuthPatch
	SelectedProviders []core.ProviderName
	Providers         []ProviderPatch
	Models            []ModelMapping
	ReplaceProviders  map[core.ProviderName]struct{}
	ReplaceModels     map[string]struct{}
}

// ModelMapping maps one public alias to a selected provider model.
type ModelMapping struct {
	ID            string
	Provider      core.ProviderName
	ProviderModel string
}

// ValidateDesiredState validates a resolved state through the production
// configuration decoder in addition to enforcing patch completeness.
func ValidateDesiredState(desired DesiredState) error {
	selected, ok := uniqueProviders(desired.SelectedProviders)
	if !ok || len(selected) == 0 || len(desired.Providers) != len(selected) {
		return ErrPlan
	}
	if !safeText(desired.NewRuntimeRoot) ||
		!validGatewayPatch(desired.Gateway) {
		return ErrPlan
	}

	providers := make(map[string]validationProvider, len(desired.Providers))
	seenProviders := make(map[core.ProviderName]struct{}, len(desired.Providers))
	for _, patch := range desired.Providers {
		if _, exists := selected[patch.Name]; !exists {
			return ErrPlan
		}
		if _, duplicate := seenProviders[patch.Name]; duplicate {
			return ErrPlan
		}
		seenProviders[patch.Name] = struct{}{}
		if !completeProviderPatch(patch) {
			return ErrPlan
		}
		providers[string(patch.Name)] = validationProvider{
			Executable:    patch.Command.Value.Executable,
			PrefixArgs:    append([]string(nil), patch.Command.Value.PrefixArgs...),
			ConfigHome:    patch.ConfigHome.Value,
			CredentialEnv: append([]string(nil), patch.CredentialEnv.Value...),
		}
	}

	aliases, ok := validateModelMappings(desired.Models, selected)
	if !ok {
		return ErrPlan
	}
	models := make([]config.Model, 0, len(desired.Models))
	for _, model := range desired.Models {
		models = append(models, config.Model{
			ID:            model.ID,
			Provider:      string(model.Provider),
			ProviderModel: model.ProviderModel,
		})
	}
	for name := range desired.ReplaceProviders {
		if _, exists := selected[name]; !exists {
			return ErrPlan
		}
	}
	for alias := range desired.ReplaceModels {
		if _, exists := aliases[alias]; !exists {
			return ErrPlan
		}
	}

	document := validationDocument{
		Runtime:   validationRuntime{Root: desired.NewRuntimeRoot},
		Providers: providers,
		Models:    models,
	}
	for name := range selected {
		if desiredHasProviderModel(desired.Models, name) {
			continue
		}
		alias := validationAlias(len(document.Models), aliases)
		aliases[alias] = struct{}{}
		document.Models = append(document.Models, config.Model{
			ID:            alias,
			Provider:      string(name),
			ProviderModel: "init-validation",
		})
	}
	if desired.Gateway.APIKeyEnv != "" {
		value := desired.Gateway.APIKeyEnv
		document.Server.APIKeyEnv = &value
	}
	if desired.Gateway.APIKeyFile != "" {
		value := desired.Gateway.APIKeyFile
		document.Server.APIKeyFile = &value
	}
	encoded, err := toml.Marshal(document)
	if err != nil {
		return ErrPlan
	}
	if _, err := config.Decode(bytes.NewReader(encoded)); err != nil {
		return ErrPlan
	}
	return nil
}

func desiredHasProviderModel(models []ModelMapping, provider core.ProviderName) bool {
	for _, model := range models {
		if model.Provider == provider {
			return true
		}
	}
	return false
}

func validationAlias(index int, existing map[string]struct{}) string {
	for {
		alias := "initconfig-validation-" + strconv.Itoa(index)
		if _, collision := existing[alias]; !collision {
			return alias
		}
		index++
	}
}

type validationDocument struct {
	Server    validationServer              `toml:"server"`
	Runtime   validationRuntime             `toml:"runtime"`
	Providers map[string]validationProvider `toml:"providers"`
	Models    []config.Model                `toml:"models"`
}

type validationServer struct {
	APIKeyEnv  *string `toml:"api_key_env,omitempty"`
	APIKeyFile *string `toml:"api_key_file,omitempty"`
}

type validationRuntime struct {
	Root string `toml:"root"`
}

type validationProvider struct {
	Executable    string   `toml:"executable"`
	PrefixArgs    []string `toml:"prefix_args,omitempty"`
	ConfigHome    string   `toml:"config_home"`
	CredentialEnv []string `toml:"credential_env,omitempty"`
}

func completeProviderPatch(patch ProviderPatch) bool {
	if !patch.Command.Set || !patch.ConfigHome.Set || !patch.CredentialEnv.Set {
		return false
	}
	if !safeText(patch.Command.Value.Executable) ||
		!safeText(patch.ConfigHome.Value) {
		return false
	}
	for _, argument := range patch.Command.Value.PrefixArgs {
		if !safeText(argument) {
			return false
		}
	}
	for _, name := range patch.CredentialEnv.Value {
		if !safeText(name) {
			return false
		}
	}
	return true
}

func validGatewayPatch(patch GatewayAuthPatch) bool {
	if !patch.Set {
		return patch.APIKeyEnv == "" && patch.APIKeyFile == "" && !patch.KeyExplicit
	}
	if patch.APIKeyEnv != "" && patch.APIKeyFile != "" {
		return false
	}
	if patch.APIKeyEnv != "" &&
		(!safeText(patch.APIKeyEnv) || !environmentNamePattern.MatchString(patch.APIKeyEnv)) {
		return false
	}
	if patch.APIKeyFile != "" && !safeText(patch.APIKeyFile) {
		return false
	}
	return !patch.KeyExplicit || patch.APIKeyFile != ""
}
