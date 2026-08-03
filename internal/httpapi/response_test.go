package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestOpaqueIDSourceKnownVector(t *testing.T) {
	seed := make([]byte, 32)
	for i := range seed {
		seed[i] = byte(i)
	}

	ids, err := NewOpaqueIDSource(bytes.NewReader(seed))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := ids.Next("req"), "req_4zjyptqb6kbk3hg5zi2xx3d7ba"; got != want {
		t.Fatalf("id=%q want=%q", got, want)
	}
}

func TestOpaqueIDSourceRequiresCompleteSeed(t *testing.T) {
	tests := []struct {
		name   string
		reader io.Reader
	}{
		{name: "short", reader: bytes.NewReader(make([]byte, 31))},
		{name: "reader error", reader: errorReader{}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NewOpaqueIDSource(test.reader); !errors.Is(err, errOpaqueIDSource) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestOpaqueIDSourceReadsExactlyOneSeed(t *testing.T) {
	reader := &countingReader{reader: bytes.NewReader(make([]byte, 64))}
	if _, err := NewOpaqueIDSource(reader); err != nil {
		t.Fatal(err)
	}
	if reader.total != 32 {
		t.Fatalf("read=%d", reader.total)
	}
}

func TestOpaqueIDSourcePrefixesAndAlphabet(t *testing.T) {
	ids, err := NewOpaqueIDSource(bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}

	pattern := regexp.MustCompile(`^(req|resp|msg)_[a-z2-7]{26}$`)
	seen := make(map[string]struct{})
	for _, prefix := range []string{"req", "resp", "msg"} {
		id := ids.Next(prefix)
		if !pattern.MatchString(id) {
			t.Fatalf("id=%q", id)
		}
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate id=%q", id)
		}
		seen[id] = struct{}{}
	}
}

func TestOpaqueIDSourceIsConcurrentAndUnique(t *testing.T) {
	ids, err := NewOpaqueIDSource(bytes.NewReader(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}

	const count = 10_000
	results := make(chan string, count)
	var workers sync.WaitGroup
	workers.Add(count)
	for range count {
		go func() {
			defer workers.Done()
			results <- ids.Next("req")
		}()
	}
	workers.Wait()
	close(results)

	seen := make(map[string]struct{}, count)
	for id := range results {
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicate id=%q", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("unique=%d want=%d", len(seen), count)
	}
}

func TestEncodeSuccessResponseTextShape(t *testing.T) {
	request := core.Request{
		ModelAlias: "codex-default",
		Input:      "hello",
		Format:     core.OutputFormat{Type: core.FormatText},
	}
	snapshot, err := snapshotResponseRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	limit, err := successResponseBufferLimit(1<<20, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := encodeSuccessResponse(
		snapshot,
		core.Result{Text: "Final provider output"},
		"resp_TEST",
		"msg_TEST",
		time.Unix(1_785_369_600, 0),
		time.Unix(1_785_369_601, 0),
		1<<20,
		limit,
	)
	if err != nil {
		t.Fatal(err)
	}

	want := decodeJSONObject(t, []byte(`{
	  "id":"resp_TEST",
	  "object":"response",
	  "created_at":1785369600,
	  "completed_at":1785369601,
	  "status":"completed",
	  "background":false,
	  "error":null,
	  "incomplete_details":null,
	  "instructions":null,
	  "model":"codex-default",
	  "output":[{
	    "id":"msg_TEST",
	    "type":"message",
	    "status":"completed",
	    "role":"assistant",
	    "content":[{
	      "type":"output_text",
	      "annotations":[],
	      "text":"Final provider output"
	    }]
	  }],
	  "parallel_tool_calls":false,
	  "previous_response_id":null,
	  "store":false,
	  "text":{"format":{"type":"text"}},
	  "tools":[],
	  "tool_choice":"none"
	}`))
	got := decodeJSONObject(t, raw)
	if !objectsEqual(got, want) {
		t.Fatalf("response=%s", raw)
	}
	for _, field := range []string{"usage", "output_json"} {
		if _, exists := got[field]; exists {
			t.Fatalf("invented field %q in response", field)
		}
	}
}

func TestEncodeSuccessResponseEchoesJSONSchemaAndDescriptionPresence(t *testing.T) {
	tests := []struct {
		name        string
		description *string
		wantPresent bool
	}{
		{name: "absent", description: nil, wantPresent: false},
		{name: "present empty", description: stringPointer(""), wantPresent: true},
		{name: "present", description: stringPointer("structured answer"), wantPresent: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := core.Request{
				ModelAlias:   "claude-default",
				Instructions: stringPointer("be concise"),
				Input:        "hello",
				Format: core.OutputFormat{
					Type:        core.FormatJSONSchema,
					Name:        "result",
					Description: test.description,
					Schema: json.RawMessage(
						`{"type":"object","properties":{},"required":[],"additionalProperties":false}`,
					),
				},
			}
			snapshot, err := snapshotResponseRequest(request)
			if err != nil {
				t.Fatal(err)
			}
			raw, err := encodeSuccessResponse(
				snapshot,
				core.Result{Text: " {\n\"ok\":true\n} "},
				"resp_TEST",
				"msg_TEST",
				time.Unix(10, 0),
				time.Unix(11, 0),
				1<<20,
				1<<20,
			)
			if err != nil {
				t.Fatal(err)
			}

			root := decodeJSONObject(t, raw)
			if root["instructions"] != "be concise" {
				t.Fatalf("instructions=%v", root["instructions"])
			}
			output := root["output"].([]any)[0].(map[string]any)
			content := output["content"].([]any)[0].(map[string]any)
			if content["text"] != " {\n\"ok\":true\n} " {
				t.Fatalf("output text=%q", content["text"])
			}
			text := root["text"].(map[string]any)
			format := text["format"].(map[string]any)
			if format["type"] != "json_schema" || format["name"] != "result" || format["strict"] != true {
				t.Fatalf("format=%v", format)
			}
			schema := format["schema"].(map[string]any)
			if schema["type"] != "object" || schema["additionalProperties"] != false {
				t.Fatalf("schema=%v", schema)
			}
			_, present := format["description"]
			if present != test.wantPresent {
				t.Fatalf("description present=%v want=%v", present, test.wantPresent)
			}
			if test.wantPresent && format["description"] != *test.description {
				t.Fatalf("description=%v", format["description"])
			}
		})
	}
}

func TestResponseSnapshotOwnsEchoData(t *testing.T) {
	instructions := "original instructions"
	description := "original description"
	schemaBytes := []byte(`{"type":"object","properties":{},"required":[],"additionalProperties":false}`)
	request := core.Request{
		ModelAlias:   "gemini-default",
		Instructions: &instructions,
		Input:        "input",
		Format: core.OutputFormat{
			Type:        core.FormatJSONSchema,
			Name:        "result",
			Description: &description,
			Schema:      schemaBytes,
		},
	}
	snapshot, err := snapshotResponseRequest(request)
	if err != nil {
		t.Fatal(err)
	}

	instructions = "mutated instructions"
	description = "mutated description"
	for i := range schemaBytes {
		schemaBytes[i] = 'x'
	}
	request.ModelAlias = "mutated-alias"

	raw, err := encodeSuccessResponse(
		snapshot,
		core.Result{Text: `{}`},
		"resp_TEST",
		"msg_TEST",
		time.Unix(1, 0),
		time.Unix(2, 0),
		100,
		1<<20,
	)
	if err != nil {
		t.Fatal(err)
	}
	root := decodeJSONObject(t, raw)
	if root["model"] != "gemini-default" || root["instructions"] != "original instructions" {
		t.Fatalf("response=%s", raw)
	}
	format := root["text"].(map[string]any)["format"].(map[string]any)
	if format["description"] != "original description" {
		t.Fatalf("format=%v", format)
	}
}

func TestResponseSnapshotPreservesBoundedSchemaNumbersWithoutFloatNarrowing(t *testing.T) {
	rawRequest := []byte(`{
		"model":"codex-default",
		"input":"hello",
		"text":{"format":{
			"type":"json_schema",
			"name":"result",
			"strict":true,
			"schema":{
				"type":"object",
				"properties":{"value":{
					"type":"number",
					"minimum":1e309,
					"const":1234567890123456789012345678901234567890
				}},
				"required":["value"],
				"additionalProperties":false
			}
		}}
	}`)
	request, apiErr := DecodeRequest(rawRequest, DefaultRequestLimits())
	if apiErr != nil {
		t.Fatalf("decode error=%v", apiErr)
	}

	snapshot, err := snapshotResponseRequest(request)
	if err != nil {
		t.Fatalf("snapshot error=%v", err)
	}
	if !bytes.Equal(snapshot.format.schema, request.Format.Schema) {
		t.Fatalf("schema changed:\n got: %s\nwant: %s", snapshot.format.schema, request.Format.Schema)
	}
	for _, number := range [][]byte{
		[]byte("1e309"),
		[]byte("1234567890123456789012345678901234567890"),
	} {
		if !bytes.Contains(snapshot.format.schema, number) {
			t.Fatalf("schema lost exact number %q: %s", number, snapshot.format.schema)
		}
	}
}

func TestResponseSnapshotRejectsInvalidSchemaRootOrFraming(t *testing.T) {
	for _, test := range []struct {
		name   string
		schema json.RawMessage
	}{
		{name: "non-object", schema: json.RawMessage(`[]`)},
		{name: "malformed", schema: json.RawMessage(`{`)},
		{name: "trailing", schema: json.RawMessage(`{} {}`)},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := snapshotResponseRequest(core.Request{
				ModelAlias: "model",
				Input:      "input",
				Format: core.OutputFormat{
					Type:   core.FormatJSONSchema,
					Name:   "result",
					Schema: test.schema,
				},
			})
			if !errors.Is(err, errResponseEncoding) {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestEncodeSuccessResponseRejectsUnsafeResultOrID(t *testing.T) {
	snapshot, err := snapshotResponseRequest(core.Request{
		ModelAlias: "model",
		Input:      "input",
		Format:     core.OutputFormat{Type: core.FormatText},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name       string
		text       string
		responseID string
		messageID  string
		finalBytes int
	}{
		{name: "empty output", responseID: "resp_TEST", messageID: "msg_TEST", finalBytes: 10},
		{name: "invalid utf8", text: string([]byte{0xff}), responseID: "resp_TEST", messageID: "msg_TEST", finalBytes: 10},
		{name: "over final cap", text: "123", responseID: "resp_TEST", messageID: "msg_TEST", finalBytes: 2},
		{name: "control response id", text: "ok", responseID: "resp_bad\nheader", messageID: "msg_TEST", finalBytes: 10},
		{name: "wrong message prefix", text: "ok", responseID: "resp_TEST", messageID: "req_TEST", finalBytes: 10},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			raw, err := encodeSuccessResponse(
				snapshot,
				core.Result{Text: test.text},
				test.responseID,
				test.messageID,
				time.Unix(1, 0),
				time.Unix(2, 0),
				test.finalBytes,
				1<<20,
			)
			if !errors.Is(err, errResponseEncoding) || raw != nil {
				t.Fatalf("raw=%q error=%v", raw, err)
			}
		})
	}
}

func TestBoundedEncodingAndSuccessCeiling(t *testing.T) {
	limit, err := successResponseBufferLimit(64, 128)
	if err != nil {
		t.Fatal(err)
	}
	if want := 6*(64+128) + 128*1024; limit != want {
		t.Fatalf("limit=%d want=%d", limit, want)
	}
	if _, err := successResponseBufferLimit(-1, 1); !errors.Is(err, errResponseEncoding) {
		t.Fatalf("negative limit error=%v", err)
	}
	if _, err := successResponseBufferLimit(int(^uint(0)>>1), 1); !errors.Is(err, errResponseEncoding) {
		t.Fatalf("overflow error=%v", err)
	}

	raw, err := encodeBounded(struct {
		Value string `json:"value"`
	}{Value: strings.Repeat("<", 20)}, 10)
	if !errors.Is(err, errResponseEncoding) || raw != nil {
		t.Fatalf("raw=%q error=%v", raw, err)
	}
}

func TestSuccessCeilingAcceptsEscapeHeavyBoundedData(t *testing.T) {
	instructions := strings.Repeat("<", 128)
	request := core.Request{
		ModelAlias:   "model",
		Instructions: &instructions,
		Input:        "input",
		Format:       core.OutputFormat{Type: core.FormatText},
	}
	snapshot, err := snapshotResponseRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	limit, err := successResponseBufferLimit(64, 256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := encodeSuccessResponse(
		snapshot,
		core.Result{Text: strings.Repeat("<", 64)},
		"resp_TEST",
		"msg_TEST",
		time.Unix(1, 0),
		time.Unix(2, 0),
		64,
		limit,
	); err != nil {
		t.Fatal(err)
	}
}

func TestEncodeModelsResponseSortsAndUsesFixedShape(t *testing.T) {
	models := []core.Model{
		{ID: "z-model", Provider: core.ProviderGemini, ProviderModel: "z", Created: 42},
		{ID: "a-model", Provider: core.ProviderCodex, ProviderModel: "a"},
	}
	raw, err := encodeModelsResponse(models)
	if err != nil {
		t.Fatal(err)
	}
	root := decodeJSONObject(t, raw)
	if root["object"] != "list" {
		t.Fatalf("response=%s", raw)
	}
	data := root["data"].([]any)
	want := []struct {
		id      string
		created float64
	}{{id: "a-model", created: 0}, {id: "z-model", created: 42}}
	for index, expected := range want {
		model := data[index].(map[string]any)
		if model["id"] != expected.id || model["object"] != "model" ||
			model["created"] != expected.created || model["owned_by"] != "local" {
			t.Fatalf("model[%d]=%v", index, model)
		}
	}

	models[0].ID = "mutated"
	if strings.Contains(string(raw), "mutated") {
		t.Fatalf("response retained caller mutation: %s", raw)
	}
}

func TestPublicAPIErrorCatalogAndEnvelope(t *testing.T) {
	codes := []string{
		core.CodeInvalidJSON,
		core.CodeInvalidRequest,
		core.CodeUnsupportedParameter,
		core.CodeInvalidJSONSchema,
		core.CodeInvalidBearerKey,
		core.CodeNotFound,
		core.CodeModelNotFound,
		core.CodeMethodNotAllowed,
		core.CodeRequestTimeout,
		core.CodeRequestTooLarge,
		core.CodeUnsupportedMediaType,
		core.CodeServerBusy,
		core.CodeQueueFull,
		core.CodeProviderRateLimited,
		core.CodeQueueTimeout,
		core.CodeProviderNotReady,
		core.CodeProviderAuthRequired,
		core.CodeServiceShuttingDown,
		core.CodeProviderTimeout,
		core.CodeOutputLimitExceeded,
		core.CodeProviderProtocolError,
		core.CodeStructuredOutputInvalid,
		core.CodeProviderFailed,
		core.CodeProcessCleanupFailed,
		core.CodeInternalError,
	}

	for _, code := range codes {
		t.Run(code, func(t *testing.T) {
			mapped := publicAPIError(core.Error(code, nil))
			if mapped.CodeValue() != code {
				t.Fatalf("mapped=%s", mapped.CodeValue())
			}
			raw, err := encodeErrorResponse(mapped)
			if err != nil {
				t.Fatal(err)
			}
			root := decodeJSONObject(t, raw)
			envelope := root["error"].(map[string]any)
			if envelope["message"] != mapped.MessageText() ||
				envelope["type"] != mapped.TypeName() ||
				envelope["code"] != code || envelope["param"] != nil {
				t.Fatalf("envelope=%v", envelope)
			}
		})
	}
}

func TestPublicAPIErrorAllowsOnlyCodeCompatibleParameters(t *testing.T) {
	tests := []struct {
		name      string
		code      string
		param     string
		wantCode  string
		wantParam *string
	}{
		{name: "request field", code: core.CodeInvalidRequest, param: "text.format.type", wantCode: core.CodeInvalidRequest, wantParam: stringPointer("text.format.type")},
		{name: "size field", code: core.CodeRequestTooLarge, param: "instructions", wantCode: core.CodeRequestTooLarge, wantParam: stringPointer("instructions")},
		{name: "fixed query", code: core.CodeUnsupportedParameter, param: "query", wantCode: core.CodeUnsupportedParameter, wantParam: stringPointer("query")},
		{name: "query wrong code", code: core.CodeInvalidRequest, param: "query", wantCode: core.CodeInternalError},
		{name: "field wrong code", code: core.CodeProviderFailed, param: "model", wantCode: core.CodeInternalError},
		{name: "attacker parameter", code: core.CodeUnsupportedParameter, param: "query.planted-secret", wantCode: core.CodeInternalError},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := publicAPIError(core.Error(test.code, &test.param))
			if mapped.CodeValue() != test.wantCode {
				t.Fatalf("code=%s want=%s", mapped.CodeValue(), test.wantCode)
			}
			gotParam := mapped.ParamValue()
			if !equalStringPointers(gotParam, test.wantParam) {
				t.Fatalf("param=%v want=%v", gotParam, test.wantParam)
			}
			if strings.Contains(mapped.Error(), "planted-secret") {
				t.Fatalf("secret in error=%q", mapped.Error())
			}
		})
	}
}

func TestPublicAPIErrorUsesOnlyApprovedProvenance(t *testing.T) {
	api := core.Error(core.CodeProviderFailed, nil)
	meta := core.ResultMeta{
		Provider:        core.ProviderClaude,
		StdoutBytes:     10,
		StderrBytes:     4,
		ProviderVersion: "2.1.208",
		ExitCategory:    "nonzero_exit",
		StopReason:      "completed",
		StopAction:      "none",
	}
	outcome, err := core.NewOutcomeError(api, meta)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name     string
		err      error
		wantCode string
	}{
		{name: "api", err: api, wantCode: core.CodeProviderFailed},
		{name: "wrapped api", err: fmt.Errorf("planted wrapper secret: %w", api), wantCode: core.CodeProviderFailed},
		{name: "outcome", err: outcome, wantCode: core.CodeProviderFailed},
		{name: "raw error", err: errors.New("planted raw secret"), wantCode: core.CodeInternalError},
		{name: "forged metadata method", err: forgedOutcomeError{}, wantCode: core.CodeInternalError},
		{name: "exact deadline", err: context.DeadlineExceeded, wantCode: core.CodeRequestTimeout},
		{name: "wrapped deadline rejected", err: fmt.Errorf("planted deadline secret: %w", context.DeadlineExceeded), wantCode: core.CodeInternalError},
		{name: "raw cancel", err: context.Canceled, wantCode: core.CodeInternalError},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			mapped := publicAPIError(test.err)
			if mapped.CodeValue() != test.wantCode {
				t.Fatalf("code=%s want=%s", mapped.CodeValue(), test.wantCode)
			}
			raw, encodeErr := encodeErrorResponse(mapped)
			if encodeErr != nil {
				t.Fatal(encodeErr)
			}
			if strings.Contains(string(raw), "planted") {
				t.Fatalf("secret in envelope=%s", raw)
			}
		})
	}

	gotMeta, ok := outcomeResultMetadata(fmt.Errorf("wrapper: %w", outcome))
	if !ok || gotMeta != meta {
		t.Fatalf("meta=%+v ok=%v", gotMeta, ok)
	}
	if _, ok := outcomeResultMetadata(forgedOutcomeError{}); ok {
		t.Fatal("accepted duck-typed metadata")
	}
}

func TestOutcomeDeadlineMapsToRequestTimeout(t *testing.T) {
	outcome, err := core.NewOutcomeError(context.DeadlineExceeded, core.ResultMeta{})
	if err != nil {
		t.Fatal(err)
	}
	if got := publicAPIError(outcome).CodeValue(); got != core.CodeRequestTimeout {
		t.Fatalf("code=%s", got)
	}
}

func decodeJSONObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(raw, &value); err != nil {
		t.Fatalf("decode %q: %v", raw, err)
	}
	return value
}

func objectsEqual(left, right map[string]any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func stringPointer(value string) *string {
	return &value
}

type forgedOutcomeError struct{}

func (forgedOutcomeError) Error() string { return "planted forged metadata secret" }

func (forgedOutcomeError) ResultMetadata() core.ResultMeta {
	return core.ResultMeta{ProviderVersion: "planted"}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) {
	return 0, errors.New("planted reader failure")
}

type countingReader struct {
	reader io.Reader
	total  int
}

func (r *countingReader) Read(p []byte) (int, error) {
	n, err := r.reader.Read(p)
	r.total += n
	return n, err
}
