//go:build windows

package testutil

import "testing"

func TestPathIsWithinTreatsDifferentWindowsVolumesAsOutside(t *testing.T) {
	inside, err := pathIsWithin(`D:\repository`, `C:\temporary`)
	if err != nil {
		t.Fatalf("pathIsWithin(different volumes) error = %v", err)
	}
	if inside {
		t.Fatal("path on a different Windows volume was classified as inside")
	}
}

func TestPathIsWithinFailsClosedForSameVolumeNamespaceAlias(t *testing.T) {
	if _, err := pathIsWithin(
		`D:\repository`,
		`\\?\D:\repository\temporary`,
	); err == nil {
		t.Fatal("same-volume namespace alias was not rejected")
	}
}
