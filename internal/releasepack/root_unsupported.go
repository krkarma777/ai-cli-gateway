//go:build !linux && !darwin

package releasepack

import "io/fs"

func validateReleasepackHost() error {
	return newCategorizedError(categoryInvalidUsage)
}

func validateOutputAuthority(string, fs.FileInfo) error {
	return newCategorizedError(categoryInvalidUsage)
}
