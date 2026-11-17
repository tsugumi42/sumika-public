package store

import (
	"context"
	"testing"
)

// 连续边界按账号、会话隔离；任何未完成状态都不能被本轮较新的证明跨过。
func TestQQBackfillSettlement(t *testing.T) {
	t.Parallel()
	for _, state := range []string{"open", "blocked", "abandoned", "filled"} {
		t.Run(state, func(t *testing.T) {
			t.Parallel()
			set, _ := goSet(t)
			s, err := set.For("qq")
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			g := ChannelGap{Channel: "qq", Conversation: "qq:group:800", FromMS: 100, ToMS: 200, Reason: "outage", State: state, LastTryMS: 10}
			r := QQBackfillResult{Conversation: g.Conversation, FromMS: 50, Covered: true, CoveredFromMS: 50, CoveredToMS: 300, Gaps: []ChannelGap{g}}
			if err := s.SettleQQBackfill(ctx, "account-a", 300, []QQBackfillResult{r}); err != nil {
				t.Fatal(err)
			}
			c, err := s.QQContinuousRange(ctx, "account-a", g.Conversation)
			if err != nil {
				t.Fatal(err)
			}
			want := int64(99)
			if state == "filled" {
				want = 300
			}
			if c.FromMS != 50 || c.ThroughMS != want {
				t.Fatalf("缺口门槛错误 state=%s boundary=%+v", state, c)
			}
			// 同账号另一会话、另一账号同会话均独立；检查位置不冒充连续边界。
			other := QQBackfillResult{Conversation: "qq:group:801", FromMS: 250, Covered: true, CoveredFromMS: 250, CoveredToMS: 400}
			if err := s.SettleQQBackfill(ctx, "account-a", 400, []QQBackfillResult{other}); err != nil {
				t.Fatal(err)
			}
			if err := s.SettleQQBackfill(ctx, "account-b", 500, []QQBackfillResult{{Conversation: other.Conversation, FromMS: 450}}); err != nil {
				t.Fatal(err)
			}
			c, err = s.QQContinuousRange(ctx, "account-a", g.Conversation)
			if err != nil || c.ThroughMS != want {
				t.Fatal("其他会话推进了旧洞", c, err)
			}
			a, err := s.QQContinuousRange(ctx, "account-a", other.Conversation)
			if err != nil || a.ThroughMS != 400 {
				t.Fatal(a, err)
			}
			b, err := s.QQContinuousRange(ctx, "account-b", other.Conversation)
			if err != nil || b.ThroughMS != 449 {
				t.Fatal("账号边界混用", b, err)
			}
		})
	}
	t.Run("连续边界只接已证明区间", func(t *testing.T) {
		t.Parallel()
		set, _ := goSet(t)
		s, err := set.For("qq")
		if err != nil {
			t.Fatal(err)
		}
		ctx := context.Background()
		for _, step := range []struct {
			from, to, wantFrom, wantThrough int64
			gap                             bool
		}{
			{10, 100, 10, 100, false},
			{200, 300, 10, 100, false},
			{50, 400, 10, 400, false},
			{5, 500, 5, 4, true},
		} {
			r := QQBackfillResult{Conversation: "qq:group:802", FromMS: step.from, Covered: true, CoveredFromMS: step.from, CoveredToMS: step.to}
			if step.gap {
				r.Covered = false
				r.Gaps = []ChannelGap{{Channel: "qq", Conversation: r.Conversation, FromMS: 5, ToMS: 9, State: "blocked", Reason: "outage"}}
			}
			if err := s.SettleQQBackfill(ctx, "account-a", step.to, []QQBackfillResult{r}); err != nil {
				t.Fatal(err)
			}
			c, err := s.QQContinuousRange(ctx, "account-a", r.Conversation)
			if err != nil || c.FromMS != step.wantFrom || c.ThroughMS != step.wantThrough {
				t.Fatalf("不相连的证明或更早缺口被跳过：step=%+v boundary=%+v err=%v", step, c, err)
			}
		}
	})
	for _, table := range []string{"qq_backfill_continuity", "qq_sync"} {
		t.Run("结算失败回滚/"+table, func(t *testing.T) {
			t.Parallel()
			set, _ := goSet(t)
			s, err := set.For("qq")
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			if err := s.SetQQCheckedThrough(ctx, "account-a", 20); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Events.Exec(`CREATE TRIGGER fail_settlement BEFORE INSERT ON ` + table + ` BEGIN SELECT RAISE(FAIL,'编造结算落盘失败'); END`); err != nil {
				t.Fatal(err)
			}
			r := QQBackfillResult{Conversation: "qq:group:800", FromMS: 10, Gaps: []ChannelGap{{Channel: "qq", Conversation: "qq:group:800", FromMS: 10, ToMS: 100, Reason: "unverified-history", State: "blocked"}}}
			if err := s.SettleQQBackfill(ctx, "account-a", 100, []QQBackfillResult{r}); err == nil {
				t.Fatal("落盘失败被当成功")
			}
			var gaps, bounds int
			if err := s.Events.QueryRow(`SELECT count(*) FROM channel_gaps`).Scan(&gaps); err != nil {
				t.Fatal(err)
			}
			if err := s.Events.QueryRow(`SELECT count(*) FROM qq_backfill_continuity`).Scan(&bounds); err != nil {
				t.Fatal(err)
			}
			checked, err := s.QQCheckedThrough(ctx, "account-a", 0)
			if err != nil || gaps != 0 || bounds != 0 || checked != 20 {
				t.Fatalf("事务部分提交 gaps=%d boundaries=%d checked=%d err=%v", gaps, bounds, checked, err)
			}
		})
	}
}
