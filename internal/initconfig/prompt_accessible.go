package initconfig

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

const maxPromptLineBytes = 64 << 10

const (
	maxPromptAliasBytes         = 128
	maxPromptProviderModelBytes = 256
	maxPromptEnvironmentBytes   = 128
)

var errPromptBack = errors.New("prompt back")

type terminalRestoreError struct {
	cause error
}

func (terminalRestoreError) Error() string {
	return "restore terminal mode"
}

func (err terminalRestoreError) Unwrap() error {
	return err.cause
}

func (terminalRestoreError) Is(target error) bool {
	return target == ErrPlan
}

type contextLineReader interface {
	ReadLine(context.Context) (string, error)
}

type accessiblePrompt struct {
	output io.Writer
	reader contextLineReader
}

type failedContextLineReader struct{}

func (failedContextLineReader) ReadLine(ctx context.Context) (string, error) {
	if ctx != nil && ctx.Err() != nil {
		return "", ctx.Err()
	}
	return "", ErrPlan
}

func newAccessiblePrompt(
	output io.Writer,
	reader contextLineReader,
) *accessiblePrompt {
	return &accessiblePrompt{output: output, reader: reader}
}

func (prompt *accessiblePrompt) SelectProviders(
	ctx context.Context,
	_ ProviderSelectionRequest,
) (ProviderSelectionResponse, error) {
	if err := accessiblePromptReady(ctx, prompt); err != nil {
		return ProviderSelectionResponse{}, err
	}
	if err := prompt.write(
		"Select providers (comma-separated numbers; back; cancel):\n" +
			"  1) codex\n" +
			"  2) claude\n" +
			"  3) gemini\n",
	); err != nil {
		return ProviderSelectionResponse{}, err
	}

	for {
		value, err := prompt.readLine(ctx)
		if err != nil {
			return ProviderSelectionResponse{}, err
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "back":
			return ProviderSelectionResponse{Decision: ReviewBack}, nil
		case "cancel":
			return ProviderSelectionResponse{}, context.Canceled
		}
		providers, ok := parseProviderSelection(value)
		if ok {
			return ProviderSelectionResponse{
				Providers: providers,
				Decision:  ReviewConfirm,
			}, nil
		}
		if err := prompt.write(
			"Invalid selection. Choose one or more listed numbers.\n",
		); err != nil {
			return ProviderSelectionResponse{}, err
		}
	}
}

func (prompt *accessiblePrompt) Collect(
	ctx context.Context,
	request CollectRequest,
) (CollectResponse, error) {
	if err := accessiblePromptReady(ctx, prompt); err != nil {
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
			input, err := prompt.collectProvider(ctx, name, discovery)
			if errors.Is(err, errPromptBack) {
				return CollectResponse{
					Options:         cloneOptions(request.Initial),
					BackToSelection: true,
				}, nil
			}
			if err != nil {
				return CollectResponse{}, err
			}
			if options.Provider == nil {
				options.Provider = make(map[core.ProviderName]ProviderInput)
			}
			options.Provider[name] = input
			if !promptOptionsHaveProviderModel(options, request.Existing, name) {
				model, err := prompt.collectModel(ctx, name, options, request.Existing)
				if errors.Is(err, errPromptBack) {
					return CollectResponse{
						Options:         cloneOptions(request.Initial),
						BackToSelection: true,
					}, nil
				}
				if err != nil {
					return CollectResponse{}, err
				}
				options.Models = append(options.Models, model)
			}
			for {
				another, err := prompt.confirm(ctx, "Add another model for "+string(name)+"? [y/N] (back; cancel):\n", false)
				if errors.Is(err, errPromptBack) {
					return CollectResponse{
						Options:         cloneOptions(request.Initial),
						BackToSelection: true,
					}, nil
				}
				if err != nil {
					return CollectResponse{}, err
				}
				if !another {
					break
				}
				model, err := prompt.collectModel(ctx, name, options, request.Existing)
				if errors.Is(err, errPromptBack) {
					return CollectResponse{
						Options:         cloneOptions(request.Initial),
						BackToSelection: true,
					}, nil
				}
				if err != nil {
					return CollectResponse{}, err
				}
				options.Models = append(options.Models, model)
			}
		}
	}

	gateway, err := prompt.collectGateway(ctx, options.Gateway, request.Existing)
	if errors.Is(err, errPromptBack) {
		return CollectResponse{
			Options:         cloneOptions(request.Initial),
			BackToSelection: true,
		}, nil
	}
	if err != nil {
		return CollectResponse{}, err
	}
	options.Gateway = gateway
	return CollectResponse{Options: cloneOptions(options)}, nil
}

