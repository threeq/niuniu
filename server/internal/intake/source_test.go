package intake

import (
	"context"
	"errors"
	"testing"
)

// fakeSource is a minimal adapter used to pin the Source contract: Kind
// identifies the adapter, Normalize is a pure payload -> Trigger mapping, and
// Reply sends a message back to the thread.
type fakeSource struct {
	kind         string
	triggers     []Trigger
	normalizeErr error
	replied      []Reply
}

var _ Source = (*fakeSource)(nil)

func (f *fakeSource) Kind() string { return f.kind }

func (f *fakeSource) Normalize(ctx context.Context, raw any) ([]Trigger, error) {
	if f.normalizeErr != nil {
		return nil, f.normalizeErr
	}
	return f.triggers, nil
}

func (f *fakeSource) Reply(ctx context.Context, thread ThreadRef, msg Reply) error {
	f.replied = append(f.replied, msg)
	return nil
}

func TestSourceContract(t *testing.T) {
	trigger := Trigger{
		ProjectID:  1,
		SourceKind: SourceKindIMBot,
		SourceRef:  "chat-1",
		EventID:    "evt-1",
		EventKind:  EventKindMessage,
		Thread:     ThreadRef{ThreadExtID: "thread-1"},
		Actor:      ActorRef{ExtID: "ou_alice", Name: "Alice"},
		Text:       "ship it",
	}
	src := &fakeSource{kind: SourceKindIMBot, triggers: []Trigger{trigger}}

	var adapter Source = src
	if adapter.Kind() != SourceKindIMBot {
		t.Fatalf("kind = %q", adapter.Kind())
	}
	got, err := adapter.Normalize(context.Background(), map[string]any{"id": "evt-1"})
	if err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if len(got) != 1 || got[0].EventID != "evt-1" {
		t.Fatalf("normalized = %+v", got)
	}
	if err := adapter.Reply(context.Background(), trigger.Thread, Reply{Text: "picked up"}); err != nil {
		t.Fatalf("Reply: %v", err)
	}
	if len(src.replied) != 1 || src.replied[0].Text != "picked up" {
		t.Fatalf("replies = %+v", src.replied)
	}
}

func TestSourceNormalizeError(t *testing.T) {
	wantErr := errors.New("bad payload")
	src := &fakeSource{normalizeErr: wantErr}
	if _, err := src.Normalize(context.Background(), nil); !errors.Is(err, wantErr) {
		t.Fatalf("err = %v, want %v", err, wantErr)
	}
}
