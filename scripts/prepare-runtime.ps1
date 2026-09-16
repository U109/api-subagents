$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'ensure-runtime.ps1')
[Console]::OutputEncoding = New-Object System.Text.UTF8Encoding($false)
# 静默下载，使标准输出只包含供构建脚本读取的 JSON。
$node = Get-ProjectNode -Bundled -Quiet
@{node=$node} | ConvertTo-Json -Compress
