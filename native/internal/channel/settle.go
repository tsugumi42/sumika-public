package channel

// 回合定了以后（规格-渠道中间件 第十节 10.1）：渠道来的回合，她说的话内核不写库，随"回合定了"交出来（Settled.Said，每步有字的一句，Clean 以后的字），
// 中间件裁决、改写、写库、发出去。结局是 done 才按步号的顺序对每一句做：
//
//	1 标记过滤  内核的 Clean 已经做过了，这里不重做（再跑一遍同一个函数结果不变，settle_test.go 查）
//	2 改写      去句尾句号等，按渠道（Shaper.Rewrite），出错用原文。结果就是「发出去的字」，库、历史、发送三处都用它
//	3 字数保险丝 改写以后超过 MaxChars 字 → 拦下，记「保险丝」。按整句，在拆条之前算
//	4 守卫      CheckOutbound：查的是第 2 步之后的字，把这一回合前面已经放行的句子接在前面一起查（拆开说的私事单看第二句不拦）。
//	            查出来整句拦下，计划记「拦下」，不存原文；不是失败，不重试。守卫没判成（出错、超时）也拦下，reason 记「守卫没判成」
//	5 出站记数   只记数不拦（Config.Audit，分类器还没搬到 Go，nil 就不记）
//	6 拆条      Shaper.Split，拆出来的段存进计划，之后不再重算
//	7 写库      Outlet.SendHeld：她这句话的 events（origin = character，试运行档是 automation:dry-run）、计划、各段（待发）一个事务；
//	            拦下的只记一条没有原文的计划（Outlet.Record）
//
// 每句写完库之后、发出去之前：把写进库的那几句（试运行的不算）同步交回内核（Hub.Commit，seq 和库里一样的字），Commit 返回了才开始发。
// Commit 报错记一行日志，再 RestartIn 让下一批从库里重建实例（刚写进库的几行一定读得到），已经写进库的话照发。
// 结局是 stopped 或 error：缓冲的句子全部丢掉，不写库、不发，只写一条没有原文的计划（已打断 / 出错），不调 Commit（内核当场拿掉那几句）。
//
// 整个处理（裁决、写库、Commit、开始发）做完才放开屏障；panic 也要放开，不然这个会话后面的批次永远等下去。

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sumika-native/internal/kernel"
	"time"
	"unicode/utf8"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
	"sumika-native/internal/textclean"
	"sumika-native/internal/turn"
)

func (r *Responder) shaperFor(channel string) Shaper {
	sh := r.cfg.Shapers[channel]
	if sh.Rewrite == nil {
		sh.Rewrite = func(s string) string { return s }
	}
	if sh.Split == nil {
		sh.Split = splitMessage
	}
	return sh
}

// safe 跑一个会 panic 的钩子，panic 当出错
func safe[T any](f func() T) (v T, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("出了意外：%v", p)
		}
	}()
	return f(), nil
}

func (r *Responder) process(key string, rd *round, s turn.Settled) {
	defer r.finish(key, rd)
	outcome, reason := store.GateOutcomeError, "裁决没有完成"
	defer func() {
		if p := recover(); p != nil {
			reason = fmt.Sprintf("裁决出了意外：%v", p)
			r.logf("会话 %s 的回合 %s 裁决时出了意外：%v，屏障照样放开", s.Conversation, s.TurnID, p)
		}
		r.mu.Lock()
		ids := r.claimOutcome(rd)
		r.mu.Unlock()
		r.gateOutcome(ids, outcome, reason)
	}()
	if r.ctx.Err() != nil {
		outcome, reason = store.GateOutcomeInterrupted, "后台关闭，这一轮没做完"
		return
	}
	switch s.Outcome {
	case turn.OutcomeDone:
		outcome, reason = r.settleDone(rd, s)
	case turn.OutcomeStopped:
		outcome, reason = store.GateOutcomeInterrupted, "被新消息打断，这一轮没说出口"
		r.record(rd, store.PlanKindInterrupted, reason)
	default:
		reason = "回合出错：未知"
		if s.Err != nil {
			reason = "回合出错：" + truncRunes(s.Err.Error(), 120)
		}
		r.record(rd, store.PlanKindError, reason)
	}
}

