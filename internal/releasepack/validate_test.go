//go:build linux || darwin

package releasepack

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
	"time"
)

const expectedModule = "github.com/krkarma777/ai-cli-gateway"

var fixtureSourcePaths = []string{
	"go.mod",
	"README.md",
	"LICENSE",
	"THIRD_PARTY_NOTICES.md",
	"config.example.toml",
	filepath.Join("examples", "config", "codex.example.toml"),
	filepath.Join("examples", "openai-sdk", "python", "main.py"),
	filepath.Join("examples", "openai-sdk", "python", "requirements.txt"),
	filepath.Join("examples", "openai-sdk", "python", "requirements.lock"),
	filepath.Join("examples", "openai-sdk", "javascript", "main.mjs"),
	filepath.Join("examples", "openai-sdk", "javascript", "package.json"),
	filepath.Join("examples", "openai-sdk", "javascript", "package-lock.json"),
	filepath.Join("deploy", "systemd", "ai-cli-gateway.service"),
}

type releaseFixture struct {
	repositoryRoot string
	stagingRoot    string
	outputRoot     string
	options        ArchiveOptions
}

func TestNewArchivePlanAcceptsExactInputs(t *testing.T) {
	fixture := newReleaseFixture(t)

	plan, err := newArchivePlan(fixture.options)
	if err != nil {
		t.Fatalf("newArchivePlan() error = %v", err)
	}
	if plan.Version != "0.1.0" {
		t.Fatalf("Version = %q, want 0.1.0", plan.Version)
	}

	wantTargets := []target{
		{GOOS: "linux", GOARCH: "amd64", Directory: "linux_amd64", Executable: "ai-cli-gateway", Format: formatTarGzip, IncludeSystemd: true},
		{GOOS: "linux", GOARCH: "arm64", Directory: "linux_arm64", Executable: "ai-cli-gateway", Format: formatTarGzip, IncludeSystemd: true},
		{GOOS: "darwin", GOARCH: "amd64", Directory: "darwin_amd64", Executable: "ai-cli-gateway", Format: formatTarGzip},
		{GOOS: "darwin", GOARCH: "arm64", Directory: "darwin_arm64", Executable: "ai-cli-gateway", Format: formatTarGzip},
		{GOOS: "windows", GOARCH: "amd64", Directory: "windows_amd64", Executable: "ai-cli-gateway.exe", Format: formatZIP},
	}
	if !slices.Equal(plan.Targets, wantTargets) {
		t.Fatalf("Targets = %#v, want %#v", plan.Targets, wantTargets)
	}
	if len(plan.Binaries) != len(wantTargets) {
		t.Fatalf("len(Binaries) = %d, want %d", len(plan.Binaries), len(wantTargets))
	}
	for i, binary := range plan.Binaries {
		if binary.Target != wantTargets[i] {
			t.Errorf("Binaries[%d].Target = %#v, want %#v", i, binary.Target, wantTargets[i])
		}
		wantPath := filepath.Join(fixture.stagingRoot, wantTargets[i].Directory, wantTargets[i].Executable)
		if binary.Path != wantPath {
			t.Errorf("Binaries[%d].Path = %q, want %q", i, binary.Path, wantPath)
		}
	}

	wantCommon := make([]sourceFile, 0, len(fixtureSourcePaths)-1)
	for _, name := range fixtureSourcePaths[:len(fixtureSourcePaths)-1] {
		wantCommon = append(wantCommon, sourceFile{Name: filepath.ToSlash(name), Path: filepath.Join(fixture.repositoryRoot, name)})
	}
	if !slices.Equal(plan.Sources.Common, wantCommon) {
		t.Fatalf("Sources.Common = %#v, want %#v", plan.Sources.Common, wantCommon)
	}
	wantSystemd := sourceFile{
		Name: "deploy/systemd/ai-cli-gateway.service",
		Path: filepath.Join(fixture.repositoryRoot, fixtureSourcePaths[len(fixtureSourcePaths)-1]),
	}
	if plan.Sources.Systemd != wantSystemd {
		t.Fatalf("Sources.Systemd = %#v, want %#v", plan.Sources.Systemd, wantSystemd)
	}
}

func TestNewArchivePlanAcceptsExistingEmptyOutput(t *testing.T) {
	fixture := newReleaseFixture(t)
	mustMkdir(t, fixture.outputRoot)

	if _, err := newArchivePlan(fixture.options); err != nil {
		t.Fatalf("newArchivePlan() error = %v", err)
	}
}

