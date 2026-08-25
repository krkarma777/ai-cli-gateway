package initconfig

import (
	"bytes"
	"path/filepath"
	"sort"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/pelletier/go-toml/v2"
)

const (
	defaultProviderConcurrency            = 1
	defaultProviderQueueSize              = 32
	defaultProviderQueueBytes       int64 = 16_777_216
	defaultProviderQueueTimeout           = 30 * time.Second
	defaultProviderExecutionTimeout       = 5 * time.Minute
)

func renderProvider(
	name core.ProviderName,
	provider config.Provider,
) ([]byte, error) {
	if !knownProvider(name) || !safeText(provider.Executable) ||
		!safeText(provider.ConfigHome) {
		return nil, ErrPlan
	}
	for _, argument := range provider.PrefixArgs {
		if !safeText(argument) {
			return nil, ErrPlan
		}
	}
	for _, environmentName := range provider.CredentialEnv {
		if !safeText(environmentName) {
			return nil, ErrPlan
		}
	}
	if provider.Concurrency < 0 || provider.QueueSize < 0 ||
		provider.QueueBytes < 0 || provider.QueueTimeout < 0 ||
		provider.ExecutionTimeout < 0 {
		return nil, ErrPlan
	}

	var output bytes.Buffer
	output.WriteString("[providers.")
	output.WriteString(string(name))
	output.WriteString("]\n")
	if err := writeTOMLField(&output, "executable", provider.Executable); err != nil {
		return nil, err
	}
	if len(provider.PrefixArgs) > 0 {
		if err := writeTOMLField(&output, "prefix_args", provider.PrefixArgs); err != nil {
			return nil, err
		}
	}
	if err := writeTOMLField(&output, "config_home", provider.ConfigHome); err != nil {
		return nil, err
	}
	if len(provider.CredentialEnv) > 0 {
		if err := writeTOMLField(&output, "credential_env", provider.CredentialEnv); err != nil {
			return nil, err
		}
	}
	if provider.Concurrency != 0 && provider.Concurrency != defaultProviderConcurrency {
		if err := writeTOMLField(&output, "concurrency", provider.Concurrency); err != nil {
			return nil, err
		}
	}
	if provider.QueueSize != 0 && provider.QueueSize != defaultProviderQueueSize {
		if err := writeTOMLField(&output, "queue_size", provider.QueueSize); err != nil {
			return nil, err
		}
	}
	if provider.QueueBytes != 0 && provider.QueueBytes != defaultProviderQueueBytes {
		if err := writeTOMLField(&output, "queue_bytes", provider.QueueBytes); err != nil {
			return nil, err
		}
	}
	queueTimeout := time.Duration(provider.QueueTimeout)
	if queueTimeout != 0 && queueTimeout != defaultProviderQueueTimeout {
		if err := writeTOMLField(&output, "queue_timeout", queueTimeout.String()); err != nil {
			return nil, err
		}
	}
	executionTimeout := time.Duration(provider.ExecutionTimeout)
	if executionTimeout != 0 && executionTimeout != defaultProviderExecutionTimeout {
		if err := writeTOMLField(
			&output,
			"execution_timeout",
			executionTimeout.String(),
		); err != nil {
			return nil, err
		}
	}

	result := append([]byte(nil), output.Bytes()...)
	if !validRenderedProvider(name, provider.ConfigHome, result) {
		return nil, ErrPlan
	}
	return result, nil
}

func renderModel(model config.Model) ([]byte, error) {
	if model.Created < 0 {
		return nil, ErrPlan
	}
	provider := core.ProviderName(model.Provider)
	if !knownProvider(provider) {
		return nil, ErrPlan
	}
	if _, err := core.NewRegistry([]core.Model{{
		ID:            model.ID,
		Provider:      provider,
		ProviderModel: model.ProviderModel,
		Created:       model.Created,
	}}); err != nil {
		return nil, ErrPlan
	}

	var output bytes.Buffer
	output.WriteString("[[models]]\n")
	for _, field := range []struct {
		name  string
		value any
	}{
		{"id", model.ID},
		{"provider", model.Provider},
		{"provider_model", model.ProviderModel},
		{"created", model.Created},
	} {
		if err := writeTOMLField(&output, field.name, field.value); err != nil {
			return nil, err
		}
	}
	return append([]byte(nil), output.Bytes()...), nil
}

