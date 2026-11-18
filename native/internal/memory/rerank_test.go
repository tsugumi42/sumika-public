package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/embed"
)

// fakeRerank 按测试给的函数回：记下查的什么、给了哪些正文、调了几次
type fakeRerank struct {
	fn    func(ctx context.Context, query string, docs []string) ([]int, []float64, error)
	calls int
	query string
	docs  []string
}

func (f *fakeRerank) Rerank(ctx context.Context, query string, docs []string) ([]int, []float64, error) {
	f.calls++
	f.query = query
	f.docs = append([]string(nil), docs...)
	return f.fn(ctx, query, docs)
}

// reversed 是「倒过来」的重排：分数从大到小顺着新顺序给
func reversed(_ context.Context, _ string, docs []string) ([]int, []float64, error) {
	n := len(docs)
	order := make([]int, n)
	scores := make([]float64, n)
	for i := range order {
		order[i] = n - 1 - i
		scores[i] = float64(n - i)
	}
	return order, scores, nil
}

// rerankDB：群 A 里五条都带「吉他」（都进候选），另加群 B 一条、主人的一条（都看得见别的场合、这个群里看不见）
func rerankDB(t *testing.T) string {
	t.Helper()
	return bunDb(t, [][]string{
		{"1", "fact", "active", "吉他的事一", "qq:1", "conv:" + GA},
		{"2", "fact", "active", "吉他的事二", "qq:1", "conv:" + GA},
		{"3", "fact", "active", "吉他的事三", "qq:1", "conv:" + GA},
		{"4", "fact", "active", "吉他的事四", "qq:1", "conv:" + GA},
		{"5", "fact", "active", "吉他的事五", "qq:1", "conv:" + GA},
		{"6", "fact", "active", "群B的吉他事", "qq:1", "conv:" + GB},
		{"7", "fact", "active", "主人的吉他事", "owner", "owner"},
	}, nil)
}

// openForRerank 打开一份记忆；logf 收日志（nil 就是不打）
func openForRerank(t *testing.T, opt Options) *Memory {
	t.Helper()
	m, err := Open(context.Background(), rerankDB(t), nil, nil, opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	return m
}

func rerankQuery() Query { return Query{Text: "吉他", Mode: ModeExplicit} }

// 不配重排：和以前一字不差（两次结果一样），Recalled 里也没有重排的痕迹
func TestRerankNilKeepsOrder(t *testing.T) {
	t.Parallel()
	m := openForRerank(t, Options{})
	ctx := context.Background()
	a := m.Recall(ctx, inGroup(GA), rerankQuery())
	b := m.Recall(ctx, inGroup(GA), rerankQuery())
	if got, want := fmt.Sprint(refs(a.Candidates)), fmt.Sprint(refs(b.Candidates)); got != want {
		t.Fatalf("不配重排时两次召回顺序该一样：%s / %s", got, want)
	}
	if got, want := fmt.Sprint(refs(a.Hits)), fmt.Sprint(refs(b.Hits)); got != want {
		t.Fatalf("不配重排时两次给出来的该一样：%s / %s", got, want)
	}
	if a.Reranked || a.RerankSkipped != "" {
		t.Fatalf("没配重排不该说重排了：%v %q", a.Reranked, a.RerankSkipped)
	}
	if len(a.Candidates) < 3 {
		t.Fatalf("这条题要有几条候选才量得出顺序：%v", refs(a.Candidates))
	}
}

// 重排把顺序倒过来：候选和给出来的都跟着倒；只改顺序，条目一条不多一条不少
func TestRerankReversesOrder(t *testing.T) {
	t.Parallel()
	base := openForRerank(t, Options{}).Recall(context.Background(), inGroup(GA), rerankQuery())
	f := &fakeRerank{fn: reversed}
	m := openForRerank(t, Options{Reranker: f})
	r := m.Recall(context.Background(), inGroup(GA), rerankQuery())
	if !r.Reranked || r.RerankSkipped != "" {
		t.Fatalf("这一轮该用上重排：%v %q", r.Reranked, r.RerankSkipped)
	}
	if f.calls != 1 || f.query != "吉他" {
		t.Fatalf("重排该被调一次、查的是这一句：%d 次 %q", f.calls, f.query)
	}
	want := append([]string(nil), refs(base.Candidates)...)
	for i, j := 0, len(want)-1; i < j; i, j = i+1, j-1 {
		want[i], want[j] = want[j], want[i]
	}
	if got := refs(r.Candidates); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("候选没跟着倒：得到 %v，要 %v", got, want)
	}
	if got, n := refs(r.Hits), len(refs(base.Hits)); len(got) != n {
		t.Fatalf("重排不该加加减减条目：%v（原来 %d 条）", got, n)
	}
	if len(r.Hits) > 1 && r.Hits[0].Ref != want[0] {
		t.Fatalf("给出来的第一条该是新顺序的第一条：%v", refs(r.Hits))
	}
	for i := 1; i < len(r.Hits); i++ {
		if r.Hits[i].Rerank > r.Hits[i-1].Rerank {
			t.Fatalf("重排分数该顺着新顺序变小：%v", refs(r.Hits))
		}
	}
	if len(r.Hits) > 0 && r.Hits[0].Rerank == 0 {
		t.Fatal("重排给的分数没带上")
	}
}

