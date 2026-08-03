// Package testutil contains test-only executable build helpers.
package testutil

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

const maxBuildOutputBytes = 64 * 1024
const maxBuildDuration = 60 * time.Second
const maxRepositoryRootSearchDepth = 8
const expectedModuleDeclaration = "module github.com/krkarma777/ai-cli-gateway"

var errRepositoryRootUnavailable = errors.New("repository root unavailable")

// BuildFakeCLI builds the deterministic fake provider CLI in a trusted fixture.
func BuildFakeCLI(t testing.TB) string {
	t.Helper()
	return buildTemporaryCommand(t, "./internal/testcli/cmd/fake-ai-cli", "fake-ai-cli")
}

// BuildGateway builds the gateway command in a trusted fixture.
func BuildGateway(t testing.TB) string {
	t.Helper()
	return buildTemporaryCommand(t, "./cmd/ai-cli-gateway", "ai-cli-gateway")
}

func buildTemporaryCommand(t testing.TB, packagePath, name string) string {
	t.Helper()
	root := repositoryRoot(t)
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	output := filepath.Join(TrustedTempDir(t), name)
	var combined boundedBuffer
	err := runWithBuildDeadline(
		context.Background(),
		maxBuildDuration,
		func(ctx context.Context) error {
			// Package paths and output are fixed or test-owned; no shell is involved.
			//nolint:gosec
			cmd := exec.CommandContext(
				ctx,
				"go",
				"build",
				"-o",
				output,
				packagePath,
			)
			cmd.Dir = root
			cmd.Stdout = &combined
			cmd.Stderr = &combined
			return cmd.Run()
		},
	)
	if err != nil {
		t.Fatalf(
			"go build %s: %v\n%s",
			packagePath,
			err,
			combined.String(),
		)
	}
	absolute, err := filepath.Abs(output)
	if err != nil {
		t.Fatalf("absolute build output: %v", err)
	}
	return absolute
}

func runWithBuildDeadline(
	parent context.Context,
	timeout time.Duration,
	run func(context.Context) error,
) error {
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return run(ctx)
}

func repositoryRoot(t testing.TB) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate testutil source")
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal("locate repository root")
	}
	root, err := locateRepositoryRoot(file, cwd)
	if err != nil {
		t.Fatal("locate repository root")
	}
	return root
}

type repositoryFileOps struct {
	lstat    func(string) (fs.FileInfo, error)
	readFile func(string) ([]byte, error)
}

type repositoryRootState uint8

const (
	repositoryRootAbsent repositoryRootState = iota
	repositoryRootValid
	repositoryRootUnsafe
)

func locateRepositoryRoot(callerPath, workingDirectory string) (string, error) {
	return locateRepositoryRootWithFS(
		callerPath,
		workingDirectory,
		repositoryFileOps{lstat: os.Lstat, readFile: os.ReadFile},
	)
}

func locateRepositoryRootWithFS(
	callerPath string,
	workingDirectory string,
	ops repositoryFileOps,
) (string, error) {
	if ops.lstat == nil || ops.readFile == nil ||
		!filepath.IsAbs(workingDirectory) {
		return "", errRepositoryRootUnavailable
	}

	if filepath.IsAbs(callerPath) {
		callerRoot := filepath.Dir(filepath.Dir(filepath.Dir(
			filepath.Clean(callerPath),
		)))
		state := inspectRepositoryRoot(callerRoot, ops)
		switch state {
		case repositoryRootValid:
			return callerRoot, nil
		case repositoryRootUnsafe:
			return "", errRepositoryRootUnavailable
		case repositoryRootAbsent:
		}
	}

	current := filepath.Clean(workingDirectory)
	found := ""
	for range maxRepositoryRootSearchDepth {
		if isFilesystemRoot(current) {
			break
		}
		switch inspectRepositoryRoot(current, ops) {
		case repositoryRootValid:
			if found != "" && found != current {
				return "", errRepositoryRootUnavailable
			}
			found = current
		case repositoryRootUnsafe:
			return "", errRepositoryRootUnavailable
		case repositoryRootAbsent:
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	if found == "" {
		return "", errRepositoryRootUnavailable
	}
	return found, nil
}

func inspectRepositoryRoot(path string, ops repositoryFileOps) repositoryRootState {
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) || isFilesystemRoot(path) {
		return repositoryRootUnsafe
	}
	directoryInfo, err := ops.lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return repositoryRootAbsent
	}
	if err != nil || !directoryInfo.IsDir() {
		return repositoryRootUnsafe
	}

	modulePath := filepath.Join(path, "go.mod")
	moduleInfo, err := ops.lstat(modulePath)
	if errors.Is(err, fs.ErrNotExist) {
		return repositoryRootAbsent
	}
	if err != nil || !moduleInfo.Mode().IsRegular() {
		return repositoryRootUnsafe
	}
	contents, err := ops.readFile(modulePath)
	if err != nil {
		return repositoryRootUnsafe
	}
	line := contents
	if index := bytes.IndexByte(line, '\n'); index >= 0 {
		line = line[:index]
	}
	line = bytes.TrimSuffix(line, []byte{'\r'})
	if string(line) != expectedModuleDeclaration {
		return repositoryRootUnsafe
	}
	return repositoryRootValid
}

func isFilesystemRoot(path string) bool {
	cleaned := filepath.Clean(path)
	return filepath.Dir(cleaned) == cleaned
}

type boundedBuffer struct {
	data bytes.Buffer
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	remaining := maxBuildOutputBytes - b.data.Len()
	if remaining > 0 {
		_, _ = b.data.Write(p[:min(len(p), remaining)])
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string {
	if b.data.Len() == maxBuildOutputBytes {
		_, _ = io.WriteString(&b.data, "\n[build output truncated]")
	}
	return b.data.String()
}
