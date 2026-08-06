package sdkcontract

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const portAllocatorCode = "import socket\ns=socket.socket(socket.AF_INET,socket.SOCK_STREAM)\ns.bind(('127.0.0.1',0))\nprint(s.getsockname()[1])\ns.close()\n"

var allocatedPortPattern = regexp.MustCompile(`^[1-9][0-9]{0,4}\n$`)

type pathIdentity struct {
	original   string
	aliasInfo  fs.FileInfo
	target     string
	targetInfo fs.FileInfo
	targetHash [sha256.Size]byte
	javascript bool
}

type groupCommandResult struct{ stdout, stderr []byte }

type realSystem struct {
	mu         sync.Mutex
	options    Options
	python     pathIdentity
	node       pathIdentity
	javascript pathIdentity
	repository fs.FileInfo
	module     fs.FileInfo
	validated  bool
}

func init() {
	productionSystemFactory = func() system { return &realSystem{} }
}

func (s *realSystem) Supported() bool { return platformSupported() }

func (s *realSystem) ValidateOptions(options Options) error {
	if !s.Supported() {
		return newError(categoryUnsupported)
	}
	repository, module, err := validateRepository(options.RepositoryRoot)
	if err != nil {
		return err
	}
	python, err := validateExecutableIdentity(options.PythonExecutable)
	if err != nil {
		return err
	}
	node, err := validateExecutableIdentity(options.NodeExecutable)
	if err != nil {
		return err
	}
	javascript, err := validateJavaScriptIdentity(options.JavaScriptEntrypoint)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.options = options
	s.python = python
	s.node = node
	s.javascript = javascript
	s.repository = repository
	s.module = module
	s.validated = true
	s.mu.Unlock()
	return nil
}

func (s *realSystem) MkdirTemp(parent, pattern string) (ownedRoot, error) {
	return createOwnedRoot(parent, pattern)
}

func (s *realSystem) MkdirAll(path string, mode fs.FileMode) error {
	if mode != 0o700 {
		return newError(categoryInvalid)
	}
	return makePrivateTree(path)
}

func (s *realSystem) WriteFile(path string, data []byte, mode fs.FileMode) error {
	return writePrivateFile(path, data, mode)
}

func (s *realSystem) ReadRandom(destination []byte) (int, error) {
	return io.ReadFull(rand.Reader, destination)
}

type realTimer struct{ timer *time.Timer }

func (t realTimer) C() <-chan time.Time { return t.timer.C }
func (t realTimer) Stop() bool          { return t.timer.Stop() }
func (s *realSystem) NewTimer(duration time.Duration) timer {
	return realTimer{timer: time.NewTimer(duration)}
}

func (s *realSystem) Build(ctx context.Context, repositoryRoot, output, packagePath string, grace time.Duration) error {
	if packagePath != "./cmd/ai-cli-gateway" && packagePath != "./internal/testcli/cmd/fake-codex-cli" {
		return newError(categoryInvalid)
	}
	if err := s.revalidateRepository(repositoryRoot); err != nil {
		return err
	}
	goExecutable, err := resolveBuildTool(exec.LookPath)
	if err != nil {
		return err
	}
	environment := minimalBuildEnvironment()
	_, err = runGroupCommand(ctx, goExecutable, repositoryRoot,
		[]string{"build", "-trimpath", "-o", output, packagePath}, environment, grace, 0, 8<<10)
	if err != nil {
		return err
	}
	info, err := os.Lstat(output)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 {
		return newError(categoryFailed)
	}
	return nil
}

