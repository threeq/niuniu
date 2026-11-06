package service

// MIME parsing for the IMAP email Source (design §6.2). Everything here is a
// pure function: raw RFC 5322 bytes in, normalized trigger fields out — no
// network and no database. The poller in email_source.go owns the lifecycle.
//
// A message the parser cannot read is reported as an error; the poller advances
// past it so a single malformed message can never wedge a mailbox.

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"net/textproto"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/html"
	"golang.org/x/text/encoding/htmlindex"
	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"

	"github.com/niuniu-dev/niuniu/internal/intake"
)

// emailBodyLimit caps the stored body (design §6.2: 64KB). A longer body is
// truncated on a rune boundary so the result is still valid UTF-8.
const emailBodyLimit = 64 << 10

// emailMIMEDepth bounds nested multipart recursion on hostile input.
const emailMIMEDepth = 16

// parsedEmail is one message reduced to what the intake pipeline needs.
type parsedEmail struct {
	MessageID  string
	InReplyTo  string
	References []string
	// ThreadRoot is the conversation key: the first References entry, else
	// In-Reply-To, else this message's own Message-ID. A reply therefore
	// resolves to the task its root created.
	ThreadRoot  string
	FromAddress string
	FromName    string
	Subject     string
	Date        time.Time
	Text        string
	Attachments []intake.Attachment

	// IgnoreReason is non-empty when the message must not reach the intake
	// pipeline (auto-reply, bulk mail, bounce, or a message from ourselves).
	IgnoreReason string
}

// emailEnvelope holds the headers the ignore rules and threading need.
type emailEnvelope struct {
	AutoSubmitted string
	Precedence    string
	ReturnPath    string
}

// parseEmailMessage decodes raw RFC 5322 bytes. selfAddress is the binding's
// own mailbox (used for the loop guard); ignoreAuto turns the design's default
// ignore rules off when the binding sets ignore_auto=false.
func parseEmailMessage(raw []byte, selfAddress string, ignoreAuto bool) (parsedEmail, error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return parsedEmail{}, fmt.Errorf("email parse: read message: %w", err)
	}
	dec := newEmailWordDecoder()
	hdr := msg.Header

	p := parsedEmail{
		MessageID: trimMessageID(hdr.Get("Message-Id")),
		InReplyTo: trimMessageID(hdr.Get("In-Reply-To")),
		Subject:   decodeEmailHeader(dec, hdr.Get("Subject")),
	}
	p.References = messageIDList(hdr.Get("References"))
	if t, err := hdr.Date(); err == nil {
		p.Date = t
	}
	p.FromAddress, p.FromName = parseFromHeader(hdr.Get("From"))

	var plain, htmlBody string
	if err := walkMIMEPart(textproto.MIMEHeader(hdr), msg.Body, &p, &plain, &htmlBody, 0); err != nil {
		return parsedEmail{}, err
	}

	body := strings.TrimSpace(plain)
	if body == "" {
		body = htmlToText(htmlBody)
	}
	body = stripQuotedReply(body)
	body = truncateUTF8(body, emailBodyLimit)

	text := p.Subject
	if body != "" {
		if text != "" {
			text += "\n\n"
		}
		text += body
	}
	p.Text = strings.TrimSpace(text)

	p.ThreadRoot = firstNonEmpty(p.firstReference(), p.InReplyTo, p.MessageID)

	if ignoreAuto {
		env := emailEnvelope{
			AutoSubmitted: strings.TrimSpace(hdr.Get("Auto-Submitted")),
			Precedence:    strings.ToLower(strings.TrimSpace(hdr.Get("Precedence"))),
			ReturnPath:    strings.TrimSpace(hdr.Get("Return-Path")),
		}
		p.IgnoreReason = emailIgnoreReason(env, p.FromAddress, selfAddress)
	}
	return p, nil
}

func (p parsedEmail) firstReference() string {
	if len(p.References) == 0 {
		return ""
	}
	return p.References[0]
}

// emailIgnoreReason applies the design's default ignore rules: system
// auto-replies, bulk/list mail, bounces, and the binding's own address (a
// message we sent would otherwise create work for ourselves on every poll).
func emailIgnoreReason(env emailEnvelope, fromAddress, selfAddress string) string {
	if env.AutoSubmitted != "" && !strings.EqualFold(env.AutoSubmitted, "no") {
		return "auto-submitted"
	}
	switch env.Precedence {
	case "bulk", "junk", "list":
		return "precedence"
	}
	if env.ReturnPath == "<>" {
		return "bounce"
	}
	if selfAddress != "" && strings.EqualFold(strings.TrimSpace(fromAddress), strings.TrimSpace(selfAddress)) {
		return "self"
	}
	return ""
}

// --- conversion to the intake pipeline --------------------------------------

