package channel

// 群聊闸门的算式（规格-群聊闸门 第三、四节，规格-渠道中间件 第八节），从 daemon/util/gate.ts、daemon/util/fuse.ts 搬来的纯函数：
// 一批消息"该接的可能性"、一小时窗口里的门槛、空闲退避、保险丝、频率三层挑哪一条。不碰库、不碰钟，状态能存成 JSON 重启后接着算
// （JSON 的键和 Bun 的 gate_state 一样，同一份状态两边读得懂）。有状态的闸门在 gate.go。
//
// 结构照 MaiBot 的动态频率（docs/archive/notes/MaiBot-动态频率.md），改掉规格 3.3 的几个坑：主动份额至少留目标的 10%（不会被 @ 多了饿死）；
// 连着的 @ 1 分钟内合成一轮；状态存库；门槛加一点抖动（ADR-077）；不用 MaiBot 的静态表，轮次少的时候也按这个群自己的轮次算（ADR-079）。

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// GateWeights 是"该接的可能性"的权重：借来的，不是我们自己拟合的（规格 3.1）。MaiBot 用它自己"不设门控时 Planner 首轮"的记录拟合，
// 来源 MaiBot f9ec367 src/maisaka/turn_trigger/reply_likelihood.py。攒够两周我们自己的判断记录（gate_decisions）后重拟合
type GateWeights struct {
	Intercept, Mention, AtOther, Question, MediaOnly, SelfRatio, RecentCount, LogSinceSelf, Batch float64
}

// BorrowedWeights 是借来的那一组
var BorrowedWeights = GateWeights{
	Intercept:    -1.2005,
	Mention:      1.5852,  // 新消息里叫了她
	AtOther:      -1.1157, // @ 了别人
	Question:     0.4807,  // 带问号
	MediaOnly:    -1.0281, // 全是表情、图片这类占位
	SelfRatio:    2.6525,  // 最近 5 分钟她说的占比
	RecentCount:  -0.0367, // 最近 5 分钟多少条
	LogSinceSelf: 0.0571,  // ln(1 + 离她上次说话的秒数)
	Batch:        0.1012,  // 这批几条（最多算 20）
}

// 闸门的数（都是 Bun 现值，来源见各行）
const (
	GateWindow       = time.Hour        // 一小时窗口
	GateRound        = 40 * time.Second // 40 秒内到的算一轮，每轮只记一个可能性（MaiBot：按 40 秒切出的轮数和实际判断次数最接近）
	GateForcedExpect = 0.91             // 一次被叫到预计回几次（MaiBot 的实测均值）
	GateBurst        = time.Minute      // 1 分钟内的一串 @ 合成一轮（规格 3.3）
	GateMinProactive = 0.1              // 主动的份额至少留目标的这么多（规格 3.3）
	GateRecent       = 5 * time.Minute  // 最近多久算"最近"
	gateSinceSelf    = 3600.0           // 上下文里没有她说的话时，"离她上次说话"按这么多秒算（和 MaiBot 拟合时一样）
	MaxPending       = 50               // 一次最多看、最多交给她几条（ADR-109 第 4 条）
	EarlierCap       = 10000            // 数"更早的还有几条"最多数到这么多（数满了说"一万条以上"）
	GateJitter       = 0.03             // 门槛抖动的幅度（ADR-077）
	DefaultFrequency = 0.3              // 默认频率（ADR-061）
	FuseBase         = 4                // 保险丝：一小时上限 = round(4 + 16 × 频率)，频率 0 / 0.3 / 1 → 4 / 9 / 20 条（ADR-099）
	FusePerFrequency = 16               // 同上
	FuseForcedFactor = 2                // 被叫到时放宽到上限的几倍才停
	FuseWindow       = time.Hour        // 保险丝按最近多久算
	GateBackoffStart = 2                // 空闲退避（MaiBot 默认）：连着第 2 次看了没说话起等 15 秒，之后翻倍，最多 5 分钟
	GateBackoffBase  = 15 * time.Second // 同上
	GateBackoffCap   = 5 * time.Minute  // 同上
	GateBackoffFree  = 6                // 等的时候攒够 6 条、或者有人叫她就马上看
)

// GateFeatures 是一批消息的 8 个特征
type GateFeatures struct {
	Mention      bool    `json:"mention"`
	AtOther      bool    `json:"atOther"`
	Question     bool    `json:"question"`
	MediaOnly    bool    `json:"mediaOnly"`
	SelfRatio    float64 `json:"selfRatio"`
	RecentCount  int     `json:"recentCount"`
	SinceSelfSec float64 `json:"sinceSelfSec"`
	Batch        int     `json:"batch"`
}

