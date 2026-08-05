//go:build linux || darwin

package releasepack

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestWriteArchivesCreatesDeterministicSafeArchives(t *testing.T) {
	fixture := newReleaseFixture(t)
	secondOutput := filepath.Join(filepath.Dir(fixture.outputRoot), "output-second")
	secondOptions := fixture.options
	secondOptions.OutputRoot = secondOutput

	first, err := WriteArchives(fixture.options)
	if err != nil {
		t.Fatalf("WriteArchives(first) error = %v", err)
	}
	second, err := WriteArchives(secondOptions)
	if err != nil {
		t.Fatalf("WriteArchives(second) error = %v", err)
	}

	wantNames := []string{
		"ai-cli-gateway_0.1.0_linux_amd64.tar.gz",
		"ai-cli-gateway_0.1.0_linux_arm64.tar.gz",
		"ai-cli-gateway_0.1.0_darwin_amd64.tar.gz",
		"ai-cli-gateway_0.1.0_darwin_arm64.tar.gz",
		"ai-cli-gateway_0.1.0_windows_amd64.zip",
	}
	if got := assetNames(first); !slices.Equal(got, wantNames) {
		t.Fatalf("asset names = %q, want %q", got, wantNames)
	}
	if got := assetNames(second); !slices.Equal(got, wantNames) {
		t.Fatalf("second asset names = %q, want %q", got, wantNames)
	}
	for _, outputRoot := range []string{fixture.outputRoot, secondOutput} {
		if mode := mustStat(t, outputRoot).Mode().Perm(); mode != 0o700 {
			t.Errorf("created output root %q mode = %04o, want 0700", outputRoot, mode)
		}
	}

	for i, asset := range first {
		if asset.Path != filepath.Join(fixture.outputRoot, asset.Name) {
			t.Errorf("asset[%d].Path = %q, want fixed output path", i, asset.Path)
		}
		if second[i].Path != filepath.Join(secondOutput, second[i].Name) {
			t.Errorf("second asset[%d].Path = %q, want fixed output path", i, second[i].Path)
		}
		firstBytes := mustReadFile(t, asset.Path)
		secondBytes := mustReadFile(t, second[i].Path)
		if !slices.Equal(firstBytes, secondBytes) {
			t.Errorf("archive %q differs across output roots", asset.Name)
		}
		if info := mustStat(t, asset.Path); info.Mode().Perm() != 0o644 {
			t.Errorf("archive %q mode = %04o, want 0644", asset.Name, info.Mode().Perm())
		}

		base, format := archiveBaseAndFormat(t, asset.Name)
		includeSystemd := strings.Contains(asset.Name, "_linux_")
		if format == formatTarGzip {
			inspectTarGzip(t, firstBytes, base, includeSystemd, fixture.options.SourceTime)
		} else {
			inspectZIP(t, firstBytes, base, fixture.options.SourceTime)
		}
	}
}

