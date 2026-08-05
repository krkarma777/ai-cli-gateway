package releasepack

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

type archiveEntry struct {
	Name       string
	SourcePath string
	Mode       fs.FileMode
	Directory  bool
}

type archiveSource struct {
	Info os.FileInfo
	Size int64
}

type plannedArchive struct {
	Asset   Asset
	Format  archiveFormat
	Entries []archiveEntry
}

type archiveFileHooks struct {
	createTemp  func(string, string) (*os.File, error)
	openFile    func(string) (*os.File, error)
	copyN       func(io.Writer, io.Reader, int64) (int64, error)
	closeFile   func(*os.File) error
	chmod       func(*os.File, fs.FileMode) error
	chmodRoot   func(*os.File, fs.FileMode) error
	link        func(string, string) error
	removeTemp  func(string) error
	removeOwned func(string) error
	removeRoot  func(string) error
}

func defaultArchiveFileHooks() archiveFileHooks {
	return archiveFileHooks{
		createTemp:  os.CreateTemp,
		openFile:    os.Open,
		copyN:       io.CopyN,
		closeFile:   (*os.File).Close,
		chmod:       (*os.File).Chmod,
		chmodRoot:   (*os.File).Chmod,
		link:        os.Link,
		removeTemp:  os.Remove,
		removeOwned: os.Remove,
		removeRoot:  os.Remove,
	}
}

var archiveFiles = defaultArchiveFileHooks()

// WriteArchives validates the fixed release inputs and publishes exactly one
// deterministic archive for every supported target.
func WriteArchives(options ArchiveOptions) (assets []Asset, resultErr error) {
	plan, err := newArchivePlan(options)
	if err != nil {
		return nil, err
	}

	archives, err := planArchives(plan)
	if err != nil {
		return nil, newArchiveFailure()
	}
	sources, err := snapshotArchiveSources(archives)
	if err != nil {
		return nil, newArchiveFailure()
	}

	hooks := archiveFiles
	rootInfo, createdRoot, err := prepareArchiveOutputRoot(plan.OutputRoot)
	if err != nil {
		return nil, newArchiveFailure()
	}
	owned := newOwnedArchivePaths(plan.OutputRoot, rootInfo, createdRoot)
	succeeded := false
	defer func() {
		if !succeeded {
			assets = nil
			if cleanupSucceeded := owned.cleanup(hooks); !cleanupSucceeded || resultErr == nil {
				resultErr = newArchiveFailure()
			}
		}
	}()
	if createdRoot {
		rootInfo, err = secureCreatedArchiveOutputRoot(plan.OutputRoot, rootInfo, hooks)
		if err != nil {
			return nil, newArchiveFailure()
		}
		owned.rootInfo = rootInfo
	}

	assets = make([]Asset, 0, len(archives))
	for _, archive := range archives {
		if !sameArchiveOutputRoot(plan.OutputRoot, rootInfo) {
			return nil, newArchiveFailure()
		}
		temporary, createErr := hooks.createTemp(plan.OutputRoot, ".release-archive-*")
		if createErr != nil {
			return nil, newArchiveFailure()
		}
		temporaryPath := temporary.Name()
		if !isDirectArchiveOutput(plan.OutputRoot, temporaryPath) {
			_ = temporary.Close()
			return nil, newArchiveFailure()
		}
		temporaryInfo, statErr := temporary.Stat()
		if statErr != nil || !temporaryInfo.Mode().IsRegular() {
			_ = temporary.Close()
			return nil, newArchiveFailure()
		}
		pathInfo, statErr := os.Lstat(temporaryPath)
		if statErr != nil || !os.SameFile(temporaryInfo, pathInfo) {
			_ = temporary.Close()
			return nil, newArchiveFailure()
		}
		owned.track(temporaryPath, temporaryInfo)
		if !sameArchiveOutputRoot(plan.OutputRoot, rootInfo) {
			_ = temporary.Close()
			return nil, newArchiveFailure()
		}

		writeErr := writePlannedArchive(temporary, archive, plan.SourceTime, sources, hooks)
		if writeErr != nil {
			_ = temporary.Close()
			return nil, newArchiveFailure()
		}
		if err := hooks.chmod(temporary, 0o644); err != nil {
			_ = temporary.Close()
			return nil, newArchiveFailure()
		}
		if err := hooks.closeFile(temporary); err != nil {
			_ = temporary.Close()
			return nil, newArchiveFailure()
		}
		if !owned.same(temporaryPath) || !sameArchiveOutputRoot(plan.OutputRoot, rootInfo) {
			return nil, newArchiveFailure()
		}

		if err := hooks.link(temporaryPath, archive.Asset.Path); err != nil {
			return nil, newArchiveFailure()
		}
		finalInfo, statErr := os.Lstat(archive.Asset.Path)
		if statErr != nil || !os.SameFile(temporaryInfo, finalInfo) {
			return nil, newArchiveFailure()
		}
		owned.track(archive.Asset.Path, temporaryInfo)
		if !owned.same(temporaryPath) || !sameArchiveOutputRoot(plan.OutputRoot, rootInfo) {
			return nil, newArchiveFailure()
		}
		if err := hooks.removeTemp(temporaryPath); err != nil {
			return nil, newArchiveFailure()
		}
		owned.untrack(temporaryPath)
		assets = append(assets, archive.Asset)
	}

	if !sameArchiveOutputRoot(plan.OutputRoot, rootInfo) || !owned.allSame() {
		return nil, newArchiveFailure()
	}
	succeeded = true
	return assets, nil
}

