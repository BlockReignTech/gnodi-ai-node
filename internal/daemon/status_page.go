package daemon

import (
	"net/http"
)

// statusPage is the operator's local dashboard.
//
// Served on loopback only. It answers the three questions an operator actually
// has — am I connected, what am I serving, and is work arriving — without them
// needing to read logs or curl JSON.
const statusPage = `<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Gnodi AI Node</title>
<style>
  :root {
    color-scheme: dark;
    --surface:#02060B; --raised:#070C13; --card:#0B121B;
    --steel:#363F48; --text:#E8EBF2; --muted:#9AA4B2;
    --accent:#FA7C1F; --good:#4ADE80; --bad:#D8695E;
  }
  * { box-sizing:border-box }
  body {
    margin:0; background:var(--surface); color:var(--text);
    font:15px/1.6 ui-sans-serif,-apple-system,"Segoe UI",sans-serif;
  }
  .wrap { max-width:820px; margin:0 auto; padding:40px 24px 64px }
  h1 { font-size:20px; letter-spacing:.04em; text-transform:uppercase; margin:0 0 4px }
  .sub { color:var(--muted); font-size:13px; margin-bottom:28px }
  .pill {
    display:inline-flex; align-items:center; gap:7px; padding:4px 11px;
    border:1px solid var(--steel); border-radius:99px; font-size:12px;
  }
  .dot { width:8px; height:8px; border-radius:99px; background:var(--muted) }
  .dot.on { background:var(--good) } .dot.off { background:var(--bad) }
  .grid { display:grid; gap:12px; grid-template-columns:repeat(auto-fit,minmax(170px,1fr)); margin:24px 0 }
  .tile { border:1px solid var(--steel); border-radius:10px; background:var(--card); padding:14px 16px }
  .tile .k { font-size:10px; letter-spacing:.14em; text-transform:uppercase; color:var(--muted) }
  .tile .v { font-size:22px; margin-top:4px; font-variant-numeric:tabular-nums }
  h2 { font-size:11px; letter-spacing:.14em; text-transform:uppercase; color:var(--muted); margin:28px 0 10px }
  table { width:100%; border-collapse:collapse; font-size:13.5px }
  th { text-align:left; font-size:10px; letter-spacing:.12em; text-transform:uppercase; color:var(--muted);
       padding:8px 12px; border-bottom:1px solid var(--steel) }
  td { padding:9px 12px; border-bottom:1px solid rgba(54,63,72,.45); font-family:ui-monospace,Menlo,monospace; font-size:12.5px }
  tr:last-child td { border-bottom:0 }
  .box { border:1px solid var(--steel); border-radius:10px; background:var(--raised); overflow:hidden }
  code { font-family:ui-monospace,Menlo,monospace; font-size:12px; color:var(--muted); word-break:break-all }
  .btn { font:600 12px/1 ui-sans-serif,-apple-system,"Segoe UI",sans-serif; letter-spacing:.06em;
         text-transform:uppercase; border-radius:8px; padding:11px 20px; cursor:pointer;
         border:1px solid var(--steel); background:var(--raised); color:var(--text) }
  .btn:hover:not(:disabled) { border-color:var(--accent); color:var(--accent) }
  .btn.primary { background:var(--accent); border-color:var(--accent); color:var(--surface) }
  .btn.primary:hover:not(:disabled) { background:#FF8F3C; color:var(--surface) }
  .btn:disabled { opacity:.5; cursor:default }
  .bar { display:flex; align-items:center; gap:12px; flex-wrap:wrap; margin:18px 0 4px }
  .chat { border:1px solid var(--steel); border-radius:10px; background:var(--raised); padding:14px }
  .log { min-height:120px; max-height:320px; overflow-y:auto; display:flex; flex-direction:column; gap:10px }
  .msg { font-size:14px; line-height:1.55; white-space:pre-wrap; word-break:break-word }
  .msg.you { color:var(--muted) }
  .msg.you b, .msg.ai b { display:block; font-size:10px; letter-spacing:.14em;
                          text-transform:uppercase; color:var(--muted); margin-bottom:3px; font-weight:600 }
  .row { display:flex; gap:8px; margin-top:12px }
  select, input, textarea {
    background:var(--surface); border:1px solid var(--steel); color:var(--text);
    border-radius:8px; padding:10px; font:14px ui-sans-serif,-apple-system,"Segoe UI",sans-serif;
  }
  textarea { flex:1; resize:none }
  .hint { color:var(--muted); font-size:12px; margin-top:8px }
  footer { margin-top:32px; color:var(--muted); font-size:12px }
</style>
</head>
<body>
<div class="wrap">
  <h1>Gnodi AI Node</h1>
  <div class="sub">Operator dashboard · loopback only</div>
  <div class="pill"><span class="dot" id="dot"></span><span id="conn">checking…</span></div>

  <div class="bar">
    <button class="btn primary" id="toggle" disabled>…</button>
    <span class="sub" id="servestate" style="margin:0"></span>
  </div>

  <div class="grid">
    <div class="tile"><div class="k">Serving now</div><div class="v" id="inflight">–</div></div>
    <div class="tile"><div class="k">Capacity</div><div class="v" id="cap">–</div></div>
    <div class="tile"><div class="k">Models</div><div class="v" id="nmodels">–</div></div>
    <div class="tile"><div class="k">VRAM</div><div class="v" id="vram">–</div></div>
  </div>

  <h2>Earnings</h2>
  <div class="grid">
    <div class="tile"><div class="k">Earned all time</div><div class="v" id="earned">–</div></div>
    <div class="tile"><div class="k">Unclaimed</div><div class="v" id="unclaimed" style="color:var(--accent)">–</div></div>
  </div>
  <div class="box"><table>
    <thead><tr><th>Epoch</th><th>Amount (aGNOD)</th><th>Status</th></tr></thead>
    <tbody id="epochs"><tr><td colspan="3">loading…</td></tr></tbody>
  </table></div>
  <div class="sub" style="margin-top:10px" id="payout"></div>

  <h2>Models served</h2>
  <div class="box"><table>
    <thead><tr><th>Network id</th><th>Engine reference</th></tr></thead>
    <tbody id="models"><tr><td colspan="2">loading…</td></tr></tbody>
  </table></div>

  <h2>Chat with your own node</h2>
  <div class="chat">
    <div class="log" id="log">
      <div class="msg you">Ask your own hardware something. Nothing leaves this machine.</div>
    </div>
    <div class="row">
      <select id="chatmodel"></select>
      <textarea id="prompt" rows="1" placeholder="Message…"></textarea>
      <button class="btn" id="send">Send</button>
    </div>
    <div class="hint">
      Runs on the models already loaded for the network — no gateway, no credits, no
      round trip. A reply here uses the same card the network is served from, so it
      will queue behind any job in flight.
    </div>
  </div>

  <h2>Identity</h2>
  <div class="box" style="padding:14px 16px">
    <div style="font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted)">Device public key</div>
    <code id="pubkey">–</code>
    <div style="margin-top:12px;font-size:11px;letter-spacing:.12em;text-transform:uppercase;color:var(--muted)">Gateway</div>
    <code id="gateway">–</code>
  </div>

  <footer>
    Manifest <span id="manifest">–</span> · node <span id="version">–</span> ·
    refreshes every 3s
  </footer>
</div>
<script>
const $ = (id) => document.getElementById(id);

/** aGNOD wei → a short decimal string. BigInt so large values stay exact. */
function agnod(wei) {
  const v = BigInt(wei || "0");
  const whole = v / 10n ** 18n;
  const frac = (v % 10n ** 18n).toString().padStart(18, "0").slice(0, 4);
  return whole + "." + frac;
}

async function refreshEarnings() {
  try {
    const r = await fetch("/earnings", { cache: "no-store" });
    const d = await r.json();

    if (d.connected === false) {
      $("payout").textContent = "Waiting for the gateway to report this node's payout address.";
      return;
    }
    $("payout").textContent = d.operator ? "Payout address " + d.operator : "";
    if (d.error) {
      $("epochs").innerHTML = "<tr><td colspan=3>could not read claims — " + d.error + "</td></tr>";
      return;
    }

    $("earned").textContent = agnod(d.totalWei);
    $("unclaimed").textContent = agnod(d.unclaimedWei);

    const rows = (d.claims || []).map((c) => {
      // 'claimed' is deliberately tri-state: undefined means the chain could
      // not be read, which is not the same as "not yet claimed".
      const status = c.claimed === true ? "claimed"
        : c.claimed === false ? "claimable"
        : "unknown";
      return "<tr><td>" + c.epoch + "</td><td>" + agnod(c.amountWei) +
             "</td><td>" + status + "</td></tr>";
    });
    $("epochs").innerHTML = rows.length
      ? rows.join("")
      : "<tr><td colspan=3>no settled epochs yet</td></tr>";
  } catch (e) {
    $("epochs").innerHTML = "<tr><td colspan=3>earnings unavailable</td></tr>";
  }
}

async function refresh() {
  try {
    const r = await fetch("/status", { cache: "no-store" });
    if (!r.ok) throw new Error("status " + r.status);
    const d = await r.json();

    $("dot").className = "dot on";
    $("conn").textContent = "Connected to the gateway";
    $("inflight").textContent = d.inFlight;
    $("cap").textContent = d.maxConcurrency;
    $("nmodels").textContent = (d.models || []).length;
    $("vram").textContent = d.vramGb ? d.vramGb + " GB" : "—";
    $("pubkey").textContent = d.devicePubkey || "—";
    $("gateway").textContent = d.gateway || "—";
    $("manifest").textContent = d.manifestVersion ? "v" + d.manifestVersion : "off";
    $("version").textContent = d.version || "—";

    const catalog = d.catalog || {};
    const rows = (d.models || []).map((id) =>
      "<tr><td>" + id + "</td><td>" + (catalog[id] || id) + "</td></tr>");
    $("models").innerHTML = rows.length
      ? rows.join("")
      : "<tr><td colspan=2>no models ready — check the logs</td></tr>";

    if (d.serving !== serving) paintServing(d.serving !== false);

    // Rebuilt only when the list changes, so it does not fight the operator
    // mid-selection every three seconds.
    const picker = $("chatmodel");
    const have = Array.from(picker.options).map((o) => o.value).join(",");
    const want = (d.models || []).join(",");
    if (have !== want) {
      const keep = picker.value;
      picker.innerHTML = (d.models || [])
        .map((m) => "<option value=\"" + m + "\">" + m + "</option>").join("");
      if ((d.models || []).includes(keep)) picker.value = keep;
    }
  } catch (e) {
    // The daemon is the thing being watched, so a failed poll is itself the
    // signal worth showing.
    $("dot").className = "dot off";
    $("conn").textContent = "Daemon not responding";
  }
}
let serving = null;

async function setServing(on) {
  const b = $("toggle");
  b.disabled = true;
  b.textContent = on ? "Starting…" : "Stopping…";
  try {
    const r = await fetch("/serving", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ on }),
    });
    if (!r.ok) throw new Error("status " + r.status);
    await refresh();
  } catch (e) {
    $("servestate").textContent = "That did not take — check the logs.";
  } finally {
    b.disabled = false;
  }
}

function paintServing(on) {
  serving = on;
  const b = $("toggle");
  b.textContent = on ? "Stop serving" : "Start serving";
  b.className = on ? "btn" : "btn primary";
  b.disabled = false;
  $("servestate").textContent = on
    ? "Serving the network. Stop to free the card for something else."
    : "Paused. Weights are unloaded and the card is yours.";
}

$("toggle").addEventListener("click", () => setServing(!serving));

// --- local chat -----------------------------------------------------------
const log = $("log");

function bubble(cls, who, text) {
  const el = document.createElement("div");
  el.className = "msg " + cls;
  const b = document.createElement("b");
  b.textContent = who;
  el.appendChild(b);
  el.appendChild(document.createTextNode(text));
  log.appendChild(el);
  log.scrollTop = log.scrollHeight;
  return el;
}

async function send() {
  const box = $("prompt");
  const text = box.value.trim();
  const model = $("chatmodel").value;
  if (!text || !model) return;
  box.value = "";
  $("send").disabled = true;
  bubble("you", "You", text);
  const out = bubble("ai", model, "Thinking…");
  try {
    const r = await fetch("/v1/chat/completions", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ model, messages: [{ role: "user", content: text }] }),
    });
    const d = await r.json();
    const reply = (d.choices && d.choices[0] && d.choices[0].message &&
                   d.choices[0].message.content) || "";
    if (reply) {
      out.lastChild.textContent = reply;
      return;
    }
    // Say what actually came back. "(no reply)" hid a perfectly good error
    // message from the engine for as long as it took somebody to read the
    // daemon's source, and the first real bug here was exactly that: an error
    // object where an answer should have been.
    const why = (d.error && (d.error.message || d.error)) || d.message ||
                (r.ok ? "" : "HTTP " + r.status);
    out.lastChild.textContent = why
      ? "No answer: " + (typeof why === "string" ? why : JSON.stringify(why))
      : "No answer, and the engine said nothing about why.";
  } catch (e) {
    out.lastChild.textContent = "The engine did not answer: " + e.message;
  } finally {
    $("send").disabled = false;
    box.focus();
  }
}

$("send").addEventListener("click", send);
$("prompt").addEventListener("keydown", (e) => {
  if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); send(); }
});

refresh();
refreshEarnings();
setInterval(refresh, 3000);
// Settlement happens once an epoch, so this needs nothing like the same cadence.
setInterval(refreshEarnings, 60000);
</script>
</body>
</html>
`

func (d *Daemon) dashboard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(statusPage))
}
