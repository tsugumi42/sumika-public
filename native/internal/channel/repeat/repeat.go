// Package repeat 是复读插件（规格-渠道中间件 第十二节，规格-群聊插件 第二节，ADR-171）：同一个群里两个不同的人接连发了同一句，她跟着发一遍，
// 不过模型。它是跨平台的拦截插件，任何渠道的群都能跑（现在只有 QQ 有群）；默认全关，由宿主按群打开。
//
// 状态是从库里的最近几条消息算出来的（Batch.Recent），不靠内存里的"这一轮"标记：重启以后照样对，也不会和库里的事实对不上。
// 判法是纯函数——只数"这一条之前，紧挨着的、和它一样的那串消息里，有几个不同的人"：
//   - 正好 1 个，而且这一条的发言人不是他 → 这一条让这一轮到了 2 个人，掷一次概率；
//   - 0 个（头一个说这句）、已经 2 个以上（第三个人接着复读）、或者是同一个人连发 → 不跟。
//
// "同一轮只跟一次"（不管掷中没中，后面再有人接着复读也不再跟）就是这样自然得到的：第三个人发的时候，前面已经有两个人了。
// "只跟最近的"也是：一批里让这一轮到 2 个人的那一条，后面要是又有人聊了别的，就不跟（她看到时已经过去了）。
// 她自己的消息不算人数，也不隔断这一串（Koishi 的复读插件也忽略自己发的）。别的角色的消息算一个人。
//
// 冷却（同一个群跟过以后等 3 分钟）记在这个插件实例里、所有角色共用：同一个群里两个角色都开了复读，一个跟了，另一个在冷却里，不会两个都跟。
// 重启以后从最近的消息里找自己上次跟的时刻（origin = automation:repeater）重建；试运行档的"跟"在库里是 automation:dry-run，
// 分不出是不是复读，所以重启以后试运行的冷却会忘（多跟一次试运行而已）。
package repeat

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
	"time"
	"unicode"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel/types"
)

const (
	// Name 是插件名，也是 automation:<Name> 里的名字和开关的键（规格 第十节"来源"）
	Name = "repeater"
	// Priority 是优先级初值（需求-拦截类插件接口 第四节）：戳回去 10，复读 20
	Priority = 20
	// DefaultCooldown 是同一个群跟过一次以后等多久（规格-群聊插件 第二节，主人 2026-10-14 定的 3 分钟，可调；没有每小时上限）
	DefaultCooldown = 3 * time.Minute
	// recentWindow 是往回看多少条。一串复读再长，也只需要看到"前面有没有两个不同的人"，30 条够；
	// 一个人把同一句连发了 30 遍的话，第 31 条起会看不到别人，不影响判断（看得到的都是他）
	recentWindow = 30
)

// Guard 和回合用同一份记忆守卫；可选的 CheckRepeat 免去确认模型。
type Guard interface {
	CheckOutbound(context.Context, string, audience.Audience) ([]string, error)
}

type RepeatGuard interface {
	CheckRepeat(context.Context, string, audience.Audience) ([]string, error)
}

type Outbox interface {
	types.Outbox
	Record(context.Context, string, string, types.Conversation, string, string) (string, error)
	SendOwned(context.Context, string, string, types.Conversation, []types.Part, []*types.MediaResource) (string, error)
}

// Config 是复读的配置。
type Config struct {
	Out        Outbox // 必填：插件只拿出口，拿不到适配器
	GuardFor   func(character string) Guard
	Audience   func(types.Conversation) (audience.Audience, error)
	CanTrigger func(channel, actor string) bool // 同闸门的名单；不接话的人不能给复读凑人数
	Media      func(context.Context, types.Conversation, string) (*types.MediaResource, error)
	// Chance 是某个会话的复读概率（0 到 1，掷出的数小于它才跟）；nil 就是 100%（规格-群聊插件 第二节：默认 100%，全局一个值，单个群可以覆盖）
	Chance func(types.Conversation) float64
	// Cooldown 是某个会话跟过以后等多久；nil 用 DefaultCooldown
	Cooldown func(types.Conversation) time.Duration
	// Rand 掷概率，返回 [0, 1)；nil 用 math/rand/v2。测试里换成固定的数
	Rand func() float64
	// Homophones 是主人确认过的同音写法：每一组里的写法互相算一样（"我超 / 我草"）。数据不写在代码里，由宿主从配置里读了传进来
	Homophones [][]string
	// Sound 把一段文字读成"读音键"，两句的读音键相同（且不是空串）就算一样。拼音的规矩（不管声调、前后鼻音归一，平翘舌和 l/n 不放宽，
	// 规格-群聊插件 第二节）是它的事。nil 就不比读音：Go 里现在没有汉字转拼音的库（加依赖要写 ADR），所以只有 Homophones 这张表生效
	Sound func(string) string
	// GuardTimeout 是守卫判一句话的整体时限；0 用 10 秒。测试里调短，不用真等满
	GuardTimeout time.Duration
}

