package identity

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadOrCreate_GeneratesThenReuses(t *testing.T) {
	dir := t.TempDir()

	first, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	second, err := LoadOrCreate(dir)
	if err != nil {
		t.Fatalf("reload: %v", err)
	}

	// The device key must survive restarts — a new key on every boot would be
	// rejected by the gateway as a key mismatch.
	if first.PublicKeyBase64() != second.PublicKeyBase64() {
		t.Error("key changed across reloads")
	}
}

func TestLoadOrCreate_KeyFileIsPrivate(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreate(dir); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, FileName))
	if err != nil {
		t.Fatal(err)
	}
	// Anyone who can read this key can impersonate the node and take its work.
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Errorf("key file mode = %o, want 600", perm)
	}
}

func TestKey_SignatureVerifiesAgainstPublicKey(t *testing.T) {
	k, err := LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("gateway nonce")
	sig, err := base64.StdEncoding.DecodeString(k.SignBase64(msg))
	if err != nil {
		t.Fatal(err)
	}
	pub, err := base64.StdEncoding.DecodeString(k.PublicKeyBase64())
	if err != nil {
		t.Fatal(err)
	}
	if len(pub) != ed25519.PublicKeySize {
		t.Fatalf("public key is %d bytes, want %d", len(pub), ed25519.PublicKeySize)
	}
	if !ed25519.Verify(pub, msg, sig) {
		t.Error("signature does not verify against the reported public key")
	}
	if ed25519.Verify(pub, []byte("different nonce"), sig) {
		t.Error("signature verified against the wrong message")
	}
}

func TestLoadOrCreate_RejectsCorruptKey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, FileName), []byte("not-a-key"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreate(dir); err == nil {
		t.Fatal("expected an error for a corrupt key file")
	}
}

func TestLoadOrCreate_RequiresStateDir(t *testing.T) {
	if _, err := LoadOrCreate(""); err == nil {
		t.Fatal("expected an error for an empty state dir")
	}
}
