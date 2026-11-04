// Package webhooksource normalizes the team-edition email / generic webhook
// Sources into the platform-neutral intake.Trigger (Epic #755 / design §6.10).
//
// These adapters carry no GitHub-specific structure. External payload text is
// data, never instructions: normalization only maps fields and never derives
// authority from message content. Authorization is decided by the shared Intake
// policy (allow list / rules / mode), exactly as for the other Sources.
package webhooksource

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

// Generic webhook payload. The contract is deliberately small: a caller sets
// `id` (stable across retries) and `text`; everything else is optional. When
// `id` is absent the body hash is used, so an identical redelivery still
// dedupes — distinct events without an id should provide one.
type genericPayload struct {
	ID          string          `json:"id"`
	EventID     string          `json:"event_id"`
	Event       string          `json:"event"`
	Actor       json.RawMessage `json:"actor"`
	ThreadID    string          `json:"thread_id"`
	Text        string          `json:"text"`
	SourceURL   string          `json:"url"`
	ReceivedAt  string          `json:"received_at"`
	Attachments []attachment    `json:"attachments"`
}

type attachment struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	MIMEType  string `json:"mime_type"`
	SizeBytes int64  `json:"size_bytes"`
}

// Email payload. It accepts either a flat string or an object for the
// address-bearing fields, matching the common inbound-parse providers
// (Mailgun / SendGrid / Postmark all expose these under slightly different
// names).
type emailPayload struct {
	MessageID  string          `json:"message_id"`
	ThreadID   string          `json:"thread_id"`
	InReplyTo  string          `json:"in_reply_to"`
	References string          `json:"references"`
	From       json.RawMessage `json:"from"`
	To         json.RawMessage `json:"to"`
	Subject    string          `json:"subject"`
	Text       string          `json:"text"`
	BodyPlain  string          `json:"body-plain"`
	Body       string          `json:"body"`
	HTML       string          `json:"html"`
	SourceURL  string          `json:"url"`
	Date       string          `json:"date"`
}

type address struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// NormalizeGeneric maps a generic webhook body to a Trigger. bindingName is the
// project's configured SourceRef; it is authoritative (never taken from the
// payload).
func NormalizeGeneric(projectID int64, bindingName string, raw []byte) (intake.Trigger, error) {
	var p genericPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return intake.Trigger{}, fmt.Errorf("webhooksource: decode payload: %w", err)
	}
	text := strings.TrimSpace(p.Text)
	if text == "" && len(p.Attachments) == 0 {
		return intake.Trigger{}, fmt.Errorf("webhooksource: text or attachments required")
	}
	actorID, actorName, isBot := parseActor(p.Actor)
	eventID := firstNonEmpty(p.ID, p.EventID)
	if eventID == "" {
		eventID = "body:" + bodyHash(raw)
	}
	kind := strings.TrimSpace(p.Event)
	if kind == "" {
		kind = intake.EventKindMessage
	}
	return intake.Trigger{
		ProjectID:   projectID,
		SourceKind:  intake.SourceKindWebhook,
		SourceRef:   bindingName,
		EventID:     eventID,
		EventKind:   kind,
		Thread:      intake.ThreadRef{ThreadExtID: strings.TrimSpace(p.ThreadID)},
		Actor:       intake.ActorRef{ExtID: actorID, Name: actorName, IsBot: isBot},
		Text:        text,
		Attachments: convertAttachments(p.Attachments),
		Raw:         append(json.RawMessage(nil), raw...),
		ReceivedAt:  parseTime(p.ReceivedAt),
		SourceURL:   strings.TrimSpace(p.SourceURL),
		// This Source receives every event on the binding, so an unlisted actor is
		// an inferred candidate — the same contract as a polled GitHub repo. Auto
		// mode still requires a matching rule and never grants an unlisted actor
		// explicit authority.
		ReceiveAll: true,
	}, nil
}

// NormalizeEmail maps an inbound-parse email body to a Trigger.
func NormalizeEmail(projectID int64, bindingName string, raw []byte) (intake.Trigger, error) {
	var p emailPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return intake.Trigger{}, fmt.Errorf("webhooksource: decode email: %w", err)
	}
	body := firstNonEmpty(p.Text, p.BodyPlain, p.Body, p.HTML)
	subject := strings.TrimSpace(p.Subject)
	if strings.TrimSpace(body) == "" && subject == "" {
		return intake.Trigger{}, fmt.Errorf("webhooksource: email body required")
	}
	text := subject
	if b := strings.TrimSpace(body); b != "" {
		if text != "" {
			text += "\n\n"
		}
		text += b
	}
	actorID, actorName := parseAddress(p.From)
	if actorID == "" {
		actorID = actorName
	}
	messageID := strings.TrimSpace(p.MessageID)
	eventID := messageID
	if eventID == "" {
		eventID = "body:" + bodyHash(raw)
	}
	// Prefer an explicit thread id, then the standard reply headers. A standalone
	// message without either becomes its own thread instead of resuming an
	// unrelated task.
	thread := firstNonEmpty(p.ThreadID, p.InReplyTo, p.References, messageID)
	return intake.Trigger{
		ProjectID:  projectID,
		SourceKind: intake.SourceKindEmail,
		SourceRef:  bindingName,
		EventID:    eventID,
		EventKind:  intake.EventKindMessage,
		Thread:     intake.ThreadRef{ThreadExtID: thread},
		Actor:      intake.ActorRef{ExtID: actorID, Name: actorName},
		Text:       text,
		Raw:        append(json.RawMessage(nil), raw...),
		ReceivedAt: parseTime(p.Date),
		SourceURL:  strings.TrimSpace(p.SourceURL),
		ReceiveAll: true,
	}, nil
}

// parseActor accepts either a bare string id or {"id","name","is_bot"}.
func parseActor(raw json.RawMessage) (id, name string, isBot bool) {
	if len(raw) == 0 {
		return "", "", false
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		s := strings.TrimSpace(asString)
		return s, s, false
	}
	var a struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Login string `json:"login"`
		IsBot bool   `json:"is_bot"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", "", false
	}
	id = firstNonEmpty(a.ID, a.Login, a.Name)
	name = firstNonEmpty(a.Name, a.Login, a.ID)
	return id, name, a.IsBot
}

// parseAddress accepts "a@b" or {"email","name"}.
func parseAddress(raw json.RawMessage) (email, name string) {
	if len(raw) == 0 {
		return "", ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		s := strings.TrimSpace(asString)
		return s, s
	}
	var a address
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", ""
	}
	email = strings.TrimSpace(a.Email)
	name = strings.TrimSpace(a.Name)
	if name == "" {
		name = email
	}
	return email, name
}

func convertAttachments(in []attachment) []intake.Attachment {
	if len(in) == 0 {
		return nil
	}
	out := make([]intake.Attachment, 0, len(in))
	for _, a := range in {
		out = append(out, intake.Attachment{
			Kind:      a.Kind,
			Name:      a.Name,
			URL:       a.URL,
			MIMEType:  a.MIMEType,
			SizeBytes: a.SizeBytes,
		})
	}
	return out
}

func bodyHash(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func parseTime(raw string) time.Time {
	s := strings.TrimSpace(raw)
	if s == "" {
		return time.Time{}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, time.RFC1123Z, time.RFC1123} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}
