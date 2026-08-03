package process

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"
)

const testRequestID = "request01"

func TestOpenRootCreatesMissingAbsoluteRoot(t *testing.T) {
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
	if !info.IsDir() {
		t.Fatalf("root mode=%v", info.Mode())
	}
}

func TestOpenRootRejectsRelativePath(t *testing.T) {
	if _, err := OpenRoot("relative/runtime"); err == nil {
		t.Fatal("relative root unexpectedly accepted")
	}
}

func TestOpenRootRejectsNonDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	if err := os.WriteFile(path, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenRoot(path); err == nil {
		t.Fatal("non-directory root unexpectedly accepted")
	}
}

func TestOpenRootRejectsSymlink(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "runtime")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}

	if _, err := OpenRoot(link); err == nil {
		t.Fatal("symlink root unexpectedly accepted")
	}
}

func TestOpenRootRejectsSymlinkedParentComponent(t *testing.T) {
	parent := t.TempDir()
	actual := filepath.Join(parent, "actual")
	if err := os.Mkdir(actual, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(parent, "linked")
	if err := os.Symlink(actual, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}

	if _, err := OpenRoot(filepath.Join(link, "runtime")); err == nil {
		t.Fatal("root below symlinked component unexpectedly accepted")
	}
}

func TestRootIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	first, err := OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := first.Close(); err != nil {
			t.Error(err)
		}
	})

	if _, err := OpenRoot(path); !errors.Is(err, ErrRootLocked) {
		t.Fatalf("second OpenRoot() error=%v, want ErrRootLocked", err)
	}
}

