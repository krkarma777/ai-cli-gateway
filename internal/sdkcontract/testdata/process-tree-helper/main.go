// Package main provides an ignore-TERM process tree for SDK helper cancellation tests.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--descendant" {
		descendantMain()
		return
	}
	os.Exit(parentMain())
}

func parentMain() int {
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	defer signal.Stop(term)
	executable, err := os.Executable()
	if err != nil {
		return 1
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return 1
	}
	command := exec.CommandContext(context.Background(), executable, "--descendant") //nolint:gosec // current test-owned executable and fixed argv.
	command.Env = []string{}
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.ExtraFiles = []*os.File{writer}
	if err := command.Start(); err != nil {
		_ = reader.Close()
		_ = writer.Close()
		return 1
	}
	_ = writer.Close()
	cleanup := true
	defer func() {
		if cleanup {
			_ = command.Process.Kill()
			_ = command.Wait()
		}
	}()
	var ready [1]byte
	if _, err := io.ReadFull(reader, ready[:]); err != nil || ready[0] != 1 {
		_ = reader.Close()
		return 1
	}
	_ = reader.Close()
	pid := os.Getpid()
	pgid, err := unix.Getpgid(pid)
	if err != nil || pid != pgid {
		return 1
	}
	descendantPID := command.Process.Pid
	descendantPGID, err := unix.Getpgid(descendantPID)
	if err != nil || descendantPGID != pgid {
		return 1
	}
	readyPath := filepath.Join(filepath.Dir(executable), "process-tree.ready")
	payload := []byte(fmt.Sprintf("%d %d %d\n", pid, descendantPID, pgid))
	if err := os.WriteFile(readyPath, payload, 0o600); err != nil {
		return 1
	}
	if err := os.Chmod(readyPath, 0o600); err != nil {
		return 1
	}
	cleanup = false
	for {
		<-term
	}
}

func descendantMain() {
	term := make(chan os.Signal, 1)
	signal.Notify(term, syscall.SIGTERM)
	defer signal.Stop(term)
	ready := os.NewFile(uintptr(3), "process-tree-ready")
	if ready == nil {
		os.Exit(2)
	}
	_, err := ready.Write([]byte{1})
	_ = ready.Close()
	if err != nil {
		os.Exit(2)
	}
	for {
		<-term
	}
}
