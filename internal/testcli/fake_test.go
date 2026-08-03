package testcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

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
