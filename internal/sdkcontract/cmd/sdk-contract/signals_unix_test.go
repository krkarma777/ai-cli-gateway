//go:build !windows

package main

import (
	"os"
	"syscall"
	"testing"
	"time"
)

func TestSignalContextCancelsOnSIGTERM(t *testing.T) {
	ctx, stop := signalContext()
	defer stop()
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatalf("deliver SIGTERM: %v", err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("signal context was not canceled by SIGTERM")
	}
}
