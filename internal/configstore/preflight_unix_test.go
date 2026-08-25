//go:build !windows

package configstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestPreflightIsReadOnlyAndReturnsMissingKey(t *testing.T) {
	t.Parallel()

	directory := privateStoreDir(t)
	configPath := filepath.Join(directory, "config.toml")
	keyPath := filepath.Join(directory, "gateway.key")
	providerHome := filepath.Join(directory, "codex-home")
	writer := NewWriter()
	base, err := writer.Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	before := directoryNames(t, directory)
	result, err := writer.Preflight(context.Background(), Mutation{
		Base:      base,
		Candidate: storeCandidate(t, keyPath, providerHome),
		Key: KeyPlan{
			Intent: KeyIntentEnsure,
			Path:   keyPath,
			DistinctFrom: []string{
				configPath,
				filepath.Join(directory, "codex"),
			},
		},
		PrivateDirs: []string{providerHome},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if result.KeyState != KeyStateMissing {
		t.Fatalf("KeyState = %d, want KeyStateMissing", result.KeyState)
	}
	if after := directoryNames(t, directory); !reflect.DeepEqual(after, before) {
		t.Fatalf("Preflight mutated directory: before %v after %v", before, after)
	}
	for _, path := range []string{
		configPath,
		BackupPath(configPath),
		LockPath(configPath),
		keyPath,
		providerHome,
	} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("Preflight created %q: %v", filepath.Base(path), err)
		}
	}
}

func TestPreflightKeyStateMatrix(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		intent        KeyIntent
		allowExisting bool
		createKey     bool
		want          KeyState
	}{
		{"ensure missing", KeyIntentEnsure, false, false, KeyStateMissing},
		{"inspect missing", KeyIntentInspect, false, false, KeyStateMissing},
		{"ensure orphan", KeyIntentEnsure, false, true, KeyStateNeedsConfirmation},
		{"ensure explicit reuse", KeyIntentEnsure, true, true, KeyStateReusable},
		{"inspect configured", KeyIntentInspect, false, true, KeyStateReusable},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := privateStoreDir(t)
			configPath := filepath.Join(directory, "config.toml")
			keyPath := filepath.Join(directory, "gateway.key")
			if test.createKey {
				writePrivateStoreFile(t, keyPath, append(bytes.Repeat([]byte{'a'}, 64), '\n'), 0o600)
			}
			base, err := NewWriter().Load(context.Background(), configPath)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			result, err := NewWriter().Preflight(context.Background(), Mutation{
				Base:      base,
				Candidate: storeCandidate(t, keyPath, filepath.Join(directory, "home")),
				Key: KeyPlan{
					Intent:        test.intent,
					Path:          keyPath,
					AllowExisting: test.allowExisting,
					DistinctFrom:  []string{configPath},
				},
			})
			if err != nil {
				t.Fatalf("Preflight() error = %v", err)
			}
			if result.KeyState != test.want {
				t.Fatalf("KeyState = %d, want %d", result.KeyState, test.want)
			}
		})
	}
}

func TestPreflightCandidateAndKeyPlanMustAgree(t *testing.T) {
	t.Parallel()

	directory := privateStoreDir(t)
	configPath := filepath.Join(directory, "config.toml")
	keyPath := filepath.Join(directory, "gateway.key")
	base, err := NewWriter().Load(context.Background(), configPath)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	fileCandidate := storeCandidate(t, keyPath, filepath.Join(directory, "home"))
	noneCandidate := bytes.Replace(fileCandidate,
		[]byte("api_key_file = "+quotedStoreValue(keyPath)+"\n"), nil, 1)
	for name, mutation := range map[string]Mutation{
		"file with no plan": {
			Base: base, Candidate: fileCandidate,
		},
		"file with wrong path": {
			Base: base, Candidate: fileCandidate,
			Key: KeyPlan{Intent: KeyIntentEnsure, Path: filepath.Join(directory, "other.key")},
		},
		"file with unknown intent": {
			Base: base, Candidate: fileCandidate,
			Key: KeyPlan{Intent: KeyIntent(99), Path: keyPath},
		},
		"none with file plan": {
			Base: base, Candidate: noneCandidate,
			Key: KeyPlan{Intent: KeyIntentEnsure, Path: keyPath},
		},
	} {
		mutation := mutation
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewWriter().Preflight(context.Background(), mutation); !errors.Is(err, ErrStore) {
				t.Fatalf("Preflight() error = %v, want ErrStore", err)
			}
		})
	}

	result, err := NewWriter().Preflight(context.Background(), Mutation{
		Base: base, Candidate: noneCandidate,
	})
	if err != nil || result.KeyState != KeyStateNone {
		t.Fatalf("Preflight(none) = %#v, %v", result, err)
	}
}

