# Regions (war3map.w3r) + Import Manager (war3map.imp) — both start empty on the
# fixture and are fully CASC-free. Includes the v1.0.0 baseline-refresh edge:
# a map.save right after imports.add must NOT spuriously refuse with "changed on
# disk" (the direct write refreshes the change-detection baseline).
function Invoke-RegionsImportsScenarios {
    param([Parameter(Mandatory)][pscustomobject]$Ctx)
    $b = $Ctx.Bridge
    $folder = Open-FreshFixture $b

    # --- Regions ---
    $empty = Send-ForgeRpc $b 'regions.list' @{}
    Check 'regions.list empty on fresh fixture' (-not $empty.error -and $empty.result.regions.Count -eq 0) "count=$($empty.result.regions.Count)"

    $created = Send-ForgeRpc $b 'regions.create' @{ name = 'SpawnZone'; min_x = -512; min_y = -256; max_x = 512; max_y = 256 }
    Check 'regions.create returns creation_number' (-not $created.error -and $created.result.creation_number -ge 1) $created.error.message
    $cn = $created.result.creation_number

    $afterCreate = (Send-ForgeRpc $b 'regions.list' @{}).result.regions
    Check 'region appears in list' ($afterCreate.Count -eq 1 -and $afterCreate[0].name -eq 'SpawnZone') "count=$($afterCreate.Count)"

    # move preserves size 1024x512.
    Test-ForgeRpcOK $b 'regions.move' 'regions.move' @{ creation_number = $cn; dx = 100; dy = 50 } | Out-Null
    $moved = (Send-ForgeRpc $b 'regions.get' @{ creation_number = $cn }).result
    Check 'move preserves size (1024x512)' (($moved.max_x - $moved.min_x) -eq 1024 -and ($moved.max_y - $moved.min_y) -eq 512) "w=$($moved.max_x-$moved.min_x) h=$($moved.max_y-$moved.min_y)"

    # resize normalizes inverted corners.
    Test-ForgeRpcOK $b 'regions.resize (inverted)' 'regions.resize' @{ creation_number = $cn; min_x = 200; min_y = 300; max_x = -200; max_y = -300 } | Out-Null
    $resized = (Send-ForgeRpc $b 'regions.get' @{ creation_number = $cn }).result
    Check 'resize normalizes so min<=max' ($resized.min_x -le $resized.max_x -and $resized.min_y -le $resized.max_y) "$($resized.min_x)..$($resized.max_x)"

    Test-ForgeRpcOK $b 'regions.rename' 'regions.rename' @{ creation_number = $cn; name = 'Boss Arena' } | Out-Null

    # negatives.
    Test-ForgeRpcErr $b 'regions.get bad cn' 'regions.get' @{ creation_number = 99999 } 'creation_number'
    Test-ForgeRpcErr $b 'regions.create empty name' 'regions.create' @{ name = ''; min_x = 0; min_y = 0; max_x = 1; max_y = 1 } 'name'

    # save -> war3map.w3r, reopen persists.
    Test-ForgeRpcOK $b 'save regions' 'map.save' @{} | Out-Null
    $w3r = Join-Path $folder 'war3map.w3r'
    Check 'war3map.w3r written non-empty' ((Test-Path $w3r) -and (Get-Item $w3r).Length -gt 0)
    Test-ForgeRpcOK $b 'close' 'map.close' @{} | Out-Null
    Test-ForgeRpcOK $b 'reopen' 'map.open' @{ path = $folder } | Out-Null
    $reopened = (Send-ForgeRpc $b 'regions.list' @{}).result.regions
    Check 'region persists across save+reopen' ($reopened.Count -eq 1)

    # delete.
    Test-ForgeRpcOK $b 'regions.delete' 'regions.delete' @{ creation_number = $reopened[0].creation_number } | Out-Null
    Check 'region list empty after delete' ((Send-ForgeRpc $b 'regions.list' @{}).result.regions.Count -eq 0)

    # --- Imports ---
    Open-FreshFixture $b | Out-Null  # fresh map for imports
    $i0 = Send-ForgeRpc $b 'imports.list' @{}
    Check 'imports.list empty on fresh fixture' (-not $i0.error -and $i0.result.imports.Count -eq 0) "count=$($i0.result.imports.Count)"

    $b64 = 'aGVsbG8gZTJlIHdvcmxk' # "hello e2e world" (15 bytes)
    Test-ForgeRpcOK $b 'imports.add' 'imports.add' @{ path = 'war3mapImported\test.txt'; data_base64 = $b64 } | Out-Null
    Check 'imports.list shows 1' ((Send-ForgeRpc $b 'imports.list' @{}).result.imports.Count -eq 1)
    Test-ForgeRpcOK $b 'imports.rename' 'imports.rename' @{ old_path = 'war3mapImported\test.txt'; new_path = 'war3mapImported\renamed.txt' } | Out-Null

    # negatives.
    Test-ForgeRpcErr $b 'imports.add traversal rejected' 'imports.add' @{ path = '..\..\evil.txt'; data_base64 = $b64 } 'unsafe'
    Test-ForgeRpcErr $b 'imports.add empty path' 'imports.add' @{ path = ''; data_base64 = $b64 } 'required'
    Test-ForgeRpcErr $b 'imports.rename missing old' 'imports.rename' @{ old_path = 'war3mapImported\nope.dat'; new_path = 'war3mapImported\x.dat' } 'no imported file'

    # v1.0.0 baseline-refresh edge: save right after a direct import write must NOT refuse.
    $s1 = Send-ForgeRpc $b 'map.save' @{}
    Check 'save after imports.* NOT spuriously refused' (-not $s1.error) $s1.error.message
    $s2 = Send-ForgeRpc $b 'map.save' @{}
    Check 'second consecutive save also clean' (-not $s2.error) $s2.error.message

    Test-ForgeRpcOK $b 'imports.remove' 'imports.remove' @{ path = 'war3mapImported\renamed.txt' } | Out-Null
    Check 'imports list empty after remove' ((Send-ForgeRpc $b 'imports.list' @{}).result.imports.Count -eq 0)
}
