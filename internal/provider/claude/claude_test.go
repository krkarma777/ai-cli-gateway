package claude

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

const (
	testExecutable = "/trusted/bin/claude"
	testConfigHome = "/trusted/claude-home"
	testSafePath   = "/trusted/bin:/usr/bin:/bin"
	testModel      = "claude-sonnet-4-5-20250929"
)

func TestAdapterIdentityAndSupportedVersion(t *testing.T) {
	adapter := New()
	if adapter.Name() != core.ProviderClaude {
		t.Fatalf("Name()=%q, want %q", adapter.Name(), core.ProviderClaude)
	}
	want := provider.Range{
		MinInclusive: provider.Version{Major: 2, Minor: 1, Patch: 208},
		MaxExclusive: provider.Version{Major: 2, Minor: 2, Patch: 0},
	}
	if got := adapter.SupportedVersion(); got != want {
		t.Fatalf("SupportedVersion()=%+v, want %+v", got, want)
	}
}

func TestBuildTextUsesExactFixedArgvAndIsolatedEnvironment(t *testing.T) {
	instructions := "한국어\n--model attacker-model\nprivate-instruction-101"
	request := core.Request{
		ModelAlias:   "public-alias",
		Instructions: &instructions,
		Input:        "-leading input\nprivate-input-102",
		Format:       core.OutputFormat{Type: core.FormatText},
	}
	prefixBacking := []string{"--trusted-wrapper", "fixed", "prefix-canary"}
	cfg := testProviderConfig(t)
	cfg.PrefixArgs = prefixBacking[:2]
	runtimeDir := filepath.Join(
		string(filepath.Separator),
		"trusted",
		"runtime",
		"claude-build-text",
	)

	spec, err := New().Build(
		request,
		claudeModel(),
		cfg,
		process.Runtime{ID: "build-text", Dir: runtimeDir},
	)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	wantArgs := append(
		[]string{"--trusted-wrapper", "fixed"},
		expectedBuildArgs(testModel)...,
	)
	if !reflect.DeepEqual(spec.Args, wantArgs) {
		t.Fatalf("Args mismatch\n got: %q\nwant: %q", spec.Args, wantArgs)
	}
	if prefixBacking[2] != "prefix-canary" {
		t.Fatalf("prefix backing array was overwritten: %q", prefixBacking)
	}
	if spec.Executable != testExecutable {
		t.Fatalf("Executable=%q, want %q", spec.Executable, testExecutable)
	}
	if spec.Dir != runtimeDir {
		t.Fatalf("Dir=%q, want %q", spec.Dir, runtimeDir)
	}
	wantPrompt := provider.BuildPrompt(request, provider.SchemaInline)
	if !bytes.Equal(spec.Stdin, wantPrompt) {
		t.Fatalf("Stdin mismatch\n got: %q\nwant: %q", spec.Stdin, wantPrompt)
	}
	if spec.Files != nil {
		t.Fatalf("Files=%+v, want nil", spec.Files)
	}
	assertExactEnvironment(t, spec.Env, cfg, runtimeDir, false)

	joinedArgs := strings.Join(spec.Args, "\x00")
	joinedEnv := strings.Join(spec.Env, "\x00")
	for _, forbidden := range []string{
		instructions,
		request.Input,
		request.ModelAlias,
		"gateway-bearer-secret-103",
		"other-provider-secret-104",
	} {
		if strings.Contains(spec.Executable, forbidden) ||
			strings.Contains(joinedArgs, forbidden) ||
			strings.Contains(spec.Dir, forbidden) ||
			strings.Contains(joinedEnv, forbidden) {
			t.Fatalf("command metadata exposed %q", forbidden)
		}
	}
	if !bytes.Contains(spec.Stdin, []byte(instructions)) ||
		!bytes.Contains(spec.Stdin, []byte(request.Input)) {
		t.Fatal("request bytes did not remain in stdin")
	}
}

func TestBuildSchemaUsesInlinePromptWithoutArgsOrFiles(t *testing.T) {
	description := "private-description-201"
	instructions := "private-instructions-202"
	schema := []byte(
		`{"type":"object","properties":{"private-203":{"type":"string"}}}`,
	)
	request := core.Request{
		Instructions: &instructions,
		Input:        "private-input-204",
		Format: core.OutputFormat{
			Type:        core.FormatJSONSchema,
			Name:        "private-name-205",
			Description: &description,
			Schema:      schema,
		},
	}
	cfg := testProviderConfig(t)
	requestRuntime := process.Runtime{
		ID:  "schema-build",
		Dir: filepath.Join(t.TempDir(), "request-schema-build"),
	}

	spec, err := New().Build(request, claudeModel(), cfg, requestRuntime)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	wantArgs := append(
		append([]string(nil), cfg.PrefixArgs...),
		expectedBuildArgs(testModel)...,
	)
	if !reflect.DeepEqual(spec.Args, wantArgs) {
		t.Fatalf("Args mismatch\n got: %q\nwant: %q", spec.Args, wantArgs)
	}
	if slices.Contains(spec.Args, "--json-schema") {
		t.Fatalf("Args contains --json-schema: %q", spec.Args)
	}
	if spec.Files != nil {
		t.Fatalf("Files=%+v, want nil", spec.Files)
	}
	wantPrompt := provider.BuildPrompt(request, provider.SchemaInline)
	if !bytes.Equal(spec.Stdin, wantPrompt) {
		t.Fatalf("Stdin mismatch\n got: %q\nwant: %q", spec.Stdin, wantPrompt)
	}
	for _, required := range [][]byte{
		[]byte(instructions),
		[]byte(request.Input),
		[]byte(request.Format.Name),
		[]byte(description),
		schema,
	} {
		if !bytes.Contains(spec.Stdin, required) {
			t.Fatalf("stdin omitted inline contract value %q", required)
		}
	}
	joinedArgs := strings.Join(spec.Args, "\x00")
	for _, forbidden := range []string{
		instructions,
		request.Input,
		request.Format.Name,
		description,
		string(schema),
	} {
		if strings.Contains(joinedArgs, forbidden) {
			t.Fatalf("request value exposed in argv: %q", forbidden)
		}
	}
}

func TestBuildAllowsOnlyExplicitAnthropicCredential(t *testing.T) {
	request := core.Request{Format: core.OutputFormat{Type: core.FormatText}}
	runtimeDir := filepath.Join(t.TempDir(), "credential-runtime")

	cfg := testProviderConfig(t)
	cfg.CredentialEnv = []string{"ANTHROPIC_API_KEY"}
	cfg.LookupEnv = func(name string) (string, bool) {
		switch name {
		case "ANTHROPIC_API_KEY":
			return "anthropic-secret-301", true
		case "SystemRoot":
			return `C:\Windows`, true
		default:
			return "planted-unselected-secret-302", true
		}
	}
	spec, err := New().Build(
		request,
		claudeModel(),
		cfg,
		process.Runtime{ID: "credential", Dir: runtimeDir},
	)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	assertExactEnvironment(t, spec.Env, cfg, runtimeDir, true)
	for _, entry := range spec.Env {
		if strings.Contains(entry, "planted-unselected-secret-302") {
			t.Fatalf("unselected lookup value reached environment: %q", entry)
		}
	}

	for _, credentials := range [][]string{
		{""},
		{"anthropic_api_key"},
		{"ANTHROPIC_AUTH_TOKEN"},
		{"ANTHROPIC_API_KEY", "ANTHROPIC_API_KEY"},
		{"ANTHROPIC_API_KEY", "OPENAI_API_KEY"},
	} {
		cfg := testProviderConfig(t)
		cfg.CredentialEnv = credentials
		got, buildErr := New().Build(
			request,
			claudeModel(),
			cfg,
			process.Runtime{ID: "invalid-credential", Dir: runtimeDir},
		)
		if buildErr == nil {
			t.Fatalf("Build() accepted CredentialEnv=%q", credentials)
		}
		if !reflect.DeepEqual(got, process.CommandSpec{}) {
			t.Fatalf("failed Build returned partial spec: %+v", got)
		}
		if strings.Contains(buildErr.Error(), "ANTHROPIC") ||
			strings.Contains(buildErr.Error(), "OPENAI") {
			t.Fatalf("error exposed credential name: %q", buildErr)
		}
	}
}

