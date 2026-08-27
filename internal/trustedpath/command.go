// Package trustedpath retains and revalidates command-file identity evidence.
package trustedpath

import (
	"errors"
	"io/fs"
)

var (
	// ErrUnsafe is the fixed error for invalid or untrusted command evidence.
	ErrUnsafe = errors.New("command path is unsafe")
	// ErrMissing is the fixed error for an absent direct command path.
	ErrMissing = errors.New("command path is missing")
)

// CommandFileInspection is retained identity and optional bounded content.
type CommandFileInspection interface {
	Bytes() []byte
	FileInfo() fs.FileInfo
	Revalidate() error
	Close() error
}

// CommandReadMode selects identity-only or whole-file bounded inspection.
type CommandReadMode uint8

const (
	// CommandIdentityOnly retains identity without reading command content.
	CommandIdentityOnly CommandReadMode = iota + 1
	// CommandBoundedContent reads the whole command only when it fits the limit.
	CommandBoundedContent
)

// CommandPath records the validated lexical and resolved command spellings.
type CommandPath struct {
	Clean        string
	Resolved     string
	CanonicalKey string
}

type commandPathInspection interface {
	commandPath() CommandPath
}

// InspectionPath returns path metadata only for this package's inspections.
func InspectionPath(inspection CommandFileInspection) (CommandPath, bool) {
	provider, ok := inspection.(commandPathInspection)
	if !ok || provider == nil {
		return CommandPath{}, false
	}
	path := provider.commandPath()
	if path.Clean == "" || path.Resolved == "" {
		return CommandPath{}, false
	}
	return path, true
}

func validCommandRead(mode CommandReadMode, limit int64) bool {
	switch mode {
	case CommandIdentityOnly:
		return limit == 0
	case CommandBoundedContent:
		return limit > 0
	default:
		return false
	}
}
