package initconfig

import (
	"context"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

type scriptedPrompt struct {
	selectProviders func(context.Context, ProviderSelectionRequest) (ProviderSelectionResponse, error)
	collect         func(context.Context, CollectRequest) (CollectResponse, error)
	review          func(context.Context, ReviewRequest) (ReviewResponse, error)
	confirmKey      func(context.Context, KeyConfirmationRequest) (ReviewDecision, error)
}

func (prompt *scriptedPrompt) SelectProviders(
	ctx context.Context,
	request ProviderSelectionRequest,
) (ProviderSelectionResponse, error) {
	if prompt.selectProviders == nil {
		panic("unexpected SelectProviders call")
	}
	return prompt.selectProviders(ctx, request)
}

func (prompt *scriptedPrompt) Collect(
	ctx context.Context,
	request CollectRequest,
) (CollectResponse, error) {
	if prompt.collect == nil {
		panic("unexpected Collect call")
	}
	return prompt.collect(ctx, request)
}

func (prompt *scriptedPrompt) Review(
	ctx context.Context,
	request ReviewRequest,
) (ReviewResponse, error) {
	if prompt.review == nil {
		panic("unexpected Review call")
	}
	return prompt.review(ctx, request)
}

func (prompt *scriptedPrompt) ConfirmKeyAction(
	ctx context.Context,
	request KeyConfirmationRequest,
) (ReviewDecision, error) {
	if prompt.confirmKey == nil {
		panic("unexpected ConfirmKeyAction call")
	}
	return prompt.confirmKey(ctx, request)
}

func TestPlanInteractiveAPIIsAvailable(t *testing.T) {
	_, err := PlanInteractive(
		context.Background(), Options{}, nil, Source{}, nil, nil, nil, nil,
		CollectAll, "", "",
	)
	if !errors.Is(err, ErrPlan) {
		t.Fatalf("PlanInteractive() error = %v, want ErrPlan", err)
	}
}

func TestConfirmInteractiveAPIIsAvailable(t *testing.T) {
	_, err := ConfirmInteractive(
		context.Background(), PlanningResult{}, nil, nil,
	)
	if !errors.Is(err, ErrPlan) {
		t.Fatalf("ConfirmInteractive() error = %v, want ErrPlan", err)
	}
}

func TestPlanInteractiveBareStartSelectsDiscoversAndCollects(t *testing.T) {
	var calls []string
	prompt := &scriptedPrompt{
		selectProviders: func(
			_ context.Context,
			request ProviderSelectionRequest,
		) (ProviderSelectionResponse, error) {
			calls = append(calls, "select")
			if len(request.Initial) != 0 || len(request.Existing) != 0 {
				t.Fatalf("selection request = %#v", request)
			}
			return ProviderSelectionResponse{
				Providers: []core.ProviderName{core.ProviderCodex},
				Decision:  ReviewConfirm,
			}, nil
		},
		collect: func(
			_ context.Context,
			request CollectRequest,
		) (CollectResponse, error) {
			calls = append(calls, "collect")
			if _, ok := request.Discovery[core.ProviderCodex]; !ok {
				t.Fatalf("Collect discovery = %#v", request.Discovery)
			}
			options := cloneOptions(request.Initial)
			options.Provider = map[core.ProviderName]ProviderInput{
				core.ProviderCodex: {
					Executable: StringValue{Set: true, Value: "/opt/bin/codex"},
					ConfigHome: StringValue{Set: true, Value: "/srv/codex"},
				},
			}
			options.Models = []ModelMapping{{
				ID: "codex-local", Provider: core.ProviderCodex, ProviderModel: "gpt-user-chosen",
			}}
			return CollectResponse{Options: options}, nil
		},
	}
	discover := func(
		_ context.Context,
		options Options,
	) (map[core.ProviderName]ProviderDiscovery, error) {
		calls = append(calls, "discover")
		if !reflect.DeepEqual(options.Providers, []core.ProviderName{core.ProviderCodex}) {
			t.Fatalf("discovered providers = %#v", options.Providers)
		}
		return map[core.ProviderName]ProviderDiscovery{
			core.ProviderCodex: {},
		}, nil
	}

	result, err := PlanInteractive(
		context.Background(), Options{}, nil, Source{}, nil, discover, prompt,
		func(SemanticDiff) error { panic("unexpected presenter call") },
		CollectAll, "/srv/runtime", "/srv/gateway.key",
	)
	if err != nil {
		t.Fatalf("PlanInteractive() error = %v", err)
	}
	if result.Decision != ReviewConfirm || result.Resume == nil {
		t.Fatalf("result = %#v", result)
	}
	if got, want := result.Plan.Merge.KeyPath, "/srv/gateway.key"; got != want {
		t.Fatalf("KeyPath = %q, want %q", got, want)
	}
	if got, want := calls, []string{"select", "discover", "collect"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
	if got := result.Plan.Desired.Models[0].ProviderModel; got != "gpt-user-chosen" {
		t.Fatalf("provider model = %q", got)
	}
}

func TestPlanInteractiveExplicitMultiProviderBypassesSelectionAndUsesCollectedAnswers(t *testing.T) {
	initial := Options{
		Providers: []core.ProviderName{core.ProviderClaude, core.ProviderGemini},
	}
	prompt := &scriptedPrompt{
		collect: func(
			_ context.Context,
			request CollectRequest,
		) (CollectResponse, error) {
			if len(request.Initial.Models) != 0 {
				t.Fatalf("models were guessed before collection: %#v", request.Initial.Models)
			}
			claude := request.Discovery[core.ProviderClaude]
			gemini := request.Discovery[core.ProviderGemini]
			options := cloneOptions(request.Initial)
			options.Provider = map[core.ProviderName]ProviderInput{
				core.ProviderClaude: {
					Executable: StringValue{Set: true, Value: claude.Commands[0].Command.Executable},
					ConfigHome: StringValue{Set: true, Value: "/edited/claude-home"},
					Auth:       AuthAnthropicAPIKey,
					AuthSet:    true,
				},
				core.ProviderGemini: {
					Executable: StringValue{Set: true, Value: gemini.Commands[0].Command.Executable},
					ConfigHome: geminiPathInput(gemini.ConfigHomes[0].Path),
					Auth:       AuthVertexServiceAccount,
					AuthSet:    true,
				},
			}
			options.Models = []ModelMapping{
				{ID: "claude-fast", Provider: core.ProviderClaude, ProviderModel: "sonnet-user"},
				{ID: "claude-deep", Provider: core.ProviderClaude, ProviderModel: "opus-user"},
				{ID: "gemini-local", Provider: core.ProviderGemini, ProviderModel: "gemini-user"},
			}
			options.Gateway = GatewayInput{Auth: GatewayAuthNone, AuthSet: true}
			return CollectResponse{Options: options}, nil
		},
	}
	discover := func(
		_ context.Context,
		options Options,
	) (map[core.ProviderName]ProviderDiscovery, error) {
		if !reflect.DeepEqual(options.Providers, initial.Providers) {
			t.Fatalf("providers = %#v", options.Providers)
		}
		return map[core.ProviderName]ProviderDiscovery{
			core.ProviderClaude: {
				Commands: []CommandCandidate{{Command: ProviderCommand{Executable: "/opt/bin/claude"}}},
			},
			core.ProviderGemini: {
				Commands:    []CommandCandidate{{Command: ProviderCommand{Executable: "/opt/bin/gemini"}}},
				ConfigHomes: []PathCandidate{{Path: "/suggested/gemini-home"}},
			},
		}, nil
	}

	result, err := PlanInteractive(
		context.Background(), initial, nil, Source{}, nil, discover, prompt,
		func(SemanticDiff) error { return nil }, CollectAll, "/runtime", "/key",
	)
	if err != nil {
		t.Fatalf("PlanInteractive() error = %v", err)
	}
	if got := result.Plan.Desired.Providers[0].ConfigHome.Value; got != "/edited/claude-home" {
		t.Fatalf("edited config home = %q", got)
	}
	if got := result.Plan.Desired.Providers[0].CredentialEnv.Value; !reflect.DeepEqual(got, []string{"ANTHROPIC_API_KEY"}) {
		t.Fatalf("Claude credential env = %#v", got)
	}
	if got := result.Plan.Desired.Providers[1].CredentialEnv.Value; !reflect.DeepEqual(got, []string{
		"GOOGLE_APPLICATION_CREDENTIALS", "GOOGLE_CLOUD_PROJECT", "GOOGLE_CLOUD_LOCATION",
	}) {
		t.Fatalf("Gemini credential env = %#v", got)
	}
	if got := len(result.Plan.Desired.Models); got != 3 {
		t.Fatalf("model count = %d", got)
	}
}

func geminiPathInput(path string) StringValue {
	return StringValue{Set: true, Value: path}
}

func TestPlanInteractiveReselectsAndRediscoversOnlyTheNewSelection(t *testing.T) {
	selections := [][]core.ProviderName{
		{core.ProviderCodex},
		{core.ProviderClaude},
	}
	selectCall := 0
	collectCall := 0
	var discovered [][]core.ProviderName
	prompt := &scriptedPrompt{
		selectProviders: func(
			_ context.Context,
			request ProviderSelectionRequest,
		) (ProviderSelectionResponse, error) {
			if selectCall == 1 && !reflect.DeepEqual(request.Initial, []core.ProviderName{core.ProviderCodex}) {
				t.Fatalf("reselection initial = %#v", request.Initial)
			}
			response := ProviderSelectionResponse{
				Providers: append([]core.ProviderName(nil), selections[selectCall]...),
				Decision:  ReviewConfirm,
			}
			selectCall++
			return response, nil
		},
		collect: func(
			_ context.Context,
			request CollectRequest,
		) (CollectResponse, error) {
			collectCall++
			if collectCall == 1 {
				return CollectResponse{
					Options:         request.Initial,
					BackToSelection: true,
				}, nil
			}
			options := cloneOptions(request.Initial)
			options.Provider = map[core.ProviderName]ProviderInput{
				core.ProviderClaude: {
					Executable: StringValue{Set: true, Value: "/opt/bin/claude"},
					ConfigHome: StringValue{Set: true, Value: "/srv/claude"},
					Auth:       AuthConfigHome,
					AuthSet:    true,
				},
			}
			options.Models = []ModelMapping{{
				ID: "claude-local", Provider: core.ProviderClaude, ProviderModel: "chosen",
			}}
			return CollectResponse{Options: options}, nil
		},
	}
	discover := func(
		_ context.Context,
		options Options,
	) (map[core.ProviderName]ProviderDiscovery, error) {
		discovered = append(discovered, append([]core.ProviderName(nil), options.Providers...))
		result := make(map[core.ProviderName]ProviderDiscovery, len(options.Providers))
		for _, name := range options.Providers {
			result[name] = ProviderDiscovery{}
		}
		return result, nil
	}

	result, err := PlanInteractive(
		context.Background(), Options{}, nil, Source{}, nil, discover, prompt,
		func(SemanticDiff) error { return nil }, CollectAll, "/runtime", "/key",
	)
	if err != nil {
		t.Fatalf("PlanInteractive() error = %v", err)
	}
	if got, want := discovered, selections; !reflect.DeepEqual(got, want) {
		t.Fatalf("discoveries = %#v, want %#v", got, want)
	}
	if got := result.Plan.Desired.SelectedProviders; !reflect.DeepEqual(got, []core.ProviderName{core.ProviderClaude}) {
		t.Fatalf("selected providers = %#v", got)
	}
}

func TestPlanInteractiveCollisionReviewKeepsProviderAndReplacesOnlyChosenModel(t *testing.T) {
	sourceBytes := mergeTableDocument()
	existingValue := mustDecodeMergeConfig(t, sourceBytes)
	existing := &existingValue
	working := collisionInteractiveOptions()
	var events []string
	prompt := &scriptedPrompt{
		collect: func(
			_ context.Context,
			request CollectRequest,
		) (CollectResponse, error) {
			events = append(events, "collect")
			if request.Existing == existing {
				t.Fatal("Collect received caller-owned existing pointer")
			}
			return CollectResponse{Options: cloneOptions(working)}, nil
		},
		review: func(
			_ context.Context,
			request ReviewRequest,
		) (ReviewResponse, error) {
			events = append(events, "review")
			if len(request.Collisions) != 2 {
				t.Fatalf("collisions = %#v", request.Collisions)
			}
			return ReviewResponse{
				Decision: ReviewConfirm,
				Collisions: []CollisionDecision{
					{Target: DiffModel, Name: "codex-existing", Choice: CollisionReplace},
					{Target: DiffProvider, Name: "codex", Choice: CollisionKeepExisting},
				},
			}, nil
		},
	}
	presenter := func(diff SemanticDiff) error {
		events = append(events, "present")
		if len(diff.Entries) == 0 {
			t.Fatal("collision preview is empty")
		}
		return nil
	}

	result, err := PlanInteractive(
		context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}},
		nil, Source{Bytes: sourceBytes, Exists: true}, existing,
		func(_ context.Context, _ Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return map[core.ProviderName]ProviderDiscovery{core.ProviderCodex: {}}, nil
		},
		prompt, presenter, CollectAll, testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil {
		t.Fatalf("PlanInteractive() error = %v", err)
	}
	if got, want := events, []string{"collect", "present", "review"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("events = %#v, want %#v", got, want)
	}
	if result.Decision != ReviewConfirm || result.Resume == nil {
		t.Fatalf("result = %#v", result)
	}
	provider := result.Plan.Desired.Providers[0]
	if got, want := provider.ConfigHome.Value, existing.Providers["codex"].ConfigHome; got != want {
		t.Fatalf("kept config home = %q, want %q", got, want)
	}
	if len(result.Plan.Desired.ReplaceProviders) != 0 {
		t.Fatalf("ReplaceProviders = %#v", result.Plan.Desired.ReplaceProviders)
	}
	if _, ok := result.Plan.Desired.ReplaceModels["codex-existing"]; !ok {
		t.Fatalf("ReplaceModels = %#v", result.Plan.Desired.ReplaceModels)
	}
	if got, want := result.Plan.Merge.Config.Models[0].ProviderModel, "gpt-replacement"; got != want {
		t.Fatalf("replaced model = %q, want %q", got, want)
	}
	if got := result.Plan.Merge.Config.Models[1].ID; got != "codex-new" {
		t.Fatalf("new alias = %q", got)
	}

	keyPrompt := &scriptedPrompt{
		collect: func(_ context.Context, request CollectRequest) (CollectResponse, error) {
			if !containsString(request.Initial.ReplaceModels, "codex-existing") ||
				request.Initial.Provider[core.ProviderCodex].ConfigHome.Set {
				t.Fatalf("collision decisions were not resumed: %#v", request.Initial)
			}
			return CollectResponse{Options: Options{Gateway: request.Initial.Gateway}}, nil
		},
	}
	resumed, err := PlanInteractive(
		context.Background(), Options{NonInteractive: true}, result.Resume,
		Source{Bytes: sourceBytes, Exists: true}, existing, nil, keyPrompt,
		func(SemanticDiff) error { return nil }, CollectGatewayKey,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil {
		t.Fatalf("key-only resume error = %v", err)
	}
	if _, ok := resumed.Plan.Desired.ReplaceModels["codex-existing"]; !ok ||
		len(resumed.Plan.Desired.ReplaceProviders) != 0 {
		t.Fatalf("resumed decisions = %#v/%#v",
			resumed.Plan.Desired.ReplaceProviders, resumed.Plan.Desired.ReplaceModels)
	}
}

func TestPlanInteractiveCollisionDeclineReturnsWithoutConverging(t *testing.T) {
	sourceBytes := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, sourceBytes)
	presented := false
	prompt := &scriptedPrompt{
		collect: func(context.Context, CollectRequest) (CollectResponse, error) {
			return CollectResponse{Options: collisionInteractiveOptions()}, nil
		},
		review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
			if !presented {
				t.Fatal("Review called before preview was presented")
			}
			return ReviewResponse{Decision: ReviewDecline}, nil
		},
	}
	result, err := PlanInteractive(
		context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}}, nil,
		Source{Bytes: sourceBytes, Exists: true}, &existing,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return nil, nil
		}, prompt,
		func(SemanticDiff) error { presented = true; return nil },
		CollectAll, testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil || result.Decision != ReviewDecline || result.Resume == nil {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
}

