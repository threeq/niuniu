package main

import (
	"io"
)

// watchParentPipe reads r until EOF (or error) in a goroutine and then
// calls onEOF exactly once. Intended use: the caller passes os.Stdin in
// embedded mode; when the parent (niuniu-desktop) process exits the
// pipe closes and onEOF fires, letting us shut down gracefully instead
// of becoming an orphan.
func watchParentPipe(r io.Reader, onEOF func()) {
	go func() {
		_, _ = io.Copy(io.Discard, r)
		if onEOF != nil {
			onEOF()
		}
	}()
}
