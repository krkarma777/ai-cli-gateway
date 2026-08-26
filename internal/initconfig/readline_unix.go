//go:build !windows

package initconfig

import (
	"context"
	"errors"
	"io"
	"math"
	"unicode/utf8"

	xterm "github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
)

const terminalReadPollMilliseconds = 50

type unixContextLineReader struct {
	fd int
}

func newContextLineReader(input io.Reader, _ io.Writer) contextLineReader {
	if nilInterface(input) {
		return failedContextLineReader{}
	}
	descriptor, ok := input.(fileDescriptor)
	if !ok {
		return failedContextLineReader{}
	}
	fd := descriptor.Fd()
	if fd > math.MaxInt32 || !xterm.IsTerminal(fd) {
		return failedContextLineReader{}
	}
	return &unixContextLineReader{fd: int(fd)}
}

func (reader *unixContextLineReader) ReadLine(
	ctx context.Context,
) (string, error) {
	if ctx == nil || reader == nil || reader.fd < 0 || reader.fd > math.MaxInt32 {
		return "", ErrPlan
	}
	var line []byte
	buffer := make([]byte, 512)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		pollFD := []unix.PollFd{{
			Fd:     int32(reader.fd),
			Events: unix.POLLIN | unix.POLLHUP | unix.POLLERR,
		}}
		ready, err := unix.Poll(pollFD, terminalReadPollMilliseconds)
		if err != nil {
			if errors.Is(err, unix.EINTR) {
				continue
			}
			return "", ErrPlan
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if ready == 0 {
			continue
		}
		events := pollFD[0].Revents
		if events&unix.POLLNVAL != 0 {
			return "", ErrPlan
		}
		if events&unix.POLLIN == 0 {
			if events&unix.POLLHUP != 0 {
				return "", io.EOF
			}
			if events&unix.POLLERR != 0 {
				return "", ErrPlan
			}
			continue
		}
		count, err := unix.Read(reader.fd, buffer)
		if err != nil {
			if errors.Is(err, unix.EINTR) || errors.Is(err, unix.EAGAIN) {
				continue
			}
			return "", ErrPlan
		}
		if count == 0 {
			return "", io.EOF
		}
		for _, value := range buffer[:count] {
			if value == '\n' {
				if len(line) > 0 && line[len(line)-1] == '\r' {
					line = line[:len(line)-1]
				}
				if !utf8.Valid(line) {
					return "", ErrPlan
				}
				return string(line), nil
			}
			line = append(line, value)
			if len(line) > maxPromptLineBytes {
				return "", ErrPlan
			}
		}
	}
}
