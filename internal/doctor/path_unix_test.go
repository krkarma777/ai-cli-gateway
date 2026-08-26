//go:build !windows

package doctor

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/gatewaykey"
	"github.com/krkarma777/ai-cli-gateway/internal/provider"
	"github.com/krkarma777/ai-cli-gateway/internal/testutil"
)

func TestValidateUnixLeafPolicies(t *testing.T) {
	euid := uint32(os.Geteuid()) //nolint:gosec // Kernel UIDs use uint32.
	other := euid + 1
	if other == 0 {
		other++
	}

	tests := []struct {
		name string
		kind pathKind
		mode fs.FileMode
		uid  uint32
		ok   bool
	}{
		{"executable user 0700", pathKindExecutable, 0o700, euid, true},
		{"executable root 0555", pathKindExecutable, 0o555, 0, true},
		{"executable no execute", pathKindExecutable, 0o600, euid, false},
		{"executable group write", pathKindExecutable, 0o720, euid, false},
		{"executable other write", pathKindExecutable, 0o702, euid, false},
		{"executable setuid", pathKindExecutable, 0o700 | fs.ModeSetuid, euid, false},
		{"executable setgid", pathKindExecutable, 0o700 | fs.ModeSetgid, euid, false},
		{"executable sticky", pathKindExecutable, 0o700 | fs.ModeSticky, euid, false},
		{"executable directory", pathKindExecutable, fs.ModeDir | 0o700, euid, false},
		{"executable untrusted owner", pathKindExecutable, 0o700, other, false},
		{"entrypoint user 0500", pathKindEntrypoint, 0o500, euid, true},
		{"config exact", pathKindConfigHome, fs.ModeDir | 0o700, euid, true},
		{"config root owner", pathKindConfigHome, fs.ModeDir | 0o700, 0, euid == 0},
		{"config permissive", pathKindConfigHome, fs.ModeDir | 0o750, euid, false},
		{"config not directory", pathKindConfigHome, 0o700, euid, false},
		{"credential read only", pathKindCredential, 0o400, euid, true},
		{"credential read write", pathKindCredential, 0o600, euid, true},
		{"credential group readable", pathKindCredential, 0o640, euid, false},
		{"credential executable", pathKindCredential, 0o700, euid, false},
		{"credential write only", pathKindCredential, 0o200, euid, false},
		{"credential special", pathKindCredential, 0o600 | fs.ModeSetgid, euid, false},
		{"credential directory", pathKindCredential, fs.ModeDir | 0o600, euid, false},
		{"PATH directory", pathKindSafeDirectory, fs.ModeDir | 0o755, 0, true},
		{"PATH writable", pathKindSafeDirectory, fs.ModeDir | 0o775, euid, false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			info := unixTestFileInfo{mode: test.mode, uid: test.uid}
			err := validateUnixLeaf(info, test.kind, euid)
			if (err == nil) != test.ok {
				t.Fatalf("validateUnixLeaf() error=%v, want ok=%t", err, test.ok)
			}
		})
	}
}

func TestValidateUnixAuthorityRejectsWritableOrUntrustedAncestors(t *testing.T) {
	euid := uint32(os.Geteuid()) //nolint:gosec // Kernel UIDs use uint32.
	other := euid + 1
	if other == 0 {
		other++
	}
	for _, test := range []struct {
		name string
		info unixTestFileInfo
		ok   bool
	}{
		{"user directory", unixTestFileInfo{mode: fs.ModeDir | 0o755, uid: euid}, true},
		{"root directory", unixTestFileInfo{mode: fs.ModeDir | 0o755, uid: 0}, true},
		{"sticky writable", unixTestFileInfo{mode: fs.ModeDir | fs.ModeSticky | 0o777, uid: 0}, false},
		{"group writable", unixTestFileInfo{mode: fs.ModeDir | 0o770, uid: euid}, false},
		{"other writable", unixTestFileInfo{mode: fs.ModeDir | 0o707, uid: euid}, false},
		{"untrusted owner", unixTestFileInfo{mode: fs.ModeDir | 0o755, uid: other}, false},
		{"not directory", unixTestFileInfo{mode: 0o755, uid: euid}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := validateUnixAuthority(test.info, euid)
			if (err == nil) != test.ok {
				t.Fatalf("validateUnixAuthority() error=%v, want ok=%t", err, test.ok)
			}
		})
	}
}

