package schema

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/httpapi"
)

const (
	testSchemaBytes = 32 * 1024
	testOutputBytes = 1 << 20
	plantedSchema   = "PLANTED_SCHEMA_FRAGMENT"
	plantedOutput   = "PLANTED_OUTPUT_FRAGMENT"
)

func TestDefaultLimitsAndValidation(t *testing.T) {
	t.Parallel()

	got, err := DefaultLimits(101, 202)
	if err != nil {
		t.Fatal(err)
	}
	want := Limits{
		SchemaBytes:   101,
		MaxNodes:      512,
		MaxDepth:      32,
		MaxProperties: 100,
		MaxEnum:       256,
		OutputBytes:   202,
		OutputDepth:   128,
		NumberBytes:   128,
	}
	if got != want {
		t.Fatalf("DefaultLimits()=%+v, want %+v", got, want)
	}

	for _, tt := range []struct {
		name        string
		schemaBytes int
		outputBytes int
	}{
		{"zero schema bytes", 0, 1},
		{"negative schema bytes", -1, 1},
		{"zero output bytes", 1, 0},
		{"negative output bytes", 1, -1},
		{"combined overflow", math.MaxInt, 1},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			limits, err := DefaultLimits(tt.schemaBytes, tt.outputBytes)
			if !errors.Is(err, ErrLimits) {
				t.Fatalf("DefaultLimits() error=%v, want sentinel %v", err, ErrLimits)
			}
			if limits != (Limits{}) {
				t.Fatalf("DefaultLimits()=%+v, want zero value", limits)
			}
		})
	}
}

func TestCompileAcceptsBriefSchema(t *testing.T) {
	t.Parallel()

	valid := core.OutputFormat{
		Type: core.FormatJSONSchema,
		Name: "result",
		Schema: json.RawMessage(`{
		  "type":"object",
		  "properties":{
		    "name":{"type":"string","minLength":1,"maxLength":20},
		    "count":{"type":"integer","minimum":0,"maximum":10}
		  },
		  "required":["name","count"],
		  "additionalProperties":false
		}`),
	}
	if _, err := Compile(valid, defaultTestLimits(t)); err != nil {
		t.Fatalf("Compile() error=%v", err)
	}
}

func TestCompileAcceptsCompletePortableKeywordProfile(t *testing.T) {
	t.Parallel()

	format := formatForSchema(`{
	  "type":"object",
	  "title":"Result",
	  "description":"Portable result",
	  "properties":{
	    "name":{
	      "type":"string",
	      "minLength":1,
	      "maxLength":20,
	      "enum":["Ada","Grace"]
	    },
	    "count":{
	      "type":"integer",
	      "minimum":0,
	      "maximum":10,
	      "exclusiveMinimum":-1,
	      "exclusiveMaximum":11
	    },
	    "ratio":{"type":"number","const":0.5},
	    "tags":{
	      "type":"array",
	      "items":{"type":"string"},
	      "minItems":0,
	      "maxItems":3
	    },
	    "metadata":{
	      "type":"object",
	      "properties":{"enabled":{"type":"boolean"}},
	      "required":["enabled"],
	      "additionalProperties":false,
	      "minProperties":1,
	      "maxProperties":1
	    },
	    "nothing":{"type":"null"}
	  },
	  "required":["name","count","ratio","tags","metadata","nothing"],
	  "additionalProperties":false,
	  "minProperties":6,
	  "maxProperties":6
	}`)
	if _, err := Compile(format, defaultTestLimits(t)); err != nil {
		t.Fatalf("Compile() error=%v", err)
	}
}

func TestCompileDistinguishesSchemaKeywordsFromDataKeys(t *testing.T) {
	t.Parallel()

	format := formatForSchema(`{
	  "type":"object",
	  "properties":{
	    "$ref":{
	      "enum":[{"$defs":{"anyOf":true},"unknown":1}],
	      "const":{"format":"literal","properties":{"pattern":"literal"}}
	    }
	  },
	  "required":["$ref"],
	  "additionalProperties":false
	}`)
	if _, err := Compile(format, defaultTestLimits(t)); err != nil {
		t.Fatalf("Compile() treated a property or literal key as a keyword: %v", err)
	}
}

