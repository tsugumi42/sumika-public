package channel

import (
	"encoding/json"
	"math"
	"os"
	"reflect"
	"testing"
	"time"
)

// 对拍：testdata/gate.json 是 Bun 版算式（daemon/util/gate.ts、fuse.ts）对编的输入的输出（sumika-bun 仓库的 scripts/go-golden-gate.ts 生成，2026-11-01 分仓前是这里的 testdata/gen-gate.ts），Go 版要一样

func near(a, b float64) bool { return math.Abs(a-b) <= 1e-9*math.Max(1, math.Abs(b)) }

type gateFixture struct {
	Features []struct {
		Now   int64 `json:"now"`
		Batch []struct {
			At          int64  `json:"at"`
			Text        string `json:"text"`
			Self        bool   `json:"self"`
			AtOther     bool   `json:"atOther"`
			MentionsHer bool   `json:"mentionsHer"`
		} `json:"batch"`
		Recent []struct {
			At          int64  `json:"at"`
			Text        string `json:"text"`
			Self        bool   `json:"self"`
			AtOther     bool   `json:"atOther"`
			MentionsHer bool   `json:"mentionsHer"`
		} `json:"recent"`
		Features    GateFeatures `json:"features"`
		Probability float64      `json:"probability"`
	} `json:"features"`
	Runs []struct {
		Ops []struct {
			Op        string          `json:"op"`
			Now       int64           `json:"now"`
			P         float64         `json:"p"`
			Frequency float64         `json:"frequency"`
			Jitter    float64         `json:"jitter"`
			Spoke     bool            `json:"spoke"`
			Pending   int             `json:"pending"`
			Called    bool            `json:"called"`
			RawOut    json.RawMessage `json:"out"`
			State     GateState       `json:"state"`
		} `json:"ops"`
	} `json:"runs"`
	Fuse []struct {
		Frequency float64 `json:"frequency"`
		N         int     `json:"n"`
		Forced    bool    `json:"forced"`
		Cap       int     `json:"cap"`
		Why       string  `json:"why"`
	} `json:"fuse"`
}

