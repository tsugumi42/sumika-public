package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/segview"
)

func serviceRowApp() *app {
	a := shellApp()
	a.sel, a.title, a.group, a.rail = "qq:group:90002", "编造的群", true, 1
	say := func(id, text string, at int64) *msg {
		return &msg{id: id, who: "虚构甲", actor: "200", text: text, blocks: segview.TextBlocks(text), at: at}
	}
	a.msgs = []*msg{
		say("m1", "第一句", 60000),
		{id: "s1", service: "虚构乙 加入了群聊", at: 61000},
		say("m2", "第二句", 62000),
		{id: "s2", service: "虚构丙 被禁言 10 分钟", at: 63000},
		{id: "s3", service: "这是一条很长很长的通知，用来看它超过一行的时候会不会换行、还居中，而不是把整个消息列撑开或者被截断在屏幕外面", at: 64000},
	}
	a.list = a.newList()
	return a
}

// 通知条是居中的一行，没有头像和名字；不把前后两句连成一组。
func TestServiceRowDrawsCenteredWithoutBubble(t *testing.T) {
	a := serviceRowApp()
	tt := shellTester(a, 2048, 1030)
	row, ok := tt.Find("通知：虚构乙 加入了群聊")
	if !ok {
		t.Fatalf("没有通知条：%q", tt.Texts())
	}
	text := boxInside(t, tt, row, "虚构乙 加入了群聊")
	if d := (text.X + text.W/2) - (row.X + row.W/2); d < -1 || d > 1 {
		t.Fatalf("通知条没居中：文字 %+v，行 %+v", text, row)
	}
	long := "这是一条很长很长的通知，用来看它超过一行的时候会不会换行、还居中，而不是把整个消息列撑开或者被截断在屏幕外面"
	longRow, ok := tt.Find("通知：" + long)
	if !ok {
		t.Fatal("没有长通知条")
	}
	if longText := boxInside(t, tt, longRow, long); longText.X < longRow.X-1 || longText.X+longText.W > longRow.X+longRow.W+1 {
		t.Fatalf("长通知条越出了消息列：%+v，行 %+v", longText, longRow)
	}
	first, _ := tt.Find("消息：m1")
	second, _ := tt.Find("消息：m2")
	if second.Y-first.Y-first.H < 2*a.chatTokens().GapGroups {
		t.Fatalf("前后两句不该隔着通知条连在一起：%+v %+v", first, second)
	}
	if dir := os.Getenv("LOOK_SHOT"); dir != "" {
		f, err := os.Create(filepath.Join(dir, "通知条.png"))
		if err != nil {
			t.Fatal(err)
		}
		png.Encode(f, tt.Image())
		f.Close()
	}
}

func boxInside(t *testing.T, tt *ui.Tester, row ui.Rect, text string) ui.Rect {
	t.Helper()
	r, ok := tt.Find(text)
	if !ok {
		t.Fatalf("没有文字 %q", text)
	}
	return r
}

// 显示规则：有通知条文字的通知要显示，别的通知照旧不显示。
func TestShowsAsRowIncludesService(t *testing.T) {
	t.Parallel()
	cases := []struct {
		v    api.MessageView
		want bool
	}{
		{api.MessageView{Kind: "message"}, true},
		{api.MessageView{Kind: "notice", ActorKind: "system"}, true},
		{api.MessageView{Kind: "notice", Service: "虚构乙 加入了群聊"}, true},
		{api.MessageView{Kind: "notice", ActorKind: "human"}, false},
	}
	for _, c := range cases {
		if got := showsAsRow(c.v); got != c.want {
			t.Errorf("%+v：%v，应为 %v", c.v, got, c.want)
		}
	}
}
