package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// catchUp=latest（规格-记忆写入上线 W5 验收 6）。会话、号码、话都是编的；预期手写

const (
	cuOwnerPriv = "qq:private:20001:60001" // 主人的私聊
	cuOtherPriv = "qq:private:20001:60002" // 别人的私聊
	cuGroup     = "qq:group:5001001"
	cuGroup2    = "qq:group:5001002" // 没有游标的群
	cuLocal     = "console:suzune"
	cuForeign   = "telegram:private:1:2"
)

// cuRig：每个会话 5 条话；newRig 给 cuOwnerPriv 立了游标 0，别的会话的游标由各个测试自己立。cuGroup2 一直没有游标行
func cuRig(t *testing.T, places []string, mod ...func(*Options)) *rig {
	t.Helper()
	r := newRig(t, cuOwnerPriv, rigOpts{mod: func(o *Options) {
		o.WritePlaces = places
		for _, f := range mod {
			f(o)
		}
	}})
	for _, c := range []struct {
		conv string
		who  actor
	}{{cuOwnerPriv, ownerA}, {cuOtherPriv, pingA}, {cuGroup, ownerA}, {cuGroup2, ownerA}, {cuLocal, ownerA}, {cuForeign, ownerA}} {
		for i := 0; i < 5; i++ {
			r.ev.add(c.conv, c.who, fmt.Sprintf("编的第%d句", i+1))
		}
	}
	return r
}

func (r *rig) cursorOf(t *testing.T, conv string) (seq int64, ok bool) {
	t.Helper()
	st, ok, err := r.m.SummaryState(context.Background(), conv)
	if err != nil {
		t.Fatal(err)
	}
	return st.LastSeq, ok
}

func (r *rig) setCursor(t *testing.T, conv string, seq int64) {
	t.Helper()
	if _, err := r.m.InitCursor(context.Background(), conv, seq); err != nil {
		t.Fatal(err)
	}
}

// firstSeqOf：这个会话第一条的 seq（编的事件来源里 seq 是各会话共用一串数，会话里第 n 条的 seq 不是 n）
func (r *rig) firstSeqOf(conv string) int64 {
	for _, e := range r.ev.evs {
		if e.Conversation == conv {
			return e.Seq
		}
	}
	return 0
}

func (r *rig) lastSeqOf(conv string) (last int64) {
	for _, e := range r.ev.evs {
		if e.Conversation == conv {
			last = e.Seq
		}
	}
	return
}

func TestCatchUpLatest(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := cuRig(t, []string{WritePlaceOwnerPrivate})
	r.setCursor(t, cuOtherPriv, r.firstSeqOf(cuOtherPriv)+1) // 看过 2 条，还剩 3 条
	r.setCursor(t, cuGroup, r.firstSeqOf(cuGroup))
	r.setCursor(t, cuLocal, 0)
	r.setCursor(t, cuForeign, 0)
	before := map[string]int64{}
	for _, c := range []string{cuOwnerPriv, cuOtherPriv, cuGroup, cuLocal, cuForeign} {
		before[c], _ = r.cursorOf(t, c)
	}

	res, err := r.m.CatchUpToLatest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Done || res.Sessions != 1 || res.Skipped != 5 {
		t.Fatalf("第一次：只有主人的私聊在放行名单里，推 1 个会话、跳过 5 条：%+v", res)
	}
	if seq, _ := r.cursorOf(t, cuOwnerPriv); seq != r.lastSeqOf(cuOwnerPriv) {
		t.Errorf("主人的私聊该推到最新 %d，得到 %d", r.lastSeqOf(cuOwnerPriv), seq)
	}
	for _, c := range []string{cuOtherPriv, cuGroup, cuLocal, cuForeign} {
		if seq, _ := r.cursorOf(t, c); seq != before[c] {
			t.Errorf("%s 不在放行名单里，游标不该动：%d → %d", c, before[c], seq)
		}
	}
	if _, ok := r.cursorOf(t, cuGroup2); ok {
		t.Error("没有游标的会话不在这里管，不该建出游标")
	}
	if mask, ok, _ := r.m.Meta(ctx, MetaCatchUpPlaces); !ok || mask != placeBit(WritePlaceOwnerPrivate) {
		t.Errorf("catchup_places 该记 owner-private：%d %v", mask, ok)
	}
	if !strings.Contains(r.sink.String(), "catchup_latest") {
		t.Errorf("日志该有一行：%q", r.sink.String())
	}
	if strings.Contains(r.sink.String(), "20001") || strings.Contains(r.sink.String(), "60001") {
		t.Errorf("日志里有会话 id：%q", r.sink.String())
	}

	// 重启：名单没变，不再推。主人的私聊在这之后又来了新话，也不动
	r.ev.add(cuOwnerPriv, ownerA, "后来又说的")
	cur, _ := r.cursorOf(t, cuOwnerPriv)
	res, err = r.m.CatchUpToLatest(ctx)
	if err != nil || !res.Done || res.Sessions != 0 {
		t.Fatalf("名单没变该什么都不做：%+v %v", res, err)
	}
	if seq, _ := r.cursorOf(t, cuOwnerPriv); seq != cur {
		t.Fatalf("重启后游标又被推了：%d → %d", cur, seq)
	}
}

