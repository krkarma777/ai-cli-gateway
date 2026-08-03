package codex

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
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

const (
	testExecutable = "/trusted/bin/codex"
	testConfigHome = "/trusted/codex-home"
	testSafePath   = "/trusted/bin:/usr/bin:/bin"
	testModel      = "gpt-5.4-codex"
)

func TestAdapterIdentityAndSupportedVersion(t *testing.T) {
	adapter := New()
	if adapter.Name() != core.ProviderCodex {
		t.Fatalf("Name()=%q", adapter.Name())
	}
	want := provider.Range{
		MinInclusive: provider.Version{Major: 0, Minor: 146, Patch: 0},
		MaxExclusive: provider.Version{Major: 0, Minor: 147, Patch: 0},
	}
	if got := adapter.SupportedVersion(); got != want {
		t.Fatalf("SupportedVersion()=%+v, want %+v", got, want)
	}
	var _ provider.Adapter = adapter
}

func TestBuildTextUsesExactFixedArgvAndIsolatedEnvironment(t *testing.T) {
	instructions := "한국어\n--model attacker-model\n\"quoted instruction\""
	request := core.Request{
		ModelAlias:   "public-alias",
		Instructions: &instructions,
		Input:        "-leading input\nFAKE_HEADER: gateway-key-781",
		Format:       core.OutputFormat{Type: core.FormatText},
	}
	model := codexModel()
	prefixBacking := []string{"--trusted-wrapper", "fixed", "prefix-canary"}
	cfg := testProviderConfig(t)
	cfg.PrefixArgs = prefixBacking[:2]
	runtimeDir := filepath.Join(
		string(filepath.Separator),
		"trusted",
		"runtime",
		"request-build-text",
	)
	requestRuntime := process.Runtime{
		ID:  "build-text",
		Dir: runtimeDir,
	}

	spec, err := New().Build(request, model, cfg, requestRuntime)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}

	wantArgs := append(
		[]string{"--trusted-wrapper", "fixed"},
		expectedTextArgs(testModel)...,
	)
	if !reflect.DeepEqual(spec.Args, wantArgs) {
		t.Fatalf("Args mismatch\n got: %q\nwant: %q", spec.Args, wantArgs)
	}
	if prefixBacking[2] != "prefix-canary" {
		t.Fatalf("prefix backing array was overwritten: %q", prefixBacking)
	}
	if spec.Executable != testExecutable {
		t.Fatalf("Executable=%q", spec.Executable)
	}
	if spec.Dir != runtimeDir {
		t.Fatalf("Dir=%q, want %q", spec.Dir, runtimeDir)
	}
	wantPrompt := provider.BuildPrompt(request, provider.SchemaFile)
	if !bytes.Equal(spec.Stdin, wantPrompt) {
		t.Fatalf("Stdin mismatch\n got: %q\nwant: %q", spec.Stdin, wantPrompt)
	}
	if len(spec.Files) != 0 {
		t.Fatalf("text Files=%+v, want none", spec.Files)
	}
	assertExactEnvironment(t, spec.Env, cfg, runtimeDir)

	joinedArgs := strings.Join(spec.Args, "\x00")
	joinedEnv := strings.Join(spec.Env, "\x00")
	for _, forbidden := range []string{
		instructions,
		request.Input,
		"gateway-key-781",
		"other-provider-credential-782",
		"provider-output-identity-783",
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

func TestBuildSchemaOwnsOneExactFileAndPlacesPathBeforeStdinSentinel(
	t *testing.T,
) {
	instructions := "private-instructions-391\n--output-schema attacker"
	description := "private-description-392"
	schema := []byte(
		`{"type":"object","private-schema-marker-393":{"type":"string"}}`,
	)
	request := core.Request{
		Instructions: &instructions,
		Input:        "private-input-394\n--model attacker-model",
		Format: core.OutputFormat{
			Type:        core.FormatJSONSchema,
			Name:        "private-name-395",
			Description: &description,
			Schema:      schema,
		},
	}
	cfg := testProviderConfig(t)
	requestRuntime := process.Runtime{
		ID:  "schema-build",
		Dir: filepath.Join(t.TempDir(), "request-schema-build"),
	}

	spec, err := New().Build(request, codexModel(), cfg, requestRuntime)
	if err != nil {
		t.Fatalf("Build() error: %v", err)
	}
	schemaPath := filepath.Join(requestRuntime.Dir, "output-schema.json")
	if !filepath.IsAbs(schemaPath) {
		t.Fatalf("schema path is not absolute: %q", schemaPath)
	}
	wantArgs := append(
		append([]string(nil), cfg.PrefixArgs...),
		expectedSchemaArgs(testModel, schemaPath)...,
	)
	if !reflect.DeepEqual(spec.Args, wantArgs) {
		t.Fatalf("Args mismatch\n got: %q\nwant: %q", spec.Args, wantArgs)
	}
	if got := spec.Args[len(spec.Args)-3:]; !reflect.DeepEqual(
		got,
		[]string{"--output-schema", schemaPath, "-"},
	) {
		t.Fatalf("schema tail=%q", got)
	}
	wantFiles := []process.FileSpec{{
		Name: "output-schema.json",
		Data: schema,
		Mode: 0o600,
	}}
	if !reflect.DeepEqual(spec.Files, wantFiles) {
		t.Fatalf("Files=%+v, want %+v", spec.Files, wantFiles)
	}
	wantPrompt := provider.BuildPrompt(request, provider.SchemaFile)
	if !bytes.Equal(spec.Stdin, wantPrompt) {
		t.Fatalf("Stdin mismatch\n got: %q\nwant: %q", spec.Stdin, wantPrompt)
	}
	for _, forbidden := range [][]byte{
		schema,
		[]byte(request.Format.Name),
		[]byte(description),
	} {
		if bytes.Contains(spec.Stdin, forbidden) {
			t.Fatalf("schema metadata leaked into stdin: %q", forbidden)
		}
	}
	joinedArgs := strings.Join(spec.Args, "\x00")
	for _, forbidden := range []string{
		instructions,
		request.Input,
		string(schema),
		request.Format.Name,
		description,
	} {
		if strings.Contains(joinedArgs, forbidden) ||
			strings.Contains(spec.Executable, forbidden) ||
			strings.Contains(spec.Dir, forbidden) ||
			strings.Contains(spec.Files[0].Name, forbidden) {
			t.Fatalf("request value exposed outside stdin/file data: %q", forbidden)
		}
	}

	schema[0] = 'X'
	if spec.Files[0].Data[0] != '{' {
		t.Fatalf("FileSpec data aliases request schema: %q", spec.Files[0].Data)
	}
}

func TestBuildReturnsFreshOwnedDataAcrossRepeatedAndConcurrentCalls(
	t *testing.T,
) {
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
	credentialBacking := []string{"credential-canary"}
	cfg := testProviderConfig(t)
	cfg.PrefixArgs = prefixBacking[:2]
	cfg.CredentialEnv = credentialBacking[:0]
	requestRuntime := process.Runtime{
		ID:  "owned-build",
		Dir: filepath.Join(t.TempDir(), "request-owned-build"),
	}

	const builds = 32
	specs := make([]process.CommandSpec, builds)
	errs := make([]error, builds)
	var wait sync.WaitGroup
	for index := 0; index < builds; index++ {
		index := index
		wait.Add(1)
		go func() {
			defer wait.Done()
			specs[index], errs[index] = New().Build(
				request,
				codexModel(),
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
		credentialBacking[0] != "credential-canary" {
		t.Fatalf(
			"configuration backing arrays changed: prefix=%q credentials=%q",
			prefixBacking,
			credentialBacking,
		)
	}

	secondArgs := slices.Clone(specs[1].Args)
	secondEnv := slices.Clone(specs[1].Env)
	secondStdin := bytes.Clone(specs[1].Stdin)
	secondFileData := bytes.Clone(specs[1].Files[0].Data)
	specs[0].Args[0] = "mutated-args"
	specs[0].Env[0] = "MUTATED=environment"
	specs[0].Stdin[0] = 'X'
	specs[0].Files[0].Name = "mutated-name"
	specs[0].Files[0].Data[0] = 'X'
	specs[0].Files = append(specs[0].Files, process.FileSpec{Name: "extra"})

	if !reflect.DeepEqual(specs[1].Args, secondArgs) ||
		!reflect.DeepEqual(specs[1].Env, secondEnv) ||
		!bytes.Equal(specs[1].Stdin, secondStdin) ||
		!bytes.Equal(specs[1].Files[0].Data, secondFileData) {
		t.Fatal("separate Build results alias mutable command data")
	}
	if cfg.PrefixArgs[0] != "--trusted-prefix" ||
		len(cfg.CredentialEnv) != 0 ||
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
				model := codexModel()
				model.Provider = core.ProviderClaude
				return New().Build(baseRequest, model, baseConfig, requestRuntime)
			},
			second: func() (process.CommandSpec, error) {
				model := codexModel()
				model.Provider = core.ProviderGemini
				return New().Build(baseRequest, model, baseConfig, requestRuntime)
			},
		},
		{
			name: "invalid configured model",
			first: func() (process.CommandSpec, error) {
				model := codexModel()
				model.ProviderModel = "--model planted-model-secret-501"
				return New().Build(baseRequest, model, baseConfig, requestRuntime)
			},
			second: func() (process.CommandSpec, error) {
				model := codexModel()
				model.ProviderModel = "planted-model-secret-502\nnext"
				return New().Build(baseRequest, model, baseConfig, requestRuntime)
			},
		},
		{
			name: "credential environment unsupported",
			first: func() (process.CommandSpec, error) {
				cfg := baseConfig
				cfg.CredentialEnv = []string{"PLANTED_CODEX_TOKEN_503"}
				return New().Build(baseRequest, codexModel(), cfg, requestRuntime)
			},
			second: func() (process.CommandSpec, error) {
				cfg := baseConfig
				cfg.CredentialEnv = []string{"PLANTED_OTHER_TOKEN_504"}
				return New().Build(baseRequest, codexModel(), cfg, requestRuntime)
			},
		},
		{
			name: "nil prompt",
			first: func() (process.CommandSpec, error) {
				req := baseRequest
				req.Input = "planted-input-secret-505"
				req.Format.Type = core.FormatType("planted-format-secret-506")
				return New().Build(req, codexModel(), baseConfig, requestRuntime)
			},
			second: func() (process.CommandSpec, error) {
				req := baseRequest
				req.Input = "planted-input-secret-507"
				req.Format.Type = core.FormatType("planted-format-secret-508")
				return New().Build(req, codexModel(), baseConfig, requestRuntime)
			},
		},
		{
			name: "invalid environment",
			first: func() (process.CommandSpec, error) {
				cfg := baseConfig
				cfg.SafePath = "planted-safe-path-509\x00tail"
				return New().Build(baseRequest, codexModel(), cfg, requestRuntime)
			},
			second: func() (process.CommandSpec, error) {
				cfg := baseConfig
				cfg.SafePath = "planted-safe-path-510\x00tail"
				return New().Build(baseRequest, codexModel(), cfg, requestRuntime)
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
				"CODEX_TOKEN",
				"OTHER_TOKEN",
				"Claude",
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

func TestParseUsesClosedPrecedenceAndPreservesSuccessfulStdout(t *testing.T) {
	request := core.Request{
		Input:  "planted-prompt-601",
		Format: core.OutputFormat{Type: core.FormatText},
	}
	tests := []struct {
		name         string
		result       process.Result
		want         string
		wantCategory provider.ErrorCategory
	}{
		{
			name: "valid output exact",
			result: process.Result{
				Stdout:   []byte("  exact output\nwith newline\n"),
				Stderr:   []byte("planted-stderr-602"),
				ExitCode: 0,
			},
			want: "  exact output\nwith newline\n",
		},
		{
			name: "whitespace is nonempty output",
			result: process.Result{
				Stdout:   []byte("\n"),
				ExitCode: 0,
			},
			want: "\n",
		},
		{
			name: "nonzero precedes empty",
			result: process.Result{
				Stderr:   []byte("planted-stderr-603"),
				ExitCode: 7,
			},
			wantCategory: provider.ProviderErrorFailed,
		},
		{
			name: "nonzero precedes invalid UTF-8",
			result: process.Result{
				Stdout:   []byte{0xff},
				Stderr:   []byte("planted-stderr-604"),
				ExitCode: 9,
			},
			wantCategory: provider.ProviderErrorFailed,
		},
		{
			name: "empty success",
			result: process.Result{
				Stderr:   []byte("planted-stderr-605"),
				ExitCode: 0,
			},
			wantCategory: provider.ProviderErrorProtocol,
		},
		{
			name: "invalid UTF-8 success",
			result: process.Result{
				Stdout:   []byte{'o', 'k', 0xff},
				Stderr:   []byte("planted-stderr-606"),
				ExitCode: 0,
			},
			wantCategory: provider.ProviderErrorProtocol,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := New().Parse(request, test.result)
			if test.wantCategory == "" {
				if err != nil || got != test.want {
					t.Fatalf("Parse()=(%q,%v), want (%q,nil)", got, err, test.want)
				}
				return
			}
			if got != "" || err == nil {
				t.Fatalf("Parse()=(%q,%v), want categorized error", got, err)
			}
			var providerErr *provider.ProviderError
			if !errors.As(err, &providerErr) {
				t.Fatalf("error type=%T, want *provider.ProviderError", err)
			}
			if providerErr.Category() != test.wantCategory {
				t.Fatalf(
					"Category()=%q, want %q",
					providerErr.Category(),
					test.wantCategory,
				)
			}
			for _, forbidden := range []string{
				request.Input,
				string(test.result.Stdout),
				string(test.result.Stderr),
				"planted",
				"identity@example.test",
				"/private/runtime/path",
			} {
				if forbidden != "" && strings.Contains(err.Error(), forbidden) {
					t.Fatalf("error exposed %q: %q", forbidden, err)
				}
			}
		})
	}
}

func expectedTextArgs(model string) []string {
	return append(expectedBuildBaseArgs(model), "-")
}

func expectedSchemaArgs(model, schemaPath string) []string {
	return append(
		expectedBuildBaseArgs(model),
		"--output-schema",
		schemaPath,
		"-",
	)
}

func expectedBuildBaseArgs(model string) []string {
	return []string{
		"--ask-for-approval",
		"never",
		"exec",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--strict-config",
		"--sandbox",
		"read-only",
		"--skip-git-repo-check",
		"--color",
		"never",
		"--disable",
		"shell_tool",
		"--disable",
		"unified_exec",
		"--disable",
		"code_mode_host",
		"--disable",
		"apps",
		"--disable",
		"plugins",
		"--disable",
		"remote_plugin",
		"--disable",
		"hooks",
		"--disable",
		"multi_agent",
		"--disable",
		"browser_use",
		"--disable",
		"browser_use_external",
		"--disable",
		"computer_use",
		"--disable",
		"in_app_browser",
		"--disable",
		"image_generation",
		"--disable",
		"skill_search",
		"--disable",
		"skill_mcp_dependency_install",
		"--disable",
		"workspace_dependencies",
		"-c",
		`web_search="disabled"`,
		"--model",
		model,
	}
}

func codexModel() core.Model {
	return core.Model{
		ID:            "public-codex",
		Provider:      core.ProviderCodex,
		ProviderModel: testModel,
	}
}

func testProviderConfig(t *testing.T) provider.ProviderConfig {
	t.Helper()
	return provider.ProviderConfig{
		Executable: testExecutable,
		PrefixArgs: []string{"--trusted-prefix", "fixed"},
		ConfigHome: testConfigHome,
		SafePath:   testSafePath,
		LookupEnv: func(name string) (string, bool) {
			if name == "SystemRoot" {
				return `C:\Windows`, true
			}
			t.Fatalf("unexpected environment lookup %q", name)
			return "", false
		},
	}
}

func assertExactEnvironment(
	t *testing.T,
	got []string,
	cfg provider.ProviderConfig,
	runtimeDir string,
) {
	t.Helper()
	want := []string{
		"CODEX_HOME=" + cfg.ConfigHome,
		"HOME=" + runtimeDir,
		"NO_COLOR=1",
		"PATH=" + cfg.SafePath,
		"TEMP=" + runtimeDir,
		"TMP=" + runtimeDir,
		"TMPDIR=" + runtimeDir,
	}
	if runtime.GOOS == "windows" {
		want = append(want, `SystemRoot=C:\Windows`)
		slices.Sort(want)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Env mismatch\n got: %q\nwant: %q", got, want)
	}
	allowed := map[string]struct{}{
		"CODEX_HOME": {},
		"HOME":       {},
		"NO_COLOR":   {},
		"PATH":       {},
		"TEMP":       {},
		"TMP":        {},
		"TMPDIR":     {},
	}
	if runtime.GOOS == "windows" {
		allowed["SystemRoot"] = struct{}{}
	}
	for _, entry := range got {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			t.Fatalf("environment entry has no value: %q", entry)
		}
		if _, ok := allowed[name]; !ok {
			t.Fatalf("unexpected environment name %q", name)
		}
	}
}

func TestProbeBuildsExactlyFiveIsolatedCommandsInOrder(t *testing.T) {
	cfg := testProviderConfig(t)
	prefixBacking := []string{"--trusted-probe-prefix", "fixed", "probe-canary"}
	cfg.PrefixArgs = prefixBacking[:2]
	runner := &scriptedProbeRunner{
		t:           t,
		steps:       healthyProbeSteps(),
		mutateBuilt: true,
	}

	health := New().Probe(context.Background(), cfg, runner)
	assertHealthyCodex(t, health)

	wantCommands := [][]string{
		{"--version"},
		{"--ask-for-approval", "never", "exec", "--help"},
		{"features", "list"},
		{"login", "status"},
		{"doctor", "--json"},
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
		assertExactEnvironment(t, spec.Env, cfg, runner.runtimes[index].Dir)
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
			err:         errors.New("planted private version runner error"),
			wantProblem: provider.ProblemVersionUnreadable,
		},
		{
			name: "nonzero",
			result: process.Result{
				Stdout:   []byte("codex-cli 0.146.0\n"),
				ExitCode: 7,
			},
			wantProblem: provider.ProblemVersionUnreadable,
		},
		{
			name:        "malformed",
			result:      process.Result{Stdout: []byte("codex-cli private")},
			wantProblem: provider.ProblemVersionUnreadable,
		},
		{
			name: "ambiguous",
			result: process.Result{
				Stdout: []byte("codex-cli 0.146.0 and 0.146.1\n"),
			},
			wantProblem: provider.ProblemVersionUnreadable,
		},
		{
			name: "below minimum",
			result: process.Result{
				Stdout: []byte("codex-cli 0.145.999\n"),
			},
			wantVersion: "0.145.999",
			wantProblem: provider.ProblemVersionUnsupported,
		},
		{
			name: "exclusive maximum",
			result: process.Result{
				Stdout: []byte("codex-cli 0.147.0\n"),
			},
			wantVersion: "0.147.0",
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

			if len(runner.specs) != 1 {
				t.Fatalf("probe count=%d, want version only", len(runner.specs))
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
			assertHealthDoesNotContain(t, health, "planted", "private")
		})
	}
}

func TestProbeGlobalApprovalPlacementAndExactHelpTokens(t *testing.T) {
	required := requiredHelpTokens()
	for _, token := range required {
		token := token
		t.Run("missing_"+probeTestName(token), func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[1].result.Stdout = []byte(
				helpOutputReplacing(token, ""),
			)
			health := probeWithSteps(t, steps)
			assertProbeOutcome(
				t,
				health,
				provider.HealthNotReady,
				"authenticated",
				[]string{provider.ProblemCapabilityMissing},
				false,
			)
		})
	}

	t.Run("output schema prefix collision", func(t *testing.T) {
		steps := healthyProbeSteps()
		steps[1].result.Stdout = []byte(
			helpOutputReplacing(
				"--output-schema",
				"--output-schema-evil",
			),
		)
		health := probeWithSteps(t, steps)
		assertProbeOutcome(
			t,
			health,
			provider.HealthNotReady,
			"authenticated",
			[]string{provider.ProblemCapabilityMissing},
			false,
		)
	})
}

func TestProbeRequiresExactCompleteFeatureRows(t *testing.T) {
	first := requiredFeatureNames()[0]
	tests := []struct {
		name   string
		output []byte
	}{
		{
			name: "missing",
			output: []byte(strings.Replace(
				healthyFeatureOutput(),
				first+" stable false\n",
				"",
				1,
			)),
		},
		{
			name: "name prefix collision",
			output: []byte(strings.Replace(
				healthyFeatureOutput(),
				first+" stable false",
				first+"_evil stable false",
				1,
			)),
		},
		{
			name: "duplicate",
			output: []byte(
				healthyFeatureOutput() + first + " stable false\n",
			),
		},
		{
			name: "removed",
			output: []byte(strings.Replace(
				healthyFeatureOutput(),
				first+" stable false",
				first+" removed false",
				1,
			)),
		},
		{
			name: "malformed short row",
			output: []byte(strings.Replace(
				healthyFeatureOutput(),
				first+" stable false",
				first+" stable",
				1,
			)),
		},
		{
			name: "malformed enabled state",
			output: []byte(strings.Replace(
				healthyFeatureOutput(),
				first+" stable false",
				first+" stable maybe",
				1,
			)),
		},
		{
			name:   "malformed unrelated row",
			output: []byte(healthyFeatureOutput() + "malformed\n"),
		},
		{
			name: "syntactically invalid unrelated name",
			output: []byte(
				healthyFeatureOutput() +
					"1invalid_feature stable false\n",
			),
		},
		{
			name:   "invalid UTF-8",
			output: []byte{0xff},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[2].result.Stdout = test.output
			health := probeWithSteps(t, steps)
			assertProbeOutcome(
				t,
				health,
				provider.HealthNotReady,
				"authenticated",
				[]string{provider.ProblemCapabilityMissing},
				false,
			)
		})
	}
}

