// Package configsource binds decoded configuration and source evidence to one
// retained startup handle.
package configsource

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/krkarma777/ai-cli-gateway/internal/config"
)

const maxSourceBytes = 1 << 20

// ErrUnavailable is the fixed path-free result for unavailable or unstable
// configuration source evidence.
var ErrUnavailable = errors.New("configuration source is unavailable")

type sourceOpener func(string) (*os.File, error)

// Snapshot retains the one handle that supplied its decoded configuration and
// identity evidence.
type Snapshot struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	info   fs.FileInfo
	digest [sha256.Size]byte
	config config.Config
}

// Load opens, decodes, fingerprints, and retains one stable configuration
// source.
func Load(path string) (*Snapshot, error) {
	return loadWithOpen(path, openSourceFile)
}

func loadWithOpen(path string, open sourceOpener) (*Snapshot, error) {
	clean, ok := cleanSourcePath(path)
	if !ok || open == nil {
		return nil, ErrUnavailable
	}
	file, err := open(clean)
	if err != nil || file == nil {
		if file != nil {
			_ = file.Close()
		}
		return nil, ErrUnavailable
	}
	retained := false
	defer func() {
		if !retained {
			_ = file.Close()
		}
	}()

	info, ok := stableSourceInfo(clean, file, nil)
	if !ok {
		return nil, ErrUnavailable
	}
	raw, ok := readSourceBytes(file)
	if !ok {
		return nil, ErrUnavailable
	}
	defer clear(raw)
	decoded, err := config.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, ErrUnavailable
	}
	digest := sha256.Sum256(raw)
	if !revalidateSource(clean, file, info, digest) {
		return nil, ErrUnavailable
	}

	retained = true
	return &Snapshot{
		file: file, path: clean, info: info, digest: digest, config: cloneConfig(decoded),
	}, nil
}

// Config returns a deep defensive copy, or the zero configuration when the
// snapshot is nil, zero, or closed.
func (s *Snapshot) Config() config.Config {
	if s == nil {
		return config.Config{}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil || s.info == nil {
		return config.Config{}
	}
	return cloneConfig(s.config)
}

// FileInfo returns identity evidence from the retained source handle. It
// returns nil after close and for nil or zero snapshots.
func (s *Snapshot) FileInfo() fs.FileInfo {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	return s.info
}

// Revalidate checks the retained handle, selected path, and original digest
// without replacing the decoded configuration.
func (s *Snapshot) Revalidate() error {
	if s == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil || s.info == nil || s.path == "" ||
		!revalidateSource(s.path, s.file, s.info, s.digest) {
		return ErrUnavailable
	}
	return nil
}

// Close releases retained source evidence. A nil, zero, or already closed
// snapshot fails closed.
func (s *Snapshot) Close() error {
	if s == nil {
		return ErrUnavailable
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return ErrUnavailable
	}
	err := s.file.Close()
	s.file = nil
	s.path = ""
	s.info = nil
	clear(s.digest[:])
	s.config = config.Config{}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func cleanSourcePath(path string) (string, bool) {
	if path == "" || strings.IndexByte(path, 0) >= 0 {
		return "", false
	}
	absolute, err := filepath.Abs(path)
	if err != nil || absolute == "" || strings.IndexByte(absolute, 0) >= 0 {
		return "", false
	}
	clean := filepath.Clean(absolute)
	return clean, filepath.IsAbs(clean)
}

func readSourceBytes(file *os.File) ([]byte, bool) {
	if file == nil {
		return nil, false
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, false
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxSourceBytes+1))
	if err != nil || len(raw) > maxSourceBytes {
		clear(raw)
		return nil, false
	}
	return raw, true
}

func revalidateSource(
	path string,
	file *os.File,
	retained fs.FileInfo,
	digest [sha256.Size]byte,
) bool {
	if _, ok := stableSourceInfo(path, file, retained); !ok {
		return false
	}
	raw, ok := readSourceBytes(file)
	if !ok {
		return false
	}
	currentDigest := sha256.Sum256(raw)
	clear(raw)
	if currentDigest != digest {
		return false
	}
	_, ok = stableSourceInfo(path, file, retained)
	return ok
}

func stableSourceInfo(
	path string,
	file *os.File,
	retained fs.FileInfo,
) (fs.FileInfo, bool) {
	if file == nil || !platformSourceStable(path, file) {
		return nil, false
	}
	handleInfo, err := file.Stat()
	if err != nil || !handleInfo.Mode().IsRegular() {
		return nil, false
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !pathInfo.Mode().IsRegular() ||
		!os.SameFile(handleInfo, pathInfo) {
		return nil, false
	}
	if retained != nil && !os.SameFile(retained, handleInfo) {
		return nil, false
	}
	return handleInfo, true
}

func cloneConfig(cfg config.Config) config.Config {
	providers := make(map[string]config.Provider, len(cfg.Providers))
	for name, providerConfig := range cfg.Providers {
		providerConfig.PrefixArgs = slices.Clone(providerConfig.PrefixArgs)
		providerConfig.CredentialEnv = slices.Clone(providerConfig.CredentialEnv)
		providers[name] = providerConfig
	}
	cfg.Providers = providers
	cfg.Models = slices.Clone(cfg.Models)
	return cfg
}
