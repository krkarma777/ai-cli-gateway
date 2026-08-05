package releasepack

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
)

type ChecksumOptions struct {
	RepositoryRoot string
	StagingRoot    string
	OutputRoot     string
	Tag            string
}

var (
	checksumOpenAsset   = os.Open
	checksumWriteOutput = func(writer io.Writer, data []byte) (int, error) { return writer.Write(data) }
)

func WriteChecksums(options ChecksumOptions) (asset Asset, resultErr error) {
	if err := validateRootSet(options.RepositoryRoot, options.StagingRoot, options.OutputRoot, false); err != nil {
		return Asset{}, err
	}
	version, err := validateTag(options.Tag)
	if err != nil {
		return Asset{}, err
	}
	if _, _, err := validateRepositoryAndStaging(options.RepositoryRoot, options.StagingRoot); err != nil {
		return Asset{}, err
	}
	expected := append(expectedArchiveNames(version), "ai-cli-gateway_"+version+"_sbom.spdx.json")
	names, err := validateExactRegularFiles(options.OutputRoot, expected)
	if err != nil {
		return Asset{}, err
	}

	var manifest strings.Builder
	for _, name := range names {
		path := filepath.Join(options.OutputRoot, name)
		before, err := os.Lstat(path)
		if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
			return Asset{}, newChecksumFailure()
		}
		file, err := checksumOpenAsset(path)
		if err != nil || file == nil {
			return Asset{}, newChecksumFailure()
		}
		descriptorInfo, statErr := file.Stat()
		hasher := sha256.New()
		_, copyErr := io.Copy(hasher, file)
		closeErr := file.Close()
		after, pathErr := os.Lstat(path)
		if statErr != nil || copyErr != nil || closeErr != nil || pathErr != nil ||
			!descriptorInfo.Mode().IsRegular() || !os.SameFile(before, descriptorInfo) || !os.SameFile(descriptorInfo, after) {
			return Asset{}, newChecksumFailure()
		}
		manifest.WriteString(hex.EncodeToString(hasher.Sum(nil)))
		manifest.WriteString(" *")
		manifest.WriteString(name)
		manifest.WriteByte('\n')
	}

	asset = Asset{Name: "SHA256SUMS", Path: filepath.Join(options.OutputRoot, "SHA256SUMS")}
	file, err := os.OpenFile(asset.Path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return Asset{}, newChecksumFailure()
	}
	createdInfo, statErr := file.Stat()
	if statErr != nil || !createdInfo.Mode().IsRegular() {
		_ = file.Close()
		cleanupCreatedFile(asset.Path, createdInfo)
		return Asset{}, newChecksumFailure()
	}
	succeeded := false
	defer func() {
		if !succeeded {
			asset = Asset{}
			cleanupCreatedFile(filepath.Join(options.OutputRoot, "SHA256SUMS"), createdInfo)
			resultErr = newChecksumFailure()
		}
	}()
	data := []byte(manifest.String())
	if n, writeErr := checksumWriteOutput(file, data); writeErr != nil || n != len(data) {
		_ = file.Close()
		return Asset{}, newChecksumFailure()
	}
	if err := file.Chmod(0o644); err != nil {
		_ = file.Close()
		return Asset{}, newChecksumFailure()
	}
	if err := file.Close(); err != nil {
		return Asset{}, newChecksumFailure()
	}
	current, err := os.Lstat(asset.Path)
	if err != nil || !os.SameFile(createdInfo, current) {
		return Asset{}, newChecksumFailure()
	}
	succeeded = true
	return asset, nil
}
