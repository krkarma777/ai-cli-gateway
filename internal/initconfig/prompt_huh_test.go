package initconfig

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
	xterm "github.com/charmbracelet/x/term"
	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestHuhPromptDependencyStartupHasNoDebugLogSideEffect(t *testing.T) {
	const childEnvironment = "AI_CLI_GATEWAY_HUH_STARTUP_CHILD"
	if os.Getenv(childEnvironment) == "1" {
		if _, err := os.Stat("tea_debug.log"); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("dependency startup created tea_debug.log: %v", err)
		}
		if flags := log.Flags(); flags != log.LstdFlags {
			t.Fatalf("dependency startup changed standard logger flags to %d", flags)
		}
		if writer := log.Writer(); writer != io.Writer(os.Stderr) {
			t.Fatalf("dependency startup changed standard logger output to %T", writer)
		}
		return
	}

	workingDirectory := t.TempDir()
	//nolint:gosec // The test re-executes its own binary with a fixed test selector.
	command := exec.CommandContext(
		t.Context(),
		os.Args[0],
		"-test.run=^TestHuhPromptDependencyStartupHasNoDebugLogSideEffect$",
	)
	command.Dir = workingDirectory
	command.Env = append(os.Environ(), childEnvironment+"=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("startup child failed: %v\n%s", err, output)
	}
	if _, err := os.Stat(filepath.Join(workingDirectory, "tea_debug.log")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("dependency startup left tea_debug.log: %v", err)
	}
}

func TestHuhPromptSelectProvidersVisualModeValidationAndBack(t *testing.T) {
	t.Run("multi-select remains visual under TERM dumb", func(t *testing.T) {
		t.Setenv("TERM", "dumb")
		// Enter first proves the empty selection is rejected. Space selects
		// Codex, Down+Space selects Claude, Enter advances, Enter confirms.
		input := bytes.NewBufferString("\r \x1b[B \r\r")
		prompt := newHuhPrompt(input, io.Discard)

		response, err := prompt.SelectProviders(
			context.Background(), ProviderSelectionRequest{},
		)
		if err != nil {
			t.Fatalf("SelectProviders() error = %v", err)
		}
		want := []core.ProviderName{core.ProviderCodex, core.ProviderClaude}
		if response.Decision != ReviewConfirm ||
			!reflect.DeepEqual(response.Providers, want) {
			t.Fatalf("SelectProviders() = %#v, want %#v", response, want)
		}
	})

	t.Run("action can go back", func(t *testing.T) {
		// Select Codex, advance to the inline action, move right to Back.
		t.Setenv("TERM", "dumb")
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		output := newHuhRenderSignalWriter(
			"Select providers",
			"Provider selection action",
		)
		input := &renderGatedTestReader{
			ctx:    ctx,
			ready:  output.ready,
			chunks: []string{" \r", "l\r"},
		}
		prompt := newHuhPrompt(input, output)
		response, err := prompt.SelectProviders(
			ctx, ProviderSelectionRequest{},
		)
		if err != nil {
			t.Fatalf("SelectProviders() error = %v\nrendered output:\n%s", err, output.String())
		}
		if response.Decision != ReviewBack || len(response.Providers) != 0 {
			t.Fatalf("SelectProviders() = %#v, want ReviewBack", response)
		}
	})
}

type huhRenderSignalWriter struct {
	mutex    sync.Mutex
	output   bytes.Buffer
	markers  [][]byte
	ready    []chan struct{}
	signaled []bool
}

func newHuhRenderSignalWriter(markers ...string) *huhRenderSignalWriter {
	writer := &huhRenderSignalWriter{
		markers:  make([][]byte, len(markers)),
		ready:    make([]chan struct{}, len(markers)),
		signaled: make([]bool, len(markers)),
	}
	for index, marker := range markers {
		writer.markers[index] = []byte(marker)
		writer.ready[index] = make(chan struct{})
	}
	return writer
}