func TestPlanInteractivePresenterFailureStopsBeforeCollisionReview(t *testing.T) {
	sourceBytes := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, sourceBytes)
	prompt := &scriptedPrompt{
		collect: func(context.Context, CollectRequest) (CollectResponse, error) {
			return CollectResponse{Options: collisionInteractiveOptions()}, nil
		},
		review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
			t.Fatal("Review called after presenter failure")
			return ReviewResponse{}, nil
		},
	}
	_, err := PlanInteractive(
		context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}}, nil,
		Source{Bytes: sourceBytes, Exists: true}, &existing,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return nil, nil
		}, prompt,
		func(SemanticDiff) error { return errors.New("PLANTED presenter secret") },
		CollectAll, testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if !errors.Is(err, ErrPlan) || err.Error() != ErrPlan.Error() {
		t.Fatalf("error = %v, want fixed ErrPlan", err)
	}
}

func TestPlanInteractiveCollisionBackReopensPriorAnswersForEditing(t *testing.T) {
	sourceBytes := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, sourceBytes)
	collectCall := 0
	prompt := &scriptedPrompt{
		selectProviders: func(
			context.Context,
			ProviderSelectionRequest,
		) (ProviderSelectionResponse, error) {
			t.Fatal("SelectProviders called after collision back for explicit providers")
			return ProviderSelectionResponse{}, nil
		},
		collect: func(
			_ context.Context,
			request CollectRequest,
		) (CollectResponse, error) {
			collectCall++
			if collectCall == 1 {
				return CollectResponse{Options: collisionInteractiveOptions()}, nil
			}
			if got := request.Initial.Provider[core.ProviderCodex].ConfigHome.Value; got != testAbsolutePath("changed", "codex-home") {
				t.Fatalf("prior config home = %q", got)
			}
			return CollectResponse{Options: Options{
				Providers: []core.ProviderName{core.ProviderCodex},
				Models: []ModelMapping{{
					ID: "codex-new", Provider: core.ProviderCodex, ProviderModel: "gpt-new",
				}},
			}}, nil
		},
		review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
			return ReviewResponse{Decision: ReviewBack}, nil
		},
	}
	result, err := PlanInteractive(
		context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}}, nil,
		Source{Bytes: sourceBytes, Exists: true}, &existing,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return map[core.ProviderName]ProviderDiscovery{core.ProviderCodex: {}}, nil
		}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil {
		t.Fatalf("PlanInteractive() error = %v", err)
	}
	if collectCall != 2 || result.Decision != ReviewConfirm {
		t.Fatalf("collectCall/result = %d/%#v", collectCall, result)
	}
	if got := result.Plan.Desired.Providers[0].ConfigHome.Value; got != existing.Providers["codex"].ConfigHome {
		t.Fatalf("edited provider home = %q", got)
	}
}

