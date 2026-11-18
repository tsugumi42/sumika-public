package main

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/chatui"
	"sumika-native/internal/segview"
	"sumika-native/internal/winshell"
)

// uiBox 是从 Tester 已提交的元素树里读出的一个元素（mygo 的 Tester 没有公开遍历节点，
// 按固定版本的字段读，和 chatui 的 tree_test.go 同一做法；升级 mygo 后读不到字段会 panic，要跟着改）。
type uiBox struct {
	Key, Label, Text string
	Box              ui.Rect
}

func uiBoxes(tt *ui.Tester) []uiBox {
	root := reflect.ValueOf(tt).Elem().FieldByName("rt").Elem().FieldByName("c").FieldByName("root")
	var out []uiBox
	var walk func(reflect.Value)
	walk = func(p reflect.Value) {
		v := p.Elem()
		f := func(n string) float32 { return float32(v.FieldByName(n).Float()) }
		b := uiBox{Text: v.FieldByName("text").String(), Label: v.FieldByName("label").String(), Box: ui.Rect{X: f("x"), Y: f("y"), W: f("w"), H: f("h")}}
		if k := v.FieldByName("key"); !k.IsNil() && k.Elem().Kind() == reflect.String {
			b.Key = k.Elem().String()
		}
		out = append(out, b)
		for ch := v.FieldByName("first"); !ch.IsNil(); ch = ch.Elem().FieldByName("next") {
			walk(ch)
		}
	}
	walk(root)
	return out
}

func mdApp(texts ...string) *app {
	a := &app{sel: "c", group: false, title: "测试", recalled: map[string]bool{}, speakers: map[string]bool{}}
	for i, text := range texts {
		a.msgs = append(a.msgs, &msg{id: fmt.Sprintf("r%d", i), who: "晴枝", actor: "char1", md: true, text: text, blocks: segview.TextBlocks(text), at: int64(i+1) * 60000})
	}
	a.list = a.newList()
	return a
}

const mdSample = "先看这个：\n\n```go\nfunc main() { fmt.Println(\"这一行特别特别长，长到一定会超过气泡的宽度，所以它必须折行而不是撑破气泡\") }\n```\n\n要做的：\n\n- 第一件\n- 第二件\n\n用 `go test ./...` 跑，详情见[文档](https://example.com/doc)。"

// 角色回复走 Markdown：代码块、列表、链接、行内代码都画成结构，没有源码标记；长代码行不撑破气泡。
func TestWorkbenchMarkdownRenders(t *testing.T) {
	for _, width := range []int{900, 360} {
		a := mdApp(mdSample)
		tt := ui.NewTester(func(c *ui.Context) { a.applyLook(c); a.workbenchChat(c) }, width, 700)
		tt.SetScale(1.25)
		tt.Frame()
		tt.Frame()
		if a.chatMD == nil || a.chatMD.Len() == 0 {
			t.Fatalf("宽 %d：Markdown 状态没建起来，正文还是纯文字", width)
		}
		for _, want := range []string{"第一件", "第二件", "文档", "go test"} {
			if !tt.HasText(want) {
				t.Errorf("宽 %d：缺 %q，文字 %q", width, want, tt.Texts())
			}
		}
		if _, ok := tt.Find("复制代码"); !ok {
			t.Errorf("宽 %d：代码块没有复制按钮", width)
		}
		for _, s := range tt.Texts() {
			if strings.Contains(s, "```") || strings.Contains(s, "](") || strings.Contains(s, "- 第一件") || strings.Contains(s, "`go test") {
				t.Errorf("宽 %d：画出了源码标记 %q", width, s)
			}
		}
		boxes := uiBoxes(tt)
		var bubble, code *uiBox
		for i := range boxes {
			switch {
			case boxes[i].Label == "气泡" && bubble == nil:
				bubble = &boxes[i]
			case boxes[i].Key == "code" && code == nil:
				code = &boxes[i]
			}
		}
		if bubble == nil || code == nil {
			t.Fatalf("宽 %d：找不到气泡或代码块", width)
		}
		const slack = 0.75
		if code.Box.X < bubble.Box.X-slack || code.Box.X+code.Box.W > bubble.Box.X+bubble.Box.W+slack {
			t.Errorf("宽 %d：代码块 %+v 越出气泡 %+v", width, code.Box, bubble.Box)
		}
		if bubble.Box.X+bubble.Box.W > float32(width)+slack {
			t.Errorf("宽 %d：气泡 %+v 越出窗口", width, bubble.Box)
		}
		t.Logf("宽 %d：气泡 %+v 代码块 %+v", width, bubble.Box, code.Box)
	}
}

