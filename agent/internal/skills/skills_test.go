package skills

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSkill(t *testing.T, dir, name, content string) {
	t.Helper()
	d := filepath.Join(dir, name)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func setUserHome(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("HOME", dir)        // unix
	t.Setenv("USERPROFILE", dir) // windows
}

func TestScanProjectAndUserSkills(t *testing.T) {
	cwd := t.TempDir()
	home := t.TempDir()
	setUserHome(t, home)
	writeSkill(t, filepath.Join(cwd, ".niuniu-agent", "skills"), "deploy",
		"---\nname: deploy\ndescription: Verify release readiness before shipping.\n---\n\nStep one: run the gates.\n")
	writeSkill(t, filepath.Join(home, ".niuniu-agent", "skills"), "mail-format",
		"---\nname: mail-format\ndescription: House style for outbound mail.\n---\n\nBe brief.\n")

	// Same name in both scopes: the project skill wins.
	writeSkill(t, filepath.Join(home, ".niuniu-agent", "skills"), "deploy",
		"---\nname: deploy\ndescription: USER COPY — must be shadowed.\n---\n\nx\n")

	got := Scan(cwd)
	if len(got) != 2 {
		t.Fatalf("scan = %+v, want 2 skills", got)
	}
	byName := map[string]Skill{}
	for _, s := range got {
		byName[s.Name] = s
	}
	d, ok := byName["deploy"]
	if !ok {
		t.Fatalf("deploy missing: %+v", got)
	}
	if strings.Contains(d.Description, "shadowed") {
		t.Errorf("project skill must win over user skill: %+v", d)
	}
	if d.Description != "Verify release readiness before shipping." {
		t.Errorf("deploy description = %q", d.Description)
	}
	if !strings.HasSuffix(d.Path, filepath.Join("SKILL.md")) || !strings.Contains(d.Path, "deploy") {
		t.Errorf("deploy path = %q", d.Path)
	}
	if byName["mail-format"].Description != "House style for outbound mail." {
		t.Errorf("mail-format = %+v", byName["mail-format"])
	}
}

func TestScanEmptyAndNamelessFrontmatter(t *testing.T) {
	cwd := t.TempDir()
	// Missing frontmatter entirely: name falls back to the directory name.
	writeSkill(t, filepath.Join(cwd, ".niuniu-agent", "skills"), "bare", "just some instructions\n")
	// Empty dir is not an error.
	os.MkdirAll(filepath.Join(cwd, ".niuniu-agent", "skills", "empty"), 0o755)

	got := Scan(cwd)
	if len(got) != 1 || got[0].Name != "bare" || got[0].Description != "" {
		t.Fatalf("scan = %+v", got)
	}
}

func TestScanNoSkillsDir(t *testing.T) {
	if got := Scan(t.TempDir()); len(got) != 0 {
		t.Errorf("scan = %+v, want empty", got)
	}
}

func TestLoadBodyStripsFrontmatter(t *testing.T) {
	cwd := t.TempDir()
	p := filepath.Join(cwd, ".niuniu-agent", "skills", "deploy", "SKILL.md")
	writeSkill(t, filepath.Join(cwd, ".niuniu-agent", "skills"), "deploy",
		"---\nname: deploy\ndescription: d\n---\n\n# Steps\n\n1. run gates\n")

	body, err := LoadBody(p)
	if err != nil {
		t.Fatalf("LoadBody: %v", err)
	}
	if strings.Contains(body, "name: deploy") || strings.Contains(body, "---\n") {
		t.Errorf("body still carries frontmatter: %q", body)
	}
	if !strings.Contains(body, "1. run gates") {
		t.Errorf("body = %q", body)
	}
}

// —— Skill 工具 ——

func TestToolLoadsByNameAndRejectsUnknown(t *testing.T) {
	cwd := t.TempDir()
	writeSkill(t, filepath.Join(cwd, ".niuniu-agent", "skills"), "deploy",
		"---\nname: deploy\ndescription: Verify release readiness.\n---\n\nRun the gates.\n")

	tool := NewTool(Scan(cwd))
	def := tool.Def()
	if def.Name != "Skill" || def.Description == "" || len(def.InputSchema) == 0 {
		t.Fatalf("def = %+v", def)
	}

	out, err := tool.Execute(context.Background(), json.RawMessage(`{"name":"deploy"}`))
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !strings.Contains(out, "Run the gates.") {
		t.Errorf("out = %q", out)
	}

	_, err = tool.Execute(context.Background(), json.RawMessage(`{"name":"nope"}`))
	if err == nil || !strings.Contains(err.Error(), "deploy") {
		t.Errorf("err = %v, want unknown-name error listing available skills", err)
	}
	// Missing name input is also an error.
	if _, err := tool.Execute(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Error("want error for missing name")
	}
}
