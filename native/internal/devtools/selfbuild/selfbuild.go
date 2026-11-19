// Package selfbuild 让 sumika-dev 在自己的源码变了以后先重编自己再执行（规格-脚本迁Go「落点」）。
// 钩子里每条都 go run 要 2.5～3.4 秒，编好的程序只要 0.2 秒；所以调用编好的程序，只在开发工具自己的源码改过时才重编。
package selfbuild

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// BuiltDigest 是编译时用 -ldflags "-X sumika-native/internal/devtools/selfbuild.BuiltDigest=<摘要>" 写进去的源码摘要。
// 直接 go build / go run 没写它，是空串，会被当成“对不上”而重编成带摘要的版本。
var BuiltDigest string

// 环境变量。
const (
	EnvRoot      = "SUMIKA_DEV_ROOT"        // 指定模块根（native/）或仓库根
	EnvNoRebuild = "SUMIKA_DEV_NO_REBUILD"  // 为 1 时不检查、不重编，给测试用
	EnvGo        = "SUMIKA_DEV_GO"          // go 程序的绝对路径；不设就用编出本程序的那套 GOROOT 里的 go
	envReexec    = "SUMIKA_DEV_REEXEC_DONE" // 重编后重新执行时带上本次的源码摘要；新程序算出的摘要等于它、却仍和自己编进去的对不上时，不再重编，防止无限重编
)

// AllowedModulePackages 是开发工具许可 import 的本模块纯工具包（相对模块根，含子目录）。
// archcheck 的白名单直接引用这一份：开发工具依赖的本模块代码，和参与摘要的目录是同一份常量，不会一边加了另一边漏掉。
var AllowedModulePackages = []string{"internal/jsjson", "internal/netsafe"}

// SourceDirs 是参与源码摘要的目录，相对模块根；另加 go.mod、go.sum 和 Go 版本。
// 用 go list -deps 现算要 1 秒左右（2026-11-18 量：963～1042 毫秒），钩子每次启动都付不起，所以用固定列表，
// 由 archcheck 保证“开发工具的本模块传递依赖 ⊂ 这个列表”，并保证这些目录里没有 go:embed 指令（embed 的非 .go 文件摘要不覆盖，要用先改 Digest）。
var SourceDirs = append([]string{"cmd/sumika-dev", "internal/devtools"}, AllowedModulePackages...)

