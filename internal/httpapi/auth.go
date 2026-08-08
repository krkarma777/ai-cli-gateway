package httpapi

import (
	"net/http"
	"strings"

	"github.com/krkarma777/ai-cli-gateway/internal/gatewaykey"
)

type authenticator struct {
	snapshot gatewaykey.Snapshot
}

func (a authenticator) authorized(header http.Header) bool {
	if !a.snapshot.Enabled() {
		return a.snapshot.Matches("")
	}
	values := header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return false
	}
	return a.snapshot.Matches(values[0][len("Bearer "):])
}
