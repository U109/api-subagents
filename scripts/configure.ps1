$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
# 源码和已安装插件共用配置入口，始终使用相同本机配置目录。
if (Test-Path -LiteralPath (Join-Path $root 'go.mod')) {
    $worker = Join-Path $root 'build/bin/api-subagents-worker.exe'
    if (-not (Test-Path -LiteralPath $worker)) { & (Join-Path $PSScriptRoot 'build.ps1') -Target plugin }
} else {
    $mcp = Get-Content -LiteralPath (Join-Path $root '.mcp.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    $worker = $mcp.mcpServers.'api-subagents'.command
    if (-not [IO.Path]::IsPathRooted($worker)) { $worker = Join-Path $root $worker }
}
& $worker --configure
if ($LASTEXITCODE) { throw 'Configuration service failed.' }
