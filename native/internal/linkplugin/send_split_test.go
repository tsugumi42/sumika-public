package linkplugin

// 一条原文按字节拆成几条以后的收尾（规格-链接发送改版 B16）：文字版和直发都按回执一条条算，
// 前面有段没确认时后面被出口中止的条要原样补发，同一条不重发；部分送到按失败算 landed。
// 号、正文都是编的。

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/taskrun"
)

// splitGIF 是一张大动图：体积够大（开晚到回声看守），又不是静态图（没有转 180° 那一级，直接退文字版）
func splitGIF() *types.MediaResource {
	return types.MemoryMedia("g", "image/gif", 720, 405, make([]byte, 5<<20))
}

// textVersionPart 是一条带长正文和媒体的普通消息：退文字版时正文超过 16500 字节，会拆成两条
func textVersionPart() types.Part {
	return types.Part{Kind: types.PartMessage, Body: []types.BodySegment{
		{Kind: types.BodyText, Text: strings.Repeat("字", 6000)}, // 18000 字节
		{Kind: types.BodyGIF, Media: splitGIF()},
	}}
}

// capture 是 taskrun 运行记录和日志的收集器，用来看“文字版：全部/部分/没送到”的标签
type capture struct {
	mu   sync.Mutex
	logs []string
}

func (c *capture) AddTaskRun(taskrun.Row) error { return nil }

func (c *capture) logf(format string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.logs = append(c.logs, fmt.Sprintf(format, args...))
}

func (c *capture) has(sub string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, l := range c.logs {
		if strings.Contains(l, sub) {
			return true
		}
	}
	return false
}

func (f *flow) capture() *capture {
	c := &capture{}
	f.r.p.opt.Degrade = &taskrun.Degrader{Logf: c.logf, Sink: c}
	return c
}

// 一条原文拆成几条的直接发：某条被拒后同组后面的不再补发，结局记部分送到，原因写明第几条被拒、后面几条没发。
// 旧代码把被中止的后面几条当“被连累”原样补发，群里会收到缺了中间一条的后续内容。
func TestDirectSplitMiddleRejectedStopsRest(t *testing.T) {
	t.Parallel()
	g := &types.SplitGroup{}
	parts := make([]types.Part, 3)
	for i, text := range []string{"第一条编的正文", "第二条编的正文", "第三条编的正文"} {
		parts[i] = types.Part{Kind: types.PartMessage, Body: types.TextBody(text), Split: &types.SplitPart{Group: g, Index: i + 1, Total: 3}}
	}
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		switch n {
		case 1:
			return ok("-91001")
		case 2:
			return rejected()
		case 3:
			return echoReply{out: PartOutcome{State: OutcomeAborted}} // 第二条被拒，第三条被出口中止
		}
		t.Fatalf("不该再发第 %d 次", n)
		return ok("x")
	}, nil)
	rep := f.deliver(nil, parts...)
	if s := f.out.sent(); len(s) != 3 || s[0].text != "第一条编的正文" || s[1].text != "第二条编的正文" || s[2].text != "第三条编的正文" {
		t.Fatalf("三条各提交一次，被中止的第三条不补发：%d 帧 %+v", len(s), s)
	}
	if rep.delivered || !rep.partial || !rep.landed || rep.fallback || rep.unsure {
		t.Fatalf("第一条到了、第二条被拒、第三条没发：部分送到：%+v", rep)
	}
	if want := "第 2 条被拒，后面 1 条没发"; !strings.Contains(rep.why, want) {
		t.Fatalf("原因要写 %q：%q", want, rep.why)
	}
}

