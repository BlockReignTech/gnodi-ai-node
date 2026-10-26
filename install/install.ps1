<#
.SYNOPSIS
  Install and run a Gnodi AI node on Windows.

.DESCRIPTION
  Sets up everything a node needs on a Windows machine with an NVIDIA card:
  the inference engine, the model weights, the daemon, and a launcher.

  Run it from PowerShell:

      powershell -ExecutionPolicy Bypass -File install.ps1 -LicenseKey XXXX-XXXX-XXXX-XXXX

  Re-running upgrades the daemon and leaves the configuration alone, so it is
  also how you take a new release.

  Nothing here needs Administrator. The daemon listens on no inbound port and
  wants no privileges; it dials out, which is what lets a machine behind a home
  router serve traffic at all.

.PARAMETER LicenseKey
  The AI Node License this machine serves under. Required on a first install.

.PARAMETER Version
  Release tag to install. Defaults to the latest.
#>
[CmdletBinding()]
param(
  [string]$LicenseKey,
  [string]$Version = "latest",
  [string]$GatewayUrl = "wss://gw.gnodi-ai.com/v1/agent",
  [string]$NodeSvcUrl = "https://api.gnodiservices.com",
  [int]$MaxConcurrency = 2,
  [switch]$SkipModels
)

$ErrorActionPreference = "Stop"
$Repo = "BlockReignTech/gnodi-ai-node"
$Home_ = Join-Path $env:LOCALAPPDATA "GnodiAINode"
$Exe = Join-Path $Home_ "gnodi-ai-node.exe"
$EnvFile = Join-Path $Home_ "node.env"
$Launcher = Join-Path $Home_ "start-node.ps1"

function Info($m) { Write-Host "  $m" }
function Step($m) { Write-Host "`n==> $m" -ForegroundColor Cyan }
function Warn($m) { Write-Host "  ! $m" -ForegroundColor Yellow }
function Die($m) { Write-Host "error: $m" -ForegroundColor Red; exit 1 }

New-Item -ItemType Directory -Force -Path $Home_ | Out-Null

# --- 1. the card -----------------------------------------------------------
# Checked first because it decides everything downstream: which models the node
# can hold, and whether it is worth continuing at all. CPU-only inference is
# slow enough that jobs time out and route elsewhere, which costs success rate
# without earning anything.
Step "Checking the GPU"
$smi = Get-Command nvidia-smi -ErrorAction SilentlyContinue
if (-not $smi) {
  Die @"
nvidia-smi was not found, so there is no usable NVIDIA driver on this machine.
Install the current GeForce or Studio driver and run this again. A node without
a GPU cannot serve the network.
"@
}
$vramMib = [int](& nvidia-smi --query-gpu=memory.total --format=csv,noheader,nounits | Select-Object -First 1)
$gpuName = (& nvidia-smi --query-gpu=name --format=csv,noheader | Select-Object -First 1).Trim()
# Rounded, not truncated. A card never reports the size on its box -- an 8GB
# RTX 5060 Ti reports 8151MiB because the driver reserves the rest -- and
# truncating 7.96 gives 7, which is below every threshold in the catalog. The
# first 8GB card to run this was told nothing fits it.
$vramGb = [math]::Round($vramMib / 1024)
Info "$gpuName with $vramGb GB of VRAM ($vramMib MiB reported)"
if ($vramGb -lt 8) {
  Die "8 GB is the minimum; this card reports $vramMib MiB. Nothing in the catalog fits."
}

# --- 2. the engine ---------------------------------------------------------
Step "Checking the inference engine"
$ollama = Get-Command ollama -ErrorAction SilentlyContinue
if (-not $ollama) {
  Info "Ollama is not installed. Downloading the installer..."
  $setup = Join-Path $env:TEMP "OllamaSetup.exe"
  Invoke-WebRequest -Uri "https://ollama.com/download/OllamaSetup.exe" -OutFile $setup -UseBasicParsing
  Info "Running it -- accept the prompts, then this script continues."
  Start-Process -FilePath $setup -Wait
  $env:Path = [Environment]::GetEnvironmentVariable("Path", "Machine") + ";" +
              [Environment]::GetEnvironmentVariable("Path", "User")
  $ollama = Get-Command ollama -ErrorAction SilentlyContinue
  if (-not $ollama) { Die "Ollama still is not on PATH. Open a new PowerShell window and re-run." }
}
Info "Ollama at $($ollama.Source)"

