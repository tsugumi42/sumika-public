package channel

// 按平台分库的验收（ADR-199、规格-聊天记录按平台分库 第四、八节）：真的用 OpenGo 开 local、qq 两个库，
// 验消息和计划各进各的库、没登记的渠道什么都不写、闸门按会话取对库、出口启动时每个平台库各扫一次。
// 别的测试里的 store.Single 所有平台都是同一个库，证明不了"选对了库"，这几条必须开真的两个库（set.go 的 Single 注释）。

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// openTwo 开一组真的两个库（local、qq），收尾时关掉
func openTwo(t *testing.T) *store.Set {
	t.Helper()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = set.Close() })
	return set
}

func dbOf(t *testing.T, set *store.Set, platform string) *store.Store {
	t.Helper()
	st, ok := set.Platform(platform)
	if !ok {
		t.Fatalf("平台 %s 的库没打开", platform)
	}
	return st
}

// tableRows 数一张表的行；表名是测试里写死的几个，不是从外面来的
func tableRows(t *testing.T, st *store.Store, table string) int {
	t.Helper()
	return countRows(t, st, `SELECT count(*) FROM `+table)
}

// 一个 QQ 群会话进来一条消息（Intake 入库那条路），再为它出去一个计划（Outlet 写计划那条路，不 Start）：
// 四张表都在 qq 库里有行，local 库一行都没有
func TestSplitInboundAndOutboundWriteTheirPlatformDB(t *testing.T) {
	t.Parallel()
	set := openTwo(t)
	ctx := context.Background()

	in, err := NewIntake(Config{Stores: set, Roster: testRoster(), Now: func() time.Time { return baseTime }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(in.Close)

	qqConv := group("g1")
	tk, err := in.Submit(ctx, msg(acctA, qqConv, "9101", friend, "QQ 群里的一句话"), Options{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tk.Wait(ctx); err != nil {
		t.Fatalf("QQ 群消息入库失败：%v", err)
	}

	out, err := NewOutlet(OutletConfig{Stores: set, Roster: testRoster(), Adapters: []types.Adapter{fake.New(chQQ, types.Caps{})}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(out.Close)
	if _, err := out.SendHeld(ctx, "ca", types.OriginCharacter, qqConv, []types.Part{txt("QQ 群里的回复")}); err != nil {
		t.Fatalf("QQ 群的发送计划没写进库：%v", err)
	}

	qq := dbOf(t, set, "qq")
	for _, table := range []string{"events", "channel_msgs", "observations", "outbound_plans"} {
		if n := tableRows(t, qq, table); n == 0 {
			t.Errorf("qq 库的 %s 该有行，一行都没有", table)
		}
	}
	local := dbOf(t, set, "local")
	for _, table := range []string{"events", "channel_msgs", "observations", "outbound_plans"} {
		if n := tableRows(t, local, table); n != 0 {
			t.Errorf("local 库的 %s 不该有 QQ 会话的行，有 %d 行", table, n)
		}
	}
}

// 没登记的渠道（名册里配了一个号，让它走到入库那一步）：入库报错，两个库的 events 都是 0 行
func TestSplitUnregisteredChannelWritesNothing(t *testing.T) {
	t.Parallel()
	set := openTwo(t)
	ctx := context.Background()

	ro := Roster{
		Owners:   map[string]string{"telegram": owner},
		Accounts: []Account{{Channel: "telegram", ID: "40001", Character: "ca"}},
	}
	in, err := NewIntake(Config{Stores: set, Roster: ro, Now: func() time.Time { return baseTime }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(in.Close)

	conv := types.Conversation{Channel: "telegram", Scene: types.SceneGroup, ID: "tg-1"}
	tk, err := in.Submit(ctx, msg("40001", conv, "1", friend, "没登记的渠道"), Options{})
	if err == nil { // 收下的这一条，失败要在入库那一步报出来
		_, err = tk.Wait(ctx)
	}
	if err == nil {
		t.Fatal("没登记的渠道该报错")
	}
	if !strings.Contains(err.Error(), "没有登记平台") {
		t.Fatalf("报的错该说没登记平台：%v", err)
	}
	for _, platform := range []string{"local", "qq"} {
		if n := tableRows(t, dbOf(t, set, platform), "events"); n != 0 {
			t.Errorf("%s 库不该有事件，有 %d 行", platform, n)
		}
	}
}

// 两个库各一条 seq=1（文字不同）：闸门按 QQ 会话读"攒着的消息"，读到的是 QQ 库那一条；按本地会话读，读到本地的
func TestSplitGateReadsPendingFromConversationPlatform(t *testing.T) {
	t.Parallel()
	set := openTwo(t)

	qqConv := group("g1")
	localConv := types.Conversation{Channel: "console", Scene: types.SceneGroup, ID: "lg1"}
	for _, c := range []struct {
		store *store.Store
		conv  types.Conversation
		text  string
	}{
		{dbOf(t, set, "qq"), qqConv, "QQ 群里的话"},
		{dbOf(t, set, "local"), localConv, "本地群里的话"},
	} {
		res, err := c.store.AppendInbound(store.NewInbound{Event: store.NewEvent{
			Channel: c.conv.Channel, Conversation: c.conv.String(), ActorKind: "human", ActorID: friend,
			Origin: "peer", Kind: "message", Text: c.text, CreatedAt: baseTime.UnixMilli(),
		}})
		if err != nil {
			t.Fatal(err)
		}
		if res.Seq != 1 {
			t.Fatalf("%s 库的第一条该是 seq 1：%d", c.conv.Channel, res.Seq)
		}
	}

	g, err := NewGate(GateConfig{Stores: set, Roster: testRoster(), Release: func(types.Batch) {}, Loc: time.UTC})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(g.Close)

	for _, c := range []struct {
		conv types.Conversation
		want string
	}{{qqConv, "QQ 群里的话"}, {localConv, "本地群里的话"}} {
		rows, _, _, _, err := g.pending(types.Batch{Conv: c.conv, Character: "ca", Account: acctA}, 0)
		if err != nil {
			t.Fatalf("%s 读攒着的消息出错：%v", c.conv.String(), err)
		}
		if len(rows) != 1 || rows[0].Text != c.want {
			var got []string
			for _, r := range rows {
				got = append(got, r.Text)
			}
			t.Fatalf("%s 该只读到 %q，读到的是 %v", c.conv.String(), c.want, got)
		}
	}
}

// 启动时每个平台库各扫一次：两个库里各一条"发送中"的计划，NewOutlet 以后两条都清理了（状态不再是发送中）
func TestSplitOutletSweepsEveryPlatformOnStart(t *testing.T) {
	t.Parallel()
	set := openTwo(t)

	plans := []struct {
		platform string
		conv     types.Conversation
		id       string
	}{
		{"qq", group("g1"), "P-qq"},
		{"local", types.Conversation{Channel: "console", Scene: types.SceneGroup, ID: "lg1"}, "P-local"},
	}
	for _, c := range plans {
		st := dbOf(t, set, c.platform)
		_, err := st.AppendPlan(store.NewPlan{
			ID: c.id, CharacterID: "ca", Conversation: c.conv.String(), Account: acctA, Origin: "character",
			Kind: store.PlanKindSend, At: baseTime.UnixMilli(),
			Event: &store.NewEvent{
				Channel: c.conv.Channel, Conversation: c.conv.String(), ActorKind: "character", ActorID: "ca",
				Origin: "character", Kind: "message", Text: "上次没发完的话", CreatedAt: baseTime.UnixMilli(),
			},
			Observers: []string{"ca"},
			Parts:     []store.NewPart{{Kind: "text", Text: "上次没发完的话"}},
		})
		if err != nil {
			t.Fatalf("%s 库造计划出错：%v", c.platform, err)
		}
	}

	out, err := NewOutlet(OutletConfig{Stores: set, Roster: testRoster()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(out.Close)

	for _, c := range plans {
		p, err := dbOf(t, set, c.platform).GetPlan(c.id)
		if err != nil {
			t.Fatalf("%s 库读计划出错：%v", c.platform, err)
		}
		if p.State == store.PlanSending {
			t.Errorf("%s 库的计划 %s 还是发送中：没被启动时的 Sweep 扫到", c.platform, c.id)
		}
		if len(p.Parts) != 1 || p.Parts[0].State == store.PartPending {
			t.Errorf("%s 库的计划 %s 的段没被清理：%+v", c.platform, c.id, p.Parts)
		}
	}
}

// 库组已经关了：出口不启动（不然上次没发完的计划没人清理，也没有一行日志）
func TestSplitOutletRefusesClosedStores(t *testing.T) {
	t.Parallel()
	set := openTwo(t)
	if err := set.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := NewOutlet(OutletConfig{Stores: set, Roster: testRoster()}); err == nil {
		t.Fatal("库组关了以后出口还启动了")
	}
}
