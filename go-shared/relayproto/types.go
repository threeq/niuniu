package relayproto

// FrameType identifies a control or data frame on the tunnel.
type FrameType uint8

const (
	FrameHello      FrameType = 1
	FrameOpenStream FrameType = 2
	FrameData       FrameType = 3
	FrameClose      FrameType = 4
	FramePing       FrameType = 5
	FramePong       FrameType = 6
	FrameGoaway     FrameType = 7
)

func (t FrameType) String() string {
	switch t {
	case FrameHello:
		return "HELLO"
	case FrameOpenStream:
		return "OPEN_STREAM"
	case FrameData:
		return "DATA"
	case FrameClose:
		return "CLOSE"
	case FramePing:
		return "PING"
	case FramePong:
		return "PONG"
	case FrameGoaway:
		return "GOAWAY"
	default:
		return "UNKNOWN"
	}
}

// StreamType tags an OPEN_STREAM frame's payload type.
type StreamType uint8

const (
	StreamHTTP StreamType = 1
	StreamWS   StreamType = 2
	StreamSSE  StreamType = 3
)
