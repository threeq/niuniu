package service

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestGetSystemInfo_DefaultDirs locks in the directory-picker default contract:
// SystemInfo exposes the niuniu data dir (so the SPA can block it) and a home
// dir when the OS resolves one, both Unix-style (forward-slash). See SystemInfo
// field comments.
func TestGetSystemInfo_DefaultDirs(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), ".niuniu")
	info := NewDirectoryService(dataDir).GetSystemInfo()

	if info.OS != runtime.GOOS {
		t.Errorf("OS = %q, want %q", info.OS, runtime.GOOS)
	}
	if info.DataDir == "" {
		t.Error("DataDir must not be empty")
	}
	if strings.Contains(info.DataDir, "\\") {
		t.Errorf("DataDir must be Unix-style (no backslash), got %q", info.DataDir)
	}
	// HomeDir is best-effort (empty when os.UserHomeDir fails), but when set it
	// must be Unix-style too.
	if strings.Contains(info.HomeDir, "\\") {
		t.Errorf("HomeDir must be Unix-style (no backslash), got %q", info.HomeDir)
	}
}

// TestGetSystemInfo_CleansHomeDir guards the personal/desktop quirk where the
// launcher sets the home env to "<dataDir>/.." (so os.UserHomeDir() returns a
// path with a literal ".." segment). GetSystemInfo must Clean it so the picker
// opens on the real home dir, not an ugly "…/.niuniu/.." path.
func TestGetSystemInfo_CleansHomeDir(t *testing.T) {
	base := t.TempDir()
	raw := filepath.Join(base, ".niuniu") + string(filepath.Separator) + ".."
	t.Setenv("HOME", raw)
	t.Setenv("USERPROFILE", raw)

	info := NewDirectoryService(filepath.Join(base, ".niuniu")).GetSystemInfo()

	want := filepath.ToSlash(filepath.Clean(base))
	if info.HomeDir != want {
		t.Errorf("HomeDir = %q, want cleaned %q", info.HomeDir, want)
	}
	if strings.Contains(info.HomeDir, "..") {
		t.Errorf("HomeDir must not contain '..': %q", info.HomeDir)
	}
}

// TestValidatePath_BlocksNiuniuDataDir locks in that the data dir and its
// descendants are rejected, while sibling/parent dirs stay allowed.
func TestValidatePath_BlocksNiuniuDataDir(t *testing.T) {
	home := t.TempDir()
	dataDir := filepath.Join(home, ".niuniu")
	svc := NewDirectoryService(dataDir)

	blocked := []string{
		dataDir,
		filepath.Join(dataDir, "workspaces"),
		filepath.Join(dataDir, "workspaces", "deep", "nested"),
	}
	for _, p := range blocked {
		if err := svc.ValidatePath(p); err == nil {
			t.Errorf("ValidatePath(%q) = nil, want rejection (inside data dir)", p)
		}
		if !svc.IsForbidden(p) {
			t.Errorf("IsForbidden(%q) = false, want true", p)
		}
	}

	allowed := []string{
		home,
		filepath.Join(home, "projects"),
		filepath.Join(home, ".niuniu-not-really"), // prefix match must not over-block
	}
	for _, p := range allowed {
		if err := svc.ValidatePath(p); err != nil {
			t.Errorf("ValidatePath(%q) = %v, want nil (outside data dir)", p, err)
		}
		if svc.IsForbidden(p) {
			t.Errorf("IsForbidden(%q) = true, want false", p)
		}
	}
}
