package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/app"
)

const wantUsage = "usage:\n" +
	"  ai-cli-gateway version\n" +
	"  ai-cli-gateway init [OPTIONS]\n" +
	"  ai-cli-gateway serve [--config PATH]\n" +
	"  ai-cli-gateway doctor [--config PATH] [--json]\n"

func TestRunVersion(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"version"}, bytes.NewReader(nil), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code=%d stderr=%q", code, stderr.String())
	}
	if got, want := stdout.String(), "ai-cli-gateway dev (none, unknown)\n"; got != want {
		t.Fatalf("stdout=%q, want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr=%q, want empty", got)
	}
}

func TestRunHelpWritesExactUsageToStdout(t *testing.T) {
	var stdout, stderr bytes.Buffer

	code := Run([]string{"--help"}, bytes.NewReader(nil), &stdout, &stderr)

	if code != 0 {
		t.Fatalf("code=%d, want 0", code)
	}
	if got := stdout.String(); got != wantUsage {
		t.Fatalf("stdout=%q, want %q", got, wantUsage)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr=%q, want empty", got)
	}
}

func TestRunCommandHelpWritesExactUsageToStdout(t *testing.T) {
	for _, command := range []string{"version", "init", "serve", "doctor"} {
		t.Run(command, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			code := Run([]string{command, "--help"}, bytes.NewReader(nil), &stdout, &stderr)

			if code != 0 {
				t.Fatalf("code=%d, want 0", code)
			}
			if got := stdout.String(); got != wantUsage {
				t.Fatalf("stdout=%q, want %q", got, wantUsage)
			}
			if got := stderr.String(); got != "" {
				t.Fatalf("stderr=%q, want empty", got)
			}
		})
	}
}

func TestRunRejectsNilWritersWithoutPanicking(t *testing.T) {
	tests := []struct {
		name   string
		args   []string
		stdout io.Writer
		stderr io.Writer
	}{
		{name: "nil stdout", args: []string{"version"}, stderr: &bytes.Buffer{}},
		{name: "nil stderr", args: []string{"--help"}, stdout: &bytes.Buffer{}},
		{name: "both nil", args: []string{"unknown"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := Run(tt.args, bytes.NewReader(nil), tt.stdout, tt.stderr)
			if code != 2 {
				t.Fatalf("code=%d, want 2", code)
			}
			if output, ok := tt.stdout.(*bytes.Buffer); ok && output.Len() != 0 {
				t.Fatalf("stdout=%q, want empty", output.String())
			}
			if output, ok := tt.stderr.(*bytes.Buffer); ok && output.Len() != 0 {
				t.Fatalf("stderr=%q, want empty", output.String())
			}
		})
	}
}

