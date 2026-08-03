package httpapi

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base32"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

// IDSource supplies opaque application identifiers. Production callers use the
// fixed req, resp, and msg domains; tests may inject a deterministic source.
type IDSource interface {
	Next(prefix string) string
}

var (
	errOpaqueIDSource   = errors.New("opaque ID source initialization failed")
	errResponseEncoding = errors.New("response encoding failed")
)

const (
	responseEncodingOverhead = 128 * 1024
	errorResponseBufferLimit = 4 * 1024
)

type opaqueIDSource struct {
	seed    [sha256.Size]byte
	counter atomic.Uint64
}

var opaqueIDEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewOpaqueIDSource reads one 256-bit seed. ID generation cannot fail after
// construction and does not use request data.
func NewOpaqueIDSource(reader io.Reader) (IDSource, error) {
	if reader == nil {
		return nil, errOpaqueIDSource
	}
	var source opaqueIDSource
	if _, err := io.ReadFull(reader, source.seed[:]); err != nil {
		return nil, errOpaqueIDSource
	}
	return &source, nil
}

func (s *opaqueIDSource) Next(prefix string) string {
	counter := s.counter.Add(1)
	var encodedCounter [8]byte
	binary.BigEndian.PutUint64(encodedCounter[:], counter)

	digest := hmac.New(sha256.New, s.seed[:])
	_, _ = digest.Write([]byte(prefix))
	_, _ = digest.Write([]byte{0})
	_, _ = digest.Write(encodedCounter[:])
	sum := digest.Sum(nil)

	encoded := opaqueIDEncoding.EncodeToString(sum[:16])
	return prefix + "_" + strings.ToLower(encoded)
}

type responseRequestSnapshot struct {
	model        string
	instructions *string
	format       responseFormatSnapshot
}

type responseFormatSnapshot struct {
	typ         core.FormatType
	name        string
	description *string
	schema      json.RawMessage
}

type responseDTO struct {
	ID                 string               `json:"id"`
	Object             string               `json:"object"`
	CreatedAt          int64                `json:"created_at"`
	CompletedAt        int64                `json:"completed_at"`
	Status             string               `json:"status"`
	Background         bool                 `json:"background"`
	Error              any                  `json:"error"`
	IncompleteDetails  any                  `json:"incomplete_details"`
	Instructions       *string              `json:"instructions"`
	Model              string               `json:"model"`
	Output             []responseMessageDTO `json:"output"`
	ParallelToolCalls  bool                 `json:"parallel_tool_calls"`
	PreviousResponseID any                  `json:"previous_response_id"`
	Store              bool                 `json:"store"`
	Text               responseTextDTO      `json:"text"`
	Tools              []struct{}           `json:"tools"`
	ToolChoice         string               `json:"tool_choice"`
}

type responseMessageDTO struct {
	ID      string               `json:"id"`
	Type    string               `json:"type"`
	Status  string               `json:"status"`
	Role    string               `json:"role"`
	Content []responseContentDTO `json:"content"`
}

type responseContentDTO struct {
	Type        string     `json:"type"`
	Annotations []struct{} `json:"annotations"`
	Text        string     `json:"text"`
}

type responseTextDTO struct {
	Format any `json:"format"`
}

type textFormatDTO struct {
	Type string `json:"type"`
}

type schemaFormatDTO struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description *string         `json:"description,omitempty"`
	Strict      bool            `json:"strict"`
	Schema      json.RawMessage `json:"schema"`
}

type modelListDTO struct {
	Object string     `json:"object"`
	Data   []modelDTO `json:"data"`
}

type modelDTO struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

type errorEnvelopeDTO struct {
	Error errorDTO `json:"error"`
}

type errorDTO struct {
	Message string  `json:"message"`
	Type    string  `json:"type"`
	Param   *string `json:"param"`
	Code    string  `json:"code"`
}

