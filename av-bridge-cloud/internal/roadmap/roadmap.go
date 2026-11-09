// Package roadmap holds the product roadmap shown in the portal: Involve's
// own plans, global (no customer_id, no RLS). Content is read through the
// admin pool and permission-checked in the handlers.
package roadmap

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

// ---- Editor writes ----
//
// Every write takes the caller's transaction, validates its input (trimmed,
// length-checked; a *ValidationError is a 400 with its message) and leaves
// positions rewritten as 0..n-1. by is what the *_by columns record.

// ValidationError is bad editor input; Error() is a plain message for the
// editor.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

var (
	// ErrNotFound is an item id that does not exist.
	ErrNotFound = errors.New("not found")
	// ErrThemeHasItems refuses removing a theme that still holds items.
	ErrThemeHasItems = errors.New("Move or delete this theme's items first.")
)

// Statuses an item may take.
var Statuses = map[string]bool{"committed": true, "planned": true, "exploring": true}

const maxListLen = 50

// text trims s and checks its length in characters.
func text(s, field string, min, max int) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	if n < min || n > max {
		if n == 0 {
			return "", invalid(field + " is required.")
		}
		return "", invalid(fmt.Sprintf("%s must be %d characters or fewer.", field, max))
	}
	return s, nil
}

type SettingsInput struct {
	Title      string `json:"title"`
	Headline   string `json:"headline"`
	Lede       string `json:"lede"`
	Disclaimer string `json:"disclaimer"`
	AsOf       string `json:"as_of"`
}

// SaveSettings replaces the roadmap's header text and as-of date.
func SaveSettings(ctx context.Context, tx pgx.Tx, in SettingsInput, by string) error {
	var err error
	if in.Title, err = text(in.Title, "Title", 1, 80); err != nil {
		return err
	}
	if in.Headline, err = text(in.Headline, "Headline", 1, 160); err != nil {
		return err
	}
	if in.Lede, err = text(in.Lede, "Introduction", 1, 600); err != nil {
		return err
	}
	if in.Disclaimer, err = text(in.Disclaimer, "Disclaimer", 1, 600); err != nil {
		return err
	}
	asOf, perr := time.Parse("2006-01-02", strings.TrimSpace(in.AsOf))
	if perr != nil {
		return invalid("As-of date must be a date (YYYY-MM-DD).")
	}
	if err := lockList(ctx, tx, "roadmap_settings"); err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		UPDATE roadmap_settings
		   SET title = $1, headline = $2, lede = $3, disclaimer = $4, as_of = $5::date,
		       updated_at = now(), updated_by = $6`,
		in.Title, in.Headline, in.Lede, in.Disclaimer, asOf.Format("2006-01-02"), by)
	return err
}

// ItemInput is a create (every field required except ReleaseMonth) or a
// patch (nil fields are left alone; ReleaseMonth "" clears it).
type ItemInput struct {
	ThemeID      *string `json:"theme_id"`
	Period       *int    `json:"period"`
	Status       *string `json:"status"`
	ReleaseMonth *string `json:"release_month"`
	Title        *string `json:"title"`
	Description  *string `json:"description"`
}

type itemRow struct {
	themeID, status, title, description string
	period                              int
	releaseMonth                        *string
}

func themeExists(ctx context.Context, tx pgx.Tx, id string) (bool, error) {
	var ok bool
	err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM roadmap_themes WHERE id::text = $1)`, id).Scan(&ok)
	return ok, err
}

func applyItem(ctx context.Context, tx pgx.Tx, row *itemRow, in ItemInput) error {
	var err error
	if in.ThemeID != nil {
		id := strings.TrimSpace(*in.ThemeID)
		ok, err := themeExists(ctx, tx, id)
		if err != nil {
			return err
		}
		if !ok {
			return invalid("Choose a theme.")
		}
		row.themeID = id
	}
	if in.Period != nil {
		if *in.Period < 0 || *in.Period > 3 {
			return invalid("Period must be 0 to 3.")
		}
		row.period = *in.Period
	}
	if in.Status != nil {
		s := strings.TrimSpace(*in.Status)
		if !Statuses[s] {
			return invalid("Status must be committed, planned or exploring.")
		}
		row.status = s
	}
	if in.ReleaseMonth != nil {
		s, err := text(*in.ReleaseMonth, "Release month", 0, 20)
		if err != nil {
			return err
		}
		if s == "" {
			row.releaseMonth = nil
		} else {
			row.releaseMonth = &s
		}
	}
	if in.Title != nil {
		if row.title, err = text(*in.Title, "Title", 1, 80); err != nil {
			return err
		}
	}
	if in.Description != nil {
		if row.description, err = text(*in.Description, "Description", 1, 240); err != nil {
			return err
		}
	}
	return nil
}

