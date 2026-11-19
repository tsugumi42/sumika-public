package main

// 环境自检快照的生产者（规格-环境自检 设计第 1 条）：基础部分宿主自己读配置和密钥库，
// 后台开着时再向 App 要运行部分；界面和命令行都读这一份，绘制时不算。
// 快照在每次 set（开关接收、打开失败、后台没开、退出）后重算；配置保存和解锁密钥经 Options.EnvChanged
// 触发重算；运行部分另外每 60 秒重算一次。

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/config"
	"sumika-native/internal/envcheck"
	"sumika-native/internal/paths"
	"sumika-native/internal/secrets"
	"sumika-native/internal/toolchain"

	backend "sumika-native/internal/app"
)

const (
	// envRuntimeRefresh 是运行部分的重算间隔（规格：每 60 秒一次）。
	envRuntimeRefresh = 60 * time.Second
	// envInputsTimeout 是向 App 要一次运行部分的上限：单条自检 2 秒，再加上读 MCP 隔离行。
	envInputsTimeout = 5 * time.Second
)

// hostEnvState 是环境快照和它的测试注入点；和 hostState 分开锁，读快照不占 op 锁。
type hostEnvState struct {
	mu   sync.RWMutex
	snap envcheck.Snapshot

	toolsMu      sync.Mutex
	toolsChecked bool
	tools        []envcheck.ToolResult
	// toolsSync 让一次重查（路径 + 版本）同一时间只有一份，版本等待在外面做。
	toolsSync sync.Mutex

	// runtime 只给测试替换运行部分的输入；nil 走当前 App.EnvInputs。
	runtime func(context.Context) envcheck.Inputs
	// resolve 只给测试替换外部程序查找；nil 走 toolchain.Refresh。
	resolve func(toolchain.Env) []toolchain.Result
	// waitVersions 只给测试替换版本等待；nil 走 toolchain.Wait。
	waitVersions func(context.Context) []toolchain.Result
}

// EnvSnapshot 是最近一次生成的环境快照；界面和命令行读它，不在绘制时读文件或查程序。
func (h *host) EnvSnapshot() envcheck.Snapshot {
	h.envSnap.mu.RLock()
	defer h.envSnap.mu.RUnlock()
	s := h.envSnap.snap
	s.Items = append([]envcheck.Item(nil), s.Items...)
	return s
}

// envChanged 由 App 在配置保存、密钥库变化后调用：重查外部程序，再重算快照。
// App 的调用方线程不被这里拖住，实际工作在后台依次做（同一时间只有一份）。
func (h *host) envChanged() {
	go h.refreshToolsAsync()
}

// refreshToolsAsync 在后台查一遍外部程序再重算快照。
// 查路径不持 op 锁：toolchain.Refresh 自己并发安全，只读配置和环境变量；杀毒软件扫着时一次能花几秒，
// 而开关接收排在 op 锁后面，不能被它挡住。记结果和重算快照才拿 op（内存 + 读配置；refreshEnv
// 要读 h.cur / h.mode，这两个只在 op 下改），这一段不跑外部程序。版本在外面等，取到再补一次快照。
func (h *host) refreshToolsAsync() {
	h.envSnap.toolsSync.Lock()
	defer h.envSnap.toolsSync.Unlock()

	if h.ctx.Err() != nil {
		return
	}
	results := h.resolveTools()

	h.op.Lock()
	if h.ctx.Err() != nil {
		h.op.Unlock()
		return
	}
	h.setTools(results)
	h.emitEnv(h.refreshEnv())
	h.op.Unlock()

	if h.envSnap.resolve != nil { // 测试注入一次给全，包括版本
		return
	}
	ctx, cancel := context.WithTimeout(h.ctx, toolchain.VersionTimeout+time.Second)
	wait := h.envSnap.waitVersions
	if wait == nil {
		wait = toolchain.Wait
	}
	withVersions := wait(ctx)
	cancel()

	h.op.Lock()
	defer h.op.Unlock()
	if h.ctx.Err() != nil {
		return
	}
	h.setTools(withVersions)
	h.emitEnv(h.refreshEnv())
}

// watchEnv 每 60 秒重算一次运行部分；后台不在接收模式时没有运行状态可算，不动。
func (h *host) watchEnv() {
	t := time.NewTicker(envRuntimeRefresh)
	defer t.Stop()
	for {
		select {
		case <-h.ctx.Done():
			return
		case <-t.C:
			h.op.Lock()
			if h.cur != nil && h.mode == backend.Receive && h.ctx.Err() == nil {
				h.emitEnv(h.refreshEnv())
			}
			h.op.Unlock()
		}
	}
}

