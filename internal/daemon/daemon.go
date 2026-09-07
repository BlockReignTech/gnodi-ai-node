// Package daemon orchestrates the node lifecycle: dial the gateway and serve
// jobs pushed down that socket, register and heartbeat with NodeSvc, and expose
// a local status page.
//
// The daemon does not listen for inbound inference. Under ADR-002 the node
// dials out, which is what lets a machine behind residential NAT serve traffic
// and removes the unauthenticated public endpoint the previous design exposed.
package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/blockreigntech/gnodi-ai-node/internal/agent"
	"github.com/blockreigntech/gnodi-ai-node/internal/chain"
	"github.com/blockreigntech/gnodi-ai-node/internal/config"
	"github.com/blockreigntech/gnodi-ai-node/internal/identity"
	"github.com/blockreigntech/gnodi-ai-node/internal/inference"
	"github.com/blockreigntech/gnodi-ai-node/internal/manifest"
	"github.com/blockreigntech/gnodi-ai-node/internal/nodesvc"
)

// Daemon ties together the gateway agent, the local inference backend, the
// NodeSvc client, and the optional chain client.
type Daemon struct {
	cfg    config.Config
	ns     *nodesvc.Client
	proxy  *inference.Proxy
	chain  *chain.Client // nil if ChainRPC not configured
	key    *identity.Key
	agent  *agent.Client
	models *manifest.Manager // nil when the manifest is disabled

	mu      sync.Mutex
	catalog manifest.Catalog
	version int
}

// New builds a Daemon, loading or creating the device key.
func New(cfg config.Config) (*Daemon, error) {
	key, err := identity.LoadOrCreate(cfg.StateDir)
	if err != nil {
		return nil, err
	}

	hc := &http.Client{Timeout: 10 * time.Minute} // generation can be slow
	proxy := inference.NewProxy(cfg.InferenceURL, cfg.Models, hc)

	d := &Daemon{
		cfg:   cfg,
		ns:    nodesvc.New(cfg.NodeSvcURL, cfg.LicenseKey, &http.Client{Timeout: 30 * time.Second}),
		proxy: proxy,
		key:   key,
	}
	if cfg.ChainRPC != "" {
		d.chain = chain.New(cfg.ChainRPC, &http.Client{Timeout: 10 * time.Second})
	}
	if cfg.ManifestEnabled() {
		d.models = &manifest.Manager{
			URL:       cfg.ManifestURL,
			PublicKey: cfg.ManifestPubKey,
			VramGb:    cfg.VramGb,
			Allow:     cfg.Models, // optional operator narrowing
			AutoPull:  cfg.AutoPull,
			HTTP:      &http.Client{Timeout: 60 * time.Minute}, // pulls are large
			Ollama:    manifest.NewOllama(cfg.OllamaURL, &http.Client{Timeout: 60 * time.Minute}),
			Logger:    log.Default(),
		}
	}

	d.agent = agent.New(agent.Config{
		GatewayURL:     cfg.GatewayURL,
		LicenseKey:     cfg.LicenseKey,
		NodeVersion:    cfg.NodeVersion,
		Models:         cfg.Models,
		MaxConcurrency: cfg.MaxConcurrency,
		Engine:         cfg.Engine,
		VramGb:         cfg.VramGb,
	}, key, proxy, log.Default())
	return d, nil
}

// Handler returns the daemon's local routes. Bound to loopback: it reports what
// this node is doing and is not intended to face the network.
func (d *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", d.dashboard)
	mux.HandleFunc("GET /health", d.health)
	mux.HandleFunc("GET /status", d.status)
	return mux
}

// Register activates the node and reports its capabilities to NodeSvc.
//
// The endpoint field is empty by design — there is no inbound endpoint any
// more. The gateway learns what this node serves over the agent socket; NodeSvc
// keeps the record for licensing and the Cosmos-side distributor.
func (d *Daemon) Register(ctx context.Context) error {
	if err := d.ns.Activate(ctx, d.cfg.NodeVersion); err != nil {
		return err
	}
	return d.ns.ReportCapabilities(ctx, "", d.proxy.Models())
}

