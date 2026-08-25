package configstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"os"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
	"github.com/krkarma777/ai-cli-gateway/internal/gatewaykey"
)

// BackupPath returns the fixed prior-version backup target.
func BackupPath(configPath string) string {
	return configPath + ".bak"
}

// LockPath returns the persistent advisory-lock sentinel target.
func LockPath(configPath string) string {
	return configPath + ".lock"
}

// Preflight performs structural and revision inspection without mutation.
func (writer *Writer) Preflight(
	ctx context.Context,
	mutation Mutation,
) (PreflightResult, error) {
	if writer == nil || ctx == nil || ctx.Err() != nil ||
		!validSnapshotShape(mutation.Base) || len(mutation.Candidate) == 0 ||
		len(mutation.Candidate) > maxConfigBytes {
		return PreflightResult{}, ErrStore
	}
	candidate, err := config.Decode(bytes.NewReader(mutation.Candidate))
	if err != nil {
		return PreflightResult{}, ErrInvalidConfig
	}
	current, err := writer.Load(ctx, mutation.Base.path)
	if err != nil {
		return PreflightResult{}, err
	}
	if !sameSnapshotRevision(mutation.Base, current) {
		return PreflightResult{}, ErrUnsafePath
	}
	if err := inspectReservedArtifacts(mutation.Base.path); err != nil {
		return PreflightResult{}, err
	}
	if err := inspectPrivateDirectories(mutation); err != nil {
		return PreflightResult{}, err
	}
	return preflightKey(candidate.Server.APIKeyFile, mutation)
}

func validSnapshotShape(snapshot Snapshot) bool {
	if snapshot.path == "" {
		return false
	}
	key, ok := nativeStorePathKey(snapshot.path)
	if !ok || key == "" || snapshot.parent.path == "" {
		return false
	}
	if snapshot.exists {
		return len(snapshot.bytes) != 0 && snapshot.digest == sha256Bytes(snapshot.bytes) &&
			snapshot.file != (nativeFileMetadata{}) && len(snapshot.missing) == 0
	}
	return snapshot.bytes == nil && snapshot.digest == ([32]byte{}) &&
		snapshot.file == (nativeFileMetadata{}) && len(snapshot.missing) != 0
}

func sameSnapshotRevision(left Snapshot, right Snapshot) bool {
	if left.path != right.path || left.exists != right.exists ||
		left.parent != right.parent {
		return false
	}
	if left.exists {
		return left.file == right.file && left.digest == right.digest &&
			bytes.Equal(left.bytes, right.bytes)
	}
	return equalStrings(left.missing, right.missing)
}

func inspectReservedArtifacts(configPath string) error {
	if err := inspectReservedArtifact(BackupPath(configPath), false); err != nil {
		return err
	}
	return inspectReservedArtifact(LockPath(configPath), true)
}

func inspectReservedArtifact(path string, lock bool) error {
	target, err := openNativeLoadTarget(path)
	if err != nil {
		return err
	}
	if !target.exists {
		if !revalidateNativeLoadTarget(target) {
			return ErrUnsafePath
		}
		return nil
	}
	if lock && !safeNativeLockMetadata(target.metadata) {
		_ = target.file.Close()
		return ErrUnsafePath
	}
	if !revalidateNativeLoadTarget(target) {
		_ = target.file.Close()
		return ErrUnsafePath
	}
	if err := target.file.Close(); err != nil {
		return ErrStore
	}
	return nil
}

func inspectPrivateDirectories(mutation Mutation) error {
	seen := make(map[string]struct{}, len(mutation.PrivateDirs))
	reserved := reservedMutationPathKeys(mutation.Base.path, mutation.Key.Path)
	for _, path := range mutation.PrivateDirs {
		key, ok := nativeStorePathKey(path)
		if !ok {
			return ErrUnsafePath
		}
		if _, duplicate := seen[key]; duplicate {
			return ErrUnsafePath
		}
		seen[key] = struct{}{}
		if _, collision := reserved[key]; collision {
			return ErrUnsafePath
		}
		if _, err := inspectNativePrivateDirectory(path); err != nil {
			return err
		}
	}
	return nil
}

