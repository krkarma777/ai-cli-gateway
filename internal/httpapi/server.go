package httpapi

import (
	"context"
	"errors"
	"io"
	"log"
	"log/slog"
	"mime"
	"net"
	"net/http"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/gatewaykey"
	"github.com/krkarma777/ai-cli-gateway/internal/observability"
)

const (
	maxHTTPBodyBytes       = int64(16 << 20)
	maxRequestFieldBytes   = 16 << 20
	maxRequestSchemaBytes  = 1 << 20
	maxHandlerLimit        = 4096
	maxBodyReaderLimit     = 256
	maxHTTPHeaderBytes     = 1 << 20
	maxHTTPServerTimeout   = 24 * time.Hour
	maxFinalResponseBytes  = 16 << 20
	maxConfiguredModels    = 1024
	responsesEndpoint      = "/v1/responses"
	modelsEndpoint         = "/v1/models"
	unmatchedEndpoint      = "unmatched"
	applicationContentType = "application/json"
	requestIDPrefix        = "req_"
	requestIDSuffixBytes   = 26
)

var errServerConfiguration = errors.New("HTTP server configuration is invalid")

// Backend is the provider-neutral HTTP application boundary.
type Backend interface {
	Respond(context.Context, core.Request) (core.Result, error)
	Models() []core.Model
}

// Config contains the complete bounded HTTP server configuration.
type Config struct {
	Listen            string
	GatewayAuth       gatewaykey.Snapshot
	HTTPBodyBytes     int64
	RequestLimits     RequestLimits
	HandlerLimit      int
	BodyReaderLimit   int
	MaxHeaderBytes    int
	ReadHeaderTimeout time.Duration
	BodyReadTimeout   time.Duration
	IdleTimeout       time.Duration
	FinalBytes        int
	MaxModels         int
}

// CounterSink receives closed numeric cancellation observations only.
type CounterSink interface {
	ClientCanceled()
}

// Dependencies contains trusted process-local HTTP dependencies.
type Dependencies struct {
	Now      func() time.Time
	IDs      IDSource
	Counters CounterSink
}

type applicationHandler struct {
	config          Config
	auth            authenticator
	backend         Backend
	now             func() time.Time
	ids             IDSource
	counters        CounterSink
	logger          *slog.Logger
	handlerGate     chan struct{}
	bodyReaderGate  chan struct{}
	allowedHost     string
	localhostHost   string
	modelsBody      []byte
	providerByAlias map[string]core.ProviderName
	successLimit    int
}

// New validates the complete HTTP boundary, freezes the model snapshot, and
// returns an unstarted net/http server plus its exact application handler.
func New(
	config Config,
	dependencies Dependencies,
	backend Backend,
	logger *slog.Logger,
) (*http.Server, http.Handler, error) {
	port, ok := validateServerConfig(config)
	if !ok ||
		!config.GatewayAuth.Valid() ||
		isNilInterface(backend) ||
		dependencies.Now == nil ||
		isNilInterface(dependencies.IDs) ||
		isNilInterface(dependencies.Counters) ||
		logger == nil {
		return nil, nil, errServerConfiguration
	}

	models := backend.Models()
	if len(models) == 0 || len(models) > config.MaxModels {
		return nil, nil, errServerConfiguration
	}
	for _, model := range models {
		if model.Created < 0 {
			return nil, nil, errServerConfiguration
		}
	}
	registry, err := core.NewRegistry(models)
	if err != nil {
		return nil, nil, errServerConfiguration
	}
	frozenModels := registry.Models()
	modelsBody, err := encodeModelsResponse(frozenModels)
	if err != nil {
		return nil, nil, errServerConfiguration
	}
	successLimit, err := successResponseBufferLimit(
		config.FinalBytes,
		config.HTTPBodyBytes,
	)
	if err != nil {
		return nil, nil, errServerConfiguration
	}

	providerByAlias := make(map[string]core.ProviderName, len(frozenModels))
	for _, model := range frozenModels {
		providerByAlias[model.ID] = model.Provider
	}

	handler := &applicationHandler{
		config:          config,
		auth:            authenticator{snapshot: config.GatewayAuth},
		backend:         backend,
		now:             dependencies.Now,
		ids:             dependencies.IDs,
		counters:        dependencies.Counters,
		logger:          logger,
		handlerGate:     make(chan struct{}, config.HandlerLimit),
		bodyReaderGate:  make(chan struct{}, config.BodyReaderLimit),
		allowedHost:     config.Listen,
		localhostHost:   net.JoinHostPort("localhost", port),
		modelsBody:      append([]byte(nil), modelsBody...),
		providerByAlias: providerByAlias,
		successLimit:    successLimit,
	}
	server := &http.Server{
		Addr:                         config.Listen,
		Handler:                      handler,
		DisableGeneralOptionsHandler: true,
		ReadHeaderTimeout:            config.ReadHeaderTimeout,
		IdleTimeout:                  config.IdleTimeout,
		MaxHeaderBytes:               config.MaxHeaderBytes,
		ErrorLog:                     log.New(io.Discard, "", 0),
	}
	return server, handler, nil
}

