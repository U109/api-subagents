param([string]$ProfileRoot, [switch]$SkipCodex)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$worker = Join-Path $root 'build/bin/api-subagents-worker.exe'
# 源码入口自动准备 Go 和依赖；已安装插件直接复用独立程序，不要求开发工具。
if (Test-Path -LiteralPath (Join-Path $root 'go.mod')) {
    & (Join-Path $PSScriptRoot 'build.ps1') -Target plugin
    if (-not (Test-Path -LiteralPath $worker)) { throw 'Worker build was not completed.' }
} else {
    $mcp = Get-Content -LiteralPath (Join-Path $root '.mcp.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    $worker = $mcp.mcpServers.'api-subagents'.command
    if (-not [IO.Path]::IsPathRooted($worker)) { $worker = Join-Path $root $worker }
}
$installArgs = @('--install','--source',$root)
if ($ProfileRoot) { $installArgs += @('--profile',$ProfileRoot) }
if ($SkipCodex) { $installArgs += '--skip-codex' }
& $worker @installArgs
if ($LASTEXITCODE) { throw 'Plugin installation failed.' }
Write-Host 'Plugin ready. Open Configure.cmd, save a model, then start a new Codex task.'
