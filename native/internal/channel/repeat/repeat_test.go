package repeat

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/types"
)

// 复读插件的测试：假出口，Batch 手工造（Recent 返回事先放好的"前面的消息"）。号、群、话都是编的。
// 规格-群聊插件 第二节的验收一条一条在这里；和链、入库一站、真出口连起来的行为在 channel 包的 chain_integration_test.go

var t0 = time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)

type send struct {
	Character, Origin string
	Conv              types.Conversation
	Parts             []types.Part
}

// fakeOut 是假出口：记下每次 Send，ctx 到期就拒绝（和真出口一样）
type fakeOut struct {
	mu    sync.Mutex
	sends []send
	err   error
}

func (f *fakeOut) Send(ctx context.Context, character, origin string, conv types.Conversation, parts []types.Part) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return "", f.err
	}
	f.sends = append(f.sends, send{character, origin, conv, parts})
	return fmt.Sprintf("plan-%d", len(f.sends)), nil
}
func (f *fakeOut) Record(context.Context, string, string, types.Conversation, string, string) (string, error) {
	return "blocked", nil
}

func (f *fakeOut) SendOwned(ctx context.Context, character, origin string, conv types.Conversation, parts []types.Part, resources []*types.MediaResource) (string, error) {
	defer func() {
		for _, r := range resources {
			_ = r.Release()
		}
	}()
	return f.Send(ctx, character, origin, conv, parts)
}

type clearGuard struct{}

func (clearGuard) CheckOutbound(context.Context, string, audience.Audience) ([]string, error) {
	return nil, nil
}

func (f *fakeOut) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

func (f *fakeOut) last() send {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.sends[len(f.sends)-1]
}

var grp = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "5001"}

func txt(s string) []types.Seg { return []types.Seg{{Kind: types.SegText, Text: s}} }

func img(hash string) []types.Seg { return []types.Seg{{Kind: types.SegImage, Hash: hash}} }
func mface(hash string) []types.Seg {
	return []types.Seg{{Kind: types.SegEmoji, Hash: hash, Ref: "e-" + hash}}
}

var nextSeq atomic.Int64

// m 造一条别人说的话；who 是 "A" 这样的短名，变成 qq:A
func m(who string, segs []types.Seg) types.Msg {
	return types.Msg{Seq: nextSeq.Add(1), Time: t0, Who: "qq:" + who, Segs: segs}
}

// self 造她自己说的一条（Self 为真，没有段）
func self(origin string, at time.Time) types.Msg {
	return types.Msg{Seq: nextSeq.Add(1), Time: at, Who: "ca", Self: true, Origin: origin}
}

// batch 造一批：prev 是这一批之前的消息，news 是这一批新到的
func batch(prev []types.Msg, news ...types.Msg) types.Batch {
	return types.Batch{
		Conv: grp, Character: "ca", Account: "20001", Now: t0, Trigger: types.TriggerMessage, Msgs: news,
		Recent: func(n int) ([]types.Msg, error) {
			if len(prev) > n {
				return prev[len(prev)-n:], nil
			}
			return prev, nil
		},
	}
}

func newRepeater(t *testing.T, tweak func(*Config)) (*Repeater, *fakeOut) {
	t.Helper()
	out := &fakeOut{}
	cfg := Config{Out: out, Rand: func() float64 { return 0 }}
	cfg.Media = func(_ context.Context, _ types.Conversation, hash string) (*types.MediaResource, error) {
		return types.MemoryMedia(hash, "image/png", 1, 1, []byte{1}), nil
	}
	cfg.GuardFor = func(string) Guard { return clearGuard{} }
	cfg.Audience = func(c types.Conversation) (audience.Audience, error) {
		return audience.Audience{Channel: c.Channel, Scene: c.Scene, Conversation: c.String()}, nil
	}
	if tweak != nil {
		tweak(&cfg)
	}
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return r, out
}

