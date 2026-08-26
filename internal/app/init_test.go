package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/configstore"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/doctor"
	"github.com/krkarma777/ai-cli-gateway/internal/initconfig"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

func TestProductionInitDependenciesAreCompleteAndLazy(t *testing.T) {
	t.Parallel()

	deps := ProductionInitDependencies(panicWriter{})
	if deps.Store == nil || deps.Entropy == nil || deps.DefaultInitRuntimeRoot == nil ||
		deps.diagnose == nil || deps.Runtime.Listen == nil || deps.Discover == nil ||
		deps.IsTerminal == nil || deps.NewPrompt == nil || deps.LookupEnv == nil {
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
	result := Init(context.Background(), options, nonInteractiveInitStreams(&output), InitDependencies{
		Store: store, Entropy: panicInitReader{},
		Discover: func(context.Context, string, initconfig.Options, *config.Config, initconfig.DiscoveryDependencies) (map[core.ProviderName]initconfig.ProviderDiscovery, error) {
			panic("non-interactive init called discovery")
		},
		IsTerminal: func(io.Reader, io.Writer) bool {
			panic("non-interactive init inspected terminal state")
		},
		NewPrompt: func(io.Reader, io.Writer, bool) initconfig.Prompt {
			panic("non-interactive init constructed a prompt")
		},
		LookupEnv: func(string) (string, bool) {
			panic("non-interactive init looked up interactive environment")
		},
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

func TestInitInteractiveNonTerminalReturnsFixedFlagGuidanceWithoutPlanning(t *testing.T) {
	t.Parallel()

	store := &recordingInitStore{}
	var stdout, stderr bytes.Buffer
	terminalCalls := 0
	result := Init(context.Background(), initconfig.Options{
		ConfigPath: filepath.Join(t.TempDir(), "config.toml"),
	}, Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr}, InitDependencies{
		Store: store,
		IsTerminal: func(_ io.Reader, _ io.Writer) bool {
			terminalCalls++
			return false
		},
		Discover: func(context.Context, string, initconfig.Options, *config.Config, initconfig.DiscoveryDependencies) (map[core.ProviderName]initconfig.ProviderDiscovery, error) {
			panic("non-terminal init called discovery")
		},
		NewPrompt: func(io.Reader, io.Writer, bool) initconfig.Prompt {
			panic("non-terminal init constructed a prompt")
		},
		LookupEnv: func(string) (string, bool) {
			panic("non-terminal init looked up environment")
		},
		DefaultInitRuntimeRoot: func() (string, error) {
			panic("non-terminal init resolved runtime root")
		},
	})

	want := "init_requires_non_interactive: pass --non-interactive and all required flags\n"
	if result != (InitResult{Outcome: InitUsage}) || stdout.Len() != 0 ||
		stderr.String() != want || terminalCalls != 1 || len(store.calls) != 0 {
		t.Fatalf("Init() = %#v stdout/stderr %q/%q terminal/store %d/%q",
			result, stdout.String(), stderr.String(), terminalCalls, store.calls)
	}
}

func TestInitInteractiveDryRunDiscoversSelectedProviderBeforeFinalReview(t *testing.T) {
	t.Parallel()

	snapshot, _, runtimeRoot := freshInitFixture(t)
	home := filepath.Join(filepath.Dir(snapshot.Path()), "codex-home")
	executable := filepath.Join(filepath.Dir(snapshot.Path()), "bin", "codex")
	store := &recordingInitStore{
		snapshot:  snapshot,
		preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
		commitFn: func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error) {
			panic("interactive dry run called Commit")
		},
	}
	var stdout, stderr bytes.Buffer
	events := make([]string, 0, 8)
	prompt := &scriptedInitPrompt{}
	prompt.selectProviders = func(context.Context, initconfig.ProviderSelectionRequest) (initconfig.ProviderSelectionResponse, error) {
		events = append(events, "select")
		return initconfig.ProviderSelectionResponse{
			Providers: []core.ProviderName{core.ProviderCodex},
			Decision:  initconfig.ReviewConfirm,
		}, nil
	}
	prompt.collect = func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
		events = append(events, "collect")
		if len(request.Discovery) != 1 || len(request.Discovery[core.ProviderCodex].Commands) != 1 {
			t.Fatalf("Collect discovery = %#v", request.Discovery)
		}
		options := request.Initial
		options.Provider = map[core.ProviderName]initconfig.ProviderInput{
			core.ProviderCodex: {
				Executable: initconfig.StringValue{Set: true, Value: executable},
				ConfigHome: initconfig.StringValue{Set: true, Value: home},
			},
		}
		options.Models = []initconfig.ModelMapping{{
			ID: "codex-local", Provider: core.ProviderCodex, ProviderModel: "gpt-test",
		}}
		return initconfig.CollectResponse{Options: options}, nil
	}
	prompt.review = func(_ context.Context, request initconfig.ReviewRequest) (initconfig.ReviewResponse, error) {
		events = append(events, "review")
		if len(request.Collisions) != 0 || !slices.Equal(store.calls, []string{"load", "preflight"}) ||
			!strings.Contains(stdout.String(), "+ gateway-auth gateway\n") {
			t.Fatalf("final review occurred before preflight/diff: collisions=%#v calls=%q output=%q",
				request.Collisions, store.calls, stdout.String())
		}
		return initconfig.ReviewResponse{Decision: initconfig.ReviewConfirm}, nil
	}

	result := Init(context.Background(), initconfig.Options{
		ConfigPath: snapshot.Path(), DryRun: true,
	}, Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr}, InitDependencies{
		Store: store, Entropy: panicInitReader{},
		DefaultInitRuntimeRoot: func() (string, error) { return runtimeRoot, nil },
		IsTerminal: func(_ io.Reader, _ io.Writer) bool {
			events = append(events, "terminal")
			return true
		},
		LookupEnv: func(name string) (string, bool) {
			events = append(events, "env:"+name)
			return "1", name == "AI_CLI_GATEWAY_ACCESSIBLE"
		},
		NewPrompt: func(input io.Reader, output io.Writer, accessible bool) initconfig.Prompt {
			events = append(events, "prompt")
			if !accessible || input == nil || output != &stderr {
				t.Fatalf("NewPrompt streams/accessibility = %#v/%#v/%t", input, output, accessible)
			}
			return prompt
		},
		Discover: func(_ context.Context, path string, options initconfig.Options, existing *config.Config, _ initconfig.DiscoveryDependencies) (map[core.ProviderName]initconfig.ProviderDiscovery, error) {
			events = append(events, "discover")
			if path != snapshot.Path() || existing != nil ||
				!slices.Equal(options.Providers, []core.ProviderName{core.ProviderCodex}) {
				t.Fatalf("Discover path/options/existing = %q/%#v/%#v", path, options, existing)
			}
			return map[core.ProviderName]initconfig.ProviderDiscovery{
				core.ProviderCodex: {Commands: []initconfig.CommandCandidate{{
					Command: initconfig.ProviderCommand{Executable: executable},
					Source:  initconfig.CandidatePATH,
				}}},
			}, nil
		},
		diagnose: func(context.Context, string, Dependencies) (doctor.Diagnosis, error) {
			panic("interactive dry run called Doctor")
		},
	})

	wantEvents := []string{"terminal", "env:AI_CLI_GATEWAY_ACCESSIBLE", "prompt", "select", "discover", "collect", "review"}
	if result != (InitResult{Outcome: InitDryRun}) || !slices.Equal(events, wantEvents) ||
		!strings.HasSuffix(stdout.String(), "dry_run: no files changed; post-write doctor was not run\n") ||
		stderr.Len() != 0 {
		t.Fatalf("Init() = %#v events=%q stdout/stderr=%q/%q", result, events, stdout.String(), stderr.String())
	}
}