// 以后名单里新增取值的那次启动再推一次，只推新放行的会话，原来的不动；取值去掉再加回来算新增
func TestCatchUpWhenPlacesGrow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := cuRig(t, []string{WritePlaceOwnerPrivate})
	r.setCursor(t, cuOtherPriv, r.firstSeqOf(cuOtherPriv)+1)
	groupCur := r.firstSeqOf(cuGroup)
	r.setCursor(t, cuGroup, groupCur)
	if _, err := r.m.CatchUpToLatest(ctx); err != nil {
		t.Fatal(err)
	}
	ownerCur, _ := r.cursorOf(t, cuOwnerPriv)
	r.ev.add(cuOwnerPriv, ownerA, "又来一句")

	// 新增 platform-private：别人的私聊推到最新，主人的私聊原来就放行、不再推
	r.m.opt.WritePlaces = []string{WritePlaceOwnerPrivate, WritePlacePlatformPrivate}
	res, err := r.m.CatchUpToLatest(ctx)
	if err != nil || res.Done || res.Sessions != 1 || res.Skipped != 3 {
		t.Fatalf("新增私聊：只推别人的私聊（还剩 3 条）：%+v %v", res, err)
	}
	if seq, _ := r.cursorOf(t, cuOtherPriv); seq != r.lastSeqOf(cuOtherPriv) {
		t.Errorf("别人的私聊该推到最新：%d", seq)
	}
	if seq, _ := r.cursorOf(t, cuOwnerPriv); seq != ownerCur {
		t.Errorf("主人的私聊原来就放行，不该再推：%d → %d", ownerCur, seq)
	}
	if seq, _ := r.cursorOf(t, cuGroup); seq != groupCur {
		t.Errorf("群还没放行，游标不该动：%d", seq)
	}

	// 新增 platform-group
	r.m.opt.WritePlaces = []string{WritePlaceOwnerPrivate, WritePlacePlatformPrivate, WritePlacePlatformGroup}
	res, err = r.m.CatchUpToLatest(ctx)
	if err != nil || res.Sessions != 1 {
		t.Fatalf("新增群：%+v %v", res, err)
	}
	if seq, _ := r.cursorOf(t, cuGroup); seq != r.lastSeqOf(cuGroup) {
		t.Errorf("群该推到最新：%d", seq)
	}
	if _, ok := r.cursorOf(t, cuGroup2); ok {
		t.Error("没有游标的群不该被建出游标")
	}

	// platform-group 去掉：位图缩小；群里攒了新话，再加回来算新增，推一次
	r.m.opt.WritePlaces = []string{WritePlaceOwnerPrivate, WritePlacePlatformPrivate}
	if res, err = r.m.CatchUpToLatest(ctx); err != nil || !res.Done {
		t.Fatalf("去掉取值不推：%+v %v", res, err)
	}
	if mask, _, _ := r.m.Meta(ctx, MetaCatchUpPlaces); mask != placeBit(WritePlaceOwnerPrivate)|placeBit(WritePlacePlatformPrivate) {
		t.Errorf("位图该缩小：%d", mask)
	}
	r.ev.add(cuGroup, ownerA, "群里攒的话")
	r.m.opt.WritePlaces = []string{WritePlaceOwnerPrivate, WritePlacePlatformPrivate, WritePlacePlatformGroup}
	res, err = r.m.CatchUpToLatest(ctx)
	if err != nil || res.Sessions != 1 {
		t.Fatalf("再加回来该再推一次：%+v %v", res, err)
	}
}

// 出错不更新位图，下次启动重来；游标只往前
func TestCatchUpFailuresAndMonotonic(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := cuRig(t, []string{WritePlaceOwnerPrivate})
	boom := errors.New("库忙")
	n := 0
	r.ev.said = func(conv string) (Event, bool, error) {
		n++
		if n == 1 {
			return Event{}, false, boom
		}
		return Event{Conversation: conv, ActorKind: "owner", Origin: "owner", Kind: "message"}, true, nil
	}
	if _, err := r.m.CatchUpToLatest(ctx); err == nil {
		t.Fatal("认不出是不是主人的私聊时该报错")
	}
	if _, ok, _ := r.m.Meta(ctx, MetaCatchUpPlaces); ok {
		t.Fatal("出错不该记位图")
	}
	if seq, _ := r.cursorOf(t, cuOwnerPriv); seq != 0 {
		t.Fatalf("出错时游标不该动：%d", seq)
	}
	// 下次启动成功
	if res, err := r.m.CatchUpToLatest(ctx); err != nil || res.Sessions != 1 {
		t.Fatalf("重来该成功：%+v %v", res, err)
	}
	// advanceCursor 只往前
	last := r.lastSeqOf(cuOwnerPriv)
	if moved, err := r.m.advanceCursor(ctx, cuOwnerPriv, last-3); moved || err != nil {
		t.Fatalf("往回推该不动：%v %v", moved, err)
	}
	if seq, _ := r.cursorOf(t, cuOwnerPriv); seq != last {
		t.Fatalf("游标被往回推了：%d", seq)
	}
	if moved, err := r.m.advanceCursor(ctx, "qq:group:9", 99); moved || err != nil {
		t.Fatalf("没有这行不该建：%v %v", moved, err)
	}
}

