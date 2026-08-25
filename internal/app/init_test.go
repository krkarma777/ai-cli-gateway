package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/configstore"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/doctor"
	"github.com/krkarma777/ai-cli-gateway/internal/initconfig"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

func TestProductionInitDependenciesAreCompleteAndLazy(t *testing.T) {
	t.Parallel()

	deps := ProductionInitDependencies(panicWriter{})
	if deps.Store == nil || deps.Entropy == nil || deps.DefaultInitRuntimeRoot == nil ||
		deps.diagnose == nil || deps.Runtime.Listen == nil {
		t.Fatal("ProductionInitDependencies() returned an incomplete dependency graph")
	}
}

func TestInitDryRunWritesCompleteDiffBeforeSummaryAndTouchesNoMutation(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := freshInitFixture(t)
	store := &recordingInitStore{
		snapshot:  snapshot,
		preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
		commitFn: func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error) {
			panic("dry run called Commit")
		},
	}
	var output bytes.Buffer
	result := Init(context.Background(), options, &output, InitDependencies{
		Store:   store,
		Entropy: panicInitReader{},
		DefaultInitRuntimeRoot: func() (string, error) {
			return runtimeRoot, nil
		},
	})

	if result != (InitResult{Outcome: InitDryRun}) {
		t.Fatalf("Init() = %#v, want dry run", result)
	}
	if got := store.calls; !slices.Equal(got, []string{"load", "preflight"}) {
		t.Fatalf("store calls = %q, want load/preflight", got)
	}
	if got := output.String(); !strings.HasPrefix(got, "+ gateway-auth gateway\n") ||
		!strings.HasSuffix(got, "dry_run: no files changed; post-write doctor was not run\n") {
		t.Fatalf("Init() output = %q", got)
	}
	if store.mutation.Base.Path() != snapshot.Path() ||
		store.mutation.Key.Intent != configstore.KeyIntentEnsure ||
		store.mutation.Key.Path != filepath.Join(filepath.Dir(snapshot.Path()), "gateway.key") ||
		!slices.Contains(store.mutation.PrivateDirs, options.Provider[core.ProviderCodex].ConfigHome.Value) ||
		!slices.Contains(store.mutation.Key.DistinctFrom, snapshot.Path()) ||
		!slices.Contains(store.mutation.Key.DistinctFrom, options.Provider[core.ProviderCodex].Executable.Value) {
		t.Fatalf("mutation = %#v", store.mutation)
	}
}

func TestInitRequiresCompleteDiffWriteBeforeEntropyOrCommit(t *testing.T) {
	t.Parallel()

	for _, dryRun := range []bool{false, true} {
		dryRun := dryRun
		t.Run(map[bool]string{false: "real", true: "dry-run"}[dryRun], func(t *testing.T) {
			t.Parallel()
			snapshot, options, runtimeRoot := freshInitFixture(t)
			options.DryRun = dryRun
			store := &recordingInitStore{
				snapshot:  snapshot,
				preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
				commitFn: func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error) {
					panic("failed output called Commit")
				},
			}

			result := Init(context.Background(), options, &appErrorWriter{}, InitDependencies{
				Store:   store,
				Entropy: panicInitReader{},
				DefaultInitRuntimeRoot: func() (string, error) {
					return runtimeRoot, nil
				},
			})

			if result != (InitResult{Outcome: InitFailed}) {
				t.Fatalf("Init() = %#v, want failed", result)
			}
			if got := store.calls; !slices.Equal(got, []string{"load", "preflight"}) {
				t.Fatalf("store calls = %q, want load/preflight", got)
			}
		})
	}
}

