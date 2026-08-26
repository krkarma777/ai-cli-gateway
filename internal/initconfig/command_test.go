package initconfig

import (
	"context"
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestResolveCommandCandidateRejectsUnknownProviderAndUnsafePath(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider core.ProviderName
		path     string
	}{
		{name: "unknown provider", provider: core.ProviderName("unknown"), path: testAbsolutePath("bin", "provider")},
		{name: "empty path", provider: core.ProviderCodex, path: ""},
		{name: "relative path", provider: core.ProviderCodex, path: "relative-provider"},
		{name: "NUL path", provider: core.ProviderCodex, path: testAbsolutePath("bin", "provider") + "\x00tail"},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			_, err := ResolveCommandCandidate(
				test.provider,
				test.path,
				"",
				DiscoveryDependencies{
					OpenCommandPath: func(string, CommandReadMode, int64) (CommandFileInspection, error) {
						calls++
						return nil, nil
					},
				},
			)
			if !errors.Is(err, ErrPlan) {
				t.Fatalf("ResolveCommandCandidate() error = %v, want ErrPlan", err)
			}
			if calls != 0 {
				t.Fatalf("OpenCommandPath calls = %d, want 0", calls)
			}
		})
	}
}

func TestResolveCommandCandidateRecognizesOnlyFrozenProviderWindowsShims(t *testing.T) {
	for provider, relative := range map[core.ProviderName]string{
		core.ProviderCodex:  `node_modules\@openai\codex\bin\codex.js`,
		core.ProviderClaude: `node_modules\@anthropic-ai\claude-code\cli.js`,
		core.ProviderGemini: `node_modules\@google\gemini-cli\dist\index.js`,
	} {
		for _, payload := range [][]byte{
			commandTestLegacyWindowsNPMShim(relative),
			commandTestModernWindowsNPMShim(relative),
		} {
			got, ok := recognizeWindowsNPMShim(provider, payload)
			if !ok || got != relative {
				t.Fatalf("recognizeWindowsNPMShim(%q) = %q/%v, want %q/true", provider, got, ok, relative)
			}
			for _, mutation := range [][]byte{
				append(append([]byte(nil), payload...), []byte(" & calc.exe\r\n")...),
				[]byte(strings.Replace(string(payload), `%~dp0`, `%CD%`, 1)),
				[]byte(strings.Replace(string(payload), relative, `node_modules\external\index.js`, 1)),
			} {
				if got, ok := recognizeWindowsNPMShim(provider, mutation); ok || got != "" {
					t.Fatalf("mutated %q shim accepted as %q", provider, got)
				}
			}
		}
	}
	if got, ok := recognizeWindowsNPMShim(
		core.ProviderName("unknown"),
		commandTestLegacyWindowsNPMShim(`node_modules\unknown\index.js`),
	); ok || got != "" {
		t.Fatalf("unknown provider shim accepted as %q", got)
	}
}

func TestResolveCommandCandidateDiscoveryInspectionStopsOnCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	inspection := newCommandTestInspection("provider.cmd", nil)
	calls := 0
	wrapped := discoveryCommandDependencies(ctx, DiscoveryDependencies{
		OpenCommandPath: func(string, CommandReadMode, int64) (CommandFileInspection, error) {
			calls++
			cancel()
			return inspection, nil
		},
	})
	got, err := wrapped.OpenCommandPath("ignored", CommandIdentityOnly, 0)
	if !errors.Is(err, context.Canceled) || got != nil {
		t.Fatalf("first inspection = %#v/%v, want nil/context.Canceled", got, err)
	}
	if calls != 1 || inspection.closes != 1 {
		t.Fatalf("calls/closes = %d/%d, want 1/1", calls, inspection.closes)
	}
	got, err = wrapped.OpenCommandPath("ignored-again", CommandIdentityOnly, 0)
	if !errors.Is(err, context.Canceled) || got != nil || calls != 1 {
		t.Fatalf("second inspection = %#v/%v calls=%d", got, err, calls)
	}
}

func commandTestLegacyWindowsNPMShim(relativeEntrypoint string) []byte {
	return []byte("@IF EXIST \"%~dp0\\node.exe\" (\r\n" +
		"  \"%~dp0\\node.exe\"  \"%~dp0\\" + relativeEntrypoint + "\" %*\r\n" +
		") ELSE (\r\n" +
		"  @SETLOCAL\r\n" +
		"  @SET PATHEXT=%PATHEXT:;.JS;=;%\r\n" +
		"  node  \"%~dp0\\" + relativeEntrypoint + "\" %*\r\n" +
		")\r\n")
}

func commandTestModernWindowsNPMShim(relativeEntrypoint string) []byte {
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

type commandTestInspection struct {
	payload       []byte
	info          fs.FileInfo
	revalidateErr error
	closeErr      error
	revalidations int
	closes        int
}

func (i *commandTestInspection) Bytes() []byte {
	return append([]byte(nil), i.payload...)
}

func (i *commandTestInspection) FileInfo() fs.FileInfo {
	return i.info
}

func (i *commandTestInspection) Revalidate() error {
	i.revalidations++
	return i.revalidateErr
}

func (i *commandTestInspection) Close() error {
	i.closes++
	return i.closeErr
}

type commandTestFileInfo struct {
	name string
	mode fs.FileMode
}

func (i commandTestFileInfo) Name() string       { return i.name }
func (i commandTestFileInfo) Size() int64        { return 0 }
func (i commandTestFileInfo) Mode() fs.FileMode  { return i.mode }
func (i commandTestFileInfo) ModTime() time.Time { return time.Time{} }
func (i commandTestFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i commandTestFileInfo) Sys() any           { return nil }

func newCommandTestInspection(name string, payload []byte) *commandTestInspection {
	return &commandTestInspection{
		payload: append([]byte(nil), payload...),
		info:    commandTestFileInfo{name: name, mode: 0o700},
	}
}
