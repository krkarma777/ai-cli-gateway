//go:build darwin

package process

import (
	"path/filepath"
	"testing"
)

func TestOpenRootCanonicalizesDefaultDarwinTemporaryAlias(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime")
	canonicalParent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	canonicalPath := filepath.Join(canonicalParent, filepath.Base(path))

	root, err := OpenRoot(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Close(); err != nil {
			t.Error(err)
		}
	})
	if root.path != canonicalPath {
		t.Fatalf("root path=%q want canonical path %q", root.path, canonicalPath)
	}

	rt := prepareTestRuntime(t, root)
	if rt.Dir != filepath.Join(canonicalPath, requestPrefix+rt.ID) {
		t.Fatalf("Runtime.Dir=%q", rt.Dir)
	}
}
