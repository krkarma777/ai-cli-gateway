package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"sort"
	"strings"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/configstore"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/doctor"
	"github.com/krkarma777/ai-cli-gateway/internal/gatewaykey"
	"github.com/krkarma777/ai-cli-gateway/internal/initconfig"
)

// InitOutcome is the closed result classification consumed by the CLI.
type InitOutcome uint8

const (
	// InitReady means the saved or current setup passed post-write diagnosis.
	InitReady InitOutcome = iota + 1
	// InitNotReady means setup state is known but selected readiness failed.
	InitNotReady
	// InitDeclined means an interactive confirmation was declined.
	InitDeclined
	// InitDryRun means read-only planning and preflight completed.
	InitDryRun
	// InitUsage means input or replacement authority was incomplete.
	InitUsage
	// InitFailed means an operational step failed with a known state.
	InitFailed
	// InitRecoveryRequired means the prior backup state needs manual inspection.
	InitRecoveryRequired
	// InitCanceled means cancellation stopped setup before or after publication.
	InitCanceled
)

// InitResult reports the outcome and whether configuration publication was proved.
type InitResult struct {
	Outcome InitOutcome
	Saved   bool
}

// Streams preserves the caller-owned init input and output boundaries.
type Streams struct {
	In  io.Reader
	Out io.Writer
	Err io.Writer
}

// InitStore owns the read-only and transactional configuration boundary.
type InitStore interface {
	Load(context.Context, string) (configstore.Snapshot, error)
	Preflight(context.Context, configstore.Mutation) (configstore.PreflightResult, error)
	Commit(context.Context, configstore.Mutation, []byte) (configstore.CommitResult, error)
}

type initDiagnose func(context.Context, string, Dependencies) (doctor.Diagnosis, error)

// InitDependencies contains the process-local effects used by Init.
type InitDependencies struct {
	Runtime                Dependencies
	Store                  InitStore
	Entropy                io.Reader
	DefaultInitRuntimeRoot func() (string, error)
	Discover               func(context.Context, string, initconfig.Options, *config.Config, initconfig.DiscoveryDependencies) (map[core.ProviderName]initconfig.ProviderDiscovery, error)
	Discovery              initconfig.DiscoveryDependencies
	IsTerminal             func(io.Reader, io.Writer) bool
	NewPrompt              func(io.Reader, io.Writer, bool) initconfig.Prompt
	LookupEnv              func(string) (string, bool)

	diagnose initDiagnose
}

// ProductionInitDependencies constructs lazy production Init dependencies.
func ProductionInitDependencies(logWriter io.Writer) InitDependencies {
	return InitDependencies{
		Runtime:                ProductionDependencies(logWriter),
		Store:                  configstore.NewWriter(),
		Entropy:                rand.Reader,
		DefaultInitRuntimeRoot: config.DefaultInitRuntimeRoot,
		Discover:               initconfig.DiscoverProviders,
		Discovery: initconfig.DiscoveryDependencies{
			LookupEnv:   os.LookupEnv,
			LookPath:    exec.LookPath,
			UserHomeDir: os.UserHomeDir,
			AbsPath:     filepath.Abs,
		},
		IsTerminal: initconfig.IsInteractiveTerminal,
		NewPrompt:  initconfig.NewTerminalPrompt,
		LookupEnv:  os.LookupEnv,
		diagnose:   diagnose,
	}
}

