package server

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"sumika-native/internal/localmodels/engine"
	"sumika-native/internal/localmodels/ort"
	"sumika-native/internal/localmodels/pool"
)

// Env 是 Run 要用的"外面的东西"。生产里全用零值（缺省）；测试注入假的，不用真 dll、真模型、真信号。
type Env struct {
	Getenv  func(string) string // 缺省 os.Getenv
	Stdin   io.Reader           // 缺省 os.Stdin；只有 --exit-on-stdin-eof 才读
	Stdout  io.Writer           // --verify 的逐项结果；缺省 os.Stdout
	Stderr  io.Writer           // 日志和错误行；缺省 os.Stderr
	Exe     func() (string, error)
	Exit    func(code int)    // 看门狗触发时调；缺省 os.Exit
	Signals <-chan os.Signal  // 缺省自己订 os.Interrupt 和 SIGTERM
	Ready   func(addr string) // 监听好、READY 行写完以后调（测试靠它拿端口）

	Registry *engine.Registry                          // 缺省 engine.BuiltinRegistry()
	OpenORT  func(absPath string) (ort.Runtime, error) // 缺省 ort.Open(路径, ort.KnownDLLs, ort.PinnedVersion)
	// MakeLoad 换掉真的加载函数（缺省 FileLoader.Load）：参数是已经被监视器包过的运行时，
	// 测试用它造假分词器加假会话，前向照样被监视、被看门狗盯着。
	MakeLoad func(rt ort.Runtime) pool.LoadFunc
	Limits   *Limits // 缺省 DefaultLimits()
}

func (e *Env) fill() {
	if e.Getenv == nil {
		e.Getenv = os.Getenv
	}
	if e.Stdin == nil {
		e.Stdin = os.Stdin
	}
	if e.Stdout == nil {
		e.Stdout = os.Stdout
	}
	if e.Stderr == nil {
		e.Stderr = os.Stderr
	}
	if e.Exe == nil {
		e.Exe = os.Executable
	}
	if e.Exit == nil {
		e.Exit = os.Exit
	}
	if e.OpenORT == nil {
		e.OpenORT = func(p string) (ort.Runtime, error) { return ort.Open(p, ort.KnownDLLs, ort.PinnedVersion) }
	}
}

