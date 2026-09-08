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

// fakeNodeSvc records which /nodes/* endpoints were hit and what they carried.
func fakeNodeSvc(t *testing.T) (*httptest.Server, *sync.Map) {
	t.Helper()
	hits := &sync.Map{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 4096)
		n, _ := r.Body.Read(buf)
		hits.Store(r.URL.Path, string(buf[:n]))
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func testConfig(t *testing.T, nodeSvcURL string) config.Config {
	t.Helper()
	return config.Config{
		LicenseKey:     "KEY-1",
		GatewayURL:     "ws://127.0.0.1:1/v1/agent", // never dialled in these tests
		NodeSvcURL:     nodeSvcURL,
		InferenceURL:   "http://127.0.0.1:1/v1",
		Models:         []string{"llama2-13b"},
		MaxConcurrency: 2,
		StateDir:       t.TempDir(),
		StatusAddr:     "127.0.0.1:0",
		NodeVersion:    "test",
		Engine:         "ollama",
	}
}

func TestDaemon_RegisterAndHeartbeat(t *testing.T) {
	ns, hits := fakeNodeSvc(t)
	d, err := New(testConfig(t, ns.URL))
	if err != nil {
		t.Fatalf("new: %v", err)
	}

	if err := d.Register(context.Background()); err != nil {
		t.Fatalf("register: %v", err)
	}
	if _, ok := hits.Load("/nodes/activate"); !ok {
		t.Error("activate not called")
	}
	body, ok := hits.Load("/nodes/capabilities")
	if !ok {
		t.Fatal("capabilities not reported")
	}
	// There is no inbound endpoint any more; the gateway learns what this node
	// serves over the agent socket.
	if !strings.Contains(body.(string), `"llama2-13b"`) {
		t.Errorf("capabilities did not carry the model list: %s", body)
	}

	if err := d.HeartbeatOnce(context.Background()); err != nil {
		t.Fatalf("heartbeat: %v", err)
	}
	if _, ok := hits.Load("/nodes/heartbeat"); !ok {
		t.Error("heartbeat not called")
	}
}

func TestDaemon_RegisterFailsOnBadLicense(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)

	d, err := New(testConfig(t, srv.URL))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	if err := d.Register(context.Background()); err == nil {
		t.Fatal("expected register to fail on a 401 activate")
	}
}

func TestDaemon_LocalRoutes(t *testing.T) {
	ns, _ := fakeNodeSvc(t)
	d, err := New(testConfig(t, ns.URL))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	h := d.Handler()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/health", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"ok"`) {
		t.Errorf("health: %d %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status: %d", rec.Code)
	}
	for _, want := range []string{`"inFlight":0`, `"maxConcurrency":2`, `"devicePubkey"`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("status missing %s: %s", want, rec.Body.String())
		}
	}
}

// The node must no longer expose an inference endpoint: that route was the
// unauthenticated public surface ADR-002 removed by inverting the connection.
func TestDaemon_ServesNoInboundInference(t *testing.T) {
	ns, _ := fakeNodeSvc(t)
	d, err := New(testConfig(t, ns.URL))
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{}")))
	if rec.Code == http.StatusOK {
		t.Error("the node still serves inbound inference")
	}
}

func TestDaemon_DeviceKeyPersistsAcrossRestarts(t *testing.T) {
	ns, _ := fakeNodeSvc(t)
	cfg := testConfig(t, ns.URL)

	first, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	second, err := New(cfg) // same StateDir: a "restart"
	if err != nil {
		t.Fatal(err)
	}
	if first.key.PublicKeyBase64() != second.key.PublicKeyBase64() {
		t.Error("device key changed across restarts; the gateway would reject it as a key mismatch")
	}
}

// Before the handshake the node does not know where its revenue settles.
// Saying so beats reporting zero earnings, which reads as "you earned nothing".
func TestDaemon_EarningsBeforeConnecting(t *testing.T) {
	ns, _ := fakeNodeSvc(t)
	d, err := New(testConfig(t, ns.URL))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/earnings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("earnings: %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"connected":false`) {
		t.Errorf("expected connected:false, got %s", rec.Body.String())
	}
}

func TestDaemon_StatusReportsPayoutAddress(t *testing.T) {
	ns, _ := fakeNodeSvc(t)
	d, err := New(testConfig(t, ns.URL))
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	d.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/status", nil))
	if !strings.Contains(rec.Body.String(), `"operator"`) {
		t.Errorf("status should carry the payout address field: %s", rec.Body.String())
	}
}
