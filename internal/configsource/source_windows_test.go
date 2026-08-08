//go:build windows

package configsource

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func restoreSourceModTime(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	path16, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatalf("encode source path: %v", err)
	}
	handle, err := windows.CreateFile(
		path16,
		windows.FILE_WRITE_ATTRIBUTES,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil,
		windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT,
		0,
	)
	if err != nil {
		t.Fatalf("open source mtime handle: %v", err)
	}
	defer windows.CloseHandle(handle) //nolint:errcheck // Test cleanup reports through the assertion path.
	filetime := windows.NsecToFiletime(modTime.UnixNano())
	if err := windows.SetFileTime(handle, nil, &filetime, &filetime); err != nil {
		t.Fatalf("restore source mtime: %v", err)
	}
}

func TestSameWindowsSourceIdentityUsesVolumeAndFileIndex(t *testing.T) {
	left := windows.ByHandleFileInformation{
		VolumeSerialNumber: 7,
		FileIndexHigh:      11,
		FileIndexLow:       13,
	}
	if !sameWindowsSourceIdentity(left, left) {
		t.Fatal("identical Windows source identities did not match")
	}
	for name, mutate := range map[string]func(*windows.ByHandleFileInformation){
		"volume": func(info *windows.ByHandleFileInformation) { info.VolumeSerialNumber++ },
		"high":   func(info *windows.ByHandleFileInformation) { info.FileIndexHigh++ },
		"low":    func(info *windows.ByHandleFileInformation) { info.FileIndexLow++ },
	} {
		t.Run(name, func(t *testing.T) {
			right := left
			mutate(&right)
			if sameWindowsSourceIdentity(left, right) {
				t.Fatalf("different %s identity matched", name)
			}
		})
	}
}

func TestLoadRejectsWindowsReparseSource(t *testing.T) {
	regular := writeSourceConfig(t, "SOURCE_KEY")
	symlink := filepath.Join(t.TempDir(), "config-link.toml")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Skipf("Windows symlink unavailable: %v", err)
	}
	snapshot, err := Load(symlink)
	if snapshot != nil {
		_ = snapshot.Close()
		t.Fatalf("Load() snapshot = %#v, want nil", snapshot)
	}
	assertSourceUnavailable(t, err)
}

func TestWindowsSourcePathMetadataUsesCompatibleShareAndNativeIdentity(t *testing.T) {
	path := writeSourceConfig(t, "SOURCE_KEY")
	retained, err := openSourceFile(path)
	if err != nil {
		t.Fatalf("openSourceFile() error = %v", err)
	}
	t.Cleanup(func() { _ = retained.Close() })

	calls := 0
	metadata, ok := platformSourceMetadataWithOpen(
		path,
		retained,
		func(
			name *uint16,
			desiredAccess uint32,
			shareMode uint32,
			securityAttributes *windows.SecurityAttributes,
			creationDisposition uint32,
			flagsAndAttributes uint32,
			templateFile windows.Handle,
		) (windows.Handle, error) {
			calls++
			if desiredAccess != windows.FILE_READ_ATTRIBUTES ||
				shareMode != windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE ||
				creationDisposition != windows.OPEN_EXISTING ||
				flagsAndAttributes != windows.FILE_FLAG_OPEN_REPARSE_POINT {
				t.Fatalf("path metadata open access/share/disposition/flags = %#x/%#x/%#x/%#x",
					desiredAccess, shareMode, creationDisposition, flagsAndAttributes)
			}
			return windows.CreateFile(
				name,
				desiredAccess,
				shareMode,
				securityAttributes,
				creationDisposition,
				flagsAndAttributes,
				templateFile,
			)
		},
	)
	if !ok || calls != 1 {
		t.Fatalf("platformSourceMetadataWithOpen() = %+v/%v, calls %d", metadata, ok, calls)
	}
}

func TestWindowsSourceMetadataRejectsChangedAndReparseEvidence(t *testing.T) {
	native := windows.ByHandleFileInformation{
		FileAttributes:     windows.FILE_ATTRIBUTE_NORMAL,
		LastWriteTime:      windows.Filetime{LowDateTime: 17},
		VolumeSerialNumber: 7,
		FileSizeLow:        64,
		NumberOfLinks:      1,
		FileIndexHigh:      11,
		FileIndexLow:       13,
	}
	basic := windowsSourceBasicInfo{
		LastWriteTime:  17,
		ChangeTime:     19,
		FileAttributes: windows.FILE_ATTRIBUTE_NORMAL,
	}
	baseline, ok := windowsSourceMetadataFromEvidence(windows.FILE_TYPE_DISK, native, basic)
	if !ok {
		t.Fatal("valid Windows source metadata was rejected")
	}
	if !sameSourceMetadata(baseline, baseline) {
		t.Fatal("identical Windows source metadata did not match")
	}

	unsafe := []struct {
		name     string
		fileType uint32
		native   windows.ByHandleFileInformation
		basic    windowsSourceBasicInfo
	}{
		{name: "non-disk", fileType: windows.FILE_TYPE_PIPE, native: native, basic: basic},
		{name: "native reparse", fileType: windows.FILE_TYPE_DISK, native: func() windows.ByHandleFileInformation {
			value := native
			value.FileAttributes |= windows.FILE_ATTRIBUTE_REPARSE_POINT
			return value
		}(), basic: basic},
		{name: "basic reparse", fileType: windows.FILE_TYPE_DISK, native: native, basic: func() windowsSourceBasicInfo {
			value := basic
			value.FileAttributes |= windows.FILE_ATTRIBUTE_REPARSE_POINT
			return value
		}()},
		{name: "directory", fileType: windows.FILE_TYPE_DISK, native: func() windows.ByHandleFileInformation {
			value := native
			value.FileAttributes |= windows.FILE_ATTRIBUTE_DIRECTORY
			return value
		}(), basic: basic},
		{name: "zero links", fileType: windows.FILE_TYPE_DISK, native: func() windows.ByHandleFileInformation {
			value := native
			value.NumberOfLinks = 0
			return value
		}(), basic: basic},
		{name: "missing change time", fileType: windows.FILE_TYPE_DISK, native: native, basic: func() windowsSourceBasicInfo {
			value := basic
			value.ChangeTime = 0
			return value
		}()},
	}
	for _, test := range unsafe {
		t.Run(test.name, func(t *testing.T) {
			if _, ok := windowsSourceMetadataFromEvidence(
				test.fileType,
				test.native,
				test.basic,
			); ok {
				t.Fatal("unsafe Windows source evidence was accepted")
			}
		})
	}

	mutations := map[string]func(*sourceMetadata){
		"volume":        func(value *sourceMetadata) { value.volume++ },
		"file index":    func(value *sourceMetadata) { value.index++ },
		"attributes":    func(value *sourceMetadata) { value.attributes++ },
		"link count":    func(value *sourceMetadata) { value.nlink++ },
		"size":          func(value *sourceMetadata) { value.size++ },
		"creation time": func(value *sourceMetadata) { value.creationTime++ },
		"write time":    func(value *sourceMetadata) { value.lastWriteTime++ },
		"change time":   func(value *sourceMetadata) { value.changeTime++ },
	}
	for name, mutate := range mutations {
		t.Run("changed "+name, func(t *testing.T) {
			changed := baseline
			mutate(&changed)
			if sameSourceMetadata(baseline, changed) {
				t.Fatalf("changed %s metadata matched", name)
			}
		})
	}
}
