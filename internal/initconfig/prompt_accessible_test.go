package initconfig

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	xterm "github.com/charmbracelet/x/term"
	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

type scriptedContextLineReader struct {
	steps []func(context.Context) (string, error)
	calls int
}

func (reader *scriptedContextLineReader) ReadLine(
	ctx context.Context,
) (string, error) {
	if reader.calls >= len(reader.steps) {
		return "", io.EOF
	}
	step := reader.steps[reader.calls]
	reader.calls++
	return step(ctx)
}

func line(value string) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return value, nil }
}

func lineError(err error) func(context.Context) (string, error) {
	return func(context.Context) (string, error) { return "", err }
}

func TestAccessiblePromptSelectProvidersUsesStableNumberedInput(t *testing.T) {
	reader := &scriptedContextLineReader{steps: []func(context.Context) (string, error){
		line("4"),
		line("1, 3"),
	}}
	var output bytes.Buffer
	prompt := newAccessiblePrompt(&output, reader)

	response, err := prompt.SelectProviders(
		context.Background(), ProviderSelectionRequest{},
	)
	if err != nil {
		t.Fatalf("SelectProviders() error = %v", err)
	}
	if response.Decision != ReviewConfirm {
		t.Fatalf("Decision = %d, want ReviewConfirm", response.Decision)
	}
	want := []core.ProviderName{core.ProviderCodex, core.ProviderGemini}
	if !reflect.DeepEqual(response.Providers, want) {
		t.Fatalf("Providers = %#v, want %#v", response.Providers, want)
	}
	text := output.String()
	for _, fragment := range []string{
		"Select providers (comma-separated numbers; back; cancel):\n",
		"  1) codex\n",
		"  2) claude\n",
		"  3) gemini\n",
		"Invalid selection. Choose one or more listed numbers.\n",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("output missing %q:\n%s", fragment, text)
		}
	}
}

func TestAccessiblePromptSelectProvidersBackCancelEOFAndContext(t *testing.T) {
	tests := []struct {
		name         string
		ctx          func() context.Context
		steps        []func(context.Context) (string, error)
		wantDecision ReviewDecision
		wantErr      error
		wantCalls    int
	}{
		{
			name:         "back",
			ctx:          context.Background,
			steps:        []func(context.Context) (string, error){line("back")},
			wantDecision: ReviewBack,
			wantCalls:    1,
		},
		{
			name:      "cancel",
			ctx:       context.Background,
			steps:     []func(context.Context) (string, error){line("cancel")},
			wantErr:   context.Canceled,
			wantCalls: 1,
		},
		{
			name:      "EOF",
			ctx:       context.Background,
			steps:     []func(context.Context) (string, error){lineError(io.EOF)},
			wantErr:   io.EOF,
			wantCalls: 1,
		},
		{
			name: "canceled before read",
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			steps:     []func(context.Context) (string, error){line("1")},
			wantErr:   context.Canceled,
			wantCalls: 0,
		},
		{
			name: "canceled during read",
			ctx:  context.Background,
			steps: []func(context.Context) (string, error){func(ctx context.Context) (string, error) {
				cancelCtx, ok := ctx.(interface{ Done() <-chan struct{} })
				if !ok || cancelCtx.Done() == nil {
					return "", errors.New("test requires cancelable context")
				}
				return "1", nil
			}},
			wantErr:   nil,
			wantCalls: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			ctx := test.ctx()
			var cancel context.CancelFunc
			if test.name == "canceled during read" {
				ctx, cancel = context.WithCancel(context.Background())
				test.steps = []func(context.Context) (string, error){func(context.Context) (string, error) {
					cancel()
					return "1", nil
				}}
				test.wantErr = context.Canceled
			}
			reader := &scriptedContextLineReader{steps: test.steps}
			prompt := newAccessiblePrompt(io.Discard, reader)
			response, err := prompt.SelectProviders(ctx, ProviderSelectionRequest{})
			if !errors.Is(err, test.wantErr) || (test.wantErr == nil && err != nil) {
				t.Fatalf("SelectProviders() error = %v, want %v", err, test.wantErr)
			}
			if response.Decision != test.wantDecision {
				t.Fatalf("Decision = %d, want %d", response.Decision, test.wantDecision)
			}
			if reader.calls != test.wantCalls {
				t.Fatalf("ReadLine calls = %d, want %d", reader.calls, test.wantCalls)
			}
		})
	}
}