func TestBuildSelectedCredentialMustBePresentAndNonempty(t *testing.T) {
	request := core.Request{Format: core.OutputFormat{Type: core.FormatText}}
	runtimeDir := filepath.Join(t.TempDir(), "missing-credential-runtime")

	var firstError string
	for _, lookup := range []provider.LookupEnv{
		func(name string) (string, bool) {
			if name == "SystemRoot" {
				return `C:\Windows`, true
			}
			return "", false
		},
		func(name string) (string, bool) {
			if name == "SystemRoot" {
				return `C:\Windows`, true
			}
			return "", true
		},
		nil,
	} {
		cfg := testProviderConfig(t)
		cfg.CredentialEnv = []string{"ANTHROPIC_API_KEY"}
		cfg.LookupEnv = lookup
		spec, err := New().Build(
			request,
			claudeModel(),
			cfg,
			process.Runtime{ID: "missing-credential", Dir: runtimeDir},
		)
		if err == nil || !reflect.DeepEqual(spec, process.CommandSpec{}) {
			t.Fatalf("Build()=(%+v,%v), want zero spec and error", spec, err)
		}
		if firstError == "" {
			firstError = err.Error()
		} else if err.Error() != firstError {
			t.Fatalf("credential failures varied: %q vs %q", firstError, err)
		}
		for _, forbidden := range []string{
			"ANTHROPIC",
			"missing-credential-runtime",
			"identity@example.test",
		} {
			if strings.Contains(err.Error(), forbidden) {
				t.Fatalf("error exposed %q: %q", forbidden, err)
			}
		}
	}
}

func TestBuildEnforcesCompleteFramedPromptDecimalLimit(t *testing.T) {
	cfg := testProviderConfig(t)
	requestRuntime := process.Runtime{
		ID:  "prompt-limit",
		Dir: filepath.Join(t.TempDir(), "prompt-limit-runtime"),
	}
	atLimit := requestWithFramedPromptBytes(t, 10_000_000)

	spec, err := New().Build(atLimit, claudeModel(), cfg, requestRuntime)
	if err != nil {
		t.Fatalf("Build(exact limit) error: %v", err)
	}
	if got := len(spec.Stdin); got != 10_000_000 {
		t.Fatalf("len(Stdin)=%d, want 10000000", got)
	}

	overLimit := atLimit
	overLimit.Input += "S"
	overSpec, overErr := New().Build(
		overLimit,
		claudeModel(),
		cfg,
		requestRuntime,
	)
	if overErr == nil {
		t.Fatal("Build(one byte over limit) unexpectedly succeeded")
	}
	if !reflect.DeepEqual(overSpec, process.CommandSpec{}) {
		t.Fatalf("over-limit Build returned partial spec: %+v", overSpec)
	}
	if strings.Contains(overErr.Error(), overLimit.Input[len(overLimit.Input)-1:]) ||
		strings.Contains(overErr.Error(), requestRuntime.Dir) {
		t.Fatalf("over-limit error exposed request data: %q", overErr)
	}
}

func TestBuildReturnsFreshOwnedDataAcrossConcurrentCalls(t *testing.T) {
	instructions := "owned instructions"
	request := core.Request{
		Instructions: &instructions,
		Input:        "owned input",
		Format: core.OutputFormat{
			Type:   core.FormatJSONSchema,
			Name:   "owned",
			Schema: []byte(`{"type":"object"}`),
		},
	}
	prefixBacking := []string{"--trusted-prefix", "fixed", "prefix-canary"}
	credentialBacking := []string{"ANTHROPIC_API_KEY", "credential-canary"}
	cfg := testProviderConfig(t)
	cfg.PrefixArgs = prefixBacking[:2]
	cfg.CredentialEnv = credentialBacking[:1]
	cfg.LookupEnv = func(name string) (string, bool) {
		switch name {
		case "ANTHROPIC_API_KEY":
			return "anthropic-secret-301", true
		case "SystemRoot":
			return `C:\Windows`, true
		default:
			return "", false
		}
	}
	requestRuntime := process.Runtime{
		ID:  "owned-build",
		Dir: filepath.Join(t.TempDir(), "request-owned-build"),
	}

	const builds = 32
	specs := make([]process.CommandSpec, builds)
	errs := make([]error, builds)
	var wait sync.WaitGroup
	for index := range builds {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			specs[index], errs[index] = New().Build(
				request,
				claudeModel(),
				cfg,
				requestRuntime,
			)
		}()
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("Build[%d]() error: %v", index, err)
		}
	}
	if prefixBacking[2] != "prefix-canary" ||
		credentialBacking[1] != "credential-canary" {
		t.Fatalf(
			"configuration backing arrays changed: prefix=%q credentials=%q",
			prefixBacking,
			credentialBacking,
		)
	}

	secondArgs := slices.Clone(specs[1].Args)
	secondEnv := slices.Clone(specs[1].Env)
	secondStdin := bytes.Clone(specs[1].Stdin)
	specs[0].Args[0] = "mutated-args"
	specs[0].Env[0] = "MUTATED=environment"
	specs[0].Stdin[0] = 'X'
	specs[0].Files = append(specs[0].Files, process.FileSpec{Name: "extra"})

	if !reflect.DeepEqual(specs[1].Args, secondArgs) ||
		!reflect.DeepEqual(specs[1].Env, secondEnv) ||
		!bytes.Equal(specs[1].Stdin, secondStdin) {
		t.Fatal("separate Build results alias mutable command data")
	}
	if cfg.PrefixArgs[0] != "--trusted-prefix" ||
		cfg.CredentialEnv[0] != "ANTHROPIC_API_KEY" ||
		request.Format.Schema[0] != '{' {
		t.Fatal("Build result mutation reached caller-owned data")
	}
}

