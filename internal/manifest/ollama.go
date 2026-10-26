package manifest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Ollama talks to the local engine's management API (not the OpenAI-compatible
// surface, which lives under /v1).
type Ollama struct {
	baseURL string
	hc      *http.Client
}

func NewOllama(baseURL string, hc *http.Client) *Ollama {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Ollama{baseURL: strings.TrimRight(baseURL, "/"), hc: hc}
}

// OllamaBaseFrom derives the management base from an OpenAI-compatible URL:
// http://host:11434/v1 -> http://host:11434.
func OllamaBaseFrom(inferenceURL string) string {
	return strings.TrimSuffix(strings.TrimRight(inferenceURL, "/"), "/v1")
}

// Local lists installed models as ref -> digest.
func (o *Ollama) Local(ctx context.Context) (map[string]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/api/tags", nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("list local models: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama /api/tags returned %d", resp.StatusCode)
	}

	var body struct {
		Models []struct {
			Name   string `json:"name"`
			Digest string `json:"digest"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode /api/tags: %w", err)
	}

	out := make(map[string]string, len(body.Models))
	for _, m := range body.Models {
		digest := m.Digest
		if digest != "" && !strings.HasPrefix(digest, "sha256:") {
			digest = "sha256:" + digest
		}
		out[m.Name] = digest
	}
	return out, nil
}

// Pull downloads a model, reporting progress through onProgress.
//
// Ollama streams NDJSON status lines; they are surfaced rather than swallowed
// because a 40GB pull with no output looks indistinguishable from a hang.
func (o *Ollama) Pull(ctx context.Context, ref string, onProgress func(status string)) error {
	payload, _ := json.Marshal(map[string]any{"model": ref, "stream": true})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/pull", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := o.hc.Do(req)
	if err != nil {
		return fmt.Errorf("pull %s: %w", ref, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ollama /api/pull returned %d for %s", resp.StatusCode, ref)
	}

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	last := ""
	for scanner.Scan() {
		var line struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			continue
		}
		if line.Error != "" {
			return fmt.Errorf("pull %s: %s", ref, line.Error)
		}
		if line.Status != "" && line.Status != last && onProgress != nil {
			onProgress(line.Status)
			last = line.Status
		}
	}
	return scanner.Err()
}

// Loaded is one model currently held by the engine.
type Loaded struct {
	Ref       string
	VramBytes int64
}

// Resident reports what the engine currently holds in card memory.
//
// Ollama's own accounting, not the card's: it counts the weights this engine
// has loaded and nothing else. Everything else using the GPU — a desktop
// compositor, a browser, a game — is invisible here, which is why the budget
// this is measured against reserves headroom rather than spending the whole
// card.
func (o *Ollama) Resident(ctx context.Context) ([]Loaded, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL+"/api/ps", nil)
	if err != nil {
		return nil, err
	}
	resp, err := o.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ollama /api/ps returned %d", resp.StatusCode)
	}
	var body struct {
		Models []struct {
			Name     string `json:"name"`
			Model    string `json:"model"`
			SizeVram int64  `json:"size_vram"`
		} `json:"models"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("decode /api/ps: %w", err)
	}
	out := make([]Loaded, 0, len(body.Models))
	for _, m := range body.Models {
		ref := m.Name
		if ref == "" {
			ref = m.Model
		}
		out = append(out, Loaded{Ref: ref, VramBytes: m.SizeVram})
	}
	return out, nil
}

// ResidentBytes is the total of Resident.
func (o *Ollama) ResidentBytes(ctx context.Context) (int64, error) {
	loaded, err := o.Resident(ctx)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, m := range loaded {
		total += m.VramBytes
	}
	return total, nil
}

// Unload releases a model from card memory.
//
// `keep_alive: 0` is Ollama's own idiom for "put this down now". Used when a
// model warmed successfully but pushed the resident set past its budget: it
// loads, so it is not broken, it simply does not fit beside the others.
func (o *Ollama) Unload(ctx context.Context, ref string) error {
	payload, err := json.Marshal(map[string]any{"model": ref, "keep_alive": 0})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL+"/api/generate", bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("ollama /api/generate returned %d unloading %s", resp.StatusCode, ref)
	}
	return nil
}
