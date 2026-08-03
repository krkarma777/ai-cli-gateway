// Package provider defines the shared contracts used by provider CLI adapters.
package provider

import (
	"context"
	"slices"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/krkarma777/ai-cli-gateway/internal/process"
)

// ProviderConfig contains trusted configuration for one provider CLI.
//
//nolint:revive // The plan fixes this public cross-package contract name.
type ProviderConfig struct {
	Executable    string
	PrefixArgs    []string
	ConfigHome    string
	CredentialEnv []string
	SafePath      string
	LookupEnv     LookupEnv
}

// Clone returns a copy that owns its mutable slice fields.
func (c ProviderConfig) Clone() ProviderConfig {
	c.PrefixArgs = slices.Clone(c.PrefixArgs)
	c.CredentialEnv = slices.Clone(c.CredentialEnv)
	return c
}

// LookupEnv returns the explicitly named environment value, when present.
type LookupEnv func(string) (string, bool)

// SchemaDelivery identifies how an adapter receives a validated JSON Schema.
type SchemaDelivery uint8

const (
	// SchemaInline embeds the validated schema in the prompt contract.
	SchemaInline SchemaDelivery = iota
	// SchemaFile tells the provider to use the gateway-owned schema file.
	SchemaFile
)

// ProbeRunner executes one provider health probe. RunProbe owns a fresh request
// runtime for each command and passes it to build. The runner must clean and
// discard that runtime after builder, execution, or parsing failure.
type ProbeRunner interface {
	RunProbe(
		context.Context,
		func(process.Runtime) (process.CommandSpec, error),
	) (process.Result, error)
}

// Adapter builds, probes, and parses one supported provider CLI protocol.
type Adapter interface {
	Name() core.ProviderName
	SupportedVersion() Range
	Probe(context.Context, ProviderConfig, ProbeRunner) Health
	Build(core.Request, core.Model, ProviderConfig, process.Runtime) (
		process.CommandSpec, error)
	Parse(core.Request, process.Result) (string, error)
}

// HealthStatus is a closed provider readiness state.
type HealthStatus string

const (
	// HealthReady means the provider can accept requests.
	HealthReady HealthStatus = "ready"
	// HealthNotReady means a known condition prevents provider requests.
	HealthNotReady HealthStatus = "not_ready"
	// HealthUnknown means readiness could not be established safely.
	HealthUnknown HealthStatus = "unknown"
)

const (
	// ProblemExecutableMissing identifies an unavailable provider executable.
	ProblemExecutableMissing = "executable_missing"
	// ProblemExecutableUnsafe identifies a provider executable that failed safety checks.
	ProblemExecutableUnsafe = "executable_unsafe"
	// ProblemVersionUnreadable identifies a provider version that could not be established.
	ProblemVersionUnreadable = "version_unreadable"
	// ProblemVersionUnsupported identifies a provider version outside its supported range.
	ProblemVersionUnsupported = "version_unsupported"
	// ProblemCapabilityMissing identifies a required provider capability that is unavailable.
	ProblemCapabilityMissing = "capability_missing"
	// ProblemConfigHomeUnsafe identifies a provider configuration home that failed safety checks.
	ProblemConfigHomeUnsafe = "config_home_unsafe"
	// ProblemAuthMissing identifies absent provider authentication.
	ProblemAuthMissing = "auth_missing"
	// ProblemAuthUnknown identifies provider authentication that could not be established.
	ProblemAuthUnknown = "auth_unknown"
	// ProblemCredentialMissing identifies an unavailable required provider credential.
	//nolint:gosec // This is a closed diagnostic identifier, not credential material.
	ProblemCredentialMissing = "credential_missing"
	// ProblemCredentialFileUnsafe identifies a provider credential file that failed safety checks.
	//nolint:gosec // This is a closed diagnostic identifier, not credential material.
	ProblemCredentialFileUnsafe = "credential_file_unsafe"
)

// Health contains only filtered provider readiness information.
type Health struct {
	Provider     core.ProviderName `json:"provider"`
	Status       HealthStatus      `json:"status"`
	Version      string            `json:"version,omitempty"`
	Auth         string            `json:"auth"`
	Capabilities []string          `json:"capabilities"`
	Problems     []string          `json:"problems,omitempty"`
}

// Clone returns a copy that owns its mutable slice fields.
func (h Health) Clone() Health {
	h.Capabilities = slices.Clone(h.Capabilities)
	h.Problems = slices.Clone(h.Problems)
	return h
}

// ErrorCategory is a closed provider failure category.
type ErrorCategory string

const (
	// ProviderErrorAuthRequired means provider authentication is unavailable.
	ProviderErrorAuthRequired ErrorCategory = "auth_required"
	// ProviderErrorRateLimited means a structured provider response proved a
	// provider-side rate limit.
	ProviderErrorRateLimited ErrorCategory = "rate_limited"
	// ProviderErrorProtocol means the provider response violated its protocol.
	ProviderErrorProtocol ErrorCategory = "protocol"
	// ProviderErrorFailed means the provider failed without a safer category.
	ProviderErrorFailed ErrorCategory = "failed"
)

// ProviderError exposes only a closed failure category and fixed safe text.
//
//nolint:revive // The plan fixes this public cross-package contract name.
type ProviderError struct {
	category ErrorCategory
}

// NewProviderError constructs a provider error. Unknown categories fail closed
// to ProviderErrorFailed.
func NewProviderError(category ErrorCategory) *ProviderError {
	if !validProviderErrorCategory(category) {
		category = ProviderErrorFailed
	}
	return &ProviderError{category: category}
}

// Category returns the closed failure category. Nil and invalid values fail
// closed to ProviderErrorFailed.
func (e *ProviderError) Category() ErrorCategory {
	if e == nil || !validProviderErrorCategory(e.category) {
		return ProviderErrorFailed
	}
	return e.category
}

// Error returns fixed category-derived text and never includes provider data.
func (e *ProviderError) Error() string {
	switch e.Category() {
	case ProviderErrorAuthRequired:
		return "provider authentication is required"
	case ProviderErrorRateLimited:
		return "provider rate limit was reached"
	case ProviderErrorProtocol:
		return "provider protocol error"
	case ProviderErrorFailed:
		return "provider failed"
	default:
		return "provider failed"
	}
}

func validProviderErrorCategory(category ErrorCategory) bool {
	switch category {
	case ProviderErrorAuthRequired,
		ProviderErrorRateLimited,
		ProviderErrorProtocol,
		ProviderErrorFailed:
		return true
	default:
		return false
	}
}