// CreateItem adds an item at the end of its theme and period, and returns
// its id.
func CreateItem(ctx context.Context, tx pgx.Tx, in ItemInput, by string) (string, error) {
	switch {
	case in.ThemeID == nil:
		return "", invalid("Choose a theme.")
	case in.Period == nil:
		return "", invalid("Choose a period.")
	case in.Status == nil:
		return "", invalid("Choose a status.")
	case in.Title == nil:
		return "", invalid("Title is required.")
	case in.Description == nil:
		return "", invalid("Description is required.")
	}
	var row itemRow
	if err := applyItem(ctx, tx, &row, in); err != nil {
		return "", err
	}
	if err := lockCell(ctx, tx, row.themeID, row.period); err != nil {
		return "", err
	}
	var id string
	err := tx.QueryRow(ctx, `
		INSERT INTO roadmap_items (theme_id, period_position, status, release_month, title, description, position, updated_by)
		VALUES ($1::uuid, $2, $3, $4, $5, $6,
		        (SELECT count(*) FROM roadmap_items WHERE theme_id = $1::uuid AND period_position = $2), $7)
		RETURNING id::text`,
		row.themeID, row.period, row.status, row.releaseMonth, row.title, row.description, by).Scan(&id)
	if err != nil {
		return "", themeGone(err)
	}
	return id, renumber(ctx, tx, row.themeID, row.period)
}

// UpdateItem applies a patch. An item moved to another theme or period goes
// to the end of its new cell, and both cells are renumbered.
func UpdateItem(ctx context.Context, tx pgx.Tx, id string, in ItemInput, by string) error {
	readItem := func(lock string) (itemRow, error) {
		var r itemRow
		err := tx.QueryRow(ctx, `
			SELECT theme_id::text, period_position, status, release_month, title, description
			  FROM roadmap_items WHERE id::text = $1`+lock, id).
			Scan(&r.themeID, &r.period, &r.status, &r.releaseMonth, &r.title, &r.description)
		if errors.Is(err, pgx.ErrNoRows) {
			return r, ErrNotFound
		}
		return r, err
	}

	// Lock order matches OrderItems and CreateItem: cell advisory locks
	// first, then row locks. So work out both cells from an unlocked read,
	// lock them (sorted, de-duplicated), and only then lock the row.
	peek, err := readItem("")
	if err != nil {
		return err
	}
	oldTheme, oldPeriod := peek.themeID, peek.period
	dest := peek
	if err := applyItem(ctx, tx, &dest, in); err != nil {
		return err
	}
	if err := lockCells(ctx, tx, cell{oldTheme, oldPeriod}, cell{dest.themeID, dest.period}); err != nil {
		return err
	}
	row, err := readItem(" FOR UPDATE")
	if err != nil {
		return err
	}
	if row.themeID != oldTheme || row.period != oldPeriod {
		// Someone moved it between the read and the lock.
		return invalid("This item was just moved by someone else. Reload and try again.")
	}
	if err := applyItem(ctx, tx, &row, in); err != nil {
		return err
	}
	moved := row.themeID != oldTheme || row.period != oldPeriod
	_, err = tx.Exec(ctx, `
		UPDATE roadmap_items
		   SET theme_id = $2::uuid, period_position = $3, status = $4, release_month = $5,
		       title = $6, description = $7, updated_at = now(), updated_by = $8,
		       position = CASE WHEN $9::boolean THEN
		           (SELECT count(*) FROM roadmap_items WHERE theme_id = $2::uuid AND period_position = $3)
		           ELSE position END
		 WHERE id::text = $1`,
		id, row.themeID, row.period, row.status, row.releaseMonth, row.title, row.description, by, moved)
	if err != nil {
		return themeGone(err)
	}
	if moved {
		if err := renumber(ctx, tx, oldTheme, oldPeriod); err != nil {
			return err
		}
	}
	return renumber(ctx, tx, row.themeID, row.period)
}

// DeleteItem removes an item and closes the gap it leaves.
func DeleteItem(ctx context.Context, tx pgx.Tx, id string) error {
	var theme string
	var period int
	err := tx.QueryRow(ctx, `DELETE FROM roadmap_items WHERE id::text = $1
		RETURNING theme_id::text, period_position`, id).Scan(&theme, &period)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return renumber(ctx, tx, theme, period)
}

