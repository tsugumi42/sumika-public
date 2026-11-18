package memory

import (
	"context"
	"database/sql"
	"errors"
	"slices"
)

// 开后台整理时带旧游标的会话怎么办（规格-记忆写入上线 W5，Q6）。
// Bun 导入把每个会话的整理游标一起搬了过来，所以开整理的第一刻，有游标而游标后面还有聊天的会话会接着旧游标整理，
// 把 Bun 最后一次整理到接管之间、接管后只读期里的聊天都发给后台模型（花钱）。配置 memory.write.catchUp 必须自己选：
//   - latest：对放行场合里"游标落在最新之前"的会话，把游标推到最新，那一段不补；这里的 CatchUpToLatest 做这件事；
//   - cursor：不动游标，落后的按正常排程整理。什么都不用做，所以没有对应的函数。
//
// 只在开整理的第一次启动、以及以后 places 里新增一个取值的那次启动推：memory_meta 里 catchup_places 记着已经处理过的取值（位图），
// 重启不再推；取值从名单里去掉就从位图里去掉，以后再加回来算新增，会再推一次；整理停掉的那次启动位图整个清空（ResetCatchUp），
// 所以"关了整理、过几天再开"也当第一次，不会把停整理期间的聊天接着整理。没有游标的会话不在这里管（启动扫描立游标是"从现在起、不回填"）。

// CatchUpResult 是一次 CatchUpToLatest 的结局。计数不含会话 id
type CatchUpResult struct {
	Done     bool  // 这次没有新增的取值，什么都没做（以前推过了）
	Sessions int   // 游标被推到最新的会话数
	Skipped  int64 // 跳过的事件数（这些会话里游标之后到最新之间的所有事件）
}

// placeBit：取值在位图里的位置
func placeBit(place string) int64 {
	i := slices.Index(AllWritePlaces(), place)
	if i < 0 {
		return 0
	}
	return 1 << i
}

func placesMask(places []string) (mask int64) {
	for _, p := range places {
		mask |= placeBit(p)
	}
	return mask
}

// placesFromMask 把位图里的取值挑出来（按 AllWritePlaces 的顺序）
func placesFromMask(mask int64) []string {
	var out []string
	for _, p := range AllWritePlaces() {
		if mask&placeBit(p) != 0 {
			out = append(out, p)
		}
	}
	return out
}

// CatchUpToLatest：开整理前调一次（memplugin 还没开始排程的时候）。需要写入和整理都开着。
// 新增的取值 = 现在放行的取值里 catchup_places 还没记着的。对每个会话：现在放行、而以前那份名单（记着的取值）下不放行的，
// 有游标而游标比最新小的，把游标推到最新（只往前，不往回）。全部推完才更新 catchup_places；中途出错不更新，下次启动重来（推游标是幂等的）。
// 返回的 Sessions 和 Skipped 给日志用（只有计数）
func (m *Memory) CatchUpToLatest(ctx context.Context) (CatchUpResult, error) {
	return m.catchUpToLatest(ctx, true)
}