func TestAccessiblePromptRejectsOversizedLineBeforeParsing(t *testing.T) {
	reader := &scriptedContextLineReader{steps: []func(context.Context) (string, error){
		line(strings.Repeat("1", maxPromptLineBytes+1)),
	}}
	prompt := newAccessiblePrompt(io.Discard, reader)

	_, err := prompt.SelectProviders(context.Background(), ProviderSelectionRequest{})
	if !errors.Is(err, ErrPlan) {
		t.Fatalf("SelectProviders() error = %v, want exact ErrPlan", err)
	}
}

func TestAccessiblePromptRestoreFailureOutranksSimultaneousCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader := &scriptedContextLineReader{steps: []func(context.Context) (string, error){
		func(context.Context) (string, error) {
			cancel()
			return "", terminalRestoreError{}
		},
	}}
	prompt := newAccessiblePrompt(io.Discard, reader)

	_, err := prompt.SelectProviders(ctx, ProviderSelectionRequest{})
	var restoreErr terminalRestoreError
	if !errors.As(err, &restoreErr) || !errors.Is(err, ErrPlan) {
		t.Fatalf("SelectProviders() error = %v, want terminalRestoreError wrapping ErrPlan", err)
	}
}

func TestAccessiblePromptTerminalLineReaderCancelsBlockedRead(t *testing.T) {
	const childEnvironment = "AI_CLI_GATEWAY_ACCESSIBLE_PTY_CHILD"
	if os.Getenv(childEnvironment) == "1" {
		runAccessibleBlockedReadPTYChild(t, os.Stdin, os.Stderr)
		return
	}
	if runtime.GOOS == "darwin" {
		if _, err := os.Stat("/usr/bin/script"); err != nil {
			t.Skipf("script utility unavailable: %v", err)
		}
		runPromptPTYParent(
			t,
			childEnvironment+"=1",
			"^TestAccessiblePromptTerminalLineReaderCancelsBlockedRead$",
			nil,
		)
		return
	}
	if runtime.GOOS == "windows" {
		if !IsInteractiveTerminal(os.Stdin, os.Stderr) {
			t.Skip("test process has no interactive Windows console")
		}
		runAccessibleBlockedReadPTYChild(t, os.Stdin, os.Stderr)
		return
	}
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no controlling terminal: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })
	if !IsInteractiveTerminal(terminal, terminal) {
		t.Skip("controlling terminal is not interactive")
	}
	runAccessibleBlockedReadPTYChild(t, terminal, terminal)
}

func runAccessibleBlockedReadPTYChild(
	t *testing.T,
	input *os.File,
	output *os.File,
) {
	t.Helper()
	if !IsInteractiveTerminal(input, output) {
		t.Fatal("blocked-read test streams are not terminals")
	}
	before, err := xterm.GetState(input.Fd())
	if err != nil {
		t.Fatalf("GetState before accessible read: %v", err)
	}

	reader := newContextLineReader(input, output)
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, readErr := reader.ReadLine(ctx)
		result <- readErr
	}()
	t.Cleanup(cancel)
	time.Sleep(75 * time.Millisecond)
	cancel()

	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ReadLine() error = %v, want exact context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("ReadLine did not return after cancellation")
	}
	after, err := xterm.GetState(input.Fd())
	if err != nil {
		t.Fatalf("GetState after accessible read: %v", err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("accessible read changed terminal state: before=%#v after=%#v", before, after)
	}
}

