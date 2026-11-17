package app

// 共用场景（testdata/shared/backfill，格式见该目录 README.md、规格见 docs/规格-补录覆盖.md）：
// 同一份 JSON 给 Go 和以后的 Bun 后台验收。这里是 Go 的跑法：用真实 qqBackfill.round、真实 Intake 和临时库，
// 只把协议端换成按脚本回页的假历史源。SUMIKA_SHARED_UPDATE=1 把实际结果写回各文件的 expect（重跑结果不变）。

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/sharedtest"
	"sumika-native/internal/store"
)

type bfMsg struct {
	ID    string `json:"id"`
	AgoMS int64  `json:"ago_ms"`
	Seq   int64  `json:"seq"`
	From  string `json:"from"` // other（默认）| self
}
type bfGapIn struct {
	FromAgoMS    int64  `json:"from_ago_ms"`
	ToAgoMS      int64  `json:"to_ago_ms"`
	State        string `json:"state"`
	Reason       string `json:"reason"`
	Tries        int    `json:"tries"`
	LastTryAgoMS int64  `json:"last_try_ago_ms"`
}
type bfPeer struct {
	Latest      bool `json:"latest"`
	Continuous  bool `json:"continuous"`
	Unsupported bool `json:"unsupported"`
}
type bfSetup struct {
	WatermarkAgoMS int64     `json:"watermark_ago_ms"`
	Peer           bfPeer    `json:"peer"`
	Stored         []bfMsg   `json:"stored"`
	Gaps           []bfGapIn `json:"gaps"`
}
type bfPage struct {
	Messages   []bfMsg `json:"messages"`
	Error      string  `json:"error"`
	LiveDuring []bfMsg `json:"live_during"`
	Repeat     bool    `json:"repeat"` // 真：之后所有查询都回这一页
	Cancel     bool    `json:"cancel"` // 真：回这一页之前先取消这一轮（模拟断线、停止）
}
type bfCall struct {
	Anchor string `json:"anchor"`
	Count  int    `json:"count"`
}
type bfStored struct {
	ID    string `json:"id"`
	AgoMS int64  `json:"ago_ms"`
}
type bfGapOut struct {
	FromAgoMS    int64  `json:"from_ago_ms"`
	ToAgoMS      int64  `json:"to_ago_ms"`
	State        string `json:"state"`
	Reason       string `json:"reason"`
	Tries        int    `json:"tries"`
	LastTryAgoMS int64  `json:"last_try_ago_ms"`
}
type bfContinuity struct {
	FromAgoMS    int64 `json:"from_ago_ms"`
	ThroughAgoMS int64 `json:"through_ago_ms"`
}
type bfExpect struct {
	Error            string        `json:"error"`
	Calls            []bfCall      `json:"calls"`
	Stored           []bfStored    `json:"stored"`
	Gaps             []bfGapOut    `json:"gaps"`
	WatermarkAgoMS   int64         `json:"watermark_ago_ms"`
	Continuity       *bfContinuity `json:"continuity"` // 这个会话已证明连续的区间；没有记录是 null
	CompleteCoverage bool          `json:"complete_coverage"`
}
type bfStep struct {
	AdvanceMS  int64    `json:"advance_ms"`
	LiveBefore []bfMsg  `json:"live_before"`
	Peer       *bfPeer  `json:"peer,omitempty"`
	Pages      []bfPage `json:"pages"`
	Expect     bfExpect `json:"expect"`
}
type bfScenario struct {
	ID          string   `json:"id"`
	About       string   `json:"about"`
	SourceTests []string `json:"source_tests"`
	Scene       string   `json:"scene"` // private | group
	Setup       bfSetup  `json:"setup"`
	Steps       []bfStep `json:"steps"`
}

func TestSharedBackfill(t *testing.T) {
	t.Parallel()
	files := sharedtest.Files(t, "backfill")
	seen := map[string]bool{}
	for _, path := range files {
		var sc bfScenario
		sharedtest.Load(t, path, &sc)
		if seen[sc.ID] {
			t.Fatalf("场景号重复：%s", sc.ID)
		}
		seen[sc.ID] = true
		t.Run(sc.ID, func(t *testing.T) {
			got := runBackfillScenario(t, sc)
			if sharedtest.Update() {
				for i := range sc.Steps {
					sc.Steps[i].Expect = got[i]
				}
				sharedtest.Save(t, path, sc)
				return
			}
			for i, step := range sc.Steps {
				if ok, want, have := sharedtest.Same(step.Expect, got[i]); !ok {
					t.Errorf("第 %d 步结果不同\n期望：%s\n实际：%s", i+1, want, have)
				}
			}
		})
	}
}

