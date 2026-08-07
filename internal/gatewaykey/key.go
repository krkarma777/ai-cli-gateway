package gatewaykey

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"io"
	"strings"
)

const entropySize = 32

var ErrUnavailable = errors.New("gateway authentication is unavailable")

type LookupEnv func(string) (string, bool)

type snapshotState uint8

const (
	snapshotInvalid snapshotState = iota
	snapshotDisabled
	snapshotEnabled
)

type Snapshot struct {
	state  snapshotState
	digest [sha256.Size]byte
}

func Disabled() Snapshot {
	return Snapshot{state: snapshotDisabled}
}

func FromEnvironment(name string, lookup LookupEnv) (Snapshot, error) {
	if lookup == nil {
		return Snapshot{}, ErrUnavailable
	}

	value, ok := lookup(name)
	if !ok || value == "" || strings.IndexByte(value, 0) >= 0 {
		return Snapshot{}, ErrUnavailable
	}

	return Snapshot{
		state:  snapshotEnabled,
		digest: sha256.Sum256([]byte(value)),
	}, nil
}

func Parse(r io.Reader) (Snapshot, error) {
	if r == nil {
		return Snapshot{}, ErrUnavailable
	}

	input, err := io.ReadAll(io.LimitReader(r, 67))
	defer clear(input)
	if err != nil {
		return Snapshot{}, ErrUnavailable
	}

	var token []byte
	switch {
	case len(input) == 64:
		token = input
	case len(input) == 65 && input[64] == '\n':
		token = input[:64]
	case len(input) == 66 && input[64] == '\r' && input[65] == '\n':
		token = input[:64]
	default:
		return Snapshot{}, ErrUnavailable
	}

	for _, b := range token {
		if !((b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')) {
			return Snapshot{}, ErrUnavailable
		}
	}

	return Snapshot{
		state:  snapshotEnabled,
		digest: sha256.Sum256(token),
	}, nil
}

func Generate(random io.Reader) ([]byte, error) {
	if random == nil {
		return nil, ErrUnavailable
	}

	var entropy [entropySize]byte
	defer clear(entropy[:])
	if _, err := io.ReadFull(random, entropy[:]); err != nil {
		return nil, ErrUnavailable
	}

	key := make([]byte, hex.EncodedLen(len(entropy))+1)
	hex.Encode(key[:len(key)-1], entropy[:])
	key[len(key)-1] = '\n'
	return key, nil
}

func (s Snapshot) Valid() bool {
	return s.state == snapshotDisabled || s.state == snapshotEnabled
}

func (s Snapshot) Enabled() bool {
	return s.state == snapshotEnabled
}

func (s Snapshot) Matches(token string) bool {
	switch s.state {
	case snapshotDisabled:
		return true
	case snapshotEnabled:
		digest := sha256.Sum256([]byte(token))
		return subtle.ConstantTimeCompare(s.digest[:], digest[:]) == 1
	default:
		return false
	}
}
