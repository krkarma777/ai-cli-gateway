//go:build !windows

package sdkcontract

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type unixOwnedRoot struct {
	mu     sync.Mutex
	path   string
	handle *os.File
	info   fs.FileInfo
	closed bool
}

type unixRecoveryRoot struct {
	mu     sync.Mutex
	path   string
	closed bool
}

func (r *unixRecoveryRoot) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

func (r *unixRecoveryRoot) RemoveExact() error { return newError(categoryCleanup) }

func (r *unixRecoveryRoot) Close() error {
	if r == nil {
		return newError(categoryCleanup)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.closed = true
	return nil
}

func createOwnedRoot(parent, pattern string) (ownedRoot, error) {
	return createOwnedRootWithOperations(parent, pattern, os.MkdirTemp, os.Open, os.Remove)
}

func createOwnedRootWithOperations(
	parent, pattern string,
	mkdirTemp func(string, string) (string, error),
	open func(string) (*os.File, error),
	remove func(string) error,
) (ownedRoot, error) {
	if !filepath.IsAbs(parent) || filepath.Clean(parent) != parent || pattern != ".sdk-contract-" {
		return nil, newCleanupError(true)
	}
	if mkdirTemp == nil || open == nil || remove == nil {
		return nil, newCleanupError(true)
	}
	if err := validateSecureAncestors(parent); err != nil {
		return nil, newCleanupError(true)
	}
	path, err := mkdirTemp(parent, pattern)
	if err != nil {
		return nil, newCleanupError(true)
	}
	if err := os.Chmod(path, 0o700); err != nil { // #nosec G302 -- bootstrap access before descriptor chmod and identity verification.
		if removeErr := remove(path); removeErr != nil {
			return &unixRecoveryRoot{path: path}, newCleanupError(false)
		}
		return nil, newCleanupError(true)
	}
	handle, err := open(path)
	if err != nil {
		if removeErr := remove(path); removeErr != nil {
			return &unixRecoveryRoot{path: path}, newCleanupError(false)
		}
		return nil, newCleanupError(true)
	}
	root := &unixOwnedRoot{path: path, handle: handle}
	rollback := func() (ownedRoot, error) {
		closeErr := handle.Close()
		removeErr := remove(path)
		if closeErr != nil || removeErr != nil {
			root.closed = true
			return root, newCleanupError(false)
		}
		return nil, newCleanupError(true)
	}
	if err := handle.Chmod(0o700); err != nil {
		return rollback()
	}
	handleInfo, err := handle.Stat()
	if err != nil || !privateDirectory(handleInfo) {
		return rollback()
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !os.SameFile(handleInfo, pathInfo) || !privateDirectory(pathInfo) {
		return rollback()
	}
	root.info = handleInfo
	return root, nil
}

func (r *unixOwnedRoot) Path() string {
	if r == nil {
		return ""
	}
	return r.path
}

func (r *unixOwnedRoot) RemoveExact() error {
	if r == nil {
		return newError(categoryCleanup)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return newError(categoryCleanup)
	}
	pathInfo, statErr := os.Lstat(r.path)
	handleInfo, handleErr := r.handle.Stat()
	identityOK := statErr == nil && handleErr == nil && pathInfo.Mode()&os.ModeSymlink == 0 &&
		privateDirectory(pathInfo) && privateDirectory(handleInfo) && os.SameFile(pathInfo, handleInfo) && os.SameFile(handleInfo, r.info)
	closeErr := r.handle.Close()
	r.closed = true
	if !identityOK || closeErr != nil {
		return newError(categoryCleanup)
	}
	if err := os.RemoveAll(r.path); err != nil {
		return newError(categoryCleanup)
	}
	if _, err := os.Lstat(r.path); !errors.Is(err, os.ErrNotExist) {
		return newError(categoryCleanup)
	}
	return nil
}

func (r *unixOwnedRoot) Close() error {
	if r == nil {
		return newError(categoryCleanup)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	if err := r.handle.Close(); err != nil {
		return newError(categoryCleanup)
	}
	return nil
}

func privateDirectory(info fs.FileInfo) bool {
	if info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return false
	}
	euid := os.Geteuid()
	if euid < 0 || uint64(euid) > math.MaxUint32 {
		return false
	}
	uid, ok := unixOwner(info)
	return ok && uid == uint32(euid) // #nosec G115 -- bounds checked immediately above.
}

func validateSecureAncestors(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return newError(categoryInvalid)
	}
	euid := os.Geteuid()
	if euid < 0 || uint64(euid) > math.MaxUint32 {
		return newError(categoryInvalid)
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return newError(categoryInvalid)
		}
		uid, ok := unixOwner(info)
		if !ok || uid != 0 && uid != uint32(euid) {
			return newError(categoryInvalid)
		}
		if info.Mode().Perm()&0o022 != 0 && info.Mode()&os.ModeSticky == 0 {
			return newError(categoryInvalid)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func unixOwner(info fs.FileInfo) (uint32, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0, false
	}
	return stat.Uid, true
}

func writePrivateFile(path string, data []byte, mode fs.FileMode) error {
	if mode != 0o600 || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return newError(categoryInvalid)
	}
	// #nosec G304 -- path is an absolute, normalized child of the owned private root.
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return newError(categoryFailed)
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = os.Remove(path)
		}
	}()
	if err := file.Chmod(mode); err != nil {
		return newError(categoryFailed)
	}
	if _, err := file.Write(data); err != nil {
		return newError(categoryFailed)
	}
	if err := file.Sync(); err != nil {
		return newError(categoryFailed)
	}
	handleInfo, err := file.Stat()
	if err != nil || !handleInfo.Mode().IsRegular() || handleInfo.Mode().Perm() != mode {
		return newError(categoryFailed)
	}
	if err := file.Close(); err != nil {
		return newError(categoryFailed)
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || pathInfo.Mode()&os.ModeSymlink != 0 || !pathInfo.Mode().IsRegular() || pathInfo.Mode().Perm() != mode || !os.SameFile(handleInfo, pathInfo) {
		return newError(categoryFailed)
	}
	remove = false
	return nil
}