// emailRawPayload is what lands in intake_events.payload_json: the normalized
// message WITHOUT body-part attachment bytes. Attachments stay metadata-only;
// the task fetches one only if it actually needs it.
type emailRawPayload struct {
	MessageID   string              `json:"message_id"`
	InReplyTo   string              `json:"in_reply_to,omitempty"`
	References  []string            `json:"references,omitempty"`
	ThreadID    string              `json:"thread_id"`
	From        emailRawAddress     `json:"from"`
	Subject     string              `json:"subject"`
	Date        string              `json:"date,omitempty"`
	Text        string              `json:"text"`
	Attachments []intake.Attachment `json:"attachments,omitempty"`
}

type emailRawAddress struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// eventID is Message-ID when present, else the IMAP location. The location is
// stable across polls, so a redelivery still dedupes in the pipeline.
func (p parsedEmail) eventID(uidValidity, uid uint32) string {
	if p.MessageID != "" {
		return p.MessageID
	}
	return fmt.Sprintf("imap:%d:%d", uidValidity, uid)
}

func (p parsedEmail) trigger(projectID int64, sourceRef string, uidValidity, uid uint32, receivedAt time.Time) intake.Trigger {
	if receivedAt.IsZero() {
		receivedAt = p.Date
	}
	payload := emailRawPayload{
		MessageID:   p.MessageID,
		InReplyTo:   p.InReplyTo,
		References:  p.References,
		ThreadID:    p.ThreadRoot,
		From:        emailRawAddress{Email: p.FromAddress, Name: p.FromName},
		Subject:     p.Subject,
		Text:        p.Text,
		Attachments: p.Attachments,
	}
	if !p.Date.IsZero() {
		payload.Date = p.Date.UTC().Format(time.RFC3339)
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		// Marshaling only strings and structs cannot fail for a parsed message.
		raw = nil
	}
	name := p.FromName
	if name == "" {
		name = p.FromAddress
	}
	return intake.Trigger{
		ProjectID:   projectID,
		SourceKind:  intake.SourceKindEmail,
		SourceRef:   sourceRef,
		EventID:     p.eventID(uidValidity, uid),
		EventKind:   intake.EventKindMessage,
		Thread:      intake.ThreadRef{ThreadExtID: p.ThreadRoot},
		Actor:       intake.ActorRef{ExtID: p.FromAddress, Name: name},
		Text:        p.Text,
		Attachments: p.Attachments,
		Raw:         raw,
		ReceivedAt:  receivedAt,
		// The binding reads the whole mailbox, so an unlisted sender is an
		// inferred candidate — the same contract as a polled GitHub repo.
		ReceiveAll: true,
	}
}

// --- MIME walking ------------------------------------------------------------

func walkMIMEPart(h textproto.MIMEHeader, body io.Reader, p *parsedEmail, plain, htmlBody *string, depth int) error {
	if depth > emailMIMEDepth {
		return fmt.Errorf("email parse: multipart nesting too deep")
	}
	mediaType, params, err := mime.ParseMediaType(h.Get("Content-Type"))
	if err != nil || mediaType == "" {
		mediaType = "text/plain"
	}
	disposition, dispParams, _ := mime.ParseMediaType(h.Get("Content-Disposition"))
	filename := dispParams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	mediaType = strings.ToLower(mediaType)

	decoded := decodeTransferEncoding(body, h.Get("Content-Transfer-Encoding"))

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return nil
		}
		mr := multipart.NewReader(decoded, boundary)
		for {
			part, err := mr.NextPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("email parse: multipart part: %w", err)
			}
			// multipart strips Content-Transfer-Encoding after decoding
			// quoted-printable, so a remaining header means base64 only.
			if err := walkMIMEPart(part.Header, part, p, plain, htmlBody, depth+1); err != nil {
				return err
			}
		}
	}

	if strings.Contains(strings.ToLower(disposition), "attachment") || filename != "" ||
		!strings.HasPrefix(mediaType, "text/") && mediaType != "" {
		size, err := io.Copy(io.Discard, decoded)
		if err != nil {
			return fmt.Errorf("email parse: read attachment %q: %w", filename, err)
		}
		p.Attachments = append(p.Attachments, intake.Attachment{
			Kind:      emailAttachmentKind(mediaType, disposition),
			Name:      decodedFilename(filename, h),
			MIMEType:  mediaType,
			SizeBytes: size,
		})
		return nil
	}

	content, err := io.ReadAll(io.LimitReader(decoded, emailBodyLimit*8))
	if err != nil {
		return fmt.Errorf("email parse: read text part: %w", err)
	}
	text := decodeCharset(content, params["charset"])
	switch mediaType {
	case "text/html":
		if strings.TrimSpace(*htmlBody) == "" {
			*htmlBody = text
		}
	default: // text/plain and any other text/*
		if strings.TrimSpace(*plain) == "" {
			*plain = text
		}
	}
	return nil
}

func decodedFilename(filename string, h textproto.MIMEHeader) string {
	if filename == "" {
		return ""
	}
	return decodeEmailHeader(newEmailWordDecoder(), filename)
}

func decodeTransferEncoding(body io.Reader, cte string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, body)
	case "quoted-printable":
		return quotedprintable.NewReader(body)
	default:
		return body
	}
}

