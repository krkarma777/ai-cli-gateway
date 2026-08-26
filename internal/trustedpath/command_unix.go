//go:build !windows

package trustedpath

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const maxUnixCommandSymlinks = 40

type unixCommandInspection struct {
	mu     sync.Mutex
	file   *os.File
	path   CommandPath
	info   fs.FileInfo
	bytes  []byte
	parent *unixCommandParentEvidence
	closed bool
}

type unixCommandParentEvidence struct {
	path string
	name string
	info fs.FileInfo
	leaf unixCommandAnchor
}

type unixCommandAnchor struct {
	dev   uint64
	ino   uint64
	mode  uint32
	uid   uint32
	gid   uint32
	nlink uint64
	size  int64
}

// OpenCommandPath validates, retains, and optionally reads one Unix command.
func OpenCommandPath(
	path string,
	mode CommandReadMode,
	limit int64,
) (CommandFileInspection, error) {
	if !validCommandRead(mode, limit) || path == "" ||
		strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(path) {
		return nil, ErrUnsafe
	}
	clean := filepath.Clean(path)
	resolved, validatedInfo, err := resolveUnixCommand(clean)
	if err != nil {
		return nil, err
	}
	file, err := openUnixCommand(resolved, mode)
	if err != nil {
		if mode == CommandIdentityOnly && runtime.GOOS == "darwin" &&
			(errors.Is(err, unix.EACCES) || errors.Is(err, unix.EPERM)) &&
			unixCommandIdentityFallbackAllowed(validatedInfo) {
			return openUnixCommandWithParentEvidence(clean, resolved, validatedInfo)
		}
		return nil, classifyUnixCommandOpen(err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = file.Close()
		}
	}()

	handleInfo, err := file.Stat()
	if err != nil || validateUnixCommandLeaf(handleInfo) != nil ||
		!os.SameFile(validatedInfo, handleInfo) {
		return nil, ErrUnsafe
	}
	payload := []byte(nil)
	if mode == CommandBoundedContent {
		if handleInfo.Size() < 0 || handleInfo.Size() > limit {
			return nil, ErrUnsafe
		}
		payload, err = io.ReadAll(io.LimitReader(file, limit+1))
		if err != nil || int64(len(payload)) > limit {
			return nil, ErrUnsafe
		}
		afterRead, statErr := file.Stat()
		if statErr != nil || !sameUnixCommandSnapshot(handleInfo, afterRead) {
			return nil, ErrUnsafe
		}
		handleInfo = afterRead
	}

	rechecked, pathInfo, err := resolveUnixCommand(clean)
	if err != nil || rechecked != resolved ||
		!sameUnixCommandSnapshot(handleInfo, pathInfo) {
		return nil, ErrUnsafe
	}
	keep = true
	return &unixCommandInspection{
		file:  file,
		path:  CommandPath{Clean: clean, Resolved: resolved},
		info:  handleInfo,
		bytes: append([]byte(nil), payload...),
	}, nil
}

func (i *unixCommandInspection) Bytes() []byte {
	i.mu.Lock()
	defer i.mu.Unlock()
	return append([]byte(nil), i.bytes...)
}

func (i *unixCommandInspection) FileInfo() fs.FileInfo {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.info
}

func (i *unixCommandInspection) Revalidate() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed || i.file == nil || i.info == nil {
		return ErrUnsafe
	}
	if i.parent != nil {
		return i.revalidateParentEvidence()
	}
	handleInfo, err := i.file.Stat()
	if err != nil || validateUnixCommandLeaf(handleInfo) != nil ||
		!sameUnixCommandSnapshot(i.info, handleInfo) {
		return ErrUnsafe
	}
	resolved, pathInfo, err := resolveUnixCommand(i.path.Clean)
	if err != nil || resolved != i.path.Resolved ||
		!sameUnixCommandSnapshot(handleInfo, pathInfo) {
		return ErrUnsafe
	}
	fresh, err := openUnixCommand(i.path.Resolved, CommandIdentityOnly)
	if err != nil {
		return ErrUnsafe
	}
	freshInfo, statErr := fresh.Stat()
	closeErr := fresh.Close()
	if statErr != nil || closeErr != nil ||
		validateUnixFreshCommandEvidence(handleInfo, freshInfo) != nil {
		return ErrUnsafe
	}
	return nil
}