// GateMsg 是一条群消息里闸门要看的
type GateMsg struct {
	At       time.Time
	Text     string
	Self     bool // 是她自己说的
	AtOther  bool // @ 了别人（不是她、不是全体）
	Mentions bool // 叫到了她（@、引用她的话、别名）
}

var mediaRe = regexp.MustCompile(`^\[(表情包|图片|语音|文件|卡片|视频|表情|合并转发)`)

// GateFeaturesOf 算一批新消息（她还没看过的、别人说的）的特征。recent 是这个群最近的消息（含她自己说的），
// 用来算最近 5 分钟的占比和离她上次说话多久
func GateFeaturesOf(batch, recent []GateMsg, now time.Time) GateFeatures {
	var texts []string
	for _, m := range batch {
		if t := strings.TrimSpace(m.Text); t != "" {
			texts = append(texts, t)
		}
	}
	f := GateFeatures{Batch: len(batch), SinceSelfSec: gateSinceSelf}
	for _, m := range batch {
		f.Mention = f.Mention || m.Mentions
		f.AtOther = f.AtOther || m.AtOther
	}
	for _, t := range texts {
		if strings.ContainsAny(t, "?？") {
			f.Question = true
		}
	}
	f.MediaOnly = len(texts) > 0
	for _, t := range texts {
		if !mediaRe.MatchString(t) {
			f.MediaOnly = false
		}
	}
	var self int
	var lastSelf time.Time
	for _, m := range recent {
		d := now.Sub(m.At)
		if d <= GateRecent && d >= 0 {
			f.RecentCount++
			if m.Self {
				self++
			}
		}
		if m.Self && !m.At.After(now) && m.At.After(lastSelf) {
			lastSelf = m.At
		}
	}
	if f.RecentCount > 0 {
		f.SelfRatio = float64(self) / float64(f.RecentCount)
	}
	if !lastSelf.IsZero() {
		f.SinceSelfSec = now.Sub(lastSelf).Seconds()
	}
	return f
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}

// Probability 是这批"该接的可能性"（0～1）
func (w GateWeights) Probability(f GateFeatures) float64 {
	logit := w.Intercept + w.Mention*b2f(f.Mention) + w.AtOther*b2f(f.AtOther) + w.Question*b2f(f.Question) + w.MediaOnly*b2f(f.MediaOnly) +
		w.SelfRatio*math.Min(1, math.Max(0, f.SelfRatio)) + w.RecentCount*math.Max(0, float64(f.RecentCount)) +
		w.LogSinceSelf*math.Log1p(math.Max(0, f.SinceSelfSec)) + w.Batch*math.Min(20, math.Max(0, float64(f.Batch)))
	return 1 / (1 + math.Exp(-logit))
}

// GateIdle 是空闲退避的状态：连着几次看了没说话、退避到什么时候（毫秒）
type GateIdle struct {
	Count int   `json:"count"`
	Until int64 `json:"until"`
}

// GateState 是一个群（一个角色）的窗口状态：能 JSON 存进库，重启后接着算（规格 3.3）。时间都是毫秒
type GateState struct {
	// Rounds 是主动的轮次：[开始时间, 这一轮的可能性]
	Rounds [][2]float64 `json:"rounds"`
	// Forced 是被叫到的强制触发时间（每次预计回 GateForcedExpect 次）
	Forced []int64 `json:"forced"`
	// Replies 是她真的说了话的时间（试运行想说的也算）
	Replies []int64 `json:"replies"`
	// RoundAt 是正开着的那一轮从什么时候开始；nil 是没有
	RoundAt *int64 `json:"roundAt"`
	// LastForcedAt 是上一次强制触发的时间（合并一串 @ 用）
	LastForcedAt *int64   `json:"lastForcedAt"`
	Idle         GateIdle `json:"idle"`
}

// NewGateState 是空状态（切片是空的而不是 nil，存出来是 [] 不是 null，Bun 读得懂）
func NewGateState() GateState {
	return GateState{Rounds: [][2]float64{}, Forced: []int64{}, Replies: []int64{}}
}

func ms(t time.Time) int64 { return t.UnixMilli() }

// Prune 去掉窗口外的
func (s *GateState) Prune(now time.Time) {
	cut := float64(ms(now) - GateWindow.Milliseconds())
	r := s.Rounds[:0]
	for _, x := range s.Rounds {
		if x[0] >= cut {
			r = append(r, x)
		}
	}
	s.Rounds = r
	keep := func(xs []int64) []int64 {
		out := xs[:0]
		for _, t := range xs {
			if float64(t) >= cut {
				out = append(out, t)
			}
		}
		return out
	}
	s.Forced = keep(s.Forced)
	s.Replies = keep(s.Replies)
}

