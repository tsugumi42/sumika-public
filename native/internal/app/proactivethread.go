package app

// 主动联系的话题线（规格-主动联系 第四～十节，第 2 步：本地私聊）。
//
//   - 导演放行 → Wake 开线（或续醒），先把"醒来行"存成线里的 system 事件，再在线里起一轮模型（turn.ThreadInput，身份和听众写死）；
//   - 线里的工具（proactivetools.go）：owner_presence 看主人在哪、thread_send 发给他、stay_quiet、end_thread、hand_back、proactive_snooze；
//   - 本地投递不走出口：往 console:<角色> 写一条她的话（origin character，不是 automation），MarkStale 让该会话下一轮重建，
//     attend 在窗口不在前台时弹系统通知；
//   - 主人在本地回：认领（线回）或旁听（普通路径回、线只记下他回过），认领时由宿主替他写库，再转述进线；
//   - 线的状态机在 proactive.Apply，状态存在 trace.db；超时、认领宽限靠导演每次轮询调 sweep。
//
// 线里没有执行工具、没有 /goal、/loop：For 只给线和主人私聊，PrivateOK 为假，ownerConsoleAudience 不成立。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/config"
	"sumika-native/internal/history"
	"sumika-native/internal/persona"
	"sumika-native/internal/proactive"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// AttentionProactive 是"她来找你了"（本地窗口不在前台时弹系统通知）
const AttentionProactive = "proactive"

const (
	threadScene = "scene-proactive-thread"
	// threadTextMax 是本地投递一条话最多多少字：本地私聊的普通回复没有字数保险丝，这里只是防模型一次倒出大段
	threadTextMax = 1000
)

// 日志里模型和发送相关的原因码（规格第七节"日志"，第 2 步用到的）
const (
	reasonModelQuiet      = "model-quiet"
	reasonModelSpoke      = "model-spoke"
	reasonModelNoStance   = "model-no-stance"
	reasonThinkOnly       = "think-only"
	reasonSendFailed      = "send-failed"
	reasonSendBlockedCap  = "send-blocked-cap"
	reasonSendBlockedGate = "send-blocked-guard"
)

type proactiveHost struct {
	a   *App
	mgr *proactive.Threads
	dir *proactive.Director

	mu      sync.Mutex
	handles map[string]map[string]string // 线号 → 句柄 → 会话（owner_presence 发的，只在本线内有效）
	quiet   map[string]string            // 线号 → 这一轮 stay_quiet 的理由
	refused map[string]string            // 线号 → 这一轮最后一次没发出去的原因码
	spoke   map[string]int               // 线号 → 这一轮真发出去几条
	thought map[string]bool              // 线号 → 这一轮 think 档记了本想说的话
	runs    map[string][]threadRun       // 线号 → 正在跑和排队等着的轮（旧的在前；线里的轮同一会话串行，最前面的是正在执行的）
	runSeq  int

	recovered proactive.RecoverReport // 启动恢复的结果，runProactive 里 afterRecover 处理
}

// threadRun 是线里的一轮：started 是起这一轮的时刻（认领只对这一刻以前的负责），cancel 用来停它
type threadRun struct {
	id      int
	started time.Time
	cancel  context.CancelFunc
}

func newProactiveHost(a *App, mgr *proactive.Threads) *proactiveHost {
	return &proactiveHost{a: a, mgr: mgr, handles: map[string]map[string]string{}, quiet: map[string]string{}, refused: map[string]string{},
		spoke: map[string]int{}, thought: map[string]bool{}, runs: map[string][]threadRun{}}
}

func (h *proactiveHost) cfg() config.Proactive { return h.a.cfg.Proactive }

// enabledFor 是这个角色有没有开主动联系
func (h *proactiveHost) enabledFor(char string) bool {
	p := h.cfg()
	if !p.Enabled {
		return false
	}
	for _, c := range p.Characters {
		if c == char {
			return true
		}
	}
	return false
}

func localConvOf(char string) string { return "console:" + char }

// ───────────── 导演放行以后：开线、续醒 ─────────────

