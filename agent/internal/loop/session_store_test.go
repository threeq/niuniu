package loop

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/agent/internal/model"
	"github.com/niuniu-dev/niuniu/agent/internal/tools"
)

func TestSessionSaveAndResume(t *testing.T) {
	dir := t.TempDir()
	fm := &fakeModel{script: []*model.Response{
		textRespUsage("first answer", 10, 2),
		textRespUsage("second answer mentions first", 20, 4),
	}}
	reg := tools.NewRegistry(&stubTool{})

	// 会话 1：跑一轮并持久化。
	s1 := NewSession(fm, reg, "sys")
	if _, err := s1.Prompt(context.Background(), "question one", Options{}); err != nil {
		t.Fatal(err)
	}
	st := s1.ExportState("sess-1")
	if err := SaveSession(dir, st); err != nil {
		t.Fatalf("SaveSession: %v", err)
	}
	if err := SaveSession(dir, s1.ExportState("sess-1")); err != nil {
		t.Errorf("re-save must overwrite: %v", err)
	}

	// 会话 2：恢复并续跑——第二轮请求必须带第一轮的历史。
	loaded, err := LoadSession(dir, "sess-1")
	if err != nil {
		t.Fatalf("LoadSession: %v", err)
	}
	s2 := RestoreSession(fm, reg, loaded)
	if _, err := s2.Prompt(context.Background(), "question two", Options{}); err != nil {
		t.Fatal(err)
	}
	req := fm.reqs[1]
	if len(req.Messages) != 3 { // q1, a1, q2
		t.Fatalf("resumed messages = %d, want 3", len(req.Messages))
	}
	if req.Messages[0].Text() != "question one" {
		t.Errorf("history lost: %q", req.Messages[0].Text())
	}

	// latest 定位。
	id, err := LatestSessionID(dir)
	if err != nil || id != "sess-1" {
		t.Errorf("LatestSessionID = %q, %v", id, err)
	}
	// 未知的 id 报错。
	if _, err := LoadSession(dir, "nope"); err == nil {
		t.Error("want error for unknown session id")
	}
}