func TestValidateUnixPathsResolveOnlySafeCleanObjects(t *testing.T) {
	root := newSecureUnixTestTree(t)
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(bin, "provider")
	writeUnixTestFile(t, executable, 0o700)

	alias := filepath.Join(root, "provider-link")
	if err := os.Symlink(executable, alias); err != nil {
		t.Fatal(err)
	}
	validated, disposition := validateExecutablePath(alias)
	if disposition != pathSafe || validated.Clean != alias ||
		validated.Resolved != executable {
		t.Fatalf("validated alias=%+v disposition=%v", validated, disposition)
	}

	nonclean := filepath.Join(root, "never-walked", "..", "bin", "provider")
	validated, disposition = validateExecutablePath(nonclean)
	if disposition != pathSafe || validated.Clean != executable ||
		validated.Resolved != executable {
		t.Fatalf("validated nonclean=%+v disposition=%v", validated, disposition)
	}
	if _, err := os.Lstat(filepath.Join(root, "never-walked")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("discarded original component was touched: %v", err)
	}

	missing := filepath.Join(bin, "missing")
	if _, disposition := validateExecutablePath(missing); disposition != pathMissing {
		t.Fatalf("missing disposition=%v", disposition)
	}

	broken := filepath.Join(root, "broken")
	if err := os.Symlink(missing, broken); err != nil {
		t.Fatal(err)
	}
	if _, disposition := validateExecutablePath(broken); disposition != pathUnsafe {
		t.Fatalf("broken symlink disposition=%v", disposition)
	}

	loopA := filepath.Join(root, "loop-a")
	loopB := filepath.Join(root, "loop-b")
	if err := os.Symlink(loopB, loopA); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(loopA, loopB); err != nil {
		t.Fatal(err)
	}
	if _, disposition := validateExecutablePath(loopA); disposition != pathUnsafe {
		t.Fatalf("loop disposition=%v", disposition)
	}

	if _, disposition := validateExecutablePath("relative/provider"); disposition != pathUnsafe {
		t.Fatalf("relative disposition=%v", disposition)
	}
	if _, disposition := validateExecutablePath(executable + "\x00tail"); disposition != pathUnsafe {
		t.Fatalf("NUL disposition=%v", disposition)
	}
}

func TestValidateUnixPrivateLeavesRejectSymlinksAndUnsafeAncestors(t *testing.T) {
	root := newSecureUnixTestTree(t)
	configHome := filepath.Join(root, "config")
	if err := os.Mkdir(configHome, 0o700); err != nil {
		t.Fatal(err)
	}
	credential := filepath.Join(root, "credential.json")
	writeUnixTestFile(t, credential, 0o600)

	if got, disposition := validateConfigHomePath(configHome); disposition != pathSafe || got.Resolved != configHome {
		t.Fatalf("config=%+v disposition=%v", got, disposition)
	}
	if got, disposition := validateCredentialPath(credential); disposition != pathSafe || got.Resolved != credential {
		t.Fatalf("credential=%+v disposition=%v", got, disposition)
	}

	configAlias := filepath.Join(root, "config-alias")
	credentialAlias := filepath.Join(root, "credential-alias")
	if err := os.Symlink(configHome, configAlias); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(credential, credentialAlias); err != nil {
		t.Fatal(err)
	}
	if _, disposition := validateConfigHomePath(configAlias); disposition != pathUnsafe {
		t.Fatalf("config symlink disposition=%v", disposition)
	}
	if _, disposition := validateCredentialPath(credentialAlias); disposition != pathUnsafe {
		t.Fatalf("credential symlink disposition=%v", disposition)
	}

	unsafeParent := filepath.Join(root, "unsafe-parent")
	if err := os.Mkdir(unsafeParent, 0o700); err != nil {
		t.Fatal(err)
	}
	unsafeExecutable := filepath.Join(unsafeParent, "provider")
	writeUnixTestFile(t, unsafeExecutable, 0o700)
	//nolint:gosec // The fixture intentionally creates an unsafe directory.
	if err := os.Chmod(unsafeParent, 0o770); err != nil {
		t.Fatal(err)
	}
	if _, disposition := validateExecutablePath(unsafeExecutable); disposition != pathUnsafe {
		t.Fatalf("unsafe ancestor disposition=%v", disposition)
	}
}