func TestProbeAcceptsPinnedFeatureListGrammar(t *testing.T) {
	steps := healthyProbeSteps()
	steps[2].result.Stdout = []byte(pinnedFeatureOutput())

	assertHealthyCodex(t, probeWithSteps(t, steps))
}

func TestProbeRejectsUnknownFeatureMaturity(t *testing.T) {
	tests := []struct {
		name   string
		output string
	}{
		{
			name: "required row",
			output: strings.Replace(
				healthyFeatureOutput(),
				"shell_tool stable false",
				"shell_tool attacker false",
				1,
			),
		},
		{
			name: "unrelated row",
			output: healthyFeatureOutput() +
				"unrelated_feature attacker false\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[2].result.Stdout = []byte(test.output)

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

func TestProbeClassifiesExecutionExitAndEncodingFailuresDeterministically(
	t *testing.T,
) {
	tests := []struct {
		name             string
		mutate           func([]scriptedProbeStep)
		wantStatus       provider.HealthStatus
		wantAuth         string
		wantProblems     []string
		wantCapabilities bool
	}{
		{
			name: "version execution error",
			mutate: func(steps []scriptedProbeStep) {
				steps[0].err = errors.New("planted version execution error")
			},
			wantStatus: provider.HealthNotReady,
			wantAuth:   "unknown",
			wantProblems: []string{
				provider.ProblemVersionUnreadable,
				provider.ProblemCapabilityMissing,
				provider.ProblemAuthUnknown,
			},
			wantCapabilities: false,
		},
		{
			name: "version nonzero",
			mutate: func(steps []scriptedProbeStep) {
				steps[0].result.ExitCode = 7
			},
			wantStatus: provider.HealthNotReady,
			wantAuth:   "unknown",
			wantProblems: []string{
				provider.ProblemVersionUnreadable,
				provider.ProblemCapabilityMissing,
				provider.ProblemAuthUnknown,
			},
			wantCapabilities: false,
		},
		{
			name: "version invalid UTF-8",
			mutate: func(steps []scriptedProbeStep) {
				steps[0].result.Stdout = []byte{0xff}
			},
			wantStatus: provider.HealthNotReady,
			wantAuth:   "unknown",
			wantProblems: []string{
				provider.ProblemVersionUnreadable,
				provider.ProblemCapabilityMissing,
				provider.ProblemAuthUnknown,
			},
			wantCapabilities: false,
		},
		{
			name: "help execution error",
			mutate: func(steps []scriptedProbeStep) {
				steps[1].err = errors.New("planted help execution error")
			},
			wantStatus:       provider.HealthNotReady,
			wantAuth:         "authenticated",
			wantProblems:     []string{provider.ProblemCapabilityMissing},
			wantCapabilities: false,
		},
		{
			name: "help nonzero",
			mutate: func(steps []scriptedProbeStep) {
				steps[1].result.ExitCode = 2
			},
			wantStatus:       provider.HealthNotReady,
			wantAuth:         "authenticated",
			wantProblems:     []string{provider.ProblemCapabilityMissing},
			wantCapabilities: false,
		},
		{
			name: "help invalid UTF-8",
			mutate: func(steps []scriptedProbeStep) {
				steps[1].result.Stdout = []byte{0xff}
			},
			wantStatus:       provider.HealthNotReady,
			wantAuth:         "authenticated",
			wantProblems:     []string{provider.ProblemCapabilityMissing},
			wantCapabilities: false,
		},
		{
			name: "features execution error",
			mutate: func(steps []scriptedProbeStep) {
				steps[2].err = errors.New("planted features execution error")
			},
			wantStatus:       provider.HealthNotReady,
			wantAuth:         "authenticated",
			wantProblems:     []string{provider.ProblemCapabilityMissing},
			wantCapabilities: false,
		},
		{
			name: "login execution error",
			mutate: func(steps []scriptedProbeStep) {
				steps[3].err = errors.New(
					"planted auth identity@example.test",
				)
			},
			wantStatus:       provider.HealthUnknown,
			wantAuth:         "unknown",
			wantProblems:     []string{provider.ProblemAuthUnknown},
			wantCapabilities: true,
		},
		{
			name: "login nonzero despite authenticated prose",
			mutate: func(steps []scriptedProbeStep) {
				steps[3].result.ExitCode = 1
				steps[3].result.Stdout = []byte(
					"authenticated identity@example.test",
				)
			},
			wantStatus:       provider.HealthNotReady,
			wantAuth:         "missing",
			wantProblems:     []string{provider.ProblemAuthMissing},
			wantCapabilities: true,
		},
		{
			name: "doctor execution error",
			mutate: func(steps []scriptedProbeStep) {
				steps[4].err = errors.New("planted doctor path /private/secret")
			},
			wantStatus:       provider.HealthNotReady,
			wantAuth:         "authenticated",
			wantProblems:     []string{provider.ProblemCapabilityMissing},
			wantCapabilities: true,
		},
		{
			name: "doctor nonzero",
			mutate: func(steps []scriptedProbeStep) {
				steps[4].result.ExitCode = 1
			},
			wantStatus:       provider.HealthNotReady,
			wantAuth:         "authenticated",
			wantProblems:     []string{provider.ProblemCapabilityMissing},
			wantCapabilities: true,
		},
		{
			name: "doctor invalid UTF-8",
			mutate: func(steps []scriptedProbeStep) {
				steps[4].result.Stdout = []byte{0xff}
			},
			wantStatus:       provider.HealthNotReady,
			wantAuth:         "authenticated",
			wantProblems:     []string{provider.ProblemCapabilityMissing},
			wantCapabilities: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps := healthyProbeSteps()
			test.mutate(steps)
			health := probeWithSteps(t, steps)
			assertProbeOutcome(
				t,
				health,
				test.wantStatus,
				test.wantAuth,
				test.wantProblems,
				test.wantCapabilities,
			)
			assertHealthDoesNotContain(
				t,
				health,
				"planted",
				"identity@example.test",
				"/private/secret",
				string([]byte{0xff}),
			)
		})
	}
}

func TestProbeRetainsCanonicalVersionAndRejectsUnsupportedRange(
	t *testing.T,
) {
	for _, versionOutput := range []string{
		"codex-cli 0.145.999\n",
		"codex-cli 0.147.0\n",
	} {
		t.Run(strings.TrimSpace(versionOutput), func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[0].result.Stdout = []byte(versionOutput)
			health := probeWithSteps(t, steps)
			wantVersion, err := provider.ParseVersion(versionOutput)
			if err != nil {
				t.Fatal(err)
			}
			if health.Version != wantVersion.String() {
				t.Fatalf(
					"Version=%q, want %q",
					health.Version,
					wantVersion.String(),
				)
			}
			assertProbeOutcome(
				t,
				health,
				provider.HealthNotReady,
				"unknown",
				[]string{
					provider.ProblemVersionUnsupported,
					provider.ProblemCapabilityMissing,
					provider.ProblemAuthUnknown,
				},
				false,
			)
		})
	}
}

func TestProbeDoctorUsesBoundedDuplicateSafeFixedCheckAllowlist(
	t *testing.T,
) {
	validAggregates := []string{"ok", "warn", "fail"}
	for _, aggregate := range validAggregates {
		t.Run("valid aggregate "+aggregate, func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[4].result.Stdout = []byte(doctorOutput(
				"1",
				fmt.Sprintf("%q", aggregate),
				fixedDoctorChecks("ok", "ok", "ok"),
				`,"unknownRoot":{"secret":"planted-root-secret"}`,
			))
			health := probeWithSteps(t, steps)
			assertHealthyCodex(t, health)
			assertHealthDoesNotContain(t, health, aggregate, "planted-root-secret")
		})
	}

	deepUnknown := strings.Repeat("[", 12) + "0" +
		strings.Repeat("]", 12)
	validChecks := fixedDoctorChecks("ok", "ok", "ok")
	tests := []struct {
		name   string
		output string
	}{
		{
			name: "duplicate root key",
			output: `{"schemaVersion":1,"schemaVersion":1,` +
				`"overallStatus":"ok","checks":` + validChecks + `}`,
		},
		{
			name: "duplicate retained check field",
			output: doctorOutput(
				"1",
				`"ok"`,
				strings.Replace(
					validChecks,
					`"status":"ok"`,
					`"status":"ok","status":"ok"`,
					1,
				),
				"",
			),
		},
		{
			name: "trailing JSON",
			output: doctorOutput(
				"1",
				`"ok"`,
				validChecks,
				"",
			) + `{}`,
		},
		{
			name: "bounded depth",
			output: doctorOutput(
				"1",
				`"ok"`,
				validChecks,
				`,"unknownDepth":`+deepUnknown,
			),
		},
		{
			name: "bounded number",
			output: doctorOutput(
				"1",
				`"ok"`,
				validChecks,
				`,"unknownNumber":123456789012345678901`,
			),
		},
		{
			name: "wrong schema version",
			output: doctorOutput(
				"2",
				`"ok"`,
				validChecks,
				"",
			),
		},
		{
			name: "noncanonical numeric schema version",
			output: doctorOutput(
				"1.0",
				`"ok"`,
				validChecks,
				"",
			),
		},
		{
			name: "string schema version",
			output: doctorOutput(
				`"1"`,
				`"ok"`,
				validChecks,
				"",
			),
		},
		{
			name:   "missing schema version",
			output: `{"overallStatus":"ok","checks":` + validChecks + `}`,
		},
		{
			name: "unknown aggregate",
			output: doctorOutput(
				"1",
				`"healthy"`,
				validChecks,
				"",
			),
		},
		{
			name: "wrong aggregate type",
			output: doctorOutput(
				"1",
				"true",
				validChecks,
				"",
			),
		},
		{
			name:   "missing aggregate",
			output: `{"schemaVersion":1,"checks":` + validChecks + `}`,
		},
		{
			name:   "checks wrong type",
			output: doctorOutput("1", `"ok"`, `[]`, ""),
		},
		{
			name: "missing fixed check",
			output: doctorOutput(
				"1",
				`"ok"`,
				`{"auth.credentials":{"id":"auth.credentials","status":"ok"},`+
					`"config.load":{"id":"config.load","status":"ok"}}`,
				"",
			),
		},
		{
			name: "fixed check wrong type",
			output: doctorOutput(
				"1",
				`"ok"`,
				strings.Replace(
					validChecks,
					`{"id":"installation","status":"ok",`+
						`"path":"/private/planted-installation"}`,
					`"not-an-object"`,
					1,
				),
				"",
			),
		},
		{
			name: "fixed check id mismatch",
			output: doctorOutput(
				"1",
				`"ok"`,
				strings.Replace(
					validChecks,
					`"id":"config.load"`,
					`"id":"planted-identity"`,
					1,
				),
				"",
			),
		},
		{
			name: "fixed check status unknown",
			output: doctorOutput(
				"1",
				`"ok"`,
				strings.Replace(
					validChecks,
					`"id":"installation","status":"ok"`,
					`"id":"installation","status":"healthy"`,
					1,
				),
				"",
			),
		},
		{
			name: "fixed check not ok",
			output: doctorOutput(
				"1",
				`"ok"`,
				fixedDoctorChecks("fail", "ok", "ok"),
				"",
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[4].result.Stdout = []byte(test.output)
			health := probeWithSteps(t, steps)
			assertProbeOutcome(
				t,
				health,
				provider.HealthNotReady,
				"authenticated",
				[]string{provider.ProblemCapabilityMissing},
				true,
			)
			assertHealthDoesNotContain(
				t,
				health,
				"planted",
				"identity",
				test.output,
			)
		})
	}
}

