package channel

// 流水线：入库一站之后、闸门之前的几步接在一起（规格-渠道中间件 第六节第 8 步到第七节）。
// 入库一站（intake.go、intake_notice.go）每存好一条消息、处理完一条通知，调这里的 OnStored、OnNotice——两个都在那个会话的队列里、
// 一个接一个调，所以这里不用再排队；它们不能卡住（卡住就堵住这个会话的下一条），问插件的时限在链里。
//
// 一条消息的路：观察（算 Addressed）→ 交给观察者（Observers，只排任务）→ 拦截链 → 交给闸门（Gate 钩子）。
//   - 只有"这个角色实时看到的、别人说的"才往下走：补录的（只入库、只观察，不进链、不触发回复）、陌生人和自检的（不回不分析）、
//     她自己说的（她的号发的，复读不能跟自己），都不进链也不交给闸门——闸门要数她说过几句、补录占多少，自己读库；
//   - 链里有插件接了，Dispatch.Result.Taken 为真：闸门照常收到，只计数、更新退避，不做放行判断，这一批也就不会交给内核；
//   - 同一条消息两个号都看到、绑了两个角色，每个角色各过一遍链、各交一次（OnStored 就是这样调的）。
//
// 一条通知的路：撤回先告诉 OnRecall（第 3 步接上以后，她正准备回的就是被撤回的这条时取消这一轮），再过链（戳回去）。通知不交给闸门：
// "通知本来就不计"，戳她不等于叫她（规格 第三节），同一批里的 @ 提问照常走自己的路。
//
// 闸门（第 5 步）和内核（第 3 步）还没有：Gate、OnRecall 是给它们留的口，nil 就什么也不做。

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

// Dispatch 是交给闸门的一批和它过链的结果
type Dispatch struct {
	Batch  types.Batch
	Result ChainResult
}

// Recalled 是"有人撤回了一条消息"，交给第 3 步：它的回合在跑，批里正好有被撤回的这条（TargetSeq 在 Input.Seqs 里），就取消这一轮。
// 规格-群聊闸门 2.2 写的是"她正准备回的就是这条，取消这次回复；她已经回了的不再追着说"；Bun 里没有这个行为（只有闸门读批时跳过被撤回的，
// 第二十节第 12 条查过），所以这是新做的
type Recalled struct {
	Character string
	Conv      types.Conversation
	Actor     string // 撤回的人（平台号）；群管理员撤回别人的话，和被撤回那条的作者不是一个人
	MsgID     string // 平台消息号
	TargetSeq int64  // 被撤回的那条在库里的 seq；库里没有（更早的、没存过的）是 0
	TargetID  string
}

// PipelineConfig 是流水线的配置
type PipelineConfig struct {
	// Stores 是按渠道取库的那一组（ADR-199）：读引用、读最近几条都按会话的渠道取，取不到就报错，不落到别的库
	Stores store.Stores
	// Roster 要和入库一站用的是同一份（"叫到她"认她的每一个号靠它）
	Roster  Roster
	Seen    SeenNotifier // nil 表示尚未接入内核
	Chain   *Chain
	Aliases func(character string) []string // 和闸门同一份主人填写的叫名表
	// Gate 接过链的结果（第 5 步的闸门挂在这里）；nil 就是没有闸门
	Gate func(Dispatch)
	// OnRecall 接撤回（第 3 步的回合挂在这里）；nil 就是什么也不做
	OnRecall func(Recalled)
	// Observers 在过链之前看到每一条进链的消息（入库以后的观察，见 Observer）
	Observers []Observer
	// OnMessage 在角色档位和观察过滤之前接一次已提交入站消息，仅允许快速入队。
	OnMessage func(Stored)
	// Control 在 OnStored 最前面看每一条已入库的消息（规格-主人QQ私聊审批）：返回 true 表示这条是对审批的回答，
	// 到此为止，不进观察、拦截链、闸门和模型；返回 false 照常往下走。nil 就是没有这一道。要快、不阻塞：它在会话队列里调用。
	Control func(Stored) bool
	Logf    func(format string, args ...any)
}

// Observer 是入库以后的观察者（插件统一规范草案 v0.1 叫 Message.Observe；规格-链接解析 8.2 的"纯提链入队"）。
// Observe 在会话队列里同步调用，只许排任务、马上返回：联网、调模型放进它自己的队列，生命周期它自己管（Close 时取消、等收尾）。
// 不保证每条都送到：队列满了它自己丢、自己记日志；要可靠的另做持久任务
type Observer interface {
	Name() string
	Observe(Observation)
}

// Observation 是交给观察者的一条。和进拦截链的是同一批：这个角色实时看到的、别人说的、会话在说话或试运行；
// 补录、陌生人、自检、她自己说的都不给。同一条两个角色都看到，各给一次（Duplicate 为真的是第二次以后）。
// 段直接给（消息已经在内存里）；观察者要据此做什么，权限照样按 Character、Conv 自己核
type Observation struct {
	Character string
	Conv      types.Conversation
	Seq       int64
	Origin    string
	Segs      []types.Seg
	Duplicate bool
}

