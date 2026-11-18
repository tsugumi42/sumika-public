package memory

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// 证据动作落到写入层（规格-记忆写入上线 W3a）：EvidenceOp 只给待确认的事实记一行 fact_evidence，别的什么都不动。
// 名字、会话、号码都是编的

// evDay 是某一天中午（本地时间），证据的"哪一天"按本地时区算
func evDay(d int) int64 { return time.Date(2026, 10, d, 12, 0, 0, 0, time.Local).UnixMilli() }

func evOp(factID, conv string, day int, bySubject bool) EvidenceOp {
	return EvidenceOp{FactID: factID, Evidence: Evidence{Conversation: conv, At: evDay(day), Seq: int64(day), Speaker: "owner", BySubject: bySubject}}
}

// summaryFor：整理批，听众是 conv 里的主人（PrivateOK：主人的私事 owner 范围看得见）。转述的证据只在候选对听众看得见时才记（W3b），
// 本文件里的候选范围是 owner，所以用这个听众
func summaryFor(conv string, ops ...Op) Batch {
	return Batch{By: BySummary, Ops: ops, Audience: &Audience{Channel: ChannelOf(conv), Conversation: conv, Scene: "private", PrivateOK: true}, Wait: testLockWait}
}

// 一条待确认的事实：记证据只多一行 fact_evidence，状态、范围、修订、谁放行的一个字节都不变；同一天同一会话还是一次，本人说的补上"本人说过"
func TestEvidenceOpRecordsOneRowAndNothingElse(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	id := idOf(mustApply(t, m, summary(candidate("主人喜欢喝茶", "owner", "owner"))).Outcomes[0].Ref)
	before := rowOf(t, m, id)
	rows := func() int { return count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ?`, id) }
	if rows() != 0 {
		t.Fatalf("没带证据写的候选一开始没有证据行：%d", rows())
	}

	res := mustApply(t, m, summaryFor("console:a", evOp(id, "console:a", 20, false)))
	if o := res.Outcomes[0]; o.Result != ResultEvidence || o.Status != "candidate" || res.Counts[CountEvidenceAdded] != 1 || res.Counts[CountEvidenceGone] != 0 {
		t.Errorf("第一次：结局 %+v，计数 %v", o, res.Counts)
	}
	// 另一个会话、另一天：各是一次
	mustApply(t, m, summaryFor("qq:private:20001:7001", evOp(id, "qq:private:20001:7001", 20, false)))
	mustApply(t, m, summaryFor("console:a", evOp(id, "console:a", 21, false)))
	if rows() != 3 {
		t.Fatalf("三个不同的（天，会话）该是 3 行：%d", rows())
	}
	// 同一天同一会话又说一遍：还是一次
	res = mustApply(t, m, summaryFor("console:a", evOp(id, "console:a", 20, false)))
	if rows() != 3 || res.Counts[CountEvidenceRepeat] != 1 || res.Counts[CountEvidenceAdded] != 0 {
		t.Errorf("同一天同一会话：行数 %d，计数 %v", rows(), res.Counts)
	}
	// 同一天同一会话，这回是本人说的：补上"本人说过"，行数不变
	res = mustApply(t, m, summaryFor("console:a", evOp(id, "console:a", 20, true)))
	if by := count(t, m, `SELECT sum(by_subject) FROM fact_evidence WHERE fact_id = ?`, id); rows() != 3 || by != 1 || res.Counts[CountEvidenceBySelf] != 1 {
		t.Errorf("补上本人说过：行数 %d，本人说的 %d，计数 %v", rows(), by, res.Counts)
	}
	if after := rowOf(t, m, id); after != before {
		t.Errorf("记证据不该动事实本身：\n之前 %s\n之后 %s", before, after)
	}
	if n := count(t, m, `SELECT count(*) FROM fact_revisions WHERE fact_id = ?`, id); n != 1 {
		t.Errorf("记证据不记修订，只有创建时那一条：%d", n)
	}
}

// 形状 × 入口：被记证据的那一条是什么状态。手写的预期：只有待确认的记，别的（有效、被取代、撤销、AI 没通过、编号不存在）一律不记、数 evidence_gone，
// 库里一个字节没变。形状表复用取代保护那张（它的"待确认"一行就是唯一记得上的）
func TestEvidenceOpOnlyRecordsOnACandidate(t *testing.T) {
	t.Parallel()
	records := map[string]bool{"待确认": true}
	shapes := append(append([]protectShape{}, protectShapes...), protectShape{name: "编号不存在"})
	for _, sh := range shapes {
		t.Run(sh.name, func(t *testing.T) {
			t.Parallel()
			m, _ := newWritable(t, nil)
			id := "NOSUCH"
			if sh.status != "" {
				id = seedShape(t, m, sh, "测试用的事实 "+sh.name)
			}
			before := ""
			if sh.status != "" {
				before = rowOf(t, m, id)
			}
			res := mustApply(t, m, summary(evOp(id, "console:a", 20, true)))
			rows := count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ?`, id)
			want := records[sh.name]
			switch {
			case want && (rows != 1 || res.Counts[CountEvidenceAdded] != 1 || res.Counts[CountEvidenceGone] != 0 || res.Outcomes[0].Result != ResultEvidence):
				t.Errorf("待确认的该记一行：行数 %d，计数 %v，结局 %+v", rows, res.Counts, res.Outcomes[0])
			case !want && (rows != 0 || res.Counts[CountEvidenceAdded] != 0 || res.Counts[CountEvidenceGone] != 1 || res.Outcomes[0].Result != ResultNoop):
				t.Errorf("不是待确认的不该记：行数 %d，计数 %v，结局 %+v", rows, res.Counts, res.Outcomes[0])
			}
			if sh.status != "" && rowOf(t, m, id) != before {
				t.Errorf("事实本身变了：\n之前 %s\n之后 %s", before, rowOf(t, m, id))
			}
		})
	}
}

