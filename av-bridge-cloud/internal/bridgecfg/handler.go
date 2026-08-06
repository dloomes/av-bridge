// Package bridgecfg serves the bridge's config-pull endpoints. The cloud is
// the source of truth for per-device protocol config (address, credentials,
// poll rate, commands, subscriptions). The bridge fetches its device set from
// here on a tick and reconciles its local hub against the response.
//
// On first run the bridge can seed its YAML into the cloud via PUT — accepted
// only when the collector has zero devices, so portal-side edits later
// can't be overwritten by a returning bridge.
package bridgecfg

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"github.com/dloomes/av-bridge-cloud/internal/bridgeauth"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/jackc/pgx/v5"
)

// Subscription mirrors the bridge's config.SubscriptionSpec. Duplicated rather
// than imported so the cloud module stays decoupled from the bridge module.
type Subscription struct {
	Tag       string `json:"tag"`
	Attribute string `json:"attribute"`
	Channel   int    `json:"channel"`
	Label     string `json:"label"`
	Rate      int    `json:"rate,omitempty"`
}

// Device is the wire shape returned to the bridge. ID is the reported_id the
// bridge already knows (its YAML identifier); the cloud's UUID is hidden from
// the bridge because the bridge dispatches by reported_id today.
type Device struct {
	ID            string            `json:"id"`
	Name          string            `json:"name,omitempty"`
	Type          string            `json:"type,omitempty"`
	Protocol      string            `json:"protocol,omitempty"`
	Address       string            `json:"address,omitempty"`
	BaudRate     int                `json:"baud_rate,omitempty"`
	Username     string             `json:"username,omitempty"`
	Password     string             `json:"password,omitempty"`
	PollRate     int                `json:"poll_rate_seconds,omitempty"`
	Commands     map[string]string  `json:"commands,omitempty"`
	Tags         map[string]string  `json:"tags,omitempty"`
	Subscriptions []Subscription    `json:"subscriptions,omitempty"`
}

// Handler serves both /bridge/config endpoints. Auth + cipher are shared so
// credentials never live in plaintext beyond the response body.
type Handler struct {
	store  *db.Store
	auth   *bridgeauth.Authenticator
	cipher secrets.Cipher
	log    *slog.Logger
}

func NewHandler(store *db.Store, cipher secrets.Cipher, log *slog.Logger) *Handler {
	return &Handler{
		store:  store,
		auth:   bridgeauth.New(store, cipher, log),
		cipher: cipher,
		log:    log,
	}
}

type getReq struct {
	CollectorID string `json:"collector_id"`
}

type getResp struct {
	Devices []Device `json:"devices"`
}

// Get returns the current device config for the requesting collector, with
// credentials decrypted inline. POSTed (not GETed) because every bridge call
// signs the request body; an empty body wouldn't authenticate.
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	_, col, ok := h.auth.Authenticate(w, r)
	if !ok {
		return
	}

	var devices []Device
	err := h.store.WithTenant(r.Context(), col.CustomerID, func(tx pgx.Tx) error {
		rows, err := tx.Query(r.Context(), `
			SELECT COALESCE(reported_id,''), COALESCE(name,''), COALESCE(type,''),
			       COALESCE(protocol,''), COALESCE(address,''),
			       COALESCE(baud_rate, 0),
			       username_enc, password_enc,
			       COALESCE(poll_rate_seconds, 0),
			       commands, tags, subscriptions
			  FROM devices
			 WHERE collector_id = $1
			 ORDER BY reported_id`,
			col.ID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d := Device{}
			var (
				userEnc, passEnc []byte
				cmds, tags, subs []byte
			)
			if err := rows.Scan(
				&d.ID, &d.Name, &d.Type, &d.Protocol, &d.Address, &d.BaudRate,
				&userEnc, &passEnc, &d.PollRate, &cmds, &tags, &subs,
			); err != nil {
				return err
			}
			if d.ID == "" {
				// reported_id null means the bridge never named this device — skip.
				// Could happen for portal-created devices before they propagate.
				continue
			}
			if len(userEnc) > 0 {
				plain, err := h.cipher.Decrypt(userEnc)
				if err != nil {
					return err
				}
				d.Username = string(plain)
			}
			if len(passEnc) > 0 {
				plain, err := h.cipher.Decrypt(passEnc)
				if err != nil {
					return err
				}
				d.Password = string(plain)
			}
			if len(cmds) > 0 {
				_ = json.Unmarshal(cmds, &d.Commands)
			}
			if len(tags) > 0 {
				_ = json.Unmarshal(tags, &d.Tags)
			}
			if len(subs) > 0 {
				_ = json.Unmarshal(subs, &d.Subscriptions)
			}
			devices = append(devices, d)
		}
		return rows.Err()
	})
	if err != nil {
		h.log.Error("config get failed", "collector", col.ID, "error", err)
		bridgeauth.WriteErr(w, http.StatusInternalServerError, "internal error")
		return
	}
	if devices == nil {
		devices = []Device{}
	}
	// Record the successful pull so the /collectors page can flag stale
	// config sync — best-effort, don't block the response on failure.
	if err := h.store.TouchCollectorConfigPull(r.Context(), col.ID); err != nil {
		h.log.Warn("touch collector config pull failed", "collector", col.ID, "error", err)
	}
	bridgeauth.WriteJSON(w, http.StatusOK, getResp{Devices: devices})
}

