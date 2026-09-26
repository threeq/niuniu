package niuniuagent

import (
	"bytes"
	"log/slog"
	"strings"
	"sync"
)

// stderrLogger pipes the niuniu-agent child's stderr into the server log,
// one slog record per line. The agent logs its own diagnostics (model-client
// failures, panics, compact/summarizer activity) to stderr; without this
// bridge those were inherited by the server terminal and lost when niuniu
// runs as a service — a hung turn had no trace.
type stderrLogger struct {
	backend string
	mu      sync.Mutex
	buf     bytes.Buffer
}

func (w *stderrLogger) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf.Write(p)
	for {
		i := bytes.IndexByte(w.buf.Bytes(), '\n')
		if i < 0 {
			// Bound a pathological unterminated line.
			if w.buf.Len() > 8<<10 {
				slog.Warn("niuniu-agent stderr", "line", w.buf.String())
				w.buf.Reset()
			}
			break
		}
		line := strings.TrimRight(w.buf.String()[:i], "\r ")
		w.buf.Next(i + 1) // consume the line plus '\n'
		if line != "" {
			slog.Info("niuniu-agent stderr", "line", line)
		}
	}
	return len(p), nil
}
