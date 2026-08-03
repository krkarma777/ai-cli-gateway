//go:build windows

package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"unsafe"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
	"golang.org/x/sys/windows"
)

func TestNormalizeWindowsInputPathCleansBeforeUseAndRejectsUnsafeForms(
	t *testing.T,
) {
	clean, key, err := normalizeWindowsInputPath(
		`C:\Trusted\never-walked\..\Bin\tool.exe`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if clean != `C:\Trusted\Bin\tool.exe` ||
		key != `c:\trusted\bin\tool.exe` {
		t.Fatalf("normalized input=(%q, %q)", clean, key)
	}

	for _, path := range []string{
		`relative\tool.exe`,
		`\\?\C:\Trusted\tool.exe`,
		`\\?\UNC\server\share\tool.exe`,
		`\\.\C:\Trusted\tool.exe`,
		`\??\C:\Trusted\tool.exe`,
		`\Device\HarddiskVolume3\Trusted\tool.exe`,
		`C:\Trusted\tool.exe:stream`,
		"C:\\Trusted\\tool.exe\x00tail",
	} {
		t.Run(path, func(t *testing.T) {
			if _, _, err := normalizeWindowsInputPath(path); err == nil {
				t.Fatalf("unsafe input %q was accepted", path)
			}
		})
	}
}

func TestNormalizeWindowsFinalPathAcceptsOnlyCanonicalDOSOrUNC(t *testing.T) {
	for _, test := range []struct {
		raw   string
		clean string
		key   string
	}{
		{
			raw:   `\\?\C:\Trusted\Bin\tool.exe`,
			clean: `C:\Trusted\Bin\tool.exe`,
			key:   `c:\trusted\bin\tool.exe`,
		},
		{
			raw:   `\\?\UNC\Server\Share\Bin\tool.exe`,
			clean: `\\Server\Share\Bin\tool.exe`,
			key:   `\\server\share\bin\tool.exe`,
		},
	} {
		t.Run(test.raw, func(t *testing.T) {
			clean, key, err := normalizeWindowsFinalPath(test.raw)
			if err != nil {
				t.Fatal(err)
			}
			if clean != test.clean || key != test.key {
				t.Fatalf("normalized final=(%q, %q), want (%q, %q)",
					clean, key, test.clean, test.key)
			}
		})
	}

	for _, path := range []string{
		`\\.\C:\Trusted\tool.exe`,
		`\Device\HarddiskVolume3\Trusted\tool.exe`,
		`C:\Trusted\tool.exe:stream`,
		"C:\\Trusted\\tool.exe\x00tail",
	} {
		if _, _, err := normalizeWindowsFinalPath(path); err == nil {
			t.Fatalf("unsafe final path %q was accepted", path)
		}
	}
}

func TestWindowsAncestorPathsIncludeRootsInLexicalOrder(t *testing.T) {
	for _, test := range []struct {
		path string
		want []string
	}{
		{
			path: `C:\Trusted\Bin\tool.exe`,
			want: []string{`C:\`, `C:\Trusted`, `C:\Trusted\Bin`},
		},
		{
			path: `\\Server\Share\Trusted\tool.exe`,
			want: []string{`\\Server\Share\`, `\\Server\Share\Trusted`},
		},
	} {
		t.Run(test.path, func(t *testing.T) {
			got, err := windowsAncestorPaths(test.path)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("ancestors=%q, want %q", got, test.want)
			}
		})
	}
}

func TestNormalizeWindowsFileEvidencePreservesReparseAndIdentity(t *testing.T) {
	info := windows.ByHandleFileInformation{
		FileAttributes:     windows.FILE_ATTRIBUTE_DIRECTORY | windows.FILE_ATTRIBUTE_REPARSE_POINT,
		VolumeSerialNumber: 7,
		FileIndexHigh:      0x11223344,
		FileIndexLow:       0x55667788,
	}
	object, reparse, identity := normalizeWindowsFileEvidence(info)
	if object != aclObjectDirectory || !reparse {
		t.Fatalf("object/reparse=%v/%t", object, reparse)
	}
	if identity != (windowsFileID{
		Volume: 7,
		Index:  0x1122334455667788,
	}) {
		t.Fatalf("identity=%+v", identity)
	}

	info.FileAttributes = windows.FILE_ATTRIBUTE_DEVICE
	object, reparse, _ = normalizeWindowsFileEvidence(info)
	if object != aclObjectUnknown || reparse {
		t.Fatalf("device evidence=%v/%t", object, reparse)
	}
}

func TestNormalizeWindowsSecurityDescriptorAcquiresOwnerDACLAndACEs(
	t *testing.T,
) {
	descriptor, err := windows.SecurityDescriptorFromString(
		`O:SYD:(A;;GRGX;;;SY)`,
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := normalizeWindowsSecurityDescriptor(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	if !evidence.Supported || !evidence.DACLPresent || evidence.DACLNull ||
		evidence.OwnerSID != aclLocalSystemSID || len(evidence.ACEs) != 1 {
		t.Fatalf("descriptor evidence=%+v", evidence)
	}
	ace := evidence.ACEs[0]
	if ace.Kind != aclACEAllow || ace.Flags != 0 ||
		ace.Mask != aclGenericRead|aclGenericExecute ||
		ace.SID != aclLocalSystemSID {
		t.Fatalf("ACE=%+v", ace)
	}
}

func TestAcquireWindowsTokenSnapshotUsesEffectiveTokenEvidence(t *testing.T) {
	effective := windows.GetCurrentThreadEffectiveToken()
	restricted, err := effective.IsRestricted()
	if err != nil {
		t.Fatal(err)
	}
	token, err := acquireWindowsTokenSnapshot()
	if restricted {
		if err == nil {
			t.Fatal("restricted effective token was accepted")
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	effectiveUser, err := effective.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	if effectiveUser == nil || effectiveUser.User.Sid == nil ||
		token.UserSID != effectiveUser.User.Sid.String() {
		t.Fatalf("token user=%q, want effective thread user", token.UserSID)
	}
	if !token.Supported || !validACLSID(token.UserSID) {
		t.Fatalf("token=%+v", token)
	}
	for _, group := range token.Groups {
		if !validACLSID(group.SID) || group.Enabled && group.DenyOnly {
			t.Fatalf("group=%+v", group)
		}
	}
}

func TestAcquireWindowsTokenSnapshotWorksUnderThreadImpersonation(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.ImpersonateSelf(windows.SecurityImpersonation); err != nil {
		t.Skipf("thread impersonation unavailable: %v", err)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			t.Error(err)
		}
	}()

	effective := windows.GetCurrentThreadEffectiveToken()
	effectiveUser, err := effective.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := acquireWindowsTokenSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	if effectiveUser == nil || effectiveUser.User.Sid == nil ||
		snapshot.UserSID != effectiveUser.User.Sid.String() {
		t.Fatalf("snapshot user=%q, want impersonation token user", snapshot.UserSID)
	}
}

func TestAcquireWindowsTokenSnapshotRejectsRestrictedEffectiveThreadToken(
	t *testing.T,
) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	var processToken windows.Token
	if err := windows.OpenProcessToken(
		windows.CurrentProcess(),
		windows.TOKEN_DUPLICATE|windows.TOKEN_QUERY,
		&processToken,
	); err != nil {
		t.Fatal(err)
	}
	defer processToken.Close() //nolint:errcheck // Test cleanup.
	processRestricted, err := processToken.IsRestricted()
	if err != nil {
		t.Fatal(err)
	}
	if processRestricted {
		t.Skip("process token is already restricted")
	}

	restricted, err := newRestrictedWindowsImpersonationToken(processToken)
	if err != nil {
		t.Fatal(err)
	}
	defer restricted.Close() //nolint:errcheck // Test cleanup.
	if err := windows.SetThreadToken(nil, restricted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			t.Error(err)
		}
	}()

	effectiveRestricted, err := windows.GetCurrentThreadEffectiveToken().IsRestricted()
	if err != nil {
		t.Fatal(err)
	}
	if !effectiveRestricted {
		t.Fatal("fixture effective token is not restricted")
	}
	if _, err := acquireWindowsTokenSnapshot(); err == nil {
		t.Fatal("restricted effective thread token was accepted")
	}
}

func TestAcquireWindowsPathSnapshotPreservesNativeReparseEvidence(t *testing.T) {
	root := t.TempDir()
	fileTarget := filepath.Join(root, "target-file")
	if err := os.WriteFile(fileTarget, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name   string
		target string
		object aclObject
		policy windowsACLPolicy
	}{
		{
			name:   "file symlink",
			target: fileTarget,
			object: aclObjectFile,
			policy: windowsExecutablePolicy,
		},
		{
			name:   "directory symlink or junction evidence",
			target: t.TempDir(),
			object: aclObjectDirectory,
			policy: windowsPathDirectoryPolicy,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			link := filepath.Join(t.TempDir(), "native-reparse")
			if err := os.Symlink(test.target, link); err != nil {
				t.Skipf("native reparse creation unavailable: %v", err)
			}
			token, err := acquireWindowsTokenSnapshot()
			if err != nil {
				t.Fatal(err)
			}
			snapshot, _, err := acquireWindowsPathSnapshot(link, token)
			if err != nil {
				t.Fatal(err)
			}
			if !snapshot.Reparse || snapshot.Object != test.object {
				t.Fatalf("reparse snapshot=%+v", snapshot)
			}
			if err := evaluateWindowsACL(snapshot, test.policy); err == nil {
				t.Fatal("native reparse evidence was accepted")
			}
		})
	}
}

func TestTrustedWindowsFixturesPassCompleteDoctorPathPolicy(t *testing.T) {
	executableDirectory := testutil.TrustedTempDir(t)
	executable := filepath.Join(executableDirectory, "provider.exe")
	testutil.WriteTrustedFile(t, executable, []byte("fixture"), 0o700)
	configHome := testutil.TrustedTempDir(t)
	credential := filepath.Join(configHome, "service.json")
	testutil.WriteTrustedFile(t, credential, []byte("fixture"), 0o600)

	for name, validate := range map[string]func() pathDisposition{
		"executable": func() pathDisposition {
			_, disposition := validateExecutablePath(executable)
			return disposition
		},
		"config home": func() pathDisposition {
			_, disposition := validateConfigHomePath(configHome)
			return disposition
		},
		"credential": func() pathDisposition {
			_, disposition := validateCredentialPath(credential)
			return disposition
		},
	} {
		t.Run(name, func(t *testing.T) {
			if disposition := validate(); disposition != pathSafe {
				t.Fatalf("trusted fixture disposition = %v, want pathSafe", disposition)
			}
		})
	}
}

func TestNormalizeWindowsSystemDirectoriesRequiresAPIDerivedSystem32(
	t *testing.T,
) {
	root, system32, err := normalizeWindowsSystemDirectories(
		`C:\Windows\.`,
		`C:\Windows\System32\.`,
	)
	if err != nil {
		t.Fatal(err)
	}
	if root != `C:\Windows` || system32 != `C:\Windows\System32` {
		t.Fatalf("directories=%q/%q", root, system32)
	}

	for _, system := range []string{
		`D:\Windows\System32`,
		`C:\Windows\SysWOW64`,
		`\\?\C:\Windows\System32`,
		`C:\Windows\System32:stream`,
	} {
		if _, _, err := normalizeWindowsSystemDirectories(
			`C:\Windows`,
			system,
		); err == nil {
			t.Fatalf("system directory %q was accepted", system)
		}
	}
}

func TestClassifyWindowsPathErrorDistinguishesMissingFromUnsafe(t *testing.T) {
	for _, missing := range []error{
		windows.ERROR_FILE_NOT_FOUND,
		windows.ERROR_PATH_NOT_FOUND,
	} {
		if got := classifyWindowsPathError(missing); got != pathMissing {
			t.Fatalf("classifyWindowsPathError(%v)=%v", missing, got)
		}
	}
	if got := classifyWindowsPathError(windows.ERROR_ACCESS_DENIED); got != pathUnsafe {
		t.Fatalf("access denied disposition=%v", got)
	}
}

func newRestrictedWindowsImpersonationToken(
	processToken windows.Token,
) (windows.Token, error) {
	var impersonation windows.Token
	if err := windows.DuplicateTokenEx(
		processToken,
		windows.TOKEN_DUPLICATE|windows.TOKEN_IMPERSONATE|windows.TOKEN_QUERY,
		nil,
		windows.SecurityImpersonation,
		windows.TokenImpersonation,
		&impersonation,
	); err != nil {
		return 0, err
	}
	defer impersonation.Close() //nolint:errcheck // Intermediate test token.
	user, err := impersonation.GetTokenUser()
	if err != nil {
		return 0, fmt.Errorf("read impersonation token user: %w", err)
	}
	if user == nil || user.User.Sid == nil {
		return 0, fmt.Errorf("read impersonation token user: missing SID")
	}
	restriction := windows.SIDAndAttributes{Sid: user.User.Sid}
	var restricted windows.Token
	procedure := windows.NewLazySystemDLL("advapi32.dll").NewProc(
		"CreateRestrictedToken",
	)
	success, _, callErr := procedure.Call(
		uintptr(impersonation),
		0,
		0,
		0,
		0,
		0,
		1,
		uintptr(unsafe.Pointer(&restriction)),
		uintptr(unsafe.Pointer(&restricted)),
	)
	runtime.KeepAlive(restriction)
	if success == 0 {
		return 0, fmt.Errorf("create restricted token: %w", callErr)
	}
	return restricted, nil
}