func TestCompileRejectsUnsupportedKeywordsAndRootShape(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		schema string
	}{
		{"reference", rootWith(`"$ref":"https://` + plantedSchema + `/schema"`)},
		{"definitions", rootWith(`"$defs":{}`)},
		{"any of", rootWith(`"anyOf":[]`)},
		{"one of", rootWith(`"oneOf":[]`)},
		{"all of", rootWith(`"allOf":[]`)},
		{"pattern", rootWith(`"pattern":"x"`)},
		{"format", rootWith(`"format":"email"`)},
		{"unique items", rootWith(`"uniqueItems":true`)},
		{
			"nested unsupported keyword",
			`{"type":"object","properties":{"v":{"type":"string","pattern":"x"}},"required":["v"],"additionalProperties":false}`,
		},
		{
			"missing root type",
			`{"properties":{},"required":[],"additionalProperties":false}`,
		},
		{
			"unknown type",
			`{"type":"record","properties":{},"required":[],"additionalProperties":false}`,
		},
		{
			"type array",
			`{"type":["object","null"],"properties":{},"required":[],"additionalProperties":false}`,
		},
		{"non-object root", `{"type":"string"}`},
		{
			"missing additional properties",
			`{"type":"object","properties":{},"required":[]}`,
		},
		{
			"additional properties true",
			`{"type":"object","properties":{},"required":[],"additionalProperties":true}`,
		},
		{
			"optional property",
			`{"type":"object","properties":{"v":{"type":"string"}},"required":[],"additionalProperties":false}`,
		},
		{
			"required property not declared",
			`{"type":"object","properties":{},"required":["v"],"additionalProperties":false}`,
		},
		{
			"duplicate required property",
			`{"type":"object","properties":{"v":{"type":"string"}},"required":["v","v"],"additionalProperties":false}`,
		},
		{
			"tuple items",
			`{"type":"object","properties":{"v":{"type":"array","items":[{"type":"string"}]}},"required":["v"],"additionalProperties":false}`,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if compiled, err := Compile(
				formatForSchema(tt.schema),
				defaultTestLimits(t),
			); !errors.Is(err, ErrInvalidSchema) || compiled != nil {
				t.Fatalf(
					"Compile()=(%v, %v), want (nil, %v)",
					compiled,
					err,
					ErrInvalidSchema,
				)
			}
		})
	}
}

func TestCompileRejectsWrongKeywordTypesAndInvalidBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		schema string
	}{
		{"type number", `{"type":1}`},
		{
			"properties array",
			`{"type":"object","properties":[],"required":[],"additionalProperties":false}`,
		},
		{
			"required object",
			`{"type":"object","properties":{},"required":{},"additionalProperties":false}`,
		},
		{
			"required non-string",
			`{"type":"object","properties":{},"required":[1],"additionalProperties":false}`,
		},
		{
			"additional properties string",
			`{"type":"object","properties":{},"required":[],"additionalProperties":"false"}`,
		},
		{"items string", rootWith(`"items":"schema"`)},
		{"enum object", rootWith(`"enum":{}`)},
		{"min length string", rootWith(`"minLength":"1"`)},
		{"max length fraction", rootWith(`"maxLength":1.5`)},
		{"min items negative", rootWith(`"minItems":-1`)},
		{"max items boolean", rootWith(`"maxItems":true`)},
		{"min properties fraction", rootWith(`"minProperties":1.1`)},
		{"max properties string", rootWith(`"maxProperties":"1"`)},
		{"minimum string", rootWith(`"minimum":"0"`)},
		{"maximum boolean", rootWith(`"maximum":false`)},
		{"exclusive minimum null", rootWith(`"exclusiveMinimum":null`)},
		{"exclusive maximum string", rootWith(`"exclusiveMaximum":"1"`)},
		{"description number", rootWith(`"description":1`)},
		{"title boolean", rootWith(`"title":false`)},
		{"reversed string bounds", rootWith(`"minLength":2,"maxLength":1`)},
		{"reversed item bounds", rootWith(`"minItems":2,"maxItems":1`)},
		{
			"reversed property bounds",
			rootWith(`"minProperties":2,"maxProperties":1`),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := Compile(
				formatForSchema(tt.schema),
				defaultTestLimits(t),
			); !errors.Is(err, ErrInvalidSchema) {
				t.Fatalf("Compile() error=%v, want sentinel %v", err, ErrInvalidSchema)
			}
		})
	}
}

