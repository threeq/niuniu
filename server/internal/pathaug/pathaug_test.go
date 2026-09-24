package pathaug

import "testing"

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
