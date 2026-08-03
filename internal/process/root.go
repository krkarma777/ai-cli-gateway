package process

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	lockName          = ".lock"
	requestPrefix     = "request-"
	quarantinePrefix  = "quarantine-"
	runtimeDirMode    = fs.FileMode(0o700)
	runtimeFileMode   = fs.FileMode(0o600)
	geminiSettingsDir = ".gemini"
	geminiSettings    = "settings.json"
	directoryBatch    = 64
	maxFileNameBytes  = 255
	maxCleanupTimeout = 5 * time.Second
)

var geminiSettingsPath = filepath.Join(geminiSettingsDir, geminiSettings)

var (
	errRootClosed       = errors.New("runtime root is closed")
	errInvalidRuntime   = errors.New("runtime does not belong to root")
	errSymlinkRefused   = errors.New("symlink refused")
	errUnsafeRuntimeDir = errors.New("unsafe runtime directory")
)

// ErrRootLocked identifies exclusive runtime-root lock contention.
var ErrRootLocked = errors.New("runtime root is locked")

type runtimeState uint8

const (
	runtimeActive runtimeState = iota
	runtimeQuarantined
	runtimeRemoved
)

type runtimeRecord struct {
	mu          sync.Mutex
	state       runtimeState
	generation  uint64
	requestRoot *os.Root
	requestDir  *os.File
	requestInfo fs.FileInfo
}

// Root is an exclusively locked owner of request-local runtime directories.
type Root struct {
	path     string
	anchor   *os.Root
	rootDir  *os.File
	lock     *os.File
	rootInfo fs.FileInfo

	lifecycle sync.RWMutex
	closed    bool

	recordsMu  sync.Mutex
	records    map[string]*runtimeRecord
	generation uint64
}

// OpenRoot validates, creates if needed, and exclusively locks an absolute
// runtime root.
func OpenRoot(path string) (*Root, error) {
	clean := filepath.Clean(path)
	if !filepath.IsAbs(clean) {
		return nil, errors.New("runtime root must be absolute")
	}
	originalInfo, originalErr := os.Lstat(clean)
	if originalErr == nil && originalInfo.Mode()&fs.ModeSymlink != 0 {
		return nil, errUnsafeRuntimeDir
	}
	if originalErr != nil && !errors.Is(originalErr, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect runtime root: %w", originalErr)
	}
	clean, err := canonicalizeRootPath(clean)
	if err != nil {
		return nil, fmt.Errorf("canonicalize runtime root: %w", err)
	}
	if err := validateImmediateParent(clean); err != nil {
		return nil, err
	}

	info, err := os.Lstat(clean)
	createdRoot := false
	switch {
	case errors.Is(err, fs.ErrNotExist):
		if err := createSecureDirectory(clean); err != nil {
			return nil, fmt.Errorf("create runtime root: %w", err)
		}
		createdRoot = true
		if err := bootstrapCreatedRootMode(clean); err != nil {
			_ = os.Remove(clean)
			return nil, fmt.Errorf("bootstrap runtime root mode: %w", err)
		}
		info, err = os.Lstat(clean)
	case err != nil:
		return nil, fmt.Errorf("inspect runtime root: %w", err)
	}
	if err != nil {
		return nil, fmt.Errorf("inspect created runtime root: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		return nil, errUnsafeRuntimeDir
	}
	opened := false
	defer func() {
		if createdRoot && !opened {
			_ = os.Remove(clean)
		}
	}()
	anchor, err := os.OpenRoot(clean)
	if err != nil {
		return nil, fmt.Errorf("anchor runtime root: %w", err)
	}
	keepAnchor := false
	defer func() {
		if !keepAnchor {
			_ = anchor.Close()
		}
	}()
	rootDir, err := anchor.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open runtime root handle: %w", err)
	}
	keepRootDir := false
	defer func() {
		if !keepRootDir {
			_ = rootDir.Close()
		}
	}()
	rootHandleInfo, err := rootDir.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect runtime root handle: %w", err)
	}
	if !os.SameFile(info, rootHandleInfo) {
		return nil, errors.New("runtime root identity changed while anchoring")
	}
	if createdRoot {
		if err := forceCreatedMode(rootDir, runtimeDirMode); err != nil {
			return nil, fmt.Errorf("set runtime root mode: %w", err)
		}
		rootHandleInfo, err = rootDir.Stat()
		if err != nil {
			return nil, fmt.Errorf("inspect secured runtime root handle: %w", err)
		}
	} else if err := validateOwnedPath(
		clean,
		info,
		true,
		runtimeDirMode,
	); err != nil {
		return nil, fmt.Errorf("validate runtime root: %w", err)
	}
	if err := validateOwnedFile(
		rootDir,
		rootHandleInfo,
		true,
		runtimeDirMode,
	); err != nil {
		return nil, fmt.Errorf("validate runtime root handle: %w", err)
	}

	lock, createdLock, err := rOpenLockFile(anchor)
	if err != nil {
		return nil, fmt.Errorf("open anchored runtime lock: %w", err)
	}
	keepLock := false
	defer func() {
		if !keepLock {
			closeUnretainedLock(lock)
		}
	}()
	if createdLock {
		if err := forceCreatedMode(lock, runtimeFileMode); err != nil {
			return nil, fmt.Errorf("set runtime lock mode: %w", err)
		}
	}

	lockInfo, err := lock.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect runtime lock: %w", err)
	}
	if same, err := sameFilesystem(rootDir, lock); err != nil {
		return nil, fmt.Errorf("inspect runtime lock filesystem: %w", err)
	} else if !same {
		return nil, errors.New("cross-filesystem runtime lock refused")
	}
	if err := validateOwnedFile(lock, lockInfo, false, runtimeFileMode); err != nil {
		return nil, fmt.Errorf("validate runtime lock: %w", err)
	}
	lockPathInfo, err := anchor.Lstat(lockName)
	if err != nil {
		return nil, fmt.Errorf("inspect anchored runtime lock: %w", err)
	}
	if lockPathInfo.Mode()&fs.ModeSymlink != 0 ||
		!os.SameFile(lockInfo, lockPathInfo) {
		return nil, errors.New("runtime lock path changed")
	}
	if err := lockFile(lock); err != nil {
		return nil, fmt.Errorf("lock runtime root: %w", err)
	}

	root := &Root{
		path:     clean,
		anchor:   anchor,
		rootDir:  rootDir,
		lock:     lock,
		rootInfo: rootHandleInfo,
		records:  make(map[string]*runtimeRecord),
	}
	if err := root.verifyOwnedLocked(); err != nil {
		_ = unlockFile(lock)
		return nil, err
	}
	keepAnchor = true
	keepRootDir = true
	keepLock = true
	opened = true
	return root, nil
}