func TestWriteArchivesReturnsArchiveFailureAndCleansOwnedPaths(t *testing.T) {
	tests := []struct {
		name string
		fail func(*testing.T, *releaseFixture, *archiveFileHooks)
	}{
		{
			name: "create",
			fail: func(_ *testing.T, _ *releaseFixture, hooks *archiveFileHooks) {
				hooks.createTemp = func(string, string) (*os.File, error) {
					return nil, errors.New("create failed at /private/create")
				}
			},
		},
		{
			name: "copy",
			fail: func(_ *testing.T, _ *releaseFixture, hooks *archiveFileHooks) {
				hooks.copyN = func(io.Writer, io.Reader, int64) (int64, error) {
					return 0, errors.New("copy failed at /private/copy")
				}
			},
		},
		{
			name: "close",
			fail: func(_ *testing.T, _ *releaseFixture, hooks *archiveFileHooks) {
				hooks.closeFile = func(file *os.File) error {
					_ = file.Close()
					return errors.New("close failed at /private/close")
				}
			},
		},
		{
			name: "chmod",
			fail: func(_ *testing.T, _ *releaseFixture, hooks *archiveFileHooks) {
				hooks.chmod = func(*os.File, fs.FileMode) error {
					return errors.New("chmod failed at /private/chmod")
				}
			},
		},
		{
			name: "link publication",
			fail: func(_ *testing.T, _ *releaseFixture, hooks *archiveFileHooks) {
				calls := 0
				hooks.link = func(oldPath, newPath string) error {
					calls++
					if calls == 1 {
						return os.Link(oldPath, newPath)
					}
					return errors.New("link failed at /private/link")
				}
			},
		},
		{
			name: "temporary unlink",
			fail: func(_ *testing.T, _ *releaseFixture, hooks *archiveFileHooks) {
				hooks.removeTemp = func(string) error {
					return errors.New("unlink failed at /private/unlink")
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			hooks := defaultArchiveFileHooks()
			test.fail(t, &fixture, &hooks)
			withArchiveFileHooks(t, hooks)

			assets, err := WriteArchives(fixture.options)
			if len(assets) != 0 {
				t.Fatalf("assets = %#v, want none", assets)
			}
			assertCategory(t, err, categoryArchiveFailure)
			assertOutputAbsentOrEmpty(t, fixture.outputRoot)
		})
	}
}

func TestWriteArchivesDoesNotClobberOrCleanAttackerFinal(t *testing.T) {
	fixture := newReleaseFixture(t)
	hooks := defaultArchiveFileHooks()
	const attackerContents = "attacker-owned\n"
	hooks.link = func(oldPath, newPath string) error {
		if err := os.WriteFile(newPath, []byte(attackerContents), 0o600); err != nil {
			t.Fatalf("inject final %q: %v", newPath, err)
		}
		return os.Link(oldPath, newPath)
	}
	withArchiveFileHooks(t, hooks)

	assets, err := WriteArchives(fixture.options)
	if len(assets) != 0 {
		t.Fatalf("assets = %#v, want none", assets)
	}
	assertCategory(t, err, categoryArchiveFailure)

	entries, readErr := os.ReadDir(fixture.outputRoot)
	if readErr != nil {
		t.Fatalf("ReadDir(output): %v", readErr)
	}
	if len(entries) != 1 {
		t.Fatalf("output entries = %v, want attacker final only", entries)
	}
	if got := string(mustReadFile(t, filepath.Join(fixture.outputRoot, entries[0].Name()))); got != attackerContents {
		t.Fatalf("attacker final contents = %q, want %q", got, attackerContents)
	}
}

func TestWriteArchivesPreservesFinalReplacedAfterLink(t *testing.T) {
	fixture := newReleaseFixture(t)
	hooks := defaultArchiveFileHooks()
	const attackerContents = "post-link attacker\n"
	var finalPath string
	hooks.link = func(oldPath, newPath string) error {
		if err := os.Link(oldPath, newPath); err != nil {
			return err
		}
		if err := os.Remove(newPath); err != nil {
			return err
		}
		finalPath = newPath
		return os.WriteFile(newPath, []byte(attackerContents), 0o600)
	}
	withArchiveFileHooks(t, hooks)

	assets, err := WriteArchives(fixture.options)
	if len(assets) != 0 {
		t.Fatalf("assets = %#v, want none", assets)
	}
	assertCategory(t, err, categoryArchiveFailure)
	if got := string(mustReadFile(t, finalPath)); got != attackerContents {
		t.Fatalf("post-link replacement contents = %q, want preserved %q", got, attackerContents)
	}
}

func TestWriteArchivesAcceptsExistingPrivateOutputRoot(t *testing.T) {
	fixture := newReleaseFixture(t)
	mustMkdir(t, fixture.outputRoot)
	if err := os.Chmod(fixture.outputRoot, 0o700); err != nil {
		t.Fatalf("Chmod(output, 0700): %v", err)
	}

	assets, err := WriteArchives(fixture.options)
	if err != nil {
		t.Fatalf("WriteArchives() error = %v", err)
	}
	if len(assets) != len(releaseTargets) {
		t.Fatalf("len(assets) = %d, want %d", len(assets), len(releaseTargets))
	}
}

func TestWriteArchivesRejectsExistingOutputRootWithoutMode0700(t *testing.T) {
	fixture := newReleaseFixture(t)
	mustMkdir(t, fixture.outputRoot)
	if err := os.Chmod(fixture.outputRoot, 0o755); err != nil {
		t.Fatalf("Chmod(output, 0755): %v", err)
	}

	assets, err := WriteArchives(fixture.options)
	if len(assets) != 0 {
		t.Fatalf("assets = %#v, want none", assets)
	}
	assertCategory(t, err, categoryUnsafePath)
	if entries := mustReadDir(t, fixture.outputRoot); len(entries) != 0 {
		t.Fatalf("output entries = %v, want empty", entries)
	}
}

func TestWriteArchivesRejectsWritableNonStickyOutputAncestor(t *testing.T) {
	fixture := newReleaseFixture(t)
	unsafeAncestor := filepath.Join(filepath.Dir(fixture.outputRoot), "unsafe-ancestor")
	mustMkdir(t, unsafeAncestor)
	if err := os.Chmod(unsafeAncestor, 0o777); err != nil {
		t.Fatalf("Chmod(unsafe ancestor, 0777): %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unsafeAncestor, 0o700) })
	fixture.options.OutputRoot = filepath.Join(unsafeAncestor, "output")

	assets, err := WriteArchives(fixture.options)
	if len(assets) != 0 {
		t.Fatalf("assets = %#v, want none", assets)
	}
	assertCategory(t, err, categoryUnsafePath)
	if _, statErr := os.Lstat(fixture.options.OutputRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("unsafe output root stat error = %v, want not exist", statErr)
	}
}

func TestWriteArchivesRejectsForeignOwnedOutputRoot(t *testing.T) {
	fixture := newReleaseFixture(t)
	mustMkdir(t, fixture.outputRoot)
	if err := os.Chmod(fixture.outputRoot, 0o700); err != nil {
		t.Fatalf("Chmod(output, 0700): %v", err)
	}
	original := releasepackEffectiveUID
	releasepackEffectiveUID = func() int { return os.Geteuid() + 1 }
	t.Cleanup(func() { releasepackEffectiveUID = original })

	assets, err := WriteArchives(fixture.options)
	if len(assets) != 0 {
		t.Fatalf("assets = %#v, want none", assets)
	}
	assertCategory(t, err, categoryUnsafePath)
	if entries := mustReadDir(t, fixture.outputRoot); len(entries) != 0 {
		t.Fatalf("output entries = %v, want empty", entries)
	}
}

func TestWriteArchivesCreatesMode0700OutputDespiteRestrictiveUmask(t *testing.T) {
	fixture := newReleaseFixture(t)
	hooks := defaultArchiveFileHooks()
	descriptorChmodCalls := 0
	realDescriptorChmod := hooks.chmodRoot
	hooks.chmodRoot = func(file *os.File, mode fs.FileMode) error {
		descriptorChmodCalls++
		if mode != 0o700 {
			t.Fatalf("output-root descriptor chmod mode = %04o, want 0700", mode)
		}
		return realDescriptorChmod(file, mode)
	}
	withArchiveFileHooks(t, hooks)
	oldUmask := syscall.Umask(0o777)
	restored := false
	t.Cleanup(func() {
		if !restored {
			syscall.Umask(oldUmask)
		}
	})

	assets, err := WriteArchives(fixture.options)
	syscall.Umask(oldUmask)
	restored = true

	if err != nil {
		t.Fatalf("WriteArchives() error = %v", err)
	}
	if len(assets) != len(releaseTargets) {
		t.Fatalf("len(assets) = %d, want %d", len(assets), len(releaseTargets))
	}
	if descriptorChmodCalls != 1 {
		t.Fatalf("output-root descriptor chmod calls = %d, want 1", descriptorChmodCalls)
	}
	if mode := mustStat(t, fixture.outputRoot).Mode(); mode.Perm() != 0o700 || mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		t.Fatalf("created output mode = %v, want exact 0700", mode)
	}
}

func TestWriteArchivesTarAndZIPPayloadsEqualFixtureSources(t *testing.T) {
	fixture := newReleaseFixture(t)
	assertPackagedFixturePayloadsAreDistinct(t, fixture)
	assets, err := WriteArchives(fixture.options)
	if err != nil {
		t.Fatalf("WriteArchives() error = %v", err)
	}

	for _, asset := range assets {
		data := mustReadFile(t, asset.Path)
		base, format := archiveBaseAndFormat(t, asset.Name)
		if format == formatTarGzip {
			gzipReader, err := gzip.NewReader(strings.NewReader(string(data)))
			if err != nil {
				t.Fatalf("gzip.NewReader(%q): %v", asset.Name, err)
			}
			tarReader := tar.NewReader(gzipReader)
			for {
				header, err := tarReader.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("tar.Next(%q): %v", asset.Name, err)
				}
				if header.Typeflag != tar.TypeReg {
					continue
				}
				got, err := io.ReadAll(tarReader)
				if err != nil {
					t.Fatalf("ReadAll(%q): %v", header.Name, err)
				}
				assertFixturePayload(t, fixture, asset.Name, base, header.Name, got)
			}
			if err := gzipReader.Close(); err != nil {
				t.Fatalf("Close(gzip %q): %v", asset.Name, err)
			}
			continue
		}

		zipReader, err := zip.NewReader(strings.NewReader(string(data)), int64(len(data)))
		if err != nil {
			t.Fatalf("zip.NewReader(%q): %v", asset.Name, err)
		}
		for _, file := range zipReader.File {
			if file.FileInfo().IsDir() {
				continue
			}
			opened, err := file.Open()
			if err != nil {
				t.Fatalf("Open(%q): %v", file.Name, err)
			}
			got, readErr := io.ReadAll(opened)
			closeErr := opened.Close()
			if readErr != nil || closeErr != nil {
				t.Fatalf("ReadAll(%q): read=%v close=%v", file.Name, readErr, closeErr)
			}
			assertFixturePayload(t, fixture, asset.Name, base, file.Name, got)
		}
	}
}

func assertPackagedFixturePayloadsAreDistinct(t *testing.T, fixture releaseFixture) {
	t.Helper()
	seen := make(map[string]string)
	for _, relative := range fixtureSourcePaths[1:] {
		name := filepath.Join(fixture.repositoryRoot, relative)
		contents := string(mustReadFile(t, name))
		if previous, duplicate := seen[contents]; duplicate {
			t.Fatalf("fixture payloads %q and %q are identical; correspondence test requires distinct bytes", previous, name)
		}
		seen[contents] = name
	}
	for _, releaseTarget := range releaseTargets {
		name := filepath.Join(fixture.stagingRoot, releaseTarget.Directory, releaseTarget.Executable)
		contents := string(mustReadFile(t, name))
		if previous, duplicate := seen[contents]; duplicate {
			t.Fatalf("fixture payloads %q and %q are identical; correspondence test requires distinct bytes", previous, name)
		}
		seen[contents] = name
	}
}

func assertFixturePayload(t *testing.T, fixture releaseFixture, assetName, base, entryName string, got []byte) {
	t.Helper()
	type fixtureTarget struct {
		directory  string
		executable string
	}
	targets := map[string]fixtureTarget{
		"ai-cli-gateway_0.1.0_linux_amd64.tar.gz":  {directory: "linux_amd64", executable: "ai-cli-gateway"},
		"ai-cli-gateway_0.1.0_linux_arm64.tar.gz":  {directory: "linux_arm64", executable: "ai-cli-gateway"},
		"ai-cli-gateway_0.1.0_darwin_amd64.tar.gz": {directory: "darwin_amd64", executable: "ai-cli-gateway"},
		"ai-cli-gateway_0.1.0_darwin_arm64.tar.gz": {directory: "darwin_arm64", executable: "ai-cli-gateway"},
		"ai-cli-gateway_0.1.0_windows_amd64.zip":   {directory: "windows_amd64", executable: "ai-cli-gateway.exe"},
	}
	releaseTarget, ok := targets[assetName]
	if !ok {
		t.Fatalf("unknown fixture asset %q", assetName)
	}
	relative := strings.TrimPrefix(entryName, base+"/")
	sourcePath := filepath.Join(fixture.repositoryRoot, filepath.FromSlash(relative))
	if relative == releaseTarget.executable {
		sourcePath = filepath.Join(fixture.stagingRoot, releaseTarget.directory, releaseTarget.executable)
	}
	want := mustReadFile(t, sourcePath)
	if !slices.Equal(got, want) {
		t.Fatalf("archive payload %q = %q, want fixture source %q bytes %q", entryName, got, sourcePath, want)
	}
}

func TestWriteArchivesCleansOnlyTrackedPaths(t *testing.T) {
	fixture := newReleaseFixture(t)
	hooks := defaultArchiveFileHooks()
	const sentinel = "attacker-sentinel"
	var once sync.Once
	hooks.copyN = func(io.Writer, io.Reader, int64) (int64, error) {
		once.Do(func() {
			if err := os.WriteFile(filepath.Join(fixture.outputRoot, sentinel), []byte("keep\n"), 0o600); err != nil {
				t.Fatalf("inject sentinel: %v", err)
			}
		})
		return 0, errors.New("copy failed")
	}
	withArchiveFileHooks(t, hooks)

	_, err := WriteArchives(fixture.options)
	assertCategory(t, err, categoryArchiveFailure)
	entries, readErr := os.ReadDir(fixture.outputRoot)
	if readErr != nil {
		t.Fatalf("ReadDir(output): %v", readErr)
	}
	if len(entries) != 1 || entries[0].Name() != sentinel {
		t.Fatalf("output entries = %v, want only %q", entries, sentinel)
	}
}

func TestWriteArchivesRetriesOneShotCleanupUnlinkFailure(t *testing.T) {
	fixture := newReleaseFixture(t)
	hooks := defaultArchiveFileHooks()
	hooks.copyN = func(io.Writer, io.Reader, int64) (int64, error) {
		return 0, errors.New("force rollback")
	}
	removeCalls := 0
	hooks.removeOwned = func(name string) error {
		removeCalls++
		if removeCalls == 1 {
			return errors.New("one-shot cleanup unlink failure")
		}
		return os.Remove(name)
	}
	withArchiveFileHooks(t, hooks)

	assets, err := WriteArchives(fixture.options)
	if len(assets) != 0 {
		t.Fatalf("assets = %#v, want none", assets)
	}
	assertCategory(t, err, categoryArchiveFailure)
	if removeCalls < 2 {
		t.Fatalf("cleanup unlink calls = %d, want retry", removeCalls)
	}
	assertOutputAbsentOrEmpty(t, fixture.outputRoot)
}

func TestWriteArchivesPersistentCleanupUnlinkFailureTaintsRoot(t *testing.T) {
	fixture := newReleaseFixture(t)
	hooks := defaultArchiveFileHooks()
	hooks.copyN = func(io.Writer, io.Reader, int64) (int64, error) {
		return 0, errors.New("force rollback")
	}
	hooks.removeOwned = func(string) error {
		return errors.New("persistent cleanup unlink failure")
	}
	withArchiveFileHooks(t, hooks)

	assets, err := WriteArchives(fixture.options)
	if len(assets) != 0 {
		t.Fatalf("assets = %#v, want none", assets)
	}
	assertCategory(t, err, categoryArchiveFailure)
	entries := mustReadDir(t, fixture.outputRoot)
	if len(entries) != 1 || !strings.HasPrefix(entries[0].Name(), ".release-archive-") {
		t.Fatalf("tainted output entries = %v, want only refused owned temporary", entries)
	}
}

func TestWriteArchivesRetriesOneShotOutputRootRemovalFailure(t *testing.T) {
	fixture := newReleaseFixture(t)
	hooks := defaultArchiveFileHooks()
	hooks.createTemp = func(string, string) (*os.File, error) {
		return nil, errors.New("force rollback")
	}
	removeCalls := 0
	hooks.removeRoot = func(name string) error {
		removeCalls++
		if removeCalls == 1 {
			return errors.New("one-shot output root removal failure")
		}
		return os.Remove(name)
	}
	withArchiveFileHooks(t, hooks)

	assets, err := WriteArchives(fixture.options)
	if len(assets) != 0 {
		t.Fatalf("assets = %#v, want none", assets)
	}
	assertCategory(t, err, categoryArchiveFailure)
	if removeCalls < 2 {
		t.Fatalf("output-root removal calls = %d, want retry", removeCalls)
	}
	if _, statErr := os.Lstat(fixture.outputRoot); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("output root stat error = %v, want not exist", statErr)
	}
}

