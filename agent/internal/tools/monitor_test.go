package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func runMonitor(t *testing.T, in monitorInput) (string, error) {
	t.Helper()
	raw, _ := json.Marshal(in)
	return Monitor{}.Execute(context.Background(), raw)
}

// 预起后台任务 → Monitor 按正则收集命中行（进程退出后仍返回内容）。
func TestMonitorCollectsMatchingLines(t *testing.T) {
	id, _ := startBackgroundBash("echo build-start && echo ready-port-8080 && echo done")
	time.Sleep(500 * time.Millisecond) // let it finish
	out, err := runMonitor(t, monitorInput{BashID: id, Filter: `ready-port-\d+`, TimeoutSec: 5})
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	if !strings.Contains(out, "ready-port-8080") {
		t.Errorf("output = %q", out)
	}
}

// 内联 command：Monitor 自己拉起后台进程并监控。
func TestMonitorInlineCommand(t *testing.T) {
	out, err := runMonitor(t, monitorInput{Command: "echo hello-monitor-world", Filter: `hello-monitor`, TimeoutSec: 10})
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	if !strings.Contains(out, "hello-monitor-world") {
		t.Errorf("output = %q", out)
	}
}

// 无命中 + 进程退出 → 干净的 no-match 报告（不是错误）。
func TestMonitorCleanNoMatch(t *testing.T) {
	id, _ := startBackgroundBash("echo nothing-relevant")
	time.Sleep(500 * time.Millisecond)
	out, err := runMonitor(t, monitorInput{BashID: id, Filter: `never-appears-xyz`, TimeoutSec: 5})
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	if !strings.Contains(out, "no matching lines") {
		t.Errorf("output = %q", out)
	}
}

// 超时路径：长进程 + 永不命中 → timeout 报告、进程仍跑。
func TestMonitorTimeout(t *testing.T) {
	id, _ := startBackgroundBash("ping -n 30 127.0.0.1 > NUL 2>&1 || sleep 30")
	out, err := runMonitor(t, monitorInput{BashID: id, Filter: `never-appears-xyz`, TimeoutSec: 1})
	if err != nil {
		t.Fatalf("monitor: %v", err)
	}
	if !strings.Contains(out, "timeout") {
		t.Errorf("output = %q", out)
	}
}

// 参数校验：缺 filter / 缺来源 / 坏正则。
func TestMonitorValidation(t *testing.T) {
	if _, err := runMonitor(t, monitorInput{}); err == nil || !strings.Contains(err.Error(), "filter") {
		t.Errorf("err = %v", err)
	}
	if _, err := runMonitor(t, monitorInput{Filter: "x"}); err == nil {
		t.Error("want error without bash_id/command")
	}
	if _, err := runMonitor(t, monitorInput{BashID: "bash-1", Filter: "("}); err == nil || !strings.Contains(err.Error(), "bad filter") {
		t.Errorf("err = %v", err)
	}
}
