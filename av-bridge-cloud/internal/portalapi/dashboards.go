package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

// Dashboards: a named, ordered list of widgets laid out in the portal's
// grid. Personal dashboards belong to one user (owner_user_id); shared
// ones (owner_user_id NULL) are visible to everyone in the organisation
// and changed only with dashboard.share. Widgets fetch their own data
// through the normal endpoints, so the layout holds no customer data.

// dashboardWidgetTypes is the closed set the portal can render; keep in
// step with av-bridge-portal/src/components/dashboard/widgets.tsx.
var dashboardWidgetTypes = map[string]bool{
	"status": true, "alerts": true, "attention": true, "readiness": true,
	"usage": true, "uptime": true, "collectors": true, "warranty": true,
	"firmware": true, "room": true, "device": true, "activity": true,
	// Wallboard widgets.
	"clock": true, "notice": true, "bignumber": true, "inuse": true,
	"buildings": true, "map": true,
}

var dashboardWidgetSizes = map[string]bool{"s": true, "m": true, "l": true, "xl": true}

var dashboardWidgetID = regexp.MustCompile(`^[A-Za-z0-9_-]{1,40}$`)

const (
	maxDashboardWidgets      = 40
	maxDashboardWidgetConfig = 2 << 10
	maxDashboardLayout       = 64 << 10
)

type dashboardWidget struct {
	ID     string          `json:"id"`
	Type   string          `json:"type"`
	Size   string          `json:"size"`
	Config json.RawMessage `json:"config,omitempty"`
}

type dashboardLayout struct {
	Widgets []dashboardWidget `json:"widgets"`
}

// validateDashboardLayout checks a layout from the portal and returns it
// re-encoded, so only known fields are stored.
func validateDashboardLayout(raw json.RawMessage) ([]byte, error) {
	if len(raw) > maxDashboardLayout {
		return nil, errors.New("layout is too large")
	}
	var l dashboardLayout
	if err := json.Unmarshal(raw, &l); err != nil {
		return nil, errors.New("layout must be {\"widgets\": [...]}")
	}
	if l.Widgets == nil {
		l.Widgets = []dashboardWidget{}
	}
	if len(l.Widgets) > maxDashboardWidgets {
		return nil, errors.New("a dashboard can have at most 40 widgets")
	}
	seen := map[string]bool{}
	for i, w := range l.Widgets {
		switch {
		case !dashboardWidgetID.MatchString(w.ID):
			return nil, errors.New("widget id must be 1–40 letters, digits, - or _")
		case seen[w.ID]:
			return nil, errors.New("widget ids must be unique")
		case !dashboardWidgetTypes[w.Type]:
			return nil, errors.New("unknown widget type " + w.Type)
		case !dashboardWidgetSizes[w.Size]:
			return nil, errors.New("widget size must be s, m, l or xl")
		case len(w.Config) > maxDashboardWidgetConfig:
			return nil, errors.New("widget settings are too large")
		}
		seen[w.ID] = true
		if len(w.Config) > 0 {
			var obj map[string]any
			if err := json.Unmarshal(w.Config, &obj); err != nil || obj == nil {
				return nil, errors.New("widget config must be an object")
			}
		} else {
			l.Widgets[i].Config = json.RawMessage(`{}`)
		}
	}
	return json.Marshal(l)
}

