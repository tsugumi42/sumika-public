package main

// 后台宿主（规格-界面接Go后台 1.2、3.2、3.3）：桌面入口里唯一管 backend.App 的地方，全部在后台线程跑。
// 每次状态变化（含失败原因）都写一行日志（desklog.go）：桌面没有控制台，状态区之外只有日志文件能查原因。
// 默认只看历史（archive）；托盘点"开始接收 QQ"才关掉 archive、按配置里确认过的那一个小号开 receive，
// 开不了就重开 archive 并写明原因；连 archive 也开不了（数据锁被别的进程拿走）就明说后台没在运行。
// 状态变化经 publish 交给界面线程；界面拿到的 Reader 在换后台之前先 Desktop.Close，查询不会碰到关掉的库。

import (
	"context"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sumika-native/internal/applog"
	"sync"
	"time"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/config"
	"sumika-native/internal/datalock"
	"sumika-native/internal/envcheck"
	"sumika-native/internal/rpcserve"
)

// 宿主阶段：界面的状态区和托盘提示照这个写
const (
	phaseStarting  = "starting"  // 正在检查后台
	phaseArchive   = "archive"   // 只看历史
	phaseLocal     = "local"     // 本地可用（QQ 没开）：后台按接收模式开着，只是没装 QQ 单元
	phaseReceiving = "receiving" // 接收中
	phaseSwitching = "switching" // 正在切换
	phaseBlocked   = "blocked"   // 后台没在运行
	phaseStopping  = "stopping"  // 正在退出
	phaseStopped   = "stopped"
)

type hostState struct {
	Phase   string
	Mode    backend.Mode
	Reader  api.Reader       // 没有可用后台时为 nil
	Memory  api.MemoryReader // 记忆页的只读接口，和 Reader 同生同灭；没有可用后台时为 nil
	Reason  string           // 给人看的原因：失败、受阻、回退
	Account string           // "开始接收 QQ"用的小号（打码）；空 = 菜单不可用
	NoRecv  string           // 不能开始接收的原因
	// Accounts 是配置里的 QQ 小号数（-1 = 还没读到）。0 和“多个没指定”是两件事：
	// 0 个启动时直接开本地可用，多个照旧停在只看历史等 -account（规格-不带QQ也能用 设计第 3 条）。
	Accounts int

	// 本地私聊交给 Bun 时（backend.local 是 bun，规格-本地私聊切到Bun）：Bun 子进程现在连着没有、给人看的一行说明。
	// 连着时 archive 阶段也能发本地消息（本地私聊归 Bun，不归 Go 的 receive）；不在时发送被拦下并显示这一行
	LocalByBun bool
	LocalOK    bool
	LocalLine  string

	// 模型目录取不到时的原因和来源地址（规格-模型配置 6.1 第 7 条）：目录是好的时 CatalogError 空。
	// 由 watchCatalog 从后台的当前目录带过来，界面在聊天框上方写“模型来源连不上”那一行。
	CatalogSource string
	CatalogError  string

	// StoreUnavailable 是打不开、已经整个停掉的平台库（规格-库版本与升级护栏 第 8 条）：
	// 每个平台一行，带原因和解除办法。后台每次打开时重算。
	StoreUnavailable []string
	// Env 是最近一次环境自检快照：宿主重算后随状态发布，运行页只读它（hostenval.go）。
	// 零值表示还没跑完；界面照这个写“环境检查还没跑完”。
	Env envcheck.Snapshot
}

// localSendOff 说现在为什么不能发本地消息；空串就是可以。
// go 模式：只看历史（archive）阶段不能发；bun 模式：看 Bun 子进程在不在，和 Go 是不是 archive 无关
func (s hostState) localSendOff() string {
	if s.LocalByBun {
		if s.LocalOK {
			return ""
		}
		if s.LocalLine == "" {
			return "本地私聊的后台不在"
		}
		return s.LocalLine
	}
	if s.Mode == backend.Archive {
		return "请先启动后台，再发送本地消息"
	}
	return ""
}

