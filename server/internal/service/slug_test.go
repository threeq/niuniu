package service

import (
	"errors"
	"regexp"
	"strings"
	"testing"
)

func TestDeriveSlug(t *testing.T) {
	cases := []struct {
		name string
		in   string
		// when fixed is empty, want is checked via wantPattern instead
		want        string
		wantPattern *regexp.Regexp
	}{
		{name: "ascii lower", in: "acme", want: "acme"},
		{name: "ascii mixed case", in: "Acme Corp", want: "acme-corp"},
		{name: "ascii with digits", in: "My Team 2026", want: "my-team-2026"},
		{name: "ascii with punctuation", in: "My Team!!!", want: "my-team"},
		{name: "ascii leading/trailing punct", in: "---hello---", want: "hello"},
		{name: "collapsed dashes", in: "a   b   c", want: "a-b-c"},
		{name: "chinese mixed with ascii", in: "测试 Team", want: "team"},
		{
			name:        "all chinese falls back to random",
			in:          "测试组织",
			wantPattern: regexp.MustCompile(`^org-[0-9a-f]{6}$`),
		},
		{
			name:        "empty input falls back to random",
			in:          "",
			wantPattern: regexp.MustCompile(`^org-[0-9a-f]{6}$`),
		},
		{
			name:        "all punctuation falls back to random",
			in:          "---",
			wantPattern: regexp.MustCompile(`^org-[0-9a-f]{6}$`),
		},
		{
			name: "very long name truncated to 48",
			in:   strings.Repeat("a", 60),
			want: strings.Repeat("a", 48),
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveSlug(tc.in)
			if tc.wantPattern != nil {
				if !tc.wantPattern.MatchString(got) {
					t.Fatalf("deriveSlug(%q) = %q, want match %s", tc.in, got, tc.wantPattern)
				}
				return
			}
			if got != tc.want {
				t.Fatalf("deriveSlug(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestIsSlugUniqueViolation(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "unrelated error", err: errors.New("connection refused"), want: false},
		{name: "unique on different column", err: errors.New("UNIQUE constraint failed: users.email"), want: false},
		{name: "sqlite UNIQUE constraint failed",
			err:  errors.New("UNIQUE constraint failed: organizations.slug"),
			want: true},
		{name: "pg duplicate key with sqlstate",
			err:  errors.New(`ERROR: duplicate key value violates unique constraint "organizations_slug_key" (SQLSTATE 23505)`),
			want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := isSlugUniqueViolation(tc.err)
			if got != tc.want {
				t.Fatalf("isSlugUniqueViolation(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