// noCooldown：一个测试里要连着跟好几次的，关掉冷却（冷却自己有专门的测试）
func noCooldown(c *Config) { c.Cooldown = func(types.Conversation) time.Duration { return 0 } }

func run(r *Repeater, b types.Batch) types.Verdict {
	return r.Interceptor().Intercept(context.Background(), b)
}

func wantTaken(t *testing.T, v types.Verdict, out *fakeOut, sends int) {
	t.Helper()
	if !v.Taken || out.count() != sends {
		t.Fatalf("该接了，发了 %d 次：%+v", out.count(), v)
	}
}

func wantPassed(t *testing.T, v types.Verdict, out *fakeOut, sends int, reasonHas string) {
	t.Helper()
	if v.Taken || out.count() != sends {
		t.Fatalf("不该接，发了 %d 次（想要 %d）：%+v", out.count(), sends, v)
	}
	if !strings.Contains(v.Reason, reasonHas) {
		t.Fatalf("原因里该有 %q：%q", reasonHas, v.Reason)
	}
}

// ---------- 规格-群聊插件 第二节的验收 ----------

// 两人发同一句 → 跟 1 次；跟的是原话，来源是 automation:repeater，发给这个角色在这个会话里
func TestTwoPeopleSaySameSentenceFollowOnce(t *testing.T) {
	r, out := newRepeater(t, nil)
	v := run(r, batch([]types.Msg{m("A", txt("今天好冷"))}, m("B", txt("今天好冷"))))
	wantTaken(t, v, out, 1)
	s := out.last()
	if s.Character != "ca" || s.Origin != "automation:repeater" || s.Conv != grp || len(s.Parts) != 1 || !reflect.DeepEqual(s.Parts[0], types.Message("今天好冷")) {
		t.Fatalf("发出去的：%+v", s)
	}
	if strings.Contains(v.Reason, "今天好冷") {
		t.Fatalf("理由里不该有原文：%q", v.Reason)
	}
}

// 第三个人再发 → 不再跟（同一轮只跟一次）；她自己跟的那句不算人数，也不隔断这一串
func TestThirdPersonDoesNotFollowAgain(t *testing.T) {
	r, out := newRepeater(t, nil)
	prev := []types.Msg{m("A", txt("好耶")), m("B", txt("好耶")), self("automation:repeater", t0)}
	v := run(r, batch(prev, m("C", txt("好耶"))))
	wantPassed(t, v, out, 0, "同一轮只跟一次")
	// 第四个、第五个也一样
	prev = append(prev, m("C", txt("好耶")))
	wantPassed(t, run(r, batch(prev, m("D", txt("好耶")))), out, 0, "同一轮只跟一次")
}

// 一个人连发两遍 → 不跟；A 发了两遍以后 B 接上，才算两个人
func TestSamePersonTwiceDoesNotCount(t *testing.T) {
	r, out := newRepeater(t, nil)
	wantPassed(t, run(r, batch([]types.Msg{m("A", txt("哈"))}, m("A", txt("哈")))), out, 0, "")
	wantTaken(t, run(r, batch([]types.Msg{m("A", txt("哈")), m("A", txt("哈"))}, m("B", txt("哈")))), out, 1)
}

// 头一个说的人不算复读
func TestFirstSpeakerIsNotARepeat(t *testing.T) {
	r, out := newRepeater(t, nil)
	wantPassed(t, run(r, batch([]types.Msg{m("A", txt("别的话"))}, m("B", txt("新话")))), out, 0, "只有一个人")
}

// 概率没命中以后第三个人再发 → 也不跟（这一轮掷过了）；掷了多少、概率多少都写进原因
func TestAfterMissedRollThirdPersonDoesNotFollow(t *testing.T) {
	r, out := newRepeater(t, func(c *Config) {
		c.Chance = func(types.Conversation) float64 { return 0.5 }
		c.Rand = func() float64 { return 0.9 }
	})
	prev := []types.Msg{m("A", txt("乐"))}
	v := run(r, batch(prev, m("B", txt("乐"))))
	wantPassed(t, v, out, 0, "概率没命中：掷出 0.90，概率 0.50")
	prev = append(prev, m("B", txt("乐")))
	wantPassed(t, run(r, batch(prev, m("C", txt("乐")))), out, 0, "同一轮只跟一次")
}