func setProcessUmask(mask int) int { return syscall.Umask(mask) }

func validateExecutableIdentity(path string) (pathIdentity, error) {
	return validatePathIdentity(path, true)
}

func validateJavaScriptIdentity(path string) (pathIdentity, error) {
	identity, err := validatePathIdentity(path, false)
	if err != nil {
		return pathIdentity{}, err
	}
	if identity.aliasInfo.Mode()&os.ModeSymlink != 0 || identity.target != path {
		return pathIdentity{}, newError(categoryInvalid)
	}
	identity.javascript = true
	return identity, nil
}

func validatePathIdentity(path string, executable bool) (pathIdentity, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return pathIdentity{}, newError(categoryInvalid)
	}
	if err := validateSecureAncestors(filepath.Dir(path)); err != nil {
		return pathIdentity{}, err
	}
	aliasInfo, err := os.Lstat(path)
	if err != nil || aliasInfo.IsDir() || aliasInfo.Mode()&os.ModeType != 0 && aliasInfo.Mode()&os.ModeSymlink == 0 {
		return pathIdentity{}, newError(categoryInvalid)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(resolved) {
		return pathIdentity{}, newError(categoryInvalid)
	}
	resolved = filepath.Clean(resolved)
	if err := validateSecureAncestors(filepath.Dir(resolved)); err != nil {
		return pathIdentity{}, err
	}
	targetInfo, err := os.Lstat(resolved)
	if err != nil || targetInfo.Mode()&os.ModeSymlink != 0 || !targetInfo.Mode().IsRegular() || targetInfo.Mode().Perm()&0o022 != 0 || targetInfo.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return pathIdentity{}, newError(categoryInvalid)
	}
	if executable && targetInfo.Mode().Perm()&0o111 == 0 {
		return pathIdentity{}, newError(categoryInvalid)
	}
	return pathIdentity{original: path, aliasInfo: aliasInfo, target: resolved, targetInfo: targetInfo}, nil
}

