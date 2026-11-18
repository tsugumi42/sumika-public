package toolchain

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

type layout struct {
	root         string
	pathDir      string
	configDir    string
	programFiles string
	programW6432 string
	localAppData string
	systemRoot   string
}

func writeFake(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, nil, 0o755); err != nil {
		t.Fatal(err)
	}
}

func newLayout(t *testing.T) layout {
	t.Helper()
	root := t.TempDir()
	l := layout{
		root:         root,
		pathDir:      filepath.Join(root, "path"),
		configDir:    filepath.Join(root, "config"),
		programFiles: filepath.Join(root, "pf"),
		programW6432: filepath.Join(root, "pf64"),
		localAppData: filepath.Join(root, "local"),
		systemRoot:   filepath.Join(root, "sys"),
	}
	for _, dir := range []string{l.pathDir, l.configDir, l.programFiles, l.programW6432, l.localAppData, l.systemRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", l.pathDir)
	t.Setenv("PATHEXT", ".COM;.EXE;.BAT;.CMD")
	t.Setenv("ProgramFiles", l.programFiles)
	t.Setenv("ProgramW6432", l.programW6432)
	t.Setenv("LOCALAPPDATA", l.localAppData)
	t.Setenv("SystemRoot", l.systemRoot)
	return l
}

func (l layout) env() Env {
	return Env{
		LookPath:     exec.LookPath,
		Exists:       func(p string) bool { _, err := os.Stat(p); return err == nil },
		ResourceRoot: l.root,
		ProgramFiles: l.programFiles,
		ProgramW6432: l.programW6432,
		LocalAppData: l.localAppData,
		SystemRoot:   l.systemRoot,
	}
}

func vendorPath(name string) func(layout) string {
	return func(l layout) string { return filepath.Join(l.root, "vendor", "bin", exeName(name)) }
}

func pathPath(name string) func(layout) string {
	return func(l layout) string { return filepath.Join(l.pathDir, exeName(name)) }
}

func populateBundled(t *testing.T, l layout) {
	t.Helper()
	for _, name := range []string{"pwsh", "powershell", "rg", "fd", "ffmpeg", "ffprobe", "git"} {
		writeFake(t, vendorPath(name)(l))
	}
}

func populateConfig(t *testing.T, l layout) {
	t.Helper()
	writeFake(t, filepath.Join(l.configDir, "ffmpeg.exe"))
	writeFake(t, filepath.Join(l.configDir, "ffprobe.exe"))
}

func populatePath(t *testing.T, l layout) {
	t.Helper()
	for _, name := range []string{"pwsh", "powershell", "rg", "fd", "ffmpeg", "ffprobe", "git", "taskkill", "rundll32"} {
		writeFake(t, pathPath(name)(l))
	}
}

func populateSystem(t *testing.T, l layout) {
	t.Helper()
	writeFake(t, filepath.Join(l.programFiles, "PowerShell", "7", "pwsh.exe"))
	writeFake(t, filepath.Join(l.systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe"))
	writeFake(t, filepath.Join(l.programFiles, "Git", "cmd", "git.exe"))
	writeFake(t, filepath.Join(l.systemRoot, "System32", "taskkill.exe"))
	writeFake(t, filepath.Join(l.systemRoot, "System32", "rundll32.exe"))
}

func byName(t *testing.T, results []Result, name string) Result {
	t.Helper()
	for _, r := range results {
		if r.Name == name {
			return r
		}
	}
	t.Fatalf("登记表里没有 %s：%+v", name, results)
	return Result{}
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

type expectation struct {
	source Source
	path   func(layout) string // nil 表示这个布局下不该找到
}

// 规格-外部程序清单 验收第 1 条的五种布局：只有自带、只有配置、只有 PATH、只有系统目录、都有。
// 每个登记程序在每个布局下要么找到规定的那一个，要么找不到；taskkill、rundll32 只认系统目录。
func TestLookupOrderLayouts(t *testing.T) {
	cases := []struct {
		name  string
		setup func(*testing.T, layout)
		want  map[string]expectation
	}{
		{
			name: "只有自带", setup: populateBundled,
			want: map[string]expectation{
				"pwsh":       {SourceBundled, vendorPath("pwsh")},
				"powershell": {SourceBundled, vendorPath("pwsh")}, // 执行用的先 7
				"rg":         {SourceBundled, vendorPath("rg")},
				"fd":         {SourceBundled, vendorPath("fd")},
				"ffmpeg":     {SourceBundled, vendorPath("ffmpeg")},
				"ffprobe":    {SourceBundled, vendorPath("ffprobe")},
				"git":        {SourceBundled, vendorPath("git")},
			},
		},
		{
			name: "只有配置", setup: populateConfig,
			want: map[string]expectation{
				"ffmpeg":  {SourceConfig, func(l layout) string { return filepath.Join(l.configDir, "ffmpeg.exe") }},
				"ffprobe": {SourceConfig, func(l layout) string { return filepath.Join(l.configDir, "ffprobe.exe") }},
			},
		},
		{
			name: "只有 PATH", setup: populatePath,
			want: map[string]expectation{
				"pwsh":       {SourcePath, pathPath("pwsh")},
				"powershell": {SourcePath, pathPath("pwsh")}, // 先 7
				"rg":         {SourcePath, pathPath("rg")},
				"fd":         {SourcePath, pathPath("fd")},
				"ffmpeg":     {SourcePath, pathPath("ffmpeg")},
				"ffprobe":    {SourcePath, pathPath("ffprobe")},
				"git":        {SourcePath, pathPath("git")},
			},
		},
		{
			name: "只有系统目录", setup: populateSystem,
			want: map[string]expectation{
				"pwsh":       {SourceSystem, func(l layout) string { return filepath.Join(l.programFiles, "PowerShell", "7", "pwsh.exe") }},
				"powershell": {SourceSystem, func(l layout) string { return filepath.Join(l.programFiles, "PowerShell", "7", "pwsh.exe") }},
				"powershell51": {SourceSystem, func(l layout) string {
					return filepath.Join(l.systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
				}},
				"git":      {SourceSystem, func(l layout) string { return filepath.Join(l.programFiles, "Git", "cmd", "git.exe") }},
				"taskkill": {SourceSystem, func(l layout) string { return filepath.Join(l.systemRoot, "System32", "taskkill.exe") }},
				"rundll32": {SourceSystem, func(l layout) string { return filepath.Join(l.systemRoot, "System32", "rundll32.exe") }},
			},
		},
		{
			name: "都有",
			setup: func(t *testing.T, l layout) {
				populateBundled(t, l)
				populateConfig(t, l)
				populatePath(t, l)
				populateSystem(t, l)
			},
			want: map[string]expectation{
				"pwsh":       {SourceBundled, vendorPath("pwsh")},
				"powershell": {SourceBundled, vendorPath("pwsh")},
				"powershell51": {SourceSystem, func(l layout) string {
					return filepath.Join(l.systemRoot, "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
				}},
				"rg":       {SourceBundled, vendorPath("rg")},
				"fd":       {SourceBundled, vendorPath("fd")},
				"ffmpeg":   {SourceBundled, vendorPath("ffmpeg")},
				"ffprobe":  {SourceBundled, vendorPath("ffprobe")},
				"git":      {SourceBundled, vendorPath("git")},
				"taskkill": {SourceSystem, func(l layout) string { return filepath.Join(l.systemRoot, "System32", "taskkill.exe") }},
				"rundll32": {SourceSystem, func(l layout) string { return filepath.Join(l.systemRoot, "System32", "rundll32.exe") }},
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			l := newLayout(t)
			tc.setup(t, l)
			env := l.env()
			env.FFmpeg = filepath.Join(l.configDir, "ffmpeg.exe")
			env.FFprobe = filepath.Join(l.configDir, "ffprobe.exe")
			results := Resolve(env)
			for _, e := range registry {
				got := byName(t, results, e.name)
				want, ok := tc.want[e.name]
				if !ok {
					if got.Found {
						t.Errorf("%s：这个布局不该找到，却找到 %+v", e.name, got)
					}
					continue
				}
				// Git for Windows 的标准安装目录（Git\cmd\git.exe 这类反斜杠写法）只在 Windows 上找，别的系统上系统目录里的 git 不算找到
				if e.name == "git" && want.source == SourceSystem && runtime.GOOS != "windows" {
					want.path = nil
				}
				if want.path == nil {
					if got.Found {
						t.Errorf("%s：这个布局不该找到，却找到 %+v", e.name, got)
					}
					continue
				}
				wantPath := want.path(l)
				if !got.Found || !samePath(got.Path, wantPath) || got.Source != want.source {
					t.Errorf("%s：找到 %+v，要 %s %s", e.name, got, want.source, wantPath)
				}
			}
		})
	}
}

// 执行工具那一行先 7 后 5.1：自带里只有 5.1 时，powershell 行用它，pwsh 行不算找到。
func TestPowerShellBundledFiveOne(t *testing.T) {
	l := newLayout(t)
	writeFake(t, vendorPath("powershell")(l))
	results := Resolve(l.env())
	if got := byName(t, results, "pwsh"); got.Found {
		t.Errorf("pwsh 行只认 7，不该找到 %+v", got)
	}
	ps := byName(t, results, "powershell")
	if !ps.Found || !samePath(ps.Path, vendorPath("powershell")(l)) || ps.Source != SourceBundled {
		t.Errorf("powershell 行应找到自带的 5.1：%+v", ps)
	}
}

// 有的机器上 fd 装成 fdfind：PATH 里只有 fdfind 也要找到。
func TestFdAliasFromPath(t *testing.T) {
	l := newLayout(t)
	writeFake(t, filepath.Join(l.pathDir, exeName("fdfind")))
	got := byName(t, Resolve(l.env()), "fd")
	if !got.Found || !samePath(got.Path, filepath.Join(l.pathDir, exeName("fdfind"))) || got.Source != SourcePath {
		t.Errorf("fd 应找到 PATH 里的 fdfind：%+v", got)
	}
}

// Windows 上程序名带 .exe，别的系统不带。
func TestExeName(t *testing.T) {
	want := "pwsh"
	if runtime.GOOS == "windows" {
		want = "pwsh.exe"
	}
	if got := exeName("pwsh"); got != want {
		t.Errorf("exeName(pwsh) = %q，要 %q", got, want)
	}
}

// 查找按当前系统的程序名：Windows 上仍然先找 pwsh.exe（不能变成裸名，否则可能碰到 .cmd / .bat 包装）。
func TestFindPwshLooksForPlatformName(t *testing.T) {
	var asked []string
	env := Env{
		LookPath: func(name string) (string, error) {
			asked = append(asked, name)
			return "", exec.ErrNotFound
		},
		Exists: func(string) bool { return false },
	}
	if p, source := findPwsh(env); p != "" || source != "" {
		t.Fatalf("假环境里不该找到 pwsh：%q %q", p, source)
	}
	if len(asked) != 1 || asked[0] != exeName("pwsh") {
		t.Fatalf("findPwsh 查了 %v，要只查一次 %q", asked, exeName("pwsh"))
	}
}

// 登记表本身：名字和顺序固定，只有 rg、fd 是必需，每个都有用途。
func TestRegistryShape(t *testing.T) {
	want := []string{"pwsh", "powershell", "powershell51", "rg", "fd", "ffmpeg", "ffprobe", "git", "taskkill", "rundll32"}
	if len(registry) != len(want) {
		t.Fatalf("登记了 %d 个程序，要 %d", len(registry), len(want))
	}
	for i, e := range registry {
		if e.name != want[i] {
			t.Errorf("第 %d 个登记 %q，要 %q", i, e.name, want[i])
		}
		if e.purpose == "" {
			t.Errorf("%s 没有用途", e.name)
		}
		if e.required != (e.name == "rg" || e.name == "fd") {
			t.Errorf("%s 的 Required = %v", e.name, e.required)
		}
	}
}

// 配置里写的路径找不到：警告一次，然后继续按 PATH 找。
func TestConfiguredMissingWarnsOnce(t *testing.T) {
	l := newLayout(t)
	writeFake(t, filepath.Join(l.pathDir, exeName("ffmpeg")))
	env := l.env()
	env.FFmpeg = filepath.Join(l.configDir, "missing.exe")
	var warns []string
	env.Warnf = func(format string, args ...any) { warns = append(warns, fmt.Sprintf(format, args...)) }
	got := byName(t, Resolve(env), "ffmpeg")
	if !got.Found || !samePath(got.Path, filepath.Join(l.pathDir, exeName("ffmpeg"))) || got.Source != SourcePath {
		t.Errorf("配置写错应退回 PATH：%+v", got)
	}
	Resolve(env)
	if len(warns) != 1 || !strings.Contains(warns[0], "missing.exe") {
		t.Errorf("同一个找不到的配置路径只警告一次：%q", warns)
	}
}

func resetCache() {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	cacheMu.Lock()
	versionGen++
	cache = nil
	cacheSet = false
	latestVersions = nil
	cacheMu.Unlock()
}

// 没初始化时 Lookup 现查一次并进缓存；文件没了也还读缓存，Refresh 后才更新。
func TestLookupLazilyResolvesAndCaches(t *testing.T) {
	l := newLayout(t)
	rg := filepath.Join(l.pathDir, exeName("rg"))
	writeFake(t, rg)
	resetCache()

	got, ok := Lookup("rg")
	if !ok || !samePath(got.Path, rg) || got.Source != SourcePath {
		t.Fatalf("Lookup 应现查 PATH 里的 rg：%+v %v", got, ok)
	}
	if _, ok := Lookup("not-registered"); ok {
		t.Fatal("没登记的程序不该找到")
	}
	if err := os.Remove(rg); err != nil {
		t.Fatal(err)
	}
	if got, ok := Lookup("rg"); !ok || !samePath(got.Path, rg) {
		t.Errorf("缓存不该因为文件没了就变：%+v %v", got, ok)
	}
	Refresh(l.env())
	if _, ok := Lookup("rg"); ok {
		t.Error("Refresh 后应看到 rg 已经不在了")
	}
}

// 懒加载只查路径：缓存空时 Lookup 不起取版本的子进程，之后的 Wait 也不等版本；
// 显式 Refresh 才取版本。
func TestLookupLazyDoesNotFetchVersions(t *testing.T) {
	l := newLayout(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	rg := filepath.Join(l.pathDir, exeName("rg"))
	if err := os.WriteFile(rg, data, 0o755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(l.root, "version-called")
	t.Setenv("TOOLCHAIN_VERSION_HELPER", "1")
	t.Setenv("TOOLCHAIN_VERSION_MARKER", marker)
	restore := SetVersionCommandsForTests(map[string][]string{
		"rg": {"-test.run=TestToolchainVersionHelper"},
	})
	defer restore()
	resetCache()

	got, ok := Lookup("rg")
	if !ok || !samePath(got.Path, rg) || got.Version != "" {
		t.Fatalf("Lookup 应只查路径、不留版本：%+v %v", got, ok)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	waited := Wait(ctx)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("懒加载之后 Wait 不该等版本，卡了 %v", elapsed)
	}
	if v := byName(t, waited, "rg"); v.Version != "" {
		t.Errorf("懒加载不该取到版本：%+v", v)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("Lookup 执行了版本命令（marker err=%v）", err)
	}

	results := RefreshAndWait(ctx, l.env())
	if v := byName(t, results, "rg"); v.Version != "rg 14.1.0" {
		t.Errorf("Refresh 后应取到版本：%+v", v)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("Refresh 应执行版本命令：%v", err)
	}
}

// 缓存和查找并发安全（有 -race 时真查数据竞争）。
func TestLookupAndRefreshConcurrent(t *testing.T) {
	l := newLayout(t)
	env := l.env()
	Refresh(env)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				Lookup("rg")
				Refresh(env)
			}
		}()
	}
	wg.Wait()
}

// 验收第 5 条：一个假程序睡 10 秒，3 秒后放弃、版本留空；别的程序版本照常有，第一行才算版本。
func TestVersionFetchTimesOutAndKeepsOthers(t *testing.T) {
	newLayout(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := Env{
		LookPath: func(name string) (string, error) {
			if name == "rg" || name == "ffmpeg" {
				return exe, nil
			}
			return "", exec.ErrNotFound
		},
		Exists: func(string) bool { return false },
	}
	t.Setenv("TOOLCHAIN_VERSION_HELPER", "1")
	restore := SetVersionCommandsForTests(map[string][]string{
		"rg":     {"-test.run=TestToolchainVersionHelper"},
		"ffmpeg": {"-test.run=TestToolchainSleepHelper"},
	})
	defer restore()
	resetCache()

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	start := time.Now()
	results := RefreshAndWait(ctx, env)
	elapsed := time.Since(start)

	rg := byName(t, results, "rg")
	if rg.Version != "rg 14.1.0" {
		t.Errorf("rg 的版本应是输出第一行：%q", rg.Version)
	}
	ff := byName(t, results, "ffmpeg")
	if !ff.Found || ff.Version != "" {
		t.Errorf("睡 10 秒的程序应在 3 秒放弃、版本留空：%+v", ff)
	}
	if elapsed > 5*time.Second {
		t.Errorf("取版本拖了 %v，超时没生效", elapsed)
	}
}

// Refresh 只查路径，不等版本：版本命令卡住时它也要马上回来（宿主持锁只做这一步）。
func TestRefreshReturnsBeforeVersions(t *testing.T) {
	newLayout(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	env := Env{
		LookPath: func(name string) (string, error) {
			if name == "rg" || name == "ffmpeg" {
				return exe, nil
			}
			return "", exec.ErrNotFound
		},
		Exists: func(string) bool { return false },
	}
	t.Setenv("TOOLCHAIN_VERSION_HELPER", "1")
	restore := SetVersionCommandsForTests(map[string][]string{
		"rg":     {"-test.run=TestToolchainVersionHelper"},
		"ffmpeg": {"-test.run=TestToolchainSleepHelper"},
	})
	defer restore()
	resetCache()

	start := time.Now()
	results := Refresh(env)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Refresh 等了 %v，版本不该挡路径查找", elapsed)
	}
	if ff := byName(t, results, "ffmpeg"); ff.Version != "" {
		t.Errorf("Refresh 返回时版本还没取：%+v", ff)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()
	waited := Wait(ctx)
	if rg := byName(t, waited, "rg"); rg.Version != "rg 14.1.0" {
		t.Errorf("Wait 后应拿到版本：%+v", rg)
	}
	if ff := byName(t, waited, "ffmpeg"); ff.Version != "" {
		t.Errorf("睡 10 秒的程序版本应留空：%+v", ff)
	}
}

// 子进程入口：正常测试进程里直接返回，只有被取版本命令拉起时才干活。
func TestToolchainVersionHelper(t *testing.T) {
	if os.Getenv("TOOLCHAIN_VERSION_HELPER") != "1" {
		return
	}
	if marker := os.Getenv("TOOLCHAIN_VERSION_MARKER"); marker != "" {
		if err := os.WriteFile(marker, []byte("called"), 0o600); err != nil {
			fmt.Println(err)
		}
	}
	fmt.Println("rg 14.1.0")
	fmt.Println("second line")
}

func TestToolchainSleepHelper(t *testing.T) {
	if os.Getenv("TOOLCHAIN_VERSION_HELPER") != "1" {
		return
	}
	time.Sleep(10 * time.Second)
}