func (m *Memory) catchUpToLatest(ctx context.Context, requireEnabled bool) (CatchUpResult, error) {
	w, err := m.writer()
	if err != nil {
		return CatchUpResult{}, err
	}
	if requireEnabled && !m.SummarizeEnabled() {
		return CatchUpResult{}, ErrSummarizeOff
	}
	es, err := m.events()
	if err != nil {
		return CatchUpResult{}, err
	}
	want := placesMask(m.writePlaces())
	done, _, err := m.Meta(ctx, MetaCatchUpPlaces)
	if err != nil {
		return CatchUpResult{}, err
	}
	if want&^done == 0 {
		// 没有新增的取值。取值被去掉了的，位图跟着缩小（再加回来算新增）
		if done != want {
			if err := m.setMeta(ctx, MetaCatchUpPlaces, want); err != nil {
				return CatchUpResult{}, err
			}
		}
		return CatchUpResult{Done: true}, nil
	}
	before := placesFromMask(done)
	heads, err := es.Conversations(ctx)
	if err != nil {
		return CatchUpResult{}, err
	}
	var res CatchUpResult
	for _, h := range heads {
		now, err := m.placeAllowed(ctx, m.writePlaces(), h.Conversation)
		if err != nil {
			return res, err
		}
		if !now {
			continue
		}
		old, err := m.placeAllowed(ctx, before, h.Conversation)
		if err != nil {
			return res, err
		}
		if old {
			continue // 以前那份名单下就放行，早先推过了（或者那时选的是接着整理）
		}
		st, ok, err := m.SummaryState(ctx, h.Conversation)
		if err != nil {
			return res, err
		}
		if !ok || h.LastSeq <= st.LastSeq {
			continue
		}
		skipped, err := countEventsAfter(ctx, es, h.Conversation, st.LastSeq)
		if err != nil {
			return res, err
		}
		moved, err := m.advanceCursor(ctx, h.Conversation, h.LastSeq)
		if err != nil {
			return res, err
		}
		if moved {
			res.Sessions++
			res.Skipped += skipped
		}
	}
	if err := m.setMeta(ctx, MetaCatchUpPlaces, want); err != nil {
		return res, err
	}
	w.logf("记忆[%s]：catchUp=latest：%d 个会话的整理游标推到了最新，跳过 %d 条事件没整理（catchup_latest）", m.opt.Character, res.Sessions, res.Skipped)
	return res, nil
}

// ResetCatchUp：整理这次没开，就把"已处理过的取值"清空（只在写入开着、整理没开的那次启动调，memwrite 的收尾）。
// 不清的话：选了 catchUp=latest、开过整理，出事后停了整理，几天后重新开——位图还记着取值，CatchUpToLatest 当"早先推过了"什么都不做，
// 停整理期间的整段聊天就按旧游标接着整理（花钱），和选 latest "落后的不补" 的本意相反。清空以后下次开整理像第一次一样再推一遍。
// 位图本来没记（没开过）就什么都不写。需要写入开着；整理开不开都行
func (m *Memory) ResetCatchUp(ctx context.Context) error {
	if _, err := m.writer(); err != nil {
		return err
	}
	done, ok, err := m.Meta(ctx, MetaCatchUpPlaces)
	if err != nil {
		return err
	}
	if !ok || done == 0 {
		return nil
	}
	return m.setMeta(ctx, MetaCatchUpPlaces, 0)
}

// countEventsAfter 数游标之后有多少条事件（分页读，只数不留）
func countEventsAfter(ctx context.Context, es EventSource, conv string, after int64) (n int64, err error) {
	for {
		evs, err := es.Events(ctx, conv, after, maxBatchEvents)
		if err != nil {
			return n, err
		}
		fresh := 0
		for _, e := range evs {
			if e.Seq > after {
				after = e.Seq
				fresh++
			}
		}
		n += int64(fresh)
		if fresh == 0 || len(evs) < maxBatchEvents {
			return n, nil
		}
	}
}

// advanceCursor 把一个会话的游标往前推到 to；只往前（比现在小或一样不动）、没有这个会话的行不建。
// 失败次数、下次时间不动。拿写锁，和整理的写互斥。返回有没有推
func (m *Memory) advanceCursor(ctx context.Context, conv string, to int64) (bool, error) {
	w, err := m.writer()
	if err != nil {
		return false, err
	}
	unlock, err := w.lock(ctx, 0)
	if err != nil {
		return false, err
	}
	defer unlock()
	r, err := w.db.ExecContext(ctx, `UPDATE summary_state SET last_seq = ?, updated_at = ? WHERE conversation = ? AND last_seq < ?`, to, w.now().UnixMilli(), conv, to)
	if err != nil {
		return false, err
	}
	n, err := r.RowsAffected()
	return n > 0, err
}

// setMeta 写 memory_meta 的一个键（有就改）。SetMetaIfAbsent 是"首次只记一次"，这个是会变的数
func (m *Memory) setMeta(ctx context.Context, key string, value int64) error {
	w, err := m.writer()
	if err != nil {
		return err
	}
	unlock, err := w.lock(ctx, 0)
	if err != nil {
		return err
	}
	defer unlock()
	_, err = w.db.ExecContext(ctx, `INSERT INTO memory_meta (key, value) VALUES (?, ?) ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	return err
}
