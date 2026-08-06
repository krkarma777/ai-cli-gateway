// Package main provides the ignore-TERM Codex fixture used by SDK lifecycle integration tests.
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

	"github.com/krkarma777/ai-cli-gateway/internal/testcli"
	"golang.org/x/sys/unix"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "--fixture-descendant" {
		descendantMain()
		return
	}
	os.Exit(testcli.CodexReadyMainWithFinal(os.Args[1:], os.Stdin, os.Stdout, os.Stderr, slowFinal))
}

func slowFinal(_ io.Reader, _ io.Writer, _ io.Writer) int {
	return runAfterTERMHandler(signal.Notify, signal.Stop, startSlowFinal)
}

func startSlowFinal() int {
	executable, err := os.Executable()
	if err != nil {
		return 1
	}
	registryPath := filepath.Join(filepath.Dir(executable), "fixture.registry")
	// #nosec G304 -- registryPath is the fixed FIFO adjacent to this test-owned executable.
	registry, err := os.OpenFile(registryPath, os.O_WRONLY, 0)
	if err != nil {
		return 1
	}
	defer func() { _ = registry.Close() }()
	pid := os.Getpid()
	pgid, err := unix.Getpgid(pid)
	if err != nil || pgid != pid {
		return 1
	}
	if _, err := fmt.Fprintf(registry, "provider %d %d\n", pid, pgid); err != nil {
		return 1
	}

	reader, writer, err := os.Pipe()
	if err != nil {
		return 1
	}
	command := exec.CommandContext(context.Background(), executable, "--fixture-descendant") //nolint:gosec // current test-owned executable and closed argv.
	command.Env = []string{}
	command.Stdin = nil
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
	descendantPID := command.Process.Pid
	descendantPGID, err := unix.Getpgid(descendantPID)
	if err != nil || descendantPGID != pgid {
		return 1
	}
	if _, err := fmt.Fprintf(registry, "descendant %d %d\n", descendantPID, descendantPGID); err != nil {
		return 1
	}
	cleanup = false
	return 0
}

func descendantMain() {
	os.Exit(runAfterTERMHandler(signal.Notify, signal.Stop, publishDescendantReady))
}

func publishDescendantReady() int {
	ready := os.NewFile(uintptr(3), "fixture-ready")
	if ready == nil {
		return 2
	}
	_, err := ready.Write([]byte{1})
	_ = ready.Close()
	if err != nil {
		return 2
	}
	return 0
}

func runAfterTERMHandler(
	notify func(chan<- os.Signal, ...os.Signal),
	stop func(chan<- os.Signal),
	start func() int,
) int {
	if notify == nil || stop == nil || start == nil {
		return 1
	}
	term := make(chan os.Signal, 1)
	notify(term, syscall.SIGTERM)
	if code := start(); code != 0 {
		stop(term)
		return code
	}
	for {
		<-term
	}
}
