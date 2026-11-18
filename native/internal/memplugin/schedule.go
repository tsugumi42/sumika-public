package memplugin

// 后台整理的排程（规格-记忆-v2 13.2.5，W5）：什么时候整理、几个会话同时整理、失败了什么时候再试、插件停掉时怎么收。
// 一批怎么整理在 memory.SummarizeBatch，库里的游标和退避在 memory/summary_state.go，这里只管"排"。
//
// 触发只听"她看到的新事件"的通知（Notify），不挂 OnSettled：闸门没放行的消息不进回合，Settled 不会发（13.2.5、13.2.14 的禁止清单）。
// 通知回调里只记一笔、重置计时器就返回，模型调用在自己的协程里。
//
// 同一个 (角色, 会话) 一次只有一个协程在干活（串行）；干活期间来的请求合成一个待跑标记，跑完再看一眼。
// 写库的锁在角色库那边（remember 和整理都拿它），这里的串行只是不让同一个会话同时调两次模型

import (
	"errors"
	"sync"
	"time"

	"sumika-native/internal/memory"
)

// 排程的起步值（规格-记忆-v2 13.2.5：10 分钟和 40 条是研究铃音的建议，其余是我定的）
const (
	defaultQuiet          = 10 * time.Minute // 一个会话安静这么久，把攒着的整理掉
	defaultThreshold      = 40               // 攒够这么多条立刻整理
	defaultParallel       = 2                // 同时在调模型的会话数
	defaultStartupBatches = 20               // 每次启动最多排这么多批（含续跑），再多的等新通知
	defaultImageRetry     = time.Minute      // 在没识完的图前面截断了，隔多久再看一眼
)

// skey 是一个 (角色, 会话)
type skey struct{ char, conv string }

// slot 是一个 (角色, 会话) 的排程状态，字段都由 sched.mu 保护
type slot struct {
	k skey

	// 两个计时：quiet 是"安静 10 分钟"，每条新通知重置；retry 是退避或等识图到期再试，和新通知无关。
	// 代号：旧的回调晚到（停的时候它已经在跑了），代号对不上就当没来
	quietStop, retryStop func() bool
	quietGen, retryGen   uint64

	run sync.Mutex // 同一个会话的 SummarizeBatch 一次一个：排程的整理和压缩前的整理互相等（先拿信号量再拿它，压缩前的只拿它）

	pumping   bool // 有一个协程在替它干活（数条数或整理），同一个会话至多一个
	wantCheck bool // 来过新通知，还没数过库里攒了几条
	wantRun   bool // 有人要整理：几个请求合成一个
	force     bool // 合成的请求里有 force
	startup   bool // 合成的请求里有启动扫描排的：续跑的每一批也占启动的名额
	dirty     bool // 上一次整理以后来过新通知：停了的会话靠它重新开
	since     int  // 上一次数库以后来了几条通知
	counted   int  // 上一次数库数到几条；-1 = 不知道
}

// scanState 是启动扫描对一个角色的结果。mu 管 done 和 known，扫的时候持着，别的会话要用先等
type scanState struct {
	mu    sync.Mutex
	done  bool
	known map[string]bool // 启动时这个角色就有的会话；没在里面又没有游标的，是启动以后才有的新会话，从头整理
}

type sched struct {
	l *life

	mu      sync.Mutex
	slots   map[skey]*slot
	budget  int  // 启动还能排几批
	stopped bool // stop 以后不再排任何东西

	sem chan struct{} // 同时整理的会话数上限

	scanMu    sync.Mutex
	scan      map[string]*scanState // nil = 没配启动扫描（Options.Characters 为空）；没配的时候没有游标的会话不动，免得误回填
	scanning  chan struct{}         // 启动扫描的协程退出时关；没扫就是 nil
	warnedRO  map[string]bool       // 写入没开的角色，日志只记一次
	warnedNew map[string]bool
}

