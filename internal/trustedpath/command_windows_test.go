//go:build windows

package trustedpath

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

func TestOpenTrustedCommandWindowsRetainsIdentityAndBoundedContent(t *testing.T) {
	directory := testutil.TrustedTempDir(t)
	path := filepath.Join(directory, "provider.cmd")
	payload := []byte("closed shim fixture")
	testutil.WriteTrustedFile(t, path, payload, 0o700)

	inspection, err := OpenCommandPath(
		path,
		CommandBoundedContent,
		int64(len(payload)),
	)
	if err != nil {
		t.Fatalf("OpenCommandPath() error = %v", err)
	}
	if !slices.Equal(inspection.Bytes(), payload) {
		t.Fatalf("Bytes() = %q, want %q", inspection.Bytes(), payload)
	}
	metadata, ok := InspectionPath(inspection)
	if !ok || metadata.Clean != path || metadata.Resolved == "" || metadata.CanonicalKey == "" {
		t.Fatalf("InspectionPath() = %+v/%v", metadata, ok)
	}
	if err := inspection.Revalidate(); err != nil {
		t.Fatalf("Revalidate() error = %v", err)
	}
	if err := inspection.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestOpenTrustedCommandWindowsPreservesDoctorDispositionTable(t *testing.T) {
	directory := testutil.TrustedTempDir(t)
	safe := filepath.Join(directory, "provider.exe")
	testutil.WriteTrustedFile(t, safe, []byte("fixture"), 0o700)
	missing := filepath.Join(directory, "missing.exe")

	for _, test := range []struct {
		name    string
		path    string
		wantErr error
	}{
		{name: "safe", path: safe},
		{name: "missing", path: missing, wantErr: ErrMissing},
		{name: "relative", path: `relative\provider.exe`, wantErr: ErrUnsafe},
		{name: "device namespace", path: `\\?\C:\Trusted\provider.exe`, wantErr: ErrUnsafe},
		{name: "alternate stream", path: safe + ":stream", wantErr: ErrUnsafe},
		{name: "NUL", path: safe + "\x00tail", wantErr: ErrUnsafe},
	} {
		t.Run(test.name, func(t *testing.T) {
			inspection, err := OpenCommandPath(test.path, CommandIdentityOnly, 0)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("OpenCommandPath() error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := inspection.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestOpenTrustedCommandWindowsRejectsReparseAndReplacement(t *testing.T) {
	directory := testutil.TrustedTempDir(t)
	target := filepath.Join(directory, "target.exe")
	testutil.WriteTrustedFile(t, target, []byte("target"), 0o700)
	link := filepath.Join(directory, "link.exe")
	if err := os.Symlink(target, link); err == nil {
		if inspection, openErr := OpenCommandPath(link, CommandIdentityOnly, 0); openErr == nil {
			_ = inspection.Close()
			t.Fatal("reparse command was accepted")
		}
	}

	inspection, err := OpenCommandPath(target, CommandIdentityOnly, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer inspection.Close() //nolint:errcheck // Test cleanup after assertion.
	old := filepath.Join(directory, "old.exe")
	if err := os.Rename(target, old); err != nil {
		t.Fatal(err)
	}
	testutil.WriteTrustedFile(t, target, []byte("replacement"), 0o700)
	if err := inspection.Revalidate(); !errors.Is(err, ErrUnsafe) {
		t.Fatalf("Revalidate() error = %v, want ErrUnsafe", err)
	}
}

func TestOpenTrustedCommandWindowsACLMatchesDoctorAuthorityPolicy(t *testing.T) {
	userSID := "S-1-5-21-1000"
	identity := windowsCommandFileID{volume: 7, index: 11}
	safe := windowsCommandEvidence{
		descriptor: true,
		dacl:       true,
		object:     windowsCommandObjectFile,
		ownerSID:   userSID,
		token:      windowsCommandToken{userSID: userSID},
		aces: []windowsCommandACE{{
			kind: windowsCommandACEAllow,
			mask: windowsCommandGenericAll,
			sid:  userSID,
		}},
		openedID:   identity,
		reopenedID: identity,
		canonical:  `C:\Trusted\provider.exe`,
	}
	for _, owner := range []string{
		userSID,
		windowsCommandLocalSystemSID,
		windowsCommandAdminsSID,
		windowsCommandInstallerSID,
	} {
		evidence := safe
		evidence.ownerSID = owner
		if err := evaluateWindowsCommandEvidence(
			evidence,
			windowsCommandObjectFile,
			windowsCommandLeafRequired,
			windowsCommandLeafForbidden,
		); err != nil {
			t.Fatalf("trusted owner %q rejected: %v", owner, err)
		}
	}

	for _, mutate := range []func(*windowsCommandEvidence){
		func(e *windowsCommandEvidence) { e.ownerSID = "S-1-5-21-9999" },
		func(e *windowsCommandEvidence) { e.reparse = true },
		func(e *windowsCommandEvidence) { e.reopenedID.index++ },
		func(e *windowsCommandEvidence) {
			e.aces = append(e.aces, windowsCommandACE{
				kind: windowsCommandACEAllow,
				mask: windowsCommandWriteData,
				sid:  "S-1-1-0",
			})
		},
	} {
		evidence := safe
		evidence.aces = append([]windowsCommandACE(nil), safe.aces...)
		mutate(&evidence)
		if err := evaluateWindowsCommandEvidence(
			evidence,
			windowsCommandObjectFile,
			windowsCommandLeafRequired,
			windowsCommandLeafForbidden,
		); !errors.Is(err, ErrUnsafe) {
			t.Fatalf("unsafe evidence accepted: %+v", evidence)
		}
	}
}

func TestValidateWindowsCommandFreshEvidenceRejectsSecurityMutation(t *testing.T) {
	userSID := "S-1-5-21-1000"
	identity := windowsCommandFileID{volume: 7, index: 11}
	canonical := `C:\Trusted\provider.exe`
	safe := windowsCommandEvidence{
		descriptor: true,
		dacl:       true,
		object:     windowsCommandObjectFile,
		ownerSID:   userSID,
		token:      windowsCommandToken{userSID: userSID},
		aces: []windowsCommandACE{{
			kind: windowsCommandACEAllow,
			mask: windowsCommandGenericAll,
			sid:  userSID,
		}},
		openedID:   identity,
		reopenedID: identity,
		canonical:  canonical,
	}
	key := mustWindowsCommandKey(canonical)
	if err := validateWindowsCommandFreshEvidence(safe, identity, key); err != nil {
		t.Fatalf("safe fresh evidence rejected: %v", err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*windowsCommandEvidence)
	}{
		{name: "reparse", mutate: func(e *windowsCommandEvidence) { e.reparse = true }},
		{name: "untrusted owner", mutate: func(e *windowsCommandEvidence) { e.ownerSID = "S-1-5-21-9999" }},
		{name: "null DACL", mutate: func(e *windowsCommandEvidence) { e.daclNull = true }},
		{name: "changed identity", mutate: func(e *windowsCommandEvidence) { e.reopenedID.index++ }},
		{name: "changed path", mutate: func(e *windowsCommandEvidence) { e.canonical = `C:\Other\provider.exe` }},
		{
			name: "untrusted write grant",
			mutate: func(e *windowsCommandEvidence) {
				e.aces = append(e.aces, windowsCommandACE{
					kind: windowsCommandACEAllow,
					mask: windowsCommandWriteData,
					sid:  "S-1-1-0",
				})
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			fresh := safe
			fresh.aces = append([]windowsCommandACE(nil), safe.aces...)
			test.mutate(&fresh)
			if err := validateWindowsCommandFreshEvidence(fresh, identity, key); !errors.Is(err, ErrUnsafe) {
				t.Fatalf("unsafe fresh evidence accepted: %+v", fresh)
			}
		})
	}
}
