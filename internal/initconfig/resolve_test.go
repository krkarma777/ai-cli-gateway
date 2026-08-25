package initconfig

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestResolveNonInteractiveUsesExplicitValuesBeforeExistingValues(t *testing.T) {
	t.Parallel()

	existing := existingTestConfig()
	options := Options{
		NonInteractive: true,
		Providers:      []core.ProviderName{core.ProviderCodex},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderCodex: {
				Executable: setString(testAbsolutePath("new", "codex")),
				ConfigHome: setString(testAbsolutePath("new", "codex-home")),
			},
		},
		Models: []ModelMapping{{
			ID:            "codex-new",
			Provider:      core.ProviderCodex,
			ProviderModel: "gpt-new",
		}},
	}

	got, err := ResolveNonInteractive(
		options,
		&existing,
		testAbsolutePath("new-runtime"),
		testAbsolutePath("gateway.key"),
	)
	if err != nil {
		t.Fatalf("ResolveNonInteractive() error = %v", err)
	}

	want := DesiredState{
		NewRuntimeRoot:    testAbsolutePath("new-runtime"),
		SelectedProviders: []core.ProviderName{core.ProviderCodex},
		Providers: []ProviderPatch{{
			Name: core.ProviderCodex,
			Command: Optional[ProviderCommand]{
				Set: true,
				Value: ProviderCommand{
					Executable: testAbsolutePath("new", "codex"),
				},
			},
			ConfigHome: Optional[string]{Set: true, Value: testAbsolutePath("new", "codex-home")},
			CredentialEnv: Optional[[]string]{
				Set: true,
			},
		}},
		Models: []ModelMapping{{
			ID:            "codex-new",
			Provider:      core.ProviderCodex,
			ProviderModel: "gpt-new",
		}},
		ReplaceProviders: map[core.ProviderName]struct{}{},
		ReplaceModels:    map[string]struct{}{},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveNonInteractive() = %#v, want %#v", got, want)
	}
}

func TestResolveNonInteractivePreservesCompleteExistingProviderAndOmittedAliases(t *testing.T) {
	t.Parallel()

	existing := existingTestConfig()
	existing.Providers["claude"] = config.Provider{
		Executable:       testAbsolutePath("bin", "claude"),
		ConfigHome:       testAbsolutePath("homes", "claude"),
		CredentialEnv:    []string{"ANTHROPIC_API_KEY"},
		Concurrency:      7,
		QueueSize:        19,
		QueueBytes:       9_999,
		QueueTimeout:     config.Duration(17 * time.Second),
		ExecutionTimeout: config.Duration(3 * time.Minute),
	}
	existing.Models = append(existing.Models, config.Model{
		ID:            "claude-existing",
		Provider:      "claude",
		ProviderModel: "sonnet",
		Created:       123,
	})
	options := Options{
		NonInteractive: true,
		Providers:      []core.ProviderName{core.ProviderClaude},
	}

	got, err := ResolveNonInteractive(
		options,
		&existing,
		testAbsolutePath("new-runtime"),
		testAbsolutePath("gateway.key"),
	)
	if err != nil {
		t.Fatalf("ResolveNonInteractive() error = %v", err)
	}
	if len(got.Models) != 0 {
		t.Fatalf("Models = %#v, want omitted patch", got.Models)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("Providers = %#v", got.Providers)
	}
	provider := got.Providers[0]
	if provider.Command.Value.Executable != existing.Providers["claude"].Executable ||
		provider.ConfigHome.Value != existing.Providers["claude"].ConfigHome ||
		!reflect.DeepEqual(provider.CredentialEnv.Value, []string{"ANTHROPIC_API_KEY"}) {
		t.Fatalf("resolved provider = %#v", provider)
	}
	if got.Gateway.Set {
		t.Fatalf("Gateway = %#v, want preserved source patch", got.Gateway)
	}
	if len(got.Providers) != 1 || got.Providers[0].Name != core.ProviderClaude {
		t.Fatalf("unselected providers leaked into patch: %#v", got.Providers)
	}
}

