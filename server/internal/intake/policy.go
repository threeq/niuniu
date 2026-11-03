package intake

import (
	"strings"
	"unicode"
)

// Mode is the per project+source intake mode.
type Mode string

const (
	// ModeOff processes explicit requests only; inferred candidates are not
	// evaluated.
	ModeOff Mode = "off"
	// ModeObserve executes explicit requests and records inferred candidates as
	// dry-run decisions without creating anything.
	ModeObserve Mode = "observe"
	// ModeAuto executes requested work and inferred work inside rules and cap.
	ModeAuto Mode = "auto"
)

// Strength is the authorization strength of an actor for one trigger.
type Strength string

const (
	StrengthExplicit Strength = "explicit"
	StrengthInferred Strength = "inferred"
	StrengthDenied   Strength = "denied"
)

// Reason codes recorded on every decision. They are stable identifiers, not
// user-facing text.
const (
	ReasonAllowList   = "allow_list"
	ReasonBotList     = "bot_list"
	ReasonRule        = "rule"
	ReasonDenied      = "denied"
	ReasonDuplicate   = "duplicate"
	ReasonObserved    = "observed"
	ReasonAccepted    = "accepted"
	ReasonResumed     = "resumed"
	ReasonOffInferred = "off_inferred"
	ReasonDailyCap    = "daily_cap"
	ReasonDisabled    = "disabled"
)

// RuleConditions narrows when a rule applies. Every non-empty group must match;
// keywords match when ANY keyword is contained in the trigger text. A rule with
// no conditions never matches — an empty rule is not a catch-all.
type RuleConditions struct {
	Actors   []string `json:"actors,omitempty"`
	Keywords []string `json:"keywords,omitempty"`
}

// Rule is one human-authored intake rule.
type Rule struct {
	ID          int64
	Name        string
	Instruction string
	Priority    int64
	Enabled     bool
	Conditions  RuleConditions
}

// Policy is the effective intake policy for one project+source.
//
// Rules must be ordered highest priority first; Evaluate stops at the first
// match. AllowList and BotList hold external actor ids.
type Policy struct {
	ProjectID  int64
	SourceKind string
	Version    int64
	Mode       Mode
	DailyCap   int64
	AllowList  []string
	BotList    []string
	Rules      []Rule
}

// Authorization is the result of evaluating a policy for a trigger.
type Authorization struct {
	Strength       Strength
	MatchedRuleIDs []int64
	ReasonCode     string
}

// Evaluate decides how strongly the actor is authorized for this trigger.
//
// Order matters: an allow-listed actor is explicit even if it is also a bot or
// matches a rule. Rule-matched and bot actors are inferred, never explicit.
// Actor ids from the message text play no part in this decision.
func (p Policy) Evaluate(t Trigger) Authorization {
	if contains(p.AllowList, t.Actor.ExtID) {
		return Authorization{Strength: StrengthExplicit, ReasonCode: ReasonAllowList}
	}
	if t.Actor.IsBot && contains(p.BotList, t.Actor.ExtID) {
		return Authorization{Strength: StrengthInferred, ReasonCode: ReasonBotList}
	}
	for _, r := range p.Rules {
		if r.matches(t) {
			return Authorization{
				Strength:       StrengthInferred,
				MatchedRuleIDs: []int64{r.ID},
				ReasonCode:     ReasonRule,
			}
		}
	}
	return Authorization{Strength: StrengthDenied, ReasonCode: ReasonDenied}
}

func (r Rule) matches(t Trigger) bool {
	if !r.Enabled {
		return false
	}
	if len(r.Conditions.Actors) == 0 && len(r.Conditions.Keywords) == 0 {
		return false
	}
	if len(r.Conditions.Actors) > 0 && !contains(r.Conditions.Actors, t.Actor.ExtID) {
		return false
	}
	if len(r.Conditions.Keywords) > 0 && !containsKeyword(t.Text, r.Conditions.Keywords) {
		return false
	}
	return true
}

func contains(list []string, want string) bool {
	for _, v := range list {
		if v == want {
			return true
		}
	}
	return false
}

func containsKeyword(text string, keywords []string) bool {
	lower := strings.ToLower(text)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw == "" {
			continue
		}
		// A Latin keyword must match a whole token: "deploy" must not match
		// "undeployed" or "deployment", or attacker-controlled prose could earn
		// inferred authorization by embedding the keyword. CJK has no word
		// boundaries, so those keywords fall back to substring matching.
		if isASCIIWord(kw) {
			if containsWord(lower, kw) {
				return true
			}
			continue
		}
		if strings.Contains(lower, kw) {
			return true
		}
	}
	return false
}

func isASCIIWord(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r > unicode.MaxASCII || !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// containsWord reports whether word occurs in text delimited by non-word
// characters. text is expected to be lowercased.
func containsWord(text, word string) bool {
	for start := 0; start <= len(text)-len(word); {
		i := strings.Index(text[start:], word)
		if i < 0 {
			return false
		}
		i += start
		end := i + len(word)
		beforeOK := i == 0 || !isWordByte(text[i-1])
		afterOK := end == len(text) || !isWordByte(text[end])
		if beforeOK && afterOK {
			return true
		}
		start = i + 1
	}
	return false
}

func isWordByte(b byte) bool {
	return b == '_' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}