func (prompt *accessiblePrompt) Review(
	ctx context.Context,
	request ReviewRequest,
) (ReviewResponse, error) {
	if err := accessiblePromptReady(ctx, prompt); err != nil {
		return ReviewResponse{}, err
	}
	if len(request.Diff.Entries) != 0 {
		if _, ok := validatedDiffEntries(request.Diff.Entries); !ok {
			return ReviewResponse{}, ErrPlan
		}
	}
	decisions := make([]CollisionDecision, 0, len(request.Collisions))
	seen := make(map[string]struct{}, len(request.Collisions))
	for _, collision := range request.Collisions {
		if (collision.Target != DiffProvider && collision.Target != DiffModel) ||
			!validDiffName(collision.Target, collision.Name) {
			return ReviewResponse{}, ErrPlan
		}
		key := strconv.Itoa(int(collision.Target)) + "\x00" + collision.Name
		if _, duplicate := seen[key]; duplicate {
			return ReviewResponse{}, ErrPlan
		}
		seen[key] = struct{}{}
		choice, err := prompt.choose(
			ctx,
			"Resolve "+promptCollisionTarget(collision.Target)+" "+collision.Name+" (back; cancel):\n",
			[]string{"replace", "keep existing"},
		)
		if errors.Is(err, errPromptBack) {
			return ReviewResponse{Decision: ReviewBack}, nil
		}
		if err != nil {
			return ReviewResponse{}, err
		}
		decision := CollisionDecision{
			Target: collision.Target,
			Name:   collision.Name,
			Choice: CollisionReplace,
		}
		if choice == 1 {
			decision.Choice = CollisionKeepExisting
		}
		decisions = append(decisions, decision)
	}
	choice, err := prompt.choose(
		ctx,
		"Review action (cancel aborts setup):\n",
		[]string{"confirm", "back", "decline"},
	)
	if errors.Is(err, errPromptBack) {
		return ReviewResponse{Decision: ReviewBack}, nil
	}
	if err != nil {
		return ReviewResponse{}, err
	}
	switch choice {
	case 0:
		return ReviewResponse{
			Decision: ReviewConfirm, Collisions: decisions,
		}, nil
	case 1:
		return ReviewResponse{Decision: ReviewBack}, nil
	case 2:
		return ReviewResponse{Decision: ReviewDecline}, nil
	default:
		return ReviewResponse{}, ErrPlan
	}
}

func (prompt *accessiblePrompt) ConfirmKeyAction(
	ctx context.Context,
	request KeyConfirmationRequest,
) (ReviewDecision, error) {
	if err := accessiblePromptReady(ctx, prompt); err != nil {
		return 0, err
	}
	if (request.Kind != ConfirmOrphanReuse &&
		request.Kind != ConfirmMissingConfiguredKeyCreation) ||
		!boundedPromptText(request.Path, maxPromptLineBytes, false) {
		return 0, ErrPlan
	}
	title := "Reuse the existing unreferenced Gateway key at " + request.Path
	if request.Kind == ConfirmMissingConfiguredKeyCreation {
		title = "Create the missing configured Gateway key at " + request.Path
	}
	confirmed, err := prompt.confirm(
		ctx, title+"? [y/N] (back; cancel):\n", false,
	)
	if errors.Is(err, errPromptBack) {
		return ReviewBack, nil
	}
	if err != nil {
		return 0, err
	}
	if confirmed {
		return ReviewConfirm, nil
	}
	return ReviewDecline, nil
}