// refreshEnv 重算整份快照：基础部分现读配置和密钥库，运行部分向 App 要（或按不可用原因记 unknown）。
// 返回算好的快照；调用方用 emitEnv 交给界面（宿主状态里带着同一份）。
func (h *host) refreshEnv() envcheck.Snapshot {
	location, cfg, cfgErr := h.envConfig()
	basic := h.envBasic(location, cfg, cfgErr)

	var runtime envcheck.Inputs
	switch {
	case h.envSnap.runtime != nil:
		runtime = h.envSnap.runtime(context.Background())
	case h.cur != nil && h.mode == backend.Receive:
		ctx, cancel := context.WithTimeout(context.Background(), envInputsTimeout)
		runtime = h.cur.EnvInputs(ctx)
		cancel()
	default:
		markRuntimeUnavailable(&runtime, h.envUnavailableReason())
	}

	runtime.At = time.Now()
	runtime.Mode = h.envMode()
	runtime.Secrets = basic.secrets
	runtime.Platform = basic.platform
	runtime.Tools = h.envToolsInput()
	runtime.Paths = basic.paths
	snap := envcheck.Build(runtime)

	h.envSnap.mu.Lock()
	h.envSnap.snap = snap
	h.envSnap.mu.Unlock()
	return snap
}

// emitEnv 把刚算出来的快照记进最近一次状态并交给界面，不重新发布阶段：
// 阶段发布有自己的先后次序（退出流程的测试钉住），环境重算不值得多一条同阶段的状态。
func (h *host) emitEnv(snap envcheck.Snapshot) {
	h.stateMu.Lock()
	h.state.Env = snap
	ok := h.state.Phase != ""
	h.stateMu.Unlock()
	if !ok || h.onEnv == nil {
		return
	}
	h.onEnv(snap)
}

// envUnavailableReason 是运行部分查不了时写进详情的原因：后台没开、只看历史，或退回只看历史前的失败摘要。
func (h *host) envUnavailableReason() string {
	if h.cur == nil {
		return "后台没开"
	}
	if h.mode != backend.Receive {
		reason := strings.TrimSpace(h.current().Reason)
		if reason == "" || reason == "只看历史" {
			return "只看历史"
		}
		return reason
	}
	return "后台没开"
}

func (h *host) envMode() envcheck.Mode {
	if h.cur == nil {
		return envcheck.ModeStopped
	}
	if h.mode == backend.Receive {
		return envcheck.ModeReceive
	}
	return envcheck.ModeArchive
}

func markRuntimeUnavailable(in *envcheck.Inputs, reason string) {
	in.Gateway = envcheck.GatewayInput{Reason: reason}
	in.Catalog = envcheck.CatalogInput{Reason: reason}
	in.Embed = envcheck.CheckInput{Reason: reason}
	in.Kernel = envcheck.CheckInput{Reason: reason}
	in.Plugins = envcheck.PluginsInput{Reason: reason}
	in.MCP = envcheck.MCPInput{CheckInput: envcheck.CheckInput{Reason: reason}}
	in.Analyzer = envcheck.AnalyzerInput{Reason: reason}
	in.Backup = envcheck.BackupInput{Reason: reason}
}

// envConfig 读这次快照用的资源根和配置；读不到配置也返回资源根，基础部分按能读到的尽量报。
func (h *host) envConfig() (backend.RootLocation, config.Config, error) {
	location, err := backend.ResourceRoot(h.opt.Root)
	if err != nil {
		return backend.RootLocation{}, config.Config{}, err
	}
	cfg, err := config.LoadIn(location.Dir, h.opt.DataDir)
	return location, cfg, err
}

type hostEnvBasic struct {
	secrets  envcheck.SecretsInput
	platform envcheck.PlatformInput
	paths    envcheck.PathsInput
}

