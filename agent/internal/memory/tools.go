package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// memoryToolTypes is the advisory taxonomy offered to the model.
var memoryToolTypes = []string{TypePattern, TypeGotcha, TypeDecision, TypeUser, TypeRef}

// SaveTool is the MemorySave tool: create or update (by title) a memory.
type SaveTool struct{ store *Store }

// NewSaveTool builds MemorySave over a store.
func NewSaveTool(s *Store) tools.Tool { return SaveTool{s} }

func (t SaveTool) Def() model.ToolDef {
	return model.ToolDef{
		Name: "MemorySave",
		Description: "Persist a durable lesson, decision, or gotcha to long-term memory (project layer). " +
			"Saving an existing title UPDATES that entry instead of duplicating it — search first if unsure. " +
			"Keep content short and self-contained; it will be injected into future sessions as advisory context.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"title":{"type":"string","description":"Short unique name (the update key)"},` +
			`"type":{"type":"string","enum":["pattern","gotcha","decision","user","ref"],"description":"Entry kind"},` +
			`"content":{"type":"string","description":"The lesson itself, one or two sentences"},` +
			`"tags":{"type":"array","items":{"type":"string"},"description":"Optional keywords for recall"}},` +
			`"required":["title","content"]}`),
	}
}

func (t SaveTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Title   string   `json:"title"`
		Type    string   `json:"type"`
		Content string   `json:"content"`
		Tags    []string `json:"tags"`
	}
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("invalid input: %w", err)
	}
	if strings.TrimSpace(in.Title) == "" {
		return "", fmt.Errorf("title is required")
	}
	if strings.TrimSpace(in.Content) == "" {
		return "", fmt.Errorf("content is required")
	}
	existed := false
	if hits, _ := t.store.Search(in.Title); len(hits) > 0 {
		for _, h := range hits {
			if h.Title == in.Title {
				existed = true
			}
		}
	}
	id, err := t.store.Save(Entry{Title: in.Title, Type: in.Type, Content: in.Content, Tags: in.Tags})
	if err != nil {
		return "", err
	}
	verb := "saved"
	if existed {
		verb = "updated"
	}
	return fmt.Sprintf("%s memory %q (project layer) — it will be recalled in future sessions", verb, id), nil
}

// SearchTool is the MemorySearch tool.
type SearchTool struct{ store *Store }

// NewSearchTool builds MemorySearch over a store.
func NewSearchTool(s *Store) tools.Tool { return SearchTool{s} }

func (t SearchTool) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "MemorySearch",
		Description: "Search long-term memory by keywords (empty query lists everything). Returns entries with their ids, types, and contents, best matches first.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","description":"Keywords; empty lists all entries"}}}`),
	}
}

func (t SearchTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Query string `json:"query"`
	}
	if len(input) > 0 {
		if err := json.Unmarshal(input, &in); err != nil {
			return "", fmt.Errorf("invalid input: %w", err)
		}
	}
	hits, err := t.store.Search(in.Query)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "(no matching memories)", nil
	}
	var b strings.Builder
	for _, e := range hits {
		fmt.Fprintf(&b, "[%s] %s (id: %s, %s layer)\n  %s\n", e.Type, e.Title, e.ID, e.Layer, e.Content)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Section renders the recalled-memory system section; an empty body omits
// the section entirely. Memory is positioned as ADVISORY context — it may
// be stale, the model judges.
func Section(body string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	return `
# Memory

Lessons recalled from previous sessions (best matches first). This is ADVISORY context: it may be outdated or wrong — verify against reality before relying on it. Search more with MemorySearch; persist durable lessons with MemorySave.

` + body + "\n"
}

// reflectSystem instructs the model to distill one durable lesson from the
// transcript, in a machine-parseable format.
const reflectSystem = `You distill durable, reusable lessons from an agent working transcript.

Output EXACTLY ONE of:
1. "NONE" — if nothing worth remembering happened (routine work, no surprises).
2. A single memory entry in this format:
TITLE: <short unique name, kebab-case preferred>
TYPE: <pattern|gotcha|decision|user|ref>
---
<one or two sentences of the lesson itself, self-contained>

Rules: only durable knowledge (a pitfall, a settled decision, a non-obvious pattern, a user preference, a useful reference). NOT task status, NOT conversation recap. If unsure, output NONE.`

// Reflect runs one extra model call at end-of-session to distill a durable
// lesson from the transcript and persist it. Same-title entries update in
// place (the store dedupes). Returns the saved id, or "" when the model
// answered NONE (nothing worth remembering).
func Reflect(ctx context.Context, m model.Model, transcript string, s *Store) (string, error) {
	transcript = strings.TrimSpace(transcript)
	if transcript == "" {
		return "", nil
	}
	if len(transcript) > 24<<10 {
		transcript = transcript[:24<<10] + "\n…(truncated)"
	}
	resp, err := m.Complete(ctx, model.Request{
		System: reflectSystem,
		Messages: []model.Message{{Role: model.RoleUser, Blocks: []model.Block{
			{Type: model.BlockText, Text: transcript},
		}}},
		MaxTokens: 512,
	})
	if err != nil {
		return "", fmt.Errorf("memory reflect: %w", err)
	}
	text := strings.TrimSpace(resp.Message.Text())
	if text == "" || text == "NONE" {
		return "", nil
	}
	entry, perr := parseReflectOutput(text)
	if perr != nil {
		return "", fmt.Errorf("memory reflect: %w", perr)
	}
	return s.Save(entry)
}

// parseReflectOutput parses the TITLE/TYPE/---/body format.
func parseReflectOutput(text string) (Entry, error) {
	var e Entry
	rest, ok := strings.CutPrefix(text, "TITLE:")
	if !ok {
		return e, fmt.Errorf("output does not start with TITLE: (and is not NONE)")
	}
	title, rest, _ := strings.Cut(rest, "\n")
	e.Title = strings.TrimSpace(title)
	typeLine, rest, ok := strings.Cut(strings.TrimSpace(rest), "\n")
	if !ok || !strings.HasPrefix(strings.TrimSpace(typeLine), "TYPE:") {
		return e, fmt.Errorf("missing TYPE: line")
	}
	e.Type = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(typeLine), "TYPE:"))
	_, body, ok := strings.Cut(strings.TrimSpace(rest), "---")
	if !ok {
		return e, fmt.Errorf("missing --- separator")
	}
	e.Content = strings.TrimSpace(body)
	if e.Title == "" || e.Content == "" {
		return e, fmt.Errorf("empty title or content")
	}
	return e, nil
}
