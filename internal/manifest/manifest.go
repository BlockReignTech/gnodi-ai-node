// Package manifest fetches, verifies and applies the network's signed model
// catalog (ADR-002 D-6).
//
// The catalog pins each model id to an exact engine reference and content
// digest, so `gnodi/llama3.1-8b` means the identical weights on every node.
// Without that, two nodes can advertise the same name at different
// quantizations — same price to the user, materially different quality, and the
// router cannot tell them apart.
package manifest

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
)

// Model is one catalog entry.
type Model struct {
	ID            string `json:"id"`
	Label         string `json:"label,omitempty"`
	Engine        string `json:"engine"`
	Ref           string `json:"ref"`
	Digest        string `json:"digest"`
	MinVramGb     int    `json:"minVramGb"`
	ContextTokens int    `json:"contextTokens"`
}

// Manifest is the catalog payload.
type Manifest struct {
	Version  int     `json:"version"`
	IssuedAt string  `json:"issuedAt"`
	Models   []Model `json:"models"`
}

// Signed wraps the catalog with its signature.
//
// The signature covers the raw payload bytes rather than a re-serialization of
// the parsed object: making correctness depend on Go and TypeScript agreeing
// byte-for-byte on canonical JSON would be a needless source of failure.
type Signed struct {
	KeyID     string `json:"keyId"`
	Payload   string `json:"payload"`
	Signature string `json:"signature"`
}

var digestRe = regexp.MustCompile(`^sha256:[0-9a-f]{8,}$`)

// Verify checks the signature against a pinned public key and returns the
// catalog.
//
// The key is pinned in the node, not fetched: otherwise a compromised gateway
// could tell the whole fleet to download arbitrary weights.
func Verify(signed Signed, publicKeyB64 string) (Manifest, error) {
	var m Manifest

	pub, err := base64.StdEncoding.DecodeString(publicKeyB64)
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return m, fmt.Errorf("manifest public key must be 32 base64-encoded bytes")
	}
	payload, err := base64.StdEncoding.DecodeString(signed.Payload)
	if err != nil {
		return m, fmt.Errorf("manifest payload is not valid base64")
	}
	sig, err := base64.StdEncoding.DecodeString(signed.Signature)
	if err != nil || len(sig) != ed25519.SignatureSize {
		return m, fmt.Errorf("manifest signature is malformed")
	}
	if !ed25519.Verify(pub, payload, sig) {
		return m, fmt.Errorf("manifest signature does not verify against the pinned key")
	}
	if err := json.Unmarshal(payload, &m); err != nil {
		return m, fmt.Errorf("manifest payload is not valid JSON: %w", err)
	}
	if err := validate(m); err != nil {
		return m, err
	}
	return m, nil
}

func validate(m Manifest) error {
	if m.Version < 1 {
		return fmt.Errorf("manifest version must be positive")
	}
	if len(m.Models) == 0 {
		return fmt.Errorf("manifest contains no models")
	}
	seen := map[string]bool{}
	for _, model := range m.Models {
		switch {
		case model.ID == "" || model.Ref == "" || model.Engine == "":
			return fmt.Errorf("model %q is missing id, ref or engine", model.ID)
		case !digestRe.MatchString(model.Digest):
			return fmt.Errorf("model %q has a malformed digest %q", model.ID, model.Digest)
		case model.MinVramGb <= 0:
			return fmt.Errorf("model %q needs a positive minVramGb", model.ID)
		case seen[model.ID]:
			return fmt.Errorf("duplicate model id %q", model.ID)
		}
		seen[model.ID] = true
	}
	return nil
}

// Fetch downloads and verifies the catalog from the gateway.
func Fetch(ctx context.Context, hc *http.Client, url, publicKeyB64 string) (Manifest, error) {
	var m Manifest
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return m, err
	}
	resp, err := hc.Do(req)
	if err != nil {
		return m, fmt.Errorf("fetch manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("manifest endpoint returned %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return m, fmt.Errorf("read manifest: %w", err)
	}
	var signed Signed
	if err := json.Unmarshal(body, &signed); err != nil {
		return m, fmt.Errorf("manifest is not valid JSON: %w", err)
	}
	return Verify(signed, publicKeyB64)
}

// SelectForVram returns the models a card of vramGb can hold, optionally
// narrowed to an operator allowlist. Ordered largest-first, so the most capable
// model this machine can run is pulled before the smaller ones.
func SelectForVram(m Manifest, vramGb int, allow []string) []Model {
	allowed := map[string]bool{}
	for _, a := range allow {
		allowed[a] = true
	}

	var out []Model
	for _, model := range m.Models {
		if model.MinVramGb > vramGb {
			continue
		}
		if len(allowed) > 0 && !allowed[model.ID] {
			continue
		}
		out = append(out, model)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].MinVramGb != out[j].MinVramGb {
			return out[i].MinVramGb > out[j].MinVramGb
		}
		return out[i].ID < out[j].ID
	})
	return out
}
