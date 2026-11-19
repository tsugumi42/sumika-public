// Package app 管桌面端和命令行共用的 archive / receive 后台。
// archive 只读已有 Go 历史；receive 先拿同一把数据锁，
// 再装配现有的单账号 QQ 后台。
package app

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sumika-native/internal/applog"
	"sync"
	"sync/atomic"
	"time"

	"sumika-native/internal/alert"
	"sumika-native/internal/approval"
	"sumika-native/internal/artists"
	"sumika-native/internal/askowner"
	"sumika-native/internal/assetsvc"
	"sumika-native/internal/backup"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/sendlock"
	"sumika-native/internal/channel/snowluma"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/checkpoint"
	"sumika-native/internal/config"
	"sumika-native/internal/datalock"
	"sumika-native/internal/dbguard"
	"sumika-native/internal/ecosystem"
	"sumika-native/internal/embed"
	"sumika-native/internal/forwardfetch"
	"sumika-native/internal/goal"
	"sumika-native/internal/kernel"
	"sumika-native/internal/lifetools"
	"sumika-native/internal/lineend"
	"sumika-native/internal/linkcard"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkplugin"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktranslate"
	"sumika-native/internal/llm"
	"sumika-native/internal/localgroup"
	"sumika-native/internal/loop"
	"sumika-native/internal/mcpclient"
	"sumika-native/internal/mediafetch"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/memory"
	"sumika-native/internal/memplugin"
	"sumika-native/internal/memwrite"
	"sumika-native/internal/modelmeta"
	"sumika-native/internal/paths"
	"sumika-native/internal/pdftext"
	"sumika-native/internal/persona"
	"sumika-native/internal/policy"
	"sumika-native/internal/proactive"
	"sumika-native/internal/projectcontext"
	"sumika-native/internal/prompts"
	"sumika-native/internal/remind"
	"sumika-native/internal/runstats"
	"sumika-native/internal/secrets"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
	"sumika-native/internal/store/memsource"
	"sumika-native/internal/taskrun"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
	"sumika-native/internal/whisper"
)

const (
	// shutdownLimit 是整个关机的上限（规格第六节）：停适配器、补录这些也算在内；工具宽限和排空另外共用 drainTimeoutMs。
	shutdownLimit = 30 * time.Second
	// responderIdle 是等在途回复返回的上限（规格第六节第 3 步）；超时记日志往下走
	responderIdle = 10 * time.Second
)

// Options 选择后台模式，以及资源根和数据根。
type Options struct {
	Root    string // 资源根；空 = exe 所在目录，开发时显式指定
	DataDir string // 数据目录；空 = 照配置
	Mode    Mode   // 空 = archive；CLI 显式 receive
	Account string // receive 必填，要连的 QQ 小号；LocalOnly 时必须为空
	// LocalOnly：receive 只开本地（本地私聊、本地群聊、记忆），不装 QQ 单元。和 Account 互斥，只能和 receive 一起用。
	// 临时：P2-1 过渡，P5-7 换成平台监督器。
	LocalOnly bool
	// BackupTempRoots 只给测试：备份把哪些位置当“临时目录”（数据目录在里面就一律把备份放旁边）。nil = 系统临时目录；空切片 = 没有
	BackupTempRoots []string
	// NewMemoryDB 只供测试注入建库失败：nil = memory.CreateIfEmpty。
	// Report.Action 为 create 表示“这次是建库失败”（不拦启动）；别的错误照旧整体启动失败。
	NewMemoryDB func(ctx context.Context, data, file, snapshotDir, relName string) (bool, dbguard.Report, error)
	Logf        func(string, ...any) // nil = log.Printf
	// Logs：设了就按模块写进 applog（规格-日志分级）；比 Logf 优先。桌面和 sumika-go 都用它
	Logs *applog.Root
	// LLM：测试换成假的（假 Magpie 也可以直接给一个指过去的 llm.Client）；nil = llm.New(llm.Magpie(cfg.Magpie.BaseURL))
	LLM *llm.Client
	// DetectShell：查本机命令行（拼"怎么跑命令"的说明用）；nil = turn.DetectShell。测试可换成固定结果
	DetectShell func(context.Context) *turn.Shell
	// Now 是时钟；nil = time.Now
	Now func() time.Time
	// BackfillSettle 是 QQ 连上以后等多久才开始第一轮补录（等重连抖动过去）；0 是默认 30 秒，测试才改
	BackfillSettle time.Duration
	// LinkEchoWait 非 0 时代替配置里链接展开等回声的 echoWaitMS（规格-链接发送改版 A1；配置下限 500 毫秒），测试才改
	LinkEchoWait      time.Duration
	LinkVerifyTimeout time.Duration
	// LinkTranslateCall 替换链接翻译的模型调用，测试才用；nil 走后台模型
	LinkTranslateCall linktranslate.Caller
	// LinkTranslateGenerate 替换已选定模型之后的那一次生成调用，测试才用（看选了哪个模型）；nil 走 memoryGenerator
	LinkTranslateGenerate func(ctx context.Context, model, system, user string) (string, error)
	// 只给测试替换探测用；正式运行探测 7720 和 cfg.Console.Port。
	// ProbePorts 非 nil 时替换探测端点；给空切片要报参数错误。
	ProbePorts  []int
	DialContext DialContextFunc
	// NewEmbed 只供测试替换本地子进程；nil 用 embed.NewLocal，archive 不调用。
	NewEmbed func(embed.LocalConfig, func(string, ...any)) LocalEmbedding
	// SharedAnalyzer 只供测试共用一个常驻 pwsh：不设时每开一个 App 都起一个真 pwsh 并在关闭时等它退出，一条测试因此多花几秒；
	// 设了就用它建审批门，App 关闭时不关它（调用方管）。生产不设
	SharedAnalyzer *policy.Analyzer
	// OnGate 只供测试：审批门建好以后马上交给它（关机测试要拿这个 App 自己那份解析器的进程号，按名字或"新出现的 pwsh"认会认到别的测试的）。生产不设
	OnGate func(*approval.Gate)
	// OutletPace 只供测试缩短出口的重试间隔、停顿（零值走出口默认）：真钟下一次明确失败要隔 1 秒才重试，一条测试的失败计划能因此多等好几秒；生产不设
	OutletPace channel.Pace
	// LinkFetcher 只供测试替换链接取数（不联网）；nil 用 linkreader.NewFetcher
	LinkFetcher linkreader.ContentFetcher
	// LifeClient、LifeGeocodeURL、LifeForecastURL 只供测试：把日历的假日数据和天气的联网引到假服务器；nil 用真的。
	// 测试里应当总给一个会报错的客户端，免得测试悄悄联网
	LifeClient                      *http.Client
	LifeGeocodeURL, LifeForecastURL string
	// MediaDownloader 只供测试替换收图下载的网络部分（把连接引到假服务器）；nil 用默认的（只认 QQ 图片域名）
	MediaDownloader *mediafetch.Downloader
	// ExtraChannels 只供测试：在 QQ 之外再装几个渠道（规格-多平台装配第 3 步用假渠道验收）；nil 就只有 QQ
	ExtraChannels []ChannelSpec
	// Attention：需要主人看的事（attention.go）。桌面宿主接上，按主窗口在不在前台弹系统通知；nil 就不提醒。
	// 在后台线程调，回调里别等界面
	Attention func(Attention)
	// EnvChanged 在配置保存、密钥库变化之后调一次：桌面宿主据此重算环境快照。在调用方线程调，要快、别等界面
	EnvChanged func()
	// WindowForeground 报告本地主窗口此刻是否在前台（显示着、没最小化、有键盘焦点）。主动联系的导演用来判断主人是不是正在看；nil 当"不在前台"。
	// 会在后台线程调，要快、别等界面
	WindowForeground func() bool
	// RemoteApprovalTick 是主人 QQ 私聊审批的巡检间隔（remoteapproval.go）；0 用默认 5 秒。测试调小，才能在很短的时间里断言“没发”。
	RemoteApprovalTick time.Duration
	// QQCallTimeout 是 QQ 发送动作等协议端回包的上限；0 用适配器默认的 10 秒。测试调小，“不回包”的用例才不用等真的 10 秒。
	QQCallTimeout time.Duration
	// QQQueryGap 是 QQ 两次只读查询（历史翻页、核实）之间最少隔多久；0 用适配器默认的 2 秒。只供测试：假钟不前进时每次查询都真等满 2 秒；生产不设。
	QQQueryGap time.Duration
	// ModelRetry 是模型请求出错后重试的节奏；零值用默认（1、2、4、8 秒）。测试调短，出错的用例才不用陪真实的十几秒退避。
	ModelRetry turn.RetryPolicy
	// CatalogRetry 是模型目录取不到时后台重试的节奏（modelcatalog.go）；零值用 30 秒、1 分、2 分、5 分。测试换成自己的等待，不真的等
	CatalogRetry CatalogRetry
	// ExternalPollEvery 是 backend.local 为 bun 时查 local.db data_version 的间隔；0 = 一秒（store.ExternalPollInterval）。测试才改
	ExternalPollEvery time.Duration
	// LocalOwner 是本地私聊交给谁（"go" 或 "bun"）：桌面宿主启动时读一次配置做的决定，之后每次 Open 都照它走，不再重读 backend.local
	// （宿主的 Bun 子进程是按那次决定拉起的；中途改了 sumika.yml，两边各读一次会对不上）。
	// 空 = 这次 Open 照配置文件（命令行后台、测试等一次 Open 就读一次的入口）。和配置文件现在的值不一样时照宿主的，记一行日志，重启 sumika.exe 才生效
	LocalOwner string
}

