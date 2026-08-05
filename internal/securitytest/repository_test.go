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
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
	"github.com/pelletier/go-toml/v2"
)

const expectedModule = "github.com/krkarma777/ai-cli-gateway"

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
			`if seconds < 1 or seconds > 300:`, `assert len(models.data) == 1`,
			`assert_fields(model, {"id", "object", "created", "owned_by"})`, `assert model.id == "codex-sdk-test"`,
			`assert model.object == "model"`, `assert model.created == 0`, `assert model.owned_by == "local"`,
			`assert isinstance(response._request_id, str)`, `assert response._request_id.startswith("req_")`,
			`assert isinstance(response.id, str) and response.id.startswith("resp_")`,
			`assert response.object == "response"`,
			`assert isinstance(response.created_at, int) and not isinstance(response.created_at, bool)`,
			`assert isinstance(response.completed_at, int) and not isinstance(response.completed_at, bool)`,
			`assert response.completed_at >= response.created_at`, `assert response.status == "completed"`,
			`assert response.background is False`, `assert response.error is None`,
			`assert response.incomplete_details is None`, `assert response.instructions == "SDK contract instruction."`,
			`assert response.model == model_name`, `assert response.parallel_tool_calls is False`,
			`assert response.previous_response_id is None`, `assert response.store is False`,
			`assert response.tools == []`, `assert response.tool_choice == "none"`,
			`assert_fields(response.text, {"format"})`, `assert_fields(response.text.format, {"type"})`,
			`assert response.text.format.type == "text"`, `assert len(response.output) == 1`,
			`assert isinstance(message.id, str) and message.id.startswith("msg_")`,
			`assert message.type == "message"`, `assert message.status == "completed"`,
			`assert message.role == "assistant"`, `assert len(message.content) == 1`, `assert content.type == "output_text"`,
			`assert content.annotations == []`, `assert content.text == "SDK_GATEWAY_OK\n"`,
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
			`assert.equal(models.data.length, 1)`, `assertFields(model, ["id", "object", "created", "owned_by"])`,
			`assert.equal(model.id, "codex-sdk-test")`, `assert.equal(model.object, "model")`,
			`assert.equal(model.created, 0)`, `assert.equal(model.owned_by, "local")`,
			`assert.equal(typeof response._request_id, "string")`, `assert.match(response._request_id, /^req_/)`,
			`assert.equal(typeof response.id, "string")`, `assert.match(response.id, /^resp_/)`,
			`assert.equal(response.object, "response")`, `assert.equal(Number.isSafeInteger(response.created_at), true)`,
			`assert.equal(Number.isSafeInteger(response.completed_at), true)`,
			`assert.equal(response.completed_at >= response.created_at, true)`,
			`assert.equal(response.status, "completed")`, `assert.equal(response.background, false)`,
			`assert.equal(response.error, null)`, `assert.equal(response.incomplete_details, null)`,
			`assert.equal(response.instructions, "SDK contract instruction.")`, `assert.equal(response.model, modelName)`,
			`assert.equal(response.parallel_tool_calls, false)`, `assert.equal(response.previous_response_id, null)`,
			`assert.equal(response.store, false)`, `assert.deepEqual(response.tools, [])`,
			`assert.equal(response.tool_choice, "none")`, `assertFields(response.text, ["format"])`,
			`assertFields(response.text.format, ["type"])`, `assert.equal(response.text.format.type, "text")`,
			`assert.equal(response.output.length, 1)`, `assert.equal(typeof message.id, "string")`,
			`assert.match(message.id, /^msg_/)`, `assert.equal(message.type, "message")`,
			`assert.equal(message.status, "completed")`, `assert.equal(message.role, "assistant")`,
			`assert.equal(message.content.length, 1)`, `assert.equal(content.type, "output_text")`,
			`assert.deepEqual(content.annotations, [])`,
			`assert.equal(content.text, "SDK_GATEWAY_OK\n")`,
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
			`response = client.responses.create( model=model_name, instructions="SDK contract instruction.", input="SDK contract input.", text={"format": {"type": "text"}}, stream=False, store=False, tools=[], tool_choice="none", )`,
			`assert_fields( response, { "id", "object", "created_at", "completed_at", "status", "background", "error", "incomplete_details", "instructions", "model", "output", "parallel_tool_calls", "previous_response_id", "store", "text", "tools", "tool_choice", }, )`,
			`assert_fields(message, {"id", "type", "status", "role", "content"})`,
			`assert_fields(content, {"type", "annotations", "text"})`,
		},
		"JavaScript": {
			`const response = await client.responses.create({ model: modelName, instructions: "SDK contract instruction.", input: "SDK contract input.", text: { format: { type: "text" } }, stream: false, store: false, tools: [], tool_choice: "none", });`,
			`assertFields(response, [ "id", "object", "created_at", "completed_at", "status", "background", "error", "incomplete_details", "instructions", "model", "output", "parallel_tool_calls", "previous_response_id", "store", "text", "tools", "tool_choice", ]);`,
			`assertFields(message, ["id", "type", "status", "role", "content"]);`,
			`assertFields(content, ["type", "annotations", "text"]);`,
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
		{"python request instruction", "Python", "examples/openai-sdk/python/main.py", `instructions="SDK contract instruction."`, `instructions="changed"`},
		{"python request store", "Python", "examples/openai-sdk/python/main.py", `store=False`, `store=True`},
		{"python response role", "Python", "examples/openai-sdk/python/main.py", `assert message.role == "assistant"`, `assert message.role == "user"`},
		{"python response text", "Python", "examples/openai-sdk/python/main.py", `assert content.text == "SDK_GATEWAY_OK\n"`, `assert content.text == "changed\n"`},
		{"python fixed error", "Python", "examples/openai-sdk/python/main.py", `sdk_contract_error: python_assertion`, `sdk_contract_error: changed`},
		{"python fixed success", "Python", "examples/openai-sdk/python/main.py", `python_sdk_contract_ok`, `python_contract_changed`},
		{"python API status integer", "Python", "examples/openai-sdk/python/main.py", `isinstance(status, int)`, `status is not None`},
		{"python API status boolean", "Python", "examples/openai-sdk/python/main.py", ` and not isinstance(status, bool)`, ``},
		{"python API status lower bound", "Python", "examples/openai-sdk/python/main.py", `400 <= status`, `399 <= status`},
		{"python API status upper bound", "Python", "examples/openai-sdk/python/main.py", `status <= 599`, `status <= 600`},
		{"python API status exception disclosure", "Python", "examples/openai-sdk/python/main.py", `return "unknown"`, "return str(error)\n    return \"unknown\""},
		{"javascript logging suppression", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `logLevel: "off"`, `logLevel: "warn"`},
		{"javascript timeout maximum", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `seconds > 300`, `seconds > 301`},
		{"javascript retries", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `maxRetries: 0`, `maxRetries: 1`},
		{"javascript request input", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `input: "SDK contract input."`, `input: "changed"`},
		{"javascript request tools", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `tools: []`, `tools: [{ type: "function" }]`},
		{"javascript response role", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `assert.equal(message.role, "assistant")`, `assert.equal(message.role, "user")`},
		{"javascript response text", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `assert.equal(content.text, "SDK_GATEWAY_OK\n")`, `assert.equal(content.text, "changed\n")`},
		{"javascript fixed error", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `sdk_contract_error: javascript_assertion`, `sdk_contract_error: changed`},
		{"javascript fixed success", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `javascript_sdk_contract_ok`, `javascript_contract_changed`},
		{"javascript API status integer", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `Number.isSafeInteger(error.status)`, `Number.isFinite(error.status)`},
		{"javascript API status lower bound", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `error.status >= 400`, `error.status >= 399`},
		{"javascript API status upper bound", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `error.status <= 599`, `error.status <= 600`},
		{"javascript API status exception disclosure", "JavaScript", "examples/openai-sdk/javascript/main.mjs", `return "unknown"`, "return String(error);\n  return \"unknown\""},
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
			{name: "models API status 400", environment: sdkAPIErrorEnvironment("models:400"), output: "sdk_contract_error: python_api 400\n", exitCode: 1},
			{name: "responses API status 599", environment: sdkAPIErrorEnvironment("responses:599"), output: "sdk_contract_error: python_api 599\n", exitCode: 1},
			{name: "API status missing", environment: sdkAPIErrorEnvironment("models:missing"), output: "sdk_contract_error: python_api unknown\n", exitCode: 1},
			{name: "API status non-integer", environment: sdkAPIErrorEnvironment("responses:string"), output: "sdk_contract_error: python_api unknown\n", exitCode: 1},
			{name: "API status boolean", environment: sdkAPIErrorEnvironment("models:boolean"), output: "sdk_contract_error: python_api unknown\n", exitCode: 1},
			{name: "API status below range", environment: sdkAPIErrorEnvironment("responses:399"), output: "sdk_contract_error: python_api unknown\n", exitCode: 1},
			{name: "API status above range", environment: sdkAPIErrorEnvironment("models:600"), output: "sdk_contract_error: python_api unknown\n", exitCode: 1},
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
			{name: "models API status 400", environment: sdkAPIErrorEnvironment("models:400"), output: "sdk_contract_error: javascript_api 400\n", exitCode: 1},
			{name: "responses API status 599", environment: sdkAPIErrorEnvironment("responses:599"), output: "sdk_contract_error: javascript_api 599\n", exitCode: 1},
			{name: "API status missing", environment: sdkAPIErrorEnvironment("models:missing"), output: "sdk_contract_error: javascript_api unknown\n", exitCode: 1},
			{name: "API status non-integer", environment: sdkAPIErrorEnvironment("responses:string"), output: "sdk_contract_error: javascript_api unknown\n", exitCode: 1},
			{name: "API status boolean", environment: sdkAPIErrorEnvironment("models:boolean"), output: "sdk_contract_error: javascript_api unknown\n", exitCode: 1},
			{name: "API status below range", environment: sdkAPIErrorEnvironment("responses:399"), output: "sdk_contract_error: javascript_api unknown\n", exitCode: 1},
			{name: "API status above range", environment: sdkAPIErrorEnvironment("models:600"), output: "sdk_contract_error: javascript_api unknown\n", exitCode: 1},
		})
	})
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
		"AI_CLI_GATEWAY_MODEL=codex-sdk-test",
		"AI_CLI_GATEWAY_TIMEOUT_SECONDS=" + timeout,
	}
}

