// Package collectorha runs the lease that decides which machine of a
// collector group (a primary and its warm standbys, migration 0051) polls
// the group's devices.
//
// The lease lives on the primary's row. Every /bridge/poll from a group
// machine calls Acquire, which renews the lease for its holder, hands it
// to the poller when it has lapsed (failover) or when an operator asked
// for that machine (handover), and otherwise reports "standby". Only the
// holder gets the device list and commands.
//
// Safety: the bridge stops polling devices FenceAfter after its last
// successful poll as holder, which is well inside TTL. The cloud only
// gives the lease away after TTL, so the old holder has always stopped
// before the new one starts — even when the old holder is cut off from
// the cloud and can't be told.
package collectorha

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	// TTL is how long a lease lasts without renewal. Holders renew on
	// every poll (at most ~25s apart while the long-poll holds).
	TTL = 60 * time.Second
	// HandoverGrace is how long a handed-over lease waits before the new
	// holder may start, so the old holder has stopped first.
	HandoverGrace = 10 * time.Second
)

// channelPending mirrors commands.ChannelPending (commands imports this
// package). A NOTIFY wakes a machine's held poll so a role change lands
// within a second.
const channelPending = "cmd_pending"

// Lease is a machine's standing in its group after Acquire.
type Lease struct {
	// Grouped is false for a collector with no standbys: no lease, it
	// always serves its own devices (the pre-HA behaviour).
	Grouped bool
	// Active: this machine holds the lease and serves the group.
	Active bool
	// GroupID is the primary's id — owner of the devices and commands.
	GroupID string
}

// Role is the wire value for the poll response ("" when not grouped).
func (l Lease) Role() string {
	switch {
	case !l.Grouped:
		return ""
	case l.Active:
		return "active"
	default:
		return "standby"
	}
}

// Manager runs lease operations on the admin pool: bridge calls are
// authenticated by HMAC, not a tenant session.
type Manager struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func NewManager(store *db.Store, log *slog.Logger) *Manager {
	return &Manager{pool: store.AdminPool(), log: log}
}

// Acquire renews, takes over or declines the group lease for machine m.
func (mgr *Manager) Acquire(ctx context.Context, m db.Collector) (Lease, error) {
	gid := m.GroupID()
	lease := Lease{GroupID: gid}

	grouped, err := mgr.grouped(ctx, m)
	if err != nil {
		return lease, err
	}
	if !grouped {
		lease.Active = true
		return lease, nil
	}
	lease.Grouped = true

	var (
		active  bool
		changed bool
		prev    *string
		cust    string
	)
	err = pgx.BeginFunc(ctx, mgr.pool, func(tx pgx.Tx) error {
		var holder, handover *string
		var expires, notBefore *time.Time
		if err := tx.QueryRow(ctx, `
			SELECT lease_holder::text, lease_expires_at, lease_not_before,
			       lease_handover_to::text, customer_id::text
			  FROM collectors WHERE id = $1 FOR UPDATE`, gid,
		).Scan(&holder, &expires, &notBefore, &handover, &cust); err != nil {
			return err
		}
		prev = holder
		now := time.Now()
		isHolder := holder != nil && *holder == m.ID
		lapsed := holder == nil || expires == nil || !expires.After(now)

		switch {
		// The operator asked for another machine: the holder steps down
		// now, and the lease waits HandoverGrace for the target.
		case isHolder && handover != nil && *handover != m.ID:
			_, err := tx.Exec(ctx, `
				UPDATE collectors
				   SET lease_holder = lease_handover_to,
				       lease_not_before = now() + make_interval(secs => $2),
				       lease_expires_at = now() + make_interval(secs => $2) + make_interval(secs => $3),
				       lease_handover_to = NULL
				 WHERE id = $1`, gid, int(HandoverGrace.Seconds()), int(TTL.Seconds()))
			if err != nil {
				return err
			}
			changed = true
			return nil

		// Holder renewing, once any handover grace has passed.
		case isHolder && (notBefore == nil || !notBefore.After(now)):
			active = true

		// Lapsed lease: take it, unless the operator asked for a
		// different machine (which then gets it when it polls).
		case !isHolder && lapsed && (handover == nil || *handover == m.ID):
			active = true
			changed = true

		default:
			return nil
		}
		_, err := tx.Exec(ctx, `
			UPDATE collectors
			   SET lease_holder = $2,
			       lease_expires_at = now() + make_interval(secs => $3),
			       lease_not_before = NULL,
			       lease_handover_to = CASE WHEN lease_handover_to = $2 THEN NULL ELSE lease_handover_to END
			 WHERE id = $1`, gid, m.ID, int(TTL.Seconds()))
		return err
	})
	if err != nil {
		return lease, err
	}
	lease.Active = active

	if active && m.StandbyFor != "" {
		// Device freshness follows whichever machine is serving.
		if _, err := mgr.pool.Exec(ctx,
			`UPDATE collectors SET serving_seen_at = now() WHERE id = $1`, gid); err != nil {
			mgr.log.Warn("stamp serving_seen_at", "collector", gid, "error", err)
		}
	}
	if changed {
		mgr.onChange(ctx, gid, cust, prev, m, active)
	}
	return lease, nil
}

