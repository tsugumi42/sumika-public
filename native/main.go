// Sumika 桌面入口（规格-界面接Go后台 1.1、第三节）：mygo 原生界面，聊天区使用 MewTypeUI，和 Go 后台在同一个进程里。
// 四栏照 docs/信息架构.md「主窗口：四栏」：图标竖栏 → 会话列表 → 聊天窗口 → 右侧栏。
// 默认只看历史（archive）：直接读 Go 的库，托盘里点"开始接收 QQ"才接收（ADR-209）；后台启动后本地私聊和群聊可发文字、停止，审批仅私聊，桌面外观可编辑。
// -demo 显式选演示数据（mock.go，全是编的），标题写"演示数据"，单实例和数据都和正式的分开。
//
//	go build -ldflags "-H windowsgui" -o sumika.exe .
//	sumika.exe -root <资源根> [-data <数据目录>] [-account <号>]
//	sumika.exe -demo
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"sync"
	"sync/atomic"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/content"
	"github.com/tsugumi42/MewTypeUI/dialogs"

	"sumika-native/internal/api"
	backend "sumika-native/internal/app"
	"sumika-native/internal/chatui"
	"sumika-native/internal/desksettings"
	"sumika-native/internal/loadstate"
	"sumika-native/internal/segview"
	"sumika-native/internal/tools"
	"sumika-native/internal/winshell"
)

type msg struct {
	// 临时行（live_rows.go）：发出去还没进库的话、正在生成的回复
	liveKind      int8
	liveStep      int                    // 回复：第几步
	liveSealed    bool                   // 回复：这一步已存库，等真消息接替
	liveDeadline  time.Time              // 回复：等到什么时候
	liveLoose     bool                   // 发出的话带图或引用：接替时不比文字
	listKey       string                 // 真消息接替临时行后沿用它的列表键
	enterNow      bool                   // 临时行要播进场动画
	noEnter       bool                   // 接替临时行的真消息不再播
	entered       bool                   // 进场动画播过了
	proc          []api.LocalProcessStep // 这句回复之前的思考和工具（后台读的）
	procRow       *msg                   // 它们在消息列里的那一行
	steps         []*chatui.Step         // 过程行的内容
	liveGap       int                    // 过程行：在第几步回复之前
	liveTurn      string                 // 这一行属于哪位成员的这一轮（本地群聊），私聊是空
	claimedBy     *msg                   // 临时行被哪条真消息接替了
	id            string
	service       string // 不空时这行是通知条，不是消息
	serviceIcon   string // 通知条前面的小图标名（宿主写的系统行按来源给：目标、定期任务、命令）；空就不画
	seq           int64  // 平台内的 seq：只用来判断"实时到"，排序看 (at, seq)
	version       uint64 // 后台的行版本：旧页不能把新状态改回去
	mine          bool
	who           string
	owner         bool // 主人发的：名字上强调色（2026-11-02 主人）
	color         string
	text          string
	segs          []seg
	at            int64
	md            bool // 她说的话按 Markdown 显示；群友的话照原文
	streaming     bool
	live          bool // 窗口开着时由订阅第一次看到的（规格 3.6 的"实时到"）；取页来的不是
	recalled      bool
	quote         *quote          // 引用的那条（quote.go）；没引用是 nil
	reacts        []reactionGroup // 被贴的表情，同一个表情合成一个（reaction.go）
	avatar        string          // 发言人的头像 key（avatar.go）；没有 QQ 头像的是空
	chatColor     chatui.OKLCH
	chatName      uint64
	chatNotes     []string
	chatReactions []chatui.Reaction
	chatQuote     *chatui.Quote
	chatParts     []chatui.Part // 分块消息（引用回复加附件）；不空时新界面按块画

	actor       string          // 发送人的 id（分组按它，不按显示名）
	isChar      bool            // 是角色发的
	blocks      []segview.Block // 后台转换时算好；无段也在此时按 text 排版，docOf 只读。
	links       []segview.LinkRef
	source      api.EventRef
	forwardPath []string
	markdown    map[[2]int]*content.MarkdownState // 跟消息而非虚拟行存活，流式替换继续沿用。
	// 状态标记（bubble.go 的 notesOf）
	unsent  string
	aborted bool
	partial bool
	// proactive 不为空：这条是她主动联系发来的（规格-主动联系 第十节）；气泡时间旁标"主动联系"，悬停看她为什么找你
	proactive *api.ProactiveMark
}

