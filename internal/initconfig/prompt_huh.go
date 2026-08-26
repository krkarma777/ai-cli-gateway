package initconfig

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	xterm "github.com/charmbracelet/x/term"
	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

var (
	errHuhSelectionRequired = errors.New("select at least one provider")
	errHuhInvalidPath       = errors.New("enter a valid path")
	errHuhInvalidAlias      = errors.New("enter a valid unused model alias")
	errHuhInvalidModel      = errors.New("enter a valid provider model")
	errHuhInvalidEnv        = errors.New("enter a valid environment name")
)

type huhPrompt struct {
	input  io.Reader
	output io.Writer
}

func newHuhPrompt(input io.Reader, output io.Writer) *huhPrompt {
	return &huhPrompt{input: input, output: output}
}

func (prompt *huhPrompt) SelectProviders(
	ctx context.Context,
	request ProviderSelectionRequest,
) (ProviderSelectionResponse, error) {
	if err := huhPromptReady(ctx, prompt); err != nil {
		return ProviderSelectionResponse{}, err
	}
	initial := append([]core.ProviderName(nil), request.Initial...)
	if len(initial) == 0 {
		initial = append(initial, request.Existing...)
	}
	if _, ok := uniqueProviders(initial); !ok {
		return ProviderSelectionResponse{}, ErrPlan
	}
	if _, ok := uniqueProviders(request.Existing); !ok {
		return ProviderSelectionResponse{}, ErrPlan
	}

	providerOptions := []huh.Option[core.ProviderName]{
		huh.NewOption("Codex", core.ProviderCodex),
		huh.NewOption("Claude", core.ProviderClaude),
		huh.NewOption("Gemini", core.ProviderGemini),
	}
	selection := huh.NewMultiSelect[core.ProviderName]().
		Title("Select providers").
		Description("Space toggles; Enter continues; Shift+Tab goes back.").
		Options(providerOptions...).
		Value(&initial).
		Validate(func(values []core.ProviderName) error {
			if len(values) == 0 {
				return errHuhSelectionRequired
			}
			if _, ok := uniqueProviders(values); !ok {
				return ErrPlan
			}
			return nil
		})
	action := ReviewConfirm
	actionField := huh.NewSelect[ReviewDecision]().
		Title("Provider selection action").
		Options(
			huh.NewOption("Continue", ReviewConfirm),
			huh.NewOption("Back", ReviewBack),
		).
		Value(&action).
		Inline(true)
	form := huh.NewForm(
		huh.NewGroup(selection).Title("Providers"),
		huh.NewGroup(actionField).Title("Continue or go back"),
	)
	if err := prompt.run(ctx, form); err != nil {
		return ProviderSelectionResponse{}, err
	}
	if action == ReviewBack {
		return ProviderSelectionResponse{Decision: ReviewBack}, nil
	}
	if action != ReviewConfirm {
		return ProviderSelectionResponse{}, ErrPlan
	}
	if _, ok := uniqueProviders(initial); !ok || len(initial) == 0 {
		return ProviderSelectionResponse{}, ErrPlan
	}
	return ProviderSelectionResponse{
		Providers: append([]core.ProviderName(nil), initial...),
		Decision:  ReviewConfirm,
	}, nil
}