// 概率：掷出的数小于概率才跟；默认 100%；0 就是不跟
func TestChance(t *testing.T) {
	for _, tc := range []struct {
		name   string
		chance func(types.Conversation) float64
		roll   float64
		want   bool
	}{
		{"默认 100%，掷到最大也跟", nil, 0.999999, true},
		{"一半，掷 0.3 跟", func(types.Conversation) float64 { return 0.5 }, 0.3, true},
		{"一半，掷 0.5 不跟（小于才跟）", func(types.Conversation) float64 { return 0.5 }, 0.5, false},
		{"0 永远不跟", func(types.Conversation) float64 { return 0 }, 0, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, out := newRepeater(t, func(c *Config) { c.Chance = tc.chance; c.Rand = func() float64 { return tc.roll } })
			v := run(r, batch([]types.Msg{m("A", txt("乐"))}, m("B", txt("乐"))))
			if v.Taken != tc.want || (out.count() == 1) != tc.want {
				t.Fatalf("%+v 发了 %d 次", v, out.count())
			}
		})
	}
}

// 概率可以按会话覆盖
func TestChancePerConversation(t *testing.T) {
	r, out := newRepeater(t, func(c *Config) {
		c.Chance = func(conv types.Conversation) float64 {
			if conv.ID == "5002" {
				return 0
			}
			return 1
		}
	})
	b := batch([]types.Msg{m("A", txt("乐"))}, m("B", txt("乐")))
	b.Conv = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "5002"}
	wantPassed(t, run(r, b), out, 0, "概率没命中")
	wantTaken(t, run(r, batch([]types.Msg{m("A", txt("乐"))}, m("B", txt("乐")))), out, 1)
}

// 同一张图（哈希一样）→ 跟，发的是同一张图；同一个表情包 → 跟；一张当图片发、一张当表情发也算一样，发的是最后那条的样子
func TestSameImageAndEmoji(t *testing.T) {
	r, out := newRepeater(t, noCooldown)
	wantTaken(t, run(r, batch([]types.Msg{m("A", img("h1"))}, m("B", img("h1")))), out, 1)
	if p := out.last().Parts[0]; p.Kind != types.PartMessage || p.Body[0].Kind != types.BodyImage || p.Body[0].Media.ID != "h1" {
		t.Fatalf("图：%+v", p)
	}
	wantTaken(t, run(r, batch([]types.Msg{m("A", mface("h2"))}, m("B", mface("h2")))), out, 2)
	if p := out.last().Parts[0]; p.Body[0].Media.ID != "h2" {
		t.Fatalf("表情包：%+v", p)
	}
	wantTaken(t, run(r, batch([]types.Msg{m("A", img("h3"))}, m("B", mface("h3")))), out, 3)
	if p := out.last().Parts[0]; p.Body[0].Media.ID != "h3" {
		t.Fatalf("发的是最后那条的样子：%+v", p)
	}
	// 哈希不一样的图不是一样的
	wantPassed(t, run(r, batch([]types.Msg{m("A", img("h4"))}, m("B", img("h5")))), out, 3, "只有一个人")
	// 文字和图不会相等，哪怕文字恰好是哈希的样子
	wantPassed(t, run(r, batch([]types.Msg{m("A", txt("h6"))}, m("B", img("h6")))), out, 3, "只有一个人")
}

// 没接媒体资源服务（Config.Media 为空）时，图片和表情包复读停在这里、写明原因；生产里接了（app/channel.go，主人 2026-11-08 同意复读发图）
func TestMediaRepeatStaysOffWithoutResourceService(t *testing.T) {
	r, out := newRepeater(t, func(c *Config) { c.Media = nil; noCooldown(c) })
	wantPassed(t, run(r, batch([]types.Msg{m("A", img("h1"))}, m("B", img("h1")))), out, 0, "复读媒体资源服务未接入")
	wantPassed(t, run(r, batch([]types.Msg{m("A", mface("h2"))}, m("B", mface("h2")))), out, 0, "复读媒体资源服务未接入")
	wantTaken(t, run(r, batch([]types.Msg{m("A", txt("乐"))}, m("B", txt("乐")))), out, 1)
}

