package localrunner

import (
	"context"
	"runtime"
	"strings"
	"sync"
	"testing"
)

// echoCmd / stderrCmd / exitCmd return a shell command string that works under
// the platform shell Execute uses (cmd on Windows, sh elsewhere).
func echoCmd(s string) string { return "echo " + s }

func stderrCmd(s string) string {
	if runtime.GOOS == "windows" {
		return "echo " + s + " 1>&2"
	}
	return "echo " + s + " 1>&2"
}

func exitCmd(code string) string { return "exit " + code }

func TestExecute_CapturesStdout(t *testing.T) {
	res := Execute(context.Background(), echoCmd("hello"), t.TempDir(), nil)
	if !res.OK || res.Exit != 0 {
		t.Fatalf("expected clean exit, got OK=%v exit=%d err=%v", res.OK, res.Exit, res.Err)
	}
	if !strings.Contains(res.Stdout, "hello") {
		t.Fatalf("stdout %q should contain 'hello'", res.Stdout)
	}
}

func TestExecute_StreamsLines(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	res := Execute(context.Background(), echoCmd("streamed"), t.TempDir(), func(level, text string) {
		mu.Lock()
		lines = append(lines, level+":"+text)
		mu.Unlock()
	})
	if !res.OK {
		t.Fatalf("expected OK, err=%v", res.Err)
	}
	found := false
	for _, l := range lines {
		if strings.HasPrefix(l, levelStdout+":") && strings.Contains(l, "streamed") {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a streamed stdout line, got %v", lines)
	}
}

func TestExecute_NonZeroExit(t *testing.T) {
	res := Execute(context.Background(), exitCmd("3"), t.TempDir(), nil)
	if res.OK {
		t.Fatal("expected OK=false for non-zero exit")
	}
	if res.Exit != 3 {
		t.Fatalf("expected exit 3, got %d", res.Exit)
	}
	if res.Err != nil {
		t.Fatalf("a non-zero exit is not an engine error, got %v", res.Err)
	}
}

func TestExecute_Stderr(t *testing.T) {
	res := Execute(context.Background(), stderrCmd("oops"), t.TempDir(), nil)
	if !strings.Contains(res.Stderr, "oops") {
		t.Fatalf("stderr %q should contain 'oops'", res.Stderr)
	}
}
