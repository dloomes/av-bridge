package notify

import (
	"fmt"
	"html"
	"strings"
	"time"
)

// Alert email templates. Two entry points:
//   renderAlertSubject — the RFC822 Subject header
//   renderAlertText    — plain-text body, for clients that reject HTML
//   renderAlertHTML    — HTML body, table-laid-out and inline-styled to
//                        survive Gmail, Outlook, Apple Mail on desktop + web
//                        + iOS/Android without a <style> block.
//
// Palette + font stack match the nightly digest so alerts feel like part
// of the same product. Only new pattern here is the severity chip beneath
// the header band, mirrored across the plain-text body so ops can grep
// text-mode logs by "[CRITICAL]".

// severityColours returns the header band + chip colours for a given
// severity string. Unknown severities fall through to the info palette so
// a broken event still renders — better than an empty template.
func severityColours(severity string) (headerBg, chipBg, chipText string) {
	switch strings.ToLower(severity) {
	case "critical":
		return "#b91c1c", "#fef2f2", "#7f1d1d"
	case "warning":
		return "#a16207", "#fef3c7", "#78350f"
	default:
		return "#0f172a", "#e0f2fe", "#075985"
	}
}

func renderAlertSubject(evt AlertEvent) string {
	return fmt.Sprintf("[%s] %s — %s",
		strings.ToUpper(evt.Severity),
		evt.SubjectName(),
		evt.AlertKey,
	)
}

// renderAlertText is the plain-text fallback body. Kept succinct — mirrors
// the HTML content so a text-only client sees the same facts, just
// unstyled. Same fields as the HTML version so ops can trust either.
func renderAlertText(evt AlertEvent) string {
	severity := strings.ToUpper(evt.Severity)
	var b strings.Builder
	fmt.Fprintf(&b, "AV BRIDGE ALERT\n\n")
	fmt.Fprintf(&b, "[%s] %s\n", severity, evt.SubjectName())
	fmt.Fprintf(&b, "%s — %s\n\n", evt.SubjectLabel(), evt.AlertKey)
	if evt.Message != "" {
		fmt.Fprintf(&b, "%s\n\n", evt.Message)
	}
	fmt.Fprintf(&b, "Severity:  %s\n", severity)
	fmt.Fprintf(&b, "%-9s %s\n", evt.SubjectLabel()+":", evt.SubjectName())
	fmt.Fprintf(&b, "Alert:     %s\n", evt.AlertKey)
	fmt.Fprintf(&b, "Opened:    %s\n", evt.OpenedAt.UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "\n—\nYou received this because your notification channel is subscribed to %s-level alerts and above.\n", strings.ToLower(severity))
	return b.String()
}