// lockedWriter 让日志、错误行、池的日志共用一个写口：每次 Write 是整行，不交错。
type lockedWriter struct {
	mu sync.Mutex
	w  io.Writer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// lateRuntime 先占着位置：FileLoader 在启动检查之前就要建（Check、Verify 的哈希缓存在它里面，加载时不重算），
// 而 dll 要等文件检查过了才打开（规格 GM5 的检查顺序：点名的模型在 dll 前面）。dll 打开以后用 set 补上。
type lateRuntime struct {
	mu sync.Mutex
	rt ort.Runtime
}

func (l *lateRuntime) set(rt ort.Runtime) {
	l.mu.Lock()
	l.rt = rt
	l.mu.Unlock()
}

func (l *lateRuntime) get() ort.Runtime {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.rt
}

func (l *lateRuntime) Version() string {
	if rt := l.get(); rt != nil {
		return rt.Version()
	}
	return ""
}

func (l *lateRuntime) NewSession(modelPath string, o ort.Options) (ort.Session, error) {
	rt := l.get()
	if rt == nil {
		return nil, errors.New("ONNX Runtime 还没有加载")
	}
	return rt.NewSession(modelPath, o)
}

func (l *lateRuntime) Close() error { return nil } // 真的运行时由 Run 收尾时关

// loopbackHosts 是允许监听的地址（ADR-198 的"本机"）。
var loopbackHosts = map[string]bool{"127.0.0.1": true, "localhost": true, "::1": true}

// Run 是 sumika-models 的整个进程：解析参数 → 启动检查 → 起服务 → 等信号 / stdin 关闭 → 收尾。返回退出码。
//
// 启动检查按顺序（规格 GM5）：令牌 → 监听地址 → 模型根目录 → 登记表自身 → 点名的模型（--models，缺省 = 登记的全部）的文件和哈希
// → dll（ort.Open 的几道关）→ 端口。前几项失败是退出码 2，端口被占是 3；第一行都是 "[sumika-models] 错误：…"。
// --lazy-models 里的模型和没点名的模型不在启动时查哈希（lazy 的只看文件在不在，决定 ready）。
func Run(args []string, env Env) int {
	env.fill()
	lw := &lockedWriter{w: env.Stderr}
	fail := func(code int, format string, a ...any) int {
		fmt.Fprintf(lw, "%s%s\n", ErrPrefix, fmt.Sprintf(format, a...))
		return code
	}

	opts, usage, err := parseFlags(args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprint(lw, usage)
		return ExitOK
	}
	if err != nil {
		return fail(ExitConfig, "命令行参数有问题：%v（--help 看全部选项）", err)
	}
	if err := opts.check(); err != nil {
		return fail(ExitConfig, "%v", err)
	}

	if opts.verify {
		return runVerify(opts, &env, fail)
	}

	// 1. 令牌
	token := strings.TrimSpace(env.Getenv(TokenEnv))
	if token == "" {
		return fail(ExitConfig, "环境变量 %s 没设：每个请求都要带 Authorization: Bearer <令牌>，没有令牌就拒绝启动（令牌由起服务的一方生成，Local 里的 randomToken）", TokenEnv)
	}
	// 2. 监听地址
	if !loopbackHosts[opts.host] {
		return fail(ExitConfig, "只允许监听本机回环地址（127.0.0.1、localhost、::1），收到 %q", opts.host)
	}
	// 3. 模型根目录
	root, err := resolveRoot(opts, &env)
	if err != nil {
		return fail(ExitConfig, "%v", err)
	}
	// 4. 登记表自身，和点名的模型
	reg := env.Registry
	if reg == nil {
		if reg, err = engine.BuiltinRegistry(); err != nil {
			return fail(ExitConfig, "登记表自己有问题：%v", err)
		}
	}
	// --lazy-models 里不认识的 id（拼错、写成别的名字）只忽略、不退出：lazy 放的是重排这类可选的模型，它写错不该让必须就绪的嵌入也起不来
	lazyFlag, unknownLazy := dropUnknownLazy(reg, opts.lazyModels)
	named, lazy, err := selectModels(reg, opts.models, lazyFlag)
	if err != nil {
		return fail(ExitConfig, "%v", err)
	}
	if opts.maxLineTokens != 0 && opts.maxLineTokens < engine.MinMaxTokens {
		return fail(ExitConfig, "--max-line-tokens %d 太小：至少 %d", opts.maxLineTokens, engine.MinMaxTokens)
	}
	late := &lateRuntime{}
	loader, err := pool.NewFileLoader(root, late, ort.Options{IntraThreads: opts.threads}, opts.maxLineTokens)
	if err != nil {
		return fail(ExitConfig, "%v", err)
	}
	lg := log.New(lw, "", log.LstdFlags)
	// 启动检查期间的日志先攒着，全部过了、端口也绑上以后再一起写出去：检查失败时 stderr 的第一行必须是错误行
	// （Local 把启动阶段 stderr 的最后 20 行放进 lastErr，W1 契约第 6 条），不能前面先有几行 CHECKED。
	var boot bytes.Buffer
	bootLog := log.New(&boot, "", log.LstdFlags)
	for _, id := range unknownLazy {
		bootLog.Printf("WARN --lazy-models 里的模型 %s 不在登记表里，已忽略（登记的有：%s）；请求它会回 404，别的模型不受影响", clip(id), strings.Join(reg.SortedIDs(), "、"))
	}
	for _, spec := range named { // 点名的：文件在、哈希对，否则退出码 2（Local 停止重试：换一次也一样）
		if err := loader.Check(spec); err != nil {
			return fail(ExitConfig, "--models 点名的模型 %s 的文件有问题：%v", spec.ID, err)
		}
		ms, err := loader.Verify(context.Background(), spec)
		if err != nil {
			return fail(ExitConfig, "--models 点名的模型 %s 的文件和登记表对不上：%v", spec.ID, err)
		}
		bootLog.Printf("CHECKED model=%s hash_ms=%d", spec.ID, ms)
	}
	problems := map[string]string{}
	for _, spec := range lazy { // --lazy-models 里的：只看文件在不在，缺了只让它自己 ready=false
		if err := loader.Check(spec); err != nil {
			problems[spec.ID] = err.Error()
			bootLog.Printf("WARN model=%s 文件有问题，先不提供（ready=false）：%v", spec.ID, err)
		}
	}
	// 5. dll
	libPath, err := ortLibraryPath(opts, &env)
	if err != nil {
		return fail(ExitConfig, "%v", err)
	}
	rt, err := env.OpenORT(libPath)
	if err != nil {
		return fail(ExitConfig, "onnxruntime 动态库不合格（%s）：%v", libPath, err)
	}
	mon := NewMonitor()
	monitored := mon.WrapRuntime(rt)
	late.set(monitored)
	// 6. 端口
	port, err := resolvePort(opts, &env)
	if err != nil {
		_ = rt.Close()
		return fail(ExitConfig, "%v", err)
	}
	ln, err := net.Listen("tcp", net.JoinHostPort(opts.host, strconv.Itoa(port)))
	if err != nil {
		_ = rt.Close()
		if isPortProblem(err) {
			return fail(ExitPortBusy, "%s:%d 已被占用（或没有权限绑定）：%v；Local 会换一个口重试，手起的话用 --port 换一个", opts.host, port, err)
		}
		return fail(ExitConfig, "监听 %s:%d 失败：%v", opts.host, port, err)
	}

	_, _ = lw.Write(boot.Bytes())

	// 都过了：建池、建服务
	served := append(append([]engine.Spec(nil), named...), lazy...)
	entries := make([]pool.Entry, len(served))
	for i, spec := range served {
		entries[i] = pool.Entry{Spec: spec, Problem: problems[spec.ID]}
	}
	load := loader.Load
	if env.MakeLoad != nil {
		load = env.MakeLoad(monitored)
	}
	p, err := pool.New(pool.Config{
		Models: entries, Load: load, Estimate: loader.Estimate,
		MaxLoaded: max(1, opts.maxLoaded), // server.py 是 max(1, n)；池里 0 是"用缺省 2"，不能把命令行的 0 直接传进去
		Idle:      seconds(opts.idleUnload), MaxBytes: uint64(opts.maxBytes), Wait: seconds(opts.wait),
		CloseWait: 5 * time.Second, LogOut: lw,
	})
	if err != nil {
		_ = ln.Close()
		_ = rt.Close()
		return fail(ExitConfig, "建模型池失败：%v", err)
	}
	srvh, err := New(Config{
		Pool: p, Models: served, Registered: reg.IDs(), Token: token, ORTVersion: rt.Version(),
		MaxRequest: seconds(opts.maxRequest), Monitor: mon, Log: lg,
	})
	if err != nil {
		_ = ln.Close()
		p.Close()
		_ = rt.Close()
		return fail(ExitConfig, "建 HTTP 服务失败：%v", err)
	}
	limits := DefaultLimits()
	if env.Limits != nil {
		limits = *env.Limits
	}
	hs := NewHTTPServer(srvh.Handler(), limits, log.New(lw, "HTTP ", log.LstdFlags))

	lg.Printf("READY engine=go ort=%s listen=%s models=%s lazy=%s max_loaded=%d idle_unload=%gs threads=%d max_line_tokens=%d watchdog=%gs",
		rt.Version(), ln.Addr(), idList(named), idList(lazy), max(1, opts.maxLoaded), opts.idleUnload, opts.threads, opts.maxLineTokens, opts.watchdog)
	if env.Ready != nil {
		env.Ready(ln.Addr().String())
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- Serve(hs, ln, limits) }()

	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if opts.watchdog > 0 {
		wd := &Watchdog{Mon: mon, Limit: seconds(opts.watchdog), Fail: func(code int, msg string) {
			fmt.Fprintf(lw, "%s%s\n", ErrPrefix, msg)
			env.Exit(code)
		}}
		go wd.Run(ctx)
	}
	stdinGone := make(chan struct{})
	if opts.exitOnStdinEOF {
		go func() {
			_, _ = io.Copy(io.Discard, env.Stdin) // 读到 EOF 或出错都说明父进程不在了
			close(stdinGone)
		}()
	}
	sigs := env.Signals
	if sigs == nil {
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(ch)
		sigs = ch
	}

	code := ExitOK
	select {
	case s := <-sigs:
		lg.Printf("STOP reason=signal signal=%v", s)
	case <-stdinGone:
		lg.Printf("STOP reason=stdin-eof")
	case err := <-serveErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			code = fail(ExitFailed, "HTTP 服务出错退出：%v", err)
		}
	}

	// 收尾：先停 HTTP（等处理中的请求，等不到就断开它们：断开会取消请求的 ctx，只停在行边界），再关池，最后关运行时。
	// 兜底计时器：Session.Close 要等正在跑的那一路 Run 结束，Run 卡死时 Close 也会卡，不能让收尾无限等下去。
	hard := time.AfterFunc(15*time.Second, func() {
		fmt.Fprintf(lw, "%s收尾超过 15 秒，强制退出\n", ErrPrefix)
		env.Exit(ExitFailed)
	})
	defer hard.Stop()
	shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	if err := hs.Shutdown(shutCtx); err != nil {
		_ = hs.Close()
	}
	cancel()
	stop()
	p.Close()
	_ = rt.Close()
	lg.Printf("EXIT code=%d", code)
	return code
}

