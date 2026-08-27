// Command ai-cli-gateway provides an AI CLI gateway.
package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/krkarma777/ai-cli-gateway/internal/cli"
	"github.com/krkarma777/ai-cli-gateway/internal/selftest"
)

func main() {
	if handled, code := selftest.Main(
		os.Args[1:],
		os.Stdout,
		os.Stderr,
	); handled {
		os.Exit(code)
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		shutdownSignals()...,
	)
	code := cli.RunContext(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}