func planArchives(plan archivePlan) ([]plannedArchive, error) {
	if len(plan.Targets) != len(plan.Binaries) {
		return nil, errors.New("target and binary counts differ")
	}
	archives := make([]plannedArchive, 0, len(plan.Targets))
	for i, releaseTarget := range plan.Targets {
		binary := plan.Binaries[i]
		if binary.Target != releaseTarget {
			return nil, errors.New("target and binary order differs")
		}

		base := "ai-cli-gateway_" + plan.Version + "_" + releaseTarget.GOOS + "_" + releaseTarget.GOARCH
		extension := ".tar.gz"
		if releaseTarget.Format == formatZIP {
			extension = ".zip"
		} else if releaseTarget.Format != formatTarGzip {
			return nil, errors.New("unknown archive format")
		}
		assetName := base + extension
		entries, err := planArchiveEntries(base, releaseTarget, binary, plan.Sources)
		if err != nil {
			return nil, err
		}
		archives = append(archives, plannedArchive{
			Asset:   Asset{Name: assetName, Path: filepath.Join(plan.OutputRoot, assetName)},
			Format:  releaseTarget.Format,
			Entries: entries,
		})
	}
	return archives, nil
}

func planArchiveEntries(base string, releaseTarget target, binary stagedBinary, sources sourceSet) ([]archiveEntry, error) {
	entries := []archiveEntry{
		{Name: base + "/", Mode: 0o755, Directory: true},
	}
	executableMode := fs.FileMode(0o755)
	if releaseTarget.GOOS == "windows" {
		executableMode = 0o644
	}
	entries = append(entries, archiveEntry{
		Name:       path.Join(base, releaseTarget.Executable),
		SourcePath: binary.Path,
		Mode:       executableMode,
	})

	for _, source := range sources.Common {
		if source.Name == "go.mod" {
			continue
		}
		entries = append(entries, archiveEntry{
			Name:       path.Join(base, source.Name),
			SourcePath: source.Path,
			Mode:       0o644,
		})
	}
	if releaseTarget.IncludeSystemd {
		entries = append(entries, archiveEntry{
			Name:       path.Join(base, sources.Systemd.Name),
			SourcePath: sources.Systemd.Path,
			Mode:       0o644,
		})
	}

	directories := make(map[string]struct{})
	for _, entry := range entries {
		if entry.Directory {
			directories[entry.Name] = struct{}{}
			continue
		}
		for parent := path.Dir(entry.Name); parent != "."; parent = path.Dir(parent) {
			name := parent + "/"
			if _, exists := directories[name]; exists {
				break
			}
			directories[name] = struct{}{}
			entries = append(entries, archiveEntry{Name: name, Mode: 0o755, Directory: true})
		}
	}

	slices.SortFunc(entries, func(first, second archiveEntry) int {
		return strings.Compare(first.Name, second.Name)
	})
	for i, entry := range entries {
		if err := validateArchiveEntry(entry); err != nil {
			return nil, err
		}
		if i > 0 && entries[i-1].Name == entry.Name {
			return nil, errors.New("duplicate archive entry")
		}
	}
	return entries, nil
}

