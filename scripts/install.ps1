param([string]$ProfileRoot = [Environment]::GetFolderPath('UserProfile'), [switch]$SkipCodex)
$ErrorActionPreference = 'Stop'
$packageRoot = Split-Path -Parent $PSScriptRoot
$nodePath = (Get-Command node -ErrorAction Stop).Source
$nodeVersion = & $nodePath --version
if ([int]($nodeVersion.TrimStart('v').Split('.')[0]) -lt 20) { throw 'Node.js 20 or later is required.' }
$pluginName = 'api-subagents'
$profilePath = [IO.Path]::GetFullPath($ProfileRoot)
$target = [IO.Path]::GetFullPath((Join-Path $profilePath "plugins\$pluginName"))
if (-not $target.StartsWith($profilePath.TrimEnd('\') + '\', [StringComparison]::OrdinalIgnoreCase)) { throw 'Invalid plugin destination.' }
$marketplace = Join-Path $profilePath '.agents\plugins\marketplace.json'
$catalog = if (Test-Path -LiteralPath $marketplace) { Get-Content -LiteralPath $marketplace -Raw -Encoding UTF8 | ConvertFrom-Json } else { [pscustomobject]@{name='personal'; interface=[pscustomobject]@{displayName='Personal'}; plugins=@()} }
if ($catalog.name -notmatch '^[A-Za-z0-9_-]+$' -or $null -eq $catalog.plugins) { throw 'Invalid existing personal marketplace. No changes made.' }
$entry = @($catalog.plugins | Where-Object { $_.name -eq $pluginName })
if ($entry.Count -gt 1) { throw 'Duplicate plugin entries. No changes made.' }
if ($entry.Count -eq 1 -and ($entry[0].source.source -ne 'local' -or $entry[0].source.path -ne './plugins/api-subagents')) { throw 'An existing plugin with this name has another source. No changes made.' }
$utf8 = New-Object System.Text.UTF8Encoding($false)
New-Item -ItemType Directory -Force -Path $target | Out-Null
if ([IO.Path]::GetFullPath($packageRoot).TrimEnd('\') -ne $target.TrimEnd('\')) {
    foreach ($part in @('.codex-plugin','.mcp.json','dist','skills','scripts','src','tests','Configure.cmd','Install.cmd','README.md','package.json','package-lock.json','THIRD-PARTY-NOTICES.txt')) {
        $source = Join-Path $packageRoot $part
        if (Test-Path -LiteralPath $source) { Copy-Item -LiteralPath $source -Destination $target -Recurse -Force }
    }
}
$manifestFile = Join-Path $target '.codex-plugin\plugin.json'
$manifest = Get-Content -LiteralPath $manifestFile -Raw -Encoding UTF8 | ConvertFrom-Json
$manifest.version = ($manifest.version -split '\+')[0] + '+codex.' + [DateTime]::UtcNow.ToString('yyyyMMddHHmmss')
[IO.File]::WriteAllText($manifestFile, ($manifest | ConvertTo-Json -Depth 20), $utf8)
$mcpFile = Join-Path $target '.mcp.json'
$mcp = Get-Content -LiteralPath $mcpFile -Raw -Encoding UTF8 | ConvertFrom-Json
$mcp.mcpServers.'api-subagents'.command = $nodePath
[IO.File]::WriteAllText($mcpFile, ($mcp | ConvertTo-Json -Depth 20), $utf8)
[IO.File]::WriteAllText((Join-Path $target 'runtime.json'), (@{node=$nodePath} | ConvertTo-Json), $utf8)
if ($entry.Count -eq 0) {
    $catalog.plugins = @($catalog.plugins) + @([pscustomobject]@{name=$pluginName; source=[pscustomobject]@{source='local';path='./plugins/api-subagents'};policy=[pscustomobject]@{installation='AVAILABLE';authentication='ON_INSTALL'};category='Productivity'})
    New-Item -ItemType Directory -Force -Path (Split-Path -Parent $marketplace) | Out-Null
    if (Test-Path -LiteralPath $marketplace) { Copy-Item -LiteralPath $marketplace -Destination ($marketplace + '.backup-' + [DateTime]::UtcNow.ToString('yyyyMMddHHmmss')) }
    $temp = $marketplace + '.tmp-' + [Guid]::NewGuid().ToString('N')
    [IO.File]::WriteAllText($temp, ($catalog | ConvertTo-Json -Depth 30), $utf8)
    Move-Item -LiteralPath $temp -Destination $marketplace -Force
}
if (-not $SkipCodex) {
    $cli = Get-Command codex -ErrorAction SilentlyContinue
    if (-not $cli) {
        $binaries = Join-Path $env:LOCALAPPDATA 'OpenAI\Codex\bin'
        if (Test-Path -LiteralPath $binaries) { $cli = Get-ChildItem -LiteralPath $binaries -Filter codex.exe -Recurse | Sort-Object LastWriteTime -Descending | Select-Object -First 1 }
    }
    $cliPath = if ($cli.Source) { $cli.Source } elseif ($cli.FullName) { $cli.FullName } else { $null }
    if ($cliPath) { & $cliPath plugin add ($pluginName + '@' + $catalog.name); if ($LASTEXITCODE -ne 0) { Write-Warning 'Open Codex Plugins > Personal to install API Subagents.' } }
    else { Write-Host 'Open Codex Plugins > Personal to install API Subagents.' }
}
Write-Host "Plugin ready: $target"
Write-Host 'Next: double-click Configure.cmd, save a model, then start a new Codex task.'
