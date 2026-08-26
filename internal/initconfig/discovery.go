package initconfig

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
)

// CandidateSource identifies where a read-only discovery suggestion came from.
type CandidateSource string

const (
	// CandidateExplicit marks a suggestion supplied explicitly by the user.
	CandidateExplicit CandidateSource = "explicit"
	// CandidateExisting marks a suggestion from the loaded configuration.
	CandidateExisting CandidateSource = "existing"
	// CandidateEnvironment marks a provider-specific environment path hint.
	CandidateEnvironment CandidateSource = "environment"
	// CandidatePATH marks an executable suggestion discovered through PATH.
	CandidatePATH CandidateSource = "path"
	// CandidateConventional marks a provider's conventional per-user home.
	CandidateConventional CandidateSource = "conventional"
	// CandidateDedicated marks a new private home suggestion beside the config.
	CandidateDedicated CandidateSource = "dedicated"
)

// PathCandidate is a config-home suggestion that still requires confirmation.
type PathCandidate struct {
	Path   string
	Source CandidateSource
}

// CommandCandidate is a provider command suggestion that still requires confirmation.
type CommandCandidate struct {
	Command ProviderCommand
	Source  CandidateSource
}

// ProviderDiscovery contains read-only suggestions for one selected provider.
type ProviderDiscovery struct {
	Commands    []CommandCandidate
	ConfigHomes []PathCandidate
	AuthChoices []AuthID
}

// DiscoveryDependencies supplies read-only environment and path operations.
type DiscoveryDependencies struct {
	LookupEnv       provider.LookupEnv
	LookPath        func(string) (string, error)
	UserHomeDir     func() (string, error)
	AbsPath         func(string) (string, error)
	OpenCommandPath func(string, CommandReadMode, int64) (CommandFileInspection, error)
}

type discoveryProfile struct {
	commandName      string
	environmentHome  string
	conventionalHome string
	authChoices      func(
		context.Context,
		ProviderInput,
		config.Provider,
		bool,
		provider.LookupEnv,
	) ([]AuthID, error)
}

// DiscoverProviders returns confirmation-required suggestions without mutating state.
func DiscoverProviders(
	ctx context.Context,
	configPath string,
	options Options,
	existing *config.Config,
	deps DiscoveryDependencies,
) (map[core.ProviderName]ProviderDiscovery, error) {
	if err := discoveryContextError(ctx); err != nil {
		return nil, err
	}
	if err := ValidateOptions(options); err != nil {
		return nil, err
	}

	discoveries := make(map[core.ProviderName]ProviderDiscovery, len(options.Providers))
	if len(options.Providers) == 0 {
		return discoveries, nil
	}
	if !safeText(configPath) {
		return nil, ErrPlan
	}

	for _, name := range options.Providers {
		if err := discoveryContextError(ctx); err != nil {
			return nil, err
		}
		profile, ok := profileForProvider(name)
		if !ok {
			return nil, ErrPlan
		}
		input := options.Provider[name]
		current, exists := existingProvider(existing, name)

		commands, err := discoverCommandCandidates(
			ctx,
			name,
			input,
			current,
			exists,
			profile.commandName,
			deps,
		)
		if err != nil {
			return nil, err
		}
		homes, err := discoverHomeCandidates(
			ctx,
			configPath,
			name,
			input,
			current,
			exists,
			profile,
			deps,
		)
		if err != nil {
			return nil, err
		}
		auth, err := profile.authChoices(
			ctx,
			input,
			current,
			exists,
			deps.LookupEnv,
		)
		if err != nil {
			return nil, err
		}
		if err := discoveryContextError(ctx); err != nil {
			return nil, err
		}

		discoveries[name] = ProviderDiscovery{
			Commands:    commands,
			ConfigHomes: homes,
			AuthChoices: auth,
		}
	}
	return discoveries, nil
}

func profileForProvider(name core.ProviderName) (discoveryProfile, bool) {
	switch name {
	case core.ProviderCodex:
		return codexDiscoveryProfile(), true
	case core.ProviderClaude:
		return claudeDiscoveryProfile(), true
	case core.ProviderGemini:
		return geminiDiscoveryProfile(), true
	default:
		return discoveryProfile{}, false
	}
}