func TestBuildFailsClosedWithFixedSafeErrors(t *testing.T) {
	baseRequest := core.Request{Format: core.OutputFormat{Type: core.FormatText}}
	baseConfig := testProviderConfig(t)
	requestRuntime := process.Runtime{
		ID:  "invalid-build",
		Dir: filepath.Join(t.TempDir(), "request-invalid-build"),
	}

	tests := []struct {
		name   string
		first  func() (process.CommandSpec, error)
		second func() (process.CommandSpec, error)
	}{
		{
			name: "wrong provider",
			first: func() (process.CommandSpec, error) {
				model := claudeModel()
				model.Provider = core.ProviderCodex
				return New().Build(baseRequest, model, baseConfig, requestRuntime)
			},
			second: func() (process.CommandSpec, error) {
				model := claudeModel()
				model.Provider = core.ProviderGemini
				return New().Build(baseRequest, model, baseConfig, requestRuntime)
			},
		},
		{
			name: "invalid configured model",
			first: func() (process.CommandSpec, error) {
				model := claudeModel()
				model.ProviderModel = "--model planted-model-secret-401"
				return New().Build(baseRequest, model, baseConfig, requestRuntime)
			},
			second: func() (process.CommandSpec, error) {
				model := claudeModel()
				model.ProviderModel = "planted-model-secret-402\nnext"
				return New().Build(baseRequest, model, baseConfig, requestRuntime)
			},
		},
		{
			name: "invalid credential configuration",
			first: func() (process.CommandSpec, error) {
				cfg := baseConfig
				cfg.CredentialEnv = []string{"PLANTED_CLAUDE_TOKEN_403"}
				return New().Build(baseRequest, claudeModel(), cfg, requestRuntime)
			},
			second: func() (process.CommandSpec, error) {
				cfg := baseConfig
				cfg.CredentialEnv = []string{"PLANTED_OTHER_TOKEN_404"}
				return New().Build(baseRequest, claudeModel(), cfg, requestRuntime)
			},
		},
		{
			name: "nil prompt",
			first: func() (process.CommandSpec, error) {
				req := baseRequest
				req.Input = "planted-input-secret-405"
				req.Format.Type = core.FormatType("planted-format-secret-406")
				return New().Build(req, claudeModel(), baseConfig, requestRuntime)
			},
			second: func() (process.CommandSpec, error) {
				req := baseRequest
				req.Input = "planted-input-secret-407"
				req.Format.Type = core.FormatType("planted-format-secret-408")
				return New().Build(req, claudeModel(), baseConfig, requestRuntime)
			},
		},
		{
			name: "invalid environment",
			first: func() (process.CommandSpec, error) {
				cfg := baseConfig
				cfg.SafePath = "planted-safe-path-409\x00tail"
				return New().Build(baseRequest, claudeModel(), cfg, requestRuntime)
			},
			second: func() (process.CommandSpec, error) {
				cfg := baseConfig
				cfg.ConfigHome = "planted-config-home-410\x00tail"
				return New().Build(baseRequest, claudeModel(), cfg, requestRuntime)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			firstSpec, firstErr := test.first()
			secondSpec, secondErr := test.second()
			if firstErr == nil || secondErr == nil {
				t.Fatalf(
					"errors=(%v,%v), specs=(%+v,%+v)",
					firstErr,
					secondErr,
					firstSpec,
					secondSpec,
				)
			}
			if firstErr.Error() != secondErr.Error() {
				t.Fatalf(
					"variable-derived errors: first=%q second=%q",
					firstErr,
					secondErr,
				)
			}
			if !reflect.DeepEqual(firstSpec, process.CommandSpec{}) ||
				!reflect.DeepEqual(secondSpec, process.CommandSpec{}) {
				t.Fatalf(
					"failed Build returned partial specs: %+v %+v",
					firstSpec,
					secondSpec,
				)
			}
			for _, forbidden := range []string{
				"planted",
				"secret",
				"CLAUDE_TOKEN",
				"OTHER_TOKEN",
				"Codex",
				"Gemini",
			} {
				if strings.Contains(firstErr.Error(), forbidden) ||
					strings.Contains(secondErr.Error(), forbidden) {
					t.Fatalf("error exposed %q: %v", forbidden, firstErr)
				}
			}
		})
	}
}

func TestParseReturnsOnlyExactSuccessfulResultString(t *testing.T) {
	request := core.Request{
		Input:  "private-prompt-501",
		Format: core.OutputFormat{Type: core.FormatText},
	}
	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{
			name: "ordinary",
			stdout: `{"type":"result","subtype":"success",` +
				`"is_error":false,"result":"hello"}`,
			want: "hello",
		},
		{
			name: "empty result",
			stdout: `{"type":"result","subtype":"success",` +
				`"is_error":false,"result":""}`,
		},
		{
			name: "whitespace result",
			stdout: `{"type":"result","subtype":"success",` +
				`"is_error":false,"result":"  \n"}`,
			want: "  \n",
		},
		{
			name: "unknown metadata discarded",
			stdout: `{"type":"result","subtype":"success",` +
				`"is_error":false,"result":"exact",` +
				`"session_id":"private-session-502",` +
				`"usage":{"input_tokens":7},` +
				`"modelUsage":{"sonnet":{"costUSD":1.25}},` +
				`"warnings":["private-warning-503"]}`,
			want: "exact",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := New().Parse(request, process.Result{
				Stdout:   []byte(test.stdout),
				Stderr:   []byte("private-stderr-504"),
				ExitCode: 0,
			})
			if err != nil || got != test.want {
				t.Fatalf(
					"Parse()=(%q,%v), want (%q,nil)",
					got,
					err,
					test.want,
				)
			}
		})
	}
}

func TestParseClassifiesSuccessArmAPIStatusesWithClosedPrecedence(
	t *testing.T,
) {
	tests := []struct {
		name         string
		statusField  string
		exitCode     int
		wantCategory provider.ErrorCategory
	}{
		{
			name:         "401 zero",
			statusField:  `,"api_error_status":401`,
			wantCategory: provider.ProviderErrorAuthRequired,
		},
		{
			name:         "401 nonzero",
			statusField:  `,"api_error_status":401`,
			exitCode:     1,
			wantCategory: provider.ProviderErrorAuthRequired,
		},
		{
			name:         "403 zero",
			statusField:  `,"api_error_status":403`,
			wantCategory: provider.ProviderErrorAuthRequired,
		},
		{
			name:         "403 nonzero",
			statusField:  `,"api_error_status":403`,
			exitCode:     7,
			wantCategory: provider.ProviderErrorAuthRequired,
		},
		{
			name:         "429 zero",
			statusField:  `,"api_error_status":429`,
			wantCategory: provider.ProviderErrorRateLimited,
		},
		{
			name:         "429 nonzero",
			statusField:  `,"api_error_status":429`,
			exitCode:     9,
			wantCategory: provider.ProviderErrorRateLimited,
		},
		{
			name:         "unrecognized integer",
			statusField:  `,"api_error_status":500`,
			wantCategory: provider.ProviderErrorFailed,
		},
		{
			name:         "negative integer",
			statusField:  `,"api_error_status":-1`,
			wantCategory: provider.ProviderErrorFailed,
		},
		{
			name:         "null",
			statusField:  `,"api_error_status":null`,
			wantCategory: provider.ProviderErrorFailed,
		},
		{
			name:         "absent",
			wantCategory: provider.ProviderErrorFailed,
		},
		{
			name:         "decimal is impossible",
			statusField:  `,"api_error_status":429.0`,
			wantCategory: provider.ProviderErrorProtocol,
		},
		{
			name:         "exponent is impossible",
			statusField:  `,"api_error_status":4.29e2`,
			wantCategory: provider.ProviderErrorProtocol,
		},
		{
			name:         "string is impossible",
			statusField:  `,"api_error_status":"429"`,
			wantCategory: provider.ProviderErrorProtocol,
		},
		{
			name:         "boolean is impossible",
			statusField:  `,"api_error_status":true`,
			wantCategory: provider.ProviderErrorProtocol,
		},
		{
			name:         "object is impossible",
			statusField:  `,"api_error_status":{"value":429}`,
			wantCategory: provider.ProviderErrorProtocol,
		},
		{
			name:         "array is impossible",
			statusField:  `,"api_error_status":[429]`,
			wantCategory: provider.ProviderErrorProtocol,
		},
		{
			name:         "wrong status nonzero is generic failure",
			statusField:  `,"api_error_status":"429"`,
			exitCode:     1,
			wantCategory: provider.ProviderErrorFailed,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			stdout := `{"type":"result","subtype":"success",` +
				`"is_error":true,"result":"private-result-601"` +
				test.statusField + `}`
			_, err := New().Parse(
				core.Request{
					Input:  "private-prompt-602",
					Format: core.OutputFormat{Type: core.FormatText},
				},
				process.Result{
					Stdout:   []byte(stdout),
					Stderr:   []byte("private-stderr-603"),
					ExitCode: test.exitCode,
				},
			)
			assertProviderCategory(t, err, test.wantCategory)
			assertErrorOmits(
				t,
				err,
				"private-result-601",
				"private-prompt-602",
				"private-stderr-603",
			)
		})
	}
}