func TestResolveNonInteractiveRequiresCompleteNewProviderAndAlias(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Options){
		"missing executable": func(options *Options) {
			input := options.Provider[core.ProviderClaude]
			input.Executable = StringValue{}
			options.Provider[core.ProviderClaude] = input
		},
		"missing config home": func(options *Options) {
			input := options.Provider[core.ProviderClaude]
			input.ConfigHome = StringValue{}
			options.Provider[core.ProviderClaude] = input
		},
		"missing auth": func(options *Options) {
			input := options.Provider[core.ProviderClaude]
			input.Auth = ""
			input.AuthSet = false
			options.Provider[core.ProviderClaude] = input
		},
		"missing requested alias": func(options *Options) {
			options.Models = nil
		},
	}

	for name, mutate := range tests {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			options := newClaudeOptions()
			mutate(&options)
			_, err := ResolveNonInteractive(
				options,
				nil,
				testAbsolutePath("runtime"),
				testAbsolutePath("gateway.key"),
			)
			if !errors.Is(err, ErrUsage) {
				t.Fatalf("ResolveNonInteractive() error = %v, want ErrUsage", err)
			}
		})
	}
}

func TestResolveNonInteractiveRequiresAnAliasAfterExistingMerge(t *testing.T) {
	t.Parallel()

	existing := existingTestConfig()
	existing.Models = nil
	options := Options{
		NonInteractive: true,
		Providers:      []core.ProviderName{core.ProviderCodex},
	}
	_, err := ResolveNonInteractive(
		options,
		&existing,
		testAbsolutePath("runtime"),
		testAbsolutePath("gateway.key"),
	)
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveNonInteractive() error = %v, want ErrUsage", err)
	}
}

func TestResolveNonInteractiveMapsEveryProviderAuthenticationShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider core.ProviderName
		auth     AuthID
		want     []string
	}{
		{"codex config home", core.ProviderCodex, AuthConfigHome, nil},
		{"claude config home", core.ProviderClaude, AuthConfigHome, nil},
		{"claude API key", core.ProviderClaude, AuthAnthropicAPIKey, []string{"ANTHROPIC_API_KEY"}},
		{"Gemini API key", core.ProviderGemini, AuthGeminiAPIKey, []string{"GEMINI_API_KEY"}},
		{"Google API key", core.ProviderGemini, AuthGoogleAPIKey, []string{"GOOGLE_API_KEY"}},
		{"Vertex service account", core.ProviderGemini, AuthVertexServiceAccount, []string{
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GOOGLE_CLOUD_PROJECT",
			"GOOGLE_CLOUD_LOCATION",
		}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := CredentialEnvironment(test.provider, test.auth)
			if err != nil {
				t.Fatalf("CredentialEnvironment() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("CredentialEnvironment() = %q, want %q", got, test.want)
			}
			if len(got) > 0 {
				got[0] = "MUTATED"
				again, err := CredentialEnvironment(test.provider, test.auth)
				if err != nil || reflect.DeepEqual(got, again) {
					t.Fatalf("CredentialEnvironment() retained mutable output: %q, %v", again, err)
				}
			}
		})
	}

	for _, invalid := range []struct {
		provider core.ProviderName
		auth     AuthID
	}{
		{core.ProviderCodex, AuthAnthropicAPIKey},
		{core.ProviderClaude, AuthGeminiAPIKey},
		{core.ProviderGemini, AuthConfigHome},
		{core.ProviderName("planted"), AuthConfigHome},
		{core.ProviderClaude, AuthID("planted")},
	} {
		if _, err := CredentialEnvironment(invalid.provider, invalid.auth); !errors.Is(err, ErrUsage) {
			t.Fatalf("CredentialEnvironment(%q, %q) error = %v, want ErrUsage", invalid.provider, invalid.auth, err)
		}
	}
}

