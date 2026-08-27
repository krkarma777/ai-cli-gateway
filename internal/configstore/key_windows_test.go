//go:build windows

package configstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

func TestPublishGatewayKeyWindowsBuildContract(t *testing.T) {
	t.Parallel()
	var publish func(*os.File, *os.Root, *os.File, string, string) error = nativeRenameNoReplace
	if publish == nil {
		t.Fatal("Windows no-replace publisher is nil")
	}
}

func TestPublishGatewayKeyWindowsCreatesAndNeverReplacesTarget(t *testing.T) {
	t.Run("creates", func(t *testing.T) {
		root := testutil.TrustedTempDir(t)
		configPath := filepath.Join(root, "config.toml")
		keyPath := filepath.Join(root, "gateway.key")
		base, err := NewWriter().Load(context.Background(), configPath)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		payload := windowsGatewayKeyPayload('a')
		result, err := NewWriter().Commit(context.Background(), Mutation{
			Base:      base,
			Candidate: validWindowsStoreConfig(t, root, keyPath),
			Key: KeyPlan{
				Intent: KeyIntentEnsure, Path: keyPath, DistinctFrom: []string{configPath},
			},
		}, payload)
		if err != nil || result.State != CommitCommitted ||
			!result.ConfigChanged || !result.KeyCreated {
			t.Fatalf("Commit() = %#v, %v", result, err)
		}
		assertWindowsStoreBytes(t, keyPath, payload)
	})

	t.Run("target appears", func(t *testing.T) {
		root := testutil.TrustedTempDir(t)
		configPath := filepath.Join(root, "config.toml")
		keyPath := filepath.Join(root, "gateway.key")
		base, err := NewWriter().Load(context.Background(), configPath)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		planted := windowsGatewayKeyPayload('b')
		writer := NewWriter()
		created := false
		writer.ops.operationHook = func(operation operationKind) error {
			if operation == operationCreate && !created {
				created = true
				testutil.WriteTrustedFile(t, keyPath, planted, 0o600)
			}
			return nil
		}
		result, err := writer.Commit(context.Background(), Mutation{
			Base:      base,
			Candidate: validWindowsStoreConfig(t, root, keyPath),
			Key: KeyPlan{
				Intent: KeyIntentEnsure, Path: keyPath, DistinctFrom: []string{configPath},
			},
		}, windowsGatewayKeyPayload('c'))
		if result != (CommitResult{}) || !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Commit() = %#v, %v", result, err)
		}
		got, readErr := os.ReadFile(keyPath) // #nosec G304 -- exact test-owned path.
		if readErr != nil || !bytes.Equal(got, planted) {
			t.Fatalf("appearing target = %q, %v", got, readErr)
		}
		if _, err := os.Lstat(configPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("failed key publication wrote config: %v", err)
		}
	})
}

func windowsGatewayKeyPayload(value byte) []byte {
	payload := bytes.Repeat([]byte{value}, 65)
	payload[64] = '\n'
	return payload
}
