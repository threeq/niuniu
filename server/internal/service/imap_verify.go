// Package service: minimal IMAP login probe for bind-time credential
// validation (office-mail, spec §11 — non-technical users need immediate
// feedback on a wrong password/host). This is intentionally NOT a mail client:
// it does one LOGIN handshake (optionally STARTTLS-upgrading first) and LOGOUT,
// just enough to confirm the credential authenticates. Reading/sending mail
// stays entirely in the MCP server.
package service

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"strings"
	"time"
)

// imapVerifyTimeout bounds the whole connect+login probe.
const imapVerifyTimeout = 12 * time.Second

// ErrIMAPAuth is returned when the server rejects the credentials (tagged NO/BAD
// to LOGIN) — distinguishable from connect/network errors so callers can give a
// precise "wrong username or password" message.
type ErrIMAPAuth struct{ Detail string }

func (e *ErrIMAPAuth) Error() string { return "imap login rejected: " + e.Detail }

// VerifyImapLogin connects to host:port (implicit TLS when security=="ssl"/"",
// STARTTLS when "starttls", plaintext when "none") and performs a LOGIN with
// user/pass. Returns nil on success, *ErrIMAPAuth on credential rejection, or a
// wrapped connect/IO error otherwise. Defaults port by security when 0.
func VerifyImapLogin(ctx context.Context, host string, port int, security, user, pass string) error {
	security = strings.ToLower(strings.TrimSpace(security))
	if port == 0 {
		if security == "starttls" || security == "none" {
			port = 143
		} else {
			port = 993
		}
	}
	deadline := time.Now().Add(imapVerifyTimeout)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", port))
	d := net.Dialer{Deadline: deadline}

	var conn net.Conn
	var err error
	if security == "" || security == "ssl" || security == "tls" || security == "implicit" {
		conn, err = tls.DialWithDialer(&d, "tcp", addr, &tls.Config{ServerName: host})
	} else {
		conn, err = d.DialContext(ctx, "tcp", addr)
	}
	if err != nil {
		return fmt.Errorf("connect %s: %w", addr, err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(deadline)

	br := bufio.NewReader(conn)
	if _, err := br.ReadString('\n'); err != nil { // server greeting
		return fmt.Errorf("read greeting: %w", err)
	}

	if security == "starttls" {
		upgraded, err := imapStartTLS(br, conn, host)
		if err != nil {
			return err
		}
		conn = upgraded
		_ = conn.SetDeadline(deadline)
		br = bufio.NewReader(conn)
	}

	return imapLoginExchange(br, conn, user, pass)
}

// imapStartTLS issues STARTTLS over the plaintext conn and returns the upgraded
// TLS connection.
func imapStartTLS(br *bufio.Reader, conn net.Conn, host string) (net.Conn, error) {
	if _, err := io.WriteString(conn, "a0 STARTTLS\r\n"); err != nil {
		return nil, fmt.Errorf("write STARTTLS: %w", err)
	}
	if err := imapAwaitTag(br, "a0"); err != nil {
		return nil, fmt.Errorf("STARTTLS: %w", err)
	}
	tconn := tls.Client(conn, &tls.Config{ServerName: host})
	if err := tconn.Handshake(); err != nil {
		return nil, fmt.Errorf("tls handshake: %w", err)
	}
	return tconn, nil
}

// imapLoginExchange writes a quoted LOGIN and inspects the tagged reply. Split
// out (taking an io.Reader/Writer) so it is unit-testable against a scripted
// connection without a real server.
func imapLoginExchange(r *bufio.Reader, w io.Writer, user, pass string) error {
	cmd := fmt.Sprintf("a1 LOGIN %s %s\r\n", imapQuote(user), imapQuote(pass))
	if _, err := io.WriteString(w, cmd); err != nil {
		return fmt.Errorf("write LOGIN: %w", err)
	}
	if err := imapAwaitTag(r, "a1"); err != nil {
		return err
	}
	_, _ = io.WriteString(w, "a2 LOGOUT\r\n") // best-effort
	return nil
}

// imapAwaitTag reads response lines until the one tagged `tag`, returning nil on
// OK, *ErrIMAPAuth on NO/BAD. Untagged (`*`) lines are skipped.
func imapAwaitTag(r *bufio.Reader, tag string) error {
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			if line == "" {
				return fmt.Errorf("read response: %w", err)
			}
		}
		fields := strings.Fields(strings.TrimRight(line, "\r\n"))
		if len(fields) < 2 || fields[0] != tag {
			if err != nil {
				return fmt.Errorf("read response: %w", err)
			}
			continue // untagged / continuation
		}
		switch strings.ToUpper(fields[1]) {
		case "OK":
			return nil
		default: // NO / BAD
			return &ErrIMAPAuth{Detail: strings.TrimSpace(strings.TrimRight(line, "\r\n"))}
		}
	}
}

// imapQuote renders an IMAP quoted string, escaping `\` and `"`.
func imapQuote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
