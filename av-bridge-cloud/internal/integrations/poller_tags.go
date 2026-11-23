package integrations

// Tagging of Lens devices (marcus:<device id>) runs here, not in the add-device
// request: Lens rejects assigning a freshly created tag (403
// AUTHORIZATION_ERROR) for roughly 5-10 seconds, so the poller creates the tag
// and retries the assignment on later cycles.

import (
	"context"
	"errors"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/integrations/polylens"
)

const (
	tagPrefix = "marcus:"
	// maxTagOps bounds the tag operations per integration per cycle.
	maxTagOps = 20
	// tagGiveUp is how long a link may stay pending before it is marked failed.
	tagGiveUp = 15 * time.Minute
	// ghostAge is how old (by our own request time) a listed tag must be before
	// a failed assignment is blamed on a stale listing and the tag recreated.
	ghostAge = 30 * time.Second

	msgTagRole   = "Lens role can't tag devices"
	msgTagFailed = "Poly Lens did not accept the tag"
)

type pendingTag struct {
	deviceID, externalID string
	requested            time.Time
}

// tagPending processes this integration's links waiting to be tagged. It never
// affects device status or the integration's own status.
func (p *Poller) tagPending(ctx context.Context, in integrationRow, client *polylens.Client) {
	if !in.allowTagging {
		// Tagging was switched off before the request was served.
		if _, err := p.admin.Exec(ctx, `
			UPDATE integration_devices SET tag_state = 'none', tag_error = NULL
			 WHERE integration_id = $1 AND customer_id = $2 AND tag_state = 'pending'`, in.id, in.customerID); err != nil {
			p.log.Warn("clear pending lens tags failed", "integration", in.id, "error", err)
		}
		return
	}
	rows, err := p.admin.Query(ctx, `
		SELECT l.device_id::text, l.external_id, COALESCE(l.tag_requested_at, now())
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
		if errors.As(err, &rl) {
			p.log.Info("poly lens rate limited; skipping tagging", "integration", in.id, "retry_after", rl.RetryAfter)
			return
		}
		p.recordTag(ctx, in, client, t, err)
	}
}

// tagOne makes one attempt: ensure the tag exists, then assign it.
func (p *Poller) tagOne(ctx context.Context, in integrationRow, client *polylens.Client, t pendingTag) error {
	name := tagPrefix + t.deviceID
	id, err := client.FindTag(ctx, in.accountID, name)
	if err != nil {
		return err
	}
	found := id != ""
	if !found {
		if id, err = client.CreateTag(ctx, in.accountID, name); err != nil {
			return err
		}
	}
	err = client.AssignTag(ctx, id, t.externalID)
	// The tag listing can keep a deleted tag for minutes. When a listed tag
	// that is old by our own clock cannot be assigned, make a new one.
	if err != nil && found && time.Since(t.requested) > ghostAge &&
		(errors.Is(err, polylens.ErrTagNotFound) || errors.Is(err, polylens.ErrForbidden)) {
		if id, err = client.CreateTag(ctx, in.accountID, name); err != nil {
			return err
		}
		err = client.AssignTag(ctx, id, t.externalID)
	}
	return err
}

// recordTag stores the outcome of one attempt. A 403 or any other error keeps
// the link pending until the give-up window has passed.
func (p *Poller) recordTag(ctx context.Context, in integrationRow, client *polylens.Client, t pendingTag, err error) {
	state, msg := "tagged", ""
	switch {
	case err == nil:
	case time.Since(t.requested) > tagGiveUp:
		state, msg = "failed", msgTagFailed
		if errors.Is(err, polylens.ErrForbidden) {
			msg = msgTagRole
		}
		// Give-up: the per-device tag was kept while retrying; remove it now
		// (best effort, never blocks recording the outcome).
		dctx, dcancel := context.WithTimeout(ctx, p.lensTimeout())
		if derr := client.DeleteTagByName(dctx, in.accountID, tagPrefix+t.deviceID); derr != nil {
			p.log.Info("poly lens tag cleanup after give-up failed", "device_id", t.deviceID, "error", derr)
		}
		dcancel()
	default:
		state = "pending"
		if !errors.Is(err, polylens.ErrForbidden) {
			msg = msgTagFailed
		}
		p.log.Info("poly lens tagging will be retried", "device_id", t.deviceID, "error", err)
	}
	if _, uerr := p.admin.Exec(ctx, `
		UPDATE integration_devices SET tag_state = $3, tag_error = NULLIF($4, ''), tag_attempted_at = now()
		 WHERE device_id = $1 AND customer_id = $2 AND tag_state = 'pending'`,
		t.deviceID, in.customerID, state, msg); uerr != nil {
		p.log.Warn("record lens tag state failed", "device_id", t.deviceID, "error", uerr)
	}
}