func TestCloseUnretainedLockKeepsPersistentSentinel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(path, runtimeDirMode); err != nil {
		t.Fatal(err)
	}
	anchor, err := os.OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := anchor.Close(); err != nil {
			t.Error(err)
		}
	})

	lock, created, err := rOpenLockFile(anchor)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first lock open did not create the sentinel")
	}
	closeUnretainedLock(lock)

	info, err := anchor.Lstat(lockName)
	if err != nil {
		t.Fatalf("persistent lock sentinel disappeared: %v", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.Mode().IsRegular() {
		t.Fatalf("persistent lock sentinel mode=%v", info.Mode())
	}

	reopened, created, err := rOpenLockFile(anchor)
	if err != nil {
		t.Fatal(err)
	}
	if created {
		t.Fatal("reopening the persistent sentinel created a replacement inode")
	}
	closeUnretainedLock(reopened)
}

func TestWindowsAncestorAuthorityPolicy(t *testing.T) {
	const (
		deleteAccess      = uint32(0x00010000)
		writeDACAccess    = uint32(0x00040000)
		writeOwnerAccess  = uint32(0x00080000)
		deleteChildAccess = uint32(0x00000040)
		genericAllAccess  = uint32(0x10000000)
		maximumAccess     = uint32(0x02000000)
		readAccess        = uint32(0x00020000)
	)
	if err := validateWindowsAncestorAuthority(false, nil); err == nil {
		t.Fatal("untrusted ancestor owner was accepted")
	}
	for _, access := range []uint32{
		deleteAccess,
		writeDACAccess,
		writeOwnerAccess,
		deleteChildAccess,
		genericAllAccess,
		maximumAccess,
	} {
		if err := validateWindowsAncestorAuthority(
			true,
			[]windowsAncestorGrant{{access: access}},
		); err == nil {
			t.Fatalf("untrusted mutating access %#x was accepted", access)
		}
	}
	if err := validateWindowsAncestorAuthority(
		true,
		[]windowsAncestorGrant{{access: readAccess}},
	); err != nil {
		t.Fatalf("untrusted read-only access was rejected: %v", err)
	}
	if err := validateWindowsAncestorAuthority(
		true,
		[]windowsAncestorGrant{{
			access:  deleteAccess | writeDACAccess | writeOwnerAccess,
			trusted: true,
		}},
	); err != nil {
		t.Fatalf("trusted mutating access was rejected: %v", err)
	}
}

func TestPrepareValidatesRequestID(t *testing.T) {
	root := openTestRoot(t)
	valid := []string{
		"12345678",
		"AbCdEf_-",
		strings.Repeat("a", 80),
	}
	for _, id := range valid {
		t.Run("valid-"+id[:8], func(t *testing.T) {
			rt, err := root.Prepare(id)
			if err != nil {
				t.Fatal(err)
			}
			if rt.ID != id {
				t.Fatalf("ID=%q", rt.ID)
			}
			if rt.Dir != filepath.Join(rootPathForTest(root), "request-"+id) {
				t.Fatalf("Dir=%q", rt.Dir)
			}
		})
	}

	invalid := []string{
		"",
		"short",
		strings.Repeat("a", 81),
		"contains.dot",
		"contains/slash",
		`contains\slash`,
		"contains space",
		"é2345678",
		"../escape",
	}
	for _, id := range invalid {
		t.Run("invalid-"+strings.ReplaceAll(id, "/", "_"), func(t *testing.T) {
			if _, err := root.Prepare(id); err == nil {
				t.Fatalf("ID %q unexpectedly accepted", id)
			}
		})
	}
}

func TestPrepareIsExclusive(t *testing.T) {
	root := openTestRoot(t)
	if _, err := root.Prepare(testRequestID); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Prepare(testRequestID); err == nil {
		t.Fatal("duplicate request directory unexpectedly accepted")
	}
}

func TestMaterializeCreatesExclusiveFile(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)

	err := root.Materialize(rt, []FileSpec{{
		Name: "input.json",
		Data: []byte(`{"ok":true}`),
		Mode: 0o777,
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(rt.Dir, "input.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"ok":true}` {
		t.Fatalf("data=%q", got)
	}

	if err := root.Materialize(rt, []FileSpec{{
		Name: "input.json",
		Data: []byte("replacement"),
		Mode: 0o600,
	}}); err == nil {
		t.Fatal("existing file unexpectedly overwritten")
	}
	got, err = os.ReadFile(filepath.Join(rt.Dir, "input.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != `{"ok":true}` {
		t.Fatalf("existing data changed to %q", got)
	}
}

func TestMaterializeCreatesExactGeminiSettingsTree(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	settingsData := []byte(`{"security":{"auth":{"selectedType":"gemini-api-key"}}}`)
	settingsName := filepath.Join(".gemini", "settings.json")

	if err := root.Materialize(rt, []FileSpec{{
		Name: settingsName,
		Data: settingsData,
		Mode: 0o777,
	}}); err != nil {
		t.Fatal(err)
	}

	directoryPath := filepath.Join(rt.Dir, ".gemini")
	settingsPath := filepath.Join(directoryPath, "settings.json")
	// settingsPath is an exact path beneath this test's private runtime.
	//nolint:gosec
	got, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, settingsData) {
		t.Fatalf("settings data=%q, want %q", got, settingsData)
	}
	if runtime.GOOS != "windows" {
		directoryInfo, err := os.Lstat(directoryPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := directoryInfo.Mode().Perm(); got != 0o700 {
			t.Fatalf(".gemini mode=%#o, want 0700", got)
		}
		settingsInfo, err := os.Lstat(settingsPath)
		if err != nil {
			t.Fatal(err)
		}
		if got := settingsInfo.Mode().Perm(); got != 0o600 {
			t.Fatalf("settings mode=%#o, want 0600", got)
		}
	}
	for _, name := range []string{"system-defaults.json", "system-settings.json"} {
		if _, err := os.Lstat(filepath.Join(directoryPath, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("unexpected %s exists or stat failed: %v", name, err)
		}
	}
}

func TestMaterializeRejectsEveryOtherNestedShapeBeforeWriting(t *testing.T) {
	separator := string(filepath.Separator)
	accepted := filepath.Join(".gemini", "settings.json")
	tests := []struct {
		name  string
		specs []FileSpec
	}{
		{name: "case directory", specs: []FileSpec{{Name: ".Gemini" + separator + "settings.json"}}},
		{name: "case file", specs: []FileSpec{{Name: ".gemini" + separator + "Settings.json"}}},
		{name: "redundant dot", specs: []FileSpec{{Name: ".gemini" + separator + "." + separator + "settings.json"}}},
		{name: "parent", specs: []FileSpec{{Name: ".gemini" + separator + ".." + separator + "settings.json"}}},
		{name: "repeated separator", specs: []FileSpec{{Name: ".gemini" + separator + separator + "settings.json"}}},
		{name: "other directory", specs: []FileSpec{{Name: "other" + separator + "settings.json"}}},
		{name: "alternate separator", specs: []FileSpec{{Name: `.gemini\settings.json`}}},
		{name: "absolute", specs: []FileSpec{{Name: filepath.Join(string(filepath.Separator), ".gemini", "settings.json")}}},
		{name: "embedded NUL", specs: []FileSpec{{Name: ".gemini" + separator + "settings.json\x00"}}},
		{name: "unicode", specs: []FileSpec{{Name: ".gémini" + separator + "settings.json"}}},
		{name: "nested plus base", specs: []FileSpec{{Name: accepted}, {Name: "base"}}},
		{name: "duplicate nested", specs: []FileSpec{{Name: accepted}, {Name: accepted}}},
	}
	if filepath.Separator == '\\' {
		tests[6].specs[0].Name = ".gemini/settings.json"
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := openTestRoot(t)
			rt := prepareTestRuntime(t, root)
			for index := range test.specs {
				test.specs[index].Data = []byte("must not write")
			}
			if err := root.Materialize(rt, test.specs); err == nil {
				t.Fatalf("Materialize accepted specs=%+v", test.specs)
			}
			assertRequestDirectoryEmpty(t, rt.Dir)
		})
	}
}

func TestMaterializeGeminiSettingsRollsBackCreatedTreeOnFailure(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	cause := errors.New("injected write failure")

	_, err := materializeGeminiSettings(
		rt.record,
		FileSpec{
			Name: filepath.Join(".gemini", "settings.json"),
			Data: []byte("must roll back"),
		},
		nil,
		func() error { return cause },
		nil,
	)
	if !errors.Is(err, cause) {
		t.Fatalf("error=%v, want injected cause", err)
	}
	assertRequestDirectoryEmpty(t, rt.Dir)
}

func TestMaterializeGeminiSettingsRestoresModeBeforeAnchoring(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix umask/mode behavior")
	}
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	directoryPath := filepath.Join(rt.Dir, ".gemini")

	_, err := materializeGeminiSettings(
		rt.record,
		FileSpec{
			Name: filepath.Join(".gemini", "settings.json"),
			Data: []byte("settings"),
		},
		func() error {
			// directoryPath is an exact path below this test's private runtime.
			return os.Chmod(directoryPath, 0)
		},
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("materialize after restrictive creation mode: %v", err)
	}
	directoryInfo, err := os.Lstat(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := directoryInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf(".gemini mode=%#o, want 0700", got)
	}
}

func TestMaterializeGeminiSettingsDetectsPublicDirectoryReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("open directory replacement requires native Windows coverage")
	}
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	directoryPath := filepath.Join(rt.Dir, ".gemini")
	movedPath := filepath.Join(rt.Dir, ".gemini-created-by-call")
	markerPath := filepath.Join(directoryPath, "replacement-marker")

	_, err := materializeGeminiSettings(
		rt.record,
		FileSpec{
			Name: filepath.Join(".gemini", "settings.json"),
			Data: []byte("settings"),
		},
		nil,
		nil,
		func() error {
			if err := os.Rename(directoryPath, movedPath); err != nil {
				return err
			}
			if err := os.Mkdir(directoryPath, 0o700); err != nil {
				return err
			}
			return os.WriteFile(markerPath, []byte("preserve"), 0o600)
		},
	)
	if err == nil {
		t.Fatal("public .gemini replacement was not detected")
	}
	if _, err := os.Lstat(markerPath); err != nil {
		t.Fatalf("replacement entry was not preserved: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(movedPath, "settings.json")); err != nil {
		t.Fatalf("identity-proven created tree disappeared unexpectedly: %v", err)
	}
}

func TestMaterializeGeminiSettingsRejectsPostWriteFileReplacement(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	originalData := []byte("original settings")
	replacementData := []byte("replacement must be preserved")
	settingsPath := filepath.Join(rt.Dir, ".gemini", "settings.json")
	movedPath := filepath.Join(rt.Dir, ".gemini", "created-settings.json")

	_, err := materializeGeminiSettings(
		rt.record,
		FileSpec{
			Name: filepath.Join(".gemini", "settings.json"),
			Data: originalData,
		},
		nil,
		nil,
		func() error {
			if err := os.Rename(settingsPath, movedPath); err != nil {
				return err
			}
			return os.WriteFile(settingsPath, replacementData, 0o600)
		},
	)
	if err == nil {
		t.Fatal("post-write settings replacement was accepted")
	}
	// settingsPath is the exact path below this test's private request runtime.
	//nolint:gosec
	got, readErr := os.ReadFile(settingsPath)
	if readErr != nil {
		t.Fatalf("replacement was not preserved: %v", readErr)
	}
	if !reflect.DeepEqual(got, replacementData) {
		t.Fatalf("replacement data changed: got %q", got)
	}
	// movedPath is the exact path below this test's private request runtime.
	//nolint:gosec
	moved, readErr := os.ReadFile(movedPath)
	if readErr != nil {
		t.Fatalf("unexpected moved entry was not preserved: %v", readErr)
	}
	if !reflect.DeepEqual(moved, originalData) {
		t.Fatalf("moved original data changed: got %q", moved)
	}
}

func TestMaterializeGeminiSettingsRejectsPostWriteUnsafeMode(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native Windows DACL execution is deferred")
	}
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	settingsPath := filepath.Join(rt.Dir, ".gemini", "settings.json")

	_, err := materializeGeminiSettings(
		rt.record,
		FileSpec{
			Name: filepath.Join(".gemini", "settings.json"),
			Data: []byte("settings"),
		},
		nil,
		nil,
		func() error {
			// Deliberately weaken the private test file to exercise rejection.
			//nolint:gosec
			return os.Chmod(settingsPath, 0o644)
		},
	)
	if err == nil {
		t.Fatal("post-write unsafe settings mode was accepted")
	}
	assertRequestDirectoryEmpty(t, rt.Dir)
}

