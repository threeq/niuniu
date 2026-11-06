package webhooksource

import (
	"strings"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

// The three provider samples below are the shapes their inbound-parse webhooks
// actually post: Mailgun uses flat hyphenated fields plus message-headers,
// SendGrid puts the RFC headers in one raw block, Postmark uses PascalCase JSON
// with a Headers array. All three must normalize identically.

func TestNormalizeEmailMailgun(t *testing.T) {
	raw := []byte(`{
	  "from": "Alice Example <alice@example.test>",
	  "to": "support@inbound.example.test",
	  "subject": "Re: Deploy staging",
	  "body-plain": "please deploy v3\n\nOn Mon, Nov 5, 2026 at 10:00 AM Bob <bob@example.test> wrote:\n> old quoted text",
	  "stripped-text": "please deploy v3",
	  "Message-Id": "<mailgun-2@example.test>",
	  "message-headers": "[[\"From\", \"Alice Example <alice@example.test>\"], [\"In-Reply-To\", \"<mailgun-1@example.test>\"], [\"References\", \"<mailgun-root@example.test> <mailgun-1@example.test>\"], [\"Auto-Submitted\", \"no\"]]",
	  "timestamp": "1762300000"
	}`)
	res, err := NormalizeEmailMessage(3, "support", raw, EmailNormalizeOptions{Provider: EmailProviderMailgun})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if res.Ignored {
		t.Fatalf("ordinary mail was ignored: %s", res.IgnoreReason)
	}
	trig := res.Trigger
	if trig.EventID != "mailgun-2@example.test" {
		t.Fatalf("event id = %q", trig.EventID)
	}
	// References' first id wins, exactly as on the IMAP path.
	if trig.Thread.ThreadExtID != "mailgun-root@example.test" {
		t.Fatalf("thread = %q", trig.Thread.ThreadExtID)
	}
	if trig.Actor.ExtID != "alice@example.test" || trig.Actor.Name != "Alice Example" {
		t.Fatalf("actor = %+v", trig.Actor)
	}
	if !strings.Contains(trig.Text, "please deploy v3") || strings.Contains(trig.Text, "old quoted text") {
		t.Fatalf("text = %q", trig.Text)
	}
	if trig.ReceivedAt.IsZero() {
		t.Fatal("Mailgun timestamp should seed ReceivedAt when Date is absent")
	}
}

func TestNormalizeEmailSendGrid(t *testing.T) {
	raw := []byte(`{
	  "from": "Bob <bob@example.test>",
	  "to": "support@inbound.example.test",
	  "subject": "Re: Build failed",
	  "text": "the build is red",
	  "headers": "Received: from mail.example.test\r\nMessage-ID: <sendgrid-2@example.test>\r\nIn-Reply-To: <sendgrid-1@example.test>\r\nReferences: <sendgrid-root@example.test> <sendgrid-1@example.test>\r\nAuto-Submitted: no\r\n\r\n"
	}`)
	res, err := NormalizeEmailMessage(3, "support", raw, EmailNormalizeOptions{Provider: EmailProviderSendGrid})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if res.Ignored {
		t.Fatalf("ordinary mail was ignored: %s", res.IgnoreReason)
	}
	if res.Trigger.EventID != "sendgrid-2@example.test" {
		t.Fatalf("event id = %q", res.Trigger.EventID)
	}
	if res.Trigger.Thread.ThreadExtID != "sendgrid-root@example.test" {
		t.Fatalf("thread = %q", res.Trigger.Thread.ThreadExtID)
	}
	if res.Trigger.Actor.ExtID != "bob@example.test" || !strings.Contains(res.Trigger.Text, "the build is red") {
		t.Fatalf("trigger = %+v", res.Trigger)
	}
}

func TestNormalizeEmailPostmark(t *testing.T) {
	raw := []byte(`{
	  "From": "Carol <carol@example.test>",
	  "FromFull": {"Email":"carol@example.test","Name":"Carol"},
	  "To": "support@inbound.example.test",
	  "Subject": "Re: question",
	  "TextBody": "answer here",
	  "HtmlBody": "<p>answer here</p>",
	  "MessageID": "postmark-2",
	  "Headers": [
	    {"Name":"In-Reply-To","Value":"<postmark-1@example.test>"},
	    {"Name":"References","Value":"<postmark-root@example.test> <postmark-1@example.test>"},
	    {"Name":"Auto-Submitted","Value":"no"}
	  ],
	  "Date": "2026-11-06T12:00:00Z"
	}`)
	res, err := NormalizeEmailMessage(3, "support", raw, EmailNormalizeOptions{Provider: EmailProviderPostmark})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if res.Ignored {
		t.Fatalf("ordinary mail was ignored: %s", res.IgnoreReason)
	}
	if res.Trigger.EventID != "postmark-2" {
		t.Fatalf("event id = %q", res.Trigger.EventID)
	}
	if res.Trigger.Thread.ThreadExtID != "postmark-root@example.test" {
		t.Fatalf("thread = %q", res.Trigger.Thread.ThreadExtID)
	}
	if res.Trigger.Actor.ExtID != "carol@example.test" || res.Trigger.Actor.Name != "Carol" {
		t.Fatalf("actor = %+v", res.Trigger.Actor)
	}
	if !strings.Contains(res.Trigger.Text, "answer here") {
		t.Fatalf("text = %q", res.Trigger.Text)
	}
	if res.Trigger.ReceivedAt.IsZero() {
		t.Fatal("Date should set ReceivedAt")
	}
}

func TestNormalizeEmailGenericAliases(t *testing.T) {
	raw := []byte(`{
	  "Message-ID": "<generic-2@example.test>",
	  "In-Reply-To": "<generic-1@example.test>",
	  "References": "<generic-root@example.test>",
	  "from": {"email":"dave@example.test","name":"Dave"},
	  "to": "support@inbound.example.test",
	  "subject": "hello",
	  "body_plain": "generic body"
	}`)
	res, err := NormalizeEmailMessage(3, "support", raw, EmailNormalizeOptions{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if res.Trigger.EventID != "generic-2@example.test" {
		t.Fatalf("event id = %q", res.Trigger.EventID)
	}
	if res.Trigger.Thread.ThreadExtID != "generic-root@example.test" {
		t.Fatalf("thread = %q", res.Trigger.Thread.ThreadExtID)
	}
	if res.Trigger.Actor.ExtID != "dave@example.test" {
		t.Fatalf("actor = %+v", res.Trigger.Actor)
	}
}

func TestNormalizeEmailIgnoresAutoBulkBounceSelf(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "auto-submitted",
			raw:  `{"from":"a@example.test","to":"support@example.test","subject":"s","text":"t","auto_submitted":"auto-replied"}`,
			want: "auto-submitted",
		},
		{
			name: "mailgun precedence header",
			raw: `{"from":"a@example.test","to":"support@example.test","subject":"s","text":"t",
			  "message-headers":"[[\"Precedence\", \"bulk\"]]"}`,
			want: "precedence",
		},
		{
			name: "sendgrid bounce header",
			raw: `{"from":"mailer-daemon@example.test","to":"support@example.test","subject":"s","text":"t",
			  "headers":"Return-Path: <>\r\n\r\n"}`,
			want: "bounce",
		},
		{
			name: "self-sent",
			raw:  `{"from":"support@example.test","to":"Support <support@example.test>","subject":"s","text":"t"}`,
			want: "self",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := NormalizeEmailMessage(3, "support", []byte(tt.raw), EmailNormalizeOptions{})
			if err != nil {
				t.Fatalf("normalize: %v", err)
			}
			if !res.Ignored || res.IgnoreReason != tt.want {
				t.Fatalf("ignored=%v reason=%q, want %q", res.Ignored, res.IgnoreReason, tt.want)
			}
		})
	}
}

