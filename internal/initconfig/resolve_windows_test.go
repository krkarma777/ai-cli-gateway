//go:build windows

package initconfig

import (
	"errors"
	"reflect"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestResolveNonInteractiveAcceptsExactWindowsNodeCommandPair(t *testing.T) {
	t.Parallel()

	options := validOptions()
	input := options.Provider[core.ProviderCodex]
	input.Executable = setString(`C:\Program Files\nodejs\node.exe`)
	input.Entrypoint = setString(`C:\Provider\codex.mjs`)
	options.Provider[core.ProviderCodex] = input
	got, err := ResolveNonInteractive(
		options,
		nil,
		testAbsolutePath("runtime"),
		testAbsolutePath("gateway.key"),
	)
	if err != nil {
		t.Fatalf("ResolveNonInteractive() error = %v", err)
	}
	want := ProviderCommand{
		Executable: `C:\Program Files\nodejs\node.exe`,
		PrefixArgs: []string{`C:\Provider\codex.mjs`},
	}
	if !reflect.DeepEqual(got.Providers[0].Command.Value, want) {
		t.Fatalf("Command = %#v, want %#v", got.Providers[0].Command.Value, want)
	}
}

func TestResolveNonInteractiveRejectsInvalidWindowsCommandPairs(t *testing.T) {
	t.Parallel()

	tests := map[string]func(*ProviderInput){
		"node without entrypoint": func(input *ProviderInput) {
			input.Executable = setString(`C:\Program Files\nodejs\node.exe`)
			input.Entrypoint = StringValue{}
		},
		"entrypoint without executable": func(input *ProviderInput) {
			input.Executable = StringValue{}
			input.Entrypoint = setString(`C:\Provider\codex.mjs`)
		},
		"native with entrypoint": func(input *ProviderInput) {
			input.Executable = setString(`C:\Provider\codex.exe`)
			input.Entrypoint = setString(`C:\Provider\codex.mjs`)
		},
		"non-JavaScript entrypoint": func(input *ProviderInput) {
			input.Executable = setString(`C:\Program Files\nodejs\node.exe`)
			input.Entrypoint = setString(`C:\Provider\codex.txt`)
		},
	}
	for name, mutate := range tests {
		mutate := mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			options := validOptions()
			input := options.Provider[core.ProviderCodex]
			mutate(&input)
			options.Provider[core.ProviderCodex] = input
			_, err := ResolveNonInteractive(
				options,
				nil,
				testAbsolutePath("runtime"),
				testAbsolutePath("gateway.key"),
			)
			if !errors.Is(err, ErrUsage) {
				t.Fatalf("ResolveNonInteractive() error = %v, want ErrUsage", err)
			}
		})
	}
}
