// Package toolchain 是外部程序的登记表：统一查找顺序（自带 → 配置 → PATH → 系统安装目录）、缓存和版本。
// P2-5 第 2 步起各处调用方向 Lookup 要结果，不再自己 LookPath（规格-外部程序清单 设计第 1～3 条）。
package toolchain

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// VersionTimeout 是一个程序取版本的上限；超时就放弃，留空，不算失败。
const VersionTimeout = 3 * time.Second

// Source 是找到的位置属于哪一类
type Source string

const (
	SourceBundled Source = "bundled" // Sumika 自带的 <资源根>/vendor/bin
	SourcePath    Source = "path"    // PATH
	SourceConfig  Source = "config"  // 配置里写的路径
	SourceSystem  Source = "system"  // 系统安装目录（Program Files、System32 等）
)

// Result 是一个外部程序的查找结果
type Result struct {
	Name     string
	Required bool
	Purpose  string
	Found    bool
	Path     string
	Source   Source
	Version  string // 版本命令的第一行；没取到是空
}

// Env 是查找能看到的东西；LookPath、Exists 为空时用进程自己的。空的标准安装根不查。
// Warnf 在配置里写的路径找不到时收到一次警告（同一个路径只报一次）。
type Env struct {
	LookPath     func(string) (string, error)
	Exists       func(string) bool
	Warnf        func(format string, args ...any)
	ResourceRoot string // 资源根；下面有 vendor/bin
	FFmpeg       string // qq.media.ffmpeg
	FFprobe      string // qq.media.ffprobe
	ProgramFiles string
	ProgramW6432 string
	LocalAppData string
	SystemRoot   string
}

type entry struct {
	name     string
	required bool
	purpose  string
	find     func(Env) (string, Source)
}

// 登记顺序固定，报告按这个顺序。pwsh 给命令分析器（PowerShell 7）；powershell 是执行工具的完整退路（先 7 后 5.1）。
// 每个程序都按“自带 → 配置 → PATH → 系统安装目录”找；taskkill、rundll32 只认 System32。
var registry = []entry{
	{name: "pwsh", purpose: "命令分析器", find: findPwsh},
	{name: "powershell", purpose: "执行工具的 PowerShell", find: findWindowsPowerShell},
	{name: "powershell51", purpose: "系统通知（Windows PowerShell 5.1 的应用标识）", find: findSystemPowerShell51},
	{name: "rg", required: true, purpose: "搜索", find: func(env Env) (string, Source) { return findBundledOrPath(env, "rg") }},
	{name: "fd", required: true, purpose: "找文件", find: findFd},
	{name: "ffmpeg", purpose: "收视频和链接展开", find: func(env Env) (string, Source) { return findMedia(env, env.FFmpeg, "ffmpeg") }},
	{name: "ffprobe", purpose: "探测视频", find: func(env Env) (string, Source) { return findMedia(env, env.FFprobe, "ffprobe") }},
	{name: "git", purpose: "Git Bash 钩子", find: findGit},
	{name: "taskkill", purpose: "结束进程树（商店版 PowerShell 的兜底）", find: func(env Env) (string, Source) { return findSystemProgram(env, "taskkill") }},
	{name: "rundll32", purpose: "用系统默认程序打开链接", find: func(env Env) (string, Source) { return findSystemProgram(env, "rundll32") }},
}

// 取版本的命令；表里没有的程序（taskkill、powershell51）不取版本。测试会临时换掉。
var versionCommands = map[string][]string{
	"pwsh":       {"-NoProfile", "-Command", "$PSVersionTable.PSVersion.ToString()"},
	"powershell": {"-NoProfile", "-Command", "$PSVersionTable.PSVersion.ToString()"},
	"rg":         {"--version"},
	"fd":         {"--version"},
	"ffmpeg":     {"-version"},
	"ffprobe":    {"-version"},
	"git":        {"--version"},
}