type host struct {
	opt       backend.Options
	account   string // 配置里确认过的那一个小号；空表示菜单不可用
	qqCount   int    // 配置里的 QQ 小号数；-1 表示配置没读到
	noRecv    string
	localOnly bool                // 当前开着的是“只开本地”（phaseLocal）；切换、回退据此决定回哪
	redactor  backend.LogRedactor // 日志和状态共用；宿主刷新，外壳可并发读
	publish   func(hostState)     // 后台线程调用；界面侧自己切回主线程

	ctx    context.Context // 退出时取消：打断正在进行的 Open
	cancel context.CancelFunc

	op sync.Mutex // 一次只做一个开、切、关
	// beforeOpen 只给测试：在锁空出来、下一次 Open 之前插一手（比如另一个进程抢走数据锁）
	beforeOpen func(backend.Mode)
	cur        *backend.App
	desk       *backend.Desktop
	mode       backend.Mode
	stopR      context.CancelFunc // receive 的 Run
	// rpcListen：-rpc-listen 打开时，后台开着就把 Reader 开到本机 WebSocket 上（规格-Bun后台第1批 第五节）；rpc 是开着的那个入口
	rpcListen bool
	rpc       *rpcserve.Listener
	// inprocRPC：界面拿到的 Reader 是经进程内管道的协议客户端（inproc_rpc.go，规格-Bun后台第2批 第四节）；false 就直接交 *DesktopReader。
	// bridge 随当前数据源建、随它关
	inprocRPC        bool
	bridge           *rpcBridge
	runCh            chan error
	state            hostState    // 只在 op 下写（set）；别的线程读用 current()
	stateMu          sync.RWMutex // 只保护 state：open 进行时 op 被占着，读状态的人（托盘之外的控制接口）不能被它卡住
	pubMu            sync.Mutex   // 更新 state 并 publish 作一步：set（op 里）和 Bun 状态变化（监护的协程里）都发布，顺序不能颠倒
	storeUnavailable []string     // 当前后台记下的不可用平台（open 成功时更新）；和 state 一起发布
	envSnap          hostEnvState // 环境自检快照：基础部分 + 运行部分（hostenval.go）
	// onEnv 只给外壳：快照重算后直接交给界面线程（改 a.host.Env 并重画），不走 publish——
	// 阶段发布有先后次序（退出流程的测试钉住了），环境重算不另加一次阶段发布。nil 表示没人接。
	onEnv func(envcheck.Snapshot)

	// 本地私聊交给 Bun（bunchild.go，临时：Electrobun 接管进程后去掉）：start 时读配置，bun 模式就拉起子进程；
	// 连接属于宿主，archive ↔ receive 切换重建桥时不跟着重来
	localByBun bool
	// localOwner 是 start 里读一次配置做的决定（go / bun）：之后每次 open 都把它交给后台（Options.LocalOwner），后台不再自己读 backend.local。
	// 空 = 还没做过决定（直接调 open 的测试），后台照配置
	localOwner string
	bunLink    *bunLink
	bunSup     *bunSupervisor
	localOK    bool   // 受 stateMu 保护
	localLine  string // 同上
	// 测试替换：找 bun 和 server/、起子进程、调监护的时限
	resolveBun func(cfg config.Backend, root, dataDir string) (bunSpec, error)
	bunSpawn   func(bunSpec, func(string, ...any)) (bunProc, error)
	bunTune    func(*bunSupervisor)
}

// current 是最近一次 set 的状态，任何线程都能调，不等 op 锁
func (h *host) current() hostState {
	h.stateMu.RLock()
	defer h.stateMu.RUnlock()
	return h.state
}

func newHost(opt backend.Options, publish func(hostState)) *host {
	h := &host{opt: opt, publish: publish, inprocRPC: true, qqCount: -1}
	h.ctx, h.cancel = context.WithCancel(context.Background())
	return h
}

// receiveChoice 是启动时对“能不能开始接收 QQ”的判断：0 个和“多个没指定”必须分开，
// 前者直接开本地可用，后者照旧停在只看历史（规格-不带QQ也能用 设计第 3 条）。
type receiveChoice struct {
	account string
	count   int // 配置里的 QQ 小号数；-1 = 配置没读到
	reason  string
}

