package initconfig

import (
	"bytes"
	"path/filepath"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/trustedpath"
)

var windowsProviderEntrypoints = map[core.ProviderName]string{
	core.ProviderCodex:  `node_modules\@openai\codex\bin\codex.js`,
	core.ProviderClaude: `node_modules\@anthropic-ai\claude-code\cli.js`,
	core.ProviderGemini: `node_modules\@google\gemini-cli\dist\index.js`,
}

// CommandFileInspection is retained command identity and optional content.
type CommandFileInspection = trustedpath.CommandFileInspection

// CommandReadMode selects identity-only or bounded-content inspection.
type CommandReadMode = trustedpath.CommandReadMode

const (
	// CommandIdentityOnly requests command identity without reading content.
	CommandIdentityOnly = trustedpath.CommandIdentityOnly
	// CommandBoundedContent requests identity plus bounded command content.
	CommandBoundedContent = trustedpath.CommandBoundedContent
)

// ResolveCommandCandidate validates one confirmation-required command suggestion.
func ResolveCommandCandidate(
	name core.ProviderName,
	path string,
	explicitEntrypoint string,
	deps DiscoveryDependencies,
) (ProviderCommand, error) {
	if !knownProvider(name) || !safeText(path) || !filepath.IsAbs(path) {
		return ProviderCommand{}, ErrPlan
	}
	if explicitEntrypoint != "" &&
		(!safeText(explicitEntrypoint) || !filepath.IsAbs(explicitEntrypoint)) {
		return ProviderCommand{}, ErrPlan
	}
	cleanEntrypoint := ""
	if explicitEntrypoint != "" {
		cleanEntrypoint = filepath.Clean(explicitEntrypoint)
	}
	return resolvePlatformCommandCandidate(
		name,
		filepath.Clean(path),
		cleanEntrypoint,
		deps,
	)
}

type commandInspectionSet struct {
	values []CommandFileInspection
}

func (s *commandInspectionSet) open(
	path string,
	mode CommandReadMode,
	limit int64,
	deps DiscoveryDependencies,
) (CommandFileInspection, error) {
	open := commandPathOpener(deps)
	inspection, err := open(path, mode, limit)
	if err != nil || inspection == nil {
		if inspection != nil {
			_ = inspection.Close()
		}
		return nil, ErrPlan
	}
	s.values = append(s.values, inspection)
	info := inspection.FileInfo()
	if info == nil || !info.Mode().IsRegular() {
		return nil, ErrPlan
	}
	return inspection, nil
}

func commandPathOpener(
	deps DiscoveryDependencies,
) func(string, CommandReadMode, int64) (CommandFileInspection, error) {
	if deps.OpenCommandPath != nil {
		return deps.OpenCommandPath
	}
	return trustedpath.OpenCommandPath
}

func (s *commandInspectionSet) revalidateAndClose() error {
	valid := true
	for _, inspection := range s.values {
		if inspection.Revalidate() != nil {
			valid = false
		}
	}
	if s.close() != nil {
		valid = false
	}
	if !valid {
		return ErrPlan
	}
	return nil
}

func (s *commandInspectionSet) close() error {
	valid := true
	for index := len(s.values) - 1; index >= 0; index-- {
		if s.values[index].Close() != nil {
			valid = false
		}
	}
	s.values = nil
	if !valid {
		return ErrPlan
	}
	return nil
}

func recognizeWindowsNPMShim(
	name core.ProviderName,
	payload []byte,
) (string, bool) {
	relativeEntrypoint, ok := windowsProviderEntrypoints[name]
	if !ok {
		return "", false
	}
	if !bytes.Equal(payload, frozenWindowsNPMCommand(relativeEntrypoint)) &&
		!bytes.Equal(payload, frozenModernWindowsNPMCommand(relativeEntrypoint)) {
		return "", false
	}
	return relativeEntrypoint, true
}

func frozenWindowsNPMCommand(relativeEntrypoint string) []byte {
	return []byte("@IF EXIST \"%~dp0\\node.exe\" (\r\n" +
		"  \"%~dp0\\node.exe\"  \"%~dp0\\" + relativeEntrypoint + "\" %*\r\n" +
		") ELSE (\r\n" +
		"  @SETLOCAL\r\n" +
		"  @SET PATHEXT=%PATHEXT:;.JS;=;%\r\n" +
		"  node  \"%~dp0\\" + relativeEntrypoint + "\" %*\r\n" +
		")\r\n")
}

func frozenModernWindowsNPMCommand(relativeEntrypoint string) []byte {
	return []byte("@ECHO off\r\n" +
		"GOTO start\r\n" +
		":find_dp0\r\n" +
		"SET dp0=%~dp0\r\n" +
		"EXIT /b\r\n" +
		":start\r\n" +
		"SETLOCAL\r\n" +
		"CALL :find_dp0\r\n" +
		"\r\n" +
		"IF EXIST \"%dp0%\\node.exe\" (\r\n" +
		"  SET \"_prog=%dp0%\\node.exe\"\r\n" +
		") ELSE (\r\n" +
		"  SET \"_prog=node\"\r\n" +
		"  SET PATHEXT=%PATHEXT:;.JS;=;%\r\n" +
		")\r\n" +
		"\r\n" +
		"endLocal & goto #_undefined_# 2>NUL || title %COMSPEC% & " +
		"\"%_prog%\"  \"%dp0%\\" + relativeEntrypoint + "\" %*\r\n")
}
