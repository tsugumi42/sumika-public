package turn

// 参考信息块按档存库，重建时插回原位（规格-提示词前缀稳定 第七节，改法 1b）。
//
// 一轮渠道回合拼出的参考信息（时间、对面是谁、记忆召回……）只在实例里，不进事件库；实例一重建（闲了 45 分钟、说话人变了、
// 换 exe）这些块全丢，重建出来的请求在每个块的位置断开。这里把「可以存的」块在回合定了时写进事件库的 ref_blocks 表，
// 重建时照原位插回去：同一档、同一张角色卡、引用的记忆还是原样的才插，三道门任何一道没过，整块不插（失败关闭）。
//
// 能存什么：只有声明了 Contribution.Storable 的贡献（目前只有记忆召回，它的引用重建时核对），加上内核自己拼的几样
// （关系行、计划提醒、报时……）。不认识的插件、用户扩展、带 Notes 的块，默认不存。
// 本地回合（主窗口、本地群聊）不经过交回，不存（规格 7.5）。

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"sumika-native/internal/history"
	"sumika-native/internal/kernel"
	"sumika-native/internal/persona"
	"sumika-native/internal/store"
)

// refBlock 是这一轮拼出来、可以存的一块参考信息。before 为 0 是正文块（排在这一批第一条前面），
// 非 0 是插话时追加的那块（排在这几句插话里最早的那条事件前面）
type refBlock struct {
	text   string
	refs   []MemoryRef
	before int64
}

// refEligible 这一轮的参考信息能不能存：要接了档的算法（Config.RefScope），不是本地回合，不是子运行
func refEligible(cfg Config, in Input) bool {
	if cfg.RefScope == nil || in.local() || in.Identity.Kind == kernel.KindChild {
		return false
	}
	// 装配处给的档（Input.Scope，实例按它分）和听众算出的档对不上，说明装配处算错了档：块存在哪一档都可能串，宁可不存不插
	// （和记忆插件对 Scope 的做法一样）；生产里渠道的 Scope 就是 memory.ScopeKey(听众)
	return in.Scope != "" && cfg.RefScope(in.Audience) == in.Scope
}

// noteRefBlock 记下这一轮拼出的一块（拼完、追加进实例以后调）。unsafe：这一块里有不能存的内容（Notes），整块不存
func (l *Live) noteRefBlock(cfg Config, in Input, text string, got acceptedContext, unsafe bool, before int64) {
	if text == "" || unsafe || !got.Storable || !refEligible(cfg, in) {
		return
	}
	l.refBlocks = append(l.refBlocks, refBlock{text: text, refs: append([]MemoryRef(nil), got.Refs...), before: before})
}

// cardHashOf 是角色卡加系统段的指纹（system 就是两样拼好的）：卡一热更新，旧块不再插
func cardHashOf(system string) string {
	sum := sha256.Sum256([]byte(system))
	return hex.EncodeToString(sum[:])
}

func memoryRefsJSON(refs []MemoryRef) string {
	type r struct {
		ID     string `json:"id"`
		Hash   string `json:"hash"`
		Pinned bool   `json:"pinned,omitempty"`
	}
	out := make([]r, 0, len(refs))
	for _, x := range refs {
		out = append(out, r{x.ID, x.Hash, x.Pinned})
	}
	b, _ := json.Marshal(out)
	return string(b)
}

func parseMemoryRefs(js string) ([]MemoryRef, error) {
	var raw []struct {
		ID     string `json:"id"`
		Hash   string `json:"hash"`
		Pinned bool   `json:"pinned"`
	}
	if err := json.Unmarshal([]byte(js), &raw); err != nil || raw == nil {
		return nil, fmt.Errorf("记忆引用不是 [{id,hash}] 数组：%v", err)
	}
	out := make([]MemoryRef, 0, len(raw))
	for _, x := range raw {
		out = append(out, MemoryRef{ID: x.ID, Hash: x.Hash, Pinned: x.Pinned})
	}
	return out, nil
}

