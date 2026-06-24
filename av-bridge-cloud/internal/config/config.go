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

	// SMTP relay for outbound alert notifications. SMTPHost empty = dry-run
	// (sends log instead of actually emailing) so dev can exercise the
	// channel-config UI without standing up a mail server. SMTPFrom defaults
	// to alerts@av-bridge.local when unset.
	SMTPHost     string
	SMTPPort     string
	SMTPUsername string
	SMTPPassword string
	SMTPFrom     string
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
		CommandStaleAfter:    staleAfter,
		CommandMaxClaims:     maxClaims,
		CommandSweepInterval: sweepInterval,
		SMTPHost:             os.Getenv("POC_SMTP_HOST"),
		SMTPPort:             getenv("POC_SMTP_PORT", "587"),
		SMTPUsername:         os.Getenv("POC_SMTP_USERNAME"),
		SMTPPassword:         os.Getenv("POC_SMTP_PASSWORD"),
		SMTPFrom:             getenv("POC_SMTP_FROM", "alerts@av-bridge.local"),
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
