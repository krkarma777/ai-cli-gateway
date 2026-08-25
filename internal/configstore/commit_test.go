package configstore

import (
	"context"
	"errors"
	"testing"
)

func TestCommitStateZeroValueMeansNotCommitted(t *testing.T) {
	t.Parallel()

	var result CommitResult
	if result.State != CommitNotCommitted || result.ConfigChanged ||
		result.KeyCreated || result.BackupPath != "" {
		t.Fatalf("zero CommitResult = %#v", result)
	}
}

func TestCommitRejectsInvalidWriterContextAndMutationWithoutSideEffects(t *testing.T) {
	t.Parallel()

	writer := NewWriter()
	for name, call := range map[string]func() (CommitResult, error){
		"nil writer": func() (CommitResult, error) {
			var nilWriter *Writer
			return nilWriter.Commit(context.Background(), Mutation{}, nil)
		},
		"nil context": func() (CommitResult, error) {
			return writer.Commit(nil, Mutation{}, nil) //nolint:staticcheck // Nil-context rejection is contractual.
		},
		"zero mutation": func() (CommitResult, error) {
			return writer.Commit(context.Background(), Mutation{}, nil)
		},
	} {
		call := call
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			result, err := call()
			if result != (CommitResult{}) || !errors.Is(err, ErrStore) {
				t.Fatalf("Commit() = %#v, %v; want zero, ErrStore", result, err)
			}
		})
	}
}