// Init plans, preflights, confirms when interactive, commits, and diagnoses setup.
func Init(
	ctx context.Context,
	options initconfig.Options,
	streams Streams,
	deps InitDependencies,
) InitResult {
	stdout := streams.Out
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		if !nilLike(stdout) {
			writeFixed(stdout, "setup_not_saved\n")
		}
		return InitResult{Outcome: InitCanceled}
	}
	if nilLike(stdout) {
		return InitResult{Outcome: InitFailed}
	}
	if !options.NonInteractive {
		if nilLike(streams.In) || nilLike(streams.Err) || deps.IsTerminal == nil {
			writeFixed(stdout, "setup_failed\n")
			return InitResult{Outcome: InitFailed}
		}
		if !deps.IsTerminal(streams.In, streams.Err) {
			writeFixed(streams.Err, "init_requires_non_interactive: pass --non-interactive and all required flags\n")
			return InitResult{Outcome: InitUsage}
		}
	}
	if nilLike(deps.Store) {
		writeFixed(stdout, "setup_failed\n")
		return InitResult{Outcome: InitFailed}
	}

	snapshot, err := deps.Store.Load(ctx, options.ConfigPath)
	if err != nil {
		return initLoadFailure(ctx, stdout, err)
	}
	defaultRuntimeRoot, err := initRuntimeRoot(snapshot, deps.DefaultInitRuntimeRoot)
	if err != nil {
		return initFailure(ctx, stdout, err, false)
	}
	defaultKeyPath := filepath.Join(filepath.Dir(snapshot.Path()), "gateway.key")
	source := initconfig.Source{Bytes: snapshot.Bytes(), Exists: snapshot.Exists()}
	var (
		planning              initconfig.PlanningResult
		prompt                initconfig.Prompt
		resume                *initconfig.ResumeState
		existing              *config.Config
		discover              initconfig.DiscoverSelected
		planInteractive       func(*initconfig.ResumeState, initconfig.CollectFocus) (initconfig.InteractiveResult, error)
		interactive           = !options.NonInteractive
		previewAlreadyWritten bool
	)
	present := func(diff initconfig.SemanticDiff) error {
		_, writeErr := diff.WriteTo(stdout)
		return writeErr
	}
	if interactive {
		if deps.Discover == nil || deps.NewPrompt == nil || deps.LookupEnv == nil {
			writeFixed(stdout, "setup_failed\n")
			return InitResult{Outcome: InitFailed}
		}
		var decodeErr error
		existing, decodeErr = initExistingConfig(snapshot)
		if decodeErr != nil {
			return initPlanningFailure(stdout, decodeErr)
		}
		accessible := initAccessible(deps.LookupEnv)
		prompt = deps.NewPrompt(streams.In, streams.Err, accessible)
		discover = func(ctx context.Context, selected initconfig.Options) (map[core.ProviderName]initconfig.ProviderDiscovery, error) {
			return deps.Discover(
				ctx, snapshot.Path(), selected, existing, deps.Discovery,
			)
		}
		planInteractive = func(
			resumeState *initconfig.ResumeState,
			focus initconfig.CollectFocus,
		) (initconfig.InteractiveResult, error) {
			return initconfig.PlanInteractive(
				ctx,
				options,
				resumeState,
				source,
				existing,
				discover,
				prompt,
				present,
				focus,
				defaultRuntimeRoot,
				defaultKeyPath,
			)
		}
		interactiveResult, planErr := planInteractive(nil, initconfig.CollectAll)
		planning = interactiveResult.Plan
		resume = interactiveResult.Resume
		if planErr != nil {
			return initInteractiveFailure(ctx, stdout, planErr)
		}
		if interactiveResult.Decision == initconfig.ReviewDecline {
			return InitResult{Outcome: InitDeclined}
		}
	} else {
		planning, err = initconfig.PlanNonInteractive(
			options,
			source,
			defaultRuntimeRoot,
			defaultKeyPath,
		)
	}
	if err != nil {
		if errors.Is(err, initconfig.ErrCollision) {
			if _, writeErr := planning.Merge.Diff.WriteTo(stdout); writeErr != nil {
				return InitResult{Outcome: InitFailed}
			}
			writeFixed(stdout, "replacement_not_authorized\n")
			return InitResult{Outcome: InitUsage}
		}
		return initPlanningFailure(stdout, err)
	}

	var (
		mutation    configstore.Mutation
		runtimeDeps Dependencies
		preflight   configstore.PreflightResult
	)