func (prompt *huhPrompt) Collect(
	ctx context.Context,
	request CollectRequest,
) (CollectResponse, error) {
	if err := huhPromptReady(ctx, prompt); err != nil {
		return CollectResponse{}, err
	}
	options := cloneOptions(request.Initial)
	if request.Discovery != nil {
		if len(options.Providers) == 0 {
			return CollectResponse{}, ErrPlan
		}
		for _, name := range options.Providers {
			discovery, ok := request.Discovery[name]
			if !ok || !knownProvider(name) {
				return CollectResponse{}, ErrPlan
			}
			input, decision, err := prompt.collectProviderForm(ctx, name, discovery)
			if err != nil {
				return CollectResponse{}, err
			}
			if decision == ReviewBack {
				return huhBackToSelection(request.Initial), nil
			}
			if decision != ReviewConfirm {
				return CollectResponse{}, ErrPlan
			}
			if options.Provider == nil {
				options.Provider = make(map[core.ProviderName]ProviderInput)
			}
			options.Provider[name] = input

			collectAnother := true
			if promptOptionsHaveProviderModel(options, request.Existing, name) {
				collectAnother, decision, err = prompt.confirmAnotherModelForm(ctx, name)
				if err != nil {
					return CollectResponse{}, err
				}
				if decision == ReviewBack {
					return huhBackToSelection(request.Initial), nil
				}
			}
			for collectAnother {
				model, another, decision, err := prompt.collectModelForm(
					ctx, name, options, request.Existing,
				)
				if err != nil {
					return CollectResponse{}, err
				}
				if decision == ReviewBack {
					return huhBackToSelection(request.Initial), nil
				}
				if decision != ReviewConfirm {
					return CollectResponse{}, ErrPlan
				}
				options.Models = append(options.Models, model)
				collectAnother = another
			}
		}
	}

	gateway, decision, err := prompt.collectGatewayForm(
		ctx, options.Gateway, request.Existing,
	)
	if err != nil {
		return CollectResponse{}, err
	}
	if decision == ReviewBack {
		return huhBackToSelection(request.Initial), nil
	}
	if decision != ReviewConfirm {
		return CollectResponse{}, ErrPlan
	}
	options.Gateway = gateway
	return CollectResponse{Options: cloneOptions(options)}, nil
}

func (prompt *huhPrompt) Review(
	ctx context.Context,
	request ReviewRequest,
) (ReviewResponse, error) {
	if err := huhPromptReady(ctx, prompt); err != nil {
		return ReviewResponse{}, err
	}
	if len(request.Diff.Entries) != 0 {
		if _, ok := validatedDiffEntries(request.Diff.Entries); !ok {
			return ReviewResponse{}, ErrPlan
		}
	}
	choices := make([]CollisionChoice, len(request.Collisions))
	groups := make([]*huh.Group, 0, len(request.Collisions)+1)
	seen := make(map[string]struct{}, len(request.Collisions))
	for index, collision := range request.Collisions {
		if (collision.Target != DiffProvider && collision.Target != DiffModel) ||
			!validDiffName(collision.Target, collision.Name) {
			return ReviewResponse{}, ErrPlan
		}
		key := string(rune(collision.Target)) + "\x00" + collision.Name
		if _, duplicate := seen[key]; duplicate {
			return ReviewResponse{}, ErrPlan
		}
		seen[key] = struct{}{}
		choices[index] = CollisionReplace
		field := huh.NewSelect[CollisionChoice]().
			Title("Resolve "+promptCollisionTarget(collision.Target)+" "+collision.Name).
			Options(
				huh.NewOption("Replace", CollisionReplace),
				huh.NewOption("Keep existing", CollisionKeepExisting),
			).
			Value(&choices[index]).
			Inline(true)
		groups = append(groups, huh.NewGroup(field).Title("Collision"))
	}
	action := ReviewConfirm
	actionField := huh.NewSelect[ReviewDecision]().
		Title("Review action").
		Options(
			huh.NewOption("Confirm", ReviewConfirm),
			huh.NewOption("Back", ReviewBack),
			huh.NewOption("Decline", ReviewDecline),
		).
		Value(&action).
		Inline(true)
	groups = append(groups, huh.NewGroup(actionField).Title("Confirm review"))
	if err := prompt.run(ctx, huh.NewForm(groups...)); err != nil {
		return ReviewResponse{}, err
	}
	if action == ReviewBack || action == ReviewDecline {
		return ReviewResponse{Decision: action}, nil
	}
	if action != ReviewConfirm {
		return ReviewResponse{}, ErrPlan
	}
	decisions := make([]CollisionDecision, len(request.Collisions))
	for index, collision := range request.Collisions {
		if choices[index] != CollisionReplace &&
			choices[index] != CollisionKeepExisting {
			return ReviewResponse{}, ErrPlan
		}
		decisions[index] = CollisionDecision{
			Target: collision.Target,
			Name:   collision.Name,
			Choice: choices[index],
		}
	}
	return ReviewResponse{
		Decision: ReviewConfirm, Collisions: decisions,
	}, nil
}