func discoverCommandCandidates(
	ctx context.Context,
	name core.ProviderName,
	input ProviderInput,
	current config.Provider,
	exists bool,
	commandName string,
	deps DiscoveryDependencies,
) ([]CommandCandidate, error) {
	var candidates []CommandCandidate
	if input.Executable.Set {
		command := ProviderCommand{Executable: input.Executable.Value}
		if input.Entrypoint.Set {
			command.PrefixArgs = []string{input.Entrypoint.Value}
		}
		if err := appendCommandCandidate(&candidates, command, CandidateExplicit); err != nil {
			return nil, err
		}
	}
	if exists && current.Executable != "" {
		command := ProviderCommand{
			Executable: current.Executable,
			PrefixArgs: slices.Clone(current.PrefixArgs),
		}
		if err := appendCommandCandidate(&candidates, command, CandidateExisting); err != nil {
			return nil, err
		}
	}

	if deps.LookPath == nil {
		return candidates, nil
	}
	if err := discoveryContextError(ctx); err != nil {
		return nil, err
	}
	path, err := deps.LookPath(commandName)
	if contextErr := discoveryContextError(ctx); contextErr != nil {
		return nil, contextErr
	}
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return candidates, nil
		}
		return nil, ErrPlan
	}
	if !safeText(path) || deps.AbsPath == nil {
		return nil, ErrPlan
	}
	absolute, err := deps.AbsPath(path)
	if contextErr := discoveryContextError(ctx); contextErr != nil {
		return nil, contextErr
	}
	if err != nil || !safeText(absolute) || !filepath.IsAbs(absolute) {
		return nil, ErrPlan
	}
	command := ProviderCommand{Executable: absolute}
	if runtime.GOOS == "windows" &&
		strings.EqualFold(filepath.Ext(absolute), ".cmd") {
		if err := discoveryContextError(ctx); err != nil {
			return nil, err
		}
		command, err = ResolveCommandCandidate(
			name,
			absolute,
			"",
			discoveryCommandDependencies(ctx, deps),
		)
		if contextErr := discoveryContextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		if err != nil {
			return nil, ErrPlan
		}
	}
	if err := appendCommandCandidate(
		&candidates,
		command,
		CandidatePATH,
	); err != nil {
		return nil, err
	}
	return candidates, nil
}

func discoveryCommandDependencies(
	ctx context.Context,
	deps DiscoveryDependencies,
) DiscoveryDependencies {
	wrapped := deps
	open := commandPathOpener(deps)
	wrapped.OpenCommandPath = func(
		path string,
		mode CommandReadMode,
		limit int64,
	) (CommandFileInspection, error) {
		if err := discoveryContextError(ctx); err != nil {
			return nil, err
		}
		inspection, err := open(path, mode, limit)
		if contextErr := discoveryContextError(ctx); contextErr != nil {
			if inspection != nil {
				_ = inspection.Close()
			}
			return nil, contextErr
		}
		return inspection, err
	}
	return wrapped
}

