package memory

import (
	"context"
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"sumika-native/internal/embed"
)

// 发送前守卫的边界与退路：门槛等号、非法门槛、嵌入给出怪东西、确认模型缺失或胡答、截止时间到在途中。
// 听众取本地群聊、QQ 私聊、QQ 群三种（本地私聊在回合层就跳过了，不进这里）；条目只有主人可见，三种听众都看不到。

type edgeAudience struct {
	name string
	a    Audience
}

var edgeAudiences = []edgeAudience{
	{"本地群聊", Audience{Channel: "console", Scene: "group"}},
	{"QQ私聊", Audience{Channel: "qq", Scene: "private"}},
	{"QQ群", Audience{Channel: "qq", Scene: "group"}},
}

func eachEdgeAudience(t *testing.T, f func(t *testing.T, a Audience)) {
	t.Helper()
	for _, ea := range edgeAudiences {
		t.Run(ea.name, func(t *testing.T) { f(t, ea.a) })
	}
}

// edgeEmbed 想给什么向量就给什么（数量、维度、NaN 都由 fn 决定），并让 fn 看得到 ctx。
type edgeEmbed struct {
	fn func(ctx context.Context, kind embed.Type, texts []string) ([][]float32, error)
}

func (e *edgeEmbed) Model() string { return "编的嵌入" }
func (e *edgeEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return e.fn(ctx, embed.TypeDocument, texts)
}
func (e *edgeEmbed) EmbedTyped(ctx context.Context, kind embed.Type, texts []string) ([][]float32, error) {
	return e.fn(ctx, kind, texts)
}

// edgeVectors 库存句一律给 doc，查询句一律给 query。
func edgeVectors(doc, query []float32) func(context.Context, embed.Type, []string) ([][]float32, error) {
	return func(_ context.Context, kind embed.Type, texts []string) ([][]float32, error) {
		out := make([][]float32, len(texts))
		for i := range texts {
			if kind == embed.TypeQuery {
				out[i] = append([]float32(nil), query...)
			} else {
				out[i] = append([]float32(nil), doc...)
			}
		}
		return out, nil
	}
}

type edgeGen struct {
	fn func(ctx context.Context) (Reply, error)
}

func (g *edgeGen) Call(ctx context.Context, model, prompt, cacheKey string) (Reply, error) {
	return g.fn(ctx)
}

const (
	edgeSecret = "主人有一件不会公开的安排"
	edgeQuery  = "他准备改个日程"
)

// edgeRig 一条只有主人可见的候选 fact；确认模型默认答 yes。
func edgeRig(t *testing.T, fn func(context.Context, embed.Type, []string) ([][]float32, error)) *Memory {
	t.Helper()
	m, _, g, _ := outboundRig(t, edgeSecret)
	g.reply = `{"leaked":"yes"}`
	m.embed = &edgeEmbed{fn: fn}
	return m
}

func edgeExpect(t *testing.T, m *Memory, a Audience, text string, want []string, route string) {
	t.Helper()
	got, err := m.CheckOutbound(context.Background(), text, a)
	if err != nil || !reflect.DeepEqual(got, want) || m.GuardRoute() != route {
		t.Fatalf("blocked=%v err=%v route=%s; want=%v/%s", got, err, m.GuardRoute(), want, route)
	}
}

// edgeQueryScoring 找一个查询向量，使它与库存向量 [1,0] 归一化后的 float32 点积恰好等于 target。
// 先算实际得分再断言：float32 里 0.70 并不精确，门槛两侧的得分只能这样逐格挑出来。
func edgeQueryScoring(t *testing.T, target float32) []float32 {
	t.Helper()
	doc := []float32{1, 0}
	r := float32(0.7)
	for range 64 {
		r = math.Nextafter32(r, 0)
	}
	for range 256 {
		q := []float32{r, 0.7141428}
		if embed.Dot(embed.Unit(q), doc) == target {
			return q
		}
		r = math.Nextafter32(r, 1)
	}
	t.Fatalf("挑不出得分恰为 %v 的查询向量", target)
	return nil
}

