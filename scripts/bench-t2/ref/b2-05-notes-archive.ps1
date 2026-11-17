param([Parameter(Mandatory)][string]$Dir)
$ErrorActionPreference = 'Stop'
Set-Location $Dir
$utf8 = New-Object System.Text.UTF8Encoding($false)
$tags = @()
$dirOf = @{}
foreach ($l in [IO.File]::ReadAllLines((Join-Path $Dir 'tags.txt'), $utf8)) {
  if ($l.Trim()) { $p = $l.Trim() -split '\s+'; $tags += ,@($p[0], $p[1]); $dirOf[$p[0]] = $p[1] }
}
$script:notes = @{}
$script:former = @{}
foreach ($f in Get-ChildItem (Join-Path $Dir 'notes') -Filter *.md) {
  $t = [IO.File]::ReadAllText($f.FullName, $utf8) -replace "`r`n", "`n"
  $tag = [regex]::Match($t, '(?m)^标签:\s*(\S+)\s*$').Groups[1].Value
  $title = [regex]::Match($t, '(?m)^title:\s*(.+?)\s*$').Groups[1].Value
  $old = [regex]::Match($t, '(?m)^原文件名:\s*(\S+)\s*$').Groups[1].Value
  $script:notes[$f.Name] = @{ tag = $tag; dir = $dirOf[$tag]; title = $title; text = $t; path = $f.FullName }
  if ($old) { $script:former[$old] = $f.Name }
}
$evaluator = [System.Text.RegularExpressions.MatchEvaluator]{
  param($m)
  $link = $m.Groups[1].Value
  if ($link -like '*://*') { return $m.Value }
  $parts = $link -split '#', 2
  $frag = if ($parts.Count -gt 1) { '#' + $parts[1] } else { '' }
  $path = $parts[0]
  if ($path -eq '../README.md') { return '](../../README.md' + $frag + ')' }
  $name = $path
  if ($script:former.ContainsKey($name)) { $name = $script:former[$name] }
  if (-not $script:notes.ContainsKey($name)) { return $m.Value }
  $rel = if ($script:notes[$name].dir -eq $script:curDir) { $name } else { '../' + $script:notes[$name].dir + '/' + $name }
  return '](' + $rel + $frag + ')'
}
foreach ($name in @($script:notes.Keys)) {
  $n = $script:notes[$name]
  $script:curDir = $n.dir
  $out = [regex]::Replace($n.text, '\]\(([^)]*)\)', $evaluator)
  $dest = Join-Path $Dir ("archive/" + $n.dir + "/" + $name)
  New-Item -ItemType Directory -Force (Split-Path $dest) | Out-Null
  [IO.File]::WriteAllText($dest, $out, $utf8)
  Remove-Item $n.path
}
$idx = @('# 笔记索引', '')
foreach ($t in $tags) {
  $idx += '## ' + $t[0]
  $idx += ''
  foreach ($name in ($script:notes.Keys | Sort-Object)) {
    $n = $script:notes[$name]
    if ($n.tag -eq $t[0]) { $idx += '- [' + $n.title + '](archive/' + $n.dir + '/' + $name + ')' }
  }
  $idx += ''
}
[IO.File]::WriteAllText((Join-Path $Dir 'index.md'), (($idx -join "`n") + "`n"), $utf8)
foreach ($t in $tags) { & (Join-Path $Dir 'tools\publish.ps1') -Name $t[1] }
