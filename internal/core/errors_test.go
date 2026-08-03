package core

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestErrorCatalog(t *testing.T) {
	tests := []struct {
		code    string
		status  int
		typ     string
		message string
	}{
		{CodeInvalidJSON, 400, "invalid_request_error", "The request body is not valid JSON."},
		{CodeInvalidRequest, 400, "invalid_request_error", "The request is invalid."},
		{CodeUnsupportedParameter, 400, "invalid_request_error", "This parameter or value is not supported."},
		{CodeInvalidJSONSchema, 400, "invalid_request_error", "The JSON Schema is not supported."},
		{CodeInvalidBearerKey, 401, "authentication_error", "A valid gateway Bearer key is required."},
		{CodeNotFound, 404, "invalid_request_error", "The requested endpoint was not found."},
		{CodeModelNotFound, 404, "invalid_request_error", "The requested model alias was not found."},
		{CodeMethodNotAllowed, 405, "invalid_request_error", "The HTTP method is not allowed for this endpoint."},
		{CodeRequestTimeout, 408, "invalid_request_error", "The request was not received before its deadline."},
		{CodeRequestTooLarge, 413, "invalid_request_error", "The request exceeds a configured size limit."},
		{CodeUnsupportedMediaType, 415, "invalid_request_error", "The request media type or content encoding is not supported."},
		{CodeServerBusy, 429, "rate_limit_error", "The gateway is at its global request limit."},
		{CodeQueueFull, 429, "rate_limit_error", "The provider queue is full."},
		{CodeProviderRateLimited, 429, "rate_limit_error", "The provider rate-limited the request."},
		{CodeQueueTimeout, 503, "server_error", "The request expired while waiting for provider capacity."},
		{CodeProviderNotReady, 503, "server_error", "The selected provider is not ready."},
		{CodeProviderAuthRequired, 503, "server_error", "The selected provider requires authentication."},
		{CodeServiceShuttingDown, 503, "server_error", "The gateway is shutting down."},
		{CodeProviderTimeout, 504, "server_error", "The provider did not finish before its deadline."},
		{CodeOutputLimitExceeded, 502, "server_error", "The provider output exceeded a configured limit."},
		{CodeProviderProtocolError, 502, "server_error", "The provider returned an invalid response."},
		{CodeStructuredOutputInvalid, 502, "server_error", "The provider output did not match the requested JSON Schema."},
		{CodeProviderFailed, 502, "server_error", "The provider command failed."},
		{CodeProcessCleanupFailed, 500, "server_error", "The provider process could not be cleaned up safely."},
		{CodeInternalError, 500, "server_error", "The gateway encountered an internal error."},
	}

	for _, tt := range tests {
		t.Run(tt.code, func(t *testing.T) {
			got := Error(tt.code, nil)
			if got.StatusCode() != tt.status {
				t.Errorf("StatusCode()=%d, want %d", got.StatusCode(), tt.status)
			}
			if got.TypeName() != tt.typ {
				t.Errorf("TypeName()=%q, want %q", got.TypeName(), tt.typ)
			}
			if got.CodeValue() != tt.code {
				t.Errorf("CodeValue()=%q, want %q", got.CodeValue(), tt.code)
			}
			if got.MessageText() != tt.message {
				t.Errorf("MessageText()=%q, want %q", got.MessageText(), tt.message)
			}
			if got.ParamValue() != nil {
				t.Errorf("ParamValue()=%v, want nil", got.ParamValue())
			}
			wantError := tt.code + ": " + tt.message
			if got.Error() != wantError {
				t.Errorf("Error()=%q, want %q", got.Error(), wantError)
			}
		})
	}
}

func TestErrorDefensivelyCopiesParam(t *testing.T) {
	param := "stream"
	got := Error(CodeUnsupportedParameter, &param)
	param = "mutated-input"

	first := got.ParamValue()
	if first == nil || *first != "stream" {
		t.Fatalf("ParamValue()=%v, want stream", first)
	}
	*first = "mutated-output"
	second := got.ParamValue()
	if second == nil || *second != "stream" {
		t.Fatalf("second ParamValue()=%v, want stream", second)
	}
}

func TestErrorUnknownCodeUsesFixedInternalError(t *testing.T) {
	param := "attacker-controlled"
	got := Error("unknown\nsecret", &param)

	if got.StatusCode() != 500 ||
		got.TypeName() != "server_error" ||
		got.CodeValue() != CodeInternalError ||
		got.MessageText() != "The gateway encountered an internal error." ||
		got.ParamValue() != nil {
		t.Fatalf("Error(unknown)=%+v", got)
	}
	if strings.Contains(got.Error(), "unknown") ||
		strings.Contains(got.Error(), "secret") ||
		strings.Contains(got.Error(), param) {
		t.Fatalf("Error() exposed unknown input: %q", got.Error())
	}
}