// chooseReceiveAccount 只认显式给的 -account，或配置里恰好只有一个小号；不自动挑第一个（规格 1.2）。
// 显式给的号必须在这份配置里：不在就不返回它，开始接收因此不可用（旧版会照用下去，这是有意的收紧）。
func chooseReceiveAccount(opt backend.Options) receiveChoice {
	location, err := backend.ResourceRoot(opt.Root)
	if err != nil {
		return receiveChoice{count: -1, reason: "读不到配置：" + err.Error()}
	}
	cfg, err := config.LoadIn(location.Dir, opt.DataDir)
	if err != nil {
		return receiveChoice{count: -1, reason: "读不到配置：" + err.Error()}
	}
	count := len(cfg.QQ.Accounts)
	if opt.Account != "" {
		for _, a := range cfg.QQ.Accounts {
			if string(a.ID) == opt.Account {
				return receiveChoice{account: opt.Account, count: count}
			}
		}
		if count == 0 {
			return receiveChoice{count: 0, reason: "配置里没有 QQ 小号，现在只开了本地"}
		}
		return receiveChoice{count: count, reason: fmt.Sprintf("配置里没有 %s 这个小号", config.MaskID(opt.Account))}
	}
	switch count {
	case 0:
		return receiveChoice{count: 0, reason: "配置里没有 QQ 小号，现在只开了本地"}
	case 1:
		return receiveChoice{account: string(cfg.QQ.Accounts[0].ID), count: 1}
	}
	return receiveChoice{count: count, reason: fmt.Sprintf("配置里有 %d 个小号，要用 -account 指定一个", count)}
}

// pickReceiveAccount 是 chooseReceiveAccount 的两值形式（测试和旧调用沿用）
func pickReceiveAccount(opt backend.Options) (string, string) {
	c := chooseReceiveAccount(opt)
	return c.account, c.reason
}

// logf 写宿主自己的日志行：opt.Logf 在桌面里是日志文件（desklog.go），测试里是 t.Logf；没有就 log.Printf
func (h *host) logf(format string, args ...any) {
	if h.opt.Logs != nil {
		if !h.opt.Logs.Enabled(applog.Desk, slog.LevelInfo) {
			return // 级别没开：不格式化也不打码
		}
		h.opt.Logs.Masked(applog.Desk, slog.LevelInfo)("%s", h.redactor.Redact(fmt.Sprintf(format, args...)))
		return
	}
	line := h.redactor.Redact(fmt.Sprintf(format, args...))
	if h.opt.Logf != nil {
		h.opt.Logf("%s", line)
		return
	}
	log.Printf("%s", line)
}

func (h *host) set(s hostState) {
	s.Reason = h.redactor.Redact(s.Reason)
	h.logState(s)
	s.NoRecv = h.redactor.Redact(h.noRecv)
	s.Accounts = h.qqCount
	if h.account != "" {
		s.Account = config.MaskID(h.account)
	}
	h.pubMu.Lock()
	h.stateMu.Lock()
	s.LocalByBun, s.LocalOK, s.LocalLine = h.localByBun, h.localOK, h.localLine
	s.CatalogSource, s.CatalogError = h.state.CatalogSource, h.state.CatalogError // 目录状态由 watchCatalog 单独更新，每次阶段变化不要清零
	s.StoreUnavailable = append([]string(nil), h.storeUnavailable...)
	s.Env = h.state.Env // 上一次的快照先顶上；下面算完再发一次
	h.state = s
	h.stateMu.Unlock()
	if h.desk != nil {
		h.desk.Publish(api.RuntimeView{Mode: string(s.Mode), Phase: s.Phase, Account: h.account, Reason: s.Reason})
	}
	h.publish(s)
	h.pubMu.Unlock()
	// 每次阶段变化都重算环境快照：开关接收、打开失败退回只看历史、后台没开都走这里。
	// 算完直接交给界面（不另发一条同阶段的状态）；重算可能等 App 几秒，不占着发布锁。
	h.emitEnv(h.refreshEnv())
}

// catalogPoll 是宿主看模型目录状态的间隔：只读后台内存里的当前目录，不重新列模型。
const catalogPoll = 2 * time.Second

// watchCatalog 把后台的模型目录状态带进宿主状态：目录取不到就发布原因，后台重试成功、CatalogError 空了
// 再发布一次（界面那一行自己消失，规格-模型配置 6.1 第 7 条）。变了才发布，每 2 秒一次原子读。
func (h *host) watchCatalog(ctx context.Context, a *backend.App) {
	last := "\x00" // 来源空、原因空：和新后台的空状态相同，第一次不白发布
	t := time.NewTicker(catalogPoll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			source, _, reason := a.CatalogState()
			key := source + "\x00" + reason
			if key == last {
				continue
			}
			last = key
			h.setCatalog(source, reason)
		}
	}
}

// setCatalog 只改模型目录那两项再发布；阶段和连接事实不动，也不写状态日志（不是后台阶段变化）。
func (h *host) setCatalog(source, reason string) {
	reason = h.redactor.Redact(reason)
	h.pubMu.Lock()
	defer h.pubMu.Unlock()
	h.stateMu.Lock()
	s := h.state
	s.CatalogSource, s.CatalogError = source, reason
	h.state = s
	h.stateMu.Unlock()
	if h.desk != nil {
		h.desk.Publish(api.RuntimeView{Mode: string(s.Mode), Phase: s.Phase, Account: h.account, Reason: s.Reason})
	}
	h.publish(s)
}

