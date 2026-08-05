//go:build linux || darwin

package releasepack

import (
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"syscall"
)

var releasepackEffectiveUID = os.Geteuid

func validateReleasepackHost() error {
	return nil
}

func validateOutputAuthority(name string, leaf fs.FileInfo) error {
	effectiveUID := releasepackEffectiveUID()
	if effectiveUID < 0 || uint64(effectiveUID) > math.MaxUint32 {
		return newCategorizedError(categoryUnsafePath)
	}
	wantUID := uint32(effectiveUID)

	current := name
	if leaf == nil {
		current = filepath.Dir(name)
	} else if !privateOutputLeaf(leaf, wantUID) {
		return newCategorizedError(categoryUnsafePath)
	}

	for {
		info, err := os.Lstat(current)
		if err != nil || !secureOutputAncestor(info, wantUID) {
			return newCategorizedError(categoryUnsafePath)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
		current = parent
	}
}

func privateOutputLeaf(info fs.FileInfo, effectiveUID uint32) bool {
	mode := info.Mode()
	uid, ok := fileOwnerUID(info)
	return ok && uid == effectiveUID && info.IsDir() && mode&os.ModeSymlink == 0 &&
		mode.Perm() == 0o700 && mode&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) == 0
}

func secureOutputAncestor(info fs.FileInfo, effectiveUID uint32) bool {
	mode := info.Mode()
	uid, ok := fileOwnerUID(info)
	if !ok || (uid != 0 && uid != effectiveUID) || !info.IsDir() || mode&os.ModeSymlink != 0 {
		return false
	}
	return mode.Perm()&0o022 == 0 || mode&os.ModeSticky != 0
}

func fileOwnerUID(info fs.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, false
	}
	return stat.Uid, true
}