func TestRollbackGeminiSettingsReportsUnavailableCreatedIdentity(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	directoryPath := filepath.Join(rt.Dir, ".gemini")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	cause := errors.New("injected post-create inspection failure")

	err := rollbackGeminiSettings(
		rt.record,
		geminiMaterialization{directoryCreated: true},
		cause,
	)
	if !errors.Is(err, cause) {
		t.Fatalf("error=%v, want original cause", err)
	}
	if err.Error() == cause.Error() {
		t.Fatal("rollback omitted the identity-unavailable cleanup failure")
	}
	if _, err := os.Lstat(directoryPath); err != nil {
		t.Fatalf("identity-unproven directory was removed: %v", err)
	}
}

func TestMaterializeGeminiSettingsPreservesPreexistingTargets(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, string) string
	}{
		{
			name: "directory",
			setup: func(t *testing.T, runtimeDir string) string {
				t.Helper()
				path := filepath.Join(runtimeDir, ".gemini")
				if err := os.Mkdir(path, 0o700); err != nil {
					t.Fatal(err)
				}
				marker := filepath.Join(path, "preexisting")
				if err := os.WriteFile(marker, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				return marker
			},
		},
		{
			name: "file",
			setup: func(t *testing.T, runtimeDir string) string {
				t.Helper()
				path := filepath.Join(runtimeDir, ".gemini")
				if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
		{
			name: "symlink",
			setup: func(t *testing.T, runtimeDir string) string {
				t.Helper()
				if runtime.GOOS == "windows" {
					t.Skip("native Windows symlink coverage is deferred")
				}
				target := filepath.Join(t.TempDir(), "target")
				if err := os.Mkdir(target, 0o700); err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(runtimeDir, ".gemini")
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
				return path
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := openTestRoot(t)
			rt := prepareTestRuntime(t, root)
			preserved := test.setup(t, rt.Dir)
			if err := root.Materialize(rt, []FileSpec{{
				Name: filepath.Join(".gemini", "settings.json"),
				Data: []byte("replacement"),
			}}); err == nil {
				t.Fatal("preexisting .gemini unexpectedly accepted")
			}
			if _, err := os.Lstat(preserved); err != nil {
				t.Fatalf("preexisting target was not preserved: %v", err)
			}
		})
	}
}

func TestCleanupRemovesGeminiSettingsTree(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	if err := root.Materialize(rt, []FileSpec{{
		Name: filepath.Join(".gemini", "settings.json"),
		Data: []byte("settings"),
	}}); err != nil {
		t.Fatal(err)
	}
	if err := root.Cleanup(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(rt.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("request runtime remains: %v", err)
	}
}

func TestMaterializeRejectsUnsafeNamesBeforeWriting(t *testing.T) {
	unsafeNames := []string{
		"",
		".",
		"..",
		"../escape",
		`..\escape`,
		"nested/file",
		`nested\file`,
		string(filepath.Separator),
	}
	for _, name := range unsafeNames {
		t.Run(strings.ReplaceAll(name, "/", "_"), func(t *testing.T) {
			root := openTestRoot(t)
			rt := prepareTestRuntime(t, root)
			err := root.Materialize(rt, []FileSpec{
				{Name: "would-be-partial", Data: []byte("must not exist")},
				{Name: name, Data: []byte("unsafe")},
			})
			if err == nil {
				t.Fatalf("unsafe name %q unexpectedly accepted", name)
			}
			if _, statErr := os.Lstat(filepath.Join(rt.Dir, "would-be-partial")); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("partial file exists or stat failed: %v", statErr)
			}
		})
	}
}

func TestMaterializeRejectsEmbeddedNULBeforeWriting(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	err := root.Materialize(rt, []FileSpec{
		{Name: "would-be-partial", Data: []byte("must not exist")},
		{Name: "bad\x00name", Data: []byte("unsafe")},
	})
	if err == nil {
		t.Fatal("embedded NUL unexpectedly accepted")
	}
	if _, statErr := os.Lstat(
		filepath.Join(rt.Dir, "would-be-partial"),
	); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("partial file exists or stat failed: %v", statErr)
	}
}

func TestMaterializeRejectsSemanticCollisionsAndLongNamesBeforeWriting(t *testing.T) {
	tests := []struct {
		name   string
		second string
	}{
		{name: "case collision", second: "INPUT.JSON"},
		{name: "over component bound", second: strings.Repeat("x", 256)},
		{name: "normalization ambiguity", second: "résumé.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := openTestRoot(t)
			rt := prepareTestRuntime(t, root)
			err := root.Materialize(rt, []FileSpec{
				{Name: "input.json", Data: []byte("must not exist")},
				{Name: tt.second, Data: []byte("unsafe")},
			})
			if err == nil {
				t.Fatalf("unsafe complete set accepted: %q", tt.second)
			}
			assertRequestDirectoryEmpty(t, rt.Dir)
		})
	}
}