func TestInitInteractiveConfirmsOrphanKeyBeforeFinalDiffAndRepreflights(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := freshInitFixture(t)
	options.NonInteractive = false
	options.DryRun = true
	keyPath := filepath.Join(filepath.Dir(snapshot.Path()), "gateway.key")
	preflightCalls := 0
	store := &recordingInitStore{snapshot: snapshot}
	store.preflightFn = func(_ context.Context, mutation configstore.Mutation) (configstore.PreflightResult, error) {
		preflightCalls++
		if mutation.Key.Path != keyPath || mutation.Key.Intent != configstore.KeyIntentEnsure ||
			mutation.Key.AllowExisting != (preflightCalls == 2) {
			t.Fatalf("preflight %d key plan = %#v", preflightCalls, mutation.Key)
		}
		if preflightCalls == 1 {
			return configstore.PreflightResult{KeyState: configstore.KeyStateNeedsConfirmation}, nil
		}
		return configstore.PreflightResult{KeyState: configstore.KeyStateReusable}, nil
	}
	var stdout, stderr bytes.Buffer
	events := make([]string, 0, 5)
	prompt := &scriptedInitPrompt{
		collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
			events = append(events, "collect")
			return initconfig.CollectResponse{Options: request.Initial}, nil
		},
		confirmKey: func(_ context.Context, request initconfig.KeyConfirmationRequest) (initconfig.ReviewDecision, error) {
			events = append(events, "key")
			if request.Kind != initconfig.ConfirmOrphanReuse || request.Path != keyPath || stdout.Len() != 0 {
				t.Fatalf("key request/output = %#v/%q", request, stdout.String())
			}
			return initconfig.ReviewConfirm, nil
		},
		review: func(_ context.Context, request initconfig.ReviewRequest) (initconfig.ReviewResponse, error) {
			events = append(events, "review")
			if len(request.Collisions) != 0 || preflightCalls != 2 ||
				!strings.Contains(stdout.String(), "+ gateway-auth gateway\n") {
				t.Fatalf("final review request/preflight/output = %#v/%d/%q", request, preflightCalls, stdout.String())
			}
			return initconfig.ReviewResponse{Decision: initconfig.ReviewConfirm}, nil
		},
	}

	result := Init(context.Background(), options,
		Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr},
		interactiveInitDependencies(store, runtimeRoot, prompt))

	if result != (InitResult{Outcome: InitDryRun}) ||
		!slices.Equal(events, []string{"collect", "key", "review"}) || preflightCalls != 2 ||
		strings.Count(stdout.String(), "+ gateway-auth gateway\n") != 1 || stderr.Len() != 0 {
		t.Fatalf("Init() = %#v events=%q preflight=%d stdout/stderr=%q/%q",
			result, events, preflightCalls, stdout.String(), stderr.String())
	}
}

func TestInitInteractiveConfirmsMissingConfiguredKeyCreationAndRepreflights(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := existingInitFixture(t)
	options.NonInteractive = false
	options.DryRun = true
	keyPath := filepath.Join(filepath.Dir(snapshot.Path()), "gateway.key")
	preflightCalls := 0
	store := &recordingInitStore{snapshot: snapshot}
	store.preflightFn = func(_ context.Context, mutation configstore.Mutation) (configstore.PreflightResult, error) {
		preflightCalls++
		wantIntent := configstore.KeyIntentInspect
		if preflightCalls == 2 {
			wantIntent = configstore.KeyIntentEnsure
		}
		if mutation.Key.Path != keyPath || mutation.Key.Intent != wantIntent || mutation.Key.AllowExisting {
			t.Fatalf("preflight %d key plan = %#v", preflightCalls, mutation.Key)
		}
		return configstore.PreflightResult{KeyState: configstore.KeyStateMissing}, nil
	}
	var stdout, stderr bytes.Buffer
	prompt := &scriptedInitPrompt{
		collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
			return initconfig.CollectResponse{Options: request.Initial}, nil
		},
		confirmKey: func(_ context.Context, request initconfig.KeyConfirmationRequest) (initconfig.ReviewDecision, error) {
			if request != (initconfig.KeyConfirmationRequest{
				Kind: initconfig.ConfirmMissingConfiguredKeyCreation, Path: keyPath,
			}) || stdout.Len() != 0 {
				t.Fatalf("key request/output = %#v/%q", request, stdout.String())
			}
			return initconfig.ReviewConfirm, nil
		},
		review: func(_ context.Context, request initconfig.ReviewRequest) (initconfig.ReviewResponse, error) {
			if preflightCalls != 2 || len(request.Collisions) != 0 || stdout.Len() == 0 {
				t.Fatalf("final review preflight/request/output = %d/%#v/%q", preflightCalls, request, stdout.String())
			}
			return initconfig.ReviewResponse{Decision: initconfig.ReviewConfirm}, nil
		},
	}

	result := Init(context.Background(), options,
		Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr},
		interactiveInitDependencies(store, runtimeRoot, prompt))

	if result != (InitResult{Outcome: InitDryRun}) || preflightCalls != 2 || stderr.Len() != 0 {
		t.Fatalf("Init() = %#v preflight=%d stdout/stderr=%q/%q", result, preflightCalls, stdout.String(), stderr.String())
	}
}

