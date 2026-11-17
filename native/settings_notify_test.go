package main

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/settingsreg"
)

// notifyRegReader 是只带通知开关的假设置注册表：值在内存里，写入可以让它失败；写入要带上最近一次读到的版本，旧了就拒收。
type notifyRegReader struct {
	api.Reader
	mu      sync.Mutex
	vals    map[string]bool
	fail    error
	version int
	writes  []string
	ro      string
}

func newNotifyReg() *notifyRegReader {
	r := &notifyRegReader{vals: map[string]bool{}}
	for _, k := range notifyKeys {
		r.vals[k] = true
	}
	return r
}

func (r *notifyRegReader) view() api.SettingsView {
	v := api.SettingsView{Version: api.SettingsVersion{Config: strconv.Itoa(r.version)}}
	for _, k := range notifyKeys {
		e := settingsreg.Entry{Key: k, Type: "bool", Effect: "立即", ReadOnly: r.ro != "", Reason: r.ro}
		v.Items = append(v.Items, settingsreg.Value{Entry: e, Value: r.vals[k]})
	}
	return v
}

func (r *notifyRegReader) ReadSettingsRegistry(context.Context) (api.SettingsView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.view(), nil
}

func (r *notifyRegReader) WriteSetting(_ context.Context, ch api.SettingChange, ver api.SettingsVersion) (api.SettingsView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, ch.Key+"="+string(ch.Value))
	if r.fail != nil {
		return api.SettingsView{}, r.fail
	}
	if ver.Config != strconv.Itoa(r.version) {
		return api.SettingsView{}, errors.New("版本旧了")
	}
	r.vals[ch.Key] = string(ch.Value) == "true"
	r.version++
	return r.view(), nil
}

func (r *notifyRegReader) snapshot() (map[string]bool, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := map[string]bool{}
	for k, v := range r.vals {
		m[k] = v
	}
	return m, append([]string(nil), r.writes...)
}

// notifyUIApp 是停在“设置 → 通知”、后台带设置注册表的新界面（通知是第 6 页）。
func notifyUIApp(t *testing.T, reg *notifyRegReader) (*app, *ui.Tester, func()) {
	t.Helper()
	a, tt, pump := remoteUIApp(t, &regReader{})
	a.realTab = tabNotify
	a.reader = reg
	pump()
	return a, tt, pump
}

// 等写入到达后台、结果也回到界面（busy 清掉）再往下：busy 时开关是禁用的，结果没回来就点下一个会被忽略，
// 负载下合并队列等满时限红过（2026-11-12）
func notifyWaitWrites(t *testing.T, r *notifyRegReader, n int, pump func(), a *app) {
	t.Helper()
	remoteWait(t, "写入到达后台、结果回到界面", func() bool { pump(); _, w := r.snapshot(); return len(w) >= n && !a.notifyUI.busy })
	pump()
}

func shotNotifyRows(t *testing.T, tt *ui.Tester, name string) {
	dir := os.Getenv("LOOK_SHOT")
	if dir == "" {
		return
	}
	first, last := boxOf(t, tt, "setting-pad-"+notifyKeys[0]), boxOf(t, tt, "setting-pad-"+notifyKeys[len(notifyKeys)-1])
	const scale, pad = 1.25, 16
	rect := image.Rect(int(float64(first.Box.X)*scale)-pad, int(float64(first.Box.Y)*scale)-pad, int(float64(first.Box.X+first.Box.W)*scale)+pad, int(float64(last.Box.Y+last.Box.H)*scale)+pad)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, tt.Image().SubImage(rect)); err != nil {
		t.Fatal(err)
	}
}