func TestWriteArchivesPersistentOutputRootRemovalFailureTaintsRoot(t *testing.T) {
	fixture := newReleaseFixture(t)
	hooks := defaultArchiveFileHooks()
	hooks.createTemp = func(string, string) (*os.File, error) {
		return nil, errors.New("force rollback")
	}
	hooks.removeRoot = func(string) error {
		return errors.New("persistent output root removal failure")
	}
	withArchiveFileHooks(t, hooks)

	assets, err := WriteArchives(fixture.options)
	if len(assets) != 0 {
		t.Fatalf("assets = %#v, want none", assets)
	}
	assertCategory(t, err, categoryArchiveFailure)
	if entries := mustReadDir(t, fixture.outputRoot); len(entries) != 0 {
		t.Fatalf("tainted output entries = %v, want empty retained root", entries)
	}
}

func TestWriteArchivesDoesNotCleanAReplacedTrackedName(t *testing.T) {
	fixture := newReleaseFixture(t)
	hooks := defaultArchiveFileHooks()
	const attackerContents = "attacker-temp\n"
	var temporaryPath string
	hooks.link = func(oldPath, _ string) error {
		temporaryPath = oldPath
		if err := os.Rename(oldPath, oldPath+".moved"); err != nil {
			t.Fatalf("move owned temporary: %v", err)
		}
		if err := os.WriteFile(oldPath, []byte(attackerContents), 0o600); err != nil {
			t.Fatalf("replace temporary name: %v", err)
		}
		return errors.New("link failed after replacement")
	}
	withArchiveFileHooks(t, hooks)

	_, err := WriteArchives(fixture.options)
	assertCategory(t, err, categoryArchiveFailure)
	if got := string(mustReadFile(t, temporaryPath)); got != attackerContents {
		t.Fatalf("replacement contents = %q, want preserved %q", got, attackerContents)
	}
}

