//go:build !windows

package configstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestStageGatewayKeyWritesPrivateSyncedTempInTargetDirectory(t *testing.T) {
	t.Parallel()

	mutation, preflight := missingKeyMutation(t)
	payload := gatewayKeyPayload('b')
	writer := NewWriter()
	realSync := writer.ops.syncFile
	syncCalls := 0
	writer.ops.syncFile = func(file *os.File) error {
		syncCalls++
		return realSync(file)
	}

	staged, err := writer.stageKey(context.Background(), mutation, preflight, payload)
	if err != nil {
		t.Fatalf("stageKey() error = %v", err)
	}
	if staged == nil {
		t.Fatal("stageKey() returned nil staged key")
	}
	if filepath.Dir(staged.tempPath) != filepath.Dir(mutation.Key.Path) {
		t.Fatalf("temp directory = %q, want target directory", filepath.Dir(staged.tempPath))
	}
	if syncCalls != 1 {
		t.Fatalf("file sync calls = %d, want 1", syncCalls)
	}
	info, err := os.Lstat(staged.tempPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || info.Size() != 65 {
		t.Fatalf("staged temp info = %v, %v", info, err)
	}
	got, err := os.ReadFile(staged.tempPath) // #nosec G304 -- exact test-owned path.
	if err != nil || !bytes.Equal(got, payload) {
		t.Fatalf("staged temp payload = %q, %v", got, err)
	}
	if _, err := os.Lstat(mutation.Key.Path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target exists before publication: %v", err)
	}
	if err := staged.rollback(); err != nil {
		t.Fatalf("rollback staged temp: %v", err)
	}
	if _, err := os.Lstat(staged.tempPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("staged temp remains after rollback: %v", err)
	}
}

func TestPublishGatewayKeyNeverReplacesExistingTarget(t *testing.T) {
	t.Parallel()

	t.Run("publishes atomically", func(t *testing.T) {
		t.Parallel()
		mutation, preflight := missingKeyMutation(t)
		payload := gatewayKeyPayload('c')
		staged, err := NewWriter().stageKey(context.Background(), mutation, preflight, payload)
		if err != nil {
			t.Fatalf("stageKey() error = %v", err)
		}
		if err := staged.publish(context.Background()); err != nil {
			t.Fatalf("publish() error = %v", err)
		}
		if _, err := os.Lstat(staged.tempPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("temp remains after publish: %v", err)
		}
		got, err := os.ReadFile(mutation.Key.Path) // #nosec G304 -- exact test-owned path.
		if err != nil || !bytes.Equal(got, payload) {
			t.Fatalf("published payload = %q, %v", got, err)
		}
		if err := staged.finish(); err != nil {
			t.Fatalf("finish() error = %v", err)
		}
	})

	t.Run("target appears", func(t *testing.T) {
		t.Parallel()
		mutation, preflight := missingKeyMutation(t)
		staged, err := NewWriter().stageKey(
			context.Background(), mutation, preflight, gatewayKeyPayload('d'),
		)
		if err != nil {
			t.Fatalf("stageKey() error = %v", err)
		}
		competitor := gatewayKeyPayload('e')
		writePrivateStoreFile(t, mutation.Key.Path, competitor, 0o600)
		if err := staged.publish(context.Background()); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("publish() error = %v, want ErrUnsafePath", err)
		}
		got, err := os.ReadFile(mutation.Key.Path) // #nosec G304 -- exact test-owned path.
		if err != nil || !bytes.Equal(got, competitor) {
			t.Fatalf("competing target changed = %q, %v", got, err)
		}
		if _, err := os.Lstat(staged.tempPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned temp remains after failed publish: %v", err)
		}
	})
}

func TestPublishGatewayKeyCleanupRequiresPublishedIdentity(t *testing.T) {
	t.Parallel()

	t.Run("removes owned publication", func(t *testing.T) {
		t.Parallel()
		mutation, preflight := missingKeyMutation(t)
		staged, err := NewWriter().stageKey(
			context.Background(), mutation, preflight, gatewayKeyPayload('f'),
		)
		if err != nil {
			t.Fatalf("stageKey() error = %v", err)
		}
		if err := staged.publish(context.Background()); err != nil {
			t.Fatalf("publish() error = %v", err)
		}
		if err := staged.rollback(); err != nil {
			t.Fatalf("rollback() error = %v", err)
		}
		if _, err := os.Lstat(mutation.Key.Path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("owned publication remains: %v", err)
		}
	})

	t.Run("preserves replacement", func(t *testing.T) {
		t.Parallel()
		mutation, preflight := missingKeyMutation(t)
		staged, err := NewWriter().stageKey(
			context.Background(), mutation, preflight, gatewayKeyPayload('1'),
		)
		if err != nil {
			t.Fatalf("stageKey() error = %v", err)
		}
		if err := staged.publish(context.Background()); err != nil {
			t.Fatalf("publish() error = %v", err)
		}
		moved := mutation.Key.Path + ".moved"
		if err := os.Rename(mutation.Key.Path, moved); err != nil {
			t.Fatalf("move published key: %v", err)
		}
		replacement := gatewayKeyPayload('2')
		writePrivateStoreFile(t, mutation.Key.Path, replacement, 0o600)
		if err := staged.rollback(); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("rollback() error = %v, want ErrUnsafePath", err)
		}
		got, err := os.ReadFile(mutation.Key.Path) // #nosec G304 -- exact test-owned path.
		if err != nil || !bytes.Equal(got, replacement) {
			t.Fatalf("replacement changed = %q, %v", got, err)
		}
	})
}