func renderGatewayAuth(server config.Server) ([]byte, error) {
	if server.APIKeyEnv != "" && server.APIKeyFile != "" {
		return nil, ErrPlan
	}
	patch := GatewayAuthPatch{
		Set:        true,
		APIKeyEnv:  server.APIKeyEnv,
		APIKeyFile: server.APIKeyFile,
	}
	if !validGatewayPatch(patch) ||
		server.APIKeyFile != "" && !filepath.IsAbs(server.APIKeyFile) {
		return nil, ErrPlan
	}

	var output bytes.Buffer
	output.WriteString("[server]\n")
	if server.APIKeyFile != "" {
		if err := writeTOMLField(&output, "api_key_file", server.APIKeyFile); err != nil {
			return nil, err
		}
	}
	if server.APIKeyEnv != "" {
		if err := writeTOMLField(&output, "api_key_env", server.APIKeyEnv); err != nil {
			return nil, err
		}
	}
	return append([]byte(nil), output.Bytes()...), nil
}

func renderFresh(desired DesiredState) ([]byte, error) {
	if err := ValidateDesiredState(desired); err != nil ||
		!desired.Gateway.Set || len(desired.Models) == 0 {
		return nil, ErrPlan
	}

	server := config.Server{
		APIKeyEnv:  desired.Gateway.APIKeyEnv,
		APIKeyFile: desired.Gateway.APIKeyFile,
	}
	serverFragment, err := renderGatewayAuth(server)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	output.Write(serverFragment)
	output.WriteByte('\n')
	output.WriteString("[runtime]\n")
	if err := writeTOMLField(&output, "root", desired.NewRuntimeRoot); err != nil {
		return nil, err
	}

	providers := append([]ProviderPatch(nil), desired.Providers...)
	sort.Slice(providers, func(left, right int) bool {
		return providers[left].Name < providers[right].Name
	})
	for _, patch := range providers {
		fragment, err := renderProvider(patch.Name, config.Provider{
			Executable:    patch.Command.Value.Executable,
			PrefixArgs:    append([]string(nil), patch.Command.Value.PrefixArgs...),
			ConfigHome:    patch.ConfigHome.Value,
			CredentialEnv: append([]string(nil), patch.CredentialEnv.Value...),
		})
		if err != nil {
			return nil, err
		}
		output.WriteByte('\n')
		output.Write(fragment)
	}

	models := append([]ModelMapping(nil), desired.Models...)
	sort.Slice(models, func(left, right int) bool {
		return models[left].ID < models[right].ID
	})
	for _, mapping := range models {
		fragment, err := renderModel(config.Model{
			ID:            mapping.ID,
			Provider:      string(mapping.Provider),
			ProviderModel: mapping.ProviderModel,
			Created:       0,
		})
		if err != nil {
			return nil, err
		}
		output.WriteByte('\n')
		output.Write(fragment)
	}

	candidate := append([]byte(nil), output.Bytes()...)
	if _, err := config.Decode(bytes.NewReader(candidate)); err != nil {
		return nil, ErrPlan
	}
	return candidate, nil
}

func writeTOMLField(output *bytes.Buffer, name string, value any) error {
	if output == nil {
		return ErrPlan
	}
	encoded, err := encodeTOMLValue(value)
	if err != nil {
		return err
	}
	output.WriteString(name)
	output.WriteString(" = ")
	output.Write(encoded)
	output.WriteByte('\n')
	return nil
}

func encodeTOMLValue(value any) ([]byte, error) {
	document, err := toml.Marshal(struct {
		Value any `toml:"value"`
	}{Value: value})
	if err != nil {
		return nil, ErrPlan
	}
	encoded, err := extractTOMLValue(document)
	if err != nil {
		return nil, ErrPlan
	}
	return encoded, nil
}

func validRenderedProvider(
	name core.ProviderName,
	runtimeRoot string,
	fragment []byte,
) bool {
	root, err := encodeTOMLValue(runtimeRoot)
	if err != nil {
		return false
	}
	modelID, err := encodeTOMLValue("initconfig-render-validation")
	if err != nil {
		return false
	}
	providerName, err := encodeTOMLValue(string(name))
	if err != nil {
		return false
	}
	providerModel, err := encodeTOMLValue("init-validation")
	if err != nil {
		return false
	}
	var document bytes.Buffer
	document.WriteString("[runtime]\nroot = ")
	document.Write(root)
	document.WriteString("\n\n")
	document.Write(fragment)
	document.WriteString("\n[[models]]\nid = ")
	document.Write(modelID)
	document.WriteString("\nprovider = ")
	document.Write(providerName)
	document.WriteString("\nprovider_model = ")
	document.Write(providerModel)
	document.WriteByte('\n')
	_, err = config.Decode(bytes.NewReader(document.Bytes()))
	return err == nil
}
