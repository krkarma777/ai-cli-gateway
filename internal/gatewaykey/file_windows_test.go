//go:build windows

package gatewaykey

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
	"golang.org/x/sys/windows"
)

func TestLoadFileAcceptsOwnerOnlyLocalRegularFile(t *testing.T) {
	path := writeWindowsKeyFile(t, testKey+"\n")
	snapshot, err := LoadFile(path, nil)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if !snapshot.Valid() || !snapshot.Enabled() || !snapshot.Matches(testKey) {
		t.Fatal("LoadFile() did not return the expected enabled snapshot")
	}
}

func TestLoadFileRejectsWindowsADSAndUnsafeDriveTypesAtPolicyBoundary(t *testing.T) {
	localDrive := func(root *uint16) uint32 {
		if got := windows.UTF16PtrToString(root); got != `C:\` {
			t.Fatalf("drive root = %q, want C:\\\\", got)
		}
		return windows.DRIVE_FIXED
	}
	for _, path := range []string{
		`C:\safe\gateway.key:stream`,
		`C:\safe:component\gateway.key`,
		`C:\safe\gate:way.key`,
	} {
		if _, ok := cleanWindowsKeyPathWithDriveType(path, localDrive); ok {
			t.Fatalf("path policy accepted ADS/component colon path %q", path)
		}
	}

	for _, driveType := range []uint32{
		windows.DRIVE_UNKNOWN,
		windows.DRIVE_NO_ROOT_DIR,
		windows.DRIVE_REMOVABLE,
		windows.DRIVE_REMOTE,
		windows.DRIVE_CDROM,
		windows.DRIVE_RAMDISK,
	} {
		if _, ok := cleanWindowsKeyPathWithDriveType(
			`C:\safe\gateway.key`,
			func(*uint16) uint32 { return driveType },
		); ok {
			t.Fatalf("path policy accepted drive type %d", driveType)
		}
	}
	if got, ok := cleanWindowsKeyPathWithDriveType(
		`C:\safe\gateway.key`,
		func(*uint16) uint32 { return windows.DRIVE_FIXED },
	); !ok || got != `C:\safe\gateway.key` {
		t.Fatalf("fixed local path = %q, %t", got, ok)
	}
}

func TestLoadFileRejectsWindowsReparseEvidenceAtPolicyBoundary(t *testing.T) {
	valid := windows.ByHandleFileInformation{
		VolumeSerialNumber: 7,
		FileIndexLow:       11,
		NumberOfLinks:      1,
	}
	if !safeWindowsKeyNativeEvidence(windows.FILE_TYPE_DISK, valid, false) {
		t.Fatal("baseline regular disk evidence was rejected")
	}
	reparse := valid
	reparse.FileAttributes |= windows.FILE_ATTRIBUTE_REPARSE_POINT
	if safeWindowsKeyNativeEvidence(windows.FILE_TYPE_DISK, reparse, false) {
		t.Fatal("reparse attribute evidence was accepted")
	}
}

func TestLoadFileWindowsStableMetadataPolicy(t *testing.T) {
	valid := windows.ByHandleFileInformation{
		VolumeSerialNumber: 3,
		FileIndexLow:       5,
		NumberOfLinks:      1,
		FileSizeLow:        65,
		LastWriteTime: windows.Filetime{
			LowDateTime:  101,
			HighDateTime: 202,
		},
	}
	if !sameWindowsKeyMetadata(valid, valid) {
		t.Fatal("identical metadata was reported unstable")
	}
	changedSize := valid
	changedSize.FileSizeLow++
	if sameWindowsKeyMetadata(valid, changedSize) {
		t.Fatal("size mutation was reported stable")
	}
	changedWriteTime := valid
	changedWriteTime.LastWriteTime.LowDateTime++
	if sameWindowsKeyMetadata(valid, changedWriteTime) {
		t.Fatal("last-write mutation was reported stable")
	}
}

func TestLoadFileWindowsClosedDACLPolicy(t *testing.T) {
	const (
		userSID     = "S-1-5-21-1"
		everyoneSID = "S-1-1-0"
	)
	token := windowsKeyToken{userSID: userSID}
	valid := windowsKeyACLEvidence{
		descriptorSupported: true,
		daclPresent:         true,
		daclProtected:       true,
		ownerSID:            userSID,
		aces: []windowsKeyACE{{
			kind: windowsKeyACEAllow,
			mask: windowsACLReadData | windowsACLReadAttributes,
			sid:  userSID,
		}},
	}
	if !evaluateWindowsKeyACL(valid, token, false) {
		t.Fatal("owner-only closed leaf DACL was rejected")
	}

	tests := []struct {
		name   string
		mutate func(*windowsKeyACLEvidence)
	}{
		{name: "wrong owner", mutate: func(e *windowsKeyACLEvidence) { e.ownerSID = everyoneSID }},
		{name: "DACL absent", mutate: func(e *windowsKeyACLEvidence) { e.daclPresent = false }},
		{name: "null DACL", mutate: func(e *windowsKeyACLEvidence) { e.daclNull = true }},
		{name: "inherited leaf DACL", mutate: func(e *windowsKeyACLEvidence) { e.daclProtected = false }},
		{name: "deny owner read", mutate: func(e *windowsKeyACLEvidence) {
			e.aces = append([]windowsKeyACE{{
				kind: windowsKeyACEDeny,
				mask: windowsACLReadData,
				sid:  userSID,
			}}, e.aces...)
		}},
		{name: "allow everyone read", mutate: func(e *windowsKeyACLEvidence) {
			e.aces = append(e.aces, windowsKeyACE{
				kind: windowsKeyACEAllow,
				mask: windowsACLReadData,
				sid:  everyoneSID,
			})
		}},
		{name: "inherited allow everyone read", mutate: func(e *windowsKeyACLEvidence) {
			e.aces = append(e.aces, windowsKeyACE{
				kind:  windowsKeyACEAllow,
				flags: windowsACLACEInherited,
				mask:  windowsACLReadData,
				sid:   everyoneSID,
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			candidate := valid
			candidate.aces = append([]windowsKeyACE(nil), valid.aces...)
			tt.mutate(&candidate)
			if evaluateWindowsKeyACL(candidate, token, false) {
				t.Fatal("unsafe leaf DACL was accepted")
			}
		})
	}
}

func TestLoadFileRejectsUnsafeWindowsAncestorDACL(t *testing.T) {
	directory := testutil.TrustedTempDir(t)
	ancestor := filepath.Join(directory, "unsafe")
	if err := os.Mkdir(ancestor, 0o700); err != nil {
		t.Fatalf("create ancestor: %v", err)
	}
	path := filepath.Join(ancestor, "gateway.key")
	testutil.WriteTrustedFile(t, path, []byte(testKey+"\n"), 0o600)

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatalf("get token user: %v", err)
	}
	descriptor, err := windows.SecurityDescriptorFromString(fmt.Sprintf(
		"D:P(A;;FA;;;%s)(A;;FA;;;WD)",
		user.User.Sid.String(),
	))
	if err != nil {
		t.Fatalf("construct unsafe DACL: %v", err)
	}
	dacl, _, err := descriptor.DACL()
	if err != nil {
		t.Fatalf("extract unsafe DACL: %v", err)
	}
	if err := windows.SetNamedSecurityInfo(
		ancestor,
		windows.SE_FILE_OBJECT,
		windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION,
		nil,
		nil,
		dacl,
		nil,
	); err != nil {
		t.Fatalf("install unsafe DACL: %v", err)
	}

	assertWindowsLoadUnavailable(t, path, nil)
}

func TestLoadFileRejectsWindowsPathShapeAndNonRegularObjects(t *testing.T) {
	trusted := testutil.TrustedTempDir(t)
	directory := filepath.Join(trusted, "directory.key")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("create directory fixture: %v", err)
	}
	for _, tt := range []struct {
		name string
		path string
	}{
		{name: "empty", path: ""},
		{name: "relative", path: `relative\gateway.key`},
		{name: "drive relative", path: `C:gateway.key`},
		{name: "rooted without drive", path: `\gateway.key`},
		{name: "UNC", path: `\\server\share\gateway.key`},
		{name: "device", path: `\\.\NUL`},
		{name: "directory", path: directory},
	} {
		t.Run(tt.name, func(t *testing.T) {
			assertWindowsLoadUnavailable(t, tt.path, nil)
		})
	}
}

func TestLoadFileRejectsWindowsHardLinksReparseAndIdentityCollision(t *testing.T) {
	t.Run("hard link", func(t *testing.T) {
		path := writeWindowsKeyFile(t, testKey+"\n")
		if err := os.Link(path, path+".alias"); err != nil {
			t.Fatalf("create hard link: %v", err)
		}
		assertWindowsLoadUnavailable(t, path, nil)
	})

	t.Run("reparse leaf", func(t *testing.T) {
		path := writeWindowsKeyFile(t, testKey+"\n")
		link := path + ".link"
		if err := os.Symlink(path, link); err != nil {
			t.Skipf("creating a Windows symlink requires host support: %v", err)
		}
		assertWindowsLoadUnavailable(t, link, nil)
	})

	t.Run("distinct identity", func(t *testing.T) {
		path := writeWindowsKeyFile(t, testKey+"\n")
		identity, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat key: %v", err)
		}
		assertWindowsLoadUnavailable(t, path, []fs.FileInfo{nil, identity})
	})
}

func TestLoadFileRejectsWindowsMalformedContent(t *testing.T) {
	assertWindowsLoadUnavailable(t, writeWindowsKeyFile(t, "not-a-key\n"), nil)
}

func TestLoadFileParsesWindowsRetainedHandleOnceAndRejectsReplacement(t *testing.T) {
	path := writeWindowsKeyFile(t, testKey+"\n")
	replacement := writeWindowsKeyFile(t, strings.Repeat("1", 64)+"\n")
	calls := 0

	snapshot, err := loadFile(path, nil, func(reader io.Reader) (Snapshot, error) {
		calls++
		if err := os.Rename(replacement, path); err != nil {
			t.Skipf("host sharing policy did not permit replacement: %v", err)
		}
		return Parse(reader)
	})
	if err != ErrUnavailable {
		t.Fatalf("loadFile() error = %v, want ErrUnavailable", err)
	}
	if snapshot.Valid() {
		t.Fatal("loadFile() returned valid snapshot after path replacement")
	}
	if calls != 1 {
		t.Fatalf("parser calls = %d, want 1", calls)
	}
}

func TestLoadFileRejectsWindowsMutationRestoredAfterParse(t *testing.T) {
	path := writeWindowsKeyFile(t, testKey+"\n")
	calls := 0

	snapshot, err := loadFile(path, nil, func(reader io.Reader) (Snapshot, error) {
		calls++
		file, ok := reader.(*os.File)
		if !ok {
			t.Fatalf("parser reader type = %T, want *os.File", reader)
		}
		mutated := strings.Repeat("1", 64) + "\n"
		if err := os.WriteFile(path, []byte(mutated), 0o600); err != nil {
			return Snapshot{}, ErrUnavailable
		}
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			t.Fatalf("rewind retained handle: %v", err)
		}
		parsed, parseErr := Parse(file)
		if err := os.WriteFile(path, []byte(testKey+"\n"), 0o600); err != nil {
			t.Fatalf("restore retained file: %v", err)
		}
		return parsed, parseErr
	})
	if err != ErrUnavailable {
		t.Fatalf("loadFile() error = %v, want ErrUnavailable", err)
	}
	if snapshot.Valid() {
		t.Fatal("loadFile() returned a snapshot parsed from transient content")
	}
	if calls != 1 {
		t.Fatalf("parser calls = %d, want 1", calls)
	}
}

func assertWindowsLoadUnavailable(t *testing.T, path string, distinct []fs.FileInfo) {
	t.Helper()
	snapshot, err := LoadFile(path, distinct)
	if err != ErrUnavailable {
		t.Fatalf("LoadFile() error = %v, want exact ErrUnavailable", err)
	}
	if snapshot.Valid() || snapshot.Matches(testKey) {
		t.Fatal("LoadFile() failure returned an authorizing snapshot")
	}
}

func writeWindowsKeyFile(t *testing.T, payload string) string {
	t.Helper()
	directory := testutil.TrustedTempDir(t)
	path := filepath.Join(directory, "gateway.key")
	testutil.WriteTrustedFile(t, path, []byte(payload), 0o600)
	return path
}
