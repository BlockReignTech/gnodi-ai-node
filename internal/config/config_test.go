package config

import (
	"strings"
	"testing"
	"time"
)

func setenv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func validEnv(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"LICENSE_KEY":   "ABCD-EFGH-IJKL-MNOP",
		"GATEWAY_URL":   "wss://gw.example.com/v1/agent/",
		"NODESVC_URL":   "https://nodes.example.com/",
		"INFERENCE_URL": "http://localhost:11434/v1/",
		"MODELS":        "llama2-13b, mistral-7b ,",
		"STATE_DIR":     t.TempDir(),
	}
}

func TestLoad_Valid(t *testing.T) {
	setenv(t, validEnv(t))
	c, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.NodeSvcURL != "https://nodes.example.com" {
		t.Errorf("trailing slash not trimmed: %q", c.NodeSvcURL)
	}
	if c.GatewayURL != "wss://gw.example.com/v1/agent" {
		t.Errorf("gateway URL not trimmed: %q", c.GatewayURL)
	}
	if len(c.Models) != 2 || c.Models[0] != "llama2-13b" || c.Models[1] != "mistral-7b" {
		t.Errorf("models not parsed/trimmed: %v", c.Models)
	}
	if c.StatusAddr != "127.0.0.1:8080" {
		t.Errorf("status page must default to loopback, got %q", c.StatusAddr)
	}
	if c.MaxConcurrency != 1 || c.HeartbeatInterval != 5*time.Minute || c.Engine != "ollama" {
		t.Errorf("defaults wrong: %d %v %q", c.MaxConcurrency, c.HeartbeatInterval, c.Engine)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	setenv(t, map[string]string{"LICENSE_KEY": "x"}) // everything else missing
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing required config")
	}
	for _, want := range []string{"GATEWAY_URL", "NODESVC_URL", "INFERENCE_URL", "MODELS"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name the missing %s: %v", want, err)
		}
	}
}

// ADVERTISE_ENDPOINT was removed in ADR-002. Someone who still sets it has a
// stale config and a wrong mental model; silently ignoring it would leave them
// wondering why nothing reaches their node.
func TestLoad_RejectsRemovedAdvertiseEndpoint(t *testing.T) {
	setenv(t, validEnv(t))
	t.Setenv("ADVERTISE_ENDPOINT", "https://node1.example.com")
	_, err := Load()
	if err == nil {
		t.Fatal("expected an error for the removed ADVERTISE_ENDPOINT")
	}
	if !strings.Contains(err.Error(), "GATEWAY_URL") {
		t.Errorf("error should point at the replacement: %v", err)
	}
}

func TestLoad_GatewayURLMustBeWebSocket(t *testing.T) {
	setenv(t, validEnv(t))
	t.Setenv("GATEWAY_URL", "https://gw.example.com/v1/agent")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for a non-ws:// gateway URL")
	}
}

func TestLoad_MaxConcurrency(t *testing.T) {
	setenv(t, validEnv(t))
	t.Setenv("MAX_CONCURRENCY", "4")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.MaxConcurrency != 4 {
		t.Errorf("got %d", c.MaxConcurrency)
	}

	t.Setenv("MAX_CONCURRENCY", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected an error for MAX_CONCURRENCY=0")
	}
}

func TestLoad_BadHeartbeatInterval(t *testing.T) {
	setenv(t, validEnv(t))
	t.Setenv("HEARTBEAT_INTERVAL", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for bad HEARTBEAT_INTERVAL")
	}
}

func TestLoad_HeartbeatOverride(t *testing.T) {
	setenv(t, validEnv(t))
	t.Setenv("HEARTBEAT_INTERVAL", "30s")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HeartbeatInterval != 30*time.Second {
		t.Errorf("got %v", c.HeartbeatInterval)
	}
}