// SetVersionCommandsForTests 换掉取版本命令，只给测试用；返回恢复函数。
// 测试大量 Refresh 时不想每次起一堆真程序（句柄数、耗时都受影响）。
func SetVersionCommandsForTests(cmds map[string][]string) func() {
	versionMu.Lock()
	defer versionMu.Unlock()
	old := versionCommands
	versionCommands = make(map[string][]string, len(cmds))
	for name, args := range cmds {
		versionCommands[name] = append([]string(nil), args...)
	}
	return func() {
		versionMu.Lock()
		versionCommands = old
		versionMu.Unlock()
	}
}

// versionCommandsNow 复制一份当前表，避免取版本的后台协程和测试换表撞车。
func versionCommandsNow() map[string][]string {
	versionMu.Lock()
	defer versionMu.Unlock()
	out := make(map[string][]string, len(versionCommands))
	for name, args := range versionCommands {
		out[name] = append([]string(nil), args...)
	}
	return out
}

var (
	// refreshMu 让“没初始化时现查一次”和 Refresh 不会同时查两遍。
	refreshMu sync.Mutex
	// versionMu 保护取版本命令表；测试会换表。
	versionMu sync.Mutex
	// cacheMu 保护下面的缓存；Lookup 只读它。
	cacheMu        sync.RWMutex
	cache          []Result
	cacheSet       bool
	latestVersions <-chan struct{} // 最近一次 Refresh 的版本批次；关闭表示取完
	versionGen     uint64          // 只有最新批次的版本能写进缓存
	// warnedMissing 让配置里同一个找不到的路径只警告一次。
	warnedMissing sync.Map
)

// Resolve 查一遍登记表；只 Stat 和 LookPath，不起程序、不碰缓存。
func Resolve(env Env) []Result {
	out := make([]Result, 0, len(registry))
	for _, e := range registry {
		path, source := e.find(env)
		out = append(out, Result{Name: e.name, Required: e.required, Purpose: e.purpose, Found: path != "", Path: path, Source: source})
	}
	return out
}

// Refresh 重查登记表、替换缓存，然后在后台取版本；返回的是路径结果（版本可能还没取到）。
// 并发安全；调用方拿到结果后不用再自己 LookPath。
func Refresh(env Env) []Result {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	return refreshLocked(env, true)
}

// RefreshAndWait 和 Refresh 一样查一遍，再等版本取完（或 ctx 到期），返回带版本的结果。
// 给测试和需要完整结果的调用方用；生产界面不阻塞，不能在这里等。
func RefreshAndWait(ctx context.Context, env Env) []Result {
	Refresh(env)
	return Wait(ctx)
}

// Wait 等最近一次 Refresh 的版本取完（或 ctx 到期），返回带版本的结果；不重新查找。
// 宿主在放开 op 锁以后用它补一次带版本的快照。
func Wait(ctx context.Context) []Result {
	cacheMu.RLock()
	done := latestVersions
	cacheMu.RUnlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
		}
	}
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return cloneResults(cache)
}

// Lookup 读缓存；没初始化时用进程自己的环境现查一次，只查路径、不取版本。bool 是找到没有。
// 取版本要起子进程，进程只是问一次路径时不该起（见 Refresh）。
func Lookup(name string) (Result, bool) {
	if r, ready := lookupCached(name); ready {
		return r, r.Found
	}
	refreshMu.Lock()
	defer refreshMu.Unlock()
	if r, ready := lookupCached(name); ready {
		return r, r.Found
	}
	refreshLocked(defaultEnv(), false)
	r, _ := lookupCached(name)
	return r, r.Found
}

func lookupCached(name string) (Result, bool) {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	if !cacheSet {
		return Result{}, false
	}
	for _, r := range cache {
		if r.Name == name {
			return r, true
		}
	}
	return Result{}, true
}

// refreshLocked 查一遍登记表写缓存。withVersions 为真时再在后台取版本；
// 懒加载只查路径，不起取版本子进程，Wait 也不会等（没有批次）。
func refreshLocked(env Env, withVersions bool) []Result {
	results := Resolve(env)
	cacheMu.Lock()
	versionGen++
	if !withVersions {
		cache = cloneResults(results)
		cacheSet = true
		latestVersions = nil
		cacheMu.Unlock()
		return cloneResults(results)
	}
	commands := versionCommandsNow()
	done := make(chan struct{})
	gen := versionGen
	cache = cloneResults(results)
	cacheSet = true
	latestVersions = done
	cacheMu.Unlock()
	go fetchVersions(results, commands, gen, done)
	return cloneResults(results)
}

