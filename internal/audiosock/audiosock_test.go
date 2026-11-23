package audiosock

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	payload := []byte{1, 2, 3, 4, 5}
	if err := WriteFrame(&buf, TypeSlin8k, payload); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
	fr, err := ReadFrame(&buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if fr.Type != TypeSlin8k {
		t.Fatalf("type = 0x%02x, want 0x%02x", fr.Type, TypeSlin8k)
	}
	if !bytes.Equal(fr.Payload, payload) {
		t.Fatalf("payload = %v, want %v", fr.Payload, payload)
	}
}

func TestFrameHeaderBigEndian(t *testing.T) {
	var buf bytes.Buffer
	payload := make([]byte, 320)
	if err := WriteFrame(&buf, TypeSlin8k, payload); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	// 320 = 0x0140
	if b[0] != TypeSlin8k || b[1] != 0x01 || b[2] != 0x40 {
		t.Fatalf("header = % x, want 10 01 40", b[:3])
	}
}

func TestFormatUUID(t *testing.T) {
	b := []byte{
		0x25, 0x30, 0xd0, 0x5c, 0xfe, 0xec, 0x40, 0xa4,
		0x89, 0x5c, 0xc4, 0xc5, 0xcf, 0xb8, 0x6b, 0x8c,
	}
	got := FormatUUID(b)
	want := "2530d05c-feec-40a4-895c-c4c5cfb86b8c"
	if got != want {
		t.Fatalf("FormatUUID = %q, want %q", got, want)
	}
}

func TestHangupFrame(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, TypeHangup, nil); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 3 {
		t.Fatalf("hangup frame len = %d, want 3", buf.Len())
	}
	fr, err := ReadFrame(&buf)
	if err != nil {
		t.Fatal(err)
	}
	if fr.Type != TypeHangup || len(fr.Payload) != 0 {
		t.Fatalf("got type=0x%02x len=%d", fr.Type, len(fr.Payload))
	}
}
