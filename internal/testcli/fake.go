// Package testcli provides a deterministic fake provider executable for tests.
package testcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const floodBlockBytes = 64 * 1024
const dualStreamPressureBytes = 512 * 1024
const codexPromptMaxBytes = 8 * 1024 * 1024

var codexReadyFeatures = [...]string{
	"shell_tool",
	"unified_exec",
	"code_mode_host",
	"apps",
	"plugins",
	"remote_plugin",
	"hooks",
	"multi_agent",
	"browser_use",
	"browser_use_external",
	"computer_use",
	"in_app_browser",
	"image_generation",
	"skill_search",
	"skill_mcp_dependency_install",
	"workspace_dependencies",
}

var (
	codexVersionArgs  = [...]string{"--version"}
	codexExecHelpArgs = [...]string{
		"--ask-for-approval", "never", "exec", "--help",
	}
	codexFeaturesListArgs = [...]string{"features", "list"}
	codexLoginStatusArgs  = [...]string{"login", "status"}
	codexDoctorArgs       = [...]string{"doctor", "--json"}
	codexFinalArgs        = [...]string{
		"--ask-for-approval",
		"never",
		"exec",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--strict-config",
		"--sandbox",
		"read-only",
		"--skip-git-repo-check",
		"--color",
		"never",
		"--disable",
		"shell_tool",
		"--disable",
		"unified_exec",
		"--disable",
		"code_mode_host",
		"--disable",
		"apps",
		"--disable",
		"plugins",
		"--disable",
		"remote_plugin",
		"--disable",
		"hooks",
		"--disable",
		"multi_agent",
		"--disable",
		"browser_use",
		"--disable",
		"browser_use_external",
		"--disable",
		"computer_use",
		"--disable",
		"in_app_browser",
		"--disable",
		"image_generation",
		"--disable",
		"skill_search",
		"--disable",
		"skill_mcp_dependency_install",
		"--disable",
		"workspace_dependencies",
		"-c",
		`web_search="disabled"`,
		"--model",
		"sdk-contract-model",
		"-",
	}
)

const codexExecHelp = "PROMPT\n-\n--disable\n-c\n--strict-config\n--sandbox\n--model\n--output-schema\n--color\n--ephemeral\n--ignore-user-config\n--ignore-rules\n--skip-git-repo-check\n"
const codexDoctorJSON = "{\"schemaVersion\":1,\"overallStatus\":\"ok\",\"checks\":{\"auth.credentials\":{\"id\":\"auth.credentials\",\"status\":\"ok\"},\"config.load\":{\"id\":\"config.load\",\"status\":\"ok\"}}}\n"

// CodexFinalHandler runs after CodexReadyMainWithFinal has verified the final
// execution argv and consumed a valid bounded prompt.
type CodexFinalHandler func(io.Reader, io.Writer, io.Writer) int