func (prompt *huhPrompt) ConfirmKeyAction(
	ctx context.Context,
	request KeyConfirmationRequest,
) (ReviewDecision, error) {
	if err := huhPromptReady(ctx, prompt); err != nil {
		return 0, err
	}
	if (request.Kind != ConfirmOrphanReuse &&
		request.Kind != ConfirmMissingConfiguredKeyCreation) ||
		!boundedPromptText(request.Path, maxPromptLineBytes, false) {
		return 0, ErrPlan
	}
	title := "Reuse the existing unreferenced Gateway key?"
	if request.Kind == ConfirmMissingConfiguredKeyCreation {
		title = "Create the missing configured Gateway key?"
	}
	confirmed := false
	action := ReviewConfirm
	confirmation := huh.NewConfirm().
		Title(title).
		Description(request.Path).
		Value(&confirmed).
		Inline(true)
	actionField := huh.NewSelect[ReviewDecision]().
		Title("Key action").
		Options(
			huh.NewOption("Continue", ReviewConfirm),
			huh.NewOption("Back", ReviewBack),
		).
		Value(&action).
		Inline(true)
	form := huh.NewForm(
		huh.NewGroup(confirmation).Title("Gateway key"),
		huh.NewGroup(actionField).Title("Continue or go back"),
	)
	if err := prompt.run(ctx, form); err != nil {
		return 0, err
	}
	if action == ReviewBack {
		return ReviewBack, nil
	}
	if action != ReviewConfirm {
		return 0, ErrPlan
	}
	if confirmed {
		return ReviewConfirm, nil
	}
	return ReviewDecline, nil
}

