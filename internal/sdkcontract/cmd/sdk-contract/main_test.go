package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/sdkcontract"
)

func TestRunCommandAcceptsExactNamedFlags(t *testing.T) {
	var got sdkcontract.Options
	stopCalls := 0
	deps := commandDependencies{
		contextFactory: func() (context.Context, func()) { return context.Background(), func() { stopCalls++ } },
		run: func(_ context.Context, options sdkcontract.Options, output io.Writer) error {
			got = options
			_, _ = io.WriteString(output, "python_sdk_contract_ok\njavascript_sdk_contract_ok\n")
			return nil
		},
	}
	args := []string{"--repository-root", "/repo", "--python", "/python", "--node", "/node", "--javascript", "/main.mjs"}
	var stdout, stderr bytes.Buffer
	if code := runCommand(args, &stdout, &stderr, deps); code != 0 {
		t.Fatalf("runCommand() = %d, stderr=%q", code, stderr.String())
	}
	want := sdkcontract.Options{RepositoryRoot: "/repo", PythonExecutable: "/python", NodeExecutable: "/node", JavaScriptEntrypoint: "/main.mjs"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("options = %#v, want %#v", got, want)
	}
	if stdout.String() != "python_sdk_contract_ok\njavascript_sdk_contract_ok\n" || stderr.Len() != 0 || stopCalls != 1 {
		t.Fatalf("stdout=%q stderr=%q stopCalls=%d", stdout.String(), stderr.String(), stopCalls)
	}
}

func TestRunCommandRejectsUnknownDuplicateMissingAndEqualsFlags(t *testing.T) {
	cases := [][]string{
		{},
		{"--repository-root", "/repo", "--python", "/python", "--node", "/node"},
		{"--repository-root=/repo", "--python", "/python", "--node", "/node", "--javascript", "/js"},
		{"--repository-root", "/repo", "--repository-root", "/other", "--python", "/python", "--node", "/node", "--javascript", "/js"},
		{"--unknown", "/repo", "--python", "/python", "--node", "/node", "--javascript", "/js"},
	}
	for _, args := range cases {
		var stdout, stderr bytes.Buffer
		if code := runCommand(args, &stdout, &stderr, commandDependencies{}); code != 2 {
			t.Fatalf("runCommand(%#v) = %d", args, code)
		}
		if stdout.Len() != 0 || stderr.String() != "sdk-contract: invalid_input\n" {
			t.Fatalf("runCommand(%#v) stdout=%q stderr=%q", args, stdout.String(), stderr.String())
		}
	}
}

func TestRunCommandPrintsOnlyFixedRunCategory(t *testing.T) {
	deps := commandDependencies{
		contextFactory: func() (context.Context, func()) { return context.Background(), func() {} },
		run: func(context.Context, sdkcontract.Options, io.Writer) error {
			return errors.New("private path and secret")
		},
	}
	args := []string{"--repository-root", "/repo", "--python", "/python", "--node", "/node", "--javascript", "/main.mjs"}
	var stdout, stderr bytes.Buffer
	if code := runCommand(args, &stdout, &stderr, deps); code != 1 {
		t.Fatalf("code = %d", code)
	}
	if stderr.String() != "sdk-contract: sdk_contract_failed\n" {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