// Wake 实现 proactive.Waker：只起线、起一轮就返回（调度循环在它返回前不会再检查别的到点），结局靠 finishTurn 回头补。
// 不能在这里调导演的 ThreadClosed、SetQuiet、SetNextWake（导演这时持有状态锁）
func (h *proactiveHost) Wake(ctx context.Context, req proactive.WakeRequest) (proactive.WakeResult, error) {
	p := h.cfg()
	switch req.Kind {
	case proactive.KindWake:
		id := newThreadID(h.a.now())
		th, err := h.mgr.Create(proactive.Thread{ID: id, Character: req.Character, Conversation: audience.ThreadConversation(req.Character, id), OpenedAt: req.At})
		if err != nil {
			return proactive.WakeResult{}, err
		}
		text := wakeLine(p, req)
		if recall := h.recallFor(req.Character, req.At); recall != "" {
			text += "\n" + recall
		}
		seq, err := h.appendThreadEvent(th, history.OriginProactiveWake, text)
		if err != nil {
			h.mgr.Apply(th.ID, proactive.Event{Kind: proactive.EvTurnError, At: req.At})
			return proactive.WakeResult{}, err
		}
		h.startTurn(th, seq, text, req.RowID, nil)
		// 线开着时下次"醒"是续醒的检查时间；线要是很快关了，ThreadClosed 会把它改回正常间隔
		return proactive.WakeResult{Thread: th.ID, NextAt: req.At.Add(time.Duration(p.FollowUpMinutes) * time.Minute)}, nil
	case proactive.KindFollowUp:
		th, ok, err := h.mgr.OpenOf(req.Character)
		if err != nil {
			return proactive.WakeResult{}, err
		}
		if !ok {
			return proactive.WakeResult{}, errors.New("续醒时没有开着的线")
		}
		th, err = h.mgr.Apply(th.ID, proactive.Event{Kind: proactive.EvWake, At: req.At})
		if err != nil {
			return proactive.WakeResult{Thread: th.ID}, err
		}
		text := followUpLine(p, th, req.At)
		seq, err := h.appendThreadEvent(th, history.OriginProactiveWake, text)
		if err != nil {
			h.mgr.Apply(th.ID, proactive.Event{Kind: proactive.EvTurnError, At: req.At})
			return proactive.WakeResult{}, err
		}
		h.startTurn(th, seq, text, req.RowID, nil)
		next := req.At.Add(time.Duration(p.FollowUpMinutes) * time.Minute)
		if th.FollowUps >= p.MaxFollowUps {
			next = proactive.CloseAt(p, th)
		}
		return proactive.WakeResult{Thread: th.ID, NextAt: next}, nil
	}
	return proactive.WakeResult{}, fmt.Errorf("不认识的放行种类：%s", req.Kind)
}

func newThreadID(now time.Time) string {
	id := strings.ToLower(store.NewID(now))
	return id[len(id)-10:]
}

var weekdayZh = [...]string{"周日", "周一", "周二", "周三", "周四", "周五", "周六"}

// wakeLine 是醒来行（规格第四节，一行，字段全来自代码判断）
func wakeLine(p config.Proactive, req proactive.WakeRequest) string {
	in := req.Inputs
	var since string
	if in.SinceContactMin < 0 {
		since = "还没找过他"
	} else {
		since = "距上次找他 " + durationZh(in.SinceContactMin)
		switch in.Heat {
		case string(proactive.HeatHot):
			since += "，他回得很快"
		case string(proactive.HeatWarm):
			since += "，他回过"
		case string(proactive.HeatCold):
			since += "，他没回"
		}
	}
	sleep := "否"
	if in.InSleep {
		sleep = "是"
		if in.OwnerLastActiveAgoMin >= 0 && in.OwnerLastActiveAgoMin <= p.ActiveWithinMinutes || in.WindowForeground {
			sleep += "，不过他这会儿在活动"
		}
	}
	return fmt.Sprintf("（导演：现在 %s %s；%s；今天已找 %d 次，上限 %d 次；睡觉时段里：%s。你可以找他，也可以什么都不做。）",
		req.At.Format("15:04"), weekdayZh[req.At.Weekday()], since, in.ThreadsToday, p.MaxThreadsPerDay, sleep)
}

func followUpLine(p config.Proactive, th proactive.Thread, at time.Time) string {
	ago := int(at.Sub(th.LastSendAt) / time.Minute)
	return fmt.Sprintf("（导演：续醒。你 %s前发了话，他还没回；这是第 %d 次续醒，最多 %d 次。可以继续等、换个地方发、或者收线。）",
		durationZh(max(ago, 0)), th.FollowUps, p.MaxFollowUps)
}

