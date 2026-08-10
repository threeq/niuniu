package relayproto

import "testing"

func TestFrameTypeString(t *testing.T) {
	cases := []struct {
		in   FrameType
		want string
	}{
		{FrameHello, "HELLO"},
		{FrameOpenStream, "OPEN_STREAM"},
		{FrameData, "DATA"},
		{FrameClose, "CLOSE"},
		{FramePing, "PING"},
		{FramePong, "PONG"},
		{FrameGoaway, "GOAWAY"},
	}
	for _, c := range cases {
		if got := c.in.String(); got != c.want {
			t.Fatalf("FrameType(%d).String() = %q, want %q", c.in, got, c.want)
		}
	}
}
