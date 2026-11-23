package portalapi

// Integrations API: connect a customer's Poly Lens account and list its
// Teams Rooms for the add-device picker. The Lens client secret is encrypted
// with secrets.Cipher before storage and is write-only in every response,
// log line and audit entry.
//
// Every Lens call is scoped to the one account id stored on the customer's
// integration. Once an integration has an account, only vendor (Involve)
// users may change it, and doing so means entering the client secret again.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/integrations/polylens"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
)

const (
	providerPolyLens   = "poly_lens"
	integrationMaxBody = 16 << 10
	lensTimeout        = 40 * time.Second
)

// SetLensBaseURLs points the Poly Lens client at other endpoints (tests).
// Empty values keep the client's production defaults.
func (h *Handler) SetLensBaseURLs(tokenURL, graphqlURL string) *Handler {
	h.lensTokenURL, h.lensGraphQLURL = tokenURL, graphqlURL
	return h
}

func (h *Handler) lensClient(clientID, secret string) *polylens.Client {
	var opts []polylens.Option
	if h.lensTokenURL != "" || h.lensGraphQLURL != "" {
		opts = append(opts, polylens.WithBaseURLs(h.lensTokenURL, h.lensGraphQLURL))
	}
	return polylens.New(clientID, secret, opts...)
}

type integrationView struct {
	Provider      string     `json:"provider"`
	Status        string     `json:"status"`
	AccountName   string     `json:"account_name,omitempty"`
	AllowTagging  bool       `json:"allow_tagging"`
	HasSecret     bool       `json:"has_secret"`
	ClientID      string     `json:"client_id,omitempty"`
	LastSuccessAt *time.Time `json:"last_success_at,omitempty"`
	LastError     string     `json:"last_error,omitempty"`
	LinkedDevices int        `json:"linked_devices"`
}

func viewOf(i db.Integration, linked int) integrationView {
	return integrationView{
		Provider: i.Provider, Status: i.Status, AccountName: i.AccountName, AllowTagging: i.AllowTagging,
		HasSecret: len(i.SecretEnc) > 0, ClientID: i.ClientID, LastSuccessAt: i.LastSuccessAt,
		LastError: i.LastError, LinkedDevices: linked,
	}
}

// ListIntegrations handles GET /api/v1/integrations.
func (h *Handler) ListIntegrations(w http.ResponseWriter, r *http.Request) {
	var rows []db.Integration
	var linked map[string]int
	var snow bool
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if rows, linked, err = db.ListIntegrations(ctx, tx); err != nil {
			return err
		}
		snow, err = db.HasNotificationChannelType(ctx, tx, "servicenow")
		return err
	}) {
		return
	}
	lens := integrationView{Provider: providerPolyLens, Status: "not_connected"}
	for _, i := range rows {
		if i.Provider == providerPolyLens {
			lens = viewOf(i, linked[i.Provider])
		}
	}
	snStatus := "not_connected"
	if snow {
		snStatus = "connected"
	}
	writeJSON(w, http.StatusOK, []integrationView{
		lens,
		{Provider: "servicenow", Status: snStatus},
		{Provider: "logitech_sync", Status: "coming_soon"},
		{Provider: "microsoft_365", Status: "coming_soon"},
	})
}

type lensAccountOut struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type lensCredentials struct {
	ClientID     string `json:"client_id"`
	ClientSecret string `json:"client_secret"`
	AccountID    string `json:"account_id"`
	AllowTagging bool   `json:"allow_tagging"`
}

func decodeLensBody(w http.ResponseWriter, r *http.Request) (lensCredentials, bool) {
	var in lensCredentials
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, integrationMaxBody)).Decode(&in); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid request body")
		return in, false
	}
	in.ClientID = strings.TrimSpace(in.ClientID)
	in.AccountID = strings.TrimSpace(in.AccountID)
	return in, true
}

func (h *Handler) loadIntegration(w http.ResponseWriter, r *http.Request) (*db.Integration, bool) {
	var cur *db.Integration
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		cur, err = db.GetIntegration(ctx, tx, providerPolyLens)
		return err
	})
	return cur, ok
}

