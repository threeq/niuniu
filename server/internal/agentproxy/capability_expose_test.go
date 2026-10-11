package agentproxy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func capSection(kind, name, body string) AgentCapability {
	return AgentCapability{TargetKind: kind, Name: name, DisplayName: name, Section: body}
}

func TestRenderCapabilityBlock_Empty(t *testing.T) {
	if got := RenderCapabilityBlock(nil); got != "" {
		t.Fatalf("empty set should render nothing, got %q", got)
	}
	if got := RenderCapabilityBlock([]AgentCapability{{Name: "x", Section: "   "}}); got != "" {
		t.Fatalf("blank section should be dropped, got %q", got)
	}
}

func TestRenderCapabilityBlock_PartitionFence(t *testing.T) {
	block := RenderCapabilityBlock([]AgentCapability{
		capSection("provider", "pets", "### 外部 API · Pets\n- 怎么用：call_external_api(provider=\"pets\")"),
		capSection("data_source", "analytics", "### 数据源 · analytics（mysql）\n- 怎么用：run_data_query(source=\"analytics\")"),
	})
	if strings.Count(block, CapabilityBlockBegin) != 1 || strings.Count(block, CapabilityBlockEnd) != 1 {
		t.Fatalf("want exactly one marker pair:\n%s", block)
	}
	// The block must be explicitly framed as DATA, not instructions.
	if !strings.Contains(block, "非指令") || !strings.Contains(block, "DATA, NOT INSTRUCTIONS") {
		t.Fatalf("block is not framed as data:\n%s", block)
	}
	if !strings.Contains(block, "call_external_api") || !strings.Contains(block, "run_data_query") {
		t.Fatalf("sections missing:\n%s", block)
	}
}

func TestRenderCapabilityBlock_Deterministic(t *testing.T) {
	caps := []AgentCapability{
		capSection("provider", "b", "### B"),
		capSection("provider", "a", "### A"),
	}
	if RenderCapabilityBlock(caps) != RenderCapabilityBlock(caps) {
		t.Fatal("render is not byte-stable (would perturb the prompt cache)")
	}
}

func TestUpsertCapabilityBlock(t *testing.T) {
	block := RenderCapabilityBlock([]AgentCapability{capSection("provider", "pets", "### 外部 API · Pets")})
	base := "# Workspace\n\nsome intro\n"

	out := upsertCapabilityBlock(base, block)
	if !strings.Contains(out, "some intro") || strings.Count(out, CapabilityBlockBegin) != 1 {
		t.Fatalf("append wrong: %q", out)
	}
	// Idempotent: a re-upsert of the same block yields identical bytes, so the
	// instruction file is not rewritten on every spawn.
	if out2 := upsertCapabilityBlock(out, block); out2 != out {
		t.Fatalf("upsert is not idempotent:\n%q\n%q", out, out2)
	}
	// A different block replaces the old one in place.
	block2 := RenderCapabilityBlock([]AgentCapability{capSection("provider", "other", "### 外部 API · Other")})
	out3 := upsertCapabilityBlock(out, block2)
	if strings.Contains(out3, "Pets") || !strings.Contains(out3, "Other") || strings.Count(out3, CapabilityBlockBegin) != 1 {
		t.Fatalf("replace wrong: %q", out3)
	}
	// Empty removes the section, keeps user content.
	out4 := upsertCapabilityBlock(out3, "")
	if strings.Contains(out4, CapabilityBlockBegin) || !strings.Contains(out4, "some intro") {
		t.Fatalf("clear wrong: %q", out4)
	}
}

func TestWriteCapabilityInstructionBlock(t *testing.T) {
	dir := t.TempDir()
	claude := filepath.Join(dir, "CLAUDE.md")
	if err := os.WriteFile(claude, []byte("# Workspace\n\nintro\n"), 0o644); err != nil {
		t.Fatalf("seed: %v", err)
	}
	caps := []AgentCapability{capSection("provider", "pets", "### 外部 API · Pets\n- 调用：call_external_api(provider=\"pets\")")}

	if err := writeCapabilityInstructionBlock(dir, "claude", caps); err != nil {
		t.Fatalf("write: %v", err)
	}
	data, _ := os.ReadFile(claude)
	if !strings.Contains(string(data), CapabilityBlockBegin) || !strings.Contains(string(data), "intro") {
		t.Fatalf("block not written or intro lost: %q", data)
	}
	// A second identical write must not change the file (prompt-cache safety).
	if err := writeCapabilityInstructionBlock(dir, "claude", caps); err != nil {
		t.Fatalf("rewrite: %v", err)
	}
	data2, _ := os.ReadFile(claude)
	if string(data2) != string(data) {
		t.Fatalf("identical set rewrote the instruction file:\n%q\n%q", data, data2)
	}

	// codex writes AGENTS.md; created from scratch when absent.
	if err := writeCapabilityInstructionBlock(dir, "codex", caps); err != nil {
		t.Fatalf("codex write: %v", err)
	}
	agents, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil || !strings.Contains(string(agents), CapabilityBlockBegin) {
		t.Fatalf("AGENTS.md not created: err=%v data=%q", err, agents)
	}

	// Clearing removes the managed block.
	if err := writeCapabilityInstructionBlock(dir, "claude", nil); err != nil {
		t.Fatalf("clear: %v", err)
	}
	data, _ = os.ReadFile(claude)
	if strings.Contains(string(data), CapabilityBlockBegin) {
		t.Fatalf("clear left block behind: %q", data)
	}
}