func TestProbeLoginExitIsAuthoritativeAcrossContradictoryDoctorState(
	t *testing.T,
) {
	t.Run("login succeeds while doctor auth check fails", func(t *testing.T) {
		steps := healthyProbeSteps()
		steps[3].result.Stdout = []byte(
			"not authenticated identity@example.test",
		)
		steps[4].result.Stdout = []byte(doctorOutput(
			"1",
			`"ok"`,
			fixedDoctorChecks("fail", "ok", "ok"),
			"",
		))
		health := probeWithSteps(t, steps)
		assertProbeOutcome(
			t,
			health,
			provider.HealthNotReady,
			"authenticated",
			[]string{provider.ProblemCapabilityMissing},
			true,
		)
		assertHealthDoesNotContain(t, health, "identity@example.test")
	})

	t.Run("login fails while doctor auth check succeeds", func(t *testing.T) {
		steps := healthyProbeSteps()
		steps[3].result.ExitCode = 1
		steps[3].result.Stdout = []byte(
			"authenticated identity@example.test",
		)
		health := probeWithSteps(t, steps)
		assertProbeOutcome(
			t,
			health,
			provider.HealthNotReady,
			"missing",
			[]string{provider.ProblemAuthMissing},
			true,
		)
		assertHealthDoesNotContain(t, health, "identity@example.test")
	})

	t.Run("login success ignores invalid output bytes", func(t *testing.T) {
		steps := healthyProbeSteps()
		steps[3].result.Stdout = []byte{0xff}
		if utf8.Valid(steps[3].result.Stdout) {
			t.Fatal("fixture unexpectedly valid UTF-8")
		}
		health := probeWithSteps(t, steps)
		assertHealthyCodex(t, health)
	})
}