// 哈希为空的图（迁移来的旧段、还没算出来的）不可比：两张空哈希的图不复读，原因写明
func TestEmptyHashImagesAreNotComparable(t *testing.T) {
	r, out := newRepeater(t, nil)
	wantPassed(t, run(r, batch([]types.Msg{m("A", img(""))}, m("B", img("")))), out, 0, "哈希")
	wantPassed(t, run(r, batch([]types.Msg{m("A", img("h1"))}, m("B", img("")))), out, 0, "哈希")
	wantPassed(t, run(r, batch([]types.Msg{m("A", mface(""))}, m("B", mface("")))), out, 0, "哈希")
	// 空哈希的图隔断一串：A 的 h1 被它隔在前面，B、D 紧挨着的两张 h1 才算这一轮的两个人
	wantTaken(t, run(r, batch([]types.Msg{m("A", img("h1")), m("C", img("")), m("B", img("h1"))}, m("D", img("h1")))), out, 1)
}

// @、引用、语音、文件、卡片、小黄脸、转发、认不出的、混排的：不复读，也不 panic
func TestNonRepeatableSegments(t *testing.T) {
	for name, segs := range map[string][]types.Seg{
		"@":    {{Kind: types.SegAt, Target: "20001"}, {Kind: types.SegText, Text: "喂"}},
		"引用":   {{Kind: types.SegReply, Target: "9"}, {Kind: types.SegText, Text: "喂"}},
		"语音":   {{Kind: types.SegVoice}},
		"文件":   {{Kind: types.SegFile}},
		"卡片":   {{Kind: types.SegCard, Raw: "{}"}},
		"小黄脸":  {{Kind: types.SegFace, Ref: "14"}},
		"超级表情": {{Kind: types.SegSuperface, Ref: "1"}},
		"视频":   {{Kind: types.SegVideo}},
		"转发":   {{Kind: types.SegForward, Ref: "f"}},
		"认不出":  {{Kind: types.SegOther, Raw: "?"}},
		"文字加图": {{Kind: types.SegText, Text: "看"}, {Kind: types.SegImage, Hash: "h1"}},
		"两张图":  {{Kind: types.SegImage, Hash: "h1"}, {Kind: types.SegImage, Hash: "h1"}},
		"空消息":  {},
		"全是空白": {{Kind: types.SegText, Text: " \n "}},
	} {
		t.Run(name, func(t *testing.T) {
			r, out := newRepeater(t, nil)
			wantPassed(t, run(r, batch([]types.Msg{m("A", segs)}, m("B", segs))), out, 0, "")
		})
	}
}

// 文字比的时候去掉首尾空白、连续空白合成一个；但字不一样就是不一样
func TestWhitespaceNormalisation(t *testing.T) {
	r, out := newRepeater(t, noCooldown)
	wantTaken(t, run(r, batch([]types.Msg{m("A", txt("  吃了  没\n"))}, m("B", txt("吃了 没")))), out, 1)
	if got := types.BodyTextOf(out.last().Parts[0].Body); got != "吃了 没" {
		t.Fatalf("发的是最后那条整理过首尾的原话：%q", got)
	}
	wantPassed(t, run(r, batch([]types.Msg{m("A", txt("吃了没"))}, m("B", txt("吃了 没")))), out, 1, "")
	// 文字被适配器拆成几段也是一句
	wantTaken(t, run(r, batch([]types.Msg{m("A", []types.Seg{{Kind: types.SegText, Text: "吃了"}, {Kind: types.SegText, Text: "没"}})}, m("B", txt("吃了没")))), out, 2)
}

