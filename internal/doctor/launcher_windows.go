//go:build windows

package doctor

import (
	"path/filepath"
	"strings"
)

func resolveProviderCommand(
	executable validatedPath,
	configuredPrefix []string,
	_ func(string) (string, error),
) (resolvedProviderCommand, bool) {
	if len(configuredPrefix) == 0 {
		return nativeProviderCommand(executable), true
	}
	if len(configuredPrefix) != 1 ||
		!strings.EqualFold(filepath.Base(executable.Clean), "node.exe") {
		return resolvedProviderCommand{}, false
	}
	extension := filepath.Ext(configuredPrefix[0])
	if extension != ".js" && extension != ".mjs" {
		return resolvedProviderCommand{}, false
	}
	entrypoint, disposition := validateEntrypointPath(configuredPrefix[0])
	if disposition != pathSafe {
		return resolvedProviderCommand{}, false
	}
	return resolvedProviderCommand{
		Executable: executable,
		Entrypoint: &entrypoint,
		PrefixArgs: []string{entrypoint.Resolved},
	}, true
}