func TestInitKeyFailureAndCommitStateMatrix(t *testing.T) {
	t.Parallel()

	planted := errors.New("PLANTED_INIT_SECRET")
	tests := []struct {
		name       string
		preflight  configstore.PreflightResult
		preErr     error
		entropy    io.Reader
		commit     configstore.CommitResult
		commitErr  error
		want       InitResult
		wantOutput string
		wantCommit bool
	}{
		{
			name: "preflight failure", preErr: planted, entropy: panicInitReader{},
			want: InitResult{Outcome: InitFailed}, wantOutput: "setup_failed\n",
		},
		{
			name:      "orphan needs confirmation",
			preflight: configstore.PreflightResult{KeyState: configstore.KeyStateNeedsConfirmation},
			entropy:   panicInitReader{}, want: InitResult{Outcome: InitUsage},
			wantOutput: "key_reuse_confirmation_required\n",
		},
		{
			name:      "entropy failure",
			preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
			entropy:   appErrorReader{}, want: InitResult{Outcome: InitFailed},
			wantOutput: "setup_failed\n",
		},
		{
			name:      "not committed",
			preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
			entropy:   bytes.NewReader(make([]byte, 32)),
			commit:    configstore.CommitResult{State: configstore.CommitNotCommitted},
			commitErr: planted, want: InitResult{Outcome: InitFailed},
			wantOutput: "setup_failed\n", wantCommit: true,
		},
		{
			name:      "rolled back",
			preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
			entropy:   bytes.NewReader(make([]byte, 32)),
			commit:    configstore.CommitResult{State: configstore.CommitRolledBack},
			commitErr: planted, want: InitResult{Outcome: InitFailed},
			wantOutput: "setup_failed\n", wantCommit: true,
		},
		{
			name:       "recovery required wins",
			preflight:  configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
			entropy:    bytes.NewReader(make([]byte, 32)),
			commit:     configstore.CommitResult{State: configstore.CommitRecoveryRequired},
			commitErr:  context.Canceled,
			want:       InitResult{Outcome: InitRecoveryRequired},
			wantOutput: "backup_recovery_required\n", wantCommit: true,
		},
		{
			name:      "indeterminate",
			preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
			entropy:   bytes.NewReader(make([]byte, 32)),
			commit:    configstore.CommitResult{State: configstore.CommitIndeterminate},
			commitErr: planted, want: InitResult{Outcome: InitFailed},
			wantOutput: "setup_state_unknown\n", wantCommit: true,
		},
		{
			name:      "committed with cleanup failure",
			preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
			entropy:   bytes.NewReader(make([]byte, 32)),
			commit:    configstore.CommitResult{State: configstore.CommitCommitted, ConfigChanged: true, KeyCreated: true},
			commitErr: planted, want: InitResult{Outcome: InitFailed, Saved: true},
			wantOutput: "setup_saved_with_cleanup_failure\n", wantCommit: true,
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot, options, runtimeRoot := freshInitFixture(t)
			options.DryRun = false
			store := &recordingInitStore{
				snapshot: snapshot, preflight: test.preflight, preflightErr: test.preErr,
				commitResult: test.commit, commitErr: test.commitErr,
			}
			var output bytes.Buffer
			result := Init(context.Background(), options, &output, InitDependencies{
				Store: store, Entropy: test.entropy,
				DefaultInitRuntimeRoot: func() (string, error) { return runtimeRoot, nil },
			})

			if result != test.want {
				t.Fatalf("Init() = %#v, want %#v", result, test.want)
			}
			if strings.Contains(output.String(), "PLANTED") ||
				!strings.HasSuffix(output.String(), test.wantOutput) {
				t.Fatalf("Init() output = %q, want suffix %q", output.String(), test.wantOutput)
			}
			if got := slices.Contains(store.calls, "commit"); got != test.wantCommit {
				t.Fatalf("Commit called = %t, want %t; calls=%q", got, test.wantCommit, store.calls)
			}
			if test.wantCommit && !allZero(store.payloadAfterReturn) {
				t.Fatalf("key payload was not zeroed after Commit: %x", store.payloadAfterReturn)
			}
		})
	}
}

