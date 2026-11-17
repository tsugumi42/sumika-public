package turn

// 压缩（规格-Go后台 四点八 3f、8.2 A8）里不碰库、不调模型的部分：切在哪、摘要靠不靠得住。照 Bun 的 daemon/util/compact.ts，
// 它的测试（test/compact.test.ts）一起搬过来。原文永远在库里，这里只决定"以后给她看的那份"从哪接上

import (
	"context"
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"strings"
	"time"

	"sumika-native/internal/history"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm"
	"sumika-native/internal/llm/chat"
	"sumika-native/internal/modelcall"
	"sumika-native/internal/prompts"
	"sumika-native/internal/store"
	"sumika-native/internal/textclean"
	"sumika-native/internal/turnbudget"
)

// ───────────── 压缩本身（agent.ts runCompact） ─────────────

// 找切点最多往回看多少个事件（agent.ts runCompact 的 eventsSince 上限）
const compactEventsCap = 5000

// Compacted 是一次压缩的结局
type Compacted struct {
	EpochID string // 写进 context_epochs 的那一行；空 = 没写（前面没有可以压的、还没请求就被停下、读库出错）
	FromSeq int64  // 那一行的切点（快照之后从哪条原文接着带）
	OK      bool   // 成功了：下一轮换上新快照
	Err     string // 没成的原因（写了那一行的，error 里也是它）
	Stopped bool   // 等钩子、等摘要的时候这一轮被停下了
	Void    bool   // 写库之前核对不上（起点、快照、模型、代次变了），这一份作废，不算失败
}

// compactGuard 是后台压缩（提前压、空闲压）开始时记下的版本：写 context_epochs 之前逐项核对，任何一项变了就丢掉不写
// （照 Codex 的“捕获时记版本、换上时核验”，compaction_replacement.rs:35）。切点由 compactNow 算出来以后补上
type compactGuard struct {
	start        int64  // 会话的上下文起点（history.StartSeq）
	cut          int64  // 这一次的切点（compactNow 算出来以后填）
	latestID     string // 开始时最新的成功快照 ID（没有是空）
	model        string // 摘要请求用的模型
	modelVersion string // 那份配置的版本（换了模型设置就变了）
	gen          uint64 // 开始时这个会话的 staleGen（MarkStale、检查点回退、新话题都会让它变）
	job          *compactJob
}

// compactBaseline 是后台压缩开始时记下的起点和最新快照
func compactBaseline(st *store.Store, characterID, conversation string) (start int64, latestID string, err error) {
	if start, err = history.StartSeq(st.Events, conversation); err != nil {
		return 0, "", err
	}
	latest, err := history.LatestEpoch(st.Events, characterID, conversation)
	if err != nil {
		return 0, "", err
	}
	if latest != nil {
		latestID = latest.ID
	}
	return start, latestID, nil
}

// compactGuardBroken 在写库之前核对开始时记下的版本：返回空 = 还能写，非空 = 作废的原因。
// 起点、快照、模型读库和当前配置；会话代次和任务登记在锁里核（调用方不拿锁）
func (h *Hub) compactGuardBroken(st *store.Store, src compactSource, g *compactGuard) string {
	start, latestID, err := compactBaseline(st, src.b.CharacterID, src.b.Conversation)
	if err != nil {
		return "读历史起点或最新快照出错：" + err.Error()
	}
	if start != g.start {
		return "开始压缩以后会话的起点变了（开了新话题）"
	}
	if latestID != g.latestID {
		return "开始压缩以后已经有更新的摘要"
	}
	ck := convKey(src.b.CharacterID, src.b.Conversation)
	h.mu.Lock()
	gen, job := h.staleGen[ck], h.compactJobs[ck]
	h.mu.Unlock()
	if gen != g.gen {
		return "开始压缩以后会话重建过（检查点回退或别的线写了话）"
	}
	if g.job != nil && job != g.job {
		return "这一次已经不在压缩任务登记里"
	}
	curCfg, _ := h.modelSnapshot()
	if curCfg.ModelVersion != g.modelVersion {
		return "开始压缩以后换了模型设置"
	}
	if want := curCfg.Models[src.b.CharacterID]; want != "" && want != g.model {
		return "开始压缩以后换了模型"
	}
	return ""
}

