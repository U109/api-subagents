$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent $PSScriptRoot
$release = Get-Content -LiteralPath (Join-Path $root 'packaging/release.json') -Raw -Encoding UTF8 | ConvertFrom-Json
# 优先复用已安装的 Go；源码构建最低版本与 go.mod 一致，不修改系统 PATH。
$candidates = @((Join-Path $root '.tools/go/bin/go.exe'))
$systemGo = Get-Command go.exe -ErrorAction SilentlyContinue
if ($systemGo) { $candidates += $systemGo.Source }
foreach ($candidate in $candidates) {
    if (Test-Path -LiteralPath $candidate) {
        $versionText = & $candidate version
        if ($LASTEXITCODE -eq 0 -and $versionText -match 'go(\d+)\.(\d+)') {
            if ([int]$Matches[1] -gt 1 -or ([int]$Matches[1] -eq 1 -and [int]$Matches[2] -ge 26)) { return $candidate }
        }
    }
}
Write-Host 'Preparing verified Go build tools. First run requires an internet connection...'
$toolsRoot = Join-Path $root '.tools'
New-Item -ItemType Directory -Force -Path $toolsRoot | Out-Null
# 使用官方元数据校验精确版本的 SHA-256，代理仅来自当前进程或 Windows 网络设置。
$request = @{UseBasicParsing=$true;TimeoutSec=180}
if ($env:HTTPS_PROXY) { $request.Proxy = $env:HTTPS_PROXY }
$metadata = (Invoke-WebRequest @request -Uri 'https://go.dev/dl/?mode=json&include=all').Content | ConvertFrom-Json
$wanted = @($metadata | Where-Object version -eq ('go' + $release.goVersion))
if ($wanted.Count -ne 1) { throw 'Pinned Go version was not found in official metadata.' }
$archive = @($wanted[0].files | Where-Object { $_.os -eq 'windows' -and $_.arch -eq 'amd64' -and $_.kind -eq 'archive' })[0]
$zipPath = Join-Path $toolsRoot ('go-' + [Guid]::NewGuid().ToString('N') + '.zip')
try {
    Invoke-WebRequest @request -Uri ('https://go.dev/dl/' + $archive.filename) -OutFile $zipPath
    if ((Get-FileHash -LiteralPath $zipPath -Algorithm SHA256).Hash.ToLowerInvariant() -ne $archive.sha256) { throw 'Go download checksum mismatch.' }
    Expand-Archive -LiteralPath $zipPath -DestinationPath $toolsRoot -Force
} finally { if (Test-Path -LiteralPath $zipPath) { Remove-Item -LiteralPath $zipPath } }
return (Join-Path $toolsRoot 'go/bin/go.exe')
