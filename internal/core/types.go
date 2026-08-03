// Package core defines the gateway's provider-independent request, result, and
// error contracts.
package core

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ProviderName identifies a supported CLI provider.
type ProviderName string

// Supported provider names.
const (
	ProviderCodex  ProviderName = "codex"
	ProviderClaude ProviderName = "claude"
	ProviderGemini ProviderName = "gemini"
)

// FormatType identifies the requested output representation.
type FormatType string

// Supported output formats.
const (
	FormatText       FormatType = "text"
	FormatJSONSchema FormatType = "json_schema"
)

// OutputFormat describes the response format requested from a provider.
type OutputFormat struct {
	Type        FormatType
	Name        string
	Description *string
	Schema      json.RawMessage
}

// Request is a provider-independent gateway request.
type Request struct {
	ModelAlias   string
	Instructions *string
	Input        string
	Format       OutputFormat
}

// Weight returns the request's admission-accounting byte weight.
func (r Request) Weight() int64 {
	n := len(r.ModelAlias) + len(r.Input) + len(r.Format.Name) +
		len(r.Format.Schema)
	if r.Format.Description != nil {
		n += len(*r.Format.Description)
	}
	if r.Instructions != nil {
		n += len(*r.Instructions)
	}
	return int64(n)
}

// Model maps a public alias to a trusted provider model.
type Model struct {
	ID            string
	Provider      ProviderName
	ProviderModel string
	Created       int64
}

// Result contains provider output and safe execution metadata.
type Result struct {
	Text string
	Meta ResultMeta
}

// ResultMeta contains only numeric values and closed identifiers safe to expose.
type ResultMeta struct {
	Provider        ProviderName
	StdoutBytes     int64
	StderrBytes     int64
	QueueDepth      int
	RunningCount    int
	QueueDuration   time.Duration
	ExecutionTime   time.Duration
	ProviderVersion string
	ExitCategory    string
	StopReason      string
	StopAction      string
}

// OutcomeError combines a safe cause with validated execution metadata.
type OutcomeError struct {
	cause error
	meta  ResultMeta
}

var (
	errUnsafeOutcomeCause = errors.New("outcome cause is not safe")
	errInvalidResultMeta  = errors.New("result metadata is invalid")
)

const maxProviderVersionBytes = 32

var validExitCategories = map[string]struct{}{
	"":               {},
	"completed":      {},
	"nonzero_exit":   {},
	"start_failed":   {},
	"canceled":       {},
	"timeout":        {},
	"output_limit":   {},
	"cleanup_failed": {},
}

var validStopReasons = map[string]struct{}{
	"":                  {},
	"completed":         {},
	"client_canceled":   {},
	"gateway_shutdown":  {},
	"execution_timeout": {},
	"output_limit":      {},
	"cleanup_failed":    {},
}

var validStopActions = map[string]struct{}{
	"":              {},
	"none":          {},
	"term":          {},
	"kill":          {},
	"terminate_job": {},
}

// NewOutcomeError creates a safe error with closed, metadata-only execution
// details. It deliberately rejects wrapped causes because an arbitrary wrapper
// can add sensitive text to its Error method.
func NewOutcomeError(cause error, meta ResultMeta) (*OutcomeError, error) {
	if !isSafeOutcomeCause(cause) {
		return nil, errUnsafeOutcomeCause
	}
	if !validateResultMeta(meta) {
		return nil, errInvalidResultMeta
	}
	return &OutcomeError{cause: cause, meta: meta}, nil
}

func (e *OutcomeError) Error() string {
	if e == nil || e.cause == nil {
		return CodeInternalError + ": The gateway encountered an internal error."
	}
	return e.cause.Error()
}

func (e *OutcomeError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.cause
}

// ResultMetadata returns a defensive copy of the execution metadata.
func (e *OutcomeError) ResultMetadata() ResultMeta {
	if e == nil {
		return ResultMeta{}
	}
	return e.meta
}

func isSafeOutcomeCause(cause error) bool {
	// Exact identity is required: arbitrary wrappers can add unsafe text.
	//nolint:errorlint
	if cause == context.Canceled || cause == context.DeadlineExceeded {
		return true
	}
	// Exact concrete type is required for the same reason.
	//nolint:errorlint
	apiErr, ok := cause.(*APIError)
	return ok && apiErr.isCanonical()
}

func validateResultMeta(meta ResultMeta) bool {
	if meta.StdoutBytes < 0 ||
		meta.StderrBytes < 0 ||
		meta.QueueDepth < 0 ||
		meta.RunningCount < 0 ||
		meta.QueueDuration < 0 ||
		meta.ExecutionTime < 0 {
		return false
	}
	if meta.Provider != "" && !knownProvider(meta.Provider) {
		return false
	}
	if !IsCanonicalProviderVersion(meta.ProviderVersion) {
		return false
	}
	if _, ok := validExitCategories[meta.ExitCategory]; !ok {
		return false
	}
	if _, ok := validStopReasons[meta.StopReason]; !ok {
		return false
	}
	if _, ok := validStopActions[meta.StopAction]; !ok {
		return false
	}
	return true
}

// IsCanonicalProviderVersion reports whether version is empty or one bounded,
// exact ASCII major.minor.patch value. Empty represents a stage at which no
// provider version has been established yet.
func IsCanonicalProviderVersion(version string) bool {
	if version == "" {
		return true
	}
	if len(version) > maxProviderVersionBytes {
		return false
	}

	majorEnd := strings.IndexByte(version, '.')
	if majorEnd <= 0 {
		return false
	}
	minorStart := majorEnd + 1
	minorWidth := strings.IndexByte(version[minorStart:], '.')
	if minorWidth <= 0 {
		return false
	}
	minorEnd := minorStart + minorWidth
	patchStart := minorEnd + 1
	if patchStart >= len(version) || strings.IndexByte(version[patchStart:], '.') >= 0 {
		return false
	}

	return canonicalVersionComponent(version[:majorEnd]) &&
		canonicalVersionComponent(version[minorStart:minorEnd]) &&
		canonicalVersionComponent(version[patchStart:])
}

func canonicalVersionComponent(component string) bool {
	if component == "" || len(component) > 1 && component[0] == '0' {
		return false
	}
	for index := range len(component) {
		if component[index] < '0' || component[index] > '9' {
			return false
		}
	}
	_, err := strconv.ParseUint(component, 10, 64)
	return err == nil
}

func knownProvider(provider ProviderName) bool {
	switch provider {
	case ProviderCodex, ProviderClaude, ProviderGemini:
		return true
	default:
		return false
	}
}