func TestMaterializeRollsBackFilesCreatedBeforeLaterFailure(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	existing := filepath.Join(rt.Dir, "existing")
	if err := os.WriteFile(existing, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := root.Materialize(rt, []FileSpec{
		{Name: "created-by-call", Data: []byte("remove")},
		{Name: "existing", Data: []byte("must not overwrite")},
	})
	if err == nil {
		t.Fatal("later exclusive-open failure unexpectedly succeeded")
	}
	if _, err := os.Lstat(
		filepath.Join(rt.Dir, "created-by-call"),
	); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("transaction file remains: %v", err)
	}
	// existing is an exact path below this test's private temporary directory.
	//nolint:gosec
	got, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatalf("preexisting file changed: %q", got)
	}
}

func TestMaterializeDoesNotFollowSymlink(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("outside"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(rt.Dir, "input.json")
	if err := os.Symlink(target, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}

	if err := root.Materialize(rt, []FileSpec{{
		Name: "input.json",
		Data: []byte("overwrite"),
		Mode: 0o600,
	}}); err == nil {
		t.Fatal("symlink unexpectedly overwritten")
	}
	// target is an exact path below this test's private temporary directory.
	//nolint:gosec
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "outside" {
		t.Fatalf("outside target changed to %q", got)
	}
}

func TestRootMethodsRejectForgedRuntime(t *testing.T) {
	root := openTestRoot(t)
	outside := filepath.Join(t.TempDir(), "request-"+testRequestID)
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	rt := Runtime{ID: testRequestID, Dir: outside}

	if err := root.Materialize(rt, []FileSpec{{Name: "unsafe", Data: []byte("x")}}); err == nil {
		t.Fatal("forged runtime unexpectedly materialized")
	}
	if err := root.Cleanup(context.Background(), rt); err == nil {
		t.Fatal("forged runtime unexpectedly cleaned")
	}
	if _, err := os.Lstat(outside); err != nil {
		t.Fatalf("outside directory affected: %v", err)
	}
}

func TestCleanupRemovesRuntimeAndIsIdempotent(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	if err := root.Materialize(rt, []FileSpec{{Name: "input", Data: []byte("x")}}); err != nil {
		t.Fatal(err)
	}

	if err := root.Cleanup(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(rt.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("request directory remains: %v", err)
	}
	if err := root.Cleanup(context.Background(), rt); err != nil {
		t.Fatalf("second cleanup failed: %v", err)
	}
}

func TestCleanupRetiresRemovedRecordsAtHighVolume(t *testing.T) {
	root := openTestRoot(t)
	const requests = 1000
	for i := range requests {
		id := fmt.Sprintf("id%06d", i)
		rt, err := root.Prepare(id)
		if err != nil {
			t.Fatal(err)
		}
		if err := root.Cleanup(context.Background(), rt); err != nil {
			t.Fatal(err)
		}
	}
	root.recordsMu.Lock()
	got := len(root.records)
	root.recordsMu.Unlock()
	if got != 0 {
		t.Fatalf("retained removed records=%d", got)
	}
}

func TestStaleRuntimeCannotValidateAfterSameIDReuse(t *testing.T) {
	root := openTestRoot(t)
	stale := prepareTestRuntime(t, root)
	if err := root.Cleanup(context.Background(), stale); err != nil {
		t.Fatal(err)
	}
	current, err := root.Prepare(stale.ID)
	if err != nil {
		t.Fatal(err)
	}

	if err := root.Materialize(stale, []FileSpec{{
		Name: "stale",
		Data: []byte("must not write"),
	}}); err == nil {
		t.Fatal("stale runtime materialized after same-ID reuse")
	}
	if err := root.Cleanup(context.Background(), stale); err == nil {
		t.Fatal("stale runtime cleaned after same-ID reuse")
	}
	if err := root.Materialize(current, []FileSpec{{
		Name: "current",
		Data: []byte("current"),
	}}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(current.Dir, "current"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "current" {
		t.Fatalf("current data=%q", got)
	}
}

func TestRecordMapTracksOnlyLiveAndQuarantinedRuntimes(t *testing.T) {
	root := openTestRoot(t)
	const active = 12
	const quarantined = 8
	for i := range active {
		id := fmt.Sprintf("active%03d", i)
		if _, err := root.Prepare(id); err != nil {
			t.Fatal(err)
		}
	}
	for i := range quarantined {
		id := fmt.Sprintf("closed%03d", i)
		rt, err := root.Prepare(id)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := root.Cleanup(ctx, rt); err == nil {
			t.Fatal("canceled cleanup unexpectedly succeeded")
		}
	}
	root.recordsMu.Lock()
	got := len(root.records)
	root.recordsMu.Unlock()
	if want := active + quarantined; got != want {
		t.Fatalf("records=%d want=%d", got, want)
	}
	if err := root.Janitor(context.Background()); err != nil {
		t.Fatal(err)
	}
	root.recordsMu.Lock()
	got = len(root.records)
	root.recordsMu.Unlock()
	if got != active {
		t.Fatalf("records after janitor=%d want=%d", got, active)
	}
}

func TestCleanupFailureQuarantinesInsideRoot(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := root.Cleanup(ctx, rt)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("error=%T %v", err, err)
	}
	if _, statErr := os.Lstat(rt.Dir); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("request directory remains: %v", statErr)
	}
	quarantine := filepath.Join(rootPathForTest(root), "quarantine-"+rt.ID)
	info, statErr := os.Lstat(quarantine)
	if statErr != nil {
		t.Fatal(statErr)
	}
	if !info.IsDir() {
		t.Fatalf("quarantine mode=%v", info.Mode())
	}
	if filepath.Dir(quarantine) != rootPathForTest(root) {
		t.Fatalf("quarantine escaped root: %q", quarantine)
	}

	if err := root.Janitor(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Lstat(quarantine); !errors.Is(statErr, fs.ErrNotExist) {
		t.Fatalf("quarantine remains: %v", statErr)
	}
}

func TestCleanupRenameFailureLeavesRequestForJanitorRecovery(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	conflict := filepath.Join(
		rootPathForTest(root),
		quarantinePrefix+rt.ID,
	)
	if err := os.Mkdir(conflict, 0o700); err != nil {
		t.Fatal(err)
	}

	err := root.Cleanup(context.Background(), rt)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("error=%T %v", err, err)
	}
	if _, err := os.Lstat(rt.Dir); err != nil {
		t.Fatalf("failed request path was not retained: %v", err)
	}
	if err := os.Remove(conflict); err != nil {
		t.Fatal(err)
	}

	if err := root.Janitor(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(rt.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("janitor did not reclaim failed request: %v", err)
	}
	root.recordsMu.Lock()
	remaining := len(root.records)
	root.recordsMu.Unlock()
	if remaining != 0 {
		t.Fatalf("runtime records after janitor = %d, want 0", remaining)
	}
}

func TestCleanupRenameFailureCanBeRetried(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	conflict := filepath.Join(
		rootPathForTest(root),
		quarantinePrefix+rt.ID,
	)
	if err := os.Mkdir(conflict, 0o700); err != nil {
		t.Fatal(err)
	}

	err := root.Cleanup(context.Background(), rt)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("first cleanup error=%T %v", err, err)
	}
	if err := root.validateRuntimePath(rt); !errors.Is(err, errInvalidRuntime) {
		t.Fatalf("cleanup-pending runtime validation error = %v, want invalid runtime", err)
	}
	if err := root.Materialize(rt, []FileSpec{{Name: "late", Data: []byte("refuse")}}); !errors.Is(err, errInvalidRuntime) {
		t.Fatalf("cleanup-pending materialize error = %v, want invalid runtime", err)
	}
	if err := os.Remove(conflict); err != nil {
		t.Fatal(err)
	}

	if err := root.Cleanup(context.Background(), rt); err != nil {
		t.Fatalf("retry cleanup: %v", err)
	}
	if _, err := os.Lstat(rt.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("request directory remains after retry: %v", err)
	}
	if _, err := os.Lstat(conflict); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("quarantine directory remains after retry: %v", err)
	}
	assertRuntimeRetired(t, root, rt)
}

func TestCleanupReportsHandleCloseFailureAfterRetiring(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	if err := rt.record.requestDir.Close(); err != nil {
		t.Fatal(err)
	}

	err := root.Cleanup(context.Background(), rt)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("cleanup error=%T %v, want cleanup error", err, err)
	}
	if runErr.Err == nil || !strings.Contains(
		runErr.Err.Error(),
		"close runtime handles",
	) {
		t.Fatalf("cleanup cause = %v, want handle close stage", runErr.Err)
	}
	if _, err := os.Lstat(rt.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("request directory remains: %v", err)
	}
	assertRuntimeRetired(t, root, rt)
}