func TestCompileEnforcesExactComplexityBoundaries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		schema  string
		wantErr error
	}{
		{
			"512 nodes",
			schemaWithConstEntries(506),
			nil,
		},
		{
			"513 nodes",
			schemaWithConstEntries(507),
			ErrInvalidSchema,
		},
		{
			"depth 32",
			schemaWithConstDepth(31),
			nil,
		},
		{
			"depth 33",
			schemaWithConstDepth(32),
			ErrInvalidSchema,
		},
		{
			"100 properties",
			schemaWithProperties(100),
			nil,
		},
		{
			"101 properties",
			schemaWithProperties(101),
			ErrInvalidSchema,
		},
		{
			"256 enum entries",
			schemaWithEnumEntries(256),
			nil,
		},
		{
			"257 enum entries",
			schemaWithEnumEntries(257),
			ErrInvalidSchema,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			compiled, err := Compile(
				formatForSchema(tt.schema),
				defaultTestLimits(t),
			)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Compile() error=%v, want %v", err, tt.wantErr)
			}
			if (compiled == nil) != (tt.wantErr != nil) {
				t.Fatalf("Compile() compiled=%v with error=%v", compiled, err)
			}
		})
	}
}

func TestCompileOrdersNumericBoundsExactly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		lower   string
		upper   string
		wantErr error
	}{
		{
			"integers beyond float precision",
			"9007199254740992",
			"9007199254740993",
			nil,
		},
		{
			"reversed integers beyond float precision",
			"9007199254740993",
			"9007199254740992",
			ErrInvalidSchema,
		},
		{
			"equivalent exponent and decimal",
			"9.007199254740992e15",
			"9007199254740992",
			nil,
		},
		{
			"adjacent exponent above decimal",
			"9.007199254740993e15",
			"9007199254740992",
			ErrInvalidSchema,
		},
		{
			"positive fractions",
			"0.10000000000000001",
			"0.10000000000000002",
			nil,
		},
		{
			"reversed positive fractions",
			"0.10000000000000002",
			"0.10000000000000001",
			ErrInvalidSchema,
		},
		{
			"negative fractions",
			"-0.10000000000000002",
			"-0.10000000000000001",
			nil,
		},
		{
			"reversed negative fractions",
			"-0.10000000000000001",
			"-0.10000000000000002",
			ErrInvalidSchema,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			schema := schemaWithProperty(
				`"type":"number","minimum":` + tt.lower +
					`,"maximum":` + tt.upper,
			)
			_, err := Compile(formatForSchema(schema), defaultTestLimits(t))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Compile() error=%v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestCompileRejectsReversedExclusiveAndMixedNumericBounds(t *testing.T) {
	t.Parallel()

	equivalent := schemaWithProperty(
		`"type":"number",` +
			`"exclusiveMinimum":1e3,"exclusiveMaximum":1000`,
	)
	if _, err := Compile(
		formatForSchema(equivalent),
		defaultTestLimits(t),
	); err != nil {
		t.Fatalf("Compile(equivalent exclusive bounds) error=%v", err)
	}

	for _, fragment := range []string{
		`"type":"number","exclusiveMinimum":2,"exclusiveMaximum":1`,
		`"type":"number","minimum":2,"exclusiveMaximum":1`,
		`"type":"number","exclusiveMinimum":2,"maximum":1`,
	} {
		schema := schemaWithProperty(fragment)
		if _, err := Compile(
			formatForSchema(schema),
			defaultTestLimits(t),
		); !errors.Is(err, ErrInvalidSchema) {
			t.Fatalf("Compile(%s) error=%v, want %v", fragment, err, ErrInvalidSchema)
		}
	}
}

func TestCompileRejectsExponentAmplificationBeforeRationalConversion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		schema string
	}{
		{
			"bound at dependency expansion limit",
			schemaWithProperty(`"type":"number","minimum":1e1000000`),
		},
		{
			"bound above dependency expansion limit",
			schemaWithProperty(`"type":"number","maximum":1e1000001`),
		},
		{
			"enum literal",
			schemaWithProperty(`"enum":[1e1000000]`),
		},
		{
			"nested const literal",
			schemaWithProperty(`"const":{"values":[1e1000000]}`),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			compiled, err := Compile(
				formatForSchema(tt.schema),
				defaultTestLimits(t),
			)
			if !errors.Is(err, ErrInvalidSchema) || compiled != nil {
				t.Fatalf(
					"Compile()=(%v, %v), want (nil, %v)",
					compiled,
					err,
					ErrInvalidSchema,
				)
			}
		})
	}
}