// RecordRound 记这一轮的可能性：这一轮还开着就覆盖（同一轮只记一次），否则开新的一轮
func (s *GateState) RecordRound(p float64, now time.Time) {
	n := ms(now)
	if s.RoundAt != nil && n-*s.RoundAt < GateRound.Milliseconds() && len(s.Rounds) > 0 {
		s.Rounds[len(s.Rounds)-1][1] = p
		return
	}
	s.RoundAt = &n
	s.Rounds = append(s.Rounds, [2]float64{float64(n), p})
}

// CloseRound 放行进她的回合了：这一轮结束，之后到的算下一轮
func (s *GateState) CloseRound() { s.RoundAt = nil }

// RecordForced 记一次被叫到（强制触发）
func (s *GateState) RecordForced(now time.Time) {
	s.CloseRound()
	n := ms(now)
	s.Forced = append(s.Forced, n)
	s.LastForcedAt = &n
}

// RecordReply 记她说了一句
func (s *GateState) RecordReply(now time.Time) { s.Replies = append(s.Replies, ms(now)) }

// BurstWait 是这次被叫到能不能马上进回合：离上一次强制触发不到 1 分钟的，合进那一串，等这一分钟过完再一起看。返回还要等多久，0 是不用等
func (s *GateState) BurstWait(now time.Time) time.Duration {
	if s.LastForcedAt == nil {
		return 0
	}
	if left := *s.LastForcedAt + GateBurst.Milliseconds() - ms(now); left > 0 {
		return time.Duration(left) * time.Millisecond
	}
	return 0
}

// GateDecision 是一次判断的结果和算式里的数（界面悬停看"为什么没接"用，规格 第五节）
type GateDecision struct {
	Enter       bool
	Probability float64
	Threshold   float64
	Expected    float64 // 窗口里不设门槛时预计她会说几次
	Target      float64 // 按频率折算的目标
	Actual      int     // 她这小时实际说了几次
	Keep        float64
}

// Evaluate 说这批消息放不放她进回合（规格 3.2）。jitter 是这一次加在门槛上的抖动（调用方给 ±GateJitter 以内的随机数；测试给 0）。
// 和 MaiBot 的不同：主动份额至少留目标的 10%（频率不是 0 时），所以被 @ 多了也不会把门槛顶到 1
func (s *GateState) Evaluate(p, frequency float64, now time.Time, jitter float64) GateDecision {
	s.Prune(now)
	proactive := make([]float64, len(s.Rounds))
	var expProactive float64
	for i, r := range s.Rounds {
		proactive[i] = r[1]
		expProactive += r[1]
	}
	expForced := float64(len(s.Forced)) * GateForcedExpect
	expected := expProactive + expForced
	actual := len(s.Replies)
	f := math.Min(1, math.Max(0, frequency))
	target := f * expected
	var keep float64
	switch {
	case f >= 1:
		keep = 1
	case f <= 0:
		keep = 0
	case expProactive <= 0:
		keep = f
	default:
		budget := math.Max(target-expForced, GateMinProactive*target)
		feedback := math.Min(1, math.Max(0, 1+(target-float64(actual))/math.Max(1, target))) // 只收紧：说多了往下压，说少了不放宽
		keep = math.Min(1, math.Max(0, (budget/expProactive)*feedback))
	}
	// 轮次少的时候也按这个群自己的轮次算（ADR-079）
	var threshold float64
	switch {
	case keep >= 1:
		threshold = 0
	case keep <= 0:
		threshold = 1
	default:
		threshold = gateCumulative(proactive, keep*expProactive)
		threshold = math.Min(1, math.Max(0, threshold+jitter))
	}
	return GateDecision{Enter: p >= threshold, Probability: p, Threshold: threshold, Expected: expected, Target: target, Actual: actual, Keep: keep}
}

// gateCumulative：概率从高到低累加，加到额度那一刻的概率就是门槛
func gateCumulative(scores []float64, budget float64) float64 {
	xs := append([]float64(nil), scores...)
	sort.Sort(sort.Reverse(sort.Float64Slice(xs)))
	var acc float64
	for _, q := range xs {
		acc += q
		if acc >= budget {
			return q
		}
	}
	return 0
}

// RecordTurn 她进了回合、看完：说了话退避清零；没说话加一档
func (s *GateState) RecordTurn(spoke bool, now time.Time) {
	if spoke {
		s.Idle = GateIdle{}
		return
	}
	count := s.Idle.Count + 1
	var wait time.Duration
	if count >= GateBackoffStart {
		wait = min(GateBackoffCap, GateBackoffBase*time.Duration(1<<(count-GateBackoffStart)))
	}
	s.Idle = GateIdle{Count: count}
	if wait > 0 {
		s.Idle.Until = ms(now) + wait.Milliseconds()
	}
}

