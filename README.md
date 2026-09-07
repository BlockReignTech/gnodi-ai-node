# gnodi-ai-node

The operator daemon for the **Gnodi AI Node Network**. A single static Go binary
that **dials the gateway** and serves inference jobs pushed down that socket from
a local engine (Ollama / vLLM).

It listens for **no inbound traffic**. Per
[ADR-002](../docs/ADR-002-node-software-and-consumer-surfaces.md) D-2 the node
opens the connection, which is what lets a machine behind residential NAT or
CGNAT serve traffic — no port forwarding, no DNS name, no TLS certificate — and
which removes the unauthenticated public endpoint the previous design exposed.

## What it does

On start it:

1. **Activates** with NodeSvc (`POST /nodes/activate`, idempotent) and reports
   its model list.
2. **Dials** `GATEWAY_URL` and completes the
   [agent handshake](../docs/agent-protocol.md): the gateway sends a nonce, the
   node signs it with its Ed25519 **device key**, and the gateway binds that key
   to the licence on first use.
3. **Serves jobs** pushed down the socket, streaming tokens back chunk by chunk
   as the model generates them.
4. **Heartbeats** to NodeSvc for the licensing and Cosmos-side distributor.
   Routing liveness comes from the socket itself, not from this poll.
5. Exposes a **loopback status page** on `STATUS_ADDR`.

## Build & run

```bash
make build           # ./bin/gnodi-ai-node
make test            # unit + integration tests (no external services needed)
make build-linux     # static linux/amd64 + linux/arm64 binaries in ./bin

LICENSE_KEY=ABCD-EFGH-IJKL-MNOP \
GATEWAY_URL=wss://gw.gnodi-ai.com/v1/agent \
NODESVC_URL=https://nodes.gnodi.example \
INFERENCE_URL=http://localhost:11434/v1 \
MODELS=llama2-13b,mistral-7b \
./bin/gnodi-ai-node
```

With Ollama: `ollama serve` (OpenAI-compatible API at `http://localhost:11434/v1`),
`ollama pull llama2:13b`, then point `INFERENCE_URL` at it.

## Configuration

| Var | Required | Default | Notes |
|---|---|---|---|
| `LICENSE_KEY` | ✓ | — | AI Node License key (XXXX-XXXX-XXXX-XXXX) |
| `GATEWAY_URL` | ✓ | — | Agent socket, `ws://` or `wss://` |
| `NODESVC_URL` | ✓ | — | NodeSvc base (serves `/nodes/*`) |
| `INFERENCE_URL` | ✓ | — | OpenAI-compatible backend base |
| `MODELS` | ✓ | — | CSV of served models (allowlist + advertised) |
| `MAX_CONCURRENCY` | | `1` | Jobs served at once; the gateway never exceeds it |
| `STATE_DIR` | | `~/.gnodi-ai-node` | Holds the device key (created `0700`) |
| `STATUS_ADDR` | | `127.0.0.1:8080` | Local status page. Loopback by design |
| `ENGINE` / `VRAM_GB` | | `ollama` / — | Advertised capability hints |
| `NODE_VERSION` | | `dev` | Reported on activate/heartbeat and in the handshake |
| `HEARTBEAT_INTERVAL` | | `5m` | NodeSvc heartbeat cadence |
| `CHAIN_RPC` | | — | CometBFT RPC for block-height metrics (optional) |

> **`ADVERTISE_ENDPOINT` was removed.** The node dials out, so it has no address
> to advertise. Setting it is now a **startup error** rather than a silently
> ignored variable — someone who still has it configured has a stale mental
> model and would otherwise sit waiting for traffic that was never coming.

## The device key

On first run the daemon generates an Ed25519 keypair in `STATE_DIR/device.key`
(mode `0600`) and signs the gateway's nonce with it on every connect. The licence
key crosses the wire only during the handshake; it is never sent again.

The gateway binds the public key to the licence on first connection, so a stolen
licence key alone cannot displace an already-enrolled node. **Back up
`STATE_DIR`**: losing the key means re-enrolling with the network, and anyone who
can read it can impersonate the node and collect its work.

## Backpressure and restarts

The node advertises `MAX_CONCURRENCY` and rejects offers beyond it immediately,
so the gateway re-offers elsewhere in milliseconds rather than waiting on a
timeout. On shutdown the daemon drains — it stops accepting new work and lets
in-flight jobs finish — so an operator can restart without failing live requests.

A dropped socket abandons in-flight jobs on both sides: the gateway re-routes
them, the node discards its work, and the client sees a small delay. Reconnection
uses exponential backoff with jitter.

## Layout

```
cmd/gnodi-ai-node/   entrypoint (config → daemon.Run)
internal/config/     env config + validation
internal/identity/   Ed25519 device key
internal/agent/      gateway socket: handshake, job lifecycle, reconnect
internal/inference/  OpenAI-compatible backend client (streaming) + allowlist
internal/nodesvc/    NodeSvc client: activate, capabilities, heartbeat
internal/chain/      optional CometBFT /status + /net_info metrics
internal/daemon/     lifecycle orchestration + loopback status page
```

## Notes / TODO

- **One dependency:** `github.com/coder/websocket`, pure Go with no transitive
  dependencies, so the binary stays static and cross-compiles trivially. Hand
  rolling WebSocket framing, masking and the close handshake was not worth the
  risk of getting it subtly wrong.
- **Model management is manual.** ADR-002 phase 3 adds the signed manifest, so
  `MODELS` will be derived from what the node can actually run rather than
  asserted by the operator.
- Verification of correct model execution is a network-level concern (a future
  ADR), not the daemon's responsibility.
