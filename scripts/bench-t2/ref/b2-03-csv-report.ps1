param([Parameter(Mandatory)][string]$Dir)
# 参考做法：读规则、读五个 CSV、按规则清洗，写三个输出文件，每写完一个发布一次
$ErrorActionPreference = 'Stop'
Set-Location $Dir
$ci = [Globalization.CultureInfo]::InvariantCulture
$rules = Get-Content config/rules.json -Raw -Encoding utf8 | ConvertFrom-Json
$cats = Get-Content config/categories.json -Raw -Encoding utf8 | ConvertFrom-Json
$alias = @{}
foreach ($p in $cats.aliases.PSObject.Properties) { $alias[$p.Name] = $p.Value }
$formats = @{
  'YYYY-MM-DD' = 'yyyy-MM-dd'; 'DD/MM/YYYY' = 'dd/MM/yyyy'; 'MMM D, YYYY' = 'MMM d, yyyy'
  'YYYY/M/D' = 'yyyy/M/d'; 'DD.MM.YY' = 'dd.MM.yy'
}
$seen = @{}
$rows = @()
$rej = @()
foreach ($s in $rules.sources) {
  foreach ($r in (Import-Csv -Path $s.file -Encoding utf8)) {
    $a = $r.amount
    foreach ($x in $rules.amount.strip) { $a = $a.Replace($x, '') }
    $a = $a.Trim()
    $c = $r.category.Trim().ToLowerInvariant()
    if ($alias.ContainsKey($c)) { $c = $alias[$c] }
    if ($a -eq '') { $rej += "$($r.id),missing-amount"; continue }
    if ($c -eq '') { $rej += "$($r.id),missing-category"; continue }
    if ($cats.known -notcontains $c) { $rej += "$($r.id),unknown-category"; continue }
    if ($seen.ContainsKey($r.id)) { $rej += "$($r.id),duplicate"; continue }
    $seen[$r.id] = $true
    $d = [datetime]::ParseExact($r.date.Trim(), $formats[$s.date_format], $ci).ToString('yyyy-MM-dd')
    $rows += [pscustomobject]@{ id = $r.id; date = $d; category = $c; amount = [decimal]::Parse($a, $ci); owner = $r.owner }
  }
}
$rows = $rows | Sort-Object @{Expression = 'date'}, @{Expression = 'id'} -Culture 'en-US' -CaseSensitive
New-Item -ItemType Directory -Force out | Out-Null
$enc = New-Object System.Text.UTF8Encoding($false)
$lines = @('id,date,category,amount,owner')
foreach ($r in $rows) { $lines += ($r.id, $r.date, $r.category, $r.amount.ToString('0.00', $ci), $r.owner) -join ',' }
[IO.File]::WriteAllText((Join-Path $Dir 'out/merged.csv'), (($lines -join "`n") + "`n"), $enc)
& (Join-Path $Dir 'tools\publish.ps1') -Name merged-csv

$lines = @('id,reason') + $rej
[IO.File]::WriteAllText((Join-Path $Dir 'out/rejected.csv'), (($lines -join "`n") + "`n"), $enc)
& (Join-Path $Dir 'tools\publish.ps1') -Name rejected-csv

$lines = @('# 汇总')
$tn = 0; $tt = [decimal]0
foreach ($k in ($cats.known | Sort-Object)) {
  $g = @($rows | Where-Object { $_.category -eq $k })
  $sum = [decimal]0
  foreach ($x in $g) { $sum += $x.amount }
  $lines += "${k}: $($g.Count) 条, 合计 $($sum.ToString('0.00', $ci))"
  $tn += $g.Count; $tt += $sum
}
$lines += "总计: $tn 条, 合计 $($tt.ToString('0.00', $ci))"
[IO.File]::WriteAllText((Join-Path $Dir 'out/summary.md'), (($lines -join "`n") + "`n"), $enc)
& (Join-Path $Dir 'tools\publish.ps1') -Name summary-md