func TestCompileEnforcesEffectiveDecimalMagnitudeAndScaleBudget(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		number  string
		wantErr error
	}{
		{"128 digit positive magnitude", "1e127", nil},
		{"129 digit positive magnitude", "1e128", ErrInvalidSchema},
		{"128 digit negative scale", "1e-128", nil},
		{"129 digit negative scale", "1e-129", ErrInvalidSchema},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := Compile(
				formatForSchema(schemaWithProperty(
					`"type":"number","minimum":`+tt.number,
				)),
				defaultTestLimits(t),
			)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Compile() error=%v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateRejectsExponentPanicInputInSubprocess(t *testing.T) {
	if os.Getenv("SCHEMA_EXPONENT_PANIC_HELPER") == "1" {
		compiled := mustCompile(
			t,
			formatForSchema(schemaWithProperty(
				`"type":"number","minimum":0`,
			)),
			defaultTestLimits(t),
		)
		got, err := compiled.Validate([]byte(`{"value":1e1000001}`))
		if !errors.Is(err, ErrInvalidOutput) || got != "" {
			t.Fatalf(
				"Validate()=(%q, %v), want empty and %v",
				got,
				err,
				ErrInvalidOutput,
			)
		}
		return
	}

	runSchemaTestSubprocess(t, "SCHEMA_EXPONENT_PANIC_HELPER")
}

func TestValidateRejectsAdjacentLargeExponentInSubprocess(t *testing.T) {
	if os.Getenv("SCHEMA_EXPONENT_ADJACENT_HELPER") == "1" {
		compiled := mustCompile(
			t,
			permissiveValueFormat(),
			defaultTestLimits(t),
		)
		got, err := compiled.Validate([]byte(`{"value":1e1000000}`))
		if !errors.Is(err, ErrInvalidOutput) || got != "" {
			t.Fatalf(
				"Validate()=(%q, %v), want empty and %v",
				got,
				err,
				ErrInvalidOutput,
			)
		}
		return
	}

	runSchemaTestSubprocess(t, "SCHEMA_EXPONENT_ADJACENT_HELPER")
}

func TestConfiguredNumberBytesControlsEffectiveDecimalBudget(t *testing.T) {
	t.Parallel()

	limits := defaultTestLimits(t)
	limits.NumberBytes = 8

	if _, err := Compile(
		formatForSchema(schemaWithProperty(
			`"type":"number","minimum":1e7`,
		)),
		limits,
	); err != nil {
		t.Fatalf("Compile(8 digit magnitude) error=%v", err)
	}
	if _, err := Compile(
		formatForSchema(schemaWithProperty(
			`"type":"number","minimum":1e8`,
		)),
		limits,
	); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("Compile(9 digit magnitude) error=%v, want %v", err, ErrInvalidSchema)
	}

	compiled := mustCompile(t, permissiveValueFormat(), limits)
	if got, err := compiled.Validate(
		[]byte(`{"value":1e7}`),
	); err != nil || got != `{"value":1e7}` {
		t.Fatalf("Validate(8 digit magnitude)=(%q, %v)", got, err)
	}
	if got, err := compiled.Validate(
		[]byte(`{"value":1e8}`),
	); !errors.Is(err, ErrInvalidOutput) || got != "" {
		t.Fatalf(
			"Validate(9 digit magnitude)=(%q, %v), want empty and %v",
			got,
			err,
			ErrInvalidOutput,
		)
	}
}

