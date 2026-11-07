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
	ReasonDirect      = "direct_request"
	ReasonUnlisted    = "unlisted_actor"
	ReasonDenied      = "denied"
	ReasonDuplicate   = "duplicate"
	ReasonObserved    = "observed"
	ReasonAccepted    = "accepted"
	ReasonResumed     = "resumed"
	ReasonOffInferred = "off_inferred"
	ReasonNoRule      = "no_matching_rule"
	ReasonDailyCap    = "daily_cap"
	ReasonDisabled    = "disabled"
	ReasonThreadEnded = "thread_ended"
	// ReasonStale marks a trigger that arrived too late to act on (a backlog
	// replay after a reconnect, or a platform redelivery). Recorded for audit,
	// never executed.
	ReasonStale = "stale"
	// ReasonUnbound marks a require-bound event (CI success/recovery/cancel)
	// whose thread is not bound to a task. There is nothing to resume, so the
	// event is recorded and ignored instead of creating new work.
	ReasonUnbound = "unbound"
)

// RuleConditions narrows when a rule applies. Every non-empty group must match;
// keywords match when ANY keyword is contained in the trigger text. A rule with
// no conditions never matches — an empty rule is not a catch-all.
//
// EventKind selects machine events by their normalized kind (for CI:
// ci_failed / ci_succeeded / ci_recovered / ci_canceled), so a rule can target a
// whole class of events instead of guessing at their text.
type RuleConditions struct {
	EventKind string   `json:"event_kind,omitempty"`
	Actors    []string `json:"actors,omitempty"`
	Keywords  []string `json:"keywords,omitempty"`
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
	// Paused is the project-level stop switch. When set the pipeline treats the
	// mode as off regardless of source or chat overrides: inferred/observe work
	// stops immediately while explicit requests keep working.
	Paused    bool
	DailyCap  int64
	AllowList []string
	BotList   []string
	Rules     []Rule
}

// Authorization is the result of evaluating a policy for a trigger.
type Authorization struct {
	Strength       Strength
	MatchedRuleIDs []int64
	ReasonCode     string
}

// Evaluate decides how strongly the actor is authorized for this trigger.
//
// Order matters:
//
//  1. A bot is inferred only when it is on BotList; botness is a fact, not a
//     grant, so a bot outside the list is denied. A bot never becomes explicit —
//     not through the allow list, and not through Trigger.Explicit (source
//     admission). "A bot must not stop tasks" is absolute.
//  2. An allow-listed actor is explicit. This is the only actor-level grant.
//  3. When no allow list is configured, the source's own admission is still the
//     authorization boundary (an approved, project-bound IM chat) — existing
//     behaviour. Once any allow list entry exists, only listed actors are
//     explicit and unlisted actors fall through to the inferred paths.
//  4. A matching rule makes an unlisted actor inferred.
//  5. An unlisted actor in a receive-all conversation (Trigger.ReceiveAll) is an
//     inferred candidate; otherwise denied.
//
// Actor ids from the message text play no part in this decision.
//
// Keyword rules match normalized tokens as a consecutive sequence, so Latin
// keywords cannot be smuggled inside a longer word or a different word order.
// CJK keywords are the one deliberate substring exception — the script has no
// word boundaries. Inferred work only executes in auto mode, inside a matching
// rule, and is bounded by DailyCap.
func (p Policy) Evaluate(t Trigger) Authorization {
	if p.isBot(t) {
		if p.actorIn(p.BotList, t.Actor.ExtID) {
			// Bots are scope-limited like every inferred actor: attach the
			// matching rule (if any) so auto mode can require it before acting.
			return Authorization{
				Strength:       StrengthInferred,
				MatchedRuleIDs: p.matchedRuleIDs(t),
				ReasonCode:     ReasonBotList,
			}
		}
		return Authorization{Strength: StrengthDenied, ReasonCode: ReasonDenied}
	}
	if p.actorIn(p.AllowList, t.Actor.ExtID) {
		return Authorization{Strength: StrengthExplicit, ReasonCode: ReasonAllowList}
	}
	if len(p.AllowList) == 0 && t.Explicit {
		return Authorization{Strength: StrengthExplicit, ReasonCode: ReasonDirect}
	}
	for _, r := range p.Rules {
		if r.matches(p, t) {
			return Authorization{
				Strength:       StrengthInferred,
				MatchedRuleIDs: []int64{r.ID},
				ReasonCode:     ReasonRule,
			}
		}
	}
	if t.ReceiveAll {
		// A source that reads a whole scope (an IM observe chat, a polled
		// repository) treats an unlisted actor as an inferred candidate. This is
		// the design §6.2 "未列名单的人，在开启接收全部消息的范围" row; auto mode
		// still requires a matching rule, so it is not an open grant.
		return Authorization{Strength: StrengthInferred, ReasonCode: ReasonUnlisted}
	}
	return Authorization{Strength: StrengthDenied, ReasonCode: ReasonDenied}
}

