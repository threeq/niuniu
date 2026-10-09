package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

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
			"State vs preference: save only facts that stay true across sessions. One-off session state " +
			"('skip tests this time', 'don't run lint today', 'I'm tired of X right now') must NOT be saved — " +
			"not even as type=user — unless the user explicitly frames it as standing ('from now on', 'always', '以后都这样'). " +
			"A durable preference must predict a FUTURE session, not just record today's mood. " +
			"Same-turn corrections: when the user corrects something already stored ('I don't like X anymore, " +
			"remembered', 'stop using Y'), do it in THIS turn, never via end-of-session reflect: (1) MemorySearch " +
			"the stale entry, (2) re-save its exact title with lifecycle=\"deprecated\" keeping the original content, " +
			"(3) save the corrected fact under a NEW title (a deprecated entry stops being recalled but stays on disk " +
			"for the record). " +
			"Keep content short and self-contained; it will be injected into future sessions as advisory context. " +
			"Lifecycle: omitting lifecycle/expires_at on an update KEEPS the entry's current state; pass lifecycle " +
			"explicitly to close an entry (done = completed/settled; cancelled = user withdrew it; expired = past " +
			"its expires_at; deprecated = superseded by a corrected entry — closed entries stay on disk but stop " +
			"being recalled) or reopen one (open).",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"title":{"type":"string","description":"Short unique name (the update key)"},` +
			`"type":{"type":"string","enum":["pattern","gotcha","decision","user","ref"],"description":"Entry kind"},` +
			`"domain":{"type":"string","enum":["decision","preference","environment","open_item","collaboration","other"],"description":"Life area: decision = settled project decision; preference = durable user preference (reusable across sessions — NOT one-off session state like 'skip tests this once'); environment = user's environment habits (OS, shell, tooling); open_item = in-progress matter worth revisiting, pair with expires_at when there is a deadline; collaboration = how the user wants to work together; other = anything else. Optional."},` +
			`"content":{"type":"string","description":"The lesson itself, one or two sentences"},` +
			`"tags":{"type":"array","items":{"type":"string"},"description":"Optional keywords for recall"},` +
			`"lifecycle":{"type":"string","enum":["open","done","cancelled","expired","deprecated"],"description":"Life-cycle state: open = live context (default for new entries); done = completed or settled; cancelled = withdrawn, no longer applies; expired = past expires_at; deprecated = superseded by a corrected entry. Closed states are kept for the record but excluded from recall. Optional."},` +
			`"expires_at":{"type":"string","description":"Optional deadline, RFC3339 (e.g. 2026-10-20T09:00:00Z). Past it the entry counts as expired and stops being recalled — use for time-bound open items (e.g. an interview tomorrow). Optional."}},` +
			`"required":["title","content"]}`),
	}
}

func (t SaveTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Title     string   `json:"title"`
		Type      string   `json:"type"`
		Domain    string   `json:"domain"`
		Content   string   `json:"content"`
		Tags      []string `json:"tags"`
		Lifecycle string   `json:"lifecycle"`
		ExpiresAt string   `json:"expires_at"`
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
	var expires time.Time
	if s := strings.TrimSpace(in.ExpiresAt); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return "", fmt.Errorf("expires_at must be RFC3339 (e.g. 2026-10-20T09:00:00Z): %v", err)
		}
		expires = t
	}
	existed := false
	if hits, _ := t.store.Search(in.Title); len(hits) > 0 {
		for _, h := range hits {
			if h.Title == in.Title {
				existed = true
			}
		}
	}
	id, err := t.store.Save(Entry{
		Title:     in.Title,
		Type:      in.Type,
		Domain:    in.Domain,
		Content:   in.Content,
		Tags:      in.Tags,
		Lifecycle: in.Lifecycle,
		ExpiresAt: expires,
	})
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
		Name: "MemorySearch",
		Description: "Search long-term memory by keywords (empty query lists everything). Returns entries with their ids, types, lifecycle, and contents, best matches first. " +
			"Lifecycle: open = live context; done = completed/settled; cancelled = withdrawn; expired = past its expires_at; deprecated = superseded by a corrected entry. " +
			"Closed entries are excluded from automatic recall AND from default results here — pass the lifecycle filter explicitly to find and update (or deprecate) stale memories.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{` +
			`"query":{"type":"string","description":"Keywords; empty lists all live entries"},` +
			`"lifecycle":{"type":"string","description":"Optional exact-match filter: open|done|cancelled|expired|deprecated. Explicit filter overrides the default closed-entry exclusion."},` +
			`"domain":{"type":"string","description":"Optional exact-match filter: decision|preference|environment|open_item|collaboration|other"}}}`),
	}
}

