// Package loadstate 是"加载占位什么时候出现、出现了留多久、旧数据怎么留"的公共状态机，各页面共用，不要每页自己写计时。
// 它只管时间和状态，不画东西、不依赖任何界面库（新界面和旧页面都能用，ADR-322）；数据本身仍由页面自己存。
//
// 规则（照通行做法，数值和出处见下面两个常量）：
//  1. 开始加载后，过 ShowDelay 数据还没到，才画骨架屏（没有旧数据）或角落的刷新标记（有旧数据）；阈值内到了就全程不画。
//  2. 占位一旦画出来，至少留 MinShow 再撤，不让它刚出现就消失。
//  3. 刷新、切筛选、切角色时有旧数据就继续显示旧数据（Begin 前别清数据），新数据到了原地替换；只有确实没有数据时才走骨架。
//  4. 失败时旧数据不清，标"过期"；没有旧数据才算出错。
//
// 时间由调用方每帧传入（界面里用 c.Now()），本包不读时钟，测试里用可控的时间推进。
package loadstate

import "time"

const (
	// ShowDelay 开始加载后多久才允许出现加载占位。
	// 参照：交互时延的通行分档把 0.1 秒当"即时"、1 秒当"思路不被打断"（Nielsen，Response Times），点一下之后正常取数是几十毫秒，
	// 远低于 0.1 秒的不该有任何反馈；React 生态里专门防转圈闪烁的 spin-delay 默认 delay 500 毫秒、minDuration 200 毫秒
	// （smeijer/spin-delay，src/index.ts，2026-11-08 核对）。本项目取 300 毫秒：比 spin-delay 早一点出反馈，仍落在 0.1 到 1 秒之间。
	// React 自己没有"延迟多久才出兜底"的常量（Suspense 靠 transition 保留旧界面），这里不拿它当出处。
	// 没有核对 Telegram、iOS / macOS 各自写死的数值。
	ShowDelay = 300 * time.Millisecond

	// MinShow 占位出现后至少留多久再撤。出处：React 调度里的 FALLBACK_THROTTLE_MS 就是 300 毫秒：
	// "兜底刚出现过，300 毫秒内不揭示真内容，免得连着闪出很多加载状态"
	// （facebook/react，packages/react-reconciler/src/ReactFiberWorkLoop.js，2026-11-08 核对）；spin-delay 的 minDuration 是 200 毫秒。
	// 取 300，与 ShowDelay 对称，两个数一起好记、一起调。
	MinShow = 300 * time.Millisecond
)

// Loader 是一块数据的加载状态。零值可用（没有数据、没在加载）。只在界面线程用，不加锁。
type Loader struct {
	// Delay、Min 为 0 时用 ShowDelay、MinShow；测试或个别场合要改时填
	Delay, Min time.Duration

	loading bool
	has     bool // 页面手里有能显示的数据（旧的也算）
	stale   bool // 最近一次读失败，旧数据还在
	failed  bool // 最近一次读失败，而且没有旧数据

	started bool // 这一轮的起点已记下（第一次 View 时记，起点就是页面真正能看到它的那一帧）
	startAt time.Time

	shown     bool // 占位（骨架或标记）正在画
	shownAt   time.Time
	shownSkel bool // 画出来的是骨架（没有数据），不是角落的标记
	held      bool // 读完了，但占位还没留够 MinShow
}

// View 是这一帧页面该怎么画。
type View struct {
	// Skeleton：画骨架屏，不画内容（没有可显示的数据，或骨架还没留够 MinShow）
	Skeleton bool
	// Waiting：在加载、还没到阈值、也没有数据：什么都不画，但位置要占住（占位的尺寸和骨架、真实内容一致，不让布局跳）
	Waiting bool
	// Refreshing：有数据在显示，后台在刷新且已过阈值（或还没留够 MinShow）：角落放不打扰的小标记，内容不动
	Refreshing bool
	// Stale：最近一次读失败，显示的是旧数据，页面应标"过期"
	Stale bool
	// Failed：读失败而且没有任何可显示的数据，页面画出错状态
	Failed bool
	// Next 大于 0 表示这么久以后状态会变，页面要安排一帧（界面里 c.After(Next)）；0 表示不用等
	Next time.Duration
}

func (l *Loader) delay() time.Duration {
	if l.Delay > 0 {
		return l.Delay
	}
	return ShowDelay
}

func (l *Loader) minShow() time.Duration {
	if l.Min > 0 {
		return l.Min
	}
	return MinShow
}

// Begin 开始一轮读取。已经在读就什么都不做（计时不重来：连敲搜索词、连点筛选是同一段"忙"）。
// 调之前不要清页面手里的数据；真的要换成另一件东西（点了另一条记录），先 Reset 再 Begin。
func (l *Loader) Begin() {
	if l.loading {
		return
	}
	l.loading = true
	l.started = false
	l.held = false // 上一轮占位还在留，就接着留，不重新数阈值（shown 保持）
}

// Succeed 读到了：数据已由页面存好。占位若已画出，会再留到够 MinShow 才撤。
func (l *Loader) Succeed() {
	l.finish()
	l.has, l.stale, l.failed = true, false, false
}

// Fail 读失败：有旧数据就留着并标过期，没有才算出错。
func (l *Loader) Fail() {
	l.finish()
	if l.has {
		l.stale, l.failed = true, false
	} else {
		l.stale, l.failed = false, true
	}
}

func (l *Loader) finish() {
	l.loading = false
	l.started = false
	l.held = l.shown
}

// Cancel 这一轮读取作废了（离开页面、被新请求顶掉又不再读）：不动数据，占位立刻撤。
func (l *Loader) Cancel() {
	l.loading, l.started, l.held, l.shown = false, false, false, false
}

// Reset 手里的数据作废（换成了另一件东西，旧的不能再当它的内容显示）；不动正在进行的读取和占位计时。
func (l *Loader) Reset() {
	l.has, l.stale, l.failed = false, false, false
}

// Loading 现在是否有读取在途。
func (l *Loader) Loading() bool { return l.loading }

// HasData 页面手里是否有能显示的数据。
func (l *Loader) HasData() bool { return l.has }

// View 算这一帧的画法。now 是这一帧的时间；每帧调一次，调用本身会推进"起点""占位已出现"这两个记号。
func (l *Loader) View(now time.Time) View {
	v := View{Stale: l.stale, Failed: l.failed}
	switch {
	case l.loading:
		if !l.started {
			l.started, l.startAt = true, now
		}
		if !l.shown {
			if wait := l.startAt.Add(l.delay()).Sub(now); wait > 0 {
				v.Next = wait
				v.Waiting = !l.has
				return v
			}
			l.shown, l.shownAt, l.shownSkel = true, now, !l.has
		}
		if !l.has {
			l.shownSkel = true // 占位出来以后数据被换掉了：从角落标记升级成骨架
		}
	case l.held:
		rest := l.shownAt.Add(l.minShow()).Sub(now)
		if rest <= 0 {
			l.held, l.shown = false, false
			return v
		}
		v.Next = rest
	default:
		return v
	}
	if l.shownSkel {
		v.Skeleton = true
	} else {
		v.Refreshing = true
	}
	return v
}
