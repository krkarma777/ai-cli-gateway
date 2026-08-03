package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"net/http"
	"strings"
)

var errAuthenticator = errors.New("gateway authentication configuration is invalid")

type authenticator struct {
	enabled bool
	digest  [sha256.Size]byte
}

func newAuthenticator(
	environmentName string,
	lookup func(string) (string, bool),
) (authenticator, error) {
	if environmentName == "" {
		return authenticator{}, nil
	}
	if !validAPIKeyEnvironmentName(environmentName) || lookup == nil {
		return authenticator{}, errAuthenticator
	}
	secret, ok := lookup(environmentName)
	if !ok || secret == "" {
		return authenticator{}, errAuthenticator
	}
	return authenticator{
		enabled: true,
		digest:  sha256.Sum256([]byte(secret)),
	}, nil
}

func (a authenticator) authorized(header http.Header) bool {
	if !a.enabled {
		return true
	}
	values := header.Values("Authorization")
	if len(values) != 1 || !strings.HasPrefix(values[0], "Bearer ") {
		return false
	}
	token := values[0][len("Bearer "):]
	if token == "" {
		return false
	}
	presented := sha256.Sum256([]byte(token))
	return subtle.ConstantTimeCompare(presented[:], a.digest[:]) == 1
}

func validAPIKeyEnvironmentName(name string) bool {
	if name == "" || !isUpperEnvironmentInitial(name[0]) {
		return false
	}
	for index := 1; index < len(name); index++ {
		character := name[index]
		if !isUpperEnvironmentInitial(character) &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func isUpperEnvironmentInitial(character byte) bool {
	return character == '_' || character >= 'A' && character <= 'Z'
}