func TestWriteArchivesRejectsTempReplacementBeforeLinkPublication(t *testing.T) {
	fixture := newReleaseFixture(t)
	hooks := defaultArchiveFileHooks()
	const attackerContents = "attacker-archive\n"
	var temporaryPath string
	var once sync.Once
	hooks.link = func(oldPath, newPath string) error {
		var injectErr error
		once.Do(func() {
			temporaryPath = oldPath
			if err := os.Rename(oldPath, oldPath+".moved"); err != nil {
				injectErr = err
				return
			}
			injectErr = os.WriteFile(oldPath, []byte(attackerContents), 0o600)
		})
		if injectErr != nil {
			return injectErr
		}
		return os.Link(oldPath, newPath)
	}
	withArchiveFileHooks(t, hooks)

	assets, err := WriteArchives(fixture.options)
	if len(assets) != 0 {
		t.Fatalf("assets = %#v, want none", assets)
	}
	assertCategory(t, err, categoryArchiveFailure)
	if got := string(mustReadFile(t, temporaryPath)); got != attackerContents {
		t.Fatalf("replacement contents = %q, want preserved %q", got, attackerContents)
	}
	finalPath := filepath.Join(fixture.outputRoot, "ai-cli-gateway_0.1.0_linux_amd64.tar.gz")
	if got := string(mustReadFile(t, finalPath)); got != attackerContents {
		t.Fatalf("unexpected published final contents = %q, want preserved %q", got, attackerContents)
	}
}