// putRefBlocks 把这一轮拼出的块写进这个会话的事件库，同一个事务里清掉窗口起点之前的块（规格 7.3 窗口裁剪）。
// firstOwn：这一批没有事件时（主动开口），正文块排在她自己的第一句前面。出错整体回滚，库里不留半截。
// 同一块再写一次覆盖，Commit 重做是幂等的。没有这一轮的块也要清理
func (h *Hub) putRefBlocks(st *store.Store, l *Live, firstOwn int64) error {
	in, cfg := l.in, l.cfg
	if !refEligible(cfg, in) {
		return nil
	}
	scope := cfg.RefScope(in.Audience)
	if scope == "" {
		return nil // refEligible 已经挡掉了；缺档不当通配，不存
	}
	var rows []store.RefBlock
	ordinal := map[int64]int{}
	for _, b := range l.refBlocks {
		before := b.before
		if before == 0 {
			if before = minSeq(in.Seqs); before == 0 {
				before = firstOwn
			}
		}
		if before == 0 {
			continue // 没有可以挂的位置（这一批没有事件、她也没说话）：不存
		}
		rows = append(rows, store.RefBlock{
			CharacterID: in.CharacterID, Conversation: in.Conversation, ScopeKey: scope,
			BeforeSeq: before, Ordinal: ordinal[before], CardHash: cardHashOf(l.system),
			MemoryRefsJSON: memoryRefsJSON(b.refs), Text: b.text, CreatedAt: cfg.Now().UnixMilli(),
		})
		ordinal[before]++
	}
	var floor int64
	var err error
	if rebuildFromStart(cfg, in) {
		floor, err = history.WindowFloorFromStart(st.Events, in.CharacterID, in.Conversation)
	} else {
		floor, err = history.WindowFloor(st.Events, in.CharacterID, in.Conversation, cfg.RecentMessages)
	}
	if err != nil {
		return err
	}
	return st.SaveRefBlocks(rows, in.CharacterID, in.Conversation, floor)
}

func minSeq(seqs []int64) int64 {
	var m int64
	for _, s := range seqs {
		if m == 0 || s < m {
			m = s
		}
	}
	return m
}

// refBlocksFor 取重建要插回的块：同角色、同会话、同档、起点不早于重建后留下的第一条消息，按位置排；
// 卡变了的、引用的记忆核对不过的整块不要。读事件库出错就报错，不用部分块
// gatedBlock 是过了三道门、要插回去的一块：库里的行，加解析好的记忆引用
type gatedBlock struct {
	store.RefBlock
	Refs []MemoryRef
}

func refBlocksFor(plug *plugins, cfg Config, st *store.Store, c persona.Character, in Input, system string, firstSeq int64) ([]gatedBlock, error) {
	if !refEligible(cfg, in) || firstSeq <= 0 {
		return nil, nil
	}
	scope := cfg.RefScope(in.Audience)
	if scope == "" {
		return nil, nil
	}
	rows, err := st.ListRefBlocks(c.ID, in.Conversation, scope, firstSeq)
	if err != nil {
		return nil, err
	}
	card := cardHashOf(system)
	now := cfg.Now()
	var out []gatedBlock
	for _, b := range rows {
		if b.CardHash != card || b.Text == "" {
			continue
		}
		refs, err := parseMemoryRefs(b.MemoryRefsJSON)
		if err != nil {
			plug.logf("会话 %s 的一块参考信息引用坏了，不插回：%v", in.Conversation, err)
			continue
		}
		if !plug.refsStillValid(context.Background(), RefCheckIn{CharacterID: c.ID, Conversation: in.Conversation, Audience: in.Audience, Now: now, Refs: refs}) {
			continue
		}
		out = append(out, gatedBlock{RefBlock: b, Refs: refs})
	}
	return out, nil
}

// restoredState 从插回的块里恢复「这个实例里已经给过什么」：最近一块报时的块的时刻（报时隔 15 分钟才再报）、
// 给过的对面是谁（relation 整段出现在某块里就算给过；变了就对不上，照旧再给一次）。
// 只看真的插回去的块：没插回的那些她在实例里看不到，不能算给过
func restoredState(inserted []gatedBlock, relation string) (clockAt time.Time, given string) {
	for i := len(inserted) - 1; i >= 0; i-- {
		text := inserted[i].Text
		if clockAt.IsZero() && strings.HasPrefix(text, ContextHeader+"\n现在是") {
			clockAt = time.UnixMilli(inserted[i].CreatedAt)
		}
		if given == "" && relation != "" && strings.Contains("\n"+text+"\n", "\n"+relation+"\n") {
			given = relation
		}
	}
	return clockAt, given
}

// PurgeRefBlocks 删掉引用了这些记忆的块（记忆删了、被取代、改了文字或范围）。characterID 为空是所有角色（共用池的记忆）。
// 重建时的核对是兜底，这里是清理：块文字是记忆的派生副本，不能比记忆活得久。一个库删失败记日志、接着删下一个
func (h *Hub) PurgeRefBlocks(characterID string, memoryRefs []string) {
	if h.Stores == nil || len(memoryRefs) == 0 {
		return
	}
	for _, n := range h.Stores.Each() {
		if err := n.Store.PurgeRefBlocksByMemory(characterID, memoryRefs); err != nil {
			h.plugins.logf("清理引用了记忆的参考信息块（%s）没成，重建时的核对会兜底：%v", n.Platform, err)
		}
	}
}
