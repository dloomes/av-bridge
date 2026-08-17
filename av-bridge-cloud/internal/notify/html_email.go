package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/smtp"
	"strings"
	"time"
)

// SendHTMLEmail sends a multipart/alternative message with a plain-text
// fallback (for clients that reject HTML) and an HTML body (the default).
//
// Supports multiple To + Cc recipients — used by the nightly digest sender
// which fans a single message out to the customer's notification email
// channels plus the vendor helpdesk address.
//
// If cfg.Host is empty, runs in dry-run mode: logs the intended send and
// returns nil so callers can exercise the full path without a real relay.
// Symmetric with the alert-sender behaviour in senders.go.
func SendHTMLEmail(
	ctx context.Context,
	cfg SMTPConfig,
	to []string,
	cc []string,
	subject string,
	htmlBody string,
	textBody string,
	log *slog.Logger,
) error {
	recipients := dedupeRecipients(to, cc)
	if len(recipients) == 0 {
		return errors.New("no recipients")
	}

	fromHeader := cfg.From
	if fromHeader == "" {
		fromHeader = "alerts@av-bridge.local"
	}
	// SMTP MAIL FROM envelope needs a bare address; a display-name form
	// like "AV Bridge <noreply@…>" trips SES with 553 Invalid email
	// address. Parse the configured value once, hand the envelope the
	// bare address, keep the display-name form on the RFC822 From:
	// header so the recipient's client shows the friendly name. Same
	// fix as senders.go — must live here too because SendHTMLEmail
	// bypasses that path.
	envelopeFrom := fromHeader
	if parsed, err := mail.ParseAddress(fromHeader); err == nil {
		envelopeFrom = parsed.Address
	}

	if cfg.Host == "" {
		log.Info("digest: SMTP dry-run (SMTP not configured)",
			"to", strings.Join(to, ","),
			"cc", strings.Join(cc, ","),
			"subject", subject,
			"html_bytes", len(htmlBody),
		)
		return nil
	}

	msg := buildMultipartMessage(fromHeader, to, cc, subject, htmlBody, textBody)
	addr := cfg.Host + ":" + cfg.Port

	var auth smtp.Auth
	if cfg.Username != "" {
		auth = smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- smtp.SendMail(addr, auth, envelopeFrom, recipients, msg)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// dedupeRecipients merges To + Cc into a single envelope RCPT-TO list with
// case-insensitive dedup. Preserves order (To first, Cc second) so the
// server sees a stable envelope.
func dedupeRecipients(to, cc []string) []string {
	seen := make(map[string]struct{}, len(to)+len(cc))
	out := make([]string, 0, len(to)+len(cc))
	for _, addr := range append(append([]string{}, to...), cc...) {
		trimmed := strings.TrimSpace(addr)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, dup := seen[key]; dup {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, trimmed)
	}
	return out
}

func buildMultipartMessage(from string, to, cc []string, subject, htmlBody, textBody string) []byte {
	boundary := randomBoundary()

	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", strings.Join(to, ", "))
	if len(cc) > 0 {
		fmt.Fprintf(&b, "Cc: %s\r\n", strings.Join(cc, ", "))
	}
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "Date: %s\r\n", time.Now().UTC().Format(time.RFC1123Z))
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: multipart/alternative; boundary=\"%s\"\r\n", boundary)
	fmt.Fprintf(&b, "\r\n")

	// Text part first — per RFC 2046 the last part is the preferred one; HTML
	// wins in modern clients, plain-text falls through in text-only clients.
	fmt.Fprintf(&b, "--%s\r\n", boundary)
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&b, "Content-Transfer-Encoding: 8bit\r\n")
	fmt.Fprintf(&b, "\r\n")
	b.WriteString(textBody)
	fmt.Fprintf(&b, "\r\n")

	fmt.Fprintf(&b, "--%s\r\n", boundary)
	fmt.Fprintf(&b, "Content-Type: text/html; charset=utf-8\r\n")
	fmt.Fprintf(&b, "Content-Transfer-Encoding: 8bit\r\n")
	fmt.Fprintf(&b, "\r\n")
	b.WriteString(htmlBody)
	fmt.Fprintf(&b, "\r\n")

	fmt.Fprintf(&b, "--%s--\r\n", boundary)
	return b.Bytes()
}

func randomBoundary() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// Fall back to a timestamp — collisions inside a single message are
		// impossible; only cross-message uniqueness suffers slightly.
		return fmt.Sprintf("boundary_%d", time.Now().UnixNano())
	}
	return "boundary_" + hex.EncodeToString(buf[:])
}
