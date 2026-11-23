package integrations

// Tagging of Lens devices (marcus:<device id>) runs here, not in the add-device
// request: Lens rejects assigning a freshly created tag (403
// AUTHORIZATION_ERROR) for roughly 5-10 seconds, so the poller creates the tag
// and retries the assignment on later cycles. The tag is created once; its id
// is stored in integration_devices.lens_tag_id and reused, and it is recreated
// only if Lens says that id no longer exists.

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/integrations/polylens"
)

const (
	tagPrefix = "marcus:"
	// maxTagOps bounds the tag operations per integration per cycle.
	maxTagOps = 20
	// tagGiveUp is how long a link may stay pending before it is marked failed.
	// Lens can refuse a new tag for ~10 minutes (seen in UAT), so allow 30.
	tagGiveUp = 30 * time.Minute

	msgTagRole   = "Lens role can't tag devices"
	msgTagFailed = "Poly Lens did not accept the tag"
)

type pendingTag struct {
	deviceID, externalID string
	requested            time.Time
}

// errSkipTag means the link is no longer waiting to be tagged (deleted, or
// tagging switched off) so nothing is recorded.
var errSkipTag = errors.New("lens tag request no longer pending")

// tagPending processes this integration's links waiting to be tagged. It never
// affects device status or the integration's own status.
func (p *Poller) tagPending(ctx context.Context, in integrationRow, client *polylens.Client) {
	if !in.allowTagging {
		p.dropPending(ctx, in, client)
		return
	}
	rows, err := p.admin.Query(ctx, `
		SELECT l.device_id::text, l.external_id,
		       -- the give-up window starts at the first attempt, so a poller
		       -- outage can't make a link fail before it was ever tried
		       CASE WHEN l.tag_attempted_at IS NULL THEN now() ELSE COALESCE(l.tag_requested_at, now()) END
		  FROM integration_devices l JOIN devices d ON d.id = l.device_id
		 WHERE l.integration_id = $1 AND l.customer_id = $2 AND l.tag_state = 'pending'
		   AND d.customer_id = l.customer_id AND d.deleted_at IS NULL
		 ORDER BY l.tag_requested_at NULLS FIRST LIMIT $3`, in.id, in.customerID, maxTagOps)
	if err != nil {
		p.log.Warn("load pending lens tags failed", "integration", in.id, "error", err)
		return
	}
	var todo []pendingTag
	for rows.Next() {
		var t pendingTag
		if err := rows.Scan(&t.deviceID, &t.externalID, &t.requested); err != nil {
			rows.Close()
			p.log.Warn("load pending lens tags failed", "integration", in.id, "error", err)
			return
		}
		todo = append(todo, t)
	}
	rows.Close()
	if len(todo) == 0 {
		return
	}
	tctx, cancel := context.WithTimeout(ctx, p.lensTimeout())
	defer cancel()
	for _, t := range todo {
		if tctx.Err() != nil {
			return
		}
		err := p.tagOne(tctx, in, client, t)
		var rl *polylens.RateLimitError
		switch {
		case errors.As(err, &rl):
			p.log.Info("poly lens rate limited; skipping tagging", "integration", in.id, "retry_after", rl.RetryAfter)
			return
		case errors.Is(err, errSkipTag):
			continue
		case err != nil && tctx.Err() != nil:
			return // our own deadline, not Lens refusing: record nothing
		}
		p.recordTag(ctx, tctx, in, client, t, err)
	}
}

// dropPending clears requests left pending after tagging was switched off and
// best-effort deletes any tag already created for them.
func (p *Poller) dropPending(ctx context.Context, in integrationRow, client *polylens.Client) {
	rows, err := p.admin.Query(ctx, `
		SELECT device_id::text, COALESCE(lens_tag_id, '') FROM integration_devices
		 WHERE integration_id = $1 AND customer_id = $2 AND tag_state = 'pending' LIMIT $3`, in.id, in.customerID, maxTagOps)
	if err != nil {
		p.log.Warn("load pending lens tags failed", "integration", in.id, "error", err)
		return
	}
	type row struct{ dev, tag string }
	var list []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.dev, &r.tag); err != nil {
			rows.Close()
			return
		}
		list = append(list, r)
	}
	rows.Close()
	if len(list) == 0 {
		return
	}
	dctx, cancel := context.WithTimeout(ctx, p.lensTimeout())
	defer cancel()
	for _, r := range list {
		if r.tag != "" && dctx.Err() == nil {
			if err := client.DeleteTagOnce(dctx, r.tag); err != nil {
				p.log.Info("poly lens tag cleanup failed", "device_id", r.dev, "error", err)
			}
		}
		if _, err := p.admin.Exec(ctx, `
			UPDATE integration_devices SET tag_state = 'none', tag_error = NULL, lens_tag_id = NULL
			 WHERE device_id = $1 AND customer_id = $2 AND tag_state = 'pending'`, r.dev, in.customerID); err != nil {
			p.log.Warn("clear pending lens tag failed", "device_id", r.dev, "error", err)
		}
	}
}

