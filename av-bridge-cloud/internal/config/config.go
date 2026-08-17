package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is loaded from the environment (12-factor) so the same binary runs
// under Docker Compose, AWS, GCP, Azure, or bare metal.
type Config struct {
	ListenAddr string

	// MigrationDSN points at a role that can run DDL (CREATE TABLE/ROLE, ALTER
	// TABLE ENABLE RLS, CREATE POLICY). In dev compose it's the Postgres
	// superuser; in production it's whatever one-shot migration role the
	// deployment grants. Required — migrations run on every cloud startup.
	MigrationDSN string

	AdminDSN  string // app_admin role (BYPASSRLS) — auth lookup, bootstrap
	TenantDSN string // app_tenant role (RLS-enforced) — all per-tenant data

	SecretKeyHex string // 64 hex chars = AES-256 key for at-rest secret encryption

	// Bearer token gating the admin endpoints (POST /admin/collectors etc.).
	// If empty, those endpoints reject every request — the placeholder for proper
	// Customer Admin auth once the portal lands.
	AdminAPIToken string

	// Slice 2 portal-read auth: one env-held token mapped to a customer. The
	// portal sends Authorization: Bearer <PoCPortalToken>; the portalauth
	// StaticResolver maps it to PoCPortalCustomerID. Replaced by DB-backed or
	// JWT resolvers as the auth slices land. PoCPortalRole controls what the
	// dev token is allowed to do (defaults to "admin" for dev convenience).
	PoCPortalToken      string
	PoCPortalCustomerID string
	PoCPortalRole       string

	// Optional PoC bootstrap: seed a single tenant + collector on startup so the
	// existing on-prem bridge can push without a registration UI yet.
	BootstrapPoC    bool
	PoCBridgeID     string
	PoCCustomerName string
	PoCHMACSecret   string

	// Stale-claim sweeper (Slice 3.1). A command that's been in_progress longer
	// than CommandStaleAfter is requeued to pending; once it has been claimed
	// CommandMaxClaims times without completing, it's failed with error
	// 'bridge_timeout'. The sweeper runs every CommandSweepInterval. Defaults
	// chosen to be safe: 5m stale, 3 max claims, 30s sweep.
	CommandStaleAfter    time.Duration
	CommandMaxClaims     int
	CommandSweepInterval time.Duration

	// Session cleanup — deletes user_sessions rows that have been
	// non-functional (expired or revoked) for longer than the retention
	// window. Cheap DELETE on an indexed column; hourly by default is
	// more than enough to keep the table trim without creating a
	// hot-path burden. 7-day retention leaves a grace window for any
	// future "recent sessions" audit view.
	SessionCleanupInterval  time.Duration
	SessionCleanupRetention time.Duration

	// Collector-health watcher — opens collector_offline alerts when a
	// bridge stops phoning home for OfflineAfter. Interval controls the
	// scan cadence; keep it below OfflineAfter so a fresh-offline
	// collector doesn't wait a full window before being noticed.
	CollectorHealthInterval     time.Duration
	CollectorHealthOfflineAfter time.Duration

	// Nightly Room Readiness — see docs/nightly-lifecycle-spec.md and
	// internal/nightly. TickInterval is how often the scheduler wakes;
	// GraceWindow bounds how late an event can still fire (protects
	// against fresh restarts triggering stale events); WarmupSeconds is
	// the total post-power-on wait before the room is declared ready.
	// DryRun means "log the intended device commands but don't send
	// them" — Slice 3 default is true so we can observe timing +
	// state-machine behaviour without touching real devices.
	NightlyTickInterval  time.Duration
	NightlyGraceWindow   time.Duration
	NightlyWarmupSeconds int
	NightlyDryRun        bool

	// Nightly digest sender — the morning email. TickInterval controls how
	// often the sender wakes to check whether any customer's digest is due
	// (spec: "power_on_time + 30min" local). SendAfterOffset is the delay
	// after the customer's local power_on_time before we send — 30m by
	// default so every room has time to finish its warm-up (+ Phase B
	// routine) before the digest is generated.
	NightlyDigestTickInterval    time.Duration
	NightlyDigestSendAfterOffset time.Duration

	// Nightly routine executor — Phase B slice 1. When enabled, a run
	// whose routine has steps takes the `testing` phase branch after
	// warming instead of going straight to `ready`. Read-side steps
	// (check_metric, expect_status) execute for real; write-side steps
	// (power/command) respect NightlyDryRun. StepTimeout caps a single
	// step's execution (default 5m); StuckAfter is how long a run can
	// sit in `testing` phase before startup-sweep marks it failed
	// (default 15m — comfortably longer than any reasonable routine).
	NightlyExecEnabled    bool
	NightlyExecStepTimeout time.Duration
	NightlyExecStuckAfter  time.Duration

	// SMTP relay for outbound alert notifications. SMTPHost empty = dry-run
	// (sends log instead of actually emailing) so dev can exercise the
	// channel-config UI without standing up a mail server. SMTPFrom defaults
	// to alerts@av-bridge.local when unset.
	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string

	// Local auth seed — creates a vendor admin on first startup if all three
	// are set and no user with that email already exists. Existing rows are
	// never overwritten, so rotating VENDOR_ADMIN_PASSWORD after first boot
	// has no effect (use the change-password endpoint or an admin CLI). All
	// three empty ⇒ no seed runs.
	VendorAdminEmail    string
	VendorAdminPassword string
	VendorAdminName     string

	// Entra ID (Microsoft) vendor sign-in — M1: vendor-only. Single-tenant
	// Entra app registration in Involve's tenant, used exclusively by
	// helpdesk staff. When any of TenantID / ClientID / ClientSecret is
	// unset, the vendor Entra path is disabled and the portal SSO tile
	// stays visually inert. PortalBaseURL is the origin the callback
	// redirects the browser back to (e.g. "https://app.uat.involvecloud.com")
	// so `/sign-in/callback?token=...` lands on the right frontend. Empty
	// PortalBaseURL falls back to the callback request's own scheme+host
	// (works when portal and API share an origin).
	EntraVendorTenantID     string
	EntraVendorClientID     string
	EntraVendorClientSecret string
	EntraVendorRedirectURI  string
	EntraPortalBaseURL      string
}