func (prompt *accessiblePrompt) collectProvider(
	ctx context.Context,
	name core.ProviderName,
	discovery ProviderDiscovery,
) (ProviderInput, error) {
	commandLabels := make([]string, 0, len(discovery.Commands)+1)
	for _, candidate := range discovery.Commands {
		label, ok := commandCandidateLabel(candidate)
		if !ok {
			return ProviderInput{}, ErrPlan
		}
		commandLabels = append(commandLabels, label)
	}
	commandLabels = append(commandLabels, "Enter another path")
	commandIndex, err := prompt.choose(
		ctx, "Choose "+string(name)+" executable (back; cancel):\n", commandLabels,
	)
	if err != nil {
		return ProviderInput{}, err
	}
	var input ProviderInput
	if commandIndex < len(discovery.Commands) {
		command := discovery.Commands[commandIndex].Command
		input.Executable = StringValue{Set: true, Value: command.Executable}
		if len(command.PrefixArgs) == 1 {
			input.Entrypoint = StringValue{Set: true, Value: command.PrefixArgs[0]}
		} else if len(command.PrefixArgs) != 0 {
			return ProviderInput{}, ErrPlan
		}
	} else {
		executable, err := prompt.input(
			ctx,
			"Executable path (back; cancel):\n",
			"Invalid executable path.\n",
			func(value string) bool {
				return boundedPromptText(value, maxPromptLineBytes, false) &&
					filepath.IsAbs(value)
			},
			"",
		)
		if err != nil {
			return ProviderInput{}, err
		}
		input.Executable = StringValue{Set: true, Value: executable}
		if runtime.GOOS == "windows" &&
			strings.EqualFold(filepath.Base(executable), "node.exe") {
			entrypoint, err := prompt.input(
				ctx,
				"Node entrypoint path (back; cancel):\n",
				"Invalid Node entrypoint path.\n",
				func(value string) bool {
					extension := strings.ToLower(filepath.Ext(value))
					return boundedPromptText(value, maxPromptLineBytes, false) &&
						filepath.IsAbs(value) &&
						(extension == ".js" || extension == ".mjs")
				},
				"",
			)
			if err != nil {
				return ProviderInput{}, err
			}
			input.Entrypoint = StringValue{Set: true, Value: entrypoint}
		}
	}

	homeLabels := make([]string, 0, len(discovery.ConfigHomes)+1)
	for _, candidate := range discovery.ConfigHomes {
		label, ok := pathCandidateLabel(candidate)
		if !ok {
			return ProviderInput{}, ErrPlan
		}
		homeLabels = append(homeLabels, label)
	}
	homeLabels = append(homeLabels, "Enter another path")
	homeIndex, err := prompt.choose(
		ctx, "Choose "+string(name)+" config home (back; cancel):\n", homeLabels,
	)
	if err != nil {
		return ProviderInput{}, err
	}
	if homeIndex < len(discovery.ConfigHomes) {
		input.ConfigHome = StringValue{
			Set: true, Value: discovery.ConfigHomes[homeIndex].Path,
		}
	} else {
		home, err := prompt.input(
			ctx,
			"Config home path (back; cancel):\n",
			"Invalid config home path.\n",
			func(value string) bool {
				return boundedPromptText(value, maxPromptLineBytes, false) &&
					filepath.IsAbs(value)
			},
			"",
		)
		if err != nil {
			return ProviderInput{}, err
		}
		input.ConfigHome = StringValue{Set: true, Value: home}
	}

	if name == core.ProviderCodex {
		if len(discovery.AuthChoices) != 1 ||
			discovery.AuthChoices[0] != AuthConfigHome {
			return ProviderInput{}, ErrPlan
		}
		return input, nil
	}
	authLabels := make([]string, len(discovery.AuthChoices))
	seenAuth := make(map[AuthID]struct{}, len(discovery.AuthChoices))
	for index, auth := range discovery.AuthChoices {
		label, ok := promptAuthLabel(name, auth)
		if !ok {
			return ProviderInput{}, ErrPlan
		}
		if _, duplicate := seenAuth[auth]; duplicate {
			return ProviderInput{}, ErrPlan
		}
		seenAuth[auth] = struct{}{}
		authLabels[index] = label
	}
	wantChoices := 2
	if name == core.ProviderGemini {
		wantChoices = 3
	}
	if len(authLabels) != wantChoices {
		return ProviderInput{}, ErrPlan
	}
	authIndex, err := prompt.choose(
		ctx, "Choose "+string(name)+" authentication (back; cancel):\n", authLabels,
	)
	if err != nil {
		return ProviderInput{}, err
	}
	input.Auth = discovery.AuthChoices[authIndex]
	input.AuthSet = true
	return input, nil
}

