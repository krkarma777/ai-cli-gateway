//go:build !windows

package doctor

import (
	"bytes"
	"os"
)

var (
	unixNodeEnvShebangLF   = []byte("#!/usr/bin/env node\n")
	unixNodeEnvShebangCRLF = []byte("#!/usr/bin/env node\r\n")
)

func resolveProviderCommand(
	executable validatedPath,
	configuredPrefix []string,
	lookupExecutable func(string) (string, error),
) (resolvedProviderCommand, bool) {
	if len(configuredPrefix) != 0 || lookupExecutable == nil {
		return resolvedProviderCommand{}, false
	}
	if !exactUnixNodeEnvLauncher(executable.Resolved) {
		return nativeProviderCommand(executable), true
	}
	candidate, err := lookupExecutable("node")
	if err != nil {
		return resolvedProviderCommand{}, false
	}
	node, disposition := validateExecutablePath(candidate)
	if disposition != pathSafe {
		return resolvedProviderCommand{}, false
	}
	launcher, disposition := validateExecutablePath(executable.Clean)
	if disposition != pathSafe ||
		launcher.Resolved != executable.Resolved ||
		!sameValidatedIdentity(launcher, executable) {
		return resolvedProviderCommand{}, false
	}
	return resolvedProviderCommand{
		Executable: node,
		Entrypoint: &launcher,
		PrefixArgs: []string{launcher.Resolved},
	}, true
}

func exactUnixNodeEnvLauncher(path string) bool {
	file, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = file.Close() }()
	payload := make([]byte, len(unixNodeEnvShebangCRLF))
	count, _ := file.Read(payload)
	payload = payload[:count]
	return bytes.HasPrefix(payload, unixNodeEnvShebangLF) ||
		bytes.HasPrefix(payload, unixNodeEnvShebangCRLF)
}
