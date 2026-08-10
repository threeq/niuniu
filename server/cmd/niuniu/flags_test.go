package main

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseFlagsEmbedded(t *testing.T) {
	f, rest, err := parseFlags([]string{"--embedded", "--addr=127.0.0.1:0"})
	require.NoError(t, err)
	require.True(t, f.Embedded)
	require.Equal(t, "127.0.0.1:0", f.Addr)
	require.Empty(t, rest)
}

func TestParseFlagsDefault(t *testing.T) {
	f, rest, err := parseFlags(nil)
	require.NoError(t, err)
	require.False(t, f.Embedded)
	require.Equal(t, "", f.Addr)
	require.Empty(t, rest)
}

func TestParseFlagsRejectsUnknown(t *testing.T) {
	_, _, err := parseFlags([]string{"--unknown"})
	require.Error(t, err)
}