func TestNewArchivePlanCopiesFixedTargets(t *testing.T) {
	fixture := newReleaseFixture(t)
	plan, err := newArchivePlan(fixture.options)
	if err != nil {
		t.Fatalf("newArchivePlan() error = %v", err)
	}

	original := releaseTargets[0]
	releaseTargets[0].GOOS = "changed-after-planning"
	t.Cleanup(func() { releaseTargets[0] = original })

	if plan.Targets[0] != original {
		t.Fatalf("planned target changed through package target storage: got %#v, want %#v", plan.Targets[0], original)
	}
	if plan.Binaries[0].Target != original {
		t.Fatalf("planned binary target changed through package target storage: got %#v, want %#v", plan.Binaries[0].Target, original)
	}
}

func TestNewArchivePlanRejectsInvalidTags(t *testing.T) {
	tags := []string{
		"v0.1",
		"v01.0.0",
		"v0.1.0-rc.1",
		"v0.1.0+build",
		" v0.1.0",
		"v0.1.0 ",
		"v0.1.0;rm",
		"v0.1.0$(id)",
		"v0.1.0|true",
	}
	for _, tag := range tags {
		t.Run(tag, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			fixture.options.Tag = tag
			assertCategory(t, newArchivePlanError(fixture.options), categoryInvalidTag)
		})
	}
}

func TestNewArchivePlanRejectsInvalidSourceTimes(t *testing.T) {
	local := time.FixedZone("fixture-local", 9*60*60)
	times := map[string]time.Time{
		"local time":                time.Date(2026, 8, 4, 6, 29, 53, 0, local),
		"nonzero nanoseconds":       time.Date(2026, 8, 4, 6, 29, 53, 1, time.UTC),
		"before ZIP epoch":          time.Date(1979, 12, 31, 23, 59, 59, 0, time.UTC),
		"after Unix uint32 maximum": time.Unix(int64(math.MaxUint32)+1, 0).UTC(),
	}
	for name, sourceTime := range times {
		t.Run(name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			fixture.options.SourceTime = sourceTime
			assertCategory(t, newArchivePlanError(fixture.options), categoryMissingInput)
		})
	}
}

func TestNewArchivePlanRejectsUnsafeRoots(t *testing.T) {
	t.Run("relative repository root", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		fixture.options.RepositoryRoot = "repository"
		assertCategory(t, newArchivePlanError(fixture.options), categoryUnsafePath)
	})

	t.Run("unclean staging root", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		fixture.options.StagingRoot += string(filepath.Separator) + "."
		assertCategory(t, newArchivePlanError(fixture.options), categoryUnsafePath)
	})

	t.Run("symlink in repository root components", func(t *testing.T) {
		requireSymlink(t)
		fixture := newReleaseFixture(t)
		linkedParent := filepath.Join(t.TempDir(), "linked-parent")
		mustSymlink(t, filepath.Dir(fixture.repositoryRoot), linkedParent)
		fixture.options.RepositoryRoot = filepath.Join(linkedParent, filepath.Base(fixture.repositoryRoot))
		assertCategory(t, newArchivePlanError(fixture.options), categoryUnsafePath)
	})

	t.Run("repository and staging overlap", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		fixture.options.StagingRoot = fixture.repositoryRoot
		assertCategory(t, newArchivePlanError(fixture.options), categoryUnsafePath)
	})

	t.Run("output nested in repository", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		fixture.options.OutputRoot = filepath.Join(fixture.repositoryRoot, "release-output")
		assertCategory(t, newArchivePlanError(fixture.options), categoryUnsafePath)
	})

	t.Run("staging nested in output", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		fixture.options.OutputRoot = filepath.Dir(fixture.stagingRoot)
		assertCategory(t, newArchivePlanError(fixture.options), categoryUnsafePath)
	})
}

