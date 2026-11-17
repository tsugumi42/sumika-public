package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/config"
	"sumika-native/internal/uitest"
)

// newWorkbenchModelHarness 是新界面（外壳）里的模型页；旧界面的整窗数字框另有 newModelHarness。
func newWorkbenchModelHarness(t *testing.T) *modelHarness {
	t.Helper()
	f := &modelFake{demoModelSettings: newDemoModelSettings([]character{{ID: "test-a", Name: "编造角色"}})}
	a := shellApp()
	a.rail, a.realTab, a.demoMode = 4, tabModels, false
	a.set = nil
	a.chars = []character{{ID: "test-a", Name: "编造角色"}}
	a.models.writer = f
	h := &modelHarness{a: a, fake: f, q: make(chan func(), 32)}
	a.postFn = func(fn func()) { h.q <- fn }
	h.tt = shellTester(a, 2048, 1030)
	h.tt.SetSize(2048, 6000) // 整页不滚动，下面的行都在画面里
	h.tt.Frame()
	h.wait(t)
	h.tt.Frame()
	return h
}

func (h *modelHarness) numberKey(f config.ModelField) string { return fmt.Sprint("model-number-", f) }

// typeInto 点进某个数字框的输入、全选、打字、回车。输入框和行名同名，所以按键找位置。
func (h *modelHarness) typeInto(t *testing.T, key, text string) {
	t.Helper()
	b := boxOf(t, h.tt, key+"-input").Box
	h.tt.ClickAt(b.X+b.W/2, b.Y+b.H/2)
	h.tt.Frame()
	h.tt.Key(ui.Cmd, ui.KeyA)
	h.tt.Type(text)
	h.tt.Key(0, ui.KeyEnter)
	h.tt.Frame()
}

// 上下文那五项数字：点一下加或减就存，一步到位，不弹窗；写的是原来的配置键和类型。
func TestWorkbenchModelNumbersSaveInline(t *testing.T) {
	for _, tc := range []struct {
		label, button string
		field         config.ModelField
		want          any // int 或 float64
	}{
		{"上下文长度上限", "加", config.ContextWindow, 1000}, // 0 = 跟模型，加一步先到下限 1000
		{"留给回复和工具往返", "减", config.ContextReserve, 15384},
		{"压缩后保留的原文", "加", config.ContextKeepTokens, 25000},
		{"开始压缩的比例", "加", config.ContextSoftRatio, 0.75},
		{"强制压缩的比例", "减", config.ContextHardRatio, 0.8},
	} {
		t.Run(string(tc.field), func(t *testing.T) {
			h := newWorkbenchModelHarness(t)
			h.click(t, tc.label+" "+tc.button)
			h.wait(t)
			if len(h.fake.calls) != 1 {
				t.Fatalf("应该只存一次：%+v", h.fake.calls)
			}
			ch := h.fake.calls[0].change
			var got any
			switch {
			case ch.Int != nil:
				got = *ch.Int
			case ch.Float != nil:
				got = *ch.Float
			}
			if ch.Field != tc.field || got != tc.want {
				t.Fatalf("存的是 %v=%v，应是 %v=%v", ch.Field, got, tc.field, tc.want)
			}
			if h.a.models.numberBox.Open {
				t.Fatal("不该再弹数字框")
			}
		})
	}
}

// 打字也是改完即存；不合法的不写入，原因写在行下面；退回原值。
func TestWorkbenchModelNumbersRejectInvalid(t *testing.T) {
	h := newWorkbenchModelHarness(t)
	h.typeInto(t, h.numberKey(config.ContextKeepTokens), "999")
	h.tt.Frame()
	if len(h.fake.calls) != 0 || !h.tt.HasText("最少保留 1000 token") {
		t.Fatalf("太小的保留原文该被拒：调用 %d，画面 %q", len(h.fake.calls), h.tt.Texts())
	}
	h.typeInto(t, h.numberKey(config.ContextKeepTokens), "30000")
	h.wait(t)
	if len(h.fake.calls) != 1 || *h.fake.calls[0].change.Int != 30000 {
		t.Fatalf("合法值该存：%+v", h.fake.calls)
	}
	if h.tt.HasText("最少保留 1000 token") {
		t.Fatalf("存成功后拒绝说明该消失：%q", h.tt.Texts())
	}
}

// 软比例不能超过硬比例：加到顶那一下被拒，不写入，行内说明；硬比例往下减到软比例时同理。
func TestWorkbenchModelRatiosStayOrdered(t *testing.T) {
	h := newWorkbenchModelHarness(t)
	for range 3 { // 0.70 → 0.75 → 0.80 → 0.85，和强制压缩的 0.85 并齐
		h.click(t, "开始压缩的比例 加")
		h.wait(t)
	}
	if got := h.a.models.view.Configured.Context.SoftRatio; got != 0.85 || len(h.fake.calls) != 3 {
		t.Fatalf("软比例 %v，调用 %d 次", got, len(h.fake.calls))
	}
	h.click(t, "开始压缩的比例 加")
	if len(h.fake.calls) != 3 || !h.tt.HasText("开始压缩的比例不能大于强制压缩的比例") {
		t.Fatalf("超过硬比例该被拒并说明：调用 %d，画面 %q", len(h.fake.calls), h.tt.Texts())
	}
	h.click(t, "强制压缩的比例 减")
	if len(h.fake.calls) != 3 || h.a.models.view.Configured.Context.HardRatio != 0.85 {
		t.Fatalf("硬比例减到软比例以下该被拒：调用 %d", len(h.fake.calls))
	}
	// 硬比例往上加是合法的，同时清掉过时的说明
	h.click(t, "强制压缩的比例 加")
	h.wait(t)
	if h.a.models.view.Configured.Context.HardRatio != 0.9 || h.tt.HasText("不能大于强制压缩") {
		t.Fatalf("硬比例 %v；画面 %q", h.a.models.view.Configured.Context.HardRatio, h.tt.Texts())
	}
}

