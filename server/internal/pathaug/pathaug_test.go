package pathaug

import (
	"strings"
	"testing"
)

func TestMergeMissing_AppendsNewKeepsOrder(t *testing.T) {
	base := "/usr/bin:/bin"
	got := mergeMissing(base, []string{"/opt/homebrew/bin", "/bin", "/usr/local/bin"})
	want := "/usr/bin:/bin:/opt/homebrew/bin:/usr/local/bin"
	if got != want {
		t.Errorf("mergeMissing = %q, want %q", got, want)
	}
}

func TestMergeMissing_NoDuplicatesWithinAdditions(t *testing.T) {
	got := mergeMissing("/bin", []string{"/a", "/a", "/b", "/a"})
	want := "/bin:/a:/b"
	if got != want {
		t.Errorf("mergeMissing = %q, want %q", got, want)
	}
}

func TestMergeMissing_EmptyAdditionsUnchanged(t *testing.T) {
	if got := mergeMissing("/usr/bin:/bin", nil); got != "/usr/bin:/bin" {
		t.Errorf("mergeMissing = %q, want unchanged", got)
	}
}

func TestLatestNvmBin_NumericVersionOrder(t *testing.T) {
	// Lexical compare would pick v9 over v20; the numeric compare must not.
	dirs := []string{
		"/Users/u/.nvm/versions/node/v18.0.0/bin",
		"/Users/u/.nvm/versions/node/v20.11.1/bin",
		"/Users/u/.nvm/versions/node/v9.10.0/bin",
	}
	if got := latestNvmBin(dirs); got != dirs[1] {
		t.Errorf("latestNvmBin = %q, want %q", got, dirs[1])
	}
}

func TestLatestNvmBin_IgnoresNonVersionDirs(t *testing.T) {
	dirs := []string{"/Users/u/.nvm/versions/node/garbage/bin", "/Users/u/.nvm/versions/node/v16.5.0/bin"}
	if got := latestNvmBin(dirs); got != dirs[1] {
		t.Errorf("latestNvmBin = %q, want %q", got, dirs[1])
	}
	if got := latestNvmBin(nil); got != "" {
		t.Errorf("latestNvmBin(nil) = %q, want empty", got)
	}
}

func TestParseShellPathOutput_PicksPathLineFromRcNoise(t *testing.T) {
	// Interactive rc files print banners/prompts before the echo lands.
	out := "last login from tty\np10k instant prompt junk >>\n/usr/local/bin:/opt/homebrew/bin:/Users/u/.nvm/versions/node/v20.11.1/bin\n"
	got, ok := parseShellPathOutput(out)
	if !ok {
		t.Fatal("expected PATH line detected")
	}
	want := "/usr/local/bin:/opt/homebrew/bin:/Users/u/.nvm/versions/node/v20.11.1/bin"
	if got != want {
		t.Errorf("parseShellPathOutput = %q, want %q", got, want)
	}
}

func TestParseShellPathOutput_NoPlausibleLine(t *testing.T) {
	if _, ok := parseShellPathOutput("total junk\nno paths here\n"); ok {
		t.Error("expected no detection for output without a PATH-looking line")
	}
}

func TestShellCandidates_SHELLFirstThenDefaults(t *testing.T) {
	cands := shellCandidates(func(k string) string {
		if k == "SHELL" {
			return "/usr/bin/fish"
		}
		return ""
	}, func() string { return "" }, func(string) bool { return true })
	if len(cands) != 3 {
		t.Fatalf("candidates = %d, want 3", len(cands))
	}
	if cands[0].bin != "/usr/bin/fish" {
		t.Errorf("first candidate = %q, want SHELL (/usr/bin/fish)", cands[0].bin)
	}
	if cands[0].args[len(cands[0].args)-1] != "string join : $PATH" {
		t.Errorf("fish args = %v, want fish-specific PATH expression", cands[0].args)
	}
	if cands[1].bin != "/bin/zsh" || cands[2].bin != "/bin/bash" {
		t.Errorf("fallbacks = %q, %q; want zsh then bash", cands[1].bin, cands[2].bin)
	}
}

func TestShellCandidates_DSDefaultShellBeforeStaticFallbacks(t *testing.T) {
	// GUI processes have no $SHELL; the Directory Services default must rank
	// ahead of the static zsh/bash fallbacks (a bash user would otherwise be
	// probed via zsh first).
	cands := shellCandidates(func(string) string { return "" },
		func() string { return "/bin/bash" }, func(string) bool { return true })
	if len(cands) != 2 {
		t.Fatalf("candidates = %d, want 2", len(cands))
	}
	if cands[0].bin != "/bin/bash" {
		t.Errorf("first candidate = %q, want dscl default (/bin/bash)", cands[0].bin)
	}
	if cands[1].bin != "/bin/zsh" {
		t.Errorf("second candidate = %q, want /bin/zsh", cands[1].bin)
	}
}

func TestShellCandidates_DedupesAndSkipsMissing(t *testing.T) {
	cands := shellCandidates(func(k string) string {
		if k == "SHELL" {
			return "/bin/zsh"
		}
		return ""
	}, func() string { return "" }, func(p string) bool { return p == "/bin/zsh" }) // only zsh "exists"
	if len(cands) != 1 || cands[0].bin != "/bin/zsh" {
		t.Errorf("candidates = %+v, want exactly [/bin/zsh]", cands)
	}
}

func TestShellCandidates_NoShellEnvStillDefaults(t *testing.T) {
	cands := shellCandidates(func(string) string { return "" },
		func() string { return "" }, func(string) bool { return true })
	if len(cands) != 2 || cands[0].bin != "/bin/zsh" || cands[1].bin != "/bin/bash" {
		t.Errorf("candidates = %+v, want [/bin/zsh /bin/bash]", cands)
	}
}

func TestParseUserShellOutput(t *testing.T) {
	if got := parseUserShellOutput("UserShell: /bin/zsh\n"); got != "/bin/zsh" {
		t.Errorf("parseUserShellOutput = %q, want /bin/zsh", got)
	}
	if got := parseUserShellOutput("no such user\n"); got != "" {
		t.Errorf("parseUserShellOutput = %q, want empty", got)
	}
}

func TestArgsForShell_PerFamily(t *testing.T) {
	cases := []struct {
		shell    string
		wantFish bool
	}{
		{"/bin/zsh", false},
		{"/bin/bash", false},
		{"/bin/sh", false},
		{"/bin/ksh", false},
		{"/bin/tcsh", false},
		{"/bin/csh", false},
		{"/usr/local/bin/oh-my-zsh-custom", false},
		{"/opt/homebrew/bin/fish", true},
		{"/usr/bin/fish", true},
	}
	for _, c := range cases {
		args := argsForShell(c.shell)
		joined := strings.Join(args, " ")
		if c.wantFish != strings.Contains(joined, "string join : $PATH") {
			t.Errorf("argsForShell(%q) = %v, fish expression presence = %v", c.shell, args, c.wantFish)
		}
	}
}

func TestBuildAdditions_ExistingDirsOnly(t *testing.T) {
	exists := func(p string) bool { return p != "/usr/local/sbin" && p != "/Users/u/.local/bin" }
	got := buildAdditions("/Users/u", exists, "/Users/u/.nvm/versions/node/v20.11.1/bin")
	want := []string{
		"/opt/homebrew/bin", "/opt/homebrew/sbin", "/usr/local/bin",
		"/Users/u/.nvm/versions/node/v20.11.1/bin", "/Users/u/.volta/bin",
	}
	if len(got) != len(want) {
		t.Fatalf("buildAdditions = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("buildAdditions[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
