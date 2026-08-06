//go:build linux || darwin

package releasepack

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

func TestWriteChecksumsWritesExactSortedBinaryRecords(t *testing.T) {
	fixture := newChecksumFixture(t)
	asset, err := WriteChecksums(fixture.options)
	if err != nil {
		t.Fatalf("WriteChecksums() error = %v", err)
	}
	if asset.Name != "SHA256SUMS" || asset.Path != filepath.Join(fixture.outputRoot, "SHA256SUMS") {
		t.Fatalf("asset = %#v", asset)
	}
	if mode := mustStat(t, asset.Path).Mode().Perm(); mode != 0o644 {
		t.Fatalf("SHA256SUMS mode = %04o, want 0644", mode)
	}
	records := strings.Split(strings.TrimSuffix(string(mustReadFile(t, asset.Path)), "\n"), "\n")
	wantNames := append(expectedArchiveNames("0.1.0"), "ai-cli-gateway_0.1.0_sbom.spdx.json")
	slices.Sort(wantNames)
	if len(records) != len(wantNames) {
		t.Fatalf("record count = %d, want %d", len(records), len(wantNames))
	}
	for i, record := range records {
		parts := strings.SplitN(record, " *", 2)
		if len(parts) != 2 || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(parts[0]) || parts[1] != wantNames[i] {
			t.Fatalf("record[%d] = %q, want digest for %q", i, record, wantNames[i])
		}
	}
	if err := independentlyVerifyChecksums(asset.Path); err != nil {
		t.Fatalf("independent verification: %v", err)
	}
	mustWriteFile(t, filepath.Join(fixture.outputRoot, wantNames[0]), "mutated\n")
	if err := independentlyVerifyChecksums(asset.Path); err == nil {
		t.Fatal("independent verifier accepted mutated asset")
	}
}

func TestWriteChecksumsRejectsInvalidRootsAndClosedMembership(t *testing.T) {
	tests := []struct {
		name     string
		alter    func(*testing.T, *checksumFixture)
		category errorCategory
	}{
		{"relative root", func(_ *testing.T, fixture *checksumFixture) { fixture.options.OutputRoot = "relative" }, categoryUnsafePath},
		{"symlinked root", func(t *testing.T, fixture *checksumFixture) {
			link := filepath.Join(filepath.Dir(fixture.outputRoot), "output-link")
			mustSymlink(t, fixture.outputRoot, link)
			fixture.options.OutputRoot = link
		}, categoryUnsafePath},
		{"overlapping root", func(_ *testing.T, fixture *checksumFixture) { fixture.options.OutputRoot = fixture.repositoryRoot }, categoryUnsafePath},
		{"missing asset", func(t *testing.T, fixture *checksumFixture) {
			mustRemove(t, filepath.Join(fixture.outputRoot, expectedArchiveNames("0.1.0")[0]))
		}, categoryMissingInput},
		{"extra file", func(t *testing.T, fixture *checksumFixture) {
			mustWriteFile(t, filepath.Join(fixture.outputRoot, "extra"), "extra")
		}, categoryUnsafePath},
		{"malformed asset name", func(t *testing.T, fixture *checksumFixture) {
			old := filepath.Join(fixture.outputRoot, expectedArchiveNames("0.1.0")[0])
			mustRename(t, old, old+".bad")
		}, categoryUnsafePath},
		{"symlink asset", func(t *testing.T, fixture *checksumFixture) {
			path := filepath.Join(fixture.outputRoot, expectedArchiveNames("0.1.0")[0])
			mustRemove(t, path)
			mustSymlink(t, fixture.options.RepositoryRoot, path)
		}, categoryUnsafePath},
		{"existing sums", func(t *testing.T, fixture *checksumFixture) {
			mustWriteFile(t, filepath.Join(fixture.outputRoot, "SHA256SUMS"), "existing")
		}, categoryUnsafePath},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newChecksumFixture(t)
			test.alter(t, &fixture)
			asset, err := WriteChecksums(fixture.options)
			if asset != (Asset{}) {
				t.Fatalf("asset = %#v", asset)
			}
			assertCategory(t, err, test.category)
		})
	}
}

func TestWriteChecksumsOperationalFailuresAreClosedAndClean(t *testing.T) {
	tests := []struct {
		name string
		fail func(*testing.T)
	}{
		{"asset read", func(t *testing.T) {
			original := checksumOpenAsset
			checksumOpenAsset = func(string) (*os.File, error) { return nil, errors.New("private asset") }
			t.Cleanup(func() { checksumOpenAsset = original })
		}},
		{"output write", func(t *testing.T) {
			original := checksumWriteOutput
			checksumWriteOutput = func(io.Writer, []byte) (int, error) { return 0, errors.New("private output") }
			t.Cleanup(func() { checksumWriteOutput = original })
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newChecksumFixture(t)
			test.fail(t)
			asset, err := WriteChecksums(fixture.options)
			if asset != (Asset{}) {
				t.Fatalf("asset = %#v", asset)
			}
			assertCategory(t, err, categoryChecksumFailure)
			if _, statErr := os.Lstat(filepath.Join(fixture.outputRoot, "SHA256SUMS")); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("partial sums stat = %v", statErr)
			}
		})
	}
}

