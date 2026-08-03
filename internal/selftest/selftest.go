// Package selftest implements the gateway's hidden process-containment probe.
package selftest

import (
	"io"
	"os"
	"os/exec"
)

const (
	internalMode = "__process-selftest"
	parentMode   = "parent"
	childMode    = "child"
)

// Main handles only the gateway's exact hidden self-test argv.
func Main(
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) (handled bool, exitCode int) {
	if len(args) == 0 || args[0] != internalMode {
		return false, 0
	}
	if len(args) != 2 {
		return true, 2
	}
	switch args[1] {
	case parentMode:
		return true, runParent(stdout, stderr)
	case childMode:
		return true, runChild()
	default:
		return true, 2
	}
}

func runParent(stdout io.Writer, stderr io.Writer) int {
	executable, err := os.Executable()
	if err != nil {
		return 1
	}
	return runParentPlatform(executable, stdout, stderr)
}

func writeAll(dst io.Writer, data []byte) error {
	for len(data) != 0 {
		count, err := dst.Write(data)
		if err != nil {
			return err
		}
		if count <= 0 || count > len(data) {
			return io.ErrShortWrite
		}
		data = data[count:]
	}
	return nil
}

func killAndWait(cmd *exec.Cmd) {
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
}
