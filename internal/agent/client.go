package agent

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/blockreigntech/gnodi-ai-node/internal/identity"
	"github.com/blockreigntech/gnodi-ai-node/internal/inference"
)

// Runner executes a job against the local inference backend.
type Runner interface {
	Serves(model string) bool
	Stream(ctx context.Context, model string, messages []inference.Message,
		maxTokens int, onDelta func(string) error) (inference.Result, error)
}

// Config is the agent's connection and capability settings.
type Config struct {
	// GatewayURL is the agent endpoint, e.g. wss://gw.gnodi-ai.com/v1/agent.
	GatewayURL     string
	LicenseKey     string
	NodeVersion    string
	Models         []string
	MaxConcurrency int
	Engine         string
	VramGb         int
}

// Client is the node's connection to the gateway.
type Client struct {
	cfg    Config
	key    *identity.Key
	runner Runner
	logger *log.Logger

	writes   chan []byte
	slots    chan struct{}
	draining atomic.Bool

	mu       sync.Mutex
	jobs     map[string]context.CancelFunc
	inFlight int
}

// New builds a client. maxConcurrency below 1 is treated as 1.
func New(cfg Config, key *identity.Key, runner Runner, logger *log.Logger) *Client {
	if cfg.MaxConcurrency < 1 {
		cfg.MaxConcurrency = 1
	}
	if logger == nil {
		logger = log.Default()
	}
	return &Client{
		cfg:    cfg,
		key:    key,
		runner: runner,
		logger: logger,
		slots:  make(chan struct{}, cfg.MaxConcurrency),
		jobs:   make(map[string]context.CancelFunc),
	}
}

// Drain stops accepting new work while letting in-flight jobs finish, so an
// operator can restart without failing live requests.
func (c *Client) Drain() {
	c.draining.Store(true)
	c.sendJSON(statusFrame{T: "status", Accepting: false, InFlight: c.snapshotInFlight()})
}

// Run connects and serves until ctx is cancelled, reconnecting with exponential
// backoff plus jitter. It returns only when ctx ends or the gateway rejects the
// node in a way retrying cannot fix.
func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	const maxBackoff = 30 * time.Second

	for {
		err := c.runOnce(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		var fatal *fatalError
		if errors.As(err, &fatal) {
			return fmt.Errorf("gateway rejected this node: %w", err)
		}
		if err != nil {
			c.logger.Printf("agent: connection ended (%v); retrying in %s", err, backoff.Round(time.Second))
		}

		// Jitter keeps a fleet from reconnecting in lockstep after a gateway
		// restart and immediately knocking it over again.
		jitter := time.Duration(rand.Int63n(int64(backoff / 2)))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(backoff + jitter):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// fatalError marks a rejection that reconnecting will not fix.
type fatalError struct{ code, message string }

func (e *fatalError) Error() string { return e.code + ": " + e.message }

// runOnce holds a single connection for its whole life.
func (c *Client) runOnce(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, c.cfg.GatewayURL, nil)
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()
	conn.SetReadLimit(4 << 20) // 4 MiB: prompts can be large

	if err := c.handshake(ctx, conn); err != nil {
		return err
	}

	// A single writer goroutine owns the socket: WebSocket writes are not safe
	// to make concurrently, and job goroutines all emit chunks.
	c.writes = make(chan []byte, 256)
	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-c.writes:
				if err := conn.Write(ctx, websocket.MessageText, msg); err != nil {
					cancel()
					return
				}
			}
		}
	}()

	c.draining.Store(false)
	c.send(capabilitiesFrame{
		T: "capabilities", Models: c.cfg.Models, MaxConcurrency: c.cfg.MaxConcurrency,
		Engine: c.cfg.Engine, VramGb: c.cfg.VramGb,
	})
	c.logger.Printf("agent: connected to %s serving %s", c.cfg.GatewayURL, strings.Join(c.cfg.Models, ", "))

	err = c.readLoop(ctx, conn)

	// The socket is gone: every in-flight job is lost. The gateway re-routes
	// them, so finishing here would only waste the GPU.
	c.cancelAllJobs()
	cancel()
	<-writerDone
	return err
}

func (c *Client) handshake(ctx context.Context, conn *websocket.Conn) error {
	hsCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	raw, err := readFrame(hsCtx, conn)
	if err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	var hello helloFrame
	if err := json.Unmarshal(raw, &hello); err != nil || hello.T != "hello" {
		return fmt.Errorf("expected hello, got %q", truncate(raw))
	}
	if hello.Protocol != ProtocolVersion {
		return &fatalError{"bad_protocol", fmt.Sprintf("gateway speaks v%d, this node speaks v%d", hello.Protocol, ProtocolVersion)}
	}

	nonce, err := hex.DecodeString(hello.Nonce)
	if err != nil || len(nonce) == 0 {
		return fmt.Errorf("gateway sent an unusable nonce")
	}

	if err := writeJSON(hsCtx, conn, authFrame{
		T: "auth", Protocol: ProtocolVersion,
		LicenseKey:  c.cfg.LicenseKey,
		Pubkey:      c.key.PublicKeyBase64(),
		Sig:         c.key.SignBase64(nonce),
		NodeVersion: c.cfg.NodeVersion,
	}); err != nil {
		return fmt.Errorf("send auth: %w", err)
	}

	raw, err = readFrame(hsCtx, conn)
	if err != nil {
		return fmt.Errorf("read ready: %w", err)
	}
	var probe typeOnly
	_ = json.Unmarshal(raw, &probe)
	if probe.T == "error" {
		var ef errorFrame
		_ = json.Unmarshal(raw, &ef)
		return &fatalError{ef.Code, ef.Message}
	}
	var ready readyFrame
	if err := json.Unmarshal(raw, &ready); err != nil || ready.T != "ready" {
		return fmt.Errorf("expected ready, got %q", truncate(raw))
	}
	return nil
}

