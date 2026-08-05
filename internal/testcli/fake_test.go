package testcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCodexReadyProbeCommands(t *testing.T) {
	features := []string{
		"shell_tool",
		"unified_exec",
		"code_mode_host",
		"apps",
		"plugins",
		"remote_plugin",
		"hooks",
		"multi_agent",
		"browser_use",
		"browser_use_external",
		"computer_use",
		"in_app_browser",
		"image_generation",
		"skill_search",
		"skill_mcp_dependency_install",
		"workspace_dependencies",
	}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "version", args: []string{"--version"}, want: "codex-cli 0.146.0\n"},
		{
			name: "exec help",
			args: []string{"--ask-for-approval", "never", "exec", "--help"},
			want: "PROMPT\n-\n--disable\n-c\n--strict-config\n--sandbox\n--model\n--output-schema\n--color\n--ephemeral\n--ignore-user-config\n--ignore-rules\n--skip-git-repo-check\n",
		},
		{
			name: "features list",
			args: []string{"features", "list"},
			want: func() string {
				lines := make([]string, 0, len(features))
				for _, feature := range features {
					lines = append(lines, feature+" stable false")
				}
				return strings.Join(lines, "\n") + "\n"
			}(),
		},
		{name: "login status", args: []string{"login", "status"}, want: "Logged in\n"},
		{
			name: "doctor json",
			args: []string{"doctor", "--json"},
			want: "{\"schemaVersion\":1,\"overallStatus\":\"ok\",\"checks\":{\"auth.credentials\":{\"id\":\"auth.credentials\",\"status\":\"ok\"},\"config.load\":{\"id\":\"config.load\",\"status\":\"ok\"}}}\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := CodexReadyMain(tt.args, strings.NewReader("private prompt"), &stdout, &stderr); code != 0 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stdout.String() != tt.want || stderr.Len() != 0 {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestCodexReadyRejectsNonexactProbeCommands(t *testing.T) {
	for _, args := range [][]string{
		{"exec", "--ask-for-approval", "never", "--help"},
		{"--ask-for-approval", "never", "exec"},
		{"features", "list", "--extra"},
		{"--version-extra"},
	} {
		var stdout, stderr bytes.Buffer
		if code := CodexReadyMain(args, strings.NewReader("private prompt"), &stdout, &stderr); code != 2 {
			t.Fatalf("args=%q code=%d stdout=%q stderr=%q", args, code, stdout.String(), stderr.String())
		}
		if stdout.Len() != 0 || stderr.String() != "fake-codex-cli: unsupported command\n" {
			t.Fatalf("args=%q stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
	}
}

func TestCodexReadyExactFinalExecution(t *testing.T) {
	args := codexReadyFinalArgs()
	const prompt = "private prompt marker 7fa6"
	var stdout, stderr bytes.Buffer
	code := CodexReadyMain(args, strings.NewReader(prompt), &stdout, &stderr)
	if code != 0 || stdout.String() != "SDK_GATEWAY_OK\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), prompt) || strings.Contains(stderr.String(), prompt) {
		t.Fatalf("prompt exposed stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCodexReadyRejectsInvalidFinalExecution(t *testing.T) {
	base := codexReadyFinalArgs()
	tests := []struct {
		name  string
		args  []string
		stdin string
	}{
		{name: "empty stdin", args: base},
		{name: "output schema", args: append(append([]string(nil), base[:len(base)-1]...), "--output-schema", "schema.json", "-"), stdin: "private prompt"},
		{name: "other model", args: append(append([]string(nil), base[:len(base)-2]...), "other-model", "-"), stdin: "private prompt"},
		{name: "missing terminal dash", args: base[:len(base)-1], stdin: "private prompt"},
		{name: "mode flag", args: append([]string{"--mode=codex-ready"}, base...), stdin: "private prompt"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := CodexReadyMain(tt.args, strings.NewReader(tt.stdin), &stdout, &stderr); code != 2 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 || stderr.String() != "fake-codex-cli: unsupported command\n" {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestCodexReadyRejectsNilFinalHandler(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := CodexReadyMainWithFinal(codexReadyFinalArgs(), strings.NewReader("private prompt"), &stdout, &stderr, nil); code != 2 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.Len() != 0 || stderr.String() != "fake-codex-cli: unsupported command\n" {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCodexReadyRejectsNilStdin(t *testing.T) {
	tests := []struct {
		name string
		run  func(*bytes.Buffer, *bytes.Buffer) int
	}{
		{
			name: "default final handler",
			run: func(stdout, stderr *bytes.Buffer) int {
				return CodexReadyMain(codexReadyFinalArgs(), nil, stdout, stderr)
			},
		},
		{
			name: "custom final handler",
			run: func(stdout, stderr *bytes.Buffer) int {
				called := false
				code := CodexReadyMainWithFinal(
					codexReadyFinalArgs(),
					nil,
					stdout,
					stderr,
					func(io.Reader, io.Writer, io.Writer) int {
						called = true
						return 0
					},
				)
				if called {
					t.Fatal("final handler was called")
				}
				return code
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := tt.run(&stdout, &stderr); code != 2 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 || stderr.String() != "fake-codex-cli: unsupported command\n" {
				t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
			}
		})
	}
}

func TestMainCodexReady(t *testing.T) {
	args := append([]string{"--mode=codex-ready"}, codexReadyFinalArgs()...)
	var stdout, stderr bytes.Buffer
	if code := Main(args, strings.NewReader("private prompt"), &stdout, &stderr); code != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if stdout.String() != "SDK_GATEWAY_OK\n" || stderr.Len() != 0 {
		t.Fatalf("stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestCodexReadyCommandBuilds(t *testing.T) {
	cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "fake-codex-cli"), "./cmd/fake-codex-cli")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build: %v\n%s", err, output)
	}
}

func codexReadyFinalArgs() []string {
	return []string{
		"--ask-for-approval", "never", "exec", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--strict-config", "--sandbox", "read-only", "--skip-git-repo-check", "--color", "never",
		"--disable", "shell_tool", "--disable", "unified_exec", "--disable", "code_mode_host", "--disable", "apps", "--disable", "plugins", "--disable", "remote_plugin", "--disable", "hooks", "--disable", "multi_agent", "--disable", "browser_use", "--disable", "browser_use_external", "--disable", "computer_use", "--disable", "in_app_browser", "--disable", "image_generation", "--disable", "skill_search", "--disable", "skill_mcp_dependency_install", "--disable", "workspace_dependencies",
		"-c", `web_search="disabled"`, "--model", "sdk-contract-model", "-",
	}
}

func TestMainRequiresExactlyOneExplicitMode(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "missing"},
		{name: "missing value", args: []string{"--mode"}},
		{name: "empty equals", args: []string{"--mode="}},
		{name: "duplicate split", args: []string{"--mode", "text", "--mode=text"}},
		{name: "unknown", args: []string{"--mode", "unknown"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Main(tt.args, strings.NewReader(""), &stdout, &stderr); code != 2 {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
			if stdout.Len() != 0 {
				t.Fatalf("stdout=%q", stdout.String())
			}
			if strings.Contains(stderr.String(), "secret") {
				t.Fatalf("stderr=%q", stderr.String())
			}
		})
	}
}

func TestMainParsesModeAnywhereAndIgnoresAdapterFlags(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(
		[]string{"exec", "--fixed-adapter-flag", "value", "--mode", "text", "--model", "safe-model"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if code != 0 || stdout.String() != "hello\n" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestMainEchoesStdin(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(
		[]string{"--mode=echo-stdin"},
		strings.NewReader("length-framed input"),
		&stdout,
		&stderr,
	)
	if code != 0 || stdout.String() != "length-framed input" || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestMainCodexEmptyAndInvalidUTF8Modes(t *testing.T) {
	tests := []struct {
		name       string
		mode       string
		wantStdout []byte
	}{
		{
			name: "empty success",
			mode: "empty-success",
		},
		{
			name:       "invalid UTF-8",
			mode:       "invalid-utf8",
			wantStdout: []byte{0xff},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Main(
				[]string{"--mode=" + test.mode},
				strings.NewReader("private prompt"),
				&stdout,
				&stderr,
			)
			if code != 0 {
				t.Fatalf(
					"code=%d stdout=%q stderr=%q",
					code,
					stdout.Bytes(),
					stderr.Bytes(),
				)
			}
			if !bytes.Equal(stdout.Bytes(), test.wantStdout) {
				t.Fatalf(
					"stdout=%v, want %v",
					stdout.Bytes(),
					test.wantStdout,
				)
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr=%q", stderr.Bytes())
			}
		})
	}
}

func TestMainCodexSchemaProbeReportsOnlyByteCounts(t *testing.T) {
	t.Chdir(t.TempDir())
	schema := []byte(`{"private_marker_781":"value_marker_782"}`)
	prompt := "private prompt\nwith newline"
	if err := os.WriteFile("output-schema.json", schema, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := Main(
		[]string{"--mode=codex-schema-probe"},
		strings.NewReader(prompt),
		&stdout,
		&stderr,
	)
	want := fmt.Sprintf(
		"{\"stdin_bytes\":%d,\"schema_bytes\":%d}\n",
		len(prompt),
		len(schema),
	)
	if code != 0 || stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf(
			"code=%d stdout=%q stderr=%q",
			code,
			stdout.String(),
			stderr.String(),
		)
	}
	for _, secret := range []string{
		"private prompt",
		"private_marker_781",
		"value_marker_782",
	} {
		if strings.Contains(stdout.String(), secret) {
			t.Fatalf("stdout exposed %q: %q", secret, stdout.String())
		}
	}
}

func TestMainFixedOutputModes(t *testing.T) {
	tests := []struct {
		mode       string
		code       int
		wantStdout string
	}{
		{mode: "codex-json", wantStdout: `{"answer":"hello"}` + "\n"},
		{mode: "claude-json", wantStdout: `{"type":"result","subtype":"success","is_error":false,"result":"hello"}` + "\n"},
		{mode: "invalid-json", wantStdout: `{"invalid":`},
		{
			mode: "duplicate-json",
			wantStdout: `{"type":"result","subtype":"success","is_error":false,` +
				`"result":"first","result":"second"}` + "\n",
		},
		{mode: "fenced-json", wantStdout: "```json\n{\"answer\":\"hello\"}\n```\n"},
		{mode: "schema-mismatch", wantStdout: `{"answer":7}` + "\n"},
		{mode: "exit-7", code: 7},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Main([]string{"--mode=" + tt.mode}, strings.NewReader(""), &stdout, &stderr)
			if code != tt.code || stdout.String() != tt.wantStdout {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}

func TestMainClaudeErrorsUsePinnedNumericStatus(t *testing.T) {
	tests := []struct {
		mode   string
		status int
	}{
		{mode: "claude-auth-error", status: 401},
		{mode: "claude-rate-limit", status: 429},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := Main([]string{"--mode", tt.mode}, strings.NewReader(""), &stdout, &stderr)
			if code != 1 {
				t.Fatalf("code=%d stderr=%q", code, stderr.String())
			}
			var envelope struct {
				Type           string `json:"type"`
				Subtype        string `json:"subtype"`
				IsError        bool   `json:"is_error"`
				APIErrorStatus int    `json:"api_error_status"`
				Result         string `json:"result"`
				Errors         any    `json:"errors"`
			}
			if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			if envelope.Type != "result" ||
				envelope.Subtype != "success" ||
				!envelope.IsError ||
				envelope.APIErrorStatus != tt.status ||
				envelope.Result != "fixed discarded provider failure" ||
				envelope.Errors != nil {
				t.Fatalf("envelope=%+v", envelope)
			}
		})
	}
}

func TestMainClaudeExecutionErrorUsesDocumentedErrorArm(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Main(
		[]string{"--mode=claude-execution-error"},
		strings.NewReader("private prompt"),
		&stdout,
		&stderr,
	)
	if code != 1 || stderr.Len() != 0 {
		t.Fatalf(
			"code=%d stdout=%q stderr=%q",
			code,
			stdout.String(),
			stderr.String(),
		)
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"type", "subtype", "is_error", "errors"} {
		if _, ok := envelope[required]; !ok {
			t.Fatalf("envelope missing %q: %s", required, stdout.Bytes())
		}
	}
	for _, forbidden := range []string{"result", "api_error_status"} {
		if _, ok := envelope[forbidden]; ok {
			t.Fatalf("envelope contains %q: %s", forbidden, stdout.Bytes())
		}
	}
	if string(envelope["type"]) != `"result"` ||
		string(envelope["subtype"]) != `"error_during_execution"` ||
		string(envelope["is_error"]) != "true" {
		t.Fatalf("envelope=%s", stdout.Bytes())
	}
	var elements []string
	if err := json.Unmarshal(envelope["errors"], &elements); err != nil {
		t.Fatal(err)
	}
	if len(elements) != 1 || elements[0] != "fixed execution failure" {
		t.Fatalf("errors=%q", elements)
	}
	if strings.Contains(stdout.String(), "private prompt") {
		t.Fatalf("stdout exposed stdin: %q", stdout.String())
	}
}

func TestMainClaudeStdinProbeReturnsOnlyFramedByteCount(t *testing.T) {
	prompt := "private prompt\nwith unicode 한국어"
	var stdout, stderr bytes.Buffer
	code := Main(
		[]string{"--mode=claude-stdin-probe"},
		strings.NewReader(prompt),
		&stdout,
		&stderr,
	)
	want := fmt.Sprintf(
		`{"type":"result","subtype":"success",`+
			`"is_error":false,"result":"stdin_bytes=%d"}`+"\n",
		len(prompt),
	)
	if code != 0 || stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf(
			"code=%d stdout=%q stderr=%q",
			code,
			stdout.String(),
			stderr.String(),
		)
	}
	if strings.Contains(stdout.String(), "private prompt") ||
		strings.Contains(stdout.String(), "한국어") {
		t.Fatalf("stdout exposed stdin: %q", stdout.String())
	}
}

func TestMainGeminiEnvelopes(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := Main([]string{"--mode=gemini-json"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		if stdout.String() != `{"session_id":"fake-session","response":"hello",`+
			`"stats":{"models":{}},"warnings":[]}`+"\n" {
			t.Fatalf("stdout=%q", stdout.String())
		}
	})
	t.Run("error", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := Main([]string{"--mode=gemini-error"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if _, exists := envelope["response"]; exists {
			t.Fatalf("error envelope contains response: %s", stdout.Bytes())
		}
		if string(envelope["session_id"]) != `"fake-session"` {
			t.Fatalf("error envelope session metadata=%s", envelope["session_id"])
		}
		var errorValue struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(envelope["error"], &errorValue); err != nil {
			t.Fatal(err)
		}
		if errorValue.Type == "" {
			t.Fatalf("envelope=%s", stdout.Bytes())
		}
	})
	t.Run("duplicate response", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := Main([]string{"--mode=gemini-duplicate-json"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		if stdout.String() != `{"response":"first","response":"second"}`+"\n" {
			t.Fatalf("stdout=%q", stdout.String())
		}
	})
	t.Run("fenced response", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		if code := Main([]string{"--mode=gemini-fenced-response"}, strings.NewReader(""), &stdout, &stderr); code != 0 {
			t.Fatalf("code=%d stderr=%q", code, stderr.String())
		}
		var envelope struct {
			Response string `json:"response"`
		}
		if err := json.Unmarshal(stdout.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if envelope.Response != "```json\n{\"answer\":\"hello\"}\n```" {
			t.Fatalf("response=%q", envelope.Response)
		}
	})
}

func TestMainGeminiStdinProbeUsesOnlyDisposableSettings(t *testing.T) {
	runtimeDir := t.TempDir()
	t.Chdir(runtimeDir)
	settingsDir := filepath.Join(runtimeDir, ".gemini")
	if err := os.Mkdir(settingsDir, 0o700); err != nil {
		t.Fatal(err)
	}
	settings := []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}}}`)
	if err := os.WriteFile(filepath.Join(settingsDir, "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GEMINI_CLI_HOME", runtimeDir)
	t.Setenv(
		"GEMINI_CLI_SYSTEM_DEFAULTS_PATH",
		filepath.Join(settingsDir, "system-defaults.json"),
	)
	t.Setenv(
		"GEMINI_CLI_SYSTEM_SETTINGS_PATH",
		filepath.Join(settingsDir, "system-settings.json"),
	)
	prompt := "private prompt 한국어\ncredential-value-must-not-print"

	var stdout, stderr bytes.Buffer
	code := Main(
		[]string{"--mode=gemini-stdin-probe"},
		strings.NewReader(prompt),
		&stdout,
		&stderr,
	)
	want := fmt.Sprintf(
		`{"session_id":"fake-session","response":"stdin_bytes=%d auth_type=gemini-api-key settings_secure=true",`+
			`"stats":{"models":{}},"warnings":[]}`+"\n",
		len(prompt),
	)
	if code != 0 || stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	for _, sensitive := range []string{
		"private prompt",
		"credential-value-must-not-print",
		runtimeDir,
		"GEMINI_CLI_HOME",
		"GEMINI_API_KEY",
	} {
		if strings.Contains(stdout.String(), sensitive) || strings.Contains(stderr.String(), sensitive) {
			t.Fatalf("fake output exposed %q", sensitive)
		}
	}
	if runtime.GOOS != "windows" {
		// This negative test deliberately makes the fixture file too permissive.
		//nolint:gosec
		if err := os.Chmod(filepath.Join(settingsDir, "settings.json"), 0o644); err != nil {
			t.Fatal(err)
		}
		stdout.Reset()
		stderr.Reset()
		code = Main(
			[]string{"--mode=gemini-stdin-probe"},
			strings.NewReader(prompt),
			&stdout,
			&stderr,
		)
		if code != 1 || stdout.Len() != 0 || strings.Contains(stderr.String(), runtimeDir) {
			t.Fatalf("insecure mode result code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
		}
	}
}