// ChannelSpec 是往装配里多加的一个渠道：渠道名、平台（库文件名）、连的号和绑的角色、适配器、准入和分流。
// 三个函数都要给，缺了 Open 报错（不替它补一个放行的默认）；适配器自报的名字要和 Channel 一样。
// 适配器的 Run 一返回（不管有没有错），整个后台就照顺序关机，QQ 也停
type ChannelSpec struct {
	Channel, Platform, Account, Character string
	Adapter                               types.Adapter
	Admit                                 func(types.Inbound) channel.Admission
	AdmitNotice                           func(types.Notice) channel.Admission
	Dispatch                              func(channel.Dispatch)
}

// App 持有自己的资源，直到 Run 返回或 Close 完成。
type App struct {
	opt          Options
	cfg          config.Config
	root         string
	rootLocation RootLocation
	paths        paths.Paths
	now          func() time.Time
	logf         func(string, ...any)
	selfcheck    selfcheckState  // 运行自检：计数器、只读连接、评估循环（selfcheck.go）
	envInputs    envInputsState  // 环境自检运行部分的只读连接（envinputs.go），和自检开关无关
	backup       *backup.Service // 备份：每天自动备份、手动备份（backup.go）
	backupWarned sync.Map        // 备份目录有问题的 Warn 已经记过的原因（同一原因只记一次，backup.go）
	tokens       []string        // 日志出口要抹掉的令牌（定了就不再改，不用锁）
	people       memsource.QQPeople

	account   string // 要连的小号
	character string // 这个号绑的角色

	llm          *llm.Client // magpie 的客户端（现在唯一有目录的服务商，见 catalogProvider）；发请求走 router
	router       *llm.Router
	modelRuntime atomic.Pointer[ModelRuntime]
	modelMu      sync.Mutex       // 保存、前提转换和发布按同一顺序完成
	modelDev     *modelmeta.Store // models.dev 的本地缓存（模型窗口和单次输出的第三层，ADR-334）
	// 模型目录取不到之后的后台重试（modelcatalog.go）：catalogCancel 非空就是在跑
	catalogMu     sync.Mutex
	catalogCancel context.CancelFunc
	catalogDone   chan struct{}
	linkRuntime   atomic.Pointer[LinkRuntime]
	linkMu        sync.Mutex
	embedding     LocalEmbedding
	embedAlert    atomic.Bool // 从首次 off / down 到 ready 只提醒一次，重试中的 starting 不清除。

	alerts    *alert.Hub
	degrade   *taskrun.Degrader // 退化、不支持、失败的统一记录（日志加提醒加 task_runs，规格-运行页 第七节）；库打开后才有
	alertSub  *alert.Sub
	alertDone chan struct{}

	guardRoutes guardRoutes // 各角色守卫上次走的路线，走退路时提醒（guardroute.go）

	approvalGate *approval.Gate
	approvals    *approval.Approvals
	// 主人 QQ 私聊审批（remoteapproval.go）：默认关，投影一直建着，每个入口现读配置
	remote    remoteState
	remoteOut *channel.AutomationOutlet
	asks      *askowner.Center // 她问主人、等回答的问题；回答不是批准，所以不和 approvals 共用（规格-向主人提问）

	// modeOf 是 QQ 渠道的有效档位（链、闸门、出口、回合共用这一个），openChannel 里定好
	modeOf func(types.Conversation) string

	set             *store.Set
	hub             *turn.Hub
	kernel          *trackedKernel
	ecosystem       *Ecosystem
	mcpServers      map[string]mcpclient.ServerConfig // mcp.json 里启用的服务；解除隔离后按它补连
	checkpoints     *checkpoint.Service
	pdf             *pdftext.Engine
	authority       *ecosystem.Authority
	authorityMu     sync.Mutex
	authorityIssued map[string]kernel.Scope
	ecosystemCaps   []string
	groups          *localgroup.Service
	whisper         *whisper.Service // 悄悄话（规格-群聊引用和路由）；没有本地库时是 nil
	hubCancel       context.CancelFunc
	memories        map[string]*memory.Memory
	memoryIDs       []string
	ownerMemory     *memory.Memory
	memMu           sync.Mutex // 管上面两个的写入，和嵌入就绪时对它们的遍历（回调在嵌入看管的协程上，会和 openMemories 同时跑）；打开完以后只读，读的人不用锁
	roster          channel.Roster
	channels        []*channelUnit    // 装好的渠道，QQ 是第一项（规格-多平台装配第 3 步）
	adapter         *snowluma.Adapter // QQ 那一项的适配器：发送门要看它就绪没有
	chain           *channel.Chain
	outlet          *channel.Outlet
	linkTransOnce   sync.Once            // 链接翻译的缓存第一次用到时才建（linkexpand.go）
	linkTransCache  *linktranslate.Cache // 干净链接 + 目标语言 + 原文哈希 → 译文，内存 LRU
	mediaBlocks     mediaBlocks          // 链接展开记下的明确失败组合，见 media_block.go（ADR-310）
	hardQQMode      func(types.Conversation) string
	linkMedia       *linkexpand.MediaManager
	linkCards       *linkcard.Renderer
	responder       *channel.Responder
	gate            *channel.Gate
	pipeline        *channel.Pipeline
	intake          *channel.Intake
	media           *mediafetch.Fetcher // 收图下载；qq.media.download 关着是 nil
	backfill        *qqBackfill         // 只在 receive 且 qq.backfill.enabled 时构造
	forwards        *forwardfetch.Fetcher
	imageWait       time.Duration       // 组请求前等图的上限，默认 imageWait；测试里调小
	links           *linkplugin.Plugin  // 链接解析的观察者和内核插件；给她看关着是 nil
	life            *lifetools.Plugin   // 日历、天气、汇率工具和免费接口引擎；设置页、设置工具查地点候选也用它
	linkSvc         *linkreader.Service // 链接取数和缓存，关库之前关
	artistsOnce     sync.Once           // 画师表（规格-画师原图）懒建一份：ctl 命令和链接插件共用
	artistsBook     *artists.Book
	closing         atomic.Bool // 关机进入收尾：提醒不再写库
	shutDowned      atomic.Bool
	drainFailed     atomic.Bool
	shutdownMu      sync.Mutex    // Run 超时后仍可能有收尾，重试 Close 不能并发关同一组资源
	responderWait   time.Duration // 测试缩短等待；零值用 responderIdle。
	shutdownWait    time.Duration // 测试再缩短整体等待；零值用 shutdownLimit。
	processClose    sync.Once     // 正常收尾和整体超时出口都要等子进程退出。

	// sendLock 是接过来的 QQ 发送权；句柄关闭后立即重新钳制，接不过来则照常收包。
	sendLock     *sendlock.Holder
	sendResultMu sync.Mutex // 串行不同会话的明确结果，提醒与通道门按同一顺序处理
	dataLock     *datalock.DataLock
	localOwner   string             // 本地私聊交给谁（go / bun）：Open 时定下，之后不变（local_owner.go 的 pickLocalOwner）
	localLock    *datalock.DataLock // go/local.lock：本地私聊的写权。backend.local 是 bun 时不拿（Bun 拿），是 nil
	stopExt      func()             // 停 local.db 的 data_version 轮询（local_owner.go）

	projectTrust    projectcontext.Trust // 项目说明的信任；没开项目说明是 nil（客户端自知要据此核验工作目录）
	projectTrustDyn *projectTrust        // 同一个对象的具体类型：问一次、记住
	desktopOnce     sync.Once            // 桌面只读接口（desktop.go），用到才建
	desktop         *Desktop
	typingMu        sync.Mutex
	typing          map[string]time.Time    // 对方正在输入：会话 → 显示到什么时候
	desktopPtr      atomic.Pointer[Desktop] // 同 desktop，给别的协程安全地读（状态订阅唤醒用）

	slashes      *slashTable // 本地私聊的斜杠命令表（slash.go）；archive 没有
	pendingNotes pendingNotices
	goals        *goal.Manager       // /goal（goalwire.go）
	loops        *loop.Scheduler     // /loop（loopwire.go）
	loopRounds   sync.Map            // 任务号 → loopRound，一轮的待分类行在哪
	proactive    *proactive.Director // 主动联系的导演（proactivewire.go）；没打开就是 nil
	pthreads     *proactiveHost      // 主动联系的话题线宿主（proactivethread.go）；没打开就是 nil
	reminders    *remind.Scheduler   // 提醒（remindwire.go）；archive 没有，提醒表读不出来也是 nil
	rhost        *reminderHost       // 提醒的宿主：到点怎么说、发到哪
	auto         autoRuns            // 宿主自动发起的活动，停止按钮据此取消
	attnMu       sync.Mutex          // 保护 attnLast（attention.go）
	attnLast     map[string]time.Time
	autoCtx      context.Context    // 自动活动的根，关机最先取消
	autoCancel   context.CancelFunc //
	autoWG       sync.WaitGroup
}

