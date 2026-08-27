package initconfig

import (
	"context"
	"errors"
	"io"
	"unicode/utf16"
	"unicode/utf8"
)

type promptConsoleKeyKind uint8

const (
	promptConsoleRune promptConsoleKeyKind = iota + 1
	promptConsoleBackspace
	promptConsoleEnter
)

type promptConsoleKeyEvent struct {
	kind   promptConsoleKeyKind
	unit   uint16
	repeat uint16
}

type promptConsoleDriver interface {
	enterRawMode() (restore func() error, err error)
	readKey(context.Context) (promptConsoleKeyEvent, error)
}

type promptLineBuffer struct {
	runes         []rune
	byteCount     int
	maximumBytes  int
	highSurrogate uint16
}

func newPromptLineBuffer(maximumBytes int) *promptLineBuffer {
	return &promptLineBuffer{maximumBytes: maximumBytes}
}

func (buffer *promptLineBuffer) appendUTF16(unit uint16) (rune, bool, error) {
	if buffer == nil || buffer.maximumBytes < 0 {
		return 0, false, ErrPlan
	}
	if buffer.highSurrogate != 0 {
		if !isLowSurrogate(unit) {
			return 0, false, ErrPlan
		}
		character := utf16.DecodeRune(rune(buffer.highSurrogate), rune(unit))
		buffer.highSurrogate = 0
		return buffer.appendRune(character)
	}
	if isHighSurrogate(unit) {
		buffer.highSurrogate = unit
		return 0, false, nil
	}
	if isLowSurrogate(unit) {
		return 0, false, ErrPlan
	}
	return buffer.appendRune(rune(unit))
}

func (buffer *promptLineBuffer) appendRune(character rune) (rune, bool, error) {
	if !safeText(string(character)) {
		return 0, false, ErrPlan
	}
	size := utf8.RuneLen(character)
	if size < 0 || buffer.byteCount > buffer.maximumBytes-size {
		return 0, false, ErrPlan
	}
	buffer.runes = append(buffer.runes, character)
	buffer.byteCount += size
	return character, true, nil
}

func (buffer *promptLineBuffer) backspace() (bool, error) {
	if buffer == nil || buffer.highSurrogate != 0 {
		return false, ErrPlan
	}
	if len(buffer.runes) == 0 {
		return false, nil
	}
	last := len(buffer.runes) - 1
	size := utf8.RuneLen(buffer.runes[last])
	if size < 0 || size > buffer.byteCount {
		return false, ErrPlan
	}
	buffer.runes = buffer.runes[:last]
	buffer.byteCount -= size
	return true, nil
}

func (buffer *promptLineBuffer) value() (string, error) {
	if buffer == nil || buffer.highSurrogate != 0 {
		return "", ErrPlan
	}
	return string(buffer.runes), nil
}

func isHighSurrogate(unit uint16) bool {
	return unit >= 0xd800 && unit <= 0xdbff
}

func isLowSurrogate(unit uint16) bool {
	return unit >= 0xdc00 && unit <= 0xdfff
}

func readPromptConsoleLine(
	ctx context.Context,
	output io.Writer,
	driver promptConsoleDriver,
) (line string, returnErr error) {
	if ctx == nil || nilInterface(output) || nilInterface(driver) {
		return "", ErrPlan
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	restore, err := driver.enterRawMode()
	if err != nil || restore == nil {
		return "", ErrPlan
	}
	defer func() {
		if err := restore(); err != nil {
			line = ""
			returnErr = terminalRestoreError{cause: err}
		}
	}()

	buffer := newPromptLineBuffer(maxPromptLineBytes)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		event, err := driver.readKey(ctx)
		if contextErr := ctx.Err(); contextErr != nil {
			return "", contextErr
		}
		if err != nil {
			return "", normalizePromptConsoleError(err)
		}
		repeats := int(event.repeat)
		if repeats == 0 {
			repeats = 1
		}
		switch event.kind {
		case promptConsoleRune:
			for range repeats {
				character, accepted, appendErr := buffer.appendUTF16(event.unit)
				if appendErr != nil {
					return "", ErrPlan
				}
				if accepted {
					if err := writePromptConsole(output, string(character)); err != nil {
						return "", err
					}
				}
			}
		case promptConsoleBackspace:
			for range repeats {
				removed, backspaceErr := buffer.backspace()
				if backspaceErr != nil {
					return "", ErrPlan
				}
				if removed {
					if err := writePromptConsole(output, "\b \b"); err != nil {
						return "", err
					}
				}
			}
		case promptConsoleEnter:
			value, valueErr := buffer.value()
			if valueErr != nil {
				return "", ErrPlan
			}
			if err := writePromptConsole(output, "\r\n"); err != nil {
				return "", err
			}
			return value, nil
		default:
			return "", ErrPlan
		}
	}
}

func normalizePromptConsoleError(err error) error {
	switch {
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	case errors.Is(err, io.EOF):
		return io.EOF
	default:
		return ErrPlan
	}
}

func writePromptConsole(output io.Writer, value string) error {
	written, err := io.WriteString(output, value)
	if err != nil || written != len(value) {
		return ErrPlan
	}
	return nil
}