// Main runs one explicit deterministic fake mode and returns its process exit
// code.
func Main(
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) int {
	mode, err := parseMode(args)
	if err != nil || !knownMode(mode) {
		_, _ = io.WriteString(stderr, "fake-ai-cli: invalid mode\n")
		return 2
	}

	switch mode {
	case "codex-ready":
		return CodexReadyMain(removeMode(args), stdin, stdout, stderr)
	case "text":
		return writeFixed(stdout, "hello\n")
	case "echo-stdin":
		if _, err := io.Copy(stdout, stdin); err != nil {
			_, _ = io.WriteString(stderr, "fake-ai-cli: stdin failure\n")
			return 1
		}
		return 0
	case "empty-success":
		return 0
	case "invalid-utf8":
		if err := writeAll(stdout, []byte{0xff}); err != nil {
			return 1
		}
		return 0
	case "codex-schema-probe":
		return codexSchemaProbe(stdin, stdout, stderr)
	case "read-request-file":
		data, err := os.ReadFile("request.json")
		if err != nil || writeAll(stdout, data) != nil {
			_, _ = io.WriteString(stderr, "fake-ai-cli: request read failure\n")
			return 1
		}
		return 0
	case "codex-json":
		return writeFixed(stdout, `{"answer":"hello"}`+"\n")
	case "claude-json":
		return writeFixed(
			stdout,
			`{"type":"result","subtype":"success","is_error":false,`+
				`"result":"hello"}`+"\n",
		)
	case "claude-auth-error":
		if code := writeFixed(
			stdout,
			`{"type":"result","subtype":"success","is_error":true,`+
				`"api_error_status":401,`+
				`"result":"fixed discarded provider failure"}`+"\n",
		); code != 0 {
			return code
		}
		return 1
	case "claude-rate-limit":
		if code := writeFixed(
			stdout,
			`{"type":"result","subtype":"success","is_error":true,`+
				`"api_error_status":429,`+
				`"result":"fixed discarded provider failure"}`+"\n",
		); code != 0 {
			return code
		}
		return 1
	case "claude-execution-error":
		if code := writeFixed(
			stdout,
			`{"type":"result","subtype":"error_during_execution",`+
				`"is_error":true,`+
				`"errors":["fixed execution failure"]}`+"\n",
		); code != 0 {
			return code
		}
		return 1
	case "claude-stdin-probe":
		return claudeStdinProbe(stdin, stdout, stderr)
	case "gemini-json":
		return writeFixed(
			stdout,
			`{"session_id":"fake-session","response":"hello",`+
				`"stats":{"models":{}},"warnings":[]}`+"\n",
		)
	case "gemini-error":
		return writeFixed(
			stdout,
			`{"session_id":"fake-session","error":{"type":"fake_error",`+
				`"message":"fixed fake error"}}`+"\n",
		)
	case "gemini-duplicate-json":
		return writeFixed(
			stdout,
			`{"response":"first","response":"second"}`+"\n",
		)
	case "gemini-fenced-response":
		return writeFixed(
			stdout,
			"{\"session_id\":\"fake-session\",\"response\":\"```json\\n"+
				"{\\\"answer\\\":\\\"hello\\\"}\\n```\",\"stats\":{\"models\":{}},"+
				"\"warnings\":[]}\n",
		)
	case "gemini-stdin-probe":
		return geminiStdinProbe(stdin, stdout, stderr)
	case "gemini-wait-release":
		return geminiWaitRelease(stdout, stderr)
	case "invalid-json":
		return writeFixed(stdout, `{"invalid":`)
	case "duplicate-json":
		return writeFixed(
			stdout,
			`{"type":"result","subtype":"success","is_error":false,`+
				`"result":"first","result":"second"}`+"\n",
		)
	case "fenced-json":
		return writeFixed(stdout, "```json\n{\"answer\":\"hello\"}\n```\n")
	case "schema-mismatch":
		return writeFixed(stdout, `{"answer":7}`+"\n")
	case "exit-7":
		return 7
	case "flood-stdout":
		flood(stdout, 'x')
		return 0
	case "flood-stderr":
		flood(stderr, 'e')
		return 0
	case "pressure-both":
		return pressureBoth(stdout, stderr)
	case "flood-once-exit-7":
		if err := writeAll(stdout, fixedFloodBlock('x')); err != nil {
			return 1
		}
		return 7
	case "release-then-flood":
		return releaseThenFlood(stdout, stderr)
	case "ignore-term":
		ignoreSignals()
		return 0
	case "retry-until-canceled":
		retryUntilCanceled()
		return 0
	case "spawn-child-hold":
		return spawnChildHold(stdout, stderr)
	case "spawn-grandchild-hold":
		return spawnGrandchildHold(stdout, stderr)
	case "spawn-grandchild-middle":
		return spawnGrandchildMiddle(stdout, stderr)
	case "spawn-ignore-term-child":
		return spawnIgnoreTermChild(stdout, stderr)
	case "ignore-term-ready":
		return ignoreTermReady(stderr)
	case "spawn-session-escape":
		return spawnSessionEscape(stdout, stderr)
	case "hang", "child-hold", "grandchild-hold", "session-escape":
		blockUntilKilled()
		return 0
	default:
		return 2
	}
}

// CodexReadyMain emulates only the Codex probes and final SDK invocation that
// the Codex provider adapter is allowed to issue.
func CodexReadyMain(args []string, stdin io.Reader, stdout io.Writer, stderr io.Writer) int {
	return CodexReadyMainWithFinal(args, stdin, stdout, stderr, codexGatewayOK)
}

