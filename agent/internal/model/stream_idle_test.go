package model

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// 复现桌面端「天气轮」卡死：网关发部分 SSE 后持续发 ping 心跳帧但模型
// 事件停了，且永不关连接。验证事件级 idle 看门狗能否打破这种挂起。

func TestScanSSEIdleFiresDespiteHeartbeats(t *testing.T) {
	os.Setenv("NIUNIU_AGENT_STREAM_IDLE_TIMEOUT", "500ms")
	defer os.Unsetenv("NIUNIU_AGENT_STREAM_IDLE_TIMEOUT")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"thinking_delta\",\"thinking\":\"start\"}}\n\n")
		f.Flush()
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case <-tick.C:
				fmt.Fprint(w, "event: ping\ndata: {\"type\":\"ping\"}\n\n")
				f.Flush()
			}
		}
	}))
	defer srv.Close()

	body := mustGet(srv.URL)
	closer, canClose := body.(io.Closer)
	if !canClose {
		t.Skip("body is not a Closer")
	}
	events := 0
	err := scanSSE(context.Background(), body, func() { closer.Close() }, func(event, data string) error {
		events++
		return nil
	})
	if err == nil {
		t.Fatal("idle watchdog must fire when only pings arrive — got nil error (stream hung)")
	}
	if events != 1 {
		t.Errorf("events = %d, want 1 (only the initial delta)", events)
	}
}

// 对照：真实 data 帧持续到达时，看门狗不误杀。
func TestScanSSENoIdleWhileDataFlows(t *testing.T) {
	os.Setenv("NIUNIU_AGENT_STREAM_IDLE_TIMEOUT", "500ms")
	defer os.Unsetenv("NIUNIU_AGENT_STREAM_IDLE_TIMEOUT")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "event: content_block_delta\ndata: {\"delta\":{\"type\":\"text_delta\",\"text\":\"d%d\"}}\n\n", i)
			f.Flush()
			time.Sleep(100 * time.Millisecond)
		}
	}))
	defer srv.Close()

	events := 0
	err := scanSSE(context.Background(), mustGet(srv.URL), func() {}, func(event, data string) error {
		events++
		return nil
	})
	if err != nil {
		t.Fatalf("scanSSE: %v", err)
	}
	if events != 5 {
		t.Errorf("events = %d, want 5", events)
	}
}

func mustGet(url string) io.ReadCloser {
	resp, err := http.Get(url)
	if err != nil {
		panic(err)
	}
	return resp.Body
}