func TestPlanInteractiveResumeCollectGatewayKeySkipsDiscoveryAndPreservesOtherAnswers(t *testing.T) {
	original := Options{
		Providers: []core.ProviderName{core.ProviderCodex},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderCodex: {
				Executable: setString(testAbsolutePath("bin", "codex")),
				ConfigHome: setString(testAbsolutePath("home", "codex")),
			},
		},
		Models: []ModelMapping{{
			ID: "codex-local", Provider: core.ProviderCodex, ProviderModel: "chosen-model",
		}},
		Gateway: GatewayInput{
			Auth: GatewayAuthFile, AuthSet: true,
			KeyFile: setString(testAbsolutePath("keys", "old.key")),
		},
	}
	firstPrompt := &scriptedPrompt{
		collect: func(_ context.Context, request CollectRequest) (CollectResponse, error) {
			return CollectResponse{Options: request.Initial}, nil
		},
	}
	first, err := PlanInteractive(
		context.Background(), original, nil, Source{}, nil,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return map[core.ProviderName]ProviderDiscovery{core.ProviderCodex: {}}, nil
		}, firstPrompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("keys", "default.key"),
	)
	if err != nil {
		t.Fatalf("initial PlanInteractive() error = %v", err)
	}
	if !first.Plan.Merge.KeyAllowExisting {
		t.Fatal("explicit Gateway key path did not allow validated existing-key reuse")
	}

	secondPrompt := &scriptedPrompt{
		collect: func(_ context.Context, request CollectRequest) (CollectResponse, error) {
			if request.Discovery != nil {
				t.Fatalf("key-only Collect discovery = %#v, want nil convention", request.Discovery)
			}
			if got := request.Initial.Models[0].ProviderModel; got != "chosen-model" {
				t.Fatalf("resumed provider model = %q", got)
			}
			gateway := request.Initial.Gateway
			gateway.KeyFile = setString(testAbsolutePath("keys", "new.key"))
			// The key-only convention permits the adapter to return just this
			// group; omitted provider/model groups must remain resumed answers.
			return CollectResponse{Options: Options{Gateway: gateway}}, nil
		},
	}
	poisonInitial := Options{NonInteractive: true, Providers: []core.ProviderName{"unknown"}}
	second, err := PlanInteractive(
		context.Background(), poisonInitial, first.Resume, Source{}, nil,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			t.Fatal("discovery called during CollectGatewayKey")
			return nil, nil
		}, secondPrompt, func(SemanticDiff) error { return nil }, CollectGatewayKey,
		testAbsolutePath("runtime"), testAbsolutePath("keys", "default.key"),
	)
	if err != nil {
		t.Fatalf("resumed PlanInteractive() error = %v", err)
	}
	if got, want := second.Plan.Desired.Gateway.APIKeyFile, testAbsolutePath("keys", "new.key"); got != want {
		t.Fatalf("Gateway key path = %q, want %q", got, want)
	}
	if got, want := second.Plan.Desired.Providers[0].Command.Value.Executable, testAbsolutePath("bin", "codex"); got != want {
		t.Fatalf("provider executable changed to %q, want %q", got, want)
	}
	if got := second.Plan.Desired.Models[0].ProviderModel; got != "chosen-model" {
		t.Fatalf("provider model changed to %q", got)
	}
}

func collisionInteractiveOptions() Options {
	return Options{
		Providers: []core.ProviderName{core.ProviderCodex},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderCodex: {
				ConfigHome: setString(testAbsolutePath("changed", "codex-home")),
			},
		},
		Models: []ModelMapping{
			{ID: "codex-existing", Provider: core.ProviderCodex, ProviderModel: "gpt-replacement"},
			{ID: "codex-new", Provider: core.ProviderCodex, ProviderModel: "gpt-new"},
		},
	}
}

func TestConfirmInteractivePresentsBeforeFinalClosedDecision(t *testing.T) {
	options := validOptions()
	plan, err := PlanNonInteractive(
		options, Source{}, testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil {
		t.Fatalf("PlanNonInteractive() error = %v", err)
	}
	for _, decision := range []ReviewDecision{ReviewConfirm, ReviewBack, ReviewDecline} {
		decision := decision
		t.Run(string(rune('0'+decision)), func(t *testing.T) {
			var events []string
			prompt := &scriptedPrompt{
				review: func(_ context.Context, request ReviewRequest) (ReviewResponse, error) {
					events = append(events, "review")
					if len(request.Collisions) != 0 || len(request.Diff.Entries) == 0 {
						t.Fatalf("final review = %#v", request)
					}
					return ReviewResponse{Decision: decision}, nil
				},
			}
			got, err := ConfirmInteractive(
				context.Background(), plan, prompt,
				func(diff SemanticDiff) error {
					events = append(events, "present")
					diff.Entries[0].Name = "mutated-presenter-copy"
					return nil
				},
			)
			if err != nil || got != decision {
				t.Fatalf("ConfirmInteractive() = %d, %v", got, err)
			}
			if !reflect.DeepEqual(events, []string{"present", "review"}) {
				t.Fatalf("events = %#v", events)
			}
			if plan.Merge.Diff.Entries[0].Name == "mutated-presenter-copy" {
				t.Fatal("presenter mutated caller-owned plan")
			}
		})
	}
}

func TestConfirmInteractivePresenterFailureAndPromptCancellationAreClosed(t *testing.T) {
	options := validOptions()
	plan, err := PlanNonInteractive(
		options, Source{}, testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil {
		t.Fatalf("PlanNonInteractive() error = %v", err)
	}
	t.Run("presenter failure", func(t *testing.T) {
		prompt := &scriptedPrompt{
			review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
				t.Fatal("Review called after presenter failure")
				return ReviewResponse{}, nil
			},
		}
		_, err := ConfirmInteractive(
			context.Background(), plan, prompt,
			func(SemanticDiff) error { return errors.New("PLANTED presenter secret") },
		)
		if err != ErrPlan {
			t.Fatalf("error = %v, want exact ErrPlan", err)
		}
	})
	t.Run("EOF is cancellation", func(t *testing.T) {
		prompt := &scriptedPrompt{
			review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
				return ReviewResponse{}, io.EOF
			},
		}
		_, err := ConfirmInteractive(
			context.Background(), plan, prompt, func(SemanticDiff) error { return nil },
		)
		if err != context.Canceled {
			t.Fatalf("error = %v, want exact context.Canceled", err)
		}
	})
	t.Run("canceled during final review", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		prompt := &scriptedPrompt{
			review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
				cancel()
				return ReviewResponse{Decision: ReviewConfirm}, nil
			},
		}
		_, err := ConfirmInteractive(
			ctx, plan, prompt, func(SemanticDiff) error { return nil },
		)
		if err != context.Canceled {
			t.Fatalf("error = %v, want exact context.Canceled", err)
		}
	})
}

func TestPlanInteractivePromptEOFAbortAndContextCancellationAreCanceled(t *testing.T) {
	t.Run("selection EOF", func(t *testing.T) {
		prompt := &scriptedPrompt{
			selectProviders: func(context.Context, ProviderSelectionRequest) (ProviderSelectionResponse, error) {
				return ProviderSelectionResponse{}, io.EOF
			},
		}
		_, err := PlanInteractive(
			context.Background(), Options{}, nil, Source{}, nil,
			func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
				t.Fatal("discovery called after EOF")
				return nil, nil
			}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
			testAbsolutePath("runtime"), testAbsolutePath("key"),
		)
		if err != context.Canceled {
			t.Fatalf("error = %v, want exact context.Canceled", err)
		}
	})
	t.Run("prompt abort", func(t *testing.T) {
		prompt := &scriptedPrompt{
			collect: func(context.Context, CollectRequest) (CollectResponse, error) {
				return CollectResponse{}, errors.Join(
					context.Canceled, errors.New("PLANTED prompt secret"),
				)
			},
		}
		_, err := PlanInteractive(
			context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}},
			nil, Source{}, nil,
			func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
				return nil, nil
			}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
			testAbsolutePath("runtime"), testAbsolutePath("key"),
		)
		if err != context.Canceled {
			t.Fatalf("error = %v, want exact context.Canceled", err)
		}
	})
	t.Run("canceled during selection", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		prompt := &scriptedPrompt{
			selectProviders: func(context.Context, ProviderSelectionRequest) (ProviderSelectionResponse, error) {
				cancel()
				return ProviderSelectionResponse{
					Providers: []core.ProviderName{core.ProviderCodex}, Decision: ReviewConfirm,
				}, nil
			},
		}
		_, err := PlanInteractive(
			ctx, Options{}, nil, Source{}, nil,
			func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
				t.Fatal("discovery called after context cancellation")
				return nil, nil
			}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
			testAbsolutePath("runtime"), testAbsolutePath("key"),
		)
		if err != context.Canceled {
			t.Fatalf("error = %v, want exact context.Canceled", err)
		}
	})
	t.Run("terminal restore failure outranks simultaneous cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		reader := &scriptedContextLineReader{steps: []func(context.Context) (string, error){
			func(context.Context) (string, error) {
				cancel()
				return "", terminalRestoreError{cause: errors.New("planted restore failure")}
			},
		}}
		prompt := newAccessiblePrompt(io.Discard, reader)

		_, err := PlanInteractive(
			ctx, Options{}, nil, Source{}, nil,
			func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
				t.Fatal("discovery called after terminal restore failure")
				return nil, nil
			}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
			testAbsolutePath("runtime"), testAbsolutePath("key"),
		)
		if err != ErrPlan {
			t.Fatalf("error = %v, want exact ErrPlan", err)
		}
	})
	t.Run("Huh terminal restore failure outranks simultaneous cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		prompt := &scriptedPrompt{
			selectProviders: func(context.Context, ProviderSelectionRequest) (ProviderSelectionResponse, error) {
				cancel()
				return ProviderSelectionResponse{}, finalizeHuhRun(
					ctx,
					nil,
					errors.New("planted Huh terminal restore failure"),
				)
			},
		}

		_, err := PlanInteractive(
			ctx, Options{}, nil, Source{}, nil,
			func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
				t.Fatal("discovery called after Huh terminal restore failure")
				return nil, nil
			}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
			testAbsolutePath("runtime"), testAbsolutePath("key"),
		)
		if err != ErrPlan {
			t.Fatalf("error = %v, want exact ErrPlan", err)
		}
	})
}

