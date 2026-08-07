package gatewaykey

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
)

const testKey = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestGenerateReadsExactly32BytesAndReturnsLowercaseHexWithLF(t *testing.T) {
	entropy := make([]byte, 33)
	for i := range entropy {
		entropy[i] = byte(i)
	}
	random := bytes.NewReader(entropy)

	got, err := Generate(random)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	const want = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f\n"
	if string(got) != want {
		t.Fatalf("Generate() = %q, want %q", got, want)
	}
	if random.Len() != 1 {
		t.Fatalf("Generate() consumed %d bytes, want 32", len(entropy)-random.Len())
	}
}

func TestGenerateFailuresReturnOnlyErrUnavailable(t *testing.T) {
	tests := []struct {
		name   string
		reader io.Reader
	}{
		{name: "nil reader", reader: nil},
		{name: "short entropy", reader: strings.NewReader("short")},
		{name: "reader failure", reader: errorReader{err: errors.New("entropy device detail")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Generate(tt.reader)
			if err != ErrUnavailable {
				t.Fatalf("Generate() error = %v, want sentinel", err)
			}
			if got != nil {
				t.Fatalf("Generate() output = %q, want nil", got)
			}
			if strings.Contains(err.Error(), "detail") {
				t.Fatalf("Generate() leaked underlying error: %q", err)
			}
		})
	}
}

func TestParseAcceptsExactLowercaseHexWithOptionalLineEnding(t *testing.T) {
	for _, suffix := range []string{"", "\n", "\r\n"} {
		t.Run(fmt.Sprintf("suffix_%q", suffix), func(t *testing.T) {
			snapshot, err := Parse(strings.NewReader(testKey + suffix))
			if err != nil {
				t.Fatalf("Parse() error = %v", err)
			}
			if !snapshot.Valid() {
				t.Fatal("Parse() snapshot is invalid")
			}
			if !snapshot.Enabled() {
				t.Fatal("Parse() snapshot is disabled")
			}
			if !snapshot.Matches(testKey) {
				t.Fatal("Parse() snapshot does not match the textual key")
			}
		})
	}
}

func TestParseRejectsMalformedInput(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{name: "empty", input: ""},
		{name: "short", input: testKey[:63]},
		{name: "long", input: testKey + "0"},
		{name: "uppercase", input: "A" + testKey[1:]},
		{name: "nonhex", input: "g" + testKey[1:]},
		{name: "leading space", input: " " + testKey[1:]},
		{name: "trailing space", input: testKey[:63] + " "},
		{name: "tab", input: testKey[:32] + "\t" + testKey[33:]},
		{name: "embedded NUL", input: testKey[:32] + "\x00" + testKey[33:]},
		{name: "final NUL", input: testKey[:63] + "\x00"},
		{name: "bare CR", input: testKey + "\r"},
		{name: "extra LF", input: testKey + "\n\n"},
		{name: "more than 66 bytes", input: testKey + "\r\n0"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := Parse(strings.NewReader(tt.input))
			if err != ErrUnavailable {
				t.Fatalf("Parse() error = %v, want sentinel", err)
			}
			if snapshot.Valid() || snapshot.Enabled() || snapshot.Matches(testKey) {
				t.Fatal("Parse() failure returned an authorizing snapshot")
			}
		})
	}
}

func TestParseFailsClosedForNilAndReaderErrors(t *testing.T) {
	tests := []struct {
		name   string
		reader io.Reader
	}{
		{name: "nil reader", reader: nil},
		{name: "reader failure", reader: errorReader{err: errors.New("filesystem detail")}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			snapshot, err := Parse(tt.reader)
			if err != ErrUnavailable {
				t.Fatalf("Parse() error = %v, want sentinel", err)
			}
			if strings.Contains(err.Error(), "detail") {
				t.Fatalf("Parse() leaked underlying error: %q", err)
			}
			if snapshot.Valid() || snapshot.Matches(testKey) {
				t.Fatal("Parse() failure returned an authorizing snapshot")
			}
		})
	}
}

func TestParseObservesAtMost67Bytes(t *testing.T) {
	reader := &countingReader{remaining: 1024}

	snapshot, err := Parse(reader)
	if err != ErrUnavailable {
		t.Fatalf("Parse() error = %v, want sentinel", err)
	}
	if snapshot.Valid() {
		t.Fatal("Parse() oversized input returned a valid snapshot")
	}
	if reader.read != 67 {
		t.Fatalf("Parse() observed %d bytes, want exactly 67 for oversized input", reader.read)
	}
}

