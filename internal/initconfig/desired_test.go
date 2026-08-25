package initconfig

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestValidateOptionsAcceptsInteractiveAndStrictNonInteractiveShapes(t *testing.T) {
	t.Parallel()

	tests := map[string]Options{
		"bare interactive":        {},
		"complete noninteractive": validOptions(),
		"all providers": {
			NonInteractive: true,
			Providers: []core.ProviderName{
				core.ProviderCodex,
				core.ProviderClaude,
				core.ProviderGemini,
			},
			Provider: map[core.ProviderName]ProviderInput{
				core.ProviderCodex: {
					Executable: setString(testAbsolutePath("bin", "codex")),
					ConfigHome: setString(testAbsolutePath("homes", "codex")),
				},
				core.ProviderClaude: {
					Executable: setString(testAbsolutePath("bin", "claude")),
					ConfigHome: setString(testAbsolutePath("homes", "claude")),
					Auth:       AuthAnthropicAPIKey,
					AuthSet:    true,
				},
				core.ProviderGemini: {
					Executable: setString(testAbsolutePath("bin", "gemini")),
					ConfigHome: setString(testAbsolutePath("homes", "gemini")),
					Auth:       AuthVertexServiceAccount,
					AuthSet:    true,
				},
			},
			Models: []ModelMapping{
				{ID: "codex-local", Provider: core.ProviderCodex, ProviderModel: "gpt-test"},
				{ID: "claude-local", Provider: core.ProviderClaude, ProviderModel: "sonnet"},
				{ID: "gemini-local", Provider: core.ProviderGemini, ProviderModel: "gemini-test"},
			},
			Gateway: GatewayInput{
				Auth:    GatewayAuthEnvironment,
				AuthSet: true,
				KeyEnv:  setString("AI_CLI_GATEWAY_API_KEY"),
			},
		},
	}

	for name, options := range tests {
		options := options
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := ValidateOptions(options); err != nil {
				t.Fatalf("ValidateOptions() error = %v", err)
			}
		})
	}
}

func TestValidateOptionsRejectsUnknownDuplicateAndInconsistentInput(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*Options){
		"missing noninteractive provider": func(options *Options) {
			*options = Options{NonInteractive: true}
		},
		"unknown provider": func(options *Options) {
			options.Providers[0] = core.ProviderName("planted-provider")
		},
		"duplicate provider": func(options *Options) {
			options.Providers = append(options.Providers, core.ProviderCodex)
		},
		"provider input not selected": func(options *Options) {
			options.Provider[core.ProviderClaude] = ProviderInput{}
		},
		"unknown provider input": func(options *Options) {
			options.Provider[core.ProviderName("planted-provider")] = ProviderInput{}
		},
		"hidden executable value": func(options *Options) {
			input := options.Provider[core.ProviderCodex]
			input.Executable = StringValue{Value: testAbsolutePath("bin", "codex")}
			options.Provider[core.ProviderCodex] = input
		},
		"empty set executable": func(options *Options) {
			input := options.Provider[core.ProviderCodex]
			input.Executable = setString("")
			options.Provider[core.ProviderCodex] = input
		},
		"control in config home": func(options *Options) {
			input := options.Provider[core.ProviderCodex]
			input.ConfigHome = setString("/safe/\nplanted")
			options.Provider[core.ProviderCodex] = input
		},
		"codex auth flag": func(options *Options) {
			input := options.Provider[core.ProviderCodex]
			input.Auth = AuthConfigHome
			input.AuthSet = true
			options.Provider[core.ProviderCodex] = input
		},
		"hidden auth": func(options *Options) {
			input := options.Provider[core.ProviderCodex]
			input.Auth = AuthConfigHome
			options.Provider[core.ProviderCodex] = input
		},
		"claude gemini auth": func(options *Options) {
			options.Providers = append(options.Providers, core.ProviderClaude)
			options.Provider[core.ProviderClaude] = ProviderInput{
				Auth:    AuthGeminiAPIKey,
				AuthSet: true,
			}
		},
		"unknown auth": func(options *Options) {
			options.Providers = append(options.Providers, core.ProviderClaude)
			options.Provider[core.ProviderClaude] = ProviderInput{
				Auth:    AuthID("planted-auth"),
				AuthSet: true,
			}
		},
		"model provider not selected": func(options *Options) {
			options.Models[0].Provider = core.ProviderClaude
		},
		"duplicate model": func(options *Options) {
			options.Models = append(options.Models, options.Models[0])
		},
		"invalid model alias": func(options *Options) {
			options.Models[0].ID = "-invalid"
		},
		"invalid provider model": func(options *Options) {
			options.Models[0].ProviderModel = "-invalid"
		},
		"duplicate provider replacement": func(options *Options) {
			options.ReplaceProviders = []core.ProviderName{core.ProviderCodex, core.ProviderCodex}
		},
		"unselected provider replacement": func(options *Options) {
			options.ReplaceProviders = []core.ProviderName{core.ProviderClaude}
		},
		"duplicate model replacement": func(options *Options) {
			options.ReplaceModels = []string{"codex-local", "codex-local"}
		},
		"unrequested model replacement": func(options *Options) {
			options.ReplaceModels = []string{"missing-alias"}
		},
		"unknown gateway auth": func(options *Options) {
			options.Gateway = GatewayInput{Auth: GatewayAuthID("planted-auth"), AuthSet: true}
		},
		"hidden gateway auth": func(options *Options) {
			options.Gateway = GatewayInput{Auth: GatewayAuthFile}
		},
		"file auth with environment": func(options *Options) {
			options.Gateway = GatewayInput{
				Auth:    GatewayAuthFile,
				AuthSet: true,
				KeyEnv:  setString("AI_CLI_GATEWAY_API_KEY"),
			}
		},
		"environment auth without name": func(options *Options) {
			options.Gateway = GatewayInput{Auth: GatewayAuthEnvironment, AuthSet: true}
		},
		"environment auth with file": func(options *Options) {
			options.Gateway = GatewayInput{
				Auth:    GatewayAuthEnvironment,
				AuthSet: true,
				KeyEnv:  setString("AI_CLI_GATEWAY_API_KEY"),
				KeyFile: setString(testAbsolutePath("gateway.key")),
			}
		},
		"none auth with source": func(options *Options) {
			options.Gateway = GatewayInput{
				Auth:    GatewayAuthNone,
				AuthSet: true,
				KeyFile: setString(testAbsolutePath("gateway.key")),
			}
		},
		"source without auth": func(options *Options) {
			options.Gateway.KeyFile = setString(testAbsolutePath("gateway.key"))
		},
		"invalid config path": func(options *Options) {
			options.ConfigPath = "-planted"
		},
	}

	for name, mutate := range tests {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			options := validOptions()
			mutate(&options)
			if err := ValidateOptions(options); !errors.Is(err, ErrUsage) {
				t.Fatalf("ValidateOptions() error = %v, want ErrUsage", err)
			}
		})
	}
}

