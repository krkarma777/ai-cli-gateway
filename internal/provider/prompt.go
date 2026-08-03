package provider

import (
	"bytes"
	"encoding/json"
	"fmt"
	"unicode/utf8"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

const (
	plainTextOutputContract = "Return only the final answer as plain text."
	inlineJSONInstruction   = "Return exactly one JSON object and no surrounding text."
	fileJSONOutputContract  = "Follow the JSON Schema supplied in the gateway-owned schema file and return exactly one JSON object with no surrounding text."
)

type inlineSchemaContract struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description *string         `json:"description,omitempty"`
	Strict      bool            `json:"strict"`
	Schema      json.RawMessage `json:"schema"`
}

// BuildPrompt returns the exact stdin framing for a provider request. Unknown
// formats, schema delivery values, or invalid inline schema JSON fail closed by
// returning nil.
func BuildPrompt(request core.Request, delivery SchemaDelivery) []byte {
	if delivery != SchemaInline && delivery != SchemaFile {
		return nil
	}

	outputContract, ok := buildOutputContract(request, delivery)
	if !ok {
		return nil
	}

	var prompt bytes.Buffer
	prompt.WriteString("AI_CLI_GATEWAY/1\n")
	if request.Instructions == nil {
		prompt.WriteString("INSTRUCTIONS NULL\n")
	} else {
		writePromptSection(&prompt, "INSTRUCTIONS", []byte(*request.Instructions))
	}
	writePromptSection(&prompt, "INPUT", []byte(request.Input))
	writePromptSection(&prompt, "OUTPUT_CONTRACT", outputContract)
	return prompt.Bytes()
}

func buildOutputContract(
	request core.Request,
	delivery SchemaDelivery,
) ([]byte, bool) {
	switch request.Format.Type {
	case core.FormatText:
		return []byte(plainTextOutputContract), true
	case core.FormatJSONSchema:
		if delivery == SchemaFile {
			return []byte(fileJSONOutputContract), true
		}
		if !utf8.Valid(request.Format.Schema) {
			return nil, false
		}
		inline, err := json.Marshal(inlineSchemaContract{
			Type:        string(core.FormatJSONSchema),
			Name:        request.Format.Name,
			Description: request.Format.Description,
			Strict:      true,
			Schema:      request.Format.Schema,
		})
		if err != nil {
			return nil, false
		}
		inline = append(inline, '\n')
		inline = append(inline, inlineJSONInstruction...)
		return inline, true
	default:
		return nil, false
	}
}

func writePromptSection(prompt *bytes.Buffer, name string, value []byte) {
	_, _ = fmt.Fprintf(prompt, "%s %d\n", name, len(value))
	_, _ = prompt.Write(value)
	_ = prompt.WriteByte('\n')
}