func TestDoctorTrustedCommandParityUnix(t *testing.T) {
	root := newSecureUnixTestTree(t)
	safe := filepath.Join(root, "provider")
	writeUnixTestFile(t, safe, 0o700)
	alias := filepath.Join(root, "provider-link")
	if err := os.Symlink(safe, alias); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(root, "missing")
	broken := filepath.Join(root, "broken")
	if err := os.Symlink(missing, broken); err != nil {
		t.Fatal(err)
	}
	noExecute := filepath.Join(root, "no-execute")
	writeUnixTestFile(t, noExecute, 0o600)
	writable := filepath.Join(root, "writable")
	writeUnixTestFile(t, writable, 0o720)
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		name string
		path string
		want pathDisposition
	}{
		{name: "safe native", path: safe, want: pathSafe},
		{name: "safe symlink", path: alias, want: pathSafe},
		{name: "missing", path: missing, want: pathMissing},
		{name: "broken symlink", path: broken, want: pathUnsafe},
		{name: "relative", path: "relative", want: pathUnsafe},
		{name: "NUL", path: safe + "\x00tail", want: pathUnsafe},
		{name: "no execute", path: noExecute, want: pathUnsafe},
		{name: "writable leaf", path: writable, want: pathUnsafe},
		{name: "directory", path: directory, want: pathUnsafe},
	} {
		t.Run(test.name, func(t *testing.T) {
			validated, got := validateExecutablePath(test.path)
			if got != test.want {
				t.Fatalf("validateExecutablePath() disposition = %v, want %v", got, test.want)
			}
			if got == pathSafe && (validated.Info == nil || validated.Resolved == "") {
				t.Fatalf("safe validated path = %+v", validated)
			}
		})
	}
}