// Open 先拿数据锁、检查导入状态，再打开业务库。
// archive 不建库、不读令牌、不查询模型。
// 失败时（包括取消）释放所有已取得的资源。
func Open(ctx context.Context, opt Options) (*App, error) {
	mode, err := ParseMode(string(opt.Mode))
	if err != nil {
		return nil, err
	}
	opt.Mode = mode
	if opt.LocalOnly && mode != Receive {
		return nil, fmt.Errorf("%w：只开本地（-local-only）只能和 receive 一起用", ErrArguments)
	}
	if mode == Receive {
		if opt.LocalOnly && strings.TrimSpace(opt.Account) != "" {
			return nil, fmt.Errorf("%w：只开本地（-local-only）不能同时指定 -account", ErrArguments)
		}
		if !opt.LocalOnly && strings.TrimSpace(opt.Account) == "" {
			return nil, fmt.Errorf("%w：receive 必须指定 -account（不要 QQ 就用 -local-only）", ErrArguments)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	location, err := ResourceRoot(opt.Root)
	if err != nil {
		return nil, err
	}
	root := location.Dir
	tools.UseVendorBin(root) // grep、find 要用的 rg、fd 在 <资源根>/vendor/bin
	cfg, err := config.LoadIn(root, opt.DataDir)
	if err != nil {
		return nil, fmt.Errorf("读配置：%w", err)
	}
	ApplyLogSettings(opt.Logs, cfg.Logs)
	dataDir := opt.DataDir
	if dataDir == "" {
		dataDir = cfg.DataDirIn(root)
	}
	var p paths.Paths
	var initialized bool
	if mode == Archive {
		p, err = paths.Open(dataDir)
	} else {
		p, initialized, err = openDataDir(dataDir)
	}
	if err != nil {
		return nil, err
	}
	if initialized && opt.Logs != nil {
		opt.Logs.Logf(applog.App)("新建了数据目录 %s（写了身份证）", p.Root)
	}
	owner, err := pickLocalOwner(opt.LocalOwner, cfg.Backend)
	if err != nil {
		return nil, err
	}
	a := &App{opt: opt, cfg: cfg, root: root, rootLocation: location, paths: p, now: time.Now, imageWait: imageWait, localOwner: owner}
	if opt.Now != nil {
		a.now = opt.Now
	}
	if err := a.initModelRuntime(nil); err != nil {
		return nil, err
	}
	a.initLinkRuntime()
	a.logf = a.newLogf()
	a.warnRemovedConfigKeys()
	a.warnConfigNotices()
	a.selfcheck.counters = selfcheck.NewCounters() // 先于渠道建好：入库一站、适配器往里数
	if opt.LocalOwner != "" && opt.LocalOwner != cfg.Backend.Local {
		a.logf("本地私聊：sumika.yml 的 backend.local 现在是 %s，和启动时的决定（%s）不一样；这次照启动时的走，重启 sumika.exe 才生效", cfg.Backend.Local, opt.LocalOwner)
	}

	failed := func(err error) (*App, error) {
		a.cleanup()
		return nil, err
	}
	// 进程锁在第一份库打开之前取得，直到全部库关完才释放。
	a.dataLock, err = datalock.LockData(p.Root)
	if err != nil {
		return failed(err)
	}
	// 本地私聊的写锁：go 模式、能写的 receive 阶段 Go 拿（防止有人手动起了 Bun 去写本地）；bun 模式 Bun 拿，这里不拿。
	// archive 只读、不写本地，不拿也不建锁文件（读路径不新建文件的测试守着）；之后切到 receive 时再拿，被占就回 archive 并写明原因
	if mode == Receive && !a.localByBun() {
		if a.localLock, err = datalock.LockLocal(p.Root); err != nil {
			return failed(err)
		}
	}
	// 播放副本属于桌面临时产物；archive 也清旧副本，不写聊天库。
	if _, err := mediastore.CleanVideoTmp(p.Root, "qq", a.now().Add(-24*time.Hour)); err != nil {
		a.logf("视频：清理旧播放副本失败：%v", err)
	}
	if err := a.checkLegacyListeners(ctx); err != nil {
		return failed(err)
	}
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	if mode == Archive {
		a.set, err = store.OpenGoReadOnlyOpts(ctx, p, store.DefaultRegistry, store.OpenOptions{Logf: a.logfFor(applog.Store)})
		if err != nil {
			return failed(fmt.Errorf("打开已有 Go 历史（archive 不建库）：%w", err))
		}
		if len(a.set.Platforms()) == 0 {
			return failed(storeUnavailableAllError(a.set))
		}
		a.watchExternalLocal()
		a.openBackup() // archive 也能手动备份；调度只在接收模式里开
		if err := ctx.Err(); err != nil {
			return failed(err)
		}
		return a, nil
	}

	// 接收模式的通知出口先建好：账号判断失败、发送权取不到、自检都要用它（archive 保持 nil，startup_test 钉着）
	a.alerts = alert.New()
	if !opt.LocalOnly {
		if err := a.prepareQQ(); err != nil {
			return failed(err)
		}
	}

	// 打开哪些平台的库从登记表来：默认只有本地和 QQ，多装的渠道把自己的平台加进去
	reg := store.DefaultRegistry
	if len(opt.ExtraChannels) > 0 {
		ps := reg.Platforms()
		for _, c := range opt.ExtraChannels {
			ps = append(ps, store.Platform{Channel: c.Channel, Name: c.Platform})
		}
		if reg, err = store.NewRegistry(ps...); err != nil {
			return failed(err)
		}
	}
	// 1. 路径和库：Go 的库不许落在 Bun 的目录里（两个进程不能写同一个库文件），登记里每个平台的库都查
	dbFiles := []string{}
	for _, pl := range reg.Platforms() {
		dbFiles = append(dbFiles, p.ChatDB(pl.Name))
	}
	for _, f := range append(dbFiles, p.GoTraceDB()) {
		if err := snowluma.CheckDBPath(f); err != nil {
			return failed(err)
		}
	}
	set, err := store.OpenGoOpts(p, reg, store.OpenOptions{ReadOnly: a.localReadOnlyPlatforms(), Logf: a.logfFor(applog.Store)})
	if err != nil {
		return failed(fmt.Errorf("打开 Go 的库：%w", err))
	}
	a.set = set
	if len(set.Platforms()) == 0 {
		return failed(storeUnavailableAllError(set))
	}
	a.watchExternalLocal()
	a.degrade = &taskrun.Degrader{Logf: a.logfFor(applog.Taskrun), Alerts: a.alerts, Sink: &store.TaskSink{S: set.TraceStore()},
		Redact: func(s string) string { return RedactLog(s, a.tokens) }, Now: a.now}
	// 运行页用量的汇总表按本地时区对账（偏移没变就什么都不做，第一次或时区变了才从原始行重建）。
	// 失败只记日志：统计会退回直接查原始记录，结果一样，只是慢
	if _, err := runstats.SyncRollup(ctx, set.TraceStore().Trace, time.Local, a.now()); err != nil {
		a.logf("运行页：用量汇总表没对上账，统计先直接查原始记录：%v", err)
	}

	// 2. 模型
	// 查命令行要起一次 PowerShell（约一秒），和列模型同时做，不拖慢启动
	detect := opt.DetectShell
	if detect == nil {
		detect = turn.DetectShell
	}
	shellCh := make(chan *turn.Shell, 1)
	go func() { shellCh <- detect(ctx) }()
	client := opt.LLM
	if client == nil {
		client = llm.New(llm.Magpie(cfg.MagpieBaseURL()))
		// 内容空闲时限（agent.firstContentSeconds、contentIdleSeconds；0 = 不管）；测试给的假客户端自己定
		client.FirstContent = time.Duration(cfg.Agent.FirstContentSeconds * float64(time.Second))
		client.ContentIdle = time.Duration(cfg.Agent.ContentIdleSeconds * float64(time.Second))
	}
	a.llm = client
	// 请求按模型引用里的服务商分发：现在只有 magpie 接了连接（其余服务商的连接和目录是后面的步骤），
	// 引用到别家的明确报"没有可用的连接"，不落到 magpie
	a.router = llm.NewRouter(cfg.ModelRefs(), map[string]*llm.Client{catalogProvider: client}, cfg.DisabledProviders()...)
	a.initModelMeta()
	// 列模型不再挡启动（规格-模型配置 6.1）：取不到时有同来源的缓存用缓存、没有用空列表，
	// CatalogError 记下原因照常进接收中，后台按退避自己重试
	models, readAt, cached, catalogErr := a.loadModelCatalog(ctx)
	if catalogErr != nil && ctx.Err() != nil {
		return failed(ctx.Err()) // 是调用方取消/到时限，不是来源坏了：照旧不启动
	}
	catalogMsg := ""
	if catalogErr != nil {
		catalogMsg = catalogErr.Error()
		a.logf("%s", catalogFailure(cached, readAt, len(models), catalogErr))
	}
	models = a.resolveModels(models, a.cfg.ModelMeta.Overrides)
	if err := a.initModelRuntimeCatalog(models, readAt, catalogMsg); err != nil {
		return failed(err)
	}
	// 完整接收时先查账号绑的角色卡：失败要停在审批门、记忆、嵌入、内核建好之前（改动前的位置）。
	// 只开本地没有 QQ 角色可查。
	if !opt.LocalOnly {
		if err := a.checkModels(); err != nil {
			return failed(err)
		}
	}
	if catalogErr != nil {
		a.startCatalogRetry()
	}

	// 3. 审批门：权限照配置，卡片等桌面回答；没有桌面时仍按既有期限到期。
	if opt.SharedAnalyzer != nil {
		a.approvalGate = approval.NewGateWith(p.Root, opt.SharedAnalyzer)
		a.approvalGate.AnalyzeBudget = 2 * time.Minute // 测试专用：第一条命令要等共用 pwsh 冷启动，别把机器忙算成看不清
	} else {
		a.approvalGate = approval.NewGate(p.Root)
	}
	if opt.OnGate != nil {
		opt.OnGate(a.approvalGate)
	}
	// 备份目录（含联接解析后的真实路径）和数据目录一样是受保护目录：里面是数据目录的完整副本，读它也每次都问（规格-数据目录与备份 10.4）。
	// 要在审批门建好以后、项目说明的读取批准（下面）拿到受保护目录清单之前并进去
	a.openBackup()
	a.protectBackupDir()
	a.approvalGate.Mode = cfg.Tools.ApprovalMode
	lineend.SetDefault(cfg.Tools.LineEnding) // write 新建文件的换行兜底；审批预览和真写入读同一个值
	a.approvalGate.ParentOf = func(conversation string) string {
		if a.ecosystem == nil || a.ecosystem.Tasks == nil {
			return ""
		}
		if id, ok := strings.CutPrefix(conversation, "internal:"); ok {
			if s, ok := a.ecosystem.Tasks.Owner(id); ok {
				return s.Conversation
			}
		}
		return ""
	}
	a.approvalGate.ApprovalMinutes = cfg.Tools.ApprovalMinutes
	a.approvalGate.AutoApproveDirs = cfg.Tools.AutoApproveDirs
	a.approvals = approval.NewApprovals(a.approvalGate)
	a.asks = askowner.NewCenter()
	a.approvals.Logf = a.logfFor(applog.Approval)
	a.approvals.Errorf = a.errorFor(applog.Approval)
	a.approvals.Report = func(title, body string) {
		a.alerts.Raise(alert.Alert{Key: "approval:" + title, Class: "approval", Title: title, Body: body})
	}
	a.approvalGate.SessionSaveErr = func(err error) {
		a.logf("审批：会话审批档、“这个对话”的放行没写进文件，重启后会丢：%v", err)
	}
	if err := a.approvalGate.UseSessionFile(p.ApprovalSessionsFile()); err != nil {
		a.logf("审批：%v", err)
		a.alerts.Raise(alert.Alert{Key: "approval:sessions-load", Class: "approval", Title: "各会话的审批档读不了", Body: err.Error()})
	}
	if err := a.approvalGate.Grants.LoadErr; err != nil {
		a.logf("审批：%v", err)
		a.alerts.Raise(alert.Alert{Key: "approval:grants-load", Class: "approval", Title: "始终允许的记录读不了", Body: err.Error()})
	}
	if bad := a.approvalGate.Grants.BadRules; len(bad) > 0 {
		a.logf("放行规则里有写错的，已跳过：%s", strings.Join(bad, "；"))
		a.alerts.Raise(alert.Alert{Key: "approval:bad-rules", Class: "approval", Title: "有放行规则写错了", Body: "写错的规则不会生效（写错的拒绝规则也不会拦）。在 设置 · 工具与审批 里撤销后重写：" + strings.Join(bad, "；")})
	}
	turn.WireApprovalSteps(a.approvals, set.TraceStore())
	turn.WireGateSteps(a.approvalGate, set.TraceStore(), a.logfFor(applog.Turn))
	a.approvalGate.Egress = a.whisperEgress

	// 4. 内核。ctx 自己拿：关机的第三步才取消它（取消 Open 传进来的 ctx 会跳过前面的停适配器、停入库）
	hubCtx, hubCancel := context.WithCancel(context.Background())
	a.hubCancel = hubCancel
	a.startModelMetaRefresh(hubCtx)
	// 闸门的 Release、出口的 OnPart、内核的 OnSteer 互相要对方的实例：用闭包解开先后，不改组件的接口（任务第 5a 条）
	var responder *channel.Responder
	tc := turn.Config{
		Root: root, Data: p,
		Lifecycle:    a.lifecycleConfig(),
		WorkSettings: cfg.Agent.Work,
		Thinking:     string(cfg.Agent.Thinking.Group), RecentMessages: cfg.Agent.RecentMessages, Workdir: a.workdir(),
		Now: a.now, Gate: a.approvalGate, Logf: a.logfFor(applog.Turn), Warnf: a.warnFor(applog.Turn), Errorf: a.errorFor(applog.Turn), Debugf: a.debugFor(applog.Turn), Shell: <-shellCh, Retry: a.opt.ModelRetry,
		RefScope: memory.ScopeKey, // 参考信息块按听众的可见档存库（规格-提示词前缀稳定第七节）
		OwnerPrivate: func(conv string) bool { // 开始新话题只允许主人的平台私聊（qq:private:<她的号>:<对方号>），对端是 qq.owner
			parts := strings.Split(conv, ":")
			owner := string(cfg.QQ.Owner)
			return owner != "" && len(parts) == 4 && parts[0] == "qq" && parts[1] == "private" && parts[3] == owner
		},
		ApprovalRuleOf: func(conversation string) string {
			cwd, err := a.workspaceFor(context.Background(), conversation) // 会话绑了目录，“在 X 里写改不问”的 X 就是它
			if err != nil || cwd == "" {
				cwd = a.workdir()
			}
			return approvalRule(a.approvalGate.ModeFor(conversation), cwd, a.approvalGate.AutoApproveDirs)
		},
		WorkspaceFor:  a.workspaceFor,
		OnLocalStored: a.onLocalStored,
		PrepareImages: a.prepareImages,
		RestoreImages: a.restoreImages,
		LocalThinking: string(cfg.Agent.Thinking.Local), PrivateThinking: string(cfg.Agent.Thinking.Private),
		ModelSnapshot: func() (turn.Config, []llm.Model) {
			s := a.currentModels()
			c := s.turn
			c.CatalogError = s.CatalogError
			return c, s.Models
		},
		MaxLive: cfg.Agent.MaxLive, IdleAfter: time.Duration(cfg.Agent.IdleMinutes * float64(time.Minute)),
		IdleCompact: cfg.Agent.IdleCompact.Enabled, IdleCompactAfter: time.Duration(cfg.Agent.IdleCompact.After * float64(time.Minute)), IdleCompactMinTokens: cfg.Agent.IdleCompact.MinTokens,
		OnSteer: func(e turn.SteerEvent) {
			if responder != nil {
				responder.OnSteer(e)
			}
		},
	}
	var project *projectcontext.Plugin
	if cfg.ProjectContext.Enabled {
		userRoot := ""
		if cfg.ProjectContext.UserContext {
			userRoot = filepath.Join(p.Root, "config", "context")
		}
		// 信任 = 配置里手写的信任根（可以没有）+ 打开新目录、发现 AGENTS.md 时问一次、答过记住（规格-审批分级 8.5）
		var static projectcontext.Trust
		if len(cfg.ProjectContext.TrustedRoots) > 0 {
			static = projectcontext.ConfigTrust(cfg.ProjectContext.TrustedRoots, cfg.ProjectContext.RootChecks)
		}
		a.projectTrustDyn = newProjectTrust(static, a.approvalGate.Grants)
		a.projectTrust = a.projectTrustDyn
		project = projectcontext.NewPlugin(projectcontext.PluginOptions{
			Enabled: true, Trust: a.projectTrust, UserRoot: userRoot, Ask: a.askProjectTrust,
			Resolver: projectcontext.Options{Approve: a.projectReadApproval()},
		})
		tc.Instructions = project
	}
	ac := cfg.Agent.Context
	tc.Context = turn.ContextLimits{Window: ac.Window, Reserve: ac.Reserve, SoftRatio: ac.SoftRatio, HardRatio: ac.HardRatio,
		KeepTokens: ac.KeepTokens, NoAutoCompact: !ac.AutoCompactChat}
	WireKernelBudget(&tc, set.TraceStore(), a.router, cfg.Agent.Scheduler, cfg.Agent.Budget)
	a.hub = turn.NewHub(hubCtx, tc, set, a.router, models)
	a.slashes = newSlashTable()
	a.slashes.add(cdSlashCommand, a.slashCD)
	a.slashes.add(trustSlashCommand, a.slashTrust)
	if err := a.openKernelEcosystem(ctx); err != nil {
		return failed(err)
	}
	if err := a.openCheckpoints(); err != nil {
		return failed(err)
	}
	a.openPDF()
	if err := a.configureReviews(&a.hub.Cfg); err != nil {
		return failed(err)
	}
	if project != nil {
		if err := a.hub.Use(project); err != nil {
			a.logf("项目说明插件没启用，别的照常：%v", err)
		}
	}
	a.kernel = &trackedKernel{Kernel: a.hub}
	a.autoCtx, a.autoCancel = context.WithCancel(context.Background())
	// 本地私聊交给 Bun 时，目标、定期任务、提醒、主动联系、本地群聊、悄悄话都只写本地：一个都不装，Go 就不会到点触发、不会开轮
	if !a.localByBun() {
		if err := a.openGoals(); err != nil {
			return failed(err)
		}
	}
	if err := a.hub.Use(budgetNoticePlugin{app: a}); err != nil {
		a.logf("预算提醒插件没启用，别的照常：%v", err)
	}
	// 素材服务：图标、表情、字体经插件服务 "assets" 给别的插件用（规格-插件两层 第九节）
	if err := a.hub.Use(&assetsvc.Plugin{}); err != nil {
		a.logf("素材服务没启用，别的照常：%v", err)
	}
	if !a.localByBun() {
		if err := a.openLoops(); err != nil {
			return failed(err)
		}
		a.openProactive()
		a.openReminders()
	}

	a.openEmbedding()
	// 5. 记忆（只读）
	if err := a.openMemories(ctx); err != nil {
		return failed(err)
	}
	if !a.localByBun() {
		a.openLocalGroups()
		a.openWhispers()
	}

	// 6. 渠道中间件、7. 适配器和接收端、8. 提醒
	if err := a.openChannel(&responder); err != nil {
		return failed(err)
	}

	a.openBackup() // 要在自检之前：自检的 S9 读它的状态
	a.openSelfcheck()
	if err := a.openRemoteApproval(); err != nil {
		return failed(err)
	}
	if err := a.openClientInfo(); err != nil {
		return failed(err)
	}
	if err := a.openUserHooks(); err != nil {
		return failed(err)
	}
	if err := a.openLifeTools(); err != nil {
		return failed(err)
	}
	if err := a.openAskOwner(); err != nil {
		return failed(err)
	}
	if err := a.openSettingTool(); err != nil {
		return failed(err)
	}
	a.warnUnknownPlugins()

	if cfg.QQ.Reply {
		a.logf("配置里 qq.reply 开着：QQ 私聊只回主人，而且要发送启用条件（qq.goSend 等七项，见 qqSendMode）都齐了才回，不齐照旧只存不回；别人的私聊一律只存不回")
	}
	if err := ctx.Err(); err != nil {
		return failed(err)
	}
	return a, nil
}

// Run 起适配器收消息；ctx 取消就照第六节的顺序停，返回时全关好。
// 返回 error 只给"根本没法运行"（适配器返回的）和关机超时
func (a *App) Run(ctx context.Context) error {
	if a.shutDowned.Load() {
		return errors.New("这个 App 已经关过了")
	}
	if a.opt.Mode == Archive {
		<-ctx.Done()
		return a.Close()
	}
	// 每个渠道的适配器各起一个；任何一个返回就照第六节的顺序关，关机第 1 步停全部
	adapterCtx, stopAdapter := context.WithCancel(context.Background())
	defer stopAdapter()
	adapterDone := make(chan error, len(a.channels))
	for _, u := range a.channels {
		go func(u *channelUnit) { adapterDone <- u.adapter.Run(adapterCtx, u.sink) }(u)
	}

	a.runLoops()     // 定期任务的调度器随后台起，关机时最先停（stopAuto）
	a.runProactive() // 主动联系的导演同样随后台起、最先停
	a.runReminders() // 提醒的调度器同样随后台起、最先停
	a.runSelfcheck() // 运行自检：关机时随 closeSet 收尾
	a.runBackup()    // 每天自动备份的调度：关机时随 closeSet 收尾（先于关库）
	start := time.Now()
	var (
		cause       error
		adapterStop = false
	)
	select {
	case <-ctx.Done():
		// 带上等了多久：太晚说明 Run 在收到信号之前就被别的东西挡着了
		a.logf("收到退出信号，按顺序关（Run 起了 %v）", time.Since(start).Round(time.Millisecond))
	case err := <-adapterDone:
		adapterStop, cause = true, err
		if err != nil {
			a.logf("适配器返回了错误，按顺序关：%v", err)
		}
	}

	// 整体关机与第 3 步共用期限；超时返回前仍要收掉本 App 的子进程。
	limit := a.shutdownDuration()
	shutdownCtx, stopShutdown := context.WithTimeout(context.Background(), limit)
	defer stopShutdown()
	done := make(chan error, 1)
	go func() {
		done <- a.shutdown(shutdownCtx, func() {
			stopAdapter()
			left := len(a.channels)
			if adapterStop {
				left-- // 先返回的那个已经收过了
			}
			// 等到关机时限为止：哪个适配器不认取消，也不能把后面的交还发送锁、关库一起拖住
			for ; left > 0; left-- {
				select {
				case err := <-adapterDone:
					if err != nil {
						a.logf("关机时一个适配器返回了错误：%v", err)
					}
				case <-shutdownCtx.Done():
					a.logf("还有 %d 个适配器到关机时限还没停下，不再等，往下关", left)
					return
				}
			}
		})
	}()
	select {
	case err := <-done:
		if cause != nil {
			return cause
		}
		return err
	case <-shutdownCtx.Done():
		a.closing.Store(true)
		a.drainFailed.Store(true)
		a.hubCancel()
		a.closeProcesses()
		a.logf("关机超过 %v 还没关完，不再等（进程要退出）", limit)
		return fmt.Errorf("关机超过 %v 还没关完", limit)
	}
}

// Close 释放已经打开的后台，没启动过 Run 也能关。调用方必须先停止 Run，
// 等它退出再调 Close；关机不能和仍在使用资源的调用方并发。
// RPCGoRunFile 是 Go 的界面协议入口（-rpc-listen）写地址和令牌的文件（规格-Bun后台第1批 第三节）。
// run/rpc.json 归 Bun，Go 写 run/rpc-go.json（规格-本地私聊切到Bun）
func (a *App) RPCGoRunFile() string { return a.paths.RPCGoRunFile() }

func (a *App) Close() error {
	if a.opt.Mode == Archive {
		a.shutdownMu.Lock()
		defer a.shutdownMu.Unlock()
		if a.shutDowned.Load() {
			return nil
		}
		a.shutDowned.Store(true)
		return errors.Join(a.closeSet(), a.closeLocks())
	}
	ctx, cancel := context.WithTimeout(context.Background(), a.shutdownDuration())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.shutdown(ctx, func() {}) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		a.drainFailed.Store(true)
		return fmt.Errorf("关机未完成，保留库和数据锁：%w", ctx.Err())
	}
}

// shutdownDiag 记关机诊断：每一步开始、结束的时刻和用时进日志。负载下"取消来晚了"时看是哪一步挡着；
// 只记时长，不带号码和原文
type shutdownDiag struct{ start time.Time }

func newShutdownDiag() *shutdownDiag { return &shutdownDiag{start: time.Now()} }

// step 跑一步，前后各记一行
func (d *shutdownDiag) step(logf func(string, ...any), name string, f func()) {
	begin := time.Now()
	logf("关机诊断：%s 开始（关机后 %v）", name, begin.Sub(d.start).Round(time.Millisecond))
	f()
	end := time.Now()
	logf("关机诊断：%s 结束（关机后 %v，用时 %v）", name, end.Sub(d.start).Round(time.Millisecond), end.Sub(begin).Round(time.Millisecond))
}

// shutdown 照第六节的顺序停：关入口 → 封 Hub 并给已派发工具宽限 → 等审批授权、步骤落库 → 关库。
// 审批与路由、内核共用从封口前开始的 Drain 截止；到点升级取消，不另起一份等待。整体超时出口另外保证子进程已退出。
func (a *App) shutdown(ctx context.Context, stopAdapter func()) error {
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()
	if a.shutDowned.Load() && !a.drainFailed.Load() {
		return nil
	}
	d := newShutdownDiag()
	a.shutDowned.Store(true)
	a.closing.Store(true) // 提醒的出口不再写库（后面要关库）
	a.sealKernel()
	a.stopAuto()
	a.stopCatalogRetry(ctx)
	// 1. 停适配器：不再有新消息进来
	d.step(a.logf, "1 停适配器", stopAdapter)
	a.closeBackfill() // 取消并等补录退出，之后才关入库队列和库
	// 2. 先关入口，再取消内核；Close 可能正等在 StopIn，不能在取消和计时之前调。
	d.step(a.logf, "2 关入口", func() {
		a.closeRemoteApproval() // 它的发送走出口，要在出口关之前停
		a.pipeline.Close()
		a.responder.Stop()
		a.gate.Close()
		a.intake.Close()
		a.closeForwards()
		a.closeMedia() // 入库一站不再回调之后：取消在途的下载，之后不写库不写文件
		a.closeLinks()
	})
	// 3. 路由、裁决（含判断收尾）、内核 Outcome 共用一个截止时间。
	a.logf("关机诊断：3 取消内核、审批（关机后 %v）", time.Since(d.start).Round(time.Millisecond))
	wait := a.responderWait
	if wait == 0 {
		wait = time.Duration(a.cfg.Agent.Lifecycle.DrainTimeoutMS) * time.Millisecond
	}
	var idle context.Context
	var cancel context.CancelFunc
	if a.responderWait > 0 {
		idle, cancel = context.WithTimeout(ctx, a.responderWait)
	} else {
		idle, cancel = context.WithCancel(ctx)
	}
	responderDone := make(chan struct{})
	go func() { a.responder.Close(); close(responderDone) }()
	select {
	case <-responderDone:
	case <-idle.Done():
		a.logf("在途的回复 %v 内没停完，往下关", wait)
	}
	if err := a.drainKernel(idle); err != nil {
		cancel()
		a.logf("关机失败：%v", err)
		return err
	}
	cancel()
	if err := a.set.TraceStore().SealOutcomes(); err != nil {
		a.logf("关库前补判断结局出错：%v", err)
	}
	// 所有内核活动实际退出以后才允许关闭插件、出口和存储。
	d.step(a.logf, "4 等提前压缩停下、停记忆插件", func() {
		a.hub.Idle()
		a.hub.StopPlugin(memplugin.ID)
	})
	// 5. 出口：没发完的计划收尾，封库
	d.step(a.logf, "5 关出口", a.outlet.Close)
	// 5.5 交出 QQ 发送权（规格 5.2：停收 → 等在途 → 写 released → 关句柄）：出口的 Close 已经停收、
	// 等在发的回执最多 CloseWait + leakGrace（5 + 2 秒，在规格 15 秒的上限以内），这里只差写 released 和放锁。
	// 第 3 步不摘钳制，本来就没有真发的；以后摘了，这个顺序也照样成立
	d.step(a.logf, "5.5 交出 QQ 发送锁", a.releaseSendLock)
	// 6. 各角色记忆、库、提醒订阅
	d.step(a.logf, "6 关记忆、库、提醒", func() {
		for _, id := range a.memoryIDs {
			a.memories[id].Close()
		}
		if a.ownerMemory != nil {
			a.ownerMemory.Close()
		}
		a.closeProcesses()
		a.closePDF()
		if err := a.closeSet(); err != nil {
			a.logf("关库出错：%v", err)
		}
		if err := a.closeLocks(); err != nil {
			a.logf("放开数据库进程锁：%v", err)
		}
		if a.alertSub != nil {
			a.alertSub.Close()
			<-a.alertDone
		}
	})
	a.logf("关完了（关机诊断：总共 %v）", time.Since(d.start).Round(time.Millisecond))
	return nil
}

// cleanup 是 Open 中途失败时把已经打开的关掉（反着来）。没到那一步的字段是零值，跳过
func (a *App) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), a.shutdownDuration())
	defer cancel()
	a.cleanupWithContext(ctx)
}

