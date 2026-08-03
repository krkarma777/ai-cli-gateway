// Package httpapi contains strict HTTP API request decoding.
package httpapi

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/safejson"
)

// RequestLimits bounds normalized request fields and parser tokens.
type RequestLimits struct {
	InputBytes        int
	InstructionsBytes int
	SchemaBytes       int
	MaxDepth          int
	MaxNumberBytes    int
}

// ErrRequestLimits reports an invalid or overflowing request-limit set.
var ErrRequestLimits = errors.New("request limits are invalid")

const (
	defaultInputBytes        = 512 * 1024
	defaultInstructionsBytes = 256 * 1024
	defaultSchemaBytes       = 32 * 1024
	defaultMaxDepth          = 64
	defaultMaxNumberBytes    = 128
)

var formatNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

var (
	topLevelFields = fieldSet(
		"model",
		"instructions",
		"input",
		"text",
		"stream",
		"store",
		"tools",
		"tool_choice",
	)
	textFields       = fieldSet("format")
	textFormatFields = fieldSet("type")
	schemaFields     = fieldSet(
		"type",
		"name",
		"description",
		"strict",
		"schema",
	)
)

// DefaultRequestLimits returns the production defaults for normalized request
// fields and fixed parser bounds.
func DefaultRequestLimits() RequestLimits {
	return RequestLimits{
		InputBytes:        defaultInputBytes,
		InstructionsBytes: defaultInstructionsBytes,
		SchemaBytes:       defaultSchemaBytes,
		MaxDepth:          defaultMaxDepth,
		MaxNumberBytes:    defaultMaxNumberBytes,
	}
}

// NewRequestLimits validates configured byte limits and combines them with the
// fixed request-parser bounds.
func NewRequestLimits(
	inputBytes int,
	instructionsBytes int,
	schemaBytes int,
) (RequestLimits, error) {
	limits := RequestLimits{
		InputBytes:        inputBytes,
		InstructionsBytes: instructionsBytes,
		SchemaBytes:       schemaBytes,
		MaxDepth:          defaultMaxDepth,
		MaxNumberBytes:    defaultMaxNumberBytes,
	}
	if err := ValidateRequestLimits(limits); err != nil {
		return RequestLimits{}, err
	}
	return limits, nil
}

// ValidateRequestLimits rejects non-positive bounds and combinations whose
// aggregate could overflow an int during later admission accounting.
func ValidateRequestLimits(limits RequestLimits) error {
	total := 0
	for _, value := range []int{
		limits.InputBytes,
		limits.InstructionsBytes,
		limits.SchemaBytes,
		limits.MaxDepth,
		limits.MaxNumberBytes,
	} {
		if value <= 0 || total > math.MaxInt-value {
			return ErrRequestLimits
		}
		total += value
	}
	return nil
}

