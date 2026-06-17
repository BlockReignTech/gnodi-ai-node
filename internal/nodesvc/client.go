// Package nodesvc is a small client for the gnodi-sdk NodeSvc public API used by
// the node daemon: activate, report capabilities, and heartbeat.
package nodesvc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// Metrics are optional chain stats reported on heartbeat.
type Metrics struct {
	BlockHeight *int64
	CatchingUp  *bool
	NumPeers    *int
}

// Client talks to NodeSvc /nodes/* endpoints with the node's license key.
type Client struct {
	baseURL    string
	licenseKey string
	http       *http.Client
}

// New returns a NodeSvc client. If hc is nil, http.DefaultClient is used.
func New(baseURL, licenseKey string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{baseURL: baseURL, licenseKey: licenseKey, http: hc}
}

// Activate registers the node (idempotent: NodeSvc returns 200 if already registered).
func (c *Client) Activate(ctx context.Context, nodeVersion string) error {
	body, _ := json.Marshal(map[string]string{"licenseKey": c.licenseKey, "nodeVersion": nodeVersion})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/nodes/activate", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	return c.do(req, "activate")
}

// ReportCapabilities tells NodeSvc the inference endpoint and models this node serves.
func (c *Client) ReportCapabilities(ctx context.Context, endpoint string, models []string) error {
	body, _ := json.Marshal(map[string]any{"endpoint": endpoint, "models": models})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/nodes/capabilities", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-LICENSE-KEY", c.licenseKey)
	return c.do(req, "capabilities")
}

// Heartbeat reports liveness plus optional chain metrics via headers.
func (c *Client) Heartbeat(ctx context.Context, nodeVersion string, m Metrics) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/nodes/heartbeat", nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-LICENSE-KEY", c.licenseKey)
	if nodeVersion != "" {
		req.Header.Set("X-NODE-VERSION", nodeVersion)
	}
	if m.BlockHeight != nil {
		req.Header.Set("X-BLOCK-HEIGHT", strconv.FormatInt(*m.BlockHeight, 10))
	}
	if m.CatchingUp != nil {
		req.Header.Set("X-CATCHING-UP", strconv.FormatBool(*m.CatchingUp))
	}
	if m.NumPeers != nil {
		req.Header.Set("X-NUM-PEERS", strconv.Itoa(*m.NumPeers))
	}
	return c.do(req, "heartbeat")
}

func (c *Client) do(req *http.Request, op string) error {
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("%s request failed: %w", op, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("%s: NodeSvc returned %d: %s", op, resp.StatusCode, bytes.TrimSpace(b))
	}
	return nil
}