func TestParseMapsEveryDocumentedErrorArmToFailed(t *testing.T) {
	for _, subtype := range []string{
		"error_during_execution",
		"error_max_turns",
		"error_max_budget_usd",
		"error_max_structured_output_retries",
	} {
		for _, exitCode := range []int{0, 1} {
			t.Run(subtype+"/exit-"+strconv.Itoa(exitCode), func(t *testing.T) {
				stdout := `{"type":"result","subtype":"` + subtype + `",` +
					`"is_error":true,` +
					`"errors":["private-error-element-701"],` +
					`"session_id":"private-session-702"}`
				_, err := New().Parse(
					core.Request{
						Input:  "private-prompt-703",
						Format: core.OutputFormat{Type: core.FormatText},
					},
					process.Result{
						Stdout:   []byte(stdout),
						Stderr:   []byte("private-stderr-704"),
						ExitCode: exitCode,
					},
				)
				assertProviderCategory(
					t,
					err,
					provider.ProviderErrorFailed,
				)
				assertErrorOmits(
					t,
					err,
					"private-error-element-701",
					"private-session-702",
					"private-prompt-703",
					"private-stderr-704",
				)
			})
		}
	}
}

func TestParseRejectsMalformedAndImpossibleZeroExitEnvelopes(t *testing.T) {
	tests := []struct {
		name   string
		stdout []byte
	}{
		{name: "empty"},
		{name: "invalid UTF-8", stdout: []byte{'{', '"', 0xff, '"', '}'}},
		{name: "malformed", stdout: []byte(`{"type":"result"`)},
		{
			name: "duplicate root",
			stdout: []byte(`{"type":"result","type":"result",` +
				`"subtype":"success","is_error":false,"result":"x"}`),
		},
		{
			name: "duplicate nested metadata",
			stdout: []byte(`{"type":"result","subtype":"success",` +
				`"is_error":false,"result":"x",` +
				`"usage":{"private":1,"private":2}}`),
		},
		{
			name: "trailing value",
			stdout: []byte(`{"type":"result","subtype":"success",` +
				`"is_error":false,"result":"x"} {}`),
		},
		{name: "root array", stdout: []byte(`[]`)},
		{name: "root scalar", stdout: []byte(`true`)},
		{
			name: "missing type",
			stdout: []byte(`{"subtype":"success",` +
				`"is_error":false,"result":"x"}`),
		},
		{
			name: "wrong type field",
			stdout: []byte(`{"type":7,"subtype":"success",` +
				`"is_error":false,"result":"x"}`),
		},
		{
			name: "unknown type",
			stdout: []byte(`{"type":"assistant","subtype":"success",` +
				`"is_error":false,"result":"x"}`),
		},
		{
			name: "missing subtype",
			stdout: []byte(`{"type":"result",` +
				`"is_error":false,"result":"x"}`),
		},
		{
			name: "wrong subtype field",
			stdout: []byte(`{"type":"result","subtype":7,` +
				`"is_error":false,"result":"x"}`),
		},
		{
			name: "unknown subtype",
			stdout: []byte(`{"type":"result","subtype":"private-subtype",` +
				`"is_error":true,"errors":[]}`),
		},
		{
			name: "missing is error",
			stdout: []byte(`{"type":"result","subtype":"success",` +
				`"result":"x"}`),
		},
		{
			name: "wrong is error",
			stdout: []byte(`{"type":"result","subtype":"success",` +
				`"is_error":"false","result":"x"}`),
		},
		{
			name: "success missing result",
			stdout: []byte(`{"type":"result","subtype":"success",` +
				`"is_error":false}`),
		},
		{
			name: "success wrong result",
			stdout: []byte(`{"type":"result","subtype":"success",` +
				`"is_error":false,"result":7}`),
		},
		{
			name: "successful arm with status",
			stdout: []byte(`{"type":"result","subtype":"success",` +
				`"is_error":false,"result":"x",` +
				`"api_error_status":null}`),
		},
		{
			name: "successful arm with errors",
			stdout: []byte(`{"type":"result","subtype":"success",` +
				`"is_error":false,"result":"x","errors":[]}`),
		},
		{
			name: "API failure missing result",
			stdout: []byte(`{"type":"result","subtype":"success",` +
				`"is_error":true,"api_error_status":401}`),
		},
		{
			name: "API failure wrong result",
			stdout: []byte(`{"type":"result","subtype":"success",` +
				`"is_error":true,"result":{},"api_error_status":401}`),
		},
		{
			name: "API failure with errors",
			stdout: []byte(`{"type":"result","subtype":"success",` +
				`"is_error":true,"result":"x",` +
				`"api_error_status":401,"errors":[]}`),
		},
		{
			name: "error arm false",
			stdout: []byte(`{"type":"result",` +
				`"subtype":"error_during_execution",` +
				`"is_error":false,"errors":[]}`),
		},
		{
			name: "error arm missing errors",
			stdout: []byte(`{"type":"result",` +
				`"subtype":"error_during_execution",` +
				`"is_error":true}`),
		},
		{
			name: "error arm wrong errors",
			stdout: []byte(`{"type":"result",` +
				`"subtype":"error_during_execution",` +
				`"is_error":true,"errors":{}}`),
		},
		{
			name: "error arm with result",
			stdout: []byte(`{"type":"result",` +
				`"subtype":"error_during_execution",` +
				`"is_error":true,"errors":[],"result":"x"}`),
		},
		{
			name: "error arm with status",
			stdout: []byte(`{"type":"result",` +
				`"subtype":"error_during_execution",` +
				`"is_error":true,"errors":[],` +
				`"api_error_status":401}`),
		},
		{
			name: "number token limit",
			stdout: []byte(`{"type":"result","subtype":"success",` +
				`"is_error":true,"result":"x",` +
				`"api_error_status":123456789012345678901}`),
		},
	}

	request := core.Request{
		Input:  "private-prompt-801",
		Format: core.OutputFormat{Type: core.FormatText},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New().Parse(request, process.Result{
				Stdout:   test.stdout,
				Stderr:   []byte("private-stderr-802"),
				ExitCode: 0,
			})
			assertProviderCategory(t, err, provider.ProviderErrorProtocol)
			assertErrorOmits(
				t,
				err,
				string(test.stdout),
				request.Input,
				"private-stderr-802",
				"private",
			)
		})
	}
}

