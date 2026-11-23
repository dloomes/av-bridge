// Package integrations runs the background work for vendor-cloud
// integrations. The poller reads Poly Lens for every linked Teams Room and
// writes status and readings the same way Collector ingest does, so the
// portal, alerts and reports treat Lens devices like any other.
package integrations

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/integrations/polylens"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/dloomes/av-bridge-cloud/internal/wsfanout"
)

// Readings is what the alert evaluator sees after each poll of a device.
// Status is online, offline or unknown. SignedIn and PeripheralsLinked are
// nil when Lens did not say.
type Readings struct {
	Status            string
	SignedIn          *bool
	PeripheralsLinked *bool
	TeamsHealth       string
}

// AlertEvaluator opens and clears alerts from a device's latest readings.
type AlertEvaluator interface {
	Evaluate(ctx context.Context, customerID, deviceID string, r Readings) error
}

const (
	msgUnreachable  = "Poly Lens unreachable"
	msgNotFound     = "Not found in this Lens account"
	msgDisconnected = "Integration disconnected"
	msgRejected     = "Poly Lens rejected the credentials"
	msgNoSecret     = "Stored Poly Lens credentials could not be read"
	msgForbidden    = "Poly Lens role lacks permission"
)

// Poller polls every Poly Lens integration on an interval. It runs as the
// admin (cross-tenant) pool and scopes each device write to its customer.
type Poller struct {
	admin    *pgxpool.Pool
	cipher   secrets.Cipher
	live     wsfanout.Publisher
	alerts   AlertEvaluator
	interval time.Duration
	log      *slog.Logger

	tokenURL, graphqlURL string
	// onlyCustomers limits a pass to these customers (tests sharing a database).
	onlyCustomers []string
	// timeout bounds one integration's calls to Lens; zero means the default.
	timeout time.Duration

	mu      sync.Mutex
	clients map[string]cachedClient
}

type cachedClient struct {
	key    string
	client *polylens.Client
}

// maxConcurrent is how many integrations are polled at once, so one slow
// Lens cannot hold up the other customers.
const maxConcurrent = 4

// maxLensWait is the longest one integration's Lens calls may take.
const maxLensWait = 20 * time.Second

func NewPoller(admin *pgxpool.Pool, cipher secrets.Cipher, live wsfanout.Publisher, alerts AlertEvaluator, interval time.Duration, log *slog.Logger) *Poller {
	return &Poller{admin: admin, cipher: cipher, live: live, alerts: alerts, interval: interval, log: log,
		clients: map[string]cachedClient{}}
}

// SetLensBaseURLs points the Lens client at other endpoints (tests).
func (p *Poller) SetLensBaseURLs(tokenURL, graphqlURL string) *Poller {
	p.tokenURL, p.graphqlURL = tokenURL, graphqlURL
	return p
}

