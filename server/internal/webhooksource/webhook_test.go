package webhooksource

import (
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

func TestNormalizeGeneric(t *testing.T) {
	raw := []byte(`{
	  "id":"evt-1",
	  "actor":{"id":"u-7","name":"Alice","is_bot":false},
	  "thread_id":"thread-9",
	  "text":"please deploy",
	  "url":"https://example.test/events/1",
	  "received_at":"2026-11-04T00:00:00Z",
	  "attachments":[{"kind":"file","name":"log.txt","url":"https://example.test/log.txt","mime_type":"text/plain","size_bytes":12}]
	}`)
	trig, err := NormalizeGeneric(3, "prod-hooks", raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if trig.SourceKind != intake.SourceKindWebhook || trig.SourceRef != "prod-hooks" {
		t.Fatalf("source = %s/%s", trig.SourceKind, trig.SourceRef)
	}
	if trig.EventID != "evt-1" || trig.EventKind != intake.EventKindMessage {
		t.Fatalf("event = %s/%s", trig.EventID, trig.EventKind)
	}
	if trig.Thread.ThreadExtID != "thread-9" {
		t.Fatalf("thread = %q", trig.Thread.ThreadExtID)
	}
	if trig.Actor.ExtID != "u-7" || trig.Actor.Name != "Alice" || trig.Actor.IsBot {
		t.Fatalf("actor = %+v", trig.Actor)
	}
	if trig.Text != "please deploy" || trig.SourceURL != "https://example.test/events/1" {
		t.Fatalf("text/url = %q/%q", trig.Text, trig.SourceURL)
	}
	if len(trig.Attachments) != 1 || trig.Attachments[0].Name != "log.txt" {
		t.Fatalf("attachments = %+v", trig.Attachments)
	}
	if !trig.ReceiveAll || trig.Explicit {
		t.Fatalf("authorization flags = receiveAll:%v explicit:%v", trig.ReceiveAll, trig.Explicit)
	}
	if trig.ReceivedAt.IsZero() {
		t.Fatal("received_at was not parsed")
	}
}

func TestNormalizeGenericActorStringAndHashFallback(t *testing.T) {
	raw := []byte(`{"actor":"ci-bot","text":"run tests"}`)
	a, err := NormalizeGeneric(3, "hooks", raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if a.Actor.ExtID != "ci-bot" || a.Actor.Name != "ci-bot" {
		t.Fatalf("actor = %+v", a.Actor)
	}
	if !strings.HasPrefix(a.EventID, "body:") {
		t.Fatalf("event id = %q, want a body hash", a.EventID)
	}
	// The same body redelivered must dedupe: the fallback id is stable.
	b, err := NormalizeGeneric(3, "hooks", append([]byte(nil), raw...))
	if err != nil {
		t.Fatalf("normalize 2: %v", err)
	}
	if a.EventID != b.EventID {
		t.Fatalf("hash fallback is not stable: %q != %q", a.EventID, b.EventID)
	}
}

func TestNormalizeGenericRejectsEmpty(t *testing.T) {
	if _, err := NormalizeGeneric(3, "hooks", []byte(`{}`)); err == nil {
		t.Fatal("an empty payload must be rejected")
	}
	if _, err := NormalizeGeneric(3, "hooks", []byte(`{`)); err == nil {
		t.Fatal("malformed JSON must be rejected")
	}
}

func TestNormalizeEmail(t *testing.T) {
	raw := []byte(`{
	  "message_id":"<m1@example.test>",
	  "in_reply_to":"<m0@example.test>",
	  "from":{"email":"alice@example.test","name":"Alice"},
	  "to":"tasks@example.test",
	  "subject":"Deploy the service",
	  "body-plain":"please deploy v2",
	  "date":"2026-11-04T00:00:00Z"
	}`)
	trig, err := NormalizeEmail(3, "support", raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if trig.SourceKind != intake.SourceKindEmail || trig.SourceRef != "support" {
		t.Fatalf("source = %s/%s", trig.SourceKind, trig.SourceRef)
	}
	if trig.EventID != "<m1@example.test>" {
		t.Fatalf("event id = %q", trig.EventID)
	}
	// Reply headers thread the message back to its conversation.
	if trig.Thread.ThreadExtID != "<m0@example.test>" {
		t.Fatalf("thread = %q", trig.Thread.ThreadExtID)
	}
	if trig.Actor.ExtID != "alice@example.test" || trig.Actor.Name != "Alice" {
		t.Fatalf("actor = %+v", trig.Actor)
	}
	if !strings.Contains(trig.Text, "Deploy the service") || !strings.Contains(trig.Text, "please deploy v2") {
		t.Fatalf("text = %q", trig.Text)
	}
	if trig.Actor.IsBot {
		t.Fatal("email actors are not bots by default")
	}
}

func TestNormalizeEmailStringAddressAndStandaloneThread(t *testing.T) {
	raw := []byte(`{"message_id":"m2","from":"bob@example.test","subject":"hi","text":"hello"}`)
	trig, err := NormalizeEmail(3, "support", raw)
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if trig.Actor.ExtID != "bob@example.test" {
		t.Fatalf("actor = %+v", trig.Actor)
	}
	// No reply headers: the message is its own thread rather than resuming an
	// unrelated conversation.
	if trig.Thread.ThreadExtID != "m2" {
		t.Fatalf("thread = %q", trig.Thread.ThreadExtID)
	}
}

func TestNormalizeEmailRejectsEmpty(t *testing.T) {
	if _, err := NormalizeEmail(3, "support", []byte(`{}`)); err == nil {
		t.Fatal("an empty email must be rejected")
	}
}
