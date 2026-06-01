# History (undo/redo + transactional groups), view mode, gameplay constants, and
# the diagnostics health gate. CASC-free.
function Invoke-HistoryViewDiagScenarios {
    param([Parameter(Mandatory)][pscustomobject]$Ctx)
    $b = $Ctx.Bridge
    Open-FreshFixture $b | Out-Null

    $posOf = {
        param($cn)
        $e = (Send-ForgeRpc $b 'units.list' @{}).result.entities | Where-Object { $_.creation_number -eq $cn } | Select-Object -First 1
        return $e.position
    }

    # --- undo/redo: the core history regression ---
    $orig = & $posOf 21
    Test-ForgeRpcOK $b 'units.move (for history)' 'units.move' @{ creation_number = 21; x = 512; y = -256; z = 64 } | Out-Null
    Test-ForgeRpcOK $b 'history.undo' 'history.undo' @{} | Out-Null
    $undone = & $posOf 21
    Check 'undo restores original position' ($undone[0] -eq $orig[0] -and $undone[1] -eq $orig[1] -and $undone[2] -eq $orig[2]) "got $($undone -join ',') want $($orig -join ',')"
    Test-ForgeRpcOK $b 'history.redo' 'history.redo' @{} | Out-Null
    $redone = & $posOf 21
    Check 'redo re-applies position' ($redone[0] -eq 512 -and $redone[2] -eq 64) "got $($redone -join ',')"

    # undo on empty stack is a no-op (not an error).
    Test-ForgeRpcOK $b 'undo drains entry' 'history.undo' @{} | Out-Null
    Test-ForgeRpcOK $b 'undo on empty stack is no-op' 'history.undo' @{} | Out-Null

    # --- groups ---
    Test-ForgeRpcOK $b 'move before group' 'units.move' @{ creation_number = 21; x = 50; y = 50; z = 0 } | Out-Null
    $undoBefore = (Send-ForgeRpc $b 'history.list' @{}).result.undo.Count
    Test-ForgeRpcOK $b 'begin_group' 'history.begin_group' @{ label = 'Batch move' } | Out-Null
    $og = (Send-ForgeRpc $b 'history.list' @{}).result.open_group_depth
    Check 'open_group_depth=1 while open' ($og -eq 1) "got $og"
    Test-ForgeRpcErr $b 'undo blocked while group open' 'history.undo' @{} 'group'
    Test-ForgeRpcOK $b 'group move 1' 'units.move' @{ creation_number = 23; x = 100; y = 100; z = 0 } | Out-Null
    Test-ForgeRpcOK $b 'group move 2' 'units.move' @{ creation_number = 23; x = 200; y = 200; z = 0 } | Out-Null
    Test-ForgeRpcOK $b 'end_group' 'history.end_group' @{} | Out-Null
    $undoAfter = (Send-ForgeRpc $b 'history.list' @{}).result.undo.Count
    Check 'group collapses to exactly ONE undo entry' ($undoAfter -eq $undoBefore + 1) "before=$undoBefore after=$undoAfter"
    Test-ForgeRpcErr $b 'end_group without begin errors' 'history.end_group' @{} 'without'
    # abort recovers a dangling group.
    Test-ForgeRpcOK $b 'begin_group (dangling)' 'history.begin_group' @{ label = 'Dangling' } | Out-Null
    Test-ForgeRpcOK $b 'abort_group' 'history.abort_group' @{} | Out-Null
    Test-ForgeRpcOK $b 'undo works after abort' 'history.undo' @{} | Out-Null

    # --- view ---
    Test-ForgeRpcOK $b 'view.set_mode terrain' 'view.set_mode' @{ mode = 'terrain' } | Out-Null
    $gm = (Send-ForgeRpc $b 'view.get_mode' @{}).result.mode
    Check 'view.get_mode reads back terrain' ($gm -eq 'terrain') "got $gm"
    Test-ForgeRpcErr $b 'view.set_mode invalid' 'view.set_mode' @{ mode = 'banana' } 'terrain'
    Test-ForgeRpcErr $b 'view.set_mode missing' 'view.set_mode' @{} 'required'
    Test-ForgeRpcOK $b 'view doodad category visible' 'view.set_doodad_category_visible' @{ category = 'Trees/Destructibles'; visible = $false } | Out-Null

    # --- gameplay constants (map-backed, CASC-free) ---
    $ap = Send-ForgeRpc $b 'gameplay_constants.apply' @{ rows = @(@{ section = 'Misc'; key = 'GoldPerLumber'; value = '7' }) }
    Check 'gameplay_constants.apply rows_applied=1' (-not $ap.error -and $ap.result.rows_applied -eq 1) "$($ap.error.message)$($ap.result.rows_applied)"
    $rows = (Send-ForgeRpc $b 'gameplay_constants.get' @{}).result.rows
    $hit = $rows | Where-Object { $_.section -eq 'Misc' -and $_.key -eq 'GoldPerLumber' -and $_.value -eq '7' }
    Check 'applied gameplay constant reads back' ([bool]$hit)

    # --- diagnostics health gate ---
    $d = Send-ForgeRpc $b 'diagnostics.get' @{}
    Check 'diagnostics.get ok=false in headless' (-not $d.error -and -not $d.result.ok)
    Check 'diagnostics go.bridge.calls > 0' ($d.result.go.bridge.calls -gt 0) "calls=$($d.result.go.bridge.calls)"
    Check 'diagnostics go.bridge.panics == 0 (no handler panicked)' ($d.result.go.bridge.panics -eq 0) "panics=$($d.result.go.bridge.panics)"
    $arm = Send-ForgeRpc $b 'diagnostics.arm' @{ on = $true }
    Check 'diagnostics.arm on -> armed=true' (-not $arm.error -and $arm.result.armed)
    Test-ForgeRpcOK $b 'diagnostics.arm off' 'diagnostics.arm' @{ on = $false } | Out-Null

    # bridge.ping shape.
    $p = Send-ForgeRpc $b 'bridge.ping' @{ garbage = 'x' }
    Check 'bridge.ping ok + tolerates junk params' (-not $p.error -and $p.result.ok) $p.error.message
}