func TestNewArchivePlanRejectsInvalidRepositoryInputs(t *testing.T) {
	tests := []struct {
		name     string
		category errorCategory
		mutate   func(*testing.T, *releaseFixture)
	}{
		{
			name:     "wrong module",
			category: categoryMissingInput,
			mutate: func(t *testing.T, fixture *releaseFixture) {
				mustWriteFile(t, filepath.Join(fixture.repositoryRoot, "go.mod"), "module example.com/not-the-release-module\n")
			},
		},
		{
			name:     "missing public source",
			category: categoryMissingInput,
			mutate: func(t *testing.T, fixture *releaseFixture) {
				mustRemove(t, filepath.Join(fixture.repositoryRoot, "README.md"))
			},
		},
		{
			name:     "non-regular public source",
			category: categoryUnsafePath,
			mutate: func(t *testing.T, fixture *releaseFixture) {
				path := filepath.Join(fixture.repositoryRoot, "README.md")
				mustRemove(t, path)
				mustMkdir(t, path)
			},
		},
		{
			name:     "symlinked descendant directory",
			category: categoryUnsafePath,
			mutate: func(t *testing.T, fixture *releaseFixture) {
				requireSymlink(t)
				examples := filepath.Join(fixture.repositoryRoot, "examples")
				realExamples := filepath.Join(fixture.repositoryRoot, "real-examples")
				mustRename(t, examples, realExamples)
				mustSymlink(t, realExamples, examples)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			test.mutate(t, &fixture)
			assertCategory(t, newArchivePlanError(fixture.options), test.category)
		})
	}
}

func TestNewArchivePlanRejectsInvalidStagingInputs(t *testing.T) {
	tests := []struct {
		name     string
		category errorCategory
		mutate   func(*testing.T, *releaseFixture)
	}{
		{
			name:     "missing target",
			category: categoryMissingInput,
			mutate: func(t *testing.T, fixture *releaseFixture) {
				mustRemoveAll(t, filepath.Join(fixture.stagingRoot, "linux_arm64"))
			},
		},
		{
			name:     "extra target",
			category: categoryUnsafePath,
			mutate: func(t *testing.T, fixture *releaseFixture) {
				mustMkdir(t, filepath.Join(fixture.stagingRoot, "freebsd_amd64"))
			},
		},
		{
			name:     "extra file in target directory",
			category: categoryUnsafePath,
			mutate: func(t *testing.T, fixture *releaseFixture) {
				mustWriteFile(t, filepath.Join(fixture.stagingRoot, "linux_amd64", "notes.txt"), "extra")
			},
		},
		{
			name:     "wrong executable suffix",
			category: categoryUnsafePath,
			mutate: func(t *testing.T, fixture *releaseFixture) {
				directory := filepath.Join(fixture.stagingRoot, "linux_amd64")
				mustRename(t, filepath.Join(directory, "ai-cli-gateway"), filepath.Join(directory, "ai-cli-gateway.exe"))
			},
		},
		{
			name:     "symlinked target directory",
			category: categoryUnsafePath,
			mutate: func(t *testing.T, fixture *releaseFixture) {
				requireSymlink(t)
				directory := filepath.Join(fixture.stagingRoot, "darwin_amd64")
				realDirectory := filepath.Join(filepath.Dir(fixture.stagingRoot), "real-darwin-amd64")
				mustRename(t, directory, realDirectory)
				mustSymlink(t, realDirectory, directory)
			},
		},
		{
			name:     "staged executable symlink",
			category: categoryUnsafePath,
			mutate: func(t *testing.T, fixture *releaseFixture) {
				requireSymlink(t)
				directory := filepath.Join(fixture.stagingRoot, "darwin_arm64")
				executable := filepath.Join(directory, "ai-cli-gateway")
				realExecutable := filepath.Join(filepath.Dir(fixture.stagingRoot), "real-ai-cli-gateway")
				mustRename(t, executable, realExecutable)
				mustSymlink(t, realExecutable, executable)
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			test.mutate(t, &fixture)
			assertCategory(t, newArchivePlanError(fixture.options), test.category)
		})
	}
}

func TestNewArchivePlanRejectsUnsafeOutput(t *testing.T) {
	t.Run("nonempty output root", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		mustWriteFile(t, filepath.Join(fixture.outputRoot, "existing"), "do not overwrite")
		assertCategory(t, newArchivePlanError(fixture.options), categoryUnsafePath)
	})

	t.Run("output root symlink", func(t *testing.T) {
		requireSymlink(t)
		fixture := newReleaseFixture(t)
		realOutput := filepath.Join(filepath.Dir(fixture.outputRoot), "real-output")
		mustMkdir(t, realOutput)
		mustSymlink(t, realOutput, fixture.outputRoot)
		assertCategory(t, newArchivePlanError(fixture.options), categoryUnsafePath)
	})

	t.Run("absent output beneath symlinked parent", func(t *testing.T) {
		requireSymlink(t)
		fixture := newReleaseFixture(t)
		realParent := filepath.Join(t.TempDir(), "real-parent")
		mustMkdir(t, realParent)
		linkedParent := filepath.Join(t.TempDir(), "linked-parent")
		mustSymlink(t, realParent, linkedParent)
		fixture.options.OutputRoot = filepath.Join(linkedParent, "absent-output")
		assertCategory(t, newArchivePlanError(fixture.options), categoryUnsafePath)
	})
}