func TestPlanInteractiveRejectsInvalidExistingSourceBeforeDiscoveryOrPrompt(t *testing.T) {
	initial := validOptions()
	initial.NonInteractive = false
	prompt := &scriptedPrompt{
		collect: func(context.Context, CollectRequest) (CollectResponse, error) {
			t.Fatal("Collect called for invalid source")
			return CollectResponse{}, nil
		},
	}
	_, err := PlanInteractive(
		context.Background(), initial, nil,
		Source{Bytes: []byte("PLANTED_INVALID_SOURCE"), Exists: true}, nil,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			t.Fatal("discovery called for invalid source")
			return nil, nil
		}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != ErrPlan {
		t.Fatalf("error = %v, want exact ErrPlan", err)
	}
}

func TestPlanInteractiveRejectsInexactCollisionDecisionsAtomically(t *testing.T) {
	sourceBytes := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, sourceBytes)
	prompt := &scriptedPrompt{
		collect: func(context.Context, CollectRequest) (CollectResponse, error) {
			return CollectResponse{Options: collisionInteractiveOptions()}, nil
		},
		review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
			return ReviewResponse{
				Decision: ReviewConfirm,
				Collisions: []CollisionDecision{
					{Target: DiffProvider, Name: "codex", Choice: CollisionReplace},
					{Target: DiffProvider, Name: "codex", Choice: CollisionKeepExisting},
				},
			}, nil
		},
	}
	result, err := PlanInteractive(
		context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}}, nil,
		Source{Bytes: sourceBytes, Exists: true}, &existing,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return nil, nil
		}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != ErrPlan {
		t.Fatalf("error = %v, want exact ErrPlan", err)
	}
	if result.Resume == nil || len(result.Resume.options.ReplaceProviders) != 0 ||
		len(result.Resume.options.ReplaceModels) != 0 {
		t.Fatalf("invalid decisions partially changed resume = %#v", result.Resume)
	}
}

func TestPlanInteractiveDefensivelyClonesExistingDiscoveryPlanAndResume(t *testing.T) {
	sourceBytes := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, sourceBytes)
	discovery := map[core.ProviderName]ProviderDiscovery{
		core.ProviderCodex: {
			Commands: []CommandCandidate{{
				Command: ProviderCommand{
					Executable: testAbsolutePath("bin", "codex"),
					PrefixArgs: []string{"safe-prefix"},
				},
			}},
		},
	}
	prompt := &scriptedPrompt{
		collect: func(_ context.Context, request CollectRequest) (CollectResponse, error) {
			provider := request.Existing.Providers["codex"]
			provider.ConfigHome = testAbsolutePath("poison", "home")
			request.Existing.Providers["codex"] = provider
			request.Discovery[core.ProviderCodex].Commands[0].Command.PrefixArgs[0] = "poison-prefix"
			return CollectResponse{Options: Options{
				Providers: []core.ProviderName{core.ProviderCodex},
			}}, nil
		},
	}
	result, err := PlanInteractive(
		context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}}, nil,
		Source{Bytes: sourceBytes, Exists: true}, &existing,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return discovery, nil
		}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil {
		t.Fatalf("PlanInteractive() error = %v", err)
	}
	if got := discovery[core.ProviderCodex].Commands[0].Command.PrefixArgs[0]; got != "safe-prefix" {
		t.Fatalf("caller discovery mutated to %q", got)
	}
	wantHome := existing.Providers["codex"].ConfigHome
	if got := result.Plan.Desired.Providers[0].ConfigHome.Value; got != wantHome {
		t.Fatalf("planned home = %q, want %q", got, wantHome)
	}
	result.Plan.Desired.Providers[0].ConfigHome.Value = testAbsolutePath("mutated", "plan")
	keyPrompt := &scriptedPrompt{
		collect: func(_ context.Context, request CollectRequest) (CollectResponse, error) {
			if got := request.Initial.Provider[core.ProviderCodex].ConfigHome; got.Set || got.Value != "" {
				t.Fatalf("resume changed through returned plan: %#v", got)
			}
			return CollectResponse{Options: Options{Gateway: request.Initial.Gateway}}, nil
		},
	}
	resumed, err := PlanInteractive(
		context.Background(), Options{}, result.Resume,
		Source{Bytes: sourceBytes, Exists: true}, &existing, nil, keyPrompt,
		func(SemanticDiff) error { return nil }, CollectGatewayKey,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil || resumed.Plan.Desired.Providers[0].ConfigHome.Value != wantHome {
		t.Fatalf("resumed plan/error = %#v/%v", resumed.Plan.Desired, err)
	}
}

func TestPlanInteractiveCollisionPreviewContainsNoRawSourceContent(t *testing.T) {
	const planted = "PLANTED_RAW_SOURCE_SECRET"
	sourceBytes := append([]byte("# "+planted+"\n"), mergeTableDocument()...)
	existing := mustDecodeMergeConfig(t, sourceBytes)
	prompt := &scriptedPrompt{
		collect: func(context.Context, CollectRequest) (CollectResponse, error) {
			return CollectResponse{Options: collisionInteractiveOptions()}, nil
		},
		review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
			return ReviewResponse{Decision: ReviewDecline}, nil
		},
	}
	result, err := PlanInteractive(
		context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}}, nil,
		Source{Bytes: sourceBytes, Exists: true}, &existing,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return nil, nil
		}, prompt,
		func(diff SemanticDiff) error {
			if strings.Contains(fmt.Sprintf("%#v", diff), planted) {
				t.Fatal("semantic preview retained raw source content")
			}
			return nil
		}, CollectAll, testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil || result.Decision != ReviewDecline {
		t.Fatalf("result/error = %#v/%v", result, err)
	}
}

func TestPlanInteractiveCollectCannotOverrideExplicitInputsOrInjectAuthorization(t *testing.T) {
	explicitExecutable := testAbsolutePath("explicit", "codex")
	explicitKey := testAbsolutePath("explicit", "gateway.key")
	initial := Options{
		ConfigPath: testAbsolutePath("explicit", "config.toml"),
		DryRun:     true,
		Providers:  []core.ProviderName{core.ProviderCodex},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderCodex: {Executable: setString(explicitExecutable)},
		},
		Models: []ModelMapping{{
			ID: "fixed-alias", Provider: core.ProviderCodex, ProviderModel: "fixed-model",
		}},
		Gateway: GatewayInput{
			Auth: GatewayAuthFile, AuthSet: true, KeyFile: setString(explicitKey),
		},
	}
	prompt := &scriptedPrompt{
		selectProviders: func(context.Context, ProviderSelectionRequest) (ProviderSelectionResponse, error) {
			t.Fatal("explicit provider selection was reopened")
			return ProviderSelectionResponse{}, nil
		},
		collect: func(_ context.Context, request CollectRequest) (CollectResponse, error) {
			options := cloneOptions(request.Initial)
			options.Providers = []core.ProviderName{core.ProviderClaude}
			options.ConfigPath = testAbsolutePath("poison", "config.toml")
			options.DryRun = false
			input := options.Provider[core.ProviderCodex]
			input.Executable = setString(testAbsolutePath("poison", "codex"))
			input.ConfigHome = setString(testAbsolutePath("collected", "codex-home"))
			options.Provider[core.ProviderCodex] = input
			options.Models = []ModelMapping{
				{ID: "fixed-alias", Provider: core.ProviderCodex, ProviderModel: "poison-model"},
				{ID: "extra-alias", Provider: core.ProviderCodex, ProviderModel: "extra-model"},
			}
			options.Gateway = GatewayInput{Auth: GatewayAuthNone, AuthSet: true}
			options.ReplaceProviders = []core.ProviderName{core.ProviderCodex}
			options.ReplaceModels = []string{"fixed-alias", "extra-alias"}
			return CollectResponse{Options: options}, nil
		},
	}
	result, err := PlanInteractive(
		context.Background(), initial, nil, Source{}, nil,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return map[core.ProviderName]ProviderDiscovery{core.ProviderCodex: {}}, nil
		}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("default.key"),
	)
	if err != nil {
		t.Fatalf("PlanInteractive() error = %v", err)
	}
	if got := result.Plan.Desired.Providers[0].Command.Value.Executable; got != explicitExecutable {
		t.Fatalf("explicit executable = %q, want %q", got, explicitExecutable)
	}
	if got := result.Plan.Desired.Providers[0].ConfigHome.Value; got != testAbsolutePath("collected", "codex-home") {
		t.Fatalf("collected missing config home = %q", got)
	}
	if got := result.Plan.Desired.Models; !reflect.DeepEqual(got, []ModelMapping{
		{ID: "fixed-alias", Provider: core.ProviderCodex, ProviderModel: "fixed-model"},
		{ID: "extra-alias", Provider: core.ProviderCodex, ProviderModel: "extra-model"},
	}) {
		t.Fatalf("models = %#v", got)
	}
	if got := result.Plan.Desired.Gateway.APIKeyFile; got != explicitKey {
		t.Fatalf("explicit key = %q, want %q", got, explicitKey)
	}
	if len(result.Plan.Desired.ReplaceProviders) != 0 || len(result.Plan.Desired.ReplaceModels) != 0 {
		t.Fatalf("Collect injected authorization = %#v/%#v",
			result.Plan.Desired.ReplaceProviders, result.Plan.Desired.ReplaceModels)
	}
	if result.Resume.options.ConfigPath != initial.ConfigPath ||
		result.Resume.options.DryRun != initial.DryRun {
		t.Fatalf("resume CLI fields = %q/%t", result.Resume.options.ConfigPath, result.Resume.options.DryRun)
	}
}

