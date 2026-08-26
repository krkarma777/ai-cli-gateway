package initconfig

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestDiscoverProvidersOrdersCandidatesByPrecedence(t *testing.T) {
	configPath := testAbsolutePath("configuration", "config.toml")
	userHome := testAbsolutePath("users", "alice")
	pathResults := map[string]string{
		"codex":  filepath.Join("lookup", "codex"),
		"claude": filepath.Join("lookup", "claude"),
		"gemini": filepath.Join("lookup", "gemini"),
	}
	absoluteResults := map[string]string{
		pathResults["codex"]:  testAbsolutePath("path", "codex"),
		pathResults["claude"]: testAbsolutePath("path", "claude"),
		pathResults["gemini"]: testAbsolutePath("path", "gemini"),
	}
	environment := map[string]string{
		"CODEX_HOME":        testAbsolutePath("environment", "codex"),
		"CLAUDE_CONFIG_DIR": testAbsolutePath("environment", "claude"),
	}

	options := Options{
		Providers: []core.ProviderName{
			core.ProviderCodex,
			core.ProviderClaude,
			core.ProviderGemini,
		},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderCodex: {
				Executable: setString(testAbsolutePath("explicit", "codex")),
				ConfigHome: setString(testAbsolutePath("explicit-home", "codex")),
			},
			core.ProviderClaude: {
				Executable: setString(testAbsolutePath("explicit", "claude")),
				ConfigHome: setString(testAbsolutePath("explicit-home", "claude")),
			},
			core.ProviderGemini: {
				Executable: setString(testAbsolutePath("explicit", "gemini")),
				ConfigHome: setString(testAbsolutePath("explicit-home", "gemini")),
			},
		},
	}
	existing := &config.Config{Providers: map[string]config.Provider{
		"codex": {
			Executable: testAbsolutePath("existing", "codex"),
			ConfigHome: testAbsolutePath("existing-home", "codex"),
		},
		"claude": {
			Executable: testAbsolutePath("existing", "claude"),
			ConfigHome: testAbsolutePath("existing-home", "claude"),
		},
		"gemini": {
			Executable:    testAbsolutePath("existing", "gemini"),
			ConfigHome:    testAbsolutePath("existing-home", "gemini"),
			CredentialEnv: []string{"GEMINI_API_KEY"},
		},
	}}
	deps := DiscoveryDependencies{
		LookupEnv: lookupDiscoveryEnvironment(environment),
		LookPath: func(name string) (string, error) {
			return pathResults[name], nil
		},
		UserHomeDir: func() (string, error) { return userHome, nil },
		AbsPath: func(path string) (string, error) {
			return absoluteResults[path], nil
		},
		OpenCommandPath: func(string, CommandReadMode, int64) (CommandFileInspection, error) {
			t.Fatal("DiscoverProviders called Task 2 command inspection")
			return nil, nil
		},
	}

	got, err := DiscoverProviders(context.Background(), configPath, options, existing, deps)
	if err != nil {
		t.Fatalf("DiscoverProviders() error = %v", err)
	}

	for _, name := range options.Providers {
		discovery, ok := got[name]
		if !ok {
			t.Fatalf("DiscoverProviders() omitted %q", name)
		}
		wantCommands := []CommandCandidate{
			{
				Command: ProviderCommand{Executable: testAbsolutePath("explicit", string(name))},
				Source:  CandidateExplicit,
			},
			{
				Command: ProviderCommand{Executable: testAbsolutePath("existing", string(name))},
				Source:  CandidateExisting,
			},
			{
				Command: ProviderCommand{Executable: testAbsolutePath("path", string(name))},
				Source:  CandidatePATH,
			},
		}
		if !reflect.DeepEqual(discovery.Commands, wantCommands) {
			t.Errorf("DiscoverProviders()[%q].Commands = %#v, want %#v", name, discovery.Commands, wantCommands)
		}

		wantHomes := []PathCandidate{
			{Path: testAbsolutePath("explicit-home", string(name)), Source: CandidateExplicit},
			{Path: testAbsolutePath("existing-home", string(name)), Source: CandidateExisting},
		}
		switch name {
		case core.ProviderCodex:
			wantHomes = append(wantHomes,
				PathCandidate{Path: environment["CODEX_HOME"], Source: CandidateEnvironment},
				PathCandidate{Path: filepath.Join(userHome, ".codex"), Source: CandidateConventional},
			)
		case core.ProviderClaude:
			wantHomes = append(wantHomes,
				PathCandidate{Path: environment["CLAUDE_CONFIG_DIR"], Source: CandidateEnvironment},
				PathCandidate{Path: filepath.Join(userHome, ".claude"), Source: CandidateConventional},
			)
		case core.ProviderGemini:
			wantHomes = append(wantHomes,
				PathCandidate{Path: filepath.Join(userHome, ".gemini"), Source: CandidateConventional},
			)
		}
		wantHomes = append(wantHomes, PathCandidate{
			Path:   filepath.Join(filepath.Dir(configPath), "providers", string(name)),
			Source: CandidateDedicated,
		})
		if !reflect.DeepEqual(discovery.ConfigHomes, wantHomes) {
			t.Errorf("DiscoverProviders()[%q].ConfigHomes = %#v, want %#v", name, discovery.ConfigHomes, wantHomes)
		}
	}

	assertAuthChoices(t, got[core.ProviderCodex].AuthChoices, []AuthID{AuthConfigHome})
	assertAuthChoices(t, got[core.ProviderClaude].AuthChoices, []AuthID{
		AuthConfigHome,
		AuthAnthropicAPIKey,
	})
	assertAuthChoices(t, got[core.ProviderGemini].AuthChoices, []AuthID{
		AuthGeminiAPIKey,
		AuthGoogleAPIKey,
		AuthVertexServiceAccount,
	})
}

