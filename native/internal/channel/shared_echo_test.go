package channel

// 共用场景（testdata/shared/qq-send，格式见该目录 README.md）：发出一条 / 几条话，协议端的回执怎么回（确认、明确失败、结果未知、迟迟不回），
// 回声（协议端把我们自己发的消息推回来）怎么认领，每一段最后是什么状态、用的哪个平台消息号。
// 用真实 Outlet、临时库、假适配器和假钟。SUMIKA_SHARED_UPDATE=1 把实际结果写回各文件的 expect。

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/sharedtest"
)

type seStep struct {
	Do         string   `json:"do"` // say | wait_sent | wait_part | wait_plan | pump | wait_timer | advance | claim | release
	Label      string   `json:"label,omitempty"`
	As         string   `json:"as,omitempty"`          // say：角色 ca（A 号）或 cb（B 号）
	Texts      []string `json:"texts,omitempty"`       // say：每条一段
	N          int      `json:"n,omitempty"`           // wait_sent：适配器收到几次 Send
	Idx        int      `json:"idx,omitempty"`         // wait_part：第几段
	State      string   `json:"state,omitempty"`       // wait_part、wait_plan：等到什么状态
	MS         int64    `json:"ms,omitempty"`          // wait_timer、advance
	Account    string   `json:"account,omitempty"`     // claim：回声是哪个号发的 a | b
	Conv       string   `json:"conv,omitempty"`        // claim：g1（默认）| g2
	PlatformID string   `json:"platform_id,omitempty"` // claim
	Text       string   `json:"text,omitempty"`        // claim
	Media      bool     `json:"media,omitempty"`       // claim：回声里是图片，不是文字
	Expect     seOut    `json:"expect"`
}
type sePart struct {
	State      string `json:"state"`
	Via        string `json:"via"`
	PlatformID string `json:"platform_id"`
	Attempts   int    `json:"attempts"`
}
type sePlan struct {
	Label string   `json:"label"`
	State string   `json:"state"`
	Parts []sePart `json:"parts"`
}
type seOut struct {
	Claimed *bool   `json:"claimed,omitempty"` // claim：认领了没有
	Plan    *sePlan `json:"plan,omitempty"`    // wait_part、wait_plan：那一刻这条计划的样子
}
type seFinal struct {
	Plans    []sePlan          `json:"plans"`
	Sends    int               `json:"sends"`    // 适配器一共收到几次 Send
	Mapped   map[string]string `json:"mapped"`   // 登记了映射的平台消息号 -> 指向哪条计划（label）；没映射的号不在里面
	Unmapped []string          `json:"unmapped"` // claim 用过、但没有登记映射的平台消息号
}
type seScenario struct {
	ID          string   `json:"id"`
	About       string   `json:"about"`
	SourceTests []string `json:"source_tests"`
	// Receipt：unknown 每次都回结果未知；confirmed 回确认（号 8000+第几次）；failed_first 第一次明确失败（等重试）、之后确认；
	// rejected 每次都明确拒绝（用完即失败）；hang_first 第一次 Send 迟迟不回（等 release），之后确认，迟到的回包带号 9999
	Receipt string   `json:"receipt"`
	Steps   []seStep `json:"steps"`
	Final   seFinal  `json:"final"`
}

func TestSharedQQSend(t *testing.T) {
	t.Parallel()
	for _, path := range sharedtest.Files(t, "qq-send") {
		var sc seScenario
		sharedtest.Load(t, path, &sc)
		t.Run(sc.ID, func(t *testing.T) {
			steps, final := runSendScenario(t, sc)
			if sharedtest.Update() {
				for i := range sc.Steps {
					sc.Steps[i].Expect = steps[i]
				}
				sc.Final = final
				sharedtest.Save(t, path, sc)
				return
			}
			for i, s := range sc.Steps {
				if ok, want, got := sharedtest.Same(s.Expect, steps[i]); !ok {
					t.Errorf("第 %d 步（%s）不同\n期望：%s\n实际：%s", i+1, s.Do, want, got)
				}
			}
			if ok, want, got := sharedtest.Same(sc.Final, final); !ok {
				t.Errorf("最终状态不同\n期望：%s\n实际：%s", want, got)
			}
		})
	}
}

