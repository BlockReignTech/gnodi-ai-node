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
  footer { margin-top:32px; color:var(--muted); font-size:12px }
</style>
</head>
<body>
<div class="wrap">
  <h1>Gnodi AI Node</h1>
  <div class="sub">Operator dashboard · loopback only</div>
  <div class="pill"><span class="dot" id="dot"></span><span id="conn">checking…</span></div>

  <div class="grid">
    <div class="tile"><div class="k">Serving now</div><div class="v" id="inflight">–</div></div>
    <div class="tile"><div class="k">Capacity</div><div class="v" id="cap">–</div></div>
    <div class="tile"><div class="k">Models</div><div class="v" id="nmodels">–</div></div>
    <div class="tile"><div class="k">VRAM</div><div class="v" id="vram">–</div></div>
  </div>

  <h2>Models served</h2>
  <div class="box"><table>
    <thead><tr><th>Network id</th><th>Engine reference</th></tr></thead>
    <tbody id="models"><tr><td colspan="2">loading…</td></tr></tbody>
  </table></div>

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
  } catch (e) {
    // The daemon is the thing being watched, so a failed poll is itself the
    // signal worth showing.
    $("dot").className = "dot off";
    $("conn").textContent = "Daemon not responding";
  }
}
refresh();
setInterval(refresh, 3000);
</script>
</body>
</html>
`

func (d *Daemon) dashboard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(statusPage))
}
