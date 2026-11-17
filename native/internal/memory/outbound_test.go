package memory

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"sumika-native/internal/embed"
)

// 嵌入按文本给可控的语义方向；库存和查询类型分开计，缓存失效会反映到真实判定。
type outboundFakeEmbed struct {
	model  string
	down   bool
	vector func(string) []float32
}

func (e *outboundFakeEmbed) Model() string { return e.model }
func (e *outboundFakeEmbed) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return e.EmbedTyped(ctx, embed.TypeDocument, texts)
}
func (e *outboundFakeEmbed) EmbedTyped(ctx context.Context, kind embed.Type, texts []string) ([][]float32, error) {
	if e.down {
		return nil, errors.New("编的嵌入错误和令牌")
	}
	out := make([][]float32, len(texts))
	for i, s := range texts {
		out[i] = e.vector(s)
	}
	return out, nil
}

type outboundFakeGen struct {
	reply  string
	fail   bool
	answer func(string) string
}

func (g *outboundFakeGen) Call(ctx context.Context, model, prompt, cacheKey string) (Reply, error) {
	if g.answer != nil {
		return Reply{Text: g.answer(prompt)}, nil
	}
	if g.fail {
		return Reply{}, errors.New("编的模型错误和令牌")
	}
	return Reply{Text: g.reply}, nil
}

func outboundRig(t *testing.T, text string) (*Memory, *outboundFakeEmbed, *outboundFakeGen, *logSink) {
	t.Helper()
	x, err := OpenIndex(context.Background(), filepath.Join(t.TempDir(), "memory-v2.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { x.Close() })
	e := &outboundFakeEmbed{model: "编的嵌入", vector: func(string) []float32 { return []float32{1, 0} }}
	g := &outboundFakeGen{reply: `{"leaked":"yes"}`}
	logs := &logSink{}
	m := &Memory{index: x, embed: e, items: []Item{{Ref: "f:100", Type: "fact", Status: "candidate", Text: text, Scopes: []string{"owner"}}}, opt: Options{Gen: g, Model: func() string { return "编的模型" }, Logf: logs.logf}}
	return m, e, g, logs
}
func outboundGroup() Audience {
	return Audience{Channel: "qq", Conversation: "qq:100:group:200", Scene: "group"}
}
func outboundCheck(t *testing.T, m *Memory, text string, want []string, route string) {
	t.Helper()
	got, err := m.CheckOutbound(context.Background(), text, outboundGroup())
	if err != nil || !reflect.DeepEqual(got, want) || m.GuardRoute() != route {
		t.Fatalf("blocked=%v err=%v route=%s; want=%v/%s", got, err, m.GuardRoute(), want, route)
	}
}

func TestOutboundLiteral(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, memory, text string
		blocked            bool
	}{
		{"十字跨标点", "主人把备用电池收在蓝柜底层", "把备用电池，收在蓝柜底层！", true},
		{"六字短记忆", "蓝柜藏备用电池", "蓝柜藏备用电池", true},
		{"不足六字", "蓝柜电池", "蓝柜电池", false},
		{"只有九字相同", "甲乙丙丁戊己庚辛壬癸", "甲乙丙丁戊己庚辛壬另一个字", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, _, _, _ := outboundRig(t, tc.memory)
			m.embed = nil
			var want []string
			route := "embed-down"
			if tc.blocked {
				want = []string{"f:100"}
				route = "normal"
			}
			outboundCheck(t, m, tc.text, want, route)
		})
	}
}

func TestOutboundFallbacks(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name                 string
		embedDown, modelDown bool
		reply                string
		want                 []string
		route                string
	}{
		{"嵌入失败", true, false, `{"leaked":"yes"}`, nil, "embed-down"},
		{"模型失败", false, true, "", []string{"f:100"}, "model-down"},
		{"模型坏输出", false, false, "编的坏输出", []string{"f:100"}, "model-down"},
		{"模型否认", false, false, `{"leaked":"no"}`, nil, "normal"},
		{"模型确认", false, false, `{"leaked":"yes"}`, []string{"f:100"}, "normal"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, e, g, _ := outboundRig(t, "主人把纪念票根放在蓝盒子")
			e.down, g.fail, g.reply = tc.embedDown, tc.modelDown, tc.reply
			outboundCheck(t, m, "他留着那些老车票", tc.want, tc.route)
		})
	}
}

