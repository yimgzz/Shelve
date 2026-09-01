package termws

import (
	"bytes"
	"strings"
	"testing"
)

func TestAppendDecodeRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		tabID   string
		payload []byte
	}{
		{"empty payload", "01ABC", nil},
		{"text", "01ABC", []byte("hello \x1b[31mworld")},
		{"binary", "01M1EKG5488Q6JDYK7DZ3DWJ1X", bytes.Repeat([]byte{0x00, 0xff, 0x7f}, 1000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame, err := AppendFrame(nil, tc.tabID, tc.payload)
			if err != nil {
				t.Fatalf("AppendFrame: %v", err)
			}
			tabID, payload, err := DecodeFrame(frame)
			if err != nil {
				t.Fatalf("DecodeFrame: %v", err)
			}
			if tabID != tc.tabID {
				t.Fatalf("tabID = %q, want %q", tabID, tc.tabID)
			}
			if !bytes.Equal(payload, tc.payload) {
				t.Fatalf("payload mismatch: got %d bytes, want %d", len(payload), len(tc.payload))
			}
		})
	}
}

func TestAppendFrameAppends(t *testing.T) {
	dst := []byte("prefix")
	frame, err := AppendFrame(dst, "t", []byte("ab"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(frame), "prefix") {
		t.Fatalf("AppendFrame did not append: %q", frame)
	}
	tabID, payload, err := DecodeFrame(frame[len("prefix"):])
	if err != nil {
		t.Fatal(err)
	}
	if tabID != "t" || string(payload) != "ab" {
		t.Fatalf("round trip = %q/%q", tabID, payload)
	}
}

func TestAppendFrameRejects(t *testing.T) {
	if _, err := AppendFrame(nil, "", []byte("x")); err == nil {
		t.Fatal("empty tabID: want error")
	}
	if _, err := AppendFrame(nil, strings.Repeat("a", maxTabIDLen+1), []byte("x")); err == nil {
		t.Fatal("long tabID: want error")
	}
	if _, err := AppendFrame(nil, "t", make([]byte, maxPayload+1)); err == nil {
		t.Fatal("oversize payload: want error")
	}
}

func TestDecodeFrameRejectsMalformed(t *testing.T) {
	good, err := AppendFrame(nil, "tab1", []byte("data"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		cut  func([]byte) []byte
	}{
		{"too short", func(b []byte) []byte { return b[:4] }},
		{"zero tabID len", func(b []byte) []byte { b[0] = 0; return b }},
		{"truncated tabID", func(b []byte) []byte { return b[:1+len("tab1")-1+4] }},
		{"truncated payload", func(b []byte) []byte { return b[:len(b)-1] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := append([]byte(nil), good...)
			if _, _, err := DecodeFrame(tc.cut(frame)); err == nil {
				t.Fatal("malformed frame: want error")
			}
		})
	}
	// Oversize declared length is rejected before the buffer is touched.
	bad := append([]byte(nil), good...)
	bad[1+len("tab1")+3] = 0xff
	if _, _, err := DecodeFrame(bad); err == nil {
		t.Fatal("oversize declared payload: want error")
	}
}