func (prompt *huhPrompt) collectProviderForm(
	ctx context.Context,
	name core.ProviderName,
	discovery ProviderDiscovery,
) (ProviderInput, ReviewDecision, error) {
	commandLabels := make([]string, 0, len(discovery.Commands)+1)
	for _, candidate := range discovery.Commands {
		label, ok := commandCandidateLabel(candidate)
		if !ok {
			return ProviderInput{}, 0, ErrPlan
		}
		commandLabels = append(commandLabels, label)
	}
	commandLabels = append(commandLabels, "Enter another path")
	commandOptions := make([]huh.Option[int], len(commandLabels))
	for index, label := range commandLabels {
		commandOptions[index] = huh.NewOption(label, index)
	}
	commandIndex := 0
	customCommandIndex := len(discovery.Commands)
	executable := ""
	entrypoint := ""
	commandSelect := huh.NewSelect[int]().
		Title("Choose " + string(name) + " executable").
		Options(commandOptions...).
		Value(&commandIndex)
	executableInput := huh.NewInput().
		Title("Executable path").
		CharLimit(maxPromptLineBytes).
		Value(&executable).
		Validate(func(value string) error {
			if !boundedPromptText(value, maxPromptLineBytes, false) ||
				!filepath.IsAbs(value) {
				return errHuhInvalidPath
			}
			return nil
		})
	entrypointInput := huh.NewInput().
		Title("Node entrypoint path").
		CharLimit(maxPromptLineBytes).
		Value(&entrypoint).
		Validate(func(value string) error {
			extension := strings.ToLower(filepath.Ext(value))
			if !boundedPromptText(value, maxPromptLineBytes, false) ||
				!filepath.IsAbs(value) ||
				(extension != ".js" && extension != ".mjs") {
				return errHuhInvalidPath
			}
			return nil
		})

	homeLabels := make([]string, 0, len(discovery.ConfigHomes)+1)
	for _, candidate := range discovery.ConfigHomes {
		label, ok := pathCandidateLabel(candidate)
		if !ok {
			return ProviderInput{}, 0, ErrPlan
		}
		homeLabels = append(homeLabels, label)
	}
	homeLabels = append(homeLabels, "Enter another path")
	homeOptions := make([]huh.Option[int], len(homeLabels))
	for index, label := range homeLabels {
		homeOptions[index] = huh.NewOption(label, index)
	}
	homeIndex := 0
	customHomeIndex := len(discovery.ConfigHomes)
	home := ""
	homeSelect := huh.NewSelect[int]().
		Title("Choose " + string(name) + " config home").
		Options(homeOptions...).
		Value(&homeIndex)
	homeInput := huh.NewInput().
		Title("Config home path").
		CharLimit(maxPromptLineBytes).
		Value(&home).
		Validate(func(value string) error {
			if !boundedPromptText(value, maxPromptLineBytes, false) ||
				!filepath.IsAbs(value) {
				return errHuhInvalidPath
			}
			return nil
		})

	auth := AuthConfigHome
	authOptions, err := huhAuthOptions(name, discovery.AuthChoices)
	if err != nil {
		return ProviderInput{}, 0, err
	}
	if len(discovery.AuthChoices) > 0 {
		auth = discovery.AuthChoices[0]
	}
	authSelect := huh.NewSelect[AuthID]().
		Title("Choose " + string(name) + " authentication").
		Options(authOptions...).
		Value(&auth)
	action := ReviewConfirm
	actionSelect := huh.NewSelect[ReviewDecision]().
		Title("Provider action").
		Options(
			huh.NewOption("Continue", ReviewConfirm),
			huh.NewOption("Back to provider selection", ReviewBack),
		).
		Value(&action).
		Inline(true)
	form := huh.NewForm(
		huh.NewGroup(commandSelect).Title("Executable"),
		huh.NewGroup(executableInput).WithHideFunc(func() bool {
			return commandIndex != customCommandIndex
		}),
		huh.NewGroup(entrypointInput).WithHideFunc(func() bool {
			return commandIndex != customCommandIndex ||
				!isWindowsNodeExecutable(runtime.GOOS, executable)
		}),
		huh.NewGroup(homeSelect).Title("Config home"),
		huh.NewGroup(homeInput).WithHideFunc(func() bool {
			return homeIndex != customHomeIndex
		}),
		huh.NewGroup(authSelect).Title("Authentication").WithHide(name == core.ProviderCodex),
		huh.NewGroup(actionSelect).Title("Continue or go back"),
	)
	if err := prompt.run(ctx, form); err != nil {
		return ProviderInput{}, 0, err
	}
	if action == ReviewBack {
		return ProviderInput{}, ReviewBack, nil
	}
	if action != ReviewConfirm {
		return ProviderInput{}, 0, ErrPlan
	}
	var input ProviderInput
	if commandIndex < len(discovery.Commands) {
		command := discovery.Commands[commandIndex].Command
		input.Executable = StringValue{Set: true, Value: command.Executable}
		if len(command.PrefixArgs) == 1 {
			input.Entrypoint = StringValue{Set: true, Value: command.PrefixArgs[0]}
		} else if len(command.PrefixArgs) != 0 {
			return ProviderInput{}, 0, ErrPlan
		}
	} else if commandIndex == customCommandIndex {
		input.Executable = StringValue{Set: true, Value: executable}
		input.Entrypoint = customEntrypointForExecutable(runtime.GOOS, executable, entrypoint)
	} else {
		return ProviderInput{}, 0, ErrPlan
	}
	if homeIndex < len(discovery.ConfigHomes) {
		input.ConfigHome = StringValue{
			Set: true, Value: discovery.ConfigHomes[homeIndex].Path,
		}
	} else if homeIndex == customHomeIndex {
		input.ConfigHome = StringValue{Set: true, Value: home}
	} else {
		return ProviderInput{}, 0, ErrPlan
	}
	if name != core.ProviderCodex {
		input.Auth = auth
		input.AuthSet = true
	}
	return input, ReviewConfirm, nil
}

func customEntrypointForExecutable(goos, executable, entrypoint string) StringValue {
	if entrypoint == "" || !isWindowsNodeExecutable(goos, executable) {
		return StringValue{}
	}
	return StringValue{Set: true, Value: entrypoint}
}

