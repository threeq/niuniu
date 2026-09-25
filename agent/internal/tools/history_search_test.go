package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
)

// 归档：每条被压缩消息一个 JSON 块；检索：关键词命中并排序。
func TestSaveAndSearchHistoryArchive(t *testing.T) {
	dir := t.TempDir()
	msgs := []model.Message{
		{Role: model.RoleUser, Blocks: []model.Block{{Type: model.BlockText, Text: "帮我修复 login 页面的 JWT 过期崩溃"}}},
		{Role: model.RoleAssistant, Blocks: []model.Block{{Type: model.BlockToolUse, ID: "t1", Name: "Edit"}, {Type: model.BlockText, Text: "已修改 auth.go：改用 refresh token 续期"}}},
		{Role: model.RoleUser, Blocks: []model.Block{{Type: model.BlockToolResult, ToolUseID: "t1", Text: "ok"}}},
	}
	if err := SaveHistoryArchive(dir, msgs); err != nil {
		t.Fatalf("save: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 3 {
		t.Fatalf("archived %d chunks, want 3", len(entries))
	}

	// 命中关键词（跨中英文大小写）。
	hits, err := SearchHistoryArchive(dir, "refresh token 续期", 5)
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(hits) == 0 {
		t.Fatal("no hits")
	}
	if !strings.Contains(hits[0].Text, "auth.go") {
		t.Errorf("top hit = %q", hits[0].Text)
	}

	// 无关关键词 → 零命中（不报错）。
	if hits, _ := SearchHistoryArchive(dir, "kubernetes 部署", 5); len(hits) != 0 {
		t.Errorf("unrelated query returned %d hits", len(hits))
	}
	// 空目录 → 干净的零命中。
	if hits, _ := SearchHistoryArchive(filepath.Join(dir, "none"), "x", 5); len(hits) != 0 {
		t.Error("missing dir must be a clean miss")
	}
}

// 超长消息截断到 chunk 上限。
func TestSaveHistoryArchiveCapsText(t *testing.T) {
	dir := t.TempDir()
	big := strings.Repeat("x", 10<<10)
	if err := SaveHistoryArchive(dir, []model.Message{{Role: model.RoleUser, Blocks: []model.Block{{Type: model.BlockText, Text: big}}}}); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	data, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if len(data) > (4<<10)+512 {
		t.Errorf("chunk not capped: %d bytes", len(data))
	}
}

// 工具冒烟：HistorySearch.Execute 走通查询路径。
func TestHistorySearchToolExecute(t *testing.T) {
	dir := t.TempDir()
	if err := SaveHistoryArchive(dir, []model.Message{{Role: model.RoleAssistant, Blocks: []model.Block{{Type: model.BlockText, Text: "error code E42 means disk full"}}}}); err != nil {
		t.Fatal(err)
	}
	h := HistorySearch{Dir: dir}
	out, err := h.Execute(context.Background(), json.RawMessage(`{"query":"E42 disk"}`))
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(out, "E42") {
		t.Errorf("output = %q", out)
	}
	// 空归档：干净的 no-match 文案。
	empty := HistorySearch{Dir: t.TempDir()}
	out2, err := empty.Execute(context.Background(), json.RawMessage(`{"query":"anything"}`))
	if err != nil || !strings.Contains(out2, "no archived history") {
		t.Errorf("empty archive: %q, %v", out2, err)
	}
}
