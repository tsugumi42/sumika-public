// Package remotepick 是“边打字边搜候选”的状态机，不依赖任何界面：设置页里要选地点、模型、会话这类“字打不对”的项，
// 都用它取候选。界面一侧（winshell.Kit.RemotePicker）只负责画。
//
// 规矩（规格-客户端自知与设置工具 第十一节，控件一行）：
//   - 去抖：停手 Debounce 以后才发请求，打字过程中不发；
//   - 取消过期请求：字变了，在途的请求立刻取消，它晚到的结果也丢掉（按代数认，不按“谁先回”）；
//   - 失败有文字：取不到就写一句话，不留空白；字没变不自动重试，点重试或改字才再发；
//   - 清空字或太短：取消在途请求、清掉候选，不发请求。
//
// 状态都在界面线程上读写：请求在另一个 goroutine 里跑，回来的结果经 Post 回到界面线程再进状态。
package remotepick

import (
	"context"
	"strings"
	"time"
)

// Candidate 是一个候选。ID 在同一次查询里唯一；Label 是选中后显示的规范结果；Detail 是候选行上的补充；Data 是调用方带的东西（选中后用）
type Candidate struct {
	ID, Label, Detail string
	Data              any
}

// Fetch 按查询词取候选。ctx 在字变了、关掉、重试时取消；返回的错误文字会原样给人看，所以只写人话，不带网址和内部细节
type Fetch func(ctx context.Context, query string) ([]Candidate, error)

// 默认值
const (
	DefaultDebounce = 300 * time.Millisecond
	DefaultMinRunes = 1
)

// State 是一个搜索框的状态
type State struct {
	Fetch    Fetch
	Post     func(func()) // 把函数交回界面线程；必须给
	Debounce time.Duration
	MinRunes int

	// 下面几项给界面读
	Text    string      // 输入框里的字（界面把输入框绑在它上面；改了字调 Edit 报一声，Edit 比的是上次报的字）
	Items   []Candidate // 最近一次查到的候选（对应 Settled）
	Loading bool        // 请求在途
	Err     string      // 最近一次失败的文字；有新请求或字变了就清掉
	Settled string      // Items 对应的查询词；空 = 还没查成过

	seen     string // 上次 Edit 报的字
	want     string // 想查的词（去掉首尾空白）
	edited   time.Time
	inflight string
	failed   string // 失败过的词：字不变不自动重试
	seq      uint64
	cancel   context.CancelFunc
}

func (s *State) debounce() time.Duration {
	if s.Debounce > 0 {
		return s.Debounce
	}
	return DefaultDebounce
}

func (s *State) minRunes() int {
	if s.MinRunes > 0 {
		return s.MinRunes
	}
	return DefaultMinRunes
}

// Edit 报告输入框的字变成了 text。重复报同一个字没有影响
func (s *State) Edit(now time.Time, text string) {
	s.Text = text
	if text == s.seen {
		return
	}
	s.seen = text
	q := strings.TrimSpace(text)
	if q == s.want {
		return
	}
	s.want, s.edited = q, now
	s.Err, s.failed = "", ""
	s.abort() // 字变了：在途的请求作废
	if len([]rune(q)) < s.minRunes() {
		s.want, s.Items, s.Settled = "", nil, ""
	}
}

// abort 取消在途请求，并让它晚到的结果被丢掉
func (s *State) abort() {
	s.seq++
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.Loading, s.inflight = false, ""
}

// Step 每帧调一次。到了去抖时间就发请求；返回下次要醒来的间隔（0 = 不用）。
// 候选里留着上一个词的结果直到新结果回来，不先清空：先清空再出来会让下拉闪一下
func (s *State) Step(now time.Time) time.Duration {
	if s.want == "" || s.want == s.Settled || s.want == s.inflight || s.want == s.failed || s.Fetch == nil {
		return 0
	}
	if left := s.debounce() - now.Sub(s.edited); left > 0 {
		return left
	}
	s.start()
	return 0
}

func (s *State) start() {
	s.abort()
	q := s.want
	ctx, cancel := context.WithCancel(context.Background())
	s.cancel, s.inflight, s.Loading, s.Err = cancel, q, true, ""
	seq, fetch, post := s.seq, s.Fetch, s.Post
	go func() {
		items, err := fetch(ctx, q)
		if ctx.Err() != nil {
			return // 已经作废了，不用回
		}
		post(func() { s.deliver(seq, q, items, err) })
	}()
}

func (s *State) deliver(seq uint64, q string, items []Candidate, err error) {
	if seq != s.seq {
		return // 过期的
	}
	s.cancel, s.Loading, s.inflight = nil, false, ""
	if err != nil {
		s.failed = q
		s.Err = err.Error()
		if strings.TrimSpace(s.Err) == "" {
			s.Err = "没取到候选"
		}
		return
	}
	s.Items, s.Settled, s.failed = items, q, ""
}

// Retry 失败后再发一次（字不变）
func (s *State) Retry() {
	if s.want == "" {
		return
	}
	s.failed, s.Err, s.edited = "", "", time.Time{}
}

// Empty 查过了，没有结果（界面写“没有匹配的地点”）
func (s *State) Empty() bool {
	return s.Settled != "" && s.Settled == s.want && len(s.Items) == 0 && !s.Loading && s.Err == ""
}

// Reset 清掉字、候选、在途请求（选中以后、关掉下拉时用）
func (s *State) Reset() {
	s.abort()
	s.Text, s.seen, s.want, s.Items, s.Settled, s.Err, s.failed = "", "", "", nil, "", "", ""
}

// Close 关页面时取消在途请求
func (s *State) Close() { s.abort() }