func resolveBuildTool(lookup func(string) (string, error)) (string, error) {
	if lookup == nil {
		return "", newError(categoryFailed)
	}
	path, err := lookup("go")
	if err != nil {
		return "", newError(categoryFailed)
	}
	if !filepath.IsAbs(path) {
		path, err = filepath.Abs(path)
		if err != nil {
			return "", newError(categoryFailed)
		}
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil || !filepath.IsAbs(resolved) {
		return "", newError(categoryFailed)
	}
	info, err := os.Lstat(resolved)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 || info.Mode().Perm()&0o022 != 0 || info.Mode()&(os.ModeSetuid|os.ModeSetgid|os.ModeSticky) != 0 {
		return "", newError(categoryFailed)
	}
	return resolved, nil
}

func minimalBuildEnvironment() []string {
	values := make([]string, 0, 4)
	for _, name := range []string{"PATH", "HOME", "GOCACHE", "GOMODCACHE"} {
		if value, ok := os.LookupEnv(name); ok && value != "" {
			values = append(values, name+"="+value)
		}
	}
	return values
}

func (s *realSystem) AllocatePort(ctx context.Context, executable string, grace time.Duration) (uint16, error) {
	identity, ok := s.executableIdentity(executable, true)
	if !ok || revalidateExecutableIdentity(identity) != nil {
		return 0, newError(categoryInvalid)
	}
	result, err := runGroupCommand(ctx, executable, "", []string{"-I", "-c", portAllocatorCode}, []string{}, grace, 6, 8<<10)
	if err != nil {
		return 0, err
	}
	return parseAllocatedPort(result.stdout)
}

func parseAllocatedPort(output []byte) (uint16, error) {
	if !allocatedPortPattern.Match(output) {
		return 0, newError(categoryFailed)
	}
	value, err := strconv.ParseUint(strings.TrimSuffix(string(output), "\n"), 10, 16)
	if err != nil || value == 0 || value > 65535 {
		return 0, newError(categoryFailed)
	}
	return uint16(value), nil
}

func (s *realSystem) StartFixtureRegistry(path string, grace time.Duration) (fixtureRegistry, error) {
	return startPlatformRegistry(path, grace)
}

func (s *realSystem) StartGateway(executable, directory string, argv, environment []string, output io.Writer) (child, error) {
	if err := s.revalidateRepository(directory); err != nil {
		return nil, err
	}
	identity, err := validateExecutableIdentity(executable)
	if err != nil || revalidateExecutableIdentity(identity) != nil {
		return nil, newError(categoryInvalid)
	}
	return startPlatformChild(executable, directory, argv, environment, output)
}

func (s *realSystem) ProbeModels(ctx context.Context, baseURL, key string) (int, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(baseURL, "/")+"/models", nil)
	if err != nil {
		return 0, newError(categoryFailed)
	}
	if key != "" {
		request.Header.Set("Authorization", "Bearer "+key)
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport}
	response, err := client.Do(request)
	if err != nil {
		return 0, newError(categoryFailed)
	}
	defer func() { _ = response.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8<<10))
	return response.StatusCode, nil
}

func (s *realSystem) RunClient(ctx context.Context, executable string, argv, environment []string, grace time.Duration) ([]byte, error) {
	identity, isPython := s.executableIdentity(executable, true)
	if !isPython {
		identity, isPython = s.executableIdentity(executable, false)
		if !isPython {
			return nil, newError(categoryInvalid)
		}
	}
	if err := revalidateExecutableIdentity(identity); err != nil {
		return nil, err
	}
	s.mu.Lock()
	options := s.options
	javascript := s.javascript
	s.mu.Unlock()
	if executable == options.PythonExecutable {
		want := []string{"-I", filepath.Join(options.RepositoryRoot, "examples/openai-sdk/python/main.py")}
		if !equalStrings(argv, want) {
			return nil, newError(categoryInvalid)
		}
	} else {
		if len(argv) != 1 || argv[0] != options.JavaScriptEntrypoint || revalidateJavaScriptIdentity(javascript) != nil {
			return nil, newError(categoryInvalid)
		}
	}
	return runSDKClientCommand(ctx, executable, "", argv, environment, grace)
}

