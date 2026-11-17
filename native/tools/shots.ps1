# 截图验收：主窗口一张、设置每页一张（SUMIKA_SETTINGS=n，n 从 0 数到设置分类的个数减一，现在是 0～7），哪一页一打开就退出了就打印它的报错前几行。
# 结果在 shots/（不进 git）：main.png、set-0.png … set-7.png、sheet.png（设置各页拼成一张）
$here = Split-Path $PSScriptRoot
$exe = Join-Path $here 'sumika.exe'
$shots = Join-Path $here 'shots'
New-Item -ItemType Directory -Force $shots | Out-Null
function Shoot([string]$name, [string]$page) {
  if ($page) { $env:SUMIKA_SETTINGS = $page } else { Remove-Item Env:SUMIKA_SETTINGS -ErrorAction SilentlyContinue }
  $err = Join-Path $env:TEMP "sumika-native-$name.txt"
  $p = Start-Process $exe -ArgumentList '-demo' -WorkingDirectory $here -PassThru -RedirectStandardError $err
  Start-Sleep 3
  if ($p.HasExited) {
    Write-Output "$name 崩了："
    Get-Content $err | Where-Object { $_ -match 'panic|goroutine 1 ' } | Select-Object -First 4
    Get-Content $err | Where-Object { $_ -match 'sumika-native/' } | Select-Object -First 4
  } else {
    & (Join-Path $PSScriptRoot 'shot.ps1') -Process sumika -Out (Join-Path $shots "$name.png") | Out-Null
    Stop-Process -Id $p.Id
    Write-Output "$name 好"
  }
}
Shoot 'main' ''
foreach ($i in 0..7) { Shoot "set-$i" "$i" }
Remove-Item Env:SUMIKA_SETTINGS -ErrorAction SilentlyContinue
Add-Type -AssemblyName System.Drawing
$w = 1025; $h = 562
$sheet = New-Object System.Drawing.Bitmap ($w * 3), ($h * 3)
$g = [System.Drawing.Graphics]::FromImage($sheet); $g.InterpolationMode = 'HighQualityBicubic'
foreach ($i in 0..7) {
  $f = Join-Path $shots "set-$i.png"
  if (Test-Path $f) { $im = [System.Drawing.Image]::FromFile($f); $g.DrawImage($im, ($i % 3) * $w, [math]::Floor($i / 3) * $h, $w, $h); $im.Dispose() }
}
$sheet.Save((Join-Path $shots 'sheet.png')); $g.Dispose(); $sheet.Dispose()
