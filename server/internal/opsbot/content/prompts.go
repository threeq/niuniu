package content

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/opsbot/packs"
)

// Pack is a resolved platform pack: its manifest plus a filesystem rooted at
// the pack version directory, so prompts/ can be read without unpacking.
type Pack struct {
	Manifest packs.Manifest
	Files    fs.FS
}

// PackResolver finds the pack version a content run should use.
type PackResolver interface {
	Resolve(ctx context.Context, packID string) (Pack, error)
}

// BuiltinPacks resolves the seed packs embedded in the server.
type BuiltinPacks struct{}

var _ PackResolver = BuiltinPacks{}

func (BuiltinPacks) Resolve(ctx context.Context, packID string) (Pack, error) {
	if err := ctx.Err(); err != nil {
		return Pack{}, err
	}
	manifest, files, err := packs.BuiltinPack(packID)
	if err != nil {
		return Pack{}, err
	}
	return Pack{Manifest: manifest, Files: files}, nil
}

// RegistryPacks resolves the highest published pack version in an owner's pack
// registry.
type RegistryPacks struct {
	Registry  *packs.Registry
	OwnerType string
	OwnerID   int64
}

var _ PackResolver = RegistryPacks{}

func (r RegistryPacks) Resolve(ctx context.Context, packID string) (Pack, error) {
	if err := ctx.Err(); err != nil {
		return Pack{}, err
	}
	if r.Registry == nil {
		return Pack{}, errors.New("content: pack registry is not configured")
	}
	manifest, err := r.Registry.LatestPublished(r.OwnerType, r.OwnerID, packID)
	if err != nil {
		return Pack{}, err
	}
	root, err := r.Registry.Root(r.OwnerType, r.OwnerID, packID, manifest.Version)
	if err != nil {
		return Pack{}, err
	}
	return Pack{Manifest: manifest, Files: os.DirFS(root)}, nil
}

// Prompt is one pack prompt handed to a copy provider.
type Prompt struct {
	Name string `json:"name"`
	Body string `json:"body"`
}

// copyPromptNames lists the writing prompts a format uses. Missing prompts are
// skipped, so a pack can ship only the ones it needs.
func copyPromptNames(format string) []string {
	var structure string
	switch format {
	case FormatArticle:
		structure = "long-form-structure.md"
	case FormatNote:
		structure = "note-structure.md"
	}
	names := []string{"topic-selection.md", "title.md"}
	if structure != "" {
		names = append(names, structure)
	}
	if format == FormatNote {
		names = append(names, "tone.md")
	}
	return names
}

func coverPromptName(format string) string {
	if format == FormatArticle {
		return "cover-copy.md"
	}
	return "cover.md"
}

func layoutPromptName(format string) string {
	if format == FormatArticle {
		return "layout.md"
	}
	return "layout-tags.md"
}

func loadPrompts(files fs.FS, names ...string) ([]Prompt, error) {
	if files == nil {
		return nil, nil
	}
	out := make([]Prompt, 0, len(names))
	for _, name := range names {
		body, err := promptText(files, name)
		if err != nil {
			return nil, err
		}
		if body == "" {
			continue
		}
		out = append(out, Prompt{Name: name, Body: body})
	}
	return out, nil
}

// promptText reads one prompt; a pack that does not ship it yields "".
func promptText(files fs.FS, name string) (string, error) {
	if files == nil || strings.TrimSpace(name) == "" {
		return "", nil
	}
	data, err := fs.ReadFile(files, path.Join("prompts", name))
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("content: read prompt %s: %w", name, err)
	}
	return string(data), nil
}