// 没走到嵌入这一步的提前返回（无可判句子、没有禁看条目、字面先命中、上下文已结束）不覆盖上一次的路线：
// 嵌入挂着时一句空回复或主人私聊的一句话，不能把路线写回"正常"，不然守卫退路的提醒会来回跳。
func TestOutboundEarlyExitKeepsRoute(t *testing.T) {
	t.Parallel()
	ownerPrivate := Audience{Channel: "console", Conversation: "console:private", Scene: "private", PrivateOK: true}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	for _, tc := range []struct {
		name string
		run  func(m *Memory) ([]string, error)
		want []string
	}{
		{"全是标点没有可判句子", func(m *Memory) ([]string, error) {
			return m.CheckOutbound(context.Background(), "。。。", outboundGroup())
		}, nil},
		{"主人私聊没有禁看条目", func(m *Memory) ([]string, error) {
			return m.CheckOutbound(context.Background(), "他留着那些老车票", ownerPrivate)
		}, nil},
		{"字面先命中", func(m *Memory) ([]string, error) {
			return m.CheckOutbound(context.Background(), "主人把纪念票根放在蓝盒子", outboundGroup())
		}, []string{"f:100"}},
		{"复读也一样", func(m *Memory) ([]string, error) {
			return m.CheckRepeat(context.Background(), "。。。", outboundGroup())
		}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, e, _, _ := outboundRig(t, "主人把纪念票根放在蓝盒子")
			e.down = true
			outboundCheck(t, m, "他留着那些老车票", nil, "embed-down")
			got, err := tc.run(m)
			if err != nil || !reflect.DeepEqual(got, tc.want) || m.GuardRoute() != "embed-down" {
				t.Fatalf("提前返回不该动路线：blocked=%v err=%v route=%s；要 %v / embed-down", got, err, m.GuardRoute(), tc.want)
			}
			e.down = false
			m.opt.Gen.(*outboundFakeGen).reply = `{"leaked":"no"}`
			outboundCheck(t, m, "他留着那些老车票", nil, "normal")
		})
	}
	// 上下文已结束返回错误，同样不动路线
	m, e, _, _ := outboundRig(t, "主人把纪念票根放在蓝盒子")
	e.down = true
	outboundCheck(t, m, "他留着那些老车票", nil, "embed-down")
	if _, err := m.CheckOutbound(cancelled, "他留着那些老车票", outboundGroup()); err == nil || m.GuardRoute() != "embed-down" {
		t.Fatalf("上下文结束应报错且不动路线：err=%v route=%s", err, m.GuardRoute())
	}
}

func TestOutboundThreshold(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name      string
		threshold float64
		want      []string
	}{
		{"默认低于门槛", 0, nil},
		{"配置放低", 0.65, []string{"f:100"}},
		{"等于门槛", 1, []string{"f:100"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, e, _, _ := outboundRig(t, "主人有一件不会公开的安排")
			m.opt.GuardThreshold = tc.threshold
			if tc.threshold != 1 {
				e.vector = func(s string) []float32 {
					if strings.Contains(s, "安排") {
						return []float32{1, 0}
					}
					return []float32{0.68, 0.7332121}
				}
			}
			outboundCheck(t, m, "他准备改个日程", tc.want, "normal")
		})
	}
}

func TestOutboundCumulative(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, text string
		want       []string
	}{
		{"前段单独", "说到合成主人", nil},
		{"后段单独", "明天轮到牙科了", nil},
		{"累计", "说到合成主人\n明天轮到牙科了", []string{"f:100"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m, e, g, _ := outboundRig(t, "主人明天会去检查牙齿，约了下午的号")
			e.vector = func(s string) []float32 {
				if strings.Contains(s, "合成主人") || strings.Contains(s, "检查牙齿") {
					return []float32{1, 0}
				}
				return []float32{0, 1}
			}
			g.answer = func(prompt string) string {
				if strings.Contains(prompt, `说到合成主人\n明天轮到牙科了`) {
					return `{"leaked":"yes"}`
				}
				return `{"leaked":"no"}`
			}
			outboundCheck(t, m, tc.text, tc.want, "normal")
		})
	}
}

