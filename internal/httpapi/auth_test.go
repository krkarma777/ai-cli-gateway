package httpapi

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/krkarma777/ai-cli-gateway/internal/gatewaykey"
)

func TestAuthenticatorDisabledBypassesBearerHeader(t *testing.T) {
	auth := authenticator{snapshot: gatewaykey.Disabled()}
	header := make(http.Header)
	header.Add("Authorization", "Basic wrong-token")
	header.Add("Authorization", "Bearer wrong-token")

	if !auth.authorized(header) {
		t.Fatal("explicitly disabled authentication rejected request")
	}
}

func TestAuthenticatorZeroSnapshotFailsClosed(t *testing.T) {
	var auth authenticator
	for _, header := range []http.Header{
		{},
		{"Authorization": []string{"Bearer anything"}},
	} {
		if auth.authorized(header) {
			t.Fatal("zero snapshot authorized request")
		}
	}
}

func TestAuthenticatorUsesImmutableEnvironmentSnapshot(t *testing.T) {
	const name = "AI_CLI_GATEWAY_TASK7_TEST_KEY"
	const original = "environment-original-token"
	const mutated = "environment-mutated-token"
	t.Setenv(name, original)
	snapshot, err := gatewaykey.FromEnvironment(name, os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	auth := authenticator{snapshot: snapshot}

	if err := os.Setenv(name, mutated); err != nil {
		t.Fatal(err)
	}
	if !auth.authorized(bearerHeader(original)) {
		t.Fatal("snapshot stopped matching after environment source mutation")
	}
	if auth.authorized(bearerHeader(mutated)) {
		t.Fatal("snapshot followed environment source mutation")
	}
}

func TestAuthenticatorUsesImmutableFileSnapshot(t *testing.T) {
	const original = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	mutated := strings.Repeat("f", 64)
	path := filepath.Join(t.TempDir(), "gateway.key")
	if err := os.WriteFile(path, []byte(original+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path) // #nosec G304 -- path is the test-owned TempDir file created immediately above.
	if err != nil {
		t.Fatal(err)
	}
	snapshot, parseErr := gatewaykey.Parse(file)
	closeErr := file.Close()
	if parseErr != nil {
		t.Fatal(parseErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	auth := authenticator{snapshot: snapshot}

	if err := os.WriteFile(path, []byte(mutated+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !auth.authorized(bearerHeader(original)) {
		t.Fatal("snapshot stopped matching after file source mutation")
	}
	if auth.authorized(bearerHeader(mutated)) {
		t.Fatal("snapshot followed file source mutation")
	}
}

func TestAuthenticatorRequiresExactSingleBearerHeader(t *testing.T) {
	snapshot, err := gatewaykey.FromEnvironment("GATEWAY_KEY", func(string) (string, bool) {
		return "correct-token", true
	})
	if err != nil {
		t.Fatal(err)
	}
	auth := authenticator{snapshot: snapshot}

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

func bearerHeader(token string) http.Header {
	return http.Header{"Authorization": []string{"Bearer " + token}}
}
