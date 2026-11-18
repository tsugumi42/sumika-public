package app

// 大图没回声以后换形式、原来那份的回声晚到时，撤回补发的那份（规格-链接发送改版 B15）：宿主这一道门，
// 和结果不明时的表情。插件那一半（时间窗、竞态、文字版）在 linkplugin/late_test.go，用假出口测；
// 这里走真装配、真库里的计划，大图的 30 秒等待在这套真时钟的夹具里等不起，所以用小图造出两份计划，再把原段改成“被回声认领”。

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkplugin"
)

type dupPart struct {
	planID, id string
	idx        int
}

// dupScenario 造出两份计划：原图没确认也没进群，转 180° 的图确认了（回包的号）。
// 返回原段（已改成被回声认领，号 echoID）和补发份
func dupScenario(t *testing.T, entry string, echoID string, opaque bool) (*testEnv, *App, *rejectPeer, dupPart, dupPart, *websocket.Conn) {
	t.Helper()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 && !f.Images[0].LeftBlue {
			return replyUnconfirmed // 原图：没确认、也没进群
		}
		return replyOK
	})
	conn := startSend(t, e, a)
	scriptLink(t, e, a, conn, entry, "961", "https://x.com/paper/status/76001")
	waitImages(t, peer, 2)
	peer.settle(t)
	st, _ := a.set.For("qq")
	rows, err := st.Events.Query(`SELECT plan_id, idx, state, COALESCE(platform_id, '') FROM outbound_parts WHERE kind = 'message' AND text LIKE '%"media"%' ORDER BY sent_at, plan_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var orig, dup dupPart
	for rows.Next() {
		var p dupPart
		var state string
		if err := rows.Scan(&p.planID, &p.idx, &state, &p.id); err != nil {
			t.Fatal(err)
		}
		if state == "confirmed" {
			dup = p
		} else {
			orig = p
		}
	}
	if orig.planID == "" || dup.planID == "" || dup.id == "" {
		t.Fatalf("要有一份没确认的原段和一份确认了的补发份：orig=%+v dup=%+v", orig, dup)
	}
	// 原来的回声晚到：走真的出口认领（回声里图的 md5 就是原图的），原段转成确认（via echo、回声的号），
	// 出口记下这是强匹配；宿主门槛不信插件，问的就是出口这份记录
	var origFrame sentFrame
	for _, f := range peer.imageFrames() {
		if !f.Images[0].LeftBlue {
			origFrame = f
		}
	}
	if len(origFrame.Images) == 0 {
		t.Fatal("没找到原图那一帧")
	}
	in := types.Inbound{Kind: types.InboundSelf, Account: testAccount, Conv: linkConv(entry), PlatformID: echoID, From: types.User{ID: testAccount}, Time: e.now}
	for _, tx := range origFrame.Texts {
		in.Segs = append(in.Segs, types.Seg{Kind: types.SegText, Text: tx})
	}
	file := origFrame.Images[0].MD5 + ".png"
	if opaque {
		file = "opaque-file-id" // NT 的不透明 fileId：读不出 md5，只凭形状认领（弱匹配）
	}
	in.Segs = append(in.Segs, types.Seg{Kind: types.SegImage, File: file})
	if ok, err := a.outlet.ClaimEcho(in); err != nil || !ok {
		t.Fatalf("原图的回声该被出口认领：%v %v", ok, err)
	}
	// 这套夹具的时钟不走，两段的 sent_at 是同一毫秒；真实里补发份至少在等回声（几百毫秒到 30 秒）之后才发。把原段往前拨 5 秒
	if _, err := st.Events.Exec(`UPDATE outbound_parts SET sent_at = sent_at - 5000 WHERE plan_id = ? AND idx = ?`, orig.planID, orig.idx); err != nil {
		t.Fatal(err)
	}
	orig.id = echoID
	return e, a, peer, orig, dup, conn
}

// 宿主按库里的计划独立判：成立就只对补发份发 delete_msg；号、段、会话、状态、来源、时限、发送许可任一不对都不调适配器
func TestRecallLinkDupHostGate(t *testing.T) {
	t.Parallel()
	eachEntry(t, func(t *testing.T, entry string) {
		e, a, peer, orig, dup, _ := dupScenario(t, entry, "-7001", false)
		conv := linkConv(entry)
		ctx := context.Background()
		ref := func(p dupPart) linkplugin.PartRef {
			return linkplugin.PartRef{PlanID: p.planID, Idx: p.idx, PlatformID: p.id}
		}
		st, _ := a.set.For("qq")
		set := func(q string, args ...any) {
			t.Helper()
			if _, err := st.Events.Exec(q, args...); err != nil {
				t.Fatal(err)
			}
		}
		refuse := func(name string, err error, why string) {
			t.Helper()
			if err == nil || !strings.Contains(err.Error(), "撤回门槛不成立") || !strings.Contains(err.Error(), why) {
				t.Errorf("%s：宿主应拒绝（%s）：%v", name, why, err)
			}
		}

		bad := func(o, d linkplugin.PartRef) error { return a.recallLinkDup(ctx, conv, o, d) }
		wrongOrigID, wrongDupID := ref(orig), ref(dup)
		wrongOrigID.PlatformID = "-123"
		wrongDupID.PlatformID = "-124"
		sameID := ref(dup)
		sameID.PlatformID = orig.id
		refuse("原段号不对", bad(wrongOrigID, ref(dup)), "原段不是被回声认领的")
		refuse("补发号不对", bad(ref(orig), wrongDupID), "补发份不是确认送到的那一段")
		refuse("补发号和回声同号", bad(ref(orig), sameID), "同一个")
		refuse("号为空", bad(ref(orig), linkplugin.PartRef{PlanID: dup.planID, Idx: dup.idx}), "平台消息号不明")
		refuse("号为 0", bad(ref(orig), linkplugin.PartRef{PlanID: dup.planID, Idx: dup.idx, PlatformID: "0"}), "平台消息号不明")
		refuse("别的段", bad(ref(orig), linkplugin.PartRef{PlanID: dup.planID, Idx: dup.idx + 1, PlatformID: dup.id}), "计划里没有这一段")
		refuse("别的计划", bad(ref(orig), linkplugin.PartRef{PlanID: "不存在的计划", Idx: 0, PlatformID: dup.id}), "读不到")
		refuse("别的会话", a.recallLinkDup(ctx, otherConv(entry), ref(orig), ref(dup)), "没有发送许可")
		refuse("本地会话", a.recallLinkDup(ctx, types.Conversation{Channel: "local", Scene: types.SceneGroup, ID: "g1"}, ref(orig), ref(dup)), "不是 QQ 会话")

		// 两段的关系（复核问题 1、3）：补发份不晚于原段、原段是转发段、出口没记这个计划（查不到是为哪条源消息发的），都不撤
		set(`UPDATE outbound_parts SET sent_at = sent_at + 5000 WHERE plan_id = ? AND idx = ?`, orig.planID, orig.idx)
		refuse("补发份不晚于原段", bad(ref(orig), ref(dup)), "补发份不晚于原段")
		set(`UPDATE outbound_parts SET sent_at = sent_at - 5000 WHERE plan_id = ? AND idx = ?`, orig.planID, orig.idx)
		set(`UPDATE outbound_parts SET kind = 'forward' WHERE plan_id = ? AND idx = ?`, orig.planID, orig.idx)
		refuse("原段是转发段", bad(ref(orig), ref(dup)), "转发段")
		set(`UPDATE outbound_parts SET kind = 'message' WHERE plan_id = ? AND idx = ?`, orig.planID, orig.idx)
		ghost := dup.planID + "-ghost" // 库里有、出口没提交过的计划：宿主查不到它是为哪条源消息发的
		set(`INSERT INTO outbound_plans (id, character_id, conversation, event_seq, account, origin, kind, reason, state, created_at, done_at)
			SELECT ?, character_id, conversation, NULL, account, origin, kind, reason, state, created_at, done_at FROM outbound_plans WHERE id = ?`, ghost, dup.planID)
		set(`INSERT INTO outbound_parts (plan_id, idx, kind, text, ref, pause_ms, state, attempts, platform_id, via, err, sent_at)
			SELECT ?, idx, kind, text, ref, pause_ms, state, attempts, platform_id, via, err, sent_at FROM outbound_parts WHERE plan_id = ?`, ghost, dup.planID)
		refuse("出口没记这个计划", bad(ref(orig), linkplugin.PartRef{PlanID: ghost, Idx: dup.idx, PlatformID: dup.id}), "查不到")
		// 补发份不是确认的：不撤
		set(`UPDATE outbound_parts SET state = 'unknown' WHERE plan_id = ? AND idx = ?`, dup.planID, dup.idx)
		refuse("补发份没确认", bad(ref(orig), ref(dup)), "补发份不是确认送到的那一段")
		set(`UPDATE outbound_parts SET state = 'confirmed' WHERE plan_id = ? AND idx = ?`, dup.planID, dup.idx)
		// 原段不是回声认领的（回包确认）：不撤
		set(`UPDATE outbound_parts SET via = 'reply' WHERE plan_id = ? AND idx = ?`, orig.planID, orig.idx)
		refuse("原段不是回声认领", bad(ref(orig), ref(dup)), "原段不是被回声认领的")
		set(`UPDATE outbound_parts SET via = 'echo' WHERE plan_id = ? AND idx = ?`, orig.planID, orig.idx)
		// 来源不是链接展开
		set(`UPDATE outbound_plans SET origin = 'character' WHERE id = ?`, dup.planID)
		refuse("补发份来源不是链接展开", bad(ref(orig), ref(dup)), "不是链接展开发的")
		set(`UPDATE outbound_plans SET origin = ? WHERE id = ?`, linkOrigin, dup.planID)
		// 没有发送许可
		was := a.currentLinks()
		off := &LinkRuntime{Values: was.Values.Clone(), Version: was.Version}
		off.Values.Expand.Enabled = false
		a.linkRuntime.Store(off)
		refuse("没有发送许可", bad(ref(orig), ref(dup)), "没有发送许可")
		a.linkRuntime.Store(was)
		if got := peer.recallSnapshot(); len(got) != 0 {
			t.Fatalf("门槛不成立不该发 delete_msg：%v", got)
		}

		// 都成立：撤的是补发份的号，不是原段回声的号
		if err := bad(ref(orig), ref(dup)); err != nil {
			t.Fatalf("门槛都成立该能撤：%v", err)
		}
		if got := peer.recallSnapshot(); len(got) != 1 || got[0] != dup.id || got[0] == orig.id {
			t.Fatalf("只撤补发份 %s，不撤原段的回声 %s：%v", dup.id, orig.id, got)
		}

		// 超过 2 分钟（从补发份第一次调 Send 起）：不撤
		e.nowMu.Lock()
		e.now = e.now.Add(linkplugin.RecallWithin + time.Second)
		e.nowMu.Unlock()
		refuse("超过两分钟", bad(ref(orig), ref(dup)), "超过 2 分钟")
		if got := peer.recallSnapshot(); len(got) != 1 {
			t.Fatalf("超时不该再发 delete_msg：%v", got)
		}
	})
}

// 结果不明、一段都没确认送到：撤掉开始表情，不贴完成也不贴失败（B15）。
// 以前这种结果算“送到了”，群里什么也没出现却贴了“完成”。改坏检查：expand_flow.go 里把这一支改回 delivered，这条红
func TestUnknownResultPostsNoDoneEmoji(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 {
			return replyUnconfirmed
		}
		return replyOK
	}, historyCheckOn, "    doneEmoji: \"124\"\n    failEmoji: \"9\"\n")
	peer.histSilent = true // 历史查不了：图的结果不明，不换形式、不补发
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "962", "https://x.com/paper/status/76002")
	waitFor(t, "开始表情贴了又撤了", func() bool {
		trail, _ := reactionTrail(peer.snapshot())
		return len(trail) >= 2 && trail[0] == "贴 76" && trail[1] == "撤 76"
	})
	peer.settle(t)
	trail, _ := reactionTrail(peer.snapshot())
	if strings.Join(trail, "、") != "贴 76、撤 76" {
		t.Fatalf("结果不明：只贴开始、撤开始，不贴完成（124）也不贴失败（9）：%v", trail)
	}
	if got := len(peer.imageFrames()); got != 1 {
		t.Fatalf("结果不明不换形式、不补发：%d 帧", got)
	}
}

// 原段是弱匹配（回声里图的标识读不出，只凭形状认领）：出口记的不是强匹配，宿主不撤，哪怕两段、号、时间、源消息都对得上。
// 改坏检查：去掉 recallDupRefusal 里的 ClaimedStrong 判断，这条红（撤了）
func TestRecallLinkDupHostRefusesWeakClaim(t *testing.T) {
	t.Parallel()
	eachEntry(t, func(t *testing.T, entry string) {
		_, a, peer, orig, dup, _ := dupScenario(t, entry, "-7001", true)
		err := a.recallLinkDup(context.Background(), linkConv(entry),
			linkplugin.PartRef{PlanID: orig.planID, Idx: orig.idx, PlatformID: orig.id}, linkplugin.PartRef{PlanID: dup.planID, Idx: dup.idx, PlatformID: dup.id})
		if err == nil || !strings.Contains(err.Error(), "强匹配") {
			t.Fatalf("弱认领不撤：%v", err)
		}
		if got := peer.recallSnapshot(); len(got) != 0 {
			t.Fatalf("不该发 delete_msg：%v", got)
		}
	})
}

// 补发份属于另一次展开（另一条源消息）：哪怕两段各自合规，也不能拿它当这个原段的补发份撤。
// 改坏检查：去掉 recallDupRefusal 里比较源消息的那一支，这条红（撤了）
func TestRecallLinkDupHostRefusesOtherSource(t *testing.T) {
	t.Parallel()
	eachEntry(t, func(t *testing.T, entry string) {
		e, a, peer, orig, dup, conn := dupScenario(t, entry, "-7001", false)
		scriptLink(t, e, a, conn, entry, "962", "https://x.com/paper/status/76002")
		waitFor(t, "第二条展开也发出了确认的图", func() bool {
			st, _ := a.set.For("qq")
			var n int
			_ = st.Events.QueryRow(`SELECT count(*) FROM outbound_parts WHERE plan_id NOT IN (?, ?) AND kind = 'message' AND state = 'confirmed' AND text LIKE '%"media"%'`, orig.planID, dup.planID).Scan(&n)
			return n > 0
		})
		peer.settle(t)
		st, _ := a.set.For("qq")
		var other dupPart
		if err := st.Events.QueryRow(`SELECT plan_id, idx, platform_id FROM outbound_parts WHERE plan_id NOT IN (?, ?) AND kind = 'message' AND state = 'confirmed' AND text LIKE '%"media"%' ORDER BY sent_at, plan_id`, orig.planID, dup.planID).Scan(&other.planID, &other.idx, &other.id); err != nil {
			t.Fatal(err)
		}
		conv := linkConv(entry)
		o := linkplugin.PartRef{PlanID: orig.planID, Idx: orig.idx, PlatformID: orig.id}
		err := a.recallLinkDup(context.Background(), conv, o, linkplugin.PartRef{PlanID: other.planID, Idx: other.idx, PlatformID: other.id})
		if err == nil || !strings.Contains(err.Error(), "不是同一条源消息") {
			t.Fatalf("另一条源消息的补发份不撤：%v", err)
		}
		if got := peer.recallSnapshot(); len(got) != 0 {
			t.Fatalf("不该发 delete_msg：%v", got)
		}
		// 对照：本来那一对仍能撤
		if err := a.recallLinkDup(context.Background(), conv, o, linkplugin.PartRef{PlanID: dup.planID, Idx: dup.idx, PlatformID: dup.id}); err != nil {
			t.Fatalf("同一条源消息的那一对该能撤：%v", err)
		}
	})
}