// lensFailure writes the response for a failed Lens call and says whether the
// error was a credential rejection. Messages never carry the secret.
func lensFailure(w http.ResponseWriter, err error) {
	var rl *polylens.RateLimitError
	switch {
	case errors.Is(err, polylens.ErrUnauthorized):
		writeErr(w, http.StatusBadRequest, "Poly Lens rejected these credentials")
	case errors.As(err, &rl):
		if secs := int(rl.RetryAfter.Seconds()); secs > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(secs))
		}
		writeErr(w, http.StatusTooManyRequests, "Poly Lens is rate limiting requests; try again shortly")
	default:
		writeErr(w, http.StatusBadGateway, "Could not reach Poly Lens")
	}
}

// credentialsFor returns the client id and secret to call Lens with. A secret
// in the request is used as given. Without one, only a vendor user may reuse
// the stored secret.
func (h *Handler) credentialsFor(p portalauth.Principal, in lensCredentials, cur *db.Integration) (clientID, secret string, status int, msg string) {
	clientID = in.ClientID
	if in.ClientSecret != "" {
		if clientID == "" {
			return "", "", http.StatusBadRequest, "client_id is required"
		}
		return clientID, in.ClientSecret, 0, ""
	}
	if !p.IsVendor || cur == nil || len(cur.SecretEnc) == 0 {
		return "", "", http.StatusBadRequest, "client_secret is required"
	}
	plain, err := h.cipher.Decrypt(cur.SecretEnc)
	if err != nil {
		h.log.Error("integration secret decrypt failed", "error", err)
		return "", "", http.StatusInternalServerError, "internal error"
	}
	if clientID == "" {
		clientID = cur.ClientID
	}
	return clientID, string(plain), 0, ""
}

// TestPolyLens handles POST /api/v1/integrations/poly_lens/test: it checks the
// credentials and lists the accounts they can reach. After an account has been
// stored, customer users only see that account.
func (h *Handler) TestPolyLens(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	in, ok := decodeLensBody(w, r)
	if !ok {
		return
	}
	cur, ok := h.loadIntegration(w, r)
	if !ok {
		return
	}
	clientID, secret, status, msg := h.credentialsFor(p, in, cur)
	if status != 0 {
		writeErr(w, status, msg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), lensTimeout)
	defer cancel()
	accts, err := h.lensClient(clientID, secret).Accounts(ctx)
	if err != nil {
		lensFailure(w, err)
		return
	}
	out := make([]lensAccountOut, 0, len(accts))
	for _, a := range accts {
		if cur != nil && cur.AccountID != "" && !p.IsVendor && a.ID != cur.AccountID {
			continue
		}
		out = append(out, lensAccountOut{ID: a.ID, Name: a.Name})
	}
	writeJSON(w, http.StatusOK, map[string]any{"accounts": out})
}

func integrationSnapshot(i *db.Integration) map[string]any {
	if i == nil {
		return nil
	}
	return map[string]any{
		"provider": i.Provider, "status": i.Status, "client_id": i.ClientID,
		"account_id": i.AccountID, "account_name": i.AccountName, "allow_tagging": i.AllowTagging,
		"has_secret": len(i.SecretEnc) > 0,
	}
}