// compactVars 是压缩提示词的变量；称呼空着用"所有者"（内部子运行重建出来的 Built 没经过卡）
func compactVars(b *Built, cut string) map[string]string {
	return map[string]string{"cut": store.JSSlice(cut, 60), "owner_name": b.ownerLabel()}
}

// compactSource 是压缩的材料：她在实例里看到的那份，去掉每轮参考信息、工具调用和结果、思考（8.1"听众和权限"：摘要不带
// 参考信息、不带高档工具结果，所有档都能用），上一份摘要留着（提示词要它在旧摘要的基础上更新）。Bun 拿整个实例（参考信息、
// 工具结果都在里面，规格 8.2 A13 写着"Bun 也红"）。要在这个会话的回合队列里取：那时实例不会被别的回合改。
// 实例的 Built、配置拷一份：后台提前压的时候下一轮已经开始了，refresh 会原地换模型和几道线，主人也可能正在改配置
type compactSource struct {
	cfg  Config
	b    *Built
	msgs []Msg // 过滤以后的消息（含开头的系统提示）
	// from、last 是实例里她看到的最早、最晚那条事件：找切点只在这中间。更晚的（这一轮、还排着队的那几批：已经存进库，她还没看到）
	// 不算——主窗口的回合一个接一个跑，排着队的一定比她看到的都晚；裁剪掉的（更早的）也不算
	from, last int64
	before     int // 压之前的估算（和硬门槛比的那个：上一次请求的真实输入和按实例估的取大的）
	work       kernel.Invocation
	// topic：本地私聊且在干活，用 compact-topic，材料带工具调用和结果（规格-工具历史恢复和压缩 2.2）。
	// toolCost 是每条说过的话后面跟着的工具步骤估算（找切点时算进去）；record 是追加在摘要末尾的累计工作记录
	topic    bool
	toolCost map[int64]int
	record   workRecord
}

func (l *Live) compactSource(cfg Config) compactSource {
	b := *l.b
	src := compactSource{cfg: cfg, b: &b, before: max(int(l.lastTotalInput), l.b.Fixed+conversationTokens(l.msgs))}
	if !l.noWorkFold {
		src.work = l.in.Identity
	}
	local := workHistoryEnabled(cfg, l.in)
	if local {
		src.record, src.topic = l.workRecordFor()
		// 本地私聊重建时会把工具步骤接回来（不管用哪份提示词），切点要把它们算进去，不然压完又被工具历史撑满
		src.toolCost = toolCostOf(l.msgs)
	}
	for _, m := range l.msgs {
		switch {
		case m.Role == "system" || m.Kind == KindSummary:
			src.msgs = append(src.msgs, m)
		case src.topic && (m.Kind == KindWork || m.Role == "toolResult" || (m.Role == "assistant" && len(m.Calls) > 0 && m.Stop == "")):
			// 工作场景留下工具步骤，思考和用量不带（这份只用来写摘要，用量留着会把输出上限估小）
			m.Thinking, m.Replay, m.HasThinking, m.Usage, m.Images, m.imageSeqs = "", chat.Replay{}, false, nil, nil, nil
			src.msgs = append(src.msgs, m)
			if m.Seq > 0 {
				if src.from == 0 || m.Seq < src.from {
					src.from = m.Seq
				}
				src.last = max(src.last, m.Seq)
			}
		case m.Kind != KindSaid || (m.Role != "user" && m.Role != "assistant"):
			// 参考信息、工具结果
		case m.Role == "assistant" && textclean.Trim(m.Text) == "":
			// 只调了工具、没说话的那一步
		default:
			if m.Role == "assistant" {
				m = Msg{Role: "assistant", Text: m.Text, HasText: true, Kind: KindSaid, Seq: m.Seq, Stop: m.Stop}
			}
			src.msgs = append(src.msgs, m)
			if m.Seq > 0 {
				if src.from == 0 || m.Seq < src.from {
					src.from = m.Seq
				}
				src.last = max(src.last, m.Seq)
			}
		}
	}
	return src
}

