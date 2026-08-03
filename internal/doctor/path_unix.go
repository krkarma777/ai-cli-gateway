//go:build !windows

package doctor

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const maxUnixPathSymlinks = 40

func validatePlatformPath(
	path string,
	kind pathKind,
) (validatedPath, pathDisposition) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return validatedPath{}, pathUnsafe
	}
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return validatedPath{}, pathUnsafe
	}

	resolved, firstInfo, err := resolveUnixPath(clean, kind)
	if err != nil {
		if errors.Is(err, errPathMissing) {
			return validatedPath{}, pathMissing
		}
		return validatedPath{}, pathUnsafe
	}
	rechecked, secondInfo, err := resolveUnixPath(clean, kind)
	if err != nil || rechecked != resolved ||
		firstInfo == nil || secondInfo == nil ||
		!os.SameFile(firstInfo, secondInfo) {
		return validatedPath{}, pathUnsafe
	}
	return validatedPath{
		Clean:    clean,
		Resolved: resolved,
		Info:     secondInfo,
	}, pathSafe
}

func resolveUnixPath(
	clean string,
	kind pathKind,
) (string, fs.FileInfo, error) {
	pending := splitUnixAbsolutePath(clean)
	resolved := string(filepath.Separator)
	followedSymlink := false
	symlinks := 0

	for {
		rootInfo, err := os.Lstat(resolved)
		if err != nil || validateUnixAuthority(
			rootInfo,
			uint32(os.Geteuid()), //nolint:gosec // Kernel UIDs use uint32.
		) != nil {
			return "", nil, errPathUnsafe
		}
		if len(pending) == 0 {
			if err := validateUnixLeaf(
				rootInfo,
				kind,
				uint32(os.Geteuid()), //nolint:gosec // Kernel UIDs use uint32.
			); err != nil {
				return "", nil, errPathUnsafe
			}
			return resolved, rootInfo, nil
		}

		component := pending[0]
		candidate := filepath.Join(resolved, component)
		info, err := os.Lstat(candidate)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && !followedSymlink {
				return "", nil, errPathMissing
			}
			return "", nil, errPathUnsafe
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			if len(pending) == 1 && privateUnixLeaf(kind) {
				return "", nil, errPathUnsafe
			}
			symlinks++
			if symlinks > maxUnixPathSymlinks {
				return "", nil, errPathUnsafe
			}
			target, err := os.Readlink(candidate)
			if err != nil || target == "" {
				return "", nil, errPathUnsafe
			}
			rest := filepath.Join(pending[1:]...)
			var combined string
			if filepath.IsAbs(target) {
				combined = filepath.Join(target, rest)
			} else {
				combined = filepath.Join(resolved, target, rest)
			}
			combined = filepath.Clean(combined)
			if !filepath.IsAbs(combined) {
				return "", nil, errPathUnsafe
			}
			pending = splitUnixAbsolutePath(combined)
			resolved = string(filepath.Separator)
			followedSymlink = true
			continue
		}

		if len(pending) > 1 {
			if err := validateUnixAuthority(
				info,
				uint32(os.Geteuid()), //nolint:gosec // Kernel UIDs use uint32.
			); err != nil {
				return "", nil, errPathUnsafe
			}
			resolved = candidate
			pending = pending[1:]
			continue
		}

		if err := validateUnixLeaf(
			info,
			kind,
			uint32(os.Geteuid()), //nolint:gosec // Kernel UIDs use uint32.
		); err != nil {
			return "", nil, errPathUnsafe
		}
		return filepath.Clean(candidate), info, nil
	}
}

func validateUnixAuthority(info fs.FileInfo, effectiveUID uint32) error {
	if info == nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return errPathUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != 0 && stat.Uid != effectiveUID) ||
		info.Mode().Perm()&0o022 != 0 {
		return errPathUnsafe
	}
	return nil
}

func validateUnixLeaf(
	info fs.FileInfo,
	kind pathKind,
	effectiveUID uint32,
) error {
	if info == nil || info.Mode()&fs.ModeSymlink != 0 {
		return errPathUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return errPathUnsafe
	}
	mode := info.Mode()
	special := mode & (fs.ModeSetuid | fs.ModeSetgid | fs.ModeSticky)

	switch kind {
	case pathKindExecutable, pathKindEntrypoint:
		if !mode.IsRegular() ||
			(stat.Uid != 0 && stat.Uid != effectiveUID) ||
			mode.Perm()&0o111 == 0 ||
			mode.Perm()&0o022 != 0 ||
			special != 0 {
			return errPathUnsafe
		}
	case pathKindConfigHome:
		if !mode.IsDir() || stat.Uid != effectiveUID ||
			mode.Perm() != 0o700 || special != 0 {
			return errPathUnsafe
		}
	case pathKindCredential:
		permissions := mode.Perm()
		if !mode.IsRegular() || stat.Uid != effectiveUID ||
			(permissions != 0o400 && permissions != 0o600) ||
			special != 0 {
			return errPathUnsafe
		}
	case pathKindSafeDirectory:
		return validateUnixAuthority(info, effectiveUID)
	default:
		return errPathUnsafe
	}
	return nil
}

func privateUnixLeaf(kind pathKind) bool {
	return kind == pathKindConfigHome || kind == pathKindCredential
}

func splitUnixAbsolutePath(path string) []string {
	trimmed := strings.TrimPrefix(path, string(filepath.Separator))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, string(filepath.Separator))
}

func platformPathDefaults() (platformDefaults, error) {
	defaults := platformDefaults{SafePathTail: make([]validatedPath, 0, 2)}
	for _, path := range []string{"/usr/bin", "/bin"} {
		validated, disposition := validateSafeDirectoryPath(path)
		if disposition != pathSafe {
			return platformDefaults{}, errPathUnsafe
		}
		defaults.SafePathTail = append(defaults.SafePathTail, validated)
	}
	return defaults, nil
}