func TestStageGatewayKeyRechecksReuseOrphanAndTargetTransitions(t *testing.T) {
	t.Parallel()

	t.Run("authorized reuse does not write", func(t *testing.T) {
		t.Parallel()
		mutation, _ := missingKeyMutation(t)
		mutation.Key.AllowExisting = true
		payload := gatewayKeyPayload('3')
		writePrivateStoreFile(t, mutation.Key.Path, payload, 0o600)
		preflight, err := NewWriter().Preflight(context.Background(), mutation)
		if err != nil || preflight.KeyState != KeyStateReusable {
			t.Fatalf("Preflight() = %#v, %v", preflight, err)
		}
		before := directoryNames(t, filepath.Dir(mutation.Key.Path))
		staged, err := NewWriter().stageKey(context.Background(), mutation, preflight, nil)
		if err != nil || staged != nil {
			t.Fatalf("stageKey(reuse) = %#v, %v", staged, err)
		}
		after := directoryNames(t, filepath.Dir(mutation.Key.Path))
		if !equalStrings(before, after) {
			t.Fatalf("reuse mutated directory: before %v after %v", before, after)
		}
	})

	t.Run("reusable target disappears", func(t *testing.T) {
		t.Parallel()
		mutation, _ := missingKeyMutation(t)
		mutation.Key.AllowExisting = true
		writePrivateStoreFile(t, mutation.Key.Path, gatewayKeyPayload('4'), 0o600)
		preflight, err := NewWriter().Preflight(context.Background(), mutation)
		if err != nil {
			t.Fatalf("Preflight() error = %v", err)
		}
		if err := os.Remove(mutation.Key.Path); err != nil {
			t.Fatalf("Remove key: %v", err)
		}
		if _, err := NewWriter().stageKey(context.Background(), mutation, preflight, nil); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("stageKey() error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("unconfirmed orphan", func(t *testing.T) {
		t.Parallel()
		mutation, _ := missingKeyMutation(t)
		writePrivateStoreFile(t, mutation.Key.Path, gatewayKeyPayload('5'), 0o600)
		preflight, err := NewWriter().Preflight(context.Background(), mutation)
		if err != nil || preflight.KeyState != KeyStateNeedsConfirmation {
			t.Fatalf("Preflight() = %#v, %v", preflight, err)
		}
		if _, err := NewWriter().stageKey(context.Background(), mutation, preflight, nil); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("stageKey() error = %v, want ErrUnsafePath", err)
		}
	})

	t.Run("missing target appears before staging", func(t *testing.T) {
		t.Parallel()
		mutation, preflight := missingKeyMutation(t)
		writePrivateStoreFile(t, mutation.Key.Path, gatewayKeyPayload('6'), 0o600)
		if _, err := NewWriter().stageKey(
			context.Background(), mutation, preflight, gatewayKeyPayload('7'),
		); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("stageKey() error = %v, want ErrUnsafePath", err)
		}
	})
}

func TestStageGatewayKeyRejectsReservedAndProviderCollisions(t *testing.T) {
	t.Parallel()

	for name, selectPath := range map[string]func(string) string{
		"config": func(path string) string { return path },
		"backup": BackupPath,
		"lock":   LockPath,
	} {
		name, selectPath := name, selectPath
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := privateStoreDir(t)
			configPath := filepath.Join(root, "config.toml")
			base, err := NewWriter().Load(context.Background(), configPath)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			keyPath := selectPath(configPath)
			mutation := Mutation{
				Base:      base,
				Candidate: storeCandidate(t, keyPath, filepath.Join(root, "home")),
				Key:       KeyPlan{Intent: KeyIntentEnsure, Path: keyPath},
			}
			if _, err := NewWriter().stageKey(
				context.Background(), mutation,
				PreflightResult{KeyState: KeyStateMissing}, gatewayKeyPayload('8'),
			); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("stageKey() error = %v, want ErrUnsafePath", err)
			}
		})
	}

	t.Run("provider identity", func(t *testing.T) {
		t.Parallel()
		mutation, _ := missingKeyMutation(t)
		writePrivateStoreFile(t, mutation.Key.Path, gatewayKeyPayload('9'), 0o600)
		provider := filepath.Join(filepath.Dir(mutation.Key.Path), "provider")
		if err := os.Link(mutation.Key.Path, provider); err != nil {
			t.Fatalf("Link: %v", err)
		}
		mutation.Key.AllowExisting = true
		mutation.Key.DistinctFrom = append(mutation.Key.DistinctFrom, provider)
		if _, err := NewWriter().stageKey(
			context.Background(), mutation,
			PreflightResult{KeyState: KeyStateReusable}, nil,
		); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("stageKey() error = %v, want ErrUnsafePath", err)
		}
	})
}

func missingKeyMutation(t *testing.T) (Mutation, PreflightResult) {
	t.Helper()
	root := privateStoreDir(t)
	keyDirectory := filepath.Join(root, "keys")
	if err := os.Mkdir(keyDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir key directory: %v", err)
	}
	configPath := filepath.Join(root, "config.toml")
	keyPath := filepath.Join(keyDirectory, "gateway.key")
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	mutation := Mutation{
		Base:      base,
		Candidate: storeCandidate(t, keyPath, filepath.Join(root, "provider-home")),
		Key: KeyPlan{
			Intent:       KeyIntentEnsure,
			Path:         keyPath,
			DistinctFrom: []string{configPath},
		},
	}
	preflight, err := NewWriter().Preflight(context.Background(), mutation)
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if preflight.KeyState != KeyStateMissing {
		t.Fatalf("Preflight KeyState = %d, want missing", preflight.KeyState)
	}
	return mutation, preflight
}

func gatewayKeyPayload(value byte) []byte {
	return append(bytes.Repeat([]byte{value}, 64), '\n')
}