type putReq struct {
	CollectorID string   `json:"collector_id"`
	Devices     []Device `json:"devices"`
}

type putResp struct {
	Inserted int `json:"inserted"`
}

// Put seeds the cloud's device set from the bridge's YAML on first run. Only
// accepted when the collector has NEVER been seeded — gated on
// collectors.first_seeded_at, not on current device count. This prevents a
// bridge restart (which resets the in-memory `seeded` flag) from re-seeding
// YAML on top of portal-side deletes.
//
// To re-permit seeding for a collector after intentional reset, an operator
// must clear first_seeded_at explicitly:
//
//	UPDATE collectors SET first_seeded_at = NULL WHERE id = '<uuid>';
func (h *Handler) Put(w http.ResponseWriter, r *http.Request) {
	body, col, ok := h.auth.Authenticate(w, r)
	if !ok {
		return
	}
	var req putReq
	if err := json.Unmarshal(body, &req); err != nil {
		bridgeauth.WriteErr(w, http.StatusBadRequest, "invalid JSON")
		return
	}

	var inserted int
	err := h.store.WithTenant(r.Context(), col.CustomerID, func(tx pgx.Tx) error {
		var alreadySeededAt *string
		if err := tx.QueryRow(r.Context(),
			`SELECT first_seeded_at::text FROM collectors WHERE id = $1`, col.ID,
		).Scan(&alreadySeededAt); err != nil {
			return err
		}
		if alreadySeededAt != nil {
			return errAlreadySeeded
		}

		for _, d := range req.Devices {
			if d.ID == "" {
				return errMissingID
			}
			userEnc, err := encryptOptional(h.cipher, d.Username)
			if err != nil {
				return err
			}
			passEnc, err := encryptOptional(h.cipher, d.Password)
			if err != nil {
				return err
			}
			cmds := jsonOrNil(d.Commands)
			tags := jsonOrNil(d.Tags)
			subs := jsonOrNil(d.Subscriptions)

			if _, err := tx.Exec(r.Context(), `
				INSERT INTO devices (
					customer_id, collector_id, reported_id, name, type, protocol,
					address, baud_rate, username_enc, password_enc,
					poll_rate_seconds, commands, tags, subscriptions
				) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12::jsonb, $13::jsonb, $14::jsonb)`,
				col.CustomerID, col.ID, d.ID, nullIfEmpty(d.Name), nullIfEmpty(d.Type), nullIfEmpty(d.Protocol),
				nullIfEmpty(d.Address), nullIfZero(d.BaudRate), userEnc, passEnc,
				nullIfZero(d.PollRate), cmds, tags, subs,
			); err != nil {
				return err
			}
			inserted++
		}
		// Stamp the collector so any subsequent restart-driven seed attempt
		// will be rejected here regardless of how many devices remain.
		if _, err := tx.Exec(r.Context(),
			`UPDATE collectors SET first_seeded_at = now() WHERE id = $1`, col.ID,
		); err != nil {
			return err
		}
		return nil
	})
	switch err {
	case nil:
		bridgeauth.WriteJSON(w, http.StatusCreated, putResp{Inserted: inserted})
	case errAlreadySeeded:
		bridgeauth.WriteErr(w, http.StatusConflict,
			"collector has already been seeded; edit via portal instead")
	case errMissingID:
		bridgeauth.WriteErr(w, http.StatusBadRequest, "each device must have an id")
	default:
		h.log.Error("config put failed", "collector", col.ID, "error", err)
		bridgeauth.WriteErr(w, http.StatusInternalServerError, "internal error")
	}
}

// Sentinel errors so the WithTenant closure can signal back to the HTTP layer
// without losing the error class to fmt.Errorf wrapping.
var (
	errAlreadySeeded = pgErr("already seeded")
	errMissingID     = pgErr("missing id")
)

type pgErr string

func (e pgErr) Error() string { return string(e) }

func encryptOptional(c secrets.Cipher, plain string) ([]byte, error) {
	if plain == "" {
		return nil, nil
	}
	return c.Encrypt([]byte(plain))
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullIfZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

func jsonOrNil(v any) any {
	switch x := v.(type) {
	case map[string]string:
		if len(x) == 0 {
			return nil
		}
	case []Subscription:
		if len(x) == 0 {
			return nil
		}
	}
	b, err := json.Marshal(v)
	if err != nil || string(b) == "null" {
		return nil
	}
	return string(b)
}
