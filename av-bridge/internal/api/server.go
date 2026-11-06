package api

import (
	"strings"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/dloomes/av-bridge/internal/cloud"
	"github.com/dloomes/av-bridge/internal/device"
	"github.com/dloomes/av-bridge/internal/hub"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"
)

var upgrader = websocket.Upgrader{
	CheckOrigin: checkSameOrigin,
}

// Server is the local HTTP API. It exposes:
//
//	GET  /api/v1/devices                  - list all devices and their status
//	GET  /api/v1/devices/{id}             - get one device
//	POST /api/v1/devices/{id}/command     - send a command to a device
//	GET  /api/v1/devices/{id}/telemetry   - poll device now
//	POST /api/v1/devices/{id}/reconnect   - force reconnect a device
//	GET  /api/v1/status                   - hub-wide summary
//	GET  /ws/events                       - WebSocket stream of all device events
type Server struct {
	hub         *hub.Hub
	cloud       *cloud.Client
	auth        AuthConfig
	srv         *http.Server
	subMu       sync.RWMutex
	subscribers map[chan *device.Event]struct{}
}

func New(listenAddr string, h *hub.Hub, c *cloud.Client, auth AuthConfig) *Server {
	s := &Server{
		hub:         h,
		cloud:       c,
		auth:        auth,
		subscribers: make(map[chan *device.Event]struct{}),
	}

	if effective, forced := safeListenAddr(listenAddr, auth); forced {
		slog.Error("local API refused to listen on the network without authentication; "+
			"set api.auth.enabled: true with at least one api.auth.api_keys entry to use hub.listen_addr, "+
			"listening on loopback only",
			"hub.listen_addr", listenAddr, "listening", effective)
		listenAddr = effective
	}

	r := mux.NewRouter()

	// Apply middleware stack: recovery → logging → auth
	r.Use(recoveryMiddleware)
	r.Use(loggingMiddleware)
	r.Use(browserGuard(auth))
	r.Use(authMiddleware(auth))

	r.HandleFunc("/healthz", s.healthz).Methods(http.MethodGet)
	r.HandleFunc("/metrics", s.metricsHandler).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/status", s.hubStatus).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/devices", s.listDevices).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/devices/{id}", s.getDevice).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/devices/{id}/command", s.sendCommand).Methods(http.MethodPost)
	r.HandleFunc("/api/v1/devices/{id}/telemetry", s.getTelemetry).Methods(http.MethodGet)
	r.HandleFunc("/api/v1/devices/{id}/reconnect", s.reconnectDevice).Methods(http.MethodPost)
	r.HandleFunc("/ws/events", s.wsEvents)

	s.srv = &http.Server{
		Addr:         listenAddr,
		Handler:      r,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	return s
}

// Addr is the address the API actually listens on, after the fail-closed
// check in New may have moved it to loopback.
func (s *Server) Addr() string { return s.srv.Addr }

func (s *Server) Start() error {
	slog.Info("API server listening", "addr", s.srv.Addr)
	return s.srv.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	return s.srv.Shutdown(ctx)
}

// BroadcastEvent forwards a device event to all connected WebSocket clients.
// Non-blocking: slow subscribers drop frames rather than backing up the hub.
func (s *Server) BroadcastEvent(e *device.Event) {
	s.subMu.RLock()
	defer s.subMu.RUnlock()
	for ch := range s.subscribers {
		select {
		case ch <- e:
		default:
		}
	}
}

func (s *Server) subscribe() chan *device.Event {
	ch := make(chan *device.Event, 64)
	s.subMu.Lock()
	s.subscribers[ch] = struct{}{}
	s.subMu.Unlock()
	return ch
}

func (s *Server) unsubscribe(ch chan *device.Event) {
	s.subMu.Lock()
	delete(s.subscribers, ch)
	s.subMu.Unlock()
}

// ---- Handlers ----

