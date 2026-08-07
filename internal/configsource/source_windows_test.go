//go:build windows

package configsource

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

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
