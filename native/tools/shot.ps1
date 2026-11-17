# 截一个窗口（按进程名找主窗口），不用把它拉到前面：PrintWindow + PW_RENDERFULLCONTENT
param([string]$Process = "sumika", [string]$Out = "$PSScriptRoot\..\shots\shot.png")
Add-Type -AssemblyName System.Drawing
Add-Type @"
using System; using System.Runtime.InteropServices;
public static class W {
  [DllImport("user32.dll")] public static extern bool GetWindowRect(IntPtr h, out RECT r);
  [DllImport("user32.dll")] public static extern bool PrintWindow(IntPtr h, IntPtr hdc, uint flags);
  [DllImport("user32.dll")] public static extern bool SetProcessDpiAwarenessContext(IntPtr v);
  public struct RECT { public int L, T, R, B; }
}
"@
[void][W]::SetProcessDpiAwarenessContext([IntPtr](-4))
$p = Get-Process -Name $Process -ErrorAction Stop | Where-Object { $_.MainWindowHandle -ne 0 } | Select-Object -First 1
$r = New-Object W+RECT; [void][W]::GetWindowRect($p.MainWindowHandle, [ref]$r)
$w = $r.R - $r.L; $h = $r.B - $r.T
$bmp = New-Object System.Drawing.Bitmap $w, $h
$g = [System.Drawing.Graphics]::FromImage($bmp); $hdc = $g.GetHdc()
[void][W]::PrintWindow($p.MainWindowHandle, $hdc, 2); $g.ReleaseHdc($hdc); $g.Dispose()
New-Item -ItemType Directory -Force (Split-Path $Out) | Out-Null
$bmp.Save($Out, [System.Drawing.Imaging.ImageFormat]::Png); $bmp.Dispose()
"$w x $h -> $Out"
