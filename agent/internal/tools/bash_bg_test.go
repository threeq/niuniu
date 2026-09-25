package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestBackgroundBashAndOutput(t *testing.T) {
	reg := NewRegistry(Bash{}, BashOutput{})

	params, _ := json.Marshal(map[string]any{"command": "echo bg-ready", "run_in_background": true})
	out, err := reg.Execute(context.Background(), "Bash", params)
	if err != nil {
		t.Fatalf("Bash bg: %v", err)
	}
	if !strings.Contains(out, "bash-") || !strings.Contains(out, "BashOutput") {
		t.Fatalf("start output = %q", out)
	}
	id := out[strings.Index(out, "bash-"):]
	if i := strings.IndexAny(id, " )"); i >= 0 {
		id = id[:i]
	}
	oid, _ := json.Marshal(map[string]any{"bash_id": id})

	// 轮询直到退出。
	deadline := time.Now().Add(10 * time.Second)
	var res string
	for {
		res, err = reg.Execute(context.Background(), "BashOutput", oid)
		if err != nil {
			t.Fatalf("BashOutput: %v", err)
		}
		if strings.Contains(res, "exited") || strings.Contains(res, "status: done") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("background bash never exited; last = %q", res)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !strings.Contains(res, "bg-ready") {
		t.Errorf("output missing echoed text: %q", res)
	}

	// 未知 id 报错。
	if _, err := reg.Execute(context.Background(), "BashOutput", json.RawMessage(`{"bash_id":"bash-999"}`)); err == nil {
		t.Error("want error for unknown bash id")
	}
}