func TestInitInteractiveKeyBackResumesOnlyKeyCollectionAndRebuildsPlan(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := freshInitFixture(t)
	options.NonInteractive = false
	options.DryRun = true
	defaultKey := filepath.Join(filepath.Dir(snapshot.Path()), "gateway.key")
	revisedKey := filepath.Join(filepath.Dir(snapshot.Path()), "revised.key")
	events := make([]string, 0, 8)
	store := &recordingInitStore{snapshot: snapshot}
	store.preflightFn = func(_ context.Context, mutation configstore.Mutation) (configstore.PreflightResult, error) {
		events = append(events, "preflight:"+mutation.Key.Path)
		switch mutation.Key.Path {
		case defaultKey:
			return configstore.PreflightResult{KeyState: configstore.KeyStateNeedsConfirmation}, nil
		case revisedKey:
			return configstore.PreflightResult{KeyState: configstore.KeyStateMissing}, nil
		default:
			t.Fatalf("unexpected key path %q", mutation.Key.Path)
			return configstore.PreflightResult{}, nil
		}
	}
	var stdout, stderr bytes.Buffer
	collectCalls := 0
	prompt := &scriptedInitPrompt{
		collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
			collectCalls++
			if collectCalls == 1 {
				events = append(events, "collect-all")
				if request.Discovery == nil {
					t.Fatal("initial collection had nil discovery")
				}
				return initconfig.CollectResponse{Options: request.Initial}, nil
			}
			events = append(events, "collect-key")
			if request.Discovery != nil || !slices.Equal(request.Initial.Providers, options.Providers) ||
				!reflect.DeepEqual(request.Initial.Models, options.Models) {
				t.Fatalf("key-only resume request = %#v", request)
			}
			revised := request.Initial
			revised.Gateway = initconfig.GatewayInput{
				Auth: initconfig.GatewayAuthFile, AuthSet: true,
				KeyFile: initconfig.StringValue{Set: true, Value: revisedKey},
			}
			return initconfig.CollectResponse{Options: revised}, nil
		},
		confirmKey: func(context.Context, initconfig.KeyConfirmationRequest) (initconfig.ReviewDecision, error) {
			events = append(events, "key-back")
			return initconfig.ReviewBack, nil
		},
		review: func(context.Context, initconfig.ReviewRequest) (initconfig.ReviewResponse, error) {
			events = append(events, "review")
			return initconfig.ReviewResponse{Decision: initconfig.ReviewConfirm}, nil
		},
	}
	deps := interactiveInitDependencies(store, runtimeRoot, prompt)
	discoveryCalls := 0
	deps.Discover = func(context.Context, string, initconfig.Options, *config.Config, initconfig.DiscoveryDependencies) (map[core.ProviderName]initconfig.ProviderDiscovery, error) {
		discoveryCalls++
		return map[core.ProviderName]initconfig.ProviderDiscovery{}, nil
	}

	result := Init(context.Background(), options,
		Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr}, deps)

	wantEvents := []string{
		"collect-all", "preflight:" + defaultKey, "key-back", "collect-key",
		"preflight:" + revisedKey, "review",
	}
	if result != (InitResult{Outcome: InitDryRun}) || !slices.Equal(events, wantEvents) ||
		collectCalls != 2 || discoveryCalls != 1 ||
		!strings.Contains(stdout.String(), revisedKey) || strings.Contains(stdout.String(), defaultKey) ||
		stderr.Len() != 0 {
		t.Fatalf("Init() = %#v events=%q discover=%d stdout/stderr=%q/%q",
			result, events, discoveryCalls, stdout.String(), stderr.String())
	}
}

func TestInitInteractiveFinalBackResumesAllAnswersAndRepreflights(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := freshInitFixture(t)
	options.NonInteractive = false
	options.DryRun = true
	events := make([]string, 0, 10)
	preflightCalls := 0
	store := &recordingInitStore{snapshot: snapshot}
	store.preflightFn = func(context.Context, configstore.Mutation) (configstore.PreflightResult, error) {
		preflightCalls++
		events = append(events, "preflight")
		return configstore.PreflightResult{KeyState: configstore.KeyStateMissing}, nil
	}
	var stdout, stderr bytes.Buffer
	collectCalls := 0
	reviewCalls := 0
	prompt := &scriptedInitPrompt{
		selectProviders: func(_ context.Context, request initconfig.ProviderSelectionRequest) (initconfig.ProviderSelectionResponse, error) {
			events = append(events, "select-resume")
			if !slices.Equal(request.Initial, options.Providers) {
				t.Fatalf("resumed selection = %#v", request)
			}
			return initconfig.ProviderSelectionResponse{Providers: request.Initial, Decision: initconfig.ReviewConfirm}, nil
		},
		collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
			collectCalls++
			events = append(events, "collect")
			if request.Discovery == nil {
				t.Fatal("full collection received nil discovery")
			}
			if collectCalls == 1 {
				return initconfig.CollectResponse{Options: request.Initial}, nil
			}
			revised := request.Initial
			revised.Models[0].ProviderModel = "gpt-revised"
			return initconfig.CollectResponse{Options: revised}, nil
		},
		review: func(context.Context, initconfig.ReviewRequest) (initconfig.ReviewResponse, error) {
			reviewCalls++
			if reviewCalls == 1 {
				events = append(events, "review-back")
				return initconfig.ReviewResponse{Decision: initconfig.ReviewBack}, nil
			}
			events = append(events, "review-confirm")
			return initconfig.ReviewResponse{Decision: initconfig.ReviewConfirm}, nil
		},
	}
	deps := interactiveInitDependencies(store, runtimeRoot, prompt)
	discoveryCalls := 0
	deps.Discover = func(context.Context, string, initconfig.Options, *config.Config, initconfig.DiscoveryDependencies) (map[core.ProviderName]initconfig.ProviderDiscovery, error) {
		discoveryCalls++
		events = append(events, "discover")
		return map[core.ProviderName]initconfig.ProviderDiscovery{}, nil
	}

	result := Init(context.Background(), options,
		Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr}, deps)

	wantEvents := []string{
		"discover", "collect", "preflight", "review-back", "select-resume",
		"discover", "collect", "preflight", "review-confirm",
	}
	if result != (InitResult{Outcome: InitDryRun}) || !slices.Equal(events, wantEvents) ||
		preflightCalls != 2 || discoveryCalls != 2 || collectCalls != 2 || reviewCalls != 2 ||
		!strings.Contains(stdout.String(), "provider_model: gpt-revised") || stderr.Len() != 0 {
		t.Fatalf("Init() = %#v events=%q stdout/stderr=%q/%q", result, events, stdout.String(), stderr.String())
	}
}