// CodexReadyMainWithFinal verifies the exact supported Codex argv forms. The
// final callback is never called before the final invocation and prompt pass.
func CodexReadyMainWithFinal(
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	final CodexFinalHandler,
) int {
	switch {
	case slices.Equal(args, codexVersionArgs[:]):
		return writeFixed(stdout, "codex-cli 0.146.0\n")
	case slices.Equal(args, codexExecHelpArgs[:]):
		return writeFixed(stdout, codexExecHelp)
	case slices.Equal(args, codexFeaturesListArgs[:]):
		return writeCodexFeatures(stdout)
	case slices.Equal(args, codexLoginStatusArgs[:]):
		return writeFixed(stdout, "Logged in\n")
	case slices.Equal(args, codexDoctorArgs[:]):
		return writeFixed(stdout, codexDoctorJSON)
	case slices.Equal(args, codexFinalArgs[:]) && final != nil:
		if !validCodexPrompt(stdin) {
			return codexUnsupported(stderr)
		}
		return final(strings.NewReader(""), stdout, stderr)
	default:
		return codexUnsupported(stderr)
	}
}

func removeMode(args []string) []string {
	for index, arg := range args {
		if arg == "--mode" {
			return append(append([]string(nil), args[:index]...), args[index+2:]...)
		}
		if strings.HasPrefix(arg, "--mode=") {
			return append(append([]string(nil), args[:index]...), args[index+1:]...)
		}
	}
	return append([]string(nil), args...)
}

func validCodexPrompt(stdin io.Reader) bool {
	prompt, err := io.ReadAll(io.LimitReader(stdin, codexPromptMaxBytes+1))
	return err == nil && len(prompt) != 0 && len(prompt) <= codexPromptMaxBytes && utf8.Valid(prompt)
}

func writeCodexFeatures(stdout io.Writer) int {
	for _, feature := range codexReadyFeatures {
		if code := writeFixed(stdout, feature+" stable false\n"); code != 0 {
			return code
		}
	}
	return 0
}

func codexGatewayOK(_ io.Reader, stdout io.Writer, _ io.Writer) int {
	return writeFixed(stdout, "SDK_GATEWAY_OK\n")
}

func codexUnsupported(stderr io.Writer) int {
	_, _ = io.WriteString(stderr, "fake-codex-cli: unsupported command\n")
	return 2
}

func parseMode(args []string) (string, error) {
	var mode string
	found := false
	for i := 0; i < len(args); i++ {
		arg := args[i]
		var value string
		switch {
		case arg == "--mode":
			if i+1 >= len(args) || strings.HasPrefix(args[i+1], "--mode") {
				return "", errors.New("missing mode")
			}
			i++
			value = args[i]
		case strings.HasPrefix(arg, "--mode="):
			value = strings.TrimPrefix(arg, "--mode=")
		default:
			continue
		}
		if found {
			return "", errors.New("duplicate mode")
		}
		if value == "" {
			return "", errors.New("empty mode")
		}
		found = true
		mode = value
	}
	if !found {
		return "", errors.New("missing mode")
	}
	return mode, nil
}

func knownMode(mode string) bool {
	switch mode {
	case "text",
		"codex-ready",
		"echo-stdin",
		"empty-success",
		"invalid-utf8",
		"codex-schema-probe",
		"read-request-file",
		"codex-json",
		"claude-json",
		"claude-auth-error",
		"claude-rate-limit",
		"claude-execution-error",
		"claude-stdin-probe",
		"gemini-json",
		"gemini-error",
		"gemini-duplicate-json",
		"gemini-fenced-response",
		"gemini-stdin-probe",
		"gemini-wait-release",
		"invalid-json",
		"duplicate-json",
		"fenced-json",
		"schema-mismatch",
		"exit-7",
		"flood-stdout",
		"flood-stderr",
		"pressure-both",
		"flood-once-exit-7",
		"release-then-flood",
		"hang",
		"ignore-term",
		"retry-until-canceled",
		"spawn-child-hold",
		"child-hold",
		"spawn-grandchild-hold",
		"spawn-grandchild-middle",
		"grandchild-hold",
		"spawn-ignore-term-child",
		"ignore-term-ready",
		"spawn-session-escape",
		"session-escape":
		return true
	default:
		return false
	}
}