func (a *App) cleanupWithContext(ctx context.Context) {
	a.shutdownMu.Lock()
	defer a.shutdownMu.Unlock()
	if a.shutDowned.Load() && !a.drainFailed.Load() {
		return
	}
	a.shutDowned.Store(true)
	a.closing.Store(true)
	a.sealKernel()
	a.stopAuto()
	a.stopCatalogRetry(ctx)
	a.closeBackfill()
	if a.intake != nil {
		a.intake.Close()
	}
	a.closeForwards()
	a.closeMedia()
	a.closeRemoteApproval()
	if a.pipeline != nil {
		a.pipeline.Close()
	}
	if a.gate != nil {
		a.gate.Close()
	}
	a.closeLinks()
	// 和正常关机相同：先封 Hub，再等审批持久化，最后才能关库。
	if err := a.drainKernel(ctx); err != nil {
		a.logf("启动清理失败：%v", err)
		return
	}
	if a.responder != nil {
		a.responder.Close()
	}
	if a.hub != nil {
		a.hub.Idle()
		a.hub.StopPlugin(memplugin.ID)
	}
	if a.outlet != nil {
		a.outlet.Close()
	}
	// 锁在出口之后交出去，和关机同一个顺序
	a.releaseSendLock()
	for _, id := range a.memoryIDs {
		a.memories[id].Close()
	}
	if a.ownerMemory != nil {
		a.ownerMemory.Close()
	}
	a.closeProcesses()
	if a.set != nil {
		a.closeSet()
	}
	a.closeLocks()
	if a.alertSub != nil {
		a.alertSub.Close()
		<-a.alertDone
	}
}

