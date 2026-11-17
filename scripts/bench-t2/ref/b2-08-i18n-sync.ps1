param([Parameter(Mandatory)][string]$Dir)
$ErrorActionPreference = 'Stop'
Set-Location $Dir
$pub = Join-Path $Dir 'tools\publish.ps1'
$utf8 = New-Object Text.UTF8Encoding $false
function New-Ordinal { [System.Collections.Specialized.OrderedDictionary]::new([StringComparer]::Ordinal) }
function Load([string]$name) {
  $o = New-Ordinal
  $j = [IO.File]::ReadAllText((Join-Path $Dir "locales\$name.json"), [Text.Encoding]::UTF8) | ConvertFrom-Json
  foreach ($p in $j.PSObject.Properties) { $o[$p.Name] = [string]$p.Value }
  return $o
}
$zh = Load 'zh'
function Sync($lang, $fix) {
  $old = Load $lang
  foreach ($k in @($fix.Keys)) { if ($old.Contains($k)) { $old[$fix[$k]] = $old[$k]; $old.Remove($k) } }
  $new = New-Ordinal
  foreach ($k in $zh.Keys) {
    if ($old.Contains($k)) { $new[$k] = $old[$k] } else { $new[$k] = 'TODO:' + $zh[$k] }
  }
  [IO.File]::WriteAllText((Join-Path $Dir "locales\$lang.json"), ((ConvertTo-Json $new) + "`n"), $utf8)
  $todo = @($new.Values | Where-Object { $_.StartsWith('TODO:') }).Count
  return @{ keys = $new.Count; todo = $todo }
}
$en = Sync 'en' @{ 'settings.titel' = 'settings.title'; 'task.titlePlaceholer' = 'task.titlePlaceholder'; 'error.notfound' = 'error.notFound' }
& $pub -Name en
$ja = Sync 'ja' @{ 'nav.setings' = 'nav.settings'; 'task.status.runing' = 'task.status.running' }
& $pub -Name ja
$stats = [ordered]@{ base = 'zh'; total = $zh.Count; languages = [ordered]@{ en = [ordered]@{ keys = $en.keys; todo = $en.todo }; ja = [ordered]@{ keys = $ja.keys; todo = $ja.todo } } }
[IO.File]::WriteAllText((Join-Path $Dir 'locales\stats.json'), ((ConvertTo-Json $stats -Depth 5) + "`n"), $utf8)
'参考做法完成'
