package main

import (
	"testing"

	backend "sumika-native/internal/app"
)

// 状态页：有平台库没打开时多一行写明平台和原因；没有时不出这一行。
func TestStatusBodyShowsUnavailablePlatforms(t *testing.T) {
	a := &app{postFn: func(fn func()) { fn() }}
	sh := newShell(a, backend.Options{}, false)
	sh.host = newHost(sh.opt, func(hostState) {})
	defer sh.host.cancel()
	line := "qq 的库没打开：第 9 号迁移失败：编的；快照在 C:\\data\\qq-failed.sqlite（sumika-ctl db status 看详情）"
	a.host = hostState{Phase: phaseArchive, Reason: "只看历史", StoreUnavailable: []string{line}}
	tt := testerOf(2048, 1030, a.statusBody)
	if !tt.HasText(line) {
		t.Fatalf("状态页没有不可用平台那一行：%v", tt.Texts())
	}
	a.host.StoreUnavailable = nil
	tt = testerOf(2048, 1030, a.statusBody)
	if tt.HasText(line) || tt.HasText("qq 的库没打开") {
		t.Fatalf("没有不可用平台时还写着那一行：%v", tt.Texts())
	}
}

// 状态页：配置里还写着已删的键时多一组“配置里的旧项”：键名当标题、说明放 hint；没有时不出这一组。
// 窄窗口（420、900）下整句说明和标题都在窗口里，说明折行但一个字不丢
func TestStatusBodyShowsRemovedConfigNotes(t *testing.T) {
	a := &app{postFn: func(fn func()) { fn() }}
	sh := newShell(a, backend.Options{}, false)
	sh.host = newHost(sh.opt, func(hostState) {})
	defer sh.host.cancel()
	why := "写着 bun：本地私聊的 Bun 后台已移除，现在由 Go 处理；可以把 backend.local 从 sumika.yml 里删掉。注意：Bun 上建的提醒、目标、定期任务和“记住”的授权 Go 看不到；Go 会按默认配置启动提醒、主动联系、本地群聊、悄悄话等功能，不想要的去设置里关掉"
	note := "backend.local " + why
	a.host = hostState{Phase: phaseArchive, Reason: "只看历史", ConfigNotes: []string{note}}
	for _, w := range []int{2048, 900, 420} {
		tt := testerOf(w, 3000, a.statusBody)
		if !tt.HasText("配置里的旧项") {
			t.Fatalf("%d 宽：状态页没有已删键那一组：%v", w, tt.Texts())
		}
		title, ok := tt.Find("backend.local")
		if !ok || title.X+title.W > float32(w) {
			t.Fatalf("%d 宽：键名该单独当标题且在窗口里：%v %+v", w, ok, title)
		}
		hint, ok := tt.Find(why)
		if !ok {
			t.Fatalf("%d 宽：整句说明该完整出现：%v", w, tt.Texts())
		}
		if hint.X < 0 || hint.X+hint.W > float32(w) {
			t.Errorf("%d 宽：说明撑出窗口：左 %.1f 右 %.1f", w, hint.X, hint.X+hint.W)
		}
		if tt.HasText(note) {
			t.Errorf("%d 宽：整句不该再塞进标题", w)
		}
	}
	a.host.ConfigNotes = nil
	tt := testerOf(2048, 1030, a.statusBody)
	if tt.HasText("配置里的旧项") {
		t.Fatalf("没有已删键时还写着那一组：%v", tt.Texts())
	}
}

func TestSplitConfigNote(t *testing.T) {
	if k, w := splitConfigNote("backend 已不再使用：删掉"); k != "backend" || w != "已不再使用：删掉" {
		t.Fatalf("拆错了：%q %q", k, w)
	}
	if k, w := splitConfigNote("单独一个词"); k != "单独一个词" || w != "" {
		t.Fatalf("没有空格该整句当键名：%q %q", k, w)
	}
}