func TestInitCancellationBeforeAndAfterCommit(t *testing.T) {
	t.Parallel()

	t.Run("before commit", func(t *testing.T) {
		t.Parallel()
		snapshot, options, runtimeRoot := freshInitFixture(t)
		options.DryRun = false
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		store := &recordingInitStore{snapshot: snapshot}
		var output bytes.Buffer

		result := Init(ctx, options, &output, InitDependencies{
			Store: store, Entropy: panicInitReader{},
			DefaultInitRuntimeRoot: func() (string, error) { return runtimeRoot, nil },
		})

		if result != (InitResult{Outcome: InitCanceled}) || output.String() != "setup_canceled\n" {
			t.Fatalf("Init() = %#v output %q", result, output.String())
		}
	})

	t.Run("after committed", func(t *testing.T) {
		t.Parallel()
		snapshot, options, runtimeRoot := freshInitFixture(t)
		options.DryRun = false
		ctx, cancel := context.WithCancel(context.Background())
		store := &recordingInitStore{
			snapshot:  snapshot,
			preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
			commitFn: func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error) {
				cancel()
				return configstore.CommitResult{State: configstore.CommitCommitted, ConfigChanged: true}, context.Canceled
			},
		}
		var output bytes.Buffer

		result := Init(ctx, options, &output, InitDependencies{
			Store: store, Entropy: bytes.NewReader(make([]byte, 32)),
			DefaultInitRuntimeRoot: func() (string, error) { return runtimeRoot, nil },
		})

		if result != (InitResult{Outcome: InitCanceled, Saved: true}) ||
			!strings.HasSuffix(output.String(), "setup_saved_before_cancellation\n") {
			t.Fatalf("Init() = %#v output %q", result, output.String())
		}
	})

	t.Run("after diff before entropy", func(t *testing.T) {
		t.Parallel()
		snapshot, options, runtimeRoot := freshInitFixture(t)
		options.DryRun = false
		ctx, cancel := context.WithCancel(context.Background())
		store := &recordingInitStore{
			snapshot:  snapshot,
			preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
			commitFn: func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error) {
				panic("post-diff cancellation called Commit")
			},
		}
		writer := &cancelAfterInitWrite{cancel: cancel}

		result := Init(ctx, options, writer, InitDependencies{
			Store: store, Entropy: panicInitReader{},
			DefaultInitRuntimeRoot: func() (string, error) { return runtimeRoot, nil },
		})

		if result != (InitResult{Outcome: InitCanceled}) ||
			!strings.HasSuffix(writer.output.String(), "setup_canceled\n") ||
			slices.Contains(store.calls, "commit") {
			t.Fatalf("Init() = %#v output %q calls %q", result, writer.output.String(), store.calls)
		}
	})
}

func TestInitMapsLoadPlanningAndPathFailuresWithoutLeakingErrors(t *testing.T) {
	t.Parallel()

	planted := errors.New("PLANTED_INIT_BOUNDARY_SECRET")
	tests := []struct {
		name      string
		loadErr   error
		mutate    func(*initconfig.Options)
		defaultFn func() (string, error)
		want      InitResult
		line      string
	}{
		{
			name: "invalid existing config", loadErr: configstore.ErrInvalidConfig,
			want: InitResult{Outcome: InitUsage}, line: "configuration_invalid\n",
		},
		{
			name: "unsafe store path", loadErr: configstore.ErrUnsafePath,
			want: InitResult{Outcome: InitFailed}, line: "setup_failed\n",
		},
		{
			name: "incomplete input after load",
			mutate: func(options *initconfig.Options) {
				input := options.Provider[core.ProviderCodex]
				input.Executable = initconfig.StringValue{}
				options.Provider[core.ProviderCodex] = input
			},
			want: InitResult{Outcome: InitUsage}, line: "init_input_invalid\n",
		},
		{
			name: "default runtime failure",
			defaultFn: func() (string, error) {
				return "", planted
			},
			want: InitResult{Outcome: InitFailed}, line: "setup_failed\n",
		},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot, options, runtimeRoot := freshInitFixture(t)
			if test.mutate != nil {
				test.mutate(&options)
			}
			defaultFn := test.defaultFn
			if defaultFn == nil {
				defaultFn = func() (string, error) { return runtimeRoot, nil }
			}
			store := &recordingInitStore{snapshot: snapshot, loadErr: test.loadErr}
			var output bytes.Buffer

			result := Init(context.Background(), options, &output, InitDependencies{
				Store: store, Entropy: panicInitReader{}, DefaultInitRuntimeRoot: defaultFn,
			})

			if result != test.want || output.String() != test.line ||
				strings.Contains(output.String(), "PLANTED") {
				t.Fatalf("Init() = %#v output %q, want %#v/%q", result, output.String(), test.want, test.line)
			}
			if slices.Contains(store.calls, "preflight") || slices.Contains(store.calls, "commit") {
				t.Fatalf("unexpected storage calls: %q", store.calls)
			}
		})
	}
}

func TestInitRejectsUnauthorizedReplacementAfterLoadingWithoutPreflight(t *testing.T) {
	t.Parallel()

	snapshot, options, _ := existingInitFixture(t)
	input := options.Provider[core.ProviderCodex]
	input.Executable.Value = filepath.Join(filepath.Dir(snapshot.Path()), "replacement-codex")
	options.Provider[core.ProviderCodex] = input
	store := &recordingInitStore{snapshot: snapshot}
	var output bytes.Buffer

	result := Init(context.Background(), options, &output, InitDependencies{
		Store: store, Entropy: panicInitReader{},
		DefaultInitRuntimeRoot: func() (string, error) {
			panic("existing config requested a default runtime root")
		},
	})

	if result != (InitResult{Outcome: InitUsage}) ||
		!strings.Contains(output.String(), "~ provider codex\n") ||
		!strings.HasSuffix(output.String(), "replacement_not_authorized\n") ||
		!slices.Equal(store.calls, []string{"load"}) {
		t.Fatalf("Init() = %#v output %q calls %q", result, output.String(), store.calls)
	}
}