func (prompt *accessiblePrompt) collectModel(
	ctx context.Context,
	name core.ProviderName,
	options Options,
	existing *config.Config,
) (ModelMapping, error) {
	defaultAlias := string(name) + "-local"
	if promptAliasExists(defaultAlias, options, existing) {
		defaultAlias = ""
	}
	title := "Public model alias"
	if defaultAlias != "" {
		title += " [" + defaultAlias + "]"
	}
	alias, err := prompt.input(
		ctx,
		title+" (back; cancel):\n",
		"Invalid or duplicate model alias.\n",
		func(value string) bool {
			return validPromptAlias(value, name) &&
				!promptAliasExists(value, options, existing)
		},
		defaultAlias,
	)
	if err != nil {
		return ModelMapping{}, err
	}
	providerModel, err := prompt.input(
		ctx,
		"Provider model (no default; back; cancel):\n",
		"Invalid provider model.\n",
		func(value string) bool {
			return len(value) <= maxPromptProviderModelBytes &&
				core.ValidateProviderModel(value) == nil
		},
		"",
	)
	if err != nil {
		return ModelMapping{}, err
	}
	return ModelMapping{
		ID: alias, Provider: name, ProviderModel: providerModel,
	}, nil
}

func (prompt *accessiblePrompt) collectGateway(
	ctx context.Context,
	initial GatewayInput,
	existing *config.Config,
) (GatewayInput, error) {
	defaultChoice := gatewayDefaultChoice(initial, existing)
	index, err := prompt.chooseDefault(
		ctx,
		"Choose Gateway authentication (back; cancel):\n",
		[]string{"key file", "environment variable", "none"},
		defaultChoice,
	)
	if err != nil {
		return GatewayInput{}, err
	}
	switch index {
	case 0:
		pathDefault := ""
		if initial.KeyFile.Set {
			pathDefault = initial.KeyFile.Value
		} else if existing != nil {
			pathDefault = existing.Server.APIKeyFile
		}
		path, err := prompt.input(
			ctx,
			"Gateway key file (empty uses the default; back; cancel):\n",
			"Invalid Gateway key file path.\n",
			func(value string) bool {
				return value == "" || (boundedPromptText(value, maxPromptLineBytes, false) &&
					filepath.IsAbs(value))
			},
			pathDefault,
		)
		if err != nil {
			return GatewayInput{}, err
		}
		gateway := GatewayInput{Auth: GatewayAuthFile, AuthSet: true}
		if path != "" {
			gateway.KeyFile = StringValue{Set: true, Value: path}
		}
		return gateway, nil
	case 1:
		environmentDefault := ""
		if initial.KeyEnv.Set {
			environmentDefault = initial.KeyEnv.Value
		} else if existing != nil {
			environmentDefault = existing.Server.APIKeyEnv
		}
		environment, err := prompt.input(
			ctx,
			"Gateway key environment name (back; cancel):\n",
			"Invalid environment name.\n",
			func(value string) bool {
				return len(value) <= maxPromptEnvironmentBytes &&
					environmentNamePattern.MatchString(value)
			},
			environmentDefault,
		)
		if err != nil {
			return GatewayInput{}, err
		}
		return GatewayInput{
			Auth: GatewayAuthEnvironment, AuthSet: true,
			KeyEnv: StringValue{Set: true, Value: environment},
		}, nil
	case 2:
		return GatewayInput{Auth: GatewayAuthNone, AuthSet: true}, nil
	default:
		return GatewayInput{}, ErrPlan
	}
}

func (prompt *accessiblePrompt) choose(
	ctx context.Context,
	title string,
	options []string,
) (int, error) {
	return prompt.chooseDefault(ctx, title, options, -1)
}

