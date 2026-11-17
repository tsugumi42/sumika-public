package main

import (
	"context"
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/settingsreg"
)

// regReader 是只带设置注册表的假数据源：值在内存里，写入可以让它失败；名字、号都是编的。
type regReader struct {
	api.Reader
	mu       sync.Mutex
	on       bool
	readOnly bool
	fail     error
	version  int
	writes   []string
}

func (r *regReader) view() api.SettingsView {
	e := settingsreg.Entry{Key: remoteApprovalKey, Type: "bool", Effect: "立即", ReadOnly: r.readOnly}
	if r.readOnly {
		e.Reason = "编的原因：只能看"
	}
	return api.SettingsView{Items: []settingsreg.Value{{Entry: e, Value: r.on}}, Version: api.SettingsVersion{Config: string(rune('a' + r.version))}}
}

func (r *regReader) ReadSettingsRegistry(context.Context) (api.SettingsView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.view(), nil
}

func (r *regReader) WriteSetting(_ context.Context, ch api.SettingChange, _ api.SettingsVersion) (api.SettingsView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, ch.Key+"="+string(ch.Value))
	if r.fail != nil {
		return api.SettingsView{}, r.fail
	}
	r.on = string(ch.Value) == "true"
	r.version++
	v := r.view()
	v.Effect = "立即" // 真实的 WriteSetting 落盘后带上注册表里的生效时机
	return v, nil
}

func (r *regReader) snapshot() (bool, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.on, append([]string(nil), r.writes...)
}

// remoteUIApp 是停在“设置 → 平台账号”、后台带设置注册表的新界面。post 进队列，由测试在界面线程上放行。
func remoteUIApp(t *testing.T, reader *regReader) (*app, *ui.Tester, func()) {
	t.Helper()
	a := shellApp()
	a.rail, a.realTab, a.demoMode = 4, tabAccounts, false
	a.set = nil
	a.real = realSettingsOf(t, nil)
	a.reader = reader
	var mu sync.Mutex
	var queue []func()
	a.postFn = func(f func()) { mu.Lock(); queue = append(queue, f); mu.Unlock() }
	tt := shellTester(a, 2048, 1030)
	pump := func() {
		for range 50 {
			mu.Lock()
			q := queue
			queue = nil
			mu.Unlock()
			if len(q) == 0 {
				tt.Frame()
				mu.Lock()
				empty := len(queue) == 0
				mu.Unlock()
				if empty {
					return
				}
				continue
			}
			for _, f := range q {
				f()
			}
			tt.Frame()
		}
	}
	return a, tt, pump
}

// 等写入到达后台、结果也回到界面（busy 清掉）再往下，理由同 notifyWaitWrites
func waitWrites(t *testing.T, r *regReader, n int, pump func(), a *app) {
	t.Helper()
	remoteWait(t, "写入到达后台、结果回到界面", func() bool { pump(); _, w := r.snapshot(); return len(w) >= n && !a.remoteUI.busy })
	pump()
}

func remoteWait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 2000 {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("等不到：%s", what)
}

