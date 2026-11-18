package goal

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// DecisionKind 是轮末处理的结果种类。
type DecisionKind int

const (
	// Idle：这个会话没有进行中的目标（或处理期间被主人暂停、清除了），宿主什么都不用做。
	Idle DecisionKind = iota
	// Continue：没达成，宿主把 Prompt 当作下一轮的输入自动发出（界面上显示成灰色系统行）。
	Continue
	// Achieved：达成，目标已清除，界面显示“目标达成：Reason”。
	Achieved
	// Paused：暂停，Reason 是原因，等主人点“继续”。
	Paused
)

// Decision 是 AfterTurn 的返回值。Goal 是处理后的目标快照（达成时 Status 为 done）。
type Decision struct {
	Kind   DecisionKind
	Prompt string
	Reason string
	Goal   Goal
}

// Config 是 Manager 的配置，零值可用。
type Config struct {
	DefaultMaxRounds int   // 默认 30
	DefaultMaxTokens int64 // 默认 0，不限
	MaxJudgeFails    int   // 连续裁判出错几次就暂停，默认 3
	Now              func() time.Time
	// IsStuck 判断她这一轮的回答是不是在说卡住；为空用内置的几个明确说法。
	// 裁判返回 blocked 也算卡住。
	IsStuck func(answer string) bool
}

// Manager 管所有会话的目标。方法并发安全；裁判调用期间不持锁。
type Manager struct {
	store Store
	judge Judge
	cfg   Config
	mu    sync.Mutex
}

