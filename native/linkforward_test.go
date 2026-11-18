package main

import (
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sumika-native/internal/linktemplate"
)

// 链接解析里的“合并转发”分组：展开、插入占位符、画成卡片的预览、错误写在框下、保存写进配置、恢复默认。演示设置，不碰真实配置。
func TestForwardGroupEditSaveAndReset(t *testing.T) {
	p := newPlatformHarness(t, 2048, 1030, false)
	p.openLinks(t)
	p.click(t, "节点名和卡片文字")
	for _, n := range []string{"节点名（留空用她的 QQ 名字）", "卡片标题", "预览第一行", "预览第二行", "底部摘要", "插入占位符", "$标题", "$字数", "$图片数", "$UP主", "$站点", "保存合并转发"} {
		if _, ok := p.tt.Find(n); !ok {
			t.Fatalf("合并转发分组缺 %s：%v", n, p.tt.Texts())
		}
	}
	// 没填：预览是 QQ 默认
	if !p.tt.HasText("群聊的聊天记录") || !p.tt.HasText("查看3条转发消息") {
		t.Fatalf("没填时预览应是 QQ 默认：%v", p.tt.Texts())
	}
	f := &p.a.linkSettings.fwd
	// 点“$标题”插进选中的那一项（默认标题）
	p.click(t, "$标题")
	if f.text[0] != "$标题" || !f.dirty {
		t.Fatalf("占位符没插进标题：%+v", f.text)
	}
	f.name = "纸鸢"
	f.text[1], f.text[2], f.text[3], f.text[4] = "$字数", "$图片数", "$UP主", "$标题"
	p.tt.Frame()
	p.tt.Frame()
	for _, want := range []string{"纸船试航", "共 1200 字", "3 张图", "纸灯实验室"} {
		if !p.tt.HasText(want) {
			t.Fatalf("预览缺 %q：%v", want, p.tt.Texts())
		}
	}
	// 写错：错误写在框下，不能保存
	f.text[3] = "$分段"
	p.tt.Frame()
	p.tt.Frame()
	if !p.tt.HasText("不认识 $分段") {
		t.Fatalf("错误没写在框下：%v", p.tt.Texts())
	}
	writes := p.a.linkSettings.writer.(*linkPageFake).writes
	p.click(t, "保存合并转发")
	if p.a.linkSettings.writer.(*linkPageFake).writes != writes {
		t.Fatal("有错时不该保存")
	}
	f.text[3] = "$UP主"
	p.tt.Frame()
	p.click(t, "保存合并转发")
	p.pump(t)
	got := p.expand().Forward
	if got.NodeName != "纸鸢" || got.Source != "$标题" || !slices.Equal(got.News, []string{"$字数", "$图片数"}) || got.Summary != "$UP主" || got.Prompt != "$标题" {
		t.Fatalf("没存对：%+v", got)
	}
	if f.dirty {
		t.Fatal("保存后草稿应不再是改动中")
	}
	// 恢复默认
	p.click(t, "恢复 QQ 默认")
	p.pump(t)
	if g := p.expand().Forward; g.NodeName != "" || g.Source != "" || len(g.News) != 0 || g.Summary != "" || g.Prompt != "" {
		t.Fatalf("没清掉：%+v", g)
	}
	if f.name != "" || f.text != [5]string{} {
		t.Fatalf("草稿没清：%+v", f)
	}
}

func TestForwardNodeNameValidationShownUnderBox(t *testing.T) {
	t.Parallel()
	var f linkForwardState
	f.name = strings.Repeat("长", 41)
	errs, bad := f.errors()
	if !bad || !strings.Contains(errs[0], "最多 40 个字") {
		t.Fatalf("节点名太长应报错：%v", errs)
	}
	f.name = "好"
	f.text[2] = "[$标题"
	errs, bad = f.errors()
	if !bad || errs[0] != "" || errs[3] == "" {
		t.Fatalf("预览第二行写错应只在它下面报：%v", errs)
	}
}

func TestForwardPreviewFallsBackAndClips(t *testing.T) {
	t.Parallel()
	if _, ok := forwardPreview("", 16); ok {
		t.Fatal("空的是默认")
	}
	if _, ok := forwardPreview("$UP主", 16); !ok {
		t.Fatal("示例数据有作者")
	}
	long := strings.Repeat("长", 30)
	if s, _ := forwardPreview(long, linktemplate.CardSourceLimit); len([]rune(s)) > linktemplate.CardSourceLimit {
		t.Fatalf("预览没按同一个上限截：%q", s)
	}
}

// 截图只给用户看：卡片区域，深浅两套
func TestForwardGroupScreenshots(t *testing.T) {
	dir := os.Getenv("FORWARD_SHOT_DIR")
	if dir == "" {
		t.Skip("设 FORWARD_SHOT_DIR 才截图")
	}
	for _, dark := range []bool{false, true} {
		p := newPlatformHarness(t, 2048, 1030, dark)
		p.openLinks(t)
		p.click(t, "节点名和卡片文字")
		f := &p.a.linkSettings.fwd
		f.name = "纸鸢"
		f.text = [5]string{"$标题", "$字数", "$图片数", "$UP主", "$标题"}
		f.dirty = true
		p.tt.Frame()
		p.tt.Frame()
		p.click(t, "预览（示例数据）")
		img := p.tt.Image()
		name := "forward-group-light.png"
		if dark {
			name = "forward-group-dark.png"
		}
		file, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		_ = png.Encode(file, img)
		file.Close()
	}
}

// 回归：直接在文本框里打字（不点占位符按钮）也算改动，不能被配置里的旧值每帧盖回去；点保存后、设置刷新前草稿也不闪回
func TestForwardTypingSurvivesFrames(t *testing.T) {
	p := newPlatformHarness(t, 2048, 1030, false)
	p.openLinks(t)
	p.click(t, "节点名和卡片文字")
	f := &p.a.linkSettings.fwd
	r, ok := p.tt.Find("卡片标题") // 框上方的小标题；文本框就在它下面
	if !ok {
		t.Fatalf("找不到卡片标题：%v", p.tt.Texts())
	}
	for i := 0; i < 10 && r.Y+r.H+60 > 1030; i++ { // 页面上方多一行时文本框会落到屏幕外，先滚进来
		p.tt.Scroll(1000, 500, 0, 300)
		p.tt.Frame()
		r, _ = p.tt.Find("卡片标题")
	}
	p.tt.ClickAt(r.X+40, r.Y+r.H+24)
	p.tt.Frame()
	p.tt.Type("$标题")
	for range 3 {
		p.tt.Frame()
	}
	if f.text[0] != "$标题" || !f.dirty {
		t.Fatalf("打的字被盖回去了：text=%q dirty=%v", f.text[0], f.dirty)
	}
	p.click(t, "保存合并转发")
	for range 3 {
		p.tt.Frame() // 设置还没刷新的几帧
	}
	if f.text[0] != "$标题" {
		t.Fatalf("保存后草稿闪回旧值：%q", f.text[0])
	}
}