func TestPlanInteractiveCollectCannotOverrideExplicitProviderAuth(t *testing.T) {
	initial := Options{
		Providers: []core.ProviderName{core.ProviderClaude},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderClaude: {
				Executable: setString(testAbsolutePath("bin", "claude")),
				ConfigHome: setString(testAbsolutePath("home", "claude")),
				Auth:       AuthConfigHome,
				AuthSet:    true,
			},
		},
		Models: []ModelMapping{{
			ID: "claude-local", Provider: core.ProviderClaude, ProviderModel: "chosen",
		}},
	}
	prompt := &scriptedPrompt{
		collect: func(_ context.Context, request CollectRequest) (CollectResponse, error) {
			options := cloneOptions(request.Initial)
			input := options.Provider[core.ProviderClaude]
			input.Auth = AuthAnthropicAPIKey
			input.AuthSet = true
			options.Provider[core.ProviderClaude] = input
			return CollectResponse{Options: options}, nil
		},
	}
	result, err := PlanInteractive(
		context.Background(), initial, nil, Source{}, nil,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return nil, nil
		}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil {
		t.Fatalf("PlanInteractive() error = %v", err)
	}
	if got := result.Plan.Desired.Providers[0].CredentialEnv.Value; len(got) != 0 {
		t.Fatalf("explicit config-home auth overwritten: %#v", got)
	}
}

func TestPlanInteractiveCollectRetainsExistingReplacementAuthorization(t *testing.T) {
	initial := Options{
		Providers: []core.ProviderName{core.ProviderCodex},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderCodex: {
				Executable: setString(testAbsolutePath("bin", "codex")),
				ConfigHome: setString(testAbsolutePath("home", "codex")),
			},
		},
		Models: []ModelMapping{{
			ID: "codex-local", Provider: core.ProviderCodex, ProviderModel: "chosen",
		}},
		ReplaceProviders: []core.ProviderName{core.ProviderCodex},
		ReplaceModels:    []string{"codex-local"},
	}
	prompt := &scriptedPrompt{
		collect: func(_ context.Context, request CollectRequest) (CollectResponse, error) {
			options := cloneOptions(request.Initial)
			options.Models = nil
			options.ReplaceProviders = nil
			options.ReplaceModels = nil
			return CollectResponse{Options: options}, nil
		},
	}
	result, err := PlanInteractive(
		context.Background(), initial, nil, Source{}, nil,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return nil, nil
		}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil {
		t.Fatalf("PlanInteractive() error = %v", err)
	}
	if !containsProvider(result.Resume.options.ReplaceProviders, core.ProviderCodex) ||
		!containsString(result.Resume.options.ReplaceModels, "codex-local") {
		t.Fatalf("replacement authority = %#v/%#v",
			result.Resume.options.ReplaceProviders, result.Resume.options.ReplaceModels)
	}
}

func TestPlanInteractiveExplicitProviderBackReopensDetailsWithoutSelection(t *testing.T) {
	collectCalls := 0
	discoverCalls := 0
	prompt := &scriptedPrompt{
		selectProviders: func(context.Context, ProviderSelectionRequest) (ProviderSelectionResponse, error) {
			t.Fatal("SelectProviders called for locked explicit selection")
			return ProviderSelectionResponse{}, nil
		},
		collect: func(_ context.Context, request CollectRequest) (CollectResponse, error) {
			collectCalls++
			if !reflect.DeepEqual(request.Initial.Providers, []core.ProviderName{core.ProviderCodex}) {
				t.Fatalf("providers = %#v", request.Initial.Providers)
			}
			if collectCalls == 1 {
				return CollectResponse{Options: request.Initial, BackToSelection: true}, nil
			}
			options := cloneOptions(request.Initial)
			options.Provider = map[core.ProviderName]ProviderInput{
				core.ProviderCodex: {
					Executable: setString(testAbsolutePath("bin", "codex")),
					ConfigHome: setString(testAbsolutePath("home", "codex")),
				},
			}
			options.Models = []ModelMapping{{
				ID: "codex-local", Provider: core.ProviderCodex, ProviderModel: "chosen",
			}}
			return CollectResponse{Options: options}, nil
		},
	}
	result, err := PlanInteractive(
		context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}}, nil,
		Source{}, nil,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			discoverCalls++
			return map[core.ProviderName]ProviderDiscovery{core.ProviderCodex: {}}, nil
		}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil || result.Decision != ReviewConfirm || collectCalls != 2 || discoverCalls != 2 {
		t.Fatalf("result/error/calls = %#v/%v/%d/%d", result, err, collectCalls, discoverCalls)
	}
}

func TestPlanInteractiveRequiresMatchingExistingSourceBeforeInteraction(t *testing.T) {
	sourceBytes := mergeTableDocument()
	matching := mustDecodeMergeConfig(t, sourceBytes)
	mismatch := cloneConfig(matching)
	mismatch.Server.Listen = "127.0.0.1:9090"
	initial := validOptions()
	initial.NonInteractive = false
	for _, test := range []struct {
		name     string
		source   Source
		existing *config.Config
	}{
		{name: "existing missing", source: Source{Bytes: sourceBytes, Exists: true}},
		{name: "semantic mismatch", source: Source{Bytes: sourceBytes, Exists: true}, existing: &mismatch},
		{name: "unexpected existing", source: Source{}, existing: &matching},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			_, err := PlanInteractive(
				context.Background(), initial, nil, test.source, test.existing,
				func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
					t.Fatal("discovery called for incoherent source")
					return nil, nil
				}, &scriptedPrompt{}, func(SemanticDiff) error { return nil }, CollectAll,
				testAbsolutePath("runtime"), testAbsolutePath("key"),
			)
			if err != ErrPlan {
				t.Fatalf("error = %v, want exact ErrPlan", err)
			}
		})
	}
}

func TestPlanInteractiveInvalidCollectOutputIsFixedPlanFailure(t *testing.T) {
	prompt := &scriptedPrompt{
		collect: func(context.Context, CollectRequest) (CollectResponse, error) {
			return CollectResponse{Options: Options{
				Providers: []core.ProviderName{core.ProviderCodex},
			}}, nil
		},
	}
	_, err := PlanInteractive(
		context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}}, nil,
		Source{}, nil,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return nil, nil
		}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != ErrPlan {
		t.Fatalf("error = %v, want exact ErrPlan", err)
	}
}

func TestPlanInteractiveDiscoveryEOFIsOperationalFailure(t *testing.T) {
	_, err := PlanInteractive(
		context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}}, nil,
		Source{}, nil,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return nil, io.EOF
		}, &scriptedPrompt{}, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != ErrPlan {
		t.Fatalf("error = %v, want exact ErrPlan", err)
	}
}

func TestConfirmInteractiveRejectsUnauthorizedCollisionAndIncompleteMerge(t *testing.T) {
	sourceBytes := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, sourceBytes)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Providers[0].ConfigHome.Value = testAbsolutePath("changed", "codex-home")
	preview, err := PlanMerge(sourceBytes, true, desired)
	if !errors.Is(err, ErrCollision) {
		t.Fatalf("PlanMerge() error = %v, want ErrCollision", err)
	}
	for _, test := range []struct {
		name string
		plan PlanningResult
	}{
		{
			name: "unauthorized collision preview",
			plan: PlanningResult{Desired: desired, Merge: preview},
		},
		{
			name: "empty fabricated merge",
			plan: PlanningResult{Desired: validDesiredState()},
		},
		{
			name: "candidate config mismatch",
			plan: func() PlanningResult {
				options := validOptions()
				valid, planErr := PlanNonInteractive(
					options, Source{}, testAbsolutePath("runtime"), testAbsolutePath("key"),
				)
				if planErr != nil {
					t.Fatalf("PlanNonInteractive() error = %v", planErr)
				}
				valid.Merge.Config.Server.Listen = "127.0.0.1:9090"
				return valid
			}(),
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			prompt := &scriptedPrompt{
				review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
					t.Fatal("Review called for invalid final plan")
					return ReviewResponse{}, nil
				},
			}
			_, err := ConfirmInteractive(
				context.Background(), test.plan, prompt,
				func(SemanticDiff) error {
					t.Fatal("presenter called for invalid final plan")
					return nil
				},
			)
			if err != ErrPlan {
				t.Fatalf("error = %v, want exact ErrPlan", err)
			}
		})
	}
}

func TestConfirmInteractiveValidatesRuntimeRootOnlyForFreshPlans(t *testing.T) {
	t.Run("fresh runtime mismatch is rejected", func(t *testing.T) {
		plan, err := PlanNonInteractive(
			validOptions(), Source{}, testAbsolutePath("runtime"), testAbsolutePath("key"),
		)
		if err != nil {
			t.Fatalf("PlanNonInteractive() error = %v", err)
		}
		plan.Desired.NewRuntimeRoot = testAbsolutePath("forged-runtime")

		_, err = ConfirmInteractive(
			context.Background(), plan, &scriptedPrompt{},
			func(SemanticDiff) error {
				t.Fatal("presenter called for fresh runtime mismatch")
				return nil
			},
		)
		if err != ErrPlan {
			t.Fatalf("error = %v, want exact ErrPlan", err)
		}
	})

	t.Run("existing no-op preserves runtime", func(t *testing.T) {
		source := mergeTableDocument()
		existing := mustDecodeMergeConfig(t, source)
		desired := desiredFromExisting(existing, core.ProviderCodex)
		merge, err := PlanMerge(source, true, desired)
		if err != nil || merge.Changed {
			t.Fatalf("PlanMerge() = changed %t, error %v", merge.Changed, err)
		}
		if merge.Config.Runtime.Root == desired.NewRuntimeRoot {
			t.Fatal("fixture did not preserve a distinct existing runtime root")
		}
		decision, err := ConfirmInteractive(
			context.Background(), PlanningResult{Desired: desired, Merge: merge},
			&scriptedPrompt{review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
				return ReviewResponse{Decision: ReviewConfirm}, nil
			}},
			func(SemanticDiff) error { return nil },
		)
		if err != nil || decision != ReviewConfirm {
			t.Fatalf("ConfirmInteractive() = %d, %v", decision, err)
		}
	})
}

