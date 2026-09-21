package model

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderProfilesListsAndMarks(t *testing.T) {
	dir := t.TempDir()
	// LoadChain 找 dir/.niuniu-agent/config.json——写到正确位置。
	projCfgDir := filepath.Join(dir, ".niuniu-agent")
	if err := os.MkdirAll(projCfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projCfgDir, "config.json"), []byte(glmProfileJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	clearProfileEnv(t)
	t.Setenv("GLM_TOKEN", "set-value")

	active := Config{Provider: ProviderAnthropic, Model: "GLM-5.3-Flash",
		BaseURL: "https://open.bigmodel.cn/api/anthropic", AuthToken: "set-value"}
	out := RenderProfiles(dir, "", active)

	if !strings.Contains(out, "glm") || !strings.Contains(out, "local") {
		t.Fatalf("render = %s", out)
	}
	if !strings.Contains(out, "GLM_TOKEN(set)") {
		t.Errorf("credential state missing:\n%s", out)
	}
	if !strings.Contains(out, "LOCAL_KEY(unset)") {
		t.Errorf("unset credential state missing:\n%s", out)
	}
	if !strings.Contains(out, "← active") {
		t.Errorf("active marker missing:\n%s", out)
	}
	// 明文凭据值不得出现。
	if strings.Contains(out, "set-value") {
		t.Errorf("credential value leaked into listing:\n%s", out)
	}
}

func TestLoadChainConfigFlagWins(t *testing.T) {
	dir := t.TempDir()
	flagPath := filepath.Join(dir, "flagcfg.json")
	if err := os.WriteFile(flagPath, []byte(`{"defaultProfile":"flagcfg","profiles":{"flagcfg":{"provider":"openai","model":"fm"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	proj := writeProfile(t, dir, `{"defaultProfile":"projcfg","profiles":{"projcfg":{"provider":"openai","model":"pm"}}}`)

	projData, glob, gotPath := LoadChain(filepath.Dir(proj), flagPath, "")
	if gotPath != flagPath {
		t.Errorf("path = %q, want flag path", gotPath)
	}
	pf, err := ParseProfileFile(projData)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pf.Profiles["flagcfg"]; !ok {
		t.Errorf("flag config not loaded: %+v", pf)
	}
	_ = glob
}
