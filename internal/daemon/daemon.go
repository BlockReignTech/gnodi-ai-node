// Package daemon orchestrates the node lifecycle: serve OpenAI-compatible
// inference, register with NodeSvc (activate + report capabilities), and heartbeat.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/blockreigntech/gnodi-ai-node/internal/chain"
	"github.com/blockreigntech/gnodi-ai-node/internal/config"
	"github.com/blockreigntech/gnodi-ai-node/internal/inference"
	"github.com/blockreigntech/gnodi-ai-node/internal/nodesvc"
)

// Daemon ties together the inference proxy, NodeSvc client, and optional chain client.
type Daemon struct {
	cfg   config.Config
	ns    *nodesvc.Client
	proxy *inference.Proxy
	chain *chain.Client // nil if ChainRPC not configured
}

// New builds a Daemon from config.
func New(cfg config.Config) *Daemon {
	hc := &http.Client{Timeout: 60 * time.Second}
	d := &Daemon{
		cfg:   cfg,
		ns:    nodesvc.New(cfg.NodeSvcURL, cfg.LicenseKey, hc),
		proxy: inference.NewProxy(cfg.InferenceURL, cfg.Models, hc),
	}
	if cfg.ChainRPC != "" {
		d.chain = chain.New(cfg.ChainRPC, &http.Client{Timeout: 10 * time.Second})
	}
	return d
}

// Handler returns the daemon's HTTP routes (inference + health).
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/chat/completions", d.proxy.ChatCompletions)
	mux.HandleFunc("GET /health", d.health)
	return mux
}

// Register activates the node and reports its capabilities to NodeSvc.
func (d *Daemon) Register(ctx context.Context) error {
	if err := d.ns.Activate(ctx, d.cfg.NodeVersion); err != nil {
		return err
	}
	return d.ns.ReportCapabilities(ctx, d.cfg.AdvertiseEndpoint, d.cfg.Models)
}

// HeartbeatOnce sends a single heartbeat with best-effort chain metrics.
func (d *Daemon) HeartbeatOnce(ctx context.Context) error {
	var m nodesvc.Metrics
	if d.chain != nil {
		m = d.chain.Metrics(ctx)
	}
	return d.ns.Heartbeat(ctx, d.cfg.NodeVersion, m)
}

// Run starts the HTTP server, registers, and heartbeats until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	srv := &http.Server{Addr: d.cfg.ListenAddr, Handler: d.Handler()}
	serveErr := make(chan error, 1)
	go func() {
		log.Printf("inference server listening on %s (models: %v)", d.cfg.ListenAddr, d.cfg.Models)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()

	if err := d.Register(ctx); err != nil {
		_ = srv.Shutdown(context.Background())
		return err
	}
	log.Printf("registered with NodeSvc; advertising %s", d.cfg.AdvertiseEndpoint)

	if err := d.HeartbeatOnce(ctx); err != nil {
		log.Printf("initial heartbeat failed: %v", err)
	}

	ticker := time.NewTicker(d.cfg.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			log.Print("shutting down…")
			shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return srv.Shutdown(shutCtx)
		case err := <-serveErr:
			return err
		case <-ticker.C:
			if err := d.HeartbeatOnce(ctx); err != nil {
				log.Printf("heartbeat failed: %v", err) // keep serving; transient
			}
		}
	}
}

func (d *Daemon) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":  "ok",
		"version": d.cfg.NodeVersion,
		"models":  d.cfg.Models,
	})
}
