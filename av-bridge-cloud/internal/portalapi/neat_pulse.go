package portalapi

// Neat Pulse integration API (read-only): connect a customer's Pulse
// organisation with an API key, list its room devices for the add-device
// picker, and create devices linked to them. The API key is encrypted with
// secrets.Cipher before storage and is write-only in every response, log
// line and audit entry. The organisation id is stored as the integration's
// account_id; once stored, only vendor (Involve) users may change it, and
// doing so means entering the API key again (the Poly Lens rule).

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/integrations/neatpulse"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

const (
	providerNeatPulse  = "neat_pulse"
	sourceNeatPulse    = "neat_pulse"
	deviceTypeZoomRoom = "zoom_room"
	neatTimeout        = 40 * time.Second
	// neatPickerDetailTTL is how long the picker reuses an endpoint's details.
	neatPickerDetailTTL = 5 * time.Minute
	// neatPickerMaxDetails caps details calls per picker request.
	neatPickerMaxDetails = 50
	neatPickerParallel   = 4
)

// SetNeatBaseURL points the Neat Pulse client at another base URL (tests).
func (h *Handler) SetNeatBaseURL(u string) *Handler {
	h.neatBaseURL = u
	return h
}

func (h *Handler) neatClient(apiKey string) *neatpulse.Client {
	var opts []neatpulse.Option
	if h.neatBaseURL != "" {
		opts = append(opts, neatpulse.WithBaseURL(h.neatBaseURL))
	}
	return neatpulse.New(apiKey, opts...)
}

type neatCredentials struct {
	OrgID  string `json:"org_id"`
	APIKey string `json:"api_key"`
}

func decodeNeatBody(w http.ResponseWriter, r *http.Request) (neatCredentials, bool) {
	var in neatCredentials
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, integrationMaxBody)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return in, false
	}
	in.OrgID = strings.TrimSpace(in.OrgID)
	in.APIKey = strings.TrimSpace(in.APIKey)
	return in, true
}

// neatFailure writes the response for a failed Pulse call: 400 for a
// rejected key or unknown organisation, 429 when rate limited, 502
// otherwise (the error is logged; it never carries the key).
func (h *Handler) neatFailure(w http.ResponseWriter, err error) {
	var rl *neatpulse.RateLimitError
	switch {
	case errors.Is(err, neatpulse.ErrUnauthorized):
		writeErr(w, http.StatusBadRequest, "Neat Pulse rejected this API key, or it can't read that organisation")
	case errors.Is(err, neatpulse.ErrNotFound):
		writeErr(w, http.StatusBadRequest, "Neat Pulse doesn't know that organisation ID")
	case errors.As(err, &rl):
		if secs := int(rl.RetryAfter.Seconds()); secs > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
		writeErr(w, http.StatusTooManyRequests, "Neat Pulse is rate limiting requests; try again shortly")
	default:
		h.log.Warn("neat pulse call failed", "error", err)
		writeErr(w, http.StatusBadGateway, "Could not reach Neat Pulse")
	}
}

// neatKeyFor returns the organisation and API key to call Pulse with. A key
// in the request is used as given. Without one, only a vendor user may reuse
// the stored key, and only for the stored organisation. A customer user may
// not use another organisation once one is stored.
func (h *Handler) neatKeyFor(p portalauth.Principal, in neatCredentials, cur *db.Integration) (org, key string, status int, msg string) {
	org = in.OrgID
	if org == "" && cur != nil {
		org = cur.AccountID
	}
	if org == "" {
		return "", "", http.StatusBadRequest, "org_id is required"
	}
	if cur != nil && cur.AccountID != "" && org != cur.AccountID && !p.IsVendor {
		return "", "", http.StatusForbidden, "Only Involve support can change the Neat Pulse organisation"
	}
	if in.APIKey != "" {
		return org, in.APIKey, 0, ""
	}
	if !p.IsVendor || cur == nil || len(cur.SecretEnc) == 0 || org != cur.AccountID {
		return "", "", http.StatusBadRequest, "api_key is required"
	}
	plain, err := h.cipher.Decrypt(cur.SecretEnc)
	if err != nil {
		h.log.Error("integration secret decrypt failed", "error", err)
		return "", "", http.StatusInternalServerError, "internal error"
	}
	return org, string(plain), 0, ""
}

// TestNeatPulse handles POST /api/v1/integrations/neat_pulse/test: it checks
// the key can list the organisation's devices and returns how many there are.
func (h *Handler) TestNeatPulse(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	in, ok := decodeNeatBody(w, r)
	if !ok {
		return
	}
	cur, ok := h.loadIntegrationFor(w, r, providerNeatPulse)
	if !ok {
		return
	}
	org, key, status, msg := h.neatKeyFor(p, in, cur)
	if status != 0 {
		writeErr(w, status, msg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), neatTimeout)
	defer cancel()
	eps, err := h.neatClient(key).ListEndpoints(ctx, org)
	if err != nil {
		h.neatFailure(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"org_id": org, "device_count": len(eps)})
}