func (c *Client) readLoop(ctx context.Context, conn *websocket.Conn) error {
	for {
		raw, err := readFrame(ctx, conn)
		if err != nil {
			return err
		}
		var probe typeOnly
		if err := json.Unmarshal(raw, &probe); err != nil {
			continue // unparseable frames are ignored, per the spec
		}

		switch probe.T {
		case "job.offer":
			var offer offerFrame
			if err := json.Unmarshal(raw, &offer); err == nil {
				c.onOffer(ctx, offer)
			}
		case "job.cancel":
			var cf cancelFrame
			if err := json.Unmarshal(raw, &cf); err == nil {
				c.cancelJob(cf.JobID)
			}
		case "error":
			var ef errorFrame
			_ = json.Unmarshal(raw, &ef)
			return &fatalError{ef.Code, ef.Message}
		default:
			// Unknown frame types are ignored so the protocol can grow without
			// a coordinated upgrade of every node.
		}
	}
}

// onOffer decides whether to take a job, and starts it if so.
func (c *Client) onOffer(ctx context.Context, offer offerFrame) {
	switch {
	case !c.runner.Serves(offer.Model):
		c.send(rejectFrame{T: "job.reject", JobID: offer.JobID, Reason: "no_model"})
		return
	case c.draining.Load():
		c.send(rejectFrame{T: "job.reject", JobID: offer.JobID, Reason: "draining"})
		return
	}

	// Non-blocking: at capacity we reject immediately so the gateway can
	// re-offer elsewhere in milliseconds rather than waiting on a timeout.
	select {
	case c.slots <- struct{}{}:
	default:
		c.send(rejectFrame{T: "job.reject", JobID: offer.JobID, Reason: "busy"})
		return
	}

	deadline := time.Duration(offer.DeadlineMs) * time.Millisecond
	if deadline <= 0 {
		deadline = 2 * time.Minute
	}
	jobCtx, cancel := context.WithTimeout(ctx, deadline)
	c.trackJob(offer.JobID, cancel)

	c.send(acceptFrame{T: "job.accept", JobID: offer.JobID})
	go c.runJob(jobCtx, offer)
}

func (c *Client) runJob(ctx context.Context, offer offerFrame) {
	defer func() {
		c.untrackJob(offer.JobID)
		<-c.slots
	}()

	started := time.Now()
	seq := 0
	messages := make([]inference.Message, 0, len(offer.Messages))
	for _, m := range offer.Messages {
		messages = append(messages, inference.Message{Role: m.Role, Content: m.Content})
	}

	result, err := c.runner.Stream(ctx, offer.Model, messages, offer.MaxTokens, func(delta string) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		c.send(chunkFrame{T: "job.chunk", JobID: offer.JobID, Seq: seq, Delta: delta})
		seq++
		return nil
	})

	if err != nil {
		// A cancelled job is the gateway's own doing — it already stopped
		// billing and is not waiting, so there is nothing useful to report.
		if ctx.Err() != nil {
			return
		}
		code := "engine"
		if errors.Is(err, inference.ErrModelNotServed) {
			code = "no_model"
		}
		c.send(jobErrorFrame{T: "job.error", JobID: offer.JobID, Code: code, Message: err.Error()})
		return
	}

	c.send(doneFrame{
		T: "job.done", JobID: offer.JobID,
		PromptTokens:     result.PromptTokens,
		CompletionTokens: result.CompletionTokens,
		DurationMs:       time.Since(started).Milliseconds(),
	})
}

// --- job bookkeeping ---

func (c *Client) trackJob(id string, cancel context.CancelFunc) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.jobs[id] = cancel
	c.inFlight++
}

func (c *Client) untrackJob(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if cancel, ok := c.jobs[id]; ok {
		cancel()
		delete(c.jobs, id)
		c.inFlight--
	}
}

func (c *Client) cancelJob(id string) {
	c.mu.Lock()
	cancel := c.jobs[id]
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (c *Client) cancelAllJobs() {
	c.mu.Lock()
	for id, cancel := range c.jobs {
		cancel()
		delete(c.jobs, id)
	}
	c.inFlight = 0
	c.mu.Unlock()
}

func (c *Client) snapshotInFlight() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inFlight
}

// InFlight reports how many jobs are running, for the status page.
func (c *Client) InFlight() int { return c.snapshotInFlight() }

// --- framing helpers ---

func (c *Client) send(v any) { c.sendJSON(v) }

func (c *Client) sendJSON(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		return
	}
	w := c.writes
	if w == nil {
		return // not connected
	}
	// Dropping a frame beats blocking a job goroutine on a stalled socket; the
	// gateway treats a stalled job as a failure and re-routes it.
	select {
	case w <- data:
	default:
		c.logger.Printf("agent: write buffer full, dropped a frame")
	}
}

func readFrame(ctx context.Context, conn *websocket.Conn) ([]byte, error) {
	_, data, err := conn.Read(ctx)
	return data, err
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, data)
}

func truncate(b []byte) string {
	if len(b) > 120 {
		return string(b[:120]) + "…"
	}
	return string(b)
}