# Keep the weights resident.
#
# Ollama evicts an idle model after five minutes by default, and the next
# request then pays a cold load -- measured at 80 to 115 seconds on a T4 against
# about 45ms warm. At real traffic most requests arrive more than five minutes
# apart, so most requests would be cold: past the gateway's job timeout, failed
# over to the paid fallback, and from a user's seat the node simply hangs.
Step "Keeping the weights in VRAM"
$current = [Environment]::GetEnvironmentVariable("OLLAMA_KEEP_ALIVE", "User")
if ($current -ne "-1") {
  [Environment]::SetEnvironmentVariable("OLLAMA_KEEP_ALIVE", "-1", "User")
  Info "Set OLLAMA_KEEP_ALIVE=-1 for your user account"
  # Ollama reads this at start, so it has to be restarted to take effect.
  $running = Get-Process ollama -ErrorAction SilentlyContinue
  if ($running) {
    Info "Restarting Ollama so it picks that up"
    Get-Process ollama, "ollama app" -ErrorAction SilentlyContinue | Stop-Process -Force
    Start-Sleep -Seconds 3
  }
} else {
  Info "Already set"
}
$env:OLLAMA_KEEP_ALIVE = "-1"
if (-not (Get-Process ollama -ErrorAction SilentlyContinue)) {
  Info "Starting Ollama"
  Start-Process -FilePath $ollama.Source -ArgumentList "serve" -WindowStyle Hidden
  Start-Sleep -Seconds 4
}

# --- 3. the models ---------------------------------------------------------
# Pulled here rather than left to the daemon so the download has a progress bar
# instead of looking like a hang. The daemon would fetch the same weights from
# the same signed catalog; this only front-loads it.
if (-not $SkipModels) {
  Step "Downloading model weights (several GB -- this is the slow part)"
  # What this card can actually hold at once, which is not the same as what it
  # is allowed to run. The daemon measures the resident set as it loads and
  # keeps only the models that fit together, so pulling more than that is
  # several gigabytes of download for weights it will drop anyway.
  if ($vramGb -ge 24) {
    $models = @("gemma3:12b-it-q4_K_M", "llama3.1:8b-instruct-q4_K_M", "mistral:7b-instruct-q4_K_M")
  } elseif ($vramGb -ge 12) {
    $models = @("llama3.1:8b-instruct-q4_K_M", "mistral:7b-instruct-q4_K_M")
  } else {
    # 8-11GB: one model, with headroom for its context. mistral-7b is the
    # smallest in the catalog and the network routes real traffic to it.
    $models = @("mistral:7b-instruct-q4_K_M")
  }
  foreach ($m in $models) {
    Info "pulling $m"
    & $ollama.Source pull $m
    if ($LASTEXITCODE -ne 0) { Warn "could not pull $m -- the daemon will try again on start" }
  }
}

# --- 4. the daemon ---------------------------------------------------------
Step "Installing the node daemon"
$asset = "gnodi-ai-node_windows_amd64.exe"
$base = if ($Version -eq "latest") {
  "https://github.com/$Repo/releases/latest/download"
} else {
  "https://github.com/$Repo/releases/download/$Version"
}
$tmp = Join-Path $env:TEMP $asset
Invoke-WebRequest -Uri "$base/$asset" -OutFile $tmp -UseBasicParsing

