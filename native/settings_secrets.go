package main

// 设置 · 数据 · 密钥（规格-数据目录与备份 5.3.2“界面”、5.3.3、ADR-373 第 2 条）：
// 一行状态（解开了 / 锁着 · 有没有主密码 · 本机副本在不在）、没设主密码时的常驻提示、
// 名字列表（只写“已存”，不显示值）、添加 / 删除（二次确认）、设 / 改主密码（输两次，
// 成功后把恢复码显示一次，点“抄好了”才关）、锁着时的解锁输入框。
//
// 值、主密码、恢复码不进日志，也不留在界面状态里超过必要时间：提交后清空输入，
// 恢复码只在对话框开着时留在内存，关掉即丢。后台在另一个线程，界面每帧只读状态。
// 控件走 winshell 的 Kit；旧界面退回原来的行、输入框和对话框（settingsDialog），不另画新控件。

import (
	"fmt"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/ctlproto"
)

type secretsPageUI struct {
	gen   uint64
	asked bool
	busy  bool
	seq   uint64

	loadErr string
	note    string
	status  ctlproto.SecretsStatus
	names   []string

	addOpen, passOpen, delOpen, recoveryOpen bool
	addName, addValue, addErr                string
	pass1, pass2, passErr                    string
	deleteName                               string
	recoveryCode                             string
	unlockSecret                             string
	unlockRecovery                           bool
	unlockErr                                string
}

func (a *app) secretsGroup(c *ui.Context) {
	sa, ok := a.reader.(api.SecretsAdmin)
	if !ok {
		return // 演示、旧数据源没有密钥库
	}
	st := &a.secretsUI
	if st.gen != a.srcGen {
		*st = secretsPageUI{gen: a.srcGen}
	}
	if !st.asked {
		st.asked = true
		a.secretsReload(sa)
	}
	st = &a.secretsUI
	a.settingsGroup(c, "密钥", func() {
		if st.loadErr != "" {
			ui.Row(c).FillWidth().Gap(c.Theme().Space(2)).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Grow(1).MinWidth(0).Children(func() { a.secretsNote(c, "读不到密钥库："+st.loadErr, true) })
				a.settingsButton(c, "secrets-retry", "重试", false, st.busy, func() { a.secretsReload(sa) })
			})
			return
		}
		if st.status.File && !st.status.Unlocked {
			a.secretsUnlockRow(c, st, sa)
		}
		a.secretsNote(c, secretsStatusLine(st.status), false)
		if st.status.Note != "" {
			a.secretsNote(c, st.status.Note, true)
		}
		if !st.status.HasPassphrase {
			ui.Row(c).FillWidth().Gap(c.Theme().Space(2)).AlignItems(ui.Center).Children(func() {
				ui.Box(c).Grow(1).MinWidth(0).Children(func() { a.secretsNote(c, "没设主密码：换电脑或重装系统后密钥解不开", false) })
				a.settingsButton(c, "secrets-set-pass", "设主密码", false, st.busy, func() {
					st.openPassphrase()
				})
			})
		} else {
			a.secretsRow(c, "secrets-passphrase", "主密码", "换电脑时用主密码或恢复码解开；忘了主密码就用恢复码", func() {
				a.settingsButton(c, "secrets-set-pass", "改主密码", false, st.busy || st.status.File && !st.status.Unlocked, func() {
					st.openPassphrase()
				})
			})
		}
		for _, name := range st.names {
			name := name
			a.secretsRow(c, "secrets-name-"+name, name, "已存", func() {
				a.settingsButton(c, "secrets-del-"+name, "删除", false, st.busy, func() {
					st.deleteName = name
					st.delOpen = true
				})
			})
		}
		if st.status.Unlocked && len(st.names) == 0 {
			a.secretsNote(c, "还没有存密钥", false)
		}
		a.secretsRow(c, "secrets-add-row", "添加一个密钥", "名字和值都只存在这台电脑的密钥库里", func() {
			a.settingsButton(c, "secrets-add", "添加", false, st.busy || st.status.File && !st.status.Unlocked, func() {
				st.addName, st.addValue, st.addErr = "", "", ""
				st.addOpen = true
			})
		})
		if st.note != "" {
			a.secretsNote(c, "没有改成："+st.note, true)
		}
	})
	a.secretsDialogs(c, st, sa)
}

// secretsFrame 每帧跑一次，跟设置页停在哪个标签无关：对话框一关就把恢复码从内存里清掉。
// 以前这句写在 secretsGroup 开头，切到别的标签、密钥页不再画，清码也就不跑了（P1-2 ③复核遗留）。
func (a *app) secretsFrame() {
	if !a.secretsUI.recoveryOpen {
		a.secretsUI.recoveryCode = ""
	}
}

// openPassphrase 打开设 / 改主密码对话框：每次都从空白开始，不让上一次的输入留在框里。
func (st *secretsPageUI) openPassphrase() {
	st.pass1, st.pass2, st.passErr = "", "", ""
	st.passOpen = true
}

