# gnodi-ai-node

The operator daemon for the **Gnodi AI Node Network**. A single static Go binary
that serves an OpenAI-compatible inference endpoint backed by a local engine
(Ollama / vLLM), registers with NodeSvc, reports its capabilities so the gateway
can route to it, and heartbeats.

## What it does

On start it:

1. **Serves** `POST /v1/chat/completions` (OpenAI-compatible) + `GET /health`,
   forwarding to the local inference backend and enforcing the model allowlist.
2. **Activates** with NodeSvc (`POST /nodes/activate`, idempotent).
3. **Reports capabilities** (`POST /nodes/capabilities`) — its public endpoint +
   served models — so the gateway knows where to route.
4. **Heartbeats** on an interval (`POST /nodes/heartbeat`) with optional chain
   metrics (block height / sync state / peers) from a CometBFT RPC.

The gateway then calls `ADVERTISE_ENDPOINT/v1/chat/completions`, meters the
credits served, and the settlement job pays the operator's EVM address.

## Build & run

```bash
make build           # ./bin/gnodi-ai-node
make test            # unit + integration tests (no external services needed)
make build-linux     # static linux/amd64 + linux/arm64 binaries in ./bin

# run (all required vars must be set)
LICENSE_KEY=ABCD-EFGH-IJKL-MNOP \
NODESVC_URL=https://nodes.gnodi.example \
INFERENCE_URL=http://localhost:11434/v1 \
ADVERTISE_ENDPOINT=https://my-node.example.com \
MODELS=llama2-13b,mistral-7b \
CHAIN_RPC=https://rpc.gnodipowered.com \
./bin/gnodi-ai-node
```

With Ollama: `ollama serve` (exposes an OpenAI-compatible API at
`http://localhost:11434/v1`), `ollama pull llama2:13b`, then point
`INFERENCE_URL` at it.

## Configuration (environment)

| Var | Required | Default | Notes |
|---|---|---|---|
| `LICENSE_KEY` | ✓ | — | AI Node License key (XXXX-XXXX-XXXX-XXXX) |
| `NODESVC_URL` | ✓ | — | NodeSvc base (serves `/nodes/*`) |
| `INFERENCE_URL` | ✓ | — | OpenAI-compatible backend base; daemon POSTs to `…/chat/completions` |
| `ADVERTISE_ENDPOINT` | ✓ | — | Public URL the gateway reaches this node at |
| `MODELS` | ✓ | — | CSV of served models (allowlist + reported) |
| `LISTEN_ADDR` | | `:8080` | Bind address for the inference server |
| `NODE_VERSION` | | `dev` | Reported on activate/heartbeat |
| `HEARTBEAT_INTERVAL` | | `5m` | Go duration |
| `CHAIN_RPC` | | — | CometBFT RPC for block-height metrics (optional) |

## Layout

```
cmd/gnodi-ai-node/   entrypoint (config → daemon.Run)
internal/config/     env config + validation
internal/nodesvc/    NodeSvc client: activate, capabilities, heartbeat
internal/inference/  OpenAI-compatible proxy + model allowlist
internal/chain/      optional CometBFT /status + /net_info metrics
internal/daemon/     lifecycle orchestration + HTTP routes
```

## Notes / TODO

- **Inference endpoint auth (v1):** the served `/v1/chat/completions` is open;
  the gateway is the metering authority. A shared-secret header from the gateway
  is a planned addition.
- Dependency-free (Go stdlib only) → trivial cross-compilation and a tiny binary.
- Verification of correct model execution is a network-level concern (future ADR),
  not the daemon's responsibility.
