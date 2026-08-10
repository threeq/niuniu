package main

import (
	"io"
	"strings"
	"testing"
	"time"
)

// TestWatchParentPipe_EOF verifies the watcher triggers onEOF when its
// input Reader returns EOF (simulating parent closing the pipe).
func TestWatchParentPipe_EOF(t *testing.T) {
	r := strings.NewReader("") // zero-length; first Read returns EOF
	done := make(chan struct{})
	watchParentPipe(r, func() { close(done) })
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("onEOF never fired")
	}
}

// TestWatchParentPipe_DiscardsData verifies the watcher silently drains
// bytes without invoking onEOF while the parent is alive.
func TestWatchParentPipe_DiscardsData(t *testing.T) {
	r, w := io.Pipe()
	eof := make(chan struct{})
	watchParentPipe(r, func() { close(eof) })

	_, _ = w.Write([]byte("noise\n"))
	select {
	case <-eof:
		t.Fatal("onEOF fired while pipe was still open")
	case <-time.After(200 * time.Millisecond):
	}

	_ = w.Close()
	select {
	case <-eof:
	case <-time.After(2 * time.Second):
		t.Fatal("onEOF did not fire after close")
	}
}
