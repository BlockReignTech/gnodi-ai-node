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
)

// Proxy forwards chat-completion requests to an OpenAI-compatible backend.
type Proxy struct {
	backendURL string // POSTs to backendURL + "/chat/completions"
	allowed    map[string]bool
	http       *http.Client
}

// NewProxy builds a proxy serving exactly `models`. If hc is nil, a default
// client is used.
func NewProxy(backendURL string, models []string, hc *http.Client) *Proxy {
	if hc == nil {
		hc = http.DefaultClient
	}
	allow := make(map[string]bool, len(models))
	for _, m := range models {
		allow[m] = true
	}
	return &Proxy{backendURL: backendURL, allowed: allow, http: hc}
}

// Models returns the served model list.
func (p *Proxy) Models() []string {
	out := make([]string, 0, len(p.allowed))
	for m := range p.allowed {
		out = append(out, m)
	}
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
	if !p.allowed[probe.Model] {
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
