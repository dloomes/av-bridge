package portalapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// helpdeskCreateCustomerReq is the wire shape for POST /helpdesk/customers.
// initial_admin is optional but strongly recommended — without it, nobody
// can log into the new tenant until a second admin call creates a user, and
// the customer's own admin can't do that themselves. slug is optional and
// controls the per-customer branded URL <slug>.<env>.involvecloud.com.
type helpdeskCreateCustomerReq struct {
	Name          string `json:"name"`
	EntraTenantID string `json:"entra_tenant_id,omitempty"`
	Slug          string `json:"slug,omitempty"`
	InitialAdmin  *struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		FullName string `json:"full_name,omitempty"`
	} `json:"initial_admin,omitempty"`
}

// HelpdeskCreateCustomer — POST /api/v1/helpdesk/customers
//
// Vendor-only. Creates a customer + default region/location/building/room +
// optional first admin in one transaction. Audits customer.create and
// (if seeded) user.create separately so the trail reads naturally.
func (h *Handler) HelpdeskCreateCustomer(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())

	var req helpdeskCreateCustomerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}

	opts := db.CreateCustomerOptions{
		Name:          req.Name,
		EntraTenantID: strings.TrimSpace(req.EntraTenantID),
		Slug:          req.Slug,
	}
	if req.InitialAdmin != nil {
		opts.InitialAdmin = &db.InitialAdminOptions{
			Email:    strings.TrimSpace(req.InitialAdmin.Email),
			Password: req.InitialAdmin.Password,
			FullName: req.InitialAdmin.FullName,
		}
	}

	res, err := h.store.CreateCustomer(r.Context(), opts)
	if err != nil {
		// db.CreateCustomer returns user-facing messages for validation
		// failures and duplicates; internal errors get logged separately.
		msg := err.Error()
		switch {
		case errors.Is(err, db.ErrSlugReserved):
			writeErr(w, http.StatusConflict, msg)
		case strings.Contains(msg, "already exists"),
			strings.Contains(msg, "required"),
			strings.Contains(msg, "at least"),
			strings.Contains(msg, "slug must"):
			writeErr(w, http.StatusBadRequest, msg)
		default:
			h.log.Error("create customer", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	// Audit in the new tenant's scope. tx.Commit already ran inside the
	// store, so this is a separate small tx that just records the trail.
	// The new customer id becomes the tenant scope.
	_ = h.store.WithTenant(r.Context(), res.CustomerID, func(tx pgx.Tx) error {
		if err := audit.Record(r.Context(), tx, res.CustomerID, stampActor(p, audit.Entry{
			Action: "customer.create",
			TargetKind: "customer", TargetID: res.CustomerID,
			After: mustJSON(map[string]any{
				"name":            req.Name,
				"entra_tenant_id": req.EntraTenantID,
				"slug":            db.NormalizeSlug(req.Slug),
			}),
		})); err != nil {
			return err
		}
		if res.AdminUserID != "" {
			return audit.Record(r.Context(), tx, res.CustomerID, stampActor(p, audit.Entry{
				Action: "user.create",
				TargetKind: "user", TargetID: res.AdminUserID,
				After: mustJSON(map[string]any{
					"email": strings.ToLower(strings.TrimSpace(req.InitialAdmin.Email)),
					"role":  "admin",
					"note":  "initial admin — seeded during customer creation",
				}),
			}))
		}
		return nil
	})

	writeJSON(w, http.StatusOK, map[string]string{
		"customer_id":   res.CustomerID,
		"region_id":     res.RegionID,
		"location_id":   res.LocationID,
		"building_id":   res.BuildingID,
		"room_id":       res.RoomID,
		"admin_user_id": res.AdminUserID,
	})
}

// helpdeskUpdateCustomerReq is the wire shape for PATCH
// /helpdesk/customers/{id}. Pointer per field so an absent JSON key means
// "leave this stored value alone"; an explicit "" clears slug to NULL.
// Name cannot be blank (customers.name is NOT NULL) — an empty-string name
// is rejected with 400.
type helpdeskUpdateCustomerReq struct {
	Name *string `json:"name,omitempty"`
	Slug *string `json:"slug,omitempty"`
}

// HelpdeskUpdateCustomer — PATCH /api/v1/helpdesk/customers/{id}
//
// Vendor-only. Edits mutable fields on an existing customer record. Today
// that's name + URL slug; Entra tenant id + branding stay on their own
// endpoints. Returns 204 on success, 400/409 on validation, 404 if the id
// doesn't exist.
func (h *Handler) HelpdeskUpdateCustomer(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())

	customerID := r.PathValue("id")
	if customerID == "" {
		writeErr(w, http.StatusBadRequest, "customer id required")
		return
	}

	var req helpdeskUpdateCustomerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	if err := h.store.UpdateCustomer(r.Context(), customerID, db.UpdateCustomerOptions{
		Name: req.Name,
		Slug: req.Slug,
	}); err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeErr(w, http.StatusNotFound, "customer not found")
		case errors.Is(err, db.ErrSlugReserved):
			writeErr(w, http.StatusConflict, err.Error())
		case strings.Contains(err.Error(), "already exists"):
			writeErr(w, http.StatusConflict, err.Error())
		case strings.Contains(err.Error(), "slug must"),
			strings.Contains(err.Error(), "cannot be blank"):
			writeErr(w, http.StatusBadRequest, err.Error())
		default:
			h.log.Error("update customer", "customer", customerID, "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
		}
		return
	}

	// Audit inside the target tenant's scope — matches the create-customer
	// pattern so the trail lives with the tenant it describes.
	after := map[string]any{}
	if req.Name != nil {
		after["name"] = strings.TrimSpace(*req.Name)
	}
	if req.Slug != nil {
		after["slug"] = db.NormalizeSlug(*req.Slug)
	}
	_ = h.store.WithTenant(r.Context(), customerID, func(tx pgx.Tx) error {
		return audit.Record(r.Context(), tx, customerID, stampActor(p, audit.Entry{
			Action: "customer.update",
			TargetKind: "customer", TargetID: customerID,
			After: mustJSON(after),
		}))
	})

	w.WriteHeader(http.StatusNoContent)
}

