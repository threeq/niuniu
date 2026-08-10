package service

import (
	"log/slog"
	"os"
	"time"
)

// removeDirectoryWithProcessCleanup kills processes running inside dir,
// then removes it. Retries up to 3 times on failure (handles Windows
// file-lock timing where native modules need a moment to be released).
func removeDirectoryWithProcessCleanup(dir string) {
	if dir == "" {
		return
	}

	killProcessesInDir(dir)

	var removeErr error
	for attempt := range 3 {
		if removeErr = os.RemoveAll(dir); removeErr == nil {
			return
		}
		if attempt < 2 {
			killProcessesInDir(dir)
		}
		time.Sleep(500 * time.Millisecond)
	}

	slog.Warn("failed to remove workspace directory, will be cleaned up later",
		"path", dir, "error", removeErr)
}
