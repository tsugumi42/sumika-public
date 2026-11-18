package watchdog

import (
	"errors"
	"testing"
)

// fakeRun 是内存里的 Run 项
type fakeRun struct {
	vals     map[string]string
	disabled map[string]bool
	setErr   error
}

func newFakeRun() *fakeRun { return &fakeRun{vals: map[string]string{}, disabled: map[string]bool{}} }
func (f *fakeRun) Get(n string) (string, bool) {
	v, ok := f.vals[n]
	return v, ok
}
func (f *fakeRun) Set(n, v string) error {
	if f.setErr != nil {
		return f.setErr
	}
	f.vals[n] = v
	return nil
}
func (f *fakeRun) Delete(n string) error        { delete(f.vals, n); return nil }
func (f *fakeRun) Disabled(n string) bool       { return f.disabled[n] }
func (f *fakeRun) ClearDisabled(n string) error { delete(f.disabled, n); return nil }

const (
	exeA       = `D:\x\sumika.exe`
	legacyA    = `"D:\x\sumika.exe" --mygo-opened-at-login`
	watchedA   = `"D:\x\sumika.exe" -watch`
	otherLegcy = `"D:\y\sumika.exe" --mygo-opened-at-login`
)

func TestAutostartOn(t *testing.T) {
	cases := []struct {
		name     string
		val      string
		disabled bool
		want     bool
	}{
		{"没有这一项", "", false, false},
		{"守护进程的写法", watchedA, false, true},
		{"mygo 以前的写法也算开着", legacyA, false, true},
		{"大小写不同的路径", `"d:\X\SUMIKA.exe" -watch`, false, true},
		{"指向别的 exe 不算", otherLegcy, false, false},
		{"别的参数不算", `"D:\x\sumika.exe" -demo`, false, false},
		{"任务管理器里关掉了", watchedA, true, false},
	}
	for _, c := range cases {
		f := newFakeRun()
		if c.val != "" {
			f.vals["Sumika"] = c.val
		}
		f.disabled["Sumika"] = c.disabled
		if got := (Autostart{Store: f, Name: "Sumika", Exe: exeA}).On(); got != c.want {
			t.Errorf("%s：%v", c.name, got)
		}
	}
}

func TestAutostartSet(t *testing.T) {
	f := newFakeRun()
	f.disabled["Sumika"] = true
	a := Autostart{Store: f, Name: "Sumika", Exe: exeA}
	if err := a.Set(true); err != nil || f.vals["Sumika"] != watchedA || f.disabled["Sumika"] {
		t.Fatalf("开：%q disabled=%v %v", f.vals["Sumika"], f.disabled["Sumika"], err)
	}
	if err := a.Set(false); err != nil || len(f.vals) != 0 {
		t.Fatalf("关：%v %v", f.vals, err)
	}
	f.setErr = errors.New("注册表写不了")
	if err := a.Set(true); err == nil || a.On() {
		t.Fatalf("写失败要报错：%v", err)
	}
}

func TestAutostartMigrate(t *testing.T) {
	// 旧写法 → 守护进程写法，别的不动（包括任务管理器里关掉的状态）
	f := newFakeRun()
	f.vals["Sumika"], f.disabled["Sumika"] = legacyA, true
	a := Autostart{Store: f, Name: "Sumika", Exe: exeA}
	if changed, err := a.Migrate(); !changed || err != nil || f.vals["Sumika"] != watchedA || !f.disabled["Sumika"] {
		t.Fatalf("迁移：%v %v %q disabled=%v", changed, err, f.vals["Sumika"], f.disabled["Sumika"])
	}
	if changed, _ := a.Migrate(); changed {
		t.Fatal("已经是新写法不该再改")
	}
	for name, val := range map[string]string{"没有这一项": "", "别的 exe 的旧写法": otherLegcy, "用户自己写的": `"D:\x\sumika.exe" -data D:\d`} {
		f := newFakeRun()
		if val != "" {
			f.vals["Sumika"] = val
		}
		before := f.vals["Sumika"]
		if changed, err := (Autostart{Store: f, Name: "Sumika", Exe: exeA}).Migrate(); changed || err != nil || f.vals["Sumika"] != before {
			t.Errorf("%s：不该动 %v %v %q", name, changed, err, f.vals["Sumika"])
		}
	}
	f = newFakeRun()
	f.vals["Sumika"], f.setErr = legacyA, errors.New("拒绝访问")
	if changed, err := (Autostart{Store: f, Name: "Sumika", Exe: exeA}).Migrate(); changed || err == nil {
		t.Fatalf("写失败要报错且不算改了：%v %v", changed, err)
	}
}