// HeartbeatOnce sends a single NodeSvc heartbeat with best-effort chain metrics.
//
// Liveness for *routing* comes from the agent socket, not from this: the socket
// being open is a continuous signal where a five-minute poll is a coarse one.
// This heartbeat remains for the licensing and emission-reward side.
func (d *Daemon) HeartbeatOnce(ctx context.Context) error {
	var m nodesvc.Metrics
	if d.chain != nil {
		m = d.chain.Metrics(ctx)
	}
	return d.ns.Heartbeat(ctx, d.cfg.NodeVersion, m)
}

// SyncModels fetches the signed catalog, pulls what this machine can run, and
// points the inference proxy at the result.
//
// Runs before the node connects: advertising a model the engine cannot actually
// serve would have the gateway route work here that is certain to fail, which
// costs the operator their success rate for nothing.
func (d *Daemon) SyncModels(ctx context.Context) error {
	if d.models == nil {
		return nil // manifest disabled; MODELS is authoritative
	}
	if d.models.VramGb <= 0 {
		detected := manifest.DetectVramGb(ctx)
		if detected <= 0 {
			return fmt.Errorf(
				"could not detect GPU memory; set VRAM_GB to the card's size in GB, " +
					"or set MANIFEST_DISABLED=true and list MODELS by hand")
		}
		log.Printf("manifest: detected %dGB of VRAM", detected)
		d.models.VramGb = detected
	}

	res, err := d.models.Sync(ctx)
	if err != nil {
		return err
	}
	for id, why := range res.Skipped {
		log.Printf("manifest: skipping %s — %s", id, why)
	}

	d.proxy.SetCatalog(res.Catalog)
	d.mu.Lock()
	d.catalog, d.version = res.Catalog, res.Version
	d.mu.Unlock()

	d.agent.SetModels(d.proxy.Models())
	return nil
}

// Run registers, starts the local status server and the NodeSvc heartbeat, and
// serves gateway jobs until ctx is cancelled.
func (d *Daemon) Run(ctx context.Context) error {
	if err := d.SyncModels(ctx); err != nil {
		return err
	}
	if err := d.Register(ctx); err != nil {
		return err
	}
	log.Printf("registered with NodeSvc as %s", d.cfg.NodeVersion)

	srv := &http.Server{Addr: d.cfg.StatusAddr, Handler: d.Handler()}
	serveErr := make(chan error, 1)
	go func() {
		log.Printf("status page on http://%s (loopback only)", d.cfg.StatusAddr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr <- err
		}
	}()
	defer func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	if err := d.HeartbeatOnce(ctx); err != nil {
		log.Printf("initial heartbeat failed: %v", err) // transient; keep going
	}
	ticker := time.NewTicker(d.cfg.HeartbeatInterval)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := d.HeartbeatOnce(ctx); err != nil {
					log.Printf("heartbeat failed: %v", err)
				}
			}
		}
	}()

	agentErr := make(chan error, 1)
	go func() { agentErr <- d.agent.Run(ctx) }()

	select {
	case <-ctx.Done():
		log.Print("draining before shutdown…")
		d.agent.Drain() // stop taking new work; in-flight jobs finish
		return nil
	case err := <-serveErr:
		return err
	case err := <-agentErr:
		if errors.Is(err, context.Canceled) {
			return nil
		}
		return err
	}
}

func (d *Daemon) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{
		"status":  "ok",
		"version": d.cfg.NodeVersion,
		"models":  d.proxy.Models(),
	})
}

func (d *Daemon) status(w http.ResponseWriter, _ *http.Request) {
	d.mu.Lock()
	catalog, version := d.catalog, d.version
	d.mu.Unlock()

	writeJSON(w, map[string]any{
		"version":         d.cfg.NodeVersion,
		"gateway":         d.cfg.GatewayURL,
		"models":          d.proxy.Models(),
		"catalog":         catalog,
		"manifestVersion": version,
		"vramGb":          d.modelsVram(),
		"maxConcurrency":  d.cfg.MaxConcurrency,
		"inFlight":        d.agent.InFlight(),
		"devicePubkey":    d.key.PublicKeyBase64(),
	})
}

func (d *Daemon) modelsVram() int {
	if d.models != nil {
		return d.models.VramGb
	}
	return d.cfg.VramGb
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
