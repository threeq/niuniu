package main

import (
	"regexp"
	"testing"
)

func TestSlugifyAdminCLI(t *testing.T) {
	cases := map[string]string{
		"Acme":     "acme",
		"Acme Inc": "acme-inc",
		"My Team!": "my-team",
	}
	for in, want := range cases {
		if got := slugifyCLI(in); got != want {
			t.Errorf("slugify(%q) = %q; want %q", in, got, want)
		}
	}

	// Names with no ASCII slug characters (whitespace-only, CJK) fall back to a
	// stable hash-based slug (org-xxxxxxxx) instead of all colliding on
	// "default" — see commit 0cce1982.
	hashSlug := regexp.MustCompile(`^org-[0-9a-f]{8}$`)
	for _, in := range []string{"   ", "牛牛"} {
		got := slugifyCLI(in)
		if !hashSlug.MatchString(got) {
			t.Errorf("slugify(%q) = %q; want hash-based slug org-xxxxxxxx", in, got)
		}
		if again := slugifyCLI(in); again != got {
			t.Errorf("slugify(%q) not stable: %q != %q", in, got, again)
		}
	}
}
