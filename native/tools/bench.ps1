# 原生原型的"放大十倍"压测（规格-原生界面原型 第五节第 3 条）：模拟数据里一万条的"测试群"，加载全部 → 从底滚到顶，跑几遍。
# 量法和 docs/archive/notes/对照-Flutter与mygo-2026-10-23.md 第五节同一把尺子：进程启动到第一页（外部计时）、内存（工作集，每 0.5 秒一次）、
# 帧耗时（mygo 的 MYGO_FRAME_STATS 日志里每帧"took"）。结果写到 -Out 目录，最后打一张表。
# -Exe、-ExeArgs 换程序和参数（默认 sumika.exe -demo）；正式模式读编的数据用：-ExeArgs '-root','<仓库根>','-data','<desk-fixture 目录>' -Conv 'qq:group:90002'
param([int]$Rounds = 3, [int]$Seconds = 100, [string]$Out = "$env:TEMP\sumika-native-bench", [string]$Conv = 'qq:group:1001',
  [string]$Exe = (Join-Path $PSScriptRoot '..\sumika.exe'), [string[]]$ExeArgs = @('-demo'))
$exe = Resolve-Path $Exe
New-Item -ItemType Directory -Force $Out | Out-Null
$rows = @()
for ($r = 1; $r -le $Rounds; $r++) {
  $fp = Join-Path $Out "$r-firstpage.txt"; $sd = Join-Path $Out "$r-scroll.txt"; $fr = Join-Path $Out "$r-frames.txt"
  Remove-Item $fp, $sd, $fr -ErrorAction SilentlyContinue
  $env:SUMIKA_DEMO = 'scroll'; $env:SUMIKA_FIRSTPAGE = $fp; $env:SUMIKA_CONV = $Conv; $env:SUMIKA_SCROLLDONE = $sd; $env:MYGO_FRAME_STATS = 'all'
  $quoted = $ExeArgs | ForEach-Object { if ($_ -match '\s') { '"' + $_ + '"' } else { $_ } }
  $p = if ($quoted) { Start-Process -FilePath $exe -ArgumentList $quoted -WorkingDirectory (Split-Path $exe) -PassThru -RedirectStandardError $fr } else { Start-Process -FilePath $exe -WorkingDirectory (Split-Path $exe) -PassThru -RedirectStandardError $fr }
  $mem = New-Object System.Collections.Generic.List[double]
  $priv = New-Object System.Collections.Generic.List[double]
  $t0 = Get-Date
  while (((Get-Date) - $t0).TotalSeconds -lt $Seconds) {
    Start-Sleep -Milliseconds 500
    $p.Refresh(); if ($p.HasExited) { break }
    $mem.Add([math]::Round($p.WorkingSet64 / 1MB, 1)); $priv.Add([math]::Round($p.PrivateMemorySize64 / 1MB, 1))
    if ((Test-Path $sd) -and $mem.Count -gt 4) { Start-Sleep 2; $p.Refresh(); $mem.Add([math]::Round($p.WorkingSet64 / 1MB, 1)); break }
  }
  $start = $p.StartTime
  if (-not $p.HasExited) { Stop-Process -Id $p.Id -Force }
  $first = if (Test-Path $fp) { [math]::Round(([DateTimeOffset]::FromUnixTimeMilliseconds([int64](Get-Content $fp)).LocalDateTime - $start).TotalMilliseconds) } else { $null }
  $took = Select-String -Path $fr -Pattern 'took ([\d.]+) ms' | ForEach-Object { [double]$_.Matches[0].Groups[1].Value } | Sort-Object
  $n = $took.Count
  # 滚到的条数不够 1 万（自动压测没加载上来）的那一遍标成无效，不和别的遍一起比（2026-10-24 第 1 遍只滚了 200 条）
  $scroll = Get-Content $sd -ErrorAction SilentlyContinue
  $valid = $scroll -and ([int]($scroll -split ' ')[0]) -ge 10000
  $rows += [pscustomobject]@{
    遍 = if ($valid) { "$r" } else { "$r（无效）" }; 启动到第一页ms = $first; 滚动 = $scroll
    帧数 = $n; 帧中位ms = if ($n) { $took[[int]($n * 0.5)] } else { $null }; 帧p95ms = if ($n) { $took[[int]($n * 0.95)] } else { $null }
    卡帧占比 = if ($n) { '{0:P1}' -f (($took | Where-Object { $_ -gt 16.7 }).Count / $n) } else { $null }
    帧最大ms = if ($n) { $took[$n - 1] } else { $null }; 超33ms = ($took | Where-Object { $_ -gt 33 }).Count; 超100ms = ($took | Where-Object { $_ -gt 100 }).Count
    私有峰值MB = ($priv | Measure-Object -Maximum).Maximum
    内存开头MB = $mem[0]; 内存峰值MB = ($mem | Measure-Object -Maximum).Maximum; 内存最后MB = $mem[$mem.Count - 1]
  }
  Start-Sleep -Seconds 3
}
'SUMIKA_DEMO', 'SUMIKA_FIRSTPAGE', 'SUMIKA_CONV', 'SUMIKA_SCROLLDONE', 'MYGO_FRAME_STATS' | ForEach-Object { Remove-Item "Env:$_" -ErrorAction SilentlyContinue }
$rows | Export-Csv (Join-Path $Out 'rows.csv') -NoTypeInformation -Encoding utf8
$rows | Format-Table -AutoSize | Out-String -Width 400