// claimOutcome 接手这一回合的判断结局，只能接一次；调用时 r.mu 在手里，返回的副本可在放锁后写库。
func (r *Responder) claimOutcome(rd *round) []int64 {
	if rd.outcomeClaimed {
		return nil
	}
	rd.outcomeClaimed = true
	return slices.Clone(rd.gateDecisionIDs)
}

// gateOutcome 逐条回填判断，关机时也写（库还开着）；失败只记日志，不影响回合。
func (r *Responder) gateOutcome(ids []int64, outcome, reason string) {
	if r.cfg.Stores == nil {
		return
	}
	for _, id := range ids {
		if err := r.cfg.Stores.TraceStore().SetGateOutcome(id, outcome, reason); err != nil {
			r.logf("闸门判断 %d 记回合结局失败：%v", id, err)
		}
	}
}

// record 记一条没有原文的计划（拦下、已打断、出错）
func (r *Responder) record(rd *round, kind, reason string) {
	if r.ctx.Err() != nil {
		return
	}
	id, err := r.cfg.Writer.Record(r.ctx, rd.character, types.OriginCharacter, rd.conv, kind, reason)
	switch {
	case errors.Is(err, ErrClosed):
	case err != nil:
		r.warnf("会话 %s 记计划（%s）失败：%v", rd.conv.String(), kind, err)
	default:
		r.logf("会话 %s 计划 %s：%s（%s）", rd.conv.String(), id, kind, reason)
	}
}

// written 是写进库、等着交回内核和开始发的一句
type written struct {
	step int
	held Held
	text string // 库里这一行的字（events.text）
}

