package portalapi

import (
	"encoding/json"
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
// the customer's own admin can't do that themselves.
type helpdeskCreateCustomerReq struct {
	Name            string `json:"name"`
	EntraTenantID   string `json:"entra_tenant_id,omitempty"`
	InitialAdmin    *struct {
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
		case strings.Contains(msg, "already exists"),
			strings.Contains(msg, "required"),
			strings.Contains(msg, "at least"):
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
		if err := audit.Record(r.Context(), tx, res.CustomerID, audit.Entry{
			Actor: p.ActorLabel(), Action: "customer.create",
			TargetKind: "customer", TargetID: res.CustomerID,
			After: mustJSON(map[string]any{
				"name":            req.Name,
				"entra_tenant_id": req.EntraTenantID,
			}),
		}); err != nil {
			return err
		}
		if res.AdminUserID != "" {
			return audit.Record(r.Context(), tx, res.CustomerID, audit.Entry{
				Actor: p.ActorLabel(), Action: "user.create",
				TargetKind: "user", TargetID: res.AdminUserID,
				After: mustJSON(map[string]any{
					"email": strings.ToLower(strings.TrimSpace(req.InitialAdmin.Email)),
					"role":  "admin",
					"note":  "initial admin — seeded during customer creation",
				}),
			})
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

