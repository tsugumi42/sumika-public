package app

// 断线补录只在 QQ 接收装配里运行；连接寿命管理和一轮处理都在这里，实时入库不改水位。
import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

const (
	backfillPageSize = 100
	backfillPageCap  = 5
	backfillBudget   = 120
	backfillRetry    = 10 * time.Minute
	backfillOverlap  = time.Minute
	backfillSettle   = 30 * time.Second // 连上以后等多久才开始第一轮：等重连抖动过去
)

type backfillSource interface {
	types.HistoryReader
	Caps() types.Caps
	Ready() bool
	BeginQueryRound(int)
	EndQueryRound()
	RemainingQueryBudget() int
}

type qqBackfill struct {
	st             *store.Store
	intake         *channel.Intake
	source         backfillSource
	clock          types.Clock
	settle         time.Duration // 测试才设；0 是 backfillSettle
	account, owner string
	groups         []string
	logf           func(string, ...any)
	mu             sync.Mutex // 连接通知与关闭串行；等旧一轮退出后才接新连接
	cancel         context.CancelFunc
	done           chan struct{}
	lastRound      time.Time // 仅工人读写；换工人前一定等 done
	tasks          backfillTasks
	media          backfillMedia
	queryStarted   time.Time
	queryRemaining int
	queryActive    bool
	inQueryRound   bool // 仅历史工人读写；单段插队期间父轮次仍在执行。
}

func (a *App) openBackfill() error {
	if u, off := a.set.Unavailable()["qq"]; off {
		a.logf("QQ 的库没打开，补录不启动：%s", u.Reason)
		return nil
	}
	st, err := a.set.For("qq")
	if err != nil {
		return err
	}
	if err := a.interruptAlignment(context.Background(), st); err != nil {
		return err
	}
	if !a.cfg.QQ.Backfill.Enabled {
		return nil
	}
	b := &qqBackfill{settle: a.opt.BackfillSettle, st: st, intake: a.intake, source: a.adapter, clock: a.clock(), account: a.account, owner: string(a.cfg.QQ.Owner), logf: a.logf}
	if a.cfg.QQ.Backfill.Groups {
		for _, id := range a.cfg.QQ.Groups {
			b.groups = append(b.groups, string(id))
		}
	}
	a.backfill = b
	b.media.media, b.media.forwards = a.media, a.forwards
	if a.media != nil {
		a.media.SetBackfillNotify(st.NotifyBackfillMedia)
	}
	if a.forwards != nil {
		a.forwards.SetBackfillNotify(st.NotifyBackfillMedia)
	}
	return nil
}
func (a *App) closeBackfill() {
	if a.backfill != nil {
		a.backfill.stop()
	}
}

func (b *qqBackfill) connected(ctx context.Context) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.aliveLocked() {
		return
	}
	ctx, b.cancel = context.WithCancel(ctx)
	b.done = make(chan struct{})
	b.tasks.Lock()
	b.initTasksLocked()
	b.tasks.online = true
	b.tasks.Unlock()
	cancel := b.cancel
	go func() {
		defer close(b.done)
		defer b.endQueries()
		defer b.cancelQueued(ctx)
		defer cancel()
		if err := b.run(ctx); err != nil && ctx.Err() == nil {
			b.logf("QQ 补录停止：%v", err)
		}
	}()
}

