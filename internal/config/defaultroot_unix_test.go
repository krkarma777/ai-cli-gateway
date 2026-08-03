//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestDecodeUsesUnixEffectiveUIDDefaultRootOnlyWhenOmitted(t *testing.T) {
	f := newFixture(t)
	document := strings.Replace(f.document(), "root = "+tomlQuote(f.root)+"\n", "", 1)
	cfg := mustDecode(t, document)

	want := filepath.Join(
		os.TempDir(),
		"ai-cli-gateway-"+strconv.Itoa(os.Geteuid()),
	)
	if cfg.Runtime.Root != want {
		t.Fatalf("Runtime.Root = %q, want %q", cfg.Runtime.Root, want)
	}

	requireDecodeError(t, replaceLine(f.document(), "root = "+tomlQuote(f.root), `root = ""`))
}