func (writer *huhRenderSignalWriter) Write(value []byte) (int, error) {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	written, err := writer.output.Write(value)
	for index, marker := range writer.markers {
		if !writer.signaled[index] && bytes.Contains(writer.output.Bytes(), marker) {
			writer.signaled[index] = true
			close(writer.ready[index])
		}
	}
	return written, err
}

func (writer *huhRenderSignalWriter) String() string {
	writer.mutex.Lock()
	defer writer.mutex.Unlock()
	return writer.output.String()
}

type renderGatedTestReader struct {
	ctx    context.Context
	ready  []chan struct{}
	chunks []string
	next   int
	offset int
	mutex  sync.Mutex
}

func (reader *renderGatedTestReader) Read(buffer []byte) (int, error) {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if reader.next >= len(reader.chunks) || reader.next >= len(reader.ready) {
		return 0, io.EOF
	}
	if reader.offset == 0 {
		select {
		case <-reader.ready[reader.next]:
		case <-reader.ctx.Done():
			return 0, reader.ctx.Err()
		}
	}
	chunk := reader.chunks[reader.next]
	count := copy(buffer, chunk[reader.offset:])
	reader.offset += count
	if reader.offset == len(chunk) {
		reader.next++
		reader.offset = 0
	}
	return count, nil
}

func TestHuhPromptMapsAbortAndContextCancellation(t *testing.T) {
	t.Run("Ctrl-C", func(t *testing.T) {
		prompt := newHuhPrompt(bytes.NewBuffer([]byte{3}), io.Discard)
		_, err := prompt.SelectProviders(
			context.Background(), ProviderSelectionRequest{},
		)
		if !reflect.DeepEqual(err, context.Canceled) {
			t.Fatalf("SelectProviders() error = %v, want exact context.Canceled", err)
		}
	})

	t.Run("context canceled before form", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		prompt := newHuhPrompt(bytes.NewBufferString(" \r\r"), io.Discard)
		_, err := prompt.SelectProviders(ctx, ProviderSelectionRequest{})
		if !reflect.DeepEqual(err, context.Canceled) {
			t.Fatalf("SelectProviders() error = %v, want exact context.Canceled", err)
		}
	})
}

func TestHuhProgramModelContextCancellationAbortsGracefully(t *testing.T) {
	value := ""
	form := huh.NewForm(huh.NewGroup(huh.NewInput().Value(&value)))
	form.SubmitCmd = tea.Quit
	form.CancelCmd = tea.Quit
	model := huhProgramModel{model: form}

	next, command := model.Update(huhContextCanceledMsg{})
	final, ok := next.(huhProgramModel)
	if !ok {
		t.Fatalf("Update() model = %T, want huhProgramModel", next)
	}
	finalForm, ok := final.model.(*huh.Form)
	if !ok {
		t.Fatalf("inner model = %T, want *huh.Form", final.model)
	}
	if finalForm.State != huh.StateAborted {
		t.Fatalf("Form.State = %v, want StateAborted", finalForm.State)
	}
	if command == nil {
		t.Fatal("Update() command = nil, want graceful tea.Quit")
	}
	commandMessage := command()
	if _, ok := commandMessage.(tea.QuitMsg); !ok {
		t.Fatalf("Update() command message = %T, want tea.QuitMsg", commandMessage)
	}
	if view := final.View(); !view.ReportFocus {
		t.Fatal("View().ReportFocus = false, want true")
	}
}

func TestHuhPromptRunFinalizationRestoreFailureOutranksCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cause := errors.New("planted Huh terminal restore failure")

	err := finalizeHuhRun(ctx, nil, cause)
	var restoreErr terminalRestoreError
	if !errors.As(err, &restoreErr) || !errors.Is(err, ErrPlan) || !errors.Is(err, cause) {
		t.Fatalf("finalizeHuhRun() error = %v, want terminalRestoreError wrapping cause and ErrPlan", err)
	}
}

