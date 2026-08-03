//go:build !windows

package process

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestClassifyUnixLockErrorMapsOnlyContention(t *testing.T) {
	for _, contention := range []error{unix.EWOULDBLOCK, unix.EAGAIN} {
		if got := classifyLockError(contention); !errors.Is(got, ErrRootLocked) {
			t.Fatalf("classifyLockError(%v)=%v, want ErrRootLocked", contention, got)
		}
	}

	other := errors.New("private non-contention lock failure")
	if got := classifyLockError(other); !errors.Is(got, other) ||
		errors.Is(got, ErrRootLocked) {
		t.Fatalf("classifyLockError(other)=%v", got)
	}
	if got := classifyLockError(nil); got != nil {
		t.Fatalf("classifyLockError(nil)=%v", got)
	}
}

func TestUnixPlatformBaseFilenameRules(t *testing.T) {
	for _, name := range []string{
		"input.json",
		".hidden",
		"file:stream",
		"CON.txt",
		"trailing.",
		"trailing ",
	} {
		if !validPlatformFileName(name) {
			t.Errorf("valid POSIX base filename rejected: %q", name)
		}
	}
}

func TestOpenRootCreatesExactUnixMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	root, err := OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode(); got != os.ModeDir|0o700 {
		t.Fatalf("created root mode=%v", got)
	}
}

func TestOpenRootRejectsNonStickyAttackerWritableParent(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "writable")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	// The deliberately unsafe mode is the behavior under test.
	//nolint:gosec
	if err := os.Chmod(parent, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Restore the test-owned directory so temporary cleanup can remove it.
		//nolint:gosec
		_ = os.Chmod(parent, 0o700)
	})
	if _, err := OpenRoot(filepath.Join(parent, "runtime")); err == nil {
		t.Fatal("root below non-sticky attacker-writable parent was accepted")
	}
}

func TestOpenRootRejectsNonStickyAttackerWritableAncestor(t *testing.T) {
	ancestor := filepath.Join(t.TempDir(), "writable-ancestor")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	// The deliberately unsafe mode is the behavior under test.
	//nolint:gosec
	if err := os.Chmod(ancestor, 0o777); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Restore the test-owned directory so temporary cleanup can remove it.
		//nolint:gosec
		_ = os.Chmod(ancestor, 0o700)
	})
	parent := filepath.Join(ancestor, "private-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenRoot(filepath.Join(parent, "runtime")); err == nil {
		t.Fatal("root below non-sticky attacker-writable ancestor was accepted")
	}
}

func TestUnixAncestorSecurityRejectsUntrustedOwnerRegardlessOfMode(t *testing.T) {
	foreignUID := uint32(1)
	if os.Geteuid() == 1 {
		foreignUID = 2
	}
	for _, test := range []struct {
		name string
		mode os.FileMode
	}{
		{name: "read-only-owner-can-chmod", mode: os.ModeDir | 0o555},
		{
			name: "read-only-sticky-owner-can-chmod",
			mode: os.ModeDir | os.ModeSticky | 0o555,
		},
		{
			name: "writable-sticky-owner-can-replace",
			mode: os.ModeDir | os.ModeSticky | 0o777,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			info := unixAncestorTestInfo{
				mode: test.mode,
				uid:  foreignUID,
			}
			if err := validateImmediateParentSecurity("", info); err == nil {
				t.Fatal("ancestor owned by an untrusted UID was accepted")
			}
		})
	}
}

func TestUnixAncestorSecurityAcceptsRootOwnedStickyDirectory(t *testing.T) {
	info := unixAncestorTestInfo{
		mode: os.ModeDir | os.ModeSticky | 0o777,
		uid:  0,
	}
	if err := validateImmediateParentSecurity("", info); err != nil {
		t.Fatalf("root-owned sticky ancestor was rejected: %v", err)
	}
}