func TestCleanupAndJanitorDoNotDeleteRequestMovedOutsideRoot(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)

	moved := filepath.Join(t.TempDir(), "moved-request")
	if err := os.Rename(rt.Dir, moved); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(moved, "outside-data")
	if err := os.WriteFile(sentinel, []byte("must remain"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := root.Cleanup(context.Background(), rt)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("error=%T %v", err, err)
	}
	assertOutsideRuntimeUntouched(t, moved, sentinel)

	if err := root.Janitor(context.Background()); err != nil {
		t.Fatal(err)
	}
	assertOutsideRuntimeUntouched(t, moved, sentinel)
}

func TestJanitorRefusesReplacementAtCleanupPendingRequestName(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)

	moved := filepath.Join(t.TempDir(), "moved-request")
	if err := os.Rename(rt.Dir, moved); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rt.Dir, 0o700); err != nil {
		t.Fatal(err)
	}
	replacementSentinel := filepath.Join(rt.Dir, "replacement-data")
	if err := os.WriteFile(
		replacementSentinel,
		[]byte("must remain"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	err := root.Cleanup(context.Background(), rt)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("cleanup error=%T %v", err, err)
	}
	err = root.Janitor(context.Background())
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("janitor error=%T %v", err, err)
	}
	got, err := os.ReadFile(replacementSentinel) //nolint:gosec // Exact test-owned path.
	if err != nil {
		t.Fatalf("replacement directory was deleted: %v", err)
	}
	if string(got) != "must remain" {
		t.Fatalf("replacement sentinel changed: %q", got)
	}
	if _, err := os.Lstat(moved); err != nil {
		t.Fatalf("moved original was affected: %v", err)
	}
}