// 默认门槛 0.70 的等号两侧：低一格、正好、高一格（按归一化后的实际 float32 得分）。
func TestOutboundEdgeDefaultThreshold(t *testing.T) {
	t.Parallel()
	eq := float32(0.7)
	for _, tc := range []struct {
		name  string
		score float32
		block bool
	}{
		{"低一格", math.Nextafter32(eq, 0), false},
		{"正好等于", eq, true},
		{"高一格", math.Nextafter32(eq, 1), true},
	} {
		for _, check := range []string{"CheckRepeat", "CheckOutbound"} {
			t.Run(tc.name+"/"+check, func(t *testing.T) {
				t.Parallel()
				q := edgeQueryScoring(t, tc.score)
				eachEdgeAudience(t, func(t *testing.T, a Audience) {
					m := edgeRig(t, edgeVectors([]float32{1, 0}, q))
					var got []string
					var err error
					if check == "CheckRepeat" {
						got, err = m.CheckRepeat(context.Background(), edgeQuery, a)
					} else {
						got, err = m.CheckOutbound(context.Background(), edgeQuery, a)
					}
					var want []string
					if tc.block {
						want = []string{"f:100"}
					}
					if err != nil || !reflect.DeepEqual(got, want) || m.GuardRoute() != "normal" {
						t.Fatalf("得分=%v blocked=%v err=%v route=%s; want=%v", tc.score, got, err, m.GuardRoute(), want)
					}
				})
			})
		}
	}
}

func TestOutboundEdgeInvalidThreshold(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		v    float64
	}{
		{"NaN", math.NaN()},
		{"负数", -0.1},
		{"负无穷", math.Inf(-1)},
		{"大于1", 1.0000001},
		{"正无穷", math.Inf(1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eachEdgeAudience(t, func(t *testing.T, a Audience) {
				// 向量完全相同：门槛不合法时若被当成"比不过"，这句会被悄悄放行。
				m := edgeRig(t, edgeVectors([]float32{1, 0}, []float32{0, 1}))
				m.opt.GuardThreshold = tc.v
				got, err := m.CheckOutbound(context.Background(), edgeQuery, a)
				if err == nil || len(got) != 0 {
					t.Fatalf("非法门槛 %v 没报错：blocked=%v err=%v", tc.v, got, err)
				}
			})
		})
	}
	t.Run("零用默认0.70", func(t *testing.T) {
		t.Parallel()
		eachEdgeAudience(t, func(t *testing.T, a Audience) {
			m := edgeRig(t, edgeVectors([]float32{1, 0}, edgeQueryScoring(t, math.Nextafter32(0.7, 1))))
			m.opt.GuardThreshold = 0
			edgeExpect(t, m, a, edgeQuery, []string{"f:100"}, "normal")
			m.opt.GuardThreshold = 1
			edgeExpect(t, m, a, edgeQuery, nil, "normal")
		})
	})
}