func FromEnv() (Config, error) {
	staleAfter, err := getenvDuration("COMMAND_STALE_AFTER", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	sweepInterval, err := getenvDuration("COMMAND_SWEEP_INTERVAL", 30*time.Second)
	if err != nil {
		return Config{}, err
	}
	maxClaims, err := getenvInt("COMMAND_MAX_CLAIMS", 3)
	if err != nil {
		return Config{}, err
	}
	sessionCleanupInterval, err := getenvDuration("SESSION_CLEANUP_INTERVAL", 1*time.Hour)
	if err != nil {
		return Config{}, err
	}
	sessionCleanupRetention, err := getenvDuration("SESSION_CLEANUP_RETENTION", 7*24*time.Hour)
	if err != nil {
		return Config{}, err
	}
	// Collector-health defaults: sweep every minute, flag a collector
	// offline after 5 min. Matches computeCollectorStatus's offline
	// threshold in portalapi so the /collectors page and the alert
	// generator agree on what "offline" means.
	collectorHealthInterval, err := getenvDuration("COLLECTOR_HEALTH_INTERVAL", 60*time.Second)
	if err != nil {
		return Config{}, err
	}
	collectorHealthOfflineAfter, err := getenvDuration("COLLECTOR_HEALTH_OFFLINE_AFTER", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	nightlyTick, err := getenvDuration("NIGHTLY_TICK_INTERVAL", 60*time.Second)
	if err != nil {
		return Config{}, err
	}
	nightlyGrace, err := getenvDuration("NIGHTLY_GRACE_WINDOW", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}
	nightlyWarmup, err := getenvInt("NIGHTLY_WARMUP_SECONDS", 60)
	if err != nil {
		return Config{}, err
	}
	nightlyDigestTick, err := getenvDuration("NIGHTLY_DIGEST_TICK_INTERVAL", 60*time.Second)
	if err != nil {
		return Config{}, err
	}
	nightlyDigestOffset, err := getenvDuration("NIGHTLY_DIGEST_SEND_AFTER_OFFSET", 30*time.Minute)
	if err != nil {
		return Config{}, err
	}
	nightlyExecStepTimeout, err := getenvDuration("NIGHTLY_EXEC_STEP_TIMEOUT", 5*time.Minute)
	if err != nil {
		return Config{}, err
	}
	nightlyExecStuckAfter, err := getenvDuration("NIGHTLY_EXEC_STUCK_AFTER", 15*time.Minute)
	if err != nil {
		return Config{}, err
	}

	c := Config{
		ListenAddr:           getenv("CLOUD_LISTEN_ADDR", ":8090"),
		MigrationDSN:         os.Getenv("DATABASE_MIGRATION_URL"),
		AdminDSN:             os.Getenv("DATABASE_ADMIN_URL"),
		TenantDSN:            os.Getenv("DATABASE_TENANT_URL"),
		SecretKeyHex:         os.Getenv("CLOUD_SECRET_KEY"),
		AdminAPIToken:        os.Getenv("ADMIN_API_TOKEN"),
		PoCPortalToken:       os.Getenv("POC_PORTAL_TOKEN"),
		PoCPortalCustomerID:  getenv("POC_PORTAL_CUSTOMER_ID", "11111111-1111-1111-1111-111111111111"),
		PoCPortalRole:        getenv("POC_PORTAL_ROLE", "admin"),
		BootstrapPoC:         os.Getenv("BOOTSTRAP_POC") == "true",
		PoCBridgeID:          getenv("POC_BRIDGE_COLLECTOR_ID", "poc-collector-01"),
		PoCCustomerName:      getenv("POC_CUSTOMER_NAME", "PoC Customer"),
		PoCHMACSecret:        os.Getenv("POC_HMAC_SECRET"),
		CommandStaleAfter:       staleAfter,
		CommandMaxClaims:        maxClaims,
		CommandSweepInterval:    sweepInterval,
		SessionCleanupInterval:  sessionCleanupInterval,
		SessionCleanupRetention: sessionCleanupRetention,
		CollectorHealthInterval:     collectorHealthInterval,
		CollectorHealthOfflineAfter: collectorHealthOfflineAfter,
		NightlyTickInterval:     nightlyTick,
		NightlyGraceWindow:      nightlyGrace,
		NightlyWarmupSeconds:    nightlyWarmup,
		// Default true: safer to no-op than to power-cycle a room by
		// surprise. Operators flip NIGHTLY_DRY_RUN=false once they've
		// wired the command queue in the follow-up slice.
		NightlyDryRun:                getenv("NIGHTLY_DRY_RUN", "true") != "false",
		NightlyDigestTickInterval:    nightlyDigestTick,
		NightlyDigestSendAfterOffset: nightlyDigestOffset,
		// Executor default false — opt-in per environment. Once enabled,
		// warming-phase runs with a routine assigned go via testing.
		NightlyExecEnabled:     getenv("NIGHTLY_EXEC_ENABLED", "false") == "true",
		NightlyExecStepTimeout: nightlyExecStepTimeout,
		NightlyExecStuckAfter:  nightlyExecStuckAfter,
		SMTPHost:             os.Getenv("POC_SMTP_HOST"),
		SMTPPort:             getenv("POC_SMTP_PORT", "587"),
		SMTPUsername:         os.Getenv("POC_SMTP_USERNAME"),
		SMTPPassword:         os.Getenv("POC_SMTP_PASSWORD"),
		SMTPFrom:             getenv("POC_SMTP_FROM", "alerts@av-bridge.local"),
		VendorAdminEmail:     os.Getenv("VENDOR_ADMIN_EMAIL"),
		VendorAdminPassword:  os.Getenv("VENDOR_ADMIN_PASSWORD"),
		VendorAdminName:      getenv("VENDOR_ADMIN_NAME", "Vendor Administrator"),

		EntraVendorTenantID:     os.Getenv("ENTRA_VENDOR_TENANT_ID"),
		EntraVendorClientID:     os.Getenv("ENTRA_VENDOR_CLIENT_ID"),
		EntraVendorClientSecret: os.Getenv("ENTRA_VENDOR_CLIENT_SECRET"),
		EntraVendorRedirectURI:  os.Getenv("ENTRA_VENDOR_REDIRECT_URI"),
		EntraPortalBaseURL:      os.Getenv("ENTRA_PORTAL_BASE_URL"),
	}
	if c.MigrationDSN == "" {
		return c, fmt.Errorf("DATABASE_MIGRATION_URL is required")
	}
	if c.AdminDSN == "" || c.TenantDSN == "" {
		return c, fmt.Errorf("DATABASE_ADMIN_URL and DATABASE_TENANT_URL are required")
	}
	if c.SecretKeyHex == "" {
		return c, fmt.Errorf("CLOUD_SECRET_KEY is required (64 hex chars = 32 bytes)")
	}
	if c.BootstrapPoC && c.PoCHMACSecret == "" {
		return c, fmt.Errorf("POC_HMAC_SECRET is required when BOOTSTRAP_POC=true")
	}
	return c, nil
}

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func getenvDuration(k string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	return d, nil
}

func getenvInt(k string, def int) (int, error) {
	v := os.Getenv(k)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", k, err)
	}
	return n, nil
}