func TestRunRejectsNilReaderWithoutDispatchOrOutput(t *testing.T) {
	t.Parallel()

	var stdout, stderr bytes.Buffer
	if code := Run([]string{"version"}, nil, &stdout, &stderr); code != 2 {
		t.Fatalf("Run() code = %d, want 2", code)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("Run() stdout/stderr = %q/%q, want empty", stdout.String(), stderr.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := Run([]string{"unknown"}, bytes.NewReader(nil), &stdout, &stderr)
	if code != 2 {
		t.Fatalf("code=%d, want 2", code)
	}
	if got := stdout.String(); got != "" {
		t.Fatalf("stdout=%q, want empty", got)
	}
	if got := stderr.String(); got != wantUsage {
		t.Fatalf("stderr=%q, want %q", got, wantUsage)
	}
}

func TestRunContextDispatchesAcceptedServeAndDoctorCommands(t *testing.T) {
	type contextKey struct{}
	requestContext := context.WithValue(context.Background(), contextKey{}, "marker")

	tests := []struct {
		name        string
		args        []string
		wantCode    int
		wantPath    string
		wantJSON    bool
		wantServe   int
		wantDoctor  int
		wantDefault int
	}{
		{
			name:        "serve default",
			args:        []string{"serve"},
			wantPath:    "default/config.toml",
			wantServe:   1,
			wantDefault: 1,
		},
		{
			name:      "serve",
			args:      []string{"serve", "--config", "config/local.toml"},
			wantPath:  "config/local.toml",
			wantServe: 1,
		},
		{
			name:       "doctor text",
			args:       []string{"doctor", "--config", "doctor.toml"},
			wantCode:   7,
			wantPath:   "doctor.toml",
			wantDoctor: 1,
		},
		{
			name:        "doctor text default",
			args:        []string{"doctor"},
			wantCode:    7,
			wantPath:    "default/config.toml",
			wantDoctor:  1,
			wantDefault: 1,
		},
		{
			name:        "doctor json default",
			args:        []string{"doctor", "--json"},
			wantCode:    7,
			wantPath:    "default/config.toml",
			wantJSON:    true,
			wantDoctor:  1,
			wantDefault: 1,
		},
		{
			name:       "doctor json after config",
			args:       []string{"doctor", "--config", "doctor.toml", "--json"},
			wantCode:   7,
			wantPath:   "doctor.toml",
			wantJSON:   true,
			wantDoctor: 1,
		},
		{
			name:       "doctor json before config",
			args:       []string{"doctor", "--json", "--config", "doctor.toml"},
			wantCode:   7,
			wantPath:   "doctor.toml",
			wantJSON:   true,
			wantDoctor: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			serveCalls := 0
			doctorCalls := 0
			defaultCalls := 0
			gotPath := ""
			gotJSON := false
			commandSet := commands{
				defaultConfigPath: func() (string, error) {
					defaultCalls++
					return "default/config.toml", nil
				},
				serve: func(ctx context.Context, path string) error {
					serveCalls++
					if ctx != requestContext {
						t.Fatalf("serve context was not forwarded")
					}
					gotPath = path
					return nil
				},
				doctor: func(ctx context.Context, path string, jsonOutput bool, output io.Writer) int {
					doctorCalls++
					if ctx != requestContext {
						t.Fatalf("doctor context was not forwarded")
					}
					if output != &stdout {
						t.Fatalf("doctor stdout was not forwarded")
					}
					gotPath = path
					gotJSON = jsonOutput
					_, _ = io.WriteString(output, "doctor output\n")
					return 7
				},
			}

			code := runContext(requestContext, tt.args, bytes.NewReader(nil), &stdout, &stderr, commandSet)

			if code != tt.wantCode {
				t.Fatalf("code=%d, want %d", code, tt.wantCode)
			}
			if serveCalls != tt.wantServe || doctorCalls != tt.wantDoctor {
				t.Fatalf(
					"serve calls=%d doctor calls=%d, want %d and %d",
					serveCalls,
					doctorCalls,
					tt.wantServe,
					tt.wantDoctor,
				)
			}
			if defaultCalls != tt.wantDefault {
				t.Fatalf("default path calls=%d, want %d", defaultCalls, tt.wantDefault)
			}
			if gotPath != tt.wantPath {
				t.Fatalf("path=%q, want %q", gotPath, tt.wantPath)
			}
			if gotJSON != tt.wantJSON {
				t.Fatalf("json=%t, want %t", gotJSON, tt.wantJSON)
			}
			if tt.wantDoctor == 0 && stdout.Len() != 0 {
				t.Fatalf("stdout=%q, want empty", stdout.String())
			}
			if tt.wantDoctor == 1 && stdout.String() != "doctor output\n" {
				t.Fatalf("stdout=%q, want doctor output", stdout.String())
			}
			if stderr.Len() != 0 {
				t.Fatalf("stderr=%q, want empty", stderr.String())
			}
		})
	}
}

func TestRunContextDefaultConfigPathFailureDoesNotDispatch(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "serve", args: []string{"serve"}},
		{name: "doctor", args: []string{"doctor"}},
		{name: "doctor json", args: []string{"doctor", "--json"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			serveCalls := 0
			doctorCalls := 0
			defaultCalls := 0

			code := runContext(
				context.Background(),
				tt.args,
				bytes.NewReader(nil),
				&stdout,
				&stderr,
				commands{
					defaultConfigPath: func() (string, error) {
						defaultCalls++
						return "", errors.New("private resolver failure")
					},
					serve: func(context.Context, string) error {
						serveCalls++
						return nil
					},
					doctor: func(context.Context, string, bool, io.Writer) int {
						doctorCalls++
						return 0
					},
				},
			)

			if code != 2 {
				t.Fatalf("code=%d, want 2", code)
			}
			if defaultCalls != 1 {
				t.Fatalf("default path calls=%d, want 1", defaultCalls)
			}
			if serveCalls != 0 || doctorCalls != 0 {
				t.Fatalf("serve calls=%d doctor calls=%d, want 0 and 0", serveCalls, doctorCalls)
			}
			if got := stdout.String(); got != "" {
				t.Fatalf("stdout=%q, want empty", got)
			}
			if got, want := stderr.String(), "default_config_path_unavailable: pass --config PATH\n"; got != want {
				t.Fatalf("stderr=%q, want %q", got, want)
			}
		})
	}
}

func TestRunContextNormalizesNilContextBeforeDispatch(t *testing.T) {
	var stdout, stderr bytes.Buffer
	var received context.Context

	//nolint:staticcheck // The CLI boundary explicitly accepts and normalizes nil.
	code := runContext(
		nil,
		[]string{"serve", "--config", "config.toml"},
		bytes.NewReader(nil),
		&stdout,
		&stderr,
		commands{
			serve: func(ctx context.Context, _ string) error {
				received = ctx
				return nil
			},
		},
	)

	if code != 0 {
		t.Fatalf("code=%d, want 0; stderr=%q", code, stderr.String())
	}
	if received == nil {
		t.Fatal("serve received a nil context")
	}
}

