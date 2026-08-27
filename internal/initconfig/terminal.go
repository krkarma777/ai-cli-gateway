package initconfig

import (
	"io"

	xterm "github.com/charmbracelet/x/term"
)

type fileDescriptor interface {
	Fd() uintptr
}

// IsInteractiveTerminal reports whether both prompt streams are terminals.
func IsInteractiveTerminal(stdin io.Reader, stderr io.Writer) bool {
	if nilInterface(stdin) || nilInterface(stderr) {
		return false
	}
	input, inputOK := stdin.(fileDescriptor)
	output, outputOK := stderr.(fileDescriptor)
	return inputOK && outputOK &&
		xterm.IsTerminal(input.Fd()) && xterm.IsTerminal(output.Fd())
}

// NewTerminalPrompt constructs the selected interactive renderer. The caller
// decides accessibility before reaching this terminal-only boundary.
func NewTerminalPrompt(
	stdin io.Reader,
	stderr io.Writer,
	accessible bool,
) Prompt {
	if accessible {
		return newAccessiblePrompt(stderr, newContextLineReader(stdin, stderr))
	}
	return newHuhPrompt(stdin, stderr)
}