func TestDiscoverProvidersStableDeduplicationKeepsFirstSource(t *testing.T) {
	configPath := testAbsolutePath("configuration", "config.toml")
	sharedCommand := testAbsolutePath("bin", "codex")
	userHome := testAbsolutePath("users", "alice")
	sharedHome := filepath.Join(userHome, ".codex")
	options := Options{
		Providers: []core.ProviderName{core.ProviderCodex},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderCodex: {
				Executable: setString(sharedCommand),
				ConfigHome: setString(sharedHome),
			},
		},
	}
	existing := &config.Config{Providers: map[string]config.Provider{
		"codex": {Executable: sharedCommand, ConfigHome: sharedHome},
	}}
	deps := DiscoveryDependencies{
		LookupEnv: lookupDiscoveryEnvironment(map[string]string{"CODEX_HOME": sharedHome}),
		LookPath:  func(string) (string, error) { return "relative-codex", nil },
		UserHomeDir: func() (string, error) {
			return userHome, nil
		},
		AbsPath: func(string) (string, error) { return sharedCommand, nil },
	}

	got, err := DiscoverProviders(context.Background(), configPath, options, existing, deps)
	if err != nil {
		t.Fatalf("DiscoverProviders() error = %v", err)
	}
	discovery := got[core.ProviderCodex]
	wantCommands := []CommandCandidate{{
		Command: ProviderCommand{Executable: sharedCommand},
		Source:  CandidateExplicit,
	}}
	if !reflect.DeepEqual(discovery.Commands, wantCommands) {
		t.Fatalf("Commands = %#v, want %#v", discovery.Commands, wantCommands)
	}
	wantHomes := []PathCandidate{
		{Path: sharedHome, Source: CandidateExplicit},
		{
			Path:   filepath.Join(filepath.Dir(configPath), "providers", "codex"),
			Source: CandidateDedicated,
		},
	}
	if !reflect.DeepEqual(discovery.ConfigHomes, wantHomes) {
		t.Fatalf("ConfigHomes = %#v, want %#v", discovery.ConfigHomes, wantHomes)
	}
}

