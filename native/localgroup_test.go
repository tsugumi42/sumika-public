package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/desktopread"
	"sumika-native/internal/localgroup"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// 真的点击表单，真的写临时库，再在列表点开空群；不以假写接口回显证明接线。
func TestLocalGroupCreateUIAndEmptyConversation(t *testing.T) {
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	set, err := store.OpenGo(p, "local", "qq")
	if err != nil {
		t.Fatal(err)
	}
	defer set.Close()
	var chars []api.CharacterView
	for i := range 10 {
		chars = append(chars, api.CharacterView{ID: fmt.Sprintf("c%d", i), Name: fmt.Sprintf("编造角色%d", i)})
	}
	st, _ := set.Platform("local")
	groups := localgroup.New(st, nil, chars, time.Now)
	defer groups.Close()
	svc := desktopread.New(set, chars, nil).WithLocalGroups(groups)
	defer svc.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	posted := make(chan func(), 16)
	a := &app{ctx: ctx, viewCtx: ctx, reader: desktopread.Reader{Service: svc}, keys: map[string]api.ConversationKey{}, postFn: func(f func()) { posted <- f }, caps: []api.Capability{{Name: "local-group", State: api.Ready}}}
	for _, c := range chars {
		a.chars = append(a.chars, characterFromResult(api.Result[api.CharacterView]{State: api.Ready, Value: c}))
	}
	tt := ui.NewTester(a.convList, 480, 560)
	tt.SetScale(1.25)
	tt.SetPreferences(ui.Preferences{ReduceMotion: true})
	tt.Frame()
	click := func(label string) {
		t.Helper()
		if err := tt.Click(label); err != nil {
			t.Fatal(err)
		}
		tt.Frame()
	}
	click("新建群聊")
	if !a.gc.open {
		t.Fatal("建群表单没有打开")
	}
	clickAt(t, tt, "群聊名称输入")
	tt.Type(strings.Repeat("宽", 41))
	tt.Frame()
	click("编造角色0 · c0")
	click("编造角色1 · c1")
	e := &topEnv{a: a, tt: tt, queue: posted}
	click("创建群聊")
	e.drive(t, "非法群名返回", func() bool { return !a.gc.busy })
	tt.Frame()
	if !a.gc.open || a.gc.err == "" || len(a.homes) != 0 {
		t.Fatalf("失败应留在表单且不建半群：%+v", a.gc)
	}
	clickAt(t, tt, "群聊名称输入")
	tt.Key(ui.Cmd, ui.KeyA)
	tt.Type("编造的会客室")
	tt.Frame()
	if a.gc.name != "编造的会客室" {
		t.Fatalf("失败后应能重新编辑群名：%q", a.gc.name)
	}
	click("创建群聊")
	e.drive(t, "建群成功", func() bool { return !a.gc.busy })
	tt.Frame()
	if a.gc.open || a.sel == "" || len(a.homes) != 1 {
		t.Fatalf("建好应打开空群：open=%v sel=%q homes=%d err=%q", a.gc.open, a.sel, len(a.homes), a.gc.err)
	}
	if _, ok := tt.Find("编造的会客室"); !ok {
		t.Fatal("新建空群被列表过滤掉了")
	}
	if !tt.HasText("还没有消息") {
		t.Fatal("空群需要真实空状态")
	}
	gs, err := st.ReadLocalGroups(ctx, []string{"c0", "c1"}, "")
	if err != nil || len(gs) != 1 || gs[0].Name != "编造的会客室" {
		t.Fatalf("UI 没有写出真实群：%+v %v", gs, err)
	}
	e.drive(t, "空群第一页", func() bool { return a.firstDone })
	a.beginGroup()
	a.gc.name = strings.Repeat("宽", 40)
	for _, size := range [][2]int{{480, 560}, {2048, 1030}} {
		tt.SetSize(size[0], size[1])
		tt.Frame()
		for _, label := range []string{"群聊名称", "创建群聊", "关闭建群"} {
			r, ok := tt.Find(label)
			if !ok || r.X < 0 || r.Y < 0 || r.X+r.W > float32(size[0]) || r.Y+r.H > float32(size[1]) {
				t.Fatalf("%dx%d 的 %s 越界：%+v", size[0], size[1], label, r)
			}
			if label == "创建群聊" && (r.W < 24 || r.H < 24) {
				t.Fatalf("点击目标小于 24：%+v", r)
			}
			t.Logf("%dx%d %s bounds=%+v", size[0], size[1], label, r)
		}
	}
	click("关闭建群")
	for range 3 {
		tt.Frame()
	}
	if a.gc.open {
		t.Fatal("关闭没有生效")
	}
	// 占住唯一的库连接，让创建停在事务之前；关闭应取消它，迟到结果不得重开表单。
	tx, err := st.Events.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	a.beginGroup()
	a.gc.name = "关闭时取消"
	a.gc.choices[0].selected = true
	a.gc.choices[1].selected = true
	tt.Frame()
	click("创建群聊")
	click("关闭建群")
	select {
	case f := <-posted:
		f()
	case <-time.After(uiWaitLimit):
		t.Fatal("关闭未取消建群")
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	gs, err = st.ReadLocalGroups(ctx, []string{"c0", "c1"}, "")
	if err != nil || len(gs) != 1 || a.gc.open || len(a.homes) != 1 {
		t.Fatalf("关闭后迟到创建：groups=%v open=%v err=%v", gs, a.gc.open, err)
	}
	if a.convCancel != nil {
		a.convCancel()
	}
	cancel()
}

func TestLocalGroupMessageUsesEachActorIdentity(t *testing.T) {
	t.Parallel()
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "group", Conversation: "console:group:made-up"}
	get := func(id string) (character, bool) {
		name := "名字-" + id
		if id == "b" {
			name = "主人"
		}
		return character{ID: id, Name: name, Color: "#557799"}, true
	}
	for _, id := range []string{"a", "b"} {
		v := api.MessageView{ActorKind: "character", ActorID: id, Origin: "character", DisplayName: "同名", Text: "编造发言"}
		m := msgFromView(key, v, get)
		want := "名字-" + id
		if id == "b" {
			want = "主人"
		}
		if m.actor != id || m.who != want || m.mine || m.owner {
			t.Fatalf("群回复不能被当成 group 角色或主人：%+v", m)
		}
	}
}