func secretsStatusLine(st ctlproto.SecretsStatus) string {
	line := "还没有密钥文件"
	switch {
	case st.File && st.Unlocked:
		line = "已解开"
	case st.File:
		line = "锁着"
	}
	pass := "没设主密码"
	if st.HasPassphrase {
		pass = "设了主密码"
	}
	local := "本机副本：不在"
	if st.LocalCopy {
		local = "本机副本：在"
	}
	return fmt.Sprintf("%s · %s · %s", line, pass, local)
}

func (a *app) secretsUnlockRow(c *ui.Context, st *secretsPageUI, sa api.SecretsAdmin) {
	if !st.status.HasPassphrase {
		a.secretsNote(c, "这台电脑解不开本机那一份，也没有可用的主密码或恢复码", true)
	}
	submit := func() {
		if st.busy || st.unlockSecret == "" {
			return
		}
		secret, recovery := st.unlockSecret, st.unlockRecovery
		st.unlockSecret = ""
		st.unlockErr = ""
		st.busy = true
		ctx, gen, seq := a.base(), a.srcGen, st.seq+1
		st.seq = seq
		go func() {
			out, err := sa.SecretsUnlock(ctx, secret, recovery)
			a.post(func() {
				if a.srcGen != gen || a.secretsUI.seq != seq {
					return
				}
				s := &a.secretsUI
				s.busy = false
				if err != nil {
					s.unlockErr = err.Error()
					return
				}
				s.unlockRecovery = false
				a.secretsReload(sa)
				s.note = out.Note
			})
		}()
	}
	hint := "输入主密码或恢复码解开"
	a.secretsRow(c, "secrets-unlock", hint, "", func() {
		field := a.secretsField(c, "secrets-unlock-input", &st.unlockSecret, "主密码或恢复码", true, submit)
		field.Width(240)
		a.secretsCheck(c, "secrets-unlock-recovery", "用恢复码", &st.unlockRecovery)
		a.settingsButton(c, "secrets-unlock-btn", "解开", true, st.busy || !st.status.HasPassphrase, submit)
	})
	if st.unlockErr != "" {
		a.secretsNote(c, "没解开："+st.unlockErr, true)
	}
}

func (a *app) secretsDialogs(c *ui.Context, st *secretsPageUI, sa api.SecretsAdmin) {
	a.settingsDialog(c, "secrets-add", &st.addOpen, settingsDialogSpec{Title: "添加密钥", Actions: []settingsDialogAction{
		{ID: "add", Label: "添加", Default: true, KeepOpen: true, Disabled: st.busy},
		{ID: "cancel", Label: "取消"},
	}, OnAction: func(id string) {
		if id == "add" {
			a.secretsAdd(sa)
		}
	}}, func() {
		a.secretsNote(c, "名字用字母、数字和 . _ -，最长 128；值存下来以后不再显示", false)
		a.secretsField(c, "secrets-add-name", &st.addName, "密钥名字", false, nil)
		a.secretsField(c, "secrets-add-value", &st.addValue, "密钥值", true, nil)
		if st.addErr != "" {
			a.secretsNote(c, "没有存下："+st.addErr, true)
		}
	})

	a.settingsDialog(c, "secrets-passphrase-dialog", &st.passOpen, settingsDialogSpec{Title: "设主密码", Actions: []settingsDialogAction{
		{ID: "save", Label: "保存", Default: true, KeepOpen: true, Disabled: st.busy},
		{ID: "cancel", Label: "取消"},
	}, OnAction: func(id string) {
		if id == "save" {
			a.secretsSetPassphrase(sa)
		}
	}}, func() {
		a.secretsNote(c, "至少 8 个字符。设了主密码就能换电脑，忘了主密码时用恢复码；两样都忘了密钥作废，要重新填。", false)
		a.secretsField(c, "secrets-pass-1", &st.pass1, "新的主密码", true, nil)
		a.secretsField(c, "secrets-pass-2", &st.pass2, "再输一次新的主密码", true, nil)
		if st.passErr != "" {
			a.secretsNote(c, st.passErr, true)
		}
	})

	a.settingsDialog(c, "secrets-delete-dialog", &st.delOpen, settingsDialogSpec{Title: "删除密钥？", Actions: []settingsDialogAction{
		{ID: "delete", Label: "删除", Default: true, Danger: true, KeepOpen: true, Disabled: st.busy},
		{ID: "cancel", Label: "取消"},
	}, OnAction: func(id string) {
		if id == "delete" {
			a.secretsDelete(sa)
		}
	}}, func() {
		a.secretsNote(c, "删掉“"+st.deleteName+"”以后要用时得重新填；值不显示，也找不回来。", true)
	})

	a.settingsDialog(c, "secrets-recovery-dialog", &st.recoveryOpen, settingsDialogSpec{Title: "恢复码", Locked: true, Actions: []settingsDialogAction{
		{ID: "ok", Label: "抄好了", Default: true},
	}, OnAction: func(id string) {
		if id == "ok" {
			st.recoveryCode = ""
		}
	}}, func() {
		a.secretsNote(c, "只显示这一次，抄下来。忘了主密码时用它解开；关掉以后就看不到了。", false)
		ui.Text(c.Key("secrets-recovery-code"), st.recoveryCode).FontWeight(600).Selectable().Wrap()
	})
}

