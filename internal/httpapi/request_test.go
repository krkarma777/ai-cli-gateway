package httpapi

import (
	"encoding/json"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

const requestFragment = "PLANTED_REQUEST_FRAGMENT"

func TestDefaultAndConstructedRequestLimits(t *testing.T) {
	t.Parallel()

	want := RequestLimits{
		InputBytes:        512 * 1024,
		InstructionsBytes: 256 * 1024,
		SchemaBytes:       32 * 1024,
		MaxDepth:          64,
		MaxNumberBytes:    128,
	}
	if got := DefaultRequestLimits(); got != want {
		t.Fatalf("DefaultRequestLimits()=%+v, want %+v", got, want)
	}

	got, err := NewRequestLimits(101, 102, 103)
	if err != nil {
		t.Fatal(err)
	}
	want.InputBytes = 101
	want.InstructionsBytes = 102
	want.SchemaBytes = 103
	if got != want {
		t.Fatalf("NewRequestLimits()=%+v, want %+v", got, want)
	}
}

func TestNewRequestLimitsRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		input        int
		instructions int
		schema       int
	}{
		{"zero input", 0, 1, 1},
		{"zero instructions", 1, 0, 1},
		{"zero schema", 1, 1, 0},
		{"negative input", -1, 1, 1},
		{"negative instructions", 1, -1, 1},
		{"negative schema", 1, 1, -1},
		{"combined overflow", math.MaxInt, 1, 1},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NewRequestLimits(tt.input, tt.instructions, tt.schema)
			if !errors.Is(err, ErrRequestLimits) {
				t.Fatalf("NewRequestLimits() error=%v, want sentinel %v", err, ErrRequestLimits)
			}
			if got != (RequestLimits{}) {
				t.Fatalf("NewRequestLimits()=%+v, want zero value on error", got)
			}
		})
	}
}

func TestDecodeRequestSubset(t *testing.T) {
	t.Parallel()

	raw := []byte(`{
	  "model":"codex-default",
	  "instructions":"be concise",
	  "input":"hello",
	  "text":{"format":{"type":"text"}},
	  "stream":false,
	  "store":false,
	  "tools":[],
	  "tool_choice":"none"
	}`)
	got, apiErr := DecodeRequest(raw, DefaultRequestLimits())
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if got.ModelAlias != "codex-default" || got.Input != "hello" ||
		got.Instructions == nil || *got.Instructions != "be concise" {
		t.Fatalf("request=%+v", got)
	}
	if got.Format.Type != core.FormatText {
		t.Fatalf("format=%+v, want text", got.Format)
	}
}

func TestDecodeRequestDefaultsAndNullInstructions(t *testing.T) {
	t.Parallel()

	got, apiErr := DecodeRequest(
		[]byte(`{"model":"m","input":"i","instructions":null}`),
		DefaultRequestLimits(),
	)
	if apiErr != nil {
		t.Fatal(apiErr)
	}
	if got.Format.Type != core.FormatText || got.Instructions != nil {
		t.Fatalf("request=%+v, want default text format and nil instructions", got)
	}
}

func TestDecodeRequestJSONSchemaDescriptionPresence(t *testing.T) {
	t.Parallel()

	schema := `{"type":"object","properties":{},"required":[],"additionalProperties":false}`
	tests := []struct {
		name            string
		descriptionJSON string
		wantPresent     bool
	}{
		{"absent", "", false},
		{"explicit empty", `,"description":""`, true},
	}

	var weights = make(map[string]int64)
	for _, tt := range tests {
		raw := []byte(`{"model":"m","input":"i","text":{"format":{` +
			`"type":"json_schema","name":"result","strict":true,"schema":` +
			schema + tt.descriptionJSON + `}}}`)
		got, apiErr := DecodeRequest(raw, DefaultRequestLimits())
		if apiErr != nil {
			t.Fatalf("%s: %v", tt.name, apiErr)
		}
		if got.Format.Type != core.FormatJSONSchema ||
			got.Format.Name != "result" ||
			(got.Format.Description != nil) != tt.wantPresent {
			t.Fatalf("%s: format=%+v", tt.name, got.Format)
		}
		if got.Format.Description != nil && *got.Format.Description != "" {
			t.Fatalf("%s: description=%q, want explicit empty", tt.name, *got.Format.Description)
		}

		var echoed map[string]any
		if err := json.Unmarshal(got.Format.Schema, &echoed); err != nil {
			t.Fatalf("%s: stored schema is not valid JSON: %v", tt.name, err)
		}
		if echoed["type"] != "object" {
			t.Fatalf("%s: stored schema=%s", tt.name, got.Format.Schema)
		}
		weights[tt.name] = got.Weight()
	}
	if weights["absent"] != weights["explicit empty"] {
		t.Fatalf("empty description changed byte weight: %v", weights)
	}
}