func TestWriteArchivesRejectsChangedValidatedFiles(t *testing.T) {
	t.Run("replacement", func(t *testing.T) {
		fixture := newReleaseFixture(t)
		hooks := defaultArchiveFileHooks()
		var once sync.Once
		hooks.openFile = func(name string) (*os.File, error) {
			var replaceErr error
			once.Do(func() {
				oldName := name + ".replaced"
				if err := os.Rename(name, oldName); err != nil {
					replaceErr = err
					return
				}
				replaceErr = os.WriteFile(name, []byte("replacement\n"), 0o600)
			})
			if replaceErr != nil {
				return nil, replaceErr
			}
			return os.Open(name)
		}
		withArchiveFileHooks(t, hooks)

		_, err := WriteArchives(fixture.options)
		assertCategory(t, err, categoryArchiveFailure)
		assertOutputAbsentOrEmpty(t, fixture.outputRoot)
	})

	for _, change := range []string{"growth", "shrink"} {
		t.Run(change, func(t *testing.T) {
			fixture := newReleaseFixture(t)
			hooks := defaultArchiveFileHooks()
			var once sync.Once
			hooks.copyN = func(dst io.Writer, src io.Reader, size int64) (int64, error) {
				copied, err := io.CopyN(dst, src, size)
				if err != nil {
					return copied, err
				}
				var changeErr error
				once.Do(func() {
					file, ok := src.(*os.File)
					if !ok {
						changeErr = errors.New("source is not an os.File")
						return
					}
					if change == "growth" {
						var appendFile *os.File
						appendFile, changeErr = os.OpenFile(file.Name(), os.O_APPEND|os.O_WRONLY, 0)
						if changeErr == nil {
							_, changeErr = appendFile.Write([]byte("growth"))
							_ = appendFile.Close()
						}
					} else {
						changeErr = os.Truncate(file.Name(), size-1)
					}
				})
				return copied, changeErr
			}
			withArchiveFileHooks(t, hooks)

			_, err := WriteArchives(fixture.options)
			assertCategory(t, err, categoryArchiveFailure)
			assertOutputAbsentOrEmpty(t, fixture.outputRoot)
		})
	}
}