func TestInitInteractiveDeclineCancelAndRestoreFailureStayClosed(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		prompt     func(context.CancelFunc) *scriptedInitPrompt
		want       InitResult
		wantOutput string
		preflight  bool
	}{
		{
			name: "final decline",
			prompt: func(context.CancelFunc) *scriptedInitPrompt {
				return &scriptedInitPrompt{
					collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
						return initconfig.CollectResponse{Options: request.Initial}, nil
					},
					review: func(context.Context, initconfig.ReviewRequest) (initconfig.ReviewResponse, error) {
						return initconfig.ReviewResponse{Decision: initconfig.ReviewDecline}, nil
					},
				}
			},
			want: InitResult{Outcome: InitDeclined}, preflight: true,
		},
		{
			name: "prompt cancellation",
			prompt: func(cancel context.CancelFunc) *scriptedInitPrompt {
				return &scriptedInitPrompt{collect: func(context.Context, initconfig.CollectRequest) (initconfig.CollectResponse, error) {
					cancel()
					return initconfig.CollectResponse{}, context.Canceled
				}}
			},
			want: InitResult{Outcome: InitCanceled}, wantOutput: "setup_not_saved\n",
		},
		{
			name: "restore plan failure wins over cancellation",
			prompt: func(cancel context.CancelFunc) *scriptedInitPrompt {
				return &scriptedInitPrompt{collect: func(context.Context, initconfig.CollectRequest) (initconfig.CollectResponse, error) {
					cancel()
					return initconfig.CollectResponse{}, appTerminalRestorePlanError{}
				}}
			},
			want: InitResult{Outcome: InitFailed}, wantOutput: "setup_failed\n",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot, options, runtimeRoot := freshInitFixture(t)
			options.NonInteractive = false
			ctx, cancel := context.WithCancel(context.Background())
			store := &recordingInitStore{
				snapshot:  snapshot,
				preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
				commitFn: func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error) {
					panic("closed interactive outcome called Commit")
				},
			}
			var stdout, stderr bytes.Buffer
			result := Init(ctx, options,
				Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr},
				interactiveInitDependencies(store, runtimeRoot, test.prompt(cancel)))

			if result != test.want || stderr.Len() != 0 ||
				(test.wantOutput != "" && stdout.String() != test.wantOutput) ||
				slices.Contains(store.calls, "preflight") != test.preflight ||
				slices.Contains(store.calls, "commit") {
				t.Fatalf("Init() = %#v calls=%q stdout/stderr=%q/%q", result, store.calls, stdout.String(), stderr.String())
			}
		})
	}
}

func TestInitInteractiveKeyConfirmationEOFIsCancellation(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := freshInitFixture(t)
	options.NonInteractive = false
	store := &recordingInitStore{
		snapshot:  snapshot,
		preflight: configstore.PreflightResult{KeyState: configstore.KeyStateNeedsConfirmation},
		commitFn: func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error) {
			panic("key confirmation EOF called Commit")
		},
	}
	prompt := &scriptedInitPrompt{
		collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
			return initconfig.CollectResponse{Options: request.Initial}, nil
		},
		confirmKey: func(context.Context, initconfig.KeyConfirmationRequest) (initconfig.ReviewDecision, error) {
			return 0, io.EOF
		},
	}
	var stdout, stderr bytes.Buffer

	result := Init(context.Background(), options,
		Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr},
		interactiveInitDependencies(store, runtimeRoot, prompt))

	if result != (InitResult{Outcome: InitCanceled}) ||
		stdout.String() != "setup_not_saved\n" || stderr.Len() != 0 ||
		slices.Contains(store.calls, "commit") {
		t.Fatalf("Init() = %#v calls=%q stdout/stderr=%q/%q",
			result, store.calls, stdout.String(), stderr.String())
	}
}

