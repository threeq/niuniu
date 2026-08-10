package claudehome

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestReadEmailFromConfigDir_NoFile(t *testing.T) {
	if got := ReadEmailFromConfigDir(t.TempDir()); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestReadEmailFromConfigDir_GoodFile(t *testing.T) {
	tmp := t.TempDir()
	body := `{"oauthAccount":{"emailAddress":"u@example.com","subscriptionType":"pro"}}`
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadEmailFromConfigDir(tmp); got != "u@example.com" {
		t.Errorf("got %q, want u@example.com", got)
	}
}

func TestReadEmailFromConfigDir_MissingField(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"), []byte(`{"otherField":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadEmailFromConfigDir(tmp); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestReadEmailFromConfigDir_BadJSON(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadEmailFromConfigDir(tmp); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}

func TestCredsExistInConfigDir(t *testing.T) {
	tmp := t.TempDir()
	if CredsExistInConfigDir(tmp) {
		t.Error("expected false when missing")
	}
	if err := os.WriteFile(filepath.Join(tmp, ".credentials.json"), []byte(`{"x":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if !CredsExistInConfigDir(tmp) {
		t.Error("expected true after write")
	}
}

func TestReadEmailFromHomeJSON(t *testing.T) {
	homeKey := "HOME"
	if runtime.GOOS == "windows" {
		homeKey = "USERPROFILE"
	}
	t.Setenv(homeKey, t.TempDir())
	if got := ReadEmailFromHomeJSON(); got != "" {
		t.Errorf("expected empty in fresh home, got %q", got)
	}
	body := `{"oauthAccount":{"emailAddress":"home@x.com"}}`
	if err := os.WriteFile(filepath.Join(os.Getenv(homeKey), ".claude.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadEmailFromHomeJSON(); got != "home@x.com" {
		t.Errorf("got %q, want home@x.com", got)
	}
}

func TestReadTierFromConfigDir_NoFile(t *testing.T) {
	sub, rate := ReadTierFromConfigDir(t.TempDir())
	if sub != "" || rate != "" {
		t.Errorf("expected empty pair, got (%q, %q)", sub, rate)
	}
}

func TestReadTierFromConfigDir_StripsClaudePrefix(t *testing.T) {
	tmp := t.TempDir()
	body := `{"oauthAccount":{"organizationType":"claude_max","organizationRateLimitTier":"default_claude_max_20x"}}`
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sub, rate := ReadTierFromConfigDir(tmp)
	if sub != "max" || rate != "default_claude_max_20x" {
		t.Errorf("got (%q, %q), want (max, default_claude_max_20x)", sub, rate)
	}
}

func TestReadTierFromConfigDir_NonClaudePrefixKeptVerbatim(t *testing.T) {
	tmp := t.TempDir()
	body := `{"oauthAccount":{"organizationType":"team","organizationRateLimitTier":"tier_x"}}`
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sub, rate := ReadTierFromConfigDir(tmp)
	if sub != "team" || rate != "tier_x" {
		t.Errorf("got (%q, %q), want (team, tier_x)", sub, rate)
	}
}

func TestReadTierFromConfigDir_MissingFields(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"), []byte(`{"oauthAccount":{"emailAddress":"x@y"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	sub, rate := ReadTierFromConfigDir(tmp)
	if sub != "" || rate != "" {
		t.Errorf("expected empty pair, got (%q, %q)", sub, rate)
	}
}

func TestReadTierFromConfigDir_BadJSON(t *testing.T) {
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, ".claude.json"), []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub, rate := ReadTierFromConfigDir(tmp)
	if sub != "" || rate != "" {
		t.Errorf("expected empty pair, got (%q, %q)", sub, rate)
	}
}

func TestReadTierFromHomeJSON(t *testing.T) {
	homeKey := "HOME"
	if runtime.GOOS == "windows" {
		homeKey = "USERPROFILE"
	}
	t.Setenv(homeKey, t.TempDir())
	sub, rate := ReadTierFromHomeJSON()
	if sub != "" || rate != "" {
		t.Errorf("expected empty in fresh home, got (%q, %q)", sub, rate)
	}
	body := `{"oauthAccount":{"organizationType":"claude_max","organizationRateLimitTier":"default_claude_max_5x"}}`
	if err := os.WriteFile(filepath.Join(os.Getenv(homeKey), ".claude.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	sub, rate = ReadTierFromHomeJSON()
	if sub != "max" || rate != "default_claude_max_5x" {
		t.Errorf("got (%q, %q), want (max, default_claude_max_5x)", sub, rate)
	}
}