func newSched(l *life) *sched {
	o := l.p.opt
	return &sched{l: l, slots: map[skey]*slot{}, budget: o.StartupBatches, sem: make(chan struct{}, o.Parallel),
		warnedRO: map[string]bool{}, warnedNew: map[string]bool{}}
}

func (s *sched) opt() Options { return s.l.p.opt }

// memory 取这个角色开了后台整理的记忆；没有、没开写入、没开整理就是 nil，那个角色的整理什么都不做
func (s *sched) memory(char string) *memory.Memory {
	m := s.opt().Memories(char)
	if m == nil || !m.Writable() || !m.SummarizeEnabled() {
		return nil
	}
	return m
}

// memoryFor 在 memory 之外再查这个会话的场合放不放行（memory.write.places，规格-记忆写入上线 W5）。
// 通知、启动扫描、整理按钮和归档（request）、压缩前（beforeCompact）、立游标（ensureCursor）五条路都经过它或下面的 PlaceAllows，
// 不放行的会话排不上、立不了游标。SummarizeBatch 里还有同一个判断挡一次，这里只是提前挡，省得空转
func (s *sched) memoryFor(char, conv string) *memory.Memory {
	m := s.memory(char)
	if m == nil || !m.PlaceMayWrite(conv) {
		return nil
	}
	return m
}

func (s *sched) slotLocked(k skey) *slot {
	sl, ok := s.slots[k]
	if !ok {
		sl = &slot{k: k, counted: -1}
		s.slots[k] = sl
	}
	return sl
}

// ───────────── 入口 ─────────────

// notify：这个角色在这个会话里看到了新事件。只记一笔、重置计时器，立刻返回（钩子里不调模型，13.2.5）
func (l *life) notify(char, conv string) {
	if !l.enter() {
		return
	}
	defer l.wg.Done()
	s := l.s
	if s.memoryFor(char, conv) == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	sl := s.slotLocked(skey{char, conv})
	sl.dirty = true
	sl.since++
	s.armLocked(sl, false, s.opt().Quiet)
	// 数库要读最多 200 行，不能每条通知都数：上次数到 n 条，再来 40-n 条通知才可能攒够
	if sl.counted < 0 || sl.counted+sl.since >= s.opt().Threshold {
		sl.wantCheck = true
	}
	s.kickLocked(sl)
}

// request：有人要整理这个会话（压缩前、整理按钮、归档用 force；不看退避，B4）。和别的请求合成一个
func (l *life) request(char, conv string, force bool) {
	if !l.enter() {
		return
	}
	defer l.wg.Done()
	s := l.s
	if s.memoryFor(char, conv) == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	sl := s.slotLocked(skey{char, conv})
	sl.wantRun = true
	sl.force = sl.force || force
	s.kickLocked(sl)
}

// ───────────── 计时 ─────────────

// armLocked 重新计时：旧的作废（retry 为真是退避的那个，否则是安静的那个）。持着 s.mu 调
func (s *sched) armLocked(sl *slot, retry bool, d time.Duration) {
	stop, gen := &sl.quietStop, &sl.quietGen
	if retry {
		stop, gen = &sl.retryStop, &sl.retryGen
	}
	if *stop != nil {
		(*stop)()
	}
	*gen++
	g := *gen
	*stop = s.opt().After(d, func() { s.timerFired(sl, retry, g) })
}

// timerFired：到点了。只标记"要整理"，真干活交给协程；到点时不再复查门槛（Bun 的做法，13.2.5）
func (s *sched) timerFired(sl *slot, retry bool, gen uint64) {
	if !s.l.enter() {
		return
	}
	defer s.l.wg.Done()
	s.mu.Lock()
	defer s.mu.Unlock()
	cur, stop := sl.quietGen, &sl.quietStop
	if retry {
		cur, stop = sl.retryGen, &sl.retryStop
	}
	if s.stopped || cur != gen {
		return
	}
	*stop = nil
	sl.wantRun = true
	s.kickLocked(sl)
}