// 拆条的第一条就不明：后面被出口中止的条照直发路径的规矩原样补发（和文字版同一条收尾规则），
// 不是“被拒”，不记部分送到。
func TestDirectSplitFirstUnknownResendsTail(t *testing.T) {
	t.Parallel()
	g := &types.SplitGroup{}
	texts := []string{"第一条编的正文", "第二条编的正文", "第三条编的正文"}
	parts := make([]types.Part, len(texts))
	for i, text := range texts {
		parts[i] = types.Part{Kind: types.PartMessage, Body: types.TextBody(text), Split: &types.SplitPart{Group: g, Index: i + 1, Total: len(texts)}}
	}
	var f *flow
	f = newFlow(t, func(n int, p sentPart) echoReply {
		switch n {
		case 1:
			return echoReply{out: PartOutcome{State: OutcomeUnknown}} // 第一条没有回执
		case 2, 3:
			return echoReply{out: PartOutcome{State: OutcomeAborted}} // 后面被出口中止
		}
		return okSent(fmt.Sprintf("-911%02d", n), f.clock) // 原样补发的两条
	}, nil)
	rep := f.deliver(nil, parts...)
	s := f.out.sent()
	if len(s) != 5 || s[3].text != texts[1] || s[4].text != texts[2] {
		t.Fatalf("后面两条原样补发一次、第一条不重发：%d 帧 %+v", len(s), s)
	}
	if !rep.delivered || !rep.landed || !rep.unsure || rep.partial || rep.fallback {
		t.Fatalf("第一条不明、补发两条确认：送到但不确定第一条：%+v", rep)
	}
}

// 最后一条被中止、前面都确认：没有被连累的后续条，不补发；不算部分送到。
func TestDirectSplitLastAbortedNoResend(t *testing.T) {
	t.Parallel()
	g := &types.SplitGroup{}
	texts := []string{"第一条编的正文", "第二条编的正文", "第三条编的正文"}
	parts := make([]types.Part, len(texts))
	for i, text := range texts {
		parts[i] = types.Part{Kind: types.PartMessage, Body: types.TextBody(text), Split: &types.SplitPart{Group: g, Index: i + 1, Total: len(texts)}}
	}
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 3 {
			return echoReply{out: PartOutcome{State: OutcomeAborted}}
		}
		return ok(fmt.Sprintf("-912%02d", n))
	}, nil)
	rep := f.deliver(nil, parts...)
	if s := f.out.sent(); len(s) != 3 {
		t.Fatalf("最后一条中止不补发：%d 帧 %+v", len(s), s)
	}
	if !rep.delivered || !rep.landed || rep.partial || rep.unsure {
		t.Fatalf("前两条确认、最后一条中止：按送到算：%+v", rep)
	}
}

// 拆条的直发回执是空的：整批结果不明，不补发、不往拆条组里记被拒。
func TestDirectSplitEmptyReceipt(t *testing.T) {
	t.Parallel()
	g := &types.SplitGroup{}
	parts := make([]types.Part, 2)
	for i, text := range []string{"第一条编的正文", "第二条编的正文"} {
		parts[i] = types.Part{Kind: types.PartMessage, Body: types.TextBody(text), Split: &types.SplitPart{Group: g, Index: i + 1, Total: 2}}
	}
	var f *flow
	f = newFlow(t, func(int, sentPart) echoReply { return ok("-91300") }, nil)
	f.r.p.opt.Expand.Outcomes = func(types.Conversation, string) ([]PartOutcome, error) { return nil, nil }
	rep := f.deliver(nil, parts...)
	if s := f.out.sent(); len(s) != 2 {
		t.Fatalf("回执是空的：不补发：%d 帧 %+v", len(s), s)
	}
	if !rep.delivered || rep.landed || !rep.unsure || rep.partial {
		t.Fatalf("回执空：结果不明、没 landed：%+v", rep)
	}
}

// 文字版拆成三条：中间一条被拒后，最后一条被出口中止也不再补发（和直发拆条同一规则，免得群里缺中间一段）；
// 结局部分送到，确认到的第一条算 landed，原因写明第几条被拒、后面几条没发。
func TestTextFallbackMiddleRejectedStopsRest(t *testing.T) {
	t.Parallel()
	part := types.Part{Kind: types.PartMessage, Body: []types.BodySegment{
		{Kind: types.BodyText, Text: strings.Repeat("字", 14000)}, // 42000 字节，文字版拆三条
		{Kind: types.BodyGIF, Media: types.MemoryMedia("g", "image/gif", 2, 2, []byte{1})},
	}}
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		switch n {
		case 1:
			return rejected() // 原段动图被拒，直接退文字版
		case 2:
			return ok("-92001") // 文字版第一条确认
		case 3:
			return rejected() // 第二条被拒
		case 4:
			return echoReply{out: PartOutcome{State: OutcomeAborted}} // 第三条被出口中止
		}
		return okSent(fmt.Sprintf("-920%02d", n), f.clock) // 补发的第三条
	}, nil)
	cap := f.capture()
	rep := f.deliver(nil, part)
	s := f.out.sent()
	if len(s) != 4 {
		t.Fatalf("中间被拒后，后面被中止的条不再补发（和直发拆条同一规则）：%d 帧 %+v", len(s), s)
	}
	if !rep.fallback || !rep.landed || rep.delivered || !rep.partial || rep.unsure {
		t.Fatalf("一条确认、一条被拒、一条没发：部分送到：%+v", rep)
	}
	if !cap.has("文字版：部分送到") {
		t.Fatal("标签要写部分送到")
	}
	if want := "文字版第 2 条被拒，后面 1 条没发"; !strings.Contains(rep.why, want) {
		t.Fatalf("原因要写 %q：%q", want, rep.why)
	}
}