func TestErrorCategory(t *testing.T) {
	if got := ErrorCategory(nil); got != "" {
		t.Fatalf("ErrorCategory(nil) = %q, want empty string", got)
	}

	categories := []errorCategory{
		categoryInvalidTag,
		categoryInvalidUsage,
		categoryUnsafePath,
		categoryMissingInput,
		categoryArchiveFailure,
		categorySBOMFailure,
		categoryChecksumFailure,
		categoryInternalError,
	}
	for _, category := range categories {
		t.Run(string(category), func(t *testing.T) {
			err := newCategorizedError(category)
			if got := ErrorCategory(err); got != string(category) {
				t.Fatalf("ErrorCategory() = %q, want %q", got, category)
			}
			if got := err.Error(); got != string(category) {
				t.Fatalf("Error() = %q, want category only %q", got, category)
			}
		})
	}

	if got := ErrorCategory(errors.New("foreign error containing /private/secret/path")); got != string(categoryInternalError) {
		t.Fatalf("ErrorCategory(foreign error) = %q, want %q", got, categoryInternalError)
	}
}

func newReleaseFixture(t *testing.T) releaseFixture {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks(fixture root): %v", err)
	}
	fixture := releaseFixture{
		repositoryRoot: filepath.Join(base, "repository"),
		stagingRoot:    filepath.Join(base, "staging"),
		outputRoot:     filepath.Join(base, "output"),
	}
	mustMkdir(t, fixture.repositoryRoot)
	mustMkdir(t, fixture.stagingRoot)

	for _, relative := range fixtureSourcePaths {
		contents := "fixture:" + filepath.ToSlash(relative) + "\n"
		if relative == "go.mod" {
			contents = "module " + expectedModule + "\n\ngo 1.26.0\n"
		}
		mustWriteFile(t, filepath.Join(fixture.repositoryRoot, relative), contents)
	}
	for _, releaseTarget := range releaseTargets {
		contents := "binary:" + releaseTarget.Directory + "/" + releaseTarget.Executable + "\n"
		mustWriteFile(t, filepath.Join(fixture.stagingRoot, releaseTarget.Directory, releaseTarget.Executable), contents)
	}

	fixture.options = ArchiveOptions{
		RepositoryRoot: fixture.repositoryRoot,
		StagingRoot:    fixture.stagingRoot,
		OutputRoot:     fixture.outputRoot,
		Tag:            "v0.1.0",
		SourceTime:     time.Date(2026, 8, 4, 6, 29, 53, 0, time.UTC),
	}
	return fixture
}

func newArchivePlanError(options ArchiveOptions) error {
	_, err := newArchivePlan(options)
	return err
}

func assertCategory(t *testing.T, err error, want errorCategory) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want category %q", want)
	}
	if got := ErrorCategory(err); got != string(want) {
		t.Fatalf("ErrorCategory(%v) = %q, want %q", err, got, want)
	}
	if err.Error() != string(want) {
		t.Fatalf("error text = %q, want category only %q", err.Error(), want)
	}
}

func mustWriteFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("WriteFile(%q): %v", path, err)
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatalf("MkdirAll(%q): %v", path, err)
	}
}

func mustRemove(t *testing.T, path string) {
	t.Helper()
	if err := os.Remove(path); err != nil {
		t.Fatalf("Remove(%q): %v", path, err)
	}
}

func mustRemoveAll(t *testing.T, path string) {
	t.Helper()
	if err := os.RemoveAll(path); err != nil {
		t.Fatalf("RemoveAll(%q): %v", path, err)
	}
}

func mustRename(t *testing.T, oldPath, newPath string) {
	t.Helper()
	if err := os.Rename(oldPath, newPath); err != nil {
		t.Fatalf("Rename(%q, %q): %v", oldPath, newPath, err)
	}
}

func mustSymlink(t *testing.T, oldPath, newPath string) {
	t.Helper()
	if err := os.Symlink(oldPath, newPath); err != nil {
		t.Fatalf("Symlink(%q, %q): %v", oldPath, newPath, err)
	}
}

func requireSymlink(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fixture symlinks require privileges not guaranteed on Windows")
	}
}
