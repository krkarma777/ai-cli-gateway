//go:build linux || darwin

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsClosedUsageFormsWithoutLeakingInputs(t *testing.T) {
	root := cliFixtureRoot(t)
	valid := []string{"archives", "--repository-root", root.repository, "--staging-root", root.staging, "--output-root", root.output, "--tag", "v0.1.0", "--source-epoch", "1785805793"}
	tests := []struct {
		name string
		args []string
		want string
	}{
		{"missing subcommand", nil, "invalid_usage"},
		{"unknown subcommand", []string{"publish"}, "invalid_usage"},
		{"top help", []string{"--help"}, "invalid_usage"},
		{"subcommand help", []string{"archives", "--help"}, "invalid_usage"},
		{"missing flag", valid[:len(valid)-2], "invalid_usage"},
		{"duplicate flag", append(append([]string{}, valid...), "--tag", "v0.1.0"), "invalid_usage"},
		{"relative path", []string{"archives", "--repository-root", "private-relative", "--staging-root", root.staging, "--output-root", root.output, "--tag", "v0.1.0", "--source-epoch", "1785805793"}, "unsafe_path"},
		{"unknown flag", append(append([]string{}, valid...), "--private-unknown", "value"), "invalid_usage"},
		{"short flag", []string{"archives", "-repository-root", root.repository, "--staging-root", root.staging, "--output-root", root.output, "--tag", "v0.1.0", "--source-epoch", "1785805793"}, "invalid_usage"},
		{"trailing argument", append(append([]string{}, valid...), "private-trailing"), "invalid_usage"},
		{"invalid epoch", []string{"archives", "--repository-root", root.repository, "--staging-root", root.staging, "--output-root", root.output, "--tag", "v0.1.0", "--source-epoch", "not-private-epoch"}, "invalid_usage"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			if code := run(test.args, &stderr); code == 0 {
				t.Fatal("run exit = 0")
			}
			if got := stderr.String(); got != "releasepack: "+test.want+"\n" {
				t.Fatalf("stderr = %q", got)
			}
			assertNoCLILeak(t, stderr.String(), root)
		})
	}
}

func TestRunPreservesPackageCategoriesAndOnlyPrintsCategory(t *testing.T) {
	t.Run("invalid tag", func(t *testing.T) {
		fixture := cliFixtureRoot(t)
		args := []string{"archives", "--repository-root", fixture.repository, "--staging-root", fixture.staging, "--output-root", fixture.output, "--tag", "private-invalid-tag", "--source-epoch", "1785805793"}
		assertRunCategory(t, args, "invalid_tag", fixture)
	})
	t.Run("missing input", func(t *testing.T) {
		fixture := cliFixtureRoot(t)
		args := []string{"checksums", "--repository-root", filepath.Join(fixture.base, "private-missing"), "--staging-root", fixture.staging, "--output-root", fixture.output, "--tag", "v0.1.0"}
		assertRunCategory(t, args, "missing_input", fixture)
	})
	t.Run("archive failure", func(t *testing.T) {
		fixture := cliFixtureRoot(t)
		binary := filepath.Join(fixture.staging, "linux_amd64", "ai-cli-gateway")
		if err := os.Chmod(binary, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(binary, 0o600) })
		args := []string{"archives", "--repository-root", fixture.repository, "--staging-root", fixture.staging, "--output-root", fixture.output, "--tag", "v0.1.0", "--source-epoch", "1785805793"}
		assertRunCategory(t, args, "archive_failure", fixture)
	})
	t.Run("sbom failure", func(t *testing.T) {
		t.Setenv("RELEASEPACK_PRIVATE_ENV", "private-environment-secret")
		fixture := cliFixtureRoot(t)
		mustMkdirCLI(t, fixture.output)
		writeCLIArchives(t, fixture.output)
		raw := filepath.Join(fixture.base, "private-raw.spdx.json")
		mustWriteCLI(t, raw, `{"private-raw-spdx-secret":`)
		args := []string{"sbom", "--repository-root", fixture.repository, "--staging-root", fixture.staging, "--output-root", fixture.output, "--raw-sbom", raw, "--tag", "v0.1.0", "--source-epoch", "1785805793"}
		assertRunCategory(t, args, "sbom_failure", fixture)
	})
	t.Run("checksum failure", func(t *testing.T) {
		fixture := cliFixtureRoot(t)
		mustMkdirCLI(t, fixture.output)
		writeCLIArchives(t, fixture.output)
		mustWriteCLI(t, filepath.Join(fixture.output, "ai-cli-gateway_0.1.0_sbom.spdx.json"), "sbom")
		subject := filepath.Join(fixture.output, "ai-cli-gateway_0.1.0_darwin_amd64.tar.gz")
		if err := os.Chmod(subject, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(subject, 0o600) })
		args := []string{"checksums", "--repository-root", fixture.repository, "--staging-root", fixture.staging, "--output-root", fixture.output, "--tag", "v0.1.0"}
		assertRunCategory(t, args, "checksum_failure", fixture)
	})
}