type app struct {
	deskSaves  deskSaves        // 写桌面设置：按界面上改的顺序写，退出前等写完
	dropOpen   map[string]*bool // 新界面键值下拉各自的开合（pickDrop）
	win        *mygo.Window
	auto       string
	started    time.Time
	streamSecs float64       // 流式压测：长回复吐完用了几秒
	shown      time.Duration // 启动到第一页聊天记录画出来

	demoMode bool       // 演示发送只留在本窗口内，并开放演示压测
	reader   api.Reader // 当前数据源；后台没起来或在切换时是 nil
	host     hostState  // 宿主状态（生产）
	runtime  api.RuntimeView
	caps     []api.Capability
	moreConv bool // 会话还有没列出来的

	rail           int       // 0 本地 1 QQ 2 记忆 3 运行 4 设置
	pn             paneState // 会话列表和右侧栏的宽度、折叠（panes.go）
	nav            dialogs.RailState
	search         [2]string // 本地和 QQ 各留一份搜索词，切页不清空
	dialogs        [2]conversationList
	dialogRevision uint64
	chars          []character
	convs          []conv // QQ 群和私聊
	local          []conv // 本地私聊（每个角色的日常）
	homes          []conv // 本地群聊
	keys           map[string]api.ConversationKey
	set            *settingsState // 演示的后台设置；外观与正式窗口共用
	real           api.Result[api.Settings]
	realLoad       loadstate.Loader // "正在读配置…"晚点才出现（status.go）
	subLoad        loadstate.Loader // 聊天标题栏小字里的"取数据中…"晚点才出现（scrolltop.go）
	ovLoad         loadstate.Loader // 会话列表的第一份概览：到之前不写"还没有会话"（convlist.go）
	models         modelPageState
	linkSettings   linkPageState
	lifeSettings   lifePageState     // 插件设置页“日历和天气”的所在地
	look           appearanceState   // 桌面主题与编辑状态，不属于后台配置
	location       dataLocationState // 启动时的根目录及重启后才生效的编辑
	realTab        int               // 正式设置页选中的分类
	sel            string            // 选中的会话 id（库里的 conversation，原样）
	gen            uint64            // 代次：每次 open 自增；取页、订阅的结果带着它回来，对不上就丢
	remoteUI       remoteApprovalUI  // 设置页“平台账号”里主人 QQ 私聊审批的开关（settings_remote_approval.go）
	notifyUI       notifyToggleUI    // 设置页“通知”里按类的开关（settings_notify.go）
	embedUI        embedModelUI      // 设置页“记忆和学习”缺嵌入模型时的入口（settings_embed.go）
	secretsUI      secretsPageUI     // 设置页“数据 · 密钥”（settings_secrets.go）
	srcGen         uint64            // 数据源代次：只在换数据源（attach）时自增；概览、设置的结果按它丢，打开会话不动它
	title          string
	group          bool

	msgs      []*msg
	recalled  map[string]bool // 被撤回的消息 id
	speakers  map[string]bool // 在这个会话里说过话的角色 id：两个以上才在右边也写名字
	list      ui.ListState
	rowAt     int
	avatarTop float32
	downloads downloadStore
	visFrame  uint64
	visFirst  int
	visLast   int
	loading   bool
	status    string
	cursor    api.Cursor // 往上翻的游标；空 = 没有更早的
	hasMore   bool
	olderOff  bool      // 自动往上翻失败过：不再自动翻，等主人点按钮（scrolltop.go）
	olderTop  string    // 上一帧列表第一条的 id：变了说明前面加了行，这一帧读到的 Visible 还是旧下标（scrolltop.go）
	topPrev   *msg      // 已加载的最老一条再往前的那条消息（后台给的，只用发送人和时间）：最老那一行靠它判断是不是一组的第一条、是不是新的一天（row.go）
	jump      jumpState // 点引用要找的原消息还没加载时，边往上翻边找（quote.go）
	flashID   string    // 跳过去闪一下的那一行；flashAt 是它第一次构建的时刻
	flashAt   time.Time

	arr       arrivals
	firstDone bool // 这个会话的第一份快照到了：之后的快照按"实时到"并进来
	demoSeq   int64

	input         string
	demo          int
	tab           int
	localRuns     map[string]*localRun
	stepOpen      map[string]*bool            // 过程气泡每一段的展开状态
	composers     map[string]*chatui.Composer // 新界面每个会话一份输入区草稿
	selBar        chatui.SelectionBar         // 新界面消息正文的选区工具条（整个窗口一份）
	pick          chatui.Picker               // 多选（悄悄话会话）：同一时刻只有一个会话在多选，切会话就退出
	stickyAv      chatui.StickyAvatars        // 新界面群聊头像层：钉在可见区域里（整个窗口一份）
	quoteHL       chatui.Highlight            // 点引用回到原消息后要高亮的那一段
	chatMD        *chatui.MD                  // 新界面里她的回复各自的 Markdown 状态（解析和排版缓存），换会话时丢掉
	approvals     []api.LocalApproval
	approvalState map[string]*approvalUI
	approvalErr   string
	questions     []api.LocalQuestion // 她在等主人回答的问题（规格-向主人提问）
	questionState map[string]*questionUI
	questionErr   string
	heldBy        map[string][]api.HeldInput // 会话 → 重启后没处理的话（workbench_held.go）
	localStatus   map[string]api.LocalStatus // 会话 → 目标、定期任务、自动轮的状态（localstatus.go）
	goalOpen      map[string]bool            // 会话 → 输入框上方的目标行是否展开（新界面，workbench_goal.go）
	loopsUI       wbLoopsState               // 设置 → 定期任务页的界面状态（workbench_loops.go）
	rulesUI       approvalRulesUI            // 设置 → 工具与审批的放行规则（workbench_rules.go）
	webUI         webOrderUI                 // 设置 → 工具与审批的联网顺序（workbench_web.go）
	remindUI      wbRemindState              // 设置 → 提醒页的界面状态（workbench_reminders.go）

	mem       memPage         // 记忆页（memorypage.go）
	run       runPage         // 运行页（runpage.go）
	envOpen   map[string]bool // 运行页“状态”里环境项展开没有（status.go），键是清单 ID
	gc        groupCreate
	pastePane pasteView   // 输入框里折叠的粘贴内容的查看框（workbench_paste.go）
	away      atomic.Bool // 主窗口隐藏或最小化着：记忆页的订阅不跑（shell.go 的窗口事件设）

	realDetail bool // 后台配置项：显示键名和来源路径

	// ctx 是进程寿命（退出时取消）；convCtx 是当前会话的订阅，换会话就取消。测试里可以不设
	ctx        context.Context
	cancel     context.CancelFunc
	convCancel context.CancelFunc
	viewCancel context.CancelFunc // 概览订阅（换数据源时取消）
	viewCtx    context.Context    // 当前数据源的寿命：小黄脸、图的后台活跟着它走
	postFn     func(func())
	savePanes  func(desksettings.Panes) // 把分栏的选择写进桌面设置（后台线程写）；设置读出来以后由外壳放进来，没有就不存

	faces     *faceStore
	bodies    bodyStore
	facesOnce sync.Once                    // boot 的后台线程和界面线程都会调 fs()，建一次
	probe     func(name string) *ui.Handle // 只有测试设：见 row.go 的 probeOf

	avatars        *avatarStore // 头像的存储（avatar.go），跟着数据源的寿命
	rm             readMark     // 看了就标已读的状态（readmark.go）
	imgs           *imageStore
	forward        forwardViewState
	imgFetch       func(ctx context.Context, ref api.MediaRef, tier int) ([]byte, error) // 测试里换掉取图的去处；不设走 reader
	chatLightbox   chatLightbox                                                          // 新视图看大图；旧界面的灯箱仍挂在各自行上
	mainView       string                                                                // legacy 或 workbench；空和 legacy 都是旧界面
	winControls    *winshell.WindowControls                                              // 自己画的窗口按钮；Tester 没有真窗口时为 nil
	mainViewLocked bool                                                                  // SUMIKA_MAIN_VIEW 只用于隔离演示，不写桌面设置
	desktop        *desksettings.Store
	workbench      workbenchState
}