func TestDecodeRequestRejections(t *testing.T) {
	t.Parallel()

	validSchema := `{"type":"object"}`
	tests := []struct {
		name  string
		raw   []byte
		code  string
		param *string
	}{
		{"malformed", []byte(`{"model":"m","input":"` + requestFragment), core.CodeInvalidJSON, nil},
		{"duplicate", []byte(`{"model":"m","model":"` + requestFragment + `","input":"i"}`), core.CodeInvalidJSON, nil},
		{"trailing", []byte(`{"model":"m","input":"i"} []`), core.CodeInvalidJSON, nil},
		{"bom", []byte("\xef\xbb\xbf{}"), core.CodeInvalidJSON, nil},
		{"invalid utf8", []byte{'{', '"', 0xff, '"', '}'}, core.CodeInvalidRequest, nil},
		{"nul", []byte("{\"model\":\"m\",\"input\":\"\x00\"}"), core.CodeInvalidRequest, nil},
		{"escaped nul", []byte(`{"model":"m","input":"\u0000"}`), core.CodeInvalidRequest, nil},
		{"root array", []byte(`[]`), core.CodeInvalidRequest, nil},
		{"missing model", []byte(`{"input":"i"}`), core.CodeInvalidRequest, stringPtr("model")},
		{"empty model", []byte(`{"model":"","input":"i"}`), core.CodeInvalidRequest, stringPtr("model")},
		{"model wrong type", []byte(`{"model":1,"input":"i"}`), core.CodeInvalidRequest, stringPtr("model")},
		{"missing input", []byte(`{"model":"m"}`), core.CodeInvalidRequest, stringPtr("input")},
		{"empty input", []byte(`{"model":"m","input":""}`), core.CodeInvalidRequest, stringPtr("input")},
		{"input array", []byte(`{"model":"m","input":["` + requestFragment + `"]}`), core.CodeInvalidRequest, stringPtr("input")},
		{"instructions wrong type", []byte(`{"model":"m","input":"i","instructions":[]}`), core.CodeInvalidRequest, stringPtr("instructions")},
		{"unknown top level", []byte(`{"model":"m","input":"i","unknown":"` + requestFragment + `"}`), core.CodeUnsupportedParameter, nil},
		{"stream true", []byte(`{"model":"m","input":"i","stream":true}`), core.CodeUnsupportedParameter, stringPtr("stream")},
		{"stream wrong type", []byte(`{"model":"m","input":"i","stream":"` + requestFragment + `"}`), core.CodeInvalidRequest, stringPtr("stream")},
		{"store true", []byte(`{"model":"m","input":"i","store":true}`), core.CodeUnsupportedParameter, stringPtr("store")},
		{"store wrong type", []byte(`{"model":"m","input":"i","store":null}`), core.CodeInvalidRequest, stringPtr("store")},
		{"tools nonempty", []byte(`{"model":"m","input":"i","tools":["` + requestFragment + `"]}`), core.CodeUnsupportedParameter, stringPtr("tools")},
		{"tools wrong type", []byte(`{"model":"m","input":"i","tools":{}}`), core.CodeInvalidRequest, stringPtr("tools")},
		{"tool choice unsupported", []byte(`{"model":"m","input":"i","tool_choice":"` + requestFragment + `"}`), core.CodeUnsupportedParameter, stringPtr("tool_choice")},
		{"tool choice wrong type", []byte(`{"model":"m","input":"i","tool_choice":false}`), core.CodeInvalidRequest, stringPtr("tool_choice")},
		{"text wrong type", []byte(`{"model":"m","input":"i","text":[]}`), core.CodeInvalidRequest, stringPtr("text")},
		{"unknown text field", []byte(`{"model":"m","input":"i","text":{"format":{"type":"text"},"unknown":"` + requestFragment + `"}}`), core.CodeUnsupportedParameter, stringPtr("text")},
		{"missing format", []byte(`{"model":"m","input":"i","text":{}}`), core.CodeInvalidRequest, stringPtr("text.format")},
		{"format wrong type", []byte(`{"model":"m","input":"i","text":{"format":"` + requestFragment + `"}}`), core.CodeInvalidRequest, stringPtr("text.format")},
		{"missing format type", []byte(`{"model":"m","input":"i","text":{"format":{}}}`), core.CodeInvalidRequest, stringPtr("text.format.type")},
		{"format type wrong type", []byte(`{"model":"m","input":"i","text":{"format":{"type":false}}}`), core.CodeInvalidRequest, stringPtr("text.format.type")},
		{"format type empty", []byte(`{"model":"m","input":"i","text":{"format":{"type":""}}}`), core.CodeUnsupportedParameter, stringPtr("text.format.type")},
		{"format type unsupported", []byte(`{"model":"m","input":"i","text":{"format":{"type":"` + requestFragment + `"}}}`), core.CodeUnsupportedParameter, stringPtr("text.format.type")},
		{"text format extra", []byte(`{"model":"m","input":"i","text":{"format":{"type":"text","name":"` + requestFragment + `"}}}`), core.CodeUnsupportedParameter, stringPtr("text.format")},
		{"schema format extra", []byte(`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"n","strict":true,"schema":` + validSchema + `,"extra":"` + requestFragment + `"}}}`), core.CodeUnsupportedParameter, stringPtr("text.format")},
		{"schema name missing", []byte(`{"model":"m","input":"i","text":{"format":{"type":"json_schema","strict":true,"schema":` + validSchema + `}}}`), core.CodeInvalidRequest, stringPtr("text.format.name")},
		{"schema name empty", []byte(`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"","strict":true,"schema":` + validSchema + `}}}`), core.CodeUnsupportedParameter, stringPtr("text.format.name")},
		{"schema name invalid", []byte(`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"bad name","strict":true,"schema":` + validSchema + `}}}`), core.CodeUnsupportedParameter, stringPtr("text.format.name")},
		{"schema name too long", []byte(`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"` + strings.Repeat("a", 65) + `","strict":true,"schema":` + validSchema + `}}}`), core.CodeUnsupportedParameter, stringPtr("text.format.name")},
		{"strict missing", []byte(`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"n","schema":` + validSchema + `}}}`), core.CodeInvalidRequest, stringPtr("text.format.strict")},
		{"strict wrong type", []byte(`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"n","strict":"` + requestFragment + `","schema":` + validSchema + `}}}`), core.CodeInvalidRequest, stringPtr("text.format.strict")},
		{"strict false", []byte(`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"n","strict":false,"schema":` + validSchema + `}}}`), core.CodeUnsupportedParameter, stringPtr("text.format.strict")},
		{"schema missing", []byte(`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"n","strict":true}}}`), core.CodeInvalidRequest, stringPtr("text.format.schema")},
		{"schema wrong type", []byte(`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"n","strict":true,"schema":"` + requestFragment + `"}}}`), core.CodeInvalidRequest, stringPtr("text.format.schema")},
		{"description wrong type", []byte(`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"n","description":null,"strict":true,"schema":` + validSchema + `}}}`), core.CodeInvalidRequest, stringPtr("text.format.description")},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertDecodeError(t, tt.raw, DefaultRequestLimits(), tt.code, tt.param)
		})
	}
}

