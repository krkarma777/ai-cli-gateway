//go:build windows

package process

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

const maxWindowsBaseNameUTF16 = 255

func validPlatformFileName(name string) bool {
	if !utf8.ValidString(name) ||
		len(utf16.Encode([]rune(name))) > maxWindowsBaseNameUTF16 ||
		strings.HasSuffix(name, ".") ||
		strings.HasSuffix(name, " ") {
		return false
	}
	for _, r := range name {
		if r < 0x20 || strings.ContainsRune(`<>:"/\|?*`, r) {
			return false
		}
	}

	stem := name
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
