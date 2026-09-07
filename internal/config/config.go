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
	// Engine and VramGb are advertised capability hints (optional).
	Engine string
	VramGb int
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
		HeartbeatInterval: 5 * time.Minute,
	}
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
	if len(c.Models) == 0 {
		problems = append(problems, "MODELS is required")
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