// chatText 是自检拿来对路径的原文：材料里的聊天（系统提示不算：它里头的路径不能替摘要里编出来的路径作证）
func (src compactSource) chatText() string {
	var out []string
	for _, m := range src.msgs {
		if m.Role != "system" {
			out = append(out, m.Text)
		}
		for _, c := range m.Calls {
			out = append(out, argStrings(c.Args)...)
		}
	}
	return strings.Join(out, "\n")
}

// keptTokens 是切点之后留着的原文估算；工作场景加上跟着的工具步骤（重建时按预算放，最多这么多）
func (src compactSource) keptTokens(events []history.Event, cut int64) int {
	n := keptTokens(events, cut)
	if src.toolCost == nil {
		return n
	}
	tools := 0
	for seq, cost := range src.toolCost {
		if seq >= cut {
			tools += cost
		}
	}
	return n + min(tools, src.cfg.Context.keepTokens()+restoredStubTokens)
}

// 留着的原文（切点那条起的消息；没有切点算全部）的估算
func keptTokens(events []history.Event, cut int64) int {
	n := 0
	for _, e := range events {
		if e.Kind == "message" && e.Seq >= cut {
			n += sumikaTokens(e.Text) + 4
		}
	}
	return n
}

// workRecordFor 是这一份的累计工作记录：上一份摘要里的、重建时从旧 root 算的、实例里这些工具调用；清单是当前状态（整份替换）。
// 第二个返回值：算不算在干活
func (l *Live) workRecordFor() (workRecord, bool) {
	var record workRecord
	for _, m := range l.msgs {
		if m.Kind == KindSummary {
			record.merge(parseWorkRecord(m.Text))
		}
	}
	record.merge(l.workRecord)
	var seen workRecord
	seen.merge(l.workRecord)
	seen.noteAll(l.msgs)
	record.merge(seen)
	for _, p := range l.workState.Pending {
		record.Pending = touch(record.Pending, p, recordMaxPending)
	}
	return record, workActivity(l.msgs, l.workState, seen)
}

// toolCostOf 把工具步骤的估算记到它前面最近一条说过的话上（和恢复时挂靠的是同一条）
func toolCostOf(msgs []Msg) map[int64]int {
	cost := map[int64]int{}
	var anchor int64
	for _, m := range msgs {
		if m.Seq > 0 {
			anchor = m.Seq
		}
		if anchor == 0 {
			continue
		}
		switch {
		case m.Role == "toolResult":
			cost[anchor] += sumikaMessages(m.Text)
		case m.Role == "assistant" && len(m.Calls) > 0:
			for _, c := range m.Calls {
				cost[anchor] += sumikaTokens(jsjson.Stringify(c.Args))
			}
		}
	}
	return cost
}

// withoutToolSteps 去掉工具调用和结果，只留说的话（回复的字留着）
func withoutToolSteps(msgs []Msg) []Msg {
	out := make([]Msg, 0, len(msgs))
	for _, m := range msgs {
		switch {
		case m.Role == "toolResult" || m.Kind == KindWork:
		case m.Role == "assistant" && len(m.Calls) > 0:
			m.Calls = nil
			if textclean.Trim(m.Text) != "" {
				out = append(out, m)
			}
		default:
			out = append(out, m)
		}
	}
	return out
}

// argStrings 取出参数里的所有字符串（自检对路径用；不拿 JSON 文本比，那样反斜杠是转义过的）
func argStrings(v jsjson.Value) []string {
	switch v.Kind {
	case jsjson.String:
		return []string{v.Str}
	case jsjson.Array:
		var out []string
		for _, x := range v.Arr {
			out = append(out, argStrings(x)...)
		}
		return out
	case jsjson.Object:
		var out []string
		for _, k := range v.Keys {
			out = append(out, argStrings(v.Obj[k])...)
		}
		return out
	}
	return nil
}

// cutEvents 是找切点要看的事件：实例里她看到的最早那条起，最后一个"新话题"分隔之后（agent.ts runCompact 的 scope），
// 去掉没说出口的（试运行、看完不说）
func cutEvents(st *store.Store, src compactSource) ([]history.Event, error) {
	var all []history.Event
	err := history.WalkEvents(st.Events, src.b.Conversation, src.from, src.last, 200, func(page []history.Event) error { all = append(all, page...); return nil })
	if err != nil {
		return nil, err
	}
	var events []history.Event
	for _, e := range all {
		switch {
		case history.IsContextBreak(e):
			events = events[:0]
		case !history.NotSaid(e):
			events = append(events, e)
		}
	}
	return events, nil
}

