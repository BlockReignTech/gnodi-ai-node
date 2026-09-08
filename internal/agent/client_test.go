package agent

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/blockreigntech/gnodi-ai-node/internal/identity"
	"github.com/blockreigntech/gnodi-ai-node/internal/inference"
)

const testLicense = "AAAA-BBBB-CCCC-DDDD"

// fakeRunner stands in for the local inference backend.
type fakeRunner struct {
	models []string
	chunks []string
	err    error
	block  chan struct{} // when non-nil, Stream waits on it before finishing
	mu     sync.Mutex
	calls  int
}

func (f *fakeRunner) Serves(m string) bool {
	for _, s := range f.models {
		if s == m {
			return true
		}
	}
	return false
}

func (f *fakeRunner) Stream(ctx context.Context, _ string, _ []inference.Message, _ int,
	onDelta func(string) error) (inference.Result, error) {
	f.mu.Lock()
	f.calls++
	f.mu.Unlock()

	if f.err != nil {
		return inference.Result{}, f.err
	}
	for _, c := range f.chunks {
		if err := onDelta(c); err != nil {
			return inference.Result{}, err
		}
	}
	if f.block != nil {
		select {
		case <-f.block:
		case <-ctx.Done():
			return inference.Result{}, ctx.Err()
		}
	}
	return inference.Result{PromptTokens: 3, CompletionTokens: 7}, nil
}

// gatewayConn wraps the server side of the socket with test helpers.
type gatewayConn struct {
	t    *testing.T
	conn *websocket.Conn
	ctx  context.Context
}

func (g *gatewayConn) send(v any) {
	g.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		g.t.Fatal(err)
	}
	if err := g.conn.Write(g.ctx, websocket.MessageText, data); err != nil {
		g.t.Fatalf("gateway write: %v", err)
	}
}

func (g *gatewayConn) read() map[string]any {
	g.t.Helper()
	ctx, cancel := context.WithTimeout(g.ctx, 5*time.Second)
	defer cancel()
	_, data, err := g.conn.Read(ctx)
	if err != nil {
		g.t.Fatalf("gateway read: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		g.t.Fatalf("gateway parse: %v", err)
	}
	return out
}

// readUntil returns the first frame of the given type, skipping others.
func (g *gatewayConn) readUntil(t string) map[string]any {
	g.t.Helper()
	for i := 0; i < 20; i++ {
		f := g.read()
		if f["t"] == t {
			return f
		}
	}
	g.t.Fatalf("never saw a %q frame", t)
	return nil
}

// handshake performs the gateway side and verifies the node's signature.
func (g *gatewayConn) handshake() map[string]any {
	g.t.Helper()
	nonce := make([]byte, 32)
	for i := range nonce {
		nonce[i] = byte(i)
	}
	g.send(map[string]any{
		"t": "hello", "protocol": ProtocolVersion,
		"nonce": hex.EncodeToString(nonce), "heartbeatSec": 30,
	})

	auth := g.readUntil("auth")
	pub, err := base64.StdEncoding.DecodeString(auth["pubkey"].(string))
	if err != nil {
		g.t.Fatalf("bad pubkey: %v", err)
	}
	sig, err := base64.StdEncoding.DecodeString(auth["sig"].(string))
	if err != nil {
		g.t.Fatalf("bad sig: %v", err)
	}
	if !ed25519.Verify(pub, nonce, sig) {
		g.t.Fatal("node's signature does not verify against the nonce it was sent")
	}
	if auth["licenseKey"] != testLicense {
		g.t.Errorf("licence key = %v", auth["licenseKey"])
	}

	g.send(map[string]any{"t": "ready", "nodeId": testLicense})
	return auth
}

// startGateway runs a WebSocket server driving `script` for one connection.
func startGateway(t *testing.T, script func(g *gatewayConn)) string {
	t.Helper()
	done := make(chan struct{})
	var once sync.Once

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.CloseNow()
		script(&gatewayConn{t: t, conn: conn, ctx: r.Context()})
		once.Do(func() { close(done) })
	}))
	t.Cleanup(func() {
		srv.Close()
		select {
		case <-done:
		default:
		}
	})
	return "ws://" + strings.TrimPrefix(srv.URL, "http://") + "/v1/agent"
}