// Run polls once straight away, then every interval, until ctx ends.
func (p *Poller) Run(ctx context.Context) {
	if p.interval <= 0 {
		p.log.Warn("integration poller disabled", "interval", p.interval)
		return
	}
	p.log.Info("integration poller started", "interval", p.interval)
	t := time.NewTicker(p.interval)
	defer t.Stop()
	for {
		if err := p.PollOnce(ctx); err != nil && ctx.Err() == nil {
			p.log.Warn("integration poll failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

type integrationRow struct {
	id, customerID, status, clientID, accountID string
	secretEnc                                   []byte
	allowTagging                                bool
}

type linkedDevice struct {
	deviceID, name, typ, externalID string
}

// onlyCustomersSQL limits a pass to $1 when it lists customers. pgx sends a
// nil slice (production) as NULL, and cardinality(NULL) is NULL, so the
// COALESCE is what makes an unset filter mean "every customer".
const onlyCustomersSQL = `(COALESCE(cardinality($1::text[]), 0) = 0 OR customer_id::text = ANY($1))`

// PollOnce does one pass over every Poly Lens integration. A failure on one
// integration never stops the others.
func (p *Poller) PollOnce(ctx context.Context) error {
	rows, err := p.admin.Query(ctx, `
		SELECT id::text, customer_id::text, status, COALESCE(client_id, ''), COALESCE(account_id, ''), secret_enc, allow_tagging
		  FROM integrations WHERE provider = 'poly_lens'
		   AND `+db.CustomerActiveSQL("customer_id")+`
		   AND `+onlyCustomersSQL, p.onlyCustomers)
	if err != nil {
		return err
	}
	var ints []integrationRow
	for rows.Next() {
		var r integrationRow
		if err := rows.Scan(&r.id, &r.customerID, &r.status, &r.clientID, &r.accountID, &r.secretEnc, &r.allowTagging); err != nil {
			rows.Close()
			return err
		}
		ints = append(ints, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	p.pruneClients(ints)
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup
	for _, in := range ints {
		if ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(in integrationRow) {
			defer wg.Done()
			defer func() { <-sem }()
			if err := p.pollIntegration(ctx, in); err != nil && ctx.Err() == nil {
				p.log.Warn("integration poll failed", "integration", in.id, "error", err)
			}
		}(in)
	}
	wg.Wait()
	return ctx.Err()
}

func (p *Poller) lensTimeout() time.Duration {
	if p.timeout > 0 {
		return p.timeout
	}
	t := maxLensWait
	if p.interval > 0 && p.interval/2 < t {
		t = p.interval / 2
	}
	return t
}

// clientFor returns the Lens client of an integration, keeping it (and its
// OAuth token) between polls until the credentials or account change.
func (p *Poller) clientFor(in integrationRow, secret string) *polylens.Client {
	key := in.clientID + "|" + in.accountID + "|" + secret
	p.mu.Lock()
	defer p.mu.Unlock()
	if c, ok := p.clients[in.id]; ok && c.key == key {
		return c.client
	}
	var opts []polylens.Option
	if p.tokenURL != "" || p.graphqlURL != "" {
		opts = append(opts, polylens.WithBaseURLs(p.tokenURL, p.graphqlURL))
	}
	c := polylens.New(in.clientID, secret, opts...)
	p.clients[in.id] = cachedClient{key: key, client: c}
	return c
}

func (p *Poller) pruneClients(ints []integrationRow) {
	live := make(map[string]bool, len(ints))
	for _, in := range ints {
		live[in.id] = true
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for id := range p.clients {
		if !live[id] {
			delete(p.clients, id)
		}
	}
}

func (p *Poller) linked(ctx context.Context, in integrationRow) ([]linkedDevice, error) {
	rows, err := p.admin.Query(ctx, `
		SELECT d.id::text, COALESCE(d.name, ''), COALESCE(d.type, ''), l.external_id
		  FROM integration_devices l JOIN devices d ON d.id = l.device_id
		 WHERE l.integration_id = $1 AND l.customer_id = $2 AND d.customer_id = l.customer_id AND d.deleted_at IS NULL`, in.id, in.customerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []linkedDevice
	for rows.Next() {
		var d linkedDevice
		if err := rows.Scan(&d.deviceID, &d.name, &d.typ, &d.externalID); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (p *Poller) pollIntegration(ctx context.Context, in integrationRow) error {
	devs, err := p.linked(ctx, in)
	if err != nil {
		return err
	}
	if in.status == "disconnected" || len(in.secretEnc) == 0 || in.accountID == "" {
		for _, d := range devs {
			p.markUnknown(ctx, in, d, msgDisconnected, false)
		}
		return nil
	}
	if len(devs) == 0 {
		// Nothing was asked of Lens, so nothing is proved: leave the status.
		return nil
	}
	secret, err := p.cipher.Decrypt(in.secretEnc)
	if err != nil {
		p.failIntegration(ctx, in, devs, msgNoSecret)
		return nil
	}
	client := p.clientFor(in, string(secret))
	ids := make([]string, len(devs))
	for i, d := range devs {
		ids[i] = d.externalID
	}
	// Only the Lens call has a deadline; the writes below use the parent
	// context so a hung Lens can still be recorded as unknown.
	lensCtx, cancel := context.WithTimeout(ctx, p.lensTimeout())
	found, err := client.Devices(lensCtx, in.accountID, ids)
	cancel()
	if err != nil {
		var rl *polylens.RateLimitError
		switch {
		case errors.As(err, &rl):
			// Lens asked us to wait: skip this cycle, change nothing.
			p.log.Info("poly lens rate limited; skipping cycle", "integration", in.id, "retry_after", rl.RetryAfter)
		case errors.Is(err, polylens.ErrUnauthorized):
			p.failIntegration(ctx, in, devs, msgRejected)
		case errors.Is(err, polylens.ErrForbidden):
			p.failIntegration(ctx, in, devs, msgForbidden)
		case ctx.Err() != nil:
			return ctx.Err()
		default:
			p.failIntegration(ctx, in, devs, msgUnreachable)
		}
		return nil
	}
	byID := make(map[string]polylens.Device, len(found))
	for _, f := range found {
		byID[f.ID] = f
	}
	for _, d := range devs {
		if f, ok := byID[d.externalID]; ok {
			p.writeDevice(ctx, in, d, f)
		} else {
			p.markUnknown(ctx, in, d, msgNotFound, true)
		}
	}
	p.tagPending(ctx, in, client)
	return p.setIntegration(ctx, in, "")
}

// setIntegration records the outcome of a call to Lens. An empty msg means
// success. A disconnected integration is never touched.
func (p *Poller) setIntegration(ctx context.Context, in integrationRow, msg string) error {
	if msg == "" {
		_, err := p.admin.Exec(ctx, `
			UPDATE integrations SET status = 'connected', last_success_at = now(), last_error = NULL
			 WHERE id = $1 AND customer_id = $2 AND status <> 'disconnected'`, in.id, in.customerID)
		return err
	}
	_, err := p.admin.Exec(ctx, `
		UPDATE integrations SET status = 'error', last_error = $3, updated_at = now()
		 WHERE id = $1 AND customer_id = $2 AND status <> 'disconnected'`, in.id, in.customerID, msg)
	return err
}

// failIntegration marks the integration errored and its devices unknown. The
// message is a fixed string, never the underlying error, which could carry
// request detail.
func (p *Poller) failIntegration(ctx context.Context, in integrationRow, devs []linkedDevice, msg string) {
	if err := p.setIntegration(ctx, in, msg); err != nil {
		p.log.Warn("mark integration error failed", "integration", in.id, "error", err)
	}
	reason := msgUnreachable
	if msg == msgForbidden {
		reason = msg // a role problem is not an outage
	}
	for _, d := range devs {
		p.markUnknown(ctx, in, d, reason, true)
	}
}

// tenantTx runs fn in an admin-pool transaction with the tenant variable set,
// so the policies would apply even to a role that does not bypass RLS.
func (p *Poller) tenantTx(ctx context.Context, customerID string, fn func(pgx.Tx) error) error {
	tx, err := p.admin.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, "SELECT set_config('app.current_customer', $1, true)", customerID); err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// markUnknown shows a device as unknown with the reason. last_seen_at moves
// to now so the 15-minute stale rule does not turn it offline; a telemetry
// row is kept for every cycle except for a disconnected integration.
func (p *Poller) markUnknown(ctx context.Context, in integrationRow, d linkedDevice, reason string, telemetry bool) {
	metrics := map[string]any{"error": reason}
	b, _ := json.Marshal(metrics)
	wrote := false
	err := p.tenantTx(ctx, in.customerID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE devices SET latest_status = 'unknown', latest_metrics = $3::jsonb, last_seen_at = now()
			 WHERE id = $1 AND customer_id = $2 AND deleted_at IS NULL`, d.deviceID, in.customerID, string(b))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		wrote = true
		if !telemetry {
			return nil
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO telemetry (customer_id, device_id, status, metrics, error, ts)
			VALUES ($1, $2, 'unknown', $3::jsonb, $4, now())`, in.customerID, d.deviceID, string(b), reason)
		return err
	})
	if err != nil {
		p.log.Warn("mark device unknown failed", "device", d.deviceID, "error", err)
		return
	}
	if !wrote {
		return
	}
	p.publish(in, d, "unknown", metrics)
	p.evaluate(ctx, in, d, Readings{Status: "unknown"})
}

func (p *Poller) writeDevice(ctx context.Context, in integrationRow, d linkedDevice, f polylens.Device) {
	status := "offline"
	if f.Connected {
		status = "online"
	}
	metrics, signedIn := readingsFor(f)
	b, _ := json.Marshal(metrics)
	wrote := false
	err := p.tenantTx(ctx, in.customerID, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE devices SET
			    make             = COALESCE(NULLIF(make, ''), 'Poly'),
			    latest_status    = $3,
			    latest_metrics   = $4::jsonb,
			    last_seen_at     = now(),
			    last_online_at   = CASE WHEN $3 = 'online' THEN now() ELSE last_online_at END,
			    model            = COALESCE(NULLIF($5, ''), model),
			    serial_number    = COALESCE(NULLIF($6, ''), serial_number),
			    firmware_version = COALESCE(NULLIF($7, ''), firmware_version),
			    mac_address      = COALESCE(NULLIF($8, ''), mac_address)
			 WHERE id = $1 AND customer_id = $2 AND deleted_at IS NULL`,
			d.deviceID, in.customerID, status, string(b), f.Model, f.SerialNumber, f.SoftwareVersion, f.MACAddress)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		wrote = true
		_, err = tx.Exec(ctx, `
			INSERT INTO telemetry (customer_id, device_id, status, metrics, ts)
			VALUES ($1, $2, $3, $4::jsonb, now())`, in.customerID, d.deviceID, status, string(b))
		return err
	})
	if err != nil {
		p.log.Warn("write lens device failed", "device", d.deviceID, "error", err)
		return
	}
	if !wrote {
		return
	}
	p.publish(in, d, status, metrics)
	p.evaluate(ctx, in, d, Readings{Status: status, SignedIn: signedIn, PeripheralsLinked: f.PeripheralsLinked, TeamsHealth: f.TeamsHealth})
}

func (p *Poller) publish(in integrationRow, d linkedDevice, status string, metrics map[string]any) {
	if p.live == nil {
		return
	}
	payload := map[string]any{"status": status}
	for k, v := range metrics {
		payload[k] = v
	}
	p.live.Publish(in.customerID, wsfanout.Event{
		DeviceID:   d.deviceID,
		DeviceName: d.name,
		DeviceType: d.typ,
		EventType:  "status",
		Payload:    payload,
		Timestamp:  time.Now().UTC(),
	})
}

func (p *Poller) evaluate(ctx context.Context, in integrationRow, d linkedDevice, r Readings) {
	if p.alerts == nil {
		return
	}
	if err := p.alerts.Evaluate(ctx, in.customerID, d.deviceID, r); err != nil {
		p.log.Warn("integration alert evaluation failed", "device", d.deviceID, "error", err)
	}
}

// readingsFor maps a Lens device to the readings stored in latest_metrics.
// Fields Lens did not report are omitted (unknown, never bad).
func readingsFor(f polylens.Device) (map[string]any, *bool) {
	cs := callState(f.CallStatus)
	m := map[string]any{"call_state": cs, "active_calls": 0}
	if cs == "in_call" {
		m["active_calls"] = 1
	}
	var signedIn *bool
	if pa := strings.TrimSpace(f.PresenceAvailability); pa != "" {
		v := !strings.EqualFold(pa, "Offline") && !strings.EqualFold(pa, "PresenceUnknown")
		signedIn = &v
		m["teams_signed_in"] = v
		m["teams_presence"] = f.PresenceAvailability
	}
	set := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	set("teams_health", f.TeamsHealth)
	set("teams_app_version", f.AppVersion)
	set("software_version", f.SoftwareVersion)
	if f.PeripheralsLinked != nil {
		m["peripherals_linked"] = *f.PeripheralsLinked
	}
	if !f.LastDetected.IsZero() {
		m["lens_last_seen"] = f.LastDetected.UTC().Format(time.RFC3339)
	}
	return m, signedIn
}

// callState maps Lens's callStatus text to in_call, idle or unknown.
// Observed on a real tenant (Task 11): IN_CALL, NOT_IN_CALL, UNKNOWN and
// UNSUPPORTED (devices that cannot report); the last two map to unknown.
func callState(s string) string {
	n := strings.NewReplacer(" ", "", "_", "", "-", "").Replace(strings.ToLower(s))
	switch n {
	case "incall", "inameeting", "busy":
		return "in_call"
	case "idle", "notincall", "available", "none":
		return "idle"
	}
	return "unknown"
}
