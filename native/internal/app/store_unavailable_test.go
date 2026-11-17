package app

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/fake"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/dbguard"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/selfcheck"
	"sumika-native/internal/store"
)

// makeStoreTooNew 造一个 min_reader 比本程序新的库：打开时护栏判“太新”，平台整个停掉。
func makeStoreTooNew(t *testing.T, data, platform string) {
	t.Helper()
	p, err := paths.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.ChatDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenChatFile(p.ChatDB(platform), platform)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sumika_schema SET min_reader = 999`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
}

func evalStoreUnavailable(t *testing.T, a *App) selfcheck.Result {
	t.Helper()
	for _, c := range selfcheck.Checks(selfcheck.DefaultThresholds()) {
		if c.ID() == "store.unavailable" {
			env := &selfcheck.Env{StoreUnavailable: a.storeUnavailableSelfcheck}
			return c.Eval(context.Background(), env)
		}
	}
	t.Fatal("没有 store.unavailable 这项自检")
	return selfcheck.Result{}
}

// qq 的库太新：App 照常打开，QQ 通道、补录一个都不起；本地私聊照常收发；S14 bad；db status 里 qq 不可用。
func TestQQStoreUnavailableKeepsLocalChat(t *testing.T) {
	e := newEnv(t)
	makeStoreTooNew(t, e.data, "qq")
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("本地回复")})
	if got := a.set.Platforms(); len(got) != 1 || got[0] != "local" {
		t.Fatalf("打开的平台是 %v，要只有 local", got)
	}
	for _, u := range a.channels {
		if u.channel == "qq" {
			t.Fatal("QQ 的库没打开，QQ 通道却装进了渠道清单")
		}
	}
	if a.backfill != nil {
		t.Fatal("QQ 的库没打开，补录却装上了")
	}
	lines := a.StoreUnavailableLines()
	if len(lines) != 1 || !strings.Contains(lines[0], "qq 的库没打开") || !strings.Contains(lines[0], "sumika-ctl db status") {
		t.Fatalf("宿主状态没写 QQ 的原因：%v", lines)
	}
	ctx := context.Background()
	key := localKey("console:" + testChar)
	if err := d.Reader.SendLocal(ctx, key, "你好", nil); err != nil {
		t.Fatalf("本地私聊不能发：%v", err)
	}
	page, err := d.Reader.ReadMessages(ctx, api.ConversationRequest{Key: key, Limit: 20}, "")
	if err != nil {
		t.Fatalf("本地私聊不能读：%v", err)
	}
	if len(page.Value.Items) != 2 || page.Value.Items[0].Text != "你好" || page.Value.Items[1].Text != "本地回复" {
		t.Fatalf("本地私聊的记录：%+v", page.Value.Items)
	}
	r := evalStoreUnavailable(t, a)
	if r.State != selfcheck.Bad || r.Next == "" || r.Example["platform"] != "qq" || r.Example["reason"] != dbguard.ReasonTooNew {
		t.Fatalf("S14 没报 qq：%+v", r)
	}
	status, err := a.DBStatus(ctx)
	if err != nil {
		t.Fatalf("db status：%v", err)
	}
	var found bool
	for _, db := range status.Databases {
		if db.Name == "qq" {
			found = true
			if db.Status != "unavailable" || db.Reason != dbguard.ReasonTooNew || db.Path == "" {
				t.Fatalf("db status 里 qq：%+v", db)
			}
		}
		if db.Name == "trace" && (db.Status != "ok" || len(db.Tracks) == 0 || db.Tracks[0].Latest == 0) {
			t.Fatalf("db status 里 trace：%+v", db)
		}
	}
	if !found {
		t.Fatal("db status 里没有 qq")
	}
}

// local 的库太新：App 照常打开、不 panic；桌面读本地返回带原因的错误；本地发送也被拒；QQ 照常可用。
func TestLocalStoreUnavailableDesktopReadFails(t *testing.T) {
	e := newEnv(t)
	makeStoreTooNew(t, e.data, "local")
	a, d := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("不会用到")})
	if a.groups != nil {
		t.Fatal("local 的库没打开，本地群聊却装上了")
	}
	if _, ok := a.set.Platform("qq"); !ok {
		t.Fatal("local 不可用时 QQ 被一起停了")
	}
	ctx := context.Background()
	key := localKey("console:" + testChar)
	_, err := d.Reader.ReadMessages(ctx, api.ConversationRequest{Key: key, Limit: 20}, "")
	if err == nil || !strings.Contains(err.Error(), "平台 local 的库没打开") {
		t.Fatalf("桌面读本地没有带原因的错误：%v", err)
	}
	var u *dbguard.Unavailable
	if !errors.As(err, &u) {
		t.Fatalf("桌面读本地的错误里取不到 *dbguard.Unavailable：%v", err)
	}
	if err := d.Reader.SendLocal(ctx, key, "发不出去", nil); err == nil {
		t.Fatal("local 不可用，本地发送却成功了")
	}
	if r := evalStoreUnavailable(t, a); r.State != selfcheck.Bad || r.Example["platform"] != "local" {
		t.Fatalf("S14 没报 local：%+v", r)
	}
}

// 全部打开时 S14 是 ok（App 真的把 Set.Unavailable 交给了自检）。
func TestStoreUnavailableCheckOkWhenAllOpen(t *testing.T) {
	e := newEnv(t)
	a := e.open()
	t.Cleanup(func() { a.Close() })
	if r := evalStoreUnavailable(t, a); r.State != selfcheck.Ok {
		t.Fatalf("库都开着，S14 却不是 ok：%+v", r)
	}
}

// W14：QQ 的库不可用时 readPeople 不去开 qq.db（把表删掉：真开了会报错），名册空着。
func TestReadPeopleSkipsUnavailableQQStore(t *testing.T) {
	e := newEnv(t)
	p, err := paths.Open(e.data)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(p.ChatDir(), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenChatFile(p.ChatDB("qq"), "qq")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`DROP TABLE qq_people`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sumika_schema SET min_reader = 999`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
	a := e.open()
	t.Cleanup(func() { a.Close() })
	if len(a.people.People) != 0 {
		t.Fatalf("QQ 库没打开，名册却不是空的：%+v", a.people)
	}
}