func TestDiscoverProvidersKeepsSafeRelativeEnvironmentHomeHints(t *testing.T) {
	tests := []struct {
		name         string
		provider     core.ProviderName
		environment  string
		relativeHome string
	}{
		{
			name:         "Codex",
			provider:     core.ProviderCodex,
			environment:  "CODEX_HOME",
			relativeHome: filepath.Join("relative", "codex-home"),
		},
		{
			name:         "Claude",
			provider:     core.ProviderClaude,
			environment:  "CLAUDE_CONFIG_DIR",
			relativeHome: filepath.Join("relative", "claude-home"),
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			deps := DiscoveryDependencies{
				LookupEnv: func(name string) (string, bool) {
					if name == test.environment {
						return test.relativeHome, true
					}
					return "", false
				},
				LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
			}
			got, err := DiscoverProviders(
				context.Background(),
				testAbsolutePath("configuration", "config.toml"),
				Options{Providers: []core.ProviderName{test.provider}},
				nil,
				deps,
			)
			if err != nil {
				t.Fatalf("DiscoverProviders() returned an unexpected fixed error")
			}
			homes := got[test.provider].ConfigHomes
			if len(homes) == 0 || homes[0] != (PathCandidate{
				Path:   test.relativeHome,
				Source: CandidateEnvironment,
			}) {
				t.Fatalf("first ConfigHomes candidate = %#v, want the relative environment hint", homes)
			}
		})
	}
}

func TestDiscoverProvidersRanksAuthenticationByExplicitExistingAndPresence(t *testing.T) {
	configPath := testAbsolutePath("configuration", "config.toml")
	vertexProfile := []string{
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT",
		"GOOGLE_CLOUD_LOCATION",
	}
	options := Options{
		Providers: []core.ProviderName{core.ProviderClaude, core.ProviderGemini},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderClaude: {Auth: AuthConfigHome, AuthSet: true},
			core.ProviderGemini: {Auth: AuthGoogleAPIKey, AuthSet: true},
		},
	}
	existing := &config.Config{Providers: map[string]config.Provider{
		"claude": {CredentialEnv: []string{"ANTHROPIC_API_KEY"}},
		"gemini": {CredentialEnv: vertexProfile},
	}}
	deps := discoveryDependenciesWithoutPaths(map[string]string{
		"ANTHROPIC_API_KEY": "present-anthropic-value",
		"GEMINI_API_KEY":    "present-gemini-value",
	})

	got, err := DiscoverProviders(context.Background(), configPath, options, existing, deps)
	if err != nil {
		t.Fatalf("DiscoverProviders() error = %v", err)
	}
	assertAuthChoices(t, got[core.ProviderClaude].AuthChoices, []AuthID{
		AuthConfigHome,
		AuthAnthropicAPIKey,
	})
	assertAuthChoices(t, got[core.ProviderGemini].AuthChoices, []AuthID{
		AuthGoogleAPIKey,
		AuthVertexServiceAccount,
		AuthGeminiAPIKey,
	})
}

