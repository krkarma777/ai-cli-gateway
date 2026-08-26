package initconfig

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
)

func TestPromptLineBufferAssemblesUTF16AndBackspacesOneRune(t *testing.T) {
	buffer := newPromptLineBuffer(4)

	if character, accepted, err := buffer.appendUTF16(0xd83d); err != nil || accepted {
		t.Fatalf("append high surrogate = %q, %t, %v; want pending", character, accepted, err)
	}
	character, accepted, err := buffer.appendUTF16(0xde00)
	if err != nil || !accepted || character != '\U0001f600' {
		t.Fatalf("append low surrogate = %q, %t, %v; want grinning face", character, accepted, err)
	}
	if buffer.byteCount != 4 {
		t.Fatalf("UTF-8 byte count = %d, want 4", buffer.byteCount)
	}

	removed, err := buffer.backspace()
	if err != nil || !removed || buffer.byteCount != 0 {
		t.Fatalf("backspace = %t, %v; byte count = %d, want removed and zero", removed, err, buffer.byteCount)
	}
	for _, unit := range []uint16{'a', 'b', 'c', 'd'} {
		if _, accepted, err := buffer.appendUTF16(unit); err != nil || !accepted {
			t.Fatalf("append %#x = accepted %t, error %v", unit, accepted, err)
		}
	}
	if _, _, err := buffer.appendUTF16('e'); err != ErrPlan {
		t.Fatalf("append beyond actual UTF-8 limit = %v, want exact ErrPlan", err)
	}
	value, err := buffer.value()
	if err != nil || value != "abcd" {
		t.Fatalf("value = %q, %v; want abcd", value, err)
	}
}

func TestPromptLineBufferRejectsSurrogatesThatCannotFormARune(t *testing.T) {
	tests := []struct {
		name string
		run  func(*promptLineBuffer) error
	}{
		{
			name: "isolated low surrogate",
			run: func(buffer *promptLineBuffer) error {
				_, _, err := buffer.appendUTF16(0xde00)
				return err
			},
		},
		{
			name: "high surrogate followed by BMP rune",
			run: func(buffer *promptLineBuffer) error {
				_, _, _ = buffer.appendUTF16(0xd83d)
				_, _, err := buffer.appendUTF16('a')
				return err
			},
		},
		{
			name: "high surrogate followed by enter",
			run: func(buffer *promptLineBuffer) error {
				_, _, _ = buffer.appendUTF16(0xd83d)
				_, err := buffer.value()
				return err
			},
		},
		{
			name: "high surrogate followed by backspace",
			run: func(buffer *promptLineBuffer) error {
				_, _, _ = buffer.appendUTF16(0xd83d)
				_, err := buffer.backspace()
				return err
			},
		},
		{
			name: "surrogate pair exceeds actual byte limit",
			run: func(buffer *promptLineBuffer) error {
				buffer.maximumBytes = 3
				_, _, _ = buffer.appendUTF16(0xd83d)
				_, _, err := buffer.appendUTF16(0xde00)
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.run(newPromptLineBuffer(8)); err != ErrPlan {
				t.Fatalf("error = %v, want exact ErrPlan", err)
			}
		})
	}
}

type scriptedPromptConsoleDriver struct {
	events       []promptConsoleKeyEvent
	readErr      error
	enterErr     error
	restoreErr   error
	beforeRead   func()
	enterCalls   int
	readCalls    int
	restoreCalls int
}

func (driver *scriptedPromptConsoleDriver) enterRawMode() (func() error, error) {
	driver.enterCalls++
	if driver.enterErr != nil {
		return nil, driver.enterErr
	}
	return func() error {
		driver.restoreCalls++
		return driver.restoreErr
	}, nil
}

func (driver *scriptedPromptConsoleDriver) readKey(
	context.Context,
) (promptConsoleKeyEvent, error) {
	driver.readCalls++
	if driver.beforeRead != nil {
		driver.beforeRead()
		driver.beforeRead = nil
	}
	if len(driver.events) == 0 {
		if driver.readErr != nil {
			return promptConsoleKeyEvent{}, driver.readErr
		}
		return promptConsoleKeyEvent{}, io.EOF
	}
	event := driver.events[0]
	driver.events = driver.events[1:]
	return event, nil
}

func TestPromptConsoleLineEchoesAcceptedRunesAndEditing(t *testing.T) {
	driver := &scriptedPromptConsoleDriver{events: []promptConsoleKeyEvent{
		{kind: promptConsoleRune, unit: 'A', repeat: 2},
		{kind: promptConsoleRune, unit: 0xd83d},
		{kind: promptConsoleRune, unit: 0xde00},
		{kind: promptConsoleBackspace},
		{kind: promptConsoleRune, unit: '\u754c'},
		{kind: promptConsoleEnter},
	}}
	var output bytes.Buffer

	line, err := readPromptConsoleLine(context.Background(), &output, driver)
	if err != nil || line != "AA\u754c" {
		t.Fatalf("readPromptConsoleLine() = %q, %v; want AA\u754c", line, err)
	}
	if got, want := output.String(), "AA\U0001f600\b \b\u754c\r\n"; got != want {
		t.Fatalf("console echo = %q, want %q", got, want)
	}
	if driver.enterCalls != 1 || driver.restoreCalls != 1 {
		t.Fatalf("mode calls = enter %d, restore %d; want 1, 1", driver.enterCalls, driver.restoreCalls)
	}
}

