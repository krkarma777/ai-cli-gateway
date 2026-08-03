package provider

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
)

const textOutputContract = "Return only the final answer as plain text."

func TestBuildPromptDistinguishesAbsentAndPresentEmptyInstructions(t *testing.T) {
	tests := []struct {
		name         string
		instructions *string
		want         string
	}{
		{
			name: "absent",
			want: "AI_CLI_GATEWAY/1\n" +
				"INSTRUCTIONS NULL\n" +
				"INPUT 5\nhello\n" +
				"OUTPUT_CONTRACT 43\n" +
				textOutputContract + "\n",
		},
		{
			name:         "present empty",
			instructions: stringPointer(""),
			want: "AI_CLI_GATEWAY/1\n" +
				"INSTRUCTIONS 0\n\n" +
				"INPUT 5\nhello\n" +
				"OUTPUT_CONTRACT 43\n" +
				textOutputContract + "\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := BuildPrompt(core.Request{
				Instructions: test.instructions,
				Input:        "hello",
				Format:       core.OutputFormat{Type: core.FormatText},
			}, SchemaInline)
			if string(got) != test.want {
				t.Fatalf("prompt mismatch\n got: %q\nwant: %q", got, test.want)
			}
		})
	}
}

func TestBuildPromptCountsUTF8BytesAndPreservesPayloadExactly(t *testing.T) {
	instructions := "한국어\nINPUT 999\n--not-an-argument"
	input := "-leading\nOUTPUT_CONTRACT 999\n\"quoted\""
	want := "AI_CLI_GATEWAY/1\n" +
		"INSTRUCTIONS 37\n" + instructions + "\n" +
		"INPUT 37\n" + input + "\n" +
		"OUTPUT_CONTRACT 43\n" + textOutputContract + "\n"

	got := BuildPrompt(core.Request{
		Instructions: &instructions,
		Input:        input,
		Format:       core.OutputFormat{Type: core.FormatText},
	}, SchemaInline)

	if string(got) != want {
		t.Fatalf("prompt mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestBuildPromptInlineSchemaHasStableCompactObject(t *testing.T) {
	request := core.Request{
		Input: "답해 주세요",
		Format: core.OutputFormat{
			Type:   core.FormatJSONSchema,
			Name:   "answer",
			Schema: []byte(`{ "type": "object", "properties": { "답": { "type": "string" } } }`),
		},
	}
	wantContract := `{"type":"json_schema","name":"answer","strict":true,"schema":{"type":"object","properties":{"답":{"type":"string"}}}}` +
		"\nReturn exactly one JSON object and no surrounding text."
	want := "AI_CLI_GATEWAY/1\n" +
		"INSTRUCTIONS NULL\n" +
		"INPUT 16\n답해 주세요\n" +
		fmt.Sprintf("OUTPUT_CONTRACT %d\n", len(wantContract)) +
		wantContract + "\n"

	got := BuildPrompt(request, SchemaInline)
	if string(got) != want {
		t.Fatalf("prompt mismatch\n got: %q\nwant: %q", got, want)
	}
}

func TestBuildPromptInlineSchemaPreservesDescriptionPresence(t *testing.T) {
	base := core.Request{
		Format: core.OutputFormat{
			Type:   core.FormatJSONSchema,
			Name:   "answer",
			Schema: []byte(`{"type":"object"}`),
		},
	}

	withoutDescription := BuildPrompt(base, SchemaInline)
	if bytes.Contains(withoutDescription, []byte(`"description"`)) {
		t.Fatalf("nil description was emitted: %q", withoutDescription)
	}

	base.Format.Description = stringPointer("")
	withEmptyDescription := BuildPrompt(base, SchemaInline)
	wantOrder := `"type":"json_schema","name":"answer","description":"","strict":true,"schema":`
	if !bytes.Contains(withEmptyDescription, []byte(wantOrder)) {
		t.Fatalf("empty description missing or keys unstable: %q", withEmptyDescription)
	}
}

func TestBuildPromptRejectsInvalidUTF8InInlineSchema(t *testing.T) {
	request := core.Request{
		Format: core.OutputFormat{
			Type:   core.FormatJSONSchema,
			Name:   "answer",
			Schema: []byte("{\"type\":\"object\",\"description\":\"\xff\"}"),
		},
	}

	if got := BuildPrompt(request, SchemaInline); got != nil {
		t.Fatalf("invalid UTF-8 inline schema produced prompt: %q", got)
	}
}

func TestBuildPromptSchemaFileContractContainsNoSchemaMetadata(t *testing.T) {
	instructions := "private instructions"
	request := core.Request{
		Instructions: &instructions,
		Input:        "private input",
		Format: core.OutputFormat{
			Type:        core.FormatJSONSchema,
			Name:        "secret-schema-name",
			Description: stringPointer("secret description"),
			Schema:      []byte(`{"type":"object","secret-schema-marker":true}`),
		},
	}
	wantContract := "Follow the JSON Schema supplied in the gateway-owned schema file and return exactly one JSON object with no surrounding text."

	got := BuildPrompt(request, SchemaFile)
	outputContract := mustPromptSection(t, got, "OUTPUT_CONTRACT")
	if string(outputContract) != wantContract {
		t.Fatalf("file contract=%q, want %q", outputContract, wantContract)
	}
	for _, forbidden := range []string{
		"secret-schema-name",
		"secret description",
		"secret-schema-marker",
		"private instructions",
		"private input",
	} {
		if bytes.Contains(outputContract, []byte(forbidden)) {
			t.Fatalf("file output contract contains %q", forbidden)
		}
	}
	if !bytes.Contains(got, []byte(instructions)) || !bytes.Contains(got, []byte(request.Input)) {
		t.Fatal("separately framed instructions or input missing from overall prompt")
	}
}

func TestBuildPromptProducesStdinWithoutPromptInArgs(t *testing.T) {
	instructions := "--secret-system-flag"
	request := core.Request{
		Instructions: &instructions,
		Input:        "-prompt-must-stay-on-stdin",
		Format:       core.OutputFormat{Type: core.FormatText},
	}

	command := process.CommandSpec{
		Executable: "/trusted/provider",
		Args:       []string{"exec", "--safe", "-"},
		Stdin:      BuildPrompt(request, SchemaInline),
	}

	args := strings.Join(command.Args, "\x00")
	for _, promptFragment := range []string{instructions, request.Input, textOutputContract} {
		if strings.Contains(args, promptFragment) {
			t.Fatalf("prompt fragment %q appeared in argv", promptFragment)
		}
		if !bytes.Contains(command.Stdin, []byte(promptFragment)) {
			t.Fatalf("prompt fragment %q missing from stdin", promptFragment)
		}
	}
}

func TestBuildPromptReturnsFreshOwnedBuffers(t *testing.T) {
	request := core.Request{
		Input:  "owned prompt",
		Format: core.OutputFormat{Type: core.FormatText},
	}

	first := BuildPrompt(request, SchemaInline)
	second := BuildPrompt(request, SchemaInline)
	if len(first) == 0 || len(second) == 0 {
		t.Fatal("BuildPrompt returned an empty prompt")
	}
	first[0] = 'X'
	if second[0] != 'A' {
		t.Fatalf("second prompt buffer was mutated: %q", second)
	}
}

func TestBuildPromptRejectsUnknownFormatOrDelivery(t *testing.T) {
	if got := BuildPrompt(core.Request{
		Format: core.OutputFormat{Type: core.FormatType("unknown")},
	}, SchemaInline); got != nil {
		t.Fatalf("unknown format produced prompt: %q", got)
	}
	if got := BuildPrompt(core.Request{
		Format: core.OutputFormat{
			Type:   core.FormatJSONSchema,
			Name:   "answer",
			Schema: []byte(`{"type":"object"}`),
		},
	}, SchemaDelivery(255)); got != nil {
		t.Fatalf("unknown delivery produced prompt: %q", got)
	}
}

func mustPromptSection(t *testing.T, prompt []byte, section string) []byte {
	t.Helper()
	header := []byte(section + " ")
	start := bytes.Index(prompt, header)
	if start < 0 {
		t.Fatalf("%s header missing from %q", section, prompt)
	}
	start += len(header)
	headerEnd := bytes.IndexByte(prompt[start:], '\n')
	if headerEnd < 0 {
		t.Fatalf("%s header newline missing", section)
	}
	var length int
	if _, err := fmt.Sscanf(string(prompt[start:start+headerEnd]), "%d", &length); err != nil {
		t.Fatalf("%s length invalid: %v", section, err)
	}
	valueStart := start + headerEnd + 1
	valueEnd := valueStart + length
	if length < 0 || valueEnd >= len(prompt) || prompt[valueEnd] != '\n' {
		t.Fatalf("%s payload framing invalid", section)
	}
	return prompt[valueStart:valueEnd]
}

func stringPointer(value string) *string {
	return &value
}
