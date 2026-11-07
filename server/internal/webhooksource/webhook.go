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
	"net/mail"
	"sort"
	"strconv"
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

// Supported inbound-parse providers (design §6.3). Vendor payloads differ only
// in field names; NormalizeEmailMessage normalizes every alias to one contract,
// so the provider preset is an identity/validation aid, not a parsing branch.
const (
	EmailProviderGeneric  = "generic"
	EmailProviderMailgun  = "mailgun"
	EmailProviderSendGrid = "sendgrid"
	EmailProviderPostmark = "postmark"
)

// EmailProviderPreset identifies a supported inbound-parse vendor.
type EmailProviderPreset struct {
	Provider    string
	DisplayName string
}

var emailProviderPresets = []EmailProviderPreset{
	{Provider: EmailProviderGeneric, DisplayName: "Generic inbound parse"},
	{Provider: EmailProviderMailgun, DisplayName: "Mailgun"},
	{Provider: EmailProviderSendGrid, DisplayName: "SendGrid"},
	{Provider: EmailProviderPostmark, DisplayName: "Postmark"},
}

// EmailProviderPresets returns every supported preset (a copy).
func EmailProviderPresets() []EmailProviderPreset {
	return append([]EmailProviderPreset(nil), emailProviderPresets...)
}

// EmailProviderPresetFor resolves a provider name. Case and surrounding space
// are ignored; an empty name means the generic contract.
func EmailProviderPresetFor(provider string) (EmailProviderPreset, bool) {
	name := strings.ToLower(strings.TrimSpace(provider))
	if name == "" {
		name = EmailProviderGeneric
	}
	for _, p := range emailProviderPresets {
		if p.Provider == name {
			return p, true
		}
	}
	return EmailProviderPreset{}, false
}

// EmailNormalizeOptions tunes the inbound adapter.
type EmailNormalizeOptions struct {
	// Provider is the configured vendor (mailgun|sendgrid|postmark|generic). It
	// only decides which body field is preferred; headers and reply fields are
	// alias-normalized for every vendor.
	Provider string
	// SelfAddress is the binding's own mailbox, used for the loop guard. When
	// empty the message's recipient is used, so mail the mailbox sends to itself
	// is still ignored.
	SelfAddress string
	// DisableIgnore turns the default ignore rules off (config.ignore_auto=false).
	DisableIgnore bool
}

