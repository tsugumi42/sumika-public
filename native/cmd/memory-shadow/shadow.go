package main

// 一次影子整理的流程（规格-记忆写入上线 W6 技术路线）：
//
//	检查参数 → -work 守卫 → 清上次遗留 → 拷库（snapshot.go）→ 对副本迁移 → 开副本的聊天库和记忆库 → 认场合、筛会话 →
//	（可选）种游标 → 对放行的会话循环 SummarizeBatch(force) → 读副本里新写的东西（extract.go）→ 删副本
//
// 全程只碰 -work/copy 里的副本；真库只在 takeSnapshot 里被读。

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"sumika-native/internal/datalock"
	"sumika-native/internal/memory"
	"sumika-native/internal/paths"
	"sumika-native/internal/prompts"
	"sumika-native/internal/store"
	"sumika-native/internal/store/memsource"
)

// summaryPromptName 是生产整理读的提示词文件名（memory 包的 summaryPrompt，W3a 起是 summarize-v2；summarize.md 是 Bun 读的旧版，Go 不读）。
// 那个常量没导出，这里照抄一份；抄错了 -prompt 换上的文件就到不了模型，TestPromptOverride 靠"换上的标记词真进了发给模型的提示词"钉住
const summaryPromptName = "summarize-v2"

type mode string

const (
	modeEstimate mode = "estimate" // 默认：固定空回复把循环跑一遍，只数批数和字数，一次模型都不调
	modeFake     mode = "fake"     // 固定回复（或注入的脚本），整条流水线走一遍
	modeReal     mode = "real"     // 真的调后台模型；必须给 spend
)

type runConfig struct {
	Data, Work, Char string
	Name             string // 她的名字（填进提示词），空用角色 id
	Root             string // 项目根（找 prompts/），空从当前目录往上找
	Places           []string
	Cursors          string // "real"（默认，用副本里的真实游标）| "seed"（给每个会话种游标，提示词 A/B 用）
	SeedLast         time.Duration
	SeedSince        time.Time
	MaxBatches       int // 每个会话最多跑几批，零用 50
	Mode             mode
	Spend            int    // real 必须 > 0：最多调几次后台模型
	Model            string // real 用的后台模型 id
	Gen              memory.Generator
	PromptFile       string
	Keep             bool
	Seed             int64 // 漏记抽查抽样的随机种子，零用 1

	Ignore ignoreCheck
	Guard  pathGuard
	Logf   func(string, ...any)
	Now    func() time.Time

	// 给测试：callCap 在 real 以外的模式里也限制调用次数；hookAfterCopy 在拷完、开库之前调用
	callCap       int
	hookAfterCopy func(copyDir string)
	// hookBeforeExtract 在整理跑完、读副本之前调用（副本的记忆库已关）：测试往里塞一条范围宽了的事实，看自检红不红
	hookBeforeExtract func(memFile string)
	// noToolGate 只给测试：关掉工具侧的第二道门（每个有游标的会话都当放行），看 W5 的第一道门（Options.WritePlaces）自己拦不拦得住
	noToolGate bool
}

func (c *runConfig) logf(f string, a ...any) {
	if c.Logf != nil {
		c.Logf(f, a...)
	}
}

