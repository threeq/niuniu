package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestEmitReady(t *testing.T) {
	var buf bytes.Buffer
	err := emitReady(&buf, "127.0.0.1:54321")
	require.NoError(t, err)

	line := buf.String()
	require.True(t, len(line) > 0 && line[len(line)-1] == '\n', "must end with newline")

	var parsed struct {
		Event string `json:"event"`
		Addr  string `json:"addr"`
	}
	require.NoError(t, json.Unmarshal([]byte(line[:len(line)-1]), &parsed))
	require.Equal(t, "ready", parsed.Event)
	require.Equal(t, "127.0.0.1:54321", parsed.Addr)
}