func (r *Responder) settleDone(rd *round, s turn.Settled) (outcome, reason string) {
	outcome, reason = store.GateOutcomeSelfSkip, "看完没出声"
	said := slices.Clone(s.Said)
	sort.SliceStable(said, func(i, j int) bool { return said[i].Step < said[j].Step })
	sh := r.shaperFor(rd.conv.Channel)
	var out []written
	var passed []string // 这一回合前面放行的句子（改写以后的字），守卫累计着查
	for _, sd := range said {
		if r.ctx.Err() != nil {
			return store.GateOutcomeInterrupted, "渠道已关闭"
		}
		// 2 改写
		rew, err := safe(func() string { return sh.Rewrite(sd.Text) })
		if err != nil {
			r.logf("会话 %s 第 %d 步改写%v，用原文", rd.conv.String(), sd.Step, err)
			rew = sd.Text
		}
		if textclean.Trim(rew) == "" {
			r.logf("会话 %s 第 %d 步改写以后是空的，没有可发的", rd.conv.String(), sd.Step)
			outcome, reason = store.GateOutcomeBlocked, "改写以后为空，没有可发的"
			continue
		}
		// 3 字数保险丝
		if n := utf8.RuneCountInString(rew); n > r.cfg.MaxChars {
			outcome, reason = store.GateOutcomeFuse, fmt.Sprintf("保险丝：一句 %d 字，超过 %d 字", n, r.cfg.MaxChars)
			r.record(rd, store.PlanKindBlocked, reason)
			continue
		}
		// 4 守卫
		if why := r.guard(rew, passed, s); why != "" {
			outcome, reason = store.GateOutcomeBlocked, why
			r.record(rd, store.PlanKindBlocked, why)
			continue
		}
		// 5 出站记数（只记数；记到哪一条事件上要等写库）
		var hits []AuditHit
		if r.cfg.Audit != nil {
			hits, _ = safe(func() []AuditHit { return r.cfg.Audit(rew) })
		}
		// 6 拆条
		pieces, err := safe(func() []string { return sh.Split(rew) })
		if err != nil || len(pieces) == 0 {
			r.logf("会话 %s 第 %d 步拆条%v，整句当一段发", rd.conv.String(), sd.Step, err)
			pieces = []string{textclean.Trim(rew)}
		}
		parts := make([]types.Part, len(pieces))
		for i, p := range pieces {
			parts[i] = types.Message(p)
		}
		// 7 写库
		identity := s.Identity
		identity.StepID = fmt.Sprintf("%s/step/%d", s.TurnID, sd.Step)
		h, err := r.cfg.Writer.SendHeld(kernel.WithInvocation(r.ctx, identity), rd.character, types.OriginCharacter, rd.conv, parts)
		switch {
		case errors.Is(err, ErrClosed):
			return store.GateOutcomeInterrupted, "渠道已关闭"
		case errors.Is(err, ErrNotSpeaking):
			outcome, reason = store.GateOutcomeBlocked, "这个会话现在是只记录档，没发"
			r.record(rd, store.PlanKindBlocked, reason)
			continue
		case errors.Is(err, ErrSendCheck):
			outcome, reason = store.GateOutcomeBlocked, truncRunes(err.Error(), 120)
			r.record(rd, store.PlanKindBlocked, reason)
			continue
		case err != nil:
			outcome, reason = store.GateOutcomeError, "写库失败："+truncRunes(err.Error(), 120)
			r.record(rd, store.PlanKindError, reason)
			continue
		}
		passed = append(passed, rew)
		out = append(out, written{step: sd.Step, held: h, text: eventText(parts)})
		if r.cfg.OnAudit != nil {
			for _, hit := range hits {
				r.cfg.OnAudit(AuditRecord{Character: rd.character, Conv: rd.conv.String(), EventSeq: h.EventSeq, Hit: hit, Dry: h.DryRun})
			}
		}
	}
	if r.ctx.Err() != nil {
		return store.GateOutcomeInterrupted, "渠道已关闭"
	}

	// 把写进库的交回内核：回合说了话（Said 非空），内核那边就等着这一次交回；一句都没写进去（全拦下了、全是试运行）也交，交空的，
	// 实例里这一回合那一段才照库重写掉，不留到下一轮开始时才重写、记一行「没交回」
	if len(s.Said) > 0 {
		var lines []turn.Committed
		for _, w := range out {
			if !w.held.DryRun && w.held.EventSeq > 0 {
				lines = append(lines, turn.Committed{Step: w.step, Seq: w.held.EventSeq, Text: w.text})
			}
		}
		if err := r.cfg.Kernel.Commit(rd.character, s.Conversation, s.TurnID, lines); err != nil {
			r.logf("会话 %s 回合 %s 交回写库的句子（Commit）报错：%v，让下一批从库里重建实例", s.Conversation, s.TurnID, err)
			r.cfg.Kernel.RestartIn(rd.character, s.Conversation)
		}
	}
	// 交回了才开始发
	for _, w := range out {
		if err := w.held.Start(); err != nil && !errors.Is(err, ErrClosed) {
			r.logf("会话 %s 计划 %s 开始发送失败：%v", rd.conv.String(), w.held.PlanID, err)
		}
	}
	if r.cfg.OnTurnDone != nil && r.ctx.Err() == nil {
		if _, err := safe(func() struct{} {
			r.cfg.OnTurnDone(rd.character, rd.conv, len(said) > 0, len(out), time.Now())
			return struct{}{}
		}); err != nil {
			r.logf("会话 %s 回合定了的通知%v", rd.conv.String(), err)
		}
	}
	if len(out) > 0 {
		outcome, reason = store.GateOutcomeDry, ""
		for _, w := range out {
			if !w.held.DryRun {
				outcome = store.GateOutcomeSpoke
				break
			}
		}
	}
	return outcome, reason
}

