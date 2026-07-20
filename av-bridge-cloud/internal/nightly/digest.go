// Digest sender — the "morning email" half of Room Readiness.
//
// Runs as a background goroutine alongside the lifecycle scheduler. Every
// tick, looks for customers whose lifecycle window has finished (i.e. it's
// past power_on_time + offset in their timezone) and haven't yet received
// their digest for the local calendar day. Aggregates the last night's
// runs into an HTML email and sends it to the customer's email
// notification channels, cc'd to the vendor helpdesk address.
//
// Idempotence: `nightly_schedule.digest_last_sent_for` stores the last
// local date we sent for. A cloud restart or duplicated tick within the
// same customer-local day is a no-op.
//
// Design reference: docs/nightly-lifecycle-spec.md §10.1.

package nightly

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/notify"
)

// DigestConfig bundles the sender's tunables.
type DigestConfig struct {
	// TickInterval — how often the sender wakes to look for due customers.
	// 60s matches the scheduler; keeps behaviour predictable and the sender
	// cheap even at moderate customer counts.
	TickInterval time.Duration

	// SendAfterOffset — how long after the customer's `power_on_time` we
	// wait before sending the digest. Gives every room time to complete
	// its warm-up + (Phase B) recipe. Spec default 30 minutes.
	SendAfterOffset time.Duration

	// SMTP relay config. Empty Host = dry-run (log the intended send).
	SMTP notify.SMTPConfig
}

// DigestSender is the orchestrator. One instance per cloud process.
type DigestSender struct {
	pool *pgxpool.Pool
	cfg  DigestConfig
	log  *slog.Logger
	// now overrides time.Now for tests. Nil in prod → uses time.Now().
	now func() time.Time
}

func NewDigestSender(pool *pgxpool.Pool, cfg DigestConfig, log *slog.Logger) *DigestSender {
	return &DigestSender{pool: pool, cfg: cfg, log: log}
}

// Run blocks until ctx is cancelled, ticking on cfg.TickInterval.
func (s *DigestSender) Run(ctx context.Context) {
	if s.cfg.TickInterval <= 0 {
		s.log.Warn("nightly digest sender disabled (tick_interval non-positive)")
		return
	}
	s.log.Info("nightly digest sender started",
		"tick", s.cfg.TickInterval,
		"send_after_offset", s.cfg.SendAfterOffset,
		"smtp_dry_run", s.cfg.SMTP.Host == "",
	)
	t := time.NewTicker(s.cfg.TickInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.tick(ctx)
		}
	}
}

// tick is one pass of the sender. Exposed so tests / the send-now endpoint
// can drive it deterministically.
func (s *DigestSender) tick(ctx context.Context) {
	customers, err := s.findDueCustomers(ctx)
	if err != nil {
		s.log.Warn("nightly digest: find due customers failed", "error", err)
		return
	}
	for _, c := range customers {
		if err := s.sendFor(ctx, c, false); err != nil {
			s.log.Warn("nightly digest: send failed",
				"customer", c.customerID, "error", err)
		}
	}
}

// SendForCustomer runs the digest pipeline for one customer immediately,
// bypassing the "already sent today" guard. Used by the portal's
// send-test endpoint so operators can preview the email without waiting.
func (s *DigestSender) SendForCustomer(ctx context.Context, customerID string) error {
	c, err := s.loadCustomer(ctx, customerID)
	if err != nil {
		return err
	}
	return s.sendFor(ctx, c, true)
}

// customerInfo bundles every field the digest needs about the customer.
type customerInfo struct {
	customerID       string
	customerName     string
	timezone         string
	powerOnTime      time.Duration
	helpdeskEmail    string
	digestLastSent   *time.Time // date-only, midnight-aligned
	enabled          bool
}

