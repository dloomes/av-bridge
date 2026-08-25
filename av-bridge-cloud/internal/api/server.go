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
// fan-out, gated by the same Resolver as the rest of /api/v1. EntraVendor and
// EntraCustomer are optional — when non-nil, their SSO authorize/callback
// routes are registered (they live outside the auth middleware since a
// pre-login user has no bearer token).
type PortalRoutes struct {
	Resolver      portalauth.Resolver
	Portal        *portalapi.Handler
	WSHub         *wsfanout.Hub
	EntraVendor   *portalapi.EntraVendorHandler
	EntraCustomer *portalapi.EntraCustomerHandler
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

// PublicRoutes bundles endpoints that need to serve without auth: they run
// before a user has any credentials to hand over (branding on the sign-in
// page), as part of establishing new credentials (password reset), to
// redeem a vendor-issued break-glass link (magic-link consume), or to
// redeem a pre-provisioned collector enrollment token from an on-site
// install script. Nil fields skip registration. All are intentionally
// under /public/*.
type PublicRoutes struct {
	Branding              http.HandlerFunc
	PasswordResetRequest  http.HandlerFunc
	PasswordResetComplete http.HandlerFunc
	MagicLinkConsume      http.HandlerFunc
	CollectorEnroll          http.HandlerFunc
	CollectorInstallScript   http.HandlerFunc
	CollectorInstallScriptPS http.HandlerFunc
	ListDownloads            http.HandlerFunc
	ServeDownload            http.HandlerFunc
}

// NewServer wires the routes. Go 1.22 method-aware patterns keep us dependency-free.
// adminCollectors may be nil — registration endpoints are off when no ADMIN_API_TOKEN
// is configured. portal may be nil — read API is off when no POC_PORTAL_TOKEN is set.
// bridgeCommands wires POST /bridge/poll and POST /bridge/commands/{id}/result.
func NewServer(addr string, ingest, adminCollectors http.Handler, portal *PortalRoutes, bridgeCommands BridgeCommandRoutes, bridgeConfig BridgeConfigRoutes, public PublicRoutes, log *slog.Logger) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	})
	mux.Handle("POST /ingest", ingest)
	if adminCollectors != nil {
		mux.Handle("POST /admin/collectors", adminCollectors)
	}

	// Unauthenticated public endpoints. Kept as a separate group so a review
	// of this file surfaces them in one place — everything under /public/*
	// is intentionally readable without a token.
	if public.Branding != nil {
		mux.Handle("GET /public/branding", public.Branding)
	}
	if public.PasswordResetRequest != nil {
		mux.Handle("POST /public/password-reset/request", public.PasswordResetRequest)
	}
	if public.PasswordResetComplete != nil {
		mux.Handle("POST /public/password-reset/complete", public.PasswordResetComplete)
	}
	if public.MagicLinkConsume != nil {
		mux.Handle("GET /public/magic-link/consume", public.MagicLinkConsume)
	}
	if public.CollectorEnroll != nil {
		mux.Handle("POST /public/collectors/enroll", public.CollectorEnroll)
	}
	if public.CollectorInstallScript != nil {
		mux.Handle("GET /public/collectors/install.sh", public.CollectorInstallScript)
	}
	if public.CollectorInstallScriptPS != nil {
		mux.Handle("GET /public/collectors/install.ps1", public.CollectorInstallScriptPS)
	}
	if public.ListDownloads != nil {
		mux.Handle("GET /public/downloads", public.ListDownloads)
	}
	if public.ServeDownload != nil {
		mux.Handle("GET /public/downloads/{key}", public.ServeDownload)
	}

	if portal != nil {
		// wrap: any authenticated portal user — used for /whoami and the
		// auth self-service endpoints (logout, change-password) where the
		// caller just needs a valid session.
		//
		// wrapPerm: authenticated + the named permission granted (via role
		// membership on the calling tenant). Vendor callers bypass the
		// permission check by design — HasPermission short-circuits on
		// IsVendor in portalauth/middleware.go.
		wrap := func(h http.HandlerFunc) http.Handler {
			return portalauth.Middleware(portal.Resolver, h)
		}
		wrapPerm := func(perm string, h http.HandlerFunc) http.Handler {
			return portalauth.Middleware(portal.Resolver, portalauth.RequirePermission(perm)(h))
		}

		// Login is intentionally OUTSIDE the auth middleware — a fresh user
		// has no token yet. Logout accepts a token but tolerates an
		// unauthenticated caller (idempotent 204) so it can be a "clear my
		// session server-side" call even after the token expired.
		mux.Handle("POST /api/v1/auth/login", http.HandlerFunc(portal.Portal.Login))
		mux.Handle("POST /api/v1/auth/logout", http.HandlerFunc(portal.Portal.Logout))
		mux.Handle("POST /api/v1/auth/change-password", wrap(portal.Portal.ChangePassword))

		// Entra ID vendor SSO. Also outside auth middleware — the callback
		// establishes the session, it can't require one. Registered only
		// when config is present; the nil-check keeps deploys without
		// Entra credentials from exposing broken routes.
		if portal.EntraVendor != nil {
			mux.Handle("GET /api/v1/auth/entra/vendor/authorize", http.HandlerFunc(portal.EntraVendor.Authorize))
			mux.Handle("GET /api/v1/auth/entra/vendor/callback", http.HandlerFunc(portal.EntraVendor.Callback))
		}
		if portal.EntraCustomer != nil {
			mux.Handle("GET /api/v1/auth/entra/customer/authorize", http.HandlerFunc(portal.EntraCustomer.Authorize))
			mux.Handle("GET /api/v1/auth/entra/customer/callback", http.HandlerFunc(portal.EntraCustomer.Callback))
		}

		// Whoami is any authenticated user — no specific permission needed
		// (it returns the caller's own identity + effective permissions).
		mux.Handle("GET /api/v1/whoami", wrap(portal.Portal.Whoami))
		// Preference update — any authenticated user can flip their own
		// landing_page. Same auth stance as whoami.
		mux.Handle("PATCH /api/v1/me/preferences", wrap(portal.Portal.UpdateMyPreferences))

		// Vendor-only cross-tenant endpoints — RequireVendor stays because
		// vendor is a Principal flag orthogonal to the customer RBAC.
		mux.Handle("GET /api/v1/helpdesk/customers",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.HelpdeskListCustomers))))
		mux.Handle("GET /api/v1/helpdesk/overview",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.HelpdeskOverview))))
		mux.Handle("POST /api/v1/helpdesk/customers",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.HelpdeskCreateCustomer))))
		mux.Handle("PATCH /api/v1/helpdesk/customers/{id}",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.HelpdeskUpdateCustomer))))

		// Vendor-tenant user management (M3.1). Same-shape endpoints as
		// the customer /users CRUD but scoped to the single vendor
		// tenant and RequireVendor'd. No POST — new vendor users are
		// only provisioned via Entra JIT (see entra.go).
		mux.Handle("GET /api/v1/helpdesk/users",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.HelpdeskListUsers))))
		mux.Handle("PATCH /api/v1/helpdesk/users/{id}",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.HelpdeskUpdateUser))))
		mux.Handle("DELETE /api/v1/helpdesk/users/{id}",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.HelpdeskDeleteUser))))
		mux.Handle("POST /api/v1/helpdesk/users/{id}/magic-link",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.IssueVendorMagicLink))))

		// Dashboard-level reads. view.dashboard covers the shape of the
		// portal's main pages: fleet status, device list + detail + live
		// telemetry, event stream, alerts feed, and the physical-hierarchy
		// listings the sidebar uses to render its location tree.
		mux.Handle("GET /api/v1/status", wrapPerm(portalauth.PermViewDashboard, portal.Portal.Status))
		mux.Handle("GET /api/v1/collectors", wrapPerm(portalauth.PermViewDashboard, portal.Portal.ListCollectors))
		// Pre-provisioning + enrollment (collector-enroll v1). Gated by
		// collector.crud — customer admins get it out of the box, vendor
		// bypass rides the standard middleware path.
		mux.Handle("POST /api/v1/collectors", wrapPerm(portalauth.PermCollectorCRUD, portal.Portal.CreateCollector))
		mux.Handle("POST /api/v1/collectors/{id}/enrollment-token", wrapPerm(portalauth.PermCollectorCRUD, portal.Portal.ReissueCollectorEnrollmentToken))
		mux.Handle("DELETE /api/v1/collectors/{id}", wrapPerm(portalauth.PermCollectorCRUD, portal.Portal.DeleteCollector))
		mux.Handle("GET /api/v1/devices", wrapPerm(portalauth.PermViewDashboard, portal.Portal.ListDevices))
		mux.Handle("GET /api/v1/devices/{id}", wrapPerm(portalauth.PermViewDashboard, portal.Portal.GetDevice))
		mux.Handle("GET /api/v1/devices/{id}/telemetry", wrapPerm(portalauth.PermViewDashboard, portal.Portal.GetTelemetry))
		mux.Handle("GET /api/v1/devices/{id}/telemetry/history", wrapPerm(portalauth.PermViewDashboard, portal.Portal.TelemetryHistory))
		mux.Handle("GET /api/v1/devices/{id}/events", wrapPerm(portalauth.PermViewDashboard, portal.Portal.DeviceEvents))
		mux.Handle("GET /api/v1/events", wrapPerm(portalauth.PermViewDashboard, portal.Portal.ListEvents))
		mux.Handle("GET /api/v1/alerts", wrapPerm(portalauth.PermViewDashboard, portal.Portal.ListAlerts))
		mux.Handle("GET /api/v1/alerts/summary", wrapPerm(portalauth.PermViewDashboard, portal.Portal.AlertsSummary))
		mux.Handle("GET /api/v1/regions", wrapPerm(portalauth.PermViewDashboard, portal.Portal.ListRegions))
		mux.Handle("GET /api/v1/locations", wrapPerm(portalauth.PermViewDashboard, portal.Portal.ListLocations))
		mux.Handle("GET /api/v1/buildings", wrapPerm(portalauth.PermViewDashboard, portal.Portal.ListBuildings))
		mux.Handle("GET /api/v1/rooms", wrapPerm(portalauth.PermViewDashboard, portal.Portal.ListRooms))
		mux.Handle("GET /api/v1/adapters", wrapPerm(portalauth.PermViewDashboard, portal.Portal.ListAdapters))
		mux.Handle("GET /api/v1/commands/{id}", wrapPerm(portalauth.PermViewDashboard, portal.Portal.GetCommand))
		if portal.WSHub != nil {
			mux.Handle("GET /ws/events",
				portalauth.Middleware(portal.Resolver,
					portalauth.RequirePermission(portalauth.PermViewDashboard)(http.HandlerFunc(portal.WSHub.ServeHTTP))))
		}

		// Category-specific reads — each gated by its matching view.* key.
		mux.Handle("GET /api/v1/audit", wrapPerm(portalauth.PermViewAudit, portal.Portal.ListAudit))
		mux.Handle("GET /api/v1/reports/device-uptime", wrapPerm(portalauth.PermViewReports, portal.Portal.DeviceUptimeReport))
		mux.Handle("GET /api/v1/reports/room-activity", wrapPerm(portalauth.PermViewReports, portal.Portal.RoomActivityReport))
		mux.Handle("GET /api/v1/firmware", wrapPerm(portalauth.PermViewFirmware, portal.Portal.FirmwareSummary))
		mux.Handle("GET /api/v1/firmware/targets", wrapPerm(portalauth.PermViewFirmware, portal.Portal.ListFirmwareTargets))
		mux.Handle("GET /api/v1/notifications/channels", wrapPerm(portalauth.PermViewNotifications, portal.Portal.ListNotificationChannels))
		mux.Handle("GET /api/v1/users", wrapPerm(portalauth.PermViewUsers, portal.Portal.ListUsers))
		mux.Handle("GET /api/v1/roles", wrapPerm(portalauth.PermViewUsers, portal.Portal.ListRoles))
		mux.Handle("GET /api/v1/roles/{id}", wrapPerm(portalauth.PermViewUsers, portal.Portal.GetRole))

		// Entra group → role mappings (M3). Customer-scoped: gated by
		// role_mapping.manage. Reads share the same gate — the mapping
		// table is admin ops surface, not something we want random tenant
		// users browsing. Vendor cross-tenant editing rides X-Customer-Scope
		// + the vendor-bypass rule the middleware already applies.
		mux.Handle("GET /api/v1/role-mappings", wrapPerm(portalauth.PermRoleMappingManage, portal.Portal.ListCustomerRoleMappings))
		mux.Handle("POST /api/v1/role-mappings", wrapPerm(portalauth.PermRoleMappingManage, portal.Portal.CreateCustomerRoleMapping))
		mux.Handle("PATCH /api/v1/role-mappings/{id}", wrapPerm(portalauth.PermRoleMappingManage, portal.Portal.UpdateCustomerRoleMapping))
		mux.Handle("DELETE /api/v1/role-mappings/{id}", wrapPerm(portalauth.PermRoleMappingManage, portal.Portal.DeleteCustomerRoleMapping))

		// Vendor-scoped role mappings — RequireVendor since the vendor
		// tenant has no per-tenant roles table and only the helpdesk
		// itself needs to configure them (there's one vendor tenant today).
		mux.Handle("GET /api/v1/vendor-role-mappings",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.ListVendorRoleMappings))))
		mux.Handle("POST /api/v1/vendor-role-mappings",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.CreateVendorRoleMapping))))
		mux.Handle("PATCH /api/v1/vendor-role-mappings/{id}",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.UpdateVendorRoleMapping))))
		mux.Handle("DELETE /api/v1/vendor-role-mappings/{id}",
			portalauth.Middleware(portal.Resolver,
				portalauth.RequireVendor(http.HandlerFunc(portal.Portal.DeleteVendorRoleMapping))))

		// Tenant branding — reads are open to any authed user (the portal
		// needs the logo/colours for the current tenant to render), writes
		// gate on branding.update. Vendor cross-tenant editing rides the
		// same PATCH via X-Customer-Scope + vendor-bypass on the permission
		// check, so no separate /helpdesk endpoint is needed.
		mux.Handle("GET /api/v1/branding", wrap(portal.Portal.GetBranding))
		mux.Handle("PATCH /api/v1/branding", wrapPerm(portalauth.PermBrandingUpdate, portal.Portal.UpdateBranding))

		// Assets (CMDB). view.assets covers list + detail so operators and
		// viewers can browse inventory; asset.crud gates writes for admins
		// (or custom roles). Physical scope is enforced by the RESTRICTIVE
		// RLS policy in migration 0022 — nothing to repeat here.
		mux.Handle("GET /api/v1/assets", wrapPerm(portalauth.PermViewAssets, portal.Portal.ListAssets))
		mux.Handle("GET /api/v1/assets/{id}", wrapPerm(portalauth.PermViewAssets, portal.Portal.GetAsset))
		mux.Handle("POST /api/v1/assets", wrapPerm(portalauth.PermAssetCRUD, portal.Portal.CreateAsset))
		mux.Handle("PATCH /api/v1/assets/{id}", wrapPerm(portalauth.PermAssetCRUD, portal.Portal.UpdateAsset))
		mux.Handle("DELETE /api/v1/assets/{id}", wrapPerm(portalauth.PermAssetCRUD, portal.Portal.DeleteAsset))
		// CSV round-trip. Export is a plain read — anyone with view.assets
		// gets the current fleet; RLS applies. Import mutates so it gates
		// on asset.crud.
		mux.Handle("GET /api/v1/assets/export.csv", wrapPerm(portalauth.PermViewAssets, portal.Portal.ExportAssets))
		mux.Handle("POST /api/v1/assets/import", wrapPerm(portalauth.PermAssetCRUD, portal.Portal.ImportAssets))

		// Nightly Room Readiness — schedule CRUD. Slice 1 exposes only the
		// customer-level schedule row; room overrides and routine CRUD land
		// in subsequent slices. GET auto-provisions defaults so the portal
		// never needs a "create" step.
		mux.Handle("GET /api/v1/nightly/schedule", wrapPerm(portalauth.PermNightlyView, portal.Portal.GetNightlySchedule))
		mux.Handle("PATCH /api/v1/nightly/schedule", wrapPerm(portalauth.PermNightlyManage, portal.Portal.UpdateNightlySchedule))
		// Per-room overrides — slice 2A. GET lists rooms + effective
		// schedule (COALESCE'd against customer default); PATCH upserts an
		// override with explicit-null-clears semantics; DELETE removes the
		// row entirely so the room reverts to inheriting.
		mux.Handle("GET /api/v1/nightly/rooms", wrapPerm(portalauth.PermNightlyView, portal.Portal.ListNightlyRooms))
		mux.Handle("PATCH /api/v1/nightly/rooms/{id}", wrapPerm(portalauth.PermNightlyManage, portal.Portal.UpdateRoomOverride))
		mux.Handle("DELETE /api/v1/nightly/rooms/{id}", wrapPerm(portalauth.PermNightlyManage, portal.Portal.DeleteRoomOverride))
		// Test routines — slice 2B. Read gated on nightly.view (viewers
		// can browse existing routines); writes on nightly.manage.
		mux.Handle("GET /api/v1/nightly/routines", wrapPerm(portalauth.PermNightlyView, portal.Portal.ListNightlyRoutines))
		mux.Handle("GET /api/v1/nightly/routines/{id}", wrapPerm(portalauth.PermNightlyView, portal.Portal.GetNightlyRoutine))
		mux.Handle("POST /api/v1/nightly/routines", wrapPerm(portalauth.PermNightlyManage, portal.Portal.CreateNightlyRoutine))
		mux.Handle("PATCH /api/v1/nightly/routines/{id}", wrapPerm(portalauth.PermNightlyManage, portal.Portal.UpdateNightlyRoutine))
		mux.Handle("DELETE /api/v1/nightly/routines/{id}", wrapPerm(portalauth.PermNightlyManage, portal.Portal.DeleteNightlyRoutine))
		// Run history — slice 4. Read only; the scheduler is the writer.
		mux.Handle("GET /api/v1/nightly/runs", wrapPerm(portalauth.PermNightlyView, portal.Portal.ListNightlyRuns))
		mux.Handle("GET /api/v1/nightly/runs/{id}", wrapPerm(portalauth.PermNightlyView, portal.Portal.GetNightlyRun))
		// Morning digest — slice 5. The goroutine sends automatically; this
		// endpoint lets an operator trigger a preview send on demand.
		mux.Handle("POST /api/v1/nightly/digest/send-now", wrapPerm(portalauth.PermNightlyManage, portal.Portal.SendNightlyDigest))
		// Ad-hoc routine trigger — Phase B slice 1. Runs the routine
		// against the chosen room immediately, skipping the power-cycle
		// preamble. Body accepts an optional {routine_id} override for
		// "test this routine against this room" from the editor.
		mux.Handle("POST /api/v1/nightly/rooms/{id}/run-now", wrapPerm(portalauth.PermNightlyManage, portal.Portal.SendRoutineRunNow))

		// User CRUD — every write is a distinct permission so a custom role
		// can, e.g., grant create-and-update but not delete-and-reset.
		mux.Handle("POST /api/v1/users", wrapPerm(portalauth.PermUserCreate, portal.Portal.CreateUser))
		mux.Handle("PATCH /api/v1/users/{id}", wrapPerm(portalauth.PermUserUpdate, portal.Portal.UpdateUser))
		mux.Handle("POST /api/v1/users/{id}/reset-password", wrapPerm(portalauth.PermUserResetPassword, portal.Portal.ResetUserPassword))
		// M4.1 break-glass. Vendor-only enforcement lives inside the
		// handler because we want to reuse the /users route path (it
		// composes with X-Customer-Scope and the customer-user CRUD
		// permissions). Non-vendor callers get a 403 with a clear
		// "vendor helpdesk only" message.
		mux.Handle("POST /api/v1/users/{id}/magic-link",
			portalauth.Middleware(portal.Resolver, http.HandlerFunc(portal.Portal.IssueCustomerMagicLink)))
		mux.Handle("DELETE /api/v1/users/{id}", wrapPerm(portalauth.PermUserDelete, portal.Portal.DeleteUser))

		// Role catalogue writes — single role.crud permission covers all
		// three. System-default protection is enforced by the handlers.
		mux.Handle("POST /api/v1/roles", wrapPerm(portalauth.PermRoleCRUD, portal.Portal.CreateRole))
		mux.Handle("PATCH /api/v1/roles/{id}", wrapPerm(portalauth.PermRoleCRUD, portal.Portal.UpdateRole))
		mux.Handle("DELETE /api/v1/roles/{id}", wrapPerm(portalauth.PermRoleCRUD, portal.Portal.DeleteRole))

		// Device CRUD.
		mux.Handle("POST /api/v1/devices", wrapPerm(portalauth.PermDeviceCRUD, portal.Portal.CreateDevice))
		mux.Handle("PATCH /api/v1/devices/{id}", wrapPerm(portalauth.PermDeviceCRUD, portal.Portal.UpdateDevice))
		mux.Handle("DELETE /api/v1/devices/{id}", wrapPerm(portalauth.PermDeviceCRUD, portal.Portal.DeleteDevice))

		// Physical hierarchy CRUD — single permission for the whole tree.
		mux.Handle("POST /api/v1/regions", wrapPerm(portalauth.PermHierarchyCRUD, portal.Portal.CreateRegion))
		mux.Handle("POST /api/v1/locations", wrapPerm(portalauth.PermHierarchyCRUD, portal.Portal.CreateLocation))
		mux.Handle("POST /api/v1/buildings", wrapPerm(portalauth.PermHierarchyCRUD, portal.Portal.CreateBuilding))
		mux.Handle("POST /api/v1/rooms", wrapPerm(portalauth.PermHierarchyCRUD, portal.Portal.CreateRoom))
		mux.Handle("PATCH /api/v1/regions/{id}", wrapPerm(portalauth.PermHierarchyCRUD, portal.Portal.UpdateRegion))
		mux.Handle("PATCH /api/v1/locations/{id}", wrapPerm(portalauth.PermHierarchyCRUD, portal.Portal.UpdateLocation))
		mux.Handle("PATCH /api/v1/buildings/{id}", wrapPerm(portalauth.PermHierarchyCRUD, portal.Portal.UpdateBuilding))
		mux.Handle("PATCH /api/v1/rooms/{id}", wrapPerm(portalauth.PermHierarchyCRUD, portal.Portal.UpdateRoom))
		mux.Handle("DELETE /api/v1/regions/{id}", wrapPerm(portalauth.PermHierarchyCRUD, portal.Portal.DeleteRegion))
		mux.Handle("DELETE /api/v1/locations/{id}", wrapPerm(portalauth.PermHierarchyCRUD, portal.Portal.DeleteLocation))
		mux.Handle("DELETE /api/v1/buildings/{id}", wrapPerm(portalauth.PermHierarchyCRUD, portal.Portal.DeleteBuilding))
		mux.Handle("DELETE /api/v1/rooms/{id}", wrapPerm(portalauth.PermHierarchyCRUD, portal.Portal.DeleteRoom))

		// Command channel. Split single-device, reconnect, and bulk so a
		// role can be defined as e.g. "can reconnect but not send arbitrary
		// commands" — useful for a signage tech who's meant to power-cycle
		// but not change inputs.
		mux.Handle("POST /api/v1/devices/{id}/command", wrapPerm(portalauth.PermCommandDevice, portal.Portal.SubmitCommand))
		mux.Handle("POST /api/v1/devices/{id}/reconnect", wrapPerm(portalauth.PermReconnectDevice, portal.Portal.SubmitReconnect))
		mux.Handle("POST /api/v1/commands/bulk", wrapPerm(portalauth.PermCommandBulk, portal.Portal.SubmitBulkCommand))

		// Alert lifecycle — acknowledge and resolve are separately gated.
		mux.Handle("POST /api/v1/alerts/{id}/acknowledge", wrapPerm(portalauth.PermAlertAcknowledge, portal.Portal.AcknowledgeAlert))
		mux.Handle("POST /api/v1/alerts/{id}/resolve", wrapPerm(portalauth.PermAlertResolve, portal.Portal.ResolveAlert))

		// Firmware policy — single permission for the whole target CRUD.
		mux.Handle("POST /api/v1/firmware/targets", wrapPerm(portalauth.PermFirmwareTargetCRUD, portal.Portal.UpsertFirmwareTarget))
		mux.Handle("DELETE /api/v1/firmware/targets/{id}", wrapPerm(portalauth.PermFirmwareTargetCRUD, portal.Portal.DeleteFirmwareTarget))

		// Notification channel CRUD vs test-send. Split so an ops-team role
		// can verify their own on-call channels (test) without owning full
		// channel management (crud).
		mux.Handle("POST /api/v1/notifications/channels", wrapPerm(portalauth.PermNotificationCRUD, portal.Portal.CreateNotificationChannel))
		mux.Handle("PATCH /api/v1/notifications/channels/{id}", wrapPerm(portalauth.PermNotificationCRUD, portal.Portal.UpdateNotificationChannel))
		mux.Handle("DELETE /api/v1/notifications/channels/{id}", wrapPerm(portalauth.PermNotificationCRUD, portal.Portal.DeleteNotificationChannel))
		mux.Handle("POST /api/v1/notifications/channels/{id}/test", wrapPerm(portalauth.PermNotificationTest, portal.Portal.TestNotificationChannel))
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