// 设置 → 通知里五类开关默认全开；点一个写进设置注册表，成功才变，连着点几个不被版本号挡；
// 写失败时值不变、写明没改成，且不影响别的开关。
func TestNotifySwitchesPerKind(t *testing.T) {
	reg := newNotifyReg()
	a, tt, pump := notifyUIApp(t, reg)
	for _, want := range []string{"目标", "定期任务", "等你批准", "她的提问", "提醒到点"} {
		if !tt.HasText(want) {
			t.Fatalf("缺“%s”：%q", want, tt.Texts())
		}
	}
	for _, k := range notifyKeys {
		if !a.notifyUI.known[k] || !a.notifyUI.enabled[k] || !hasKey(tt, "setting-"+k+"-switch") {
			t.Fatalf("%s 应读到、默认开、画出开关：%+v", k, a.notifyUI)
		}
	}
	shotNotifyRows(t, tt, "通知设置-默认全开.png")

	clickKey(t, tt, "setting-notifications.goal-switch")
	notifyWaitWrites(t, reg, 1, pump, a)
	clickKey(t, tt, "setting-notifications.reminder-switch")
	notifyWaitWrites(t, reg, 2, pump, a)
	vals, writes := reg.snapshot()
	if vals["notifications.goal"] || vals["notifications.reminder"] || !vals["notifications.loop"] || !vals["notifications.approval"] || !vals["notifications.question"] {
		t.Fatalf("只该关掉目标和提醒：%v", vals)
	}
	if writes[0] != "notifications.goal=false" || writes[1] != "notifications.reminder=false" {
		t.Fatalf("写入内容不对：%v", writes)
	}
	if a.notifyUI.enabled["notifications.goal"] || a.notifyUI.enabled["notifications.reminder"] || !a.notifyUI.enabled["notifications.loop"] {
		t.Fatalf("显示要跟着变：%+v", a.notifyUI.enabled)
	}
	shotNotifyRows(t, tt, "通知设置-关了目标和提醒.png")

	clickKey(t, tt, "setting-notifications.goal-switch") // 再点打开
	notifyWaitWrites(t, reg, 3, pump, a)
	if vals, _ = reg.snapshot(); !vals["notifications.goal"] {
		t.Fatalf("应又打开：%v", vals)
	}

	reg.mu.Lock()
	reg.fail = errors.New("编的失败")
	reg.mu.Unlock()
	clickKey(t, tt, "setting-notifications.loop-switch")
	notifyWaitWrites(t, reg, 4, pump, a)
	if !a.notifyUI.enabled["notifications.loop"] || !strings.Contains(strings.Join(tt.Texts(), "|"), "没有改成：编的失败") {
		t.Fatalf("失败时值不变并写明：%+v %q", a.notifyUI.enabled, tt.Texts())
	}
	if vals, _ = reg.snapshot(); !vals["notifications.loop"] {
		t.Fatal("后台值不该变")
	}
	shotNotifyRows(t, tt, "通知设置-写失败.png")
}

// 注册表标了只读：开关点不动，说明换成原因。
func TestNotifySwitchesReadOnly(t *testing.T) {
	reg := newNotifyReg()
	reg.ro = "编的原因：只能看"
	a, tt, pump := notifyUIApp(t, reg)
	if !tt.HasText("编的原因：只能看") {
		t.Fatalf("要显示原因：%q", tt.Texts())
	}
	clickKey(t, tt, "setting-notifications.goal-switch")
	pump()
	if _, w := reg.snapshot(); len(w) != 0 || !a.notifyUI.enabled["notifications.goal"] {
		t.Fatalf("只读不能写：%v", w)
	}
}

// 后台没有设置注册表（旧数据源）：退回只读的普通行，不摆假开关。
func TestNotifySwitchesWithoutRegistry(t *testing.T) {
	a := shellApp()
	a.rail, a.realTab, a.demoMode = 4, tabNotify, false
	a.set = nil
	a.real = realSettingsOf(t, nil)
	tt := shellTester(a, 2048, 1030)
	if !tt.HasText("提醒到点") {
		t.Fatalf("只读行也要在：%q", tt.Texts())
	}
	if hasKey(tt, "setting-notifications.goal-switch") {
		t.Fatal("没有注册表不能有开关")
	}
}