// 嵌入给出不对的东西：不许悄悄当成"不像"放行而路线仍是正常；按 ADR-186 退字面，路线报 embed-down。
func TestOutboundEdgeBadEmbeddings(t *testing.T) {
	t.Parallel()
	nan, inf := float32(math.NaN()), float32(math.Inf(1))
	tiny := float32(math.SmallestNonzeroFloat32) // 模长平方在 float64 里不为零，归一化时 1/模长 在 float32 里溢出
	same := []float32{1, 0}
	type fn = func(context.Context, embed.Type, []string) ([][]float32, error)
	only := func(kind embed.Type, bad []float32) fn {
		return func(_ context.Context, k embed.Type, texts []string) ([][]float32, error) {
			out := make([][]float32, len(texts))
			for i := range texts {
				out[i] = append([]float32(nil), same...)
				if k == kind {
					out[i] = append([]float32(nil), bad...)
				}
			}
			return out, nil
		}
	}
	count := func(kind embed.Type, n int) fn {
		return func(_ context.Context, k embed.Type, texts []string) ([][]float32, error) {
			if k != kind {
				return edgeVectors(same, same)(context.Background(), k, texts)
			}
			out := make([][]float32, n)
			for i := range out {
				out[i] = append([]float32(nil), same...)
			}
			return out, nil
		}
	}
	// 同一批里第二句的维度和第一句不同
	mixed := func(kind embed.Type) fn {
		return func(_ context.Context, k embed.Type, texts []string) ([][]float32, error) {
			out, _ := edgeVectors(same, same)(context.Background(), k, texts)
			if k == kind && len(out) > 1 {
				out[1] = []float32{1, 0, 0}
			}
			return out, nil
		}
	}
	two := "第一句话。第二句话。"
	for _, tc := range []struct {
		name         string
		secret, text string
		fn           fn
	}{
		{"查询数量少", edgeSecret, edgeQuery, count(embed.TypeQuery, 0)},
		{"查询数量多", edgeSecret, edgeQuery, count(embed.TypeQuery, 2)},
		{"库存数量少", edgeSecret, edgeQuery, count(embed.TypeDocument, 0)},
		{"库存数量多", edgeSecret, edgeQuery, count(embed.TypeDocument, 2)},
		{"查询空向量", edgeSecret, edgeQuery, only(embed.TypeQuery, []float32{})},
		{"查询零向量", edgeSecret, edgeQuery, only(embed.TypeQuery, []float32{0, 0})},
		{"库存零向量", edgeSecret, edgeQuery, only(embed.TypeDocument, []float32{0, 0})},
		{"查询NaN", edgeSecret, edgeQuery, only(embed.TypeQuery, []float32{nan, 0})},
		{"查询Inf", edgeSecret, edgeQuery, only(embed.TypeQuery, []float32{inf, 0})},
		{"查询负Inf", edgeSecret, edgeQuery, only(embed.TypeQuery, []float32{-inf, 0})},
		{"库存NaN", edgeSecret, edgeQuery, only(embed.TypeDocument, []float32{0, nan})},
		{"库存Inf", edgeSecret, edgeQuery, only(embed.TypeDocument, []float32{inf, 1})},
		{"查询模长极小", edgeSecret, edgeQuery, only(embed.TypeQuery, []float32{tiny, 0})},
		{"库存模长极小", edgeSecret, edgeQuery, only(embed.TypeDocument, []float32{tiny, 0})},
		{"查询同批维度不一", edgeSecret, two, mixed(embed.TypeQuery)},
		{"库存同批维度不一", two, edgeQuery, mixed(embed.TypeDocument)},
		{"库存与查询维度不同", edgeSecret, edgeQuery, only(embed.TypeDocument, []float32{1, 0, 0})},
		{"查询比库存多一维", edgeSecret, edgeQuery, only(embed.TypeQuery, []float32{1, 0, 0})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eachEdgeAudience(t, func(t *testing.T, a Audience) {
				m := edgeRig(t, tc.fn)
				m.items[0].Text = tc.secret
				edgeExpect(t, m, a, tc.text, nil, "embed-down")
			})
		})
	}
}

