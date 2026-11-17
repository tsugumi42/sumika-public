package app

import (
	"context"
	"strconv"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// 同一批编造历史经过真实 Intake 和临时库；不能把“请求成功”当成区间已补齐。
func TestAlignmentExistingCoverageContract(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, reason string
		events       int
		tail         bool
	}{
		{"锚点不动", "stalled", 2, false},
		{"只回最近页", "stalled", 2, false},
		{"只添较新消息", "stalled", 3, false},
		{"中间断页", "broken-page", 3, false},
		{"重复号改时间", "broken-page", 2, false},
		{"缓存首页已越过起点", "unverified-history", 0, true},
		{"缓存首页翻满五页", "page-cap", 6, true},
		{"连续分页", "", 3, false},
		{"同秒跨页", "", 3, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newBackfillEnv(t)
			e.b.owner, e.b.groups = "", []string{testGroup}
			e.p.stale = tc.tail
			c := backfillGroup()
			head, anchor := e.msg(c, "8100", time.Minute), e.msg(c, "8101", 2*time.Minute)
			e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
				if tc.name == "缓存首页已越过起点" {
					return []types.Inbound{e.msg(c, "8099", 2*time.Hour)}, nil
				}
				if tc.name == "缓存首页翻满五页" {
					n := e.p.count()
					rows := []types.Inbound{e.msg(c, strconv.Itoa(8300+n), time.Duration(n+1)*time.Minute)}
					if q.anchor != "" {
						rows = append(rows, e.msg(c, q.anchor, time.Duration(n)*time.Minute))
					} else {
						rows = append(rows, head)
					}
					return rows, nil
				}
				if q.anchor == "" {
					return []types.Inbound{head, anchor}, nil
				}
				switch tc.name {
				case "锚点不动":
					return []types.Inbound{anchor}, nil
				case "只回最近页":
					return []types.Inbound{head, anchor}, nil
				case "只添较新消息":
					return []types.Inbound{anchor, e.msg(c, "8102", 30*time.Second)}, nil
				case "中间断页":
					return []types.Inbound{e.msg(c, "8102", 30*time.Minute), e.msg(c, "8103", 2*time.Hour)}, nil
				case "重复号改时间":
					changed := anchor
					changed.Time = e.clk.Now().Add(-2 * time.Hour)
					return []types.Inbound{changed}, nil
				case "同秒跨页":
					if q.anchor == anchor.PlatformID {
						return []types.Inbound{anchor, e.msg(c, "8102", 2*time.Minute)}, nil
					}
					return []types.Inbound{e.msg(c, "8102", 2*time.Minute), e.msg(c, "8103", 2*time.Hour)}, nil
				default:
					return []types.Inbound{anchor, e.msg(c, "8102", 30*time.Minute), e.msg(c, "8103", 2*time.Hour)}, nil
				}
			}
			e.run()
			if e.n() != tc.events {
				t.Fatalf("取得的可用消息未照存：events=%d want=%d", e.n(), tc.events)
			}
			gaps := e.gaps()
			if tc.reason == "" {
				if len(gaps) != 0 || e.through() != e.clk.Now().UnixMilli() {
					t.Fatalf("连续覆盖未完成：gaps=%+v through=%d", gaps, e.through())
				}
				return
			}
			wantFrom, wantTo := e.clk.Now().Add(-61*time.Minute).UnixMilli(), anchor.Time.UnixMilli()
			if tc.tail || tc.reason == "broken-page" {
				wantTo = e.clk.Now().UnixMilli()
			}
			if len(gaps) != 1 || gaps[0].Reason != tc.reason || gaps[0].FromMS != wantFrom || gaps[0].ToMS != wantTo {
				t.Fatalf("扫描或覆盖边界错误：gaps=%+v wantFrom=%d wantTo=%d", gaps, wantFrom, wantTo)
			}
			before := e.through()
			e.save(e.msg(c, "8199", 0))
			if e.through() != before {
				t.Fatal("失败后实时消息越过缺口")
			}
		})
	}
	t.Run("旧outage与未知首页中段证明", alignmentInteriorGapCoverage)
}

// 检查位置可以继续记录在线处理；连续边界不能跨过任何未完成缺口。
func TestBackfillGapPinsContinuousWatermark(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"open", "blocked", "abandoned"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			e := newBackfillEnv(t)
			e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
				return []types.Inbound{e.msg(q.conv, "8200", time.Minute)}, nil
			}
			e.b.connected(context.Background())
			e.clk.WaitTimer(t, 30*time.Second)
			e.clk.Advance(30 * time.Second)
			e.clk.WaitTimer(t, time.Minute)
			before := e.through()
			g := e.gaps()[0]
			continuous, err := e.st.QQContinuousRange(context.Background(), testAccount, g.Conversation)
			if err != nil {
				t.Fatal("缺少独立连续边界", err)
			}
			if continuous.ThroughMS >= g.FromMS {
				t.Fatalf("首轮受阻却推进连续边界：%+v gap=%+v", continuous, g)
			}
			g.State = state
			if err := e.st.UpdateChannelGap(context.Background(), g); err != nil {
				t.Fatal(err)
			}
			for range 3 {
				e.clk.Advance(time.Minute)
				e.clk.WaitTimer(t, time.Minute)
			}
			e.b.stop()
			after, err := e.st.QQContinuousRange(context.Background(), testAccount, g.Conversation)
			if err != nil || after != continuous {
				t.Fatal("在线检查或实时处理越过未完成缺口", continuous, after, err)
			}
			if e.through() <= before || len(e.gaps()) != 1 || e.gaps()[0].State != state || e.gaps()[0].FromMS != g.FromMS || e.gaps()[0].ToMS != g.ToMS {
				t.Fatalf("%s 缺口丢失或阻塞在线处理水位：%+v through=%d", state, e.gaps(), e.through())
			}
		})
	}
}