func TestValidateOptionsRejectsMoreThanConfigModelLimit(t *testing.T) {
	t.Parallel()

	options := validOptions()
	options.Models = make([]ModelMapping, maxDesiredModels+1)
	for index := range options.Models {
		options.Models[index] = ModelMapping{
			ID:            fmt.Sprintf("model-%04d", index),
			Provider:      core.ProviderCodex,
			ProviderModel: "gpt-test",
		}
	}
	if err := ValidateOptions(options); !errors.Is(err, ErrUsage) {
		t.Fatalf("ValidateOptions() error = %v, want ErrUsage", err)
	}
}

func TestValidateDesiredStateAcceptsCompleteClosedState(t *testing.T) {
	t.Parallel()

	state := validDesiredState()
	if err := ValidateDesiredState(state); err != nil {
		t.Fatalf("ValidateDesiredState() error = %v", err)
	}

	state.Gateway = GatewayAuthPatch{Set: true}
	if err := ValidateDesiredState(state); err != nil {
		t.Fatalf("ValidateDesiredState(disabled auth) error = %v", err)
	}

	state.Models = nil
	if err := ValidateDesiredState(state); err != nil {
		t.Fatalf("ValidateDesiredState(omitted existing models) error = %v", err)
	}
}

func TestValidateDesiredStateRejectsIncompleteOrInvalidState(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*DesiredState){
		"missing provider": func(state *DesiredState) {
			state.SelectedProviders = nil
			state.Providers = nil
			state.Models = nil
		},
		"duplicate selected provider": func(state *DesiredState) {
			state.SelectedProviders = append(state.SelectedProviders, core.ProviderCodex)
		},
		"unknown selected provider": func(state *DesiredState) {
			state.SelectedProviders[0] = core.ProviderName("planted-provider")
			state.Providers[0].Name = core.ProviderName("planted-provider")
			state.Models[0].Provider = core.ProviderName("planted-provider")
		},
		"missing provider patch": func(state *DesiredState) {
			state.Providers = nil
		},
		"duplicate provider patch": func(state *DesiredState) {
			state.Providers = append(state.Providers, state.Providers[0])
		},
		"unselected provider patch": func(state *DesiredState) {
			patch := state.Providers[0]
			patch.Name = core.ProviderClaude
			state.Providers = append(state.Providers, patch)
		},
		"missing command": func(state *DesiredState) {
			state.Providers[0].Command = Optional[ProviderCommand]{}
		},
		"missing config home": func(state *DesiredState) {
			state.Providers[0].ConfigHome = Optional[string]{}
		},
		"missing credential decision": func(state *DesiredState) {
			state.Providers[0].CredentialEnv = Optional[[]string]{}
		},
		"relative executable": func(state *DesiredState) {
			state.Providers[0].Command.Value.Executable = "relative"
		},
		"relative config home": func(state *DesiredState) {
			state.Providers[0].ConfigHome.Value = "relative"
		},
		"invalid credential profile": func(state *DesiredState) {
			state.Providers[0].CredentialEnv.Value = []string{"ANTHROPIC_API_KEY"}
		},
		"invalid runtime root": func(state *DesiredState) {
			state.NewRuntimeRoot = "relative"
		},
		"model for unselected provider": func(state *DesiredState) {
			state.Models[0].Provider = core.ProviderClaude
		},
		"duplicate model": func(state *DesiredState) {
			state.Models = append(state.Models, state.Models[0])
		},
		"replace unknown provider": func(state *DesiredState) {
			state.ReplaceProviders = map[core.ProviderName]struct{}{core.ProviderClaude: {}}
		},
		"replace unknown model": func(state *DesiredState) {
			state.ReplaceModels = map[string]struct{}{"missing-alias": {}}
		},
		"gateway sources conflict": func(state *DesiredState) {
			state.Gateway = GatewayAuthPatch{
				Set:        true,
				APIKeyEnv:  "AI_CLI_GATEWAY_API_KEY",
				APIKeyFile: testAbsolutePath("gateway.key"),
			}
		},
		"gateway hidden state": func(state *DesiredState) {
			state.Gateway = GatewayAuthPatch{APIKeyFile: testAbsolutePath("gateway.key")}
		},
		"gateway relative file": func(state *DesiredState) {
			state.Gateway = GatewayAuthPatch{Set: true, APIKeyFile: "relative"}
		},
		"gateway invalid environment": func(state *DesiredState) {
			state.Gateway = GatewayAuthPatch{Set: true, APIKeyEnv: "lowercase"}
		},
		"gateway explicit marker without file": func(state *DesiredState) {
			state.Gateway = GatewayAuthPatch{Set: true, KeyExplicit: true}
		},
	}

	for name, mutate := range tests {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			state := validDesiredState()
			mutate(&state)
			if err := ValidateDesiredState(state); !errors.Is(err, ErrPlan) {
				t.Fatalf("ValidateDesiredState() error = %v, want ErrPlan", err)
			}
		})
	}
}

