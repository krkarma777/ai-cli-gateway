package app

import (
	"bytes"
	"strings"
	"testing"
)

func TestWriteInitCompletionRendersSafeReadySavedStepsExactly(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := writeInitCompletion(&output, initCompletion{
		ConfigPath: "/private/setup/config's file.toml",
		BackupPath: "/private/setup/config's file.toml.bak",
		KeyPath:    "/private/setup/gateway's key",
		Listen:     "127.0.0.1:8080",
		Saved:      true,
		Ready:      true,
	})
	if err != nil {
		t.Fatalf("writeInitCompletion() error = %v", err)
	}
	want := "saved_config: \"/private/setup/config's file.toml\"\n" +
		"backup_config: \"/private/setup/config's file.toml.bak\"\n" +
		"gateway_key_file: \"/private/setup/gateway's key\"\n" +
		"client_key_posix: export AI_CLI_GATEWAY_API_KEY=\"$(cat -- '/private/setup/gateway'\\''s key')\"\n" +
		"client_key_powershell: $env:AI_CLI_GATEWAY_API_KEY = [System.IO.File]::ReadAllText('/private/setup/gateway''s key').TrimEnd(\"`r\", \"`n\")\n" +
		"serve_posix: ai-cli-gateway serve --config '/private/setup/config'\\''s file.toml'\n" +
		"serve_powershell: ai-cli-gateway serve --config '/private/setup/config''s file.toml'\n" +
		"request_posix: curl --fail-with-body -H \"Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}\" 'http://127.0.0.1:8080/v1/models'\n" +
		"request_powershell: curl.exe --fail-with-body -H \"Authorization: Bearer $env:AI_CLI_GATEWAY_API_KEY\" 'http://127.0.0.1:8080/v1/models'\n" +
		"setup_ready\n"
	if output.String() != want {
		t.Fatalf("writeInitCompletion() = %q, want %q", output.String(), want)
	}
}

func TestWriteInitCompletionRendersEnvironmentAuthenticationExactly(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := writeInitCompletion(&output, initCompletion{
		ConfigPath: "/private/setup/config.toml",
		KeyEnv:     "CUSTOM_GATEWAY_KEY",
		Listen:     "127.0.0.1:8080",
		Noop:       true,
		Ready:      true,
	})
	if err != nil {
		t.Fatalf("writeInitCompletion() error = %v", err)
	}
	want := "already_current: \"/private/setup/config.toml\"\n" +
		"client_key_posix: export AI_CLI_GATEWAY_API_KEY=\"${CUSTOM_GATEWAY_KEY:?not set}\"\n" +
		"client_key_powershell: $env:AI_CLI_GATEWAY_API_KEY = [System.Environment]::GetEnvironmentVariable('CUSTOM_GATEWAY_KEY')\n" +
		"serve_posix: ai-cli-gateway serve --config '/private/setup/config.toml'\n" +
		"serve_powershell: ai-cli-gateway serve --config '/private/setup/config.toml'\n" +
		"request_posix: curl --fail-with-body -H \"Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}\" 'http://127.0.0.1:8080/v1/models'\n" +
		"request_powershell: curl.exe --fail-with-body -H \"Authorization: Bearer $env:AI_CLI_GATEWAY_API_KEY\" 'http://127.0.0.1:8080/v1/models'\n" +
		"setup_ready\n"
	if output.String() != want {
		t.Fatalf("writeInitCompletion() = %q, want %q", output.String(), want)
	}
}

func TestWriteInitCompletionOmitsAuthenticationForOpenListener(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	err := writeInitCompletion(&output, initCompletion{
		ConfigPath: "/private/setup/config.toml",
		Listen:     "127.0.0.1:8080",
		Noop:       true,
		Ready:      true,
	})
	if err != nil {
		t.Fatalf("writeInitCompletion() error = %v", err)
	}
	want := "already_current: \"/private/setup/config.toml\"\n" +
		"serve_posix: ai-cli-gateway serve --config '/private/setup/config.toml'\n" +
		"serve_powershell: ai-cli-gateway serve --config '/private/setup/config.toml'\n" +
		"request_posix: curl --fail-with-body 'http://127.0.0.1:8080/v1/models'\n" +
		"request_powershell: curl.exe --fail-with-body 'http://127.0.0.1:8080/v1/models'\n" +
		"setup_ready\n"
	if output.String() != want {
		t.Fatalf("writeInitCompletion() = %q, want %q", output.String(), want)
	}
}

func TestWriteInitCompletionDistinguishesNoopAndNotReady(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		view initCompletion
		want string
	}{
		{
			name: "no-op ready",
			view: initCompletion{ConfigPath: "/config.toml", Noop: true, Ready: true},
			want: "already_current: \"/config.toml\"\nsetup_ready\n",
		},
		{
			name: "saved not ready",
			view: initCompletion{ConfigPath: "/config.toml", Saved: true},
			want: "saved_config: \"/config.toml\"\nsetup_saved_but_not_ready\n",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			if err := writeInitCompletion(&output, test.view); err != nil {
				t.Fatalf("writeInitCompletion() error = %v", err)
			}
			if output.String() != test.want {
				t.Fatalf("output = %q, want %q", output.String(), test.want)
			}
		})
	}
}

func TestWriteInitCompletionRejectsUnsafeOrContradictoryStateWithoutWriting(t *testing.T) {
	t.Parallel()

	for _, view := range []initCompletion{
		{},
		{ConfigPath: "/config.toml\nPLANTED", Noop: true, Ready: true},
		{ConfigPath: "/config.toml", Saved: true, Noop: true, Ready: true},
		{ConfigPath: "/config.toml", BackupPath: "/config.toml.bak", Noop: true, Ready: true},
		{ConfigPath: "/config.toml", KeyPath: "relative.key", Saved: true, Ready: true},
		{ConfigPath: "/config.toml", KeyPath: "/gateway.key", KeyEnv: "CUSTOM_GATEWAY_KEY", Saved: true, Ready: true},
		{ConfigPath: "/config.toml", KeyEnv: "lowercase", Saved: true, Ready: true},
		{ConfigPath: "/config.toml", KeyEnv: "BAD-NAME", Saved: true, Ready: true},
		{ConfigPath: "/config.toml", Listen: "bad\nPLANTED", Saved: true, Ready: true},
	} {
		var output bytes.Buffer
		if err := writeInitCompletion(&output, view); err == nil || output.Len() != 0 {
			t.Fatalf("writeInitCompletion(%#v) error/output = %v/%q", view, err, output.String())
		}
	}

	var output bytes.Buffer
	_ = writeInitCompletion(&output, initCompletion{
		ConfigPath: "/config.toml", KeyPath: "/gateway.key", Listen: "127.0.0.1:8080", Saved: true, Ready: true,
	})
	if strings.Contains(output.String(), strings.Repeat("0", 64)) {
		t.Fatal("completion output unexpectedly contains key material")
	}
}