// 证据动作只有整理能给，编号、会话必须写；不合规整批不写
func TestEvidenceOpBatchRules(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	id := idOf(mustApply(t, m, summary(candidate("主人喜欢喝茶", "owner", "owner"))).Outcomes[0].Ref)
	for _, c := range []struct {
		name string
		b    Batch
	}{
		{"remember 带证据动作", Batch{By: ByRemember, OnDup: DupReport, Ops: []Op{evOp(id, "console:a", 20, true)}}},
		{"没写编号", summary(evOp("", "console:a", 20, true))},
		{"没写会话", summary(evOp(id, "", 20, true))},
		{"好的后面跟着坏的：整批不写", summary(evOp(id, "console:a", 20, true), evOp("", "console:a", 21, true))},
	} {
		t.Run(c.name, func(t *testing.T) {
			if _, err := m.apply(context.Background(), c.b); err == nil {
				t.Fatal("该报错")
			}
			if n := count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ?`, id); n != 0 {
				t.Errorf("一行都不该写：%d", n)
			}
		})
	}
}

// 边界 ⑬：几个会话同时给同一条候选记证据，写锁串起来，不同的（天，会话）各一行，同一个（天，会话）只有一行
func TestEvidenceOpConcurrentSessions(t *testing.T) {
	t.Parallel()
	m, _ := newWritable(t, nil)
	id := idOf(mustApply(t, m, summary(candidate("主人喜欢喝茶", "owner", "owner"))).Outcomes[0].Ref)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			// 4 个会话，每个会话有两个人同时来（一个本人说的、一个转述的，谁先到不定）；每个会话只该留一行
			conv := fmt.Sprintf("qq:private:20001:700%d", i%4)
			if _, err := m.apply(context.Background(), summaryFor(conv, evOp(id, conv, 20, i < 4))); err != nil {
				t.Errorf("apply：%v", err)
			}
		}()
	}
	wg.Wait()
	if n := count(t, m, `SELECT count(*) FROM fact_evidence WHERE fact_id = ?`, id); n != 4 {
		t.Errorf("4 个会话该是 4 行：%d", n)
	}
	if by := count(t, m, `SELECT sum(by_subject) FROM fact_evidence WHERE fact_id = ?`, id); by != 4 {
		t.Errorf("每个会话都有一次是本人说的，4 行都该是本人说过（先到后到都一样）：%d", by)
	}
}
