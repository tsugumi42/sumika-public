package winshell

import (
	"time"

	"github.com/egoist/mygo/ui"
)

// 错开淡入（设计语言第九节）：一组兄弟元素进场时，每个比前一个晚一点点开始，淡入加很小的上移。
// 一组的总时长封顶 StaggerCap，条目再多整组也在这个时间内放完；只有前 StaggerMax 个参与，其余直接出现。
//
// 取值：
//   - 每项自己淡入的时长 = motion.fast（140 毫秒），透明度带 NonMotion（减少动态效果时淡入照常）；
//   - 相邻两项晚开始的时间 staggerStep = 30 毫秒（mygo 文档 docs/ui/motion.md “Delays and staggering” 的示例值）；
//   - 上移 = travel.menu（6，和菜单进场同一档），只用 Translate，不动布局；减少动态效果时不上移；
//   - 组的总时长 = 最后一项晚开始的时间 + 每项淡入时长，≤ StaggerCap（任务给定的 300 毫秒）：知道一共几项时把步长压到
//     (StaggerCap − 每项时长) ÷ (参与项数 − 1) 以内；不知道几项（n 传 0）时按“晚开始的时间最多 StaggerCap − 每项时长”封顶。

const (
	// StaggerMax 是错开淡入最多让几项参与；第 StaggerMax 项（从 0 数起）及以后直接出现。
	StaggerMax = 12
	// StaggerCap 是一整组错开淡入的总时长上限。
	StaggerCap = 300 * time.Millisecond
	// staggerStep 是相邻两项晚开始的时间。
	staggerStep = 30 * time.Millisecond
)

// staggerEach 是每项自己淡入的时长（令牌 motion.fast）。
func staggerEach() time.Duration { return base.Motion.MotionFast }

// StaggerDelay 是第 i 项（从 0 数起）晚开始多久，n 是一共几项（不知道传 0）。不参与的项返回 -1。未按慢放倍数换算。
func StaggerDelay(i, n int) time.Duration {
	if i < 0 || i >= StaggerMax {
		return -1
	}
	room := StaggerCap - staggerEach() // 最后一项最晚能晚开始多久
	step := staggerStep
	if m := min(n, StaggerMax); m > 1 && time.Duration(m-1)*step > room {
		step = room / time.Duration(m-1)
	}
	return min(time.Duration(i)*step, room)
}

// StaggerTotal 是 n 项的一整组从开始到放完的时长（未按慢放倍数换算），总是 ≤ StaggerCap。
func StaggerTotal(n int) time.Duration {
	last := min(n, StaggerMax) - 1
	if last < 0 {
		return 0
	}
	return StaggerDelay(last, n) + staggerEach()
}

// StaggerIn 让 e 作为一组兄弟里的第 i 个进场：先全透明，晚 StaggerDelay(i, n) 开始，淡入并从下面上移一点。
// play 为假时，第一次出现的 e 直接就位（页面刚打开、不是用户换了什么，不放没人触发的动画）；
// 已经开始淡入的 e 不管 play 怎么变都会放完。e 必须有 key。
//
// 为什么状态驱动而不用 Transition 的 Enter：换分类、下拉面板展开时，这些元素和它们的父元素常常是同一帧新出现的，Enter 不播
// （和 popIn、fadeIn 同一个原因）。第一次见到的那一帧先 Jump 到 0，再带 Delay 走向 1。
func (k *Kit) StaggerIn(c *ui.Context, e ui.Element, i, n int, play bool) ui.Element {
	phase := ui.Local(e, "stagger-phase", func() int { return 0 }) // 0 没见过，1 在放，2 放完了
	if *phase == 0 {
		delay := StaggerDelay(i, n)
		if !play || delay < 0 {
			*phase = 2
			return e
		}
		*phase = 1
		e.Jump("stagger", 0)
	}
	if *phase != 1 {
		return e
	}
	delay := max(StaggerDelay(i, n), 0)
	a := ui.Animation{Duration: slowDur(staggerEach()), Ease: ui.EaseOut, Delay: slowDur(delay), NonMotion: true}
	v := e.AnimateTo("stagger", 1, a)
	if v >= 1 {
		*phase = 2
		return e
	}
	e.Opacity(v)
	if !c.Preferences().ReduceMotion {
		e.Translate(0, (1-v)*float32(base.Num.TravelMenu))
	}
	return e
}

// staggerRun 是一组里“现在排到第几个”的计数，SettingsPane 在摆内容时挂在 Kit 上，SettingGroup 取号。
type staggerRun struct {
	play bool
	next int
}

// takeStagger 给下一个元素取序号；没有在错开淡入的摆放里时返回 -1。
func (k *Kit) takeStagger() (i int, play bool) {
	if k.stag == nil {
		return -1, false
	}
	i = k.stag.next
	k.stag.next++
	return i, k.stag.play
}
