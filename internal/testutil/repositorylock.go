package testutil

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repositoryScanLockPrefix = "ai-cli-gateway-repository-scan-"

// AcquireRepositoryScanLock serializes tests that scan or temporarily mutate
// the repository. Call it before registering cleanup for any repository-local
// mutation so that the mutation is removed before the lock is released.
func AcquireRepositoryScanLock(t testing.TB) {
	t.Helper()
	root := repositoryRoot(t)
	path, err := repositoryScanLockPath(root)
	if err != nil {
		t.Fatalf("resolve repository scan lock: %v", err)
	}
	file, err := openRepositoryScanLock(path)
	if err != nil {
		t.Fatalf("open repository scan lock: %v", err)
	}
	if err := lockRepositoryScanFile(file); err != nil {
		_ = file.Close()
		t.Fatalf("acquire repository scan lock: %v", err)
	}
	t.Cleanup(func() {
		if err := errors.Join(
			unlockRepositoryScanFile(file),
			file.Close(),
		); err != nil {
			t.Errorf("release repository scan lock: %v", err)
		}
	})
}

func repositoryScanLockPath(root string) (string, error) {
	if root == "" || !filepath.IsAbs(root) {
		return "", errors.New("repository root must be absolute")
	}
	tempRoot, err := filepath.Abs(os.TempDir())
	if err != nil {
		return "", fmt.Errorf("resolve temporary directory: %w", err)
	}
	tempRoot, err = filepath.EvalSymlinks(tempRoot)
	if err != nil {
		return "", fmt.Errorf("resolve temporary directory aliases: %w", err)
	}
	inside, err := pathIsWithin(root, tempRoot)
	if err != nil {
		return "", err
	}
	if inside {
		return "", errors.New("temporary lock directory is inside repository")
	}
	digest := sha256.Sum256([]byte(filepath.Clean(root)))
	return filepath.Join(
		tempRoot,
		fmt.Sprintf("%s%x.lock", repositoryScanLockPrefix, digest[:16]),
	), nil
}

func pathIsWithin(root, candidate string) (bool, error) {
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(candidate))
	if err != nil {
		return false, fmt.Errorf("compare repository and lock paths: %w", err)
	}
	return relative == "." ||
		(relative != ".." && !filepath.IsAbs(relative) &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator))), nil
}

func openRepositoryScanLock(path string) (*os.File, error) {
	info, err := os.Lstat(path)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return nil, errors.New("repository scan lock path is not a regular file")
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600) //nolint:gosec // The path is derived from the repository digest below the external temporary root.
	if err != nil {
		return nil, err
	}
	openedInfo, statErr := file.Stat()
	pathInfo, lstatErr := os.Lstat(path)
	if statErr != nil || lstatErr != nil ||
		pathInfo.Mode()&os.ModeSymlink != 0 ||
		!openedInfo.Mode().IsRegular() || !pathInfo.Mode().IsRegular() ||
		!os.SameFile(openedInfo, pathInfo) {
		_ = file.Close()
		return nil, errors.New("repository scan lock changed while opening")
	}
	return file, nil
}