// 派生库里缓存的句向量坏了：不能把 NaN 点积当成"不像"放行。
func TestOutboundEdgeCachedVectors(t *testing.T) {
	t.Parallel()
	nan, inf := float32(math.NaN()), float32(math.Inf(1))
	for _, tc := range []struct {
		name  string
		blob  []byte
		heal  bool // 能识别成坏向量并重算覆盖
		route string
	}{
		{"含NaN", encodeVec([]float32{nan, 0}), true, "normal"},
		{"含Inf", encodeVec([]float32{inf, 0}), true, "normal"},
		{"全零", encodeVec([]float32{0, 0}), true, "normal"},
		{"被缩小到几乎为零", encodeVec([]float32{1e-3, 0}), false, "normal"},
		{"维度多一维", encodeVec([]float32{1, 0, 0}), true, "normal"},
		{"维度少一维", encodeVec([]float32{1}), true, "normal"},
		{"截断的字节", []byte{0, 0, 0x80, 0x3f, 1, 2, 3}, true, "normal"},              // 解出来一维
		{"尾部多出零碎字节", append(encodeVec([]float32{1, 0}), 7, 7, 7), true, "normal"}, // 解出来维度对，字节数不是 4 的倍数
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eachEdgeAudience(t, func(t *testing.T, a Audience) {
				m := edgeRig(t, edgeVectors([]float32{1, 0}, []float32{1, 0}))
				edgeExpect(t, m, a, edgeQuery, []string{"f:100"}, "normal") // 先把缓存填上
				res, err := m.index.db.Exec(`UPDATE memory_sentences SET vec=? WHERE ref='f:100' AND idx=0`, tc.blob)
				if n, _ := res.RowsAffected(); err != nil || n != 1 {
					t.Fatalf("改缓存没改到：%v %d", err, n)
				}
				// 缩小的向量归一化以后方向没变，仍然像；坏向量重算以后也像。都必须拦，且路线正常。
				edgeExpect(t, m, a, edgeQuery, []string{"f:100"}, "normal")
				if tc.heal {
					var blob []byte
					if err := m.index.db.QueryRow(`SELECT vec FROM memory_sentences WHERE ref='f:100' AND idx=0`).Scan(&blob); err != nil || len(blob) != 8 || !reflect.DeepEqual(decodeVec(blob), []float32{1, 0}) {
						t.Fatalf("坏缓存没被重算覆盖：%v（%d 字节）%v", decodeVec(blob), len(blob), err)
					}
				}
			})
		})
	}
	t.Run("同一条两句里一句维度异常", func(t *testing.T) {
		t.Parallel()
		eachEdgeAudience(t, func(t *testing.T, a Audience) {
			m := edgeRig(t, edgeVectors([]float32{1, 0}, []float32{0, 1}))
			m.items[0].Text = "第一句话。第二句话。"
			edgeExpect(t, m, a, edgeQuery, nil, "normal")
			res, err := m.index.db.Exec(`UPDATE memory_sentences SET vec=? WHERE ref='f:100' AND idx=1`, encodeVec([]float32{0, 1, 0}))
			if n, _ := res.RowsAffected(); err != nil || n != 1 {
				t.Fatalf("改缓存没改到：%v %d", err, n)
			}
			// 维度异常的那一句重算覆盖，守卫不会长期停在退路上；库存句 [1,0] 与查询 [0,1] 正交，所以不拦。
			edgeExpect(t, m, a, edgeQuery, nil, "normal")
			var blob []byte
			if err := m.index.db.QueryRow(`SELECT vec FROM memory_sentences WHERE ref='f:100' AND idx=1`).Scan(&blob); err != nil || !reflect.DeepEqual(decodeVec(blob), []float32{1, 0}) {
				t.Fatalf("维度异常的缓存没被重算覆盖：%v %v", decodeVec(blob), err)
			}
		})
	})
}

// 确认小模型缺失、胡答：过了相似度门槛的全部拦下，路线报 model-down。
func TestOutboundEdgeConfirmModel(t *testing.T) {
	t.Parallel()
	setup := func(t *testing.T) *Memory {
		return edgeRig(t, edgeVectors([]float32{1, 0}, []float32{1, 0}))
	}
	for _, tc := range []struct {
		name  string
		apply func(m *Memory)
	}{
		{"生成器缺失", func(m *Memory) { m.opt.Gen = nil }},
		{"模型函数缺失", func(m *Memory) { m.opt.Model = nil }},
		{"模型名为空", func(m *Memory) { m.opt.Model = func() string { return "" } }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eachEdgeAudience(t, func(t *testing.T, a Audience) {
				m := setup(t)
				tc.apply(m)
				edgeExpect(t, m, a, edgeQuery, []string{"f:100"}, "model-down")
			})
		})
	}
	t.Run("没过相似度门槛时模型缺失无关", func(t *testing.T) {
		t.Parallel()
		eachEdgeAudience(t, func(t *testing.T, a Audience) {
			m := edgeRig(t, edgeVectors([]float32{1, 0}, []float32{0, 1}))
			m.opt.Gen = nil
			edgeExpect(t, m, a, edgeQuery, nil, "normal")
		})
	})
	for _, reply := range []string{
		`{"leaked":"maybe"}`, `{"leaked":""}`, `{}`, `{"leaked":"YES"}`, `{"leaked":" no"}`,
		`{"leaked":true}`, `{"leaked":null}`, `{"leaked":["yes"]}`, `[]`, `null`, `yes`, ``,
	} {
		t.Run("leaked="+reply, func(t *testing.T) {
			t.Parallel()
			eachEdgeAudience(t, func(t *testing.T, a Audience) {
				m := setup(t)
				m.opt.Gen = &outboundFakeGen{reply: reply}
				edgeExpect(t, m, a, edgeQuery, []string{"f:100"}, "model-down")
			})
		})
	}
}