// Pipeline 是流水线
type Pipeline struct {
	cfg    PipelineConfig
	ctx    context.Context
	cancel context.CancelFunc
}

// NewPipeline 造流水线
func NewPipeline(cfg PipelineConfig) (*Pipeline, error) {
	if cfg.Stores == nil {
		return nil, errors.New("没有库")
	}
	if cfg.Chain == nil {
		return nil, errors.New("没有拦截链")
	}
	p := &Pipeline{cfg: cfg}
	p.ctx, p.cancel = context.WithCancel(context.Background())
	return p, nil
}

// Close 关闭：正在问的插件收到到期的 ctx，之后 OnStored、OnNotice 什么也不做，也不再碰闸门和库（清单④）。
// 关闭的顺序是：Pipeline.Close → 入库一站 Close（等在做的做完）→ 出口 Close → 关库
func (p *Pipeline) Close() { p.cancel() }

func (p *Pipeline) logf(format string, args ...any) {
	if p.cfg.Logf != nil {
		p.cfg.Logf(format, args...)
		return
	}
	p.cfg.Chain.logf(format, args...)
}

// OnStored 接 Config.OnStored
func (p *Pipeline) OnStored(s Stored) {
	if p.ctx.Err() == nil && p.cfg.Control != nil && p.cfg.Control(s) {
		return
	}
	if p.ctx.Err() == nil && p.cfg.OnMessage != nil {
		p.cfg.OnMessage(s)
	}
	if p.ctx.Err() == nil && !s.Duplicate && p.cfg.Seen != nil && s.Inbound.Conv.Channel != "console" {
		mode := p.cfg.Chain.modeOf(s.Inbound.Conv)
		if mode == ModeSpeak || mode == ModeDryRun {
			p.cfg.Seen.NotifySeen(turn.Seen{CharacterID: s.Character, Conversation: s.Inbound.Conv.String(), Seq: s.Seq,
				Backfill: slices.Contains(s.Tags, TagBackfill), Stranger: slices.Contains(s.Tags, "stranger"),
				FromSelf: slices.Contains(s.Tags, TagFromSelf), Selftest: slices.Contains(s.Tags, "selftest")})
		}
	}
	if p.ctx.Err() != nil || !s.Observed || slices.Contains(s.Tags, TagBackfill) {
		return
	}
	if s.Actor.Kind == ActorCharacter && s.Actor.ID == s.Character {
		return
	}
	// 不接话的人仍可见、照发看到了的通知，但不能驱动插件、取链接或回复。
	if !p.cfg.Roster.CanTrigger(s.Inbound.Conv.Channel, s.Actor.ID) {
		return
	}
	if m := p.cfg.Chain.modeOf(s.Inbound.Conv); m == ModeSpeak || m == ModeDryRun {
		p.observe(Observation{Character: s.Character, Conv: s.Inbound.Conv, Seq: s.Seq, Origin: s.Origin, Segs: s.Inbound.Segs, Duplicate: s.Duplicate})
	}
	in := s.Inbound
	now := p.cfg.Chain.Now()
	at := in.Time
	if at.IsZero() {
		at = now
	}
	addressed := p.addressed(s)
	b := types.Batch{
		Conv: in.Conv, Character: s.Character, Account: in.Account, Now: now, Trigger: types.TriggerMessage,
		Msgs:      []types.Msg{{Seq: s.Seq, Time: at, Who: s.Actor.ID, Origin: s.Origin, Segs: in.Segs, Name: nameOf(in.From), Addressed: addressed}},
		Addressed: addressed,
		Recent:    p.recentFunc(in.Conv, s.Seq, s.Character),
	}
	res := p.cfg.Chain.Run(p.ctx, b)
	if p.ctx.Err() != nil || p.cfg.Gate == nil {
		return
	}
	p.cfg.Gate(Dispatch{Batch: b, Result: res})
}

// observe 挨个交给观察者；一个 panic 不耽误别的观察者和后面的拦截链
func (p *Pipeline) observe(o Observation) {
	for _, ob := range p.cfg.Observers {
		func() {
			defer func() {
				if r := recover(); r != nil {
					p.logf("观察者 %s 处理会话 %s 第 %d 条时出错：%v", ob.Name(), o.Conv.String(), o.Seq, r)
				}
			}()
			ob.Observe(o)
		}()
	}
}

