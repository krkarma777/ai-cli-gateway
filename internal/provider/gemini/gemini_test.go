package gemini

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

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

const (
	testModel    = "gemini-2.5-pro"
	testSafePath = "/trusted/bin:/usr/bin:/bin"
)

func TestAdapterIdentityAndSupportedVersion(t *testing.T) {
	adapter := New()
	if got := adapter.Name(); got != core.ProviderGemini {
		t.Fatalf("Name()=%q, want %q", got, core.ProviderGemini)
	}
	want := provider.Range{
		MinInclusive: provider.Version{Major: 0, Minor: 53, Patch: 0},
		MaxExclusive: provider.Version{Major: 0, Minor: 54, Patch: 0},
	}
	if got := adapter.SupportedVersion(); got != want {
		t.Fatalf("SupportedVersion()=%+v, want %+v", got, want)
	}
}

func TestBuildUsesExactArgsEnvironmentSettingsAndCredentialProfile(t *testing.T) {
	profiles := []struct {
		name        string
		credentials []string
		values      map[string]string
		authType    string
	}{
		{
			name:        "Gemini API key",
			credentials: []string{"GEMINI_API_KEY"},
			values:      map[string]string{"GEMINI_API_KEY": "gemini-secret-101"},
			authType:    "gemini-api-key",
		},
		{
			name:        "Google API key",
			credentials: []string{"GOOGLE_API_KEY"},
			values:      map[string]string{"GOOGLE_API_KEY": "google-secret-102"},
			authType:    "vertex-ai",
		},
		{
			name: "service account permuted",
			credentials: []string{
				"GOOGLE_CLOUD_LOCATION",
				"GOOGLE_APPLICATION_CREDENTIALS",
				"GOOGLE_CLOUD_PROJECT",
			},
			values: map[string]string{
				"GOOGLE_APPLICATION_CREDENTIALS": absoluteTestPath("service-account.json"),
				"GOOGLE_CLOUD_PROJECT":           "project-103",
				"GOOGLE_CLOUD_LOCATION":          "location-104",
			},
			authType: "vertex-ai",
		},
	}

	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			instructions := "private instructions --model attacker"
			request := core.Request{
				ModelAlias:   "public-alias",
				Instructions: &instructions,
				Input:        "private input\n-e attacker",
				Format:       core.OutputFormat{Type: core.FormatText},
			}
			cfg := testProviderConfig(profile.credentials, profile.values)
			prefixBacking := []string{"--trusted-wrapper", "fixed", "canary"}
			credentialBacking := append(slices.Clone(profile.credentials), "canary")
			cfg.PrefixArgs = prefixBacking[:2]
			cfg.CredentialEnv = credentialBacking[:len(profile.credentials)]
			runtimeDir := absoluteTestPath("request-build")

			spec, err := New().Build(
				request,
				geminiModel(),
				cfg,
				process.Runtime{ID: "build-test", Dir: runtimeDir},
			)
			if err != nil {
				t.Fatalf("Build() error: %v", err)
			}
			wantArgs := append(
				[]string{"--trusted-wrapper", "fixed"},
				expectedBuildArgs(testModel)...,
			)
			if !reflect.DeepEqual(spec.Args, wantArgs) {
				t.Fatalf("Args=%q, want %q", spec.Args, wantArgs)
			}
			if spec.Executable != cfg.Executable || spec.Dir != runtimeDir {
				t.Fatalf("Executable/Dir=%q/%q", spec.Executable, spec.Dir)
			}
			wantPrompt := provider.BuildPrompt(request, provider.SchemaInline)
			if !bytes.Equal(spec.Stdin, wantPrompt) {
				t.Fatalf("Stdin=%q, want exact framed prompt", spec.Stdin)
			}
			assertExactBuildEnvironment(t, spec.Env, cfg, runtimeDir, profile.values)
			assertExactSettingsFile(t, spec.Files, profile.authType)
			if prefixBacking[2] != "canary" || credentialBacking[len(profile.credentials)] != "canary" {
				t.Fatal("Build overwrote caller backing arrays")
			}

			metadata := strings.Join(
				append(append([]string{spec.Executable, spec.Dir}, spec.Args...), spec.Env...),
				"\x00",
			)
			for _, forbidden := range []string{
				instructions,
				request.Input,
				request.ModelAlias,
				cfg.ConfigHome,
				"gateway-bearer-secret",
				"other-provider-secret",
				"proxy-secret",
			} {
				if strings.Contains(metadata, forbidden) {
					t.Fatalf("command metadata exposed %q", forbidden)
				}
			}
			settings := string(spec.Files[0].Data)
			for name, value := range profile.values {
				if strings.Contains(settings, name) || strings.Contains(settings, value) {
					t.Fatalf("settings exposed credential material")
				}
			}
		})
	}
}

