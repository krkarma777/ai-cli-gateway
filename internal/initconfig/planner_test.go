package initconfig

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestPlanNonInteractiveComposesFreshNoWritePlan(t *testing.T) {
	t.Parallel()

	options := validOptions()
	options.ConfigPath = testAbsolutePath("does-not-exist", "config.toml")
	options.DryRun = true
	defaultRuntime := testAbsolutePath("runtime")
	defaultKey := testAbsolutePath("config", "gateway.key")
	result, err := PlanNonInteractive(
		options,
		Source{},
		defaultRuntime,
		defaultKey,
	)
	if err != nil {
		t.Fatalf("PlanNonInteractive() error = %v", err)
	}
	if result.Desired.NewRuntimeRoot != defaultRuntime ||
		result.Desired.Gateway.APIKeyFile != defaultKey {
		t.Fatalf("Desired = %#v", result.Desired)
	}
	if !result.Merge.Changed || result.Merge.KeyAction != KeyActionEnsure {
		t.Fatalf("Merge = %#v", result.Merge)
	}
	if len(result.Merge.Candidate) == 0 {
		t.Fatal("Candidate is empty")
	}
}

func TestPlanNonInteractiveUsesOnlySuppliedExistingSource(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	options := Options{
		ConfigPath:     testAbsolutePath("missing", "must-not-open.toml"),
		NonInteractive: true,
		DryRun:         true,
		Providers:      []core.ProviderName{core.ProviderCodex},
	}
	result, err := PlanNonInteractive(
		options,
		Source{Bytes: source, Exists: true},
		testAbsolutePath("runtime-default-not-used"),
		testAbsolutePath("gateway-default-not-used.key"),
	)
	if err != nil {
		t.Fatalf("PlanNonInteractive() error = %v", err)
	}
	if result.Merge.Changed || !bytes.Equal(result.Merge.Candidate, source) {
		t.Fatalf("existing no-op changed source:\n%s", result.Merge.Candidate)
	}
	if result.Desired.Providers[0].Command.Value.Executable !=
		testAbsolutePath("bin", "codex") {
		t.Fatalf("Desired provider = %#v", result.Desired.Providers[0])
	}
}

func TestPlanNonInteractiveReturnsSafeCollisionPreview(t *testing.T) {
	t.Parallel()

	source := mergeTableDocument()
	options := Options{
		NonInteractive: true,
		Providers:      []core.ProviderName{core.ProviderCodex},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderCodex: {
				ConfigHome: setString(testAbsolutePath("changed", "codex-home")),
			},
		},
	}
	result, err := PlanNonInteractive(
		options,
		Source{Bytes: source, Exists: true},
		testAbsolutePath("runtime"),
		testAbsolutePath("gateway.key"),
	)
	if !errors.Is(err, ErrCollision) {
		t.Fatalf("PlanNonInteractive() error = %v, want ErrCollision", err)
	}
	if len(result.Merge.Candidate) == 0 || len(result.Merge.Collisions) != 1 {
		t.Fatalf("collision result = %#v", result)
	}
}

func TestPlanNonInteractiveRejectsInvalidSourceAndIncompleteInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		options Options
		source  Source
		want    error
	}{
		{
			name:    "invalid existing source",
			options: Options{NonInteractive: true, Providers: []core.ProviderName{core.ProviderCodex}},
			source:  Source{Bytes: []byte("PLANTED_INVALID"), Exists: true},
			want:    ErrPlan,
		},
		{
			name:    "missing provider",
			options: Options{NonInteractive: true},
			want:    ErrUsage,
		},
		{
			name:    "interactive options",
			options: Options{Providers: []core.ProviderName{core.ProviderCodex}},
			want:    ErrUsage,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := PlanNonInteractive(
				test.options,
				test.source,
				testAbsolutePath("runtime"),
				testAbsolutePath("gateway.key"),
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("PlanNonInteractive() error = %v, want %v", err, test.want)
			}
			if len(result.Merge.Candidate) != 0 {
				t.Fatalf("invalid plan returned candidate %q", result.Merge.Candidate)
			}
		})
	}
}

func TestPlanNonInteractiveDefensivelyCopiesInputsAndResults(t *testing.T) {
	t.Parallel()

	options := validOptions()
	sourceBytes := []byte(nil)
	first, err := PlanNonInteractive(
		options,
		Source{Bytes: sourceBytes},
		testAbsolutePath("runtime"),
		testAbsolutePath("gateway.key"),
	)
	if err != nil {
		t.Fatalf("PlanNonInteractive() error = %v", err)
	}
	want := clonePlanningResultForTest(first)

	options.Providers[0] = core.ProviderClaude
	input := options.Provider[core.ProviderCodex]
	input.Executable.Value = "MUTATED"
	options.Provider[core.ProviderCodex] = input
	options.Models[0].ID = "mutated"
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("result aliases input: %#v, want %#v", first, want)
	}

	first.Desired.SelectedProviders[0] = core.ProviderClaude
	first.Desired.Providers[0].Command.Value.Executable = "MUTATED"
	first.Merge.Candidate[0] = 'X'
	second, err := PlanNonInteractive(
		validOptions(),
		Source{},
		testAbsolutePath("runtime"),
		testAbsolutePath("gateway.key"),
	)
	if err != nil {
		t.Fatalf("second PlanNonInteractive() error = %v", err)
	}
	if second.Desired.SelectedProviders[0] != core.ProviderCodex ||
		second.Desired.Providers[0].Command.Value.Executable == "MUTATED" ||
		second.Merge.Candidate[0] == 'X' {
		t.Fatalf("planner retained prior result memory: %#v", second)
	}
}

func clonePlanningResultForTest(result PlanningResult) PlanningResult {
	return PlanningResult{
		Desired: cloneDesiredState(result.Desired),
		Merge:   cloneMergePlan(result.Merge),
	}
}