func (i *unixCommandInspection) revalidateParentEvidence() error {
	parentInfo, err := i.file.Stat()
	if err != nil || validateUnixCommandAuthority(
		parentInfo,
		uint32(os.Geteuid()), //nolint:gosec // Kernel UIDs use uint32.
	) != nil || !sameUnixCommandAuthorityIdentity(i.parent.info, parentInfo) {
		return ErrUnsafe
	}
	retainedLeaf, err := readUnixCommandAnchor(i.file, i.parent.name)
	if err != nil || validateUnixCommandAnchor(retainedLeaf) != nil ||
		!sameUnixCommandAnchor(i.parent.leaf, retainedLeaf) {
		return ErrUnsafe
	}

	resolved, pathInfo, err := resolveUnixCommand(i.path.Clean)
	if err != nil || resolved != i.path.Resolved ||
		!sameUnixCommandSnapshot(i.info, pathInfo) ||
		!sameUnixCommandAnchorFileInfo(retainedLeaf, pathInfo) {
		return ErrUnsafe
	}

	fresh, err := openUnixCommandParent(i.parent.path)
	if err != nil {
		return ErrUnsafe
	}
	freshParentInfo, statErr := fresh.Stat()
	freshLeaf, leafErr := readUnixCommandAnchor(fresh, i.parent.name)
	closeErr := fresh.Close()
	if statErr != nil || leafErr != nil || closeErr != nil ||
		validateUnixCommandAuthority(
			freshParentInfo,
			uint32(os.Geteuid()), //nolint:gosec // Kernel UIDs use uint32.
		) != nil || !sameUnixCommandAuthorityIdentity(parentInfo, freshParentInfo) ||
		validateUnixCommandAnchor(freshLeaf) != nil ||
		!sameUnixCommandAnchor(retainedLeaf, freshLeaf) {
		return ErrUnsafe
	}

	finalLeaf, err := readUnixCommandAnchor(i.file, i.parent.name)
	if err != nil || validateUnixCommandAnchor(finalLeaf) != nil ||
		!sameUnixCommandAnchor(freshLeaf, finalLeaf) {
		return ErrUnsafe
	}
	return nil
}

func (i *unixCommandInspection) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil
	}
	i.closed = true
	if i.file == nil {
		return ErrUnsafe
	}
	err := i.file.Close()
	i.file = nil
	if err != nil {
		return ErrUnsafe
	}
	return nil
}

func (i *unixCommandInspection) commandPath() CommandPath {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.path
}

func resolveUnixCommand(clean string) (string, fs.FileInfo, error) {
	pending := splitUnixCommandPath(clean)
	resolved := string(filepath.Separator)
	followedSymlink := false
	symlinks := 0
	effectiveUID := uint32(os.Geteuid()) //nolint:gosec // Kernel UIDs use uint32.

	for {
		rootInfo, err := os.Lstat(resolved)
		if err != nil || validateUnixCommandAuthority(rootInfo, effectiveUID) != nil {
			return "", nil, ErrUnsafe
		}
		if len(pending) == 0 {
			if validateUnixCommandLeaf(rootInfo) != nil {
				return "", nil, ErrUnsafe
			}
			return resolved, rootInfo, nil
		}

		component := pending[0]
		candidate := filepath.Join(resolved, component)
		info, err := os.Lstat(candidate)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) && !followedSymlink {
				return "", nil, ErrMissing
			}
			return "", nil, ErrUnsafe
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			symlinks++
			if symlinks > maxUnixCommandSymlinks {
				return "", nil, ErrUnsafe
			}
			target, err := os.Readlink(candidate)
			if err != nil || target == "" {
				return "", nil, ErrUnsafe
			}
			rest := filepath.Join(pending[1:]...)
			combined := filepath.Join(resolved, target, rest)
			if filepath.IsAbs(target) {
				combined = filepath.Join(target, rest)
			}
			combined = filepath.Clean(combined)
			if !filepath.IsAbs(combined) {
				return "", nil, ErrUnsafe
			}
			pending = splitUnixCommandPath(combined)
			resolved = string(filepath.Separator)
			followedSymlink = true
			continue
		}

		if len(pending) > 1 {
			if validateUnixCommandAuthority(info, effectiveUID) != nil {
				return "", nil, ErrUnsafe
			}
			resolved = candidate
			pending = pending[1:]
			continue
		}
		if validateUnixCommandLeaf(info) != nil {
			return "", nil, ErrUnsafe
		}
		return filepath.Clean(candidate), info, nil
	}
}