// PutPolyLens handles PUT /api/v1/integrations/poly_lens (connect or update).
func (h *Handler) PutPolyLens(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	in, ok := decodeLensBody(w, r)
	if !ok {
		return
	}
	cur, ok := h.loadIntegration(w, r)
	if !ok {
		return
	}
	if in.ClientID == "" {
		writeErr(w, http.StatusBadRequest, "client_id is required")
		return
	}
	hasAccount := cur != nil && cur.AccountID != ""
	accountID := in.AccountID
	if hasAccount {
		if accountID == "" {
			accountID = cur.AccountID
		}
		if accountID != cur.AccountID {
			if !p.IsVendor {
				writeErr(w, http.StatusForbidden, "Only Involve support can change the Poly Lens account")
				return
			}
			if in.ClientSecret == "" {
				writeErr(w, http.StatusBadRequest, "client_secret must be entered again to change the account")
				return
			}
		}
	} else if accountID == "" {
		writeErr(w, http.StatusBadRequest, "account_id is required")
		return
	}
	// A secret is needed to connect, to reconnect after a disconnect, and when
	// the client id changes (the stored secret belongs to the old one).
	needSecret := !hasAccount || len(cur.SecretEnc) == 0 || in.ClientID != cur.ClientID
	if in.ClientSecret == "" && needSecret {
		writeErr(w, http.StatusBadRequest, "client_secret is required")
		return
	}

	next := db.Integration{
		Provider: providerPolyLens, ClientID: in.ClientID, AccountID: accountID, AllowTagging: in.AllowTagging,
	}
	if hasAccount {
		next.AccountName = cur.AccountName
	}
	if in.ClientSecret != "" {
		// Verify the credentials and that they can reach the chosen account.
		ctx, cancel := context.WithTimeout(r.Context(), lensTimeout)
		defer cancel()
		accts, err := h.lensClient(in.ClientID, in.ClientSecret).Accounts(ctx)
		if err != nil {
			lensFailure(w, err)
			return
		}
		found := false
		for _, a := range accts {
			if a.ID == accountID {
				found, next.AccountName = true, a.Name
			}
		}
		if !found {
			writeErr(w, http.StatusBadRequest, "These credentials cannot reach that Poly Lens account")
			return
		}
		enc, err := h.cipher.Encrypt([]byte(in.ClientSecret))
		if err != nil {
			h.log.Error("integration secret encrypt failed", "error", err)
			writeErr(w, http.StatusInternalServerError, "internal error")
			return
		}
		next.SecretEnc = enc
	}

	actor := p.Email
	if actor == "" {
		actor = p.ActorLabel()
	}
	var saved *db.Integration
	var linked int
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		before, err := db.GetIntegration(ctx, tx, providerPolyLens)
		if err != nil {
			return err
		}
		id, _, err := db.UpsertIntegration(ctx, tx, p.CustomerID, next, actor)
		if err != nil {
			return err
		}
		if saved, err = db.GetIntegration(ctx, tx, providerPolyLens); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM integration_devices WHERE integration_id = $1`, id).Scan(&linked); err != nil {
			return err
		}
		action := "integration.update"
		if before == nil || before.Status == "disconnected" {
			action = "integration.connect"
		}
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: action, TargetKind: "integration", TargetID: id,
			Before: integrationSnapshot(before), After: integrationSnapshot(saved),
		}))
	}) {
		return
	}
	writeJSON(w, http.StatusOK, viewOf(*saved, linked))
}

// DeletePolyLens handles DELETE /api/v1/integrations/poly_lens: the secret is
// wiped and the integration marked disconnected; linked devices are kept.
func (h *Handler) DeletePolyLens(w http.ResponseWriter, r *http.Request) {
	p, _ := portalauth.From(r.Context())
	actor := p.Email
	if actor == "" {
		actor = p.ActorLabel()
	}
	found := false
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		before, err := db.GetIntegration(ctx, tx, providerPolyLens)
		if err != nil || before == nil {
			return err
		}
		id, ok, err := db.DisconnectIntegration(ctx, tx, providerPolyLens, actor)
		if err != nil || !ok {
			return err
		}
		found = true
		after := integrationSnapshot(before)
		after["status"], after["has_secret"] = "disconnected", false
		return audit.Record(ctx, tx, p.CustomerID, stampActor(p, audit.Entry{
			Action: "integration.disconnect", TargetKind: "integration", TargetID: id,
			Before: integrationSnapshot(before), After: after,
		}))
	}) {
		return
	}
	if !found {
		writeErr(w, http.StatusNotFound, "integration not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type lensDeviceOut struct {
	ExternalID           string  `json:"external_id"`
	Name                 string  `json:"name"`
	Model                string  `json:"model"`
	Serial               string  `json:"serial"`
	MAC                  string  `json:"mac"`
	RoomName             string  `json:"room_name"`
	SiteName             string  `json:"site_name"`
	Connected            bool    `json:"connected"`
	LinkedDeviceID       *string `json:"linked_device_id,omitempty"`
	AlreadyMonitoredAs   *string `json:"already_monitored_as,omitempty"`
	AlreadyMonitoredByID *string `json:"already_monitored_device_id,omitempty"`
}

func normSerial(s string) string { return strings.ToLower(strings.TrimSpace(s)) }

// normMAC keeps only the hex digits, lower-cased, so aa:bb, AA-BB and aabb match.
func normMAC(s string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(s) {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// ListPolyLensDevices handles GET /api/v1/integrations/poly_lens/devices?q=:
// the Teams Rooms in the stored Lens account, marked when already linked or
// already monitored through a Collector (matched by serial number or MAC).
func (h *Handler) ListPolyLensDevices(w http.ResponseWriter, r *http.Request) {
	var cur *db.Integration
	var links map[string]string
	var idents []db.CollectorDeviceIdent
	if !h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if cur, err = db.GetIntegration(ctx, tx, providerPolyLens); err != nil || cur == nil {
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
	if cur == nil || cur.Status == "disconnected" || len(cur.SecretEnc) == 0 || cur.AccountID == "" {
		writeErr(w, http.StatusConflict, "Poly Lens is not connected")
		return
	}
	plain, err := h.cipher.Decrypt(cur.SecretEnc)
	if err != nil {
		h.log.Error("integration secret decrypt failed", "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	client := h.lensClient(cur.ClientID, string(plain))
	ctx, cancel := context.WithTimeout(r.Context(), lensTimeout)
	defer cancel()
	devs, err := client.ListTeamsRooms(ctx, cur.AccountID)
	if err != nil {
		if errors.Is(err, polylens.ErrUnauthorized) {
			h.recordIntegrationError(r, cur.ID, "Poly Lens rejected the stored credentials")
			writeErr(w, http.StatusBadGateway, "Poly Lens rejected the stored credentials; reconnect the integration")
			return
		}
		h.log.Warn("poly lens device list failed", "error", err)
		lensFailure(w, err)
		return
	}
	if cur.Status == "error" {
		h.recordIntegrationOK(r, cur.ID)
	}

	bySerial, byMAC := map[string]db.CollectorDeviceIdent{}, map[string]db.CollectorDeviceIdent{}
	for _, c := range idents {
		if s := normSerial(c.Serial); s != "" {
			bySerial[s] = c
		}
		if m := normMAC(c.MAC); m != "" {
			byMAC[m] = c
		}
	}
	needle := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("q")))
	out := make([]lensDeviceOut, 0, len(devs))
	for _, d := range devs {
		if needle != "" && !strings.Contains(strings.ToLower(strings.Join(
			[]string{d.Name, d.SerialNumber, d.MACAddress, d.RoomName, d.SiteName, d.Model}, "\n")), needle) {
			continue
		}
		row := lensDeviceOut{
			ExternalID: d.ID, Name: d.Name, Model: d.Model, Serial: d.SerialNumber, MAC: d.MACAddress,
			RoomName: d.RoomName, SiteName: d.SiteName, Connected: d.Connected,
		}
		if id, ok := links[d.ID]; ok {
			row.LinkedDeviceID = &id
		}
		var match *db.CollectorDeviceIdent
		if s := normSerial(d.SerialNumber); s != "" {
			if c, ok := bySerial[s]; ok {
				match = &c
			}
		}
		if m := normMAC(d.MACAddress); match == nil && m != "" {
			if c, ok := byMAC[m]; ok {
				match = &c
			}
		}
		if match != nil {
			name, id := match.Name, match.ID
			row.AlreadyMonitoredAs, row.AlreadyMonitoredByID = &name, &id
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	resp := map[string]any{"devices": out}
	if client.Degraded() {
		resp["degraded"] = true
	}
	writeJSON(w, http.StatusOK, resp)
}

func (h *Handler) recordIntegrationError(r *http.Request, id, msg string) {
	p, _ := portalauth.From(r.Context())
	err := h.store.WithTenantScope(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return db.MarkIntegrationError(r.Context(), tx, id, msg)
	})
	if err != nil {
		h.log.Error("mark integration error failed", "error", err)
	}
}

func (h *Handler) recordIntegrationOK(r *http.Request, id string) {
	p, _ := portalauth.From(r.Context())
	err := h.store.WithTenantScope(r.Context(), p.CustomerID, principalScope(p), func(tx pgx.Tx) error {
		return db.MarkIntegrationOK(r.Context(), tx, id)
	})
	if err != nil {
		h.log.Error("mark integration ok failed", "error", err)
	}
}