func codexSchemaProbe(stdin io.Reader, stdout, stderr io.Writer) int {
	stdinBytes, err := io.Copy(io.Discard, stdin)
	if err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: stdin failure\n")
		return 1
	}
	schema, err := os.ReadFile("output-schema.json")
	if err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: schema read failure\n")
		return 1
	}
	if _, err := fmt.Fprintf(
		stdout,
		"{\"stdin_bytes\":%d,\"schema_bytes\":%d}\n",
		stdinBytes,
		len(schema),
	); err != nil {
		return 1
	}
	return 0
}

func claudeStdinProbe(stdin io.Reader, stdout, stderr io.Writer) int {
	stdinBytes, err := io.Copy(io.Discard, stdin)
	if err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: stdin failure\n")
		return 1
	}
	if _, err := fmt.Fprintf(
		stdout,
		"{\"type\":\"result\",\"subtype\":\"success\","+
			"\"is_error\":false,\"result\":\"stdin_bytes=%d\"}\n",
		stdinBytes,
	); err != nil {
		return 1
	}
	return 0
}

func geminiStdinProbe(stdin io.Reader, stdout, stderr io.Writer) int {
	stdinBytes, err := io.Copy(io.Discard, stdin)
	if err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: stdin failure\n")
		return 1
	}
	runtimeDir, err := os.Getwd()
	if err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: runtime failure\n")
		return 1
	}
	home, present := os.LookupEnv("GEMINI_CLI_HOME")
	if !present || home != runtimeDir {
		_, _ = io.WriteString(stderr, "fake-ai-cli: home isolation failure\n")
		return 1
	}
	settingsDir := filepath.Join(runtimeDir, ".gemini")
	directoryInfo, err := os.Lstat(settingsDir)
	if err != nil || directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() {
		_, _ = io.WriteString(stderr, "fake-ai-cli: settings directory failure\n")
		return 1
	}
	settingsPath := filepath.Join(settingsDir, "settings.json")
	settingsInfo, err := os.Lstat(settingsPath)
	if err != nil || settingsInfo.Mode()&os.ModeSymlink != 0 || !settingsInfo.Mode().IsRegular() {
		_, _ = io.WriteString(stderr, "fake-ai-cli: settings file failure\n")
		return 1
	}
	if runtime.GOOS != "windows" &&
		(directoryInfo.Mode().Perm() != 0o700 || settingsInfo.Mode().Perm() != 0o600) {
		_, _ = io.WriteString(stderr, "fake-ai-cli: settings mode failure\n")
		return 1
	}
	for _, override := range []struct {
		name string
		path string
	}{
		{
			name: "GEMINI_CLI_SYSTEM_DEFAULTS_PATH",
			path: filepath.Join(settingsDir, "system-defaults.json"),
		},
		{
			name: "GEMINI_CLI_SYSTEM_SETTINGS_PATH",
			path: filepath.Join(settingsDir, "system-settings.json"),
		},
	} {
		value, exists := os.LookupEnv(override.name)
		if !exists || value != override.path {
			_, _ = io.WriteString(stderr, "fake-ai-cli: settings isolation failure\n")
			return 1
		}
		if _, statErr := os.Lstat(value); !errors.Is(statErr, os.ErrNotExist) {
			_, _ = io.WriteString(stderr, "fake-ai-cli: settings override exists\n")
			return 1
		}
	}

	settingsBytes, err := os.ReadFile(filepath.Join(".gemini", "settings.json"))
	if err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: settings read failure\n")
		return 1
	}
	var settings struct {
		Security struct {
			Auth struct {
				SelectedType string `json:"selectedType"`
			} `json:"auth"`
		} `json:"security"`
	}
	if err := json.Unmarshal(settingsBytes, &settings); err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: settings parse failure\n")
		return 1
	}
	authType := settings.Security.Auth.SelectedType
	if authType != "gemini-api-key" && authType != "vertex-ai" {
		_, _ = io.WriteString(stderr, "fake-ai-cli: auth type failure\n")
		return 1
	}
	if _, err := fmt.Fprintf(
		stdout,
		"{\"session_id\":\"fake-session\",\"response\":"+
			"\"stdin_bytes=%d auth_type=%s settings_secure=true\",\"stats\":{\"models\":{}},"+
			"\"warnings\":[]}\n",
		stdinBytes,
		authType,
	); err != nil {
		return 1
	}
	return 0
}