func (t SearchTool) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in struct {
		Query     string `json:"query"`
		Lifecycle string `json:"lifecycle"`
		Domain    string `json:"domain"`
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
	filtered := hits[:0:0]
	for _, e := range hits {
		// Default results skip closed lifecycles (mirrors the recall
		// filter); an explicit lifecycle query overrides so stale memories
		// stay findable for correction.
		if in.Lifecycle != "" {
			if e.Lifecycle != in.Lifecycle {
				continue
			}
		} else if lifecycleClosed(e.Lifecycle) {
			continue
		}
		if in.Domain != "" && e.Domain != in.Domain {
			continue
		}
		filtered = append(filtered, e)
	}
	if len(filtered) == 0 {
		return "(no matching memories)", nil
	}
	var b strings.Builder
	for _, e := range filtered {
		meta := fmt.Sprintf("id: %s, %s layer", e.ID, e.Layer)
		if e.Domain != "" {
			meta += ", domain: " + e.Domain
		}
		if e.Lifecycle != LifecycleOpen {
			meta += ", lifecycle: " + e.Lifecycle
		}
		if !e.ExpiresAt.IsZero() {
			meta += ", due: " + e.ExpiresAt.Format("2006-01-02")
		}
		fmt.Fprintf(&b, "[%s] %s (%s)\n  %s\n", e.Type, e.Title, meta, e.Content)
	}
	return strings.TrimRight(b.String(), "\n"), nil
}

// Section renders the recalled-memory system section; an empty body omits
// the section entirely. Memory is positioned as ADVISORY context — it may
// be stale, the model judges. The guidance paragraph is FIXED text (prompt
// cache: it must stay byte-identical within a session, so no dates, ids or
// per-recall content in it — see internal/prompt's stable-prefix rules);
// the advisory phrasing rule lives here rather than in Build so every
// recall-bearing entry path carries the same contract.
func Section(body string) string {
	if strings.TrimSpace(body) == "" {
		return ""
	}
	return `
# Memory

Lessons recalled from previous sessions (best matches first). This is ADVISORY context: it may be outdated or wrong — verify against reality before relying on it. When acting on a recalled preference or decision, cite it as a question to confirm, not an assertion — e.g. 之前记录你偏好X，这次沿用吗？; if the user denies it, stop applying it for the rest of the session (and consider saving the correction with MemorySave). Lines tagged [open] or [open, due YYYY-MM-DD] are live follow-ups worth revisiting; past-due ones are filtered out before injection. Search more with MemorySearch; persist durable lessons with MemorySave.

Saving rule — state vs preference: only durable, reusable facts belong here. One-off session state ("skip tests this once", "don't run lint today") is NOT a preference; never save it unless the user explicitly frames it as standing ("from now on", "always").

Same-turn corrections: when the user corrects something already stored ("I don't like X anymore", "stop using Y"), retire the stale entry and record the correction in THIS turn — (1) MemorySearch the stale entry, (2) re-save its exact title with lifecycle="deprecated" keeping the original content, (3) save the corrected fact under a new title. Don't defer to end-of-session reflect; deprecated entries stop being recalled.

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

Rules: only durable knowledge (a pitfall, a settled decision, a non-obvious pattern, a user preference, a useful reference). NOT task status, NOT conversation recap. State vs preference: session-scoped state ("skip tests this once", "don't run lint today") is NOT a preference — never save it unless the user explicitly frames it as standing ("from now on", "always"); a durable preference must predict future sessions, not just record today's mood. Corrections already applied by the agent mid-session (stale entry deprecated, corrected entry saved) need no second entry — do not re-extract them. If unsure, output NONE.`

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