// 冷却：跟过以后 3 分钟内同一个群不再跟，原因里写"冷却中"；冷却一过又可以跟；别的群不受影响
func TestCooldown(t *testing.T) {
	r, out := newRepeater(t, nil)
	wantTaken(t, run(r, batch([]types.Msg{m("A", txt("一"))}, m("B", txt("一")))), out, 1)

	b := batch([]types.Msg{m("A", txt("二"))}, m("B", txt("二")))
	b.Now = t0.Add(time.Minute)
	v := run(r, b)
	wantPassed(t, v, out, 1, "冷却中")
	if !strings.Contains(v.Reason, "60 秒前") || !strings.Contains(v.Reason, "还要等 120 秒") {
		t.Fatalf("冷却的原因要说清上次多久以前、还要等多久：%q", v.Reason)
	}

	b2 := batch([]types.Msg{m("A", txt("二"))}, m("B", txt("二")))
	b2.Conv = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "5002"}
	b2.Now = t0.Add(time.Minute)
	wantTaken(t, run(r, b2), out, 2)

	b3 := batch([]types.Msg{m("A", txt("三"))}, m("B", txt("三")))
	b3.Now = t0.Add(DefaultCooldown)
	wantTaken(t, run(r, b3), out, 3)
}

// 冷却可以按会话调；调成 0 就是不冷却
func TestCooldownIsConfigurable(t *testing.T) {
	r, out := newRepeater(t, func(c *Config) { c.Cooldown = func(types.Conversation) time.Duration { return 10 * time.Second } })
	wantTaken(t, run(r, batch([]types.Msg{m("A", txt("一"))}, m("B", txt("一")))), out, 1)
	b := batch([]types.Msg{m("A", txt("二"))}, m("B", txt("二")))
	b.Now = t0.Add(11 * time.Second)
	wantTaken(t, run(r, b), out, 2)

	r2, out2 := newRepeater(t, func(c *Config) { c.Cooldown = func(types.Conversation) time.Duration { return 0 } })
	wantTaken(t, run(r2, batch([]types.Msg{m("A", txt("一"))}, m("B", txt("一")))), out2, 1)
	wantTaken(t, run(r2, batch([]types.Msg{m("A", txt("二"))}, m("B", txt("二")))), out2, 2)
}

// ---------- ADR-171 补充的几条 ----------

// 同音：主人确认过的写法算一样（连起来的也在一组）；不在表里的不算
func TestHomophoneTable(t *testing.T) {
	r, out := newRepeater(t, func(c *Config) {
		noCooldown(c)
		c.Homophones = [][]string{{"我超", "我草"}, {"我超", "握草"}, {"烧", "扫"}}
	})
	wantTaken(t, run(r, batch([]types.Msg{m("A", txt("我超"))}, m("B", txt("我草")))), out, 1)
	wantTaken(t, run(r, batch([]types.Msg{m("A", txt("我草"))}, m("B", txt("握草")))), out, 2) // 两条都挂在"我超"上，互相也一样
	wantPassed(t, run(r, batch([]types.Msg{m("A", txt("我超"))}, m("B", txt("我炒")))), out, 2, "")
	wantPassed(t, run(r, batch([]types.Msg{m("A", txt("烧"))}, m("B", txt("我超")))), out, 2, "")
	// 同音只在紧挨着的一串里并：A 说"我超"，中间别人说了别的，B 说"我草"，不跟
	wantPassed(t, run(r, batch([]types.Msg{m("A", txt("我超")), m("C", txt("无关"))}, m("B", txt("我草")))), out, 2, "")
}

