package registry

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// ValidateAgentName ensures the name is safe for filesystem operations.
func ValidateAgentName(name string) error {
	if name == "" {
		return fmt.Errorf("agent name cannot be empty")
	}
	if strings.ContainsAny(name, `/\`) || filepath.Base(name) != name || name == "." || name == ".." {
		return fmt.Errorf("invalid agent name %q", name)
	}
	return nil
}

type AgentInfo struct {
	Source      string   `json:"source"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	ClonedFrom  string   `json:"cloned_from,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	Author      string   `json:"author,omitempty"`
	FilePath    string   `json:"file_path,omitempty"`
	SourceURL   string   `json:"source_url,omitempty"`
	// DisplayName is an optional localized label (e.g. the Chinese name from a
	// curated catalog). Name stays a filesystem-safe slug used as the identifier;
	// DisplayName is for presentation only and is not persisted on import.
	DisplayName string `json:"display_name,omitempty"`
	// Emoji is an optional decorative glyph surfaced for catalog beautification.
	// Like color/vibe it is display-only metadata and is not persisted on import.
	Emoji string `json:"emoji,omitempty"`
}

type AgentDetail struct {
	AgentInfo
	Content string `json:"content"`
}

type AgentSource interface {
	Type() string
	List(ctx context.Context) ([]AgentInfo, error)
	Get(ctx context.Context, name string) (*AgentDetail, error)
	Refresh(ctx context.Context) error
}