func runBackfillScenario(t *testing.T, sc bfScenario) []bfExpect {
	e := newBackfillEnv(t)
	var conv types.Conversation
	switch sc.Scene {
	case "private":
		conv = backfillPrivate()
	case "group":
		conv = backfillGroup()
		e.b.owner, e.b.groups = "", []string{testGroup}
	default:
		t.Fatalf("scene 只认 private、group：%q", sc.Scene)
	}
	mk := func(m bfMsg) types.Inbound {
		in := e.msg(conv, m.ID, time.Duration(m.AgoMS)*time.Millisecond)
		in.MessageSeq = m.Seq
		switch m.From {
		case "", "other":
		case "self":
			in.Kind, in.From.ID = types.InboundSelf, testAccount
		default:
			t.Fatalf("from 只认 other、self：%q", m.From)
		}
		return in
	}
	setPeer := func(p bfPeer) {
		e.p.mu.Lock()
		e.p.stale, e.p.continuous, e.p.unsupported = !p.Latest, p.Continuous, p.Unsupported
		e.p.mu.Unlock()
	}
	setPeer(sc.Setup.Peer)
	e.watermark(e.clk.Now().Add(-time.Duration(sc.Setup.WatermarkAgoMS) * time.Millisecond))
	for _, m := range sc.Setup.Stored {
		e.save(mk(m))
	}
	for _, g := range sc.Setup.Gaps {
		now := e.clk.Now().UnixMilli()
		if err := e.st.RecordChannelGap(context.Background(), store.ChannelGap{Channel: "qq", Conversation: conv.String(), FromMS: now - g.FromAgoMS, ToMS: now - g.ToAgoMS, Reason: "outage", LastTryMS: now}); err != nil {
			t.Fatal(err)
		}
		var row store.ChannelGap
		for _, x := range e.gaps() {
			if x.Conversation == conv.String() && x.FromMS == now-g.FromAgoMS && x.ToMS == now-g.ToAgoMS {
				row = x
			}
		}
		if row.ID == 0 {
			t.Fatalf("预置缺口被合并了，场景要拆开写：%+v", g)
		}
		row.State, row.Tries, row.LastTryMS = g.State, g.Tries, now-g.LastTryAgoMS
		if g.Reason != "" {
			row.Reason = g.Reason
		}
		if err := e.st.UpdateChannelGap(context.Background(), row); err != nil {
			t.Fatal(err)
		}
	}
	var out []bfExpect
	for si, step := range sc.Steps {
		if step.AdvanceMS > 0 {
			e.clk.Advance(time.Duration(step.AdvanceMS) * time.Millisecond)
		}
		if step.Peer != nil {
			setPeer(*step.Peer)
		}
		for _, m := range step.LiveBefore {
			e.save(mk(m))
		}
		e.p.mu.Lock()
		e.p.calls = nil
		e.p.mu.Unlock()
		pages := step.Pages
		ctx, cancel := context.WithCancel(context.Background())
		e.p.read = func(_ context.Context, q historyCall) ([]types.Inbound, error) {
			n := e.p.count() - 1 // HistoryPage 已经记过这一次
			if n >= len(pages) && len(pages) > 0 && pages[len(pages)-1].Repeat {
				n = len(pages) - 1
			}
			if n >= len(pages) {
				t.Errorf("场景 %s 第 %d 步：第 %d 次查询没有脚本页", sc.ID, si+1, n+1)
				return nil, errors.New("脚本页用完")
			}
			pg := pages[n]
			if pg.Cancel {
				cancel()
			}
			for _, m := range pg.LiveDuring {
				e.save(mk(m))
			}
			var rows []types.Inbound
			for _, m := range pg.Messages {
				rows = append(rows, mk(m))
			}
			if pg.Error != "" {
				return rows, errors.New(pg.Error)
			}
			return rows, nil
		}
		res := bfExpect{}
		if err := e.b.round(ctx); err != nil {
			res.Error = err.Error()
		}
		cancel()
		now := e.clk.Now().UnixMilli()
		e.p.mu.Lock()
		res.Calls = []bfCall{}
		for _, c := range e.p.calls {
			res.Calls = append(res.Calls, bfCall{c.anchor, c.count})
		}
		e.p.mu.Unlock()
		res.Stored = []bfStored{}
		rows, err := e.st.Events.Query(`SELECT m.platform_id,e.created_at FROM events e JOIN channel_msgs m ON m.event_seq=e.seq ORDER BY e.seq`)
		if err != nil {
			t.Fatal(err)
		}
		for rows.Next() {
			var id string
			var at int64
			if err := rows.Scan(&id, &at); err != nil {
				t.Fatal(err)
			}
			res.Stored = append(res.Stored, bfStored{id, now - at})
		}
		rows.Close()
		res.Gaps = []bfGapOut{}
		for _, g := range e.gaps() {
			res.Gaps = append(res.Gaps, bfGapOut{now - g.FromMS, now - g.ToMS, g.State, g.Reason, g.Tries, now - g.LastTryMS})
		}
		sort.SliceStable(res.Gaps, func(i, j int) bool {
			a, b := res.Gaps[i], res.Gaps[j]
			if a.FromAgoMS != b.FromAgoMS {
				return a.FromAgoMS > b.FromAgoMS
			}
			if a.ToAgoMS != b.ToAgoMS {
				return a.ToAgoMS > b.ToAgoMS
			}
			return a.State < b.State
		})
		res.WatermarkAgoMS = now - e.through()
		var c bfContinuity
		switch err := e.st.Events.QueryRow(`SELECT from_ms,through_ms FROM qq_backfill_continuity WHERE account=? AND conversation=?`, testAccount, conv.String()).Scan(&c.FromAgoMS, &c.ThroughAgoMS); {
		case err == nil:
			c.FromAgoMS, c.ThroughAgoMS = now-c.FromAgoMS, now-c.ThroughAgoMS
			res.Continuity = &c
		case !errors.Is(err, sql.ErrNoRows):
			t.Fatal(err)
		}
		open, abandoned, _, err := e.st.AlignmentGapCounts(context.Background(), testAccount, e.b.allowed)
		if err != nil {
			t.Fatal(err)
		}
		res.CompleteCoverage = open+abandoned == 0
		out = append(out, res)
	}
	return out
}