func (a *App) shutdownDuration() time.Duration {
	limit := max(shutdownLimit, time.Duration(a.cfg.Agent.Lifecycle.DrainTimeoutMS)*time.Millisecond)
	if a.shutdownWait > 0 && a.shutdownWait < limit {
		limit = a.shutdownWait
	}
	return limit
}

// closeProcesses 可从正常、启动失败、整体关机超时三个出口调用；返回时进程已回收且不会再启动。
func (a *App) closeProcesses() {
	a.processClose.Do(func() {
		tools.StopAllBackground() // 她起的后台命令跟着程序一起收掉
		if a.approvalGate != nil {
			if err := a.approvalGate.Grants.Flush(); err != nil { // 最近用过的时间平时隔一阵才落盘
				a.logf("放行规则的最近用过时间没写下：%v", err)
			}
		}
		if a.approvalGate != nil && a.approvalGate.Analyzer != nil && a.opt.SharedAnalyzer == nil {
			a.approvalGate.Analyzer.Close()
		}
		if a.embedding != nil {
			a.embedding.Stop()
		}
	})
}

// clock 是各组件的钟：现在几点取 Options.Now，计时器用真的
func (a *App) clock() types.Clock { return clock{now: a.now} }

// workdir 是本地会话的缺省目录，每轮由内核冻结到 Workspace，再传给工具和审批。
// 照 Bun tools.ts：相对路径按进程当前目录算，没写就是用户目录。
func (a *App) workdir() string {
	w := strings.TrimSpace(a.cfg.Tools.Workdir)
	if w == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return a.root
		}
		return home
	}
	if abs, err := filepath.Abs(w); err == nil {
		return abs
	}
	return w
}