func snapshotResponseRequest(request core.Request) (responseRequestSnapshot, error) {
	if request.ModelAlias == "" || !utf8.ValidString(request.ModelAlias) {
		return responseRequestSnapshot{}, errResponseEncoding
	}

	snapshot := responseRequestSnapshot{
		model: request.ModelAlias,
		format: responseFormatSnapshot{
			typ: request.Format.Type,
		},
	}
	if request.Instructions != nil {
		value := *request.Instructions
		snapshot.instructions = &value
	}

	switch request.Format.Type {
	case core.FormatText:
		if request.Format.Name != "" || request.Format.Description != nil || len(request.Format.Schema) != 0 {
			return responseRequestSnapshot{}, errResponseEncoding
		}
	case core.FormatJSONSchema:
		if !formatNamePattern.MatchString(request.Format.Name) || len(request.Format.Schema) == 0 {
			return responseRequestSnapshot{}, errResponseEncoding
		}
		decoder := json.NewDecoder(bytes.NewReader(request.Format.Schema))
		decoder.UseNumber()
		var schemaValue any
		if err := decoder.Decode(&schemaValue); err != nil {
			return responseRequestSnapshot{}, errResponseEncoding
		}
		if _, ok := schemaValue.(map[string]any); !ok {
			return responseRequestSnapshot{}, errResponseEncoding
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return responseRequestSnapshot{}, errResponseEncoding
		}
		snapshot.format.name = request.Format.Name
		if request.Format.Description != nil {
			value := *request.Format.Description
			snapshot.format.description = &value
		}
		snapshot.format.schema = append(json.RawMessage(nil), request.Format.Schema...)
	default:
		return responseRequestSnapshot{}, errResponseEncoding
	}
	return snapshot, nil
}

func successResponseBufferLimit(finalBytes int, bodyBytes int64) (int, error) {
	if finalBytes <= 0 || bodyBytes <= 0 {
		return 0, errResponseEncoding
	}
	maxInt := int64(^uint(0) >> 1)
	if bodyBytes > maxInt || int64(finalBytes) > maxInt-bodyBytes {
		return 0, errResponseEncoding
	}
	sum := int64(finalBytes) + bodyBytes
	if sum > (maxInt-responseEncodingOverhead)/6 {
		return 0, errResponseEncoding
	}
	return int(6*sum + responseEncodingOverhead), nil
}

func encodeSuccessResponse(
	snapshot responseRequestSnapshot,
	result core.Result,
	responseID string,
	messageID string,
	createdAt time.Time,
	completedAt time.Time,
	finalBytes int,
	bufferLimit int,
) ([]byte, error) {
	if !validApplicationID(responseID, "resp") ||
		!validApplicationID(messageID, "msg") ||
		finalBytes <= 0 ||
		result.Text == "" ||
		!utf8.ValidString(result.Text) ||
		len(result.Text) > finalBytes {
		return nil, errResponseEncoding
	}

	var format any
	switch snapshot.format.typ {
	case core.FormatText:
		format = textFormatDTO{Type: string(core.FormatText)}
	case core.FormatJSONSchema:
		format = schemaFormatDTO{
			Type:        string(core.FormatJSONSchema),
			Name:        snapshot.format.name,
			Description: cloneStringPointer(snapshot.format.description),
			Strict:      true,
			Schema:      append(json.RawMessage(nil), snapshot.format.schema...),
		}
	default:
		return nil, errResponseEncoding
	}

	response := responseDTO{
		ID:                 responseID,
		Object:             "response",
		CreatedAt:          createdAt.Unix(),
		CompletedAt:        completedAt.Unix(),
		Status:             "completed",
		Background:         false,
		Error:              nil,
		IncompleteDetails:  nil,
		Instructions:       cloneStringPointer(snapshot.instructions),
		Model:              snapshot.model,
		ParallelToolCalls:  false,
		PreviousResponseID: nil,
		Store:              false,
		Text:               responseTextDTO{Format: format},
		Tools:              make([]struct{}, 0),
		ToolChoice:         "none",
		Output: []responseMessageDTO{{
			ID:     messageID,
			Type:   "message",
			Status: "completed",
			Role:   "assistant",
			Content: []responseContentDTO{{
				Type:        "output_text",
				Annotations: make([]struct{}, 0),
				Text:        result.Text,
			}},
		}},
	}
	return encodeBounded(response, bufferLimit)
}

func encodeModelsResponse(models []core.Model) ([]byte, error) {
	registry, err := core.NewRegistry(models)
	if err != nil {
		return nil, errResponseEncoding
	}
	sorted := registry.Models()
	data := make([]modelDTO, len(sorted))
	for index, model := range sorted {
		if model.Created < 0 {
			return nil, errResponseEncoding
		}
		data[index] = modelDTO{
			ID:      model.ID,
			Object:  "model",
			Created: model.Created,
			OwnedBy: "local",
		}
	}

	limit, ok := modelResponseBufferLimit(sorted)
	if !ok {
		return nil, errResponseEncoding
	}
	return encodeBounded(modelListDTO{Object: "list", Data: data}, limit)
}

