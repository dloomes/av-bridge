package collectorupdate

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/jackc/pgx/v5"
)

// channelPending mirrors commands.ChannelPending (that package imports
// this one, so it can't be imported back). A NOTIFY on it wakes the
// collector's held /bridge/poll so an Update press is delivered at once.
const channelPending = "cmd_pending"

// Timeouts for attempts that go quiet.
const (
	// InProgressTimeout: a collector that took an update and hasn't
	// reported back by now has failed (unless it's visibly on the new
	// version — then it just couldn't report).
	InProgressTimeout = 15 * time.Minute
	// RequestedTimeout: an Update press for a collector that never comes
	// online lapses rather than firing days later.
	RequestedTimeout = 24 * time.Hour
)

// Instruction is the offer sent to the bridge. Field names match the
// bridge's update.Instruction.
type Instruction struct {
	Version   string `json:"version"`
	Artefact  string `json:"artefact"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	Signature string `json:"signature"`
}

// PollMeta is what a bridge reports about itself on each poll. Bridges
// older than the updater send none of it.
type PollMeta struct {
	Version       string `json:"version"`
	Platform      string `json:"platform"`
	UpdateCapable *bool  `json:"update_capable"`
	UpdateBlocker string `json:"update_blocker"`
}

// Service ties the bundled release to collector rows. Bridge-facing
// calls run on the admin pool (the bridge is authenticated by HMAC, not
// a tenant session); portal-facing ones take the caller's tenant tx.
type Service struct {
	store   *db.Store
	release *Release
	log     *slog.Logger
	now     func() time.Time
}

func NewService(store *db.Store, release *Release, log *slog.Logger) *Service {
	return &Service{store: store, release: release, log: log, now: time.Now}
}

// Release is the bundled release, or nil.
func (s *Service) Release() *Release {
	if s == nil {
		return nil
	}
	return s.release
}

// Offer records what the bridge reported and returns an update for it
// to install, or nil.
func (s *Service) Offer(ctx context.Context, collectorID string, meta PollMeta) *Instruction {
	if meta.Version == "" || meta.UpdateCapable == nil {
		return nil // pre-updater bridge
	}
	pool := s.store.AdminPool()
	// bridge_version too: polls are the freshest signal of what the
	// collector runs (ingest and config pulls also stamp it, but later),
	// and the portal decides "update available" from it.
	if _, err := pool.Exec(ctx, `
		UPDATE collectors
		   SET bridge_platform = $2, update_capable = $3, update_blocker = NULLIF($4, ''),
		       bridge_version = $5
		 WHERE id = $1
		   AND (bridge_platform IS DISTINCT FROM $2
		     OR update_capable IS DISTINCT FROM $3
		     OR update_blocker IS DISTINCT FROM NULLIF($4, '')
		     OR bridge_version IS DISTINCT FROM $5)`,
		collectorID, meta.Platform, *meta.UpdateCapable, meta.UpdateBlocker, meta.Version,
	); err != nil {
		s.log.Warn("record update capability", "collector", collectorID, "error", err)
		return nil
	}

	var (
		state, target, tz string
		windowStart       *int16
	)
	if err := pool.QueryRow(ctx, `
		SELECT c.update_state, COALESCE(c.update_target_version, ''),
		       c.update_window_start, COALESCE(NULLIF(b.timezone, ''), 'Europe/London')
		  FROM collectors c LEFT JOIN buildings b ON b.id = c.building_id
		 WHERE c.id = $1`, collectorID,
	).Scan(&state, &target, &windowStart, &tz); err != nil {
		s.log.Warn("load collector update state", "collector", collectorID, "error", err)
		return nil
	}

	// A collector that's come back on the version it was sent has
	// succeeded, whether or not its own report made it through.
	if state == "in_progress" && target != "" && meta.Version == target {
		s.setState(ctx, collectorID, "in_progress", "succeeded", "")
		return nil
	}

	r := s.release
	ok, reason := r.Eligibility(Collector{
		Version: meta.Version, Platform: meta.Platform,
		Capable: meta.UpdateCapable, Blocker: meta.UpdateBlocker,
	})
	switch {
	case state == "requested":
		if !ok {
			s.setState(ctx, collectorID, "requested", "failed", reason)
			return nil
		}
	case windowStart != nil && ok:
		// Automatic: once per release. A failed or rolled-back attempt
		// at this version isn't retried by the schedule; Update can.
		if target == r.Version && state != "idle" && state != "succeeded" {
			return nil
		}
		if !InWindow(s.now(), tz, int(*windowStart)) {
			return nil
		}
	default:
		return nil
	}

	art, _ := ArtefactFor(meta.Platform)
	f := r.Files[art]
	tag, err := pool.Exec(ctx, `
		UPDATE collectors
		   SET update_state = 'in_progress', update_target_version = $3,
		       update_message = NULL, update_state_at = now()
		 WHERE id = $1 AND update_state = $2`,
		collectorID, state, r.Version)
	if err != nil || tag.RowsAffected() == 0 {
		return nil // raced with another poll, or failed — next poll retries
	}
	s.log.Info("offering collector update", "collector", collectorID,
		"from", meta.Version, "to", r.Version, "trigger", map[bool]string{true: "manual", false: "schedule"}[state == "requested"])
	return &Instruction{
		Version: r.Version, Artefact: art, URL: "/public/downloads/" + art,
		SHA256: f.SHA256, Signature: f.Signature,
	}
}

// Report applies a state reported by the bridge for the attempt at
// version. Reports for any other version are stale and ignored.
func (s *Service) Report(ctx context.Context, collectorID, state, version, message string) error {
	var next string
	switch state {
	case "restarting":
		next = "in_progress"
		if message == "" {
			message = "Restarting on the new version"
		}
	case "succeeded", "failed", "rolled_back":
		next = state
	default:
		return errors.New("unknown update state")
	}
	if len(message) > 500 {
		message = message[:500]
	}
	_, err := s.store.AdminPool().Exec(ctx, `
		UPDATE collectors
		   SET update_state = $3, update_message = NULLIF($4, ''), update_state_at = now()
		 WHERE id = $1 AND update_target_version = $2
		   AND update_state IN ('in_progress', 'requested')`,
		collectorID, version, next, message)
	return err
}

func (s *Service) setState(ctx context.Context, collectorID, from, to, message string) {
	if _, err := s.store.AdminPool().Exec(ctx, `
		UPDATE collectors
		   SET update_state = $3, update_message = NULLIF($4, ''), update_state_at = now()
		 WHERE id = $1 AND update_state = $2`,
		collectorID, from, to, message); err != nil {
		s.log.Warn("set collector update state", "collector", collectorID, "error", err)
	}
}

// Sweep resolves attempts that went quiet. Exported for tests.
func (s *Service) Sweep(ctx context.Context) {
	pool := s.store.AdminPool()
	if _, err := pool.Exec(ctx, `
		UPDATE collectors
		   SET update_state = CASE WHEN bridge_version = update_target_version
		                           THEN 'succeeded' ELSE 'failed' END,
		       update_message = CASE WHEN bridge_version = update_target_version
		                             THEN NULL
		                             ELSE 'No result from the collector within 15 minutes' END,
		       update_state_at = now()
		 WHERE update_state = 'in_progress'
		   AND update_state_at < now() - make_interval(secs => $1)`,
		int(InProgressTimeout.Seconds())); err != nil {
		s.log.Warn("collector update sweep (in progress)", "error", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE collectors
		   SET update_state = 'failed',
		       update_message = 'The collector didn''t come online within 24 hours',
		       update_state_at = now()
		 WHERE update_state = 'requested'
		   AND update_state_at < now() - make_interval(secs => $1)`,
		int(RequestedTimeout.Seconds())); err != nil {
		s.log.Warn("collector update sweep (requested)", "error", err)
	}
}