func TestDiscoverProvidersRanksOnlyCompleteCredentialProfiles(t *testing.T) {
	configPath := testAbsolutePath("configuration", "config.toml")
	tests := []struct {
		name        string
		provider    core.ProviderName
		environment map[string]string
		want        []AuthID
	}{
		{
			name:     "present Anthropic key",
			provider: core.ProviderClaude,
			environment: map[string]string{
				"ANTHROPIC_API_KEY": "unique-anthropic-secret",
			},
			want: []AuthID{AuthAnthropicAPIKey, AuthConfigHome},
		},
		{
			name:     "complete Vertex profile",
			provider: core.ProviderGemini,
			environment: map[string]string{
				"GOOGLE_APPLICATION_CREDENTIALS": "unique-service-account-secret",
				"GOOGLE_CLOUD_PROJECT":           "unique-project-secret",
				"GOOGLE_CLOUD_LOCATION":          "unique-location-secret",
			},
			want: []AuthID{
				AuthVertexServiceAccount,
				AuthGeminiAPIKey,
				AuthGoogleAPIKey,
			},
		},
		{
			name:     "incomplete Vertex profile",
			provider: core.ProviderGemini,
			environment: map[string]string{
				"GOOGLE_APPLICATION_CREDENTIALS": "unique-service-account-secret",
				"GOOGLE_CLOUD_PROJECT":           "unique-project-secret",
			},
			want: []AuthID{
				AuthGeminiAPIKey,
				AuthGoogleAPIKey,
				AuthVertexServiceAccount,
			},
		},
		{
			name:     "NUL credential is absent",
			provider: core.ProviderGemini,
			environment: map[string]string{
				"GOOGLE_API_KEY": "unique-google-secret\x00suffix",
			},
			want: []AuthID{
				AuthGeminiAPIKey,
				AuthGoogleAPIKey,
				AuthVertexServiceAccount,
			},
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			options := Options{Providers: []core.ProviderName{test.provider}}
			got, err := DiscoverProviders(
				context.Background(),
				configPath,
				options,
				nil,
				discoveryDependenciesWithoutPaths(test.environment),
			)
			if err != nil {
				t.Fatalf("DiscoverProviders() error = %v", err)
			}
			assertAuthChoices(t, got[test.provider].AuthChoices, test.want)
		})
	}
}