func durationZh(min int) string {
	if min < 60 {
		return fmt.Sprintf("%d 分钟", min)
	}
	return fmt.Sprintf("%d 小时 %d 分", min/60, min%60)
}

// appendThreadEvent 往线里存一条 system 事件（醒来行、主人回复转述、结论）；带 proactive-thread 标签，观察者是这个角色
func (h *proactiveHost) appendThreadEvent(th proactive.Thread, origin, text string) (int64, error) {
	st, err := h.a.set.ForConversation(th.Conversation)
	if err != nil {
		return 0, err
	}
	return st.AppendEvent(store.NewEvent{Channel: "console", Conversation: th.Conversation, ActorKind: "system", ActorID: audience.ThreadActor, Origin: origin,
		Kind: "message", Text: text, Segments: map[string]any{"tags": []string{audience.ThreadTag}}, CreatedAt: h.a.now().UnixMilli()}, []string{th.Character})
}

// startTurn 在线里起一轮模型（后台协程，随 autoCtx 关机时取消）；rowID 是导演日志里这次放行的行，轮结束后往里补结局
func (h *proactiveHost) startTurn(th proactive.Thread, seq int64, text string, rowID int64, onDone func()) {
	a := h.a
	started := a.now()
	ctx, cancel := context.WithCancel(a.autoCtx)
	h.mu.Lock()
	h.runSeq++
	runID := h.runSeq
	h.runs[th.ID] = append(h.runs[th.ID], threadRun{id: runID, started: started, cancel: cancel})
	delete(h.quiet, th.ID)
	delete(h.refused, th.ID)
	delete(h.spoke, th.ID)
	delete(h.thought, th.ID)
	h.mu.Unlock()
	a.autoWG.Add(1)
	go func() {
		defer a.autoWG.Done()
		defer cancel()
		in := turn.ThreadInput(th.Character, th.Conversation, []int64{seq}, text, []string{threadScene})
		out := <-a.hub.RespondContext(ctx, in)
		h.finishTurn(th.ID, runID, rowID, started, out)
		if onDone != nil {
			onDone()
		}
	}()
}

// removeRunLocked 把这一轮从在跑的表里拿掉（只拿自己的：同一条线可能还有排在后面的轮）
func (h *proactiveHost) removeRunLocked(id string, runID int) {
	list := h.runs[id]
	for i, r := range list {
		if r.id == runID {
			list = append(list[:i:i], list[i+1:]...)
			break
		}
	}
	if len(list) == 0 {
		delete(h.runs, id)
	} else {
		h.runs[id] = list
	}
}

// running 这条线现在有没有轮在跑或排队
func (h *proactiveHost) running(id string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.runs[id]) > 0
}

// currentRunStart 是线里正在执行的那一轮的起点（最老的一轮）；没有在跑的轮 ok 为假
func (h *proactiveHost) currentRunStart(id string) (time.Time, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if list := h.runs[id]; len(list) > 0 {
		return list[0].started, true
	}
	return time.Time{}, false
}

// finishTurn 是线里一轮结束以后的收尾：按线的状态机落 EvTurnEnd 或 EvTurnError，给导演日志补上这次放行的结局，
// 还没表态的认领交回普通路径，线要是关了就走 afterClose
func (h *proactiveHost) finishTurn(id string, runID int, rowID int64, started time.Time, out turn.Outcome) {
	// 收尾做完才从在跑的表里拿掉：等"没有在跑的轮"的一方（测试、关机）看到的是收尾以后的状态
	defer func() {
		h.mu.Lock()
		h.removeRunLocked(id, runID)
		h.mu.Unlock()
	}()
	h.mu.Lock()
	note := h.quiet[id]
	refused := h.refused[id]
	spoke := h.spoke[id]
	thought := h.thought[id]
	h.mu.Unlock()
	failed := out.Err != nil || (out.Result != nil && out.Result.Stopped)
	if out.Err != nil && !errors.Is(out.Err, context.Canceled) {
		h.a.logf("主动联系：线 %s 的一轮出错：%v", id, out.Err)
	}
	ev := proactive.Event{Kind: proactive.EvTurnEnd, At: h.a.now()}
	if failed {
		ev.Kind = proactive.EvTurnError
	}
	th, err := h.mgr.Apply(id, ev)
	switch {
	case err == nil && th.State == proactive.ThreadClosed:
		h.afterClose(th)
	case err != nil && !errors.Is(err, proactive.ErrNoop) && !errors.Is(err, proactive.ErrClosed):
		h.a.logf("主动联系：线 %s 收尾：%v", id, err)
	}
	// 认领了主人的话、这一轮既没回也没交回的：不让他的话没人回。只管这一轮开始前认领的；
	// 这一轮跑着时主人又来的话，排在后面的那一轮会处理
	if th2, err := h.mgr.Get(id); err == nil {
		h.handBackPending(th2, false, started)
	}
	reason := reasonModelNoStance
	switch {
	case failed:
		reason = proactive.ReasonError
	case spoke > 0:
		reason = reasonModelSpoke
	case thought:
		reason = reasonThinkOnly
	case refused != "":
		reason = refused
	case note != "":
		reason = reasonModelQuiet
	}
	if rowID != 0 {
		if err := (proactive.DBStore{DB: h.a.set.Trace()}).Annotate(rowID, reason, id, proactive.ClipNote(note, 80)); err != nil {
			h.a.logf("主动联系：补日志失败：%v", err)
		}
	}
	h.reschedule(id)
}

