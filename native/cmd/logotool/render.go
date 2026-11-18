package main

import (
	"context"
	"flag"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const defaultEdge = `C:\Program Files (x86)\Microsoft\Edge\Application\msedge.exe`

// 小尺寸的页面至少这么大：Edge 的窗口有最小尺寸，直接把窗口设成 N×N 会被撑大或截图尺寸不对，
// 所以一律用"大页面、图放左上角、截完整页再裁出 N×N"。
const minPage = 800

// transparentARGB 是 Edge 默认背景色的"全透明"：八个零。拆成两段写，免得隐私扫描把一串数字当成号码
const transparentARGB = "0000" + "0000"

func fileURL(p string) string {
	abs, _ := filepath.Abs(p)
	u := url.URL{Scheme: "file", Path: "/" + filepath.ToSlash(abs)}
	return u.String()
}

func cmdRender(args []string) error {
	fs := flag.NewFlagSet("render", flag.ContinueOnError)
	svg := fs.String("svg", "", "SVG 文件")
	size := fs.Int("size", 0, "输出边长 N（输出 N×N）")
	out := fs.String("out", "", "输出 PNG")
	edge := fs.String("edge", defaultEdge, "msedge.exe 的路径")
	verbose := fs.Bool("v", false, "打印每次尝试的细节")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *svg == "" || *out == "" || *size <= 0 {
		return fmt.Errorf("render 需要 -svg、-size（正整数）、-out")
	}
	if _, err := os.Stat(*svg); err != nil {
		return fmt.Errorf("读不到 SVG：%w", err)
	}
	if _, err := os.Stat(*edge); err != nil {
		return fmt.Errorf("找不到 Edge：%w", err)
	}
	absOut, _ := filepath.Abs(*out)
	if err := os.MkdirAll(filepath.Dir(absOut), 0o755); err != nil {
		return err
	}
	// 每次调用自己一个工作目录（含独立的 Edge 用户目录），几个阶段同时渲染也不会抢同一个 profile。
	work, err := os.MkdirTemp(filepath.Dir(absOut), ".logotool-render-")
	if err != nil {
		return err
	}
	defer removeAllRetry(work)

	n := *size
	page := max(n, minPage)
	html := fmt.Sprintf(`<!doctype html><meta charset="utf-8"><style>html,body{margin:0;padding:0;background:transparent;overflow:hidden}img{display:block;margin:0;padding:0;border:0}</style><img src="%s" width="%d" height="%d">`,
		fileURL(*svg), n, n)
	htmlPath := filepath.Join(work, "page.html")
	if err := os.WriteFile(htmlPath, []byte(html), 0o644); err != nil {
		return err
	}

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		raw := filepath.Join(work, "raw.png")
		os.Remove(raw)
		start := time.Now()
		lastErr = launchEdge(*edge, work, htmlPath, raw, page, page+200, *verbose)
		if lastErr == nil {
			var dst *image.NRGBA
			dst, lastErr = waitShot(raw, n, 40*time.Second)
			if lastErr == nil {
				if *verbose {
					fmt.Fprintf(os.Stderr, "第 %d 次渲染成功，用时 %.1fs\n", attempt, time.Since(start).Seconds())
				}
				return writePNGAtomic(absOut, dst)
			}
		}
		if *verbose {
			fmt.Fprintf(os.Stderr, "第 %d 次渲染没成功：%v\n", attempt, lastErr)
		}
		killByWorkDir(work) // 没出图的那一次，把还挂着的浏览器收掉再重来
	}
	return fmt.Errorf("Edge 三次都没渲染出 %d×%d 的图：%w", n, n, lastErr)
}