func TestAPIErrorSupportsErrorsAs(t *testing.T) {
	want := Error(CodeProviderFailed, nil)
	wrapped := errors.New("outer: " + want.Error())
	if errors.As(wrapped, new(*APIError)) {
		t.Fatal("plain text unexpectedly matched *APIError")
	}

	var got *APIError
	if !errors.As(wrapSafe(want), &got) || got != want {
		t.Fatalf("errors.As() got %v, want original APIError", got)
	}
}

func TestNewOutcomeErrorAcceptsOnlySafeCauses(t *testing.T) {
	apiErr := Error(CodeProviderFailed, nil)
	tests := []struct {
		name  string
		cause error
	}{
		{"api_error", apiErr},
		{"context_canceled", context.Canceled},
		{"deadline_exceeded", context.DeadlineExceeded},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NewOutcomeError(tt.cause, validResultMeta())
			if err != nil {
				t.Fatalf("NewOutcomeError() error=%v", err)
			}
			if got.Error() != tt.cause.Error() {
				t.Errorf("Error()=%q, want %q", got.Error(), tt.cause.Error())
			}
			// Unwrap must return the identical direct cause, not just a matching chain.
			//nolint:errorlint
			if got.Unwrap() != tt.cause {
				t.Errorf("Unwrap()=%v, want identical cause", got.Unwrap())
			}
			if !errors.Is(got, tt.cause) {
				t.Errorf("errors.Is(%v, %v)=false", got, tt.cause)
			}
		})
	}
}

func TestNewOutcomeErrorRejectsUnsafeCauses(t *testing.T) {
	for _, cause := range []error{
		nil,
		errors.New("raw provider error with secret"),
		wrapSafe(Error(CodeProviderFailed, nil)),
		wrapSafe(context.Canceled),
	} {
		if got, err := NewOutcomeError(cause, validResultMeta()); err == nil || got != nil {
			t.Fatalf("NewOutcomeError(%v)=(%v, %v), want nil, error", cause, got, err)
		}
	}
}

func TestOutcomeErrorReturnsDefensiveMetadata(t *testing.T) {
	want := validResultMeta()
	got, err := NewOutcomeError(Error(CodeProviderFailed, nil), want)
	if err != nil {
		t.Fatal(err)
	}

	first := got.ResultMetadata()
	first.ProviderVersion = "9.9.9"
	first.StdoutBytes = 999
	second := got.ResultMetadata()
	if second != want {
		t.Fatalf("second ResultMetadata()=%+v, want %+v", second, want)
	}
}

func TestNewOutcomeErrorAcceptsClosedStopEnums(t *testing.T) {
	reasons := []string{
		"",
		"completed",
		"client_canceled",
		"gateway_shutdown",
		"execution_timeout",
		"output_limit",
		"cleanup_failed",
	}
	actions := []string{"", "none", "term", "kill", "terminate_job"}

	for _, reason := range reasons {
		for _, action := range actions {
			meta := validResultMeta()
			meta.StopReason = reason
			meta.StopAction = action
			if _, err := NewOutcomeError(Error(CodeProviderFailed, nil), meta); err != nil {
				t.Errorf("reason=%q action=%q: %v", reason, action, err)
			}
		}
	}
}

func TestNewOutcomeErrorRejectsUnsafeMetadata(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*ResultMeta)
	}{
		{"negative_stdout", func(m *ResultMeta) { m.StdoutBytes = -1 }},
		{"negative_stderr", func(m *ResultMeta) { m.StderrBytes = -1 }},
		{"negative_queue_depth", func(m *ResultMeta) { m.QueueDepth = -1 }},
		{"negative_running_count", func(m *ResultMeta) { m.RunningCount = -1 }},
		{"negative_queue_duration", func(m *ResultMeta) { m.QueueDuration = -time.Nanosecond }},
		{"negative_execution_time", func(m *ResultMeta) { m.ExecutionTime = -time.Nanosecond }},
		{"unknown_provider", func(m *ResultMeta) { m.Provider = ProviderName("other\nsecret") }},
		{"noncanonical_version", func(m *ResultMeta) { m.ProviderVersion = "1460" }},
		{"oversized_version", func(m *ResultMeta) {
			m.ProviderVersion = "18446744073709551615.0.1234567890"
		}},
		{"unknown_exit_category", func(m *ResultMeta) { m.ExitCategory = "other\nsecret" }},
		{"unknown_stop_reason", func(m *ResultMeta) { m.StopReason = "other\nsecret" }},
		{"unknown_stop_action", func(m *ResultMeta) { m.StopAction = "other\nsecret" }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := validResultMeta()
			tt.mutate(&meta)
			if got, err := NewOutcomeError(Error(CodeProviderFailed, nil), meta); err == nil || got != nil {
				t.Fatalf("NewOutcomeError()=(%v, %v), want nil, error", got, err)
			}
		})
	}
}

