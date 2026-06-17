package daemon

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/blockreigntech/gnodi-ai-node/internal/config"
)

// fakeNodeSvc records which /nodes/* endpoints were hit.
func fakeNodeSvc(t *testing.T) (*httptest.Server, *sync.Map) {
	t.Helper()
	hits := &sync.Map{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Store(r.URL.Path, true)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func fakeBackend(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestDaemon_RegisterServeHeartbeat(t *testing.T) {
	ns, hits := fakeNodeSvc(t)
	be := fakeBackend(t)

	cfg := config.Config{
		LicenseKey:        "KEY-1",
		NodeSvcURL:        ns.URL,
		InferenceURL:      be.URL,
		AdvertiseEndpoint: "https://node1.example.com",
		Models:            []string{"llama2-13b"},
		NodeVersion:       "test",
	}
	d := New(cfg)
	ctx := context.Background()

	// register → activate + capabilities
	if err := d.Register(ctx); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := hits.Load("/nodes/activate"); !ok {
		t.Error("activate not called")
	}
	if _, ok := hits.Load("/nodes/capabilities"); !ok {
		t.Error("capabilities not reported")
	}

	// serve an inference request through the daemon handler → proxied to backend
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"llama2-13b","messages":[]}`))
	d.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"content":"ok"`) {
		t.Fatalf("inference not proxied: %d %s", rec.Code, rec.Body.String())
	}

	// heartbeat (no chain configured → no metrics, still posts)
	if err := d.HeartbeatOnce(ctx); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, ok := hits.Load("/nodes/heartbeat"); !ok {
		t.Error("heartbeat not called")
	}

	// health endpoint
	hrec := httptest.NewRecorder()
	d.Handler().ServeHTTP(hrec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if hrec.Code != http.StatusOK || !strings.Contains(hrec.Body.String(), `"status":"ok"`) {
		t.Errorf("health: %d %s", hrec.Code, hrec.Body.String())
	}
}

func TestDaemon_RegisterFailsOnBadLicense(t *testing.T) {
	// NodeSvc returns 401 for activate
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	be := fakeBackend(t)

	d := New(config.Config{
		LicenseKey:        "BAD",
		NodeSvcURL:        srv.URL,
		InferenceURL:      be.URL,
		AdvertiseEndpoint: "https://n",
		Models:            []string{"m"},
	})
	if err := d.Register(context.Background()); err == nil {
		t.Fatal("expected register to fail on 401 activate")
	}
}
