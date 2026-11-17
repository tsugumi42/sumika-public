param([Parameter(Mandatory)][string]$Dir)
# 参考做法：按 services.json 读每个服务的日志，按 rules.json 统计，写 report.json / report.md，再按服务发布
$ErrorActionPreference = 'Stop'
Set-Location $Dir
$rules = Get-Content config/rules.json -Raw -Encoding utf8 | ConvertFrom-Json
$svcs = (Get-Content config/services.json -Raw -Encoding utf8 | ConvertFrom-Json).services
$fixText = Get-Content docs/fixes.md -Raw -Encoding utf8
$fixes = @{}
foreach ($m in [regex]::Matches($fixText, '(?m)^## (\w+)\s*\r?\n(.+)$')) { $fixes[$m.Groups[1].Value] = $m.Groups[2].Value.Trim() }
$names = @($rules.categories | ForEach-Object { $_.name })

function Read-Entries($file, $fmt) {
  foreach ($ln in (Get-Content $file -Encoding utf8)) {
    if ($ln.Trim() -eq '' -or $ln -match '^\s') { continue }
    switch ($fmt) {
      'iso' { $p = $ln -split ' ', 3; [pscustomobject]@{ level = $p[1]; msg = ($p[2] -split ': ', 2)[1] } }
      'syslog' { $m = [regex]::Match($ln, 'level=(\w+) msg="(.*)"$'); [pscustomobject]@{ level = $m.Groups[1].Value; msg = $m.Groups[2].Value } }
      'jsonl' { $j = $ln | ConvertFrom-Json; [pscustomobject]@{ level = $j.level; msg = $j.msg } }
      'bracket' { $m = [regex]::Match($ln, '^\[[^\]]*\] \((\w+)\) .*? - (.*)$'); [pscustomobject]@{ level = $m.Groups[1].Value; msg = $m.Groups[2].Value } }
    }
  }
}

$report = [ordered]@{}
foreach ($s in $svcs) {
  $c = [ordered]@{}
  foreach ($n in $names) { $c[$n] = 0 }
  $c['other'] = 0
  $total = 0
  foreach ($e in (Read-Entries $s.log $s.format)) {
    if ($rules.error_levels -notcontains $e.level.ToUpperInvariant()) { continue }
    $total++
    $hit = $null
    foreach ($cat in $rules.categories) {
      foreach ($w in $cat.match) { if ($e.msg.ToLowerInvariant().Contains($w)) { $hit = $cat.name; break } }
      if ($hit) { break }
    }
    if ($hit) { $c[$hit]++ } else { $c['other']++ }
  }
  $top = $names[0]
  foreach ($n in $names) { if ($c[$n] -gt $c[$top]) { $top = $n } }
  $report[$s.name] = [ordered]@{ total = $total; categories = $c; top = $top }
}
$enc = New-Object System.Text.UTF8Encoding($false)
[IO.File]::WriteAllText((Join-Path $Dir 'report.json'), ([ordered]@{ services = $report } | ConvertTo-Json -Depth 6), $enc)
$md = @('# 服务错误报告', '')
foreach ($s in $svcs) {
  $r = $report[$s.name]
  $md += "## $($s.name)", '', "最多的错误：$($r.top)（$($r.categories[$r.top]) 次）", '', $fixes[$r.top], ''
}
[IO.File]::WriteAllText((Join-Path $Dir 'report.md'), ($md -join "`n"), $enc)
foreach ($s in $svcs) { & (Join-Path $Dir 'tools\publish.ps1') -Name "service-$($s.name)" }