func TestParseNonzeroExitFallsBackToFailedWithoutParsingOutput(t *testing.T) {
	tests := [][]byte{
		nil,
		{0xff},
		[]byte(`{"private-malformed":`),
		[]byte(`{"type":"result","subtype":"success",` +
			`"is_error":false,"result":"would-be-success"}`),
		[]byte(`{"type":"result","subtype":"success",` +
			`"is_error":true,"result":"private","api_error_status":"429"}`),
		[]byte(`{"type":"result","subtype":"success",` +
			`"is_error":true,"api_error_status":401}`),
		[]byte(`{"type":"result","subtype":"success",` +
			`"is_error":true,"result":"private","api_error_status":401,` +
			`"errors":[]}`),
		[]byte(`{"type":"result","subtype":"success",` +
			`"is_error":false,"result":"private","api_error_status":401}`),
		[]byte(`{"type":"result","subtype":"error_during_execution",` +
			`"is_error":true,"errors":[],"api_error_status":401}`),
		[]byte(`{"type":"result","subtype":"private",` +
			`"is_error":true,"errors":[]}`),
	}
	for index, stdout := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			request := core.Request{
				Input:  "private-prompt-901",
				Format: core.OutputFormat{Type: core.FormatText},
			}
			_, err := New().Parse(request, process.Result{
				Stdout:   stdout,
				Stderr:   []byte("private-stderr-902"),
				ExitCode: 7,
			})
			assertProviderCategory(t, err, provider.ProviderErrorFailed)
			assertErrorOmits(
				t,
				err,
				string(stdout),
				request.Input,
				"private-stderr-902",
				"private",
			)
		})
	}
}

func TestProbeBuildsExactlyThreeIsolatedCommandsInOrder(t *testing.T) {
	cfg := testProviderConfig(t)
	prefixBacking := []string{"--trusted-probe-prefix", "fixed", "probe-canary"}
	cfg.PrefixArgs = prefixBacking[:2]
	runner := &scriptedProbeRunner{
		t:           t,
		steps:       healthyProbeSteps(),
		mutateBuilt: true,
	}

	//nolint:staticcheck // Exercise the adapter's explicit nil-context contract.
	health := New().Probe(nil, cfg, runner)
	assertHealthyClaude(t, health)

	wantCommands := [][]string{
		{"--version"},
		append(expectedBuildArgs("sonnet"), "--help"),
		{"auth", "status"},
	}
	if len(runner.specs) != len(wantCommands) {
		t.Fatalf("probe count=%d, want %d", len(runner.specs), len(wantCommands))
	}
	for index, wantCommand := range wantCommands {
		spec := runner.specs[index]
		wantArgs := append(
			[]string{"--trusted-probe-prefix", "fixed"},
			wantCommand...,
		)
		if !reflect.DeepEqual(spec.Args, wantArgs) {
			t.Fatalf(
				"probe[%d] Args=%q, want %q",
				index,
				spec.Args,
				wantArgs,
			)
		}
		if spec.Executable != cfg.Executable {
			t.Fatalf("probe[%d] Executable=%q", index, spec.Executable)
		}
		if spec.Dir != runner.runtimes[index].Dir {
			t.Fatalf(
				"probe[%d] Dir=%q, want %q",
				index,
				spec.Dir,
				runner.runtimes[index].Dir,
			)
		}
		assertExactEnvironment(t, spec.Env, cfg, runner.runtimes[index].Dir, false)
		if spec.Stdin != nil {
			t.Fatalf("probe[%d] Stdin=%q, want nil", index, spec.Stdin)
		}
		if spec.Files != nil {
			t.Fatalf("probe[%d] Files=%+v, want nil", index, spec.Files)
		}
	}
	if prefixBacking[2] != "probe-canary" ||
		!reflect.DeepEqual(
			cfg.PrefixArgs,
			[]string{"--trusted-probe-prefix", "fixed"},
		) {
		t.Fatalf("probe mutated configured PrefixArgs: %q", prefixBacking)
	}
	for index := 1; index < len(runner.runtimes); index++ {
		if runner.runtimes[index].Dir == runner.runtimes[index-1].Dir {
			t.Fatalf("probe runtimes were reused: %+v", runner.runtimes)
		}
	}
}

func TestProbeStopsAfterVersionGateFailure(t *testing.T) {
	tests := []struct {
		name        string
		result      process.Result
		err         error
		wantVersion string
		wantProblem string
	}{
		{
			name:        "runner error",
			err:         errors.New("private version runner error"),
			wantProblem: provider.ProblemVersionUnreadable,
		},
		{
			name: "nonzero",
			result: process.Result{
				Stdout:   []byte("claude 2.1.208\n"),
				ExitCode: 1,
			},
			wantProblem: provider.ProblemVersionUnreadable,
		},
		{
			name:        "malformed",
			result:      process.Result{Stdout: []byte("claude private")},
			wantProblem: provider.ProblemVersionUnreadable,
		},
		{
			name: "ambiguous",
			result: process.Result{
				Stdout: []byte("claude 2.1.208 and 2.1.209\n"),
			},
			wantProblem: provider.ProblemVersionUnreadable,
		},
		{
			name: "below minimum",
			result: process.Result{
				Stdout: []byte("claude 2.1.207\n"),
			},
			wantVersion: "2.1.207",
			wantProblem: provider.ProblemVersionUnsupported,
		},
		{
			name: "exclusive maximum",
			result: process.Result{
				Stdout: []byte("claude 2.2.0\n"),
			},
			wantVersion: "2.2.0",
			wantProblem: provider.ProblemVersionUnsupported,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[0] = scriptedProbeStep{result: test.result, err: test.err}
			runner := &scriptedProbeRunner{t: t, steps: steps}

			health := New().Probe(
				context.Background(),
				testProviderConfig(t),
				runner,
			)

			if runner.calls != 1 || len(runner.specs) != 1 {
				t.Fatalf(
					"runner calls/specs=%d/%d, want 1/1",
					runner.calls,
					len(runner.specs),
				)
			}
			if got := runner.specs[0].Args[len(runner.specs[0].Args)-1]; got != "--version" {
				t.Fatalf("version probe final arg=%q", got)
			}
			assertProbeOutcome(
				t,
				health,
				provider.HealthNotReady,
				"unknown",
				[]string{
					test.wantProblem,
					provider.ProblemCapabilityMissing,
					provider.ProblemAuthUnknown,
				},
				false,
			)
			if health.Version != test.wantVersion {
				t.Fatalf("Version=%q, want %q", health.Version, test.wantVersion)
			}
			assertHealthDoesNotContain(t, health, "private", "runner error")
		})
	}
}

