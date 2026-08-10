package probe

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestReadConfiguredPort_Valid(t *testing.T) {
	dir := t.TempDir()
	yaml := `server:
  port: 4242
storage:
  driver: sqlite
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte(yaml), 0o644))

	port := readConfiguredPort(dir)
	require.Equal(t, 4242, port)
}

func TestReadConfiguredPort_Malformed(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("{not: valid: yaml"), 0o644))

	port := readConfiguredPort(dir)
	require.Equal(t, 0, port, "malformed yaml returns 0")
}

func TestReadConfiguredPort_Missing(t *testing.T) {
	dir := t.TempDir() // no config.yaml
	port := readConfiguredPort(dir)
	require.Equal(t, 0, port)
}
