package mcp

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".mcp.json")
	os.WriteFile(path, []byte(`{
		"mcpServers": {
			"blackboard": {
				"command": "niuniu-mcp",
				"args": ["--project-id", "21"],
				"env": {"NIUNIU_MCP_TOKEN": "t0k"}
			},
			"simple": {"command": "echo-server"}
		}
	}`), 0o644)

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	bb, ok := cfg["blackboard"]
	if !ok {
		t.Fatalf("missing blackboard server: %+v", cfg)
	}
	if bb.Command != "niuniu-mcp" || len(bb.Args) != 2 || bb.Args[1] != "21" {
		t.Errorf("blackboard = %+v", bb)
	}
	if bb.Env["NIUNIU_MCP_TOKEN"] != "t0k" {
		t.Errorf("blackboard env = %+v", bb.Env)
	}
	if cfg["simple"].Command != "echo-server" {
		t.Errorf("simple = %+v", cfg["simple"])
	}
}

func TestLoadConfigMissingFileIsEmpty(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), ".mcp.json"))
	if err != nil {
		t.Fatalf("missing file must not error (no servers is normal): %v", err)
	}
	if len(cfg) != 0 {
		t.Errorf("cfg = %+v, want empty", cfg)
	}
}

func TestLoadConfigInvalid(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.json")
	os.WriteFile(bad, []byte(`{"mcpServers": {`), 0o644)
	if _, err := LoadConfig(bad); err == nil {
		t.Error("want error for malformed json")
	}
	// Server entry without a command is invalid.
	nocmd := filepath.Join(dir, "nocmd.json")
	os.WriteFile(nocmd, []byte(`{"mcpServers": {"x": {"args": ["a"]}}}`), 0o644)
	if _, err := LoadConfig(nocmd); err == nil || !strings.Contains(err.Error(), "x") {
		t.Errorf("err = %v, want server-name error for missing command", err)
	}
}

func TestStartSkipsBrokenServers(t *testing.T) {
	// 一个坏 server（命令不存在）+ 一个好 server（本测试二进制复演 fake）。
	// 坏 server 只告警，不阻断好 server 的工具注册。
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, ".mcp.json"), []byte(`{
		"mcpServers": {
			"broken": {"command": "definitely-not-a-real-binary-xyz"},
			"good": {"command": `+jsonString(os.Args[0])+`, "args": ["-test.run=TestHelperMCPServer$"], "env": {"NIUNIU_TEST_MCP_SERVER": "1"}}
		}
	}`), 0o644)

	mgr := Start(dir)
	defer mgr.Close()
	if _, ok := mgr.ToolNames()["mcp__good__echo"]; !ok {
		t.Errorf("good server tools missing: %v", mgr.ToolNames())
	}
}

// jsonString encodes s as a JSON string literal (escapes Windows backslashes).
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
