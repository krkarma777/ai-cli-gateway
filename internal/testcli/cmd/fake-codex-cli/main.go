// Package main exposes the deterministic Codex-ready test CLI as a temporary executable.
package main

import (
	"os"

	"github.com/krkarma777/ai-cli-gateway/internal/testcli"
)

func main() {
	os.Exit(testcli.CodexReadyMain(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