func main() {
	os.Exit(runMain())
}

func runMain() int {
	var opt backend.Options
	var demo, rpcListen, inprocRPC, watch bool
	var remote string
	flag.StringVar(&opt.Root, "root", "", "资源根；不给依次读 exe 旁 sumika.root、往上找 sumika.example.yml、用 exe 目录")
	flag.StringVar(&opt.DataDir, "data", "", "数据目录；不给 = 照配置")
	flag.StringVar(&opt.Account, "account", "", "\"开始接收 QQ\"用的小号；不给 = 配置里恰好只有一个时用它")
	flag.BoolVar(&demo, "demo", false, "只看编的演示数据，不碰任何库")
	flag.BoolVar(&rpcListen, "rpc-listen", false, "后台开着时把只读的界面协议开到本机 WebSocket 上，地址和令牌写进 <数据目录>/run/rpc-go.json（run/rpc.json 是 Bun 后台留下的名字，Go 不写它）")
	flag.BoolVar(&inprocRPC, "inproc-rpc", true, "主窗口经进程内协议客户端读写后台（默认）；false 退回直接用后台对象")
	flag.StringVar(&remote, "backend-url", "", "不开本地后台，只当界面连一个协议后台：auto 读 -data 目录的 run/rpc.json；或 ws://127.0.0.1:<端口>/rpc（令牌放环境变量 SUMIKA_RPC_TOKEN）")
	flag.BoolVar(&watch, "watch", false, "守护进程模式：不开窗口，拉起一个不带 -watch 的自己当桌面程序并盯着它，异常退出留记录、弹通知、重新拉起（开机自启用这个）")
	flag.Parse()
	watchedBy = os.Getenv(watchedByEnv)
	os.Unsetenv(watchedByEnv) // 桌面程序自己再起的子进程不必带着它
	if watch {
		if demo || remote != "" {
			fmt.Fprintln(os.Stderr, "-watch 不能和 -demo、-backend-url 一起用：演示和远端界面没有要守护的数据目录")
			return 2
		}
		return runWatchdog(opt, watchChildArgs(os.Args[1:]))
	}
	crashLogEnabled = !demo                  // 正式运行才把崩溃信息写进 logs/crash.log，见 crashlog.go
	heartbeatEnabled = !demo && remote == "" // 正式运行才写心跳，见 watch.go
	if demo && remote != "" {
		fmt.Fprintln(os.Stderr, "-demo 和 -backend-url 只能选一个")
		return 2
	}

	// 单实例按应用名认（规格 3.1）：演示和正式分开，不按 QQ 账号认
	name := "Sumika"
	if demo {
		name = "Sumika Demo"
	} else if remote != "" {
		name = "Sumika Remote" // 远端界面和正在跑的 sumika.exe 各是各的实例
	}
	var err error
	name, err = automationName(name, demo)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	mygo.App.SetName(name)
	if !mygo.App.RequestSingleInstanceLock() {
		return 0 // 已经开着的那个会把主窗口叫出来（OnSecondInstance）
	}

	ctx, cancel := context.WithCancel(context.Background())
	a := &app{ctx: ctx, cancel: cancel, auto: os.Getenv("SUMIKA_DEMO"), started: time.Now(), demoMode: demo, recalled: map[string]bool{}, speakers: map[string]bool{}, keys: map[string]api.ConversationKey{}}
	a.applyMainViewEnv()
	if os.Getenv("SUMIKA_TAB") == "stats" {
		a.tab = 2
	}
	// 解码原图留下的空闲内存 Go 不急着还；软上限让它早点回收，空下来时每 20 秒还一次（2026-10-23 量的）
	debug.SetMemoryLimit(512 << 20)
	go func() {
		t := time.NewTicker(20 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				debug.FreeOSMemory()
			case <-ctx.Done():
				return
			}
		}
	}()
	sh := newShell(a, opt, demo)
	sh.remote, sh.rpcListen, sh.inprocRPC = remote, rpcListen, inprocRPC
	if !demo && remote == "" {
		sh.useWatchedAutostart() // 开机自启启动守护进程（watch.go）
	}
	automationShell(sh)
	defer sh.closeLog()
	mygo.App.OnSecondInstance(func(args []string, wd string) { sh.secondInstance(args, wd) })
	mygo.App.OnNotificationClick(sh.notificationClicked)
	mygo.App.OnWindowAllClosed(func() {}) // 窗口平时只隐藏；真没有窗口了也由退出流程决定
	mygo.App.OnBeforeQuit(sh.beforeQuit)
	mygo.App.OnWillQuit(func(*mygo.QuitEvent) { sh.destroyTray() })
	mygo.App.WhenReady(sh.ready)
	// notify 工具的系统通知出口：mygo 的桌面通知（Show 会自己回到主线程）
	tools.SetNotifier(func(title, body string) error {
		return mygo.NewNotification(mygo.NotificationOptions{Title: title, Body: body, Image: windowIconPNG}).Show()
	})
	if err := mygo.App.Run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return sh.exitCode
}

