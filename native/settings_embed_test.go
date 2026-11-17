package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"sumika-native/internal/api"
)

// embedRegReader 是只带缺模型状态的假设置注册表：值在内存里，写入可以让它失败；写入要带上最近一次读到的版本。
type embedRegReader struct {
	api.Reader
	mu       sync.Mutex
	missing  *api.EmbedMissingModel
	fail     error
	version  int
	writes   []api.SettingChange
	versions []api.SettingsVersion
}

func (r *embedRegReader) view() api.SettingsView {
	v := api.SettingsView{Version: api.SettingsVersion{Config: strconv.Itoa(r.version)}, EmbedMissingModel: r.missing}
	return v
}

func (r *embedRegReader) ReadSettingsRegistry(context.Context) (api.SettingsView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.view(), nil
}

func (r *embedRegReader) WriteSetting(_ context.Context, ch api.SettingChange, ver api.SettingsVersion) (api.SettingsView, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.writes = append(r.writes, ch)
	r.versions = append(r.versions, ver)
	if r.fail != nil {
		return api.SettingsView{}, r.fail
	}
	if ver.Config != strconv.Itoa(r.version) {
		return api.SettingsView{}, errors.New("版本旧了")
	}
	r.version++
	return r.view(), nil
}

func (r *embedRegReader) snapshot() ([]api.SettingChange, []api.SettingsVersion) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]api.SettingChange(nil), r.writes...), append([]api.SettingsVersion(nil), r.versions...)
}

