param([ValidateSet('plugin','desktop','installer')][string]$Target = 'desktop')
$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
Push-Location -LiteralPath $root
try {
    $goExe = & (Join-Path $PSScriptRoot 'ensure-go.ps1')
    $env:PATH = (Split-Path -Parent $goExe) + ';' + $env:PATH
    $release = Get-Content -LiteralPath 'packaging/release.json' -Raw -Encoding UTF8 | ConvertFrom-Json
    New-Item -ItemType Directory -Force -Path 'build/bin','bundle','release' | Out-Null
    # Go 的 embed 在依赖分析时也要求文件存在；第一次构建先放一个空 ZIP，再用白名单覆盖。
    if (-not (Test-Path -LiteralPath 'bundle/payload.zip')) { [IO.File]::WriteAllBytes((Join-Path $root 'bundle/payload.zip'), [byte[]](80,75,5,6,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0,0)) }
    & $goExe mod download
    if ($LASTEXITCODE) { throw 'Go dependency download failed.' }
    & $goExe build -trimpath -ldflags "-s -w -X github.com/U109/api-subagents/internal/buildinfo.Version=$($release.version)" -o 'build/bin/api-subagents-worker.exe' ./cmd/worker
    if ($LASTEXITCODE) { throw 'Worker build failed.' }
    & $goExe run ./cmd/package notices
    if ($LASTEXITCODE) { throw 'License collection failed.' }
    & $goExe run ./cmd/package stage
    if ($LASTEXITCODE) { throw 'Plugin package failed.' }
    & $goExe run ./cmd/package verify
    if ($LASTEXITCODE) { throw 'Package verification failed.' }
    if ($Target -eq 'plugin') { return }
    New-Item -ItemType Directory -Force -Path 'build/windows' | Out-Null
    Copy-Item -LiteralPath 'packaging/icon.ico' -Destination 'build/windows/icon.ico' -Force
    & $goExe run github.com/wailsapp/wails/v2/cmd/wails@v2.14.0 build -s -skipbindings -trimpath -platform windows/amd64 -webview2 download -ldflags "-s -w -X main.packaged=true -X github.com/U109/api-subagents/internal/buildinfo.Version=$($release.version)"
    if ($LASTEXITCODE) { throw 'Wails desktop build failed.' }
    if ($Target -eq 'installer') {
        $nsis = Get-Command makensis.exe -ErrorAction SilentlyContinue
        $nsisPath = if ($env:NSIS_EXE) { $env:NSIS_EXE } elseif ($nsis) { $nsis.Source } else { Join-Path ${env:ProgramFiles(x86)} 'NSIS/makensis.exe' }
        if (-not (Test-Path -LiteralPath $nsisPath)) { throw 'NSIS is required to build an installer. Install NSIS and rerun with -Target installer.' }
        & $nsisPath ("/DVERSION=" + $release.version) (Join-Path $root 'packaging/installer.nsi')
        if ($LASTEXITCODE) { throw 'Installer build failed.' }
        & $goExe run ./cmd/package metadata
        if ($LASTEXITCODE) { throw 'Update metadata generation failed.' }
    }
} finally { Pop-Location }
