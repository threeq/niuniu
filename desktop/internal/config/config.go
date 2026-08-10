package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

type Connection struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Host      string    `json:"host"`
	Port      int       `json:"port"`
	IsDefault bool      `json:"is_default"`
	CreatedAt time.Time `json:"created_at"`
}

type WindowState struct {
	X         int  `json:"x"`
	Y         int  `json:"y"`
	Width     int  `json:"width"`
	Height    int  `json:"height"`
	Maximized bool `json:"maximized"`
}

type HotkeyConfig struct {
	// ToggleWindow is the global accelerator that shows/hides the LOCAL main
	// window, e.g. "Ctrl+Shift+N" (Windows/Linux) or "Cmd+Shift+N" (macOS).
	ToggleWindow string `json:"toggle_window"`
	// ToggleWindowEnabled gates registration of the main-window hotkey. Defaults
	// to true; set false to disable the shortcut entirely.
	ToggleWindowEnabled bool `json:"toggle_window_enabled"`
	// ToggleAI is the global accelerator that shows/hides the AI-aggregation
	// window, e.g. "Ctrl+Shift+Z" (Windows/Linux) or "Cmd+Shift+Z" (macOS).
	// Empty falls back to the built-in candidate list (hotkey.RegisterAI).
	ToggleAI string `json:"toggle_ai"`
	// ToggleAIEnabled gates registration of the AI hotkey. Defaults to true;
	// set false to disable the shortcut entirely.
	ToggleAIEnabled bool `json:"toggle_ai_enabled"`
}

// DefaultAIAccelerator is the platform-conventional default AI-window hotkey:
// Cmd+Shift+Z on macOS, Ctrl+Shift+Z elsewhere.
func DefaultAIAccelerator() string {
	if runtime.GOOS == "darwin" {
		return "Cmd+Shift+Z"
	}
	return "Ctrl+Shift+Z"
}

// DefaultWindowAccelerator is the platform-conventional default main-window
// hotkey: Cmd+Shift+N on macOS, Ctrl+Shift+N elsewhere.
func DefaultWindowAccelerator() string {
	if runtime.GOOS == "darwin" {
		return "Cmd+Shift+N"
	}
	return "Ctrl+Shift+N"
}

// AIService is a user-defined AI web service in the AI-aggregation window.
// Built-in services live in code (cmd/personal/aiservices.go); only custom
// (user-added) services are persisted here. ID is a nanosecond timestamp
// string, matching the Connection ID convention.
type AIService struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	URL       string    `json:"url"`
	CreatedAt time.Time `json:"created_at"`
}

// AIPrompt is one entry in the clipboard-style prompt library. Clicking a
// prompt copies Content to the system clipboard (never injected into a page).
type AIPrompt struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Content string   `json:"content"`
	// Tags are user-defined labels for grouping/filtering prompts in the library
	// (empty on legacy entries). Stored normalized: trimmed, de-duplicated, non-empty.
	Tags []string `json:"tags,omitempty"`
}

// AIConfig persists the AI-aggregation window's user state. It is intentionally
// decoupled from the SPA/server: purely local desktop preferences.
type AIConfig struct {
	// CustomServices are user-added services (beyond the built-in catalog).
	CustomServices []AIService `json:"custom_services"`
	// HiddenBuiltins holds the IDs of built-in services the user removed from
	// their rail. Built-ins are code-defined and can't be deleted, only hidden.
	HiddenBuiltins []string `json:"hidden_builtins"`
	// DefaultServiceID is the service auto-focused when the hub first opens
	// (empty = none).
	DefaultServiceID string `json:"default_service_id"`
	// LastServiceID is the most recently opened service, used when no default
	// is set ("记住上次使用的服务").
	LastServiceID string `json:"last_service_id"`
	// Prompts is the user's clipboard prompt library. Nil/empty on first run;
	// the frontend seeds a few starter prompts the user can edit or delete.
	Prompts []AIPrompt `json:"prompts"`
}

// LegacyRelayConfig is the pre-move shape of `relay` in config.json.  We keep
// it for the sole purpose of detecting a legacy config on startup and nuking
// the plaintext password that lives there — the business has moved to
// niuniu-server, which reads credentials from the OS keychain.  The struct is
// unused elsewhere; do not add new fields or start reading them.
type LegacyRelayConfig struct {
	Enabled        bool   `json:"enabled"`
	URL            string `json:"url"`
	Email          string `json:"email"`
	Password       string `json:"password"`
	LanHostEnabled bool   `json:"lan_host_enabled"`
}