func inspectTarGzip(t *testing.T, data []byte, base string, includeSystemd bool, sourceTime time.Time) {
	t.Helper()
	reader, err := gzip.NewReader(strings.NewReader(string(data)))
	if err != nil {
		t.Fatalf("gzip.NewReader(%q): %v", base, err)
	}
	defer reader.Close()
	if reader.Name != "" || reader.Comment != "" || len(reader.Extra) != 0 {
		t.Errorf("gzip metadata for %q = name %q comment %q extra %x, want empty", base, reader.Name, reader.Comment, reader.Extra)
	}
	if reader.OS != 255 {
		t.Errorf("gzip OS for %q = %d, want 255", base, reader.OS)
	}
	if !reader.ModTime.Equal(sourceTime) {
		t.Errorf("gzip modtime for %q = %v, want exact instant %v", base, reader.ModTime, sourceTime)
	}

	want := expectedArchiveEntries(base, includeSystemd, false)
	got := make([]string, 0, len(want))
	seen := make(map[string]struct{}, len(want))
	tarReader := tar.NewReader(reader)
	for {
		header, nextErr := tarReader.Next()
		if errors.Is(nextErr, io.EOF) {
			break
		}
		if nextErr != nil {
			t.Fatalf("tar.Next(%q): %v", base, nextErr)
		}
		assertSafeArchiveName(t, header.Name, seen)
		got = append(got, header.Name)
		expected, ok := want[header.Name]
		if !ok {
			t.Errorf("unexpected tar entry %q", header.Name)
			continue
		}
		if header.Format != tar.FormatUSTAR {
			t.Errorf("tar entry %q format = %v, want USTAR", header.Name, header.Format)
		}
		if header.Mode != int64(expected.mode.Perm()) {
			t.Errorf("tar entry %q mode = %04o, want %04o", header.Name, header.Mode, expected.mode.Perm())
		}
		if header.Uid != 0 || header.Gid != 0 || header.Uname != "" || header.Gname != "" || header.Linkname != "" {
			t.Errorf("tar entry %q has nonempty identity/link metadata: %#v", header.Name, header)
		}
		if !header.ModTime.Equal(sourceTime) {
			t.Errorf("tar entry %q modtime = %v, want exact instant %v", header.Name, header.ModTime, sourceTime)
		}
		if expected.directory {
			if header.Typeflag != tar.TypeDir || !strings.HasSuffix(header.Name, "/") || header.Size != 0 {
				t.Errorf("tar directory %q type/size = %d/%d, want directory/zero", header.Name, header.Typeflag, header.Size)
			}
		} else {
			if header.Typeflag != tar.TypeReg || header.Size < 0 {
				t.Errorf("tar file %q type/size = %d/%d, want regular/nonnegative", header.Name, header.Typeflag, header.Size)
			}
		}
		if _, err := io.Copy(io.Discard, tarReader); err != nil {
			t.Fatalf("read tar entry %q: %v", header.Name, err)
		}
	}
	assertArchiveOrder(t, got, want)
}