func TestInitInteractiveCanceledKeyDecisionStopsBeforeDecisionEffects(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		decision initconfig.ReviewDecision
	}{
		{name: "decline", decision: initconfig.ReviewDecline},
		{name: "confirm", decision: initconfig.ReviewConfirm},
		{name: "back", decision: initconfig.ReviewBack},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			snapshot, options, runtimeRoot := freshInitFixture(t)
			options.NonInteractive = false
			ctx, cancel := context.WithCancel(context.Background())
			preflightCalls := 0
			store := &recordingInitStore{snapshot: snapshot}
			store.preflightFn = func(context.Context, configstore.Mutation) (configstore.PreflightResult, error) {
				preflightCalls++
				return configstore.PreflightResult{KeyState: configstore.KeyStateNeedsConfirmation}, nil
			}
			store.commitFn = func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error) {
				panic("canceled key decision called Commit")
			}
			collectCalls := 0
			confirmCalls := 0
			prompt := &scriptedInitPrompt{
				collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
					collectCalls++
					return initconfig.CollectResponse{Options: request.Initial}, nil
				},
				confirmKey: func(context.Context, initconfig.KeyConfirmationRequest) (initconfig.ReviewDecision, error) {
					confirmCalls++
					cancel()
					return test.decision, nil
				},
			}
			var stdout, stderr bytes.Buffer

			result := Init(ctx, options,
				Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr},
				interactiveInitDependencies(store, runtimeRoot, prompt))

			if result != (InitResult{Outcome: InitCanceled}) ||
				stdout.String() != "setup_not_saved\n" || stderr.Len() != 0 ||
				preflightCalls != 1 || collectCalls != 1 || confirmCalls != 1 ||
				slices.Contains(store.calls, "commit") {
				t.Fatalf("Init() = %#v calls=%q preflight/collect/confirm=%d/%d/%d stdout/stderr=%q/%q",
					result, store.calls, preflightCalls, collectCalls, confirmCalls,
					stdout.String(), stderr.String())
			}
		})
	}
}

func TestInitInteractiveMissingConfiguredKeyCommitsAfterFinalConfirmEvenWithoutConfigDiff(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := existingInitFixture(t)
	options.NonInteractive = false
	events := make([]string, 0, 7)
	preflightCalls := 0
	store := &recordingInitStore{snapshot: snapshot}
	store.preflightFn = func(context.Context, configstore.Mutation) (configstore.PreflightResult, error) {
		preflightCalls++
		events = append(events, "preflight")
		return configstore.PreflightResult{KeyState: configstore.KeyStateMissing}, nil
	}
	store.commitFn = func(_ context.Context, mutation configstore.Mutation, payload []byte) (configstore.CommitResult, error) {
		events = append(events, "commit")
		if mutation.Key.Intent != configstore.KeyIntentEnsure || len(payload) != 65 {
			t.Fatalf("Commit key/payload = %#v/%d", mutation.Key, len(payload))
		}
		return configstore.CommitResult{State: configstore.CommitCommitted, KeyCreated: true}, nil
	}
	prompt := &scriptedInitPrompt{
		collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
			return initconfig.CollectResponse{Options: request.Initial}, nil
		},
		confirmKey: func(context.Context, initconfig.KeyConfirmationRequest) (initconfig.ReviewDecision, error) {
			events = append(events, "key-confirm")
			return initconfig.ReviewConfirm, nil
		},
		review: func(context.Context, initconfig.ReviewRequest) (initconfig.ReviewResponse, error) {
			events = append(events, "final-confirm")
			return initconfig.ReviewResponse{Decision: initconfig.ReviewConfirm}, nil
		},
	}
	deps := interactiveInitDependencies(store, runtimeRoot, prompt)
	deps.Entropy = &eventInitReader{events: &events, payload: bytes.Repeat([]byte{0x2a}, 32)}
	deps.diagnose = func(context.Context, string, Dependencies) (doctor.Diagnosis, error) {
		events = append(events, "doctor")
		return doctor.Diagnosis{}, errors.New("PLANTED_DOCTOR_FAILURE")
	}
	var stdout, stderr bytes.Buffer

	result := Init(context.Background(), options,
		Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr}, deps)

	wantEvents := []string{
		"preflight", "key-confirm", "preflight", "final-confirm", "entropy", "commit", "doctor",
	}
	if result != (InitResult{Outcome: InitFailed, Saved: true}) ||
		!slices.Equal(events, wantEvents) || preflightCalls != 2 ||
		!strings.HasSuffix(stdout.String(), "post_write_doctor_failed\n") || stderr.Len() != 0 {
		t.Fatalf("Init() = %#v events=%q stdout/stderr=%q/%q", result, events, stdout.String(), stderr.String())
	}
}

func TestInitInteractiveKeyAndFinalRestoreFailuresWinOverSimultaneousCancellation(t *testing.T) {
	t.Parallel()

	for _, stage := range []string{"key", "final"} {
		stage := stage
		t.Run(stage, func(t *testing.T) {
			t.Parallel()
			snapshot, options, runtimeRoot := freshInitFixture(t)
			options.NonInteractive = false
			ctx, cancel := context.WithCancel(context.Background())
			keyState := configstore.KeyStateMissing
			if stage == "key" {
				keyState = configstore.KeyStateNeedsConfirmation
			}
			store := &recordingInitStore{
				snapshot:  snapshot,
				preflight: configstore.PreflightResult{KeyState: keyState},
				commitFn: func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error) {
					panic("restore failure called Commit")
				},
			}
			prompt := &scriptedInitPrompt{
				collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
					return initconfig.CollectResponse{Options: request.Initial}, nil
				},
			}
			if stage == "key" {
				prompt.confirmKey = func(context.Context, initconfig.KeyConfirmationRequest) (initconfig.ReviewDecision, error) {
					cancel()
					return 0, appTerminalRestorePlanError{}
				}
			} else {
				prompt.review = func(context.Context, initconfig.ReviewRequest) (initconfig.ReviewResponse, error) {
					cancel()
					return initconfig.ReviewResponse{}, appTerminalRestorePlanError{}
				}
			}
			var stdout, stderr bytes.Buffer

			result := Init(ctx, options,
				Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr},
				interactiveInitDependencies(store, runtimeRoot, prompt))

			if result != (InitResult{Outcome: InitFailed}) ||
				!strings.HasSuffix(stdout.String(), "setup_failed\n") ||
				strings.Contains(stdout.String(), "setup_not_saved") || stderr.Len() != 0 ||
				slices.Contains(store.calls, "commit") {
				t.Fatalf("Init() = %#v calls=%q stdout/stderr=%q/%q", result, store.calls, stdout.String(), stderr.String())
			}
		})
	}
}