func TestAccessiblePromptCollectGatewayKeyOnlyPreservesProviderAnswers(t *testing.T) {
	initial := Options{
		Providers: []core.ProviderName{core.ProviderCodex},
		Provider: map[core.ProviderName]ProviderInput{
			core.ProviderCodex: {
				Executable: StringValue{Set: true, Value: "/opt/bin/codex"},
				ConfigHome: StringValue{Set: true, Value: "/srv/codex"},
			},
		},
		Models: []ModelMapping{{
			ID: "codex-local", Provider: core.ProviderCodex, ProviderModel: "gpt-user",
		}},
	}
	reader := &scriptedContextLineReader{steps: []func(context.Context) (string, error){
		line("2"),
		line("not-valid"),
		line("AI_CLI_GATEWAY_API_KEY"),
	}}
	var output bytes.Buffer
	prompt := newAccessiblePrompt(&output, reader)

	response, err := prompt.Collect(context.Background(), CollectRequest{
		Initial:   initial,
		Existing:  &config.Config{},
		Discovery: nil,
	})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if !reflect.DeepEqual(response.Options.Providers, initial.Providers) ||
		!reflect.DeepEqual(response.Options.Provider, initial.Provider) ||
		!reflect.DeepEqual(response.Options.Models, initial.Models) {
		t.Fatalf("key-only collection changed provider answers: %#v", response.Options)
	}
	wantGateway := GatewayInput{
		Auth:    GatewayAuthEnvironment,
		AuthSet: true,
		KeyEnv:  StringValue{Set: true, Value: "AI_CLI_GATEWAY_API_KEY"},
	}
	if !reflect.DeepEqual(response.Options.Gateway, wantGateway) {
		t.Fatalf("Gateway = %#v, want %#v", response.Options.Gateway, wantGateway)
	}
	if !strings.Contains(output.String(), "Invalid environment name.\n") {
		t.Fatalf("output did not report fixed validation error:\n%s", output.String())
	}
}

func TestAccessiblePromptGatewayKeyFileRejectsRelativeThenAcceptsAbsolute(t *testing.T) {
	absolutePath := testAbsolutePath("gateway.key")
	reader := &scriptedContextLineReader{steps: []func(context.Context) (string, error){
		line("1"),
		line("relative.key"),
		line(absolutePath),
	}}
	var output bytes.Buffer
	prompt := newAccessiblePrompt(&output, reader)

	response, err := prompt.Collect(context.Background(), CollectRequest{})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	if got := response.Options.Gateway.KeyFile; !got.Set || got.Value != absolutePath {
		t.Fatalf("Gateway.KeyFile = %#v, want absolute corrected path %q", got, absolutePath)
	}
	if !strings.Contains(output.String(), "Invalid Gateway key file path.") {
		t.Fatalf("output did not report the rejected relative path:\n%s", output.String())
	}
}

func TestAccessiblePromptCollectUsesDiscoveryLabelsAndMultipleUserModels(t *testing.T) {
	executable := testAbsolutePath("bin", "codex")
	configHome := testAbsolutePath("homes", "codex")
	reader := &scriptedContextLineReader{steps: []func(context.Context) (string, error){
		line("1"), // PATH command.
		line("1"), // Existing config home.
		line(""),  // codex-local alias default.
		line("gpt-user"),
		line("yes"),
		line("codex-deep"),
		line("gpt-deep-user"),
		line("no"),
		line("3"), // Gateway auth disabled.
	}}
	var output bytes.Buffer
	prompt := newAccessiblePrompt(&output, reader)

	response, err := prompt.Collect(context.Background(), CollectRequest{
		Initial: Options{Providers: []core.ProviderName{core.ProviderCodex}},
		Discovery: map[core.ProviderName]ProviderDiscovery{
			core.ProviderCodex: {
				Commands: []CommandCandidate{{
					Command: ProviderCommand{Executable: executable},
					Source:  CandidatePATH,
				}},
				ConfigHomes: []PathCandidate{{
					Path: configHome, Source: CandidateExisting,
				}},
				AuthChoices: []AuthID{AuthConfigHome},
			},
		},
	})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	input := response.Options.Provider[core.ProviderCodex]
	if input.Executable != (StringValue{Set: true, Value: executable}) ||
		input.ConfigHome != (StringValue{Set: true, Value: configHome}) {
		t.Fatalf("provider input = %#v", input)
	}
	wantModels := []ModelMapping{
		{ID: "codex-local", Provider: core.ProviderCodex, ProviderModel: "gpt-user"},
		{ID: "codex-deep", Provider: core.ProviderCodex, ProviderModel: "gpt-deep-user"},
	}
	if !reflect.DeepEqual(response.Options.Models, wantModels) {
		t.Fatalf("Models = %#v, want %#v", response.Options.Models, wantModels)
	}
	if response.Options.Gateway != (GatewayInput{Auth: GatewayAuthNone, AuthSet: true}) {
		t.Fatalf("Gateway = %#v", response.Options.Gateway)
	}
	text := output.String()
	for _, label := range []string{executable + " (from PATH)", configHome + " (existing config)"} {
		if !strings.Contains(text, label) {
			t.Fatalf("output missing source label %q:\n%s", label, text)
		}
	}
	if strings.Contains(text, "provider model [") {
		t.Fatalf("provider model was presented with a guessed default:\n%s", text)
	}
}