# Verify before running it. This binary holds a licence key and a device key and
# runs unattended; one extra request is cheap insurance.
try {
  $sumsPath = Join-Path $env:TEMP "SHA256SUMS"
  Invoke-WebRequest -Uri "$base/SHA256SUMS" -OutFile $sumsPath -UseBasicParsing
  $expected = (Get-Content $sumsPath | Where-Object { $_ -match [regex]::Escape($asset) } |
               ForEach-Object { ($_ -split '\s+')[0] } | Select-Object -First 1)
  $actual = (Get-FileHash -Path $tmp -Algorithm SHA256).Hash.ToLower()
  if ($expected -and $expected.ToLower() -ne $actual) {
    Die "checksum mismatch -- refusing to install`n  published:  $expected`n  downloaded: $actual"
  }
  if ($expected) { Info "checksum verified" } else { Warn "no checksum published for $asset" }
} catch {
  Warn "could not fetch SHA256SUMS; skipping verification ($($_.Exception.Message))"
}
Move-Item -Force -Path $tmp -Destination $Exe
Info "installed $Exe"

# --- 5. configuration ------------------------------------------------------
Step "Configuration"
if (Test-Path $EnvFile) {
  Info "keeping the existing config at $EnvFile"
} else {
  if (-not $LicenseKey) {
    $LicenseKey = Read-Host "  AI Node License key (XXXX-XXXX-XXXX-XXXX)"
  }
  if (-not $LicenseKey) { Die "a licence key is required" }

  # MODELS is deliberately not set. The daemon reads the network's signed
  # catalog, picks what this card can hold, verifies each digest, and warms the
  # set before advertising any of it -- and since v0.2.0 it measures the resident
  # set as it loads so it keeps only what fits together. Listing models by hand
  # overrides all of that.
  @"
# Gnodi AI Node. Re-run install.ps1 after editing, or just restart the node.
LICENSE_KEY=$LicenseKey
GATEWAY_URL=$GatewayUrl
NODESVC_URL=$NodeSvcUrl
INFERENCE_URL=http://localhost:11434/v1
MAX_CONCURRENCY=$MaxConcurrency
"@ | Set-Content -Path $EnvFile -Encoding ASCII

  # The licence key is in this file, so take it off the inherited ACL and give
  # it to this user alone.
  $acl = Get-Acl $EnvFile
  $acl.SetAccessRuleProtection($true, $false)
  $acl.SetAccessRule((New-Object System.Security.AccessControl.FileSystemAccessRule(
    "$env:USERDOMAIN\$env:USERNAME", "FullControl", "Allow")))
  Set-Acl -Path $EnvFile -AclObject $acl
  Info "wrote $EnvFile (readable only by you)"
}

# --- 6. launcher -----------------------------------------------------------
# Windows has no EnvironmentFile, so the launcher does what systemd would: read
# the config and put it in the environment of this process only. The licence key
# never becomes a global variable that every other program on the machine can
# read.
@'
$ErrorActionPreference = "Stop"
$dir = Split-Path -Parent $MyInvocation.MyCommand.Path
Get-Content (Join-Path $dir "node.env") | ForEach-Object {
  if ($_ -match '^\s*([A-Z0-9_]+)\s*=\s*(.*)$') {
    [Environment]::SetEnvironmentVariable($Matches[1], $Matches[2].Trim(), "Process")
  }
}
$env:OLLAMA_KEEP_ALIVE = "-1"
if (-not (Get-Process ollama -ErrorAction SilentlyContinue)) {
  $o = Get-Command ollama -ErrorAction SilentlyContinue
  if ($o) { Start-Process -FilePath $o.Source -ArgumentList "serve" -WindowStyle Hidden; Start-Sleep 4 }
}
Write-Host "Dashboard: http://127.0.0.1:8080" -ForegroundColor Cyan
Write-Host "First start warms the models before serving -- expect a few minutes.`n"
& (Join-Path $dir "gnodi-ai-node.exe")
'@ | Set-Content -Path $Launcher -Encoding ASCII
Info "wrote $Launcher"

# --- 7. done ---------------------------------------------------------------
Write-Host "`n==> Ready" -ForegroundColor Green
Write-Host @"

  Start the node:
      powershell -ExecutionPolicy Bypass -File "$Launcher"

  Dashboard:   http://127.0.0.1:8080
  Config:      $EnvFile

  The first start loads each model before advertising it, so the dashboard
  shows "Daemon not responding" for a few minutes and then connects. That is
  the node refusing to accept work it cannot yet serve.

  Stop serving from the dashboard when you want the card back -- it unloads the
  weights and hands the GPU over without shutting anything down.
"@