func TestDoctorTrustedCommandParityDarwinRootOwnerOnlyExecutable(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin retained-handle parity fixture")
	}
	if os.Geteuid() == 0 {
		t.Skip("fixture must be inaccessible to the current effective user")
	}

	const path = "/usr/libexec/cups/backend/lpd"
	info, err := os.Lstat(path)
	if err != nil {
		t.Skipf("trusted system fixture unavailable: %v", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Uid != 0 ||
		info.Mode().Perm() != 0o700 {
		t.Skipf("fixture owner/mode no longer models root-owned 0700: mode=%v stat=%T", info.Mode(), info.Sys())
	}
	resolved, _, oldErr := resolveUnixPath(path, pathKindExecutable)
	if oldErr != nil || resolved != path {
		t.Skipf("fixture ancestors no longer satisfy the pre-delegation policy: resolved=%q err=%v", resolved, oldErr)
	}

	if _, disposition := validateExecutablePath(path); disposition != pathSafe {
		t.Fatalf("delegated disposition = %v, want pre-Task2 disposition %v", disposition, pathSafe)
	}
}

func TestBuildSafePathValidatesEveryCandidateBeforeIdentityDedup(t *testing.T) {
	root := newSecureUnixTestTree(t)
	bin := filepath.Join(root, "bin")
	aliasBin := filepath.Join(root, "alias-bin")
	tail := filepath.Join(root, "tail")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(aliasBin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tail, 0o700); err != nil {
		t.Fatal(err)
	}
	executablePath := filepath.Join(bin, "provider")
	writeUnixTestFile(t, executablePath, 0o700)
	executableAlias := filepath.Join(aliasBin, "provider")
	if err := os.Symlink(executablePath, executableAlias); err != nil {
		t.Fatal(err)
	}
	executable, disposition := validateExecutablePath(executableAlias)
	if disposition != pathSafe {
		t.Fatalf("executable disposition=%v", disposition)
	}
	tailPath, disposition := validateSafeDirectoryPath(tail)
	if disposition != pathSafe {
		t.Fatalf("tail disposition=%v", disposition)
	}

	safePath, err := buildSafePath(executable, nil, platformDefaults{
		SafePathTail: []validatedPath{tailPath, tailPath},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := strings.Join([]string{aliasBin, bin, tail}, string(os.PathListSeparator))
	if safePath != want {
		t.Fatalf("SafePath=%q, want %q", safePath, want)
	}

	missingTail := tailPath
	missingTail.Resolved = filepath.Join(root, "missing-tail")
	if _, err := buildSafePath(executable, nil, platformDefaults{
		SafePathTail: []validatedPath{tailPath, missingTail},
	}); err == nil {
		t.Fatal("missing duplicate-key tail was accepted")
	}

	noncleanTail := tailPath
	noncleanTail.Resolved += string(filepath.Separator) + "."
	if _, err := buildSafePath(executable, nil, platformDefaults{
		SafePathTail: []validatedPath{noncleanTail},
	}); err == nil {
		t.Fatal("nonclean stored tail was accepted")
	}

	separatorDir := filepath.Join(root, "bad:path")
	if err := os.Mkdir(separatorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	separatorTail, disposition := validateSafeDirectoryPath(separatorDir)
	if disposition != pathSafe {
		t.Fatalf("separator tail disposition=%v", disposition)
	}
	if _, err := buildSafePath(executable, nil, platformDefaults{
		SafePathTail: []validatedPath{separatorTail},
	}); err == nil {
		t.Fatal("path-list separator was accepted")
	}

	aliasTail := filepath.Join(root, "tail-alias")
	if err := os.Symlink(tail, aliasTail); err != nil {
		t.Fatal(err)
	}
	validatedAlias, disposition := validateSafeDirectoryPath(aliasTail)
	if disposition != pathSafe {
		t.Fatalf("alias tail disposition=%v", disposition)
	}
	if err := os.Remove(aliasTail); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing-target"), aliasTail); err != nil {
		t.Fatal(err)
	}
	if _, err := buildSafePath(executable, nil, platformDefaults{
		SafePathTail: []validatedPath{validatedAlias},
	}); err == nil {
		t.Fatal("replaced mandatory tail alias was accepted from stale resolved evidence")
	}

	oldTail := filepath.Join(root, "old-tail")
	if err := os.Rename(tail, oldTail); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(tail, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := buildSafePath(executable, nil, platformDefaults{
		SafePathTail: []validatedPath{tailPath},
	}); err == nil {
		t.Fatal("same-spelling replacement tail was accepted from stale identity")
	}
}

func TestPlatformPathDefaultsRequireUnixFixedTails(t *testing.T) {
	defaults, err := platformPathDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if len(defaults.SafePathTail) != 2 {
		t.Fatalf("fixed tails=%d, want 2", len(defaults.SafePathTail))
	}
	got := []string{
		defaults.SafePathTail[0].Clean,
		defaults.SafePathTail[1].Clean,
	}
	if want := []string{"/usr/bin", "/bin"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fixed tail clean paths=%q, want %q", got, want)
	}
}

func TestRunFileGatewayAuthIncludesUnixNodeExecutableAndEntrypointIdentity(t *testing.T) {
	directory := newSecureUnixTestTree(t)
	launcher := filepath.Join(directory, "provider-launcher")
	if err := os.WriteFile(launcher, []byte("#!/usr/bin/env node\nfixture"), 0o700); err != nil { // #nosec G306 -- this TempDir fixture must be executable to model a Node launcher.
		t.Fatalf("write launcher: %v", err)
	}
	node := filepath.Join(directory, "node")
	writeUnixTestFile(t, node, 0o700)
	configPath := filepath.Join(directory, "config.toml")
	writeUnixTestFile(t, configPath, 0o600)

	cfg := doctorTestConfig(t, core.ProviderCodex)
	cfg.Server.Listen = "localhost:8080"
	cfg.Server.APIKeyFile = filepath.Join(directory, "gateway.key")
	configured := cfg.Providers["codex"]
	configured.Executable = launcher
	configured.PrefixArgs = nil
	cfg.Providers["codex"] = configured
	adapter := &doctorTestAdapter{name: core.ProviderCodex, interval: reportTestRange()}
	dependencies := doctorTestDependencies(map[core.ProviderName]provider.Adapter{
		core.ProviderCodex: adapter,
	})
	dependencies.GatewayExecutable = doctorTestExecutable(t)
	dependencies.ConfigIdentity = mustDoctorFileInfo(t, configPath)
	lookupCalls := 0
	dependencies.LookupExecutable = func(name string) (string, error) {
		lookupCalls++
		if name != "node" {
			t.Fatalf("executable lookup name = %q", name)
		}
		return node, nil
	}
	wantAuth := doctorGatewaySnapshot(t, "unix-node-key")
	loaderCalls := 0
	dependencies.LoadGatewayKey = func(_ string, distinct []fs.FileInfo) (gatewaykey.Snapshot, error) {
		loaderCalls++
		validatedNode, nodeDisposition := validateExecutablePath(node)
		validatedLauncher, launcherDisposition := validateExecutablePath(launcher)
		if nodeDisposition != pathSafe || launcherDisposition != pathSafe {
			t.Fatalf("node/launcher dispositions = %v/%v", nodeDisposition, launcherDisposition)
		}
		want := []fs.FileInfo{
			dependencies.ConfigIdentity,
			validatedNode.Info,
			validatedLauncher.Info,
		}
		if !sameDoctorIdentityList(distinct, want) {
			t.Fatalf("distinct identities = %#v, want config/node/entrypoint", distinct)
		}
		return wantAuth, nil
	}

	diagnosis, err := Run(context.Background(), cfg, dependencies)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if loaderCalls != 1 || lookupCalls != 1 ||
		!diagnosis.GatewayAuth().Matches("unix-node-key") {
		t.Fatalf("loader/lookup/auth = %d/%d/%#v", loaderCalls, lookupCalls, diagnosis.GatewayAuth())
	}
}

type unixTestFileInfo struct {
	mode fs.FileMode
	uid  uint32
}

func (i unixTestFileInfo) Name() string       { return "test" }
func (i unixTestFileInfo) Size() int64        { return 0 }
func (i unixTestFileInfo) Mode() fs.FileMode  { return i.mode }
func (i unixTestFileInfo) ModTime() time.Time { return time.Time{} }
func (i unixTestFileInfo) IsDir() bool        { return i.mode.IsDir() }
func (i unixTestFileInfo) Sys() any           { return &syscall.Stat_t{Uid: i.uid} }

func newSecureUnixTestTree(t *testing.T) string {
	t.Helper()
	testutil.AcquireRepositoryScanLock(t)
	root, err := os.MkdirTemp(".", ".doctor-path-test-")
	if err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // This is the exact private directory mode under test.
	if err := os.Chmod(root, 0o700); err != nil {
		_ = os.RemoveAll(root)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.RemoveAll(root); err != nil {
			t.Error(err)
		}
	})
	abs, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Clean(abs)
}

func writeUnixTestFile(t *testing.T, path string, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte("fixture"), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
