package app

import (
	"context"
	"strconv"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
)

// 缓存端点不能替代中间的 200 条；恢复协议端后逐轮核对消息集合和可见缺口。
func TestBackfillUnprovenCoverage(t *testing.T) {
	t.Parallel()
	t.Run("202条缓存恢复再补录", func(t *testing.T) {
		t.Parallel()
		e := newBackfillEnv(t)
		e.b.owner, e.b.groups, e.p.stale = "", []string{testGroup}, true
		e.p.continuous = false
		c, now := backfillGroup(), e.clk.Now()
		rows := []types.Inbound{e.msg(c, "1", 300*time.Second)}
		for id := 2; id <= 202; id++ {
			rows = append(rows, e.msg(c, strconv.Itoa(id), time.Duration(203-id)*time.Second))
		}
		for _, in := range []types.Inbound{rows[0], rows[201]} {
			tk, err := e.b.intake.Submit(context.Background(), in, channel.Options{Backfill: true})
			if err != nil {
				t.Fatal(err)
			}
			if _, err = tk.Wait(context.Background()); err != nil {
				t.Fatal(err)
			}
		}
		e.watermark(now.Add(-201 * time.Second))
		manualGap(t, e, c, 261*time.Second, 0, "open")
		e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) {
			return []types.Inbound{rows[0], rows[201]}, nil
		}
		ctx := manualConnection(t, e)
		for round := range 3 {
			if round > 0 {
				e.clk.Advance(backfillRetry)
				e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
					end := len(rows)
					if q.anchor != "" {
						for i, in := range rows {
							if in.PlatformID == q.anchor {
								end = i + 1
								break
							}
						}
					}
					return rows[max(0, end-q.count):end], nil
				}
			}
			if round == 0 {
				e.run()
			} else {
				j, err := e.b.requestGap(ctx, gapRef(e.gaps()[0]))
				if err != nil {
					t.Fatal(err)
				}
				drainManual(t, e, ctx, j)
			}
			page, err := e.st.ReadAccountGaps(context.Background(), testAccount, "", 100)
			if err != nil {
				t.Fatal(err)
			}
			var have int
			for _, in := range rows {
				var n int
				if err := e.st.Events.QueryRow(`SELECT count(*) FROM channel_msgs WHERE conversation=? AND platform_id=?`, c.String(), in.PlatformID).Scan(&n); err != nil {
					t.Fatal(err)
				}
				have += n
			}
			if round > 0 && have != 202 {
				t.Fatalf("手动回溯没补回全部历史: %d/202", have)
			}
			if have < 202 && len(page.Items) == 0 {
				t.Fatalf("静默漏信 round=%d have=%d/202", round, have)
			}
			if len(page.Items) == 0 {
				t.Fatal("没有连续性证明仍清掉缺口")
			}
			for _, g := range page.Items {
				if g.State != "blocked" && g.State != "abandoned" {
					t.Fatalf("应明确受阻 %+v", g)
				}
			}
			t.Logf("round=%d stored=%d/202 missing=%d visible_gaps=%d calls=%d", round+1, have, 202-have, len(page.Items), e.p.count())
		}
	})
	t.Run("未知首页中段", func(t *testing.T) {
		t.Parallel()
		e := newBackfillEnv(t)
		e.b.owner, e.b.groups, e.p.stale = "", []string{testGroup}, true
		e.p.continuous = false
		c := backfillGroup()
		manualGap(t, e, c, 40*time.Minute, 10*time.Minute, "open")
		e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) {
			return []types.Inbound{e.msg(c, "71", 5*time.Minute), e.msg(c, "72", 80*time.Minute)}, nil
		}
		e.run()
		for _, g := range e.gaps() {
			if g.State == "filled" {
				t.Fatalf("中段被时间包围误清 %+v", g)
			}
		}
	})
	t.Run("手动右锚点零新增", func(t *testing.T) {
		t.Parallel()
		e := newBackfillEnv(t)
		e.b.owner, e.b.groups, e.p.stale = "", []string{testGroup}, true
		e.p.continuous = false
		c := backfillGroup()
		g := manualGap(t, e, c, 20*time.Minute, 10*time.Minute, "open")
		right, left := e.msg(c, "82", 5*time.Minute), e.msg(c, "81", 25*time.Minute)
		e.save(right)
		e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) { return []types.Inbound{left, right}, nil }
		ctx := manualConnection(t, e)
		j, err := e.b.requestGap(ctx, gapRef(g))
		if err != nil {
			t.Fatal(err)
		}
		done := drainManual(t, e, ctx, j)
		current, err := e.st.ChannelGap(ctx, g.ID)
		if err != nil {
			t.Fatal(err)
		}
		if done.Inserted != 0 || done.CompleteCoverage || current.State != "blocked" {
			t.Fatalf("无证据的零新增完成 %+v %+v", done, current)
		}
	})
	t.Run("新私聊空页", func(t *testing.T) {
		t.Parallel()
		e := newBackfillEnv(t)
		e.p.continuous = false
		e.run()
		g := e.gaps()
		if e.n() != 0 || len(g) != 1 || g[0].State != "blocked" || g[0].Reason != "no-anchor" {
			t.Fatalf("空页冒充完整 %+v", g)
		}
	})
	t.Run("最新私聊仍无页内证明", func(t *testing.T) {
		t.Parallel()
		e := newBackfillEnv(t)
		e.p.continuous = false
		c := backfillPrivate()
		manualGap(t, e, c, 40*time.Minute, 10*time.Minute, "open")
		e.p.read = func(context.Context, historyCall) ([]types.Inbound, error) {
			return []types.Inbound{e.msg(c, "91", time.Minute), e.msg(c, "92", 80*time.Minute)}, nil
		}
		e.run()
		for _, g := range e.gaps() {
			if g.State == "filled" {
				t.Fatalf("Latest 不能替代页内证明 %+v", g)
			}
		}
	})
	t.Run("旧filled不合成新的覆盖证明", func(t *testing.T) {
		t.Parallel()
		e := newBackfillEnv(t)
		g := manualGap(t, e, backfillPrivate(), 20*time.Minute, 10*time.Minute, "filled")
		j, err := e.b.requestGap(context.Background(), gapRef(g))
		if err != nil || j.Inserted != 0 || j.CompleteCoverage || e.p.count() != 0 {
			t.Fatal("没有新任务证据却报告完整", j, err)
		}
	})
}