// envBasic 生成不依赖 App 的基础部分：密钥库、这次选中的 QQ 账号、数据目录和绑机器的路径。
func (h *host) envBasic(location backend.RootLocation, cfg config.Config, cfgErr error) hostEnvBasic {
	var out hostEnvBasic
	dataDir := h.opt.DataDir
	if dataDir == "" && cfgErr == nil {
		dataDir = cfg.DataDirIn(location.Dir)
	}

	selected := h.envSelectedAccounts(cfg)
	if len(selected) > 0 {
		out.secrets.Account = string(selected[0].ID)
		for _, a := range selected {
			if strings.TrimSpace(a.Token) == "" {
				out.secrets.NeedToken = true
				break
			}
		}
	} else if h.account != "" {
		// 配置读不到或账号被删了：这次选中的号仍然要令牌，照实报。
		out.secrets.NeedToken, out.secrets.Account = true, h.account
	}

	locked := false
	var names []string
	if dataDir == "" {
		out.secrets.ReadErr = "找不到数据目录，读不了密钥状态"
	} else {
		file := paths.Paths{Root: dataDir}.SecretsFile()
		st, err := secrets.Open(file)
		switch {
		case err == nil:
			out.secrets.Exists, out.secrets.Unlocked, out.secrets.HasLocalCopy = true, true, true
			names = st.Names()
			if e := st.LocalCopyError(); e != nil {
				out.secrets.HasLocalCopy = false
				out.secrets.Note = e.Error()
			}
		case errors.Is(err, secrets.ErrNotFound):
			// 没有文件：Exists 为假，不用令牌时这一项是 na。
		case errors.Is(err, secrets.ErrLocked):
			out.secrets.Exists, out.secrets.Unlocked, locked = true, false, true
			out.secrets.Note = err.Error()
		case errors.Is(err, secrets.ErrFormat):
			out.secrets.Exists = true
			out.secrets.Note = "密钥文件格式不对，读不出来（可能被别的程序改坏了）"
		default:
			out.secrets.ReadErr = err.Error()
		}
	}

	hasName := make(map[string]bool, len(names))
	for _, name := range names {
		hasName[name] = true
	}
	out.platform = envcheck.PlatformInput{Enabled: len(cfg.QQ.Accounts) > 0 || h.account != "", Locked: locked}
	for _, a := range selected {
		id := string(a.ID)
		inline := strings.TrimSpace(a.Token) != ""
		out.platform.Accounts = append(out.platform.Accounts, envcheck.PlatformAccount{
			ID: id, URL: a.URL,
			HasToken: inline || hasName["qq."+id+".token"],
		})
	}

	out.paths.Root = dataDir
	out.paths.Source = "配置 dataDir"
	if h.opt.DataDir != "" {
		out.paths.Source = "命令行 -data"
	}
	if dataDir != "" && cfgErr == nil {
		for _, kv := range []struct{ key, value string }{
			{"embed.modelsRoot", cfg.Embed.ModelsRoot},
			{"embed.modelsExe", cfg.Embed.ModelsExe},
			{"embed.ortLibrary", cfg.Embed.OrtLibrary},
			{"qq.media.ffmpeg", cfg.QQ.Media.FFmpeg},
			{"qq.media.ffprobe", cfg.QQ.Media.FFprobe},
		} {
			value := strings.TrimSpace(kv.value)
			if value == "" || !filepath.IsAbs(value) {
				continue
			}
			if !pathInside(dataDir, value) {
				out.paths.Outside = append(out.paths.Outside, envcheck.PathValue{Key: kv.key, Value: value})
			}
		}
	}
	return out
}

// envSelectedAccounts 是这次要看的 QQ 账号：有选中的小号只看它，没有就把配置里的都列出来。
func (h *host) envSelectedAccounts(cfg config.Config) []config.QQAccount {
	if h.account == "" {
		return cfg.QQ.Accounts
	}
	for _, a := range cfg.QQ.Accounts {
		if string(a.ID) == h.account {
			return []config.QQAccount{a}
		}
	}
	return cfg.QQ.Accounts
}

// pathInside 说 path 在 dir 里面（同一个盘、按 Windows 的大小写不敏感比较）。
func pathInside(dir, path string) bool {
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	if filepath.IsAbs(rel) {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func (h *host) envToolsInput() envcheck.ToolsInput {
	h.envSnap.toolsMu.Lock()
	defer h.envSnap.toolsMu.Unlock()
	return envcheck.ToolsInput{Checked: h.envSnap.toolsChecked, Results: append([]envcheck.ToolResult(nil), h.envSnap.tools...)}
}

// toolEnv 是登记表按现在的配置和环境要看的路径。
func (h *host) toolEnv() toolchain.Env {
	location, cfg, cfgErr := h.envConfig()
	var env toolchain.Env
	if cfgErr == nil {
		env = toolchain.Env{
			ResourceRoot: location.Dir,
			FFmpeg:       cfg.QQ.Media.FFmpeg,
			FFprobe:      cfg.QQ.Media.FFprobe,
		}
	}
	env.ProgramFiles = os.Getenv("ProgramFiles")
	env.ProgramW6432 = os.Getenv("ProgramW6432")
	env.LocalAppData = os.Getenv("LocalAppData")
	env.SystemRoot = os.Getenv("SystemRoot")
	env.Warnf = h.logf
	return env
}

// resolveTools 按现在的配置重查登记表的路径（只 Stat / LookPath，不启动程序），不碰 op 锁。
// toolchain.Refresh 自己串行化并替换缓存；开后台的路径不再调它，第一次真用到外部程序时 Lookup 会现查。
func (h *host) resolveTools() []toolchain.Result {
	env := h.toolEnv()
	if h.envSnap.resolve != nil {
		return h.envSnap.resolve(env)
	}
	return toolchain.Refresh(env)
}

// setTools 把登记表结果换成环境快照里的外部程序子项。
func (h *host) setTools(results []toolchain.Result) {
	out := make([]envcheck.ToolResult, 0, len(results))
	for _, r := range results {
		out = append(out, envcheck.ToolResult{Name: r.Name, Required: r.Required, Found: r.Found,
			Path: r.Path, Source: string(r.Source), Version: r.Version, Purpose: r.Purpose})
	}
	h.envSnap.toolsMu.Lock()
	h.envSnap.toolsChecked = true
	h.envSnap.tools = out
	h.envSnap.toolsMu.Unlock()
}
