# Sumika 的 logo 素材

一只深藏青色的乌鸫探头进画面。主人在 ChatGPT 里定的原图是 `reference.png`（1254×1254，SHA256 前 16 位 06DF101F1F4670ED），SVG 是照它一块一块描出来再逐像素对拍过的（ADR-230）。

| 文件 | 是什么 |
|---|---|
| `reference.png` | 主人定的原图，对拍用，不改 |
| `master.svg` | 定稿：1254×1254、带奶油底的纯矢量，其余都从它来 |
| `icon.svg` | 程序图标：master 外面套圆角方块遮罩（半径是边长的 22%），圆角外透明 |
| `tray.svg` | 托盘小图：整个头放进圆角方块，眼和喙放大 1.35 倍，小尺寸下看得清 |
| `icon-1024.png` | icon.svg 的 1024 导出，给头像、商店页这类要大图的地方用 |

程序里用到的 PNG 在 `native/assets/` 下，不在这里：`assets/tray/tray-<边长>.png`（托盘，七张，运行时按系统小图标的边长挑）、`assets/icon/icon-<边长>.png`（九张，打进 exe 的图标资源，其中 256 那张也是没带资源时的窗口图标）。`native/rsrc_windows_amd64.syso`、`rsrc_windows_arm64.syso` 是用这九张图打出来的 Windows 图标资源，普通的 `go build` 会自己链进 exe。

## 重新导出

改了 SVG 以后，在 `native/` 里照下面做。导出用 Edge（`C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`，路径不同加 `-edge <路径>`），每张图起一次无头 Edge，整套几分钟。

```powershell
cd native
foreach ($n in 16,20,24,32,40,48,64,128,256) { go run ./cmd/logotool render -svg ../docs/assets/logo/icon.svg -size $n -out assets/icon/icon-$n.png }
foreach ($n in 16,20,24,32,40,48,64) { go run ./cmd/logotool render -svg ../docs/assets/logo/tray.svg -size $n -out assets/tray/tray-$n.png }
go run ./cmd/logotool render -svg ../docs/assets/logo/icon.svg -size 1024 -out ../docs/assets/logo/icon-1024.png
$icons = Get-ChildItem assets/icon/icon-*.png | ForEach-Object FullName
go run ./cmd/logotool syso -arch amd64 -out rsrc_windows_amd64.syso $icons
go run ./cmd/logotool syso -arch arm64 -out rsrc_windows_arm64.syso $icons
go test ./cmd/logotool
```

- 最后一条会对一遍：提交的两个 `.syso` 是不是正好由 `assets/icon` 里的 PNG 打出来的；改了图标没重新打就红。
- 托盘的尺寸表在 `native/icons.go` 的 `traySizes`，加减尺寸时和 `assets/tray` 里的文件一起改（`icons_test.go` 两头对）。
- 2026-11-01 用本机 Edge 重新渲染过：九张图标、七张托盘图、1024 的图都和提交的逐字节一样。Edge 升级后抗锯齿可能有细微差别，那时以重新渲染的为准、一起提交。
- 不手改 PNG 和 `.syso`：它们都是 SVG 的导出物。

## 对拍

`master.svg` 和 `reference.png` 的差距用 `logotool measure` 量：

```powershell
go run ./cmd/logotool render -svg ../docs/assets/logo/master.svg -size 1254 -out <目录>/master-1254.png
go run ./cmd/logotool measure -ref ../docs/assets/logo/reference.png -cand <目录>/master-1254.png -out <目录>
```

输出目录里有 `metrics.json` 和差异图（`diff.png`、`overlay.png`、`side.png`、按类别的 `classes_*.png`）。定稿那一版量出来：身体、眼、喙、底色四块的重叠率（IoU）分别是 0.9994、0.9969、0.9970、0.9996；边界到边界的距离平均 0.14 像素、最大 1 像素；整图平均每通道差 1.09（满分 255）。

## 确认 exe 的文件图标换上了

编出 exe 以后（`go build -trimpath -ldflags "-s -w -H windowsgui" -o sumika.exe .`），在 PowerShell 里：

```powershell
Add-Type -AssemblyName System.Drawing
[System.Drawing.Icon]::ExtractAssociatedIcon((Resolve-Path sumika.exe).Path).ToBitmap().Save("$PWD\assoc.png")
```

`assoc.png` 应该是那只乌鸫，和 `native/assets/icon/icon-32.png` 逐像素一样；不是的话大概率是 `.syso` 没链进去（文件名要和 `GOOS_GOARCH` 对上，改了架构要有对应的那个文件）。`native/icons_windows_test.go` 的 `TestExeCarriesIconResource` 也查这件事。