func (c *runConfig) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// validate 检查参数并补默认值。花钱的门在这里：-gen real 必须同时给 spend，工具自己不设默认值
func (c *runConfig) validate() error {
	if strings.TrimSpace(c.Data) == "" {
		return errors.New("要给 -data（数据目录）")
	}
	if strings.TrimSpace(c.Char) == "" {
		return errors.New("要给 -char（角色 id）")
	}
	if c.Mode == "" {
		c.Mode = modeEstimate
	}
	switch c.Mode {
	case modeEstimate, modeFake:
		if c.Spend != 0 {
			return errors.New("-spend 只配 -gen real 用：估算和 fake 不调后台模型，不花钱")
		}
	case modeReal:
		if c.Spend <= 0 {
			return errors.New("-gen real 必须同时给 -spend <最多调几次后台模型>：工具自己不设默认值（规格 W6 第 6 点）")
		}
		if !realGenReady {
			return errors.New("-gen real 现在是关着的：场合门已经接上，缺的是真实生成器、W5 合进 main、主人看过估算同意 -spend 的数（见 gen.go 的 realGenReady）")
		}
		if c.Gen == nil || strings.TrimSpace(c.Model) == "" {
			return errors.New("-gen real 要有后台模型的生成器和模型 id")
		}
		c.callCap = c.Spend
	default:
		return fmt.Errorf("-gen %q 不认识（认 fake、real；不给就是估算）", c.Mode)
	}
	switch c.Cursors {
	case "", "real":
		c.Cursors = "real"
		if c.SeedLast != 0 || !c.SeedSince.IsZero() {
			return errors.New("-last / -since 只配 -cursors seed 用")
		}
	case "seed":
		if (c.SeedLast > 0) == !c.SeedSince.IsZero() {
			return errors.New("-cursors seed 要给 -last <天数> 或 -since <日期>，二选一")
		}
	default:
		return fmt.Errorf("-cursors %q 不认识（认 real、seed）", c.Cursors)
	}
	if c.MaxBatches <= 0 {
		c.MaxBatches = 50
	}
	if len(c.Places) == 0 {
		c.Places = append([]string(nil), defaultPlaces...)
	}
	if c.Seed == 0 {
		c.Seed = 1
	}
	if c.Name == "" {
		c.Name = c.Char
	}
	return nil
}

// batchRec 是一批的记录（只有计数，没有正文）
type batchRec struct {
	N           int
	Conv        string
	From, To    int64
	ModelCalled bool
	Wrote       bool
	Counts      map[string]int
	Err         string
}

// emptyBatch 是一批"调了模型却什么都没写"的批，漏记抽查用。Transcript 是这一批实际给过模型的聊天行（取自提示词，不是另外去库里重取）
type emptyBatch struct {
	N          int
	Conv       string
	From, To   int64
	Transcript string
	Reason     string
}

const emptySample = 30 // 漏记抽查抽几批

type runResult struct {
	Mode           mode
	CursorMode     string
	SeedSince      time.Time
	Places         []string
	Char           string
	Snap           *snapshot
	Sessions       []sessionInfo
	Batches        []batchRec
	Empties        []emptyBatch
	EmptyTotal     int
	Counts         map[string]int
	Sweep          memory.ConfirmSweepResult // 打开 autoConfirm 的那次启动做的补提升（先于整理循环）
	Calls          int
	PromptRunes    int
	ReplyRunes     int
	CallCap        int
	Truncated      bool // 被 -spend 截断
	PromptSource   string
	PromptSHA      string
	Started        time.Time
	Finished       time.Time
	Work, Out      string
	Kept           string // -keep 时副本留在哪
	Extract        *extracted
	Check          *checkResult
	ShadowSeed     int64
	CopyDir        string
	StaleCleaned   int
	MemoryRelative string
}

var wroteKeys = []string{memory.CountAddedActive, memory.CountAddedCandidate, memory.CountAddedEpisode, memory.CountMerged, memory.CountEvidenceAdded, memory.CountRevoked}

func wrote(counts map[string]int) bool {
	for _, k := range wroteKeys {
		if counts[k] > 0 {
			return true
		}
	}
	return false
}

