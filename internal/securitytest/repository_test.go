package securitytest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
	"github.com/pelletier/go-toml/v2"
	"go.yaml.in/yaml/v3"
)

const expectedModule = "github.com/krkarma777/ai-cli-gateway"

// privateNotesDirectory holds untracked working notes. It is ignored by Git and
// skipped by the repository scan, so drafts may reference local paths that the
// published tree must never contain.
const privateNotesDirectory = "docs/superpowers"

const (
	checkoutAction         = "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1"              // v7.0.1
	setupGoAction          = "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"              // v7.0.0
	golangciAction         = "golangci/golangci-lint-action@ba0d7d2ec06a0ea1cb5fa41b2e4a3ab91d21278a" // v9.3.0, peeled
	setupPythonAction      = "actions/setup-python@5fda3b95a4ea91299a34e894583c3862153e4b97"          // v7.0.0
	setupNodeAction        = "actions/setup-node@820762786026740c76f36085b0efc47a31fe5020"            // v7.0.0
	attestAction           = "actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6"                // v4.2.2
	uploadArtifactAction   = "actions/upload-artifact@043fb46d1a93c77aae656e7c1c64a875d1fc6a0a"       // v7.0.1
	downloadArtifactAction = "actions/download-artifact@3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c"     // v8.0.1
)

var scanRootFlag = flag.String("scan-root", "", "scan an explicit absolute materialized repository root")

var codexConfigAuthFilenamePattern = regexp.MustCompile(`(?i)(?:^|[\\/[:space:]"']+)[^\\/[:space:]"']*(?:auth(?:entication)?|credentials?|tokens?|login)[^\\/[:space:]"']*\.(?:json|toml|yaml|yml|ini|conf|db)(?:$|[^[:alnum:]_])`)

func TestCodexConfigExampleContentSafety(t *testing.T) {
	contents := readRepositoryFile(t, "examples/config/codex.example.toml")
	if err := codexConfigContentSafety(contents); err != nil {
		t.Fatalf("shipped codex.example.toml safety error: %v", err)
	}
	for _, mutation := range []string{
		`C:\Users\alice`,
		"C:/Users/alice",
		".CODEX/SESSION-AUTH.JSON",
		"LOGIN-state.toml",
		"tokens.yaml",
		"token",
		"ToKeN",
	} {
		t.Run(mutation, func(t *testing.T) {
			if err := codexConfigContentSafety(append(contents, []byte("\n# "+mutation)...)); err == nil {
				t.Fatalf("safety policy accepted mutation %q", mutation)
			}
		})
	}
}

func codexConfigContentSafety(contents []byte) error {
	if !utf8.Valid(contents) {
		return errors.New("not valid UTF-8")
	}
	text := string(contents)
	lower := strings.ToLower(text)
	for _, forbidden := range []string{
		"prefix_args", "credential_env", "concurrency", "queue_size", "queue_bytes", "queue_timeout", "execution_timeout",
		"/users/", "/home/", "c:\\users\\", "c:/users/", "@", "account", "token",
	} {
		if strings.Contains(lower, forbidden) {
			return errors.New("contains forbidden marker " + strconv.Quote(forbidden))
		}
	}
	if codexConfigAuthFilenamePattern.MatchString(text) {
		return errors.New("contains auth-like filename")
	}
	return nil
}

func TestCodexConfigExampleContract(t *testing.T) {
	contents := readRepositoryFile(t, "examples/config/codex.example.toml")
	if err := codexConfigContentSafety(contents); err != nil {
		t.Fatalf("codex.example.toml safety error: %v", err)
	}
	text := string(contents)

	var raw map[string]any
	if err := toml.Unmarshal(contents, &raw); err != nil {
		t.Fatalf("parse codex.example.toml as TOML: %v", err)
	}
	requireExactTableKeys(t, "root", raw, "server", "runtime", "providers", "models")
	requireExactTableKeys(t, "server", requireTOMLTable(t, raw, "server"), "listen", "api_key_env")
	requireExactTableKeys(t, "runtime", requireTOMLTable(t, raw, "runtime"), "root")
	providers := requireTOMLTable(t, raw, "providers")
	requireExactTableKeys(t, "providers", providers, "codex")
	requireExactTableKeys(t, "providers.codex", requireTOMLTable(t, providers, "codex"), "executable", "config_home")
	models, ok := raw["models"].([]any)
	if !ok || len(models) != 1 {
		t.Fatalf("models = %T with length %d, want exactly one table", raw["models"], lengthOfSlice(models))
	}
	model, ok := models[0].(map[string]any)
	if !ok {
		t.Fatalf("models[0] = %T, want TOML table", models[0])
	}
	requireExactTableKeys(t, "models[0]", model, "id", "provider", "provider_model", "created")

	for _, marker := range []string{
		"/opt/ai-cli-gateway/bin/codex",
		"/var/lib/ai-cli-gateway/codex-home",
		"/var/lib/ai-cli-gateway/runtime",
		"configured-provider-model",
	} {
		if count := strings.Count(text, marker); count != 1 {
			t.Fatalf("marker %q occurs %d times, want exactly once", marker, count)
		}
	}
}

func TestCodexConfigExampleUnixDecode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the committed example is deliberately Unix/systemd-oriented")
	}
	got, err := config.Decode(bytes.NewReader(readRepositoryFile(t, "examples/config/codex.example.toml")))
	if err != nil {
		t.Fatalf("Decode(unchanged codex.example.toml): %v", err)
	}
	requireCodexExampleDecoded(t, got, false)
}

func TestCodexConfigExampleWindowsDecodeAfterExactSubstitution(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows validates the deterministic test-local copy")
	}
	contents := string(readRepositoryFile(t, "examples/config/codex.example.toml"))
	for _, substitution := range []struct{ unix, windows string }{
		{"/opt/ai-cli-gateway/bin/codex", "C:/ai-cli-gateway/bin/codex.exe"},
		{"/var/lib/ai-cli-gateway/codex-home", "C:/ProgramData/ai-cli-gateway/codex-home"},
		{"/var/lib/ai-cli-gateway/runtime", "C:/ProgramData/ai-cli-gateway/runtime"},
		{"configured-provider-model", "sdk-contract-model"},
	} {
		if count := strings.Count(contents, substitution.unix); count != 1 {
			t.Fatalf("Unix marker %q occurs %d times, want exactly once", substitution.unix, count)
		}
		contents = strings.Replace(contents, substitution.unix, substitution.windows, 1)
	}
	got, err := config.Decode(strings.NewReader(contents))
	if err != nil {
		t.Fatalf("Decode(test-local Windows config copy): %v", err)
	}
	requireCodexExampleDecoded(t, got, true)
}

func requireCodexExampleDecoded(t *testing.T, got config.Config, windows bool) {
	t.Helper()
	runtimeRoot := "/var/lib/ai-cli-gateway/runtime"
	executable := "/opt/ai-cli-gateway/bin/codex"
	configHome := "/var/lib/ai-cli-gateway/codex-home"
	providerModel := "configured-provider-model"
	if windows {
		runtimeRoot = "C:/ProgramData/ai-cli-gateway/runtime"
		executable = "C:/ai-cli-gateway/bin/codex.exe"
		configHome = "C:/ProgramData/ai-cli-gateway/codex-home"
		providerModel = "sdk-contract-model"
	}
	if got.Server.Listen != "127.0.0.1:8080" || got.Server.APIKeyEnv != "AI_CLI_GATEWAY_API_KEY" {
		t.Fatalf("Server = %#v, want explicit listener and API key environment", got.Server)
	}
	if got.Runtime.Root != runtimeRoot {
		t.Fatalf("Runtime.Root = %q, want %q", got.Runtime.Root, runtimeRoot)
	}
	if len(got.Providers) != 1 {
		t.Fatalf("Providers has %d entries, want exactly one", len(got.Providers))
	}
	provider, ok := got.Providers["codex"]
	if !ok {
		t.Fatal("Providers is missing codex")
	}
	if provider.Executable != executable || provider.ConfigHome != configHome || len(provider.PrefixArgs) != 0 || len(provider.CredentialEnv) != 0 {
		t.Fatalf("Providers[codex] = %#v, want only explicit executable and config home", provider)
	}
	if provider.Concurrency != 1 || provider.QueueSize != 32 || provider.QueueBytes != 16_777_216 ||
		provider.QueueTimeout != config.Duration(30*time.Second) || provider.ExecutionTimeout != config.Duration(5*time.Minute) {
		t.Fatalf("Providers[codex] defaults = %#v, want decoder defaults", provider)
	}
	wantModels := []config.Model{{ID: "codex-local", Provider: "codex", ProviderModel: providerModel, Created: 0}}
	if !reflect.DeepEqual(got.Models, wantModels) {
		t.Fatalf("Models = %#v, want %#v", got.Models, wantModels)
	}
}

func TestConfigExampleStaticContract(t *testing.T) {
	contents := readRepositoryFile(t, "config.example.toml")
	if !utf8.Valid(contents) {
		t.Fatal("config.example.toml is not valid UTF-8")
	}
	for _, character := range string(contents) {
		if unicode.IsControl(character) && character != '\n' {
			t.Fatalf("config.example.toml contains a forbidden control character: U+%04X", character)
		}
	}
	if hasPrivateKeyHeader(contents) || hasClosedCatalogToken(contents) || hasDeveloperHomePath(contents) {
		t.Fatal("config.example.toml contains private, credential, or developer-specific material")
	}

	text := string(contents)
	for _, forbidden := range []string{"/Users/", "/home/", "C:\\Users\\", "auth.json", "credentials.json", "@"} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("config.example.toml contains forbidden identity/auth marker %q", forbidden)
		}
	}
	for _, required := range []string{
		"Codex >=0.146.0,<0.147.0",
		"Claude Code >=2.1.208,<2.2.0",
		"Gemini CLI >=0.53.0,<0.54.0",
		"compiled in and cannot be overridden by configuration",
		"GEMINI_API_KEY",
		"upstream availability",
		"billing tier",
		"quota",
		"entitlement",
		"key validity",
		"does not restrict or infer the upstream tier",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("config.example.toml is missing required contract text %q", required)
		}
	}
	for _, assignment := range []string{
		"AI_CLI_GATEWAY_API_KEY=",
		"GEMINI_API_KEY=",
		"ANTHROPIC_API_KEY=",
		"GOOGLE_API_KEY=",
	} {
		if strings.Contains(text, assignment) {
			t.Fatalf("config.example.toml assigns a credential value through %q", assignment)
		}
	}

	var raw map[string]any
	if err := toml.Unmarshal(contents, &raw); err != nil {
		t.Fatalf("parse config.example.toml as TOML: %v", err)
	}
	requireExactTableKeys(t, "root", raw, "server", "runtime", "providers", "models")
	requireExactTableKeys(t, "server", requireTOMLTable(t, raw, "server"),
		"listen", "api_key_env", "http_body_bytes", "input_bytes", "instructions_bytes",
		"schema_bytes", "handler_limit", "body_reader_limit", "max_header_bytes",
		"read_header_timeout", "body_read_timeout", "idle_timeout", "shutdown_timeout")
	requireExactTableKeys(t, "runtime", requireTOMLTable(t, raw, "runtime"),
		"root", "term_grace", "cleanup_timeout", "stdout_bytes", "stderr_bytes", "final_bytes")

	providers := requireTOMLTable(t, raw, "providers")
	requireExactTableKeys(t, "providers", providers, "codex", "claude", "gemini")
	for _, name := range []string{"codex", "claude", "gemini"} {
		requireExactTableKeys(t, "providers."+name, requireTOMLTable(t, providers, name),
			"executable", "prefix_args", "config_home", "credential_env", "concurrency",
			"queue_size", "queue_bytes", "queue_timeout", "execution_timeout")
	}

	models, ok := raw["models"].([]any)
	if !ok || len(models) != 3 {
		t.Fatalf("models = %T with length %d, want exactly three tables", raw["models"], lengthOfSlice(models))
	}
	for index, value := range models {
		model, ok := value.(map[string]any)
		if !ok {
			t.Fatalf("models[%d] = %T, want TOML table", index, value)
		}
		requireExactTableKeys(t, "models["+strconv.Itoa(index)+"]", model,
			"id", "provider", "provider_model", "created")
	}

	for _, placeholder := range unixExamplePaths() {
		if count := strings.Count(text, placeholder); count != 1 {
			t.Fatalf("Unix placeholder %q occurs %d times, want exactly once", placeholder, count)
		}
	}
}

func TestConfigExampleUnixDecode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the committed example is deliberately Unix/systemd-oriented")
	}
	contents := readRepositoryFile(t, "config.example.toml")
	got, err := config.Decode(bytes.NewReader(contents))
	if err != nil {
		t.Fatalf("Decode(unchanged config.example.toml): %v", err)
	}
	if want := expectedExampleConfig(false); !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded config.example.toml = %#v, want %#v", got, want)
	}
}

func TestConfigExampleWindowsDecodeAfterExactPathSubstitution(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native Windows validates the deterministic test-local copy")
	}
	contents := string(readRepositoryFile(t, "config.example.toml"))
	unixPaths := unixExamplePaths()
	windowsPaths := windowsExamplePaths()
	for index, unixPath := range unixPaths {
		if count := strings.Count(contents, unixPath); count != 1 {
			t.Fatalf("Unix placeholder %q occurs %d times, want exactly once", unixPath, count)
		}
		contents = strings.Replace(contents, unixPath, windowsPaths[index], 1)
	}
	for _, unixPath := range unixPaths {
		if strings.Contains(contents, unixPath) {
			t.Fatalf("test-local Windows copy retains Unix path %q", unixPath)
		}
	}

	got, err := config.Decode(strings.NewReader(contents))
	if err != nil {
		t.Fatalf("Decode(test-local Windows config copy): %v", err)
	}
	if want := expectedExampleConfig(true); !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded Windows config copy = %#v, want %#v", got, want)
	}
	for _, provider := range got.Providers {
		for _, pathValue := range []string{provider.Executable, provider.ConfigHome} {
			if strings.HasPrefix(pathValue, "/opt/") || strings.HasPrefix(pathValue, "/var/") || strings.HasPrefix(pathValue, "/run/") {
				t.Fatalf("decoded Windows path field retains Unix deployment path %q", pathValue)
			}
		}
	}
}

func expectedExampleConfig(windows bool) config.Config {
	paths := unixExamplePaths()
	if windows {
		paths = windowsExamplePaths()
	}
	providerDefaults := func(executable, configHome string, credentialEnv []string) config.Provider {
		return config.Provider{
			Executable:       executable,
			ConfigHome:       configHome,
			CredentialEnv:    credentialEnv,
			Concurrency:      1,
			QueueSize:        32,
			QueueBytes:       16_777_216,
			QueueTimeout:     config.Duration(30 * time.Second),
			ExecutionTimeout: config.Duration(5 * time.Minute),
		}
	}
	return config.Config{
		Server: config.Server{
			Listen:            "127.0.0.1:8080",
			APIKeyEnv:         "AI_CLI_GATEWAY_API_KEY",
			HTTPBodyBytes:     1_048_576,
			InputBytes:        524_288,
			InstructionsBytes: 262_144,
			SchemaBytes:       32_768,
			HandlerLimit:      128,
			BodyReaderLimit:   32,
			MaxHeaderBytes:    16_384,
			ReadHeaderTimeout: config.Duration(5 * time.Second),
			BodyReadTimeout:   config.Duration(15 * time.Second),
			IdleTimeout:       config.Duration(60 * time.Second),
			ShutdownTimeout:   config.Duration(15 * time.Second),
		},
		Runtime: config.Runtime{
			Root:           paths[6],
			TermGrace:      config.Duration(2 * time.Second),
			CleanupTimeout: config.Duration(5 * time.Second),
			StdoutBytes:    2_097_152,
			StderrBytes:    262_144,
			FinalBytes:     1_048_576,
		},
		Providers: map[string]config.Provider{
			"codex":  providerDefaults(paths[0], paths[3], nil),
			"claude": providerDefaults(paths[1], paths[4], nil),
			"gemini": providerDefaults(paths[2], paths[5], []string{"GEMINI_API_KEY"}),
		},
		Models: []config.Model{
			{ID: "codex-local", Provider: "codex", ProviderModel: "configured-provider-model", Created: 0},
			{ID: "claude-local", Provider: "claude", ProviderModel: "configured-provider-model", Created: 0},
			{ID: "gemini-local", Provider: "gemini", ProviderModel: "configured-provider-model", Created: 0},
		},
	}
}

func unixExamplePaths() []string {
	return []string{
		"/opt/ai-cli-gateway/bin/codex",
		"/opt/ai-cli-gateway/bin/claude",
		"/opt/ai-cli-gateway/bin/gemini",
		"/var/lib/ai-cli-gateway/codex-home",
		"/var/lib/ai-cli-gateway/claude-home",
		"/var/lib/ai-cli-gateway/gemini-home",
		"/run/ai-cli-gateway",
	}
}

func windowsExamplePaths() []string {
	return []string{
		"C:/ai-cli-gateway/bin/codex.exe",
		"C:/ai-cli-gateway/bin/claude.exe",
		"C:/ai-cli-gateway/bin/gemini.exe",
		"C:/ProgramData/ai-cli-gateway/codex-home",
		"C:/ProgramData/ai-cli-gateway/claude-home",
		"C:/ProgramData/ai-cli-gateway/gemini-home",
		"C:/ProgramData/ai-cli-gateway/runtime",
	}
}

func requireTOMLTable(t *testing.T, table map[string]any, key string) map[string]any {
	t.Helper()
	value, ok := table[key].(map[string]any)
	if !ok {
		t.Fatalf("TOML field %q = %T, want table", key, table[key])
	}
	return value
}

func requireExactTableKeys(t *testing.T, name string, table map[string]any, keys ...string) {
	t.Helper()
	if len(table) != len(keys) {
		t.Fatalf("%s has %d fields, want exactly %d", name, len(table), len(keys))
	}
	for _, key := range keys {
		if _, ok := table[key]; !ok {
			t.Fatalf("%s is missing field %q", name, key)
		}
	}
}

func lengthOfSlice(values []any) int {
	return len(values)
}

func TestOfficialSDKExamplesContract(t *testing.T) {
	requirements := string(readRepositoryFile(t, "examples/openai-sdk/python/requirements.txt"))
	requirementLines := nonCommentLines(requirements)
	if !reflect.DeepEqual(requirementLines, []string{"openai==2.53.0"}) {
		t.Fatalf("requirements.txt pins = %q, want exactly openai==2.53.0", requirementLines)
	}

	lockedRequirements := nonCommentLines(string(readRepositoryFile(t, "examples/openai-sdk/python/requirements.lock")))
	exactPythonPin := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*==[A-Za-z0-9][A-Za-z0-9.!+_-]*$`)
	foundPythonSDK := false
	for _, line := range lockedRequirements {
		if !exactPythonPin.MatchString(line) {
			t.Fatalf("requirements.lock contains a non-exact dependency pin")
		}
		if line == "openai==2.53.0" {
			foundPythonSDK = true
		}
	}
	if !foundPythonSDK {
		t.Fatal("requirements.lock does not contain openai==2.53.0")
	}
	lockedPythonBytes := []byte(strings.Join(lockedRequirements, "\n"))
	if hasClosedCatalogToken(lockedPythonBytes) || hasDeveloperHomePath(lockedPythonBytes) {
		t.Fatal("requirements.lock contains credential or developer-specific material")
	}
	for _, forbidden := range []string{"://", " @ ", "-e ", "--editable", "../", "~/", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY"} {
		if strings.Contains(string(lockedPythonBytes), forbidden) {
			t.Fatalf("requirements.lock contains forbidden dependency or credential marker %q", forbidden)
		}
	}

	var packageManifest struct {
		Name         string            `json:"name"`
		Private      bool              `json:"private"`
		Type         string            `json:"type"`
		Engines      map[string]string `json:"engines"`
		Dependencies map[string]string `json:"dependencies"`
		Scripts      map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(readRepositoryFile(t, "examples/openai-sdk/javascript/package.json"), &packageManifest); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	if packageManifest.Type != "module" || packageManifest.Engines["node"] != ">=24" ||
		packageManifest.Dependencies["openai"] != "6.48.0" {
		t.Fatalf("package.json does not pin the required module, Node, and OpenAI SDK contract")
	}
	if !packageManifest.Private || len(packageManifest.Engines) != 1 || len(packageManifest.Dependencies) != 1 {
		t.Fatal("package.json must be private and contain only the declared engine and dependency")
	}
	if len(packageManifest.Scripts) != 0 {
		t.Fatal("package.json root scripts must be empty")
	}

	var packageLock struct {
		LockfileVersion int `json:"lockfileVersion"`
		Packages        map[string]struct {
			Version      string            `json:"version"`
			Integrity    string            `json:"integrity"`
			Dependencies map[string]string `json:"dependencies"`
			Scripts      map[string]string `json:"scripts"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(readRepositoryFile(t, "examples/openai-sdk/javascript/package-lock.json"), &packageLock); err != nil {
		t.Fatalf("parse package-lock.json: %v", err)
	}
	rootPackage, ok := packageLock.Packages[""]
	if packageLock.LockfileVersion != 3 || !ok || rootPackage.Dependencies["openai"] != "6.48.0" {
		t.Fatal("package-lock.json does not lock the required root package and OpenAI SDK")
	}
	if len(rootPackage.Scripts) != 0 {
		t.Fatal("package-lock.json root scripts must be empty")
	}
	for path, dependency := range packageLock.Packages {
		if path != "" && dependency.Integrity == "" {
			t.Fatalf("package-lock.json package %q has no integrity", path)
		}
	}

	for _, source := range []struct {
		name     string
		contents string
	}{
		{
			name:     "Python",
			contents: string(readRepositoryFile(t, "examples/openai-sdk/python/main.py")),
		},
		{
			name:     "JavaScript",
			contents: string(readRepositoryFile(t, "examples/openai-sdk/javascript/main.mjs")),
		},
	} {
		if err := validateOfficialSDKSource(source.name, source.contents); err != nil {
			t.Fatalf("%s SDK source contract: %v", source.name, err)
		}
	}
}

func validateOfficialSDKSource(language, contents string) error {
	rawRequired := map[string][]string{
		"Python": {
			`os.environ.pop("OPENAI_LOG", None)`, `"max_retries": 0`, `models = client.models.list()`,
			`return 300.0`, `if len(value) > 3:`, `if not value.isascii() or not value.isdecimal():`,
			`if seconds < 1 or seconds > 300:`, `assert models.object == "list"`,
			`matching_models = [model for model in models.data if model.id == model_name]`,
			`assert len(matching_models) == 1`, `model = matching_models[0]`,
			`assert_fields(model, {"id", "object", "created", "owned_by"})`, `assert model.id == model_name`,
			`assert model.object == "model"`, `assert model.created == 0`, `assert model.owned_by == "local"`,
			`def assert_expected_output(value: object) -> None:`, `assert isinstance(value, str)`,
			`assert value in ("SDK_GATEWAY_OK", "SDK_GATEWAY_OK\n")`,
			`assert isinstance(response._request_id, str)`, `assert response._request_id.startswith("req_")`,
			`assert isinstance(response.id, str) and response.id.startswith("resp_")`,
			`assert response.object == "response"`, `def assert_integer_timestamp(value: object) -> None:`,
			`assert isinstance(value, (int, float)) and not isinstance(value, bool)`,
			`assert math.isfinite(numeric) and numeric.is_integer()`,
			`assert_integer_timestamp(response.created_at)`, `assert_integer_timestamp(response.completed_at)`,
			`assert response.completed_at >= response.created_at`, `assert response.status == "completed"`,
			`assert response.background is False`, `assert response.error is None`,
			`assert response.incomplete_details is None`, `assert response.instructions == "Return only the exact text requested."`,
			`assert response.model == model_name`, `assert response.parallel_tool_calls is False`,
			`assert response.previous_response_id is None`, `assert response.store is False`,
			`assert response.tools == []`, `assert response.tool_choice == "none"`,
			`assert_fields(response.text, {"format"})`, `assert_fields(response.text.format, {"type"})`,
			`assert response.text.format.type == "text"`, `assert len(response.output) == 1`,
			`assert isinstance(message.id, str) and message.id.startswith("msg_")`,
			`assert message.type == "message"`, `assert message.status == "completed"`,
			`assert message.role == "assistant"`, `assert len(message.content) == 1`, `assert content.type == "output_text"`,
			`assert content.annotations == []`, `assert_expected_output(content.text)`,
			`status = getattr(error, "status_code", None)`,
			`if isinstance(status, int) and not isinstance(status, bool) and 400 <= status <= 599:`,
			`return str(status)`, `return "unknown"`,
			`print(f"sdk_contract_error: missing {error.name}", file=sys.stderr)`,
			`print("sdk_contract_error: invalid AI_CLI_GATEWAY_TIMEOUT_SECONDS", file=sys.stderr)`,
			`print(f"sdk_contract_error: python_api {api_status(error)}", file=sys.stderr)`,
			`print("sdk_contract_error: python_assertion", file=sys.stderr)`, `print("python_sdk_contract_ok")`,
		},
		"JavaScript": {
			`logLevel: "off"`, `maxRetries: 0`, `const models = await client.models.list()`,
			`return 300_000`, `if (!/^[0-9]+$/.test(value))`,
			`if (!Number.isSafeInteger(seconds) || seconds < 1 || seconds > 300)`,
			`assert.equal(models.object, "list")`,
			`const matchingModels = models.data.filter((model) => model.id === modelName)`,
			`assert.equal(matchingModels.length, 1)`, `const [model] = matchingModels`,
			`assertFields(model, ["id", "object", "created", "owned_by"])`,
			`assert.equal(model.id, modelName)`, `assert.equal(model.object, "model")`,
			`assert.equal(model.created, 0)`, `assert.equal(model.owned_by, "local")`,
			`function assertExpectedOutput(value)`, `assert.equal(typeof value, "string")`,
			`assert.equal(value === "SDK_GATEWAY_OK" || value === "SDK_GATEWAY_OK\n", true)`,
			`assert.equal(typeof response._request_id, "string")`, `assert.match(response._request_id, /^req_/)`,
			`assert.equal(typeof response.id, "string")`, `assert.match(response.id, /^resp_/)`,
			`assert.equal(response.object, "response")`, `assert.equal(Number.isSafeInteger(response.created_at), true)`,
			`assert.equal(Number.isSafeInteger(response.completed_at), true)`,
			`assert.equal(response.completed_at >= response.created_at, true)`,
			`assert.equal(response.status, "completed")`, `assert.equal(response.background, false)`,
			`assert.equal(response.error, null)`, `assert.equal(response.incomplete_details, null)`,
			`assert.equal(response.instructions, "Return only the exact text requested.")`, `assert.equal(response.model, modelName)`,
			`assertExpectedOutput(response.output_text)`,
			`assert.equal(response.parallel_tool_calls, false)`, `assert.equal(response.previous_response_id, null)`,
			`assert.equal(response.store, false)`, `assert.deepEqual(response.tools, [])`,
			`assert.equal(response.tool_choice, "none")`, `assertFields(response.text, ["format"])`,
			`assertFields(response.text.format, ["type"])`, `assert.equal(response.text.format.type, "text")`,
			`assert.equal(response.output.length, 1)`, `assert.equal(typeof message.id, "string")`,
			`assert.match(message.id, /^msg_/)`, `assert.equal(message.type, "message")`,
			`assert.equal(message.status, "completed")`, `assert.equal(message.role, "assistant")`,
			`assert.equal(message.content.length, 1)`, `assert.equal(content.type, "output_text")`,
			`assert.deepEqual(content.annotations, [])`,
			`assertExpectedOutput(content.text)`,
			`if (Number.isSafeInteger(error.status) && error.status >= 400 && error.status <= 599)`,
			`return String(error.status)`, `return "unknown"`,
			"console.error(`sdk_contract_error: missing ${error.name}`)",
			`console.error("sdk_contract_error: invalid AI_CLI_GATEWAY_TIMEOUT_SECONDS")`,
			"console.error(`sdk_contract_error: javascript_api ${apiStatus(error)}`)",
			`console.error("sdk_contract_error: javascript_assertion")`, `console.log("javascript_sdk_contract_ok")`,
		},
	}
	normalizedRequired := map[string][]string{
		"Python": {
			`response = client.responses.create( model=model_name, instructions="Return only the exact text requested.", input="Reply with exactly: SDK_GATEWAY_OK", text={"format": {"type": "text"}}, stream=False, store=False, tools=[], tool_choice="none", )`,
			`assert_fields( response, { "id", "object", "created_at", "completed_at", "status", "background", "error", "incomplete_details", "instructions", "model", "output", "parallel_tool_calls", "previous_response_id", "text", "tools", "tool_choice", }, )`,
			`assert_fields(message, {"id", "type", "status", "role", "content"})`,
			`assert_fields(content, {"type", "annotations", "text"})`,
			`except Exception: print("sdk_contract_error: python_assertion", file=sys.stderr) return 1`,
		},
		"JavaScript": {
			`const response = await client.responses.create({ model: modelName, instructions: "Return only the exact text requested.", input: "Reply with exactly: SDK_GATEWAY_OK", text: { format: { type: "text" } }, stream: false, store: false, tools: [], tool_choice: "none", });`,
			`assertFields(response, [ "id", "object", "created_at", "completed_at", "status", "background", "error", "incomplete_details", "instructions", "model", "output", "output_text", "parallel_tool_calls", "previous_response_id", "store", "text", "tools", "tool_choice", ]);`,
			`assertFields(message, ["id", "type", "status", "role", "content"]);`,
			`assertFields(content, ["type", "annotations", "text"]);`,
			`} else { console.error("sdk_contract_error: javascript_assertion"); } process.exitCode = 1;`,
		},
	}
	markers, ok := rawRequired[language]
	if !ok {
		return errors.New("unknown SDK language")
	}
	for _, marker := range markers {
		if !strings.Contains(contents, marker) {
			return errors.New("missing required contract marker")
		}
		if strings.HasPrefix(marker, "assert") && !hasExecutableSDKAssertion(contents, marker) {
			return errors.New("required SDK assertion is not executable")
		}
	}
	normalized := collapseWhitespace(contents)
	for _, marker := range normalizedRequired[language] {
		if !strings.Contains(normalized, marker) {
			return errors.New("missing required normalized contract marker")
		}
	}
	if language == "Python" && strings.Index(contents, `os.environ.pop("OPENAI_LOG", None)`) > strings.Index(contents, "import openai") {
		return errors.New("Python logging suppression occurs after SDK import")
	}
	if (language == "Python" && strings.Contains(contents, "str(error)")) ||
		(language == "JavaScript" && strings.Contains(contents, "String(error)")) {
		return errors.New("source serializes an SDK exception")
	}
	directExceptionOutput := map[string]*regexp.Regexp{
		"Python":     regexp.MustCompile(`\bprint\s*\(\s*error(?:\s*[,)]|\.)`),
		"JavaScript": regexp.MustCompile(`\bconsole\s*\.\s*error\s*\(\s*error(?:\s*[,)]|\.)`),
	}
	if directExceptionOutput[language].MatchString(contents) {
		return errors.New("source prints an SDK exception")
	}
	variables := regexp.MustCompile(`AI_CLI_GATEWAY_[A-Z0-9_]+`).FindAllString(contents, -1)
	gotVariables := make(map[string]struct{})
	for _, variable := range variables {
		gotVariables[variable] = struct{}{}
	}
	wantVariables := map[string]struct{}{
		"AI_CLI_GATEWAY_BASE_URL": {}, "AI_CLI_GATEWAY_API_KEY": {},
		"AI_CLI_GATEWAY_MODEL": {}, "AI_CLI_GATEWAY_TIMEOUT_SECONDS": {},
	}
	if !reflect.DeepEqual(gotVariables, wantVariables) {
		return errors.New("gateway environment variable set is not closed")
	}
	lower := strings.ToLower(contents)
	for _, forbidden := range []string{
		"stream=true", "stream: true", "submit_tool_outputs", "function_call_output",
		`"type": "function"`, `type: "function"`, "debug=true", "debug: true",
	} {
		if strings.Contains(lower, forbidden) {
			return errors.New("source contains forbidden behavior")
		}
	}
	for _, line := range strings.Split(contents, "\n") {
		trimmed := strings.ReplaceAll(strings.TrimSpace(line), " ", "")
		if strings.HasPrefix(trimmed, "tools=") && trimmed != "tools=[]," ||
			strings.HasPrefix(trimmed, "tools:") && trimmed != "tools:[]," {
			return errors.New("source contains a nonempty or indirect tools request")
		}
	}
	return nil
}

func hasExecutableSDKAssertion(contents, marker string) bool {
	for _, line := range strings.Split(contents, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), marker) {
			return true
		}
	}
	return false
}

func TestOfficialSDKExamplesContractRejectsMutations(t *testing.T) {
	tests := []struct {
		name        string
		language    string
		path        string
		original    string
		replacement string
	}{
		{"python logging suppression", "Python", "examples/openai-sdk/python/main.py", `os.environ.pop("OPENAI_LOG", None)`, `os.environ.get("OPENAI_LOG")`},
		{"python timeout length", "Python", "examples/openai-sdk/python/main.py", `if len(value) > 3:`, `if len(value) > 4:`},
		{"python timeout maximum", "Python", "examples/openai-sdk/python/main.py", `seconds > 300`, `seconds > 301`},
		{"python retries", "Python", "examples/openai-sdk/python/main.py", `"max_retries": 0`, `"max_retries": 1`},
		{"python models object", "Python", "examples/openai-sdk/python/main.py", `assert models.object == "list"`, `assert models.object == "collection"`},
		{"python models object commented", "Python", "examples/openai-sdk/python/main.py", `    assert models.object == "list"`, `    # assert models.object == "list"`},
		{"python model alias selection", "Python", "examples/openai-sdk/python/main.py", `matching_models = [model for model in models.data if model.id == model_name]`, `matching_models = [model for model in models.data if model.id == "codex-sdk-test"]`},
		{"python exact model alias count", "Python", "examples/openai-sdk/python/main.py", `assert len(matching_models) == 1`, `assert len(matching_models) > 0`},
		{"python request instruction", "Python", "examples/openai-sdk/python/main.py", `instructions="Return only the exact text requested."`, `instructions="changed"`},
		{"python request input", "Python", "examples/openai-sdk/python/main.py", `input="Reply with exactly: SDK_GATEWAY_OK"`, `input="changed"`},
		{"python request store", "Python", "examples/openai-sdk/python/main.py", `store=False`, `store=True`},
		{"python response role", "Python", "examples/openai-sdk/python/main.py", `assert message.role == "assistant"`, `assert message.role == "user"`},
		{"python response text", "Python", "examples/openai-sdk/python/main.py", `assert value in ("SDK_GATEWAY_OK", "SDK_GATEWAY_OK\n")`, `assert value.startswith("SDK_GATEWAY_OK")`},
		{"python fixed error", "Python", "examples/openai-sdk/python/main.py", `sdk_contract_error: python_assertion`, `sdk_contract_error: changed`},
		{"python fixed success", "Python", "examples/openai-sdk/python/main.py", `python_sdk_contract_ok`, `python_contract_changed`},
		{"python API status integer", "Python", "examples/openai-sdk/python/main.py", `isinstance(status, int)`, `status is not None`},
		{"python API status boolean", "Python", "examples/openai-sdk/python/main.py", ` and not isinstance(status, bool)`, ``},
		{"python API status lower bound", "Python", "examples/openai-sdk/python/main.py", `400 <= status`, `399 <= status`},
		{"python API status upper bound", "Python", "examples/openai-sdk/python/main.py", `status <= 599`, `status <= 600`},
		{"python API status exception disclosure", "Python", "examples/openai-sdk/python/main.py", `return "unknown"`, "return str(error)\n    return \"unknown\""},
		{"python generic exception disclosure", "Python", "examples/openai-sdk/python/main.py", `print("sdk_contract_error: python_assertion", file=sys.stderr)`, "print(error, file=sys.stderr)\n        print(\"sdk_contract_error: python_assertion\", file=sys.stderr)"},
		{"python generic exception interpolation", "Python", "examples/openai-sdk/python/main.py", "except Exception:\n        print(\"sdk_contract_error: python_assertion\", file=sys.stderr)", "except Exception as error:\n        print(f\"{error}\", file=sys.stderr)\n        print(\"sdk_contract_error: python_assertion\", file=sys.stderr)"},
		{"javascript logging suppression", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `logLevel: "off"`, `logLevel: "warn"`},
		{"javascript timeout maximum", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `seconds > 300`, `seconds > 301`},
		{"javascript retries", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `maxRetries: 0`, `maxRetries: 1`},
		{"javascript models object", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `assert.equal(models.object, "list")`, `assert.equal(models.object, "collection")`},
		{"javascript models object commented", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `  assert.equal(models.object, "list");`, `  // assert.equal(models.object, "list");`},
		{"javascript model alias selection", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `model.id === modelName`, `model.id === "codex-sdk-test"`},
		{"javascript exact model alias count", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `assert.equal(matchingModels.length, 1)`, `assert.equal(matchingModels.length > 0, true)`},
		{"javascript request instruction", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `instructions: "Return only the exact text requested."`, `instructions: "changed"`},
		{"javascript request input", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `input: "Reply with exactly: SDK_GATEWAY_OK"`, `input: "changed"`},
		{"javascript request tools", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `tools: []`, `tools: [{ type: "function" }]`},
		{"javascript response role", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `assert.equal(message.role, "assistant")`, `assert.equal(message.role, "user")`},
		{"javascript response text", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `assert.equal(value === "SDK_GATEWAY_OK" || value === "SDK_GATEWAY_OK\n", true)`, `assert.equal(value.startsWith("SDK_GATEWAY_OK"), true)`},
		{"javascript fixed error", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `sdk_contract_error: javascript_assertion`, `sdk_contract_error: changed`},
		{"javascript fixed success", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `javascript_sdk_contract_ok`, `javascript_contract_changed`},
		{"javascript API status integer", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `Number.isSafeInteger(error.status)`, `Number.isFinite(error.status)`},
		{"javascript API status lower bound", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `error.status >= 400`, `error.status >= 399`},
		{"javascript API status upper bound", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `error.status <= 599`, `error.status <= 600`},
		{"javascript API status exception disclosure", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `return "unknown"`, "return String(error);\n  return \"unknown\""},
		{"javascript generic exception disclosure", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `console.error("sdk_contract_error: javascript_assertion")`, "console.error(error);\n    console.error(\"sdk_contract_error: javascript_assertion\")"},
		{"javascript generic exception console log", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `console.error("sdk_contract_error: javascript_assertion")`, "console.log(error);\n    console.error(\"sdk_contract_error: javascript_assertion\")"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			contents := string(readRepositoryFile(t, test.path))
			if count := strings.Count(contents, test.original); count != 1 {
				t.Fatalf("mutation target occurs %d times, want exactly once", count)
			}
			mutated := strings.Replace(contents, test.original, test.replacement, 1)
			if err := validateOfficialSDKSource(test.language, mutated); err == nil {
				t.Fatal("source contract accepted a security/compatibility mutation")
			}
		})
	}
}

func TestOfficialSDKExamplesSuppressHostileLogging(t *testing.T) {
	t.Run("Python", func(t *testing.T) {
		python := lookPathForSDKTest(t, "python3", "python")
		root := t.TempDir()
		writeFixtureFile(t, root, "openai/__init__.py", []byte(pythonSDKContractStub))
		pythonSource := filepath.Join(root, "main.py")
		writeFixtureFile(t, root, "main.py", readRepositoryFile(t, "examples/openai-sdk/python/main.py"))
		runSDKClientCases(t, python, pythonSource, append(sdkTestEnvironment(),
			"PYTHONPATH="+root,
			"PYTHONPYCACHEPREFIX="+filepath.Join(root, "python-cache"),
		), []sdkClientCase{
			{name: "missing", output: "sdk_contract_error: missing AI_CLI_GATEWAY_BASE_URL\n", exitCode: 1},
			{name: "non-ASCII timeout", environment: sdkValidEnvironment("٣"), output: "sdk_contract_error: invalid AI_CLI_GATEWAY_TIMEOUT_SECONDS\n", exitCode: 1},
			{name: "oversized timeout", environment: sdkValidEnvironment(strings.Repeat("9", 5000)), output: "sdk_contract_error: invalid AI_CLI_GATEWAY_TIMEOUT_SECONDS\n", exitCode: 1},
			{name: "success", environment: sdkValidEnvironment("5"), output: "python_sdk_contract_ok\n", exitCode: 0},
			{name: "success without trailing newline", environment: sdkOutputEnvironment("plain"), output: "python_sdk_contract_ok\n", exitCode: 0},
			{name: "reject double trailing newline", environment: sdkOutputEnvironment("double-newline"), output: "sdk_contract_error: python_assertion\n", exitCode: 1},
			{name: "reject extra output", environment: sdkOutputEnvironment("extra"), output: "sdk_contract_error: python_assertion\n", exitCode: 1},
			{name: "model alias missing", environment: sdkModelsDataEnvironment("missing"), output: "sdk_contract_error: python_assertion\n", exitCode: 1},
			{name: "model alias duplicated", environment: sdkModelsDataEnvironment("duplicate"), output: "sdk_contract_error: python_assertion\n", exitCode: 1},
			{name: "models API status 400", environment: sdkAPIErrorEnvironment("models:400"), output: "sdk_contract_error: python_api 400\n", exitCode: 1},
			{name: "responses API status 599", environment: sdkAPIErrorEnvironment("responses:599"), output: "sdk_contract_error: python_api 599\n", exitCode: 1},
			{name: "API status missing", environment: sdkAPIErrorEnvironment("models:missing"), output: "sdk_contract_error: python_api unknown\n", exitCode: 1},
			{name: "API status non-integer", environment: sdkAPIErrorEnvironment("responses:string"), output: "sdk_contract_error: python_api unknown\n", exitCode: 1},
			{name: "API status boolean", environment: sdkAPIErrorEnvironment("models:boolean"), output: "sdk_contract_error: python_api unknown\n", exitCode: 1},
			{name: "API status below range", environment: sdkAPIErrorEnvironment("responses:399"), output: "sdk_contract_error: python_api unknown\n", exitCode: 1},
			{name: "API status above range", environment: sdkAPIErrorEnvironment("models:600"), output: "sdk_contract_error: python_api unknown\n", exitCode: 1},
			{name: "models object missing", environment: sdkModelsObjectEnvironment("missing"), output: "sdk_contract_error: python_assertion\n", exitCode: 1},
			{name: "models object wrong", environment: sdkModelsObjectEnvironment("wrong"), output: "sdk_contract_error: python_assertion\n", exitCode: 1},
			{name: "models generic exception", environment: sdkGenericErrorEnvironment("models"), output: "sdk_contract_error: python_assertion\n", exitCode: 1},
			{name: "responses generic exception", environment: sdkGenericErrorEnvironment("responses"), output: "sdk_contract_error: python_assertion\n", exitCode: 1},
		})
	})

	t.Run("JavaScript", func(t *testing.T) {
		node := lookPathForSDKTest(t, "node")
		root := t.TempDir()
		writeFixtureFile(t, root, "package.json", []byte(`{"private":true,"type":"module"}`))
		writeFixtureFile(t, root, "node_modules/openai/package.json", []byte(`{"name":"openai","type":"module","exports":"./index.mjs"}`))
		writeFixtureFile(t, root, "node_modules/openai/index.mjs", []byte(javaScriptSDKContractStub))
		javaScriptSource := filepath.Join(root, "main.mjs")
		writeFixtureFile(t, root, "main.mjs", readRepositoryFile(t, "examples/openai-sdk/javascript/main.mjs"))
		runSDKClientCases(t, node, javaScriptSource, sdkTestEnvironment(), []sdkClientCase{
			{name: "missing", output: "sdk_contract_error: missing AI_CLI_GATEWAY_BASE_URL\n", exitCode: 1},
			{name: "non-ASCII timeout", environment: sdkValidEnvironment("٣"), output: "sdk_contract_error: invalid AI_CLI_GATEWAY_TIMEOUT_SECONDS\n", exitCode: 1},
			{name: "oversized timeout", environment: sdkValidEnvironment(strings.Repeat("9", 5000)), output: "sdk_contract_error: invalid AI_CLI_GATEWAY_TIMEOUT_SECONDS\n", exitCode: 1},
			{name: "success", environment: sdkValidEnvironment("5"), output: "javascript_sdk_contract_ok\n", exitCode: 0},
			{name: "success without trailing newline", environment: sdkOutputEnvironment("plain"), output: "javascript_sdk_contract_ok\n", exitCode: 0},
			{name: "reject double trailing newline", environment: sdkOutputEnvironment("double-newline"), output: "sdk_contract_error: javascript_assertion\n", exitCode: 1},
			{name: "reject extra output", environment: sdkOutputEnvironment("extra"), output: "sdk_contract_error: javascript_assertion\n", exitCode: 1},
			{name: "model alias missing", environment: sdkModelsDataEnvironment("missing"), output: "sdk_contract_error: javascript_assertion\n", exitCode: 1},
			{name: "model alias duplicated", environment: sdkModelsDataEnvironment("duplicate"), output: "sdk_contract_error: javascript_assertion\n", exitCode: 1},
			{name: "models API status 400", environment: sdkAPIErrorEnvironment("models:400"), output: "sdk_contract_error: javascript_api 400\n", exitCode: 1},
			{name: "responses API status 599", environment: sdkAPIErrorEnvironment("responses:599"), output: "sdk_contract_error: javascript_api 599\n", exitCode: 1},
			{name: "API status missing", environment: sdkAPIErrorEnvironment("models:missing"), output: "sdk_contract_error: javascript_api unknown\n", exitCode: 1},
			{name: "API status non-integer", environment: sdkAPIErrorEnvironment("responses:string"), output: "sdk_contract_error: javascript_api unknown\n", exitCode: 1},
			{name: "API status boolean", environment: sdkAPIErrorEnvironment("models:boolean"), output: "sdk_contract_error: javascript_api unknown\n", exitCode: 1},
			{name: "API status below range", environment: sdkAPIErrorEnvironment("responses:399"), output: "sdk_contract_error: javascript_api unknown\n", exitCode: 1},
			{name: "API status above range", environment: sdkAPIErrorEnvironment("models:600"), output: "sdk_contract_error: javascript_api unknown\n", exitCode: 1},
			{name: "models object missing", environment: sdkModelsObjectEnvironment("missing"), output: "sdk_contract_error: javascript_assertion\n", exitCode: 1},
			{name: "models object wrong", environment: sdkModelsObjectEnvironment("wrong"), output: "sdk_contract_error: javascript_assertion\n", exitCode: 1},
			{name: "models generic exception", environment: sdkGenericErrorEnvironment("models"), output: "sdk_contract_error: javascript_assertion\n", exitCode: 1},
			{name: "responses generic exception", environment: sdkGenericErrorEnvironment("responses"), output: "sdk_contract_error: javascript_assertion\n", exitCode: 1},
		})
	})
}

func TestSDKContractScript(t *testing.T) {
	root, err := repositoryScanRoot("")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	path := filepath.Join(root, "scripts", "sdk-contract.sh")
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat sdk-contract script: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Fatalf("script mode = %v, want a regular file", info.Mode())
	}
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0o755 {
		t.Fatalf("script mode = %v, want regular 0755 on POSIX", info.Mode())
	}
	contents, err := os.ReadFile(path) //nolint:gosec // fixed repository-owned script.
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	text := string(contents)
	if !strings.HasPrefix(text, "#!/bin/sh\nset -eu\n") {
		t.Fatal("script does not use the closed POSIX shell prologue")
	}
	for _, forbidden := range []string{"eval ", "sh -c", "bash -c", "ANTHROPIC_", "GEMINI_", "OPENAI_", "Return only the exact text requested.", "Reply with exactly: SDK_GATEWAY_OK", "AI_CLI_GATEWAY_API_KEY="} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("script contains forbidden construct %q", forbidden)
		}
	}
	if strings.Count(text, "exec go run -trimpath ./internal/sdkcontract/cmd/sdk-contract") != 1 {
		t.Fatal("script does not contain exactly one closed Go runner exec")
	}
	if runtime.GOOS != "windows" {
		command := exec.CommandContext(context.Background(), path) //nolint:gosec // fixed repository-owned executable under test.
		command.Env = []string{"PATH=" + os.Getenv("PATH")}
		var stdout, stderr bytes.Buffer
		command.Stdout, command.Stderr = &stdout, &stderr
		err = command.Run()
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 2 || stdout.Len() != 0 || stderr.String() != "usage: scripts/sdk-contract.sh PYTHON NODE JAVASCRIPT\n" {
			t.Fatalf("zero-argument script result err=%v stdout=%q stderr=%q", err, stdout.String(), stderr.String())
		}
	}
}

func readGettingStarted(t *testing.T) string {
	t.Helper()
	return string(readRepositoryFile(t, "docs/getting-started.md"))
}

func TestReferenceGatewayKeyWindowsRuntimePathPolicy(t *testing.T) {
	contents := string(readRepositoryFile(t, "docs/reference.md"))
	for _, required := range []string{
		"runtime loading of `server.api_key_file` requires a drive-qualified, drive-absolute path on a fixed local drive",
		"UNC, network, mapped, removable, and reparse locations are rejected",
	} {
		if !strings.Contains(contents, required) {
			t.Fatalf("reference is missing Windows gateway-key runtime path policy %q", required)
		}
	}
}

func TestSDKContractRecoveryGuidance(t *testing.T) {
	contents := readGettingStarted(t)
	for _, required := range []string{
		"sdk_contract_cleanup_failed",
		"owner-only `.sdk-contract-*` sibling",
		"ensure no recorded contract process remains",
		"remove only the exact retained directory",
		"never prints the retained path or underlying error",
	} {
		if !strings.Contains(contents, required) {
			t.Fatalf("getting-started guide is missing SDK recovery guidance %q", required)
		}
	}
}

type sdkClientCase struct {
	name        string
	environment []string
	output      string
	exitCode    int
}

func lookPathForSDKTest(t *testing.T, names ...string) string {
	t.Helper()
	for _, name := range names {
		path, err := exec.LookPath(name)
		if err == nil {
			return path
		}
	}
	t.Skip("required SDK example runtime is not installed")
	return ""
}

func sdkTestEnvironment() []string {
	environment := []string{"OPENAI_LOG=debug"}
	for _, name := range []string{"PATH", "SystemRoot", "WINDIR", "ComSpec", "PATHEXT"} {
		if value := os.Getenv(name); value != "" {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func sdkValidEnvironment(timeout string) []string {
	return []string{
		"AI_CLI_GATEWAY_BASE_URL=http://127.0.0.1:1/v1",
		"AI_CLI_GATEWAY_API_" + "KEY=fixture-only",
		"AI_CLI_GATEWAY_MODEL=codex-local",
		"AI_CLI_GATEWAY_TIMEOUT_SECONDS=" + timeout,
	}
}

func sdkAPIErrorEnvironment(mode string) []string {
	return append(sdkValidEnvironment("5"), "SDK_CONTRACT_FAKE_API_ERROR="+mode)
}

func sdkGenericErrorEnvironment(location string) []string {
	return append(sdkValidEnvironment("5"), "SDK_CONTRACT_FAKE_GENERIC_ERROR="+location)
}

func sdkModelsObjectEnvironment(mode string) []string {
	return append(sdkValidEnvironment("5"), "SDK_CONTRACT_FAKE_MODELS_OBJECT="+mode)
}

func sdkModelsDataEnvironment(mode string) []string {
	return append(sdkValidEnvironment("5"), "SDK_CONTRACT_FAKE_MODELS_DATA="+mode)
}

func sdkOutputEnvironment(mode string) []string {
	return append(sdkValidEnvironment("5"), "SDK_CONTRACT_FAKE_OUTPUT="+mode)
}

func runSDKClientCases(t *testing.T, executable, source string, baseEnvironment []string, cases []sdkClientCase) {
	t.Helper()
	for _, test := range cases {
		t.Run(filepath.Base(source)+" "+test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, source) //nolint:gosec // Fixed test runtime and repository-owned copied source.
			command.Env = append(append([]string{}, baseEnvironment...), test.environment...)
			output, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal("SDK fixture client exceeded its local execution deadline")
			}
			exitCode := 0
			if err != nil {
				var exitError *exec.ExitError
				if !errors.As(err, &exitError) {
					t.Fatalf("run SDK fixture client: %v", err)
				}
				exitCode = exitError.ExitCode()
			}
			if exitCode != test.exitCode {
				t.Fatalf("exit code = %d, want %d", exitCode, test.exitCode)
			}
			if !sdkClientOutputMatches(output, test.output, runtime.GOOS) {
				t.Fatalf("combined output = %q, want exactly one fixed line %q", output, test.output)
			}
		})
	}
}

func sdkClientOutputMatches(output []byte, want, goos string) bool {
	if string(output) == want {
		return true
	}
	return goos == "windows" && string(output) == strings.ReplaceAll(want, "\n", "\r\n")
}

func TestSDKClientOutputMatchesNativeLineEndings(t *testing.T) {
	tests := []struct {
		name   string
		output string
		want   string
		goos   string
		match  bool
	}{
		{name: "POSIX LF", output: "fixed\n", want: "fixed\n", goos: "linux", match: true},
		{name: "POSIX rejects CRLF", output: "fixed\r\n", want: "fixed\n", goos: "linux", match: false},
		{name: "Windows LF", output: "fixed\n", want: "fixed\n", goos: "windows", match: true},
		{name: "Windows CRLF", output: "fixed\r\n", want: "fixed\n", goos: "windows", match: true},
		{name: "Windows rejects extra line", output: "fixed\r\nextra\r\n", want: "fixed\n", goos: "windows", match: false},
		{name: "Windows rejects lone CR", output: "fixed\r", want: "fixed\n", goos: "windows", match: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := sdkClientOutputMatches([]byte(test.output), test.want, test.goos); got != test.match {
				t.Fatalf("sdkClientOutputMatches(%q, %q, %q) = %v, want %v", test.output, test.want, test.goos, got, test.match)
			}
		})
	}
}

const pythonSDKContractStub = `import os
import sys

if os.environ.get("OPENAI_LOG") == "debug":
    print("python_sdk_debug", file=sys.stderr)

class APIError(Exception):
    pass

def raise_generic_error(location):
    if os.environ.get("SDK_CONTRACT_FAKE_GENERIC_ERROR") == location:
        raise RuntimeError("planted-sensitive-generic-exception-message")

def raise_api_error(location):
    mode = os.environ.get("SDK_CONTRACT_FAKE_API_ERROR")
    if mode is None or not mode.startswith(location + ":"):
        return
    kind = mode.split(":", 1)[1]
    error = APIError("planted-sensitive-exception-message")
    statuses = {
        "400": 400,
        "599": 599,
        "string": "400",
        "boolean": True,
        "399": 399,
        "600": 600,
    }
    if kind != "missing":
        error.status_code = statuses[kind]
    raise error

class Value:
    def __init__(self, **fields):
        self.__dict__.update(fields)
        self.model_fields_set = set(fields)

class Models:
    def list(self):
        raise_generic_error("models")
        raise_api_error("models")
        data = [
            Value(id="decoy-local", object="model", created=0, owned_by="local"),
            Value(id="codex-local", object="model", created=0, owned_by="local"),
        ]
        data_mode = os.environ.get("SDK_CONTRACT_FAKE_MODELS_DATA")
        if data_mode == "missing":
            data = data[:1]
        elif data_mode == "duplicate":
            data.append(Value(id="codex-local", object="model", created=0, owned_by="local"))
        mode = os.environ.get("SDK_CONTRACT_FAKE_MODELS_OBJECT")
        if mode == "missing":
            return Value(data=data)
        return Value(object="collection" if mode == "wrong" else "list", data=data)

class Responses:
    def create(self, **request):
        expected = {
            "model": "codex-local",
            "instructions": "Return only the exact text requested.",
            "input": "Reply with exactly: SDK_GATEWAY_OK",
            "text": {"format": {"type": "text"}},
            "stream": False,
            "store": False,
            "tools": [],
            "tool_choice": "none",
        }
        if request != expected:
            raise AssertionError
        raise_generic_error("responses")
        raise_api_error("responses")
        output = {
            "plain": "SDK_GATEWAY_OK",
            "double-newline": "SDK_GATEWAY_OK\n\n",
            "extra": "SDK_GATEWAY_OK extra",
        }.get(os.environ.get("SDK_CONTRACT_FAKE_OUTPUT"), "SDK_GATEWAY_OK\n")
        content = Value(type="output_text", annotations=[], text=output)
        message = Value(id="msg_fixture", type="message", status="completed", role="assistant", content=[content])
        text = Value(format=Value(type="text"))
        response = Value(
            id="resp_fixture", object="response", created_at=1.0, completed_at=2.0, status="completed",
            background=False, error=None, incomplete_details=None, instructions="Return only the exact text requested.",
            model="codex-local", output=[message], parallel_tool_calls=False, previous_response_id=None,
            text=text, tools=[], tool_choice="none",
        )
        response.store = False
        response._request_id = "req_fixture"
        return response

class OpenAI:
    def __init__(self, **options):
        if os.environ.get("OPENAI_LOG") == "debug":
            print("python_client_debug", file=sys.stderr)
        if options.get("max_retries") != 0 or options.get("timeout") != 5.0:
            raise AssertionError
        self.models = Models()
        self.responses = Responses()
`

const javaScriptSDKContractStub = `class APIError extends Error {}

function raiseGenericError(location) {
  if (process.env.SDK_CONTRACT_FAKE_GENERIC_ERROR === location) {
    throw new Error("planted-sensitive-generic-exception-message");
  }
}

function raiseAPIError(location) {
  const mode = process.env.SDK_CONTRACT_FAKE_API_ERROR;
  if (mode === undefined || !mode.startsWith(location + ":")) {
    return;
  }
  const kind = mode.slice(location.length + 1);
  const error = new APIError("planted-sensitive-exception-message");
  const statuses = {
    "400": 400,
    "599": 599,
    string: "400",
    boolean: true,
    "399": 399,
    "600": 600,
  };
  if (kind !== "missing") {
    error.status = statuses[kind];
  }
  throw error;
}

class OpenAI {
  constructor(options) {
    if (process.env.OPENAI_LOG === "debug" && options.logLevel !== "off") {
      console.error("javascript_sdk_debug");
    }
    if (options.maxRetries !== 0 || options.timeout !== 5_000) {
      throw new Error();
    }
    this.models = {
      list: async () => {
        raiseGenericError("models");
        raiseAPIError("models");
        const data = [
          { id: "decoy-local", object: "model", created: 0, owned_by: "local" },
          { id: "codex-local", object: "model", created: 0, owned_by: "local" },
        ];
        const dataMode = process.env.SDK_CONTRACT_FAKE_MODELS_DATA;
        if (dataMode === "missing") {
          data.splice(1);
        } else if (dataMode === "duplicate") {
          data.push({ id: "codex-local", object: "model", created: 0, owned_by: "local" });
        }
        const mode = process.env.SDK_CONTRACT_FAKE_MODELS_OBJECT;
        if (mode === "missing") {
          return { data };
        }
        return { object: mode === "wrong" ? "collection" : "list", data };
      },
    };
    this.responses = {
      create: async (request) => {
        const expected = {
          model: "codex-local",
          instructions: "Return only the exact text requested.",
          input: "Reply with exactly: SDK_GATEWAY_OK",
          text: { format: { type: "text" } },
          stream: false,
          store: false,
          tools: [],
          tool_choice: "none",
        };
        if (JSON.stringify(request) !== JSON.stringify(expected)) {
          throw new Error();
        }
        raiseGenericError("responses");
        raiseAPIError("responses");
        const output = {
          plain: "SDK_GATEWAY_OK",
          "double-newline": "SDK_GATEWAY_OK\n\n",
          extra: "SDK_GATEWAY_OK extra",
        }[process.env.SDK_CONTRACT_FAKE_OUTPUT] ?? "SDK_GATEWAY_OK\n";
        const response = {
          id: "resp_fixture",
          object: "response",
          created_at: 1,
          completed_at: 2,
          status: "completed",
          background: false,
          error: null,
          incomplete_details: null,
          instructions: "Return only the exact text requested.",
          model: "codex-local",
          output: [{
            id: "msg_fixture",
            type: "message",
            status: "completed",
            role: "assistant",
            content: [{ type: "output_text", annotations: [], text: output }],
          }],
          output_text: output,
          parallel_tool_calls: false,
          previous_response_id: null,
          store: false,
          text: { format: { type: "text" } },
          tools: [],
          tool_choice: "none",
        };
        Object.defineProperty(response, "_request_id", { value: "req_fixture", enumerable: false });
        return response;
      },
    };
  }
}

OpenAI.APIError = APIError;
export default OpenAI;
`

func TestGettingStartedReleaseQuickStart(t *testing.T) {
	readme := readGettingStarted(t)
	if err := validateREADMEReleaseQuickStart(readme); err != nil {
		t.Fatalf("README release Quick Start contract: %v", err)
	}
}

func TestGettingStartedWindowsACLProgram(t *testing.T) {
	document, err := parseREADMEQuickStart(readGettingStarted(t))
	if err != nil {
		t.Fatal(err)
	}
	windows := quickStartFences(document, quickStartWindowsSection, "powershell")
	if len(windows) != 6 {
		t.Fatalf("PowerShell fence count = %d, want six", len(windows))
	}
	if err := validateREADMEWindowsACLProgram(windows[3]); err != nil {
		t.Fatalf("README Windows ACL program is not a closed exact-rule program: %v", err)
	}
}

func TestGettingStartedReleaseQuickStartRejectsMutations(t *testing.T) {
	readme := readGettingStarted(t)
	if err := validateREADMEReleaseQuickStart(readme); err != nil {
		t.Fatalf("baseline README Quick Start must be valid before mutation checks: %v", err)
	}
	tests := []struct {
		name     string
		sealOnly bool
		mutate   func(string) (string, error)
	}{
		{name: "broad POSIX filename regex", mutate: replaceREADMEOnce(`length($0) == 66 + length(name) && substr($0, 65) == " *" name`, `$0 ~ name`)},
		{name: "commented POSIX exact count", mutate: replaceREADMEOnce(`      if (matches != 1) exit 1`, `      # if (matches != 1) exit 1`)},
		{name: "POSIX digest merely nonempty", mutate: replaceREADMEOnce(`test "${ACTUAL_SHA}" = "${EXPECTED_SHA}"`, `test -n "${ACTUAL_SHA}"`)},
		{name: "broad PowerShell checksum regex", mutate: replaceREADMEOnce(`$ArchivePattern = '^[0-9a-f]{64} \*' + [regex]::Escape($ArchiveName) + '$'`, `$ArchivePattern = $ArchiveName`)},
		{name: "PowerShell checksum comparison is inert", mutate: replaceREADMEOnce(`if (-not [String]::Equals($ExpectedSHA, $ActualSHA, [StringComparison]::OrdinalIgnoreCase)) {`, `[StringComparison]::OrdinalIgnoreCase | Out-Null`+"\n"+`if ($false) {`)},
		{name: "printed POSIX key", mutate: replaceREADMENth(`export AI_CLI_GATEWAY_API_KEY="${GATEWAY_KEY}"`, `export AI_CLI_GATEWAY_API_KEY="${GATEWAY_KEY}"`+"\n"+`printf '%s\n' "${GATEWAY_KEY}"`, 2)},
		{name: "commented POSIX terminal load", mutate: replaceREADMENth(`GATEWAY_KEY="$(LC_ALL=C tr -d '\n' < "${GATEWAY_CONFIG_DIR}/gateway.key")"`, `# GATEWAY_KEY="$(LC_ALL=C tr -d '\n' < "${GATEWAY_CONFIG_DIR}/gateway.key")"`, 2)},
		{name: "POSIX terminal load in dead branch", mutate: replaceREADMENth(`GATEWAY_KEY="$(LC_ALL=C tr -d '\n' < "${GATEWAY_CONFIG_DIR}/gateway.key")"`, "if false; then\n  "+`GATEWAY_KEY="$(LC_ALL=C tr -d '\n' < "${GATEWAY_CONFIG_DIR}/gateway.key")"`+"\nfi", 2)},
		{name: "bare PowerShell terminal key", mutate: replaceREADMENth(`$LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()`, `$LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()`+"\n"+`$LoadedGatewayKey`, 2)},
		{name: "PowerShell terminal key piped to host", mutate: replaceREADMENth(`$LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()`, `$LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()`+"\n"+`$LoadedGatewayKey | Out-Host`, 2)},
		{name: "indirect POSIX key file output", sealOnly: true, mutate: replaceREADMEOnce(`ai-cli-gateway doctor --config "${GATEWAY_CONFIG_FILE}"`, `cat "${GATEWAY_CONFIG_DIR}"/*`+"\n"+`ai-cli-gateway doctor --config "${GATEWAY_CONFIG_FILE}"`)},
		{name: "indirect PowerShell key file output", sealOnly: true, mutate: replaceREADMEOnce(`ai-cli-gateway.exe doctor --config $GatewayConfigFile`, `Get-ChildItem $GatewayConfigDir | Get-Content | Out-Host`+"\n"+`ai-cli-gateway.exe doctor --config $GatewayConfigFile`)},
		{name: "comment-only fence source change", sealOnly: true, mutate: replaceREADMEOnce("```bash\nset -eu\nVERSION=0.1.0", "```bash\nset -eu\n# sealed source changed\nVERSION=0.1.0")},
		{name: "early POSIX fence prints key", mutate: replaceREADMEOnce("```bash\nset -eu\nVERSION=0.1.0", "```bash\nset -eu\nprintf '%s\\n' \"${GATEWAY_KEY}\"\nVERSION=0.1.0")},
		{name: "unexpected shell fence prints key", mutate: replaceREADMEOnce("### Official SDK checks\n", "```sh\nprintf '%s\\n' \"${GATEWAY_KEY}\"\n```\n\n### Official SDK checks\n")},
		{name: "tilde PowerShell fence prints key", mutate: replaceREADMEOnce("### Official SDK checks\n", "~~~powershell\n$LoadedGatewayKey | Out-Host\n~~~\n\n### Official SDK checks\n")},
		{name: "first Bash fence changed to text", mutate: replaceREADMEOnce("```bash\nset -eu\nVERSION=0.1.0", "```text\nset -eu\nVERSION=0.1.0")},
		{name: "Darwin ARM64 branch removed", mutate: replaceREADMEOnce(`  Darwin:arm64) ASSET="ai-cli-gateway_${VERSION}_darwin_arm64.tar.gz" ;;`+"\n", "")},
		{name: "Darwin ARM64 branch moved to unused function", mutate: moveREADMEHostBranchToUnusedFunction},
		{name: "POSIX substitution commented", mutate: replaceREADMEOnce(`    [q{configured-provider-model}, $ENV{CODEX_MODEL}],`, `    # [q{configured-provider-model}, $ENV{CODEX_MODEL}],`)},
		{name: "POSIX chmod commented", mutate: replaceREADMEOnce(`chmod 700 "${GATEWAY_CONFIG_DIR}" "${GATEWAY_RUNTIME_DIR}" "${CODEX_CONFIG_HOME}"`, `# chmod 700 "${GATEWAY_CONFIG_DIR}" "${GATEWAY_RUNTIME_DIR}" "${CODEX_CONFIG_HOME}"`)},
		{name: "models curl commented", mutate: replaceREADMENth("curl --fail-with-body \\\n  -H \"Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}\" \\\n  http://127.0.0.1:8080/v1/models", "# curl --fail-with-body \\\n#   -H \"Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}\" \\\n#   http://127.0.0.1:8080/v1/models", 1)},
		{name: "Node SDK command commented", mutate: replaceREADMEOnce(`node "${SDK_WORK_ROOT}/javascript/main.mjs"`, `# node "${SDK_WORK_ROOT}/javascript/main.mjs"`)},
		{name: "Node SDK command in dead branch", mutate: replaceREADMEOnce(`node "${SDK_WORK_ROOT}/javascript/main.mjs"`, "if false; then\n  "+`node "${SDK_WORK_ROOT}/javascript/main.mjs"`+"\nfi")},
		{name: "SDK fence prints gateway key", mutate: replaceREADMEOnce(`node "${SDK_WORK_ROOT}/javascript/main.mjs"`, `node "${SDK_WORK_ROOT}/javascript/main.mjs"`+"\n"+`printf '%s\n' "${AI_CLI_GATEWAY_API_KEY}"`)},
		{name: "systemd link moved to Windows", mutate: moveREADMESystemdLink},
		{name: "raw systemd path added to Windows", mutate: replaceREADMEOnce(`Use PowerShell 7 as an unprivileged gateway identity.`, `Use PowerShell 7 as an unprivileged gateway identity. See deploy/systemd/ai-cli-gateway.service.`)},
		{name: "encoded systemd path added to Windows", mutate: replaceREADMEOnce(`Use PowerShell 7 as an unprivileged gateway identity.`, `Use PowerShell 7 as an unprivileged gateway identity. See [Linux service unit](deploy/%73ystemd/ai-cli-gateway.service).`)},
		{name: "PowerShell accepts preexisting target", mutate: replaceREADMEOnce(`if (Test-Path -LiteralPath $FreshTarget) { throw 'private target already exists' }`, `if (Test-Path -LiteralPath $FreshTarget) { Write-Output 'reusing target' }`)},
		{name: "PowerShell directory right weakened", mutate: replaceREADMEOnce(`[Security.AccessControl.FileSystemRights]::FullControl`, `[Security.AccessControl.FileSystemRights]::Read`)},
		{name: "PowerShell file Synchronize omitted", mutate: replaceREADMEOnce("    [Security.AccessControl.FileSystemRights]::Synchronize\n", "")},
		{name: "PowerShell preserves inherited ACEs", mutate: replaceREADMEOnce(`$ACL.SetAccessRuleProtection($true, $false)`, `$ACL.SetAccessRuleProtection($true, $true)`)},
		{name: "PowerShell removes only one matching ACE", mutate: replaceREADMEOnce(`$ACL.RemoveAccessRuleAll($ExistingRule)`, `$ACL.RemoveAccessRule($ExistingRule) | Out-Null`)},
		{name: "PowerShell current owner omitted", mutate: replaceREADMEOnce(`$ACL.SetOwner($CurrentSIDObject)`, `# $ACL.SetOwner($CurrentSIDObject)`)},
		{name: "PowerShell key exact ACL setter is inert", mutate: replaceREADMEOnce(`Set-ExactPrivateFileACL $GatewayKeyPath`, `$null = 'Set-ExactPrivateFileACL $GatewayKeyPath'`)},
		{name: "PowerShell key exact ACL setter in dead branch", mutate: replaceREADMEOnce(`Set-ExactPrivateFileACL $GatewayKeyPath`, "if ($false) {\n  Set-ExactPrivateFileACL $GatewayKeyPath\n}")},
		{name: "PowerShell directory exact ACL loop in dead branch", mutate: replaceREADMEOnce("foreach ($PrivateDir in @($GatewayConfigDir, $GatewayRuntimeDir)) {\n  Set-ExactPrivateDirectoryACL $PrivateDir\n}", "if ($false) {\n  foreach ($PrivateDir in @($GatewayConfigDir, $GatewayRuntimeDir)) {\n    Set-ExactPrivateDirectoryACL $PrivateDir\n  }\n}")},
		{name: "PowerShell directory exact ACL setter in dead branch", mutate: replaceREADMEOnce(`  Set-ExactPrivateDirectoryACL $PrivateDir`, "  if ($false) {\n    Set-ExactPrivateDirectoryACL $PrivateDir\n  }")},
		{name: "PowerShell exact ACL assertion count weakened", mutate: replaceREADMEOnce(`if ($Rules.Count -ne 1) { throw 'private ACL must contain exactly one rule' }`, `if ($Rules.Count -lt 1) { throw 'private ACL has no access rule' }`)},
		{name: "PowerShell exact ACL rights assertion omitted", mutate: replaceREADMEOnce(`  if ($Rule.FileSystemRights -ne $ExpectedRights) { throw 'private ACL rights differ' }`, `  # exact rights assertion omitted`)},
		{name: "PowerShell exact ACL inheritance assertion omitted", mutate: replaceREADMEOnce(`  if ($Rule.InheritanceFlags -ne $ExpectedInheritance) { throw 'private ACL inheritance flags differ' }`, `  # exact inheritance assertion omitted`)},
		{name: "PowerShell exact ACL propagation assertion omitted", mutate: replaceREADMEOnce(`  if ($Rule.PropagationFlags -ne [Security.AccessControl.PropagationFlags]::None) { throw 'private ACL propagation differs' }`, `  # exact propagation assertion omitted`)},
		{name: "PowerShell TOML model assertion is inert", mutate: replaceREADMEOnce(`Assert-SafeTOMLValue $CodexModelTOML`, `$null = 'Assert-SafeTOMLValue $CodexModelTOML'`)},
		{name: "PowerShell TOML model assertion in dead branch", mutate: replaceREADMEOnce(`Assert-SafeTOMLValue $CodexModelTOML`, "if ($false) {\n  Assert-SafeTOMLValue $CodexModelTOML\n}")},
		{name: "PowerShell terminal load commented", mutate: replaceREADMENth(`$LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()`, `# $LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()`, 2)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated, err := test.mutate(readme)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateREADMEReleaseQuickStart(mutated); err == nil {
				t.Fatal("README Quick Start validator accepted the mutation")
			}
			semanticErr := validateREADMEReleaseQuickStartSemantics(mutated)
			if test.sealOnly && semanticErr != nil {
				t.Fatalf("source-seal-only mutation was rejected before source sealing: %v", semanticErr)
			}
			if !test.sealOnly && semanticErr == nil {
				t.Fatal("README Quick Start semantic validator accepted the mutation before source sealing")
			}
		})
	}
}

func TestGettingStartedQuickStartSemanticHelpersRejectBypasses(t *testing.T) {
	readme := readGettingStarted(t)
	t.Run("cross-section HTML comment", func(t *testing.T) {
		mutated, err := replaceREADMEOnce("### POSIX (macOS and Linux)\n", "<!--\n### POSIX (macOS and Linux)\n")(readme)
		if err != nil {
			t.Fatal(err)
		}
		mutated, err = replaceREADMEOnce("\n## SDK contract recovery\n", "\n-->\n## SDK contract recovery\n")(mutated)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseREADMEQuickStart(mutated); err == nil {
			t.Fatal("Quick Start parser accepted a cross-section HTML comment")
		}
	})

	t.Run("indented code in prose", func(t *testing.T) {
		mutated, err := replaceREADMEOnce(
			"Use PowerShell 7 as an unprivileged gateway identity.",
			"Use PowerShell 7 as an unprivileged gateway identity.\n\n    $LoadedGatewayKey | Out-Host",
		)(readme)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseREADMEQuickStart(mutated); err == nil {
			t.Fatal("Quick Start parser accepted indented executable code in prose")
		}
	})

	t.Run("tab-expanded indented code in prose", func(t *testing.T) {
		mutated, err := replaceREADMEOnce(
			"Use PowerShell 7 as an unprivileged gateway identity.",
			"Use PowerShell 7 as an unprivileged gateway identity.\n\n   \t$LoadedGatewayKey | Out-Host",
		)(readme)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseREADMEQuickStart(mutated); err == nil {
			t.Fatal("Quick Start parser accepted tab-expanded indented code in prose")
		}
	})

	t.Run("Windows reference link", func(t *testing.T) {
		if err := validateREADMEWindowsServiceLinks("See [Linux unit][svc]."); err == nil {
			t.Fatal("Windows prose validator accepted a reference-style link")
		}
	})

	t.Run("literal percent prose", func(t *testing.T) {
		if err := validateREADMEWindowsServiceLinks("The gateway remains 100% local."); err != nil {
			t.Fatalf("Windows prose validator rejected a literal percent: %v", err)
		}
	})

	t.Run("Markdown-obfuscated Windows service path", func(t *testing.T) {
		if err := validateREADMEWindowsServiceLinks(`See deploy/sys**temd**/ai-cli-gateway.**service**.`); err == nil {
			t.Fatal("Windows prose validator accepted Markdown-obfuscated service terms")
		}
	})

	t.Run("cross-section HTML processing instruction", func(t *testing.T) {
		mutated, err := replaceREADMEOnce("### POSIX (macOS and Linux)\n", "<?hidden\n### POSIX (macOS and Linux)\n")(readme)
		if err != nil {
			t.Fatal(err)
		}
		mutated, err = replaceREADMEOnce("\n## SDK contract recovery\n", "\n?>\n## SDK contract recovery\n")(mutated)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := parseREADMEQuickStart(mutated); err == nil {
			t.Fatal("Quick Start parser accepted a cross-section HTML processing instruction")
		}
	})

	t.Run("shell logical continuation", func(t *testing.T) {
		statements, err := shellTopLevelStatements("false && \\\n  node \"${SDK_WORK_ROOT}/javascript/main.mjs\"")
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(statements, `node "${SDK_WORK_ROOT}/javascript/main.mjs"`) {
			t.Fatal("shell reachability parser treated a continued conditional command as independently top-level")
		}
	})

	t.Run("shell top-level termination", func(t *testing.T) {
		statements, err := shellTopLevelStatements("exit 0\nnode \"${SDK_WORK_ROOT}/javascript/main.mjs\"")
		if err != nil {
			t.Fatal(err)
		}
		if slices.Contains(statements, `node "${SDK_WORK_ROOT}/javascript/main.mjs"`) {
			t.Fatal("shell reachability parser treated a command after top-level exit as reachable")
		}
	})
}

func TestGettingStartedQuickStartWholeDocumentBoundaries(t *testing.T) {
	readme := readGettingStarted(t)
	if err := validateREADMEReleaseQuickStartSemantics(readme); err != nil {
		t.Fatalf("baseline README semantic contract: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(string) (string, error)
	}{
		{
			name: "Quick Start inside HTML comment",
			mutate: func(document string) (string, error) {
				mutated, err := replaceREADMEOnce("\n## Quick Start\n", "\n<!--\n## Quick Start\n")(document)
				if err != nil {
					return "", err
				}
				return replaceREADMEOnce("\n## SDK contract recovery\n", "\n## SDK contract recovery\n-->\n")(mutated)
			},
		},
		{
			name: "Quick Start inside HTML details",
			mutate: func(document string) (string, error) {
				mutated, err := replaceREADMEOnce("\n## Quick Start\n", "\n<details><summary>Hidden</summary>\n## Quick Start\n")(document)
				if err != nil {
					return "", err
				}
				return replaceREADMEOnce("\n## SDK contract recovery\n", "\n## SDK contract recovery\n</details>\n")(mutated)
			},
		},
		{
			name: "Quick Start inside outer tilde fence",
			mutate: func(document string) (string, error) {
				mutated, err := replaceREADMEOnce("\n## Quick Start\n", "\n~~~~text\n## Quick Start\n")(document)
				if err != nil {
					return "", err
				}
				return replaceREADMEOnce("\n## SDK contract recovery\n", "\n## SDK contract recovery\n~~~~\n")(mutated)
			},
		},
		{
			name: "Quick Start inside longer tilde fence",
			mutate: func(document string) (string, error) {
				mutated, err := replaceREADMEOnce("\n## Quick Start\n", "\n~~~~~text\n## Quick Start\n")(document)
				if err != nil {
					return "", err
				}
				return replaceREADMEOnce("\n## SDK contract recovery\n", "\n## SDK contract recovery\n~~~~~\n")(mutated)
			},
		},
		{
			name: "Quick Start inside outer backtick fence",
			mutate: func(document string) (string, error) {
				mutated, err := replaceREADMEOnce("\n## Quick Start\n", "\n````text\n## Quick Start\n")(document)
				if err != nil {
					return "", err
				}
				return replaceREADMEOnce("\n## SDK contract recovery\n", "\n## SDK contract recovery\n````\n")(mutated)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated, err := test.mutate(readme)
			if err != nil {
				t.Fatal(err)
			}
			if err := validateREADMEReleaseQuickStartSemantics(mutated); err == nil {
				t.Fatal("README semantic validator accepted a non-rendered Quick Start boundary")
			}
		})
	}

	t.Run("later fenced literals do not affect boundaries", func(t *testing.T) {
		mutated := readme + "\n~~~~text\n<!-- literal fenced HTML -->\n## Quick Start\n## SDK contract recovery\n<?literal?>\n~~~~\n"
		if err := validateREADMEReleaseQuickStartSemantics(mutated); err != nil {
			t.Fatalf("semantic validator rejected inert later fenced literals: %v", err)
		}
	})

	t.Run("HTML outside Quick Start is rejected", func(t *testing.T) {
		mutated := readme + "\n<details>\n</details>\n"
		if err := validateREADMEReleaseQuickStartSemantics(mutated); err == nil {
			t.Fatal("semantic validator accepted raw HTML outside Quick Start")
		}
	})

	t.Run("CRLF README preserves boundaries and source seals", func(t *testing.T) {
		mutated := strings.ReplaceAll(readme, "\n", "\r\n")
		if err := validateREADMEReleaseQuickStart(mutated); err != nil {
			t.Fatalf("validator rejected CRLF README: %v", err)
		}
	})
}

func replaceREADMEOnce(old, replacement string) func(string) (string, error) {
	return func(document string) (string, error) {
		if count := strings.Count(document, old); count != 1 {
			return "", fmt.Errorf("README mutation target %q occurs %d times, want one", old, count)
		}
		return strings.Replace(document, old, replacement, 1), nil
	}
}

func replaceREADMENth(old, replacement string, occurrence int) func(string) (string, error) {
	return func(document string) (string, error) {
		position := 0
		for current := 1; current <= occurrence; current++ {
			relative := strings.Index(document[position:], old)
			if relative < 0 {
				return "", fmt.Errorf("README mutation target %q has fewer than %d occurrences", old, occurrence)
			}
			position += relative
			if current == occurrence {
				return document[:position] + replacement + document[position+len(old):], nil
			}
			position += len(old)
		}
		return "", errors.New("unreachable README mutation")
	}
}

func moveREADMESystemdLink(document string) (string, error) {
	const sentence = "Linux service operators can adapt the checked-in [systemd service example](../deploy/systemd/ai-cli-gateway.service) after completing the same path, ownership, Doctor, and credential checks."
	if strings.Count(document, sentence) != 1 {
		return "", errors.New("systemd mutation target is not unique")
	}
	document = strings.Replace(document, sentence, "Linux service operators must complete the same path, ownership, Doctor, and credential checks.", 1)
	const windows = "### Windows PowerShell\n"
	if strings.Count(document, windows) != 1 {
		return "", errors.New("Windows heading mutation target is not unique")
	}
	return strings.Replace(document, windows, windows+"\nUse the [systemd service example](../deploy/systemd/ai-cli-gateway.service).\n", 1), nil
}

func moveREADMEHostBranchToUnusedFunction(document string) (string, error) {
	const branch = `  Darwin:arm64) ASSET="ai-cli-gateway_${VERSION}_darwin_arm64.tar.gz" ;;`
	if strings.Count(document, branch) != 1 {
		return "", errors.New("Darwin ARM64 branch mutation target is not unique")
	}
	document = strings.Replace(document, branch+"\n", "", 1)
	const selector = `case "$(uname -s):$(uname -m)" in`
	if strings.Count(document, selector) != 1 {
		return "", errors.New("host selector mutation target is not unique")
	}
	unused := "unused_darwin_arm64() {\n  case \"$1\" in\n" + branch + "\n  esac\n}\n"
	return strings.Replace(document, selector, unused+selector, 1), nil
}

type readmeQuickStartFence struct {
	section  string
	language string
	source   string
	body     string
}

type readmeQuickStartDocument struct {
	prose  map[string]string
	fences []readmeQuickStartFence
}

const (
	quickStartRootSection    = "root"
	quickStartPOSIXSection   = "posix"
	quickStartWindowsSection = "windows"
	quickStartSDKSection     = "sdk"
)

func validateREADMEReleaseQuickStart(readme string) error {
	return validateREADMEReleaseQuickStartContract(readme, true)
}

func validateREADMEReleaseQuickStartSemantics(readme string) error {
	return validateREADMEReleaseQuickStartContract(readme, false)
}

func validateREADMEReleaseQuickStartContract(readme string, sealSources bool) error {
	document, err := parseREADMEQuickStart(readme)
	if err != nil {
		return err
	}
	if err := validateREADMEQuickStartFenceSequence(document); err != nil {
		return err
	}
	rootProse := document.prose[quickStartRootSection]
	posixProse := document.prose[quickStartPOSIXSection]
	windowsProse := document.prose[quickStartWindowsSection]
	sdkProse := document.prose[quickStartSDKSection]
	for _, marker := range []string{
		"v0.1.0", "ai-cli-gateway_0.1.0_linux_amd64.tar.gz",
		"ai-cli-gateway_0.1.0_linux_arm64.tar.gz", "ai-cli-gateway_0.1.0_darwin_amd64.tar.gz",
		"ai-cli-gateway_0.1.0_darwin_arm64.tar.gz", "ai-cli-gateway_0.1.0_windows_amd64.zip",
	} {
		if !strings.Contains(rootProse, marker) {
			return fmt.Errorf("active Quick Start prose is missing %q", marker)
		}
	}
	const terms = "You are responsible for installing and authenticating each provider CLI and for using it in accordance with its applicable terms."
	if !strings.Contains(sdkProse, terms) {
		return errors.New("active SDK prose is missing the provider-terms notice")
	}
	const systemdLink = "[systemd service example](../deploy/systemd/ai-cli-gateway.service)"
	if !strings.Contains(posixProse, systemdLink) {
		return errors.New("systemd link is not active POSIX-only prose")
	}
	if err := validateREADMEWindowsServiceLinks(windowsProse); err != nil {
		return err
	}

	posixFences := quickStartFences(document, quickStartPOSIXSection, "bash")
	windowsFences := quickStartFences(document, quickStartWindowsSection, "powershell")
	sdkFences := quickStartFences(document, quickStartSDKSection, "bash")
	if len(posixFences) != 6 || len(windowsFences) != 6 || len(sdkFences) != 1 {
		return fmt.Errorf("executable fence counts = POSIX %d, Windows %d, SDK %d", len(posixFences), len(windowsFences), len(sdkFences))
	}
	posixCode := strings.Join(posixFences, "\n")
	windowsCode := strings.Join(windowsFences, "\n")
	sdkCode := sdkFences[0]

	hostSelector, err := extractREADMEPOSIXHostSelector(posixFences[0])
	if err != nil {
		return fmt.Errorf("POSIX host selector: %w", err)
	}
	for _, branch := range []string{
		`Linux:x86_64) ASSET="ai-cli-gateway_${VERSION}_linux_amd64.tar.gz" ;;`,
		`Linux:aarch64|Linux:arm64) ASSET="ai-cli-gateway_${VERSION}_linux_arm64.tar.gz" ;;`,
		`Darwin:x86_64) ASSET="ai-cli-gateway_${VERSION}_darwin_amd64.tar.gz" ;;`,
		`Darwin:arm64) ASSET="ai-cli-gateway_${VERSION}_darwin_arm64.tar.gz" ;;`,
	} {
		if !containsExactTrimmedLine(hostSelector, branch) {
			return fmt.Errorf("POSIX selector is missing %q", branch)
		}
	}
	if strings.Contains(hostSelector, "function ") || regexp.MustCompile(`(?m)^[[:space:]]*[[:alnum:]_]+\(\)[[:space:]]*\{`).MatchString(hostSelector) {
		return errors.New("POSIX selector contains a function declaration instead of active case arms")
	}
	if err := requireOrderedMarkers(posixCode,
		`awk -v name="${ASSET}"`,
		`length($0) == 66 + length(name) && substr($0, 65) == " *" name`,
		`if (matches != 1) exit 1`,
		`test "${#EXPECTED_SHA}" -eq 64`,
		`case "${EXPECTED_SHA}" in *[!0-9a-f]*) exit 1 ;; esac`,
		`ACTUAL_SHA="$(shasum -a 256 "${ASSET}" | awk '{print $1}')"`,
		`test "${ACTUAL_SHA}" = "${EXPECTED_SHA}"`,
		`tar -xzf "${ASSET}"`,
	); err != nil {
		return fmt.Errorf("POSIX checksum contract: %w", err)
	}
	for _, forbidden := range []string{"$0 ~ name", "shasum -c", `test -n "${ACTUAL_SHA}"`} {
		if strings.Contains(posixCode, forbidden) {
			return fmt.Errorf("POSIX checksum uses forbidden broad check %q", forbidden)
		}
	}
	if err := requireOrderedMarkers(windowsCode,
		`$ArchivePattern = '^[0-9a-f]{64} \*' + [regex]::Escape($ArchiveName) + '$'`,
		`$ManifestMatches = @(Get-Content -LiteralPath $ManifestPath | Where-Object { $_ -cmatch $ArchivePattern })`,
		`if ($ManifestMatches.Count -ne 1)`,
		`if ($ExpectedSHA -cnotmatch '^[0-9a-f]{64}$')`,
		`$ActualSHA = (Get-FileHash -Algorithm SHA256 -LiteralPath $ArchivePath).Hash`,
		`if (-not [String]::Equals($ExpectedSHA, $ActualSHA, [StringComparison]::OrdinalIgnoreCase)) {`,
		`throw 'archive checksum mismatch'`,
		`Expand-Archive -LiteralPath $ArchivePath`,
	); err != nil {
		return fmt.Errorf("PowerShell checksum contract: %w", err)
	}

	for index, block := range posixFences[3:6] {
		if err := requireOrderedMarkers(block,
			`GATEWAY_KEY="$(LC_ALL=C tr -d '\n' < "${GATEWAY_CONFIG_DIR}/gateway.key")"`,
			`test "${#GATEWAY_KEY}" -eq 64`,
			`case "${GATEWAY_KEY}" in *[!0-9a-f]*) exit 1 ;; esac`,
			`export AI_CLI_GATEWAY_API_KEY="${GATEWAY_KEY}"`,
		); err != nil {
			return fmt.Errorf("POSIX key terminal %d: %w", index+1, err)
		}
	}
	for index, block := range windowsFences[3:6] {
		if err := requireOrderedMarkers(block,
			`$LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()`,
			`if ($LoadedGatewayKey -cnotmatch '^[0-9a-f]{64}$')`,
			`$env:AI_CLI_GATEWAY_API_KEY = $LoadedGatewayKey`,
		); err != nil {
			return fmt.Errorf("PowerShell key terminal %d: %w", index+1, err)
		}
	}
	if err := validateREADMEKeyUseContract(posixFences, windowsFences, sdkFences); err != nil {
		return err
	}

	for _, marker := range []string{
		`validate_toml_value() {`, `*\\*|*\"*|*[[:cntrl:]]*`,
		`validate_toml_value "${CODEX_EXECUTABLE}"`, `validate_toml_value "${CODEX_CONFIG_HOME}"`,
		`validate_toml_value "${GATEWAY_RUNTIME_DIR}"`, `validate_toml_value "${CODEX_MODEL}"`,
		`[q{/opt/ai-cli-gateway/bin/codex}, $ENV{CODEX_EXECUTABLE}]`,
		`[q{/var/lib/ai-cli-gateway/codex-home}, $ENV{CODEX_CONFIG_HOME}]`,
		`[q{/var/lib/ai-cli-gateway/runtime}, $ENV{GATEWAY_RUNTIME_DIR}]`,
		`[q{configured-provider-model}, $ENV{CODEX_MODEL}]`,
		`chmod 700 "${GATEWAY_CONFIG_DIR}" "${GATEWAY_RUNTIME_DIR}" "${CODEX_CONFIG_HOME}"`,
		`chmod 600 "${GATEWAY_CONFIG_FILE}"`,
	} {
		if !strings.Contains(posixFences[3], marker) {
			return fmt.Errorf("active POSIX configuration is missing %q", marker)
		}
	}
	for _, marker := range []string{
		`$CodexExecutableTOML = $CodexExecutable.Replace('\', '/')`,
		`$CodexConfigHomeTOML = $CodexConfigHome.Replace('\', '/')`,
		`$GatewayRuntimeTOML = $GatewayRuntimeDir.Replace('\', '/')`, `$CodexModelTOML = $CodexModel`,
		`function Assert-SafeTOMLValue`, `[char]::IsControl($Character)`,
		`Assert-SafeTOMLValue $CodexExecutableTOML`, `Assert-SafeTOMLValue $CodexConfigHomeTOML`,
		`Assert-SafeTOMLValue $GatewayRuntimeTOML`, `Assert-SafeTOMLValue $CodexModelTOML`,
		`Replace-ExactlyOnce $ConfigText '/opt/ai-cli-gateway/bin/codex' $CodexExecutableTOML`,
		`Replace-ExactlyOnce $ConfigText '/var/lib/ai-cli-gateway/codex-home' $CodexConfigHomeTOML`,
		`Replace-ExactlyOnce $ConfigText '/var/lib/ai-cli-gateway/runtime' $GatewayRuntimeTOML`,
		`Replace-ExactlyOnce $ConfigText 'configured-provider-model' $CodexModelTOML`,
		`if (Test-Path -LiteralPath $FreshTarget) { throw 'private target already exists' }`,
		`function Set-ExactPrivateACL`, `$ACL.SetAccessRuleProtection($true, $false)`,
		`$ACL.RemoveAccessRuleAll($ExistingRule)`, `$ACL.SetOwner($CurrentSIDObject)`,
		`Set-Acl -LiteralPath $Path -AclObject $ACL`,
		`AreAccessRulesProtected`, `$Rules.Count -ne 1`,
		`$Rule.IsInherited`, `$Rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value`,
		`$RuleSID -ne $CurrentSID`,
		`[Security.AccessControl.AccessControlType]::Allow`,
		`Set-ExactPrivateDirectoryACL $PrivateDir`, `Set-ExactPrivateFileACL $GatewayConfigFile`,
		`Set-ExactPrivateFileACL $GatewayKeyPath`,
	} {
		if !strings.Contains(windowsFences[3], marker) {
			return fmt.Errorf("active PowerShell configuration is missing %q", marker)
		}
	}
	powerShellSetupStatements, err := powerShellTopLevelStatements(windowsFences[3])
	if err != nil {
		return fmt.Errorf("PowerShell configuration reachability: %w", err)
	}
	if err := requireTopLevelStatementCounts(powerShellSetupStatements, map[string]int{
		`Assert-SafeTOMLValue $CodexExecutableTOML`:                           1,
		`Assert-SafeTOMLValue $CodexConfigHomeTOML`:                           1,
		`Assert-SafeTOMLValue $GatewayRuntimeTOML`:                            1,
		`Assert-SafeTOMLValue $CodexModelTOML`:                                1,
		`foreach ($PrivateDir in @($GatewayConfigDir, $GatewayRuntimeDir)) {`: 1,
		`Set-ExactPrivateFileACL $GatewayConfigFile`:                          1,
		`Set-ExactPrivateFileACL $GatewayKeyPath`:                             1,
	}); err != nil {
		return fmt.Errorf("PowerShell configuration reachability: %w", err)
	}
	if err := validateREADMEWindowsACLProgram(windowsFences[3]); err != nil {
		return fmt.Errorf("PowerShell exact ACL contract: %w", err)
	}

	if err := requireOrderedMarkers(posixFences[5],
		`curl --fail-with-body`, `http://127.0.0.1:8080/v1/models`,
		`curl --fail-with-body`, `http://127.0.0.1:8080/v1/responses`,
	); err != nil {
		return fmt.Errorf("active POSIX curl contract: %w", err)
	}
	if err := requireOrderedMarkers(windowsFences[5],
		`curl.exe --fail-with-body`, `http://127.0.0.1:8080/v1/models`,
		`curl.exe --fail-with-body`, `http://127.0.0.1:8080/v1/responses`,
	); err != nil {
		return fmt.Errorf("active PowerShell curl contract: %w", err)
	}
	posixRequestStatements, err := shellTopLevelStatements(posixFences[5])
	if err != nil {
		return fmt.Errorf("active POSIX request reachability: %w", err)
	}
	if err := requireTopLevelStatementCounts(posixRequestStatements, map[string]int{`curl --fail-with-body \`: 2}); err != nil {
		return fmt.Errorf("active POSIX request reachability: %w", err)
	}
	powerShellRequestStatements, err := powerShellTopLevelStatements(windowsFences[5])
	if err != nil {
		return fmt.Errorf("active PowerShell request reachability: %w", err)
	}
	if err := requireTopLevelStatementCounts(powerShellRequestStatements, map[string]int{`curl.exe --fail-with-body ` + "`": 2}); err != nil {
		return fmt.Errorf("active PowerShell request reachability: %w", err)
	}
	for _, marker := range []string{
		`python3.12 -m venv`, `examples/openai-sdk/python/requirements.lock`,
		`examples/openai-sdk/python/main.py`, `npm ci --ignore-scripts --prefix`,
		`examples/openai-sdk/javascript/package-lock.json`, `node "${SDK_WORK_ROOT}/javascript/main.mjs"`,
		`AI_CLI_GATEWAY_BASE_URL="http://127.0.0.1:8080/v1"`, `AI_CLI_GATEWAY_MODEL="codex-local"`,
	} {
		if !strings.Contains(sdkCode, marker) {
			return fmt.Errorf("active SDK commands are missing %q", marker)
		}
	}
	sdkStatements, err := shellTopLevelStatements(sdkCode)
	if err != nil {
		return fmt.Errorf("active SDK reachability: %w", err)
	}
	if err := requireTopLevelStatementCounts(sdkStatements, map[string]int{`node "${SDK_WORK_ROOT}/javascript/main.mjs"`: 1}); err != nil {
		return fmt.Errorf("active SDK reachability: %w", err)
	}
	for _, marker := range []string{
		"1..300", "300", "models.list()", "responses.create()", "non-streaming",
		"SDK_GATEWAY_OK", "zero or one trailing newline",
	} {
		if !strings.Contains(sdkProse, marker) {
			return fmt.Errorf("active SDK prose is missing %q", marker)
		}
	}
	if sealSources {
		if err := validateREADMEQuickStartFenceSources(document); err != nil {
			return err
		}
	}
	return nil
}

func parseREADMEQuickStart(readme string) (readmeQuickStartDocument, error) {
	normalized := strings.ReplaceAll(readme, "\r\n", "\n")
	quickStartSource, err := extractREADMEQuickStartSource(normalized)
	if err != nil {
		return readmeQuickStartDocument{}, err
	}
	if strings.Contains(quickStartSource, "<!--") || strings.Contains(quickStartSource, "-->") {
		return readmeQuickStartDocument{}, errors.New("Quick Start must not contain HTML comments")
	}
	section := quickStartRootSection
	sectionOrder := []string{quickStartPOSIXSection, quickStartWindowsSection, quickStartSDKSection}
	nextSection := 0
	proseLines := map[string][]string{
		quickStartRootSection: {}, quickStartPOSIXSection: {}, quickStartWindowsSection: {}, quickStartSDKSection: {},
	}
	fences := make([]readmeQuickStartFence, 0)
	var fenceMarker byte
	fenceLength := 0
	language := ""
	fenceLines := make([]string, 0)
	for _, line := range strings.Split(quickStartSource, "\n") {
		if fenceLength > 0 {
			if isGFMFenceClosing(line, fenceMarker, fenceLength) {
				source := strings.Join(fenceLines, "\n")
				fences = append(fences, readmeQuickStartFence{
					section: section, language: language, source: source, body: executableREADMEFence(source),
				})
				fenceMarker = 0
				fenceLength = 0
				language = ""
				fenceLines = fenceLines[:0]
				continue
			}
			fenceLines = append(fenceLines, line)
			continue
		}
		marker, length, info, opening, err := parseGFMFenceOpening(line)
		if err != nil {
			return readmeQuickStartDocument{}, err
		}
		if opening {
			fenceMarker = marker
			fenceLength = length
			language = info
			continue
		}
		if strings.HasPrefix(line, "### ") {
			var next string
			switch line {
			case "### POSIX (macOS and Linux)":
				next = quickStartPOSIXSection
			case "### Windows PowerShell":
				next = quickStartWindowsSection
			case "### Official SDK checks":
				next = quickStartSDKSection
			default:
				return readmeQuickStartDocument{}, fmt.Errorf("unexpected Quick Start subsection %q", line)
			}
			if nextSection >= len(sectionOrder) || sectionOrder[nextSection] != next {
				return readmeQuickStartDocument{}, errors.New("Quick Start subsections are missing, duplicated, or reordered")
			}
			nextSection++
			section = next
			continue
		}
		if regexp.MustCompile(`^[ \t]{0,3}\[[^]\r\n]+\]:`).MatchString(line) {
			return readmeQuickStartDocument{}, errors.New("Quick Start prose contains a non-rendered Markdown reference definition")
		}
		if err := validateREADMEQuickStartProseLine(line); err != nil {
			return readmeQuickStartDocument{}, err
		}
		proseLines[section] = append(proseLines[section], line)
	}
	if fenceLength > 0 {
		return readmeQuickStartDocument{}, errors.New("Quick Start has an unterminated code fence")
	}
	if nextSection != len(sectionOrder) {
		return readmeQuickStartDocument{}, errors.New("Quick Start is missing a required subsection")
	}
	prose := make(map[string]string, len(proseLines))
	for name, lines := range proseLines {
		rendered, err := renderedREADMEProse(strings.Join(lines, "\n"))
		if err != nil {
			return readmeQuickStartDocument{}, fmt.Errorf("Quick Start %s prose: %w", name, err)
		}
		prose[name] = rendered
	}
	return readmeQuickStartDocument{prose: prose, fences: fences}, nil
}

func extractREADMEQuickStartSource(readme string) (string, error) {
	lines := strings.Split(readme, "\n")
	quickStartLine := -1
	recoveryLine := -1
	var fenceMarker byte
	fenceLength := 0
	for index, line := range lines {
		if fenceLength > 0 {
			if isGFMFenceClosing(line, fenceMarker, fenceLength) {
				fenceMarker = 0
				fenceLength = 0
			}
			continue
		}
		marker, length, _, opening, err := parseGFMFenceOpening(line)
		if err != nil {
			return "", err
		}
		if opening {
			fenceMarker = marker
			fenceLength = length
			continue
		}
		if containsREADMERawHTMLConstruct(line) {
			return "", errors.New("README contains raw HTML outside a GFM code fence")
		}
		switch line {
		case "## Quick Start":
			if quickStartLine >= 0 {
				return "", errors.New("README contains more than one top-level Quick Start heading")
			}
			quickStartLine = index
		case "## SDK contract recovery":
			if recoveryLine >= 0 {
				return "", errors.New("getting-started guide contains more than one top-level SDK contract recovery heading")
			}
			recoveryLine = index
		}
	}
	if fenceLength > 0 {
		return "", errors.New("README contains an unterminated GFM code fence")
	}
	if quickStartLine < 0 || recoveryLine < 0 {
		return "", errors.New("Quick Start and SDK contract recovery headings must each occur exactly once at top level")
	}
	if quickStartLine >= recoveryLine {
		return "", errors.New("Quick Start must precede SDK contract recovery")
	}
	return strings.Join(lines[quickStartLine+1:recoveryLine], "\n"), nil
}

func containsREADMERawHTMLConstruct(line string) bool {
	if strings.Contains(line, "<!") || strings.Contains(line, "-->") ||
		strings.Contains(line, "<?") || strings.Contains(line, "?>") {
		return true
	}
	return regexp.MustCompile(`(?i)<[ \t]*/?[ \t]*[a-z][a-z0-9-]*(?:[ \t/>]|$)`).MatchString(line)
}

func validateREADMEQuickStartProseLine(line string) error {
	if strings.ContainsAny(line, "<>") {
		return errors.New("Quick Start prose contains a raw HTML construct")
	}
	if strings.TrimSpace(line) != "" && readmeIndentColumns(line) >= 4 {
		return errors.New("Quick Start prose contains indented code outside an explicit fence")
	}
	trimmed := strings.TrimLeft(line, " ")
	if strings.HasPrefix(trimmed, ">") || regexp.MustCompile(`^(?:[-+*]|[0-9]+[.)])[ \t]+`).MatchString(trimmed) {
		return errors.New("Quick Start prose contains a Markdown container outside an explicit fence")
	}
	return nil
}

func readmeIndentColumns(line string) int {
	columns := 0
	for index := 0; index < len(line); index++ {
		switch line[index] {
		case ' ':
			columns++
		case '\t':
			columns += 4 - columns%4
		default:
			return columns
		}
	}
	return columns
}

func parseGFMFenceOpening(line string) (byte, int, string, bool, error) {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent == len(line) || (line[indent] != '`' && line[indent] != '~') {
		return 0, 0, "", false, nil
	}
	marker := line[indent]
	end := indent
	for end < len(line) && line[end] == marker {
		end++
	}
	length := end - indent
	if length < 3 {
		return 0, 0, "", false, nil
	}
	info := strings.TrimSpace(line[end:])
	if marker == '`' && strings.Contains(info, "`") {
		return 0, 0, "", false, errors.New("Quick Start contains an invalid backtick fence info string")
	}
	return marker, length, info, true, nil
}

func isGFMFenceClosing(line string, marker byte, openingLength int) bool {
	indent := 0
	for indent < len(line) && line[indent] == ' ' {
		indent++
	}
	if indent > 3 || indent == len(line) || line[indent] != marker {
		return false
	}
	end := indent
	for end < len(line) && line[end] == marker {
		end++
	}
	if end-indent < openingLength {
		return false
	}
	return strings.Trim(line[end:], " \t") == ""
}

func renderedREADMEProse(document string) (string, error) {
	withoutComments := stripREADMEHTMLComments(document)
	if regexp.MustCompile(`(?i)<[[:space:]]*/?[[:space:]]*[a-z][^>]*>`).MatchString(withoutComments) {
		return "", errors.New("contains raw HTML outside a code fence")
	}
	return withoutComments, nil
}

func validateREADMEWindowsServiceLinks(prose string) error {
	if strings.ContainsAny(prose, "[]") {
		return errors.New("Windows Quick Start prose contains Markdown link syntax")
	}
	normalized := prose
	for iteration := 0; iteration < 8; iteration++ {
		next := decodeREADMEPercentEscapes(html.UnescapeString(normalized))
		if next == normalized {
			break
		}
		normalized = next
		if iteration == 7 {
			return errors.New("Windows Quick Start prose exceeds the decoding limit")
		}
	}
	normalized = strings.NewReplacer("*", "", "_", "", "~", "", "`", "").Replace(normalized)
	normalized = strings.ToLower(strings.ReplaceAll(normalized, `\`, "/"))
	if strings.Contains(normalized, "systemd") || strings.Contains(normalized, ".service") {
		return errors.New("Windows Quick Start prose contains a service or systemd reference")
	}
	return nil
}

func decodeREADMEPercentEscapes(value string) string {
	var decoded strings.Builder
	decoded.Grow(len(value))
	for index := 0; index < len(value); index++ {
		if value[index] == '%' && index+2 < len(value) {
			high, highOK := decodeREADMEHexDigit(value[index+1])
			low, lowOK := decodeREADMEHexDigit(value[index+2])
			if highOK && lowOK {
				decoded.WriteByte(high<<4 | low)
				index += 2
				continue
			}
		}
		decoded.WriteByte(value[index])
	}
	return decoded.String()
}

func decodeREADMEHexDigit(value byte) (byte, bool) {
	switch {
	case value >= '0' && value <= '9':
		return value - '0', true
	case value >= 'a' && value <= 'f':
		return value - 'a' + 10, true
	case value >= 'A' && value <= 'F':
		return value - 'A' + 10, true
	default:
		return 0, false
	}
}

func validateREADMEKeyUseContract(posixFences, windowsFences, sdkFences []string) error {
	if len(posixFences) != 6 || len(windowsFences) != 6 || len(sdkFences) != 1 {
		return errors.New("Quick Start key-use contract requires all POSIX, PowerShell, and SDK fences")
	}
	posixExpected := make([][]string, len(posixFences))
	posixExpected[3] = []string{
		`openssl rand -hex 32 > "${GATEWAY_CONFIG_DIR}/gateway.key"`,
		`chmod 600 "${GATEWAY_CONFIG_DIR}/gateway.key"`,
		`GATEWAY_KEY="$(LC_ALL=C tr -d '\n' < "${GATEWAY_CONFIG_DIR}/gateway.key")"`,
		`test "${#GATEWAY_KEY}" -eq 64`,
		`case "${GATEWAY_KEY}" in *[!0-9a-f]*) exit 1 ;; esac`,
		`export AI_CLI_GATEWAY_API_KEY="${GATEWAY_KEY}"`,
	}
	posixExpected[4] = []string{
		`GATEWAY_KEY="$(LC_ALL=C tr -d '\n' < "${GATEWAY_CONFIG_DIR}/gateway.key")"`,
		`test "${#GATEWAY_KEY}" -eq 64`,
		`case "${GATEWAY_KEY}" in *[!0-9a-f]*) exit 1 ;; esac`,
		`export AI_CLI_GATEWAY_API_KEY="${GATEWAY_KEY}"`,
	}
	posixExpected[5] = []string{
		`GATEWAY_KEY="$(LC_ALL=C tr -d '\n' < "${GATEWAY_CONFIG_DIR}/gateway.key")"`,
		`test "${#GATEWAY_KEY}" -eq 64`,
		`case "${GATEWAY_KEY}" in *[!0-9a-f]*) exit 1 ;; esac`,
		`export AI_CLI_GATEWAY_API_KEY="${GATEWAY_KEY}"`,
		`-H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \`,
		`-H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \`,
	}
	windowsExpected := make([][]string, len(windowsFences))
	windowsExpected[3] = []string{
		`$GatewayKeyPath = Join-Path $GatewayConfigDir 'gateway.key'`,
		`foreach ($FreshTarget in @($GatewayConfigDir, $GatewayRuntimeDir, $GatewayConfigFile, $GatewayKeyPath)) {`,
		`$GatewayKey = [Convert]::ToHexString($RandomBytes).ToLowerInvariant()`,
		`[IO.File]::WriteAllText($GatewayKeyPath, $GatewayKey, [Text.UTF8Encoding]::new($false))`,
		`Set-ExactPrivateFileACL $GatewayKeyPath`,
		`$LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()`,
		`if ($LoadedGatewayKey -cnotmatch '^[0-9a-f]{64}$') { throw 'invalid gateway key file' }`,
		`$env:AI_CLI_GATEWAY_API_KEY = $LoadedGatewayKey`,
	}
	windowsExpected[4] = []string{
		`$GatewayKeyPath = Join-Path $GatewayConfigDir 'gateway.key'`,
		`$LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()`,
		`if ($LoadedGatewayKey -cnotmatch '^[0-9a-f]{64}$') { throw 'invalid gateway key file' }`,
		`$env:AI_CLI_GATEWAY_API_KEY = $LoadedGatewayKey`,
	}
	windowsExpected[5] = []string{
		`$GatewayKeyPath = Join-Path $GatewayConfigDir 'gateway.key'`,
		`$LoadedGatewayKey = [IO.File]::ReadAllText($GatewayKeyPath).Trim()`,
		`if ($LoadedGatewayKey -cnotmatch '^[0-9a-f]{64}$') { throw 'invalid gateway key file' }`,
		`$env:AI_CLI_GATEWAY_API_KEY = $LoadedGatewayKey`,
		`-H "Authorization: Bearer $env:AI_CLI_GATEWAY_API_KEY" ` + "`",
		`-H "Authorization: Bearer $env:AI_CLI_GATEWAY_API_KEY" ` + "`",
	}
	for index, expected := range posixExpected {
		if actual := readmeKeyBearingLines(posixFences[index]); !slices.Equal(actual, expected) {
			return fmt.Errorf("POSIX key-use contract differs in fence %d", index+1)
		}
		topLevel, err := shellTopLevelStatements(posixFences[index])
		if err != nil {
			return fmt.Errorf("POSIX fence %d reachability: %w", index+1, err)
		}
		for _, statement := range expected {
			if strings.HasPrefix(statement, "-H ") {
				continue
			}
			if !slices.Contains(topLevel, statement) {
				return fmt.Errorf("POSIX key-use statement in fence %d is not top-level reachable", index+1)
			}
		}
	}
	for index, expected := range windowsExpected {
		actual := readmeKeyBearingLines(windowsFences[index])
		if !slices.Equal(actual, expected) {
			return fmt.Errorf("PowerShell key-use contract differs in fence %d", index+1)
		}
		topLevel, err := powerShellTopLevelStatements(windowsFences[index])
		if err != nil {
			return fmt.Errorf("PowerShell fence %d reachability: %w", index+1, err)
		}
		for _, statement := range expected {
			if !slices.Contains(topLevel, statement) {
				return fmt.Errorf("PowerShell key-use statement in fence %d is not top-level reachable", index+1)
			}
		}
	}
	if actual := readmeKeyBearingLines(sdkFences[0]); len(actual) != 0 {
		return errors.New("SDK key-use contract forbids key-bearing statements")
	}
	return nil
}

func readmeKeyBearingLines(block string) []string {
	result := make([]string, 0)
	for _, line := range strings.Split(block, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "gateway_key") || strings.Contains(lower, "gatewaykey") ||
			strings.Contains(lower, "gateway.key") || strings.Contains(lower, "ai_cli_gateway_api_key") {
			result = append(result, strings.TrimSpace(line))
		}
	}
	return result
}

func quickStartFences(document readmeQuickStartDocument, section, language string) []string {
	result := make([]string, 0)
	for _, fence := range document.fences {
		if fence.section == section && fence.language == language {
			result = append(result, fence.body)
		}
	}
	return result
}

func validateREADMEQuickStartFenceSequence(document readmeQuickStartDocument) error {
	expected := []struct {
		section  string
		language string
	}{
		{quickStartPOSIXSection, "bash"}, {quickStartPOSIXSection, "bash"},
		{quickStartPOSIXSection, "bash"}, {quickStartPOSIXSection, "bash"},
		{quickStartPOSIXSection, "bash"}, {quickStartPOSIXSection, "bash"},
		{quickStartWindowsSection, "powershell"}, {quickStartWindowsSection, "powershell"},
		{quickStartWindowsSection, "powershell"}, {quickStartWindowsSection, "powershell"},
		{quickStartWindowsSection, "powershell"}, {quickStartWindowsSection, "powershell"},
		{quickStartSDKSection, "bash"},
	}
	if len(document.fences) != len(expected) {
		return fmt.Errorf("Quick Start contains %d code fences, want exactly %d", len(document.fences), len(expected))
	}
	for index, want := range expected {
		got := document.fences[index]
		if got.section != want.section || got.language != want.language {
			return fmt.Errorf(
				"Quick Start fence %d = %s/%s, want %s/%s",
				index+1, got.section, got.language, want.section, want.language,
			)
		}
	}
	return nil
}

func validateREADMEQuickStartFenceSources(document readmeQuickStartDocument) error {
	expected := []string{
		"8b27de336607dffb1ab2e2f64d871d871e4f3beef4d5fc74d22476c911cc8414", // POSIX download and checksum.
		"3f492cee04c9825a4b8542d983b4ebe7f84e251af786bab5f2e0dfb236262c5d", // POSIX attestation.
		"d54bf15936a9f8afd0dfbe3688e928719b92adf10411d31947d93a4c05652389", // POSIX install.
		"70a3ca8fb27e89baa998ee97fc4ea5dd79f404bfe13e9d9657c754efdee9f0ac", // POSIX configuration.
		"4db8c2817d0f3ea8ac4994d9bcff5b715954ff389a2b21288f4fb8bc21b50476", // POSIX serve.
		"635ec9abb0c8d2a3a05f630403104391341f8669a0ca97b854b7aaf3c561c1fd", // POSIX requests.
		"055e3b1a0d12505f1f854879ebf0c0c1ffe52b10d94621aba03f63f81299aed7", // Windows download and checksum.
		"67f363e541443c9275a1aacc834d1ce93c7ac7d638db764cd323b81d1fce7c3b", // Windows attestation.
		"57b0a84d1b0fbd75264c24c4cbbc8f3807f28a3f3d0e0e5088ea001c69c9bc46", // Windows install.
		"e945b7509ade4560e6912eb15f6d8d9990708524824c360e06cd7880f8998537", // Windows configuration.
		"5b26c7ae1423bf83919abc35593accd9137111778cb21f641c473441e72cadc0", // Windows serve.
		"ab30f260752be7690d82a380260d41ece4a6680c13d59755ea3d980447b679a0", // Windows requests.
		"182d7dd2aba1e42acce798618dac0dc23f13051ad1c96a7190162ca18558ca6f", // SDK checks.
	}
	if len(document.fences) != len(expected) {
		return errors.New("Quick Start fence source contract requires exactly thirteen fences")
	}
	for index, fence := range document.fences {
		digest := sha256.Sum256([]byte(fence.source))
		actual := hex.EncodeToString(digest[:])
		if actual != expected[index] {
			return fmt.Errorf(
				"Quick Start fence %d (%s/%s) source digest = %s, want %s",
				index+1, fence.section, fence.language, actual, expected[index],
			)
		}
	}
	return nil
}

func extractREADMEPOSIXHostSelector(block string) (string, error) {
	const startMarker = `case "$(uname -s):$(uname -m)" in`
	start := strings.Index(block, startMarker)
	if start < 0 || strings.Count(block, startMarker) != 1 {
		return "", errors.New("active host case selector must occur exactly once")
	}
	lines := strings.Split(block[start:], "\n")
	depth := 0
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "case ") && strings.HasSuffix(trimmed, " in") {
			depth++
		}
		if trimmed == "esac" {
			depth--
			if depth == 0 {
				return strings.Join(lines[:index+1], "\n"), nil
			}
		}
	}
	return "", errors.New("active host case selector is not structurally closed")
}

func containsExactTrimmedLine(document, expected string) bool {
	for _, line := range strings.Split(document, "\n") {
		if strings.TrimSpace(line) == expected {
			return true
		}
	}
	return false
}

func powerShellTopLevelStatements(block string) ([]string, error) {
	depth := 0
	statements := make([]string, 0)
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if depth == 0 {
			statements = append(statements, trimmed)
		}
		delta, err := powerShellBraceDelta(line)
		if err != nil {
			return nil, err
		}
		depth += delta
		if depth < 0 {
			return nil, errors.New("PowerShell block closes more scopes than it opens")
		}
	}
	if depth != 0 {
		return nil, errors.New("PowerShell block has an unclosed scope")
	}
	return statements, nil
}

func powerShellBraceDelta(line string) (int, error) {
	delta := 0
	inSingleQuote := false
	inDoubleQuote := false
	for index := 0; index < len(line); index++ {
		character := line[index]
		if character == '`' && !inSingleQuote {
			index++
			continue
		}
		if inSingleQuote {
			if character == '\'' {
				if index+1 < len(line) && line[index+1] == '\'' {
					index++
					continue
				}
				inSingleQuote = false
			}
			continue
		}
		if inDoubleQuote {
			if character == '"' {
				inDoubleQuote = false
			}
			continue
		}
		switch character {
		case '#':
			return delta, nil
		case '\'':
			inSingleQuote = true
		case '"':
			inDoubleQuote = true
		case '{':
			delta++
		case '}':
			delta--
		}
	}
	if inSingleQuote || inDoubleQuote {
		return 0, errors.New("PowerShell line has an unclosed quoted string")
	}
	return delta, nil
}

func shellTopLevelStatements(block string) ([]string, error) {
	depth := 0
	statements := make([]string, 0)
	functionStart := regexp.MustCompile(`^(?:function[[:space:]]+)?[[:alnum:]_]+\(\)[[:space:]]*\{$`)
	inSingleQuote := false
	inDoubleQuote := false
	pending := ""
	terminated := false
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		startedInQuote := inSingleQuote || inDoubleQuote
		if !startedInQuote && (trimmed == "fi" || trimmed == "done" || trimmed == "esac" || trimmed == "}") {
			depth--
			if depth < 0 {
				return nil, errors.New("shell block closes more scopes than it opens")
			}
		}
		if !startedInQuote && depth == 0 && pending == "" && !terminated {
			statements = append(statements, trimmed)
		}
		var continuation bool
		var err error
		inSingleQuote, inDoubleQuote, continuation, err = scanShellLine(line, inSingleQuote, inDoubleQuote)
		if err != nil {
			return nil, err
		}
		if startedInQuote || inSingleQuote || inDoubleQuote {
			continue
		}
		piece := strings.TrimSpace(strings.TrimSuffix(strings.TrimRight(line, " \t"), `\`))
		control := piece
		if pending != "" {
			control = pending + " " + piece
		}
		if continuation {
			pending = control
			continue
		}
		pending = ""
		if trimmed == "fi" || trimmed == "done" || trimmed == "esac" || trimmed == "}" {
			continue
		}
		wasTopLevel := depth == 0
		switch {
		case strings.HasPrefix(control, "if ") && (strings.HasSuffix(control, "; then") || strings.HasSuffix(control, " then")) && !strings.Contains(control, "; fi"):
			depth++
		case (strings.HasPrefix(control, "for ") || strings.HasPrefix(control, "while ") || strings.HasPrefix(control, "until ")) && strings.HasSuffix(control, "; do"):
			depth++
		case strings.HasPrefix(control, "case ") && strings.HasSuffix(control, " in") && !strings.Contains(control, " esac"):
			depth++
		case functionStart.MatchString(control):
			depth++
		}
		if wasTopLevel && isUnconditionalShellTerminator(control) {
			terminated = true
		}
	}
	if inSingleQuote || inDoubleQuote {
		return nil, errors.New("shell block has an unclosed quoted string")
	}
	if pending != "" {
		return nil, errors.New("shell block has an unclosed line continuation")
	}
	if depth != 0 {
		return nil, errors.New("shell block has an unclosed scope")
	}
	return statements, nil
}

func isUnconditionalShellTerminator(statement string) bool {
	fields := strings.Fields(statement)
	if len(fields) == 0 {
		return false
	}
	return fields[0] == "exit" || fields[0] == "return" || fields[0] == "exec"
}

func scanShellLine(line string, inSingleQuote, inDoubleQuote bool) (bool, bool, bool, error) {
	trimmedRight := strings.TrimRight(line, " \t")
	continuation := false
	for index := 0; index < len(trimmedRight); index++ {
		character := trimmedRight[index]
		if inSingleQuote {
			if character == '\'' {
				inSingleQuote = false
			}
			continue
		}
		if character == '\\' {
			if index == len(trimmedRight)-1 {
				continuation = true
				continue
			}
			index++
			continue
		}
		if inDoubleQuote {
			if character == '"' {
				inDoubleQuote = false
			}
			continue
		}
		switch character {
		case '#':
			return inSingleQuote, inDoubleQuote, false, nil
		case '\'':
			inSingleQuote = true
		case '"':
			inDoubleQuote = true
		}
	}
	return inSingleQuote, inDoubleQuote, continuation, nil
}

func requireTopLevelStatementCounts(statements []string, expected map[string]int) error {
	for statement, want := range expected {
		if got := stringsCount(statements, statement); got != want {
			return fmt.Errorf("top-level statement %q occurs %d times, want %d", statement, got, want)
		}
	}
	return nil
}

func stringsCount(values []string, expected string) int {
	count := 0
	for _, value := range values {
		if value == expected {
			count++
		}
	}
	return count
}

func validateREADMEWindowsACLProgram(block string) error {
	if strings.Contains(block, "icacls.exe") {
		return errors.New("exact ACL program must not preserve ambient explicit rules through icacls")
	}
	assertFunction, err := extractREADMEPowerShellFunction(block, "Assert-ExactPrivateACL")
	if err != nil {
		return err
	}
	if err := requireOrderedMarkers(assertFunction,
		`$ACL = Get-Acl -LiteralPath $Path`,
		`if (-not $ACL.AreAccessRulesProtected)`,
		`$OwnerSID = $ACL.GetOwner([Security.Principal.SecurityIdentifier]).Value`,
		`if ($OwnerSID -ne $CurrentSID)`,
		`if ($Rules.Count -ne 1)`,
		`$RuleSID = $Rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value`,
		`if ($RuleSID -ne $CurrentSID)`,
		`[Security.AccessControl.AccessControlType]::Allow`,
		`if ($Rule.FileSystemRights -ne $ExpectedRights)`,
		`if ($Rule.InheritanceFlags -ne $ExpectedInheritance)`,
		`if ($Rule.PropagationFlags -ne [Security.AccessControl.PropagationFlags]::None)`,
	); err != nil {
		return fmt.Errorf("exact ACL assertion: %w", err)
	}
	setFunction, err := extractREADMEPowerShellFunction(block, "Set-ExactPrivateACL")
	if err != nil {
		return err
	}
	if err := requireOrderedMarkers(setFunction,
		`$ACL = Get-Acl -LiteralPath $Path`,
		`$ACL.SetAccessRuleProtection($true, $false)`,
		`foreach ($ExistingRule in @($ACL.Access))`,
		`$ACL.RemoveAccessRuleAll($ExistingRule)`,
		`$CurrentSIDObject = [Security.Principal.SecurityIdentifier]::new($CurrentSID)`,
		`$ACL.SetOwner($CurrentSIDObject)`,
		`$Rule = [Security.AccessControl.FileSystemAccessRule]::new(`,
		`$ACL.SetAccessRule($Rule)`,
		`Set-Acl -LiteralPath $Path -AclObject $ACL`,
		`Assert-ExactPrivateACL $Path $ExpectedRights $ExpectedInheritance`,
	); err != nil {
		return fmt.Errorf("exact ACL setter: %w", err)
	}
	if strings.Count(setFunction, "$ACL.RemoveAccessRuleAll(") != 1 || strings.Count(setFunction, "$ACL.SetAccessRule(") != 1 || strings.Count(setFunction, "FileSystemAccessRule]::new(") != 1 {
		return errors.New("exact ACL setter must purge by identity and add exactly one rule")
	}
	directoryFunction, err := extractREADMEPowerShellFunction(block, "Set-ExactPrivateDirectoryACL")
	if err != nil {
		return err
	}
	if err := requireOrderedMarkers(directoryFunction,
		`[Security.AccessControl.InheritanceFlags]::ContainerInherit`,
		`[Security.AccessControl.InheritanceFlags]::ObjectInherit`,
		`Set-ExactPrivateACL $Path ([Security.AccessControl.FileSystemRights]::FullControl) $Inheritance`,
	); err != nil {
		return fmt.Errorf("exact directory ACL setter: %w", err)
	}
	fileFunction, err := extractREADMEPowerShellFunction(block, "Set-ExactPrivateFileACL")
	if err != nil {
		return err
	}
	if err := requireOrderedMarkers(fileFunction,
		`[Security.AccessControl.FileSystemRights]::Read`,
		`[Security.AccessControl.FileSystemRights]::Write`,
		`[Security.AccessControl.FileSystemRights]::Synchronize`,
		`Set-ExactPrivateACL $Path $Rights ([Security.AccessControl.InheritanceFlags]::None)`,
	); err != nil {
		return fmt.Errorf("exact file ACL setter: %w", err)
	}
	const directoryLoop = "foreach ($PrivateDir in @($GatewayConfigDir, $GatewayRuntimeDir)) {\n  Set-ExactPrivateDirectoryACL $PrivateDir\n}"
	if strings.Count(block, directoryLoop) != 1 {
		return errors.New("exact directory ACL setter loop must be a direct top-level body")
	}
	for target, setter := range map[string]string{
		"PrivateDir": "Set-ExactPrivateDirectoryACL", "GatewayConfigFile": "Set-ExactPrivateFileACL", "GatewayKeyPath": "Set-ExactPrivateFileACL",
	} {
		marker := setter + " $" + target
		if strings.Count(block, marker) != 1 {
			return fmt.Errorf("exact ACL setter for %s occurs %d times, want one", target, strings.Count(block, marker))
		}
	}
	return nil
}

func executableREADMEFence(body string) string {
	withoutHTML := stripREADMEHTMLComments(body)
	lines := make([]string, 0)
	for _, line := range strings.Split(withoutHTML, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func stripREADMEHTMLComments(document string) string {
	var result strings.Builder
	for len(document) > 0 {
		start := strings.Index(document, "<!--")
		if start < 0 {
			result.WriteString(document)
			break
		}
		result.WriteString(document[:start])
		document = document[start+len("<!--"):]
		end := strings.Index(document, "-->")
		if end < 0 {
			break
		}
		document = document[end+len("-->"):]
	}
	return result.String()
}

func requireOrderedMarkers(document string, markers ...string) error {
	position := 0
	for _, marker := range markers {
		relative := strings.Index(document[position:], marker)
		if relative < 0 {
			return fmt.Errorf("missing ordered executable marker %q", marker)
		}
		position += relative + len(marker)
	}
	return nil
}

func TestGettingStartedPOSIXChecksumCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the documented POSIX checksum commands require Bash and shasum")
	}
	if _, err := exec.LookPath("bash"); err != nil {
		t.Fatal("Bash is required to verify the documented POSIX checksum commands")
	}
	if _, err := exec.LookPath("shasum"); err != nil {
		t.Fatal("shasum is required to verify the documented POSIX checksum commands")
	}
	document, err := parseREADMEQuickStart(readGettingStarted(t))
	if err != nil {
		t.Fatal(err)
	}
	fences := quickStartFences(document, quickStartPOSIXSection, "bash")
	if len(fences) != 6 {
		t.Fatalf("POSIX Bash fence count = %d, want six", len(fences))
	}
	const startMarker = `MANIFEST_LINE="$(`
	const endMarker = `test "${ACTUAL_SHA}" = "${EXPECTED_SHA}"`
	start := strings.Index(fences[0], startMarker)
	end := strings.Index(fences[0], endMarker)
	if start < 0 || end < start {
		t.Fatal("cannot extract the documented POSIX checksum program")
	}
	program := "set -eu\nASSET=ai-cli-gateway_0.1.0_darwin_arm64.tar.gz\n" + fences[0][start:end+len(endMarker)] + "\n"
	asset := []byte("verified release fixture\n")
	digest := sha256.Sum256(asset)
	validRecord := fmt.Sprintf("%x *ai-cli-gateway_0.1.0_darwin_arm64.tar.gz\n", digest)
	decoys := ""
	for index := 0; index < 5; index++ {
		decoys += fmt.Sprintf("%064x *decoy-%d\n", index+1, index)
	}
	tests := []struct {
		name     string
		manifest string
		wantOK   bool
	}{
		{name: "valid selected record", manifest: decoys + validRecord, wantOK: true},
		{name: "duplicate selected record", manifest: decoys + validRecord + validRecord},
		{name: "mismatched selected digest", manifest: decoys + strings.Repeat("0", 64) + " *ai-cli-gateway_0.1.0_darwin_arm64.tar.gz\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "ai-cli-gateway_0.1.0_darwin_arm64.tar.gz"), asset, 0o600); err != nil {
				t.Fatalf("write asset fixture: %v", err)
			}
			if err := os.WriteFile(filepath.Join(root, "SHA256SUMS"), []byte(test.manifest), 0o600); err != nil {
				t.Fatalf("write manifest fixture: %v", err)
			}
			command := exec.CommandContext(context.Background(), "bash", "-c", program) //nolint:gosec // Fixed README program and test-owned fixtures.
			command.Dir = root
			command.Env = []string{"PATH=" + os.Getenv("PATH")}
			output, err := command.CombinedOutput()
			if test.wantOK && err != nil {
				t.Fatalf("documented checksum program failed: %v output=%q", err, output)
			}
			if !test.wantOK && err == nil {
				t.Fatal("documented checksum program accepted an invalid manifest")
			}
		})
	}
}

func TestGettingStartedPOSIXHostSelectorCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the documented POSIX host selector requires a POSIX shell")
	}
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Fatal("Bash is required to execute the documented POSIX host selector")
	}
	document, err := parseREADMEQuickStart(readGettingStarted(t))
	if err != nil {
		t.Fatal(err)
	}
	fences := quickStartFences(document, quickStartPOSIXSection, "bash")
	if len(fences) != 6 {
		t.Fatalf("POSIX Bash fence count = %d, want six", len(fences))
	}
	selector, err := extractREADMEPOSIXHostSelector(fences[0])
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	uname := filepath.Join(root, "uname")
	const fakeUname = `#!/bin/sh
case "$1" in
  -s) printf '%s\n' "${README_TEST_UNAME_S:?}" ;;
  -m) printf '%s\n' "${README_TEST_UNAME_M:?}" ;;
  *) exit 64 ;;
esac
`
	if err := os.WriteFile(uname, []byte(fakeUname), 0o600); err != nil {
		t.Fatalf("write fake uname: %v", err)
	}
	if err := os.Chmod(uname, 0o700); err != nil { //nolint:gosec // Test-owned fake uname must be executable and private.
		t.Fatalf("make fake uname executable: %v", err)
	}
	program := "set -eu\nVERSION=0.1.0\n" + selector + "\nprintf '%s' \"${ASSET}\"\n"
	tests := []struct {
		name    string
		system  string
		machine string
		want    string
		wantOK  bool
	}{
		{name: "Linux x86-64", system: "Linux", machine: "x86_64", want: "ai-cli-gateway_0.1.0_linux_amd64.tar.gz", wantOK: true},
		{name: "Linux aarch64", system: "Linux", machine: "aarch64", want: "ai-cli-gateway_0.1.0_linux_arm64.tar.gz", wantOK: true},
		{name: "Linux arm64", system: "Linux", machine: "arm64", want: "ai-cli-gateway_0.1.0_linux_arm64.tar.gz", wantOK: true},
		{name: "Darwin Intel", system: "Darwin", machine: "x86_64", want: "ai-cli-gateway_0.1.0_darwin_amd64.tar.gz", wantOK: true},
		{name: "Darwin Apple silicon", system: "Darwin", machine: "arm64", want: "ai-cli-gateway_0.1.0_darwin_arm64.tar.gz", wantOK: true},
		{name: "unsupported", system: "FreeBSD", machine: "amd64"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, bash, "-c", program) //nolint:gosec // Fixed README program and test-owned fake uname.
			command.Env = []string{
				"PATH=" + root + string(os.PathListSeparator) + os.Getenv("PATH"),
				"README_TEST_UNAME_S=" + test.system,
				"README_TEST_UNAME_M=" + test.machine,
			}
			output, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal("documented host selector exceeded its local deadline")
			}
			if test.wantOK {
				if err != nil {
					t.Fatalf("documented host selector failed: %v output=%q", err, output)
				}
				if string(output) != test.want {
					t.Fatalf("selected asset = %q, want %q", output, test.want)
				}
			} else if err == nil {
				t.Fatalf("documented host selector accepted unsupported tuple with output %q", output)
			}
		})
	}
}

func TestGettingStartedQuickStartTOMLSubstitutionValues(t *testing.T) {
	document, err := parseREADMEQuickStart(readGettingStarted(t))
	if err != nil {
		t.Fatal(err)
	}
	posix := quickStartFences(document, quickStartPOSIXSection, "bash")
	windows := quickStartFences(document, quickStartWindowsSection, "powershell")
	if len(posix) != 6 || len(windows) != 6 {
		t.Fatal("Quick Start configuration fences are incomplete")
	}
	for _, marker := range []string{`*\\*|*\"*|*[[:cntrl:]]*`, `validate_toml_value "${CODEX_MODEL}"`} {
		if !strings.Contains(posix[3], marker) {
			t.Fatalf("POSIX TOML rejection contract is missing %q", marker)
		}
	}
	for _, marker := range []string{`[char]::IsControl($Character)`, `Assert-SafeTOMLValue $CodexModelTOML`} {
		if !strings.Contains(windows[3], marker) {
			t.Fatalf("PowerShell TOML rejection contract is missing %q", marker)
		}
	}

	for _, value := range []string{"back\\slash", `double"quote`, "carriage\rreturn", "line\nfeed", "horizontal\ttab", "delete\x7fcontrol"} {
		if readmeTOMLBasicStringValueSafe(value) {
			t.Fatalf("unsafe TOML substitution value %q was accepted", value)
		}
	}
	template := string(readRepositoryFile(t, "examples/config/codex.example.toml"))
	posixConfig := replaceREADMEConfigMarkers(t, template, map[string]string{
		"/opt/ai-cli-gateway/bin/codex":      "/opt/Codex Tools/bin/codex",
		"/var/lib/ai-cli-gateway/codex-home": "/var/lib/AI CLI Gateway/codex-home",
		"/var/lib/ai-cli-gateway/runtime":    "/var/lib/AI CLI Gateway/runtime",
		"configured-provider-model":          "accessible-model_1",
	})
	windowsConfig := replaceREADMEConfigMarkers(t, template, map[string]string{
		"/opt/ai-cli-gateway/bin/codex":      "C:/Tools/Codex/codex.exe",
		"/var/lib/ai-cli-gateway/codex-home": "C:/Gateway Service/codex-home",
		"/var/lib/ai-cli-gateway/runtime":    "C:/Gateway Service/runtime",
		"configured-provider-model":          "accessible-model_1",
	})
	if runtime.GOOS == "windows" {
		if _, err := config.Decode(strings.NewReader(windowsConfig)); err != nil {
			t.Fatalf("safe Windows substitutions do not pass the real config decoder: %v", err)
		}
		var parsed map[string]any
		if err := toml.Unmarshal([]byte(posixConfig), &parsed); err != nil {
			t.Fatalf("safe POSIX substitutions are not valid TOML: %v", err)
		}
	} else {
		if _, err := config.Decode(strings.NewReader(posixConfig)); err != nil {
			t.Fatalf("safe POSIX substitutions do not pass the real config decoder: %v", err)
		}
		var parsed map[string]any
		if err := toml.Unmarshal([]byte(windowsConfig), &parsed); err != nil {
			t.Fatalf("safe normalized Windows substitutions are not valid TOML: %v", err)
		}
	}
}

func readmeTOMLBasicStringValueSafe(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character == '\\' || character == '"' || unicode.IsControl(character) {
			return false
		}
	}
	return true
}

func replaceREADMEConfigMarkers(t *testing.T, template string, replacements map[string]string) string {
	t.Helper()
	for marker, replacement := range replacements {
		if !readmeTOMLBasicStringValueSafe(replacement) {
			t.Fatalf("test replacement %q is not safe", replacement)
		}
		if count := strings.Count(template, marker); count != 1 {
			t.Fatalf("config marker %q occurs %d times, want one", marker, count)
		}
		template = strings.Replace(template, marker, replacement, 1)
	}
	return template
}

func TestGettingStartedWindowsPowerShellFencesNative(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("PowerShell fence parsing is exercised by Windows CI")
	}
	document, err := parseREADMEQuickStart(readGettingStarted(t))
	if err != nil {
		t.Fatal(err)
	}
	windows := quickStartFences(document, quickStartWindowsSection, "powershell")
	if len(windows) != 6 {
		t.Fatalf("PowerShell fence count = %d, want six", len(windows))
	}
	for index, source := range windows {
		t.Run(fmt.Sprintf("fence_%d", index+1), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "readme-fence.ps1")
			if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
				t.Fatalf("write PowerShell source: %v", err)
			}
			const parseOnly = `$ErrorActionPreference = 'Stop'
$Source = [IO.File]::ReadAllText($env:README_PS_SOURCE)
$null = [scriptblock]::Create($Source)
`
			output, err := runREADMEPowerShell(t, parseOnly, "README_PS_SOURCE="+path)
			if err != nil {
				t.Fatalf("PowerShell fence does not parse: %v output=%q", err, output)
			}
		})
	}
}

func TestGettingStartedWindowsChecksumCommandsNative(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the documented PowerShell checksum commands are exercised by Windows CI")
	}
	document, err := parseREADMEQuickStart(readGettingStarted(t))
	if err != nil {
		t.Fatal(err)
	}
	windows := quickStartFences(document, quickStartWindowsSection, "powershell")
	if len(windows) != 6 {
		t.Fatalf("PowerShell fence count = %d, want six", len(windows))
	}
	program, err := extractREADMEWindowsChecksumProgram(windows[0])
	if err != nil {
		t.Fatal(err)
	}
	const archiveName = "ai-cli-gateway_0.1.0_windows_amd64.zip"
	archive := []byte("verified Windows release fixture\n")
	digest := sha256.Sum256(archive)
	validRecord := fmt.Sprintf("%x *%s\n", digest, archiveName)
	tests := []struct {
		name     string
		manifest string
		wantOK   bool
	}{
		{name: "valid selected record", manifest: strings.Repeat("0", 64) + " *decoy.zip\n" + validRecord, wantOK: true},
		{name: "duplicate selected record", manifest: validRecord + validRecord},
		{name: "malformed selected record", manifest: "not-a-sha256 *" + archiveName + "\n"},
		{name: "mismatched selected digest", manifest: strings.Repeat("0", 64) + " *" + archiveName + "\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			archivePath := filepath.Join(root, archiveName)
			manifestPath := filepath.Join(root, "SHA256SUMS")
			if err := os.WriteFile(archivePath, archive, 0o600); err != nil {
				t.Fatalf("write archive fixture: %v", err)
			}
			if err := os.WriteFile(manifestPath, []byte(test.manifest), 0o600); err != nil {
				t.Fatalf("write checksum fixture: %v", err)
			}
			script := "$ErrorActionPreference = 'Stop'\n" +
				"$ArchiveName = $env:README_ARCHIVE_NAME\n" +
				"$ArchivePath = $env:README_ARCHIVE_PATH\n" +
				"$ManifestPath = $env:README_MANIFEST_PATH\n" + program + "\n"
			output, err := runREADMEPowerShell(t, script,
				"README_ARCHIVE_NAME="+archiveName,
				"README_ARCHIVE_PATH="+archivePath,
				"README_MANIFEST_PATH="+manifestPath,
			)
			if test.wantOK && err != nil {
				t.Fatalf("documented PowerShell checksum program failed: %v output=%q", err, output)
			}
			if !test.wantOK && err == nil {
				t.Fatalf("documented PowerShell checksum program accepted invalid fixture with output %q", output)
			}
		})
	}
}

func TestGettingStartedWindowsTOMLValidationFunctionNative(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the documented PowerShell TOML function is exercised by Windows CI")
	}
	document, err := parseREADMEQuickStart(readGettingStarted(t))
	if err != nil {
		t.Fatal(err)
	}
	windows := quickStartFences(document, quickStartWindowsSection, "powershell")
	function, err := extractREADMEPowerShellFunction(windows[3], "Assert-SafeTOMLValue")
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		value  string
		wantOK bool
	}{
		{name: "normalized path", value: "C:/Tools/Codex/codex.exe", wantOK: true},
		{name: "model", value: "accessible-model_1", wantOK: true},
		{name: "empty"},
		{name: "backslash", value: `C:\Tools\Codex\codex.exe`},
		{name: "double quote", value: `bad"model`},
		{name: "control", value: "bad\tmodel"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			script := "$ErrorActionPreference = 'Stop'\n" + function + "\nAssert-SafeTOMLValue $env:README_TOML_VALUE\n"
			output, err := runREADMEPowerShell(t, script, "README_TOML_VALUE="+test.value)
			if test.wantOK && err != nil {
				t.Fatalf("documented TOML validation rejected safe value: %v output=%q", err, output)
			}
			if !test.wantOK && err == nil {
				t.Fatalf("documented TOML validation accepted unsafe value %q", test.value)
			}
		})
	}
}

func TestGettingStartedWindowsACLCommandsNative(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("the documented PowerShell ACL commands are exercised by Windows CI")
	}
	document, err := parseREADMEQuickStart(readGettingStarted(t))
	if err != nil {
		t.Fatal(err)
	}
	windows := quickStartFences(document, quickStartWindowsSection, "powershell")
	if err := validateREADMEWindowsACLProgram(windows[3]); err != nil {
		t.Fatal(err)
	}
	functions := make([]string, 0, 4)
	for _, name := range []string{"Assert-ExactPrivateACL", "Set-ExactPrivateACL", "Set-ExactPrivateDirectoryACL", "Set-ExactPrivateFileACL"} {
		function, err := extractREADMEPowerShellFunction(windows[3], name)
		if err != nil {
			t.Fatal(err)
		}
		functions = append(functions, function)
	}
	root := t.TempDir()
	configDir := filepath.Join(root, "config")
	runtimeDir := filepath.Join(root, "runtime")
	for _, path := range []string{configDir, runtimeDir} {
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatalf("create private directory fixture: %v", err)
		}
	}
	configFile := filepath.Join(configDir, "config.toml")
	keyFile := filepath.Join(configDir, "gateway.key")
	for _, path := range []string{configFile, keyFile} {
		if err := os.WriteFile(path, []byte("fixture\n"), 0o600); err != nil {
			t.Fatalf("create private file fixture: %v", err)
		}
	}
	targets := []struct {
		name      string
		path      string
		directory bool
	}{
		{name: "config directory", path: configDir, directory: true},
		{name: "runtime directory", path: runtimeDir, directory: true},
		{name: "config file", path: configFile},
		{name: "key file", path: keyFile},
	}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "icacls.exe", target.path, "/grant", "*S-1-1-0:R") //nolint:gosec // Fixed Windows utility, well-known Everyone SID, and test-owned targets.
			output, err := command.CombinedOutput()
			if ctx.Err() != nil {
				t.Fatal("icacls foreign-rule fixture exceeded its local execution deadline")
			}
			if err != nil {
				t.Fatalf("create explicit foreign ACL fixture: %v output=%q", err, output)
			}
			kind := "file"
			readmeSetter := "Set-ExactPrivateFileACL $env:README_ACL_PATH"
			if target.directory {
				kind = "directory"
				readmeSetter = "Set-ExactPrivateDirectoryACL $env:README_ACL_PATH"
			}
			script := "$ErrorActionPreference = 'Stop'\n" +
				"$CurrentSID = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value\n" +
				strings.Join(functions, "\n") + "\n" + readmeSetter + "\n" + readmeWindowsIndependentACLAssertion
			output, err = runREADMEPowerShell(t, script,
				"README_ACL_PATH="+target.path,
				"README_ACL_KIND="+kind,
			)
			if err != nil {
				t.Fatalf("documented ACL assertion or independent Get-Acl check failed: %v output=%q", err, output)
			}
		})
	}
}

func extractREADMEWindowsChecksumProgram(block string) (string, error) {
	const startMarker = `$ArchivePattern = '^[0-9a-f]{64} \*' + [regex]::Escape($ArchiveName) + '$'`
	const endMarker = "if (-not [String]::Equals($ExpectedSHA, $ActualSHA, [StringComparison]::OrdinalIgnoreCase)) {\n  throw 'archive checksum mismatch'\n}"
	start := strings.Index(block, startMarker)
	end := strings.Index(block, endMarker)
	if start < 0 || end < start || strings.Count(block, startMarker) != 1 || strings.Count(block, endMarker) != 1 {
		return "", errors.New("PowerShell checksum comparison program is missing or not unique")
	}
	return block[start : end+len(endMarker)], nil
}

func extractREADMEPowerShellFunction(block, name string) (string, error) {
	marker := "function " + name
	start := strings.Index(block, marker)
	if start < 0 || strings.Count(block, marker) != 1 {
		return "", fmt.Errorf("PowerShell function %s is missing or not unique", name)
	}
	opening := strings.Index(block[start:], "{")
	if opening < 0 {
		return "", fmt.Errorf("PowerShell function %s has no body", name)
	}
	depth := 0
	for index := start + opening; index < len(block); index++ {
		switch block[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return block[start : index+1], nil
			}
		}
	}
	return "", fmt.Errorf("PowerShell function %s has an unclosed body", name)
}

func runREADMEPowerShell(t *testing.T, script string, environment ...string) ([]byte, error) {
	t.Helper()
	powerShell := ""
	for _, candidate := range []string{"pwsh.exe", "powershell.exe"} {
		path, err := exec.LookPath(candidate)
		if err == nil {
			powerShell = path
			break
		}
	}
	if powerShell == "" {
		t.Fatal("Windows CI must provide pwsh.exe or powershell.exe")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, powerShell, "-NoLogo", "-NoProfile", "-NonInteractive", "-Command", script) //nolint:gosec // Fixed PowerShell runtime and repository-owned scripts.
	command.Env = append(os.Environ(), environment...)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatal("PowerShell README contract exceeded its local execution deadline")
	}
	return output, err
}

const readmeWindowsIndependentACLAssertion = `$ACL = Get-Acl -LiteralPath $env:README_ACL_PATH
if (-not $ACL.AreAccessRulesProtected) { throw 'independent check: inherited DACL' }
$OwnerSID = $ACL.GetOwner([Security.Principal.SecurityIdentifier]).Value
$CurrentSID = [Security.Principal.WindowsIdentity]::GetCurrent().User.Value
if ($OwnerSID -ne $CurrentSID) { throw 'independent check: wrong owner' }
$Rules = @($ACL.Access)
if ($Rules.Count -ne 1) { throw 'independent check: rule count' }
$Rule = $Rules[0]
$RuleSID = $Rule.IdentityReference.Translate([Security.Principal.SecurityIdentifier]).Value
if ($RuleSID -ne $CurrentSID) { throw 'independent check: wrong rule identity' }
if ($Rule.IsInherited) { throw 'independent check: inherited rule' }
if ($Rule.AccessControlType -ne [Security.AccessControl.AccessControlType]::Allow) { throw 'independent check: non-allow rule' }
if ($env:README_ACL_KIND -eq 'directory') {
  $ExpectedRights = [Security.AccessControl.FileSystemRights]::FullControl
  $ExpectedInheritance = [Security.AccessControl.InheritanceFlags]::ContainerInherit -bor [Security.AccessControl.InheritanceFlags]::ObjectInherit
} else {
  $ExpectedRights = [Security.AccessControl.FileSystemRights]::Read -bor [Security.AccessControl.FileSystemRights]::Write -bor [Security.AccessControl.FileSystemRights]::Synchronize
  $ExpectedInheritance = [Security.AccessControl.InheritanceFlags]::None
}
if ($Rule.FileSystemRights -ne $ExpectedRights) { throw 'independent check: wrong rights' }
if ($Rule.InheritanceFlags -ne $ExpectedInheritance) { throw 'independent check: wrong inheritance' }
if ($Rule.PropagationFlags -ne [Security.AccessControl.PropagationFlags]::None) { throw 'independent check: wrong propagation' }
`

func TestPublicPolicyContributionSecurityAndIgnoreBoundary(t *testing.T) {
	contributing := string(readRepositoryFile(t, "CONTRIBUTING.md"))
	requireContainsAll(t, "CONTRIBUTING.md", contributing,
		"Go 1.26.5", "golangci-lint v2.12.2", "TDD", "failing test",
		"fake CLI", "gofmt", "go vet", "golangci-lint", "go test ./...",
		"go test -race ./...", "go test -tags=integration ./...", "-trimpath",
		"cross-platform", "Windows Job Object", "private security",
		"no real provider CLI", "credentials", "inference",
	)
	for _, forbidden := range []string{"contributor license agreement", "Developer Certificate of Origin", "support SLA", "release SLA"} {
		if strings.Contains(strings.ToLower(contributing), strings.ToLower(forbidden)) {
			t.Fatalf("CONTRIBUTING.md invents an out-of-scope policy %q", forbidden)
		}
	}

	security := string(readRepositoryFile(t, "SECURITY.md"))
	requireContainsAll(t, "SECURITY.md", security,
		"https://github.com/krkarma777/ai-cli-gateway/security/advisories/new",
		"private vulnerability reporting", "not a public issue", "main",
		"real tokens", "auth files", "prompts", "model outputs", "provider stderr",
		"account identity", "sensitive paths", "fake CLI", "synthetic",
		"upstream provider",
	)
	deadlinePromise := regexp.MustCompile(`(?i)(respond|reply|resolve|fix).{0,24}within\s+[0-9]+\s+(hour|day|week)`)
	if deadlinePromise.MatchString(security) || strings.Contains(strings.ToLower(security), "support sla") {
		t.Fatal("SECURITY.md promises an unsupported response or remediation deadline")
	}

	ignore := string(readRepositoryFile(t, ".gitignore"))
	wantLines := []string{
		"/ai-cli-gateway", "/fake-ai-cli", "*.test", "*.test.exe", "*.exe", "*.prof",
		"coverage*.out", "/.idea/", "/.vscode/", "/docs/superpowers/",
		"/config.toml", ".env", ".env.*", "!.env.example",
		"/.codex/", "/.claude/", "/.gemini/", "auth.json", ".credentials.json",
		"credentials.json", "oauth_creds.json", "google_accounts.json",
	}
	gotLines := nonCommentLines(ignore)
	if !reflect.DeepEqual(gotLines, wantLines) {
		t.Fatalf(".gitignore rules = %q, want exact narrow rules %q", gotLines, wantLines)
	}
	// The exact wantLines comparison above is what pins the rule set. This loop
	// names the published paths that must never be hidden, so a rule covering one
	// of them fails with a specific reason instead of a whole-file diff.
	for _, forbidden := range []string{
		"config.example.toml", "docs/getting-started", "docs/reference",
		"docs/releases", "README", "settings.json", "internal/securitytest",
	} {
		if strings.Contains(ignore, forbidden) {
			t.Fatalf(".gitignore contains forbidden broad/public rule %q", forbidden)
		}
	}
}

func TestGitAttributesPinsTextCheckoutToLF(t *testing.T) {
	attributes := string(readRepositoryFile(t, ".gitattributes"))
	if attributes != "* text=auto eol=lf\n" {
		t.Fatalf(".gitattributes = %q, want one repository-wide LF rule", attributes)
	}
}

func requireContainsAll(t *testing.T, name, document string, required ...string) {
	t.Helper()
	for _, value := range required {
		if !strings.Contains(document, value) {
			t.Fatalf("%s is missing %q", name, value)
		}
	}
}

func requireNearby(t *testing.T, name, document, first, second string) {
	t.Helper()
	for search := 0; search < len(document); {
		relative := strings.Index(document[search:], first)
		if relative < 0 {
			break
		}
		start := search + relative
		left := start - 300
		if left < 0 {
			left = 0
		}
		right := start + len(first) + 300
		if right > len(document) {
			right = len(document)
		}
		if strings.Contains(document[left:right], second) {
			return
		}
		search = start + len(first)
	}
	t.Fatalf("%s does not place %q near %q", name, second, first)
}

func collapseWhitespace(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func nonCommentLines(document string) []string {
	lines := make([]string, 0)
	for _, line := range strings.Split(strings.ReplaceAll(document, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			lines = append(lines, trimmed)
		}
	}
	return lines
}

func TestLicenseAndNoticesMatchFrozenReviewedSurface(t *testing.T) {
	license := readRepositoryFile(t, "LICENSE")
	sum := sha256.Sum256(license)
	if got, want := hex.EncodeToString(sum[:]), "cfc7749b96f63bd31c3c42b5c471bf756814053e847c10f3eb003417bc523d30"; got != want {
		t.Fatalf("LICENSE SHA-256 = %s, want unmodified Apache-2.0 text %s", got, want)
	}

	notices := string(readRepositoryFile(t, "THIRD_PARTY_NOTICES.md"))
	wantModules := map[string]struct{}{
		"github.com/pelletier/go-toml/v2@v2.4.3":          {},
		"github.com/santhosh-tekuri/jsonschema/v6@v6.0.2": {},
		"golang.org/x/sys@v0.47.0":                        {},
		"golang.org/x/text@v0.14.0":                       {},
	}
	modulePattern := regexp.MustCompile(`[A-Za-z0-9.-]+(?:/[A-Za-z0-9._-]+)+@v[0-9][A-Za-z0-9.-]*`)
	gotModules := make(map[string]struct{})
	for _, module := range modulePattern.FindAllString(notices, -1) {
		gotModules[module] = struct{}{}
	}
	if !reflect.DeepEqual(gotModules, wantModules) {
		t.Fatalf("THIRD_PARTY_NOTICES module union = %v, want exact frozen union %v", gotModules, wantModules)
	}

	requireNearby(t, "go-toml license classification", notices, "github.com/pelletier/go-toml/v2@v2.4.3", "MIT")
	requireNearby(t, "jsonschema license classification", notices, "github.com/santhosh-tekuri/jsonschema/v6@v6.0.2", "Apache-2.0")
	requireNearby(t, "x/sys license classification", notices, "golang.org/x/sys@v0.47.0", "BSD-3-Clause")
	requireNearby(t, "x/text license classification", notices, "golang.org/x/text@v0.14.0", "BSD-3-Clause")
	requireContainsAll(t, "THIRD_PARTY_NOTICES pinned evidence", notices,
		"https://github.com/pelletier/go-toml/blob/v2.4.3/LICENSE",
		"https://github.com/santhosh-tekuri/jsonschema/blob/v6.0.2/LICENSE",
		"https://github.com/golang/sys/blob/v0.47.0/LICENSE",
		"https://github.com/golang/text/blob/v0.14.0/LICENSE",
		"https://github.com/golang/sys/blob/v0.47.0/PATENTS",
		"https://github.com/golang/text/blob/v0.14.0/PATENTS",
		"no separate NOTICE",
	)
	for _, extra := range []string{
		"github.com/dlclark/regexp2@v1.11.0",
		"golang.org/x/mod@v0.8.0",
		"golang.org/x/tools@v0.6.0",
	} {
		if strings.Contains(notices, extra) {
			t.Fatalf("THIRD_PARTY_NOTICES includes non-compiled graph extra %q", extra)
		}
	}
	for name, requiredText := range map[string]string{
		"go-toml MIT license": goTomlMITLicenseText,
		"x/sys BSD license":   goSysBSDLicenseText,
		"x/text BSD license":  goTextBSDLicenseText,
		"Go PATENTS grant":    goPatentsGrantText,
	} {
		if !strings.Contains(notices, requiredText) {
			t.Fatalf("THIRD_PARTY_NOTICES is missing exact applicable %s", name)
		}
	}

	const (
		sysHeading  = "## golang.org/x/sys@v0.47.0"
		textHeading = "## golang.org/x/text@v0.14.0"
	)
	sysStart := strings.Index(notices, sysHeading)
	textStart := strings.Index(notices, textHeading)
	if sysStart < 0 || textStart <= sysStart {
		t.Fatal("THIRD_PARTY_NOTICES must keep the frozen x/sys section before x/text")
	}
	sysSection := notices[sysStart:textStart]
	textSection := notices[textStart:]
	if !strings.Contains(sysSection, goSysBSDLicenseText) ||
		strings.Contains(sysSection, goTextBSDLicenseText) {
		t.Fatal("THIRD_PARTY_NOTICES does not associate the exact x/sys BSD notice only with x/sys")
	}
	if !strings.Contains(textSection, goTextBSDLicenseText) ||
		strings.Contains(textSection, goSysBSDLicenseText) {
		t.Fatal("THIRD_PARTY_NOTICES does not associate the exact x/text BSD notice only with x/text")
	}
}

const goTomlMITLicenseText = `The MIT License (MIT)

go-toml v2
Copyright (c) 2021 - 2023 Thomas Pelletier

Permission is hereby granted, free of charge, to any person obtaining a copy
of this software and associated documentation files (the "Software"), to deal
in the Software without restriction, including without limitation the rights
to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
copies of the Software, and to permit persons to whom the Software is
furnished to do so, subject to the following conditions:

The above copyright notice and this permission notice shall be included in all
copies or substantial portions of the Software.

THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
SOFTWARE.`

const goSysBSDLicenseText = `Copyright 2009 The Go Authors.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google LLC nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.`

const goTextBSDLicenseText = `Copyright (c) 2009 The Go Authors. All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are
met:

   * Redistributions of source code must retain the above copyright
notice, this list of conditions and the following disclaimer.
   * Redistributions in binary form must reproduce the above
copyright notice, this list of conditions and the following disclaimer
in the documentation and/or other materials provided with the
distribution.
   * Neither the name of Google Inc. nor the names of its
contributors may be used to endorse or promote products derived from
this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS
"AS IS" AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT
LIMITED TO, THE IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR
A PARTICULAR PURPOSE ARE DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT
OWNER OR CONTRIBUTORS BE LIABLE FOR ANY DIRECT, INDIRECT, INCIDENTAL,
SPECIAL, EXEMPLARY, OR CONSEQUENTIAL DAMAGES (INCLUDING, BUT NOT
LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR SERVICES; LOSS OF USE,
DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER CAUSED AND ON ANY
THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY, OR TORT
(INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.`

const goPatentsGrantText = `Additional IP Rights Grant (Patents)

"This implementation" means the copyrightable works distributed by
Google as part of the Go project.

Google hereby grants to You a perpetual, worldwide, non-exclusive,
no-charge, royalty-free, irrevocable (except as stated in this section)
patent license to make, have made, use, offer to sell, sell, import,
transfer and otherwise run, modify and propagate the contents of this
implementation of Go, where such license applies only to those patent
claims, both currently owned or controlled by Google and acquired in
the future, licensable by Google that are necessarily infringed by this
implementation of Go.  This grant does not include claims that would be
infringed only as a consequence of further modification of this
implementation.  If you or your agent or exclusive licensee institute or
order or agree to the institution of patent litigation against any
entity (including a cross-claim or counterclaim in a lawsuit) alleging
that this implementation of Go or any code incorporated within this
implementation of Go constitutes direct or contributory patent
infringement, or inducement of patent infringement, then any patent
rights granted to you under this License for this implementation of Go
shall terminate as of the date such litigation is filed.`

func TestSystemdHardenedServiceBoundary(t *testing.T) {
	service := string(readRepositoryFile(t, "deploy/systemd/ai-cli-gateway.service"))
	assignments := parseSystemdAssignments(t, service)

	for key, value := range map[string]string{
		"Unit.Description":              "AI CLI Gateway",
		"Unit.After":                    "network-online.target",
		"Unit.Wants":                    "network-online.target",
		"Service.Type":                  "simple",
		"Service.User":                  "ai-cli-gateway",
		"Service.Group":                 "ai-cli-gateway",
		"Service.EnvironmentFile":       "/etc/ai-cli-gateway/ai-cli-gateway.env",
		"Service.ExecStart":             "/opt/ai-cli-gateway/bin/ai-cli-gateway serve --config /etc/ai-cli-gateway/config.toml",
		"Service.RuntimeDirectory":      "ai-cli-gateway",
		"Service.RuntimeDirectoryMode":  "0700",
		"Service.StateDirectory":        "ai-cli-gateway",
		"Service.StateDirectoryMode":    "0700",
		"Service.ReadWritePaths":        "/run/ai-cli-gateway /var/lib/ai-cli-gateway",
		"Service.NoNewPrivileges":       "true",
		"Service.PrivateTmp":            "true",
		"Service.PrivateDevices":        "true",
		"Service.ProtectSystem":         "strict",
		"Service.ProtectHome":           "true",
		"Service.UMask":                 "0077",
		"Service.CapabilityBoundingSet": "",
		"Service.AmbientCapabilities":   "",
		"Service.LimitCORE":             "0",
		"Service.KillMode":              "control-group",
		"Service.Restart":               "on-failure",
		"Service.RestartSec":            "5s",
		"Service.TimeoutStopSec":        "45s",
		"Install.WantedBy":              "multi-user.target",
	} {
		requireSystemdAssignment(t, assignments, key, value)
	}

	requireContainsAll(t, "systemd operator guidance", service,
		"root-owned", "0600", "127.0.0.1:8080", "loopback",
		"Node-based CLIs", "executable memory", "MemoryDenyWriteExecute",
		"deliberately not enabled", "dedicated service identity",
	)
	for _, forbidden := range []string{"Service.Environment", "Service.PrivateNetwork", "Service.MemoryDenyWriteExecute"} {
		if _, ok := assignments[forbidden]; ok {
			t.Fatalf("systemd service activates forbidden directive %s", forbidden)
		}
	}
	execStart := assignments["Service.ExecStart"][0]
	for _, secretMarker := range []string{"API_KEY", "TOKEN", "SECRET", "PASSWORD", "Bearer", "--api-key", "--token"} {
		if strings.Contains(execStart, secretMarker) {
			t.Fatalf("systemd ExecStart contains inline secret marker %q", secretMarker)
		}
	}
	if strings.Contains(service, "Environment=\"") || strings.Contains(service, "Environment='") {
		t.Fatal("systemd service contains a literal Environment= assignment")
	}
}

func parseSystemdAssignments(t *testing.T, document string) map[string][]string {
	t.Helper()
	result := make(map[string][]string)
	section := ""
	for lineNumber, line := range strings.Split(strings.ReplaceAll(document, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, ";") {
			continue
		}
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = strings.TrimSuffix(strings.TrimPrefix(trimmed, "["), "]")
			continue
		}
		separator := strings.IndexByte(trimmed, '=')
		if section == "" || separator <= 0 {
			t.Fatalf("systemd line %d is not a section or assignment", lineNumber+1)
		}
		key := section + "." + strings.TrimSpace(trimmed[:separator])
		value := strings.TrimSpace(trimmed[separator+1:])
		result[key] = append(result[key], value)
	}
	return result
}

func requireSystemdAssignment(t *testing.T, assignments map[string][]string, key, value string) {
	t.Helper()
	got := assignments[key]
	if len(got) != 1 || got[0] != value {
		t.Fatalf("systemd %s = %q, want exactly %q", key, got, value)
	}
}

func TestWorkflowMultiPlatformReleaseContract(t *testing.T) {
	workflow := string(readRepositoryFile(t, ".github/workflows/ci.yml"))
	if err := validateSDKCIWorkflowContract(workflow); err != nil {
		t.Fatalf("CI SDK contract: %v", err)
	}
	if strings.Contains(workflow, "pull_request_target") {
		t.Fatal("CI must not use privileged pull_request_target")
	}
	if got := topLevelYAMLBlockLines(workflow, "permissions:"); !reflect.DeepEqual(got, []string{"contents: read"}) {
		t.Fatalf("CI permissions = %q, want only contents: read", got)
	}
	if regexp.MustCompile(`(?m)^\s*[A-Za-z0-9_-]+:\s*write\s*$`).MatchString(workflow) {
		t.Fatal("CI grants a write permission")
	}

	jobs := extractYAMLJobBlocks(t, workflow)
	wantJobs := map[string]struct{}{
		"lint": {}, "linux": {}, "macos": {}, "windows": {}, "cross-build": {}, "sdk-contract": {},
	}
	gotJobs := make(map[string]struct{}, len(jobs))
	for name := range jobs {
		gotJobs[name] = struct{}{}
	}
	if !reflect.DeepEqual(gotJobs, wantJobs) {
		t.Fatalf("CI jobs = %v, want exact release jobs %v", gotJobs, wantJobs)
	}
	for name, block := range jobs {
		if !strings.Contains(block, "timeout-minutes:") {
			t.Fatalf("CI job %q has no bounded timeout-minutes", name)
		}
		requireContainsAll(t, "CI job "+name, block,
			checkoutAction, setupGoAction,
			"go-version-file: .go-version", "cache: true")
	}

	wantActionsByJob := expectedCIJobActions()
	for name, block := range jobs {
		actions, err := parsedYAMLJobActions(block)
		if err != nil {
			t.Fatalf("parse CI job %q actions: %v", name, err)
		}
		if !reflect.DeepEqual(actions, wantActionsByJob[name]) {
			t.Fatalf("CI job %q actions = %q, want exact ordered actions %q", name, actions, wantActionsByJob[name])
		}
	}
	requireContainsAll(t, "lint job", jobs["lint"],
		"runs-on: ubuntu-latest", "gofmt -l .", "go vet ./...",
		golangciAction, "version: v2.12.2")
	if err := validateCIActionlintStep(workflow, jobs["lint"]); err != nil {
		t.Fatalf("CI actionlint contract: %v", err)
	}
	requireContainsAll(t, "Linux job", jobs["linux"],
		"runs-on: ubuntu-latest", "go mod verify", "go test -count=1 ./...",
		"go test -race -timeout=20m -count=1 ./...", "go test -tags=integration -count=1 ./...",
		"go test -tags=live -run '^$' ./internal/provider/...", "CGO_ENABLED: 0",
		"go build -trimpath", "RUNNER_TEMP")
	requireContainsAll(t, "macOS job", jobs["macos"],
		"runs-on: macos-latest", "go test -count=1 ./...", "go test -race -timeout=20m -count=1 ./...",
		"go test -tags=integration -count=1 ./...", "CGO_ENABLED: 0",
		"go build -trimpath", "RUNNER_TEMP")
	requireContainsAll(t, "Windows job", jobs["windows"],
		"runs-on: windows-latest", "go test -count=1 ./...",
		"go test -tags=integration -count=1 -v ./...", "CGO_ENABLED: 0",
		"go build -trimpath", "$env:RUNNER_TEMP")
	if strings.Contains(jobs["windows"], "continue-on-error") ||
		regexp.MustCompile(`(?m)\bgo\s+test\s+-c(?:\s|$)`).MatchString(jobs["windows"]) {
		t.Fatal("Windows CI may not allow or replace native containment execution")
	}

	cross := jobs["cross-build"]
	requireContainsAll(t, "cross-build job", cross,
		"runs-on: ubuntu-latest", "matrix:", "include:", "CGO_ENABLED: 0",
		"GOOS", "GOARCH", "go build -trimpath", "RUNNER_TEMP", ".exe")
	for _, tuple := range [][2]string{
		{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"},
		{"darwin", "arm64"}, {"windows", "amd64"},
	} {
		requireYAMLMatrixTuple(t, cross, tuple[0], tuple[1])
	}
	if strings.Count(cross, "goos:") != 5 || strings.Count(cross, "goarch:") != 5 {
		t.Fatal("cross-build matrix must contain exactly five explicit target tuples")
	}
	requireContainsAll(t, "SDK contract job", jobs["sdk-contract"],
		"runs-on: ubuntu-latest", "timeout-minutes: 12",
		setupPythonAction, setupNodeAction)

	if strings.Count(workflow, "-tags=live") != 1 ||
		!strings.Contains(workflow, "go test -tags=live -run '^$' ./internal/provider/...") {
		t.Fatal("CI live-tag coverage must be compile-only and occur exactly once")
	}
	for _, forbidden := range []string{
		"secrets.", "upload-artifact", "download-artifact", "AI_CLI_GATEWAY_LIVE_",
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"continue-on-error: true", "allow-failure", "actions/checkout@v6", "actions/setup-go@v6",
		"actions/checkout@v7", "actions/setup-go@v7", "golangci/golangci-lint-action@v9",
		"d583c34f0599d37dbac4a198b9c83201be380893",
	} {
		if strings.Contains(workflow, forbidden) {
			t.Fatalf("CI contains forbidden secret, upload, bypass, or stale contract %q", forbidden)
		}
	}
	lowerWorkflow := strings.ToLower(workflow)
	for _, invocation := range []string{" codex ", " claude ", " gemini "} {
		if strings.Contains(lowerWorkflow, invocation) {
			t.Fatalf("CI appears to invoke an installed provider via %q", strings.TrimSpace(invocation))
		}
	}
}

func validateCIActionlintStep(workflow, lintJob string) error {
	steps, err := parseYAMLSteps(lintJob)
	if err != nil {
		return err
	}
	var actionlintStep string
	for _, step := range steps {
		if strings.HasPrefix(step, "- name: Validate workflow syntax\n") {
			if actionlintStep != "" {
				return errors.New("multiple actionlint validation steps")
			}
			actionlintStep = step
		}
	}
	if actionlintStep == "" {
		return errors.New("missing parsed Validate workflow syntax step")
	}
	if err := requireExactTextSHA256("parsed actionlint step metadata", actionlintStep, "a931acb732b7b1ff34549eb83f61795e502e014023bd6c7b7f4f962af0d84597"); err != nil {
		return err
	}
	actionlintRun, err := decodedWorkflowStepRun([]byte(workflow), "lint", "Validate workflow syntax")
	if err != nil {
		return fmt.Errorf("decode actionlint run: %w", err)
	}
	if err := requireExactTextSHA256("decoded actionlint run", actionlintRun, "fb19e602eee29370c9ca45392e5178b91ee76c181e05c2368984ae8de95cf5d3"); err != nil {
		return err
	}
	for _, required := range []string{
		"  shell: bash",
		"set -euo pipefail",
		"umask 077",
		`ACTIONLINT_ROOT="${RUNNER_TEMP}/actionlint-tools"`,
		`ENV_BIN=/usr/bin/env`,
		`validate_build_tool() {`,
		`GO_IDENTITY="$(validate_build_tool "${GO_BIN}")"`,
		`test "$(validate_build_tool "${GO_BIN}" "${GO_IDENTITY}")" = "${GO_IDENTITY}"`,
		`(( (permissions & 07000) == 0 )) || return 1`,
		`(( (permissions & 07022) == 0 )) || return 1`,
		`validate_authority "${binary%/*}" || return 1`,
		`"${ENV_BIN}" -i`,
		`GOTOOLCHAIN=local`,
		`GOPROXY=https://proxy.golang.org`,
		`GOSUMDB=sum.golang.org`,
		`GOPRIVATE=`,
		`GONOPROXY=`,
		`GONOSUMDB=`,
		`GOINSECURE=`,
		`GOENV=off`,
		`GOFLAGS=`,
		`GOWORK=off`,
		`CGO_ENABLED=0`,
		`install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`,
		`run_clean_actionlint -version`,
		`run_clean_actionlint -help`,
		`-config-file "${ACTIONLINT_ROOT}/config/actionlint.yaml"`,
		`.github/workflows/ci.yml .github/workflows/release.yml`,
	} {
		if !strings.Contains(actionlintStep, required) {
			return fmt.Errorf("parsed actionlint step is missing %q", required)
		}
	}
	if err := validateCommandSubstitutionGuards(actionlintStep, "expected_identity", false); err != nil {
		return fmt.Errorf("actionlint binary validation: %w", err)
	}
	if strings.Contains(actionlintStep, "curl ") || strings.Contains(actionlintStep, "wget ") {
		return errors.New("actionlint step uses a mutable downloader")
	}
	if regexp.MustCompile(`(?m)(^|[[:space:]])PATH=`).MatchString(actionlintStep) || strings.Contains(actionlintStep, "${PATH}") {
		return errors.New("actionlint isolated children receive PATH")
	}
	if strings.Count(actionlintStep, `"${ENV_BIN}" -i`) != 2 {
		return errors.New("actionlint step must define exactly two env -i helpers")
	}
	lines := trimmedShellLines(actionlintStep)
	if shellLineCount(lines, `run_clean_go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`) != 1 ||
		shellLinePrefixCount(lines, "run_clean_go install ") != 1 {
		return errors.New("actionlint install invocation is not the one exact executable line")
	}
	wantLint := []string{
		`run_clean_actionlint \`,
		`-config-file "${ACTIONLINT_ROOT}/config/actionlint.yaml" \`,
		`-shellcheck= -pyflakes= -no-color \`,
		`.github/workflows/ci.yml .github/workflows/release.yml`,
	}
	if shellLineSequenceCount(lines, wantLint) != 1 || shellLinePrefixCount(lines, "run_clean_actionlint ") != 1 ||
		shellLineCount(lines, `test "$(run_clean_actionlint -version | sed -n '1p')" = v1.7.12`) != 1 ||
		shellLineCount(lines, `actionlint_help="$(run_clean_actionlint -help 2>&1)"`) != 1 {
		return errors.New("actionlint invocations do not contain the exact version, help, and lint commands")
	}
	for _, exact := range []struct {
		text  string
		count int
	}{
		{`run_clean_go env GOVERSION`, 1},
		{`run_clean_go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12`, 1},
		{`run_clean_actionlint -version`, 1},
		{`run_clean_actionlint -help`, 1},
		{`revalidate_build_tools`, 3},
		{`revalidate_actionlint`, 4},
	} {
		if strings.Count(actionlintStep, exact.text) != exact.count {
			return fmt.Errorf("actionlint step %q count = %d, want %d", exact.text, strings.Count(actionlintStep, exact.text), exact.count)
		}
	}
	goHelperStart := strings.Index(actionlintStep, "run_clean_go() {")
	actionlintHelperStart := strings.Index(actionlintStep, "run_clean_actionlint() {")
	if goHelperStart < 0 || actionlintHelperStart < 0 || actionlintHelperStart <= goHelperStart {
		return errors.New("actionlint clean helper boundaries are missing")
	}
	goHelper := actionlintStep[goHelperStart:actionlintHelperStart]
	goNames, err := isolatedInvocationEnvironmentNames(goHelper, `"${GO_BIN}" "$@"`)
	if err != nil {
		return fmt.Errorf("clean Go helper: %w", err)
	}
	wantGoNames := []string{"CGO_ENABLED", "GOBIN", "GOCACHE", "GOENV", "GOFLAGS", "GOINSECURE", "GOMODCACHE", "GONOPROXY", "GONOSUMDB", "GOPATH", "GOPRIVATE", "GOPROXY", "GOSUMDB", "GOTOOLCHAIN", "GOWORK", "HOME", "LANG", "LC_ALL", "TMPDIR", "TZ", "XDG_CONFIG_HOME"}
	if !reflect.DeepEqual(goNames, wantGoNames) {
		return fmt.Errorf("clean Go environment names = %v, want %v", goNames, wantGoNames)
	}
	actionlintHelper := actionlintStep[actionlintHelperStart:]
	runtimeNames, err := isolatedInvocationEnvironmentNames(actionlintHelper, `"${ACTIONLINT_BIN}" "$@"`)
	if err != nil {
		return fmt.Errorf("clean actionlint helper: %w", err)
	}
	wantRuntimeNames := []string{"HOME", "LANG", "LC_ALL", "TMPDIR", "TZ", "XDG_CONFIG_HOME"}
	if !reflect.DeepEqual(runtimeNames, wantRuntimeNames) {
		return fmt.Errorf("clean actionlint environment names = %v, want %v", runtimeNames, wantRuntimeNames)
	}
	return nil
}

func isolatedInvocationEnvironmentNames(helper, executableLine string) ([]string, error) {
	start := strings.Index(helper, `"${ENV_BIN}" -i`)
	end := strings.Index(helper, executableLine)
	if start < 0 || end < 0 || end <= start {
		return nil, errors.New("missing absolute env -i or executable boundary")
	}
	matches := regexp.MustCompile(`\b([A-Z][A-Z0-9_]*)=`).FindAllStringSubmatch(helper[start:end], -1)
	names := make([]string, 0, len(matches))
	seen := make(map[string]struct{}, len(matches))
	for _, match := range matches {
		if _, duplicate := seen[match[1]]; duplicate {
			return nil, fmt.Errorf("duplicate environment name %s", match[1])
		}
		seen[match[1]] = struct{}{}
		names = append(names, match[1])
	}
	slices.Sort(names)
	return names, nil
}

func TestWorkflowMultiPlatformReleaseContractRejectsMutations(t *testing.T) {
	workflow := string(readRepositoryFile(t, ".github/workflows/ci.yml"))
	if err := validateSDKCIWorkflowContract(workflow); err != nil {
		t.Fatalf("base CI SDK contract must be valid before mutation checks: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{name: "actionlint missing env isolation", mutate: replaceCIOnce(`            "${ENV_BIN}" -i \`, `            "${ENV_BIN}" \`)},
		{name: "actionlint blank line breaks env isolation", mutate: replaceCIOnce("            \"${ENV_BIN}\" -i \\\n              HOME=\"${ACTIONLINT_ROOT}/home\" \\\n", "            \"${ENV_BIN}\" -i \\\n\n              HOME=\"${ACTIONLINT_ROOT}/home\" \\\n")},
		{name: "actionlint step BASH_ENV injection", mutate: replaceCIOnce("      - name: Validate workflow syntax\n        shell: bash\n", "      - name: Validate workflow syntax\n        env:\n          BASH_ENV: /tmp/actionlint-env\n        shell: bash\n")},
		{name: "actionlint custom shell template", mutate: replaceCIOnce("      - name: Validate workflow syntax\n        shell: bash\n", "      - name: Validate workflow syntax\n        shell: bash -e {0}\n")},
		{name: "actionlint Go child receives PATH", mutate: replaceCIOnce(`              HOME="${ACTIONLINT_ROOT}/home" \`, "              PATH=/usr/bin \\\n"+`              HOME="${ACTIONLINT_ROOT}/home" \`)},
		{name: "actionlint Go policy omitted", mutate: replaceCIOnce(" GOINSECURE= GOENV=off GOFLAGS= GOWORK=off CGO_ENABLED=0 \\\n", " GOINSECURE= GOFLAGS= GOWORK=off CGO_ENABLED=0 \\\n")},
		{name: "actionlint Go child extra environment", mutate: replaceCIOnce(" GOINSECURE= GOENV=off GOFLAGS= GOWORK=off CGO_ENABLED=0 \\\n", " GOINSECURE= GOENV=off GOFLAGS= GOWORK=off CGO_ENABLED=0 ATTACKER=value \\\n")},
		{name: "actionlint uses relative Go", mutate: replaceCIOnce(`              "${GO_BIN}" "$@"`, `              go "$@"`)},
		{name: "actionlint Go uses strict generated-binary policy", mutate: replaceCIOnce(`GO_IDENTITY="$(validate_build_tool "${GO_BIN}")"`, `GO_IDENTITY="$(validate_binary "${GO_BIN}")"`)},
		{name: "actionlint Go special-bit guard removed", mutate: replaceCIOnce(`            (( (permissions & 07000) == 0 )) || return 1`+"\n", "")},
		{name: "actionlint Go ancestor authority added", mutate: replaceShellFunctionOnce("validate_build_tool", `(( (permissions & 07000) == 0 )) || return 1`, "(( (permissions & 07000) == 0 )) || return 1\n            validate_authority \"${binary%/*}\" || return 1")},
		{name: "actionlint authority early success", mutate: replaceShellFunctionOnce("validate_authority", "validate_authority() {", "validate_authority() {\n            return 0")},
		{name: "actionlint authority directory failure masked", mutate: replaceShellFunctionOnce("validate_authority", `test -d "${current}" || return 1`, `test -d "${current}"`)},
		{name: "actionlint authority mode conversion removed", mutate: replaceShellFunctionOnce("validate_authority", `permissions=$((8#${mode}))`, `permissions=0`)},
		{name: "actionlint resolver command failure masked", mutate: replaceShellFunctionOnce("resolve_binary", `candidate="$(command -v -- "$1")" || return 1`, `candidate="$(command -v -- "$1")"`)},
		{name: "actionlint resolver early success", mutate: replaceShellFunctionOnce("resolve_binary", "resolve_binary() {", "resolve_binary() {\n            return 0")},
		{name: "actionlint resolver identity output removed", mutate: replaceShellFunctionOnce("resolve_binary", `printf '%s\n' "${candidate}"`, `: "${candidate}"`)},
		{name: "actionlint Go symlink failure masked", mutate: replaceShellFunctionOnce("validate_build_tool", `test ! -L "${binary}" || return 1`, `test ! -L "${binary}"`)},
		{name: "actionlint Go early success", mutate: replaceShellFunctionOnce("validate_build_tool", "validate_build_tool() {", "validate_build_tool() {\n            return 0")},
		{name: "actionlint Go owner failure masked", mutate: replaceShellFunctionOnce("validate_build_tool", `owner="$(stat -c '%u' -- "${binary}")" || return 1`, `owner="$(stat -c '%u' -- "${binary}")"`)},
		{name: "actionlint Go mode conversion removed", mutate: replaceShellFunctionOnce("validate_build_tool", `permissions=$((8#${mode}))`, `permissions=0`)},
		{name: "actionlint Go identity failure masked", mutate: replaceShellFunctionOnce("validate_build_tool", `identity="$(stat -c '%d:%i' -- "${binary}")" || return 1`, `identity="$(stat -c '%d:%i' -- "${binary}")"`)},
		{name: "actionlint Go identity output removed", mutate: replaceShellFunctionOnce("validate_build_tool", `printf '%s\n' "${identity}"`, `: "${identity}"`)},
		{name: "actionlint strict executable failure masked", mutate: replaceShellFunctionOnce("validate_binary", `test -x "${binary}" || return 1`, `test -x "${binary}"`)},
		{name: "actionlint strict early success", mutate: replaceShellFunctionOnce("validate_binary", "validate_binary() {", "validate_binary() {\n            return 0")},
		{name: "actionlint strict mode conversion removed", mutate: replaceShellFunctionOnce("validate_binary", `permissions=$((8#${mode}))`, `permissions=0`)},
		{name: "actionlint strict identity failure masked", mutate: replaceShellFunctionOnce("validate_binary", `identity="$(stat -c '%d:%i' -- "${binary}")" || return 1`, `identity="$(stat -c '%d:%i' -- "${binary}")"`)},
		{name: "actionlint strict identity output removed", mutate: replaceShellFunctionOnce("validate_binary", `printf '%s\n' "${identity}"`, `: "${identity}"`)},
		{name: "actionlint alternate strict function redefinition", mutate: insertAfterShellFunction("validate_binary", "          function validate_binary { return 0; }\n")},
		{name: "actionlint strict mode failure masked", mutate: replaceCIOnce(`            (( (permissions & 07022) == 0 )) || return 1`, `            (( (permissions & 07022) == 0 ))`)},
		{name: "actionlint strict authority failure masked", mutate: replaceCIOnce(`            validate_authority "${binary%/*}" || return 1`, `            validate_authority "${binary%/*}"`)},
		{name: "actionlint runtime receives Go variable", mutate: replaceCIOnce("          run_clean_actionlint() {\n            \"${ENV_BIN}\" -i \\\n              HOME=\"${ACTIONLINT_ROOT}/home\" \\\n", "          run_clean_actionlint() {\n            \"${ENV_BIN}\" -i \\\n              GOFLAGS=attacker \\\n              HOME=\"${ACTIONLINT_ROOT}/home\" \\\n")},
		{name: "actionlint build identity revalidation omitted", mutate: replaceCIOnce("          revalidate_build_tools\n          test \"$(run_clean_go env GOVERSION)\" = go1.26.5\n", "          test \"$(run_clean_go env GOVERSION)\" = go1.26.5\n")},
		{
			name: "actionlint pinned install hidden in dead string",
			mutate: replaceCIOnce(
				"          run_clean_go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12\n",
				"          : 'run_clean_go install github.com/rhysd/actionlint/cmd/actionlint@v1.7.12'\n          run_clean_go install attacker.invalid/actionlint@v0\n",
			),
		},
		{
			name: "actionlint targets hidden in dead string",
			mutate: replaceCIOnce(
				"          run_clean_actionlint \\\n            -config-file \"${ACTIONLINT_ROOT}/config/actionlint.yaml\" \\\n            -shellcheck= -pyflakes= -no-color \\\n            .github/workflows/ci.yml .github/workflows/release.yml\n",
				"          : '-config-file \"${ACTIONLINT_ROOT}/config/actionlint.yaml\" .github/workflows/ci.yml .github/workflows/release.yml'\n          run_clean_actionlint /dev/null\n",
			),
		},
		{name: "missing workflow_call trigger", mutate: replaceCIOnce("  workflow_call:\n", "")},
		{name: "extra trigger", mutate: replaceCIOnce("  workflow_call:\n", "  workflow_call:\n  schedule:\n")},
		{name: "workflow_call inputs", mutate: replaceCIOnce("  workflow_call:\n", "  workflow_call:\n    inputs:\n")},
		{name: "workflow_call secrets", mutate: replaceCIOnce("  workflow_call:\n", "  workflow_call:\n    secrets:\n")},
		{name: "missing SDK job", mutate: replaceCIOnce("  sdk-contract:\n", "  sdk-contract-removed:\n")},
		{name: "extra job", mutate: replaceCIOnce("jobs:\n", "jobs:\n  unexpected:\n    runs-on: ubuntu-latest\n    timeout-minutes: 1\n    steps:\n")},
		{
			name: "second top-level jobs mapping",
			mutate: func(document string) string {
				return document + "\njobs:\n  shadow:\n    runs-on: ubuntu-latest\n    timeout-minutes: 1\n    steps:\n      - run: true\n"
			},
		},
		{
			name: "job write permissions",
			mutate: replaceCIOnce(
				"  lint:\n    runs-on: ubuntu-latest\n",
				"  lint:\n    runs-on: ubuntu-latest\n    permissions: { contents: write }\n",
			),
		},
		{
			name: "flow-style action step",
			mutate: replaceCIOnce(
				"  lint:\n    runs-on: ubuntu-latest\n    timeout-minutes: 10\n    steps:\n",
				"  lint:\n    runs-on: ubuntu-latest\n    timeout-minutes: 10\n    steps:\n      - { uses: attacker/action@deadbeef }\n",
			),
		},
		{
			name: "job if disables checks",
			mutate: replaceCIOnce(
				"  macos:\n    runs-on: macos-latest\n",
				"  macos:\n    runs-on: macos-latest\n    if: false\n",
			),
		},
		{
			name: "job continue-on-error bypass",
			mutate: replaceCIOnce(
				"  macos:\n    runs-on: macos-latest\n",
				"  macos:\n    runs-on: macos-latest\n    continue-on-error: true\n",
			),
		},
		{
			name: "step if disables checks",
			mutate: replaceCIOnce(
				"      - name: Unit tests\n        run: go test -count=1 ./...\n",
				"      - name: Unit tests\n        if: false\n        run: go test -count=1 ./...\n",
			),
		},
		{
			name: "step continue-on-error bypass",
			mutate: replaceCIOnce(
				"      - name: Unit tests\n        run: go test -count=1 ./...\n",
				"      - name: Unit tests\n        continue-on-error: true\n        run: go test -count=1 ./...\n",
			),
		},
		{
			name: "duplicate step uses field",
			mutate: replaceCIOnce(
				"      - uses: "+checkoutAction+"\n",
				"      - uses: "+checkoutAction+"\n        uses: attacker/action@deadbeef\n",
			),
		},
		{
			name: "duplicate legacy runs-on field",
			mutate: replaceCIOnce(
				"  lint:\n    runs-on: ubuntu-latest\n",
				"  lint:\n    runs-on: ubuntu-latest\n    runs-on: ubuntu-latest\n",
			),
		},
		{
			name: "duplicate legacy timeout field",
			mutate: replaceCIOnce(
				"  lint:\n    runs-on: ubuntu-latest\n    timeout-minutes: 10\n",
				"  lint:\n    runs-on: ubuntu-latest\n    timeout-minutes: 10\n    timeout-minutes: 10\n",
			),
		},
		{
			name: "Windows runner hidden by comment",
			mutate: replaceCIOnce(
				"  windows:\n    runs-on: windows-latest\n",
				"  windows:\n    runs-on: ubuntu-latest # runs-on: windows-latest\n",
			),
		},
		{
			name: "setup-go moved out of Linux with decoys",
			mutate: func(document string) string {
				linuxSetup := strings.Join([]string{
					"      - uses: " + setupGoAction,
					"        with:",
					"          go-version-file: .go-version",
					"          cache: true",
				}, "\n") + "\n"
				linuxDecoy := strings.Join([]string{
					"      - name: No Go setup",
					"        run: |",
					"          # " + setupGoAction,
					"          # go-version-file: .go-version",
					"          # cache: true",
					"          true",
				}, "\n") + "\n"
				lintIndex := strings.Index(document, linuxSetup)
				if lintIndex < 0 {
					return document
				}
				linuxRelative := strings.Index(document[lintIndex+len(linuxSetup):], linuxSetup)
				if linuxRelative < 0 {
					return document
				}
				linuxIndex := lintIndex + len(linuxSetup) + linuxRelative
				withoutLinux := document[:linuxIndex] + linuxDecoy + document[linuxIndex+len(linuxSetup):]
				return withoutLinux[:lintIndex] + linuxSetup + linuxSetup + withoutLinux[lintIndex+len(linuxSetup):]
			},
		},
		{
			name: "legacy run command and step name hidden by comments",
			mutate: replaceCIOnce(
				"      - name: Unit tests\n        run: go test -count=1 ./...\n",
				"      - name: No unit coverage # Unit tests\n        run: echo skipped # go test -count=1 ./...\n",
			),
		},
		{
			name:   "Linux race timeout omitted",
			mutate: replaceCINth("go test -race -timeout=20m -count=1 ./...", "go test -race -count=1 ./...", 1),
		},
		{
			name:   "macOS race timeout omitted",
			mutate: replaceCINth("go test -race -timeout=20m -count=1 ./...", "go test -race -count=1 ./...", 2),
		},
		{
			name: "wrong SDK runner",
			mutate: replaceCIOnce(
				"  sdk-contract:\n    runs-on: ubuntu-latest\n",
				"  sdk-contract:\n    runs-on: macos-latest\n",
			),
		},
		{
			name: "wrong SDK timeout",
			mutate: replaceCIOnce(
				"  sdk-contract:\n    runs-on: ubuntu-latest\n    timeout-minutes: 12\n",
				"  sdk-contract:\n    runs-on: ubuntu-latest\n    timeout-minutes: 13\n",
			),
		},
		{
			name: "wrong setup-python action",
			mutate: replaceCIOnce(
				setupPythonAction,
				"actions/setup-python@v6",
			),
		},
		{
			name: "wrong setup-node action",
			mutate: replaceCIOnce(
				setupNodeAction,
				"actions/setup-node@v6",
			),
		},
		{name: "wrong Python runtime", mutate: replaceCIOnce(`python-version: "3.12"`, `python-version: "3.13"`)},
		{name: "wrong Node runtime", mutate: replaceCIOnce(`node-version: "24"`, `node-version: "22"`)},
		{
			name: "Python preflight loses environment isolation",
			mutate: replaceCIOnce(
				`/usr/bin/env -i "${RUNNER_TEMP}/sdk-python/bin/python" -I -c 'import openai'`,
				`"${RUNNER_TEMP}/sdk-python/bin/python" -I -c 'import openai'`,
			),
		},
		{
			name: "Python allocator preflight bypassed",
			mutate: replaceCIOnce(
				`PORT_OUTPUT="$(/usr/bin/env -i "${RUNNER_TEMP}/sdk-python/bin/python" -I -c "${PORT_ALLOCATOR_CODE}" 2>/dev/null)"`,
				`PORT_OUTPUT=1`,
			),
		},
		{
			name: "Node preflight loses environment isolation",
			mutate: replaceCIOnce(
				`/usr/bin/env -i "${RUNNER_TEMP}/sdk-node/node" --input-type=module`,
				`"${RUNNER_TEMP}/sdk-node/node" --input-type=module`,
			),
		},
		{
			name: "SDK build preflight uses host Go cache",
			mutate: replaceCIOnce(
				`GOCACHE="${SDK_BUILD_ROOT}/gocache" \`,
				`GOCACHE="$(go env GOCACHE)" \`,
			),
		},
		{
			name:   "gateway build preflight removed",
			mutate: replaceCIOnce("      - name: Preflight gateway build\n", "      - name: Skipped gateway build\n"),
		},
		{
			name:   "fake CLI build preflight removed",
			mutate: replaceCIOnce("      - name: Preflight fake CLI build\n", "      - name: Skipped fake CLI build\n"),
		},
		{
			name: "Python root below workspace",
			mutate: func(document string) string {
				return strings.ReplaceAll(document, "${RUNNER_TEMP}/sdk-python", "${GITHUB_WORKSPACE}/sdk-python")
			},
		},
		{
			name: "JavaScript root below workspace",
			mutate: func(document string) string {
				return strings.ReplaceAll(document, "${RUNNER_TEMP}/sdk-javascript", "${GITHUB_WORKSPACE}/sdk-javascript")
			},
		},
		{
			name: "Node root below workspace",
			mutate: func(document string) string {
				return strings.ReplaceAll(document, "${RUNNER_TEMP}/sdk-node", "${GITHUB_WORKSPACE}/sdk-node")
			},
		},
		{
			name:   "Python venv follows hosted toolcache target",
			mutate: replaceCIOnce(`python -m venv --copies`, `python -m venv`),
		},
		{
			name: "Node argument uses hosted toolcache target",
			mutate: replaceCIOnce(
				`            "${RUNNER_TEMP}/sdk-node/node" \`,
				`            "$(command -v node)" \`,
			),
		},
		{
			name: "Python root create mode weakened",
			mutate: replaceCIOnce(
				`install -d -m 0700 "${RUNNER_TEMP}/sdk-python"`,
				`install -d -m 0755 "${RUNNER_TEMP}/sdk-python"`,
			),
		},
		{
			name: "JavaScript root reassert mode weakened",
			mutate: replaceCIOnce(
				`chmod 0700 "${RUNNER_TEMP}/sdk-javascript"`,
				`chmod 0755 "${RUNNER_TEMP}/sdk-javascript"`,
			),
		},
		{
			name: "Python exact-mode check removed",
			mutate: replaceCIOnce(
				`          test "$(stat -c '%a' "${RUNNER_TEMP}/sdk-python")" = "700"`+"\n",
				"",
			),
		},
		{
			name: "JavaScript exact-mode check removed",
			mutate: replaceCIOnce(
				`          test "$(stat -c '%a' "${RUNNER_TEMP}/sdk-javascript")" = "700"`+"\n",
				"",
			),
		},
		{
			name: "Node exact-mode check removed",
			mutate: replaceCIOnce(
				`          test "$(stat -c '%a' "${RUNNER_TEMP}/sdk-node/node")" = "700"`+"\n",
				"",
			),
		},
		{name: "unlocked Python requirements", mutate: replaceCIOnce("requirements.lock", "requirements.txt")},
		{
			name:   "pip version-check flag removed",
			mutate: replaceCIOnce(" --disable-pip-version-check", ""),
		},
		{name: "pip noninteractive flag removed", mutate: replaceCIOnce(" --no-input", "")},
		{
			name: "extra JavaScript copy",
			mutate: replaceCIOnce(
				`          cp "${GITHUB_WORKSPACE}/examples/openai-sdk/javascript/package-lock.json" "${RUNNER_TEMP}/sdk-javascript/package-lock.json"`+"\n",
				`          cp "${GITHUB_WORKSPACE}/examples/openai-sdk/javascript/package-lock.json" "${RUNNER_TEMP}/sdk-javascript/package-lock.json"`+"\n"+
					`          cp "${GITHUB_WORKSPACE}/README.md" "${RUNNER_TEMP}/sdk-javascript/README.md"`+"\n",
			),
		},
		{name: "npm lifecycle scripts enabled", mutate: replaceCIOnce("npm ci --ignore-scripts", "npm ci")},
		{
			name: "wrong npm prefix",
			mutate: replaceCIOnce(
				`npm ci --ignore-scripts --prefix "${RUNNER_TEMP}/sdk-javascript"`,
				`npm ci --ignore-scripts --prefix "${RUNNER_TEMP}/sdk-python"`,
			),
		},
		{
			name: "SDK argument added",
			mutate: replaceCIOnce(
				`            "${RUNNER_TEMP}/sdk-javascript/main.mjs"`,
				`            "${RUNNER_TEMP}/sdk-javascript/main.mjs" "extra"`,
			),
		},
		{
			name:   "SDK argument removed",
			mutate: replaceCIOnce(`            "${RUNNER_TEMP}/sdk-node/node" \`+"\n", ""),
		},
		{
			name: "SDK arguments reordered",
			mutate: replaceCIOnce(
				"            \"${RUNNER_TEMP}/sdk-python/bin/python\" \\\n"+
					"            \"${RUNNER_TEMP}/sdk-node/node\" \\\n",
				"            \"${RUNNER_TEMP}/sdk-node/node\" \\\n"+
					"            \"${RUNNER_TEMP}/sdk-python/bin/python\" \\\n",
			),
		},
		{
			name: "SDK argument unquoted",
			mutate: replaceCIOnce(
				`            "${RUNNER_TEMP}/sdk-python/bin/python" \`,
				`            ${RUNNER_TEMP}/sdk-python/bin/python \`,
			),
		},
		{
			name: "action hidden after top-level comment boundary",
			mutate: func(document string) string {
				return strings.TrimRight(document, "\n") +
					"\n# parser boundary\n      - uses: attacker/action@deadbeef\n"
			},
		},
		{
			name: "job permissions hidden after top-level comment boundary",
			mutate: func(document string) string {
				return strings.TrimRight(document, "\n") +
					"\n# parser boundary\n    permissions: { contents: write }\n"
			},
		},
		{
			name: "document start marker after top-level comment boundary",
			mutate: func(document string) string {
				return strings.TrimRight(document, "\n") +
					"\n# parser boundary\n---\n"
			},
		},
		{
			name: "document end marker after top-level comment boundary",
			mutate: func(document string) string {
				return strings.TrimRight(document, "\n") +
					"\n# parser boundary\n...\n"
			},
		},
		{
			name: "anchor and merge hidden after top-level comment boundary",
			mutate: func(document string) string {
				return strings.TrimRight(document, "\n") +
					"\n# parser boundary\n    env: &hidden_env\n      ESCAPE: yes\n    <<: *hidden_env\n"
			},
		},
		{
			name: "flow-style job hidden after top-level comment boundary",
			mutate: func(document string) string {
				return strings.TrimRight(document, "\n") +
					"\n# parser boundary\n  shadow: { runs-on: ubuntu-latest, timeout-minutes: 1, steps: [{ uses: attacker/action@deadbeef }] }\n"
			},
		},
		{
			name: "action hidden after job-indented comment boundary",
			mutate: func(document string) string {
				return strings.TrimRight(document, "\n") +
					"\n    # parser boundary\n      - uses: attacker/action@deadbeef\n"
			},
		},
		{
			name: "trigger hidden after top-level comment boundary",
			mutate: replaceCIOnce(
				"  workflow_call:\n\npermissions:",
				"  workflow_call:\n# parser boundary\n  workflow_dispatch:\n\npermissions:",
			),
		},
		{
			name: "permission hidden after top-level comment boundary",
			mutate: replaceCIOnce(
				"permissions:\n  contents: read\n\njobs:",
				"permissions:\n  contents: read\n# parser boundary\n  actions: write # hidden\n\njobs:",
			),
		},
		{
			name: "strategy field hidden after job-indented comment boundary",
			mutate: replaceCIOnce(
				"          extension: .exe\n    steps:",
				"          extension: .exe\n    # parser boundary\n      max-parallel: 1\n    steps:",
			),
		},
		{
			name: "trigger after trigger-indented comment",
			mutate: replaceCIOnce(
				"  workflow_call:\n",
				"  workflow_call:\n  # parser boundary\n  workflow_dispatch:\n",
			),
		},
		{
			name: "permission after permission-indented comment",
			mutate: replaceCIOnce(
				"  contents: read\n",
				"  contents: read\n  # parser boundary\n  actions: write\n",
			),
		},
		{
			name: "job field after job-indented comment",
			mutate: func(document string) string {
				return strings.TrimRight(document, "\n") +
					"\n    # parser boundary\n    permissions: { contents: write }\n"
			},
		},
		{
			name: "action after list-indented comment",
			mutate: func(document string) string {
				return strings.TrimRight(document, "\n") +
					"\n      # parser boundary\n      - uses: attacker/action@deadbeef\n"
			},
		},
		{
			name: "step field after step-indented comment",
			mutate: replaceCIOnce(
				"      - uses: "+checkoutAction+"\n",
				"      - uses: "+checkoutAction+"\n        # parser boundary\n        continue-on-error: true\n",
			),
		},
		{
			name: "strategy field after blank line",
			mutate: replaceCIOnce(
				"          extension: .exe\n    steps:",
				"          extension: .exe\n\n      max-parallel: 1\n    steps:",
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := test.mutate(workflow)
			if mutated == workflow {
				t.Fatal("mutation did not change the workflow fixture")
			}
			if err := validateSDKCIWorkflowContract(mutated); err == nil {
				t.Fatal("CI SDK contract accepted the mutation")
			}
		})
	}
}

func validateSDKCIWorkflowContract(workflow string) error {
	topLevelFields, err := parseImmediateYAMLFields(workflow, 0)
	if err != nil {
		return fmt.Errorf("top-level workflow: %w", err)
	}
	wantTopLevelFields := map[string]string{
		"name": "CI", "on": "", "permissions": "", "jobs": "",
	}
	if !reflect.DeepEqual(topLevelFields, wantTopLevelFields) {
		return fmt.Errorf("top-level fields = %v, want exact fields %v", topLevelFields, wantTopLevelFields)
	}
	if got, want := topLevelYAMLBlockLines(workflow, "on:"), []string{"push:", "pull_request:", "workflow_call:"}; !reflect.DeepEqual(got, want) {
		return fmt.Errorf("top-level triggers = %q, want exactly %q without inputs or secrets", got, want)
	}
	if got := topLevelYAMLBlockLines(workflow, "permissions:"); !reflect.DeepEqual(got, []string{"contents: read"}) {
		return fmt.Errorf("top-level permissions = %q, want only contents: read", got)
	}

	jobs, err := parseYAMLJobBlocks(workflow)
	if err != nil {
		return err
	}
	contracts := expectedCIJobContracts()
	wantJobs := make(map[string]struct{}, len(contracts))
	for name := range contracts {
		wantJobs[name] = struct{}{}
	}
	gotJobs := make(map[string]struct{}, len(jobs))
	for name := range jobs {
		gotJobs[name] = struct{}{}
	}
	if !reflect.DeepEqual(gotJobs, wantJobs) {
		return fmt.Errorf("jobs = %v, want exact set %v", gotJobs, wantJobs)
	}

	wantActionsByJob := expectedCIJobActions()
	for _, name := range []string{"lint", "linux", "macos", "windows", "cross-build", "sdk-contract"} {
		job := jobs[name]
		contract := contracts[name]
		fields, parseErr := parseImmediateYAMLFields(job, 4)
		if parseErr != nil {
			return fmt.Errorf("job %s fields: %w", name, parseErr)
		}
		if !reflect.DeepEqual(fields, contract.fields) {
			return fmt.Errorf("job %s fields = %v, want exact fields %v", name, fields, contract.fields)
		}
		steps, parseErr := parseYAMLSteps(job)
		if parseErr != nil {
			return fmt.Errorf("job %s steps: %w", name, parseErr)
		}
		if name == "lint" {
			if parseErr = validateCIActionlintStep(workflow, job); parseErr != nil {
				return fmt.Errorf("job %s actionlint: %w", name, parseErr)
			}
			filtered := make([]string, 0, len(steps)-1)
			for _, step := range steps {
				if !strings.HasPrefix(step, "- name: Validate workflow syntax\n") {
					filtered = append(filtered, step)
				}
			}
			steps = filtered
		}
		if !reflect.DeepEqual(steps, contract.steps) {
			return fmt.Errorf("job %s steps differ from the closed execution contract", name)
		}
		actions, parseErr := parsedYAMLJobActions(job)
		if parseErr != nil {
			return fmt.Errorf("job %s actions: %w", name, parseErr)
		}
		if !reflect.DeepEqual(actions, wantActionsByJob[name]) {
			return fmt.Errorf("job %s actions = %q, want exact ordered actions %q", name, actions, wantActionsByJob[name])
		}
		if contract.strategy != nil {
			strategy, blockErr := parseIndentedYAMLBlock(job, "strategy", 4)
			if blockErr != nil {
				return fmt.Errorf("job %s strategy: %w", name, blockErr)
			}
			if !reflect.DeepEqual(strategy, contract.strategy) {
				return fmt.Errorf("job %s strategy differs from the exact matrix contract", name)
			}
		}
	}
	return nil
}

type ciWorkflowJobContract struct {
	fields   map[string]string
	steps    []string
	strategy []string
}

func expectedCIJobContracts() map[string]ciWorkflowJobContract {
	checkout := "- uses: " + checkoutAction
	setupGo := yamlContractLines(
		"- uses: "+setupGoAction,
		"  with:",
		"    go-version-file: .go-version",
		"    cache: true",
	)
	unixBuild := yamlContractLines(
		"- name: Build",
		"  env:",
		"    CGO_ENABLED: 0",
		`  run: go build -trimpath -o "${RUNNER_TEMP}/ai-cli-gateway" ./cmd/ai-cli-gateway`,
	)

	return map[string]ciWorkflowJobContract{
		"lint": {
			fields: map[string]string{"runs-on": "ubuntu-latest", "timeout-minutes": "10", "steps": ""},
			steps: []string{
				checkout,
				setupGo,
				yamlContractLines(
					"- name: Check formatting",
					"  shell: bash",
					"  run: |",
					`    unformatted_files="$(gofmt -l .)"`,
					`    test -z "$unformatted_files"`,
				),
				yamlContractLines("- name: Vet", "  run: go vet ./..."),
				yamlContractLines(
					"- uses: "+golangciAction,
					"  with:",
					"    version: v2.12.2",
				),
			},
		},
		"linux": {
			fields: map[string]string{"runs-on": "ubuntu-latest", "timeout-minutes": "40", "steps": ""},
			steps: []string{
				checkout,
				setupGo,
				yamlContractLines("- name: Verify modules", "  run: go mod verify"),
				yamlContractLines("- name: Unit tests", "  run: go test -count=1 ./..."),
				yamlContractLines("- name: Race tests", "  run: go test -race -timeout=20m -count=1 ./..."),
				yamlContractLines("- name: Fake CLI integration tests", "  run: go test -tags=integration -count=1 ./..."),
				yamlContractLines("- name: Trimmed-path tests", "  run: go test -trimpath -count=1 ./..."),
				yamlContractLines("- name: Trimmed-path helper tests", "  run: GOFLAGS=-trimpath go test -count=1 ./internal/testutil ./internal/testcli"),
				yamlContractLines("- name: Compile opt-in live contracts without running them", "  run: go test -tags=live -run '^$' ./internal/provider/..."),
				unixBuild,
			},
		},
		"macos": {
			fields: map[string]string{"runs-on": "macos-latest", "timeout-minutes": "45", "steps": ""},
			steps: []string{
				checkout,
				setupGo,
				yamlContractLines("- name: Unit tests", "  run: go test -count=1 ./..."),
				yamlContractLines("- name: Race tests", "  run: go test -race -timeout=20m -count=1 ./..."),
				yamlContractLines("- name: Fake CLI integration tests", "  run: go test -tags=integration -count=1 ./..."),
				yamlContractLines("- name: Trimmed-path tests", "  run: go test -trimpath -count=1 ./..."),
				unixBuild,
			},
		},
		"windows": {
			fields: map[string]string{"runs-on": "windows-latest", "timeout-minutes": "45", "steps": ""},
			steps: []string{
				checkout,
				setupGo,
				yamlContractLines("- name: Unit tests", "  run: go test -count=1 ./..."),
				yamlContractLines(
					"- name: Native Job Object, ACL, reparse, cancellation, and cleanup tests",
					"  run: go test -tags=integration -count=1 -v ./...",
				),
				yamlContractLines("- name: Trimmed-path tests", "  run: go test -trimpath -count=1 ./..."),
				yamlContractLines(
					"- name: Build",
					"  env:",
					"    CGO_ENABLED: 0",
					`  run: go build -trimpath -o "$env:RUNNER_TEMP/ai-cli-gateway.exe" ./cmd/ai-cli-gateway`,
				),
			},
		},
		"cross-build": {
			fields: map[string]string{
				"runs-on": "ubuntu-latest", "timeout-minutes": "15", "strategy": "", "steps": "",
			},
			strategy: []string{
				"  fail-fast: false",
				"  matrix:",
				"    include:",
				"      - goos: linux",
				"        goarch: amd64",
				`        extension: ""`,
				"      - goos: linux",
				"        goarch: arm64",
				`        extension: ""`,
				"      - goos: darwin",
				"        goarch: amd64",
				`        extension: ""`,
				"      - goos: darwin",
				"        goarch: arm64",
				`        extension: ""`,
				"      - goos: windows",
				"        goarch: amd64",
				"        extension: .exe",
			},
			steps: []string{
				checkout,
				setupGo,
				yamlContractLines(
					"- name: Cross-build",
					"  env:",
					"    CGO_ENABLED: 0",
					"    GOOS: ${{ matrix.goos }}",
					"    GOARCH: ${{ matrix.goarch }}",
					"  run: >-",
					"    go build -trimpath",
					`    -o "${RUNNER_TEMP}/ai-cli-gateway-${GOOS}-${GOARCH}${{ matrix.extension }}"`,
					"    ./cmd/ai-cli-gateway",
				),
			},
		},
		"sdk-contract": {
			fields: map[string]string{"runs-on": "ubuntu-latest", "timeout-minutes": "12", "steps": ""},
			steps: []string{
				checkout,
				setupGo,
				yamlContractLines(
					"- uses: "+setupPythonAction,
					"  with:",
					`    python-version: "3.12"`,
				),
				yamlContractLines(
					"- uses: "+setupNodeAction,
					"  with:",
					`    node-version: "24"`,
				),
				yamlContractLines(
					"- name: Prepare official SDK runtimes",
					"  shell: bash",
					"  run: |",
					"    set -eu",
					"    umask 077",
					`    install -d -m 0700 "${RUNNER_TEMP}/sdk-node"`,
					`    install -m 0700 "$(command -v node)" "${RUNNER_TEMP}/sdk-node/node"`,
					`    test "$(stat -c '%a' "${RUNNER_TEMP}/sdk-node")" = "700"`,
					`    test "$(stat -c '%a' "${RUNNER_TEMP}/sdk-node/node")" = "700"`,
					`    install -d -m 0700 "${RUNNER_TEMP}/sdk-python"`,
					`    python -m venv --copies "${RUNNER_TEMP}/sdk-python"`,
					`    chmod 0700 "${RUNNER_TEMP}/sdk-python"`,
					`    test "$(stat -c '%a' "${RUNNER_TEMP}/sdk-python")" = "700"`,
					`    "${RUNNER_TEMP}/sdk-python/bin/python" -m pip install --disable-pip-version-check --no-input --requirement "${GITHUB_WORKSPACE}/examples/openai-sdk/python/requirements.lock"`,
					`    install -d -m 0700 "${RUNNER_TEMP}/sdk-javascript"`,
					`    cp "${GITHUB_WORKSPACE}/examples/openai-sdk/javascript/main.mjs" "${RUNNER_TEMP}/sdk-javascript/main.mjs"`,
					`    cp "${GITHUB_WORKSPACE}/examples/openai-sdk/javascript/package.json" "${RUNNER_TEMP}/sdk-javascript/package.json"`,
					`    cp "${GITHUB_WORKSPACE}/examples/openai-sdk/javascript/package-lock.json" "${RUNNER_TEMP}/sdk-javascript/package-lock.json"`,
					`    chmod 0700 "${RUNNER_TEMP}/sdk-javascript"`,
					`    test "$(stat -c '%a' "${RUNNER_TEMP}/sdk-javascript")" = "700"`,
					`    npm ci --ignore-scripts --prefix "${RUNNER_TEMP}/sdk-javascript"`,
				),
				yamlContractLines(
					"- name: Preflight isolated Python SDK runtime",
					"  shell: bash",
					"  run: |",
					"    set -eu",
					`    PORT_ALLOCATOR_CODE="import socket`,
					`    s=socket.socket(socket.AF_INET,socket.SOCK_STREAM)`,
					`    s.bind(('127.0.0.1',0))`,
					`    print(s.getsockname()[1])`,
					`    s.close()`,
					`    "`,
					`    /usr/bin/env -i "${RUNNER_TEMP}/sdk-python/bin/python" -I -c 'import openai' >/dev/null 2>&1`,
					`    PORT_OUTPUT="$(/usr/bin/env -i "${RUNNER_TEMP}/sdk-python/bin/python" -I -c "${PORT_ALLOCATOR_CODE}" 2>/dev/null)"`,
					`    case "${PORT_OUTPUT}" in ''|*[!0-9]*) exit 1 ;; esac`,
					`    test "${PORT_OUTPUT}" -ge 1`,
					`    test "${PORT_OUTPUT}" -le 65535`,
				),
				yamlContractLines(
					"- name: Preflight isolated Node SDK runtime",
					"  shell: bash",
					"  run: |",
					"    set -eu",
					`    cd "${RUNNER_TEMP}/sdk-javascript"`,
					`    /usr/bin/env -i "${RUNNER_TEMP}/sdk-node/node" --input-type=module --eval 'await import("openai")' >/dev/null 2>&1`,
				),
				sdkBuildPreflightContract("SDK contract command", "sdk-build-contract", "sdk-contract", "./internal/sdkcontract/cmd/sdk-contract"),
				sdkBuildPreflightContract("gateway", "sdk-build-gateway", "ai-cli-gateway", "./cmd/ai-cli-gateway"),
				sdkBuildPreflightContract("fake CLI", "sdk-build-fake", "fake-codex-cli", "./internal/testcli/cmd/fake-codex-cli"),
				yamlContractLines(
					"- name: Verify official SDK compatibility",
					"  shell: bash",
					"  run: |",
					"    set -eu",
					`    scripts/sdk-contract.sh \`,
					`      "${RUNNER_TEMP}/sdk-python/bin/python" \`,
					`      "${RUNNER_TEMP}/sdk-node/node" \`,
					`      "${RUNNER_TEMP}/sdk-javascript/main.mjs"`,
				),
			},
		},
	}
}

func expectedCIJobActions() map[string][]string {
	return map[string][]string{
		"lint": {
			checkoutAction, setupGoAction, golangciAction,
		},
		"linux":       {checkoutAction, setupGoAction},
		"macos":       {checkoutAction, setupGoAction},
		"windows":     {checkoutAction, setupGoAction},
		"cross-build": {checkoutAction, setupGoAction},
		"sdk-contract": {
			checkoutAction,
			setupGoAction,
			setupPythonAction,
			setupNodeAction,
		},
	}
}

func yamlContractLines(lines ...string) string {
	return strings.Join(lines, "\n")
}

func sdkBuildPreflightContract(name, root, binary, packagePath string) string {
	return yamlContractLines(
		"- name: Preflight "+name+" build",
		"  shell: bash",
		"  run: |",
		"    set -eu",
		"    umask 077",
		`    SDK_BUILD_ROOT="${RUNNER_TEMP}/`+root+`"`,
		`    test ! -e "${SDK_BUILD_ROOT}"`,
		`    for directory in bin gocache gomodcache gopath home tmp; do`,
		`      install -d -m 0700 "${SDK_BUILD_ROOT}/${directory}"`,
		`    done`,
		`    HOME="${SDK_BUILD_ROOT}/home" \`,
		`    GOPATH="${SDK_BUILD_ROOT}/gopath" \`,
		`    GOMODCACHE="${SDK_BUILD_ROOT}/gomodcache" \`,
		`    GOCACHE="${SDK_BUILD_ROOT}/gocache" \`,
		`    TMPDIR="${SDK_BUILD_ROOT}/tmp" \`,
		`    GOENV=off GOFLAGS= GOWORK=off GOTOOLCHAIN=local GOTELEMETRY=off CGO_ENABLED=0 \`,
		`    GOPROXY=https://proxy.golang.org GOSUMDB=sum.golang.org GOPRIVATE= GONOPROXY= GONOSUMDB= GOINSECURE= \`,
		`    go build -trimpath -o "${SDK_BUILD_ROOT}/bin/`+binary+`" `+packagePath+` >/dev/null 2>&1`,
	)
}

func replaceCIOnce(old, replacement string) func(string) string {
	return func(document string) string {
		return strings.Replace(document, old, replacement, 1)
	}
}

func replaceCINth(old, replacement string, occurrence int) func(string) string {
	return func(document string) string {
		if occurrence < 1 {
			return document
		}
		offset := 0
		for index := 1; index <= occurrence; index++ {
			relative := strings.Index(document[offset:], old)
			if relative < 0 {
				return document
			}
			offset += relative
			if index == occurrence {
				return document[:offset] + replacement + document[offset+len(old):]
			}
			offset += len(old)
		}
		return document
	}
}

func parseYAMLJobBlocks(workflow string) (map[string]string, error) {
	lines := strings.Split(strings.ReplaceAll(workflow, "\r\n", "\n"), "\n")
	topLevelJobs := 0
	for _, line := range lines {
		if leadingSpaces(line) == 0 && strings.HasPrefix(line, "jobs:") {
			topLevelJobs++
		}
	}
	if topLevelJobs != 1 {
		return nil, fmt.Errorf("top-level jobs mappings = %d, want exactly one", topLevelJobs)
	}
	jobsStart := -1
	for index, line := range lines {
		if line == "jobs:" {
			jobsStart = index + 1
			break
		}
	}
	if jobsStart < 0 {
		return nil, errors.New("no top-level jobs mapping")
	}
	jobHeader := regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)
	blocks := make(map[string]string)
	current := ""
	currentLines := make([]string, 0)
	flush := func() error {
		if current == "" {
			return nil
		}
		if _, exists := blocks[current]; exists {
			return fmt.Errorf("duplicate job %q", current)
		}
		blocks[current] = strings.Join(currentLines, "\n")
		return nil
	}
	for _, line := range lines[jobsStart:] {
		trimmed := strings.TrimSpace(line)
		indentation := leadingSpaces(line)
		if isYAMLCommentOnly(line) && indentation <= 2 {
			continue
		}
		if trimmed != "" && indentation == 0 {
			return nil, fmt.Errorf("unexpected top-level content after jobs mapping %q", line)
		}
		if trimmed != "" && indentation == 2 {
			match := jobHeader.FindStringSubmatch(line)
			if match == nil {
				return nil, fmt.Errorf("unsupported job entry %q", line)
			}
			if err := flush(); err != nil {
				return nil, err
			}
			current = match[1]
			currentLines = []string{line}
			continue
		}
		if trimmed != "" && current == "" {
			return nil, fmt.Errorf("job content before first job %q", line)
		}
		if current != "" {
			currentLines = append(currentLines, line)
		}
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(blocks) == 0 {
		return nil, errors.New("no jobs")
	}
	return blocks, nil
}

func parseImmediateYAMLFields(block string, indentation int) (map[string]string, error) {
	fields := make(map[string]string)
	prefix := strings.Repeat(" ", indentation)
	fieldPattern := regexp.MustCompile(`^` + regexp.QuoteMeta(prefix) + `([A-Za-z0-9_-]+):(?:\s*(.*))?$`)
	for _, line := range strings.Split(block, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || leadingSpaces(line) != indentation {
			continue
		}
		match := fieldPattern.FindStringSubmatch(line)
		if match == nil {
			return nil, fmt.Errorf("invalid field line %q", line)
		}
		if _, exists := fields[match[1]]; exists {
			return nil, fmt.Errorf("duplicate field %q", match[1])
		}
		fields[match[1]] = strings.TrimSpace(match[2])
	}
	return fields, nil
}

func parseYAMLSteps(jobBlock string) ([]string, error) {
	lines := strings.Split(strings.ReplaceAll(jobBlock, "\r\n", "\n"), "\n")
	stepsStart := -1
	for index, line := range lines {
		if line == "    steps:" {
			if stepsStart >= 0 {
				return nil, errors.New("duplicate steps mapping")
			}
			stepsStart = index + 1
		}
	}
	if stepsStart < 0 {
		return nil, errors.New("missing steps mapping")
	}

	steps := make([]string, 0)
	current := make([]string, 0)
	flush := func() error {
		if len(current) > 0 {
			step := strings.Join(current, "\n")
			if err := validateYAMLStepFields(step); err != nil {
				return err
			}
			steps = append(steps, step)
		}
		return nil
	}
	for _, line := range lines[stepsStart:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		indentation := leadingSpaces(line)
		if isYAMLCommentOnly(line) && indentation <= 8 {
			continue
		}
		if indentation <= 4 {
			break
		}
		if indentation == 6 {
			if !strings.HasPrefix(line, "      - ") {
				return nil, fmt.Errorf("invalid step boundary %q", line)
			}
			if err := flush(); err != nil {
				return nil, err
			}
			current = []string{strings.TrimPrefix(line, "      ")}
			continue
		}
		if len(current) == 0 {
			return nil, fmt.Errorf("step content before first list item %q", line)
		}
		if indentation < 8 {
			return nil, fmt.Errorf("invalid step indentation %q", line)
		}
		current = append(current, strings.TrimPrefix(line, "      "))
	}
	if err := flush(); err != nil {
		return nil, err
	}
	if len(steps) == 0 {
		return nil, errors.New("empty steps list")
	}
	return steps, nil
}

func validateYAMLStepFields(step string) error {
	lines := strings.Split(step, "\n")
	if len(lines) == 0 || !strings.HasPrefix(lines[0], "- ") {
		return errors.New("step does not start with a block-style list item")
	}
	fieldPattern := regexp.MustCompile(`^([A-Za-z0-9_-]+):(?:\s*(.*))?$`)
	allowedFields := map[string]struct{}{
		"name": {}, "uses": {}, "with": {}, "env": {}, "shell": {}, "run": {},
	}
	fields := make(map[string]struct{})
	addField := func(fieldLine string) error {
		match := fieldPattern.FindStringSubmatch(fieldLine)
		if match == nil {
			return fmt.Errorf("unsupported flow-style or malformed step field %q", fieldLine)
		}
		name := match[1]
		if _, allowed := allowedFields[name]; !allowed {
			return fmt.Errorf("unsupported step field %q", name)
		}
		if _, duplicate := fields[name]; duplicate {
			return fmt.Errorf("duplicate step field %q", name)
		}
		fields[name] = struct{}{}
		return nil
	}
	if err := addField(strings.TrimPrefix(lines[0], "- ")); err != nil {
		return err
	}
	for _, line := range lines[1:] {
		if leadingSpaces(line) != 2 {
			continue
		}
		if isYAMLCommentOnly(line) {
			continue
		}
		if err := addField(strings.TrimPrefix(line, "  ")); err != nil {
			return err
		}
	}
	_, hasUses := fields["uses"]
	_, hasRun := fields["run"]
	if hasUses == hasRun {
		return errors.New("step must contain exactly one of uses or run")
	}
	return nil
}

func parsedYAMLJobActions(jobBlock string) ([]string, error) {
	steps, err := parseYAMLSteps(jobBlock)
	if err != nil {
		return nil, err
	}
	actions := make([]string, 0)
	for _, step := range steps {
		if action := parsedStepAction(step); action != "" {
			actions = append(actions, action)
		}
	}
	return actions, nil
}

func parseIndentedYAMLBlock(document, key string, indentation int) ([]string, error) {
	lines := strings.Split(strings.ReplaceAll(document, "\r\n", "\n"), "\n")
	header := strings.Repeat(" ", indentation) + key + ":"
	start := -1
	for index, line := range lines {
		if line != header {
			continue
		}
		if start >= 0 {
			return nil, fmt.Errorf("duplicate %s block", key)
		}
		start = index + 1
	}
	if start < 0 {
		return nil, fmt.Errorf("missing %s block", key)
	}
	prefix := strings.Repeat(" ", indentation)
	result := make([]string, 0)
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		lineIndentation := leadingSpaces(line)
		if isYAMLCommentOnly(line) && lineIndentation <= indentation {
			continue
		}
		if lineIndentation <= indentation {
			break
		}
		result = append(result, strings.TrimPrefix(line, prefix))
	}
	if len(result) == 0 {
		return nil, fmt.Errorf("empty %s block", key)
	}
	return result, nil
}

func parsedStepAction(step string) string {
	for index, line := range strings.Split(step, "\n") {
		if index == 0 && strings.HasPrefix(line, "- uses: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "- uses: "))
		}
		if strings.HasPrefix(line, "  uses: ") {
			return strings.TrimSpace(strings.TrimPrefix(line, "  uses: "))
		}
	}
	return ""
}

func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

func isYAMLCommentOnly(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(line), "#")
}

func extractYAMLJobBlocks(t *testing.T, workflow string) map[string]string {
	t.Helper()
	lines := strings.Split(strings.ReplaceAll(workflow, "\r\n", "\n"), "\n")
	jobsStart := -1
	for index, line := range lines {
		if strings.TrimSpace(line) == "jobs:" && len(line)-len(strings.TrimLeft(line, " ")) == 0 {
			jobsStart = index + 1
			break
		}
	}
	if jobsStart < 0 {
		t.Fatal("CI workflow has no top-level jobs mapping")
	}
	jobHeader := regexp.MustCompile(`^  ([A-Za-z0-9_-]+):\s*$`)
	blocks := make(map[string]string)
	current := ""
	currentLines := make([]string, 0)
	flush := func() {
		if current != "" {
			blocks[current] = strings.Join(currentLines, "\n")
		}
	}
	for _, line := range lines[jobsStart:] {
		trimmed := strings.TrimSpace(line)
		indentation := leadingSpaces(line)
		if isYAMLCommentOnly(line) && indentation <= 2 {
			continue
		}
		if trimmed != "" && indentation == 0 {
			break
		}
		if match := jobHeader.FindStringSubmatch(line); match != nil {
			flush()
			current = match[1]
			currentLines = []string{line}
			continue
		}
		if current != "" {
			currentLines = append(currentLines, line)
		}
	}
	flush()
	return blocks
}

func topLevelYAMLBlockLines(document, header string) []string {
	lines := strings.Split(strings.ReplaceAll(document, "\r\n", "\n"), "\n")
	start := -1
	for index, line := range lines {
		if line == header {
			start = index + 1
			break
		}
	}
	if start < 0 {
		return nil
	}
	result := make([]string, 0)
	for _, line := range lines[start:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || isYAMLCommentOnly(line) {
			continue
		}
		if leadingSpaces(line) == 0 {
			break
		}
		result = append(result, trimmed)
	}
	return result
}

func requireYAMLMatrixTuple(t *testing.T, block, goos, goarch string) {
	t.Helper()
	needle := "goos: " + goos
	for search := 0; search < len(block); {
		relative := strings.Index(block[search:], needle)
		if relative < 0 {
			break
		}
		start := search + relative
		end := start + 160
		if end > len(block) {
			end = len(block)
		}
		if strings.Contains(block[start:end], "goarch: "+goarch) {
			return
		}
		search = start + len(needle)
	}
	t.Fatalf("cross-build matrix is missing %s/%s", goos, goarch)
}

type releaseWorkflowStep struct {
	Name  string
	ID    string
	Uses  string
	Shell string
	Run   string
	With  map[string]string
	Env   map[string]string
}

type releaseWorkflowJob struct {
	Uses        string
	RunsOn      string
	Timeout     string
	Needs       []string
	Permissions map[string]string
	Outputs     map[string]string
	Steps       []releaseWorkflowStep
}

type releaseWorkflowDocument struct {
	Root *yaml.Node
	Jobs map[string]releaseWorkflowJob
}

func TestReleaseWorkflowContract(t *testing.T) {
	document := readRepositoryFile(t, ".github/workflows/release.yml")
	workflow, err := parseClosedReleaseWorkflow(document)
	if err != nil {
		t.Fatalf("parse closed release workflow: %v", err)
	}
	if err := validateReleaseWorkflowContract(workflow); err != nil {
		t.Fatalf("release workflow contract: %v", err)
	}
}

func TestReleaseWorkflowContractRejectsMutations(t *testing.T) {
	document := string(readRepositoryFile(t, ".github/workflows/release.yml"))
	tests := []struct {
		name   string
		mutate func(string) string
	}{
		{name: "duplicate mapping key", mutate: replaceReleaseOnce("name: Release\n", "name: Release\nname: Shadow\n")},
		{name: "non-string mapping key", mutate: replaceReleaseOnce("name: Release\n", "1: Release\n")},
		{name: "anchor", mutate: replaceReleaseOnce("name: Release\n", "name: &release Release\n")},
		{name: "alias", mutate: replaceReleaseOnce("name: Release\n", "name: &release Release\nshadow: *release\n")},
		{name: "merge key", mutate: replaceReleaseOnce("concurrency:\n", "defaults: &defaults\n  cancel-in-progress: false\nconcurrency:\n  <<: *defaults\n")},
		{name: "explicit standard tag", mutate: replaceReleaseOnce("name: Release\n", "name: !!str Release\n")},
		{name: "explicit custom tag", mutate: replaceReleaseOnce("name: Release\n", "name: !custom Release\n")},
		{name: "flow map", mutate: replaceReleaseOnce("concurrency:\n  group: release-${{ github.repository }}-${{ github.ref_name }}\n  cancel-in-progress: false\n", "concurrency: {group: release-${{ github.repository }}-${{ github.ref_name }}, cancel-in-progress: false}\n")},
		{name: "flow list", mutate: replaceReleaseOnce("    needs:\n      - package\n      - asset-verification\n", "    needs: [package, asset-verification]\n")},
		{name: "multiple documents", mutate: func(value string) string { return value + "\n---\nname: shadow\n" }},
		{name: "unknown top field", mutate: replaceReleaseOnce("name: Release\n", "name: Release\nunexpected: true\n")},
		{name: "unknown job field", mutate: replaceReleaseOnce("  package:\n    needs: verify\n", "  package:\n    needs: verify\n    if: always()\n")},
		{name: "step if", mutate: replaceReleaseOnce("      - name: Validate release metadata\n", "      - name: Validate release metadata\n        if: always()\n")},
		{name: "step continue on error", mutate: replaceReleaseOnce("      - name: Validate release metadata\n", "      - name: Validate release metadata\n        continue-on-error: true\n")},
		{name: "unexpected checkout input", mutate: replaceReleaseOnce("          fetch-depth: 0\n", "          fetch-depth: 0\n          sparse-checkout: .\n")},
		{name: "unexpected metadata env", mutate: replaceReleaseOnce("          EVENT_SHA: ${{ github.sha }}\n", "          EVENT_SHA: ${{ github.sha }}\n          ATTACKER: value\n")},
		{name: "Syft install missing version ldflag", mutate: replaceReleaseOnce("run_clean_go install -ldflags '-X main.version=1.50.0' github.com/anchore/syft/cmd/syft@v1.50.0", "run_clean_go install github.com/anchore/syft/cmd/syft@v1.50.0")},
		{name: "Syft install moving module", mutate: replaceReleaseOnce("github.com/anchore/syft/cmd/syft@v1.50.0", "github.com/anchore/syft/cmd/syft@latest")},
		{name: "Syft Go missing env isolation", mutate: replaceReleaseOnce(`            "${ENV_BIN}" -i \`, `            "${ENV_BIN}" \`)},
		{name: "Syft Go receives PATH", mutate: replaceReleaseOnce(`              HOME="${tools_root}/home" XDG_CONFIG_HOME="${tools_root}/xdg" \`, "              PATH=/usr/bin \\\n"+`              HOME="${tools_root}/home" XDG_CONFIG_HOME="${tools_root}/xdg" \`)},
		{name: "Syft Go uses strict generated-binary policy", mutate: replaceReleaseOnce(`GO_IDENTITY="$(validate_build_tool "${GO_BIN}")"`, `GO_IDENTITY="$(validate_binary "${GO_BIN}")"`)},
		{name: "Syft Go special-bit guard removed", mutate: replaceReleaseOnce(`            (( (permissions & 07000) == 0 )) || return 1`+"\n", "")},
		{name: "Syft Go ancestor authority added", mutate: replaceShellFunctionOnce("validate_build_tool", `(( (permissions & 07000) == 0 )) || return 1`, "(( (permissions & 07000) == 0 )) || return 1\n            validate_authority \"${binary%/*}\" || return 1")},
		{name: "Syft authority early success", mutate: replaceShellFunctionOnce("validate_authority", "validate_authority() {", "validate_authority() {\n            return 0")},
		{name: "Syft authority directory failure masked", mutate: replaceShellFunctionOnce("validate_authority", `test -d "${current}" || return 1`, `test -d "${current}"`)},
		{name: "Syft authority mode conversion removed", mutate: replaceShellFunctionOnce("validate_authority", `permissions=$((8#${mode}))`, `permissions=0`)},
		{name: "Syft resolver command failure masked", mutate: replaceShellFunctionOnce("resolve_binary", `candidate="$(command -v -- "$1")" || return 1`, `candidate="$(command -v -- "$1")"`)},
		{name: "Syft resolver early success", mutate: replaceShellFunctionOnce("resolve_binary", "resolve_binary() {", "resolve_binary() {\n            return 0")},
		{name: "Syft resolver identity output removed", mutate: replaceShellFunctionOnce("resolve_binary", `printf '%s/%s\n' "${directory}" "${candidate##*/}"`, `: "${directory}/${candidate##*/}"`)},
		{name: "Syft Go symlink failure masked", mutate: replaceShellFunctionOnce("validate_build_tool", `test ! -L "${binary}" || return 1`, `test ! -L "${binary}"`)},
		{name: "Syft Go early success", mutate: replaceShellFunctionOnce("validate_build_tool", "validate_build_tool() {", "validate_build_tool() {\n            return 0")},
		{name: "Syft Go owner failure masked", mutate: replaceShellFunctionOnce("validate_build_tool", `owner="$(stat -c '%u' -- "${binary}")" || return 1`, `owner="$(stat -c '%u' -- "${binary}")"`)},
		{name: "Syft Go mode conversion removed", mutate: replaceShellFunctionOnce("validate_build_tool", `permissions=$((8#${mode}))`, `permissions=0`)},
		{name: "Syft Go identity failure masked", mutate: replaceShellFunctionOnce("validate_build_tool", `identity="$(stat -c '%d:%i' -- "${binary}")" || return 1`, `identity="$(stat -c '%d:%i' -- "${binary}")"`)},
		{name: "Syft Go identity output removed", mutate: replaceShellFunctionOnce("validate_build_tool", `printf '%s\n' "${identity}"`, `: "${identity}"`)},
		{name: "Syft strict executable failure masked", mutate: replaceShellFunctionOnce("validate_binary", `test -x "${binary}" || return 1`, `test -x "${binary}"`)},
		{name: "Syft strict early success", mutate: replaceShellFunctionOnce("validate_binary", "validate_binary() {", "validate_binary() {\n            return 0")},
		{name: "Syft strict mode conversion removed", mutate: replaceShellFunctionOnce("validate_binary", `permissions=$((8#${mode}))`, `permissions=0`)},
		{name: "Syft strict identity failure masked", mutate: replaceShellFunctionOnce("validate_binary", `identity="$(stat -c '%d:%i' -- "${binary}")" || return 1`, `identity="$(stat -c '%d:%i' -- "${binary}")"`)},
		{name: "Syft strict identity output removed", mutate: replaceShellFunctionOnce("validate_binary", `printf '%s\n' "${identity}"`, `: "${identity}"`)},
		{name: "Syft alternate strict function redefinition", mutate: insertAfterShellFunction("validate_binary", "          function validate_binary { return 0; }\n")},
		{name: "Syft strict mode failure masked", mutate: replaceReleaseOnce(`            (( (permissions & 07022) == 0 )) || return 1`, `            (( (permissions & 07022) == 0 ))`)},
		{name: "Syft strict authority failure masked", mutate: replaceReleaseOnce(`            validate_authority "${binary%/*}" || return 1`, `            validate_authority "${binary%/*}"`)},
		{name: "Syft runtime relative binary", mutate: replaceReleaseOnce(`              SYFT_CHECK_FOR_APP_UPDATE=false "${SYFT_BIN}" "$@"`, `              SYFT_CHECK_FOR_APP_UPDATE=false syft "$@"`)},
		{name: "Syft update check enabled", mutate: replaceReleaseOnce("SYFT_CHECK_FOR_APP_UPDATE=false", "SYFT_CHECK_FOR_APP_UPDATE=true")},
		{name: "Syft compliance altered", mutate: replaceReleaseOnce("SYFT_COMPLIANCE_MISSING_NAME=drop", "SYFT_COMPLIANCE_MISSING_NAME=keep")},
		{name: "Syft version field guard removed", mutate: replaceReleaseOnce(`          test "$(grep -Ec '^Version:' <<<"${syft_version}")" = 1`+"\n", "")},
		{name: "Syft version field guard narrowed", mutate: replaceReleaseOnce(`grep -Ec '^Version:'`, `grep -Ec '^Version:[[:blank:]]+1[.]50[.]0$'`)},
		{name: "Syft exact version guard removed", mutate: replaceReleaseOnce(`          test "$(grep -Ec '^Version:[[:blank:]]+1[.]50[.]0$' <<<"${syft_version}")" = 1`+"\n", "")},
		{name: "Syft version accepts trailing data", mutate: replaceReleaseOnce(`^Version:[[:blank:]]+1[.]50[.]0$`, `^Version:[[:blank:]]+1[.]50[.]0`)},
		{name: "Syft second config channel removed", mutate: replaceReleaseOnce(`            -c "${tools_root}/config/syft.yaml" -o`, `            -o`)},
		{
			name: "Syft pinned install hidden in dead string",
			mutate: replaceReleaseOnce(
				"          run_clean_go install -ldflags '-X main.version=1.50.0' github.com/anchore/syft/cmd/syft@v1.50.0\n",
				"          : \"run_clean_go install -ldflags '-X main.version=1.50.0' github.com/anchore/syft/cmd/syft@v1.50.0\"\n          run_clean_go install attacker.invalid/syft@v0\n",
			),
		},
		{
			name: "Syft scan target hidden in dead string",
			mutate: replaceReleaseOnce(
				"            \"${SYFT_BIN}\" scan \"dir:${RUNNER_TEMP}/release-staging\" \\\n            -c \"${tools_root}/config/syft.yaml\" -o \"spdx-json=${RUNNER_TEMP}/raw-syft.spdx.json\"\n",
				"            : '\"${SYFT_BIN}\" scan \"dir:${RUNNER_TEMP}/release-staging\" -c \"${tools_root}/config/syft.yaml\" -o \"spdx-json=${RUNNER_TEMP}/raw-syft.spdx.json\"'\n          \"${SYFT_BIN}\" scan /dev/null\n",
			),
		},
		{name: "Syft root identity revalidation removed", mutate: replaceReleaseOnce("            test \"$(stat -c '%d:%i' -- \"${tools_root}\")\" = \"${TOOLS_ROOT_IDENTITY}\"\n", "")},
		{name: "Syft config identity revalidation removed", mutate: replaceReleaseOnce("            test \"$(stat -c '%d:%i' -- \"${tools_root}/config/syft.yaml\")\" = \"${SYFT_CONFIG_IDENTITY}\"\n", "")},
		{name: "Syft runtime root validation removed", mutate: replaceReleaseOnce("          revalidate_syft() {\n            validate_roots\n", "          revalidate_syft() {\n")},
		{name: "Syft checksum accepts bare prefix", mutate: replaceReleaseOnce("h1:[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$", "h1:")},
		{name: "Syft checksum accepts noncanonical padding bits", mutate: replaceReleaseOnce("[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=", "[A-Za-z0-9+/]{43}=")},
		{name: "Syft checksum canonical set narrowed", mutate: replaceReleaseOnce("[AEIMQUYcgkosw048]", "[AQgw]")},
		{name: "Syft post-scan revalidation removed", mutate: replaceReleaseOnce("            -c \"${tools_root}/config/syft.yaml\" -o \"spdx-json=${RUNNER_TEMP}/raw-syft.spdx.json\"\n          revalidate_syft\n", "            -c \"${tools_root}/config/syft.yaml\" -o \"spdx-json=${RUNNER_TEMP}/raw-syft.spdx.json\"\n")},
		{name: "moving checkout ref", mutate: replaceReleaseOnce(checkoutAction, "actions/checkout@v7")},
		{name: "unpeeled golangci tag object", mutate: replaceReleaseOnce(attestAction, "actions/attest@d583c34f0599d37dbac4a198b9c83201be380893")},
		{name: "prior attest commit", mutate: replaceReleaseOnce(attestAction, "actions/attest@508db95dd578ae2727ebd6217d5ba78e4fbda05d")},
		{name: "wrong package permission", mutate: replaceReleaseOnce("      attestations: write\n", "      attestations: read\n")},
		{name: "wrong publish dependency", mutate: replaceReleaseOnce("      - asset-verification\n", "      - verify\n")},
		{name: "wrong package timeout", mutate: replaceReleaseOnce("    timeout-minutes: 25\n", "    timeout-minutes: 26\n")},
		{name: "transitive artifact id", mutate: replaceReleaseOnce("${{ needs.package.outputs.artifact_id }}", "${{ needs.asset-verification.outputs.artifact_id }}")},
		{name: "name based download", mutate: replaceReleaseOnce("          artifact-ids: ${{ needs.package.outputs.artifact_id }}\n", "          name: release\n")},
		{name: "download extra name", mutate: replaceReleaseOnce("          digest-mismatch: error\n", "          digest-mismatch: error\n          name: attacker-controlled\n")},
		{name: "download extra pattern", mutate: replaceReleaseOnce("          digest-mismatch: error\n", "          digest-mismatch: error\n          pattern: '*'\n")},
		{name: "download extra merge-multiple", mutate: replaceReleaseOnce("          digest-mismatch: error\n", "          digest-mismatch: error\n          merge-multiple: true\n")},
		{name: "publish download extra name", mutate: replaceReleaseNth("          digest-mismatch: error\n", "          digest-mismatch: error\n          name: attacker-controlled\n", 2)},
		{name: "asset verification replaced by true", mutate: replaceReleaseStepRun("Verify checksums and attestations", "          true\n")},
		{name: "asset verification replaced by decoy", mutate: replaceReleaseStepRun("Verify checksums and attestations", "          : 'sha256sum --check --strict SHA256SUMS gh attestation verify github.com/krkarma777/ai-cli-gateway/.github/workflows/release.yml'\n          true\n")},
		{name: "asset verification relocated into unused function", mutate: replaceReleaseStepRun("Verify checksums and attestations", "          verify_assets() {\n            sha256sum --check --strict SHA256SUMS\n            gh attestation verify SHA256SUMS --repo krkarma777/ai-cli-gateway\n          }\n          true\n")},
		{name: "asset verification missing asset", mutate: replaceReleaseOnce("          ai-cli-gateway_${VERSION}_linux_arm64.tar.gz\n", "")},
		{name: "asset verification additional asset", mutate: replaceReleaseOnce("          SHA256SUMS\n          ASSETS\n", "          SHA256SUMS\n          unexpected.bin\n          ASSETS\n")},
		{name: "asset verification duplicate asset", mutate: replaceReleaseOnce("          ai-cli-gateway_${VERSION}_windows_amd64.zip\n", "          ai-cli-gateway_${VERSION}_linux_amd64.tar.gz\n")},
		{name: "asset verification missing predicate", mutate: replaceReleaseOnce("              --predicate-type https://slsa.dev/provenance/v1 \\\n", "")},
		{name: "asset verification wrong repository", mutate: replaceReleaseOnce("              --repo krkarma777/ai-cli-gateway \\\n", "              --repo attacker/ai-cli-gateway \\\n")},
		{name: "asset verification call removed", mutate: replaceReleaseOnce("            gh attestation verify \"${asset}\" \\\n              --repo krkarma777/ai-cli-gateway \\\n              --predicate-type https://slsa.dev/provenance/v1 \\\n              --signer-workflow github.com/krkarma777/ai-cli-gateway/.github/workflows/release.yml \\\n              --source-digest \"${TAG_COMMIT}\" \\\n              --source-ref \"refs/tags/${TAG}\"\n", "            : \"${asset}\"\n")},
		{name: "asset verification additional call", mutate: replaceReleaseOnce("          SHA256SUMS\n          ASSETS\n", "          SHA256SUMS\n          ASSETS\n          gh attestation verify unexpected.bin\n")},
		{name: "runner temp shell expression", mutate: replaceReleaseOnce("cd \"${RUNNER_TEMP}/release-assets\"", "cd \"${{ runner.temp }}/release-assets\"")},
		{name: "GitHub expression in shell", mutate: replaceReleaseOnce("readonly repository=krkarma777/ai-cli-gateway", "readonly repository=${{ github.repository }}")},
		{name: "publication edit replaced by comment decoy", mutate: replaceReleaseOnce("          gh release edit \"${TAG}\" --repo \"${repository}\" --draft=false", "          # gh release edit \"${TAG}\" --repo \"${repository}\" --draft=false\n          false")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mutated := test.mutate(document)
			if mutated == document {
				t.Fatal("mutation did not change release workflow")
			}
			workflow, err := parseClosedReleaseWorkflow([]byte(mutated))
			if err == nil {
				err = validateReleaseWorkflowContract(workflow)
			}
			if err == nil {
				t.Fatal("closed release contract accepted mutation")
			}
		})
	}
}

func replaceReleaseOnce(old, replacement string) func(string) string {
	return func(document string) string {
		return strings.Replace(document, old, replacement, 1)
	}
}

func replaceShellFunctionOnce(name, old, replacement string) func(string) string {
	return func(document string) string {
		start, end, ok := shellFunctionSpan(document, name)
		if !ok {
			return document
		}
		body := document[start:end]
		mutated := strings.Replace(body, old, replacement, 1)
		if mutated == body {
			return document
		}
		return document[:start] + mutated + document[end:]
	}
}

func insertAfterShellFunction(name, insertion string) func(string) string {
	return func(document string) string {
		_, end, ok := shellFunctionSpan(document, name)
		if !ok {
			return document
		}
		return document[:end] + insertion + document[end:]
	}
}

func shellFunctionSpan(script, name string) (int, int, bool) {
	opener := name + "() {"
	if strings.Count(script, opener) != 1 {
		return 0, 0, false
	}
	start := strings.Index(script, opener)
	for cursor := start + len(opener); cursor < len(script); {
		lineEnd := strings.IndexByte(script[cursor:], '\n')
		if lineEnd < 0 {
			lineEnd = len(script)
		} else {
			lineEnd += cursor
		}
		if strings.TrimSpace(script[cursor:lineEnd]) == "}" {
			if lineEnd < len(script) {
				lineEnd++
			}
			return start, lineEnd, true
		}
		if lineEnd == len(script) {
			break
		}
		cursor = lineEnd + 1
	}
	return 0, 0, false
}

func validateCommandSubstitutionGuards(script, expectedName string, compact bool) error {
	if compact != (expectedName == "expected") {
		return errors.New("binary validation flavor and expected-identity name differ")
	}
	authority := []string{
		`validate_authority() {`,
		`local current="$1" owner mode permissions`,
		`case "${current}" in /*) ;; *) return 1 ;; esac`,
		`while :; do`,
		`test -d "${current}" || return 1`,
		`test ! -L "${current}" || return 1`,
		`owner="$(stat -c '%u' -- "${current}")" || return 1`,
		`mode="$(stat -c '%a' -- "${current}")" || return 1`,
		`test "${owner}" = 0 || test "${owner}" = "${effective_uid}" || return 1`,
		`permissions=$((8#${mode}))`,
		`if (( (permissions & 0022) != 0 )); then`,
		`(( (permissions & 01000) != 0 )) || return 1`,
		`fi`,
		`test "${current}" != / || break`,
		`current="${current%/*}"`,
		`test -n "${current}" || current=/`,
		`done`,
		`}`,
	}
	if compact {
		authority = []string{
			`validate_authority() {`,
			`local current="$1" owner mode permissions`,
			`while :; do`,
			`case "${current}" in /*) ;; *) return 1 ;; esac`,
			`test -d "${current}" || return 1`,
			`test ! -L "${current}" || return 1`,
			`owner="$(stat -c '%u' -- "${current}")" || return 1`,
			`mode="$(stat -c '%a' -- "${current}")" || return 1`,
			`test "${owner}" = 0 || test "${owner}" = "${effective_uid}" || return 1`,
			`permissions=$((8#${mode}))`,
			`if (( (permissions & 0022) != 0 )); then (( (permissions & 01000) != 0 )) || return 1; fi`,
			`test "${current}" != / || break`,
			`current="${current%/*}"`,
			`test -n "${current}" || current=/`,
			`done`,
			`}`,
		}
	}
	if err := requireExactShellFunction(script, "validate_authority", authority); err != nil {
		return err
	}

	resolver := []string{
		`resolve_binary() {`,
		`local candidate link directory count=0`,
		`candidate="$(command -v -- "$1")" || return 1`,
		`case "${candidate}" in /*) ;; *) return 1 ;; esac`,
		`while test -L "${candidate}"; do`,
		`count=$((count + 1))`,
		`test "${count}" -le 32 || return 1`,
		`link="$(readlink -- "${candidate}")" || return 1`,
		`case "${link}" in`,
		`/*) candidate="${link}" ;;`,
		`*) candidate="${candidate%/*}/${link}" ;;`,
		`esac`,
		`done`,
		`directory="$(CDPATH=; cd -P -- "${candidate%/*}" && pwd -P)" || return 1`,
		`candidate="${directory}/${candidate##*/}"`,
		`case "${candidate}" in /*) ;; *) return 1 ;; esac`,
		`printf '%s\n' "${candidate}"`,
		`}`,
	}
	if compact {
		resolver = []string{
			`resolve_binary() {`,
			`local candidate link directory count=0`,
			`candidate="$(command -v -- "$1")" || return 1`,
			`case "${candidate}" in /*) ;; *) return 1 ;; esac`,
			`while test -L "${candidate}"; do`,
			`count=$((count + 1)); test "${count}" -le 32 || return 1`,
			`link="$(readlink -- "${candidate}")" || return 1`,
			`case "${link}" in /*) candidate="${link}" ;; *) candidate="${candidate%/*}/${link}" ;; esac`,
			`done`,
			`directory="$(CDPATH=; cd -P -- "${candidate%/*}" && pwd -P)" || return 1`,
			`printf '%s/%s\n' "${directory}" "${candidate##*/}"`,
			`}`,
		}
	}
	if err := requireExactShellFunction(script, "resolve_binary", resolver); err != nil {
		return err
	}

	expectedGuard := `test -z "${` + expectedName + `}" || test "${identity}" = "${` + expectedName + `}" || return 1`
	buildTool := []string{
		`validate_build_tool() {`,
		`local binary="$1" ` + expectedName + `="${2:-}" owner mode permissions identity`,
		`case "${binary}" in /*) ;; *) return 1 ;; esac`,
		`test -f "${binary}" || return 1`,
		`test ! -L "${binary}" || return 1`,
		`test -x "${binary}" || return 1`,
		`owner="$(stat -c '%u' -- "${binary}")" || return 1`,
		`test "${owner}" = 0 || test "${owner}" = "${effective_uid}" || return 1`,
		`mode="$(stat -c '%a' -- "${binary}")" || return 1`,
		`permissions=$((8#${mode}))`,
		`(( (permissions & 07000) == 0 )) || return 1`,
		`identity="$(stat -c '%d:%i' -- "${binary}")" || return 1`,
		expectedGuard,
		`printf '%s\n' "${identity}"`,
		`}`,
	}
	if err := requireExactShellFunction(script, "validate_build_tool", buildTool); err != nil {
		return err
	}
	strictBinary := []string{
		`validate_binary() {`,
		`local binary="$1" ` + expectedName + `="${2:-}" owner mode permissions identity`,
		`case "${binary}" in /*) ;; *) return 1 ;; esac`,
		`test -f "${binary}" || return 1`,
		`test ! -L "${binary}" || return 1`,
		`test -x "${binary}" || return 1`,
		`owner="$(stat -c '%u' -- "${binary}")" || return 1`,
	}
	if compact {
		strictBinary = append(strictBinary,
			`test "${owner}" = 0 || test "${owner}" = "${effective_uid}" || return 1`,
			`mode="$(stat -c '%a' -- "${binary}")" || return 1`,
		)
	} else {
		strictBinary = append(strictBinary,
			`mode="$(stat -c '%a' -- "${binary}")" || return 1`,
			`test "${owner}" = 0 || test "${owner}" = "${effective_uid}" || return 1`,
		)
	}
	strictBinary = append(strictBinary,
		`permissions=$((8#${mode}))`,
		`(( (permissions & 07022) == 0 )) || return 1`,
		`validate_authority "${binary%/*}" || return 1`,
		`identity="$(stat -c '%d:%i' -- "${binary}")" || return 1`,
		expectedGuard,
		`printf '%s\n' "${identity}"`,
		`}`,
	)
	return requireExactShellFunction(script, "validate_binary", strictBinary)
}

func requireExactShellFunction(script, name string, want []string) error {
	start, end, ok := shellFunctionSpan(script, name)
	if !ok {
		return fmt.Errorf("function %s must occur exactly once and have a closing brace", name)
	}
	got := trimmedShellLines(script[start:end])
	if !reflect.DeepEqual(got, want) {
		return fmt.Errorf("function %s body differs from the exact executable contract", name)
	}
	return nil
}

func replaceReleaseNth(old, replacement string, occurrence int) func(string) string {
	return func(document string) string {
		if occurrence < 1 {
			return document
		}
		searchStart := 0
		for current := 1; current <= occurrence; current++ {
			relative := strings.Index(document[searchStart:], old)
			if relative < 0 {
				return document
			}
			index := searchStart + relative
			if current == occurrence {
				return document[:index] + replacement + document[index+len(old):]
			}
			searchStart = index + len(old)
		}
		return document
	}
}

func replaceReleaseStepRun(stepName, replacement string) func(string) string {
	return func(document string) string {
		stepMarker := "      - name: " + stepName + "\n"
		stepStart := strings.Index(document, stepMarker)
		if stepStart < 0 {
			return document
		}
		runMarker := "        run: |\n"
		runRelative := strings.Index(document[stepStart:], runMarker)
		if runRelative < 0 {
			return document
		}
		runStart := stepStart + runRelative + len(runMarker)
		remainder := document[runStart:]
		end := len(remainder)
		for _, marker := range []string{"\n      - ", "\n\n  "} {
			if index := strings.Index(remainder, marker); index >= 0 && index < end {
				end = index + 1
			}
		}
		return document[:runStart] + replacement + document[runStart+end:]
	}
}

func TestReleasePublicationScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("decoded release shell fixture requires the supported macOS/Linux packaging hosts")
	}
	document := readRepositoryFile(t, ".github/workflows/release.yml")
	workflow, err := parseClosedReleaseWorkflow(document)
	if err != nil {
		t.Fatalf("parse closed release workflow: %v", err)
	}
	step, err := namedReleaseStep(workflow.Jobs["publish"].Steps, "Publish verified release")
	if err != nil {
		t.Fatal(err)
	}
	if step.Run == "" {
		t.Fatal("decoded publication run value is empty")
	}
	assertBashSyntax(t, "publication", step.Run)

	jqPath, err := exec.LookPath("jq")
	if err != nil {
		t.Fatalf("mandatory publication fixture requires jq: %v", err)
	}
	shaPath, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Fatalf("mandatory publication fixture requires sha256sum: %v", err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}

	tests := []struct {
		name       string
		fixture    string
		badDigest  bool
		wantCreate int
		wantEdit   int
		wantMarker string
		wantOK     bool
	}{
		{name: "lightweight success", fixture: "lightweight_success", wantCreate: 1, wantEdit: 1, wantOK: true},
		{name: "annotated success", fixture: "annotated_success", wantCreate: 1, wantEdit: 1, wantOK: true},
		{name: "existing release", fixture: "graphql_existing", wantMarker: "release_already_exists"},
		{name: "GraphQL errors", fixture: "graphql_errors", wantMarker: "release_preflight_invalid"},
		{name: "GraphQL missing release", fixture: "graphql_missing_release", wantMarker: "release_preflight_invalid"},
		{name: "GraphQL wrong release type", fixture: "graphql_wrong_release_type", wantMarker: "release_preflight_invalid"},
		{name: "GraphQL empty id", fixture: "graphql_empty_id", wantMarker: "release_preflight_invalid"},
		{name: "GraphQL malformed JSON", fixture: "graphql_malformed", wantMarker: "release_preflight_invalid"},
		{name: "GraphQL CLI failure", fixture: "graphql_gh_fail", wantMarker: "release_preflight_invalid"},
		{name: "checksum mismatch", fixture: "lightweight_success", badDigest: true},
	}
	for _, phase := range []int{1, 2} {
		wantCreate := 0
		if phase == 2 {
			wantCreate = 1
		}
		for _, failure := range []string{"gh_fail", "malformed", "missing_ref", "wrong_ref", "missing_object", "wrong_object_type", "wrong_object_sha"} {
			tests = append(tests, struct {
				name       string
				fixture    string
				badDigest  bool
				wantCreate int
				wantEdit   int
				wantMarker string
				wantOK     bool
			}{name: fmt.Sprintf("ref phase %d %s", phase, failure), fixture: fmt.Sprintf("ref_p%d_%s", phase, failure), wantCreate: wantCreate})
		}
		for _, failure := range []string{"gh_fail", "malformed", "missing_top_sha", "wrong_top_sha", "missing_target", "wrong_target_type", "wrong_target_sha", "requested_sha_mismatch", "nested_tag"} {
			tests = append(tests, struct {
				name       string
				fixture    string
				badDigest  bool
				wantCreate int
				wantEdit   int
				wantMarker string
				wantOK     bool
			}{name: fmt.Sprintf("annotated phase %d %s", phase, failure), fixture: fmt.Sprintf("tag_p%d_%s", phase, failure), wantCreate: wantCreate})
		}
		tests = append(tests, struct {
			name       string
			fixture    string
			badDigest  bool
			wantCreate int
			wantEdit   int
			wantMarker string
			wantOK     bool
		}{name: fmt.Sprintf("commit mismatch phase %d", phase), fixture: fmt.Sprintf("commit_p%d_mismatch", phase), wantCreate: wantCreate})
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			binRoot := filepath.Join(root, "bin")
			assetsRoot := filepath.Join(root, "release-assets")
			if err := os.MkdirAll(binRoot, 0o700); err != nil {
				t.Fatalf("create fake bin: %v", err)
			}
			if err := os.MkdirAll(assetsRoot, 0o700); err != nil {
				t.Fatalf("create assets: %v", err)
			}
			wrapper := "#!/bin/sh\nexec \"${SPAWNGATE_TEST_BINARY}\" -test.run '^TestReleasePublicationFakeGH$' -- \"$@\"\n"
			writeFixtureFile(t, binRoot, "gh", []byte(wrapper))
			if err := os.Chmod(filepath.Join(binRoot, "gh"), 0o700); err != nil { //nolint:gosec // Test-only fixture must be executable.
				t.Fatalf("chmod fake gh: %v", err)
			}
			linkFixtureTool(t, binRoot, "jq", jqPath)
			shaWrapper := "#!/bin/sh\n{ printf '%s\\0' \"$(($# + 1))\"; printf 'sha256sum\\0'; for argument in \"$@\"; do printf '%s\\0' \"${argument}\"; done; } >> \"${GH_EVENT_LOG}\"\nexec \"${REAL_SHA256SUM}\" \"$@\"\n"
			writeFixtureFile(t, binRoot, "sha256sum", []byte(shaWrapper))
			if err := os.Chmod(filepath.Join(binRoot, "sha256sum"), 0o700); err != nil { //nolint:gosec // Test-only fixture must be executable.
				t.Fatalf("chmod sha256sum wrapper: %v", err)
			}
			writeReleasePublicationAssets(t, assetsRoot, test.badDigest)

			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "/bin/bash", "-c", step.Run) //nolint:gosec // Executes the repository-owned decoded release script in a closed fake-GitHub fixture.
			command.Dir = root
			command.Env = []string{
				"PATH=" + binRoot + ":/usr/bin:/bin",
				"GH_TOKEN=fixture-token",
				"TAG=v0.1.0",
				"VERSION=0.1.0",
				"TAG_COMMIT=" + strings.Repeat("a", 40),
				"RUNNER_TEMP=" + root,
				"GH_FIXTURE=" + test.fixture,
				"GH_LOG=" + filepath.Join(root, "gh.log"),
				"GH_EVENT_LOG=" + filepath.Join(root, "events.log"),
				"GH_REF_COUNT=" + filepath.Join(root, "ref.count"),
				"GH_TAG_COUNT=" + filepath.Join(root, "tag.count"),
				"REAL_SHA256SUM=" + shaPath,
				"SPAWNGATE_TEST_BINARY=" + testBinary,
			}
			output, runErr := command.CombinedOutput()
			if test.wantOK && runErr != nil {
				t.Fatalf("publication failed: %v: %s", runErr, output)
			}
			if !test.wantOK && runErr == nil {
				t.Fatalf("publication succeeded unexpectedly: %s", output)
			}
			if test.wantMarker != "" && strings.TrimSpace(string(output)) != test.wantMarker {
				t.Fatalf("output = %q, want only %q", output, test.wantMarker)
			}
			calls := readFakeGHCalls(t, filepath.Join(root, "gh.log"))
			createCount, editCount := 0, 0
			for _, call := range calls {
				if len(call) >= 2 && call[0] == "release" && call[1] == "create" && !slicesContain(call, "--help") {
					createCount++
				}
				if len(call) >= 2 && call[0] == "release" && call[1] == "edit" && !slicesContain(call, "--help") {
					editCount++
				}
			}
			if createCount != test.wantCreate || editCount != test.wantEdit {
				t.Fatalf("mutations create=%d edit=%d, want create=%d edit=%d; calls=%q", createCount, editCount, test.wantCreate, test.wantEdit, calls)
			}
			if test.wantOK {
				assertSuccessfulPublicationTrace(t, root, test.fixture, calls)
			}
		})
	}
}

func TestReleaseAssetVerificationScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("decoded asset-verification shell fixture requires macOS or Linux")
	}
	document := readRepositoryFile(t, ".github/workflows/release.yml")
	script, err := decodedWorkflowStepRun(document, "asset-verification", "Verify checksums and attestations")
	if err != nil {
		t.Fatal(err)
	}
	assertBashSyntax(t, "asset verification", script)
	shaPath, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Fatalf("mandatory asset-verification fixture requires sha256sum: %v", err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	root := t.TempDir()
	binRoot := filepath.Join(root, "bin")
	assetsRoot := filepath.Join(root, "release-assets")
	if err := os.MkdirAll(binRoot, 0o700); err != nil {
		t.Fatalf("create fake bin: %v", err)
	}
	if err := os.MkdirAll(assetsRoot, 0o700); err != nil {
		t.Fatalf("create assets: %v", err)
	}
	wrapper := "#!/bin/sh\nexec \"${SPAWNGATE_TEST_BINARY}\" -test.run '^TestReleasePublicationFakeGH$' -- \"$@\"\n"
	writeFixtureFile(t, binRoot, "gh", []byte(wrapper))
	if err := os.Chmod(filepath.Join(binRoot, "gh"), 0o700); err != nil { //nolint:gosec // Test-only fixture must be executable.
		t.Fatalf("chmod fake gh: %v", err)
	}
	shaWrapper := "#!/bin/sh\n{ printf '%s\\0' \"$(($# + 1))\"; printf 'sha256sum\\0'; for argument in \"$@\"; do printf '%s\\0' \"${argument}\"; done; } >> \"${GH_EVENT_LOG}\"\nexec \"${REAL_SHA256SUM}\" \"$@\"\n"
	writeFixtureFile(t, binRoot, "sha256sum", []byte(shaWrapper))
	if err := os.Chmod(filepath.Join(binRoot, "sha256sum"), 0o700); err != nil { //nolint:gosec // Test-only fixture must be executable.
		t.Fatalf("chmod sha256sum wrapper: %v", err)
	}
	writeReleasePublicationAssets(t, assetsRoot, false)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/bash", "-c", script) //nolint:gosec // Executes the repository-owned decoded verification script with fixed fake tools.
	command.Dir = root
	command.Env = []string{
		"PATH=" + binRoot + ":/usr/bin:/bin",
		"GH_TOKEN=fixture-token",
		"TAG=v0.1.0",
		"VERSION=0.1.0",
		"TAG_COMMIT=" + strings.Repeat("a", 40),
		"RUNNER_TEMP=" + root,
		"GH_FIXTURE=attestation_success",
		"GH_LOG=" + filepath.Join(root, "gh.log"),
		"GH_EVENT_LOG=" + filepath.Join(root, "events.log"),
		"REAL_SHA256SUM=" + shaPath,
		"SPAWNGATE_TEST_BINARY=" + testBinary,
	}
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("asset verification failed: %v: %s", runErr, output)
	}
	assets := []string{
		"ai-cli-gateway_0.1.0_linux_amd64.tar.gz",
		"ai-cli-gateway_0.1.0_linux_arm64.tar.gz",
		"ai-cli-gateway_0.1.0_darwin_amd64.tar.gz",
		"ai-cli-gateway_0.1.0_darwin_arm64.tar.gz",
		"ai-cli-gateway_0.1.0_windows_amd64.zip",
		"ai-cli-gateway_0.1.0_sbom.spdx.json",
		"SHA256SUMS",
	}
	wantCalls := [][]string{{"--version"}, {"attestation", "verify", "--help"}}
	for _, asset := range assets {
		wantCalls = append(wantCalls, []string{
			"attestation", "verify", asset,
			"--repo", "krkarma777/ai-cli-gateway",
			"--predicate-type", "https://slsa.dev/provenance/v1",
			"--signer-workflow", "github.com/krkarma777/ai-cli-gateway/.github/workflows/release.yml",
			"--source-digest", strings.Repeat("a", 40),
			"--source-ref", "refs/tags/v0.1.0",
		})
	}
	if calls := readFakeGHCalls(t, filepath.Join(root, "gh.log")); !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("asset-verification calls = %q, want exact %q", calls, wantCalls)
	}
	wantEvents := [][]string{{"gh", "--version"}, {"gh", "attestation", "verify", "--help"}, {"sha256sum", "--check", "--strict", "SHA256SUMS"}}
	for _, call := range wantCalls[2:] {
		wantEvents = append(wantEvents, append([]string{"gh"}, call...))
	}
	if events := readFakeGHCalls(t, filepath.Join(root, "events.log")); !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("asset-verification events = %q, want checksum-before-attestation trace %q", events, wantEvents)
	}
}

func TestReleasePublicationFakeGH(_ *testing.T) {
	if os.Getenv("GH_FIXTURE") == "" {
		return
	}
	args := os.Args
	separator := -1
	for index, arg := range args {
		if arg == "--" {
			separator = index
			break
		}
	}
	if separator < 0 {
		os.Exit(90)
	}
	os.Exit(runReleasePublicationFakeGH(args[separator+1:]))
}

func TestWorkflowActionlintIsolationScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("decoded actionlint shell fixture requires macOS or Linux")
	}
	document := readRepositoryFile(t, ".github/workflows/ci.yml")
	script, err := decodedWorkflowStepRun(document, "lint", "Validate workflow syntax")
	if err != nil {
		t.Fatal(err)
	}
	assertBashSyntax(t, "actionlint", script)
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	root := secureExternalFixtureRoot(t, "spawngate-actionlint-fixture-")
	binRoot := filepath.Join(root, "fixture-bin")
	if err := os.Mkdir(binRoot, 0o700); err != nil {
		t.Fatalf("create fixture bin: %v", err)
	}
	writeCleanToolWrapper(t, filepath.Join(binRoot, "go"), testBinary, "actionlint-go", cleanGoEnvironmentNames())
	if err := os.Chmod(filepath.Join(binRoot, "go"), 0o777); err != nil { // #nosec G302 -- models the caller-authorized GitHub hosted toolchain.
		t.Fatalf("chmod hosted Go fixture: %v", err)
	}
	writePortableStatWrapper(t, filepath.Join(binRoot, "stat"))
	poisonMarker := filepath.Join(root, "poison-ran")
	poisonHelper := filepath.Join(root, "poison-helper")
	writeFixtureFile(t, root, "poison-helper", []byte("#!/bin/sh\n: > \""+poisonMarker+"\"\nexit 97\n"))
	if err := os.Chmod(poisonHelper, 0o700); err != nil { //nolint:gosec // Test-only failing helper must be executable.
		t.Fatalf("chmod poison helper: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/bash", "-c", script) //nolint:gosec // Executes the decoded repository-owned CI shell with fixed fake tools.
	command.Dir = repositoryRootForTest(t)
	command.Env = poisonedToolParentEnvironment(root, binRoot, poisonHelper)
	if output, runErr := command.CombinedOutput(); runErr != nil {
		t.Fatalf("decoded actionlint shell: %v: %s", runErr, output)
	}
	if _, err := os.Stat(poisonMarker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("poisoned GOFLAGS helper was observed: %v", err)
	}
	actionlintRoot := filepath.Join(root, "actionlint-tools")
	wantGo := cleanGoEnvironment(actionlintRoot)
	assertExactEnvironmentRecords(t, filepath.Join(actionlintRoot, "actionlint-go.env"), 2, wantGo)
	if calls := readFakeGHCalls(t, filepath.Join(actionlintRoot, "actionlint-go.argv")); !reflect.DeepEqual(calls, [][]string{
		{"env", "GOVERSION"},
		{"install", "github.com/rhysd/actionlint/cmd/actionlint@v1.7.12"},
	}) {
		t.Fatalf("actionlint Go argv trace = %q", calls)
	}
	wantRuntime := cleanActionlintEnvironment(actionlintRoot)
	assertExactEnvironmentRecords(t, filepath.Join(actionlintRoot, "actionlint.env"), 3, wantRuntime)
	if calls := readFakeGHCalls(t, filepath.Join(actionlintRoot, "actionlint.argv")); !reflect.DeepEqual(calls, [][]string{
		{"-version"},
		{"-help"},
		{"-config-file", filepath.Join(actionlintRoot, "config", "actionlint.yaml"), "-shellcheck=", "-pyflakes=", "-no-color", ".github/workflows/ci.yml", ".github/workflows/release.yml"},
	}) {
		t.Fatalf("actionlint argv trace = %q", calls)
	}
}

func TestWorkflowActionlintIsolationRejectsWritableInstalledTool(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("decoded actionlint shell fixture requires macOS or Linux")
	}
	document := readRepositoryFile(t, ".github/workflows/ci.yml")
	script, err := decodedWorkflowStepRun(document, "lint", "Validate workflow syntax")
	if err != nil {
		t.Fatal(err)
	}
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	root := secureExternalFixtureRoot(t, "spawngate-actionlint-writable-fixture-")
	binRoot := filepath.Join(root, "fixture-bin")
	if err := os.Mkdir(binRoot, 0o700); err != nil {
		t.Fatalf("create fixture bin: %v", err)
	}
	writeCleanToolWrapper(t, filepath.Join(binRoot, "go"), testBinary, "actionlint-go-writable-install", cleanGoEnvironmentNames())
	writePortableStatWrapper(t, filepath.Join(binRoot, "stat"))
	poisonHelper := filepath.Join(root, "poison-helper")
	writeFixtureFile(t, root, "poison-helper", []byte("#!/bin/sh\nexit 97\n"))
	if err := os.Chmod(poisonHelper, 0o700); err != nil { //nolint:gosec // Test-only failing helper must be executable.
		t.Fatalf("chmod poison helper: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/bash", "-c", script) //nolint:gosec // Executes the decoded repository-owned CI shell with fixed fake tools.
	command.Dir = repositoryRootForTest(t)
	command.Env = poisonedToolParentEnvironment(root, binRoot, poisonHelper)
	output, runErr := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("decoded actionlint shell exceeded 30s: %v", ctx.Err())
	}
	if runErr == nil {
		t.Fatalf("decoded actionlint shell accepted a writable installed binary: %s", output)
	}
	actionlintRoot := filepath.Join(root, "actionlint-tools")
	installed, err := os.Stat(filepath.Join(actionlintRoot, "bin", "actionlint"))
	if err != nil {
		t.Fatalf("stat writable actionlint fixture: %v", err)
	}
	if installed.Mode().Perm()&0o022 == 0 {
		t.Fatalf("actionlint rejection fixture mode = %04o, want group/other writable", installed.Mode().Perm())
	}
	if calls := readFakeGHCalls(t, filepath.Join(actionlintRoot, "actionlint.argv")); len(calls) != 0 {
		t.Fatalf("writable actionlint executed before rejection: %q", calls)
	}
}

func TestReleaseSyftIsolationScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("decoded Syft shell fixture requires the supported macOS/Linux packaging hosts")
	}
	document := readRepositoryFile(t, ".github/workflows/release.yml")
	workflow, err := parseClosedReleaseWorkflow(document)
	if err != nil {
		t.Fatal(err)
	}
	step, err := namedReleaseStep(workflow.Jobs["package"].Steps, "Generate SBOM and checksums")
	if err != nil {
		t.Fatal(err)
	}
	assertBashSyntax(t, "Syft", step.Run)
	testBinary, err := os.Executable()
	if err != nil {
		t.Fatalf("resolve test binary: %v", err)
	}
	// The success fixtures exercise the full decoded release pipeline. Race
	// instrumentation on GitHub's macOS runner can take close to a minute. The
	// rejected fixtures still need enough headroom to distinguish a real policy
	// rejection from a slow instrumented shell.
	const rejectedFixtureTimeout = 25 * time.Second
	const successFixtureTimeout = 90 * time.Second
	tests := []struct {
		name   string
		mode   string
		wantOK bool
	}{
		{name: "closed environments", mode: "syft-go", wantOK: true},
		{name: "canonical checksum final A", mode: "syft-go-valid-checksum-a", wantOK: true},
		{name: "canonical checksum final 8", mode: "syft-go-valid-checksum-8", wantOK: true},
		{name: "wrong command path", mode: "syft-go-wrong-path"},
		{name: "wrong module version", mode: "syft-go-wrong-module"},
		{name: "missing module checksum", mode: "syft-go-missing-checksum"},
		{name: "bare module checksum prefix", mode: "syft-go-bare-checksum"},
		{name: "noncanonical module checksum padding", mode: "syft-go-invalid-checksum-padding"},
		{name: "short module checksum", mode: "syft-go-short-checksum"},
		{name: "long module checksum", mode: "syft-go-long-checksum"},
		{name: "missing module checksum padding", mode: "syft-go-missing-checksum-padding"},
		{name: "extra module checksum padding", mode: "syft-go-extra-checksum-padding"},
		{name: "invalid module checksum alphabet", mode: "syft-go-invalid-checksum-alphabet"},
		{name: "duplicate module record", mode: "syft-go-duplicate-module"},
		{name: "wrong build setting", mode: "syft-go-wrong-build-setting"},
		{name: "wrong reported version", mode: "syft-go-wrong-version"},
		{name: "trailing reported version", mode: "syft-go-trailing-version"},
		{name: "duplicate reported version", mode: "syft-go-duplicate-version"},
		{name: "valid and wrong reported versions", mode: "syft-go-mixed-wrong-version"},
		{name: "valid and trailing reported versions", mode: "syft-go-mixed-trailing-version"},
		{name: "valid and malformed reported versions", mode: "syft-go-mixed-malformed-version"},
		{name: "versioned label is not version field", mode: "syft-go-versioned-label", wantOK: true},
		{name: "wrong SPDX creator", mode: "syft-go-wrong-creator"},
		{name: "same-mode config replacement", mode: "syft-go-replace-config"},
		{name: "same-mode root replacement", mode: "syft-go-replace-root"},
		{name: "same-mode config replacement between Syft calls", mode: "syft-go-replace-config-at-syft"},
		{name: "same-mode root replacement after scan", mode: "syft-go-replace-root-after-scan"},
		{name: "writable installed Syft", mode: "syft-go-writable-install"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := secureExternalFixtureRoot(t, "spawngate-syft-fixture-")
			binRoot := filepath.Join(root, "fixture-bin")
			if err := os.Mkdir(binRoot, 0o700); err != nil {
				t.Fatalf("create fixture bin: %v", err)
			}
			writeCleanToolWrapper(t, filepath.Join(binRoot, "go"), testBinary, test.mode, cleanGoEnvironmentNames())
			if err := os.Chmod(filepath.Join(binRoot, "go"), 0o777); err != nil { // #nosec G302 -- models the caller-authorized GitHub hosted toolchain.
				t.Fatalf("chmod hosted Go fixture: %v", err)
			}
			writePortableStatWrapper(t, filepath.Join(binRoot, "stat"))
			if err := os.Mkdir(filepath.Join(root, "release-staging"), 0o700); err != nil {
				t.Fatalf("create staging: %v", err)
			}
			if err := os.Mkdir(filepath.Join(root, "release-assets"), 0o700); err != nil {
				t.Fatalf("create assets: %v", err)
			}
			writeFakeReleasepack(t, filepath.Join(root, "releasepack"))
			poisonMarker := filepath.Join(root, "poison-ran")
			poisonHelper := filepath.Join(root, "poison-helper")
			writeFixtureFile(t, root, "poison-helper", []byte("#!/bin/sh\n: > \""+poisonMarker+"\"\nexit 97\n"))
			if err := os.Chmod(poisonHelper, 0o700); err != nil { //nolint:gosec // Test-only failing helper must be executable.
				t.Fatalf("chmod poison helper: %v", err)
			}
			fixtureTimeout := rejectedFixtureTimeout
			if test.wantOK {
				fixtureTimeout = successFixtureTimeout
			}
			ctx, cancel := context.WithTimeout(context.Background(), fixtureTimeout)
			command := exec.CommandContext(ctx, "/bin/bash", "-c", step.Run) //nolint:gosec // Executes the decoded repository-owned Syft shell with fixed fake tools.
			command.Dir = repositoryRootForTest(t)
			command.Env = append(poisonedToolParentEnvironment(root, binRoot, poisonHelper),
				"TAG=v0.1.0", "VERSION=0.1.0", "SOURCE_EPOCH=1785805793", "GITHUB_WORKSPACE="+repositoryRootForTest(t))
			output, runErr := command.CombinedOutput()
			contextErr := ctx.Err()
			cancel()
			if contextErr != nil {
				t.Fatalf("decoded Syft shell exceeded %s: %v", fixtureTimeout, contextErr)
			}
			if test.wantOK && runErr != nil {
				t.Fatalf("decoded Syft shell: %v: %s", runErr, output)
			}
			if !test.wantOK && runErr == nil {
				t.Fatalf("decoded Syft shell accepted %s", test.mode)
			}
			if _, err := os.Stat(poisonMarker); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("poisoned GOFLAGS helper was observed: %v", err)
			}
			toolsRoot := filepath.Join(root, "release-tools")
			if test.mode == "syft-go-writable-install" {
				installed, statErr := os.Stat(filepath.Join(toolsRoot, "bin", "syft"))
				if statErr != nil {
					t.Fatalf("stat writable Syft fixture: %v", statErr)
				}
				if installed.Mode().Perm()&0o022 == 0 {
					t.Fatalf("Syft rejection fixture mode = %04o, want group/other writable", installed.Mode().Perm())
				}
				if calls := readFakeGHCalls(t, filepath.Join(toolsRoot, "syft.argv")); len(calls) != 0 {
					t.Fatalf("writable Syft executed before rejection: %q", calls)
				}
			}
			if test.mode == "syft-go-replace-config" || test.mode == "syft-go-replace-root" {
				if calls := readFakeGHCalls(t, filepath.Join(toolsRoot, "syft-go.argv")); !reflect.DeepEqual(calls, [][]string{{"env", "GOVERSION"}}) {
					t.Fatalf("identity replacement Go argv = %q, want failure before the second external call", calls)
				}
				if calls := readFakeGHCalls(t, filepath.Join(toolsRoot, "syft.argv")); len(calls) != 0 {
					t.Fatalf("identity replacement reached Syft: %q", calls)
				}
				target := "root"
				mode, kind := "0700", "directory"
				if test.mode == "syft-go-replace-config" {
					target, mode, kind = "config", "0600", "regular"
				}
				assertSameModeIdentityReplacementEvidence(t, toolsRoot, target, mode, kind)
			}
			if test.mode == "syft-go-replace-config-at-syft" || test.mode == "syft-go-replace-root-after-scan" {
				if calls := readFakeGHCalls(t, filepath.Join(toolsRoot, "syft-go.argv")); !reflect.DeepEqual(calls, expectedSyftGoCalls(toolsRoot)) {
					t.Fatalf("runtime identity replacement Go argv = %q", calls)
				}
				wantSyft := expectedSyftCalls(root, toolsRoot)
				target, mode, kind := "config", "0600", "regular"
				if test.mode == "syft-go-replace-config-at-syft" {
					wantSyft = wantSyft[:1]
				} else {
					target, mode, kind = "root", "0700", "directory"
				}
				if calls := readFakeGHCalls(t, filepath.Join(toolsRoot, "syft.argv")); !reflect.DeepEqual(calls, wantSyft) {
					t.Fatalf("runtime identity replacement Syft argv = %q, want %q", calls, wantSyft)
				}
				assertSameModeIdentityReplacementEvidence(t, toolsRoot, target, mode, kind)
			}
			if test.wantOK {
				assertExactEnvironmentRecords(t, filepath.Join(toolsRoot, "syft-go.env"), 3, cleanGoEnvironment(toolsRoot))
				if calls := readFakeGHCalls(t, filepath.Join(toolsRoot, "syft-go.argv")); !reflect.DeepEqual(calls, expectedSyftGoCalls(toolsRoot)) {
					t.Fatalf("Syft Go argv trace = %q", calls)
				}
				base := cleanSyftEnvironment(toolsRoot, false)
				scan := cleanSyftEnvironment(toolsRoot, true)
				records := readEnvironmentRecords(t, filepath.Join(toolsRoot, "syft.env"))
				if len(records) != 5 {
					t.Fatalf("Syft environment records = %d, want 5", len(records))
				}
				for index := 0; index < 4; index++ {
					if !reflect.DeepEqual(records[index], base) {
						t.Fatalf("Syft runtime env %d = %v, want %v", index, records[index], base)
					}
				}
				if !reflect.DeepEqual(records[4], scan) {
					t.Fatalf("Syft scan env = %v, want %v", records[4], scan)
				}
				if calls := readFakeGHCalls(t, filepath.Join(toolsRoot, "syft.argv")); !reflect.DeepEqual(calls, expectedSyftCalls(root, toolsRoot)) {
					t.Fatalf("Syft argv trace = %q", calls)
				}
			}
		})
	}
}

func expectedSyftGoCalls(toolsRoot string) [][]string {
	return [][]string{
		{"env", "GOVERSION"},
		{"install", "-ldflags", "-X main.version=1.50.0", "github.com/anchore/syft/cmd/syft@v1.50.0"},
		{"version", "-m", filepath.Join(toolsRoot, "bin", "syft")},
	}
}

func expectedSyftCalls(runnerRoot, toolsRoot string) [][]string {
	return [][]string{
		{"help"},
		{"scan", "--help"},
		{"version", "--help"},
		{"version"},
		{"scan", "dir:" + filepath.Join(runnerRoot, "release-staging"), "-c", filepath.Join(toolsRoot, "config", "syft.yaml"), "-o", "spdx-json=" + filepath.Join(runnerRoot, "raw-syft.spdx.json")},
	}
}

func assertSameModeIdentityReplacementEvidence(t *testing.T, root, target, mode, kind string) {
	t.Helper()
	evidencePath := root + ".identity-evidence"
	if strings.HasPrefix(evidencePath, root+string(os.PathSeparator)) {
		t.Fatal("identity evidence must be outside the replaced tools root")
	}
	records := readFakeGHCalls(t, evidencePath)
	if len(records) != 1 || len(records[0]) != 10 {
		t.Fatalf("identity evidence = %#v, want one complete ten-field record", records)
	}
	record := records[0]
	if record[0] != "complete" || record[1] != target {
		t.Fatalf("identity evidence header = %q, want complete/%s", record[:2], target)
	}
	oldDevice, oldInode, oldOK := strings.Cut(record[2], ":")
	newDevice, newInode, newOK := strings.Cut(record[3], ":")
	if !oldOK || !newOK || oldDevice != newDevice || oldInode == newInode {
		t.Fatalf("identity evidence did not prove same-device inode replacement: old=%q new=%q", record[2], record[3])
	}
	wantOwner := strconv.Itoa(os.Geteuid())
	if record[4] != mode || record[5] != mode || record[6] != wantOwner || record[7] != wantOwner ||
		record[8] != kind || record[9] != kind {
		t.Fatalf("identity evidence mode/owner/type = %q, want mode=%s owner=%s kind=%s", record[4:], mode, wantOwner, kind)
	}
}

func TestWorkflowToolFake(_ *testing.T) {
	args := argumentsAfterDoubleDash(os.Args)
	if len(args) < 4 || args[0] != "--mode" || args[2] != "--test-binary" {
		return
	}
	mode, testBinary, toolArgs := args[1], args[3], args[4:]
	os.Exit(runWorkflowToolFake(mode, testBinary, toolArgs))
}

func TestWorkflowToolFakeRejectsArbitraryInstall(t *testing.T) {
	tests := []struct {
		name string
		mode string
		args []string
		tool string
		log  string
	}{
		{name: "actionlint", mode: "actionlint-go", args: []string{"install", "attacker.invalid/actionlint@v0"}, tool: "actionlint", log: "actionlint-go.argv"},
		{name: "Syft", mode: "syft-go", args: []string{"install", "attacker.invalid/syft@v0"}, tool: "syft", log: "syft-go.argv"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			binRoot := filepath.Join(root, "bin")
			if err := os.Mkdir(binRoot, 0o700); err != nil {
				t.Fatalf("create fake GOBIN: %v", err)
			}
			t.Setenv("GOPATH", filepath.Join(root, "gopath"))
			t.Setenv("GOBIN", binRoot)
			if code := runWorkflowToolFake(test.mode, "unused-test-binary", test.args); code == 0 {
				t.Fatal("fake Go accepted arbitrary install argv")
			}
			if _, err := os.Stat(filepath.Join(binRoot, test.tool)); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("arbitrary install created %s: %v", test.tool, err)
			}
			if calls := readFakeGHCalls(t, filepath.Join(root, test.log)); !reflect.DeepEqual(calls, [][]string{test.args}) {
				t.Fatalf("arbitrary install trace = %q, want %q", calls, [][]string{test.args})
			}
		})
	}
}

func runWorkflowToolFake(mode, testBinary string, args []string) int {
	if strings.HasPrefix(mode, "actionlint-go") || strings.HasPrefix(mode, "syft-go") {
		root := filepath.Dir(os.Getenv("GOPATH"))
		logName := "actionlint-go.env"
		argvName := "actionlint-go.argv"
		if strings.HasPrefix(mode, "syft-go") {
			logName = "syft-go.env"
			argvName = "syft-go.argv"
		}
		if err := appendEnvironmentRecord(filepath.Join(root, logName)); err != nil {
			return 81
		}
		if err := appendFakeGHCall(filepath.Join(root, argvName), args); err != nil {
			return 81
		}
		if reflect.DeepEqual(args, []string{"env", "GOVERSION"}) {
			var err error
			switch mode {
			case "syft-go-replace-config":
				err = replaceFixtureConfigIdentity(root)
			case "syft-go-replace-root":
				err = replaceFixtureRootIdentity(root)
			}
			if err != nil {
				return 86
			}
			fmt.Println("go1.26.5")
			return 0
		}
		wantInstall := []string{"install", "github.com/rhysd/actionlint/cmd/actionlint@v1.7.12"}
		if strings.HasPrefix(mode, "syft-go") {
			wantInstall = []string{"install", "-ldflags", "-X main.version=1.50.0", "github.com/anchore/syft/cmd/syft@v1.50.0"}
		}
		if reflect.DeepEqual(args, wantInstall) {
			tool := "actionlint"
			installedMode := "actionlint"
			environment := cleanActionlintEnvironmentNames()
			if strings.HasPrefix(mode, "syft-go") {
				tool = "syft"
				installedMode = strings.TrimPrefix(mode, "syft-go")
				installedMode = "syft" + installedMode
				environment = cleanSyftEnvironmentNames()
			}
			installedPath := filepath.Join(os.Getenv("GOBIN"), tool)
			if err := writeCleanToolWrapperFile(installedPath, testBinary, installedMode, environment); err != nil {
				return 82
			}
			if mode == "actionlint-go-writable-install" || mode == "syft-go-writable-install" {
				if err := os.Chmod(installedPath, 0o777); err != nil { //nolint:gosec // Test-controlled path and writable rejection fixture.
					return 82
				}
			}
			return 0
		}
		if len(args) > 0 && args[0] == "install" {
			return 2
		}
		if len(args) == 3 && args[0] == "version" && args[1] == "-m" && strings.HasPrefix(mode, "syft-go") {
			path := "github.com/anchore/syft/cmd/syft"
			moduleVersion := "v1.50.0"
			checksum := "h1:kSQ4oshw6dwHxcYhrH1jUZl/M05kiCfyPoGJgvXe61s="
			buildSetting := "-X main.version=1.50.0"
			switch mode {
			case "syft-go-wrong-path":
				path = "example.invalid/syft"
			case "syft-go-wrong-module":
				moduleVersion = "v1.49.0"
			case "syft-go-missing-checksum":
				checksum = ""
			case "syft-go-bare-checksum":
				checksum = "h1:"
			case "syft-go-invalid-checksum-padding":
				checksum = "h1:" + strings.Repeat("A", 42) + "B="
			case "syft-go-valid-checksum-a":
				checksum = "h1:" + strings.Repeat("A", 43) + "="
			case "syft-go-valid-checksum-8":
				checksum = "h1:" + strings.Repeat("A", 42) + "8="
			case "syft-go-short-checksum":
				checksum = "h1:" + strings.Repeat("A", 42) + "="
			case "syft-go-long-checksum":
				checksum = "h1:" + strings.Repeat("A", 44) + "="
			case "syft-go-missing-checksum-padding":
				checksum = "h1:" + strings.Repeat("A", 43)
			case "syft-go-extra-checksum-padding":
				checksum = "h1:" + strings.Repeat("A", 43) + "=="
			case "syft-go-invalid-checksum-alphabet":
				checksum = "h1:" + strings.Repeat("A", 42) + "-="
			case "syft-go-wrong-build-setting":
				buildSetting = "-X main.version=1.49.0"
			}
			fmt.Printf("%s: go1.26.5\n\tpath\t%s\n\tmod\tgithub.com/anchore/syft\t%s\t%s\n\tbuild\t-ldflags=\"%s\"\n", args[2], path, moduleVersion, checksum, buildSetting)
			if mode == "syft-go-duplicate-module" {
				fmt.Printf("\tmod\tgithub.com/anchore/syft\t%s\t%s\n", moduleVersion, checksum)
			}
			return 0
		}
		return 2
	}
	if mode == "actionlint" {
		root := filepath.Dir(os.Getenv("HOME"))
		if err := appendEnvironmentRecord(filepath.Join(root, "actionlint.env")); err != nil {
			return 83
		}
		if err := appendFakeGHCall(filepath.Join(root, "actionlint.argv"), args); err != nil {
			return 83
		}
		if reflect.DeepEqual(args, []string{"-version"}) {
			fmt.Println("v1.7.12")
			return 0
		}
		if reflect.DeepEqual(args, []string{"-help"}) {
			fmt.Println("-config-file -shellcheck -pyflakes -no-color")
			return 0
		}
		return 0
	}
	if strings.HasPrefix(mode, "syft") {
		root := filepath.Dir(os.Getenv("HOME"))
		if err := appendEnvironmentRecord(filepath.Join(root, "syft.env")); err != nil {
			return 84
		}
		if err := appendFakeGHCall(filepath.Join(root, "syft.argv"), args); err != nil {
			return 84
		}
		if reflect.DeepEqual(args, []string{"version"}) {
			version := "1.50.0"
			if mode == "syft-wrong-version" {
				version = "1.49.0"
			}
			reported := "Version:    " + version
			if mode == "syft-trailing-version" {
				reported += " trailing"
			}
			fmt.Println(reported)
			if mode == "syft-duplicate-version" {
				fmt.Println(reported)
			}
			if mode == "syft-mixed-wrong-version" {
				fmt.Println("Version:    1.49.0")
			}
			if mode == "syft-mixed-trailing-version" {
				fmt.Println("Version:    1.50.0 trailing")
			}
			if mode == "syft-mixed-malformed-version" {
				fmt.Println("Version:not-a-version")
			}
			if mode == "syft-versioned-label" {
				fmt.Println("Versioned: 1.49.0")
			}
			return 0
		}
		if len(args) > 0 && args[0] == "scan" && !slicesContain(args, "--help") {
			creator := "syft-1.50.0"
			if mode == "syft-wrong-creator" {
				creator = "syft-1.49.0"
			}
			for _, argument := range args {
				if strings.HasPrefix(argument, "spdx-json=") {
					if err := os.WriteFile(strings.TrimPrefix(argument, "spdx-json="), []byte(`{"creator":"`+creator+`"}`), 0o600); err != nil { //nolint:gosec // Fixed fake-tool argv supplies a private fixture output path.
						return 85
					}
				}
			}
			if mode == "syft-replace-root-after-scan" {
				if err := replaceFixtureRootIdentity(root); err != nil {
					return 86
				}
			}
		}
		if mode == "syft-replace-config-at-syft" && reflect.DeepEqual(args, []string{"help"}) {
			if err := replaceFixtureConfigIdentity(root); err != nil {
				return 86
			}
		}
		return 0
	}
	return 2
}

func replaceFixtureConfigIdentity(root string) error {
	path := filepath.Join(root, "config", "syft.yaml")
	replaced := path + ".replaced"
	before, err := fixtureIdentityMetadata(path)
	if err != nil {
		return err
	}
	contents, err := os.ReadFile(path) //nolint:gosec // The path is a private fixture selected by the test harness.
	if err != nil {
		return err
	}
	if err := os.Rename(path, replaced); err != nil { //nolint:gosec // Both paths are fixed children of the private fixture root.
		return err
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil { //nolint:gosec // Recreates a private same-mode fixture file with a different inode.
		return err
	}
	if err := os.Chmod(path, 0o600); err != nil { //nolint:gosec // Enforces the fixture's original private mode.
		return err
	}
	if err := os.Remove(replaced); err != nil { //nolint:gosec // Removes only the fixed replaced fixture sibling.
		return err
	}
	after, err := fixtureIdentityMetadata(path)
	if err != nil {
		return err
	}
	return appendFixtureIdentityEvidence(root, "config", before, after)
}

func replaceFixtureRootIdentity(root string) error {
	replaced := root + ".replaced"
	before, err := fixtureIdentityMetadata(root)
	if err != nil {
		return err
	}
	if err := os.Rename(root, replaced); err != nil { //nolint:gosec // The root is the private fixture selected by the harness.
		return err
	}
	if err := os.Mkdir(root, 0o700); err != nil { //nolint:gosec // Recreates only the private fixture root at its retained path.
		return err
	}
	entries, err := os.ReadDir(replaced)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := os.Rename(filepath.Join(replaced, entry.Name()), filepath.Join(root, entry.Name())); err != nil { //nolint:gosec // Entries remain within the two private fixture roots.
			return err
		}
	}
	if err := os.Remove(replaced); err != nil { //nolint:gosec // Removes only the now-empty replaced fixture root.
		return err
	}
	after, err := fixtureIdentityMetadata(root)
	if err != nil {
		return err
	}
	return appendFixtureIdentityEvidence(root, "root", before, after)
}

func fixtureIdentityMetadata(path string) ([]string, error) {
	info, err := os.Lstat(path) //nolint:gosec // The caller passes only the private fixture root or its fixed config child.
	if err != nil {
		return nil, err
	}
	kind := ""
	switch {
	case info.Mode()&os.ModeSymlink != 0:
		return nil, errors.New("identity fixture target is a symlink")
	case info.IsDir():
		kind = "directory"
	case info.Mode().IsRegular():
		kind = "regular"
	default:
		return nil, errors.New("identity fixture target has unexpected type")
	}
	statValue := reflect.ValueOf(info.Sys())
	if statValue.Kind() == reflect.Pointer {
		statValue = statValue.Elem()
	}
	device, err := reflectedIntegerField(statValue, "Dev")
	if err != nil {
		return nil, err
	}
	inode, err := reflectedIntegerField(statValue, "Ino")
	if err != nil {
		return nil, err
	}
	owner, err := reflectedIntegerField(statValue, "Uid")
	if err != nil {
		return nil, err
	}
	return []string{device + ":" + inode, fmt.Sprintf("%04o", info.Mode().Perm()), owner, kind}, nil
}

func reflectedIntegerField(value reflect.Value, name string) (string, error) {
	if value.Kind() != reflect.Struct {
		return "", errors.New("fixture stat metadata is not a struct")
	}
	field := value.FieldByName(name)
	switch field.Kind() { //nolint:exhaustive // Only integer stat fields are valid; the default rejects every other kind.
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return strconv.FormatInt(field.Int(), 10), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return strconv.FormatUint(field.Uint(), 10), nil
	default:
		return "", fmt.Errorf("fixture stat metadata has no integer %s field", name)
	}
}

func appendFixtureIdentityEvidence(root, target string, before, after []string) error {
	if len(before) != 4 || len(after) != 4 {
		return errors.New("invalid identity evidence shape")
	}
	record := []string{"complete", target, before[0], after[0], before[1], after[1], before[2], after[2], before[3], after[3]}
	return appendFakeGHCall(root+".identity-evidence", record)
}

func decodedWorkflowStepRun(document []byte, jobName, stepName string) (string, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(document))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return "", err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return "", errors.New("workflow contains more than one YAML document")
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return "", errors.New("workflow is not one document")
	}
	if err := validateClosedYAMLNode(&root); err != nil {
		return "", err
	}
	jobs, err := yamlMappingValue(root.Content[0], "jobs")
	if err != nil {
		return "", err
	}
	job, err := yamlMappingValue(jobs, jobName)
	if err != nil {
		return "", err
	}
	steps, err := yamlMappingValue(job, "steps")
	if err != nil || steps.Kind != yaml.SequenceNode {
		return "", fmt.Errorf("job %s steps: %w", jobName, err)
	}
	matched := ""
	count := 0
	for _, step := range steps.Content {
		nameNode, nameErr := yamlMappingValue(step, "name")
		if nameErr != nil || nameNode.Value != stepName {
			continue
		}
		runNode, runErr := yamlMappingValue(step, "run")
		if runErr != nil || runNode.Kind != yaml.ScalarNode {
			return "", fmt.Errorf("step %s run: %w", stepName, runErr)
		}
		matched = runNode.Value
		count++
	}
	if count != 1 {
		return "", fmt.Errorf("decoded step %q count = %d", stepName, count)
	}
	return matched, nil
}

func yamlMappingValue(node *yaml.Node, key string) (*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return nil, errors.New("expected mapping")
	}
	for index := 0; index < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1], nil
		}
	}
	return nil, fmt.Errorf("missing key %q", key)
}

func assertBashSyntax(t *testing.T, name, script string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/bash", "-n") //nolint:gosec // Fixed shell validates decoded repository-owned source.
	command.Stdin = strings.NewReader(script)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s bash -n: %v: %s", name, err, output)
	}
}

func secureExternalFixtureRoot(t *testing.T, prefix string) string {
	t.Helper()
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		t.Fatalf("canonicalize external fixture base: %v", err)
	}
	root, err := os.MkdirTemp(base, prefix)
	if err != nil {
		t.Fatalf("create external fixture root: %v", err)
	}
	if err := os.Chmod(root, 0o700); err != nil { //nolint:gosec // This is a private fixture directory, not a data file.
		t.Fatalf("chmod external fixture root: %v", err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Errorf("remove external fixture root: %v", err)
		}
	})
	return root
}

func repositoryRootForTest(t *testing.T) string {
	t.Helper()
	root, err := repositoryScanRoot("")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	return root
}

func poisonedToolParentEnvironment(root, binRoot, poisonHelper string) []string {
	return []string{
		"PATH=" + binRoot + ":/usr/bin:/bin",
		"RUNNER_TEMP=" + root,
		"HOME=" + filepath.Join(root, "attacker-home"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "attacker-xdg"),
		"TMPDIR=" + filepath.Join(root, "attacker-tmp"),
		"GOBIN=" + filepath.Join(root, "attacker-bin"),
		"GOPATH=" + filepath.Join(root, "attacker-gopath"),
		"GOMODCACHE=" + filepath.Join(root, "attacker-modcache"),
		"GOCACHE=" + filepath.Join(root, "attacker-gocache"),
		"GOTOOLCHAIN=auto",
		"GOPROXY=https://attacker.invalid",
		"GOSUMDB=off",
		"GOPRIVATE=*",
		"GONOPROXY=*",
		"GONOSUMDB=*",
		"GOINSECURE=*",
		"GOENV=" + filepath.Join(root, "attacker-goenv"),
		"GOFLAGS=-toolexec=" + poisonHelper,
		"GOWORK=" + filepath.Join(root, "attacker.work"),
		"CGO_ENABLED=1",
		"LANG=attacker",
		"LC_ALL=attacker",
		"TZ=Pacific/Honolulu",
		"SYFT_CONFIG=" + filepath.Join(root, "attacker-syft.yaml"),
		"SYFT_CHECK_FOR_APP_UPDATE=true",
		"SYFT_SCOPE=all-layers",
	}
}

func cleanGoEnvironmentNames() []string {
	return []string{"HOME", "XDG_CONFIG_HOME", "GOBIN", "GOPATH", "GOMODCACHE", "GOCACHE", "TMPDIR", "GOTOOLCHAIN", "GOPROXY", "GOSUMDB", "GOPRIVATE", "GONOPROXY", "GONOSUMDB", "GOINSECURE", "GOENV", "GOFLAGS", "GOWORK", "CGO_ENABLED", "LANG", "LC_ALL", "TZ"}
}

func cleanActionlintEnvironmentNames() []string {
	return []string{"HOME", "XDG_CONFIG_HOME", "TMPDIR", "LANG", "LC_ALL", "TZ"}
}

func cleanSyftEnvironmentNames() []string {
	return []string{"HOME", "XDG_CONFIG_HOME", "TMPDIR", "LANG", "LC_ALL", "TZ", "SYFT_CONFIG", "SYFT_CHECK_FOR_APP_UPDATE"}
}

func writeCleanToolWrapper(t *testing.T, path, testBinary, mode string, environment []string) {
	t.Helper()
	if err := writeCleanToolWrapperFile(path, testBinary, mode, environment); err != nil {
		t.Fatalf("write %s wrapper: %v", mode, err)
	}
}

func writeCleanToolWrapperFile(path, testBinary, mode string, environment []string) error {
	buildExec := func(names []string) string {
		var builder strings.Builder
		builder.WriteString("exec /usr/bin/env -i \\\n")
		for _, name := range names {
			fmt.Fprintf(&builder, "  %s=\"${%s}\" \\\n", name, name)
		}
		fmt.Fprintf(&builder, "  %s -test.run '^TestWorkflowToolFake$' -- --mode %s --test-binary %s \"$@\"\n", shellSingleQuote(testBinary), shellSingleQuote(mode), shellSingleQuote(testBinary))
		return builder.String()
	}
	var script strings.Builder
	script.WriteString("#!/bin/sh\nset -eu\n")
	if strings.HasPrefix(mode, "syft") && !strings.HasPrefix(mode, "syft-go") {
		script.WriteString("if test \"${SYFT_COMPLIANCE_MISSING_NAME+x}\" = x || test \"${SYFT_COMPLIANCE_MISSING_VERSION+x}\" = x; then\n")
		names := append(append([]string{}, environment...), "SYFT_COMPLIANCE_MISSING_NAME", "SYFT_COMPLIANCE_MISSING_VERSION")
		script.WriteString(buildExec(names))
		script.WriteString("fi\n")
	}
	script.WriteString(buildExec(environment))
	if err := os.WriteFile(path, []byte(script.String()), 0o600); err != nil { //nolint:gosec // Caller supplies a private fixture path beneath a retained root.
		return err
	}
	return os.Chmod(path, 0o700) //nolint:gosec // Test-only fixture must be executable.
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func writePortableStatWrapper(t *testing.T, path string) {
	t.Helper()
	script := `#!/bin/sh
set -eu
if test "$(uname -s)" = Darwin; then
  test "$1" = -c
  format="$2"
  shift 2
  if test "${1:-}" = --; then shift; fi
  case "${format}" in
    %u) exec /usr/bin/stat -f '%u' "$1" ;;
    %a) exec /usr/bin/stat -f '%Lp' "$1" ;;
    %d:%i) exec /usr/bin/stat -f '%d:%i' "$1" ;;
    *) exit 2 ;;
  esac
fi
exec /usr/bin/stat "$@"
`
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil { //nolint:gosec // Caller supplies a private fixture path beneath a retained root.
		t.Fatalf("write portable stat: %v", err)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // Test-only fixture must be executable.
		t.Fatalf("chmod portable stat: %v", err)
	}
}

func writeFakeReleasepack(t *testing.T, path string) {
	t.Helper()
	script := `#!/bin/sh
set -eu
case "${1:-}" in
  sbom)
    shift
    raw=
    while test "$#" -gt 0; do
      if test "$1" = --raw-sbom; then raw="$2"; shift 2; continue; fi
      shift
    done
    test -n "${raw}"
    grep -F '"creator":"syft-1.50.0"' "${raw}" >/dev/null
    ;;
  checksums) ;;
  *) exit 2 ;;
esac
`
	if err := os.WriteFile(path, []byte(script), 0o600); err != nil { //nolint:gosec // Caller supplies a private fixture path beneath a retained root.
		t.Fatalf("write fake releasepack: %v", err)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // Test-only fixture must be executable.
		t.Fatalf("chmod fake releasepack: %v", err)
	}
}

func argumentsAfterDoubleDash(args []string) []string {
	for index, arg := range args {
		if arg == "--" {
			return args[index+1:]
		}
	}
	return nil
}

func appendEnvironmentRecord(path string) error {
	environment := append([]string(nil), os.Environ()...)
	slices.Sort(environment)
	return appendFakeGHCall(path, environment)
}

func readEnvironmentRecords(t *testing.T, path string) []map[string]string {
	t.Helper()
	records := readFakeGHCalls(t, path)
	result := make([]map[string]string, 0, len(records))
	for _, record := range records {
		values := make(map[string]string, len(record))
		for _, entry := range record {
			separator := strings.IndexByte(entry, '=')
			if separator < 1 {
				t.Fatalf("invalid environment entry %q", entry)
			}
			values[entry[:separator]] = entry[separator+1:]
		}
		result = append(result, values)
	}
	return result
}

func assertExactEnvironmentRecords(t *testing.T, path string, count int, want map[string]string) {
	t.Helper()
	records := readEnvironmentRecords(t, path)
	if len(records) != count {
		t.Fatalf("environment records in %s = %d, want %d", filepath.Base(path), len(records), count)
	}
	for index, got := range records {
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("environment record %d = %v, want %v", index, got, want)
		}
	}
}

func cleanGoEnvironment(root string) map[string]string {
	return map[string]string{
		"HOME": filepath.Join(root, "home"), "XDG_CONFIG_HOME": filepath.Join(root, "xdg"),
		"GOBIN": filepath.Join(root, "bin"), "GOPATH": filepath.Join(root, "gopath"),
		"GOMODCACHE": filepath.Join(root, "gomodcache"), "GOCACHE": filepath.Join(root, "gocache"),
		"TMPDIR": filepath.Join(root, "tmp"), "GOTOOLCHAIN": "local", "GOPROXY": "https://proxy.golang.org",
		"GOSUMDB": "sum.golang.org", "GOPRIVATE": "", "GONOPROXY": "", "GONOSUMDB": "", "GOINSECURE": "",
		"GOENV": "off", "GOFLAGS": "", "GOWORK": "off", "CGO_ENABLED": "0", "LANG": "C", "LC_ALL": "C", "TZ": "UTC",
	}
}

func cleanActionlintEnvironment(root string) map[string]string {
	return map[string]string{
		"HOME": filepath.Join(root, "home"), "XDG_CONFIG_HOME": filepath.Join(root, "xdg"),
		"TMPDIR": filepath.Join(root, "tmp"), "LANG": "C", "LC_ALL": "C", "TZ": "UTC",
	}
}

func cleanSyftEnvironment(root string, scan bool) map[string]string {
	result := map[string]string{
		"HOME": filepath.Join(root, "home"), "XDG_CONFIG_HOME": filepath.Join(root, "xdg"),
		"TMPDIR": filepath.Join(root, "tmp"), "LANG": "C", "LC_ALL": "C", "TZ": "UTC",
		"SYFT_CONFIG": filepath.Join(root, "config", "syft.yaml"), "SYFT_CHECK_FOR_APP_UPDATE": "false",
	}
	if scan {
		result["SYFT_COMPLIANCE_MISSING_NAME"] = "drop"
		result["SYFT_COMPLIANCE_MISSING_VERSION"] = "stub"
	}
	return result
}

func runReleasePublicationFakeGH(args []string) int {
	if err := appendFakeGHCall(os.Getenv("GH_LOG"), args); err != nil {
		return 91
	}
	if eventLog := os.Getenv("GH_EVENT_LOG"); eventLog != "" {
		event := append([]string{"gh"}, args...)
		if err := appendFakeGHCall(eventLog, event); err != nil {
			return 91
		}
	}
	if len(args) == 0 {
		return 2
	}
	fixture := os.Getenv("GH_FIXTURE")
	if args[0] == "--version" || slicesContain(args, "--help") {
		fmt.Println("gh fixture 2.99.0")
		return 0
	}
	if fixture == "attestation_success" && len(args) >= 2 && args[0] == "attestation" && args[1] == "verify" {
		return 0
	}
	if len(args) >= 2 && args[0] == "api" && args[1] == "graphql" {
		switch fixture {
		case "graphql_existing":
			fmt.Print(`{"data":{"repository":{"release":{"id":"R_1"}}}}`)
		case "graphql_errors":
			fmt.Print(`{"errors":[{"message":"fixture"}],"data":{"repository":{"release":null}}}`)
		case "graphql_missing_release":
			fmt.Print(`{"data":{"repository":{}}}`)
		case "graphql_wrong_release_type":
			fmt.Print(`{"data":{"repository":{"release":[]}}}`)
		case "graphql_empty_id":
			fmt.Print(`{"data":{"repository":{"release":{"id":""}}}}`)
		case "graphql_malformed":
			fmt.Print(`{"data":`)
		case "graphql_gh_fail":
			return 1
		default:
			fmt.Print(`{"data":{"repository":{"release":null}}}`)
		}
		return 0
	}
	if args[0] == "api" {
		endpoint := args[len(args)-1]
		if strings.Contains(endpoint, "/git/ref/tags/") {
			phase, err := incrementFixtureCounter(os.Getenv("GH_REF_COUNT"))
			if err != nil {
				return 92
			}
			failure := fixtureFailureForPhase(fixture, "ref", phase)
			switch failure {
			case "gh_fail":
				return 1
			case "malformed":
				fmt.Print(`{"ref":`)
			case "missing_ref":
				fmt.Print(`{"object":{"type":"commit","sha":"` + strings.Repeat("a", 40) + `"}}`)
			case "wrong_ref":
				fmt.Print(`{"ref":"refs/tags/v9.9.9","object":{"type":"commit","sha":"` + strings.Repeat("a", 40) + `"}}`)
			case "missing_object":
				fmt.Print(`{"ref":"refs/tags/v0.1.0"}`)
			case "wrong_object_type":
				fmt.Print(`{"ref":"refs/tags/v0.1.0","object":{"type":"tree","sha":"` + strings.Repeat("a", 40) + `"}}`)
			case "wrong_object_sha":
				fmt.Print(`{"ref":"refs/tags/v0.1.0","object":{"type":"commit","sha":"BAD"}}`)
			default:
				commit := strings.Repeat("a", 40)
				if fixture == fmt.Sprintf("commit_p%d_mismatch", phase) {
					commit = strings.Repeat("c", 40)
				}
				if strings.HasPrefix(fixture, "tag_") || fixture == "annotated_success" {
					fmt.Print(`{"ref":"refs/tags/v0.1.0","object":{"type":"tag","sha":"` + strings.Repeat("b", 40) + `"}}`)
				} else {
					fmt.Print(`{"ref":"refs/tags/v0.1.0","object":{"type":"commit","sha":"` + commit + `"}}`)
				}
			}
			return 0
		}
		if strings.Contains(endpoint, "/git/tags/") {
			phase, err := incrementFixtureCounter(os.Getenv("GH_TAG_COUNT"))
			if err != nil {
				return 93
			}
			failure := fixtureFailureForPhase(fixture, "tag", phase)
			tagSHA := strings.Repeat("b", 40)
			commitSHA := strings.Repeat("a", 40)
			switch failure {
			case "gh_fail":
				return 1
			case "malformed":
				fmt.Print(`{"sha":`)
			case "missing_top_sha":
				fmt.Print(`{"object":{"type":"commit","sha":"` + commitSHA + `"}}`)
			case "wrong_top_sha", "requested_sha_mismatch":
				fmt.Print(`{"sha":"` + strings.Repeat("d", 40) + `","object":{"type":"commit","sha":"` + commitSHA + `"}}`)
			case "missing_target":
				fmt.Print(`{"sha":"` + tagSHA + `"}`)
			case "wrong_target_type", "nested_tag":
				fmt.Print(`{"sha":"` + tagSHA + `","object":{"type":"tag","sha":"` + commitSHA + `"}}`)
			case "wrong_target_sha":
				fmt.Print(`{"sha":"` + tagSHA + `","object":{"type":"commit","sha":"BAD"}}`)
			default:
				fmt.Print(`{"sha":"` + tagSHA + `","object":{"type":"commit","sha":"` + commitSHA + `"}}`)
			}
			return 0
		}
	}
	if len(args) >= 2 && args[0] == "release" && (args[1] == "create" || args[1] == "edit") {
		return 0
	}
	return 2
}

func fixtureFailureForPhase(fixture, endpoint string, phase int) string {
	prefix := fmt.Sprintf("%s_p%d_", endpoint, phase)
	return strings.TrimPrefix(strings.TrimPrefix(fixture, prefix), fixture)
}

func slicesContain(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestNULArgumentRecordRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "argv.log")
	want := [][]string{
		{},
		{""},
		{"scan", "", "line one\nline two", "tab\tvalue"},
	}
	for _, record := range want {
		if err := appendFakeGHCall(path, record); err != nil {
			t.Fatalf("append NUL argument record: %v", err)
		}
	}
	if got := readFakeGHCalls(t, path); !reflect.DeepEqual(got, want) {
		t.Fatalf("NUL argument round trip = %#v, want %#v", got, want)
	}
}

func appendFakeGHCall(path string, args []string) error {
	for _, arg := range args {
		if strings.IndexByte(arg, 0) >= 0 {
			return errors.New("argument record contains NUL")
		}
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600) //nolint:gosec // Caller supplies a private fixture log path.
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if _, err := file.Write(append([]byte(strconv.Itoa(len(args))), 0)); err != nil {
		return err
	}
	for _, arg := range args {
		if _, err := file.Write(append([]byte(arg), 0)); err != nil {
			return err
		}
	}
	return nil
}

func readFakeGHCalls(t *testing.T, path string) [][]string {
	t.Helper()
	contents, err := os.ReadFile(path) //nolint:gosec // Caller supplies a private fixture log path.
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		t.Fatalf("read fake gh log: %v", err)
	}
	parts := bytes.Split(contents, []byte{0})
	if len(parts) == 0 || len(parts[len(parts)-1]) != 0 {
		t.Fatal("NUL argument log is missing its final field terminator")
	}
	parts = parts[:len(parts)-1]
	result := make([][]string, 0)
	for index := 0; index < len(parts); {
		count, parseErr := strconv.Atoi(string(parts[index]))
		index++
		if parseErr != nil || count < 0 || count > len(parts)-index {
			t.Fatalf("invalid argc-prefixed NUL argument log at field %d", index-1)
		}
		call := make([]string, count)
		for argument := 0; argument < count; argument++ {
			call[argument] = string(parts[index+argument])
		}
		index += count
		result = append(result, call)
	}
	return result
}

func assertSuccessfulPublicationTrace(t *testing.T, root, fixture string, calls [][]string) {
	t.Helper()
	wantQuery := "query($owner:String!,$name:String!,$tag:String!){\n  repository(owner:$owner,name:$name){\n    release(tagName:$tag){id}\n  }\n}"
	wantPrefix := [][]string{
		{"--version"},
		{"api", "--help"},
		{"release", "create", "--help"},
		{"release", "edit", "--help"},
		{"api", "graphql", "-f", "query=" + wantQuery, "-F", "owner=krkarma777", "-F", "name=ai-cli-gateway", "-F", "tag=v0.1.0"},
	}
	if len(calls) < len(wantPrefix) || !reflect.DeepEqual(calls[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("publication preflight/GraphQL calls = %q, want exact prefix %q", calls, wantPrefix)
	}
	refCall := func() []string {
		return []string{"api", "-H", "Accept: application/vnd.github+json", "-H", "X-GitHub-Api-Version: 2026-03-10", "repos/krkarma777/ai-cli-gateway/git/ref/tags/v0.1.0"}
	}
	tagCall := func() []string {
		return []string{"api", "-H", "Accept: application/vnd.github+json", "-H", "X-GitHub-Api-Version: 2026-03-10", "repos/krkarma777/ai-cli-gateway/git/tags/" + strings.Repeat("b", 40)}
	}
	assetRoot := filepath.Join(root, "release-assets")
	create := []string{
		"release", "create", "v0.1.0", "--repo", "krkarma777/ai-cli-gateway", "--title", "v0.1.0", "--verify-tag", "--draft", "--generate-notes",
		filepath.Join(assetRoot, "ai-cli-gateway_0.1.0_linux_amd64.tar.gz"),
		filepath.Join(assetRoot, "ai-cli-gateway_0.1.0_linux_arm64.tar.gz"),
		filepath.Join(assetRoot, "ai-cli-gateway_0.1.0_darwin_amd64.tar.gz"),
		filepath.Join(assetRoot, "ai-cli-gateway_0.1.0_darwin_arm64.tar.gz"),
		filepath.Join(assetRoot, "ai-cli-gateway_0.1.0_windows_amd64.zip"),
		filepath.Join(assetRoot, "ai-cli-gateway_0.1.0_sbom.spdx.json"),
		filepath.Join(assetRoot, "SHA256SUMS"),
	}
	edit := []string{"release", "edit", "v0.1.0", "--repo", "krkarma777/ai-cli-gateway", "--draft=false"}
	wantTail := [][]string{refCall(), create, refCall(), edit}
	if fixture == "annotated_success" {
		wantTail = [][]string{refCall(), tagCall(), create, refCall(), tagCall(), edit}
	}
	wantCalls := append(append([][]string{}, wantPrefix...), wantTail...)
	if !reflect.DeepEqual(calls, wantCalls) {
		t.Fatalf("successful publication call order = %q, want exact %q", calls, wantCalls)
	}
	events := readFakeGHCalls(t, filepath.Join(root, "events.log"))
	checksumIndex, createIndex, editIndex := -1, -1, -1
	for index, event := range events {
		if reflect.DeepEqual(event, []string{"sha256sum", "--check", "--strict", "SHA256SUMS"}) {
			checksumIndex = index
		}
		if len(event) >= 3 && event[0] == "gh" && event[1] == "release" && event[2] == "create" && !slicesContain(event, "--help") {
			createIndex = index
		}
		if len(event) >= 3 && event[0] == "gh" && event[1] == "release" && event[2] == "edit" && !slicesContain(event, "--help") {
			editIndex = index
		}
	}
	if checksumIndex < 0 || createIndex <= checksumIndex || editIndex <= createIndex {
		t.Fatalf("publication events do not prove checksum -> draft -> public order: %q", events)
	}
}

func incrementFixtureCounter(path string) (int, error) {
	value := 0
	contents, err := os.ReadFile(path) //nolint:gosec // Caller supplies a private fixture counter path.
	if err == nil {
		value, err = strconv.Atoi(strings.TrimSpace(string(contents)))
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, err
	}
	value++
	if err := os.WriteFile(path, []byte(strconv.Itoa(value)), 0o600); err != nil { //nolint:gosec // Caller supplies a private fixture counter path.
		return 0, err
	}
	return value, nil
}

func linkFixtureTool(t *testing.T, root, name, target string) {
	t.Helper()
	if err := os.Symlink(target, filepath.Join(root, name)); err != nil {
		t.Fatalf("link %s fixture: %v", name, err)
	}
}

func writeReleasePublicationAssets(t *testing.T, root string, badDigest bool) {
	t.Helper()
	assets := []string{
		"ai-cli-gateway_0.1.0_linux_amd64.tar.gz",
		"ai-cli-gateway_0.1.0_linux_arm64.tar.gz",
		"ai-cli-gateway_0.1.0_darwin_amd64.tar.gz",
		"ai-cli-gateway_0.1.0_darwin_arm64.tar.gz",
		"ai-cli-gateway_0.1.0_windows_amd64.zip",
		"ai-cli-gateway_0.1.0_sbom.spdx.json",
	}
	manifest := strings.Builder{}
	for index, name := range assets {
		contents := []byte(fmt.Sprintf("fixture-%d\n", index))
		writeFixtureFile(t, root, name, contents)
		sum := sha256.Sum256(contents)
		if badDigest && index == 0 {
			sum = sha256.Sum256([]byte("wrong"))
		}
		fmt.Fprintf(&manifest, "%x  %s\n", sum, name)
	}
	writeFixtureFile(t, root, "SHA256SUMS", []byte(manifest.String()))
}

func parseClosedReleaseWorkflow(document []byte) (releaseWorkflowDocument, error) {
	decoder := yaml.NewDecoder(bytes.NewReader(document))
	var root yaml.Node
	if err := decoder.Decode(&root); err != nil {
		return releaseWorkflowDocument{}, fmt.Errorf("decode document: %w", err)
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return releaseWorkflowDocument{}, errors.New("multiple YAML documents")
		}
		return releaseWorkflowDocument{}, fmt.Errorf("decode trailing document: %w", err)
	}
	if root.Kind != yaml.DocumentNode || len(root.Content) != 1 {
		return releaseWorkflowDocument{}, errors.New("workflow must be one document node")
	}
	if root.Content[0].Kind != yaml.MappingNode || root.Content[0].Style&yaml.FlowStyle != 0 {
		return releaseWorkflowDocument{}, errors.New("workflow root must be one block mapping")
	}
	if err := validateClosedYAMLNode(&root); err != nil {
		return releaseWorkflowDocument{}, err
	}
	top, err := closedYAMLMapping(root.Content[0], "name", "on", "concurrency", "jobs")
	if err != nil {
		return releaseWorkflowDocument{}, fmt.Errorf("top level: %w", err)
	}
	if value, scalarErr := closedYAMLScalar(top["name"]); scalarErr != nil || value != "Release" {
		return releaseWorkflowDocument{}, fmt.Errorf("name must be Release: %w", scalarErr)
	}
	if err := validateReleaseTrigger(top["on"]); err != nil {
		return releaseWorkflowDocument{}, err
	}
	if err := validateReleaseConcurrency(top["concurrency"]); err != nil {
		return releaseWorkflowDocument{}, err
	}
	jobNodes, err := closedYAMLMapping(top["jobs"], "verify", "package", "asset-verification", "publish")
	if err != nil {
		return releaseWorkflowDocument{}, fmt.Errorf("jobs: %w", err)
	}
	jobs := make(map[string]releaseWorkflowJob, len(jobNodes))
	for name, node := range jobNodes {
		job, parseErr := parseReleaseWorkflowJob(name, node)
		if parseErr != nil {
			return releaseWorkflowDocument{}, fmt.Errorf("job %s: %w", name, parseErr)
		}
		jobs[name] = job
	}
	return releaseWorkflowDocument{Root: &root, Jobs: jobs}, nil
}

func validateClosedYAMLNode(node *yaml.Node) error {
	if node == nil {
		return errors.New("nil YAML node")
	}
	if node.Anchor != "" || node.Alias != nil || node.Kind == yaml.AliasNode {
		return errors.New("anchors and aliases are forbidden")
	}
	if node.Style&yaml.TaggedStyle != 0 {
		return errors.New("explicit YAML tags are forbidden")
	}
	if (node.Kind == yaml.MappingNode || node.Kind == yaml.SequenceNode) && node.Style&yaml.FlowStyle != 0 {
		return errors.New("flow-style collections are forbidden")
	}
	switch node.Kind {
	case yaml.DocumentNode:
		if node.ShortTag() != "!!null" && node.Tag != "" {
			return fmt.Errorf("unsupported document tag %q", node.Tag)
		}
	case yaml.MappingNode:
		if node.ShortTag() != "!!map" {
			return fmt.Errorf("mapping tag = %q", node.ShortTag())
		}
		if len(node.Content)%2 != 0 {
			return errors.New("mapping has an odd child count")
		}
		seen := make(map[string]struct{}, len(node.Content)/2)
		for index := 0; index < len(node.Content); index += 2 {
			key := node.Content[index]
			if key.Kind != yaml.ScalarNode || key.ShortTag() != "!!str" || key.Style&yaml.TaggedStyle != 0 {
				return errors.New("mapping key must be an implicitly tagged string scalar")
			}
			if key.Value == "<<" {
				return errors.New("merge keys are forbidden")
			}
			if _, duplicate := seen[key.Value]; duplicate {
				return fmt.Errorf("duplicate mapping key %q", key.Value)
			}
			seen[key.Value] = struct{}{}
		}
	case yaml.SequenceNode:
		if node.ShortTag() != "!!seq" {
			return fmt.Errorf("sequence tag = %q", node.ShortTag())
		}
	case yaml.ScalarNode:
		switch node.ShortTag() {
		case "!!str", "!!int", "!!bool", "!!null", "!!float":
		default:
			return fmt.Errorf("unsupported implicit scalar tag %q", node.ShortTag())
		}
	case yaml.AliasNode:
		return errors.New("YAML aliases are forbidden")
	default:
		return fmt.Errorf("unsupported YAML node kind %d", node.Kind)
	}
	for _, child := range node.Content {
		if err := validateClosedYAMLNode(child); err != nil {
			return err
		}
	}
	return nil
}

func closedYAMLMapping(node *yaml.Node, allowed ...string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return nil, errors.New("expected block mapping")
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	result := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index].Value
		if _, ok := allowedSet[key]; !ok {
			return nil, fmt.Errorf("unknown field %q", key)
		}
		result[key] = node.Content[index+1]
	}
	if len(result) != len(allowedSet) {
		missing := make([]string, 0)
		for _, key := range allowed {
			if _, ok := result[key]; !ok {
				missing = append(missing, key)
			}
		}
		return nil, fmt.Errorf("missing fields %v", missing)
	}
	return result, nil
}

func closedYAMLScalar(node *yaml.Node) (string, error) {
	if node == nil || node.Kind != yaml.ScalarNode {
		return "", errors.New("expected scalar")
	}
	return node.Value, nil
}

func closedYAMLScalarMap(node *yaml.Node, allowed ...string) (map[string]string, error) {
	mapping, err := closedYAMLMapping(node, allowed...)
	if err != nil {
		return nil, err
	}
	result := make(map[string]string, len(mapping))
	for key, valueNode := range mapping {
		value, scalarErr := closedYAMLScalar(valueNode)
		if scalarErr != nil {
			return nil, fmt.Errorf("%s: %w", key, scalarErr)
		}
		result[key] = value
	}
	return result, nil
}

func validateReleaseTrigger(node *yaml.Node) error {
	trigger, err := closedYAMLMapping(node, "push")
	if err != nil {
		return fmt.Errorf("trigger: %w", err)
	}
	push, err := closedYAMLMapping(trigger["push"], "tags")
	if err != nil {
		return fmt.Errorf("push trigger: %w", err)
	}
	tags := push["tags"]
	if tags.Kind != yaml.SequenceNode || len(tags.Content) != 1 {
		return errors.New("release trigger must have one tag pattern")
	}
	value, err := closedYAMLScalar(tags.Content[0])
	if err != nil || value != "v*.*.*" {
		return errors.New("release trigger must be v*.*.*")
	}
	return nil
}

func validateReleaseConcurrency(node *yaml.Node) error {
	concurrency, err := closedYAMLScalarMap(node, "group", "cancel-in-progress")
	if err != nil {
		return fmt.Errorf("concurrency: %w", err)
	}
	want := map[string]string{
		"group":              "release-${{ github.repository }}-${{ github.ref_name }}",
		"cancel-in-progress": "false",
	}
	if !reflect.DeepEqual(concurrency, want) {
		return fmt.Errorf("concurrency = %v, want %v", concurrency, want)
	}
	return nil
}

func parseReleaseWorkflowJob(name string, node *yaml.Node) (releaseWorkflowJob, error) {
	var allowed []string
	switch name {
	case "verify":
		allowed = []string{"uses", "permissions"}
	case "package", "asset-verification", "publish":
		allowed = []string{"needs", "runs-on", "timeout-minutes", "permissions", "steps"}
		if name == "package" {
			allowed = append(allowed, "outputs")
		}
	default:
		return releaseWorkflowJob{}, fmt.Errorf("unexpected job %q", name)
	}
	fields, err := closedYAMLMapping(node, allowed...)
	if err != nil {
		return releaseWorkflowJob{}, err
	}
	job := releaseWorkflowJob{}
	if name == "verify" {
		job.Uses, err = closedYAMLScalar(fields["uses"])
		if err != nil {
			return releaseWorkflowJob{}, err
		}
		job.Permissions, err = closedYAMLScalarMap(fields["permissions"], "contents")
		return job, err
	}
	job.RunsOn, err = closedYAMLScalar(fields["runs-on"])
	if err != nil {
		return releaseWorkflowJob{}, err
	}
	job.Timeout, err = closedYAMLScalar(fields["timeout-minutes"])
	if err != nil {
		return releaseWorkflowJob{}, err
	}
	job.Needs, err = closedYAMLStringOrSequence(fields["needs"])
	if err != nil {
		return releaseWorkflowJob{}, err
	}
	permissionKeys := map[string][]string{
		"package":            {"contents", "id-token", "attestations"},
		"asset-verification": {"contents", "attestations"},
		"publish":            {"contents"},
	}
	job.Permissions, err = closedYAMLScalarMap(fields["permissions"], permissionKeys[name]...)
	if err != nil {
		return releaseWorkflowJob{}, err
	}
	if name == "package" {
		job.Outputs, err = closedYAMLScalarMap(fields["outputs"], "tag", "version", "tag_commit", "source_epoch", "artifact_id", "artifact_digest")
		if err != nil {
			return releaseWorkflowJob{}, err
		}
	}
	job.Steps, err = parseReleaseWorkflowSteps(fields["steps"])
	return job, err
}

func closedYAMLStringOrSequence(node *yaml.Node) ([]string, error) {
	if node.Kind == yaml.ScalarNode {
		value, err := closedYAMLScalar(node)
		return []string{value}, err
	}
	if node.Kind != yaml.SequenceNode || len(node.Content) == 0 {
		return nil, errors.New("needs must be a nonempty scalar or block sequence")
	}
	result := make([]string, 0, len(node.Content))
	for _, item := range node.Content {
		value, err := closedYAMLScalar(item)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}

func parseReleaseWorkflowSteps(node *yaml.Node) ([]releaseWorkflowStep, error) {
	if node.Kind != yaml.SequenceNode || len(node.Content) == 0 {
		return nil, errors.New("steps must be a nonempty block sequence")
	}
	steps := make([]releaseWorkflowStep, 0, len(node.Content))
	for index, stepNode := range node.Content {
		fields, err := closedYAMLMappingSubset(stepNode, "name", "id", "uses", "with", "env", "shell", "run")
		if err != nil {
			return nil, fmt.Errorf("step %d: %w", index, err)
		}
		step := releaseWorkflowStep{}
		for key, target := range map[string]*string{"name": &step.Name, "id": &step.ID, "uses": &step.Uses, "shell": &step.Shell, "run": &step.Run} {
			if valueNode, ok := fields[key]; ok {
				value, scalarErr := closedYAMLScalar(valueNode)
				if scalarErr != nil {
					return nil, fmt.Errorf("step %d %s: %w", index, key, scalarErr)
				}
				*target = value
			}
		}
		if (step.Uses == "") == (step.Run == "") {
			return nil, fmt.Errorf("step %d must have exactly one of uses or run", index)
		}
		if node, ok := fields["with"]; ok {
			step.With, err = closedYAMLScalarMapSubset(node)
			if err != nil {
				return nil, fmt.Errorf("step %d with: %w", index, err)
			}
		}
		if node, ok := fields["env"]; ok {
			step.Env, err = closedYAMLScalarMapSubset(node)
			if err != nil {
				return nil, fmt.Errorf("step %d env: %w", index, err)
			}
		}
		steps = append(steps, step)
	}
	return steps, nil
}

func closedYAMLMappingSubset(node *yaml.Node, allowed ...string) (map[string]*yaml.Node, error) {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return nil, errors.New("expected block mapping")
	}
	allowedSet := make(map[string]struct{}, len(allowed))
	for _, key := range allowed {
		allowedSet[key] = struct{}{}
	}
	result := make(map[string]*yaml.Node, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		key := node.Content[index].Value
		if _, ok := allowedSet[key]; !ok {
			return nil, fmt.Errorf("unknown field %q", key)
		}
		result[key] = node.Content[index+1]
	}
	return result, nil
}

func closedYAMLScalarMapSubset(node *yaml.Node) (map[string]string, error) {
	if node == nil || node.Kind != yaml.MappingNode || len(node.Content)%2 != 0 {
		return nil, errors.New("expected block mapping")
	}
	result := make(map[string]string, len(node.Content)/2)
	for index := 0; index < len(node.Content); index += 2 {
		value, err := closedYAMLScalar(node.Content[index+1])
		if err != nil {
			return nil, err
		}
		result[node.Content[index].Value] = value
	}
	return result, nil
}

func validateReleaseWorkflowContract(workflow releaseWorkflowDocument) error {
	verify := workflow.Jobs["verify"]
	if verify.Uses != "./.github/workflows/ci.yml" || !reflect.DeepEqual(verify.Permissions, map[string]string{"contents": "read"}) {
		return fmt.Errorf("verify job is not the read-only reusable CI call: %+v", verify)
	}
	packageJob := workflow.Jobs["package"]
	assetJob := workflow.Jobs["asset-verification"]
	publishJob := workflow.Jobs["publish"]
	if !reflect.DeepEqual(packageJob.Needs, []string{"verify"}) || !reflect.DeepEqual(assetJob.Needs, []string{"package"}) || !reflect.DeepEqual(publishJob.Needs, []string{"package", "asset-verification"}) {
		return fmt.Errorf("job dependency graph is not exact: package=%v asset=%v publish=%v", packageJob.Needs, assetJob.Needs, publishJob.Needs)
	}
	if packageJob.RunsOn != "ubuntu-24.04" || packageJob.Timeout != "25" ||
		assetJob.RunsOn != "ubuntu-24.04" || assetJob.Timeout != "10" ||
		publishJob.RunsOn != "ubuntu-24.04" || publishJob.Timeout != "10" {
		return errors.New("release runners or timeouts differ from the closed contract")
	}
	if !reflect.DeepEqual(packageJob.Permissions, map[string]string{"contents": "read", "id-token": "write", "attestations": "write"}) ||
		!reflect.DeepEqual(assetJob.Permissions, map[string]string{"contents": "read", "attestations": "read"}) ||
		!reflect.DeepEqual(publishJob.Permissions, map[string]string{"contents": "write"}) {
		return errors.New("release split-authority permissions differ from the closed contract")
	}
	wantOutputs := map[string]string{
		"tag":             "${{ steps.metadata.outputs.tag }}",
		"version":         "${{ steps.metadata.outputs.version }}",
		"tag_commit":      "${{ steps.metadata.outputs.tag_commit }}",
		"source_epoch":    "${{ steps.metadata.outputs.source_epoch }}",
		"artifact_id":     "${{ steps.artifact-metadata.outputs.artifact_id }}",
		"artifact_digest": "${{ steps.artifact-metadata.outputs.artifact_digest }}",
	}
	if !reflect.DeepEqual(packageJob.Outputs, wantOutputs) {
		return fmt.Errorf("package outputs = %v, want validated outputs %v", packageJob.Outputs, wantOutputs)
	}
	if err := validateReleaseActions(workflow.Jobs); err != nil {
		return err
	}
	if err := validateReleaseSteps(packageJob, assetJob, publishJob); err != nil {
		return err
	}
	return nil
}

func validateReleaseActions(jobs map[string]releaseWorkflowJob) error {
	allowed := map[string]int{
		checkoutAction: 1, setupGoAction: 1, attestAction: 1, uploadArtifactAction: 1, downloadArtifactAction: 2,
	}
	got := make(map[string]int)
	for name, job := range jobs {
		for _, step := range job.Steps {
			if step.Uses == "" {
				continue
			}
			if _, ok := allowed[step.Uses]; !ok {
				return fmt.Errorf("job %s uses unlisted action %q", name, step.Uses)
			}
			got[step.Uses]++
		}
	}
	if !reflect.DeepEqual(got, allowed) {
		return fmt.Errorf("release actions = %v, want exact %v", got, allowed)
	}
	packageSteps := jobs["package"].Steps
	if !reflect.DeepEqual(packageSteps[0].With, map[string]string{"persist-credentials": "false", "fetch-depth": "0"}) {
		return fmt.Errorf("checkout inputs = %v", packageSteps[0].With)
	}
	if !reflect.DeepEqual(packageSteps[1].With, map[string]string{"go-version": "1.26.5", "cache": "true"}) {
		return fmt.Errorf("setup-go inputs = %v", packageSteps[1].With)
	}
	if !exactStringMapKeys(packageSteps[6].With, "subject-path") {
		return fmt.Errorf("attest inputs = %v", packageSteps[6].With)
	}
	if !exactStringMapKeys(packageSteps[7].With, "name", "path", "overwrite", "if-no-files-found", "include-hidden-files", "compression-level", "retention-days") {
		return fmt.Errorf("upload inputs = %v", packageSteps[7].With)
	}
	return nil
}

func validateReleaseSteps(packageJob, assetJob, publishJob releaseWorkflowJob) error {
	if len(packageJob.Steps) != 9 || len(assetJob.Steps) != 2 || len(publishJob.Steps) != 3 {
		return fmt.Errorf("step counts package=%d asset=%d publish=%d", len(packageJob.Steps), len(assetJob.Steps), len(publishJob.Steps))
	}
	if err := validateExactReleaseStepShapes(packageJob, assetJob, publishJob); err != nil {
		return err
	}
	metadata, err := namedReleaseStep(packageJob.Steps, "Validate release metadata")
	if err != nil {
		return err
	}
	if metadata.ID != "metadata" || metadata.Shell != "bash" || !reflect.DeepEqual(metadata.Env, map[string]string{
		"EVENT_REF": "${{ github.ref }}", "EVENT_TAG": "${{ github.ref_name }}", "EVENT_SHA": "${{ github.sha }}",
	}) {
		return errors.New("package metadata step fields are not exact")
	}
	for _, token := range []string{
		`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`,
		`refs/tags/${EVENT_TAG}`, `git rev-parse "${EVENT_TAG}^{commit}"`,
		`+refs/heads/main:refs/remotes/origin/main`, `git merge-base --is-ancestor`,
		`^[0-9a-f]{40}$`, `^[0-9]+$`, `tag_commit=`, `source_epoch=`,
	} {
		if !strings.Contains(shellWithoutCommentOnlyLines(metadata.Run), token) {
			return fmt.Errorf("metadata run is missing %q", token)
		}
	}
	artifact, err := namedReleaseStep(packageJob.Steps, "Validate artifact outputs")
	if err != nil {
		return err
	}
	if artifact.ID != "artifact-metadata" || !reflect.DeepEqual(artifact.Env, map[string]string{
		"RAW_ARTIFACT_ID": "${{ steps.upload.outputs.artifact-id }}", "RAW_ARTIFACT_DIGEST": "${{ steps.upload.outputs.artifact-digest }}",
	}) || !strings.Contains(artifact.Run, `^[0-9]+$`) || !strings.Contains(artifact.Run, `^[0-9a-f]{64}$`) {
		return errors.New("artifact output validator is not exact")
	}
	syftStep, err := namedReleaseStep(packageJob.Steps, "Generate SBOM and checksums")
	if err != nil {
		return err
	}
	if err := validateReleaseSyftStep(syftStep); err != nil {
		return fmt.Errorf("Syft step: %w", err)
	}
	for _, job := range []releaseWorkflowJob{packageJob, assetJob, publishJob} {
		for _, step := range job.Steps {
			if strings.Contains(step.Run, "${{ github.") || strings.Contains(step.Run, "${{ needs.") || strings.Contains(step.Run, "${{ runner.temp }}") {
				return fmt.Errorf("step %q embeds a GitHub expression in decoded shell", step.Name)
			}
			for _, forbidden := range []string{"curl ", "wget ", "--clobber", "--overwrite", "--resume", "release delete"} {
				if strings.Contains(strings.ToLower(step.Run), forbidden) {
					return fmt.Errorf("step %q contains forbidden behavior %q", step.Name, forbidden)
				}
			}
		}
	}
	if err := validateArtifactTransfer(packageJob, assetJob, publishJob); err != nil {
		return err
	}
	assetVerification, err := namedReleaseStep(assetJob.Steps, "Verify checksums and attestations")
	if err != nil {
		return err
	}
	if err := validateReleaseAssetVerificationStep(assetVerification); err != nil {
		return fmt.Errorf("asset verification step: %w", err)
	}
	publication, err := namedReleaseStep(publishJob.Steps, "Publish verified release")
	if err != nil {
		return err
	}
	if publication.Shell != "bash" || publication.Env["GH_TOKEN"] != "${{ github.token }}" ||
		publication.Env["TAG"] != "${{ needs.package.outputs.tag }}" ||
		publication.Env["TAG_COMMIT"] != "${{ needs.package.outputs.tag_commit }}" {
		return errors.New("publication environment is not exact")
	}
	for _, token := range []string{
		`query($owner:String!,$name:String!,$tag:String!){`,
		`repository(owner:$owner,name:$name){`,
		`release(tagName:$tag){id}`,
		`resolve_live_tag()`,
		`repos/krkarma777/ai-cli-gateway/git/ref/tags/${TAG}`,
		`repos/krkarma777/ai-cli-gateway/git/tags/${object_sha}`,
		`Accept: application/vnd.github+json`,
		`X-GitHub-Api-Version: 2026-03-10`,
		`gh release create`, `--verify-tag`, `--draft`, `--generate-notes`,
		`gh release edit`, `--draft=false`, `release_already_exists`, `release_preflight_invalid`,
	} {
		if !strings.Contains(shellWithoutCommentOnlyLines(publication.Run), token) {
			return fmt.Errorf("publication run is missing %q", token)
		}
	}
	if strings.Count(publication.Run, "resolve_live_tag") != 3 {
		return errors.New("publication must define one resolver and call it exactly twice")
	}
	return nil
}

func validateReleaseAssetVerificationStep(step releaseWorkflowStep) error {
	if step.Run != expectedReleaseAssetVerificationRun() {
		return errors.New("decoded shell differs from the exact checksum and attestation contract")
	}
	return nil
}

func expectedReleaseAssetVerificationRun() string {
	return `set -euo pipefail
[[ "${TAG}" =~ ^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
[[ "${VERSION}" =~ ^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$ ]]
[[ "${TAG_COMMIT}" =~ ^[0-9a-f]{40}$ ]]
command -v gh >/dev/null
command -v sha256sum >/dev/null
gh --version >/dev/null
gh attestation verify --help >/dev/null
cd "${RUNNER_TEMP}/release-assets"
sha256sum --check --strict SHA256SUMS
while IFS= read -r asset; do
  test -f "${asset}"
  gh attestation verify "${asset}" \
    --repo krkarma777/ai-cli-gateway \
    --predicate-type https://slsa.dev/provenance/v1 \
    --signer-workflow github.com/krkarma777/ai-cli-gateway/.github/workflows/release.yml \
    --source-digest "${TAG_COMMIT}" \
    --source-ref "refs/tags/${TAG}"
done <<ASSETS
ai-cli-gateway_${VERSION}_linux_amd64.tar.gz
ai-cli-gateway_${VERSION}_linux_arm64.tar.gz
ai-cli-gateway_${VERSION}_darwin_amd64.tar.gz
ai-cli-gateway_${VERSION}_darwin_arm64.tar.gz
ai-cli-gateway_${VERSION}_windows_amd64.zip
ai-cli-gateway_${VERSION}_sbom.spdx.json
SHA256SUMS
ASSETS
`
}

func validateReleaseSyftStep(step releaseWorkflowStep) error {
	script := shellWithoutCommentOnlyLines(step.Run)
	if err := requireExactTextSHA256("decoded Syft step", step.Run, "f178500f46c9c6100763ea98ac6b00c38fb22537fbd66c1b6746c5b5ef490be8"); err != nil {
		return err
	}
	const (
		versionFieldCheck = `test "$(grep -Ec '^Version:' <<<"${syft_version}")" = 1`
		exactVersionCheck = `test "$(grep -Ec '^Version:[[:blank:]]+1[.]50[.]0$' <<<"${syft_version}")" = 1`
	)
	for _, required := range []string{
		`tools_root="${RUNNER_TEMP}/release-tools"`,
		`ENV_BIN=/usr/bin/env`,
		`GO_BIN="$(resolve_binary go)"`,
		`validate_build_tool() {`,
		`GO_IDENTITY="$(validate_build_tool "${GO_BIN}")"`,
		`test "$(validate_build_tool "${GO_BIN}" "${GO_IDENTITY}")" = "${GO_IDENTITY}"`,
		`(( (permissions & 07000) == 0 )) || return 1`,
		`(( (permissions & 07022) == 0 )) || return 1`,
		`validate_authority "${binary%/*}" || return 1`,
		`SYFT_BIN="${tools_root}/bin/syft"`,
		`run_clean_go env GOVERSION`,
		`run_clean_go install -ldflags '-X main.version=1.50.0' github.com/anchore/syft/cmd/syft@v1.50.0`,
		`run_clean_go version -m "${SYFT_BIN}"`,
		`github.com/anchore/syft/cmd/syft`,
		`TOOLS_ROOT_IDENTITY="$(stat -c '%d:%i' -- "${tools_root}")"`,
		`SYFT_CONFIG_IDENTITY="$(stat -c '%d:%i' -- "${tools_root}/config/syft.yaml")"`,
		`test "$(stat -c '%d:%i' -- "${tools_root}")" = "${TOOLS_ROOT_IDENTITY}"`,
		`test "$(stat -c '%d:%i' -- "${tools_root}/config/syft.yaml")" = "${SYFT_CONFIG_IDENTITY}"`,
		`^\tmod\tgithub[.]com/anchore/syft\t`,
		`^\tmod\tgithub[.]com/anchore/syft\tv1[.]50[.]0\th1:[A-Za-z0-9+/]{42}[AEIMQUYcgkosw048]=$`,
		`run_clean_syft help`,
		`run_clean_syft scan --help`,
		`run_clean_syft version --help`,
		`run_clean_syft version`,
		versionFieldCheck,
		exactVersionCheck,
		`SYFT_CONFIG="${tools_root}/config/syft.yaml"`,
		`SYFT_CHECK_FOR_APP_UPDATE=false`,
		`SYFT_COMPLIANCE_MISSING_NAME=drop`,
		`SYFT_COMPLIANCE_MISSING_VERSION=stub`,
		`"${SYFT_BIN}" scan "dir:${RUNNER_TEMP}/release-staging"`,
		`-c "${tools_root}/config/syft.yaml" -o "spdx-json=${RUNNER_TEMP}/raw-syft.spdx.json"`,
		`test "$(stat -c '%a' -- "${tools_root}/config/syft.yaml")" = 600`,
		`test "$(stat -c '%u' -- "${tools_root}/config/syft.yaml")" = "${effective_uid}"`,
	} {
		if !strings.Contains(script, required) {
			return fmt.Errorf("decoded shell is missing %q", required)
		}
	}
	if err := validateCommandSubstitutionGuards(script, "expected", true); err != nil {
		return fmt.Errorf("Syft binary validation: %w", err)
	}
	lines := trimmedShellLines(script)
	if shellLineCount(lines, versionFieldCheck) != 1 || shellLineCount(lines, exactVersionCheck) != 1 {
		return errors.New("Syft version guards are not the two exact executable lines")
	}
	if shellLineCount(lines, `run_clean_go install -ldflags '-X main.version=1.50.0' github.com/anchore/syft/cmd/syft@v1.50.0`) != 1 ||
		shellLinePrefixCount(lines, "run_clean_go install ") != 1 {
		return errors.New("Syft install invocation is not the one exact executable line")
	}
	for _, sequence := range [][]string{
		{`revalidate_go`, `test "$(run_clean_go env GOVERSION)" = go1.26.5`},
		{`revalidate_go`, `run_clean_go install -ldflags '-X main.version=1.50.0' github.com/anchore/syft/cmd/syft@v1.50.0`},
		{`revalidate_go`, `build_info="$(run_clean_go version -m "${SYFT_BIN}")"`},
	} {
		if shellLineSequenceCount(lines, sequence) != 1 {
			return fmt.Errorf("Syft Go invocation is not immediately preceded by revalidation: %q", sequence)
		}
	}
	if shellLineCount(lines, "revalidate_go") != 3 {
		return errors.New("Syft build must revalidate roots and binaries before exactly three Go calls")
	}
	wantScanInvocation := []string{
		`"${SYFT_BIN}" scan "dir:${RUNNER_TEMP}/release-staging" \`,
		`-c "${tools_root}/config/syft.yaml" -o "spdx-json=${RUNNER_TEMP}/raw-syft.spdx.json"`,
	}
	if shellLineSequenceCount(lines, wantScanInvocation) != 1 || shellLinePrefixCount(lines, `"${SYFT_BIN}" scan `) != 1 {
		return errors.New("Syft scan invocation is not the one exact executable sequence")
	}
	for _, line := range []string{
		`revalidate_syft; run_clean_syft help >/dev/null`,
		`revalidate_syft; run_clean_syft scan --help >/dev/null`,
		`revalidate_syft; run_clean_syft version --help >/dev/null`,
	} {
		if shellLineCount(lines, line) != 1 {
			return fmt.Errorf("Syft preflight is not exact: %q", line)
		}
	}
	for _, sequence := range [][]string{
		{`revalidate_syft`, `syft_version="$(run_clean_syft version)"`},
		{`revalidate_syft`, `"${ENV_BIN}" -i \`},
		{`-c "${tools_root}/config/syft.yaml" -o "spdx-json=${RUNNER_TEMP}/raw-syft.spdx.json"`, `revalidate_syft`, `"${RUNNER_TEMP}/releasepack" sbom \`},
	} {
		if shellLineSequenceCount(lines, sequence) != 1 {
			return fmt.Errorf("Syft invocation or post-scan transition lacks exact revalidation: %q", sequence)
		}
	}
	if shellLineCount(lines, "revalidate_syft") != 3 {
		return errors.New("Syft runtime must have exact standalone pre-use and post-scan revalidations")
	}
	wantRevalidateSyft := []string{
		`revalidate_syft() {`,
		`validate_roots`,
		`test "$(validate_binary "${ENV_BIN}" "${ENV_IDENTITY}")" = "${ENV_IDENTITY}"`,
		`test "$(validate_binary "${SYFT_BIN}" "${SYFT_IDENTITY}")" = "${SYFT_IDENTITY}"`,
		`}`,
	}
	if shellLineSequenceCount(lines, wantRevalidateSyft) != 1 {
		return errors.New("Syft runtime revalidation body is not exactly bound to roots, env, and binary identities")
	}
	for _, line := range []string{
		`TOOLS_ROOT_IDENTITY="$(stat -c '%d:%i' -- "${tools_root}")"`,
		`SYFT_CONFIG_IDENTITY="$(stat -c '%d:%i' -- "${tools_root}/config/syft.yaml")"`,
		`test "$(stat -c '%d:%i' -- "${tools_root}")" = "${TOOLS_ROOT_IDENTITY}"`,
		`test "$(stat -c '%d:%i' -- "${tools_root}/config/syft.yaml")" = "${SYFT_CONFIG_IDENTITY}"`,
	} {
		if shellLineCount(lines, line) != 1 {
			return fmt.Errorf("Syft root/config identity line is not exact: %q", line)
		}
	}
	if regexp.MustCompile(`(?m)(^|[[:space:]])PATH=`).MatchString(script) || strings.Contains(script, "${PATH}") {
		return errors.New("isolated Syft shell receives PATH")
	}
	if strings.Count(script, `"${ENV_BIN}" -i`) != 3 {
		return errors.New("Syft shell must define exactly three env -i vectors")
	}
	for _, exact := range []struct {
		text  string
		count int
	}{
		{`SYFT_CONFIG="${tools_root}/config/syft.yaml"`, 2},
		{"SYFT_CHECK_FOR_APP_UPDATE=false", 2},
		{"SYFT_COMPLIANCE_MISSING_NAME=drop", 1},
		{"SYFT_COMPLIANCE_MISSING_VERSION=stub", 1},
	} {
		if strings.Count(script, exact.text) != exact.count {
			return fmt.Errorf("%q count = %d, want %d", exact.text, strings.Count(script, exact.text), exact.count)
		}
	}
	goStart := strings.Index(script, "run_clean_go() {")
	syftStart := strings.Index(script, "run_clean_syft() {")
	if goStart < 0 || syftStart <= goStart {
		return errors.New("clean Go/Syft helper boundaries are missing")
	}
	goNames, err := isolatedInvocationEnvironmentNames(script[goStart:syftStart], `"${GO_BIN}" "$@"`)
	if err != nil {
		return err
	}
	wantGo := []string{"CGO_ENABLED", "GOBIN", "GOCACHE", "GOENV", "GOFLAGS", "GOINSECURE", "GOMODCACHE", "GONOPROXY", "GONOSUMDB", "GOPATH", "GOPRIVATE", "GOPROXY", "GOSUMDB", "GOTOOLCHAIN", "GOWORK", "HOME", "LANG", "LC_ALL", "TMPDIR", "TZ", "XDG_CONFIG_HOME"}
	if !reflect.DeepEqual(goNames, wantGo) {
		return fmt.Errorf("Go environment names = %v", goNames)
	}
	syftNames, err := isolatedInvocationEnvironmentNames(script[syftStart:], `"${SYFT_BIN}" "$@"`)
	if err != nil {
		return err
	}
	wantSyft := []string{"HOME", "LANG", "LC_ALL", "SYFT_CHECK_FOR_APP_UPDATE", "SYFT_CONFIG", "TMPDIR", "TZ", "XDG_CONFIG_HOME"}
	if !reflect.DeepEqual(syftNames, wantSyft) {
		return fmt.Errorf("Syft runtime environment names = %v", syftNames)
	}
	scanStart := strings.LastIndex(script, `"${ENV_BIN}" -i`)
	if scanStart < 0 {
		return errors.New("isolated Syft scan vector is missing")
	}
	scanEnd := strings.Index(script[scanStart:], `"${SYFT_BIN}" scan`)
	if scanEnd < 0 {
		return errors.New("isolated Syft scan vector is missing")
	}
	scanSlice := script[scanStart : scanStart+scanEnd]
	matches := regexp.MustCompile(`\b([A-Z][A-Z0-9_]*)=`).FindAllStringSubmatch(scanSlice, -1)
	scanNames := make([]string, 0, len(matches))
	for _, match := range matches {
		scanNames = append(scanNames, match[1])
	}
	slices.Sort(scanNames)
	wantScan := []string{"HOME", "LANG", "LC_ALL", "SYFT_CHECK_FOR_APP_UPDATE", "SYFT_COMPLIANCE_MISSING_NAME", "SYFT_COMPLIANCE_MISSING_VERSION", "SYFT_CONFIG", "TMPDIR", "TZ", "XDG_CONFIG_HOME"}
	if !reflect.DeepEqual(scanNames, wantScan) {
		return fmt.Errorf("Syft scan environment names = %v", scanNames)
	}
	return nil
}

func requireExactTextSHA256(name, value, want string) error {
	sum := sha256.Sum256([]byte(value))
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("%s SHA-256 = %s, want exact reviewed digest %s", name, got, want)
	}
	return nil
}

func validateExactReleaseStepShapes(packageJob, assetJob, publishJob releaseWorkflowJob) error {
	type shape struct {
		name string
		id   string
		uses string
		env  map[string]string
	}
	wantPackage := []shape{
		{uses: checkoutAction},
		{uses: setupGoAction},
		{name: "Validate release metadata", id: "metadata", env: map[string]string{"EVENT_REF": "${{ github.ref }}", "EVENT_TAG": "${{ github.ref_name }}", "EVENT_SHA": "${{ github.sha }}"}},
		{name: "Build and archive release assets", env: map[string]string{"TAG": "${{ steps.metadata.outputs.tag }}", "VERSION": "${{ steps.metadata.outputs.version }}", "TAG_COMMIT": "${{ steps.metadata.outputs.tag_commit }}", "SOURCE_EPOCH": "${{ steps.metadata.outputs.source_epoch }}"}},
		{name: "Generate SBOM and checksums", env: map[string]string{"TAG": "${{ steps.metadata.outputs.tag }}", "VERSION": "${{ steps.metadata.outputs.version }}", "SOURCE_EPOCH": "${{ steps.metadata.outputs.source_epoch }}"}},
		{name: "Verify package checksums"},
		{name: "Attest release assets", uses: attestAction},
		{name: "Upload verified release assets", id: "upload", uses: uploadArtifactAction},
		{name: "Validate artifact outputs", id: "artifact-metadata", env: map[string]string{"RAW_ARTIFACT_ID": "${{ steps.upload.outputs.artifact-id }}", "RAW_ARTIFACT_DIGEST": "${{ steps.upload.outputs.artifact-digest }}"}},
	}
	wantAsset := []shape{
		{name: "Download immutable release assets", uses: downloadArtifactAction},
		{name: "Verify checksums and attestations", env: map[string]string{"GH_TOKEN": "${{ github.token }}", "TAG": "${{ needs.package.outputs.tag }}", "VERSION": "${{ needs.package.outputs.version }}", "TAG_COMMIT": "${{ needs.package.outputs.tag_commit }}"}}, //nolint:gosec // This is GitHub's documented expression, not a credential.
	}
	wantPublish := []shape{
		{name: "Download immutable release assets", uses: downloadArtifactAction},
		{name: "Reverify package checksums"},
		{name: "Publish verified release", env: map[string]string{"GH_TOKEN": "${{ github.token }}", "TAG": "${{ needs.package.outputs.tag }}", "VERSION": "${{ needs.package.outputs.version }}", "TAG_COMMIT": "${{ needs.package.outputs.tag_commit }}"}}, //nolint:gosec // This is GitHub's documented expression, not a credential.
	}
	for jobName, pair := range map[string]struct {
		got  []releaseWorkflowStep
		want []shape
	}{
		"package":            {packageJob.Steps, wantPackage},
		"asset-verification": {assetJob.Steps, wantAsset},
		"publish":            {publishJob.Steps, wantPublish},
	} {
		for index, want := range pair.want {
			got := pair.got[index]
			if got.Name != want.name || got.ID != want.id || got.Uses != want.uses || !reflect.DeepEqual(got.Env, want.env) {
				return fmt.Errorf("%s step %d shape = name:%q id:%q uses:%q env:%v", jobName, index, got.Name, got.ID, got.Uses, got.Env)
			}
			if got.Run != "" && got.Shell != "bash" {
				return fmt.Errorf("%s run step %d shell = %q", jobName, index, got.Shell)
			}
			if got.Uses != "" && got.Shell != "" {
				return fmt.Errorf("%s action step %d has shell", jobName, index)
			}
		}
	}
	for _, job := range []releaseWorkflowJob{assetJob, publishJob} {
		for _, step := range job.Steps {
			lower := strings.ToLower(shellWithoutCommentOnlyLines(step.Run))
			for _, forbidden := range []string{"git ", "go ", "source ", "actions/checkout", "github_workspace"} {
				if strings.Contains(lower, forbidden) {
					return fmt.Errorf("downstream step %q contains repository-source behavior %q", step.Name, forbidden)
				}
			}
		}
	}
	return nil
}

func exactStringMapKeys(values map[string]string, keys ...string) bool {
	if len(values) != len(keys) {
		return false
	}
	for _, key := range keys {
		if _, ok := values[key]; !ok {
			return false
		}
	}
	return true
}

func shellWithoutCommentOnlyLines(script string) string {
	lines := make([]string, 0)
	for _, line := range strings.Split(script, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func trimmedShellLines(script string) []string {
	result := make([]string, 0)
	for _, line := range strings.Split(shellWithoutCommentOnlyLines(script), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result
}

func shellLineCount(lines []string, want string) int {
	count := 0
	for _, line := range lines {
		if line == want {
			count++
		}
	}
	return count
}

func shellLinePrefixCount(lines []string, prefix string) int {
	count := 0
	for _, line := range lines {
		if strings.HasPrefix(line, prefix) {
			count++
		}
	}
	return count
}

func shellLineSequenceCount(lines, want []string) int {
	count := 0
	for index := 0; index+len(want) <= len(lines); index++ {
		if reflect.DeepEqual(lines[index:index+len(want)], want) {
			count++
		}
	}
	return count
}

func namedReleaseStep(steps []releaseWorkflowStep, name string) (releaseWorkflowStep, error) {
	var result releaseWorkflowStep
	count := 0
	for _, step := range steps {
		if step.Name == name {
			result = step
			count++
		}
	}
	if count != 1 {
		return releaseWorkflowStep{}, fmt.Errorf("step %q count = %d, want one", name, count)
	}
	return result, nil
}

func validateArtifactTransfer(packageJob, assetJob, publishJob releaseWorkflowJob) error {
	upload := releaseWorkflowStep{}
	for _, step := range packageJob.Steps {
		if step.Uses == uploadArtifactAction {
			upload = step
		}
	}
	if upload.ID != "upload" || upload.With["overwrite"] != "false" || upload.With["if-no-files-found"] != "error" ||
		upload.With["include-hidden-files"] != "false" || upload.With["compression-level"] != "0" || upload.With["retention-days"] != "1" {
		return errors.New("artifact upload controls are not exact")
	}
	wantID := "${{ needs.package.outputs.artifact_id }}"
	for _, job := range []releaseWorkflowJob{assetJob, publishJob} {
		if len(job.Steps) == 0 || job.Steps[0].Uses != downloadArtifactAction || job.Steps[0].With["artifact-ids"] != wantID ||
			job.Steps[0].With["path"] != "${{ runner.temp }}/release-assets" || job.Steps[0].With["digest-mismatch"] != "error" ||
			!exactStringMapKeys(job.Steps[0].With, "artifact-ids", "path", "digest-mismatch") {
			return errors.New("downstream artifact download is not direct-ID fail-closed")
		}
	}
	assets := []string{
		"ai-cli-gateway_${{ steps.metadata.outputs.version }}_linux_amd64.tar.gz",
		"ai-cli-gateway_${{ steps.metadata.outputs.version }}_linux_arm64.tar.gz",
		"ai-cli-gateway_${{ steps.metadata.outputs.version }}_darwin_amd64.tar.gz",
		"ai-cli-gateway_${{ steps.metadata.outputs.version }}_darwin_arm64.tar.gz",
		"ai-cli-gateway_${{ steps.metadata.outputs.version }}_windows_amd64.zip",
		"ai-cli-gateway_${{ steps.metadata.outputs.version }}_sbom.spdx.json",
		"SHA256SUMS",
	}
	for _, action := range []string{attestAction, uploadArtifactAction} {
		var pathValue string
		for _, step := range packageJob.Steps {
			if step.Uses == action {
				if action == attestAction {
					pathValue = step.With["subject-path"]
				} else {
					pathValue = step.With["path"]
				}
			}
		}
		lines := strings.Split(strings.TrimSpace(pathValue), "\n")
		if len(lines) != len(assets) {
			return fmt.Errorf("%s path count = %d", action, len(lines))
		}
		for index, asset := range assets {
			want := "${{ runner.temp }}/release-assets/" + asset
			if lines[index] != want {
				return fmt.Errorf("%s path %d = %q, want %q", action, index, lines[index], want)
			}
		}
	}
	return nil
}

func TestMakefileExactVerificationChain(t *testing.T) {
	makefile := string(readRepositoryFile(t, "Makefile"))
	if !strings.Contains(makefile, "GOLANGCI_LINT ?= golangci-lint") {
		t.Fatal("Makefile does not provide the overridable golangci-lint command")
	}
	targets, dependencies, recipes := parseMakefileTargets(t, makefile)
	wantTargets := map[string]struct{}{
		"fmt-check": {}, "vet": {}, "lint": {}, "test": {}, "race": {},
		"integration": {}, "build": {}, "verify": {},
	}
	if !reflect.DeepEqual(targets, wantTargets) {
		t.Fatalf("Makefile targets = %v, want exact public targets %v", targets, wantTargets)
	}
	wantVerify := []string{"fmt-check", "vet", "lint", "test", "race", "integration", "build"}
	if !reflect.DeepEqual(dependencies["verify"], wantVerify) {
		t.Fatalf("verify prerequisites = %q, want exact order %q", dependencies["verify"], wantVerify)
	}
	if len(recipes["verify"]) != 0 {
		t.Fatal("verify must be the exact ordered dependency chain, not a separate recipe")
	}

	phony := makefileDirectiveFields(makefile, ".PHONY:")
	wantPhony := []string{"fmt-check", "vet", "lint", "test", "race", "integration", "build", "verify"}
	if !reflect.DeepEqual(phony, wantPhony) {
		t.Fatalf(".PHONY = %q, want exact target order %q", phony, wantPhony)
	}
	requireExactRecipe(t, recipes, "vet", "go vet ./...")
	requireExactRecipe(t, recipes, "lint", "$(GOLANGCI_LINT) run ./...")
	requireExactRecipe(t, recipes, "test", "go test ./...")
	requireExactRecipe(t, recipes, "race", "go test -race ./...")
	requireExactRecipe(t, recipes, "integration", "go test -tags=integration ./...")

	fmtRecipe := collapseWhitespace(strings.Join(recipes["fmt-check"], " "))
	requireContainsAll(t, "fmt-check recipe", fmtRecipe,
		"gofmt -l .", "test -z", "printf '%s\\n'", "exit 1")
	buildRecipe := collapseWhitespace(strings.Join(recipes["build"], " "))
	requireContainsAll(t, "build recipe", buildRecipe,
		"CGO_ENABLED=0", "go build -trimpath", `-o "$${TMPDIR:-/tmp}/ai-cli-gateway"`,
		"./cmd/ai-cli-gateway")

	lower := strings.ToLower(makefile)
	for _, forbidden := range []string{
		"git init", "git add", "git commit", "git push", "go get", "curl ", "wget ",
		"npm ", "npx ", "-tags=live", " codex ", " claude ", " gemini ",
	} {
		if strings.Contains(lower, forbidden) {
			t.Fatalf("Makefile contains forbidden repository/network/provider action %q", strings.TrimSpace(forbidden))
		}
	}
}

func parseMakefileTargets(t *testing.T, document string) (
	map[string]struct{},
	map[string][]string,
	map[string][]string,
) {
	t.Helper()
	targetPattern := regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_-]*):(?:\s*(.*))$`)
	targets := make(map[string]struct{})
	dependencies := make(map[string][]string)
	recipes := make(map[string][]string)
	current := ""
	for lineNumber, line := range strings.Split(strings.ReplaceAll(document, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "\t") {
			if current == "" {
				t.Fatalf("Makefile line %d has a recipe without a target", lineNumber+1)
			}
			recipes[current] = append(recipes[current], strings.TrimSpace(line))
			continue
		}
		current = ""
		match := targetPattern.FindStringSubmatch(line)
		if match == nil {
			continue
		}
		current = match[1]
		if _, duplicate := targets[current]; duplicate {
			t.Fatalf("Makefile target %q is duplicated", current)
		}
		targets[current] = struct{}{}
		dependencies[current] = strings.Fields(match[2])
	}
	return targets, dependencies, recipes
}

func makefileDirectiveFields(document, directive string) []string {
	for _, line := range strings.Split(document, "\n") {
		if strings.HasPrefix(line, directive) {
			return strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, directive)))
		}
	}
	return nil
}

func requireExactRecipe(t *testing.T, recipes map[string][]string, target, want string) {
	t.Helper()
	if got := recipes[target]; len(got) != 1 || got[0] != want {
		t.Fatalf("Makefile %s recipe = %q, want exactly %q", target, got, want)
	}
}

func TestScannerRejectsNonPlaceholderSecretAssignment(t *testing.T) {
	root := t.TempDir()
	secret := "planted-" + "credential-value"
	contents := "OPENAI_" + "API_KEY=" + secret + "\n"
	writeFixtureFile(t, root, "planted.env", []byte(contents))

	err := scanRepository(root)
	assertFinding(t, err, "secret_assignment", "planted.env", secret)
}

func TestRawTokenCatalogRejectsEveryClosedFamily(t *testing.T) {
	stateless520 := strings.Repeat("A", 170) + "." + strings.Repeat("b", 170) + "." + strings.Repeat("C", 178)
	statelessVariable := strings.Repeat("d", 41) + "." + strings.Repeat("E", 53) + "." + strings.Repeat("9", 67)

	positives := []struct {
		name  string
		token string
	}{
		{"openai legacy", "s" + "k-" + strings.Repeat("A", 48)},
		{"openai project", "s" + "k-" + "proj-" + strings.Repeat("B", 20)},
		{"openai service account", "s" + "k-" + "svcacct-" + strings.Repeat("C", 64)},
		{"openai admin", "s" + "k-" + "admin-" + strings.Repeat("D", 256)},
		{"anthropic api03", "s" + "k-ant-" + "api" + "03-" + strings.Repeat("E", 40)},
		{"google api", "AI" + "za" + strings.Repeat("F", 35)},
		{"github personal", "gh" + "p_" + strings.Repeat("G", 36)},
		{"github oauth", "gh" + "o_" + strings.Repeat("H", 36)},
		{"github user", "gh" + "u_" + strings.Repeat("I", 36)},
		{"github refresh", "gh" + "r_" + strings.Repeat("J", 36)},
		{"github app legacy", "gh" + "s_" + strings.Repeat("K", 36)},
		{"github app stateless 520", "gh" + "s_" + stateless520},
		{"github app stateless variable", "gh" + "s_" + statelessVariable},
		{"github fine grained", "github_" + "pat_" + strings.Repeat("L", 22) + "_" + strings.Repeat("M", 59)},
	}

	for _, test := range positives {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixtureFile(t, root, "token.txt", []byte("prefix prose "+test.token+" suffix prose\n"))
			assertFinding(t, scanRepository(root), "token", "token.txt", test.token)
		})
	}
}

func TestRawTokenCatalogConservativeNearMisses(t *testing.T) {
	legacyOpenAI := func(n int) string { return "s" + "k-" + strings.Repeat("A", n) }
	currentOpenAI := func(kind string, body string) string { return "s" + "k-" + kind + "-" + body }
	anthropic := func(body string) string { return "s" + "k-ant-" + "api" + "03-" + body }
	google := func(body string) string { return "AI" + "za" + body }
	github := func(kind string, body string) string { return "gh" + kind + "_" + body }
	fine := func(first, second string) string { return "github_" + "pat_" + first + "_" + second }

	nearMisses := []struct {
		name  string
		value string
	}{
		{"openai legacy short", legacyOpenAI(47)},
		{"openai legacy long", legacyOpenAI(49)},
		{"openai legacy alphabet", "s" + "k-" + strings.Repeat("A", 24) + "!" + strings.Repeat("B", 23)},
		{"openai legacy left boundary", "X" + legacyOpenAI(48)},
		{"openai legacy right boundary", legacyOpenAI(48) + "X"},
		{"anthropic short", anthropic(strings.Repeat("A", 39))},
		{"anthropic long", anthropic(strings.Repeat("A", 257))},
		{"anthropic alphabet", anthropic(strings.Repeat("A", 20) + "!" + strings.Repeat("B", 20))},
		{"anthropic left boundary", "X" + anthropic(strings.Repeat("A", 40))},
		{"anthropic prefix only", anthropic("")},
		{"anthropic nondigit version", "s" + "k-ant-" + "api" + "x3-" + strings.Repeat("A", 40)},
		{"google short", google(strings.Repeat("A", 34))},
		{"google long", google(strings.Repeat("A", 36))},
		{"google alphabet", google(strings.Repeat("A", 17) + "!" + strings.Repeat("B", 17))},
		{"google left boundary", "X" + google(strings.Repeat("A", 35))},
		{"google right boundary", google(strings.Repeat("A", 35)) + "X"},
	}

	for _, kind := range []string{"proj", "svcacct", "admin"} {
		valid := currentOpenAI(kind, strings.Repeat("A", 20))
		nearMisses = append(nearMisses,
			struct{ name, value string }{"openai " + kind + " short", currentOpenAI(kind, strings.Repeat("A", 19))},
			struct{ name, value string }{"openai " + kind + " long", currentOpenAI(kind, strings.Repeat("A", 257))},
			struct{ name, value string }{"openai " + kind + " alphabet", currentOpenAI(kind, strings.Repeat("A", 10)+"!"+strings.Repeat("B", 10))},
			struct{ name, value string }{"openai " + kind + " left boundary", "X" + valid},
			struct{ name, value string }{"openai " + kind + " prefix only", currentOpenAI(kind, "")},
		)
	}

	for _, kind := range []string{"p", "o", "u", "r", "s"} {
		valid := github(kind, strings.Repeat("A", 36))
		nearMisses = append(nearMisses,
			struct{ name, value string }{"github " + kind + " short", github(kind, strings.Repeat("A", 35))},
			struct{ name, value string }{"github " + kind + " long", github(kind, strings.Repeat("A", 37))},
			struct{ name, value string }{"github " + kind + " alphabet", github(kind, strings.Repeat("A", 18)+"!"+strings.Repeat("B", 17))},
			struct{ name, value string }{"github " + kind + " left boundary", "X" + valid},
			struct{ name, value string }{"github " + kind + " right boundary", valid + "X"},
		)
	}

	stateless36 := strings.Repeat("A", 10) + "." + strings.Repeat("b", 10) + "." + strings.Repeat("C", 14)
	stateless768 := strings.Repeat("A", 250) + "." + strings.Repeat("b", 250) + "." + strings.Repeat("C", 266)
	nearMisses = append(nearMisses,
		struct{ name, value string }{"stateless below minimum", github("s", strings.Repeat("A", 10)+"."+strings.Repeat("b", 10)+"."+strings.Repeat("C", 13))},
		struct{ name, value string }{"stateless above maximum", github("s", stateless768+"D")},
		struct{ name, value string }{"stateless invalid alphabet", github("s", strings.Repeat("A", 10)+"+"+strings.Repeat("b", 10)+"."+strings.Repeat("C", 20)+"."+strings.Repeat("D", 20))},
		struct{ name, value string }{"stateless zero dots", github("s", strings.Repeat("A", 80))},
		struct{ name, value string }{"stateless one dot", github("s", strings.Repeat("A", 30)+"."+strings.Repeat("B", 30))},
		struct{ name, value string }{"stateless three dots", github("s", strings.Repeat("A", 20)+"."+strings.Repeat("B", 20)+"."+strings.Repeat("C", 20)+"."+strings.Repeat("D", 20))},
		struct{ name, value string }{"stateless empty first", github("s", "."+strings.Repeat("A", 20)+"."+strings.Repeat("B", 20))},
		struct{ name, value string }{"stateless empty middle", github("s", strings.Repeat("A", 20)+".."+strings.Repeat("B", 20))},
		struct{ name, value string }{"stateless empty last", github("s", strings.Repeat("A", 20)+"."+strings.Repeat("B", 20)+".")},
		struct{ name, value string }{"stateless left boundary", "X" + github("s", stateless36)},
	)

	validFine := fine(strings.Repeat("A", 22), strings.Repeat("B", 59))
	nearMisses = append(nearMisses,
		struct{ name, value string }{"fine first short", fine(strings.Repeat("A", 21), strings.Repeat("B", 59))},
		struct{ name, value string }{"fine first long", fine(strings.Repeat("A", 23), strings.Repeat("B", 59))},
		struct{ name, value string }{"fine second short", fine(strings.Repeat("A", 22), strings.Repeat("B", 58))},
		struct{ name, value string }{"fine second long", fine(strings.Repeat("A", 22), strings.Repeat("B", 60))},
		struct{ name, value string }{"fine alphabet", fine(strings.Repeat("A", 11)+"!"+strings.Repeat("A", 10), strings.Repeat("B", 59))},
		struct{ name, value string }{"fine left boundary", "X" + validFine},
		struct{ name, value string }{"fine right boundary", validFine + "X"},
		struct{ name, value string }{"generic jwt", strings.Repeat("A", 40) + "." + strings.Repeat("B", 40) + "." + strings.Repeat("C", 40)},
	)

	for _, test := range nearMisses {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFixtureFile(t, root, "near-miss.txt", []byte(test.value+"\n"))
			if err := scanRepository(root); err != nil {
				t.Fatalf("scanRepository() error = %q, want nil for conservative near miss", err)
			}
		})
	}
}

func TestScannerRejectsPrivateKeyHeaders(t *testing.T) {
	headers := []string{
		"-----BEGIN " + "RSA PRIVATE KEY-----",
		"-----BEGIN " + "OPENSSH PRIVATE KEY-----",
		"-----BEGIN " + "EC PRIVATE KEY-----",
		"-----BEGIN " + "PRIVATE KEY-----",
		"-----BEGIN " + "ENCRYPTED PRIVATE KEY-----",
	}
	for _, header := range headers {
		root := t.TempDir()
		writeFixtureFile(t, root, "key.pem", []byte(header+"\n"))
		assertFinding(t, scanRepository(root), "private_key", "key.pem", header)
	}
}

func TestAllowedPlaceholderAcceptsOnlyGitHubTokenContext(t *testing.T) {
	tests := []struct {
		value string
		want  bool
	}{
		{value: "${{ github.token }}", want: true},
		{value: "${{ github.actor }}", want: false},
		{value: "${{ secrets.GITHUB_TOKEN }}", want: false},
		{value: "${{ github.token }}suffix", want: false},
		{value: "${{github.token}}", want: false},
	}
	for _, test := range tests {
		if got := isAllowedPlaceholder(test.value); got != test.want {
			t.Errorf("isAllowedPlaceholder(%q) = %t, want %t", test.value, got, test.want)
		}
	}
}

func TestSecretAssignmentPlaceholderAllowlist(t *testing.T) {
	allowed := []string{
		"AI_CLI_GATEWAY_" + "API_KEY\n",
		"OPENAI_" + "API_KEY=\n",
		"ANTHROPIC_" + "API_KEY=\"\"\n",
		"GEMINI_" + "API_KEY=placeholder\n",
		"GOOGLE_" + "API_KEY='replace-me'\n",
		"PROVIDER_" + "TOKEN=redacted\n",
		"SERVICE_" + "SECRET=<service-test-secret>\n",
		"LOCAL_" + "PASSWORD=${LOCAL_PASSWORD}\n",
	}
	for index, contents := range allowed {
		root := t.TempDir()
		name := "allowed-" + strconv.Itoa(index) + ".env"
		writeFixtureFile(t, root, name, []byte(contents))
		if err := scanRepository(root); err != nil {
			t.Fatalf("scanRepository(%s) error = %q, want nil", name, err)
		}
	}

	root := t.TempDir()
	writeFixtureFile(t, root, "config.example.toml", []byte("api_key_env = \"AI_CLI_GATEWAY_API_KEY\"\n"))
	if err := scanRepository(root); err != nil {
		t.Fatalf("scanRepository(config.example.toml) error = %q, want nil", err)
	}
}

func TestScannerRejectsSecretAssignmentSyntaxes(t *testing.T) {
	assignments := []string{
		"OPENAI_" + "API_KEY=actual-value\n",
		"export ANTHROPIC_" + "API_KEY='actual-value'\n",
		"GEMINI_" + "API_KEY: actual-value\n",
		"api_" + "key = \"actual-value\"\n",
		"GOOGLE_APPLICATION_" + "CREDENTIALS=/private/credential.json\n",
		"PROVIDER_" + "TOKEN=actual-value\n",
		"SERVICE_" + "SECRET=actual-value\n",
		"LOCAL_" + "PASSWORD=actual-value\n",
	}
	for index, contents := range assignments {
		root := t.TempDir()
		name := "assignment-" + strconv.Itoa(index) + ".env"
		writeFixtureFile(t, root, name, []byte(contents))
		assertFinding(t, scanRepository(root), "secret_assignment", name, "actual-value")
	}
}

func TestScannerRejectsInvalidOrControlRelativePaths(t *testing.T) {
	cases := []string{"line\nbreak", "tab\tname", "delete" + string(rune(0x7f)), string([]byte{'b', 'a', 'd', 0xff})}
	for _, relative := range cases {
		if err := validateRelativePath(relative); err == nil {
			t.Fatalf("validateRelativePath(%q) error = nil, want unsafe_path", relative)
		}
	}
	if err := validateRelativePath("clean/path.txt"); err != nil {
		t.Fatalf("validateRelativePath(clean) error = %q, want nil", err)
	}

	if runtime.GOOS == "windows" {
		return
	}
	for _, relative := range []string{"line\nbreak"} {
		root := t.TempDir()
		writeFixtureFile(t, root, relative, []byte("clean\n"))
		assertFinding(t, scanRepository(root), "unsafe_path", relative)
	}
}

func TestScannerRejectsEverySymlinkEntry(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ordinary Windows users may not have symlink privilege")
	}
	root := t.TempDir()
	writeFixtureFile(t, root, "target.txt", []byte("clean\n"))
	if err := os.Symlink("target.txt", filepath.Join(root, "link.txt")); err != nil {
		t.Fatalf("create symlink: %v", err)
	}
	assertFinding(t, scanRepository(root), "symlink", "link.txt")
}

func TestScannerSkipsOnlyGitMetadataAndPrivateNotes(t *testing.T) {
	root := t.TempDir()
	secret := "actual-" + "credential"
	username := "krkarma" + "777"
	for _, directory := range []string{".git", privateNotesDirectory} {
		writeFixtureFile(t, root, filepath.Join(directory, "hidden.env"), []byte("OPENAI_"+"API_KEY="+secret+"\n"))
	}
	writeFixtureFile(t, root, filepath.Join(privateNotesDirectory, "plan.md"), []byte("/Users/"+username+"/Dev/spawngate\n"))
	writeFixtureFile(t, root, "clean.txt", []byte("clean\n"))
	if err := scanRepository(root); err != nil {
		t.Fatalf("scanRepository() error = %q, want skipped metadata", err)
	}

	// The skip is exact: a sibling under the same published parent is still scanned.
	sibling := filepath.Join("docs", "superpowers-notes", "leak.md")
	writeFixtureFile(t, root, sibling, []byte("/Users/"+username+"/Dev/spawngate\n"))
	assertFinding(t, scanRepository(root), "developer_path", sibling)

	if err := os.RemoveAll(filepath.Join(root, "docs", "superpowers-notes")); err != nil {
		t.Fatalf("remove sibling: %v", err)
	}
	writeFixtureFile(t, root, filepath.Join(".other", "hidden.env"), []byte("OPENAI_"+"API_KEY="+secret+"\n"))
	assertFinding(t, scanRepository(root), "secret_assignment", filepath.Join(".other", "hidden.env"), secret)
}

func TestScannerSkipsTopLevelGitFileForLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	username := "krkarma" + "777"
	writeFixtureFile(t, root, ".git", []byte("gitdir: /Users/"+username+"/Dev/repository/.git/worktrees/feature\n"))
	writeFixtureFile(t, root, "clean.txt", []byte("clean\n"))

	if err := scanRepository(root); err != nil {
		t.Fatalf("scanRepository(linked worktree) error = %q, want skipped top-level Git metadata", err)
	}
}

func TestScannerRejectsTopLevelGitSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("ordinary Windows users may not have symlink privilege")
	}
	root := t.TempDir()
	writeFixtureFile(t, root, "gitdir", []byte("clean\n"))
	if err := os.Symlink("gitdir", filepath.Join(root, ".git")); err != nil {
		t.Fatalf("create top-level Git symlink: %v", err)
	}

	assertFinding(t, scanRepository(root), "symlink", ".git")
}

func TestScannerDoesNotSkipNestedGitFile(t *testing.T) {
	root := t.TempDir()
	username := "krkarma" + "777"
	relative := filepath.Join("nested", ".git")
	writeFixtureFile(t, root, relative, []byte("gitdir: /Users/"+username+"/Dev/repository/.git/worktrees/feature\n"))

	assertFinding(t, scanRepository(root), "developer_path", relative)
}

func TestScannerRejectsAuthAndGeneratedArtifactBasenames(t *testing.T) {
	authFiles := []string{"auth.json", ".credentials.json", "credentials.json", "oauth_creds.json", "google_accounts.json"}
	for _, name := range authFiles {
		root := t.TempDir()
		writeFixtureFile(t, root, name, []byte("{}\n"))
		assertFinding(t, scanRepository(root), "auth_artifact", name)
	}
	for _, name := range []string{".codex", ".claude", ".gemini"} {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatalf("create auth profile directory: %v", err)
		}
		assertFinding(t, scanRepository(root), "auth_artifact", name)
	}

	artifacts := []string{
		"config.toml", ".env", ".env.local", "coverage.out", "coverage-unit.out",
		"gateway.test", "gateway.test.exe", "gateway.exe", "cpu.prof",
		"ai-cli-gateway", "fake-ai-cli", "gateway.key", "gateway.key.tmp",
		"config.toml.bak", "config.toml.lock", "config.toml.tmp",
		"config.toml.rollback.tmp", "config.toml.bak.tmp", "config.toml.bak.restore.tmp",
	}
	for _, name := range artifacts {
		root := t.TempDir()
		writeFixtureFile(t, root, name, []byte("clean\n"))
		assertFinding(t, scanRepository(root), "generated_artifact", name)
	}

	root := t.TempDir()
	writeFixtureFile(t, root, ".env.example", []byte("OPENAI_"+"API_KEY=<test-key>\n"))
	if err := scanRepository(root); err != nil {
		t.Fatalf("scanRepository(.env.example) error = %q, want nil", err)
	}
}

func TestScannerRejectsBinaryMagicWithDisguisedNames(t *testing.T) {
	pe := make([]byte, 132)
	copy(pe, []byte{'M', 'Z'})
	binary.LittleEndian.PutUint32(pe[0x3c:], 128)
	copy(pe[128:], []byte{'P', 'E', 0, 0})

	magics := [][]byte{
		{0x7f, 'E', 'L', 'F'},
		pe,
		{0xfe, 0xed, 0xfa, 0xce},
		{0xce, 0xfa, 0xed, 0xfe},
		{0xfe, 0xed, 0xfa, 0xcf},
		{0xcf, 0xfa, 0xed, 0xfe},
		{0xca, 0xfe, 0xba, 0xbe},
		{0xbe, 0xba, 0xfe, 0xca},
		{0xca, 0xfe, 0xba, 0xbf},
		{0xbf, 0xba, 0xfe, 0xca},
	}
	for index, magic := range magics {
		root := t.TempDir()
		name := "disguised-" + strconv.Itoa(index) + ".txt"
		writeFixtureFile(t, root, name, magic)
		assertFinding(t, scanRepository(root), "binary", name)
	}
}

func TestScannerRejectsDeveloperHomePathsInEveryDocument(t *testing.T) {
	username := "krkarma" + "777"
	paths := []string{
		"/Users/" + username + "/Dev/private",
		"/home/" + username + "/private",
		"C:\\Users\\" + username + "\\private",
	}
	for index, planted := range paths {
		root := t.TempDir()
		name := "path-" + strconv.Itoa(index) + ".txt"
		writeFixtureFile(t, root, name, []byte(planted+"\n"))
		assertFinding(t, scanRepository(root), "developer_path", name, planted)
	}

	nested := "docs/reference.md"
	planted := "/Users/" + username + "/Dev/private"
	root := t.TempDir()
	writeFixtureFile(t, root, nested, []byte(planted+"\n"))
	assertFinding(t, scanRepository(root), "developer_path", nested, planted)

	root = t.TempDir()
	secret := "actual-" + "credential"
	writeFixtureFile(t, root, nested, []byte("OPENAI_"+"API_KEY="+secret+"\n"))
	assertFinding(t, scanRepository(root), "secret_assignment", nested, secret)
}

func TestModuleRootResolutionDoesNotRequireGit(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "go.mod", []byte("module "+expectedModule+"\n\ngo 1.26.0\n"))
	start := filepath.Join(root, "internal", "securitytest")
	if err := os.MkdirAll(start, 0o700); err != nil {
		t.Fatalf("create nested start: %v", err)
	}
	got, err := findModuleRoot(start)
	if err != nil {
		t.Fatalf("findModuleRoot() error = %v", err)
	}
	if got != root {
		t.Fatalf("findModuleRoot() = %q, want %q", got, root)
	}
	if _, err := os.Lstat(filepath.Join(root, ".git")); !os.IsNotExist(err) {
		t.Fatalf("fixture unexpectedly has Git metadata: %v", err)
	}
}

func TestModuleRootResolutionRejectsUnrelatedModule(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "go.mod", []byte("module example.invalid/unrelated\n"))
	if _, err := findModuleRoot(root); err == nil {
		t.Fatal("findModuleRoot(unrelated) error = nil, want module_root finding")
	}
}

func TestExplicitScanRootRequiresAbsoluteRealDirectory(t *testing.T) {
	if _, err := resolveScanRoot("relative/path"); err == nil {
		t.Fatal("resolveScanRoot(relative) error = nil, want invalid_root")
	}
	fileRoot := t.TempDir()
	writeFixtureFile(t, fileRoot, "file", []byte("clean\n"))
	if _, err := resolveScanRoot(filepath.Join(fileRoot, "file")); err == nil {
		t.Fatal("resolveScanRoot(file) error = nil, want invalid_root")
	}
	absRoot := t.TempDir()
	writeFixtureFile(t, absRoot, "go.mod", []byte("module "+expectedModule+"\n"))
	got, err := resolveScanRoot(absRoot)
	if err != nil {
		t.Fatalf("resolveScanRoot(absolute directory) error = %v", err)
	}
	if got != filepath.Clean(absRoot) {
		t.Fatalf("resolveScanRoot() = %q, want %q", got, filepath.Clean(absRoot))
	}
	unrelatedRoot := t.TempDir()
	writeFixtureFile(t, unrelatedRoot, "go.mod", []byte("module example.invalid/unrelated\n"))
	if _, err := resolveScanRoot(unrelatedRoot); err == nil {
		t.Fatal("resolveScanRoot(unrelated module) error = nil, want invalid_root")
	}

	if runtime.GOOS != "windows" {
		moduleLinkRoot := t.TempDir()
		writeFixtureFile(t, moduleLinkRoot, "real.mod", []byte("module "+expectedModule+"\n"))
		if err := os.Symlink("real.mod", filepath.Join(moduleLinkRoot, "go.mod")); err != nil {
			t.Fatalf("create go.mod symlink: %v", err)
		}
		if _, err := resolveScanRoot(moduleLinkRoot); err == nil {
			t.Fatal("resolveScanRoot(symlink go.mod) error = nil, want invalid_root")
		}

		parent := t.TempDir()
		target := filepath.Join(parent, "target")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatalf("create scan target: %v", err)
		}
		writeFixtureFile(t, target, "go.mod", []byte("module "+expectedModule+"\n"))
		link := filepath.Join(parent, "link")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("create scan-root symlink: %v", err)
		}
		if _, err := resolveScanRoot(link); err == nil {
			t.Fatal("resolveScanRoot(symlink) error = nil, want invalid_root")
		}
	}
}

func TestOrdinaryScanRootIgnoresAmbientOverrides(t *testing.T) {
	t.Setenv("AI_CLI_GATEWAY_SCAN_ROOT", t.TempDir())
	root, err := repositoryScanRoot("")
	if err != nil {
		t.Fatalf("repositoryScanRoot(ordinary) error = %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(root, "go.mod")) //nolint:gosec // root is the scanner-resolved exact module root.
	if err != nil {
		t.Fatalf("read located go.mod: %v", err)
	}
	if !strings.HasPrefix(string(contents), "module "+expectedModule+"\n") {
		t.Fatalf("ordinary scan did not resolve the exact module root")
	}
}

func TestCleanFixturePasses(t *testing.T) {
	root := t.TempDir()
	writeFixtureFile(t, root, "README.md", []byte("clean public text\n"))
	if err := scanRepository(root); err != nil {
		t.Fatalf("scanRepository(clean) error = %q, want nil", err)
	}
}

func TestRepositoryHygiene(t *testing.T) {
	testutil.AcquireRepositoryScanLock(t)
	root, err := repositoryScanRoot(*scanRootFlag)
	if err != nil {
		t.Fatalf("resolve repository scan root: %v", err)
	}
	if err := scanRepository(root); err != nil {
		t.Fatalf("repository hygiene: %v", err)
	}
}

func readRepositoryFile(t *testing.T, relative string) []byte {
	t.Helper()
	root, err := repositoryScanRoot("")
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	contents, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative))) //nolint:gosec // The test supplies a fixed repository-relative public path.
	if err != nil {
		t.Fatalf("read required repository file %q: %v", relative, err)
	}
	return contents
}

func writeFixtureFile(t *testing.T, root, relative string, contents []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("create fixture parent: %v", err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func assertFinding(t *testing.T, err error, category, relative string, forbidden ...string) {
	t.Helper()
	if err == nil {
		t.Fatalf("scanRepository() error = nil, want %s finding", category)
	}
	want := category + ": " + strconv.Quote(filepath.ToSlash(relative))
	if got := err.Error(); got != want {
		t.Fatalf("scanRepository() error = %q, want %q", got, want)
	}
	for _, secret := range forbidden {
		if secret != "" && strings.Contains(err.Error(), secret) {
			t.Fatal("scanRepository() disclosed matched bytes")
		}
	}
}

type scanFinding struct {
	category string
	relative string
}

func (finding scanFinding) Error() string {
	return finding.category + ": " + strconv.Quote(filepath.ToSlash(finding.relative))
}

func scanRepository(root string) error {
	resolved, err := validateDirectoryRoot(root)
	if err != nil {
		return err
	}

	return filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, walkErr error) error {
		relative, relErr := filepath.Rel(resolved, path)
		if relErr != nil {
			return scanFinding{category: "unsafe_path", relative: "."}
		}
		if relative == "." {
			if walkErr != nil {
				return scanFinding{category: "read_error", relative: "."}
			}
			return nil
		}
		if err := validateRelativePath(relative); err != nil {
			return err
		}
		if walkErr != nil {
			return scanFinding{category: "read_error", relative: relative}
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return scanFinding{category: "symlink", relative: relative}
		}
		if relative == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		base := strings.ToLower(entry.Name())
		if isAuthArtifact(base, entry.IsDir()) {
			return scanFinding{category: "auth_artifact", relative: relative}
		}
		if entry.IsDir() {
			if filepath.ToSlash(relative) == privateNotesDirectory {
				return filepath.SkipDir
			}
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return scanFinding{category: "read_error", relative: relative}
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		if isGeneratedArtifact(base) {
			return scanFinding{category: "generated_artifact", relative: relative}
		}

		contents, err := os.ReadFile(path) //nolint:gosec // WalkDir supplied a regular entry under the validated scan root.
		if err != nil {
			return scanFinding{category: "read_error", relative: relative}
		}
		if hasBinaryMagic(contents) {
			return scanFinding{category: "binary", relative: relative}
		}
		if hasPrivateKeyHeader(contents) {
			return scanFinding{category: "private_key", relative: relative}
		}
		if hasSecretAssignment(relative, contents) {
			return scanFinding{category: "secret_assignment", relative: relative}
		}
		if hasClosedCatalogToken(contents) {
			return scanFinding{category: "token", relative: relative}
		}
		if hasDeveloperHomePath(contents) {
			return scanFinding{category: "developer_path", relative: relative}
		}
		return nil
	})
}

func validateRelativePath(relative string) error {
	if relative == "" || !utf8.ValidString(relative) {
		return scanFinding{category: "unsafe_path", relative: relative}
	}
	for _, character := range relative {
		if unicode.IsControl(character) {
			return scanFinding{category: "unsafe_path", relative: relative}
		}
	}
	return nil
}

func repositoryScanRoot(override string) (string, error) {
	if override != "" {
		return resolveScanRoot(override)
	}
	workingDirectory, err := os.Getwd()
	if err != nil {
		return "", scanFinding{category: "module_root", relative: "."}
	}
	return findModuleRoot(workingDirectory)
}

func resolveScanRoot(root string) (string, error) {
	cleaned, err := validateDirectoryRoot(root)
	if err != nil {
		return "", err
	}
	modulePath := filepath.Join(cleaned, "go.mod")
	moduleInfo, err := os.Lstat(modulePath)
	if err != nil || moduleInfo.Mode()&os.ModeSymlink != 0 || !moduleInfo.Mode().IsRegular() {
		return "", scanFinding{category: "invalid_root", relative: "."}
	}
	contents, err := os.ReadFile(modulePath) //nolint:gosec // modulePath is the fixed go.mod child of the validated root.
	if err != nil || !hasExactModuleDeclaration(contents) {
		return "", scanFinding{category: "invalid_root", relative: "."}
	}
	return cleaned, nil
}

func validateDirectoryRoot(root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", scanFinding{category: "invalid_root", relative: "."}
	}
	cleaned := filepath.Clean(root)
	info, err := os.Lstat(cleaned)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", scanFinding{category: "invalid_root", relative: "."}
	}
	return cleaned, nil
}

func findModuleRoot(start string) (string, error) {
	if !filepath.IsAbs(start) {
		return "", scanFinding{category: "module_root", relative: "."}
	}
	candidate := filepath.Clean(start)
	for depth := 0; depth < 32; depth++ {
		info, err := os.Lstat(candidate)
		if err == nil && info.IsDir() && info.Mode()&os.ModeSymlink == 0 {
			modulePath := filepath.Join(candidate, "go.mod")
			moduleInfo, statErr := os.Lstat(modulePath)
			if statErr == nil && moduleInfo.Mode().IsRegular() && moduleInfo.Mode()&os.ModeSymlink == 0 {
				contents, readErr := os.ReadFile(modulePath)
				if readErr == nil && hasExactModuleDeclaration(contents) {
					return candidate, nil
				}
			}
		}
		parent := filepath.Dir(candidate)
		if parent == candidate {
			break
		}
		candidate = parent
	}
	return "", scanFinding{category: "module_root", relative: "."}
}

func hasExactModuleDeclaration(contents []byte) bool {
	found := false
	for _, line := range strings.Split(string(contents), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "module ") && !strings.HasPrefix(trimmed, "module\t") {
			continue
		}
		fields := strings.Fields(trimmed)
		if len(fields) != 2 || fields[0] != "module" || fields[1] != expectedModule || found {
			return false
		}
		found = true
	}
	return found
}

func isAuthArtifact(base string, directory bool) bool {
	if directory {
		return base == ".codex" || base == ".claude" || base == ".gemini"
	}
	switch base {
	case "auth.json", ".credentials.json", "credentials.json", "oauth_creds.json", "google_accounts.json":
		return true
	default:
		return false
	}
}

func isGeneratedArtifact(base string) bool {
	if base == "config.toml" || base == ".env" || base == "ai-cli-gateway" ||
		base == "fake-ai-cli" || base == "gateway.key" || base == "gateway.key.tmp" {
		return true
	}
	for _, suffix := range []string{
		".toml.bak", ".toml.lock", ".toml.tmp", ".toml.rollback.tmp",
		".toml.bak.tmp", ".toml.bak.restore.tmp",
	} {
		if strings.HasSuffix(base, suffix) {
			return true
		}
	}
	if strings.HasPrefix(base, ".env.") && base != ".env.example" {
		return true
	}
	if strings.HasPrefix(base, "coverage") && strings.HasSuffix(base, ".out") {
		return true
	}
	return strings.HasSuffix(base, ".test") || strings.HasSuffix(base, ".test.exe") ||
		strings.HasSuffix(base, ".exe") || strings.HasSuffix(base, ".prof")
}

func hasBinaryMagic(contents []byte) bool {
	if len(contents) >= 4 {
		first := binary.BigEndian.Uint32(contents[:4])
		switch first {
		case 0x7f454c46, 0xfeedface, 0xcefaedfe, 0xfeedfacf, 0xcffaedfe,
			0xcafebabe, 0xbebafeca, 0xcafebabf, 0xbfbafeca:
			return true
		}
	}
	if len(contents) < 64 || contents[0] != 'M' || contents[1] != 'Z' {
		return false
	}
	offset := int(contents[0x3c]) |
		int(contents[0x3d])<<8 |
		int(contents[0x3e])<<16 |
		int(contents[0x3f])<<24
	return offset >= 0 && offset <= len(contents)-4 && string(contents[offset:offset+4]) == "PE\x00\x00"
}

func hasPrivateKeyHeader(contents []byte) bool {
	headers := []string{
		"-----BEGIN " + "RSA PRIVATE KEY-----",
		"-----BEGIN " + "OPENSSH PRIVATE KEY-----",
		"-----BEGIN " + "EC PRIVATE KEY-----",
		"-----BEGIN " + "PRIVATE KEY-----",
		"-----BEGIN " + "ENCRYPTED PRIVATE KEY-----",
	}
	text := string(contents)
	for _, header := range headers {
		if strings.Contains(text, header) {
			return true
		}
	}
	return false
}

func hasSecretAssignment(relative string, contents []byte) bool {
	extension := strings.ToLower(filepath.Ext(relative))
	allowQuotedKey := extension == ".json" || extension == ".yaml" || extension == ".yml" || extension == ".toml"
	uppercaseOnly := extension == ".go"
	for _, line := range strings.Split(string(contents), "\n") {
		key, value, ok := splitSecretAssignment(line, allowQuotedKey)
		if ok && (!uppercaseOnly || key == strings.ToUpper(key)) && isSecretName(key) && !isAllowedPlaceholder(value) {
			return true
		}
	}
	return false
}

func splitSecretAssignment(line string, allowQuotedKey bool) (string, string, bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "export ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "export "))
	}
	if strings.HasPrefix(trimmed, "- ") {
		trimmed = strings.TrimSpace(strings.TrimPrefix(trimmed, "- "))
	}
	if trimmed == "" {
		return "", "", false
	}

	var key string
	var remainder string
	if trimmed[0] == '"' || trimmed[0] == '\'' {
		if !allowQuotedKey {
			return "", "", false
		}
		quote := trimmed[0]
		end := strings.IndexByte(trimmed[1:], quote)
		if end < 0 {
			return "", "", false
		}
		key = trimmed[1 : end+1]
		remainder = strings.TrimSpace(trimmed[end+2:])
	} else {
		separator := strings.IndexAny(trimmed, "=:")
		if separator < 0 {
			return "", "", false
		}
		key = strings.TrimSpace(trimmed[:separator])
		remainder = trimmed[separator:]
	}
	if remainder == "" || remainder[0] != '=' && remainder[0] != ':' || strings.HasPrefix(remainder, ":=") {
		return "", "", false
	}
	return key, strings.TrimSpace(remainder[1:]), true
}

func isSecretName(key string) bool {
	upper := strings.ToUpper(strings.TrimSpace(key))
	switch upper {
	case "AI_CLI_GATEWAY_API_KEY", "OPENAI_API_KEY", "CODEX_API_KEY", "ANTHROPIC_API_KEY",
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "API_KEY", "TOKEN", "SECRET", "PASSWORD", "CLIENT_SECRET":
		return true
	}
	for _, suffix := range []string{"_API_KEY", "_TOKEN", "_SECRET", "_PASSWORD", "_PRIVATE_KEY"} {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}
	return false
}

func isAllowedPlaceholder(value string) bool {
	trimmed := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(value), ","))
	if len(trimmed) >= 2 && (trimmed[0] == '"' && trimmed[len(trimmed)-1] == '"' || trimmed[0] == '\'' && trimmed[len(trimmed)-1] == '\'') {
		trimmed = strings.TrimSpace(trimmed[1 : len(trimmed)-1])
	}
	if trimmed == "${{ github.token }}" {
		return true
	}
	if trimmed == "" {
		return true
	}
	switch strings.ToLower(trimmed) {
	case "placeholder", "replace-me", "redacted":
		return true
	}
	if len(trimmed) >= 3 && trimmed[0] == '<' && trimmed[len(trimmed)-1] == '>' {
		inner := trimmed[1 : len(trimmed)-1]
		return inner != "" && !strings.ContainsAny(inner, "<>\r\n")
	}
	if len(trimmed) >= 4 && strings.HasPrefix(trimmed, "${") && strings.HasSuffix(trimmed, "}") {
		inner := trimmed[2 : len(trimmed)-1]
		if inner == "" {
			return false
		}
		for index, character := range inner {
			valid := character == '_' || character >= 'A' && character <= 'Z' ||
				character >= 'a' && character <= 'z' || index > 0 && character >= '0' && character <= '9'
			if !valid {
				return false
			}
		}
		return true
	}
	return false
}

type tokenRule struct {
	name  string
	match func(string) bool
}

func hasClosedCatalogToken(contents []byte) bool {
	text := string(contents)
	for _, rule := range closedTokenCatalog() {
		if rule.match(text) {
			return true
		}
	}
	return false
}

func closedTokenCatalog() []tokenRule {
	return []tokenRule{
		{name: "openai_legacy", match: func(text string) bool {
			return hasExactPrefixed(text, "s"+"k-", 48, isBase62, isFlexibleTokenCharacter)
		}},
		{name: "openai_project", match: func(text string) bool {
			return hasBoundedPrefixed(text, "s"+"k-"+"proj-", 20, 256, isFlexibleTokenCharacter)
		}},
		{name: "openai_service_account", match: func(text string) bool {
			return hasBoundedPrefixed(text, "s"+"k-"+"svcacct-", 20, 256, isFlexibleTokenCharacter)
		}},
		{name: "openai_admin", match: func(text string) bool {
			return hasBoundedPrefixed(text, "s"+"k-"+"admin-", 20, 256, isFlexibleTokenCharacter)
		}},
		{name: "anthropic", match: hasAnthropicToken},
		{name: "google", match: func(text string) bool {
			return hasExactPrefixed(text, "AI"+"za", 35, isFlexibleTokenCharacter, isFlexibleTokenCharacter)
		}},
		{name: "github_personal", match: func(text string) bool { return hasExactPrefixed(text, "gh"+"p_", 36, isBase62, isGitHubTokenCharacter) }},
		{name: "github_oauth", match: func(text string) bool { return hasExactPrefixed(text, "gh"+"o_", 36, isBase62, isGitHubTokenCharacter) }},
		{name: "github_user", match: func(text string) bool { return hasExactPrefixed(text, "gh"+"u_", 36, isBase62, isGitHubTokenCharacter) }},
		{name: "github_refresh", match: func(text string) bool { return hasExactPrefixed(text, "gh"+"r_", 36, isBase62, isGitHubTokenCharacter) }},
		{name: "github_app_legacy", match: func(text string) bool { return hasExactPrefixed(text, "gh"+"s_", 36, isBase62, isGitHubTokenCharacter) }},
		{name: "github_app_stateless", match: hasStatelessGitHubAppToken},
		{name: "github_fine_grained", match: hasFineGrainedGitHubToken},
	}
}

func hasExactPrefixed(text, prefix string, bodyLength int, bodyAllowed, boundaryCharacter func(byte) bool) bool {
	for search := 0; search < len(text); {
		relative := strings.Index(text[search:], prefix)
		if relative < 0 {
			return false
		}
		start := search + relative
		bodyStart := start + len(prefix)
		end := bodyStart + bodyLength
		if (start == 0 || !boundaryCharacter(text[start-1])) && end <= len(text) {
			valid := true
			for index := bodyStart; index < end; index++ {
				if !bodyAllowed(text[index]) {
					valid = false
					break
				}
			}
			if valid && (end == len(text) || !boundaryCharacter(text[end])) {
				return true
			}
		}
		search = start + 1
	}
	return false
}

func hasBoundedPrefixed(text, prefix string, minimum, maximum int, allowed func(byte) bool) bool {
	for search := 0; search < len(text); {
		relative := strings.Index(text[search:], prefix)
		if relative < 0 {
			return false
		}
		start := search + relative
		bodyStart := start + len(prefix)
		end := bodyStart
		for end < len(text) && allowed(text[end]) {
			end++
		}
		if (start == 0 || !allowed(text[start-1])) && end-bodyStart >= minimum && end-bodyStart <= maximum {
			return true
		}
		search = start + 1
	}
	return false
}

func hasAnthropicToken(text string) bool {
	prefix := "s" + "k-ant-" + "api"
	for search := 0; search < len(text); {
		relative := strings.Index(text[search:], prefix)
		if relative < 0 {
			return false
		}
		start := search + relative
		version := start + len(prefix)
		if (start == 0 || !isFlexibleTokenCharacter(text[start-1])) && version+3 <= len(text) &&
			isDigit(text[version]) && isDigit(text[version+1]) && text[version+2] == '-' {
			bodyStart := version + 3
			end := bodyStart
			for end < len(text) && isFlexibleTokenCharacter(text[end]) {
				end++
			}
			if end-bodyStart >= 40 && end-bodyStart <= 256 {
				return true
			}
		}
		search = start + 1
	}
	return false
}

func hasStatelessGitHubAppToken(text string) bool {
	prefix := "gh" + "s_"
	for search := 0; search < len(text); {
		relative := strings.Index(text[search:], prefix)
		if relative < 0 {
			return false
		}
		start := search + relative
		bodyStart := start + len(prefix)
		end := bodyStart
		for end < len(text) && isStatelessCandidateCharacter(text[end]) {
			end++
		}
		candidate := text[bodyStart:end]
		if (start == 0 || !isStatelessCandidateCharacter(text[start-1])) && len(candidate) >= 36 && len(candidate) <= 768 && strings.Count(candidate, ".") == 2 {
			segments := strings.Split(candidate, ".")
			if len(segments) == 3 && validBase64URLSegment(segments[0]) && validBase64URLSegment(segments[1]) && validBase64URLSegment(segments[2]) {
				return true
			}
		}
		search = start + 1
	}
	return false
}

func hasFineGrainedGitHubToken(text string) bool {
	prefix := "github_" + "pat_"
	for search := 0; search < len(text); {
		relative := strings.Index(text[search:], prefix)
		if relative < 0 {
			return false
		}
		start := search + relative
		first := start + len(prefix)
		separator := first + 22
		end := separator + 1 + 59
		if (start == 0 || !isGitHubTokenCharacter(text[start-1])) && end <= len(text) && text[separator] == '_' &&
			allBytes(text[first:separator], isBase62) && allBytes(text[separator+1:end], isBase62) &&
			(end == len(text) || !isGitHubTokenCharacter(text[end])) {
			return true
		}
		search = start + 1
	}
	return false
}

func allBytes(text string, allowed func(byte) bool) bool {
	if text == "" {
		return false
	}
	for index := 0; index < len(text); index++ {
		if !allowed(text[index]) {
			return false
		}
	}
	return true
}

func validBase64URLSegment(segment string) bool {
	return allBytes(segment, func(character byte) bool {
		return isBase62(character) || character == '_' || character == '-'
	})
}

func isBase62(character byte) bool {
	return character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z' || character >= '0' && character <= '9'
}

func isDigit(character byte) bool {
	return character >= '0' && character <= '9'
}

func isFlexibleTokenCharacter(character byte) bool {
	return isBase62(character) || character == '_' || character == '-'
}

func isGitHubTokenCharacter(character byte) bool {
	return isBase62(character) || character == '_'
}

func isStatelessCandidateCharacter(character byte) bool {
	return isFlexibleTokenCharacter(character) || character == '.'
}

func hasDeveloperHomePath(contents []byte) bool {
	normalized := strings.ToLower(strings.ReplaceAll(string(contents), "\\", "/"))
	username := strings.ToLower("krkarma" + "777")
	for _, marker := range []string{"/users/" + username, "/home/" + username} {
		for search := 0; search < len(normalized); {
			relative := strings.Index(normalized[search:], marker)
			if relative < 0 {
				break
			}
			start := search + relative
			end := start + len(marker)
			leftOK := start == 0 || isPathOpeningBoundary(normalized[start-1]) || start >= 2 && normalized[start-1] == ':' && isASCIIAlpha(normalized[start-2])
			rightOK := end == len(normalized) || normalized[end] == '/' || isPathClosingBoundary(normalized[end])
			if leftOK && rightOK {
				return true
			}
			search = start + 1
		}
	}
	return false
}

func isPathOpeningBoundary(character byte) bool {
	return character == ' ' || character == '\t' || character == '\n' || character == '\r' ||
		character == '"' || character == '\'' || character == '`' || character == '(' || character == '[' || character == '{' || character == '='
}

func isPathClosingBoundary(character byte) bool {
	return isPathOpeningBoundary(character) || character == ')' || character == ']' || character == '}' || character == ',' || character == ';'
}

func isASCIIAlpha(character byte) bool {
	return character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z'
}
