//go:build windows

package initconfig

import (
	"context"
	"io"

	xterm "github.com/charmbracelet/x/term"
	winconsole "github.com/charmbracelet/x/windows"
	"golang.org/x/sys/windows"
)

const terminalReadPollMilliseconds = 50

type windowsContextLineReader struct {
	output io.Writer
	driver promptConsoleDriver
}

func newContextLineReader(input io.Reader, output io.Writer) contextLineReader {
	if nilInterface(input) || nilInterface(output) {
		return failedContextLineReader{}
	}
	descriptor, ok := input.(fileDescriptor)
	if !ok || !xterm.IsTerminal(descriptor.Fd()) {
		return failedContextLineReader{}
	}
	return &windowsContextLineReader{
		output: output,
		driver: &windowsConsoleDriver{handle: windows.Handle(descriptor.Fd())},
	}
}

func (reader *windowsContextLineReader) ReadLine(
	ctx context.Context,
) (string, error) {
	if reader == nil || nilInterface(reader.output) || nilInterface(reader.driver) {
		return "", ErrPlan
	}
	return readPromptConsoleLine(ctx, reader.output, reader.driver)
}

type windowsConsoleDriver struct {
	handle windows.Handle
}

func (driver *windowsConsoleDriver) enterRawMode() (func() error, error) {
	if driver == nil || driver.handle == 0 || driver.handle == windows.InvalidHandle {
		return nil, ErrPlan
	}
	var originalMode uint32
	if err := windows.GetConsoleMode(driver.handle, &originalMode); err != nil {
		return nil, ErrPlan
	}
	readMode := originalMode &^ (windows.ENABLE_LINE_INPUT |
		windows.ENABLE_ECHO_INPUT | windows.ENABLE_PROCESSED_INPUT)
	if err := windows.SetConsoleMode(driver.handle, readMode); err != nil {
		return nil, ErrPlan
	}
	return func() error {
		return windows.SetConsoleMode(driver.handle, originalMode)
	}, nil
}

func (driver *windowsConsoleDriver) readKey(
	ctx context.Context,
) (promptConsoleKeyEvent, error) {
	if ctx == nil || driver == nil || driver.handle == 0 ||
		driver.handle == windows.InvalidHandle {
		return promptConsoleKeyEvent{}, ErrPlan
	}
	for {
		if err := ctx.Err(); err != nil {
			return promptConsoleKeyEvent{}, err
		}
		event, err := windows.WaitForSingleObject(
			driver.handle,
			terminalReadPollMilliseconds,
		)
		if err != nil {
			return promptConsoleKeyEvent{}, ErrPlan
		}
		if err := ctx.Err(); err != nil {
			return promptConsoleKeyEvent{}, err
		}
		if event == uint32(windows.WAIT_TIMEOUT) {
			continue
		}
		if event != windows.WAIT_OBJECT_0 {
			return promptConsoleKeyEvent{}, ErrPlan
		}

		var record winconsole.InputRecord
		var count uint32
		if err := winconsole.ReadConsoleInput(
			driver.handle, &record, 1, &count,
		); err != nil || count != 1 {
			return promptConsoleKeyEvent{}, ErrPlan
		}
		if record.EventType != winconsole.KEY_EVENT {
			continue
		}
		key := record.KeyEvent()
		if !key.KeyDown {
			continue
		}
		switch key.VirtualKeyCode {
		case winconsole.VK_RETURN:
			return promptConsoleKeyEvent{kind: promptConsoleEnter}, nil
		case winconsole.VK_BACK:
			return promptConsoleKeyEvent{
				kind:   promptConsoleBackspace,
				repeat: key.RepeatCount,
			}, nil
		}
		if key.Char == 3 {
			return promptConsoleKeyEvent{}, context.Canceled
		}
		if key.Char == 0 {
			continue
		}
		return promptConsoleKeyEvent{
			kind:   promptConsoleRune,
			unit:   uint16(key.Char),
			repeat: key.RepeatCount,
		}, nil
	}
}