func TestAccessiblePromptCollectModelAllowsExistingAliasForProviderChange(t *testing.T) {
	executable := testAbsolutePath("bin", "codex")
	configHome := testAbsolutePath("homes", "codex")
	existing := &config.Config{Models: []config.Model{{
		ID: "shared-existing", Provider: "claude", ProviderModel: "claude-existing",
	}}}
	reader := &scriptedContextLineReader{steps: []func(context.Context) (string, error){
		line("1"),
		line("1"),
		line("shared-existing"),
		line("gpt-replacement"),
		line("no"),
		line("3"),
	}}
	prompt := newAccessiblePrompt(io.Discard, reader)

	response, err := prompt.Collect(context.Background(), CollectRequest{
		Initial:  Options{Providers: []core.ProviderName{core.ProviderCodex}},
		Existing: existing,
		Discovery: map[core.ProviderName]ProviderDiscovery{
			core.ProviderCodex: {
				Commands: []CommandCandidate{{
					Command: ProviderCommand{Executable: executable}, Source: CandidatePATH,
				}},
				ConfigHomes: []PathCandidate{{Path: configHome, Source: CandidateExisting}},
				AuthChoices: []AuthID{AuthConfigHome},
			},
		},
	})
	if err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	want := []ModelMapping{{
		ID: "shared-existing", Provider: core.ProviderCodex, ProviderModel: "gpt-replacement",
	}}
	if !reflect.DeepEqual(response.Options.Models, want) {
		t.Fatalf("Collect().Models = %#v, want %#v", response.Options.Models, want)
	}
}

func TestAccessiblePromptCollectModelRejectsAliasAlreadyCollectedThisSession(t *testing.T) {
	options := Options{Models: []ModelMapping{{
		ID: "codex-local", Provider: core.ProviderCodex, ProviderModel: "gpt-first",
	}}}
	reader := &scriptedContextLineReader{steps: []func(context.Context) (string, error){
		line("codex-local"),
		line("codex-second"),
		line("gpt-second"),
	}}
	var output bytes.Buffer
	prompt := newAccessiblePrompt(&output, reader)

	model, err := prompt.collectModel(
		context.Background(), core.ProviderCodex, options,
	)
	if err != nil {
		t.Fatalf("collectModel() error = %v", err)
	}
	if model.ID != "codex-second" {
		t.Fatalf("collectModel().ID = %q, want corrected alias", model.ID)
	}
	if !strings.Contains(output.String(), "Invalid or duplicate model alias.\n") {
		t.Fatalf("duplicate alias was not rejected:\n%s", output.String())
	}
}