// openDataDir 打开数据目录：已有的读身份证；只有 paths.ErrUninitialized（目录不存在，或只有 config/、characters/、models/）才 Init。
// 第二个返回值表示这次真的初始化了目录，调用方据此在日志里写一行。
func openDataDir(dir string) (paths.Paths, bool, error) {
	p, initialized, err := paths.OpenOrInit(dir)
	if err != nil {
		return paths.Paths{}, false, fmt.Errorf("打开数据目录 %s：%w", dir, err)
	}
	return p, initialized, nil
}

// prepareQQ 是建库前的一段 QQ 校验和取值：找号、核 URL、记下号和角色、取令牌。
// 账号非空时，缺 URL、缺令牌、号没配置都在建库之前失败，顺序和原来一样（规格-不带QQ也能用 设计第 2 条）。
// 临时：P2-1 过渡，P5-7 换成平台监督器。
func (a *App) prepareQQ() error {
	acct, err := pickAccount(a.cfg, a.opt.Account)
	if err != nil {
		return err
	}
	if strings.TrimSpace(acct.URL) == "" {
		return fmt.Errorf("账号 %s 没有写 qq.accounts[].url：这一版不自动找协议端地址", config.MaskID(string(acct.ID)))
	}
	a.account, a.character = string(acct.ID), acct.Character
	token, err := a.tokenFor(acct)
	if err != nil {
		return err
	}
	a.tokens = []string{token}
	return nil
}