func runSDKClientCommand(
	ctx context.Context,
	executable, directory string,
	argv, environment []string,
	grace time.Duration,
) ([]byte, error) {
	result, err := runGroupCommand(ctx, executable, directory, argv, environment, grace, 8<<10, 8<<10)
	if err != nil {
		return nil, err
	}
	if len(result.stderr) != 0 {
		return nil, newError(categoryFailed)
	}
	return result.stdout, nil
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (s *realSystem) executableIdentity(path string, python bool) (pathIdentity, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.validated {
		return pathIdentity{}, false
	}
	if python && path == s.options.PythonExecutable {
		return s.python, true
	}
	if !python && path == s.options.NodeExecutable {
		return s.node, true
	}
	return pathIdentity{}, false
}

func (s *realSystem) revalidateRepository(path string) error {
	s.mu.Lock()
	if !s.validated || path != s.options.RepositoryRoot {
		s.mu.Unlock()
		return newError(categoryInvalid)
	}
	wantRoot, wantModule := s.repository, s.module
	s.mu.Unlock()
	root, module, err := validateRepository(path)
	if err != nil || !os.SameFile(root, wantRoot) || !os.SameFile(module, wantModule) {
		return newError(categoryInvalid)
	}
	return nil
}

func validateRepository(path string) (fs.FileInfo, fs.FileInfo, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, nil, newError(categoryInvalid)
	}
	if err := validateSecureAncestors(path); err != nil {
		return nil, nil, err
	}
	root, err := os.Lstat(path)
	if err != nil || root.Mode()&os.ModeSymlink != 0 || !root.IsDir() {
		return nil, nil, newError(categoryInvalid)
	}
	modulePath := filepath.Join(path, "go.mod")
	module, err := os.Lstat(modulePath)
	if err != nil || module.Mode()&os.ModeSymlink != 0 || !module.Mode().IsRegular() {
		return nil, nil, newError(categoryInvalid)
	}
	contents, err := os.ReadFile(modulePath) //nolint:gosec // fixed child of a validated repository root.
	if err != nil || !exactModuleDeclaration(contents) {
		return nil, nil, newError(categoryInvalid)
	}
	return root, module, nil
}

func exactModuleDeclaration(contents []byte) bool {
	found := false
	for _, line := range strings.Split(string(contents), "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) == 0 || fields[0] != "module" {
			continue
		}
		if found || len(fields) != 2 || fields[1] != "github.com/krkarma777/ai-cli-gateway" {
			return false
		}
		found = true
	}
	return found
}

func makePrivateTree(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return newError(categoryInvalid)
	}
	missing := make([]string, 0, 4)
	current := path
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if !privateDirectory(info) {
				return newError(categoryFailed)
			}
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return newError(categoryFailed)
		}
		missing = append(missing, current)
		parent := filepath.Dir(current)
		if parent == current {
			return newError(categoryFailed)
		}
		current = parent
	}
	for index := len(missing) - 1; index >= 0; index-- {
		candidate := missing[index]
		if err := os.Mkdir(candidate, 0o700); err != nil {
			return newError(categoryFailed)
		}
		if err := os.Chmod(candidate, 0o700); err != nil { // #nosec G302 -- bootstrap access before descriptor chmod and identity verification.
			return newError(categoryFailed)
		}
		// #nosec G304 -- candidate is the exact directory just created under the private owned root.
		handle, err := os.Open(candidate)
		if err != nil {
			return newError(categoryFailed)
		}
		chmodErr := handle.Chmod(0o700)
		handleInfo, statErr := handle.Stat()
		closeErr := handle.Close()
		pathInfo, pathErr := os.Lstat(candidate)
		if chmodErr != nil || statErr != nil || closeErr != nil || pathErr != nil || !privateDirectory(handleInfo) || !privateDirectory(pathInfo) || !os.SameFile(handleInfo, pathInfo) {
			return newError(categoryFailed)
		}
	}
	return nil
}
