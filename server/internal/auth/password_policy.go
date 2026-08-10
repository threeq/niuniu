package auth

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Policy describes password strength requirements.
type Policy struct {
	MinLength         int  // default 12
	DisallowIdentity  bool // true: password must not contain username/displayName (case-insensitive)
	ComplexityClasses int  // 0 = disabled; N = password must use at least N of: lower/upper/digit/symbol
	// TODO: HIBP breach-password check — reserved for future implementation; must not introduce network I/O
}

// DefaultPolicy returns secure defaults: MinLength=12, DisallowIdentity=true, ComplexityClasses=0.
func DefaultPolicy() Policy {
	return Policy{
		MinLength:        12,
		DisallowIdentity: true,
		ComplexityClasses: 0,
	}
}

// Identity holds user-specific strings that must not appear in the password.
// Fields may be empty; they are simply skipped when checking.
type Identity struct {
	Username    string
	DisplayName string
}

// PasswordError is returned when one or more policy rules are violated.
// FailedRules lists every failing rule name from {"min_length", "identity", "complexity"}.
type PasswordError struct {
	FailedRules []string
}

func (e *PasswordError) Error() string {
	return fmt.Sprintf("password policy violated: %s", strings.Join(e.FailedRules, ", "))
}

// Validate checks password against the policy and the caller's identity.
// All failing rules are collected before returning, so callers get the full picture at once.
func (p Policy) Validate(password string, id Identity) error {
	var failed []string

	if utf8.RuneCountInString(password) < p.MinLength {
		failed = append(failed, "min_length")
	}

	if p.DisallowIdentity {
		lower := strings.ToLower(password)
		// Skip identifiers shorter than 3 runes — common surnames like "Li" would cause too many
		// false positives if included, outweighing the security benefit.
		if len([]rune(id.Username)) >= 3 && strings.Contains(lower, strings.ToLower(id.Username)) {
			failed = append(failed, "identity")
		} else if len([]rune(id.DisplayName)) >= 3 && strings.Contains(lower, strings.ToLower(id.DisplayName)) {
			failed = append(failed, "identity")
		}
	}

	if p.ComplexityClasses > 0 {
		classes := countComplexityClasses(password)
		if classes < p.ComplexityClasses {
			failed = append(failed, "complexity")
		}
	}

	if len(failed) > 0 {
		return &PasswordError{FailedRules: failed}
	}
	return nil
}

// countComplexityClasses returns how many of the four character classes appear in s.
// Classes: lower (a-z), upper (A-Z), digit (0-9), symbol (any other printable non-space).
// Spaces are intentionally excluded from all classes.
func countComplexityClasses(s string) int {
	var hasLower, hasUpper, hasDigit, hasSymbol bool
	for _, r := range s {
		switch {
		case unicode.IsLower(r):
			hasLower = true
		case unicode.IsUpper(r):
			hasUpper = true
		case unicode.IsDigit(r):
			hasDigit = true
		case unicode.IsPrint(r) && !unicode.IsSpace(r):
			hasSymbol = true
		}
	}
	count := 0
	for _, has := range []bool{hasLower, hasUpper, hasDigit, hasSymbol} {
		if has {
			count++
		}
	}
	return count
}