func TestConfirmInteractiveRequiresCompleteProductionDiffShapes(t *testing.T) {
	fresh, err := PlanNonInteractive(
		validOptions(), Source{}, testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil {
		t.Fatalf("fresh PlanNonInteractive() error = %v", err)
	}

	source := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, source)

	providerDesired := desiredFromExisting(existing, core.ProviderCodex)
	providerDesired.Providers[0].ConfigHome.Value = testAbsolutePath("changed", "codex-home")
	providerDesired.ReplaceProviders[core.ProviderCodex] = struct{}{}
	providerMerge, err := PlanMerge(source, true, providerDesired)
	if err != nil {
		t.Fatalf("provider replacement PlanMerge() error = %v", err)
	}
	providerPlan := PlanningResult{Desired: providerDesired, Merge: providerMerge}

	modelDesired := desiredFromExisting(existing, core.ProviderCodex)
	modelDesired.Models = []ModelMapping{{
		ID:            "codex-existing",
		Provider:      core.ProviderCodex,
		ProviderModel: "gpt-replacement",
	}}
	modelDesired.ReplaceModels["codex-existing"] = struct{}{}
	modelMerge, err := PlanMerge(source, true, modelDesired)
	if err != nil {
		t.Fatalf("model replacement PlanMerge() error = %v", err)
	}
	modelPlan := PlanningResult{Desired: modelDesired, Merge: modelMerge}

	gatewayDesired := desiredFromExisting(existing, core.ProviderCodex)
	gatewayDesired.Gateway = GatewayAuthPatch{
		Set: true, APIKeyFile: testAbsolutePath("changed", "gateway.key"), KeyExplicit: true,
	}
	gatewayMerge, err := PlanMerge(source, true, gatewayDesired)
	if err != nil {
		t.Fatalf("gateway replacement PlanMerge() error = %v", err)
	}
	gatewayPlan := PlanningResult{Desired: gatewayDesired, Merge: gatewayMerge}

	clearFields := func(plan *PlanningResult, target DiffTarget, name string) {
		t.Helper()
		found := false
		for index := range plan.Merge.Diff.Entries {
			entry := &plan.Merge.Diff.Entries[index]
			if entry.Target == target && entry.Name == name {
				entry.Fields = nil
				found = true
			}
		}
		for index := range plan.Merge.Collisions {
			collision := &plan.Merge.Collisions[index]
			if collision.Target == target && collision.Name == name {
				collision.Fields = nil
			}
		}
		if !found {
			t.Fatalf("missing diff target %d/%q", target, name)
		}
	}

	malformed := []struct {
		name   string
		plan   PlanningResult
		target DiffTarget
		entry  string
	}{
		{name: "empty fresh gateway addition", plan: fresh, target: DiffGatewayAuth, entry: "gateway"},
		{name: "empty gateway replacement", plan: gatewayPlan, target: DiffGatewayAuth, entry: "gateway"},
		{name: "empty provider replacement collision", plan: providerPlan, target: DiffProvider, entry: "codex"},
		{name: "empty model replacement collision", plan: modelPlan, target: DiffModel, entry: "codex-existing"},
	}
	for _, test := range malformed {
		test := test
		t.Run(test.name, func(t *testing.T) {
			candidate := clonePlanningResult(test.plan)
			clearFields(&candidate, test.target, test.entry)
			_, err := ConfirmInteractive(
				context.Background(), candidate, &scriptedPrompt{},
				func(SemanticDiff) error {
					t.Fatal("presenter called for incomplete semantic diff")
					return nil
				},
			)
			if err != ErrPlan {
				t.Fatalf("error = %v, want exact ErrPlan", err)
			}
		})
	}

	noOpDesired := desiredFromExisting(existing, core.ProviderCodex)
	noOpMerge, err := PlanMerge(source, true, noOpDesired)
	if err != nil {
		t.Fatalf("no-op PlanMerge() error = %v", err)
	}

	claudeDesired := DesiredState{
		NewRuntimeRoot:    testAbsolutePath("new-runtime"),
		SelectedProviders: []core.ProviderName{core.ProviderClaude},
		Providers: []ProviderPatch{{
			Name: core.ProviderClaude,
			Command: Optional[ProviderCommand]{Set: true, Value: ProviderCommand{
				Executable: testAbsolutePath("bin", "claude"),
			}},
			ConfigHome: Optional[string]{Set: true, Value: testAbsolutePath("homes", "claude")},
			CredentialEnv: Optional[[]string]{
				Set: true, Value: []string{"ANTHROPIC_API_KEY"},
			},
		}},
		Models: []ModelMapping{{
			ID: "claude-local", Provider: core.ProviderClaude, ProviderModel: "sonnet",
		}},
		ReplaceProviders: map[core.ProviderName]struct{}{},
		ReplaceModels:    map[string]struct{}{},
	}
	claudeMerge, err := PlanMerge(source, true, claudeDesired)
	if err != nil {
		t.Fatalf("existing-add PlanMerge() error = %v", err)
	}

	legitimate := []struct {
		name string
		plan PlanningResult
	}{
		{name: "fresh", plan: fresh},
		{name: "existing no-op", plan: PlanningResult{Desired: noOpDesired, Merge: noOpMerge}},
		{name: "existing provider addition", plan: PlanningResult{Desired: claudeDesired, Merge: claudeMerge}},
		{name: "authorized provider replacement", plan: providerPlan},
		{name: "gateway replacement", plan: gatewayPlan},
	}
	for _, test := range legitimate {
		test := test
		t.Run("accepts "+test.name, func(t *testing.T) {
			decision, err := ConfirmInteractive(
				context.Background(), test.plan,
				&scriptedPrompt{review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
					return ReviewResponse{Decision: ReviewConfirm}, nil
				}},
				func(SemanticDiff) error { return nil },
			)
			if err != nil || decision != ReviewConfirm {
				t.Fatalf("ConfirmInteractive() = %d, %v", decision, err)
			}
		})
	}
}

func TestConfirmInteractiveEnforcesKeyActionIntegrity(t *testing.T) {
	freshDefault, err := PlanNonInteractive(
		validOptions(), Source{}, testAbsolutePath("runtime"), testAbsolutePath("default.key"),
	)
	if err != nil {
		t.Fatalf("default fresh PlanNonInteractive() error = %v", err)
	}
	explicitOptions := validOptions()
	explicitPath := testAbsolutePath("explicit", "gateway.key")
	explicitOptions.Gateway = GatewayInput{
		Auth: GatewayAuthFile, AuthSet: true, KeyFile: setString(explicitPath),
	}
	freshExplicit, err := PlanNonInteractive(
		explicitOptions, Source{}, testAbsolutePath("runtime"), testAbsolutePath("unused.key"),
	)
	if err != nil {
		t.Fatalf("explicit fresh PlanNonInteractive() error = %v", err)
	}

	source := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, source)
	newPathDesired := desiredFromExisting(existing, core.ProviderCodex)
	newPathDesired.Gateway = GatewayAuthPatch{
		Set: true, APIKeyFile: testAbsolutePath("changed", "gateway.key"), KeyExplicit: true,
	}
	newPathMerge, err := PlanMerge(source, true, newPathDesired)
	if err != nil {
		t.Fatalf("new-path PlanMerge() error = %v", err)
	}
	newPathPlan := PlanningResult{Desired: newPathDesired, Merge: newPathMerge}

	configuredPath := testAbsolutePath("configured", "gateway.key")
	configuredSource := []byte(strings.Replace(
		string(source), "api_key_env = 'AI_CLI_GATEWAY_API_KEY'",
		"api_key_file = "+string(mustTOMLValue(t, configuredPath)), 1,
	))
	configuredExisting := mustDecodeMergeConfig(t, configuredSource)
	configuredDesired := desiredFromExisting(configuredExisting, core.ProviderCodex)
	configuredMerge, err := PlanMerge(configuredSource, true, configuredDesired)
	if err != nil || configuredMerge.KeyAction != KeyActionInspect {
		t.Fatalf("configured-path PlanMerge() action/error = %d/%v", configuredMerge.KeyAction, err)
	}
	configuredPlan := PlanningResult{Desired: configuredDesired, Merge: configuredMerge}

	invalid := []struct {
		name   string
		plan   PlanningResult
		mutate func(*MergePlan)
	}{
		{
			name: "fresh key addition forged as inspect",
			plan: freshDefault,
			mutate: func(merge *MergePlan) {
				merge.KeyAction = KeyActionInspect
			},
		},
		{
			name: "existing new path forged as inspect",
			plan: newPathPlan,
			mutate: func(merge *MergePlan) {
				merge.KeyAction = KeyActionInspect
				merge.KeyAllowExisting = false
			},
		},
		{
			name: "configured path upgrade gains reuse authority",
			plan: configuredPlan,
			mutate: func(merge *MergePlan) {
				merge.KeyAction = KeyActionEnsure
				merge.KeyAllowExisting = true
			},
		},
	}
	for _, test := range invalid {
		test := test
		t.Run(test.name, func(t *testing.T) {
			candidate := clonePlanningResult(test.plan)
			test.mutate(&candidate.Merge)
			_, err := ConfirmInteractive(
				context.Background(), candidate, &scriptedPrompt{},
				func(SemanticDiff) error {
					t.Fatal("presenter called for invalid key plan")
					return nil
				},
			)
			if err != ErrPlan {
				t.Fatalf("error = %v, want exact ErrPlan", err)
			}
		})
	}

	configuredEnsure := clonePlanningResult(configuredPlan)
	configuredEnsure.Merge.KeyAction = KeyActionEnsure
	confirmedDefaultReuse := clonePlanningResult(freshDefault)
	confirmedDefaultReuse.Merge.KeyAllowExisting = true
	unconfirmedExplicitReuse := clonePlanningResult(freshExplicit)
	unconfirmedExplicitReuse.Merge.KeyAllowExisting = false
	unconfirmedExistingNewPath := clonePlanningResult(newPathPlan)
	unconfirmedExistingNewPath.Merge.KeyAllowExisting = false
	legitimate := []struct {
		name string
		plan PlanningResult
	}{
		{name: "fresh default ensure", plan: freshDefault},
		{name: "confirmed default orphan reuse", plan: confirmedDefaultReuse},
		{name: "fresh explicit reuse ensure", plan: freshExplicit},
		{name: "unconfirmed explicit new path", plan: unconfirmedExplicitReuse},
		{name: "existing new-path reuse ensure", plan: newPathPlan},
		{name: "unconfirmed existing new path", plan: unconfirmedExistingNewPath},
		{name: "configured path inspect", plan: configuredPlan},
		{name: "configured missing-key ensure upgrade", plan: configuredEnsure},
	}
	for _, test := range legitimate {
		test := test
		t.Run("accepts "+test.name, func(t *testing.T) {
			decision, err := ConfirmInteractive(
				context.Background(), test.plan,
				&scriptedPrompt{review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
					return ReviewResponse{Decision: ReviewConfirm}, nil
				}},
				func(SemanticDiff) error { return nil },
			)
			if err != nil || decision != ReviewConfirm {
				t.Fatalf("ConfirmInteractive() = %d, %v", decision, err)
			}
		})
	}
}

