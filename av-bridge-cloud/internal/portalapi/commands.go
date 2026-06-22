package portalapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/audit"
	"github.com/dloomes/av-bridge-cloud/internal/commands"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/jackc/pgx/v5"
)

// portalCommandWait is how long the portal-submit handler waits for a result
// before returning 202 + command_id so the portal can poll. Tuned for "feels
// synchronous for normal commands but doesn't block forever on a slow device."
const portalCommandWait = 15 * time.Second

// CommandReconnectName is the reserved command name the bridge interprets as
// "Disconnect + Connect this device" rather than dispatching to the adapter's
// command map.
const CommandReconnectName = "_reconnect"

// SubmitCommand — POST /api/v1/devices/{id}/command  body: { name, args }
func (h *Handler) SubmitCommand(w http.ResponseWriter, r *http.Request) {
	deviceID := r.PathValue("id")
	var req struct {
		Name string         `json:"name"`
		Args map[string]any `json:"args,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	var argsJSON []byte
	if len(req.Args) > 0 {
		argsJSON, _ = json.Marshal(req.Args)
	}
	h.submitAndWait(w, r, deviceID, req.Name, argsJSON)
}

// SubmitReconnect — POST /api/v1/devices/{id}/reconnect (no body required)
// Implemented as a special-named command the bridge knows to treat as
// Disconnect + Connect. Lets reconnect ride the same queue + idempotency +
// audit machinery as normal commands.
func (h *Handler) SubmitReconnect(w http.ResponseWriter, r *http.Request) {
	h.submitAndWait(w, r, r.PathValue("id"), CommandReconnectName, nil)
}

// GetCommand — GET /api/v1/commands/{id}
func (h *Handler) GetCommand(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var c commands.Command
	notFound := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		got, err := commands.Get(ctx, tx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		c = got
		return nil
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "command not found")
		return
	}
	writeJSON(w, http.StatusOK, c)
}

// submitAndWait is the shared body of SubmitCommand and SubmitReconnect:
// insert pending row → wait up to portalCommandWait for terminal → respond.
func (h *Handler) submitAndWait(w http.ResponseWriter, r *http.Request, deviceID, name string, args []byte) {
	p, _ := portalauth.From(r.Context())

	var cmdID string
	notFound := false
	ok := h.withTenant(w, r, func(ctx context.Context, tx pgx.Tx) error {
		id, err := commands.Submit(ctx, tx, p.CustomerID, deviceID, name, args, p.Role)
		if errors.Is(err, pgx.ErrNoRows) {
			notFound = true
			return nil
		}
		if err != nil {
			return err
		}
		cmdID = id
		// Audit captures the submission only — the eventual result is added to
		// the command row itself, retrievable via GET /api/v1/commands/{id}.
		// related_target=device so this entry surfaces on the device's
		// activity feed as well as the command-id feed.
		argsMeta := map[string]any{"name": name}
		if len(args) > 0 {
			argsMeta["args"] = json.RawMessage(args)
		}
		return audit.Record(ctx, tx, p.CustomerID, audit.Entry{
			Actor: p.Role, Action: "command.submit",
			TargetKind: "command", TargetID: id,
			RelatedTargetKind: "device", RelatedTargetID: deviceID,
			Metadata: argsMeta,
		})
	})
	if !ok {
		return
	}
	if notFound {
		writeErr(w, http.StatusNotFound, "device not found")
		return
	}

	// Each poll opens a fresh short tx so we don't hold a connection across the wait.
	final, err := commands.WaitForTerminal(r.Context(), func(ctx context.Context) (commands.Command, error) {
		var c commands.Command
		txErr := h.store.WithTenant(ctx, p.CustomerID, func(tx pgx.Tx) error {
			cc, e := commands.Get(ctx, tx, cmdID)
			c = cc
			return e
		})
		return c, txErr
	}, portalCommandWait)
	if err != nil {
		h.log.Error("wait-for-command failed", "command_id", cmdID, "error", err)
		writeErr(w, http.StatusInternalServerError, "internal error")
		return
	}

	switch final.Status {
	case commands.StatusSucceeded:
		// Pass through the result jsonb so the response matches the bridge's
		// existing CommandResponse shape ({raw, parsed, latency_ms}).
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if len(final.Result) > 0 {
			_, _ = w.Write(final.Result)
		} else {
			_, _ = w.Write([]byte("{}"))
		}
	case commands.StatusFailed:
		writeJSON(w, http.StatusInternalServerError, map[string]string{
			"error":      final.Error,
			"command_id": final.ID,
		})
	default:
		// Pending or in_progress at deadline — portal can poll GET /api/v1/commands/{id}.
		writeJSON(w, http.StatusAccepted, map[string]any{
			"command_id": cmdID,
			"status":     string(final.Status),
		})
	}
}
