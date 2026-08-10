package adapter

import "testing"

func TestClaudeResultParsesCacheTokens(t *testing.T) {
	line := `{"type":"result","subtype":"success","is_error":false,` +
		`"total_cost_usd":0.12,"num_turns":3,"duration_ms":4200,` +
		`"usage":{"input_tokens":1000,"output_tokens":200,` +
		`"cache_creation_input_tokens":50,"cache_read_input_tokens":800}}`
	ev, err := ParseStreamLine(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.InputTokens != 1000 || ev.OutputTokens != 200 {
		t.Fatalf("in/out tokens = %d/%d", ev.InputTokens, ev.OutputTokens)
	}
	if ev.CacheCreationTokens != 50 {
		t.Fatalf("cache_creation = %d, want 50", ev.CacheCreationTokens)
	}
	if ev.CacheReadTokens != 800 {
		t.Fatalf("cache_read = %d, want 800", ev.CacheReadTokens)
	}
}

// TestClaudeMessageStartParsesCacheTokens pins that message_start carries the
// per-request cache fields. They are the authoritative live context-window
// occupancy (one prompt's size), unlike the result event whose usage is the
// turn-cumulative sum across every tool round-trip.
func TestClaudeMessageStartParsesCacheTokens(t *testing.T) {
	line := `{"type":"stream_event","event":{"type":"message_start",` +
		`"message":{"usage":{"input_tokens":1200,"output_tokens":1,` +
		`"cache_creation_input_tokens":300,"cache_read_input_tokens":38000}}}}`
	ev, err := ParseStreamLine(line)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if ev.InputTokens != 1200 {
		t.Fatalf("input = %d, want 1200", ev.InputTokens)
	}
	if ev.CacheCreationTokens != 300 {
		t.Fatalf("cache_creation = %d, want 300", ev.CacheCreationTokens)
	}
	if ev.CacheReadTokens != 38000 {
		t.Fatalf("cache_read = %d, want 38000", ev.CacheReadTokens)
	}
}

func TestCodexTurnParsesCacheRead(t *testing.T) {
	a := CodexAdapter{}
	line := `{"type":"turn_completed","usage":{"input_tokens":300,"output_tokens":40,"cached_input_tokens":120}}`
	evs, err := a.ParseLine(line)
	if err != nil || len(evs) == 0 {
		t.Fatalf("parse: %v len=%d", err, len(evs))
	}
	if evs[0].CacheReadTokens != 120 {
		t.Fatalf("cache_read = %d, want 120", evs[0].CacheReadTokens)
	}
}