// logState 记一行后台状态；错误详情和独立日志都经共享脱敏，不能假定 Open 的错误已安全。
func (h *host) logState(s hostState) {
	line := phaseText(s.Phase)
	if s.Mode != "" {
		line += "，模式 " + string(s.Mode)
	}
	// 原因常以阶段名开头（"后台没在运行：……"）或干脆就是阶段名（"接收中"）：不重复写
	reason := strings.TrimPrefix(s.Reason, phaseText(s.Phase)+"：")
	if reason != "" && reason != phaseText(s.Phase) {
		line += "：" + reason
	}
	h.logf("后台状态：%s", line)
}

// open 开一个后台并交出它的只读接口；receive 另起 Run。localOnly 只和 receive 一起用（本地可用，不装 QQ 单元）
func (h *host) open(mode backend.Mode, localOnly bool) error {
	if h.beforeOpen != nil {
		h.beforeOpen(mode)
	}
	opt := h.opt
	h.redactor.Refresh(opt)
	opt.Mode = mode
	opt.LocalOwner = h.localOwner
	opt.EnvChanged = h.envChanged
	opt.LocalOnly = mode == backend.Receive && localOnly
	if mode == backend.Receive && !localOnly {
		opt.Account = h.account
	} else {
		opt.Account = ""
	}
	h.stateMu.Lock()
	h.storeUnavailable = nil // 换了后台：上一个后台不可用的平台不沿用
	h.stateMu.Unlock()
	a, err := backend.Open(h.ctx, opt)
	if err != nil {
		return err
	}
	h.cur, h.desk, h.mode = a, a.Desktop(), mode
	h.localOnly = mode == backend.Receive && localOnly
	h.stateMu.Lock() // 换了后台：上一个后台的不可用平台、目录状态都不沿用
	h.storeUnavailable = a.StoreUnavailableLines()
	h.state.CatalogSource, h.state.CatalogError = "", ""
	h.stateMu.Unlock()
	if h.inprocRPC || h.localByBun { // bun 模式必须经桥：写本地的方法靠它分给 Bun，直接用后台对象就绕过了分流
		b, err := newRPCBridge(h.ctx, &h.desk.Reader, nil, h.bunLink)
		if err != nil {
			if h.localByBun {
				h.desk.Close()
				_ = a.Close()
				h.cur, h.desk = nil, nil
				return fmt.Errorf("进程内协议没建成，本地私聊没法分给 Bun：%w", err)
			}
			// 管道握手不会失败；真失败了退回直接调，不让界面没有数据源
			h.logf("进程内协议没建成，界面直接用后台：%v", err)
		} else {
			h.bridge = b
		}
	}
	if h.rpcListen {
		// 开到 WebSocket 上的只有 Reader 的 8 个（规格 4.3）：直接用 DesktopReader，不套桥、不开写方法
		if l, err := rpcserve.Listen(h.ctx, rpcserve.New(&h.desk.Reader, buildVersion()), a.RPCGoRunFile()); err != nil {
			h.logf("界面协议入口没开成：%v", err)
		} else {
			h.rpc = l
			h.logf("界面协议入口开在 %s（只读）", l.URL)
		}
	}
	if mode == backend.Receive {
		runCtx, stop := context.WithCancel(context.Background())
		h.stopR, h.runCh = stop, make(chan error, 1)
		go func(ch chan error) { ch <- a.Run(runCtx) }(h.runCh)
		go h.watchCatalog(runCtx, a)
		go h.watchRun(a, h.runCh)
	}
	h.bunUp() // 数据锁拿到、后台开成功了才拉 Bun（第一次是启动，blocked 恢复后是重新拉起）
	return nil
}

// watchRun：receive 自己停了（适配器返回错误等），回到 archive 并写明原因；不自动重连
func (h *host) watchRun(a *backend.App, ch chan error) {
	err := <-ch
	ch <- err // 留给 closeCurrent 读
	h.op.Lock()
	defer h.op.Unlock()
	if h.cur != a || h.ctx.Err() != nil {
		return
	}
	h.closeCurrent()
	reason := "接收停了"
	if err != nil {
		reason = "接收停了：" + err.Error()
	}
	if h.localOnly {
		h.fallbackLocal(reason)
		return
	}
	h.fallback(reason)
}

