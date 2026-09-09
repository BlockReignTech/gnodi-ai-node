// Package config loads and validates the node daemon configuration from the
// environment.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// Config is the resolved node-daemon configuration.
type Config struct {
	// LicenseKey is the operator's AI Node License key (XXXX-XXXX-XXXX-XXXX).
	LicenseKey string
	// GatewayURL is the agent WebSocket endpoint the node dials, e.g.
	// wss://gw.gnodi-ai.com/v1/agent. The node never listens for inbound
	// connections, so no public address, DNS name, or certificate is needed.
	GatewayURL string
	// NodeSvcURL is the base URL of gnodi-sdk NodeSvc (serves /nodes/*).
	NodeSvcURL string
	// InferenceURL is the OpenAI-compatible backend base; the daemon POSTs to
	// InferenceURL + "/chat/completions" (e.g. http://localhost:11434/v1 for Ollama).
	InferenceURL string
	// Models this node serves (allowlist + reported to the gateway).
	Models []string
	// MaxConcurrency is how many jobs this node will run at once.
	MaxConcurrency int
	// StateDir holds the device key. Created 0700 on first run.
	StateDir string
	// StatusAddr binds the local status page. Loopback by default — it exposes
	// earnings and job history and is not meant to face the network.
	StatusAddr string
	// NodeVersion is reported on activate/heartbeat and in the agent handshake.
	NodeVersion string
	// HeartbeatInterval between NodeSvc heartbeats (default 5m).
	HeartbeatInterval time.Duration
	// ChainRPC is an optional CometBFT RPC base for block-height metrics.
	ChainRPC string
	// Engine and VramGb are advertised capability hints. VramGb is auto-detected
	// when unset and a supported GPU tool is present.
	Engine string
	VramGb int

	// ManifestURL is the signed catalog endpoint. Derived from GatewayURL when
	// unset; empty disables manifest mode.
	ManifestURL string
	// ManifestPubKey is the base64 Ed25519 key the catalog is verified against.
	// Pinned here rather than fetched: a compromised gateway must not be able to
	// tell the fleet which weights to download.
	ManifestPubKey string
	// OllamaURL is the engine's management API (pull/list), distinct from the
	// OpenAI-compatible surface at InferenceURL.
	OllamaURL string
	// AutoPull downloads catalog models this machine can run.
	AutoPull bool
	// GatewayHTTPURL is the gateway's HTTP base, for reading claim proofs.
	GatewayHTTPURL string
}

// ManifestEnabled reports whether the node manages models from the signed
// catalog rather than from a hand-written MODELS list.
func (c Config) ManifestEnabled() bool {
	return c.ManifestURL != "" && c.ManifestPubKey != ""
}

// DefaultManifestPubKey is the network's manifest signing key, so a build from
// source verifies against the same catalog a released binary does. Release
// builds set it explicitly via -ldflags; MANIFEST_PUBKEY overrides both, which
// is how a local catalog is tested.
//
// A public key, and safe in source. The private half never leaves the signer.
var DefaultManifestPubKey = "5SP2p3zmdGvD523AtowmByyksBPPAB7308jUC6Zu16c="

// gatewayHTTPFrom converts the agent socket URL to the gateway's HTTP base:
// wss://host/v1/agent -> https://host.
func gatewayHTTPFrom(gatewayURL string) string {
	if gatewayURL == "" {
		return ""
	}
	u := strings.TrimSuffix(gatewayURL, "/v1/agent")
	switch {
	case strings.HasPrefix(u, "wss://"):
		return "https://" + strings.TrimPrefix(u, "wss://")
	case strings.HasPrefix(u, "ws://"):
		return "http://" + strings.TrimPrefix(u, "ws://")
	default:
		return ""
	}
}

// manifestURLFrom derives the catalog endpoint from the agent socket URL:
// wss://host/v1/agent -> https://host/v1/manifest.
func manifestURLFrom(gatewayURL string) string {
	base := gatewayHTTPFrom(gatewayURL)
	if base == "" {
		return ""
	}
	return base + "/v1/manifest"
}

