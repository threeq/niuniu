package relayproto

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// MaxFramePayload is the maximum length of a single frame payload.
// Larger data must be split across multiple DATA frames.
const MaxFramePayload = 64 * 1024

// Frame is the wire-level unit on a tunnel.
//
// Wire format:
//
//	uint8  type
//	uint32 be stream_id
//	uint32 be length
//	...    payload
type Frame struct {
	Type     FrameType
	StreamID uint32
	Payload  []byte
}

// WriteTo serializes f to w. Returns an error if the payload exceeds MaxFramePayload.
func (f *Frame) WriteTo(w io.Writer) error {
	if len(f.Payload) > MaxFramePayload {
		return fmt.Errorf("relayproto: payload %d exceeds %d", len(f.Payload), MaxFramePayload)
	}
	hdr := make([]byte, 9)
	hdr[0] = byte(f.Type)
	binary.BigEndian.PutUint32(hdr[1:5], f.StreamID)
	binary.BigEndian.PutUint32(hdr[5:9], uint32(len(f.Payload)))
	if _, err := w.Write(hdr); err != nil {
		return err
	}
	if len(f.Payload) > 0 {
		if _, err := w.Write(f.Payload); err != nil {
			return err
		}
	}
	return nil
}

// ReadFrame reads one frame from r. Returns io.EOF only on clean stream end.
func ReadFrame(r io.Reader) (*Frame, error) {
	hdr := make([]byte, 9)
	if _, err := io.ReadFull(r, hdr); err != nil {
		return nil, err
	}
	t := FrameType(hdr[0])
	sid := binary.BigEndian.Uint32(hdr[1:5])
	length := binary.BigEndian.Uint32(hdr[5:9])
	if length > MaxFramePayload {
		return nil, fmt.Errorf("relayproto: declared length %d exceeds %d", length, MaxFramePayload)
	}
	var payload []byte
	if length > 0 {
		payload = make([]byte, length)
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
	}
	return &Frame{Type: t, StreamID: sid, Payload: payload}, nil
}

// ErrStreamClosed signals the peer closed the stream.
var ErrStreamClosed = errors.New("relayproto: stream closed by peer")
