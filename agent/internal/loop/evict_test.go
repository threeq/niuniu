package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

// bigResultTool 返回 kb KB 的结果文本，驱动逐出阈值。
type bigResultTool struct{ kb int }

func (b bigResultTool) Def() model.ToolDef {
	return model.ToolDef{Name: "Big", Description: "big", InputSchema: json.RawMessage(`{"type":"object"}`)}
}
func (b bigResultTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return strings.Repeat("x", b.kb<<10), nil
}

// runBigRounds 跑 rounds 轮「Big 工具大结果」，最后一轮纯文本收尾。
// 返回模型请求历史（fakeModel 捕获）与会话最终历史。
func runBigRounds(t *testing.T, rounds int, opts Options) (*fakeModel, []model.Message) {
	t.Helper()
	fm := &fakeModel{}
	for i := 0; i < rounds; i++ {
		fm.script = append(fm.script, &model.Response{
			Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
				{Type: model.BlockToolUse, ID: fmt.Sprintf("tu_%d", i), Name: "Big", Input: json.RawMessage(`{}`)}},
			},
			StopReason: model.StopToolUse,
		})
	}
	fm.script = append(fm.script, textResp("final"))
	reg := tools.NewRegistry(bigResultTool{kb: 8})
	sess := NewSession(fm, reg, "sys")
	if _, err := sess.Prompt(context.Background(), "go", opts); err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	return fm, sess.messages
}

// collectResults 按出现顺序收集 (全局序号, 文本)。
func collectResults(msgs []model.Message) []struct {
	idx  int
	text string
} {
	var out []struct {
		idx  int
		text string
	}
	n := 0
	for _, m := range msgs {
		for _, blk := range m.Blocks {
			if blk.Type == model.BlockToolResult {
				out = append(out, struct {
					idx  int
					text string
				}{n, blk.Text})
				n++
			}
		}
	}
	return out
}

func TestEvictOldToolResults(t *testing.T) {
	const rounds, keep = 10, 4
	_, msgs := runBigRounds(t, rounds, Options{EvictToolResults: keep, EvictKeepBytes: 512})

	results := collectResults(msgs)
	if len(results) != rounds {
		t.Fatalf("results = %d, want %d", len(results), rounds)
	}
	// 保留最近 keep 个原样（8KB）；更早的截断到 512B 且带 one-shot 标记。
	for i, r := range results {
		evicted := strings.Contains(r.text, "[result evicted")
		isOld := i < len(results)-keep
		switch {
		case isOld && !evicted:
			t.Errorf("old result #%d not evicted (len %d)", i, len(r.text))
		case !isOld && evicted:
			t.Errorf("recent result #%d was evicted", i)
		case isOld && !strings.HasSuffix(r.text, evictMark):
			t.Errorf("old result #%d not truncated (len %d)", i, len(r.text))
		case !isOld && len(r.text) < 8<<10:
			t.Errorf("recent result #%d truncated (%d bytes)", i, len(r.text))
		}
	}
}

// one-shot：再次 Prompt（新轮次把 keep 窗口前推）只逐出新越过窗口的
// 结果；已带标记的不再二次改写——旧前缀保持稳定。
func TestEvictIsOneShot(t *testing.T) {
	fm := &fakeModel{}
	for i := 0; i < 8; i++ {
		fm.script = append(fm.script, &model.Response{
			Message: model.Message{Role: model.RoleAssistant, Blocks: []model.Block{
				{Type: model.BlockToolUse, ID: fmt.Sprintf("tu_%d", i), Name: "Big", Input: json.RawMessage(`{}`)}},
			},
			StopReason: model.StopToolUse,
		})
	}
	fm.script = append(fm.script, textResp("mid"))
	fm.script = append(fm.script, textResp("final"))

	reg := tools.NewRegistry(bigResultTool{kb: 8})
	sess := NewSession(fm, reg, "sys")
	opts := Options{EvictToolResults: 2, EvictKeepBytes: 256}
	if _, err := sess.Prompt(context.Background(), "go", opts); err != nil {
		t.Fatal(err)
	}
	afterFirst := collectResults(sess.messages)
	markedFirst := 0
	for _, r := range afterFirst {
		if strings.Contains(r.text, "[result evicted") {
			markedFirst++
		}
	}
	// 续一轮纯文本（无新工具结果）——逐出最多把窗口外结果补齐标记，
	// 且已标记文本逐字节保持（one-shot：前缀不再变动）。
	sess.Prompt(context.Background(), "again", opts)
	results := collectResults(sess.messages)
	markedSecond := 0
	for _, r := range results {
		if strings.Contains(r.text, "[result evicted") {
			markedSecond++
		}
	}
	if markedSecond < markedFirst {
		t.Errorf("eviction markers regressed: %d → %d", markedFirst, markedSecond)
	}
	survived := map[string]bool{}
	for _, r := range afterFirst {
		if strings.Contains(r.text, "[result evicted") {
			survived[r.text] = true
		}
	}
	for _, r := range results {
		if !strings.Contains(r.text, "[result evicted") {
			continue
		}
		if !survived[r.text] && len(r.text) > 512+64 {
			// 新逐出允许；既有的必须原样。
			if len(r.text) < 8<<10 && !survived[r.text] {
				t.Errorf("previously evicted text was rewritten")
			}
		}
	}
}

// 模型可见性：逐出后的截断文本出现在下一轮请求历史里。
func TestEvictVisibleToModel(t *testing.T) {
	fm, _ := runBigRounds(t, 8, Options{EvictToolResults: 2, EvictKeepBytes: 512})
	last := fm.reqs[len(fm.reqs)-1]
	found := 0
	for _, m := range last.Messages {
		for _, blk := range m.Blocks {
			if blk.Type == model.BlockToolResult && strings.Contains(blk.Text, "[result evicted") {
				found++
			}
		}
	}
	if found == 0 {
		t.Error("model request does not show evicted results")
	}
}

// 默认关闭：未开启逐出时历史保持原样。
func TestEvictDisabledByDefault(t *testing.T) {
	_, msgs := runBigRounds(t, 6, Options{})
	for _, r := range collectResults(msgs) {
		if strings.Contains(r.text, "[result evicted") {
			t.Fatal("eviction must be opt-in")
		}
		if len(r.text) < 8<<10 {
			t.Fatalf("result truncated without eviction enabled")
		}
	}
}
