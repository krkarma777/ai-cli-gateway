package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	testAPIKeyEnv = "AI_CLI_GATEWAY_API_KEY"
	mib           = 1 << 20
)

type fixture struct {
	root       string
	executable string
	configHome string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	base := t.TempDir()
	executableName := "codex"
	if runtime.GOOS == "windows" {
		executableName += ".exe"
	}
	return fixture{
		root:       filepath.Join(base, "runtime"),
		executable: filepath.Join(base, "bin", executableName),
		configHome: filepath.Join(base, "config", "codex"),
	}
}

func (f fixture) document() string {
	return fmt.Sprintf(`[server]
listen = "127.0.0.1:8080"
api_key_env = %s

[runtime]
root = %s

[providers.codex]
executable = %s
config_home = %s

[[models]]
id = "codex-default"
provider = "codex"
provider_model = "gpt-test"
`,
		tomlQuote(testAPIKeyEnv),
		tomlQuote(f.root),
		tomlQuote(f.executable),
		tomlQuote(f.configHome),
	)
}

func (f fixture) providerDocument(provider, apiKeyEnv, credentialLine string) string {
	credentials := ""
	if credentialLine != "" {
		credentials = credentialLine + "\n"
	}
	return fmt.Sprintf(`[server]
api_key_env = %s

[runtime]
root = %s

[providers.%s]
executable = %s
config_home = %s
%s
[[models]]
id = "model"
provider = %s
provider_model = "provider-model"
`,
		tomlQuote(apiKeyEnv),
		tomlQuote(f.root),
		provider,
		tomlQuote(f.executable),
		tomlQuote(f.configHome),
		credentials,
		tomlQuote(provider),
	)
}

func tomlQuote(value string) string {
	return strconv.Quote(value)
}

func inServer(document, line string) string {
	return strings.Replace(document, "\n[runtime]", "\n"+line+"\n\n[runtime]", 1)
}

func inRuntime(document, line string) string {
	return strings.Replace(document, "\n[providers.codex]", "\n"+line+"\n\n[providers.codex]", 1)
}

func inProvider(document, line string) string {
	return strings.Replace(document, "\n[[models]]", "\n"+line+"\n\n[[models]]", 1)
}

func inModel(document, line string) string {
	return document + line + "\n"
}

func replaceLine(document, oldLine, newLine string) string {
	return strings.Replace(document, oldLine+"\n", newLine+"\n", 1)
}

func mustDecode(t *testing.T, document string) Config {
	t.Helper()
	cfg, err := Decode(strings.NewReader(document))
	if err != nil {
		t.Fatalf("Decode() error = %v", err)
	}
	return cfg
}

func requireDecodeError(t *testing.T, document string) {
	t.Helper()
	if _, err := Decode(strings.NewReader(document)); err == nil {
		t.Fatal("Decode() unexpectedly accepted invalid configuration")
	}
}

func requireDecodeErrorText(t *testing.T, document, want string) {
	t.Helper()
	_, err := Decode(strings.NewReader(document))
	if err == nil {
		t.Fatal("Decode() unexpectedly accepted invalid configuration")
	}
	if got := err.Error(); got != want {
		t.Fatalf("Decode() error = %q, want %q", got, want)
	}
}

func TestDecodeAppliesEveryDocumentedDefault(t *testing.T) {
	f := newFixture(t)
	cfg := mustDecode(t, f.document())

	wantServer := Server{
		Listen:            "127.0.0.1:8080",
		APIKeyEnv:         testAPIKeyEnv,
		HTTPBodyBytes:     1_048_576,
		InputBytes:        524_288,
		InstructionsBytes: 262_144,
		SchemaBytes:       32_768,
		HandlerLimit:      128,
		BodyReaderLimit:   32,
		MaxHeaderBytes:    16_384,
		ReadHeaderTimeout: Duration(5 * time.Second),
		BodyReadTimeout:   Duration(15 * time.Second),
		IdleTimeout:       Duration(60 * time.Second),
		ShutdownTimeout:   Duration(15 * time.Second),
	}
	wantRuntime := Runtime{
		Root:           f.root,
		TermGrace:      Duration(2 * time.Second),
		CleanupTimeout: Duration(5 * time.Second),
		StdoutBytes:    2_097_152,
		StderrBytes:    262_144,
		FinalBytes:     1_048_576,
	}
	wantProvider := Provider{
		Executable:       f.executable,
		ConfigHome:       f.configHome,
		Concurrency:      1,
		QueueSize:        32,
		QueueBytes:       16_777_216,
		QueueTimeout:     Duration(30 * time.Second),
		ExecutionTimeout: Duration(5 * time.Minute),
	}
	wantModels := []Model{{
		ID:            "codex-default",
		Provider:      "codex",
		ProviderModel: "gpt-test",
		Created:       0,
	}}

	if cfg.Server != wantServer {
		t.Errorf("Server = %#v, want %#v", cfg.Server, wantServer)
	}
	if cfg.Runtime != wantRuntime {
		t.Errorf("Runtime = %#v, want %#v", cfg.Runtime, wantRuntime)
	}
	if got := cfg.Providers["codex"]; !reflect.DeepEqual(got, wantProvider) {
		t.Errorf("Providers[codex] = %#v, want %#v", got, wantProvider)
	}
	if !reflect.DeepEqual(cfg.Models, wantModels) {
		t.Errorf("Models = %#v, want %#v", cfg.Models, wantModels)
	}
}

