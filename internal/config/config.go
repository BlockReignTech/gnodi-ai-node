// Package config loads and validates the node daemon configuration from the
// environment.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Config is the resolved node-daemon configuration.
type Config struct {
	// LicenseKey is the operator's AI Node License key (XXXX-XXXX-XXXX-XXXX).
	LicenseKey string
	// NodeSvcURL is the base URL of gnodi-sdk NodeSvc (serves /nodes/*).
	NodeSvcURL string
	// InferenceURL is the OpenAI-compatible backend base; the daemon POSTs to
	// InferenceURL + "/chat/completions" (e.g. http://localhost:11434/v1 for Ollama).
	InferenceURL string
	// AdvertiseEndpoint is the public URL the gateway reaches this node at; the
	// gateway calls AdvertiseEndpoint + "/v1/chat/completions". Reported to NodeSvc.
	AdvertiseEndpoint string
	// Models this node serves (allowlist + reported to NodeSvc).
	Models []string
	// ListenAddr is where the daemon's HTTP server binds (default ":8080").
	ListenAddr string
	// NodeVersion is reported on activate/heartbeat.
	NodeVersion string
	// HeartbeatInterval between heartbeats (default 5m).
	HeartbeatInterval time.Duration
	// ChainRPC is an optional CometBFT RPC base for block-height heartbeat metrics.
	ChainRPC string
}

// Load reads configuration from the environment and validates it.
func Load() (Config, error) {
	c := Config{
		LicenseKey:        os.Getenv("LICENSE_KEY"),
		NodeSvcURL:        trimURL(os.Getenv("NODESVC_URL")),
		InferenceURL:      trimURL(os.Getenv("INFERENCE_URL")),
		AdvertiseEndpoint: trimURL(os.Getenv("ADVERTISE_ENDPOINT")),
		Models:            splitCSV(os.Getenv("MODELS")),
		ListenAddr:        getenv("LISTEN_ADDR", ":8080"),
		NodeVersion:       getenv("NODE_VERSION", "dev"),
		ChainRPC:          trimURL(os.Getenv("CHAIN_RPC")),
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

// Validate returns an error listing any missing required fields.
func (c Config) Validate() error {
	var missing []string
	if c.LicenseKey == "" {
		missing = append(missing, "LICENSE_KEY")
	}
	if c.NodeSvcURL == "" {
		missing = append(missing, "NODESVC_URL")
	}
	if c.InferenceURL == "" {
		missing = append(missing, "INFERENCE_URL")
	}
	if c.AdvertiseEndpoint == "" {
		missing = append(missing, "ADVERTISE_ENDPOINT")
	}
	if len(c.Models) == 0 {
		missing = append(missing, "MODELS")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing required config: %s", strings.Join(missing, ", "))
	}
	return nil
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