func validateUnixCommandAuthority(info fs.FileInfo, effectiveUID uint32) error {
	if info == nil || !info.IsDir() || info.Mode()&fs.ModeSymlink != 0 {
		return ErrUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || (stat.Uid != 0 && stat.Uid != effectiveUID) ||
		info.Mode().Perm()&0o022 != 0 {
		return ErrUnsafe
	}
	return nil
}

func validateUnixCommandLeaf(info fs.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() ||
		info.Mode()&fs.ModeSymlink != 0 {
		return ErrUnsafe
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return ErrUnsafe
	}
	effectiveUID := uint32(os.Geteuid()) //nolint:gosec // Kernel UIDs use uint32.
	mode := info.Mode()
	if (stat.Uid != 0 && stat.Uid != effectiveUID) ||
		mode.Perm()&0o111 == 0 || mode.Perm()&0o022 != 0 ||
		mode&(fs.ModeSetuid|fs.ModeSetgid|fs.ModeSticky) != 0 {
		return ErrUnsafe
	}
	return nil
}

func unixCommandIdentityFallbackAllowed(info fs.FileInfo) bool {
	return validateUnixCommandLeaf(info) == nil
}

func openUnixCommandWithParentEvidence(
	clean string,
	resolved string,
	validatedInfo fs.FileInfo,
) (CommandFileInspection, error) {
	parentPath := filepath.Dir(resolved)
	leafName := filepath.Base(resolved)
	if parentPath == resolved || leafName == "." || leafName == string(filepath.Separator) {
		return nil, ErrUnsafe
	}
	pathParentInfo, err := os.Lstat(parentPath)
	if err != nil || validateUnixCommandAuthority(
		pathParentInfo,
		uint32(os.Geteuid()), //nolint:gosec // Kernel UIDs use uint32.
	) != nil {
		return nil, ErrUnsafe
	}
	parent, err := openUnixCommandParent(parentPath)
	if err != nil {
		return nil, ErrUnsafe
	}
	keep := false
	defer func() {
		if !keep {
			_ = parent.Close()
		}
	}()

	parentInfo, err := parent.Stat()
	if err != nil || validateUnixCommandAuthority(
		parentInfo,
		uint32(os.Geteuid()), //nolint:gosec // Kernel UIDs use uint32.
	) != nil || !sameUnixCommandAuthorityIdentity(pathParentInfo, parentInfo) {
		return nil, ErrUnsafe
	}
	leaf, err := readUnixCommandAnchor(parent, leafName)
	if err != nil || validateUnixCommandAnchor(leaf) != nil ||
		!sameUnixCommandAnchorFileInfo(leaf, validatedInfo) {
		return nil, ErrUnsafe
	}

	rechecked, pathInfo, err := resolveUnixCommand(clean)
	if err != nil || rechecked != resolved ||
		!sameUnixCommandSnapshot(validatedInfo, pathInfo) ||
		!sameUnixCommandAnchorFileInfo(leaf, pathInfo) {
		return nil, ErrUnsafe
	}
	finalLeaf, err := readUnixCommandAnchor(parent, leafName)
	if err != nil || validateUnixCommandAnchor(finalLeaf) != nil ||
		!sameUnixCommandAnchor(leaf, finalLeaf) {
		return nil, ErrUnsafe
	}

	keep = true
	return &unixCommandInspection{
		file: parent,
		path: CommandPath{Clean: clean, Resolved: resolved},
		info: pathInfo,
		parent: &unixCommandParentEvidence{
			path: parentPath,
			name: leafName,
			info: parentInfo,
			leaf: finalLeaf,
		},
	}, nil
}

func splitUnixCommandPath(path string) []string {
	trimmed := strings.TrimPrefix(path, string(filepath.Separator))
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, string(filepath.Separator))
}

func openUnixCommand(path string, mode CommandReadMode) (*os.File, error) {
	flags := unixCommandOpenFlags(mode)
	fd, err := unix.Open(path, flags, 0)
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	file := os.NewFile(uintptr(fd), "trusted-command")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrUnsafe
	}
	return file, nil
}

func openUnixCommandParent(path string) (*os.File, error) {
	const darwinSearch = 0x40100000 // O_EXEC | O_DIRECTORY (O_SEARCH).
	fd, err := unix.Open(
		path,
		darwinSearch|unixCommandNoFollowFlag()|unixCommandCloseOnExecFlag(),
		0,
	)
	if err != nil {
		return nil, err
	}
	unix.CloseOnExec(fd)
	file := os.NewFile(uintptr(fd), "trusted-command-parent")
	if file == nil {
		_ = unix.Close(fd)
		return nil, ErrUnsafe
	}
	return file, nil
}

func readUnixCommandAnchor(parent *os.File, name string) (unixCommandAnchor, error) {
	if parent == nil || name == "" || name == "." || strings.ContainsRune(name, filepath.Separator) {
		return unixCommandAnchor{}, ErrUnsafe
	}
	var stat unix.Stat_t
	if err := unix.Fstatat(
		int(parent.Fd()),
		name,
		&stat,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return unixCommandAnchor{}, ErrUnsafe
	}
	device, ok := unixCommandUnsignedInteger(reflect.ValueOf(stat.Dev))
	if !ok {
		return unixCommandAnchor{}, ErrUnsafe
	}
	return unixCommandAnchor{
		dev:   device,
		ino:   stat.Ino,
		mode:  uint32(stat.Mode), //nolint:unconvert // Stat_t.Mode width differs across supported Unix targets.
		uid:   stat.Uid,
		gid:   stat.Gid,
		nlink: uint64(stat.Nlink), //nolint:unconvert // Stat_t.Nlink width differs across supported Unix targets.
		size:  stat.Size,
	}, nil
}

