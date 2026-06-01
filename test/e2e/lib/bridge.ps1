# bridge.ps1 — reusable driver for wc3-forge's MCP bridge (NDJSON / JSON-RPC 2.0
# over a loopback TCP socket). Dot-source this from a scenario script or the
# release runner:
#
#     . "$PSScriptRoot/lib/bridge.ps1"
#     $b = Start-ForgeBridge -Bin <path-to-wc3-forge.exe>
#     try {
#         $r = Send-ForgeRpc $b 'map.open' @{ path = $folder }
#         Check 'map opens' (-not $r.error) $r.error.message
#     } finally { Stop-ForgeBridge $b }
#     Write-ForgeSummary   # prints N passed / M failed, sets $LASTEXITCODE-style
#
# WHY a raw TcpClient and not Invoke-RestMethod: the bridge speaks NDJSON, not
# HTTP. Invoke-RestMethod sends an HTTP request line ("POST / HTTP/1.1") which
# the bridge tries to parse as JSON and rejects with "invalid character 'P'".
# We frame exactly one JSON object + "\n" per request, one response line back.
#
# This library deliberately does NOT call Set-StrictMode — it is dot-sourced into
# the caller's session and shouldn't impose a mode on it. run_release.ps1 sets
# strict mode itself.

# --- check accumulator (the tiny assertion harness) --------------------------
$script:ForgeChecks = [System.Collections.Generic.List[object]]::new()

function Reset-ForgeChecks { $script:ForgeChecks = [System.Collections.Generic.List[object]]::new() }

# Check records one assertion. $ok true = pass. $detail is shown on failure.
function Check([string]$name, [bool]$ok, [string]$detail = '') {
    $script:ForgeChecks.Add([pscustomobject]@{ Name = $name; Ok = $ok; Detail = $detail })
    $tag = if ($ok) { 'PASS' } else { 'FAIL' }
    $line = "  [$tag] $name"
    if (-not $ok -and $detail) { $line += "  -> $detail" }
    Write-Host $line -ForegroundColor ($(if ($ok) { 'Green' } else { 'Red' }))
}

function Get-ForgeChecks { return $script:ForgeChecks }

# Write-ForgeSummary prints the tally and returns the failure count (0 = all green).
function Write-ForgeSummary {
    $all = $script:ForgeChecks
    $passed = @($all | Where-Object Ok).Count
    $failed = @($all | Where-Object { -not $_.Ok }).Count
    Write-Host ""
    Write-Host "==== $passed passed, $failed failed (of $($all.Count)) ====" -ForegroundColor ($(if ($failed -eq 0) { 'Green' } else { 'Red' }))
    if ($failed -gt 0) {
        Write-Host "Failures:" -ForegroundColor Red
        $all | Where-Object { -not $_.Ok } | ForEach-Object { Write-Host "  - $($_.Name): $($_.Detail)" -ForegroundColor Red }
    }
    return $failed
}

# --- bridge lifecycle --------------------------------------------------------