// ───────────── 数据源 ─────────────

// attach 换数据源（界面线程）：旧的订阅全部取消，界面清回空状态，再订阅概览
func (a *app) attach(r api.Reader) {
	if a.reader == r {
		return
	}
	if a.viewCancel != nil {
		a.viewCancel()
		a.viewCancel = nil
	}
	if a.convCancel != nil {
		a.convCancel()
		a.convCancel = nil
	}
	a.stopForward()
	a.downloads.reset()
	if a.gc.cancel != nil {
		a.gc.cancel()
	}
	a.gc = groupCreate{generation: a.gc.generation + 1}
	a.models = modelPageState{}
	a.lifeSettings.pick.Close()
	a.linkSettings = linkPageState{}
	a.lifeSettings = lifePageState{}
	a.remindUI.opts, a.remindUI.optsAt, a.remindUI.optsLoading = api.ReminderOptions{}, time.Time{}, false // 提醒开没开换了后台重问
	a.reader = r
	a.localRuns, a.composers, a.approvals, a.approvalState, a.approvalErr, a.localStatus, a.heldBy = nil, nil, nil, nil, "", nil, nil
	a.questions, a.questionState, a.questionErr = nil, nil, ""
	a.sel, a.msgs, a.chars, a.convs, a.local, a.homes = "", nil, nil, nil, nil, nil
	a.dialogRevision++
	a.dialogs = [2]conversationList{}
	a.keys = map[string]api.ConversationKey{}
	a.gen++
	a.srcGen++
	a.imgs, a.avatars = nil, nil // 图和头像的缓存绑定数据组寿命：换了后台不复用旧结果（规格 5.2）
	// 新数据源重新问一遍肯不肯标已读
	a.rm.off, a.rm.at, a.rm.busy = false, 0, false
	a.faces, a.facesOnce = nil, sync.Once{}
	a.ovLoad = loadstate.Loader{} // 新数据源：会话列表从头算"还没到"
	if r == nil {
		return
	}
	a.ovLoad.Begin()
	ctx, cancel := context.WithCancel(a.base())
	a.viewCancel, a.viewCtx = cancel, ctx
	gen := a.srcGen
	a.fs().start()
	go a.watchOverview(ctx, r, gen)
	go a.loadSettings(ctx, r, gen)
	if writer, ok := r.(api.LocalChat); ok {
		go a.watchLocalApprovals(ctx, writer, gen)
	}
	if qr, ok := r.(api.LocalQuestions); ok {
		go a.watchLocalQuestions(ctx, qr, gen)
	}
	if sr, ok := r.(api.LocalStatusReader); ok {
		go a.watchLocalStatus(ctx, sr, gen)
	}
	a.refreshHeld()
}

