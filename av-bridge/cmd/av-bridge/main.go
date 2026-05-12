package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dloomes/av-bridge/internal/api"
	"github.com/dloomes/av-bridge/internal/cloud"
	"github.com/dloomes/av-bridge/internal/cloud/lens"
	"github.com/dloomes/av-bridge/internal/config"
	"github.com/dloomes/av-bridge/internal/hub"
	"github.com/dloomes/av-bridge/internal/store"
)

func notifySystemd(state string) {
	socketPath := os.Getenv("NOTIFY_SOCKET")
	if socketPath == "" {
		return
	}
	conn, err := net.DialUnix("unixgram", nil, &net.UnixAddr{Name: socketPath, Net: "unixgram"})
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte(state))
}

var (
	version   = "dev"
	buildTime = "unknown"
)

func main() {
	configPath := flag.String("config", "/etc/av-bridge/config.yaml", "path to config file")
	envPath := flag.String("env", "", "path to env file (KEY=VALUE format); if empty, looks for poc.env or .env next to the config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	validate := flag.Bool("validate", false, "validate config and exit (0 if valid, 1 if not)")
	flag.Parse()

	if *showVersion {
		fmt.Printf("av-bridge version %s (built %s)\n", version, buildTime)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)
	// Note: re-set below after config load so cfg.Hub.LogLevel takes effect.

	// Load env file before config — config.Load expands ${VAR} references and
	// needs the values present in os.Environ at that point.
	resolvedEnv := *envPath
	if resolvedEnv == "" {
		dir := filepath.Dir(*configPath)
		for _, candidate := range []string{
			filepath.Join(dir, "poc.env"),
			filepath.Join(dir, ".env"),
		} {
			if _, statErr := os.Stat(candidate); statErr == nil {
				resolvedEnv = candidate
				break
			}
		}
	}
	if resolvedEnv != "" {
		if err := config.LoadEnvFile(resolvedEnv); err != nil {
			slog.Error("failed to load env file", "path", resolvedEnv, "error", err)
			os.Exit(1)
		}
		slog.Info("loaded env file", "path", resolvedEnv)
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		if *validate {
			fmt.Fprintf(os.Stderr, "config invalid: %v\n", err)
			os.Exit(1)
		}
		slog.Error("failed to load config", "path", *configPath, "error", err)
		os.Exit(1)
	}

	if *validate {
		fmt.Println("config valid")
		os.Exit(0)
	}

	var lvl slog.Level
	switch strings.ToLower(cfg.Hub.LogLevel) {
	case "debug":
		lvl = slog.LevelDebug
	case "warn", "warning":
		lvl = slog.LevelWarn
	case "error":
		lvl = slog.LevelError
	default:
		lvl = slog.LevelInfo
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Persistent state store (survives restarts)
	st, err := store.New(cfg.Hub.StorePath)
	if err != nil {
		slog.Error("failed to open state store", "path", cfg.Hub.StorePath, "error", err)
		os.Exit(1)
	}

	cloudClient := cloud.NewClient(cfg.Cloud, cfg.Hub.CollectorID, cfg.Hub.SiteID)

	var lensClient *lens.Client
	if cfg.Lens.Enabled {
		if cfg.Lens.ClientID == "" || cfg.Lens.ClientSecret == "" {
			slog.Error("lens enabled but client_id or client_secret missing")
			os.Exit(1)
		}
		lensClient = lens.NewClient(cfg.Lens.ClientID, cfg.Lens.ClientSecret)
		slog.Info("lens enrichment enabled")

		go func() {
			schemaCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			schema, err := lensClient.Introspect(schemaCtx)
			if err != nil {
				slog.Warn("lens introspection failed — schema discovery skipped", "error", err)
				return
			}
			slog.Info("lens schema discovered", "schema", schema)
		}()
	}

	h := hub.New(cfg, cloudClient, lensClient, st)

	authCfg := api.AuthConfig{
		Enabled:    cfg.API.Auth.Enabled,
		APIKeys:    cfg.API.Auth.APIKeys,
		HMACSecret: cfg.API.Auth.HMACSecret,
	}
	apiServer := api.New(cfg.Hub.ListenAddr, h, cloudClient, authCfg)

	if err := h.Start(ctx); err != nil {
		slog.Error("hub start failed", "error", err)
		os.Exit(1)
	}

	go func() {
		var serverErr error
		if cfg.API.TLS.Enabled {
			serverErr = apiServer.StartTLS(cfg.API.TLS.CertFile, cfg.API.TLS.KeyFile)
		} else {
			serverErr = apiServer.Start()
		}
		if serverErr != nil && serverErr != http.ErrServerClosed {
			slog.Error("API server error", "error", serverErr)
		}
	}()

	slog.Info("av-bridge running",
		"version", version,
		"devices", len(cfg.Devices),
		"listen", cfg.Hub.ListenAddr,
		"auth_enabled", cfg.API.Auth.Enabled,
		"tls_enabled", cfg.API.TLS.Enabled,
		"store", cfg.Hub.StorePath,
		"cloud_url", cfg.Cloud.WebhookURL,
	)

	notifySystemd("READY=1")

	go func() {
		ticker := time.NewTicker(15 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				notifySystemd("WATCHDOG=1")
			}
		}
	}()

	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	slog.Info("shutdown signal received")
	notifySystemd("STOPPING=1")
	cancel()

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	_ = apiServer.Shutdown(shutCtx)
	h.Stop()
	slog.Info("av-bridge stopped cleanly", "version", version)
}
