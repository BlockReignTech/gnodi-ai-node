package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "node.env")
	// Comments, blanks, padding, and a value containing both "=" and "//" --
	// FALLBACK-style URLs and model maps are full of them, and a parser that
	// splits on every "=" mangles them.
	if err := os.WriteFile(path, []byte(`
# Gnodi AI Node
LICENSE_KEY=CMZA-ZFJU-ALFL-C36C
GATEWAY_URL = wss://gw.gnodi-ai.com/v1/agent

MODELS=gnodi/llama3.1-8b,gnodi/mistral-7b
  MAX_CONCURRENCY=2
no-equals-sign
=novalue
`), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"LICENSE_KEY", "GATEWAY_URL", "MODELS", "MAX_CONCURRENCY"} {
		os.Unsetenv(k)
	}

	if err := loadEnvFile(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	for k, want := range map[string]string{
		"LICENSE_KEY":     "CMZA-ZFJU-ALFL-C36C",
		"GATEWAY_URL":     "wss://gw.gnodi-ai.com/v1/agent",
		"MODELS":          "gnodi/llama3.1-8b,gnodi/mistral-7b",
		"MAX_CONCURRENCY": "2",
	} {
		if got := os.Getenv(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestLoadEnvFileDoesNotOverrideTheShell(t *testing.T) {
	// A variable already set wins, so a terminal can still override the file --
	// which is how the AWS node pins NODE_VERSION, and how anyone debugging
	// points a node at a different gateway for one run.
	dir := t.TempDir()
	path := filepath.Join(dir, "node.env")
	if err := os.WriteFile(path, []byte("GATEWAY_URL=wss://from-the-file/v1/agent\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GATEWAY_URL", "wss://from-the-shell/v1/agent")

	if err := loadEnvFile(path); err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := os.Getenv("GATEWAY_URL"); got != "wss://from-the-shell/v1/agent" {
		t.Errorf("GATEWAY_URL = %q, want the shell's value", got)
	}
}

func TestLoadEnvFileSaysWhatIsMissing(t *testing.T) {
	// Double-clicked with no config beside it is the most likely first-run
	// failure, and with no console the message only exists in the log.
	err := loadEnvFile(filepath.Join(t.TempDir(), "absent.env"))
	if err == nil {
		t.Fatal("expected an error naming the missing file")
	}
	if got := err.Error(); !strings.Contains(got, "node.env") {
		t.Errorf("error %q should name node.env", got)
	}
}
