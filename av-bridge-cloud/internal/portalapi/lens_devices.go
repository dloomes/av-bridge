package portalapi

// Create and delete Teams Rooms managed through Poly Lens (no Collector).
// Every Lens call here is scoped to the integration's stored account: the
// create flow tags only an external id it has just read from that account,
// and delete removes the tag only from the linked external id.

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

const (
	sourceCollector = "collector"
	sourcePolyLens  = "poly_lens"
	lensTagPrefix   = "marcus:"
)

var lensPlatforms = map[string]bool{"windows": true, "android": true}

func integrationConnected(i *db.Integration) bool {
	return i != nil && i.Status != "disconnected" && len(i.SecretEnc) > 0 && i.AccountID != ""
}

// validateLensCreate returns a 400 message for a request that is not a valid
// Lens-managed device, or "".
func validateLensCreate(req createDeviceReq) string {
	switch {
	case req.Type != "teams_room":
		return "type must be teams_room for a Poly Lens device"
	case !lensPlatforms[req.Platform]:
		return "platform must be windows or android"
	case strings.TrimSpace(req.ExternalID) == "":
		return "external_id is required"
	case req.CollectorID != "" || req.ReportedID != "" || req.Protocol != "":
		return "collector_id, reported_id and protocol are not used for a Poly Lens device"
	case req.Address != "" || req.Username != "" || req.Password != "":
		return "address and credentials are not used for a Poly Lens device"
	}
	return ""
}

func (h *Handler) createLensDevice(w http.ResponseWriter, r *http.Request, req createDeviceReq) {
	if msg := validateLensCreate(req); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}
	p, _ := portalauth.From(r.Context())
	req.ExternalID = strings.TrimSpace(req.ExternalID)

	cur, ok := h.loadIntegration(w, r)
	if !ok {
		return
	}
	if !integrationConnected(cur) {
		writeErr(w, http.StatusConflict, "Connect Poly Lens in Integrations first")
		return
	}
	plain, err := h.cipher.Decrypt(cur.SecretEnc)
	if err != nil {
		h.log.Error("integration secret decrypt failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	client := h.lensClient(cur.ClientID, string(plain))
	lctx, cancel := context.WithTimeout(r.Context(), lensTimeout)
	defer cancel()
	found, err := client.Devices(lctx, cur.AccountID, []string{req.ExternalID})
	if err != nil {
		h.lensFailure(w, err)
		return
	}
	type lensInfo struct{ name, serial, mac, model string }
	var lens *lensInfo
	for _, d := range found {
		if d.ID == req.ExternalID {
			lens = &lensInfo{d.Name, d.SerialNumber, d.MACAddress, d.Model}
		}
	}
	if lens == nil {
		writeErr(w, http.StatusBadRequest, "external_id not found in the connected Poly Lens account")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = lens.name
	}

	var (
		id        string
		roomBad   bool
		duplicate bool
		assetBad  bool
		assetErr  error
		stale     bool
		tagging   bool
	)
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		// Lock the integration row: serialises concurrent links and proves
		// the account and credentials are the ones the lookup above used.
		locked, err := db.GetIntegrationForUpdate(ctx, tx, providerPolyLens)
		if err != nil {
			return err
		}
		if integrationChanged(cur, locked) {
			stale = true
			return nil
		}
		tagging = locked.AllowTagging
		if req.RoomID != "" {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM rooms WHERE id = $1)`, req.RoomID).Scan(&exists); err != nil {
				return err
			}
			if !exists {
				roomBad = true
				return nil
			}
		}
		if err := tx.QueryRow(ctx, `
			SELECT EXISTS (SELECT 1 FROM integration_devices l JOIN devices d ON d.id = l.device_id
			                WHERE l.integration_id = $1 AND l.external_id = $2 AND d.deleted_at IS NULL)`,
			locked.ID, req.ExternalID).Scan(&duplicate); err != nil {
			return err
		}
		if duplicate {
			return nil
		}
		var roomParam any
		if req.RoomID != "" {
			roomParam = req.RoomID
		}
		assetParam, bad, aerr, err := resolveDeviceAsset(ctx, tx, p.CustomerID, roomParam, "teams_room", name, req.AssetID, req.Asset)
		if err != nil {
			return err
		}
		if bad || aerr != nil {
			assetBad, assetErr = bad, aerr
			return nil
		}
		if err := tx.QueryRow(ctx, `
			INSERT INTO devices (customer_id, source, platform, room_id, name, type, serial_number, mac_address, tags, asset_id)
			VALUES ($1, 'poly_lens', $2, $3, NULLIF($4,''), 'teams_room', NULLIF($5,''), NULLIF($6,''), $7::jsonb, $8)
			RETURNING id::text`,
			p.CustomerID, req.Platform, roomParam, name, lens.serial, lens.mac, jsonOrNil(req.Tags), assetParam).Scan(&id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO integration_devices (device_id, customer_id, integration_id, external_id, external_name,
			                                 serial_number, mac_address, model)
			VALUES ($1, $2, $3, $4, NULLIF($5,''), NULLIF($6,''), NULLIF($7,''), NULLIF($8,''))`,
			id, p.CustomerID, locked.ID, req.ExternalID, lens.name, lens.serial, lens.mac, lens.model); err != nil {
			return err
		}
		after, err := audit.SnapshotDevice(ctx, tx, id)
		if err != nil {
			return err
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "device.create", TargetKind: "device", TargetID: id, After: after,
			Metadata: map[string]any{"source": sourcePolyLens, "platform": req.Platform, "external_id": req.ExternalID},
		}))
	}) {
		return
	}
	switch {
	case stale:
		writeErr(w, http.StatusConflict, "The integration changed; reload and try again")
		return
	case roomBad:
		writeErr(w, http.StatusBadRequest, "room_id not found in this customer")
		return
	case assetBad:
		writeErr(w, http.StatusBadRequest, "asset_id not found in this customer")
		return
	case assetErr != nil:
		writeErr(w, http.StatusBadRequest, assetErr.Error())
		return
	case duplicate:
		writeErr(w, http.StatusConflict, "That Poly Lens device is already linked")
		return
	}

	// Lens rejects a freshly created tag for several seconds, so tagging is
	// left to the poller, which retries. A tagging failure never blocks linking.
	tagState := "none"
	if tagging {
		tagState = "pending"
		if !h.markLensTagPending(r, id) && !h.markLensTagPending(r, id) {
			tagState = "none"
		}
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "tag_state": tagState})
}

