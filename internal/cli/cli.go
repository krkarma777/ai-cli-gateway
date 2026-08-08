// Package cli provides the command-line interface behavior.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/krkarma777/ai-cli-gateway/internal/app"
	"github.com/krkarma777/ai-cli-gateway/internal/buildinfo"
	"github.com/krkarma777/ai-cli-gateway/internal/config"
)

const usage = "usage:\n" +
	"  ai-cli-gateway version\n" +
	"  ai-cli-gateway serve [--config PATH]\n" +
	"  ai-cli-gateway doctor [--config PATH] [--json]\n"

type commands struct {
	defaultConfigPath func() (string, error)
	serve             func(context.Context, string) error
	doctor            func(context.Context, string, bool, io.Writer) int
}

// Run executes a command and returns its process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	return RunContext(context.Background(), args, stdout, stderr)
}

// RunContext executes a command with cancellation and returns its process exit
// code.
func RunContext(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
) int {
	return runContext(ctx, args, stdout, stderr, commands{
		defaultConfigPath: config.DefaultPath,
		serve: func(ctx context.Context, configPath string) error {
			return app.Serve(
				ctx,
				configPath,
				app.ProductionDependencies(stderr),
			)
		},
		doctor: func(
			ctx context.Context,
			configPath string,
			jsonOutput bool,
			output io.Writer,
		) int {
			return app.Doctor(
				ctx,
				configPath,
				jsonOutput,
				output,
				app.ProductionDependencies(stderr),
			)
		},
	})
}

func runContext(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	commands commands,
) int {
	if ctx == nil {
		ctx = context.Background()
	}
	if stdout == nil || stderr == nil {
		return 2
	}
	if len(args) == 1 && args[0] == "version" {
		_, _ = fmt.Fprintf(stdout, "ai-cli-gateway %s (%s, %s)\n",
			buildinfo.Version, buildinfo.Commit, buildinfo.Date)
		return 0
	}
	if len(args) == 1 && args[0] == "--help" ||
		len(args) == 2 && args[1] == "--help" && isCommand(args[0]) {
		_, _ = io.WriteString(stdout, usage)
		return 0
	}
	configPath, jsonOutput, explicit, ok := commandConfig(args)
	if !ok {
		_, _ = io.WriteString(stderr, usage)
		return 2
	}
	if !explicit {
		if commands.defaultConfigPath == nil {
			_, _ = io.WriteString(stderr, "default_config_path_unavailable: pass --config PATH\n")
			return 2
		}
		var err error
		configPath, err = commands.defaultConfigPath()
		if err != nil {
			_, _ = io.WriteString(stderr, "default_config_path_unavailable: pass --config PATH\n")
			return 2
		}
	}
	if args[0] == "serve" {
		if commands.serve == nil {
			_, _ = io.WriteString(stderr, "serve_failed: run ai-cli-gateway doctor\n")
			return 1
		}
		return serveResult(commands.serve(ctx, configPath), stderr)
	}
	if commands.doctor == nil {
		return 1
	}
	return commands.doctor(ctx, configPath, jsonOutput, stdout)
}

func commandConfig(args []string) (path string, jsonOutput, explicit, ok bool) {
	if len(args) == 1 {
		switch args[0] {
		case "serve", "doctor":
			return "", false, false, true
		}
	}
	if len(args) == 2 && args[0] == "doctor" && args[1] == "--json" {
		return "", true, false, true
	}
	if len(args) == 3 && args[1] == "--config" && validPath(args[2]) {
		switch args[0] {
		case "serve":
			return args[2], false, true, true
		case "doctor":
			return args[2], false, true, true
		}
	}
	if len(args) == 4 && args[0] == "doctor" {
		if args[1] == "--config" && validPath(args[2]) && args[3] == "--json" {
			return args[2], true, true, true
		}
		if args[1] == "--json" && args[2] == "--config" && validPath(args[3]) {
			return args[3], true, true, true
		}
	}
	return "", false, false, false
}

func isCommand(value string) bool {
	return value == "version" || value == "serve" || value == "doctor"
}

func validPath(value string) bool {
	return value != "" && value[0] != '-'
}

func serveResult(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	if errors.Is(err, app.ErrConfigInvalid) {
		_, _ = io.WriteString(stderr, "configuration_invalid\n")
		return 2
	}
	if errors.Is(err, app.ErrNotReady) {
		_, _ = io.WriteString(stderr, "gateway_not_ready: run ai-cli-gateway doctor\n")
		return 1
	}
	_, _ = io.WriteString(stderr, "serve_failed: run ai-cli-gateway doctor\n")
	return 1
}