func TestInitExplicitKeyReuseAndMissingCreationMatrix(t *testing.T) {
	t.Parallel()

	for _, state := range []configstore.KeyState{
		configstore.KeyStateReusable,
		configstore.KeyStateMissing,
	} {
		state := state
		name := map[configstore.KeyState]string{
			configstore.KeyStateReusable: "reuse",
			configstore.KeyStateMissing:  "create",
		}[state]
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			snapshot, options, runtimeRoot := freshInitFixture(t)
			options.DryRun = false
			explicitKey := filepath.Join(filepath.Dir(snapshot.Path()), "explicit.key")
			options.Gateway = initconfig.GatewayInput{
				Auth: initconfig.GatewayAuthFile, AuthSet: true,
				KeyFile: initconfig.StringValue{Set: true, Value: explicitKey},
			}
			store := &recordingInitStore{
				snapshot:  snapshot,
				preflight: configstore.PreflightResult{KeyState: state},
				commitFn: func(_ context.Context, mutation configstore.Mutation, payload []byte) (configstore.CommitResult, error) {
					if mutation.Key.Path != explicitKey || !mutation.Key.AllowExisting ||
						mutation.Key.Intent != configstore.KeyIntentEnsure {
						t.Fatalf("key plan = %#v", mutation.Key)
					}
					wantPayload := 0
					if state == configstore.KeyStateMissing {
						wantPayload = 65
					}
					if len(payload) != wantPayload {
						t.Fatalf("payload length = %d, want %d", len(payload), wantPayload)
					}
					return configstore.CommitResult{State: configstore.CommitCommitted}, errors.New("PLANTED_CLEANUP_SECRET")
				},
			}
			entropy := io.Reader(panicInitReader{})
			if state == configstore.KeyStateMissing {
				entropy = bytes.NewReader(bytes.Repeat([]byte{0x5a}, 32))
			}
			var output bytes.Buffer

			result := Init(context.Background(), options, &output, InitDependencies{
				Store: store, Entropy: entropy,
				DefaultInitRuntimeRoot: func() (string, error) { return runtimeRoot, nil },
			})

			if result != (InitResult{Outcome: InitFailed, Saved: true}) ||
				!strings.HasSuffix(output.String(), "setup_saved_with_cleanup_failure\n") ||
				strings.Contains(output.String(), "5a5a") {
				t.Fatalf("Init() = %#v output %q", result, output.String())
			}
		})
	}
}

func TestInitUnchangedConfiguredMissingKeyDoesNotGenerateOrCommit(t *testing.T) {
	t.Parallel()

	snapshot, options, _ := existingInitFixture(t)
	store := &recordingInitStore{
		snapshot:  snapshot,
		preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
		commitFn: func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error) {
			panic("unchanged configured missing key called Commit")
		},
	}
	var output bytes.Buffer
	result := Init(context.Background(), options, &output, InitDependencies{
		Runtime: Dependencies{}, Store: store, Entropy: panicInitReader{},
		diagnose: func(context.Context, string, Dependencies) (doctor.Diagnosis, error) {
			return doctor.Diagnosis{}, errors.New("PLANTED_DOCTOR_SECRET")
		},
	})

	if result != (InitResult{Outcome: InitFailed}) ||
		!strings.HasSuffix(output.String(), "post_write_doctor_failed\n") ||
		!slices.Equal(store.calls, []string{"load", "preflight"}) {
		t.Fatalf("Init() = %#v output %q calls %q", result, output.String(), store.calls)
	}
}

