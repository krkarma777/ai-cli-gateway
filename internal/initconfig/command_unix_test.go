//go:build !windows

package initconfig

import (
	"errors"
	"io/fs"
	"reflect"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestResolveCommandCandidateReturnsNativeUnixExecutable(t *testing.T) {
	path := testAbsolutePath("bin", "codex")
	inspection := newCommandTestInspection("codex", nil)
	got, err := ResolveCommandCandidate(
		core.ProviderCodex,
		path,
		"",
		DiscoveryDependencies{
			OpenCommandPath: func(gotPath string, mode CommandReadMode, limit int64) (CommandFileInspection, error) {
				if gotPath != path || mode != CommandIdentityOnly || limit != 0 {
					t.Fatalf("OpenCommandPath(%q, %v, %d)", gotPath, mode, limit)
				}
				return inspection, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("ResolveCommandCandidate() error = %v", err)
	}
	want := ProviderCommand{Executable: path}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveCommandCandidate() = %#v, want %#v", got, want)
	}
	if inspection.revalidations != 1 || inspection.closes != 1 {
		t.Fatalf("revalidations/closes = %d/%d, want 1/1", inspection.revalidations, inspection.closes)
	}
}

func TestResolveCommandCandidateRejectsUnixEntrypointWithoutInspecting(t *testing.T) {
	calls := 0
	_, err := ResolveCommandCandidate(
		core.ProviderCodex,
		testAbsolutePath("bin", "node"),
		testAbsolutePath("package", "codex.js"),
		DiscoveryDependencies{
			OpenCommandPath: func(string, CommandReadMode, int64) (CommandFileInspection, error) {
				calls++
				return nil, nil
			},
		},
	)
	if !errors.Is(err, ErrPlan) || calls != 0 {
		t.Fatalf("error/calls = %v/%d, want ErrPlan/0", err, calls)
	}
}

func TestResolveCommandCandidateClosesRejectedUnixInspection(t *testing.T) {
	path := testAbsolutePath("bin", "codex")
	for _, test := range []struct {
		name       string
		inspection *commandTestInspection
	}{
		{
			name:       "missing identity",
			inspection: &commandTestInspection{},
		},
		{
			name: "directory identity",
			inspection: &commandTestInspection{
				info: commandTestFileInfo{name: "codex", mode: fs.ModeDir | 0o700},
			},
		},
		{
			name: "symlink identity",
			inspection: &commandTestInspection{
				info: commandTestFileInfo{name: "codex", mode: fs.ModeSymlink | 0o700},
			},
		},
		{
			name: "revalidation failure",
			inspection: &commandTestInspection{
				info:          commandTestFileInfo{name: "codex", mode: 0o700},
				revalidateErr: errors.New("planted revalidation detail"),
			},
		},
		{
			name: "close failure",
			inspection: &commandTestInspection{
				info:     commandTestFileInfo{name: "codex", mode: 0o700},
				closeErr: errors.New("planted close detail"),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ResolveCommandCandidate(
				core.ProviderCodex,
				path,
				"",
				DiscoveryDependencies{
					OpenCommandPath: func(string, CommandReadMode, int64) (CommandFileInspection, error) {
						return test.inspection, nil
					},
				},
			)
			if !errors.Is(err, ErrPlan) {
				t.Fatalf("ResolveCommandCandidate() error = %v, want ErrPlan", err)
			}
			if test.inspection.closes != 1 {
				t.Fatalf("Close calls = %d, want 1", test.inspection.closes)
			}
		})
	}
}
