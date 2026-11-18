package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

// 假协议端进入正式装配，复读与戳一戳均只生成试运行计划；不连真实服务。
func TestBotWiringPlugins(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.extraConfig = `plugins:
  sessions:
    "qq:group:300": {repeater: true, poke: true}
    "qq:private:100:200": {poke: false}
reply:
  deny: {"qq:401": ai, "qq:402": bot, "qq:403": blocked}
`
	e.writeConfigFreq(testToken, "0")
	guardFixture(t, e, false)
	e.expectNoActions(t)
	a := e.start()
	conn := e.peer.accept(t)
	conv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	push := func(id, who, text string) {
		e.peer.send(t, conn, groupFrame(e.now, id, who, text))
		waitFor(t, "当前帧入库并完成流水线", func() bool {
			st, _ := a.set.For("qq")
			_, ok, err := st.EventByPlatformID("qq", conv.String(), id)
			return ok && err == nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := a.intake.Idle(ctx); err != nil {
			t.Fatal(err)
		}
	}
	push("901", "401", "同一句")
	push("902", "404", "同一句")
	if plans, err := e.planKinds(a, conv.String()); err != nil || len(plans) != 0 {
		t.Fatalf("名单者凑够复读人数或读取失败：%v %v", plans, err)
	}
	push("903", "405", "同一句")
	plans, err := e.planKinds(a, conv.String())
	if err != nil || len(plans) != 1 || plans[0] != store.PlanKindDryRun {
		t.Fatalf("群配置没有启用复读：%v %v", plans, err)
	}
	if a.kernel.calls.Load() != 0 {
		t.Fatal("复读后还调用模型")
	}
	for _, who := range []string{"401", "402", "403", "406"} {
		n := types.Notice{Kind: types.NoticePoke, Conv: conv, Account: testAccount, Actor: who, Target: testAccount}
		b := types.Batch{Conv: conv, Character: testChar, Account: testAccount, Now: e.now, Trigger: types.NoticePoke, Notice: &n, Mine: []string{testAccount}}
		got := a.chain.Run(context.Background(), b)
		if got.Taken != (who == "406") {
			t.Fatalf("戳回去名单/群开关：%s %+v", who, got)
		}
	}
	// 同一套正式链的邻居：QQ 私聊可以显式关，其他群默认关，本地两种场合不运行 QQ 戳插件。
	for _, cv := range []types.Conversation{
		{Channel: "qq", Scene: types.ScenePrivate, Self: testAccount, ID: testOwner},
		{Channel: "qq", Scene: types.SceneGroup, ID: "301"},
		{Channel: "console", Scene: types.ScenePrivate, Self: "local", ID: "owner"},
		{Channel: "console", Scene: types.SceneGroup, ID: "room"},
	} {
		n := types.Notice{Kind: types.NoticePoke, Conv: cv, Account: testAccount, Actor: testOwner, Target: testAccount}
		if got := a.chain.Run(context.Background(), types.Batch{Conv: cv, Character: testChar, Now: e.now, Trigger: types.NoticePoke, Notice: &n}); got.Taken {
			t.Fatalf("邻居误开：%s %+v", cv.String(), got)
		}
	}
}

func TestBotWiringAliasesAndDeny(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, text, who string
		called          bool
	}{
		{"已填别名", "小澪在吗", testOwner, true},
		{"短英文", "DS?", testOwner, true},
		{"英文子串", "ADS DSL BDSM", testOwner, false},
		{"名单叫名", "小澪 DS", "401", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			e := newEnv(t)
			e.extraConfig = "reply:\n  deny: {\"qq:401\": blocked}\n"
			e.writeConfigFreq(testToken, "0")
			file := filepath.Join(e.data, "config", "sumika.yml")
			b, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			b = []byte(strings.Replace(string(b), "    model: "+testModel, "    model: "+testModel+"\n    aliases: [小澪, DS]", 1))
			if err := os.WriteFile(file, b, 0600); err != nil {
				t.Fatal(err)
			}
			e.expectNoActions(t)
			a := e.start()
			conn := e.peer.accept(t)
			e.peer.send(t, conn, groupFrame(e.now, "911", tc.who, tc.text))
			cv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
			waitFor(t, "叫名消息完成入库", func() bool { n, _ := e.eventCount(a, cv.String()); return n > 0 })
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := a.intake.Idle(ctx); err != nil {
				t.Fatal(err)
			}
			if err := a.responder.Idle(ctx); err != nil {
				t.Fatal(err)
			}
			if tc.called {
				waitFor(t, "叫名触发试运行回复", func() bool { p, _ := e.planKinds(a, cv.String()); return len(p) == 1 })
				d, ok, err := e.latestDecision(a, cv.String())
				if err != nil || !ok || d.Verdict != channel.VerdictForced {
					t.Fatalf("叫名未强制唤醒：%+v %v", d, err)
				}
			} else if a.kernel.calls.Load() != 0 {
				t.Fatal("不该叫名却调用内核")
			}
		})
	}
}

func TestBotWiringDeniedMessageAndNotice(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.extraConfig = "reply:\n  deny: {\"qq:401\": ai, \"qq:402\": bot, \"qq:403\": blocked}\nplugins:\n  sessions:\n    \"qq:group:300\": {poke: true}\n"
	e.writeConfig(testToken)
	a := e.open()
	t.Cleanup(a.cleanup)
	cv := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
	for i, who := range []string{"401", "402", "403", "", "0"} {
		in := types.Inbound{Kind: types.InboundMessage, Account: testAccount, Conv: cv, PlatformID: fmt.Sprint(920 + i), From: types.User{ID: who, Nick: "主人"}, Time: e.now, Segs: []types.Seg{{Kind: types.SegAt, Target: testAccount}, {Kind: types.SegText, Text: "小澪"}}}
		err := a.intake.Inbound(context.Background(), in)
		if who == "" {
			if err == nil {
				t.Fatal("缺身份消息被接收")
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := a.intake.Idle(ctx); err != nil {
		t.Fatal(err)
	}
	if a.kernel.calls.Load() != 0 {
		t.Fatal("名单或冒充名字触发模型")
	}
	if ds, err := a.gate.Decisions(cv, 10); err != nil || len(ds) != 0 {
		t.Fatalf("名单仍驱动闸门：%+v %v", ds, err)
	}
	st, _ := a.set.For("qq")
	var n int
	if err := st.Events.QueryRow(`SELECT count(*) FROM observations`).Scan(&n); err != nil || n != 4 {
		t.Fatalf("不接话误删可见上下文：%d %v", n, err)
	}
	// 不依赖显示名判断，三类号的通知均不能产生任何计划。
	for _, who := range []string{"401", "402", "403", "", "0"} {
		n := types.Notice{Kind: types.NoticePoke, Account: testAccount, Conv: cv, Actor: who, Target: testAccount, Time: e.now}
		a.pipeline.OnNotice(channel.Noticed{Notice: n, Character: testChar, Observed: true})
	}
	if plans, err := e.planKinds(a, cv.String()); err != nil || len(plans) != 0 {
		t.Fatalf("名单通知绕过触发限制或读取失败：%v %v", plans, err)
	}
}
