// Package skills implements niuniu-agent's skill discovery: SKILL.md files
// under <cwd>/.niuniu-agent/skills/<name>/ (project) and
// ~/.niuniu-agent/skills/<name>/ (user). A skill is markdown with a tiny
// frontmatter (name / description); only the index rides the system prompt —
// the Skill tool loads the full body on demand, so token cost stays
// proportional to what the task actually needs.
package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// SkillsDir is the well-known skills directory under both the project cwd
// and the user home.
const SkillsDir = ".niuniu-agent/skills"

// Skill is one discovered skill.
type Skill struct {
	// Name comes from frontmatter, falling back to the directory name.
	Name string
	// Description comes from frontmatter; may be empty.
	Description string
	// Path is the SKILL.md file, for LoadBody.
	Path string
}

// Scan discovers skills for a session rooted at cwd: project skills first
// (they win name collisions), then user-home skills. Missing directories
// are not errors.
func Scan(cwd string) []Skill {
	var out []Skill
	seen := map[string]bool{}
	roots := []string{filepath.Join(cwd, filepath.FromSlash(SkillsDir))}
	if home, err := os.UserHomeDir(); err == nil {
		roots = append(roots, filepath.Join(home, filepath.FromSlash(SkillsDir)))
	}
	for _, root := range roots {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		// Deterministic order within a root.
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			path := filepath.Join(root, e.Name(), "SKILL.md")
			data, err := os.ReadFile(path)
			if err != nil {
				continue // no SKILL.md → not a skill
			}
			s := parse(data, e.Name())
			if s.Name == "" {
				s.Name = e.Name()
			}
			if seen[s.Name] {
				continue // project scope shadows user scope
			}
			seen[s.Name] = true
			s.Path = path
			out = append(out, s)
		}
	}
	return out
}

// parse splits the optional frontmatter (--- lines with name/description)
// from the markdown body.
func parse(data []byte, fallbackName string) (s Skill) {
	text := string(data)
	s.Name = fallbackName
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		rest, ok = strings.CutPrefix(text, "---\r\n")
	}
	if !ok {
		return s // no frontmatter
	}
	fm, body, found := strings.Cut(rest, "\n---")
	if !found {
		return s // unterminated frontmatter → treat whole file as body
	}
	for _, line := range strings.Split(fm, "\n") {
		key, val, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok {
			continue
		}
		val = strings.Trim(strings.TrimSpace(val), `"'`)
		switch strings.TrimSpace(key) {
		case "name":
			if val != "" {
				s.Name = val
			}
		case "description":
			s.Description = val
		}
	}
	_ = body
	return s
}

// LoadBody reads the SKILL.md at path and returns the markdown body with
// frontmatter stripped — the full instructions injected into the context.
func LoadBody(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	text := string(data)
	rest, ok := strings.CutPrefix(text, "---\n")
	if !ok {
		rest, ok = strings.CutPrefix(text, "---\r\n")
	}
	if !ok {
		return strings.TrimSpace(text), nil
	}
	if _, body, found := strings.Cut(rest, "\n---"); found {
		// Skip the remainder of the closing-fence line (--- or ---\r\n …).
		if i := strings.IndexByte(body, '\n'); i >= 0 {
			body = body[i+1:]
		}
		return strings.TrimSpace(body), nil
	}
	return strings.TrimSpace(text), nil
}

// Index renders the system-prompt lines for the discovered skills
// (name + description only — bodies load on demand via the Skill tool).
func Index(list []Skill) string {
	if len(list) == 0 {
		return ""
	}
	var b strings.Builder
	for _, s := range list {
		b.WriteString("- " + s.Name)
		if s.Description != "" {
			b.WriteString(": " + s.Description)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// Tool loads one skill's full instructions into the context by name.
type Tool struct {
	list []Skill
}

// NewTool builds the Skill tool over a discovered list.
func NewTool(list []Skill) *Tool { return &Tool{list: list} }

// Def implements tools.Tool.
func (t *Tool) Def() model.ToolDef {
	return model.ToolDef{
		Name: "Skill",
		Description: "Load a skill's complete instructions into the conversation by name. " +
			"Use when the task matches one of the skills listed in the system prompt's Skills section. " +
			"Only the index (name + description) is preloaded; call this before following a skill.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"name":{"type":"string","description":"Skill name from the Skills section"}},` +
			`"required":["name"]}`),
	}
}

// Execute implements tools.Tool.
func (t *Tool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(in.Name) == "" {
		return "", fmt.Errorf("name is required")
	}
	for _, s := range t.list {
		if s.Name == in.Name {
			return LoadBody(s.Path)
		}
	}
	names := make([]string, 0, len(t.list))
	for _, s := range t.list {
		names = append(names, s.Name)
	}
	return "", fmt.Errorf("unknown skill %q (available: %s)", in.Name, strings.Join(names, ", "))
}
