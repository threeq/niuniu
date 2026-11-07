package opsbot

import (
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/store"
)

func TestRenderApprovalCardPublish(t *testing.T) {
	payload, err := RenderApprovalCard(ApprovalCard{
		ID:   7,
		Type: ApprovalPublish,
		Publish: &PublishApprovalCard{
			Title: "秋季上新", Summary: "九宫格", Cover: "cover.jpg",
			Channel: "小红书", PublishAt: "2026-11-08 10:00", Compliance: "通过",
		},
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	for _, want := range []string{"发布审批 #7", "秋季上新", "九宫格", "小红书", "2026-11-08 10:00", "通过"} {
		if !strings.Contains(payload.Text, want) {
			t.Fatalf("card text %q missing %q", payload.Text, want)
		}
	}
	if len(payload.Buttons) != 3 {
		t.Fatalf("buttons = %d, want 3", len(payload.Buttons))
	}
	if got := payload.Buttons[0].Value; got != "opsapproval:approve:7" {
		t.Fatalf("approve button value = %q", got)
	}
}

func TestRenderApprovalCardReplyAndVideoHaveTypedActions(t *testing.T) {
	reply, err := RenderApprovalCard(ApprovalCard{
		ID: 2, Type: ApprovalReply,
		Reply: &ReplyApprovalCard{Comment: "多少钱", Category: "咨询", SuggestedReply: "私信", Risk: "低"},
	})
	if err != nil {
		t.Fatalf("render reply: %v", err)
	}
	for _, want := range []string{"回复审批 #2", "多少钱", "咨询", "私信", "低"} {
		if !strings.Contains(reply.Text, want) {
			t.Fatalf("reply card %q missing %q", reply.Text, want)
		}
	}
	if reply.Buttons[1].Label != "修改后发送" {
		t.Fatalf("reply modify label = %q", reply.Buttons[1].Label)
	}

	video, err := RenderApprovalCard(ApprovalCard{
		ID: 3, Type: ApprovalVideo,
		Video: &VideoApprovalCard{Preview: "v.mp4", Duration: "30s", Subtitles: "已烧录", Cover: "c.jpg", Size: "1080x1920"},
	})
	if err != nil {
		t.Fatalf("render video: %v", err)
	}
	for _, want := range []string{"视频成片审批 #3", "v.mp4", "30s", "已烧录", "1080x1920"} {
		if !strings.Contains(video.Text, want) {
			t.Fatalf("video card %q missing %q", video.Text, want)
		}
	}
	if video.Buttons[2].Value != "opsapproval:regenerate:3" {
		t.Fatalf("video regenerate value = %q", video.Buttons[2].Value)
	}
}

func TestRenderRiskAlert(t *testing.T) {
	got := RenderRiskAlert(RiskAlert{Account: "小红书 A", Reason: "连续发布失败", Paused: true})
	for _, want := range []string{"风险告警", "小红书 A", "连续发布失败", "已自动暂停"} {
		if !strings.Contains(got, want) {
			t.Fatalf("risk alert %q missing %q", got, want)
		}
	}
}

func TestParseApprovalCallback(t *testing.T) {
	action, id, ok := ParseApprovalCallback("opsapproval:reject:42")
	if !ok || action != ActionRejected || id != 42 {
		t.Fatalf("parse = (%q,%d,%v)", action, id, ok)
	}
	if _, _, ok := ParseApprovalCallback("permission:approve:1"); ok {
		t.Fatal("permission callback must not parse as ops approval")
	}
	if _, _, ok := ParseApprovalCallback("opsapproval:bogus:1"); ok {
		t.Fatal("unknown action must not parse")
	}
}

func TestApproverForPrefersPolicyOperator(t *testing.T) {
	bot := store.OpsBot{ApprovalPolicyJson: `{"publish":true,"operator_id":9}`}
	if got := ApproverFor(bot, 1); got != 9 {
		t.Fatalf("approver = %d, want configured operator 9", got)
	}
	if got := ApproverFor(store.OpsBot{}, 1); got != 1 {
		t.Fatalf("fallback approver = %d, want owner 1", got)
	}
}
