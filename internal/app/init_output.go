package app

import (
	"bytes"
	"errors"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

var errInitOutput = errors.New("init output failed")

type initCompletion struct {
	ConfigPath string
	BackupPath string
	KeyPath    string
	KeyEnv     string
	Listen     string
	Saved      bool
	Noop       bool
	Ready      bool
}

func writeInitCompletion(writer io.Writer, view initCompletion) error {
	if nilLike(writer) || !validInitCompletion(view) {
		return errInitOutput
	}

	var output bytes.Buffer
	if view.Saved {
		output.WriteString("saved_config: ")
	} else {
		output.WriteString("already_current: ")
	}
	output.WriteString(strconv.Quote(view.ConfigPath))
	output.WriteByte('\n')
	if view.BackupPath != "" {
		output.WriteString("backup_config: ")
		output.WriteString(strconv.Quote(view.BackupPath))
		output.WriteByte('\n')
	}
	if view.KeyPath != "" {
		output.WriteString("gateway_key_file: ")
		output.WriteString(strconv.Quote(view.KeyPath))
		output.WriteByte('\n')
		output.WriteString("client_key_posix: export AI_CLI_GATEWAY_API_KEY=\"$(cat -- ")
		output.WriteString(quotePOSIX(view.KeyPath))
		output.WriteString(")\"\n")
		output.WriteString("client_key_powershell: $env:AI_CLI_GATEWAY_API_KEY = ")
		output.WriteString("[System.IO.File]::ReadAllText(")
		output.WriteString(quotePowerShell(view.KeyPath))
		output.WriteString(").TrimEnd(\"`r\", \"`n\")\n")
	} else if view.KeyEnv != "" {
		output.WriteString("client_key_posix: export AI_CLI_GATEWAY_API_KEY=\"${")
		output.WriteString(view.KeyEnv)
		output.WriteString(":?not set}\"\n")
		output.WriteString("client_key_powershell: $env:AI_CLI_GATEWAY_API_KEY = ")
		output.WriteString("[System.Environment]::GetEnvironmentVariable(")
		output.WriteString(quotePowerShell(view.KeyEnv))
		output.WriteString(")\n")
	}
	if view.Listen != "" {
		endpoint := "http://" + view.Listen + "/v1/models"
		output.WriteString("serve_posix: ai-cli-gateway serve --config ")
		output.WriteString(quotePOSIX(view.ConfigPath))
		output.WriteByte('\n')
		output.WriteString("serve_powershell: ai-cli-gateway serve --config ")
		output.WriteString(quotePowerShell(view.ConfigPath))
		output.WriteByte('\n')
		output.WriteString("request_posix: curl --fail-with-body ")
		if view.KeyPath != "" || view.KeyEnv != "" {
			output.WriteString("-H ")
			output.WriteString("\"Authorization: Bearer ${AI_CLI_GATEWAY_API_KEY:?not set}\" ")
		}
		output.WriteString(quotePOSIX(endpoint))
		output.WriteByte('\n')
		output.WriteString("request_powershell: curl.exe --fail-with-body ")
		if view.KeyPath != "" || view.KeyEnv != "" {
			output.WriteString("-H ")
			output.WriteString("\"Authorization: Bearer $env:AI_CLI_GATEWAY_API_KEY\" ")
		}
		output.WriteString(quotePowerShell(endpoint))
		output.WriteByte('\n')
	}
	if view.Ready {
		output.WriteString("setup_ready\n")
	} else if view.Saved {
		output.WriteString("setup_saved_but_not_ready\n")
	} else {
		output.WriteString("setup_not_ready\n")
	}

	payload := output.Bytes()
	written, err := writer.Write(payload)
	if err != nil || written != len(payload) {
		return errInitOutput
	}
	return nil
}

func validInitCompletion(view initCompletion) bool {
	if !safeInitText(view.ConfigPath) || !portableAbsolutePath(view.ConfigPath) ||
		view.Saved == view.Noop {
		return false
	}
	if view.BackupPath != "" && (!view.Saved || !safeInitText(view.BackupPath) ||
		!portableAbsolutePath(view.BackupPath)) {
		return false
	}
	if view.KeyPath != "" && (!safeInitText(view.KeyPath) ||
		!portableAbsolutePath(view.KeyPath)) {
		return false
	}
	if view.KeyPath != "" && view.KeyEnv != "" {
		return false
	}
	if view.KeyEnv != "" && !safeInitEnvironmentName(view.KeyEnv) {
		return false
	}
	return view.Listen == "" || safeInitText(view.Listen)
}

func safeInitEnvironmentName(value string) bool {
	if value == "" || !initEnvironmentUpperOrUnderscore(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		character := value[index]
		if !initEnvironmentUpperOrUnderscore(character) &&
			(character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func initEnvironmentUpperOrUnderscore(character byte) bool {
	return character >= 'A' && character <= 'Z' || character == '_'
}

func quotePOSIX(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func quotePowerShell(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func portableAbsolutePath(value string) bool {
	if filepath.IsAbs(value) || strings.HasPrefix(value, "/") ||
		strings.HasPrefix(value, `\\`) {
		return true
	}
	return len(value) >= 3 &&
		((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) &&
		value[1] == ':' && (value[2] == '\\' || value[2] == '/')
}

func safeInitText(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	for _, character := range value {
		if !unicode.IsPrint(character) || unicode.IsControl(character) {
			return false
		}
	}
	return true
}