// 她的回复和主人、群友的话不混：非 Markdown 的消息（群友）里的星号和反引号照原文画。
func TestWorkbenchMarkdownOnlyForCharacter(t *testing.T) {
	a := mdApp()
	text := "**不是粗体** `不是代码`"
	a.msgs = []*msg{{id: "u1", who: "群友", actor: "user1", text: text, blocks: segview.TextBlocks(text), at: 60000}}
	tt := ui.NewTester(func(c *ui.Context) { a.applyLook(c); a.workbenchChat(c) }, 900, 500)
	tt.SetScale(1.25)
	tt.Frame()
	tt.Frame()
	if !tt.HasText("**不是粗体**") && !tt.HasText("`不是代码`") {
		t.Errorf("群友的话被当 Markdown 解析了：%q", tt.Texts())
	}
}

// 流式追加：同一条消息一字一字长出来（包括没闭合的代码块、列表、半截行内代码），气泡高度只增不减，
// 相邻两步的增量不超过一行加一个代码块的内距，不出现先塌后涨的闪动。
func TestWorkbenchMarkdownStreamingMonotonic(t *testing.T) {
	full := "好的，我来写：\n\n```go\nx := 1\ny := `两个`\nfmt.Println(x, y)\n```\n\n- 先 **加粗** 再说\n- 第二项带 `code`\n\n结尾[链接](https://example.com)。"
	runes := []rune(full)
	a := mdApp(string(runes[:1]))
	m := a.msgs[0]
	m.streaming = true
	tt := ui.NewTester(func(c *ui.Context) { a.applyLook(c); a.workbenchChat(c) }, 900, 900)
	tt.SetScale(1.25)
	height := func() float32 {
		tt.Frame()
		tt.Advance(40 * time.Millisecond) // 出字按时间推进，不推时间就停在第一个字
		tt.Frame()
		for _, b := range uiBoxes(tt) {
			if b.Label == "气泡" {
				return b.Box.H
			}
		}
		t.Fatalf("没有气泡：%q", tt.Texts())
		return 0
	}
	prev := height()
	worst := float32(0)
	for n := 2; n <= len(runes); n++ {
		m.text = string(runes[:n])
		m.blocks = segview.TextBlocks(m.text)
		h := height()
		if h < prev-0.5 {
			t.Fatalf("追加到 %q 时气泡变矮：%.1f -> %.1f", string(runes[:n]), prev, h)
		}
		if d := h - prev; d > worst {
			worst = d
		}
		if d := h - prev; d > 90 {
			t.Fatalf("追加到 %q 时气泡一次长高 %.1f", string(runes[:n]), d)
		}
		prev = h
	}
	m.streaming = false
	tt.Advance(5 * time.Second)
	final := height()
	if final < prev-0.5 {
		t.Fatalf("流式结束后变矮：%.1f -> %.1f", prev, final)
	}
	if final < 150 {
		t.Fatalf("字出全后气泡只有 %.1f 高，Markdown 没有画出代码块和列表", final)
	}
	t.Logf("流式末了 %.1f，出全后 %.1f，单步最大增量 %.1f", prev, final, worst)
}

func luma(c ui.Color) float64 {
	lin := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(c.R) + 0.7152*lin(c.G) + 0.0722*lin(c.B)
}

// 设计语言第三节：聊天底是第 0 层（bg），入站气泡是第 1 层（surface），浅色、深色里两者都不能是同一个颜色，
// 气泡都比底亮；你的气泡用角色色浅阶，也不和底相同。令牌来自外壳的原型色板（winshell.Kit），不再经旧主题翻译。
func TestChatBackgroundLayers(t *testing.T) {
	t.Parallel()
	for _, dark := range []bool{false, true} {
		s := chatui.DefaultSettings()
		s.Dark = dark
		tok := winshell.NewKit(s, "").T
		self := tok.AccentOf(chatui.DefaultChar).Soft
		if tok.C.ChatBg == tok.C.Bubble {
			t.Errorf("dark=%v：聊天底和入站气泡同色 %v", dark, tok.C.ChatBg)
		}
		if tok.C.ChatBg == self {
			t.Errorf("dark=%v：聊天底和你的气泡同色 %v", dark, tok.C.ChatBg)
		}
		if luma(tok.C.Bubble) <= luma(tok.C.ChatBg) {
			t.Errorf("dark=%v：入站气泡 %v 不比聊天底 %v 亮", dark, tok.C.Bubble, tok.C.ChatBg)
		}
		t.Logf("dark=%v 聊天底 %v 入站 %v 你的 %v，亮度 %.4f / %.4f", dark, tok.C.ChatBg, tok.C.Bubble, self, luma(tok.C.ChatBg), luma(tok.C.Bubble))
	}
}

