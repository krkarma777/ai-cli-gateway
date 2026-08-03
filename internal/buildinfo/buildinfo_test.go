package buildinfo

import "testing"

func TestDefaultsAreSafe(t *testing.T) {
	if Version != "dev" || Commit != "none" || Date != "unknown" {
		t.Fatalf("unexpected defaults: %q %q %q", Version, Commit, Date)
	}
}