// pickAccount 找要连的那个号：必须在 qq.accounts 里，绑了角色
func pickAccount(cfg config.Config, account string) (config.QQAccount, error) {
	account = strings.TrimSpace(account)
	if account == "" {
		return config.QQAccount{}, errors.New("没有说要连哪个 QQ 小号（-account）")
	}
	for _, x := range cfg.QQ.Accounts {
		if string(x.ID) == account {
			return x, nil
		}
	}
	return config.QQAccount{}, fmt.Errorf("账号 %s 不在 qq.accounts 里：先在配置里把这个小号写上、绑好角色", config.MaskID(account))
}

// tokenFor 取这个号的访问令牌（规格第三节、任务第 1 条）：配置里写了就用（${变量} 照 Bun 展开），
// 没写就读密钥 qq.<号>.token。都没有、锁着、格式坏都报错，错误里不带令牌原文
func (a *App) tokenFor(acct config.QQAccount) (string, error) {
	return tokenFor(a.paths, acct)
}

// tokenFor 是同一件事的独立函数：不看已打开的 App，自动接收前的便宜检查也能用
func tokenFor(p paths.Paths, acct config.QQAccount) (string, error) {
	id := string(acct.ID)
	if strings.TrimSpace(acct.Token) != "" {
		tok, err := config.ExpandEnv(acct.Token, nil)
		if err != nil {
			return "", fmt.Errorf("账号 %s 的访问令牌：%w", config.MaskID(id), err)
		}
		return tok, nil
	}
	st, err := secrets.Open(p.SecretsFile())
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		return "", fmt.Errorf("账号 %s 没有访问令牌：配置里没写 qq.accounts[].token，这个账号的访问令牌不在密钥库里：先用 sumika-ctl secrets list 看已有的名字，再用 sumika-ctl secrets set qq.<这个账号的号>.token 存进去", config.MaskID(id))
	case errors.Is(err, secrets.ErrLocked):
		return "", fmt.Errorf("账号 %s 的访问令牌取不出来：%w；用 sumika-ctl secrets unlock 输入主密码或恢复码", config.MaskID(id), err)
	case errors.Is(err, secrets.ErrFormat):
		return "", fmt.Errorf("账号 %s 的访问令牌取不出来：%w", config.MaskID(id), err)
	case errors.Is(err, secrets.ErrUnsupported):
		return "", fmt.Errorf("账号 %s 的访问令牌取不出来：%w", config.MaskID(id), err)
	case err != nil:
		return "", fmt.Errorf("打开密钥文件 %s：%w", p.SecretsFile(), err)
	}
	name := "qq." + id + ".token"
	tok, ok := st.Get(name)
	if !ok || tok == "" {
		return "", fmt.Errorf("账号 %s 的访问令牌不在密钥库里：先用 sumika-ctl secrets list 看已有的名字，再用 sumika-ctl secrets set qq.<这个账号的号>.token 存进去", config.MaskID(id))
	}
	return tok, nil
}

// checkModels 看本次账号绑定的角色能不能读卡、她的模型在不在目录里。其余角色只有要挂记忆时才读显示名。
// 模型不在列表只记一行、不挡启动（规格-模型配置 6.1 第 4 条）：照常进接收中，发消息时再报；目录没取到就不判断
func (a *App) checkModels() error {
	if a.character == "" {
		return nil // 只开本地：没有 QQ 角色可查
	}
	c, err := persona.Load(a.paths.CharactersDir(), a.character, a.cfg.Characters[a.character].Model)
	if err != nil {
		return fmt.Errorf("要连的角色 %s：%w", a.character, err)
	}
	cur := a.currentModels()
	if cur == nil || cur.CatalogError != "" {
		return nil // 目录没取到就没法判断；原因启动时已经记过一行，重试成功前不重复
	}
	if _, ok := cur.Find(c.Model); ok {
		return nil
	}
	a.logf("角色 %s 的模型 %q 不在模型的列表里（%s 现在有 %d 个）；照常进接收中，发消息时会报错", a.character, c.Model, a.catalogSource(), len(cur.Models))
	return nil
}

// characterIDs 是数据目录里每个有卡的角色（有 character.yml 的目录），排好序
func (a *App) characterIDs() ([]string, error) {
	ids, err := a.listCharacterIDs()
	if err != nil {
		return nil, err
	}
	if len(ids) == 0 {
		return nil, fmt.Errorf("数据目录里一个角色都没有（%s）：先把角色卡放进去", a.paths.CharactersDir())
	}
	return ids, nil
}

// listCharacterIDs 列出数据目录里有卡的角色（有 character.yml 的目录），排好序
func (a *App) listCharacterIDs() ([]string, error) {
	return characterIDsIn(a.paths.CharactersDir())
}

func characterIDsIn(charactersDir string) ([]string, error) {
	entries, err := os.ReadDir(charactersDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("列出角色：%w", err)
	}
	var ids []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := persona.CardDir(charactersDir, e.Name())
		if _, err := os.Stat(dir); err != nil {
			return nil, fmt.Errorf("看角色 %s 的卡目录：%w", e.Name(), err)
		}
		if _, err := os.Stat(filepath.Join(dir, "character.yml")); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("看角色 %s 的卡：%w", e.Name(), err)
		}
		ids = append(ids, e.Name())
	}
	return ids, nil
}

