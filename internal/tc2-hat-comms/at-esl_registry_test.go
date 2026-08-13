package comms

import (
	"strings"
	"testing"
)

// sampleM00Dump is a realistic multi-line AT+XCMD=m00 style response (CRLF rows + O^K).
func sampleM00Dump() string {
	return strings.Join([]string{
		"m00",
		"",
		"00: ff ff ff ff ff 1e aa bb cc dd ee ff 01 02 03 04",
		"10: 00 00 03 1e ff ff ff ff ff ff ff ff ff ff ff ff",
		"",
		"O^K",
	}, "\r\n")
}

func TestParseRegistryResponse_emptyDoesNotPanic(t *testing.T) {
	// Without the length gate, response[pos:pos+2] panics on empty input.
	got := parseRegistryResponse([]byte{}, 5, "m00")
	if got != 0 {
		t.Fatalf("empty response: got %d, want 0", got)
	}
}

func TestParseRegistryResponse_shortDoesNotPanic(t *testing.T) {
	// Typical SerialSendReceive leftover from a leading "\r\n": just "\r".
	got := parseRegistryResponse([]byte{'\r'}, 5, "m00")
	if got != 0 {
		t.Fatalf("short response: got %d, want 0", got)
	}
}

func TestParseRegistryResponse_validDumpParsesReg5(t *testing.T) {
	got := parseRegistryResponse([]byte(sampleM00Dump()), 5, "m00")
	if got != 0x1e {
		t.Fatalf("reg 5: got %#x, want 0x1e", got)
	}
}

func TestParseRegistryResponse_validDumpParsesReg12(t *testing.T) {
	// Legacy addressing: decimal 12 means hex row "10:" column 2 → 0x03 in the sample.
	got := parseRegistryResponse([]byte(sampleM00Dump()), 12, "m00")
	if got != 0x03 {
		t.Fatalf("reg 12: got %#x, want 0x03", got)
	}
}

func TestParseRegistryResponse_ffTreatedAsUnset(t *testing.T) {
	got := parseRegistryResponse([]byte(sampleM00Dump()), 0, "m00")
	if got != 0 {
		t.Fatalf("reg 0 (ff): got %d, want 0", got)
	}
}

func TestParseRegistryResponse_whatSerialSendReceiveWouldSee(t *testing.T) {
	// Node dumps start with putcrlf() before "00:", so UART bytes begin "\r\n...".
	// SerialSendReceive stops at the first '\n', so the captured body is only "\r".
	full := "\r\n" + sampleM00Dump() + "\r\n"
	firstNL := strings.IndexByte(full, '\n')
	if firstNL < 0 {
		t.Fatal("fixture missing newline")
	}
	// Mirror SerialSendReceive: bytes before first '\n', newline itself dropped.
	truncated := []byte(full[:firstNL])

	got := parseRegistryResponse(truncated, 5, "m00")
	if got != 0 {
		t.Fatalf("first-line-only (SerialSendReceive behaviour): got %d, want 0 (too short to parse)", got)
	}

	// Contrast: the full multi-line body (as if read past the first newline) parses.
	fullGot := parseRegistryResponse([]byte(sampleM00Dump()), 5, "m00")
	if fullGot != 0x1e {
		t.Fatalf("full dump: got %#x, want 0x1e", fullGot)
	}
}

// TestParseRegistryResponse_unguardedSliceWouldPanic documents the pre-fix crash.
// The length gate makes the equivalent path return 0 instead.
func TestParseRegistryResponse_unguardedSliceWouldPanic(t *testing.T) {
	response := []byte{}
	pos := 0
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic on unguarded empty slice")
		}
	}()
	_ = response[pos : pos+2]
}