func TestJanitorRefusesActiveRuntimeMovedToQuarantineName(t *testing.T) {
	t.Parallel()

	root := openTestRoot(t)
	runtime := prepareTestRuntime(t, root)
	if err := root.Materialize(runtime, []FileSpec{{Name: "active-data", Data: []byte("must remain")}}); err != nil {
		t.Fatalf("materialize runtime: %v", err)
	}

	quarantine := filepath.Join(rootPathForTest(root), quarantinePrefix+runtime.ID)
	if err := os.Rename(runtime.Dir, quarantine); err != nil {
		t.Fatalf("move active runtime to quarantine name: %v", err)
	}

	err := root.Janitor(context.Background())
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("Janitor() error = %v, want cleanup error", err)
	}

	data, err := os.ReadFile(filepath.Join(quarantine, "active-data")) // #nosec G304 -- test path is rooted in a trusted temporary directory.
	if err != nil {
		t.Fatalf("read active runtime data after janitor: %v", err)
	}
	if got := string(data); got != "must remain" {
		t.Fatalf("active runtime data = %q, want %q", got, "must remain")
	}

	runtime.record.mu.Lock()
	defer runtime.record.mu.Unlock()
	if runtime.record.state != runtimeActive {
		t.Fatalf("runtime state = %v, want active", runtime.record.state)
	}
}

func TestJanitorReclaimsCleanupPendingRuntimeAtQuarantineName(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	quarantine := filepath.Join(
		rootPathForTest(root),
		quarantinePrefix+rt.ID,
	)
	if err := os.Mkdir(quarantine, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := root.Cleanup(context.Background(), rt); err == nil {
		t.Fatal("cleanup unexpectedly succeeded with a quarantine collision")
	}
	if err := os.Remove(quarantine); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(rt.Dir, quarantine); err != nil {
		t.Fatal(err)
	}

	if err := root.Janitor(context.Background()); err != nil {
		t.Fatalf("janitor cleanup-pending runtime: %v", err)
	}
	if _, err := os.Lstat(quarantine); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("quarantine directory remains: %v", err)
	}
	assertRuntimeRetired(t, root, rt)
}

func TestJanitorReclaimsQuarantinedRuntimeAtRequestName(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := root.Cleanup(ctx, rt); err == nil {
		t.Fatal("cleanup unexpectedly ignored cancellation")
	}
	quarantine := filepath.Join(
		rootPathForTest(root),
		quarantinePrefix+rt.ID,
	)
	if err := os.Rename(quarantine, rt.Dir); err != nil {
		t.Fatal(err)
	}

	if err := root.Janitor(context.Background()); err != nil {
		t.Fatalf("janitor quarantined runtime at request name: %v", err)
	}
	if _, err := os.Lstat(rt.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("request directory remains: %v", err)
	}
	assertRuntimeRetired(t, root, rt)
}

