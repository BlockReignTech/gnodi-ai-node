# Gnodi Agent Protocol v1

The wire contract between the **operator node** (this repository, Go) and the
Gnodi **gateway**.

Two independent implementations read this document; it is the normative source,
not either codebase. It is public deliberately — a protocol whose security
depends on secrecy is not secure, and anyone running a licensed node observes
the whole thing anyway.

## Transport

The node dials `wss://<gateway>/v1/agent` and holds the connection open. There
is **no inbound port on the node** — that is the whole point, and it is what lets
a machine behind residential NAT serve traffic.

- One JSON object per WebSocket **text** frame.
- Every frame has a `t` (type) field. Unknown types are ignored, not fatal, so
  the protocol can grow without a flag day.
- Liveness is WebSocket ping/pong (every 30s, gateway-initiated). The socket
  being open *is* the heartbeat; connection duration over an epoch is the node's
  `uptimeBps` in the ADR-001 payout math.

## Handshake

| # | Direction | Frame |
|---|---|---|
| 1 | GW → Node | `{"t":"hello","protocol":1,"nonce":"<hex>","heartbeatSec":30}` |
| 2 | Node → GW | `{"t":"auth","protocol":1,"licenseKey":"…","pubkey":"<b64>","sig":"<b64>","nodeVersion":"…"}` |
| 3 | GW → Node | `{"t":"ready","nodeId":"…"}` — or `{"t":"error",…}` and close |
| 4 | Node → GW | `{"t":"capabilities","models":[…],"maxConcurrency":N,"vramGb":N,"engine":"ollama"}` |

`sig` is Ed25519 over the **raw bytes of the hex-decoded `nonce`**, by the key
whose public half is `pubkey` (32 bytes, base64).

**Key binding is trust-on-first-use.** The first connection for a licence key
binds its public key; later connections must present the same one. This means a
stolen licence key alone is not enough to impersonate an already-connected
operator. It does *not* protect a licence that has never connected — closing
that requires registering the public key at activation time through NodeSvc,
which is a follow-up, not part of v1.

The node must not connect before it has a licence key; the key crosses the wire
only inside the TLS session, and never again after `ready`.

## Job lifecycle

| Direction | Frame | Notes |
|---|---|---|
| GW → Node | `{"t":"job.offer","jobId","model","messages":[{role,content}],"maxTokens","deadlineMs"}` | |
| Node → GW | `{"t":"job.accept","jobId"}` | node has capacity and the model |
| Node → GW | `{"t":"job.reject","jobId","reason"}` | `busy` \| `no_model` \| `draining` |
| Node → GW | `{"t":"job.chunk","jobId","seq","delta"}` | `seq` starts at 0, strictly increasing |
| Node → GW | `{"t":"job.done","jobId","promptTokens","completionTokens","durationMs"}` | terminal |
| Node → GW | `{"t":"job.error","jobId","code","message"}` | terminal |
| GW → Node | `{"t":"job.cancel","jobId"}` | caller vanished or deadline passed |
| Node → GW | `{"t":"status","accepting","inFlight"}` | unsolicited, any time |

**Why offer/accept rather than a bare push.** A node may have just started
another job or unloaded a model. An explicit reject lets the gateway re-offer
elsewhere in milliseconds instead of discovering the failure through a timeout.

**Backpressure.** The node advertises `maxConcurrency` in `capabilities`; the
gateway never exceeds it. A node sends `status{"accepting":false}` to drain
before a planned restart, so operators can reboot without failing live requests.

**Cancellation** is advisory. The node should stop generating, but the gateway
has already stopped billing and does not wait for an acknowledgement.

## Failure and reconnect

- A dropped socket abandons every in-flight job on both sides. The gateway
  re-routes the whole job; the node discards its work. Mid-stream resume is
  deliberately not attempted — re-running is cheap, and the bookkeeping is not.
- The node reconnects with exponential backoff plus jitter.
- Re-offering after a failure follows the streaming rule: a node that fails
  **before** its first chunk is retried elsewhere; one that fails **after** ends
  the request, because the caller already holds partial text a second node would
  not continue.

## Errors

`{"t":"error","code":"…","message":"…"}` from the gateway is fatal and is
followed by a close. Codes: `bad_protocol`, `unauthorized`, `unknown_license`,
`key_mismatch`, `duplicate_connection`.

A second connection for a licence key that is already connected is refused with
`duplicate_connection`; the incumbent keeps serving.