// deskReader 用同一个指针交出读投影及可选写接口，让界面用 == 判断数据源没有变化。
// 读投影带函数字段，不能按值交出，否则界面比较接口会 panic。
// 默认交进程内协议的桥（同一个数据源里始终是同一个指针）；-inproc-rpc=false 或桥没建成就直接交 DesktopReader。
func (h *host) deskReader() api.Reader {
	if h.bridge != nil {
		return h.bridge
	}
	return &h.desk.Reader
}

// closeCurrent 先让界面的查询和订阅退出，再按模式收后台：receive 走 Run 的取消路径（它自己关库、放锁），archive 直接 Close
func (h *host) closeCurrent() error {
	if h.cur == nil {
		return nil
	}
	if h.rpc != nil { // 先断远端界面，再收后台
		h.rpc.Close()
		h.rpc = nil
	}
	if h.bridge != nil { // 界面的查询和订阅走桥：先断管道，服务端收掉订阅、取消在跑的请求
		h.bridge.Close()
		h.bridge = nil
	}
	h.desk.Close()
	var err error
	if h.mode == backend.Receive {
		h.stopR()
		err = <-h.runCh
	} else {
		err = h.cur.Close()
	}
	h.cur, h.desk, h.stopR, h.runCh = nil, nil, nil, nil
	return err
}

// fallback 回到 archive；回不去就明说后台没在运行
func (h *host) fallback(reason string) {
	if err := h.open(backend.Archive, false); err != nil {
		h.block(blockedReason(err) + "（" + reason + "）")
		return
	}
	h.set(hostState{Phase: phaseArchive, Mode: backend.Archive, Reader: h.deskReader(), Memory: h.desk.Memory, Reason: reason})
}

// fallbackLocal 回到“本地可用（QQ 没开）”；回不去才进 blocked（规格-不带QQ也能用 设计第 3 条）
func (h *host) fallbackLocal(reason string) {
	if err := h.open(backend.Receive, true); err != nil {
		h.block("后台没在运行：" + err.Error() + "（" + reason + "）")
		return
	}
	h.set(hostState{Phase: phaseLocal, Mode: backend.Receive, Reader: h.deskReader(), Memory: h.desk.Memory, Reason: reason})
}

func blockedReason(err error) string {
	if errors.Is(err, datalock.ErrDataInUse) {
		return "后台没在运行：锁被另一个进程占用"
	}
	return "后台没在运行：" + err.Error()
}

// start 启动：配置里一个 QQ 小号都没有就开本地可用，有小号照旧开只看历史（ADR-209）。
// 全新目录先只做初始化（写身份证、建空库），然后再按上面两条开；这样第一次和以后每次停在同一个阶段。
func (h *host) start() {
	h.op.Lock()
	defer h.op.Unlock()
	h.redactor.Refresh(h.opt)
	h.set(hostState{Phase: phaseStarting})
	if err := h.prepareBun(); err != nil { // 找不到 bun 或 server/：进 blocked 写明原因，不退回 Go 去管本地私聊
		h.block("后台没在运行：本地私聊交给了 Bun（sumika.yml 的 backend.local），但" + err.Error())
		return
	}
	choice := chooseReceiveAccount(h.opt) // 读配置在后台线程（规格第八节：ready 里不读文件）
	h.account, h.noRecv, h.qqCount = choice.account, choice.reason, choice.count
	if h.account != "" {
		h.logf("托盘可以开始接收的小号 %s", config.MaskID(h.account))
	} else {
		h.logf("托盘不能开始接收：%s", h.noRecv)
	}
	p, initialized, err := backend.InitDataDir(h.ctx, h.opt)
	if err != nil {
		if h.ctx.Err() != nil {
			return
		}
		h.block(blockedReason(err))
		return
	}
	if initialized {
		h.logf("新建了数据目录 %s（写了身份证）", p.Root)
		// 只有零小号才复制示例卡：有小号时 QQ 绑的角色要主人自己放卡，不能悄悄塞一张示例卡进角色列表
		if err := backend.EnsureExampleCharacter(p, h.opt, h.qqCount == 0); err != nil {
			if h.ctx.Err() != nil {
				return
			}
			h.block("后台没在运行：" + err.Error())
			return
		}
	}
	if h.qqCount == 0 {
		if err := h.open(backend.Receive, true); err != nil {
			if h.ctx.Err() != nil {
				return
			}
			h.block(blockedReason(err))
			return
		}
		reason := "本地可用（QQ 没开）"
		if initialized {
			reason += "；新建了数据目录 " + p.Root
		}
		h.set(hostState{Phase: phaseLocal, Mode: backend.Receive, Reader: h.deskReader(), Memory: h.desk.Memory, Reason: reason})
		go h.refreshToolsAsync()
		go h.watchEnv()
		return
	}
	if err := h.open(backend.Archive, false); err != nil {
		if h.ctx.Err() != nil {
			return
		}
		h.block(blockedReason(err))
		return
	}
	h.set(hostState{Phase: phaseArchive, Mode: backend.Archive, Reader: h.deskReader(), Memory: h.desk.Memory, Reason: "只看历史"})
	go h.refreshToolsAsync()
	go h.watchEnv()
}