// Repeater 是复读插件
type Repeater struct {
	cfg   Config
	class map[string]int // 同音写法 → 它在哪一组（并查集合并过，"烧 / 掃""烧 / 扫"这样连起来的也在一组）

	mu     sync.Mutex
	last   map[string]time.Time // 会话 → 上次跟的时刻；所有角色共用
	loaded map[string]bool      // 这个会话的 last 从库里读过了
}

// New 造复读插件
func New(cfg Config) (*Repeater, error) {
	if cfg.Out == nil {
		return nil, errors.New("复读插件没有出口")
	}
	if cfg.Rand == nil {
		cfg.Rand = rand.Float64
	}
	return &Repeater{cfg: cfg, class: groupHomophones(cfg.Homophones), last: map[string]time.Time{}, loaded: map[string]bool{}}, nil
}

// Interceptor 交给拦截链：任何渠道的群，只关心消息
func (r *Repeater) Interceptor() types.Interceptor {
	return types.Interceptor{
		Name: Name, Priority: Priority,
		Occasions: []types.Occasion{{Scene: types.SceneGroup}},
		Triggers:  []string{types.TriggerMessage},
		Intercept: r.intercept,
	}
}

// groupHomophones 把每一组同音写法并成一类（并查集），返回写法 → 类号。写法先按和比较时一样的规矩整理（空白合成一个、去首尾）
func groupHomophones(groups [][]string) map[string]int {
	parent := map[string]string{}
	var find func(string) string
	find = func(s string) string {
		if parent[s] == "" {
			parent[s] = s
		}
		if parent[s] != s {
			parent[s] = find(parent[s])
		}
		return parent[s]
	}
	for _, g := range groups {
		for i := 1; i < len(g); i++ {
			a, b := normText(g[0]), normText(g[i])
			if a != "" && b != "" {
				parent[find(a)] = find(b)
			}
		}
	}
	class, ids := map[string]int{}, map[string]int{}
	for s := range parent {
		root := find(s)
		if _, ok := ids[root]; !ok {
			ids[root] = len(ids) + 1
		}
		class[s] = ids[root]
	}
	return class
}

// normText：去掉首尾空白，连续的空白合成一个（规格-群聊插件 第二节"怎么算一样"）
func normText(s string) string {
	return strings.Join(strings.FieldsFunc(s, unicode.IsSpace), " ")
}

// unit 只复读单一内容；媒体按内容哈希比较，发送时向宿主申请资源。
type unit struct {
	media bool   // 图片、表情包（按文件内容的哈希比）；否则是文字
	key   string // 文字是整理过的字，图片 / 表情包是哈希
	part  types.Part
	desc  string // 写进日志的样子：只有种类和字数，不放原文
}

// unitOf 把一条消息的段变成 unit；ok 为假时 why 是不复读的原因
func unitOf(segs []types.Seg) (u unit, why string, ok bool) {
	var text strings.Builder
	var media []types.Seg
	for _, s := range segs {
		switch s.Kind {
		case types.SegText:
			text.WriteString(s.Text)
		case types.SegImage, types.SegEmoji:
			media = append(media, s)
		case types.SegFace, types.SegSuperface:
			return unit{}, "含小黄脸，出口还不会发，先不复读", false
		case types.SegAt, types.SegReply, types.SegVoice, types.SegFile, types.SegCard:
			return unit{}, "含 @、引用、语音、文件、卡片之类，不复读", false
		default:
			return unit{}, "含复读不了的段（" + s.Kind + "），不复读", false
		}
	}
	trimmed := strings.TrimSpace(text.String())
	switch {
	case len(media) == 0 && trimmed == "":
		return unit{}, "没有字", false
	case len(media) == 0:
		n := len([]rune(trimmed))
		return unit{key: normText(trimmed), part: types.Message(trimmed), desc: fmt.Sprintf("文字 %d 字", n)}, "", true
	case trimmed != "" || len(media) > 1:
		return unit{}, "混排的消息（文字加图，或不止一张图）出口发不成一条，不复读", false
	}
	m := media[0]
	if m.Hash == "" {
		// 迁移来的旧图、还没算出哈希的：两张空哈希的图不能当成一样（规格 第十二节，清单⑯）
		return unit{}, "图片还没算出哈希，不能比", false
	}
	return unit{media: true, key: m.Hash, part: types.Part{Kind: types.PartMessage, Body: []types.BodySegment{{Kind: types.BodyImage}}}, desc: "图片或表情包"}, "", true
}

