package doctor

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/krkarma777/ai-cli-gateway/internal/trustedpath"
)

type pathDisposition uint8

const (
	pathUnsafe pathDisposition = iota
	pathMissing
	pathSafe
)

type pathKind uint8

const (
	pathKindExecutable pathKind = iota
	pathKindEntrypoint
	pathKindConfigHome
	pathKindCredential
	pathKindSafeDirectory
)

var (
	errPathUnsafe  = errors.New("path is unsafe")
	errPathMissing = errors.New("path is missing")
)

type validatedPath struct {
	Clean        string
	Resolved     string
	CanonicalKey string
	Info         fs.FileInfo
}

type platformDefaults struct {
	SafePathTail     []validatedPath
	FrozenSystemRoot string
}

func resolveGatewayExecutable(path string) (string, error) {
	validated, disposition := validateExecutablePath(path)
	if disposition != pathSafe {
		return "", errPathUnsafe
	}
	return validated.Resolved, nil
}

func validateExecutablePath(path string) (validatedPath, pathDisposition) {
	return validatePlatformPath(path, pathKindExecutable)
}

func validateConfigHomePath(path string) (validatedPath, pathDisposition) {
	return validatePlatformPath(path, pathKindConfigHome)
}

func validateCredentialPath(path string) (validatedPath, pathDisposition) {
	return validatePlatformPath(path, pathKindCredential)
}

func validateSafeDirectoryPath(path string) (validatedPath, pathDisposition) {
	return validatePlatformPath(path, pathKindSafeDirectory)
}

func validateTrustedCommandPath(path string) (validatedPath, pathDisposition) {
	inspection, err := trustedpath.OpenCommandPath(
		path,
		trustedpath.CommandIdentityOnly,
		0,
	)
	if err != nil {
		if errors.Is(err, trustedpath.ErrMissing) {
			return validatedPath{}, pathMissing
		}
		return validatedPath{}, pathUnsafe
	}
	if inspection == nil {
		return validatedPath{}, pathUnsafe
	}
	metadata, metadataOK := trustedpath.InspectionPath(inspection)
	info := inspection.FileInfo()
	revalidateErr := inspection.Revalidate()
	closeErr := inspection.Close()
	if !metadataOK || info == nil || revalidateErr != nil || closeErr != nil {
		return validatedPath{}, pathUnsafe
	}
	return validatedPath{
		Clean:        metadata.Clean,
		Resolved:     metadata.Resolved,
		CanonicalKey: metadata.CanonicalKey,
		Info:         info,
	}, pathSafe
}

func buildSafePath(
	executable validatedPath,
	entrypoint *validatedPath,
	defaults platformDefaults,
) (string, error) {
	candidates := []string{
		filepath.Dir(executable.Clean),
		filepath.Dir(executable.Resolved),
	}
	if entrypoint != nil {
		candidates = append(
			candidates,
			filepath.Dir(entrypoint.Clean),
			filepath.Dir(entrypoint.Resolved),
		)
	}

	validated := make(
		[]validatedPath,
		0,
		len(candidates)+len(defaults.SafePathTail),
	)
	for _, candidate := range candidates {
		if !validSafePathSpelling(candidate) {
			return "", errPathUnsafe
		}
		path, disposition := validateSafeDirectoryPath(candidate)
		if disposition != pathSafe || !validSafePathSpelling(path.Resolved) {
			return "", errPathUnsafe
		}
		validated = append(validated, path)
	}
	for _, tail := range defaults.SafePathTail {
		if !validSafePathSpelling(tail.Clean) ||
			!validSafePathSpelling(tail.Resolved) {
			return "", errPathUnsafe
		}
		rechecked, disposition := validateSafeDirectoryPath(tail.Clean)
		if disposition != pathSafe ||
			rechecked.Resolved != tail.Resolved ||
			!sameValidatedIdentity(rechecked, tail) {
			return "", errPathUnsafe
		}
		validated = append(validated, rechecked)
	}

	unique := make([]validatedPath, 0, len(validated))
	for _, candidate := range validated {
		duplicate := false
		for _, prior := range unique {
			if sameValidatedPath(prior, candidate) {
				duplicate = true
				break
			}
		}
		if !duplicate {
			unique = append(unique, candidate)
		}
	}

	parts := make([]string, len(unique))
	for index := range unique {
		parts[index] = unique[index].Resolved
	}
	if len(parts) == 0 {
		return "", errPathUnsafe
	}
	return strings.Join(parts, string(os.PathListSeparator)), nil
}

func validSafePathSpelling(path string) bool {
	return path != "" &&
		filepath.IsAbs(path) &&
		filepath.Clean(path) == path &&
		strings.IndexByte(path, 0) < 0 &&
		!strings.ContainsRune(path, os.PathListSeparator)
}

func sameValidatedPath(left, right validatedPath) bool {
	if left.CanonicalKey != "" &&
		right.CanonicalKey != "" &&
		left.CanonicalKey == right.CanonicalKey {
		return true
	}
	return sameValidatedIdentity(left, right)
}

func sameValidatedIdentity(left, right validatedPath) bool {
	return left.Info != nil && right.Info != nil && os.SameFile(left.Info, right.Info)
}