// findDueCustomers returns customers whose lifecycle window has finished
// today and who haven't been sent a digest yet for the local calendar day.
func (s *DigestSender) findDueCustomers(ctx context.Context) ([]customerInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ns.customer_id::text,
		       COALESCE(c.name, ''),
		       ns.timezone,
		       ns.power_on_time,
		       COALESCE(ns.helpdesk_email, ''),
		       ns.digest_last_sent_for,
		       ns.enabled
		  FROM nightly_schedule ns
		  JOIN customers c ON c.id = ns.customer_id
		 WHERE ns.enabled = true
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var all []customerInfo
	for rows.Next() {
		var (
			c            customerInfo
			powerOn      time.Time
			lastSent     *time.Time
		)
		if err := rows.Scan(
			&c.customerID, &c.customerName, &c.timezone,
			&powerOn, &c.helpdeskEmail, &lastSent, &c.enabled,
		); err != nil {
			return nil, err
		}
		c.powerOnTime = timeOfDayToDuration(powerOn)
		c.digestLastSent = lastSent
		all = append(all, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	nowUTC := s.wallNow().UTC()
	var due []customerInfo
	for _, c := range all {
		if !s.isDue(c, nowUTC) {
			continue
		}
		due = append(due, c)
	}
	return due, nil
}

// isDue is pure — the "send today?" check pulled out for testability.
func (s *DigestSender) isDue(c customerInfo, nowUTC time.Time) bool {
	loc, err := time.LoadLocation(c.timezone)
	if err != nil {
		return false
	}
	nowLocal := nowUTC.In(loc)

	// Guard against clock-skew: if the sender fires before power-on-time in
	// the customer tz, we shouldn't be sending anything yet today.
	sendAtLocal := time.Date(
		nowLocal.Year(), nowLocal.Month(), nowLocal.Day(),
		int(c.powerOnTime/time.Hour),
		int((c.powerOnTime%time.Hour)/time.Minute),
		0, 0, loc,
	).Add(s.cfg.SendAfterOffset)
	if nowLocal.Before(sendAtLocal) {
		return false
	}

	// Already sent for this local date → not due.
	if c.digestLastSent != nil {
		lastDate := c.digestLastSent.In(loc).Format("2006-01-02")
		todayDate := nowLocal.Format("2006-01-02")
		if lastDate >= todayDate {
			return false
		}
	}
	return true
}

func (s *DigestSender) loadCustomer(ctx context.Context, customerID string) (customerInfo, error) {
	var (
		c        customerInfo
		powerOn  time.Time
		lastSent *time.Time
	)
	err := s.pool.QueryRow(ctx, `
		SELECT ns.customer_id::text,
		       COALESCE(c.name, ''),
		       ns.timezone,
		       ns.power_on_time,
		       COALESCE(ns.helpdesk_email, ''),
		       ns.digest_last_sent_for,
		       ns.enabled
		  FROM nightly_schedule ns
		  JOIN customers c ON c.id = ns.customer_id
		 WHERE ns.customer_id = $1
	`, customerID).Scan(
		&c.customerID, &c.customerName, &c.timezone,
		&powerOn, &c.helpdeskEmail, &lastSent, &c.enabled,
	)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) || errors.Is(err, sql.ErrNoRows) {
			return c, ErrNoSchedule
		}
		return c, err
	}
	c.powerOnTime = timeOfDayToDuration(powerOn)
	c.digestLastSent = lastSent
	return c, nil
}

// ErrNoSchedule is returned by SendForCustomer when the customer has no
// nightly_schedule row (e.g. Room Readiness never configured for them).
// The send-now endpoint surfaces this as a 404.
var ErrNoSchedule = errors.New("nightly schedule not found for customer")

// runRow is the aggregated view of one nightly_run used by the digest.
type runRow struct {
	runID          string
	roomID         string
	roomName       string
	buildingName   string
	locationName   string
	regionName     string
	phase          string
	status         string
	scheduledAt    time.Time
	completedAt    *time.Time
	failureReason  string
	excludedUntil  *time.Time // via join; if non-nil AND >= today, the room was excluded
}