// 读音钩子：读音键相同（且不是空串）才算一样；"算了 / 爽了"这种平翘舌和 l/n 的差别，由钩子决定不并，插件只比钩子的结果
func TestSoundHook(t *testing.T) {
	sound := map[string]string{"是的": "shi de", "湿的": "shi de", "算了": "suan le", "爽了": "shuang le", "空": ""}
	r, out := newRepeater(t, func(c *Config) { noCooldown(c); c.Sound = func(s string) string { return sound[s] } })
	wantTaken(t, run(r, batch([]types.Msg{m("A", txt("是的"))}, m("B", txt("湿的")))), out, 1)
	wantPassed(t, run(r, batch([]types.Msg{m("A", txt("算了"))}, m("B", txt("爽了")))), out, 1, "")
	wantPassed(t, run(r, batch([]types.Msg{m("A", txt("空"))}, m("B", txt("不知道")))), out, 1, "") // 两边都空读音不能当一样
	wantPassed(t, run(r, batch([]types.Msg{m("A", txt("未知1"))}, m("B", txt("未知2")))), out, 1, "")
	// 图片不过读音钩子
	wantPassed(t, run(r, batch([]types.Msg{m("A", img("x"))}, m("B", txt("x")))), out, 1, "")
}

// 没给读音钩子：拼音不比（Go 里还没有汉字转拼音的库），只有字和同音表
func TestNoSoundHookMeansNoPinyin(t *testing.T) {
	r, out := newRepeater(t, nil)
	wantPassed(t, run(r, batch([]types.Msg{m("A", txt("是的"))}, m("B", txt("湿的")))), out, 0, "")
}

// 只跟最近的：前面复读、后面又聊了别的 → 不跟
func TestOnlyFollowTheLatest(t *testing.T) {
	r, out := newRepeater(t, noCooldown)
	// 前面两个人复读过了，这一批是新的话
	wantPassed(t, run(r, batch([]types.Msg{m("A", txt("乐")), m("B", txt("乐"))}, m("C", txt("别的话")))), out, 0, "")
	// 同一批里：A、B 复读以后 C 聊了别的 → 不跟
	wantPassed(t, run(r, batch(nil, m("A", txt("乐")), m("B", txt("乐")), m("C", txt("别的话")))), out, 0, "")
	// 同一批里 A、B 复读以后 C 也跟着复读 → 这一轮还是最近的，跟
	wantTaken(t, run(r, batch(nil, m("A", txt("乐")), m("B", txt("乐")), m("C", txt("乐")))), out, 1)
	// 同一批里 A、B 复读以后 C 发了个没法比的（语音）→ 也是聊了别的
	wantPassed(t, run(r, batch(nil, m("A", txt("笑")), m("B", txt("笑")), m("C", []types.Seg{{Kind: types.SegVoice}}))), out, 1, "")
	// 一批里有两轮：第一轮被打断、第二轮是最近的 → 跟第二轮
	wantTaken(t, run(r, batch(nil, m("A", txt("甲")), m("B", txt("甲")), m("C", txt("乙")), m("D", txt("乙")))), out, 2)
	if got := types.BodyTextOf(out.last().Parts[0].Body); got != "乙" {
		t.Fatalf("该跟最近的那一轮：%q", got)
	}
}

// 被叫到的批次不接（复读是跟着群友一起搞，被叫到就该回答）
func TestAddressedBatchIsNotTaken(t *testing.T) {
	r, out := newRepeater(t, nil)
	b := batch([]types.Msg{m("A", txt("乐"))}, m("B", txt("乐")))
	b.Addressed = true
	wantPassed(t, run(r, b), out, 0, "叫她")
}

// 她自己的消息不算人数，也不隔断一串
func TestHerOwnMessagesAreIgnored(t *testing.T) {
	r, out := newRepeater(t, nil)
	// A 说了，她插了一句别的，B 说同一句：算两个人
	wantTaken(t, run(r, batch([]types.Msg{m("A", txt("乐")), self("character", t0)}, m("B", txt("乐")))), out, 1)
	// 她自己发的和群友说的同一句，不算一个人：A 说了，她说了同一句，A 又说一遍 → 还是一个人
	r2, out2 := newRepeater(t, nil)
	wantPassed(t, run(r2, batch([]types.Msg{m("A", txt("乐")), self("character", t0)}, m("A", txt("乐")))), out2, 0, "")
	// 一批里只有她自己的
	wantPassed(t, run(r2, batch(nil, self("character", t0))), out2, 0, "没有别人说的话")
}

