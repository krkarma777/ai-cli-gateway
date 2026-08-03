// Package observability provides closed, metadata-only gateway telemetry.
package observability

import (
	"log/slog"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

const (
	requestCompletedMessage = "request_completed"
	invalidMetadataMessage  = "log_metadata_invalid"
	encodedRequestIDBytes   = 26
	encodedRequestIDPrefix  = "req_"
	endpointResponses       = "/v1/responses"
	endpointModels          = "/v1/models"
	endpointUnmatched       = "unmatched"
)

var modelAliasPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

// CounterSnapshot is one immutable view of the in-memory counters.
type CounterSnapshot struct {
	ClientCanceled uint64
}

// Counters stores numeric process-local telemetry only. Its zero value is
// ready for concurrent use.
type Counters struct {
	clientCanceled atomic.Uint64
}

// ClientCanceled records one response suppressed because the client canceled.
func (c *Counters) ClientCanceled() {
	if c == nil {
		return
	}
	c.clientCanceled.Add(1)
}

// Snapshot returns a point-in-time copy of the counters.
func (c *Counters) Snapshot() CounterSnapshot {
	if c == nil {
		return CounterSnapshot{}
	}
	return CounterSnapshot{ClientCanceled: c.clientCanceled.Load()}
}

// RequestEvent is the complete request-log schema. It intentionally has no
// body, output, credential, argv, environment, path, or raw-error field.
type RequestEvent struct {
	RequestID       string
	Endpoint        string
	Status          int
	ModelAlias      string
	Provider        core.ProviderName
	InputBytes      int
	StdoutBytes     int64
	StderrBytes     int64
	FinalBytes      int
	QueueDepth      int
	RunningCount    int
	QueueDuration   time.Duration
	ExecutionTime   time.Duration
	ErrorCode       string
	ExitCategory    string
	ProviderVersion string
	StopReason      string
	StopAction      string
}

// LogRequest emits one validated, fixed-schema request event. Durations are
// consistently recorded as integer milliseconds. An invalid event is replaced
// by one fixed message with no attributes from the rejected value.
func LogRequest(logger *slog.Logger, event RequestEvent) {
	if logger == nil {
		return
	}
	if !validRequestEvent(event) {
		logger.Warn(invalidMetadataMessage)
		return
	}

	logger.Info(requestCompletedMessage,
		"request_id", event.RequestID,
		"endpoint", event.Endpoint,
		"status", event.Status,
		"model_alias", event.ModelAlias,
		"provider", event.Provider,
		"input_bytes", event.InputBytes,
		"stdout_bytes", event.StdoutBytes,
		"stderr_bytes", event.StderrBytes,
		"final_bytes", event.FinalBytes,
		"queue_depth", event.QueueDepth,
		"running_count", event.RunningCount,
		"queue_duration_ms", event.QueueDuration.Milliseconds(),
		"execution_time_ms", event.ExecutionTime.Milliseconds(),
		"error_code", event.ErrorCode,
		"exit_category", event.ExitCategory,
		"provider_version", event.ProviderVersion,
		"stop_reason", event.StopReason,
		"stop_action", event.StopAction,
	)
}

func validRequestEvent(event RequestEvent) bool {
	if !validRequestID(event.RequestID) ||
		!validEndpoint(event.Endpoint) ||
		!validStatusCode(event.Status, event.ErrorCode) ||
		!validModelIdentity(event.ModelAlias, event.Provider) ||
		event.InputBytes < 0 ||
		event.StdoutBytes < 0 ||
		event.StderrBytes < 0 ||
		event.FinalBytes < 0 ||
		event.QueueDepth < 0 ||
		event.RunningCount < 0 ||
		event.QueueDuration < 0 ||
		event.ExecutionTime < 0 ||
		!core.IsCanonicalProviderVersion(event.ProviderVersion) ||
		!validExitCategory(event.ExitCategory) ||
		!validStopReason(event.StopReason) ||
		!validStopAction(event.StopAction) {
		return false
	}
	return true
}

func validRequestID(id string) bool {
	if len(id) != len(encodedRequestIDPrefix)+encodedRequestIDBytes ||
		id[:len(encodedRequestIDPrefix)] != encodedRequestIDPrefix {
		return false
	}
	for _, char := range id[len(encodedRequestIDPrefix):] {
		if char < 'a' || char > 'z' {
			if char < '2' || char > '7' {
				return false
			}
		}
	}
	return true
}

func validEndpoint(endpoint string) bool {
	switch endpoint {
	case endpointResponses, endpointModels, endpointUnmatched:
		return true
	default:
		return false
	}
}

func validStatusCode(status int, code string) bool {
	if code == "" {
		return status == 200
	}
	catalogError := core.Error(code, nil)
	return catalogError.CodeValue() == code && catalogError.StatusCode() == status
}

func validModelIdentity(alias string, provider core.ProviderName) bool {
	if alias == "" || provider == "" {
		return alias == "" && provider == ""
	}
	if !modelAliasPattern.MatchString(alias) {
		return false
	}
	switch provider {
	case core.ProviderCodex, core.ProviderClaude, core.ProviderGemini:
		return true
	default:
		return false
	}
}

func validExitCategory(category string) bool {
	switch category {
	case "", "completed", "nonzero_exit", "start_failed", "canceled",
		"timeout", "output_limit", "cleanup_failed":
		return true
	default:
		return false
	}
}

func validStopReason(reason string) bool {
	switch reason {
	case "", "completed", "client_canceled", "gateway_shutdown",
		"execution_timeout", "output_limit", "cleanup_failed":
		return true
	default:
		return false
	}
}

func validStopAction(action string) bool {
	switch action {
	case "", "none", "term", "kill", "terminate_job":
		return true
	default:
		return false
	}
}
