package memory

import (
	"context"
	"slices"
	"sync"
	"testing"
	"time"
)

// 参考信息块引用记忆的指纹（refs.go，规格-提示词前缀稳定 7.2、7.3）：写入时按 Recall 给她看的样子算，重建时 VerifyRefs 重算再比。
// 名字、编号都是编的。

var inGroupOwnerAsked = func(g string) Audience { a := inGroup(g); a.OwnerAsked = true; return a }

type changes struct {
	mu   sync.Mutex
	refs []string
}

func (c *changes) add(r []string) {
	c.mu.Lock()
	c.refs = append(c.refs, r...)
	c.mu.Unlock()
}

func (c *changes) has(ref string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Contains(c.refs, ref)
}

// refRig 是一个可写的记忆，带一条固定要点（boundary，所有听众都会召回）
type refRig struct {
	t    *testing.T
	m    *Memory
	got  *changes
	ref  string
	file string
}

func newRefRig(t *testing.T, scopes ...string) *refRig {
	t.Helper()
	got := &changes{}
	file := migratedDB(t, nil)
	m, err := Open(context.Background(), file, nil, nil, Options{Writable: true, Character: "suzune", Logf: func(string, ...any) {}, OnChanged: got.add})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	if len(scopes) == 0 {
		scopes = []string{"public"}
	}
	mustApply(t, m, Batch{By: BySummary, Ops: []Op{FactOp{Kind: "boundary", Text: "不在群里说主人的住址", Subject: "owner", Status: "active", ConfirmedBy: "owner", Scopes: scopes}}})
	id := str(t, m, `SELECT id FROM facts WHERE text = ?`, "不在群里说主人的住址")
	return &refRig{t: t, m: m, got: got, ref: "f:" + id, file: file}
}

// digests 照写入时的做法：召回一次，取固定要点的指纹（和 memplugin 给块存的是同一份）
func (r *refRig) digests(a Audience, now time.Time) []RefDigest {
	r.t.Helper()
	rec := r.m.Recall(context.Background(), a, Query{Text: "你好", Addressed: true, Now: now})
	var out []RefDigest
	for _, p := range rec.Pinned {
		out = append(out, HitDigest(p))
	}
	if len(out) == 0 {
		r.t.Fatal("没有召回到固定要点")
	}
	return out
}

func TestVerifyRefsFresh(t *testing.T) {
	t.Parallel()
	r := newRefRig(t)
	now := time.Now()
	ds := r.digests(inGroup(GA), now)
	if !r.m.VerifyRefs(context.Background(), inGroup(GA), now, ds) {
		t.Fatal("没动过的记忆核对不过")
	}
	if !r.m.VerifyRefs(context.Background(), inGroup(GA), now, nil) {
		t.Fatal("空引用应当直接过")
	}
}

// 隔了一天，渲染出来的「N 天前」变了，但指纹不含它，还是对得上
func TestVerifyRefsIgnoresRenderedAge(t *testing.T) {
	t.Parallel()
	r := newRefRig(t)
	now := time.Now()
	ds := r.digests(inGroup(GA), now)
	if !r.m.VerifyRefs(context.Background(), inGroup(GA), now.Add(30*time.Hour), ds) {
		t.Fatal("过了一天，指纹不该因为「几天前」变了而对不上")
	}
}

func TestVerifyRefsDeleted(t *testing.T) {
	t.Parallel()
	r := newRefRig(t)
	now := time.Now()
	ds := r.digests(inGroup(GA), now)
	mustApply(t, r.m, Batch{By: ByRemember, Ops: []Op{RevokeOp{ID: idOf(r.ref), Reason: "测试"}}})
	if r.m.VerifyRefs(context.Background(), inGroup(GA), now, ds) {
		t.Fatal("记忆忘掉了，引用它的块还核对得过")
	}
	if !r.got.has(r.ref) {
		t.Fatalf("记忆忘掉了，OnChanged 没报 %s：%v", r.ref, r.got.refs)
	}
}

// 改写 = 取代：旧条目不在了，换成新的一条
func TestVerifyRefsRewritten(t *testing.T) {
	t.Parallel()
	r := newRefRig(t)
	now := time.Now()
	ds := r.digests(inGroup(GA), now)
	mustApply(t, r.m, Batch{By: ByRemember, OnDup: DupReport, Ops: []Op{FactOp{Kind: "boundary", Text: "不在群里说主人的住址和电话", Subject: "owner", Status: "active", ConfirmedBy: "owner", Scopes: []string{"public"}, Supersedes: idOf(r.ref)}}})
	if r.m.VerifyRefs(context.Background(), inGroup(GA), now, ds) {
		t.Fatal("记忆改写了，引用旧版本的块还核对得过")
	}
	if !r.got.has(r.ref) {
		t.Fatalf("记忆被取代，OnChanged 没报旧条目 %s：%v", r.ref, r.got.refs)
	}
}

// 范围改了：同一条记忆，范围从 public 收窄到 owner，给她看的样子（范围进指纹）变了
func TestVerifyRefsScopeChanged(t *testing.T) {
	t.Parallel()
	r := newRefRig(t)
	now := time.Now()
	ds := r.digests(inGroup(GA), now)
	if _, err := r.m.w.db.Exec(`UPDATE item_scopes SET scope = 'owner' WHERE ref = ?`, r.ref); err != nil {
		t.Fatal(err)
	}
	if err := r.m.w.reload(); err != nil {
		t.Fatal(err)
	}
	if r.m.VerifyRefs(context.Background(), inGroup(GA), now, ds) {
		t.Fatal("记忆的范围改了，块还核对得过")
	}
	if !r.got.has(r.ref) {
		t.Fatalf("范围改了，OnChanged 没报 %s", r.ref)
	}
}