// pendingTagID reports whether the link still waits to be tagged (the device
// is live, the state is pending and tagging is still allowed) and the stored
// Lens tag id.
func (p *Poller) pendingTagID(ctx context.Context, in integrationRow, deviceID string) (bool, string, error) {
	var id string
	err := p.admin.QueryRow(ctx, `
		SELECT COALESCE(l.lens_tag_id, '')
		  FROM integration_devices l
		  JOIN devices d ON d.id = l.device_id AND d.customer_id = l.customer_id
		  JOIN integrations i ON i.id = l.integration_id
		 WHERE l.device_id = $1 AND l.customer_id = $2 AND l.integration_id = $3
		   AND l.tag_state = 'pending' AND d.deleted_at IS NULL
		   AND i.allow_tagging AND i.status <> 'disconnected'`, deviceID, in.customerID, in.id).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, "", nil
	}
	return err == nil, id, err
}

// ensureTag creates the per-device tag and stores its id, replacing old (""
// when none is stored). The store is conditional so a second writer cannot
// leave two tags.
func (p *Poller) ensureTag(ctx context.Context, in integrationRow, client *polylens.Client, t pendingTag, old string) (string, error) {
	id, err := client.CreateTag(ctx, in.accountID, tagPrefix+t.deviceID)
	if err != nil {
		return "", err
	}
	tag, err := p.admin.Exec(ctx, `
		UPDATE integration_devices SET lens_tag_id = $4
		 WHERE device_id = $1 AND customer_id = $2 AND tag_state = 'pending'
		   AND COALESCE(lens_tag_id, '') = $3`, t.deviceID, in.customerID, old, id)
	if err == nil && tag.RowsAffected() == 1 {
		return id, nil
	}
	// Lost the race or the link went away: do not keep the extra tag.
	_ = client.DeleteTagOnce(ctx, id)
	if err != nil {
		return "", err
	}
	return "", errSkipTag
}

// tagOne makes one attempt: use the stored tag (creating it once), then assign it.
func (p *Poller) tagOne(ctx context.Context, in integrationRow, client *polylens.Client, t pendingTag) error {
	ok, id, err := p.pendingTagID(ctx, in, t.deviceID)
	if err != nil {
		return err
	}
	if !ok {
		return errSkipTag
	}
	if id == "" {
		if id, err = p.ensureTag(ctx, in, client, t, ""); err != nil {
			return err
		}
	}
	// A delete or a tagging switch-off may have landed meanwhile.
	if ok, _, err = p.pendingTagID(ctx, in, t.deviceID); err != nil {
		return err
	} else if !ok {
		return errSkipTag
	}
	err = client.AssignTag(ctx, id, t.externalID)
	if errors.Is(err, polylens.ErrTagNotFound) {
		// The stored tag no longer exists in Lens: replace it, once.
		if id, err = p.ensureTag(ctx, in, client, t, id); err != nil {
			return err
		}
		err = client.AssignTag(ctx, id, t.externalID)
	}
	return err
}

// recordTag stores the outcome of one attempt. A 403 or any other error keeps
// the link pending until the give-up window has passed. At give-up the stored
// tag gets one delete attempt under the cycle deadline; a leftover is logged
// and its id stays on the row.
func (p *Poller) recordTag(ctx, tctx context.Context, in integrationRow, client *polylens.Client, t pendingTag, err error) {
	state, msg := "tagged", ""
	clearTag := false
	switch {
	case err == nil:
	case time.Since(t.requested) > tagGiveUp:
		state, msg = "failed", msgTagFailed
		if errors.Is(err, polylens.ErrForbidden) {
			msg = msgTagRole
		}
		var id string
		_ = p.admin.QueryRow(ctx, `SELECT COALESCE(lens_tag_id, '') FROM integration_devices WHERE device_id = $1 AND customer_id = $2`,
			t.deviceID, in.customerID).Scan(&id)
		clearTag = true
		if id != "" {
			if derr := client.DeleteTagOnce(tctx, id); derr != nil {
				clearTag = false
				p.log.Info("poly lens tag cleanup after give-up failed; tag left behind", "device_id", t.deviceID, "error", derr)
			}
		}
	default:
		state = "pending"
		if !errors.Is(err, polylens.ErrForbidden) {
			msg = msgTagFailed
		}
		p.log.Info("poly lens tagging will be retried", "device_id", t.deviceID, "error", err)
	}
	if _, uerr := p.admin.Exec(ctx, `
		UPDATE integration_devices SET tag_state = $3, tag_error = NULLIF($4, ''),
		       tag_requested_at = CASE WHEN tag_attempted_at IS NULL THEN now() ELSE tag_requested_at END,
		       tag_attempted_at = now(),
		       lens_tag_id = CASE WHEN $5 THEN NULL ELSE lens_tag_id END
		 WHERE device_id = $1 AND customer_id = $2 AND tag_state = 'pending'`,
		t.deviceID, in.customerID, state, msg, clearTag); uerr != nil {
		p.log.Warn("record lens tag state failed", "device_id", t.deviceID, "error", uerr)
	}
}