func TestConfirmInteractiveAcceptsAuthorizedRetainedCollisionMetadata(t *testing.T) {
	sourceBytes := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, sourceBytes)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Providers[0].ConfigHome.Value = testAbsolutePath("changed", "codex-home")
	desired.ReplaceProviders[core.ProviderCodex] = struct{}{}
	merge, err := PlanMerge(sourceBytes, true, desired)
	if err != nil || len(merge.Collisions) != 1 {
		t.Fatalf("authorized PlanMerge() = %#v, %v", merge.Collisions, err)
	}
	expected := cloneMergePlan(merge).Diff
	presented := false
	prompt := &scriptedPrompt{
		review: func(_ context.Context, request ReviewRequest) (ReviewResponse, error) {
			if !presented {
				t.Fatal("Review called before presenter")
			}
			if !reflect.DeepEqual(request.Diff, expected) || len(request.Collisions) != 0 {
				t.Fatalf("final review = %#v, want diff %#v", request, expected)
			}
			return ReviewResponse{Decision: ReviewConfirm}, nil
		},
	}
	decision, err := ConfirmInteractive(
		context.Background(), PlanningResult{Desired: desired, Merge: merge}, prompt,
		func(diff SemanticDiff) error {
			if !reflect.DeepEqual(diff, expected) {
				t.Fatalf("presented diff = %#v, want %#v", diff, expected)
			}
			presented = true
			diff.Entries[0].Name = "mutated-presenter-copy"
			if len(diff.Entries[0].Fields) != 0 {
				diff.Entries[0].Fields[0].After = "mutated-presenter-field"
			}
			return nil
		},
	)
	if err != nil || decision != ReviewConfirm {
		t.Fatalf("ConfirmInteractive() = %d, %v", decision, err)
	}
}

func TestPlanInteractiveCollectCannotPreauthorizeCollision(t *testing.T) {
	sourceBytes := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, sourceBytes)
	reviewed := false
	prompt := &scriptedPrompt{
		collect: func(context.Context, CollectRequest) (CollectResponse, error) {
			options := collisionInteractiveOptions()
			options.ReplaceProviders = []core.ProviderName{core.ProviderCodex}
			options.ReplaceModels = []string{"codex-existing"}
			return CollectResponse{Options: options}, nil
		},
		review: func(_ context.Context, request ReviewRequest) (ReviewResponse, error) {
			reviewed = true
			if len(request.Collisions) != 2 {
				t.Fatalf("collisions = %#v", request.Collisions)
			}
			return ReviewResponse{Decision: ReviewDecline}, nil
		},
	}
	result, err := PlanInteractive(
		context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}}, nil,
		Source{Bytes: sourceBytes, Exists: true}, &existing,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return nil, nil
		}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil || result.Decision != ReviewDecline || !reviewed {
		t.Fatalf("result/error/reviewed = %#v/%v/%t", result, err, reviewed)
	}
	if len(result.Resume.options.ReplaceProviders) != 0 ||
		len(result.Resume.options.ReplaceModels) != 0 {
		t.Fatalf("prompt-injected authority retained = %#v", result.Resume.options)
	}
}

func TestPlanInteractiveCollisionDecisionTableIsExactAndAtomic(t *testing.T) {
	tests := []struct {
		name      string
		decisions []CollisionDecision
	}{
		{
			name: "missing",
			decisions: []CollisionDecision{
				{Target: DiffProvider, Name: "codex", Choice: CollisionReplace},
			},
		},
		{
			name: "extra",
			decisions: []CollisionDecision{
				{Target: DiffProvider, Name: "codex", Choice: CollisionReplace},
				{Target: DiffModel, Name: "codex-existing", Choice: CollisionReplace},
				{Target: DiffModel, Name: "extra", Choice: CollisionReplace},
			},
		},
		{
			name: "duplicate",
			decisions: []CollisionDecision{
				{Target: DiffProvider, Name: "codex", Choice: CollisionReplace},
				{Target: DiffProvider, Name: "codex", Choice: CollisionKeepExisting},
			},
		},
		{
			name: "unknown target",
			decisions: []CollisionDecision{
				{Target: DiffTarget(99), Name: "codex", Choice: CollisionReplace},
				{Target: DiffModel, Name: "codex-existing", Choice: CollisionReplace},
			},
		},
		{
			name: "unknown choice",
			decisions: []CollisionDecision{
				{Target: DiffProvider, Name: "codex", Choice: CollisionChoice(99)},
				{Target: DiffModel, Name: "codex-existing", Choice: CollisionReplace},
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			sourceBytes := mergeTableDocument()
			existing := mustDecodeMergeConfig(t, sourceBytes)
			prompt := &scriptedPrompt{
				collect: func(context.Context, CollectRequest) (CollectResponse, error) {
					return CollectResponse{Options: collisionInteractiveOptions()}, nil
				},
				review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
					return ReviewResponse{
						Decision: ReviewConfirm, Collisions: test.decisions,
					}, nil
				},
			}
			result, err := PlanInteractive(
				context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}}, nil,
				Source{Bytes: sourceBytes, Exists: true}, &existing,
				func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
					return nil, nil
				}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
				testAbsolutePath("runtime"), testAbsolutePath("key"),
			)
			if err != ErrPlan {
				t.Fatalf("error = %v, want exact ErrPlan", err)
			}
			if len(result.Resume.options.ReplaceProviders) != 0 ||
				len(result.Resume.options.ReplaceModels) != 0 {
				t.Fatalf("partial authorization = %#v", result.Resume.options)
			}
		})
	}
}

func TestPlanInteractiveRepeatedDecidedCollisionFailsClosedWithoutReprompt(t *testing.T) {
	sourceBytes := mergeTwoProviderDocument()
	existing := mustDecodeMergeConfig(t, sourceBytes)
	reviewCalls := 0
	prompt := &scriptedPrompt{
		collect: func(context.Context, CollectRequest) (CollectResponse, error) {
			return CollectResponse{Options: Options{
				Providers: []core.ProviderName{core.ProviderClaude},
				Models: []ModelMapping{{
					ID:            "shared-existing",
					Provider:      core.ProviderClaude,
					ProviderModel: "claude-replacement",
				}},
			}}, nil
		},
		review: func(_ context.Context, request ReviewRequest) (ReviewResponse, error) {
			reviewCalls++
			if len(request.Collisions) != 1 {
				t.Fatalf("collisions = %#v", request.Collisions)
			}
			return ReviewResponse{
				Decision: ReviewConfirm,
				Collisions: []CollisionDecision{{
					Target: DiffModel, Name: "shared-existing", Choice: CollisionKeepExisting,
				}},
			}, nil
		},
	}
	_, err := PlanInteractive(
		context.Background(), Options{Providers: []core.ProviderName{core.ProviderClaude}}, nil,
		Source{Bytes: sourceBytes, Exists: true}, &existing,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return nil, nil
		}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != ErrPlan || reviewCalls != 1 {
		t.Fatalf("error/reviewCalls = %v/%d, want ErrPlan/1", err, reviewCalls)
	}
}

func TestConvergedInteractivePlanningRejectsRepeatedCollision(t *testing.T) {
	preview := PlanningResult{Merge: MergePlan{
		Candidate:  []byte("safe-preview"),
		Collisions: []Collision{{Target: DiffProvider, Name: "codex"}},
	}}
	got, err := requireConvergedInteractivePlanning(preview, ErrCollision)
	if err != ErrPlan {
		t.Fatalf("error = %v, want exact ErrPlan", err)
	}
	if !reflect.DeepEqual(got, clonePlanningResult(preview)) {
		t.Fatalf("preview = %#v, want defensive clone %#v", got, preview)
	}
}

