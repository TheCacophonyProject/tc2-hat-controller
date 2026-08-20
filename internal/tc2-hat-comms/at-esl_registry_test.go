package comms

import (
	"strings"
	"testing"
)

func TestParseRegistryByte(t *testing.T) {
	golden := strings.Join([]string{
		"m00",
		"",
		"00: ff ff ff ff ff 1e aa bb cc dd ee ff 01 02 03 04",
		"10: 00 00 03 1e ff ff ff ff ff ff ff ff ff ff ff ff",
		"20: ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff ff",
		"",
		"O^K",
	}, "\r\n")

	mixed := string([]byte{0xa5, 0xfc}) + "\r\nO^K\r\n" + golden

	tests := []struct {
		name    string
		resp    string
		reg     int
		want    int64
		wantErr bool
	}{
		{
			name:    "empty",
			resp:    "",
			reg:     0x05,
			wantErr: true,
		},
		{
			name:    "short garbage",
			resp:    "\r",
			reg:     0x05,
			wantErr: true,
		},
		{
			name:    "no dump lines",
			resp:    "\r\nO^K\r\n",
			reg:     0x05,
			wantErr: true,
		},
		{
			name: "prediction lockout reg 0x05",
			resp: golden,
			reg:  0x05,
			want: 0x1e, // 30 mins
		},
		{
			name: "battery hours reg 0x12",
			resp: golden,
			reg:  0x12,
			want: 0x03,
		},
		{
			name: "battery mins reg 0x13",
			resp: golden,
			reg:  0x13,
			want: 0x1e,
		},
		{
			name: "ff treated as unset zero",
			resp: golden,
			reg:  0x00,
			want: 0,
		},
		{
			name: "mixed prefix noise still parses",
			resp: mixed,
			reg:  0x05,
			want: 0x1e,
		},
		{
			name: "uppercase hex line",
			resp: "00: FF FF FF FF FF 0A 00 00 00 00 00 00 00 00 00 00\r\n",
			reg:  0x05,
			want: 0x0a,
		},
		{
			name:    "column past end of short line",
			resp:    "00: ff ff\r\n",
			reg:     0x05,
			wantErr: true,
		},
		{
			// An m10 dump the node labelled relative to the request: the only row
			// present is row 1, so it is read despite the 00: label.
			name: "row relabelled to dump start",
			resp: "m10\r\n\r\n00: 00 00 03 1e ff ff ff ff ff ff ff ff ff ff ff ff\r\n\r\nO^K\r\n",
			reg:  0x13,
			want: 0x1e,
		},
		{
			name: "echoed command is not mistaken for a row",
			resp: "m00\r\n00: ff ff ff ff ff 1e ff ff ff ff ff ff ff ff ff ff\r\nO^K\r\n",
			reg:  0x05,
			want: 0x1e,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseRegistryByte([]byte(tt.resp), tt.reg)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected error, got value %d", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %d want %d", got, tt.want)
			}
		})
	}
}

func TestFormatRegistryRowHighlightsSelectedColumn(t *testing.T) {
	fields := strings.Fields("10: 00 00 03 1e ff ff ff ff ff ff ff ff ff ff ff ff")
	got := formatRegistryRow(fields, 2)
	want := "10: 00 00 [03] 1e ff ff ff ff ff ff ff ff ff ff ff ff"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestCalcCRC16Stable(t *testing.T) {
	// Ensure AT+XCMD payload CRC helper still returns two bytes.
	crc := calcCRC16([]byte("m00"))
	if len(crc) != 2 {
		t.Fatalf("expected 2 CRC bytes, got %d", len(crc))
	}
}

func TestRegistryDumpCommandUsesRowStart(t *testing.T) {
	tests := []struct {
		reg  int
		want string
	}{
		{reg: 0x00, want: "m00"},
		{reg: 0x05, want: "m00"},
		{reg: 0x12, want: "m10"},
		{reg: 0x13, want: "m10"},
		{reg: 0xff, want: "mf0"},
	}

	for _, tt := range tests {
		if got := registryDumpCommand(tt.reg); got != tt.want {
			t.Errorf("registryDumpCommand(0x%02x) = %q, want %q", tt.reg, got, tt.want)
		}
	}
}

// The node treats 0x7e as a frame flag and we do not byte-stuff AT+XCMD
// payloads, so a command whose CRC contains a flag/CR/LF/ESC byte is truncated
// on the wire. Unaligned "m12" hits this (CRC 0xea,0x7e); row starts must not.
func TestRegistryDumpCommandCRCAvoidsFramingBytes(t *testing.T) {
	framing := map[byte]string{0x7e: "flag", '\r': "CR", '\n': "LF", 0x1b: "ESC"}

	for row := 0; row < 16; row++ {
		reg := row * 16
		cmd := registryDumpCommand(reg)
		for _, b := range calcCRC16([]byte(cmd)) {
			if name, bad := framing[b]; bad {
				t.Errorf("CRC of %q contains %s byte 0x%02x", cmd, name, b)
			}
		}
	}
}
