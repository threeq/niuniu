package bundle

import (
	"os"
	"strings"
	"testing"
)

func sep() string { return string(os.PathListSeparator) }

func TestMergePaths_DedupePreservesFirstSeenOrder(t *testing.T) {
	got := mergePaths(
		strings.Join([]string{"/opt/homebrew/bin", "/usr/bin"}, sep()),
		strings.Join([]string{"/usr/bin", "/bin", "/usr/local/bin"}, sep()),
		strings.Join([]string{"/usr/local/bin"}, sep()),
	)
	want := strings.Join([]string{"/opt/homebrew/bin", "/usr/bin", "/bin", "/usr/local/bin"}, sep())
	if got != want {
		t.Fatalf("mergePaths() = %q, want %q", got, want)
	}
}

func TestMergePaths_DropsEmptyEntries(t *testing.T) {
	got := mergePaths("", strings.Join([]string{"/usr/bin", "", "/bin"}, sep()), "")
	want := strings.Join([]string{"/usr/bin", "/bin"}, sep())
	if got != want {
		t.Fatalf("mergePaths() = %q, want %q", got, want)
	}
}

func TestMergePaths_RecoversMissingToolDir(t *testing.T) {
	// Simulate the bug: GUI-inherited PATH lacks /usr/local/bin where node lives;
	// the login-shell PATH supplies it. After merge the dir must be present.
	minimal := strings.Join([]string{"/usr/bin", "/bin", "/usr/sbin", "/sbin"}, sep())
	shell := strings.Join([]string{"/usr/local/bin", "/usr/bin", "/bin"}, sep())
	got := mergePaths(shell, minimal)
	if !contains(got, "/usr/local/bin") {
		t.Fatalf("merged PATH %q missing /usr/local/bin", got)
	}
}

func TestSetEnv_ReplacesExistingKey(t *testing.T) {
	env := []string{"FOO=1", "PATH=/old", "BAR=2"}
	got := setEnv(env, "PATH", "/new")
	if countPrefix(got, "PATH=") != 1 {
		t.Fatalf("expected exactly one PATH entry, got %v", got)
	}
	if !hasEntry(got, "PATH=/new") {
		t.Fatalf("PATH not set to /new: %v", got)
	}
	if !hasEntry(got, "FOO=1") || !hasEntry(got, "BAR=2") {
		t.Fatalf("setEnv dropped unrelated keys: %v", got)
	}
}

func TestSetEnv_AppendsWhenAbsent(t *testing.T) {
	env := []string{"FOO=1"}
	got := setEnv(env, "PATH", "/new")
	if !hasEntry(got, "PATH=/new") || !hasEntry(got, "FOO=1") {
		t.Fatalf("setEnv() = %v, want FOO=1 and PATH=/new", got)
	}
}

func contains(pathList, dir string) bool {
	for _, d := range strings.Split(pathList, sep()) {
		if d == dir {
			return true
		}
	}
	return false
}

func countPrefix(env []string, prefix string) int {
	n := 0
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			n++
		}
	}
	return n
}

func hasEntry(env []string, want string) bool {
	for _, e := range env {
		if e == want {
			return true
		}
	}
	return false
}