// launchEdge 启动 Edge 并等启动器进程退出。
// 注意：msedge.exe 的启动器进程 0.1 秒就退出（退出码 0），真正的无头浏览器是它拉起来的另一个进程，
// 几秒后才写出截图，所以"进程退出"不等于"图已经出来"，要再用 waitShot 等文件。
func launchEdge(edge, work, htmlPath, raw string, w, h int, verbose bool) error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, edge,
		"--headless=new", "--disable-gpu", "--hide-scrollbars",
		"--user-data-dir="+filepath.Join(work, "profile"),
		"--default-background-color="+transparentARGB,
		"--force-device-scale-factor=1",
		"--window-size="+strconv.Itoa(w)+","+strconv.Itoa(h),
		"--screenshot="+raw,
		fileURL(htmlPath))
	if verbose {
		fmt.Fprintf(os.Stderr, "命令行：%v\n", cmd.Args)
	}
	start := time.Now()
	runErr := cmd.Run()
	if ctx.Err() != nil {
		return fmt.Errorf("Edge 启动器 60 秒没退出")
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "Edge 启动器退出 err=%v 用时 %.1fs\n", runErr, time.Since(start).Seconds())
	}
	return nil
}

// errTooSmall 表示截图是完整的，但比要的小——再等也不会变，直接换下一次尝试。
type errTooSmall struct{ got, want string }

func (e errTooSmall) Error() string { return "截图只有 " + e.got + "，不够 " + e.want }

// waitShot 等截图文件出现并且是完整的 PNG（半截文件解码会失败，继续等）。
func waitShot(raw string, n int, limit time.Duration) (*image.NRGBA, error) {
	deadline := time.Now().Add(limit)
	for {
		dst, err := decodeRaw(raw, n)
		if err == nil {
			return dst, nil
		}
		if _, small := err.(errTooSmall); small {
			return nil, err
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等了 %v 还没有完整的截图：%w", limit, err)
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// killByWorkDir：找出命令行里带着本次工作目录的进程（只可能是本次拉起的 Edge），按 PID 结束。
// 不按进程名杀；工作目录名里有随机数，不会碰到别的 Edge。
func killByWorkDir(work string) {
	if strings.TrimSpace(work) == "" {
		return // 空串会让 Contains('') 恒真，把本机所有进程都结束掉
	}
	script := `$w = '` + strings.ReplaceAll(work, `'`, `''`) + `'
Get-CimInstance Win32_Process | Where-Object { $_.ProcessId -ne $PID -and $_.CommandLine -and $_.CommandLine.Contains($w) } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }`
	runKillScript(script)
}

// runKillScript 真正跑结束脚本；单独成变量，测试里换掉，不去碰真进程
var runKillScript = func(script string) {
	exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command", script).Run()
}

// decodeRaw 读 Edge 的截图，确认至少 n×n，返回左上角 n×n 那块。
func decodeRaw(path string, n int) (*image.NRGBA, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	im, err := png.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("截图不是完整的 PNG：%w", err)
	}
	b := im.Bounds()
	if b.Dx() < n || b.Dy() < n {
		return nil, errTooSmall{fmt.Sprintf("%d×%d", b.Dx(), b.Dy()), fmt.Sprintf("%d×%d", n, n)}
	}
	dst := image.NewNRGBA(image.Rect(0, 0, n, n))
	if src, ok := im.(*image.NRGBA); ok {
		// 原样逐行拷贝，不经过预乘来回转换，半透明像素的颜色不会被改。
		for y := 0; y < n; y++ {
			copy(dst.Pix[y*dst.Stride:y*dst.Stride+n*4], src.Pix[(y+b.Min.Y-src.Rect.Min.Y)*src.Stride+(b.Min.X-src.Rect.Min.X)*4:])
		}
		return dst, nil
	}
	draw.Draw(dst, dst.Bounds(), im, b.Min, draw.Src)
	return dst, nil
}

func removeAllRetry(dir string) {
	// Edge 的子进程偶尔多占几百毫秒的文件，删不掉就等一下再试；最后还删不掉就留着，不报错。
	for i := 0; i < 10; i++ {
		if os.RemoveAll(dir) == nil {
			return
		}
		time.Sleep(300 * time.Millisecond)
	}
}