reviewPlanning:
	for {
		for {
			mutation, runtimeDeps, err = buildInitMutation(snapshot, planning, deps.Runtime)
			if err != nil {
				return initFailure(ctx, stdout, err, false)
			}
			preflight, err = deps.Store.Preflight(ctx, mutation)
			if err != nil {
				if errors.Is(err, configstore.ErrInvalidConfig) {
					writeFixed(stdout, "configuration_invalid\n")
					return InitResult{Outcome: InitUsage}
				}
				return initFailure(ctx, stdout, err, false)
			}
			if !validInitPreflight(planning.Merge.KeyAction, preflight.KeyState) {
				writeFixed(stdout, "setup_failed\n")
				return InitResult{Outcome: InitFailed}
			}
			if planning.Merge.KeyAction == initconfig.KeyActionEnsure &&
				preflight.KeyState == configstore.KeyStateNeedsConfirmation &&
				planning.Merge.KeyAllowExisting {
				writeFixed(stdout, "setup_failed\n")
				return InitResult{Outcome: InitFailed}
			}
			if !interactive {
				break
			}

			confirmationKind := initconfig.KeyConfirmationKind(0)
			switch {
			case planning.Merge.KeyAction == initconfig.KeyActionEnsure &&
				preflight.KeyState == configstore.KeyStateNeedsConfirmation &&
				!planning.Merge.KeyAllowExisting:
				confirmationKind = initconfig.ConfirmOrphanReuse
			case planning.Merge.KeyAction == initconfig.KeyActionInspect &&
				preflight.KeyState == configstore.KeyStateMissing:
				confirmationKind = initconfig.ConfirmMissingConfiguredKeyCreation
			}
			if confirmationKind == 0 {
				break
			}
			decision, confirmationErr := prompt.ConfirmKeyAction(
				ctx,
				initconfig.KeyConfirmationRequest{
					Kind: confirmationKind,
					Path: planning.Merge.KeyPath,
				},
			)
			if confirmationErr != nil {
				return initInteractiveFailure(ctx, stdout, confirmationErr)
			}
			if contextErr := ctx.Err(); contextErr != nil {
				return initInteractiveFailure(ctx, stdout, contextErr)
			}
			switch decision {
			case initconfig.ReviewConfirm:
				if confirmationKind == initconfig.ConfirmOrphanReuse {
					planning.Merge.KeyAllowExisting = true
				} else {
					planning.Merge.KeyAction = initconfig.KeyActionEnsure
				}
				continue
			case initconfig.ReviewDecline:
				return InitResult{Outcome: InitDeclined}
			case initconfig.ReviewBack:
				interactiveResult, planErr := planInteractive(
					resume, initconfig.CollectGatewayKey,
				)
				planning = interactiveResult.Plan
				resume = interactiveResult.Resume
				if planErr != nil {
					return initInteractiveFailure(ctx, stdout, planErr)
				}
				if interactiveResult.Decision == initconfig.ReviewDecline {
					return InitResult{Outcome: InitDeclined}
				}
				continue
			default:
				writeFixed(stdout, "setup_failed\n")
				return InitResult{Outcome: InitFailed}
			}
		}

		if interactive {
			decision, confirmErr := initconfig.ConfirmInteractive(
				ctx,
				planning,
				prompt,
				present,
			)
			if confirmErr != nil {
				return initInteractiveFailure(ctx, stdout, confirmErr)
			}
			switch decision {
			case initconfig.ReviewConfirm:
				previewAlreadyWritten = true
				break reviewPlanning
			case initconfig.ReviewDecline:
				return InitResult{Outcome: InitDeclined}
			case initconfig.ReviewBack:
				interactiveResult, planErr := planInteractive(
					resume, initconfig.CollectAll,
				)
				planning = interactiveResult.Plan
				resume = interactiveResult.Resume
				if planErr != nil {
					return initInteractiveFailure(ctx, stdout, planErr)
				}
				if interactiveResult.Decision == initconfig.ReviewDecline {
					return InitResult{Outcome: InitDeclined}
				}
				previewAlreadyWritten = false
				continue reviewPlanning
			default:
				writeFixed(stdout, "setup_failed\n")
				return InitResult{Outcome: InitFailed}
			}
		}
		break reviewPlanning
	}
	if !previewAlreadyWritten {
		if _, err := planning.Merge.Diff.WriteTo(stdout); err != nil {
			return InitResult{Outcome: InitFailed}
		}
	}
	if ctx.Err() != nil {
		writeFixed(stdout, "setup_not_saved\n")
		return InitResult{Outcome: InitCanceled}
	}
	if options.DryRun {
		if !writeRequired(stdout, "dry_run: no files changed; post-write doctor was not run\n") {
			return InitResult{Outcome: InitFailed}
		}
		return InitResult{Outcome: InitDryRun}
	}
	if preflight.KeyState == configstore.KeyStateNeedsConfirmation {
		writeFixed(stdout, "key_reuse_confirmation_required\n")
		return InitResult{Outcome: InitUsage}
	}

	var (
		commitResult configstore.CommitResult
		saved        bool
		needsCommit  = planning.Merge.Changed ||
			planning.Merge.KeyAction == initconfig.KeyActionEnsure &&
				preflight.KeyState == configstore.KeyStateMissing
		noop = !needsCommit
	)
	if needsCommit {
		var keyPayload []byte
		if planning.Merge.KeyAction == initconfig.KeyActionEnsure &&
			preflight.KeyState == configstore.KeyStateMissing {
			if nilLike(deps.Entropy) {
				writeFixed(stdout, "setup_failed\n")
				return InitResult{Outcome: InitFailed}
			}
			keyPayload, err = gatewaykey.Generate(deps.Entropy)
			if err != nil {
				writeFixed(stdout, "setup_failed\n")
				return InitResult{Outcome: InitFailed}
			}
		}
		if ctx.Err() != nil {
			clearInitBytes(keyPayload)
			writeFixed(stdout, "setup_not_saved\n")
			return InitResult{Outcome: InitCanceled}
		}
		commitResult, err = deps.Store.Commit(ctx, mutation, keyPayload)
		clearInitBytes(keyPayload)
		result, terminal := interpretInitCommit(ctx, stdout, commitResult, err)
		if terminal {
			return result
		}
		saved = true
	} else if err := ctx.Err(); err != nil {
		writeFixed(stdout, "setup_not_saved\n")
		return InitResult{Outcome: InitCanceled}
	}

	diagnoseFn := deps.diagnose
	if diagnoseFn == nil {
		diagnoseFn = diagnose
	}
	diagnosis, diagnoseErr := diagnoseFn(ctx, snapshot.Path(), runtimeDeps)
	if diagnoseErr != nil && !errors.Is(diagnoseErr, errDiagnosisSourceClose) {
		if ctx.Err() != nil {
			return initCanceledAfterDiagnosis(stdout, saved)
		}
		writeFixed(stdout, "post_write_doctor_failed\n")
		return InitResult{Outcome: InitFailed, Saved: saved}
	}
	report := diagnosis.Report()
	writeErr := doctor.WriteText(stdout, report)
	var closeErr error
	if diagnosis.RuntimeRoot != nil {
		if runtimeDeps.CloseRoot == nil {
			closeErr = ErrShutdown
		} else {
			closeErr = runtimeDeps.CloseRoot(diagnosis.RuntimeRoot)
		}
	}
	if writeErr != nil {
		return InitResult{Outcome: InitFailed, Saved: saved}
	}
	if ctx.Err() != nil {
		return initCanceledAfterDiagnosis(stdout, saved)
	}
	if diagnoseErr != nil || closeErr != nil {
		writeFixed(stdout, "post_write_doctor_failed\n")
		return InitResult{Outcome: InitFailed, Saved: saved}
	}

	ready := selectedProvidersReady(report, planning.Desired.SelectedProviders)
	completion := initCompletion{
		ConfigPath: snapshot.Path(),
		KeyPath:    planning.Merge.Config.Server.APIKeyFile,
		KeyEnv:     planning.Merge.Config.Server.APIKeyEnv,
		Listen:     planning.Merge.Config.Server.Listen,
		Saved:      saved,
		Noop:       noop,
		Ready:      ready,
	}
	if saved {
		completion.BackupPath = commitResult.BackupPath
	}
	if err := writeInitCompletion(stdout, completion); err != nil {
		return InitResult{Outcome: InitFailed, Saved: saved}
	}
	if !ready {
		return InitResult{Outcome: InitNotReady, Saved: saved}
	}
	return InitResult{Outcome: InitReady, Saved: saved}
}

