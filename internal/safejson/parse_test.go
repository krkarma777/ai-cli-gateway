package safejson

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

const plantedFragment = "PLANTED_REQUEST_FRAGMENT"

func TestParseCategories(t *testing.T) {
	t.Parallel()

	limits := Limits{MaxDepth: 4, MaxNumberBytes: 8}
	tests := []struct {
		name string
		raw  []byte
		want error
	}{
		{"object", []byte(`{"a":1}`), nil},
		{"number boundary", []byte(`{"n":12345678}`), nil},
		{"duplicate root", []byte(`{"a":1,"a":2}`), ErrDuplicate},
		{"duplicate nested", []byte(`{"a":{"x":1,"x":2}}`), ErrDuplicate},
		{"duplicate decoded key", []byte(`{"a":1,"\u0061":2}`), ErrDuplicate},
		{"trailing", []byte(`{"a":1} []`), ErrTrailing},
		{"trailing malformed", []byte(`{"a":1} ` + plantedFragment), ErrSyntax},
		{"bom", []byte("\xef\xbb\xbf{}"), ErrSyntax},
		{"raw nul", []byte("{\"a\":\"\x00\"}"), ErrNUL},
		{"escaped nul", []byte(`{"a":"\u0000"}`), ErrNUL},
		{"escaped nul key", []byte(`{"\u0000":"value"}`), ErrNUL},
		{"invalid utf8", []byte{'{', '"', 'a', '"', ':', '"', 0xff, '"', '}'}, ErrEncoding},
		{"malformed", []byte(`{"` + plantedFragment + `":`), ErrSyntax},
		{"empty", nil, ErrSyntax},
		{"root array", []byte(`[]`), ErrRootObject},
		{"root scalar", []byte(`true`), ErrRootObject},
		{"long number", []byte(`{"n":123456789}`), ErrLimit},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := Parse(tt.raw, limits)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Parse() error=%v, want sentinel %v", err, tt.want)
			}
			if tt.want == nil {
				if _, ok := got.(map[string]any); !ok {
					t.Fatalf("Parse() value type=%T, want map[string]any", got)
				}
				return
			}
			if got != nil {
				t.Fatalf("Parse() value=%v, want nil on error", got)
			}
			if strings.Contains(err.Error(), plantedFragment) {
				t.Fatalf("Parse() error exposed request fragment: %q", err)
			}
		})
	}
}

func TestParseDepthBoundary(t *testing.T) {
	t.Parallel()

	limits := Limits{MaxDepth: 4, MaxNumberBytes: 8}
	tests := []struct {
		name string
		raw  string
		want error
	}{
		{"depth four", objectWithNestedArrays(3), nil},
		{"depth five", objectWithNestedArrays(4), ErrLimit},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Parse([]byte(tt.raw), limits)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Parse() error=%v, want sentinel %v", err, tt.want)
			}
		})
	}
}

func TestParsePreservesIntegerBeyondMachineRange(t *testing.T) {
	t.Parallel()

	const lexical = "9223372036854775808"
	got, err := Parse(
		[]byte(`{"n":`+lexical+`}`),
		Limits{MaxDepth: 1, MaxNumberBytes: len(lexical)},
	)
	if err != nil {
		t.Fatal(err)
	}
	number, ok := got.(map[string]any)["n"].(json.Number)
	if !ok || number.String() != lexical {
		t.Fatalf("number=(%T, %v), want json.Number(%s)", number, number, lexical)
	}
}

func TestParseRejectsInvalidLimits(t *testing.T) {
	t.Parallel()

	for _, limits := range []Limits{
		{MaxDepth: 0, MaxNumberBytes: 8},
		{MaxDepth: 4, MaxNumberBytes: 0},
		{MaxDepth: -1, MaxNumberBytes: 8},
		{MaxDepth: 4, MaxNumberBytes: -1},
	} {
		if _, err := Parse([]byte(`{}`), limits); !errors.Is(err, ErrLimit) {
			t.Errorf("Parse(%+v) error=%v, want sentinel %v", limits, err, ErrLimit)
		}
	}
}

func objectWithNestedArrays(depth int) string {
	return `{"v":` + strings.Repeat("[", depth) +
		strings.Repeat("]", depth) + `}`
}
