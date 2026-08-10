package api

import (
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/gin-gonic/gin"
)

// drivesMarker is a virtual path representing the Windows "This PC" level — the
// list of drive roots (C:\, D:\, …). Going "up" from a drive root lands here so
// the user can switch drives (filepath.Dir("C:\\") is "C:\\" itself, a dead end).
const drivesMarker = "::drives"

// FSHandler exposes a read-only local-directory browser, used by the office
// assistant's knowledge-base folder picker (the browser can't reveal absolute
// paths, so the picker walks the server's filesystem instead).
//
// Personal edition ONLY: the server runs on the user's own machine, so listing
// their directories is legitimate. In team edition the server is remote and its
// filesystem is not the user's — exposing it would be info disclosure — so the
// endpoint is disabled (404) and the UI falls back to manual path entry.
type FSHandler struct {
	enabled bool // true in personal edition (auth disabled)
}

func NewFSHandler(personal bool) *FSHandler { return &FSHandler{enabled: personal} }

type fsDirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// maxDirEntries caps a single listing so a huge directory can't bloat the
// response.
const maxDirEntries = 2000

// ListDirs: GET /api/fs/list-dirs?path=<abs> — list the immediate
// subdirectories of `path` (defaults to the user's home). Directories only —
// no files, no file contents. Hidden (dot) directories are omitted.
func (h *FSHandler) ListDirs(c *gin.Context) {
	if !h.enabled {
		c.Status(http.StatusNotFound)
		return
	}
	home, _ := os.UserHomeDir()
	dir := strings.TrimSpace(c.Query("path"))

	// Windows "This PC" level: list available drive roots so the user can switch
	// between C:, D:, … (which the plain directory walk can't reach).
	if runtime.GOOS == "windows" && dir == drivesMarker {
		c.JSON(http.StatusOK, gin.H{
			"path":   drivesMarker,
			"parent": "",
			"home":   home,
			"dirs":   windowsDrives(),
		})
		return
	}

	if dir == "" {
		dir = home
	}
	dir = filepath.Clean(dir)
	if !filepath.IsAbs(dir) {
		BadRequest(c, "path must be absolute")
		return
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		c.Status(http.StatusNotFound)
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		InternalError(c, err)
		return
	}
	dirs := make([]fsDirEntry, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dirs = append(dirs, fsDirEntry{Name: e.Name(), Path: filepath.Join(dir, e.Name())})
		if len(dirs) >= maxDirEntries {
			break
		}
	}
	sort.Slice(dirs, func(i, j int) bool {
		return strings.ToLower(dirs[i].Name) < strings.ToLower(dirs[j].Name)
	})
	parent := filepath.Dir(dir)
	if parent == dir {
		// At a root. On Windows, "up" goes to the drive list (so the user can
		// switch C:/D:/…); elsewhere an empty parent means "can't go up".
		if runtime.GOOS == "windows" {
			parent = drivesMarker
		} else {
			parent = ""
		}
	}
	c.JSON(http.StatusOK, gin.H{
		"path":   dir,
		"parent": parent,
		"home":   home,
		"dirs":   dirs,
	})
}

// windowsDrives returns the available drive roots (C:\, D:\, …) by probing each
// letter — the "This PC" level for the picker.
func windowsDrives() []fsDirEntry {
	out := make([]fsDirEntry, 0, 8)
	for c := 'A'; c <= 'Z'; c++ {
		p := string(c) + ":\\"
		if fi, err := os.Stat(p); err == nil && fi.IsDir() {
			out = append(out, fsDirEntry{Name: string(c) + ":", Path: p})
		}
	}
	return out
}
