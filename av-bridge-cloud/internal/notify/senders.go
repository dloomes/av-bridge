package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/smtp"
	"strings"
	"time"
)

// Senders is the default SenderRegistry — switches on Channel.Type and
// hands off to the right transport. Dev-friendly: if SMTP isn't configured,
// email sends log the message instead of failing, so the alerts loop can be
// exercised end-to-end without a real mail server.
type Senders struct {
	smtp    SMTPConfig
	httpc   *http.Client
	log     *slog.Logger
	dryRun  bool // true when SMTP unconfigured — emails log instead of send
}

// SMTPConfig holds the per-process SMTP relay credentials. Empty SMTPHost
// flips Senders into dry-run mode (log + treat as success). Real deployments
// configure these via env (POC_SMTP_*).
type SMTPConfig struct {
	Host     string
	Port     string // "587" / "465" / "25"
	Username string
	Password string
	From     string
}

func NewSenders(cfg SMTPConfig, log *slog.Logger) *Senders {
	return &Senders{
		smtp:   cfg,
		httpc:  &http.Client{Timeout: 8 * time.Second},
		log:    log,
		dryRun: cfg.Host == "",
	}
}

func (s *Senders) Send(ctx context.Context, ch Channel, evt AlertEvent) error {
	switch ch.Type {
	case "email":
		return s.sendEmail(ctx, ch, evt)
	case "teams":
		return s.sendTeams(ctx, ch, evt)
	case "webhook":
		return s.sendWebhook(ctx, ch, evt)
	default:
		return ErrUnsupportedChannel
	}
}

// ---- email -----------------------------------------------------------------

func (s *Senders) sendEmail(ctx context.Context, ch Channel, evt AlertEvent) error {
	if ch.Target == "" {
		return errors.New("email target is empty")
	}
	subject, body := renderEmail(evt)

	if s.dryRun {
		s.log.Info("notify: email dry-run (SMTP not configured)",
			"to", ch.Target, "subject", subject)
		return nil
	}

	from := s.smtp.From
	if from == "" {
		from = "alerts@av-bridge.local"
	}
	addr := s.smtp.Host + ":" + s.smtp.Port
	msg := buildRFC822(from, ch.Target, subject, body)

	var auth smtp.Auth
	if s.smtp.Username != "" {
		auth = smtp.PlainAuth("", s.smtp.Username, s.smtp.Password, s.smtp.Host)
	}

	// net/smtp doesn't honour context directly — run the blocking call in a
	// goroutine and abandon on ctx timeout. The relay may continue to attempt
	// delivery but at least the dispatcher returns promptly.
	errCh := make(chan error, 1)
	go func() {
		errCh <- smtp.SendMail(addr, auth, from, []string{ch.Target}, msg)
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func buildRFC822(from, to, subject, body string) []byte {
	var b bytes.Buffer
	fmt.Fprintf(&b, "From: %s\r\n", from)
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	fmt.Fprintf(&b, "MIME-Version: 1.0\r\n")
	fmt.Fprintf(&b, "Content-Type: text/plain; charset=utf-8\r\n")
	fmt.Fprintf(&b, "\r\n")
	b.WriteString(body)
	return b.Bytes()
}

func renderEmail(evt AlertEvent) (subject, body string) {
	severity := strings.ToUpper(evt.Severity)
	subject = fmt.Sprintf("[%s] %s — %s", severity, evt.DeviceName, evt.AlertKey)

	var b strings.Builder
	fmt.Fprintf(&b, "Severity: %s\n", severity)
	fmt.Fprintf(&b, "Device:   %s\n", evt.DeviceName)
	fmt.Fprintf(&b, "Alert:    %s\n", evt.AlertKey)
	fmt.Fprintf(&b, "Opened:   %s\n", evt.OpenedAt.UTC().Format(time.RFC3339))
	if evt.Message != "" {
		fmt.Fprintf(&b, "\n%s\n", evt.Message)
	}
	body = b.String()
	return
}

// ---- Teams (incoming webhook) ----------------------------------------------
//
// Microsoft Teams "Incoming Webhook" connector accepts adaptive cards via
// POST to the per-channel URL the user pastes into our settings. We send a
// minimal card with severity-coloured theme and the alert text; the webhook
// returns 200 on success.

func (s *Senders) sendTeams(ctx context.Context, ch Channel, evt AlertEvent) error {
	if ch.Target == "" {
		return errors.New("teams webhook URL is empty")
	}
	colour := "0078D4"
	switch evt.Severity {
	case "critical":
		colour = "D13438"
	case "warning":
		colour = "F2C811"
	}
	body := map[string]any{
		"@type":      "MessageCard",
		"@context":   "https://schema.org/extensions",
		"themeColor": colour,
		"summary":    fmt.Sprintf("%s: %s", evt.DeviceName, evt.AlertKey),
		"title":      fmt.Sprintf("[%s] %s", strings.ToUpper(evt.Severity), evt.DeviceName),
		"text":       evt.Message,
		"sections": []map[string]any{{
			"facts": []map[string]string{
				{"name": "Alert", "value": evt.AlertKey},
				{"name": "Opened", "value": evt.OpenedAt.UTC().Format(time.RFC3339)},
			},
		}},
	}
	return s.postJSON(ctx, ch.Target, body)
}

// ---- generic webhook -------------------------------------------------------
//
// Posts the alert as plain JSON. Use this for ServiceNow / Dynamics / a Zap /
// anything that accepts a webhook. Headers in Channel.Config (e.g.
// Authorization) are passed through for callers that need bearer auth.

func (s *Senders) sendWebhook(ctx context.Context, ch Channel, evt AlertEvent) error {
	if ch.Target == "" {
		return errors.New("webhook URL is empty")
	}
	body := map[string]any{
		"customer_id": evt.CustomerID,
		"device_id":   evt.DeviceID,
		"device_name": evt.DeviceName,
		"alert_key":   evt.AlertKey,
		"severity":    evt.Severity,
		"message":     evt.Message,
		"opened_at":   evt.OpenedAt.UTC().Format(time.RFC3339),
		"payload":     evt.Payload,
	}
	headers := map[string]string{}
	if hdrs, ok := ch.Config["headers"].(map[string]any); ok {
		for k, v := range hdrs {
			if s, ok := v.(string); ok {
				headers[k] = s
			}
		}
	}
	return s.postJSONWithHeaders(ctx, ch.Target, body, headers)
}

func (s *Senders) postJSON(ctx context.Context, url string, payload any) error {
	return s.postJSONWithHeaders(ctx, url, payload, nil)
}

func (s *Senders) postJSONWithHeaders(ctx context.Context, url string, payload any, headers map[string]string) error {
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}
