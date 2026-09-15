package tools

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// Read returns a file's content with line numbers (cat -n style). The
// default line cap bounds context damage on huge files.
type Read struct{}

// readInput.
type readInput struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"` // 1-based first line to print
	Limit  int    `json:"limit"`  // max lines to print
}

// readDefaultLimit is the line cap when the model does not set one.
const readDefaultLimit = 2000

func (Read) Def() model.ToolDef {
	return model.ToolDef{
		Name: "Read",
		Description: "Reads a text file and returns its content with line numbers. " +
			"Set offset (1-based line) to read from a position and limit to cap the number of lines. Refuses binary files.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"path":{"type":"string","description":"File path to read"},` +
			`"offset":{"type":"integer","description":"1-based line number to start from"},` +
			`"limit":{"type":"integer","description":"Max lines to return (default 2000)"}},` +
			`"required":["path"]}`),
	}
}

func (Read) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in readInput
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("invalid input: %w", err)
		}
	}
	if in.Path == "" {
		return "", errors.New("path is required")
	}
	f, err := os.Open(in.Path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	// Binary sniff: a NUL byte in the first 8 KiB means "not text".
	sniff := make([]byte, 8<<10)
	n, _ := io.ReadFull(f, sniff)
	if n > 0 && bytes.IndexByte(sniff[:n], 0) >= 0 {
		return "", fmt.Errorf("%s: binary file, refusing to read", in.Path)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}

	limit := in.Limit
	if limit <= 0 {
		limit = readDefaultLimit
	}
	start := in.Offset
	if start <= 0 {
		start = 1
	}

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var b strings.Builder
	line, printed := 0, 0
	for sc.Scan() {
		line++
		if line < start {
			continue
		}
		fmt.Fprintf(&b, "%6d\t%s\n", line, sc.Text())
		printed++
		if printed >= limit {
			break
		}
	}
	if err := sc.Err(); err != nil {
		return "", err
	}
	if printed == 0 {
		return "(empty file, or offset beyond end of file)", nil
	}
	return b.String(), nil
}