func TestOpenRootRejectsSymlinkedAncestorComponent(t *testing.T) {
	parent := t.TempDir()
	actual := filepath.Join(parent, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	privateParent := filepath.Join(actual, "private-parent")
	if err := os.Mkdir(privateParent, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "linked-ancestor")
	if err := os.Symlink(actual, link); err != nil {
		t.Fatal(err)
	}
	root, err := OpenRoot(filepath.Join(link, "private-parent", "runtime"))
	if err == nil {
		_ = root.Close()
		t.Fatal("root below a symlinked ancestor was accepted")
	}
}

func TestOpenRootRejectsEveryUnsafeUnixPermissionMode(t *testing.T) {
	for permission := 0; permission <= 0o777; permission++ {
		mode := os.FileMode(permission)
		if mode == 0o700 {
			continue
		}
		t.Run(fmt.Sprintf("%04o", permission), func(t *testing.T) {
			assertOpenRootRejectsUnixMode(t, mode)
		})
	}
}

func TestOpenRootRejectsRetainedUnixSpecialModes(t *testing.T) {
	for _, mode := range []os.FileMode{
		0o700 | os.ModeSticky,
		0o700 | os.ModeSetgid,
		0o700 | os.ModeSetuid,
	} {
		t.Run(mode.String(), func(t *testing.T) {
			assertOpenRootRejectsUnixMode(t, mode)
		})
	}
}

func TestOpenRootRejectsUnsafeUnixLockMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(path, ".lock")
	if err := os.WriteFile(lockPath, nil, 0o400); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenRoot(path); err == nil {
		t.Fatal("unsafe lock mode unexpectedly accepted")
	}
}

func TestPrepareAndMaterializeUseExactUnixModes(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	dirInfo, err := os.Lstat(rt.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode(); got != os.ModeDir|0o700 {
		t.Fatalf("request mode=%v", got)
	}

	if err := root.Materialize(rt, []FileSpec{{
		Name: "secret",
		Data: []byte("value"),
		Mode: 0o777,
	}}); err != nil {
		t.Fatal(err)
	}
	fileInfo, err := os.Lstat(filepath.Join(rt.Dir, "secret"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode(); got != 0o600 {
		t.Fatalf("file mode=%v", got)
	}
}

func TestCreationForcesExactModesUnderRestrictiveUmask(t *testing.T) {
	const helperEnv = "SPAWNGATE_RESTRICTIVE_UMASK_HELPER"
	if os.Getenv(helperEnv) == "1" {
		previous := unix.Umask(0o400)
		defer unix.Umask(previous)
		path := os.Getenv("SPAWNGATE_RESTRICTIVE_UMASK_ROOT")
		root, err := OpenRoot(path)
		if err != nil {
			t.Fatal(err)
		}
		rt, err := root.Prepare(testRequestID)
		if err != nil {
			_ = root.Close()
			t.Fatal(err)
		}
		if err := root.Materialize(rt, []FileSpec{{
			Name: "secret",
			Data: []byte("value"),
		}}); err != nil {
			_ = root.Close()
			t.Fatal(err)
		}
		assertUnixMode(t, path, os.ModeDir|runtimeDirMode)
		assertUnixMode(t, filepath.Join(path, lockName), runtimeFileMode)
		assertUnixMode(t, rt.Dir, os.ModeDir|runtimeDirMode)
		assertUnixMode(t, filepath.Join(rt.Dir, "secret"), runtimeFileMode)
		if err := root.Close(); err != nil {
			t.Fatal(err)
		}
		return
	}

	path := filepath.Join(t.TempDir(), "runtime")
	// The executable is this test binary and every argument is fixed.
	//nolint:gosec,noctx
	cmd := exec.Command(
		os.Args[0],
		"-test.count=1",
		"-test.run=^TestCreationForcesExactModesUnderRestrictiveUmask$",
	)
	cmd.Env = append(
		os.Environ(),
		helperEnv+"=1",
		"SPAWNGATE_RESTRICTIVE_UMASK_ROOT="+path,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("restrictive-umask helper failed: %v\n%s", err, output)
	}
}

func TestMaterializeRejectsReplacedPublicRootPath(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "runtime")
	root, err := OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	rt := prepareTestRuntime(t, root)

	movedRoot := filepath.Join(parent, "runtime-moved")
	if err := os.Rename(rootPath, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		outside,
		filepath.Join(rootPath, requestPrefix+rt.ID),
	); err != nil {
		t.Fatal(err)
	}

	if err := root.Materialize(rt, []FileSpec{{
		Name: "anchored",
		Data: []byte("inside original root"),
	}}); err == nil {
		t.Fatal("Materialize accepted a replaced public root path")
	}
	if _, err := os.Lstat(filepath.Join(
		movedRoot,
		requestPrefix+rt.ID,
		"anchored",
	)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("hidden anchored request was modified: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "anchored")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("replacement target affected: %v", err)
	}
}

func TestPrepareRejectsReplacedPublicRootPath(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "runtime")
	root, err := OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	movedRoot := filepath.Join(parent, "runtime-moved")
	if err := os.Rename(rootPath, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}

	rt, err := root.Prepare(testRequestID)
	if err == nil {
		t.Fatalf("Prepare accepted a replaced public root path: %+v", rt)
	}
	if rt.ID != "" || rt.Dir != "" {
		t.Fatalf("Prepare failure returned a misleading Runtime: %+v", rt)
	}
	anchoredRequest := filepath.Join(
		movedRoot,
		requestPrefix+testRequestID,
	)
	if _, err := os.Lstat(anchoredRequest); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("hidden anchored root was modified: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(
		rootPath,
		requestPrefix+testRequestID,
	)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("replacement root affected: %v", err)
	}
}

func TestMaterializeRejectsReplacedPublicRequestPath(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	movedRequest := filepath.Join(rootPathForTest(root), "moved-request")
	if err := os.Rename(rt.Dir, movedRequest); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "keep")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, rt.Dir); err != nil {
		t.Fatal(err)
	}

	if err := root.Materialize(rt, []FileSpec{{
		Name: "anchored",
		Data: []byte("inside original request"),
	}}); err == nil {
		t.Fatal("Materialize accepted a replaced public request path")
	}
	if _, err := os.Lstat(
		filepath.Join(movedRequest, "anchored"),
	); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("hidden anchored request was modified: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(outside, "anchored")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("replacement target affected: %v", err)
	}
	if err := root.Cleanup(context.Background(), rt); err != nil {
		t.Fatalf("cleanup of renamed original request failed: %v", err)
	}
	if _, err := os.Lstat(movedRequest); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("renamed original request remains: %v", err)
	}
	replacement, err := os.Lstat(rt.Dir)
	if err != nil {
		t.Fatalf("replacement request name was removed: %v", err)
	}
	if replacement.Mode()&fs.ModeSymlink == 0 {
		t.Fatalf("replacement mode=%v, want symlink", replacement.Mode())
	}
	// outsideFile is an exact path below this test's private temporary root.
	//nolint:gosec
	got, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside" {
		t.Fatalf("replacement target changed: %q", got)
	}
}