func TestBuildSchemaStaysInlineAndSystemOverridesAreNotMaterialized(t *testing.T) {
	description := "private description"
	instructions := "private instructions"
	request := core.Request{
		Instructions: &instructions,
		Input:        "private input",
		Format: core.OutputFormat{
			Type:        core.FormatJSONSchema,
			Name:        "private-output-name",
			Description: &description,
			Schema:      []byte(`{"type":"object","properties":{"private":{"type":"string"}}}`),
		},
	}
	cfg := testProviderConfig(
		[]string{"GEMINI_API_KEY"},
		map[string]string{"GEMINI_API_KEY": "private-api-key"},
	)
	runtimeDir := absoluteTestPath("schema-runtime")
	spec, err := New().Build(request, geminiModel(), cfg, process.Runtime{Dir: runtimeDir})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(spec.Args, append(cfg.PrefixArgs, expectedBuildArgs(testModel)...)) {
		t.Fatalf("Args=%q", spec.Args)
	}
	if !bytes.Equal(spec.Stdin, provider.BuildPrompt(request, provider.SchemaInline)) {
		t.Fatal("schema request did not use inline prompt")
	}
	if len(spec.Files) != 1 || spec.Files[0].Name != filepath.Join(".gemini", "settings.json") {
		t.Fatalf("Files=%+v", spec.Files)
	}
	for _, name := range []string{"system-defaults.json", "system-settings.json"} {
		if strings.Contains(spec.Files[0].Name, name) || strings.Contains(string(spec.Files[0].Data), name) {
			t.Fatalf("system override was materialized: %s", name)
		}
	}
	joinedArgs := strings.Join(spec.Args, "\x00")
	for _, value := range []string{
		instructions,
		request.Input,
		request.Format.Name,
		description,
		string(request.Format.Schema),
	} {
		if strings.Contains(joinedArgs, value) {
			t.Fatalf("request data reached argv: %q", value)
		}
	}
}

func TestBuildSnapshotsEachCredentialOnceAndFailsClosed(t *testing.T) {
	runtimeDir := absoluteTestPath("snapshot-runtime")
	request := core.Request{Format: core.OutputFormat{Type: core.FormatText}}
	credentials := []string{
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT",
		"GOOGLE_CLOUD_LOCATION",
	}
	wantValues := map[string]string{
		"GOOGLE_APPLICATION_CREDENTIALS": absoluteTestPath("credentials.json"),
		"GOOGLE_CLOUD_PROJECT":           "project",
		"GOOGLE_CLOUD_LOCATION":          "location",
	}
	counts := make(map[string]int)
	cfg := testProviderConfig(credentials, wantValues)
	cfg.LookupEnv = func(name string) (string, bool) {
		counts[name]++
		if value, ok := wantValues[name]; ok {
			return value + strings.Repeat("-changed", counts[name]-1), true
		}
		if name == "SystemRoot" {
			return `C:\Windows`, true
		}
		return "planted-unselected-secret", true
	}
	spec, err := New().Build(request, geminiModel(), cfg, process.Runtime{Dir: runtimeDir})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range credentials {
		if counts[name] != 1 {
			t.Fatalf("lookup count[%s]=%d, want 1", name, counts[name])
		}
	}
	assertExactBuildEnvironment(t, spec.Env, cfg, runtimeDir, wantValues)
	for _, entry := range spec.Env {
		if strings.Contains(entry, "planted-unselected-secret") || strings.Contains(entry, "-changed") {
			t.Fatalf("environment used unsnapshotted value: %q", entry)
		}
	}

	invalidProfiles := [][]string{
		nil,
		{"GEMINI_API_KEY", "GOOGLE_API_KEY"},
		{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT"},
		{"GEMINI_API_KEY", "GEMINI_API_KEY"},
		{"GOOGLE_GENAI_USE_VERTEXAI"},
	}
	for index, profile := range invalidProfiles {
		t.Run(fmt.Sprintf("invalid-profile-%d", index), func(t *testing.T) {
			bad := testProviderConfig(profile, map[string]string{})
			assertFixedBuildFailure(t, request, geminiModel(), bad, runtimeDir)
		})
	}

	failures := []struct {
		name   string
		values map[string]string
		lookup provider.LookupEnv
	}{
		{name: "lookup nil", lookup: nil},
		{name: "absent", lookup: func(string) (string, bool) { return "", false }},
		{name: "empty", values: map[string]string{"GEMINI_API_KEY": ""}},
		{name: "NUL", values: map[string]string{"GEMINI_API_KEY": "secret\x00tail"}},
	}
	for _, failure := range failures {
		t.Run(failure.name, func(t *testing.T) {
			bad := testProviderConfig([]string{"GEMINI_API_KEY"}, failure.values)
			if failure.lookup != nil || failure.name == "lookup nil" {
				bad.LookupEnv = failure.lookup
			}
			assertFixedBuildFailure(t, request, geminiModel(), bad, runtimeDir)
		})
	}

	serviceValues := map[string]string{
		"GOOGLE_APPLICATION_CREDENTIALS": "relative-private.json",
		"GOOGLE_CLOUD_PROJECT":           "project",
		"GOOGLE_CLOUD_LOCATION":          "location",
	}
	assertFixedBuildFailure(
		t,
		request,
		geminiModel(),
		testProviderConfig(credentials, serviceValues),
		runtimeDir,
	)
}

func TestBuildRejectsUnsafeModelRuntimeAndPromptWithFixedErrors(t *testing.T) {
	request := core.Request{Input: "private-input", Format: core.OutputFormat{Type: core.FormatText}}
	cfg := testProviderConfig(
		[]string{"GEMINI_API_KEY"},
		map[string]string{"GEMINI_API_KEY": "private-secret"},
	)
	model := geminiModel()
	runtimeDir := absoluteTestPath("safe-runtime")

	wrong := model
	wrong.Provider = core.ProviderClaude
	assertFixedBuildFailure(t, request, wrong, cfg, runtimeDir)
	badModel := model
	badModel.ProviderModel = "--private-model"
	assertFixedBuildFailure(t, request, badModel, cfg, runtimeDir)
	assertFixedBuildFailure(t, request, model, cfg, "relative/private-runtime")
	assertFixedBuildFailure(t, request, model, cfg, runtimeDir+string(filepath.Separator)+".."+string(filepath.Separator)+"unclean")
	assertFixedBuildFailure(t, request, model, cfg, runtimeDir+"\x00private")
	badPrompt := request
	badPrompt.Format.Type = core.FormatType("private-format")
	assertFixedBuildFailure(t, badPrompt, model, cfg, runtimeDir)
	badEnv := cfg
	badEnv.SafePath = "private\x00path"
	assertFixedBuildFailure(t, request, model, badEnv, runtimeDir)
}

func TestBuildReturnsFreshOwnedDataAcrossConcurrentCalls(t *testing.T) {
	request := core.Request{
		Input: "owned input",
		Format: core.OutputFormat{
			Type:   core.FormatJSONSchema,
			Name:   "owned",
			Schema: []byte(`{"type":"object"}`),
		},
	}
	cfg := testProviderConfig(
		[]string{"GEMINI_API_KEY"},
		map[string]string{"GEMINI_API_KEY": "owned-secret"},
	)
	const builds = 24
	specs := make([]process.CommandSpec, builds)
	errs := make([]error, builds)
	var wait sync.WaitGroup
	for index := range builds {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			specs[index], errs[index] = New().Build(
				request,
				geminiModel(),
				cfg,
				process.Runtime{Dir: absoluteTestPath("owned-runtime")},
			)
		}(index)
	}
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("Build[%d] error: %v", index, err)
		}
	}
	second := cloneCommandSpec(specs[1])
	specs[0].Args[0] = "mutated"
	specs[0].Env[0] = "MUTATED=1"
	specs[0].Stdin[0] = 'X'
	specs[0].Files[0].Name = "mutated"
	specs[0].Files[0].Data[0] = 'X'
	if !reflect.DeepEqual(specs[1], second) {
		t.Fatal("Build results alias mutable data")
	}
	if cfg.PrefixArgs[0] != "--trusted-prefix" || cfg.CredentialEnv[0] != "GEMINI_API_KEY" {
		t.Fatal("Build mutation reached configuration")
	}
}

