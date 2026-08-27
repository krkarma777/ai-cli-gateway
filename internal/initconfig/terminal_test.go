package initconfig

import (
	"bytes"
	"os"
	"testing"
)

func TestIsInteractiveTerminalRejectsNonTerminals(t *testing.T) {
	t.Parallel()

	if IsInteractiveTerminal(nil, nil) {
		t.Fatal("nil streams reported as interactive")
	}
	if IsInteractiveTerminal(bytes.NewReader(nil), &bytes.Buffer{}) {
		t.Fatal("streams without file descriptors reported as interactive")
	}

	input, output, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe(): %v", err)
	}
	t.Cleanup(func() {
		_ = input.Close()
		_ = output.Close()
	})
	if IsInteractiveTerminal(input, output) {
		t.Fatal("pipe reported as interactive")
	}

	null, err := os.OpenFile(os.DevNull, os.O_RDWR, 0)
	if err != nil {
		t.Fatalf("open %s: %v", os.DevNull, err)
	}
	t.Cleanup(func() { _ = null.Close() })
	if IsInteractiveTerminal(null, null) {
		t.Fatalf("%s reported as interactive", os.DevNull)
	}

	closed, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatalf("open closed-file fixture: %v", err)
	}
	if err := closed.Close(); err != nil {
		t.Fatalf("close fixture: %v", err)
	}
	if IsInteractiveTerminal(closed, closed) {
		t.Fatal("closed file reported as interactive")
	}
}

func TestIsInteractiveTerminalRequiresBothTTYs(t *testing.T) {
	terminal, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		t.Skipf("no controlling terminal: %v", err)
	}
	t.Cleanup(func() { _ = terminal.Close() })

	if !IsInteractiveTerminal(terminal, terminal) {
		t.Fatal("controlling terminal was not detected")
	}
	if IsInteractiveTerminal(terminal, &bytes.Buffer{}) {
		t.Fatal("TTY stdin with non-TTY stderr reported as interactive")
	}
	if IsInteractiveTerminal(bytes.NewReader(nil), terminal) {
		t.Fatal("non-TTY stdin with TTY stderr reported as interactive")
	}
}

func TestAccessiblePromptNewTerminalPromptSelectsRequestedAdapter(t *testing.T) {
	t.Parallel()

	accessible := NewTerminalPrompt(bytes.NewReader(nil), &bytes.Buffer{}, true)
	if _, ok := accessible.(*accessiblePrompt); !ok {
		t.Fatalf("accessible prompt type = %T, want *accessiblePrompt", accessible)
	}
	visual := NewTerminalPrompt(bytes.NewReader(nil), &bytes.Buffer{}, false)
	if _, ok := visual.(*huhPrompt); !ok {
		t.Fatalf("visual prompt type = %T, want *huhPrompt", visual)
	}
}