func TestMaterializeRejectsReplacedPublicRequestDirectory(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	movedRequest := filepath.Join(rootPathForTest(root), "moved-request-directory")
	if err := os.Rename(rt.Dir, movedRequest); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rt.Dir, 0o700); err != nil {
		t.Fatal(err)
	}

	if err := root.Materialize(rt, []FileSpec{{
		Name: "anchored",
		Data: []byte("must not be written"),
	}}); err == nil {
		t.Fatal("Materialize accepted a replacement request directory")
	}
	for _, path := range []string{
		filepath.Join(movedRequest, "anchored"),
		filepath.Join(rt.Dir, "anchored"),
	} {
		if _, err := os.Lstat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("replacement scenario wrote %s: %v", path, err)
		}
	}
	if err := root.Cleanup(context.Background(), rt); err != nil {
		t.Fatalf("cleanup of renamed original request failed: %v", err)
	}
	if _, err := os.Lstat(movedRequest); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("renamed original request remains: %v", err)
	}
	replacement, err := os.Lstat(rt.Dir)
	if err != nil {
		t.Fatalf("replacement request directory was removed: %v", err)
	}
	if !replacement.IsDir() {
		t.Fatalf("replacement mode=%v, want directory", replacement.Mode())
	}
}

func TestValidateRuntimePathForLaunchRejectsRequestReplacement(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	if err := root.Materialize(rt, []FileSpec{{
		Name: "input",
		Data: []byte("request"),
	}}); err != nil {
		t.Fatal(err)
	}
	assertRuntimeDirMatchesRecord(t, rt)
	if err := root.validateRuntimePath(rt); err != nil {
		t.Fatalf("stable runtime path was rejected: %v", err)
	}

	movedRequest := filepath.Join(rootPathForTest(root), "moved-for-launch")
	if err := os.Rename(rt.Dir, movedRequest); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rt.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.validateRuntimePath(rt); err == nil {
		t.Fatal("launch validator accepted a replaced public request path")
	}
	if err := root.Cleanup(context.Background(), rt); err != nil {
		t.Fatalf("cleanup of launch-rejected runtime failed: %v", err)
	}
	if _, err := os.Lstat(movedRequest); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("renamed original request remains: %v", err)
	}
	replacement, err := os.Lstat(rt.Dir)
	if err != nil {
		t.Fatalf("replacement request directory was removed: %v", err)
	}
	if !replacement.IsDir() {
		t.Fatalf("replacement mode=%v, want directory", replacement.Mode())
	}
}