// 别的角色的消息算一个人（它是群里的一员）
func TestOtherCharacterCountsAsAPerson(t *testing.T) {
	r, out := newRepeater(t, nil)
	other := types.Msg{Seq: 1, Time: t0, Who: "cb", Segs: txt("乐")}
	wantTaken(t, run(r, batch([]types.Msg{other}, m("B", txt("乐")))), out, 1)
}

// ---------- 重启、多角色、出错 ----------

// 重启以后冷却从最近的消息里重建：自己上次跟的（automation:repeater）还在冷却里就不跟；已经过了就跟；试运行的认不出来，不算
func TestCooldownRebuiltAfterRestart(t *testing.T) {
	followed := func(ago time.Duration, origin string) []types.Msg {
		return []types.Msg{m("A", txt("旧话")), self(origin, t0.Add(-ago)), m("A", txt("新话"))}
	}
	for _, tc := range []struct {
		name   string
		prev   []types.Msg
		follow bool
	}{
		{"一分钟前跟过：还在冷却", followed(time.Minute, "automation:repeater"), false},
		{"五分钟前跟过：过了", followed(5*time.Minute, "automation:repeater"), true},
		{"一分钟前的试运行：重启以后认不出，当没跟过", followed(time.Minute, "automation:dry-run"), true},
		{"一分钟前她自己正常说的话不是复读", followed(time.Minute, "character"), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, out := newRepeater(t, nil) // 全新的实例，等于重启
			v := run(r, batch(tc.prev, m("B", txt("新话"))))
			if v.Taken != tc.follow || (out.count() == 1) != tc.follow {
				t.Fatalf("%+v 发了 %d 次", v, out.count())
			}
			if !tc.follow && !strings.Contains(v.Reason, "冷却中") {
				t.Fatalf("原因：%q", v.Reason)
			}
		})
	}
}

// 重启以后"这一轮"也能从库里重建：重启前 A 说过，重启后 B 接上，算两个人
func TestRoundRebuiltAfterRestart(t *testing.T) {
	r, out := newRepeater(t, nil)
	wantTaken(t, run(r, batch([]types.Msg{m("A", txt("乐"))}, m("B", txt("乐")))), out, 1)
	// 重启以后：前两个人和她跟的那句都在库里，第三个人不跟
	r2, out2 := newRepeater(t, nil)
	prev := []types.Msg{m("A", txt("乐")), m("B", txt("乐")), self("automation:repeater", t0.Add(-time.Hour))}
	wantPassed(t, run(r2, batch(prev, m("C", txt("乐")))), out2, 0, "同一轮只跟一次")
}

// 同一个群两个角色都开了复读：一个跟了，另一个在冷却里，不会两个都跟（冷却记在插件实例里，所有角色共用）
func TestTwoCharactersInOneGroupDoNotBothFollow(t *testing.T) {
	r, out := newRepeater(t, nil)
	prev := []types.Msg{m("A", txt("乐"))}
	news := m("B", txt("乐"))
	b1, b2 := batch(prev, news), batch(prev, news)
	b2.Character, b2.Account = "cb", "20002"
	wantTaken(t, run(r, b1), out, 1)
	wantPassed(t, run(r, b2), out, 1, "冷却中")
}

// 出口发不出去（渠道断开、发送检查没过、档位变了）：不算接了，这一批照常往下走；冷却不记，下次可以再跟
func TestSendFailureIsNotTaken(t *testing.T) {
	r, out := newRepeater(t, nil)
	out.err = errors.New("发送检查没过：风控间隔")
	v := run(r, batch([]types.Msg{m("A", txt("乐"))}, m("B", txt("乐"))))
	wantPassed(t, v, out, 0, "风控间隔")
	out.err = nil
	wantTaken(t, run(r, batch([]types.Msg{m("A", txt("乐"))}, m("B", txt("乐")))), out, 1)
}

