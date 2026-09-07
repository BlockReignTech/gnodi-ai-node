// Package inference exposes an OpenAI-compatible /v1/chat/completions endpoint
// that forwards to a local backend (Ollama, vLLM, …), enforcing the node's model
// allowlist. The gateway routes inference requests here.
package inference

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"sort"
	"sync"
)

// Proxy forwards chat-completion requests to an OpenAI-compatible backend.
//
// It holds a catalog mapping the network's model ids to engine references: the
// gateway routes on `gnodi/llama3.1-8b`, while Ollama knows the same weights as
// `llama3.1:8b-instruct-q4_K_M`. Translating here keeps the engine's naming out
// of the protocol.
type Proxy struct {
	backendURL string // POSTs to backendURL + "/chat/completions"
	mu         sync.RWMutex
	catalog    map[string]string // model id -> engine ref
	http       *http.Client
}

// NewProxy builds a proxy serving exactly `models`, where each id is also its
// engine reference. The manifest replaces this mapping via SetCatalog.
func NewProxy(backendURL string, models []string, hc *http.Client) *Proxy {
	if hc == nil {
		hc = http.DefaultClient
	}
	catalog := make(map[string]string, len(models))
	for _, m := range models {
		catalog[m] = m
	}
	return &Proxy{backendURL: backendURL, catalog: catalog, http: hc}
}

// SetCatalog replaces the served models. Safe to call while jobs are running.
func (p *Proxy) SetCatalog(catalog map[string]string) {
	next := make(map[string]string, len(catalog))
	for id, ref := range catalog {
		next[id] = ref
	}
	p.mu.Lock()
	p.catalog = next
	p.mu.Unlock()
}

// ref resolves a network model id to its engine reference.
func (p *Proxy) ref(id string) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	r, ok := p.catalog[id]
	return r, ok
}

// Models returns the served model ids, sorted for stable reporting.
func (p *Proxy) Models() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]string, 0, len(p.catalog))
	for m := range p.catalog {
		out = append(out, m)
	}
	sort.Strings(out)
	return out
}

// ChatCompletions handles POST /v1/chat/completions. It checks the requested
// model is served, forwards the request body to the backend, and streams the
// response back unchanged (works for both streaming and non-streaming).
func (p *Proxy) ChatCompletions(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MiB cap
	if err != nil {
		writeError(w, http.StatusBadRequest, "could not read request body")
		return
	}

	var probe struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &probe); err != nil || probe.Model == "" {
		writeError(w, http.StatusBadRequest, "request must include a JSON \"model\" field")
		return
	}
	if _, ok := p.ref(probe.Model); !ok {
		writeError(w, http.StatusNotFound, "model not served by this node: "+probe.Model)
		return
	}

	if err := p.forward(r.Context(), body, w); err != nil {
		writeError(w, http.StatusBadGateway, "inference backend error: "+err.Error())
	}
}

func (p *Proxy) forward(ctx context.Context, body []byte, w http.ResponseWriter) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.backendURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); ct != "" {
		w.Header().Set("Content-Type", ct)
	}
	w.WriteHeader(resp.StatusCode)
	flusher, _ := w.(http.Flusher)
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return nil // client gone
			}
			if flusher != nil {
				flusher.Flush() // stream SSE chunks promptly
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return nil // backend stream ended/errored; response already started
		}
	}
}

func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"message": msg}})
}
