package chatui

import (
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"
)

// 自动召回行：不是工具调用，单独一种；显示这一轮注入的每条记忆和真有的分数。

func recallItems() []RecallItem {
	return []RecallItem{
		{Text: "不在群里提主人的私事", Kind: "boundary", Pinned: true, Source: "主人确认", At: 1_760_000_000_000},
		{Text: "编造人物甲最近在练虚构乐器", Kind: "fact", Source: "原话", At: 1_760_100_000_000, Score: 0.031, Rerank: 0.82, Sim: 0.71, Vector: 2, Lexical: 1},
		{Text: "下周要搬到编造城", Kind: "episode", Source: "群友转述", At: 1_760_200_000_000, Hearsay: true, Score: 0.016, Lexical: 3},
	}
}

func recallHost(open map[string]*bool, s Settings) Host {
	return Host{T: Derive(s), Column: 720, NoEnter: true, StepOpen: func(id string) *bool {
		if open[id] == nil {
			open[id] = new(bool)
		}
		return open[id]
	}}
}

func clickSummary(t *testing.T, tt *ui.Tester) {
	t.Helper()
	if err := tt.Click("过程摘要"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
}

func TestSummarizeRecallIsNotATool(t *testing.T) {
	rec := &Step{ID: "r", Kind: StepRecall, State: StepDone, Recall: recallItems()}
	if s := Summarize([]*Step{rec}); s.Text != "召回 3 条记忆" || s.Icon != "bookmark" || s.Running {
		t.Errorf("只有召回：%+v", s)
	}
	one := &Step{ID: "t1", Kind: StepTool, State: StepDone, Tool: "todo_write"}
	if s := Summarize([]*Step{rec, one}); s.Text != "召回 3 条记忆 · 调用了 todo_write" || s.Icon != "tool" {
		t.Errorf("召回加一个工具，调用数不含召回：%+v", s)
	}
	two := &Step{ID: "t2", Kind: StepTool, State: StepDone, Tool: "task"}
	if s := Summarize([]*Step{rec, one, two}); s.Text != "召回 3 条记忆 · 调用了 2 个工具" {
		t.Errorf("两个工具不该因为召回变成 3 个：%+v", s)
	}
	think := &Step{ID: "k", Kind: StepThink, State: StepDone, Seconds: 4}
	if s := Summarize([]*Step{rec, think}); s.Text != "召回 3 条记忆 · 想了 4 秒" || s.Icon != "brain" {
		t.Errorf("召回加思考：%+v", s)
	}
}

func TestRecallCardShowsItemsAndOnlyRealScores(t *testing.T) {
	h := recallHost(map[string]*bool{}, DefaultSettings())
	m := procMsg("p", 0, &Step{ID: "r", Kind: StepRecall, State: StepDone, Recall: recallItems()})
	tt := msgsTester(h, []*Msg{m}, 900)
	if all := strings.Join(tt.Texts(), "\n"); strings.Contains(all, "虚构乐器") || !strings.Contains(all, "召回 3 条记忆") {
		t.Fatalf("收起时只有摘要：%q", all)
	}
	clickSummary(t, tt) // 只有这一段：外层展开直接是明细，不再折第二层
	all := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{"不在群里提主人的私事", "编造人物甲最近在练虚构乐器", "下周要搬到编造城", "固定要点 · ", "事实 · ", "待确认", "主人确认", "群友转述", "重排 0.82", "向量 0.71 第 2 名", "词法第 1 名", "综合 0.031", "词法第 3 名", recallTime(1_760_100_000_000)} {
		if !strings.Contains(all, want) {
			t.Errorf("展开后缺「%s」：\n%s", want, all)
		}
	}
	// 没有的不编：固定要点没有任何分数；只有词法的那条没有重排、向量
	for _, line := range tt.Texts() {
		if strings.Contains(line, "固定要点") && (strings.Contains(line, "重排") || strings.Contains(line, "综合") || strings.Contains(line, "向量")) {
			t.Errorf("固定要点不该有分数：%q", line)
		}
		if strings.Contains(line, "词法第 3 名") && (strings.Contains(line, "重排") || strings.Contains(line, "向量")) {
			t.Errorf("没配重排、向量没中的不该写分：%q", line)
		}
	}
	// 能点开的一行是"召回"，工具的才写"调用了"
	if strings.Contains(all, "调用了") {
		t.Errorf("自动召回被写成了工具调用：%s", all)
	}
}

func TestRecallLongItemFoldsAndListScrolls(t *testing.T) {
	long := strings.Repeat("很长的一条记忆正文。", 40)
	items := []RecallItem{{Text: long, Kind: "fact", Source: "原话"}}
	for i := 0; i < 30; i++ {
		items = append(items, RecallItem{Text: fmt.Sprintf("第 %d 条", i), Kind: "fact", Source: "原话"})
	}
	open := map[string]*bool{}
	h := recallHost(open, DefaultSettings())
	tt := msgsTester(h, []*Msg{procMsg("p", 0, &Step{ID: "r", Kind: StepRecall, State: StepDone, Recall: items})}, 900)
	clickSummary(t, tt)
	if all := strings.Join(tt.Texts(), "\n"); strings.Contains(all, long) || !strings.Contains(all, "展开全文") {
		t.Fatalf("太长的单条应该先折起来：%d 条文字", len(tt.Texts()))
	}
	sc := tree(tt).find("recall-scroll")
	if sc == nil {
		t.Fatal("没有滚动区")
	}
	if max := recallMaxLines * h.rowLine(); sc.Box.H > max+1 {
		t.Errorf("整块高 %.1f，超过上限 %.1f", sc.Box.H, max)
	}
	if err := tt.Click("展开全文"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	tt.Frame()
	if !strings.Contains(strings.Join(tt.Texts(), "\n"), long) || !tt.HasText("收起") {
		t.Error("点了展开全文没看到全文")
	}
}

// 模型主动调的记忆工具仍是工具卡：展开能看到它返回的内容（和自动召回分开）。
func TestActiveMemoryToolCardShowsReturn(t *testing.T) {
	h := Host{T: Derive(DefaultSettings()), Column: 720}
	yes := true
	steps := []*Step{{ID: "s", Kind: StepSearch, State: StepDone, Tool: "memory_search", Target: "虚构乐器", Output: "找到 1 条：编造人物甲在练虚构乐器"}}
	tt := processTester(h, steps, map[string]*bool{"s": &yes})
	if !tt.HasText("找到 1 条：编造人物甲在练虚构乐器") {
		t.Fatalf("展开的 memory_search 看不到返回的内容：%v", tt.Texts())
	}
	steps[0].Output = ""
	tt = processTester(h, steps, map[string]*bool{"s": &yes})
	if tt.HasText("找到 1 条") {
		t.Error("没有返回内容却显示了")
	}
}

// LOOK_SHOT：浅色、深色各出一张，给人看。
func TestRecallLookShot(t *testing.T) {
	dir := os.Getenv("LOOK_SHOT")
	if dir == "" {
		t.Skip("设 LOOK_SHOT 才出图")
	}
	for name, s := range map[string]Settings{"浅色": DefaultSettings(), "深色": func() Settings { v := DefaultSettings(); v.Dark = true; return v }()} {
		open := map[string]*bool{}
		h := recallHost(open, s)
		items := append(recallItems(), RecallItem{Text: strings.Repeat("这是一条比较长的记忆，用来看单条折叠的样子。", 6), Kind: "fact", Source: "原话", At: 1_760_300_000_000, Score: 0.012, Vector: 5, Sim: 0.58})
		m := procMsg("p", 0, &Step{ID: "r", Kind: StepRecall, State: StepDone, Recall: items}, &Step{ID: "k", Kind: StepThink, State: StepDone, Seconds: 3, Thought: "想想怎么回"})
		m2 := procMsg("q", 5, &Step{ID: "r2", Kind: StepRecall, State: StepDone, Recall: items[:2]})
		tt := ui.NewTester(func(c *ui.Context) {
			ui.Column(c.Key("root")).Width(720).Padding(10).Gap(8).Background(h.T.C.ChatBg).Children(func() {
				h.Message(c, m, nil, nil)
				h.Message(c, m2, nil, nil)
			})
		}, 760, 700)
		tt.Frame()
		tt.Frame()
		*h.StepOpen("p"), *h.StepOpen("r") = true, true // 第一条展开（外层和召回行都点开），第二条收起
		tt.Frame()
		tt.Frame()
		f, err := os.Create(filepath.Join(dir, "召回行-"+name+".png"))
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, tt.Image()); err != nil {
			t.Fatal(err)
		}
		f.Close()
	}
}

func TestRecallCardShowsPrevious(t *testing.T) {
	h := recallHost(map[string]*bool{}, DefaultSettings())
	its := recallItems()
	its[1].Previous = []RecallPrior{{Text: "旧的住址甲", At: 1_759_000_000_000}, {Text: "旧的住址乙", At: 1_758_000_000_000}}
	m := procMsg("p", 0, &Step{ID: "r", Kind: StepRecall, State: StepDone, Recall: its})
	tt := msgsTester(h, []*Msg{m}, 900)
	clickSummary(t, tt)
	all := strings.Join(tt.Texts(), "\n")
	for _, want := range []string{"取代了：", "旧的住址甲", "旧的住址乙", recallTime(1_759_000_000_000)} {
		if !strings.Contains(all, want) {
			t.Errorf("展开后缺「%s」：\n%s", want, all)
		}
	}
	if n := strings.Count(all, "取代了："); n != 1 {
		t.Errorf("只有带旧版本的那条写“取代了”，实际 %d 处", n)
	}
}
