package main

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"unsafe"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/segview"
)

func TestScaleHistoryAnchor(t *testing.T) {
	const n = 10000
	names := []string{"甲", "乙", "丙", "丁", "戊", "己", "庚", "辛", "壬", "癸"}
	msgs := make([]*msg, n)
	for i := range n {
		text := fmt.Sprintf("历史 %s %d", names[i%len(names)], i)
		if i == 5000 {
			text = "锚点句子-5000"
		}
		msgs[i] = &msg{id: fmt.Sprintf("m%d", i), who: names[i%len(names)], text: text, blocks: segview.TextBlocks(text), at: int64(i) * 1000}
	}
	run := func(name string, view func(*app, *ui.Context)) {
		t.Helper()
		a := &app{sel: "c", group: true, title: "测试群", msgs: append([]*msg(nil), msgs...)}
		a.list = a.newList()
		tt := ui.NewTester(func(c *ui.Context) { view(a, c) }, 900, 640)
		tt.SetScale(1.25)
		tt.Frame()
		elements := countElements(t, tt)
		a.list.ScrollTo(5000, ui.Start)
		tt.Frame()
		tt.Frame()
		first, last := a.list.Visible()
		if first < 4980 || first > 5020 {
			t.Fatalf("%s landed at %d", name, first)
		}
		built := 0
		if last >= first {
			built = last - first + 1
		}
		before, ok := tt.Find("锚点句子-5000")
		if !ok {
			t.Fatalf("%s anchor text missing", name)
		}
		anchor := a.msgs[first].id
		older := make([]*msg, 30)
		for i := range older {
			text := fmt.Sprintf("更早 %d", i)
			older[i] = &msg{id: fmt.Sprintf("old%d", i), who: names[0], text: text, blocks: segview.TextBlocks(text), at: int64(i)}
		}
		a.msgs = append(older, a.msgs...)
		tt.Frame()
		tt.Frame()
		first, _ = a.list.Visible()
		if first < 0 || first >= len(a.msgs) || a.msgs[first].id != anchor {
			got := ""
			if first >= 0 && first < len(a.msgs) {
				got = a.msgs[first].id
			}
			t.Fatalf("%s prepend moved anchor %s -> %s at %d", name, anchor, got, first)
		}
		after, ok := tt.Find("锚点句子-5000")
		jump := 0.0
		if !ok {
			t.Logf("%s anchor left the viewport", name)
			jump = -1
		} else {
			jump = float64(after.Y - before.Y)
			if name == "workbench" && (jump < -1 || jump > 1) {
				t.Fatalf("%s anchor jumped %.2f DIP", name, jump)
			}
		}
		cache := 0
		if s := a.is(); s != nil {
			cache = s.bytes
		}
		t.Logf("%s elements=%d built=%d jump=%.2f cache=%d", name, elements, built, jump, cache)
		if elements > 4000 {
			t.Fatalf("%s built too much of the history: %d", name, elements)
		}
		if dir := os.Getenv("SUMIKA_S7_SHOT"); dir != "" && name == "workbench" {
			saveShot(t, tt, filepath.Join(dir, "s7-scroll.png"))
			narrow := ui.NewTester(func(c *ui.Context) {
				a.applyLook(c)
				a.workbenchView(c, a.look.dark)
			}, 480, 640)
			narrow.SetScale(1.25)
			narrow.Frame()
			saveShot(t, narrow, filepath.Join(dir, "s7-narrow.png"))
		}
	}
	run("workbench", func(a *app, c *ui.Context) {
		a.applyLook(c)
		a.workbenchChat(c)
	})
	run("legacy", func(a *app, c *ui.Context) {
		a.applyLook(c)
		a.chatView(c)
	})
}

func saveShot(t *testing.T, tt *ui.Tester, path string) {
	t.Helper()
	img := tt.Image()
	if img == nil {
		t.Fatal("no image")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		t.Fatal(err)
	}
	b := img.Bounds()
	t.Logf("shot %s %dx%d", path, b.Dx(), b.Dy())
}

func countElements(t *testing.T, tt *ui.Tester) int {
	t.Helper()
	rt := reflect.ValueOf(tt).Elem().FieldByName("rt")
	if !rt.IsValid() || rt.IsNil() {
		t.Fatal("tester runtime missing")
	}
	access := rt.Elem().FieldByName("access")
	reflect.NewAt(access.Type(), unsafe.Pointer(access.UnsafeAddr())).Elem().SetBool(true)
	tt.Frame()
	return int(rt.Elem().FieldByName("accessNodes").Int())
}