func newClient(t *testing.T, url string, runner Runner, maxConcurrency int) *Client {
	t.Helper()
	key, err := identity.LoadOrCreate(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return New(Config{
		GatewayURL: url, LicenseKey: testLicense, NodeVersion: "test",
		Models: []string{"m"}, MaxConcurrency: maxConcurrency, Engine: "ollama",
	}, key, runner, log.New(io.Discard, "", 0))
}

func TestAgent_HandshakeAndJob(t *testing.T) {
	runner := &fakeRunner{models: []string{"m"}, chunks: []string{"Hel", "lo"}}
	gotChunks := make(chan []string, 1)

	url := startGateway(t, func(g *gatewayConn) {
		g.handshake()

		caps := g.readUntil("capabilities")
		if caps["maxConcurrency"].(float64) != 2 {
			t.Errorf("maxConcurrency = %v", caps["maxConcurrency"])
		}
		if caps["engine"] != "ollama" {
			t.Errorf("engine = %v", caps["engine"])
		}

		g.send(map[string]any{
			"t": "job.offer", "jobId": "j1", "model": "m",
			"messages":  []map[string]string{{"role": "user", "content": "hi"}},
			"maxTokens": 32, "deadlineMs": 10000,
		})

		if f := g.readUntil("job.accept"); f["jobId"] != "j1" {
			t.Errorf("accept for %v", f["jobId"])
		}

		var deltas []string
		for {
			f := g.read()
			switch f["t"] {
			case "job.chunk":
				deltas = append(deltas, f["delta"].(string))
			case "job.done":
				if f["promptTokens"].(float64) != 3 || f["completionTokens"].(float64) != 7 {
					t.Errorf("done frame usage = %v", f)
				}
				gotChunks <- deltas
				return
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := newClient(t, url, runner, 2)
	go func() { _ = client.runOnce(ctx) }()

	select {
	case deltas := <-gotChunks:
		if strings.Join(deltas, "") != "Hello" {
			t.Errorf("chunks = %v", deltas)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for job.done")
	}
}

func TestAgent_RejectsUnservedModel(t *testing.T) {
	runner := &fakeRunner{models: []string{"m"}}
	got := make(chan string, 1)

	url := startGateway(t, func(g *gatewayConn) {
		g.handshake()
		g.readUntil("capabilities")
		g.send(map[string]any{
			"t": "job.offer", "jobId": "j1", "model": "something-else",
			"messages": []map[string]string{{"role": "user", "content": "hi"}}, "maxTokens": 8,
		})
		got <- g.readUntil("job.reject")["reason"].(string)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = newClient(t, url, runner, 1).runOnce(ctx) }()

	select {
	case reason := <-got:
		if reason != "no_model" {
			t.Errorf("reason = %q", reason)
		}
	case <-ctx.Done():
		t.Fatal("timed out")
	}
	if runner.calls != 0 {
		t.Error("node ran a model it does not serve")
	}
}

func TestAgent_RejectsWhenAtCapacity(t *testing.T) {
	block := make(chan struct{})
	runner := &fakeRunner{models: []string{"m"}, chunks: []string{"x"}, block: block}
	got := make(chan string, 1)

	url := startGateway(t, func(g *gatewayConn) {
		g.handshake()
		g.readUntil("capabilities")

		offer := func(id string) map[string]any {
			return map[string]any{
				"t": "job.offer", "jobId": id, "model": "m",
				"messages":  []map[string]string{{"role": "user", "content": "hi"}},
				"maxTokens": 8, "deadlineMs": 10000,
			}
		}
		g.send(offer("j1"))
		g.readUntil("job.accept") // j1 occupies the only slot
		g.send(offer("j2"))

		for {
			f := g.read()
			if f["t"] == "job.reject" && f["jobId"] == "j2" {
				got <- f["reason"].(string)
				close(block)
				return
			}
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = newClient(t, url, runner, 1).runOnce(ctx) }()

	select {
	case reason := <-got:
		// Busy is rejected immediately rather than queued, so the gateway can
		// re-offer elsewhere instead of waiting on a timeout.
		if reason != "busy" {
			t.Errorf("reason = %q", reason)
		}
	case <-ctx.Done():
		t.Fatal("timed out")
	}
}

func TestAgent_ReportsJobErrors(t *testing.T) {
	runner := &fakeRunner{models: []string{"m"}, err: errors.New("ollama died")}
	got := make(chan map[string]any, 1)

	url := startGateway(t, func(g *gatewayConn) {
		g.handshake()
		g.readUntil("capabilities")
		g.send(map[string]any{
			"t": "job.offer", "jobId": "j1", "model": "m",
			"messages":  []map[string]string{{"role": "user", "content": "hi"}},
			"maxTokens": 8, "deadlineMs": 10000,
		})
		g.readUntil("job.accept")
		got <- g.readUntil("job.error")
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	go func() { _ = newClient(t, url, runner, 1).runOnce(ctx) }()

	select {
	case f := <-got:
		if !strings.Contains(f["message"].(string), "ollama died") {
			t.Errorf("error frame = %v", f)
		}
	case <-ctx.Done():
		t.Fatal("timed out")
	}
}

func TestAgent_FatalRejectionStopsRetrying(t *testing.T) {
	url := startGateway(t, func(g *gatewayConn) {
		g.send(map[string]any{
			"t": "hello", "protocol": ProtocolVersion,
			"nonce": hex.EncodeToString([]byte("nonce")), "heartbeatSec": 30,
		})
		g.readUntil("auth")
		g.send(map[string]any{"t": "error", "code": "unknown_license", "message": "not registered"})
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Run (not runOnce): a rejection reconnecting cannot fix must stop the loop
	// rather than hammer the gateway forever.
	err := newClient(t, url, &fakeRunner{models: []string{"m"}}, 1).Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "unknown_license") {
		t.Fatalf("got %v, want a fatal unknown_license error", err)
	}
}

func TestAgent_ProtocolMismatchIsFatal(t *testing.T) {
	url := startGateway(t, func(g *gatewayConn) {
		g.send(map[string]any{"t": "hello", "protocol": 99, "nonce": "00", "heartbeatSec": 30})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := newClient(t, url, &fakeRunner{models: []string{"m"}}, 1).Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "bad_protocol") {
		t.Fatalf("got %v, want a fatal bad_protocol error", err)
	}
}

func TestAgent_DrainRejectsNewWork(t *testing.T) {
	runner := &fakeRunner{models: []string{"m"}, chunks: []string{"x"}}
	got := make(chan string, 1)
	client := make(chan *Client, 1)

	url := startGateway(t, func(g *gatewayConn) {
		g.handshake()
		g.readUntil("capabilities")

		(<-client).Drain()
		g.readUntil("status") // drain announced

		g.send(map[string]any{
			"t": "job.offer", "jobId": "j1", "model": "m",
			"messages":  []map[string]string{{"role": "user", "content": "hi"}},
			"maxTokens": 8, "deadlineMs": 10000,
		})
		got <- g.readUntil("job.reject")["reason"].(string)
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := newClient(t, url, runner, 1)
	go func() { _ = c.runOnce(ctx) }()
	client <- c

	select {
	case reason := <-got:
		if reason != "draining" {
			t.Errorf("reason = %q", reason)
		}
	case <-ctx.Done():
		t.Fatal("timed out")
	}
}

// The node cannot derive its own payout address; the gateway supplies it in the
// handshake so the operator can see their earnings without being told it
// out of band.
func TestAgent_LearnsPayoutAddressFromHandshake(t *testing.T) {
	const operator = "0x70997970C51812dc3A010C7d01b50e0d17dc79C8"
	ready := make(chan struct{})

	url := startGateway(t, func(g *gatewayConn) {
		nonce := make([]byte, 32)
		g.send(map[string]any{
			"t": "hello", "protocol": ProtocolVersion,
			"nonce": hex.EncodeToString(nonce), "heartbeatSec": 30,
		})
		g.readUntil("auth")
		g.send(map[string]any{"t": "ready", "nodeId": testLicense, "operator": operator})
		g.readUntil("capabilities")
		close(ready)
		<-g.ctx.Done()
	})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	c := newClient(t, url, &fakeRunner{models: []string{"m"}}, 1)
	if got := c.Operator(); got != "" {
		t.Errorf("operator should be empty before the handshake, got %q", got)
	}
	go func() { _ = c.runOnce(ctx) }()

	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("timed out")
	}
	if got := c.Operator(); got != operator {
		t.Errorf("Operator() = %q, want %q", got, operator)
	}
}
