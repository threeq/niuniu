package agentproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMissingFlags covers the shared help-text flag checker used by both
// ProbeClaudeCLI and ProbeCodexCLI. The probes themselves shell out to the real
// binaries (no unit coverage, same as before), but the parsing/decision core is
// pure and worth pinning.
func TestMissingFlags(t *testing.T) {
	codexHelp := `Usage: codex exec [OPTIONS] [PROMPT]

Options:
      --json                     Emit events as JSON lines
      --skip-git-repo-check      Allow running outside a git repo
  -C, --cd <DIR>                 Working directory
      --sandbox <MODE>           Sandbox policy [read-only|workspace-write|danger-full-access]
  -m, --model <MODEL>            Model to use
`

	// Local sample list (the codex judge no longer probes exec flags; this just
	// exercises the shared missingFlags helper still used by ProbeClaudeCLI).
	sample := []string{"--json", "--sandbox", "--skip-git-repo-check"}

	tests := []struct {
		name     string
		help     string
		required []string
		want     []string
	}{
		{
			name:     "all sample flags present",
			help:     codexHelp,
			required: sample,
			want:     nil,
		},
		{
			name:     "one flag dropped",
			help:     strings.ReplaceAll(codexHelp, "--sandbox", "--sbx"),
			required: sample,
			want:     []string{"--sandbox"},
		},
		{
			name:     "empty help misses everything",
			help:     "",
			required: sample,
			want:     sample,
		},
		{
			name:     "empty required is always satisfied",
			help:     "anything",
			required: nil,
			want:     nil,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := missingFlags(tc.help, tc.required)
			if strings.Join(got, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("missingFlags = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestNiuniuAgentConfiguredCommandWins: an explicit config command is used
// verbatim — the desktop install's real layout (config points at the sidecar
// dir, which is NOT on PATH) must resolve.
func TestNiuniuAgentConfiguredCommandWins(t *testing.T) {
	prev := niuniuAgentConfigured
	defer func() { niuniuAgentConfigured = prev }()

	SetNiuniuAgentConfiguredCommand(`C:\Users\me\.niuniu\desktop-v2\sidecars\niuniu-agent.exe`)
	if got := NiuniuAgentBinary(); got != `C:\Users\me\.niuniu\desktop-v2\sidecars\niuniu-agent.exe` {
		t.Fatalf("NiuniuAgentBinary() = %q, want the configured path", got)
	}
}

// TestProbeNiuniuAgentCLI_ResolvesConfiguredPath is the regression for the
// reported "niuniu-agent 不可用" on a desktop install where the binary lived in
// the sidecar dir and PATH had no niuniu entry: the old probe only called
// exec.LookPath("niuniu-agent") and therefore always failed.
func TestProbeNiuniuAgentCLI_ResolvesConfiguredPath(t *testing.T) {
	prev := niuniuAgentConfigured
	prevAvail := niuniuAgentAvailable.Load()
	t.Cleanup(func() {
		niuniuAgentConfigured = prev
		niuniuAgentAvailable.Store(prevAvail)
	})

	// A real file at an absolute path that is NOT on PATH.
	dir := t.TempDir()
	bin := filepath.Join(dir, "niuniu-agent")
	if err := os.WriteFile(bin, []byte("stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	SetNiuniuAgentConfiguredCommand(bin)
	ProbeNiuniuAgentCLI()
	if !NiuniuAgentAvailable() {
		t.Fatal("probe failed for an existing configured binary outside PATH — the desktop sidecar layout regressed")
	}

	// Missing binary must flip it back off.
	SetNiuniuAgentConfiguredCommand(filepath.Join(dir, "does-not-exist"))
	ProbeNiuniuAgentCLI()
	if NiuniuAgentAvailable() {
		t.Fatal("probe passed for a nonexistent binary")
	}
}
