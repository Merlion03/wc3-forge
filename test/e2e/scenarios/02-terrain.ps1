# Terrain — set_height/get_tile round-trip, set_tile (palette-aware), brushes, and
# out-of-range handling. All CASC-free (terrain lives in war3map.w3e). The fixture
# grid is 9x9 (valid col/row 0..8) with a 6-entry ground palette.
function Invoke-TerrainScenarios {
    param([Parameter(Mandatory)][pscustomobject]$Ctx)
    $b = $Ctx.Bridge
    $folder = Open-FreshFixture $b

    # set_height -> get_tile reflects it (heights are quantized to ~0.25 steps).
    Test-ForgeRpcOK $b 'set_height (0,0)=128' 'terrain.set_height' @{ col = 0; row = 0; height = 128.0 } | Out-Null
    $t = Send-ForgeRpc $b 'terrain.get_tile' @{ col = 0; row = 0 }
    Check 'get_tile reflects set height ~128' (-not $t.error -and $t.result.height -ge 127 -and $t.result.height -le 129) "height=$($t.result.height)"

    # set_tile to a different in-use palette FourCC (must already be in the palette).
    $orig = (Send-ForgeRpc $b 'terrain.get_tile' @{ col = 0; row = 0 }).result.ground_tile_id
    $other = $null
    foreach ($c in @(@(8, 8), @(4, 4), @(1, 1), @(8, 0), @(0, 8))) {
        $g = (Send-ForgeRpc $b 'terrain.get_tile' @{ col = $c[0]; row = $c[1] }).result.ground_tile_id
        if ($g -and $g -ne $orig) { $other = $g; break }
    }
    if ($other) {
        Test-ForgeRpcOK $b 'set_tile to a distinct palette id' 'terrain.set_tile' @{ col = 0; row = 0; ground_tile_id = $other } | Out-Null
        $painted = (Send-ForgeRpc $b 'terrain.get_tile' @{ col = 0; row = 0 }).result.ground_tile_id
        Check 'set_tile reflected in get_tile' ($painted -eq $other) "got $painted want $other"
    }

    # Brushes over a footprint.
    Test-ForgeRpcOK $b 'brush_height raise' 'terrain.brush_height' @{ col = 4; row = 4; radius = 2; mode = 'raise'; strength = 16 } | Out-Null
    Test-ForgeRpcOK $b 'brush_water add' 'terrain.brush_water' @{ col = 3; row = 3; radius = 1; mode = 'add' } | Out-Null

    # Out-of-range corner errors gracefully (process stays alive).
    Test-ForgeRpcErr $b 'get_tile out-of-range errors' 'terrain.get_tile' @{ col = 9999; row = 9999 } '.'

    # Save persists w3e.
    Test-ForgeRpcOK $b 'save terrain edits' 'map.save' @{} | Out-Null
    Check 'war3map.w3e written' (Test-Path (Join-Path $folder 'war3map.w3e'))
}