// 没开整理、没开写入：不做
func TestCatchUpNeedsSummarize(t *testing.T) {
	t.Parallel()
	r := cuRig(t, []string{WritePlaceOwnerPrivate}, func(o *Options) { o.SummarizeEnabled = false })
	if _, err := r.m.CatchUpToLatest(context.Background()); !errors.Is(err, ErrSummarizeOff) {
		t.Fatalf("整理没开该报 ErrSummarizeOff：%v", err)
	}
	ro, err := Open(context.Background(), r.file, nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer ro.Close()
	if _, err := ro.CatchUpToLatest(context.Background()); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("只读的该报 ErrNotWritable：%v", err)
	}
}

// 名单全开：主人的私聊、别人的私聊、群、本地都推；陌生渠道从来不动
func TestCatchUpOnlyAllowedPlaces(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := cuRig(t, AllWritePlaces())
	for _, c := range []string{cuOtherPriv, cuGroup, cuLocal, cuForeign} {
		r.setCursor(t, c, r.firstSeqOf(c))
	}
	res, err := r.m.CatchUpToLatest(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if res.Sessions != 4 {
		t.Fatalf("主人的私聊、别人的私聊、群、本地四个会话该推，陌生渠道不推：%+v", res)
	}
	if seq, _ := r.cursorOf(t, cuForeign); seq != r.firstSeqOf(cuForeign) {
		t.Errorf("陌生渠道的游标不该动：%d", seq)
	}
}

// 开整理 → 停整理（写入还开着）→ 再开整理：位图清空以后再开当第一次，停整理期间的聊天再推一遍，不按旧游标接着整理
func TestCatchUpAgainAfterSummarizeWasOff(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	r := cuRig(t, []string{WritePlaceOwnerPrivate})
	if res, err := r.m.CatchUpToLatest(ctx); err != nil || res.Done || res.Sessions != 1 {
		t.Fatalf("第一次开整理：%+v %v", res, err)
	}
	pushed, _ := r.cursorOf(t, cuOwnerPriv)
	if mask, ok, _ := r.m.Meta(ctx, MetaCatchUpPlaces); !ok || mask == 0 {
		t.Fatalf("前提：位图该记下了：%d %v", mask, ok)
	}
	// 停整理期间主人的私聊里又聊了 3 句；整理没开的那次启动清空位图
	for i := 0; i < 3; i++ {
		r.ev.add(cuOwnerPriv, ownerA, fmt.Sprintf("停整理期间的第%d句", i+1))
	}
	if err := r.m.ResetCatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if mask, ok, _ := r.m.Meta(ctx, MetaCatchUpPlaces); !ok || mask != 0 {
		t.Fatalf("清空以后位图该是 0：%d %v", mask, ok)
	}
	if seq, _ := r.cursorOf(t, cuOwnerPriv); seq != pushed {
		t.Fatalf("清空位图不该动游标：%d → %d", pushed, seq)
	}
	// 再开整理：当第一次，把停整理期间的 3 句跳过
	res, err := r.m.CatchUpToLatest(ctx)
	if err != nil || res.Done || res.Sessions != 1 || res.Skipped != 3 {
		t.Fatalf("再开整理该再推一遍、跳过停整理期间的 3 条：%+v %v", res, err)
	}
	if seq, _ := r.cursorOf(t, cuOwnerPriv); seq != r.lastSeqOf(cuOwnerPriv) {
		t.Fatalf("游标该推到最新 %d：%d", r.lastSeqOf(cuOwnerPriv), seq)
	}
	// 没开过（位图根本没记）：什么都不写；只读的记忆调它报 ErrNotWritable
	r2 := cuRig(t, []string{WritePlaceOwnerPrivate})
	if err := r2.m.ResetCatchUp(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := r2.m.Meta(ctx, MetaCatchUpPlaces); ok {
		t.Error("从没推过就别凭空写一个键")
	}
	ro, err := Open(ctx, migratedDB(t, nil), nil, nil, Options{Character: "suzune"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ro.Close() })
	if err := ro.ResetCatchUp(ctx); !errors.Is(err, ErrNotWritable) {
		t.Fatalf("只读的该报 ErrNotWritable：%v", err)
	}
}
