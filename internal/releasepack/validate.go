package releasepack

import (
	"bufio"
	"errors"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const releaseModulePath = "github.com/krkarma777/ai-cli-gateway"

var exactReleaseTag = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)

var validateModuleClose = (*os.File).Close

var commonSourceNames = [...]string{
	"go.mod",
	"README.md",
	"LICENSE",
	"THIRD_PARTY_NOTICES.md",
	"config.example.toml",
	"examples/config/codex.example.toml",
	"examples/openai-sdk/python/main.py",
	"examples/openai-sdk/python/requirements.txt",
	"examples/openai-sdk/python/requirements.lock",
	"examples/openai-sdk/javascript/main.mjs",
	"examples/openai-sdk/javascript/package.json",
	"examples/openai-sdk/javascript/package-lock.json",
}

const systemdSourceName = "deploy/systemd/ai-cli-gateway.service"

func newArchivePlan(options ArchiveOptions) (archivePlan, error) {
	if err := validateRootSet(options.RepositoryRoot, options.StagingRoot, options.OutputRoot, true); err != nil {
		return archivePlan{}, err
	}
	version, sourceTime, err := validateTagAndSourceTime(options.Tag, options.SourceTime)
	if err != nil {
		return archivePlan{}, err
	}
	sources, binaries, err := validateRepositoryAndStaging(options.RepositoryRoot, options.StagingRoot)
	if err != nil {
		return archivePlan{}, err
	}
	if err := validateEmptyOutputRoot(options.OutputRoot); err != nil {
		return archivePlan{}, err
	}

	targets := append([]target(nil), releaseTargets[:]...)
	return archivePlan{
		RepositoryRoot: options.RepositoryRoot,
		StagingRoot:    options.StagingRoot,
		OutputRoot:     options.OutputRoot,
		Tag:            options.Tag,
		Version:        version,
		SourceTime:     sourceTime,
		Sources: sourceSet{
			Common:  append([]sourceFile(nil), sources.Common...),
			Systemd: sources.Systemd,
		},
		Binaries: append([]stagedBinary(nil), binaries...),
		Targets:  targets,
	}, nil
}

func validateTag(tag string) (string, error) {
	if !exactReleaseTag.MatchString(tag) {
		return "", newCategorizedError(categoryInvalidTag)
	}
	return strings.TrimPrefix(tag, "v"), nil
}

func validateSourceTime(sourceTime time.Time) (time.Time, error) {
	minimum := time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC).Unix()
	if sourceTime.Location() != time.UTC ||
		sourceTime.Nanosecond() != 0 ||
		sourceTime.Unix() < minimum ||
		sourceTime.Unix() > int64(math.MaxUint32) {
		return time.Time{}, newCategorizedError(categoryMissingInput)
	}
	return sourceTime, nil
}

func validateTagAndSourceTime(tag string, sourceTime time.Time) (string, time.Time, error) {
	version, err := validateTag(tag)
	if err != nil {
		return "", time.Time{}, err
	}
	validatedTime, err := validateSourceTime(sourceTime)
	if err != nil {
		return "", time.Time{}, err
	}
	return version, validatedTime, nil
}

func validateRootSet(repositoryRoot, stagingRoot, outputRoot string, allowAbsentOutput bool) error {
	if err := validateReleasepackHost(); err != nil {
		return err
	}
	if err := validateRequiredDirectoryRoot(repositoryRoot); err != nil {
		return err
	}
	if err := validateRequiredDirectoryRoot(stagingRoot); err != nil {
		return err
	}
	if err := validateOutputRoot(outputRoot, allowAbsentOutput); err != nil {
		return err
	}

	roots := [...]string{repositoryRoot, stagingRoot, outputRoot}
	for i := 0; i < len(roots); i++ {
		for j := i + 1; j < len(roots); j++ {
			if rootsOverlap(roots[i], roots[j]) {
				return newCategorizedError(categoryUnsafePath)
			}
		}
	}
	return nil
}

func validateRepositoryAndStaging(repositoryRoot, stagingRoot string) (sourceSet, []stagedBinary, error) {
	common := make([]sourceFile, 0, len(commonSourceNames))
	for _, name := range commonSourceNames {
		path, err := validateRegularDescendant(repositoryRoot, filepath.FromSlash(name))
		if err != nil {
			return sourceSet{}, nil, err
		}
		common = append(common, sourceFile{Name: name, Path: path})
	}
	systemdPath, err := validateRegularDescendant(repositoryRoot, filepath.FromSlash(systemdSourceName))
	if err != nil {
		return sourceSet{}, nil, err
	}
	if err := validateModuleDeclaration(common[0].Path); err != nil {
		return sourceSet{}, nil, err
	}

	targetDirectories := make([]string, 0, len(releaseTargets))
	for _, releaseTarget := range releaseTargets {
		targetDirectories = append(targetDirectories, releaseTarget.Directory)
	}
	if err := validateExactDirectoryNames(stagingRoot, targetDirectories); err != nil {
		return sourceSet{}, nil, err
	}

	binaries := make([]stagedBinary, 0, len(releaseTargets))
	for _, releaseTarget := range releaseTargets {
		directory, err := validateDirectoryDescendant(stagingRoot, releaseTarget.Directory)
		if err != nil {
			return sourceSet{}, nil, err
		}
		if err := validateExactDirectoryNames(directory, []string{releaseTarget.Executable}); err != nil {
			return sourceSet{}, nil, err
		}
		binaryPath, err := validateRegularDescendant(stagingRoot, filepath.Join(releaseTarget.Directory, releaseTarget.Executable))
		if err != nil {
			return sourceSet{}, nil, err
		}
		binaries = append(binaries, stagedBinary{Target: releaseTarget, Path: binaryPath})
	}

	return sourceSet{
		Common:  common,
		Systemd: sourceFile{Name: systemdSourceName, Path: systemdPath},
	}, binaries, nil
}

