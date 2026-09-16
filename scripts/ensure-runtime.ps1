# 查找可用的 Node；缺失时下载固定版本并核对官方 SHA-256，不修改系统 PATH。
function Get-ProjectNode {
    param([switch]$Bundled, [switch]$Quiet)
    # npm 从 PowerShell 7 启动时可能继承不兼容的模块路径，优先加载当前宿主的系统模块。
    $env:PSModulePath = (Join-Path $PSHOME 'Modules') + ';' + $env:PSModulePath
    $root = Split-Path -Parent $PSScriptRoot
    $settings = Get-Content -LiteralPath (Join-Path $root 'desktop\release.json') -Raw -Encoding UTF8 | ConvertFrom-Json
    if ($settings.nodeVersion -notmatch '^\d+\.\d+\.\d+$') { throw 'Invalid bundled Node version.' }
    if (-not $Bundled) {
        $existing = Get-Command node.exe -ErrorAction SilentlyContinue
        if ($existing) {
            $version = & $existing.Source --version
            $npmCli = Join-Path (Split-Path -Parent $existing.Source) 'node_modules\npm\bin\npm-cli.js'
            if ($LASTEXITCODE -eq 0 -and [version]$version.TrimStart('v') -ge [version]'22.12.0' -and (Test-Path -LiteralPath $npmCli)) { return $existing.Source }
        }
    }
    $arch = if (-not $Bundled -and $env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'x64' }
    $name = 'node-v' + $settings.nodeVersion + '-win-' + $arch
    $cache = [IO.Path]::GetFullPath((Join-Path $root '.runtime'))
    $runtime = Join-Path $cache $name
    $node = Join-Path $runtime 'node.exe'
    if ((Test-Path -LiteralPath $node) -and (Test-Path -LiteralPath (Join-Path $runtime '.verified'))) { return $node }
    New-Item -ItemType Directory -Path $cache -Force | Out-Null
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
    $base = 'https://nodejs.org/dist/v' + $settings.nodeVersion
    if (-not $Quiet) { Write-Host ('Downloading verified Node.js ' + $settings.nodeVersion + '...') }
    $checksums = (Invoke-WebRequest -UseBasicParsing -Uri ($base + '/SHASUMS256.txt') -TimeoutSec 60).Content
    $match = [regex]::Match($checksums, '(?m)^([a-f0-9]{64})\s+' + [regex]::Escape($name + '.zip') + '\r?$')
    if (-not $match.Success) { throw 'Official Node checksum not found.' }
    $archive = Join-Path $cache ($name + '.zip')
    if (-not (Test-Path -LiteralPath $archive) -or (Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $match.Groups[1].Value) {
        Invoke-WebRequest -UseBasicParsing -Uri ($base + '/' + $name + '.zip') -OutFile $archive -TimeoutSec 300
    }
    if ((Get-FileHash -LiteralPath $archive -Algorithm SHA256).Hash -ne $match.Groups[1].Value) { throw 'Node download checksum mismatch. Run Install.cmd again to retry.' }
    Expand-Archive -LiteralPath $archive -DestinationPath $cache -Force
    if (-not (Test-Path -LiteralPath $node)) { throw 'Node archive is incomplete.' }
    [IO.File]::WriteAllText((Join-Path $runtime '.verified'), $match.Groups[1].Value)
    return $node
}
