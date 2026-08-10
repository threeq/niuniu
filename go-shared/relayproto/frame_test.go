package relayproto

import (
	"bytes"
	"testing"
)

func TestFrameRoundTrip(t *testing.T) {
	f := &Frame{
		Type:     FrameData,
		StreamID: 42,
		Payload:  []byte("hello"),
	}
	buf := &bytes.Buffer{}
	if err := f.WriteTo(buf); err != nil {
		t.Fatalf("WriteTo: %v", err)
	}
	got, err := ReadFrame(buf)
	if err != nil {
		t.Fatalf("ReadFrame: %v", err)
	}
	if got.Type != f.Type || got.StreamID != f.StreamID || !bytes.Equal(got.Payload, f.Payload) {
		t.Fatalf("round-trip mismatch: %+v vs %+v", f, got)
	}
}

func TestFrameRejectsOversizePayload(t *testing.T) {
	f := &Frame{
		Type:    FrameData,
		Payload: make([]byte, MaxFramePayload+1),
	}
	if err := f.WriteTo(&bytes.Buffer{}); err == nil {
		t.Fatalf("expected error for oversize payload")
	}
}

func TestReadFrameRejectsOversizeLen(t *testing.T) {
	buf := &bytes.Buffer{}
	buf.WriteByte(byte(FrameData))
	buf.Write([]byte{0, 0, 0, 1})
	bad := uint32(MaxFramePayload + 1)
	buf.Write([]byte{byte(bad >> 24), byte(bad >> 16), byte(bad >> 8), byte(bad)})
	if _, err := ReadFrame(buf); err == nil {
		t.Fatalf("expected error for oversize declared length")
	}
}