func TestDiscoverProvidersSkipsEveryUnselectedProviderDependency(t *testing.T) {
	var lookedUpCommands []string
	var lookedUpEnvironment []string
	inspectionCalls := 0
	deps := DiscoveryDependencies{
		LookupEnv: func(name string) (string, bool) {
			lookedUpEnvironment = append(lookedUpEnvironment, name)
			return "", false
		},
		LookPath: func(name string) (string, error) {
			lookedUpCommands = append(lookedUpCommands, name)
			return "", exec.ErrNotFound
		},
		UserHomeDir: func() (string, error) {
			return testAbsolutePath("users", "alice"), nil
		},
		AbsPath: func(string) (string, error) {
			t.Fatal("AbsPath called without a PATH result")
			return "", nil
		},
		OpenCommandPath: func(string, CommandReadMode, int64) (CommandFileInspection, error) {
			inspectionCalls++
			return nil, nil
		},
	}

	got, err := DiscoverProviders(
		context.Background(),
		testAbsolutePath("configuration", "config.toml"),
		Options{Providers: []core.ProviderName{core.ProviderCodex}},
		nil,
		deps,
	)
	if err != nil {
		t.Fatalf("DiscoverProviders() error = %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("DiscoverProviders() returned %d providers, want 1", len(got))
	}
	if !reflect.DeepEqual(lookedUpCommands, []string{"codex"}) {
		t.Fatalf("LookPath calls = %q, want [codex]", lookedUpCommands)
	}
	if !reflect.DeepEqual(lookedUpEnvironment, []string{"CODEX_HOME"}) {
		t.Fatalf("LookupEnv calls = %q, want [CODEX_HOME]", lookedUpEnvironment)
	}
	if inspectionCalls != 0 {
		t.Fatalf("OpenCommandPath calls = %d, want 0 before Task 2", inspectionCalls)
	}
}

func TestDiscoverProvidersWithEmptySelectionCallsNoDependencies(t *testing.T) {
	calls := 0
	deps := DiscoveryDependencies{
		LookupEnv: func(string) (string, bool) { calls++; return "", false },
		LookPath: func(string) (string, error) {
			calls++
			return "", exec.ErrNotFound
		},
		UserHomeDir: func() (string, error) { calls++; return "", errors.New("missing") },
		AbsPath:     func(string) (string, error) { calls++; return "", errors.New("missing") },
		OpenCommandPath: func(string, CommandReadMode, int64) (CommandFileInspection, error) {
			calls++
			return nil, errors.New("missing")
		},
	}

	got, err := DiscoverProviders(context.Background(), "", Options{}, nil, deps)
	if err != nil {
		t.Fatalf("DiscoverProviders() error = %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("DiscoverProviders() = %#v, want empty", got)
	}
	if calls != 0 {
		t.Fatalf("discovery dependency calls = %d, want 0", calls)
	}
}

func TestDiscoverProvidersMakesPATHCandidatesAbsolute(t *testing.T) {
	relative := filepath.Join("relative", "bin", "codex")
	absolute := testAbsolutePath("absolute", "bin", "codex")
	absCalls := []string{}
	deps := DiscoveryDependencies{
		LookPath: func(name string) (string, error) {
			if name != "codex" {
				t.Fatalf("LookPath(%q), want codex", name)
			}
			return relative, nil
		},
		AbsPath: func(path string) (string, error) {
			absCalls = append(absCalls, path)
			return absolute, nil
		},
	}

	got, err := DiscoverProviders(
		context.Background(),
		testAbsolutePath("configuration", "config.toml"),
		Options{Providers: []core.ProviderName{core.ProviderCodex}},
		nil,
		deps,
	)
	if err != nil {
		t.Fatalf("DiscoverProviders() error = %v", err)
	}
	if !reflect.DeepEqual(absCalls, []string{relative}) {
		t.Fatalf("AbsPath calls = %q, want [%q]", absCalls, relative)
	}
	want := []CommandCandidate{{
		Command: ProviderCommand{Executable: absolute},
		Source:  CandidatePATH,
	}}
	if !reflect.DeepEqual(got[core.ProviderCodex].Commands, want) {
		t.Fatalf("Commands = %#v, want %#v", got[core.ProviderCodex].Commands, want)
	}
}

func TestDiscoverProvidersReturnsFixedErrorsForUnsafePATHResults(t *testing.T) {
	secret := "unique-path-error-secret"
	tests := []struct {
		name     string
		lookPath func(string) (string, error)
		absPath  func(string) (string, error)
		wantErr  error
	}{
		{
			name: "missing command is not fatal",
			lookPath: func(string) (string, error) {
				return "", fmt.Errorf("missing %s: %w", secret, exec.ErrNotFound)
			},
			absPath: func(string) (string, error) {
				t.Fatal("AbsPath called for missing command")
				return "", nil
			},
		},
		{
			name:     "control in PATH result",
			lookPath: func(string) (string, error) { return "unsafe\npath", nil },
			absPath:  func(string) (string, error) { return testAbsolutePath("bin", "codex"), nil },
			wantErr:  ErrPlan,
		},
		{
			name:     "absolute conversion failure",
			lookPath: func(string) (string, error) { return "relative-codex", nil },
			absPath:  func(string) (string, error) { return "", errors.New("failure " + secret) },
			wantErr:  ErrPlan,
		},
		{
			name:     "relative converted result",
			lookPath: func(string) (string, error) { return "relative-codex", nil },
			absPath:  func(string) (string, error) { return "still-relative", nil },
			wantErr:  ErrPlan,
		},
		{
			name:     "control in converted result",
			lookPath: func(string) (string, error) { return "relative-codex", nil },
			absPath:  func(string) (string, error) { return testAbsolutePath("bin", "codex") + "\t", nil },
			wantErr:  ErrPlan,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			deps := DiscoveryDependencies{LookPath: test.lookPath, AbsPath: test.absPath}
			got, err := DiscoverProviders(
				context.Background(),
				testAbsolutePath("configuration", "config.toml"),
				Options{Providers: []core.ProviderName{core.ProviderCodex}},
				nil,
				deps,
			)
			formatted := fmt.Sprintf("%v %v", got, err)
			if strings.Contains(formatted, secret) {
				t.Fatal("DiscoverProviders() retained the planted dependency error value")
			}
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("DiscoverProviders() returned the wrong fixed error category; want %v", test.wantErr)
			}
		})
	}
}