func (prompt *accessiblePrompt) chooseDefault(
	ctx context.Context,
	title string,
	options []string,
	defaultIndex int,
) (int, error) {
	if len(options) == 0 || defaultIndex >= len(options) {
		return 0, ErrPlan
	}
	var rendered strings.Builder
	rendered.WriteString(title)
	for index, option := range options {
		if !boundedPromptText(option, maxPromptLineBytes, false) {
			return 0, ErrPlan
		}
		rendered.WriteString("  ")
		rendered.WriteString(strconv.Itoa(index + 1))
		rendered.WriteString(") ")
		rendered.WriteString(option)
		if index == defaultIndex {
			rendered.WriteString(" [default]")
		}
		rendered.WriteByte('\n')
	}
	if err := prompt.write(rendered.String()); err != nil {
		return 0, err
	}
	for {
		value, err := prompt.readLine(ctx)
		if err != nil {
			return 0, err
		}
		normalized := strings.ToLower(strings.TrimSpace(value))
		switch normalized {
		case "back":
			return 0, errPromptBack
		case "cancel":
			return 0, context.Canceled
		case "":
			if defaultIndex >= 0 {
				return defaultIndex, nil
			}
		}
		index, parseErr := strconv.Atoi(normalized)
		if parseErr == nil && index >= 1 && index <= len(options) {
			return index - 1, nil
		}
		if err := prompt.write("Invalid selection. Choose a listed number.\n"); err != nil {
			return 0, err
		}
	}
}

func (prompt *accessiblePrompt) input(
	ctx context.Context,
	title string,
	validationMessage string,
	validate func(string) bool,
	defaultValue string,
) (string, error) {
	if validate == nil || title == "" || len(title) > maxPromptLineBytes ||
		validationMessage == "" || len(validationMessage) > maxPromptLineBytes ||
		defaultValue != "" && !boundedPromptText(defaultValue, maxPromptLineBytes, false) {
		return "", ErrPlan
	}
	if err := prompt.write(title); err != nil {
		return "", err
	}
	for {
		value, err := prompt.readLine(ctx)
		if err != nil {
			return "", err
		}
		trimmed := strings.TrimSpace(value)
		switch strings.ToLower(trimmed) {
		case "back":
			return "", errPromptBack
		case "cancel":
			return "", context.Canceled
		}
		if trimmed == "" && defaultValue != "" {
			trimmed = defaultValue
		}
		if validate(trimmed) {
			return trimmed, nil
		}
		if err := prompt.write(validationMessage); err != nil {
			return "", err
		}
	}
}

func (prompt *accessiblePrompt) confirm(
	ctx context.Context,
	title string,
	defaultValue bool,
) (bool, error) {
	if err := prompt.write(title); err != nil {
		return false, err
	}
	for {
		value, err := prompt.readLine(ctx)
		if err != nil {
			return false, err
		}
		switch strings.ToLower(strings.TrimSpace(value)) {
		case "":
			return defaultValue, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		case "back":
			return false, errPromptBack
		case "cancel":
			return false, context.Canceled
		default:
			if err := prompt.write("Enter yes or no.\n"); err != nil {
				return false, err
			}
		}
	}
}

func commandCandidateLabel(candidate CommandCandidate) (string, bool) {
	if !boundedPromptText(candidate.Command.Executable, maxPromptLineBytes, false) ||
		!filepath.IsAbs(candidate.Command.Executable) || len(candidate.Command.PrefixArgs) > 1 {
		return "", false
	}
	parts := []string{candidate.Command.Executable}
	for _, argument := range candidate.Command.PrefixArgs {
		if !boundedPromptText(argument, maxPromptLineBytes, false) {
			return "", false
		}
		parts = append(parts, argument)
	}
	source, ok := promptSourceLabel(candidate.Source)
	if !ok {
		return "", false
	}
	return strings.Join(parts, " ") + " (" + source + ")", true
}

func pathCandidateLabel(candidate PathCandidate) (string, bool) {
	if !boundedPromptText(candidate.Path, maxPromptLineBytes, false) {
		return "", false
	}
	source, ok := promptSourceLabel(candidate.Source)
	if !ok {
		return "", false
	}
	return candidate.Path + " (" + source + ")", true
}

func promptSourceLabel(source CandidateSource) (string, bool) {
	switch source {
	case CandidateExplicit:
		return "from flags", true
	case CandidateExisting:
		return "existing config", true
	case CandidateEnvironment:
		return "from provider environment", true
	case CandidatePATH:
		return "from PATH", true
	case CandidateConventional:
		return "conventional home", true
	case CandidateDedicated:
		return "new dedicated home", true
	default:
		return "", false
	}
}

