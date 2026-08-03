package core_test

import (
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
)

func TestNoncanonicalAPIErrorUsesSafeFallbackAndIsRejected(t *testing.T) {
	errValue := &core.APIError{}

	if got := errValue.StatusCode(); got != 500 {
		t.Errorf("StatusCode()=%d, want 500", got)
	}
	if got := errValue.TypeName(); got != "server_error" {
		t.Errorf("TypeName()=%q, want server_error", got)
	}
	if got := errValue.CodeValue(); got != core.CodeInternalError {
		t.Errorf("CodeValue()=%q, want %q", got, core.CodeInternalError)
	}
	if got := errValue.ParamValue(); got != nil {
		t.Errorf("ParamValue()=%v, want nil", got)
	}
	if got := errValue.MessageText(); got != "The gateway encountered an internal error." {
		t.Errorf("MessageText()=%q, want fixed internal error message", got)
	}
	if got := errValue.Error(); got !=
		"internal_error: The gateway encountered an internal error." {
		t.Errorf("Error()=%q, want fixed internal error text", got)
	}

	outcome, err := core.NewOutcomeError(errValue, core.ResultMeta{})
	if err == nil || outcome != nil {
		t.Fatalf("NewOutcomeError(noncanonical)=(%v, %v), want nil, error", outcome, err)
	}
}