func TestCleanupUsesAnchoredRootAfterRootReplacement(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "runtime")
	root, err := OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	rt := prepareTestRuntime(t, root)
	if err := root.Materialize(rt, []FileSpec{{
		Name: "owned",
		Data: []byte("remove"),
	}}); err != nil {
		t.Fatal(err)
	}

	movedRoot := filepath.Join(parent, "runtime-moved")
	if err := os.Rename(rootPath, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "keep")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(
		outside,
		filepath.Join(rootPath, requestPrefix+rt.ID),
	); err != nil {
		t.Fatal(err)
	}

	if err := root.Cleanup(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(
		movedRoot,
		requestPrefix+rt.ID,
	)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("anchored request remains: %v", err)
	}
	// outsideFile is an exact path below this test's private temporary root.
	//nolint:gosec
	got, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside" {
		t.Fatalf("replacement target changed: %q", got)
	}
}

func TestJanitorUsesAnchoredRootAfterRootReplacement(t *testing.T) {
	parent := t.TempDir()
	rootPath := filepath.Join(parent, "runtime")
	root, err := OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	staleName := requestPrefix + "stale001"
	if err := os.Mkdir(filepath.Join(rootPath, staleName), 0o700); err != nil {
		t.Fatal(err)
	}

	movedRoot := filepath.Join(parent, "runtime-moved")
	if err := os.Rename(rootPath, movedRoot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(parent, "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "keep")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(rootPath, staleName)); err != nil {
		t.Fatal(err)
	}

	if err := root.Janitor(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(
		movedRoot,
		staleName,
	)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("anchored stale runtime remains: %v", err)
	}
	// outsideFile is an exact path below this test's private temporary root.
	//nolint:gosec
	got, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside" {
		t.Fatalf("replacement target changed: %q", got)
	}
}

func TestCleanupOpenedChildCannotFollowReplacementSymlink(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	record := root.records[rt.ID]
	childName := "child"
	childPath := filepath.Join(rt.Dir, childName)
	if err := os.Mkdir(childPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(childPath, "owned"),
		[]byte("remove"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	entryInfo, err := record.requestRoot.Lstat(childName)
	if err != nil {
		t.Fatal(err)
	}
	opened, err := openAnchoredDirectory(
		record.requestRoot,
		childName,
		entryInfo,
		root.rootDir,
	)
	if err != nil {
		t.Fatal(err)
	}

	movedChild := filepath.Join(rt.Dir, "moved-child")
	if err := os.Rename(childPath, movedChild); err != nil {
		_ = opened.Close()
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		_ = opened.Close()
		t.Fatal(err)
	}
	outsideFile := filepath.Join(outside, "keep")
	if err := os.WriteFile(outsideFile, []byte("outside"), 0o600); err != nil {
		_ = opened.Close()
		t.Fatal(err)
	}
	if err := os.Symlink(outside, childPath); err != nil {
		_ = opened.Close()
		t.Fatal(err)
	}

	if err := removeOpenedDirectory(
		context.Background(),
		record.requestRoot,
		childName,
		opened,
		root.rootDir,
	); err == nil {
		t.Fatal("replaced child name unexpectedly accepted")
	}
	if _, err := os.Lstat(
		filepath.Join(movedChild, "owned"),
	); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("opened child contents remain: %v", err)
	}
	// outsideFile is an exact path below this test's private temporary root.
	//nolint:gosec
	got, err := os.ReadFile(outsideFile)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside" {
		t.Fatalf("replacement target changed: %q", got)
	}
}

func assertOpenRootRejectsUnixMode(t *testing.T, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// The target is a test-owned directory, not a sensitive file.
		//nolint:gosec
		_ = os.Chmod(path, 0o700)
	})
	// path is always supplied by helpers using private test temporary roots.
	//nolint:gosec
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	const special = os.ModeSetuid | os.ModeSetgid | os.ModeSticky
	if mode&special != 0 && info.Mode()&special != mode&special {
		t.Skipf("filesystem did not retain special mode %v", mode&special)
	}

	if _, err := OpenRoot(path); err == nil {
		t.Fatalf("mode %04o unexpectedly accepted", mode)
	}
}

func assertUnixMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	// path is always supplied by helpers using private test temporary roots.
	//nolint:gosec
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode(); got != want {
		t.Fatalf("%s mode=%v want=%v", path, got, want)
	}
}

type unixAncestorTestInfo struct {
	mode os.FileMode
	uid  uint32
}

func (info unixAncestorTestInfo) Name() string       { return "ancestor" }
func (info unixAncestorTestInfo) Size() int64        { return 0 }
func (info unixAncestorTestInfo) Mode() os.FileMode  { return info.mode }
func (info unixAncestorTestInfo) ModTime() time.Time { return time.Time{} }
func (info unixAncestorTestInfo) IsDir() bool        { return info.mode.IsDir() }
func (info unixAncestorTestInfo) Sys() any {
	return &syscall.Stat_t{Uid: info.uid}
}
