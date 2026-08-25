package initconfig

import (
	"bytes"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestCanonicalRenderProviderUsesFixedMinimalFieldOrder(t *testing.T) {
	t.Parallel()

	provider := config.Provider{
		Executable:       testAbsolutePath("bin", "claude"),
		ConfigHome:       testAbsolutePath("homes", "claude"),
		CredentialEnv:    []string{"ANTHROPIC_API_KEY"},
		Concurrency:      7,
		QueueSize:        19,
		QueueBytes:       9_999,
		QueueTimeout:     config.Duration(45 * time.Second),
		ExecutionTimeout: config.Duration(7 * time.Minute),
	}
	got, err := renderProvider(core.ProviderClaude, provider)
	if err != nil {
		t.Fatalf("renderProvider() error = %v", err)
	}
	want := "[providers.claude]\n" +
		"executable = " + quotedTOMLTestValue(t, provider.Executable) + "\n" +
		"config_home = " + quotedTOMLTestValue(t, provider.ConfigHome) + "\n" +
		"credential_env = ['ANTHROPIC_API_KEY']\n" +
		"concurrency = 7\n" +
		"queue_size = 19\n" +
		"queue_bytes = 9999\n" +
		"queue_timeout = '45s'\n" +
		"execution_timeout = '7m0s'\n"
	if string(got) != want {
		t.Fatalf("renderProvider() =\n%s\nwant:\n%s", got, want)
	}
}

func TestCanonicalRenderProviderOmitsNormalizedDefaults(t *testing.T) {
	t.Parallel()

	provider := config.Provider{
		Executable:       testAbsolutePath("bin", "codex"),
		ConfigHome:       testAbsolutePath("homes", "codex"),
		Concurrency:      1,
		QueueSize:        32,
		QueueBytes:       16_777_216,
		QueueTimeout:     config.Duration(30 * time.Second),
		ExecutionTimeout: config.Duration(5 * time.Minute),
	}
	got, err := renderProvider(core.ProviderCodex, provider)
	if err != nil {
		t.Fatalf("renderProvider() error = %v", err)
	}
	want := "[providers.codex]\n" +
		"executable = " + quotedTOMLTestValue(t, provider.Executable) + "\n" +
		"config_home = " + quotedTOMLTestValue(t, provider.ConfigHome) + "\n"
	if string(got) != want {
		t.Fatalf("renderProvider() = %q, want %q", got, want)
	}
}

func TestCanonicalRenderModelEscapesThroughTOMLAndKeepsCreated(t *testing.T) {
	t.Parallel()

	model := config.Model{
		ID:            "codex-local",
		Provider:      "codex",
		ProviderModel: `model"quoted\path=variant`,
		Created:       42,
	}
	got, err := renderModel(model)
	if err != nil {
		t.Fatalf("renderModel() error = %v", err)
	}
	want := "[[models]]\n" +
		"id = 'codex-local'\n" +
		"provider = 'codex'\n" +
		`provider_model = 'model"quoted\path=variant'` + "\n" +
		"created = 42\n"
	if string(got) != want {
		t.Fatalf("renderModel() = %q, want %q", got, want)
	}
}

func TestCanonicalRenderGatewayAuthUsesOnlyClosedSourceFields(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		server config.Server
		want   string
	}{
		{
			name: "file",
			server: config.Server{
				APIKeyFile: testAbsolutePath("config", "gateway.key"),
			},
			want: "[server]\napi_key_file = " +
				quotedTOMLTestValue(t, testAbsolutePath("config", "gateway.key")) + "\n",
		},
		{
			name:   "environment",
			server: config.Server{APIKeyEnv: "AI_CLI_GATEWAY_API_KEY"},
			want:   "[server]\napi_key_env = 'AI_CLI_GATEWAY_API_KEY'\n",
		},
		{name: "disabled", want: "[server]\n"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := renderGatewayAuth(test.server)
			if err != nil {
				t.Fatalf("renderGatewayAuth() error = %v", err)
			}
			if string(got) != test.want {
				t.Fatalf("renderGatewayAuth() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRenderFreshProducesMinimalDeterministicValidatedDocument(t *testing.T) {
	t.Parallel()

	desired := DesiredState{
		NewRuntimeRoot: testAbsolutePath("runtime"),
		Gateway: GatewayAuthPatch{
			Set:        true,
			APIKeyFile: testAbsolutePath("config", "gateway.key"),
		},
		SelectedProviders: []core.ProviderName{
			core.ProviderGemini,
			core.ProviderCodex,
			core.ProviderClaude,
		},
		Providers: []ProviderPatch{
			completeTestProviderPatch(core.ProviderGemini, AuthGoogleAPIKey),
			completeTestProviderPatch(core.ProviderCodex, AuthConfigHome),
			completeTestProviderPatch(core.ProviderClaude, AuthConfigHome),
		},
		Models: []ModelMapping{
			{ID: "gemini-local", Provider: core.ProviderGemini, ProviderModel: "gemini-test"},
			{ID: "codex-z", Provider: core.ProviderCodex, ProviderModel: "gpt-z"},
			{ID: "codex-a", Provider: core.ProviderCodex, ProviderModel: "gpt-a"},
			{ID: "claude-local", Provider: core.ProviderClaude, ProviderModel: "sonnet"},
		},
		ReplaceProviders: map[core.ProviderName]struct{}{},
		ReplaceModels:    map[string]struct{}{},
	}

	got, err := renderFresh(desired)
	if err != nil {
		t.Fatalf("renderFresh() error = %v", err)
	}
	decoded, err := config.Decode(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("config.Decode(renderFresh()) error = %v\n%s", err, got)
	}
	if decoded.Runtime.Root != desired.NewRuntimeRoot ||
		decoded.Server.APIKeyFile != desired.Gateway.APIKeyFile {
		t.Fatalf("decoded fresh config = %#v", decoded)
	}
	if strings.Contains(string(got), "listen =") ||
		strings.Contains(string(got), "concurrency =") ||
		strings.Contains(string(got), "queue_size =") {
		t.Fatalf("fresh document contains defaulted fields:\n%s", got)
	}
	if len(got) == 0 || got[len(got)-1] != '\n' || bytes.Contains(got, []byte("\r\n")) {
		t.Fatalf("fresh document line endings are not canonical LF: %q", got)
	}

	providerPositions := []int{
		bytes.Index(got, []byte("[providers.claude]")),
		bytes.Index(got, []byte("[providers.codex]")),
		bytes.Index(got, []byte("[providers.gemini]")),
	}
	if !sort.IntsAreSorted(providerPositions) || providerPositions[0] < 0 {
		t.Fatalf("provider positions = %v\n%s", providerPositions, got)
	}
	modelPositions := []int{
		bytes.Index(got, []byte(`id = 'claude-local'`)),
		bytes.Index(got, []byte(`id = 'codex-a'`)),
		bytes.Index(got, []byte(`id = 'codex-z'`)),
		bytes.Index(got, []byte(`id = 'gemini-local'`)),
	}
	if !sort.IntsAreSorted(modelPositions) || modelPositions[0] < 0 {
		t.Fatalf("model positions = %v\n%s", modelPositions, got)
	}
	for _, model := range decoded.Models {
		if model.Created != 0 {
			t.Fatalf("new model Created = %d", model.Created)
		}
	}
}

func TestCanonicalRenderAndFreshRejectInvalidValues(t *testing.T) {
	t.Parallel()

	provider := config.Provider{
		Executable: testAbsolutePath("bin", "codex") + "\nPLANTED",
		ConfigHome: testAbsolutePath("homes", "codex"),
	}
	if _, err := renderProvider(core.ProviderCodex, provider); !errors.Is(err, ErrPlan) {
		t.Fatalf("renderProvider() error = %v, want ErrPlan", err)
	}
	if _, err := renderModel(config.Model{
		ID:            "codex-local",
		Provider:      "codex",
		ProviderModel: "-invalid",
	}); !errors.Is(err, ErrPlan) {
		t.Fatalf("renderModel() error = %v, want ErrPlan", err)
	}
	if _, err := renderGatewayAuth(config.Server{
		APIKeyEnv:  "AI_CLI_GATEWAY_API_KEY",
		APIKeyFile: testAbsolutePath("gateway.key"),
	}); !errors.Is(err, ErrPlan) {
		t.Fatalf("renderGatewayAuth() error = %v, want ErrPlan", err)
	}

	desired := validDesiredState()
	desired.Gateway = GatewayAuthPatch{Set: true, APIKeyFile: testAbsolutePath("gateway.key")}
	desired.Models = nil
	if _, err := renderFresh(desired); !errors.Is(err, ErrPlan) {
		t.Fatalf("renderFresh() error = %v, want ErrPlan", err)
	}
}

func completeTestProviderPatch(name core.ProviderName, auth AuthID) ProviderPatch {
	credentials, err := CredentialEnvironment(name, auth)
	if err != nil {
		panic(err)
	}
	return ProviderPatch{
		Name: name,
		Command: Optional[ProviderCommand]{
			Set: true,
			Value: ProviderCommand{
				Executable: testAbsolutePath("bin", string(name)),
			},
		},
		ConfigHome: Optional[string]{
			Set:   true,
			Value: testAbsolutePath("homes", string(name)),
		},
		CredentialEnv: Optional[[]string]{Set: true, Value: credentials},
	}
}

func quotedTOMLTestValue(t *testing.T, value string) string {
	t.Helper()
	encoded, err := encodeTOMLValue(value)
	if err != nil {
		t.Fatalf("encodeTOMLValue(%q) error = %v", value, err)
	}
	return string(encoded)
}

func TestCanonicalRenderOwnsReturnedBytes(t *testing.T) {
	t.Parallel()

	model := config.Model{
		ID:            "codex-local",
		Provider:      "codex",
		ProviderModel: "gpt-test",
	}
	first, err := renderModel(model)
	if err != nil {
		t.Fatal(err)
	}
	second, err := renderModel(model)
	if err != nil {
		t.Fatal(err)
	}
	first[0] = 'X'
	if reflect.DeepEqual(first, second) {
		t.Fatal("renderModel() outputs alias one another")
	}
}
