//go:build windows

package initconfig

import (
	"path/filepath"
	"strings"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func resolvePlatformCommandCandidate(
	name core.ProviderName,
	path string,
	explicitEntrypoint string,
	deps DiscoveryDependencies,
) (ProviderCommand, error) {
	extension := filepath.Ext(path)
	if extension == ".cmd" {
		if explicitEntrypoint != "" {
			return ProviderCommand{}, ErrPlan
		}
		return resolveWindowsShimCandidate(name, path, deps)
	}
	if strings.EqualFold(extension, ".cmd") || strings.EqualFold(extension, ".bat") {
		return ProviderCommand{}, ErrPlan
	}
	if explicitEntrypoint == "" {
		if strings.EqualFold(filepath.Base(path), "node.exe") {
			return ProviderCommand{}, ErrPlan
		}
		return resolveWindowsNativeCandidate(path, deps)
	}
	if !strings.EqualFold(filepath.Base(path), "node.exe") ||
		!windowsJavaScriptEntrypoint(explicitEntrypoint) {
		return ProviderCommand{}, ErrPlan
	}
	inspections := commandInspectionSet{}
	defer inspections.close() //nolint:errcheck // Explicit finalization reports close failures.
	if _, err := inspections.open(path, CommandIdentityOnly, 0, deps); err != nil {
		return ProviderCommand{}, ErrPlan
	}
	if _, err := inspections.open(
		explicitEntrypoint,
		CommandIdentityOnly,
		0,
		deps,
	); err != nil {
		return ProviderCommand{}, ErrPlan
	}
	if err := inspections.revalidateAndClose(); err != nil {
		return ProviderCommand{}, ErrPlan
	}
	return ProviderCommand{
		Executable: path,
		PrefixArgs: []string{explicitEntrypoint},
	}, nil
}

func resolveWindowsNativeCandidate(
	path string,
	deps DiscoveryDependencies,
) (ProviderCommand, error) {
	inspections := commandInspectionSet{}
	defer inspections.close() //nolint:errcheck // Explicit finalization reports close failures.
	if _, err := inspections.open(path, CommandIdentityOnly, 0, deps); err != nil {
		return ProviderCommand{}, ErrPlan
	}
	if err := inspections.revalidateAndClose(); err != nil {
		return ProviderCommand{}, ErrPlan
	}
	return ProviderCommand{Executable: path}, nil
}

func resolveWindowsShimCandidate(
	name core.ProviderName,
	shimPath string,
	deps DiscoveryDependencies,
) (ProviderCommand, error) {
	inspections := commandInspectionSet{}
	defer inspections.close() //nolint:errcheck // Explicit finalization reports close failures.
	shim, err := inspections.open(
		shimPath,
		CommandBoundedContent,
		maxWindowsCommandShimBytes,
		deps,
	)
	if err != nil {
		return ProviderCommand{}, ErrPlan
	}
	payload := shim.Bytes()
	if int64(len(payload)) > maxWindowsCommandShimBytes {
		return ProviderCommand{}, ErrPlan
	}
	relativeEntrypoint, ok := recognizeWindowsNPMShim(name, payload)
	if !ok {
		return ProviderCommand{}, ErrPlan
	}
	entrypoint := filepath.Clean(filepath.Join(
		filepath.Dir(shimPath),
		filepath.FromSlash(strings.ReplaceAll(relativeEntrypoint, `\`, "/")),
	))
	if !filepath.IsAbs(entrypoint) || !windowsJavaScriptEntrypoint(entrypoint) {
		return ProviderCommand{}, ErrPlan
	}

	node := filepath.Join(filepath.Dir(shimPath), "node.exe")
	local := commandInspectionSet{}
	if _, localErr := local.open(
		node,
		CommandIdentityOnly,
		0,
		deps,
	); localErr == nil {
		inspections.values = append(inspections.values, local.values...)
		local.values = nil
	} else {
		_ = local.close()
		if deps.LookPath == nil {
			return ProviderCommand{}, ErrPlan
		}
		candidate, lookupErr := deps.LookPath("node.exe")
		if lookupErr != nil || !safeText(candidate) ||
			!filepath.IsAbs(candidate) ||
			!strings.EqualFold(filepath.Base(candidate), "node.exe") {
			return ProviderCommand{}, ErrPlan
		}
		node = filepath.Clean(candidate)
		if _, err := inspections.open(
			node,
			CommandIdentityOnly,
			0,
			deps,
		); err != nil {
			return ProviderCommand{}, ErrPlan
		}
	}
	if _, err := inspections.open(
		entrypoint,
		CommandIdentityOnly,
		0,
		deps,
	); err != nil {
		return ProviderCommand{}, ErrPlan
	}
	if err := inspections.revalidateAndClose(); err != nil {
		return ProviderCommand{}, ErrPlan
	}
	return ProviderCommand{
		Executable: node,
		PrefixArgs: []string{entrypoint},
	}, nil
}

func windowsJavaScriptEntrypoint(path string) bool {
	extension := filepath.Ext(path)
	return extension == ".js" || extension == ".mjs"
}
