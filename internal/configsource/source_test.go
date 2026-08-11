package configsource

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
)

func TestLoadBindsDecodedConfigIdentityAndDigestToOneRetainedHandle(t *testing.T) {
	path := writeSourceConfig(t, "SOURCE_KEY")
	pathInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat() error = %v", err)
	}
	if !os.SameFile(pathInfo, pathInfo) {
		t.Fatal("Lstat identity could not be materialized before retained source open")
	}
	opens := 0
	snapshot, err := loadWithOpen(path, func(actual string) (*os.File, error) {
		opens++
		if actual != path {
			t.Fatalf("open path = %q, want %q", actual, path)
		}
		return openSourceFile(actual)
	})
	if err != nil {
		t.Fatalf("loadWithOpen() error = %v", err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	if opens != 1 {
		t.Fatalf("open calls = %d, want 1", opens)
	}

	cfg := snapshot.Config()
	if cfg.Server.APIKeyEnv != "SOURCE_KEY" ||
		cfg.Providers["claude"].Executable == "" ||
		len(cfg.Models) != 1 || cfg.Models[0].ID != "source-model" {
		t.Fatalf("decoded config = %+v", cfg)
	}
	if snapshot.FileInfo() == nil || !os.SameFile(snapshot.FileInfo(), pathInfo) {
		t.Fatalf("FileInfo() = %#v, want retained source identity", snapshot.FileInfo())
	}
	if err := snapshot.Revalidate(); err != nil {
		t.Fatalf("Revalidate() error = %v", err)
	}
	if opens != 1 {
		t.Fatalf("open calls after Revalidate = %d, want 1", opens)
	}
}

func TestLoadPreservesStableCallerSelectedRelativePathSemantics(t *testing.T) {
	absolute := writeSourceConfig(t, "SOURCE_KEY")
	t.Chdir(filepath.Dir(absolute))
	snapshot, err := Load(filepath.Base(absolute))
	if err != nil {
		t.Fatalf("Load(relative) error = %v", err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	if snapshot.Config().Server.APIKeyEnv != "SOURCE_KEY" {
		t.Fatalf("relative Config() = %+v", snapshot.Config())
	}
	if err := snapshot.Revalidate(); err != nil {
		t.Fatalf("relative Revalidate() error = %v", err)
	}
}

func TestLoadRejectsHandlePathIdentityDisagreement(t *testing.T) {
	selected := writeSourceConfig(t, "SELECTED_KEY")
	other := writeSourceConfig(t, "OTHER_KEY")
	opens := 0
	snapshot, err := loadWithOpen(selected, func(string) (*os.File, error) {
		opens++
		return openSourceFile(other)
	})
	if snapshot != nil {
		_ = snapshot.Close()
		t.Fatalf("loadWithOpen() snapshot = %#v, want nil", snapshot)
	}
	assertSourceUnavailable(t, err)
	if opens != 1 {
		t.Fatalf("open calls = %d, want 1", opens)
	}
}

func TestSnapshotRevalidateRejectsDigestMismatchWithoutReplacingConfig(t *testing.T) {
	path := writeSourceConfig(t, "ORIGINAL_KEY")
	reads := 0
	snapshot, err := loadWithOpenAndRead(
		path,
		openSourceFile,
		func(file *os.File) ([]byte, bool) {
			raw, ok := readSourceBytes(file)
			if !ok {
				return nil, false
			}
			reads++
			if reads == 3 {
				raw[len(raw)/2] ^= 1
			}
			return raw, true
		},
	)
	if err != nil {
		t.Fatalf("loadWithOpenAndRead() error = %v", err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })
	if reads != 2 {
		t.Fatalf("reads after load = %d, want 2", reads)
	}
	want := snapshot.Config()

	assertSourceUnavailable(t, snapshot.Revalidate())
	if reads != 3 {
		t.Fatalf("reads after Revalidate = %d, want 3", reads)
	}
	if got := snapshot.Config(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Config() changed after failed revalidation: got %+v, want %+v", got, want)
	}
}

func TestSnapshotRevalidateRejectsPathReplacement(t *testing.T) {
	path := writeSourceConfig(t, "ORIGINAL_KEY")
	payload, err := os.ReadFile(path) // #nosec G304 -- path is created by writeSourceConfig in this test's private TempDir.
	if err != nil {
		t.Fatalf("read original source: %v", err)
	}
	snapshot, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })

	displaced := path + ".displaced"
	if err := os.Rename(path, displaced); err != nil {
		t.Fatalf("rename retained source: %v", err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil { // #nosec G703 -- path is the test-owned TempDir file just renamed above.
		t.Fatalf("write replacement source: %v", err)
	}
	assertSourceUnavailable(t, snapshot.Revalidate())
}

func TestSnapshotConfigReturnsDeepDefensiveClone(t *testing.T) {
	path := writeSourceConfig(t, "SOURCE_KEY")
	snapshot, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	t.Cleanup(func() { _ = snapshot.Close() })

	first := snapshot.Config()
	provider := first.Providers["claude"]
	provider.CredentialEnv[0] = "PLANTED_ENV"
	first.Providers["claude"] = provider
	first.Models[0].ID = "planted-model"
	delete(first.Providers, "claude")

	second := snapshot.Config()
	provider = second.Providers["claude"]
	if provider.CredentialEnv[0] != "ANTHROPIC_API_KEY" ||
		second.Models[0].ID != "source-model" {
		t.Fatalf("defensive Config() = %+v", second)
	}
}

func TestSnapshotNilZeroAndClosedStateFailClosed(t *testing.T) {
	assertClosedSource := func(t *testing.T, snapshot *Snapshot) {
		t.Helper()
		if got := snapshot.Config(); !reflect.DeepEqual(got, config.Config{}) {
			t.Fatalf("Config() = %+v, want zero", got)
		}
		if got := snapshot.FileInfo(); got != nil {
			t.Fatalf("FileInfo() = %#v, want nil", got)
		}
		assertSourceUnavailable(t, snapshot.Revalidate())
		assertSourceUnavailable(t, snapshot.Close())
	}

	t.Run("nil", func(t *testing.T) { assertClosedSource(t, nil) })
	t.Run("zero", func(t *testing.T) { assertClosedSource(t, &Snapshot{}) })
	t.Run("closed", func(t *testing.T) {
		snapshot, err := Load(writeSourceConfig(t, "SOURCE_KEY"))
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if err := snapshot.Close(); err != nil {
			t.Fatalf("Close() error = %v", err)
		}
		assertClosedSource(t, snapshot)
	})
}

func TestLoadFailuresReturnOnlyFixedPathFreeError(t *testing.T) {
	secretPath := filepath.Join(t.TempDir(), "secret-config-name.toml")
	if err := os.WriteFile(secretPath, []byte("not valid = [toml"), 0o600); err != nil {
		t.Fatalf("write malformed source: %v", err)
	}
	oversizedPath := filepath.Join(t.TempDir(), "oversized-secret.toml")
	oversized := sourceConfigDocument(t, "SOURCE_KEY") + strings.Repeat("#", (1<<20)+1)
	if err := os.WriteFile(oversizedPath, []byte(oversized), 0o600); err != nil {
		t.Fatalf("write oversized source: %v", err)
	}

	for _, path := range []string{
		secretPath,
		oversizedPath,
		filepath.Join(t.TempDir(), "missing-secret-config.toml"),
	} {
		t.Run(filepath.Base(path), func(t *testing.T) {
			snapshot, err := Load(path)
			if snapshot != nil {
				_ = snapshot.Close()
				t.Fatalf("Load() snapshot = %#v, want nil", snapshot)
			}
			assertSourceUnavailable(t, err)
			if strings.Contains(err.Error(), filepath.Base(path)) {
				t.Fatalf("error %q exposes source path", err)
			}
		})
	}
}

func writeSourceConfig(t *testing.T, apiKeyEnv string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(sourceConfigDocument(t, apiKeyEnv)), 0o600); err != nil {
		t.Fatalf("write source config: %v", err)
	}
	return path
}

func sourceConfigDocument(t *testing.T, apiKeyEnv string) string {
	t.Helper()
	base := t.TempDir()
	return fmt.Sprintf(`[server]
api_key_env = %s

[runtime]
root = %s

[providers.claude]
executable = %s
config_home = %s
credential_env = ["ANTHROPIC_API_KEY"]

[[models]]
id = "source-model"
provider = "claude"
provider_model = "trusted-model"
`, strconv.Quote(apiKeyEnv), strconv.Quote(filepath.Join(base, "runtime")),
		strconv.Quote(filepath.Join(base, "claude")),
		strconv.Quote(filepath.Join(base, "claude-home")))
}

func assertSourceUnavailable(t *testing.T, err error) {
	t.Helper()
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want exact ErrUnavailable", err)
	}
	if errors.Unwrap(err) != nil {
		t.Fatalf("error %v unexpectedly wraps another error", err)
	}
}
