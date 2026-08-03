package httpapi

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAuthenticatorDisabledDoesNotReadEnvironment(t *testing.T) {
	calls := 0
	auth, err := newAuthenticator("", func(string) (string, bool) {
		calls++
		return "planted-secret", true
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatalf("lookup calls=%d", calls)
	}
	if !auth.authorized(http.Header{}) {
		t.Fatal("disabled authentication rejected request")
	}
}

func TestAuthenticatorReadsAndHashesSecretOnce(t *testing.T) {
	secret := "gateway-secret-DO-NOT-LEAK"
	calls := 0
	auth, err := newAuthenticator("AI_CLI_GATEWAY_API_KEY", func(name string) (string, bool) {
		calls++
		if name != "AI_CLI_GATEWAY_API_KEY" {
			t.Fatalf("name=%q", name)
		}
		return secret, true
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("lookup calls=%d", calls)
	}
	if strings.Contains(fmt.Sprintf("%#v", auth), secret) {
		t.Fatal("authenticator retained printable raw secret")
	}

	secret = "mutated"
	header := make(http.Header)
	header.Set("Authorization", "Bearer gateway-secret-DO-NOT-LEAK")
	if !auth.authorized(header) {
		t.Fatal("digest changed after source mutation")
	}
	if calls != 1 {
		t.Fatalf("lookup repeated: calls=%d", calls)
	}
}

func TestAuthenticatorRejectsMissingOrEmptySecretWithFixedError(t *testing.T) {
	for _, test := range []struct {
		name  string
		value string
		ok    bool
	}{
		{name: "missing", ok: false},
		{name: "empty", value: "", ok: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := newAuthenticator("AI_CLI_GATEWAY_API_KEY", func(string) (string, bool) {
				return test.value, test.ok
			})
			if !errors.Is(err, errAuthenticator) {
				t.Fatalf("error=%v", err)
			}
			if strings.Contains(fmt.Sprint(err), "AI_CLI_GATEWAY_API_KEY") {
				t.Fatalf("error exposed input=%q", err)
			}
		})
	}
}

func TestAuthenticatorRequiresExactSingleBearerHeader(t *testing.T) {
	auth, err := newAuthenticator("AI_CLI_GATEWAY_API_KEY", func(string) (string, bool) {
		return "correct-token", true
	})
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		values []string
		want   bool
	}{
		{name: "absent"},
		{name: "empty", values: []string{""}},
		{name: "basic", values: []string{"Basic correct-token"}},
		{name: "case changed scheme", values: []string{"bearer correct-token"}},
		{name: "missing space", values: []string{"Bearercorrect-token"}},
		{name: "empty token", values: []string{"Bearer "}},
		{name: "extra space", values: []string{"Bearer  correct-token"}},
		{name: "tab separator", values: []string{"Bearer\tcorrect-token"}},
		{name: "trailing space", values: []string{"Bearer correct-token "}},
		{name: "wrong", values: []string{"Bearer wrong-token"}},
		{name: "comma combined", values: []string{"Bearer correct-token, Bearer correct-token"}},
		{name: "duplicates", values: []string{"Bearer correct-token", "Bearer correct-token"}},
		{name: "correct", values: []string{"Bearer correct-token"}, want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			header := make(http.Header)
			for _, value := range test.values {
				header.Add("Authorization", value)
			}
			if got := auth.authorized(header); got != test.want {
				t.Fatalf("authorized=%v want=%v", got, test.want)
			}
		})
	}
}

func TestAPIKeyEnvironmentNameGrammar(t *testing.T) {
	for _, valid := range []string{"AI_CLI_GATEWAY_API_KEY", "A", "_LOCAL_1"} {
		if !validAPIKeyEnvironmentName(valid) {
			t.Fatalf("rejected %q", valid)
		}
	}
	for _, invalid := range []string{"lowercase", "1_KEY", "KEY-NAME", "KEY NAME", "KEY\x00NAME"} {
		if validAPIKeyEnvironmentName(invalid) {
			t.Fatalf("accepted %q", invalid)
		}
	}
}
