package agentproxy

import (
	"context"
	"strings"

	"github.com/niuniu-dev/niuniu/internal/agentbackend"
)

// niuniu 引擎的 text/thinking 事件是 chunk 级 delta：agent 端流式开启时，
// ACP 每收到一个增量片就发一条 agent_message_chunk / agent_thought_chunk，
// 服务端逐片映射为 Event。广播必须逐片（前端实时流式渲染依赖它），但
// 持久化必须聚合——逐片落库会让历史消息退化为逐 token 碎片。
//
// niuniuStreamAgg 累积连续 delta；在类型切换（text↔thinking）、工具事件、
// 回合结束（done/error）时由调用方 drain 成整条 Event 落库。drain 出的
// 事件只 persist 不 broadcast（流式期间的逐片广播已经覆盖了实时展示，
// flush 再广播会造成前端内容重复）。
type niuniuStreamAgg struct {
	text    strings.Builder
	think   strings.Builder
	hasText bool
	hasThink bool
}

func (a *niuniuStreamAgg) feedText(delta string)    { a.text.WriteString(delta); a.hasText = true }
func (a *niuniuStreamAgg) feedThinking(delta string) { a.think.WriteString(delta); a.hasThink = true }

func (a *niuniuStreamAgg) empty() bool { return !a.hasText && !a.hasThink }

// drain returns the accumulated thinking block (first) and text block
// (second) as whole-message events, resetting the buffers.
func (a *niuniuStreamAgg) drain() []agentbackend.Event {
	var out []agentbackend.Event
	if a.hasThink {
		out = append(out, agentbackend.Event{Type: agentbackend.EventThinking, Thinking: a.think.String()})
		a.think.Reset()
		a.hasThink = false
	}
	if a.hasText {
		out = append(out, agentbackend.Event{Type: agentbackend.EventText, Text: a.text.String()})
		a.text.Reset()
		a.hasText = false
	}
	return out
}

// handleNiuniuEvent is the niuniu-engine wrapper around the shared
// handleGooseEvent mapping: chunk deltas are broadcast immediately but
// persisted only in aggregated whole-message form.
func (s *WorkspaceSession) handleNiuniuEvent(ctx context.Context, ev agentbackend.Event, msgId string) {
	switch ev.Type {
	case agentbackend.EventText:
		s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventText, ev.Text, msgId, "assistant", s.workspaceID))
		s.niuniuAgg.feedText(ev.Text)
	case agentbackend.EventThinking:
		s.hub.Broadcast(s.workspaceID, NewOutputEvent(EventThinking, ev.Thinking, msgId, "assistant", s.workspaceID))
		s.niuniuAgg.feedThinking(ev.Thinking)
	case agentbackend.EventToolUse, agentbackend.EventToolResult:
		// 工具事件前先落已积累的文本块，保持历史里 thinking/text 在工具调用之前。
		s.flushNiuniuStream(ctx, msgId)
		s.handleGooseEvent(ctx, ev, msgId)
	case agentbackend.EventDone, agentbackend.EventError:
		s.flushNiuniuStream(ctx, msgId)
		s.handleGooseEvent(ctx, ev, msgId)
	default:
		s.handleGooseEvent(ctx, ev, msgId)
	}
}

// flushNiuniuStream persists the accumulated whole-message blocks (thinking
// first, then text) WITHOUT broadcasting — the per-delta broadcasts during
// streaming already covered live display.
func (s *WorkspaceSession) flushNiuniuStream(ctx context.Context, msgId string) {
	for _, ev := range s.niuniuAgg.drain() {
		switch ev.Type {
		case agentbackend.EventThinking:
			s.persistEvent(ctx, NewOutputEvent(EventThinking, ev.Thinking, msgId, "assistant", s.workspaceID), 0)
		case agentbackend.EventText:
			s.persistEvent(ctx, NewOutputEvent(EventText, ev.Text, msgId, "assistant", s.workspaceID), 0)
		}
	}
}