// compactNow 压一次（agent.ts runCompact）：切点（前面没有可以压的就到此为止，不等插件、不请求）→ 压缩前的钩子 → 和实例
// 同一个前缀、末尾加一句整理的请求，要一份摘要 → 自检 → 写 context_epochs（成功、失败都记）和 model_runs（stage compact）。
// ctx 取消（被停下、被新的一句打断）就不再等、不再压（请求发出去以后被停下的记一行失败的，不当快照用）；后台在关（NewHub 的 ctx 取消）不写库。切点不晚于实例里她看到的最后一条；
// maxSeq > 0 时不晚于它；压缩前的钩子给的上限再取小的，按它切压不到硬门槛的八成以下就不管它（安全阀，规格-记忆-v2 13.2.7）。
// guard 是后台压开始时记下的版本：写库之前核对，任何一项变了就不写快照（只记一行作废和 model_runs）。
// 压成了清掉"上一次后台压缩没成"的记号
func (h *Hub) compactNow(ctx context.Context, src compactSource, trigger string, maxSeq int64, guard *compactGuard) Compacted {
	// 工作库的折叠线只在压缩成功、摘要（带累计清单）写进库以后才推进：先推进的话，压缩没切点或自检不过时，
	// 折叠线以前的工具步骤既不在摘要里、重启后也读不回来，累计清单就丢了它们
	var fold func()
	if p, ok := h.Cfg.Work.(*workPort); ok && workEnabled(src.cfg, src.work) {
		wctx, cancel := context.WithTimeout(ctx, time.Duration(h.Cfg.WorkSettings.CompactTimeoutMs)*time.Millisecond)
		candidate, err := p.service.Candidate(wctx, src.work)
		cancel()
		if err != nil {
			return Compacted{Err: err.Error()}
		}
		if candidate.State.ThroughOrdinal > 0 {
			fold = func() {
				wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Duration(h.Cfg.WorkSettings.CompactTimeoutMs)*time.Millisecond)
				defer cancel()
				if err := p.service.Install(wctx, src.work, candidate); err != nil {
					h.plugins.logf("会话 %s 压缩成功，但工作库折叠没装上：%v", src.b.Conversation, err)
				}
			}
		}
	}
	cfg, b := src.cfg, src.b
	h.compactingNotice(cfg, b, true)
	defer h.compactingNotice(cfg, b, false)
	if src.last == 0 {
		return Compacted{Err: "前面的聊天还不多，没有可以压的"}
	}
	// 这个会话所在平台的库：读切点前后的事件、写压缩快照；模型调用记进共用的运行记录
	st, err := h.storeOf(b.Conversation)
	if err != nil {
		return Compacted{Err: err.Error()}
	}
	events, err := cutEvents(st, src)
	if err != nil {
		return Compacted{Err: err.Error()}
	}
	var imageSeqs []int64
	for _, m := range src.msgs {
		if len(m.Images) > 0 {
			if len(m.imageSeqs) > 0 {
				imageSeqs = append(imageSeqs, m.imageSeqs...)
			} else if m.Seq != 0 {
				imageSeqs = append(imageSeqs, m.Seq)
			}
		}
	}
	texts := map[int64]string{}
	if len(imageSeqs) > 0 {
		rendered, err := history.Of(st.Events, b.CharacterID, b.Conversation, imageSeqs)
		if err != nil {
			return Compacted{Err: err.Error()}
		}
		for _, m := range rendered {
			texts[m.Seq] = m.Text
		}
	}
	for i := range src.msgs {
		if len(src.msgs[i].Images) > 0 {
			seqs := src.msgs[i].imageSeqs
			if len(seqs) == 0 {
				seqs = []int64{src.msgs[i].Seq}
			}
			var parts []string
			for _, seq := range seqs {
				if text, ok := texts[seq]; ok {
					parts = append(parts, text)
				}
			}
			if len(parts) > 0 {
				src.msgs[i].Text = strings.Join(parts, "\n")
			}
			src.msgs[i].Images = nil
		}
	}
	if maxSeq == 0 || maxSeq > src.last {
		maxSeq = src.last
	}
	keep := cfg.Context.keepTokens()
	if cutPointCost(events, keep, maxSeq, src.toolCost) == 0 {
		return Compacted{Err: "前面的聊天还不多，没有可以压的"}
	}
	limit := h.plugins.beforeCompact(ctx, BeforeCompact{CharacterID: b.CharacterID, Conversation: b.Conversation, Trigger: trigger})
	if ctx.Err() != nil {
		return Compacted{Stopped: true, Err: "压缩之前被停下了"}
	}
	started := cfg.Now() // 从摘要请求算起（等插件的时间不算进这一次压缩的耗时）
	bound := maxSeq
	if limit > 0 && limit < bound {
		bound = limit
	}
	cut := cutPointCost(events, keep, bound, src.toolCost)
	if bound != maxSeq && (cut == 0 || float64(b.Fixed+src.keptTokens(events, cut)) > b.Hard*0.8) {
		if cut != 0 || float64(b.Fixed+src.keptTokens(events, 0)) > b.Hard*0.8 {
			h.plugins.logf("会话 %s：记忆只整理到第 %d 条，按它切压不下来，照常压缩", b.Conversation, limit-1)
			cut = cutPointCost(events, keep, maxSeq, src.toolCost)
		} else {
			h.plugins.logf("会话 %s：记忆只整理到第 %d 条，切到那里前面没有可以压的，这次不压", b.Conversation, limit-1)
			return Compacted{Err: fmt.Sprintf("记忆只整理到第 %d 条，切到那里前面没有可以压的", limit-1)}
		}
	}
	var cutText string
	dropped, kept := 0, 0
	for _, e := range events {
		if e.Seq == cut {
			cutText = e.Text
		}
		if e.Kind == "message" {
			if e.Seq < cut {
				dropped++
			} else {
				kept++
			}
		}
	}
	promptName, kind := "compact-chat", "chat"
	if src.topic {
		promptName, kind = "compact-topic", "topic"
	}
	ins, err := prompts.Store{Root: cfg.Root, UserDir: cfg.Data.PromptsDir()}.Render(promptName, compactVars(src.b, cutText))
	if err != nil {
		return Compacted{Err: err.Error()}
	}
	// 和实例同一个前缀（系统提示、工具定义、建实例时读进来的历史），整理的请求追加在末尾（agent.ts 595 行：手拼的请求少了开头那条，
	// 缓存全断）。之后每轮的参考信息去掉了，所以缓存只能走到第一块参考信息之前（ADR-190 第 1 条的代价）
	// 工作场景的工具结果可能很长：过一遍软门槛精简；还超硬门槛（或者服务商报超了，见下面）就退到不带工具，清单照样由宿主记
	material := src.msgs
	noTools := !src.topic
	if src.topic {
		material, _ = b.slim(material)
		if float64(b.Fixed+conversationTokens(material)) > b.Hard {
			material, noTools = withoutToolSteps(material), true
		}
	}
	msgs := append(append([]Msg(nil), material...), Msg{Role: "user", Text: ins.Text, Kind: KindContext})
	req := b.requestFor(msgs)
	identity := kernel.InvocationFromContext(ctx)
	if identity.RootID == "" && trigger != "manual" {
		identity = b.Invocation
	}
	if identity.RootID == "" {
		id := "compact:" + storeID(cfg)
		identity = kernel.Invocation{RootID: id, RunID: id, Kind: "compact", Scope: kernel.Scope{CharacterID: b.CharacterID, Conversation: b.Conversation}}
		if err := AdmitBudgetRoot(ctx, cfg.Budget, identity, cfg.BudgetConfig.LimitsFor(identity.Scope, time.Now())); err != nil {
			return Compacted{Err: err.Error()}
		}
	}
	b.Invocation = identity
	b.Invocation.Kind = "compact"
	b.Invocation.StepID = "step:" + storeID(cfg)
	timeout := cfg.CompactTimeout
	if timeout <= 0 {
		timeout = 180 * time.Second
	}
	tctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var res llm.Result
	for attempt := 0; ; attempt++ {
		res, err = modelStream(tctx, cfg, b, req, nil)
		if errors.Is(err, turnbudget.ErrExhausted) {
			// 被拦下时把原因和当时的数写清楚：实例多大、这个根的上限、已经用掉多少
			limit, used := int64(0), int64(0)
			if cfg.Budget != nil {
				if snap, e := cfg.Budget.Snapshot(ctx, identity.RootID); e == nil {
					limit, used = snap.Limits.MaxTokens, snap.Tokens
				}
			}
			h.plugins.logf("会话 %s 的压缩被预算拦下：%s（trigger %s，实例约 %d token，根的累计令牌上限 %d，已用 %d）",
				b.Conversation, errorText(err), trigger, src.before, limit, used)
		}
		if err == nil || tctx.Err() != nil || errors.Is(err, turnbudget.ErrExhausted) {
			break
		}
		if !noTools && llm.IsContextOverflow(err) {
			noTools = true
			h.plugins.logf("会话 %s 压缩的摘要请求自己超了窗口，退到不带工具再试一次", b.Conversation)
			req = b.requestFor(append(withoutToolSteps(material), Msg{Role: "user", Text: ins.Text, Kind: KindContext}))
			continue
		}
		wait, ok := cfg.Retry.wait(err, attempt)
		if !ok {
			break
		}
		h.plugins.logf("会话 %s 压缩的摘要请求出错：%s；%.1f 秒以后再请求（第 %d 次重试）", b.Conversation, errorText(err), wait.Seconds(), attempt+1)
		t := time.NewTimer(wait)
		select {
		case <-tctx.Done():
		case <-t.C:
		}
		t.Stop()
	}
	if h.base.Err() != nil {
		return Compacted{Stopped: true, Err: "后台正在关"} // agent.ts runCompact 的 !alive：不写库
	}
	written := stripWorkRecord(textclean.Trim(Clean(res.Text))) // 模型写的同名块不算：工具输出可能伪造
	summary := written
	if written != "" && !src.record.empty() {
		summary = written + "\n\n" + src.record.render()
	}
	after := b.Fixed + sumikaMessages(history.SummaryHeader+"\n"+summary) + src.keptTokens(events, cut)
	var problems []string
	var errClass string // 失败原因类别（llm.Class*）；problems 为空时不写进库
	switch {
	case ctx.Err() != nil:
		problems, errClass = []string{"压缩的时候被停下了，摘要没写完"}, llm.ClassCancelled
	case errors.Is(tctx.Err(), context.DeadlineExceeded):
		problems, errClass = []string{fmt.Sprintf("摘要请求没完成（超过 %v）", timeout)}, llm.ClassTimeout
	case err != nil:
		problems, errClass = []string{errorText(err)}, modelcall.ErrorClass(err)
	case len(res.ToolCalls) > 0:
		problems, errClass = []string{"摘要请求要调工具，没有写摘要"}, llm.ClassParseFailed
	default:
		// 摘要没写成样（太短、太长、没压小等）：模型回了话但不合格，和"输出不合规"同一类
		problems = summaryProblemsFor(written, src.chatText(), src.before, after, b.Hard, src.topic)
		errClass = llm.ClassParseFailed
	}
	now := cfg.Now()
	id := store.NewID(now)
	// 后台压的开始时的版本：起点、切点、快照、模型、会话代次。写库之前核对，变了一样就不写这份快照；
	// 摘要请求已经付过钱，照常记进 model_runs（切点之后来的消息不受影响，重建照常接上）
	if guard != nil {
		guard.cut = cut
		if why := h.compactGuardBroken(st, src, guard); why != "" {
			h.plugins.logf("会话 %s 的压缩结果作废（切点第 %d 条）：%s", b.Conversation, guard.cut, why)
			cj, _ := store.JSString(map[string]any{"compact": map[string]any{"kind": kind, "trigger": trigger, "before": src.before, "after": after,
				"dropped": dropped, "kept": kept, "problems": []string{"作废：" + why}}})
			errText := "作废：" + why
			run := b.runRow(cfg, &res, "", started, time.Time{}, cj, &errText, "", false, "")
			run.Stage = "compact"
			run.Sections = append(run.Sections, store.RunSection{ID: ins.ID, Source: ins.Source, Text: ins.Text})
			if err := st.AddRun(run); err != nil {
				h.plugins.logf("会话 %s 作废的那次压缩没记上：%v", b.Conversation, err)
			}
			return Compacted{Void: true, Err: errText}
		}
	}
	// 核验和写库之间还有一个很小的窗口：新话题正好在这中间写了分隔，这份快照只会白占一行，重建只读分隔之后的、
	// 用不上它；检查点回退不写分隔，快照照样写，下一轮照常换上。为这个窗口再拿一次库锁不值得
	status := "ok"
	var errStr *string
	if len(problems) > 0 {
		status = "failed"
		s := strings.Join(problems, "；")
		errStr = &s
	}
	// 后台压缩被取消（开新话题、检查点回退、关机）不算失败：不写失败快照，下一次空闲压缩不为一次取消退避 6 小时。
	// 模型调用照样记一行（付过的钱看得到）；手动压缩的取消仍照 A8 写一份失败
	skipEpoch := ctx.Err() != nil && (trigger == "early" || trigger == "idle")
	if !skipEpoch {
		if err := st.AddEpoch(store.Epoch{ID: id, Conversation: b.Conversation, CharacterID: b.CharacterID, FromSeq: cut, Summary: summary, Kind: kind, Trigger: trigger,
			TokensBefore: src.before, TokensAfter: after, Dropped: dropped, Kept: kept, Model: b.Model.ID, Status: status, Error: errStr,
			Ms: now.Sub(started).Milliseconds(), CreatedAt: now.UnixMilli()}); err != nil {
			h.plugins.logf("会话 %s 压缩的结果没写进库：%v", b.Conversation, err)
			return Compacted{Err: err.Error(), Stopped: ctx.Err() != nil}
		}
		if status == "ok" && fold != nil {
			fold()
		}
	}
	if problems == nil {
		problems = []string{}
	}
	cj, _ := store.JSString(map[string]any{"compact": map[string]any{"kind": kind, "trigger": trigger, "before": src.before, "after": after, "dropped": dropped, "kept": kept,
		"prefixReuse": min(100, int(math.Round(float64(res.Usage.CacheRead)/float64(max(1, src.before))*100))), "problems": problems}})
	run := b.runRow(cfg, &res, "", started, time.Time{}, cj, errStr, errClassOf(errStr, errClass), false, "")
	run.Stage = "compact"
	if !skipEpoch {
		run.EpochID = &id
	}
	run.Sections = append(run.Sections, store.RunSection{ID: ins.ID, Source: ins.Source, Text: ins.Text})
	if err := st.AddRun(run); err != nil {
		h.plugins.logf("会话 %s 压缩的那次模型调用没记上：%v", b.Conversation, err)
	}
	c := Compacted{FromSeq: cut, OK: status == "ok", Stopped: ctx.Err() != nil}
	if !skipEpoch {
		c.EpochID = id
	}
	if errStr != nil {
		c.Err = *errStr
	}
	if c.OK {
		h.mu.Lock()
		delete(h.compactFailures, convKey(b.CharacterID, b.Conversation))
		h.mu.Unlock()
	}
	return c
}

