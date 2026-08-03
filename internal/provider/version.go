package provider

import (
	"errors"
	"strconv"
	"unicode"
	"unicode/utf8"
)

const (
	maxVersionOutputBytes    = 4096
	maxVersionComponentBytes = 20
)

var errInvalidVersion = errors.New("provider version is invalid")

// Version is a numeric provider CLI version.
type Version struct {
	Major uint64
	Minor uint64
	Patch uint64
}

// String returns the canonical numeric major.minor.patch representation.
func (v Version) String() string {
	return strconv.FormatUint(v.Major, 10) + "." +
		strconv.FormatUint(v.Minor, 10) + "." +
		strconv.FormatUint(v.Patch, 10)
}

// Range is an inclusive-minimum, exclusive-maximum version interval.
type Range struct {
	MinInclusive Version
	MaxExclusive Version
}

// Contains reports whether version lies within the numeric interval.
func (r Range) Contains(version Version) bool {
	return compareVersion(version, r.MinInclusive) >= 0 &&
		compareVersion(version, r.MaxExclusive) < 0
}

// ParseVersion extracts exactly one standalone ASCII major.minor.patch token
// from bounded provider output.
func ParseVersion(output string) (Version, error) {
	if output == "" ||
		len(output) > maxVersionOutputBytes ||
		!utf8.ValidString(output) {
		return Version{}, errInvalidVersion
	}

	cores := findVersionCores(output)
	if len(cores) != 1 {
		return Version{}, errInvalidVersion
	}
	core := cores[0]
	if !standaloneVersionCore(output, core) ||
		len(core.major) > maxVersionComponentBytes ||
		len(core.minor) > maxVersionComponentBytes ||
		len(core.patch) > maxVersionComponentBytes {
		return Version{}, errInvalidVersion
	}

	major, err := strconv.ParseUint(core.major, 10, 64)
	if err != nil {
		return Version{}, errInvalidVersion
	}
	minor, err := strconv.ParseUint(core.minor, 10, 64)
	if err != nil {
		return Version{}, errInvalidVersion
	}
	patch, err := strconv.ParseUint(core.patch, 10, 64)
	if err != nil {
		return Version{}, errInvalidVersion
	}
	return Version{Major: major, Minor: minor, Patch: patch}, nil
}

type versionCore struct {
	start int
	end   int
	major string
	minor string
	patch string
}

func findVersionCores(output string) []versionCore {
	var cores []versionCore
	for start := 0; start < len(output); start++ {
		if !asciiDigit(output[start]) ||
			(start > 0 && asciiDigit(output[start-1])) {
			continue
		}

		majorEnd := consumeASCIIDigits(output, start)
		if majorEnd >= len(output) || output[majorEnd] != '.' {
			continue
		}
		minorStart := majorEnd + 1
		if minorStart >= len(output) || !asciiDigit(output[minorStart]) {
			continue
		}
		minorEnd := consumeASCIIDigits(output, minorStart)
		if minorEnd >= len(output) || output[minorEnd] != '.' {
			continue
		}
		patchStart := minorEnd + 1
		if patchStart >= len(output) || !asciiDigit(output[patchStart]) {
			continue
		}
		patchEnd := consumeASCIIDigits(output, patchStart)
		cores = append(cores, versionCore{
			start: start,
			end:   patchEnd,
			major: output[start:majorEnd],
			minor: output[minorStart:minorEnd],
			patch: output[patchStart:patchEnd],
		})
	}
	return cores
}

func standaloneVersionCore(output string, core versionCore) bool {
	if core.start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(output[:core.start])
		if versionIdentifierRune(previous) {
			return false
		}
	}
	if core.end < len(output) {
		next, _ := utf8.DecodeRuneInString(output[core.end:])
		if versionIdentifierRune(next) {
			return false
		}
	}
	return true
}

func versionIdentifierRune(value rune) bool {
	return unicode.IsLetter(value) ||
		unicode.IsNumber(value) ||
		unicode.IsMark(value) ||
		unicode.Is(unicode.Pc, value) ||
		unicode.Is(unicode.Cf, value) ||
		value == '_' ||
		value == '.' ||
		value == '-' ||
		value == '+'
}

func consumeASCIIDigits(value string, start int) int {
	end := start
	for end < len(value) && asciiDigit(value[end]) {
		end++
	}
	return end
}

func asciiDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

func compareVersion(left, right Version) int {
	switch {
	case left.Major < right.Major:
		return -1
	case left.Major > right.Major:
		return 1
	case left.Minor < right.Minor:
		return -1
	case left.Minor > right.Minor:
		return 1
	case left.Patch < right.Patch:
		return -1
	case left.Patch > right.Patch:
		return 1
	default:
		return 0
	}
}
