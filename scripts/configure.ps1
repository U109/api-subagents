$ErrorActionPreference = 'Stop'
$packageRoot = Split-Path -Parent $PSScriptRoot
$runtimeFile = Join-Path $packageRoot 'runtime.json'
$nodePath = if (Test-Path -LiteralPath $runtimeFile) { (Get-Content -LiteralPath $runtimeFile -Raw -Encoding UTF8 | ConvertFrom-Json).node } else { (Get-Command node -ErrorAction Stop).Source }
& $nodePath (Join-Path $packageRoot 'dist/server.mjs') --configure
exit $LASTEXITCODE