// cutPoint：从最新往前数，留到约 keep 个 token 为止，切在主人（平台上是人）说话的那一条前面（一来一回不拆开）；最近一轮总是留着。
// 返回快照之后从哪条原文接着带（from_seq）；前面没有可以压的（只有一轮，或者全都在保留的范围里）返回 0。
// maxSeq > 0 时切点不晚于它（压掉的只能是记忆整理过的，规格-记忆-v2 13.2.7）
func cutPoint(events []history.Event, keep int, maxSeq int64) int64 {
	return cutPointCost(events, keep, maxSeq, nil)
}

// cutPointCost 同 cutPoint，每条事件再加上跟着它的工具步骤（工作场景：不算的话压完工具历史还是满的，下一轮又压）
func cutPointCost(events []history.Event, keep int, maxSeq int64, extra map[int64]int) int64 {
	var msgs []history.Event
	for _, e := range events {
		if e.Kind == "message" {
			msgs = append(msgs, e)
		}
	}
	var asks []int
	for i, e := range msgs {
		if e.ActorKind == "owner" || e.ActorKind == "human" {
			asks = append(asks, i)
		}
	}
	if len(asks) < 2 {
		return 0
	}
	cost := func(from, to int) int {
		n := 0
		for _, e := range msgs[from:to] {
			n += sumikaTokens(e.Text) + 4 + extra[e.Seq]
		}
		return n
	}
	cut := asks[len(asks)-1] // 最近一轮至少留着
	used := cost(cut, len(msgs))
	for k := len(asks) - 2; k >= 0; k-- {
		i := asks[k]
		more := cost(i, cut)
		if used+more > keep {
			break
		}
		used += more
		cut = i
	}
	// 往前挪到不晚于 maxSeq 的主人那句
	for k := len(asks) - 1; k >= 0; k-- {
		if i := asks[k]; i <= cut && (maxSeq <= 0 || msgs[i].Seq <= maxSeq) {
			if i == 0 {
				return 0
			}
			return msgs[i].Seq
		}
	}
	return 0
}