func TestInitInteractiveRejectsNonconvergedOrphanPreflightAfterApproval(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := freshInitFixture(t)
	options.NonInteractive = false
	options.DryRun = true
	preflightCalls := 0
	store := &recordingInitStore{snapshot: snapshot}
	store.preflightFn = func(context.Context, configstore.Mutation) (configstore.PreflightResult, error) {
		preflightCalls++
		return configstore.PreflightResult{KeyState: configstore.KeyStateNeedsConfirmation}, nil
	}
	confirmCalls := 0
	reviewCalls := 0
	prompt := &scriptedInitPrompt{
		collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
			return initconfig.CollectResponse{Options: request.Initial}, nil
		},
		confirmKey: func(context.Context, initconfig.KeyConfirmationRequest) (initconfig.ReviewDecision, error) {
			confirmCalls++
			return initconfig.ReviewConfirm, nil
		},
		review: func(context.Context, initconfig.ReviewRequest) (initconfig.ReviewResponse, error) {
			reviewCalls++
			return initconfig.ReviewResponse{Decision: initconfig.ReviewConfirm}, nil
		},
	}
	var stdout, stderr bytes.Buffer

	result := Init(context.Background(), options,
		Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr},
		interactiveInitDependencies(store, runtimeRoot, prompt))

	if result != (InitResult{Outcome: InitFailed}) || preflightCalls != 2 ||
		confirmCalls != 1 || reviewCalls != 0 || stdout.String() != "setup_failed\n" || stderr.Len() != 0 {
		t.Fatalf("Init() = %#v preflight/confirm/review=%d/%d/%d stdout/stderr=%q/%q",
			result, preflightCalls, confirmCalls, reviewCalls, stdout.String(), stderr.String())
	}
}

func TestInitInteractiveCollisionPreviewPrecedesChoiceAndExplicitValueWinsDiscovery(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := existingInitFixture(t)
	options.NonInteractive = false
	options.DryRun = true
	explicitExecutable := filepath.Join(filepath.Dir(snapshot.Path()), "explicit-codex")
	discoveredExecutable := filepath.Join(filepath.Dir(snapshot.Path()), "discovered-codex")
	input := options.Provider[core.ProviderCodex]
	input.Executable = initconfig.StringValue{Set: true, Value: explicitExecutable}
	options.Provider[core.ProviderCodex] = input
	events := make([]string, 0, 5)
	store := &recordingInitStore{snapshot: snapshot}
	store.preflightFn = func(context.Context, configstore.Mutation) (configstore.PreflightResult, error) {
		events = append(events, "preflight")
		return configstore.PreflightResult{KeyState: configstore.KeyStateReusable}, nil
	}
	var stdout, stderr bytes.Buffer
	reviewCalls := 0
	prompt := &scriptedInitPrompt{
		collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
			events = append(events, "collect")
			if request.Discovery[core.ProviderCodex].Commands[0].Command.Executable != discoveredExecutable {
				t.Fatalf("discovery = %#v", request.Discovery)
			}
			collected := request.Initial
			providerInput := collected.Provider[core.ProviderCodex]
			providerInput.Executable = initconfig.StringValue{Set: true, Value: discoveredExecutable}
			collected.Provider[core.ProviderCodex] = providerInput
			return initconfig.CollectResponse{Options: collected}, nil
		},
		review: func(_ context.Context, request initconfig.ReviewRequest) (initconfig.ReviewResponse, error) {
			reviewCalls++
			if reviewCalls == 1 {
				events = append(events, "collision-review")
				if len(request.Collisions) != 1 || !slices.Equal(store.calls, []string{"load"}) ||
					!strings.Contains(stdout.String(), explicitExecutable) ||
					strings.Contains(stdout.String(), discoveredExecutable) {
					t.Fatalf("collision preview/request/calls = %q/%#v/%q", stdout.String(), request, store.calls)
				}
				return initconfig.ReviewResponse{
					Decision: initconfig.ReviewConfirm,
					Collisions: []initconfig.CollisionDecision{{
						Target: initconfig.DiffProvider, Name: string(core.ProviderCodex),
						Choice: initconfig.CollisionReplace,
					}},
				}, nil
			}
			events = append(events, "final-review")
			if len(request.Collisions) != 0 {
				t.Fatalf("final collisions = %#v", request.Collisions)
			}
			return initconfig.ReviewResponse{Decision: initconfig.ReviewConfirm}, nil
		},
	}
	deps := interactiveInitDependencies(store, runtimeRoot, prompt)
	deps.Discover = func(context.Context, string, initconfig.Options, *config.Config, initconfig.DiscoveryDependencies) (map[core.ProviderName]initconfig.ProviderDiscovery, error) {
		events = append(events, "discover")
		return map[core.ProviderName]initconfig.ProviderDiscovery{
			core.ProviderCodex: {Commands: []initconfig.CommandCandidate{{
				Command: initconfig.ProviderCommand{Executable: discoveredExecutable},
				Source:  initconfig.CandidatePATH,
			}}},
		}, nil
	}

	result := Init(context.Background(), options,
		Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr}, deps)

	wantEvents := []string{"discover", "collect", "collision-review", "preflight", "final-review"}
	if result != (InitResult{Outcome: InitDryRun}) || !slices.Equal(events, wantEvents) ||
		strings.Count(stdout.String(), "~ provider codex\n") != 2 ||
		strings.Contains(stdout.String(), discoveredExecutable) || stderr.Len() != 0 {
		t.Fatalf("Init() = %#v events=%q stdout/stderr=%q/%q", result, events, stdout.String(), stderr.String())
	}
}