type dashboardSummary struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Shared      bool      `json:"shared"`
	CanEdit     bool      `json:"can_edit"`
	WidgetCount int       `json:"widget_count"`
	CreatedBy   string    `json:"created_by"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type dashboardDetail struct {
	dashboardSummary
	Layout json.RawMessage `json:"layout"`
}

// dashboardVisible is the WHERE clause for dashboards a user can see:
// their own, and the organisation's shared ones. $1 is the user id.
const dashboardVisible = `(owner_user_id IS NULL OR owner_user_id::text = $1)`

// displayAllows reports whether a display link (if p is one) may show the
// dashboard: only those it was created for.
func displayAllows(p portalauth.Principal, id string) bool {
	return p.DisplayLinkID == "" || slices.Contains(p.DisplayDashboardIDs, id)
}

func canEditDashboard(p portalauth.Principal, shared bool) bool {
	if shared {
		return p.HasPermission(portalauth.PermDashboardShare)
	}
	return p.UserID != ""
}

// ListDashboards — GET /api/v1/dashboards
//
// The caller's own dashboards and the organisation's shared ones, shared
// first, then by name.
func (h *Handler) ListDashboards(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	out := []dashboardSummary{}
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `
			SELECT id::text, name, owner_user_id IS NULL, jsonb_array_length(layout->'widgets'),
			       created_by, updated_at
			  FROM dashboards
			 WHERE `+dashboardVisible+`
			 ORDER BY owner_user_id IS NOT NULL, lower(name)`, p.UserID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d dashboardSummary
			if err := rows.Scan(&d.ID, &d.Name, &d.Shared, &d.WidgetCount, &d.CreatedBy, &d.UpdatedAt); err != nil {
				return err
			}
			if !displayAllows(p, d.ID) {
				continue
			}
			d.CanEdit = canEditDashboard(p, d.Shared)
			out = append(out, d)
		}
		return rows.Err()
	}) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dashboards": out,
		"can_share":  p.HasPermission(portalauth.PermDashboardShare),
	})
}

// loadDashboard reads one visible dashboard; found is false when it
// doesn't exist or belongs to someone else.
func loadDashboard(ctx context.Context, tx pgx.Tx, p portalauth.Principal, id string) (dashboardDetail, bool, error) {
	var d dashboardDetail
	err := tx.QueryRow(ctx, `
		SELECT id::text, name, owner_user_id IS NULL, jsonb_array_length(layout->'widgets'),
		       created_by, updated_at, layout
		  FROM dashboards
		 WHERE id::text = $2 AND `+dashboardVisible, p.UserID, id).Scan(
		&d.ID, &d.Name, &d.Shared, &d.WidgetCount, &d.CreatedBy, &d.UpdatedAt, &d.Layout)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && !displayAllows(p, d.ID)) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d.CanEdit = canEditDashboard(p, d.Shared)
	return d, true, nil
}

// GetDashboard — GET /api/v1/dashboards/{id}
func (h *Handler) GetDashboard(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	var (
		d     dashboardDetail
		found bool
	)
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		d, found, err = loadDashboard(ctx, tx, p, r.PathValue("id"))
		return err
	}) {
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "dashboard not found")
		return
	}
	writeJSON(w, http.StatusOK, d)
}

type dashboardBody struct {
	Name   *string         `json:"name"`
	Shared *bool           `json:"shared"`
	Layout json.RawMessage `json:"layout"`
}

func cleanDashboardName(n string) (string, error) {
	n = strings.TrimSpace(n)
	if n == "" || len([]rune(n)) > 80 {
		return "", errors.New("name must be 1–80 characters")
	}
	return n, nil
}

// CreateDashboard — POST /api/v1/dashboards  { name, shared?, layout? }
//
// shared: true publishes it to the organisation (dashboard.share).
func (h *Handler) CreateDashboard(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	var body dashboardBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDashboardLayout+4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	if body.Name == nil {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	name, err := cleanDashboardName(*body.Name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	shared := body.Shared != nil && *body.Shared
	if !canEditDashboard(p, shared) {
		writeErr(w, http.StatusForbidden, "you need the Share dashboards permission to publish a shared dashboard")
		return
	}
	layout := []byte(`{"widgets":[]}`)
	if len(body.Layout) > 0 {
		if layout, err = validateDashboardLayout(body.Layout); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	var owner *string
	if !shared {
		owner = &p.UserID
	}
	var d dashboardDetail
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var id string
		if err := tx.QueryRow(ctx, `
			INSERT INTO dashboards (customer_id, owner_user_id, name, layout, created_by)
			VALUES ($1, $2::uuid, $3, $4::jsonb, $5) RETURNING id::text`,
			p.CustomerID, owner, name, string(layout), p.ActorLabel()).Scan(&id); err != nil {
			return err
		}
		var err error
		if d, _, err = loadDashboard(ctx, tx, p, id); err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "dashboard.create", TargetKind: "dashboard", TargetID: id,
			Metadata: map[string]any{"name": name, "shared": shared},
		}))
	}) {
		return
	}
	writeJSON(w, http.StatusCreated, d)
}

// UpdateDashboard — PUT /api/v1/dashboards/{id}  { name?, shared?, layout? }
//
// Your own dashboards, or shared ones with dashboard.share. Changing
// shared moves it between your dashboards and the organisation's, and
// also needs dashboard.share.
func (h *Handler) UpdateDashboard(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	var body dashboardBody
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxDashboardLayout+4096)).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	var (
		name   string
		layout []byte
		err    error
	)
	if body.Name != nil {
		if name, err = cleanDashboardName(*body.Name); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	if len(body.Layout) > 0 {
		if layout, err = validateDashboardLayout(body.Layout); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	var (
		d        dashboardDetail
		status   = http.StatusOK
		errorMsg string
	)
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		cur, found, err := loadDashboard(ctx, tx, p, r.PathValue("id"))
		if err != nil {
			return err
		}
		switch {
		case !found:
			status, errorMsg = http.StatusNotFound, "dashboard not found"
			return nil
		case !cur.CanEdit:
			status, errorMsg = http.StatusForbidden, "you need the Share dashboards permission to change a shared dashboard"
			return nil
		}
		shared := cur.Shared
		if body.Shared != nil && *body.Shared != cur.Shared {
			if !p.HasPermission(portalauth.PermDashboardShare) || p.UserID == "" {
				status, errorMsg = http.StatusForbidden, "you need the Share dashboards permission to share or unshare a dashboard"
				return nil
			}
			shared = *body.Shared
		}
		// Sharing clears the owner; unsharing makes it yours; otherwise the
		// owner stays as it is (nil here).
		var owner *string
		if !shared && cur.Shared {
			owner = &p.UserID
		}
		if _, err := tx.Exec(ctx, `
			UPDATE dashboards
			   SET name          = COALESCE(NULLIF($2, ''), name),
			       layout        = COALESCE($3::jsonb, layout),
			       owner_user_id = CASE WHEN $4 THEN NULL ELSE COALESCE($5::uuid, owner_user_id) END,
			       updated_at    = now()
			 WHERE id::text = $1`,
			cur.ID, name, nullableJSON(layout), shared, owner); err != nil {
			return err
		}
		if d, _, err = loadDashboard(ctx, tx, p, cur.ID); err != nil {
			return err
		}
		meta := map[string]any{"name": d.Name, "shared": d.Shared, "widgets": d.WidgetCount}
		if shared != cur.Shared {
			meta["shared_changed"] = true
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "dashboard.update", TargetKind: "dashboard", TargetID: cur.ID, Metadata: meta,
		}))
	}) {
		return
	}
	if errorMsg != "" {
		writeErr(w, status, errorMsg)
		return
	}
	writeJSON(w, http.StatusOK, d)
}

func nullableJSON(b []byte) any {
	if b == nil {
		return nil
	}
	return string(b)
}

// DeleteDashboard — DELETE /api/v1/dashboards/{id}
func (h *Handler) DeleteDashboard(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	var (
		status   = http.StatusNoContent
		errorMsg string
	)
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		cur, found, err := loadDashboard(ctx, tx, p, r.PathValue("id"))
		if err != nil {
			return err
		}
		switch {
		case !found:
			status, errorMsg = http.StatusNotFound, "dashboard not found"
			return nil
		case !cur.CanEdit:
			status, errorMsg = http.StatusForbidden, "you need the Share dashboards permission to delete a shared dashboard"
			return nil
		}
		if _, err := tx.Exec(ctx, `DELETE FROM dashboards WHERE id::text = $1`, cur.ID); err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "dashboard.delete", TargetKind: "dashboard", TargetID: cur.ID,
			Metadata: map[string]any{"name": cur.Name, "shared": cur.Shared},
		}))
	}) {
		return
	}
	if errorMsg != "" {
		writeErr(w, status, errorMsg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