func promptAuthLabel(name core.ProviderName, auth AuthID) (string, bool) {
	switch name {
	case core.ProviderClaude:
		switch auth {
		case AuthConfigHome:
			return "prepared config home", true
		case AuthAnthropicAPIKey:
			return "ANTHROPIC_API_KEY environment", true
		}
	case core.ProviderGemini:
		switch auth {
		case AuthGeminiAPIKey:
			return "GEMINI_API_KEY environment", true
		case AuthGoogleAPIKey:
			return "GOOGLE_API_KEY environment", true
		case AuthVertexServiceAccount:
			return "Vertex service-account environment profile", true
		}
	}
	return "", false
}

func promptCollisionTarget(target DiffTarget) string {
	if target == DiffProvider {
		return "provider"
	}
	return "model"
}

func boundedPromptText(value string, maximum int, allowEmpty bool) bool {
	if value == "" {
		return allowEmpty
	}
	return len(value) <= maximum && safeText(value)
}

func validPromptAlias(value string, name core.ProviderName) bool {
	if len(value) > maxPromptAliasBytes {
		return false
	}
	_, err := core.NewRegistry([]core.Model{{
		ID: value, Provider: name, ProviderModel: "prompt-validation",
	}})
	return err == nil
}

func promptAliasExists(alias string, options Options, existing *config.Config) bool {
	for _, model := range options.Models {
		if model.ID == alias {
			return true
		}
	}
	if existing != nil {
		for _, model := range existing.Models {
			if model.ID == alias {
				return true
			}
		}
	}
	return false
}

func promptOptionsHaveProviderModel(
	options Options,
	existing *config.Config,
	name core.ProviderName,
) bool {
	for _, model := range options.Models {
		if model.Provider == name {
			return true
		}
	}
	if existing != nil {
		for _, model := range existing.Models {
			if core.ProviderName(model.Provider) == name {
				return true
			}
		}
	}
	return false
}

func gatewayDefaultChoice(initial GatewayInput, existing *config.Config) int {
	if initial.AuthSet {
		switch initial.Auth {
		case GatewayAuthFile:
			return 0
		case GatewayAuthEnvironment:
			return 1
		case GatewayAuthNone:
			return 2
		}
	}
	if existing != nil {
		if existing.Server.APIKeyFile != "" {
			return 0
		}
		if existing.Server.APIKeyEnv != "" {
			return 1
		}
		return 2
	}
	return 0
}

func accessiblePromptReady(ctx context.Context, prompt *accessiblePrompt) error {
	if ctx == nil || prompt == nil || nilInterface(prompt.output) ||
		nilInterface(prompt.reader) {
		return ErrPlan
	}
	return ctx.Err()
}

func (prompt *accessiblePrompt) write(value string) error {
	written, err := io.WriteString(prompt.output, value)
	if err != nil || written != len(value) {
		return ErrPlan
	}
	return nil
}

func (prompt *accessiblePrompt) readLine(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	value, err := prompt.reader.ReadLine(ctx)
	var restoreErr terminalRestoreError
	if errors.As(err, &restoreErr) {
		return "", restoreErr
	}
	if contextErr := ctx.Err(); contextErr != nil {
		return "", contextErr
	}
	if err != nil {
		switch {
		case errors.Is(err, context.Canceled):
			return "", context.Canceled
		case errors.Is(err, context.DeadlineExceeded):
			return "", context.DeadlineExceeded
		case errors.Is(err, io.EOF):
			return "", io.EOF
		default:
			return "", ErrPlan
		}
	}
	if len(value) > maxPromptLineBytes {
		return "", ErrPlan
	}
	return value, nil
}

func parseProviderSelection(value string) ([]core.ProviderName, bool) {
	if len(value) > maxPromptLineBytes || !safeText(value) {
		return nil, false
	}
	fields := strings.FieldsFunc(value, func(character rune) bool {
		return character == ',' || character == ' ' || character == '\t'
	})
	if len(fields) == 0 {
		return nil, false
	}
	providers := []core.ProviderName{
		core.ProviderCodex,
		core.ProviderClaude,
		core.ProviderGemini,
	}
	selected := make([]core.ProviderName, 0, len(fields))
	seen := make(map[int]struct{}, len(fields))
	for _, field := range fields {
		index, err := strconv.Atoi(field)
		if err != nil || index < 1 || index > len(providers) {
			return nil, false
		}
		if _, duplicate := seen[index]; duplicate {
			return nil, false
		}
		seen[index] = struct{}{}
		selected = append(selected, providers[index-1])
	}
	return selected, true
}