func TestInitInteractiveSelectsAccessiblePromptOnlyFromClosedEnvironmentValues(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		values     map[string]string
		want       bool
		wantLookup []string
	}{
		{
			name:   "explicit accessibility",
			values: map[string]string{"AI_CLI_GATEWAY_ACCESSIBLE": "1", "TERM": "xterm"},
			want:   true, wantLookup: []string{"AI_CLI_GATEWAY_ACCESSIBLE"},
		},
		{
			name:   "dumb terminal",
			values: map[string]string{"AI_CLI_GATEWAY_ACCESSIBLE": "0", "TERM": "dumb"},
			want:   true, wantLookup: []string{"AI_CLI_GATEWAY_ACCESSIBLE", "TERM"},
		},
		{
			name:       "visual",
			values:     map[string]string{"TERM": "xterm-256color"},
			wantLookup: []string{"AI_CLI_GATEWAY_ACCESSIBLE", "TERM"},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			snapshot, options, runtimeRoot := freshInitFixture(t)
			options.NonInteractive = false
			prompt := &scriptedInitPrompt{collect: func(context.Context, initconfig.CollectRequest) (initconfig.CollectResponse, error) {
				return initconfig.CollectResponse{}, context.Canceled
			}}
			deps := interactiveInitDependencies(&recordingInitStore{snapshot: snapshot}, runtimeRoot, prompt)
			lookups := make([]string, 0, 2)
			deps.LookupEnv = func(name string) (string, bool) {
				lookups = append(lookups, name)
				value, present := test.values[name]
				return value, present
			}
			promptCalls := 0
			deps.NewPrompt = func(_ io.Reader, _ io.Writer, boolValue bool) initconfig.Prompt {
				promptCalls++
				if boolValue != test.want {
					t.Fatalf("accessible = %t, want %t", boolValue, test.want)
				}
				return prompt
			}
			var stdout, stderr bytes.Buffer

			result := Init(context.Background(), options,
				Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr}, deps)

			if result != (InitResult{Outcome: InitCanceled}) || promptCalls != 1 ||
				!slices.Equal(lookups, test.wantLookup) || stdout.String() != "setup_not_saved\n" || stderr.Len() != 0 {
				t.Fatalf("Init() = %#v prompt/lookups=%d/%q stdout/stderr=%q/%q",
					result, promptCalls, lookups, stdout.String(), stderr.String())
			}
		})
	}
}

