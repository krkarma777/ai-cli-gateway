package releasepack

import (
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

const (
	zipLocalSignature   = 0x04034b50
	zipCentralSignature = 0x02014b50
	zipEndSignature     = 0x06054b50
	zipExtendedTimeID   = 0x5455
)

func TestZIPHeadersContainOnlyExactExtendedTimestamp(t *testing.T) {
	fixture := newReleaseFixture(t)
	assets, err := WriteArchives(fixture.options)
	if err != nil {
		t.Fatalf("WriteArchives() error = %v", err)
	}
	zipPath := filepath.Join(fixture.outputRoot, "ai-cli-gateway_0.1.0_windows_amd64.zip")
	if assets[len(assets)-1].Path != zipPath {
		t.Fatalf("last asset path = %q, want %q", assets[len(assets)-1].Path, zipPath)
	}
	data, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatalf("ReadFile(ZIP): %v", err)
	}

	endOffset := findZIPEnd(t, data)
	end := data[endOffset:]
	if got := binary.LittleEndian.Uint32(end[:4]); got != zipEndSignature {
		t.Fatalf("end signature = %#x, want %#x", got, zipEndSignature)
	}
	if binary.LittleEndian.Uint16(end[20:22]) != 0 {
		t.Errorf("ZIP archive comment length is nonzero")
	}
	entryCount := int(binary.LittleEndian.Uint16(end[10:12]))
	centralOffset := int(binary.LittleEndian.Uint32(end[16:20]))
	wantUnix := uint32(fixture.options.SourceTime.Unix())

	offset := centralOffset
	for entry := 0; entry < entryCount; entry++ {
		central := checkedZIPSlice(t, data, offset, 46)
		if got := binary.LittleEndian.Uint32(central[:4]); got != zipCentralSignature {
			t.Fatalf("central[%d] signature = %#x, want %#x", entry, got, zipCentralSignature)
		}
		nameLength := int(binary.LittleEndian.Uint16(central[28:30]))
		extraLength := int(binary.LittleEndian.Uint16(central[30:32]))
		commentLength := int(binary.LittleEndian.Uint16(central[32:34]))
		if commentLength != 0 {
			t.Errorf("central[%d] comment length = %d, want zero", entry, commentLength)
		}
		body := checkedZIPSlice(t, data, offset+46, nameLength+extraLength+commentLength)
		centralName := string(body[:nameLength])
		assertExactExtendedTimeExtra(t, "central "+centralName, body[nameLength:nameLength+extraLength], wantUnix)

		localOffset := int(binary.LittleEndian.Uint32(central[42:46]))
		local := checkedZIPSlice(t, data, localOffset, 30)
		if got := binary.LittleEndian.Uint32(local[:4]); got != zipLocalSignature {
			t.Fatalf("local[%q] signature = %#x, want %#x", centralName, got, zipLocalSignature)
		}
		localNameLength := int(binary.LittleEndian.Uint16(local[26:28]))
		localExtraLength := int(binary.LittleEndian.Uint16(local[28:30]))
		localBody := checkedZIPSlice(t, data, localOffset+30, localNameLength+localExtraLength)
		localName := string(localBody[:localNameLength])
		if localName != centralName {
			t.Errorf("local name = %q, central name = %q", localName, centralName)
		}
		assertExactExtendedTimeExtra(t, "local "+localName, localBody[localNameLength:], wantUnix)

		offset += 46 + nameLength + extraLength + commentLength
	}
	if offset != endOffset {
		t.Fatalf("central directory ended at %d, EOCD starts at %d", offset, endOffset)
	}
}

func assertExactExtendedTimeExtra(t *testing.T, location string, extra []byte, wantUnix uint32) {
	t.Helper()
	if len(extra) != 9 {
		t.Errorf("%s extra length = %d (%x), want exactly one 9-byte 0x5455 field", location, len(extra), extra)
		return
	}
	if got := binary.LittleEndian.Uint16(extra[0:2]); got != zipExtendedTimeID {
		t.Errorf("%s extra ID = %#x, want %#x", location, got, zipExtendedTimeID)
	}
	if got := binary.LittleEndian.Uint16(extra[2:4]); got != 5 {
		t.Errorf("%s extended timestamp size = %d, want 5", location, got)
	}
	if extra[4] != 1 {
		t.Errorf("%s extended timestamp flags = %#x, want 1", location, extra[4])
	}
	if got := binary.LittleEndian.Uint32(extra[5:9]); got != wantUnix {
		t.Errorf("%s extended timestamp = %d, want %d", location, got, wantUnix)
	}
}

func findZIPEnd(t *testing.T, data []byte) int {
	t.Helper()
	for offset := len(data) - 22; offset >= 0; offset-- {
		if binary.LittleEndian.Uint32(data[offset:offset+4]) == zipEndSignature {
			return offset
		}
	}
	t.Fatal("ZIP end-of-central-directory signature not found")
	return 0
}

func checkedZIPSlice(t *testing.T, data []byte, offset, length int) []byte {
	t.Helper()
	if offset < 0 || length < 0 || offset > len(data)-length {
		t.Fatalf("ZIP slice [%d:%d] exceeds %d bytes", offset, offset+length, len(data))
	}
	return data[offset : offset+length]
}
