package bundle

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestReadReadySuccess(t *testing.T) {
	input := `{"event":"ready","addr":"127.0.0.1:54321"}` + "\n"
	addr, err := readReady(strings.NewReader(input), 1*time.Second)
	require.NoError(t, err)
	require.Equal(t, "127.0.0.1:54321", addr)
}

func TestReadReadyBadJSON(t *testing.T) {
	addr, err := readReady(strings.NewReader("garbage\n"), 1*time.Second)
	require.Error(t, err)
	require.Empty(t, addr)
}

func TestReadReadyEmpty(t *testing.T) {
	// zero bytes — simulates child exiting before writing anything
	addr, err := readReady(&bytes.Buffer{}, 200*time.Millisecond)
	require.Error(t, err)
	require.Empty(t, addr)
}

func TestReadReadyWrongEvent(t *testing.T) {
	addr, err := readReady(strings.NewReader(`{"event":"other","addr":"x"}`+"\n"), 1*time.Second)
	require.Error(t, err)
	require.Empty(t, addr)
}
