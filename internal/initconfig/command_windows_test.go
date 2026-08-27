//go:build windows

package initconfig

import (
	"errors"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestResolveCommandCandidateReturnsExplicitWindowsNodeCommand(t *testing.T) {
	node := `C:\Trusted\node.exe`
	entrypoint := `C:\Trusted\node_modules\@openai\codex\bin\codex.js`
	opened := []string{}
	inspections := []*commandTestInspection{
		newCommandTestInspection("node.exe", nil),
		newCommandTestInspection("codex.js", nil),
	}
	got, err := ResolveCommandCandidate(
		core.ProviderCodex,
		node,
		entrypoint,
		DiscoveryDependencies{
			OpenCommandPath: func(path string, mode CommandReadMode, limit int64) (CommandFileInspection, error) {
				if mode != CommandIdentityOnly || limit != 0 {
					t.Fatalf("OpenCommandPath mode/limit = %v/%d", mode, limit)
				}
				opened = append(opened, path)
				return inspections[len(opened)-1], nil
			},
		},
	)
	if err != nil {
		t.Fatalf("ResolveCommandCandidate() error = %v", err)
	}
	want := ProviderCommand{Executable: node, PrefixArgs: []string{entrypoint}}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(opened, []string{node, entrypoint}) {
		t.Fatalf("command/opened = %#v/%q, want %#v", got, opened, want)
	}
	for index, inspection := range inspections {
		if inspection.revalidations != 1 || inspection.closes != 1 {
			t.Fatalf("inspection %d revalidations/closes = %d/%d", index, inspection.revalidations, inspection.closes)
		}
	}
}

func TestResolveCommandCandidateRecognizesClosedWindowsNPMShim(t *testing.T) {
	shim := `C:\Trusted\bin\codex.cmd`
	entrypoint := `C:\Trusted\bin\node_modules\@openai\codex\bin\codex.js`
	node := `C:\Trusted\bin\node.exe`
	payload := frozenWindowsNPMShim(`node_modules\@openai\codex\bin\codex.js`)
	opened := []string{}
	inspections := map[string]*commandTestInspection{
		shim:       newCommandTestInspection("codex.cmd", payload),
		node:       newCommandTestInspection("node.exe", nil),
		entrypoint: newCommandTestInspection("codex.js", nil),
	}
	got, err := ResolveCommandCandidate(
		core.ProviderCodex,
		shim,
		"",
		DiscoveryDependencies{
			LookPath: func(string) (string, error) {
				t.Fatal("LookPath called despite a valid sibling node.exe")
				return "", nil
			},
			OpenCommandPath: func(path string, mode CommandReadMode, limit int64) (CommandFileInspection, error) {
				opened = append(opened, path)
				if path == shim {
					if mode != CommandBoundedContent || limit != maxWindowsCommandShimBytes {
						t.Fatalf("shim mode/limit = %v/%d", mode, limit)
					}
				} else if mode != CommandIdentityOnly || limit != 0 {
					t.Fatalf("identity mode/limit = %v/%d", mode, limit)
				}
				inspection, ok := inspections[path]
				if !ok {
					return nil, errors.New("missing fixture")
				}
				return inspection, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("ResolveCommandCandidate() error = %v", err)
	}
	want := ProviderCommand{Executable: node, PrefixArgs: []string{entrypoint}}
	if !reflect.DeepEqual(got, want) || !reflect.DeepEqual(opened, []string{shim, node, entrypoint}) {
		t.Fatalf("command/opened = %#v/%q, want %#v", got, opened, want)
	}
	for path, inspection := range inspections {
		if inspection.revalidations != 1 || inspection.closes != 1 {
			t.Fatalf("%s revalidations/closes = %d/%d", path, inspection.revalidations, inspection.closes)
		}
	}
}

func TestResolveCommandCandidateWindowsShimFallsBackToAbsolutePATHNode(t *testing.T) {
	shim := `C:\Trusted\bin\claude.cmd`
	sibling := `C:\Trusted\bin\node.exe`
	node := `D:\Trusted\node.exe`
	entrypoint := `C:\Trusted\bin\node_modules\@anthropic-ai\claude-code\cli.js`
	shimInspection := newCommandTestInspection("claude.cmd", frozenWindowsNPMShim(`node_modules\@anthropic-ai\claude-code\cli.js`))
	nodeInspection := newCommandTestInspection("node.exe", nil)
	entrypointInspection := newCommandTestInspection("cli.js", nil)
	lookups := 0
	got, err := ResolveCommandCandidate(
		core.ProviderClaude,
		shim,
		"",
		DiscoveryDependencies{
			LookPath: func(name string) (string, error) {
				lookups++
				if name != "node.exe" {
					t.Fatalf("LookPath(%q), want node.exe", name)
				}
				return node, nil
			},
			OpenCommandPath: func(path string, mode CommandReadMode, _ int64) (CommandFileInspection, error) {
				switch path {
				case shim:
					if mode != CommandBoundedContent {
						t.Fatalf("shim mode = %v", mode)
					}
					return shimInspection, nil
				case sibling:
					return nil, errors.New("sibling unavailable")
				case node:
					return nodeInspection, nil
				case entrypoint:
					return entrypointInspection, nil
				default:
					t.Fatalf("unexpected path %q", path)
					return nil, nil
				}
			},
		},
	)
	if err != nil {
		t.Fatalf("ResolveCommandCandidate() error = %v", err)
	}
	want := ProviderCommand{Executable: node, PrefixArgs: []string{entrypoint}}
	if !reflect.DeepEqual(got, want) || lookups != 1 {
		t.Fatalf("command/lookups = %#v/%d, want %#v/1", got, lookups, want)
	}
}

func TestResolveCommandCandidateRejectsWindowsShimWithoutValidatedNode(t *testing.T) {
	shim := `C:\Trusted\bin\codex.cmd`
	sibling := `C:\Trusted\bin\node.exe`
	for _, test := range []struct {
		name     string
		lookPath func(string) (string, error)
	}{
		{
			name: "missing PATH node",
			lookPath: func(string) (string, error) {
				return "", exec.ErrNotFound
			},
		},
		{
			name: "relative PATH node",
			lookPath: func(string) (string, error) {
				return "node.exe", nil
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			shimInspection := newCommandTestInspection(
				"codex.cmd",
				frozenWindowsNPMShim(`node_modules\@openai\codex\bin\codex.js`),
			)
			_, err := ResolveCommandCandidate(
				core.ProviderCodex,
				shim,
				"",
				DiscoveryDependencies{
					LookPath: test.lookPath,
					OpenCommandPath: func(path string, _ CommandReadMode, _ int64) (CommandFileInspection, error) {
						if path == shim {
							return shimInspection, nil
						}
						if path == sibling {
							return nil, errors.New("no sibling")
						}
						t.Fatalf("unexpected inspection %q", path)
						return nil, nil
					},
				},
			)
			if !errors.Is(err, ErrPlan) {
				t.Fatalf("ResolveCommandCandidate() error = %v, want ErrPlan", err)
			}
			if shimInspection.closes != 1 {
				t.Fatalf("shim Close calls = %d, want 1", shimInspection.closes)
			}
		})
	}
}

func TestResolveCommandCandidateClosesAllWindowsEvidenceAfterIdentityChange(t *testing.T) {
	shim := `C:\Trusted\bin\gemini.cmd`
	node := `C:\Trusted\bin\node.exe`
	entrypoint := `C:\Trusted\bin\node_modules\@google\gemini-cli\dist\index.js`
	inspections := map[string]*commandTestInspection{
		shim: newCommandTestInspection(
			"gemini.cmd",
			frozenWindowsNPMShim(`node_modules\@google\gemini-cli\dist\index.js`),
		),
		node:       newCommandTestInspection("node.exe", nil),
		entrypoint: newCommandTestInspection("index.js", nil),
	}
	inspections[entrypoint].revalidateErr = errors.New("changed entrypoint")
	_, err := ResolveCommandCandidate(
		core.ProviderGemini,
		shim,
		"",
		DiscoveryDependencies{
			OpenCommandPath: func(path string, _ CommandReadMode, _ int64) (CommandFileInspection, error) {
				inspection, ok := inspections[path]
				if !ok {
					t.Fatalf("unexpected inspection %q", path)
				}
				return inspection, nil
			},
		},
	)
	if !errors.Is(err, ErrPlan) {
		t.Fatalf("ResolveCommandCandidate() error = %v, want ErrPlan", err)
	}
	for path, inspection := range inspections {
		if inspection.revalidations != 1 || inspection.closes != 1 {
			t.Fatalf("%s revalidations/closes = %d/%d, want 1/1", path, inspection.revalidations, inspection.closes)
		}
	}
}

func TestResolveCommandCandidateRejectsUnrecognizedWindowsShimText(t *testing.T) {
	valid := string(frozenWindowsNPMShim(`node_modules\@google\gemini-cli\dist\index.js`))
	for _, test := range []struct {
		name    string
		payload string
	}{
		{name: "arbitrary batch", payload: `@echo pwned`},
		{name: "PowerShell", payload: `powershell -Command Get-ChildItem`},
		{name: "external entrypoint", payload: strings.Replace(valid, `%~dp0\node_modules`, `D:\external\node_modules`, 1)},
		{name: "relative entrypoint", payload: strings.Replace(valid, `%~dp0\node_modules`, `..\node_modules`, 1)},
		{name: "wrong provider package", payload: strings.Replace(valid, `@google\gemini-cli`, `@openai\codex`, 1)},
		{name: "wrong extension", payload: strings.Replace(valid, `index.js`, `index.cjs`, 1)},
		{name: "wrong extension case", payload: strings.Replace(valid, `index.js`, `index.JS`, 1)},
		{name: "different variable", payload: strings.Replace(valid, `%~dp0`, `%CD%`, 1)},
		{name: "command chaining", payload: valid + " & calc.exe\r\n"},
		{name: "quoting ambiguity", payload: strings.Replace(valid, `"%~dp0\node.exe"`, `%~dp0\node.exe`, 1)},
		{name: "overflow", payload: strings.Repeat("x", int(maxWindowsCommandShimBytes)+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			shim := `C:\Trusted\bin\gemini.cmd`
			inspection := newCommandTestInspection("gemini.cmd", []byte(test.payload))
			_, err := ResolveCommandCandidate(
				core.ProviderGemini,
				shim,
				"",
				DiscoveryDependencies{
					LookPath: func(string) (string, error) {
						t.Fatal("LookPath called for rejected shim")
						return "", nil
					},
					OpenCommandPath: func(path string, mode CommandReadMode, limit int64) (CommandFileInspection, error) {
						if path != shim || mode != CommandBoundedContent || limit != maxWindowsCommandShimBytes {
							t.Fatalf("OpenCommandPath(%q, %v, %d)", path, mode, limit)
						}
						return inspection, nil
					},
				},
			)
			if !errors.Is(err, ErrPlan) {
				t.Fatalf("ResolveCommandCandidate() error = %v, want ErrPlan", err)
			}
			if inspection.closes != 1 {
				t.Fatalf("Close calls = %d, want 1", inspection.closes)
			}
		})
	}
}

func TestResolveCommandCandidateRejectsUnsafeWindowsShapes(t *testing.T) {
	for _, test := range []struct {
		name       string
		path       string
		entrypoint string
	}{
		{name: "batch file", path: `C:\Trusted\codex.bat`},
		{name: "node without entrypoint", path: `C:\Trusted\node.exe`},
		{name: "entrypoint with native executable", path: `C:\Trusted\codex.exe`, entrypoint: `C:\Trusted\codex.js`},
		{name: "relative entrypoint", path: `C:\Trusted\node.exe`, entrypoint: `relative.js`},
		{name: "wrong entrypoint extension", path: `C:\Trusted\node.exe`, entrypoint: `C:\Trusted\codex.cjs`},
		{name: "uppercase entrypoint extension", path: `C:\Trusted\node.exe`, entrypoint: `C:\Trusted\codex.JS`},
		{name: "entrypoint with cmd shim", path: `C:\Trusted\codex.cmd`, entrypoint: `C:\Trusted\codex.js`},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			_, err := ResolveCommandCandidate(
				core.ProviderCodex,
				test.path,
				test.entrypoint,
				DiscoveryDependencies{
					OpenCommandPath: func(string, CommandReadMode, int64) (CommandFileInspection, error) {
						calls++
						return newCommandTestInspection(filepath.Base(test.path), nil), nil
					},
				},
			)
			if !errors.Is(err, ErrPlan) || calls != 0 {
				t.Fatalf("error/calls = %v/%d, want ErrPlan/0", err, calls)
			}
		})
	}
}

func frozenWindowsNPMShim(relativeEntrypoint string) []byte {
	return []byte("@IF EXIST \"%~dp0\\node.exe\" (\r\n" +
		"  \"%~dp0\\node.exe\"  \"%~dp0\\" + relativeEntrypoint + "\" %*\r\n" +
		") ELSE (\r\n" +
		"  @SETLOCAL\r\n" +
		"  @SET PATHEXT=%PATHEXT:;.JS;=;%\r\n" +
		"  node  \"%~dp0\\" + relativeEntrypoint + "\" %*\r\n" +
		")\r\n")
}