// closeUnretainedLock closes only this opener's handle. The on-disk lock
// sentinel is persistent: unlinking it can split lock ownership across two
// inodes when another opener wins the lock before this opener unwinds.
func closeUnretainedLock(lock *os.File) {
	if lock != nil {
		_ = lock.Close()
	}
}

// Prepare exclusively creates one 0700 request directory.
func (r *Root) Prepare(id string) (Runtime, error) {
	release, err := r.beginOperation()
	if err != nil {
		return Runtime{}, err
	}
	defer release()
	if !validRequestID(id) {
		return Runtime{}, errors.New("invalid request ID")
	}
	if err := r.validateRootPathLocked(); err != nil {
		return Runtime{}, err
	}

	r.recordsMu.Lock()
	defer r.recordsMu.Unlock()
	if _, exists := r.records[id]; exists {
		return Runtime{}, errors.New("request ID already used")
	}

	dir := r.requestPath(id)
	requestName := requestPrefix + id
	quarantineName := quarantinePrefix + id
	if _, err := r.anchor.Lstat(quarantineName); err == nil {
		return Runtime{}, errors.New("request quarantine already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return Runtime{}, fmt.Errorf("inspect request quarantine: %w", err)
	}
	if err := r.anchor.Mkdir(requestName, runtimeDirMode); err != nil {
		return Runtime{}, fmt.Errorf("create request directory: %w", err)
	}
	if err := r.anchor.Chmod(requestName, runtimeDirMode); err != nil {
		_ = r.anchor.Remove(requestName)
		return Runtime{}, fmt.Errorf("bootstrap request directory mode: %w", err)
	}
	info, err := r.anchor.Lstat(requestName)
	if err != nil {
		return Runtime{}, fmt.Errorf("inspect request directory: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 || !info.IsDir() {
		_ = r.anchor.Remove(requestName)
		return Runtime{}, errUnsafeRuntimeDir
	}
	requestRoot, err := r.anchor.OpenRoot(requestName)
	if err != nil {
		_ = r.anchor.Remove(requestName)
		return Runtime{}, fmt.Errorf("anchor request directory: %w", err)
	}
	requestDir, err := requestRoot.Open(".")
	if err != nil {
		_ = requestRoot.Close()
		_ = r.anchor.Remove(requestName)
		return Runtime{}, fmt.Errorf("open request directory handle: %w", err)
	}
	if err := forceCreatedMode(requestDir, runtimeDirMode); err != nil {
		_ = requestDir.Close()
		_ = requestRoot.Close()
		_ = r.anchor.Remove(requestName)
		return Runtime{}, fmt.Errorf("set request directory mode: %w", err)
	}
	requestHandleInfo, err := requestDir.Stat()
	if err != nil {
		_ = requestDir.Close()
		_ = requestRoot.Close()
		_ = r.anchor.Remove(requestName)
		return Runtime{}, fmt.Errorf("inspect request directory handle: %w", err)
	}
	if !os.SameFile(info, requestHandleInfo) {
		_ = requestDir.Close()
		_ = requestRoot.Close()
		_ = r.anchor.Remove(requestName)
		return Runtime{}, errors.New("request directory identity changed while anchoring")
	}
	if same, err := sameFilesystem(r.rootDir, requestDir); err != nil {
		_ = requestDir.Close()
		_ = requestRoot.Close()
		_ = r.anchor.Remove(requestName)
		return Runtime{}, fmt.Errorf("inspect request directory filesystem: %w", err)
	} else if !same {
		_ = requestDir.Close()
		_ = requestRoot.Close()
		_ = r.anchor.Remove(requestName)
		return Runtime{}, errors.New("cross-filesystem request directory refused")
	}
	if err := validateOwnedFile(
		requestDir,
		requestHandleInfo,
		true,
		runtimeDirMode,
	); err != nil {
		_ = requestDir.Close()
		_ = requestRoot.Close()
		_ = r.anchor.Remove(requestName)
		return Runtime{}, fmt.Errorf("validate request directory handle: %w", err)
	}
	if err := r.validateRootPathLocked(); err != nil {
		_ = requestDir.Close()
		_ = requestRoot.Close()
		_ = r.anchor.Remove(requestName)
		return Runtime{}, err
	}
	if err := validatePublicDirectoryPath(dir, requestHandleInfo); err != nil {
		_ = requestDir.Close()
		_ = requestRoot.Close()
		_ = r.anchor.Remove(requestName)
		return Runtime{}, fmt.Errorf("validate public request path: %w", err)
	}

	r.generation++
	if r.generation == 0 {
		r.generation++
	}
	record := &runtimeRecord{
		state:       runtimeActive,
		generation:  r.generation,
		requestRoot: requestRoot,
		requestDir:  requestDir,
		requestInfo: requestHandleInfo,
	}
	r.records[id] = record
	return Runtime{
		ID:         id,
		Dir:        dir,
		owner:      r,
		record:     record,
		generation: record.generation,
	}, nil
}

// Materialize creates request files exclusively, forces 0600 access, writes,
// syncs, and closes each file.
func (r *Root) Materialize(runtime Runtime, specs []FileSpec) error {
	release, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer release()

	record, err := r.lockRuntimeRecord(runtime, false)
	if err != nil {
		return err
	}
	defer record.mu.Unlock()
	if record.state != runtimeActive {
		return errInvalidRuntime
	}
	if err := validateRuntimeRecordDirectory(record); err != nil {
		return err
	}
	if err := r.validateRuntimePathLocked(runtime, record); err != nil {
		return err
	}
	if err := validateFileSpecs(specs); err != nil {
		return err
	}
	if isGeminiSettingsSpec(specs) {
		materialized, err := materializeGeminiSettings(record, specs[0], nil, nil, nil)
		if err != nil {
			return err
		}
		if err := r.validateRuntimePathLocked(runtime, record); err != nil {
			return rollbackGeminiSettings(record, materialized, err)
		}
		return nil
	}

	created := make([]string, 0, len(specs))
	for _, spec := range specs {
		file, err := record.requestRoot.OpenFile(
			spec.Name,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			runtimeFileMode,
		)
		if err != nil {
			return rollbackMaterializedFiles(
				record.requestRoot,
				created,
				fmt.Errorf("create request file: %w", err),
			)
		}
		created = append(created, spec.Name)
		if err := forceCreatedMode(file, runtimeFileMode); err != nil {
			return rollbackMaterializedFiles(
				record.requestRoot,
				created,
				errors.Join(
					fmt.Errorf("set request file mode: %w", err),
					file.Close(),
				),
			)
		}
		if err := validateNewAnchoredFile(record.requestRoot, spec.Name, file); err != nil {
			return rollbackMaterializedFiles(
				record.requestRoot,
				created,
				errors.Join(err, file.Close()),
			)
		}
		if err := writeSyncClose(file, spec.Data); err != nil {
			return rollbackMaterializedFiles(
				record.requestRoot,
				created,
				fmt.Errorf("materialize request file: %w", err),
			)
		}
	}
	if err := r.validateRuntimePathLocked(runtime, record); err != nil {
		return rollbackMaterializedFiles(record.requestRoot, created, err)
	}
	return nil
}

// Cleanup removes a request runtime within a bounded context. If removal fails,
// it attempts an in-root atomic rename to the request's closed quarantine name.
func (r *Root) Cleanup(ctx context.Context, runtime Runtime) error {
	release, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer release()

	record, err := r.lockRuntimeRecord(runtime, true)
	if err != nil {
		return err
	}
	retire := false
	defer func() {
		record.mu.Unlock()
		if retire {
			r.retireRecord(runtime.ID, record)
		}
	}()

	cleanupCtx, cancel := boundedCleanupContext(ctx)
	defer cancel()

	switch record.state {
	case runtimeActive:
		quarantined, quarantineErr := r.quarantineRecordedRuntime(
			cleanupCtx,
			runtime.ID,
			record,
		)
		if quarantined {
			record.state = runtimeQuarantined
		}
		if quarantineErr != nil {
			return &RunError{Kind: ErrorCleanup, Err: quarantineErr}
		}
	case runtimeQuarantined:
	case runtimeRemoved:
		return nil
	default:
		return errInvalidRuntime
	}

	err = r.removeRecordedRuntime(
		cleanupCtx,
		quarantinePrefix+runtime.ID,
		record,
	)
	if err != nil {
		return &RunError{Kind: ErrorCleanup, Err: err}
	}
	record.state = runtimeRemoved
	_ = closeRuntimeRecord(record)
	retire = true
	return nil
}

// Janitor removes only stale directory names in the closed request/quarantine
// namespace. It refuses symlink entries and never follows them.
func (r *Root) Janitor(ctx context.Context) error {
	release, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer release()

	cleanupCtx, cancel := boundedCleanupContext(ctx)
	defer cancel()
	if err := cleanupCtx.Err(); err != nil {
		return &RunError{Kind: ErrorCleanup, Err: err}
	}
	rootEntries, err := r.anchor.Open(".")
	if err != nil {
		return &RunError{Kind: ErrorCleanup, Err: err}
	}
	defer func() {
		_ = rootEntries.Close()
	}()

	var cleanupErrors []error
	for {
		if err := cleanupCtx.Err(); err != nil {
			cleanupErrors = append(cleanupErrors, err)
			break
		}
		entries, readErr := rootEntries.ReadDir(directoryBatch)
		for _, entry := range entries {
			if err := cleanupCtx.Err(); err != nil {
				cleanupErrors = append(cleanupErrors, err)
				break
			}
			id, prefix, ok := parseClosedRuntimeName(entry.Name())
			if !ok {
				continue
			}

			record, stale := r.lockStaleRuntime(id, prefix)
			if !stale {
				continue
			}
			cleanupErr := func() error {
				defer r.unlockStaleRuntime(record)
				if err := cleanupCtx.Err(); err != nil {
					return err
				}
				if err := r.removeAnchoredDirectory(
					cleanupCtx,
					r.anchor,
					entry.Name(),
				); err != nil {
					return err
				}
				if record != nil &&
					prefix == quarantinePrefix &&
					record.state == runtimeQuarantined {
					record.state = runtimeRemoved
					_ = closeRuntimeRecord(record)
					if r.records[id] == record {
						delete(r.records, id)
					}
				}
				return nil
			}()
			if cleanupErr != nil {
				cleanupErrors = append(cleanupErrors, cleanupErr)
				if cleanupCtx.Err() != nil {
					break
				}
			}
		}
		if cleanupCtx.Err() != nil {
			break
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			cleanupErrors = append(cleanupErrors, readErr)
			break
		}
	}
	if len(cleanupErrors) != 0 {
		return &RunError{Kind: ErrorCleanup, Err: errors.Join(cleanupErrors...)}
	}
	return nil
}

// Close releases the root lock. It is idempotent.
func (r *Root) Close() error {
	if r == nil {
		return nil
	}
	r.lifecycle.Lock()
	defer r.lifecycle.Unlock()
	if r.closed {
		return nil
	}
	r.closed = true
	recordsErr := r.closeRuntimeRecords()
	lock := r.lock
	r.lock = nil
	rootDir := r.rootDir
	r.rootDir = nil
	anchor := r.anchor
	r.anchor = nil
	if lock == nil {
		return errors.Join(
			recordsErr,
			closeFile(rootDir),
			closeRoot(anchor),
		)
	}
	unlockErr := unlockFile(lock)
	closeErr := lock.Close()
	return errors.Join(
		recordsErr,
		unlockErr,
		closeErr,
		closeFile(rootDir),
		closeRoot(anchor),
	)
}

func (r *Root) closeRuntimeRecords() error {
	r.recordsMu.Lock()
	defer r.recordsMu.Unlock()
	var errs []error
	for _, record := range r.records {
		record.mu.Lock()
		errs = append(errs, closeRuntimeRecord(record))
		record.mu.Unlock()
	}
	return errors.Join(errs...)
}

func (r *Root) beginOperation() (func(), error) {
	if r == nil {
		return nil, errRootClosed
	}
	r.lifecycle.RLock()
	if r.closed || r.anchor == nil || r.rootDir == nil || r.lock == nil {
		r.lifecycle.RUnlock()
		return nil, errRootClosed
	}
	if err := r.verifyOwnedLocked(); err != nil {
		r.lifecycle.RUnlock()
		return nil, err
	}
	return r.lifecycle.RUnlock, nil
}

func (r *Root) verifyOwnedLocked() error {
	info, err := r.rootDir.Stat()
	if err != nil {
		return fmt.Errorf("inspect runtime root handle: %w", err)
	}
	if !os.SameFile(r.rootInfo, info) {
		return errors.New("runtime root identity changed")
	}
	if err := validateOwnedFile(
		r.rootDir,
		info,
		true,
		runtimeDirMode,
	); err != nil {
		return fmt.Errorf("validate runtime root handle: %w", err)
	}

	lockInfo, err := r.lock.Stat()
	if err != nil {
		return fmt.Errorf("inspect runtime lock: %w", err)
	}
	pathInfo, err := r.anchor.Lstat(lockName)
	if err != nil {
		return fmt.Errorf("inspect anchored runtime lock: %w", err)
	}
	if pathInfo.Mode()&fs.ModeSymlink != 0 ||
		!os.SameFile(lockInfo, pathInfo) {
		return errors.New("runtime lock identity changed")
	}
	if err := validateOwnedFile(r.lock, lockInfo, false, runtimeFileMode); err != nil {
		return fmt.Errorf("validate runtime lock: %w", err)
	}
	return nil
}

func (r *Root) validateRootPathLocked() error {
	if err := validatePublicDirectoryPath(r.path, r.rootInfo); err != nil {
		return fmt.Errorf("validate public runtime root path: %w", err)
	}
	return nil
}

// validateRuntimePath is the final same-package handoff check for a provider
// launch that will use Runtime.Dir as its working directory.
func (r *Root) validateRuntimePath(runtime Runtime) error {
	release, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer release()
	record, err := r.lockRuntimeRecord(runtime, false)
	if err != nil {
		return err
	}
	defer record.mu.Unlock()
	if record.state != runtimeActive {
		return errInvalidRuntime
	}
	if err := validateRuntimeRecordDirectory(record); err != nil {
		return err
	}
	return r.validateRuntimePathLocked(runtime, record)
}

func (r *Root) validateRuntimePathLocked(
	runtime Runtime,
	record *runtimeRecord,
) error {
	if err := r.validateRootPathLocked(); err != nil {
		return err
	}
	if err := validatePublicDirectoryPath(
		runtime.Dir,
		record.requestInfo,
	); err != nil {
		return fmt.Errorf("validate public request path: %w", err)
	}
	return nil
}

func validatePublicDirectoryPath(path string, expected fs.FileInfo) error {
	if expected == nil {
		return errUnsafeRuntimeDir
	}
	info, err := lstatNoLinkDirectory(path)
	if err != nil {
		return err
	}
	if !os.SameFile(expected, info) {
		return errors.New("public directory path identity changed")
	}
	return nil
}

func (r *Root) lockRuntimeRecord(
	runtime Runtime,
	allowRetired bool,
) (*runtimeRecord, error) {
	if !validRequestID(runtime.ID) ||
		runtime.Dir != r.requestPath(runtime.ID) ||
		runtime.owner != r ||
		runtime.record == nil ||
		runtime.generation == 0 ||
		runtime.record.generation != runtime.generation {
		return nil, errInvalidRuntime
	}
	r.recordsMu.Lock()
	current := r.records[runtime.ID]
	record := runtime.record
	if current != record && (!allowRetired || current != nil) {
		r.recordsMu.Unlock()
		return nil, errInvalidRuntime
	}
	record.mu.Lock()
	if current == nil && record.state != runtimeRemoved {
		record.mu.Unlock()
		r.recordsMu.Unlock()
		return nil, errInvalidRuntime
	}
	r.recordsMu.Unlock()
	return record, nil
}

func (r *Root) retireRecord(id string, record *runtimeRecord) {
	r.recordsMu.Lock()
	defer r.recordsMu.Unlock()
	if r.records[id] != record {
		return
	}
	record.mu.Lock()
	defer record.mu.Unlock()
	if record.state == runtimeRemoved {
		delete(r.records, id)
	}
}

func runtimeRecordIsLive(record *runtimeRecord, prefix string) bool {
	switch prefix {
	case requestPrefix:
		return record.state == runtimeActive
	case quarantinePrefix:
		return false
	default:
		return false
	}
}

func (r *Root) lockStaleRuntime(
	id string,
	prefix string,
) (*runtimeRecord, bool) {
	r.recordsMu.Lock()
	record := r.records[id]
	if record != nil {
		record.mu.Lock()
	}
	if record != nil && runtimeRecordIsLive(record, prefix) {
		record.mu.Unlock()
		r.recordsMu.Unlock()
		return nil, false
	}
	return record, true
}

func (r *Root) unlockStaleRuntime(record *runtimeRecord) {
	if record != nil {
		record.mu.Unlock()
	}
	r.recordsMu.Unlock()
}

func (r *Root) requestPath(id string) string {
	return filepath.Join(r.path, requestPrefix+id)
}

func validateImmediateParent(path string) error {
	parent := filepath.Dir(path)
	info, err := os.Lstat(parent)
	if err != nil {
		return fmt.Errorf("inspect runtime root parent: %w", err)
	}
	if info.Mode()&fs.ModeSymlink != 0 {
		return errors.New("runtime root parent is a symlink")
	}
	if !info.IsDir() {
		return errors.New("runtime root parent is not a directory")
	}
	if err := validateImmediateParentSecurity(parent, info); err != nil {
		return err
	}
	return validateRootAncestorSecurity(path)
}

func rOpenLockFile(anchor *os.Root) (*os.File, bool, error) {
	lock, err := anchor.OpenFile(
		lockName,
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		runtimeFileMode,
	)
	if err == nil {
		return lock, true, nil
	}
	if !errors.Is(err, fs.ErrExist) {
		return nil, false, err
	}
	entryInfo, err := anchor.Lstat(lockName)
	if err != nil {
		return nil, false, err
	}
	if entryInfo.Mode()&fs.ModeSymlink != 0 ||
		!entryInfo.Mode().IsRegular() {
		return nil, false, errors.New("unsafe runtime lock entry")
	}
	lock, err = anchor.OpenFile(lockName, os.O_RDWR, 0)
	if err != nil {
		return nil, false, err
	}
	lockInfo, err := lock.Stat()
	if err != nil {
		_ = lock.Close()
		return nil, false, err
	}
	if !os.SameFile(entryInfo, lockInfo) {
		_ = lock.Close()
		return nil, false, errors.New("runtime lock identity changed while opening")
	}
	return lock, false, nil
}

func validRequestID(id string) bool {
	if len(id) < 8 || len(id) > 80 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		if (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '_' ||
			c == '-' {
			continue
		}
		return false
	}
	return true
}

func parseClosedRuntimeName(name string) (id, prefix string, ok bool) {
	for _, candidate := range []string{requestPrefix, quarantinePrefix} {
		if strings.HasPrefix(name, candidate) {
			id := strings.TrimPrefix(name, candidate)
			return id, candidate, validRequestID(id)
		}
	}
	return "", "", false
}

func validateRuntimeRecordDirectory(record *runtimeRecord) error {
	if record == nil ||
		record.requestRoot == nil ||
		record.requestDir == nil ||
		record.requestInfo == nil {
		return errUnsafeRuntimeDir
	}
	info, err := record.requestDir.Stat()
	if err != nil {
		return fmt.Errorf("inspect request directory handle: %w", err)
	}
	if !os.SameFile(record.requestInfo, info) {
		return errUnsafeRuntimeDir
	}
	if err := validateOwnedFile(
		record.requestDir,
		info,
		true,
		runtimeDirMode,
	); err != nil {
		return fmt.Errorf("validate request directory handle: %w", err)
	}
	return nil
}

func validateFileSpecs(specs []FileSpec) error {
	if isGeminiSettingsSpec(specs) {
		return nil
	}
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if !validFileName(spec.Name) {
			return errors.New("request file name must be a safe bounded base name")
		}
		key := strings.ToLower(spec.Name)
		if _, exists := seen[key]; exists {
			return errors.New("duplicate request file name")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func isGeminiSettingsSpec(specs []FileSpec) bool {
	return len(specs) == 1 && specs[0].Name == geminiSettingsPath
}

type geminiMaterialization struct {
	directoryCreated bool
	directoryInfo    fs.FileInfo
	fileInfo         fs.FileInfo
}

func materializeGeminiSettings(
	record *runtimeRecord,
	spec FileSpec,
	afterMkdir func() error,
	beforeWrite func() error,
	afterWrite func() error,
) (geminiMaterialization, error) {
	var materialized geminiMaterialization
	if record == nil || record.requestRoot == nil || record.requestDir == nil ||
		spec.Name != geminiSettingsPath {
		return materialized, errInvalidRuntime
	}
	if _, err := record.requestRoot.Lstat(geminiSettingsDir); err == nil {
		return materialized, errors.New("gemini settings directory already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return materialized, errors.New("inspect Gemini settings directory")
	}
	if err := record.requestRoot.Mkdir(geminiSettingsDir, runtimeDirMode); err != nil {
		return materialized, errors.New("create Gemini settings directory")
	}
	materialized.directoryCreated = true

	directoryInfo, err := record.requestRoot.Lstat(geminiSettingsDir)
	if err != nil {
		return materialized, rollbackGeminiSettings(
			record,
			materialized,
			errors.New("inspect created Gemini settings directory"),
		)
	}
	materialized.directoryInfo = directoryInfo
	if directoryInfo.Mode()&fs.ModeSymlink != 0 || !directoryInfo.IsDir() {
		return materialized, rollbackGeminiSettings(
			record,
			materialized,
			errors.New("created Gemini settings directory is unsafe"),
		)
	}
	if afterMkdir != nil {
		if err := afterMkdir(); err != nil {
			return materialized, rollbackGeminiSettings(record, materialized, err)
		}
	}
	if err := record.requestRoot.Chmod(geminiSettingsDir, runtimeDirMode); err != nil {
		return materialized, rollbackGeminiSettings(
			record,
			materialized,
			errors.New("set Gemini settings directory mode before anchoring"),
		)
	}
	currentDirectoryInfo, err := record.requestRoot.Lstat(geminiSettingsDir)
	if err != nil || currentDirectoryInfo.Mode()&fs.ModeSymlink != 0 ||
		!currentDirectoryInfo.IsDir() ||
		!os.SameFile(materialized.directoryInfo, currentDirectoryInfo) {
		return materialized, rollbackGeminiSettings(
			record,
			materialized,
			errors.New("validate Gemini settings directory before anchoring"),
		)
	}

	opened, err := openAnchoredDirectory(
		record.requestRoot,
		geminiSettingsDir,
		currentDirectoryInfo,
		record.requestDir,
	)
	if err != nil {
		return materialized, rollbackGeminiSettings(
			record,
			materialized,
			errors.New("anchor Gemini settings directory"),
		)
	}
	fail := func(cause error, file *os.File) (geminiMaterialization, error) {
		cause = errors.Join(cause, closeFile(file), opened.Close())
		return materialized, rollbackGeminiSettings(record, materialized, cause)
	}

	if err := forceCreatedMode(opened.file, runtimeDirMode); err != nil {
		return fail(errors.New("set Gemini settings directory mode"), nil)
	}
	directoryHandleInfo, err := opened.file.Stat()
	if err != nil {
		return fail(errors.New("inspect Gemini settings directory handle"), nil)
	}
	if !os.SameFile(materialized.directoryInfo, directoryHandleInfo) {
		return fail(errors.New("gemini settings directory identity changed"), nil)
	}
	if err := validateOwnedFile(
		opened.file,
		directoryHandleInfo,
		true,
		runtimeDirMode,
	); err != nil {
		return fail(errors.New("validate Gemini settings directory handle"), nil)
	}
	currentDirectoryInfo, err = record.requestRoot.Lstat(geminiSettingsDir)
	if err != nil || currentDirectoryInfo.Mode()&fs.ModeSymlink != 0 ||
		!currentDirectoryInfo.IsDir() ||
		!os.SameFile(materialized.directoryInfo, currentDirectoryInfo) {
		return fail(errors.New("validate anchored Gemini settings directory"), nil)
	}

	file, err := opened.root.OpenFile(
		geminiSettings,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		runtimeFileMode,
	)
	if err != nil {
		return fail(errors.New("create Gemini settings file"), nil)
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return fail(errors.New("inspect created Gemini settings file"), file)
	}
	materialized.fileInfo = fileInfo
	if err := forceCreatedMode(file, runtimeFileMode); err != nil {
		return fail(errors.New("set Gemini settings file mode"), file)
	}
	if err := validateNewAnchoredFile(opened.root, geminiSettings, file); err != nil {
		return fail(errors.New("validate Gemini settings file"), file)
	}
	if beforeWrite != nil {
		if err := beforeWrite(); err != nil {
			return fail(err, file)
		}
	}
	if err := writeSyncClose(file, spec.Data); err != nil {
		return fail(errors.New("materialize Gemini settings file"), nil)
	}
	if err := validateGeminiSettingsFile(
		opened.root,
		materialized.fileInfo,
	); err != nil {
		return fail(err, nil)
	}
	if afterWrite != nil {
		if err := afterWrite(); err != nil {
			return fail(err, nil)
		}
	}
	if err := validateGeminiSettingsFile(
		opened.root,
		materialized.fileInfo,
	); err != nil {
		return fail(err, nil)
	}
	directoryHandleInfo, err = opened.file.Stat()
	if err != nil {
		return fail(errors.New("inspect Gemini settings directory after write"), nil)
	}
	if err := validateOwnedFile(
		opened.file,
		directoryHandleInfo,
		true,
		runtimeDirMode,
	); err != nil {
		return fail(errors.New("validate Gemini settings directory after write"), nil)
	}
	currentDirectoryInfo, err = record.requestRoot.Lstat(geminiSettingsDir)
	if err != nil || currentDirectoryInfo.Mode()&fs.ModeSymlink != 0 ||
		!currentDirectoryInfo.IsDir() ||
		!os.SameFile(materialized.directoryInfo, currentDirectoryInfo) ||
		!os.SameFile(directoryHandleInfo, currentDirectoryInfo) {
		return fail(errors.New("gemini settings directory identity changed after write"), nil)
	}
	if err := opened.Close(); err != nil {
		return materialized, rollbackGeminiSettings(
			record,
			materialized,
			errors.New("close Gemini settings directory"),
		)
	}
	return materialized, nil
}

func validateGeminiSettingsFile(
	settingsRoot *os.Root,
	expected fs.FileInfo,
) (result error) {
	if settingsRoot == nil || expected == nil {
		return errors.New("gemini settings file identity is unavailable")
	}
	file, err := settingsRoot.Open(geminiSettings)
	if err != nil {
		return errors.New("open Gemini settings file for final validation")
	}
	defer func() {
		if err := file.Close(); err != nil {
			result = errors.Join(
				result,
				errors.New("close Gemini settings file after final validation"),
			)
		}
	}()

	handleInfo, err := file.Stat()
	if err != nil {
		return errors.New("inspect Gemini settings file after write")
	}
	if err := validateOwnedFile(
		file,
		handleInfo,
		false,
		runtimeFileMode,
	); err != nil {
		return errors.New("validate Gemini settings file after write")
	}
	pathInfo, err := settingsRoot.Lstat(geminiSettings)
	if err != nil || pathInfo.Mode()&fs.ModeSymlink != 0 ||
		!pathInfo.Mode().IsRegular() ||
		!os.SameFile(expected, handleInfo) ||
		!os.SameFile(handleInfo, pathInfo) {
		return errors.New("gemini settings file identity changed after write")
	}
	return nil
}

func rollbackGeminiSettings(
	record *runtimeRecord,
	materialized geminiMaterialization,
	cause error,
) error {
	errs := []error{cause}
	if record == nil || record.requestRoot == nil || record.requestDir == nil ||
		!materialized.directoryCreated {
		return errors.Join(errs...)
	}
	if materialized.directoryInfo == nil {
		if _, err := record.requestRoot.Lstat(geminiSettingsDir); errors.Is(err, fs.ErrNotExist) {
			return errors.Join(errs...)
		}
		errs = append(errs, errors.New("rollback Gemini settings directory identity unavailable"))
		return errors.Join(errs...)
	}

	currentDirectoryInfo, err := record.requestRoot.Lstat(geminiSettingsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return errors.Join(errs...)
	}
	if err != nil || currentDirectoryInfo.Mode()&fs.ModeSymlink != 0 ||
		!currentDirectoryInfo.IsDir() ||
		!os.SameFile(materialized.directoryInfo, currentDirectoryInfo) {
		errs = append(errs, errors.New("rollback Gemini settings directory identity"))
		return errors.Join(errs...)
	}

	opened, err := openAnchoredDirectory(
		record.requestRoot,
		geminiSettingsDir,
		currentDirectoryInfo,
		record.requestDir,
	)
	if err != nil {
		errs = append(errs, errors.New("rollback anchor Gemini settings directory"))
		return errors.Join(errs...)
	}
	if materialized.fileInfo != nil {
		currentFileInfo, fileErr := opened.root.Lstat(geminiSettings)
		switch {
		case errors.Is(fileErr, fs.ErrNotExist):
		case fileErr != nil:
			errs = append(errs, errors.New("rollback inspect Gemini settings file"))
		case currentFileInfo.Mode()&fs.ModeSymlink != 0 ||
			!currentFileInfo.Mode().IsRegular() ||
			!os.SameFile(materialized.fileInfo, currentFileInfo):
			errs = append(errs, errors.New("rollback Gemini settings file identity"))
		default:
			if removeErr := opened.root.Remove(geminiSettings); removeErr != nil &&
				!errors.Is(removeErr, fs.ErrNotExist) {
				errs = append(errs, errors.New("rollback Gemini settings file"))
			}
		}
	}
	if err := opened.Close(); err != nil {
		errs = append(errs, errors.New("rollback close Gemini settings directory"))
		return errors.Join(errs...)
	}

	currentDirectoryInfo, err = record.requestRoot.Lstat(geminiSettingsDir)
	if errors.Is(err, fs.ErrNotExist) {
		return errors.Join(errs...)
	}
	if err != nil || currentDirectoryInfo.Mode()&fs.ModeSymlink != 0 ||
		!currentDirectoryInfo.IsDir() ||
		!os.SameFile(materialized.directoryInfo, currentDirectoryInfo) {
		errs = append(errs, errors.New("rollback Gemini settings directory identity"))
		return errors.Join(errs...)
	}
	if err := record.requestRoot.Remove(geminiSettingsDir); err != nil &&
		!errors.Is(err, fs.ErrNotExist) {
		errs = append(errs, errors.New("rollback Gemini settings directory"))
	}
	return errors.Join(errs...)
}

func validFileName(name string) bool {
	if name == "" ||
		len(name) > maxFileNameBytes ||
		name == "." ||
		name == ".." ||
		filepath.IsAbs(name) ||
		filepath.VolumeName(name) != "" ||
		filepath.Base(name) != name ||
		strings.IndexByte(name, 0) >= 0 ||
		strings.ContainsAny(name, `/\`) ||
		!validPlatformFileName(name) {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if (c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '.' ||
			c == '_' ||
			c == '-' {
			continue
		}
		return false
	}
	return true
}

func validateNewAnchoredFile(
	requestRoot *os.Root,
	name string,
	file *os.File,
) error {
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect request file: %w", err)
	}
	if err := validateOwnedFile(
		file,
		info,
		false,
		runtimeFileMode,
	); err != nil {
		return fmt.Errorf("validate request file: %w", err)
	}
	pathInfo, err := requestRoot.Lstat(name)
	if err != nil {
		return fmt.Errorf("inspect anchored request file: %w", err)
	}
	if pathInfo.Mode()&fs.ModeSymlink != 0 ||
		!os.SameFile(info, pathInfo) {
		return errors.New("request file identity changed")
	}
	return nil
}

func closeFile(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

func closeRoot(root *os.Root) error {
	if root == nil {
		return nil
	}
	return root.Close()
}

func writeSyncClose(file *os.File, data []byte) error {
	_, writeErr := file.Write(data)
	var syncErr error
	if writeErr == nil {
		syncErr = file.Sync()
	}
	closeErr := file.Close()
	return errors.Join(writeErr, syncErr, closeErr)
}

func rollbackMaterializedFiles(
	requestRoot *os.Root,
	created []string,
	cause error,
) error {
	errs := []error{cause}
	for i := len(created) - 1; i >= 0; i-- {
		if err := requestRoot.Remove(created[i]); err != nil &&
			!errors.Is(err, fs.ErrNotExist) {
			errs = append(errs, fmt.Errorf(
				"rollback request file %q: %w",
				created[i],
				err,
			))
		}
	}
	return errors.Join(errs...)
}

func boundedCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok &&
		time.Until(deadline) <= maxCleanupTimeout {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, maxCleanupTimeout)
}

func (r *Root) removeRecordedRuntime(
	ctx context.Context,
	name string,
	record *runtimeRecord,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entryInfo, err := r.anchor.Lstat(name)
	if err != nil {
		return err
	}
	if entryInfo.Mode()&fs.ModeSymlink != 0 ||
		!entryInfo.IsDir() ||
		!os.SameFile(record.requestInfo, entryInfo) {
		return errors.New(
			"recorded runtime quarantine identity does not match",
		)
	}
	opened, err := openAnchoredDirectory(
		r.anchor,
		name,
		entryInfo,
		r.rootDir,
	)
	if err != nil {
		return err
	}
	if !os.SameFile(record.requestInfo, opened.info) {
		_ = opened.Close()
		return errors.New(
			"opened runtime quarantine identity does not match",
		)
	}
	if err := validateOwnedFile(
		opened.file,
		opened.info,
		true,
		runtimeDirMode,
	); err != nil {
		_ = opened.Close()
		return err
	}
	return removeOpenedDirectory(ctx, r.anchor, name, opened, r.rootDir)
}

func (r *Root) findRecordedRuntimeName(
	ctx context.Context,
	preferredName string,
	record *runtimeRecord,
) (string, error) {
	current, err := r.anchor.Lstat(preferredName)
	if err == nil &&
		current.Mode()&fs.ModeSymlink == 0 &&
		current.IsDir() &&
		os.SameFile(record.requestInfo, current) {
		return preferredName, nil
	}
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	entries, err := r.anchor.Open(".")
	if err != nil {
		return "", err
	}
	defer func() {
		_ = entries.Close()
	}()
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		batch, readErr := entries.ReadDir(directoryBatch)
		for _, entry := range batch {
			if err := ctx.Err(); err != nil {
				return "", err
			}
			info, err := r.anchor.Lstat(entry.Name())
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return "", err
			}
			if info.Mode()&fs.ModeSymlink == 0 &&
				info.IsDir() &&
				os.SameFile(record.requestInfo, info) {
				return entry.Name(), nil
			}
		}
		if errors.Is(readErr, io.EOF) {
			return "", errors.New(
				"recorded runtime identity is not present in anchored root",
			)
		}
		if readErr != nil {
			return "", readErr
		}
	}
}

func (r *Root) removeAnchoredDirectory(
	ctx context.Context,
	parent *os.Root,
	name string,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	entryInfo, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if entryInfo.Mode()&fs.ModeSymlink != 0 {
		return fmt.Errorf("%s: %w", name, errSymlinkRefused)
	}
	if !entryInfo.IsDir() {
		return errUnsafeRuntimeDir
	}
	opened, err := openAnchoredDirectory(
		parent,
		name,
		entryInfo,
		r.rootDir,
	)
	if err != nil {
		return err
	}
	if err := validateOwnedFile(
		opened.file,
		opened.info,
		true,
		runtimeDirMode,
	); err != nil {
		_ = opened.Close()
		return err
	}
	return removeOpenedDirectory(ctx, parent, name, opened, r.rootDir)
}

func removeAnchoredContents(
	ctx context.Context,
	directory *os.Root,
	rootDir *os.File,
) error {
	entries, err := directory.Open(".")
	if err != nil {
		return err
	}
	defer func() {
		_ = entries.Close()
	}()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		batch, readErr := entries.ReadDir(directoryBatch)
		for _, entry := range batch {
			if err := ctx.Err(); err != nil {
				return err
			}
			info, err := directory.Lstat(entry.Name())
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			if err != nil {
				return err
			}
			if info.IsDir() && info.Mode()&fs.ModeSymlink == 0 {
				if err := removeAnchoredChildDirectory(
					ctx,
					directory,
					entry.Name(),
					info,
					rootDir,
				); err != nil {
					return err
				}
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := directory.Remove(entry.Name()); err != nil &&
				!errors.Is(err, fs.ErrNotExist) {
				return err
			}
		}
		if errors.Is(readErr, io.EOF) {
			return nil
		}
		if readErr != nil {
			return readErr
		}
	}
}

func removeAnchoredChildDirectory(
	ctx context.Context,
	parent *os.Root,
	name string,
	entryInfo fs.FileInfo,
	rootDir *os.File,
) error {
	opened, err := openAnchoredDirectory(
		parent,
		name,
		entryInfo,
		rootDir,
	)
	if err != nil {
		return err
	}
	return removeOpenedDirectory(ctx, parent, name, opened, rootDir)
}

type anchoredDirectory struct {
	root *os.Root
	file *os.File
	info fs.FileInfo
}

func openAnchoredDirectory(
	parent *os.Root,
	name string,
	entryInfo fs.FileInfo,
	rootDir *os.File,
) (*anchoredDirectory, error) {
	childRoot, err := parent.OpenRoot(name)
	if err != nil {
		return nil, err
	}
	childDir, err := childRoot.Open(".")
	if err != nil {
		_ = childRoot.Close()
		return nil, err
	}
	childInfo, err := childDir.Stat()
	if err != nil {
		_ = childDir.Close()
		_ = childRoot.Close()
		return nil, err
	}
	if !os.SameFile(entryInfo, childInfo) {
		_ = childDir.Close()
		_ = childRoot.Close()
		return nil, errors.New("child directory identity changed while anchoring")
	}
	if same, err := sameFilesystem(rootDir, childDir); err != nil {
		_ = childDir.Close()
		_ = childRoot.Close()
		return nil, err
	} else if !same {
		_ = childDir.Close()
		_ = childRoot.Close()
		return nil, errors.New("cross-filesystem child refused")
	}
	return &anchoredDirectory{
		root: childRoot,
		file: childDir,
		info: childInfo,
	}, nil
}

func (d *anchoredDirectory) Close() error {
	if d == nil {
		return nil
	}
	file := d.file
	root := d.root
	d.file = nil
	d.root = nil
	return errors.Join(closeFile(file), closeRoot(root))
}

func removeOpenedDirectory(
	ctx context.Context,
	parent *os.Root,
	name string,
	opened *anchoredDirectory,
	rootDir *os.File,
) error {
	if opened == nil || opened.root == nil || opened.file == nil {
		return errors.New("missing anchored directory")
	}
	if err := removeAnchoredContents(ctx, opened.root, rootDir); err != nil {
		return errors.Join(err, opened.Close())
	}
	childInfo := opened.info
	if err := opened.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	current, err := parent.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return errors.New("child directory name disappeared during cleanup")
	}
	if err != nil {
		return err
	}
	if current.Mode()&fs.ModeSymlink != 0 ||
		!os.SameFile(childInfo, current) {
		return errors.New("child directory name identity changed during cleanup")
	}
	return parent.Remove(name)
}

func (r *Root) quarantineRecordedRuntime(
	ctx context.Context,
	id string,
	record *runtimeRecord,
) (bool, error) {
	requestName := requestPrefix + id
	quarantineName := quarantinePrefix + id
	sourceName, err := r.findRecordedRuntimeName(ctx, requestName, record)
	if err != nil {
		return false, fmt.Errorf("find failed request cleanup target: %w", err)
	}
	if _, err := r.anchor.Lstat(quarantineName); err == nil {
		return false, errors.New("quarantine target already exists")
	} else if !errors.Is(err, fs.ErrNotExist) {
		return false, fmt.Errorf("inspect quarantine target: %w", err)
	}
	if err := renameRuntimeNoReplace(
		r.rootDir,
		r.anchor,
		sourceName,
		quarantineName,
	); err != nil {
		return false, fmt.Errorf("rename request quarantine: %w", err)
	}
	quarantineInfo, err := r.anchor.Lstat(quarantineName)
	if err != nil {
		return true, fmt.Errorf("inspect renamed quarantine target: %w", err)
	}
	if quarantineInfo.Mode()&fs.ModeSymlink != 0 ||
		!quarantineInfo.IsDir() ||
		!os.SameFile(record.requestInfo, quarantineInfo) {
		return true, errors.New(
			"renamed quarantine target identity does not match",
		)
	}
	return true, nil
}

func closeRuntimeRecord(record *runtimeRecord) error {
	if record == nil {
		return nil
	}
	requestDir := record.requestDir
	requestRoot := record.requestRoot
	record.requestDir = nil
	record.requestRoot = nil
	return errors.Join(closeFile(requestDir), closeRoot(requestRoot))
}
