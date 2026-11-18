package main

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

func TestWorkbenchDrawsMessage(t *testing.T) {
	a := &app{sel: "c", msgs: []*msg{{
		id: "m", who: "访客", text: "隔离的句子", blocks: segview.TextBlocks("隔离的句子"),
	}}}
	a.list = a.newList()
	tt := ui.NewTester(func(c *ui.Context) { a.workbenchChat(c) }, 900, 600)
	tt.Frame()
	if _, ok := tt.Find("隔离的句子"); !ok {
		t.Fatal("message missing")
	}
}

func TestWorkbenchSwitchKeepsVisibleMessage(t *testing.T) {
	a := &app{}
	for i := range 120 {
		a.msgs = append(a.msgs, &msg{id: fmt.Sprintf("m%d", i), text: "字", blocks: segview.TextBlocks("字")})
	}
	a.list = a.newList()
	request := false
	tt := ui.NewTester(func(c *ui.Context) {
		if request {
			request = false
			a.switchMainView(c, "workbench")
		}
		a.list.Key = func(i int) any { return a.msgs[i].id }
		ui.List(c, &a.list, len(a.msgs), func(i int) {
			row := ui.Box(c.Key(a.msgs[i].id)).FillWidth().Height(32)
			if a.workbenchEnabled() {
				row.Height(44)
			}
			row.Children(func() {
				a.workbenchAnchor(c, i, row)
				ui.Text(c, a.msgs[i].id)
			})
		}).Fill()
	}, 400, 300)
	tt.Frame()
	a.list.ScrollTo(45, ui.Start)
	tt.Frame()
	tt.Frame()
	first, _ := a.list.Visible()
	want := a.msgs[first].id
	request = true
	tt.Frame()
	tt.Frame()
	tt.Frame()
	first, last := a.list.Visible()
	if first > last || a.msgs[first].id != want {
		t.Fatalf("visible row after switch: want %s, got %d..%d id %s", want, first, last, a.msgs[first].id)
	}
}