# Start-ForgeBridge launches a headless wc3-forge instance (MCP bridge only, no
# window), waits for its per-pid lockfile to appear, and returns a bridge object
# { Proc, Pid, Port, Token, LockDir }. Each call gets its OWN isolated lock dir
# (WC3FORGE_MCP_LOCK_DIR) so parallel runners never read each other's lockfile,
# and so we only ever kill OUR pid on teardown (never a sibling/interactive one).
function Start-ForgeBridge {
    param(
        [Parameter(Mandatory)][string]$Bin,
        [string]$Open,                 # optional map folder/.w3x to auto-load
        [int]$TimeoutSec = 25
    )
    if (-not (Test-Path $Bin)) { throw "Start-ForgeBridge: binary not found: $Bin" }
    $lockDir = Join-Path ([System.IO.Path]::GetTempPath()) ("wc3forge-e2e-lock-" + [System.Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Force $lockDir | Out-Null

    $args = @('--headless')
    if ($Open) { $args += @('--open', $Open) }

    $prevLockDir = $env:WC3FORGE_MCP_LOCK_DIR
    $env:WC3FORGE_MCP_LOCK_DIR = $lockDir
    try {
        $proc = Start-Process -FilePath $Bin -ArgumentList $args -PassThru
    } finally {
        # Restore the caller's env immediately; the child already inherited the override.
        if ($null -eq $prevLockDir) { Remove-Item Env:WC3FORGE_MCP_LOCK_DIR -ErrorAction SilentlyContinue }
        else { $env:WC3FORGE_MCP_LOCK_DIR = $prevLockDir }
    }

    $lockPath = Join-Path $lockDir "$($proc.Id).lock"
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    while (-not (Test-Path $lockPath) -and (Get-Date) -lt $deadline) {
        if ($proc.HasExited) { throw "Start-ForgeBridge: process exited early (code $($proc.ExitCode)) before writing a lockfile" }
        Start-Sleep -Milliseconds 120
    }
    if (-not (Test-Path $lockPath)) {
        Stop-Process -Id $proc.Id -Force -ErrorAction SilentlyContinue
        throw "Start-ForgeBridge: lockfile never appeared at $lockPath within ${TimeoutSec}s"
    }
    $lock = Get-Content $lockPath -Raw | ConvertFrom-Json
    return [pscustomobject]@{
        Proc    = $proc
        Pid     = $proc.Id
        Port    = [int]$lock.port
        Token   = [string]$lock.token
        LockDir = $lockDir
    }
}

# Stop-ForgeBridge kills ONLY the bridge's own pid (never a blanket kill) and
# removes its isolated lock dir.
function Stop-ForgeBridge {
    param([Parameter(Mandatory)][pscustomobject]$Bridge)
    if ($Bridge -and $Bridge.Pid) { Stop-Process -Id $Bridge.Pid -Force -ErrorAction SilentlyContinue }
    if ($Bridge -and $Bridge.LockDir -and (Test-Path $Bridge.LockDir)) {
        Remove-Item -Recurse -Force $Bridge.LockDir -ErrorAction SilentlyContinue
    }
}

$script:ForgeRpcId = 0

# Send-ForgeRpc sends one JSON-RPC request and returns the parsed response object
# (a PSCustomObject with .result and/or .error). Token auth is auto-injected into
# params._token. Each call uses a fresh connection (matches the bridge's
# per-request connection model).
function Send-ForgeRpc {
    param(
        [Parameter(Mandatory)][pscustomobject]$Bridge,
        [Parameter(Mandatory)][string]$Method,
        [hashtable]$Params,
        [int]$TimeoutMs = 8000
    )
    if ($null -eq $Params) { $Params = @{} }
    $p = @{} + $Params
    $p['_token'] = $Bridge.Token
    $script:ForgeRpcId++
    $req = @{ jsonrpc = '2.0'; id = $script:ForgeRpcId; method = $Method; params = $p } | ConvertTo-Json -Depth 20 -Compress

    $client = New-Object System.Net.Sockets.TcpClient
    try {
        $iar = $client.BeginConnect('127.0.0.1', $Bridge.Port, $null, $null)
        if (-not $iar.AsyncWaitHandle.WaitOne($TimeoutMs)) { throw "connect timeout to 127.0.0.1:$($Bridge.Port)" }
        $client.EndConnect($iar)
        $client.ReceiveTimeout = $TimeoutMs
        $client.SendTimeout = $TimeoutMs
        $stream = $client.GetStream()
        $writer = New-Object System.IO.StreamWriter($stream)
        $writer.NewLine = "`n"
        $writer.AutoFlush = $true
        $writer.WriteLine($req)
        $reader = New-Object System.IO.StreamReader($stream)
        $line = $reader.ReadLine()
        if ($null -eq $line) { throw "no response (empty line) for $Method" }
        return $line | ConvertFrom-Json
    } finally {
        $client.Close()
    }
}

# Test-ForgeRpcOK sends a call and Checks that it returned a non-error result.
# Returns the .result (or $null on error) so the caller can assert on fields.
function Test-ForgeRpcOK {
    param(
        [Parameter(Mandatory)][pscustomobject]$Bridge,
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Method,
        [hashtable]$Params
    )
    try {
        $r = Send-ForgeRpc $Bridge $Method $Params
    } catch {
        Check $Name $false "transport error: $_"
        return $null
    }
    if ($r.PSObject.Properties.Name -contains 'error' -and $r.error) {
        Check $Name $false "rpc error: $($r.error.message)"
        return $null
    }
    Check $Name $true
    return $r.result
}

# Test-ForgeRpcErr sends a call and Checks that it returned an error whose message
# matches $Pattern (regex). Used for the negative paths (e.g. stale-save refusal).
function Test-ForgeRpcErr {
    param(
        [Parameter(Mandatory)][pscustomobject]$Bridge,
        [Parameter(Mandatory)][string]$Name,
        [Parameter(Mandatory)][string]$Method,
        [hashtable]$Params,
        [string]$Pattern = '.'
    )
    try {
        $r = Send-ForgeRpc $Bridge $Method $Params
    } catch {
        Check $Name $false "transport error: $_"
        return
    }
    $hasErr = ($r.PSObject.Properties.Name -contains 'error' -and $r.error)
    if (-not $hasErr) { Check $Name $false "expected an error, got ok result"; return }
    if ($r.error.message -notmatch $Pattern) {
        Check $Name $false "error message '$($r.error.message)' did not match /$Pattern/"
        return
    }
    Check $Name $true
}
