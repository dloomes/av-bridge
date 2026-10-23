package nightly

import (
	"context"
	"time"
)

// One-off schedule dates (migration 0066). A date closes a room, or every
// room, for the day (no power-on, no readiness check), or opens it with
// its own times, whatever the weekly schedule says. A room's own date
// wins over an estate-wide one. Dates are in the organisation's timezone.

type schedDate struct {
	closed  bool
	on, off *time.Duration // open dates: their times, nil for the usual
}

// dateBook holds one organisation's dates, keyed "YYYY-MM-DD".
type dateBook struct {
	estate map[string]schedDate
	rooms  map[string]map[string]schedDate // room id → date → entry
}

// lookup finds the date that applies to room on day: the room's, else the
// estate's.
func (b *dateBook) lookup(roomID string, day time.Time) (schedDate, bool) {
	if b == nil {
		return schedDate{}, false
	}
	key := day.Format(time.DateOnly)
	if d, ok := b.rooms[roomID][key]; ok {
		return d, true
	}
	d, ok := b.estate[key]
	return d, ok
}

// plan is what a room does on one local day: whether it's in use, and
// when it powers on and off.
type plan struct {
	active  bool
	on, off time.Duration
}

// dayPlan applies the room's dates over its weekly schedule.
func (b *dateBook) dayPlan(roomID string, day time.Time, days []int, on, off time.Duration) plan {
	if d, ok := b.lookup(roomID, day); ok {
		if d.closed {
			return plan{}
		}
		p := plan{active: true, on: on, off: off}
		if d.on != nil {
			p.on = *d.on
		}
		if d.off != nil {
			p.off = *d.off
		}
		return p
	}
	return plan{active: containsInt(days, isoWeekday(day)), on: on, off: off}
}

// loadDateBooks loads every organisation's dates near today (the
// scheduler's clock): from a couple of days back (a run that started then)
// to past a fortnight ahead (the furthest a power-on is looked for), with
// a day's slack either way for timezones.
func (s *Scheduler) loadDateBooks(ctx context.Context) (map[string]*dateBook, error) {
	today := s.wallNow().UTC().Format(time.DateOnly)
	rows, err := s.pool.Query(ctx, `
		SELECT customer_id::text, COALESCE(room_id::text, ''), on_date, closed,
		       power_on_time, power_off_time
		  FROM schedule_date
		 WHERE on_date BETWEEN $1::date - 2 AND $1::date + 16`, today)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	books := map[string]*dateBook{}
	for rows.Next() {
		var (
			cust, room string
			day        time.Time
			closed     bool
			on, off    *time.Time
		)
		if err := rows.Scan(&cust, &room, &day, &closed, &on, &off); err != nil {
			return nil, err
		}
		b := books[cust]
		if b == nil {
			b = &dateBook{estate: map[string]schedDate{}, rooms: map[string]map[string]schedDate{}}
			books[cust] = b
		}
		d := schedDate{closed: closed}
		if on != nil {
			v := timeOfDayToDuration(*on)
			d.on = &v
		}
		if off != nil {
			v := timeOfDayToDuration(*off)
			d.off = &v
		}
		key := day.Format(time.DateOnly)
		if room == "" {
			b.estate[key] = d
		} else {
			if b.rooms[room] == nil {
				b.rooms[room] = map[string]schedDate{}
			}
			b.rooms[room][key] = d
		}
	}
	return books, rows.Err()
}

// nextPowerOn is when a room switched off at scheduledOffLocal next powers
// on: later the same day if that day's plan powers on after the switch-off,
// otherwise on the next day in use, up to a fortnight ahead. The zero time
// means no day in that fortnight is in use.
func nextPowerOn(scheduledOffLocal time.Time, planFor func(day time.Time) plan) time.Time {
	loc := scheduledOffLocal.Location()
	base := time.Date(scheduledOffLocal.Year(), scheduledOffLocal.Month(), scheduledOffLocal.Day(), 0, 0, 0, 0, loc)
	at := func(day time.Time, d time.Duration) time.Time {
		return time.Date(day.Year(), day.Month(), day.Day(), int(d/time.Hour), int((d%time.Hour)/time.Minute), 0, 0, loc)
	}
	for i := 0; i <= 14; i++ {
		day := base.AddDate(0, 0, i)
		p := planFor(day)
		if !p.active {
			continue
		}
		if on := at(day, p.on); on.After(scheduledOffLocal) {
			return on
		}
	}
	// Nothing in use for a fortnight (closed for works, say): keep
	// waiting. The zero time means "not yet"; later ticks look again as
	// more days come into range.
	return time.Time{}
}