// fetchVersions 并发取每个找到的程序的版本；只写最新批次，过期的结果丢掉。
func fetchVersions(results []Result, commands map[string][]string, gen uint64, done chan struct{}) {
	defer close(done)
	var wg sync.WaitGroup
	for _, r := range results {
		args, ok := commands[r.Name]
		if !r.Found || !ok || len(args) == 0 {
			continue
		}
		wg.Add(1)
		go func(r Result, args []string) {
			defer wg.Done()
			version := runVersion(r.Path, args)
			if version == "" {
				return
			}
			cacheMu.Lock()
			if gen == versionGen {
				for i := range cache {
					if cache[i].Name == r.Name && cache[i].Path == r.Path {
						cache[i].Version = version
						break
					}
				}
			}
			cacheMu.Unlock()
		}(r, args)
	}
	wg.Wait()
}

// runVersion 跑一次版本命令，最多 VersionTimeout；第一行就是版本，取不到留空。
func runVersion(path string, args []string) string {
	ctx, cancel := context.WithTimeout(context.Background(), VersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	hideWindow(cmd) // Windows 上不弹窗；别的系统什么也不做
	cmd.WaitDelay = time.Second
	out, _ := cmd.Output()
	return firstLine(string(out))
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}

func cloneResults(in []Result) []Result {
	return append([]Result(nil), in...)
}

func defaultEnv() Env {
	return Env{
		ProgramFiles: os.Getenv("ProgramFiles"),
		ProgramW6432: os.Getenv("ProgramW6432"),
		LocalAppData: os.Getenv("LocalAppData"),
		SystemRoot:   os.Getenv("SystemRoot"),
	}
}

func (e Env) lookPath(name string) (string, error) {
	if e.LookPath != nil {
		return e.LookPath(name)
	}
	return exec.LookPath(name)
}

func (e Env) exists(path string) bool {
	if e.Exists != nil {
		return e.Exists(path)
	}
	_, err := os.Stat(path)
	return err == nil
}

// exeName 是程序在 PATH 和自带目录里的文件名：Windows 带 .exe，别的系统不带。
// Windows 上不带扩展名时 LookPath 按 PATHEXT 试，可能先碰到同名的 .cmd / .bat 包装脚本。
func exeName(name string) string {
	if runtime.GOOS == "windows" {
		return name + ".exe"
	}
	return name
}

// bundledPath 是 <资源根>/vendor/bin/<系统的程序名>；没有资源根或文件不在就是空。
func bundledPath(env Env, name string) (string, bool) {
	if env.ResourceRoot == "" {
		return "", false
	}
	p := filepath.Join(env.ResourceRoot, "vendor", "bin", exeName(name))
	if env.exists(p) {
		return p, true
	}
	return "", false
}

func findBundledOrPath(env Env, name string) (string, Source) {
	if p, ok := bundledPath(env, name); ok {
		return p, SourceBundled
	}
	if p, err := env.lookPath(name); err == nil {
		return p, SourcePath
	}
	return "", ""
}

func findFd(env Env) (string, Source) {
	if p, ok := bundledPath(env, "fd"); ok {
		return p, SourceBundled
	}
	// 和 tools.toolPath 一样：有的机器上 fd 装成 fdfind
	for _, name := range []string{"fd", "fdfind"} {
		if p, err := env.lookPath(name); err == nil {
			return p, SourcePath
		}
	}
	return "", ""
}

func findMedia(env Env, configured, name string) (string, Source) {
	if p, ok := bundledPath(env, name); ok {
		return p, SourceBundled
	}
	if configured != "" {
		if p, err := env.lookPath(configured); err == nil {
			return p, SourceConfig
		}
		env.warnMissingConfig(configured, name)
	}
	if p, err := env.lookPath(name); err == nil {
		return p, SourcePath
	}
	return "", ""
}

func (e Env) warnMissingConfig(configured, name string) {
	if e.Warnf == nil {
		return
	}
	if _, loaded := warnedMissing.LoadOrStore(configured, struct{}{}); loaded {
		return
	}
	e.Warnf("配置里 %s 的路径 %q 找不到，继续按顺序找", name, configured)
}

// findPwsh 给命令分析器：自带 → PATH → Program Files 的 PowerShell 7。
// 第三级是 P2-5 的有意变化：以前只认 PATH，Program Files 里的 7 不算（坑 7）。
func findPwsh(env Env) (string, Source) {
	if p, ok := bundledPath(env, "pwsh"); ok {
		return p, SourceBundled
	}
	if p, err := env.lookPath(exeName("pwsh")); err == nil {
		return p, SourcePath
	}
	if env.ProgramFiles != "" {
		if p := filepath.Join(env.ProgramFiles, "PowerShell", "7", "pwsh.exe"); env.exists(p) {
			return p, SourceSystem
		}
	}
	return "", ""
}

// findWindowsPowerShell 给执行工具和钩子：先 PowerShell 7（自带 → PATH → Program Files），再 5.1（自带 → PATH → System32）。
// 行名是 powershell，但可能返回 pwsh；命令分析器能不能用看上面的 pwsh 一行，两件事分开。
func findWindowsPowerShell(env Env) (string, Source) {
	if p, ok := bundledPath(env, "pwsh"); ok {
		return p, SourceBundled
	}
	if p, err := env.lookPath(exeName("pwsh")); err == nil {
		return p, SourcePath
	}
	if env.ProgramFiles != "" {
		if p := filepath.Join(env.ProgramFiles, "PowerShell", "7", "pwsh.exe"); env.exists(p) {
			return p, SourceSystem
		}
	}
	if p, ok := bundledPath(env, "powershell"); ok {
		return p, SourceBundled
	}
	if p, err := env.lookPath(exeName("powershell")); err == nil {
		return p, SourcePath
	}
	if env.SystemRoot != "" {
		if p := filepath.Join(env.SystemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"); env.exists(p) {
			return p, SourceSystem
		}
	}
	return "", ""
}

// findGit 给 Git Bash 钩子：自带 → PATH → Git for Windows 的标准安装根。
func findGit(env Env) (string, Source) {
	if p, ok := bundledPath(env, "git"); ok {
		return p, SourceBundled
	}
	if p, err := env.lookPath(exeName("git")); err == nil {
		return p, SourcePath
	}
	// Windows 上裸名还会按 PATHEXT 试 git.cmd / git.bat，留给这种安装；别的系统上面已经是不带扩展名的名字。
	if runtime.GOOS == "windows" {
		if p, err := env.lookPath("git"); err == nil {
			return p, SourcePath
		}
	}
	for _, root := range []string{env.ProgramFiles, env.ProgramW6432, env.LocalAppData} {
		if root == "" {
			continue
		}
		for _, rel := range []string{`Git\cmd\git.exe`, `Git\bin\git.exe`, `Programs\Git\cmd\git.exe`, `Programs\Git\bin\git.exe`} {
			if p := filepath.Join(root, rel); env.exists(p) {
				return p, SourceSystem
			}
		}
	}
	return "", ""
}

// findSystemPowerShell51 只给系统通知：Windows PowerShell 5.1 的应用标识才弹得出来，PowerShell 7 的 WinRT 类型不全。
func findSystemPowerShell51(env Env) (string, Source) {
	if env.SystemRoot == "" {
		return "", ""
	}
	p := filepath.Join(env.SystemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if env.exists(p) {
		return p, SourceSystem
	}
	return "", ""
}

// findSystemProgram 是 Windows 自带的命令：只认 %SystemRoot%\System32\<名字>.exe，不走 PATH。
func findSystemProgram(env Env, name string) (string, Source) {
	if env.SystemRoot == "" {
		return "", ""
	}
	p := filepath.Join(env.SystemRoot, "System32", name+".exe")
	if env.exists(p) {
		return p, SourceSystem
	}
	return "", ""
}
