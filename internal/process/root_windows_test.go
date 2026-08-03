//go:build windows

package process

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestClassifyWindowsLockErrorMapsOnlyContention(t *testing.T) {
	if got := classifyLockError(windows.ERROR_LOCK_VIOLATION); !errors.Is(got, ErrRootLocked) {
		t.Fatalf("classifyLockError(contention)=%v, want ErrRootLocked", got)
	}

	other := windows.ERROR_ACCESS_DENIED
	if got := classifyLockError(other); !errors.Is(got, other) ||
		errors.Is(got, ErrRootLocked) {
		t.Fatalf("classifyLockError(access denied)=%v", got)
	}
	if got := classifyLockError(nil); got != nil {
		t.Fatalf("classifyLockError(nil)=%v", got)
	}
}

func TestWindowsPlatformRejectsUnsafeBaseFilenames(t *testing.T) {
	unsafe := unsafeWindowsFilenames()
	for _, name := range unsafe {
		t.Run(name, func(t *testing.T) {
			if validPlatformFileName(name) {
				t.Fatalf("unsafe Windows base filename accepted: %q", name)
			}
		})
	}
	valid := []string{
		"input.json",
		".hidden",
		"COM10",
		"LPT0.txt",
		"auxiliary.txt",
		"name with space.txt",
		"file.stream.txt",
		"데이터.json",
	}
	for _, name := range valid {
		t.Run(name, func(t *testing.T) {
			if !validPlatformFileName(name) {
				t.Fatalf("valid Windows base filename rejected: %q", name)
			}
		})
	}
}

func TestMaterializeRejectsUnsafeWindowsNamesBeforeWriting(t *testing.T) {
	for _, name := range unsafeWindowsFilenames() {
		t.Run(name, func(t *testing.T) {
			root := openTestRoot(t)
			rt := prepareTestRuntime(t, root)
			err := root.Materialize(rt, []FileSpec{
				{Name: "would-be-partial", Data: []byte("must not exist")},
				{Name: name, Data: []byte("unsafe")},
			})
			if err == nil {
				t.Fatalf("unsafe Windows name accepted: %q", name)
			}
			if _, statErr := os.Lstat(
				filepath.Join(rt.Dir, "would-be-partial"),
			); !errors.Is(statErr, fs.ErrNotExist) {
				t.Fatalf("partial file exists or stat failed: %v", statErr)
			}
		})
	}
}

func TestMaterializeCannotCreateWindowsAlternateStream(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	if err := root.Materialize(rt, []FileSpec{
		{Name: "would-be-partial", Data: []byte("must not exist")},
		{Name: "base.txt:stream", Data: []byte("unsafe")},
	}); err == nil {
		t.Fatal("alternate stream name unexpectedly accepted")
	}
	for _, name := range []string{"would-be-partial", "base.txt"} {
		if _, err := os.Lstat(
			filepath.Join(rt.Dir, name),
		); !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("%q exists or stat failed: %v", name, err)
		}
	}

	streamPath, err := windows.UTF16PtrFromString(
		filepath.Join(rt.Dir, "base.txt:stream"),
	)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		streamPath,
		windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err == nil {
		_ = windows.CloseHandle(handle)
		t.Fatal("alternate stream was materialized")
	}
}

func TestOpenRootRejectsUnsafeWindowsDACL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;;FA;;;WD)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		t.Fatal(err)
	}

	if _, err := OpenRoot(path); err == nil {
		t.Fatal("world-writable DACL unexpectedly accepted")
	}
}

func TestWindowsAncestorDescriptorRejectsUntrustedOwner(t *testing.T) {
	sd, err := windows.SecurityDescriptorFromString(
		"O:WDD:P(A;;GR;;;WD)",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWindowsAncestorDescriptor(sd); err == nil {
		t.Fatal("descriptor with untrusted owner was accepted")
	}
}

func TestWindowsAncestorDescriptorRejectsUntrustedMutationGrants(
	t *testing.T,
) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, access := range []uint32{
		0x00010000, // DELETE
		0x00000040, // FILE_DELETE_CHILD
		0x00040000, // WRITE_DAC
		0x00080000, // WRITE_OWNER
	} {
		t.Run(fmt.Sprintf("%08x", access), func(t *testing.T) {
			sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
				"O:%[1]sD:P(A;;FA;;;%[1]s)(A;;0x%[2]08x;;;WD)",
				user.User.Sid.String(),
				access,
			))
			if err != nil {
				t.Fatal(err)
			}
			if err := validateWindowsAncestorDescriptor(sd); err == nil {
				t.Fatalf("untrusted mutation grant %#x was accepted", access)
			}
		})
	}
}

func TestWindowsAncestorDescriptorAcceptsUntrustedReadOnlyGrant(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"O:%[1]sD:P(A;;FA;;;%[1]s)(A;;GR;;;WD)",
		user.User.Sid.String(),
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := validateWindowsAncestorDescriptor(sd); err != nil {
		t.Fatalf("read-only ancestor grant was rejected: %v", err)
	}
}

func TestOpenRootRejectsUnsafeWindowsParentAuthority(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "unsafe-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	setWindowsAncestorDACL(t, parent, 0x00000040)
	if _, err := OpenRoot(filepath.Join(parent, "runtime")); err == nil {
		t.Fatal("parent granting FILE_DELETE_CHILD to Everyone was accepted")
	}
}

func TestOpenRootRejectsUnsafeWindowsAncestorAuthority(t *testing.T) {
	ancestor := filepath.Join(t.TempDir(), "unsafe-ancestor")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(ancestor, "private-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	setWindowsAncestorDACL(t, ancestor, 0x00010000)
	if _, err := OpenRoot(filepath.Join(parent, "runtime")); err == nil {
		t.Fatal("ancestor granting DELETE to Everyone was accepted")
	}
}

func TestCleanupLockedWindowsFileEntersQuarantine(t *testing.T) {
	root := openTestRoot(t)
	rt := prepareTestRuntime(t, root)
	path := filepath.Join(rt.Dir, "locked")
	pathPtr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(
		pathPtr,
		windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE,
		nil,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = windows.CloseHandle(handle)
	})

	err = root.Cleanup(context.Background(), rt)
	var runErr *RunError
	if !errors.As(err, &runErr) || runErr.Kind != ErrorCleanup {
		t.Fatalf("error=%T %v", err, err)
	}
	quarantine := filepath.Join(
		rootPathForTest(root),
		"quarantine-"+testRequestID,
	)
	info, err := os.Lstat(quarantine)
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Fatalf("quarantine mode=%v", info.Mode())
	}
}

func setWindowsAncestorDACL(t *testing.T, path string, access uint32) {
	t.Helper()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sd, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"D:P(A;;FA;;;%s)(A;;0x%08x;;;WD)",
		user.User.Sid.String(),
		access,
	))
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(
		path,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|
			windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		t.Fatal(err)
	}
}

func unsafeWindowsFilenames() []string {
	return []string{
		"file:stream",
		":stream",
		"bad<name",
		"bad>name",
		`bad"name`,
		"bad|name",
		"bad?name",
		"bad*name",
		"control\x01",
		"trailing.",
		"trailing ",
		"normal..",
		"CON",
		"con",
		"Con.txt",
		"CON .txt",
		"PRN.log",
		"AUX",
		"NUL.json",
		"COM1",
		"com9.txt",
		"LPT1",
		"lpt9.out",
		"COM¹",
		"com².txt",
		"LPT³",
		"CONIN$",
		"conout$.txt",
	}
}
