// Multi 是记忆跨平台读事件的路由（规格-聊天记录按平台分库 第五节第 4 步）：每个平台库一个 Source 视图，
// 带会话的入口按会话选库，不限会话的 Find、FindWindow 和 Conversations 跨库查再合并。
// 不自己拆会话串选库（规格第六节）：选库一律交给 store.Stores.ForConversation；记忆包自己的 ChannelOf 只管范围判断，
// 两边拆法一致有对照测试（contrast_test.go）
package memsource

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"

	"sumika-native/internal/memory"
	"sumika-native/internal/store"
)

// Multi 是一个角色的记忆跨平台读聊天记录（memory.EventSource）
type Multi struct {
	stores  store.Stores
	sources []namedSource // 按平台名排好序（store.Each 给的次序，合并时按它保持确定）
	byDB    map[*sql.DB]*Source
}

type namedSource struct {
	platform string
	src      *Source
}

var _ memory.EventSource = (*Multi)(nil)

// NewMulti 给 stores 里的每个平台库各建一个 Source（store.Single 只有一份，同样适用）。
// 每个库建的时候各查一次自己的可选表（发送计划、qq_people）
func NewMulti(stores store.Stores, characterID string) (*Multi, error) {
	named := stores.Each()
	if len(named) == 0 {
		return nil, errors.New("memsource：这组库里一个平台都没有")
	}
	m := &Multi{stores: stores, byDB: make(map[*sql.DB]*Source, len(named))}
	for _, n := range named {
		if n.Store == nil || n.Store.Events == nil {
			return nil, fmt.Errorf("memsource：平台 %s 没有聊天库", n.Platform)
		}
		src, err := New(n.Store, characterID)
		if err != nil {
			return nil, fmt.Errorf("memsource：平台 %s：%w", n.Platform, err)
		}
		m.sources = append(m.sources, namedSource{n.Platform, src})
		m.byDB[n.Store.Events] = src
	}
	return m, nil
}

// route 按会话选库。没登记的会话报错，不落到别的库，也不自己拆会话串
func (m *Multi) route(conv string) (*Source, error) {
	st, err := m.stores.ForConversation(conv)
	if err != nil {
		return nil, fmt.Errorf("memsource：%w", err)
	}
	src := m.byDB[st.Events]
	if src == nil {
		return nil, fmt.Errorf("memsource：会话 %q 的库不在这一组里", conv)
	}
	return src, nil
}

// Events 取这个会话 afterSeq 之后最早的 limit 条，只从会话所在的库读
func (m *Multi) Events(ctx context.Context, conv string, afterSeq int64, limit int) ([]memory.Event, error) {
	src, err := m.route(conv)
	if err != nil {
		return nil, err
	}
	return src.Events(ctx, conv, afterSeq, limit)
}

// Revoked 按（会话，seq）问：seq 只在一个库里唯一，会话决定去哪个库查
func (m *Multi) Revoked(ctx context.Context, conv string, seqs []int64) (map[int64]bool, error) {
	src, err := m.route(conv)
	if err != nil {
		return nil, err
	}
	return src.Revoked(ctx, conv, seqs)
}

// LastSaid 只从会话所在的库读
func (m *Multi) LastSaid(ctx context.Context, conv string) (memory.Event, bool, error) {
	src, err := m.route(conv)
	if err != nil {
		return memory.Event{}, false, err
	}
	return src.LastSaid(ctx, conv)
}

// Find：有会话时只在这个会话所在的库找；不限会话时（recall_raw）每个库的 Source 各取前 limit 条，
// 合并后按 At 降序取前 limit 条。各取前 limit 条就够了，前提是 Source.Find 的"前"也是按时间（created_at，同一毫秒按 seq）：
// 全局的前 limit 条里，属于某一库的那几条在它自己库里按时间也排在前 limit 条以内。seq 跨库不能比，所以不拿 seq 排；
// 同一毫秒按平台名升序、再按 seq 降序，结果稳定（规格第七节"跨库合并按时间排"）
func (m *Multi) Find(ctx context.Context, conv string, like []string, limit int) ([]memory.Event, error) {
	if conv != "" {
		src, err := m.route(conv)
		if err != nil {
			return nil, err
		}
		return src.Find(ctx, conv, like, limit)
	}
	return m.findAcross(ctx, findScope{}, like, limit)
}

// FindWindow 在每个平台搜索同一毫秒时间窗 [sinceAt, untilAt)，排除当前会话，再按 Find 的规则合并。
func (m *Multi) FindWindow(ctx context.Context, excludeConversation string, like []string, sinceAt, untilAt int64, limit int) ([]memory.Event, error) {
	if sinceAt >= untilAt {
		return nil, nil
	}
	return m.findAcross(ctx, findScope{excludeConversation: excludeConversation, window: true, sinceAt: sinceAt, untilAt: untilAt}, like, limit)
}

func (m *Multi) findAcross(ctx context.Context, scope findScope, like []string, limit int) ([]memory.Event, error) {
	if len(like) == 0 || limit <= 0 {
		return nil, nil
	}
	type hit struct {
		platform string
		ev       memory.Event
	}
	var all []hit
	for _, ns := range m.sources {
		evs, err := ns.src.find(ctx, scope, like, limit)
		if err != nil {
			return nil, err
		}
		for _, ev := range evs {
			all = append(all, hit{ns.platform, ev})
		}
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].ev.At != all[j].ev.At {
			return all[i].ev.At > all[j].ev.At
		}
		if all[i].platform != all[j].platform {
			return all[i].platform < all[j].platform
		}
		return all[i].ev.Seq > all[j].ev.Seq
	})
	if len(all) > limit {
		all = all[:limit]
	}
	out := make([]memory.Event, len(all))
	for i, h := range all {
		out[i] = h.ev
	}
	return out, nil
}

// Conversations 各库的拼起来，按 LastAt 降序（跨库 seq 不能比）；同一毫秒按会话串升序，结果稳定
func (m *Multi) Conversations(ctx context.Context) ([]memory.ConvHead, error) {
	var out []memory.ConvHead
	for _, ns := range m.sources {
		hs, err := ns.src.Conversations(ctx)
		if err != nil {
			return nil, err
		}
		out = append(out, hs...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].LastAt != out[j].LastAt {
			return out[i].LastAt > out[j].LastAt
		}
		return out[i].Conversation < out[j].Conversation
	})
	return out, nil
}