// 过期随时间变：写块时没过期，重建时过了（暂时的事记下 TempDays 天以后不再给她）
func TestVerifyRefsExpired(t *testing.T) {
	t.Parallel()
	r := newRefRig(t)
	mustApply(t, r.m, Batch{By: BySummary, Ops: []Op{FactOp{Kind: "promise", Text: "这周六一起吃饭", Subject: "owner", Status: "active", ConfirmedBy: "owner", Scopes: []string{"public"}, Temporal: "temporary"}}})
	now := time.Now()
	var ds []RefDigest
	for _, p := range r.m.Recall(context.Background(), inGroup(GA), Query{Text: "你好", Addressed: true, Now: now}).Pinned {
		ds = append(ds, HitDigest(p))
	}
	if len(ds) != 2 {
		t.Fatalf("固定要点应有两条，实际 %d", len(ds))
	}
	if !r.m.VerifyRefs(context.Background(), inGroup(GA), now, ds) {
		t.Fatal("还没过期，应当核对得过")
	}
	if r.m.VerifyRefs(context.Background(), inGroup(GA), now.Add((TempDays+1)*24*time.Hour), ds) {
		t.Fatal("暂时的事过了期限，块还核对得过")
	}
}

// 隔离：止血线把这条藏起来以后，引用它的块不插
func TestVerifyRefsQuarantined(t *testing.T) {
	t.Parallel()
	r := newRefRig(t)
	mustApply(t, r.m, Batch{By: BySummary, Ops: []Op{FactOp{Kind: "boundary", Text: "整理写的约定", Subject: "owner", Status: "active", ConfirmedBy: "ai", Scopes: []string{"public"}}}})
	now := time.Now()
	var ds []RefDigest
	for _, p := range r.m.Recall(context.Background(), inGroup(GA), Query{Text: "你好", Addressed: true, Now: now}).Pinned {
		ds = append(ds, HitDigest(p))
	}
	if len(ds) != 2 {
		t.Fatalf("固定要点应有两条，实际 %d", len(ds))
	}
	q, err := Open(context.Background(), r.file, nil, nil, Options{Character: "suzune", Logf: func(string, ...any) {}, QuarantineSince: time.Now().Add(-time.Hour).UnixMilli()})
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()
	if q.VerifyRefs(context.Background(), inGroup(GA), now, ds) {
		t.Fatal("整理写的条目被隔离了，引用它的块还核对得过")
	}
	var kept []RefDigest // 主人亲口放行的那条不在隔离范围
	for _, d := range ds {
		if d.Ref == r.ref {
			kept = append(kept, d)
		}
	}
	if len(kept) != 1 || !q.VerifyRefs(context.Background(), inGroup(GA), now, kept) {
		t.Fatal("主人放行的那条不在隔离范围，应当还核对得过")
	}
}

// 「主人私下说的」标记进指纹：群里主人问了才给的私事，主人没问的这一档重算，指纹对不上
func TestVerifyRefsOwnerOnlyMarkInDigest(t *testing.T) {
	t.Parallel()
	r := newRefRig(t, "owner")
	now := time.Now()
	asked := r.digests(inGroupOwnerAsked(GA), now)
	if !r.m.VerifyRefs(context.Background(), inGroupOwnerAsked(GA), now, asked) {
		t.Fatal("同一档重算应当对得上")
	}
	if r.m.VerifyRefs(context.Background(), inGroup(GA), now, asked) {
		t.Fatal("主人问过那一档的块，换成主人没问的听众核对，「主人私下说的」标记不同，应当对不上")
	}
}

// 引用里有一条对不上，整批就是假，不用部分
func TestVerifyRefsOneBadFailsAll(t *testing.T) {
	t.Parallel()
	r := newRefRig(t)
	now := time.Now()
	ds := r.digests(inGroup(GA), now)
	bad := append(slices.Clone(ds), RefDigest{Ref: "f:不存在", Hash: "x"})
	if r.m.VerifyRefs(context.Background(), inGroup(GA), now, bad) {
		t.Fatal("引用里有不存在的记忆，整批应当对不上")
	}
	wrong := []RefDigest{{Ref: ds[0].Ref, Hash: "错的指纹"}}
	if r.m.VerifyRefs(context.Background(), inGroup(GA), now, wrong) {
		t.Fatal("指纹不对应当对不上")
	}
}

// 新增的条目不算「变了」：没有块引用它
func TestChangedRefsIgnoresAdded(t *testing.T) {
	t.Parallel()
	old := map[string]*Item{"f:1": {Ref: "f:1", Text: "甲"}}
	now := map[string]*Item{"f:1": {Ref: "f:1", Text: "甲"}, "f:2": {Ref: "f:2", Text: "乙"}}
	if got := changedRefs(old, now); len(got) != 0 {
		t.Fatalf("没删没改只新增，不该报：%v", got)
	}
	now["f:1"] = &Item{Ref: "f:1", Text: "甲改了"}
	if got := changedRefs(old, now); !slices.Equal(got, []string{"f:1"}) {
		t.Fatalf("改了文字应报 f:1：%v", got)
	}
	delete(now, "f:1")
	if got := changedRefs(old, now); !slices.Equal(got, []string{"f:1"}) {
		t.Fatalf("删了应报 f:1：%v", got)
	}
}