func revalidateExecutableIdentity(identity pathIdentity) error {
	if identity.javascript {
		return newError(categoryInvalid)
	}
	return revalidatePathIdentity(identity, true)
}

func revalidateJavaScriptIdentity(identity pathIdentity) error {
	if !identity.javascript {
		return newError(categoryInvalid)
	}
	return revalidatePathIdentity(identity, false)
}

func revalidatePathIdentity(identity pathIdentity, executable bool) error {
	current, err := validatePathIdentity(identity.original, executable)
	if err != nil {
		return err
	}
	if current.target != identity.target || !os.SameFile(current.aliasInfo, identity.aliasInfo) || !os.SameFile(current.targetInfo, identity.targetInfo) {
		return newError(categoryInvalid)
	}
	return nil
}

type unixChild struct {
	cmd      *exec.Cmd
	pid      int
	pgid     int
	exited   chan struct{}
	waitMu   sync.Mutex
	waitErr  error
	stopOnce sync.Once
	result   cleanupResult
	killSent bool
}

func startUnixChild(executable, directory string, argv, environment []string, output io.Writer) (child, error) {
	return startUnixCommand(executable, directory, argv, environment, output, output)
}

func startPlatformChild(executable, directory string, argv, environment []string, output io.Writer) (child, error) {
	return startUnixChild(executable, directory, argv, environment, output)
}

func platformSupported() bool { return true }

func startUnixCommand(executable, directory string, argv, environment []string, stdout, stderr io.Writer) (*unixChild, error) {
	if outputNil(stdout) || outputNil(stderr) {
		return nil, newError(categoryFailed)
	}
	cmd := exec.CommandContext(context.Background(), executable, argv...) //nolint:gosec // validated executable and closed argv, never a shell.
	cmd.Dir = directory
	cmd.Env = make([]string, len(environment))
	copy(cmd.Env, environment)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return nil, newError(categoryFailed)
	}
	child := &unixChild{cmd: cmd, pid: cmd.Process.Pid, pgid: cmd.Process.Pid, exited: make(chan struct{})}
	go func() {
		err := cmd.Wait()
		child.waitMu.Lock()
		child.waitErr = err
		child.waitMu.Unlock()
		close(child.exited)
	}()
	pgid, err := unix.Getpgid(child.pid)
	if err != nil || pgid != child.pid {
		return child, newCleanupError(false)
	}
	return child, nil
}

func outputNil(writer io.Writer) bool { return writer == nil }

func (c *unixChild) PID() int {
	if c == nil {
		return 0
	}
	return c.pid
}
func (c *unixChild) Exited() <-chan struct{} {
	if c == nil {
		closed := make(chan struct{})
		close(closed)
		return closed
	}
	return c.exited
}

func (c *unixChild) StopAndWait(grace time.Duration) cleanupResult {
	if c == nil || grace <= 0 {
		return cleanupResult{SafeToRemove: false, Err: newError(categoryCleanup)}
	}
	c.stopOnce.Do(func() { c.result = c.stopAndWait(grace) })
	return c.result
}

func (c *unixChild) stopAndWait(grace time.Duration) cleanupResult {
	termErr := signalGroup(c.pgid, unix.SIGTERM)
	if termErr != nil && !errors.Is(termErr, unix.ESRCH) {
		_ = signalGroup(c.pgid, unix.SIGKILL)
	}
	if waitForGroupAndChild(c, grace) {
		if termErr != nil && !errors.Is(termErr, unix.ESRCH) {
			return cleanupResult{SafeToRemove: true, Err: newError(categoryCleanup)}
		}
		return cleanupResult{SafeToRemove: true}
	}
	killErr := signalGroup(c.pgid, unix.SIGKILL)
	c.killSent = true
	if waitForGroupAndChild(c, grace) {
		if killErr != nil && !errors.Is(killErr, unix.ESRCH) {
			return cleanupResult{SafeToRemove: true, Err: newError(categoryCleanup)}
		}
		return cleanupResult{SafeToRemove: true}
	}
	return cleanupResult{SafeToRemove: false, Err: newError(categoryCleanup)}
}