func validateServerConfig(config Config) (string, bool) {
	host, port, err := net.SplitHostPort(config.Listen)
	if err != nil || host == "" || !decimalPort(port) {
		return "", false
	}
	parsedPort, err := strconv.ParseUint(port, 10, 16)
	if err != nil || parsedPort == 0 {
		return "", false
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return "", false
	}
	limits := config.RequestLimits
	if config.HTTPBodyBytes <= 0 || config.HTTPBodyBytes > maxHTTPBodyBytes ||
		ValidateRequestLimits(limits) != nil ||
		limits.InputBytes > maxRequestFieldBytes ||
		limits.InstructionsBytes > maxRequestFieldBytes ||
		limits.SchemaBytes > maxRequestSchemaBytes ||
		int64(limits.InputBytes) > config.HTTPBodyBytes ||
		int64(limits.InstructionsBytes) > config.HTTPBodyBytes ||
		int64(limits.SchemaBytes) > config.HTTPBodyBytes ||
		limits.MaxDepth != defaultMaxDepth ||
		limits.MaxNumberBytes != defaultMaxNumberBytes ||
		config.HandlerLimit <= 0 || config.HandlerLimit > maxHandlerLimit ||
		config.BodyReaderLimit <= 0 || config.BodyReaderLimit > maxBodyReaderLimit ||
		config.BodyReaderLimit > config.HandlerLimit ||
		config.MaxHeaderBytes <= 0 || config.MaxHeaderBytes > maxHTTPHeaderBytes ||
		!validServerTimeout(config.ReadHeaderTimeout) ||
		!validServerTimeout(config.BodyReadTimeout) ||
		!validServerTimeout(config.IdleTimeout) ||
		config.FinalBytes <= 0 || config.FinalBytes > maxFinalResponseBytes ||
		config.MaxModels <= 0 || config.MaxModels > maxConfiguredModels {
		return "", false
	}
	if _, err := successResponseBufferLimit(config.FinalBytes, config.HTTPBodyBytes); err != nil {
		return "", false
	}
	return port, true
}

func decimalPort(port string) bool {
	if port == "" {
		return false
	}
	for index := range len(port) {
		if port[index] < '0' || port[index] > '9' {
			return false
		}
	}
	return true
}

func validServerTimeout(timeout time.Duration) bool {
	return timeout > 0 && timeout <= maxHTTPServerTimeout
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	kind := reflected.Kind()
	return (kind == reflect.Chan || kind == reflect.Func ||
		kind == reflect.Interface || kind == reflect.Map ||
		kind == reflect.Pointer || kind == reflect.Slice) && reflected.IsNil()
}

func (h *applicationHandler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	requestID := h.ids.Next("req")
	if !validGeneratedRequestID(requestID) {
		return
	}
	endpoint := unmatchedEndpoint
	if !tryAcquire(h.handlerGate) {
		h.publishUnreadError(writer, request, requestID, endpoint, core.Error(core.CodeServerBusy, nil), core.ResultMeta{}, false, "", "", 0)
		return
	}
	defer releaseGate(h.handlerGate)

	if request.Host != h.allowedHost && request.Host != h.localhostHost {
		h.publishUnreadError(writer, request, requestID, endpoint, core.Error(core.CodeInvalidRequest, nil), core.ResultMeta{}, false, "", "", 0)
		return
	}
	if request.URL.RawQuery != "" || request.URL.ForceQuery {
		param := "query"
		h.publishUnreadError(writer, request, requestID, endpoint, core.Error(core.CodeUnsupportedParameter, &param), core.ResultMeta{}, false, "", "", 0)
		return
	}
	if !h.auth.authorized(request.Header) {
		h.publishUnreadError(writer, request, requestID, endpoint, core.Error(core.CodeInvalidBearerKey, nil), core.ResultMeta{}, false, "", "", 0)
		return
	}

	path := request.URL.EscapedPath()
	endpoint = classifyEndpoint(path)
	switch path {
	case responsesEndpoint:
		if request.Method != http.MethodPost {
			h.publishUnreadError(writer, request, requestID, endpoint, core.Error(core.CodeMethodNotAllowed, nil), core.ResultMeta{}, false, "", "", 0)
			return
		}
	case modelsEndpoint:
		if request.Method != http.MethodGet {
			h.publishUnreadError(writer, request, requestID, endpoint, core.Error(core.CodeMethodNotAllowed, nil), core.ResultMeta{}, false, "", "", 0)
			return
		}
	default:
		h.publishUnreadError(writer, request, requestID, endpoint, core.Error(core.CodeNotFound, nil), core.ResultMeta{}, false, "", "", 0)
		return
	}

	if !validContentEncoding(request.Header) {
		h.publishUnreadError(writer, request, requestID, endpoint, core.Error(core.CodeUnsupportedMediaType, nil), core.ResultMeta{}, false, "", "", 0)
		return
	}

	switch path {
	case responsesEndpoint:
		h.handleResponses(writer, request, requestID)
	case modelsEndpoint:
		h.publishUnread(writer, request, requestID, endpoint, http.StatusOK, h.modelsBody, observability.RequestEvent{})
	}
}