func TestValidateRejectsRepeatedLargeExponentsInSubprocess(t *testing.T) {
	if os.Getenv("SCHEMA_EXPONENT_REPEATED_HELPER") == "1" {
		compiled := mustCompile(
			t,
			formatForSchema(schemaWithProperty(
				`"type":"array","items":{"type":"number","minimum":0}`,
			)),
			defaultTestLimits(t),
		)
		numbers := strings.TrimSuffix(strings.Repeat("1e1000000,", 64), ",")
		got, err := compiled.Validate([]byte(`{"value":[` + numbers + `]}`))
		if !errors.Is(err, ErrInvalidOutput) || got != "" {
			t.Fatalf(
				"Validate()=(%q, %v), want empty and %v",
				got,
				err,
				ErrInvalidOutput,
			)
		}
		return
	}

	runSchemaTestSubprocess(t, "SCHEMA_EXPONENT_REPEATED_HELPER")
}

func TestValidateEnforcesEffectiveDecimalMagnitudeAndScaleBudget(t *testing.T) {
	t.Parallel()

	compiled := mustCompile(
		t,
		permissiveValueFormat(),
		defaultTestLimits(t),
	)
	tests := []struct {
		name    string
		number  string
		wantErr error
	}{
		{"128 digit positive magnitude", "1e127", nil},
		{"129 digit positive magnitude", "1e128", ErrInvalidOutput},
		{"128 digit negative scale", "1e-128", nil},
		{"129 digit negative scale", "1e-129", ErrInvalidOutput},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw := `{"value":` + tt.number + `}`
			got, err := compiled.Validate([]byte(raw))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() error=%v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && got != raw {
				t.Fatalf("Validate()=%q, want %q", got, raw)
			}
			if tt.wantErr != nil && got != "" {
				t.Fatalf("Validate()=%q, want empty string", got)
			}
		})
	}
}