// K 可配：只把前两条交给重排，其余照原样接在后面
func TestRerankOnlyTopK(t *testing.T) {
	t.Parallel()
	base := openForRerank(t, Options{}).Recall(context.Background(), inGroup(GA), rerankQuery())
	f := &fakeRerank{fn: reversed}
	r := openForRerank(t, Options{Reranker: f, RerankK: 2}).Recall(context.Background(), inGroup(GA), rerankQuery())
	if len(f.docs) != 2 {
		t.Fatalf("RerankK=2 只该送两条：%v", f.docs)
	}
	baseRefs := refs(base.Candidates)
	if len(baseRefs) < 3 {
		t.Fatalf("这条题要有三条以上候选：%v", baseRefs)
	}
	want := []string{baseRefs[1], baseRefs[0]}
	want = append(want, baseRefs[2:]...)
	if got := refs(r.Candidates); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("只有前两条该换位置：得到 %v，要 %v", got, want)
	}
}

// 超时、出错、顺序不对、未就绪：都退回原来的顺序，记一行日志（不带正文），这一轮不失败
func TestRerankFallsBack(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		fn   func(ctx context.Context, query string, docs []string) ([]int, []float64, error)
		slow bool
		want string
	}{
		{"超时", func(ctx context.Context, q string, docs []string) ([]int, []float64, error) {
			<-ctx.Done()
			return nil, nil, ctx.Err()
		}, true, "超时"},
		{"出错", func(ctx context.Context, q string, docs []string) ([]int, []float64, error) {
			return nil, nil, errors.New("假服务回了 500")
		}, false, "出错"},
		{"未就绪", func(ctx context.Context, q string, docs []string) ([]int, []float64, error) {
			return nil, nil, embed.ErrUnavailable
		}, false, "未就绪"},
		{"越界", func(ctx context.Context, q string, docs []string) ([]int, []float64, error) {
			order := make([]int, len(docs))
			for i := range order {
				order[i] = i
			}
			order[0] = len(docs) + 3
			return order, nil, nil
		}, false, "顺序不对"},
		{"重复", func(ctx context.Context, q string, docs []string) ([]int, []float64, error) {
			order := make([]int, len(docs))
			for i := range order {
				order[i] = i
			}
			order[1] = order[0]
			return order, nil, nil
		}, false, "顺序不对"},
		{"少一条", func(ctx context.Context, q string, docs []string) ([]int, []float64, error) {
			order := make([]int, len(docs)-1)
			for i := range order {
				order[i] = i
			}
			return order, nil, nil
		}, false, "顺序不对"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			base := openForRerank(t, Options{}).Recall(context.Background(), inGroup(GA), rerankQuery())
			var logs []string
			m := openForRerank(t, Options{Reranker: &fakeRerank{fn: c.fn}, Logf: func(f string, a ...any) {
				logs = append(logs, fmt.Sprintf(f, a...))
			}})
			ctx, cancel := context.Background(), func() {}
			if c.slow {
				ctx, cancel = context.WithTimeout(ctx, 30*time.Millisecond)
			}
			defer cancel()
			r := m.Recall(ctx, inGroup(GA), rerankQuery())
			if r.Reranked || r.RerankSkipped != c.want {
				t.Fatalf("该退回原顺序、原因 %q：%v %q", c.want, r.Reranked, r.RerankSkipped)
			}
			if got, want := fmt.Sprint(refs(r.Candidates)), fmt.Sprint(refs(base.Candidates)); got != want {
				t.Fatalf("没退回原来的顺序：%s，要 %s", got, want)
			}
			if got, want := fmt.Sprint(refs(r.Hits)), fmt.Sprint(refs(base.Hits)); got != want {
				t.Fatalf("给出来的也乱了：%s，要 %s", got, want)
			}
			if len(logs) != 1 || !strings.Contains(logs[0], c.want) {
				t.Fatalf("该记一行日志说一声（%s）：%v", c.want, logs)
			}
			if strings.Contains(logs[0], "吉他的事") {
				t.Fatalf("日志不该带正文：%q", logs[0])
			}
		})
	}
}

// 重排接收同角色跨场合候选，不能把旧的场合过滤重新加进来。
func TestRerankIncludesCrossSceneItems(t *testing.T) {
	t.Parallel()
	f := &fakeRerank{fn: reversed}
	m := openForRerank(t, Options{Reranker: f})
	r := m.Recall(context.Background(), inGroup(GA), rerankQuery())
	if len(f.docs) == 0 {
		t.Fatal("重排该被调到（这条题有几条候选）")
	}
	if len(f.docs) != min(len(r.Candidates), RerankK) {
		t.Fatalf("送去重排的该正好是前 K 条候选：%d 条，候选 %d 条", len(f.docs), len(r.Candidates))
	}
	for _, text := range []string{"群B", "主人的"} {
		if !strings.Contains(strings.Join(f.docs, "\n"), text) {
			t.Errorf("重排缺少跨场合候选 %q：%v", text, f.docs)
		}
	}
}