// EmailNormalizeResult is the normalized trigger plus the ignore verdict. An
// ignored message produces no event; the caller acknowledges it without
// recording anything.
type EmailNormalizeResult struct {
	Trigger      intake.Trigger
	Ignored      bool
	IgnoreReason string
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

// NormalizeEmail maps an inbound-parse email body to a Trigger without the
// ignore verdict. Use NormalizeEmailMessage on the inbound path so auto-mail,
// bulk mail, bounces and self-sent messages are dropped.
func NormalizeEmail(projectID int64, bindingName string, raw []byte) (intake.Trigger, error) {
	res, err := NormalizeEmailMessage(projectID, bindingName, raw, EmailNormalizeOptions{DisableIgnore: true})
	return res.Trigger, err
}

// NormalizeEmailMessage maps an inbound-parse email body to a Trigger, applying
// the same ignore rules and thread/event-id rules as the IMAP poller.
func NormalizeEmailMessage(projectID int64, bindingName string, raw []byte, opts EmailNormalizeOptions) (EmailNormalizeResult, error) {
	fields, err := decodeEmailFields(raw)
	if err != nil {
		return EmailNormalizeResult{}, fmt.Errorf("webhooksource: decode email: %w", err)
	}
	headers := decodeEmailHeaders(fields)

	plain, htmlBody := emailBodies(opts.Provider, fields)
	body := strings.TrimSpace(plain)
	if body == "" && strings.TrimSpace(htmlBody) != "" {
		body = HTMLToText(htmlBody)
	}
	body = StripQuotedReply(body)
	body = TruncateUTF8(body, EmailBodyLimit)
	subject := fields.str("subject")
	if body == "" && subject == "" {
		return EmailNormalizeResult{}, fmt.Errorf("webhooksource: email body required")
	}

	messageID := TrimMessageID(firstNonEmpty(fields.str("message_id"), headers.get("message-id")))
	inReplyTo := FirstMessageID(firstNonEmpty(fields.str("in_reply_to"), headers.get("in-reply-to")))
	references := MessageIDList(firstNonEmpty(fields.str("references"), headers.get("references")))
	if len(references) == 0 {
		references = MessageIDList(strings.Join(fields.strList("references"), " "))
	}
	// Explicit thread_id is a caller override; otherwise follow the same rule as
	// IMAP: first References id → In-Reply-To → own Message-ID.
	thread := EmailThreadRoot(references, inReplyTo, messageID)
	if explicit := fields.str("thread_id"); explicit != "" {
		thread = explicit
	}

	actorID, actorName := parseAddress(fields.raw("from", "from_full", "sender"))
	if actorName == "" {
		actorName = actorID
	}

	selfAddress := strings.TrimSpace(opts.SelfAddress)
	if selfAddress == "" {
		if to := fields.recipients(); len(to) > 0 {
			selfAddress = to[0]
		}
	}

	ignoreReason := ""
	if !opts.DisableIgnore {
		ignoreReason = EmailIgnoreReason(EmailHeaders{
			AutoSubmitted: headers.get("auto-submitted"),
			Precedence:    headers.get("precedence"),
			ReturnPath:    headers.get("return-path"),
		}, actorID, selfAddress)
	}

	text := subject
	if b := strings.TrimSpace(body); b != "" {
		if text != "" {
			text += "\n\n"
		}
		text += b
	}

	eventID := messageID
	if eventID == "" {
		eventID = "body:" + bodyHash(raw)
	}

	return EmailNormalizeResult{
		Trigger: intake.Trigger{
			ProjectID:  projectID,
			SourceKind: intake.SourceKindEmail,
			SourceRef:  bindingName,
			EventID:    eventID,
			EventKind:  intake.EventKindMessage,
			Thread:     intake.ThreadRef{ThreadExtID: thread},
			Actor:      intake.ActorRef{ExtID: actorID, Name: actorName},
			Text:       text,
			Raw:        append(json.RawMessage(nil), raw...),
			ReceivedAt: emailReceivedAt(fields),
			SourceURL:  fields.str("url"),
			ReceiveAll: true,
		},
		Ignored:      ignoreReason != "",
		IgnoreReason: ignoreReason,
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

// parseAddress accepts "a@b", "Name <a@b>" or {"email","name"}. Providers send
// the address in either shape; the mailbox comparison for the loop guard needs
// the bare address, so a display-name string is parsed rather than kept verbatim.
func parseAddress(raw json.RawMessage) (email, name string) {
	if len(raw) == 0 {
		return "", ""
	}
	var asString string
	if err := json.Unmarshal(raw, &asString); err == nil {
		s := strings.TrimSpace(asString)
		if s == "" {
			return "", ""
		}
		if a, err := mail.ParseAddress(s); err == nil {
			return strings.ToLower(strings.TrimSpace(a.Address)), strings.TrimSpace(a.Name)
		}
		return strings.ToLower(s), s
	}
	var a address
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", ""
	}
	email = strings.ToLower(strings.TrimSpace(a.Email))
	name = strings.TrimSpace(a.Name)
	if name == "" {
		name = email
	}
	return email, name
}

// --- provider payload field mapping -----------------------------------------

// emailFields indexes a payload by a normalized key: case, '-', '_', '.' and
// spaces are dropped, so message_id / Message-Id / MessageID are one field.
// Keys are inserted in sorted order so a payload carrying two spellings of the
// same field resolves deterministically.
type emailFields map[string]json.RawMessage

func decodeEmailFields(raw []byte) (emailFields, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, err
	}
	fields := make(emailFields, len(payload))
	keys := make([]string, 0, len(payload))
	for k := range payload {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		nk := normalizeFieldName(k)
		if _, exists := fields[nk]; !exists {
			fields[nk] = payload[k]
		}
	}
	return fields, nil
}

func normalizeFieldName(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch r {
		case '-', '_', ' ', '.':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func (f emailFields) raw(names ...string) json.RawMessage {
	for _, name := range names {
		if v, ok := f[normalizeFieldName(name)]; ok && len(v) > 0 && string(v) != "null" {
			return v
		}
	}
	return nil
}

// str returns a scalar field (string or number). Arrays/objects yield "".
func (f emailFields) str(names ...string) string {
	return scalarString(f.raw(names...))
}

func scalarString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return strings.TrimSpace(s)
	}
	var n json.Number
	if err := json.Unmarshal(raw, &n); err == nil {
		return strings.TrimSpace(n.String())
	}
	return ""
}

func (f emailFields) strList(names ...string) []string {
	raw := f.raw(names...)
	if len(raw) == 0 {
		return nil
	}
	var one string
	if err := json.Unmarshal(raw, &one); err == nil {
		return []string{strings.TrimSpace(one)}
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err == nil {
		out := make([]string, 0, len(many))
		for _, v := range many {
			if t := strings.TrimSpace(v); t != "" {
				out = append(out, t)
			}
		}
		return out
	}
	return nil
}

// emailBodies picks the plain/HTML parts. Every alias is considered; the
// provider only sets the preference order when a payload carries more than one.
func emailBodies(provider string, f emailFields) (plain, htmlBody string) {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case EmailProviderMailgun:
		return f.str("body-plain", "stripped-text", "text", "body"),
			f.str("body-html", "stripped-html", "html")
	case EmailProviderSendGrid:
		return f.str("text", "body", "body-plain"),
			f.str("html", "body-html")
	case EmailProviderPostmark:
		return f.str("TextBody", "text", "body-plain", "body"),
			f.str("HtmlBody", "html", "body-html")
	default:
		return f.str("text", "body-plain", "body", "stripped-text", "TextBody"),
			f.str("html", "body-html", "HtmlBody")
	}
}

// emailHeaderSet is a case-insensitive header map. The first occurrence wins so
// a provider that repeats a header cannot flip the ignore verdict.
type emailHeaderSet map[string]string

func (s emailHeaderSet) set(name, value string) {
	k := strings.ToLower(strings.TrimSpace(name))
	v := strings.TrimSpace(value)
	if k == "" || v == "" {
		return
	}
	if _, exists := s[k]; !exists {
		s[k] = v
	}
}

func (s emailHeaderSet) get(name string) string {
	return s[strings.ToLower(name)]
}

// decodeEmailHeaders collects the headers the ignore rules need. Vendors expose
// them differently: Mailgun `message-headers` is an array of [name,value] pairs,
// Postmark `Headers` is an array of {Name,Value}, SendGrid `headers` is the raw
// header block. All three are parsed here, plus flat generic fields.
func decodeEmailHeaders(f emailFields) emailHeaderSet {
	set := emailHeaderSet{}
	for _, key := range []string{"messageheaders", "headers"} {
		if raw, ok := f[key]; ok {
			set.addRaw(raw)
		}
	}
	for _, name := range []string{"auto-submitted", "precedence", "return-path"} {
		if v := f.str(name); v != "" {
			set.set(name, v)
		}
	}
	return set
}

func (s emailHeaderSet) addRaw(raw json.RawMessage) {
	var block string
	if err := json.Unmarshal(raw, &block); err == nil {
		// Mailgun's message-headers is a JSON-encoded array; some senders post it
		// as a JSON string, so unwrap before falling back to a raw header block.
		if trimmed := strings.TrimSpace(block); strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "{") {
			s.addRaw(json.RawMessage(trimmed))
			return
		}
		for _, pair := range parseHeaderBlock(block) {
			s.set(pair[0], pair[1])
		}
		return
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err != nil {
		return
	}
	for _, item := range list {
		var pair []string
		if err := json.Unmarshal(item, &pair); err == nil && len(pair) >= 2 {
			s.set(pair[0], pair[1])
			continue
		}
		var obj struct {
			Name  string `json:"Name"`
			Value string `json:"Value"`
		}
		if err := json.Unmarshal(item, &obj); err == nil && obj.Name != "" {
			s.set(obj.Name, obj.Value)
		}
	}
}

func parseHeaderBlock(block string) [][2]string {
	var out [][2]string
	var current *[2]string
	for _, line := range strings.Split(strings.ReplaceAll(block, "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if current != nil {
				current[1] += " " + strings.TrimSpace(line)
			}
			continue
		}
		i := strings.Index(line, ":")
		if i <= 0 {
			current = nil
			continue
		}
		out = append(out, [2]string{strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])})
		current = &out[len(out)-1]
	}
	return out
}

