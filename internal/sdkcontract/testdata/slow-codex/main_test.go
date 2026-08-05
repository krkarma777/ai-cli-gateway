package main

import (
	"os"
	"syscall"
	"testing"
)

func TestRunAfterTERMHandlerInstallsBeforeStart(t *testing.T) {
	notified := false
	stopped := false
	code := runAfterTERMHandler(
		func(_ chan<- os.Signal, signals ...os.Signal) {
			notified = len(signals) == 1 && signals[0] == syscall.SIGTERM
		},
		func(_ chan<- os.Signal) { stopped = true },
		func() int {
			if !notified {
				return 91
			}
			return 7
		},
	)
	if code != 7 || !notified || !stopped {
		t.Fatalf("code=%d notified=%t stopped=%t", code, notified, stopped)
	}
}
