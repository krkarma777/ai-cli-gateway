// Package main provides the repository-internal SDK contract test command.
package main

import (
	"context"
	"io"
	"os"

	"github.com/krkarma777/ai-cli-gateway/internal/sdkcontract"
)

type commandDependencies struct {
	contextFactory func() (context.Context, func())
	run            func(context.Context, sdkcontract.Options, io.Writer) error
}

func main() {
	code := runCommand(os.Args[1:], os.Stdout, os.Stderr, commandDependencies{
		contextFactory: signalContext,
		run:            sdkcontract.Run,
	})
	os.Exit(code)
}

func runCommand(args []string, stdout, stderr io.Writer, dependencies commandDependencies) int {
	options, ok := parseArgs(args)
	if !ok || stdout == nil || stderr == nil {
		writeFixed(stderr, "sdk-contract: invalid_input\n")
		return 2
	}
	if dependencies.contextFactory == nil || dependencies.run == nil {
		writeFixed(stderr, "sdk-contract: sdk_contract_failed\n")
		return 1
	}
	ctx, stop := dependencies.contextFactory()
	if ctx == nil || stop == nil {
		writeFixed(stderr, "sdk-contract: sdk_contract_failed\n")
		return 1
	}
	defer stop()
	if err := dependencies.run(ctx, options, stdout); err != nil {
		writeFixed(stderr, "sdk-contract: "+sdkcontract.ErrorCategory(err)+"\n")
		return 1
	}
	return 0
}

func parseArgs(args []string) (sdkcontract.Options, bool) {
	if len(args) != 8 {
		return sdkcontract.Options{}, false
	}
	values := make(map[string]string, 4)
	for index := 0; index < len(args); index += 2 {
		name, value := args[index], args[index+1]
		if value == "" || value[0] == '-' {
			return sdkcontract.Options{}, false
		}
		switch name {
		case "--repository-root", "--python", "--node", "--javascript":
		default:
			return sdkcontract.Options{}, false
		}
		if _, exists := values[name]; exists {
			return sdkcontract.Options{}, false
		}
		values[name] = value
	}
	if len(values) != 4 {
		return sdkcontract.Options{}, false
	}
	return sdkcontract.Options{
		RepositoryRoot: values["--repository-root"], PythonExecutable: values["--python"],
		NodeExecutable: values["--node"], JavaScriptEntrypoint: values["--javascript"],
	}, true
}

func writeFixed(writer io.Writer, value string) {
	if writer != nil {
		_, _ = io.WriteString(writer, value)
	}
}
