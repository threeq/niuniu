package agentproxy

import (
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/agentbackend"
)

// 连续 text delta 聚合成一条完整 EventText。
func TestStreamAggAccumulatesTextDeltas(t *testing.T) {
	var a niuniuStreamAgg
	a.feedText("数据")
	a.feedText("流")
	a.feedText("聚合")
	if a.empty() {
		t.Fatal("must not be empty after feeds")
	}
	out := a.drain()
	if len(out) != 1 || out[0].Type != agentbackend.EventText {
		t.Fatalf("out = %+v", out)
	}
	if out[0].Text != "数据流聚合" {
		t.Errorf("text = %q", out[0].Text)
	}
	if !a.empty() {
		t.Error("drain must reset")
	}
}

// thinking 与 text 交替：drain 先 thinking 后 text，各自整条。
func TestStreamAggThinkingThenText(t *testing.T) {
	var a niuniuStreamAgg
	a.feedThinking("plan: ")
	a.feedThinking("check data")
	a.feedText("uni")
	a.feedText("数据")
	out := a.drain()
	if len(out) != 2 {
		t.Fatalf("out = %+v, want [thinking, text]", out)
	}
	if out[0].Type != agentbackend.EventThinking || out[0].Thinking != "plan: check data" {
		t.Errorf("thinking = %+v", out[0])
	}
	if out[1].Type != agentbackend.EventText || out[1].Text != "uni数据" {
		t.Errorf("text = %+v", out[1])
	}
	// 二次 drain 为空（类型切换场景重复 flush 安全）。
	if again := a.drain(); len(again) != 0 {
		t.Errorf("second drain = %+v", again)
	}
}

// 空 flush 安全（工具事件时无未积累内容）。
func TestStreamAggEmptyDrain(t *testing.T) {
	var a niuniuStreamAgg
	if !a.empty() {
		t.Fatal("fresh agg must be empty")
	}
	if out := a.drain(); len(out) != 0 {
		t.Errorf("out = %+v", out)
	}
}

// 完整回合时序（时序敏感的聚合指令流）：thinking 片 → text 片 → 工具 →
// text 片 → done。模拟 handleNiuniuEvent 的 flush 节奏，验证落库粒度。
func TestStreamAggTurnTimeline(t *testing.T) {
	var a niuniuStreamAgg
	var persisted []string
	flush := func() {
		for _, ev := range a.drain() {
			switch ev.Type {
			case agentbackend.EventThinking:
				persisted = append(persisted, "think:"+ev.Thinking)
			case agentbackend.EventText:
				persisted = append(persisted, "text:"+ev.Text)
			}
		}
	}
	// thinking 流式
	a.feedThinking("should"); a.feedThinking(" check")
	flush() // tool_use 前置 flush
	// text 流式
	a.feedText("uni"); a.feedText("数据"); a.feedText("事务")
	flush() // done 前置 flush
	joined := strings.Join(persisted, "|")
	want := "think:should check|text:uni数据事务"
	if joined != want {
		t.Errorf("persisted = %q, want %q", joined, want)
	}
	// 两条整消息（而不是 5+ 条碎片）。
	if len(persisted) != 2 {
		t.Errorf("%d persisted rows, want 2 whole messages", len(persisted))
	}
}