// AllowsHighRiskAction reports whether this trigger may stop a task or trigger
// another high-risk action. Only explicit (allow-listed or, before any allow
// list exists, source-admitted) non-bot actors qualify. A bot is never allowed
// — including a bot that was also placed on the allow list, because "a bot must
// not stop tasks" is absolute.
func (p Policy) AllowsHighRiskAction(t Trigger) bool {
	if p.isBot(t) {
		return false
	}
	return p.Evaluate(t).Strength == StrengthExplicit
}

// isBot treats either adapter-reported botness or bot-list membership as a bot.
// BotList is how IM (whose payloads carry no bot flag) marks an actor as a bot.
func (p Policy) isBot(t Trigger) bool {
	return t.Actor.IsBot || p.actorIn(p.BotList, t.Actor.ExtID)
}

// actorIn reports whether actor is in a configured actor list. Matching goes
// through actorKey, so GitHub logins compare case-insensitively and accept the
// "@login" form; every other source's ids remain opaque exact strings. An empty
// actor id never matches — it cannot inherit a grant from a blank list entry.
func (p Policy) actorIn(list []string, actor string) bool {
	want := p.actorKey(actor)
	if want == "" {
		return false
	}
	for _, v := range list {
		if p.actorKey(v) == want {
			return true
		}
	}
	return false
}

// actorKey canonicalizes one external actor id for comparison. GitHub logins
// are case-insensitive and are commonly written with a leading "@", so both
// forms map to the same key. Other sources' ids are opaque and exact.
func (p Policy) actorKey(raw string) string {
	id := strings.TrimSpace(raw)
	if p.SourceKind != SourceKindGitHub {
		return id
	}
	id = strings.TrimPrefix(id, "@")
	return strings.ToLower(id)
}

// matchedRuleIDs returns the highest-priority matching rule, as a single-element
// slice for the decision record. Evaluation stops at the first match.
func (p Policy) matchedRuleIDs(t Trigger) []int64 {
	for _, r := range p.Rules {
		if r.matches(p, t) {
			return []int64{r.ID}
		}
	}
	return nil
}

func (r Rule) matches(p Policy, t Trigger) bool {
	if !r.Enabled {
		return false
	}
	if r.Conditions.EventKind == "" && len(r.Conditions.Actors) == 0 && len(r.Conditions.Keywords) == 0 {
		return false
	}
	if r.Conditions.EventKind != "" &&
		!strings.EqualFold(strings.TrimSpace(r.Conditions.EventKind), strings.TrimSpace(t.EventKind)) {
		return false
	}
	if len(r.Conditions.Actors) > 0 && !p.actorIn(r.Conditions.Actors, t.Actor.ExtID) {
		return false
	}
	if len(r.Conditions.Keywords) > 0 && !containsKeyword(t.Text, r.Conditions.Keywords) {
		return false
	}
	return true
}

func containsKeyword(text string, keywords []string) bool {
	lower := strings.ToLower(text)
	tokens := tokenize(lower)
	for _, kw := range keywords {
		kw = strings.ToLower(strings.TrimSpace(kw))
		if kw == "" {
			continue
		}
		// CJK has no word boundaries, so those keywords are the one deliberate
		// substring case (documented in Policy.Evaluate). Everything else —
		// including multi-word and punctuated Latin phrases — is normalized to
		// tokens and must match as a consecutive sequence. Falling back to raw
		// substring matching here would let "deploy now" match "deploy it now"
		// and let prose embed a keyword to earn inferred authorization.
		if containsCJK(kw) {
			if strings.Contains(lower, kw) {
				return true
			}
			continue
		}
		want := tokenize(kw)
		if len(want) == 0 {
			continue
		}
		if containsTokenSequence(tokens, want) {
			return true
		}
	}
	return false
}

// tokenize splits text on every non-letter/digit rune, so punctuation and
// underscores are boundaries.
func tokenize(s string) []string {
	var (
		out []string
		b   strings.Builder
	)
	flush := func() {
		if b.Len() > 0 {
			out = append(out, b.String())
			b.Reset()
		}
	}
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return out
}

// containsTokenSequence reports whether want occurs as consecutive tokens.
func containsTokenSequence(tokens, want []string) bool {
	if len(want) == 0 || len(want) > len(tokens) {
		return false
	}
	for i := 0; i+len(want) <= len(tokens); i++ {
		match := true
		for j := range want {
			if tokens[i+j] != want[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

// containsCJK reports whether s contains Han, Hiragana, Katakana or Hangul.
// Only these get substring matching: they are written without word boundaries.
func containsCJK(s string) bool {
	for _, r := range s {
		switch {
		case r >= 0x4E00 && r <= 0x9FFF, // Han
			r >= 0x3400 && r <= 0x4DBF, // Han extension A
			r >= 0x3040 && r <= 0x309F, // Hiragana
			r >= 0x30A0 && r <= 0x30FF, // Katakana
			r >= 0xAC00 && r <= 0xD7AF, // Hangul syllables
			r >= 0x1100 && r <= 0x11FF: // Hangul Jamo
			return true
		}
	}
	return false
}
