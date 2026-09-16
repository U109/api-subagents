param([string]$Executable)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
if (-not $Executable) { $Executable = Join-Path $root 'build/bin/API Subagents.exe' }
$testRoot = Join-Path $root ('.tools/smoke-' + [Guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Force -Path (Join-Path $testRoot 'config'),(Join-Path $testRoot 'codex') | Out-Null
$priorData = $env:API_SUBAGENTS_HOME
$priorCodex = $env:CODEX_HOME
try {
    # 使用完全隔离的数据与 Codex 目录；测试结束保留报告，便于排查失败。
    $env:API_SUBAGENTS_HOME = Join-Path $testRoot 'config'
    $env:CODEX_HOME = Join-Path $testRoot 'codex'
    $report = Join-Path $testRoot 'report.json'
    $process = Start-Process -FilePath $Executable -ArgumentList @('--smoke-report',('"' + $report + '"')) -WindowStyle Hidden -PassThru
    if (-not $process.WaitForExit(40000)) {
        Stop-Process -Id $process.Id -ErrorAction SilentlyContinue
        throw 'Isolated desktop startup timed out.'
    }
    if (-not (Test-Path -LiteralPath $report)) { throw 'The desktop did not produce its startup report.' }
    $result = Get-Content -LiteralPath $report -Raw -Encoding UTF8 | ConvertFrom-Json
    if (-not $result.ok -or -not $result.configLoaded -or $result.tabs -ne 4) { throw ('Desktop smoke failed: ' + ($result | ConvertTo-Json -Compress)) }
    Write-Host ('Verified WebView2, Go bindings and four configuration tabs: ' + $result.version)
} finally {
    $env:API_SUBAGENTS_HOME = $priorData
    $env:CODEX_HOME = $priorCodex
}
