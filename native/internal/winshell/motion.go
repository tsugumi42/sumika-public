package winshell

import "github.com/egoist/mygo/ui"

// 对话框的进场原来在这里（popIn：上移 10、缩放 .985）；2026-11-15 起改成从打开它的按钮长出来，见 dialog.go 的 dialogMorph。

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