func TestInitPostWriteDoctorReadinessAndCleanupMatrix(t *testing.T) {
	t.Run("ready", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		result, output := runNoopFixtureInit(t, fixture, nil)
		if result != (InitResult{Outcome: InitReady}) ||
			!strings.Contains(output, "core:\n") || !strings.Contains(output, "already_current:") ||
			strings.Contains(output, "Authorization:") ||
			strings.Contains(output, "AI_CLI_GATEWAY_API_KEY") ||
			!strings.Contains(output, "request_posix: curl --fail-with-body 'http://127.0.0.1:18080/v1/models'") ||
			!strings.HasSuffix(output, "setup_ready\n") || fixture.closeCalls != 1 ||
			fixture.listenCalls != 0 {
			t.Fatalf("Init() = %#v output %q close/listen %d/%d", result, output, fixture.closeCalls, fixture.listenCalls)
		}
	})

	t.Run("environment gateway authentication", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		addFixtureGatewayKey(t, fixture.configPath, "CUSTOM_GATEWAY_KEY")
		fixture.deps.LookupEnv = func(name string) (string, bool) {
			if name != "CUSTOM_GATEWAY_KEY" {
				t.Fatalf("LookupEnv(%q), want CUSTOM_GATEWAY_KEY", name)
			}
			return "test-gateway-key", true
		}
		result, output := runNoopFixtureInit(t, fixture, nil)
		if result != (InitResult{Outcome: InitReady}) ||
			!strings.Contains(output, "client_key_posix: export AI_CLI_GATEWAY_API_KEY=\"${CUSTOM_GATEWAY_KEY:?not set}\"") ||
			!strings.Contains(output, "GetEnvironmentVariable('CUSTOM_GATEWAY_KEY')") ||
			!strings.Contains(output, "Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}") ||
			strings.Contains(output, "test-gateway-key") ||
			!strings.HasSuffix(output, "setup_ready\n") {
			t.Fatalf("Init() = %#v output %q", result, output)
		}
	})

	t.Run("selected not ready", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		adapter := fixture.deps.Adapters[core.ProviderCodex].(*appTestAdapter)
		adapter.health.Status = provider.HealthNotReady
		adapter.health.Auth = "missing"
		adapter.health.Problems = []string{provider.ProblemAuthMissing}
		result, output := runNoopFixtureInit(t, fixture, nil)
		if result != (InitResult{Outcome: InitNotReady}) ||
			!strings.HasSuffix(output, "setup_not_ready\n") || fixture.closeCalls != 1 {
			t.Fatalf("Init() = %#v output %q close %d", result, output, fixture.closeCalls)
		}
	})

	t.Run("unselected not ready is visible but ignored", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		addNotReadyClaudeProvider(t, fixture)
		result, output := runNoopFixtureInit(t, fixture, nil)
		if result != (InitResult{Outcome: InitReady}) ||
			!strings.Contains(output, "claude\tnot_ready") ||
			!strings.HasSuffix(output, "setup_ready\n") || fixture.closeCalls != 1 {
			t.Fatalf("Init() = %#v output %q close %d", result, output, fixture.closeCalls)
		}
	})

	t.Run("doctor failure", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		fixture.deps.GatewayExecutable = func() (string, error) {
			return "", errors.New("PLANTED_DOCTOR_SECRET")
		}
		result, output := runNoopFixtureInit(t, fixture, nil)
		if result != (InitResult{Outcome: InitFailed}) ||
			!strings.HasSuffix(output, "post_write_doctor_failed\n") ||
			strings.Contains(output, "PLANTED") {
			t.Fatalf("Init() = %#v output %q", result, output)
		}
	})

	t.Run("root close failure after complete report", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		originalClose := fixture.deps.CloseRoot
		fixture.deps.CloseRoot = func(root *process.Root) error {
			if err := originalClose(root); err != nil {
				return err
			}
			return errors.New("PLANTED_ROOT_CLOSE_SECRET")
		}
		result, output := runNoopFixtureInit(t, fixture, nil)
		if result != (InitResult{Outcome: InitFailed}) ||
			!strings.Contains(output, "core:\n") ||
			!strings.HasSuffix(output, "post_write_doctor_failed\n") ||
			strings.Contains(output, "PLANTED") {
			t.Fatalf("Init() = %#v output %q", result, output)
		}
	})

	t.Run("report write failure still closes root", func(t *testing.T) {
		fixture := newReadyAppFixture(t)
		writer := &initFailOnCallWriter{fail: 2}
		result, _ := runNoopFixtureInit(t, fixture, writer)
		if result != (InitResult{Outcome: InitFailed}) || writer.calls != 2 || fixture.closeCalls != 1 {
			t.Fatalf("Init() = %#v writes/close %d/%d", result, writer.calls, fixture.closeCalls)
		}
	})
}

