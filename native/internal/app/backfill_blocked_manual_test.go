package app

import (
	"context"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
)

// 已受阻与分页证据失败只影响证明，不能挡住手动请求继续取得并保存有效历史。
func TestManualBlockedGapStoresHistory(t *testing.T) {
	t.Parallel()
	for _, c := range []types.Conversation{backfillPrivate(), backfillGroup()} {
		t.Run(c.Scene, func(t *testing.T) {
			t.Parallel()
			e := newBackfillEnv(t)
			e.b.groups = []string{testGroup}
			e.p.continuous = false
			e.p.stale = true
			g := manualGap(t, e, c, 20*time.Minute, 10*time.Minute, "blocked")
			right, duplicate := e.msg(c, "9000", 5*time.Minute), e.msg(c, "9002", 15*time.Minute)
			e.save(right)
			e.save(duplicate)
			newer, older, outside := e.msg(c, "9001", 12*time.Minute), e.msg(c, "9003", 18*time.Minute), e.msg(c, "9004", 25*time.Minute)
			e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
				if c.Scene == types.ScenePrivate {
					return []types.Inbound{older, right, duplicate, newer, outside}, nil
				}
				switch q.anchor {
				case right.PlatformID:
					return []types.Inbound{newer}, nil
				case newer.PlatformID:
					return []types.Inbound{older, duplicate}, nil
				default:
					return []types.Inbound{outside}, nil
				}
			}
			before := e.through()
			ctx := manualConnection(t, e)
			j, err := e.b.requestGap(ctx, gapRef(g))
			if err != nil {
				t.Fatal(err)
			}
			done := drainManual(t, e, ctx, j)
			if done.State != "completed" || done.Inserted != 2 || done.Duplicates != 1 || done.CompleteCoverage {
				t.Fatalf("手动受阻补录未保存历史: %+v", done)
			}
			var ids string
			err = e.st.Events.QueryRow(`SELECT group_concat(platform_id,',') FROM (SELECT m.platform_id FROM events e JOIN channel_msgs m ON m.event_seq=e.seq WHERE e.conversation=? ORDER BY e.created_at,e.seq)`, c.String()).Scan(&ids)
			if err != nil || ids != "9003,9002,9001,9000" {
				t.Fatal("历史去重或平台时间顺序错误", ids, err)
			}
			var tagged, outbound int
			if err = e.st.Events.QueryRow(`SELECT count(*) FROM events e JOIN channel_msgs m ON m.event_seq=e.seq WHERE m.platform_id IN ('9001','9003') AND e.segments_json LIKE '%"backfill"%'`).Scan(&tagged); err != nil {
				t.Fatal(err)
			}
			if err = e.st.Events.QueryRow(`SELECT count(*) FROM outbound_parts`).Scan(&outbound); err != nil {
				t.Fatal(err)
			}
			current, err := e.st.ChannelGap(ctx, g.ID)
			if err != nil || current.State != "blocked" || current.LastTryMS != g.LastTryMS || current.LastManualMS == 0 || e.through() != before || tagged != 2 || outbound != 0 {
				t.Fatal("手动补录改变缺口、位置或触发出站", current, tagged, outbound, err)
			}
		})
	}
}