func validateRequiredDirectoryRoot(root string) error {
	if err := validateAbsoluteCleanPath(root); err != nil {
		return err
	}
	if err := validateExistingComponents(root); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newCategorizedError(categoryMissingInput)
		}
		return newCategorizedError(categoryUnsafePath)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return newCategorizedError(categoryUnsafePath)
	}
	return nil
}

func validateOutputRoot(root string, allowAbsent bool) error {
	if err := validateAbsoluteCleanPath(root); err != nil {
		return err
	}
	if err := validateExistingComponents(root); err != nil {
		return err
	}
	info, err := os.Lstat(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) && allowAbsent {
			return validateOutputAuthority(root, nil)
		}
		if errors.Is(err, os.ErrNotExist) {
			return newCategorizedError(categoryMissingInput)
		}
		return newCategorizedError(categoryUnsafePath)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return newCategorizedError(categoryUnsafePath)
	}
	return validateOutputAuthority(root, info)
}

func validateAbsoluteCleanPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return newCategorizedError(categoryUnsafePath)
	}
	return nil
}

func validateExistingComponents(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return newCategorizedError(categoryUnsafePath)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return newCategorizedError(categoryUnsafePath)
		}

		parent := filepath.Dir(current)
		if parent == current {
			return nil
		}
	}
}

func rootsOverlap(first, second string) bool {
	return pathContains(first, second) || pathContains(second, first)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil || filepath.IsAbs(relative) {
		return false
	}
	return relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func validateRegularDescendant(root, relative string) (string, error) {
	return validateDescendant(root, relative, false)
}

func validateDirectoryDescendant(root, relative string) (string, error) {
	return validateDescendant(root, relative, true)
}

func validateDescendant(root, relative string, directory bool) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return "", newCategorizedError(categoryInternalError)
	}
	parts := strings.Split(relative, string(filepath.Separator))
	current := root
	for i, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", newCategorizedError(categoryInternalError)
		}
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return "", newCategorizedError(categoryMissingInput)
			}
			return "", newCategorizedError(categoryUnsafePath)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return "", newCategorizedError(categoryUnsafePath)
		}
		leaf := i == len(parts)-1
		if !leaf && !info.IsDir() {
			return "", newCategorizedError(categoryUnsafePath)
		}
		if leaf {
			if directory && !info.IsDir() {
				return "", newCategorizedError(categoryUnsafePath)
			}
			if !directory && !info.Mode().IsRegular() {
				return "", newCategorizedError(categoryUnsafePath)
			}
		}
	}
	return current, nil
}

func validateExactDirectoryNames(directory string, expected []string) error {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return newCategorizedError(categoryUnsafePath)
	}
	wanted := append([]string(nil), expected...)
	slices.Sort(wanted)

	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		actual = append(actual, entry.Name())
	}
	for _, name := range actual {
		if _, found := slices.BinarySearch(wanted, name); !found {
			return newCategorizedError(categoryUnsafePath)
		}
	}
	for _, name := range wanted {
		if _, found := slices.BinarySearch(actual, name); !found {
			return newCategorizedError(categoryMissingInput)
		}
	}
	return nil
}

func validateModuleDeclaration(path string) error {
	//nolint:gosec // The module path was validated as a regular repository descendant.
	file, err := os.Open(path)
	if err != nil {
		return newCategorizedError(categoryMissingInput)
	}

	want := "module " + releaseModulePath
	moduleLines := 0
	valid := false
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "module") {
			moduleLines++
			valid = line == want
		}
	}
	scanErr := scanner.Err()
	closeErr := validateModuleClose(file)
	if scanErr != nil || closeErr != nil || moduleLines != 1 || !valid {
		return newCategorizedError(categoryMissingInput)
	}
	return nil
}

func validateEmptyOutputRoot(outputRoot string) error {
	info, err := os.Lstat(outputRoot)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return newCategorizedError(categoryUnsafePath)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return newCategorizedError(categoryUnsafePath)
	}
	entries, err := os.ReadDir(outputRoot)
	if err != nil || len(entries) != 0 {
		return newCategorizedError(categoryUnsafePath)
	}
	return nil
}

func expectedArchiveNames(version string) []string {
	names := make([]string, 0, len(releaseTargets))
	for _, releaseTarget := range releaseTargets {
		extension := ".tar.gz"
		if releaseTarget.Format == formatZIP {
			extension = ".zip"
		}
		names = append(names, "ai-cli-gateway_"+version+"_"+releaseTarget.GOOS+"_"+releaseTarget.GOARCH+extension)
	}
	return names
}

func validateExactRegularFiles(root string, expected []string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, newCategorizedError(categoryUnsafePath)
	}
	wanted := append([]string(nil), expected...)
	slices.Sort(wanted)
	actual := make([]string, 0, len(entries))
	for _, entry := range entries {
		actual = append(actual, entry.Name())
	}
	slices.Sort(actual)
	for _, name := range actual {
		if _, found := slices.BinarySearch(wanted, name); !found {
			return nil, newCategorizedError(categoryUnsafePath)
		}
	}
	for _, name := range wanted {
		if _, found := slices.BinarySearch(actual, name); !found {
			return nil, newCategorizedError(categoryMissingInput)
		}
		if _, err := validateRegularDescendant(root, name); err != nil {
			return nil, err
		}
	}
	return wanted, nil
}
