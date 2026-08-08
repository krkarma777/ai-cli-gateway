package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"path"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/krkarma777/ai-cli-gateway/internal/core"
	"github.com/pelletier/go-toml/v2"
)

const (
	maxConfigBytes = 1 << 20
	maxModels      = 1_024

	maxHTTPBodyBytes     = 16 << 20
	maxInputBytes        = 16 << 20
	maxInstructionsBytes = 16 << 20
	maxSchemaBytes       = 1 << 20
	maxFinalBytes        = 16 << 20
	maxStdoutBytes       = 64 << 20
	maxStderrBytes       = 16 << 20
	maxHeaderBytes       = 1 << 20

	maxHandlerLimit          = 4_096
	maxBodyReaderLimit       = 256
	maxConcurrency           = 64
	maxQueueSize             = 4_096
	maxQueueBytes      int64 = 1 << 30

	maxConfiguredDuration = 24 * time.Hour
	maxPrefixArgBytes     = 4_096
)

var (
	// ErrConfigTooLarge is returned both when the bounded input cannot be read
	// and when it exceeds one MiB. It never includes reader-controlled text.
	ErrConfigTooLarge = errors.New("configuration input is unreadable or exceeds 1 MiB")

	errInvalidTOML = errors.New("decode config: invalid TOML")

	environmentNamePattern = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)

	allowedCredentialEnv = map[string]map[string]struct{}{
		"codex": {},
		"claude": {
			"ANTHROPIC_API_KEY": {},
		},
		"gemini": {
			"GEMINI_API_KEY":                 {},
			"GOOGLE_API_KEY":                 {},
			"GOOGLE_APPLICATION_CREDENTIALS": {},
			"GOOGLE_CLOUD_PROJECT":           {},
			"GOOGLE_CLOUD_LOCATION":          {},
		},
	}
)

type wireConfig struct {
	Server    wireServer              `toml:"server"`
	Runtime   wireRuntime             `toml:"runtime"`
	Providers map[string]wireProvider `toml:"providers"`
	Models    []Model                 `toml:"models"`
}

type wireServer struct {
	Listen            *string   `toml:"listen"`
	APIKeyEnv         *string   `toml:"api_key_env"`
	APIKeyFile        *string   `toml:"api_key_file"`
	HTTPBodyBytes     *int64    `toml:"http_body_bytes"`
	InputBytes        *int64    `toml:"input_bytes"`
	InstructionsBytes *int64    `toml:"instructions_bytes"`
	SchemaBytes       *int64    `toml:"schema_bytes"`
	HandlerLimit      *int64    `toml:"handler_limit"`
	BodyReaderLimit   *int64    `toml:"body_reader_limit"`
	MaxHeaderBytes    *int64    `toml:"max_header_bytes"`
	ReadHeaderTimeout *Duration `toml:"read_header_timeout"`
	BodyReadTimeout   *Duration `toml:"body_read_timeout"`
	IdleTimeout       *Duration `toml:"idle_timeout"`
	ShutdownTimeout   *Duration `toml:"shutdown_timeout"`
}

type wireRuntime struct {
	Root           *string   `toml:"root"`
	TermGrace      *Duration `toml:"term_grace"`
	CleanupTimeout *Duration `toml:"cleanup_timeout"`
	StdoutBytes    *int64    `toml:"stdout_bytes"`
	StderrBytes    *int64    `toml:"stderr_bytes"`
	FinalBytes     *int64    `toml:"final_bytes"`
}

type wireProvider struct {
	Executable       string    `toml:"executable"`
	PrefixArgs       []string  `toml:"prefix_args"`
	ConfigHome       string    `toml:"config_home"`
	CredentialEnv    []string  `toml:"credential_env"`
	Concurrency      *int64    `toml:"concurrency"`
	QueueSize        *int64    `toml:"queue_size"`
	QueueBytes       *int64    `toml:"queue_bytes"`
	QueueTimeout     *Duration `toml:"queue_timeout"`
	ExecutionTimeout *Duration `toml:"execution_timeout"`
}

// Load opens and decodes path without exposing path-controlled text in errors.
func Load(path string) (Config, error) {
	// Load's API deliberately accepts a caller-selected configuration path.
	//nolint:gosec
	file, err := os.Open(path)
	if err != nil {
		return Config{}, errors.New("open config: failed")
	}
	defer func() {
		_ = file.Close()
	}()
	return Decode(file)
}

