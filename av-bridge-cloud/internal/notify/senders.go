package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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

// sendEmail delegates to the multipart HTML+text sender so alert notifications
// use the same rendering path as the nightly digest — inline-styled HTML for
// the visual client, plain-text fallback for anything that rejects HTML.
// Templates live in alert_template.go.
func (s *Senders) sendEmail(ctx context.Context, ch Channel, evt AlertEvent) error {
	if ch.Target == "" {
		return errors.New("email target is empty")
	}
	subject := renderAlertSubject(evt)
	textBody := renderAlertText(evt)
	htmlBody := renderAlertHTML(evt)
	return SendHTMLEmail(ctx, s.smtp, []string{ch.Target}, nil, subject, htmlBody, textBody, s.log)
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
		"summary":    fmt.Sprintf("%s: %s", evt.SubjectName(), evt.AlertKey),
		"title":      fmt.Sprintf("[%s] %s", strings.ToUpper(evt.Severity), evt.SubjectName()),
		"text":       evt.Message,
		"sections": []map[string]any{{
			"facts": []map[string]string{
				{"name": evt.SubjectLabel(), "value": evt.SubjectName()},
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
		"alert_key":   evt.AlertKey,
		"severity":    evt.Severity,
		"message":     evt.Message,
		"opened_at":   evt.OpenedAt.UTC().Format(time.RFC3339),
		"payload":     evt.Payload,
	}
	// Only include the subject fields that are actually populated — so a
	// collector alert doesn't ship an empty device_id/device_name and
	// vice-versa.
	if evt.CollectorID != "" {
		body["collector_id"] = evt.CollectorID
		body["collector_name"] = evt.CollectorName
	} else {
		body["device_id"] = evt.DeviceID
		body["device_name"] = evt.DeviceName
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
