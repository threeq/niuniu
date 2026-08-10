//go:build windows

package localrunner

import (
	"context"
	"strings"
	"testing"
)

// TestExecute_PreservesEmbeddedQuotes is the regression for the "write escaping"
// bug: a command carrying double quotes must reach cmd.exe verbatim, not with
// Go's EscapeArg turning " into \". Before the SysProcAttr.CmdLine fix, this
// echoed `import \"fmt\"` (stray backslashes), which broke writing source files.
func TestExecute_PreservesEmbeddedQuotes(t *testing.T) {
	res := Execute(context.Background(), `echo import "fmt"`, t.TempDir(), nil)
	if !res.OK {
		t.Fatalf("exec failed: %+v", res)
	}
	got := strings.TrimSpace(res.Stdout)
	if got != `import "fmt"` {
		t.Fatalf("quotes mangled: got %q, want %q", got, `import "fmt"`)
	}
	if strings.Contains(got, `\"`) {
		t.Fatalf("output has backslash-escaped quotes: %q", got)
	}
}

// TestExecute_PreservesRedirectedQuotedWrite covers the actual use case: writing
// a source line with quotes to a file via redirection, then reading it back.
func TestExecute_PreservesRedirectedQuotedWrite(t *testing.T) {
	dir := t.TempDir()
	res := Execute(context.Background(), `echo import "fmt">out.txt`, dir, nil)
	if !res.OK {
		t.Fatalf("write failed: %+v", res)
	}
	read := Execute(context.Background(), `type out.txt`, dir, nil)
	if !read.OK {
		t.Fatalf("read failed: %+v", read)
	}
	if got := strings.TrimSpace(read.Stdout); got != `import "fmt"` {
		t.Fatalf("file content mangled: got %q, want %q", got, `import "fmt"`)
	}
}