func TestNormalizeProviderOwnsSlicesAndPreservesDefaults(t *testing.T) {
	prefixArgs := []string{"/trusted/provider.mjs"}
	credentialEnv := []string{"ANTHROPIC_API_KEY"}
	provider, err := normalizeProvider(wireProvider{
		Executable:    "/trusted/node",
		PrefixArgs:    prefixArgs,
		ConfigHome:    "/trusted/config",
		CredentialEnv: credentialEnv,
	})
	if err != nil {
		t.Fatalf("normalizeProvider() error = %v", err)
	}

	prefixArgs[0] = "/mutated/provider.mjs"
	credentialEnv[0] = "MUTATED_API_KEY"
	if got := provider.PrefixArgs[0]; got != "/trusted/provider.mjs" {
		t.Fatalf("normalized PrefixArgs changed through wire slice: %q", got)
	}
	if got := provider.CredentialEnv[0]; got != "ANTHROPIC_API_KEY" {
		t.Fatalf("normalized CredentialEnv changed through wire slice: %q", got)
	}

	if provider.Concurrency != 1 ||
		provider.QueueSize != 32 ||
		provider.QueueBytes != 16_777_216 ||
		provider.QueueTimeout != Duration(30*time.Second) ||
		provider.ExecutionTimeout != Duration(5*time.Minute) {
		t.Fatalf("normalized provider defaults changed: %#v", provider)
	}
}

func TestLoadDecodesAFile(t *testing.T) {
	f := newFixture(t)
	path := filepath.Join(t.TempDir(), "gateway.toml")
	if err := os.WriteFile(path, []byte(f.document()), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.Server.APIKeyEnv != testAPIKeyEnv || cfg.Models[0].ID != "codex-default" {
		t.Fatalf("Load() = %#v", cfg)
	}
}

func TestDecodeRejectsUnknownAndDuplicateTOMLKeys(t *testing.T) {
	f := newFixture(t)
	tests := map[string]string{
		"top-level": inModel(f.document(), `[surprise]`),
		"server":    inServer(f.document(), `surprise = 1`),
		"runtime":   inRuntime(f.document(), `surprise = 1`),
		"provider":  inProvider(f.document(), `surprise = 1`),
		"model":     inModel(f.document(), `surprise = 1`),
		"duplicate": inServer(f.document(), `listen = "127.0.0.1:9090"`),
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			requireDecodeError(t, document)
		})
	}
}

func TestDecodeRejectsExplicitZeroAndNegativeDefaultedNumbers(t *testing.T) {
	f := newFixture(t)
	type numberField struct {
		name   string
		inject func(string, string) string
	}
	fields := []numberField{
		{"http_body_bytes", inServer},
		{"input_bytes", inServer},
		{"instructions_bytes", inServer},
		{"schema_bytes", inServer},
		{"handler_limit", inServer},
		{"body_reader_limit", inServer},
		{"max_header_bytes", inServer},
		{"stdout_bytes", inRuntime},
		{"stderr_bytes", inRuntime},
		{"final_bytes", inRuntime},
		{"concurrency", inProvider},
		{"queue_size", inProvider},
		{"queue_bytes", inProvider},
	}

	for _, field := range fields {
		for _, value := range []string{"0", "-1"} {
			t.Run(field.name+"_"+value, func(t *testing.T) {
				requireDecodeError(t, field.inject(f.document(), field.name+" = "+value))
			})
		}
	}
}

func TestDecodeRejectsExplicitZeroAndNegativeDefaultedDurations(t *testing.T) {
	f := newFixture(t)
	type durationField struct {
		name   string
		inject func(string, string) string
	}
	fields := []durationField{
		{"read_header_timeout", inServer},
		{"body_read_timeout", inServer},
		{"idle_timeout", inServer},
		{"shutdown_timeout", inServer},
		{"term_grace", inRuntime},
		{"cleanup_timeout", inRuntime},
		{"queue_timeout", inProvider},
		{"execution_timeout", inProvider},
	}

	for _, field := range fields {
		for _, value := range []string{"0s", "-1s"} {
			t.Run(field.name+"_"+value, func(t *testing.T) {
				line := field.name + " = " + tomlQuote(value)
				requireDecodeError(t, field.inject(f.document(), line))
			})
		}
	}
}

func TestDurationUsesExactGoDurationSyntax(t *testing.T) {
	f := newFixture(t)
	cfg := mustDecode(t, inServer(f.document(), `read_header_timeout = "1.5s"`))
	if cfg.Server.ReadHeaderTimeout != Duration(1500*time.Millisecond) {
		t.Fatalf("ReadHeaderTimeout = %v", cfg.Server.ReadHeaderTimeout)
	}

	for _, value := range []string{"1", "seconds", " 1s", "1s ", "NaN", "25h"} {
		t.Run(value, func(t *testing.T) {
			requireDecodeError(t, inServer(f.document(),
				"read_header_timeout = "+tomlQuote(value)))
		})
	}
}

