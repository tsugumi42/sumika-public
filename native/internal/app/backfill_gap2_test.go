package app

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

func alignmentInteriorGapCoverage(t *testing.T) {
	for _, name := range []string{"尾段未证实", "更早页失败", "断页不算", "终点未覆盖", "未入库区间"} {
		t.Run(name, func(t *testing.T) {
			e := newBackfillEnv(t)
			e.b.owner, e.b.groups = "", []string{testGroup}
			e.p.stale = name == "尾段未证实" || name == "终点未覆盖"
			e.p.continuous = false
			c := backfillGroup()
			lo, hi := e.clk.Now().Add(-40*time.Minute).UnixMilli(), e.clk.Now().Add(-10*time.Minute).UnixMilli()
			if name == "终点未覆盖" {
				hi = e.clk.Now().UnixMilli()
			}
			lastTry := int64(0)
			if name == "未入库区间" {
				lo, hi = e.clk.Now().Add(-75*time.Minute).UnixMilli(), e.clk.Now().Add(-70*time.Minute).UnixMilli()
				lastTry = e.clk.Now().UnixMilli()
			}
			if err := e.st.RecordChannelGap(context.Background(), store.ChannelGap{Channel: "qq", Conversation: c.String(), FromMS: lo, ToMS: hi, Reason: "outage", LastTryMS: lastTry}); err != nil {
				t.Fatal(err)
			}
			e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
				if q.anchor == "" {
					if name == "未入库区间" {
						return []types.Inbound{e.msg(c, "9200", 5*time.Minute), e.msg(c, "9201", 75*time.Minute), e.msg(c, "9202", 80*time.Minute)}, nil
					}
					return []types.Inbound{e.msg(c, "9200", 5*time.Minute), e.msg(c, "9201", 20*time.Minute)}, nil
				}
				if q.anchor == "9201" {
					rows := []types.Inbound{e.msg(c, "9202", 50*time.Minute)}
					if name != "断页不算" {
						rows = append(rows, e.msg(c, "9201", 20*time.Minute))
					}
					return rows, nil
				}
				return nil, errors.New("编的失败")
			}
			e.run()
			var original store.ChannelGap
			for _, g := range e.gaps() {
				if g.ID == 1 {
					original = g
				}
			}
			if original.State == "filled" {
				t.Fatalf("扫描边界不能证明中段完整: %+v", e.gaps())
			}
		})
	}
}

func TestBackfillAbandonedDoesNotRewind(t *testing.T) {
	t.Parallel()
	e := newBackfillEnv(t)
	e.b.owner, e.b.groups = "", []string{testGroup}
	c := backfillGroup()
	now := e.clk.Now()
	e.watermark(now.Add(-25 * time.Hour))
	g := store.ChannelGap{Channel: "qq", Conversation: c.String(), FromMS: now.Add(-26 * time.Hour).UnixMilli(), ToMS: now.Add(-time.Minute).UnixMilli(), Reason: "page-cap"}
	if err := e.st.RecordChannelGap(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	g = e.gaps()[0]
	g.State = "abandoned"
	g.Tries = 3
	if err := e.st.UpdateChannelGap(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	old := e.msg(c, "9400", 2*time.Minute)
	e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
		return []types.Inbound{e.msg(c, "9401", 0), old}, nil
	}
	e.run()
	gaps := e.gaps()
	if e.p.count() != 1 || len(gaps) != 1 || gaps[0].State != "abandoned" || e.through() != now.UnixMilli() {
		t.Fatalf("放弃区间仍拖住回溯: calls=%d gaps=%+v through=%d", e.p.count(), gaps, e.through())
	}
	e.clk.Advance(backfillRetry)
	e.run()
	if e.p.count() != 2 || len(e.gaps()) != 1 {
		t.Fatalf("后续轮次又翻旧区间: calls=%d gaps=%+v", e.p.count(), e.gaps())
	}
}

// 旧 open 只在既有五页预算内尝试；不能把它和当前窗口之间整天新增成缺口。
func TestBackfillOldOpenPageBound(t *testing.T) {
	t.Parallel()
	e := newBackfillEnv(t)
	e.b.owner, e.b.groups = "", []string{testGroup}
	e.p.continuous = false
	c := backfillGroup()
	now := e.clk.Now()
	e.watermark(now.Add(-10 * time.Minute))
	if err := e.st.RecordChannelGap(context.Background(), store.ChannelGap{Channel: "qq", Conversation: c.String(), FromMS: now.Add(-26 * time.Hour).UnixMilli(), ToMS: now.Add(-25 * time.Hour).UnixMilli(), Reason: "page-cap"}); err != nil {
		t.Fatal(err)
	}
	e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
		n := e.p.count()
		rows := []types.Inbound{e.msg(c, "950"+strconv.Itoa(n), time.Duration(n)*time.Minute)}
		if q.anchor != "" {
			rows = append(rows, e.msg(c, q.anchor, time.Duration(n-1)*time.Minute))
		}
		return rows, nil
	}
	e.run()
	gaps := e.gaps()
	if e.p.count() != 5 || len(gaps) != 2 || gaps[0].ToMS != now.Add(-25*time.Hour).UnixMilli() || gaps[0].Tries != 1 || gaps[1].FromMS != now.Add(-11*time.Minute).UnixMilli() || gaps[1].ToMS != now.UnixMilli() {
		t.Fatalf("旧缺口扩大成跨日窗口或突破五页：calls=%d gaps=%+v", e.p.count(), gaps)
	}
}

func TestBackfillAbandonedTailNotReopened(t *testing.T) {
	t.Parallel()
	for _, next := range []bool{false, true} {
		t.Run(strconv.FormatBool(next), func(t *testing.T) {
			t.Parallel()
			e := newBackfillEnv(t)
			e.b.owner, e.b.groups, e.p.stale = "", []string{testGroup}, true
			c := backfillGroup()
			now := e.clk.Now()
			if err := e.st.RecordChannelGap(context.Background(), store.ChannelGap{Channel: "qq", Conversation: c.String(), FromMS: now.Add(-26 * time.Hour).UnixMilli(), ToMS: now.UnixMilli(), Reason: "page-cap"}); err != nil {
				t.Fatal(err)
			}
			g := e.gaps()[0]
			g.State, g.Tries = "abandoned", 3
			if err := e.st.UpdateChannelGap(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			head := e.msg(c, "9600", 2*time.Hour)
			e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) { return []types.Inbound{head}, nil }
			e.run()
			if next {
				e.clk.Advance(backfillRetry)
				e.run()
			}
			gaps := e.gaps()
			want := 1
			if next {
				want = 2
			}
			if len(gaps) != want || gaps[0].State != "abandoned" || gaps[0].Tries != 3 || (next && (gaps[1].FromMS != now.UnixMilli()+1 || gaps[1].ToMS != e.clk.Now().UnixMilli())) {
				t.Fatalf("未知首页重新登记了已放弃旧区间：%+v", gaps)
			}
		})
	}
}
