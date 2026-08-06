package releasepack

import "time"

// ArchiveOptions identifies the complete, closed set of inputs used to build
// release archives.
type ArchiveOptions struct {
	RepositoryRoot string
	StagingRoot    string
	OutputRoot     string
	Tag            string
	SourceTime     time.Time
}

// Asset identifies a generated release asset.
type Asset struct {
	Name string
	Path string
}

type archiveFormat uint8

const (
	formatTarGzip archiveFormat = iota + 1
	formatZIP
)

type target struct {
	GOOS           string
	GOARCH         string
	Directory      string
	Executable     string
	Format         archiveFormat
	IncludeSystemd bool
}

var releaseTargets = [...]target{
	{GOOS: "linux", GOARCH: "amd64", Directory: "linux_amd64", Executable: "ai-cli-gateway", Format: formatTarGzip, IncludeSystemd: true},
	{GOOS: "linux", GOARCH: "arm64", Directory: "linux_arm64", Executable: "ai-cli-gateway", Format: formatTarGzip, IncludeSystemd: true},
	{GOOS: "darwin", GOARCH: "amd64", Directory: "darwin_amd64", Executable: "ai-cli-gateway", Format: formatTarGzip},
	{GOOS: "darwin", GOARCH: "arm64", Directory: "darwin_arm64", Executable: "ai-cli-gateway", Format: formatTarGzip},
	{GOOS: "windows", GOARCH: "amd64", Directory: "windows_amd64", Executable: "ai-cli-gateway.exe", Format: formatZIP},
}

type sourceFile struct {
	Name string
	Path string
}

type sourceSet struct {
	Common  []sourceFile
	Systemd sourceFile
}

type stagedBinary struct {
	Target target
	Path   string
}

type archivePlan struct {
	RepositoryRoot string
	StagingRoot    string
	OutputRoot     string
	Tag            string
	Version        string
	SourceTime     time.Time
	Sources        sourceSet
	Binaries       []stagedBinary
	Targets        []target
}