func (h *applicationHandler) handleResponses(
	writer http.ResponseWriter,
	request *http.Request,
	requestID string,
) {
	if !validJSONContentType(request.Header) {
		h.publishUnreadError(writer, request, requestID, responsesEndpoint, core.Error(core.CodeUnsupportedMediaType, nil), core.ResultMeta{}, false, "", "", 0)
		return
	}
	if !tryAcquire(h.bodyReaderGate) {
		h.publishUnreadError(writer, request, requestID, responsesEndpoint, core.Error(core.CodeServerBusy, nil), core.ResultMeta{}, false, "", "", 0)
		return
	}
	normalized, apiErr, canceled, bodyDeadline := h.readAndDecode(writer, request)
	releaseGate(h.bodyReaderGate)
	if canceled {
		h.counters.ClientCanceled()
		return
	}
	if apiErr != nil {
		h.publishErrorWithCancellationPolicy(
			writer,
			request,
			requestID,
			responsesEndpoint,
			apiErr,
			core.ResultMeta{},
			false,
			"",
			"",
			0,
			!bodyDeadline,
		)
		return
	}

	snapshot, err := snapshotResponseRequest(normalized)
	if err != nil {
		h.publishError(writer, request, requestID, responsesEndpoint, core.Error(core.CodeInternalError, nil), core.ResultMeta{}, false, "", "", 0)
		return
	}
	modelAlias, providerName, knownModel := h.loggingIdentity(normalized.ModelAlias)
	if request.Context().Err() != nil {
		h.counters.ClientCanceled()
		return
	}
	createdAt := h.now()
	result, backendErr := h.backend.Respond(request.Context(), normalized)
	completedAt := h.now()
	if backendErr != nil {
		metadata, hasMetadata := outcomeResultMetadata(backendErr)
		h.publishError(
			writer,
			request,
			requestID,
			responsesEndpoint,
			backendErr,
			metadata,
			hasMetadata,
			modelAlias,
			providerName,
			len(normalized.Input),
		)
		return
	}

	responseID := h.ids.Next("resp")
	messageID := h.ids.Next("msg")
	body, err := encodeSuccessResponse(
		snapshot,
		result,
		responseID,
		messageID,
		createdAt,
		completedAt,
		h.config.FinalBytes,
		h.successLimit,
	)
	if err != nil {
		h.publishError(
			writer,
			request,
			requestID,
			responsesEndpoint,
			core.Error(core.CodeInternalError, nil),
			core.ResultMeta{},
			false,
			modelAlias,
			providerName,
			len(normalized.Input),
		)
		return
	}
	event := requestEventFromMeta(result.Meta)
	if knownModel {
		event.ModelAlias = modelAlias
		event.Provider = core.ProviderName(providerName)
	}
	event.InputBytes = len(normalized.Input)
	event.FinalBytes = len(result.Text)
	h.publish(writer, request, requestID, responsesEndpoint, http.StatusOK, body, event)
}