// receiveOutcome 是 startReceive 这一次的结果种类。调用方（自动接收的重试、日志）按它判断，不去猜 Reason 的文字
type receiveOutcome int

const (
	receiveStarted       receiveOutcome = iota // 开始接收成功
	receiveAlready                             // 本来就在接收，什么都没做
	receiveCancelled                           // 程序正在退出，没做
	receiveNoAccount                           // 没有能接收的小号，没做（重试没用）
	receiveBlocked                             // 关后台时出错，后台进了 blocked（重试没用）
	receiveOpenFailed                          // 接收后台开不了，已回到出发时的阶段（协议端没起来等，值得重试；模型目录取不到不再算这一类）
	receiveLocalBusy                           // 本地有回合在跑，没拆后台（自动接收等下一轮再看）
	receiveLocalNotReady                       // 自动接收的便宜检查没过，仍留在本地可用（等下一轮再看）
)

// receiveResult 带着种类和错误；Err 只在 receiveBlocked、receiveOpenFailed、receiveLocalBusy、receiveLocalNotReady 时有
type receiveResult struct {
	Outcome receiveOutcome
	Err     error
}

// Retryable 只有“接收后台开不了”“本地有回合”“便宜检查没过”值得再试
func (r receiveResult) Retryable() bool {
	switch r.Outcome {
	case receiveOpenFailed, receiveLocalBusy, receiveLocalNotReady:
		return true
	}
	return false
}

// runningTurns 是当前后台正在跑的回合数：直接问 DesktopReader，不经过进程内桥（桥没有这个方法）
func (h *host) runningTurns() int {
	if h.desk == nil {
		return 0
	}
	return h.desk.Reader.RunningTurns()
}

// startReceive 关掉当前后台 → 开完整接收；开不了回出发时的阶段（规格 3.2、P2-1 设计第 3 条）。
// 出发时是本地可用的话，本地有回合在跑就拒绝，不切断回合。
func (h *host) startReceive() receiveResult {
	h.op.Lock()
	defer h.op.Unlock()
	if h.ctx.Err() != nil {
		return receiveResult{Outcome: receiveCancelled}
	}
	if h.mode == backend.Receive && h.cur != nil && !h.localOnly {
		return receiveResult{Outcome: receiveAlready}
	}
	if h.account == "" {
		h.set(hostState{Phase: h.state.Phase, Mode: h.state.Mode, Reader: h.state.Reader, Memory: h.state.Memory, Reason: "不能开始接收：" + h.noRecv})
		return receiveResult{Outcome: receiveNoAccount, Err: errors.New(h.noRecv)}
	}
	fromLocal := h.localOnly
	if fromLocal {
		if n := h.runningTurns(); n > 0 {
			reason := fmt.Sprintf("本地有回合在跑（%d 个），等它结束再开始接收", n)
			h.set(hostState{Phase: phaseLocal, Mode: backend.Receive, Reader: h.state.Reader, Memory: h.state.Memory, Reason: "不能开始接收：" + reason})
			return receiveResult{Outcome: receiveLocalBusy, Err: errors.New(reason)}
		}
		h.set(hostState{Phase: phaseSwitching, Reason: "正在开始接收 QQ（先关掉只开本地）"})
	} else {
		h.set(hostState{Phase: phaseSwitching, Reason: "正在开始接收 QQ"})
	}
	if err := h.closeCurrent(); err != nil {
		h.block("关闭后台失败：" + err.Error())
		return receiveResult{Outcome: receiveBlocked, Err: err}
	}
	if err := h.open(backend.Receive, false); err != nil {
		if h.ctx.Err() != nil {
			return receiveResult{Outcome: receiveCancelled}
		}
		if fromLocal {
			h.fallbackLocal("开始接收失败：" + err.Error())
		} else {
			h.fallback("开始接收失败：" + err.Error())
		}
		if h.cur == nil { // 连出发时的阶段也回不去，后台已进 blocked
			return receiveResult{Outcome: receiveBlocked, Err: err}
		}
		return receiveResult{Outcome: receiveOpenFailed, Err: err}
	}
	h.set(hostState{Phase: phaseReceiving, Mode: backend.Receive, Reader: h.deskReader(), Memory: h.desk.Memory, Reason: "接收中"})
	return receiveResult{Outcome: receiveStarted}
}