func isWindowsNodeExecutable(goos, executable string) bool {
	if goos != "windows" {
		return false
	}
	normalized := strings.ReplaceAll(executable, `\`, "/")
	return strings.EqualFold(filepath.Base(normalized), "node.exe")
}

func (prompt *huhPrompt) collectModelForm(
	ctx context.Context,
	name core.ProviderName,
	options Options,
	existing *config.Config,
) (ModelMapping, bool, ReviewDecision, error) {
	alias := string(name) + "-local"
	if promptAliasExists(alias, options, existing) {
		alias = ""
	}
	providerModel := ""
	addAnother := false
	action := ReviewConfirm
	aliasInput := huh.NewInput().
		Title("Public model alias").
		Description("Only the alias may have a default.").
		CharLimit(maxPromptAliasBytes).
		Value(&alias).
		Validate(func(value string) error {
			if !validPromptAlias(value, name) ||
				promptAliasExists(value, options, existing) {
				return errHuhInvalidAlias
			}
			return nil
		})
	modelInput := huh.NewInput().
		Title("Provider model (no default)").
		CharLimit(maxPromptProviderModelBytes).
		Value(&providerModel).
		Validate(func(value string) error {
			if len(value) > maxPromptProviderModelBytes ||
				core.ValidateProviderModel(value) != nil {
				return errHuhInvalidModel
			}
			return nil
		})
	anotherConfirm := huh.NewConfirm().
		Title("Add another model for " + string(name) + "?").
		Value(&addAnother).
		Inline(true)
	actionSelect := huh.NewSelect[ReviewDecision]().
		Title("Model action").
		Options(
			huh.NewOption("Continue", ReviewConfirm),
			huh.NewOption("Back to provider selection", ReviewBack),
		).
		Value(&action).
		Inline(true)
	form := huh.NewForm(
		huh.NewGroup(aliasInput, modelInput).Title("Model for "+string(name)),
		huh.NewGroup(anotherConfirm).Title("More models"),
		huh.NewGroup(actionSelect).Title("Continue or go back"),
	)
	if err := prompt.run(ctx, form); err != nil {
		return ModelMapping{}, false, 0, err
	}
	if action == ReviewBack {
		return ModelMapping{}, false, ReviewBack, nil
	}
	if action != ReviewConfirm {
		return ModelMapping{}, false, 0, ErrPlan
	}
	return ModelMapping{
		ID: alias, Provider: name, ProviderModel: providerModel,
	}, addAnother, ReviewConfirm, nil
}

func (prompt *huhPrompt) confirmAnotherModelForm(
	ctx context.Context,
	name core.ProviderName,
) (bool, ReviewDecision, error) {
	another := false
	action := ReviewConfirm
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewConfirm().
				Title("Add another model for "+string(name)+"?").
				Value(&another).
				Inline(true),
		),
		huh.NewGroup(
			huh.NewSelect[ReviewDecision]().
				Title("Model action").
				Options(
					huh.NewOption("Continue", ReviewConfirm),
					huh.NewOption("Back to provider selection", ReviewBack),
				).
				Value(&action).
				Inline(true),
		),
	)
	if err := prompt.run(ctx, form); err != nil {
		return false, 0, err
	}
	return another, action, nil
}

func (prompt *huhPrompt) collectGatewayForm(
	ctx context.Context,
	initial GatewayInput,
	existing *config.Config,
) (GatewayInput, ReviewDecision, error) {
	choice := gatewayDefaultChoice(initial, existing)
	path := ""
	if initial.KeyFile.Set {
		path = initial.KeyFile.Value
	} else if existing != nil {
		path = existing.Server.APIKeyFile
	}
	environment := ""
	if initial.KeyEnv.Set {
		environment = initial.KeyEnv.Value
	} else if existing != nil {
		environment = existing.Server.APIKeyEnv
	}
	action := ReviewConfirm
	authSelect := huh.NewSelect[int]().
		Title("Choose Gateway authentication").
		Options(
			huh.NewOption("key file", 0),
			huh.NewOption("environment variable", 1),
			huh.NewOption("none", 2),
		).
		Value(&choice)
	pathInput := huh.NewInput().
		Title("Gateway key file (empty uses the default)").
		CharLimit(maxPromptLineBytes).
		Value(&path).
		Validate(func(value string) error {
			if value != "" && (!boundedPromptText(value, maxPromptLineBytes, false) ||
				!filepath.IsAbs(value)) {
				return errHuhInvalidPath
			}
			return nil
		})
	environmentInput := huh.NewInput().
		Title("Gateway key environment name").
		CharLimit(maxPromptEnvironmentBytes).
		Value(&environment).
		Validate(func(value string) error {
			if len(value) > maxPromptEnvironmentBytes ||
				!environmentNamePattern.MatchString(value) {
				return errHuhInvalidEnv
			}
			return nil
		})
	actionSelect := huh.NewSelect[ReviewDecision]().
		Title("Gateway action").
		Options(
			huh.NewOption("Continue", ReviewConfirm),
			huh.NewOption("Back", ReviewBack),
		).
		Value(&action).
		Inline(true)
	form := huh.NewForm(
		huh.NewGroup(authSelect).Title("Gateway authentication"),
		huh.NewGroup(pathInput).WithHideFunc(func() bool { return choice != 0 }),
		huh.NewGroup(environmentInput).WithHideFunc(func() bool { return choice != 1 }),
		huh.NewGroup(actionSelect).Title("Continue or go back"),
	)
	if err := prompt.run(ctx, form); err != nil {
		return GatewayInput{}, 0, err
	}
	if action == ReviewBack {
		return GatewayInput{}, ReviewBack, nil
	}
	if action != ReviewConfirm {
		return GatewayInput{}, 0, ErrPlan
	}
	switch choice {
	case 0:
		gateway := GatewayInput{Auth: GatewayAuthFile, AuthSet: true}
		if path != "" {
			gateway.KeyFile = StringValue{Set: true, Value: path}
		}
		return gateway, ReviewConfirm, nil
	case 1:
		return GatewayInput{
			Auth: GatewayAuthEnvironment, AuthSet: true,
			KeyEnv: StringValue{Set: true, Value: environment},
		}, ReviewConfirm, nil
	case 2:
		return GatewayInput{Auth: GatewayAuthNone, AuthSet: true}, ReviewConfirm, nil
	default:
		return GatewayInput{}, 0, ErrPlan
	}
}

func huhAuthOptions(
	name core.ProviderName,
	choices []AuthID,
) ([]huh.Option[AuthID], error) {
	want := 1
	if name == core.ProviderClaude {
		want = 2
	} else if name == core.ProviderGemini {
		want = 3
	}
	if len(choices) != want {
		return nil, ErrPlan
	}
	options := make([]huh.Option[AuthID], len(choices))
	seen := make(map[AuthID]struct{}, len(choices))
	for index, choice := range choices {
		if _, duplicate := seen[choice]; duplicate {
			return nil, ErrPlan
		}
		seen[choice] = struct{}{}
		if name == core.ProviderCodex {
			if choice != AuthConfigHome {
				return nil, ErrPlan
			}
			options[index] = huh.NewOption("prepared config home", choice)
			continue
		}
		label, ok := promptAuthLabel(name, choice)
		if !ok {
			return nil, ErrPlan
		}
		options[index] = huh.NewOption(label, choice)
	}
	return options, nil
}

func huhBackToSelection(initial Options) CollectResponse {
	return CollectResponse{
		Options:         cloneOptions(initial),
		BackToSelection: true,
	}
}

func huhPromptReady(ctx context.Context, prompt *huhPrompt) error {
	if ctx == nil || prompt == nil || nilInterface(prompt.input) ||
		nilInterface(prompt.output) {
		return ErrPlan
	}
	return ctx.Err()
}

func (prompt *huhPrompt) run(ctx context.Context, form *huh.Form) error {
	if err := huhPromptReady(ctx, prompt); err != nil || form == nil {
		if err != nil {
			return err
		}
		return ErrPlan
	}
	// Bubble Tea cannot discover dimensions from an injected non-file writer.
	// A real terminal replaces this fallback with its actual size during Run.
	form.WithProgramOptions(tea.WithWindowSize(80, 24)).
		WithAccessible(false).
		WithInput(prompt.input).
		WithOutput(prompt.output)

	var descriptor uintptr
	var original *xterm.State
	if input, ok := prompt.input.(fileDescriptor); ok &&
		xterm.IsTerminal(input.Fd()) {
		descriptor = input.Fd()
		state, err := xterm.GetState(descriptor)
		if err != nil {
			return ErrPlan
		}
		original = state
	}
	runErr := form.RunWithContext(ctx)
	var restoreErr error
	if original != nil {
		restoreErr = xterm.Restore(descriptor, original)
	}
	return finalizeHuhRun(ctx, runErr, restoreErr)
}

func finalizeHuhRun(ctx context.Context, runErr, restoreErr error) error {
	if restoreErr != nil {
		return terminalRestoreError{cause: restoreErr}
	}
	if ctx == nil {
		return ErrPlan
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return contextErr
	}
	switch {
	case runErr == nil:
		return nil
	case errors.Is(runErr, huh.ErrUserAborted):
		return context.Canceled
	case errors.Is(runErr, context.Canceled):
		return context.Canceled
	case errors.Is(runErr, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return ErrPlan
	}
}
