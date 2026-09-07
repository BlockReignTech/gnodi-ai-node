// Package identity manages the node's Ed25519 device key.
//
// The key proves to the gateway that this machine is the one that first
// connected with the operator's licence key. Per the agent protocol, the licence
// key crosses the wire only during the handshake; every later reconnect is
// authenticated by signing the gateway's nonce with this key.
package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Key is the node's device keypair.
type Key struct {
	priv ed25519.PrivateKey
}

// FileName is the key file's name inside the state directory.
const FileName = "device.key"

// LoadOrCreate reads the device key from stateDir, generating one on first run.
//
// The file is written 0600 and the directory 0700: anyone who can read this key
// can impersonate the node to the gateway and collect its work.
func LoadOrCreate(stateDir string) (*Key, error) {
	if stateDir == "" {
		return nil, errors.New("state directory is required")
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return nil, fmt.Errorf("create state dir: %w", err)
	}
	path := filepath.Join(stateDir, FileName)

	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		decoded, derr := base64.StdEncoding.DecodeString(string(raw))
		if derr != nil || len(decoded) != ed25519.PrivateKeySize {
			return nil, fmt.Errorf("device key at %s is corrupt; delete it to re-enrol", path)
		}
		return &Key{priv: ed25519.PrivateKey(decoded)}, nil
	case errors.Is(err, os.ErrNotExist):
		_, priv, gerr := ed25519.GenerateKey(rand.Reader)
		if gerr != nil {
			return nil, fmt.Errorf("generate device key: %w", gerr)
		}
		encoded := base64.StdEncoding.EncodeToString(priv)
		if werr := os.WriteFile(path, []byte(encoded), 0o600); werr != nil {
			return nil, fmt.Errorf("write device key: %w", werr)
		}
		return &Key{priv: priv}, nil
	default:
		return nil, fmt.Errorf("read device key: %w", err)
	}
}

// PublicKeyBase64 is the raw 32-byte public key, base64-encoded, as the auth
// frame carries it.
func (k *Key) PublicKeyBase64() string {
	pub := k.priv.Public().(ed25519.PublicKey)
	return base64.StdEncoding.EncodeToString(pub)
}

// SignBase64 signs msg and returns the base64 signature.
func (k *Key) SignBase64(msg []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(k.priv, msg))
}