func TestPreflightRejectsChangedBaseUnsafeArtifactsAndCollisions(t *testing.T) {
	t.Parallel()

	t.Run("changed base", func(t *testing.T) {
		t.Parallel()
		directory := privateStoreDir(t)
		configPath := filepath.Join(directory, "config.toml")
		writePrivateStoreFile(t, configPath, validStoreConfig(t), 0o600)
		base, err := NewWriter().Load(context.Background(), configPath)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		changed := bytes.Replace(validStoreConfig(t), []byte("gpt-test"), []byte("gpt-changed"), 1)
		writePrivateStoreFile(t, configPath, changed, 0o600)
		if _, err := NewWriter().Preflight(context.Background(), Mutation{
			Base: base, Candidate: changed,
		}); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Preflight() error = %v, want ErrUnsafePath", err)
		}
	})

	for _, reserved := range []string{"config", "backup", "lock", "distinct"} {
		reserved := reserved
		t.Run("key collision "+reserved, func(t *testing.T) {
			t.Parallel()
			directory := privateStoreDir(t)
			configPath := filepath.Join(directory, "config.toml")
			keyPath := filepath.Join(directory, "gateway.key")
			collision := map[string]string{
				"config":   configPath,
				"backup":   BackupPath(configPath),
				"lock":     LockPath(configPath),
				"distinct": keyPath,
			}[reserved]
			base, err := NewWriter().Load(context.Background(), configPath)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			plan := KeyPlan{Intent: KeyIntentEnsure, Path: collision}
			if reserved == "distinct" {
				plan.Path = keyPath
				plan.DistinctFrom = []string{keyPath}
			}
			if _, err := NewWriter().Preflight(context.Background(), Mutation{
				Base:      base,
				Candidate: storeCandidate(t, plan.Path, filepath.Join(directory, "home")),
				Key:       plan,
			}); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Preflight() error = %v, want ErrUnsafePath", err)
			}
		})
	}

	t.Run("unsafe provider home", func(t *testing.T) {
		t.Parallel()
		directory := privateStoreDir(t)
		configPath := filepath.Join(directory, "config.toml")
		keyPath := filepath.Join(directory, "gateway.key")
		home := filepath.Join(directory, "home")
		if err := os.Mkdir(home, 0o755); err != nil { // #nosec G301 -- intentionally unsafe fixture.
			t.Fatalf("Mkdir: %v", err)
		}
		base, err := NewWriter().Load(context.Background(), configPath)
		if err != nil {
			t.Fatalf("Load() error = %v", err)
		}
		if _, err := NewWriter().Preflight(context.Background(), Mutation{
			Base: base, Candidate: storeCandidate(t, keyPath, home),
			Key:         KeyPlan{Intent: KeyIntentEnsure, Path: keyPath},
			PrivateDirs: []string{home},
		}); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("Preflight() error = %v, want ErrUnsafePath", err)
		}
	})
}

func TestPreflightRejectsLockSentinelOutsideExactPolicy(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		payload []byte
		mode    os.FileMode
	}{
		{name: "read only", mode: 0o400},
		{name: "nonempty", payload: []byte("occupied\n"), mode: 0o600},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			directory := privateStoreDir(t)
			configPath := filepath.Join(directory, "config.toml")
			writePrivateStoreFile(t, LockPath(configPath), test.payload, test.mode)
			base, err := NewWriter().Load(context.Background(), configPath)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			if _, err := NewWriter().Preflight(context.Background(), Mutation{
				Base: base, Candidate: validStoreConfig(t),
			}); !errors.Is(err, ErrUnsafePath) {
				t.Fatalf("Preflight() error = %v, want ErrUnsafePath", err)
			}
		})
	}
}

func storeCandidate(t *testing.T, keyPath string, providerHome string) []byte {
	t.Helper()
	runtimeRoot := filepath.Join(privateStoreDir(t), "runtime")
	return []byte("[server]\napi_key_file = " + quotedStoreValue(keyPath) + "\n\n" +
		"[runtime]\nroot = " + quotedStoreValue(runtimeRoot) + "\n\n" +
		"[providers.codex]\nexecutable = '/bin/echo'\nconfig_home = " + quotedStoreValue(providerHome) + "\n\n" +
		"[[models]]\nid = 'codex-test'\nprovider = 'codex'\nprovider_model = 'gpt-test'\n")
}

func directoryNames(t *testing.T, path string) []string {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	names := make([]string, len(entries))
	for index, entry := range entries {
		names[index] = entry.Name()
	}
	return names
}