// BackoffLeft 说还在不在退避：攒够 6 条、或者有人叫她就不等了。返回还要等多久，0 是不用等
func (s *GateState) BackoffLeft(now time.Time, pending int, called bool) time.Duration {
	if called || pending >= GateBackoffFree || s.Idle.Until <= ms(now) {
		return 0
	}
	return time.Duration(s.Idle.Until-ms(now)) * time.Millisecond
}

// ───────────── 保险丝（ADR-099） ─────────────

// FuseCap 是这个频率下一小时的上限（主动接话到这里先停）
func FuseCap(frequency float64) int {
	return int(math.Round(FuseBase + FusePerFrequency*math.Min(1, math.Max(0, frequency))))
}

// FuseBeforeTurn 说进回合之前：这小时已经说了 n 条，这一轮还进不进。forced 是被叫到。
// 主动的到上限就先不进，被叫到的到上限的 FuseForcedFactor 倍才不进。返回不进的理由，空是进
func FuseBeforeTurn(n int, frequency float64, forced bool) string {
	cap := FuseCap(frequency)
	switch {
	case !forced && n >= cap:
		return fmt.Sprintf("保险丝：这个群最近一小时她说了 %d 条（频率 %s 时主动最多 %d 条），先不主动接，被叫到照样回", n, fmtNum(frequency), cap)
	case forced && n >= cap*FuseForcedFactor:
		return fmt.Sprintf("保险丝：这个群最近一小时她说了 %d 条（到了上限 %d 条的 %d 倍），被叫到也先不接", n, cap, FuseForcedFactor)
	}
	return ""
}

// 数字写成最短的样子（和 JS 一样：0.3 写 0.3，1 写 1，0.28125 写 0.28125）
func fmtNum(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// ───────────── 频率三层（规格-群聊闸门 第四节） ─────────────

// FreqRule 是 gate_rules 里的一行：Conversation 写 "*" 是所有群；Slot 写 "" 是全天，或 "HH:MM-HH:MM"；Until 有值是临时的（到点就不算了）
type FreqRule struct {
	Conversation string
	Slot         string
	Frequency    float64
	Until        int64 // 毫秒；0 是没有
}

// FrequencyFrom 说现在生效的频率和是哪一条规则定的（From：temporary / slot / group / default / config）。
// 层次，下面的盖上面的：临时的 > 这个群的时段 > 这个群全天 > 所有群的时段 > 所有群全天 > 配置的默认。skipTemp 是不看临时的那一层
type FreqPick struct {
	Value float64
	From  string
	Label string
}

var slotRe = regexp.MustCompile(`^(\d\d):(\d\d)-(\d\d):(\d\d)$`)

func inSlot(slot string, hm int) bool {
	m := slotRe.FindStringSubmatch(slot)
	if m == nil {
		return false
	}
	atoi := func(s string) int { n := 0; fmt.Sscanf(s, "%d", &n); return n }
	a, b := atoi(m[1])*60+atoi(m[2]), atoi(m[3])*60+atoi(m[4])
	if a <= b {
		return hm >= a && hm < b
	}
	return hm >= a || hm < b // 跨午夜的时段（22:00-07:00）
}

// FrequencyFrom 从规则里挑现在生效的那一条。rules 只需要含这个群和 "*" 的行，Until 已过期的这里再滤一遍
func FrequencyFrom(rules []FreqRule, conversation string, now time.Time, def float64, skipTemp bool) FreqPick {
	hm := now.Hour()*60 + now.Minute()
	var live []FreqRule
	for _, r := range rules {
		if (r.Conversation == conversation || r.Conversation == "*") && (r.Until == 0 || r.Until > ms(now)) {
			live = append(live, r)
		}
	}
	if !skipTemp {
		for _, r := range live {
			if r.Until != 0 {
				return FreqPick{r.Frequency, "temporary", fmt.Sprintf("临时：%s，到 %s", fmtNum(r.Frequency), time.UnixMilli(r.Until).In(now.Location()).Format("15:04"))}
			}
		}
	}
	for _, c := range []string{conversation, "*"} {
		who := "这个群"
		if c == "*" {
			who = "所有群"
		}
		for _, r := range live {
			if r.Conversation == c && r.Until == 0 && r.Slot != "" && inSlot(r.Slot, hm) {
				return FreqPick{r.Frequency, "slot", fmt.Sprintf("%s %s：%s", who, r.Slot, fmtNum(r.Frequency))}
			}
		}
		for _, r := range live {
			if r.Conversation == c && r.Until == 0 && r.Slot == "" {
				from := "group"
				if c == "*" {
					from = "default"
				}
				return FreqPick{r.Frequency, from, fmt.Sprintf("%s：%s", who, fmtNum(r.Frequency))}
			}
		}
	}
	return FreqPick{def, "config", "默认：" + fmtNum(def)}
}