// 链取消了 ctx（超时、渠道关了）以后，插件不能再通过出口发东西
func TestNoSendAfterContextDone(t *testing.T) {
	r, out := newRepeater(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	v := r.Interceptor().Intercept(ctx, batch([]types.Msg{m("A", txt("乐"))}, m("B", txt("乐"))))
	if v.Taken || out.count() != 0 {
		t.Fatalf("ctx 到期以后不该发：%+v %d", v, out.count())
	}
}

// 读不到最近的消息、一批里没有消息：当没接，不 panic
func TestDegenerateBatches(t *testing.T) {
	r, out := newRepeater(t, nil)
	b := batch(nil, m("B", txt("乐")))
	b.Recent = func(int) ([]types.Msg, error) { return nil, errors.New("库坏了") }
	wantPassed(t, run(r, b), out, 0, "库坏了")
	wantPassed(t, run(r, batch(nil)), out, 0, "没有消息")
	b = batch(nil, m("B", txt("乐")))
	b.Recent = nil
	wantPassed(t, run(r, b), out, 0, "没有消息")
}

// 往回看的条数有上限：Recent 被问的是 30 条
func TestAsksForBoundedHistory(t *testing.T) {
	r, _ := newRepeater(t, nil)
	asked := 0
	b := batch(nil, m("B", txt("乐")))
	b.Recent = func(n int) ([]types.Msg, error) { asked = n; return nil, nil }
	run(r, b)
	if asked != 30 {
		t.Fatalf("问了 %d 条", asked)
	}
}

// 很长的一串复读里第三个人、第三十个人都不会再跟，也不会因为窗口截断而误判成"又是两个人"
func TestLongRunStaysQuiet(t *testing.T) {
	r, out := newRepeater(t, nil)
	var prev []types.Msg
	for i := 0; i < 60; i++ {
		prev = append(prev, m(fmt.Sprintf("P%d", i), txt("乐")))
	}
	wantPassed(t, run(r, batch(prev, m("Z", txt("乐")))), out, 0, "同一轮只跟一次")
	// 一个人把同一句连发 60 遍，窗口里只有他一个；再来一个别人 → 看到的只有一个人，跟（这是窗口的局限，也是正确的：整串里确实只有他一个）
	prev = nil
	for i := 0; i < 60; i++ {
		prev = append(prev, m("A", txt("乐")))
	}
	wantTaken(t, run(r, batch(prev, m("B", txt("乐")))), out, 1)
}

// 多个会话同时跑（链按会话排队，不同会话是并发的）：-race 下不出数据竞争，各会话各自冷却
func TestConcurrentConversations(t *testing.T) {
	r, out := newRepeater(t, nil)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			b := batch([]types.Msg{m("A", txt("乐"))}, m("B", txt("乐")))
			b.Conv = types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: fmt.Sprintf("%d", 6000+i)}
			if v := run(r, b); !v.Taken {
				t.Errorf("会话 %d：%+v", i, v)
			}
		}(i)
	}
	wg.Wait()
	if out.count() != 20 {
		t.Fatalf("20 个会话各跟一次：%d", out.count())
	}
}

// 插件声明：名字（也是 automation:<名字> 和开关的键）、优先级 20、任何渠道的群、只关心消息
func TestInterceptorDeclaration(t *testing.T) {
	r, _ := newRepeater(t, nil)
	p := r.Interceptor()
	if p.Name != "repeater" || p.Priority != 20 || len(p.Triggers) != 1 || p.Triggers[0] != types.TriggerMessage || len(p.Occasions) != 1 || p.Occasions[0] != (types.Occasion{Scene: types.SceneGroup}) {
		t.Fatalf("%+v", p)
	}
	if len(p.DefaultOn) != 0 {
		t.Fatalf("复读在哪里都默认关，由主人按群打开：%+v", p.DefaultOn)
	}
	if _, err := New(Config{}); err == nil {
		t.Fatal("没有出口该报错")
	}
}