func validateUnixCommandAnchor(anchor unixCommandAnchor) error {
	mode := anchor.mode
	effectiveUID := uint32(os.Geteuid()) //nolint:gosec // Kernel UIDs use uint32.
	if mode&uint32(unix.S_IFMT) != uint32(unix.S_IFREG) ||
		(anchor.uid != 0 && anchor.uid != effectiveUID) ||
		mode&0o111 == 0 || mode&0o022 != 0 ||
		mode&uint32(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return ErrUnsafe
	}
	return nil
}

func sameUnixCommandAnchor(left, right unixCommandAnchor) bool {
	return left == right
}

func sameUnixCommandAnchorFileInfo(anchor unixCommandAnchor, info fs.FileInfo) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false
	}
	device, ok := unixCommandUnsignedInteger(reflect.ValueOf(stat.Dev))
	return ok && anchor.dev == device &&
		anchor.ino == stat.Ino &&
		anchor.mode == uint32(stat.Mode) && //nolint:unconvert // Stat_t.Mode width differs across supported Unix targets.
		anchor.uid == stat.Uid && anchor.gid == stat.Gid &&
		anchor.nlink == uint64(stat.Nlink) && //nolint:unconvert // Stat_t.Nlink width differs across supported Unix targets.
		anchor.size == stat.Size
}

func unixCommandUnsignedInteger(value reflect.Value) (uint64, bool) {
	if !value.IsValid() {
		return 0, false
	}
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if value.Int() < 0 {
			if value.Kind() == reflect.Int32 {
				return uint64(uint32(value.Int())), true // #nosec G115 -- preserves Darwin dev_t identity bits.
			}
			return 0, false
		}
		return uint64(value.Int()), true //nolint:gosec // Negative values were rejected above.
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return value.Uint(), true
	case reflect.Invalid, reflect.Bool,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128,
		reflect.Array, reflect.Chan, reflect.Func, reflect.Interface,
		reflect.Map, reflect.Pointer, reflect.Slice, reflect.String,
		reflect.Struct, reflect.UnsafePointer:
		return 0, false
	default:
		return 0, false
	}
}

func sameUnixCommandAuthorityIdentity(left, right fs.FileInfo) bool {
	if left == nil || right == nil || left.Mode() != right.Mode() {
		return false
	}
	leftStat, leftOK := left.Sys().(*syscall.Stat_t)
	rightStat, rightOK := right.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && leftStat.Dev == rightStat.Dev &&
		leftStat.Ino == rightStat.Ino && leftStat.Uid == rightStat.Uid &&
		leftStat.Gid == rightStat.Gid
}

func unixCommandOpenFlags(mode CommandReadMode) int {
	flags := unixCommandNoFollowFlag() | unixCommandCloseOnExecFlag()
	if mode == CommandIdentityOnly {
		flags |= unixCommandMetadataFlag()
	}
	return flags
}

func unixCommandMetadataFlag() int {
	switch runtime.GOOS {
	case "darwin":
		return 0x40000000 // O_EXEC
	case "linux":
		return 0x200000 // O_PATH on supported Linux architectures.
	case "freebsd":
		return 0x400000 // O_PATH
	default:
		return unix.O_RDONLY
	}
}

func unixCommandNoFollowFlag() int {
	switch runtime.GOOS {
	case "linux":
		return 0x20000 // O_NOFOLLOW
	default:
		return 0x100 // O_NOFOLLOW on Darwin and the supported BSDs.
	}
}

func unixCommandCloseOnExecFlag() int {
	return unix.O_CLOEXEC
}

func classifyUnixCommandOpen(err error) error {
	if errors.Is(err, fs.ErrNotExist) {
		return ErrMissing
	}
	return ErrUnsafe
}

func sameUnixCommandSnapshot(left, right fs.FileInfo) bool {
	if left == nil || right == nil ||
		left.Mode() != right.Mode() || left.Size() != right.Size() ||
		!left.ModTime().Equal(right.ModTime()) {
		return false
	}
	leftStat, leftOK := left.Sys().(*syscall.Stat_t)
	rightStat, rightOK := right.Sys().(*syscall.Stat_t)
	return leftOK && rightOK && leftStat.Dev == rightStat.Dev &&
		leftStat.Ino == rightStat.Ino && leftStat.Uid == rightStat.Uid &&
		leftStat.Gid == rightStat.Gid
}

func validateUnixFreshCommandEvidence(original, fresh fs.FileInfo) error {
	if validateUnixCommandLeaf(fresh) != nil ||
		!sameUnixCommandSnapshot(original, fresh) {
		return ErrUnsafe
	}
	return nil
}
