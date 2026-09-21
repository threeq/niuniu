package rsi

import (
	"fmt"
	"strings"

	"github.com/niuniu-dev/niuniu/agent/internal/eval"
)

// Fitness-gated self-evolution (OpenRSI-inspired): the task corpus is split
// into a PUBLIC set used during iteration and a PRIVATE held-out set used
// only for the adoption decision. A candidate system prompt is adopted iff
// its private-set pass rate is not lower than the incumbent's — iterating
// on the public set alone cannot game the adoption gate.
//
// Think-first protocol: every rewrite proposal must state its causal
// mechanism, expected gain, and falsification condition BEFORE any compute
// is spent; proposals missing any part are rejected without evaluation.

// Proposal is a think-first prompt rewrite proposal.
type Proposal struct {
	Mechanism       string
	ExpectedGain    string
	Falsification   string
	NewSystemPrompt string
}

// ParseThinkFirst validates a think-first proposal: all three sections
// present and non-empty.
func ParseThinkFirst(text string) (Proposal, error) {
	var p Proposal
	get := func(key string) string {
		for _, line := range strings.Split(text, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), key+":") {
				return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), key+":"))
			}
		}
		return ""
	}
	p.Mechanism = get("MECHANISM")
	p.ExpectedGain = get("EXPECTED GAIN")
	p.Falsification = get("FALSIFICATION")
	if p.Mechanism == "" {
		return p, fmt.Errorf("think-first proposal missing MECHANISM")
	}
	if p.ExpectedGain == "" {
		return p, fmt.Errorf("think-first proposal missing EXPECTED GAIN")
	}
	if p.Falsification == "" {
		return p, fmt.Errorf("think-first proposal missing FALSIFICATION")
	}
	return p, nil
}

// SplitTasks deterministically splits tasks into public (iteration) and
// private (adoption) sets by name hash, at the given private ratio.
func SplitTasks(tasks []eval.Task, privateRatio float64) (public, private []eval.Task) {
	for _, t := range tasks {
		h := fnv32(t.Name)
		if float64(h%1000)/1000.0 < privateRatio {
			private = append(private, t)
		} else {
			public = append(public, t)
		}
	}
	return public, private
}

func fnv32(s string) uint32 {
	h := uint32(2166136261)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= 16777619
	}
	return h
}

// FitnessGate: the adoption rule — new pass rate must be >= old.
func FitnessGate(newRate, oldRate float64) bool {
	return newRate >= oldRate
}

// AdversarialRetest: the winner must beat the incumbent's private baseline
// across fresh re-runs (mean), guarding against a lucky single pass.
func AdversarialRetest(newRates []float64, incumbentBaseline float64) bool {
	if len(newRates) == 0 {
		return false
	}
	sum := 0.0
	for _, r := range newRates {
		sum += r
	}
	return sum/float64(len(newRates)) > incumbentBaseline
}

// GenreRouting: same-genre lessons rank before others for a task hint.
// This is RecallFor's relevance ordering — kept as an explicit named
// helper so the routing intent is greppable.
func GenreRouting(store LessonSource, taskHint string, topN int, maxBytes int) (string, error) {
	return store.RecallFor(taskHint, topN, maxBytes)
}

// LessonSource is the minimal store surface for genre routing.
type LessonSource interface {
	RecallFor(taskHint string, topN, maxBytes int) (string, error)
}