func loadGateFixture(t *testing.T) gateFixture {
	t.Helper()
	b, err := os.ReadFile("testdata/gate.json")
	if err != nil {
		t.Fatal(err)
	}
	var f gateFixture
	if err := json.Unmarshal(b, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

func TestGateFeaturesAndProbabilityMatchBun(t *testing.T) {
	t.Parallel()
	f := loadGateFixture(t)
	for i, c := range f.Features {
		conv := func(at int64, text string, self, atOther, ment bool) GateMsg {
			return GateMsg{At: time.UnixMilli(at), Text: text, Self: self, AtOther: atOther, Mentions: ment}
		}
		var batch, recent []GateMsg
		for _, m := range c.Batch {
			batch = append(batch, conv(m.At, m.Text, m.Self, m.AtOther, m.MentionsHer))
		}
		for _, m := range c.Recent {
			recent = append(recent, conv(m.At, m.Text, m.Self, m.AtOther, m.MentionsHer))
		}
		got := GateFeaturesOf(batch, recent, time.UnixMilli(c.Now))
		want := c.Features
		if got.Mention != want.Mention || got.AtOther != want.AtOther || got.Question != want.Question || got.MediaOnly != want.MediaOnly ||
			got.RecentCount != want.RecentCount || got.Batch != want.Batch || !near(got.SelfRatio, want.SelfRatio) || !near(got.SinceSelfSec, want.SinceSelfSec) {
			t.Fatalf("第 %d 组特征不一样：Go %+v，Bun %+v", i, got, want)
		}
		if p := BorrowedWeights.Probability(got); !near(p, c.Probability) {
			t.Fatalf("第 %d 组可能性不一样：Go %v，Bun %v", i, p, c.Probability)
		}
	}
	if len(f.Features) < 100 {
		t.Fatal("对拍的样本太少")
	}
}

func TestGateStateMachineMatchesBun(t *testing.T) {
	t.Parallel()
	f := loadGateFixture(t)
	var evals, backoffs, bursts int
	for ri, run := range f.Runs {
		s := NewGateState()
		for oi, o := range run.Ops {
			now := time.UnixMilli(o.Now)
			where := func() string { return "第 " + itoa(ri) + " 串第 " + itoa(oi) + " 步（" + o.Op + "）" }
			switch o.Op {
			case "round":
				s.RecordRound(o.P, now)
			case "forced":
				s.RecordForced(now)
			case "reply":
				s.RecordReply(now)
			case "close":
				s.CloseRound()
			case "turn":
				s.RecordTurn(o.Spoke, now)
			case "eval":
				var want struct {
					Enter                                          bool
					Probability, Threshold, Expected, Target, Keep float64
					Actual                                         int
				}
				if err := json.Unmarshal(o.RawOut, &want); err != nil {
					t.Fatal(err)
				}
				d := s.Evaluate(o.P, o.Frequency, now, o.Jitter)
				if d.Enter != want.Enter || d.Actual != want.Actual || !near(d.Threshold, want.Threshold) || !near(d.Expected, want.Expected) ||
					!near(d.Target, want.Target) || !near(d.Keep, want.Keep) || !near(d.Probability, want.Probability) {
					t.Fatalf("%s 的判断不一样：Go %+v，Bun %+v", where(), d, want)
				}
				evals++
			case "backoff":
				var want float64
				_ = json.Unmarshal(o.RawOut, &want)
				if got := s.BackoffLeft(now, o.Pending, o.Called); got.Milliseconds() != int64(want) {
					t.Fatalf("%s 还要等多久不一样：Go %v，Bun %v ms", where(), got, want)
				}
				backoffs++
			case "burst":
				var want float64
				_ = json.Unmarshal(o.RawOut, &want)
				if got := s.BurstWait(now); got.Milliseconds() != int64(want) {
					t.Fatalf("%s 合并一串要等多久不一样：Go %v，Bun %v ms", where(), got, want)
				}
				bursts++
			default:
				t.Fatalf("不认识的操作 %q", o.Op)
			}
			// 每一步之后的状态都要一样（包括 eval 里 Prune 掉的）
			if !reflect.DeepEqual(norm(s), norm(o.State)) {
				t.Fatalf("%s 之后的状态不一样：\nGo  %+v\nBun %+v", where(), s, o.State)
			}
		}
	}
	if evals < 100 || backoffs < 30 || bursts < 30 {
		t.Fatalf("对拍的样本太少：判断 %d、退避 %d、合并 %d", evals, backoffs, bursts)
	}
}

// norm 让"空切片"和"nil 切片"一样（比较用）
func norm(s GateState) GateState {
	if len(s.Rounds) == 0 {
		s.Rounds = nil
	}
	if len(s.Forced) == 0 {
		s.Forced = nil
	}
	if len(s.Replies) == 0 {
		s.Replies = nil
	}
	return s
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestFuseMatchesBun(t *testing.T) {
	t.Parallel()
	f := loadGateFixture(t)
	for _, c := range f.Fuse {
		if got := FuseCap(c.Frequency); got != c.Cap {
			t.Fatalf("频率 %v 的上限：Go %d，Bun %d", c.Frequency, got, c.Cap)
		}
		if got := FuseBeforeTurn(c.N, c.Frequency, c.Forced); got != c.Why {
			t.Fatalf("频率 %v 已说 %d 条 forced=%v：Go %q，Bun %q", c.Frequency, c.N, c.Forced, got, c.Why)
		}
	}
}

// 规格-群聊闸门 第九节：饿死测试。频率 0.2、一小时内被 @ 3 次、主动预计 8 次，主动的门槛不会到 1（MaiBot 的写法会到 1）
func TestGateDoesNotStarveProactive(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_790_000_000_000)
	s := NewGateState()
	for i := 0; i < 16; i++ { // 16 轮，可能性从 0.2 排到 0.8，合起来预计主动 8 次
		s.RecordRound(0.2+0.6*float64(i)/15, now.Add(-time.Duration(52-i*3)*time.Minute))
		s.CloseRound()
	}
	for i := 0; i < 3; i++ {
		s.RecordForced(now.Add(-time.Duration(40-i*10) * time.Minute))
	}
	d := s.Evaluate(0.9, 0.2, now, 0)
	// 预计 8 + 3×0.91 = 10.73，目标 2.146，被叫的份额 2.73 比目标还大：主动份额按目标的 10% 留（0.2146），不是 0
	if d.Threshold >= 1 || d.Keep <= 0 {
		t.Fatalf("主动的份额被饿死了：门槛 %v，保留比例 %v", d.Threshold, d.Keep)
	}
	if !d.Enter {
		t.Fatalf("可能性 0.9 的主动消息该能进：%+v", d)
	}
	// MaiBot 的写法：份额 = 目标 - 被叫的 = 负数 → 保留比例 0 → 门槛 1。这里拦住"放回 MaiBot 写法"
	if want := 0.1 * d.Target / 8; math.Abs(d.Keep-want) > 1e-12 {
		t.Fatalf("保留比例应该是目标的 10%% 除以主动预计：%v，实际 %v", want, d.Keep)
	}
}

// 规格-群聊闸门 第九节：只收紧。说少了门槛不降；说多了门槛升
func TestGateOnlyTightens(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_790_000_000_000)
	mk := func(replies int) GateState {
		s := NewGateState()
		for i := 0; i < 20; i++ {
			s.RecordRound(0.05+float64(i)*0.04, now.Add(-time.Duration(55-i*2)*time.Minute))
			s.CloseRound()
		}
		for i := 0; i < replies; i++ {
			s.RecordReply(now.Add(-time.Duration(i+1) * time.Minute))
		}
		return s
	}
	none := func() GateDecision { s := mk(0); return s.Evaluate(0.5, 0.5, now, 0) }()
	if none.Target < 3 {
		t.Fatalf("样本不对：目标应该有好几次，实际 %v", none.Target)
	}
	// 说够了：门槛不低于一次没说的时候（说少了不放宽，说够了不会比没说更松）
	enough := func() GateDecision { s := mk(int(math.Ceil(none.Target))); return s.Evaluate(0.5, 0.5, now, 0) }()
	if enough.Threshold < none.Threshold {
		t.Fatalf("说够了门槛不该降：没说 %v，说够 %v", none.Threshold, enough.Threshold)
	}
	// 说多了：门槛升
	over := func() GateDecision { s := mk(int(none.Target) * 3); return s.Evaluate(0.5, 0.5, now, 0) }()
	if over.Threshold <= none.Threshold {
		t.Fatalf("说多了门槛该升：没说 %v，说多了 %v", none.Threshold, over.Threshold)
	}
}

func TestBackoffSchedule(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_790_000_000_000)
	s := NewGateState()
	want := []time.Duration{0, 15 * time.Second, 30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute}
	for i, w := range want {
		s.RecordTurn(false, now)
		if got := s.BackoffLeft(now, 0, false); got != w {
			t.Fatalf("连着第 %d 次看了没说话：该等 %v，实际 %v", i+1, w, got)
		}
	}
	// 攒够 6 条或者有人叫她，就不等
	if got := s.BackoffLeft(now, 6, false); got != 0 {
		t.Fatalf("攒够 6 条该马上看：%v", got)
	}
	if got := s.BackoffLeft(now, 5, true); got != 0 {
		t.Fatalf("被叫到该马上看：%v", got)
	}
	if got := s.BackoffLeft(now, 5, false); got == 0 {
		t.Fatal("只攒了 5 条不该马上看")
	}
	s.RecordTurn(true, now) // 说了话，清零
	if s.Idle != (GateIdle{}) {
		t.Fatalf("说了话该清零：%+v", s.Idle)
	}
}

