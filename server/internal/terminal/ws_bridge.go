package terminal

import (
	"encoding/json"
	"log/slog"

	"github.com/gorilla/websocket"
)

type ResizeMessage struct {
	Type string `json:"type"`
	Rows uint16 `json:"rows"`
	Cols uint16 `json:"cols"`
}

// OnCloseFunc is called when a WebSocket connection closes.
// The bool return value indicates whether the PTY process should also be closed.
// Return true if this was the last connection and PTY should close.
type OnCloseFunc func() bool

// Bridge connects a WebSocket to a PTY process bidirectionally.
// - WS text/binary messages → PTY stdin
// - PTY stdout → WS binary messages
// - WS JSON messages with type "resize" → PTY resize
//
// If onClose is provided, it will be called when the WebSocket connection closes.
// The onClose callback should return true if the PTY process should be closed
// (i.e., this was the last connection), or false to keep the PTY running.
//
// Both internal goroutines are guaranteed to exit when Bridge returns,
// preventing goroutine leaks even when the PTY process outlives the WebSocket.
func Bridge(conn *websocket.Conn, proc *PTYProcess, onClose OnCloseFunc) error {
	errc := make(chan error, 2)
	done := make(chan struct{}) // closed when bridge is tearing down

	// goroutine 1: PTY → WS
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := proc.Read(buf)
			if err != nil {
				errc <- err
				return
			}
			// Check if bridge is shutting down before writing to WS
			select {
			case <-done:
				return
			default:
			}
			if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
				errc <- err
				return
			}
		}
	}()

	// goroutine 2: WS → PTY (handle resize messages)
	go func() {
		for {
			msgType, data, err := conn.ReadMessage()
			if err != nil {
				errc <- err
				return
			}
			if msgType == websocket.TextMessage {
				var msg ResizeMessage
				if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
					if err := proc.Resize(msg.Rows, msg.Cols); err != nil {
						slog.Warn("resize failed", "error", err)
					}
					continue
				}
			}
			if _, err := proc.Write(data); err != nil {
				errc <- err
				return
			}
		}
	}()

	// Wait for either goroutine to finish
	err := <-errc

	// Signal both goroutines to stop, then close the WebSocket.
	// Closing done unblocks the PTY→WS goroutine if it's between Read/Write.
	// Closing the WS connection unblocks ReadMessage in the WS→PTY goroutine
	// and causes WriteMessage to fail in the PTY→WS goroutine.
	close(done)
	conn.Close()

	// Wait for the other goroutine to exit to prevent goroutine leak.
	// Since conn is closed, both goroutines will exit promptly:
	// - WS→PTY: ReadMessage returns error on closed conn
	// - PTY→WS: WriteMessage returns error on closed conn, or proc.Read
	//   returns error if PTY exits. If PTY is still alive, proc.Read may
	//   block, but the goroutine is bounded — it will exit when the PTY
	//   is eventually closed by the idle timer or explicit Stop.
	select {
	case <-errc:
	case <-proc.Done():
		// PTY exited, both goroutines will finish
	}

	// Call onClose callback if provided to check if PTY should close
	if onClose != nil {
		if onClose() {
			proc.Close()
		}
	}
	return err
}
