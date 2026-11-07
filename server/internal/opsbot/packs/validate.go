package packs

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

var validStatuses = map[string]bool{
	"draft":      true,
	"testing":    true,
	"published":  true,
	"deprecated": true,
}

var validTransports = map[string]bool{
	"openapi": true,
	"mcp":     true,
	"browser": true,
	"manual":  true,
}

var validCredentialScopes = map[string]bool{
	"owner":   true,
	"account": true,
}

// Validate checks that a Platform Pack is internally consistent and that every
// browser recipe it references exists under root and parses as a complete
// Recipe. root is the pack version directory.
func Validate(root string, m Manifest) error {
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("pack id %q is invalid: must match %s", m.ID, idPattern)
	}
	if m.Version <= 0 {
		return fmt.Errorf("pack %s version must be > 0, got %d", m.ID, m.Version)
	}
	if !validStatuses[m.Status] {
		return fmt.Errorf("pack %s has invalid status %q", m.ID, m.Status)
	}
	if len(m.Capabilities) == 0 {
		return fmt.Errorf("pack %s declares no capabilities", m.ID)
	}

	declared := make(map[string]bool, len(m.Capabilities))
	for _, capability := range m.Capabilities {
		if strings.TrimSpace(capability) == "" {
			return fmt.Errorf("pack %s declares an empty capability", m.ID)
		}
		if declared[capability] {
			return fmt.Errorf("pack %s declares capability %q more than once", m.ID, capability)
		}
		declared[capability] = true

		binding, ok := m.Bindings[capability]
		if !ok {
			return fmt.Errorf("pack %s capability %q has no binding", m.ID, capability)
		}
		if err := validateBinding(root, m.ID, capability, binding); err != nil {
			return err
		}
	}

	slotNames := make([]string, 0, len(m.CredentialSlots))
	for name := range m.CredentialSlots {
		slotNames = append(slotNames, name)
	}
	sort.Strings(slotNames)
	for _, name := range slotNames {
		if !validCredentialScopes[m.CredentialSlots[name].Scope] {
			return fmt.Errorf("pack %s credential slot %q has invalid scope %q",
				m.ID, name, m.CredentialSlots[name].Scope)
		}
	}

	if declared["publish"] && len(m.ContentSchema.Formats) == 0 {
		return fmt.Errorf("pack %s declares publish but has no content format", m.ID)
	}
	if declared["metrics"] && len(m.MetricsMap.Fields) == 0 {
		return fmt.Errorf("pack %s declares metrics but has no metrics map fields", m.ID)
	}

	return nil
}

func validateBinding(root, packID, capability string, b Binding) error {
	if !validTransports[b.Transport] {
		return fmt.Errorf("pack %s capability %q has invalid transport %q", packID, capability, b.Transport)
	}

	switch b.Transport {
	case "openapi", "mcp":
		if strings.TrimSpace(b.Provider) == "" {
			return fmt.Errorf("pack %s capability %q uses %s without a provider", packID, capability, b.Transport)
		}
	case "browser":
		if strings.TrimSpace(b.Recipe) == "" {
			return fmt.Errorf("pack %s capability %q uses browser without a recipe", packID, capability)
		}
		return validateRecipe(root, packID, capability, b.Recipe)
	}

	return nil
}

func validateRecipe(root, packID, capability, recipePath string) error {
	data, err := readInsideRoot(root, recipePath)
	if err != nil {
		return fmt.Errorf("pack %s capability %q recipe %q: %w", packID, capability, recipePath, err)
	}

	var recipe Recipe
	if err := json.Unmarshal(data, &recipe); err != nil {
		return fmt.Errorf("pack %s capability %q recipe %q is invalid JSON: %w", packID, capability, recipePath, err)
	}
	if strings.TrimSpace(recipe.Name) == "" {
		return fmt.Errorf("pack %s capability %q recipe %q has no name", packID, capability, recipePath)
	}
	if len(recipe.Steps) == 0 {
		return fmt.Errorf("pack %s capability %q recipe %q has no steps", packID, capability, recipePath)
	}
	for i, step := range recipe.Steps {
		if strings.TrimSpace(step.Action) == "" {
			return fmt.Errorf("pack %s capability %q recipe %q step %d has no action",
				packID, capability, recipePath, i+1)
		}
	}

	return nil
}

// readInsideRoot reads a pack-relative file through os.Root, which rejects
// absolute paths, "..", and symlinks that resolve outside root. This keeps an
// agent-generated manifest from reading arbitrary host files.
func readInsideRoot(root, name string) ([]byte, error) {
	if !filepath.IsLocal(name) {
		return nil, fmt.Errorf("recipe path must stay inside the pack root")
	}
	r, err := os.OpenRoot(root)
	if err != nil {
		return nil, err
	}
	defer r.Close()

	f, err := r.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	return io.ReadAll(f)
}