func TestFrequencyLayers(t *testing.T) {
	t.Parallel()
	loc := time.FixedZone("test", 8*3600)
	at := func(h, m int) time.Time { return time.Date(2026, 10, 27, h, m, 0, 0, loc) }
	conv := "qq:group:100"
	rules := []FreqRule{
		{Conversation: "*", Frequency: 0.5},
		{Conversation: "*", Slot: "22:00-07:00", Frequency: 0.1},
		{Conversation: conv, Frequency: 0.8},
		{Conversation: conv, Slot: "09:00-12:00", Frequency: 0.2},
		{Conversation: conv, Slot: "temp", Frequency: 0, Until: at(15, 0).UnixMilli()},
		{Conversation: "qq:group:200", Frequency: 0.9}, // 别的群的，不该被挑到
	}
	cases := []struct {
		name     string
		now      time.Time
		skipTemp bool
		conv     string
		want     float64
		from     string
	}{
		{"临时的盖过一切", at(10, 0), false, conv, 0, "temporary"},
		{"临时的到点就不算", at(15, 0), false, conv, 0.8, "group"},
		{"这个群的时段盖过这个群全天", at(10, 0), true, conv, 0.2, "slot"},
		{"这个群全天盖过所有群的时段", at(23, 0), true, conv, 0.8, "group"},
		{"所有群的时段（跨午夜）", at(23, 0), true, "qq:group:300", 0.1, "slot"},
		{"所有群的时段（跨午夜，凌晨）", at(3, 0), true, "qq:group:300", 0.1, "slot"},
		{"所有群全天", at(12, 0), true, "qq:group:300", 0.5, "default"},
		{"别的群自己的规则", at(12, 0), true, "qq:group:200", 0.9, "group"},
	}
	for _, c := range cases {
		got := FrequencyFrom(rules, c.conv, c.now, DefaultFrequency, c.skipTemp)
		if got.Value != c.want || got.From != c.from {
			t.Errorf("%s：想要 %v/%s，实际 %v/%s（%s）", c.name, c.want, c.from, got.Value, got.From, got.Label)
		}
	}
	if got := FrequencyFrom(nil, conv, at(12, 0), 0.3, false); got.Value != 0.3 || got.From != "config" {
		t.Errorf("没有规则用配置的默认：%+v", got)
	}
}

