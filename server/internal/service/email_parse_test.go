package service

import (
	"encoding/base64"
	"strings"
	"testing"
)

// buildEmail renders a minimal RFC 5322 message. headers are "Name: value"
// lines; body is appended verbatim after the blank separator.
func buildEmail(headers []string, body string) []byte {
	var b strings.Builder
	for _, h := range headers {
		b.WriteString(h)
		b.WriteString("\r\n")
	}
	b.WriteString("\r\n")
	b.WriteString(body)
	return []byte(b.String())
}

func TestParseEmailThreadRootPrecedence(t *testing.T) {
	tests := []struct {
		name     string
		headers  []string
		wantRoot string
	}{
		{
			name: "references first id wins",
			headers: []string{
				"Message-ID: <reply-2@example.com>",
				"In-Reply-To: <reply-1@example.com>",
				"References: <root@example.com> <reply-1@example.com>",
			},
			wantRoot: "root@example.com",
		},
		{
			name: "in-reply-to when references is missing",
			headers: []string{
				"Message-ID: <reply-2@example.com>",
				"In-Reply-To: <root@example.com>",
			},
			wantRoot: "root@example.com",
		},
		{
			name:     "own message id for a standalone message",
			headers:  []string{"Message-ID: <root@example.com>"},
			wantRoot: "root@example.com",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseEmailMessage(buildEmail(tt.headers, "hello"), "", true)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if p.ThreadRoot != tt.wantRoot {
				t.Fatalf("thread root = %q, want %q", p.ThreadRoot, tt.wantRoot)
			}
		})
	}
}