// runShadow 做完一整次。返回时副本已经删掉（-keep 除外），报告数据都在返回值里
func runShadow(ctx context.Context, cfg runConfig) (res *runResult, err error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	data, err := filepath.Abs(cfg.Data)
	if err != nil {
		return nil, err
	}
	if _, err := paths.Open(data); err != nil {
		return nil, fmt.Errorf("数据目录不对：%w", err)
	}
	cfg.Data = data
	started := cfg.now()
	root := cfg.Root
	if root == "" {
		if root, err = findRoot("."); err != nil {
			return nil, err
		}
	}
	ps := prompts.Store{Root: root}
	text, _, err := ps.Read(summaryPromptName)
	if err != nil {
		return nil, fmt.Errorf("读整理提示词：%w", err)
	}
	res = &runResult{Mode: cfg.Mode, CursorMode: cfg.Cursors, Places: cfg.Places, Char: cfg.Char, Started: started, ShadowSeed: cfg.Seed,
		PromptSource: "出厂 prompts/" + summaryPromptName + ".md", CallCap: cfg.callCap, Counts: map[string]int{}}

	work := cfg.Work
	if work == "" {
		work = defaultWork(data, started)
	}
	res.StaleCleaned = cleanStale(data, started, cfg.logf)
	work, err = prepareWork(data, work, cfg.Ignore, started)
	if err != nil {
		return nil, err
	}
	res.Work, res.Out = work, filepath.Join(work, "out")
	copyDir := filepath.Join(work, "copy")
	res.CopyDir = copyDir
	for _, d := range []string{copyDir, res.Out} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, err
		}
	}
	// 本次运行持到结束的锁：别的运行的"清上次遗留"据此知道这份副本还有人在用
	runLock, err := datalock.LockData(copyDir)
	if err != nil {
		return nil, err
	}
	defer func() {
		unmarked := false // -keep 的标记没记上：副本在，但以后的运行不一定放过它
		if cfg.Keep {
			// 先记 .keep 再放锁，不然另一次运行的 cleanStale 在两步之间撞上，会把它当崩溃遗留删掉
			if kerr := markKeep(work); kerr != nil {
				unmarked = true
				cfg.logf("记不上 -keep 的标记：%v（下一次运行可能把这份副本当崩溃遗留清掉，要留请先拷走）", kerr)
			}
		}
		runLock.Close()
		if cfg.Keep {
			// 出错返回时 res 是 nil（return nil, err 把具名返回值覆盖了）：不能直接写 res.Kept，路径只能走日志
			if res != nil {
				res.Kept = copyDir
			}
			if unmarked {
				// 标记没记上，不能再说"以后的运行不会清它"：和上一行互相矛盾，读的人会信错的那句
				cfg.logf("副本这会儿还在 %s（-keep 的标记没记上，要留请先拷走；用完请删）", copyDir)
			} else {
				cfg.logf("副本留在 %s（-keep，用完请删；以后的运行不会清它）", copyDir)
			}
			return
		}
		if rerr := os.RemoveAll(copyDir); rerr != nil {
			cfg.logf("删副本 %s 失败：%v（请手动删）", copyDir, rerr)
		}
	}()

	if cfg.PromptFile != "" {
		b, err := os.ReadFile(cfg.PromptFile)
		if err != nil {
			return nil, fmt.Errorf("读 -prompt：%w", err)
		}
		text = string(b)
		ud := filepath.Join(copyDir, "prompts")
		if err := os.MkdirAll(ud, 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(ud, summaryPromptName+".md"), b, 0o600); err != nil {
			return nil, err
		}
		ps.UserDir = ud
		res.PromptSource = "文件 " + filepath.Base(cfg.PromptFile)
	}
	res.PromptSHA = shaOfText(prompts.LF(text))

	snap, err := takeSnapshot(ctx, snapOpts{Data: data, Copy: copyDir, Char: cfg.Char, Guard: cfg.Guard, Logf: cfg.logf})
	if err != nil {
		return nil, err
	}
	res.Snap, res.MemoryRelative = snap, snap.MemoryRel
	if cfg.hookAfterCopy != nil {
		cfg.hookAfterCopy(copyDir)
	}

	memFile := filepath.Join(copyDir, filepath.FromSlash(snap.MemoryRel))
	if err := memory.Migrate(ctx, memFile); err != nil {
		return nil, fmt.Errorf("对副本迁移：%w", err)
	}
	set, err := store.OpenGoReadOnly(ctx, paths.Paths{Root: copyDir})
	if err != nil {
		return nil, fmt.Errorf("开副本的聊天库：%w", err)
	}
	defer set.Close()
	events, err := memsource.NewMulti(set, cfg.Char)
	if err != nil {
		return nil, err
	}

	// 认场合、筛会话
	heads, err := events.Conversations(ctx)
	if err != nil {
		return nil, fmt.Errorf("列会话：%w", err)
	}
	for _, h := range heads {
		kind, err := classifyConv(ctx, events, h.Conversation)
		if err != nil {
			return nil, fmt.Errorf("认会话场合：%w", err)
		}
		allowed := kind.allowed(cfg.Places) || cfg.noToolGate
		res.Sessions = append(res.Sessions, sessionInfo{Conv: h.Conversation, Kind: kind, Allowed: allowed, LastSeq: h.LastSeq, LastAt: h.LastAt})
	}

	if cfg.Cursors == "seed" {
		since := cfg.SeedSince
		if cfg.SeedLast > 0 {
			since = started.Add(-cfg.SeedLast)
		}
		res.SeedSince = since
		if err := seedCursors(ctx, memFile, set, res.Sessions, since, started); err != nil {
			return nil, err
		}
	}

	ids, err := existingIDs(memFile)
	if err != nil {
		return nil, err
	}

	var inner memory.Generator
	switch {
	case cfg.Mode == modeReal:
		inner = cfg.Gen
	case cfg.Mode == modeFake && cfg.Gen != nil:
		inner = cfg.Gen
	case cfg.Mode == modeFake:
		inner = demoGen{}
	default:
		inner = fixedGen{emptyReply}
	}
	gen := &spendGen{inner: inner, limit: cfg.callCap}
	model := cfg.Model
	if model == "" {
		model = "shadow-" + string(cfg.Mode)
	}
	// SummarizeEnabled 和 WritePlaces 是 W5 的两样：整理开着，放行的名单就是 -places（第一道场合门，SummarizeBatch 里；工具侧的 classify.go 是第二道）。
	// AutoConfirm 在副本里强制打开（正式配置 memory.write.autoConfirm 默认关）：攒够证据的待确认事实真的提升，报告 C 节才列得出"打开以后会提升哪几条"；
	// ConfirmOccasions、ConfirmKinds 不给，用生产的默认（3 次不同场合、只有 fact 和 preference）
	mem, err := memory.Open(ctx, memFile, nil, nil, memory.Options{
		Gen: gen, Events: events, Writable: true, SummarizeEnabled: true, WritePlaces: append([]string(nil), cfg.Places...), AutoConfirm: true,
		Character: cfg.Char, Logf: cfg.logf, Now: cfg.Now,
		Prompts: ps, Name: cfg.Name, Model: func() string { return model },
	})
	if err != nil {
		return nil, fmt.Errorf("开副本的记忆库：%w", err)
	}
	if !mem.SummarizeEnabled() {
		// 门没开却照常汇报"没有可整理的"是最糟的结果（边界 ⑧）：直接报错，不往下跑
		mem.Close()
		return nil, errors.New("副本的记忆库没有打开整理（SummarizeEnabled）：不往下跑")
	}

	// 每个放行的会话：游标现在在哪、落后多少、涉及几个人
	for i := range res.Sessions {
		s := &res.Sessions[i]
		if !s.Allowed {
			continue
		}
		st, ok, err := mem.SummaryState(ctx, s.Conv)
		if err != nil {
			mem.Close()
			return nil, err
		}
		s.HasCursor, s.Cursor = ok, st.LastSeq
		if !ok {
			continue // 没有游标的新会话这次不整理，落后多少、涉及几个人都不数（不然人数是整段历史的）
		}
		if s.LastSeq > s.Cursor {
			s.Behind = s.LastSeq - s.Cursor
		}
		if s.People, err = speakersSince(ctx, events, s.Conv, s.Cursor); err != nil {
			mem.Close()
			return nil, err
		}
	}

	// 打开 autoConfirm 的那次启动，memplugin 在排整理之前先对已经攒够证据的待确认事实补提升一遍（W3b 的 ConfirmSweep）；
	// 这里同样先做：导入的旧候选带着 Bun 记的证据行，会不会一起被提升，C 节要让你先看到（不调模型，估算模式也做）
	sw, err := mem.ConfirmSweep(ctx)
	if err != nil {
		mem.Close()
		return nil, fmt.Errorf("启动补提升：%w", err)
	}
	res.Sweep = sw
	cfg.logf("启动补提升：%s", sweepText(sw))

	runLoop(ctx, mem, gen, &cfg, res)
	res.Calls, res.PromptRunes, res.ReplyRunes, res.Truncated = gen.stats()
	if err := mem.Close(); err != nil {
		cfg.logf("关副本的记忆库：%v", err)
	}

	if cfg.hookBeforeExtract != nil {
		cfg.hookBeforeExtract(memFile)
	}
	ex, err := extract(ctx, memFile, events, ids)
	if err != nil {
		return nil, fmt.Errorf("读副本里新写的东西：%w", err)
	}
	res.Extract = ex
	if res.Check, err = runSelfCheck(ctx, ex, events); err != nil {
		return nil, fmt.Errorf("自检：%w", err)
	}
	res.Finished = cfg.now()
	return res, nil
}

