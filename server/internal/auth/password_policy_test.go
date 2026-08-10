package auth

import (
	"testing"
)

func TestPolicy_Validate_DefaultPolicyFields(t *testing.T) {
	p := DefaultPolicy()
	if p.MinLength != 12 {
		t.Errorf("MinLength: got %d, want 12", p.MinLength)
	}
	if !p.DisallowIdentity {
		t.Error("DisallowIdentity: got false, want true")
	}
	if p.ComplexityClasses != 0 {
		t.Errorf("ComplexityClasses: got %d, want 0", p.ComplexityClasses)
	}
}

func TestPolicy_Validate_TooShort(t *testing.T) {
	tests := []struct {
		name     string
		password string
		wantRule string
	}{
		{"three chars", "abc", "min_length"},
		{"eleven chars", "abcdefghijk", "min_length"},
		{"exactly twelve", "abcdefghijkl", ""},
	}
	p := DefaultPolicy()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.Validate(tt.password, Identity{})
			if tt.wantRule == "" {
				if err != nil {
					if pe, ok := err.(*PasswordError); ok {
						for _, r := range pe.FailedRules {
							if r == "min_length" {
								t.Errorf("unexpected min_length failure for %q", tt.password)
							}
						}
					}
				}
				return
			}
			assertRuleFailed(t, err, tt.wantRule)
		})
	}
}

func TestPolicy_Validate_ContainsUsername(t *testing.T) {
	tests := []struct {
		name     string
		password string
		id       Identity
		wantFail bool
	}{
		{
			name:     "password contains username case-insensitive",
			password: "MyPassword-alice-2024",
			id:       Identity{Username: "Alice"},
			wantFail: true,
		},
		{
			name:     "password does not contain username",
			password: "completely-different-pw-1!",
			id:       Identity{Username: "Alice"},
			wantFail: false,
		},
		{
			name:     "exact match lowercased",
			password: "alice12345678!",
			id:       Identity{Username: "Alice"},
			wantFail: true,
		},
	}
	p := Policy{MinLength: 1, DisallowIdentity: true, ComplexityClasses: 0}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.Validate(tt.password, tt.id)
			if tt.wantFail {
				assertRuleFailed(t, err, "identity")
			} else {
				assertRuleNotFailed(t, err, "identity")
			}
		})
	}
}

func TestPolicy_Validate_ShortUsernameSkipped(t *testing.T) {
	tests := []struct {
		name     string
		password string
		id       Identity
	}{
		{
			name:     "two-char username Li is skipped",
			password: "sliding-window-pw-1",
			id:       Identity{Username: "Li"},
		},
		{
			name:     "one-char username is skipped",
			password: "apassword-that-has-a-in-it",
			id:       Identity{Username: "a"},
		},
	}
	p := Policy{MinLength: 1, DisallowIdentity: true, ComplexityClasses: 0}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.Validate(tt.password, tt.id)
			assertRuleNotFailed(t, err, "identity")
		})
	}
}

func TestPolicy_Validate_ShortDisplayNameSkipped(t *testing.T) {
	tests := []struct {
		name     string
		password string
		id       Identity
	}{
		{
			name:     "two-char display name Li is skipped",
			password: "sliding-li-pw-long",
			id:       Identity{DisplayName: "Li"},
		},
		{
			name:     "one-char display name is skipped",
			password: "apassword-that-has-a",
			id:       Identity{DisplayName: "a"},
		},
	}
	p := Policy{MinLength: 1, DisallowIdentity: true, ComplexityClasses: 0}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.Validate(tt.password, tt.id)
			assertRuleNotFailed(t, err, "identity")
		})
	}
}

func TestPolicy_Validate_DisabledIdentityCheckPasses(t *testing.T) {
	tests := []struct {
		name     string
		password string
		id       Identity
	}{
		{
			name:     "contains username but check disabled",
			password: "alice-longpassword",
			id:       Identity{Username: "alice"},
		},
		{
			name:     "contains display name but check disabled",
			password: "bobsmith-longpassword",
			id:       Identity{DisplayName: "bobsmith"},
		},
	}
	p := Policy{MinLength: 1, DisallowIdentity: false, ComplexityClasses: 0}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.Validate(tt.password, tt.id)
			assertRuleNotFailed(t, err, "identity")
		})
	}
}