func initExistingConfig(snapshot configstore.Snapshot) (*config.Config, error) {
	if !snapshot.Exists() {
		return nil, nil
	}
	decoded, err := config.Decode(bytes.NewReader(snapshot.Bytes()))
	if err != nil {
		return nil, initconfig.ErrPlan
	}
	return &decoded, nil
}

func initAccessible(lookup func(string) (string, bool)) bool {
	value, present := lookup("AI_CLI_GATEWAY_ACCESSIBLE")
	if present && value == "1" {
		return true
	}
	value, present = lookup("TERM")
	return present && value == "dumb"
}

func initRuntimeRoot(
	snapshot configstore.Snapshot,
	defaultRoot func() (string, error),
) (string, error) {
	if snapshot.Exists() {
		cfg, err := config.Decode(bytes.NewReader(snapshot.Bytes()))
		if err != nil || cfg.Runtime.Root == "" {
			return "", initconfig.ErrPlan
		}
		return cfg.Runtime.Root, nil
	}
	if defaultRoot == nil {
		return "", config.ErrDefaultPath
	}
	root, err := defaultRoot()
	if err != nil || root == "" {
		return "", config.ErrDefaultPath
	}
	return root, nil
}

func buildInitMutation(
	snapshot configstore.Snapshot,
	planning initconfig.PlanningResult,
	runtimeDeps Dependencies,
) (configstore.Mutation, Dependencies, error) {
	intent, ok := initKeyIntent(planning.Merge.KeyAction)
	if !ok {
		return configstore.Mutation{}, runtimeDeps, configstore.ErrStore
	}
	privateDirs, err := missingInitConfigHomes(planning.Desired.Providers)
	if err != nil {
		return configstore.Mutation{}, runtimeDeps, err
	}
	privateDirs, err = appendMissingInitDirectory(
		privateDirs,
		filepath.Dir(planning.Desired.NewRuntimeRoot),
	)
	if err != nil {
		return configstore.Mutation{}, runtimeDeps, err
	}
	if planning.Merge.KeyAction == initconfig.KeyActionEnsure &&
		filepath.Clean(filepath.Dir(planning.Merge.KeyPath)) !=
			filepath.Clean(filepath.Dir(snapshot.Path())) {
		privateDirs, err = appendMissingInitDirectory(
			privateDirs,
			filepath.Dir(planning.Merge.KeyPath),
		)
		if err != nil {
			return configstore.Mutation{}, runtimeDeps, err
		}
	}
	distinct := []string{snapshot.Path()}
	providerNames := make([]string, 0, len(planning.Merge.Config.Providers))
	for name := range planning.Merge.Config.Providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)
	for _, name := range providerNames {
		configured := planning.Merge.Config.Providers[name]
		distinct = appendDistinctInitPath(distinct, configured.Executable)
		if runtime.GOOS == "windows" &&
			strings.EqualFold(filepath.Base(configured.Executable), "node.exe") &&
			len(configured.PrefixArgs) == 1 {
			distinct = appendDistinctInitPath(distinct, configured.PrefixArgs[0])
		}
	}
	credentialPath, frozenRuntime := freezeInitVertexCredential(planning, runtimeDeps)
	if credentialPath != "" {
		distinct = appendDistinctInitPath(distinct, credentialPath)
	}
	keyPlan := configstore.KeyPlan{}
	if intent != configstore.KeyIntentNone {
		keyPlan = configstore.KeyPlan{
			Intent:        intent,
			Path:          planning.Merge.KeyPath,
			DistinctFrom:  distinct,
			AllowExisting: planning.Merge.KeyAllowExisting,
		}
	}
	return configstore.Mutation{
		Base:        snapshot,
		Candidate:   slices.Clone(planning.Merge.Candidate),
		Key:         keyPlan,
		PrivateDirs: privateDirs,
	}, frozenRuntime, nil
}

