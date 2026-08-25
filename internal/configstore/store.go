// Package configstore owns safe, transactional guided-init configuration writes.
package configstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
)

const maxConfigBytes = 1 << 20

var (
	// ErrInvalidConfig reports a bounded source or candidate rejected by the
	// production configuration decoder.
	ErrInvalidConfig = errors.New("configuration is invalid")
	// ErrUnsafePath reports a path, object, authority, or identity that cannot
	// be safely used for a configuration transaction.
	ErrUnsafePath = errors.New("configuration path is unsafe")
	// ErrStore reports a fixed operational storage failure.
	ErrStore = errors.New("configuration storage failed")
)

// Snapshot is opaque evidence for one absent or stable private configuration.
type Snapshot struct {
	path    string
	bytes   []byte
	exists  bool
	digest  [sha256.Size]byte
	file    nativeFileMetadata
	parent  nativeDirectoryEvidence
	missing []string
}

// Exists reports whether the validated source existed when loaded.
func (snapshot Snapshot) Exists() bool {
	return snapshot.exists
}

// Bytes returns a defensive copy of the validated source bytes.
func (snapshot Snapshot) Bytes() []byte {
	if !snapshot.exists || snapshot.bytes == nil {
		return nil
	}
	return append([]byte(nil), snapshot.bytes...)
}

// Path returns only the caller-confirmed normalized target path.
func (snapshot Snapshot) Path() string {
	return snapshot.path
}

type operations struct {
	afterRead     func()
	syncFile      func(*os.File) error
	syncDirectory func(*os.File) error
	commitHook    func(commitPoint) error
	operationHook func(operationKind) error
}

type operationKind uint8

const (
	operationOpen operationKind = iota + 1
	operationMkdir
	operationLock
	operationCreate
	operationWrite
	operationSyncFile
	operationStat
	operationBackupReplace
	operationBackupRestore
	operationConfigReplace
	operationDirectorySync
	operationRollback
	operationCleanup
	operationUnlock
	operationClose
)

func defaultOperations() operations {
	return operations{
		syncFile: func(file *os.File) error {
			if file == nil {
				return ErrStore
			}
			return file.Sync()
		},
		syncDirectory: nativeSyncConfigDirectory,
	}
}

// Writer performs configstore operations with per-instance dependencies.
type Writer struct {
	ops operations
}

// KeyIntent is the closed key-file operation requested by a mutation.
type KeyIntent uint8

const (
	// KeyIntentNone means the candidate does not use file-backed auth.
	KeyIntentNone KeyIntent = iota
	// KeyIntentInspect validates a previously configured key without creating it.
	KeyIntentInspect
	// KeyIntentEnsure creates a missing key or reuses only an authorized one.
	KeyIntentEnsure
)

// KeyPlan binds the candidate-derived key target to transaction intent.
type KeyPlan struct {
	Intent        KeyIntent
	Path          string
	DistinctFrom  []string
	AllowExisting bool
}

// KeyState describes the read-only key observation made by Preflight.
type KeyState uint8

const (
	// KeyStateNone means the candidate does not use file-backed auth.
	KeyStateNone KeyState = iota
	// KeyStateMissing means the key target is safely absent.
	KeyStateMissing
	// KeyStateNeedsConfirmation means a valid unapproved orphan exists.
	KeyStateNeedsConfirmation
	// KeyStateReusable means the existing key is valid and authorized for reuse.
	KeyStateReusable
)

// PreflightResult is the immutable result of read-only mutation inspection.
type PreflightResult struct {
	KeyState KeyState
}

// Mutation binds a candidate and closed key/directory plans to one base.
type Mutation struct {
	Base        Snapshot
	Candidate   []byte
	Key         KeyPlan
	PrivateDirs []string
}

// NewWriter constructs a production configuration writer.
func NewWriter() *Writer {
	return &Writer{ops: defaultOperations()}
}

func (writer *Writer) runOperationHook(operation operationKind) error {
	if writer == nil || writer.ops.operationHook == nil {
		return nil
	}
	if writer.ops.operationHook(operation) != nil {
		return ErrStore
	}
	return nil
}

// Load returns opaque evidence for one absent or stable private config.
func (writer *Writer) Load(ctx context.Context, configPath string) (Snapshot, error) {
	if writer == nil || ctx == nil {
		return Snapshot{}, ErrStore
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, fixedContextError(err)
	}
	if err := writer.runOperationHook(operationOpen); err != nil {
		return Snapshot{}, err
	}
	target, err := openNativeLoadTarget(configPath)
	if err != nil {
		return Snapshot{}, err
	}
	if target.file != nil {
		defer func() { _ = target.file.Close() }()
	}
	if !target.exists {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, fixedContextError(err)
		}
		if !revalidateNativeLoadTarget(target) {
			return Snapshot{}, ErrUnsafePath
		}
		return Snapshot{
			path: target.path, parent: target.parent,
			missing: append([]string(nil), target.missing...),
		}, nil
	}

	raw, ok := readBoundedConfig(target.file)
	if !ok {
		return Snapshot{}, ErrStore
	}
	defer clear(raw)
	if writer.ops.afterRead != nil {
		writer.ops.afterRead()
	}
	if err := ctx.Err(); err != nil {
		return Snapshot{}, fixedContextError(err)
	}
	if _, err := config.Decode(bytes.NewReader(raw)); err != nil {
		return Snapshot{}, ErrInvalidConfig
	}
	if !revalidateNativeLoadTarget(target) {
		return Snapshot{}, ErrUnsafePath
	}
	if err := target.file.Close(); err != nil {
		target.file = nil
		return Snapshot{}, ErrStore
	}
	target.file = nil
	digest := sha256.Sum256(raw)
	return Snapshot{
		path: target.path, bytes: append([]byte(nil), raw...), exists: true,
		digest: digest, file: target.metadata, parent: target.parent,
	}, nil
}

func readBoundedConfig(file *os.File) ([]byte, bool) {
	if file == nil {
		return nil, false
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, false
	}
	value, err := io.ReadAll(io.LimitReader(file, maxConfigBytes+1))
	if err != nil || len(value) > maxConfigBytes {
		clear(value)
		return nil, false
	}
	return value, true
}

func fixedContextError(err error) error {
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrStore
}

func fixedStoreError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return fixedContextError(err)
	}
	if errors.Is(err, ErrUnsafePath) {
		return ErrUnsafePath
	}
	return ErrStore
}