// HasLegacyPassword reports true when an upgraded config still carries a
// plaintext relay password on disk.  Callers should clear it and persist the
// config again so the plaintext is not long-lived.
func (c *LegacyRelayConfig) HasLegacyPassword() bool {
	return c != nil && c.Password != ""
}

type DesktopConfig struct {
	Connections    []Connection `json:"connections"`
	Notifications  bool         `json:"notifications"`
	StartOnLogin   bool         `json:"start_on_login"`
	WindowState    WindowState  `json:"window_state"`
	Hotkey         HotkeyConfig `json:"hotkey"`
	SkippedVersion string       `json:"skipped_version"`

	// AI holds the AI-aggregation window's local state (custom services,
	// hidden built-ins, default/last service, prompt library).
	AI AIConfig `json:"ai"`

	// LegacyRelay preserves read-access to an older config layout purely so
	// the upgrade path can detect and remove the plaintext password that
	// used to live here.  Written back as zero-value by Save().
	LegacyRelay LegacyRelayConfig `json:"relay,omitempty"`
}

func DefaultDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".niuniu", "desktop")
}

func DefaultPath() string {
	return filepath.Join(DefaultDir(), "config.json")
}

func LoadFrom(path string) (*DesktopConfig, error) {
	cfg := &DesktopConfig{
		Notifications: true,
		Hotkey: HotkeyConfig{
			ToggleWindow:        DefaultWindowAccelerator(),
			ToggleWindowEnabled: true,
			ToggleAI:            DefaultAIAccelerator(),
			ToggleAIEnabled:     true,
		},
		WindowState: WindowState{Width: 1280, Height: 800},
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func Load() (*DesktopConfig, error) {
	return LoadFrom(DefaultPath())
}

func SaveTo(cfg *DesktopConfig, path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	// Atomic write: write to temp file then rename to avoid corruption on crash
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func Save(cfg *DesktopConfig) error {
	return SaveTo(cfg, DefaultPath())
}

func (c *DesktopConfig) SetDefault(id string) {
	for i := range c.Connections {
		c.Connections[i].IsDefault = c.Connections[i].ID == id
	}
}

func (c *DesktopConfig) GetDefault() *Connection {
	for i := range c.Connections {
		if c.Connections[i].IsDefault {
			return &c.Connections[i]
		}
	}
	return nil
}

// --- AI aggregation helpers (operate on the persisted AIConfig) ---

// AddAIService appends a custom service and returns its generated ID.
func (a *AIConfig) AddAIService(name, url string) string {
	id := "ai-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	a.CustomServices = append(a.CustomServices, AIService{
		ID: id, Name: name, URL: url, CreatedAt: time.Now(),
	})
	return id
}

// RemoveAIService drops a custom service by ID. Returns true if one was
// removed. Built-in services are never in CustomServices; use HideBuiltin.
func (a *AIConfig) RemoveAIService(id string) bool {
	for i := range a.CustomServices {
		if a.CustomServices[i].ID == id {
			a.CustomServices = append(a.CustomServices[:i], a.CustomServices[i+1:]...)
			return true
		}
	}
	return false
}

// IsBuiltinHidden reports whether a built-in service ID is on the hidden list.
func (a *AIConfig) IsBuiltinHidden(id string) bool {
	return slices.Contains(a.HiddenBuiltins, id)
}

// HideBuiltin adds a built-in ID to the hidden list (idempotent).
func (a *AIConfig) HideBuiltin(id string) {
	if a.IsBuiltinHidden(id) {
		return
	}
	a.HiddenBuiltins = append(a.HiddenBuiltins, id)
}

// AddPrompt appends a prompt-library entry and returns its generated ID. Tags are
// normalized (trimmed, de-duplicated, empties dropped) before storing.
func (a *AIConfig) AddPrompt(title, content string, tags []string) string {
	id := "p-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	a.Prompts = append(a.Prompts, AIPrompt{ID: id, Title: title, Content: content, Tags: normalizeTags(tags)})
	return id
}

// normalizeTags trims, drops empties, and de-duplicates (case-insensitive) tags,
// preserving first-seen order. Returns nil for no usable tags.
func normalizeTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(tags))
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		key := strings.ToLower(t)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// RemovePrompt drops a prompt by ID. Returns true if one was removed.
func (a *AIConfig) RemovePrompt(id string) bool {
	for i := range a.Prompts {
		if a.Prompts[i].ID == id {
			a.Prompts = append(a.Prompts[:i], a.Prompts[i+1:]...)
			return true
		}
	}
	return false
}