func runSendScenario(t *testing.T, sc seScenario) ([]seOut, seFinal) {
	h := newOH(t, types.Caps{})
	h.run(nil)
	late, lateDone := make(chan struct{}), make(chan struct{})
	var once sync.Once
	switch sc.Receipt {
	case "unknown":
		h.unknownReceipts()
	case "confirmed":
		h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			return fake.Confirmed(c.Part.ID, fmt.Sprint(8000+c.N), c.At), nil
		})
	case "failed_first":
		h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			if c.N == 1 {
				return fake.Failed(c.Part.ID, "协议端说没发出去", c.At), nil
			}
			return fake.Confirmed(c.Part.ID, fmt.Sprint(6000+c.N), c.At), nil
		})
	case "rejected":
		h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			return fake.Failed(c.Part.ID, "拒绝", c.At), nil
		})
	case "hang_first":
		h.ad.OnSend(func(_ context.Context, c fake.SendCall) (types.Receipt, error) {
			if c.N == 1 {
				defer close(lateDone)
				<-late
				return fake.Confirmed(c.Part.ID, "9999", c.At), nil
			}
			return fake.Confirmed(c.Part.ID, fmt.Sprint(8000+c.N), c.At), nil
		})
	default:
		t.Fatalf("receipt 不认：%q", sc.Receipt)
	}
	plans := map[string]string{} // label -> plan id
	var order []string
	snap := func(label string) sePlan {
		p := h.plan(plans[label])
		out := sePlan{Label: label, State: p.State, Parts: []sePart{}}
		for _, pt := range p.Parts {
			out.Parts = append(out.Parts, sePart{pt.State, pt.Via, pt.PlatformID, pt.Attempts})
		}
		return out
	}
	acct := func(n string) string {
		switch n {
		case "", "a":
			return acctA
		case "b":
			return acctB
		}
		t.Fatalf("account 只认 a、b：%q", n)
		return ""
	}
	var outs []seOut
	var claimIDs []string
	for i, st := range sc.Steps {
		var out seOut
		switch st.Do {
		case "say":
			var parts []types.Part
			for _, s := range st.Texts {
				parts = append(parts, txt(s))
			}
			as := st.As
			if as == "" {
				as = "ca"
			}
			id, err := h.out.Send(context.Background(), as, types.OriginCharacter, group("g1"), parts)
			if err != nil {
				t.Fatal(err)
			}
			plans[st.Label] = id
			order = append(order, st.Label)
		case "wait_sent":
			h.waitSent(st.N)
		case "wait_part":
			h.waitPart(plans[st.Label], st.Idx, st.State)
			p := snap(st.Label)
			out.Plan = &p
		case "wait_plan":
			h.waitPlan(plans[st.Label], st.State)
			p := snap(st.Label)
			out.Plan = &p
		case "pump":
			// 一直拨钟（重试间隔、段间停顿），直到这条计划不再是发送中
			deadline := time.Now().Add(5 * time.Second)
			for h.plan(plans[st.Label]).State == "sending" {
				if time.Now().After(deadline) {
					t.Fatalf("拨钟 5 秒计划还在发送中：%s", st.Label)
				}
				if h.clk.Pending() > 0 {
					h.clk.Advance(time.Second)
				}
				time.Sleep(time.Millisecond)
			}
			p := snap(st.Label)
			out.Plan = &p
		case "wait_timer":
			h.clk.WaitTimer(t, time.Duration(st.MS)*time.Millisecond)
		case "advance":
			h.clk.Advance(time.Duration(st.MS) * time.Millisecond)
		case "claim":
			conv := group("g1")
			if st.Conv == "g2" {
				conv = group("g2")
			}
			in := echoOf(acct(st.Account), conv, st.PlatformID, st.Text)
			if st.Media {
				in.Segs = []types.Seg{{Kind: types.SegImage, Hash: "abc"}}
			}
			ok := h.claim(in)
			out.Claimed = &ok
			claimIDs = append(claimIDs, st.PlatformID)
		case "release":
			once.Do(func() { close(late) })
			<-lateDone
			time.Sleep(20 * time.Millisecond) // 给 goroutine 机会（错误的实现会在这里把已认领的段改回去）
		default:
			t.Fatalf("第 %d 步 do 不认：%q", i+1, st.Do)
		}
		outs = append(outs, out)
	}
	once.Do(func() { close(late) })
	final := seFinal{Plans: []sePlan{}, Mapped: map[string]string{}, Unmapped: []string{}}
	for _, l := range order {
		final.Plans = append(final.Plans, snap(l))
	}
	final.Sends = len(h.ad.Sent())
	rows, err := h.st.Events.Query(`SELECT m.platform_id, e.seq FROM channel_msgs m JOIN events e ON e.seq=m.event_seq`)
	if err != nil {
		t.Fatal(err)
	}
	type mapRow struct {
		pid string
		seq int64
	}
	var mapRows []mapRow // 先读完再关：库连接只有一条，读着行的时候不能再查别的
	for rows.Next() {
		var r mapRow
		if err := rows.Scan(&r.pid, &r.seq); err != nil {
			t.Fatal(err)
		}
		mapRows = append(mapRows, r)
	}
	rows.Close()
	bySeq := map[int64]string{}
	for _, l := range order {
		bySeq[h.plan(plans[l]).EventSeq] = l
	}
	for _, r := range mapRows {
		final.Mapped[r.pid] = bySeq[r.seq]
	}
	seen := map[string]bool{}
	for _, id := range claimIDs {
		if _, ok := final.Mapped[id]; !ok && !seen[id] {
			seen[id] = true
			final.Unmapped = append(final.Unmapped, id)
		}
	}
	sort.Strings(final.Unmapped)
	return outs, final
}