// 整体取消、截止在嵌入或确认途中到达：返回错误（上层拦下），哪怕这时模型 / 嵌入已经给了"放行"的答案。
func TestOutboundEdgeDeadlineMidway(t *testing.T) {
	t.Parallel()
	block := func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() }
	for _, tc := range []struct {
		name string
		want error
		run  func(m *Memory) (context.Context, context.CancelFunc)
	}{
		{"嵌入途中截止", context.DeadlineExceeded, func(m *Memory) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
			m.embed = &edgeEmbed{fn: func(ctx context.Context, _ embed.Type, _ []string) ([][]float32, error) { return nil, block(ctx) }}
			return ctx, cancel
		}},
		{"嵌入途中取消且仍给了向量", context.Canceled, func(m *Memory) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			m.embed = &edgeEmbed{fn: func(c context.Context, k embed.Type, texts []string) ([][]float32, error) {
				cancel()
				return edgeVectors([]float32{1, 0}, []float32{0, 1})(c, k, texts)
			}}
			return ctx, cancel
		}},
		{"确认途中截止", context.DeadlineExceeded, func(m *Memory) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			m.opt.Gen = &edgeGen{fn: func(c context.Context) (Reply, error) { return Reply{}, block(c) }}
			return ctx, cancel
		}},
		{"确认途中取消却答了否", context.Canceled, func(m *Memory) (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			m.opt.Gen = &edgeGen{fn: func(context.Context) (Reply, error) { cancel(); return Reply{Text: `{"leaked":"no"}`}, nil }}
			return ctx, cancel
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eachEdgeAudience(t, func(t *testing.T, a Audience) {
				m := edgeRig(t, edgeVectors([]float32{1, 0}, []float32{1, 0}))
				ctx, cancel := tc.run(m)
				defer cancel()
				got, err := m.CheckOutbound(ctx, edgeQuery, a)
				if !errors.Is(err, tc.want) || len(got) != 0 {
					t.Fatalf("blocked=%v err=%v; want err=%v", got, err, tc.want)
				}
			})
		})
	}
}