// Run sweeps once a minute until ctx ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.Sweep(ctx)
		}
	}
}

// ErrNotEligible carries the reason a collector can't be updated now.
type ErrNotEligible struct{ Reason string }

func (e ErrNotEligible) Error() string { return e.Reason }

// ErrInFlight: an update is already requested or running.
var ErrInFlight = errors.New("an update is already in progress for this collector")

// Request marks a collector for update to the bundled release, inside the
// caller's tenant tx (RLS confines it to their collectors). Returns
// pgx.ErrNoRows if the collector isn't visible.
func (s *Service) Request(ctx context.Context, tx pgx.Tx, collectorID string) error {
	var (
		c     Collector
		state string
	)
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(bridge_version, ''), COALESCE(bridge_platform, ''),
		       update_capable, COALESCE(update_blocker, ''), update_state
		  FROM collectors WHERE id = $1 FOR UPDATE`, collectorID,
	).Scan(&c.Version, &c.Platform, &c.Capable, &c.Blocker, &state)
	if err != nil {
		return err
	}
	if state == "requested" || state == "in_progress" {
		return ErrInFlight
	}
	if ok, reason := s.Release().Eligibility(c); !ok {
		return ErrNotEligible{Reason: reason}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE collectors
		   SET update_state = 'requested', update_target_version = $2,
		       update_message = NULL, update_state_at = now()
		 WHERE id = $1`, collectorID, s.release.Version); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `SELECT pg_notify($1, $2)`, channelPending, collectorID)
	return err
}
