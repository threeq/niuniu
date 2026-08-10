package connection

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type sseEvent struct {
	Type        string `json:"type"`
	Content     string `json:"content"`
	WorkspaceId int64  `json:"workspaceId"`
}

type EventCallback func(eventType, content string, workspaceId int64)

type SSEListener struct {
	url      string
	callback EventCallback
	cancel   context.CancelFunc
	ctx      context.Context
}

func NewSSEListener(url string, callback EventCallback) *SSEListener {
	ctx, cancel := context.WithCancel(context.Background())
	return &SSEListener{url: url, callback: callback, ctx: ctx, cancel: cancel}
}

func (l *SSEListener) Start() { go l.run() }

func (l *SSEListener) Stop() { l.cancel() }

func (l *SSEListener) run() {
	for {
		select {
		case <-l.ctx.Done():
			return
		default:
		}
		l.connect()
		select {
		case <-l.ctx.Done():
			return
		case <-time.After(3 * time.Second):
		}
	}
}

func (l *SSEListener) connect() {
	req, err := http.NewRequestWithContext(l.ctx, "GET", l.url, nil)
	if err != nil {
		slog.Debug("SSE request creation failed", "error", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		if l.ctx.Err() != nil {
			return // stopped
		}
		slog.Debug("SSE connect failed", "url", l.url, "error", err)
		return
	}
	defer resp.Body.Close()

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		if l.ctx.Err() != nil {
			return
		}
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		var evt sseEvent
		if err := json.Unmarshal([]byte(data), &evt); err != nil {
			continue
		}
		if evt.Type == "ping" {
			continue
		}
		if l.callback != nil {
			l.callback(evt.Type, evt.Content, evt.WorkspaceId)
		}
	}
}