func TestDiscoverProvidersMapsUnexpectedLookPathErrorToFixedErrPlan(t *testing.T) {
	planted := "planted-look-path-error-text"
	deps := DiscoveryDependencies{
		LookPath: func(string) (string, error) {
			return "", errors.New(planted)
		},
		AbsPath: func(string) (string, error) {
			t.Fatal("AbsPath called after unexpected LookPath failure")
			return "", nil
		},
	}

	got, err := DiscoverProviders(
		context.Background(),
		testAbsolutePath("configuration", "config.toml"),
		Options{Providers: []core.ProviderName{core.ProviderCodex}},
		nil,
		deps,
	)
	formatted := fmt.Sprintf("%v %v", got, err)
	if strings.Contains(formatted, planted) {
		t.Fatal("DiscoverProviders() retained the unexpected LookPath error text")
	}
	if !errors.Is(err, ErrPlan) {
		t.Fatal("DiscoverProviders() did not map unexpected LookPath failure to ErrPlan")
	}
	if got != nil {
		t.Fatalf("DiscoverProviders() = %#v, want nil on unexpected LookPath failure", got)
	}
}

func TestDiscoverProvidersHonorsCancellationBeforeAndBetweenProviders(t *testing.T) {
	t.Run("before discovery", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		deps := DiscoveryDependencies{
			LookupEnv: func(string) (string, bool) { calls++; return "", false },
			LookPath:  func(string) (string, error) { calls++; return "", nil },
		}
		got, err := DiscoverProviders(
			ctx,
			testAbsolutePath("configuration", "config.toml"),
			Options{Providers: []core.ProviderName{core.ProviderCodex}},
			nil,
			deps,
		)
		if err != context.Canceled {
			t.Fatalf("DiscoverProviders() error = %v, want exact context.Canceled", err)
		}
		if got != nil {
			t.Fatalf("DiscoverProviders() = %#v, want nil", got)
		}
		if calls != 0 {
			t.Fatalf("dependency calls = %d, want 0", calls)
		}
	})

	t.Run("between selected providers", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		lookedUp := []string{}
		deps := DiscoveryDependencies{
			LookPath: func(name string) (string, error) {
				lookedUp = append(lookedUp, name)
				return filepath.Join("relative", name), nil
			},
			AbsPath: func(path string) (string, error) {
				cancel()
				return testAbsolutePath("bin", filepath.Base(path)), nil
			},
		}
		got, err := DiscoverProviders(
			ctx,
			testAbsolutePath("configuration", "config.toml"),
			Options{Providers: []core.ProviderName{core.ProviderCodex, core.ProviderClaude}},
			nil,
			deps,
		)
		if err != context.Canceled {
			t.Fatalf("DiscoverProviders() error = %v, want exact context.Canceled", err)
		}
		if got != nil {
			t.Fatalf("DiscoverProviders() = %#v, want nil", got)
		}
		if !reflect.DeepEqual(lookedUp, []string{"codex"}) {
			t.Fatalf("LookPath calls = %q, want [codex]", lookedUp)
		}
	})
}

func TestDiscoverProvidersReturnsCancellationAfterCredentialLookup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	planted := "planted-canceled-credential-value"
	deps := DiscoveryDependencies{
		LookupEnv: func(name string) (string, bool) {
			switch name {
			case "CLAUDE_CONFIG_DIR":
				return "", false
			case "ANTHROPIC_API_KEY":
				cancel()
				return planted, true
			default:
				t.Fatalf("LookupEnv(%q) was not selected", name)
				return "", false
			}
		},
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
	}

	got, err := DiscoverProviders(
		ctx,
		testAbsolutePath("configuration", "config.toml"),
		Options{Providers: []core.ProviderName{core.ProviderClaude}},
		nil,
		deps,
	)
	formatted := fmt.Sprintf("%v %v", got, err)
	if strings.Contains(formatted, planted) {
		t.Fatal("DiscoverProviders() retained the credential value after cancellation")
	}
	if err != context.Canceled {
		t.Fatal("DiscoverProviders() did not return exact context.Canceled")
	}
	if got != nil {
		t.Fatalf("DiscoverProviders() = %#v, want nil after credential cancellation", got)
	}
}