func TestPromptConsoleLineRejectsControlBeforeEchoAndRestoresMode(t *testing.T) {
	for _, unit := range []uint16{'\x1b', '\t'} {
		t.Run(string(rune(unit)), func(t *testing.T) {
			driver := &scriptedPromptConsoleDriver{events: []promptConsoleKeyEvent{
				{kind: promptConsoleRune, unit: unit},
				{kind: promptConsoleEnter},
			}}
			var output bytes.Buffer

			_, err := readPromptConsoleLine(context.Background(), &output, driver)
			if err != ErrPlan {
				t.Fatalf("readPromptConsoleLine() error = %v, want exact ErrPlan", err)
			}
			if output.Len() != 0 {
				t.Fatalf("control input was echoed as %q", output.String())
			}
			if driver.restoreCalls != 1 {
				t.Fatalf("restore calls = %d, want 1", driver.restoreCalls)
			}
		})
	}
}

type failingPromptWriter struct{}

func (failingPromptWriter) Write([]byte) (int, error) {
	return 0, errors.New("planted prompt write failure")
}

func TestPromptConsoleLineRestoresModeOnEveryPostEntryReturn(t *testing.T) {
	tests := []struct {
		name      string
		configure func(*scriptedPromptConsoleDriver) (context.Context, io.Writer)
		wantErr   error
	}{
		{
			name: "success",
			configure: func(driver *scriptedPromptConsoleDriver) (context.Context, io.Writer) {
				driver.events = []promptConsoleKeyEvent{{kind: promptConsoleEnter}}
				return context.Background(), io.Discard
			},
		},
		{
			name: "read error",
			configure: func(driver *scriptedPromptConsoleDriver) (context.Context, io.Writer) {
				driver.readErr = errors.New("planted prompt read failure")
				return context.Background(), io.Discard
			},
			wantErr: ErrPlan,
		},
		{
			name: "write error",
			configure: func(driver *scriptedPromptConsoleDriver) (context.Context, io.Writer) {
				driver.events = []promptConsoleKeyEvent{{kind: promptConsoleRune, unit: 'a'}}
				return context.Background(), failingPromptWriter{}
			},
			wantErr: ErrPlan,
		},
		{
			name: "invalid UTF-16",
			configure: func(driver *scriptedPromptConsoleDriver) (context.Context, io.Writer) {
				driver.events = []promptConsoleKeyEvent{{kind: promptConsoleRune, unit: 0xde00}}
				return context.Background(), io.Discard
			},
			wantErr: ErrPlan,
		},
		{
			name: "cancellation after blocked read",
			configure: func(driver *scriptedPromptConsoleDriver) (context.Context, io.Writer) {
				ctx, cancel := context.WithCancel(context.Background())
				driver.beforeRead = cancel
				driver.events = []promptConsoleKeyEvent{{kind: promptConsoleRune, unit: 'a'}}
				return ctx, io.Discard
			},
			wantErr: context.Canceled,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			driver := &scriptedPromptConsoleDriver{}
			ctx, output := test.configure(driver)
			_, err := readPromptConsoleLine(ctx, output, driver)
			if err != test.wantErr {
				t.Fatalf("readPromptConsoleLine() error = %v, want exact %v", err, test.wantErr)
			}
			if driver.enterCalls != 1 || driver.restoreCalls != 1 {
				t.Fatalf("mode calls = enter %d, restore %d; want 1, 1", driver.enterCalls, driver.restoreCalls)
			}
		})
	}
}

func TestPromptConsoleLineRestoreFailureOutranksReadResult(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	driver := &scriptedPromptConsoleDriver{
		beforeRead: cancel,
		events:     []promptConsoleKeyEvent{{kind: promptConsoleRune, unit: 'a'}},
		restoreErr: errors.New("planted restore failure"),
	}

	line, err := readPromptConsoleLine(ctx, io.Discard, driver)
	var restoreErr terminalRestoreError
	if line != "" || !errors.As(err, &restoreErr) {
		t.Fatalf("readPromptConsoleLine() = %q, %v; want terminalRestoreError", line, err)
	}
	if driver.restoreCalls != 1 {
		t.Fatalf("restore calls = %d, want 1", driver.restoreCalls)
	}
}

func TestPromptConsoleLineDoesNotRestoreWhenModeEntryFails(t *testing.T) {
	driver := &scriptedPromptConsoleDriver{enterErr: errors.New("planted mode entry failure")}
	_, err := readPromptConsoleLine(context.Background(), io.Discard, driver)
	if err != ErrPlan {
		t.Fatalf("readPromptConsoleLine() error = %v, want exact ErrPlan", err)
	}
	if driver.restoreCalls != 0 {
		t.Fatalf("restore calls = %d, want 0", driver.restoreCalls)
	}
}
