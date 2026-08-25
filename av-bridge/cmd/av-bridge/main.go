package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/kardianos/service"

	"github.com/dloomes/av-bridge/internal/api"
	"github.com/dloomes/av-bridge/internal/cloud"
	"github.com/dloomes/av-bridge/internal/cloud/lens"
	"github.com/dloomes/av-bridge/internal/cloudpoll"
	"github.com/dloomes/av-bridge/internal/cloudpull"
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

// defaultConfigPath returns the platform-appropriate config path when the
// user hasn't overridden it. Linux keeps its historical /etc location so
// deployed collectors don't shift under us. Windows defaults to a
// ProgramData path — the standard "shared per-machine app state" location
// that the Windows Service (LocalSystem) can read even without a user
// profile. macOS is a distant third but included for parity with the
// service library.
func defaultConfigPath() string {
	switch runtime.GOOS {
	case "windows":
		pd := os.Getenv("ProgramData")
		if pd == "" {
			pd = `C:\ProgramData`
		}
		return filepath.Join(pd, "av-bridge", "config.yaml")
	case "darwin":
		return "/Library/Application Support/av-bridge/config.yaml"
	default:
		return "/etc/av-bridge/config.yaml"
	}
}

func main() {
	configPath := flag.String("config", defaultConfigPath(), "path to config file")
	envPath := flag.String("env", "", "path to env file (KEY=VALUE format); if empty, looks for poc.env or .env next to the config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	validate := flag.Bool("validate", false, "validate config and exit (0 if valid, 1 if not)")
	// -service <cmd> plumbs the binary into kardianos/service. Empty (the
	// default) means "run in the foreground the way the bridge always
	// has" — safe for existing systemd units and docker containers that
	// invoke the binary directly. Recognised values:
	//   install / uninstall — register/deregister the service with the OS
	//   start / stop / restart — talk to an installed service
	//   status              — print current service state
	//   run                 — used by the OS service host to drive the
	//                         program; not typically invoked by hand
	svcCmd := flag.String("service", "", "control the OS service: install|uninstall|start|stop|restart|status|run")
	flag.Parse()

	if *showVersion {
		fmt.Printf("av-bridge version %s (built %s)\n", version, buildTime)
		return
	}

	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)

	// The kardianos/service library wants the Program interface hooked
	// up before we've done any config work — install/uninstall don't
	// need a valid config to run, and Start/Stop are async callbacks
	// that shouldn't block the service host. Program.run does the real
	// startup once Start is invoked by the platform service manager.
	prog := &program{
		configPath: *configPath,
		envPath:    *envPath,
		validate:   *validate,
	}

	svcConfig := &service.Config{
		Name:        "av-bridge",
		DisplayName: "AV Bridge Collector",
		Description: "On-prem collector that ships AV device telemetry to the av-bridge cloud.",
		// -config and -env flags forwarded to the service host invocation
		// so an admin who installed from a non-default location doesn't
		// have to hand-edit the service definition afterwards.
		Arguments: buildServiceArgs(*configPath, *envPath),
	}

	s, err := service.New(prog, svcConfig)
	if err != nil {
		slog.Error("service init failed", "error", err)
		os.Exit(1)
	}
	prog.svc = s

	// Explicit sub-command from -service takes precedence over the
	// "am I running inside a service host?" auto-detect below. This lets
	// an installer script drive install/start deterministically without
	// worrying about which platform detection kicks in.
	if *svcCmd != "" {
		if *svcCmd == "run" {
			// "run" bypasses the platform-detect and unconditionally
			// launches the service loop — handy for foreground debugging
			// on Windows where interactive-vs-service detection has
			// historically been flaky.
			if err := s.Run(); err != nil {
				slog.Error("service run failed", "error", err)
				os.Exit(1)
			}
			return
		}
		if err := service.Control(s, *svcCmd); err != nil {
			slog.Error("service control failed", "command", *svcCmd, "error", err)
			os.Exit(1)
		}
		fmt.Printf("service %s: ok\n", *svcCmd)
		return
	}

	// No sub-command: hand off to the service library which:
	//   - when running under an OS service host (Windows Service, systemd
	//     with Type=notify, launchd), invokes Program.Start / Program.Stop
	//     via IPC callbacks; the platform owns the process lifecycle.
	//   - when running interactively (developer terminal, docker), calls
	//     Program.Start once and then blocks in a signal loop, cleaning
	//     up with Program.Stop on SIGINT/SIGTERM.
	// Either way Start returns fast — the real work happens in run().
	if err := s.Run(); err != nil {
		slog.Error("service run failed", "error", err)
		os.Exit(1)
	}
}

// buildServiceArgs builds the argument list that gets baked into the
// service registration. Only non-default values are forwarded so the
// Windows Service Manager / systemd unit stays readable.
func buildServiceArgs(configPath, envPath string) []string {
	args := []string{}
	if configPath != "" && configPath != defaultConfigPath() {
		args = append(args, "-config", configPath)
	}
	if envPath != "" {
		args = append(args, "-env", envPath)
	}
	return args
}