// OnNotice 接 Config.OnNotice
func (p *Pipeline) OnNotice(nd Noticed) {
	nt := nd.Notice
	if p.ctx.Err() != nil || !nd.Observed || nt.Kind == types.NoticeKicked {
		return
	}
	if nt.Kind == types.NoticeRecall && p.cfg.OnRecall != nil {
		p.cfg.OnRecall(Recalled{
			Character: nd.Character, Conv: nt.Conv, Actor: nt.Actor, MsgID: nt.MsgID,
			TargetSeq: nd.Target.Seq, TargetID: nd.Target.ID,
		})
	}
	if !p.cfg.Roster.CanTrigger(nt.Conv.Channel, p.cfg.Roster.Resolve(nt.Conv.Channel, nt.Actor).ID) {
		return
	}
	b := types.Batch{
		Conv: nt.Conv, Character: nd.Character, Account: nt.Account, Now: p.cfg.Chain.Now(), Trigger: nt.Kind, Notice: &nt,
		Mine: p.cfg.Roster.accountsOf(nt.Conv.Channel, nd.Character),
	}
	p.cfg.Chain.Run(p.ctx, b)
}

// addressed 在链前统一计算 @、引用她、主人填写的别名；叫名字也必须让复读让路。
func (p *Pipeline) addressed(s Stored) bool {
	st, err := p.cfg.Stores.For(s.Inbound.Conv.Channel)
	if err != nil {
		p.logf("渠道 %s 会话 %s 判断引用的是不是她的话时取库出错：%v，当没叫到", s.Inbound.Conv.Channel, s.Inbound.Conv.String(), err)
		return false
	}
	return addressedBy(st, p.cfg.Roster, s.Inbound.Conv, s.Character, s.Inbound.Segs, p.logf) || aliasInSegs(p.cfg.Aliases, s.Character, s.Inbound.Segs)
}

// addressedBy 是 addressed 的本体：闸门给"她上次看完以后攒下的那几条"补算每一条是不是叫到了她，用的是同一个判断
func addressedBy(st *store.Store, roster Roster, conv types.Conversation, character string, segs []types.Seg, logf func(string, ...any)) bool {
	mine := roster.accountsOf(conv.Channel, character)
	for _, seg := range segs {
		switch seg.Kind {
		case types.SegAt:
			if seg.Target != types.AtAll && slices.Contains(mine, seg.Target) {
				return true
			}
		case types.SegReply:
			ref, ok, err := st.EventByPlatformID(conv.Channel, conv.String(), seg.Target)
			if err != nil {
				logf("渠道 %s 会话 %s 判断引用的是不是她的话时查库出错：%v，当没叫到", conv.Channel, conv.String(), err)
				continue
			}
			if ok && ref.ActorKind == ActorCharacter && ref.ActorID == character {
				return true
			}
		}
	}
	return false
}

// recentFunc 造 Batch.Recent：这一批（seq 为 beforeSeq 的这条）之前的最近 n 条消息
func (p *Pipeline) recentFunc(conv types.Conversation, beforeSeq int64, character string) func(n int) ([]types.Msg, error) {
	return recentFuncFor(p.cfg.Stores, conv, beforeSeq, character)
}

// recentFuncFor 按会话的渠道取库，再造 Batch.Recent；取不到库（没登记的渠道）时这个函数返回那个错误，
// 不落到别的库。闸门放行的那一批也用这里（闸门和流水线读的是同一份）
func recentFuncFor(stores store.Stores, conv types.Conversation, beforeSeq int64, character string) func(n int) ([]types.Msg, error) {
	st, err := stores.For(conv.Channel)
	if err != nil {
		return func(int) ([]types.Msg, error) { return nil, err }
	}
	return recentFunc(st, conv, beforeSeq, character)
}

func recentFunc(st *store.Store, conv types.Conversation, beforeSeq int64, character string) func(n int) ([]types.Msg, error) {
	return func(n int) ([]types.Msg, error) {
		rows, err := st.RecentMessages(conv.String(), beforeSeq, n)
		if err != nil {
			return nil, err
		}
		out := make([]types.Msg, len(rows))
		for i, r := range rows {
			out[i] = types.Msg{
				Seq: r.Seq, Time: time.UnixMilli(r.CreatedAt), Who: r.ActorID, Origin: r.Origin,
				Self: r.ActorKind == ActorCharacter && r.ActorID == character,
			}
			// 平台来的消息存的是 {"segs": [...]}；她经出口发的是 {"parts": [...]}，没有段。解不出来就没有段（比较时视为不可比）
			if r.Segments.Valid {
				out[i].Segs, out[i].Name = segsAndName(r.Segments.String)
			}
		}
		return out, nil
	}
}

// segsAndName 从存好的 segments_json 里取出段和发言当时的名字；解不出来都是空
func segsAndName(raw string) ([]types.Seg, string) {
	var body struct {
		Segs []types.Seg `json:"segs"`
		From types.User  `json:"from"`
	}
	if json.Unmarshal([]byte(raw), &body) != nil {
		return nil, ""
	}
	return body.Segs, nameOf(body.From)
}

// nameOf 是发言人给模型看的名字：群名片 > 昵称 > 号（名字是平台的缓存，会变；身份认号，不认名字）
func nameOf(u types.User) string {
	switch {
	case u.Card != "":
		return u.Card
	case u.Nick != "":
		return u.Nick
	}
	return u.ID
}
