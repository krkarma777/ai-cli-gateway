package gatewaykey

import (
	"io"
	"io/fs"
)

type snapshotParser func(io.Reader) (Snapshot, error)

// LoadFile validates and parses an owner-private key from one retained handle.
func LoadFile(path string, distinctFrom []fs.FileInfo) (Snapshot, error) {
	return loadFile(path, distinctFrom, Parse)
}