func TestDiscoveryNeverRetainsCredentialValues(t *testing.T) {
	secrets := map[string]string{
		"ANTHROPIC_API_KEY":              "secret-anthropic-7f3b",
		"GEMINI_API_KEY":                 "secret-gemini-0a91",
		"GOOGLE_API_KEY":                 "secret-google-45ce",
		"GOOGLE_APPLICATION_CREDENTIALS": "secret-service-account-2d18",
		"GOOGLE_CLOUD_PROJECT":           "secret-project-96ab",
		"GOOGLE_CLOUD_LOCATION":          "secret-location-31ef",
	}
	environment := map[string]string{
		"CODEX_HOME":        testAbsolutePath("environment", "codex"),
		"CLAUDE_CONFIG_DIR": testAbsolutePath("environment", "claude"),
	}
	for name, value := range secrets {
		environment[name] = value
	}
	lookedUp := []string{}
	deps := DiscoveryDependencies{
		LookupEnv: func(name string) (string, bool) {
			lookedUp = append(lookedUp, name)
			value, ok := environment[name]
			return value, ok
		},
		LookPath: func(name string) (string, error) {
			return "", fmt.Errorf(
				"missing-command-with-%s: %w",
				secrets["GEMINI_API_KEY"],
				exec.ErrNotFound,
			)
		},
		UserHomeDir: func() (string, error) {
			return "", errors.New("missing-home-with-" + secrets["ANTHROPIC_API_KEY"])
		},
		AbsPath: func(string) (string, error) {
			t.Fatal("AbsPath called for missing command")
			return "", nil
		},
	}
	options := Options{Providers: []core.ProviderName{
		core.ProviderCodex,
		core.ProviderClaude,
		core.ProviderGemini,
	}}

	got, err := DiscoverProviders(
		context.Background(),
		testAbsolutePath("configuration", "config.toml"),
		options,
		nil,
		deps,
	)
	if err != nil {
		formattedError := err.Error()
		for name, secret := range secrets {
			if strings.Contains(formattedError, secret) {
				t.Fatalf("discovery error retained %s value", name)
			}
		}
		t.Fatal("DiscoverProviders() returned an unexpected fixed error")
	}
	var output bytes.Buffer
	if _, writeErr := fmt.Fprintf(&output, "%v %v", got, err); writeErr != nil {
		t.Fatalf("format discovery fixture: %v", writeErr)
	}
	formatted := output.String()
	for name, secret := range secrets {
		if strings.Contains(formatted, secret) {
			t.Errorf("formatted discovery retained %s value", name)
		}
	}

	wantLookups := []string{
		"CODEX_HOME",
		"CLAUDE_CONFIG_DIR",
		"ANTHROPIC_API_KEY",
		"GEMINI_API_KEY",
		"GOOGLE_API_KEY",
		"GOOGLE_APPLICATION_CREDENTIALS",
		"GOOGLE_CLOUD_PROJECT",
		"GOOGLE_CLOUD_LOCATION",
	}
	if !reflect.DeepEqual(lookedUp, wantLookups) {
		t.Fatalf("LookupEnv calls = %q, want %q", lookedUp, wantLookups)
	}
	assertAuthChoices(t, got[core.ProviderClaude].AuthChoices, []AuthID{
		AuthAnthropicAPIKey,
		AuthConfigHome,
	})
	assertAuthChoices(t, got[core.ProviderGemini].AuthChoices, []AuthID{
		AuthGeminiAPIKey,
		AuthGoogleAPIKey,
		AuthVertexServiceAccount,
	})
}

func discoveryDependenciesWithoutPaths(environment map[string]string) DiscoveryDependencies {
	return DiscoveryDependencies{
		LookupEnv: lookupDiscoveryEnvironment(environment),
		LookPath:  func(string) (string, error) { return "", exec.ErrNotFound },
		UserHomeDir: func() (string, error) {
			return "", errors.New("home unavailable")
		},
		AbsPath: func(string) (string, error) {
			return "", errors.New("unexpected absolute path call")
		},
	}
}

func lookupDiscoveryEnvironment(values map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		value, ok := values[name]
		return value, ok
	}
}

func assertAuthChoices(t *testing.T, got, want []AuthID) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("AuthChoices = %#v, want %#v", got, want)
	}
}