// kickLocked：有活就叫起这个会话的协程（已经有一个在干就不叫，它跑完会看到新标记）。持着 s.mu 调
func (s *sched) kickLocked(sl *slot) {
	if sl.pumping || !(sl.wantRun || sl.wantCheck) || !s.l.enter() {
		return
	}
	sl.pumping = true
	go s.pump(sl)
}

// pump 是一个会话的协程：整理优先，其次数库；没活了就退出。同一个会话至多一个，所以它们是串行的
func (s *sched) pump(sl *slot) {
	defer s.l.wg.Done()
	for {
		s.mu.Lock()
		if s.stopped || s.l.ctx.Err() != nil {
			sl.pumping = false
			s.mu.Unlock()
			return
		}
		switch {
		case sl.wantRun:
			force, startup, dirty := sl.force, sl.startup, sl.dirty
			sl.wantRun, sl.force, sl.startup, sl.dirty = false, false, false, false
			s.mu.Unlock()
			s.runBatch(sl, force, startup, dirty)
		case sl.wantCheck:
			sl.wantCheck, sl.since = false, 0
			s.mu.Unlock()
			s.check(sl)
		default:
			sl.pumping = false
			s.mu.Unlock()
			return
		}
	}
}

// ───────────── 干活 ─────────────

// check 数库里攒了几条，够了就排一次整理。通知只用来唤醒，数以库为准：通知比入库先到，到点读库仍能看到这条（边界 ⑧）
func (s *sched) check(sl *slot) {
	m := s.memoryFor(sl.k.char, sl.k.conv)
	if m == nil || !s.ensureCursor(m, sl.k) {
		return
	}
	n, _, err := m.Pending(s.l.ctx, sl.k.conv)
	if err != nil {
		if s.l.ctx.Err() == nil {
			s.opt().Logf("记忆[%s]：数不出攒了几条（原因 %s）", sl.k.char, memory.FailureCode(err))
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sl.counted = n
	if n >= s.opt().Threshold {
		sl.wantRun = true
	}
}

// runBatch 整理一批。force 不看退避（B4）；排队的整理在这里才看退避（B5，SummarizeBatch 开头）
func (s *sched) runBatch(sl *slot, force, startup, dirty bool) {
	l := s.l
	m := s.memoryFor(sl.k.char, sl.k.conv)
	if m == nil || !s.ensureCursor(m, sl.k) {
		return
	}
	select {
	case s.sem <- struct{}{}:
	case <-l.ctx.Done():
		return
	}
	defer func() { <-s.sem }()
	if dirty && !force {
		// 停了的会话来了新通知：再给一次机会（这一次再失败就又停）
		if _, err := m.ReopenSummary(l.ctx, sl.k.conv); err != nil && l.ctx.Err() == nil {
			s.opt().Logf("记忆[%s]：停了的整理没能重新开（原因 %s）", sl.k.char, memory.FailureCode(err))
		}
	}
	sl.run.Lock()
	res, err := m.SummarizeBatch(l.ctx, sl.k.conv, force)
	sl.run.Unlock()
	if err != nil {
		if l.ctx.Err() != nil || errors.Is(err, memory.ErrClosed) {
			return // 插件停了、库关了：不记成失败
		}
		s.failed(sl, m, err)
		return
	}
	s.settled(sl, m, res, startup)
}

// failed 记一次失败：次数加一、定下次时间（写在库里，重启不清零），到点自动再试，不等新消息；满 MaxFailures 次就停
func (s *sched) failed(sl *slot, m *memory.Memory, err error) {
	l := s.l
	st, e2 := m.NoteFailure(l.ctx, sl.k.conv)
	if e2 != nil {
		if l.ctx.Err() == nil {
			s.opt().Logf("记忆[%s]：整理失败（原因 %s），失败次数没记上（原因 %s）", sl.k.char, memory.FailureCode(err), memory.FailureCode(e2))
		}
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sl.counted = -1
	if st.Failures >= memory.MaxFailures {
		s.opt().Logf("记忆[%s]：整理失败（原因 %s），连着失败 %d 次，先停了，等新消息再开", sl.k.char, memory.FailureCode(err), st.Failures)
		return
	}
	s.opt().Logf("记忆[%s]：整理失败（原因 %s），连着第 %d 次，%s 以后再试", sl.k.char, memory.FailureCode(err), st.Failures, memory.BackoffDelay(st.Failures))
	s.retryAtLocked(sl, st.RetryAt)
}

// retryAtLocked：到库里记的那个时间再试，至少隔一秒（免得两边的钟差一点就转成空转）
func (s *sched) retryAtLocked(sl *slot, at int64) {
	d := time.UnixMilli(at).Sub(s.opt().Now())
	s.armLocked(sl, true, max(d, time.Second))
}

// settled 看一批的结局，决定接着做什么
func (s *sched) settled(sl *slot, m *memory.Memory, res memory.BatchResult, startup bool) {
	var st memory.SummaryState
	if res.Skipped == memory.SkipBackoff {
		st, _, _ = m.SummaryState(s.l.ctx, sl.k.conv)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sl.counted, sl.since = -1, 0
	switch res.Skipped {
	case memory.SkipBackoff:
		// 排队的整理轮到时还在退避：到点再来
		if st.RetryAt > 0 {
			s.retryAtLocked(sl, st.RetryAt)
		}
	case memory.SkipStopped, memory.SkipNoModel, memory.SkipGeneration, memory.SkipCursorMoved, memory.SkipNoCursor, memory.SkipPlace:
		// 停了等新通知；没选模型不算失败（B6），选好以后下一次触发照常整理；忘掉期间算的作废、游标被挪过：等下一次触发；
		// 场合没放行（W5）：不是失败，也不重排，等新通知再看一眼（放行名单是启动时定的，一次运行里不变，所以多半还是不放行）
	default: // 整理了，或者已整理过、没有可整理的
		if res.Counts[memory.CountWaitImages] > 0 {
			// 在没识完的图前面截断了：图到了或等够了 10 分钟再整理，不靠新通知（识图结果不是新事件）
			s.armLocked(sl, true, s.opt().ImageRetry)
		}
		if res.More {
			// 后面还有没读到的，接着排下一批。启动排的那一串每续一批也占一个名额，用完了就停，剩下的等新通知
			if !startup || s.reserveLocked() {
				sl.wantRun = true
				sl.startup = sl.startup || startup
			} else {
				s.opt().Logf("记忆[%s]：启动时能排的批数用完了，这个会话后面的等新通知（原因 startup_over_limit）", sl.k.char)
			}
		}
	}
}

// reserveLocked：占一个启动的名额（排的时候就占，免得一口气排出几百个再一个个丢）。持着 s.mu 调
func (s *sched) reserveLocked() bool {
	if s.budget <= 0 {
		return false
	}
	s.budget--
	return true
}

// ───────────── 启动扫描 ─────────────

// startScan 在 Setup 里调：登记要扫的角色，起协程去扫。没配 Characters 就不扫（此时没有游标的会话一律不动，免得把整段历史当新消息整理）
func (l *life) startScan() {
	s := l.s
	if l.p.opt.Characters == nil {
		return
	}
	chars := l.p.opt.Characters()
	s.scanMu.Lock()
	s.scan = map[string]*scanState{}
	for _, c := range chars {
		s.scan[c] = &scanState{}
	}
	s.scanning = make(chan struct{})
	s.scanMu.Unlock()
	if !l.enter() {
		close(s.scanning)
		return
	}
	go func() {
		defer l.wg.Done()
		defer close(s.scanning)
		for _, c := range chars {
			if l.ctx.Err() != nil {
				return
			}
			m := s.memory(c)
			if m == nil {
				s.warnReadOnly(c)
				continue
			}
			// 整理提示词被改得没有候选块：证据功能整个关着，启动时就说一声（每批整理前 memory 自己还会查，这里是早点让人知道）
			if err := m.CheckSummaryPrompt(); errors.Is(err, memory.ErrPromptStale) {
				s.opt().Logf("记忆[%s]：%v", c, err)
			}
			s.sweepConfirm(m, c)
			s.scanChar(m, c)
		}
	}()
}

// sweepConfirm：自动提升打开着的话，启动时对已经攒够证据的待确认事实补跑一遍（memory.ConfirmSweep，规格-记忆写入上线 W3b）：
// 记账模式下攒够了的候选不用各自再等一次证据，才和主人看过的报告对得上。没打开自动提升时它什么都不做。日志只写计数
func (s *sched) sweepConfirm(m *memory.Memory, char string) {
	r, err := m.ConfirmSweep(s.l.ctx)
	if err != nil {
		if s.l.ctx.Err() == nil {
			s.opt().Logf("记忆[%s]：启动时补提升待确认没做完（原因 %s）", char, memory.FailureCode(err))
		}
		return
	}
	if r.Checked > 0 || r.Skipped > 0 {
		s.opt().Logf("记忆[%s]：启动时补提升待确认：看了 %d 条，提升 %d 条，%d 条想取代的旧事实受保护、没提升（confirm_blocked_protected），%d 条范围不在放行的场合里、没碰（sweep_out_of_places）", char, r.Checked, r.Confirmed, r.Blocked, r.Skipped)
	}
}

// warnReadOnly：这个角色不整理，日志只记一次。只开了 remember、整理没开（第一档）是正常的，别和 memwrite 刚记的"写入已开"打架
func (s *sched) warnReadOnly(char string) {
	s.scanMu.Lock()
	first := !s.warnedRO[char]
	s.warnedRO[char] = true
	s.scanMu.Unlock()
	if !first {
		return
	}
	m := s.opt().Memories(char)
	switch {
	case m == nil:
		s.opt().Logf("记忆[%s]：这个角色没有记忆库（no_memory_db），后台整理不管它", char)
	case m.Writable():
		s.opt().Logf("记忆[%s]：后台整理没开，只有 remember 在写", char)
	default:
		s.opt().Logf("记忆[%s]：记忆库是只读的、写入没开（write_disabled），后台整理不管它", char)
	}
}

// scanState 取这个角色的启动扫描状态；启动以后才出现的角色（不在 Characters 里）算一份扫完的、什么会话都没有的
func (s *sched) scanStateOf(char string) *scanState {
	s.scanMu.Lock()
	defer s.scanMu.Unlock()
	if s.scan == nil {
		return nil
	}
	sc, ok := s.scan[char]
	if !ok {
		sc = &scanState{done: true, known: map[string]bool{}}
		s.scan[char] = sc
	}
	return sc
}

// scanChar 扫一个角色：没有游标的会话从现在最新的 seq 立游标（不回填，模型调用 0 次）；
// 有游标而后面还有新的，排一批（最多排到启动的批数用完）。扫没成功（读不到会话）下次有这个角色的通知时再扫
func (s *sched) scanChar(m *memory.Memory, char string) {
	sc := s.scanStateOf(char)
	if sc == nil {
		return
	}
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if sc.done {
		return
	}
	ctx := s.l.ctx
	heads, err := m.Conversations(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.opt().Logf("记忆[%s]：启动扫描读不到会话（原因 %s）", char, memory.FailureCode(err))
		}
		return
	}
	known := make(map[string]bool, len(heads))
	var queue []string
	inited, notAllowed := 0, 0
	for _, h := range heads {
		known[h.Conversation] = true
		// 场合没放行的会话：不立游标（不然以后把它的场合加进 places，会从 0 回填整段历史）、不排批，但算"启动时就有的会话"
		allowed, err := m.PlaceAllows(ctx, h.Conversation)
		if err != nil {
			if ctx.Err() == nil {
				s.opt().Logf("记忆[%s]：启动扫描没扫完，认不出一个会话是不是主人的私聊（原因 %s）", char, memory.FailureCode(err))
			}
			return
		}
		if !allowed {
			notAllowed++
			continue
		}
		st, ok, err := m.SummaryState(ctx, h.Conversation)
		if err == nil && !ok {
			var made bool
			if made, err = m.InitCursor(ctx, h.Conversation, h.LastSeq); made {
				inited++
			}
		} else if err == nil && h.LastSeq > st.LastSeq {
			queue = append(queue, h.Conversation)
		}
		if err != nil {
			if ctx.Err() == nil {
				s.opt().Logf("记忆[%s]：启动扫描没扫完（原因 %s）", char, memory.FailureCode(err))
			}
			return
		}
	}
	sc.known, sc.done = known, true
	s.mu.Lock()
	defer s.mu.Unlock()
	over := 0
	for _, conv := range queue {
		if !s.reserveLocked() {
			over++
			continue
		}
		sl := s.slotLocked(skey{char, conv})
		sl.wantRun, sl.startup = true, true
		s.kickLocked(sl)
	}
	s.opt().Logf("记忆[%s]：启动扫描 %d 个会话，%d 个场合没放行（place_skipped），%d 个没有游标的从现在起整理（不回填，startup_no_backfill），%d 个有没整理的排了批，%d 个等新通知（startup_over_limit）",
		char, len(heads), notAllowed, inited, len(queue)-over, over)
}

// ensureCursor：这个会话有游标才能整理。没有的：启动时就有的会话扫描已经立过了；启动以后才有的新会话从头整理（从 seq 0 立，
// 它的第一条消息也是新的，不能当历史跳过）；还没扫到这个角色就先扫；没配启动扫描就不动，免得误回填。返回现在有没有游标
func (s *sched) ensureCursor(m *memory.Memory, k skey) bool {
	ctx := s.l.ctx
	// 场合没放行的会话（含名单只有 owner-private 时别人的私聊）不立游标、不算有游标：先问这个，再看库里有没有
	if allowed, err := m.PlaceAllows(ctx, k.conv); err != nil || !allowed {
		if err != nil && ctx.Err() == nil {
			s.opt().Logf("记忆[%s]：认不出一个会话是不是主人的私聊（原因 %s），先不整理它", k.char, memory.FailureCode(err))
		}
		return false
	}
	_, ok, err := m.SummaryState(ctx, k.conv)
	if err != nil {
		if ctx.Err() == nil {
			s.opt().Logf("记忆[%s]：读不到游标（原因 %s）", k.char, memory.FailureCode(err))
		}
		return false
	}
	if ok {
		return true
	}
	sc := s.scanStateOf(k.char)
	if sc == nil {
		s.scanMu.Lock()
		first := !s.warnedNew[k.char]
		s.warnedNew[k.char] = true
		s.scanMu.Unlock()
		if first {
			s.opt().Logf("记忆[%s]：有会话没有整理游标，又没配启动扫描，不整理它（免得把整段历史当新消息，startup_no_backfill）", k.char)
		}
		return false
	}
	s.scanChar(m, k.char)
	sc.mu.Lock()
	known, done := sc.known[k.conv], sc.done
	sc.mu.Unlock()
	if !done || known {
		return false // 没扫成，下一条通知再试；或扫过却没立上游标（出过错）
	}
	if _, err := m.InitCursor(ctx, k.conv, 0); err != nil {
		if ctx.Err() == nil {
			s.opt().Logf("记忆[%s]：新会话没能立游标（原因 %s）", k.char, memory.FailureCode(err))
		}
		return false
	}
	return true
}

// ───────────── 停掉 ─────────────

// stopTimers：停掉所有计时，之后不再排任何东西（停掉顺序的第 ② 步，13.2.5）。在途的协程由 life.stop 等
func (s *sched) stopTimers() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	for _, sl := range s.slots {
		if sl.quietStop != nil {
			sl.quietStop()
			sl.quietStop = nil
		}
		if sl.retryStop != nil {
			sl.retryStop()
			sl.retryStop = nil
		}
		sl.quietGen++
		sl.retryGen++
	}
}

// busy：还有协程在干活或有活没干（测试等它安静下来用）
func (s *sched) busy() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, sl := range s.slots {
		if sl.pumping || sl.wantRun || sl.wantCheck {
			return true
		}
	}
	return false
}