// same 说两个 unit 是不是"一样"：图片和表情包按哈希比，一张当图片发、一张当表情发也算一样；文字整理过以后相同，或者在同一组同音写法里，
// 或者读音键相同
func (r *Repeater) same(a, b unit) bool {
	if a.media || b.media {
		return a.media && b.media && a.key == b.key
	}
	if a.key == b.key {
		return true
	}
	if ca, cb := r.class[a.key], r.class[b.key]; ca != 0 && ca == cb {
		return true
	}
	if r.cfg.Sound != nil {
		if sa := r.cfg.Sound(a.key); sa != "" && sa == r.cfg.Sound(b.key) {
			return true
		}
	}
	return false
}

// item 是序列里的一条别人说的话
type item struct {
	who string
	u   unit
	ok  bool   // 能比
	why string // 不能比的原因
}

// intercept 是拦截函数
func (r *Repeater) intercept(ctx context.Context, b types.Batch) types.Verdict {
	if b.Addressed {
		return types.Passed("这一批里有人叫她，复读不接") // 复读是跟着群友一起搞，被叫到就该回答（规格 第十九节 B10，我的建议）
	}
	if len(b.Msgs) == 0 || b.Recent == nil {
		return types.Passed("这一批没有消息")
	}
	prev, err := b.Recent(recentWindow)
	if err != nil {
		return types.Passed("读不到最近的消息：" + err.Error())
	}
	r.warm(b.Conv, prev)

	// 前面的加这一批的，去掉她自己说的（不算人数，也不隔断）
	var items []item
	batchStart := 0
	for i, m := range append(append([]types.Msg(nil), prev...), b.Msgs...) {
		if i == len(prev) {
			batchStart = len(items)
		}
		if m.Self {
			continue
		}
		it := item{who: m.Who}
		if r.cfg.CanTrigger != nil && !r.cfg.CanTrigger(b.Conv.Channel, m.Who) {
			it.why = "不接话名单或身份缺失，不参与复读"
		} else {
			it.u, it.why, it.ok = unitOf(m.Segs)
		}
		items = append(items, it)
	}
	if len(items) == batchStart {
		return types.Passed("这一批里没有别人说的话")
	}

	hit := -1
	for i := batchStart; i < len(items); i++ {
		if r.makesTwo(items, i) && r.stillLatest(items, i) {
			hit = i
		}
	}
	if hit < 0 {
		return types.Passed(r.whyNot(items))
	}
	u := items[hit].u

	conv := b.Conv
	cooldown := DefaultCooldown
	if r.cfg.Cooldown != nil {
		cooldown = r.cfg.Cooldown(conv)
	}
	if left, in := r.coolingDown(conv, b.Now, cooldown); in {
		return types.Passed(fmt.Sprintf("冷却中：上次跟是 %d 秒前，还要等 %d 秒（%s）", int((cooldown-left)/time.Second), int(left/time.Second), u.desc))
	}

	chance := 1.0
	if r.cfg.Chance != nil {
		chance = r.cfg.Chance(conv)
	}
	roll := r.cfg.Rand()
	if roll >= chance {
		return types.Passed(fmt.Sprintf("概率没命中：掷出 %.2f，概率 %.2f（%s）", roll, chance, u.desc))
	}

	if !u.media {
		if reason := r.guard(ctx, b.Character, conv, types.BodyTextOf(u.part.Body)); reason != "" {
			if _, err := r.cfg.Out.Record(ctx, b.Character, types.OriginAutomationPrefix+Name, conv, "blocked", reason); err != nil {
				return types.Passed(reason + "，记录没写成")
			}
			return types.Passed(reason)
		}
	}

	var planID string
	if u.media {
		if r.cfg.Media == nil {
			return types.Passed("复读媒体资源服务未接入")
		}
		resource, e := r.cfg.Media(ctx, conv, u.key)
		if e != nil {
			return types.Passed("复读媒体不可读")
		}
		u.part.Body[0].Media = resource
		if resource.MIME == "image/gif" {
			u.part.Body[0].Kind = types.BodyGIF
		}
		planID, err = r.cfg.Out.SendOwned(ctx, b.Character, types.OriginAutomationPrefix+Name, conv, []types.Part{u.part}, []*types.MediaResource{resource})
	} else {
		planID, err = r.cfg.Out.Send(ctx, b.Character, types.OriginAutomationPrefix+Name, conv, []types.Part{u.part})
	}
	if err != nil {
		// 没发成：不记冷却，也不算接了——这一批照常往下走，不能因为复读坏了让她哑巴
		return types.Passed("想跟，没发出去：" + err.Error())
	}
	r.markFollowed(conv, b.Now)
	return types.Took(fmt.Sprintf("两个人复读了同一句（%s），掷出 %.2f，概率 %.2f，跟了一遍，计划 %s", u.desc, roll, chance, planID))
}

