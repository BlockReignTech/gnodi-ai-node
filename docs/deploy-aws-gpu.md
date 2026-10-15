# Running a node on a cloud GPU (AWS g4dn.xlarge)

A start-to-finish runbook for standing up a Gnodi AI node on a rented GPU and
leaving it serving traffic unattended. Every command and number here comes from
the reference node the network runs today — an AWS `g4dn.xlarge` with a Tesla T4,
serving `gnodi/llama3.1-8b` and `gnodi/mistral-7b` at **74–80 ms** per request.

It is written for AWS because that is what was used. Nothing in it is
AWS-specific past [step 1](#1-provision-the-instance): any Ubuntu 22.04 box with
an NVIDIA card, a driver, and outbound HTTPS will do — a Hetzner GPU box, a
Vast.ai instance, a machine under your desk. The node **dials out** and listens
on no inbound port, so it does not need a public address, a DNS name, a
certificate, or a forwarded port anywhere.

> **Read [What this costs](#9-what-this-costs) before you rent anything.** At
> today's network volume a rented cloud GPU does not pay for itself. Hardware you
> already own is a different calculation, and that section gives you both.

---

## 0. Before you start

You need four things. The first two come from the network and **must be in place
before the node first connects** — the gateway refuses a socket whose licence it
does not already know.

| | |
|---|---|
| **An AI Node License key** | `XXXX-XXXX-XXXX-XXXX`, from the Gnodi portal. A node licence used for Cosmos-side rewards is a *different* licence; see [below](#can-i-reuse-my-existing-node-licence). |
| **Your node enrolled with the gateway** | Send the network operator your licence key, your EVM payout address, and your licence tier. Until NodeSvc sync is live this is a manual step on our side; it takes a minute. Without it the handshake fails with `unknown_license`. |
| **An EVM payout address** | Where settlement pays. It is bound to the licence at enrolment, not configured on the box — a node cannot assert its own payout address. |
| **An SSH keypair** | For the instance. Keep the `.pem` somewhere you will not lose it; AWS will not show it again. |

---

## 1. Provision the instance

### Request the quota first

A fresh AWS account has a **G-instance vCPU limit of 0**, and the launch fails
with `You have requested more vCPU capacity than your current vCPU limit of 0`.
This is the single most likely thing to stop you on day one, and approval is not
instant.

Service Quotas → **Amazon EC2** → *Running On-Demand G and VT instances* →
request **at least 4** vCPUs. `g4dn.xlarge` is 4 vCPUs; ask for more only if you
plan to run several.

### Launch

| Setting | Value | Why |
|---|---|---|
| Instance type | **`g4dn.xlarge`** — 4 vCPU, 16 GB RAM, 1× NVIDIA T4 (15 GB usable VRAM) | The cheapest current-generation NVIDIA instance that holds two 8B-class models resident. |
| AMI | **Deep Learning Base OSS Nvidia Driver GPU AMI (Ubuntu 22.04)** | The driver and CUDA are pre-installed and known-good. Installing the driver by hand on a plain Ubuntu AMI costs an hour and a reboot cycle. |
| Root volume | **100 GB gp3** | The DL AMI alone is ~45 GB; model weights are 4–9 GB each. The reference node sits at 78 GB of 97 GB with four models pulled. 30 GB will fill before you finish. |
| Key pair | yours | |
| Security group | **inbound: SSH (22) from your IP only. Nothing else.** | The node opens its own outbound connection. There is no inbound port to serve on, so there is nothing to expose — and the status dashboard is loopback-only by design. |
| Outbound | leave the default allow-all, or allow **443/tcp** | Needs the gateway, NodeSvc, GitHub (binary), and the Ollama registry (weights). |

The AMI id differs per region — `ami-0009337d70644d42e` was the us-east-1 image
in March 2026. Pick it by name in the console rather than pasting an id.

### Verify the GPU before going further

```bash
ssh -i ~/path/to/your-key.pem ubuntu@<instance-ip>
nvidia-smi --query-gpu=name,memory.total,driver_version --format=csv,noheader
```

Expected on a `g4dn.xlarge`:

```
Tesla T4, 15360 MiB, 580.126.09
```

If `nvidia-smi` is missing or errors, stop here and fix the driver — nothing
downstream works without it, and the node auto-detects VRAM through this command.

---

## 2. Install the engine (Ollama)

```bash
curl -fsSL https://ollama.com/install.sh | sh
```

This installs Ollama and a systemd unit, and starts it on
`http://localhost:11434`. It detects the GPU on its own.

### Keep the weights resident — do not skip this

By default Ollama evicts an idle model after **5 minutes**. The next request then
pays a cold load, measured on this T4 at **80–115 seconds** against **~45 ms**
once resident. At real traffic levels most requests arrive more than five minutes
apart, so *most* requests would be cold — well past the gateway's job timeout.
The gateway fails over to the commercial fallback, and from a user's seat your
node simply hangs.

```bash
sudo mkdir -p /etc/systemd/system/ollama.service.d
sudo tee /etc/systemd/system/ollama.service.d/keep-alive.conf >/dev/null <<'EOF'
[Service]
Environment="OLLAMA_KEEP_ALIVE=-1"
EOF
sudo systemctl daemon-reload
sudo systemctl restart ollama
```

`-1` means never evict. The daemon also asks for `keep_alive: -1` on every
warm-up and job, but the service-level setting is what survives an Ollama
restart.

---

## 3. Decide what to run — the VRAM budget

The node fetches a **signed catalog** and selects every model whose
`minVramGb` fits your card. That test is **per model, not cumulative** — and with
`OLLAMA_KEEP_ALIVE=-1` everything selected stays resident at once. On a 15 GB
card an unrestricted node selects three models totalling ~20 GB of weights, which
thrashes or OOMs.

So on anything under 24 GB, **set `MODELS` explicitly** and keep the sum inside
your card.

| Catalog id | Engine ref | Needs | Resident |
|---|---|---|---|
| `gnodi/llama3.1-8b` | `llama3.1:8b-instruct-q4_K_M` | 8 GB | 5.3 GB |
| `gnodi/mistral-7b` | `mistral:7b-instruct-q4_K_M` | 8 GB | 4.9 GB |
| `gnodi/gemma3-12b` | `gemma3:12b-it-q4_K_M` | 12 GB | ~8.5 GB |
| `gnodi/qwen2.5-14b` | `qwen2.5:14b-instruct-q4_K_M` | 16 GB | — |
| `gnodi/gemma4-31b` | `gemma4:31b-it-q4_K_M` | 24 GB | — |
| `gnodi/qwen2.5-32b` | `qwen2.5:32b-instruct-q4_K_M` | 24 GB | — |
| `gnodi/llama3.1-70b` | `llama3.1:70b-instruct-q4_K_M` | 48 GB | — |

**What the reference node runs:** `gnodi/llama3.1-8b` + `gnodi/mistral-7b` —
10.2 GB resident of 15.4 GB, leaving headroom for two concurrent jobs' KV cache.
Two models at two different sizes serve more of the router's traffic than one
model with VRAM to spare.

Note that a T4 reports 15 GB, so `qwen2.5-14b` at `minVramGb: 16` is **not**
selectable on this card even though its weights are 9 GB. The threshold is
deliberately conservative about context and KV cache.

Weights are pulled automatically on first start (`AUTO_PULL=true`). You can
pre-pull to get it over with — the download is the slow part:

```bash
ollama pull llama3.1:8b-instruct-q4_K_M
ollama pull mistral:7b-instruct-q4_K_M
```

---

## 4. Install the node daemon

```bash
curl -fsSL https://get.gnodi-ai.com | LICENSE_KEY=XXXX-XXXX-XXXX-XXXX sh
```

The installer verifies the binary against the published `SHA256SUMS`, writes a
`0600` config at `/etc/gnodi-ai-node/node.env`, installs a systemd unit, and
starts it. Re-running later upgrades the binary and **leaves your config alone**.

---

## 5. Configure

```bash
sudo nano /etc/gnodi-ai-node/node.env
```

The three lines that matter on a card this size:

```ini
# Narrow to what fits resident together (see step 3).
MODELS=gnodi/llama3.1-8b,gnodi/mistral-7b

# Two jobs at once. At 1, a single slow request blocks everything behind it;
# the T4 has the headroom for two.
MAX_CONCURRENCY=2

# NodeSvc, for licensing and the Cosmos-side distributor.
NODESVC_URL=https://api.gnodiservices.com
```

The installer's defaults for `GATEWAY_URL` (`wss://gw.gnodi-ai.com/v1/agent`) and
`INFERENCE_URL` (`http://localhost:11434/v1`) are already correct — leave them.

```bash
sudo systemctl restart gnodi-ai-node
```

Everything else is optional; `gnodi-ai-node`'s
[configuration table](../README.md#configuration) is the full list. Two worth
knowing: `VRAM_GB` overrides auto-detection (required on Apple silicon, where
there is no `nvidia-smi`), and setting the removed `ADVERTISE_ENDPOINT` is now a
**startup error** rather than a silently ignored variable.

---

## 6. Verify it is actually serving

### The log

```bash
sudo journalctl -u gnodi-ai-node -f
```

A healthy start looks exactly like this:

```
manifest: detected 15GB of VRAM
manifest: v4 ready with gnodi/llama3.1-8b, gnodi/mistral-7b
warm: gnodi/llama3.1-8b ready in 28ms
warm: gnodi/mistral-7b ready in 26ms
registered with NodeSvc as v0.1.2
status page on http://127.0.0.1:8080 (loopback only)
agent: connected to wss://gw.gnodi-ai.com/v1/agent serving gnodi/llama3.1-8b, gnodi/mistral-7b
```

Reading it line by line:

- **`manifest: v4 ready with …`** — the signed catalog verified against the
  pinned public key, and these are the models selected for your VRAM. If a model
  you expected is missing, look for a `manifest: skipping` or
  `manifest: REFUSING` line above.
- **`warm: … ready in 28ms`** — the daemon loads every model through the same
  `/chat/completions` path a real job takes, *before* advertising it, so the
  gateway never routes work that is certain to time out. **28 ms is a restart**,
  where Ollama still holds the weights. On a genuinely cold boot expect
  **1–2 minutes per model** on a T4, and the node is not routable until it
  finishes. A model that will not load is logged as `warm: dropping <id>` and
  removed from the catalog — an honest smaller catalog beats a generous one that
  times out.
- **`agent: connected to … serving …`** — the socket is up and the node is
  routable for exactly those ids.

### The GPU

```bash
ollama ps
```

Both models should show `100% GPU` and `UNTIL: Forever`. Anything else means
`OLLAMA_KEEP_ALIVE` did not take.

### The dashboard

The status page is bound to loopback on purpose — it is for the operator, not the
network. Tunnel to it from your laptop:

```bash
ssh -i ~/path/to/your-key.pem -L 8080:127.0.0.1:8080 ubuntu@<instance-ip>
```

Then open <http://127.0.0.1:8080>: connection state, models with their pinned
refs, jobs in flight, VRAM, the device public key, and earnings per settled epoch.

### End to end

Send a real request through the network and check who served it. The response
carries `served_by` — your licence key when your node took the job, `fallback`
when it went to the commercial backend. On the reference node, `served_by` is the
node at **74–80 ms**, against **236–618 ms** for the fallback.

---

## 7. Keeping it up unattended

The systemd unit the installer writes already handles most of this:
`Restart=always` with a 5-second backoff, `DynamicUser=yes` (the daemon needs no
privileges), and `StateDirectory=` so the device key survives restarts.

What is worth knowing beyond that:

- **Reconnects are automatic**, with exponential backoff and jitter. The
  reference node was verified reconnecting unattended through a full gateway
  restart with nobody logged in.
- **Registrations survive a gateway restart.** You do not need to be re-enrolled
  after one; the node just reconnects.
- **Restarts drain.** The daemon stops accepting new work and lets in-flight jobs
  finish, so you can restart without failing live requests.
- **A cold boot costs 2–4 minutes** of warm-up before the node is routable. That
  is the cost of never advertising a model you cannot serve. Factor it into
  anything that reboots the box — unattended security upgrades included.
- **Back up `STATE_DIR`** (`/var/lib/gnodi-ai-node` under systemd). It holds the
  Ed25519 device key. Losing it means re-enrolling; anyone who can read it can
  impersonate your node and collect its work.
- **Watch the disk.** Weights accumulate, and a full root volume breaks Ollama in
  confusing ways. `df -h /` and `ollama list`; remove refs you no longer serve
  with `ollama rm`.

---

## 8. Troubleshooting

| Symptom | What it means | Fix |
|---|---|---|
| `unknown_license` | The gateway has no registration for this licence key. | Enrolment ([step 0](#0-before-you-start)) has not happened. Send the network operator your licence key and payout address. |
| `key_mismatch` | A different device key is already bound to this licence. The gateway binds the first key it sees, so a stolen licence alone cannot displace an enrolled node. | You most likely lost `STATE_DIR` and regenerated a key. Ask the network operator to clear the binding; the binding is in-memory, so it also clears on the next gateway restart. |
| `duplicate_connection` | This licence is already connected — usually an old process, or the same licence on two machines. | One node per licence. Stop the other. |
| `unauthorized` | Signature or nonce rejected. | Almost always a truncated or mistyped licence key in `node.env`. |
| Requests hang, then answer slowly from elsewhere | Cold model loads exceeding the gateway's job timeout, failing over to the fallback. | You are on a build without warm-up, or warm-up is still running. Wait for the `warm: … ready` lines; upgrade if they never appear. |
| `REFUSING` in the log, model not served | Your local copy's digest differs from the one the catalog publishes under that id. | `ollama rm <ref>` and let the node re-pull. The node will not serve weights the network did not publish — two nodes serving the same id at different quantizations for the same price is unrouteable. |
| Node advertises fewer models than you set | They did not load, or they did not fit. | Check `warm: dropping` lines and the VRAM budget in [step 3](#3-decide-what-to-run--the-vram-budget). |
| `heartbeat failed: NodeSvc returned 500` | NodeSvc could not record the heartbeat. | **Not your problem and not urgent.** Heartbeats feed licensing and the Cosmos-side distributor; routing liveness comes from the socket, so you keep serving traffic normally through them. Report it if it persists for hours. |
| `ADVERTISE_ENDPOINT` startup error | A stale config from before the dial-out model. | Delete the line. The node has no address to advertise. |
| Launch fails with `vCPU limit of 0` | AWS G-instance quota. | [Step 1](#request-the-quota-first). |

---

## 9. What this costs

Be clear-eyed about this. The numbers are ours, measured, and we would rather you
see them before you spend money than after.

| | |
|---|---|
| `g4dn.xlarge` on demand | **~$380/month** ($0.526/hr) |
| Same, 1-year reserved | ~$230/month |
| Spot | $0.15–0.20/hr, interruptible |
| **A GPU you already own** | **~$2–4/month** in electricity at current network volume — about 67 seconds of actual GPU work per day |

At the network's current traffic, a rented `g4dn.xlarge` **does not break even**:
it would take roughly 21,200 messages a day to cover $380/month, against the low
dozens the network serves today. The reference node exists to prove the network
can serve real traffic on company hardware, not because the economics work yet.

Which is the honest recommendation: **if you are running a node today, run it on
hardware you already have.** A 12 GB card serves `gnodi/llama3.1-8b` perfectly
well, and the marginal cost is electricity. Rent a GPU when network volume
justifies it.

How the earnings themselves work — tier points, quality, daily epochs, the Merkle
claim — is in
[ADR-001](https://github.com/BlockReignTech/gnodi-ai-contracts/blob/main/docs/ADR-001-credits-and-revenue-distribution.md).
Two things to know here: tier points come from your **licence tier** (full 100,
half 50, quarter 25, mini 10), and quality is `uptime × success rate`, which is
why the node refuses to advertise a model it cannot actually serve.

---

## Appendix: the reference node, exactly as configured

```ini
# /etc/gnodi-ai-node/node.env   (mode 0600)
LICENSE_KEY=XXXX-XXXX-XXXX-XXXX
GATEWAY_URL=wss://gw.gnodi-ai.com/v1/agent
NODESVC_URL=https://api.gnodiservices.com
INFERENCE_URL=http://localhost:11434/v1
NODE_VERSION=v0.1.2
MAX_CONCURRENCY=2
MODELS=gnodi/llama3.1-8b,gnodi/mistral-7b
```

```
AWS g4dn.xlarge   ·   Ubuntu 22.04.5 LTS   ·   Deep Learning Base OSS AMI
Tesla T4, 15360 MiB, driver 580.126.09
97 GB root volume
Ollama with OLLAMA_KEEP_ALIVE=-1
2 models resident, 10.2 GB of 15.4 GB VRAM
74–80 ms served latency
```

### Can I reuse my existing node licence?

No — and you should not want to. An **AI Node License** is a separate licence
type from the Cosmos-side node licence that earns daily GNODI rewards. Running
the AI daemon against the wrong licence does not endanger the other one, but it
will not be routable either: the gateway admits a licence that was enrolled for
AI serving. Mint an AI Node License and keep the two separate.
