package main

// 设置页“平台账号”里主人 QQ 私聊审批的开关（规格-主人QQ私聊审批）：默认关，打开后离开时审批会发到你的 QQ 私聊、回复同意就批。
// 值走设置注册表（和输入框底行的审批方式同一条写入路径）：读一次拿版本号，写成功才改显示，失败值不变、下一帧重读。
// 说明那一行写明风险（QQ 号被盗就能批），来自设置项表里的常驻说明；这里不再另写一遍。

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

const remoteApprovalKey = "qq.remoteApproval.enabled"

type remoteApprovalUI struct {
	gen      uint64
	asked    bool
	busy     bool
	known    bool
	enabled  bool
	readOnly string
	version  api.SettingsVersion
	note     string
	loadErr  string
}

// remoteApprovalRow 画开关一行。后台没有设置注册表（演示、旧数据源）返回 false，交回只读的普通行。
func (a *app) remoteApprovalRow(c *ui.Context, it api.Result[api.Setting]) bool {
	reg, ok := a.reader.(api.SettingsRegistry)
	if !ok || it.State != api.Ready && it.State != api.Unsupported {
		return false
	}
	st := &a.remoteUI
	if st.gen != a.srcGen {
		*st = remoteApprovalUI{gen: a.srcGen}
	}
	if !st.asked {
		st.asked, st.busy = true, true
		a.loadRemoteApproval(reg)
	}
	s := it.Value
	name := s.Name
	if name == "" {
		name = s.Key
	}
	hint := s.Help
	switch {
	case st.loadErr != "":
		hint = "读不到：" + st.loadErr
	case st.note != "":
		hint = st.note
	case st.readOnly != "":
		hint = st.readOnly
	}
	disabled := st.busy || !st.known || st.readOnly != ""
	onChange := func(v bool) {
		if !st.busy && st.known && v != st.enabled {
			a.writeRemoteApproval(reg, v)
		}
	}
	id := "setting-" + s.Key
	if k := a.settingsKit(); k != nil {
		k.Row(c, id, name, hint, func() { k.SwitchValue(c, id+"-switch", name, st.enabled, disabled, onChange) })
		return true
	}
	a.settingsToggle(c, id, name, st.enabled, disabled, onChange)
	return true
}

func (a *app) loadRemoteApproval(reg api.SettingsRegistry) {
	ctx, generation := a.base(), a.srcGen
	go func() {
		v, err := reg.ReadSettingsRegistry(ctx)
		a.post(func() {
			if a.srcGen != generation {
				return
			}
			st := &a.remoteUI
			st.busy = false
			if err != nil {
				st.loadErr = err.Error()
				return
			}
			st.loadErr = ""
			st.apply(v)
		})
	}()
}

func (a *app) writeRemoteApproval(reg api.SettingsRegistry, on bool) {
	st := &a.remoteUI
	st.busy, st.note = true, ""
	ctx, generation, version := a.base(), a.srcGen, st.version
	go func() {
		v, err := reg.WriteSetting(ctx, api.SettingChange{Key: remoteApprovalKey, Value: []byte(strconv.FormatBool(on))}, version)
		a.post(func() {
			if a.srcGen != generation {
				return
			}
			st := &a.remoteUI
			st.busy = false
			if err != nil {
				st.note = "没有改成：" + err.Error() // 值不变，不回成功的话
				st.asked = false                // 版本可能旧了，下一帧重读
				return
			}
			st.apply(v) // 成功不另写一行字：开关自己变了就是反馈，风险那句说明要一直在；只有失败才换成“没有改成”
		})
	}()
}

func (st *remoteApprovalUI) apply(v api.SettingsView) {
	st.version = v.Version
	for _, item := range v.Items {
		if item.Key != remoteApprovalKey {
			continue
		}
		if on, ok := item.Value.(bool); ok {
			st.enabled, st.known = on, true
		}
		st.readOnly = ""
		if item.ReadOnly {
			st.readOnly = item.Reason
			if st.readOnly == "" {
				st.readOnly = "这里只能看"
			}
		}
	}
}
