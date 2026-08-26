package initconfig

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

// PlanInteractive collects, validates, and plans an interactive init session.
func PlanInteractive(
	ctx context.Context,
	initial Options,
	resume *ResumeState,
	source Source,
	existing *config.Config,
	discover DiscoverSelected,
	prompt Prompt,
	present DiffPresenter,
	focus CollectFocus,
	defaultRuntimeRoot string,
	defaultKeyPath string,
) (InteractiveResult, error) {
	explicit := cloneOptions(initial)
	working := cloneOptions(initial)
	if resume != nil {
		working = cloneOptions(resume.options)
	}
	result := func(plan PlanningResult, decision ReviewDecision) InteractiveResult {
		return InteractiveResult{
			Plan:     clonePlanningResult(plan),
			Decision: decision,
			Resume:   &ResumeState{options: cloneOptions(working)},
		}
	}
	if resume == nil {
		if working.NonInteractive {
			return result(PlanningResult{}, 0), ErrUsage
		}
		if err := ValidateOptions(working); err != nil {
			return result(PlanningResult{}, 0), err
		}
	} else if working.NonInteractive || ValidateOptions(working) != nil {
		return result(PlanningResult{}, 0), ErrPlan
	}
	if ctx == nil ||
		(focus != CollectAll && focus != CollectGatewayKey) ||
		(focus == CollectGatewayKey && resume == nil) ||
		(focus == CollectAll && discover == nil) ||
		nilInterface(prompt) || present == nil {
		return result(PlanningResult{}, 0), ErrPlan
	}
	if resume != nil && len(working.Providers) == 0 {
		return result(PlanningResult{}, 0), ErrPlan
	}
	source.Bytes = append([]byte(nil), source.Bytes...)
	if source.Exists && len(source.Bytes) == 0 ||
		!source.Exists && len(source.Bytes) != 0 {
		return result(PlanningResult{}, 0), ErrPlan
	}
	if source.Exists {
		if existing == nil {
			return result(PlanningResult{}, 0), ErrPlan
		}
		decoded, err := config.Decode(bytes.NewReader(source.Bytes))
		if err != nil {
			return result(PlanningResult{}, 0), ErrPlan
		}
		decoded = cloneConfig(decoded)
		normalizedExisting := cloneConfig(*existing)
		if !reflect.DeepEqual(decoded, normalizedExisting) {
			return result(PlanningResult{}, 0), ErrPlan
		}
		existing = &normalizedExisting
	} else if existing != nil {
		return result(PlanningResult{}, 0), ErrPlan
	}
	if err := ctx.Err(); err != nil {
		return result(PlanningResult{}, 0), err
	}

	selectionNeeded := len(working.Providers) == 0 ||
		(resume != nil && focus == CollectAll)
	selectionLocked := resume == nil && len(explicit.Providers) != 0

interactionLoop:
	for {
		if focus == CollectGatewayKey {
			if err := ctx.Err(); err != nil {
				return result(PlanningResult{}, 0), err
			}
			collected, err := prompt.Collect(ctx, CollectRequest{
				Initial:  cloneOptions(working),
				Existing: cloneConfigPointer(existing),
			})
			if err != nil {
				return result(PlanningResult{}, 0), promptInteractionError(ctx, err)
			}
			if err := ctx.Err(); err != nil {
				return result(PlanningResult{}, 0), err
			}
			if collected.BackToSelection {
				return result(PlanningResult{}, 0), ErrPlan
			}
			// A nil Discovery map is the frozen Prompt seam's key-only focus
			// convention. Enforce that contract at the resolver boundary even if
			// a faulty adapter returns changes for hidden provider/model groups.
			working.Gateway = collected.Options.Gateway
			if err := ValidateOptions(working); err != nil {
				return result(PlanningResult{}, 0), ErrPlan
			}
		} else {
			for {
				if selectionNeeded {
					if err := ctx.Err(); err != nil {
						return result(PlanningResult{}, 0), err
					}
					selection, err := prompt.SelectProviders(ctx, ProviderSelectionRequest{
						Initial:  append([]core.ProviderName(nil), working.Providers...),
						Existing: existingProviderSelection(existing),
					})
					if err != nil {
						return result(PlanningResult{}, 0), promptInteractionError(ctx, err)
					}
					if err := ctx.Err(); err != nil {
						return result(PlanningResult{}, 0), err
					}
					switch selection.Decision {
					case ReviewConfirm:
						if len(selection.Providers) == 0 {
							return result(PlanningResult{}, 0), ErrPlan
						}
						if _, ok := uniqueProviders(selection.Providers); !ok {
							return result(PlanningResult{}, 0), ErrPlan
						}
						working = optionsForSelection(working, selection.Providers)
					case ReviewBack:
						if len(selection.Providers) != 0 {
							return result(PlanningResult{}, 0), ErrPlan
						}
						if len(working.Providers) == 0 {
							// Back from the first screen is a closed, no-change exit.
							return result(PlanningResult{}, ReviewDecline), nil
						}
					case ReviewDecline:
						return result(PlanningResult{}, 0), ErrPlan
					default:
						return result(PlanningResult{}, 0), ErrPlan
					}
				}

				if err := ctx.Err(); err != nil {
					return result(PlanningResult{}, 0), err
				}
				discovery, err := discover(ctx, cloneOptions(working))
				if err != nil {
					return result(PlanningResult{}, 0), dependencyInteractionError(ctx, err)
				}
				if err := ctx.Err(); err != nil {
					return result(PlanningResult{}, 0), err
				}
				collected, err := prompt.Collect(ctx, CollectRequest{
					Initial:   cloneOptions(working),
					Existing:  cloneConfigPointer(existing),
					Discovery: cloneDiscovery(discovery),
				})
				if err != nil {
					return result(PlanningResult{}, 0), promptInteractionError(ctx, err)
				}
				if err := ctx.Err(); err != nil {
					return result(PlanningResult{}, 0), err
				}
				candidate := collectCandidate(
					collected.Options, working, explicit, resume == nil,
				)
				if candidate.NonInteractive || ValidateOptions(candidate) != nil ||
					len(candidate.Providers) == 0 {
					return result(PlanningResult{}, 0), ErrPlan
				}
				if !providerSlicesEqual(candidate.Providers, working.Providers) {
					return result(PlanningResult{}, 0), ErrPlan
				}
				working = candidate
				if collected.BackToSelection {
					selectionNeeded = !selectionLocked
					continue
				}
				break
			}
		}

		planning, err := planCollectedOptions(
			working, source, existing, defaultRuntimeRoot, defaultKeyPath,
		)
		if contextErr := ctx.Err(); contextErr != nil {
			return result(planning, 0), contextErr
		}
		if err == nil {
			return result(planning, ReviewConfirm), nil
		}
		if !errors.Is(err, ErrCollision) {
			return result(PlanningResult{}, 0), err
		}

		if err := present(cloneMergePlan(planning.Merge).Diff); err != nil {
			if ctx.Err() != nil {
				return result(planning, 0), ctx.Err()
			}
			return result(planning, 0), ErrPlan
		}
		if err := ctx.Err(); err != nil {
			return result(planning, 0), err
		}
		review, err := prompt.Review(ctx, ReviewRequest{
			Diff:       cloneMergePlan(planning.Merge).Diff,
			Collisions: cloneMergePlan(planning.Merge).Collisions,
		})
		if err != nil {
			return result(planning, 0), promptInteractionError(ctx, err)
		}
		if err := ctx.Err(); err != nil {
			return result(planning, 0), err
		}
		switch review.Decision {
		case ReviewBack:
			if len(review.Collisions) != 0 {
				return result(planning, 0), ErrPlan
			}
			selectionNeeded = !selectionLocked
			continue interactionLoop
		case ReviewDecline:
			if len(review.Collisions) != 0 {
				return result(planning, 0), ErrPlan
			}
			return result(planning, ReviewDecline), nil
		case ReviewConfirm:
			if err := applyCollisionDecisions(
				&working, planning.Merge.Collisions, review.Collisions,
			); err != nil {
				return result(planning, 0), err
			}
		default:
			return result(planning, 0), ErrPlan
		}

		postDecision, postDecisionErr := planCollectedOptions(
			working, source, existing, defaultRuntimeRoot, defaultKeyPath,
		)
		if contextErr := ctx.Err(); contextErr != nil {
			return result(postDecision, 0), contextErr
		}
		converged, err := requireConvergedInteractivePlanning(
			postDecision, postDecisionErr,
		)
		if err != nil {
			return result(converged, 0), ErrPlan
		}
		return result(converged, ReviewConfirm), nil
	}
}