func discoverHomeCandidates(
	ctx context.Context,
	configPath string,
	name core.ProviderName,
	input ProviderInput,
	current config.Provider,
	exists bool,
	profile discoveryProfile,
	deps DiscoveryDependencies,
) ([]PathCandidate, error) {
	var candidates []PathCandidate
	if input.ConfigHome.Set {
		if err := appendPathCandidate(
			&candidates,
			input.ConfigHome.Value,
			CandidateExplicit,
		); err != nil {
			return nil, err
		}
	}
	if exists && current.ConfigHome != "" {
		if err := appendPathCandidate(
			&candidates,
			current.ConfigHome,
			CandidateExisting,
		); err != nil {
			return nil, err
		}
	}
	if profile.environmentHome != "" {
		path, present, err := discoveryEnvironmentPath(
			ctx,
			deps.LookupEnv,
			profile.environmentHome,
		)
		if err != nil {
			return nil, err
		}
		if present {
			if err := appendPathCandidate(
				&candidates,
				path,
				CandidateEnvironment,
			); err != nil {
				return nil, err
			}
		}
	}
	if deps.UserHomeDir != nil {
		if err := discoveryContextError(ctx); err != nil {
			return nil, err
		}
		home, err := deps.UserHomeDir()
		if contextErr := discoveryContextError(ctx); contextErr != nil {
			return nil, contextErr
		}
		if err == nil && safeText(home) && filepath.IsAbs(home) {
			if err := appendPathCandidate(
				&candidates,
				filepath.Join(home, profile.conventionalHome),
				CandidateConventional,
			); err != nil {
				return nil, err
			}
		}
	}
	dedicated := filepath.Join(filepath.Dir(configPath), "providers", string(name))
	if err := appendPathCandidate(&candidates, dedicated, CandidateDedicated); err != nil {
		return nil, err
	}
	return candidates, nil
}

func discoveryEnvironmentPath(
	ctx context.Context,
	lookup provider.LookupEnv,
	name string,
) (string, bool, error) {
	if err := discoveryContextError(ctx); err != nil {
		return "", false, err
	}
	if lookup == nil {
		return "", false, nil
	}
	value, present := lookup(name)
	if err := discoveryContextError(ctx); err != nil {
		return "", false, err
	}
	if !present || !safeText(value) {
		return "", false, nil
	}
	return value, true, nil
}

func credentialPresent(
	ctx context.Context,
	lookup provider.LookupEnv,
	name string,
) (bool, error) {
	if err := discoveryContextError(ctx); err != nil {
		return false, err
	}
	if lookup == nil {
		return false, nil
	}
	present := lookupCredentialPresence(lookup, name)
	if err := discoveryContextError(ctx); err != nil {
		return false, err
	}
	return present, nil
}

func lookupCredentialPresence(lookup provider.LookupEnv, name string) bool {
	value, present := lookup(name)
	return present && value != "" && strings.IndexByte(value, 0) < 0
}

func appendCommandCandidate(
	candidates *[]CommandCandidate,
	command ProviderCommand,
	source CandidateSource,
) error {
	if !safeText(command.Executable) {
		return ErrPlan
	}
	for _, argument := range command.PrefixArgs {
		if !safeText(argument) {
			return ErrPlan
		}
	}
	for _, candidate := range *candidates {
		if candidate.Command.Executable == command.Executable &&
			slices.Equal(candidate.Command.PrefixArgs, command.PrefixArgs) {
			return nil
		}
	}
	*candidates = append(*candidates, CommandCandidate{
		Command: ProviderCommand{
			Executable: command.Executable,
			PrefixArgs: slices.Clone(command.PrefixArgs),
		},
		Source: source,
	})
	return nil
}

func appendPathCandidate(
	candidates *[]PathCandidate,
	path string,
	source CandidateSource,
) error {
	if !safeText(path) {
		return ErrPlan
	}
	for _, candidate := range *candidates {
		if candidate.Path == path {
			return nil
		}
	}
	*candidates = append(*candidates, PathCandidate{Path: path, Source: source})
	return nil
}

func appendAuthChoice(choices *[]AuthID, choice AuthID) {
	for _, existing := range *choices {
		if existing == choice {
			return
		}
	}
	*choices = append(*choices, choice)
}

func credentialProfileMatches(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	remaining := make(map[string]struct{}, len(want))
	for _, name := range want {
		remaining[name] = struct{}{}
	}
	for _, name := range got {
		if _, exists := remaining[name]; !exists {
			return false
		}
		delete(remaining, name)
	}
	return len(remaining) == 0
}

func discoveryContextError(ctx context.Context) error {
	if ctx == nil {
		return ErrPlan
	}
	switch ctx.Err() {
	case nil:
		return nil
	case context.Canceled:
		return context.Canceled
	case context.DeadlineExceeded:
		return context.DeadlineExceeded
	default:
		return ErrPlan
	}
}
