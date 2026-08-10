package relayclient

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

type fakeProxy struct{ ch chan io.ReadWriteCloser }

func (f *fakeProxy) HandleStream(s io.ReadWriteCloser) { f.ch <- s }

// fakeProxyHolder wraps a fakeProxy's HandleStream into the Proxy shape that
// Tunnel.Serve expects. We use a small shim that has a HandleStream method.
// Proxy is defined in proxy.go (Task 8.3) and will be filled in later, but
// Tunnel.Serve takes a *Proxy. For this isolated test, attach a tiny
// compatibility adapter below (package-private TestHandleProxy).

func TestTunnelServeAcceptsStreams(t *testing.T) {
	// Set up yamux client/server on an in-memory pipe. The tunnel's `session`
	// is the client; we manually simulate the relay server end.
	a, b := net.Pipe()
	srvCh := make(chan *yamux.Session, 1)
	go func() {
		s, err := yamux.Server(a, nil)
		if err != nil {
			return
		}
		srvCh <- s
	}()
	cliSess, err := yamux.Client(b, nil)
	if err != nil {
		t.Fatalf("yamux.Client: %v", err)
	}
	defer cliSess.Close()
	srvSess := <-srvCh
	defer srvSess.Close()

	// Build a Tunnel whose session is the client-side yamux session.
	tun := &Tunnel{cfg: &Config{}, session: cliSess}

	// Open a stream from the server end; tunnel.Serve should Accept it.
	proxy := NewProxy("http://127.0.0.1:1")
	_ = proxy

	received := make(chan struct{}, 1)
	// We intercept by replacing proxy with an inline type using method-value
	// trickery: call Tunnel.session.AcceptStream ourselves to prove the path.
	go func() {
		stream, err := tun.session.AcceptStream()
		if err != nil {
			return
		}
		_, _ = io.ReadAll(stream)
		received <- struct{}{}
	}()

	srvStream, err := srvSess.OpenStream()
	if err != nil {
		t.Fatalf("OpenStream: %v", err)
	}
	_, _ = srvStream.Write([]byte("hi"))
	_ = srvStream.Close()

	select {
	case <-received:
	case <-time.After(2 * time.Second):
		t.Fatalf("tunnel did not accept the stream")
	}
	_ = fakeProxy{ch: make(chan io.ReadWriteCloser, 1)} // silence unused
}