// DecodeRequest decodes the supported Responses API request subset.
func DecodeRequest(data []byte, limits RequestLimits) (core.Request, *core.APIError) {
	if err := ValidateRequestLimits(limits); err != nil {
		return core.Request{}, core.Error(core.CodeInvalidRequest, nil)
	}

	value, err := safejson.Parse(data, safejson.Limits{
		MaxDepth:       limits.MaxDepth,
		MaxNumberBytes: limits.MaxNumberBytes,
	})
	if err != nil {
		return core.Request{}, mapParseError(err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return core.Request{}, core.Error(core.CodeInvalidRequest, nil)
	}
	if hasUnknownField(object, topLevelFields) {
		return core.Request{}, core.Error(core.CodeUnsupportedParameter, nil)
	}

	model, apiErr := requiredString(object, "model", "model")
	if apiErr != nil {
		return core.Request{}, apiErr
	}
	input, apiErr := requiredString(object, "input", "input")
	if apiErr != nil {
		return core.Request{}, apiErr
	}
	if len(input) > limits.InputBytes {
		return core.Request{}, parameterError(core.CodeRequestTooLarge, "input")
	}
	instructions, apiErr := optionalStringOrNull(
		object,
		"instructions",
		"instructions",
	)
	if apiErr != nil {
		return core.Request{}, apiErr
	}
	if instructions != nil && len(*instructions) > limits.InstructionsBytes {
		return core.Request{}, parameterError(
			core.CodeRequestTooLarge,
			"instructions",
		)
	}

	if apiErr := optionalExactBool(object, "stream", "stream", false); apiErr != nil {
		return core.Request{}, apiErr
	}
	if apiErr := optionalExactBool(object, "store", "store", false); apiErr != nil {
		return core.Request{}, apiErr
	}
	if apiErr := optionalEmptyArray(object, "tools", "tools"); apiErr != nil {
		return core.Request{}, apiErr
	}
	if apiErr := optionalExactString(
		object,
		"tool_choice",
		"tool_choice",
		"none",
	); apiErr != nil {
		return core.Request{}, apiErr
	}

	format := core.OutputFormat{Type: core.FormatText}
	if rawText, exists := object["text"]; exists {
		format, apiErr = decodeText(rawText, limits)
		if apiErr != nil {
			return core.Request{}, apiErr
		}
	}

	return core.Request{
		ModelAlias:   model,
		Instructions: instructions,
		Input:        input,
		Format:       format,
	}, nil
}

func decodeText(value any, limits RequestLimits) (core.OutputFormat, *core.APIError) {
	text, ok := value.(map[string]any)
	if !ok {
		return core.OutputFormat{}, parameterError(core.CodeInvalidRequest, "text")
	}
	if hasUnknownField(text, textFields) {
		return core.OutputFormat{}, parameterError(
			core.CodeUnsupportedParameter,
			"text",
		)
	}

	rawFormat, exists := text["format"]
	if !exists {
		return core.OutputFormat{}, parameterError(
			core.CodeInvalidRequest,
			"text.format",
		)
	}
	format, ok := rawFormat.(map[string]any)
	if !ok {
		return core.OutputFormat{}, parameterError(
			core.CodeInvalidRequest,
			"text.format",
		)
	}
	formatType, apiErr := requiredTypedString(format, "type", "text.format.type")
	if apiErr != nil {
		return core.OutputFormat{}, apiErr
	}

	switch formatType {
	case string(core.FormatText):
		if hasUnknownField(format, textFormatFields) {
			return core.OutputFormat{}, parameterError(
				core.CodeUnsupportedParameter,
				"text.format",
			)
		}
		return core.OutputFormat{Type: core.FormatText}, nil
	case string(core.FormatJSONSchema):
		return decodeSchemaFormat(format, limits)
	default:
		return core.OutputFormat{}, parameterError(
			core.CodeUnsupportedParameter,
			"text.format.type",
		)
	}
}

func decodeSchemaFormat(
	format map[string]any,
	limits RequestLimits,
) (core.OutputFormat, *core.APIError) {
	if hasUnknownField(format, schemaFields) {
		return core.OutputFormat{}, parameterError(
			core.CodeUnsupportedParameter,
			"text.format",
		)
	}

	name, apiErr := requiredTypedString(format, "name", "text.format.name")
	if apiErr != nil {
		return core.OutputFormat{}, apiErr
	}
	if !formatNamePattern.MatchString(name) {
		return core.OutputFormat{}, parameterError(
			core.CodeUnsupportedParameter,
			"text.format.name",
		)
	}
	description, apiErr := optionalString(
		format,
		"description",
		"text.format.description",
	)
	if apiErr != nil {
		return core.OutputFormat{}, apiErr
	}
	if apiErr := requiredExactBool(
		format,
		"strict",
		"text.format.strict",
		true,
	); apiErr != nil {
		return core.OutputFormat{}, apiErr
	}

	rawSchema, exists := format["schema"]
	if !exists {
		return core.OutputFormat{}, parameterError(
			core.CodeInvalidRequest,
			"text.format.schema",
		)
	}
	if _, ok := rawSchema.(map[string]any); !ok {
		return core.OutputFormat{}, parameterError(
			core.CodeInvalidRequest,
			"text.format.schema",
		)
	}
	encodedSchema, err := json.Marshal(rawSchema)
	if err != nil {
		return core.OutputFormat{}, parameterError(
			core.CodeInvalidRequest,
			"text.format.schema",
		)
	}
	if len(encodedSchema) > limits.SchemaBytes {
		return core.OutputFormat{}, parameterError(
			core.CodeRequestTooLarge,
			"text.format.schema",
		)
	}

	return core.OutputFormat{
		Type:        core.FormatJSONSchema,
		Name:        name,
		Description: description,
		Schema:      encodedSchema,
	}, nil
}

func requiredString(
	object map[string]any,
	key string,
	param string,
) (string, *core.APIError) {
	text, apiErr := requiredTypedString(object, key, param)
	if apiErr != nil {
		return "", apiErr
	}
	if text == "" {
		return "", parameterError(core.CodeInvalidRequest, param)
	}
	return text, nil
}

func requiredTypedString(
	object map[string]any,
	key string,
	param string,
) (string, *core.APIError) {
	value, exists := object[key]
	if !exists {
		return "", parameterError(core.CodeInvalidRequest, param)
	}
	text, ok := value.(string)
	if !ok {
		return "", parameterError(core.CodeInvalidRequest, param)
	}
	return text, nil
}

func optionalStringOrNull(
	object map[string]any,
	key string,
	param string,
) (*string, *core.APIError) {
	value, exists := object[key]
	if !exists || value == nil {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, parameterError(core.CodeInvalidRequest, param)
	}
	return &text, nil
}

func optionalString(
	object map[string]any,
	key string,
	param string,
) (*string, *core.APIError) {
	value, exists := object[key]
	if !exists {
		return nil, nil
	}
	text, ok := value.(string)
	if !ok {
		return nil, parameterError(core.CodeInvalidRequest, param)
	}
	return &text, nil
}

func optionalExactBool(
	object map[string]any,
	key string,
	param string,
	want bool,
) *core.APIError {
	value, exists := object[key]
	if !exists {
		return nil
	}
	got, ok := value.(bool)
	if !ok {
		return parameterError(core.CodeInvalidRequest, param)
	}
	if got != want {
		return parameterError(core.CodeUnsupportedParameter, param)
	}
	return nil
}

func requiredExactBool(
	object map[string]any,
	key string,
	param string,
	want bool,
) *core.APIError {
	value, exists := object[key]
	if !exists {
		return parameterError(core.CodeInvalidRequest, param)
	}
	got, ok := value.(bool)
	if !ok {
		return parameterError(core.CodeInvalidRequest, param)
	}
	if got != want {
		return parameterError(core.CodeUnsupportedParameter, param)
	}
	return nil
}

func optionalEmptyArray(
	object map[string]any,
	key string,
	param string,
) *core.APIError {
	value, exists := object[key]
	if !exists {
		return nil
	}
	array, ok := value.([]any)
	if !ok {
		return parameterError(core.CodeInvalidRequest, param)
	}
	if len(array) != 0 {
		return parameterError(core.CodeUnsupportedParameter, param)
	}
	return nil
}

func optionalExactString(
	object map[string]any,
	key string,
	param string,
	want string,
) *core.APIError {
	value, exists := object[key]
	if !exists {
		return nil
	}
	got, ok := value.(string)
	if !ok {
		return parameterError(core.CodeInvalidRequest, param)
	}
	if got != want {
		return parameterError(core.CodeUnsupportedParameter, param)
	}
	return nil
}

func mapParseError(err error) *core.APIError {
	switch {
	case errors.Is(err, safejson.ErrSyntax),
		errors.Is(err, safejson.ErrDuplicate),
		errors.Is(err, safejson.ErrTrailing):
		return core.Error(core.CodeInvalidJSON, nil)
	default:
		return core.Error(core.CodeInvalidRequest, nil)
	}
}

func parameterError(code string, param string) *core.APIError {
	return core.Error(code, &param)
}

func fieldSet(fields ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		set[field] = struct{}{}
	}
	return set
}

func hasUnknownField(
	object map[string]any,
	allowed map[string]struct{},
) bool {
	for key := range object {
		if _, ok := allowed[key]; !ok {
			return true
		}
	}
	return false
}
