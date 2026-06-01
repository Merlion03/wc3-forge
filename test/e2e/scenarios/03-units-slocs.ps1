# Units + start locations — list/move/get/create/set_field/delete + the start-
# location lifecycle. CASC-free (placed instances live in war3mapUnits.doo).
# Fixture: 10 entities incl. Paladin cn=21 (Hpal) at [-400,300,0]; start loc index 0.
function Invoke-UnitsScenarios {
    param([Parameter(Mandatory)][pscustomobject]$Ctx)
    $b = $Ctx.Bridge
    Open-FreshFixture $b | Out-Null

    # units.list returns the fixture entities under the 'entities' key.
    $list = Send-ForgeRpc $b 'units.list' @{}
    Check 'units.list returns entities array' (-not $list.error -and $list.result.entities.Count -ge 10) "count=$($list.result.entities.Count)"
    $start = $list.result.entities.Count

    # Paladin cn=21 present with the expected type + position.
    $pal = $list.result.entities | Where-Object { $_.creation_number -eq 21 } | Select-Object -First 1
    Check 'Paladin cn=21 is Hpal' ($pal -and $pal.type_id -eq 'Hpal') "type=$($pal.type_id)"
    Check 'Paladin at [-400,300,0]' ($pal -and $pal.position[0] -eq -400 -and $pal.position[1] -eq 300) "pos=$($pal.position -join ',')"

    # move + list reflects it.
    Test-ForgeRpcOK $b 'units.move cn=21' 'units.move' @{ creation_number = 21; x = 128; y = -256; z = 64 } | Out-Null
    $moved = (Send-ForgeRpc $b 'units.list' @{ filter = @{ type_id = 'Hpal' } }).result.entities | Where-Object { $_.creation_number -eq 21 } | Select-Object -First 1
    Check 'units.list reflects moved position' ($moved -and $moved.position[0] -eq 128 -and $moved.position[2] -eq 64) "pos=$($moved.position -join ',')"

    # create -> list grows.
    $created = Send-ForgeRpc $b 'units.create' @{ type_id = 'hfoo'; player = 0; x = 512; y = 512 }
    Check 'units.create returns a creation_number' (-not $created.error -and $created.result.creation_number -gt 0) $created.error.message
    $newCN = $created.result.creation_number
    $grown = (Send-ForgeRpc $b 'units.list' @{}).result.entities.Count
    Check 'list grew by 1 after create' ($grown -eq $start + 1) "got $grown want $($start+1)"

    # set_field gold + player persists.
    Test-ForgeRpcOK $b 'set_field gold' 'units.set_field' @{ creation_number = $newCN; field = 'gold'; value = 1234 } | Out-Null
    Test-ForgeRpcOK $b 'set_field player' 'units.set_field' @{ creation_number = $newCN; field = 'player'; value = 3 } | Out-Null

    # validation errors.
    Test-ForgeRpcErr $b 'create bad type_id len' 'units.create' @{ type_id = 'toolong'; player = 0; x = 0; y = 0 } 'must be exactly 4 bytes'
    Test-ForgeRpcErr $b 'create missing type_id' 'units.create' @{ player = 0; x = 0; y = 0 } 'type_id is required'
    Test-ForgeRpcErr $b 'set_field unknown field' 'units.set_field' @{ creation_number = $newCN; field = 'nonsense'; value = 1 } 'unknown unit instance field'
    Test-ForgeRpcErr $b 'move unknown cn' 'units.move' @{ creation_number = 888888; x = 0; y = 0; z = 0 } 'no unit with creation_number'

    # delete -> list shrinks back.
    Test-ForgeRpcOK $b 'units.delete' 'units.delete' @{ creation_number = $newCN } | Out-Null
    $shrunk = (Send-ForgeRpc $b 'units.list' @{}).result.entities.Count
    Check 'list shrank back after delete' ($shrunk -eq $start) "got $shrunk want $start"

    # start locations: list, create at a free index, move, delete.
    $sl0 = Send-ForgeRpc $b 'start_locations.list' @{}
    Check 'start_locations.list returns the fixture sloc' (-not $sl0.error -and $sl0.result.start_locations.Count -ge 1) "count=$($sl0.result.start_locations.Count)"
    $slStart = $sl0.result.start_locations.Count
    Test-ForgeRpcOK $b 'start_locations.create idx 1' 'start_locations.create' @{ index = 1; x = 1000; y = 2000 } | Out-Null
    Test-ForgeRpcErr $b 'create duplicate index errors' 'start_locations.create' @{ index = 0; x = 0; y = 0 } 'already exists'
    Test-ForgeRpcOK $b 'start_locations.move idx 1' 'start_locations.move' @{ index = 1; x = 1500; y = 2500 } | Out-Null
    Test-ForgeRpcOK $b 'start_locations.delete idx 1' 'start_locations.delete' @{ index = 1 } | Out-Null
    $slEnd = (Send-ForgeRpc $b 'start_locations.list' @{}).result.start_locations.Count
    Check 'start locations back to original count' ($slEnd -eq $slStart) "got $slEnd want $slStart"
}
