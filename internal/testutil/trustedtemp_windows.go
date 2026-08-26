//go:build windows

package testutil

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

const trustedTempAttempts = 100

// TrustedTempDir creates a TokenUser-owned fixture with a protected inheritable
// DACL below the current user's local cache directory. Hosted Windows runners
// can place os.TempDir on a shared work drive whose ancestors intentionally
// grant untrusted delete access, which strict path-policy tests must reject.
func TrustedTempDir(t testing.TB) string {
	t.Helper()
	cache, err := os.UserCacheDir()
	if err != nil {
		t.Fatalf("resolve trusted fixture cache: %v", err)
	}
	parent, err := filepath.Abs(cache)
	if err != nil {
		t.Fatalf("resolve trusted fixture parent: %v", err)
	}
	attributes, _, err := trustedWindowsSecurityAttributes(true)
	if err != nil {
		t.Fatalf("construct trusted fixture security: %v", err)
	}
	for range trustedTempAttempts {
		var random [16]byte
		if _, err := rand.Read(random[:]); err != nil {
			t.Fatalf("generate trusted fixture name: %v", err)
		}
		path := filepath.Join(
			parent,
			".ai-cli-gateway-test-"+hex.EncodeToString(random[:]),
		)
		pathPointer, err := windows.UTF16PtrFromString(path)
		if err != nil {
			t.Fatalf("encode trusted fixture path: %v", err)
		}
		err = windows.CreateDirectory(pathPointer, attributes)
		if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			continue
		}
		if err != nil {
			t.Fatalf("create trusted fixture directory: %v", err)
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(path); err != nil {
				t.Errorf("remove trusted fixture directory: %v", err)
			}
		})
		return path
	}
	t.Fatal("exhausted trusted fixture directory names")
	return ""
}

// CreateTrustedDirectory creates every missing component below an existing
// absolute path with a TokenUser owner and a protected inheritable DACL.
func CreateTrustedDirectory(t testing.TB, path string) {
	t.Helper()
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		t.Fatalf("trusted fixture directory path is not absolute: %q", path)
	}

	current := clean
	missing := make([]string, 0, 4)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if !info.IsDir() {
				t.Fatalf("trusted fixture parent is not a directory: %q", current)
			}
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			t.Fatalf("inspect trusted fixture directory %q: %v", current, err)
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			t.Fatalf("trusted fixture directory has no existing ancestor: %q", path)
		}
		current = parent
	}

	attributes, descriptor, err := trustedWindowsSecurityAttributes(true)
	if err != nil {
		t.Fatalf("construct trusted fixture directory security: %v", err)
	}
	for index := len(missing) - 1; index >= 0; index-- {
		component := missing[index]
		pointer, err := windows.UTF16PtrFromString(component)
		if err != nil {
			t.Fatalf("encode trusted fixture directory %q: %v", component, err)
		}
		if err := windows.CreateDirectory(pointer, attributes); err != nil {
			t.Fatalf("create trusted fixture directory %q: %v", component, err)
		}
	}
	runtime.KeepAlive(descriptor)
}

// WriteTrustedFile creates a TokenUser-owned file with a protected DACL.
func WriteTrustedFile(
	t testing.TB,
	path string,
	payload []byte,
	_ fs.FileMode,
) {
	t.Helper()
	attributes, _, err := trustedWindowsSecurityAttributes(false)
	if err != nil {
		t.Fatalf("construct trusted fixture file security: %v", err)
	}
	pathPointer, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("encode trusted fixture file path: %v", err)
	}
	handle, err := windows.CreateFile(
		pathPointer,
		windows.GENERIC_WRITE,
		0,
		attributes,
		windows.CREATE_NEW,
		windows.FILE_ATTRIBUTE_NORMAL,
		0,
	)
	if err != nil {
		t.Fatalf("create trusted fixture file: %v", err)
	}
	file := os.NewFile(uintptr(handle), path)
	if file == nil {
		_ = windows.CloseHandle(handle)
		t.Fatal("wrap trusted fixture file handle")
	}
	written, writeErr := file.Write(payload)
	if writeErr == nil && written != len(payload) {
		writeErr = io.ErrShortWrite
	}
	if err := errors.Join(writeErr, file.Sync(), file.Close()); err != nil {
		t.Fatalf("write trusted fixture file: %v", err)
	}
}

func trustedWindowsSecurityAttributes(
	inherit bool,
) (*windows.SecurityAttributes, *windows.SECURITY_DESCRIPTOR, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, nil, err
	}
	flags := ""
	if inherit {
		flags = "OICI"
	}
	sddl := fmt.Sprintf(
		"O:%[1]sD:P(A;%[2]s;FA;;;%[1]s)(A;%[2]s;FA;;;SY)(A;%[2]s;FA;;;BA)",
		user.User.Sid.String(),
		flags,
	)
	descriptor, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, nil, err
	}
	attributes := &windows.SecurityAttributes{
		Length:             uint32(unsafe.Sizeof(windows.SecurityAttributes{})),
		SecurityDescriptor: descriptor,
	}
	return attributes, descriptor, nil
}
