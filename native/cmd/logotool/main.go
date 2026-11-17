// logotool：Sumika 的 logo 素材小工具，一次性的命令行，不进桌面程序也不进 Go 后台
// （发出去的程序不许链接它，archcheck 的 toolOnly 里不用登记：它是 main 包，没有人能 import）。
// 三件事：用 Edge 把 SVG 渲染成指定边长的 PNG；把渲染出的图和参考图逐像素对拍；把几张 PNG 打成 Windows 的图标资源（.syso）。
// 用法和重新导出的步骤见 docs/assets/logo/README.md。
package main

import (
	"flag"
	"fmt"
	"os"
)

const usage = `logotool：Sumika 的 logo 素材小工具
  logotool render  -svg <svg> -size N -out <png>              用 Edge 把 SVG 渲染成 N×N 的 PNG（透明底）
  logotool measure -ref <png> -cand <png> -out <目录>         对比两张同尺寸的图，写 metrics.json、diff.png、overlay.png、side.png、classes_*.png
  logotool syso    -out <文件.syso> [-arch amd64] <png>...    把几张正方形 PNG（边长 1～256）打成只含图标的 Windows 资源（不含清单和版本信息）
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "render":
		err = cmdRender(os.Args[2:])
	case "measure":
		err = cmdMeasure(os.Args[2:])
	case "syso":
		err = cmdSyso(os.Args[2:])
	case "-h", "-help", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "不认识的子命令 %q\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		if err == flag.ErrHelp {
			os.Exit(2)
		}
		fmt.Fprintln(os.Stderr, "错误：", err)
		os.Exit(1)
	}
}
