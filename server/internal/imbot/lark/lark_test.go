package lark

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/imbot"
)

func TestPush_SendsTextWithToken(t *testing.T) {
	var tokenHits, sendHits atomic.Int64
	var gotAuth, gotReceiveID, gotText string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal"):
			tokenHits.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":0,"msg":"ok","tenant_access_token":"t-abc","expire":7200}`)
		case strings.HasSuffix(r.URL.Path, "/im/v1/messages"):
			sendHits.Add(1)
			gotAuth = r.Header.Get("Authorization")
			if q := r.URL.Query().Get("receive_id_type"); q != "chat_id" {
				t.Errorf("receive_id_type=%q, want chat_id", q)
			}
			body, _ := io.ReadAll(r.Body)
			var msg struct {
				ReceiveID string `json:"receive_id"`
				MsgType   string `json:"msg_type"`
				Content   string `json:"content"`
			}
			_ = json.Unmarshal(body, &msg)
			gotReceiveID = msg.ReceiveID
			// Plain messages now go as an interactive card so markdown renders
			// (Feishu's text type shows markdown source verbatim).
			if msg.MsgType != "interactive" {
				t.Errorf("msg_type=%q, want interactive", msg.MsgType)
			}
			// The card carries the text inside a `markdown` element.
			var card struct {
				Elements []struct {
					Tag     string `json:"tag"`
					Content string `json:"content"`
				} `json:"elements"`
			}
			_ = json.Unmarshal([]byte(msg.Content), &card)
			for _, el := range card.Elements {
				if el.Tag == "markdown" {
					gotText = el.Content
				}
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":0,"msg":"ok"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	a := New()
	cred := imbot.Credential{Channel: imbot.ChannelLark, Config: map[string]any{
		"app_id":     "cli_x",
		"app_secret": "sec",
		"base_url":   srv.URL,
	}}

	err := a.Push(context.Background(), cred, imbot.OutboundMessage{ChatExtID: "oc_1", Text: "done"})
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if gotAuth != "Bearer t-abc" {
		t.Errorf("Authorization=%q, want Bearer t-abc", gotAuth)
	}
	if gotReceiveID != "oc_1" {
		t.Errorf("receive_id=%q, want oc_1", gotReceiveID)
	}
	if gotText != "done" {
		t.Errorf("text=%q, want done", gotText)
	}

	// Second push reuses the cached token (no extra token fetch).
	if err := a.Push(context.Background(), cred, imbot.OutboundMessage{ChatExtID: "oc_1", Text: "again"}); err != nil {
		t.Fatalf("Push #2: %v", err)
	}
	if tokenHits.Load() != 1 {
		t.Errorf("token fetched %d times, want 1 (cached)", tokenHits.Load())
	}
	if sendHits.Load() != 2 {
		t.Errorf("send hits=%d, want 2", sendHits.Load())
	}
}

func TestPush_ButtonsSendInteractiveCard(t *testing.T) {
	var gotType, gotContent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"t","expire":7200}`)
			return
		}
		body, _ := io.ReadAll(r.Body)
		var msg struct {
			MsgType string `json:"msg_type"`
			Content string `json:"content"`
		}
		_ = json.Unmarshal(body, &msg)
		gotType = msg.MsgType
		gotContent = msg.Content
		_, _ = io.WriteString(w, `{"code":0}`)
	}))
	defer srv.Close()

	a := New()
	cred := imbot.Credential{Config: map[string]any{"app_id": "a", "app_secret": "b", "base_url": srv.URL}}
	err := a.Push(context.Background(), cred, imbot.OutboundMessage{
		ChatExtID: "oc",
		Text:      "Delete temp files?",
		Buttons:   []imbot.Button{{Label: "Allow", Value: "permission:approve:9"}, {Label: "Deny", Value: "permission:deny:9"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Buttons now send an interactive card whose buttons carry the callback
	// payload under value.cb (so a click round-trips back as an action_callback).
	if gotType != "interactive" {
		t.Fatalf("expected interactive msg_type, got %q", gotType)
	}
	if !strings.Contains(gotContent, "permission:approve:9") || !strings.Contains(gotContent, "permission:deny:9") {
		t.Errorf("card missing callback values: %s", gotContent)
	}
}

func TestPush_ErrorOnMissingCred(t *testing.T) {
	a := New()
	err := a.Push(context.Background(), imbot.Credential{Config: map[string]any{}}, imbot.OutboundMessage{ChatExtID: "oc"})
	if err == nil {
		t.Fatal("expected error on missing app_id/app_secret")
	}
}

func TestReact_AddsNiuReactionToMessage(t *testing.T) {
	var gotPath, gotAuth, gotEmoji string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"t-abc","expire":7200}`)
			return
		}
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		var req struct {
			ReactionType struct {
				EmojiType string `json:"emoji_type"`
			} `json:"reaction_type"`
		}
		_ = json.Unmarshal(body, &req)
		gotEmoji = req.ReactionType.EmojiType
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok","data":{"reaction_id":"rid-9"}}`)
	}))
	defer srv.Close()

	a := New()
	cred := imbot.Credential{Channel: imbot.ChannelLark, Config: map[string]any{
		"app_id": "cli_x", "app_secret": "sec", "base_url": srv.URL,
	}}

	rid, err := a.React(context.Background(), cred, "om_1", imbot.ReactionProcessing)
	if err != nil {
		t.Fatalf("React: %v", err)
	}
	if rid != "rid-9" {
		t.Errorf("reaction_id = %q, want rid-9", rid)
	}
	if gotPath != "/open-apis/im/v1/messages/om_1/reactions" {
		t.Errorf("path = %q, want .../messages/om_1/reactions", gotPath)
	}
	if gotAuth != "Bearer t-abc" {
		t.Errorf("Authorization = %q, want Bearer t-abc", gotAuth)
	}
	if gotEmoji != "AWESOMEN" {
		t.Errorf("emoji_type = %q, want AWESOMEN (牛)", gotEmoji)
	}
}