func TestPlanInteractiveCollectAllOpaqueResumeIgnoresInitialAndReopensAnswers(t *testing.T) {
	original := Options{
		ConfigPath: testAbsolutePath("first", "config.toml"),
		DryRun:     true,
		Providers:  []core.ProviderName{core.ProviderCodex},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderCodex: {
				Executable: setString(testAbsolutePath("first", "codex")),
				ConfigHome: setString(testAbsolutePath("first", "home")),
			},
		},
		Models: []ModelMapping{{
			ID: "codex-local", Provider: core.ProviderCodex, ProviderModel: "first-model",
		}},
	}
	first, err := PlanInteractive(
		context.Background(), original, nil, Source{}, nil,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return nil, nil
		}, &scriptedPrompt{
			collect: func(_ context.Context, request CollectRequest) (CollectResponse, error) {
				return CollectResponse{Options: request.Initial}, nil
			},
		}, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil {
		t.Fatalf("first PlanInteractive() error = %v", err)
	}

	selected := false
	secondPrompt := &scriptedPrompt{
		selectProviders: func(
			_ context.Context,
			request ProviderSelectionRequest,
		) (ProviderSelectionResponse, error) {
			selected = true
			if !reflect.DeepEqual(request.Initial, original.Providers) {
				t.Fatalf("resumed selection = %#v", request.Initial)
			}
			return ProviderSelectionResponse{Providers: request.Initial, Decision: ReviewConfirm}, nil
		},
		collect: func(_ context.Context, request CollectRequest) (CollectResponse, error) {
			options := cloneOptions(request.Initial)
			options.ConfigPath = testAbsolutePath("edited", "config.toml")
			options.DryRun = false
			input := options.Provider[core.ProviderCodex]
			input.Executable = setString(testAbsolutePath("edited", "codex"))
			options.Provider[core.ProviderCodex] = input
			options.Models[0].ProviderModel = "edited-model"
			options.ReplaceProviders = []core.ProviderName{core.ProviderCodex}
			options.ReplaceModels = []string{"codex-local"}
			return CollectResponse{Options: options}, nil
		},
	}
	poison := Options{NonInteractive: true, Providers: []core.ProviderName{"unknown"}}
	second, err := PlanInteractive(
		context.Background(), poison, first.Resume, Source{}, nil,
		func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
			return nil, nil
		}, secondPrompt, func(SemanticDiff) error { return nil }, CollectAll,
		testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil || !selected {
		t.Fatalf("second PlanInteractive()/selected = %v/%t", err, selected)
	}
	if got := second.Plan.Desired.Providers[0].Command.Value.Executable; got != testAbsolutePath("edited", "codex") {
		t.Fatalf("edited executable = %q", got)
	}
	if got := second.Plan.Desired.Models[0].ProviderModel; got != "edited-model" {
		t.Fatalf("edited model = %q", got)
	}
	if second.Resume.options.ConfigPath != testAbsolutePath("edited", "config.toml") ||
		second.Resume.options.DryRun ||
		len(second.Resume.options.ReplaceProviders) != 0 ||
		len(second.Resume.options.ReplaceModels) != 0 {
		t.Fatalf("resumed options = %#v", second.Resume.options)
	}
}

func TestPlanInteractiveInvalidResponseCategoriesAreFixed(t *testing.T) {
	t.Run("initial CLI input stays usage", func(t *testing.T) {
		_, err := PlanInteractive(
			context.Background(), Options{Providers: []core.ProviderName{"unknown"}}, nil,
			Source{}, nil,
			func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
				t.Fatal("discovery called for invalid initial input")
				return nil, nil
			}, &scriptedPrompt{}, func(SemanticDiff) error { return nil }, CollectAll,
			testAbsolutePath("runtime"), testAbsolutePath("key"),
		)
		if err != ErrUsage {
			t.Fatalf("error = %v, want exact ErrUsage", err)
		}
	})
	t.Run("unknown provider selection decision", func(t *testing.T) {
		prompt := &scriptedPrompt{
			selectProviders: func(context.Context, ProviderSelectionRequest) (ProviderSelectionResponse, error) {
				return ProviderSelectionResponse{
					Providers: []core.ProviderName{core.ProviderCodex},
					Decision:  ReviewDecision(99),
				}, nil
			},
		}
		_, err := PlanInteractive(
			context.Background(), Options{}, nil, Source{}, nil,
			func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
				t.Fatal("discovery called after invalid selection decision")
				return nil, nil
			}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
			testAbsolutePath("runtime"), testAbsolutePath("key"),
		)
		if err != ErrPlan {
			t.Fatalf("error = %v, want exact ErrPlan", err)
		}
	})
	t.Run("invalid collected auth", func(t *testing.T) {
		prompt := &scriptedPrompt{
			collect: func(_ context.Context, request CollectRequest) (CollectResponse, error) {
				options := cloneOptions(request.Initial)
				options.Provider = map[core.ProviderName]ProviderInput{
					core.ProviderClaude: {
						Executable: setString(testAbsolutePath("bin", "claude")),
						ConfigHome: setString(testAbsolutePath("home", "claude")),
						Auth:       AuthID("invalid-auth"),
						AuthSet:    true,
					},
				}
				options.Models = []ModelMapping{{
					ID: "claude-local", Provider: core.ProviderClaude, ProviderModel: "chosen",
				}}
				return CollectResponse{Options: options}, nil
			},
		}
		_, err := PlanInteractive(
			context.Background(), Options{Providers: []core.ProviderName{core.ProviderClaude}}, nil,
			Source{}, nil,
			func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
				return nil, nil
			}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
			testAbsolutePath("runtime"), testAbsolutePath("key"),
		)
		if err != ErrPlan {
			t.Fatalf("error = %v, want exact ErrPlan", err)
		}
	})
	t.Run("unknown collision review decision", func(t *testing.T) {
		sourceBytes := mergeTableDocument()
		existing := mustDecodeMergeConfig(t, sourceBytes)
		prompt := &scriptedPrompt{
			collect: func(context.Context, CollectRequest) (CollectResponse, error) {
				return CollectResponse{Options: collisionInteractiveOptions()}, nil
			},
			review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
				return ReviewResponse{Decision: ReviewDecision(99)}, nil
			},
		}
		_, err := PlanInteractive(
			context.Background(), Options{Providers: []core.ProviderName{core.ProviderCodex}}, nil,
			Source{Bytes: sourceBytes, Exists: true}, &existing,
			func(context.Context, Options) (map[core.ProviderName]ProviderDiscovery, error) {
				return nil, nil
			}, prompt, func(SemanticDiff) error { return nil }, CollectAll,
			testAbsolutePath("runtime"), testAbsolutePath("key"),
		)
		if err != ErrPlan {
			t.Fatalf("error = %v, want exact ErrPlan", err)
		}
	})
}

func TestConfirmInteractiveRejectsMalformedAuthorizedCollisionMetadata(t *testing.T) {
	sourceBytes := mergeTableDocument()
	existing := mustDecodeMergeConfig(t, sourceBytes)
	desired := desiredFromExisting(existing, core.ProviderCodex)
	desired.Providers[0].ConfigHome.Value = testAbsolutePath("changed", "codex-home")
	desired.ReplaceProviders[core.ProviderCodex] = struct{}{}
	merge, err := PlanMerge(sourceBytes, true, desired)
	if err != nil {
		t.Fatalf("PlanMerge() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*MergePlan)
	}{
		{
			name: "missing collision metadata",
			mutate: func(plan *MergePlan) {
				plan.Collisions = nil
			},
		},
		{
			name: "duplicate collision metadata",
			mutate: func(plan *MergePlan) {
				plan.Collisions = append(plan.Collisions, plan.Collisions[0])
			},
		},
		{
			name: "collision field mismatch",
			mutate: func(plan *MergePlan) {
				plan.Collisions[0].Fields[0].After = testAbsolutePath("other", "home")
			},
		},
		{
			name: "missing semantic diff",
			mutate: func(plan *MergePlan) {
				plan.Diff = SemanticDiff{}
			},
		},
		{
			name: "invalid key plan",
			mutate: func(plan *MergePlan) {
				plan.KeyAction = KeyActionEnsure
				plan.KeyPath = testAbsolutePath("unrelated", "key")
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneMergePlan(merge)
			test.mutate(&candidate)
			_, err := ConfirmInteractive(
				context.Background(), PlanningResult{Desired: desired, Merge: candidate},
				&scriptedPrompt{},
				func(SemanticDiff) error {
					t.Fatal("presenter called for malformed plan")
					return nil
				},
			)
			if err != ErrPlan {
				t.Fatalf("error = %v, want exact ErrPlan", err)
			}
		})
	}
}

func TestConfirmInteractiveInvalidReviewResponseIsFixed(t *testing.T) {
	plan, err := PlanNonInteractive(
		validOptions(), Source{}, testAbsolutePath("runtime"), testAbsolutePath("key"),
	)
	if err != nil {
		t.Fatalf("PlanNonInteractive() error = %v", err)
	}
	for _, test := range []struct {
		name     string
		response ReviewResponse
	}{
		{name: "unknown decision", response: ReviewResponse{Decision: ReviewDecision(99)}},
		{
			name: "unexpected collision decision",
			response: ReviewResponse{
				Decision: ReviewConfirm,
				Collisions: []CollisionDecision{{
					Target: DiffProvider, Name: "codex", Choice: CollisionReplace,
				}},
			},
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			decision, err := ConfirmInteractive(
				context.Background(), plan,
				&scriptedPrompt{
					review: func(context.Context, ReviewRequest) (ReviewResponse, error) {
						return test.response, nil
					},
				}, func(SemanticDiff) error { return nil },
			)
			if err != ErrPlan || decision != 0 {
				t.Fatalf("decision/error = %d/%v, want 0/ErrPlan", decision, err)
			}
		})
	}
}