func TestProviderVersionMetadataContract(t *testing.T) {
	tests := []struct {
		name    string
		version string
		valid   bool
	}{
		{name: "unknown", version: "", valid: true},
		{name: "all zero", version: "0.0.0", valid: true},
		{name: "codex", version: "0.146.0", valid: true},
		{name: "claude", version: "2.1.208", valid: true},
		{name: "gemini", version: "0.53.0", valid: true},
		{name: "maximum major", version: "18446744073709551615.0.0", valid: true},
		{name: "maximum minor", version: "0.18446744073709551615.0", valid: true},
		{name: "maximum patch", version: "0.0.18446744073709551615", valid: true},
		{
			name:    "exactly 32 bytes",
			version: "18446744073709551615.0.123456789",
			valid:   true,
		},
		{name: "old concatenated form", version: "1460"},
		{name: "missing patch", version: "1.2"},
		{name: "missing minor and patch", version: "1"},
		{name: "extra component", version: "1.2.3.4"},
		{name: "empty major", version: ".1.2"},
		{name: "empty minor", version: "1..2"},
		{name: "empty patch", version: "1.2."},
		{name: "leading zero major", version: "01.2.3"},
		{name: "leading zero minor", version: "1.02.3"},
		{name: "leading zero patch", version: "1.2.03"},
		{name: "major plus", version: "+1.2.3"},
		{name: "minor minus", version: "1.-2.3"},
		{name: "patch plus", version: "1.2.+3"},
		{name: "leading v", version: "v1.2.3"},
		{name: "prerelease", version: "1.2.3-rc.1"},
		{name: "build suffix", version: "1.2.3+build"},
		{name: "trailing decoration", version: "1.2.3,"},
		{name: "leading space", version: " 1.2.3"},
		{name: "trailing space", version: "1.2.3 "},
		{name: "newline", version: "1.2.3\n"},
		{name: "tab", version: "1.\t2.3"},
		{name: "NUL", version: "1.2.3\x00"},
		{name: "BOM", version: "\ufeff1.2.3"},
		{name: "Arabic digits", version: "١.٢.٣"},
		{name: "fullwidth digits", version: "１.２.３"},
		{name: "invalid UTF-8", version: string([]byte{'1', '.', '2', '.', 0xff})},
		{name: "major overflow", version: "18446744073709551616.0.0"},
		{name: "minor overflow", version: "0.18446744073709551616.0"},
		{name: "patch overflow", version: "0.0.18446744073709551616"},
		{
			name:    "33 byte total",
			version: "18446744073709551615.0.1234567890",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsCanonicalProviderVersion(test.version); got != test.valid {
				t.Fatalf(
					"IsCanonicalProviderVersion(%q)=%t, want %t",
					test.version,
					got,
					test.valid,
				)
			}

			meta := validResultMeta()
			meta.ProviderVersion = test.version
			got, err := NewOutcomeError(Error(CodeProviderFailed, nil), meta)
			if test.valid {
				if err != nil || got == nil {
					t.Fatalf("NewOutcomeError()=(%v, %v), want value, nil", got, err)
				}
				return
			}
			if err == nil || got != nil {
				t.Fatalf("NewOutcomeError()=(%v, %v), want nil, error", got, err)
			}
		})
	}
}

func TestRequestWeightCountsAllRequestBytes(t *testing.T) {
	instructions := "rules"
	description := "desc"
	request := Request{
		ModelAlias:   "model",
		Instructions: &instructions,
		Input:        "input",
		Format: OutputFormat{
			Type:        FormatJSONSchema,
			Name:        "answer",
			Description: &description,
			Schema:      []byte(`{"type":"object"}`),
		},
	}
	want := int64(len(request.ModelAlias) + len(instructions) +
		len(request.Input) + len(request.Format.Name) +
		len(description) + len(request.Format.Schema))
	if got := request.Weight(); got != want {
		t.Fatalf("Weight()=%d, want %d", got, want)
	}
}

func validResultMeta() ResultMeta {
	return ResultMeta{
		Provider:        ProviderCodex,
		StdoutBytes:     12,
		StderrBytes:     3,
		QueueDepth:      2,
		RunningCount:    1,
		QueueDuration:   2 * time.Millisecond,
		ExecutionTime:   3 * time.Millisecond,
		ProviderVersion: "0.146.0",
	}
}

type safeWrapper struct {
	err error
}

func (w safeWrapper) Error() string {
	return "wrapped: " + w.err.Error()
}

func (w safeWrapper) Unwrap() error {
	return w.err
}

func wrapSafe(err error) error {
	return safeWrapper{err: err}
}