// Digest 算 SourceDirs 下所有 .go 文件（按相对路径排序）加 go.mod、go.sum、Go 版本、GOWORK 和 GOTOOLCHAIN 的值的 SHA-256。
// 内容先把 CRLF 归一成 LF，免得 worktree 的签出方式影响摘要。
func Digest(modRoot string) (string, error) {
	var files []string
	for _, d := range SourceDirs {
		err := filepath.WalkDir(filepath.Join(modRoot, filepath.FromSlash(d)), func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if !e.IsDir() && strings.HasSuffix(p, ".go") {
				rel, err := filepath.Rel(modRoot, p)
				if err != nil {
					return err
				}
				files = append(files, filepath.ToSlash(rel))
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("读源码目录 %s 失败：%w", d, err)
		}
	}
	sort.Strings(files)
	files = append(files, "go.mod", "go.sum")
	h := sha256.New()
	// 换了 Go 版本、工作区文件或工具链选择，编出来的程序都可能不同，要重编。
	// Go 版本取“下次真要用来编的那套 Go”（goVersion），不用 runtime.Version()：那是旧程序自己当年的版本，Go 升级后不会变。
	fmt.Fprintf(h, "go\x00%s\x00gowork\x00%s\x00gotoolchain\x00%s\x00", goVersion(), os.Getenv("GOWORK"), os.Getenv("GOTOOLCHAIN"))
	for _, rel := range files {
		data, err := os.ReadFile(filepath.Join(modRoot, filepath.FromSlash(rel)))
		if err != nil {
			if rel == "go.sum" && errors.Is(err, fs.ErrNotExist) {
				data = nil // 没有第三方依赖的模块没有 go.sum
			} else {
				return "", fmt.Errorf("读 %s 失败：%w", rel, err)
			}
		}
		data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
		fmt.Fprintf(h, "%s\x00%d\x00", rel, len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// goVersion 读下次编译要用的那套 Go 的版本：go 程序所在 GOROOT 里的 VERSION 文件第一行（形如 go1.27.4）。
// 不用 `go env GOVERSION`：2026-11-18 量过，一次 109～113 毫秒，超过钩子每次启动付得起的 100 毫秒；读文件是微秒级。
// 找不到 go 或读不到 VERSION 时退回 runtime.Version()（编译时那套），这时换了 Go 要等 go.mod 之类别的变化才会重编。
func goVersion() string {
	p, err := goBinary()
	if err != nil {
		return runtime.Version()
	}
	return versionAt(filepath.Dir(filepath.Dir(p)))
}

func versionAt(goroot string) string {
	b, err := os.ReadFile(filepath.Join(goroot, "VERSION"))
	if err != nil {
		return runtime.Version()
	}
	line, _, _ := strings.Cut(string(b), "\n")
	if v := strings.TrimSpace(line); v != "" {
		return v
	}
	return runtime.Version()
}

// ErrNoModRoot 表示没设 SUMIKA_DEV_ROOT、从程序所在位置往上也找不到 go.mod：程序被拷到了仓库外，没法检查源码。
// 设了 SUMIKA_DEV_ROOT 却无效不是这个错，是写错了，要报错退出。
var ErrNoModRoot = errors.New("不在仓库里")

// FindModRoot 找模块根（native/，含 go.mod）：先看环境变量 SUMIKA_DEV_ROOT，再从可执行文件所在目录往上找 go.mod 或 native/go.mod。
func FindModRoot(exe string) (string, error) {
	if v := os.Getenv(EnvRoot); v != "" {
		if r, ok := modRootAt(v); ok {
			return r, nil
		}
		return "", fmt.Errorf("%s=%s 下找不到 go.mod 或 native/go.mod", EnvRoot, v)
	}
	for dir := filepath.Dir(exe); ; {
		if r, ok := modRootAt(dir); ok {
			return r, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("从 %s 往上找不到 native/go.mod：%w", filepath.Dir(exe), ErrNoModRoot)
		}
		dir = parent
	}
}

func modRootAt(dir string) (string, bool) {
	for _, c := range []string{dir, filepath.Join(dir, "native")} {
		if st, err := os.Stat(filepath.Join(c, "go.mod")); err == nil && !st.IsDir() {
			return c, true
		}
	}
	return "", false
}

// Prepare 是 main 在分派子命令之前调的：找模块根、检查摘要、必要时重编并执行。
// 返回 done=true 表示这次已经由新编的程序执行完了（或出错了），调用者直接用 code 退出；false 表示照现有这一版继续往下跑。
func Prepare(exe string, e Env) (done bool, code int) {
	if os.Getenv(EnvNoRebuild) == "1" {
		fmt.Fprintf(e.Stderr, "警告：%s=1，不检查 sumika-dev 的源码摘要、不自动重编，跑的可能是旧版本\n", EnvNoRebuild)
		return false, 0
	}
	root, err := FindModRoot(exe)
	if err != nil {
		if errors.Is(err, ErrNoModRoot) {
			fmt.Fprintln(e.Stderr, "sumika-dev：不在仓库里，跳过自我重编")
			return false, 0
		}
		fmt.Fprintln(e.Stderr, "错误：", err)
		return true, 1
	}
	rebuilt, code, err := Ensure(root, e)
	if err != nil {
		fmt.Fprintln(e.Stderr, "错误：", err)
		return true, code
	}
	return rebuilt, code
}

// Env 是 Ensure 要用的外部东西，测试里可以替换。
type Env struct {
	Exe    string                                                      // 正在运行的程序
	Args   []string                                                    // 原参数（不含程序名）
	Stderr io.Writer                                                   // 提示和失败原因
	Build  func(modRoot, out, digest string) error                     // 在 modRoot 里把 ./cmd/sumika-dev 编到 out，带上摘要
	Exec   func(exe string, args []string, digest string) (int, error) // 带原参数运行新程序（digest 是本次的源码摘要，要带进环境变量），返回退出码
	Built  string                                                      // 编进来的摘要，空则用 BuiltDigest
	// DiskDigest 问磁盘上那个 exe 编进去的是哪份源码的摘要；拿到锁以后用它判断“别的进程已经换好了”。nil 或出错都当作不知道，照常重编。
	DiskDigest func(exe string) (string, error)
	LockWait   time.Duration // 等锁的上限，0 用 defaultLockWait（编译超时加余量）
}

// goBinary 给出 go 程序的绝对路径。外部程序不按名字去 PATH 里找（archcheck 的外部程序规则，规格-外部程序清单）：
// 先看 SUMIKA_DEV_GO，再用编出本程序的那套 Go（runtime.GOROOT）里的 bin/go。
func goBinary() (string, error) { return goBinaryFrom(runtime.GOROOT()) }

// GoBinary 是 goBinary 的导出版：hook pre-commit 的 go vet 和自我重编用同一套 Go。
func GoBinary() (string, error) { return goBinary() }

func goBinaryFrom(goroot string) (string, error) {
	p := os.Getenv(EnvGo)
	if p == "" {
		if goroot == "" {
			return "", fmt.Errorf("本程序是用 -trimpath 编的，不知道 Go 装在哪（GOROOT 是空的）：设环境变量 %s 指向 go 程序的绝对路径", EnvGo)
		}
		name := "go"
		if runtime.GOOS == "windows" {
			name = "go.exe"
		}
		p = filepath.Join(goroot, "bin", name)
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("go 的路径 %q 不是绝对路径（%s 要写绝对路径）", p, EnvGo)
	}
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		return "", fmt.Errorf("找不到 go：%s（Go 可能升级或搬过家，设环境变量 %s 指向 go 程序，或手动 go build 一次）", p, EnvGo)
	}
	return p, nil
}

const (
	buildTimeout   = 5 * time.Minute
	buildWaitDelay = 10 * time.Second
)

// buildEnv 去掉会改变编译目标的环境变量：钩子或别的工具里设了 GOOS、GOARCH、GOFLAGS，编出来的就不是能在本机跑的 sumika-dev 了。
func buildEnv(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		switch strings.ToUpper(k) {
		case "GOFLAGS", "GOOS", "GOARCH":
			continue
		}
		out = append(out, kv)
	}
	return out
}

func buildCmd(ctx context.Context, goExe, modRoot, out, digest string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, goExe, "build", "-o", out, "-ldflags", "-X sumika-native/internal/devtools/selfbuild.BuiltDigest="+digest, "./cmd/sumika-dev")
	cmd.Dir = modRoot
	cmd.Env = buildEnv(os.Environ())
	cmd.WaitDelay = buildWaitDelay // 超时杀掉 go 以后，它的子进程（编译器、链接器）可能还攥着输出管道；不设的话 Run 会一直等到它们自己退出
	return cmd
}

// GoBuild 是真实的编译：go build -o out -ldflags "-X …BuiltDigest=摘要" ./cmd/sumika-dev
func GoBuild(modRoot, out, digest string) error {
	goExe, err := goBinary()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), buildTimeout)
	defer cancel()
	cmd := buildCmd(ctx, goExe, modRoot, out, digest)
	var buf bytes.Buffer
	cmd.Stdout, cmd.Stderr = &buf, &buf
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("go build 超过 %v 还没编完，已经停掉", buildTimeout)
		}
		return fmt.Errorf("go build 失败：%w\n%s", err, strings.TrimSpace(buf.String()))
	}
	return nil
}

