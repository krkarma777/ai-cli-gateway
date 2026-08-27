package configstore

import (
	"context"
	"errors"
	"testing"
)

func TestLockRejectsNilContextWriterAndSnapshot(t *testing.T) {
	t.Parallel()

	writer := NewWriter()
	if _, err := writer.acquireLock(nil, Snapshot{}); !errors.Is(err, ErrStore) { //nolint:staticcheck // Nil-context rejection is part of the package contract.
		t.Fatalf("acquireLock(nil) error = %v, want ErrStore", err)
	}
	var nilWriter *Writer
	if _, err := nilWriter.acquireLock(context.Background(), Snapshot{}); !errors.Is(err, ErrStore) {
		t.Fatalf("nil Writer acquireLock() error = %v, want ErrStore", err)
	}
}