// afterClose 线刚关的收尾，每条线只走一次（调用方靠 Apply 成功且新状态是 closed 保证）：
// 在线里留一条结论事件、告诉导演（更新系数；别烦另走）、写一行 close 日志、交回还没表态的认领、清句柄
func (h *proactiveHost) afterClose(th proactive.Thread) {
	a := h.a
	conclusion := th.Conclusion
	if conclusion == "" {
		conclusion = proactive.CloseReasonText(th.CloseReason)
	}
	if _, err := h.appendThreadEvent(th, "proactive:conclusion", "结论："+conclusion); err != nil {
		a.logf("主动联系：线 %s 的结论没存：%v", th.ID, err)
	}
	h.mu.Lock()
	delete(h.handles, th.ID) // 在跑的一轮（如果还有）让它自己收完，不硬停
	h.mu.Unlock()
	h.handBackPending(th, false, time.Time{})
	if h.dir == nil {
		return
	}
	kind, heat := proactive.HeatOf(th)
	if kind != proactive.CloseSnoozed {
		if err := h.dir.ThreadClosed(th.Character, kind, a.now()); err != nil {
			a.logf("主动联系：线 %s 关闭后更新导演状态失败：%v", th.ID, err)
		}
	}
	in := proactive.Inputs{SinceContactMin: -1, ThreadsToday: th.Sends, OwnerLastActiveAgoMin: -1, LocalLastInputAgoMin: -1, Heat: string(heat)}
	if !th.LastSendAt.IsZero() {
		in.SinceContactMin = int(a.now().Sub(th.LastSendAt) / time.Minute)
	}
	raw, _ := json.Marshal(in)
	h.dir.Log(th.Character, proactive.Row{At: a.now(), Kind: proactive.KindClose, Reason: string(th.CloseReason), Thread: th.ID, Inputs: raw,
		ModelNote: proactive.ClipNote(th.Conclusion, 80)})
}

// closeVia 把一个收线的事件落到线上；成功关掉就走 afterClose，返回是不是这一次把它关掉的
func (h *proactiveHost) closeVia(id string, ev proactive.Event) (proactive.Thread, bool) {
	if ev.At.IsZero() {
		ev.At = h.a.now()
	}
	th, err := h.mgr.Apply(id, ev)
	if err != nil || th.State != proactive.ThreadClosed {
		return th, false
	}
	h.afterClose(th)
	return th, true
}

// reschedule 线的状态变了：把导演的下次醒排到这条线下一次要被看的时间（续醒或截止）。线没开着就什么也不做
func (h *proactiveHost) reschedule(id string) {
	if h.dir == nil {
		return
	}
	th, err := h.mgr.Get(id)
	if err != nil {
		return
	}
	at, ok := proactive.NextCheck(h.cfg(), th)
	if !ok {
		return
	}
	if err := h.dir.SetNextWake(th.Character, at); err != nil {
		h.a.logf("主动联系：重排导演下次醒失败：%v", err)
	}
}