// OrderInput is the full ordered list of item ids in one theme and period.
type OrderInput struct {
	ThemeID string   `json:"theme_id"`
	Period  int      `json:"period"`
	IDs     []string `json:"ids"`
}

// OrderItems sets the order of one theme and period. The list must hold
// every item in that cell exactly once.
func OrderItems(ctx context.Context, tx pgx.Tx, in OrderInput) error {
	if in.Period < 0 || in.Period > 3 {
		return invalid("Period must be 0 to 3.")
	}
	ok, err := themeExists(ctx, tx, in.ThemeID)
	if err != nil {
		return err
	}
	if !ok {
		return invalid("Choose a theme.")
	}
	if err := lockCell(ctx, tx, in.ThemeID, in.Period); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM roadmap_items
		WHERE theme_id = $1::uuid AND period_position = $2 FOR UPDATE`, in.ThemeID, in.Period)
	if err != nil {
		return err
	}
	have, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	incomplete := invalid("The order must list every item in this theme and period, once each.")
	if len(in.IDs) != len(have) {
		return incomplete
	}
	want := make(map[string]bool, len(have))
	for _, id := range have {
		want[id] = true
	}
	for _, id := range in.IDs {
		if !want[id] {
			return incomplete
		}
		delete(want, id) // a repeated id is then missing
	}
	_, err = tx.Exec(ctx, `
		UPDATE roadmap_items i SET position = o.ord - 1
		  FROM unnest($1::text[]) WITH ORDINALITY AS o(id, ord)
		 WHERE i.id::text = o.id`, in.IDs)
	return err
}

// themeGone maps a foreign-key violation (the theme was deleted between the
// existence check and the write) to the same 400 as an unknown theme.
func themeGone(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return invalid("Choose a theme.")
	}
	return err
}

// lockList serialises replace-style saves of one list (themes, periods,
// shipped), so two concurrent saves cannot double it or interleave
// positions under READ COMMITTED.
func lockList(ctx context.Context, tx pgx.Tx, name string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, name)
	return err
}

// lockCell serialises writers to one theme and period, so concurrent
// creates and moves cannot hand out the same position.
func lockCell(ctx context.Context, tx pgx.Tx, theme string, period int) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('roadmap_cell:' || $1::text || ':' || $2::int))`,
		theme, period)
	return err
}

type cell struct {
	theme  string
	period int
}

func (c cell) key() string { return c.theme + ":" + strconv.Itoa(c.period) }

// lockCells takes lockCell on each distinct cell, sorted by key, so every
// writer that holds more than one cell lock takes them in the same order.
func lockCells(ctx context.Context, tx pgx.Tx, cells ...cell) error {
	sort.Slice(cells, func(i, j int) bool { return cells[i].key() < cells[j].key() })
	for i, c := range cells {
		if i > 0 && c == cells[i-1] {
			continue
		}
		if err := lockCell(ctx, tx, c.theme, c.period); err != nil {
			return err
		}
	}
	return nil
}

// renumber rewrites one cell's positions as 0..n-1, keeping their order.
func renumber(ctx context.Context, tx pgx.Tx, theme string, period int) error {
	_, err := tx.Exec(ctx, `
		UPDATE roadmap_items i SET position = r.rn - 1
		  FROM (SELECT id, row_number() OVER (ORDER BY position, title, id) AS rn
		          FROM roadmap_items WHERE theme_id = $1::uuid AND period_position = $2) r
		 WHERE i.id = r.id AND i.position <> r.rn - 1`, theme, period)
	return err
}

type PeriodInput struct {
	Name      string `json:"name"`
	Months    string `json:"months"`
	IsCurrent bool   `json:"is_current"`
}

// SavePeriods replaces the four periods, in order. Exactly one is current.
// Periods are keyed by position, so items keep their place.
func SavePeriods(ctx context.Context, tx pgx.Tx, in []PeriodInput) error {
	if len(in) != 4 {
		return invalid("There must be exactly 4 periods.")
	}
	current := 0
	for i := range in {
		var err error
		if in[i].Name, err = text(in[i].Name, "Period name", 1, 40); err != nil {
			return err
		}
		if in[i].Months, err = text(in[i].Months, "Period months", 1, 60); err != nil {
			return err
		}
		if in[i].IsCurrent {
			current++
		}
	}
	if current != 1 {
		return invalid("Mark exactly one period as current.")
	}
	if err := lockList(ctx, tx, "roadmap_periods"); err != nil {
		return err
	}
	for i, p := range in {
		if _, err := tx.Exec(ctx, `
			INSERT INTO roadmap_periods (position, name, months, is_current) VALUES ($1, $2, $3, $4)
			ON CONFLICT (position) DO UPDATE
			   SET name = EXCLUDED.name, months = EXCLUDED.months, is_current = EXCLUDED.is_current`,
			i, p.Name, p.Months, p.IsCurrent); err != nil {
			return err
		}
	}
	return nil
}