func (a *app) secretsReload(sa api.SecretsAdmin) {
	st := &a.secretsUI
	st.busy, st.loadErr, st.note = true, "", ""
	ctx, gen := a.base(), a.srcGen
	seq := st.seq + 1
	st.seq = seq
	go func() {
		status, err := sa.SecretsStatus(ctx)
		var names []string
		if err == nil && status.Unlocked {
			names, err = sa.SecretsList(ctx)
		}
		a.post(func() {
			if a.srcGen != gen || a.secretsUI.seq != seq {
				return
			}
			s := &a.secretsUI
			s.busy = false
			if err != nil {
				s.loadErr = err.Error()
				return
			}
			s.status, s.names = status, names
		})
	}()
}

func (a *app) secretsAdd(sa api.SecretsAdmin) {
	st := &a.secretsUI
	if st.busy {
		return
	}
	name, value := st.addName, st.addValue
	st.addErr, st.busy = "", true
	ctx, gen, seq := a.base(), a.srcGen, st.seq+1
	st.seq = seq
	go func() {
		err := sa.SecretsSet(ctx, name, value)
		a.post(func() {
			if a.srcGen != gen || a.secretsUI.seq != seq {
				return
			}
			s := &a.secretsUI
			if err != nil {
				s.busy = false
				s.addErr = err.Error() // 值还留在框里，改完可以直接再存
				return
			}
			s.addOpen = false
			s.addName, s.addValue = "", ""
			a.secretsReload(sa)
		})
	}()
}

func (a *app) secretsDelete(sa api.SecretsAdmin) {
	st := &a.secretsUI
	if st.busy || st.deleteName == "" {
		return
	}
	name := st.deleteName
	st.busy = true
	ctx, gen, seq := a.base(), a.srcGen, st.seq+1
	st.seq = seq
	go func() {
		err := sa.SecretsDelete(ctx, name)
		a.post(func() {
			if a.srcGen != gen || a.secretsUI.seq != seq {
				return
			}
			s := &a.secretsUI
			if err != nil {
				s.busy = false
				s.note = err.Error()
				return
			}
			s.delOpen = false
			s.deleteName = ""
			a.secretsReload(sa)
		})
	}()
}

func (a *app) secretsSetPassphrase(sa api.SecretsAdmin) {
	st := &a.secretsUI
	if st.busy {
		return
	}
	if st.pass1 != st.pass2 {
		st.passErr = "两次输入的主密码不一样"
		return
	}
	pass := st.pass1
	st.passErr, st.busy = "", true
	ctx, gen, seq := a.base(), a.srcGen, st.seq+1
	st.seq = seq
	go func() {
		code, err := sa.SecretsSetPassphrase(ctx, pass)
		a.post(func() {
			if a.srcGen != gen || a.secretsUI.seq != seq {
				return
			}
			s := &a.secretsUI
			s.busy = false
			if err != nil {
				s.passErr = err.Error()
				return
			}
			s.passOpen = false
			s.pass1, s.pass2 = "", ""
			s.recoveryCode, s.recoveryOpen = code, true
			a.secretsReload(sa)
		})
	}()
}

// ───── 控件：新界面走 winshell 的 Kit，旧界面走原来的行、输入框和说明 ─────

func (a *app) secretsRow(c *ui.Context, key, label, hint string, control func()) {
	if k := a.settingsKit(); k != nil {
		k.Row(c, key, label, hint, control)
		return
	}
	row(c, label, hint, control)
}

func (a *app) secretsNote(c *ui.Context, text string, failure bool) {
	if k := a.settingsKit(); k != nil {
		k.Note(c, text, failure)
		return
	}
	col := c.Theme().TextMuted
	if failure {
		col = c.Theme().Danger
	}
	// 和 typography.Caption 同一档字号（Rem(0.85)），只是不经组件库画。
	ui.Text(c, text).FontSize(c.Theme().Rem(0.85)).TextColor(col).Wrap()
}

func (a *app) secretsField(c *ui.Context, key string, value *string, label string, password bool, onSubmit func()) ui.Element {
	var e ui.Element
	if k := a.settingsKit(); k != nil {
		e = k.TextField(c, key, value, label)
	} else {
		e = ui.TextInput(c.Key(key), value).Label(label)
	}
	if password {
		e = e.Password()
	}
	if onSubmit != nil {
		e = e.OnSubmit(onSubmit)
	}
	return e.FillWidth()
}

func (a *app) secretsCheck(c *ui.Context, key, label string, v *bool) {
	if k := a.settingsKit(); k != nil {
		k.Check(c, key, label, v, false)
		return
	}
	ui.Checkbox(c.Key(key), v, label)
}