func preflightKey(candidatePath string, mutation Mutation) (PreflightResult, error) {
	_, err := validateKeyPlan(candidatePath, mutation)
	if err != nil {
		return PreflightResult{}, err
	}
	plan := mutation.Key
	if candidatePath == "" {
		return PreflightResult{KeyState: KeyStateNone}, nil
	}

	keyInfo, err := os.Stat(candidatePath)
	if errors.Is(err, os.ErrNotExist) {
		if _, inspectErr := inspectNativePrivateFile(candidatePath); inspectErr != nil {
			return PreflightResult{}, inspectErr
		}
		return PreflightResult{KeyState: KeyStateMissing}, nil
	}
	if err != nil || keyInfo == nil {
		return PreflightResult{}, ErrUnsafePath
	}
	for _, other := range plan.DistinctFrom {
		otherInfo, statErr := os.Stat(other)
		if statErr == nil && otherInfo != nil && os.SameFile(keyInfo, otherInfo) {
			return PreflightResult{}, ErrUnsafePath
		}
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return PreflightResult{}, ErrUnsafePath
		}
	}
	if _, err := gatewaykey.LoadFile(candidatePath, nil); err != nil {
		return PreflightResult{}, ErrUnsafePath
	}
	if plan.Intent == KeyIntentEnsure && !plan.AllowExisting {
		return PreflightResult{KeyState: KeyStateNeedsConfirmation}, nil
	}
	return PreflightResult{KeyState: KeyStateReusable}, nil
}

func validateKeyPlan(candidatePath string, mutation Mutation) (string, error) {
	plan := mutation.Key
	if candidatePath == "" {
		if plan.Intent != KeyIntentNone || plan.Path != "" || plan.AllowExisting ||
			len(plan.DistinctFrom) != 0 {
			return "", ErrStore
		}
		return "", nil
	}
	candidateKey, candidateOK := nativeStorePathKey(candidatePath)
	planKey, planOK := nativeStorePathKey(plan.Path)
	if !candidateOK || !planOK || candidateKey != planKey ||
		(plan.Intent != KeyIntentInspect && plan.Intent != KeyIntentEnsure) ||
		(plan.Intent == KeyIntentInspect && plan.AllowExisting) {
		return "", ErrStore
	}
	reserved := reservedMutationPathKeys(mutation.Base.path, "")
	if _, collision := reserved[candidateKey]; collision {
		return "", ErrUnsafePath
	}
	seen := make(map[string]struct{}, len(plan.DistinctFrom))
	for _, other := range plan.DistinctFrom {
		otherKey, ok := nativeStorePathKey(other)
		if !ok || otherKey == candidateKey {
			return "", ErrUnsafePath
		}
		if _, duplicate := seen[otherKey]; duplicate {
			return "", ErrUnsafePath
		}
		seen[otherKey] = struct{}{}
	}
	return candidateKey, nil
}

func reservedMutationPathKeys(configPath string, keyPath string) map[string]struct{} {
	paths := []string{
		configPath,
		BackupPath(configPath),
		LockPath(configPath),
		configPath + ".tmp",
		BackupPath(configPath) + ".tmp",
		configPath + ".rollback.tmp",
		BackupPath(configPath) + ".restore.tmp",
	}
	if keyPath != "" {
		paths = append(paths, keyPath)
	}
	result := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if key, ok := nativeStorePathKey(path); ok {
			result[key] = struct{}{}
		}
	}
	return result
}

func inspectNativePrivateFile(path string) (bool, error) {
	target, err := openNativeLoadTarget(path)
	if err != nil {
		return false, err
	}
	if target.file != nil {
		if err := target.file.Close(); err != nil {
			return false, ErrStore
		}
		return true, nil
	}
	if !revalidateNativeLoadTarget(target) {
		return false, ErrUnsafePath
	}
	return false, nil
}

func sha256Bytes(value []byte) [sha256.Size]byte {
	return sha256.Sum256(value)
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}
