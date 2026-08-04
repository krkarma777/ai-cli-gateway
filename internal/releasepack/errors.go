package releasepack

import "errors"

type errorCategory string

const (
	categoryInvalidTag      errorCategory = "invalid_tag"
	categoryInvalidUsage    errorCategory = "invalid_usage"
	categoryUnsafePath      errorCategory = "unsafe_path"
	categoryMissingInput    errorCategory = "missing_input"
	categoryArchiveFailure  errorCategory = "archive_failure"
	categorySBOMFailure     errorCategory = "sbom_failure"
	categoryChecksumFailure errorCategory = "checksum_failure"
	categoryInternalError   errorCategory = "internal_error"
)

type categorizedError struct {
	category errorCategory
}

func (e *categorizedError) Error() string {
	return string(e.category)
}

func newCategorizedError(category errorCategory) error {
	return &categorizedError{category: category}
}

func newArchiveFailure() error {
	return newCategorizedError(categoryArchiveFailure)
}

// ErrorCategory returns the stable, non-sensitive category for err. Foreign
// errors are deliberately collapsed to internal_error.
func ErrorCategory(err error) string {
	if err == nil {
		return ""
	}

	var categorized *categorizedError
	if !errors.As(err, &categorized) || !knownErrorCategory(categorized.category) {
		return string(categoryInternalError)
	}
	return string(categorized.category)
}

func knownErrorCategory(category errorCategory) bool {
	switch category {
	case categoryInvalidTag,
		categoryInvalidUsage,
		categoryUnsafePath,
		categoryMissingInput,
		categoryArchiveFailure,
		categorySBOMFailure,
		categoryChecksumFailure,
		categoryInternalError:
		return true
	default:
		return false
	}
}
