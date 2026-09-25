package lsp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// ---- helper-process mock language server ----
// 测试二进制以子进程身份运行一个最小 LSP server（Content-Length framing），
// 对 initialize / definition / references / hover / documentSymbol 返回
// 固定结果——协议层端到端验证，无需真实 gopls。

func TestHelperLSPServer(t *testing.T) {
	if os.Getenv("NIUNIU_AGENT_TEST_LSP_SERVER") != "1" {
		t.Skip("helper process")
	}
	r := bufio.NewReader(os.Stdin)
	w := os.Stdout
	for {
		msg, err := readMessage(r)
		if err != nil {
			return
		}
		var m struct {
			ID     *int   `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(msg, &m) != nil {
			continue
		}
		if m.ID == nil {
			continue
		}
		var result any
		switch m.Method {
		case "initialize":
			result = map[string]any{"capabilities": map[string]any{}}
		case "textDocument/definition":
			result = []map[string]any{{
				"uri":   "file:///C:/work/def.go",
				"range": map[string]any{"start": map[string]any{"line": 4, "character": 5}},
			}}
		case "textDocument/references":
			result = []map[string]any{
				{"uri": "file:///C:/work/a.go", "range": map[string]any{"start": map[string]any{"line": 1, "character": 0}}},
				{"uri": "file:///C:/work/b.go", "range": map[string]any{"start": map[string]any{"line": 7, "character": 2}}},
			}
		case "textDocument/hover":
			result = map[string]any{"contents": map[string]any{"value": "func Hello() string"}}
		case "textDocument/documentSymbol":
			result = []map[string]any{
				{"name": "Hello", "kind": 12, "range": map[string]any{"start": map[string]any{"line": 4, "character": 5}}},
				{"name": "World", "kind": 5, "range": map[string]any{"start": map[string]any{"line": 9, "character": 6}},
					"children": []map[string]any{{"name": "inner", "kind": 8, "range": map[string]any{"start": map[string]any{"line": 10, "character": 1}}}}},
			}
		default:
			result = nil
		}
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": *m.ID, "result": result})
		fmt.Fprintf(w, "Content-Length: %d\r\n\r\n%s", len(data), data)
	}
}

func startHelper(t *testing.T) *Client {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(exe, "-test.run", "TestHelperLSPServer", "-test.v")
	cmd.Env = append(os.Environ(), "NIUNIU_AGENT_TEST_LSP_SERVER=1")
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stdin.Close()
		cmd.Process.Kill()
		cmd.Wait()
	})
	return &Client{cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout)}
}

// ---- tests ----

// framing：writeMessage/readMessage 按 Content-Length 头成对。
func TestFraming(t *testing.T) {
	var buf strings.Builder
	if err := writeMessage(&buf, map[string]any{"a": "x\ny"}); err != nil {
		t.Fatal(err)
	}
	s := buf.String()
	if !strings.HasPrefix(s, "Content-Length: ") {
		t.Fatalf("missing header: %q", s)
	}
	msg, err := readMessage(bufio.NewReader(strings.NewReader(s)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(msg), `"a":"x\ny"`) {
		t.Errorf("payload = %q", msg)
	}
}

// 协议端到端：helper server 上完成 initialize + 四个导航操作。
func TestClientNavigation(t *testing.T) {
	c := startHelper(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	var initRes json.RawMessage
	if err := c.call(ctx, "initialize", map[string]any{"rootUri": "file:///C:/work"}, &initRes); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	locs, err := c.Definition(ctx, "C:/work/main.go", 9, 0)
	if err != nil || len(locs) != 1 || locs[0].Line != 4 {
		t.Fatalf("definition: %v %+v", err, locs)
	}
	refs, err := c.References(ctx, "C:/work/main.go", 9, 0)
	if err != nil || len(refs) != 2 {
		t.Fatalf("references: %v %+v", err, refs)
	}
	h, err := c.Hover(ctx, "C:/work/main.go", 9, 0)
	if err != nil || !strings.Contains(h, "func Hello") {
		t.Fatalf("hover: %v %q", err, h)
	}
	syms, err := c.Symbols(ctx, "C:/work/main.go")
	if err != nil || len(syms) != 3 { // Hello + World + child inner
		t.Fatalf("symbols: %v %+v", err, syms)
	}
	if syms[0].Kind != "function" || syms[1].Kind != "class" || syms[2].Name != "inner" {
		t.Errorf("symbols = %+v", syms)
	}
}