func TestGateFeaturesEdge(t *testing.T) {
	t.Parallel()
	now := time.UnixMilli(1_790_000_000_000)
	// 空批次、全是占位、全角问号、空白文字
	if f := GateFeaturesOf(nil, nil, now); f.MediaOnly || f.Question || f.Batch != 0 || f.SinceSelfSec != 3600 {
		t.Fatalf("空批次：%+v", f)
	}
	media := GateFeaturesOf([]GateMsg{{At: now, Text: "[表情包]"}, {At: now, Text: " [图片] "}}, nil, now)
	if !media.MediaOnly {
		t.Fatal("全是占位该算只有图和表情")
	}
	mixed := GateFeaturesOf([]GateMsg{{At: now, Text: "[图片]"}, {At: now, Text: "好看吗？"}}, nil, now)
	if mixed.MediaOnly || !mixed.Question {
		t.Fatalf("有文字有全角问号：%+v", mixed)
	}
	blank := GateFeaturesOf([]GateMsg{{At: now, Text: "  "}}, nil, now)
	if blank.MediaOnly {
		t.Fatal("只有空白不算只有图和表情")
	}
	// 她说的话在批次之后（时间在 now 之后）不算"上次说话"
	f := GateFeaturesOf([]GateMsg{{At: now, Text: "嗯"}}, []GateMsg{{At: now.Add(time.Minute), Text: "x", Self: true}}, now)
	if f.SinceSelfSec != 3600 {
		t.Fatalf("还没发生的不算上次说话：%v", f.SinceSelfSec)
	}
}
