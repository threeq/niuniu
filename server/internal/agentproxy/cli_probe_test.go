package agentproxy

import (
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