// 内层时限（ADR-186 第 4 条）：嵌入到点退字面、照常发，路线报 embed-down；确认到点按相似度拦，路线报 model-down。
// 量的是假嵌入 / 假模型被调用时离截止还剩多久，不靠墙钟；两个选项各管各的，零或负用默认，比整体长的到 10 秒为止。
func TestOutboundEdgeInnerTimeouts(t *testing.T) {
	t.Parallel()
	const short = 50 * time.Millisecond
	left := func(ctx context.Context) time.Duration {
		dl, ok := ctx.Deadline()
		if !ok {
			return -1
		}
		return time.Until(dl)
	}
	type fn = func(context.Context, embed.Type, []string) ([][]float32, error)
	// stall 让 kind 那一类嵌入记下剩余时间、等到截止；then 为空就报截止的错，不为空就到点以后照样给出它的向量。
	stall := func(kind embed.Type, got *time.Duration, then fn) fn {
		return func(ctx context.Context, k embed.Type, texts []string) ([][]float32, error) {
			if k != kind {
				return edgeVectors([]float32{1, 0}, []float32{1, 0})(ctx, k, texts)
			}
			*got = left(ctx)
			<-ctx.Done()
			if then == nil {
				return nil, ctx.Err()
			}
			return then(ctx, k, texts)
		}
	}
	for _, tc := range []struct {
		name string
		kind embed.Type
		then fn
	}{
		{"查询句嵌入到点", embed.TypeQuery, nil},
		{"库存句嵌入到点", embed.TypeDocument, nil},
		{"到点以后才给了向量", embed.TypeQuery, edgeVectors([]float32{1, 0}, []float32{0, 1})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eachEdgeAudience(t, func(t *testing.T, a Audience) {
				var got time.Duration
				m := edgeRig(t, stall(tc.kind, &got, tc.then))
				m.opt.GuardEmbedTimeout = short
				edgeExpect(t, m, a, edgeQuery, nil, "embed-down")
				if got <= 0 || got > short {
					t.Fatalf("嵌入拿到的剩余时间 %v，应在 (0, %v] 内", got, short)
				}
			})
		})
	}
	for _, tc := range []struct {
		name  string
		reply string // 到点以后才回的话；空就报截止的错
	}{
		{"确认到点", ""},
		{"到点以后才答了否", `{"leaked":"no"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eachEdgeAudience(t, func(t *testing.T, a Audience) {
				var got time.Duration
				m := edgeRig(t, edgeVectors([]float32{1, 0}, []float32{1, 0}))
				m.opt.GuardConfirmTimeout = short
				m.opt.Gen = &edgeGen{fn: func(ctx context.Context) (Reply, error) {
					got = left(ctx)
					<-ctx.Done()
					if tc.reply == "" {
						return Reply{}, ctx.Err()
					}
					return Reply{Text: tc.reply}, nil
				}}
				edgeExpect(t, m, a, edgeQuery, []string{"f:100"}, "model-down")
				if got <= 0 || got > short {
					t.Fatalf("确认拿到的剩余时间 %v，应在 (0, %v] 内", got, short)
				}
			})
		})
	}
	for _, tc := range []struct {
		name                   string
		d                      time.Duration
		embedMin, embedMax     time.Duration
		confirmMin, confirmMax time.Duration
	}{
		// 默认 2 秒、8 秒：剩余时间落在默认的一半以上、默认以内，也看得出两个没有互换
		{"零用默认", 0, GuardEmbedTimeout / 2, GuardEmbedTimeout, GuardEmbedTimeout, GuardConfirmTimeout},
		{"负数用默认", -time.Second, GuardEmbedTimeout / 2, GuardEmbedTimeout, GuardEmbedTimeout, GuardConfirmTimeout},
		{"比整体长的到10秒为止", time.Minute, GuardConfirmTimeout, 10 * time.Second, GuardConfirmTimeout, 10 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			eachEdgeAudience(t, func(t *testing.T, a Audience) {
				var embedLeft, confirmLeft time.Duration
				m := edgeRig(t, func(ctx context.Context, k embed.Type, texts []string) ([][]float32, error) {
					if k == embed.TypeQuery {
						embedLeft = left(ctx)
					}
					return edgeVectors([]float32{1, 0}, []float32{1, 0})(ctx, k, texts)
				})
				m.opt.GuardEmbedTimeout, m.opt.GuardConfirmTimeout = tc.d, tc.d
				m.opt.Gen = &edgeGen{fn: func(ctx context.Context) (Reply, error) {
					confirmLeft = left(ctx)
					return Reply{Text: `{"leaked":"yes"}`}, nil
				}}
				edgeExpect(t, m, a, edgeQuery, []string{"f:100"}, "normal")
				if embedLeft <= tc.embedMin || embedLeft > tc.embedMax || confirmLeft <= tc.confirmMin || confirmLeft > tc.confirmMax {
					t.Fatalf("剩余时间：嵌入 %v（应在 (%v, %v]）、确认 %v（应在 (%v, %v]）", embedLeft, tc.embedMin, tc.embedMax, confirmLeft, tc.confirmMin, tc.confirmMax)
				}
			})
		})
	}
}