// startReceiveAuto 是自动接收用的入口：从本地可用出发时先做便宜检查，都过了才关本地、开完整接收。
// 检查不过就留在本地可用，按原来的退避下一轮再查，不来回拆后台。
func (h *host) startReceiveAuto() receiveResult {
	if r, ok := h.autoReceivePrecheck(); !ok {
		return r
	}
	return h.startReceive()
}

// autoReceivePrecheck 在拆后台之前检查：选得出小号、配置里有 URL 和令牌、协议端地址连得上、本地没有回合在跑。
func (h *host) autoReceivePrecheck() (receiveResult, bool) {
	h.op.Lock()
	defer h.op.Unlock()
	if h.ctx.Err() != nil {
		return receiveResult{Outcome: receiveCancelled}, false
	}
	if !h.localOnly || h.state.Phase != phaseLocal {
		return receiveResult{}, true
	}
	if h.account == "" {
		h.set(hostState{Phase: phaseLocal, Mode: backend.Receive, Reader: h.state.Reader, Memory: h.state.Memory, Reason: "不能开始接收：" + h.noRecv})
		return receiveResult{Outcome: receiveNoAccount, Err: errors.New(h.noRecv)}, false
	}
	if n := h.runningTurns(); n > 0 {
		return receiveResult{Outcome: receiveLocalBusy, Err: fmt.Errorf("本地有回合在跑（%d 个），等它结束再开始接收", n)}, false
	}
	if err := backend.CheckReceiveReady(h.ctx, h.opt, h.account); err != nil {
		h.set(hostState{Phase: phaseLocal, Mode: backend.Receive, Reader: h.state.Reader, Memory: h.state.Memory, Reason: "本地可用，等条件齐了再开始接收：" + err.Error()})
		return receiveResult{Outcome: receiveLocalNotReady, Err: err}, false
	}
	return receiveResult{}, true
}

// localStopNote 是本地可用阶段收到“停止接收”时的回答：QQ 本来就没开，不动后台。
const localStopNote = "现在没在接收 QQ（只开了本地）"

// stopReceive 按退出流程收好在途工作，再回 archive；本地可用阶段什么都不做，回一句原因
func (h *host) stopReceive() string {
	h.op.Lock()
	defer h.op.Unlock()
	if h.ctx.Err() != nil || h.mode != backend.Receive || h.cur == nil {
		return ""
	}
	if h.localOnly { // 只开了本地：停止接收没有可停的 QQ 后台，不能把本地一起拆掉
		return localStopNote
	}
	h.set(hostState{Phase: phaseSwitching, Reason: "正在停止接收"})
	if err := h.closeCurrent(); err != nil {
		h.fallback("停止接收时出错：" + err.Error())
		return ""
	}
	h.fallback("已停止接收")
	return ""
}

// shutdown 是唯一的退出收尾：打断进行中的 Open，等当前操作结束，再收后台。返回收尾错误（超时由调用方计）
func (h *host) shutdown() error {
	h.cancel()
	h.op.Lock()
	defer h.op.Unlock()
	h.set(hostState{Phase: phaseStopping, Reason: "正在退出"})
	err := h.closeCurrent()
	if h.bunSup != nil { // 桥已经断了，再收 Bun 子进程（它在 sumika.exe 之外活不下去：作业对象也兜着）
		h.bunSup.stop()
	}
	h.set(hostState{Phase: phaseStopped})
	return err
}