// Load reads configuration from the environment and validates it.
func Load() (Config, error) {
	c := Config{
		LicenseKey:        os.Getenv("LICENSE_KEY"),
		GatewayURL:        trimURL(os.Getenv("GATEWAY_URL")),
		NodeSvcURL:        trimURL(os.Getenv("NODESVC_URL")),
		InferenceURL:      trimURL(os.Getenv("INFERENCE_URL")),
		Models:            splitCSV(os.Getenv("MODELS")),
		MaxConcurrency:    atoiDefault(os.Getenv("MAX_CONCURRENCY"), 1),
		StateDir:          getenv("STATE_DIR", defaultStateDir()),
		StatusAddr:        getenv("STATUS_ADDR", "127.0.0.1:8080"),
		NodeVersion:       getenv("NODE_VERSION", "dev"),
		ChainRPC:          trimURL(os.Getenv("CHAIN_RPC")),
		Engine:            getenv("ENGINE", "ollama"),
		VramGb:            atoiDefault(os.Getenv("VRAM_GB"), 0),
		ManifestPubKey:    getenv("MANIFEST_PUBKEY", DefaultManifestPubKey),
		AutoPull:          boolDefault(os.Getenv("AUTO_PULL"), true),
		HeartbeatInterval: 5 * time.Minute,
	}
	c.ManifestURL = getenv("MANIFEST_URL", manifestURLFrom(c.GatewayURL))
	if os.Getenv("MANIFEST_DISABLED") == "true" {
		c.ManifestURL = ""
	}
	c.OllamaURL = getenv("OLLAMA_URL", strings.TrimSuffix(c.InferenceURL, "/v1"))
	c.GatewayHTTPURL = getenv("GATEWAY_HTTP_URL", gatewayHTTPFrom(c.GatewayURL))
	if v := os.Getenv("HEARTBEAT_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			return c, fmt.Errorf("invalid HEARTBEAT_INTERVAL %q: %w", v, err)
		}
		c.HeartbeatInterval = d
	}
	return c, c.Validate()
}

// Validate returns an error listing any missing or unusable fields.
func (c Config) Validate() error {
	var problems []string
	if c.LicenseKey == "" {
		problems = append(problems, "LICENSE_KEY is required")
	}
	if c.GatewayURL == "" {
		problems = append(problems, "GATEWAY_URL is required")
	} else if !strings.HasPrefix(c.GatewayURL, "ws://") && !strings.HasPrefix(c.GatewayURL, "wss://") {
		problems = append(problems, "GATEWAY_URL must start with ws:// or wss://")
	}
	if c.NodeSvcURL == "" {
		problems = append(problems, "NODESVC_URL is required")
	}
	if c.InferenceURL == "" {
		problems = append(problems, "INFERENCE_URL is required")
	}
	// With the signed catalog on, MODELS is an optional allowlist: the node
	// serves whatever its card can hold unless the operator narrows it.
	if len(c.Models) == 0 && !c.ManifestEnabled() {
		problems = append(problems, "MODELS is required when the model manifest is disabled")
	}
	if c.MaxConcurrency < 1 {
		problems = append(problems, "MAX_CONCURRENCY must be at least 1")
	}
	if os.Getenv("ADVERTISE_ENDPOINT") != "" {
		// Removed in ADR-002: the node dials out, so it needs no public address.
		// Failing loudly beats silently ignoring a variable someone set on purpose.
		problems = append(problems,
			"ADVERTISE_ENDPOINT is no longer used — the node dials the gateway; set GATEWAY_URL instead")
	}
	if len(problems) > 0 {
		return fmt.Errorf("invalid configuration:\n  - %s", strings.Join(problems, "\n  - "))
	}
	return nil
}

func defaultStateDir() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ".gnodi-ai-node"
	}
	return filepath.Join(home, ".gnodi-ai-node")
}

func splitCSV(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func trimURL(s string) string { return strings.TrimRight(strings.TrimSpace(s), "/") }

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// boolDefault parses an env flag, falling back to def for anything unset or
// unrecognised.
func boolDefault(s string, def bool) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return def
	case "false", "0", "no", "off":
		return false
	case "true", "1", "yes", "on":
		return true
	default:
		return def
	}
}

func atoiDefault(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return def
	}
	return n
}