func TestAccessiblePromptCollectUsesClosedClaudeAndGeminiAuthChoices(t *testing.T) {
	tests := []struct {
		name        string
		provider    core.ProviderName
		authChoices []AuthID
		authInput   string
		wantAuth    AuthID
	}{
		{
			name:        "Claude API key environment name only",
			provider:    core.ProviderClaude,
			authChoices: []AuthID{AuthConfigHome, AuthAnthropicAPIKey},
			authInput:   "2",
			wantAuth:    AuthAnthropicAPIKey,
		},
		{
			name:     "Gemini Vertex profile name only",
			provider: core.ProviderGemini,
			authChoices: []AuthID{
				AuthGeminiAPIKey, AuthGoogleAPIKey, AuthVertexServiceAccount,
			},
			authInput: "3",
			wantAuth:  AuthVertexServiceAccount,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executable := testAbsolutePath("bin", string(test.provider))
			configHome := testAbsolutePath("homes", string(test.provider))
			reader := &scriptedContextLineReader{steps: []func(context.Context) (string, error){
				line("1"),
				line("1"),
				line(test.authInput),
				line(""),
				line("user-chosen-model"),
				line("no"),
				line("3"),
			}}
			prompt := newAccessiblePrompt(io.Discard, reader)
			response, err := prompt.Collect(context.Background(), CollectRequest{
				Initial: Options{Providers: []core.ProviderName{test.provider}},
				Discovery: map[core.ProviderName]ProviderDiscovery{
					test.provider: {
						Commands: []CommandCandidate{{
							Command: ProviderCommand{Executable: executable},
							Source:  CandidatePATH,
						}},
						ConfigHomes: []PathCandidate{{
							Path: configHome, Source: CandidateConventional,
						}},
						AuthChoices: test.authChoices,
					},
				},
			})
			if err != nil {
				t.Fatalf("Collect() error = %v", err)
			}
			input := response.Options.Provider[test.provider]
			if !input.AuthSet || input.Auth != test.wantAuth {
				t.Fatalf("provider auth = %#v, want %q", input, test.wantAuth)
			}
		})
	}
}

func TestAccessiblePromptReviewCollectsCollisionsAndConfirms(t *testing.T) {
	reader := &scriptedContextLineReader{steps: []func(context.Context) (string, error){
		line("1"), // Replace provider.
		line("2"), // Keep model.
		line("1"), // Confirm collision choices.
	}}
	prompt := newAccessiblePrompt(io.Discard, reader)
	request := ReviewRequest{Collisions: []Collision{
		{Target: DiffProvider, Name: "codex"},
		{Target: DiffModel, Name: "codex-local"},
	}}

	response, err := prompt.Review(context.Background(), request)
	if err != nil {
		t.Fatalf("Review() error = %v", err)
	}
	if response.Decision != ReviewConfirm {
		t.Fatalf("Decision = %d, want ReviewConfirm", response.Decision)
	}
	want := []CollisionDecision{
		{Target: DiffProvider, Name: "codex", Choice: CollisionReplace},
		{Target: DiffModel, Name: "codex-local", Choice: CollisionKeepExisting},
	}
	if !reflect.DeepEqual(response.Collisions, want) {
		t.Fatalf("Collisions = %#v, want %#v", response.Collisions, want)
	}
}

func TestAccessiblePromptReviewBackAndConfirmKeyActions(t *testing.T) {
	t.Run("review back", func(t *testing.T) {
		prompt := newAccessiblePrompt(io.Discard, &scriptedContextLineReader{
			steps: []func(context.Context) (string, error){line("2")},
		})
		response, err := prompt.Review(context.Background(), ReviewRequest{})
		if err != nil || response.Decision != ReviewBack {
			t.Fatalf("Review() = %#v, %v, want ReviewBack", response, err)
		}
	})
	t.Run("final decline", func(t *testing.T) {
		prompt := newAccessiblePrompt(io.Discard, &scriptedContextLineReader{
			steps: []func(context.Context) (string, error){line("3")},
		})
		response, err := prompt.Review(context.Background(), ReviewRequest{})
		if err != nil || response.Decision != ReviewDecline {
			t.Fatalf("Review() = %#v, %v, want ReviewDecline", response, err)
		}
	})

	for _, test := range []struct {
		name string
		line string
		want ReviewDecision
	}{
		{name: "confirm", line: "yes", want: ReviewConfirm},
		{name: "decline", line: "no", want: ReviewDecline},
		{name: "back", line: "back", want: ReviewBack},
	} {
		t.Run("key "+test.name, func(t *testing.T) {
			prompt := newAccessiblePrompt(io.Discard, &scriptedContextLineReader{
				steps: []func(context.Context) (string, error){line(test.line)},
			})
			decision, err := prompt.ConfirmKeyAction(
				context.Background(),
				KeyConfirmationRequest{Kind: ConfirmOrphanReuse, Path: "/srv/gateway.key"},
			)
			if err != nil || decision != test.want {
				t.Fatalf("ConfirmKeyAction() = %d, %v, want %d", decision, err, test.want)
			}
		})
	}
}
