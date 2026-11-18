package chatui

import (
	"testing"
)

// 2026-11-06 晚上两族视觉验收确认、修掉的问题，每条放回旧代码都会红。

// 思考进行中的三个点放在图标格子里，不压后面的"正在想"。
func TestThinkDotsFit(t *testing.T) {
	for _, s := range []Settings{DefaultSettings(), {Text: TextMin, Radius: RadiusMin, Density: DensityMin}, {Text: TextMax, Radius: RadiusMax, Density: DensityMax}} {
		h := Host{T: Derive(s), Column: 720}
		root := tree(processTester(h, []*Step{{ID: "t", Kind: StepThink, State: StepRunning, Thought: "想"}}, map[string]*bool{}))
		dots, lead, text := root.find("dots"), root.find("lead"), root.find("text")
		if dots == nil || lead == nil || text == nil {
			t.Fatal("找不到三个点、图标格子或文字")
		}
		if dots.Box.X < lead.Box.X-0.5 || dots.Box.X+dots.Box.W > lead.Box.X+lead.Box.W+0.5 || dots.Box.X+dots.Box.W > text.Box.X {
			t.Errorf("外观 %v/%v/%v：三个点 %v 越出图标格子 %v、或压到字 %v", s.Text, s.Radius, s.Density, dots.Box, lead.Box, text.Box)
		}
	}
}

// "进行中展开过程"开着时，进行中默认展开；点一下能收起来（不是怎么点都开着）。
func TestLiveOpenCollapsible(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 720, LiveOpen: true}
	open := map[string]*bool{}
	tt := processTester(h, []*Step{{ID: "t", Kind: StepThink, State: StepRunning, Thought: "边想边写的正文"}}, open)
	if !tt.HasText("边想边写的正文") {
		t.Fatal("开着设置时进行中应该默认展开")
	}
	*open["t"] = true // 等于点了一下
	tt.Frame()
	tt.Frame()
	if tt.HasText("边想边写的正文") {
		t.Error("点了以后还收不起来")
	}
}