func idList(specs []engine.Spec) string {
	ids := make([]string, len(specs))
	for i, s := range specs {
		ids[i] = s.ID
	}
	if len(ids) == 0 {
		return "-"
	}
	return strings.Join(ids, ",")
}

// resolveRoot 取模型根目录：--models-root > 环境变量 LOCAL_MODELS_ROOT > 没配（报错）。Go 版没有默认路径（ADR-098）。
func resolveRoot(opts *options, env *Env) (string, error) {
	root := strings.TrimSpace(opts.modelsRoot)
	if root == "" {
		root = strings.TrimSpace(env.Getenv(ModelsRootEnv))
	}
	if root == "" {
		return "", fmt.Errorf("模型根目录没配：用 --models-root 或环境变量 %s 指过去（这个程序没有默认路径）", ModelsRootEnv)
	}
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("模型根目录必须是绝对路径：%q", root)
	}
	info, err := os.Stat(root)
	switch {
	case err != nil:
		return "", fmt.Errorf("模型根目录不存在或读不了：%s（%v）", root, err)
	case !info.IsDir():
		return "", fmt.Errorf("模型根目录不是目录：%s", root)
	}
	return filepath.Clean(root), nil
}

// ortLibraryPath 是 onnxruntime.dll 的绝对路径：--ort-library，缺省是本程序所在目录里的 onnxruntime.dll（规格 2.8）。
// 不搜索、不回退到系统目录；相对路径由 ort.Open 拒绝。
func ortLibraryPath(opts *options, env *Env) (string, error) {
	if p := strings.TrimSpace(opts.ortLibrary); p != "" {
		return p, nil
	}
	exe, err := env.Exe()
	if err != nil {
		return "", fmt.Errorf("找不到本程序自己的位置，没法定 onnxruntime.dll 的缺省路径，请用 --ort-library：%v", err)
	}
	return filepath.Join(filepath.Dir(exe), "onnxruntime.dll"), nil
}

