package api

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/portalapi"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/wsfanout"
)

// PortalRoutes bundles the dependencies needed to mount the portal-facing read
// API. portal must be non-nil; resolver gates every /api/v1 route. WSHub is
// optional — when set, GET /ws/events is exposed for live device-event
// fan-out, gated by the same Resolver as the rest of /api/v1.
type PortalRoutes struct {
	Resolver portalauth.Resolver
	Portal   *portalapi.Handler
	WSHub    *wsfanout.Hub
}

// BridgeCommandRoutes are the cloud-side endpoints the bridge polls for
// pending commands and posts results to. Both auth via HMAC over the body
// (same scheme as /ingest) — no portal token required.
type BridgeCommandRoutes struct {
	Poll   http.HandlerFunc
	Result http.HandlerFunc
}

// BridgeConfigRoutes wires the config-pull endpoints. GetConfig returns the
// device set for the requesting collector; PutConfig seeds it from the
// bridge's YAML on first run (refused once the collector already has devices,
// so portal edits aren't overwritten). HMAC-authenticated.
type BridgeConfigRoutes struct {
	GetConfig http.HandlerFunc
	PutConfig http.HandlerFunc
}

// NewServer wires the routes. Go 1.22 method-aware patterns keep us dependency-free.
// adminCollectors may be nil — registration endpoints are off when no ADMIN_API_TOKEN
// is configured. portal may be nil — read API is off when no POC_PORTAL_TOKEN is set.
// bridgeCommands wires POST /bridge/poll and POST /bridge/commands/{id}/result.
func NewServer(addr string, ingest, adminCollectors http.Handler, portal *PortalRoutes, bridgeCommands BridgeCommandRoutes, bridgeConfig BridgeConfigRoutes, log *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("POST /ingest", ingest)
	if adminCollectors != nil {
		mux.Handle("POST /admin/collectors", adminCollectors)
	}

	if portal != nil {
		// wrap: any authenticated portal user (read endpoints + listings).
		// wrapAdmin: principal must hold the "admin" role (writes + placement).
		wrap := func(h http.HandlerFunc) http.Handler {
			return portalauth.Middleware(portal.Resolver, h)
		}
		adminOnly := portalauth.RequireRole("admin")
		wrapAdmin := func(h http.HandlerFunc) http.Handler {
			return portalauth.Middleware(portal.Resolver, adminOnly(h))
		}

		// Login is intentionally OUTSIDE the auth middleware — a fresh user
		// has no token yet. Logout accepts a token but tolerates an
		// unauthenticated caller (idempotent 204) so it can be a "clear my
		// session server-side" call even after the token expired.
		mux.Handle("POST /api/v1/auth/login", http.HandlerFunc(portal.Portal.Login))
		mux.Handle("POST /api/v1/auth/logout", http.HandlerFunc(portal.Portal.Logout))
		mux.Handle("POST /api/v1/auth/change-password", wrap(portal.Portal.ChangePassword))

		// Read — any authenticated user.
		mux.Handle("GET /api/v1/whoami", wrap(portal.Portal.Whoami))
		mux.Handle("GET /api/v1/helpdesk/customers",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.HelpdeskListCustomers))))
		mux.Handle("GET /api/v1/helpdesk/overview",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.HelpdeskOverview))))
		mux.Handle("GET /api/v1/status", wrap(portal.Portal.Status))
		mux.Handle("GET /api/v1/collectors", wrap(portal.Portal.ListCollectors))
		mux.Handle("GET /api/v1/devices", wrap(portal.Portal.ListDevices))
		mux.Handle("GET /api/v1/devices/{id}", wrap(portal.Portal.GetDevice))
		mux.Handle("GET /api/v1/devices/{id}/telemetry", wrap(portal.Portal.GetTelemetry))
		mux.Handle("GET /api/v1/devices/{id}/telemetry/history", wrap(portal.Portal.TelemetryHistory))
		mux.Handle("GET /api/v1/events", wrap(portal.Portal.ListEvents))
		mux.Handle("GET /api/v1/audit", wrap(portal.Portal.ListAudit))
		mux.Handle("GET /api/v1/firmware", wrap(portal.Portal.FirmwareSummary))
		mux.Handle("GET /api/v1/firmware/targets", wrap(portal.Portal.ListFirmwareTargets))
		mux.Handle("POST /api/v1/firmware/targets", wrapAdmin(portal.Portal.UpsertFirmwareTarget))
		mux.Handle("DELETE /api/v1/firmware/targets/{id}", wrapAdmin(portal.Portal.DeleteFirmwareTarget))
		mux.Handle("GET /api/v1/reports/device-uptime", wrap(portal.Portal.DeviceUptimeReport))
		mux.Handle("GET /api/v1/reports/room-activity", wrap(portal.Portal.RoomActivityReport))
		mux.Handle("GET /api/v1/alerts", wrap(portal.Portal.ListAlerts))
		mux.Handle("GET /api/v1/alerts/summary", wrap(portal.Portal.AlertsSummary))
		if portal.WSHub != nil {
			mux.Handle("GET /ws/events", portalauth.Middleware(portal.Resolver, http.HandlerFunc(portal.WSHub.ServeHTTP)))
		}
		mux.Handle("GET /api/v1/regions", wrap(portal.Portal.ListRegions))
		mux.Handle("GET /api/v1/locations", wrap(portal.Portal.ListLocations))
		mux.Handle("GET /api/v1/buildings", wrap(portal.Portal.ListBuildings))
		mux.Handle("GET /api/v1/rooms", wrap(portal.Portal.ListRooms))

		// Customer Admin writes — device CRUD, placement (folded into PATCH),
		// and hierarchy CRUD.
		mux.Handle("POST /api/v1/devices", wrapAdmin(portal.Portal.CreateDevice))
		mux.Handle("PATCH /api/v1/devices/{id}", wrapAdmin(portal.Portal.UpdateDevice))
		mux.Handle("DELETE /api/v1/devices/{id}", wrapAdmin(portal.Portal.DeleteDevice))
		mux.Handle("POST /api/v1/regions", wrapAdmin(portal.Portal.CreateRegion))
		mux.Handle("POST /api/v1/locations", wrapAdmin(portal.Portal.CreateLocation))
		mux.Handle("POST /api/v1/buildings", wrapAdmin(portal.Portal.CreateBuilding))
		mux.Handle("POST /api/v1/rooms", wrapAdmin(portal.Portal.CreateRoom))
		mux.Handle("PATCH /api/v1/regions/{id}", wrapAdmin(portal.Portal.UpdateRegion))
		mux.Handle("PATCH /api/v1/locations/{id}", wrapAdmin(portal.Portal.UpdateLocation))
		mux.Handle("PATCH /api/v1/buildings/{id}", wrapAdmin(portal.Portal.UpdateBuilding))
		mux.Handle("PATCH /api/v1/rooms/{id}", wrapAdmin(portal.Portal.UpdateRoom))
		mux.Handle("DELETE /api/v1/regions/{id}", wrapAdmin(portal.Portal.DeleteRegion))
		mux.Handle("DELETE /api/v1/locations/{id}", wrapAdmin(portal.Portal.DeleteLocation))
		mux.Handle("DELETE /api/v1/buildings/{id}", wrapAdmin(portal.Portal.DeleteBuilding))
		mux.Handle("DELETE /api/v1/rooms/{id}", wrapAdmin(portal.Portal.DeleteRoom))

		// Command submission + status (Slice 3). Submit is operator-and-above;
		// reconnect is a special-cased command name handled by the bridge.
		operatorOrAbove := portalauth.RequireRole("operator", "admin")
		wrapOperator := func(h http.HandlerFunc) http.Handler {
			return portalauth.Middleware(portal.Resolver, operatorOrAbove(h))
		}
		mux.Handle("POST /api/v1/devices/{id}/command", wrapOperator(portal.Portal.SubmitCommand))
		mux.Handle("POST /api/v1/devices/{id}/reconnect", wrapOperator(portal.Portal.SubmitReconnect))
		mux.Handle("POST /api/v1/commands/bulk", wrapOperator(portal.Portal.SubmitBulkCommand))
		mux.Handle("GET /api/v1/commands/{id}", wrap(portal.Portal.GetCommand))
		mux.Handle("POST /api/v1/alerts/{id}/acknowledge", wrapOperator(portal.Portal.AcknowledgeAlert))
		mux.Handle("POST /api/v1/alerts/{id}/resolve", wrapOperator(portal.Portal.ResolveAlert))

		// Notification channels — list/read open to any authed user, writes
		// admin-only, test-send operator-or-above (operators verify their own
		// on-call channels without needing admin).
		mux.Handle("GET /api/v1/notifications/channels", wrap(portal.Portal.ListNotificationChannels))
		mux.Handle("POST /api/v1/notifications/channels", wrapAdmin(portal.Portal.CreateNotificationChannel))
		mux.Handle("PATCH /api/v1/notifications/channels/{id}", wrapAdmin(portal.Portal.UpdateNotificationChannel))
		mux.Handle("DELETE /api/v1/notifications/channels/{id}", wrapAdmin(portal.Portal.DeleteNotificationChannel))
		mux.Handle("POST /api/v1/notifications/channels/{id}/test", wrapOperator(portal.Portal.TestNotificationChannel))
	}

	// Bridge-side command channel — HMAC-authenticated, same scheme as /ingest.
	if bridgeCommands.Poll != nil {
		mux.Handle("POST /bridge/poll", bridgeCommands.Poll)
	}
	if bridgeCommands.Result != nil {
		mux.Handle("POST /bridge/commands/{id}/result", bridgeCommands.Result)
	}

	// Bridge-side config channel — same HMAC scheme. POST not GET because every
	// bridge call signs the request body, and a GET with no body can't.
	if bridgeConfig.GetConfig != nil {
		mux.Handle("POST /bridge/config", bridgeConfig.GetConfig)
	}
	if bridgeConfig.PutConfig != nil {
		mux.Handle("PUT /bridge/config", bridgeConfig.PutConfig)
	}

	return &http.Server{
		Addr:         addr,
		Handler:      logging(log, mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
}

func logging(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rw, r)
		log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rw.status,
			"latency_ms", time.Since(start).Milliseconds(),
			"remote", r.RemoteAddr,
		)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

// Hijack passes through to the wrapped ResponseWriter so gorilla/websocket can
// upgrade /ws/events. Without this, Upgrade fails with "response does not
// implement http.Hijacker" and the route returns 500.
func (w *statusWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hj, ok := w.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("statusWriter: underlying ResponseWriter does not implement http.Hijacker")
	}
	return hj.Hijack()
}
