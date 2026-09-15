package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLSAndRead(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("hello\nworld\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	lsOut, err := LS{}.Execute(context.Background(), mustJSON(t, lsInput{Path: dir}))
	if err != nil {
		t.Fatalf("LS: %v", err)
	}
	if !strings.Contains(lsOut, "a.txt") || !strings.Contains(lsOut, "sub/") {
		t.Errorf("LS output = %q", lsOut)
	}

	readOut, err := Read{}.Execute(context.Background(), mustJSON(t, readInput{Path: filepath.Join(dir, "a.txt")}))
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if !strings.Contains(readOut, "1\thello") || !strings.Contains(readOut, "2\tworld") {
		t.Errorf("Read output = %q", readOut)
	}

	// Offset/limit windows.
	out2, _ := Read{}.Execute(context.Background(), mustJSON(t, readInput{Path: filepath.Join(dir, "a.txt"), Offset: 2, Limit: 1}))
	if !strings.Contains(out2, "2\tworld") || strings.Contains(out2, "1\thello") {
		t.Errorf("offset/limit output = %q", out2)
	}
}

func TestWriteEditRoundTrip(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "nested", "f.txt")

	if _, err := (Write{}).Execute(context.Background(), mustJSON(t, writeInput{Path: p, Content: "alpha\nbeta\ngamma\n"})); err != nil {
		t.Fatalf("Write: %v", err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "alpha\nbeta\ngamma\n" {
		t.Errorf("written content = %q", data)
	}

	// Ambiguous match must be refused.
	if _, err := (Edit{}).Execute(context.Background(), mustJSON(t, editInput{Path: p, OldString: "a", NewString: "b"})); err == nil {
		t.Error("ambiguous edit should fail")
	}
	// Unique replacement works.
	if _, err := (Edit{}).Execute(context.Background(), mustJSON(t, editInput{Path: p, OldString: "beta", NewString: "BETA"})); err != nil {
		t.Fatalf("Edit: %v", err)
	}
	data, _ = os.ReadFile(p)
	if !strings.Contains(string(data), "BETA") {
		t.Errorf("after edit = %q", data)
	}
	// Identical strings refused.
	if _, err := (Edit{}).Execute(context.Background(), mustJSON(t, editInput{Path: p, OldString: "BETA", NewString: "BETA"})); err == nil {
		t.Error("identical old/new should fail")
	}
}

func TestGrepAndGlob(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "x.go"), []byte("package main\n\nfunc Run() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "y.md"), []byte("run forest run\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, "deep", "nest"), 0o755)
	os.WriteFile(filepath.Join(dir, "deep", "nest", "z.go"), []byte("func helper() {}\n"), 0o644)

	g, _ := (Grep{}).Execute(context.Background(), mustJSON(t, grepInput{Pattern: "func", Path: dir, Include: "*.go"}))
	lines := strings.Split(g, "\n")
	if len(lines) != 2 || !strings.Contains(g, "x.go:3:") || !strings.Contains(g, filepath.Join("deep", "nest", "z.go")+":1:") {
		t.Errorf("Grep output = %q", g)
	}

	gl, _ := (Glob{}).Execute(context.Background(), mustJSON(t, globInput{Pattern: "**/*.go", Path: dir}))
	if !strings.Contains(gl, "x.go") || !strings.Contains(gl, "deep") || !strings.Contains(gl, "z.go") {
		t.Errorf("Glob output = %q", gl)
	}
	if strings.Contains(gl, "y.md") {
		t.Errorf("Glob leaked non-matching file: %q", gl)
	}

	// Single-star stays within one segment.
	gl2, _ := (Glob{}).Execute(context.Background(), mustJSON(t, globInput{Pattern: "*.go", Path: dir}))
	if strings.Contains(gl2, "z.go") {
		t.Errorf("single * crossed directories: %q", gl2)
	}
}

func TestTodoWritePersists(t *testing.T) {
	wd, _ := os.Getwd()
	defer os.Chdir(wd)
	tmp := t.TempDir()
	os.Chdir(tmp)

	out, err := (TodoWrite{}).Execute(context.Background(), mustJSON(t, todoInput{Todos: []todoItem{
		{Content: "step one", Status: "completed"},
		{Content: "step two", Status: "in_progress", ActiveForm: "doing step two"},
		{Content: "step three", Status: "pending"},
	}}))
	if err != nil {
		t.Fatalf("TodoWrite: %v", err)
	}
	if !strings.Contains(out, "[x] step one") || !strings.Contains(out, "[~] doing step two") || !strings.Contains(out, "[ ] step three") {
		t.Errorf("render = %q", out)
	}
	data, err := os.ReadFile(filepath.Join(tmp, ".niuniu-agent", "todos.json"))
	if err != nil || !strings.Contains(string(data), "step two") {
		t.Errorf("todos.json not persisted (err=%v)", err)
	}

	// Invalid status refused.
	if _, err := (TodoWrite{}).Execute(context.Background(), json.RawMessage(`{"todos":[{"content":"x","status":"nope"}]}`)); err == nil {
		t.Error("invalid status should fail")
	}
}

func TestBashEcho(t *testing.T) {
	in := `{"command":"echo niuniu-bash-ok"}`
	out, err := (Bash{}).Execute(context.Background(), json.RawMessage(in))
	if err != nil {
		t.Fatalf("Bash: %v", err)
	}
	if !strings.Contains(out, "niuniu-bash-ok") {
		t.Errorf("Bash output = %q", out)
	}
	// Non-zero exit reports output, not a tool error.
	out, err = (Bash{}).Execute(context.Background(), json.RawMessage(`{"command":"echo before-fail && exit 3"}`))
	if err != nil {
		t.Fatalf("Bash non-zero exit returned tool error: %v", err)
	}
	if !strings.Contains(out, "before-fail") {
		t.Errorf("failed-command output = %q", out)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
