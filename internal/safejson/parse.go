// Package safejson parses JSON without exposing input-derived decoder errors.
package safejson

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"unicode/utf8"
)

// Limits bounds JSON container depth and numeric token length.
type Limits struct {
	MaxDepth       int
	MaxNumberBytes int
}

// Closed parser error categories.
var (
	ErrEncoding   = errors.New("invalid string encoding")
	ErrNUL        = errors.New("NUL is not allowed")
	ErrSyntax     = errors.New("invalid JSON syntax")
	ErrDuplicate  = errors.New("duplicate object key")
	ErrTrailing   = errors.New("trailing JSON data")
	ErrRootObject = errors.New("root must be an object")
	ErrLimit      = errors.New("JSON limit exceeded")
)

// Parse decodes exactly one duplicate-free JSON object within the supplied
// limits. It returns only closed error categories that contain no input text.
func Parse(data []byte, limits Limits) (any, error) {
	if !utf8.Valid(data) {
		return nil, ErrEncoding
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return nil, ErrNUL
	}
	if bytes.HasPrefix(data, []byte{0xef, 0xbb, 0xbf}) {
		return nil, ErrSyntax
	}
	if limits.MaxDepth <= 0 || limits.MaxNumberBytes <= 0 {
		return nil, ErrLimit
	}

	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()

	value, err := readValue(dec, 0, limits)
	if err != nil {
		return nil, err
	}
	if _, ok := value.(map[string]any); !ok {
		return nil, ErrRootObject
	}
	if _, err := dec.Token(); err != io.EOF {
		if err == nil {
			return nil, ErrTrailing
		}
		return nil, ErrSyntax
	}
	return value, nil
}

func readValue(dec *json.Decoder, depth int, limits Limits) (any, error) {
	token, err := dec.Token()
	if err != nil {
		return nil, ErrSyntax
	}

	switch value := token.(type) {
	case json.Delim:
		if depth >= limits.MaxDepth {
			return nil, ErrLimit
		}
		switch value {
		case '{':
			return readObject(dec, depth+1, limits)
		case '[':
			return readArray(dec, depth+1, limits)
		default:
			return nil, ErrSyntax
		}
	case string:
		if strings.IndexByte(value, 0) >= 0 {
			return nil, ErrNUL
		}
		return value, nil
	case json.Number:
		if len(value.String()) > limits.MaxNumberBytes {
			return nil, ErrLimit
		}
		return value, nil
	case bool, nil:
		return value, nil
	default:
		return nil, ErrSyntax
	}
}

func readObject(
	dec *json.Decoder,
	depth int,
	limits Limits,
) (map[string]any, error) {
	object := make(map[string]any)
	keys := make(map[string]struct{})

	for dec.More() {
		token, err := dec.Token()
		if err != nil {
			return nil, ErrSyntax
		}
		key, ok := token.(string)
		if !ok {
			return nil, ErrSyntax
		}
		if strings.IndexByte(key, 0) >= 0 {
			return nil, ErrNUL
		}
		if _, exists := keys[key]; exists {
			return nil, ErrDuplicate
		}
		keys[key] = struct{}{}

		value, err := readValue(dec, depth, limits)
		if err != nil {
			return nil, err
		}
		object[key] = value
	}
	if err := consumeClosing(dec, '}'); err != nil {
		return nil, err
	}
	return object, nil
}

func readArray(dec *json.Decoder, depth int, limits Limits) ([]any, error) {
	array := make([]any, 0)
	for dec.More() {
		value, err := readValue(dec, depth, limits)
		if err != nil {
			return nil, err
		}
		array = append(array, value)
	}
	if err := consumeClosing(dec, ']'); err != nil {
		return nil, err
	}
	return array, nil
}

func consumeClosing(dec *json.Decoder, want json.Delim) error {
	token, err := dec.Token()
	if err != nil {
		return ErrSyntax
	}
	got, ok := token.(json.Delim)
	if !ok || got != want {
		return ErrSyntax
	}
	return nil
}