// markLensTagPending asks the poller to tag the device. It reports whether the
// request was stored.
func (h *Handler) markLensTagPending(r *http.Request, deviceID string) bool {
	p, _ := portalauth.From(r.Context())
	err := h.store.WithTenantScope(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		_, err := tx.Exec(r.Context(),
			`UPDATE integration_devices SET tag_state = 'pending', tag_error = NULL, tag_requested_at = now()
			  WHERE device_id = $1`, deviceID)
		return err
	})
	if err != nil {
		h.log.Error("record lens tag request failed", "device_id", deviceID, "error", err)
		return false
	}
	return true
}

// releaseLensTag runs before a device is deleted. For a tagged Lens device it
// removes the tag from the linked external id when the integration is still
// connected, and returns the tag state to record ("" for other devices).
func (h *Handler) releaseLensTag(r *http.Request, deviceID string) string {
	p, _ := portalauth.From(r.Context())
	var link *db.IntegrationLink
	var integ *db.Integration
	err := h.store.WithTenantScope(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		var err error
		if link, err = db.GetIntegrationLink(r.Context(), tx, deviceID); err != nil || link == nil {
			return err
		}
		integ, err = db.GetIntegration(r.Context(), tx, providerPolyLens)
		return err
	})
	if err != nil {
		h.log.Error("load lens link failed", "device_id", deviceID, "error", err)
		return ""
	}
	if link == nil {
		return ""
	}
	// A device with no tag in Lens (never created, or cleaned up after a
	// failed attempt) has nothing to release.
	hasTag := link.TagState == "tagged" || ((link.TagState == "pending" || link.TagState == "failed") && link.LensTagID != "")
	if !hasTag {
		return ""
	}
	if !integrationConnected(integ) || integ.ID != link.IntegrationID {
		return "left_behind"
	}
	plain, err := h.cipher.Decrypt(integ.SecretEnc)
	if err != nil {
		h.log.Error("integration secret decrypt failed", "error", err)
		return "left_behind"
	}
	ctx, cancel := context.WithTimeout(r.Context(), lensTimeout)
	defer cancel()
	client := h.lensClient(integ.ClientID, string(plain))
	var rerr error
	switch {
	case link.LensTagID != "" && link.TagState != "tagged":
		// Never assigned to the device, so there is nothing to unassign:
		// delete the tag itself (unassigning first could fail and skip it).
		rerr = client.DeleteTag(ctx, link.LensTagID)
	case link.LensTagID != "":
		rerr = client.RemoveTagByID(ctx, link.LensTagID, link.ExternalID)
	default:
		rerr = client.RemoveTag(ctx, integ.AccountID, link.ExternalID, lensTagPrefix+deviceID)
	}
	if link.TagState != "tagged" {
		// Never assigned: a failed cleanup leaves nothing on the device.
		if rerr != nil {
			h.log.Warn("poly lens tag cleanup failed", "device_id", deviceID, "error", rerr)
		}
		return "none"
	}
	if rerr != nil {
		h.log.Warn("poly lens tag removal failed", "device_id", deviceID, "error", rerr)
		return "left_behind"
	}
	return "none"
}