// guard 过一遍守卫。返回空就是放行；不空是拦下的原因（不含原文）
func (r *Responder) guard(text string, passed []string, s turn.Settled) string {
	g := r.cfg.Guard
	if s.Audience.Channel == "console" && s.Audience.Scene == types.ScenePrivate {
		return ""
	}
	if r.cfg.GuardFor != nil {
		// 取守卫的函数自己 panic 也是没判成：不能一路冲到 process 的兜底，那样这一回合不记计划、也不交回内核
		var err error
		if g, err = safe(func() Guard { return r.cfg.GuardFor(s.CharacterID) }); err != nil {
			r.logf("会话 %s 取守卫出了意外，没判成，这一句拦下（不放行）", s.Conversation)
			return "守卫没判成"
		}
	}
	if g == nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(r.ctx, r.cfg.GuardTimeout)
	identity := s.Identity
	identity.Kind = kernel.KindGuard
	ctx = kernel.WithInvocation(ctx, identity)
	defer cancel()
	full := text
	if len(passed) > 0 {
		full = strings.Join(append(slices.Clone(passed), text), "\n")
	}
	var refs []string
	var err error
	func() {
		defer func() {
			if p := recover(); p != nil {
				err = fmt.Errorf("守卫出了意外：%v", p)
			}
		}()
		refs, err = g.CheckOutbound(ctx, full, s.Audience)
	}()
	// 记路径（问守卫走的哪条路、叫提醒主人的回调）出意外也是没判成：不能冲出 guard，那样这一回合不记计划、也不交回内核
	if _, perr := safe(func() struct{} { r.noteRoute(g, s.Conversation); return struct{}{} }); perr != nil {
		r.logf("会话 %s 记守卫走的路时出了意外，没判成，这一句拦下（不放行）", s.Conversation)
		return "守卫没判成"
	}
	if err != nil || ctx.Err() != nil {
		kind := "出错"
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			kind = "超时"
		}
		r.logf("会话 %s 守卫%s，没判成，这一句拦下（不放行）", s.Conversation, kind)
		return "守卫没判成"
	}
	if len(refs) > 0 {
		return fmt.Sprintf("守卫拦下：命中 %d 条记忆（%s）", len(refs), strings.Join(refs, ","))
	}
	return ""
}

// noteRoute 记守卫刚才走的路；路变了才叫 OnGuardRoute（切到退路提醒一次、恢复再提醒一次，不每句提醒）
func (r *Responder) noteRoute(g Guard, conversation string) {
	gr, ok := g.(GuardRouter)
	if !ok {
		return
	}
	cur := gr.GuardRoute()
	if cur == "" {
		cur = GuardRouteNormal
	}
	r.logf("会话 %s 守卫走的是 %s", conversation, cur)
	r.routeMu.Lock()
	prev := r.lastRoute
	r.lastRoute = cur
	r.routeMu.Unlock()
	if prev != cur && r.cfg.OnGuardRoute != nil {
		r.cfg.OnGuardRoute(prev, cur)
	}
}

// automationLines 是插件自己发的话（复读等，origin 是 automation:*，不含试运行和自己不说）：它们不是回合说的，不调 Commit，
// 下一批 Respond 时把 seq 放进 Seqs、字拼进 Text，她的实例才知道自己复读过。写法和从库里重建实例时一样（history.toMessage）。
// after 是上一次交给内核的最后一条事件：之前的已经在实例里；0 是启动以后第一次，实例是新建的、从库里读历史，不用补
func (r *Responder) automationLines(b types.Batch, after int64) []seqLine {
	if r.cfg.Stores == nil || after == 0 {
		return nil
	}
	db, err := r.cfg.Stores.For(b.Conv.Channel)
	if err != nil {
		r.logf("会话 %s 读插件自己发的话时取库出错：%v，这一批不带", b.Conv.String(), err)
		return nil
	}
	lines, err := db.AutomationLines(b.Conv.String(), b.Character, after, lastSeq(b))
	if err != nil {
		r.logf("会话 %s 读插件自己发的话失败：%v，这一批不带", b.Conv.String(), err)
		return nil
	}
	out := make([]seqLine, len(lines))
	for i, l := range lines {
		out[i] = seqLine{l.Seq, "（这条是系统替你自动发的（" + strings.TrimPrefix(l.Origin, "automation:") + "），不是你说的）" + l.Text}
	}
	return out
}