func TestRunContextRejectsEveryUnsupportedSyntaxWithoutDispatch(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "empty argv"},
		{name: "config equals", args: []string{"serve", "--config=config.toml"}},
		{name: "doctor config equals", args: []string{"doctor", "--config=config.toml"}},
		{name: "short help", args: []string{"-h"}},
		{name: "help command", args: []string{"help"}},
		{name: "serve missing path", args: []string{"serve", "--config"}},
		{name: "serve empty path", args: []string{"serve", "--config", ""}},
		{name: "serve positional path", args: []string{"serve", "config.toml"}},
		{name: "serve flag path", args: []string{"serve", "--config", "--secret"}},
		{
			name: "duplicate config",
			args: []string{"serve", "--config", "one.toml", "--config", "two.toml"},
		},
		{
			name: "serve json",
			args: []string{"serve", "--config", "config.toml", "--json"},
		},
		{name: "doctor missing path", args: []string{"doctor", "--config"}},
		{name: "doctor empty path", args: []string{"doctor", "--config", ""}},
		{name: "doctor unsupported ordering", args: []string{"doctor", "--json", "doctor.toml"}},
		{
			name: "duplicate json",
			args: []string{"doctor", "--config", "config.toml", "--json", "--json"},
		},
		{
			name: "doctor duplicate config",
			args: []string{"doctor", "--config", "one.toml", "--config", "two.toml"},
		},
		{
			name: "help with config",
			args: []string{"serve", "--help", "--config", "config.toml"},
		},
		{
			name: "config with help",
			args: []string{"serve", "--config", "config.toml", "--help"},
		},
		{name: "unknown flag", args: []string{"doctor", "--unknown"}},
		{name: "positional argument", args: []string{"doctor", "config.toml"}},
		{name: "extra version argument", args: []string{"version", "extra"}},
		{name: "unknown command", args: []string{"launch"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			calls := 0
			defaultCalls := 0
			commandSet := commands{
				defaultConfigPath: func() (string, error) {
					defaultCalls++
					return "default/config.toml", nil
				},
				serve: func(context.Context, string) error {
					calls++
					return nil
				},
				doctor: func(context.Context, string, bool, io.Writer) int {
					calls++
					return 0
				},
			}

			code := runContext(context.Background(), tt.args, bytes.NewReader(nil), &stdout, &stderr, commandSet)

			if code != 2 {
				t.Fatalf("code=%d, want 2", code)
			}
			if calls != 0 {
				t.Fatalf("command calls=%d, want 0", calls)
			}
			if defaultCalls != 0 {
				t.Fatalf("default path calls=%d, want 0", defaultCalls)
			}
			if got := stdout.String(); got != "" {
				t.Fatalf("stdout=%q, want empty", got)
			}
			if got := stderr.String(); got != wantUsage {
				t.Fatalf("stderr=%q, want %q", got, wantUsage)
			}
		})
	}
}

func TestRunContextVersionAndHelpNeverDispatch(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantStdout string
	}{
		{
			name:       "version",
			args:       []string{"version"},
			wantStdout: "ai-cli-gateway dev (none, unknown)\n",
		},
		{name: "global help", args: []string{"--help"}, wantStdout: wantUsage},
		{name: "version help", args: []string{"version", "--help"}, wantStdout: wantUsage},
		{name: "serve help", args: []string{"serve", "--help"}, wantStdout: wantUsage},
		{name: "doctor help", args: []string{"doctor", "--help"}, wantStdout: wantUsage},
		{name: "init help", args: []string{"init", "--help"}, wantStdout: wantUsage},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			calls := 0
			defaultCalls := 0
			commandSet := commands{
				defaultConfigPath: func() (string, error) {
					defaultCalls++
					return "default/config.toml", nil
				},
				serve: func(context.Context, string) error {
					calls++
					return nil
				},
				doctor: func(context.Context, string, bool, io.Writer) int {
					calls++
					return 0
				},
			}

			code := runContext(context.Background(), tt.args, bytes.NewReader(nil), &stdout, &stderr, commandSet)

			if code != 0 {
				t.Fatalf("code=%d, want 0", code)
			}
			if calls != 0 {
				t.Fatalf("command calls=%d, want 0", calls)
			}
			if defaultCalls != 0 {
				t.Fatalf("default path calls=%d, want 0", defaultCalls)
			}
			if got := stdout.String(); got != tt.wantStdout {
				t.Fatalf("stdout=%q, want %q", got, tt.wantStdout)
			}
			if got := stderr.String(); got != "" {
				t.Fatalf("stderr=%q, want empty", got)
			}
		})
	}
}