func TestParseReturnsExactResponseAndDiscardsTypedMetadata(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
		want   string
	}{
		{name: "plain", stdout: `{"response":"hello"}`, want: "hello"},
		{name: "empty", stdout: `{"response":""}`},
		{name: "whitespace", stdout: `{"response":"  \n"}`, want: "  \n"},
		{
			name: "fenced with metadata",
			stdout: "{\"session_id\":\"private-session\",\"response\":\"```json\\n" +
				"{\\\"answer\\\":1}\\n```\",\"stats\":{\"nested\":{\"count\":7}}," +
				"\"warnings\":[\"private-warning\"]}",
			want: "```json\n{\"answer\":1}\n```",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := New().Parse(
				core.Request{Input: "private-prompt"},
				process.Result{Stdout: []byte(test.stdout), Stderr: []byte("private-stderr")},
			)
			if err != nil || got != test.want {
				t.Fatalf("Parse()=(%q,%v), want (%q,nil)", got, err, test.want)
			}
		})
	}
}

func TestParseExplicitErrorUnionMapsToFailed(t *testing.T) {
	tests := []struct {
		name   string
		stdout string
	}{
		{name: "without response", stdout: `{"error":{"message":"private-error"}}`},
		{name: "with string response", stdout: `{"response":"private-discarded","error":{}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := New().Parse(core.Request{}, process.Result{Stdout: []byte(test.stdout)})
			if got != "" {
				t.Fatalf("Parse returned discarded response %q", got)
			}
			assertProviderCategory(t, err, provider.ProviderErrorFailed)
			assertErrorOmits(t, err, "private-error", "private-discarded")
		})
	}
}

func TestParseRejectsClosedEnvelopeViolations(t *testing.T) {
	deep := strings.Repeat(`{"x":`, 17) + `0` + strings.Repeat(`}`, 17)
	tests := []struct {
		name   string
		stdout []byte
	}{
		{name: "empty"},
		{name: "invalid UTF-8", stdout: []byte{'{', '"', 0xff, '"', '}'}},
		{name: "malformed", stdout: []byte(`{"response":`)},
		{name: "duplicate root", stdout: []byte(`{"response":"one","response":"two"}`)},
		{name: "duplicate metadata", stdout: []byte(`{"response":"x","stats":{"n":1,"n":2}}`)},
		{name: "trailing", stdout: []byte(`{"response":"x"}{}`)},
		{name: "root array", stdout: []byte(`[]`)},
		{name: "missing response", stdout: []byte(`{"stats":{}}`)},
		{name: "wrong response", stdout: []byte(`{"response":7}`)},
		{name: "unknown", stdout: []byte(`{"response":"x","private":true}`)},
		{name: "null session", stdout: []byte(`{"response":"x","session_id":null}`)},
		{name: "wrong session", stdout: []byte(`{"response":"x","session_id":{}}`)},
		{name: "null stats", stdout: []byte(`{"response":"x","stats":null}`)},
		{name: "wrong stats", stdout: []byte(`{"response":"x","stats":[]}`)},
		{name: "null warnings", stdout: []byte(`{"response":"x","warnings":null}`)},
		{name: "wrong warnings", stdout: []byte(`{"response":"x","warnings":{}}`)},
		{name: "null error", stdout: []byte(`{"response":"x","error":null}`)},
		{name: "array error", stdout: []byte(`{"error":[]}`)},
		{name: "string error", stdout: []byte(`{"error":"private"}`)},
		{name: "number error", stdout: []byte(`{"error":7}`)},
		{name: "boolean error", stdout: []byte(`{"error":true}`)},
		{name: "wrong response error arm", stdout: []byte(`{"response":7,"error":{}}`)},
		{name: "duplicate error metadata", stdout: []byte(`{"error":{"code":1,"code":2}}`)},
		{name: "duplicate warning metadata", stdout: []byte(`{"response":"x","warnings":[{"code":1,"code":2}]}`)},
		{name: "nested depth", stdout: []byte(`{"response":"x","stats":` + deep + `}`)},
		{name: "number bound", stdout: []byte(`{"response":"x","stats":{"n":123456789012345678901}}`)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := New().Parse(
				core.Request{Input: "private-prompt"},
				process.Result{Stdout: test.stdout, Stderr: []byte("private-stderr")},
			)
			assertProviderCategory(t, err, provider.ProviderErrorProtocol)
			assertErrorOmits(t, err, string(test.stdout), "private-prompt", "private-stderr")
		})
	}
}

func TestParseNonzeroExitImmediatelyMapsToFailed(t *testing.T) {
	for index, stdout := range [][]byte{
		nil,
		{0xff},
		[]byte(`{"response":"would-be-success"}`),
		[]byte(`{"error":null}`),
		[]byte(`{"private-malformed":`),
	} {
		t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
			_, err := New().Parse(
				core.Request{Input: "private-prompt"},
				process.Result{Stdout: stdout, Stderr: []byte("private-stderr"), ExitCode: 7},
			)
			assertProviderCategory(t, err, provider.ProviderErrorFailed)
			assertErrorOmits(t, err, string(stdout), "private-prompt", "private-stderr")
		})
	}
}

func TestProbeBuildsExactlyTwoDisposableCredentialFreeCommands(t *testing.T) {
	values := map[string]string{"GEMINI_API_KEY": "private-probe-secret"}
	cfg := testProviderConfig([]string{"GEMINI_API_KEY"}, values)
	counts := make(map[string]int)
	baseLookup := cfg.LookupEnv
	cfg.LookupEnv = func(name string) (string, bool) {
		counts[name]++
		return baseLookup(name)
	}
	runner := &scriptedProbeRunner{t: t, steps: healthyProbeSteps(), mutateBuilt: true}
	//nolint:staticcheck // Exercise the explicit nil-context contract.
	health := New().Probe(nil, cfg, runner)
	assertProbeOutcome(t, health, provider.HealthReady, "configured", nil, true)
	if health.Version != "0.53.0" {
		t.Fatalf("Version=%q", health.Version)
	}
	if counts["GEMINI_API_KEY"] != 1 {
		t.Fatalf("credential lookup count=%d, want 1", counts["GEMINI_API_KEY"])
	}
	wantCommands := [][]string{{"--version"}, {"--help"}}
	if len(runner.specs) != len(wantCommands) {
		t.Fatalf("probe specs=%d", len(runner.specs))
	}
	for index, command := range wantCommands {
		spec := runner.specs[index]
		wantArgs := append(slices.Clone(cfg.PrefixArgs), command...)
		if !reflect.DeepEqual(spec.Args, wantArgs) {
			t.Fatalf("probe[%d] Args=%q, want %q", index, spec.Args, wantArgs)
		}
		if spec.Dir != runner.runtimes[index].Dir || spec.Stdin != nil || spec.Files != nil {
			t.Fatalf("probe[%d] is not disposable: %+v", index, spec)
		}
		assertExactProbeEnvironment(t, spec.Env, cfg, runner.runtimes[index].Dir)
		for _, entry := range spec.Env {
			if strings.Contains(entry, "private-probe-secret") || strings.HasPrefix(entry, "GEMINI_API_KEY=") {
				t.Fatalf("probe relayed credential: %q", entry)
			}
		}
	}
	if runner.runtimes[0].Dir == runner.runtimes[1].Dir {
		t.Fatal("probe reused runtime")
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
				Stdout:   []byte("Gemini CLI 0.53.0\n"),
				ExitCode: 1,
			},
			wantProblem: provider.ProblemVersionUnreadable,
		},
		{
			name:        "malformed",
			result:      process.Result{Stdout: []byte("Gemini CLI private")},
			wantProblem: provider.ProblemVersionUnreadable,
		},
		{
			name: "ambiguous",
			result: process.Result{
				Stdout: []byte("Gemini CLI 0.53.0 and 0.53.1\n"),
			},
			wantProblem: provider.ProblemVersionUnreadable,
		},
		{
			name: "below minimum",
			result: process.Result{
				Stdout: []byte("Gemini CLI 0.52.999\n"),
			},
			wantVersion: "0.52.999",
			wantProblem: provider.ProblemVersionUnsupported,
		},
		{
			name: "exclusive maximum",
			result: process.Result{
				Stdout: []byte("Gemini CLI 0.54.0\n"),
			},
			wantVersion: "0.54.0",
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
				configuredTestProvider(),
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
				"configured",
				[]string{
					test.wantProblem,
					provider.ProblemCapabilityMissing,
				},
				false,
			)
			if health.Version != test.wantVersion {
				t.Fatalf("Version=%q, want %q", health.Version, test.wantVersion)
			}
		})
	}
}

func TestProbeVersionHelpAndAuthStatesFailClosed(t *testing.T) {
	t.Run("version range", func(t *testing.T) {
		tests := []struct {
			text     string
			exitCode int
			problem  string
			version  string
		}{
			{text: "0.53.0\n", version: "0.53.0"},
			{text: "gemini 0.53.999\n", version: "0.53.999"},
			{text: "0.52.999\n", version: "0.52.999", problem: provider.ProblemVersionUnsupported},
			{text: "0.54.0\n", version: "0.54.0", problem: provider.ProblemVersionUnsupported},
			{text: "private-version", problem: provider.ProblemVersionUnreadable},
			{text: "0.53.0", exitCode: 1, problem: provider.ProblemVersionUnreadable},
		}
		for index, test := range tests {
			t.Run(fmt.Sprintf("case-%d", index), func(t *testing.T) {
				steps := healthyProbeSteps()
				steps[0].result.Stdout = []byte(test.text)
				steps[0].result.ExitCode = test.exitCode
				health := probeWith(t, testProviderConfig(
					[]string{"GEMINI_API_KEY"},
					map[string]string{"GEMINI_API_KEY": "secret"},
				), steps)
				wantProblems := []string(nil)
				wantStatus := provider.HealthReady
				wantCapabilities := true
				if test.problem != "" {
					wantProblems = []string{
						test.problem,
						provider.ProblemCapabilityMissing,
					}
					wantStatus = provider.HealthNotReady
					wantCapabilities = false
				}
				assertProbeOutcome(
					t,
					health,
					wantStatus,
					"configured",
					wantProblems,
					wantCapabilities,
				)
				if health.Version != test.version {
					t.Fatalf("Version=%q, want %q", health.Version, test.version)
				}
			})
		}
	})

	t.Run("help token boundaries", func(t *testing.T) {
		for _, token := range requiredProbeHelpTokens() {
			t.Run(token, func(t *testing.T) {
				steps := healthyProbeSteps()
				steps[1].result.Stdout = []byte(strings.Replace(healthyHelp(), token+"\n", "", 1))
				health := probeWith(t, configuredTestProvider(), steps)
				assertProbeOutcome(t, health, provider.HealthNotReady, "configured", []string{provider.ProblemCapabilityMissing}, false)
			})
		}
		for _, collision := range []string{
			"--output-format-evil\n--approval-mode\n-e\n--extensions\n--model\n",
			"--output-format\n--approval-mode\nprose-e-embedded\n--extensions\n--model\n",
			"--output-format\n--approval-mode\n-e\n--extensions_unsafe\n--model\n",
			"--output-format\n--approval-mode\n-e\n--extensions\n--model-evil\n",
		} {
			steps := healthyProbeSteps()
			steps[1].result.Stdout = []byte(collision)
			health := probeWith(t, configuredTestProvider(), steps)
			assertProbeOutcome(t, health, provider.HealthNotReady, "configured", []string{provider.ProblemCapabilityMissing}, false)
		}
	})

	t.Run("auth states", func(t *testing.T) {
		tests := []struct {
			name        string
			credentials []string
			values      map[string]string
			wantAuth    string
			wantProblem string
		}{
			{name: "configured", credentials: []string{"GOOGLE_API_KEY"}, values: map[string]string{"GOOGLE_API_KEY": "secret"}, wantAuth: "configured"},
			{name: "empty profile", wantAuth: "unknown", wantProblem: provider.ProblemAuthUnknown},
			{name: "invalid profile", credentials: []string{"GEMINI_API_KEY", "GOOGLE_API_KEY"}, wantAuth: "unknown", wantProblem: provider.ProblemAuthUnknown},
			{name: "missing", credentials: []string{"GEMINI_API_KEY"}, values: map[string]string{}, wantAuth: "missing", wantProblem: provider.ProblemCredentialMissing},
			{name: "empty", credentials: []string{"GEMINI_API_KEY"}, values: map[string]string{"GEMINI_API_KEY": ""}, wantAuth: "missing", wantProblem: provider.ProblemCredentialMissing},
			{name: "NUL", credentials: []string{"GEMINI_API_KEY"}, values: map[string]string{"GEMINI_API_KEY": "secret\x00tail"}, wantAuth: "missing", wantProblem: provider.ProblemCredentialMissing},
			{name: "relative service file", credentials: []string{"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION"}, values: map[string]string{"GOOGLE_APPLICATION_CREDENTIALS": "relative.json", "GOOGLE_CLOUD_PROJECT": "project", "GOOGLE_CLOUD_LOCATION": "location"}, wantAuth: "missing", wantProblem: provider.ProblemCredentialMissing},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				health := probeWith(t, testProviderConfig(test.credentials, test.values), healthyProbeSteps())
				problems := []string(nil)
				status := provider.HealthReady
				if test.wantProblem != "" {
					problems = []string{test.wantProblem}
					status = provider.HealthNotReady
				}
				assertProbeOutcome(t, health, status, test.wantAuth, problems, true)
			})
		}
	})
}

func TestProbeComputesAuthWhenRunnerOrCommandBuilderFails(t *testing.T) {
	cfg := testProviderConfig([]string{"GEMINI_API_KEY"}, map[string]string{})
	health := New().Probe(context.Background(), cfg, nil)
	assertProbeOutcome(
		t,
		health,
		provider.HealthNotReady,
		"missing",
		[]string{
			provider.ProblemVersionUnreadable,
			provider.ProblemCapabilityMissing,
			provider.ProblemCredentialMissing,
		},
		false,
	)

	cfg = configuredTestProvider()
	cfg.SafePath = "private\x00path"
	runner := &scriptedProbeRunner{t: t, steps: healthyProbeSteps()}
	health = New().Probe(context.Background(), cfg, runner)
	assertProbeOutcome(
		t,
		health,
		provider.HealthNotReady,
		"configured",
		[]string{provider.ProblemVersionUnreadable, provider.ProblemCapabilityMissing},
		false,
	)
	if runner.calls != 1 {
		t.Fatalf("runner calls=%d, want 1", runner.calls)
	}
}

func TestProbeCommandFailuresAndInvalidUTF8AreClosed(t *testing.T) {
	tests := []struct {
		name         string
		step         int
		result       process.Result
		err          error
		wantProblems []string
		capabilities bool
	}{
		{
			name: "version runner error",
			step: 0,
			err:  errors.New("private version runner error"),
			wantProblems: []string{
				provider.ProblemVersionUnreadable,
				provider.ProblemCapabilityMissing,
			},
			capabilities: false,
		},
		{
			name:   "version invalid UTF-8",
			step:   0,
			result: process.Result{Stdout: []byte{0xff}},
			wantProblems: []string{
				provider.ProblemVersionUnreadable,
				provider.ProblemCapabilityMissing,
			},
			capabilities: false,
		},
		{
			name:         "help runner error",
			step:         1,
			err:          errors.New("private help runner error"),
			wantProblems: []string{provider.ProblemCapabilityMissing},
		},
		{
			name:         "help nonzero",
			step:         1,
			result:       process.Result{Stdout: []byte(healthyHelp()), ExitCode: 2},
			wantProblems: []string{provider.ProblemCapabilityMissing},
		},
		{
			name:         "help invalid UTF-8",
			step:         1,
			result:       process.Result{Stdout: []byte{0xff}},
			wantProblems: []string{provider.ProblemCapabilityMissing},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			steps := healthyProbeSteps()
			steps[test.step] = scriptedProbeStep{result: test.result, err: test.err}
			health := probeWith(t, configuredTestProvider(), steps)
			assertProbeOutcome(
				t,
				health,
				provider.HealthNotReady,
				"configured",
				test.wantProblems,
				test.capabilities,
			)
		})
	}
}

func TestProbeAllProfilesSnapshotEverySelectedValueOnce(t *testing.T) {
	profiles := []struct {
		name        string
		credentials []string
		values      map[string]string
	}{
		{
			name:        "Gemini API key",
			credentials: []string{geminiAPIKeyName},
			values:      map[string]string{geminiAPIKeyName: "gemini-value"},
		},
		{
			name:        "Google API key",
			credentials: []string{googleAPIKeyName},
			values:      map[string]string{googleAPIKeyName: "google-value"},
		},
		{
			name: "service account permuted",
			credentials: []string{
				googleCloudProjectName,
				googleCloudLocationName,
				googleCredentialsName,
			},
			values: map[string]string{
				googleCredentialsName:   absoluteTestPath("probe-service.json"),
				googleCloudProjectName:  "project",
				googleCloudLocationName: "location",
			},
		},
	}
	for _, profile := range profiles {
		t.Run(profile.name, func(t *testing.T) {
			cfg := testProviderConfig(profile.credentials, profile.values)
			lookup := cfg.LookupEnv
			counts := make(map[string]int)
			cfg.LookupEnv = func(name string) (string, bool) {
				counts[name]++
				return lookup(name)
			}
			health := probeWith(t, cfg, healthyProbeSteps())
			assertProbeOutcome(t, health, provider.HealthReady, "configured", nil, true)
			for _, name := range profile.credentials {
				if counts[name] != 1 {
					t.Fatalf("selected credential lookup count=%d, want 1", counts[name])
				}
			}
		})
	}
}

func TestProbeServiceAccountEachValueFailureIsCredentialMissing(t *testing.T) {
	credentials := []string{
		googleCredentialsName,
		googleCloudProjectName,
		googleCloudLocationName,
	}
	valid := map[string]string{
		googleCredentialsName:   absoluteTestPath("service.json"),
		googleCloudProjectName:  "project",
		googleCloudLocationName: "location",
	}
	for _, name := range credentials {
		for _, state := range []string{"absent", "empty", "NUL"} {
			t.Run(name+"/"+state, func(t *testing.T) {
				values := make(map[string]string, len(valid))
				for key, value := range valid {
					values[key] = value
				}
				switch state {
				case "absent":
					delete(values, name)
				case "empty":
					values[name] = ""
				case "NUL":
					values[name] = "value\x00tail"
				}
				health := probeWith(t, testProviderConfig(credentials, values), healthyProbeSteps())
				assertProbeOutcome(
					t,
					health,
					provider.HealthNotReady,
					"missing",
					[]string{provider.ProblemCredentialMissing},
					true,
				)
			})
		}
	}
}

func TestProbeHealthResultsAreFreshOwnedAndNilRunnerKeepsAuth(t *testing.T) {
	cfg := configuredTestProvider()
	nilRunnerHealth := New().Probe(context.Background(), cfg, nil)
	assertProbeOutcome(
		t,
		nilRunnerHealth,
		provider.HealthNotReady,
		"configured",
		[]string{provider.ProblemVersionUnreadable, provider.ProblemCapabilityMissing},
		false,
	)

	cfg = testProviderConfig(
		[]string{"GEMINI_API_KEY"},
		map[string]string{},
	)
	steps := healthyProbeSteps()
	steps[0].result.Stdout = []byte("0.53.999 private-version-output")
	first := probeWith(t, cfg, steps)
	secondSteps := healthyProbeSteps()
	secondSteps[0].result.Stdout = bytes.Clone(steps[0].result.Stdout)
	second := probeWith(t, cfg, secondSteps)
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("Probe results are nondeterministic: %+v / %+v", first, second)
	}
	first.Capabilities[0] = "mutated"
	first.Problems[0] = "mutated"
	if second.Capabilities[0] != "stdin_prompt" ||
		second.Problems[0] != provider.ProblemCredentialMissing {
		t.Fatalf("Probe results alias mutable global state: %+v", second)
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
		r.t.Fatal("Probe passed nil context")
	}
	index := r.calls
	r.calls++
	if index >= len(r.steps) {
		return process.Result{}, errors.New("unexpected extra probe")
	}
	runtimeDir := absoluteTestPath(fmt.Sprintf("probe-runtime-%d", index))
	rt := process.Runtime{ID: fmt.Sprintf("probe-%08d", index), Dir: runtimeDir}
	spec, err := build(rt)
	if err != nil {
		return process.Result{}, err
	}
	r.specs = append(r.specs, cloneCommandSpec(spec))
	r.runtimes = append(r.runtimes, rt)
	if r.mutateBuilt {
		if len(spec.Args) > 0 {
			spec.Args[0] = "mutated"
		}
		if len(spec.Env) > 0 {
			spec.Env[0] = "MUTATED=1"
		}
	}
	step := r.steps[index]
	return step.result, step.err
}

func healthyProbeSteps() []scriptedProbeStep {
	return []scriptedProbeStep{
		{result: process.Result{Stdout: []byte("Gemini CLI 0.53.0\n")}},
		{result: process.Result{Stdout: []byte(healthyHelp())}},
	}
}

func probeWith(t *testing.T, cfg provider.ProviderConfig, steps []scriptedProbeStep) provider.Health {
	t.Helper()
	return New().Probe(context.Background(), cfg, &scriptedProbeRunner{t: t, steps: steps})
}

func assertProbeOutcome(
	t *testing.T,
	health provider.Health,
	status provider.HealthStatus,
	auth string,
	problems []string,
	capabilities bool,
) {
	t.Helper()
	if health.Provider != core.ProviderGemini || health.Status != status || health.Auth != auth {
		t.Fatalf("Health=%+v, want provider/status/auth=%q/%q/%q", health, core.ProviderGemini, status, auth)
	}
	if !reflect.DeepEqual(health.Problems, problems) {
		t.Fatalf("Problems=%q, want %q", health.Problems, problems)
	}
	wantCapabilities := []string(nil)
	if capabilities {
		wantCapabilities = []string{
			"stdin_prompt",
			"json_envelope",
			"disposable_home",
			"system_settings_isolated",
			"empty_core_tools",
			"extensions_disabled",
		}
	}
	if !reflect.DeepEqual(health.Capabilities, wantCapabilities) {
		t.Fatalf("Capabilities=%q, want %q", health.Capabilities, wantCapabilities)
	}
	encoded, err := json.Marshal(health)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"private", "secret", "GEMINI_API_KEY", "GOOGLE_"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("Health exposed %q: %s", forbidden, encoded)
		}
	}
}

func requiredProbeHelpTokens() []string {
	return []string{"--output-format", "--approval-mode", "-e", "--extensions", "--model"}
}

func healthyHelp() string {
	return strings.Join(requiredProbeHelpTokens(), "\n") + "\n"
}

func expectedBuildArgs(model string) []string {
	return []string{
		"--output-format", "json",
		"--approval-mode", "default",
		"-e", "none",
		"--model", model,
	}
}

func geminiModel() core.Model {
	return core.Model{ID: "public-gemini", Provider: core.ProviderGemini, ProviderModel: testModel}
}

func configuredTestProvider() provider.ProviderConfig {
	return testProviderConfig(
		[]string{"GEMINI_API_KEY"},
		map[string]string{"GEMINI_API_KEY": "configured-value"},
	)
}

func testProviderConfig(credentials []string, selected map[string]string) provider.ProviderConfig {
	lookup := map[string]string{
		"SystemRoot":                `C:\Windows`,
		"AI_CLI_GATEWAY_API_KEY":    "gateway-bearer-secret",
		"OPENAI_API_KEY":            "other-provider-secret",
		"ANTHROPIC_API_KEY":         "other-provider-secret",
		"HTTPS_PROXY":               "proxy-secret",
		"SSL_CERT_FILE":             "ca-secret",
		"GOOGLE_GENAI_USE_VERTEXAI": "selector-secret",
		"USER_IDENTITY":             "identity@example.test",
	}
	for name, value := range selected {
		lookup[name] = value
	}
	return provider.ProviderConfig{
		Executable:    absoluteTestPath("bin", "gemini"),
		PrefixArgs:    []string{"--trusted-prefix", "fixed"},
		ConfigHome:    absoluteTestPath("persistent-config-home-private"),
		CredentialEnv: slices.Clone(credentials),
		SafePath:      testSafePath,
		LookupEnv: func(name string) (string, bool) {
			value, ok := lookup[name]
			return value, ok
		},
	}
}

func assertExactBuildEnvironment(
	t *testing.T,
	got []string,
	cfg provider.ProviderConfig,
	runtimeDir string,
	credentials map[string]string,
) {
	t.Helper()
	want := baseEnvironment(cfg, runtimeDir)
	for name, value := range credentials {
		want = append(want, name+"="+value)
	}
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("environment does not match the closed expected set")
	}
}

func assertExactProbeEnvironment(t *testing.T, got []string, cfg provider.ProviderConfig, runtimeDir string) {
	t.Helper()
	want := baseEnvironment(cfg, runtimeDir)
	slices.Sort(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatal("probe environment does not match the closed expected set")
	}
}

func baseEnvironment(cfg provider.ProviderConfig, runtimeDir string) []string {
	settingsDir := filepath.Join(runtimeDir, ".gemini")
	want := []string{
		"GEMINI_CLI_HOME=" + runtimeDir,
		"GEMINI_CLI_SYSTEM_DEFAULTS_PATH=" + filepath.Join(settingsDir, "system-defaults.json"),
		"GEMINI_CLI_SYSTEM_SETTINGS_PATH=" + filepath.Join(settingsDir, "system-settings.json"),
		"HOME=" + runtimeDir,
		"NO_COLOR=1",
		"PATH=" + cfg.SafePath,
		"TEMP=" + runtimeDir,
		"TMP=" + runtimeDir,
		"TMPDIR=" + runtimeDir,
	}
	if runtime.GOOS == "windows" {
		want = append(want, `SystemRoot=C:\Windows`)
	}
	return want
}

func assertExactSettingsFile(t *testing.T, files []process.FileSpec, authType string) {
	t.Helper()
	if len(files) != 1 {
		t.Fatalf("Files=%+v, want exactly one", files)
	}
	file := files[0]
	if file.Name != filepath.Join(".gemini", "settings.json") || file.Mode != 0o600 {
		t.Fatalf("settings FileSpec=%+v", file)
	}
	if got, want := string(file.Data), expectedSettings(authType); got != want {
		t.Fatalf("settings JSON\n got: %s\nwant: %s", got, want)
	}
	var value map[string]any
	if err := json.Unmarshal(file.Data, &value); err != nil {
		t.Fatal(err)
	}
	if allowed := value["mcp"].(map[string]any)["allowed"]; reflect.ValueOf(allowed).Len() != 0 {
		t.Fatalf("mcp.allowed=%v", allowed)
	}
	if servers := value["mcpServers"].(map[string]any); len(servers) != 0 {
		t.Fatalf("mcpServers=%v", servers)
	}
}

func expectedSettings(authType string) string {
	return `{"advanced":{"ignoreLocalEnv":true},` +
		`"experimental":{"enableAgents":false},` +
		`"hooksConfig":{"enabled":false},` +
		`"mcp":{"allowed":[]},"mcpServers":{},` +
		`"privacy":{"usageStatisticsEnabled":false},` +
		`"security":{"auth":{"selectedType":"` + authType +
		`"},"folderTrust":{"enabled":false}},` +
		`"skills":{"enabled":false},` +
		`"telemetry":{"enabled":false,"logPrompts":false},` +
		`"tools":{"core":[]}}`
}

func assertFixedBuildFailure(
	t *testing.T,
	request core.Request,
	model core.Model,
	cfg provider.ProviderConfig,
	runtimeDir string,
) {
	t.Helper()
	first, firstErr := New().Build(request, model, cfg, process.Runtime{Dir: runtimeDir})
	second, secondErr := New().Build(request, model, cfg, process.Runtime{Dir: runtimeDir})
	if firstErr == nil || secondErr == nil || firstErr.Error() != secondErr.Error() {
		t.Fatalf("Build errors=(%v,%v)", firstErr, secondErr)
	}
	if !reflect.DeepEqual(first, process.CommandSpec{}) || !reflect.DeepEqual(second, process.CommandSpec{}) {
		t.Fatalf("failed Build returned partial specs: %+v / %+v", first, second)
	}
	for _, forbidden := range []string{
		"private", "secret", "GOOGLE", "GEMINI", runtimeDir, model.ProviderModel,
	} {
		if forbidden != "" && strings.Contains(firstErr.Error(), forbidden) {
			t.Fatalf("Build error exposed %q: %v", forbidden, firstErr)
		}
	}
}

func assertProviderCategory(t *testing.T, err error, want provider.ErrorCategory) {
	t.Helper()
	var providerErr *provider.ProviderError
	if !errors.As(err, &providerErr) || providerErr.Category() != want {
		t.Fatalf("error=%v (%T), want category %q", err, err, want)
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

func absoluteTestPath(elements ...string) string {
	if runtime.GOOS == "windows" {
		return filepath.Join(append([]string{`C:\trusted`}, elements...)...)
	}
	return filepath.Join(append([]string{string(filepath.Separator), "trusted"}, elements...)...)
}