func (a *app) base() context.Context {
	if a.ctx == nil {
		return context.Background()
	}
	return a.ctx
}

// watchOverview 概览订阅：角色、会话列表、未读、宿主状态。一个快照整份换掉
func (a *app) watchOverview(ctx context.Context, r api.Reader, gen uint64) {
	sub, err := r.WatchOverview(ctx, api.OverviewRequest{Limit: 1000})
	if err != nil {
		a.post(func() {
			if a.srcGen == gen {
				a.status = "取会话列表失败：" + err.Error()
				a.ovLoad.Fail()
			}
		})
		return
	}
	defer sub.Close()
	for snap := range sub.Updates() {
		v := snap.Result
		a.post(func() { a.applyOverview(gen, v) })
	}
}

func (a *app) applyOverview(gen uint64, v api.Result[api.Overview]) {
	if a.srcGen != gen {
		return
	}
	if v.Err != nil {
		a.status = "会话列表：" + v.Err.Error()
		if v.Stale {
			a.status += "（显示的是上一次的）"
		}
		if !v.Stale {
			a.ovLoad.Fail()
			return
		}
	}
	a.ovLoad.Succeed()
	o := v.Value
	a.chars = a.chars[:0]
	for _, c := range o.Characters.Items {
		a.chars = append(a.chars, characterFromResult(c))
	}
	a.convs, a.local, a.homes = nil, nil, nil
	for _, cv := range o.Conversations.Items {
		c := convFromView(cv)
		if c.Title == "" {
			c.Title = a.titleOf(c)
		}
		a.keys[cv.Key.Conversation] = cv.Key
		switch c.Kind {
		case "local":
			a.local = append(a.local, c)
		case "home":
			a.homes = append(a.homes, c)
		default:
			a.convs = append(a.convs, c)
		}
	}
	a.dialogRevision++
	a.moreConv = o.Conversations.HasMore
	a.caps, a.runtime = o.Capabilities, o.Runtime
	if a.demoMode && a.set == nil {
		a.set = newSettings(a.chars)
		if v := os.Getenv("SUMIKA_SETTINGS"); v != "" {
			fmt.Sscan(v, &a.set.tab)
			if a.set.tab < 0 || a.set.tab >= len(settingsTabs) { // 页数变过：越界的旧编号回到第一页，不让演示一打开就崩
				a.set.tab = 0
			}
			a.rail = 4
		}
	}
	if a.sel == "" && len(a.local) > 0 {
		a.open(a.local[0].Key.Conversation)
		a.autoDemo()
	}
}