func TestResolveNonInteractiveResolvesGatewayAuthentication(t *testing.T) {
	t.Parallel()

	defaultKey := testAbsolutePath("config", "gateway.key")
	explicitKey := testAbsolutePath("keys", "explicit.key")
	existing := existingTestConfig()
	tests := []struct {
		name     string
		existing *config.Config
		gateway  GatewayInput
		want     GatewayAuthPatch
	}{
		{
			name: "fresh defaults to unapproved file reuse",
			want: GatewayAuthPatch{Set: true, APIKeyFile: defaultKey},
		},
		{
			name:     "existing source is preserved",
			existing: &existing,
		},
		{
			name: "explicit file defaults beside config",
			gateway: GatewayInput{
				Auth:    GatewayAuthFile,
				AuthSet: true,
			},
			want: GatewayAuthPatch{Set: true, APIKeyFile: defaultKey},
		},
		{
			name: "explicit file path records reuse authority",
			gateway: GatewayInput{
				Auth:    GatewayAuthFile,
				AuthSet: true,
				KeyFile: setString(explicitKey),
			},
			want: GatewayAuthPatch{Set: true, APIKeyFile: explicitKey, KeyExplicit: true},
		},
		{
			name: "explicit existing file mode preserves path",
			existing: func() *config.Config {
				cloned := existing
				cloned.Server.APIKeyEnv = ""
				cloned.Server.APIKeyFile = testAbsolutePath("old", "gateway.key")
				return &cloned
			}(),
			gateway: GatewayInput{Auth: GatewayAuthFile, AuthSet: true},
			want: GatewayAuthPatch{
				Set:        true,
				APIKeyFile: testAbsolutePath("old", "gateway.key"),
			},
		},
		{
			name: "environment",
			gateway: GatewayInput{
				Auth:    GatewayAuthEnvironment,
				AuthSet: true,
				KeyEnv:  setString("AI_CLI_GATEWAY_API_KEY"),
			},
			want: GatewayAuthPatch{Set: true, APIKeyEnv: "AI_CLI_GATEWAY_API_KEY"},
		},
		{
			name:    "disabled",
			gateway: GatewayInput{Auth: GatewayAuthNone, AuthSet: true},
			want:    GatewayAuthPatch{Set: true},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			options := validOptions()
			options.Gateway = test.gateway
			got, err := ResolveNonInteractive(
				options,
				test.existing,
				testAbsolutePath("runtime"),
				defaultKey,
			)
			if err != nil {
				t.Fatalf("ResolveNonInteractive() error = %v", err)
			}
			if !reflect.DeepEqual(got.Gateway, test.want) {
				t.Fatalf("Gateway = %#v, want %#v", got.Gateway, test.want)
			}
		})
	}
}

func TestResolveNonInteractiveCarriesReplacementAuthorityWithoutComparing(t *testing.T) {
	t.Parallel()

	existing := existingTestConfig()
	options := validOptions()
	options.Provider[core.ProviderCodex] = ProviderInput{
		Executable: setString(testAbsolutePath("changed", "codex")),
		ConfigHome: setString(testAbsolutePath("changed", "codex-home")),
	}
	options.Models[0].ProviderModel = "changed-model"
	options.ReplaceProviders = []core.ProviderName{core.ProviderCodex}
	options.ReplaceModels = []string{"codex-local"}

	got, err := ResolveNonInteractive(
		options,
		&existing,
		testAbsolutePath("runtime"),
		testAbsolutePath("gateway.key"),
	)
	if err != nil {
		t.Fatalf("ResolveNonInteractive() error = %v", err)
	}
	if _, ok := got.ReplaceProviders[core.ProviderCodex]; !ok {
		t.Fatal("provider replacement authority was not carried")
	}
	if _, ok := got.ReplaceModels["codex-local"]; !ok {
		t.Fatal("model replacement authority was not carried")
	}
}