// recipients extracts the destination addresses. The first one is the binding's
// own mailbox for inbound mail and therefore the loop-guard comparison value.
func (f emailFields) recipients() []string {
	for _, name := range []string{"to", "recipient", "original_recipient", "delivered_to", "envelope"} {
		if raw := f.raw(name); len(raw) > 0 {
			if out := parseRecipients(raw); len(out) > 0 {
				return out
			}
		}
	}
	return nil
}

func parseRecipients(raw json.RawMessage) []string {
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		// SendGrid's `envelope` is a JSON object encoded as a string.
		if strings.HasPrefix(s, "{") {
			var env struct {
				To []string `json:"to"`
			}
			if err := json.Unmarshal([]byte(s), &env); err == nil && len(env.To) > 0 {
				return normalizeAddresses(env.To)
			}
		}
		return normalizeAddresses(strings.Split(s, ","))
	}
	var list []json.RawMessage
	if err := json.Unmarshal(raw, &list); err == nil {
		var out []string
		for _, item := range list {
			out = append(out, parseRecipients(item)...)
		}
		return out
	}
	var obj struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(raw, &obj); err == nil {
		return normalizeAddresses([]string{obj.Email})
	}
	return nil
}

func normalizeAddresses(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if a, err := mail.ParseAddress(v); err == nil {
			v = a.Address
		}
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// emailReceivedAt prefers the message Date header; Mailgun's `timestamp` is the
// delivery time in Unix seconds and is only a fallback.
func emailReceivedAt(f emailFields) time.Time {
	if t := parseTime(f.str("date", "sent_at")); !t.IsZero() {
		return t
	}
	if ts := f.str("timestamp"); ts != "" {
		if sec, err := strconv.ParseInt(ts, 10, 64); err == nil && sec > 0 {
			return time.Unix(sec, 0).UTC()
		}
	}
	return time.Time{}
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