func initKeyIntent(action initconfig.KeyAction) (configstore.KeyIntent, bool) {
	switch action {
	case initconfig.KeyActionNone:
		return configstore.KeyIntentNone, true
	case initconfig.KeyActionInspect:
		return configstore.KeyIntentInspect, true
	case initconfig.KeyActionEnsure:
		return configstore.KeyIntentEnsure, true
	default:
		return configstore.KeyIntentNone, false
	}
}

func missingInitConfigHomes(providers []initconfig.ProviderPatch) ([]string, error) {
	result := make([]string, 0, len(providers))
	for _, providerPatch := range providers {
		var err error
		result, err = appendMissingInitDirectory(
			result,
			providerPatch.ConfigHome.Value,
		)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func appendMissingInitDirectory(paths []string, path string) ([]string, error) {
	_, err := os.Lstat(path)
	switch {
	case err == nil:
		return paths, nil
	case errors.Is(err, os.ErrNotExist):
		return appendDistinctInitPath(paths, path), nil
	default:
		return nil, configstore.ErrStore
	}
}

func freezeInitVertexCredential(
	planning initconfig.PlanningResult,
	runtimeDeps Dependencies,
) (string, Dependencies) {
	const credentialName = "GOOGLE_APPLICATION_CREDENTIALS" //nolint:gosec // Public environment name, not a credential.
	selectedVertex := false
	for _, name := range planning.Desired.SelectedProviders {
		if name != core.ProviderGemini {
			continue
		}
		configured, present := planning.Merge.Config.Providers[string(name)]
		if present && slices.Contains(configured.CredentialEnv, credentialName) {
			selectedVertex = true
			break
		}
	}
	if !selectedVertex || runtimeDeps.LookupEnv == nil {
		return "", runtimeDeps
	}
	original := runtimeDeps.LookupEnv
	value, present := original(credentialName)
	runtimeDeps.LookupEnv = func(name string) (string, bool) {
		if name == credentialName {
			return value, present
		}
		return original(name)
	}
	if !present || value == "" {
		return "", runtimeDeps
	}
	return value, runtimeDeps
}

func appendDistinctInitPath(paths []string, value string) []string {
	key := filepath.Clean(value)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	for _, existing := range paths {
		existingKey := filepath.Clean(existing)
		if runtime.GOOS == "windows" {
			existingKey = strings.ToLower(existingKey)
		}
		if existingKey == key {
			return paths
		}
	}
	return append(paths, value)
}

func validInitPreflight(action initconfig.KeyAction, state configstore.KeyState) bool {
	switch action {
	case initconfig.KeyActionNone:
		return state == configstore.KeyStateNone
	case initconfig.KeyActionInspect:
		return state == configstore.KeyStateMissing || state == configstore.KeyStateReusable
	case initconfig.KeyActionEnsure:
		return state == configstore.KeyStateMissing ||
			state == configstore.KeyStateNeedsConfirmation ||
			state == configstore.KeyStateReusable
	default:
		return false
	}
}

func interpretInitCommit(
	ctx context.Context,
	stdout io.Writer,
	commit configstore.CommitResult,
	commitErr error,
) (InitResult, bool) {
	switch commit.State {
	case configstore.CommitRecoveryRequired:
		writeFixed(stdout, "backup_recovery_required\n")
		return InitResult{Outcome: InitRecoveryRequired}, true
	case configstore.CommitIndeterminate:
		writeFixed(stdout, "setup_state_unknown\n")
		return InitResult{Outcome: InitFailed}, true
	case configstore.CommitCommitted:
		if contextFailure(ctx, commitErr) {
			writeFixed(stdout, "setup_saved_before_cancellation\n")
			return InitResult{Outcome: InitCanceled, Saved: true}, true
		}
		if commitErr != nil {
			writeFixed(stdout, "setup_saved_with_cleanup_failure\n")
			return InitResult{Outcome: InitFailed, Saved: true}, true
		}
		return InitResult{}, false
	case configstore.CommitNotCommitted, configstore.CommitRolledBack:
		if contextFailure(ctx, commitErr) {
			writeFixed(stdout, "setup_not_saved\n")
			return InitResult{Outcome: InitCanceled}, true
		}
		writeFixed(stdout, "setup_failed\n")
		return InitResult{Outcome: InitFailed}, true
	default:
		writeFixed(stdout, "setup_failed\n")
		return InitResult{Outcome: InitFailed}, true
	}
}

func initLoadFailure(ctx context.Context, stdout io.Writer, err error) InitResult {
	if errors.Is(err, configstore.ErrInvalidConfig) {
		writeFixed(stdout, "configuration_invalid\n")
		return InitResult{Outcome: InitUsage}
	}
	return initFailure(ctx, stdout, err, false)
}

func initPlanningFailure(stdout io.Writer, err error) InitResult {
	switch {
	case errors.Is(err, initconfig.ErrUsage), errors.Is(err, initconfig.ErrPlan):
		writeFixed(stdout, "init_input_invalid\n")
	default:
		writeFixed(stdout, "setup_failed\n")
		return InitResult{Outcome: InitFailed}
	}
	return InitResult{Outcome: InitUsage}
}

func initInteractiveFailure(
	ctx context.Context,
	stdout io.Writer,
	err error,
) InitResult {
	if errors.Is(err, initconfig.ErrUsage) {
		return initPlanningFailure(stdout, err)
	}
	if errors.Is(err, initconfig.ErrPlan) {
		writeFixed(stdout, "setup_failed\n")
		return InitResult{Outcome: InitFailed}
	}
	if errors.Is(err, io.EOF) {
		err = context.Canceled
	}
	return initFailure(ctx, stdout, err, false)
}

func initFailure(ctx context.Context, stdout io.Writer, err error, saved bool) InitResult {
	if contextFailure(ctx, err) {
		if saved {
			writeFixed(stdout, "setup_saved_before_cancellation\n")
		} else {
			writeFixed(stdout, "setup_not_saved\n")
		}
		return InitResult{Outcome: InitCanceled, Saved: saved}
	}
	writeFixed(stdout, "setup_failed\n")
	return InitResult{Outcome: InitFailed, Saved: saved}
}

func contextFailure(ctx context.Context, err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) ||
		ctx != nil && ctx.Err() != nil
}

func initCanceledAfterDiagnosis(stdout io.Writer, saved bool) InitResult {
	if saved {
		writeFixed(stdout, "setup_saved_before_cancellation\n")
	} else {
		writeFixed(stdout, "setup_not_saved\n")
	}
	return InitResult{Outcome: InitCanceled, Saved: saved}
}

func writeRequired(writer io.Writer, value string) bool {
	written, err := io.WriteString(writer, value)
	return err == nil && written == len(value)
}

func clearInitBytes(value []byte) {
	for index := range value {
		value[index] = 0
	}
}
