param([Parameter(Mandatory)][string]$Dir)
$ErrorActionPreference = 'Stop'
Set-Location $Dir
$utf8 = New-Object System.Text.UTF8Encoding($false)
$secMap = @{ server = 'server'; db = 'database'; log = 'logging'; feature = 'features' }
$keyMap = @{ 'db.pool_size' = 'pool'; 'log.file' = 'path'; 'feature.beta_ui' = 'betaUi' }
$drop = @('server.legacy_mode', 'db.legacy_charset')
$lists = @('server.allowed_hosts', 'feature.enabled')
function Read-Ini($name) {
  $res = [ordered]@{}
  $sec = ''
  foreach ($line in [IO.File]::ReadAllLines((Join-Path $Dir "config/$name.ini"), $utf8)) {
    $l = $line.Trim()
    if (-not $l -or $l.StartsWith(';') -or $l.StartsWith('#')) { continue }
    if ($l -match '^\[(.+)\]$') { $sec = $Matches[1]; continue }
    $k, $v = $l -split '\s*=\s*', 2
    $full = "$sec.$k"
    if ($drop -contains $full) { continue }
    if ($lists -contains $full) { $val = [object[]]@($v -split '\s*,\s*') }
    elseif ($v -match '^"(.*)"$') { $val = $Matches[1] }
    elseif ($v -match '^-?\d+$') { $val = [int]$v }
    elseif ($v -eq 'true') { $val = $true }
    elseif ($v -eq 'false') { $val = $false }
    else { $val = $v }
    $newKey = if ($keyMap.ContainsKey($full)) { $keyMap[$full] } else { $k }
    $res["$($secMap[$sec]).$newKey"] = $val
  }
  return $res
}
function Nest($flat, $only) {
  $out = [ordered]@{}
  foreach ($p in $flat.Keys) {
    if ($null -ne $only -and -not ($only -contains $p)) { continue }
    $sec, $key = $p -split '\.', 2
    if (-not $out.Contains($sec)) { $out[$sec] = [ordered]@{} }
    $out[$sec][$key] = $flat[$p]
  }
  return $out
}
$ini = @{}
foreach ($n in 'dev', 'test', 'prod') { $ini[$n] = Read-Ini $n }
$cfg = [ordered]@{ version = 1; defaults = (Nest $ini['dev'] $null); environments = [ordered]@{} }
foreach ($n in 'dev', 'test', 'prod') {
  $diff = @($ini[$n].Keys | Where-Object { (ConvertTo-Json $ini[$n][$_] -Compress) -ne (ConvertTo-Json $ini['dev'][$_] -Compress) })
  $cfg.environments[$n] = [ordered]@{ overrides = (Nest $ini[$n] $diff) }
}
[IO.File]::WriteAllText((Join-Path $Dir 'config.json'), ((ConvertTo-Json $cfg -Depth 10) + "`n"), $utf8)
Remove-Item (Join-Path $Dir 'config') -Recurse
& (Join-Path $Dir 'tools\publish.ps1') -Name dev

$readme = [IO.File]::ReadAllText((Join-Path $Dir 'README.md'), $utf8)
$newSec = "## 配置`n`n三个环境的配置合并在根目录的 config.json 里：defaults 是基准（dev 环境），每个环境的 overrides 只写和 defaults 不同的键，结构见 docs/config-schema.md。`n`n运行 tools/show_ports.ps1 可以打印每个环境的端口。`n`n"
$readme = [regex]::Replace($readme, '(?s)## 配置\r?\n.*?(?=## 本地开发)', { param($m) $newSec })
[IO.File]::WriteAllText((Join-Path $Dir 'README.md'), $readme, $utf8)
& (Join-Path $Dir 'tools\publish.ps1') -Name test

$ps = @'
$cfg = Get-Content (Join-Path $PSScriptRoot '..\config.json') -Raw -Encoding utf8 | ConvertFrom-Json
foreach ($n in 'dev', 'test', 'prod') {
  $o = $cfg.environments.$n.overrides
  $port = if ($o.server -and $null -ne $o.server.port) { $o.server.port } else { $cfg.defaults.server.port }
  "$n $port"
}
'@
[IO.File]::WriteAllText((Join-Path $Dir 'tools/show_ports.ps1'), $ps + "`n", $utf8)
& (Join-Path $Dir 'tools\show_ports.ps1')
& (Join-Path $Dir 'tools\publish.ps1') -Name prod
