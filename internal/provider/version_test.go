package provider

import (
	"errors"
	"strings"
	"testing"
)

func TestParseVersionAcceptsOneStandaloneNumericToken(t *testing.T) {
	tests := []struct {
		input string
		want  Version
	}{
		{"0.146.0", Version{Major: 0, Minor: 146, Patch: 0}},
		{"2.1.220", Version{Major: 2, Minor: 1, Patch: 220}},
		{"0.53.0", Version{Major: 0, Minor: 53, Patch: 0}},
		{
			"claude 2.1.220 (Claude Code)",
			Version{Major: 2, Minor: 1, Patch: 220},
		},
	}

	for _, test := range tests {
		t.Run(test.input, func(t *testing.T) {
			got, err := ParseVersion(test.input)
			if err != nil {
				t.Fatalf("ParseVersion() error: %v", err)
			}
			if got != test.want {
				t.Fatalf("ParseVersion()=%+v, want %+v", got, test.want)
			}
		})
	}
}

func TestParseVersionRejectsUnsafeOrAmbiguousOutputWithFixedError(t *testing.T) {
	const wantError = "provider version is invalid"
	tests := []string{
		"",
		"1.2",
		"1.2.",
		"1..2",
		"1.2.3.4",
		"1.2.3-beta",
		"1.2.3+build",
		"v1.2.3",
		"release_1.2.3",
		"1.2.3release",
		"-1.2.3",
		"+1.2.3",
		"１.２.３",
		"١.٢.٣",
		"1.2.3 4.5.6",
		"release 1.2.3 and 4.5.6",
		"release 1.2.3 4.5.6",
		"release 1.2.3,4.5.6",
		strings.Repeat("x", 4097) + " 1.2.3",
		strings.Repeat("9", 21) + ".2.3",
		"18446744073709551616.2.3",
		"1.18446744073709551616.3",
		"1.2.18446744073709551616",
		"planted-secret-without-version",
	}

	for _, input := range tests {
		t.Run(versionTestName(input), func(t *testing.T) {
			got, err := ParseVersion(input)
			if err == nil {
				t.Fatalf("ParseVersion(%q)=%+v, want error", input, got)
			}
			if err.Error() != wantError {
				t.Fatalf("ParseVersion(%q) error=%q, want %q", input, err, wantError)
			}
			if input != "" && strings.Contains(err.Error(), input) {
				t.Fatalf("error echoed version output %q", input)
			}
		})
	}
}

func TestParseVersionRejectsUnicodeIdentifierBoundaryRunes(t *testing.T) {
	tests := []string{
		"release\u203f1.2.3",
		"1.2.3\u203frelease",
		"release\u200d1.2.3",
		"1.2.3\u200drelease",
	}

	for _, input := range tests {
		t.Run(versionTestName(input), func(t *testing.T) {
			got, err := ParseVersion(input)
			if err == nil {
				t.Fatalf("ParseVersion(%q)=%+v, want error", input, got)
			}
			if !errors.Is(err, errInvalidVersion) {
				t.Fatalf("ParseVersion(%q) error=%v, want %v", input, err, errInvalidVersion)
			}
		})
	}
}

func TestRangeContainsUsesInclusiveMinExclusiveMax(t *testing.T) {
	tests := []struct {
		name     string
		version  Version
		contains bool
	}{
		{"codex below", Version{Major: 0, Minor: 145, Patch: 999}, false},
		{"codex minimum", Version{Major: 0, Minor: 146, Patch: 0}, true},
		{"codex middle", Version{Major: 0, Minor: 146, Patch: 999}, true},
		{"codex maximum", Version{Major: 0, Minor: 147, Patch: 0}, false},
		{"codex above", Version{Major: 1, Minor: 0, Patch: 0}, false},
		{"claude below", Version{Major: 2, Minor: 1, Patch: 204}, false},
		{"claude minimum", Version{Major: 2, Minor: 1, Patch: 205}, true},
		{"claude cross patch", Version{Major: 2, Minor: 1, Patch: 999}, true},
		{"claude maximum", Version{Major: 2, Minor: 2, Patch: 0}, false},
		{"gemini below", Version{Major: 0, Minor: 52, Patch: 999}, false},
		{"gemini minimum", Version{Major: 0, Minor: 53, Patch: 0}, true},
		{"gemini middle", Version{Major: 0, Minor: 53, Patch: 999}, true},
		{"gemini maximum", Version{Major: 0, Minor: 54, Patch: 0}, false},
	}
	ranges := map[string]Range{
		"codex": {
			MinInclusive: Version{Major: 0, Minor: 146, Patch: 0},
			MaxExclusive: Version{Major: 0, Minor: 147, Patch: 0},
		},
		"claude": {
			MinInclusive: Version{Major: 2, Minor: 1, Patch: 205},
			MaxExclusive: Version{Major: 2, Minor: 2, Patch: 0},
		},
		"gemini": {
			MinInclusive: Version{Major: 0, Minor: 53, Patch: 0},
			MaxExclusive: Version{Major: 0, Minor: 54, Patch: 0},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			providerName := strings.Fields(test.name)[0]
			if got := ranges[providerName].Contains(test.version); got != test.contains {
				t.Fatalf("Contains(%+v)=%t, want %t", test.version, got, test.contains)
			}
		})
	}
}

func TestRangeContainsHandlesZeroAndInvalidRanges(t *testing.T) {
	onePatch := Range{
		MinInclusive: Version{},
		MaxExclusive: Version{Patch: 1},
	}
	if !onePatch.Contains(Version{}) {
		t.Fatal("[0.0.0, 0.0.1) did not contain zero")
	}
	if onePatch.Contains(Version{Patch: 1}) {
		t.Fatal("[0.0.0, 0.0.1) contained exclusive maximum")
	}
	if (Range{}).Contains(Version{}) {
		t.Fatal("zero range contained a version")
	}
	if (Range{
		MinInclusive: Version{Major: 2},
		MaxExclusive: Version{Major: 1},
	}).Contains(Version{Major: 1, Minor: 5}) {
		t.Fatal("reversed range contained a version")
	}
}

func TestVersionStringIsCanonicalNumericForm(t *testing.T) {
	version := Version{
		Major: 18446744073709551615,
		Minor: 2,
		Patch: 3,
	}
	if got, want := version.String(), "18446744073709551615.2.3"; got != want {
		t.Fatalf("String()=%q, want %q", got, want)
	}
}

func versionTestName(input string) string {
	if len(input) > 80 {
		return input[:80]
	}
	if input == "" {
		return "empty"
	}
	return input
}
