package chatui

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// 展开收起（规格-过渡动画 C5、C6、C7，铁律 3、4、5）。
//
// 一块内容的展开收起走同一个函数 disclose：
//   - 内容放进一个固定高度、裁剪的盒子，高度 = 进度 p × 内容自己的高度，p 用“利落”弹簧（不回弹）从 0 走到 1；
//     内容自己的高度读上一帧的 Bounds，盒子里的内容始终按自然高度排好，只是被裁掉一截，所以每一帧列表看到的行高单调变化；
//   - 内容的透明度用时长淡变（展开 140 毫秒淡入，收起约 100 毫秒淡出）；
//   - 铁律 4：只有“这一行的顶部在可见范围里、并且列表没有贴着末尾跟随”才动高度（Host.heightMotionOK），其余时候高度直接切换、内容照样淡入。
//     条件在翻转（展开或收起开始）那一帧判断并记住，动到一半不会因为条件变了而半途硬切；
//   - 第一次画出来就是终点（历史消息、滚出去又回来的行不播）；高度动画用 Spring 不带 NonMotion，系统要求减少动态效果时直接到位，淡变照常。
//
// 状态（弹簧通道、淡变通道）挂在调用方给的 host 元素上，盒子只在展开着或没收完的时候才画，收起后和原来一样什么也不留。

// discState 是一块展开收起内容的状态，调用方放在一个稳定的元素上（ui.Local）。
type discState struct {
	h      ui.Handle
	full   float32 // 内容自然高度（上一帧量到的）
	open   bool    // 上一帧的目标：展开着没有
	known  bool    // 已经画过第一帧
	motion bool    // 这一次翻转要不要动高度（翻转那一帧定下）
	live   bool    // 上一帧盒子还在（展开着或没收完）
}

// Live 说上一帧这块内容还在树里（展开着，或收起还没放完）。
func (d *discState) Live() bool { return d != nil && d.live }

// heightMotionOK 是铁律 4 的条件：宿主说列表没有贴着末尾跟随，并且这一行（上一帧的气泡框 b）的顶部在聊天列可见范围里。
// 宿主没给 FollowingEnd 或 ViewRect 时按贴底算（硬切加淡入，是安全的做法）。
func (h Host) heightMotionOK(b ui.Rect) bool {
	if h.FollowingEnd == nil || h.ViewRect == nil || h.FollowingEnd() {
		return false
	}
	v := h.ViewRect()
	return b.W > 0 && v.W > 0 && b.Y >= v.Y && b.Y < v.Y+v.H
}

// discHeightEps 是高度进度小到这个数就当收完（弹簧收尾的零头）。
const discHeightEps = 0.002

// disclose 在当前位置画一块能展开收起的内容。
//   - host：放弹簧和淡变通道的稳定元素；key 是盒子的键（同一个 host 下唯一）；
//   - open：现在该不该展开；force：这一次翻转无视铁律 4 的条件、一定动高度（审批卡决定后收起：它自己就是要看着变的那一张卡）；
//   - fill：盒子是否铺满父元素的宽（父元素本来就铺满时给 true；气泡那种按内容定宽的父元素给 false）；
//   - body：画内容，只在展开着或没收完时调用。
//
// 返回内容这一帧已经完全收起、没画（调用方可以据此清掉“收起中”的记录）。
func (h Host) disclose(c *ui.Context, host ui.Element, key string, st *discState, open, force, fill bool, body func()) (settled bool) {
	t := h.T
	if !st.known {
		st.known, st.open = true, open
	}
	if open != st.open {
		st.open = open
		st.motion = force || h.motionOK
	}
	target := float32(0)
	if open {
		target = 1
	}
	pk, ok := "disc-p:"+key, "disc-o:"+key
	var p float32
	if st.motion {
		p = max(0, min(1, host.AnimateTo(pk, target, ui.Animation{Spring: t.Spring.Firm})))
	} else {
		p = host.Jump(pk, target) // 硬切：弹簧通道跟着走，下一次要动时从这里起步
	}
	fade := t.Fade()
	if !open {
		fade = t.FadeOut()
	}
	o := max(0, min(1, host.AnimateTo(ok, target, fade)))
	built := open || p > discHeightEps || (st.motion && o > discHeightEps)
	st.live = built
	if !built {
		st.motion = false
		return true
	}
	wrap := ui.Column(c.Key(key)).MinWidth(0)
	if fill {
		wrap.FillWidth()
	}
	if p < 1-discHeightEps {
		wrap.Height(p * st.full).Clip()
	}
	wrap.Children(func() {
		inner := ui.Column(c.Key("disc-body")).MinWidth(0).Shrink(0)
		if fill {
			inner.FillWidth()
		}
		inner.Bind(&st.h)
		if b := st.h.Bounds(c); b.H > 0 {
			st.full = b.H
		}
		if o < 1 {
			inner.Opacity(o)
		}
		inner.Children(body)
	})
	return false
}

// FadeOut 是内容收起时的淡出：约 100 毫秒（令牌 motion.tip-out），比淡入快一点，带 NonMotion。
func (t Tokens) FadeOut() ui.Animation {
	a := t.Fade()
	a.Duration = t.D.Motion.MotionTipOut
	return a
}

// fadeFrom0 让 e 在 play 为真的第一次画出来时从透明淡入到不透明，之后一直不透明（通道 key，淡入用 Fade，带 NonMotion）。
// seen 记这个元素有没有出现过：已经出现过的（历史、滚出去又回来、重新挂上的）不播。
// 第一帧先 Jump 到 0 再 AnimateTo(1)：通道刚建的那一帧 AnimateTo 直接返回终点、也不要下一帧。
func (t Tokens) fadeFrom0(e ui.Element, key string, play bool, seen *bool) float32 {
	if !*seen {
		*seen = true
		if play {
			e.Jump(key, 0)
		}
	}
	return e.AnimateTo(key, 1, t.Fade())
}

// fadeIn 让 markdown 里整块出现的东西（表格、代码块）在第一次画出来时淡入一次（kind、idx 认是哪一块）。
// 只动透明度：不位移、不动高度，周围的东西一帧都不挪；这条消息第一次画就在场的块不播（历史、滚出去又回来、重新挂上）。
func (e *mdEntry) fadeIn(t Tokens, el ui.Element, kind string, idx int) {
	k := fmt.Sprintf("%s#%d", kind, idx)
	f := e.fades[k]
	if f == nil {
		if e.fades == nil {
			e.fades = map[string]*blockFade{}
		}
		f = &blockFade{play: e.passes > 1}
		e.fades[k] = f
	}
	if o := t.fadeFrom0(el, "block-in", f.play, &f.seen); o < 1 {
		el.Opacity(o)
	}
}