func TestHuhPromptCollectUsesSelectInputConfirmAndMultipleModels(t *testing.T) {
	executable := testAbsolutePath("bin", "codex")
	configHome := testAbsolutePath("homes", "codex")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	input := newFormHuhInput(
		[]string{"\r", "\r", "\r"}, // Discovered command, home, continue.
		[]string{"\r", "gpt-user\r", "y", "\r"},
		[]string{"codex-deep\r", "gpt-deep-user\r", "n", "\r"},
		[]string{"j", "j", "\r", "\r"}, // Gateway auth none, continue.
	)
	prompt := newHuhPrompt(input, io.Discard)

	response, err := prompt.Collect(ctx, CollectRequest{
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
}

func TestHuhPromptCollectModelAllowsExistingAliasForProviderChange(t *testing.T) {
	executable := testAbsolutePath("bin", "codex")
	configHome := testAbsolutePath("homes", "codex")
	existing := &config.Config{Models: []config.Model{{
		ID: "shared-existing", Provider: "claude", ProviderModel: "claude-existing",
	}}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var output bytes.Buffer
	prompt := newHuhPrompt(newFormHuhInputWithDelay(time.Second,
		[]string{"\r", "\r", "\r"}, // Discovered command, home, continue.
		[]string{"\x15shared-existing\r", "gpt-replacement\r", "n", "\r"},
		[]string{"\r", "\r"}, // Existing config defaults Gateway auth to none; continue.
	), &output)

	response, err := prompt.Collect(ctx, CollectRequest{
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
		t.Fatalf("Collect() error = %v\nrendered output:\n%s", err, output.String())
	}
	want := []ModelMapping{{
		ID: "shared-existing", Provider: core.ProviderCodex, ProviderModel: "gpt-replacement",
	}}
	if !reflect.DeepEqual(response.Options.Models, want) {
		t.Fatalf("Collect().Models = %#v, want %#v", response.Options.Models, want)
	}
}

func TestHuhPromptCollectModelRejectsAliasAlreadyCollectedThisSession(t *testing.T) {
	options := Options{Models: []ModelMapping{{
		ID: "codex-local", Provider: core.ProviderCodex, ProviderModel: "gpt-first",
	}}}
	prompt := newHuhPrompt(newFormHuhInput([]string{
		"codex-local\r",
		"\x15codex-second\r",
		"gpt-second\r",
		"n",
		"\r",
	}), io.Discard)

	model, another, decision, err := prompt.collectModelForm(
		context.Background(), core.ProviderCodex, options,
	)
	if err != nil {
		t.Fatalf("collectModelForm() error = %v", err)
	}
	if model.ID != "codex-second" || another || decision != ReviewConfirm {
		t.Fatalf("collectModelForm() = %#v, %t, %d; want corrected alias",
			model, another, decision)
	}
}

func TestHuhPromptGatewayKeyFileRejectsRelativeThenAcceptsAbsolute(t *testing.T) {
	absolutePath := testAbsolutePath("gateway.key")
	prompt := newHuhPrompt(newFormHuhInput([]string{
		"\r",
		"relative.key\r",
		"\x15" + absolutePath + "\r",
		"\r",
	}), io.Discard)

	gateway, decision, err := prompt.collectGatewayForm(
		context.Background(), GatewayInput{}, nil,
	)
	if err != nil {
		t.Fatalf("collectGatewayForm() error = %v", err)
	}
	if decision != ReviewConfirm {
		t.Fatalf("decision = %d, want ReviewConfirm", decision)
	}
	if got := gateway.KeyFile; !got.Set || got.Value != absolutePath {
		t.Fatalf("Gateway.KeyFile = %#v, want absolute corrected path %q", got, absolutePath)
	}
}

func TestHuhPromptReviewAndKeyConfirmation(t *testing.T) {
	t.Run("collision choices", func(t *testing.T) {
		prompt := newHuhPrompt(newFormHuhInput([]string{"\r", "l\r", "\r"}), io.Discard)
		response, err := prompt.Review(context.Background(), ReviewRequest{
			Collisions: []Collision{
				{Target: DiffProvider, Name: "codex"},
				{Target: DiffModel, Name: "codex-local"},
			},
		})
		if err != nil {
			t.Fatalf("Review() error = %v", err)
		}
		want := ReviewResponse{
			Decision: ReviewConfirm,
			Collisions: []CollisionDecision{
				{Target: DiffProvider, Name: "codex", Choice: CollisionReplace},
				{Target: DiffModel, Name: "codex-local", Choice: CollisionKeepExisting},
			},
		}
		if !reflect.DeepEqual(response, want) {
			t.Fatalf("Review() = %#v, want %#v", response, want)
		}
	})

	t.Run("final decline", func(t *testing.T) {
		prompt := newHuhPrompt(bytes.NewBufferString("ll\r"), io.Discard)
		response, err := prompt.Review(context.Background(), ReviewRequest{})
		if err != nil || response.Decision != ReviewDecline {
			t.Fatalf("Review() = %#v, %v, want ReviewDecline", response, err)
		}
	})

	t.Run("key decline", func(t *testing.T) {
		prompt := newHuhPrompt(newFormHuhInput([]string{"n", "\r"}), io.Discard)
		decision, err := prompt.ConfirmKeyAction(context.Background(), KeyConfirmationRequest{
			Kind: ConfirmMissingConfiguredKeyCreation,
			Path: "/srv/gateway.key",
		})
		if err != nil || decision != ReviewDecline {
			t.Fatalf("ConfirmKeyAction() = %d, %v, want ReviewDecline", decision, err)
		}
	})
}

func TestHuhPromptPreviousGroupNavigationRevisesProviderChoice(t *testing.T) {
	firstExecutable := testAbsolutePath("bin", "codex-a")
	secondExecutable := testAbsolutePath("bin", "codex-b")
	configHome := testAbsolutePath("homes", "codex")
	// Enter first reaches config home; Shift+Tab emits Huh's PrevGroup
	// navigation and revises the command before completing the form.
	prompt := newHuhPrompt(
		newFormHuhInput([]string{"\r", "\x1b[Z", "j", "\r", "\r", "\r"}),
		io.Discard,
	)
	input, decision, err := prompt.collectProviderForm(
		context.Background(),
		core.ProviderCodex,
		ProviderDiscovery{
			Commands: []CommandCandidate{
				{Command: ProviderCommand{Executable: firstExecutable}, Source: CandidatePATH},
				{Command: ProviderCommand{Executable: secondExecutable}, Source: CandidateExisting},
			},
			ConfigHomes: []PathCandidate{{Path: configHome, Source: CandidateExisting}},
			AuthChoices: []AuthID{AuthConfigHome},
		},
	)
	if err != nil {
		t.Fatalf("collectProviderForm() error = %v", err)
	}
	if decision != ReviewConfirm || input.Executable.Value != secondExecutable {
		t.Fatalf("collectProviderForm() = %#v, %d", input, decision)
	}
}

func TestHuhPromptCustomEntrypointUsesFinalWindowsExecutable(t *testing.T) {
	t.Run("stale entrypoint is ignored after changing to a native executable", func(t *testing.T) {
		got := customEntrypointForExecutable(
			"windows",
			`C:\\Tools\\claude.exe`,
			`C:\\Tools\\claude.js`,
		)
		if got != (StringValue{}) {
			t.Fatalf("customEntrypointForExecutable() = %#v, want unset", got)
		}
	})

	t.Run("node entrypoint is preserved", func(t *testing.T) {
		const entrypoint = `C:\\Tools\\claude.mjs`
		got := customEntrypointForExecutable(
			"windows",
			`C:\\Program Files\\nodejs\\NoDe.ExE`,
			entrypoint,
		)
		want := StringValue{Set: true, Value: entrypoint}
		if got != want {
			t.Fatalf("customEntrypointForExecutable() = %#v, want %#v", got, want)
		}
	})

	t.Run("non-Windows custom executable never uses an entrypoint", func(t *testing.T) {
		got := customEntrypointForExecutable("darwin", "/usr/local/bin/node.exe", "/srv/cli.js")
		if got != (StringValue{}) {
			t.Fatalf("customEntrypointForExecutable() = %#v, want unset", got)
		}
	})
}

func TestHuhPromptContextCancellationDuringBlockedRead(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, promptErr := newHuhPrompt(reader, io.Discard).SelectProviders(
			ctx, ProviderSelectionRequest{},
		)
		result <- promptErr
	}()
	time.Sleep(75 * time.Millisecond)
	cancel()

	select {
	case err := <-result:
		if !reflect.DeepEqual(err, context.Canceled) {
			t.Fatalf("SelectProviders() error = %v, want exact context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SelectProviders did not return after context cancellation")
	}
}

func TestHuhPromptContextDeadlineDuringBlockedRead(t *testing.T) {
	reader, writer := io.Pipe()
	t.Cleanup(func() {
		_ = reader.Close()
		_ = writer.Close()
	})
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, promptErr := newHuhPrompt(reader, io.Discard).SelectProviders(
			ctx, ProviderSelectionRequest{},
		)
		result <- promptErr
	}()

	select {
	case err := <-result:
		if !reflect.DeepEqual(err, context.DeadlineExceeded) {
			t.Fatalf("SelectProviders() error = %v, want exact context.DeadlineExceeded", err)
		}
	case <-time.After(time.Second):
		t.Fatal("SelectProviders did not return after context deadline")
	}
}

func TestHuhPromptPTY(t *testing.T) {
	if mode := os.Getenv("AI_CLI_GATEWAY_HUH_PTY_CHILD"); mode != "" {
		runHuhPromptPTYChild(t, mode)
		return
	}
	if runtime.GOOS != "darwin" {
		t.Skip("native PTY smoke uses the macOS script utility")
	}
	if _, err := os.Stat("/usr/bin/script"); err != nil {
		t.Skipf("script utility unavailable: %v", err)
	}

	tests := []struct {
		name   string
		mode   string
		chunks []string
	}{
		{
			name: "arrow space enter",
			mode: "complete",
			chunks: []string{
				" ", "\x1b[B", " ", "\r", "\r",
			},
		},
		{
			name: "collect across forms",
			mode: "collect",
			chunks: []string{
				"\r", "\r", "\r",
				"\r", "gpt-user\r", "y", "\r",
				"codex-deep\r", "gpt-deep-user\r", "n", "\r",
				"j", "j", "\r", "\r",
			},
		},
		{
			name:   "back",
			mode:   "back",
			chunks: []string{" ", "\r", "l", "\r"},
		},
		{
			name:   "Ctrl-C",
			mode:   "abort",
			chunks: []string{"\x03"},
		},
		{
			name: "context cancellation",
			mode: "context-cancel",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runHuhPromptPTYParent(t, test.mode, test.chunks)
		})
	}
}

func runHuhPromptPTYChild(t *testing.T, mode string) {
	t.Helper()
	if !IsInteractiveTerminal(os.Stdin, os.Stderr) {
		t.Fatal("script child streams are not terminals")
	}
	before, err := xterm.GetState(os.Stdin.Fd())
	if err != nil {
		t.Fatalf("GetState before prompt: %v", err)
	}
	prompt := newHuhPrompt(os.Stdin, os.Stderr)
	if mode == "collect" {
		response, collectErr := prompt.Collect(context.Background(), CollectRequest{
			Initial: Options{Providers: []core.ProviderName{core.ProviderCodex}},
			Discovery: map[core.ProviderName]ProviderDiscovery{
				core.ProviderCodex: {
					Commands: []CommandCandidate{{
						Command: ProviderCommand{Executable: "/opt/bin/codex"},
						Source:  CandidatePATH,
					}},
					ConfigHomes: []PathCandidate{{
						Path: "/srv/codex", Source: CandidateExisting,
					}},
					AuthChoices: []AuthID{AuthConfigHome},
				},
			},
		})
		assertHuhPTYTerminalRestored(t, before)
		if collectErr != nil || len(response.Options.Models) != 2 ||
			response.Options.Gateway.Auth != GatewayAuthNone {
			t.Fatalf("Collect() = %#v, %v", response, collectErr)
		}
		return
	}
	ctx := context.Background()
	if mode == "context-cancel" {
		cancelCtx, cancel := context.WithCancel(ctx)
		ctx = cancelCtx
		defer cancel()
		go func() {
			time.Sleep(75 * time.Millisecond)
			cancel()
		}()
	}
	response, promptErr := prompt.SelectProviders(ctx, ProviderSelectionRequest{})
	assertHuhPTYTerminalRestored(t, before)
	switch mode {
	case "complete":
		want := []core.ProviderName{core.ProviderCodex, core.ProviderClaude}
		if promptErr != nil || response.Decision != ReviewConfirm ||
			!reflect.DeepEqual(response.Providers, want) {
			t.Fatalf("SelectProviders() = %#v, %v", response, promptErr)
		}
	case "back":
		if promptErr != nil || response.Decision != ReviewBack {
			t.Fatalf("SelectProviders() = %#v, %v, want back", response, promptErr)
		}
	case "abort":
		if !reflect.DeepEqual(promptErr, context.Canceled) {
			t.Fatalf("SelectProviders() error = %v, want exact context.Canceled", promptErr)
		}
	case "context-cancel":
		if !reflect.DeepEqual(promptErr, context.Canceled) {
			t.Fatalf("SelectProviders() error = %v, want exact context.Canceled", promptErr)
		}
	default:
		t.Fatalf("unknown PTY child mode %q", mode)
	}
}

func assertHuhPTYTerminalRestored(t *testing.T, before *xterm.State) {
	t.Helper()
	after, stateErr := xterm.GetState(os.Stdin.Fd())
	if stateErr != nil {
		t.Fatalf("GetState after prompt: %v", stateErr)
	}
	if !equivalentRestoredTerminalState(before, after) {
		t.Fatalf("terminal state was not restored: before=%#v after=%#v", before, after)
	}
}

func equivalentRestoredTerminalState(before, after *xterm.State) bool {
	if reflect.DeepEqual(before, after) {
		return true
	}
	if runtime.GOOS != "darwin" {
		return false
	}
	return equivalentDarwinTerminalValue(reflect.ValueOf(before), reflect.ValueOf(after))
}

func equivalentDarwinTerminalValue(before, after reflect.Value) bool {
	if !before.IsValid() || !after.IsValid() || before.Kind() != after.Kind() {
		return before.IsValid() == after.IsValid()
	}
	switch before.Kind() {
	case reflect.Pointer:
		if before.IsNil() || after.IsNil() {
			return before.IsNil() == after.IsNil()
		}
		return equivalentDarwinTerminalValue(before.Elem(), after.Elem())
	case reflect.Struct:
		if before.Type() != after.Type() {
			return false
		}
		for index := 0; index < before.NumField(); index++ {
			field := before.Type().Field(index)
			beforeField := before.Field(index)
			afterField := after.Field(index)
			// Darwin marks PENDIN itself when canonical mode is restored after a
			// raw transition. termios(4) documents it as a kernel-managed state
			// bit; a second successful TIOCSETA restore leaves it set.
			if before.Type().Name() == "Termios" && field.Name == "Lflag" {
				const darwinPENDIN = uint64(0x20000000)
				if afterField.Uint() == beforeField.Uint()|darwinPENDIN {
					continue
				}
			}
			if !equivalentDarwinTerminalValue(beforeField, afterField) {
				return false
			}
		}
		return true
	case reflect.Array, reflect.Slice:
		if before.Len() != after.Len() {
			return false
		}
		for index := 0; index < before.Len(); index++ {
			if !equivalentDarwinTerminalValue(before.Index(index), after.Index(index)) {
				return false
			}
		}
		return true
	case reflect.Bool:
		return before.Bool() == after.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return before.Int() == after.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return before.Uint() == after.Uint()
	case reflect.String:
		return before.String() == after.String()
	case reflect.Invalid,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.Chan, reflect.Func, reflect.Interface, reflect.Map,
		reflect.UnsafePointer:
		return false
	default:
		return false
	}
}

func runHuhPromptPTYParent(t *testing.T, mode string, chunks []string) {
	t.Helper()
	runPromptPTYParent(
		t,
		"AI_CLI_GATEWAY_HUH_PTY_CHILD="+mode,
		"^TestHuhPromptPTY$",
		chunks,
	)
}

func runPromptPTYParent(
	t *testing.T,
	childEnvironment string,
	testPattern string,
	chunks []string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	//nolint:gosec // The helper invokes a fixed system PTY wrapper and its own test binary.
	command := exec.CommandContext(
		ctx,
		"/usr/bin/script",
		"-q",
		"-e",
		"/dev/null",
		os.Args[0],
		"-test.run="+testPattern,
	)
	command.Env = append(
		os.Environ(),
		childEnvironment,
		"TERM=xterm-256color",
	)
	input, writer, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	command.Stdin = input
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		_ = input.Close()
		_ = writer.Close()
		t.Fatalf("start script: %v", err)
	}
	_ = input.Close()
	writeDone := make(chan struct{})
	go func() {
		defer close(writeDone)
		defer func() { _ = writer.Close() }()
		time.Sleep(time.Second)
		for _, chunk := range chunks {
			if _, writeErr := io.WriteString(writer, chunk); writeErr != nil {
				return
			}
			time.Sleep(500 * time.Millisecond)
		}
	}()
	waitErr := command.Wait()
	<-writeDone
	if waitErr != nil {
		t.Fatalf("PTY child failed: %v\n%s", waitErr, output.String())
	}
}

// formHuhReader gives each Bubble Tea program one complete form script and
// then returns EOF. This matters on Windows, where cancelreader cannot cancel
// a blocked read from an injected pipe and an old program can otherwise steal
// input intended for the next form.
type formHuhReader struct {
	mutex      sync.Mutex
	forms      [][]string
	chunkDelay time.Duration
	next       int
	chunk      int
	offset     int
	formEnded  bool
}

func newFormHuhInput(forms ...[]string) *formHuhReader {
	return newFormHuhInputWithDelay(250*time.Millisecond, forms...)
}

func newFormHuhInputWithDelay(
	delay time.Duration,
	forms ...[]string,
) *formHuhReader {
	cloned := make([][]string, len(forms))
	for index, form := range forms {
		cloned[index] = append([]string(nil), form...)
	}
	return &formHuhReader{forms: cloned, chunkDelay: delay}
}

func (reader *formHuhReader) Read(buffer []byte) (int, error) {
	reader.mutex.Lock()
	defer reader.mutex.Unlock()
	if reader.next >= len(reader.forms) {
		return 0, io.EOF
	}
	if reader.formEnded {
		reader.next++
		reader.chunk = 0
		reader.offset = 0
		reader.formEnded = false
		return 0, io.EOF
	}
	form := reader.forms[reader.next]
	if reader.chunk >= len(form) {
		reader.formEnded = true
		return 0, io.EOF
	}
	if reader.offset == 0 && reader.chunk > 0 {
		time.Sleep(reader.chunkDelay)
	}
	chunk := form[reader.chunk]
	count := copy(buffer, chunk[reader.offset:])
	reader.offset += count
	if reader.offset == len(chunk) {
		reader.chunk++
		reader.offset = 0
		if reader.chunk == len(form) {
			reader.formEnded = true
		}
	}
	return count, nil
}