func geminiWaitRelease(stdout, stderr io.Writer) int {
	if !waitForFakeRelease(stderr) {
		return 1
	}
	return writeFixed(
		stdout,
		`{"session_id":"fake-session","response":"hello",`+
			`"stats":{"models":{}},"warnings":[]}`+"\n",
	)
}

func writeFixed(dst io.Writer, value string) int {
	if err := writeAll(dst, []byte(value)); err != nil {
		return 1
	}
	return 0
}

func writeAll(dst io.Writer, data []byte) error {
	for len(data) != 0 {
		n, err := dst.Write(data)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(data) {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func flood(dst io.Writer, value byte) {
	block := fixedFloodBlock(value)
	for {
		if err := writeAll(dst, block); err != nil {
			return
		}
	}
}

func fixedFloodBlock(value byte) []byte {
	block := make([]byte, floodBlockBytes)
	for i := range block {
		block[i] = value
	}
	return block
}

func pressureBoth(stdout, stderr io.Writer) int {
	type stream struct {
		writer io.Writer
		value  byte
	}
	streams := []stream{
		{writer: stdout, value: 'o'},
		{writer: stderr, value: 'e'},
	}
	failures := make(chan error, len(streams))
	var writers sync.WaitGroup
	for _, output := range streams {
		output := output
		writers.Add(1)
		go func() {
			defer writers.Done()
			failures <- writeAll(
				output.writer,
				bytesOf(output.value, dualStreamPressureBytes),
			)
		}()
	}
	writers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			return 1
		}
	}
	return 0
}

func bytesOf(value byte, count int) []byte {
	block := make([]byte, count)
	for index := range block {
		block[index] = value
	}
	return block
}

func releaseThenFlood(stdout, stderr io.Writer) int {
	if !waitForFakeRelease(stderr) {
		return 1
	}
	if err := writeAll(stdout, fixedFloodBlock('x')); err != nil {
		return 1
	}
	if err := os.WriteFile(
		".fake-overflowed",
		[]byte("overflowed\n"),
		0o600,
	); err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: overflow marker failure\n")
		return 1
	}
	blockUntilKilled()
	return 0
}

func waitForFakeRelease(stderr io.Writer) bool {
	if err := os.WriteFile(".fake-ready", []byte("ready\n"), 0o600); err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: ready failure\n")
		return false
	}
	deadline := time.NewTimer(2 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if _, err := os.Lstat(".fake-release"); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			_, _ = io.WriteString(stderr, "fake-ai-cli: release failure\n")
			return false
		}
		select {
		case <-deadline.C:
			_, _ = io.WriteString(stderr, "fake-ai-cli: release timeout\n")
			return false
		case <-ticker.C:
		}
	}
	return true
}

func ignoreSignals() {
	signals := make(chan os.Signal, 1)
	signal.Notify(signals)
	defer signal.Stop(signals)
	for {
		<-signals
	}
}

func retryUntilCanceled() {
	ctx, stop := signal.NotifyContext(context.Background())
	defer stop()
	for {
		timer := time.NewTimer(time.Second)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return
		case <-timer.C:
		}
	}
}

func blockUntilKilled() {
	for {
		time.Sleep(time.Hour)
	}
}

func spawnChildHold(stdout, stderr io.Writer) int {
	executable, err := os.Executable()
	if err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: executable unavailable\n")
		return 1
	}
	// The fake deliberately starts itself directly, never through a shell.
	//nolint:gosec,noctx
	cmd := exec.Command(executable, "--mode=child-hold")
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_, _ = io.WriteString(stderr, "fake-ai-cli: child start failed\n")
		return 1
	}
	_, _ = fmt.Fprintf(stderr, "%d\n", cmd.Process.Pid)
	return 0
}