func signalGroup(pgid int, signal unix.Signal) error {
	if pgid <= 0 {
		return newError(categoryCleanup)
	}
	return unix.Kill(-pgid, signal)
}

func groupAbsent(pgid int) bool {
	err := unix.Kill(-pgid, 0)
	return errors.Is(err, unix.ESRCH)
}

func waitForGroupAndChild(child *unixChild, grace time.Duration) bool {
	deadline := time.NewTimer(grace)
	defer deadline.Stop()
	interval := grace / 32
	if interval <= 0 {
		interval = grace
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	waited := false
	for {
		if !waited {
			select {
			case <-child.exited:
				waited = true
			default:
			}
		}
		if waited && groupAbsent(child.pgid) {
			return true
		}
		select {
		case <-deadline.C:
			return false
		case <-ticker.C:
		case <-child.exited:
			waited = true
		}
	}
}

type boundedBuffer struct {
	mu     sync.Mutex
	limit  int
	total  int
	buffer bytes.Buffer
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.total += len(data)
	remaining := b.limit - b.buffer.Len()
	if remaining > len(data) {
		remaining = len(data)
	}
	if remaining > 0 {
		_, _ = b.buffer.Write(data[:remaining])
	}
	return len(data), nil
}
func (b *boundedBuffer) Bytes() []byte {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buffer.Bytes()...)
}
func (b *boundedBuffer) Overflowed() bool { b.mu.Lock(); defer b.mu.Unlock(); return b.total > b.limit }

func runGroupCommand(ctx context.Context, executable, directory string, argv, environment []string, grace time.Duration, stdoutLimit, stderrLimit int) (groupCommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if grace <= 0 || stdoutLimit < 0 || stderrLimit < 0 {
		return groupCommandResult{}, newError(categoryInvalid)
	}
	if err := ctx.Err(); err != nil {
		return groupCommandResult{}, err
	}
	stdout := &boundedBuffer{limit: stdoutLimit}
	stderr := &boundedBuffer{limit: stderrLimit}
	child, err := startUnixCommand(executable, directory, argv, environment, stdout, stderr)
	if err != nil {
		if child != nil {
			return groupCommandResult{}, cleanupStartedChildFailure(child, err, grace)
		}
		return groupCommandResult{}, err
	}
	select {
	case <-ctx.Done():
		cleanup := child.StopAndWait(grace)
		if !cleanup.SafeToRemove || cleanup.Err != nil {
			return groupCommandResult{}, newCleanupError(cleanup.SafeToRemove)
		}
		return groupCommandResult{}, ctx.Err()
	case <-child.Exited():
	}
	cleanup := child.StopAndWait(grace)
	if !cleanup.SafeToRemove || cleanup.Err != nil {
		return groupCommandResult{}, newCleanupError(cleanup.SafeToRemove)
	}
	result := groupCommandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if stdout.Overflowed() || stderr.Overflowed() {
		return groupCommandResult{}, newError(categoryFailed)
	}
	child.waitMu.Lock()
	waitErr := child.waitErr
	child.waitMu.Unlock()
	if waitErr != nil {
		return groupCommandResult{}, newError(categoryFailed)
	}
	return result, nil
}

func cleanupStartedChildFailure(started child, original error, grace time.Duration) error {
	if started == nil {
		return original
	}
	cleanup := normalizeCleanup(started.StopAndWait(grace))
	return newCleanupError(cleanup.SafeToRemove)
}
