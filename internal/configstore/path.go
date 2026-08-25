package configstore

import (
	"os"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const maxWindowsStoreComponentUTF16 = 255

type nativeLoadTarget struct {
	path     string
	exists   bool
	file     *os.File
	metadata nativeFileMetadata
	parent   nativeDirectoryEvidence
	missing  []string
}

func safeWindowsStoreComponent(component string) bool {
	if component == "" || component == "." || component == ".." ||
		!utf8.ValidString(component) ||
		windowsStoreComponentUTF16Length(component) > maxWindowsStoreComponentUTF16 ||
		strings.HasSuffix(component, ".") || strings.HasSuffix(component, " ") {
		return false
	}
	for _, character := range component {
		if character < 0x20 || strings.ContainsRune(`<>:"/\|?*`, character) {
			return false
		}
	}
	stem := component
	if dot := strings.IndexByte(stem, '.'); dot >= 0 {
		stem = stem[:dot]
	}
	stem = strings.ToUpper(strings.TrimRight(stem, " ."))
	switch stem {
	case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$", "CLOCK$":
		return false
	}
	runes := []rune(stem)
	if len(runes) == 4 {
		prefix := string(runes[:3])
		suffix := string(runes[3])
		if (prefix == "COM" || prefix == "LPT") &&
			strings.Contains("123456789¹²³", suffix) {
			return false
		}
	}
	return true
}

func windowsStoreComponentUTF16Length(component string) int {
	return len(utf16.Encode([]rune(component)))
}

func safeWindowsStoreUntrustedAllow(private bool, mask, unsafeGrant uint32) bool {
	return !private && mask&unsafeGrant == 0
}
