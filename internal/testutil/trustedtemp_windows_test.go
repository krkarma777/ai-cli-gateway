//go:build windows

package testutil

import (
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

func TestTrustedWindowsFixturesPinTokenUserAndProtectedDACL(t *testing.T) {
	directory := TrustedTempDir(t)
	file := filepath.Join(directory, "private.json")
	WriteTrustedFile(t, file, []byte("fixture"), 0o600)

	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{directory, file} {
		descriptor, err := windows.GetNamedSecurityInfo(
			path,
			windows.SE_FILE_OBJECT,
			windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION,
		)
		if err != nil {
			t.Fatal(err)
		}
		owner, _, err := descriptor.Owner()
		if err != nil {
			t.Fatal(err)
		}
		control, _, err := descriptor.Control()
		if err != nil {
			t.Fatal(err)
		}
		if owner == nil || !owner.Equals(user.User.Sid) {
			t.Fatalf("fixture %q owner = %v, want TokenUser", path, owner)
		}
		if control&windows.SE_DACL_PROTECTED == 0 {
			t.Fatalf("fixture %q DACL is not protected", path)
		}
	}
}