func (h *applicationHandler) readAndDecode(
	writer http.ResponseWriter,
	request *http.Request,
) (core.Request, *core.APIError, bool, bool) {
	if request.Context().Err() != nil {
		// Cancellation is deliberately returned out of band so no error text can
		// reach the public response path.
		//nolint:nilerr
		return core.Request{}, nil, true, false
	}
	controller := http.NewResponseController(writer)
	deadlineInstalled := controller.SetReadDeadline(
		h.now().Add(h.config.BodyReadTimeout),
	) == nil
	body := http.MaxBytesReader(writer, request.Body, h.config.HTTPBodyBytes)
	raw, readErr := io.ReadAll(body)
	_ = body.Close()
	if deadlineInstalled {
		_ = controller.SetReadDeadline(time.Time{})
	}
	if readErr != nil {
		serverDeadline := deadlineInstalled && errors.Is(readErr, os.ErrDeadlineExceeded)
		if !serverDeadline && request.Context().Err() != nil {
			// Cancellation is deliberately returned out of band so the caller can
			// suppress every response mutation.
			//nolint:nilerr
			return core.Request{}, nil, true, false
		}
		var timeout interface{ Timeout() bool }
		if serverDeadline || (errors.As(readErr, &timeout) && timeout.Timeout()) {
			return core.Request{}, core.Error(core.CodeRequestTimeout, nil), false, serverDeadline
		}
		var tooLarge *http.MaxBytesError
		if errors.As(readErr, &tooLarge) {
			return core.Request{}, core.Error(core.CodeRequestTooLarge, nil), false, false
		}
		return core.Request{}, core.Error(core.CodeInvalidJSON, nil), false, false
	}
	if request.Context().Err() != nil {
		// Cancellation is deliberately returned out of band so no error text can
		// reach the public response path.
		//nolint:nilerr
		return core.Request{}, nil, true, false
	}
	normalized, apiErr := DecodeRequest(raw, h.config.RequestLimits)
	return normalized, apiErr, false, false
}

func (h *applicationHandler) publishError(
	writer http.ResponseWriter,
	request *http.Request,
	requestID string,
	endpoint string,
	failure error,
	metadata core.ResultMeta,
	hasMetadata bool,
	modelAlias string,
	providerName string,
	inputBytes int,
) {
	h.publishErrorWithPolicies(
		writer,
		request,
		requestID,
		endpoint,
		failure,
		metadata,
		hasMetadata,
		modelAlias,
		providerName,
		inputBytes,
		true,
		false,
	)
}

func (h *applicationHandler) publishUnreadError(
	writer http.ResponseWriter,
	request *http.Request,
	requestID string,
	endpoint string,
	failure error,
	metadata core.ResultMeta,
	hasMetadata bool,
	modelAlias string,
	providerName string,
	inputBytes int,
) {
	h.publishErrorWithPolicies(
		writer,
		request,
		requestID,
		endpoint,
		failure,
		metadata,
		hasMetadata,
		modelAlias,
		providerName,
		inputBytes,
		true,
		true,
	)
}

func (h *applicationHandler) publishErrorWithCancellationPolicy(
	writer http.ResponseWriter,
	request *http.Request,
	requestID string,
	endpoint string,
	failure error,
	metadata core.ResultMeta,
	hasMetadata bool,
	modelAlias string,
	providerName string,
	inputBytes int,
	suppressWhenCanceled bool,
) {
	h.publishErrorWithPolicies(
		writer,
		request,
		requestID,
		endpoint,
		failure,
		metadata,
		hasMetadata,
		modelAlias,
		providerName,
		inputBytes,
		suppressWhenCanceled,
		false,
	)
}

func (h *applicationHandler) publishErrorWithPolicies(
	writer http.ResponseWriter,
	request *http.Request,
	requestID string,
	endpoint string,
	failure error,
	metadata core.ResultMeta,
	hasMetadata bool,
	modelAlias string,
	providerName string,
	inputBytes int,
	suppressWhenCanceled bool,
	disposeUnreadBody bool,
) {
	apiErr := publicAPIError(failure)
	body, err := encodeErrorResponse(apiErr)
	if err != nil {
		apiErr = core.Error(core.CodeInternalError, nil)
		body, err = encodeErrorResponse(apiErr)
		if err != nil {
			return
		}
	}
	event := observability.RequestEvent{
		ModelAlias: modelAlias,
		Provider:   core.ProviderName(providerName),
		InputBytes: inputBytes,
		ErrorCode:  apiErr.CodeValue(),
	}
	if hasMetadata {
		metaEvent := requestEventFromMeta(metadata)
		event.StdoutBytes = metaEvent.StdoutBytes
		event.StderrBytes = metaEvent.StderrBytes
		event.QueueDepth = metaEvent.QueueDepth
		event.RunningCount = metaEvent.RunningCount
		event.QueueDuration = metaEvent.QueueDuration
		event.ExecutionTime = metaEvent.ExecutionTime
		event.ExitCategory = metaEvent.ExitCategory
		event.ProviderVersion = metaEvent.ProviderVersion
		event.StopReason = metaEvent.StopReason
		event.StopAction = metaEvent.StopAction
	}
	h.publishWithPolicies(
		writer,
		request,
		requestID,
		endpoint,
		apiErr.StatusCode(),
		body,
		event,
		suppressWhenCanceled,
		disposeUnreadBody,
	)
}

