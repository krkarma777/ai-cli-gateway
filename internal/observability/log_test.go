package observability_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/observability"
)

const validRequestID = "req_aaaaaaaaaaaaaaaaaaaaaaaaaa"

func TestCountersZeroValueAndConcurrentSnapshot(t *testing.T) {
	var counters observability.Counters
	if got := counters.Snapshot(); got.ClientCanceled != 0 {
		t.Fatalf("zero snapshot = %+v, want zero", got)
	}

	const goroutines = 32
	const increments = 250
	var workers sync.WaitGroup
	workers.Add(goroutines)
	for range goroutines {
		go func() {
			defer workers.Done()
			for range increments {
				counters.ClientCanceled()
			}
		}()
	}
	workers.Wait()

	if got, want := counters.Snapshot().ClientCanceled, uint64(goroutines*increments); got != want {
		t.Fatalf("ClientCanceled = %d, want %d", got, want)
	}
}

func TestLogRequestEmitsOnlyClosedMetadata(t *testing.T) {
	event := validEvent()
	record := captureOne(t, event)

	if got := record["msg"]; got != "request_completed" {
		t.Fatalf("message = %v, want request_completed", got)
	}
	want := map[string]any{
		"request_id":        validRequestID,
		"endpoint":          "/v1/responses",
		"status":            json.Number("200"),
		"model_alias":       "codex-default",
		"provider":          "codex",
		"input_bytes":       json.Number("17"),
		"stdout_bytes":      json.Number("29"),
		"stderr_bytes":      json.Number("3"),
		"final_bytes":       json.Number("23"),
		"queue_depth":       json.Number("2"),
		"running_count":     json.Number("1"),
		"queue_duration_ms": json.Number("1234"),
		"execution_time_ms": json.Number("5678"),
		"error_code":        "",
		"exit_category":     "completed",
		"provider_version":  "1.2.3",
		"stop_reason":       "completed",
		"stop_action":       "none",
	}
	delete(record, "msg")
	if len(record) != len(want) {
		t.Fatalf("attribute count = %d, want %d: %#v", len(record), len(want), record)
	}
	for key, wantValue := range want {
		if got := record[key]; got != wantValue {
			t.Errorf("%s = %#v, want %#v", key, got, wantValue)
		}
	}
}

func TestLogRequestAcceptsClosedVocabularies(t *testing.T) {
	t.Run("endpoints", func(t *testing.T) {
		for _, endpoint := range []string{"/v1/responses", "/v1/models", "unmatched"} {
			event := validEvent()
			event.Endpoint = endpoint
			assertValidEvent(t, event)
		}
	})

	t.Run("providers", func(t *testing.T) {
		for _, provider := range []core.ProviderName{
			core.ProviderCodex,
			core.ProviderClaude,
			core.ProviderGemini,
		} {
			event := validEvent()
			event.Provider = provider
			assertValidEvent(t, event)
		}

		event := validEvent()
		event.ModelAlias = ""
		event.Provider = ""
		assertValidEvent(t, event)
	})

	t.Run("error catalog status pairs", func(t *testing.T) {
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
				event := validEvent()
				event.ErrorCode = code
				event.Status = core.Error(code, nil).StatusCode()
				assertValidEvent(t, event)
			})
		}
	})

	t.Run("versions", func(t *testing.T) {
		for _, version := range []string{"", "0.0.0", "1.2.3", "18446744073709551615.0.9"} {
			event := validEvent()
			event.ProviderVersion = version
			assertValidEvent(t, event)
		}
	})

	t.Run("exit categories", func(t *testing.T) {
		for _, category := range []string{
			"", "completed", "nonzero_exit", "start_failed", "canceled",
			"timeout", "output_limit", "cleanup_failed",
		} {
			event := validEvent()
			event.ExitCategory = category
			assertValidEvent(t, event)
		}
	})

	t.Run("stop reasons", func(t *testing.T) {
		for _, reason := range []string{
			"", "completed", "client_canceled", "gateway_shutdown",
			"execution_timeout", "output_limit", "cleanup_failed",
		} {
			event := validEvent()
			event.StopReason = reason
			assertValidEvent(t, event)
		}
	})

	t.Run("stop actions", func(t *testing.T) {
		for _, action := range []string{"", "none", "term", "kill", "terminate_job"} {
			event := validEvent()
			event.StopAction = action
			assertValidEvent(t, event)
		}
	})
}