// 后台一次只收一个写：保存在途时加减钮灰掉，连点第二下不会被悄悄丢掉也不会重复写。
func TestWorkbenchModelNumbersDisabledWhileSaving(t *testing.T) {
	h := newWorkbenchModelHarness(t)
	h.fake.gate = make(chan struct{})
	h.click(t, "压缩后保留的原文 加")
	h.click(t, "压缩后保留的原文 加")
	if !h.a.models.busy {
		t.Fatal("第一下该在保存中")
	}
	close(h.fake.gate)
	h.wait(t)
	if len(h.fake.calls) != 1 {
		t.Fatalf("保存在途时第二下该被挡住：%+v", h.fake.calls)
	}
	h.click(t, "压缩后保留的原文 加") // 保存完又能点
	h.wait(t)
	if len(h.fake.calls) != 2 || *h.fake.calls[1].change.Int != 30000 {
		t.Fatalf("保存完该能接着加：%+v", h.fake.calls)
	}
}

// 各模型的窗口和输出覆盖：平时折叠，点开是两个加减行，加一步就存成手填；有手填才有“恢复”，恢复写 0。
func TestWorkbenchModelLimitsInline(t *testing.T) {
	h := newWorkbenchModelHarness(t)
	if h.tt.HasText("单次输出上限") {
		t.Fatalf("平时该折叠：%q", h.tt.Texts())
	}
	h.click(t, "demo/model-00")
	if !h.tt.HasText("单次输出上限") || !h.tt.HasText("上下文窗口") {
		t.Fatalf("点开后该有两个加减行：%q", h.tt.Texts())
	}
	h.click(t, "demo/model-00 上下文窗口 加")
	h.wait(t)
	ch := h.fake.calls[0].change
	if ch.Field != config.ModelContextOverride || ch.Model != "demo/model-00" || ch.Int == nil || *ch.Int != 138000 {
		t.Fatalf("加一步该把 128K 写成手填 138000：%+v", ch)
	}
	if !h.tt.HasText("手填的") || !hasKey(h.tt, "model-limit-demo/model-00-"+string(config.ModelContextOverride)+"-reset") {
		t.Fatalf("存完该出现“恢复”：%q", h.tt.Texts())
	}
	b := boxOf(t, h.tt, "model-limit-demo/model-00-"+string(config.ModelContextOverride)+"-reset").Box
	h.tt.ClickAt(b.X+b.W/2, b.Y+b.H/2)
	h.tt.Frame()
	h.wait(t)
	ch = h.fake.calls[1].change
	if ch.Field != config.ModelContextOverride || ch.Int == nil || *ch.Int != 0 {
		t.Fatalf("恢复该写 0 去掉覆盖：%+v", ch)
	}
	if hasKey(h.tt, "model-limit-demo/model-00-"+string(config.ModelContextOverride)+"-reset") {
		t.Fatal("恢复后不该再有恢复钮")
	}
	// 窗口太小：不写入，行内说明
	h.typeInto(t, "model-limit-demo/model-00-"+string(config.ModelContextOverride), "999")
	if len(h.fake.calls) != 2 || !h.tt.HasText("窗口至少 1000") {
		t.Fatalf("999 该被拒：调用 %d，画面 %q", len(h.fake.calls), h.tt.Texts())
	}
}

// 模型页的加减行满足几何规矩，并且和页上别的行同高。
func TestWorkbenchModelNumberRowsGeometry(t *testing.T) {
	h := newWorkbenchModelHarness(t)
	h.click(t, "demo/model-00")
	root := uitest.Tree(h.tt)
	k := h.a.workbench.shell.kit
	var pairs []uitest.Pair
	for _, f := range []config.ModelField{config.ContextWindow, config.ContextReserve, config.ContextKeepTokens, config.ContextSoftRatio, config.ContextHardRatio} {
		row := root.Find(h.numberKey(f) + "-row")
		if row == nil {
			t.Fatalf("没有 %s 的行", f)
		}
		uitest.CheckTree(t, row, string(f), k.T.RInnerMin, &pairs)
		uitest.Equal(t, string(f), "行高", row.Box.H, k.G.RowHeight)
	}
	if body := root.Find("model-limit-body-demo/model-00"); body == nil {
		t.Fatal("展开后没有模型的加减行")
	} else {
		uitest.CheckTree(t, body, "模型覆盖", k.T.RInnerMin, &pairs)
		if page := root.Find("settings-page"); page == nil || body.Box.X+body.Box.W > page.Box.X+page.Box.W+1 {
			t.Fatalf("缩进后的加减行不能超出设置页：%+v 页 %+v", body.Box, page)
		}
	}
	if dir := os.Getenv("LOOK_SHOT"); dir != "" {
		f, err := os.Create(filepath.Join(dir, "模型页-加减.png"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		// 只截上下文那一节和展开的模型：整页缩小了看不清
		img := h.tt.Image().SubImage(image.Rect(980, 780, 2000, 1480))
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
	}
	if len(pairs) == 0 {
		t.Fatal("一块都没查到，检查走空了")
	}
}
