package agentproxy

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// 显式配置永远优先——解析器不做任何改写。
func TestResolveNiuniuAgentCommandExplicitWins(t *testing.T) {
	if got := resolveNiuniuAgentCommand(`D:\custom\agent.exe`); got != `D:\custom\agent.exe` {
		t.Errorf("got %q", got)
	}
	if got := resolveNiuniuAgentCommand("agent-in-path"); got != "agent-in-path" {
		t.Errorf("got %q", got)
	}
}

// exe 同目录（sidecar 布局）优先于 ~/.niuniu/bin。
func TestResolveNiuniuAgentCommandSidecarLayout(t *testing.T) {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	// 我们无法控制测试二进制的真实位置，但至少能验证：
	// 在测试 exe 旁放一个 niuniu-agent 文件后能解析到它。
	exe, err := os.Executable()
	if err != nil {
		t.Skip("no executable path")
	}
	dir := filepath.Dir(exe)
	sidecar := filepath.Join(dir, "niuniu-agent"+suffix)
	if err := os.WriteFile(sidecar, []byte("stub"), 0o755); err != nil {
		t.Skipf("cannot write next to test exe: %v", err)
	}
	t.Cleanup(func() { _ = os.Remove(sidecar) })

	got := resolveNiuniuAgentCommand("")
	if got != sidecar {
		t.Errorf("got %q, want sidecar %q", got, sidecar)
	}
}

// exe 旁没有时：桌面解压目录（~/.niuniu/desktop-v2/sidecars）优先于
// ~/.niuniu/bin——桌面指纹机制保证 sidecars 永远是随包最新版。
func TestResolveNiuniuAgentCommandPrefersDesktopSidecars(t *testing.T) {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home")
	}
	desktop := filepath.Join(home, ".niuniu", "desktop-v2", "sidecars", "niuniu-agent"+suffix)
	homeBin := filepath.Join(home, ".niuniu", "bin", "niuniu-agent"+suffix)
	if _, err := os.Stat(desktop); err != nil {
		t.Skip("no desktop sidecar unpack on this machine")
	}
	if got := resolveNiuniuAgentCommand(""); got != desktop {
		t.Errorf("got %q, want desktop sidecar %q (preferred over bin)", got, desktop)
	}
	_ = homeBin
}

// 两处都没有 → 空串（调用方回退裸名走 PATH）。
func TestResolveNiuniuAgentCommandEmptyFallsBackToPath(t *testing.T) {
	got := resolveNiuniuAgentCommand("")
	if got != "" && filepath.IsAbs(got) {
		t.Logf("resolved to installed binary %q (acceptable)", got)
	}
	// 空串或绝对路径都合法；绝不能返回相对路径。
	if got != "" && !filepath.IsAbs(got) {
		t.Errorf("relative path %q is not a valid resolution", got)
	}
}