// 画出来也要分得开：聊天区空处的像素（带背景图案的光晕）不等于入站气泡里的像素，浅色、深色都一样。
// 深浅层次的明暗顺序按原型色板，不再套旧主题时“气泡必须比底亮”的断言：有光晕的底在深色里比气泡亮是原型的画法。
func TestWorkbenchBubbleSeparatedFromBackground(t *testing.T) {
	for _, dark := range []bool{false, true} {
		a := mdApp()
		text := "一句普通的话"
		a.msgs = []*msg{{id: "u1", who: "群友", actor: "user1", text: text, blocks: segview.TextBlocks(text), at: 60000}}
		tt := ui.NewTester(func(c *ui.Context) { a.applyLook(c); a.workbenchChat(c) }, 900, 500)
		tt.SetScale(1.25)
		tt.SetDark(dark)
		tt.Frame()
		tt.Frame()
		img := tt.Image()
		at := func(x, y float32) ui.Color {
			p := img.RGBAAt(int(x*1.25), int(y*1.25))
			return ui.Color{R: p.R, G: p.G, B: p.B, A: p.A}
		}
		var bubble *uiBox
		for _, b := range uiBoxes(tt) {
			if b.Label == "气泡" {
				b := b
				bubble = &b
				break
			}
		}
		if bubble == nil {
			t.Fatalf("dark=%v：没有气泡", dark)
		}
		bg := at(800, 300) // 气泡在顶上（消息少时从标题栏下面排起），这一点在气泡右边的空处
		in := at(bubble.Box.X+3, bubble.Box.Y+bubble.Box.H/2)
		if bg == in {
			t.Errorf("dark=%v：聊天底像素和气泡像素相同 %v", dark, bg)
		}
		if !dark {
			// 浅色里入站气泡有 shadow-1：紧贴下沿外一格，比同一列再往下 8 DIP 的纯底暗
			x := bubble.Box.X + bubble.Box.W/2
			below, far := at(x, bubble.Box.Y+bubble.Box.H+1), at(x, bubble.Box.Y+bubble.Box.H+8)
			if luma(below) >= luma(far) {
				t.Errorf("浅色：气泡下沿外没有阴影，%v 不比再往下的底 %v 暗", below, far)
			}
		}
		t.Logf("dark=%v 聊天底 %v 气泡 %v 气泡框 %+v", dark, bg, in, bubble.Box)
	}
}

// SUMIKA_S8_SHOT=<目录> 时，浅色、深色各截一张聊天组件区（消息列，不是整页）：她的 Markdown 回复、群友的话、你的话。
func TestWorkbenchShotLayers(t *testing.T) {
	dir := os.Getenv("SUMIKA_S8_SHOT")
	if dir == "" {
		t.Skip("SUMIKA_S8_SHOT 没设")
	}
	for _, dark := range []bool{false, true} {
		a := mdApp(mdSample)
		a.msgs[0].at = 1_000_000
		plain := func(id, who, actor string, mine bool, text string, at int64) *msg {
			return &msg{id: id, who: who, actor: actor, mine: mine, text: text, blocks: segview.TextBlocks(text), at: at}
		}
		a.msgs = append([]*msg{plain("u0", "群友", "user1", false, "早，今天的进度怎么样", 900_000)}, a.msgs...)
		a.msgs = append(a.msgs, plain("me1", "我", "owner", true, "收到，我等下看一下代码块折行有没有问题", 1_100_000))
		tt := ui.NewTester(func(c *ui.Context) { a.applyLook(c); a.workbenchChat(c) }, 520, 760)
		tt.SetScale(1.25)
		tt.SetDark(dark)
		tt.Frame()
		tt.Advance(3 * time.Second)
		tt.Frame()
		name := "light"
		if dark {
			name = "dark"
		}
		saveShot(t, tt, filepath.Join(dir, "chat-"+name+".png"))
	}
}