// runLoop 对放行的会话循环 SummarizeBatch(force)：直到游标不再前进，或到 -max-batches，或花费上限
func runLoop(ctx context.Context, mem *memory.Memory, gen *spendGen, cfg *runConfig, res *runResult) {
	rng := rand.New(rand.NewSource(cfg.Seed))
	n := 0
	stop := false
	for i := range res.Sessions {
		s := &res.Sessions[i]
		if !s.Allowed || stop {
			if s.Allowed && stop {
				s.NotRun = true
			}
			continue
		}
		if !s.HasCursor {
			// 没有游标的新会话：真开的时候从当时的最新一条起、不回填，这里一批也不整理（seed 模式下每个放行的会话都有游标）
			continue
		}
		for b := 0; b < cfg.MaxBatches; b++ {
			st, _, err := mem.SummaryState(ctx, s.Conv)
			if err != nil {
				s.Err = memory.FailureCode(err)
				break
			}
			gen.take()
			r, err := mem.SummarizeBatch(ctx, s.Conv, true)
			got := gen.take()
			if err != nil {
				if errors.Is(err, errBudget) {
					res.Truncated = true
					stop = true
					break
				}
				s.Err = memory.FailureCode(err)
				break
			}
			if r.Skipped == memory.SkipPlace {
				// W5 的第一道门拦下了：工具侧认为放行、但它读了事件以后判出不是主人的私聊（或会话 id 它认不出）。游标没动，没调模型
				s.PlaceSkipped = true
				res.Counts[memory.CountPlaceSkipped] += r.Counts[memory.CountPlaceSkipped]
				break
			}
			if r.Cursor <= st.LastSeq {
				break // 游标没前进：没有可整理的了
			}
			n++
			rec := batchRec{N: n, Conv: s.Conv, From: st.LastSeq + 1, To: r.Cursor, ModelCalled: got.Called, Wrote: wrote(r.Counts), Counts: r.Counts}
			res.Batches = append(res.Batches, rec)
			s.Batches++
			for k, v := range r.Counts {
				res.Counts[k] += v
			}
			if got.Called && !rec.Wrote {
				res.EmptyTotal++
				eb := emptyBatch{N: n, Conv: s.Conv, From: rec.From, To: rec.To, Transcript: transcriptOf(got.Prompt), Reason: episodeReasonOf(got.Reply)}
				switch {
				case len(res.Empties) < emptySample:
					res.Empties = append(res.Empties, eb)
				default:
					if j := rng.Intn(res.EmptyTotal); j < emptySample {
						res.Empties[j] = eb
					}
				}
			}
			if b == cfg.MaxBatches-1 && r.More {
				s.CapHit = true
			}
		}
	}
	sort.SliceStable(res.Empties, func(i, j int) bool { return res.Empties[i].N < res.Empties[j].N })
}

