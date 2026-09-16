param([string]$ProfileRoot = [Environment]::GetFolderPath('UserProfile'), [switch]$SkipCodex)
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
try {
    # 发布的插件已带构建结果；源码入口才需要下载依赖和编译。
    if ((Test-Path -LiteralPath (Join-Path $root 'src\server.mjs')) -and -not (Test-Path -LiteralPath (Join-Path $root 'runtime.json'))) {
        . (Join-Path $PSScriptRoot 'ensure-runtime.ps1')
        $nodePath = Get-ProjectNode
        $npmCli = Join-Path (Split-Path -Parent $nodePath) 'node_modules\npm\bin\npm-cli.js'
        if (-not (Test-Path -LiteralPath $npmCli)) { throw 'npm was not found next to Node.js. Reinstall Node.js or use the desktop app.' }
        Push-Location -LiteralPath $root
        $previousSkip = $env:ELECTRON_SKIP_BINARY_DOWNLOAD
        $previousPath = $env:PATH
        try {
            $env:PATH = (Split-Path -Parent $nodePath) + ';' + $env:PATH
            # 插件源码安装不需要桌面壳的二进制，避免每次下载整个 Electron。
            $env:ELECTRON_SKIP_BINARY_DOWNLOAD = '1'
            Write-Host '[1/3] Installing dependencies...'
            & $nodePath $npmCli ci --no-audit --no-fund
            if ($LASTEXITCODE -ne 0) { throw 'npm ci failed. Check your network and run Install.cmd again.' }
            Write-Host '[2/3] Building plugin...'
            & $nodePath $npmCli run build
            if ($LASTEXITCODE -ne 0) { throw 'Plugin build failed.' }
        } finally {
            $env:ELECTRON_SKIP_BINARY_DOWNLOAD = $previousSkip
            $env:PATH = $previousPath
            Pop-Location
        }
    } else {
        $runtimeFile = Join-Path $root 'runtime.json'
        $nodePath = if (Test-Path -LiteralPath $runtimeFile) { (Get-Content -LiteralPath $runtimeFile -Raw -Encoding UTF8 | ConvertFrom-Json).node } else { (Get-Command node -ErrorAction Stop).Source }
    }
    Write-Host '[3/3] Installing into Codex...'
    & (Join-Path $PSScriptRoot 'install.ps1') -ProfileRoot $ProfileRoot -NodePath $nodePath -SkipCodex:$SkipCodex
} catch {
    Write-Host ('Installation failed: ' + $_.Exception.Message) -ForegroundColor Red
    exit 1
}