func (s *Server) listDevices(w http.ResponseWriter, r *http.Request) {
	devs := s.hub.Devices()
	type item struct {
		ID       string            `json:"id"`
		Name     string            `json:"name"`
		Type     string            `json:"type"`
		Protocol string            `json:"protocol"`
		Location string            `json:"location"`
		Address  string            `json:"address,omitempty"`
		Status   device.Status     `json:"status"`
		Tags     map[string]string `json:"tags,omitempty"`
	}
	out := make([]item, 0, len(devs))
	for _, d := range devs {
		info := d.Info()
		out = append(out, item{
			ID:       info.ID,
			Name:     info.Name,
			Type:     info.Type,
			Protocol: info.Protocol,
			Location: info.Location,
			Address:  info.Address,
			Status:   d.Status(),
			Tags:     info.Tags,
		})
	}
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) getDevice(w http.ResponseWriter, r *http.Request) {
	dev := s.hub.GetDevice(mux.Vars(r)["id"])
	if dev == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}
	info := dev.Info()
	writeJSON(w, http.StatusOK, map[string]any{
		"id":       info.ID,
		"name":     info.Name,
		"type":     info.Type,
		"protocol": info.Protocol,
		"location": info.Location,
		"address":  info.Address,
		"status":   dev.Status(),
		"tags":     info.Tags,
		"commands": info.Commands,
	})
}

func (s *Server) sendCommand(w http.ResponseWriter, r *http.Request) {
	dev := s.hub.GetDevice(mux.Vars(r)["id"])
	if dev == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}

	// JSON only: a browser can't send application/json cross-site without a
	// CORS preflight, which this API never grants.
	if ct := r.Header.Get("Content-Type"); !strings.HasPrefix(strings.ToLower(strings.TrimSpace(ct)), "application/json") {
		writeJSON(w, http.StatusUnsupportedMediaType, map[string]string{"error": "Content-Type must be application/json"})
		return
	}
	var req device.CommandRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request: " + err.Error()})
		return
	}

	resp, err := dev.SendCommand(r.Context(), req)
	if err != nil {
		slog.Error("command failed", "device", dev.ID(), "command", req.Name, "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) getTelemetry(w http.ResponseWriter, r *http.Request) {
	dev := s.hub.GetDevice(mux.Vars(r)["id"])
	if dev == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}

	tel, err := dev.Poll(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}

	writeJSON(w, http.StatusOK, tel)
}

func (s *Server) wsEvents(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Warn("ws upgrade failed", "error", err)
		return
	}
	defer conn.Close()

	ch := s.subscribe()
	defer s.unsubscribe(ch)

	slog.Info("ws client connected", "remote", r.RemoteAddr)

	for {
		select {
		case e := <-ch:
			if err := conn.WriteJSON(e); err != nil {
				slog.Info("ws client disconnected", "remote", r.RemoteAddr)
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (s *Server) healthz(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "time": time.Now().UTC().Format(time.RFC3339)})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) reconnectDevice(w http.ResponseWriter, r *http.Request) {
	dev := s.hub.GetDevice(mux.Vars(r)["id"])
	if dev == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
		return
	}
	_ = dev.Disconnect()
	if err := dev.Connect(r.Context()); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "reconnected", "device_id": dev.ID()})
}

func (s *Server) hubStatus(w http.ResponseWriter, r *http.Request) {
	devs := s.hub.Devices()
	online, offline := 0, 0
	for _, d := range devs {
		switch d.Status() {
		case device.StatusOnline:
			online++
		case device.StatusOffline:
			offline++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"total":   len(devs),
		"online":  online,
		"offline": offline,
		"time":    time.Now().UTC().Format(time.RFC3339),
	})
}

// StartTLS starts the API server with TLS
func (s *Server) StartTLS(certFile, keyFile string) error {
	slog.Info("API server (TLS) listening", "addr", s.srv.Addr)
	return s.srv.ListenAndServeTLS(certFile, keyFile)
}