func TestLogRequestRejectsInvalidMetadataWithoutEcho(t *testing.T) {
	tooLongAlias := strings.Repeat("a", 129)
	tooLongVersion := "1.2." + strings.Repeat("3", 29)
	tests := []struct {
		name   string
		mutate func(*observability.RequestEvent)
	}{
		{"request id prefix", func(e *observability.RequestEvent) { e.RequestID = "resp_aaaaaaaaaaaaaaaaaaaaaaaaaa" }},
		{"request id short", func(e *observability.RequestEvent) { e.RequestID = "req_aaaaaaaa" }},
		{"request id alphabet", func(e *observability.RequestEvent) { e.RequestID = "req_aaaaaaaaaaaaaaaaaaaaaaaaa0" }},
		{"request id control", func(e *observability.RequestEvent) { e.RequestID = "req_aaaaaaaaaaaaaaaaaaaaaaaaa\n" }},
		{"endpoint", func(e *observability.RequestEvent) { e.Endpoint = "/planted/secret" }},
		{"success status", func(e *observability.RequestEvent) { e.Status = 201 }},
		{"success with code", func(e *observability.RequestEvent) { e.ErrorCode = core.CodeInternalError }},
		{"failure without code", func(e *observability.RequestEvent) { e.Status = 500 }},
		{"unknown error code", func(e *observability.RequestEvent) { e.Status = 500; e.ErrorCode = "secret_code" }},
		{"wrong error status", func(e *observability.RequestEvent) { e.Status = 400; e.ErrorCode = core.CodeInternalError }},
		{"alias only", func(e *observability.RequestEvent) { e.Provider = "" }},
		{"provider only", func(e *observability.RequestEvent) { e.ModelAlias = "" }},
		{"alias leading punctuation", func(e *observability.RequestEvent) { e.ModelAlias = "-secret" }},
		{"alias unicode", func(e *observability.RequestEvent) { e.ModelAlias = "비밀" }},
		{"alias control", func(e *observability.RequestEvent) { e.ModelAlias = "secret\n" }},
		{"alias too long", func(e *observability.RequestEvent) { e.ModelAlias = tooLongAlias }},
		{"provider", func(e *observability.RequestEvent) { e.Provider = core.ProviderName("secret_provider") }},
		{"input bytes", func(e *observability.RequestEvent) { e.InputBytes = -1 }},
		{"stdout bytes", func(e *observability.RequestEvent) { e.StdoutBytes = -1 }},
		{"stderr bytes", func(e *observability.RequestEvent) { e.StderrBytes = -1 }},
		{"final bytes", func(e *observability.RequestEvent) { e.FinalBytes = -1 }},
		{"queue depth", func(e *observability.RequestEvent) { e.QueueDepth = -1 }},
		{"running count", func(e *observability.RequestEvent) { e.RunningCount = -1 }},
		{"queue duration", func(e *observability.RequestEvent) { e.QueueDuration = -1 }},
		{"execution time", func(e *observability.RequestEvent) { e.ExecutionTime = -1 }},
		{"version components", func(e *observability.RequestEvent) { e.ProviderVersion = "1.2" }},
		{"version leading zero", func(e *observability.RequestEvent) { e.ProviderVersion = "01.2.3" }},
		{"version unicode", func(e *observability.RequestEvent) { e.ProviderVersion = "1.2.비밀" }},
		{"version control", func(e *observability.RequestEvent) { e.ProviderVersion = "1.2.3\n" }},
		{"version too long", func(e *observability.RequestEvent) { e.ProviderVersion = tooLongVersion }},
		{"exit category", func(e *observability.RequestEvent) { e.ExitCategory = "secret_exit" }},
		{"stop reason", func(e *observability.RequestEvent) { e.StopReason = "secret_reason" }},
		{"stop action", func(e *observability.RequestEvent) { e.StopAction = "secret_action" }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			event := validEvent()
			test.mutate(&event)
			record := captureOne(t, event)
			if len(record) != 1 || record["msg"] != "log_metadata_invalid" {
				t.Fatalf("invalid record = %#v, want only fixed message", record)
			}
		})
	}
}

func TestLogRequestInvalidEventDropsEveryPlantedSecret(t *testing.T) {
	const plantedValue = "PLANTED_OBSERVABILITY_VALUE"
	event := validEvent()
	event.RequestID = plantedValue
	event.Endpoint = plantedValue
	event.ModelAlias = plantedValue
	event.Provider = core.ProviderName(plantedValue)
	event.ErrorCode = plantedValue
	event.ExitCategory = plantedValue
	event.ProviderVersion = plantedValue
	event.StopReason = plantedValue
	event.StopAction = plantedValue

	var output bytes.Buffer
	logger := testLogger(&output)
	observability.LogRequest(logger, event)
	if strings.Contains(output.String(), plantedValue) {
		t.Fatalf("invalid log echoed planted secret: %q", output.String())
	}
	if got := decodeRecord(t, output.Bytes()); len(got) != 1 || got["msg"] != "log_metadata_invalid" {
		t.Fatalf("invalid record = %#v, want only fixed message", got)
	}
}

func validEvent() observability.RequestEvent {
	return observability.RequestEvent{
		RequestID:       validRequestID,
		Endpoint:        "/v1/responses",
		Status:          200,
		ModelAlias:      "codex-default",
		Provider:        core.ProviderCodex,
		InputBytes:      17,
		StdoutBytes:     29,
		StderrBytes:     3,
		FinalBytes:      23,
		QueueDepth:      2,
		RunningCount:    1,
		QueueDuration:   1234 * time.Millisecond,
		ExecutionTime:   5678 * time.Millisecond,
		ExitCategory:    "completed",
		ProviderVersion: "1.2.3",
		StopReason:      "completed",
		StopAction:      "none",
	}
}

func assertValidEvent(t *testing.T, event observability.RequestEvent) {
	t.Helper()
	record := captureOne(t, event)
	if got := record["msg"]; got != "request_completed" {
		t.Fatalf("message = %v, want request_completed; record=%#v", got, record)
	}
}

func captureOne(t *testing.T, event observability.RequestEvent) map[string]any {
	t.Helper()
	var output bytes.Buffer
	observability.LogRequest(testLogger(&output), event)
	return decodeRecord(t, output.Bytes())
}

func testLogger(output *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(output, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, attr slog.Attr) slog.Attr {
			if len(groups) == 0 && (attr.Key == slog.TimeKey || attr.Key == slog.LevelKey) {
				return slog.Attr{}
			}
			return attr
		},
	}))
}

func decodeRecord(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var record map[string]any
	if err := decoder.Decode(&record); err != nil {
		t.Fatalf("decode log %q: %v", raw, err)
	}
	if decoder.More() {
		t.Fatalf("unexpected additional log record in %q", raw)
	}
	return record
}