func TestProbeVersionFailureReturnsFixedProblemsAndStopsFurtherProbes(
	t *testing.T,
) {
	steps := healthyProbeSteps()
	steps[0].err = errors.New("planted version error")
	steps[1].err = errors.New("planted help error")
	steps[2].result.ExitCode = 2
	steps[3].result.ExitCode = 1
	steps[4].err = errors.New("planted doctor error")
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
	if len(runner.specs) != 1 {
		t.Fatalf("probe count=%d, want version only", len(runner.specs))
	}
	assertHealthDoesNotContain(t, health, "planted")
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

func TestProbeResultsAreDeterministicFreshAndRedacted(t *testing.T) {
	firstSteps := healthyProbeSteps()
	firstSteps[0].result.Stdout = []byte(
		"codex 0.146.999 planted-version-secret-901",
	)
	firstSteps[1].result.Stderr = []byte("planted-help-stderr-902")
	firstSteps[2].result.Stderr = []byte("planted-feature-stderr-903")
	firstSteps[3].result.Stdout = []byte(
		"identity@example.test planted-login-output-904",
	)
	firstSteps[3].result.Stderr = []byte("planted-login-stderr-905")
	firstSteps[3].err = errors.New("planted-login-error-906")
	firstSteps[4].result.Stdout = []byte(doctorOutput(
		"1",
		`"warn"`,
		fixedDoctorChecks("ok", "ok", "ok"),
		`,"details":"planted-doctor-detail-906",`+
			`"path":"/private/planted-path-907"`,
	))
	firstSteps[4].result.Stderr = []byte("planted-doctor-stderr-908")
	first := probeWithSteps(t, firstSteps)

	secondSteps := healthyProbeSteps()
	secondSteps[0].result.Stdout = bytes.Clone(firstSteps[0].result.Stdout)
	secondSteps[1].result.Stderr = bytes.Clone(firstSteps[1].result.Stderr)
	secondSteps[2].result.Stderr = bytes.Clone(firstSteps[2].result.Stderr)
	secondSteps[3].result.Stdout = bytes.Clone(firstSteps[3].result.Stdout)
	secondSteps[3].result.Stderr = bytes.Clone(firstSteps[3].result.Stderr)
	secondSteps[3].err = errors.New("planted-login-error-906")
	secondSteps[4].result.Stdout = bytes.Clone(firstSteps[4].result.Stdout)
	secondSteps[4].result.Stderr = bytes.Clone(firstSteps[4].result.Stderr)
	second := probeWithSteps(t, secondSteps)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("nondeterministic health\nfirst=%+v\nsecond=%+v", first, second)
	}
	assertHealthDoesNotContain(
		t,
		first,
		"planted",
		"identity@example.test",
		"/private/planted-path-907",
	)

	if len(first.Capabilities) == 0 || len(first.Problems) == 0 {
		t.Fatalf("mutation fixture lacks slices: %+v", first)
	}
	first.Capabilities[0] = "mutated-capability"
	first.Problems[0] = "mutated-problem"
	if second.Capabilities[0] != "stdin_prompt" ||
		second.Problems[0] != provider.ProblemAuthUnknown {
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
	index := len(r.specs)
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
				Stdout:   []byte("codex-cli 0.146.0\n"),
				Stderr:   []byte("planted-version-stderr"),
				ExitCode: 0,
			},
		},
		{
			result: process.Result{
				Stdout:   []byte(healthyHelpOutput()),
				Stderr:   []byte("planted-help-stderr"),
				ExitCode: 0,
			},
		},
		{
			result: process.Result{
				Stdout:   []byte(healthyFeatureOutput()),
				Stderr:   []byte("planted-feature-stderr"),
				ExitCode: 0,
			},
		},
		{
			result: process.Result{
				Stdout: []byte(
					"not authenticated identity@example.test " +
						"planted-login-output",
				),
				Stderr:   []byte("planted-login-stderr"),
				ExitCode: 0,
			},
		},
		{
			result: process.Result{
				Stdout: []byte(doctorOutput(
					"1",
					`"warn"`,
					fixedDoctorChecks("ok", "ok", "ok"),
					`,"codexVersion":"planted-version",`+
						`"generatedAt":"planted-time",`+
						`"identity":"identity@example.test",`+
						`"unrelated":{"status":"fail"}`,
				)),
				Stderr:   []byte("planted-doctor-stderr"),
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

func assertHealthyCodex(t *testing.T, health provider.Health) {
	t.Helper()
	assertProbeOutcome(
		t,
		health,
		provider.HealthReady,
		"authenticated",
		nil,
		true,
	)
	if health.Version != "0.146.0" {
		t.Fatalf("Version=%q, want 0.146.0", health.Version)
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
	if health.Provider != core.ProviderCodex {
		t.Fatalf("Provider=%q", health.Provider)
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
		"ephemeral",
		"read_only",
		"never_approve",
		"schema_file",
		"feature_hardening",
	}
}

func requiredHelpTokens() []string {
	return []string{
		"PROMPT",
		"-",
		"--disable",
		"-c",
		"--strict-config",
		"--sandbox",
		"--model",
		"--output-schema",
		"--color",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--skip-git-repo-check",
	}
}

func healthyHelpOutput() string {
	return strings.Join(requiredHelpTokens(), "\n") + "\n"
}

func helpOutputReplacing(target, replacement string) string {
	lines := requiredHelpTokens()
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

func requiredFeatureNames() []string {
	return []string{
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
}

func healthyFeatureOutput() string {
	var output strings.Builder
	for _, name := range requiredFeatureNames() {
		_, _ = fmt.Fprintf(&output, "%s stable false\n", name)
	}
	return output.String()
}

func pinnedFeatureOutput() string {
	return `apply_patch_freeform                 removed            false
apply_patch_streaming_events         under development  false
shell_tool                           stable             true
unified_exec                         stable             true
code_mode_host                       stable             false
apps                                 stable             true
plugins                              stable             true
remote_plugin                        stable             false
hooks                                stable             false
multi_agent                          stable             true
browser_use                          stable             true
browser_use_external                 stable             false
computer_use                         stable             false
in_app_browser                       stable             false
image_generation                     stable             true
skill_search                         stable             true
skill_mcp_dependency_install         stable             false
workspace_dependencies               stable             false
experimental_feature                 experimental       true
deprecated_feature                   deprecated         false
`
}

func doctorOutput(
	schemaVersion string,
	overallStatusJSON string,
	checksJSON string,
	extraRootFields string,
) string {
	return `{"schemaVersion":` + schemaVersion +
		`,"overallStatus":` + overallStatusJSON +
		`,"checks":` + checksJSON +
		extraRootFields + `}`
}

func fixedDoctorChecks(
	authStatus string,
	configStatus string,
	installationStatus string,
) string {
	return `{"auth.credentials":{"id":"auth.credentials","status":` +
		fmt.Sprintf("%q", authStatus) +
		`,"summary":"planted auth summary","details":"planted credential"},` +
		`"config.load":{"id":"config.load","status":` +
		fmt.Sprintf("%q", configStatus) +
		`,"remediation":"planted config path"},` +
		`"installation":{"id":"installation","status":` +
		fmt.Sprintf("%q", installationStatus) +
		`,"path":"/private/planted-installation"},` +
		`"unknown.check":"planted unknown scalar"}` +
		``
}

func probeTestName(value string) string {
	replacer := strings.NewReplacer("-", "dash", "_", "underscore")
	return replacer.Replace(value)
}