// renderAlertHTML mirrors the nightly digest's visual grammar: slate page
// background, single 600-px card centred, severity-coloured header band,
// summary chip, table of facts, subtle footer. Inline styles only —
// Gmail's web/mobile clients strip <style> blocks in most contexts, and
// Outlook 2016+ still uses Word for rendering (tables mandatory).
//
// The preheader trick at the top uses hidden text (display:none + hidden
// attr + zero-width padding) that inbox previews render before hiding the
// rest — so the recipient sees a summary before opening.
func renderAlertHTML(evt AlertEvent) string {
	severity := strings.ToUpper(evt.Severity)
	headerBg, chipBg, chipText := severityColours(evt.Severity)

	subjectName := html.EscapeString(evt.SubjectName())
	subjectLabel := html.EscapeString(evt.SubjectLabel())
	alertKey := html.EscapeString(evt.AlertKey)
	message := strings.TrimSpace(evt.Message)
	openedAt := evt.OpenedAt.UTC().Format("2 Jan 2006 15:04 UTC")

	// Preheader: first line an inbox preview shows before it hides. Best
	// effort — Gmail respects display:none, Apple Mail respects hidden,
	// belt-and-braces both plus a trailing zero-width padding so the
	// preview doesn't pick up the "AV BRIDGE ALERT" eyebrow.
	preheaderRaw := fmt.Sprintf("%s alert on %s", strings.Title(strings.ToLower(evt.Severity)), evt.SubjectName())
	if message != "" {
		preheaderRaw += " — " + message
	}
	if len(preheaderRaw) > 140 {
		preheaderRaw = preheaderRaw[:140] + "…"
	}
	preheader := html.EscapeString(preheaderRaw)

	var b strings.Builder

	b.WriteString(`<!doctype html><html><body style="margin:0;padding:0;background:#f5f6f8;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Arial,sans-serif;color:#0f172a;">`)

	// Preheader — invisible to the reader, visible to the inbox preview.
	fmt.Fprintf(&b, `<div style="display:none;max-height:0;overflow:hidden;font-size:1px;line-height:1px;color:#f5f6f8;opacity:0;" aria-hidden="true">%s`, preheader)
	// Zero-width joiners pad out the preview so the eyebrow ("AV BRIDGE
	// ALERT") doesn't leak into the visible portion of the preview text.
	b.WriteString(strings.Repeat("&#847;&zwnj;", 20))
	b.WriteString(`</div>`)

	// Outer wrapper — slate page bg, centred column.
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f5f6f8;padding:24px 12px;"><tr><td align="center">`)
	b.WriteString(`<table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px;background:#ffffff;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(15,23,42,0.08);">`)

	// Header band — severity-coloured. Inbox previews often show a colour
	// hint from the first few hundred pixels, so this doubles as an
	// at-a-glance severity signal even before the recipient reads.
	fmt.Fprintf(&b, `<tr><td style="background:%s;padding:24px 32px;color:#ffffff;">`, headerBg)
	b.WriteString(`<div style="font-size:12px;letter-spacing:0.14em;text-transform:uppercase;opacity:0.85;">AV Bridge Alert</div>`)
	fmt.Fprintf(&b, `<div style="font-size:22px;font-weight:600;margin-top:6px;line-height:1.25;">%s</div>`, subjectName)
	fmt.Fprintf(&b, `<div style="font-size:14px;margin-top:4px;opacity:0.9;">%s — <span style="font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;">%s</span></div>`, subjectLabel, alertKey)
	b.WriteString(`</td></tr>`)

	// Severity chip row — small coloured pill matching the header. Not
	// strictly necessary given the header colour, but reinforces the
	// severity for users who scan below the fold on mobile.
	fmt.Fprintf(&b, `<tr><td style="padding:20px 32px 0;"><span style="display:inline-block;padding:4px 12px;border-radius:999px;background:%s;color:%s;font-size:11px;font-weight:600;letter-spacing:0.14em;text-transform:uppercase;">%s</span></td></tr>`, chipBg, chipText, html.EscapeString(severity))

	// Message body — the reason the alert exists.
	if message != "" {
		fmt.Fprintf(&b, `<tr><td style="padding:14px 32px 0;"><div style="font-size:15px;line-height:1.55;color:#0f172a;white-space:pre-wrap;">%s</div></td></tr>`, html.EscapeString(message))
	}

	// Details table — the machine-readable facts.
	b.WriteString(`<tr><td style="padding:20px 32px 24px;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="border-top:1px solid #e2e8f0;">`)
	writeAlertDetailRow(&b, subjectLabel, subjectName, false)
	writeAlertDetailRow(&b, "Alert", alertKey, true)
	writeAlertDetailRow(&b, "Opened", html.EscapeString(openedAt), false)
	b.WriteString(`</table>`)
	b.WriteString(`</td></tr>`)

	// Footer — thin muted band explaining why the recipient got this.
	// Kept generic (no per-channel name) so we don't need to plumb the
	// Channel struct into the renderer.
	b.WriteString(`<tr><td style="padding:16px 32px 20px;background:#f8fafc;border-top:1px solid #e2e8f0;color:#64748b;font-size:12px;line-height:1.5;">`)
	fmt.Fprintf(&b, `You received this because your notification channel is subscribed to <strong>%s</strong>-level alerts and above. Manage your channels in the av-bridge portal.`, html.EscapeString(strings.ToLower(severity)))
	b.WriteString(`</td></tr>`)

	b.WriteString(`</table></td></tr></table></body></html>`)
	return b.String()
}

func writeAlertDetailRow(b *strings.Builder, label, value string, mono bool) {
	valueStyle := "padding:12px 0;font-size:14px;color:#0f172a;"
	if mono {
		valueStyle += "font-family:ui-monospace,SFMono-Regular,Menlo,Consolas,monospace;"
	}
	fmt.Fprintf(b, `<tr>`)
	fmt.Fprintf(b, `<td style="padding:12px 0;color:#64748b;font-size:11px;text-transform:uppercase;letter-spacing:0.08em;width:110px;vertical-align:top;">%s</td>`, label)
	fmt.Fprintf(b, `<td style="%s">%s</td>`, valueStyle, value)
	fmt.Fprintf(b, `</tr>`)
}