func inspectZIP(t *testing.T, data []byte, base string, sourceTime time.Time) {
	t.Helper()
	reader, err := zip.NewReader(strings.NewReader(string(data)), int64(len(data)))
	if err != nil {
		t.Fatalf("zip.NewReader(%q): %v", base, err)
	}
	if reader.Comment != "" {
		t.Errorf("ZIP archive comment for %q = %q, want empty", base, reader.Comment)
	}
	want := expectedArchiveEntries(base, false, true)
	got := make([]string, 0, len(reader.File))
	seen := make(map[string]struct{}, len(reader.File))
	for _, file := range reader.File {
		assertSafeArchiveName(t, file.Name, seen)
		got = append(got, file.Name)
		expected, ok := want[file.Name]
		if !ok {
			t.Errorf("unexpected ZIP entry %q", file.Name)
			continue
		}
		if file.Comment != "" {
			t.Errorf("ZIP entry %q comment = %q, want empty", file.Name, file.Comment)
		}
		if !file.Modified.Equal(sourceTime) {
			t.Errorf("ZIP entry %q modtime = %v, want exact instant %v", file.Name, file.Modified, sourceTime)
		}
		if file.Mode().Perm() != expected.mode.Perm() || file.FileInfo().IsDir() != expected.directory {
			t.Errorf("ZIP entry %q mode/dir = %v/%v, want %v/%v", file.Name, file.Mode(), file.FileInfo().IsDir(), expected.mode, expected.directory)
		}
		wantType := fs.FileMode(0)
		if expected.directory {
			wantType = fs.ModeDir
		}
		if gotType := file.Mode().Type(); gotType != wantType {
			t.Errorf("ZIP entry %q type = %v, want %v (no symlink or device)", file.Name, gotType, wantType)
		}
		if expected.directory && file.Method != zip.Store {
			t.Errorf("ZIP directory %q method = %d, want Store", file.Name, file.Method)
		}
		if !expected.directory && file.Method != zip.Deflate {
			t.Errorf("ZIP file %q method = %d, want Deflate", file.Name, file.Method)
		}
		opened, openErr := file.Open()
		if openErr != nil {
			t.Fatalf("open ZIP entry %q: %v", file.Name, openErr)
		}
		_, copyErr := io.Copy(io.Discard, opened)
		closeErr := opened.Close()
		if copyErr != nil || closeErr != nil {
			t.Fatalf("read ZIP entry %q: copy=%v close=%v", file.Name, copyErr, closeErr)
		}
	}
	assertArchiveOrder(t, got, want)
}

