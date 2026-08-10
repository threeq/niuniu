package event_test

import (
	"testing"

	"github.com/niuniu-dev/niuniu/internal/event"
	"github.com/stretchr/testify/assert"
)

// TestPhase2_EventTopicConstants_StableStrings is a regression guard ensuring
// the Go event topic constant strings never change without a coordinated update
// to the Phase 5 UI (which hardcodes these strings on the SSE subscriber side).
// See docs/superpowers/specs/2026-05-09-phase2-event-contract.md for the
// authoritative contract.
func TestPhase2_EventTopicConstants_StableStrings(t *testing.T) {
	assert.Equal(t, "run_phase_started", event.EventRunPhaseStarted)
	assert.Equal(t, "run_phase_skipped", event.EventRunPhaseSkipped)
	assert.Equal(t, "run_phase_aborted", event.EventRunPhaseAborted)
	assert.Equal(t, "gate_started", event.EventGateStarted)
	assert.Equal(t, "gate_progress", event.EventGateProgress)
	assert.Equal(t, "gate_done", event.EventGateDone)
	assert.Equal(t, "agent_lifecycle", event.EventAgentLifecycle)
}