// 文字版拆成两条、最后一条被中止、前面没有失败：不补发，按没送到的条算失败，结局部分送到。
func TestTextFallbackLastAbortedNoResend(t *testing.T) {
	t.Parallel()
	gif := types.MemoryMedia("g", "image/gif", 2, 2, []byte{1})
	part := types.Part{Kind: types.PartMessage, Body: []types.BodySegment{
		{Kind: types.BodyText, Text: strings.Repeat("字", 6000)}, // 18000 字节，文字版拆两条
		{Kind: types.BodyGIF, Media: gif},
	}}
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		switch n {
		case 1:
			return rejected()
		case 2:
			return ok("-92101")
		case 3:
			return echoReply{out: PartOutcome{State: OutcomeAborted}}
		}
		t.Fatalf("最后一条中止不补发，不该再发第 %d 次", n)
		return ok("x")
	}, nil)
	cap := f.capture()
	rep := f.deliver(nil, part)
	if s := f.out.sent(); len(s) != 3 {
		t.Fatalf("最后一条中止不补发：%d 帧 %+v", len(s), s)
	}
	if !rep.fallback || !rep.landed || rep.delivered || !rep.partial || rep.unsure {
		t.Fatalf("一条确认、一条中止：部分送到：%+v", rep)
	}
	if !cap.has("文字版：部分送到") {
		t.Fatal("标签要写部分送到")
	}
}

// 文字版拆成两条：第一条没确认，第二条被出口中止；第二条原样补发，第一条不重发。
// 旧代码只看 allConfirmed/hasFailure：第二条被中止就算整条失败，既不补发，结局也记成没送到。
func TestTextFallbackResendsAbortedTail(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		switch n {
		case 1:
			return unconfirmed(f.clock) // 原段的大动图没回声
		case 2:
			return unconfirmed(f.clock) // 文字版第一条没确认
		case 3:
			return echoReply{out: PartOutcome{State: OutcomeAborted}} // 第一条没确认，第二条被出口中止
		}
		return okSent(fmt.Sprintf("-901%02d", n), f.clock) // 补发的第二条
	}, nil)
	cap := f.capture()
	rep := f.deliver(nil, textVersionPart())
	s := f.out.sent()
	if len(s) != 4 {
		t.Fatalf("原段、文字版两条、补发的第二条：%d 帧 %+v", len(s), s)
	}
	if s[1].text == "" || s[2].text == "" || s[1].text == s[2].text || s[3].text != s[2].text {
		t.Fatalf("补发的是被中止的第二条，第一条不重发：%+v", s)
	}
	if len(s[1].text) > 16500 || len(s[2].text) > 16500 {
		t.Fatalf("文字版每条都该在上限内：%d/%d", len(s[1].text), len(s[2].text))
	}
	if !rep.fallback || !rep.unsure || !rep.delivered || !rep.landed || rep.partial {
		t.Fatalf("补发出去一条、另一条结果不明：退了文字版、不算失败、至少一段落地：%+v", rep)
	}
	if !cap.has("文字版：部分送到") {
		t.Fatal("有一条确认、一条不明：记部分送到")
	}
}