func TestInitDryRunAllProvidersMultipleModelsFreezesOnlyVertexCredentialPath(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	// The storage policy requires the test-owned transaction directory to be private.
	//nolint:gosec
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("Chmod fixture: %v", err)
	}
	existingHome := filepath.Join(root, "codex-home")
	// This exact path is below the private test-owned fixture.
	//nolint:gosec
	if err := os.Mkdir(existingHome, 0o700); err != nil {
		t.Fatalf("Mkdir existing home: %v", err)
	}
	configPath := filepath.Join(root, "config.toml")
	snapshot, err := configstore.NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load snapshot: %v", err)
	}
	options := allProviderInitOptions(root, configPath, existingHome)
	credentialPath := filepath.Join(root, "service-account.json")
	lookupCalls := 0
	runtimeDeps := Dependencies{
		LookupEnv: func(name string) (string, bool) {
			lookupCalls++
			if name != "GOOGLE_APPLICATION_CREDENTIALS" {
				t.Fatalf("Init looked up non-path credential environment %q", name)
			}
			return credentialPath, true
		},
	}
	store := &recordingInitStore{
		snapshot:  snapshot,
		preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
	}
	var output bytes.Buffer
	result := Init(context.Background(), options, &output, InitDependencies{
		Runtime: runtimeDeps, Store: store, Entropy: panicInitReader{},
		DefaultInitRuntimeRoot: func() (string, error) {
			return filepath.Join(root, "runtime"), nil
		},
	})

	if result != (InitResult{Outcome: InitDryRun}) || lookupCalls != 1 {
		t.Fatalf("Init() = %#v lookup calls %d", result, lookupCalls)
	}
	for _, fragment := range []string{
		"provider codex", "provider claude", "provider gemini",
		"model codex-fast", "model codex-deep", "model claude-local", "model gemini-local",
	} {
		if !strings.Contains(output.String(), fragment) {
			t.Fatalf("Init output lacks %q: %q", fragment, output.String())
		}
	}
	wantPrivate := []string{
		options.Provider[core.ProviderClaude].ConfigHome.Value,
		options.Provider[core.ProviderGemini].ConfigHome.Value,
	}
	if !slices.Equal(store.mutation.PrivateDirs, wantPrivate) {
		t.Fatalf("PrivateDirs = %q, want %q", store.mutation.PrivateDirs, wantPrivate)
	}
	wantDistinct := []string{
		configPath,
		options.Provider[core.ProviderClaude].Executable.Value,
		options.Provider[core.ProviderCodex].Executable.Value,
		options.Provider[core.ProviderGemini].Executable.Value,
		credentialPath,
	}
	if !slices.Equal(store.mutation.Key.DistinctFrom, wantDistinct) {
		t.Fatalf("DistinctFrom = %q, want %q", store.mutation.Key.DistinctFrom, wantDistinct)
	}
}

func TestBuildInitMutationRetainsFrozenVertexCredentialForDoctor(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := freshInitFixture(t)
	options = allProviderInitOptions(filepath.Dir(snapshot.Path()), snapshot.Path(), options.Provider[core.ProviderCodex].ConfigHome.Value)
	planning, err := initconfig.PlanNonInteractive(
		options,
		initconfig.Source{},
		runtimeRoot,
		filepath.Join(filepath.Dir(snapshot.Path()), "gateway.key"),
	)
	if err != nil {
		t.Fatalf("PlanNonInteractive: %v", err)
	}
	credentialPath := filepath.Join(filepath.Dir(snapshot.Path()), "first-service-account.json")
	current := credentialPath
	lookupCalls := 0
	runtimeDeps := Dependencies{LookupEnv: func(name string) (string, bool) {
		lookupCalls++
		return current + "-" + name, true
	}}
	credentialPath += "-GOOGLE_APPLICATION_CREDENTIALS" //nolint:gosec // Public environment name in a test path.

	mutation, frozen, err := buildInitMutation(snapshot, planning, runtimeDeps)
	if err != nil {
		t.Fatalf("buildInitMutation() error = %v", err)
	}
	current = filepath.Join(filepath.Dir(snapshot.Path()), "changed")
	got, present := frozen.LookupEnv("GOOGLE_APPLICATION_CREDENTIALS")
	if !present || got != credentialPath || lookupCalls != 1 ||
		!slices.Contains(mutation.Key.DistinctFrom, credentialPath) {
		t.Fatalf("frozen credential = %q/%t calls %d distinct %q", got, present, lookupCalls, mutation.Key.DistinctFrom)
	}
	other, present := frozen.LookupEnv("GOOGLE_CLOUD_PROJECT")
	if !present || other != current+"-GOOGLE_CLOUD_PROJECT" || lookupCalls != 2 {
		t.Fatalf("delegated lookup = %q/%t calls %d", other, present, lookupCalls)
	}
}

