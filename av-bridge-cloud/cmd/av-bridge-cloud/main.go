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
	"github.com/dloomes/av-bridge-cloud/internal/portalapi"
	"github.com/dloomes/av-bridge-cloud/internal/portalauth"
	"github.com/dloomes/av-bridge-cloud/internal/secrets"
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

	// Live-event fan-out: ingest publishes, portal /ws/events subscribes.
	// Single hub is shared so events from ingest reach subscribed portals.
	hub := wsfanout.NewHub(log)

	h := ingest.NewHandler(store, cipher, hub, log)

	var adminH http.Handler
	if cfg.AdminAPIToken != "" {
		adminH = admin.NewCollectorHandler(store.AdminPool(), cipher, cfg.AdminAPIToken, log)
		log.Info("admin endpoints enabled", "routes", []string{"POST /admin/collectors"})
	} else {
		log.Warn("ADMIN_API_TOKEN not set — admin endpoints disabled")
	}

	var portalRoutes *api.PortalRoutes
	if cfg.PoCPortalToken != "" {
		resolver := portalauth.NewStaticResolver(cfg.PoCPortalToken, cfg.PoCPortalCustomerID, cfg.PoCPortalRole)
		portalRoutes = &api.PortalRoutes{
			Resolver: resolver,
			Portal:   portalapi.New(store, cipher, log),
			WSHub:    hub,
		}
		log.Info("portal read API enabled",
			"customer_id", cfg.PoCPortalCustomerID, "role", cfg.PoCPortalRole)
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

