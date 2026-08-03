// Package schema compiles and validates the gateway's portable JSON Schema
// profile.
package schema

import (
	"encoding/json"
	"errors"
	"math"
	"math/big"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/safejson"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

// Limits bounds schema compilation and structured-output validation.
type Limits struct {
	SchemaBytes   int
	MaxNodes      int
	MaxDepth      int
	MaxProperties int
	MaxEnum       int
	OutputBytes   int
	OutputDepth   int
	NumberBytes   int
}

// Closed schema error categories.
var (
	ErrLimits        = errors.New("schema limits are invalid")
	ErrInvalidSchema = errors.New("JSON schema is invalid")
	ErrInvalidOutput = errors.New("structured output is invalid")
)

const (
	defaultMaxNodes      = 512
	defaultMaxDepth      = 32
	defaultMaxProperties = 100
	defaultMaxEnum       = 256
	defaultOutputDepth   = 128
	defaultNumberBytes   = 128
)

var (
	errPreflight = errors.New("schema preflight failed")
	errNoFetch   = errors.New("external schema loading is disabled")
	allowed      = map[string]struct{}{
		"type":                 {},
		"properties":           {},
		"required":             {},
		"additionalProperties": {},
		"items":                {},
		"enum":                 {},
		"const":                {},
		"minLength":            {},
		"maxLength":            {},
		"minItems":             {},
		"maxItems":             {},
		"minProperties":        {},
		"maxProperties":        {},
		"minimum":              {},
		"maximum":              {},
		"exclusiveMinimum":     {},
		"exclusiveMaximum":     {},
		"description":          {},
		"title":                {},
	}
	supportedTypes = map[string]struct{}{
		"object":  {},
		"array":   {},
		"string":  {},
		"number":  {},
		"integer": {},
		"boolean": {},
		"null":    {},
	}
)

// Compiled holds a locally compiled portable schema and its validation limits.
type Compiled struct {
	schema *jsonschema.Schema
	limits Limits
}

// DefaultLimits combines configured byte caps with the fixed portable-profile
// bounds.
func DefaultLimits(schemaBytes, outputBytes int) (Limits, error) {
	limits := Limits{
		SchemaBytes:   schemaBytes,
		MaxNodes:      defaultMaxNodes,
		MaxDepth:      defaultMaxDepth,
		MaxProperties: defaultMaxProperties,
		MaxEnum:       defaultMaxEnum,
		OutputBytes:   outputBytes,
		OutputDepth:   defaultOutputDepth,
		NumberBytes:   defaultNumberBytes,
	}
	if err := validateLimits(limits); err != nil {
		return Limits{}, err
	}
	return limits, nil
}

// Compile preflights and compiles an output format without permitting external
// schema resolution.
func Compile(format core.OutputFormat, limits Limits) (*Compiled, error) {
	if err := validateLimits(limits); err != nil {
		return nil, err
	}
	if format.Type != core.FormatJSONSchema ||
		len(format.Schema) > limits.SchemaBytes {
		return nil, ErrInvalidSchema
	}

	schemaValue, err := safejson.Parse(format.Schema, safejson.Limits{
		MaxDepth:       limits.MaxDepth,
		MaxNumberBytes: limits.NumberBytes,
	})
	if err != nil {
		return nil, ErrInvalidSchema
	}
	if !numbersWithinDecimalBudget(schemaValue, limits.NumberBytes) {
		return nil, ErrInvalidSchema
	}

	check := preflight{limits: limits}
	if err := check.walk(schemaValue, contextSchema, 0, true); err != nil {
		return nil, ErrInvalidSchema
	}

	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	compiler.UseLoader(rejectLoader{})
	if err := compiler.AddResource(
		"urn:ai-cli-gateway:schema",
		schemaValue,
	); err != nil {
		return nil, ErrInvalidSchema
	}
	compiled, err := compiler.Compile("urn:ai-cli-gateway:schema")
	if err != nil {
		return nil, ErrInvalidSchema
	}
	return &Compiled{schema: compiled, limits: limits}, nil
}

// Validate parses exactly one JSON object, validates it locally, and returns
// the original bytes as a string only after every check passes.
func (c *Compiled) Validate(raw []byte) (string, error) {
	if c == nil || c.schema == nil || validateLimits(c.limits) != nil {
		return "", ErrInvalidOutput
	}
	if len(raw) > c.limits.OutputBytes {
		return "", ErrInvalidOutput
	}

	value, err := safejson.Parse(raw, safejson.Limits{
		MaxDepth:       c.limits.OutputDepth,
		MaxNumberBytes: c.limits.NumberBytes,
	})
	if err != nil {
		return "", ErrInvalidOutput
	}
	if _, ok := value.(map[string]any); !ok {
		return "", ErrInvalidOutput
	}
	if !numbersWithinDecimalBudget(value, c.limits.NumberBytes) {
		return "", ErrInvalidOutput
	}
	if err := c.schema.Validate(value); err != nil {
		return "", ErrInvalidOutput
	}
	return string(raw), nil
}

func validateLimits(limits Limits) error {
	total := 0
	for _, value := range []int{
		limits.SchemaBytes,
		limits.MaxNodes,
		limits.MaxDepth,
		limits.MaxProperties,
		limits.MaxEnum,
		limits.OutputBytes,
		limits.OutputDepth,
		limits.NumberBytes,
	} {
		if value <= 0 || total > math.MaxInt-value {
			return ErrLimits
		}
		total += value
	}
	return nil
}

type walkContext uint8

const (
	contextSchema walkContext = iota
	contextProperties
	contextLiteral
)

type preflight struct {
	limits Limits
	nodes  int
}

func (p *preflight) walk(
	value any,
	context walkContext,
	containerDepth int,
	root bool,
) error {
	p.nodes++
	if p.nodes > p.limits.MaxNodes {
		return errPreflight
	}

	switch typed := value.(type) {
	case map[string]any:
		containerDepth++
		if containerDepth > p.limits.MaxDepth {
			return errPreflight
		}
		switch context {
		case contextSchema:
			if err := p.validateSchemaObject(typed, root); err != nil {
				return err
			}
			for keyword, child := range typed {
				childContext := contextLiteral
				switch keyword {
				case "properties":
					childContext = contextProperties
				case "items":
					childContext = contextSchema
				}
				if err := p.walk(
					child,
					childContext,
					containerDepth,
					false,
				); err != nil {
					return err
				}
			}
			return nil
		case contextProperties:
			if len(typed) > p.limits.MaxProperties {
				return errPreflight
			}
			for _, child := range typed {
				if err := p.walk(
					child,
					contextSchema,
					containerDepth,
					false,
				); err != nil {
					return err
				}
			}
			return nil
		case contextLiteral:
			for _, child := range typed {
				if err := p.walk(
					child,
					contextLiteral,
					containerDepth,
					false,
				); err != nil {
					return err
				}
			}
			return nil
		default:
			return errPreflight
		}

	case []any:
		containerDepth++
		if containerDepth > p.limits.MaxDepth ||
			context != contextLiteral {
			return errPreflight
		}
		for _, child := range typed {
			if err := p.walk(
				child,
				contextLiteral,
				containerDepth,
				false,
			); err != nil {
				return err
			}
		}
		return nil

	case bool:
		if context == contextSchema && !root {
			return nil
		}
		if context != contextLiteral {
			return errPreflight
		}
		return nil

	case string, json.Number, nil:
		if context != contextLiteral {
			return errPreflight
		}
		return nil

	default:
		return errPreflight
	}
}

func (p *preflight) validateSchemaObject(object map[string]any, root bool) error {
	for keyword := range object {
		if _, ok := allowed[keyword]; !ok {
			return errPreflight
		}
	}

	typeName := ""
	if value, exists := object["type"]; exists {
		var ok bool
		typeName, ok = value.(string)
		if !ok {
			return errPreflight
		}
		if _, ok := supportedTypes[typeName]; !ok {
			return errPreflight
		}
	}
	if root && typeName != "object" {
		return errPreflight
	}

	if value, exists := object["properties"]; exists {
		properties, ok := value.(map[string]any)
		if !ok || len(properties) > p.limits.MaxProperties {
			return errPreflight
		}
	}

	required, err := requiredSet(object)
	if err != nil {
		return err
	}

	if value, exists := object["additionalProperties"]; exists {
		if _, ok := value.(bool); !ok {
			return errPreflight
		}
	}

	if value, exists := object["items"]; exists {
		switch value.(type) {
		case map[string]any, bool:
		default:
			return errPreflight
		}
	}

	if value, exists := object["enum"]; exists {
		entries, ok := value.([]any)
		if !ok || len(entries) > p.limits.MaxEnum {
			return errPreflight
		}
	}

	for _, keyword := range []string{
		"minLength",
		"maxLength",
		"minItems",
		"maxItems",
		"minProperties",
		"maxProperties",
	} {
		if value, exists := object[keyword]; exists {
			if _, ok := nonNegativeInteger(
				value,
				p.limits.NumberBytes,
			); !ok {
				return errPreflight
			}
		}
	}

	for _, pair := range [][2]string{
		{"minLength", "maxLength"},
		{"minItems", "maxItems"},
		{"minProperties", "maxProperties"},
	} {
		if !orderedIntegerPair(
			object,
			pair[0],
			pair[1],
			p.limits.NumberBytes,
		) {
			return errPreflight
		}
	}

	if err := validateNumericBounds(object, p.limits.NumberBytes); err != nil {
		return err
	}

	for _, keyword := range []string{"description", "title"} {
		if value, exists := object[keyword]; exists {
			if _, ok := value.(string); !ok {
				return errPreflight
			}
		}
	}

	if typeName == "object" {
		additional, exists := object["additionalProperties"]
		closed, ok := additional.(bool)
		if !exists || !ok || closed {
			return errPreflight
		}

		properties := map[string]any{}
		if value, exists := object["properties"]; exists {
			properties = value.(map[string]any)
		}
		if len(required) != len(properties) {
			return errPreflight
		}
		for property := range properties {
			if _, ok := required[property]; !ok {
				return errPreflight
			}
		}
	}

	return nil
}

func requiredSet(object map[string]any) (map[string]struct{}, error) {
	value, exists := object["required"]
	if !exists {
		return map[string]struct{}{}, nil
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errPreflight
	}
	required := make(map[string]struct{}, len(items))
	for _, item := range items {
		name, ok := item.(string)
		if !ok {
			return nil, errPreflight
		}
		if _, duplicate := required[name]; duplicate {
			return nil, errPreflight
		}
		required[name] = struct{}{}
	}
	return required, nil
}

func nonNegativeInteger(value any, decimalBudget int) (*big.Int, bool) {
	number, ok := value.(json.Number)
	if !ok {
		return nil, false
	}
	rat, ok := exactRational(number, decimalBudget)
	if !ok || !rat.IsInt() || rat.Sign() < 0 {
		return nil, false
	}
	integer := new(big.Int).Set(rat.Num())
	if integer.Cmp(big.NewInt(int64(math.MaxInt))) > 0 {
		return nil, false
	}
	return integer, true
}

func orderedIntegerPair(
	object map[string]any,
	lowerKey string,
	upperKey string,
	decimalBudget int,
) bool {
	lowerValue, hasLower := object[lowerKey]
	upperValue, hasUpper := object[upperKey]
	if !hasLower || !hasUpper {
		return true
	}
	lower, lowerOK := nonNegativeInteger(lowerValue, decimalBudget)
	upper, upperOK := nonNegativeInteger(upperValue, decimalBudget)
	return lowerOK && upperOK && lower.Cmp(upper) <= 0
}

type rationalBound struct {
	value *big.Rat
}

func validateNumericBounds(object map[string]any, decimalBudget int) error {
	lower := make([]rationalBound, 0, 2)
	upper := make([]rationalBound, 0, 2)
	for _, bound := range []struct {
		keyword string
		target  *[]rationalBound
	}{
		{"minimum", &lower},
		{"exclusiveMinimum", &lower},
		{"maximum", &upper},
		{"exclusiveMaximum", &upper},
	} {
		value, exists := object[bound.keyword]
		if !exists {
			continue
		}
		number, ok := value.(json.Number)
		if !ok {
			return errPreflight
		}
		rat, ok := exactRational(number, decimalBudget)
		if !ok {
			return errPreflight
		}
		*bound.target = append(*bound.target, rationalBound{value: rat})
	}

	for _, low := range lower {
		for _, high := range upper {
			if low.value.Cmp(high.value) > 0 {
				return errPreflight
			}
		}
	}
	return nil
}

func exactRational(number json.Number, decimalBudget int) (*big.Rat, bool) {
	if !decimalNumberWithinBudget(number, decimalBudget) {
		return nil, false
	}
	rat, ok := new(big.Rat).SetString(number.String())
	if !ok {
		return nil, false
	}
	return rat, true
}

func numbersWithinDecimalBudget(value any, decimalBudget int) bool {
	switch typed := value.(type) {
	case map[string]any:
		for _, child := range typed {
			if !numbersWithinDecimalBudget(child, decimalBudget) {
				return false
			}
		}
		return true
	case []any:
		for _, child := range typed {
			if !numbersWithinDecimalBudget(child, decimalBudget) {
				return false
			}
		}
		return true
	case json.Number:
		return decimalNumberWithinBudget(typed, decimalBudget)
	case string, bool, nil:
		return true
	default:
		return false
	}
}

func decimalNumberWithinBudget(number json.Number, decimalBudget int) bool {
	text := number.String()
	if decimalBudget <= 0 || len(text) == 0 || len(text) > decimalBudget {
		return false
	}

	index := 0
	if text[index] == '-' {
		index++
		if index == len(text) {
			return false
		}
	}

	integerStart := index
	switch {
	case text[index] == '0':
		index++
		if index < len(text) && isDecimalDigit(text[index]) {
			return false
		}
	case text[index] >= '1' && text[index] <= '9':
		for index < len(text) && isDecimalDigit(text[index]) {
			index++
		}
	default:
		return false
	}
	integerDigits := index - integerStart

	fractionDigits := 0
	if index < len(text) && text[index] == '.' {
		index++
		fractionStart := index
		for index < len(text) && isDecimalDigit(text[index]) {
			index++
		}
		fractionDigits = index - fractionStart
		if fractionDigits == 0 {
			return false
		}
	}

	exponent := 0
	if index < len(text) && (text[index] == 'e' || text[index] == 'E') {
		index++
		negative := false
		if index < len(text) && (text[index] == '+' || text[index] == '-') {
			negative = text[index] == '-'
			index++
		}
		if index == len(text) {
			return false
		}

		for index < len(text) && isDecimalDigit(text[index]) {
			digit := int(text[index] - '0')
			if exponent > decimalBudget/10 ||
				(exponent == decimalBudget/10 &&
					digit > decimalBudget%10) {
				return false
			}
			exponent = exponent*10 + digit
			index++
		}
		if negative {
			exponent = -exponent
		}
	}

	if index != len(text) {
		return false
	}
	return exponent >= fractionDigits-decimalBudget &&
		exponent <= decimalBudget-integerDigits
}

func isDecimalDigit(value byte) bool {
	return value >= '0' && value <= '9'
}

type rejectLoader struct{}

func (rejectLoader) Load(string) (any, error) {
	return nil, errNoFetch
}