func TestSchemaBytesCapControlsRequestDecodingAndCompilation(t *testing.T) {
	t.Parallel()

	schemaText := string(validFormat().Schema)
	request := []byte(
		`{"model":"m","input":"i","text":{"format":{` +
			`"type":"json_schema","name":"result","strict":true,"schema":` +
			schemaText + `}}}`,
	)

	smallRequestLimits := httpapi.DefaultRequestLimits()
	smallRequestLimits.SchemaBytes = len(schemaText) - 1
	if _, apiErr := httpapi.DecodeRequest(request, smallRequestLimits); apiErr == nil {
		t.Fatal("DecodeRequest() accepted a schema above configured SchemaBytes")
	}

	smallCompileLimits, err := DefaultLimits(len(schemaText)-1, testOutputBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(
		validFormat(),
		smallCompileLimits,
	); !errors.Is(err, ErrInvalidSchema) {
		t.Fatalf("Compile() error=%v, want %v", err, ErrInvalidSchema)
	}

	largeRequestLimits := httpapi.DefaultRequestLimits()
	largeRequestLimits.SchemaBytes = len(schemaText)
	decoded, apiErr := httpapi.DecodeRequest(request, largeRequestLimits)
	if apiErr != nil {
		t.Fatalf("DecodeRequest() error=%v", apiErr)
	}
	if len(decoded.Format.Schema) != len(schemaText) {
		t.Fatalf(
			"decoded schema bytes=%d, want %d",
			len(decoded.Format.Schema),
			len(schemaText),
		)
	}

	largeCompileLimits, err := DefaultLimits(len(schemaText), testOutputBytes)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Compile(decoded.Format, largeCompileLimits); err != nil {
		t.Fatalf("Compile() under configured cap error=%v", err)
	}
}

func TestCompileRejectsEveryInvalidLimitField(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name   string
		mutate func(*Limits)
	}{
		{"schema bytes", func(l *Limits) { l.SchemaBytes = 0 }},
		{"nodes", func(l *Limits) { l.MaxNodes = 0 }},
		{"depth", func(l *Limits) { l.MaxDepth = 0 }},
		{"properties", func(l *Limits) { l.MaxProperties = 0 }},
		{"enum", func(l *Limits) { l.MaxEnum = 0 }},
		{"output bytes", func(l *Limits) { l.OutputBytes = 0 }},
		{"output depth", func(l *Limits) { l.OutputDepth = 0 }},
		{"number bytes", func(l *Limits) { l.NumberBytes = 0 }},
		{"aggregate overflow", func(l *Limits) { l.SchemaBytes = math.MaxInt }},
	} {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			limits := defaultTestLimits(t)
			tt.mutate(&limits)
			if compiled, err := Compile(
				validFormat(),
				limits,
			); !errors.Is(err, ErrLimits) || compiled != nil {
				t.Fatalf("Compile()=(%v, %v), want (nil, %v)", compiled, err, ErrLimits)
			}
		})
	}
}

func TestCompileReturnsClosedErrorsWithoutSchemaLeakage(t *testing.T) {
	t.Parallel()

	for _, format := range []core.OutputFormat{
		formatForSchema(rootWith(`"` + plantedSchema + `":true`)),
		formatForSchema(`{"type":"object","description":"` + plantedSchema + `"`),
		{
			Type:   core.FormatJSONSchema,
			Name:   "result",
			Schema: json.RawMessage(`{"` + plantedSchema),
		},
	} {
		compiled, err := Compile(format, defaultTestLimits(t))
		if !errors.Is(err, ErrInvalidSchema) || compiled != nil {
			t.Fatalf("Compile()=(%v, %v), want (nil, %v)", compiled, err, ErrInvalidSchema)
		}
		if strings.Contains(err.Error(), plantedSchema) {
			t.Fatalf("Compile() exposed schema fragment in error: %q", err)
		}
	}
}

func TestValidateReturnsExactValidJSON(t *testing.T) {
	t.Parallel()

	compiled := mustCompile(t, validFormat(), defaultTestLimits(t))
	raw := []byte("{\n  \"name\": \"Ada\",\n  \"count\": 3\n}\n")
	got, err := compiled.Validate(raw)
	if err != nil {
		t.Fatal(err)
	}
	if got != string(raw) {
		t.Fatalf("Validate()=%q, want exact input %q", got, raw)
	}
}

func TestValidateRejectsNonConformingOrNonExactJSON(t *testing.T) {
	t.Parallel()

	compiled := mustCompile(t, validFormat(), defaultTestLimits(t))
	tests := []struct {
		name string
		raw  string
	}{
		{"missing required", `{"name":"Ada"}`},
		{"wrong type", `{"name":"Ada","count":"3"}`},
		{"additional property", `{"name":"Ada","count":3,"extra":true}`},
		{"duplicate key", `{"name":"Ada","name":"Grace","count":3}`},
		{"fenced JSON", "```json\n{\"name\":\"Ada\",\"count\":3}\n```"},
		{"trailing prose", `{"name":"Ada","count":3} ` + plantedOutput},
		{"root array", `[]`},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := compiled.Validate([]byte(tt.raw))
			if !errors.Is(err, ErrInvalidOutput) {
				t.Fatalf("Validate() error=%v, want sentinel %v", err, ErrInvalidOutput)
			}
			if got != "" {
				t.Fatalf("Validate()=%q, want empty string on error", got)
			}
			if strings.Contains(err.Error(), plantedOutput) {
				t.Fatalf("Validate() exposed output fragment: %q", err)
			}
		})
	}
}