func TestRunContextNilWriterPreventsDispatch(t *testing.T) {
	for _, tt := range []struct {
		name   string
		stdout io.Writer
		stderr io.Writer
	}{
		{name: "nil stdout", stderr: io.Discard},
		{name: "nil stderr", stdout: io.Discard},
		{name: "both nil"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			code := runContext(
				context.Background(),
				[]string{"serve", "--config", "config.toml"},
				bytes.NewReader(nil),
				tt.stdout,
				tt.stderr,
				commands{
					serve: func(context.Context, string) error {
						calls++
						return nil
					},
				},
			)

			if code != 2 {
				t.Fatalf("code=%d, want 2", code)
			}
			if calls != 0 {
				t.Fatalf("serve calls=%d, want 0", calls)
			}
		})
	}
}

func TestRunContextMapsServeResultsToFixedOutput(t *testing.T) {
	plantedPrivateValue := "PLANTED_PRIVATE_ERROR_PAYLOAD"
	tests := []struct {
		name       string
		err        error
		wantCode   int
		wantStderr string
	}{
		{name: "success"},
		{
			name:       "configuration invalid",
			err:        app.ErrConfigInvalid,
			wantCode:   2,
			wantStderr: "configuration_invalid\n",
		},
		{
			name:       "wrapped configuration invalid",
			err:        fmt.Errorf("%s: %w", plantedPrivateValue, app.ErrConfigInvalid),
			wantCode:   2,
			wantStderr: "configuration_invalid\n",
		},
		{
			name:       "not ready",
			err:        app.ErrNotReady,
			wantCode:   1,
			wantStderr: "gateway_not_ready: run ai-cli-gateway doctor\n",
		},
		{
			name:       "not ready with cleanup failure",
			err:        errors.Join(app.ErrNotReady, app.ErrShutdown),
			wantCode:   1,
			wantStderr: "gateway_not_ready: run ai-cli-gateway doctor\n",
		},
		{
			name:       "startup failure",
			err:        app.ErrStartup,
			wantCode:   1,
			wantStderr: "serve_failed: run ai-cli-gateway doctor\n",
		},
		{
			name:       "serve failure",
			err:        app.ErrServe,
			wantCode:   1,
			wantStderr: "serve_failed: run ai-cli-gateway doctor\n",
		},
		{
			name:       "shutdown failure",
			err:        app.ErrShutdown,
			wantCode:   1,
			wantStderr: "serve_failed: run ai-cli-gateway doctor\n",
		},
		{
			name: "wrapped arbitrary private failure",
			err: fmt.Errorf(
				"outer wrapper: %w",
				errors.New(plantedPrivateValue),
			),
			wantCode:   1,
			wantStderr: "serve_failed: run ai-cli-gateway doctor\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			code := runContext(
				context.Background(),
				[]string{"serve", "--config", "config.toml"},
				bytes.NewReader(nil),
				&stdout,
				&stderr,
				commands{
					serve: func(context.Context, string) error {
						return tt.err
					},
				},
			)

			if code != tt.wantCode {
				t.Fatalf("code=%d, want %d", code, tt.wantCode)
			}
			if got := stdout.String(); got != "" {
				t.Fatalf("stdout=%q, want empty", got)
			}
			if got := stderr.String(); got != tt.wantStderr {
				t.Fatalf("stderr=%q, want %q", got, tt.wantStderr)
			}
		})
	}
}

func TestRunContextProductionCommandsDelegateConfigurationFailures(t *testing.T) {
	missingConfig := t.TempDir() + "/PLANTED_MISSING_CONFIG_SECRET.toml"

	t.Run("serve", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := RunContext(
			context.Background(),
			[]string{"serve", "--config", missingConfig},
			bytes.NewReader(nil),
			&stdout,
			&stderr,
		)

		if code != 2 {
			t.Fatalf("code=%d, want 2", code)
		}
		if got := stdout.String(); got != "" {
			t.Fatalf("stdout=%q, want empty", got)
		}
		if got, want := stderr.String(), "configuration_invalid\n"; got != want {
			t.Fatalf("stderr=%q, want %q", got, want)
		}
	})

	t.Run("doctor", func(t *testing.T) {
		var stdout, stderr bytes.Buffer
		code := RunContext(
			context.Background(),
			[]string{"doctor", "--config", missingConfig, "--json"},
			bytes.NewReader(nil),
			&stdout,
			&stderr,
		)

		if code != 2 {
			t.Fatalf("code=%d, want 2", code)
		}
		if got, want := stdout.String(), "configuration_invalid\n"; got != want {
			t.Fatalf("stdout=%q, want %q", got, want)
		}
		if got := stderr.String(); got != "" {
			t.Fatalf("stderr=%q, want empty", got)
		}
	})
}