func TestFromEnvironmentLooksUpOnceAndMatchesExactValue(t *testing.T) {
	calls := 0
	lookup := func(name string) (string, bool) {
		calls++
		if name != "GATEWAY_KEY" {
			t.Fatalf("lookup name = %q, want GATEWAY_KEY", name)
		}
		return "environment-secret", true
	}

	snapshot, err := FromEnvironment("GATEWAY_KEY", lookup)
	if err != nil {
		t.Fatalf("FromEnvironment() error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("lookup calls = %d, want 1", calls)
	}
	if !snapshot.Valid() || !snapshot.Enabled() {
		t.Fatal("FromEnvironment() did not return a valid enabled snapshot")
	}
	if !snapshot.Matches("environment-secret") {
		t.Fatal("snapshot does not match exact environment value")
	}
	if snapshot.Matches("Environment-secret") || snapshot.Matches("environment-secret\n") {
		t.Fatal("snapshot matched a different environment value")
	}
}

func TestFromEnvironmentRejectsUnavailableValuesAfterOneLookup(t *testing.T) {
	tests := []struct {
		name  string
		value string
		ok    bool
	}{
		{name: "missing", ok: false},
		{name: "empty", value: "", ok: true},
		{name: "NUL", value: "secret\x00suffix", ok: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			calls := 0
			lookup := func(string) (string, bool) {
				calls++
				return tt.value, tt.ok
			}

			snapshot, err := FromEnvironment("GATEWAY_KEY", lookup)
			if err != ErrUnavailable {
				t.Fatalf("FromEnvironment() error = %v, want sentinel", err)
			}
			if calls != 1 {
				t.Fatalf("lookup calls = %d, want 1", calls)
			}
			if snapshot.Valid() || snapshot.Enabled() || snapshot.Matches(tt.value) {
				t.Fatal("FromEnvironment() failure returned an authorizing snapshot")
			}
		})
	}
}

func TestFromEnvironmentNilLookupFailsClosedWithoutPanic(t *testing.T) {
	snapshot, err := FromEnvironment("GATEWAY_KEY", nil)
	if err != ErrUnavailable {
		t.Fatalf("FromEnvironment() error = %v, want sentinel", err)
	}
	if snapshot.Valid() || snapshot.Enabled() || snapshot.Matches("anything") {
		t.Fatal("FromEnvironment() nil lookup returned an authorizing snapshot")
	}
}

func TestSnapshotZeroValueIsInvalidAndFailsClosed(t *testing.T) {
	var snapshot Snapshot

	if snapshot.Valid() {
		t.Fatal("zero Snapshot.Valid() = true")
	}
	if snapshot.Enabled() {
		t.Fatal("zero Snapshot.Enabled() = true")
	}
	if snapshot.Matches("") || snapshot.Matches("anything") {
		t.Fatal("zero Snapshot.Matches() authorized a token")
	}
}

func TestSnapshotDisabledIsValidAndAuthorizes(t *testing.T) {
	snapshot := Disabled()

	if !snapshot.Valid() {
		t.Fatal("Disabled().Valid() = false")
	}
	if snapshot.Enabled() {
		t.Fatal("Disabled().Enabled() = true")
	}
	if !snapshot.Matches("") || !snapshot.Matches("anything") {
		t.Fatal("Disabled().Matches() did not authorize")
	}
}

func TestSnapshotFormattingDoesNotExposeSourceToken(t *testing.T) {
	const token = "format-secret-token-that-must-never-appear"
	snapshot, err := FromEnvironment("GATEWAY_KEY", func(string) (string, bool) {
		return token, true
	})
	if err != nil {
		t.Fatalf("FromEnvironment() error = %v", err)
	}

	formatted := fmt.Sprintf("%+v %#v", snapshot, snapshot)
	if strings.Contains(formatted, token) {
		t.Fatalf("formatted Snapshot exposed source token: %s", formatted)
	}
}

type errorReader struct {
	err error
}

func (r errorReader) Read([]byte) (int, error) {
	return 0, r.err
}

type countingReader struct {
	remaining int
	read      int
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.remaining == 0 {
		return 0, io.EOF
	}
	n := len(p)
	if n > r.remaining {
		n = r.remaining
	}
	for i := 0; i < n; i++ {
		p[i] = '0'
	}
	r.remaining -= n
	r.read += n
	return n, nil
}
