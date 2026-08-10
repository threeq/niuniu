package relayclient

import (
	"io"
	"net"
	"time"

	"github.com/gorilla/websocket"
)

// wsAdapter presents a websocket.Conn as a net.Conn suitable for yamux.
type wsAdapter struct {
	ws     *websocket.Conn
	reader io.Reader
}

func newWSAdapter(ws *websocket.Conn) *wsAdapter { return &wsAdapter{ws: ws} }

// NewWSAdapterConn wraps a *websocket.Conn as a net.Conn suitable for yamux.
// Used by lanhost to wrap the LAN tunnel WebSocket into a yamux server session.
func NewWSAdapterConn(ws *websocket.Conn) net.Conn { return newWSAdapter(ws) }

func (w *wsAdapter) Read(p []byte) (int, error) {
	for {
		if w.reader != nil {
			n, err := w.reader.Read(p)
			if err == io.EOF {
				w.reader = nil
				continue
			}
			return n, err
		}
		_, r, err := w.ws.NextReader()
		if err != nil {
			return 0, err
		}
		w.reader = r
	}
}

func (w *wsAdapter) Write(p []byte) (int, error) {
	wc, err := w.ws.NextWriter(websocket.BinaryMessage)
	if err != nil {
		return 0, err
	}
	n, err := wc.Write(p)
	if err != nil {
		_ = wc.Close()
		return n, err
	}
	if err := wc.Close(); err != nil {
		return n, err
	}
	return n, nil
}

func (w *wsAdapter) Close() error                       { return w.ws.Close() }
func (w *wsAdapter) LocalAddr() net.Addr                { return w.ws.LocalAddr() }
func (w *wsAdapter) RemoteAddr() net.Addr               { return w.ws.RemoteAddr() }
func (w *wsAdapter) SetDeadline(t time.Time) error {
	if err := w.ws.SetReadDeadline(t); err != nil {
		return err
	}
	return w.ws.SetWriteDeadline(t)
}
func (w *wsAdapter) SetReadDeadline(t time.Time) error  { return w.ws.SetReadDeadline(t) }
func (w *wsAdapter) SetWriteDeadline(t time.Time) error { return w.ws.SetWriteDeadline(t) }