func TestResolveNonInteractiveDefensivelyCopiesEveryMutableValue(t *testing.T) {
	t.Parallel()

	existing := existingTestConfig()
	options := Options{
		NonInteractive: true,
		Providers:      []core.ProviderName{core.ProviderClaude},
		Models: []ModelMapping{{
			ID:            "claude-new",
			Provider:      core.ProviderClaude,
			ProviderModel: "sonnet",
		}},
		ReplaceProviders: []core.ProviderName{core.ProviderClaude},
		ReplaceModels:    []string{"claude-new"},
	}
	existing.Providers["claude"] = config.Provider{
		Executable:    testAbsolutePath("bin", "claude"),
		PrefixArgs:    nil,
		ConfigHome:    testAbsolutePath("homes", "claude"),
		CredentialEnv: []string{"ANTHROPIC_API_KEY"},
	}
	existing.Models = append(existing.Models, config.Model{
		ID:            "claude-existing",
		Provider:      "claude",
		ProviderModel: "sonnet",
	})

	got, err := ResolveNonInteractive(
		options,
		&existing,
		testAbsolutePath("runtime"),
		testAbsolutePath("gateway.key"),
	)
	if err != nil {
		t.Fatalf("ResolveNonInteractive() error = %v", err)
	}
	existingProvider := existing.Providers["claude"]
	existingProvider.CredentialEnv[0] = "MUTATED"
	existing.Providers["claude"] = existingProvider
	options.Providers[0] = core.ProviderCodex
	options.Models[0].ID = "mutated"
	options.ReplaceProviders[0] = core.ProviderCodex
	options.ReplaceModels[0] = "mutated"

	if got.SelectedProviders[0] != core.ProviderClaude ||
		got.Models[0].ID != "claude-new" ||
		got.Providers[0].CredentialEnv.Value[0] != "ANTHROPIC_API_KEY" {
		t.Fatalf("resolved state aliases caller memory: %#v", got)
	}
	if _, ok := got.ReplaceProviders[core.ProviderClaude]; !ok {
		t.Fatalf("ReplaceProviders = %#v", got.ReplaceProviders)
	}
	if _, ok := got.ReplaceModels["claude-new"]; !ok {
		t.Fatalf("ReplaceModels = %#v", got.ReplaceModels)
	}
}

func TestResolveNonInteractiveRejectsInteractiveOptions(t *testing.T) {
	t.Parallel()

	options := validOptions()
	options.NonInteractive = false
	_, err := ResolveNonInteractive(
		options,
		nil,
		testAbsolutePath("runtime"),
		testAbsolutePath("gateway.key"),
	)
	if !errors.Is(err, ErrUsage) {
		t.Fatalf("ResolveNonInteractive() error = %v, want ErrUsage", err)
	}
}

func newClaudeOptions() Options {
	return Options{
		NonInteractive: true,
		Providers:      []core.ProviderName{core.ProviderClaude},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderClaude: {
				Executable: setString(testAbsolutePath("bin", "claude")),
				ConfigHome: setString(testAbsolutePath("homes", "claude")),
				Auth:       AuthConfigHome,
				AuthSet:    true,
			},
		},
		Models: []ModelMapping{{
			ID:            "claude-local",
			Provider:      core.ProviderClaude,
			ProviderModel: "sonnet",
		}},
	}
}

func existingTestConfig() config.Config {
	return config.Config{
		Server: config.Server{
			Listen:    "127.0.0.1:8080",
			APIKeyEnv: "AI_CLI_GATEWAY_API_KEY",
		},
		Runtime: config.Runtime{Root: testAbsolutePath("existing-runtime")},
		Providers: map[string]config.Provider{
			"codex": {
				Executable:    testAbsolutePath("old", "codex"),
				ConfigHome:    testAbsolutePath("old", "codex-home"),
				CredentialEnv: nil,
			},
		},
		Models: []config.Model{{
			ID:            "codex-existing",
			Provider:      "codex",
			ProviderModel: "gpt-existing",
			Created:       99,
		}},
	}
}
