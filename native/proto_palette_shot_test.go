package main

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/tsugumi42/MewTypeUI/theme"

	"sumika-native/internal/llm/llmtest"
)

// 设 PROTO_SHOT=目录 时，用和 cmd/sumika-proto 的 TestShotPalette 同一段对话，把新界面的聊天消息区浅色、深色各截一张；平时跳过。
// 为了和原型同字号比颜色和圆角，截图时把消息字号设成 16（原型默认；正式程序的默认字号归字体设置那条线管）。
func TestShotPaletteWorkbench(t *testing.T) {
	dir := os.Getenv("PROTO_SHOT")
	if dir == "" {
		t.Skip("设 PROTO_SHOT 才出图")
	}
	for _, dark := range []bool{false, true} {
		h := wbUI(t, llmtest.Script{Reply: llmtest.Text("哼，早什么早，都快十二点了哦。")}, nil)
		if dark {
			h.a.look.settings.Mode = "night"
		}
		h.tt.Frame()
		h.a.changeLook(func(d *theme.Definition) {
			size := float32(16)
			v := d.Typography["message"]
			v.Size = &size
			d.Typography["message"] = v
		})
		st := h.a.localRun("console:char00")
		for i, text := range []string{"早", "中午好"} {
			h.wbSend(t, text)
			h.until(t, "回复落入历史", func() bool { return !st.busy && len(h.a.msgs) >= 2*(i+1) })
		}
		h.tt.Frame()
		h.tt.Frame()
		first, ok := h.tt.Find("气泡")
		composer, ok2 := h.tt.Find("发一条消息")
		if !ok || !ok2 {
			t.Fatal("找不到气泡或输入框")
		}
		const k = 1.25
		img := h.tt.Image()
		r := image.Rect(int(376*k), int((first.Y-48)*k), img.Bounds().Dx(), int((composer.Y-8)*k)).Intersect(img.Bounds())
		name := map[bool]string{false: "palette-workbench-light.png", true: "palette-workbench-dark.png"}[dark]
		f, _ := os.Create(filepath.Join(dir, name))
		png.Encode(f, img.SubImage(r))
		f.Close()
		t.Logf("%s：%v", name, r)
	}
}