func TestDecodeRequestUsesClosedUnknownFieldParams(t *testing.T) {
	t.Parallel()

	assertDecodeError(
		t,
		[]byte(`{"model":"m","input":"i","zeta":1,"alpha":2}`),
		DefaultRequestLimits(),
		core.CodeUnsupportedParameter,
		nil,
	)
	assertDecodeError(
		t,
		[]byte(`{"model":"m","input":"i","text":{"alpha":1,"zeta":2}}`),
		DefaultRequestLimits(),
		core.CodeUnsupportedParameter,
		stringPtr("text"),
	)
}

func TestDecodeRequestRejectsUnknownKeysWithoutEchoingThem(t *testing.T) {
	t.Parallel()

	validSchema := `{"type":"object"}`
	tests := []struct {
		name  string
		raw   string
		param *string
	}{
		{
			"empty top level",
			`{"model":"m","input":"i","":1}`,
			nil,
		},
		{
			"empty text",
			`{"model":"m","input":"i","text":{"format":{"type":"text"},"":1}}`,
			stringPtr("text"),
		},
		{
			"empty text format",
			`{"model":"m","input":"i","text":{"format":{"type":"text","":1}}}`,
			stringPtr("text.format"),
		},
		{
			"empty schema format",
			`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"n","strict":true,"schema":` + validSchema + `,"":1}}}`,
			stringPtr("text.format"),
		},
		{
			"planted top level",
			`{"model":"m","input":"i","` + requestFragment + `":1}`,
			nil,
		},
		{
			"planted text",
			`{"model":"m","input":"i","text":{"format":{"type":"text"},"` + requestFragment + `":1}}`,
			stringPtr("text"),
		},
		{
			"planted text format",
			`{"model":"m","input":"i","text":{"format":{"type":"text","` + requestFragment + `":1}}}`,
			stringPtr("text.format"),
		},
		{
			"planted schema format",
			`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"n","strict":true,"schema":` + validSchema + `,"` + requestFragment + `":1}}}`,
			stringPtr("text.format"),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertDecodeError(
				t,
				[]byte(tt.raw),
				DefaultRequestLimits(),
				core.CodeUnsupportedParameter,
				tt.param,
			)
		})
	}
}

