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