type expectedArchiveEntry struct {
	mode      fs.FileMode
	directory bool
}

func expectedArchiveEntries(base string, includeSystemd, windows bool) map[string]expectedArchiveEntry {
	files := []string{
		"README.md",
		"LICENSE",
		"THIRD_PARTY_NOTICES.md",
		"config.example.toml",
		"examples/config/codex.example.toml",
		"examples/openai-sdk/python/main.py",
		"examples/openai-sdk/python/requirements.txt",
		"examples/openai-sdk/python/requirements.lock",
		"examples/openai-sdk/javascript/main.mjs",
		"examples/openai-sdk/javascript/package.json",
		"examples/openai-sdk/javascript/package-lock.json",
	}
	executable := "ai-cli-gateway"
	executableMode := fs.FileMode(0o755)
	if windows {
		executable = "ai-cli-gateway.exe"
		executableMode = 0o644
	}
	files = append(files, executable)
	if includeSystemd {
		files = append(files, "deploy/systemd/ai-cli-gateway.service")
	}

	want := map[string]expectedArchiveEntry{
		base + "/": {mode: 0o755, directory: true},
	}
	for _, name := range files {
		mode := fs.FileMode(0o644)
		if name == executable {
			mode = executableMode
		}
		want[path.Join(base, name)] = expectedArchiveEntry{mode: mode}
		for parent := path.Dir(name); parent != "."; parent = path.Dir(parent) {
			want[path.Join(base, parent)+"/"] = expectedArchiveEntry{mode: 0o755, directory: true}
		}
	}
	return want
}

func assertSafeArchiveName(t *testing.T, name string, seen map[string]struct{}) {
	t.Helper()
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, "\\") {
		t.Errorf("unsafe archive name %q", name)
	}
	for _, component := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if component == "" || component == "." || component == ".." {
			t.Errorf("unsafe archive component in %q", name)
		}
	}
	if _, duplicate := seen[name]; duplicate {
		t.Errorf("duplicate archive entry %q", name)
	}
	seen[name] = struct{}{}
}

func assertArchiveOrder(t *testing.T, got []string, want map[string]expectedArchiveEntry) {
	t.Helper()
	wantNames := make([]string, 0, len(want))
	for name := range want {
		wantNames = append(wantNames, name)
	}
	slices.Sort(wantNames)
	if !slices.Equal(got, wantNames) {
		t.Errorf("archive entries = %q, want lexical exact entries %q", got, wantNames)
	}
}

func archiveBaseAndFormat(t *testing.T, name string) (string, archiveFormat) {
	t.Helper()
	if strings.HasSuffix(name, ".tar.gz") {
		return strings.TrimSuffix(name, ".tar.gz"), formatTarGzip
	}
	if strings.HasSuffix(name, ".zip") {
		return strings.TrimSuffix(name, ".zip"), formatZIP
	}
	t.Fatalf("unsupported fixture archive name %q", name)
	return "", 0
}

func assetNames(assets []Asset) []string {
	names := make([]string, len(assets))
	for i, asset := range assets {
		names[i] = asset.Name
	}
	return names
}

func withArchiveFileHooks(t *testing.T, hooks archiveFileHooks) {
	t.Helper()
	original := archiveFiles
	archiveFiles = hooks
	t.Cleanup(func() { archiveFiles = original })
}

func assertOutputAbsentOrEmpty(t *testing.T, outputRoot string) {
	t.Helper()
	entries, err := os.ReadDir(outputRoot)
	if errors.Is(err, os.ErrNotExist) {
		return
	}
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", outputRoot, err)
	}
	if len(entries) != 0 {
		t.Fatalf("output entries after failure = %v, want empty", entries)
	}
}

func mustReadFile(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("ReadFile(%q): %v", name, err)
	}
	return data
}

func mustStat(t *testing.T, name string) os.FileInfo {
	t.Helper()
	info, err := os.Stat(name)
	if err != nil {
		t.Fatalf("Stat(%q): %v", name, err)
	}
	return info
}

func mustReadDir(t *testing.T, name string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(name)
	if err != nil {
		t.Fatalf("ReadDir(%q): %v", name, err)
	}
	return entries
}