// 摘要里提到的文件路径：Windows 盘符路径、带斜杠的相对路径（文件名里可以有中文：docs/待办.md）
var pathRE = regexp.MustCompile(`[A-Za-z]:[\\/][^\s\x{3000}，。；、"'` + "`" + `（）()<>|*?]+|(?:\.{0,2}/)?[\p{L}\p{N}_.-]+(?:/[\p{L}\p{N}_.-]+)+\.[\p{L}\p{N}]+`)

var trailPunctRE = regexp.MustCompile(`[.,:;]+$`)

func pathsIn(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, p := range pathRE.FindAllString(text, -1) {
		p = trailPunctRE.ReplaceAllString(p, "")
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

var boilerplateRE = regexp.MustCompile(`我是\s*AI|没有(跨对话的)?记忆|作为(一个)?(AI|人工智能|语言模型)`)

// summaryProblems 是压完自检（规格-本地话题与压缩第八节第 4 条）：摘要不能是空的、不能是"我是 AI、没有记忆"这类套话、
// 提到的路径在原文里找得到、压完要比压之前小、而且降到门槛的八成以下。返回问题（空 = 能用）
func summaryProblems(summary, source string, before, after int, limit float64) []string {
	return summaryProblemsFor(summary, source, before, after, limit, false)
}

// summaryProblemsFor 多一个 lenientPaths：工作场景路径多，模型写 ./src/a.go、原文是 src/a.go 不该判失败，文件名对得上就算
func summaryProblemsFor(summary, source string, before, after int, limit float64, lenientPaths bool) []string {
	var problems []string
	s := strings.TrimSpace(summary)
	if store.JSLen(s) < 20 {
		problems = append(problems, "摘要是空的或太短")
	}
	if boilerplateRE.MatchString(s) {
		problems = append(problems, `摘要里有"我是 AI、没有记忆"这类套话`)
	}
	norm := func(p string) string { return strings.ToLower(strings.ReplaceAll(p, `\`, "/")) }
	src := norm(source)
	var missing []string
	for _, p := range pathsIn(s) {
		n := norm(p)
		if strings.Contains(src, n) || (lenientPaths && strings.Contains(src, path.Base(n))) {
			continue
		}
		missing = append(missing, p)
	}
	if len(missing) > 0 {
		problems = append(problems, "摘要里的路径在原文里找不到："+strings.Join(missing[:min(3, len(missing))], "、"))
	}
	if float64(after) > float64(before)*0.9 {
		problems = append(problems, fmt.Sprintf("压完没怎么变小（约 %d → %d token），摘要太长或者前面本来就不长", before, after))
	}
	if float64(after) > limit*0.8 {
		problems = append(problems, fmt.Sprintf("压完还有约 %d token，没降到门槛（%d）的八成以下", after, int(math.Round(limit))))
	}
	return problems
}

// errClassOf 只在这一行真的带着错误时才给类别：摘要合格（problems 为空）时 errStr 是 nil，类别必须空，
// 不能把 switch 里留下的类别写进成功的行
func errClassOf(errStr *string, class string) string {
	if errStr == nil {
		return ""
	}
	return class
}