func TestRunArchivesCommandDryRun(t *testing.T) {
	fixture := cliFixtureRoot(t)
	if externalRoot := os.Getenv("RELEASEPACK_DRYRUN_FIXTURE_ROOT"); externalRoot != "" {
		fixture = cliFixtureAt(t, externalRoot)
	}
	args := []string{"archives", "--repository-root", fixture.repository, "--staging-root", fixture.staging, "--output-root", fixture.output, "--tag", "v0.1.0", "--source-epoch", "1785805793"}
	var stderr bytes.Buffer
	if code := run(args, &stderr); code != 0 {
		t.Fatalf("run(archives) exit = %d, stderr = %q", code, stderr.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want empty", stderr.String())
	}
	entries, err := os.ReadDir(fixture.output)
	if err != nil {
		t.Fatalf("ReadDir(output): %v", err)
	}
	if len(entries) != 5 {
		t.Fatalf("output entries = %d, want five archives", len(entries))
	}
}

type cliFixture struct{ base, repository, staging, output string }

func cliFixtureRoot(t *testing.T) cliFixture {
	t.Helper()
	return cliFixtureAt(t, t.TempDir())
}
func cliFixtureAt(t *testing.T, requestedBase string) cliFixture {
	t.Helper()
	mustMkdirCLI(t, requestedBase)
	base, err := filepath.EvalSymlinks(requestedBase)
	if err != nil {
		t.Fatal(err)
	}
	fixture := cliFixture{base: base, repository: filepath.Join(base, "private-repository-marker"), staging: filepath.Join(base, "private-staging-marker"), output: filepath.Join(base, "private-output-marker")}
	mustMkdirCLI(t, fixture.repository)
	mustMkdirCLI(t, fixture.staging)
	sources := []string{"go.mod", "README.md", "LICENSE", "THIRD_PARTY_NOTICES.md", "config.example.toml", "examples/config/codex.example.toml", "examples/openai-sdk/python/main.py", "examples/openai-sdk/python/requirements.txt", "examples/openai-sdk/python/requirements.lock", "examples/openai-sdk/javascript/main.mjs", "examples/openai-sdk/javascript/package.json", "examples/openai-sdk/javascript/package-lock.json", "deploy/systemd/ai-cli-gateway.service"}
	for _, name := range sources {
		contents := "fixture\n"
		if name == "go.mod" {
			contents = "module github.com/krkarma777/ai-cli-gateway\n\ngo 1.26.0\n"
		}
		mustWriteCLI(t, filepath.Join(fixture.repository, filepath.FromSlash(name)), contents)
	}
	targets := []struct{ dir, exe string }{{"linux_amd64", "ai-cli-gateway"}, {"linux_arm64", "ai-cli-gateway"}, {"darwin_amd64", "ai-cli-gateway"}, {"darwin_arm64", "ai-cli-gateway"}, {"windows_amd64", "ai-cli-gateway.exe"}}
	for _, target := range targets {
		mustWriteCLI(t, filepath.Join(fixture.staging, target.dir, target.exe), "private fake binary\n")
	}
	return fixture
}
func writeCLIArchives(t *testing.T, root string) {
	for _, name := range []string{"ai-cli-gateway_0.1.0_linux_amd64.tar.gz", "ai-cli-gateway_0.1.0_linux_arm64.tar.gz", "ai-cli-gateway_0.1.0_darwin_amd64.tar.gz", "ai-cli-gateway_0.1.0_darwin_arm64.tar.gz", "ai-cli-gateway_0.1.0_windows_amd64.zip"} {
		mustWriteCLI(t, filepath.Join(root, name), name)
	}
}
func mustWriteCLI(t *testing.T, path, contents string) {
	t.Helper()
	mustMkdirCLI(t, filepath.Dir(path))
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
func mustMkdirCLI(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
}
func assertRunCategory(t *testing.T, args []string, want string, fixture cliFixture) {
	t.Helper()
	var stderr bytes.Buffer
	if code := run(args, &stderr); code == 0 {
		t.Fatal("run exit = 0")
	}
	if got := stderr.String(); got != "releasepack: "+want+"\n" {
		t.Fatalf("stderr = %q, want category %q", got, want)
	}
	assertNoCLILeak(t, stderr.String(), fixture)
}
func assertNoCLILeak(t *testing.T, stderr string, fixture cliFixture) {
	t.Helper()
	for _, secret := range []string{fixture.base, fixture.repository, fixture.staging, fixture.output, "private-invalid-tag", "private fake binary", "private-raw-spdx-secret", "private-environment-secret", "not-private-epoch", "private-trailing"} {
		if secret != "" && strings.Contains(stderr, secret) {
			t.Fatalf("stderr leaked supplied value %q: %q", secret, stderr)
		}
	}
}