// sweep 每次导演轮询调一遍（Options.Housekeeping）：认领宽限到点的自动交回，到了截止时间的线收掉
func (h *proactiveHost) sweep(_ context.Context, now time.Time) {
	open, err := h.mgr.OpenThreads()
	if err != nil {
		h.a.logf("主动联系：扫开着的线失败：%v", err)
		return
	}
	grace := time.Duration(h.cfg().ClaimGraceMinutes) * time.Minute
	for _, th := range open {
		// 线里还有轮在跑或排队就不交回：那一轮会自己回或自己交回，这里再交回会让主人那句被回两次
		if !h.running(th.ID) {
			claims, err := h.mgr.ClaimsOf(th.ID, proactive.ClaimPending)
			if err == nil {
				for _, c := range claims {
					if now.Sub(c.At) >= grace {
						h.handBack(th, c, false)
					}
				}
			}
		}
		if _, ok := h.closeVia(th.ID, proactive.Event{Kind: proactive.EvTimeout, At: now}); ok {
			h.a.logf("主动联系：线 %s 到时收了", th.ID)
		}
	}
}

// ───────────── 本地投递 ─────────────

// deliverLocal 把线要说的话写进本地私聊 console:<角色>（origin character，是她说的，不是 automation），
// 让该会话下一轮重建，刷新界面，窗口不在前台时弹系统通知。返回写进去的那条事件
func (h *proactiveHost) deliverLocal(th proactive.Thread, conv, text, reason string) (seq int64, eventID string, err error) {
	a := h.a
	st, err := a.set.ForConversation(conv)
	if err != nil {
		return 0, "", err
	}
	seg := map[string]any{"proactive": map[string]any{"thread": th.ID, "reason": proactive.ClipNote(reason, 60)}}
	seq, err = st.AppendEvent(store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "character", ActorID: th.Character, Origin: "character",
		Kind: "message", Text: text, Segments: seg, CreatedAt: a.now().UnixMilli()}, []string{th.Character})
	if err != nil {
		return 0, "", err
	}
	if _, id, ok, e := st.EventTextID(conv, seq); e == nil && ok {
		eventID = id
	}
	a.hub.MarkStale(th.Character, conv)
	a.statusChanged(conv)
	if h.cfg().Notify {
		a.attend(AttentionProactive, conv, h.characterName(th.Character)+" 来找你了", text)
	}
	return seq, eventID, nil
}

func (h *proactiveHost) characterName(id string) string { return h.a.characterName(id) }

func personaDisplayName(dir, id string) string {
	if name, _, _, err := persona.Display(dir, id, ""); err == nil {
		return name
	}
	return ""
}

// ───────────── 主人在本地回：认领、旁听、交回 ─────────────

// claimLocal 在主人从本地私聊发一句话、进回合之前调用。返回 true 表示这句被线认领了，调用方不再交给该会话的普通实例；
// 不认领时，如果线发过话而主人晚于它回了，记下"主人回过"（旁听）：取消续醒、热度按它算，这一句仍走普通路径。
// 只管本地私聊的主会话 console:<角色>；不在这里调模型，也不为每句话查模型
func (a *App) claimLocal(char, conv string, parts []history.Part, text string) bool {
	h := a.pthreads
	if h == nil || conv != localConvOf(char) || !h.enabledFor(char) {
		return false
	}
	th, ok, err := h.mgr.OpenOf(char)
	if err != nil || !ok || th.Sends == 0 {
		return false
	}
	now := a.now()
	st, err := a.set.ForConversation(conv)
	if err != nil {
		return false
	}
	var facts proactive.ClaimFacts
	if send, found, err := h.mgr.LastSendTo(th.ID, conv); err == nil && found {
		facts.SentHere = true
		for _, p := range parts {
			if p.Quote == nil {
				continue
			}
			if s, found, err := h.mgr.SendByEventID(conv, p.Quote.ID); err == nil && found && s.Thread == th.ID {
				facts.QuotedThreadMsg = true
			}
		}
		if latest, found, err := st.LatestCharacterSeq(conv, char); err == nil && found && latest == send.EventSeq {
			facts.ThreadMsgLatest = true
		}
	}
	if !proactive.ClaimAllowed(h.cfg(), th, conv, facts, now) {
		h.overhear(th, now)
		return false
	}
	// 认领：宿主先替主人把这句写进会话（本地没有记录器，拦在进回合之前就哪里都没有了），再转述进线
	ev := store.NewEvent{Channel: "console", Conversation: conv, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: text, CreatedAt: now.UnixMilli()}
	if len(parts) > 0 && !(len(parts) == 1 && parts[0].Quote == nil && len(parts[0].Attachments) == 0) {
		ev.Segments = map[string][]history.Part{"local_parts": parts}
		for _, p := range parts {
			if p.Quote != nil {
				id := p.Quote.ID
				ev.ReplyTo = &id
				break
			}
		}
	}
	seq, err := st.AppendEvent(ev, []string{char})
	if err != nil {
		a.logf("主动联系：认领时替主人写库失败，这句走普通路径：%v", err)
		return false
	}
	if err := h.mgr.AddClaim(conv, seq, th.ID, now); err != nil {
		a.logf("主动联系：登记认领失败，这句走普通路径：%v", err)
		h.handBackSeq(conv, char, seq, text)
		return true
	}
	a.statusChanged(conv)
	th2, err := h.mgr.Apply(th.ID, proactive.Event{Kind: proactive.EvClaim, At: now})
	if err != nil {
		// 线在这中间关了（比如刚好被收掉）：这句不能没人回，交回去
		h.handBackSeq(conv, char, seq, text)
		_, _ = h.mgr.SetClaim(conv, seq, proactive.ClaimHanded)
		return true
	}
	relay := "（所有者在本地私聊说：" + history.EscapeUserText(text) + "）"
	rseq, err := h.appendThreadEvent(th2, history.OriginProactiveRelay, relay)
	if err != nil {
		a.logf("主动联系：转述进线失败，交回：%v", err)
		_, _ = h.mgr.SetClaim(conv, seq, proactive.ClaimHanded)
		h.handBackSeq(conv, char, seq, text)
		return true
	}
	h.reschedule(th.ID)
	h.startTurn(th2, rseq, relay, 0, nil)
	return true
}

