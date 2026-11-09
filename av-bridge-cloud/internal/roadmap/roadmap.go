// Package roadmap holds the product roadmap shown in the portal: Involve's
// own plans, global (no customer_id, no RLS). Content is read through the
// admin pool and permission-checked in the handlers.
package roadmap

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

type Settings struct {
	Title      string    `json:"title"`
	Headline   string    `json:"headline"`
	Lede       string    `json:"lede"`
	Disclaimer string    `json:"disclaimer"`
	AsOf       string    `json:"as_of"` // YYYY-MM-DD
	UpdatedAt  time.Time `json:"updated_at"`
}

type Period struct {
	ID        string `json:"id"`
	Position  int    `json:"position"`
	Name      string `json:"name"`
	Months    string `json:"months"`
	IsCurrent bool   `json:"is_current"`
}

type Theme struct {
	ID       string `json:"id"`
	Position int    `json:"position"`
	Name     string `json:"name"`
	Note     string `json:"note"`
}

type Item struct {
	ID           string `json:"id"`
	ThemeID      string `json:"theme_id"`
	Period       int    `json:"period"`
	Status       string `json:"status"`
	ReleaseMonth string `json:"release_month,omitempty"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Position     int    `json:"position"`
}

type Shipped struct {
	ID       string `json:"id"`
	When     string `json:"when"`
	Title    string `json:"title"`
	Position int    `json:"position"`
}

type Roadmap struct {
	Settings Settings  `json:"settings"`
	Periods  []Period  `json:"periods"`
	Themes   []Theme   `json:"themes"`
	Items    []Item    `json:"items"`
	Shipped  []Shipped `json:"shipped"`
}

// CanEdit reports whether p may edit the roadmap: an Involve user the
// helpdesk admin has flagged as a roadmap editor.
func CanEdit(p portalauth.Principal) bool { return p.IsVendor && p.RoadmapEditor }

// Load reads the whole roadmap, each list ordered by position. Slices are
// never nil so they marshal as [].
func Load(ctx context.Context, q pgx.Tx) (Roadmap, error) {
	r := Roadmap{Periods: []Period{}, Themes: []Theme{}, Items: []Item{}, Shipped: []Shipped{}}
	if err := q.QueryRow(ctx, `
		SELECT title, headline, lede, disclaimer, to_char(as_of, 'YYYY-MM-DD'), updated_at
		  FROM roadmap_settings`).
		Scan(&r.Settings.Title, &r.Settings.Headline, &r.Settings.Lede,
			&r.Settings.Disclaimer, &r.Settings.AsOf, &r.Settings.UpdatedAt); err != nil {
		return Roadmap{}, err
	}

	rows, err := q.Query(ctx, `SELECT id::text, position, name, months, is_current
		FROM roadmap_periods ORDER BY position`)
	if err != nil {
		return Roadmap{}, err
	}
	for rows.Next() {
		var x Period
		if err := rows.Scan(&x.ID, &x.Position, &x.Name, &x.Months, &x.IsCurrent); err != nil {
			rows.Close()
			return Roadmap{}, err
		}
		r.Periods = append(r.Periods, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Roadmap{}, err
	}

	rows, err = q.Query(ctx, `SELECT id::text, position, name, note
		FROM roadmap_themes ORDER BY position, name`)
	if err != nil {
		return Roadmap{}, err
	}
	for rows.Next() {
		var x Theme
		if err := rows.Scan(&x.ID, &x.Position, &x.Name, &x.Note); err != nil {
			rows.Close()
			return Roadmap{}, err
		}
		r.Themes = append(r.Themes, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Roadmap{}, err
	}

	rows, err = q.Query(ctx, `
		SELECT id::text, theme_id::text, period_position, status,
		       COALESCE(release_month, ''), title, description, position
		  FROM roadmap_items ORDER BY period_position, position, title`)
	if err != nil {
		return Roadmap{}, err
	}
	for rows.Next() {
		var x Item
		if err := rows.Scan(&x.ID, &x.ThemeID, &x.Period, &x.Status,
			&x.ReleaseMonth, &x.Title, &x.Description, &x.Position); err != nil {
			rows.Close()
			return Roadmap{}, err
		}
		r.Items = append(r.Items, x)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Roadmap{}, err
	}

	rows, err = q.Query(ctx, `SELECT id::text, when_label, title, position
		FROM roadmap_shipped ORDER BY position, title`)
	if err != nil {
		return Roadmap{}, err
	}
	for rows.Next() {
		var x Shipped
		if err := rows.Scan(&x.ID, &x.When, &x.Title, &x.Position); err != nil {
			rows.Close()
			return Roadmap{}, err
		}
		r.Shipped = append(r.Shipped, x)
	}
	rows.Close()
	return r, rows.Err()
}