// Decode strictly decodes, normalizes, and structurally validates TOML.
func Decode(r io.Reader) (Config, error) {
	if r == nil {
		return Config{}, ErrConfigTooLarge
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxConfigBytes+1))
	if err != nil || len(raw) > maxConfigBytes {
		return Config{}, ErrConfigTooLarge
	}

	var wire wireConfig
	decoder := toml.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&wire); err != nil {
		return Config{}, errInvalidTOML
	}

	cfg, err := normalize(wire)
	if err != nil {
		return Config{}, err
	}
	if err := validate(cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func normalize(wire wireConfig) (Config, error) {
	server, err := normalizeServer(wire.Server)
	if err != nil {
		return Config{}, err
	}
	runtimeConfig, err := normalizeRuntime(wire.Runtime)
	if err != nil {
		return Config{}, err
	}

	providers := make(map[string]Provider, len(wire.Providers))
	for name, provider := range wire.Providers {
		normalized, err := normalizeProvider(provider)
		if err != nil {
			return Config{}, err
		}
		providers[name] = normalized
	}

	return Config{
		Server:    server,
		Runtime:   runtimeConfig,
		Providers: providers,
		Models:    append([]Model(nil), wire.Models...),
	}, nil
}

func normalizeServer(wire wireServer) (Server, error) {
	if wire.APIKeyEnv != nil && wire.APIKeyFile != nil {
		return Server{}, invalidConfig("server API key source")
	}
	apiKeyFile := ""
	if wire.APIKeyFile != nil {
		apiKeyFile = *wire.APIKeyFile
		if !isAbsolutePath(runtime.GOOS, apiKeyFile) {
			return Server{}, invalidConfig("server API key file")
		}
	}
	inputBytes, err := normalizedInt(wire.InputBytes, 524_288, maxInputBytes)
	if err != nil {
		return Server{}, invalidConfig("server input byte limit")
	}
	instructionsBytes, err := normalizedInt(
		wire.InstructionsBytes,
		262_144,
		maxInstructionsBytes,
	)
	if err != nil {
		return Server{}, invalidConfig("server instructions byte limit")
	}
	schemaBytes, err := normalizedInt(wire.SchemaBytes, 32_768, maxSchemaBytes)
	if err != nil {
		return Server{}, invalidConfig("server schema byte limit")
	}
	handlerLimit, err := normalizedInt(wire.HandlerLimit, 128, maxHandlerLimit)
	if err != nil {
		return Server{}, invalidConfig("server handler limit")
	}
	bodyReaderLimit, err := normalizedInt(
		wire.BodyReaderLimit,
		32,
		maxBodyReaderLimit,
	)
	if err != nil {
		return Server{}, invalidConfig("server body reader limit")
	}
	headerBytes, err := normalizedInt(wire.MaxHeaderBytes, 16_384, maxHeaderBytes)
	if err != nil {
		return Server{}, invalidConfig("server header byte limit")
	}

	return Server{
		Listen:            stringDefault(wire.Listen, "127.0.0.1:8080"),
		APIKeyEnv:         stringDefault(wire.APIKeyEnv, ""),
		APIKeyFile:        apiKeyFile,
		HTTPBodyBytes:     int64Default(wire.HTTPBodyBytes, 1_048_576),
		InputBytes:        inputBytes,
		InstructionsBytes: instructionsBytes,
		SchemaBytes:       schemaBytes,
		HandlerLimit:      handlerLimit,
		BodyReaderLimit:   bodyReaderLimit,
		MaxHeaderBytes:    headerBytes,
		ReadHeaderTimeout: durationDefault(wire.ReadHeaderTimeout, 5*time.Second),
		BodyReadTimeout:   durationDefault(wire.BodyReadTimeout, 15*time.Second),
		IdleTimeout:       durationDefault(wire.IdleTimeout, 60*time.Second),
		ShutdownTimeout:   durationDefault(wire.ShutdownTimeout, 15*time.Second),
	}, nil
}

func normalizeRuntime(wire wireRuntime) (Runtime, error) {
	root := defaultRuntimeRoot()
	if wire.Root != nil {
		root = *wire.Root
	}
	return Runtime{
		Root:           root,
		TermGrace:      durationDefault(wire.TermGrace, 2*time.Second),
		CleanupTimeout: durationDefault(wire.CleanupTimeout, 5*time.Second),
		StdoutBytes:    int64Default(wire.StdoutBytes, 2_097_152),
		StderrBytes:    int64Default(wire.StderrBytes, 262_144),
		FinalBytes:     int64Default(wire.FinalBytes, 1_048_576),
	}, nil
}

func normalizeProvider(wire wireProvider) (Provider, error) {
	concurrency, err := normalizedInt(wire.Concurrency, 1, maxConcurrency)
	if err != nil {
		return Provider{}, invalidConfig("provider concurrency")
	}
	queueSize, err := normalizedInt(wire.QueueSize, 32, maxQueueSize)
	if err != nil {
		return Provider{}, invalidConfig("provider queue size")
	}
	return Provider{
		Executable:       wire.Executable,
		PrefixArgs:       append([]string(nil), wire.PrefixArgs...),
		ConfigHome:       wire.ConfigHome,
		CredentialEnv:    append([]string(nil), wire.CredentialEnv...),
		Concurrency:      concurrency,
		QueueSize:        queueSize,
		QueueBytes:       int64Default(wire.QueueBytes, 16_777_216),
		QueueTimeout:     durationDefault(wire.QueueTimeout, 30*time.Second),
		ExecutionTimeout: durationDefault(wire.ExecutionTimeout, 5*time.Minute),
	}, nil
}

func stringDefault(value *string, fallback string) string {
	if value == nil {
		return fallback
	}
	return *value
}

func int64Default(value *int64, fallback int64) int64 {
	if value == nil {
		return fallback
	}
	return *value
}

func normalizedInt(value *int64, fallback, ceiling int64) (int, error) {
	normalized := fallback
	if value != nil {
		normalized = *value
	}
	if normalized <= 0 || normalized > ceiling {
		return 0, errors.New("integer is outside its structural bounds")
	}
	return int(normalized), nil
}

func durationDefault(value *Duration, fallback time.Duration) Duration {
	if value == nil {
		return Duration(fallback)
	}
	return *value
}

func validate(cfg Config) error {
	if err := validateServer(cfg.Server); err != nil {
		return err
	}
	if err := validateRuntime(cfg.Runtime); err != nil {
		return err
	}
	if err := validShutdownBudget(cfg.Server, cfg.Runtime); err != nil {
		return err
	}
	if len(cfg.Providers) == 0 {
		return invalidConfig("providers")
	}

	providerNames := make([]string, 0, len(cfg.Providers))
	for name := range cfg.Providers {
		providerNames = append(providerNames, name)
	}
	sort.Strings(providerNames)

	seenCredentialEnv := make(map[string]struct{})
	for _, name := range providerNames {
		provider := cfg.Providers[name]
		if err := validateProvider(name, provider, cfg.Server.APIKeyEnv, seenCredentialEnv); err != nil {
			return err
		}
	}

	return validateModels(cfg.Models, cfg.Providers)
}

func validateServer(server Server) error {
	host, portText, err := net.SplitHostPort(server.Listen)
	if err != nil {
		return invalidConfig("server listen address")
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() || !decimalDigits(portText) {
		return invalidConfig("server listen address")
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return invalidConfig("server listen address")
	}
	if server.APIKeyEnv != "" && !environmentNamePattern.MatchString(server.APIKeyEnv) {
		return invalidConfig("server API key environment name")
	}

	if err := positiveCap("server HTTP body bytes", server.HTTPBodyBytes, maxHTTPBodyBytes); err != nil {
		return err
	}
	if err := positiveCap("server input bytes", int64(server.InputBytes), maxInputBytes); err != nil {
		return err
	}
	if err := positiveCap(
		"server instructions bytes",
		int64(server.InstructionsBytes),
		maxInstructionsBytes,
	); err != nil {
		return err
	}
	if err := positiveCap("server schema bytes", int64(server.SchemaBytes), maxSchemaBytes); err != nil {
		return err
	}
	if err := positiveCap("server handler limit", int64(server.HandlerLimit), maxHandlerLimit); err != nil {
		return err
	}
	if err := positiveCap(
		"server body reader limit",
		int64(server.BodyReaderLimit),
		maxBodyReaderLimit,
	); err != nil {
		return err
	}
	if err := positiveCap("server header bytes", int64(server.MaxHeaderBytes), maxHeaderBytes); err != nil {
		return err
	}
	for _, duration := range []struct {
		category string
		value    Duration
	}{
		{"server read header timeout", server.ReadHeaderTimeout},
		{"server body read timeout", server.BodyReadTimeout},
		{"server idle timeout", server.IdleTimeout},
		{"server shutdown timeout", server.ShutdownTimeout},
	} {
		if err := validDuration(duration.category, duration.value); err != nil {
			return err
		}
	}

	if int64(server.InputBytes) > server.HTTPBodyBytes ||
		int64(server.InstructionsBytes) > server.HTTPBodyBytes ||
		int64(server.SchemaBytes) > server.HTTPBodyBytes {
		return invalidConfig("server request byte aggregates")
	}
	if server.BodyReaderLimit > server.HandlerLimit {
		return invalidConfig("server concurrency aggregates")
	}
	return nil
}

func validateRuntime(runtimeConfig Runtime) error {
	if !isAbsolutePath(runtime.GOOS, runtimeConfig.Root) {
		return invalidConfig("runtime root")
	}
	if err := positiveCap("runtime stdout bytes", runtimeConfig.StdoutBytes, maxStdoutBytes); err != nil {
		return err
	}
	if err := positiveCap("runtime stderr bytes", runtimeConfig.StderrBytes, maxStderrBytes); err != nil {
		return err
	}
	if err := positiveCap("runtime final bytes", runtimeConfig.FinalBytes, maxFinalBytes); err != nil {
		return err
	}
	if runtimeConfig.FinalBytes > runtimeConfig.StdoutBytes {
		return invalidConfig("runtime output byte aggregates")
	}
	if err := validDuration("runtime termination grace", runtimeConfig.TermGrace); err != nil {
		return err
	}
	if err := validDuration("runtime cleanup timeout", runtimeConfig.CleanupTimeout); err != nil {
		return err
	}
	return nil
}

func validateProvider(
	name string,
	provider Provider,
	gatewayAPIKeyEnv string,
	seenCredentialEnv map[string]struct{},
) error {
	allowed, known := allowedCredentialEnv[name]
	if !known {
		return invalidConfig("provider name")
	}
	if !isAbsolutePath(runtime.GOOS, provider.Executable) {
		return invalidConfig("provider executable")
	}
	if !isAbsolutePath(runtime.GOOS, provider.ConfigHome) {
		return invalidConfig("provider config home")
	}
	if err := validatePrefixArgs(runtime.GOOS, provider.Executable, provider.PrefixArgs); err != nil {
		return err
	}
	if err := positiveCap("provider concurrency", int64(provider.Concurrency), maxConcurrency); err != nil {
		return err
	}
	if err := positiveCap("provider queue entries", int64(provider.QueueSize), maxQueueSize); err != nil {
		return err
	}
	if err := positiveCap("provider queued bytes", provider.QueueBytes, maxQueueBytes); err != nil {
		return err
	}
	if err := validDuration("provider queue timeout", provider.QueueTimeout); err != nil {
		return err
	}
	if err := validDuration("provider execution timeout", provider.ExecutionTimeout); err != nil {
		return err
	}

	providerCredentialEnv := make(map[string]struct{}, len(provider.CredentialEnv))
	for _, environmentName := range provider.CredentialEnv {
		if !environmentNamePattern.MatchString(environmentName) {
			return invalidConfig("provider credential environment name")
		}
		if _, supported := allowed[environmentName]; !supported {
			return invalidConfig("provider credential environment name")
		}
		if environmentName == gatewayAPIKeyEnv {
			return invalidConfig("provider and gateway credential separation")
		}
		if _, duplicate := providerCredentialEnv[environmentName]; duplicate {
			if name == "gemini" {
				return invalidConfig("Gemini credential profile")
			}
			return invalidConfig("provider credential environment uniqueness")
		}
		providerCredentialEnv[environmentName] = struct{}{}
	}
	if name == "gemini" && !validGeminiCredentialProfile(providerCredentialEnv) {
		return invalidConfig("Gemini credential profile")
	}
	for environmentName := range providerCredentialEnv {
		if _, duplicate := seenCredentialEnv[environmentName]; duplicate {
			return invalidConfig("provider credential environment uniqueness")
		}
		seenCredentialEnv[environmentName] = struct{}{}
	}
	return nil
}

func validGeminiCredentialProfile(profile map[string]struct{}) bool {
	switch len(profile) {
	case 0:
		return true
	case 1:
		_, geminiAPIKey := profile["GEMINI_API_KEY"]
		_, googleAPIKey := profile["GOOGLE_API_KEY"]
		return geminiAPIKey || googleAPIKey
	case 3:
		_, credentials := profile["GOOGLE_APPLICATION_CREDENTIALS"]
		_, project := profile["GOOGLE_CLOUD_PROJECT"]
		_, location := profile["GOOGLE_CLOUD_LOCATION"]
		return credentials && project && location
	default:
		return false
	}
}

func validateModels(models []Model, providers map[string]Provider) error {
	if len(models) == 0 || len(models) > maxModels {
		return invalidConfig("model count")
	}

	coreModels := make([]core.Model, 0, len(models))
	for _, model := range models {
		if model.Created < 0 {
			return invalidConfig("model creation time")
		}
		if _, declared := providers[model.Provider]; !declared {
			return invalidConfig("model provider reference")
		}
		if err := core.ValidateProviderModel(model.ProviderModel); err != nil {
			return invalidConfig("provider model argument")
		}
		coreModels = append(coreModels, core.Model{
			ID:            model.ID,
			Provider:      core.ProviderName(model.Provider),
			ProviderModel: model.ProviderModel,
			Created:       model.Created,
		})
	}
	if _, err := core.NewRegistry(coreModels); err != nil {
		return invalidConfig("model registry")
	}
	return nil
}

func positiveCap(category string, value, ceiling int64) error {
	if value <= 0 || value > ceiling {
		return invalidConfig(category)
	}
	return nil
}

func validDuration(category string, value Duration) error {
	duration := time.Duration(value)
	if duration <= 0 || duration > maxConfiguredDuration {
		return invalidConfig(category)
	}
	return nil
}

func validShutdownBudget(server Server, runtimeConfig Runtime) error {
	shutdown := time.Duration(server.ShutdownTimeout)
	termGrace := time.Duration(runtimeConfig.TermGrace)
	cleanup := time.Duration(runtimeConfig.CleanupTimeout)
	if cleanup > (time.Duration(math.MaxInt64)-termGrace-time.Second)/2 {
		return invalidConfig("shutdown cleanup budget")
	}
	required := termGrace + 2*cleanup + time.Second
	if shutdown < required {
		return invalidConfig("shutdown cleanup budget")
	}
	return nil
}

func invalidConfig(category string) error {
	return fmt.Errorf("invalid configuration: %s", category)
}

func decimalDigits(value string) bool {
	if value == "" {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func validatePrefixArgs(platform, executable string, args []string) error {
	if platform != "windows" {
		if len(args) != 0 {
			return invalidConfig("provider prefix arguments")
		}
		return nil
	}
	if len(args) == 0 {
		return nil
	}
	if len(args) != 1 ||
		!isAbsolutePath("windows", executable) ||
		!strings.EqualFold(windowsBase(executable), "node.exe") {
		return invalidConfig("provider prefix arguments")
	}

	entrypoint := args[0]
	if len(entrypoint) == 0 ||
		len(entrypoint) > maxPrefixArgBytes ||
		!utf8.ValidString(entrypoint) ||
		strings.HasPrefix(entrypoint, "-") ||
		!isAbsolutePath("windows", entrypoint) {
		return invalidConfig("provider prefix arguments")
	}
	for _, character := range entrypoint {
		if character == 0 || unicode.IsControl(character) {
			return invalidConfig("provider prefix arguments")
		}
	}
	extension := windowsExtension(entrypoint)
	if extension != ".js" && extension != ".mjs" {
		return invalidConfig("provider prefix arguments")
	}
	return nil
}

func isAbsolutePath(platform, value string) bool {
	if value == "" {
		return false
	}
	if platform != "windows" {
		return path.IsAbs(value)
	}

	if len(value) >= 3 &&
		((value[0] >= 'A' && value[0] <= 'Z') ||
			(value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' &&
		isWindowsSeparator(value[2]) {
		return true
	}
	if len(value) < 5 || !isWindowsSeparator(value[0]) || !isWindowsSeparator(value[1]) {
		return false
	}

	rest := value[2:]
	firstSeparator := strings.IndexAny(rest, `\/`)
	if firstSeparator <= 0 || firstSeparator == len(rest)-1 {
		return false
	}
	share := rest[firstSeparator+1:]
	return !isWindowsSeparator(share[0])
}

func windowsBase(value string) string {
	if separator := strings.LastIndexAny(value, `\/`); separator >= 0 {
		return value[separator+1:]
	}
	return value
}

func windowsExtension(value string) string {
	base := windowsBase(value)
	if dot := strings.LastIndexByte(base, '.'); dot >= 0 {
		return base[dot:]
	}
	return ""
}

func isWindowsSeparator(value byte) bool {
	return value == '\\' || value == '/'
}
