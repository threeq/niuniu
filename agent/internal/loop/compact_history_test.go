package loop

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// 压缩归档：compact 把被移除的消息存进 HistoryDir，摘要消息带
// HistorySearch 指针；归档内容可被检索命中。
func TestCompactArchivesEvictedHistory(t *testing.T) {
	stateJSON := `{"goal":"g","key_decisions":["k"],"open_items":["o"]}`
	fm := &fakeModel{script: []*model.Response{
		usageResp([]model.Block{{Type: model.BlockText, Text: "first answer mentions E42 disk full error"}}, model.StopEndTurn, 100),
		usageResp([]model.Block{{Type: model.BlockToolUse, ID: "tu_2", Name: "Echo", Input: json.RawMessage(`{}`)}}, model.StopToolUse, 500_000),
		usageResp([]model.Block{{Type: model.BlockText, Text: stateJSON}}, model.StopEndTurn, 50),
		usageResp([]model.Block{{Type: model.BlockText, Text: "final answer"}}, model.StopEndTurn, 300),
	}}
	reg := tools.NewRegistry(&stubTool{})
	s := NewSession(fm, reg, "sys")
	base := t.TempDir()
	historyDir := filepath.Join(base, ".niuniu-agent", "history")

	opts := Options{CompactThresholdTokens: 100_000, KeepRecentMessages: 2, HistoryDir: historyDir}
	if _, err := s.Prompt(context.Background(), "q1", opts); err != nil {
		t.Fatalf("prompt1: %v", err)
	}
	if _, err := s.Prompt(context.Background(), "q2", opts); err != nil {
		t.Fatalf("prompt2: %v", err)
	}

	// 归档文件已生成且包含被压缩内容。
	entries, err := os.ReadDir(historyDir)
	if err != nil || len(entries) == 0 {
		t.Fatalf("no archive (err=%v, n=%d)", err, len(entries))
	}
	hits, _ := tools.SearchHistoryArchive(historyDir, "E42 disk full", 5)
	if len(hits) == 0 {
		t.Fatal("archived history not searchable for compacted content")
	}

	// 摘要消息带 HistorySearch 指针。
	sum := fm.reqs[3].Messages[0].Blocks[0].Text
	if !strings.Contains(sum, "HistorySearch") {
		t.Errorf("summary message missing retrieval pointer: %q", sum)
	}
}