// overhearLocal 主人从本地私聊发了一句不能认领的话（比如带了上传的图）：不认领，但线发过话而他晚于它回了，就记下"他回过"
func (a *App) overhearLocal(char, conv string) {
	h := a.pthreads
	if h == nil || conv != localConvOf(char) || !h.enabledFor(char) {
		return
	}
	th, ok, err := h.mgr.OpenOf(char)
	if err != nil || !ok || th.Sends == 0 {
		return
	}
	h.overhear(th, a.now())
}

// overhear 旁听：不是认领，也算他回过（取消续醒、热度按它算）；只看有没有发言，不看带没带图
func (h *proactiveHost) overhear(th proactive.Thread, now time.Time) {
	if now.Before(th.LastSendAt) {
		return
	}
	if _, err := h.mgr.Apply(th.ID, proactive.Event{Kind: proactive.EvOverheard, At: now}); err == nil {
		h.reschedule(th.ID)
	}
}

// afterRecover 启动恢复以后要宿主补的两件事（导演和工具都装好、后台开跑时调一次）：
// 被重启关掉的线补做收线（结论、导演系数和下次醒，不然崩溃后 20 分钟她又醒一次），
// 重启时还在等线表态的认领，主人那句已经在库里，起一轮普通回合补回
func (h *proactiveHost) afterRecover() {
	rep := h.recovered
	h.recovered = proactive.RecoverReport{}
	for _, th := range rep.RestartClosed {
		h.afterClose(th)
	}
	for _, c := range rep.LostClaims {
		th, err := h.mgr.Get(c.Thread)
		if err != nil {
			h.a.logf("主动联系：补回丢掉的认领时找不到线 %s：%v", c.Thread, err)
			continue
		}
		st, err := h.a.set.ForConversation(c.Conversation)
		if err != nil {
			h.a.logf("主动联系：补回丢掉的认领时读不到会话：%v", err)
			continue
		}
		text, _, ok, err := st.EventTextID(c.Conversation, c.Seq)
		if err != nil || !ok {
			h.a.logf("主动联系：补回丢掉的认领时找不到那条主人消息（会话 %s，序号 %d）", c.Conversation, c.Seq)
			continue
		}
		h.handBackSeq(c.Conversation, th.Character, c.Seq, text)
	}
}

// handBackPending 把这条线所有还没表态的认领交回普通路径；block 为真时本线内不再认领那个会话（hand_back 工具用）。
// notAfter 非零时只交回这个时间及以前认领的
func (h *proactiveHost) handBackPending(th proactive.Thread, block bool, notAfter time.Time) int {
	claims, err := h.mgr.ClaimsOf(th.ID, proactive.ClaimPending)
	if err != nil {
		return 0
	}
	n := 0
	for _, c := range claims {
		if !notAfter.IsZero() && c.At.After(notAfter) {
			continue
		}
		if h.handBack(th, c, block) {
			n++
		}
	}
	return n
}

