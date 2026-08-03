package core

// APIError is a fixed, safe public error from the gateway error catalog.
type APIError struct {
	catalogBacked bool
	status        int
	typ           string
	code          string
	param         *string
	message       string
}

// Stable public gateway error codes.
const (
	CodeInvalidJSON             = "invalid_json"
	CodeInvalidRequest          = "invalid_request"
	CodeUnsupportedParameter    = "unsupported_parameter"
	CodeInvalidJSONSchema       = "invalid_json_schema"
	CodeInvalidBearerKey        = "invalid_bearer_key"
	CodeNotFound                = "not_found"
	CodeModelNotFound           = "model_not_found"
	CodeMethodNotAllowed        = "method_not_allowed"
	CodeRequestTimeout          = "request_timeout"
	CodeRequestTooLarge         = "request_too_large"
	CodeUnsupportedMediaType    = "unsupported_media_type"
	CodeServerBusy              = "server_busy"
	CodeQueueFull               = "queue_full"
	CodeProviderRateLimited     = "provider_rate_limited"
	CodeQueueTimeout            = "queue_timeout"
	CodeProviderNotReady        = "provider_not_ready"
	CodeProviderAuthRequired    = "provider_auth_required"
	CodeServiceShuttingDown     = "service_shutting_down"
	CodeProviderTimeout         = "provider_timeout"
	CodeOutputLimitExceeded     = "output_limit_exceeded"
	CodeProviderProtocolError   = "provider_protocol_error"
	CodeStructuredOutputInvalid = "structured_output_invalid"
	CodeProviderFailed          = "provider_failed"
	CodeProcessCleanupFailed    = "process_cleanup_failed"
	CodeInternalError           = "internal_error"
)

type errorSpec struct {
	status  int
	typ     string
	message string
}

const (
	internalErrorStatus  = 500
	internalErrorType    = "server_error"
	internalErrorMessage = "The gateway encountered an internal error."
)

var errorCatalog = map[string]errorSpec{
	CodeInvalidJSON: {
		status:  400,
		typ:     "invalid_request_error",
		message: "The request body is not valid JSON.",
	},
	CodeInvalidRequest: {
		status:  400,
		typ:     "invalid_request_error",
		message: "The request is invalid.",
	},
	CodeUnsupportedParameter: {
		status:  400,
		typ:     "invalid_request_error",
		message: "This parameter or value is not supported.",
	},
	CodeInvalidJSONSchema: {
		status:  400,
		typ:     "invalid_request_error",
		message: "The JSON Schema is not supported.",
	},
	CodeInvalidBearerKey: {
		status:  401,
		typ:     "authentication_error",
		message: "A valid gateway Bearer key is required.",
	},
	CodeNotFound: {
		status:  404,
		typ:     "invalid_request_error",
		message: "The requested endpoint was not found.",
	},
	CodeModelNotFound: {
		status:  404,
		typ:     "invalid_request_error",
		message: "The requested model alias was not found.",
	},
	CodeMethodNotAllowed: {
		status:  405,
		typ:     "invalid_request_error",
		message: "The HTTP method is not allowed for this endpoint.",
	},
	CodeRequestTimeout: {
		status:  408,
		typ:     "invalid_request_error",
		message: "The request was not received before its deadline.",
	},
	CodeRequestTooLarge: {
		status:  413,
		typ:     "invalid_request_error",
		message: "The request exceeds a configured size limit.",
	},
	CodeUnsupportedMediaType: {
		status:  415,
		typ:     "invalid_request_error",
		message: "The request media type or content encoding is not supported.",
	},
	CodeServerBusy: {
		status:  429,
		typ:     "rate_limit_error",
		message: "The gateway is at its global request limit.",
	},
	CodeQueueFull: {
		status:  429,
		typ:     "rate_limit_error",
		message: "The provider queue is full.",
	},
	CodeProviderRateLimited: {
		status:  429,
		typ:     "rate_limit_error",
		message: "The provider rate-limited the request.",
	},
	CodeQueueTimeout: {
		status:  503,
		typ:     "server_error",
		message: "The request expired while waiting for provider capacity.",
	},
	CodeProviderNotReady: {
		status:  503,
		typ:     "server_error",
		message: "The selected provider is not ready.",
	},
	CodeProviderAuthRequired: {
		status:  503,
		typ:     "server_error",
		message: "The selected provider requires authentication.",
	},
	CodeServiceShuttingDown: {
		status:  503,
		typ:     "server_error",
		message: "The gateway is shutting down.",
	},
	CodeProviderTimeout: {
		status:  504,
		typ:     "server_error",
		message: "The provider did not finish before its deadline.",
	},
	CodeOutputLimitExceeded: {
		status:  502,
		typ:     "server_error",
		message: "The provider output exceeded a configured limit.",
	},
	CodeProviderProtocolError: {
		status:  502,
		typ:     "server_error",
		message: "The provider returned an invalid response.",
	},
	CodeStructuredOutputInvalid: {
		status:  502,
		typ:     "server_error",
		message: "The provider output did not match the requested JSON Schema.",
	},
	CodeProviderFailed: {
		status:  502,
		typ:     "server_error",
		message: "The provider command failed.",
	},
	CodeProcessCleanupFailed: {
		status:  500,
		typ:     "server_error",
		message: "The provider process could not be cleaned up safely.",
	},
	CodeInternalError: {
		status:  internalErrorStatus,
		typ:     internalErrorType,
		message: internalErrorMessage,
	},
}

// Error constructs an immutable catalog error and defensively copies param.
func Error(code string, param *string) *APIError {
	spec, ok := errorCatalog[code]
	if !ok {
		code = CodeInternalError
		spec = errorCatalog[code]
		param = nil
	}
	var copiedParam *string
	if param != nil {
		value := *param
		copiedParam = &value
	}
	return &APIError{
		catalogBacked: true,
		status:        spec.status,
		typ:           spec.typ,
		code:          code,
		param:         copiedParam,
		message:       spec.message,
	}
}

func (e *APIError) Error() string {
	if !e.isCanonical() {
		return CodeInternalError + ": " + internalErrorMessage
	}
	return e.code + ": " + e.message
}

// StatusCode returns the catalog HTTP status.
func (e *APIError) StatusCode() int {
	if !e.isCanonical() {
		return internalErrorStatus
	}
	return e.status
}

// TypeName returns the catalog error type.
func (e *APIError) TypeName() string {
	if !e.isCanonical() {
		return internalErrorType
	}
	return e.typ
}

// CodeValue returns the stable public error code.
func (e *APIError) CodeValue() string {
	if !e.isCanonical() {
		return CodeInternalError
	}
	return e.code
}

// ParamValue returns a defensive copy of the optional request parameter.
func (e *APIError) ParamValue() *string {
	if !e.isCanonical() || e.param == nil {
		return nil
	}
	value := *e.param
	return &value
}

// MessageText returns the fixed safe catalog message.
func (e *APIError) MessageText() string {
	if !e.isCanonical() {
		return internalErrorMessage
	}
	return e.message
}

func (e *APIError) isCanonical() bool {
	if e == nil || !e.catalogBacked {
		return false
	}
	spec, ok := errorCatalog[e.code]
	return ok &&
		e.status == spec.status &&
		e.typ == spec.typ &&
		e.message == spec.message
}