// RunExe 是真实的重新执行：标准输入输出原样接上，退出码原样传回。
func RunExe(exe string, args []string, digest string) (int, error) {
	cmd := exec.Command(exe, args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = append(os.Environ(), envReexec+"="+digest)
	err := cmd.Run()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return 0, err
}

// DiskDigest 是真实的“问磁盘上的 exe”：运行它的隐藏子命令 __digest（带 SUMIKA_DEV_NO_REBUILD=1，不触发重编）。
func DiskDigest(exe string) (string, error) {
	cmd := exec.Command(exe, "__digest")
	cmd.Env = append(os.Environ(), EnvNoRebuild+"=1")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// CleanOld 清掉以前替换时改名留下的 .old.<pid>（Windows 上正在运行的程序不能覆盖、只能改名，旧的在没人用以后才删得掉；还有人在用的删不掉，留到下次）。
func CleanOld(exe string) {
	_ = os.Remove(exe + ".old")
	if m, err := filepath.Glob(exe + ".old.*"); err == nil {
		for _, f := range m {
			_ = os.Remove(f)
		}
	}
}

// Ensure 检查摘要：一致返回 (false, 0, nil)，调用者继续往下执行；
// 不一致就拿锁，拿到后先看磁盘上的 exe 是不是已经被别的进程换成新的（是就不再编），否则重编、替换，再带原参数运行 exe，返回 (true, 退出码, nil)；
// 编不过返回 (true, 非零, err)，err 里是中文原因，调用者必须退出，不能放行。
func Ensure(modRoot string, e Env) (rebuilt bool, code int, err error) {
	built := e.Built
	if built == "" {
		built = BuiltDigest
	}
	want, err := Digest(modRoot)
	if err != nil {
		return true, 1, err
	}
	if want == built {
		return false, 0, nil
	}
	if os.Getenv(envReexec) == want {
		return false, 0, nil // 刚重编完又对不上（编译期间源码又被改了）：不再递归，按这一版跑
	}
	wait := e.LockWait
	if wait == 0 {
		wait = defaultLockWait
	}
	release, err := acquireLock(e.Exe+".lock", wait)
	if err != nil {
		return true, 1, err
	}
	released := false
	unlock := func() {
		if !released {
			released = true
			release()
		}
	}
	defer unlock()
	fresh := false
	if e.DiskDigest != nil {
		if d, derr := e.DiskDigest(e.Exe); derr == nil && d == want {
			fresh = true // 等锁的时候别的进程已经编好换上了
		}
	}
	if !fresh {
		fmt.Fprintln(e.Stderr, "sumika-dev 的源码变了，重新编译……")
		tmp := fmt.Sprintf("%s.new.%d", e.Exe, os.Getpid())
		_ = os.Remove(tmp)
		if err := e.Build(modRoot, tmp, want); err != nil {
			_ = os.Remove(tmp)
			return true, 1, fmt.Errorf("重新编译 sumika-dev 失败，没有放行：%w", err)
		}
		if err := replace(e.Exe, tmp); err != nil {
			_ = os.Remove(tmp)
			return true, 1, fmt.Errorf("换掉旧的 sumika-dev 失败：%w", err)
		}
	}
	unlock() // 运行工具可能很久，别让别的进程陪着等
	code, err = e.Exec(e.Exe, e.Args, want)
	if err != nil {
		return true, 1, fmt.Errorf("运行新编的 sumika-dev 失败：%w", err)
	}
	return true, code, nil
}

// replace 用 newPath 换掉 exe：旧的先改名成 .old.<pid>（运行中的 exe 能改名不能覆盖；名字带 pid，别的还在跑的旧进程占着上一个 .old 也不碍事），
// 新的再移过来；移失败就把旧的改回去。调用者要先拿到锁。
func replace(exe, newPath string) error {
	old := fmt.Sprintf("%s.old.%d", exe, os.Getpid())
	_ = os.Remove(old)
	hadOld := true
	if err := os.Rename(exe, old); err != nil {
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		hadOld = false
	}
	if err := os.Rename(newPath, exe); err != nil {
		if hadOld {
			_ = os.Rename(old, exe)
		}
		return err
	}
	return nil
}