// ConfirmInteractive presents and confirms a completed interactive plan.
func ConfirmInteractive(
	ctx context.Context,
	plan PlanningResult,
	prompt Prompt,
	present DiffPresenter,
) (ReviewDecision, error) {
	if ctx == nil || nilInterface(prompt) || present == nil {
		return 0, ErrPlan
	}
	cloned := clonePlanningResult(plan)
	if !validFinalPlanningResult(cloned) {
		return 0, ErrPlan
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if err := present(cloneMergePlan(cloned.Merge).Diff); err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		return 0, ErrPlan
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	response, err := prompt.Review(ctx, ReviewRequest{
		Diff: cloneMergePlan(cloned.Merge).Diff,
	})
	if err != nil {
		return 0, promptInteractionError(ctx, err)
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(response.Collisions) != 0 {
		return 0, ErrPlan
	}
	switch response.Decision {
	case ReviewConfirm, ReviewBack, ReviewDecline:
		return response.Decision, nil
	default:
		return 0, ErrPlan
	}
}

func validFinalPlanningResult(plan PlanningResult) bool {
	if ValidateDesiredState(plan.Desired) != nil || len(plan.Merge.Candidate) == 0 {
		return false
	}
	decoded, err := config.Decode(bytes.NewReader(plan.Merge.Candidate))
	if err != nil || !reflect.DeepEqual(decoded, plan.Merge.Config) ||
		!selectedModelsPresent(decoded, plan.Desired.SelectedProviders) {
		return false
	}
	entries, ok := validatedDiffEntries(plan.Merge.Diff.Entries)
	if !ok || len(entries) == 0 ||
		!finalDiffMatchesPlan(entries, plan.Desired, decoded) {
		return false
	}
	fresh := finalPlanIsFresh(entries, plan.Desired, decoded)
	if !completeFinalDiffShapes(entries, fresh, decoded) ||
		fresh && decoded.Runtime.Root != plan.Desired.NewRuntimeRoot {
		return false
	}
	if !finalCollisionsConverged(entries, plan.Merge.Collisions, plan.Desired) {
		return false
	}
	changed := false
	for _, entry := range entries {
		if entry.Kind == DiffAdded || entry.Kind == DiffReplaced {
			changed = true
			break
		}
	}
	if changed != plan.Merge.Changed ||
		!finalDesiredMatchesConfig(plan.Desired, decoded) ||
		!validFinalKeyPlan(plan.Merge, entries) {
		return false
	}
	return true
}

func finalPlanIsFresh(
	entries []DiffEntry,
	desired DesiredState,
	cfg config.Config,
) bool {
	if !desired.Gateway.Set || len(cfg.Providers) != len(desired.Providers) ||
		len(cfg.Models) != len(desired.Models) {
		return false
	}
	for _, patch := range desired.Providers {
		if _, ok := cfg.Providers[string(patch.Name)]; !ok {
			return false
		}
	}
	for _, mapping := range desired.Models {
		if _, ok := finalModel(mapping.ID, cfg.Models); !ok {
			return false
		}
	}
	for _, entry := range entries {
		if entry.Target == DiffGatewayAuth {
			return entry.Kind == DiffAdded
		}
	}
	return false
}

func completeFinalDiffShapes(
	entries []DiffEntry,
	fresh bool,
	cfg config.Config,
) bool {
	for _, entry := range entries {
		switch entry.Target {
		case DiffGatewayAuth:
			if !completeGatewayDiffShape(entry, fresh, cfg.Server) {
				return false
			}
		case DiffProvider:
			if !completeProviderDiffShape(entry, fresh) {
				return false
			}
		case DiffModel:
			if !completeModelDiffShape(entry, fresh) {
				return false
			}
		default:
			return false
		}
	}
	return true
}

func completeGatewayDiffShape(entry DiffEntry, fresh bool, server config.Server) bool {
	switch entry.Kind {
	case DiffAdded:
		if !fresh {
			return false
		}
		names := []string{"mode"}
		if server.APIKeyFile != "" {
			names = append(names, "key_file")
		} else if server.APIKeyEnv != "" {
			names = append(names, "key_env")
		}
		return hasExactDiffFields(entry.Fields, names...) &&
			allDiffFieldsHaveEmptyBefore(entry.Fields)
	case DiffUnchanged:
		return !fresh && hasExactDiffFields(entry.Fields, "mode") &&
			entry.Fields[0].Before == entry.Fields[0].After
	case DiffReplaced:
		if fresh || !hasDiffField(entry.Fields, "mode") {
			return false
		}
		keySourceChanged := false
		for _, field := range entry.Fields {
			if field.Name == "key_file" || field.Name == "key_env" {
				if field.Before == field.After {
					return false
				}
				keySourceChanged = true
			}
		}
		if !keySourceChanged ||
			(server.APIKeyFile != "" && !hasDiffField(entry.Fields, "key_file")) ||
			(server.APIKeyEnv != "" && !hasDiffField(entry.Fields, "key_env")) {
			return false
		}
		return true
	default:
		return false
	}
}

func completeProviderDiffShape(entry DiffEntry, fresh bool) bool {
	switch entry.Kind {
	case DiffAdded:
		if fresh {
			return len(entry.Fields) == 0
		}
		return hasExactDiffFields(
			entry.Fields, "executable", "prefix_args", "config_home", "credential_env",
		) && allDiffFieldsHaveEmptyBefore(entry.Fields)
	case DiffUnchanged:
		return !fresh && len(entry.Fields) == 0
	case DiffReplaced:
		return !fresh && nonemptyChangedDiffFields(entry.Fields)
	default:
		return false
	}
}

func completeModelDiffShape(entry DiffEntry, fresh bool) bool {
	switch entry.Kind {
	case DiffAdded:
		return hasExactDiffFields(entry.Fields, "provider", "provider_model") &&
			allDiffFieldsHaveEmptyBefore(entry.Fields)
	case DiffUnchanged:
		return !fresh && len(entry.Fields) == 0
	case DiffReplaced:
		return !fresh && nonemptyChangedDiffFields(entry.Fields)
	default:
		return false
	}
}

func hasExactDiffFields(fields []DiffField, names ...string) bool {
	if len(fields) != len(names) {
		return false
	}
	wanted := make(map[string]struct{}, len(names))
	for _, name := range names {
		wanted[name] = struct{}{}
	}
	for _, field := range fields {
		if _, ok := wanted[field.Name]; !ok {
			return false
		}
	}
	return true
}

func hasDiffField(fields []DiffField, name string) bool {
	for _, field := range fields {
		if field.Name == name {
			return true
		}
	}
	return false
}

func allDiffFieldsHaveEmptyBefore(fields []DiffField) bool {
	for _, field := range fields {
		if field.Before != "" {
			return false
		}
	}
	return true
}

func nonemptyChangedDiffFields(fields []DiffField) bool {
	if len(fields) == 0 {
		return false
	}
	for _, field := range fields {
		if field.Before == field.After {
			return false
		}
	}
	return true
}

func finalDiffMatchesPlan(
	entries []DiffEntry,
	desired DesiredState,
	cfg config.Config,
) bool {
	expected := make(map[collisionKey]struct{}, len(desired.Providers)+len(desired.Models)+1)
	if desired.Gateway.Set {
		expected[collisionKey{target: DiffGatewayAuth, name: "gateway"}] = struct{}{}
	}
	for _, provider := range desired.Providers {
		expected[collisionKey{target: DiffProvider, name: string(provider.Name)}] = struct{}{}
	}
	for _, model := range desired.Models {
		expected[collisionKey{target: DiffModel, name: model.ID}] = struct{}{}
	}
	if len(entries) != len(expected) {
		return false
	}
	for _, entry := range entries {
		key := collisionKey{target: entry.Target, name: entry.Name}
		if _, ok := expected[key]; !ok || !diffAfterMatchesConfig(entry, cfg) {
			return false
		}
	}
	return true
}

func diffAfterMatchesConfig(entry DiffEntry, cfg config.Config) bool {
	for _, field := range entry.Fields {
		var expected string
		var ok bool
		switch entry.Target {
		case DiffGatewayAuth:
			switch field.Name {
			case "mode":
				expected, _ = gatewayMode(cfg.Server)
				ok = true
			case "key_file":
				expected, ok = cfg.Server.APIKeyFile, true
			case "key_env":
				expected, ok = cfg.Server.APIKeyEnv, true
			}
		case DiffProvider:
			provider, exists := cfg.Providers[entry.Name]
			if !exists {
				return false
			}
			switch field.Name {
			case "executable":
				expected, ok = provider.Executable, true
			case "prefix_args":
				expected, ok = strings.Join(provider.PrefixArgs, ","), true
			case "config_home":
				expected, ok = provider.ConfigHome, true
			case "credential_env":
				expected, ok = strings.Join(provider.CredentialEnv, ","), true
			}
		case DiffModel:
			model, exists := finalModel(entry.Name, cfg.Models)
			if !exists {
				return false
			}
			switch field.Name {
			case "provider":
				expected, ok = model.Provider, true
			case "provider_model":
				expected, ok = model.ProviderModel, true
			}
		}
		if !ok || field.After != expected {
			return false
		}
	}
	return true
}

func finalCollisionsConverged(
	entries []DiffEntry,
	collisions []Collision,
	desired DesiredState,
) bool {
	if hasUnauthorizedCollision(collisions, desired) {
		return false
	}
	diffByTarget := make(map[collisionKey]DiffEntry, len(entries))
	for _, entry := range entries {
		diffByTarget[collisionKey{target: entry.Target, name: entry.Name}] = entry
	}
	seen := make(map[collisionKey]struct{}, len(collisions))
	for _, collision := range collisions {
		key := collisionKey{target: collision.Target, name: collision.Name}
		entry, ok := diffByTarget[key]
		if !ok || entry.Kind != DiffReplaced ||
			(collision.Target != DiffProvider && collision.Target != DiffModel) {
			return false
		}
		if _, duplicate := seen[key]; duplicate {
			return false
		}
		seen[key] = struct{}{}
		normalized, ok := validatedDiffEntries([]DiffEntry{{
			Kind: entry.Kind, Target: collision.Target,
			Name: collision.Name, Fields: cloneDiffFields(collision.Fields),
		}})
		if !ok || len(normalized) != 1 ||
			!reflect.DeepEqual(normalized[0].Fields, entry.Fields) {
			return false
		}
	}
	for _, entry := range entries {
		if entry.Kind != DiffReplaced ||
			(entry.Target != DiffProvider && entry.Target != DiffModel) {
			continue
		}
		if _, ok := seen[collisionKey{target: entry.Target, name: entry.Name}]; !ok {
			return false
		}
	}
	return true
}

func finalDesiredMatchesConfig(desired DesiredState, cfg config.Config) bool {
	if desired.Gateway.Set &&
		(cfg.Server.APIKeyFile != desired.Gateway.APIKeyFile ||
			cfg.Server.APIKeyEnv != desired.Gateway.APIKeyEnv) {
		return false
	}
	for _, patch := range desired.Providers {
		provider, ok := cfg.Providers[string(patch.Name)]
		if !ok || provider.Executable != patch.Command.Value.Executable ||
			!reflect.DeepEqual(provider.PrefixArgs, patch.Command.Value.PrefixArgs) ||
			provider.ConfigHome != patch.ConfigHome.Value ||
			!reflect.DeepEqual(provider.CredentialEnv, patch.CredentialEnv.Value) {
			return false
		}
	}
	for _, mapping := range desired.Models {
		model, ok := finalModel(mapping.ID, cfg.Models)
		if !ok || model.Provider != string(mapping.Provider) ||
			model.ProviderModel != mapping.ProviderModel {
			return false
		}
	}
	return true
}

func finalModel(name string, models []config.Model) (config.Model, bool) {
	for _, model := range models {
		if model.ID == name {
			return model, true
		}
	}
	return config.Model{}, false
}

func validFinalKeyPlan(
	merge MergePlan,
	entries []DiffEntry,
) bool {
	keyPath := merge.Config.Server.APIKeyFile
	if keyPath == "" {
		return merge.KeyAction == KeyActionNone && merge.KeyPath == "" &&
			!merge.KeyAllowExisting
	}
	if merge.KeyPath != keyPath {
		return false
	}
	newPath := false
	for _, entry := range entries {
		if entry.Target != DiffGatewayAuth ||
			(entry.Kind != DiffAdded && entry.Kind != DiffReplaced) {
			continue
		}
		for _, field := range entry.Fields {
			if field.Name == "key_file" && field.After == keyPath {
				newPath = true
				break
			}
		}
	}
	if newPath {
		return merge.KeyAction == KeyActionEnsure
	}
	return (merge.KeyAction == KeyActionInspect || merge.KeyAction == KeyActionEnsure) &&
		!merge.KeyAllowExisting
}

func existingProviderSelection(existing *config.Config) []core.ProviderName {
	if existing == nil {
		return nil
	}
	order := []core.ProviderName{
		core.ProviderCodex,
		core.ProviderClaude,
		core.ProviderGemini,
	}
	selected := make([]core.ProviderName, 0, len(existing.Providers))
	for _, name := range order {
		if _, ok := existing.Providers[string(name)]; ok {
			selected = append(selected, name)
		}
	}
	return selected
}

func optionsForSelection(options Options, providers []core.ProviderName) Options {
	selected := make(map[core.ProviderName]struct{}, len(providers))
	for _, name := range providers {
		selected[name] = struct{}{}
	}
	options.Providers = append([]core.ProviderName(nil), providers...)
	if options.Provider != nil {
		inputs := make(map[core.ProviderName]ProviderInput, len(providers))
		for _, name := range providers {
			if input, ok := options.Provider[name]; ok {
				inputs[name] = input
			}
		}
		options.Provider = inputs
	}
	models := options.Models[:0]
	keptAliases := make(map[string]struct{}, len(options.Models))
	for _, model := range options.Models {
		if _, ok := selected[model.Provider]; ok {
			models = append(models, model)
			keptAliases[model.ID] = struct{}{}
		}
	}
	options.Models = append([]ModelMapping(nil), models...)
	replaceProviders := options.ReplaceProviders[:0]
	for _, name := range options.ReplaceProviders {
		if _, ok := selected[name]; ok {
			replaceProviders = append(replaceProviders, name)
		}
	}
	options.ReplaceProviders = append([]core.ProviderName(nil), replaceProviders...)
	replaceModels := options.ReplaceModels[:0]
	for _, alias := range options.ReplaceModels {
		if _, ok := keptAliases[alias]; ok {
			replaceModels = append(replaceModels, alias)
		}
	}
	options.ReplaceModels = append([]string(nil), replaceModels...)
	return options
}

func collectCandidate(
	response Options,
	current Options,
	explicit Options,
	initialCall bool,
) Options {
	candidate := cloneOptions(response)
	// Prompt collection never grants or revokes replacement authority. Those
	// decisions come only from CLI input already in current state or from the
	// exact collision review handled by applyCollisionDecisions.
	candidate.ReplaceProviders = append(
		[]core.ProviderName(nil), current.ReplaceProviders...,
	)
	candidate.ReplaceModels = append([]string(nil), current.ReplaceModels...)
	if !initialCall {
		return candidate
	}

	candidate.ConfigPath = explicit.ConfigPath
	candidate.NonInteractive = explicit.NonInteractive
	candidate.DryRun = explicit.DryRun
	if len(explicit.Providers) != 0 {
		candidate = optionsForSelection(candidate, explicit.Providers)
	}
	if len(explicit.Provider) != 0 {
		if candidate.Provider == nil {
			candidate.Provider = make(map[core.ProviderName]ProviderInput)
		}
		for name, explicitInput := range explicit.Provider {
			input := candidate.Provider[name]
			if explicitInput.Executable.Set {
				input.Executable = explicitInput.Executable
			}
			if explicitInput.Entrypoint.Set {
				input.Entrypoint = explicitInput.Entrypoint
			}
			if explicitInput.ConfigHome.Set {
				input.ConfigHome = explicitInput.ConfigHome
			}
			if explicitInput.AuthSet {
				input.Auth = explicitInput.Auth
				input.AuthSet = true
			}
			candidate.Provider[name] = input
		}
	}
	candidate.Models = modelsWithExplicitPrecedence(candidate.Models, explicit.Models)
	if explicit.Gateway.AuthSet {
		candidate.Gateway.Auth = explicit.Gateway.Auth
		candidate.Gateway.AuthSet = true
	}
	if explicit.Gateway.KeyFile.Set {
		candidate.Gateway.KeyFile = explicit.Gateway.KeyFile
	}
	if explicit.Gateway.KeyEnv.Set {
		candidate.Gateway.KeyEnv = explicit.Gateway.KeyEnv
	}
	// Explicit model mappings may have been restored after provider pruning, so
	// restore the already-authorized names last as well.
	candidate.ReplaceProviders = append(
		[]core.ProviderName(nil), current.ReplaceProviders...,
	)
	candidate.ReplaceModels = append([]string(nil), current.ReplaceModels...)
	return candidate
}

func modelsWithExplicitPrecedence(
	collected []ModelMapping,
	explicit []ModelMapping,
) []ModelMapping {
	explicitAliases := make(map[string]struct{}, len(explicit))
	result := append([]ModelMapping(nil), explicit...)
	for _, model := range explicit {
		explicitAliases[model.ID] = struct{}{}
	}
	for _, model := range collected {
		if _, fixed := explicitAliases[model.ID]; !fixed {
			result = append(result, model)
		}
	}
	return result
}

func providerSlicesEqual(left, right []core.ProviderName) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func planCollectedOptions(
	options Options,
	source Source,
	existing *config.Config,
	defaultRuntimeRoot string,
	defaultKeyPath string,
) (PlanningResult, error) {
	resolved := cloneOptions(options)
	resolved.NonInteractive = true
	desired, err := ResolveNonInteractive(
		resolved, existing, defaultRuntimeRoot, defaultKeyPath,
	)
	if err != nil {
		return PlanningResult{}, ErrPlan
	}
	merge, err := PlanMerge(source.Bytes, source.Exists, desired)
	return clonePlanningResult(PlanningResult{Desired: desired, Merge: merge}), err
}

func requireConvergedInteractivePlanning(
	plan PlanningResult,
	err error,
) (PlanningResult, error) {
	cloned := clonePlanningResult(plan)
	if err != nil {
		return cloned, ErrPlan
	}
	return cloned, nil
}

type collisionKey struct {
	target DiffTarget
	name   string
}

func applyCollisionDecisions(
	options *Options,
	collisions []Collision,
	decisions []CollisionDecision,
) error {
	if options == nil || len(decisions) != len(collisions) {
		return ErrPlan
	}
	pending := make(map[collisionKey]struct{}, len(collisions))
	for _, collision := range collisions {
		key := collisionKey{target: collision.Target, name: collision.Name}
		if collision.Target != DiffProvider && collision.Target != DiffModel ||
			!safeText(collision.Name) {
			return ErrPlan
		}
		if _, duplicate := pending[key]; duplicate {
			return ErrPlan
		}
		pending[key] = struct{}{}
	}
	seen := make(map[collisionKey]struct{}, len(decisions))
	for _, decision := range decisions {
		key := collisionKey{target: decision.Target, name: decision.Name}
		if _, ok := pending[key]; !ok {
			return ErrPlan
		}
		if _, duplicate := seen[key]; duplicate {
			return ErrPlan
		}
		seen[key] = struct{}{}
		if decision.Choice != CollisionReplace &&
			decision.Choice != CollisionKeepExisting {
			return ErrPlan
		}
	}
	for _, decision := range decisions {
		if decision.Choice == CollisionReplace {
			authorizeCollision(options, decision)
		} else {
			keepExistingCollision(options, decision)
		}
	}
	if err := ValidateOptions(*options); err != nil {
		return ErrPlan
	}
	return nil
}

func authorizeCollision(options *Options, decision CollisionDecision) {
	switch decision.Target {
	case DiffGatewayAuth:
		// Gateway authentication collisions are rejected before this helper.
	case DiffProvider:
		name := core.ProviderName(decision.Name)
		if !containsProvider(options.ReplaceProviders, name) {
			options.ReplaceProviders = append(options.ReplaceProviders, name)
		}
	case DiffModel:
		if !containsString(options.ReplaceModels, decision.Name) {
			options.ReplaceModels = append(options.ReplaceModels, decision.Name)
		}
	}
}

func keepExistingCollision(options *Options, decision CollisionDecision) {
	switch decision.Target {
	case DiffGatewayAuth:
		// Gateway authentication collisions are rejected before this helper.
	case DiffProvider:
		name := core.ProviderName(decision.Name)
		if options.Provider == nil {
			options.Provider = make(map[core.ProviderName]ProviderInput)
		}
		options.Provider[name] = ProviderInput{}
		options.ReplaceProviders = removeProvider(options.ReplaceProviders, name)
	case DiffModel:
		models := make([]ModelMapping, 0, len(options.Models))
		for _, model := range options.Models {
			if model.ID != decision.Name {
				models = append(models, model)
			}
		}
		options.Models = models
		options.ReplaceModels = removeString(options.ReplaceModels, decision.Name)
	}
}

func containsProvider(values []core.ProviderName, wanted core.ProviderName) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func removeProvider(values []core.ProviderName, removed core.ProviderName) []core.ProviderName {
	result := make([]core.ProviderName, 0, len(values))
	for _, value := range values {
		if value != removed {
			result = append(result, value)
		}
	}
	return result
}

func removeString(values []string, removed string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value != removed {
			result = append(result, value)
		}
	}
	return result
}

func cloneConfigPointer(existing *config.Config) *config.Config {
	if existing == nil {
		return nil
	}
	cloned := cloneConfig(*existing)
	return &cloned
}

func cloneDiscovery(
	discovery map[core.ProviderName]ProviderDiscovery,
) map[core.ProviderName]ProviderDiscovery {
	if discovery == nil {
		return nil
	}
	cloned := make(map[core.ProviderName]ProviderDiscovery, len(discovery))
	for name, providerDiscovery := range discovery {
		providerDiscovery.Commands = append(
			[]CommandCandidate(nil), providerDiscovery.Commands...,
		)
		for index := range providerDiscovery.Commands {
			providerDiscovery.Commands[index].Command.PrefixArgs = append(
				[]string(nil), providerDiscovery.Commands[index].Command.PrefixArgs...,
			)
		}
		providerDiscovery.ConfigHomes = append(
			[]PathCandidate(nil), providerDiscovery.ConfigHomes...,
		)
		providerDiscovery.AuthChoices = append(
			[]AuthID(nil), providerDiscovery.AuthChoices...,
		)
		cloned[name] = providerDiscovery
	}
	return cloned
}

func promptInteractionError(ctx context.Context, err error) error {
	var restoreErr terminalRestoreError
	if errors.As(err, &restoreErr) {
		return ErrPlan
	}
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	if errors.Is(err, io.EOF) {
		return context.Canceled
	}
	return ErrPlan
}

func dependencyInteractionError(ctx context.Context, err error) error {
	if ctx != nil && ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrPlan
}