func TestOutboundVisible(t *testing.T) {
	t.Parallel()
	for _, a := range []Audience{
		{Channel: "console", Scene: "private", PrivateOK: true},
		{Channel: "console", Scene: "group", PrivateOK: true},
		{Channel: "qq", Scene: "private", PrivateOK: true},
		{Channel: "qq", Scene: "group", OwnerAsked: true},
	} {
		t.Run(a.Channel+"/"+a.Scene, func(t *testing.T) {
			t.Parallel()
			m, _, _, _ := outboundRig(t, "主人把纪念票根放在蓝盒子")
			got, err := m.CheckOutbound(context.Background(), m.items[0].Text, a)
			if err != nil || len(got) != 0 {
				t.Fatalf("可见私事被拦：%v %v", got, err)
			}
			a.OwnerAsked, a.PrivateOK = false, false
			got, err = m.CheckOutbound(context.Background(), m.items[0].Text, a)
			if err != nil || !reflect.DeepEqual(got, []string{"f:100"}) {
				t.Fatalf("群友追问没拦：%v %v", got, err)
			}
		})
	}
}

func TestOutboundSentenceCache(t *testing.T) {
	t.Parallel()
	m, e, _, _ := outboundRig(t, "第一句话完全无关。第二句话是蓝柜的秘密。")
	e.vector = func(s string) []float32 {
		if strings.Contains(s, "无关") || strings.Contains(s, "改过") {
			return []float32{0, 1}
		}
		return []float32{1, 0}
	}
	outboundCheck(t, m, "他那个隐藏的抽屉", []string{"f:100"}, "normal")
	// 库存若被重复计算会换到不相似方向；第二次必须使用持久表里缓存的第二句。
	e.vector = func(s string) []float32 {
		if strings.Contains(s, "第一句") || strings.Contains(s, "第二句") {
			return []float32{0, 1}
		}
		return []float32{1, 0}
	}
	outboundCheck(t, m, "他那个隐藏的抽屉", []string{"f:100"}, "normal")
	m.items[0].Text = "改过后的无关事情。改过后的另一件事。"
	e.vector = func(s string) []float32 {
		if strings.Contains(s, "改过") {
			return []float32{0, 1}
		}
		return []float32{1, 0}
	}
	outboundCheck(t, m, "他那个隐藏的抽屉", nil, "normal")
	e.model = "编的另一个模型"
	e.vector = func(string) []float32 { return []float32{1, 0} }
	outboundCheck(t, m, "他那个隐藏的抽屉", []string{"f:100"}, "normal")
}

func TestOutboundErrorsAndPrivacy(t *testing.T) {
	t.Parallel()
	t.Run("库失败", func(t *testing.T) {
		t.Parallel()
		m, _, _, _ := outboundRig(t, "主人有一件不会公开的安排")
		m.index.Close()
		_, err := m.CheckOutbound(context.Background(), "他准备改个日程", outboundGroup())
		if err == nil {
			t.Fatal("库失败被当成放行")
		}
	})
	t.Run("整体取消", func(t *testing.T) {
		t.Parallel()
		m, _, _, _ := outboundRig(t, "主人有一件不会公开的安排")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := m.CheckOutbound(ctx, "他准备改个日程", outboundGroup())
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("整体取消=%v", err)
		}
	})
	t.Run("日志脱敏与恢复", func(t *testing.T) {
		t.Parallel()
		m, e, _, logs := outboundRig(t, "编的私事：账号100和令牌200藏在蓝柜")
		e.down = true
		outboundCheck(t, m, "编的待发秘密", nil, "embed-down")
		e.down = false
		outboundCheck(t, m, "编的待发秘密", []string{"f:100"}, "normal")
		log := logs.String()
		if strings.Contains(log, "编的") || strings.Contains(log, "100") || strings.Contains(log, "200") || !strings.Contains(log, "route=embed-down") || !strings.Contains(log, "route=normal") {
			t.Fatalf("日志泄漏或未记录退路：%s", log)
		}
	})
}

// 编译期只核对消费者已有的签名，实际行为在上面的场景里验。
var _ interface {
	CheckOutbound(context.Context, string, Audience) ([]string, error)
} = (*Memory)(nil)

// 守卫切句：原来是 cmd/guard-eval 里自己那份的测试，函数导出以后用例跟着搬过来
func TestOutboundSentences(t *testing.T) {
	t.Parallel()
	got := [][]string{
		OutboundSentences("主人怕铃声。下周去复查！\r\nKeep TeaCup. 费用3.5元；OK?结尾"),
		OutboundSentences("\r\n。！？  "),
	}
	want := [][]string{{"主人怕铃声。", "下周去复查！", "Keep TeaCup.", "费用3.5元；", "OK?", "结尾"}, nil}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("分句 got=%q want=%q", got, want)
	}
}