func TestInitInteractiveUnsafeKeyPreflightIsNeverOffered(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := freshInitFixture(t)
	options.NonInteractive = false
	store := &recordingInitStore{snapshot: snapshot, preflightErr: configstore.ErrUnsafePath}
	confirmCalls := 0
	reviewCalls := 0
	prompt := &scriptedInitPrompt{
		collect: func(_ context.Context, request initconfig.CollectRequest) (initconfig.CollectResponse, error) {
			return initconfig.CollectResponse{Options: request.Initial}, nil
		},
		confirmKey: func(context.Context, initconfig.KeyConfirmationRequest) (initconfig.ReviewDecision, error) {
			confirmCalls++
			return initconfig.ReviewConfirm, nil
		},
		review: func(context.Context, initconfig.ReviewRequest) (initconfig.ReviewResponse, error) {
			reviewCalls++
			return initconfig.ReviewResponse{Decision: initconfig.ReviewConfirm}, nil
		},
	}
	var stdout, stderr bytes.Buffer

	result := Init(context.Background(), options,
		Streams{In: bytes.NewReader(nil), Out: &stdout, Err: &stderr},
		interactiveInitDependencies(store, runtimeRoot, prompt))

	if result != (InitResult{Outcome: InitFailed}) || confirmCalls != 0 || reviewCalls != 0 ||
		stdout.String() != "setup_failed\n" || stderr.Len() != 0 || slices.Contains(store.calls, "commit") {
		t.Fatalf("Init() = %#v calls=%q confirm/review=%d/%d stdout/stderr=%q/%q",
			result, store.calls, confirmCalls, reviewCalls, stdout.String(), stderr.String())
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

			result := Init(context.Background(), options, nonInteractiveInitStreams(&appErrorWriter{}), InitDependencies{
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
			result := Init(context.Background(), options, nonInteractiveInitStreams(&output), InitDependencies{
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

func TestInitTypedNilEntropyFailsClosedBeforeKeyGeneration(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := freshInitFixture(t)
	options.DryRun = false
	store := &recordingInitStore{
		snapshot:  snapshot,
		preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
		commitFn: func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error) {
			panic("typed-nil entropy called Commit")
		},
	}
	var entropy *eventInitReader
	var output bytes.Buffer

	result := Init(context.Background(), options, nonInteractiveInitStreams(&output), InitDependencies{
		Store: store, Entropy: entropy,
		DefaultInitRuntimeRoot: func() (string, error) { return runtimeRoot, nil },
	})

	if result != (InitResult{Outcome: InitFailed}) ||
		!strings.HasSuffix(output.String(), "setup_failed\n") ||
		slices.Contains(store.calls, "commit") {
		t.Fatalf("Init() = %#v calls=%q output=%q", result, store.calls, output.String())
	}
}

func TestInitTypedNilEntropyIsAllowedWhenEntropyIsNotNeeded(t *testing.T) {
	t.Parallel()

	snapshot, options, runtimeRoot := freshInitFixture(t)
	store := &recordingInitStore{
		snapshot:  snapshot,
		preflight: configstore.PreflightResult{KeyState: configstore.KeyStateMissing},
		commitFn: func(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error) {
			panic("dry run called Commit")
		},
	}
	var entropy *eventInitReader
	var output bytes.Buffer

	result := Init(context.Background(), options, nonInteractiveInitStreams(&output), InitDependencies{
		Store: store, Entropy: entropy,
		DefaultInitRuntimeRoot: func() (string, error) { return runtimeRoot, nil },
	})

	if result != (InitResult{Outcome: InitDryRun}) ||
		!strings.HasSuffix(output.String(), "dry_run: no files changed; post-write doctor was not run\n") ||
		slices.Contains(store.calls, "commit") {
		t.Fatalf("Init() = %#v calls=%q output=%q", result, store.calls, output.String())
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

		result := Init(ctx, options, nonInteractiveInitStreams(&output), InitDependencies{
			Store: store, Entropy: panicInitReader{},
			DefaultInitRuntimeRoot: func() (string, error) { return runtimeRoot, nil },
		})

		if result != (InitResult{Outcome: InitCanceled}) || output.String() != "setup_not_saved\n" {
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

		result := Init(ctx, options, nonInteractiveInitStreams(&output), InitDependencies{
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

		result := Init(ctx, options, nonInteractiveInitStreams(writer), InitDependencies{
			Store: store, Entropy: panicInitReader{},
			DefaultInitRuntimeRoot: func() (string, error) { return runtimeRoot, nil },
		})

		if result != (InitResult{Outcome: InitCanceled}) ||
			!strings.HasSuffix(writer.output.String(), "setup_not_saved\n") ||
			slices.Contains(store.calls, "commit") {
			t.Fatalf("Init() = %#v output %q calls %q", result, writer.output.String(), store.calls)
		}
	})
}

func TestInitAlreadyCanceledWithTypedNilOutputPreservesCanceledOutcome(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output *initFailOnCallWriter

	result := Init(ctx, initconfig.Options{}, Streams{Out: output}, InitDependencies{})

	if result != (InitResult{Outcome: InitCanceled}) {
		t.Fatalf("Init() = %#v, want canceled", result)
	}
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

			result := Init(context.Background(), options, nonInteractiveInitStreams(&output), InitDependencies{
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

	result := Init(context.Background(), options, nonInteractiveInitStreams(&output), InitDependencies{
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

			result := Init(context.Background(), options, nonInteractiveInitStreams(&output), InitDependencies{
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
	result := Init(context.Background(), options, nonInteractiveInitStreams(&output), InitDependencies{
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

	root := testutil.TrustedTempDir(t)
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
	result := Init(context.Background(), options, nonInteractiveInitStreams(&output), InitDependencies{
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
	preflightFn        func(context.Context, configstore.Mutation) (configstore.PreflightResult, error)
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
	ctx context.Context,
	mutation configstore.Mutation,
) (configstore.PreflightResult, error) {
	store.calls = append(store.calls, "preflight")
	store.mutation = mutation
	if store.preflightFn != nil {
		return store.preflightFn(ctx, mutation)
	}
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
	root := testutil.TrustedTempDir(t)
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
	}, nonInteractiveInitStreams(writer), deps)
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

type scriptedInitPrompt struct {
	selectProviders func(context.Context, initconfig.ProviderSelectionRequest) (initconfig.ProviderSelectionResponse, error)
	collect         func(context.Context, initconfig.CollectRequest) (initconfig.CollectResponse, error)
	review          func(context.Context, initconfig.ReviewRequest) (initconfig.ReviewResponse, error)
	confirmKey      func(context.Context, initconfig.KeyConfirmationRequest) (initconfig.ReviewDecision, error)
}

func (prompt *scriptedInitPrompt) SelectProviders(
	ctx context.Context,
	request initconfig.ProviderSelectionRequest,
) (initconfig.ProviderSelectionResponse, error) {
	if prompt == nil || prompt.selectProviders == nil {
		panic("unexpected SelectProviders")
	}
	return prompt.selectProviders(ctx, request)
}

func (prompt *scriptedInitPrompt) Collect(
	ctx context.Context,
	request initconfig.CollectRequest,
) (initconfig.CollectResponse, error) {
	if prompt == nil || prompt.collect == nil {
		panic("unexpected Collect")
	}
	return prompt.collect(ctx, request)
}

func (prompt *scriptedInitPrompt) Review(
	ctx context.Context,
	request initconfig.ReviewRequest,
) (initconfig.ReviewResponse, error) {
	if prompt == nil || prompt.review == nil {
		panic("unexpected Review")
	}
	return prompt.review(ctx, request)
}

func (prompt *scriptedInitPrompt) ConfirmKeyAction(
	ctx context.Context,
	request initconfig.KeyConfirmationRequest,
) (initconfig.ReviewDecision, error) {
	if prompt == nil || prompt.confirmKey == nil {
		panic("unexpected ConfirmKeyAction")
	}
	return prompt.confirmKey(ctx, request)
}

type appErrorReader struct{}

func (appErrorReader) Read([]byte) (int, error) {
	return 0, errors.New("PLANTED_ENTROPY_SECRET")
}

type eventInitReader struct {
	events  *[]string
	payload []byte
	offset  int
}

func (reader *eventInitReader) Read(destination []byte) (int, error) {
	if reader.offset == 0 {
		*reader.events = append(*reader.events, "entropy")
	}
	if reader.offset == len(reader.payload) {
		return 0, io.EOF
	}
	written := copy(destination, reader.payload[reader.offset:])
	reader.offset += written
	return written, nil
}

type appTerminalRestorePlanError struct{}

func (appTerminalRestorePlanError) Error() string { return "planted terminal restore failure" }

func (appTerminalRestorePlanError) Is(target error) bool {
	return target == initconfig.ErrPlan
}

func (appTerminalRestorePlanError) As(target any) bool {
	value := reflect.ValueOf(target)
	if value.Kind() != reflect.Pointer || value.IsNil() || !value.Elem().CanSet() {
		return false
	}
	targetType := value.Elem().Type()
	if targetType.PkgPath() != "github.com/krkarma777/ai-cli-gateway/internal/initconfig" ||
		targetType.Name() != "terminalRestoreError" {
		return false
	}
	value.Elem().Set(reflect.Zero(targetType))
	return true
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

func nonInteractiveInitStreams(output io.Writer) Streams {
	return Streams{In: panicInitReader{}, Out: output, Err: panicWriter{}}
}

func interactiveInitDependencies(
	store InitStore,
	runtimeRoot string,
	prompt initconfig.Prompt,
) InitDependencies {
	return InitDependencies{
		Store: store, Entropy: panicInitReader{},
		DefaultInitRuntimeRoot: func() (string, error) { return runtimeRoot, nil },
		IsTerminal:             func(io.Reader, io.Writer) bool { return true },
		LookupEnv:              func(string) (string, bool) { return "", false },
		NewPrompt:              func(io.Reader, io.Writer, bool) initconfig.Prompt { return prompt },
		Discover: func(context.Context, string, initconfig.Options, *config.Config, initconfig.DiscoveryDependencies) (map[core.ProviderName]initconfig.ProviderDiscovery, error) {
			return map[core.ProviderName]initconfig.ProviderDiscovery{}, nil
		},
		diagnose: func(context.Context, string, Dependencies) (doctor.Diagnosis, error) {
			panic("interactive test called Doctor")
		},
	}
}