func TestDecodeRejectsValuesAboveEveryStructuralCeiling(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		name   string
		inject func(string, string) string
		line   string
	}{
		{"http body", inServer, "http_body_bytes = 16777217"},
		{"input", inServer, "input_bytes = 16777217"},
		{"instructions", inServer, "instructions_bytes = 16777217"},
		{"schema", inServer, "schema_bytes = 1048577"},
		{"header", inServer, "max_header_bytes = 1048577"},
		{"handlers", inServer, "handler_limit = 4097"},
		{"body readers", inServer, "body_reader_limit = 257"},
		{"stdout", inRuntime, "stdout_bytes = 67108865"},
		{"stderr", inRuntime, "stderr_bytes = 16777217"},
		{"final", inRuntime, "final_bytes = 16777217"},
		{"concurrency", inProvider, "concurrency = 65"},
		{"queue entries", inProvider, "queue_size = 4097"},
		{"queue bytes", inProvider, "queue_bytes = 1073741825"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requireDecodeError(t, test.inject(f.document(), test.line))
		})
	}
}

func TestDecodeRejectsEveryDurationAbove24Hours(t *testing.T) {
	f := newFixture(t)
	type durationField struct {
		name   string
		inject func(string, string) string
	}
	fields := []durationField{
		{"read_header_timeout", inServer},
		{"body_read_timeout", inServer},
		{"idle_timeout", inServer},
		{"shutdown_timeout", inServer},
		{"term_grace", inRuntime},
		{"cleanup_timeout", inRuntime},
		{"queue_timeout", inProvider},
		{"execution_timeout", inProvider},
	}
	for _, field := range fields {
		t.Run(field.name, func(t *testing.T) {
			line := field.name + ` = "24h0m0.000000001s"`
			requireDecodeError(t, field.inject(f.document(), line))
		})
	}
}

func TestDecodeRejectsInvalidAggregateBounds(t *testing.T) {
	f := newFixture(t)
	tests := map[string]string{
		"input exceeds HTTP body":       inServer(f.document(), "input_bytes = 1048577"),
		"instructions exceed HTTP body": inServer(f.document(), "instructions_bytes = 1048577"),
		"schema exceeds HTTP body":      inServer(inServer(f.document(), "http_body_bytes = 16384"), "schema_bytes = 16385"),
		"body readers exceed handlers":  inServer(inServer(f.document(), "handler_limit = 1"), "body_reader_limit = 2"),
		"final exceeds stdout":          inRuntime(inRuntime(f.document(), "stdout_bytes = 1024"), "final_bytes = 1025"),
		"shutdown budget too short": inRuntime(
			inRuntime(inServer(f.document(), `shutdown_timeout = "12s"`), `term_grace = "2s"`),
			`cleanup_timeout = "5s"`),
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			requireDecodeError(t, document)
		})
	}
}

func TestDecodeAcceptsExactAggregateAndStructuralBounds(t *testing.T) {
	f := newFixture(t)
	document := f.document()
	for _, line := range []string{
		"http_body_bytes = 16777216",
		"input_bytes = 16777216",
		"instructions_bytes = 16777216",
		"schema_bytes = 1048576",
		"handler_limit = 4096",
		"body_reader_limit = 256",
		"max_header_bytes = 1048576",
		`read_header_timeout = "24h"`,
		`body_read_timeout = "24h"`,
		`idle_timeout = "24h"`,
		`shutdown_timeout = "24h"`,
	} {
		document = inServer(document, line)
	}
	for _, line := range []string{
		`term_grace = "1s"`,
		`cleanup_timeout = "1s"`,
		"stdout_bytes = 67108864",
		"stderr_bytes = 16777216",
		"final_bytes = 16777216",
	} {
		document = inRuntime(document, line)
	}
	for _, line := range []string{
		"concurrency = 64",
		"queue_size = 4096",
		"queue_bytes = 1073741824",
		`queue_timeout = "24h"`,
		`execution_timeout = "24h"`,
	} {
		document = inProvider(document, line)
	}

	mustDecode(t, document)
}

func TestDecodeRejectsInvalidListeners(t *testing.T) {
	f := newFixture(t)
	values := []string{
		"",
		"0.0.0.0:8080",
		"192.0.2.1:8080",
		"localhost:8080",
		"127.0.0.1",
		"127.0.0.1:0",
		"127.0.0.1:65536",
		"127.0.0.1:http",
		"127.0.0.1:+8080",
		"[::]:8080",
	}
	for _, value := range values {
		t.Run(value, func(t *testing.T) {
			document := replaceLine(f.document(), `listen = "127.0.0.1:8080"`,
				"listen = "+tomlQuote(value))
			requireDecodeError(t, document)
		})
	}
}

func TestDecodeAcceptsIPv4AndIPv6LoopbackLiterals(t *testing.T) {
	f := newFixture(t)
	for _, value := range []string{"127.0.0.1:1", "127.255.255.254:65535", "[::1]:8080"} {
		t.Run(value, func(t *testing.T) {
			document := replaceLine(f.document(), `listen = "127.0.0.1:8080"`,
				"listen = "+tomlQuote(value))
			mustDecode(t, document)
		})
	}
}

