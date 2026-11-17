package turn

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/audience"
)

// 自动召回的过程步骤（主人 2026-11-09）：显示的就是这一轮注入给模型的那份结构化结果，不是另外再算的。

func recallPlugin(items []RecallItem, lines ...string) fakePlugin {
	return contextPlugin("recall", func(context.Context, ContextIn) (*Contribution, error) {
		if lines == nil {
			return nil, nil
		}
		return &Contribution{Lines: lines, Recall: items}, nil
	})
}

// localWithProgress 发一句本地的话，收集界面收到的每一次过程快照。
func (h *harness) localWithProgress(text string) (*Result, [][]ProcessStep) {
	h.t.Helper()
	var mu sync.Mutex
	var got [][]ProcessStep
	in := consoleInput(h.c.CharacterID, 0, text).WithProgress(nil, func(s []ProcessStep) {
		mu.Lock()
		defer mu.Unlock()
		got = append(got, append([]ProcessStep(nil), s...))
	})
	r := wait(h.t, h.hub.send(in))
	mu.Lock()
	defer mu.Unlock()
	return r, got
}

func (h *harness) recallRows() (n int, args string, attached bool) {
	h.t.Helper()
	var seq *int64
	var a *string
	rows, err := h.st.Trace.Query(`SELECT args_json, event_seq FROM steps WHERE kind = 'recall'`)
	if err != nil {
		h.t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		n++
		if err := rows.Scan(&a, &seq); err != nil {
			h.t.Fatal(err)
		}
	}
	if a != nil {
		args = *a
	}
	return n, args, seq != nil
}

func TestRecallStepLocalShowsInjected(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	items := []RecallItem{
		{Text: "要一直遵守：不在群里提私事", Kind: "boundary", Pinned: true, Source: "主人确认", CreatedAt: 1_700_000_000_000},
		{Text: "测试猫叫云朵", Kind: "fact", Source: "原话", CreatedAt: 1_700_000_100_000, Score: 0.031, Rerank: 0.82, Sim: 0.71, Vector: 2, Lexical: 1, Previous: []RecallPrior{{Text: "测试猫叫团子", CreatedAt: 1_699_000_000_000}}},
	}
	h.use(recallPlugin(items, "可能相关的记忆：", "- 测试猫叫云朵"))
	_, steps := h.localWithProgress("你好")

	// 模型收到的参考块里有它
	if !strings.Contains(h.body(0), "测试猫叫云朵") {
		t.Fatalf("模型没收到召回的那一行：%s", h.body(0))
	}
	// 界面收到的是同一份：一步 recall，不是 tool，明细原样
	var rec []ProcessStep
	for _, snap := range steps {
		for _, s := range snap {
			if s.Kind == "recall" {
				rec = append(rec[:0], s)
			}
			if s.Kind == "tool" {
				t.Errorf("自动召回被当成了工具：%+v", s)
			}
		}
	}
	if len(rec) != 1 || rec[0].State != "done" || rec[0].Tool != "" {
		t.Fatalf("界面没收到一步 recall：%+v", steps)
	}
	if got := rec[0].Recall; len(got) != 2 || !reflect.DeepEqual(got, items) {
		t.Fatalf("界面看到的和注入的不是同一份：%+v", got)
	}
	// 存进 trace 并挂到回复上：重启后读得回来，明细一致
	n, args, attached := h.recallRows()
	if n != 1 || !attached {
		t.Fatalf("trace 里应有 1 行 recall 且挂到回复上：n=%d attached=%v", n, attached)
	}
	var back []RecallItem
	if err := json.Unmarshal([]byte(args), &back); err != nil || !reflect.DeepEqual(back, items) {
		t.Fatalf("存下的明细读不回来：%v %s", err, args)
	}
}

// 没有召回（插件什么都没给）、召回为空（给了行但没有结构化明细）都不出这一步，也不存库。
func TestRecallStepAbsentWhenNothingRecalled(t *testing.T) {
	t.Parallel()
	for name, p := range map[string]fakePlugin{
		"无召回":  recallPlugin(nil),
		"召回为空": recallPlugin(nil, "别的插件那样的一行"),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, false, nil)
			h.use(p)
			_, steps := h.localWithProgress("你好")
			for _, snap := range steps {
				for _, s := range snap {
					if s.Kind == "recall" {
						t.Fatalf("不该有 recall 步：%+v", s)
					}
				}
			}
			if n, _, _ := h.recallRows(); n != 0 {
				t.Fatalf("不该存 recall 行：%d", n)
			}
		})
	}
}

// 渠道回合（QQ）不存召回明细：记忆内容只在用户本机窗口看。
func TestRecallStepNotStoredForChannel(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	h.use(recallPlugin([]RecallItem{{Text: "测试猫叫云朵", Kind: "fact"}}, "- 测试猫叫云朵"))
	h.say("qq:group:g1", "human", "p1", "你好", false, "g")
	if !strings.Contains(h.body(0), "测试猫叫云朵") {
		t.Fatal("夹具没让召回插件跑起来，这条测试什么都没证明")
	}
	if n, _, _ := h.recallRows(); n != 0 {
		t.Fatalf("QQ 回合不该存召回明细：%d", n)
	}
}

// 取消后只接纳无 Lines 的输入贡献：它没有召回，不能冒出一步。
func TestAcceptContextRecallOnlyFromInjected(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, nil)
	h.use(recallPlugin([]RecallItem{{Text: "甲", Kind: "fact"}}, "- 甲"))
	h.use(contextPlugin("input", func(context.Context, ContextIn) (*Contribution, error) {
		return &Contribution{Recall: []RecallItem{{Text: "不该出现"}}}, nil // 没有 Lines：没注入任何字
	}))
	pending := h.hub.plugins.contextContributions(context.Background(), ContextIn{}, func(string) map[string]any { return map[string]any{} })
	var got []RecallItem
	h.hub.plugins.acceptContext(context.Background(), pending, func(a acceptedContext) { got = a.Recall })
	if len(got) != 1 || got[0].Text != "甲" {
		t.Fatalf("只有真注入了字的贡献才算召回：%+v", got)
	}
}

// 本地群聊和私聊同走 console 频道（localgroup 拼的听众），都会存召回；QQ 不会。
func TestRecallStepScenes(t *testing.T) {
	t.Parallel()
	for name, c := range map[string]struct {
		a    audience.Audience
		want bool
	}{
		"本地私聊":  {audience.Console("c"), true},
		"本地群聊":  {audience.Audience{Channel: "console", Conversation: "g", Scene: "group", PrivateOK: true}, true},
		"QQ 私聊": {audience.Audience{Channel: "qq", Scene: "private"}, false},
		"QQ 群":  {audience.Audience{Channel: "qq", Scene: "group"}, false},
	} {
		if got := (Input{Audience: c.a}).local(); got != c.want {
			t.Errorf("%s：local()=%v，应为 %v", name, got, c.want)
		}
	}
}
