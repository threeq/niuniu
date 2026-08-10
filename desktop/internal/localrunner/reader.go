package localrunner

import (
	"fmt"
	"io"
	"os"
)

// reader.go implements local_read: return the contents of a file inside the
// bound directory. The path is resolved through the gateway so the hard
// boundary applies to reads exactly as it does to command working dirs.

// maxReadBytes caps a single local_read so a huge file can't overflow the WS
// frame / server read limit. Larger files are truncated with a marker.
const maxReadBytes = 512 * 1024

// ReadFile resolves rel inside g's bound directory and returns the file
// contents (truncated at maxReadBytes). now supplies audit timestamps.
func ReadFile(g *Gateway, rel string, now func() int64) (string, error) {
	abs, err := g.ResolvePath(rel, now)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("stat %q: %w", rel, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%q is a directory", rel)
	}
	f, err := os.Open(abs)
	if err != nil {
		return "", fmt.Errorf("open %q: %w", rel, err)
	}
	defer f.Close()

	// Read one byte past the cap so we can detect (and mark) truncation.
	data, err := io.ReadAll(io.LimitReader(f, maxReadBytes+1))
	if err != nil {
		return "", fmt.Errorf("read %q: %w", rel, err)
	}
	if len(data) > maxReadBytes {
		return string(data[:maxReadBytes]) + "\n…(truncated)…", nil
	}
	return string(data), nil
}