func TestBuildInitMutationPlansMissingRuntimeAndExplicitKeyParents(t *testing.T) {
	t.Parallel()

	snapshot, options, _ := freshInitFixture(t)
	root := filepath.Dir(snapshot.Path())
	runtimeRoot := filepath.Join(root, "state", "gateway", "runtime")
	keyPath := filepath.Join(root, "keys", "gateway.key")
	options.Gateway = initconfig.GatewayInput{
		Auth: initconfig.GatewayAuthFile, AuthSet: true,
		KeyFile: initconfig.StringValue{Set: true, Value: keyPath},
	}
	planning, err := initconfig.PlanNonInteractive(
		options, initconfig.Source{}, runtimeRoot,
		filepath.Join(root, "unused-default.key"),
	)
	if err != nil {
		t.Fatalf("PlanNonInteractive: %v", err)
	}
	mutation, _, err := buildInitMutation(snapshot, planning, Dependencies{})
	if err != nil {
		t.Fatalf("buildInitMutation: %v", err)
	}
	for _, path := range []string{filepath.Dir(runtimeRoot), filepath.Dir(keyPath)} {
		if !slices.Contains(mutation.PrivateDirs, path) {
			t.Fatalf("PrivateDirs = %q, want %q", mutation.PrivateDirs, path)
		}
	}
}

type recordingInitStore struct {
	snapshot           configstore.Snapshot
	loadErr            error
	preflight          configstore.PreflightResult
	preflightErr       error
	commitResult       configstore.CommitResult
	commitErr          error
	commitFn           func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error)
	calls              []string
	mutation           configstore.Mutation
	payloadAfterReturn []byte
}

func (store *recordingInitStore) Load(context.Context, string) (configstore.Snapshot, error) {
	store.calls = append(store.calls, "load")
	return store.snapshot, store.loadErr
}

func (store *recordingInitStore) Preflight(
	_ context.Context,
	mutation configstore.Mutation,
) (configstore.PreflightResult, error) {
	store.calls = append(store.calls, "preflight")
	store.mutation = mutation
	return store.preflight, store.preflightErr
}

func (store *recordingInitStore) Commit(
	ctx context.Context,
	mutation configstore.Mutation,
	payload []byte,
) (configstore.CommitResult, error) {
	store.calls = append(store.calls, "commit")
	store.mutation = mutation
	store.payloadAfterReturn = payload
	if store.commitFn != nil {
		return store.commitFn(ctx, mutation, payload)
	}
	return store.commitResult, store.commitErr
}

func freshInitFixture(
	t *testing.T,
) (configstore.Snapshot, initconfig.Options, string) {
	t.Helper()
	root := t.TempDir()
	// The storage policy requires the test-owned transaction directory to be private.
	//nolint:gosec
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatalf("Chmod fixture: %v", err)
	}
	configPath := filepath.Join(root, "config.toml")
	snapshot, err := configstore.NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load fresh snapshot: %v", err)
	}
	options := initconfig.Options{
		ConfigPath:     configPath,
		NonInteractive: true,
		DryRun:         true,
		Providers:      []core.ProviderName{core.ProviderCodex},
		Provider: map[core.ProviderName]initconfig.ProviderInput{
			core.ProviderCodex: {
				Executable: initconfig.StringValue{Set: true, Value: filepath.Join(root, "bin", "codex")},
				ConfigHome: initconfig.StringValue{Set: true, Value: filepath.Join(root, "codex-home")},
			},
		},
		Models: []initconfig.ModelMapping{{
			ID: "codex-local", Provider: core.ProviderCodex, ProviderModel: "gpt-test",
		}},
	}
	return snapshot, options, filepath.Join(root, "runtime")
}