// collectRuns returns every run belonging to the customer whose
// scheduled_at falls within the previous 24 hours as of `now`. Includes
// rooms that were excluded (no run created), so the digest can list them.
func (s *DigestSender) collectRuns(ctx context.Context, customerID string, nowUTC time.Time) ([]runRow, []runRow, error) {
	// Runs from the past 24h — captures typical overnight cycles and
	// same-day test schedules. A 30h window would be safer against clock
	// drift but risks pulling two-nights-ago's runs when a customer misses
	// a night. 24h is the sweet spot.
	windowStart := nowUTC.Add(-24 * time.Hour)
	rows, err := s.pool.Query(ctx, `
		SELECT nr.id::text,
		       nr.room_id::text,
		       COALESCE(r.name, ''),
		       COALESCE(b.name, ''),
		       COALESCE(l.name, ''),
		       COALESCE(rg.name, ''),
		       nr.phase,
		       nr.status,
		       nr.scheduled_at,
		       nr.completed_at,
		       COALESCE(nr.failure_reason, '')
		  FROM nightly_run nr
		  JOIN rooms r         ON r.id = nr.room_id
		  LEFT JOIN buildings b ON b.id = r.building_id
		  LEFT JOIN locations l ON l.id = b.location_id
		  LEFT JOIN regions   rg ON rg.id = l.region_id
		 WHERE nr.customer_id = $1
		   AND nr.scheduled_at >= $2
		 ORDER BY nr.scheduled_at ASC
	`, customerID, windowStart)
	if err != nil {
		return nil, nil, err
	}
	var runs []runRow
	for rows.Next() {
		var r runRow
		var completed *time.Time
		if err := rows.Scan(
			&r.runID, &r.roomID, &r.roomName,
			&r.buildingName, &r.locationName, &r.regionName,
			&r.phase, &r.status, &r.scheduledAt, &completed, &r.failureReason,
		); err != nil {
			rows.Close()
			return nil, nil, err
		}
		r.completedAt = completed
		runs = append(runs, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	// Second query: rooms with a currently-active exclusion. Presented in
	// the digest as "excluded — will resume DD Mon". Only rooms that DIDN'T
	// get a run in the window (otherwise the run outcome wins).
	seenRoomIDs := make(map[string]struct{}, len(runs))
	for _, r := range runs {
		seenRoomIDs[r.roomID] = struct{}{}
	}
	exRows, err := s.pool.Query(ctx, `
		SELECT r.id::text,
		       COALESCE(r.name, ''),
		       COALESCE(b.name, ''),
		       COALESCE(l.name, ''),
		       COALESCE(rg.name, ''),
		       rnc.excluded_until
		  FROM room_nightly_config rnc
		  JOIN rooms r         ON r.id = rnc.room_id
		  LEFT JOIN buildings b ON b.id = r.building_id
		  LEFT JOIN locations l ON l.id = b.location_id
		  LEFT JOIN regions   rg ON rg.id = l.region_id
		 WHERE rnc.customer_id = $1
		   AND rnc.excluded_until IS NOT NULL
		   AND rnc.excluded_until >= (now() at time zone 'UTC')::date
	`, customerID)
	if err != nil {
		return runs, nil, err
	}
	var excluded []runRow
	for exRows.Next() {
		var r runRow
		var until *time.Time
		if err := exRows.Scan(
			&r.roomID, &r.roomName,
			&r.buildingName, &r.locationName, &r.regionName,
			&until,
		); err != nil {
			exRows.Close()
			return runs, nil, err
		}
		if _, dup := seenRoomIDs[r.roomID]; dup {
			continue
		}
		r.excludedUntil = until
		excluded = append(excluded, r)
	}
	exRows.Close()
	if err := exRows.Err(); err != nil {
		return runs, nil, err
	}
	return runs, excluded, nil
}

// collectRecipients returns the To + Cc lists for a customer.
//   To = every enabled email channel's target.
//   Cc = helpdesk_email if set.
// The caller collapses duplicates.
func (s *DigestSender) collectRecipients(ctx context.Context, c customerInfo) ([]string, []string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT target
		  FROM notification_channels
		 WHERE customer_id = $1
		   AND type        = 'email'
		   AND enabled     = true
		 ORDER BY name ASC
	`, c.customerID)
	if err != nil {
		return nil, nil, err
	}
	var to []string
	for rows.Next() {
		var target string
		if err := rows.Scan(&target); err != nil {
			rows.Close()
			return nil, nil, err
		}
		if trimmed := strings.TrimSpace(target); trimmed != "" {
			to = append(to, trimmed)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}
	var cc []string
	if trimmed := strings.TrimSpace(c.helpdeskEmail); trimmed != "" {
		cc = append(cc, trimmed)
	}
	return to, cc, nil
}

// sendFor runs the full pipeline for one customer: collect, render, send,
// stamp. `manual` bypasses the digest_last_sent_for guard and doesn't
// update the stamp (so the automatic morning digest still fires).
func (s *DigestSender) sendFor(ctx context.Context, c customerInfo, manual bool) error {
	nowUTC := s.wallNow().UTC()
	runs, excluded, err := s.collectRuns(ctx, c.customerID, nowUTC)
	if err != nil {
		return fmt.Errorf("collect runs: %w", err)
	}
	to, cc, err := s.collectRecipients(ctx, c)
	if err != nil {
		return fmt.Errorf("collect recipients: %w", err)
	}
	if len(to) == 0 && len(cc) == 0 {
		// No one to send to — log but don't error. Still stamp so we don't
		// re-check every minute for a customer who's simply not routing
		// email anywhere yet.
		s.log.Info("nightly digest: no recipients configured, skipping",
			"customer", c.customerID, "manual", manual)
		if !manual {
			s.stampSent(ctx, c)
		}
		return nil
	}

	summary := summariseRuns(runs)
	subject := renderSubject(c, summary, nowUTC)
	htmlBody := renderHTML(c, runs, excluded, summary, nowUTC)
	textBody := renderText(c, runs, excluded, summary, nowUTC)

	if err := notify.SendHTMLEmail(ctx, s.cfg.SMTP, to, cc, subject, htmlBody, textBody, s.log); err != nil {
		return fmt.Errorf("smtp send: %w", err)
	}
	s.log.Info("nightly digest sent",
		"customer", c.customerID,
		"to_count", len(to),
		"cc_count", len(cc),
		"ready", summary.ready, "failed", summary.failed,
		"in_progress", summary.inProgress, "excluded", len(excluded),
		"manual", manual,
	)

	if !manual {
		s.stampSent(ctx, c)
	}
	return nil
}

// stampSent writes today's local date into digest_last_sent_for.
func (s *DigestSender) stampSent(ctx context.Context, c customerInfo) {
	loc, err := time.LoadLocation(c.timezone)
	if err != nil {
		s.log.Warn("nightly digest: bad timezone at stamp time",
			"customer", c.customerID, "tz", c.timezone, "error", err)
		return
	}
	localToday := s.wallNow().In(loc).Format("2006-01-02")
	if _, err := s.pool.Exec(ctx, `
		UPDATE nightly_schedule
		   SET digest_last_sent_for = $2::date
		 WHERE customer_id = $1
	`, c.customerID, localToday); err != nil {
		s.log.Warn("nightly digest: stamp failed",
			"customer", c.customerID, "error", err)
	}
}

func (s *DigestSender) wallNow() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// ── Rendering ────────────────────────────────────────────────────────────

type digestSummary struct {
	total      int
	ready      int
	failed     int
	inProgress int
}

func summariseRuns(runs []runRow) digestSummary {
	var s digestSummary
	for _, r := range runs {
		s.total++
		switch r.status {
		case "succeeded":
			s.ready++
		case "failed":
			s.failed++
		default:
			s.inProgress++
		}
	}
	return s
}

func renderSubject(c customerInfo, sum digestSummary, nowUTC time.Time) string {
	loc, err := time.LoadLocation(c.timezone)
	if err != nil {
		loc = time.UTC
	}
	date := nowUTC.In(loc).Format("Mon 2 Jan 2006")

	name := c.customerName
	if name == "" {
		name = "Room Readiness"
	}

	// Front-load the failure count when it's non-zero — inbox scanners see
	// "3 failed" before the customer name and know they need to act.
	if sum.failed > 0 {
		return fmt.Sprintf("[Room Readiness] %d failed · %s · %s", sum.failed, name, date)
	}
	if sum.total == 0 {
		return fmt.Sprintf("[Room Readiness] %s · %s · nothing scheduled last night", name, date)
	}
	return fmt.Sprintf("[Room Readiness] %s · %s · %d/%d ready", name, date, sum.ready, sum.total)
}

func renderText(c customerInfo, runs, excluded []runRow, sum digestSummary, nowUTC time.Time) string {
	loc, err := time.LoadLocation(c.timezone)
	if err != nil {
		loc = time.UTC
	}
	date := nowUTC.In(loc).Format("Mon 2 Jan 2006")

	var b strings.Builder
	fmt.Fprintf(&b, "Room Readiness — %s — %s\n\n", c.customerName, date)
	fmt.Fprintf(&b, "%d ready · %d failed · %d in-progress · %d excluded\n\n",
		sum.ready, sum.failed, sum.inProgress, len(excluded))

	if sum.failed > 0 {
		b.WriteString("FAILED ROOMS\n")
		b.WriteString(strings.Repeat("-", 40))
		b.WriteString("\n")
		for _, r := range runs {
			if r.status != "failed" {
				continue
			}
			fmt.Fprintf(&b, "· %s (%s)\n", r.roomName, roomBreadcrumbText(r))
			fmt.Fprintf(&b, "  Phase reached: %s\n", r.phase)
			if r.failureReason != "" {
				fmt.Fprintf(&b, "  Reason: %s\n", r.failureReason)
			}
			fmt.Fprintf(&b, "\n")
		}
	}
	if sum.inProgress > 0 {
		b.WriteString("IN-PROGRESS (may complete after this digest)\n")
		b.WriteString(strings.Repeat("-", 40))
		b.WriteString("\n")
		for _, r := range runs {
			if r.status == "succeeded" || r.status == "failed" {
				continue
			}
			fmt.Fprintf(&b, "· %s — %s\n", r.roomName, r.phase)
		}
		fmt.Fprintf(&b, "\n")
	}
	if sum.ready > 0 {
		fmt.Fprintf(&b, "%d rooms ready.\n\n", sum.ready)
	}
	if len(excluded) > 0 {
		b.WriteString("EXCLUDED ROOMS\n")
		b.WriteString(strings.Repeat("-", 40))
		b.WriteString("\n")
		for _, r := range excluded {
			until := "—"
			if r.excludedUntil != nil {
				until = r.excludedUntil.In(loc).Format("2 Jan")
			}
			fmt.Fprintf(&b, "· %s — resumes %s\n", r.roomName, until)
		}
	}
	return b.String()
}

func renderHTML(c customerInfo, runs, excluded []runRow, sum digestSummary, nowUTC time.Time) string {
	loc, err := time.LoadLocation(c.timezone)
	if err != nil {
		loc = time.UTC
	}
	date := nowUTC.In(loc).Format("Mon 2 Jan 2006")

	var b strings.Builder
	// Inline styles only — Gmail / Outlook / Apple Mail all strip <style>
	// blocks in most contexts. Fonts fall back to a system stack that
	// renders as intended on every desktop and mobile client.
	b.WriteString(`<!doctype html><html><body style="margin:0;padding:0;background:#f5f6f8;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,'Helvetica Neue',Arial,sans-serif;color:#0f172a;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0" style="background:#f5f6f8;padding:24px 12px;"><tr><td align="center">`)
	b.WriteString(`<table role="presentation" width="600" cellpadding="0" cellspacing="0" style="max-width:600px;background:#ffffff;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(15,23,42,0.08);">`)

	// Header band — colour reflects worst outcome so an inbox preview conveys severity.
	headerColour := "#0f172a"
	statusLine := fmt.Sprintf("%d rooms ready", sum.ready)
	if sum.failed > 0 {
		headerColour = "#b91c1c"
		statusLine = fmt.Sprintf("%d failed · %d ready", sum.failed, sum.ready)
	} else if sum.total == 0 {
		headerColour = "#334155"
		statusLine = "Nothing scheduled last night"
	}
	fmt.Fprintf(&b, `<tr><td style="background:%s;padding:24px 32px;color:#ffffff;">`, headerColour)
	fmt.Fprintf(&b, `<div style="font-size:12px;letter-spacing:0.12em;text-transform:uppercase;opacity:0.85;">Room Readiness</div>`)
	fmt.Fprintf(&b, `<div style="font-size:22px;font-weight:600;margin-top:6px;">%s</div>`, html.EscapeString(c.customerName))
	fmt.Fprintf(&b, `<div style="font-size:14px;margin-top:4px;opacity:0.9;">%s</div>`, html.EscapeString(date))
	fmt.Fprintf(&b, `<div style="font-size:16px;font-weight:500;margin-top:14px;">%s</div>`, html.EscapeString(statusLine))
	b.WriteString(`</td></tr>`)

	// Summary strip.
	b.WriteString(`<tr><td style="padding:16px 32px;border-bottom:1px solid #e2e8f0;">`)
	b.WriteString(`<table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr>`)
	writeStat(&b, "Ready", sum.ready, "#0f766e")
	writeStat(&b, "Failed", sum.failed, "#b91c1c")
	writeStat(&b, "In-progress", sum.inProgress, "#a16207")
	writeStat(&b, "Excluded", len(excluded), "#334155")
	b.WriteString(`</tr></table></td></tr>`)

	// Failed rooms — the reason the customer opens the email.
	if sum.failed > 0 {
		b.WriteString(`<tr><td style="padding:24px 32px 8px;">`)
		b.WriteString(`<div style="font-size:14px;font-weight:600;color:#b91c1c;text-transform:uppercase;letter-spacing:0.08em;">Failed rooms</div>`)
		b.WriteString(`</td></tr>`)
		for _, r := range runs {
			if r.status != "failed" {
				continue
			}
			writeFailedRoom(&b, r)
		}
	}

	// In-progress — likely to complete after the digest fires; called out so
	// the customer knows the digest isn't the final word for the day.
	if sum.inProgress > 0 {
		b.WriteString(`<tr><td style="padding:16px 32px 8px;">`)
		b.WriteString(`<div style="font-size:14px;font-weight:600;color:#a16207;text-transform:uppercase;letter-spacing:0.08em;">In progress</div>`)
		b.WriteString(`<div style="font-size:13px;color:#475569;margin-top:4px;">These rooms hadn't finished when the digest was generated.</div>`)
		b.WriteString(`</td></tr>`)
		for _, r := range runs {
			if r.status == "succeeded" || r.status == "failed" {
				continue
			}
			writeInProgressRoom(&b, r)
		}
	}

	// Ready count — collapsed by design (the spec calls this out). The
	// portal is the right place to browse the full list.
	if sum.ready > 0 {
		b.WriteString(`<tr><td style="padding:16px 32px;">`)
		b.WriteString(`<div style="font-size:14px;font-weight:600;color:#0f766e;text-transform:uppercase;letter-spacing:0.08em;">Ready</div>`)
		fmt.Fprintf(&b, `<div style="font-size:15px;color:#0f172a;margin-top:6px;">%d rooms passed their overnight checks.</div>`, sum.ready)
		b.WriteString(`</td></tr>`)
	}

	if len(excluded) > 0 {
		b.WriteString(`<tr><td style="padding:16px 32px 24px;">`)
		b.WriteString(`<div style="font-size:14px;font-weight:600;color:#334155;text-transform:uppercase;letter-spacing:0.08em;">Excluded</div>`)
		b.WriteString(`<ul style="margin:6px 0 0;padding-left:20px;color:#334155;font-size:14px;">`)
		for _, r := range excluded {
			until := "—"
			if r.excludedUntil != nil {
				until = r.excludedUntil.In(loc).Format("2 Jan")
			}
			fmt.Fprintf(&b, `<li>%s — resumes %s</li>`,
				html.EscapeString(r.roomName), html.EscapeString(until))
		}
		b.WriteString(`</ul></td></tr>`)
	}

	// Footer.
	b.WriteString(`<tr><td style="padding:16px 32px 24px;background:#f8fafc;border-top:1px solid #e2e8f0;color:#64748b;font-size:12px;line-height:1.5;">`)
	b.WriteString(`Room Readiness by av-bridge · <a href="#" style="color:#64748b;text-decoration:underline;">Manage schedule</a> · <a href="#" style="color:#64748b;text-decoration:underline;">View all runs</a>`)
	b.WriteString(`</td></tr>`)

	b.WriteString(`</table></td></tr></table></body></html>`)
	return b.String()
}

func writeStat(b *strings.Builder, label string, count int, colour string) {
	fmt.Fprintf(b, `<td style="text-align:center;padding:8px 4px;">`)
	fmt.Fprintf(b, `<div style="font-size:24px;font-weight:600;color:%s;">%d</div>`, colour, count)
	fmt.Fprintf(b, `<div style="font-size:12px;color:#64748b;text-transform:uppercase;letter-spacing:0.08em;margin-top:2px;">%s</div>`, label)
	fmt.Fprintf(b, `</td>`)
}

func writeFailedRoom(b *strings.Builder, r runRow) {
	fmt.Fprintf(b, `<tr><td style="padding:8px 32px 16px;">`)
	fmt.Fprintf(b, `<div style="border-left:3px solid #b91c1c;padding:10px 14px;background:#fef2f2;border-radius:0 8px 8px 0;">`)
	fmt.Fprintf(b, `<div style="font-size:15px;font-weight:600;color:#0f172a;">%s</div>`, html.EscapeString(r.roomName))
	if bc := roomBreadcrumbText(r); bc != "" {
		fmt.Fprintf(b, `<div style="font-size:12px;color:#64748b;margin-top:2px;">%s</div>`, html.EscapeString(bc))
	}
	fmt.Fprintf(b, `<div style="font-size:13px;color:#334155;margin-top:6px;">Phase reached: <strong>%s</strong></div>`, html.EscapeString(r.phase))
	if r.failureReason != "" {
		fmt.Fprintf(b, `<div style="font-size:13px;color:#7f1d1d;margin-top:4px;">%s</div>`, html.EscapeString(r.failureReason))
	}
	b.WriteString(`</div></td></tr>`)
}

func writeInProgressRoom(b *strings.Builder, r runRow) {
	fmt.Fprintf(b, `<tr><td style="padding:4px 32px 8px;">`)
	fmt.Fprintf(b, `<div style="font-size:14px;color:#0f172a;">%s <span style="color:#a16207;">— %s</span></div>`, html.EscapeString(r.roomName), html.EscapeString(r.phase))
	b.WriteString(`</td></tr>`)
}

func roomBreadcrumbText(r runRow) string {
	parts := make([]string, 0, 3)
	if r.regionName != "" {
		parts = append(parts, r.regionName)
	}
	if r.locationName != "" {
		parts = append(parts, r.locationName)
	}
	if r.buildingName != "" {
		parts = append(parts, r.buildingName)
	}
	return strings.Join(parts, " · ")
}