func TestDecodeRequestByteLimits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		raw    []byte
		limits RequestLimits
		param  string
	}{
		{
			"input",
			[]byte(`{"model":"m","input":"é"}`),
			withByteLimits(1, 10, 100),
			"input",
		},
		{
			"instructions",
			[]byte(`{"model":"m","input":"i","instructions":"é"}`),
			withByteLimits(10, 1, 100),
			"instructions",
		},
		{
			"schema",
			[]byte(`{"model":"m","input":"i","text":{"format":{"type":"json_schema","name":"n","strict":true,"schema":{"x":"é"}}}}`),
			withByteLimits(10, 10, len([]byte(`{"x":"é"}`))-1),
			"text.format.schema",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertDecodeError(
				t,
				tt.raw,
				tt.limits,
				core.CodeRequestTooLarge,
				stringPtr(tt.param),
			)
		})
	}
}

func TestDecodeRequestAcceptsExactByteLimits(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"model":"m","input":"é","instructions":"é","text":{"format":{"type":"json_schema","name":"n","strict":true,"schema":{"x":"é"}}}}`)
	limits := withByteLimits(
		len([]byte("é")),
		len([]byte("é")),
		len([]byte(`{"x":"é"}`)),
	)
	if _, apiErr := DecodeRequest(raw, limits); apiErr != nil {
		t.Fatal(apiErr)
	}
}

func TestDecodeRequestMapsParserLimitsAndInvalidLimitSets(t *testing.T) {
	t.Parallel()

	depthLimits := DefaultRequestLimits()
	depthLimits.MaxDepth = 2
	assertDecodeError(
		t,
		[]byte(`{"model":"m","input":"i","tools":[[]]}`),
		depthLimits,
		core.CodeInvalidRequest,
		nil,
	)

	numberLimits := DefaultRequestLimits()
	numberLimits.MaxNumberBytes = 3
	assertDecodeError(
		t,
		[]byte(`{"model":"m","input":"i","unknown":1234}`),
		numberLimits,
		core.CodeInvalidRequest,
		nil,
	)

	for _, mutate := range []func(*RequestLimits){
		func(limits *RequestLimits) { limits.InputBytes = 0 },
		func(limits *RequestLimits) { limits.InstructionsBytes = 0 },
		func(limits *RequestLimits) { limits.SchemaBytes = 0 },
		func(limits *RequestLimits) { limits.MaxDepth = 0 },
		func(limits *RequestLimits) { limits.MaxNumberBytes = 0 },
	} {
		invalidLimits := DefaultRequestLimits()
		mutate(&invalidLimits)
		assertDecodeError(
			t,
			[]byte(`{"model":"m","input":"i"}`),
			invalidLimits,
			core.CodeInvalidRequest,
			nil,
		)
	}

	overflowing := DefaultRequestLimits()
	overflowing.InputBytes = math.MaxInt
	assertDecodeError(
		t,
		[]byte(`{"model":"m","input":"i"}`),
		overflowing,
		core.CodeInvalidRequest,
		nil,
	)
}

func assertDecodeError(
	t *testing.T,
	raw []byte,
	limits RequestLimits,
	wantCode string,
	wantParam *string,
) {
	t.Helper()

	got, apiErr := DecodeRequest(raw, limits)
	if apiErr == nil {
		t.Fatalf("DecodeRequest()=(%+v, nil), want error", got)
	}
	if !reflect.DeepEqual(got, core.Request{}) {
		t.Fatalf("DecodeRequest() request=%+v, want zero value on error", got)
	}
	wantStatus := 400
	if wantCode == core.CodeRequestTooLarge {
		wantStatus = 413
	}
	if apiErr.StatusCode() != wantStatus || apiErr.CodeValue() != wantCode {
		t.Fatalf(
			"error status/code=(%d,%q), want (%d,%q)",
			apiErr.StatusCode(),
			apiErr.CodeValue(),
			wantStatus,
			wantCode,
		)
	}
	gotParam := apiErr.ParamValue()
	if !equalStringPointers(gotParam, wantParam) {
		t.Fatalf("error param=%v, want %v", pointerText(gotParam), pointerText(wantParam))
	}
	if strings.Contains(apiErr.Error(), requestFragment) ||
		strings.Contains(apiErr.MessageText(), requestFragment) {
		t.Fatalf("error exposed request fragment: %q", apiErr)
	}
	if gotParam != nil && strings.Contains(*gotParam, requestFragment) {
		t.Fatalf("error param exposed request fragment: %q", *gotParam)
	}
}

func withByteLimits(input, instructions, schema int) RequestLimits {
	limits := DefaultRequestLimits()
	limits.InputBytes = input
	limits.InstructionsBytes = instructions
	limits.SchemaBytes = schema
	return limits
}

func stringPtr(value string) *string {
	return &value
}

func equalStringPointers(left, right *string) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}

func pointerText(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