func existingInitFixture(
	t *testing.T,
) (configstore.Snapshot, initconfig.Options, string) {
	t.Helper()
	fresh, options, runtimeRoot := freshInitFixture(t)
	planning, err := initconfig.PlanNonInteractive(
		options,
		initconfig.Source{},
		runtimeRoot,
		filepath.Join(filepath.Dir(fresh.Path()), "gateway.key"),
	)
	if err != nil {
		t.Fatalf("PlanNonInteractive fixture: %v", err)
	}
	// fresh.Path is an exact path below the test-owned private fixture.
	//nolint:gosec
	if err := os.WriteFile(fresh.Path(), planning.Merge.Candidate, 0o600); err != nil {
		t.Fatalf("WriteFile existing fixture: %v", err)
	}
	snapshot, err := configstore.NewWriter().Load(context.Background(), fresh.Path())
	if err != nil {
		t.Fatalf("Load existing fixture: %v", err)
	}
	options.DryRun = false
	return snapshot, options, runtimeRoot
}

func allProviderInitOptions(root, configPath, codexHome string) initconfig.Options {
	return initconfig.Options{
		ConfigPath: configPath, NonInteractive: true, DryRun: true,
		Providers: []core.ProviderName{
			core.ProviderCodex,
			core.ProviderClaude,
			core.ProviderGemini,
		},
		Provider: map[core.ProviderName]initconfig.ProviderInput{
			core.ProviderCodex: {
				Executable: initconfig.StringValue{Set: true, Value: filepath.Join(root, "bin", "codex")},
				ConfigHome: initconfig.StringValue{Set: true, Value: codexHome},
			},
			core.ProviderClaude: {
				Executable: initconfig.StringValue{Set: true, Value: filepath.Join(root, "bin", "claude")},
				ConfigHome: initconfig.StringValue{Set: true, Value: filepath.Join(root, "claude-home")},
				Auth:       initconfig.AuthAnthropicAPIKey,
				AuthSet:    true,
			},
			core.ProviderGemini: {
				Executable: initconfig.StringValue{Set: true, Value: filepath.Join(root, "bin", "gemini")},
				ConfigHome: initconfig.StringValue{Set: true, Value: filepath.Join(root, "gemini-home")},
				Auth:       initconfig.AuthVertexServiceAccount,
				AuthSet:    true,
			},
		},
		Models: []initconfig.ModelMapping{
			{ID: "codex-fast", Provider: core.ProviderCodex, ProviderModel: "gpt-fast"},
			{ID: "codex-deep", Provider: core.ProviderCodex, ProviderModel: "gpt-deep"},
			{ID: "claude-local", Provider: core.ProviderClaude, ProviderModel: "sonnet"},
			{ID: "gemini-local", Provider: core.ProviderGemini, ProviderModel: "gemini-test"},
		},
	}
}

func runNoopFixtureInit(
	t *testing.T,
	fixture *readyAppFixture,
	provided io.Writer,
) (InitResult, string) {
	t.Helper()
	var output bytes.Buffer
	writer := io.Writer(&output)
	if provided != nil {
		writer = provided
	}
	deps := ProductionInitDependencies(io.Discard)
	deps.Runtime = fixture.deps
	deps.Entropy = panicInitReader{}
	deps.DefaultInitRuntimeRoot = func() (string, error) {
		panic("existing config requested a default runtime root")
	}
	result := Init(context.Background(), initconfig.Options{
		ConfigPath:       fixture.configPath,
		NonInteractive:   true,
		Providers:        []core.ProviderName{core.ProviderCodex},
		Provider:         map[core.ProviderName]initconfig.ProviderInput{},
		Models:           nil,
		Gateway:          initconfig.GatewayInput{},
		ReplaceProviders: nil,
		ReplaceModels:    nil,
	}, writer, deps)
	return result, output.String()
}

type initFailOnCallWriter struct {
	buffer bytes.Buffer
	calls  int
	fail   int
}

func (writer *initFailOnCallWriter) Write(payload []byte) (int, error) {
	writer.calls++
	if writer.calls == writer.fail {
		return 0, errors.New("PLANTED_OUTPUT_SECRET")
	}
	return writer.buffer.Write(payload)
}

type panicInitReader struct{}

func (panicInitReader) Read([]byte) (int, error) { panic("unexpected entropy read") }

type appErrorReader struct{}

func (appErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("PLANTED_ENTROPY_SECRET")
}

type cancelAfterInitWrite struct {
	output bytes.Buffer
	cancel context.CancelFunc
	calls  int
}

func (writer *cancelAfterInitWrite) Write(payload []byte) (int, error) {
	writer.calls++
	written, err := writer.output.Write(payload)
	if writer.calls == 1 {
		writer.cancel()
	}
	return written, err
}

func allZero(value []byte) bool {
	for _, octet := range value {
		if octet != 0 {
			return false
		}
	}
	return true
}