// 工人是否还在跑。run 因错误退出后 cancel 仍非空，但 done 已关；这时把它当作已停，
// 并清掉残留的 cancel，下一次 connected 才能重新开始（调用者须持 b.mu）。
func (b *qqBackfill) aliveLocked() bool {
	if b.cancel == nil {
		return false
	}
	select {
	case <-b.done:
		b.cancel = nil
		return false
	default:
		return true
	}
}
func (b *qqBackfill) stop() {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cancel != nil {
		b.cancel()
		<-b.done
		b.cancel = nil
	}
}
func (b *qqBackfill) wait(ctx context.Context, d time.Duration) error {
	t := b.clock.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C():
		return ctx.Err()
	}
}
func (b *qqBackfill) run(ctx context.Context) error {
	deadline := b.clock.Now().Add(time.Minute)
	for !b.source.Ready() {
		if !b.clock.Now().Before(deadline) {
			return errors.New("等待连接就绪超过 60 秒")
		}
		if err := b.wait(ctx, time.Second); err != nil {
			return err
		}
	}
	// 媒体就绪即补扫，不等历史防抖；重连即使延后历史轮次，也不能漏掉媒体恢复。
	b.beginQueries()
	b.resweepMedia()
	settle := b.settle
	if settle == 0 {
		settle = backfillSettle
	}
	if err := b.wait(ctx, settle); err != nil {
		return err
	}
	// 抖动期间可以推迟查询，但补过重连空档之前绝不能先推水位，否则几秒的洞会永久跳过。
	first := true
	for {
		if b.source.Ready() {
			gaps, err := b.st.ChannelGaps(ctx, "qq")
			if err != nil {
				return err
			}
			open, due := false, false
			for _, g := range gaps {
				if b.allowed(g.Conversation) && automaticGap(g) {
					open = true
					if b.clock.Now().UnixMilli()-g.LastTryMS >= backfillRetry.Milliseconds() {
						due = true
					}
				}
			}
			if (first && (b.lastRound.IsZero() || open || b.clock.Now().Sub(b.lastRound) >= backfillRetry)) || due {
				trigger := "connected"
				if !b.lastRound.IsZero() {
					trigger = "reconnect"
				}
				if err := b.executeAccount(ctx, trigger, nil); err != nil {
					return err
				}
				first = false
			} else if !first {
				if err := b.st.SetQQCheckedThrough(ctx, b.account, b.clock.Now().Add(-backfillOverlap).UnixMilli()); err != nil {
					return err
				}
			}
		}
		if err := b.runQueued(ctx); err != nil {
			return err
		}
		t := b.clock.NewTimer(time.Minute)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-b.tasks.wake:
			t.Stop()
		case <-t.C():
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
}

func (b *qqBackfill) allowed(name string) bool {
	c, err := types.ParseConversation(name)
	if err != nil || c.Channel != "qq" {
		return false
	}
	if c.Scene == types.ScenePrivate {
		return c.Self == b.account
	}
	for _, g := range b.groups {
		if c.ID == g {
			return true
		}
	}
	return false
}

type backfillTarget struct {
	conv   types.Conversation
	skip   bool
	manual bool
}

// 升级恢复的 blocked 尚未扫描（last_try_ms=0）；其余受阻旧段只接受手动请求。
func automaticGap(g store.ChannelGap) bool {
	return g.AcknowledgedMS == 0 && (g.State == "open" || g.State == "blocked" && g.LastTryMS == 0)
}

func (b *qqBackfill) targets(ctx context.Context, gaps []store.ChannelGap, now int64) ([]backfillTarget, error) {
	activity, err := b.st.QQActivities(ctx, b.account)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []backfillTarget
	add := func(c types.Conversation, skip bool) {
		name := c.String()
		if seen[name] || !b.allowed(name) {
			return
		}
		seen[name] = true
		out = append(out, backfillTarget{conv: c, skip: skip})
	}
	if b.owner != "" {
		add(types.Conversation{Channel: "qq", Scene: types.ScenePrivate, Self: b.account, ID: b.owner}, false)
	}
	// 上轮预算没轮到的先接着补，不能又让已翻过的会话用完预算。
	for priority := range 2 {
		for _, g := range gaps {
			if (g.Reason == "budget") != (priority == 0) {
				continue
			}
			if automaticGap(g) && now-g.LastTryMS >= backfillRetry.Milliseconds() {
				if c, err := types.ParseConversation(g.Conversation); err == nil {
					add(c, false)
				}
			}
		}
	}
	n := 0
	for _, a := range activity {
		c, err := types.ParseConversation(a.Conversation)
		if err != nil {
			return nil, err
		}
		if c.Scene == types.ScenePrivate && !seen[a.Conversation] {
			add(c, n >= 30)
			n++
		}
	}
	for _, a := range activity {
		c, err := types.ParseConversation(a.Conversation)
		if err != nil {
			return nil, err
		}
		if c.Scene == types.SceneGroup {
			add(c, false)
		}
	}
	for _, id := range b.groups {
		add(types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: id}, false)
	}
	return out, nil
}