func TestPolicy_Validate_Complexity(t *testing.T) {
	tests := []struct {
		name              string
		password          string
		complexityClasses int
		wantFail          bool
	}{
		{
			name:              "all lowercase fails 3-class requirement",
			password:          "abcdefghijklmnop",
			complexityClasses: 3,
			wantFail:          true,
		},
		{
			name:              "lower+digit+symbol passes 3-class requirement",
			password:          "abc123!@#defghij",
			complexityClasses: 3,
			wantFail:          false,
		},
		{
			name:              "lower+upper passes 2-class requirement",
			password:          "abcdefghijklABCD",
			complexityClasses: 2,
			wantFail:          false,
		},
		{
			name:              "lower+upper fails 3-class requirement",
			password:          "abcdefghijklABCD",
			complexityClasses: 3,
			wantFail:          true,
		},
		{
			name:              "all four classes passes any requirement",
			password:          "abcABC123!@#",
			complexityClasses: 4,
			wantFail:          false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := Policy{MinLength: 1, DisallowIdentity: false, ComplexityClasses: tt.complexityClasses}
			err := p.Validate(tt.password, Identity{})
			if tt.wantFail {
				assertRuleFailed(t, err, "complexity")
			} else {
				assertRuleNotFailed(t, err, "complexity")
			}
		})
	}
}

func TestPolicy_Validate_ComplexityDisabledPassesAnything(t *testing.T) {
	tests := []struct {
		name     string
		password string
	}{
		{"only lowercase", "abcdefghijklmnop"},
		{"only digits", "1234567890123456"},
		{"only symbols", "!@#$%^&*()!@#$%^"},
		{"spaces only — no class", "                "},
	}
	p := Policy{MinLength: 1, DisallowIdentity: false, ComplexityClasses: 0}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := p.Validate(tt.password, Identity{})
			assertRuleNotFailed(t, err, "complexity")
		})
	}
}

func TestPolicy_Validate_MultipleRulesReturnedAtOnce(t *testing.T) {
	// Too short AND contains username must both appear in FailedRules.
	p := Policy{MinLength: 12, DisallowIdentity: true, ComplexityClasses: 0}
	err := p.Validate("alice", Identity{Username: "alice"})
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	pe, ok := err.(*PasswordError)
	if !ok {
		t.Fatalf("expected *PasswordError, got %T", err)
	}
	assertContains(t, pe.FailedRules, "min_length")
	assertContains(t, pe.FailedRules, "identity")
	if len(pe.FailedRules) < 2 {
		t.Errorf("expected at least 2 failed rules, got %v", pe.FailedRules)
	}
}

func TestPolicy_Validate_FullHappyPath(t *testing.T) {
	// 12 rune password, no identity match, 3 complexity classes — must pass.
	p := Policy{MinLength: 12, DisallowIdentity: true, ComplexityClasses: 3}
	err := p.Validate("Secure!pass9word", Identity{Username: "alice", DisplayName: "Alice Smith"})
	if err != nil {
		t.Errorf("expected nil error, got: %v", err)
	}
}

func TestPolicy_Validate_RuneLengthUnicode(t *testing.T) {
	// 12 Chinese characters: each is 3 UTF-8 bytes, so byte length is 36, but rune count is 12.
	// Must pass MinLength=12 since we count runes, not bytes.
	p := Policy{MinLength: 12, DisallowIdentity: false, ComplexityClasses: 0}
	password := "一二三四五六七八九十十一十二" // 13 runes — clearly >= 12
	err := p.Validate(password, Identity{})
	assertRuleNotFailed(t, err, "min_length")

	// Exactly 12 Chinese characters must also pass.
	exactly12 := "一二三四五六七八九十甲乙"
	err = p.Validate(exactly12, Identity{})
	assertRuleNotFailed(t, err, "min_length")

	// 11 Chinese characters must fail.
	only11 := "一二三四五六七八九十甲"
	err = p.Validate(only11, Identity{})
	assertRuleFailed(t, err, "min_length")
}

// assertRuleFailed verifies that err is a *PasswordError containing rule.
func assertRuleFailed(t *testing.T, err error, rule string) {
	t.Helper()
	if err == nil {
		t.Errorf("expected rule %q to fail, but got nil error", rule)
		return
	}
	pe, ok := err.(*PasswordError)
	if !ok {
		t.Errorf("expected *PasswordError, got %T: %v", err, err)
		return
	}
	assertContains(t, pe.FailedRules, rule)
}

// assertRuleNotFailed verifies that rule does NOT appear in the error (if any).
func assertRuleNotFailed(t *testing.T, err error, rule string) {
	t.Helper()
	if err == nil {
		return
	}
	pe, ok := err.(*PasswordError)
	if !ok {
		// Non-PasswordError is not caused by this rule.
		return
	}
	for _, r := range pe.FailedRules {
		if r == rule {
			t.Errorf("rule %q should not have failed, but FailedRules = %v", rule, pe.FailedRules)
			return
		}
	}
}

func assertContains(t *testing.T, slice []string, item string) {
	t.Helper()
	for _, s := range slice {
		if s == item {
			return
		}
	}
	t.Errorf("expected %v to contain %q", slice, item)
}
