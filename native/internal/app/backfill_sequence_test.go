package app

import (
	"context"
	"errors"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// 序号证据必须跨过目标两端；消息 ID 特意与序号无关。
func TestBackfillGroupSequenceCoverage(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		seqs   []int64
		ages   []time.Duration
		filled bool
	}{
		{"连续乱序跨页", []int64{43, 42, 41, 40}, []time.Duration{5, 12, 18, 25}, true},
		{"空号", []int64{43, 42, 40}, []time.Duration{5, 12, 25}, false},
		{"缺序号", []int64{43, 0, 41, 40}, []time.Duration{5, 12, 18, 25}, false},
		{"同序号不同消息", []int64{43, 42, 42, 40}, []time.Duration{5, 12, 18, 25}, false},
		{"缺右端缓存", []int64{43, 42, 41, 40}, []time.Duration{12, 15, 18, 25}, false},
		{"缺左端", []int64{43, 42, 41, 40}, []time.Duration{5, 12, 15, 18}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newBackfillEnv(t)
			e.b.owner, e.b.groups = "", []string{testGroup}
			e.p.stale, e.p.continuous = true, false
			c := backfillGroup()
			g := manualGap(t, e, c, 20*time.Minute, 10*time.Minute, "open")
			e.clk.Advance(backfillRetry)
			var rows []types.Inbound
			for i, seq := range tc.seqs {
				in := e.msg(c, []string{"-900", "700", "-200", "500"}[i], tc.ages[i]*time.Minute+backfillRetry)
				in.MessageSeq = seq
				rows = append(rows, in)
			}
			e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
				if q.anchor == "" {
					return rows[:2], nil
				}
				return append([]types.Inbound{rows[1]}, rows[2:]...), nil
			}
			e.run()
			current, err := e.st.ChannelGap(context.Background(), g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (current.State == "filled") != tc.filled {
				t.Fatalf("序号覆盖结算错误: %+v want filled=%v", current, tc.filled)
			}
			if tc.filled {
				tailVisible := false
				for _, pending := range e.gaps() {
					if pending.State == "blocked" && pending.FromMS <= e.clk.Now().UnixMilli() && pending.ToMS >= e.clk.Now().UnixMilli() {
						tailVisible = true
					}
				}
				if !tailVisible {
					t.Fatal("旧洞序号连通后，未知新尾段消失")
				}
			}
			if e.n() != len(rows) {
				t.Fatalf("证据失败不能丢有效消息: %d want %d", e.n(), len(rows))
			}
		})
	}
}

// 首页已包住旧洞，后页的坏证据不能沿用首页的覆盖结论。
func TestReviewLateInvalidSequence(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"stalled_zero", "stalled_duplicate", "partial_error"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			e := newBackfillEnv(t)
			e.b.owner, e.b.groups = "", []string{testGroup}
			e.p.stale, e.p.continuous = true, false
			c := backfillGroup()
			g := manualGap(t, e, c, 20*time.Minute, 10*time.Minute, "open")
			rows := make([]types.Inbound, 4)
			ages := []time.Duration{5, 12, 18, 25}
			for i := range rows {
				rows[i] = e.msg(c, []string{"801", "802", "803", "804"}[i], ages[i]*time.Minute)
				rows[i].MessageSeq = 43 - int64(i)
			}
			invalid := e.msg(c, "805", 15*time.Minute)
			if mode == "stalled_duplicate" {
				invalid.MessageSeq = 42
			}
			e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
				if q.anchor == "" {
					return rows, nil
				}
				if mode == "partial_error" {
					return []types.Inbound{rows[3], invalid}, errors.New("synthetic malformed page")
				}
				return []types.Inbound{rows[3], invalid}, nil
			}
			e.run()
			got, err := e.st.ChannelGap(context.Background(), g.ID)
			if err != nil {
				t.Fatal(err)
			}
			if got.State == "filled" {
				t.Fatalf("invalid_later_page=1 filled=1 calls=%d stored=%d", e.p.count(), e.n())
			}
			if got.State != "blocked" || e.n() != 5 {
				t.Fatalf("坏证据应保留缺口和全部有效消息: state=%s stored=%d", got.State, e.n())
			}
			t.Logf("state=%s calls=%d stored=%d", got.State, e.p.count(), e.n())
		})
	}
}