// W1a：重排只在明确回忆（memory_search）上调。普通召回（被叫到也算）和问以前的都不调，哪怕候选有好几条
func TestRerankOnlyExplicit(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		q     Query
		calls int
	}{
		{"每轮自动想起", Query{Text: "吉他", Mode: ModeNormal, Addressed: true}, 0},
		{"问以前", Query{Text: "上次的吉他", Mode: ModePast}, 0},
		{"明确回忆", Query{Text: "吉他", Mode: ModeExplicit}, 1},
		{"明确回忆带 TopK（线上 memory_search 那条分支）", Query{Text: "吉他", Mode: ModeExplicit, TopK: 12}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeRerank{fn: reversed}
			m := openForRerank(t, Options{Reranker: f})
			r := m.Recall(context.Background(), inGroup(GA), tc.q)
			if f.calls != tc.calls || r.Reranked != (tc.calls == 1) {
				t.Fatalf("重排该调 %d 次、Reranked=%v：调了 %d 次，Reranked=%v（候选 %d 条）", tc.calls, tc.calls == 1, f.calls, r.Reranked, len(r.Candidates))
			}
			if len(r.Candidates) < 3 {
				t.Fatalf("这条题要有几条候选才分得出调没调：%v", refs(r.Candidates))
			}
		})
	}
}

// 四种场合的重排候选都包含同角色的八条有效事实。
func TestRerankInputsVisibleInFourPlaces(t *testing.T) {
	t.Parallel()
	file := bunDb(t, [][]string{
		{"1", "fact", "active", "公共的吉他事一", "owner", "public"},
		{"2", "fact", "active", "公共的吉他事二", "owner", "public"},
		{"3", "fact", "active", "主人私下的吉他事", "owner", "owner"},
		{"4", "fact", "active", "群A的吉他事", "qq:1", "conv:" + GA},
		{"5", "fact", "active", "群B的吉他事", "qq:1", "conv:" + GB},
		{"6", "fact", "active", "给六万的吉他事", "qq:60001", "person:qq:60001"},
		{"7", "fact", "active", "给七万的吉他事", "qq:70001", "person:qq:70001"},
		{"8", "fact", "active", "本地群的吉他事", "owner", "conv:console:g1"},
	}, nil)
	places := []struct {
		name string
		a    Audience
	}{
		{"本地私聊", ownerHere},
		{"本地群聊", Audience{Channel: "console", Conversation: "console:g1", Scene: "group"}},
		{"QQ 私聊", Audience{Channel: "qq", Conversation: "qq:60001", Scene: "private", PersonIDs: []string{"qq:60001"}}},
		{"QQ 群", inGroup(GA)},
	}
	for _, p := range places {
		t.Run(p.name, func(t *testing.T) {
			f := &fakeRerank{fn: reversed}
			m, err := Open(context.Background(), file, nil, nil, Options{Reranker: f})
			if err != nil {
				t.Fatal(err)
			}
			defer m.Close()
			m.Recall(context.Background(), p.a, Query{Text: "吉他", Mode: ModeExplicit, TopK: 12})
			if f.calls != 1 || len(f.docs) < 2 {
				t.Fatalf("这个场合至少有两条看得见的，重排该被调一次：%d 次 %v", f.calls, f.docs)
			}
			if len(f.docs) != 8 {
				t.Fatalf("跨场合候选应有八条：%v", f.docs)
			}
		})
	}
}

// memory.rerank.model 写的 id 服务里没有：每次都一样，原因码 rerank-model-unknown 照写进 RerankSkipped，日志只写一行
func TestRerankModelUnknownReasonCode(t *testing.T) {
	t.Parallel()
	f := &fakeRerank{fn: func(context.Context, string, []string) ([]int, []float64, error) {
		return nil, nil, fmt.Errorf("%w：memory.rerank.model 写的 bge-typo 服务里没有", embed.ErrRerankModelUnknown)
	}}
	var logs []string
	m := openForRerank(t, Options{Reranker: f, Logf: func(format string, a ...any) { logs = append(logs, fmt.Sprintf(format, a...)) }})
	for i := 0; i < 3; i++ {
		r := m.Recall(context.Background(), inGroup(GA), rerankQuery())
		if r.Reranked || r.RerankSkipped != "rerank-model-unknown" {
			t.Fatalf("第 %d 次：该带原因码 rerank-model-unknown：%v %q", i+1, r.Reranked, r.RerankSkipped)
		}
	}
	n := 0
	for _, l := range logs {
		if strings.Contains(l, "rerank-model-unknown") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("同一个原因的日志只该写一行：%d 行 %v", n, logs)
	}
}