// titleOf 没有标题的会话：本地日常用角色名，其余用会话身份
func (a *app) titleOf(c conv) string {
	if id := charOfConv(c.Key); id != "" {
		if ch, ok := a.charOf(id); ok && ch.Name != "" {
			return ch.Name
		}
		return id
	}
	return c.Key.Conversation
}

func (a *app) loadSettings(ctx context.Context, r api.Reader, gen uint64) {
	v, _ := r.ReadSettings(ctx)
	a.post(func() {
		if a.srcGen == gen {
			a.real = v
		}
	})
}

// post 把 fn 交回界面线程。进程退出了就丢掉；没有窗口也没换去处（测试里）也丢掉
func (a *app) post(fn func()) {
	if a.ctx != nil && a.ctx.Err() != nil {
		return
	}
	wrapped := func() {
		if a.ctx == nil || a.ctx.Err() == nil {
			fn()
		}
	}
	switch {
	case a.postFn != nil:
		a.postFn(wrapped)
	case a.win != nil:
		a.win.Update(wrapped)
	}
}

// fs 小黄脸的存储，第一次用时才建（测试里直接建 app 也能用）
func (a *app) fs() *faceStore {
	a.facesOnce.Do(func() {
		if a.faces != nil { // 测试里预先放好的
			return
		}
		ctx := a.viewCtx
		if ctx == nil {
			ctx = a.base()
		}
		a.faces = newFaceStore(ctx, a.post)
		r := a.reader
		a.faces.fetchTable = func(ctx context.Context) (map[string]faceInfo, error) { return readFaceTable(ctx, r) }
		a.faces.fetchPNG = func(ctx context.Context, id string) ([]byte, error) { return readFacePNG(ctx, r, id) }
	})
	return a.faces
}

func (a *app) charOf(id string) (character, bool) {
	for _, c := range a.chars {
		if c.ID == id {
			return c, true
		}
	}
	return character{}, false
}

// stats 右侧栏"原型数据"页：Go 进程自己的内存和缓存
func (a *app) stats(c *ui.Context) {
	t := c.Theme()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	src := "Go 后台（同一进程）"
	if a.demoMode {
		src = "演示数据"
	}
	rows := [][2]string{
		{"数据来源", src},
		{"启动到第一页画出来", fmt.Sprintf("%d ms", a.shown.Milliseconds())},
		{"这个会话已加载", fmt.Sprintf("%d 条", len(a.msgs))},
		{"Go 堆内存", fmt.Sprintf("%.1f MB", float64(ms.HeapAlloc)/(1<<20))},
		{"向系统要的内存", fmt.Sprintf("%.1f MB", float64(ms.Sys)/(1<<20))},
		{"图片缓存", fmt.Sprintf("%d 张，%.1f MB", a.is().count(), float64(a.is().bytes)/(1<<20))},
	}
	for _, r := range rows {
		ui.Row(c).Gap(8).Children(func() {
			ui.Text(c, r[0]).TextColor(t.TextMuted).Width(130)
			ui.Text(c, r[1]).Selectable().Grow(1).Wrap()
		})
	}
	ui.Text(c, "整个程序的内存要在任务管理器里看；这里的只是 Go 自己的。").TextColor(t.TextMuted).FontSize(t.FontSize * 0.85).Wrap()
	c.After(time.Second)
}

// newList 聊天列表，照 mygo 文档 docs/ui/markdown.md「A chat」：跟随最新一条，往上翻了就不跟。
// 跟随方式：默认每帧直接贴底（主人 2026-10-24 更喜欢这种）；SUMIKA_FOLLOW=glide 是文档推荐的"滑上来"
func (a *app) newList() ui.ListState {
	s := ui.ListState{FollowEnd: true}
	if os.Getenv("SUMIKA_FOLLOW") == "glide" {
		s.FollowAnimation = &ui.Smooth
	}
	return s
}