// program is the kardianos/service.Interface implementation. Start /
// Stop are the two hooks the service host calls; both must return
// quickly (Windows Service Manager gives Start ~30s before killing the
// process on timeout, and Stop needs to return before the service host
// kills us anyway).
type program struct {
	configPath string
	envPath    string
	validate   bool

	svc service.Service

	// The run loop owns its own context so Stop can cancel it. mu guards
	// cancel / stopped for the case where the service host calls Stop
	// before Start's goroutine has fully assigned them.
	mu       sync.Mutex
	cancel   context.CancelFunc
	stopped  chan struct{}
}

// Start returns immediately — long-running work goes into a goroutine.
// The service host treats a slow Start as a failure and kills the
// process, so anything that could block (config load, hub start) has to
// happen off this critical path.
func (p *program) Start(_ service.Service) error {
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})

	p.mu.Lock()
	p.cancel = cancel
	p.stopped = stopped
	p.mu.Unlock()

	go func() {
		defer close(stopped)
		if err := p.run(ctx); err != nil {
			slog.Error("run failed", "error", err)
			// On a fatal error we cancel our own context so Stop-side
			// callers know we're done. The service host will notice the
			// process exit and restart per its RestartPolicy.
			p.mu.Lock()
			if p.cancel != nil {
				p.cancel()
			}
			p.mu.Unlock()
		}
	}()
	return nil
}

// Stop cancels the run loop and waits for it to drain. Bounded wait so
// a hung shutdown doesn't outlast the OS service host's kill timer.
func (p *program) Stop(_ service.Service) error {
	p.mu.Lock()
	cancel := p.cancel
	stopped := p.stopped
	p.mu.Unlock()

	if cancel != nil {
		cancel()
	}
	if stopped == nil {
		return nil
	}
	select {
	case <-stopped:
	case <-time.After(20 * time.Second):
		return errors.New("timed out waiting for bridge to stop")
	}
	return nil
}

// run is the body of the old main() — everything from config load
// through the signal loop. Broken out so Program.Start can drive it via
// a background context, and so foreground signal handling composes
// cleanly with service-host lifecycle callbacks.
func (p *program) run(ctx context.Context) error {
	resolvedEnv := p.envPath
	if resolvedEnv == "" {
		dir := filepath.Dir(p.configPath)
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
			return fmt.Errorf("load env file %s: %w", resolvedEnv, err)
		}
		slog.Info("loaded env file", "path", resolvedEnv)
	}

	cfg, err := config.Load(p.configPath)
	if err != nil {
		if p.validate {
			fmt.Fprintf(os.Stderr, "config invalid: %v\n", err)
			os.Exit(1)
		}
		return fmt.Errorf("load config %s: %w", p.configPath, err)
	}

	if p.validate {
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

	st, err := store.New(cfg.Hub.StorePath)
	if err != nil {
		return fmt.Errorf("open state store %s: %w", cfg.Hub.StorePath, err)
	}

	cloudClient := cloud.NewClient(cfg.Cloud, cfg.Hub.CollectorID, cfg.Hub.SiteID, version, buildTime)

	var lensClient *lens.Client
	if cfg.Lens.Enabled {
		if cfg.Lens.ClientID == "" || cfg.Lens.ClientSecret == "" {
			return errors.New("lens enabled but client_id or client_secret missing")
		}
		lensClient = lens.NewClient(cfg.Lens.ClientID, cfg.Lens.ClientSecret)
		slog.Info("lens enrichment enabled")

		go func() {
			schemaCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			schema, err := lensClient.Introspect(schemaCtx)
			if err != nil {
				slog.Warn("lens introspection failed — schema discovery skipped", "error", err)
			} else {
				slog.Info("lens schema discovered", "schema", schema)
			}

			total, sample, sErr := lensClient.SampleDevices(schemaCtx, 10)
			if sErr != nil {
				slog.Warn("lens sample query failed", "error", sErr)
				return
			}
			slog.Info("lens visible devices", "total", total, "first_n", sample)
		}()
	}

	h := hub.New(cfg, cloudClient, lensClient, st)

	authCfg := api.AuthConfig{
		Enabled:    cfg.API.Auth.Enabled,
		APIKeys:    cfg.API.Auth.APIKeys,
		HMACSecret: cfg.API.Auth.HMACSecret,
	}
	apiServer := api.New(cfg.Hub.ListenAddr, h, cloudClient, authCfg)
	h.SetEventBroadcaster(apiServer)

	if err := h.Start(ctx); err != nil {
		return fmt.Errorf("hub start: %w", err)
	}

	cmdPoller := cloudpoll.NewPoller(cfg.Cloud, cfg.Hub.CollectorID, h)
	go cmdPoller.Run(ctx)

	cfgPuller := cloudpull.NewPoller(cfg.Cloud, cfg.Hub.DeviceSyncInterval, cfg.Hub.CollectorID, version, buildTime, h)
	go cfgPuller.Run(ctx)

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

	// Two shutdown signals compose here:
	//   1. Interactive/foreground:  SIGINT/SIGTERM (Ctrl+C on any OS)
	//   2. Service-host:            ctx.Done() when Program.Stop cancels it
	// Whichever fires first wins; the other becomes a no-op.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-quit:
		slog.Info("shutdown signal received")
	case <-ctx.Done():
		slog.Info("service host requested stop")
	}
	notifySystemd("STOPPING=1")

	shutCtx, shutCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer shutCancel()
	_ = apiServer.Shutdown(shutCtx)
	h.Stop()
	slog.Info("av-bridge stopped cleanly", "version", version)
	return nil
}
