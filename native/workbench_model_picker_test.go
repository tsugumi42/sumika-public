package main

import (
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 新界面的模型页：选模型是一行里的下拉，不再弹整窗列表；开关是新外壳的标准开关。
func TestWorkbenchModelPage(t *testing.T) {
	f := &modelFake{demoModelSettings: newDemoModelSettings([]character{{ID: "test-a", Name: "编造角色"}})}
	q := make(chan func(), 32)
	a := shellApp()
	a.rail, a.realTab, a.demoMode = 4, tabModels, false
	a.set = nil
	a.chars = []character{{ID: "test-a", Name: "编造角色"}}
	a.models.writer = f
	a.postFn = func(fn func()) { q <- fn }
	tt := shellTester(a, 2048, 1030)
	remoteWait(t, "模型设置读到并画出来", func() bool {
		for {
			select {
			case fn := <-q:
				fn()
				continue
			default:
			}
			break
		}
		tt.Frame()
		return a.models.loaded && !a.models.busy
	})
	if len(keysLike(tt, "-switch")) == 0 {
		t.Fatalf("模型页没有用标准开关：%v", keysLike(tt, "setting"))
	}
	if len(keysLike(tt, "model-picker-")) == 0 {
		t.Fatalf("模型页没有下拉：%q", tt.Texts())
	}
	if dir := os.Getenv("LOOK_SHOT"); dir != "" {
		save := func(name string) {
			f, err := os.Create(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			if err := png.Encode(f, tt.Image()); err != nil {
				t.Fatal(err)
			}
		}
		save("模型页.png")
		trigger := boxOf(t, tt, keysLike(tt, "model-picker-")[0]).Box
		tt.ClickAt(trigger.X+trigger.W-20, trigger.Y+trigger.H/2)
		tt.Frame()
		tt.Frame()
		save("模型页-下拉.png")
	}
}

func keysLike(tt *ui.Tester, part string) []string {
	var out []string
	for _, b := range uiBoxes(tt) {
		if containsStr(b.Key, part) {
			out = append(out, b.Key)
		}
	}
	return out
}

func containsStr(s, part string) bool { return strings.Contains(s, part) }