// openMemories 打开每个角色的记忆：默认只读（规格第五节）；memory.write 里列了的角色，前提都满足才以可写方式打开
// （memwrite.Open，规格-记忆写入上线 W5），不满足保持只读并写原因。缺库的角色先自动建空库（memory.CreateIfEmpty，
// 规格-记忆库从零建库）；建库失败不拦启动，这个角色不挂记忆、记日志和提醒，下次启动再试。文件在但打不开就报错（不许当没有记忆照跑）
func (a *App) openMemories(ctx context.Context) error {
	people, err := a.readPeople()
	if err != nil {
		return err
	}
	a.people = people
	ids, err := a.characterIDs()
	if err != nil {
		return err
	}
	a.memMu.Lock()
	a.memories = map[string]*memory.Memory{}
	a.memMu.Unlock()
	promptsDir := prompts.Store{Root: a.root, UserDir: a.paths.PromptsDir()}
	ownerRel, err := filepath.Rel(a.paths.Root, a.paths.OwnerMemoryDB())
	if err != nil {
		ownerRel = filepath.Base(a.paths.OwnerMemoryDB())
	}
	pool, err := memory.OpenOwnerPool(ctx, a.paths.OwnerMemoryDB(), a.embedding, people.People, memory.Options{
		Writable: true, SummarizeEnabled: true, AutoConfirm: a.cfg.Memory.Write.AutoConfirm,
		SnapshotDir: a.paths.UpgradeSnapshotDir(), SnapshotRel: filepath.ToSlash(ownerRel),
		ConfirmOccasions: a.cfg.Memory.Write.ConfirmOccasions, ConfirmKinds: a.cfg.Memory.Write.ConfirmKinds,
		QuarantineSince: memwrite.QuarantineSince(a.cfg.Memory.Write), Logf: a.logfFor(applog.Memory), Now: a.now,
		SourceEvents: func(char string) (memory.EventSource, error) { return memsource.NewMulti(a.set, char) },
		// 共用池的条目谁都能引用：删了、改了，所有角色引用它的参考信息块都清掉
		OnChanged: func(refs []string) { a.hub.PurgeRefBlocks("", refs) },
	})
	if err != nil {
		return fmt.Errorf("打开主人共用记忆：%w", err)
	}
	a.memMu.Lock()
	a.ownerMemory = pool
	a.memMu.Unlock()
	if a.embedding != nil {
		if status, _ := a.embedding.Status(); status == embed.StatusReady {
			pool.EmbeddingReady()
		}
	}
	createMemoryDB := a.opt.NewMemoryDB
	if createMemoryDB == nil {
		createMemoryDB = memory.CreateIfEmpty
	}
	for _, id := range ids {
		db := a.paths.MemoryDB(id)
		// 先读卡：卡坏了又没有库的角色不建库、不挂记忆（和以前“没有库就跳过”一样，不挡别的角色）；
		// 已经有库的角色卡坏了照旧整体启动失败。
		name, nameErr := persona.Name(a.paths.CharactersDir(), id)
		_, statErr := os.Stat(db)
		existed := statErr == nil
		if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
			return fmt.Errorf("看角色 %s 的记忆库：%w", id, statErr)
		}
		if nameErr != nil {
			if !existed {
				a.logf("角色 %s 的卡读不了（%v），没有记忆库也不新建，这次不挂记忆", id, nameErr)
				if a.cfg.Memory.Write.Remembers(id) || a.cfg.Memory.Write.Summarizes(id) {
					a.logf("记忆[%s]：写入保持只读（原因 %s）", id, memwrite.ReasonNoMemoryDB)
				}
				continue
			}
			return fmt.Errorf("读要挂记忆的角色 %s 的卡：%w", id, nameErr)
		}
		rel, relErr := filepath.Rel(a.paths.Root, db)
		if relErr != nil {
			rel = filepath.Base(db)
		}
		created, rep, err := createMemoryDB(ctx, a.paths.Root, db, a.paths.UpgradeSnapshotDir(), filepath.ToSlash(rel))
		if err != nil {
			// 建库失败不拦启动：新库还没有数据要保护，这个角色退回“没有记忆库”的合同，下次启动再试。
			// 已有库（文件在、有用户表）打不开、坏了，照旧整体启动失败。
			if rep.Action != dbguard.ActionCreate {
				return fmt.Errorf("看角色 %s 的记忆库：%w", id, err)
			}
			reason := createMemoryReason(err)
			a.logf("角色 %s 的记忆库没建成（%s，原因 %s），这次不挂记忆：%v", id, db, reason, err)
			a.alerts.Raise(alert.Alert{
				Key: "memory:create:" + id, Class: "记忆", Title: "角色 " + id + " 的记忆库没建成",
				Body: "路径 " + db + "；原因 " + reason + "。这个角色这次不挂记忆，修好以后重启会再试。",
			})
			if a.cfg.Memory.Write.Remembers(id) || a.cfg.Memory.Write.Summarizes(id) {
				a.logf("记忆[%s]：写入保持只读（原因 %s）", id, memwrite.ReasonNoMemoryDB)
			}
			continue
		}
		if created {
			a.logf("角色 %s 没有记忆库，已新建空库（%s）", id, db)
		}
		events, err := memsource.NewMulti(a.set, id)
		if err != nil {
			return fmt.Errorf("角色 %s 的事件来源：%w", id, err)
		}
		m, err := memwrite.Open(ctx, a.memoryWriteInput(id, db), func(s memwrite.Settings) (*memory.Memory, error) {
			opt := memory.Options{
				Character: id, Events: events, Prompts: promptsDir, Name: name, OwnerName: persona.OwnerCallFor(a.paths.CharactersDir(), id),
				Logf: a.logfFor(applog.Memory), Now: a.now,
				Shared: pool, OnChanged: func(refs []string) { a.hub.PurgeRefBlocks(id, refs) },
				Gen: memoryGenerator{app: a, character: id}, Model: func() string { return a.backgroundModel(id) },
				Background: func() (string, memory.Generator) {
					s := a.currentModels()
					return s.Values.Background(id), memoryGenerator{app: a, runtime: s, character: id}
				},
				Reranker: a.memoryReranker(), RerankK: a.cfg.Memory.Rerank.K,
			}
			s.Apply(&opt)
			return memory.Open(ctx, db, a.embedding, people.People, opt)
		})
		if err != nil {
			return fmt.Errorf("打开角色 %s 的记忆：%w", id, err)
		}
		a.memMu.Lock()
		a.memories[id] = m
		a.memoryIDs = append(a.memoryIDs, id)
		a.memMu.Unlock()
		// Open 看嵌入状态的那一刻到这里挂上之间就绪的，回调那会儿这个记忆还没挂上，没人通知它：挂上以后再看一眼
		if a.embedding != nil {
			if status, _ := a.embedding.Status(); status == embed.StatusReady {
				m.EmbeddingReady()
			}
		}
	}
	for _, listed := range append(slices.Clone(a.cfg.Memory.Write.Characters), a.cfg.Memory.Write.Summarize...) {
		if listed == "*" {
			continue
		}
		if !slices.Contains(ids, listed) {
			a.logf("记忆写入：memory.write 里的角色 %s 不存在（原因 unknown_character）", listed)
		}
	}
	mp := memplugin.New(memplugin.Options{
		Memories:      func(id string) *memory.Memory { return a.memories[id] },
		Characters:    func() []string { return ids },
		Now:           a.now,
		Logf:          a.logfFor(applog.Memwrite),
		WhisperOrigin: a.whisperOriginOfInput,
	})
	if err := a.hub.Use(mp); err != nil {
		return fmt.Errorf("挂记忆插件：%w", err)
	}
	return nil
}

// createMemoryReason 是建库失败原因词：护栏的固定词；不是护栏错误（比如建目录失败）按 create_failed 记。
func createMemoryReason(err error) string {
	var u *dbguard.Unavailable
	if errors.As(err, &u) && u.Reason != "" {
		return u.Reason
	}
	return dbguard.ReasonCreateFailed
}

// memoryWriteInput 把配置和路径收成 memwrite 要的输入（后台模型有没有配、备份目录读 cfg.Backup.Dir、空才兜底到数据目录旁边）
func (a *App) memoryWriteInput(id, db string) memwrite.Input {
	// 开写前的 memory.db 副本放在每天备份的那个目录里：用 backup.ResolveDir 的结果（相对路径、临时数据目录、写错了的情况都和每天备份一样处理），不自己拼
	dir, dirErr := a.backupDirForCopies() // 算不出来时它记 Warn（同一原因只记一次）
	return memwrite.Input{
		Data: a.paths.Root, DB: db, Char: id, Cfg: a.cfg.Memory.Write,
		BackupDir: dir, BackupFallback: a.paths.BackupRoot(), BackupDirErr: dirErr,
		HasModel: a.backgroundModel(id) != "", Now: a.now, Logf: a.logfFor(applog.Memwrite),
	}
}

// readPeople 从 Go 自己的 QQ 库（go/chat/qq.db）只读读 qq_people（别名、名片，ADR-038）。
// 以前读 Bun 的 shared/events.db，Bun 退役后那份库一挪走名册就空了（Bun 残留盘点）；两边是导入时同一份数据。库文件不存在只是"还没有名册"。
func (a *App) readPeople() (memsource.QQPeople, error) {
	if u, off := a.set.Unavailable()["qq"]; off {
		// 库打不开时连只读也不碰（护栏判它读不了）：QQ 名册先空着，记忆和界面照常。
		a.logf("QQ 的库没打开，名册先空着：%s", u.Reason)
		return memsource.QQPeople{}, nil
	}
	file := a.paths.ChatDB("qq")
	if _, err := os.Stat(file); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			a.logf("QQ 库 %s 还不存在，名册空着（记忆里的别名和名片暂时用不上）", file)
			return memsource.QQPeople{}, nil
		}
		return memsource.QQPeople{}, fmt.Errorf("看 QQ 库 %s：%w", file, err)
	}
	people, err := memsource.ReadQQPeople(file)
	if err != nil {
		return memsource.QQPeople{}, err
	}
	return people, nil
}

// sinkOf 是给 QQ 适配器的接收端
func sinkOf(a *App) *sink { return &sink{a: a, channel: "qq", account: a.account} }