func TestProbeRequiresExactHelpTokens(t *testing.T) {
	for _, token := range requiredProbeHelpTokens() {
		token := token
		t.Run("missing_"+probeTestName(token), func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[1].result.Stdout = []byte(
				helpOutputReplacing(token, ""),
			)
			assertProbeOutcome(
				t,
				probeWithSteps(t, steps),
				provider.HealthNotReady,
				"authenticated",
				[]string{provider.ProblemCapabilityMissing},
				false,
			)
		})
	}

	for _, replacement := range []string{
		"--tools-evil",
		"prefix--toolssuffix",
		"--tools_unsafe",
	} {
		t.Run("tools collision "+probeTestName(replacement), func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[1].result.Stdout = []byte(
				helpOutputReplacing("--tools", replacement),
			)
			assertProbeOutcome(
				t,
				probeWithSteps(t, steps),
				provider.HealthNotReady,
				"authenticated",
				[]string{provider.ProblemCapabilityMissing},
				false,
			)
		})
	}
}

func TestProbeVersionParsingAndRangeAreFailClosed(t *testing.T) {
	tests := []struct {
		name         string
		result       process.Result
		err          error
		wantVersion  string
		wantProblem  string
		wantStatus   provider.HealthStatus
		capabilities bool
	}{
		{
			name: "minimum",
			result: process.Result{
				Stdout:   []byte("2.1.208 (Claude Code)\n"),
				ExitCode: 0,
			},
			wantVersion:  "2.1.208",
			wantStatus:   provider.HealthReady,
			capabilities: true,
		},
		{
			name: "last patch in minor",
			result: process.Result{
				Stdout:   []byte("claude 2.1.999\n"),
				ExitCode: 0,
			},
			wantVersion:  "2.1.999",
			wantStatus:   provider.HealthReady,
			capabilities: true,
		},
		{
			name: "below minimum",
			result: process.Result{
				Stdout:   []byte("claude 2.1.207\n"),
				ExitCode: 0,
			},
			wantVersion:  "2.1.207",
			wantProblem:  provider.ProblemVersionUnsupported,
			wantStatus:   provider.HealthNotReady,
			capabilities: false,
		},
		{
			name: "exclusive maximum",
			result: process.Result{
				Stdout:   []byte("claude 2.2.0\n"),
				ExitCode: 0,
			},
			wantVersion:  "2.2.0",
			wantProblem:  provider.ProblemVersionUnsupported,
			wantStatus:   provider.HealthNotReady,
			capabilities: false,
		},
		{
			name: "unreadable",
			result: process.Result{
				Stdout:   []byte("private-version-output"),
				ExitCode: 0,
			},
			wantProblem:  provider.ProblemVersionUnreadable,
			wantStatus:   provider.HealthNotReady,
			capabilities: false,
		},
		{
			name: "invalid UTF-8",
			result: process.Result{
				Stdout:   []byte{0xff},
				ExitCode: 0,
			},
			wantProblem:  provider.ProblemVersionUnreadable,
			wantStatus:   provider.HealthNotReady,
			capabilities: false,
		},
		{
			name: "nonzero",
			result: process.Result{
				Stdout:   []byte("claude 2.1.208"),
				ExitCode: 1,
			},
			wantProblem:  provider.ProblemVersionUnreadable,
			wantStatus:   provider.HealthNotReady,
			capabilities: false,
		},
		{
			name:         "runner error",
			err:          errors.New("private version runner error"),
			wantProblem:  provider.ProblemVersionUnreadable,
			wantStatus:   provider.HealthNotReady,
			capabilities: false,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[0] = scriptedProbeStep{
				result: test.result,
				err:    test.err,
			}
			health := probeWithSteps(t, steps)
			wantProblems := []string(nil)
			wantAuth := "authenticated"
			if test.wantProblem != "" {
				wantAuth = "unknown"
				wantProblems = []string{
					test.wantProblem,
					provider.ProblemCapabilityMissing,
					provider.ProblemAuthUnknown,
				}
			}
			assertProbeOutcome(
				t,
				health,
				test.wantStatus,
				wantAuth,
				wantProblems,
				test.capabilities,
			)
			if health.Version != test.wantVersion {
				t.Fatalf(
					"Version=%q, want %q; health=%+v",
					health.Version,
					test.wantVersion,
					health,
				)
			}
			assertHealthDoesNotContain(
				t,
				health,
				"private",
				"runner error",
				string([]byte{0xff}),
			)
		})
	}
}

func TestProbeHelpFailuresAreCapabilityMissing(t *testing.T) {
	tests := []scriptedProbeStep{
		{
			result: process.Result{
				Stdout:   []byte(healthyProbeHelpOutput()),
				ExitCode: 2,
			},
		},
		{
			result: process.Result{
				Stdout:   []byte{0xff},
				ExitCode: 0,
			},
		},
		{err: errors.New("private help runner error")},
	}
	for index, failure := range tests {
		t.Run(string(rune('a'+index)), func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[1] = failure
			health := probeWithSteps(t, steps)
			assertProbeOutcome(
				t,
				health,
				provider.HealthNotReady,
				"authenticated",
				[]string{provider.ProblemCapabilityMissing},
				false,
			)
			assertHealthDoesNotContain(t, health, "private", string([]byte{0xff}))
		})
	}
}

func TestProbeAuthUsesOnlyDocumentedExitOutcome(t *testing.T) {
	tests := []struct {
		name         string
		exitCode     int
		err          error
		stdout       []byte
		stderr       []byte
		wantStatus   provider.HealthStatus
		wantAuth     string
		wantProblems []string
	}{
		{
			name:       "zero ignores missing prose",
			exitCode:   0,
			stdout:     []byte(`{"loggedIn":false,"email":"private@example.test"}`),
			stderr:     []byte("private auth stderr"),
			wantStatus: provider.HealthReady,
			wantAuth:   "authenticated",
		},
		{
			name:       "zero ignores invalid UTF-8",
			exitCode:   0,
			stdout:     []byte{0xff},
			stderr:     []byte{0xfe},
			wantStatus: provider.HealthReady,
			wantAuth:   "authenticated",
		},
		{
			name:       "one ignores authenticated prose",
			exitCode:   1,
			stdout:     []byte(`{"loggedIn":true,"email":"private@example.test"}`),
			stderr:     []byte("private authenticated stderr"),
			wantStatus: provider.HealthNotReady,
			wantAuth:   "missing",
			wantProblems: []string{
				provider.ProblemAuthMissing,
			},
		},
		{
			name:       "other exit is unknown",
			exitCode:   2,
			stdout:     []byte("authenticated private@example.test"),
			stderr:     []byte("private stderr"),
			wantStatus: provider.HealthUnknown,
			wantAuth:   "unknown",
			wantProblems: []string{
				provider.ProblemAuthUnknown,
			},
		},
		{
			name:       "runner error is unknown",
			err:        errors.New("private auth runner error"),
			stdout:     []byte("authenticated private@example.test"),
			stderr:     []byte("private stderr"),
			wantStatus: provider.HealthUnknown,
			wantAuth:   "unknown",
			wantProblems: []string{
				provider.ProblemAuthUnknown,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[2] = scriptedProbeStep{
				result: process.Result{
					Stdout:   test.stdout,
					Stderr:   test.stderr,
					ExitCode: test.exitCode,
				},
				err: test.err,
			}
			health := probeWithSteps(t, steps)
			assertProbeOutcome(
				t,
				health,
				test.wantStatus,
				test.wantAuth,
				test.wantProblems,
				true,
			)
			assertHealthDoesNotContain(
				t,
				health,
				"private",
				"loggedIn",
				string([]byte{0xff}),
				string([]byte{0xfe}),
			)
		})
	}
}