// W15：ExtraChannels 里的平台库不可用时跳过它的通道，别的渠道照常装。
func TestExtraChannelUnavailableSkipped(t *testing.T) {
	e := newEnv(t)
	makeStoreTooNew(t, e.data, "fakeplat")
	fa := fake.New("fakeplat", types.Caps{})
	opt := e.options()
	opt.ExtraChannels = []ChannelSpec{{
		Channel: "fakeplat", Platform: "fakeplat", Account: "9001", Character: "example", Adapter: fa,
		Admit:       func(types.Inbound) channel.Admission { return channel.Admission{} },
		AdmitNotice: func(types.Notice) channel.Admission { return channel.Admission{} },
		Dispatch:    func(channel.Dispatch) {},
	}}
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatalf("一个虚构平台的库不可用不该让整个后台打不开：%v", err)
	}
	t.Cleanup(func() { a.Close() })
	if _, ok := a.set.Platform("fakeplat"); ok {
		t.Fatal("库不可用的 fakeplat 还在打开的平台里")
	}
	for _, u := range a.channels {
		if u.channel == "fakeplat" {
			t.Fatal("库不可用的 fakeplat 通道却装进了渠道清单")
		}
	}
	if len(a.channels) == 0 {
		t.Fatal("别的渠道被一起停了")
	}
}

// 所有平台的库都不可用：App 打开失败，错误逐个写出每个平台的原因，不是“没有能用的库”。
func TestAllStoresUnavailableErrorNamesEach(t *testing.T) {
	e := newEnv(t)
	makeStoreTooNew(t, e.data, "qq")
	makeStoreTooNew(t, e.data, "local")
	for _, mode := range []Mode{Receive, Archive} {
		opt := e.options()
		opt.Mode = mode
		if mode == Archive {
			opt.Account = ""
		}
		a, err := Open(context.Background(), opt)
		if a != nil {
			a.Close()
		}
		if err == nil {
			t.Fatalf("%s 模式所有库都不可用，Open 却成功了", mode)
		}
		t.Logf("%s 模式两个平台都太新：%v", mode, err)
		for _, want := range []string{"local 的库没打开（too_new）", "qq 的库没打开（too_new）", "请用新版本打开"} {
			if !strings.Contains(err.Error(), want) {
				t.Fatalf("%s 模式的错误少写 %q：%v", mode, want, err)
			}
		}
		if strings.Contains(err.Error(), "没有能用的库") {
			t.Fatalf("%s 模式的错误被盖成“没有能用的库”：%v", mode, err)
		}
	}
}

// 只看历史：qq 太新只停 qq，local 能读，db status 照常返回并写出 qq 的原因。
func TestArchiveTooNewQQKeepsLocalHistory(t *testing.T) {
	e := newEnv(t)
	first := e.open()
	first.Close()
	makeStoreTooNew(t, e.data, "qq")
	opt := e.options()
	opt.Mode, opt.Account = Archive, ""
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatalf("只看历史被一个太新的平台挡住：%v", err)
	}
	t.Cleanup(func() { a.Close() })
	if got := a.set.Platforms(); len(got) != 1 || got[0] != "local" {
		t.Fatalf("归档打开的平台是 %v，要只有 local", got)
	}
	local, ok := a.set.Platform("local")
	if !ok {
		t.Fatal("local 的库没打开")
	}
	var n int
	if err := local.Events.QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil {
		t.Fatalf("local 的历史读不了：%v", err)
	}
	status, err := a.DBStatus(context.Background())
	if err != nil {
		t.Fatalf("只看历史时 db status 报错：%v", err)
	}
	var found bool
	for _, db := range status.Databases {
		if db.Name == "qq" {
			found = true
			if db.Status != "unavailable" || db.Reason != dbguard.ReasonTooNew {
				t.Fatalf("db status 里 qq：%+v", db)
			}
		}
	}
	if !found {
		t.Fatal("db status 里没有 qq")
	}
}