// 文字版第一条确认、第二条被拒：任一条确认就算 landed，结局部分送到、贴失败表情；
// 确认的那一条要登记成替大图原段补发的那份（逐条登记）。
func TestTextFallbackPartialCountsLandedAndRegistersEach(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		switch n {
		case 1:
			return unconfirmed(f.clock) // 原段的大动图没回声
		case 2:
			return okSent("-90102", f.clock) // 文字版第一条确认
		case 3:
			return rejected() // 第二条被拒
		}
		t.Fatalf("不该再发第 %d 次", n)
		return ok("x")
	}, nil)
	cap := f.capture()
	rep := f.deliver(nil, textVersionPart())
	if s := f.out.sent(); len(s) != 3 || s[1].text == "" || s[2].text == "" {
		t.Fatalf("文字版两条各发一次，被拒的不重发：%d 帧 %+v", len(s), s)
	}
	if !rep.fallback || !rep.landed || rep.delivered || !rep.partial || rep.unsure {
		t.Fatalf("一条确认、一条被拒：部分送到（贴失败表情），不是全失败：%+v", rep)
	}
	if !cap.has("文字版：部分送到") {
		t.Fatal("标签要写部分送到")
	}
	g := f.r.p.guards.get("p1", 0, f.clock)
	if g == nil {
		t.Fatal("大动图没回声，该开着看守")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.repls) != 1 || g.repls[0].ref != (PartRef{PlanID: "p2", Idx: 0, PlatformID: "-90102"}) {
		t.Fatalf("确认的那条文字版要登记成补发份：%+v", g.repls)
	}
}

// 文字版两条全部确认：标签全部送到，两条确认的份逐条登记在看守名下；不回退成失败。
func TestTextFallbackAllConfirmedTagsAndRegistersEach(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return unconfirmed(f.clock)
		}
		return okSent(fmt.Sprintf("-902%02d", n), f.clock)
	}, nil)
	cap := f.capture()
	rep := f.deliver(nil, textVersionPart())
	if s := f.out.sent(); len(s) != 3 {
		t.Fatalf("原段、文字版两条：%d 帧 %+v", len(s), s)
	}
	if !rep.fallback || !rep.landed || !rep.delivered || rep.partial || rep.unsure {
		t.Fatalf("两条都确认：全部送到：%+v", rep)
	}
	if !cap.has("文字版：全部送到") {
		t.Fatal("标签要写全部送到")
	}
	g := f.r.p.guards.get("p1", 0, f.clock)
	if g == nil {
		t.Fatal("大动图没回声，该开着看守")
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.repls) != 2 || g.repls[0].ref != (PartRef{PlanID: "p2", Idx: 0, PlatformID: "-90202"}) || g.repls[1].ref != (PartRef{PlanID: "p2", Idx: 1, PlatformID: "-90203"}) {
		t.Fatalf("两条文字版分别登记：%+v", g.repls)
	}
}

// 文字版一条都没发出去：标签没送到，不 landed。
func TestTextFallbackNoneConfirmed(t *testing.T) {
	t.Parallel()
	gif := types.MemoryMedia("g", "image/gif", 2, 2, []byte{1}) // 小动图：没有看守，被拒后直接退文字版
	var f *flow
	f = newFlow(t, func(n int, _ sentPart) echoReply {
		if n == 1 {
			return rejected()
		}
		return rejected() // 文字版也被拒
	}, nil)
	cap := f.capture()
	rep := f.deliver(nil, types.Part{Kind: types.PartMessage, Body: []types.BodySegment{
		{Kind: types.BodyText, Text: "编的正文"},
		{Kind: types.BodyGIF, Media: gif},
	}})
	if s := f.out.sent(); len(s) != 2 {
		t.Fatalf("原段、文字版各一次，失败的不重发：%d 帧 %+v", len(s), s)
	}
	if !rep.fallback || rep.landed || rep.delivered || rep.partial {
		t.Fatalf("文字版也没发出去：没 landed、失败：%+v", rep)
	}
	if !cap.has("文字版：没送到") {
		t.Fatal("标签要写没送到")
	}
}

