package channel

// 通知（戳一戳、撤回、表情回应、被踢、认不出的）进来的路径（规格-渠道中间件 第六节、第十三节"通知翻译"）。
// 和消息走同一个入库一站、同一条会话队列：同一个会话里的通知和消息一个接一个，插件（戳回去、复读）的状态不会被两条路同时改（清单⑬）。
//
// 通知不是发言：不去重成"同一条消息"，不计闸门、不计提及，所以它存成 events 的一行 kind = notice（Bun 也是这样存的，
// 主窗口要显示"某某撤回了一条消息"，撤回要指向被撤回的那条），不进给模型看的历史（history.toMessage 只收 kind = message）。
//   - 去重：两个号看到同一个群的同一条通知是正常情况，只存一份，各记一条"看到了"。通知没有平台消息号，键是通知各字段（种类、谁、对谁、
//     哪条消息、表情、哪一秒、原始数据的哈希）拼出来的，存在 channel_msgs 里（前缀 notice:，数字串的平台消息号撞不上它）。
//     代价：同一个人在同一秒内戳同一个人两下只存一行——戳回去本来也有冷却，不丢信息；
//   - 撤回、表情回应指向的那条：库里有就连上 events.reply_to（主窗口点"消息"能跳过去；复读、闸门读历史时不把被撤回的算进去），
//     没有（更早的、没存过的）就空着；
//   - 角色自己的号发出的通知不存：她经出口戳出去的，计划表里已经有（origin = automation:poke），回声再存一条就是两条；
//     真人拿着她的号手动戳的这里记不到——很少见，等真有这种需要再说；被踢没有会话，也不存（掉线提醒是第 9 步，同时有一条 Status）；
//   - 准入（白名单外的群、陌生人）和消息一样由平台插件的 AdmitNotice 决定。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// Noticed 是"一条通知处理完了"（Config.OnNotice 的参数）
type Noticed struct {
	Notice    types.Notice
	Character string // 收到它的号绑的角色
	Actor     Actor  // 谁做的（戳的人、撤回的人……），按号认
	Seq       int64  // 存进 events 的那条；没存（被踢、角色自己的号发的）是 0
	EventID   string
	Duplicate bool // 键已经在库里：另一个号先收到了，这个角色第一次看到
	Observed  bool // 这个角色算不算"看到了"（陌生人的没有）
	// Target 是撤回、表情回应指向的那条消息；库里没有（更早的、没存过的）Seq 为 0
	Target store.EventRef
}

