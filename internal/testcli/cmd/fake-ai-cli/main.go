// Package main exposes the deterministic test CLI as a temporary executable.
package main

import (
	"os"

	"github.com/krkarma777/ai-cli-gateway/internal/testcli"
)

func main() {
	os.Exit(testcli.Main(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