func validateArchiveEntry(entry archiveEntry) error {
	cleanName := strings.TrimSuffix(entry.Name, "/")
	if cleanName == "" || strings.HasPrefix(entry.Name, "/") || strings.Contains(entry.Name, "\\") || path.Clean(cleanName) != cleanName {
		return errors.New("unsafe archive entry")
	}
	for _, component := range strings.Split(cleanName, "/") {
		if component == "" || component == "." || component == ".." {
			return errors.New("unsafe archive entry")
		}
	}
	if entry.Directory {
		if !strings.HasSuffix(entry.Name, "/") || entry.SourcePath != "" || entry.Mode.Perm() != 0o755 {
			return errors.New("invalid archive directory")
		}
		return nil
	}
	if strings.HasSuffix(entry.Name, "/") || entry.SourcePath == "" || (entry.Mode.Perm() != 0o644 && entry.Mode.Perm() != 0o755) {
		return errors.New("invalid archive file")
	}
	return nil
}

func snapshotArchiveSources(archives []plannedArchive) (map[string]archiveSource, error) {
	sources := make(map[string]archiveSource)
	for _, archive := range archives {
		for _, entry := range archive.Entries {
			if entry.Directory {
				continue
			}
			if _, exists := sources[entry.SourcePath]; exists {
				continue
			}
			info, err := os.Lstat(entry.SourcePath)
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 0 {
				return nil, errors.New("archive source changed")
			}
			sources[entry.SourcePath] = archiveSource{Info: info, Size: info.Size()}
		}
	}
	return sources, nil
}

func prepareArchiveOutputRoot(outputRoot string) (os.FileInfo, bool, error) {
	info, err := os.Lstat(outputRoot)
	if errors.Is(err, os.ErrNotExist) {
		if err := os.Mkdir(outputRoot, 0o700); err != nil {
			return nil, false, err
		}
		info, err := os.Lstat(outputRoot)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, true, errors.New("created output root changed")
		}
		return info, true, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return nil, false, errors.New("output root changed")
	}
	if err := validateOutputAuthority(outputRoot, info); err != nil {
		return nil, false, errors.New("output root authority changed")
	}
	entries, err := os.ReadDir(outputRoot)
	if err != nil || len(entries) != 0 {
		return nil, false, errors.New("output root changed")
	}
	return info, false, nil
}

func secureCreatedArchiveOutputRoot(outputRoot string, createdInfo os.FileInfo, hooks archiveFileHooks) (os.FileInfo, error) {
	if err := os.Chmod(outputRoot, 0o700); err != nil {
		return nil, err
	}
	directory, err := os.Open(outputRoot)
	if err != nil {
		return nil, err
	}
	if err := hooks.chmodRoot(directory, 0o700); err != nil {
		_ = directory.Close()
		return nil, err
	}
	descriptorInfo, statErr := directory.Stat()
	closeErr := directory.Close()
	pathInfo, pathErr := os.Lstat(outputRoot)
	if statErr != nil || closeErr != nil || pathErr != nil || !os.SameFile(createdInfo, descriptorInfo) ||
		!os.SameFile(descriptorInfo, pathInfo) || validateOutputAuthority(outputRoot, descriptorInfo) != nil ||
		validateOutputAuthority(outputRoot, pathInfo) != nil {
		return nil, errors.New("created output root changed")
	}
	return descriptorInfo, nil
}

func writePlannedArchive(output io.Writer, archive plannedArchive, sourceTime time.Time, sources map[string]archiveSource, hooks archiveFileHooks) error {
	switch archive.Format {
	case formatTarGzip:
		return writeTarGzip(output, archive.Entries, sourceTime, sources, hooks)
	case formatZIP:
		return writeZIP(output, archive.Entries, sourceTime, sources, hooks)
	default:
		return errors.New("unknown archive format")
	}
}

