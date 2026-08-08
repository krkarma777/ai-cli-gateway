//go:build !windows

package configsource

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func restoreSourceModTime(t *testing.T, path string, modTime time.Time) {
	t.Helper()
	if err := os.Chtimes(path, modTime, modTime); err != nil {
		t.Fatalf("restore source mtime: %v", err)
	}
}

func TestLoadRejectsUnixSymlinkAndNonRegularSources(t *testing.T) {
	regular := writeSourceConfig(t, "SOURCE_KEY")
	symlink := filepath.Join(t.TempDir(), "config-link.toml")
	if err := os.Symlink(regular, symlink); err != nil {
		t.Fatalf("create source symlink: %v", err)
	}
	fifo := filepath.Join(t.TempDir(), "config.fifo")
	if err := unix.Mkfifo(fifo, 0o600); err != nil {
		t.Fatalf("create source FIFO: %v", err)
	}

	for _, path := range []string{symlink, filepath.Dir(regular), fifo} {
		snapshot, err := Load(path)
		if snapshot != nil {
			_ = snapshot.Close()
			t.Fatalf("Load(%q) snapshot = %#v, want nil", path, snapshot)
		}
		assertSourceUnavailable(t, err)
	}
}

func TestUnixSourceMetadataDetectsRestoredMutation(t *testing.T) {
	path := writeSourceConfig(t, "SOURCE_KEY")
	original, err := os.ReadFile(path) // #nosec G304 -- path is created by writeSourceConfig in this test's private TempDir.
	if err != nil {
		t.Fatalf("read original source: %v", err)
	}
	file, err := openSourceFile(path)
	if err != nil {
		t.Fatalf("openSourceFile() error = %v", err)
	}
	t.Cleanup(func() { _ = file.Close() })
	before, ok := platformSourceMetadata(path, file)
	if !ok {
		t.Fatal("platformSourceMetadata() rejected valid source")
	}
	baseline, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat baseline source: %v", err)
	}

	mutateAndRestoreSource(t, path, original, baseline.ModTime())
	after, ok := platformSourceMetadata(path, file)
	if !ok {
		t.Fatal("platformSourceMetadata() rejected restored source evidence")
	}
	if sameSourceMetadata(before, after) {
		t.Fatal("restored content and mtime concealed native source mutation")
	}
}

func TestCheckedUnixSourceDevicePreservesDarwinInt32BitPatterns(t *testing.T) {
	tests := []struct {
		name   string
		device int32
		want   uint64
	}{
		{name: "high bit set", device: -2_147_483_648, want: 2_147_483_648},
		{name: "all bits set", device: -1, want: 4_294_967_295},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := checkedUnixSourceDevice(test.device)
			if !ok || got != test.want {
				t.Fatalf("checkedUnixSourceDevice(%d) = %d, %t; want %d, true",
					test.device, got, ok, test.want)
			}
		})
	}
}

func TestCheckedUnixSourceDeviceKeepsDarwinInt32BitPatternsDistinct(t *testing.T) {
	left, leftOK := checkedUnixSourceDevice(int32(-1))
	right, rightOK := checkedUnixSourceDevice(int32(-2))
	if !leftOK || !rightOK || left == right {
		t.Fatalf("distinct Darwin devices normalized to %d/%t and %d/%t",
			left, leftOK, right, rightOK)
	}
}

func TestCheckedUnixSourceDevicePreservesUnsignedRange(t *testing.T) {
	for _, device := range []uint64{0, 1, 1 << 63, 18_446_744_073_709_551_615} {
		got, ok := checkedUnixSourceDevice(device)
		if !ok || got != device {
			t.Fatalf("checkedUnixSourceDevice(%d) = %d, %t; want %d, true",
				device, got, ok, device)
		}
	}
}

func TestCheckedUnixSourceDeviceRejectsNegativeWiderSignedValues(t *testing.T) {
	got, ok := checkedUnixSourceDevice(int64(-1))
	if ok || got != 0 {
		t.Fatalf("checkedUnixSourceDevice(int64(-1)) = %d, %t; want 0, false", got, ok)
	}
}

func TestSameUnixSourceMetadataCoversIdentityAndChangeEvidence(t *testing.T) {
	baseline := sourceMetadata{
		device: 2, inode: 3, mode: uint32(unix.S_IFREG | 0o600),
		uid: 4, gid: 5, nlink: 1, size: 64,
		modTimeNanos: 7, changeSeconds: 8, changeNanos: 9,
	}
	if !sameSourceMetadata(baseline, baseline) {
		t.Fatal("identical Unix source metadata did not match")
	}
	mutations := map[string]func(*sourceMetadata){
		"device":         func(value *sourceMetadata) { value.device++ },
		"inode":          func(value *sourceMetadata) { value.inode++ },
		"mode":           func(value *sourceMetadata) { value.mode++ },
		"uid":            func(value *sourceMetadata) { value.uid++ },
		"gid":            func(value *sourceMetadata) { value.gid++ },
		"link count":     func(value *sourceMetadata) { value.nlink++ },
		"size":           func(value *sourceMetadata) { value.size++ },
		"mtime":          func(value *sourceMetadata) { value.modTimeNanos++ },
		"change seconds": func(value *sourceMetadata) { value.changeSeconds++ },
		"change nanos":   func(value *sourceMetadata) { value.changeNanos++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := baseline
			mutate(&changed)
			if sameSourceMetadata(baseline, changed) {
				t.Fatalf("changed %s metadata matched", name)
			}
		})
	}
}
