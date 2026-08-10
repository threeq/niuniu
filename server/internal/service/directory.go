package service

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

type DirectoryService struct {
	// dataDir is the niuniu data dir (~/.niuniu). The directory picker must
	// never let a user select this dir or any descendant (it holds the DB,
	// workspaces, agent homes, etc.), so it is off-limits for listing,
	// selection, and submission.
	dataDir string
}

func NewDirectoryService(dataDir string) *DirectoryService {
	return &DirectoryService{dataDir: dataDir}
}

// pathWithin reports whether path is base or a descendant of base. Symlinks
// are resolved first (so a symlink pointing into the data dir is still caught);
// when a path can't be resolved (e.g. it doesn't exist yet) we fall back to the
// lexical form. Comparison is case-insensitive on Windows. Empty base never
// matches.
func pathWithin(path, base string) bool {
	if base == "" {
		return false
	}
	if rp, err := filepath.EvalSymlinks(path); err == nil {
		path = rp
	}
	if rb, err := filepath.EvalSymlinks(base); err == nil {
		base = rb
	}
	p := filepath.Clean(path)
	b := filepath.Clean(base)
	if runtime.GOOS == "windows" {
		p = strings.ToLower(p)
		b = strings.ToLower(b)
	}
	if p == b {
		return true
	}
	return strings.HasPrefix(p, b+string(filepath.Separator))
}

// IsForbidden reports whether path falls inside the niuniu data dir and must
// therefore be rejected by the picker / create flows.
func (s *DirectoryService) IsForbidden(path string) bool {
	return pathWithin(path, s.dataDir)
}

type DirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type DirectoryListResult struct {
	Path        string           `json:"path"`
	Parent      *string          `json:"parent,omitempty"`
	Directories []DirectoryEntry `json:"directories"`
}

// List returns subdirectories for a given path
func (s *DirectoryService) List(path string) (*DirectoryListResult, error) {
	// Validate path is absolute
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("path must be absolute")
	}

	// Check if path exists
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("path does not exist: %s", path)
		}
		return nil, fmt.Errorf("cannot access path: %s", err.Error())
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("path is not a directory: %s", path)
	}

	// Get parent directory
	var parent *string
	parentDir := filepath.Dir(path)
	if parentDir != path {
		parent = &parentDir
	}

	// Read directory entries
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, fmt.Errorf("cannot read directory: %s", err.Error())
	}

	var directories []DirectoryEntry
	for _, entry := range entries {
		if entry.IsDir() {
			fullPath := filepath.Join(path, entry.Name())
			// Convert to Unix-style path for API response
			unixPath := filepath.ToSlash(fullPath)
			directories = append(directories, DirectoryEntry{
				Name: entry.Name(),
				Path: unixPath,
			})
		}
	}

	return &DirectoryListResult{
		Path:        filepath.ToSlash(path),
		Parent:      parent,
		Directories: directories,
	}, nil
}

// CreateDirectory creates a directory (and parent dirs) at the given path
func (s *DirectoryService) CreateDirectory(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute")
	}

	// Check if path already exists
	if _, err := os.Stat(path); err == nil {
		// Already exists, check if it's a directory
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("cannot access path: %s", err.Error())
		}
		if !info.IsDir() {
			return fmt.Errorf("path is a file, not a directory")
		}
		return nil // Already exists as directory, that's fine
	}

	// Create directory recursively
	if err := os.MkdirAll(path, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %s", err.Error())
	}
	return nil
}

// ValidatePath checks if a path is safe to access
func (s *DirectoryService) ValidatePath(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute")
	}

	// Block the niuniu data dir and everything under it — it is not a valid
	// place to put a user repository / workspace directory.
	if s.IsForbidden(path) {
		return fmt.Errorf("access denied: path is inside the niuniu data directory")
	}

	// Block dangerous paths (common across platforms)
	lower := strings.ToLower(path)
	if strings.Contains(lower, "/proc") ||
		strings.Contains(lower, "\\windows\\system32") ||
		strings.Contains(lower, "/sys/kernel") {
		return fmt.Errorf("access denied: %s", path)
	}

	return nil
}

// SystemInfo contains server system information
type SystemInfo struct {
	OS         string   `json:"os"`          // "windows" or "linux"
	DiskDrives []string `json:"disk_drives"` // Windows drive letters, empty on Linux
	// HomeDir is the directory picker's only default location — the picker must
	// never open in (or allow selecting) the ~/.niuniu data dir.
	HomeDir string `json:"home_dir"` // user home dir (Unix-style), empty if unresolved
	// DataDir is the niuniu data dir (~/.niuniu), Unix-style. The SPA uses it to
	// disable selecting this dir or any descendant and to surface an inline
	// error before submit. The backend enforces the same rule in ValidatePath.
	DataDir string `json:"data_dir"`
}

// GetSystemInfo returns system information for the frontend
func (s *DirectoryService) GetSystemInfo() *SystemInfo {
	info := &SystemInfo{
		OS:      runtime.GOOS,
		DataDir: filepath.ToSlash(filepath.Clean(s.dataDir)),
	}

	// Clean before exposing: in personal/desktop mode USERPROFILE is set to
	// "<dataDir>\.." (see desktop/internal/bundle), so os.UserHomeDir() returns
	// a path with a literal ".." segment. Without Clean the picker would open
	// on an ugly "…/.niuniu/.." path. Clean collapses it to the real home dir.
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		info.HomeDir = filepath.ToSlash(filepath.Clean(home))
	}

	if runtime.GOOS == "windows" {
		info.DiskDrives = getWindowsDiskDrives()
	}

	return info
}