func TestProbeInvalidConfigurationFailsAllCommandsClosed(t *testing.T) {
	for _, mutate := range []func(*provider.ProviderConfig){
		func(cfg *provider.ProviderConfig) {
			cfg.CredentialEnv = []string{"PRIVATE_CLAUDE_TOKEN"}
		},
		func(cfg *provider.ProviderConfig) {
			cfg.SafePath = "private\x00path"
		},
		func(cfg *provider.ProviderConfig) {
			cfg.CredentialEnv = []string{"ANTHROPIC_API_KEY"}
			cfg.LookupEnv = nil
		},
	} {
		cfg := testProviderConfig(t)
		mutate(&cfg)
		runner := &scriptedProbeRunner{
			t:     t,
			steps: healthyProbeSteps(),
		}
		health := New().Probe(context.Background(), cfg, runner)
		assertProbeOutcome(
			t,
			health,
			provider.HealthNotReady,
			"unknown",
			[]string{
				provider.ProblemVersionUnreadable,
				provider.ProblemCapabilityMissing,
				provider.ProblemAuthUnknown,
			},
			false,
		)
		if runner.calls != 1 {
			t.Fatalf("runner calls=%d, want 1", runner.calls)
		}
		assertHealthDoesNotContain(
			t,
			health,
			"PRIVATE",
			"ANTHROPIC",
			"private",
		)
	}
}

func TestProbeNilRunnerIsFixedRedactedAndNotReady(t *testing.T) {
	health := New().Probe(
		context.Background(),
		testProviderConfig(t),
		nil,
	)
	assertProbeOutcome(
		t,
		health,
		provider.HealthNotReady,
		"unknown",
		[]string{
			provider.ProblemVersionUnreadable,
			provider.ProblemCapabilityMissing,
			provider.ProblemAuthUnknown,
		},
		false,
	)
}

func TestProbeVersionFailureReturnsFixedProblemsAndStopsFurtherProbes(t *testing.T) {
	steps := healthyProbeSteps()
	steps[0].err = errors.New("private version error")
	steps[1].err = errors.New("private help error")
	steps[2].result.ExitCode = 1
	runner := &scriptedProbeRunner{t: t, steps: steps}

	health := New().Probe(
		context.Background(),
		testProviderConfig(t),
		runner,
	)
	assertProbeOutcome(
		t,
		health,
		provider.HealthNotReady,
		"unknown",
		[]string{
			provider.ProblemVersionUnreadable,
			provider.ProblemCapabilityMissing,
			provider.ProblemAuthUnknown,
		},
		false,
	)
	if runner.calls != 1 {
		t.Fatalf("runner calls=%d, want version only", runner.calls)
	}
	assertHealthDoesNotContain(t, health, "private")
}

func TestProbeResultsAreDeterministicFreshAndRedacted(t *testing.T) {
	firstSteps := healthyProbeSteps()
	firstSteps[0].result.Stdout = []byte(
		"claude 2.1.999 private-version-output",
	)
	firstSteps[1].result.Stderr = []byte("private-help-stderr")
	firstSteps[2].result.ExitCode = 1
	firstSteps[2].result.Stdout = []byte(
		`{"loggedIn":true,"email":"private@example.test"}`,
	)
	firstSteps[2].result.Stderr = []byte("private-auth-stderr")
	first := probeWithSteps(t, firstSteps)

	secondSteps := healthyProbeSteps()
	secondSteps[0].result.Stdout = bytes.Clone(firstSteps[0].result.Stdout)
	secondSteps[1].result.Stderr = bytes.Clone(firstSteps[1].result.Stderr)
	secondSteps[2].result.ExitCode = firstSteps[2].result.ExitCode
	secondSteps[2].result.Stdout = bytes.Clone(firstSteps[2].result.Stdout)
	secondSteps[2].result.Stderr = bytes.Clone(firstSteps[2].result.Stderr)
	second := probeWithSteps(t, secondSteps)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("nondeterministic health\nfirst=%+v\nsecond=%+v", first, second)
	}
	assertHealthDoesNotContain(t, first, "private", "loggedIn")

	first.Capabilities[0] = "mutated-capability"
	first.Problems[0] = "mutated-problem"
	if second.Capabilities[0] != "stdin_prompt" ||
		second.Problems[0] != provider.ProblemAuthMissing {
		t.Fatalf("Probe results alias global slices: %+v", second)
	}
}

type scriptedProbeStep struct {
	result process.Result
	err    error
}

type scriptedProbeRunner struct {
	t           *testing.T
	steps       []scriptedProbeStep
	specs       []process.CommandSpec
	runtimes    []process.Runtime
	calls       int
	mutateBuilt bool
}

func (r *scriptedProbeRunner) RunProbe(
	ctx context.Context,
	build func(process.Runtime) (process.CommandSpec, error),
) (process.Result, error) {
	r.t.Helper()
	if ctx == nil {
		r.t.Fatal("Probe passed a nil context")
	}
	index := r.calls
	r.calls++
	if index >= len(r.steps) {
		return process.Result{}, errors.New("unexpected extra probe")
	}
	requestRuntime := process.Runtime{
		ID: fmt.Sprintf("probe-%08d", index),
		Dir: filepath.Join(
			string(filepath.Separator),
			"trusted",
			"probe-runtime",
			fmt.Sprintf("request-%08d", index),
		),
	}
	spec, err := build(requestRuntime)
	if err != nil {
		return process.Result{}, err
	}
	r.specs = append(r.specs, cloneCommandSpec(spec))
	r.runtimes = append(r.runtimes, requestRuntime)
	if r.mutateBuilt {
		if len(spec.Args) > 0 {
			spec.Args[0] = "mutated-probe-arg"
		}
		if len(spec.Env) > 0 {
			spec.Env[0] = "MUTATED=probe-env"
		}
	}
	step := r.steps[index]
	return step.result, step.err
}

func cloneCommandSpec(spec process.CommandSpec) process.CommandSpec {
	cloned := spec
	cloned.Args = slices.Clone(spec.Args)
	cloned.Env = slices.Clone(spec.Env)
	cloned.Stdin = bytes.Clone(spec.Stdin)
	cloned.Files = slices.Clone(spec.Files)
	for index := range cloned.Files {
		cloned.Files[index].Data = bytes.Clone(cloned.Files[index].Data)
	}
	return cloned
}

func healthyProbeSteps() []scriptedProbeStep {
	return []scriptedProbeStep{
		{
			result: process.Result{
				Stdout:   []byte("2.1.208 (Claude Code)\n"),
				Stderr:   []byte("private-version-stderr"),
				ExitCode: 0,
			},
		},
		{
			result: process.Result{
				Stdout:   []byte(healthyProbeHelpOutput()),
				Stderr:   []byte("private-help-stderr"),
				ExitCode: 0,
			},
		},
		{
			result: process.Result{
				Stdout:   []byte{0xff},
				Stderr:   []byte("private-auth-stderr"),
				ExitCode: 0,
			},
		},
	}
}

func probeWithSteps(
	t *testing.T,
	steps []scriptedProbeStep,
) provider.Health {
	t.Helper()
	return New().Probe(
		context.Background(),
		testProviderConfig(t),
		&scriptedProbeRunner{t: t, steps: steps},
	)
}

