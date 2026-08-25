package initconfig

import (
	"bytes"
	"errors"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

// Source is one caller-supplied configuration snapshot.
type Source struct {
	Bytes  []byte
	Exists bool
}

// PlanningResult combines the resolved semantic state and validated merge.
type PlanningResult struct {
	Desired DesiredState
	Merge   MergePlan
}

// PlanNonInteractive composes strict validation, existing-source decoding,
// explicit-only resolution, and pure merge planning. It performs no I/O.
func PlanNonInteractive(
	options Options,
	source Source,
	defaultRuntimeRoot string,
	defaultKeyPath string,
) (PlanningResult, error) {
	options = cloneOptions(options)
	source.Bytes = append([]byte(nil), source.Bytes...)
	if err := ValidateOptions(options); err != nil || !options.NonInteractive {
		return PlanningResult{}, ErrUsage
	}
	if source.Exists && len(source.Bytes) == 0 ||
		!source.Exists && len(source.Bytes) != 0 {
		return PlanningResult{}, ErrPlan
	}

	var existing *config.Config
	if source.Exists {
		decoded, err := config.Decode(bytes.NewReader(source.Bytes))
		if err != nil {
			return PlanningResult{}, ErrPlan
		}
		decoded = cloneConfig(decoded)
		existing = &decoded
	}
	desired, err := ResolveNonInteractive(
		options,
		existing,
		defaultRuntimeRoot,
		defaultKeyPath,
	)
	if err != nil {
		return PlanningResult{}, err
	}
	merge, err := PlanMerge(source.Bytes, source.Exists, desired)
	result := clonePlanningResult(PlanningResult{Desired: desired, Merge: merge})
	if err != nil {
		if errors.Is(err, ErrCollision) {
			return result, ErrCollision
		}
		return PlanningResult{}, err
	}
	return result, nil
}

func cloneOptions(options Options) Options {
	cloned := options
	cloned.Providers = append([]core.ProviderName(nil), options.Providers...)
	cloned.Models = append([]ModelMapping(nil), options.Models...)
	cloned.ReplaceProviders = append(
		[]core.ProviderName(nil),
		options.ReplaceProviders...,
	)
	cloned.ReplaceModels = append([]string(nil), options.ReplaceModels...)
	if options.Provider != nil {
		cloned.Provider = make(map[core.ProviderName]ProviderInput, len(options.Provider))
		for name, input := range options.Provider {
			cloned.Provider[name] = input
		}
	}
	return cloned
}

func clonePlanningResult(result PlanningResult) PlanningResult {
	return PlanningResult{
		Desired: cloneDesiredState(result.Desired),
		Merge:   cloneMergePlan(result.Merge),
	}
}