// guard 在 Send 写入原文之前检查；任何没判成的情况都不放行。
func (r *Repeater) guard(ctx context.Context, character string, conv types.Conversation, text string) (reason string) {
	// 取听众、取守卫、检查，哪一步 panic 都是没判成：recover 要罩住整个函数，不然取听众 / 取守卫的函数炸了会冲出去，
	// 既不记「拦下」也不走这里的理由（链外兜底只能当没接）
	defer func() {
		if recover() != nil {
			reason = "守卫没判成"
		}
	}()
	if r.cfg.Audience == nil {
		return "守卫没有听众"
	}
	a, err := r.cfg.Audience(conv)
	if err != nil {
		return "守卫没有听众"
	}
	if r.cfg.GuardFor == nil {
		return "守卫没判成"
	}
	g := r.cfg.GuardFor(character)
	if g == nil {
		return "守卫没判成"
	}
	limit := r.cfg.GuardTimeout
	if limit <= 0 {
		limit = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var refs []string
	if rg, ok := g.(RepeatGuard); ok {
		refs, err = rg.CheckRepeat(ctx, text, a)
	} else {
		refs, err = g.CheckOutbound(ctx, text, a)
	}
	if err != nil || ctx.Err() != nil {
		return "守卫没判成"
	}
	if len(refs) > 0 {
		return "守卫拦下"
	}
	return ""
}

// makesTwo：第 i 条是不是让这一轮到了 2 个人——它能比，紧挨在它前面、和它一样的那串里正好有 1 个不同的人，而且不是它的发言人
func (r *Repeater) makesTwo(items []item, i int) bool {
	cur := items[i]
	if !cur.ok {
		return false
	}
	before := map[string]bool{}
	for j := i - 1; j >= 0; j-- {
		p := items[j]
		if !p.ok || !r.same(p.u, cur.u) {
			break
		}
		before[p.who] = true
	}
	return len(before) == 1 && !before[cur.who]
}

// stillLatest：第 i 条之后，这一批里要是又有人聊了别的（不一样的，或者没法比的），这一轮就过去了，不跟
func (r *Repeater) stillLatest(items []item, i int) bool {
	for _, later := range items[i+1:] {
		if !later.ok || !r.same(items[i].u, later.u) {
			return false
		}
	}
	return true
}

// whyNot 说为什么没跟，写进日志。只看最后一条
func (r *Repeater) whyNot(items []item) string {
	last := items[len(items)-1]
	if !last.ok {
		return last.why
	}
	people := map[string]bool{last.who: true}
	for j := len(items) - 2; j >= 0; j-- {
		p := items[j]
		if !p.ok || !r.same(p.u, last.u) {
			break
		}
		people[p.who] = true
	}
	switch n := len(people); {
	case n == 1:
		return "只有一个人说这句，还不是复读"
	case n == 2:
		return "这一轮的复读不是这条让它到两个人的（同一个人又说了一遍，或者两个人已经复读过了）"
	default:
		return fmt.Sprintf("这一轮已经有 %d 个人复读了，同一轮只跟一次", n)
	}
}

// warm：这个会话还没从库里读过"上次跟是什么时候"就读一次（重启以后重建）。只认真跟的（origin = automation:repeater）
func (r *Repeater) warm(conv types.Conversation, prev []types.Msg) {
	key := conv.String()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.loaded[key] {
		return
	}
	r.loaded[key] = true
	for _, m := range prev {
		if m.Origin == types.OriginAutomationPrefix+Name && m.Time.After(r.last[key]) {
			r.last[key] = m.Time
		}
	}
}

// coolingDown：还在冷却里吗，还差多久
func (r *Repeater) coolingDown(conv types.Conversation, now time.Time, cooldown time.Duration) (left time.Duration, in bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	last, ok := r.last[conv.String()]
	if !ok || cooldown <= 0 {
		return 0, false
	}
	if since := now.Sub(last); since >= 0 && since < cooldown {
		return cooldown - since, true
	}
	return 0, false
}

func (r *Repeater) markFollowed(conv types.Conversation, now time.Time) {
	r.mu.Lock()
	r.last[conv.String()] = now
	r.mu.Unlock()
}
