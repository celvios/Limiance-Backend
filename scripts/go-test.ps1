param(
    [Parameter(ValueFromRemainingArguments = $true)]
    [string[]]$GoTestArguments
)

$ErrorActionPreference = 'Stop'
$workspace = Split-Path -Parent $PSScriptRoot
$cache = Join-Path $workspace '.cache\go-build'
New-Item -ItemType Directory -Force -Path $cache | Out-Null
$env:GOCACHE = $cache

$arguments = @('test')
if ($GoTestArguments.Count -eq 0) {
    $arguments += './...'
} else {
    $arguments += $GoTestArguments
}

& go @arguments
exit $LASTEXITCODE