// handBack 把一条被认领的主人消息交回普通路径：只在认领记录还是 pending 时做（交回只发生一次），然后用这条已入库的事件起普通回合
func (h *proactiveHost) handBack(th proactive.Thread, c proactive.Claim, block bool) bool {
	// 关机中：ctx 已取消，起的回合会被立刻掐掉。认领留在 pending，下次启动 Recover 记 lost 再补回，不先标成已交回
	if h.a.autoCtx.Err() != nil {
		return false
	}
	changed, err := h.mgr.SetClaim(c.Conversation, c.Seq, proactive.ClaimHanded)
	if err != nil || !changed {
		return false
	}
	if block {
		if err := h.mgr.Block(th.ID, c.Conversation); err != nil {
			h.a.logf("主动联系：本线不再认领 %s 没存上：%v", c.Conversation, err)
		}
	}
	st, err := h.a.set.ForConversation(c.Conversation)
	if err != nil {
		h.a.logf("主动联系：交回时读不到会话：%v", err)
		return true
	}
	text, _, ok, err := st.EventTextID(c.Conversation, c.Seq)
	if err != nil || !ok {
		h.a.logf("主动联系：交回时找不到那条主人消息（会话 %s，序号 %d）", c.Conversation, c.Seq)
		return true
	}
	h.handBackSeq(c.Conversation, th.Character, c.Seq, text)
	return true
}

// handBackSeq 用已入库的主人消息（seq）起一轮普通回合，不再存第二遍
func (h *proactiveHost) handBackSeq(conv, char string, seq int64, text string) {
	a := h.a
	ctx, cancel := context.WithCancel(a.autoCtx)
	a.autoWG.Add(1)
	go func() {
		defer a.autoWG.Done()
		defer cancel()
		out := <-a.hub.SendLocalStored(ctx, char, conv, text, turn.StoredSend{Seqs: []int64{seq}}, nil)
		if out.Err != nil && !errors.Is(out.Err, context.Canceled) {
			a.logf("主动联系：交回的那句没回成：%v", out.Err)
		}
		a.statusChanged(conv)
	}()
}

// ───────────── 别烦、关线（右栏按钮、proactive_snooze 共用） ─────────────

// snooze 主人说别烦：hours 不填用默认、有上限；设 quiet_until，关开着的线（snoozed）。返回实际到什么时候
func (h *proactiveHost) snooze(char string, hours float64, reason string) (time.Time, error) {
	p := h.cfg()
	now := h.a.now()
	if hours <= 0 {
		hours = float64(p.SnoozeDefaultHours)
	}
	if maxH := float64(p.SnoozeMaxDays * 24); hours > maxH {
		hours = maxH
	}
	until := now.Add(time.Duration(hours * float64(time.Hour)))
	if h.dir == nil {
		return until, errors.New("主动联系没有打开")
	}
	if err := h.dir.SetQuiet(char, until, now); err != nil {
		return until, err
	}
	if th, ok, err := h.mgr.OpenOf(char); err == nil && ok {
		h.closeVia(th.ID, proactive.Event{Kind: proactive.EvSnooze, At: now, Conclusion: "主人说别烦：" + proactive.ClipNote(reason, 60)})
	} else {
		h.dir.Log(char, proactive.Row{At: now, Kind: proactive.KindSkip, Reason: proactive.ReasonSnoozed, NextAt: until, ModelNote: proactive.ClipNote(reason, 80)})
	}
	return until, nil
}

// closeNow 右栏"现在别再找了"：关掉这个角色开着的线。这是主人直接说的话，不按"他没回"算冷：
// 关线原因记 snoozed（不动系数），但没有设 quiet_until，所以下次醒回到正常间隔（ThreadClosed 按"不动系数"重排）
func (h *proactiveHost) closeNow(char string) bool {
	th, ok, err := h.mgr.OpenOf(char)
	if err != nil || !ok {
		return false
	}
	closed, did := h.closeVia(th.ID, proactive.Event{Kind: proactive.EvSnooze, Conclusion: "主人让她现在别再找了"})
	if did && h.dir != nil {
		if err := h.dir.ThreadClosed(closed.Character, proactive.CloseQuiet, h.a.now()); err != nil {
			h.a.logf("主动联系：关线后重排导演下次醒失败：%v", err)
		}
	}
	return did
}
