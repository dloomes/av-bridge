package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/dloomes/av-bridge-cloud/internal/admin"
	"github.com/dloomes/av-bridge-cloud/internal/api"
	"github.com/dloomes/av-bridge-cloud/internal/bridgecfg"
	"github.com/dloomes/av-bridge-cloud/internal/commands"
	"github.com/dloomes/av-bridge-cloud/internal/config"
	"github.com/dloomes/av-bridge-cloud/internal/db"
	"github.com/dloomes/av-bridge-cloud/internal/ingest"
	"github.com/dloomes/av-bridge-cloud/internal/nightly"
	"github.com/dloomes/av-bridge-cloud/internal/notify"
	"github.com/dloomes/av-bridge-cloud/internal/portalapi"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
	"github.com/dloomes/av-bridge-cloud/internal/sessioncleanup"
	"github.com/dloomes/av-bridge-cloud/internal/wsfanout"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	cfg, err := config.FromEnv()
	if err != nil {
		log.Error("config", "error", err)
		os.Exit(1)
	}

	cipher, err := secrets.NewAESGCMFromHexKey(cfg.SecretKeyHex)
	if err != nil {
		log.Error("cipher", "error", err)
		os.Exit(1)
	}

	ctx := context.Background()

	// Wait for Postgres on the migration DSN before doing any DDL — compose's
	// healthcheck only proves it accepts connections, not that this binary can
	// auth. Then apply embedded migrations (creates roles + schema on a fresh
	// volume; baselines an existing volume; applies new versions otherwise).
	if err := db.WaitForDSN(ctx, cfg.MigrationDSN, 30*time.Second, log); err != nil {
		log.Error("migration DB not reachable", "error", err)
		os.Exit(1)
	}
	if err := db.Migrate(ctx, cfg.MigrationDSN, log); err != nil {
		log.Error("migrate", "error", err)
		os.Exit(1)
	}

	store, err := db.New(ctx, cfg.AdminDSN, cfg.TenantDSN)
	if err != nil {
		log.Error("db connect", "error", err)
		os.Exit(1)
	}
	defer store.Close()

	if err := store.WaitReady(ctx, 30*time.Second); err != nil {
		log.Error("db not ready", "error", err)
		os.Exit(1)
	}

	if cfg.BootstrapPoC {
		if err := store.BootstrapPoC(ctx, cipher, cfg.PoCBridgeID, cfg.PoCCustomerName, cfg.PoCHMACSecret); err != nil {
			log.Error("poc bootstrap", "error", err)
			os.Exit(1)
		}
		log.Info("poc tenant bootstrapped", "bridge_collector_id", cfg.PoCBridgeID)
	}

	// Vendor admin seed — create a helpdesk login on first boot so an
	// operator can sign in via local auth without needing a portal user CRUD
	// UI. Idempotent: never overwrites an existing row.
	if cfg.VendorAdminEmail != "" && cfg.VendorAdminPassword != "" {
		created, err := store.SeedLocalUser(ctx, db.SeedLocalUserOptions{
			Email:          cfg.VendorAdminEmail,
			Password:       cfg.VendorAdminPassword,
			FullName:       cfg.VendorAdminName,
			Role:           "admin",
			VendorTenantID: db.PocVendorTenantUUID(),
		})
		if err != nil {
			log.Error("seed vendor admin", "error", err)
			os.Exit(1)
		}
		if created {
			log.Info("seeded vendor admin", "email", cfg.VendorAdminEmail)
		} else {
			log.Info("vendor admin already exists — password not touched", "email", cfg.VendorAdminEmail)
		}
	}

	// Live-event fan-out: ingest publishes, portal /ws/events subscribes.
	// Single hub is shared so events from ingest reach subscribed portals.
	hub := wsfanout.NewHub(log)

	// Outbound notification dispatcher. SMTPHost empty → email sends log
	// instead of fail (dev convenience). Real deployments configure SMTP
	// via POC_SMTP_* env vars; Teams/webhook channels work either way.
	senders := notify.NewSenders(notify.SMTPConfig{
		Host:     cfg.SMTPHost,
		Port:     cfg.SMTPPort,
		Username: cfg.SMTPUsername,
		Password: cfg.SMTPPassword,
		From:     cfg.SMTPFrom,
	}, log)
	dispatcher := notify.NewDispatcher(store.AdminPool(), senders, log)
	if cfg.SMTPHost == "" {
		log.Info("notify: SMTP not configured — email channels run in dry-run mode (logs only)")
	} else {
		log.Info("notify: SMTP configured", "host", cfg.SMTPHost, "from", cfg.SMTPFrom)
	}

	h := ingest.NewHandler(store, cipher, hub, dispatcher, log)

	var adminH http.Handler
	if cfg.AdminAPIToken != "" {
		adminH = admin.NewCollectorHandler(store.AdminPool(), cipher, cfg.AdminAPIToken, log)
		log.Info("admin endpoints enabled", "routes", []string{"POST /admin/collectors"})
	} else {
		log.Warn("ADMIN_API_TOKEN not set — admin endpoints disabled")
	}

	var portalRoutes *api.PortalRoutes
	if cfg.PoCPortalToken != "" {
		// Three-tier auth. Local is checked first: session tokens have a
		// distinctive `av_` prefix so mismatches short-circuit without a DB
		// query. Mock-JWT is next for the dev sign-in presets. Static token
		// catches the long-form Bearer used by smoke scripts. All three
		// hand back the same Principal shape.
		lookup := portalauth.NewDBTenantLookup(store.AdminPool())
		local := portalauth.NewLocalResolver(store.AdminPool())
		mock := portalauth.NewMockJWTResolver(lookup, true)
		static := portalauth.NewStaticResolver(cfg.PoCPortalToken, cfg.PoCPortalCustomerID, cfg.PoCPortalRole)
		resolver := portalauth.NewChainResolver(local, mock, static)
		portalRoutes = &api.PortalRoutes{
			Resolver: resolver,
			Portal:   portalapi.New(store, cipher, dispatcher, log),
			WSHub:    hub,
		}
		log.Info("portal API enabled",
			"customer_id", cfg.PoCPortalCustomerID, "role", cfg.PoCPortalRole,
			"local_auth", "enabled", "mock_jwt", "enabled")
	} else {
		log.Warn("POC_PORTAL_TOKEN not set — portal read API disabled")
	}

	bridgeCmds := commands.NewBridgeHandler(store, cipher, log)
	bridgeRoutes := api.BridgeCommandRoutes{
		Poll:   bridgeCmds.Poll,
		Result: bridgeCmds.PostResult,
	}

	bridgeCfg := bridgecfg.NewHandler(store, cipher, log)
	bridgeConfigRoutes := api.BridgeConfigRoutes{
		GetConfig: bridgeCfg.Get,
		PutConfig: bridgeCfg.Put,
	}

	srv := api.NewServer(cfg.ListenAddr, h, adminH, portalRoutes, bridgeRoutes, bridgeConfigRoutes, log)

	// Slice 3.1 — sweep stuck in_progress commands across all tenants.
	sweeperCtx, stopSweeper := context.WithCancel(context.Background())
	defer stopSweeper()
	sweeper := commands.NewSweeper(
		store.AdminPool(),
		cfg.CommandSweepInterval,
		cfg.CommandStaleAfter,
		cfg.CommandMaxClaims,
		log,
	)
	go sweeper.Run(sweeperCtx)

	// Portal session housekeeping — deletes user_sessions rows that have
	// been non-functional (expired or revoked) for longer than the
	// retention window. Shares sweeperCtx so shutdown stops both loops
	// via the same stopSweeper() call.
	sessionCleaner := sessioncleanup.NewCleaner(
		store.AdminPool(),
		cfg.SessionCleanupInterval,
		cfg.SessionCleanupRetention,
		log,
	)
	go sessionCleaner.Run(sweeperCtx)

	// Nightly Room Readiness scheduler — enacts the customer + per-room
	// schedules by creating nightly_run rows and walking each room
	// through its power-off / power-on state machine. In DRY-RUN mode
	// (Slice 3 default), device commands are logged only, not dispatched.
	// See docs/nightly-lifecycle-spec.md.
	nightlyScheduler := nightly.NewScheduler(
		store.AdminPool(),
		nightly.Config{
			TickInterval:  cfg.NightlyTickInterval,
			GraceWindow:   cfg.NightlyGraceWindow,
			WarmupSeconds: cfg.NightlyWarmupSeconds,
			DryRun:        cfg.NightlyDryRun,
		},
		log,
	)
	go nightlyScheduler.Run(sweeperCtx)

	go func() {
		log.Info("cloud ingest listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server", "error", err)
			os.Exit(1)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	stopSweeper()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
	log.Info("shutdown complete")
}