// noticeBody 是通知存进 events.segments_json 的样子。notice 这个键和 Bun 里一样（history.IsContextBreak 也读它），值是中立的种类名
type noticeBody struct {
	Notice string `json:"notice"`
	Actor  string `json:"actor,omitempty"`
	Target string `json:"target,omitempty"`
	MsgID  string `json:"msg_id,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Raw    string `json:"raw,omitempty"`
}

// Notice 实现 types.Sink 的 Notice：排进它的会话队列就返回，不等处理。返回的 error 只表示没收下（关了、号不认识、会话填错）
func (n *Intake) Notice(ctx context.Context, nt types.Notice) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	acct, err := n.checkNotice(nt)
	if err != nil {
		return err
	}
	nt.Raw = clipRunes(nt.Raw, types.RawMax) // 认不出的通知只留个样子；活是排队以后才做的，适配器可能已经在复用内存，值拷贝在这里就定了
	key := queueKey(nt.Conv)
	if nt.Kind == types.NoticeKicked {
		key = "account\x00" + acct.Channel + "\x00" + acct.ID // 被踢没有会话，按号排
	}
	ok := n.q.submit(key, job{
		run:  func() { n.processNotice(nt, acct) },
		drop: func() { n.dropped.Add(1) },
	})
	if !ok {
		return ErrClosed
	}
	return nil
}

// checkNotice 收下之前查一遍：种类认识，号认识，会话填对了。被踢是整个号的事，没有会话（types.Notice 的约定），渠道靠号在名册里找
func (n *Intake) checkNotice(nt types.Notice) (Account, error) {
	switch nt.Kind {
	case types.NoticePoke, types.NoticeRecall, types.NoticeReaction, types.NoticeKicked, types.NoticeOther:
	default:
		return Account{}, fmt.Errorf("通知的 Kind %q 不认识", nt.Kind)
	}
	if nt.Kind == types.NoticeKicked {
		acct, ok := n.cfg.Roster.accountByID(nt.Conv.Channel, nt.Account)
		if !ok {
			return Account{}, fmt.Errorf("%w：被踢的号 %s 在名册里找不到（或不止一个渠道有这个号）", ErrUnknownAccount, nt.Account)
		}
		return acct, nil
	}
	channel := nt.Conv.Channel
	if channel == "" {
		return Account{}, errors.New("通知没有渠道")
	}
	acct, ok := n.cfg.Roster.account(channel, nt.Account)
	if !ok {
		return Account{}, fmt.Errorf("%w：%s:%s", ErrUnknownAccount, channel, nt.Account)
	}
	if err := nt.Conv.Valid(); err != nil {
		return Account{}, fmt.Errorf("会话不合法：%w", err)
	}
	if nt.Conv.Scene == types.ScenePrivate && nt.Conv.Self != nt.Account {
		return Account{}, fmt.Errorf("私聊会话属于号 %s，却是号 %s 交来的", nt.Conv.Self, nt.Account)
	}
	return acct, nil
}

func (n *Intake) processNotice(nt types.Notice, acct Account) {
	defer func() {
		if r := recover(); r != nil {
			n.logf("渠道 %s 会话 %s 处理一条通知（%s）时出了意外：%v", nt.Conv.Channel, nt.Conv.String(), nt.Kind, r)
		}
	}()
	var adm Admission
	if nt.Kind != types.NoticeKicked && n.cfg.AdmitNotice != nil {
		adm = n.cfg.AdmitNotice(nt)
	}
	if adm.Drop {
		return
	}
	nd := Noticed{Notice: nt, Character: acct.Character, Observed: !adm.NoObserve}
	if nt.Actor != "" {
		nd.Actor = n.cfg.Roster.Resolve(nt.Conv.Channel, nt.Actor)
	}

	// 被踢没有会话；角色自己的号做的通知见文件头
	if nt.Kind != types.NoticeKicked && nd.Actor.Kind != ActorCharacter {
		res, err := n.storeNotice(nt, acct, nd.Actor, adm)
		if err != nil {
			n.errorf("渠道 %s 会话 %s 通知（%s）入库失败：%v", nt.Conv.Channel, nt.Conv.String(), nt.Kind, err)
			return
		}
		nd.Seq, nd.EventID, nd.Duplicate = res.Seq, res.EventID, res.Duplicate
		if res.Duplicate && res.NewObservations == 0 {
			return // 这个角色早就看到过这条通知（同号重推）：什么也没新增，后面的步骤不再处理一遍
		}
	}
	if nt.MsgID != "" && (nt.Kind == types.NoticeRecall || nt.Kind == types.NoticeReaction) {
		db, err := n.cfg.Stores.For(nt.Conv.Channel)
		if err != nil {
			n.logf("渠道 %s 会话 %s 找通知（%s）指向的那条消息时取库出错：%v", nt.Conv.Channel, nt.Conv.String(), nt.Kind, err)
		} else if ref, ok, err := db.EventByPlatformID(nt.Conv.Channel, nt.Conv.String(), nt.MsgID); err != nil {
			n.logf("渠道 %s 会话 %s 找通知（%s）指向的那条消息时出错：%v", nt.Conv.Channel, nt.Conv.String(), nt.Kind, err)
		} else if ok {
			nd.Target = ref
		}
	}
	if n.cfg.OnNotice != nil {
		n.cfg.OnNotice(nd) // panic 由上面的 recover 接住：队列接着往下走
	}
}

// storeNotice 把通知存成 events 的一行（kind = notice），带去重键和"看到了"
func (n *Intake) storeNotice(nt types.Notice, acct Account, actor Actor, adm Admission) (store.InboundResult, error) {
	at := n.now().UnixMilli()
	if !nt.Time.IsZero() && nt.Time.UnixMilli() > 0 {
		at = nt.Time.UnixMilli()
	}
	origin := OriginPeer
	if actor.Kind == ActorOwner {
		origin = OriginOwner
	}
	var observers []string
	if !adm.NoObserve {
		observers = []string{acct.Character}
	}
	actorKind, actorID := actor.Kind, actor.ID
	if nt.Actor == "" {
		actorKind, actorID, origin = ActorHuman, nt.Conv.Channel+":", OriginPeer // 协议端没说是谁做的（认不出的通知可能这样）
	}
	db, err := n.cfg.Stores.For(nt.Conv.Channel)
	if err != nil {
		return store.InboundResult{}, err
	}
	res, err := db.AppendInbound(store.NewInbound{
		Event: store.NewEvent{
			Channel: nt.Conv.Channel, Conversation: nt.Conv.String(),
			ActorKind: actorKind, ActorID: actorID, Origin: origin, Kind: "notice",
			Text:      noticeText(nt),
			Segments:  noticeBody{Notice: nt.Kind, Actor: nt.Actor, Target: nt.Target, MsgID: nt.MsgID, Ref: nt.Ref, Raw: nt.Raw},
			CreatedAt: at,
		},
		PlatformID:      noticeKey(nt, at),
		ReplyPlatformID: nt.MsgID,
		Observers:       observers,
	})
	if err != nil {
		return res, err
	}
	if !adm.NoObserve {
		n.noteObserved(nt.Conv.Channel, acct.Character, at, res, false)
	}
	return res, nil
}

// noticeText 是通知存进 events.text 的样子（界面的列表、搜索用）。名字是平台的缓存，这里不查，写号
func noticeText(nt types.Notice) string {
	switch nt.Kind {
	case types.NoticePoke:
		return nt.Actor + " 戳了戳 " + nt.Target
	case types.NoticeRecall:
		return nt.Actor + " 撤回了一条消息"
	case types.NoticeReaction:
		return nt.Actor + " 回应了一条消息"
	}
	return "（一条没认出来的通知）"
}

// noticeKey 是通知的去重键：各字段拼起来取哈希，加前缀 notice:。同一条通知两个号各收到一份，字段一样，键就一样
func noticeKey(nt types.Notice, atMS int64) string {
	h := sha256.Sum256([]byte(strings.Join([]string{
		nt.Kind, nt.Actor, nt.Target, nt.MsgID, nt.Ref, strconv.FormatInt(atMS/1000, 10), nt.Raw,
	}, "\x00")))
	return "notice:" + nt.Kind + ":" + hex.EncodeToString(h[:12])
}