func TestValidateEnforcesOutputDepthAndNumberBoundaries(t *testing.T) {
	t.Parallel()

	compiled := mustCompile(
		t,
		permissiveValueFormat(),
		defaultTestLimits(t),
	)
	tests := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{
			"depth 128",
			`{"value":` + nestedArrays(127) + `}`,
			nil,
		},
		{
			"depth 129",
			`{"value":` + nestedArrays(128) + `}`,
			ErrInvalidOutput,
		},
		{
			"128 byte numeric token",
			`{"value":` + strings.Repeat("9", 128) + `}`,
			nil,
		},
		{
			"129 byte numeric token",
			`{"value":` + strings.Repeat("9", 129) + `}`,
			ErrInvalidOutput,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := compiled.Validate([]byte(tt.raw))
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Validate() error=%v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && got != tt.raw {
				t.Fatalf("Validate()=%q, want %q", got, tt.raw)
			}
			if tt.wantErr != nil && got != "" {
				t.Fatalf("Validate()=%q, want empty string", got)
			}
		})
	}
}

func TestValidateUsesConfiguredOutputByteCap(t *testing.T) {
	t.Parallel()

	raw := []byte(`{"name":"Ada","count":3}`)
	atLimit, err := DefaultLimits(testSchemaBytes, len(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got, err := mustCompile(t, validFormat(), atLimit).Validate(raw); err != nil ||
		got != string(raw) {
		t.Fatalf("Validate()=(%q, %v), want exact output at configured cap", got, err)
	}

	belowLimit, err := DefaultLimits(testSchemaBytes, len(raw)-1)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := mustCompile(
		t,
		validFormat(),
		belowLimit,
	).Validate(raw); !errors.Is(err, ErrInvalidOutput) || got != "" {
		t.Fatalf("Validate()=(%q, %v), want empty and %v", got, err, ErrInvalidOutput)
	}
}

func TestValidateRejectsOutputLargerThanOneMiB(t *testing.T) {
	t.Parallel()

	compiled := mustCompile(
		t,
		permissiveValueFormat(),
		defaultTestLimits(t),
	)
	raw := []byte(`{"value":"` + strings.Repeat("x", testOutputBytes) + `"}`)
	if got, err := compiled.Validate(
		raw,
	); !errors.Is(err, ErrInvalidOutput) || got != "" {
		t.Fatalf("Validate()=(%q, %v), want empty and %v", got, err, ErrInvalidOutput)
	}
}

func TestValidatePreservesExactNumbersBeyondFloatPrecision(t *testing.T) {
	t.Parallel()

	format := formatForSchema(schemaWithProperty(
		`"type":"integer","minimum":9007199254740993,` +
			`"maximum":9007199254740993`,
	))
	compiled := mustCompile(t, format, defaultTestLimits(t))

	valid := `{"value":9007199254740993}`
	if got, err := compiled.Validate([]byte(valid)); err != nil || got != valid {
		t.Fatalf("Validate()=(%q, %v), want exact valid integer", got, err)
	}
	if got, err := compiled.Validate(
		[]byte(`{"value":9007199254740992}`),
	); !errors.Is(err, ErrInvalidOutput) || got != "" {
		t.Fatalf("Validate()=(%q, %v), want empty and %v", got, err, ErrInvalidOutput)
	}
}

func TestValidateReturnsClosedErrorsWithoutOutputLeakage(t *testing.T) {
	t.Parallel()

	compiled := mustCompile(t, validFormat(), defaultTestLimits(t))
	for _, raw := range [][]byte{
		[]byte(`{"name":"` + plantedOutput + `","count":"wrong"}`),
		[]byte(`{"name":"Ada","count":3} ` + plantedOutput),
	} {
		got, err := compiled.Validate(raw)
		if !errors.Is(err, ErrInvalidOutput) || got != "" {
			t.Fatalf("Validate()=(%q, %v), want empty and %v", got, err, ErrInvalidOutput)
		}
		if strings.Contains(err.Error(), plantedOutput) {
			t.Fatalf("Validate() exposed output fragment: %q", err)
		}
	}
}

func defaultTestLimits(t *testing.T) Limits {
	t.Helper()

	limits, err := DefaultLimits(testSchemaBytes, testOutputBytes)
	if err != nil {
		t.Fatal(err)
	}
	return limits
}

func mustCompile(
	t *testing.T,
	format core.OutputFormat,
	limits Limits,
) *Compiled {
	t.Helper()

	compiled, err := Compile(format, limits)
	if err != nil {
		t.Fatal(err)
	}
	return compiled
}

func validFormat() core.OutputFormat {
	return formatForSchema(`{"type":"object","properties":{"name":{"type":"string","minLength":1,"maxLength":20},"count":{"type":"integer","minimum":0,"maximum":10}},"required":["name","count"],"additionalProperties":false}`)
}

func permissiveValueFormat() core.OutputFormat {
	return formatForSchema(schemaWithProperty(""))
}

func formatForSchema(schema string) core.OutputFormat {
	return core.OutputFormat{
		Type:   core.FormatJSONSchema,
		Name:   "result",
		Schema: json.RawMessage(schema),
	}
}

func rootWith(fragment string) string {
	return `{"type":"object","properties":{},"required":[],"additionalProperties":false,` +
		fragment + `}`
}

func schemaWithProperty(fragment string) string {
	return `{"type":"object","properties":{"value":{` + fragment +
		`}},"required":["value"],"additionalProperties":false}`
}

func schemaWithConstEntries(entries int) string {
	return rootWith(`"const":[` + strings.TrimSuffix(
		strings.Repeat("0,", entries),
		",",
	) + `]`)
}

func schemaWithConstDepth(depth int) string {
	return rootWith(
		`"const":` + strings.Repeat("[", depth) + `null` +
			strings.Repeat("]", depth),
	)
}

func schemaWithProperties(count int) string {
	properties := make([]string, 0, count)
	required := make([]string, 0, count)
	for i := 0; i < count; i++ {
		name := fmt.Sprintf("p%d", i)
		properties = append(properties, fmt.Sprintf("%q:{\"type\":\"string\"}", name))
		required = append(required, fmt.Sprintf("%q", name))
	}
	return `{"type":"object","properties":{` +
		strings.Join(properties, ",") +
		`},"required":[` + strings.Join(required, ",") +
		`],"additionalProperties":false}`
}

func schemaWithEnumEntries(entries int) string {
	values := make([]string, 0, entries)
	for i := 0; i < entries; i++ {
		values = append(values, fmt.Sprintf("%d", i))
	}
	return schemaWithProperty(`"enum":[` + strings.Join(values, ",") + `]`)
}

func nestedArrays(depth int) string {
	return strings.Repeat("[", depth) + `null` + strings.Repeat("]", depth)
}

func runSchemaTestSubprocess(t *testing.T, helperEnv string) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// Both the executable and run filter come from this test process.
	//nolint:gosec
	command := exec.CommandContext(
		ctx,
		os.Args[0],
		"-test.run=^"+t.Name()+"$",
		"-test.count=1",
	)
	command.Env = append(os.Environ(), helperEnv+"=1")
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("schema helper timed out: %v\n%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("schema helper failed: %v\n%s", err, output)
	}
}
