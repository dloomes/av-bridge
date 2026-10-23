package nightly

import (
	"testing"
	"time"
)

func TestNextPowerOnWithDates(t *testing.T) {
	weekdays := []int{1, 2, 3, 4, 5}
	on, off := 7*time.Hour+30*time.Minute, 19*time.Hour
	eight, five := 8*time.Hour, 17*time.Hour
	book := &dateBook{
		estate: map[string]schedDate{
			"2030-01-12": {on: &eight, off: &five}, // a Saturday sitting
			"2030-01-21": {closed: true},           // a bank holiday Monday
		},
		rooms: map[string]map[string]schedDate{
			"court-3": {"2030-01-14": {closed: true}}, // works in Court 3
		},
	}
	next := func(room string, offAt time.Time) string {
		got := nextPowerOn(offAt, func(day time.Time) plan {
			return book.dayPlan(room, day, weekdays, on, off)
		})
		if got.IsZero() {
			return "none"
		}
		return got.Format("Mon 2006-01-02 15:04")
	}
	friday := time.Date(2030, 1, 11, 19, 0, 0, 0, time.UTC)
	cases := []struct {
		name, room string
		offAt      time.Time
		want       string
	}{
		{"weeknight", "court-1", time.Date(2030, 1, 9, 19, 0, 0, 0, time.UTC), "Thu 2030-01-10 07:30"},
		{"open Saturday, at its own time", "court-1", friday, "Sat 2030-01-12 08:00"},
		{"after the Saturday sitting", "court-1", time.Date(2030, 1, 12, 17, 0, 0, 0, time.UTC), "Mon 2030-01-14 07:30"},
		{"the room's closed Monday", "court-3", time.Date(2030, 1, 12, 17, 0, 0, 0, time.UTC), "Tue 2030-01-15 07:30"},
		{"estate-wide bank holiday", "court-1", time.Date(2030, 1, 18, 19, 0, 0, 0, time.UTC), "Tue 2030-01-22 07:30"},
	}
	for _, c := range cases {
		if got := next(c.room, c.offAt); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}

	// A room's own date wins over the estate's.
	book.rooms["court-2"] = map[string]schedDate{"2030-01-21": {}}
	if p := book.dayPlan("court-2", time.Date(2030, 1, 21, 0, 0, 0, 0, time.UTC), weekdays, on, off); !p.active || p.off != off {
		t.Errorf("room open on the estate's closed day: %+v", p)
	}

	// Closed for longer than a fortnight: no power-on yet, keep waiting.
	shut := &dateBook{estate: map[string]schedDate{}}
	for d := 0; d < 20; d++ {
		shut.estate[time.Date(2030, 1, 10+d, 0, 0, 0, 0, time.UTC).Format(time.DateOnly)] = schedDate{closed: true}
	}
	if got := nextPowerOn(time.Date(2030, 1, 9, 19, 0, 0, 0, time.UTC), func(day time.Time) plan {
		return shut.dayPlan("court-1", day, weekdays, on, off)
	}); !got.IsZero() {
		t.Errorf("closed for three weeks, but powers on %s", got)
	}

	// No dates at all: the weekly schedule, as before.
	var none *dateBook
	if got := nextPowerOn(friday, func(day time.Time) plan {
		return none.dayPlan("court-1", day, weekdays, on, off)
	}); got.Format("Mon 15:04") != "Mon 07:30" {
		t.Errorf("no dates: %s", got)
	}
}