// grouped reports whether m belongs to a group with more than one machine.
func (mgr *Manager) grouped(ctx context.Context, m db.Collector) (bool, error) {
	if m.StandbyFor != "" {
		return true, nil
	}
	var has bool
	err := mgr.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM collectors WHERE standby_for = $1)`, m.ID).Scan(&has)
	return has, err
}

// onChange runs after the holder changed: every machine re-pulls its
// config (the new holder gets the devices, the rest an empty set), held
// polls are woken, and the change is audited.
func (mgr *Manager) onChange(ctx context.Context, gid, cust string, prev *string, m db.Collector, nowActive bool) {
	if _, err := mgr.pool.Exec(ctx,
		`UPDATE collectors SET config_version = config_version + 1 WHERE id = $1`, gid); err != nil {
		mgr.log.Warn("bump config after lease change", "collector", gid, "error", err)
	}
	if err := mgr.notifyGroup(ctx, gid); err != nil {
		mgr.log.Warn("notify group after lease change", "collector", gid, "error", err)
	}
	from := ""
	if prev != nil {
		from = *prev
	}
	action, to := "collector.failover", m.ID
	if !nowActive {
		// m stepped down for an operator handover.
		action = "collector.handover"
		to = ""
		_ = mgr.pool.QueryRow(ctx, `SELECT COALESCE(lease_holder::text, '') FROM collectors WHERE id = $1`, gid).Scan(&to)
	} else if from == "" {
		action = "collector.lease_granted"
	}
	mgr.log.Info("collector group lease changed", "group", gid, "action", action, "from", from, "to", to)
	if err := pgx.BeginFunc(ctx, mgr.pool, func(tx pgx.Tx) error {
		return audit.Record(ctx, tx, cust, audit.Entry{
			Actor: "system:collector-ha", Action: action,
			TargetKind: "collector", TargetID: gid,
			RelatedTargetKind: "collector", RelatedTargetID: to,
			Metadata: map[string]any{"from_machine": from, "to_machine": to},
		})
	}); err != nil {
		mgr.log.Warn("audit lease change", "collector", gid, "error", err)
	}
}

// notifyGroup wakes the held poll of every machine in the group.
func (mgr *Manager) notifyGroup(ctx context.Context, gid string) error {
	_, err := mgr.pool.Exec(ctx, `
		SELECT pg_notify($1, id::text)
		  FROM collectors WHERE id = $2 OR standby_for = $2`, channelPending, gid)
	return err
}

// IsActive reports, without changing anything, whether m currently
// serves its group. Used by the config pull: only the holder gets
// devices. Ungrouped collectors are always active.
func (mgr *Manager) IsActive(ctx context.Context, m db.Collector) (grouped, active bool, err error) {
	grouped, err = mgr.grouped(ctx, m)
	if err != nil || !grouped {
		return grouped, !grouped, err
	}
	err = mgr.pool.QueryRow(ctx, `
		SELECT COALESCE(lease_holder = $2
		       AND lease_expires_at > now()
		       AND (lease_not_before IS NULL OR lease_not_before <= now()), false)
		  FROM collectors WHERE id = $1`, m.GroupID(), m.ID).Scan(&active)
	return true, active, err
}

// ErrNotInGroup / ErrMachineOffline are returned by RequestHandover.
var (
	ErrNotInGroup     = errors.New("this collector has no standby, so there's nothing to switch")
	ErrMachineOffline = errors.New("that machine isn't online, so it can't take over")
)

// RequestHandover asks for machine id to become the group's active
// machine (the portal's "Make active"). Runs in the caller's tenant tx.
// The current holder steps down at its next poll; the target starts
// HandoverGrace later. If the lease has already lapsed, the target takes
// it on its next poll.
func RequestHandover(ctx context.Context, tx pgx.Tx, id string) (groupID string, err error) {
	var (
		standbyFor *string
		lastSeen   *time.Time
		hasStandby bool
	)
	if err := tx.QueryRow(ctx, `
		SELECT standby_for::text, last_seen_at,
		       EXISTS (SELECT 1 FROM collectors s WHERE s.standby_for = c.id)
		  FROM collectors c WHERE id = $1`, id,
	).Scan(&standbyFor, &lastSeen, &hasStandby); err != nil {
		return "", err
	}
	if standbyFor == nil && !hasStandby {
		return "", ErrNotInGroup
	}
	if lastSeen == nil || time.Since(*lastSeen) > 2*time.Minute {
		return "", ErrMachineOffline
	}
	groupID = id
	if standbyFor != nil {
		groupID = *standbyFor
	}
	var alreadyActive bool
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(lease_holder = $2 AND lease_expires_at > now(), false)
		  FROM collectors WHERE id = $1`, groupID, id).Scan(&alreadyActive); err != nil {
		return "", err
	}
	if alreadyActive {
		_, err = tx.Exec(ctx, `UPDATE collectors SET lease_handover_to = NULL WHERE id = $1`, groupID)
		return groupID, err
	}
	if _, err := tx.Exec(ctx,
		`UPDATE collectors SET lease_handover_to = $2 WHERE id = $1`, groupID, id); err != nil {
		return "", err
	}
	_, err = tx.Exec(ctx, `
		SELECT pg_notify($1, c.id::text)
		  FROM collectors c WHERE c.id = $2 OR c.standby_for = $2`, channelPending, groupID)
	return groupID, err
}