func sdkAPIErrorEnvironment(mode string) []string {
	return append(sdkValidEnvironment("5"), "SDK_CONTRACT_FAKE_API_ERROR="+mode)
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
			if string(output) != test.output {
				t.Fatalf("combined output = %q, want exactly one fixed line %q", output, test.output)
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
        raise_api_error("models")
        return Value(data=[Value(id="codex-sdk-test", object="model", created=0, owned_by="local")])

class Responses:
    def create(self, **request):
        expected = {
            "model": "codex-sdk-test",
            "instructions": "SDK contract instruction.",
            "input": "SDK contract input.",
            "text": {"format": {"type": "text"}},
            "stream": False,
            "store": False,
            "tools": [],
            "tool_choice": "none",
        }
        if request != expected:
            raise AssertionError
        raise_api_error("responses")
        content = Value(type="output_text", annotations=[], text="SDK_GATEWAY_OK\n")
        message = Value(id="msg_fixture", type="message", status="completed", role="assistant", content=[content])
        text = Value(format=Value(type="text"))
        response = Value(
            id="resp_fixture", object="response", created_at=1, completed_at=2, status="completed",
            background=False, error=None, incomplete_details=None, instructions="SDK contract instruction.",
            model="codex-sdk-test", output=[message], parallel_tool_calls=False, previous_response_id=None,
            store=False, text=text, tools=[], tool_choice="none",
        )
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
        raiseAPIError("models");
        return { data: [{ id: "codex-sdk-test", object: "model", created: 0, owned_by: "local" }] };
      },
    };
    this.responses = {
      create: async (request) => {
        const expected = {
          model: "codex-sdk-test",
          instructions: "SDK contract instruction.",
          input: "SDK contract input.",
          text: { format: { type: "text" } },
          stream: false,
          store: false,
          tools: [],
          tool_choice: "none",
        };
        if (JSON.stringify(request) !== JSON.stringify(expected)) {
          throw new Error();
        }
        raiseAPIError("responses");
        const response = {
          id: "resp_fixture",
          object: "response",
          created_at: 1,
          completed_at: 2,
          status: "completed",
          background: false,
          error: null,
          incomplete_details: null,
          instructions: "SDK contract instruction.",
          model: "codex-sdk-test",
          output: [{
            id: "msg_fixture",
            type: "message",
            status: "completed",
            role: "assistant",
            content: [{ type: "output_text", annotations: [], text: "SDK_GATEWAY_OK\n" }],
          }],
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

func TestREADMEOpeningAndOfficialContractSources(t *testing.T) {
	readme := string(readRepositoryFile(t, "README.md"))
	paragraphs := markdownProseParagraphs(readme)
	if len(paragraphs) < 2 {
		t.Fatal("README.md must begin with two prose paragraphs after its title")
	}
	const opening = "AI CLI Gateway turns locally authenticated AI CLIs into an OpenAI Responses-compatible API."
	if paragraphs[0] != opening {
		t.Fatalf("README first prose sentence = %q, want exact opening %q", paragraphs[0], opening)
	}
	if !strings.Contains(paragraphs[1], "**Responses API-compatible subset**") ||
		!strings.Contains(paragraphs[1], "not full OpenAI API compatibility") {
		t.Fatal("README immediate compatibility paragraph does not state the exact subset boundary")
	}

	requireContainsAll(t, "README contract sources", readme,
		"2026-07-30",
		"https://developers.openai.com/api/reference/resources/responses/methods/create",
		"https://developers.openai.com/api/docs/guides/text",
		"https://developers.openai.com/api/docs/guides/structured-outputs",
		"https://developers.openai.com/api/reference/resources/models/methods/list",
		"https://learn.chatgpt.com/docs/non-interactive-mode",
		"https://learn.chatgpt.com/docs/developer-commands?surface=cli#cli-codex-exec",
		"https://learn.chatgpt.com/docs/auth",
		"https://code.claude.com/docs/en/headless",
		"https://code.claude.com/docs/en/cli-usage",
		"https://code.claude.com/docs/en/agent-sdk/typescript",
		"https://code.claude.com/docs/en/env-vars",
		"https://code.claude.com/docs/en/authentication",
		"https://code.claude.com/docs/en/changelog",
		"https://geminicli.com/docs/cli/headless/",
		"https://geminicli.com/docs/cli/cli-reference/",
		"https://geminicli.com/docs/reference/configuration/",
		"https://geminicli.com/docs/get-started/authentication/",
		"https://geminicli.com/docs/cli/session-management/",
		"2026-05-19",
		"https://developers.googleblog.com/an-important-update-transitioning-gemini-cli-to-antigravity-cli/",
		"2026-06-18",
		"https://github.com/google-gemini/gemini-cli/discussions/28017",
		"https://developers.google.com/gemini-code-assist/docs/deprecations/code-assist-individuals",
		"2026-08-02",
		"https://geminicli.com/docs/resources/quota-and-pricing/",
	)

	for _, forbidden := range []string{
		"fully OpenAI API compatible",
		"complete OpenAI API compatibility",
		"drop-in replacement for the OpenAI API",
	} {
		if strings.Contains(readme, forbidden) {
			t.Fatalf("README makes forbidden broad compatibility claim %q", forbidden)
		}
	}
}

func TestREADMEExactAPISubsetExamplesAndErrors(t *testing.T) {
	readme := string(readRepositoryFile(t, "README.md"))
	requireContainsAll(t, "README architecture and endpoints", readme,
		"Client\n  -> POST /v1/responses\n  -> AI CLI Gateway\n  -> Codex / Claude Code / Gemini CLI adapter\n  -> final text or locally validated JSON",
		"POST /v1/responses",
		"GET /v1/models",
		"non-streaming",
		"immutable configured alias snapshot",
		"503 provider_not_ready",
		"no response retrieval endpoint",
		"SSE",
		"tool-call round trip",
		"session",
		"conversation",
		"web UI",
		"database",
	)

	for _, field := range []string{"model", "input", "instructions", "text.format", "stream", "store", "tools", "tool_choice"} {
		if !strings.Contains(readme, "`"+field+"`") {
			t.Fatalf("README request matrix is missing field %q", field)
		}
	}
	requireContainsAll(t, "README request boundary", readme,
		"required nonempty configured alias",
		"required nonempty UTF-8 string",
		"optional UTF-8 string or `null`",
		"absent or exactly `false`",
		"absent or exactly `[]`",
		"absent or exactly `\"none\"`",
		"400 unsupported_parameter",
		"duplicate",
		"trailing",
		"array",
		"multimodal",
		"previous_response_id",
		"metadata",
		"reasoning",
		"generation controls",
		"provider-specific options",
		"background",
	)

	requireContainsAll(t, "README JSON Schema profile", readme,
		"object root",
		"object`, `array`, `string`, `number`, `integer`, `boolean`, and `null`",
		"`type`, `properties`, `required`, `additionalProperties`, and `items`",
		"`enum` and `const`",
		"`minLength`", "`maxLength`", "`minItems`", "`maxItems`",
		"`minProperties`", "`maxProperties`", "`minimum`", "`maximum`",
		"`exclusiveMinimum`", "`exclusiveMaximum`", "`description`", "`title`",
		"additionalProperties:false",
		"every property",
		"required",
		"no references",
		"combinators",
		"patterns",
		"formats",
		"remote",
		"exactly one JSON object",
		"duplicate-free",
		"no repair",
		"fallback",
		"retry",
		"validated string in `output_text.text`",
		"does not invent an `output_json` field",
	)

	const responseCurl = `curl --fail-with-body \
  -H 'Content-Type: application/json' \
  -H "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}" \
  --data-binary @request.json \
  http://127.0.0.1:8080/v1/responses`
	if count := strings.Count(readme, responseCurl); count < 2 {
		t.Fatalf("README contains %d exact response curl examples, want text and schema examples", count)
	}
	requireNearby(t, "README models curl", readme, "/v1/models", "AI_CLI_GATEWAY_API_KEY")
	requireContainsAll(t, "README request examples", readme,
		"\"type\": \"text\"",
		"\"type\": \"json_schema\"",
		"\"strict\": true",
		"\"additionalProperties\": false",
		"\"required\"",
	)

	objects := readmeJSONObjects(t, readme)
	var success map[string]any
	var errorEnvelope map[string]any
	var modelList map[string]any
	for _, object := range objects {
		if object["object"] == "response" {
			success = object
		}
		if object["object"] == "list" {
			modelList = object
		}
		if _, ok := object["error"]; ok && len(object) == 1 {
			errorEnvelope = object
		}
	}
	if success == nil {
		t.Fatal("README has no complete JSON success response example")
	}
	requireExactJSONKeys(t, "success response", success,
		"id", "object", "created_at", "completed_at", "status", "background", "error",
		"incomplete_details", "instructions", "model", "output", "parallel_tool_calls",
		"previous_response_id", "store", "text", "tools", "tool_choice")
	if success["status"] != "completed" || success["background"] != false || success["store"] != false || success["tool_choice"] != "none" {
		t.Fatal("README success response example does not use the stable completed response shape")
	}
	if errorEnvelope == nil {
		t.Fatal("README has no complete stable error-envelope JSON example")
	}
	errorObject, ok := errorEnvelope["error"].(map[string]any)
	if !ok {
		t.Fatal("README error example's error field is not an object")
	}
	requireExactJSONKeys(t, "error envelope", errorObject, "message", "type", "param", "code")
	if modelList == nil {
		t.Fatal("README has no complete model-list JSON example")
	}
	requireExactJSONKeys(t, "model list", modelList, "object", "data")

	wantErrors := map[string][]string{
		"400": {"invalid_json", "invalid_request", "unsupported_parameter", "invalid_json_schema"},
		"401": {"invalid_bearer_key"},
		"404": {"not_found", "model_not_found"},
		"405": {"method_not_allowed"},
		"408": {"request_timeout"},
		"413": {"request_too_large"},
		"415": {"unsupported_media_type"},
		"429": {"server_busy", "queue_full", "provider_rate_limited"},
		"500": {"process_cleanup_failed", "internal_error"},
		"502": {"output_limit_exceeded", "provider_protocol_error", "structured_output_invalid", "provider_failed"},
		"503": {"queue_timeout", "provider_not_ready", "provider_auth_required", "service_shutting_down"},
		"504": {"provider_timeout"},
	}
	for status, codes := range wantErrors {
		for _, code := range codes {
			requireErrorTableRow(t, readme, status, code)
		}
	}
}

func TestREADMECommandsOperationsSecurityAndGeminiBoundary(t *testing.T) {
	readme := string(readRepositoryFile(t, "README.md"))
	const usage = "usage:\n" +
		"  ai-cli-gateway version\n" +
		"  ai-cli-gateway serve --config PATH\n" +
		"  ai-cli-gateway doctor --config PATH [--json]"
	requireContainsAll(t, "README commands", readme,
		"Go 1.26.5",
		usage,
		"ai-cli-gateway doctor --config PATH --json",
		"ai-cli-gateway doctor --json --config PATH",
		"ai-cli-gateway --help",
		"ai-cli-gateway version --help",
		"ai-cli-gateway serve --help",
		"ai-cli-gateway doctor --help",
		"exit status",
		"configuration_invalid",
		"gateway_not_ready: run ai-cli-gateway doctor",
		"doctor_failed",
		"serve_failed: run ai-cli-gateway doctor",
		"Codex `>=0.146.0,<0.147.0`",
		"Claude Code `>=2.1.208,<2.2.0`",
		"Gemini CLI `>=0.53.0,<0.54.0`",
		"drive-absolute",
		"UNC",
		"node.exe",
		"prefix_args",
		".js",
		".mjs",
		"implemented",
		"live-verified",
		"not-ready",
		"not run",
		"unassessed",
	)
	if strings.Contains(readme, "--config=PATH") {
		t.Fatal("README documents unsupported --config=PATH syntax")
	}

	requireContainsAll(t, "README operational defaults", readme,
		"concurrency 1", "queue 32", "16 MiB", "30 seconds", "300 seconds",
		"TERM grace 2 seconds", "cleanup 5 seconds", "HTTP body 1 MiB",
		"input 512 KiB", "instructions 256 KiB", "schema 32 KiB",
		"stdout 2 MiB", "stderr 256 KiB", "final output 1 MiB",
		"exactly one adapter attempt", "no gateway retry", "no fallback",
		"provider-internal", "usage", "cost",
		"listener", "closed", "before", "Gateway", "shutdown",
		"hard HTTP", "force close", "process containment", "drain",
		"loopback", "Bearer", "dedicated", "OS user",
		"argv", "without a shell", "stdin",
		"setsid", "power loss", "SIGKILL", "KillMode=control-group",
		"does not issue", "extract", "copy", "store login tokens",
		"does not log", "prompt", "output", "credentials",
		"separately length-framed", "provider-dependent", "adversarial `input`",
	)

	requireContainsAll(t, "README Gemini boundary", readme,
		"GEMINI_API_KEY", "GOOGLE_API_KEY", "GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION", "disposable",
		"cached personal OAuth", "unsupported", "Gemini Code Assist for individuals",
		"Google AI Pro", "Google AI Ultra", "2026-06-18", "Antigravity",
		"Code Assist Standard", "Enterprise", "paid API-key", "API-key and Vertex tiers",
		"not exhaustive", "availability", "billing tier", "quota", "entitlement",
		"live credential validity", "provider execution is authoritative",
		"configured", "readiness", "local checks only",
	)

	requireContainsAll(t, "README live-provider environment contract", readme,
		"AI_CLI_GATEWAY_LIVE_PROBES",
		"AI_CLI_GATEWAY_LIVE_INFERENCE",
		"AI_CLI_GATEWAY_LIVE_CODEX_INFERENCE",
		"AI_CLI_GATEWAY_LIVE_CLAUDE_INFERENCE",
		"AI_CLI_GATEWAY_LIVE_GEMINI_INFERENCE",
		"AI_CLI_GATEWAY_LIVE_CODEX_EXECUTABLE",
		"AI_CLI_GATEWAY_LIVE_CODEX_CONFIG_HOME",
		"AI_CLI_GATEWAY_LIVE_CODEX_MODEL",
		"AI_CLI_GATEWAY_LIVE_CLAUDE_EXECUTABLE",
		"AI_CLI_GATEWAY_LIVE_CLAUDE_CONFIG_HOME",
		"AI_CLI_GATEWAY_LIVE_CLAUDE_MODEL",
		"AI_CLI_GATEWAY_LIVE_CLAUDE_AUTH_MODE=config_home|api_key",
		"AI_CLI_GATEWAY_LIVE_GEMINI_EXECUTABLE",
		"AI_CLI_GATEWAY_LIVE_GEMINI_CONFIG_HOME",
		"AI_CLI_GATEWAY_LIVE_GEMINI_MODEL",
		"AI_CLI_GATEWAY_LIVE_GEMINI_AUTH_MODE=gemini_api_key|google_api_key|vertex",
		"operator-triggered", "may incur", "usage",
	)
	requireContainsAll(t, "README CI runtime note", readme,
		"Node24", "self-hosted", "actions/runner", "v2.327.1")

	const terms = "You are responsible for installing and authenticating each provider CLI and for using it in accordance with its applicable terms."
	if !strings.Contains(collapseWhitespace(readme), terms) {
		t.Fatal("README is missing the exact short provider terms notice")
	}
}

func TestPublicPolicyContributionSecurityAndIgnoreBoundary(t *testing.T) {
	contributing := string(readRepositoryFile(t, "CONTRIBUTING.md"))
	requireContainsAll(t, "CONTRIBUTING.md", contributing,
		"Go 1.26.5", "golangci-lint v2.12.2", "TDD", "semantic RED", "GREEN",
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
		"coverage*.out", "/config.toml", ".env", ".env.*", "!.env.example",
		"/.codex/", "/.claude/", "/.gemini/", "auth.json", ".credentials.json",
		"credentials.json", "oauth_creds.json", "google_accounts.json",
	}
	gotLines := nonCommentLines(ignore)
	if !reflect.DeepEqual(gotLines, wantLines) {
		t.Fatalf(".gitignore rules = %q, want exact narrow rules %q", gotLines, wantLines)
	}
	for _, forbidden := range []string{".superpowers", "config.example.toml", "docs/", "README", "settings.json", "internal/securitytest"} {
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

func markdownProseParagraphs(document string) []string {
	paragraphs := make([]string, 0)
	current := make([]string, 0)
	flush := func() {
		if len(current) > 0 {
			paragraphs = append(paragraphs, strings.Join(current, " "))
			current = current[:0]
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(document, "\r\n", "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			flush()
			continue
		}
		if strings.HasPrefix(trimmed, "#") || strings.HasPrefix(trimmed, "[![") || strings.HasPrefix(trimmed, "<") {
			flush()
			continue
		}
		current = append(current, trimmed)
	}
	flush()
	return paragraphs
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

func readmeJSONObjects(t *testing.T, readme string) []map[string]any {
	t.Helper()
	blocks := make([]map[string]any, 0)
	remainder := readme
	for {
		start := strings.Index(remainder, "```json\n")
		if start < 0 {
			return blocks
		}
		remainder = remainder[start+len("```json\n"):]
		end := strings.Index(remainder, "\n```")
		if end < 0 {
			t.Fatal("README contains an unterminated JSON code fence")
		}
		var value any
		decoder := json.NewDecoder(strings.NewReader(remainder[:end]))
		decoder.UseNumber()
		if err := decoder.Decode(&value); err != nil {
			t.Fatalf("README JSON example is invalid: %v", err)
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			t.Fatalf("README JSON fence does not end after exactly one value: %v", err)
		}
		if object, ok := value.(map[string]any); ok {
			blocks = append(blocks, object)
		}
		remainder = remainder[end+len("\n```"):]
	}
}

func requireExactJSONKeys(t *testing.T, name string, object map[string]any, keys ...string) {
	t.Helper()
	if len(object) != len(keys) {
		t.Fatalf("%s has %d fields, want exactly %d", name, len(object), len(keys))
	}
	for _, key := range keys {
		if _, ok := object[key]; !ok {
			t.Fatalf("%s is missing field %q", name, key)
		}
	}
}

func requireErrorTableRow(t *testing.T, readme, status, code string) {
	t.Helper()
	for _, line := range strings.Split(readme, "\n") {
		if strings.Contains(line, "|") && strings.Contains(line, "`"+code+"`") && strings.Contains(line, status) {
			return
		}
	}
	t.Fatalf("README stable error table does not map %s to HTTP %s", code, status)
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
	if !regexp.MustCompile(`(?m)^on:\s*$`).MatchString(workflow) ||
		!regexp.MustCompile(`(?m)^  push:\s*$`).MatchString(workflow) ||
		!regexp.MustCompile(`(?m)^  pull_request:\s*$`).MatchString(workflow) {
		t.Fatal("CI must use normal push and pull_request triggers")
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
	wantJobs := map[string]struct{}{"lint": {}, "linux": {}, "macos": {}, "windows": {}, "cross-build": {}}
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
			"actions/checkout@v7", "actions/setup-go@v7",
			"go-version-file: .go-version", "cache: true")
	}

	actionCounts := make(map[string]int)
	for _, line := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- uses:") {
			actionCounts[strings.TrimSpace(strings.TrimPrefix(trimmed, "- uses:"))]++
		}
	}
	wantActionCounts := map[string]int{
		"actions/checkout@v7":              5,
		"actions/setup-go@v7":              5,
		"golangci/golangci-lint-action@v9": 1,
	}
	if !reflect.DeepEqual(actionCounts, wantActionCounts) {
		t.Fatalf("CI actions = %v, want only current pinned majors %v", actionCounts, wantActionCounts)
	}
	requireContainsAll(t, "lint job", jobs["lint"],
		"runs-on: ubuntu-latest", "gofmt -l .", "go vet ./...",
		"golangci/golangci-lint-action@v9", "version: v2.12.2")
	requireContainsAll(t, "Linux job", jobs["linux"],
		"runs-on: ubuntu-latest", "go mod verify", "go test -count=1 ./...",
		"go test -race -count=1 ./...", "go test -tags=integration -count=1 ./...",
		"go test -tags=live -run '^$' ./internal/provider/...", "CGO_ENABLED: 0",
		"go build -trimpath", "RUNNER_TEMP")
	requireContainsAll(t, "macOS job", jobs["macos"],
		"runs-on: macos-latest", "go test -count=1 ./...", "go test -race -count=1 ./...",
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

	if strings.Count(workflow, "-tags=live") != 1 ||
		!strings.Contains(workflow, "go test -tags=live -run '^$' ./internal/provider/...") {
		t.Fatal("CI live-tag coverage must be compile-only and occur exactly once")
	}
	for _, forbidden := range []string{
		"secrets.", "upload-artifact", "download-artifact", "AI_CLI_GATEWAY_LIVE_",
		"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY", "GOOGLE_API_KEY",
		"continue-on-error: true", "allow-failure", "actions/checkout@v6", "actions/setup-go@v6",
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
		if strings.TrimSpace(line) != "" && len(line)-len(strings.TrimLeft(line, " ")) == 0 {
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
		if trimmed != "" && len(line)-len(strings.TrimLeft(line, " ")) == 0 {
			break
		}
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			result = append(result, trimmed)
		}
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

func TestScannerSkipsOnlyTopLevelGitAndSuperpowersMetadata(t *testing.T) {
	root := t.TempDir()
	secret := "actual-" + "credential"
	for _, directory := range []string{".git", ".superpowers"} {
		writeFixtureFile(t, root, filepath.Join(directory, "hidden.env"), []byte("OPENAI_"+"API_KEY="+secret+"\n"))
	}
	writeFixtureFile(t, root, "clean.txt", []byte("clean\n"))
	if err := scanRepository(root); err != nil {
		t.Fatalf("scanRepository() error = %q, want skipped metadata", err)
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
		"ai-cli-gateway", "fake-ai-cli",
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

func TestScannerRejectsDeveloperHomePathsExceptApprovedResearchDocs(t *testing.T) {
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

	root := t.TempDir()
	for _, relative := range []string{
		"docs/superpowers/specs/2026-07-30-ai-cli-gateway-design.md",
		"docs/superpowers/plans/2026-07-30-ai-cli-gateway.md",
	} {
		writeFixtureFile(t, root, relative, []byte("/Users/"+username+"/Dev/spawngate\n"))
	}
	if err := scanRepository(root); err != nil {
		t.Fatalf("scanRepository(approved docs) error = %q, want nil", err)
	}

	secret := "actual-" + "credential"
	writeFixtureFile(t, root, "docs/superpowers/specs/2026-07-30-ai-cli-gateway-design.md", []byte("OPENAI_"+"API_KEY="+secret+"\n"))
	assertFinding(t, scanRepository(root), "secret_assignment", "docs/superpowers/specs/2026-07-30-ai-cli-gateway-design.md", secret)
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
			if relative == ".superpowers" {
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
		if !isApprovedDeveloperPathDocument(filepath.ToSlash(relative)) && hasDeveloperHomePath(contents) {
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
	if base == "config.toml" || base == ".env" || base == "ai-cli-gateway" || base == "fake-ai-cli" {
		return true
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

func isApprovedDeveloperPathDocument(relative string) bool {
	switch relative {
	case "docs/superpowers/specs/2026-07-30-ai-cli-gateway-design.md",
		"docs/superpowers/plans/2026-07-30-ai-cli-gateway.md":
		return true
	default:
		return false
	}
}
