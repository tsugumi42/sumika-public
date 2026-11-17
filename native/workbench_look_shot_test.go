package main

import (
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"sumika-native/internal/segview"
)

// TestShotLook 出整窗图给人看（设 LOOK_SHOT 才出）：本地私聊、QQ 群聊，深浅各一张。
func TestShotLook(t *testing.T) {
	dir := os.Getenv("LOOK_SHOT")
	if dir == "" {
		t.Skip("设 LOOK_SHOT 才出图")
	}
	names := []string{"白羽", "夜莺", "晴枝", "青禾", "小满", "阿澄", "听雨", "南星"}
	for _, dark := range []bool{false, true} {
		for _, scene := range []string{"本地私聊", "QQ"} {
			a := shellApp()
			a.local = nil
			a.chars = nil
			for i, n := range names {
				id := string(rune('a' + i))
				a.local = append(a.local, conv{Key: demoKey("local:" + id), Kind: "local", Title: n, Last: lastLine{Text: "今晚的月色真好，出来走走吗", CreatedAt: t0}, Observers: []string{id}})
				a.chars = append(a.chars, character{ID: id, Name: n, Model: "demo/model-00"})
			}
			a.sel, a.title = "local:a", names[0]
			a.msgs = []*msg{
				{id: "m1", who: names[0], text: "今晚的月色真好", blocks: segview.TextBlocks("今晚的月色真好"), at: 60000},
				{id: "m2", mine: true, text: "那出去走走？", blocks: segview.TextBlocks("那出去走走？"), at: 70000},
				{id: "m3", who: names[0], text: "好呀，我去拿外套。", blocks: segview.TextBlocks("好呀，我去拿外套。"), at: 80000},
			}
			a.workbench.shell.st.Panes.SideOpen = true
			tt := shellTester(a, 2048, 1030)
			if dark {
				a.look.settings.Mode = "night" // 第一帧 initLook 会读设置，要在它后面改
				tt.Frame()
				tt.Frame()
			}
			if scene == "QQ" {
				if err := tt.Click("QQ"); err != nil {
					t.Fatal(err)
				}
				tt.Frame()
				tt.Frame()
			}
			tt.Frame()
			name := scene + map[bool]string{false: "-浅.png", true: "-深.png"}[dark]
			f, err := os.Create(filepath.Join(dir, name))
			if err != nil {
				t.Fatal(err)
			}
			png.Encode(f, tt.Image())
			f.Close()
		}
	}
}
