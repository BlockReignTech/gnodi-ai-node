package config

import (
	"testing"
	"time"
)

func setenv(t *testing.T, kv map[string]string) {
	t.Helper()
	for k, v := range kv {
		t.Setenv(k, v)
	}
}

func validEnv() map[string]string {
	return map[string]string{
		"LICENSE_KEY":        "ABCD-EFGH-IJKL-MNOP",
		"NODESVC_URL":        "https://nodes.example.com/",
		"INFERENCE_URL":      "http://localhost:11434/v1/",
		"ADVERTISE_ENDPOINT": "https://node1.example.com/",
		"MODELS":             "llama2-13b, mistral-7b ,",
	}
}

func TestLoad_Valid(t *testing.T) {
	setenv(t, validEnv())
	c, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.NodeSvcURL != "https://nodes.example.com" {
		t.Errorf("trailing slash not trimmed: %q", c.NodeSvcURL)
	}
	if len(c.Models) != 2 || c.Models[0] != "llama2-13b" || c.Models[1] != "mistral-7b" {
		t.Errorf("models not parsed/trimmed: %v", c.Models)
	}
	if c.ListenAddr != ":8080" || c.HeartbeatInterval != 5*time.Minute {
		t.Errorf("defaults wrong: %q %v", c.ListenAddr, c.HeartbeatInterval)
	}
}

func TestLoad_MissingRequired(t *testing.T) {
	setenv(t, map[string]string{"LICENSE_KEY": "x"}) // everything else missing
	_, err := Load()
	if err == nil {
		t.Fatal("expected error for missing required config")
	}
}

func TestLoad_BadHeartbeatInterval(t *testing.T) {
	setenv(t, validEnv())
	t.Setenv("HEARTBEAT_INTERVAL", "not-a-duration")
	if _, err := Load(); err == nil {
		t.Fatal("expected error for bad HEARTBEAT_INTERVAL")
	}
}

func TestLoad_HeartbeatOverride(t *testing.T) {
	setenv(t, validEnv())
	t.Setenv("HEARTBEAT_INTERVAL", "30s")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.HeartbeatInterval != 30*time.Second {
		t.Errorf("got %v", c.HeartbeatInterval)
	}
}