func copyArchiveSource(destination io.Writer, entry archiveEntry, source archiveSource, hooks archiveFileHooks) error {
	file, err := hooks.openFile(entry.SourcePath)
	if err != nil {
		return errors.New("open archive source")
	}
	defer file.Close()

	before, err := file.Stat()
	if err != nil || !before.Mode().IsRegular() || !os.SameFile(source.Info, before) || before.Size() != source.Size {
		return errors.New("archive source identity changed")
	}
	copied, err := hooks.copyN(destination, file, source.Size)
	if err != nil || copied != source.Size {
		return errors.New("copy archive source")
	}
	var probe [1]byte
	count, probeErr := file.Read(probe[:])
	if count != 0 || !errors.Is(probeErr, io.EOF) {
		return errors.New("archive source grew")
	}
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(source.Info, after) || after.Size() != source.Size {
		return errors.New("archive source changed during copy")
	}
	return nil
}

type ownedArchivePaths struct {
	outputRoot  string
	rootInfo    os.FileInfo
	removeRoot  bool
	order       []string
	ownedByPath map[string]os.FileInfo
}

func newOwnedArchivePaths(outputRoot string, rootInfo os.FileInfo, removeRoot bool) *ownedArchivePaths {
	return &ownedArchivePaths{
		outputRoot:  outputRoot,
		rootInfo:    rootInfo,
		removeRoot:  removeRoot,
		ownedByPath: make(map[string]os.FileInfo),
	}
}

func (owned *ownedArchivePaths) track(name string, info os.FileInfo) {
	if _, exists := owned.ownedByPath[name]; exists {
		return
	}
	owned.ownedByPath[name] = info
	owned.order = append(owned.order, name)
}

func (owned *ownedArchivePaths) untrack(name string) {
	delete(owned.ownedByPath, name)
}

func (owned *ownedArchivePaths) cleanup(hooks archiveFileHooks) bool {
	rootRemoved := !owned.removeRoot
	complete := true
	for pass := 0; pass < 2; pass++ {
		for i := len(owned.order) - 1; i >= 0; i-- {
			name := owned.order[i]
			want, exists := owned.ownedByPath[name]
			if !exists {
				continue
			}
			current, err := os.Lstat(name)
			if errors.Is(err, os.ErrNotExist) {
				owned.untrack(name)
				continue
			}
			if err != nil {
				complete = false
				continue
			}
			if !os.SameFile(want, current) {
				complete = false
				owned.untrack(name)
				continue
			}
			if err := hooks.removeOwned(name); err == nil || errors.Is(err, os.ErrNotExist) {
				owned.untrack(name)
			}
		}

		if owned.removeRoot && len(owned.ownedByPath) == 0 {
			current, err := os.Lstat(owned.outputRoot)
			switch {
			case errors.Is(err, os.ErrNotExist):
				rootRemoved = true
			case err == nil && os.SameFile(owned.rootInfo, current):
				if err := hooks.removeRoot(owned.outputRoot); err == nil || errors.Is(err, os.ErrNotExist) {
					rootRemoved = true
				}
			}
		}
	}
	if entries, err := os.ReadDir(owned.outputRoot); err == nil && len(entries) != 0 {
		complete = false
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		complete = false
	}
	return complete && len(owned.ownedByPath) == 0 && rootRemoved
}

func (owned *ownedArchivePaths) same(name string) bool {
	want, exists := owned.ownedByPath[name]
	if !exists {
		return false
	}
	current, err := os.Lstat(name)
	return err == nil && os.SameFile(want, current)
}

func (owned *ownedArchivePaths) allSame() bool {
	for name := range owned.ownedByPath {
		if !owned.same(name) {
			return false
		}
	}
	return true
}

func sameArchiveOutputRoot(outputRoot string, want os.FileInfo) bool {
	current, err := os.Lstat(outputRoot)
	return err == nil && os.SameFile(want, current) && validateOutputAuthority(outputRoot, current) == nil
}

func isDirectArchiveOutput(outputRoot, name string) bool {
	return filepath.IsAbs(name) && filepath.Clean(name) == name && filepath.Dir(name) == outputRoot
}
