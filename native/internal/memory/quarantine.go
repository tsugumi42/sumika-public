package memory

import "slices"

// 止血：隔离（规格-记忆写入上线 W5）。开写以后出了事，关开关只是不再写，已经写进库的东西照样会被想起来。
// 把 memory.write.quarantineSince 填成日志里"整理已开"那次的毫秒时间、重启，那之后整理写的、整理提升的东西立刻不再被想起；
// 撤销就是把键清空。只是读侧的过滤，库里一个字节不改。
//
// 隔离什么（事实，满足任一条）：
//   - 创建时间不早于 QuarantineSince；
//   - confirmed_by = ai，并且提升的时间（fact_revisions 里最晚一条 confirm 的 at）不早于 QuarantineSince，或者查不到提升的时间（查不到就当它晚，更紧）。
//     这管的是线之前创建、线之后才被整理攒够证据提升成有效的旧候选（Bun 导入的待确认；打开 autoConfirm 的启动补提升会把它们一起数），
//     只看创建时间的话它们创建在线之前，会照样被想起来。
//
// 例外：confirmed_by = owner 的事实（主人亲口让她记的、亲口放行的，含主人再说一遍同文而提成 owner 的），以及 owner_said 的（remember 写的、来源绑了主人本人的原话）；
// 片段只有整理会写，创建时间不早于线的全部隔离。
// 在哪里生效：召回、memory_search（它走召回）、给整理模型看的已知事实和候选（读侧的 snapshotForRead）。
// 不生效的地方：守卫的"禁看集合"（outbound.go 读原始快照）——被隔离的东西守卫照样继续保护，更紧；
// Items()（考题、管理页）也读原始快照，主人要能看见被隔离的是什么。

// quarantined：这一条该不该被隔离
func (m *Memory) quarantined(it *Item) bool { return quarantinedSince(it, m.opt.QuarantineSince) }

// quarantinedSince 是 quarantined 的规则本体，线（毫秒）由调用方给：记忆页（browse）用它判断"她现在想得起吗"，口径只有这一份
func quarantinedSince(it *Item, since int64) bool {
	if since <= 0 {
		return false
	}
	if it.CreatedAt >= since {
		return !(it.Type == "fact" && (it.ConfirmedBy == "owner" || it.ConfirmedBy == ConfirmedOwnerSaid))
	}
	// 创建在线之前：只有被整理在线之后提升的才藏。ConfirmedAt 为 0 是查不到提升时间（confirmed_by 是 ai 就一定有 confirm 修订，查不到是库不对劲），按晚算
	return it.Type == "fact" && it.ConfirmedBy == "ai" && (it.ConfirmedAt == 0 || it.ConfirmedAt >= since)
}

// QuarantineReport 是隔离生效的回执：线是多少、藏起了多少条。启动日志用它告诉主人这个应急开关到底管没管用（只有计数）
type QuarantineReport struct {
	Active                      bool
	Since                       int64
	Facts, Candidates, Episodes int // 被藏起来的：有效的事实、待确认的事实、片段
}

// QuarantineReport 数一遍原始快照里被隔离的条目。没设线 Active 为假
func (m *Memory) QuarantineReport() QuarantineReport {
	r := QuarantineReport{Since: m.opt.QuarantineSince, Active: m.opt.QuarantineSince > 0}
	if !r.Active {
		return r
	}
	items, _ := m.snapshot()
	for i := range items {
		if !m.quarantined(&items[i]) {
			continue
		}
		switch {
		case items[i].Type == "episode":
			r.Episodes++
		case items[i].Status == "candidate":
			r.Candidates++
		default:
			r.Facts++
		}
	}
	return r
}

// snapshotForRead 是读侧用的条目快照：没设隔离就是 snapshot()；设了就去掉被隔离的，按条目表的版本号缓存（版本号变了才重算）
func (m *Memory) snapshotForRead() ([]Item, map[string]*Item) {
	if m.opt.QuarantineSince <= 0 {
		return m.snapshot()
	}
	items, ver := m.snapshotVer()
	m.qMu.Lock()
	defer m.qMu.Unlock()
	if m.qByRef != nil && m.qVer == ver {
		return m.qItems, m.qByRef
	}
	kept := make([]Item, 0, len(items))
	for i := range items {
		if !m.quarantined(&items[i]) {
			it := items[i]
			if len(it.Previous) > 0 { // 被隔离的旧版本不借着新的露出来
				it.Previous = slices.DeleteFunc(slices.Clone(it.Previous), func(p Item) bool { return m.quarantined(&p) })
			}
			kept = append(kept, it)
		}
	}
	m.qItems, m.qByRef, m.qVer = kept, refIndex(kept), ver
	return m.qItems, m.qByRef
}