// 默认关的开关：画在“平台账号”页，说明里写着风险；点一下写进设置注册表，成功才变；失败值不变、写明没改成。
func TestRemoteApprovalSwitch(t *testing.T) {
	r := &regReader{}
	a, tt, pump := remoteUIApp(t, r)
	pump()
	for _, want := range []string{"在 QQ 私聊里批准", "号被盗就能批", "离开多久后发到 QQ"} {
		if !tt.HasText(want) {
			t.Fatalf("缺“%s”：%q", want, tt.Texts())
		}
	}
	if a.remoteUI.enabled || !a.remoteUI.known {
		t.Fatalf("默认关，且读到了：%+v", a.remoteUI)
	}
	shot := func(name string) {
		if dir := os.Getenv("LOOK_SHOT"); dir != "" {
			shotRow(t, tt, dir, "setting-pad-qq.remoteApproval.enabled", name)
		}
	}
	shot("QQ私聊审批开关-默认关.png")
	clickKey(t, tt, "setting-qq.remoteApproval.enabled-switch")
	waitWrites(t, r, 1, pump, a)
	on, writes := r.snapshot()
	if !on || len(writes) != 1 || writes[0] != remoteApprovalKey+"=true" {
		t.Fatalf("应写 true：%v %v", on, writes)
	}
	if !a.remoteUI.enabled || !tt.HasText("号被盗就能批") || strings.Contains(strings.Join(tt.Texts(), "|"), "没有改成") {
		t.Fatalf("保存后显示要变：%+v %q", a.remoteUI, tt.Texts())
	}
	shot("QQ私聊审批开关-已打开.png")
	// 再点关掉
	clickKey(t, tt, "setting-qq.remoteApproval.enabled-switch")
	waitWrites(t, r, 2, pump, a)
	if on, writes = r.snapshot(); on || writes[1] != remoteApprovalKey+"=false" {
		t.Fatalf("应写 false：%v %v", on, writes)
	}
	// 写失败：值不变，写明没改成，不回成功的话
	r.mu.Lock()
	r.fail = errors.New("编的失败")
	r.mu.Unlock()
	clickKey(t, tt, "setting-qq.remoteApproval.enabled-switch")
	waitWrites(t, r, 3, pump, a)
	// 写失败的结果异步回到界面：刷到出现为止，不按固定帧数等（负载下合并队列红过，2026-11-12）
	remoteWait(t, "写失败的说明", func() bool {
		pump()
		return strings.Contains(strings.Join(tt.Texts(), "|"), "没有改成：编的失败")
	})
	if a.remoteUI.enabled {
		t.Fatalf("失败时值不变：%+v", a.remoteUI)
	}
	if on, _ = r.snapshot(); on {
		t.Fatal("后台值不该变")
	}
	shot("QQ私聊审批开关-写失败.png")
}

// 注册表标了只读：开关点不动，说明换成原因。
func TestRemoteApprovalSwitchReadOnly(t *testing.T) {
	r := &regReader{readOnly: true}
	a, tt, pump := remoteUIApp(t, r)
	// 只读状态异步读回来：刷到出现为止（只刷一帧在负载下会早到，2026-11-12 合并队列红过）
	remoteWait(t, "只读原因", func() bool { pump(); return tt.HasText("编的原因：只能看") })
	clickKey(t, tt, "setting-qq.remoteApproval.enabled-switch")
	pump()
	if _, w := r.snapshot(); len(w) != 0 || a.remoteUI.enabled {
		t.Fatalf("只读不能写：%v", w)
	}
}

// 后台没有设置注册表（旧数据源）：退回只读的普通行，不摆假开关。
func TestRemoteApprovalSwitchWithoutRegistry(t *testing.T) {
	a := shellApp()
	a.rail, a.realTab, a.demoMode = 4, tabAccounts, false
	a.set = nil
	a.real = realSettingsOf(t, nil)
	tt := shellTester(a, 2048, 1030)
	if !tt.HasText("在 QQ 私聊里批准") {
		t.Fatalf("只读行也要在：%q", tt.Texts())
	}
	if hasKey(tt, "setting-qq.remoteApproval.enabled-switch") {
		t.Fatal("没有注册表不能有开关")
	}
}

func shotRow(t *testing.T, tt *ui.Tester, dir, key, name string) {
	t.Helper()
	b := boxOf(t, tt, key)
	img := tt.Image()
	const scale = 1.25
	pad := 16
	rect := image.Rect(int(float64(b.Box.X)*scale)-pad, int(float64(b.Box.Y)*scale)-pad, int(float64(b.Box.X+b.Box.W)*scale)+pad, int(float64(b.Box.Y+b.Box.H)*scale)+pad)
	out, err := os.Create(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if err := png.Encode(out, img.SubImage(rect)); err != nil {
		t.Fatal(err)
	}
}
