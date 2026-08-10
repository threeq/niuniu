package main

import (
	"encoding/json"
	"io"
)

// emitReady writes a single-line JSON envelope announcing that the server
// is bound and ready to accept connections on addr. The parent process
// (niuniu-desktop) reads exactly one line from the child's stdout and
// parses this envelope. Any additional stdout output from the child
// (logs etc.) must be redirected elsewhere in embedded mode.
func emitReady(w io.Writer, addr string) error {
	payload := struct {
		Event string `json:"event"`
		Addr  string `json:"addr"`
	}{Event: "ready", Addr: addr}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	b = append(b, '\n')
	_, err = w.Write(b)
	return err
}