func TestWriteChecksumsPreservesAttackerReplacement(t *testing.T) {
	fixture := newChecksumFixture(t)
	finalPath := filepath.Join(fixture.outputRoot, "SHA256SUMS")
	const attacker = "attacker-owned\n"
	original := checksumWriteOutput
	checksumWriteOutput = func(writer io.Writer, data []byte) (int, error) {
		n, err := writer.Write(data)
		if err != nil {
			return n, err
		}
		if err := os.Remove(finalPath); err != nil {
			return n, err
		}
		if err := os.WriteFile(finalPath, []byte(attacker), 0o600); err != nil {
			return n, err
		}
		return n, nil
	}
	t.Cleanup(func() { checksumWriteOutput = original })

	asset, err := WriteChecksums(fixture.options)
	if asset != (Asset{}) {
		t.Fatalf("asset = %#v, want zero", asset)
	}
	assertCategory(t, err, categoryChecksumFailure)
	if got := string(mustReadFile(t, finalPath)); got != attacker {
		t.Fatalf("replacement = %q, want preserved", got)
	}
}

func TestWriteChecksumsRejectsUnsafeOutputAuthorityBeforeHashing(t *testing.T) {
	tests := []struct {
		name  string
		alter func(*testing.T, *checksumFixture)
	}{
		{"mode", func(t *testing.T, fixture *checksumFixture) {
			//nolint:gosec // The intentionally permissive directory mode is the condition under test.
			if err := os.Chmod(fixture.outputRoot, 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"foreign owner", func(t *testing.T, _ *checksumFixture) {
			original := releasepackEffectiveUID
			releasepackEffectiveUID = func() int { return os.Geteuid() + 1 }
			t.Cleanup(func() { releasepackEffectiveUID = original })
		}},
		{"writable ancestor", func(t *testing.T, fixture *checksumFixture) {
			ancestor := filepath.Join(filepath.Dir(fixture.outputRoot), "unsafe")
			mustMkdir(t, ancestor)
			//nolint:gosec // The intentionally unsafe directory mode is required by this rejection test.
			if err := os.Chmod(ancestor, 0o777); err != nil {
				t.Fatal(err)
			}
			//nolint:gosec // Cleanup restores the owner-only mode on this test directory.
			t.Cleanup(func() { _ = os.Chmod(ancestor, 0o700) })
			mustRename(t, fixture.outputRoot, filepath.Join(ancestor, "output"))
			fixture.outputRoot = filepath.Join(ancestor, "output")
			fixture.options.OutputRoot = fixture.outputRoot
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newChecksumFixture(t)
			test.alter(t, &fixture)
			calls := 0
			original := checksumOpenAsset
			checksumOpenAsset = func(string) (*os.File, error) { calls++; return nil, errors.New("must not run") }
			t.Cleanup(func() { checksumOpenAsset = original })
			_, err := WriteChecksums(fixture.options)
			assertCategory(t, err, categoryUnsafePath)
			if calls != 0 {
				t.Fatalf("open calls = %d, want zero", calls)
			}
		})
	}
}

type checksumFixture struct {
	repositoryRoot, stagingRoot, outputRoot string
	options                                 ChecksumOptions
}

func newChecksumFixture(t *testing.T) checksumFixture {
	t.Helper()
	release := newReleaseFixture(t)
	mustMkdir(t, release.outputRoot)
	writeExpectedArchives(t, release.outputRoot, "0.1.0")
	mustWriteFile(t, filepath.Join(release.outputRoot, "ai-cli-gateway_0.1.0_sbom.spdx.json"), "sbom\n")
	return checksumFixture{repositoryRoot: release.repositoryRoot, stagingRoot: release.stagingRoot, outputRoot: release.outputRoot, options: ChecksumOptions{RepositoryRoot: release.repositoryRoot, StagingRoot: release.stagingRoot, OutputRoot: release.outputRoot, Tag: "v0.1.0"}}
}

func independentlyVerifyChecksums(path string) (resultErr error) {
	//nolint:gosec // The verifier receives the generated manifest path from a private test fixture.
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && resultErr == nil {
			resultErr = closeErr
		}
	}()
	seen := map[string]struct{}{}
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, " *", 2)
		if len(parts) != 2 || len(parts[0]) != 64 || filepath.Base(parts[1]) != parts[1] {
			return errors.New("malformed manifest")
		}
		if _, err := hex.DecodeString(parts[0]); err != nil {
			return errors.New("malformed digest")
		}
		if _, exists := seen[parts[1]]; exists {
			return errors.New("duplicate record")
		}
		seen[parts[1]] = struct{}{}
		subject, err := os.Open(filepath.Join(filepath.Dir(path), parts[1]))
		if err != nil {
			return err
		}
		hasher := sha256.New()
		_, copyErr := io.Copy(hasher, subject)
		closeErr := subject.Close()
		if copyErr != nil || closeErr != nil {
			return errors.New("subject read")
		}
		if hex.EncodeToString(hasher.Sum(nil)) != parts[0] {
			return errors.New("digest mismatch")
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}
	if len(seen) != 6 {
		return errors.New("wrong record count")
	}
	return nil
}