func TestNormalizeEmailIgnoreCanBeDisabled(t *testing.T) {
	raw := []byte(`{"from":"a@example.test","to":"support@example.test","subject":"s","text":"t","auto_submitted":"auto-replied"}`)
	res, err := NormalizeEmailMessage(3, "support", raw, EmailNormalizeOptions{DisableIgnore: true})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if res.Ignored {
		t.Fatalf("ignore_auto=false must keep the message, got %q", res.IgnoreReason)
	}
}

func TestNormalizeEmailSelfAddressOption(t *testing.T) {
	// A message from the configured mailbox is ignored even if the payload's
	// recipient field is missing.
	raw := []byte(`{"from":"owner@example.test","subject":"s","text":"t"}`)
	res, err := NormalizeEmailMessage(3, "support", raw, EmailNormalizeOptions{SelfAddress: "owner@example.test"})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !res.Ignored || res.IgnoreReason != "self" {
		t.Fatalf("ignored=%v reason=%q", res.Ignored, res.IgnoreReason)
	}
}

func TestNormalizeEmailEventIDFallbackIsStable(t *testing.T) {
	raw := []byte(`{"from":"a@example.test","subject":"s","text":"t"}`)
	a, err := NormalizeEmailMessage(3, "support", raw, EmailNormalizeOptions{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	b, err := NormalizeEmailMessage(3, "support", append([]byte(nil), raw...), EmailNormalizeOptions{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !strings.HasPrefix(a.Trigger.EventID, "body:") {
		t.Fatalf("event id = %q, want a body hash", a.Trigger.EventID)
	}
	if a.Trigger.EventID != b.Trigger.EventID {
		t.Fatalf("fallback id is not stable: %q != %q", a.Trigger.EventID, b.Trigger.EventID)
	}
}

func TestNormalizeEmailHTMLOnly(t *testing.T) {
	raw := []byte(`{"from":"a@example.test","subject":"s","html":"<p>hello <b>world</b></p>"}`)
	res, err := NormalizeEmailMessage(3, "support", raw, EmailNormalizeOptions{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if !strings.Contains(res.Trigger.Text, "hello world") {
		t.Fatalf("text = %q", res.Trigger.Text)
	}
}

func TestEmailProviderPresetFor(t *testing.T) {
	for _, name := range []string{"mailgun", " SendGrid ", "POSTMARK", ""} {
		if _, ok := EmailProviderPresetFor(name); !ok {
			t.Fatalf("preset %q should resolve", name)
		}
	}
	if p, _ := EmailProviderPresetFor(""); p.Provider != EmailProviderGeneric {
		t.Fatalf("empty provider = %q, want generic", p.Provider)
	}
	if _, ok := EmailProviderPresetFor("exchange"); ok {
		t.Fatal("unknown provider must not resolve")
	}
}

func TestNormalizeEmailRejectsEmptyPayload(t *testing.T) {
	if _, err := NormalizeEmailMessage(3, "support", []byte(`{}`), EmailNormalizeOptions{}); err == nil {
		t.Fatal("an empty email must be rejected")
	}
	if _, err := NormalizeEmailMessage(3, "support", []byte(`{`), EmailNormalizeOptions{}); err == nil {
		t.Fatal("malformed JSON must be rejected")
	}
}

func TestNormalizeEmailSpanReplyHeaders(t *testing.T) {
	// References may carry several ids; the first is the thread root and the
	// event id is the message's own id.
	raw := []byte(`{
	  "from":"a@example.test","subject":"s","text":"t",
	  "message_id":"<m3@example.test>",
	  "references":["<root@example.test>","<m2@example.test>"]
	}`)
	res, err := NormalizeEmailMessage(3, "support", raw, EmailNormalizeOptions{})
	if err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if res.Trigger.Thread.ThreadExtID != "root@example.test" || res.Trigger.EventID != "m3@example.test" {
		t.Fatalf("thread=%q event=%q", res.Trigger.Thread.ThreadExtID, res.Trigger.EventID)
	}
	if res.Trigger.SourceKind != intake.SourceKindEmail {
		t.Fatalf("source kind = %q", res.Trigger.SourceKind)
	}
}
