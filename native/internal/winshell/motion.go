package winshell

import "github.com/egoist/mygo/ui"

// popIn 让 e 在第一次出现时从“下移 travel、缩到 scale”回到原位（对话框面板用，时长取 DialogPop）。
// 为什么不用 Transition 的 Enter：对话框面板和它的整窗遮罩是同一帧出现的，面板自己的 Enter 不播（mygo 只给“父元素上一帧已经在”的孩子播），
// 所以由状态驱动：id 是这个对话框的 open 指针，没见过就先把通道放在起点（Jump），之后每帧走向终点；对话框关掉后 forget 它，下次打开再播。
// 位移和缩放不带 NonMotion：系统要求减少动态效果时 AnimateTo 直接给终点，面板原地淡入（淡入在遮罩的 Transition 上）。
func (k *Kit) popIn(e ui.Element, id *bool, travel, scale float32) {
	if k.popSeen == nil {
		k.popSeen = map[*bool]bool{}
	}
	if !k.popSeen[id] {
		k.popSeen[id] = true
		e.Jump("pop", 0)
	}
	v := e.AnimateTo("pop", 1, DialogPop)
	if v >= 1 {
		return
	}
	s := scale + (1-scale)*v
	e.TransformOrigin(.5, .5).Translate(0, (1-v)*travel).Scale(s, s)
}

// fadeIn 是“换内容”的淡入：jump 为真的那一帧把 e 放回全透明，之后 200 毫秒淡到不透明（ContentFadeAnim，无位移，旧内容不出场）。
// e 必须有 key。第一次出现（通道还不存在）直接是不透明，所以启动那一帧、拖窗口引起的重建不会闪。
func (k *Kit) fadeIn(e ui.Element, jump bool) ui.Element {
	if jump {
		e.Jump("content-fade", 0)
	}
	if o := e.AnimateTo("content-fade", 1, ContentFadeAnim); o < 1 {
		e.Opacity(o)
	}
	return e
}

// forgetPop 在对话框关着的每一帧调用，让下次打开重新播进场。
func (k *Kit) forgetPop(id *bool) { delete(k.popSeen, id) }
