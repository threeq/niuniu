package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// Archived-history retrieval: auto-compact archives the evicted messages as
// one JSON chunk per message under .niuniu-agent/history/, and the
// HistorySearch tool lets the agent pull exact details back on demand —
// the context carries only the compact state, the archive carries the rest.
// Keyword scoring (stdlib only) keeps the zero-dependency property.

const (
	archiveMaxText   = 4 << 10 // per-message chunk cap
	searchMaxBytes   = 400     // per-hit excerpt cap
	searchHitScanned = 4000    // max chunks scanned per search
)

// HistoryChunk is one archived message.
type HistoryChunk struct {
	At   int64  `json:"at"`
	Role string `json:"role"`
	Text string `json:"text"`
}

// SaveHistoryArchive appends the compacted-away messages to the archive dir
// (one file per message, named by timestamp+seq so reads stay ordered).
// Best-effort by design: the caller treats an error as non-fatal.
func SaveHistoryArchive(dir string, msgs []model.Message) error {
	if dir == "" || len(msgs) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	now := timestampForArchive()
	for i, m := range msgs {
		var b strings.Builder
		for _, blk := range m.Blocks {
			switch blk.Type {
			case model.BlockText:
				b.WriteString(blk.Text + "\n")
			case model.BlockToolResult:
				b.WriteString("[tool_result " + blk.ToolUseID + "] " + blk.Text + "\n")
			case model.BlockToolUse:
				b.WriteString("[tool_use " + blk.Name + "]\n")
			case model.BlockThinking:
				b.WriteString("[thinking]\n")
			}
			if b.Len() > archiveMaxText {
				break
			}
		}
		text := strings.TrimSpace(b.String())
		if text == "" {
			continue
		}
		if len(text) > archiveMaxText {
			text = text[:archiveMaxText]
		}
		chunk := HistoryChunk{At: now, Role: string(m.Role), Text: text}
		data, err := json.Marshal(chunk)
		if err != nil {
			return err
		}
		name := fmt.Sprintf("%d-%04d.json", now, i)
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// HistoryHit is one search result.
type HistoryHit struct {
	File string
	Role string
	Text string // excerpt
}

// SearchHistoryArchive scans the archive (oldest-first, capped) and ranks
// chunks by case-insensitive keyword hits. Empty query → nothing (a blanket
// dump would defeat the point of archiving).
func SearchHistoryArchive(dir, query string, topN int) ([]HistoryHit, error) {
	if topN <= 0 {
		topN = 5
	}
	terms := searchTerms(query)
	if dir == "" || len(terms) == 0 {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil // no archive yet — a clean miss, not an error
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	type scored struct {
		hit   HistoryHit
		score int
	}
	var hits []scored
	scanned := 0
	for _, e := range entries {
		if scanned >= searchHitScanned {
			break
		}
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		scanned++
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var ch HistoryChunk
		if err := json.Unmarshal(data, &ch); err != nil {
			continue
		}
		low := strings.ToLower(ch.Text)
		score := 0
		for _, t := range terms {
			score += strings.Count(low, t)
		}
		if score == 0 {
			continue
		}
		hits = append(hits, scored{hit: HistoryHit{File: e.Name(), Role: ch.Role, Text: excerpt(ch.Text, searchMaxBytes)}, score: score})
	}
	sort.Slice(hits, func(i, j int) bool { return hits[i].score > hits[j].score })
	if len(hits) > topN {
		hits = hits[:topN]
	}
	out := make([]HistoryHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, h.hit)
	}
	return out, nil
}

func searchTerms(q string) []string {
	fields := strings.FieldsFunc(strings.ToLower(q), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	})
	return fields
}

func excerpt(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// HistorySearch is the agent-facing tool over the archive.
type HistorySearch struct{ Dir string }

type historySearchInput struct {
	Query string `json:"query"`
	Top   int    `json:"top"`
}

func (HistorySearch) Def() model.ToolDef {
	return model.ToolDef{
		Name:        "HistorySearch",
		Description: "Searches the archived history of compacted-away conversation messages (keyword scoring). Use when details you remember existing earlier are no longer in the visible context — exact error texts, earlier tool outputs, superseded decisions.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","description":"keywords to look for"},"top":{"type":"integer","description":"max results (default 5)"}},"required":["query"]}`),
	}
}

func (h HistorySearch) Execute(_ context.Context, input json.RawMessage) (string, error) {
	var in historySearchInput
	if err := json.Unmarshal(input, &in); err != nil {
		return "", fmt.Errorf("parse input: %w", err)
	}
	hits, err := SearchHistoryArchive(h.Dir, in.Query, in.Top)
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "no archived history matches the query", nil
	}
	var b strings.Builder
	for _, hit := range hits {
		fmt.Fprintf(&b, "[%s] (%s) %s\n---\n", hit.File, hit.Role, hit.Text)
	}
	return b.String(), nil
}

func timestampForArchive() int64 { return time.Now().Unix() }