// 文字版的回执是空的、或不是终态：按结果不明处理，不当成全部送到，也不记失败、不补发。
func TestTextFallbackEmptyAndNonTerminalReceipt(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		outs func(types.Conversation, string) ([]PartOutcome, error)
	}{
		{"回执是空的", func(types.Conversation, string) ([]PartOutcome, error) { return nil, nil }},
		{"回执不是终态", func(types.Conversation, string) ([]PartOutcome, error) {
			return []PartOutcome{{Index: 0, State: "sending"}}, nil
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gif := types.MemoryMedia("g", "image/gif", 2, 2, []byte{1})
			var f *flow
			f = newFlow(t, func(n int, _ sentPart) echoReply {
				if n == 1 {
					return rejected()
				}
				return ok("mt")
			}, nil)
			base := f.out.outcomes
			f.r.p.opt.Expand.Outcomes = func(conv types.Conversation, planID string) ([]PartOutcome, error) {
				if planID == "p2" { // 只让文字版那一次回执不对
					return tc.outs(conv, planID)
				}
				return base(conv, planID)
			}
			rep := f.deliver(nil, types.Part{Kind: types.PartMessage, Body: []types.BodySegment{
				{Kind: types.BodyText, Text: "编的正文"},
				{Kind: types.BodyGIF, Media: gif},
			}})
			if s := f.out.sent(); len(s) != 2 {
				t.Fatalf("结果不明不补发：%d 帧 %+v", len(s), s)
			}
			if !rep.fallback || !rep.delivered || !rep.unsure || rep.landed || rep.partial {
				t.Fatalf("文字版结果不明：不是失败，也没 landed：%+v", rep)
			}
		})
	}
}

// splitGroupParts 造一组拆条：同一个组标记，pics 里给的下标带一张图。
func splitGroupParts(texts []string, pics map[int]*types.MediaResource) []types.Part {
	g := &types.SplitGroup{}
	out := make([]types.Part, len(texts))
	for i, tx := range texts {
		body := []types.BodySegment{{Kind: types.BodyText, Text: tx}}
		if m := pics[i]; m != nil {
			body = append(body, types.BodySegment{Kind: types.BodyImage, Media: m})
		}
		out[i] = types.Part{Kind: types.PartMessage, Body: body, Split: &types.SplitPart{Group: g, Index: i + 1, Total: len(texts)}}
	}
	return out
}

func sentTexts(f *flow) []string {
	var out []string
	for _, s := range f.out.sent() {
		out = append(out, s.text)
	}
	return out
}

// 两组拆条同一次发：第一组第 2 条被拒，只停第一组后面的；第二组被中止的三条照常补发（组标记不能撞成同一个）。
func TestSplitGroupsDoNotShareStop(t *testing.T) {
	t.Parallel()
	g1 := splitGroupParts([]string{"G1a", "G1b", "G1c"}, nil)
	g2 := splitGroupParts([]string{"G2a", "G2b", "G2c"}, nil)
	if g1[0].Split.Group == g2[0].Split.Group {
		t.Fatal("两组的标记是同一个指针：空结构体做身份会串组")
	}
	f := newFlow(t, func(n int, _ sentPart) echoReply {
		switch n {
		case 1:
			return ok("-1")
		case 2:
			return rejected()
		case 3, 4, 5, 6:
			return echoReply{out: PartOutcome{State: OutcomeAborted}}
		}
		return ok(fmt.Sprintf("-%d", n))
	}, nil)
	rep := f.deliver(nil, append(append([]types.Part{}, g1...), g2...)...)
	got := sentTexts(f)
	if len(got) != 9 || got[6] != "G2a" || got[7] != "G2b" || got[8] != "G2c" {
		t.Fatalf("第二组三条该补发、第一组第三条不该补发：%q", got)
	}
	if want := "第 2 条被拒，后面 1 条没发"; !strings.Contains(rep.why, want) {
		t.Fatalf("原因只算第一组：要 %q，得到 %q", want, rep.why)
	}
}

// 带图的那条被拒、转图重发救回来了：这一段没缺，同组后面的条照常补发，不记部分送到。
func TestSplitImagePieceRescuedKeepsRest(t *testing.T) {
	t.Parallel()
	parts := splitGroupParts([]string{"A1", "A2", "A3"}, map[int]*types.MediaResource{1: picture(t, 3)})
	f := newFlow(t, func(n int, _ sentPart) echoReply {
		switch n {
		case 2:
			return rejected()
		case 3:
			return echoReply{out: PartOutcome{State: OutcomeAborted}}
		}
		return ok(fmt.Sprintf("-%d", n))
	}, nil)
	rep := f.deliver(nil, parts...)
	got := sentTexts(f)
	resent := 0
	for _, tx := range got[3:] {
		if tx == "A3" {
			resent++
		}
	}
	if resent != 1 {
		t.Fatalf("第三条该补发一次：%q", got)
	}
	if rep.partial || strings.Contains(rep.why, "后面") {
		t.Fatalf("救回来了就不是部分送到：%+v", rep)
	}
}