// PutNeatPulse handles PUT /api/v1/integrations/neat_pulse (connect or update).
func (h *Handler) PutNeatPulse(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	in, ok := decodeNeatBody(w, r)
	if !ok {
		return
	}
	cur, ok := h.loadIntegrationFor(w, r, providerNeatPulse)
	if !ok {
		return
	}
	hasOrg := cur != nil && cur.AccountID != ""
	org := in.OrgID
	if hasOrg {
		if org == "" {
			org = cur.AccountID
		}
		if org != cur.AccountID {
			if !p.IsVendor {
				writeErr(w, http.StatusForbidden, "Only Involve support can change the Neat Pulse organisation")
				return
			}
			if in.APIKey == "" {
				writeErr(w, http.StatusBadRequest, "api_key must be entered again to change the organisation")
				return
			}
		}
	} else if org == "" {
		writeErr(w, http.StatusBadRequest, "org_id is required")
		return
	}
	// A key is needed to connect and to reconnect after a disconnect.
	if in.APIKey == "" && (!hasOrg || len(cur.SecretEnc) == 0) {
		writeErr(w, http.StatusBadRequest, "api_key is required")
		return
	}
	next := db.Integration{Provider: providerNeatPulse, AccountID: org}
	if hasOrg && org == cur.AccountID {
		next.AccountName = cur.AccountName
	}
	if in.APIKey != "" {
		// Verify the key can read the organisation.
		ctx, cancel := context.WithTimeout(r.Context(), neatTimeout)
		defer cancel()
		if _, err := h.neatClient(in.APIKey).ListEndpoints(ctx, org); err != nil {
			h.neatFailure(w, err)
			return
		}
		enc, err := h.cipher.Encrypt([]byte(in.APIKey))
		if err != nil {
			h.log.Error("integration secret encrypt failed", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		next.SecretEnc = enc
	}
	h.saveIntegration(w, r, cur, next)
}

// DeleteNeatPulse handles DELETE /api/v1/integrations/neat_pulse: the key is
// wiped and the integration marked disconnected; linked devices are kept.
func (h *Handler) DeleteNeatPulse(w http.ResponseWriter, r *http.Request) {
	h.disconnectIntegration(w, r, providerNeatPulse)
}

// neatDetailCache keeps endpoint details for the picker, so typing in its
// search box does not re-read every device from Pulse.
type neatDetailCache struct {
	mu sync.Mutex
	m  map[string]neatCachedDetail
}

type neatCachedDetail struct {
	ep neatpulse.Endpoint
	at time.Time
}

func (c *neatDetailCache) get(key string) (neatpulse.Endpoint, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	d, ok := c.m[key]
	if !ok || time.Since(d.at) > neatPickerDetailTTL {
		return neatpulse.Endpoint{}, false
	}
	return d.ep, true
}

func (c *neatDetailCache) put(key string, ep neatpulse.Endpoint) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil || len(c.m) > 5000 {
		c.m = map[string]neatCachedDetail{}
	}
	c.m[key] = neatCachedDetail{ep: ep, at: time.Now()}
}

type neatDeviceOut struct {
	ExternalID           string  `json:"external_id"`
	Name                 string  `json:"name"`
	Model                string  `json:"model"`
	Serial               string  `json:"serial"`
	RoomName             string  `json:"room_name"`
	LocationName         string  `json:"location_name"`
	RegionName           string  `json:"region_name"`
	Connected            bool    `json:"connected"`
	PrimaryMode          string  `json:"primary_mode"`
	SuggestedType        string  `json:"suggested_type"`
	LinkedDeviceID       *string `json:"linked_device_id,omitempty"`
	AlreadyMonitoredAs   *string `json:"already_monitored_as,omitempty"`
	AlreadyMonitoredByID *string `json:"already_monitored_device_id,omitempty"`
}

// neatName is the name a Neat device gets by default: its Pulse room, else
// its model and serial.
func neatName(e neatpulse.BasicEndpoint) string {
	if n := strings.TrimSpace(e.RoomName); n != "" {
		return n
	}
	return strings.TrimSpace(e.Model + " " + e.Serial)
}

// ListNeatPulseDevices handles GET /api/v1/integrations/neat_pulse/devices?q=:
// the room main units in the stored organisation (a Pad paired with another
// device is left out: it shows as that device's peripheral), marked when
// already linked or already monitored through a Collector (serial match).
func (h *Handler) ListNeatPulseDevices(w http.ResponseWriter, r *http.Request) {
	var cur *db.Integration
	var links map[string]string
	var idents []db.CollectorDeviceIdent
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if cur, err = db.GetIntegration(ctx, tx, providerNeatPulse); err != nil || cur == nil {
			return err
		}
		if links, err = db.IntegrationLinks(ctx, tx, cur.ID); err != nil {
			return err
		}
		idents, err = db.CollectorDeviceIdents(ctx, tx)
		return err
	}) {
		return
	}
	if !integrationConnected(cur) {
		writeErr(w, http.StatusConflict, "Neat Pulse is not connected")
		return
	}
	plain, err := h.cipher.Decrypt(cur.SecretEnc)
	if err != nil {
		h.log.Error("integration secret decrypt failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	client := h.neatClient(string(plain))
	ctx, cancel := context.WithTimeout(r.Context(), neatTimeout)
	defer cancel()
	eps, err := client.ListEndpoints(ctx, cur.AccountID)
	if err != nil {
		if errors.Is(err, neatpulse.ErrUnauthorized) {
			h.recordIntegrationError(r, cur.ID, "Neat Pulse rejected the API key")
			writeErr(w, http.StatusBadGateway, "Neat Pulse rejected the stored API key; reconnect the integration")
			return
		}
		h.neatFailure(w, err)
		return
	}
	if cur.Status == "error" {
		h.recordIntegrationOK(r, cur.ID)
	}

	needle := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	var cands []neatpulse.BasicEndpoint
	for _, e := range eps {
		if needle != "" && !strings.Contains(strings.ToLower(strings.Join(
			[]string{neatName(e), e.Model, e.Serial, e.RoomName, e.LocationName, e.RegionName}, "\n")), needle) {
			continue
		}
		cands = append(cands, e)
	}
	details := h.neatPickerDetails(ctx, client, cur, cands)

	bySerial := map[string]neatpulse.BasicEndpoint{}
	for _, e := range eps {
		if s := normSerial(e.Serial); s != "" {
			bySerial[s] = e
		}
	}
	// Serials some main unit names as its pair.
	pairedByMain := map[string]bool{}
	for _, e := range cands {
		if d, ok := details[e.ID]; ok && !neatpulse.IsCompanionModel(e.Model) {
			if s := normSerial(d.PairingSerial); s != "" {
				pairedByMain[s] = true
			}
		}
	}
	// Rooms that have a main unit, for Pads whose details could not be read.
	roomHasMain := map[int]bool{}
	for _, e := range eps {
		if e.RoomID != 0 && !neatpulse.IsCompanionModel(e.Model) {
			roomHasMain[e.RoomID] = true
		}
	}

	bySerialIdent := map[string]db.CollectorDeviceIdent{}
	for _, c := range idents {
		if s := normSerial(c.Serial); s != "" {
			bySerialIdent[s] = c
		}
	}
	out := make([]neatDeviceOut, 0, len(cands))
	for _, e := range cands {
		d, hasDet := details[e.ID]
		if neatpulse.IsCompanionModel(e.Model) {
			paired := pairedByMain[normSerial(e.Serial)]
			if hasDet {
				if _, ok := bySerial[normSerial(d.PairingSerial)]; ok {
					paired = true
				}
			} else if roomHasMain[e.RoomID] {
				paired = true
			}
			if paired {
				continue // a Pad: it shows as its main unit's peripheral
			}
		}
		row := neatDeviceOut{
			ExternalID: e.ID, Name: neatName(e), Model: e.Model, Serial: e.Serial, RoomName: e.RoomName,
			LocationName: e.LocationName, RegionName: e.RegionName, Connected: e.Connected,
		}
		if hasDet {
			row.PrimaryMode = d.PrimaryMode
			row.SuggestedType = neatpulse.RoomType(d.PrimaryMode)
		}
		if id, ok := links[e.ID]; ok {
			row.LinkedDeviceID = &id
		}
		if s := normSerial(e.Serial); s != "" {
			if c, ok := bySerialIdent[s]; ok {
				name, id := c.Name, c.ID
				row.AlreadyMonitoredAs, row.AlreadyMonitoredByID = &name, &id
			}
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	writeJSON(w, http.StatusOK, map[string]any{"devices": out})
}

// neatPickerDetails reads the candidates' details, a few at a time, from the
// cache where it can. A failure or a rate limit leaves the rest without
// details (their primary mode then shows as unknown).
func (h *Handler) neatPickerDetails(ctx context.Context, client *neatpulse.Client, cur *db.Integration, cands []neatpulse.BasicEndpoint) map[string]neatpulse.Endpoint {
	out := map[string]neatpulse.Endpoint{}
	var mu sync.Mutex
	var todo []string
	for _, e := range cands {
		if ep, ok := h.neatDetails.get(cur.ID + "|" + cur.AccountID + "|" + e.ID); ok {
			out[e.ID] = ep
		} else if len(todo) < neatPickerMaxDetails {
			todo = append(todo, e.ID)
		}
	}
	var stop bool
	sem := make(chan struct{}, neatPickerParallel)
	var wg sync.WaitGroup
	for _, id := range todo {
		mu.Lock()
		halt := stop
		mu.Unlock()
		if halt || ctx.Err() != nil {
			break
		}
		sem <- struct{}{}
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			defer func() { <-sem }()
			ep, err := client.Endpoint(ctx, cur.AccountID, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if errors.Is(err, neatpulse.ErrRateLimited) || errors.Is(err, neatpulse.ErrUnauthorized) {
					stop = true
				}
				return
			}
			out[id] = ep
			h.neatDetails.put(cur.ID+"|"+cur.AccountID+"|"+id, ep)
		}(id)
	}
	wg.Wait()
	return out
}

// createNeatDevice links a Neat Pulse room device (no Collector, no
// credentials). Its type follows the Pulse primary mode: Teams → teams_room,
// Zoom → zoom_room. When Pulse doesn't say, the request's type is used.
func (h *Handler) createNeatDevice(w http.ResponseWriter, r *http.Request, req createDeviceReq) {
	req.ExternalID = strings.TrimSpace(req.ExternalID)
	switch {
	case req.Type != "" && req.Type != deviceTypeTeamsRoom && req.Type != deviceTypeZoomRoom:
		writeErr(w, http.StatusBadRequest, "type must be teams_room or zoom_room for a Neat Pulse device")
		return
	case req.Platform != "" && req.Platform != "android":
		writeErr(w, http.StatusBadRequest, "platform must be android for a Neat Pulse device")
		return
	case req.ExternalID == "":
		writeErr(w, http.StatusBadRequest, "external_id is required")
		return
	case req.CollectorID != "" || req.ReportedID != "" || req.Protocol != "":
		writeErr(w, http.StatusBadRequest, "collector_id, reported_id and protocol are not used for a Neat Pulse device")
		return
	case req.Address != "" || req.Username != "" || req.Password != "":
		writeErr(w, http.StatusBadRequest, "address and credentials are not used for a Neat Pulse device")
		return
	}
	cur, ok := h.loadIntegrationFor(w, r, providerNeatPulse)
	if !ok {
		return
	}
	if !integrationConnected(cur) {
		writeErr(w, http.StatusConflict, "Connect Neat Pulse in Integrations first")
		return
	}
	plain, err := h.cipher.Decrypt(cur.SecretEnc)
	if err != nil {
		h.log.Error("integration secret decrypt failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), neatTimeout)
	defer cancel()
	ep, err := h.neatClient(string(plain)).Endpoint(ctx, cur.AccountID, req.ExternalID)
	if errors.Is(err, neatpulse.ErrNotFound) {
		writeErr(w, http.StatusBadRequest, "external_id not found in the connected Neat Pulse organisation")
		return
	}
	if err != nil {
		h.neatFailure(w, err)
		return
	}
	if neatpulse.IsCompanionModel(ep.Model) && strings.TrimSpace(ep.PairingSerial) != "" {
		writeErr(w, http.StatusBadRequest, "That is a paired Neat Pad; add the room's main unit instead (its Pad shows as a peripheral)")
		return
	}
	typ := neatpulse.RoomType(ep.PrimaryMode)
	switch {
	case typ == "" && req.Type == "":
		writeErr(w, http.StatusBadRequest, "Neat Pulse doesn't say whether this is a Teams or Zoom room; choose the type")
		return
	case typ == "":
		typ = req.Type
	case req.Type != "" && req.Type != typ:
		writeErr(w, http.StatusBadRequest, "This device's Neat Pulse primary mode is "+ep.PrimaryMode+", so its type is "+typ)
		return
	}
	name := strings.TrimSpace(ep.RoomName)
	if name == "" {
		name = strings.TrimSpace(ep.Model + " " + ep.Serial)
	}
	id, _, ok := h.insertLinkedDevice(w, r, cur, req, linkSpec{
		source: sourceNeatPulse, typ: typ, platform: "android", vendor: "Neat Pulse",
		info: linkedInfo{name: name, serial: ep.Serial, mac: ep.MAC(), model: ep.Model},
		meta: map[string]any{"type": typ, "primary_mode": ep.PrimaryMode},
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "type": typ})
}