// seedCursors 给每个放行的会话种游标：last_seq = 窗口起点（since）之前的最后一条的 seq，没有更早的就是 0（整段都在窗口里）。
// 直接对副本记忆库写 SQL，在 Memory.Open 之前（ReopenSummary 只处理停了的会话，不能种游标）。报告里要大字写"不是真实游标"
func seedCursors(ctx context.Context, memFile string, set *store.Set, ss []sessionInfo, since, now time.Time) error {
	db, err := sql.Open("sqlite", sqliteDSN(memFile, false))
	if err != nil {
		return err
	}
	defer db.Close()
	for _, s := range ss {
		if !s.Allowed {
			continue
		}
		st, err := set.ForConversation(s.Conv)
		if err != nil {
			return fmt.Errorf("种游标：%w", err)
		}
		var seq int64
		if err := st.Events.QueryRowContext(ctx, `SELECT COALESCE(MAX(seq), 0) FROM events WHERE conversation = ? AND created_at < ?`, s.Conv, since.UnixMilli()).Scan(&seq); err != nil {
			return fmt.Errorf("种游标：%w", err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO summary_state (conversation, last_seq, updated_at, failures, retry_at) VALUES (?, ?, ?, 0, NULL)
			ON CONFLICT(conversation) DO UPDATE SET last_seq = excluded.last_seq, updated_at = excluded.updated_at, failures = 0, retry_at = NULL`,
			s.Conv, seq, now.UnixMilli()); err != nil {
			return fmt.Errorf("种游标：%w", err)
		}
	}
	return nil
}