func assertRuntimeRetired(t *testing.T, root *Root, rt Runtime) {
	t.Helper()
	rt.record.mu.Lock()
	state := rt.record.state
	hasOpenHandles := rt.record.requestRoot != nil || rt.record.requestDir != nil
	rt.record.mu.Unlock()
	if state != runtimeRemoved {
		t.Fatalf("runtime state = %v, want removed", state)
	}
	if hasOpenHandles {
		t.Fatal("retired runtime retained open directory handles")
	}

	root.recordsMu.Lock()
	_, retained := root.records[rt.ID]
	root.recordsMu.Unlock()
	if retained {
		t.Fatal("retired runtime remains in the root record map")
	}
}

func assertOutsideRuntimeUntouched(t *testing.T, moved, sentinel string) {
	t.Helper()
	info, err := os.Lstat(moved)
	if err != nil {
		t.Fatalf("moved request directory was removed: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("moved request mode=%v", info.Mode())
	}
	// sentinel is an exact path below a test-owned temporary directory.
	//nolint:gosec
	got, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("outside sentinel was removed: %v", err)
	}
	if string(got) != "must remain" {
		t.Fatalf("outside sentinel changed: %q", got)
	}
}

func TestRuntimeQuarantineRenameNeverReplacesExistingTarget(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	quarantineName := quarantinePrefix + rt.ID
	quarantinePath := filepath.Join(rootPathForTest(root), quarantineName)
	if err := os.WriteFile(quarantinePath, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := renameRuntimeNoReplace(
		root.rootDir,
		root.anchor,
		requestPrefix+rt.ID,
		quarantineName,
	)
	if err == nil {
		t.Fatal("no-replace runtime rename overwrote destination")
	}
	if _, err := os.Lstat(rt.Dir); err != nil {
		t.Fatalf("request source affected: %v", err)
	}
	// quarantinePath is an exact path below the test-owned runtime root.
	//nolint:gosec
	got, err := os.ReadFile(quarantinePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "keep" {
		t.Fatalf("quarantine destination changed: %q", got)
	}
}

func TestCleanupWideDirectoryHonorsDeadlineAndReleasesLocks(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	const files = 3000
	for i := range files {
		name := filepath.Join(rt.Dir, fmt.Sprintf("%06d", i))
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Microsecond)
	defer cancel()
	started := time.Now()
	err := root.Cleanup(ctx, rt)
	elapsed := time.Since(started)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("error=%T %v", err, err)
	}
	if elapsed > time.Second {
		t.Fatalf("wide cleanup exceeded bound: %v", elapsed)
	}
	record := rt.record
	if !record.mu.TryLock() {
		t.Fatal("runtime lock remained held after deadline")
	}
	state := record.state
	record.mu.Unlock()
	if state != runtimeQuarantined {
		t.Fatalf("state=%v want quarantined", state)
	}
	if !root.lifecycle.TryLock() {
		t.Fatal("lifecycle lock remained held after deadline")
	}
	root.lifecycle.Unlock()

	if err := root.Janitor(context.Background()); err != nil {
		t.Fatal(err)
	}
	root.recordsMu.Lock()
	remaining := len(root.records)
	root.recordsMu.Unlock()
	if remaining != 0 {
		t.Fatalf("records after janitor=%d", remaining)
	}
}

func TestJanitorWideRootHonorsDeadlineAndReleasesLifecycle(t *testing.T) {
	root := openTestRoot(t)
	const files = 3000
	for i := range files {
		name := filepath.Join(
			rootPathForTest(root),
			fmt.Sprintf("ordinary-%06d", i),
		)
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Microsecond)
	defer cancel()
	started := time.Now()
	err := root.Janitor(ctx)
	elapsed := time.Since(started)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("error=%T %v", err, err)
	}
	if elapsed > time.Second {
		t.Fatalf("wide janitor exceeded bound: %v", elapsed)
	}
	if !root.lifecycle.TryLock() {
		t.Fatal("lifecycle lock remained held after janitor deadline")
	}
	root.lifecycle.Unlock()
}

