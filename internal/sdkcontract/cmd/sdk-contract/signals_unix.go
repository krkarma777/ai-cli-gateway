//go:build !windows

package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

func signalContext() (context.Context, func()) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
