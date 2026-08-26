package configstore

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestLoadRejectsInvalidContextAndPathWithFixedErrors(t *testing.T) {
	t.Parallel()

	writer := NewWriter()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	for name, test := range map[string]struct {
		ctx  context.Context
		path string
	}{
		"nil context":      {ctx: nil, path: testConfigPath(t)},
		"canceled context": {ctx: canceled, path: testConfigPath(t)},
		"empty path":       {ctx: context.Background()},
		"relative path":    {ctx: context.Background(), path: "config.toml"},
		"NUL path":         {ctx: context.Background(), path: testConfigPath(t) + "\x00x"},
	} {
		test := test
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := writer.Load(test.ctx, test.path)
			if err == nil {
				t.Fatal("Load() error = nil")
			}
			if test.path != "" && strings.Contains(err.Error(), test.path) {
				t.Fatalf("Load() error leaked path: %q", err)
			}
		})
	}
}

func TestWindowsStoreComponentPolicyRejectsAmbiguousNames(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"config.toml", "AI CLI Gateway", "키.toml"} {
		if !safeWindowsStoreComponent(name) {
			t.Fatalf("safeWindowsStoreComponent(%q) = false", name)
		}
	}
	invalidUTF8 := string([]byte{0xff})
	for _, name := range []string{
		"", ".", "..", "trailing.", "trailing ", "CON", "CONIN$", "CONOUT$",
		"CON .txt", "COM1", "COM¹", "LPT³.log", "bad<name", "bad:name",
		"bad/name", `bad\name`, "bad|name", "bad?name", "bad*name", "bad\x1fname",
		strings.Repeat("a", 256), invalidUTF8,
	} {
		if safeWindowsStoreComponent(name) {
			t.Fatalf("safeWindowsStoreComponent(%q) = true", name)
		}
	}
}

func TestWindowsPrivateStorePolicyRejectsEveryUntrustedAllow(t *testing.T) {
	t.Parallel()

	const unsafeWrite = uint32(0x00000002)
	for _, mask := range []uint32{0, 0x00000001, 0x00000020, 0x80000000, 0x10000000} {
		if safeWindowsStoreUntrustedAllow(true, mask, unsafeWrite) {
			t.Fatalf("private policy accepted untrusted allow mask %#x", mask)
		}
	}
	if !safeWindowsStoreUntrustedAllow(false, 0x00000001, unsafeWrite) {
		t.Fatal("non-private ancestor policy rejected a read-only allow")
	}
	if safeWindowsStoreUntrustedAllow(false, unsafeWrite, unsafeWrite) {
		t.Fatal("non-private ancestor policy accepted an unsafe write allow")
	}
}

func TestWindowsStorePolicyIgnoresInheritOnlyAllows(t *testing.T) {
	t.Parallel()

	const unsafeWrite = uint32(0x00000002)
	if !safeWindowsStoreUntrustedACE(true, true, unsafeWrite, unsafeWrite) {
		t.Fatal("private policy rejected an inherit-only allow that does not apply to the object")
	}
	if !safeWindowsStoreUntrustedACE(false, true, unsafeWrite, unsafeWrite) {
		t.Fatal("ancestor policy rejected an inherit-only allow that does not apply to the object")
	}
	if safeWindowsStoreUntrustedACE(true, false, 0, unsafeWrite) {
		t.Fatal("private policy accepted an applicable untrusted allow")
	}
	if safeWindowsStoreUntrustedACE(false, false, unsafeWrite, unsafeWrite) {
		t.Fatal("ancestor policy accepted an applicable unsafe write allow")
	}
}

func TestSnapshotZeroValueIsOpaqueAndDefensive(t *testing.T) {
	t.Parallel()

	var snapshot Snapshot
	if snapshot.Exists() || snapshot.Path() != "" || snapshot.Bytes() != nil {
		t.Fatalf("zero Snapshot = exists %t path %q bytes %v", snapshot.Exists(), snapshot.Path(), snapshot.Bytes())
	}
	if snapshot.Bytes() != nil {
		t.Fatal("zero Snapshot Bytes() changed")
	}
}

func TestStoreErrorsAreClosedAndPathFree(t *testing.T) {
	t.Parallel()

	for _, err := range []error{ErrInvalidConfig, ErrUnsafePath, ErrStore} {
		if err == nil || errors.Unwrap(err) != nil || strings.Contains(err.Error(), "/") || strings.Contains(err.Error(), `\`) {
			t.Fatalf("store error is not fixed and path-free: %v", err)
		}
	}
}