func (h *applicationHandler) publish(
	writer http.ResponseWriter,
	request *http.Request,
	requestID string,
	endpoint string,
	status int,
	body []byte,
	event observability.RequestEvent,
) {
	h.publishWithPolicies(
		writer,
		request,
		requestID,
		endpoint,
		status,
		body,
		event,
		true,
		false,
	)
}

func (h *applicationHandler) publishUnread(
	writer http.ResponseWriter,
	request *http.Request,
	requestID string,
	endpoint string,
	status int,
	body []byte,
	event observability.RequestEvent,
) {
	h.publishWithPolicies(
		writer,
		request,
		requestID,
		endpoint,
		status,
		body,
		event,
		true,
		true,
	)
}

func (h *applicationHandler) publishWithPolicies(
	writer http.ResponseWriter,
	request *http.Request,
	requestID string,
	endpoint string,
	status int,
	body []byte,
	event observability.RequestEvent,
	suppressWhenCanceled bool,
	disposeUnreadBody bool,
) {
	if suppressWhenCanceled && request.Context().Err() != nil {
		h.counters.ClientCanceled()
		return
	}
	if disposeUnreadBody {
		disposeUnreadRequestBody(writer, request)
	}
	header := writer.Header()
	header.Set("Content-Type", applicationContentType)
	header.Set("X-Request-ID", requestID)
	writer.WriteHeader(status)
	_, _ = writer.Write(body)
	event.RequestID = requestID
	event.Endpoint = endpoint
	event.Status = status
	observability.LogRequest(h.logger, event)
}

func disposeUnreadRequestBody(writer http.ResponseWriter, request *http.Request) {
	if request.Body == nil || request.Body == http.NoBody {
		return
	}
	writer.Header().Set("Connection", "close")
	controller := http.NewResponseController(writer)
	if controller.SetReadDeadline(time.Unix(1, 0)) == nil {
		// The deliberately expired deadline makes the standard-library request
		// body's bounded early-close drain nonblocking. It stays installed because
		// this response has already made the connection ineligible for reuse.
		_ = request.Body.Close()
	}
}

func (h *applicationHandler) loggingIdentity(alias string) (string, string, bool) {
	providerName, ok := h.providerByAlias[alias]
	if !ok {
		return "", "", false
	}
	return alias, string(providerName), true
}

func requestEventFromMeta(metadata core.ResultMeta) observability.RequestEvent {
	return observability.RequestEvent{
		StdoutBytes:     metadata.StdoutBytes,
		StderrBytes:     metadata.StderrBytes,
		QueueDepth:      metadata.QueueDepth,
		RunningCount:    metadata.RunningCount,
		QueueDuration:   metadata.QueueDuration,
		ExecutionTime:   metadata.ExecutionTime,
		ExitCategory:    metadata.ExitCategory,
		ProviderVersion: metadata.ProviderVersion,
		StopReason:      metadata.StopReason,
		StopAction:      metadata.StopAction,
	}
}

func classifyEndpoint(path string) string {
	switch path {
	case responsesEndpoint, modelsEndpoint:
		return path
	default:
		return unmatchedEndpoint
	}
}

func validGeneratedRequestID(identifier string) bool {
	if len(identifier) != len(requestIDPrefix)+requestIDSuffixBytes ||
		!strings.HasPrefix(identifier, requestIDPrefix) {
		return false
	}
	for index := len(requestIDPrefix); index < len(identifier); index++ {
		character := identifier[index]
		if (character < 'a' || character > 'z') &&
			(character < '2' || character > '7') {
			return false
		}
	}
	return true
}

func validJSONContentType(header http.Header) bool {
	values := header.Values("Content-Type")
	if len(values) != 1 {
		return false
	}
	mediaType, parameters, err := mime.ParseMediaType(values[0])
	if err != nil || !strings.EqualFold(mediaType, applicationContentType) {
		return false
	}
	if len(parameters) == 0 {
		return true
	}
	charset, ok := parameters["charset"]
	return len(parameters) == 1 && ok && strings.EqualFold(charset, "utf-8")
}

func validContentEncoding(header http.Header) bool {
	values := header.Values("Content-Encoding")
	if len(values) == 0 {
		return true
	}
	return len(values) == 1 && strings.EqualFold(strings.TrimSpace(values[0]), "identity")
}

func tryAcquire(gate chan struct{}) bool {
	select {
	case gate <- struct{}{}:
		return true
	default:
		return false
	}
}

func releaseGate(gate chan struct{}) {
	<-gate
}