func TestRemoveReaction_DeletesById(t *testing.T) {
	var gotMethod, gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"t-abc","expire":7200}`)
			return
		}
		gotMethod, gotPath, gotAuth = r.Method, r.URL.Path, r.Header.Get("Authorization")
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok"}`)
	}))
	defer srv.Close()

	a := New()
	cred := imbot.Credential{Config: map[string]any{"app_id": "a", "app_secret": "b", "base_url": srv.URL}}
	if err := a.RemoveReaction(context.Background(), cred, "om_1", "rid-9"); err != nil {
		t.Fatalf("RemoveReaction: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("method = %q, want DELETE", gotMethod)
	}
	if gotPath != "/open-apis/im/v1/messages/om_1/reactions/rid-9" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer t-abc" {
		t.Errorf("auth = %q", gotAuth)
	}
}

func TestReact_TreatsAlreadyExistsAsSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"t","expire":7200}`)
			return
		}
		// 230026 = reaction already exists (idempotent under event redelivery).
		_, _ = io.WriteString(w, `{"code":230026,"msg":"reaction already exists"}`)
	}))
	defer srv.Close()

	a := New()
	cred := imbot.Credential{Config: map[string]any{"app_id": "a", "app_secret": "b", "base_url": srv.URL}}
	if _, err := a.React(context.Background(), cred, "om_1", imbot.ReactionProcessing); err != nil {
		t.Fatalf("already-exists should be success, got %v", err)
	}
}

func TestReply_AnchorsTextToMessage(t *testing.T) {
	var gotPath, gotType, gotText string
	var replyInThread bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"t","expire":7200}`)
			return
		}
		gotPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Content       string `json:"content"`
			MsgType       string `json:"msg_type"`
			ReplyInThread bool   `json:"reply_in_thread"`
		}
		_ = json.Unmarshal(body, &req)
		gotType = req.MsgType
		replyInThread = req.ReplyInThread
		var c struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal([]byte(req.Content), &c)
		gotText = c.Text
		_, _ = io.WriteString(w, `{"code":0,"msg":"ok"}`)
	}))
	defer srv.Close()

	a := New()
	cred := imbot.Credential{Config: map[string]any{"app_id": "a", "app_secret": "b", "base_url": srv.URL}}
	if err := a.Reply(context.Background(), cred, "om_1", "#506 分析牛牛优势"); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if gotPath != "/open-apis/im/v1/messages/om_1/reply" {
		t.Errorf("path = %q", gotPath)
	}
	if gotType != "text" || replyInThread {
		t.Errorf("msg_type=%q reply_in_thread=%v, want text/false", gotType, replyInThread)
	}
	if gotText != "#506 分析牛牛优势" {
		t.Errorf("reply text = %q", gotText)
	}
}

func TestFetchResource_DownloadsImageBytes(t *testing.T) {
	var gotPath, gotAuth, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"t-abc","expire":7200}`)
			return
		}
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotType = r.URL.Query().Get("type")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("\x89PNG\r\n\x1a\nBINARY"))
	}))
	defer srv.Close()

	a := New()
	cred := imbot.Credential{Config: map[string]any{"app_id": "a", "app_secret": "b", "base_url": srv.URL}}
	data, err := a.FetchResource(context.Background(), cred, "om_1",
		imbot.InboundAttachment{Kind: "image", ResourceID: "img_v2_k"})
	if err != nil {
		t.Fatalf("FetchResource: %v", err)
	}
	if string(data) != "\x89PNG\r\n\x1a\nBINARY" {
		t.Errorf("bytes mismatch: %q", data)
	}
	if gotPath != "/open-apis/im/v1/messages/om_1/resources/img_v2_k" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer t-abc" {
		t.Errorf("auth = %q", gotAuth)
	}
	if gotType != "image" {
		t.Errorf("type = %q, want image", gotType)
	}
}

func TestFetchResource_FileUsesFileType(t *testing.T) {
	var gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/tenant_access_token/internal") {
			_, _ = io.WriteString(w, `{"code":0,"tenant_access_token":"t","expire":7200}`)
			return
		}
		gotType = r.URL.Query().Get("type")
		_, _ = w.Write([]byte("PDF"))
	}))
	defer srv.Close()

	a := New()
	cred := imbot.Credential{Config: map[string]any{"app_id": "a", "app_secret": "b", "base_url": srv.URL}}
	if _, err := a.FetchResource(context.Background(), cred, "om_1",
		imbot.InboundAttachment{Kind: "file", ResourceID: "file_v2_k", Name: "a.pdf"}); err != nil {
		t.Fatalf("FetchResource: %v", err)
	}
	if gotType != "file" {
		t.Errorf("type = %q, want file", gotType)
	}
}

func TestReact_UnsupportedReactionIsNoOp(t *testing.T) {
	// An unknown reaction must not hit the network (no valid emoji_type to send).
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatalf("React must not call the API for an unsupported reaction")
	}))
	defer srv.Close()

	a := New()
	cred := imbot.Credential{Config: map[string]any{"app_id": "a", "app_secret": "b", "base_url": srv.URL}}
	if _, err := a.React(context.Background(), cred, "om_1", imbot.Reaction("nope")); err != nil {
		t.Fatalf("unsupported reaction should no-op, got %v", err)
	}
}
