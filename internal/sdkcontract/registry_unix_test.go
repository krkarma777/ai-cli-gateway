//go:build !windows

package sdkcontract

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestFixtureRegistryUntouchedZeroRecordIsClean(t *testing.T) {
	root := trustedSiblingFixture(t)
	path := filepath.Join(root, "fixture.registry")
	registry, err := startPlatformRegistry(path, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("startPlatformRegistry() error = %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat registry: %v", err)
	}
	if info.Mode().Perm() != 0o600 || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("registry mode = %v", info.Mode())
	}
	result := registry.StopAndVerify(30 * time.Millisecond)
	if result.Err != nil || !result.SafeToRemove {
		t.Fatalf("StopAndVerify = %#v", result)
	}
}

func TestFixtureRegistryCreationDefeatsRestrictiveUmask(t *testing.T) {
	root := trustedSiblingFixture(t)
	path := filepath.Join(root, "fixture.registry")
	old := setProcessUmask(0o777)
	registry, err := startPlatformRegistry(path, 20*time.Millisecond)
	setProcessUmask(old)
	if err != nil {
		t.Fatalf("startPlatformRegistry() error = %v", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat registry: %v", err)
	}
	if info.Mode().Perm() != 0o600 || info.Mode()&os.ModeNamedPipe == 0 {
		t.Fatalf("registry mode = %v", info.Mode())
	}
	result := registry.StopAndVerify(30 * time.Millisecond)
	if result.Err != nil || !result.SafeToRemove {
		t.Fatalf("StopAndVerify = %#v", result)
	}
}

func TestRegistryConstructorRollbackCertifiesOnlySuccessfulRemoval(t *testing.T) {
	for _, test := range []struct {
		name     string
		remove   func(string) error
		wantSafe bool
	}{
		{name: "removed", remove: func(string) error { return nil }, wantSafe: true},
		{name: "removal failed", remove: func(string) error { return os.ErrPermission }},
	} {
		t.Run(test.name, func(t *testing.T) {
			registry, err := rollbackRegistryConstructor("/owned/fixture.registry", nil, test.remove)
			if !isCleanupSafety(err) || cleanupErrorSafe(err) != test.wantSafe || (registry != nil) == test.wantSafe {
				t.Fatalf("rollback registry=%#v error=%v safe=%t", registry, err, cleanupErrorSafe(err))
			}
			if registry != nil {
				result := registry.StopAndVerify(time.Millisecond)
				if result.SafeToRemove || result.Err == nil {
					t.Fatalf("recovery result = %#v", result)
				}
			}
		})
	}
}

func TestFixtureRegistryReadyArmsAndRequiresCompleteProtocol(t *testing.T) {
	root := trustedSiblingFixture(t)
	path := filepath.Join(root, "fixture.registry")
	registry, err := startPlatformRegistry(path, 15*time.Millisecond)
	if registry != nil {
		t.Cleanup(func() { _ = registry.StopAndVerify(30 * time.Millisecond) })
	}
	if err != nil {
		t.Fatalf("start registry: %v", err)
	}
	ready := registry.Ready()
	select {
	case <-ready:
		t.Fatal("Ready closed without protocol")
	case <-time.After(25 * time.Millisecond):
	}
	result := registry.StopAndVerify(30 * time.Millisecond)
	if result.Err == nil || result.SafeToRemove {
		t.Fatalf("timeout result = %#v", result)
	}
}

func TestFixtureRegistryRequirementDoesNotStartProtocolBeforeFirstByte(t *testing.T) {
	root := trustedSiblingFixture(t)
	path := filepath.Join(root, "fixture.registry")
	registry, err := startPlatformRegistry(path, 15*time.Millisecond)
	if registry != nil {
		t.Cleanup(func() { _ = registry.StopAndVerify(30 * time.Millisecond) })
	}
	if err != nil {
		t.Fatalf("start registry: %v", err)
	}
	concrete, ok := registry.(*unixFixtureRegistry)
	if !ok {
		t.Fatalf("registry type = %T", registry)
	}
	concrete.requireCompletion()

	concrete.mu.Lock()
	required := concrete.required
	protocolRequested := concrete.protocolRequested
	protocolStarted := concrete.protocolStarted
	failed := concrete.failed
	concrete.mu.Unlock()
	if !required || protocolRequested || protocolStarted || failed {
		t.Fatalf("required=%t protocol_requested=%t protocol_started=%t failed=%t", required, protocolRequested, protocolStarted, failed)
	}
	select {
	case <-concrete.arm:
		t.Fatal("cleanup requirement queued the partial-record protocol timer")
	default:
	}

	result := registry.StopAndVerify(30 * time.Millisecond)
	if result.Err == nil || result.SafeToRemove {
		t.Fatalf("required zero-record result = %#v", result)
	}
}

func TestFixtureRegistryStopDrainsPendingMalformedBytes(t *testing.T) {
	root := trustedSiblingFixture(t)
	path := filepath.Join(root, "fixture.registry")
	registry, err := startPlatformRegistry(path, 32*time.Second)
	if err != nil {
		t.Fatalf("start registry: %v", err)
	}
	// #nosec G304 -- path names the FIFO created by this test.
	writer, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open writer: %v", err)
	}
	if _, err := writer.Write([]byte("malformed\n")); err != nil {
		t.Fatalf("write malformed record: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	result := registry.StopAndVerify(30 * time.Millisecond)
	if result.Err == nil || result.SafeToRemove {
		t.Fatalf("pending malformed result = %#v", result)
	}
}

func TestFixtureRegistryFirstByteArmsAndValidRecordsCloseReady(t *testing.T) {
	root := trustedSiblingFixture(t)
	path := filepath.Join(root, "fixture.registry")
	registry, err := startPlatformRegistry(path, 100*time.Millisecond)
	if err != nil {
		t.Fatalf("start registry: %v", err)
	}
	ready := registry.Ready()
	// #nosec G304 -- path names the FIFO created by this test.
	writer, err := os.OpenFile(path, os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open FIFO writer: %v", err)
	}
	records := fmt.Sprintf("provider %d %d\ndescendant %d %d\n", 2_147_483_000, 2_147_483_000, 2_147_483_001, 2_147_483_000)
	if _, err := writer.Write([]byte(records)); err != nil {
		t.Fatalf("write records: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("close writer: %v", err)
	}
	select {
	case <-ready:
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Ready did not close for complete records")
	}
	result := registry.StopAndVerify(40 * time.Millisecond)
	if result.Err != nil || !result.SafeToRemove {
		t.Fatalf("valid result = %#v", result)
	}
}

func TestFixtureRegistryMalformedDuplicateAndOversizedFailClosed(t *testing.T) {
	for _, test := range []struct {
		payload  string
		wantSafe bool
	}{
		{payload: "malformed\n"},
		{payload: "provider 2147483000 2147483000\nprovider 2147483000 2147483000\n", wantSafe: true},
		{payload: "provider +2147483000 +2147483000\ndescendant +2147483001 +2147483000\n"},
		{payload: "provider 02147483000 02147483000\ndescendant 02147483001 02147483000\n"},
		{payload: strings.Repeat("x", 513)},
		{payload: "provider 2147483000 2147483000\n", wantSafe: true},
	} {
		t.Run(fmt.Sprintf("bytes-%d", len(test.payload)), func(t *testing.T) {
			root := trustedSiblingFixture(t)
			path := filepath.Join(root, "fixture.registry")
			registry, err := startPlatformRegistry(path, 20*time.Millisecond)
			if err != nil {
				t.Fatalf("start registry: %v", err)
			}
			// #nosec G304 -- path names the FIFO created by this test.
			writer, err := os.OpenFile(path, os.O_WRONLY, 0)
			if err != nil {
				t.Fatalf("open writer: %v", err)
			}
			_, _ = writer.Write([]byte(test.payload))
			_ = writer.Close()
			time.Sleep(30 * time.Millisecond)
			result := registry.StopAndVerify(30 * time.Millisecond)
			if result.Err == nil || result.SafeToRemove != test.wantSafe {
				t.Fatalf("invalid result = %#v", result)
			}
		})
	}
}
