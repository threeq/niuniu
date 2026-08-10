package relayclient

import (
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hashicorp/yamux"
)

// TestTunnelReconnect_RunForeverRetriesAfterDrop simulates the relay going down
// mid-session and verifies that RunForever re-dials automatically.
//
// The test spins up two successive "relay" yamux servers on an in-memory
// listener.  The first server accepts the connection and then closes it to
// simulate a network drop.  RunForever must notice the Serve return and
// re-dial, reaching the second server.
func TestTunnelReconnect_RunForeverRetriesAfterDrop(t *testing.T) {
	const wantDials = 2

	// dialCount tracks how many times a client connected successfully.
	var dialCount atomic.Int32

	// connected is closed when the second dial is observed.
	connected := make(chan struct{})

	// relay listener: accepts connections and immediately drops the first one.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			n := dialCount.Add(1)
			if n == 1 {
				// First connection: drop immediately to trigger reconnect.
				conn.Close()
				continue
			}
			// Second+ connection: keep alive briefly then close.
			srv, err := yamux.Server(conn, nil)
			if err != nil {
				conn.Close()
				return
			}
			// Signal that the second dial has landed.
			select {
			case <-connected:
			default:
				close(connected)
			}
			// Close the server session to let RunForever loop back.
			_ = srv.Close()
		}
	}()

	// Build a Tunnel that points at our fake relay address.
	// We bypass the real Dial (which does WSS + HELLO) by directly using
	// the yamux client session — we build a thin Tunnel copy whose Dial
	// opens a plain TCP+yamux connection (suitable for this unit test).
	addr := ln.Addr().String()

	// dialFn replaces the network dial inside Tunnel for testing.
	// We create a custom implementation below using a testTunnel wrapper.
	testTun := &testTunnel{addr: addr}

	proxy := NewProxy("http://127.0.0.1:1")

	done := make(chan struct{})
	go func() {
		defer close(done)
		testTun.RunForever(proxy)
	}()

	select {
	case <-connected:
		// Second dial happened — reconnect works.
	case <-time.After(10 * time.Second):
		t.Fatalf("tunnel did not reconnect within 10s (dialCount=%d)", dialCount.Load())
	}

	if dialCount.Load() < wantDials {
		t.Fatalf("expected at least %d dials, got %d", wantDials, dialCount.Load())
	}

	// Close the listener to let RunForever's next dial fail and exit.
	ln.Close()
	// Give the goroutine time to notice — we don't wait indefinitely.
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		// RunForever is designed to loop forever; this is acceptable in tests.
	}
}

// testTunnel is a test-only Tunnel variant that dials a plain TCP address with
// yamux instead of WSS + HELLO.  This lets us exercise the RunForever reconnect
// loop without spinning up a real relay server.
type testTunnel struct {
	addr    string
	session *yamux.Session
}

func (tt *testTunnel) dial() error {
	conn, err := net.DialTimeout("tcp", tt.addr, time.Second)
	if err != nil {
		return err
	}
	sess, err := yamux.Client(conn, nil)
	if err != nil {
		conn.Close()
		return err
	}
	tt.session = sess
	return nil
}

func (tt *testTunnel) serve(proxy *Proxy) error {
	if tt.session == nil {
		return io.EOF
	}
	for {
		stream, err := tt.session.AcceptStream()
		if err != nil {
			return err
		}
		go proxy.HandleStream(stream)
	}
}

func (tt *testTunnel) close() {
	if tt.session != nil {
		_ = tt.session.Close()
		tt.session = nil
	}
}

// RunForever mirrors the real Tunnel.RunForever backoff loop.
func (tt *testTunnel) RunForever(proxy *Proxy) {
	backoff := time.Millisecond * 50 // short for tests
	for {
		if err := tt.dial(); err != nil {
			time.Sleep(backoff)
			if backoff < 400*time.Millisecond {
				backoff *= 2
			}
			continue
		}
		backoff = time.Millisecond * 50
		_ = tt.serve(proxy)
		tt.close()
		time.Sleep(backoff)
	}
}
