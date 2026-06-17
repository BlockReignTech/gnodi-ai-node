// Package chain reads optional CometBFT RPC stats (block height, sync state,
// peers) for node heartbeat metrics. All failures are soft — metrics are best-effort.
package chain

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/blockreigntech/gnodi-ai-node/internal/nodesvc"
)

// Client queries a CometBFT RPC endpoint.
type Client struct {
	rpc  string
	http *http.Client
}

// New returns a chain client for the given RPC base URL.
func New(rpc string, hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{rpc: rpc, http: hc}
}

// Metrics fetches block height + sync state from /status and peer count from
// /net_info. Returns whatever it could read; missing pieces are left nil.
func (c *Client) Metrics(ctx context.Context) nodesvc.Metrics {
	var m nodesvc.Metrics

	var status struct {
		Result struct {
			SyncInfo struct {
				LatestBlockHeight string `json:"latest_block_height"`
				CatchingUp        bool   `json:"catching_up"`
			} `json:"sync_info"`
		} `json:"result"`
	}
	if c.getJSON(ctx, "/status", &status) == nil {
		if h, err := strconv.ParseInt(status.Result.SyncInfo.LatestBlockHeight, 10, 64); err == nil {
			m.BlockHeight = &h
		}
		cu := status.Result.SyncInfo.CatchingUp
		m.CatchingUp = &cu
	}

	var netInfo struct {
		Result struct {
			NPeers string `json:"n_peers"`
		} `json:"result"`
	}
	if c.getJSON(ctx, "/net_info", &netInfo) == nil {
		if p, err := strconv.Atoi(netInfo.Result.NPeers); err == nil {
			m.NumPeers = &p
		}
	}
	return m
}

func (c *Client) getJSON(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.rpc+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}