func TestDecodeRequiresAbsolutePaths(t *testing.T) {
	f := newFixture(t)
	tests := map[string]string{
		"runtime relative": replaceLine(f.document(), "root = "+tomlQuote(f.root), `root = "relative"`),
		"runtime empty":    replaceLine(f.document(), "root = "+tomlQuote(f.root), `root = ""`),
		"executable relative": replaceLine(f.document(), "executable = "+tomlQuote(f.executable),
			`executable = "relative"`),
		"executable empty": replaceLine(f.document(), "executable = "+tomlQuote(f.executable),
			`executable = ""`),
		"config relative": replaceLine(f.document(), "config_home = "+tomlQuote(f.configHome),
			`config_home = "relative"`),
		"config empty": replaceLine(f.document(), "config_home = "+tomlQuote(f.configHome),
			`config_home = ""`),
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			requireDecodeError(t, document)
		})
	}
}

func TestDecodeAllowsOmittedOrExplicitlyEmptyGatewayAPIKeyEnvironmentName(t *testing.T) {
	f := newFixture(t)
	documents := map[string]string{
		"omitted": strings.Replace(
			f.document(),
			"api_key_env = "+tomlQuote(testAPIKeyEnv)+"\n",
			"",
			1,
		),
		"explicit empty": replaceLine(
			f.document(),
			"api_key_env = "+tomlQuote(testAPIKeyEnv),
			`api_key_env = ""`,
		),
	}
	for name, document := range documents {
		t.Run(name, func(t *testing.T) {
			cfg := mustDecode(t, document)
			if cfg.Server.APIKeyEnv != "" {
				t.Fatalf("Server.APIKeyEnv = %q, want empty", cfg.Server.APIKeyEnv)
			}
		})
	}
}

func TestDecodeValidatesNonEmptyGatewayAPIKeyEnvironmentName(t *testing.T) {
	f := newFixture(t)
	for _, value := range []string{"lowercase", "9STARTS_WITH_DIGIT", "HAS-DASH", "HAS SPACE", "É"} {
		t.Run(value, func(t *testing.T) {
			document := replaceLine(f.document(), "api_key_env = "+tomlQuote(testAPIKeyEnv),
				"api_key_env = "+tomlQuote(value))
			requireDecodeError(t, document)
		})
	}

	for _, value := range []string{"_", "A", "_PRIVATE", "API_KEY_2"} {
		t.Run("valid_"+value, func(t *testing.T) {
			document := replaceLine(f.document(), "api_key_env = "+tomlQuote(testAPIKeyEnv),
				"api_key_env = "+tomlQuote(value))
			mustDecode(t, document)
		})
	}
}

func TestAllowedCredentialEnvMatchesExactClosedProviderMatrix(t *testing.T) {
	wantProviders := []string{"claude", "codex", "gemini"}
	wantCredentialEnv := map[string][]string{
		"claude": {"ANTHROPIC_API_KEY"},
		"codex":  {},
		"gemini": {
			"GEMINI_API_KEY",
			"GOOGLE_API_KEY",
			"GOOGLE_APPLICATION_CREDENTIALS",
			"GOOGLE_CLOUD_LOCATION",
			"GOOGLE_CLOUD_PROJECT",
		},
	}

	gotProviders := make([]string, 0, len(allowedCredentialEnv))
	for provider := range allowedCredentialEnv {
		gotProviders = append(gotProviders, provider)
	}
	sort.Strings(gotProviders)
	if !reflect.DeepEqual(gotProviders, wantProviders) {
		t.Fatalf("allowedCredentialEnv providers = %q, want %q", gotProviders, wantProviders)
	}

	for _, provider := range wantProviders {
		gotNames := make([]string, 0, len(allowedCredentialEnv[provider]))
		for name := range allowedCredentialEnv[provider] {
			gotNames = append(gotNames, name)
		}
		sort.Strings(gotNames)
		if !reflect.DeepEqual(gotNames, wantCredentialEnv[provider]) {
			t.Errorf(
				"allowedCredentialEnv[%q] = %q, want %q",
				provider,
				gotNames,
				wantCredentialEnv[provider],
			)
		}
	}
}

