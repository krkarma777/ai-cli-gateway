//go:build !windows

package process

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const maxTrustedAncestorSymlinks = 40

func canonicalizeRootPath(path string) (string, error) {
	parent, err := canonicalizeTrustedUnixDirectory(filepath.Dir(path))
	if err != nil {
		return "", err
	}
	return filepath.Join(parent, filepath.Base(path)), nil
}

func canonicalizeTrustedUnixDirectory(path string) (string, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return "", errors.New("runtime root parent must be absolute")
	}
	resolved := string(filepath.Separator)
	pending := splitUnixPath(clean)
	symlinks := 0
	for len(pending) > 0 {
		component := pending[0]
		pending = pending[1:]
		switch component {
		case "", ".":
			continue
		case "..":
			resolved = filepath.Dir(resolved)
			continue
		}

		candidate := filepath.Join(resolved, component)
		info, err := os.Lstat(candidate)
		if err != nil {
			return "", fmt.Errorf(
				"inspect runtime root ancestor alias: %w",
				err,
			)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			if !info.IsDir() {
				return "", errors.New(
					"runtime root ancestor is not a directory",
				)
			}
			resolved = candidate
			continue
		}
		if err := validateTrustedUnixAncestorAlias(candidate, info); err != nil {
			return "", err
		}
		symlinks++
		if symlinks > maxTrustedAncestorSymlinks {
			return "", errors.New("too many runtime root ancestor aliases")
		}
		target, err := os.Readlink(candidate)
		if err != nil {
			return "", fmt.Errorf("read runtime root ancestor alias: %w", err)
		}
		if filepath.IsAbs(target) {
			resolved = string(filepath.Separator)
		}
		pending = append(splitUnixPath(target), pending...)
	}
	return filepath.Clean(resolved), nil
}

func splitUnixPath(path string) []string {
	trimmed := strings.TrimPrefix(path, string(filepath.Separator))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, string(filepath.Separator))
}

func validateTrustedUnixAncestorAlias(
	path string,
	info fs.FileInfo,
) error {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("missing Unix ancestor alias ownership information")
	}
	if stat.Uid != 0 {
		return errors.New("runtime root ancestor alias is not root-owned")
	}
	for ancestor := filepath.Dir(path); ; ancestor = filepath.Dir(ancestor) {
		ancestorInfo, err := os.Lstat(ancestor)
		if err != nil {
			return fmt.Errorf("inspect runtime root alias authority: %w", err)
		}
		if ancestorInfo.Mode()&fs.ModeSymlink != 0 ||
			!ancestorInfo.IsDir() {
			return errors.New("runtime root alias authority is not a directory")
		}
		ancestorStat, ok := ancestorInfo.Sys().(*syscall.Stat_t)
		if !ok {
			return errors.New(
				"missing Unix runtime root alias authority information",
			)
		}
		if ancestorStat.Uid != 0 {
			return errors.New(
				"runtime root ancestor alias has mutable authority",
			)
		}
		const groupOrOtherWrite = 0o022
		if ancestorInfo.Mode().Perm()&groupOrOtherWrite != 0 &&
			ancestorInfo.Mode()&fs.ModeSticky == 0 {
			return errors.New(
				"runtime root ancestor alias has writable authority",
			)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return nil
		}
	}
}

func createSecureDirectory(path string) error {
	return os.Mkdir(path, runtimeDirMode)
}

func bootstrapCreatedRootMode(path string) error {
	return os.Chmod(path, runtimeDirMode)
}

func validateImmediateParentSecurity(_ string, info fs.FileInfo) error {
	if info == nil {
		return errors.New("missing Unix ancestor information")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("missing Unix ancestor ownership information")
	}
	// Unix effective UIDs are nonnegative and represented by the kernel as
	// uint32, matching Stat_t.Uid.
	//nolint:gosec
	effectiveUID := uint32(os.Geteuid())
	if stat.Uid != effectiveUID && stat.Uid != 0 {
		return errors.New("runtime root ancestor is owned by an untrusted user")
	}
	const groupOrOtherWrite = 0o022
	if info.Mode().Perm()&groupOrOtherWrite != 0 &&
		info.Mode()&fs.ModeSticky == 0 {
		return errors.New(
			"runtime root parent is writable by another user without sticky bit",
		)
	}
	return nil
}

func validateRootAncestorSecurity(path string) error {
	for ancestor := filepath.Dir(path); ; ancestor = filepath.Dir(ancestor) {
		info, err := os.Lstat(ancestor)
		if err != nil {
			return fmt.Errorf("inspect runtime root ancestor: %w", err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return errors.New("runtime root ancestor is a symlink")
		}
		if !info.IsDir() {
			return errors.New("runtime root ancestor is not a directory")
		}
		if err := validateImmediateParentSecurity(ancestor, info); err != nil {
			return err
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return nil
		}
	}
}

func lstatNoLinkDirectory(path string) (fs.FileInfo, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, errors.New("public directory path must be absolute")
	}
	var leaf fs.FileInfo
	for current := clean; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return nil, fmt.Errorf("inspect public directory path: %w", err)
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return nil, errors.New("public directory path traverses a symlink")
		}
		if !info.IsDir() {
			return nil, errors.New("public directory path component is not a directory")
		}
		if current == clean {
			leaf = info
		}
		parent := filepath.Dir(current)
		if parent == current {
			return leaf, nil
		}
	}
}

func validateOwnedPath(
	_ string,
	info fs.FileInfo,
	wantDirectory bool,
	expected fs.FileMode,
) error {
	return validateOwnedUnixInfo(info, wantDirectory, expected)
}

func validateOwnedFile(
	_ *os.File,
	info fs.FileInfo,
	wantDirectory bool,
	expected fs.FileMode,
) error {
	return validateOwnedUnixInfo(info, wantDirectory, expected)
}

func validateOwnedUnixInfo(
	info fs.FileInfo,
	wantDirectory bool,
	expected fs.FileMode,
) error {
	if info == nil {
		return errors.New("missing file information")
	}
	if wantDirectory {
		if !info.IsDir() {
			return errors.New("path is not a directory")
		}
	} else if !info.Mode().IsRegular() {
		return errors.New("path is not a regular file")
	}
	const special = fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky
	if got := info.Mode() & (fs.ModePerm | special); got != expected {
		return fmt.Errorf("unsafe mode %v", got)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errors.New("missing Unix ownership information")
	}
	// Unix effective UIDs are nonnegative and represented by the kernel as
	// uint32, matching Stat_t.Uid.
	//nolint:gosec
	if stat.Uid != uint32(os.Geteuid()) {
		return errors.New("path is not owned by effective user")
	}
	return nil
}

func forceCreatedMode(file *os.File, mode fs.FileMode) error {
	if file == nil {
		return errors.New("missing created file handle")
	}
	return file.Chmod(mode)
}

func lockFile(file *os.File) error {
	return classifyLockError(
		unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB),
	)
}

func classifyLockError(err error) error {
	if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
		return ErrRootLocked
	}
	return err
}

func unlockFile(file *os.File) error {
	return unix.Flock(int(file.Fd()), unix.LOCK_UN)
}