func emailAttachmentKind(mediaType, disposition string) string {
	switch {
	case strings.HasPrefix(mediaType, "image/"):
		return "image"
	case strings.HasPrefix(mediaType, "audio/"):
		return "audio"
	case strings.HasPrefix(mediaType, "video/"):
		return "video"
	}
	return "file"
}

// --- header and body helpers -------------------------------------------------

func newEmailWordDecoder() *mime.WordDecoder {
	return &mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
		enc, err := htmlindex.Get(charset)
		if err != nil || enc == unicode.UTF8 {
			return input, nil // unknown or already-UTF-8: pass the bytes through
		}
		return transform.NewReader(input, enc.NewDecoder()), nil
	}}
}

func decodeEmailHeader(dec *mime.WordDecoder, value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	if decoded, err := dec.DecodeHeader(value); err == nil {
		return strings.TrimSpace(decoded)
	}
	return strings.TrimSpace(value)
}

func decodeCharset(b []byte, charset string) string {
	cs := strings.ToLower(strings.TrimSpace(charset))
	if cs == "" || cs == "utf-8" || cs == "utf8" || cs == "us-ascii" || cs == "ascii" {
		return strings.ToValidUTF8(string(b), "")
	}
	enc, err := htmlindex.Get(cs)
	if err != nil || enc == unicode.UTF8 {
		return strings.ToValidUTF8(string(b), "")
	}
	out, err := io.ReadAll(transform.NewReader(bytes.NewReader(b), enc.NewDecoder()))
	if err != nil {
		return strings.ToValidUTF8(string(b), "")
	}
	return strings.ToValidUTF8(string(out), "")
}

// trimMessageID strips the angle brackets and surrounding whitespace that
// Message-ID / In-Reply-To carry in the wire format.
func trimMessageID(v string) string {
	s := strings.TrimSpace(v)
	s = strings.TrimPrefix(s, "<")
	s = strings.TrimSuffix(s, ">")
	return strings.TrimSpace(s)
}

// messageIDList extracts Message-IDs from a References header, which is a
// space-separated list of angle-bracketed ids.
func messageIDList(v string) []string {
	s := strings.TrimSpace(v)
	if s == "" {
		return nil
	}
	original := s
	var out []string
	for {
		start := strings.Index(s, "<")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], ">")
		if end < 0 {
			break
		}
		if id := trimMessageID(s[start : start+end+1]); id != "" {
			out = append(out, id)
		}
		s = s[start+end+1:]
	}
	if len(out) == 0 {
		// Tolerate servers that omit the angle brackets.
		for _, f := range strings.Fields(original) {
			if id := trimMessageID(f); id != "" {
				out = append(out, id)
			}
		}
	}
	return out
}

func parseFromHeader(v string) (address, name string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ""
	}
	if a, err := mail.ParseAddress(v); err == nil {
		return strings.ToLower(strings.TrimSpace(a.Address)), strings.TrimSpace(a.Name)
	}
	// Unparseable From: still keep the raw value so the actor is not empty.
	return strings.ToLower(v), ""
}

// htmlToText renders an HTML part as plain text. It is intentionally crude:
// block elements become line breaks, script/style/title are dropped, and blank
// runs collapse. Anything fancier would only add noise to a task prompt.
func htmlToText(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	doc, err := html.Parse(strings.NewReader(s))
	if err != nil {
		return strings.TrimSpace(s)
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "head", "title", "noscript":
				return
			case "br", "p", "div", "tr", "li", "ul", "ol", "table", "blockquote",
				"h1", "h2", "h3", "h4", "h5", "h6", "section", "article":
				b.WriteString("\n")
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(doc)
	return normalizeBlankLines(b.String())
}

var emailQuoteMarkers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^on\b.{0,300}\bwrote:\s*$`),
	regexp.MustCompile(`^在.{0,200}写道[:：]\s*$`),
	regexp.MustCompile(`(?i)^-{2,}\s*original message\s*-{2,}$`),
	regexp.MustCompile(`^-{2,}\s*原始邮件\s*-{2,}$`),
}

// stripQuotedReply removes the quoted history a mail client appends: the
// `> …` lines and everything from an "On … wrote:" / "在 … 写道:" marker down.
// A reply becomes just what the person actually wrote, which is what the agent
// needs to act on.
func stripQuotedReply(body string) string {
	lines := strings.Split(body, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isQuoteMarker(trimmed) {
			break
		}
		if strings.HasPrefix(trimmed, ">") {
			continue
		}
		kept = append(kept, line)
	}
	return normalizeBlankLines(strings.Join(kept, "\n"))
}

func isQuoteMarker(line string) bool {
	for _, re := range emailQuoteMarkers {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

func normalizeBlankLines(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.TrimRight(line, " \t\r")
		if strings.TrimSpace(line) == "" {
			blank++
			if blank > 1 {
				continue
			}
		} else {
			blank = 0
		}
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// truncateUTF8 cuts s to at most limit bytes without splitting a rune.
func truncateUTF8(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}
