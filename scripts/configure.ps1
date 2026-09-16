$ErrorActionPreference = 'Stop'
$packageRoot = Split-Path -Parent $PSScriptRoot
$runtimeFile = Join-Path $packageRoot 'runtime.json'
if (-not (Test-Path -LiteralPath (Join-Path $packageRoot 'dist\server.mjs'))) { throw 'Please run Install.cmd once before Configure.cmd.' }
$nodePath = if (Test-Path -LiteralPath $runtimeFile) { (Get-Content -LiteralPath $runtimeFile -Raw -Encoding UTF8 | ConvertFrom-Json).node } elseif (Test-Path -LiteralPath (Join-Path $PSScriptRoot 'ensure-runtime.ps1')) { . (Join-Path $PSScriptRoot 'ensure-runtime.ps1'); Get-ProjectNode } else { (Get-Command node -ErrorAction Stop).Source }
& $nodePath (Join-Path $packageRoot 'dist/server.mjs') --configure
exit $LASTEXITCODE
