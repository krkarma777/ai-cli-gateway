//go:build !windows

package initconfig

import (
	"errors"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestResolveNonInteractiveRejectsEntrypointOutsideWindows(t *testing.T) {
	t.Parallel()

	options := validOptions()
	input := options.Provider[core.ProviderCodex]
	input.Entrypoint = setString(testAbsolutePath("lib", "codex.mjs"))
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
}
