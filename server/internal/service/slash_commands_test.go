package service

import (
	"context"
	"testing"
)

func TestSlashCommandService_ListCommands_CodexBuiltins(t *testing.T) {
	svc := NewSlashCommandService(nil)

	commands, err := svc.ListCommands(context.Background(), "codex", "")
	if err != nil {
		t.Fatalf("ListCommands: %v", err)
	}
	for _, cmd := range commands {
		if cmd.Name == "doctor" {
			t.Fatalf("codex builtins should not include Claude-only command: %+v", commands)
		}
		if cmd.Name == "memory" && cmd.Description == "让 Agent 查看或管理 CLAUDE.md 记忆文件" {
			t.Fatalf("codex memory command should not mention CLAUDE.md: %+v", cmd)
		}
	}
	// Core codex builtins must be present.
	for _, name := range []string{"clear", "cost", "status", "review", "memory", "init"} {
		if !hasSlashCommand(commands, name) {
			t.Fatalf("codex commands should include %q: %+v", name, commands)
		}
	}
	// superpowers:* MUST NOT be advertised by niuniu — the user installs
	// the superpowers plugin globally in codex, and codex itself surfaces
	// those commands. Hardcoding them here would either lie (when the
	// plugin is not installed) or double-list (when it is).
	for _, name := range []string{"spec", "plan", "coding", "superpowers:spec", "superpowers:plan", "superpowers:coding", "superpowers:review"} {
		if hasSlashCommand(commands, name) {
			t.Fatalf("codex commands should NOT advertise superpowers/plugin alias %q: %+v", name, commands)
		}
	}
}

func hasSlashCommand(commands []SlashCommand, name string) bool {
	for _, cmd := range commands {
		if cmd.Name == name {
			return true
		}
	}
	return false
}