// NewManager 建管理器，并把存储里“进行中”的目标改成“暂停”（进程重启后不自己接着跑）。
// 返回的错误只说明个别目标文件读写失败，Manager 仍可用。
func NewManager(store Store, model Completer, cfg Config) (*Manager, error) {
	if cfg.DefaultMaxRounds <= 0 {
		cfg.DefaultMaxRounds = DefaultMaxRounds
	}
	if cfg.MaxJudgeFails <= 0 {
		cfg.MaxJudgeFails = DefaultMaxFails
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.IsStuck == nil {
		cfg.IsStuck = defaultStuck
	}
	m := &Manager{store: store, judge: Judge{Model: model}, cfg: cfg}
	return m, m.recover()
}

func (m *Manager) recover() error {
	ids, err := m.store.List()
	if err != nil {
		return err
	}
	var errs []error
	for _, id := range ids {
		g, err := m.store.Load(id)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if g == nil || g.Status != StatusActive {
			continue
		}
		g.Status, g.PauseReason = StatusPaused, "程序重启，目标已暂停"
		if err := m.store.Save(g); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

// Set 给会话设目标（替换已有的），状态为进行中。目标原文不能为空。
func (m *Manager) Set(session, text string) (Goal, error) {
	text = strings.TrimSpace(text)
	if session == "" || text == "" {
		return Goal{}, errors.New("会话号和目标原文都不能为空")
	}
	step := Budget{MaxRounds: m.cfg.DefaultMaxRounds, MaxTokens: m.cfg.DefaultMaxTokens}
	g := &Goal{Session: session, Text: text, StartedAt: m.cfg.Now(), Status: StatusActive, Budget: step, Step: step}
	m.mu.Lock()
	defer m.mu.Unlock()
	return *g, m.store.Save(g)
}

// Edit 改目标原文（/goal edit）：轮数、已用 token、预算和进行中还是暂停都保持，只换文字，
// 上一次的判断和卡住、出错的计数清零（它们是针对旧目标说的）。会话没有目标就当设新目标，created 为真。
func (m *Manager) Edit(session, text string) (g Goal, created bool, err error) {
	text = strings.TrimSpace(text)
	if session == "" || text == "" {
		return Goal{}, false, errors.New("会话号和目标原文都不能为空")
	}
	err = m.update(session, func(x *Goal) error {
		x.Text = text
		x.Last, x.JudgeFails, x.StuckStreak, x.HasLastRound = nil, 0, 0, false
		g = *x
		return nil
	})
	if errors.Is(err, ErrNoGoal) {
		g, err = m.Set(session, text)
		return g, true, err
	}
	return g, false, err
}

// SetBudget 改这个目标的预算（0 表示不限；步长同步更新）。
func (m *Manager) SetBudget(session string, b Budget) error {
	return m.update(session, func(g *Goal) error {
		g.Budget, g.Step = b, b
		return nil
	})
}

// Sessions 列出现在有目标的会话号（界面的状态汇总用）。
func (m *Manager) Sessions() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	ids, _ := m.store.List()
	return ids
}

// Get 返回会话当前的目标。
func (m *Manager) Get(session string) (Goal, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.store.Load(session)
	if err != nil || g == nil {
		return Goal{}, false
	}
	return *g, true
}

// Clear 清除目标（/goal clear）。有目标返回 true。
func (m *Manager) Clear(session string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.store.Load(session)
	if err != nil || g == nil {
		return false, err
	}
	return true, m.store.Delete(session)
}

// Pause 暂停（主人点“停止”、她在等主人回答时调）。已经不是进行中则不改。
func (m *Manager) Pause(session, reason string) error {
	return m.update(session, func(g *Goal) error {
		if g.Status == StatusActive {
			g.Status, g.PauseReason = StatusPaused, reason
		}
		return nil
	})
}

// Resume 继续。清掉卡住、出错等计数；预算到顶的那一项按步长再续一段。
// 返回的目标由宿主发一句“继续”之类的话作为这一轮的输入。
func (m *Manager) Resume(session string) (Goal, error) {
	var out Goal
	err := m.update(session, func(g *Goal) error {
		if g.Status != StatusPaused {
			return errors.New("目标不在暂停状态")
		}
		g.Status, g.PauseReason = StatusActive, ""
		g.JudgeFails, g.StuckStreak, g.Wrapping, g.HasLastRound = 0, 0, false, false
		if g.Budget.MaxRounds > 0 && g.Rounds >= g.Budget.MaxRounds {
			g.Budget.MaxRounds = g.Rounds + max(g.Step.MaxRounds, 1)
		}
		if g.Budget.MaxTokens > 0 && g.TokensUsed >= g.Budget.MaxTokens {
			g.Budget.MaxTokens = g.TokensUsed + max(g.Step.MaxTokens, 1)
		}
		out = *g
		return nil
	})
	return out, err
}

// ErrNoGoal 表示这个会话没有目标。
var ErrNoGoal = errors.New("这个会话没有目标")

func (m *Manager) update(session string, f func(*Goal) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.store.Load(session)
	if err != nil {
		return err
	}
	if g == nil {
		return ErrNoGoal
	}
	if err := f(g); err != nil {
		return err
	}
	return m.store.Save(g)
}

// 续轮和收尾提示。
const (
	continueFmt = "目标还没达成（理由：%s）。接着做；同一个阻碍连着三轮都过不去再说卡住，并说清卡在哪、需要所有者做什么。"
	wrapFmt     = "预算到了（已跑 %d 轮，已用 %d token）。这一轮先收尾：写清做到哪、还剩什么、下一步该怎么做，不要再开新的工作。"
)

// AfterTurn 在每轮结束后调用，返回宿主下一步要做什么。
// 处理期间若会话被暂停、清除或换了新目标，返回 Idle，不改任何东西。
func (m *Manager) AfterTurn(ctx context.Context, session string, r TurnResult) Decision {
	snap, ok := m.Get(session)
	if !ok || snap.Status != StatusActive {
		return Decision{Kind: Idle}
	}
	if r.NeedsUser {
		return m.finish(session, snap.StartedAt, func(g *Goal) Decision {
			count(g, r)
			return pause(g, "她在等主人回答，回复后点“继续”")
		})
	}

	v, jerr := m.judge.Judge(ctx, JudgeInput{Goal: snap.Text, Answer: r.Answer, Tools: r.Tools})
	if jerr != nil && errors.Is(context.Cause(ctx), ErrSuperseded) {
		return Decision{Kind: Idle}
	}

	return m.finish(session, snap.StartedAt, func(g *Goal) Decision {
		count(g, r)
		now := m.cfg.Now()
		switch {
		case jerr != nil && ctx.Err() != nil:
			return pause(g, "已中止")
		case errors.Is(jerr, ErrUnavailable):
			g.Last = &Judgement{Reason: jerr.Error(), At: now, Failed: true}
			return pause(g, jerr.Error())
		case jerr != nil:
			g.JudgeFails++
			g.Last = &Judgement{Reason: "裁判出错：" + jerr.Error(), At: now, Failed: true}
			if g.JudgeFails >= m.cfg.MaxJudgeFails {
				return pause(g, fmt.Sprintf("裁判连续出错 %d 次：%v", g.JudgeFails, jerr))
			}
			v = Verdict{Reason: "这一轮裁判没能给出判断，先当没达成"}
		default:
			g.JudgeFails = 0
			g.Last = &Judgement{Achieved: v.Achieved, Reason: v.Reason, At: now}
			if v.Achieved {
				g.Status = StatusDone
				_ = m.store.Delete(g.Session)
				return Decision{Kind: Achieved, Reason: v.Reason}
			}
		}

		// 兜底一：连着两轮说卡住。
		if v.Blocked || m.cfg.IsStuck(r.Answer) {
			g.StuckStreak++
		} else {
			g.StuckStreak = 0
		}
		if g.StuckStreak >= 2 {
			return pause(g, "她连着两轮说卡住了："+v.Reason)
		}
		// 兜底二：连着两轮没有工具调用、回答也几乎一样。
		ans := clip(r.Answer, maxAnswerRunes)
		if g.HasLastRound && len(r.Tools) == 0 && g.LastToolCnt == 0 && similar(ans, g.LastAnswer) {
			return pause(g, "连着两轮没有动作、回答也几乎一样，像是在空转")
		}
		g.LastAnswer, g.LastToolCnt, g.HasLastRound = ans, len(r.Tools), true

		// 预算：先给一轮收尾，收尾那一轮结束后再暂停。
		if g.Wrapping {
			return pause(g, fmt.Sprintf("预算已到，收尾轮已结束（已跑 %d 轮，已用 %d token）", g.Rounds, g.TokensUsed))
		}
		if overBudget(g) {
			g.Wrapping = true
			return Decision{Kind: Continue, Prompt: fmt.Sprintf(wrapFmt, g.Rounds, g.TokensUsed), Reason: v.Reason}
		}
		return Decision{Kind: Continue, Prompt: fmt.Sprintf(continueFmt, v.Reason), Reason: v.Reason}
	})
}

// finish 重新读出目标（裁判期间可能被改过），确认还是同一个进行中的目标再套用 f，并保存。
func (m *Manager) finish(session string, started time.Time, f func(*Goal) Decision) Decision {
	m.mu.Lock()
	defer m.mu.Unlock()
	g, err := m.store.Load(session)
	if err != nil || g == nil || g.Status != StatusActive || !g.StartedAt.Equal(started) {
		return Decision{Kind: Idle}
	}
	d := f(g)
	if g.Status != StatusDone {
		if err := m.store.Save(g); err != nil {
			g.Status, g.PauseReason = StatusPaused, "保存目标失败："+err.Error()
			d = Decision{Kind: Paused, Reason: g.PauseReason}
		}
	}
	d.Goal = *g
	return d
}

func count(g *Goal, r TurnResult) {
	g.Rounds++
	g.TokensUsed += max(r.Tokens, 0)
}

func pause(g *Goal, reason string) Decision {
	g.Status, g.PauseReason = StatusPaused, reason
	return Decision{Kind: Paused, Reason: reason}
}

func overBudget(g *Goal) bool {
	return g.Budget.MaxRounds > 0 && g.Rounds >= g.Budget.MaxRounds ||
		g.Budget.MaxTokens > 0 && g.TokensUsed >= g.Budget.MaxTokens
}

// stuckPhrases 匹配的是模型说出来的话：她会照角色卡的称呼说（铃音说“需要主人”），所以旧称呼和“所有者”都认（ADR-384）。
var stuckPhrases = []string{"卡住了", "我卡住", "被卡住", "无法继续", "没法继续", "需要所有者", "需要主人", "需要你来", "i'm stuck", "i am stuck", "i'm blocked", "i am blocked"}

func defaultStuck(answer string) bool {
	tail := strings.ToLower(clipTail(answer, 600))
	for _, p := range stuckPhrases {
		if strings.Contains(tail, p) {
			return true
		}
	}
	return false
}

// similar 判断两段回答几乎一样：规整空白后字符二元组的 Jaccard 相似度不低于 0.9。
func similar(a, b string) bool {
	a, b = oneLine(a), oneLine(b)
	if a == b {
		return true
	}
	ga, gb := bigrams(a), bigrams(b)
	if len(ga) == 0 || len(gb) == 0 {
		return false
	}
	inter := 0
	for k := range ga {
		if gb[k] {
			inter++
		}
	}
	return float64(inter)/float64(len(ga)+len(gb)-inter) >= 0.9
}

func bigrams(s string) map[[2]rune]bool {
	r := []rune(s)
	out := map[[2]rune]bool{}
	for i := 0; i+1 < len(r); i++ {
		out[[2]rune{r[i], r[i+1]}] = true
	}
	return out
}