// prepareBun：backend.local 是 bun 时找到 bun 和 server/、建好子进程的监护（bunchild.go），但**不起**子进程：
// 子进程要等 Go 的后台 Open 成功（数据锁拿到了）再起（bunUp），不然 Open 失败、宿主进 blocked 时 Bun 已经在写本地，成了没有宿主的写者。
// 不是 bun 或读不了配置（后面的 Open 会报同样的错）什么都不做；找不到 bun / server/ 回错，调用方进 blocked
func (h *host) prepareBun() error {
	h.localOwner = config.LocalByGo // 读不到配置也是这一个决定：之后后台读到的要是 bun，照这个走并记日志，不会出现“宿主以为 go、后台按 bun 跑”
	location, err := backend.ResourceRoot(h.opt.Root)
	if err != nil {
		return nil
	}
	cfg, err := config.LoadIn(location.Dir, h.opt.DataDir)
	if err != nil || !cfg.Backend.LocalOwnedByBun() {
		return nil
	}
	h.localOwner = config.LocalByBun
	dataDir := h.opt.DataDir
	if dataDir == "" {
		dataDir = cfg.DataDirIn(location.Dir)
	}
	resolve := h.resolveBun
	if resolve == nil {
		resolve = defaultResolveBun(cfg.Tools.Workdir)
	}
	spec, err := resolve(cfg.Backend, location.Dir, dataDir)
	if err != nil {
		return err
	}
	h.stateMu.Lock()
	h.localByBun, h.localOK, h.localLine = true, false, "正在启动 Bun 后台……"
	h.stateMu.Unlock()
	h.bunLink = newBunLink("Bun 后台还没连上")
	sup := newBunSupervisor(spec, h.bunLink, h.logf, h.onBunStatus)
	if h.bunSpawn != nil {
		sup.spawn = h.bunSpawn
	}
	if h.bunTune != nil {
		h.bunTune(sup)
	}
	h.bunSup = sup
	return nil
}

// bunUp：Go 的后台开成功了，拉起（或重新拉起）Bun 的监护。已经在跑、不是 bun 模式、正在退出都什么都不做
func (h *host) bunUp() {
	if h.bunSup == nil || h.ctx.Err() != nil {
		return
	}
	h.bunSup.start()
}

// bunDown：宿主进了 blocked（数据锁拿不到、后台开不起来）就把 Bun 收掉，不留没有宿主的写者；
// 之后后台又开成功了（bunUp）再拉起来。why 写进状态行
func (h *host) bunDown(why string) {
	if h.bunSup == nil {
		return
	}
	if h.bunSup.running() {
		h.bunSup.stop()
		h.logf("本地私聊的后台：已收掉（%s）", why)
	}
	h.stateMu.Lock()
	h.localOK, h.localLine = false, "本地私聊的后台已停："+why
	h.stateMu.Unlock()
}

// block 进 blocked：清掉“只开本地”的标记（后台已经不在了，下一次切换不能凭旧标记回本地），先收掉 Bun 再发布状态
func (h *host) block(reason string) {
	h.localOnly = false
	h.bunDown("后台没在运行")
	h.set(hostState{Phase: phaseBlocked, Reason: reason})
}

// defaultResolveBun 是真的找法：bun 可执行文件和 server/ 目录（bunchild.go）。workdir 是 tools.workdir，非空就传给 Bun 当没绑目录的会话的工作目录
func defaultResolveBun(workdir string) func(config.Backend, string, string) (bunSpec, error) {
	return func(cfg config.Backend, root, dataDir string) (bunSpec, error) {
		isFile := func(p string) bool { st, err := os.Stat(p); return err == nil && !st.IsDir() }
		home, _ := os.UserHomeDir()
		exe, err := resolveBunExe(cfg, exec.LookPath, home, isFile)
		if err != nil {
			return bunSpec{}, err
		}
		dir, err := resolveServerDir(cfg, root, isFile)
		if err != nil {
			return bunSpec{}, err
		}
		// -root：资源根交给 Bun，配置文件的回退位置（<资源根>/sumika.yml）和出厂提示词两边才是同一处
		args := []string{filepath.Join(dir, "src", "main.ts"), "-data", dataDir, "-root", root}
		if workdir != "" {
			args = append(args, "-workdir", workdir)
		}
		return bunSpec{Exe: exe, Args: args, Dir: dir, DataDir: dataDir}, nil
	}
}

// onBunStatus：监护报告 Bun 连上了或不在了。更新状态行并立刻发布（宿主阶段不变，界面拿到的 Reader 也是同一个，不会换数据源）
func (h *host) onBunStatus(ok bool, line string) {
	h.logf("本地私聊的后台：%s", line)
	h.pubMu.Lock()
	defer h.pubMu.Unlock()
	h.stateMu.Lock()
	h.localOK, h.localLine = ok, h.redactor.Redact(line)
	s := h.state
	s.LocalByBun, s.LocalOK, s.LocalLine = true, ok, h.localLine
	h.state = s
	h.stateMu.Unlock()
	if s.Phase != "" { // 宿主还没报过第一个状态（starting）之前不发
		h.publish(s)
	}
}