func modelResponseBufferLimit(models []core.Model) (int, bool) {
	limit := 128
	maxInt := int(^uint(0) >> 1)
	for _, model := range models {
		entry := 6*len(model.ID) + 128
		if entry < 0 || limit > maxInt-entry {
			return 0, false
		}
		limit += entry
	}
	return limit, true
}

func encodeErrorResponse(apiErr *core.APIError) ([]byte, error) {
	canonical := publicAPIError(apiErr)
	return encodeBounded(errorEnvelopeDTO{Error: errorDTO{
		Message: canonical.MessageText(),
		Type:    canonical.TypeName(),
		Param:   canonical.ParamValue(),
		Code:    canonical.CodeValue(),
	}}, errorResponseBufferLimit)
}

func publicAPIError(err error) *core.APIError {
	if err == nil {
		return core.Error(core.CodeInternalError, nil)
	}
	// Exact identity is deliberate. A raw wrapper may carry sensitive text and
	// is not an approved deadline provenance boundary.
	//nolint:errorlint
	if err == context.DeadlineExceeded {
		return core.Error(core.CodeRequestTimeout, nil)
	}

	var outcome *core.OutcomeError
	if errors.As(err, &outcome) {
		// OutcomeError constructors accept only an exact safe direct cause.
		//nolint:errorlint
		if errors.Unwrap(outcome) == context.DeadlineExceeded {
			return core.Error(core.CodeRequestTimeout, nil)
		}
	}

	var apiErr *core.APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return core.Error(core.CodeInternalError, nil)
	}
	code := apiErr.CodeValue()
	param := apiErr.ParamValue()
	if !validPublicErrorParam(code, param) {
		return core.Error(core.CodeInternalError, nil)
	}
	return core.Error(code, param)
}

func outcomeResultMetadata(err error) (core.ResultMeta, bool) {
	var outcome *core.OutcomeError
	if !errors.As(err, &outcome) || outcome == nil {
		return core.ResultMeta{}, false
	}
	return outcome.ResultMetadata(), true
}

var invalidRequestParams = stringSet(
	"model",
	"input",
	"instructions",
	"stream",
	"store",
	"tools",
	"tool_choice",
	"text",
	"text.format",
	"text.format.type",
	"text.format.name",
	"text.format.description",
	"text.format.strict",
	"text.format.schema",
)

var unsupportedParams = stringSet(
	"stream",
	"store",
	"tools",
	"tool_choice",
	"text",
	"text.format",
	"text.format.type",
	"text.format.name",
	"text.format.strict",
	"query",
)

var tooLargeParams = stringSet(
	"input",
	"instructions",
	"text.format.schema",
)

func validPublicErrorParam(code string, param *string) bool {
	if param == nil {
		return true
	}
	var allowed map[string]struct{}
	switch code {
	case core.CodeInvalidRequest:
		allowed = invalidRequestParams
	case core.CodeUnsupportedParameter:
		allowed = unsupportedParams
	case core.CodeInvalidJSONSchema:
		allowed = stringSet("text.format.schema")
	case core.CodeRequestTooLarge:
		allowed = tooLargeParams
	default:
		return false
	}
	_, ok := allowed[*param]
	return ok
}

func stringSet(values ...string) map[string]struct{} {
	set := make(map[string]struct{}, len(values))
	for _, value := range values {
		set[value] = struct{}{}
	}
	return set
}

func validApplicationID(id string, prefix string) bool {
	wantPrefix := prefix + "_"
	if !strings.HasPrefix(id, wantPrefix) {
		return false
	}
	suffix := id[len(wantPrefix):]
	if len(suffix) == 0 || len(suffix) > 80 {
		return false
	}
	for index := range len(suffix) {
		character := suffix[index]
		if (character < 'a' || character > 'z') &&
			(character < 'A' || character > 'Z') &&
			(character < '0' || character > '9') &&
			character != '_' && character != '-' {
			return false
		}
	}
	return true
}

type boundedJSONBuffer struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedJSONBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.buffer.Len() {
		return 0, errResponseEncoding
	}
	return b.buffer.Write(data)
}

func encodeBounded(value any, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, errResponseEncoding
	}
	buffer := &boundedJSONBuffer{limit: limit}
	encoder := json.NewEncoder(buffer)
	encoder.SetEscapeHTML(true)
	if err := encoder.Encode(value); err != nil {
		return nil, errResponseEncoding
	}
	return append([]byte(nil), buffer.buffer.Bytes()...), nil
}

func cloneStringPointer(value *string) *string {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}