// embedUIAppAt 是停在“设置 → 记忆和学习”、后台带设置注册表的新界面；post 进队列，由测试在界面线程上放行。
func embedUIAppAt(t *testing.T, reg *embedRegReader, w, h int) (*app, *ui.Tester, func()) {
	t.Helper()
	a := shellApp()
	a.rail, a.realTab, a.demoMode = 4, tabMemory, false
	a.set = nil
	a.real = realSettingsOf(t, nil)
	a.reader = reg
	var mu sync.Mutex
	var queue []func()
	a.postFn = func(f func()) { mu.Lock(); queue = append(queue, f); mu.Unlock() }
	tt := shellTester(a, w, h)
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

func embedUIApp(t *testing.T, reg *embedRegReader) (*app, *ui.Tester, func()) {
	t.Helper()
	return embedUIAppAt(t, reg, 2048, 1030)
}

// 缺模型、登记表有下载页：一行完整路径和两个按钮都在，点下载页调到打开网址的入口。
func TestEmbedMissingModelOffersDownloadAndFolder(t *testing.T) {
	opened := ""
	oldOpen := openBrowser
	openBrowser = func(_ *ui.Context, raw string, _ func(error)) { opened = raw }
	t.Cleanup(func() { openBrowser = oldOpen })

	missing := &api.EmbedMissingModel{
		Model:       "gte-multilingual-base",
		Path:        `D:\Sumika\data\models\_待分类\gte-multilingual-base\model_quantized.onnx`,
		DownloadURL: "https://huggingface.co/Alibaba-NLP/gte-multilingual-base",
	}
	reg := &embedRegReader{missing: missing}
	_, tt, pump := embedUIApp(t, reg)
	pump()
	for _, want := range []string{"找不到模型文件：", "model_quantized.onnx", "打开下载页", "指定模型目录"} {
		if !tt.HasText(want) {
			t.Fatalf("缺“%s”：%q", want, tt.Texts())
		}
	}
	if err := tt.Click("打开下载页"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if opened != missing.DownloadURL {
		t.Fatalf("点下载页打开了 %q，要 %q", opened, missing.DownloadURL)
	}
}

// 登记表没有下载地址：只留“指定模型目录”。
func TestEmbedMissingModelWithoutDownloadPage(t *testing.T) {
	reg := &embedRegReader{missing: &api.EmbedMissingModel{Model: "gte-multilingual-base", Path: `D:\models\model.onnx`}}
	_, tt, pump := embedUIApp(t, reg)
	pump()
	if tt.HasText("打开下载页") {
		t.Fatalf("没有下载地址不该画按钮：%q", tt.Texts())
	}
	if !tt.HasText("指定模型目录") || !tt.HasText("找不到模型文件：") {
		t.Fatalf("缺路径行或选目录按钮：%q", tt.Texts())
	}
}

// 不缺模型：这一节和原来一样，不多路径行和按钮。
func TestEmbedMissingModelAbsentLeavesSectionAlone(t *testing.T) {
	reg := &embedRegReader{}
	_, tt, pump := embedUIApp(t, reg)
	pump()
	for _, no := range []string{"找不到模型文件", "打开下载页", "指定模型目录"} {
		if tt.HasText(no) {
			t.Fatalf("不缺模型却出现了 %q：%q", no, tt.Texts())
		}
	}
}

// 点“指定模型目录”：选完写 embed.modelsRoot；取消不写；写失败显示原因。
func TestEmbedModelsRootPicker(t *testing.T) {
	oldPick := pickEmbedModelsFolder
	t.Cleanup(func() { pickEmbedModelsFolder = oldPick })

	missing := &api.EmbedMissingModel{Model: "gte-multilingual-base", Path: `D:\models\model.onnx`}

	t.Run("写回绝对路径", func(t *testing.T) {
		dir := t.TempDir()
		picked := make(chan struct{})
		pickEmbedModelsFolder = func(*mygo.Window, string, string) (string, error) {
			close(picked)
			return dir, nil
		}
		reg := &embedRegReader{missing: missing}
		a, tt, pump := embedUIApp(t, reg)
		pump()
		if err := tt.Click("指定模型目录"); err != nil {
			t.Fatal(err)
		}
		<-picked
		remoteWait(t, "写入到达后台、结果回到界面", func() bool { pump(); w, _ := reg.snapshot(); return len(w) >= 1 && !a.embedUI.busy })
		pump()
		writes, versions := reg.snapshot()
		if len(writes) != 1 || writes[0].Key != embedModelsRootKey {
			t.Fatalf("写的是 %+v，要一条 %s", writes, embedModelsRootKey)
		}
		var got string
		if err := json.Unmarshal(writes[0].Value, &got); err != nil {
			t.Fatalf("写进去的不是 JSON 字符串：%s（%v）", writes[0].Value, err)
		}
		if got != filepath.Clean(dir) {
			t.Fatalf("写进 %q，要 %q", got, filepath.Clean(dir))
		}
		if len(versions) != 1 || versions[0].Config != "0" {
			t.Fatalf("带的版本是 %+v，要读到的那份 0", versions)
		}
		if !tt.HasText("重新开始接收后生效") {
			t.Fatalf("成功没提示重新开始接收：%q", tt.Texts())
		}
	})

	t.Run("取消不写", func(t *testing.T) {
		picked := make(chan struct{})
		pickEmbedModelsFolder = func(*mygo.Window, string, string) (string, error) {
			close(picked)
			return "", nil
		}
		reg := &embedRegReader{missing: missing}
		a, tt, pump := embedUIApp(t, reg)
		pump()
		if err := tt.Click("指定模型目录"); err != nil {
			t.Fatal(err)
		}
		<-picked
		pump()
		writes, _ := reg.snapshot()
		if len(writes) != 0 || a.embedUI.busy {
			t.Fatalf("取消了还写：%+v busy=%v", writes, a.embedUI.busy)
		}
		if tt.HasText("重新开始接收后生效") {
			t.Fatalf("取消了却提示生效：%q", tt.Texts())
		}
	})

	t.Run("写失败显示原因", func(t *testing.T) {
		picked := make(chan struct{})
		pickEmbedModelsFolder = func(*mygo.Window, string, string) (string, error) {
			close(picked)
			return t.TempDir(), nil
		}
		reg := &embedRegReader{missing: missing, fail: errors.New("编的失败")}
		a, tt, pump := embedUIApp(t, reg)
		pump()
		if err := tt.Click("指定模型目录"); err != nil {
			t.Fatal(err)
		}
		<-picked
		remoteWait(t, "写入失败回到界面", func() bool { pump(); w, _ := reg.snapshot(); return len(w) >= 1 && !a.embedUI.busy })
		pump()
		if !tt.HasText("没有改成：编的失败") {
			t.Fatalf("写失败没写原因：%q", tt.Texts())
		}
		if tt.HasText("重新开始接收后生效") {
			t.Fatalf("写失败却提示生效：%q", tt.Texts())
		}
	})
}

// 200 字符的路径在 2048、1100、900 三个宽度下都只占一行、右边缘留在窗口里（设计自查第 8、11 条）。
func TestEmbedMissingModelPathFitsWidth(t *testing.T) {
	path := `C:\` + strings.Repeat("很长的模型目录\\", 24) + "model_quantized.onnx"
	if n := len([]rune(path)); n < 200 {
		t.Fatalf("造出来的路径只有 %d 字，要 200 字以上", n)
	}
	reg := &embedRegReader{missing: &api.EmbedMissingModel{Model: "gte-multilingual-base", Path: path}}
	for _, w := range []int{2048, 1100, 900} {
		_, tt, pump := embedUIAppAt(t, reg, w, 2400)
		pump()
		base, ok := tt.Find("记忆检索用的向量后端")
		if !ok {
			t.Fatalf("%d 宽：找不到嵌入后端那一行", w)
		}
		e := boxOf(t, tt, "embed-missing-path")
		if e.Box.H > base.H*1.4 {
			t.Errorf("%d 宽：路径折行了，高 %.1f（名字一行 %.1f）", w, e.Box.H, base.H)
		}
		if right := e.Box.X + e.Box.W; right > float32(w) {
			t.Errorf("%d 宽：路径撑出窗口，右边缘 %.1f", w, right)
		}
		t.Logf("%d 宽：路径宽 %.1f，高 %.1f，名字一行高 %.1f，右边缘 %.1f", w, e.Box.W, e.Box.H, base.H, e.Box.X+e.Box.W)
	}
}