func TestJanitorRemovesOnlyClosedStaleNames(t *testing.T) {
	root := openTestRoot(t)
	rootPath := rootPathForTest(root)
	remove := []string{"request-stale001", "quarantine-stale002"}
	keep := []string{
		"ordinary",
		"request-short",
		"quarantine-has.dot",
		"request-stale003.extra",
		"request_stale004",
	}
	for _, name := range append(append([]string{}, remove...), keep...) {
		if err := os.Mkdir(filepath.Join(rootPath, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if err := root.Janitor(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, name := range remove {
		if _, err := os.Lstat(filepath.Join(rootPath, name)); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("%q remains: %v", name, err)
		}
	}
	for _, name := range keep {
		if _, err := os.Lstat(filepath.Join(rootPath, name)); err != nil {
			t.Errorf("%q was affected: %v", name, err)
		}
	}
}

func TestJanitorPrepareSameIDInterleavingKeepsNewRuntimeActive(t *testing.T) {
	root := openTestRoot(t)
	id := "reused001"
	stale := filepath.Join(rootPathForTest(root), quarantinePrefix+id)
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}

	// Hold the same stale-name critical section Janitor uses. Prepare must
	// remain blocked until the old name is removed and finalization completes.
	record, staleCandidate := root.lockStaleRuntime(id, quarantinePrefix)
	if !staleCandidate || record != nil {
		t.Fatal("unrecorded quarantine was not selected as stale")
	}
	type prepareResult struct {
		runtime Runtime
		err     error
	}
	started := make(chan struct{})
	prepared := make(chan prepareResult, 1)
	go func() {
		close(started)
		rt, err := root.Prepare(id)
		prepared <- prepareResult{runtime: rt, err: err}
	}()
	<-started
	runtime.Gosched()
	select {
	case result := <-prepared:
		root.unlockStaleRuntime(record)
		t.Fatalf("Prepare completed inside janitor critical section: %v", result.err)
	default:
	}

	if err := root.removeAnchoredDirectory(
		context.Background(),
		root.anchor,
		quarantinePrefix+id,
	); err != nil {
		root.unlockStaleRuntime(record)
		t.Fatal(err)
	}
	root.unlockStaleRuntime(record)

	var result prepareResult
	select {
	case result = <-prepared:
	case <-time.After(2 * time.Second):
		t.Fatal("Prepare remained blocked after janitor critical section")
	}
	if result.err != nil {
		t.Fatal(result.err)
	}
	if err := root.Cleanup(context.Background(), result.runtime); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(result.runtime.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("new active runtime was not removed: %v", err)
	}
}

func TestJanitorStaleQuarantineCannotRemoveActiveRequestState(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	stale := filepath.Join(
		rootPathForTest(root),
		quarantinePrefix+testRequestID,
	)
	if err := os.Mkdir(stale, 0o700); err != nil {
		t.Fatal(err)
	}

	err := root.Janitor(context.Background())
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("janitor error=%T %v, want cleanup error", err, err)
	}
	if _, err := os.Lstat(stale); err != nil {
		t.Fatalf("janitor removed an unverified quarantine collision: %v", err)
	}
	if _, err := os.Lstat(rt.Dir); err != nil {
		t.Fatalf("janitor affected active request: %v", err)
	}
	if err := os.Remove(stale); err != nil {
		t.Fatal(err)
	}
	if err := root.Cleanup(context.Background(), rt); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(rt.Dir); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("active request was not removed: %v", err)
	}
}

func TestJanitorRefusesSymlinkEntry(t *testing.T) {
	root := openTestRoot(t)
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.Mkdir(outside, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(rootPathForTest(root), "request-stale001")
	if err := os.Symlink(outside, link); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}

	err := root.Janitor(context.Background())
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("error=%T %v", err, err)
	}
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("symlink affected: %v", err)
	}
	if _, err := os.Lstat(outside); err != nil {
		t.Fatalf("outside target affected: %v", err)
	}
}

func TestCloseReleasesLockAndIsIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	first, err := OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("second close failed: %v", err)
	}

	second, err := OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCloseReleasesActiveRequestDirectoryHandles(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	rt.record.mu.Lock()
	requestRoot := rt.record.requestRoot
	requestDir := rt.record.requestDir
	rt.record.mu.Unlock()
	if requestRoot != nil || requestDir != nil {
		t.Fatal("active request directory handles remain after Close")
	}
}

func TestClosedRootRejectsOperations(t *testing.T) {
	root := openTestRoot(t)
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := root.Prepare(testRequestID); err == nil {
		t.Fatal("closed root prepared runtime")
	}
	rt := Runtime{
		ID:  testRequestID,
		Dir: filepath.Join(rootPathForTest(root), "request-"+testRequestID),
	}
	if err := root.Materialize(rt, nil); err == nil {
		t.Fatal("closed root materialized runtime")
	}
	if err := root.Cleanup(context.Background(), rt); err == nil {
		t.Fatal("closed root cleaned runtime")
	}
	if err := root.Janitor(context.Background()); err == nil {
		t.Fatal("closed root ran janitor")
	}
}

func TestRunErrorWrapsInternalCause(t *testing.T) {
	cause := errors.New("internal detail")
	err := &RunError{Kind: ErrorStart, Err: cause}
	if !errors.Is(err, cause) {
		t.Fatal("RunError does not unwrap cause")
	}
	if got := err.Error(); got != "start: internal detail" {
		t.Fatalf("Error()=%q", got)
	}
}

func openTestRoot(t *testing.T) *Root {
	t.Helper()
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
	return root
}

func prepareTestRuntime(t *testing.T, root *Root) Runtime {
	t.Helper()
	rt, err := root.Prepare(testRequestID)
	if err != nil {
		t.Fatal(err)
	}
	assertRuntimeDirMatchesRecord(t, rt)
	return rt
}

func assertRuntimeDirMatchesRecord(t *testing.T, runtime Runtime) {
	t.Helper()
	if runtime.record == nil || runtime.record.requestInfo == nil {
		t.Fatal("runtime is missing retained request identity")
	}
	info, err := os.Lstat(runtime.Dir)
	if err != nil {
		t.Fatalf("inspect successful Runtime.Dir: %v", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		t.Fatalf("successful Runtime.Dir is a symlink: %v", info.Mode())
	}
	if !os.SameFile(info, runtime.record.requestInfo) {
		t.Fatal("successful Runtime.Dir does not resolve to retained request identity")
	}
}

func rootPathForTest(root *Root) string {
	return root.path
}

func assertRequestDirectoryEmpty(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("request directory contains %d entries: %v", len(entries), entries)
	}
}
