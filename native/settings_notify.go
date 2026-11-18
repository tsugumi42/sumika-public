package main

// 设置页“通知”里按类的开关（规格-目标和循环“提醒”一节）：目标、定期任务、等你批准、她的提问、提醒到点，默认全开。
// 做法同 settings_remote_approval.go：值走设置注册表，读一次拿版本号，写成功才改显示，失败值不变、下一帧重读。
// 五个开关共用一份状态（同一个版本号、同一个“正在写”），不然一个写完另一个的版本就旧了、被后台拒收。
// 后台每次要弹通知前现读配置，所以改了立即生效，不用重启。

import (
	"strconv"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
)

// notifyKeys 是这页的开关，顺序就是显示顺序
var notifyKeys = []string{"notifications.goal", "notifications.loop", "notifications.approval", "notifications.question", "notifications.reminder"}

func isNotifyKey(key string) bool {
	for _, k := range notifyKeys {
		if k == key {
			return true
		}
	}
	return false
}

type notifyToggleUI struct {
	gen      uint64
	asked    bool
	busy     bool
	version  api.SettingsVersion
	note     string
	loadErr  string
	known    map[string]bool
	enabled  map[string]bool
	readOnly map[string]string
}

// notifyRow 画一个开关行。后台没有设置注册表（演示、旧数据源）返回 false，交回只读的普通行。
func (a *app) notifyRow(c *ui.Context, it api.Result[api.Setting]) bool {
	reg, ok := a.reader.(api.SettingsRegistry)
	if !ok || it.State != api.Ready && it.State != api.Unsupported {
		return false
	}
	st := &a.notifyUI
	if st.gen != a.srcGen || st.known == nil {
		*st = notifyToggleUI{gen: a.srcGen, known: map[string]bool{}, enabled: map[string]bool{}, readOnly: map[string]string{}}
	}
	if !st.asked {
		st.asked, st.busy = true, true
		a.loadNotify(reg)
	}
	s := it.Value
	key := s.Key
	name := s.Name
	if name == "" {
		name = key
	}
	hint := s.Help
	switch {
	case st.loadErr != "":
		hint = "读不到：" + st.loadErr
	case st.note != "":
		hint = st.note
	case st.readOnly[key] != "":
		hint = st.readOnly[key]
	}
	disabled := st.busy || !st.known[key] || st.readOnly[key] != ""
	onChange := func(v bool) {
		if !st.busy && st.known[key] && v != st.enabled[key] {
			a.writeNotify(reg, key, v)
		}
	}
	id := "setting-" + key
	if k := a.settingsKit(); k != nil {
		k.Row(c, id, name, hint, func() { k.SwitchValue(c, id+"-switch", name, st.enabled[key], disabled, onChange) })
		return true
	}
	a.settingsToggle(c, id, name, st.enabled[key], disabled, onChange)
	return true
}

func (a *app) loadNotify(reg api.SettingsRegistry) {
	ctx, generation := a.base(), a.srcGen
	go func() {
		v, err := reg.ReadSettingsRegistry(ctx)
		a.post(func() {
			if a.srcGen != generation {
				return
			}
			st := &a.notifyUI
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

func (a *app) writeNotify(reg api.SettingsRegistry, key string, on bool) {
	st := &a.notifyUI
	st.busy, st.note = true, ""
	ctx, generation, version := a.base(), a.srcGen, st.version
	go func() {
		v, err := reg.WriteSetting(ctx, api.SettingChange{Key: key, Value: []byte(strconv.FormatBool(on))}, version)
		a.post(func() {
			if a.srcGen != generation {
				return
			}
			st := &a.notifyUI
			st.busy = false
			if err != nil {
				st.note = "没有改成：" + err.Error() // 值不变，不回成功的话
				st.asked = false                // 版本可能旧了，下一帧重读
				return
			}
			st.apply(v) // 成功不另写一行字：开关自己变了就是反馈
		})
	}()
}

func (st *notifyToggleUI) apply(v api.SettingsView) {
	st.version = v.Version
	for _, item := range v.Items {
		if !isNotifyKey(item.Key) {
			continue
		}
		if on, ok := item.Value.(bool); ok {
			st.enabled[item.Key], st.known[item.Key] = on, true
		}
		delete(st.readOnly, item.Key)
		if item.ReadOnly {
			st.readOnly[item.Key] = item.Reason
			if item.Reason == "" {
				st.readOnly[item.Key] = "这里只能看"
			}
		}
	}
}
