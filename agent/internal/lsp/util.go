package lsp

import (
	"encoding/json"
	"fmt"
	"os"
)

func jsonUnmarshal(data []byte, v any) error { return json.Unmarshal(data, v) }

// readFileForLSP reads a source file for didOpen (bounded).
func readFileForLSP(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	data := make([]byte, 512<<10)
	n, err := f.Read(data)
	if err != nil && n == 0 {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data[:n]), nil
}