// resolvePort：--port > 环境变量 SUMIKA_EMBED_PORT > 11440。
func resolvePort(opts *options, env *Env) (int, error) {
	if opts.port >= 0 {
		return opts.port, nil
	}
	raw := strings.TrimSpace(env.Getenv(PortEnv))
	if raw == "" {
		return DefaultPort, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 || n > 65535 {
		return 0, fmt.Errorf("%s=%q 不是 0～65535 的整数", PortEnv, raw)
	}
	return n, nil
}

// dropUnknownLazy 把 --lazy-models 里登记表没有的 id 拿掉，返回剩下的（逗号连起来）和被拿掉的。
// 重复写的不动，留给 selectModels 报"写了两次"。
func dropUnknownLazy(reg *engine.Registry, lazyFlag string) (kept string, dropped []string) {
	var ok []string
	for _, id := range splitIDs(lazyFlag) {
		if _, found := reg.Get(id); found {
			ok = append(ok, id)
		} else {
			dropped = append(dropped, id)
		}
	}
	return strings.Join(ok, ","), dropped
}

// selectModels 定下这个进程提供哪些模型。named 是必须就绪的（--models，缺省 = 登记的全部去掉 lazy 的），lazy 是 --lazy-models。
func selectModels(reg *engine.Registry, modelsFlag, lazyFlag string) (named, lazy []engine.Spec, err error) {
	lookup := func(flagName string, ids []string) ([]engine.Spec, error) {
		var out []engine.Spec
		seen := map[string]bool{}
		for _, id := range ids {
			spec, ok := reg.Get(id)
			if !ok {
				return nil, fmt.Errorf("%s 里的模型 %q 不在登记表里；登记的有：%s", flagName, id, strings.Join(reg.SortedIDs(), "、"))
			}
			if seen[id] {
				return nil, fmt.Errorf("%s 里的模型 %q 写了两次", flagName, id)
			}
			seen[id] = true
			out = append(out, spec)
		}
		return out, nil
	}
	lazyIDs, namedIDs := splitIDs(lazyFlag), splitIDs(modelsFlag)
	if lazy, err = lookup("--lazy-models", lazyIDs); err != nil {
		return nil, nil, err
	}
	if len(namedIDs) == 0 { // 缺省 = 登记的全部，去掉 lazy 的
		isLazy := map[string]bool{}
		for _, id := range lazyIDs {
			isLazy[id] = true
		}
		for _, id := range reg.IDs() {
			if !isLazy[id] {
				namedIDs = append(namedIDs, id)
			}
		}
	}
	if named, err = lookup("--models", namedIDs); err != nil {
		return nil, nil, err
	}
	inNamed := map[string]bool{}
	for _, s := range named {
		inNamed[s.ID] = true
	}
	for _, s := range lazy {
		if inNamed[s.ID] {
			return nil, nil, fmt.Errorf("模型 %q 同时在 --models 和 --lazy-models 里：要么启动时必须就绪，要么按需，不能两者都是", s.ID)
		}
	}
	if len(named)+len(lazy) == 0 {
		return nil, nil, errors.New("没有任何模型可提供")
	}
	return named, lazy, nil
}

// runVerify 是 --verify：不起服务、不要令牌、不加载 dll，算全部登记模型的文件哈希对登记表。
// 退出码 0 = 全对，1 = 有对不上的（不是 2：文件对不上是被检查出的结果，不是命令写错了）。
func runVerify(opts *options, env *Env, fail func(int, string, ...any) int) int {
	root, err := resolveRoot(opts, env)
	if err != nil {
		return fail(ExitConfig, "%v", err)
	}
	reg := env.Registry
	if reg == nil {
		if reg, err = engine.BuiltinRegistry(); err != nil {
			return fail(ExitConfig, "登记表自己有问题：%v", err)
		}
	}
	loader, err := pool.NewFileLoader(root, nil, ort.Options{}, 0)
	if err != nil {
		return fail(ExitConfig, "%v", err)
	}
	bad := 0
	for _, id := range reg.IDs() {
		spec, _ := reg.Get(id)
		if err := loader.Check(spec); err != nil {
			bad++
			fmt.Fprintf(env.Stdout, "FAIL %s  %v\n", id, err)
			continue
		}
		ms, err := loader.Verify(context.Background(), spec)
		if err != nil {
			bad++
			fmt.Fprintf(env.Stdout, "FAIL %s  %v\n", id, err)
			continue
		}
		fmt.Fprintf(env.Stdout, "OK   %s  onnx 和 tokenizer.json 的 SHA-256 都对（%d 毫秒）\n", id, ms)
	}
	if bad > 0 {
		return fail(ExitFailed, "%d 个模型的文件和登记表对不上（详情在 stdout）", bad)
	}
	return ExitOK
}
