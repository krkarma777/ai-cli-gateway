package testutil

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestLocateRepositoryRootAcceptsAbsoluteCaller(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root, expectedModuleDeclaration+"\n")
	caller := filepath.Join(root, "internal", "testutil", "fakecli.go")
	if err := os.MkdirAll(filepath.Dir(caller), 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := locateRepositoryRoot(caller, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("root=%q want=%q", got, root)
	}
}

func TestLocateRepositoryRootFallsBackFromTrimmedCaller(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root, expectedModuleDeclaration+"\n")
	cwd := filepath.Join(root, "internal", "testutil")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}

	got, err := locateRepositoryRoot(
		"github.com/krkarma777/ai-cli-gateway/internal/testutil/fakecli.go",
		cwd,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != root {
		t.Fatalf("root=%q want=%q", got, root)
	}
}

func TestLocateRepositoryRootRejectsUnrelatedNestedModule(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root, expectedModuleDeclaration+"\n")
	nested := filepath.Join(root, "nested")
	writeTestModule(t, nested, "module example.invalid/unrelated\n")
	cwd := filepath.Join(nested, "internal", "testutil")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := locateRepositoryRoot("trimmed/internal/testutil/fakecli.go", cwd)
	if !errors.Is(err, errRepositoryRootUnavailable) {
		t.Fatalf("error=%v, want fixed repository-root failure", err)
	}
}

func TestLocateRepositoryRootRejectsAmbiguousMatchingModules(t *testing.T) {
	outer := t.TempDir()
	writeTestModule(t, outer, expectedModuleDeclaration+"\n")
	inner := filepath.Join(outer, "nested")
	writeTestModule(t, inner, expectedModuleDeclaration+"\n")
	cwd := filepath.Join(inner, "internal", "testutil")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := locateRepositoryRoot("trimmed/internal/testutil/fakecli.go", cwd)
	if !errors.Is(err, errRepositoryRootUnavailable) {
		t.Fatalf("error=%v, want fixed repository-root failure", err)
	}
}

func TestLocateRepositoryRootRejectsMissingAndUnsafeModules(t *testing.T) {
	tests := []struct {
		name    string
		prepare func(*testing.T, string)
	}{
		{name: "missing", prepare: func(*testing.T, string) {}},
		{
			name: "unrelated declaration",
			prepare: func(t *testing.T, root string) {
				writeTestModule(t, root, "module example.invalid/not-this-project\n")
			},
		},
		{
			name: "non regular",
			prepare: func(t *testing.T, root string) {
				if err := os.Mkdir(filepath.Join(root, "go.mod"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "symlink",
			prepare: func(t *testing.T, root string) {
				target := filepath.Join(t.TempDir(), "target.mod")
				if err := os.WriteFile(
					target,
					[]byte(expectedModuleDeclaration+"\n"),
					0o600,
				); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(root, "go.mod")); err != nil {
					t.Skipf("symlink unavailable: %v", err)
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			test.prepare(t, root)
			cwd := filepath.Join(root, "internal", "testutil")
			if err := os.MkdirAll(cwd, 0o700); err != nil {
				t.Fatal(err)
			}
			_, err := locateRepositoryRoot(
				"trimmed/internal/testutil/fakecli.go",
				cwd,
			)
			if !errors.Is(err, errRepositoryRootUnavailable) {
				t.Fatalf("error=%v, want fixed repository-root failure", err)
			}
		})
	}
}

func TestLocateRepositoryRootRejectsUnreadableModule(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root, expectedModuleDeclaration+"\n")
	caller := filepath.Join(root, "internal", "testutil", "fakecli.go")
	if err := os.MkdirAll(filepath.Dir(caller), 0o700); err != nil {
		t.Fatal(err)
	}
	ops := repositoryFileOps{
		lstat: os.Lstat,
		readFile: func(string) ([]byte, error) {
			return nil, fs.ErrPermission
		},
	}

	_, err := locateRepositoryRootWithFS(caller, t.TempDir(), ops)
	if !errors.Is(err, errRepositoryRootUnavailable) {
		t.Fatalf("error=%v, want fixed repository-root failure", err)
	}
}

func TestLocateRepositoryRootSearchIsBounded(t *testing.T) {
	root := t.TempDir()
	writeTestModule(t, root, expectedModuleDeclaration+"\n")
	cwd := root
	for index := 0; index <= maxRepositoryRootSearchDepth; index++ {
		cwd = filepath.Join(cwd, "nested")
	}
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := locateRepositoryRoot("trimmed/internal/testutil/fakecli.go", cwd)
	if !errors.Is(err, errRepositoryRootUnavailable) {
		t.Fatalf("error=%v, want bounded-search failure", err)
	}
}

func TestLocateRepositoryRootFindsRealRepository(t *testing.T) {
	_, caller, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test caller")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Dir(filepath.Dir(cwd))

	got, err := locateRepositoryRoot(caller, cwd)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("root=%q want=%q", got, want)
	}
}

func TestRunWithBuildDeadlineCancelsInjectedRunner(t *testing.T) {
	started := time.Now()
	err := runWithBuildDeadline(
		context.Background(),
		10*time.Millisecond,
		func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("injected deadline exceeded test bound: %v", elapsed)
	}
}

func TestBuildHelperReleaseBudgets(t *testing.T) {
	if maxBuildDuration != 60*time.Second {
		t.Fatalf("build duration=%v want=60s", maxBuildDuration)
	}
	if maxBuildOutputBytes != 64*1024 {
		t.Fatalf("build output cap=%d want=%d", maxBuildOutputBytes, 64*1024)
	}
	if maxRepositoryRootSearchDepth != 8 {
		t.Fatalf(
			"repository search depth=%d want=8",
			maxRepositoryRootSearchDepth,
		)
	}
}

func TestBuildFakeCLIProducesTemporaryExecutable(t *testing.T) {
	path := BuildFakeCLI(t)
	if !filepath.IsAbs(path) {
		t.Fatalf("path=%q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	// path is the exact executable just built below this test's TempDir.
	//nolint:gosec,noctx
	out, err := exec.Command(path, "--mode=text").CombinedOutput()
	if err != nil {
		t.Fatalf("run: %v output=%q", err, out)
	}
	if string(out) != "hello\n" {
		t.Fatalf("output=%q", out)
	}
}

func TestBuildGatewayProducesTemporaryExecutable(t *testing.T) {
	path := BuildGateway(t)
	if !filepath.IsAbs(path) {
		t.Fatalf("path=%q", path)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestBuiltFakeCLIBlockingModesRemainAliveUntilKilled(t *testing.T) {
	path := BuildFakeCLI(t)
	for _, mode := range []string{"hang", "child-hold", "session-escape"} {
		t.Run(mode, func(t *testing.T) {
			// path is the exact executable just built below this test's TempDir.
			//nolint:gosec,noctx
			cmd := exec.Command(path, "--mode="+mode)
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			wait := make(chan error, 1)
			go func() {
				wait <- cmd.Wait()
			}()
			waited := false
			t.Cleanup(func() {
				if waited {
					return
				}
				if cmd.Process != nil {
					_ = cmd.Process.Kill()
				}
				select {
				case <-wait:
					waited = true
				case <-time.After(30 * time.Second):
					t.Error("fake blocking process was not reaped")
				}
			})

			select {
			case err := <-wait:
				waited = true
				t.Fatalf("fake %s exited early: %v", mode, err)
			case <-time.After(50 * time.Millisecond):
			}
		})
	}
}

func writeTestModule(t *testing.T, root string, contents string) {
	t.Helper()
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(root, "go.mod"),
		[]byte(contents),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
}
