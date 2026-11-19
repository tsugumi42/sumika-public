package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"sumika-native/internal/api"
	"sumika-native/internal/config"
)

// 翻译模型的选择（规格-链接翻译）：linkReader.expand.translate.model 空 = 跟后台模型，填了就用它。
// 走完整装配：设置登记写键 → 配置 → 展开一条帖 → 实际选定的模型 ID。
func TestLinkTranslateModelChoice(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	var seen []string
	gen := func(_ context.Context, model, _, _ string) (string, error) {
		mu.Lock()
		seen = append(seen, model)
		mu.Unlock()
		if model == "编的不存在模型" {
			return "", errors.New("模型不可用")
		}
		return "阿尔忒弥斯二号完成了绕月飞行，乘组正在返程。", nil
	}
	e, a, peer := newTranslateHost(t, enText, nil)
	a.opt.LinkTranslateGenerate = gen
	d := a.Desktop()
	defer d.Close()
	r := &d.Reader
	ctx := context.Background()

	run := func(id, status, wantModel string, wantText bool) {
		t.Helper()
		mu.Lock()
		before := len(seen)
		mu.Unlock()
		sendLink(t, e, a, id, "https://x.com/paper/status/"+status)
		waitFor(t, "原文发出", func() bool { return linkSawFrames(peer, "https://x.com/paper/status/"+status) })
		if wantText {
			waitFor(t, "译文发出", func() bool { return linkSawFrames(peer, "绕月飞行") })
		}
		mu.Lock()
		got := append([]string(nil), seen[before:]...)
		mu.Unlock()
		if len(got) != 1 || got[0] != wantModel {
			t.Fatalf("用了模型 %q，要 %q", got, wantModel)
		}
	}
	setModel := func(v string) {
		t.Helper()
		reg, err := r.ReadSettingsRegistry(ctx)
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(v)
		if _, err := r.WriteSetting(ctx, api.SettingChange{Key: "linkReader.expand.translate.model", Value: raw}, reg.Version); err != nil {
			t.Fatalf("写翻译模型 %q：%v", v, err)
		}
		if got := a.currentLinks().Values.Expand.Translate.Model; got != v {
			t.Fatalf("配置里的翻译模型 %q，要 %q", got, v)
		}
	}

	// 默认空串、后台模型也没配：没有模型可用，原文照发、不调模型
	if got := config.DefaultLinks().Expand.Translate.Model; got != "" {
		t.Fatalf("默认应为空串（跟后台模型）：%q", got)
	}
	// 空串：跟后台模型
	mv, err := r.ModelSettings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	modelSave(t, r, mv, config.ModelChange{Field: config.BackgroundModel, String: new(testModel)})
	run("9301", "77301", testModel, true)
	// 填了：用指定的模型（后台模型不变）
	setModel("编的翻译模型")
	run("9302", "77302", "编的翻译模型", true)
	// 填了不存在的模型：和后台模型配错一样，生成报错、只发原文、运行页记一行翻译失败
	setModel("编的不存在模型")
	run("9303", "77303", "编的不存在模型", false)
	waitFor(t, "运行页记一行翻译失败", func() bool { return taskRunCount(t, a, "class='translate' AND outcome='failed'") == 1 })
	time.Sleep(200 * time.Millisecond)
	if linkSawFrames(peer, "模型不可用") || strings.Contains(strings.Join(peer.snapshot(), "\n"), "翻译失败") {
		t.Fatal("失败时不该把错误写进消息")
	}
	// 清回空串：又跟后台模型
	setModel("")
	run("9304", "77304", testModel, true)
}

// 同一个翻译模型 ID 不带首尾空白才收；带了的写入被拒，不落盘
func TestLinkTranslateModelRejectsBadID(t *testing.T) {
	t.Parallel()
	_, a, _ := newTranslateHost(t, enText, nil)
	d := a.Desktop()
	defer d.Close()
	r := &d.Reader
	ctx := context.Background()
	reg, err := r.ReadSettingsRegistry(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{" 带空格", "带换行\n模型", strings.Repeat("长", 201)} {
		raw, _ := json.Marshal(bad)
		if _, err := r.WriteSetting(ctx, api.SettingChange{Key: "linkReader.expand.translate.model", Value: raw}, reg.Version); err == nil {
			t.Errorf("%q 应被拒绝", bad)
		}
	}
	if got := a.currentLinks().Values.Expand.Translate.Model; got != "" {
		t.Fatalf("被拒的值落盘了：%q", got)
	}
}

// translateModel 本身：空白串当空（手写配置里的纯空白被配置校验挡掉，这里只兜底）
func TestTranslateModelFallsBackToBackground(t *testing.T) {
	t.Parallel()
	_, a, _ := newTranslateHost(t, enText, nil)
	if got := a.translateModel("  "); got != a.backgroundModel(a.character) {
		t.Fatalf("纯空白应跟后台模型：%q", got)
	}
	if got := a.translateModel("x-model"); got != "x-model" {
		t.Fatalf("填了应用它：%q", got)
	}
}