func TestDecodeCodexRejectsEveryCredentialEnvironmentEntryWithFixedError(t *testing.T) {
	f := newFixture(t)
	const wantError = "invalid configuration: provider credential environment name"
	tests := []struct {
		name           string
		gatewayAPIKey  string
		credentialLine string
	}{
		{"openai", testAPIKeyEnv, `credential_env = ["OPENAI_API_KEY"]`},
		{"codex override", testAPIKeyEnv, `credential_env = ["CODEX_API_KEY"]`},
		{"anthropic", testAPIKeyEnv, `credential_env = ["ANTHROPIC_API_KEY"]`},
		{"gemini", testAPIKeyEnv, `credential_env = ["GEMINI_API_KEY"]`},
		{"google", testAPIKeyEnv, `credential_env = ["GOOGLE_API_KEY"]`},
		{"google application credentials", testAPIKeyEnv,
			`credential_env = ["GOOGLE_APPLICATION_CREDENTIALS"]`},
		{"vertex selector", testAPIKeyEnv,
			`credential_env = ["GOOGLE_GENAI_USE_VERTEXAI"]`},
		{"google project", testAPIKeyEnv, `credential_env = ["GOOGLE_CLOUD_PROJECT"]`},
		{"google location", testAPIKeyEnv, `credential_env = ["GOOGLE_CLOUD_LOCATION"]`},
		{"gateway collision", "OPENAI_API_KEY", `credential_env = ["OPENAI_API_KEY"]`},
		{"duplicate", testAPIKeyEnv,
			`credential_env = ["OPENAI_API_KEY", "OPENAI_API_KEY"]`},
		{"arbitrary valid name", testAPIKeyEnv, `credential_env = ["OTHER_PROVIDER_API_KEY"]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := f.providerDocument("codex", test.gatewayAPIKey, test.credentialLine)
			requireDecodeErrorText(t, document, wantError)
		})
	}
}

func TestDecodeRejectsInvalidProviderCredentialEnvironmentName(t *testing.T) {
	f := newFixture(t)
	document := f.providerDocument(
		"claude",
		testAPIKeyEnv,
		`credential_env = ["not-an-env-name"]`,
	)
	requireDecodeErrorText(
		t,
		document,
		"invalid configuration: provider credential environment name",
	)
}

func TestDecodeAcceptsOnlyClosedProvidersAndDeclaredModelReferences(t *testing.T) {
	f := newFixture(t)
	tests := map[string]string{
		"no providers": strings.Replace(
			f.document(),
			fmt.Sprintf("[providers.codex]\nexecutable = %s\nconfig_home = %s\n\n",
				tomlQuote(f.executable), tomlQuote(f.configHome)),
			"",
			1,
		),
		"unknown provider":          strings.Replace(f.document(), "[providers.codex]", "[providers.other]", 1),
		"undeclared model provider": replaceLine(f.document(), `provider = "codex"`, `provider = "claude"`),
		"unknown model provider":    replaceLine(f.document(), `provider = "codex"`, `provider = "other"`),
		"no models":                 f.document()[:strings.Index(f.document(), "[[models]]")],
		"duplicate aliases": f.document() + `
[[models]]
id = "codex-default"
provider = "codex"
provider_model = "other"
`,
		"invalid alias": replaceLine(f.document(), `id = "codex-default"`, `id = "../model"`),
		"empty provider model": replaceLine(f.document(),
			`provider_model = "gpt-test"`, `provider_model = ""`),
		"unsafe provider model": replaceLine(f.document(),
			`provider_model = "gpt-test"`, `provider_model = "-option"`),
		"negative created": inModel(f.document(), "created = -1"),
	}
	for name, document := range tests {
		t.Run(name, func(t *testing.T) {
			requireDecodeError(t, document)
		})
	}
}

func TestDecodeSupportsEachProviderCredentialSet(t *testing.T) {
	f := newFixture(t)
	claudeExecutable := filepath.Join(filepath.Dir(f.executable), "claude")
	geminiExecutable := filepath.Join(filepath.Dir(f.executable), "gemini")
	if runtime.GOOS == "windows" {
		claudeExecutable += ".exe"
		geminiExecutable += ".exe"
	}
	document := fmt.Sprintf(`[server]
api_key_env = %s

[runtime]
root = %s

[providers.codex]
executable = %s
config_home = %s
credential_env = []

[providers.claude]
executable = %s
config_home = %s
credential_env = ["ANTHROPIC_API_KEY"]

[providers.gemini]
executable = %s
config_home = %s
credential_env = ["GOOGLE_CLOUD_LOCATION", "GOOGLE_APPLICATION_CREDENTIALS",
  "GOOGLE_CLOUD_PROJECT"]

[[models]]
id = "codex"
provider = "codex"
provider_model = "gpt-test"

[[models]]
id = "claude"
provider = "claude"
provider_model = "claude-test"

[[models]]
id = "gemini"
provider = "gemini"
provider_model = "gemini-test"
`,
		tomlQuote(testAPIKeyEnv),
		tomlQuote(f.root),
		tomlQuote(f.executable),
		tomlQuote(f.configHome),
		tomlQuote(claudeExecutable),
		tomlQuote(filepath.Join(filepath.Dir(f.configHome), "claude")),
		tomlQuote(geminiExecutable),
		tomlQuote(filepath.Join(filepath.Dir(f.configHome), "gemini")),
	)

	cfg := mustDecode(t, document)
	if len(cfg.Providers) != 3 || len(cfg.Models) != 3 {
		t.Fatalf("Decode() providers/models = %d/%d, want 3/3", len(cfg.Providers), len(cfg.Models))
	}
	if got := cfg.Providers["codex"].CredentialEnv; len(got) != 0 {
		t.Fatalf("Codex CredentialEnv = %q, want empty", got)
	}
	if got := cfg.Providers["claude"].CredentialEnv; !reflect.DeepEqual(
		got,
		[]string{"ANTHROPIC_API_KEY"},
	) {
		t.Fatalf("Claude CredentialEnv = %q", got)
	}
	if got := cfg.Providers["gemini"].CredentialEnv; !reflect.DeepEqual(got, []string{
		"GOOGLE_CLOUD_LOCATION",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT",
	}) {
		t.Fatalf("Gemini CredentialEnv = %q", got)
	}
}

func TestDecodeAllowsEmptyCredentialListsForEveryProvider(t *testing.T) {
	f := newFixture(t)
	for _, provider := range []string{"codex", "claude", "gemini"} {
		for _, test := range []struct {
			name string
			line string
		}{
			{"omitted", ""},
			{"explicit empty", "credential_env = []"},
		} {
			t.Run(provider+"_"+test.name, func(t *testing.T) {
				mustDecode(t, f.providerDocument(provider, testAPIKeyEnv, test.line))
			})
		}
	}
}

func TestDecodeRejectsGatewayAPIKeyReuseForEverySupportedCredential(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		provider        string
		environmentName string
	}{
		{"claude", "ANTHROPIC_API_KEY"},
		{"gemini", "GEMINI_API_KEY"},
		{"gemini", "GOOGLE_API_KEY"},
		{"gemini", "GOOGLE_APPLICATION_CREDENTIALS"},
		{"gemini", "GOOGLE_CLOUD_PROJECT"},
		{"gemini", "GOOGLE_CLOUD_LOCATION"},
	}
	for _, test := range tests {
		t.Run(test.provider+"_"+test.environmentName, func(t *testing.T) {
			document := f.providerDocument(
				test.provider,
				test.environmentName,
				"credential_env = ["+tomlQuote(test.environmentName)+"]",
			)
			requireDecodeErrorText(
				t,
				document,
				"invalid configuration: provider and gateway credential separation",
			)
		})
	}
}

func TestDecodeEnforcesClaudeCredentialEnvironmentMatrix(t *testing.T) {
	f := newFixture(t)
	candidates := []string{
		"OPENAI_API_KEY",
		"CODEX_API_KEY",
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY",
		"GOOGLE_API_KEY",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_GENAI_USE_VERTEXAI",
		"GOOGLE_CLOUD_PROJECT",
		"GOOGLE_CLOUD_LOCATION",
		"OTHER_PROVIDER_API_KEY",
	}
	for _, environmentName := range candidates {
		t.Run(environmentName, func(t *testing.T) {
			document := f.providerDocument(
				"claude",
				testAPIKeyEnv,
				"credential_env = ["+tomlQuote(environmentName)+"]",
			)
			if environmentName == "ANTHROPIC_API_KEY" {
				mustDecode(t, document)
				return
			}
			requireDecodeErrorText(
				t,
				document,
				"invalid configuration: provider credential environment name",
			)
		})
	}
}

func TestDecodeAcceptsOnlyExactGeminiCredentialProfiles(t *testing.T) {
	f := newFixture(t)
	accepted := [][]string{
		{"GEMINI_API_KEY"},
		{"GOOGLE_API_KEY"},
		{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION"},
		{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_PROJECT"},
		{"GOOGLE_CLOUD_PROJECT", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_LOCATION"},
		{"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_APPLICATION_CREDENTIALS"},
		{"GOOGLE_CLOUD_LOCATION", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT"},
		{"GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_PROJECT", "GOOGLE_APPLICATION_CREDENTIALS"},
	}
	for _, profile := range accepted {
		t.Run(strings.Join(profile, "+"), func(t *testing.T) {
			document := f.providerDocument(
				"gemini",
				testAPIKeyEnv,
				"credential_env = ["+quotedList(profile)+"]",
			)
			cfg := mustDecode(t, document)
			if got := cfg.Providers["gemini"].CredentialEnv; !reflect.DeepEqual(got, profile) {
				t.Fatalf("CredentialEnv=%q, want %q", got, profile)
			}
		})
	}
}

func TestDecodeRejectsRecognizedInvalidGeminiProfilesWithFixedError(t *testing.T) {
	f := newFixture(t)
	const wantError = "invalid configuration: Gemini credential profile"
	profiles := [][]string{
		{"GOOGLE_APPLICATION_CREDENTIALS"},
		{"GOOGLE_CLOUD_PROJECT"},
		{"GOOGLE_CLOUD_LOCATION"},
		{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT"},
		{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_LOCATION"},
		{"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION"},
		{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		{"GEMINI_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION"},
		{"GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION"},
		{"GEMINI_API_KEY", "GEMINI_API_KEY"},
		{"GOOGLE_API_KEY", "GOOGLE_API_KEY"},
		{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "GOOGLE_CLOUD_LOCATION"},
	}
	for index, profile := range profiles {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			document := f.providerDocument(
				"gemini",
				testAPIKeyEnv,
				"credential_env = ["+quotedList(profile)+"]",
			)
			requireDecodeErrorText(t, document, wantError)
		})
	}
}

func TestDecodeRejectsUnknownAndRemovedGeminiCredentialNames(t *testing.T) {
	f := newFixture(t)
	for _, name := range []string{
		"GOOGLE_GENAI_USE_VERTEXAI",
		"ANTHROPIC_API_KEY",
		"OTHER_PROVIDER_API_KEY",
		"not-an-env-name",
	} {
		t.Run(name, func(t *testing.T) {
			document := f.providerDocument(
				"gemini",
				testAPIKeyEnv,
				"credential_env = ["+tomlQuote(name)+"]",
			)
			requireDecodeErrorText(
				t,
				document,
				"invalid configuration: provider credential environment name",
			)
		})
	}
}

func TestDecodeRejectsDuplicateSupportedCredentialEnvironmentNames(t *testing.T) {
	f := newFixture(t)
	tests := []struct {
		provider        string
		environmentName string
	}{
		{"claude", "ANTHROPIC_API_KEY"},
	}
	for _, test := range tests {
		t.Run(test.provider+"_"+test.environmentName, func(t *testing.T) {
			document := f.providerDocument(
				test.provider,
				testAPIKeyEnv,
				"credential_env = ["+
					tomlQuote(test.environmentName)+", "+
					tomlQuote(test.environmentName)+"]",
			)
			requireDecodeErrorText(
				t,
				document,
				"invalid configuration: provider credential environment uniqueness",
			)
		})
	}
}

func TestNormalizeProviderOwnsCredentialAndPrefixSlices(t *testing.T) {
	prefix := []string{"entrypoint", "prefix-canary"}
	credentials := []string{"GEMINI_API_KEY", "credential-canary"}
	normalized, err := normalizeProvider(wireProvider{
		PrefixArgs:    prefix[:1],
		CredentialEnv: credentials[:1],
	})
	if err != nil {
		t.Fatal(err)
	}

	prefix[0] = "mutated"
	credentials[0] = "mutated"
	if !reflect.DeepEqual(normalized.PrefixArgs, []string{"entrypoint"}) {
		t.Fatalf("PrefixArgs aliased wire input: %q", normalized.PrefixArgs)
	}
	if !reflect.DeepEqual(normalized.CredentialEnv, []string{"GEMINI_API_KEY"}) {
		t.Fatalf("CredentialEnv aliased wire input: %q", normalized.CredentialEnv)
	}
	normalized.PrefixArgs = append(normalized.PrefixArgs, "owned")
	normalized.CredentialEnv = append(normalized.CredentialEnv, "owned")
	if prefix[1] != "prefix-canary" || credentials[1] != "credential-canary" {
		t.Fatalf("normalized append mutated wire backing arrays: %q / %q", prefix, credentials)
	}
}

func TestNormalizeProviderDoesNotShareSlicesAcrossConfigs(t *testing.T) {
	wire := wireProvider{
		PrefixArgs:    []string{"entrypoint"},
		CredentialEnv: []string{"GEMINI_API_KEY"},
	}
	first, err := normalizeProvider(wire)
	if err != nil {
		t.Fatal(err)
	}
	second, err := normalizeProvider(wire)
	if err != nil {
		t.Fatal(err)
	}

	first.PrefixArgs[0] = "mutated"
	first.CredentialEnv[0] = "mutated"
	if !reflect.DeepEqual(second.PrefixArgs, []string{"entrypoint"}) {
		t.Fatalf("PrefixArgs shared across configs: %q", second.PrefixArgs)
	}
	if !reflect.DeepEqual(second.CredentialEnv, []string{"GEMINI_API_KEY"}) {
		t.Fatalf("CredentialEnv shared across configs: %q", second.CredentialEnv)
	}
	if !reflect.DeepEqual(wire.PrefixArgs, []string{"entrypoint"}) ||
		!reflect.DeepEqual(wire.CredentialEnv, []string{"GEMINI_API_KEY"}) {
		t.Fatal("normalized configs mutated their shared wire source")
	}
}

func quotedList(values []string) string {
	quoted := make([]string, len(values))
	for index, value := range values {
		quoted[index] = tomlQuote(value)
	}
	return strings.Join(quoted, ", ")
}

func TestDecodeCapsModelCount(t *testing.T) {
	f := newFixture(t)
	prefix := f.document()[:strings.Index(f.document(), "[[models]]")]
	var models strings.Builder
	for i := 0; i < 1_024; i++ {
		fmt.Fprintf(&models, `[[models]]
id = "model-%04d"
provider = "codex"
provider_model = "gpt-test"
`, i)
	}
	mustDecode(t, prefix+models.String())

	models.WriteString(`[[models]]
id = "model-over-limit"
provider = "codex"
provider_model = "gpt-test"
`)
	requireDecodeError(t, prefix+models.String())
}

func TestDecodeCapsInputAtOneMiB(t *testing.T) {
	f := newFixture(t)
	document := f.document()
	exact := document + strings.Repeat(" ", mib-len(document))
	mustDecode(t, exact)

	_, err := Decode(strings.NewReader(exact + " "))
	if !errors.Is(err, ErrConfigTooLarge) {
		t.Fatalf("Decode(over limit) error = %v, want ErrConfigTooLarge", err)
	}
}

type failingReader struct {
	err error
}

func (r failingReader) Read([]byte) (int, error) {
	return 0, r.err
}

func TestDecodeReturnsSafeSentinelForReaderErrors(t *testing.T) {
	secret := "reader-secret-must-not-leak"
	_, err := Decode(failingReader{err: errors.New(secret)})
	if !errors.Is(err, ErrConfigTooLarge) {
		t.Fatalf("Decode(reader error) = %v, want ErrConfigTooLarge", err)
	}
	if strings.Contains(fmt.Sprint(err), secret) {
		t.Fatalf("Decode(reader error) leaked reader error: %v", err)
	}
}

func TestDecodeErrorsNeverEchoConfigurationValues(t *testing.T) {
	f := newFixture(t)
	canary := "SECRET-CANARY-VALUE"
	documents := []string{
		inServer(f.document(), "unknown_key = "+tomlQuote(canary)),
		inServer(f.document(), "read_header_timeout = "+tomlQuote(canary)),
		replaceLine(f.document(), `provider_model = "gpt-test"`,
			"provider_model = "+tomlQuote("-"+canary)),
		inProvider(f.document(), "credential_env = ["+tomlQuote(canary)+"]"),
		replaceLine(f.document(), "root = "+tomlQuote(f.root), "root = "+tomlQuote(canary)),
	}
	for i, document := range documents {
		_, err := Decode(strings.NewReader(document))
		if err == nil {
			t.Fatalf("case %d unexpectedly succeeded", i)
		}
		if strings.Contains(fmt.Sprint(err), canary) {
			t.Fatalf("case %d leaked configuration value in error: %v", i, err)
		}
	}
}

func TestDecodeRejectsArchitectureSizedIntegerOverflow(t *testing.T) {
	f := newFixture(t)
	requireDecodeError(t, inServer(f.document(), "handler_limit = 9223372036854775807"))
	requireDecodeError(t, inServer(f.document(), "handler_limit = 9223372036854775808"))
}

func TestValidatePrefixArgsEnforcesClosedUnixShape(t *testing.T) {
	if err := validatePrefixArgs("linux", "/opt/bin/codex", nil); err != nil {
		t.Fatalf("empty Unix prefix args error = %v", err)
	}
	for _, args := range [][]string{
		{"/opt/provider.js"},
		{"--fake-mode"},
		{""},
	} {
		if err := validatePrefixArgs("linux", "/opt/bin/codex", args); err == nil {
			t.Fatalf("Unix prefix args %q accepted", args)
		}
	}
}

func TestValidatePrefixArgsEnforcesClosedWindowsShape(t *testing.T) {
	node := `C:\Program Files\nodejs\node.exe`
	entrypoint := `C:\Program Files\providers\provider.mjs`

	for _, test := range []struct {
		name       string
		executable string
		args       []string
	}{
		{"native empty", `C:\Providers\codex.exe`, nil},
		{"node empty", node, nil},
		{"node js", node, []string{`C:\Providers\provider.js`}},
		{"node mjs", node, []string{entrypoint}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePrefixArgs("windows", test.executable, test.args); err != nil {
				t.Fatalf("validatePrefixArgs() error = %v", err)
			}
		})
	}

	invalidUTF8 := "C:\\Providers\\" + string([]byte{0xff}) + ".js"
	overlong := `C:\` + strings.Repeat("a", 4_096) + ".js"
	tests := []struct {
		name       string
		executable string
		args       []string
	}{
		{"non-node executable", `C:\Providers\codex.exe`, []string{entrypoint}},
		{"relative node executable", `node.exe`, []string{entrypoint}},
		{"wrong executable basename", `C:\bin\xnode.exe`, []string{entrypoint}},
		{"more than one", node, []string{entrypoint, `C:\Providers\other.js`}},
		{"empty", node, []string{""}},
		{"relative", node, []string{`provider.js`}},
		{"option", node, []string{"--require"}},
		{"wrong extension", node, []string{`C:\Providers\provider.json`}},
		{"uppercase extension", node, []string{`C:\Providers\provider.JS`}},
		{"NUL", node, []string{"C:\\Providers\\provider\x00.js"}},
		{"control", node, []string{"C:\\Providers\\provider\n.js"}},
		{"invalid UTF-8", node, []string{invalidUTF8}},
		{"overlong", node, []string{overlong}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validatePrefixArgs("windows", test.executable, test.args); err == nil {
				t.Fatal("validatePrefixArgs() unexpectedly accepted invalid shape")
			}
		})
	}
}

func TestDecodeRejectsProductionPrefixArgsOnUnix(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix production rule")
	}
	f := newFixture(t)
	requireDecodeError(t, inProvider(f.document(), `prefix_args = ["--fake-mode"]`))
}

func TestDurationUnmarshalTextRejectsUnsafeValuesWithoutEchoingThem(t *testing.T) {
	for _, value := range []string{"", "0s", "-1s", "secret-duration"} {
		var duration Duration
		err := duration.UnmarshalText([]byte(value))
		if err == nil {
			t.Fatalf("Duration.UnmarshalText(%q) unexpectedly succeeded", value)
		}
		if strings.Contains(err.Error(), value) && value != "" {
			t.Fatalf("Duration.UnmarshalText() leaked input %q in %v", value, err)
		}
	}
}

func TestDecodeDoesNotRequireProviderPathsToExist(t *testing.T) {
	f := newFixture(t)
	document := replaceLine(f.document(), "executable = "+tomlQuote(f.executable),
		"executable = "+tomlQuote(filepath.Join(t.TempDir(), "absent", "codex")))
	mustDecode(t, document)
}

var _ io.Reader = failingReader{}