func (b *qqBackfill) round(ctx context.Context) error {
	started := b.clock.Now()
	checked, err := b.st.QQCheckedThrough(ctx, b.account, started.UnixMilli())
	if err != nil {
		return err
	}
	start := checked - backfillOverlap.Milliseconds()
	gaps, err := b.st.ChannelGaps(ctx, "qq")
	if err != nil {
		return err
	}
	targets, err := b.targets(ctx, gaps, started.UnixMilli())
	if err != nil {
		return err
	}
	b.beginQueries()
	b.inQueryRound = true
	defer func() { b.inQueryRound = false }()
	b.progress(func(j *api.BackfillJob) { j.Pending = len(targets) })
	results := make([]store.QQBackfillResult, 0, len(targets))
	for _, t := range targets {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !b.allowed(t.conv.String()) {
			continue
		}
		b.progress(func(j *api.BackfillJob) { j.CurrentConversation = backfillKey(t.conv) })
		from := start
		for _, g := range gaps {
			if g.Conversation == t.conv.String() && !automaticGap(g) && g.State != "filled" && g.FromMS <= from && from <= g.ToMS {
				from = g.ToMS + 1
			}
		}
		for _, g := range gaps {
			if g.Conversation == t.conv.String() && automaticGap(g) && started.UnixMilli()-g.LastTryMS >= backfillRetry.Milliseconds() {
				from = min(from, g.FromMS)
			}
		}
		if from > started.UnixMilli() {
			b.progress(func(j *api.BackfillJob) { j.Checked++; j.Pending-- })
			continue
		}
		coverage := backfillCoverage{reason: "budget", from: from, to: started.UnixMilli()}
		if !t.skip {
			coverage, err = b.conversation(ctx, t, from, started.UnixMilli())
			if err != nil {
				return err
			}
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		result := store.QQBackfillResult{Conversation: t.conv.String(), FromMS: from, Covered: coverage.covered, CoveredFromMS: coverage.coveredFrom, CoveredToMS: coverage.coveredTo}
		// 只有未扫描区间进入自动结算；受阻旧段不被新尾段复活。
		for i, g := range gaps {
			if g.Conversation != t.conv.String() || !automaticGap(g) {
				continue
			}
			due := started.UnixMilli()-g.LastTryMS >= backfillRetry.Milliseconds()
			if coverage.covered && from <= g.FromMS && coverage.coveredFrom <= g.FromMS && coverage.coveredTo >= g.ToMS {
				g.State = "filled"
			} else if due {
				g.State, g.Reason = "blocked", coverage.reason
				if coverage.reason == "budget" {
					g.State = "open"
				} else {
					g.Tries++
				}
			} else {
				continue
			}
			g.LastTryMS, g.UpdatedMS = started.UnixMilli(), started.UnixMilli()
			result.Updates = append(result.Updates, g)
			gaps[i] = g
		}
		if coverage.reason != "" {
			// 老缺口已经单独保存；到页数上限不能把它和新窗口之间整天再记成一个洞。
			if coverage.latest || !coverage.covered {
				coverage.from = max(coverage.from, start)
			}
			intervals := [][2]int64{{coverage.from, coverage.to}}
			if coverage.covered {
				intervals = nil
				if coverage.from < coverage.coveredFrom {
					intervals = append(intervals, [2]int64{coverage.from, min(coverage.to, coverage.coveredFrom)})
				}
				if coverage.to > coverage.coveredTo {
					intervals = append(intervals, [2]int64{max(coverage.from, coverage.coveredTo), coverage.to})
				}
			}
			// 已扫描受阻或放弃的旧段保留原行；新窗口只登记尚未记录的部分。
			for _, g := range gaps {
				if g.Conversation != t.conv.String() || automaticGap(g) || g.State == "filled" {
					continue
				}
				for i := len(intervals) - 1; i >= 0; i-- {
					interval := intervals[i]
					if interval[1] < g.FromMS || interval[0] > g.ToMS {
						continue
					}
					intervals = append(intervals[:i], intervals[i+1:]...)
					if interval[0] < g.FromMS {
						intervals = append(intervals, [2]int64{interval[0], g.FromMS - 1})
					}
					if interval[1] > g.ToMS {
						intervals = append(intervals, [2]int64{g.ToMS + 1, interval[1]})
					}
				}
			}
			for _, interval := range intervals {
				if interval[0] > interval[1] {
					continue
				}
				state := "blocked"
				if coverage.reason == "budget" {
					state = "open"
				}
				result.Gaps = append(result.Gaps, store.ChannelGap{Channel: "qq", Conversation: t.conv.String(), FromMS: interval[0], ToMS: interval[1], Reason: coverage.reason, State: state, LastTryMS: started.UnixMilli()})
			}
		}
		results = append(results, result)
		b.progress(func(j *api.BackfillJob) { j.Checked++; j.Pending-- })
		if err := b.drainGaps(ctx); err != nil {
			return err
		}
	}
	// 消息已逐条提交；缺口、连续证明和检查位置统一结算，取消或失败时一并保留旧状态。
	if err := b.st.SettleQQBackfill(ctx, b.account, started.UnixMilli(), results); err != nil {
		return err
	}
	b.logf("QQ 补录一轮完成：%d 个会话", len(targets))
	b.lastRound = started
	open, abandoned, _, err := b.st.AlignmentGapCounts(ctx, b.account, b.allowed)
	if err != nil {
		return err
	}
	b.progress(func(j *api.BackfillJob) { j.Messages = "completed"; j.CompleteCoverage = open+abandoned == 0 })
	return nil
}

type backfillCoverage struct {
	reason                 string
	from, to               int64
	covered                bool
	latest                 bool
	coveredFrom, coveredTo int64
}

// 扫描与证明分开；群可用两端之间无空号的协议序列证明，私聊不混用发送方序号。
func (b *qqBackfill) conversation(ctx context.Context, t backfillTarget, start, end int64) (backfillCoverage, error) {
	coverage := backfillCoverage{reason: "page-cap", from: start, to: end}
	if !b.source.Caps().History {
		coverage.reason = "unsupported"
		return coverage, nil
	}
	seen := map[string]struct{ at, seq int64 }{}
	seenSeq := map[int64]bool(nil)
	if t.conv.Scene == types.SceneGroup {
		seenSeq = make(map[int64]bool)
	}
	seqOK, minSeq, maxSeq := seenSeq != nil, int64(0), int64(0)
	var all []types.Inbound
	anchor := ""
	var err error
	if t.manual && t.conv.Scene == types.SceneGroup {
		anchor, _, err = b.st.GapRightAnchor(ctx, t.conv.String(), end)
		if err != nil {
			return coverage, err
		}
	}
	initialAnchor := anchor
	oldest, newest := end, int64(0)
	latest := false
	continuous, pagesOK := true, true
	for page := range backfillPageCap {
		if err := ctx.Err(); err != nil {
			return coverage, err
		}
		batch, err := b.source.HistoryPage(ctx, b.account, t.conv, anchor, backfillPageSize)
		if ctx.Err() != nil {
			return coverage, ctx.Err()
		}
		if err != nil {
			coverage.reason = "call-failed"
			if errors.Is(err, types.ErrQueryBudget) {
				coverage.reason = "budget"
			}
		}
		if !errors.Is(err, types.ErrQueryBudget) {
			b.progress(func(j *api.BackfillJob) { j.Pages++ })
		}
		continuous = continuous && batch.Continuous
		if page == 0 && initialAnchor == "" {
			latest = batch.Latest
		}
		fresh, joined, consistent := 0, anchor == "", true
		var first *types.Inbound
		for i := range batch.Messages {
			in := &batch.Messages[i]
			at := in.Time.UnixMilli()
			if in.PlatformID == anchor {
				joined = true
			}
			if previous, ok := seen[in.PlatformID]; ok {
				consistent = consistent && previous.at == at && previous.seq == in.MessageSeq
				if previous.at == at && at >= start && (!t.manual || at <= end) {
					b.progress(func(j *api.BackfillJob) { j.Duplicates++ })
				}
				continue
			}
			seen[in.PlatformID] = struct{ at, seq int64 }{at, in.MessageSeq}
			if seenSeq != nil {
				s := in.MessageSeq
				if s <= 0 || seenSeq[s] {
					seqOK = false
				} else {
					seenSeq[s] = true
					if minSeq == 0 || s < minSeq {
						minSeq = s
					}
					maxSeq = max(maxSeq, s)
				}
			}
			fresh++
			if page == 0 {
				newest = max(newest, at)
			}
			// 同秒可以继续换锚点；只回较新消息不能把下一次锚点往后挪。
			if (anchor == "" || at <= oldest) && (first == nil || in.Time.Before(first.Time)) {
				first = in
			}
			if at >= start && (!t.manual || at <= end) {
				all = append(all, *in)
			}
		}
		// 首页没有可用锚点就没有证明区间；后页普通停滞仍可保留此前的有效证据。
		pagesOK = pagesOK && err == nil && joined && consistent && (page > 0 || first != nil)
		if err != nil {
			break
		}
		if !joined || !consistent {
			coverage.reason = "broken-page"
			// 手动取历史不以覆盖证据为门槛；有更旧锚点时仍在原五页预算内继续保存。
			if !t.manual {
				break
			}
		}
		if fresh == 0 || first == nil {
			coverage.reason = "stalled"
			if page == 0 {
				coverage.reason = "no-anchor"
			}
			break
		}
		oldest = min(oldest, first.Time.UnixMilli())
		if oldest <= start {
			coverage.reason = ""
			break
		}
		if t.conv.Scene == types.ScenePrivate {
			break
		}
		anchor = first.PlatformID
	}
	// 所有扫描出口共用最终证据；后页出错或序号失效时不能沿用前页的覆盖结论。
	coverage.covered = pagesOK && (latest && continuous || seqOK && len(seenSeq) > 1 && maxSeq-minSeq == int64(len(seenSeq)-1))
	coverage.coveredFrom, coverage.coveredTo = oldest, newest
	if latest {
		coverage.coveredTo = end
	}
	coverage.latest = latest
	coverage.to = max(start, oldest)
	if coverage.covered && !latest {
		// 序号只证明收到的两端之间；旧洞填平后，未知新尾段仍须单独可见。
		coverage.from, coverage.to = start, end
		if coverage.coveredFrom > start || coverage.coveredTo < end {
			coverage.reason = "unverified-history"
		}
	}
	if !coverage.covered {
		// 首尾时间和锚点只能说明扫到哪里，不能从待补区间扣掉任何中段。
		coverage.from, coverage.to = start, end
		if coverage.reason == "" {
			coverage.reason = "unverified-history"
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Time.Before(all[j].Time) })
	for _, in := range all {
		if err := ctx.Err(); err != nil {
			return coverage, err
		}
		tk, err := b.intake.Submit(ctx, in, channel.Options{Backfill: true})
		if err != nil {
			return coverage, err
		}
		res, waitErr := tk.Wait(ctx)
		if waitErr != nil && ctx.Err() != nil {
			// 已经接受的 Ticket 要收尾，并把实际提交数保留在 canceled 任务里。
			res, err = tk.Wait(context.Background())
			waitErr = errors.Join(waitErr, err)
		}
		if res.Dropped {
			return coverage, api.BackfillFailure("invalid")
		}
		if res.Seq != 0 && !res.Dropped {
			b.progress(func(j *api.BackfillJob) {
				if res.Duplicate {
					j.Duplicates++
				} else {
					j.Inserted++
				}
			})
		}
		if waitErr != nil {
			return coverage, waitErr
		}
	}
	return coverage, nil
}