func TestValidateDesiredStateRejectsMoreThanConfigModelLimit(t *testing.T) {
	t.Parallel()

	state := validDesiredState()
	state.Models = make([]ModelMapping, maxDesiredModels+1)
	for index := range state.Models {
		state.Models[index] = ModelMapping{
			ID:            fmt.Sprintf("model-%04d", index),
			Provider:      core.ProviderCodex,
			ProviderModel: "gpt-test",
		}
	}
	if err := ValidateDesiredState(state); !errors.Is(err, ErrPlan) {
		t.Fatalf("ValidateDesiredState() error = %v, want ErrPlan", err)
	}
}

func TestValidateDesiredStateRejectsAliasedMutableCollections(t *testing.T) {
	t.Parallel()

	state := validDesiredState()
	state.Providers[0].CredentialEnv.Value = []string{}
	if err := ValidateDesiredState(state); err != nil {
		t.Fatalf("ValidateDesiredState() error = %v", err)
	}

	state.Providers[0].CredentialEnv.Value = []string{"MUTATED"}
	if err := ValidateDesiredState(state); !errors.Is(err, ErrPlan) {
		t.Fatalf("mutated ValidateDesiredState() error = %v, want ErrPlan", err)
	}
}

func validOptions() Options {
	return Options{
		NonInteractive: true,
		Providers:      []core.ProviderName{core.ProviderCodex},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderCodex: {
				Executable: setString(testAbsolutePath("bin", "codex")),
				ConfigHome: setString(testAbsolutePath("homes", "codex")),
			},
		},
		Models: []ModelMapping{{
			ID:            "codex-local",
			Provider:      core.ProviderCodex,
			ProviderModel: "gpt-test",
		}},
	}
}

func validDesiredState() DesiredState {
	return DesiredState{
		NewRuntimeRoot:    testAbsolutePath("runtime"),
		SelectedProviders: []core.ProviderName{core.ProviderCodex},
		Providers: []ProviderPatch{{
			Name: core.ProviderCodex,
			Command: Optional[ProviderCommand]{
				Set: true,
				Value: ProviderCommand{
					Executable: testAbsolutePath("bin", "codex"),
				},
			},
			ConfigHome: Optional[string]{
				Set:   true,
				Value: testAbsolutePath("homes", "codex"),
			},
			CredentialEnv: Optional[[]string]{Set: true},
		}},
		Models: []ModelMapping{{
			ID:            "codex-local",
			Provider:      core.ProviderCodex,
			ProviderModel: "gpt-test",
		}},
		ReplaceProviders: map[core.ProviderName]struct{}{},
		ReplaceModels:    map[string]struct{}{},
	}
}

func setString(value string) StringValue {
	return StringValue{Set: true, Value: value}
}

func testAbsolutePath(parts ...string) string {
	joined := strings.Join(parts, "/")
	if runtime.GOOS == "windows" {
		return `C:\AI CLI Gateway\` + strings.ReplaceAll(joined, "/", `\`)
	}
	return "/ai-cli-gateway/" + joined
}
