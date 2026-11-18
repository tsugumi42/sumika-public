package main

import (
	"strings"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
)

// 输入框之外的按键（清单 2.3"粘贴到输入框之外"、2.4"Esc 停止"，参考 Hermes desktop 的 paste-to-focus 和
// use-composer-esc-cancel）：焦点在消息区或空白处时，Esc 照样能停下这一轮，Ctrl+V 也会送进输入框。
// 都登记成窗口级快捷键：mygo 里焦点所在的控件和盖在上面的浮层（对话框、菜单、看图）先拿键，轮不到这里，
// 所以对话框、菜单开着时自然让位，别的输入框里粘贴也不会被抢。

// composerWindowKeys 登记两个窗口级快捷键。h 必须是已经定好 Busy、Stop、OnStop 的那一份。
// 只在画得出输入框的本地私聊、本地群聊里调用，别的会话没有输入框，不登记就不处理。
func (a *app) composerWindowKeys(c *ui.Context, h chatui.ComposerHost, p *chatui.Composer, k api.ConversationKey) {
	if p.Focused(c) { // 焦点在输入框里：按键归输入框自己（Esc 删空卡 / 退出回忆，Ctrl+V 走它的粘贴）
		return
	}
	if h.Busy && !h.Stop && h.OnStop != nil && !a.hasApproval(k) && !a.hasQuestion(k) && !a.forward.open && !a.workbench.shell.st.SideFloatLive() { // 看着合并转发、浮动右栏开着时 Esc 是关它们
		c.OnWindowShortcut(0, ui.KeyEscape, h.OnStop) // 审批卡、问题卡开着时 Esc 归它们，不停回复
	}
	svc, private := c.Services(), k.Scene == "private"
	c.OnWindowShortcut(ui.Cmd, ui.KeyV, func() { a.pasteIntoComposer(svc, k, p, private) })
}

// hasApproval 报告这个会话有没有等着决定的审批卡。
func (a *app) hasApproval(k api.ConversationKey) bool {
	for _, v := range a.approvals {
		if a.approvalIn(v, k) {
			return true
		}
	}
	return false
}

// approvalIn 报告这张卡在不在会话 k 的窗口里：普通卡按键；悄悄话那一轮的卡按来源会话（workbenchWhisperApprovals）。
func (a *app) approvalIn(v api.LocalApproval, k api.ConversationKey) bool {
	if v.Whisper != nil {
		return v.Whisper.Source == k && a.whisperKey(k)
	}
	return v.Key == k
}

// pasteIntoComposer 把剪贴板的内容送进当前会话的输入框并聚焦：和输入框里粘贴同一个顺序，
// 有文字就插文字，没有才看文件和图片（群聊只收文字）。剪贴板里没有能用的东西就什么都不做。
func (a *app) pasteIntoComposer(svc ui.Services, k api.ConversationKey, p *chatui.Composer, private bool) {
	if text := svc.ReadClipboard(); strings.TrimSpace(text) != "" {
		if p.PasteInto(p.Main, text) { // 超过 3000 字符折叠成附件，否则接在输入框末尾
			p.FocusMain()
		}
		return
	}
	if !private {
		return
	}
	d := svc.ReadClipboardData(transfer.FileList, transfer.PNG)
	if files, err := d.Files(); err == nil && len(files) > 0 {
		a.attachPaths(p, p.Main, files)
		p.FocusMain()
		return
	}
	if png, err := d.Read(transfer.PNG); err == nil && len(png) > 0 {
		a.pasteImage(k, p, p.Main, png)
		p.FocusMain()
	}
}

// pastTexts 是当前会话列表里自己发过的话（旧到新），给输入框 ↑ 回忆历史用。
// 临时行（刚发出还没进库的）不算，本次发出的由输入区自己记。
func (a *app) pastTexts() []string {
	var out []string
	for _, m := range a.msgs {
		if m.mine && m.service == "" && m.liveKind == 0 && strings.TrimSpace(m.text) != "" {
			out = append(out, m.text)
		}
	}
	return out
}
