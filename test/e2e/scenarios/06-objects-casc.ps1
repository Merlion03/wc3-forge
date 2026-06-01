# Object Editor — the CASC-tier surface the bare-CI Go suite can't reach. These
# load the stock base SLKs from the user's Warcraft III install (CASC), so they
# only run on a machine with WC3 (the runner skips this file under -SkipCasc).
# Covers all 7 kinds: list/fields_meta/get/set_field (stock OriginalEdit) +
# create_custom/delete_custom + the item-edit + leveled-field regressions, then
# save -> war3map.w3u on disk.
function Invoke-ObjectScenarios {
    param([Parameter(Mandatory)][pscustomobject]$Ctx)
    $b = $Ctx.Bridge
    $folder = Open-FreshFixture $b

    $kinds = @('units', 'items', 'abilities', 'buffs', 'destructables', 'doodads', 'upgrades')

    # list + fields_meta non-empty for every kind (proves distinct base SLKs load).
    foreach ($k in $kinds) {
        $l = Send-ForgeRpc $b "objects.$k.list" @{}
        if ($l.error) {
            # No CASC / no WC3 install on this machine — record once and bail out
            # of the whole object surface (every kind would fail identically).
            Check "objects.$k.list (CASC available?)" $false "needs Warcraft III/CASC: $($l.error.message). Re-run with WC3 installed, or use -SkipCasc."
            return
        }
        Check "objects.$k.list non-empty" ($l.result.Count -gt 0) "count=$($l.result.Count)"
        $fm = Send-ForgeRpc $b "objects.$k.fields_meta" @{}
        Check "objects.$k.fields_meta non-empty" (-not $fm.error -and $fm.result.Count -gt 0) "count=$($fm.result.Count)"
    }

    # get stock Paladin + classification.
    $pal = Send-ForgeRpc $b 'objects.units.get' @{ id = 'Hpal' }
    Check 'objects.units.get Hpal -> hero' (-not $pal.error -and $pal.result.id -eq 'Hpal' -and $pal.result.kind -eq 'hero') "$($pal.error.message)$($pal.result.kind)"
    Test-ForgeRpcErr $b 'objects.units.get unknown id' 'objects.units.get' @{ id = 'ZZZZ' } 'no units object'

    # set_field on a stock row -> OriginalEdit.
    Test-ForgeRpcOK $b 'objects.units.set_field Hpal hp=1234' 'objects.units.set_field' @{ id = 'Hpal'; column = 'hp'; value = '1234' } | Out-Null
    Test-ForgeRpcErr $b 'set_field unknown column' 'objects.units.set_field' @{ id = 'Hpal'; column = 'notacolumn'; value = 'x' } 'unknown field'

    # leveled-field gating: units (ShadowOpt=false) reject level>=1.
    Test-ForgeRpcErr $b 'units reject per-level edit' 'objects.units.set_field' @{ id = 'Hpal'; column = 'hp'; value = '1'; level = 1 } 'per-level'

    # items field-edit regression (items read the shared UnitMetaData.slk).
    $firstItem = (Send-ForgeRpc $b 'objects.items.list' @{}).result[0].id
    Test-ForgeRpcOK $b 'objects.items.set_field (v0.6.4 regression)' 'objects.items.set_field' @{ id = $firstItem; column = 'goldcost'; value = '777' } | Out-Null

    # create_custom (explicit + validation) then delete_custom.
    Test-ForgeRpcOK $b 'create_custom H999 from Hpal' 'objects.units.create_custom' @{ base_id = 'Hpal'; id = 'H999' } | Out-Null
    Check 'H999 present as custom' ([bool]((Send-ForgeRpc $b 'objects.units.list' @{}).result | Where-Object { $_.id -eq 'H999' -and $_.is_custom }))
    Test-ForgeRpcOK $b 'set_field on the custom' 'objects.units.set_field' @{ id = 'H999'; column = 'hp'; value = '5555' } | Out-Null
    Test-ForgeRpcErr $b 'create_custom duplicate id' 'objects.units.create_custom' @{ base_id = 'Hpal'; id = 'H999' } 'already exists'
    Test-ForgeRpcErr $b 'create_custom bad length' 'objects.units.create_custom' @{ base_id = 'Hpal'; id = 'AB' } '4'
    Test-ForgeRpcErr $b 'create_custom shadowing stock' 'objects.units.create_custom' @{ base_id = 'Hpal'; id = 'Hpal' } 'shadow'
    Test-ForgeRpcErr $b 'create_custom missing base_id' 'objects.units.create_custom' @{} 'base_id is required'
    Test-ForgeRpcOK $b 'delete_custom H999' 'objects.units.delete_custom' @{ id = 'H999' } | Out-Null
    Check 'H999 gone after delete' (-not ((Send-ForgeRpc $b 'objects.units.list' @{}).result | Where-Object { $_.id -eq 'H999' }))
    Test-ForgeRpcErr $b 'delete_custom stock errors' 'objects.units.delete_custom' @{ id = 'Hpal' } 'no custom'

    # object edits are undoable + save serializes war3map.w3u.
    Test-ForgeRpcOK $b 'undo object edit' 'history.undo' @{} | Out-Null
    Test-ForgeRpcOK $b 'save object mods' 'map.save' @{} | Out-Null
    $w3u = Join-Path $folder 'war3map.w3u'
    Check 'war3map.w3u written non-empty' ((Test-Path $w3u) -and (Get-Item $w3u).Length -gt 0)
}