func TestParseEmailPrefersPlainTextPart(t *testing.T) {
	body := strings.Join([]string{
		"--b0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"the plain body",
		"--b0",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<p>the html body</p>",
		"--b0--",
		"",
	}, "\r\n")
	raw := buildEmail([]string{
		"Message-ID: <m@example.com>",
		"Content-Type: multipart/alternative; boundary=b0",
	}, body)

	p, err := parseEmailMessage(raw, "", true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !strings.Contains(p.Text, "the plain body") {
		t.Fatalf("text = %q, want the plain part", p.Text)
	}
	if strings.Contains(p.Text, "html body") {
		t.Fatalf("text = %q, plain part should win over html", p.Text)
	}
}

func TestParseEmailFallsBackToHTML(t *testing.T) {
	body := strings.Join([]string{
		"--b1",
		"Content-Type: text/html; charset=utf-8",
		"",
		"<html><head><title>ignored</title><style>p{}</style></head><body><p>Hello</p><p>World</p></body></html>",
		"--b1--",
		"",
	}, "\r\n")
	raw := buildEmail([]string{
		"Message-ID: <h@example.com>",
		"Content-Type: multipart/mixed; boundary=b1",
	}, body)

	p, err := parseEmailMessage(raw, "", true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	want := "Hello\nWorld"
	if p.Text != want {
		t.Fatalf("text = %q, want %q", p.Text, want)
	}
}

func TestParseEmailStripsQuotedReply(t *testing.T) {
	body := strings.Join([]string{
		"Please fix the flaky test.",
		"",
		"On Mon, 2 Nov 2026 at 10:00, Bob <bob@example.com> wrote:",
		"> original line",
		"> another line",
	}, "\n")
	raw := buildEmail([]string{
		"Message-ID: <q@example.com>",
		"Content-Type: text/plain; charset=utf-8",
	}, body)

	p, err := parseEmailMessage(raw, "", true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if p.Text != "Please fix the flaky test." {
		t.Fatalf("text = %q, want only the new content", p.Text)
	}
}

func TestParseEmailStripsBareQuotedLines(t *testing.T) {
	body := "Answer inline.\n> quoted\n> more quoted\nThanks."
	raw := buildEmail([]string{
		"Message-ID: <q2@example.com>",
		"Content-Type: text/plain",
	}, body)

	p, err := parseEmailMessage(raw, "", true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Contains(p.Text, "quoted") {
		t.Fatalf("text = %q, quoted lines should be dropped", p.Text)
	}
	if !strings.Contains(p.Text, "Answer inline.") || !strings.Contains(p.Text, "Thanks.") {
		t.Fatalf("text = %q, non-quoted content lost", p.Text)
	}
}

func TestParseEmailIgnoreRules(t *testing.T) {
	base := []string{"Message-ID: <i@example.com>", "From: Bob <bob@example.com>", "Content-Type: text/plain"}
	tests := []struct {
		name    string
		headers []string
		self    string
		ignore  bool
		want    string
	}{
		{
			name:    "auto submitted",
			headers: append(append([]string{}, base...), "Auto-Submitted: auto-replied"),
			ignore:  true,
			want:    "auto-submitted",
		},
		{
			name:    "auto-submitted no is real mail",
			headers: append(append([]string{}, base...), "Auto-Submitted: no"),
			ignore:  true,
			want:    "",
		},
		{
			name:    "precedence bulk",
			headers: append(append([]string{}, base...), "Precedence: bulk"),
			ignore:  true,
			want:    "precedence",
		},
		{
			name:    "bounce with empty return-path",
			headers: append(append([]string{}, base...), "Return-Path: <>"),
			ignore:  true,
			want:    "bounce",
		},
		{
			name:    "message from the binding's own mailbox",
			headers: base,
			self:    "BOB@example.com",
			ignore:  true,
			want:    "self",
		},
		{
			name:    "rules disabled",
			headers: append(append([]string{}, base...), "Precedence: bulk"),
			ignore:  false,
			want:    "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := parseEmailMessage(buildEmail(tt.headers, "body"), tt.self, tt.ignore)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if p.IgnoreReason != tt.want {
				t.Fatalf("ignore reason = %q, want %q", p.IgnoreReason, tt.want)
			}
		})
	}
}

func TestParseEmailTruncatesBodyAt64KB(t *testing.T) {
	body := strings.Repeat("a", 100_000)
	raw := buildEmail([]string{
		"Message-ID: <big@example.com>",
		"Content-Type: text/plain",
	}, body)

	p, err := parseEmailMessage(raw, "", true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.Text) != emailBodyLimit {
		t.Fatalf("text length = %d, want %d", len(p.Text), emailBodyLimit)
	}
}

func TestParseEmailTruncationKeepsValidUTF8(t *testing.T) {
	// One byte short of a clean boundary: the cut must back off to a rune
	// boundary rather than leave half a multi-byte character.
	body := strings.Repeat("世", (emailBodyLimit/3)+10)
	raw := buildEmail([]string{
		"Message-ID: <utf@example.com>",
		"Content-Type: text/plain; charset=utf-8",
	}, body)

	p, err := parseEmailMessage(raw, "", true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(p.Text) > emailBodyLimit {
		t.Fatalf("text length = %d, want <= %d", len(p.Text), emailBodyLimit)
	}
	if !utf8Valid(p.Text) {
		t.Fatalf("truncation produced invalid UTF-8")
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

func TestParseEmailAttachmentMetadataOnly(t *testing.T) {
	pdf := []byte("%PDF-1.4 fake pdf payload")
	body := strings.Join([]string{
		"--mix",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"see the attachment",
		"--mix",
		"Content-Type: application/pdf; name=report.pdf",
		"Content-Disposition: attachment; filename=report.pdf",
		"Content-Transfer-Encoding: base64",
		"",
		base64.StdEncoding.EncodeToString(pdf),
		"--mix--",
		"",
	}, "\r\n")
	raw := buildEmail([]string{
		"Message-ID: <att@example.com>",
		"Content-Type: multipart/mixed; boundary=mix",
	}, body)

	p, err := parseEmailMessage(raw, "", true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if strings.Contains(p.Text, "PDF-1.4") {
		t.Fatalf("attachment body leaked into text: %q", p.Text)
	}
	if len(p.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(p.Attachments))
	}
	att := p.Attachments[0]
	if att.Name != "report.pdf" || att.MIMEType != "application/pdf" || att.Kind != "file" {
		t.Fatalf("attachment = %+v", att)
	}
	if att.SizeBytes != int64(len(pdf)) {
		t.Fatalf("attachment size = %d, want %d", att.SizeBytes, len(pdf))
	}
	if att.URL != "" {
		t.Fatalf("attachment URL = %q, want empty (metadata only)", att.URL)
	}
}

func TestParsedEmailEventIDFallback(t *testing.T) {
	raw := buildEmail([]string{"Content-Type: text/plain"}, "no message id")
	p, err := parseEmailMessage(raw, "", true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := p.eventID(42, 7); got != "imap:42:7" {
		t.Fatalf("event id = %q, want imap:42:7", got)
	}

	withID, err := parseEmailMessage(buildEmail([]string{"Message-ID: <x@y>"}, "body"), "", true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got := withID.eventID(42, 7); got != "x@y" {
		t.Fatalf("event id = %q, want x@y", got)
	}
}

func TestParsedEmailTriggerShape(t *testing.T) {
	raw := buildEmail([]string{
		"Message-ID: <t@example.com>",
		"From: Bob <bob@example.com>",
		"Subject: Deploy failed",
		"Date: Mon, 2 Nov 2026 10:00:00 +0800",
		"Content-Type: text/plain",
	}, "please look")
	p, err := parseEmailMessage(raw, "", true)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	trig := p.trigger(9, "inbox/3", 5, 11, p.Date)
	if trig.ProjectID != 9 || trig.SourceRef != "inbox/3" || trig.SourceKind != "email" {
		t.Fatalf("trigger = %+v", trig)
	}
	if trig.EventID != "t@example.com" || trig.Thread.ThreadExtID != "t@example.com" {
		t.Fatalf("trigger ids = %+v", trig)
	}
	if trig.Actor.ExtID != "bob@example.com" || trig.Actor.Name != "Bob" {
		t.Fatalf("actor = %+v", trig.Actor)
	}
	if trig.Text != "Deploy failed\n\nplease look" {
		t.Fatalf("text = %q", trig.Text)
	}
	if trig.ReceivedAt.IsZero() {
		t.Fatalf("receivedAt was not taken from the Date header")
	}
}
