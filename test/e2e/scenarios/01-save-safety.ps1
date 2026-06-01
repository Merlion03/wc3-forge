# Save-safety — the v1.0.0 data-integrity headline, end-to-end against the .exe:
# atomic save + backup-on-save, external-change refusal (with the committed
# marker), force-overwrite, and round-trip. NOTE: the staleness guard only checks
# files in THIS save's dirty write set, so we dirty the SAME file we tamper
# (terrain.set_height -> war3map.w3e).
function Invoke-SaveSafetyScenarios {
    param([Parameter(Mandatory)][pscustomobject]$Ctx)
    $b = $Ctx.Bridge
    $folder = Open-FreshFixture $b
    $w3e = Join-Path $folder 'war3map.w3e'
    $bak = "$w3e.bak"

    # Clean save #1 -> writes w3e + establishes a fresh baseline.
    Test-ForgeRpcOK $b 'edit terrain (dirty w3e)' 'terrain.set_height' @{ col = 0; row = 0; height = 64.0 } | Out-Null
    Test-ForgeRpcOK $b 'save #1 (clean)' 'map.save' @{} | Out-Null
    Check 'save #1 wrote war3map.w3e' (Test-Path $w3e)
    Check 'save #1 created .bak of prior bytes' (Test-Path $bak)

    # External tool changes w3e on disk.
    $orig = [System.IO.File]::ReadAllBytes($w3e)
    $tampered = $orig + ([byte[]](1, 2, 3, 4))
    [System.IO.File]::WriteAllBytes($w3e, $tampered)

    # Dirty w3e again, then save -> REFUSED, tampered bytes untouched.
    Test-ForgeRpcOK $b 'edit terrain again' 'terrain.set_height' @{ col = 1; row = 1; height = 96.0 } | Out-Null
    Test-ForgeRpcErr $b 'save #2 refused (changed on disk)' 'map.save' @{} 'changed on disk since it was opened'
    $afterRefuse = [System.IO.File]::ReadAllBytes($w3e)
    Check 'refused save left tampered bytes untouched' ($afterRefuse.Length -eq $tampered.Length) "len=$($afterRefuse.Length) want $($tampered.Length)"

    # Force overwrite -> succeeds, prior (tampered) bytes backed up.
    Test-ForgeRpcOK $b 'save #3 force overwrite' 'map.save' @{ force = $true } | Out-Null
    $bakLen = ([System.IO.File]::ReadAllBytes($bak)).Length
    Check 'force-save .bak holds the clobbered (tampered) bytes' ($bakLen -eq $tampered.Length) "bak=$bakLen tampered=$($tampered.Length)"

    # Round-trip: close + reopen parses.
    Test-ForgeRpcOK $b 'close' 'map.close' @{} | Out-Null
    $reopen = Send-ForgeRpc $b 'map.open' @{ path = $folder }
    Check 'reopen after forced save round-trips' (-not $reopen.error -and $reopen.result.loaded) $reopen.error.message

    # map.save tolerates absent/null params (the historical no-arg call).
    Test-ForgeRpcOK $b 'save tolerates empty params' 'map.save' @{} | Out-Null

    # SaveAs to a fresh .w3x; bad extension rejected.
    $w3x = Join-Path ([System.IO.Path]::GetDirectoryName($folder)) 'exported.w3x'
    Test-ForgeRpcOK $b 'save_as to .w3x' 'map.save_as' @{ path = $w3x } | Out-Null
    Check 'save_as produced a .w3x file' (Test-Path $w3x)
    Test-ForgeRpcErr $b 'save_as rejects non-.w3x extension' 'map.save_as' @{ path = (Join-Path ([System.IO.Path]::GetDirectoryName($folder)) 'bad.txt') } 'must end in'
}
