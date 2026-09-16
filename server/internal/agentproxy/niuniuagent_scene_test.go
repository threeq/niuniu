package agentproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/config"
)

func TestRenderNiuniuInject(t *testing.T) {
	s := renderNiuniuInject()
	for _, want := range []string{
		"niuniu-mcp", "mcp__niuniu__", // 工具族说明（含注册前缀）
		"看板", "黑板", "收件箱", "记忆", // 四族工具
		"AUTOHOST_DONE", // 收尾约定
		"看板纪律", "完成列",   // 看板纪律要点
		"memory_search", "MemorySave", // 记忆互通：优先服务端，本地回退
	} {
		if !strings.Contains(s, want) {
			t.Errorf("inject content missing %q", want)
		}
	}
}

// sceneMCPWriter records Generate calls.
type sceneMCPWriter struct {
	called  bool
	workDir string
	opts    config.MCPGenerateOptions
	fail    bool
}

func (w *sceneMCPWriter) Generate(wsPath string, opts config.MCPGenerateOptions, _ []string, _ string) (*MCPGenerateResult, error) {
	w.called = true
	w.workDir = wsPath
	w.opts = opts
	if w.fail {
		return nil, os.ErrPermission
	}
	return &MCPGenerateResult{}, nil
}
func (w *sceneMCPWriter) GenerateClaudeSettings(string) error { return nil }
func (w *sceneMCPWriter) GenerateCodexConfigToml(string, config.MCPGenerateOptions) error {
	return nil
}
func (w *sceneMCPWriter) GenerateCodexConfigArgs(config.MCPGenerateOptions) ([]string, error) {
	return nil, nil
}
func (w *sceneMCPWriter) NiuniuMcpServer(config.MCPGenerateOptions) (config.McpServerEntry, error) {
	return config.McpServerEntry{}, nil
}
func (w *sceneMCPWriter) SetWorkspaceKBReadonly(string, []string) error { return nil }

func TestProjectNiuniuAgentFiles(t *testing.T) {
	dir := t.TempDir()
	gen := &sceneMCPWriter{}

	err := projectNiuniuAgentFiles(dir, "tok-123", gen, 21, 932, filepath.Join(dir, ".team", "inboxes"))
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	// inject.md 落盘且内容正确。
	data, err := os.ReadFile(filepath.Join(dir, ".niuniu-agent", "inject.md"))
	if err != nil {
		t.Fatalf("inject.md: %v", err)
	}
	if !strings.Contains(string(data), "AUTOHOST_DONE") {
		t.Errorf("inject.md content wrong: %s", data)
	}
	// .mcp.json 生成被调用，参数正确（含 sessionToken → niuniu-mcp 可用）。
	if !gen.called {
		t.Fatal("Generate not called — .mcp.json (niuniu-mcp) would be missing")
	}
	if gen.workDir != dir || gen.opts.SessionToken != "tok-123" || gen.opts.WorkspaceID != 932 {
		t.Errorf("Generate args = dir:%s opts:%+v", gen.workDir, gen.opts)
	}

	// 幂等：重复投影不报错。
	if err := projectNiuniuAgentFiles(dir, "tok-123", gen, 21, 932, filepath.Join(dir, ".team", "inboxes")); err != nil {
		t.Errorf("re-project: %v", err)
	}
}

func TestProjectNiuniuAgentFilesDegraded(t *testing.T) {
	dir := t.TempDir()
	// gen 为 nil（MCP 二进制不可用）——inject.md 仍要落盘。
	if err := projectNiuniuAgentFiles(dir, "", nil, 0, 0, ""); err != nil {
		t.Fatalf("nil writer must not fail: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, ".niuniu-agent", "inject.md")); err != nil {
		t.Fatalf("inject.md missing: %v", err)
	}
	// Generate 失败上报错误但不 panic。
	gen := &sceneMCPWriter{fail: true}
	if err := projectNiuniuAgentFiles(dir, "", gen, 0, 0, ""); err == nil {
		t.Error("want error when Generate fails")
	}
}