type ThemeInput struct {
	ID   string `json:"id"` // empty = new
	Name string `json:"name"`
	Note string `json:"note"`
}

// SaveThemes replaces the theme list: new entries are added, the rest are
// renamed and reordered, and themes left out are deleted. Leaving out a
// theme that still has items returns ErrThemeHasItems.
func SaveThemes(ctx context.Context, tx pgx.Tx, in []ThemeInput) error {
	if len(in) == 0 {
		return invalid("Keep at least one theme.")
	}
	if len(in) > maxListLen {
		return invalid(fmt.Sprintf("There can be at most %d themes.", maxListLen))
	}
	if err := lockList(ctx, tx, "roadmap_themes"); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `SELECT id::text FROM roadmap_themes FOR UPDATE`)
	if err != nil {
		return err
	}
	have, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	existing := make(map[string]bool, len(have))
	for _, id := range have {
		existing[id] = true
	}
	seenID := map[string]bool{}
	seenName := map[string]bool{}
	for i := range in {
		t := &in[i]
		t.ID = strings.TrimSpace(t.ID)
		if t.ID != "" {
			if !existing[t.ID] {
				return invalid("A theme in the list no longer exists. Reload and try again.")
			}
			if seenID[t.ID] {
				return invalid("A theme is listed twice.")
			}
			seenID[t.ID] = true
		}
		if t.Name, err = text(t.Name, "Theme name", 1, 60); err != nil {
			return err
		}
		if t.Note, err = text(t.Note, "Theme note", 0, 160); err != nil {
			return err
		}
		key := strings.ToLower(t.Name)
		if seenName[key] {
			return invalid("Each theme needs a different name.")
		}
		seenName[key] = true
	}
	var drop []string
	for _, id := range have {
		if !seenID[id] {
			drop = append(drop, id)
		}
	}
	if len(drop) > 0 {
		var used bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM roadmap_items WHERE theme_id::text = ANY($1))`,
			drop).Scan(&used); err != nil {
			return err
		}
		if used {
			return ErrThemeHasItems
		}
		if _, err := tx.Exec(ctx, `DELETE FROM roadmap_themes WHERE id::text = ANY($1)`, drop); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && pgErr.Code == "23503" { // an item landed meanwhile
				return ErrThemeHasItems
			}
			return err
		}
	}
	for i, t := range in {
		if t.ID == "" {
			_, err = tx.Exec(ctx, `INSERT INTO roadmap_themes (position, name, note) VALUES ($1, $2, $3)`,
				i, t.Name, t.Note)
		} else {
			_, err = tx.Exec(ctx, `UPDATE roadmap_themes SET position = $2, name = $3, note = $4 WHERE id::text = $1`,
				t.ID, i, t.Name, t.Note)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

type ShippedInput struct {
	ID    string `json:"id"` // kept when it names an existing entry
	When  string `json:"when"`
	Title string `json:"title"`
}

// SaveShipped replaces the "Recently shipped" list, in order.
func SaveShipped(ctx context.Context, tx pgx.Tx, in []ShippedInput) error {
	if len(in) > maxListLen {
		return invalid(fmt.Sprintf("There can be at most %d shipped entries.", maxListLen))
	}
	for i := range in {
		var err error
		if in[i].When, err = text(in[i].When, "Shipped date", 1, 40); err != nil {
			return err
		}
		if in[i].Title, err = text(in[i].Title, "Shipped title", 1, 160); err != nil {
			return err
		}
	}
	if err := lockList(ctx, tx, "roadmap_shipped"); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `DELETE FROM roadmap_shipped RETURNING id::text`)
	if err != nil {
		return err
	}
	had, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return err
	}
	keep := make(map[string]bool, len(had))
	for _, id := range had {
		keep[id] = true
	}
	for i, s := range in {
		id := strings.TrimSpace(s.ID)
		if keep[id] {
			delete(keep, id) // each id once
			_, err = tx.Exec(ctx, `INSERT INTO roadmap_shipped (id, when_label, title, position) VALUES ($1::uuid, $2, $3, $4)`,
				id, s.When, s.Title, i)
		} else {
			_, err = tx.Exec(ctx, `INSERT INTO roadmap_shipped (when_label, title, position) VALUES ($1, $2, $3)`,
				s.When, s.Title, i)
		}
		if err != nil {
			return err
		}
	}
	return nil
}
