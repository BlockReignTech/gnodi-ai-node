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

## Install

```bash
curl -fsSL https://get.gnodi-ai.com | LICENSE_KEY=ABCD-EFGH-IJKL-MNOP sh
```

Downloads the binary, verifies it against the published `SHA256SUMS`, writes a
`0600` config, and installs a service (systemd on Linux, launchd on macOS).
Re-running upgrades the binary and **leaves your config alone**. Then open the
dashboard at <http://127.0.0.1:8080>.

## Build & run from source

```bash
make build           # ./bin/gnodi-ai-node
make test            # unit + integration tests (no external services needed)
make release         # static binaries for 4 platforms + SHA256SUMS in ./dist

LICENSE_KEY=ABCD-EFGH-IJKL-MNOP \
GATEWAY_URL=wss://gw.gnodi-ai.com/v1/agent \
NODESVC_URL=https://nodes.gnodi.example \
INFERENCE_URL=http://localhost:11434/v1 \
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
| `MODELS` | | — | Optional allowlist of catalog ids. Empty serves everything the card holds. **Required only with `MANIFEST_DISABLED=true`** |
| `VRAM_GB` | | auto | Card size. Auto-detected via `nvidia-smi`; set it if detection fails |
| `AUTO_PULL` | | `true` | Download catalog models this machine can run |
| `MANIFEST_URL` | | derived | Signed catalog endpoint; derived from `GATEWAY_URL` |
| `MANIFEST_PUBKEY` | | pinned | Key the catalog is verified against |
| `OLLAMA_URL` | | derived | Engine management API; `INFERENCE_URL` minus `/v1` |
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

## Models come from the signed catalog

The network publishes a catalog pinning each model id to an exact engine
reference **and content digest**. On start the node fetches it, verifies the
signature against a **pinned** public key, picks everything its card can hold,
pulls what is missing, and checks each digest.

```
gnodi/qwen2.5-32b   →   qwen2.5:32b-instruct-q4_K_M   (sha256:733884d6…)
```

The gateway routes on the network id; the node translates to the engine's own
reference. That is what makes `gnodi/llama3.1-8b` mean identical weights on every
node — otherwise two nodes serve the same name at different quantizations for the
same price, and the router cannot tell them apart.

Three consequences worth knowing:

- **The public key is pinned in the binary, not fetched.** A compromised gateway
  cannot tell the fleet which weights to download. Production builds override it
  with `-ldflags` (see `make release`) or `MANIFEST_PUBKEY`.
- **A digest mismatch means the model is not served.** If your local copy differs
  from the pinned one, the node logs `REFUSING` and drops that id rather than
  serving weights the network did not publish under it.
- **Nothing is advertised before it is verified.** The sync runs before the socket
  opens, so the gateway never routes work here that is certain to fail — which
  would cost you success rate for nothing.

Set `MANIFEST_DISABLED=true` to go back to a hand-written `MODELS` list.

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
internal/manifest/   signed catalog: verify, select by VRAM, pull, check digests
internal/inference/  OpenAI-compatible backend client (streaming) + id→ref map
internal/nodesvc/    NodeSvc client: activate, capabilities, heartbeat
internal/chain/      optional CometBFT /status + /net_info metrics
internal/daemon/     lifecycle orchestration + loopback dashboard
install/             one-line installer + service units
```

## Dashboard

`http://127.0.0.1:8080` shows connection state, models served with their pinned
refs, jobs in flight, VRAM, the device public key, and **earnings**. Loopback
only — it is for the operator, not the network.

Earnings come from the gateway: the handshake reports this node's payout address
(the one piece of the settlement path a node cannot derive for itself), and the
daemon proxies `/v1/claims/:operator` at `/earnings`. Each settled epoch shows as
`claimed`, `claimable`, or `unknown` — the last meaning the chain could not be
read, which is not the same as unclaimed.

## Notes / TODO

- **One dependency:** `github.com/coder/websocket`, pure Go with no transitive
  dependencies, so the binary stays static and cross-compiles trivially. Hand
  rolling WebSocket framing, masking and the close handshake was not worth the
  risk of getting it subtly wrong.
- **macOS VRAM is not auto-detected.** Detection uses `nvidia-smi`; on Apple
  silicon set `VRAM_GB` by hand. The node says so rather than guessing, because a
  wrong guess either wastes the machine or fails every job with an OOM.
- Verification of correct model execution is a network-level concern (a future
  ADR), not the daemon's responsibility.
