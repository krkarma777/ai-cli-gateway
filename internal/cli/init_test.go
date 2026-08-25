package cli

import (
	"bytes"
	"errors"
	"reflect"
	"runtime"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/initconfig"
)

func TestParseInitArgsAcceptsExactGrammar(t *testing.T) {
	t.Parallel()

	entrypoint := cliTestAbsolutePath("lib", "provider.mjs")
	tests := []struct {
		name string
		args []string
		want initconfig.Options
	}{
		{name: "bare interactive"},
		{
			name: "strict all-provider input",
			args: []string{
				"--config", "relative/config.toml",
				"--non-interactive",
				"--dry-run",
				"--provider", "codex",
				"--provider", "claude",
				"--provider", "gemini",
				"--codex-executable", cliTestAbsolutePath("bin", "codex"),
				"--codex-config-home", cliTestAbsolutePath("home", "codex"),
				"--claude-executable", cliTestAbsolutePath("bin", "claude"),
				"--claude-config-home", cliTestAbsolutePath("home", "claude"),
				"--claude-auth", "anthropic-api-key",
				"--gemini-executable", cliTestAbsolutePath("bin", "gemini"),
				"--gemini-config-home", cliTestAbsolutePath("home", "gemini"),
				"--gemini-auth", "vertex-service-account",
				"--codex-model", "codex-fast=gpt=fast",
				"--codex-model", "codex-deep=gpt-deep",
				"--claude-model", "claude-local=sonnet",
				"--gemini-model", "gemini-local=gemini-test",
				"--gateway-auth", "environment",
				"--gateway-key-env", "AI_CLI_GATEWAY_API_KEY",
				"--replace-provider", "codex",
				"--replace-model", "codex-fast",
			},
			want: initconfig.Options{
				ConfigPath:     "relative/config.toml",
				NonInteractive: true,
				DryRun:         true,
				Providers: []core.ProviderName{
					core.ProviderCodex,
					core.ProviderClaude,
					core.ProviderGemini,
				},
				Provider: map[core.ProviderName]initconfig.ProviderInput{
					core.ProviderCodex: {
						Executable: initconfig.StringValue{Set: true, Value: cliTestAbsolutePath("bin", "codex")},
						ConfigHome: initconfig.StringValue{Set: true, Value: cliTestAbsolutePath("home", "codex")},
					},
					core.ProviderClaude: {
						Executable: initconfig.StringValue{Set: true, Value: cliTestAbsolutePath("bin", "claude")},
						ConfigHome: initconfig.StringValue{Set: true, Value: cliTestAbsolutePath("home", "claude")},
						Auth:       initconfig.AuthAnthropicAPIKey,
						AuthSet:    true,
					},
					core.ProviderGemini: {
						Executable: initconfig.StringValue{Set: true, Value: cliTestAbsolutePath("bin", "gemini")},
						ConfigHome: initconfig.StringValue{Set: true, Value: cliTestAbsolutePath("home", "gemini")},
						Auth:       initconfig.AuthVertexServiceAccount,
						AuthSet:    true,
					},
				},
				Models: []initconfig.ModelMapping{
					{ID: "codex-fast", Provider: core.ProviderCodex, ProviderModel: "gpt=fast"},
					{ID: "codex-deep", Provider: core.ProviderCodex, ProviderModel: "gpt-deep"},
					{ID: "claude-local", Provider: core.ProviderClaude, ProviderModel: "sonnet"},
					{ID: "gemini-local", Provider: core.ProviderGemini, ProviderModel: "gemini-test"},
				},
				Gateway: initconfig.GatewayInput{
					Auth:    initconfig.GatewayAuthEnvironment,
					AuthSet: true,
					KeyEnv: initconfig.StringValue{
						Set:   true,
						Value: "AI_CLI_GATEWAY_API_KEY",
					},
				},
				ReplaceProviders: []core.ProviderName{core.ProviderCodex},
				ReplaceModels:    []string{"codex-fast"},
			},
		},
		{
			name: "file auth and entrypoints are carried without inspection",
			args: []string{
				"--provider", "codex",
				"--codex-entrypoint", entrypoint,
				"--codex-model", "codex-local=gpt-test",
				"--gateway-auth", "file",
				"--gateway-key-file", cliTestAbsolutePath("gateway.key"),
			},
			want: initconfig.Options{
				Providers: []core.ProviderName{core.ProviderCodex},
				Provider: map[core.ProviderName]initconfig.ProviderInput{
					core.ProviderCodex: {
						Entrypoint: initconfig.StringValue{Set: true, Value: entrypoint},
					},
				},
				Models: []initconfig.ModelMapping{{
					ID:            "codex-local",
					Provider:      core.ProviderCodex,
					ProviderModel: "gpt-test",
				}},
				Gateway: initconfig.GatewayInput{
					Auth:    initconfig.GatewayAuthFile,
					AuthSet: true,
					KeyFile: initconfig.StringValue{
						Set:   true,
						Value: cliTestAbsolutePath("gateway.key"),
					},
				},
			},
		},
		{
			name: "provider flags may precede selection",
			args: []string{
				"--claude-auth", "config-home",
				"--claude-model", "claude-local=sonnet",
				"--provider", "claude",
			},
			want: initconfig.Options{
				Providers: []core.ProviderName{core.ProviderClaude},
				Provider: map[core.ProviderName]initconfig.ProviderInput{
					core.ProviderClaude: {
						Auth:    initconfig.AuthConfigHome,
						AuthSet: true,
					},
				},
				Models: []initconfig.ModelMapping{{
					ID:            "claude-local",
					Provider:      core.ProviderClaude,
					ProviderModel: "sonnet",
				}},
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := parseInitArgs(test.args)
			if err != nil {
				t.Fatalf("parseInitArgs() error = %v", err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parseInitArgs() = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestParseInitArgsAcceptsEveryClosedAuthIdentifier(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{"--provider", "claude", "--claude-auth", "config-home"},
		{"--provider", "claude", "--claude-auth", "anthropic-api-key"},
		{"--provider", "gemini", "--gemini-auth", "gemini-api-key"},
		{"--provider", "gemini", "--gemini-auth", "google-api-key"},
		{"--provider", "gemini", "--gemini-auth", "vertex-service-account"},
		{"--provider", "codex", "--gateway-auth", "none"},
	}
	for _, args := range tests {
		args := args
		t.Run(args[len(args)-1], func(t *testing.T) {
			t.Parallel()
			if _, err := parseInitArgs(args); err != nil {
				t.Fatalf("parseInitArgs(%q) error = %v", args, err)
			}
		})
	}
}

func TestParseInitArgsRejectsEverythingOutsideExactGrammar(t *testing.T) {
	t.Parallel()

	validPath := cliTestAbsolutePath("safe")
	tests := map[string][]string{
		"unknown flag":                    {"--unknown", "value"},
		"positional":                      {"planted"},
		"equals syntax":                   {"--provider=codex"},
		"missing value":                   {"--provider"},
		"empty value":                     {"--provider", ""},
		"flag consumed as value":          {"--config", "--dry-run"},
		"duplicate config":                {"--config", "a", "--config", "b"},
		"duplicate noninteractive":        {"--non-interactive", "--non-interactive"},
		"duplicate dry run":               {"--dry-run", "--dry-run"},
		"unknown provider":                {"--provider", "planted"},
		"duplicate provider":              {"--provider", "codex", "--provider", "codex"},
		"provider flag without selection": {"--codex-executable", validPath},
		"model without selection":         {"--codex-model", "codex-local=gpt-test"},
		"duplicate executable": {
			"--provider", "codex",
			"--codex-executable", validPath,
			"--codex-executable", validPath,
		},
		"duplicate entrypoint": {
			"--provider", "codex",
			"--codex-entrypoint", validPath,
			"--codex-entrypoint", validPath,
		},
		"duplicate config home": {
			"--provider", "codex",
			"--codex-config-home", validPath,
			"--codex-config-home", validPath,
		},
		"duplicate auth": {
			"--provider", "claude",
			"--claude-auth", "config-home",
			"--claude-auth", "anthropic-api-key",
		},
		"codex auth":                         {"--provider", "codex", "--codex-auth", "config-home"},
		"wrong claude auth":                  {"--provider", "claude", "--claude-auth", "gemini-api-key"},
		"wrong gemini auth":                  {"--provider", "gemini", "--gemini-auth", "config-home"},
		"model without equals":               {"--provider", "codex", "--codex-model", "codex-local"},
		"model empty alias":                  {"--provider", "codex", "--codex-model", "=gpt-test"},
		"model empty provider value":         {"--provider", "codex", "--codex-model", "codex-local="},
		"duplicate model alias":              {"--provider", "codex", "--codex-model", "same=one", "--codex-model", "same=two"},
		"duplicate gateway auth":             {"--provider", "codex", "--gateway-auth", "none", "--gateway-auth", "none"},
		"unknown gateway auth":               {"--provider", "codex", "--gateway-auth", "planted"},
		"duplicate gateway file":             {"--provider", "codex", "--gateway-auth", "file", "--gateway-key-file", validPath, "--gateway-key-file", validPath},
		"duplicate gateway environment":      {"--provider", "codex", "--gateway-auth", "environment", "--gateway-key-env", "KEY", "--gateway-key-env", "KEY"},
		"gateway source without mode":        {"--provider", "codex", "--gateway-key-file", validPath},
		"gateway environment missing source": {"--provider", "codex", "--gateway-auth", "environment"},
		"gateway none with file":             {"--provider", "codex", "--gateway-auth", "none", "--gateway-key-file", validPath},
		"duplicate provider replacement":     {"--provider", "codex", "--replace-provider", "codex", "--replace-provider", "codex"},
		"unselected provider replacement":    {"--provider", "codex", "--replace-provider", "claude"},
		"unknown provider replacement":       {"--provider", "codex", "--replace-provider", "planted"},
		"duplicate model replacement":        {"--provider", "codex", "--codex-model", "one=gpt", "--replace-model", "one", "--replace-model", "one"},
		"unrequested model replacement":      {"--provider", "codex", "--replace-model", "missing"},
	}

	for name, args := range tests {
		args := args
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseInitArgs(args); !errors.Is(err, initconfig.ErrUsage) {
				t.Fatalf("parseInitArgs(%q) error = %v, want ErrUsage", args, err)
			}
		})
	}
}

func TestParseInitArgsDoesNotDispatchInitBeforeSafeStorageExists(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	if code := Run([]string{"init"}, &stdout, &stderr); code != 2 {
		t.Fatalf("Run(init) code = %d, want 2", code)
	}
	if stdout.Len() != 0 || stderr.String() != usage {
		t.Fatalf("Run(init) stdout = %q, stderr = %q", stdout.String(), stderr.String())
	}
}

func cliTestAbsolutePath(parts ...string) string {
	if runtime.GOOS == "windows" {
		path := `C:\AI CLI Gateway`
		for _, part := range parts {
			path += `\` + part
		}
		return path
	}
	path := "/ai-cli-gateway"
	for _, part := range parts {
		path += "/" + part
	}
	return path
}