func assertHealthyClaude(t *testing.T, health provider.Health) {
	t.Helper()
	assertProbeOutcome(
		t,
		health,
		provider.HealthReady,
		"authenticated",
		nil,
		true,
	)
	if health.Version != "2.1.208" {
		t.Fatalf("Version=%q, want 2.1.208", health.Version)
	}
}

func assertProbeOutcome(
	t *testing.T,
	health provider.Health,
	wantStatus provider.HealthStatus,
	wantAuth string,
	wantProblems []string,
	wantCapabilities bool,
) {
	t.Helper()
	if health.Provider != core.ProviderClaude {
		t.Fatalf("Provider=%q, want %q", health.Provider, core.ProviderClaude)
	}
	if health.Status != wantStatus {
		t.Fatalf("Status=%q, want %q; health=%+v", health.Status, wantStatus, health)
	}
	if health.Auth != wantAuth {
		t.Fatalf("Auth=%q, want %q; health=%+v", health.Auth, wantAuth, health)
	}
	if !reflect.DeepEqual(health.Problems, wantProblems) {
		t.Fatalf(
			"Problems=%q, want %q; health=%+v",
			health.Problems,
			wantProblems,
			health,
		)
	}
	wantCapabilityList := []string(nil)
	if wantCapabilities {
		wantCapabilityList = requiredCapabilities()
	}
	if !reflect.DeepEqual(health.Capabilities, wantCapabilityList) {
		t.Fatalf(
			"Capabilities=%q, want %q",
			health.Capabilities,
			wantCapabilityList,
		)
	}
}

func assertHealthDoesNotContain(
	t *testing.T,
	health provider.Health,
	forbidden ...string,
) {
	t.Helper()
	encoded, err := json.Marshal(health)
	if err != nil {
		t.Fatal(err)
	}
	forms := []string{fmt.Sprintf("%+v", health), string(encoded)}
	for _, value := range forbidden {
		if value == "" {
			continue
		}
		for _, form := range forms {
			if strings.Contains(form, value) {
				t.Fatalf("health exposed %q: %s", value, form)
			}
		}
	}
}

func requiredCapabilities() []string {
	return []string{
		"stdin_prompt",
		"json_envelope",
		"no_session_persistence",
		"empty_settings",
		"empty_tools",
		"safe_mode",
	}
}

func requiredProbeHelpTokens() []string {
	return []string{
		"--print",
		"--output-format",
		"--no-session-persistence",
		"--safe-mode",
		"--setting-sources",
		"--tools",
		"--strict-mcp-config",
		"--permission-mode",
		"--disable-slash-commands",
		"--no-chrome",
		"--model",
	}
}

func healthyProbeHelpOutput() string {
	return strings.Join(requiredProbeHelpTokens(), "\n") + "\n"
}

func helpOutputReplacing(target, replacement string) string {
	lines := requiredProbeHelpTokens()
	for index, line := range lines {
		if line != target {
			continue
		}
		if replacement == "" {
			lines = append(lines[:index], lines[index+1:]...)
		} else {
			lines[index] = replacement
		}
		return strings.Join(lines, "\n") + "\n"
	}
	panic("unknown help token")
}

func probeTestName(value string) string {
	replacer := strings.NewReplacer("-", "_", " ", "_", "/", "_")
	return strings.Trim(replacer.Replace(value), "_")
}

func assertProviderCategory(
	t *testing.T,
	err error,
	want provider.ErrorCategory,
) {
	t.Helper()
	var providerErr *provider.ProviderError
	if !errors.As(err, &providerErr) {
		t.Fatalf("error=%v (%T), want provider error", err, err)
	}
	if providerErr.Category() != want {
		t.Fatalf("Category()=%q, want %q", providerErr.Category(), want)
	}
}

func assertErrorOmits(t *testing.T, err error, values ...string) {
	t.Helper()
	if err == nil {
		t.Fatal("error is nil")
	}
	for _, value := range values {
		if value != "" && strings.Contains(err.Error(), value) {
			t.Fatalf("error exposed %q: %q", value, err)
		}
	}
}

func requestWithFramedPromptBytes(
	t *testing.T,
	target int,
) core.Request {
	t.Helper()
	request := core.Request{
		Input:  strings.Repeat("x", target-1024),
		Format: core.OutputFormat{Type: core.FormatText},
	}
	initial := provider.BuildPrompt(request, provider.SchemaInline)
	if len(initial) >= target {
		t.Fatalf("test fixture initial prompt=%d, target=%d", len(initial), target)
	}
	request.Input += strings.Repeat("y", target-len(initial))
	framed := provider.BuildPrompt(request, provider.SchemaInline)
	if len(framed) != target {
		t.Fatalf("test fixture framed prompt=%d, target=%d", len(framed), target)
	}
	return request
}

func expectedBuildArgs(model string) []string {
	return []string{
		"-p",
		"--output-format",
		"json",
		"--no-session-persistence",
		"--safe-mode",
		"--setting-sources",
		"",
		"--tools",
		"",
		"--strict-mcp-config",
		"--permission-mode",
		"dontAsk",
		"--disable-slash-commands",
		"--no-chrome",
		"--model",
		model,
	}
}

func claudeModel() core.Model {
	return core.Model{
		ID:            "public-claude",
		Provider:      core.ProviderClaude,
		ProviderModel: testModel,
	}
}

func testProviderConfig(t *testing.T) provider.ProviderConfig {
	t.Helper()
	lookup := map[string]string{
		"SystemRoot":             `C:\Windows`,
		"AI_CLI_GATEWAY_API_KEY": "gateway-bearer-secret-103",
		"OPENAI_API_KEY":         "other-provider-secret-104",
		"GEMINI_API_KEY":         "other-provider-secret-105",
		"HTTPS_PROXY":            "proxy-secret-106",
		"SSL_CERT_FILE":          "ca-secret-107",
		"USER_IDENTITY":          "identity@example.test",
	}
	return provider.ProviderConfig{
		Executable: testExecutable,
		PrefixArgs: []string{"--trusted-prefix", "fixed"},
		ConfigHome: testConfigHome,
		SafePath:   testSafePath,
		LookupEnv: func(name string) (string, bool) {
			value, ok := lookup[name]
			return value, ok
		},
	}
}

func assertExactEnvironment(
	t *testing.T,
	got []string,
	cfg provider.ProviderConfig,
	runtimeDir string,
	withCredential bool,
) {
	t.Helper()
	want := []string{
		"CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1",
		"CLAUDE_CODE_DISABLE_OFFICIAL_MARKETPLACE_AUTOINSTALL=1",
		"CLAUDE_CODE_DISABLE_TERMINAL_TITLE=1",
		"CLAUDE_CODE_SKIP_PROMPT_HISTORY=1",
		"CLAUDE_CODE_TMPDIR=" + runtimeDir,
		"CLAUDE_CONFIG_DIR=" + cfg.ConfigHome,
		"HOME=" + runtimeDir,
		"NO_COLOR=1",
		"PATH=" + cfg.SafePath,
		"TEMP=" + runtimeDir,
		"TMP=" + runtimeDir,
		"TMPDIR=" + runtimeDir,
	}
	if withCredential {
		want = append(want, "ANTHROPIC_API_KEY=anthropic-secret-301")
	}
	if runtime.GOOS == "windows" {
		want = append(want, `SystemRoot=C:\Windows`)
	}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Env mismatch\n got: %q\nwant: %q", got, want)
	}
}
