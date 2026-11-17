package linkplugin

// 复核（data/dev/review-bigimage-572d948b.md）问题 5、6：等回声时展开被取消、插件关闭后晚到的回声。

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
)

// 等回声的 30 秒里展开被取消（源消息撤回、展开关了）：AwaitPart 是被打断提前返回的，不是等满了没回声。
// 不记“这张图被拒”（6 小时内这张图都不再原样发）、不换形式、不留看守；回执是结果不明。
// 改坏检查（复核问题 6）：去掉 confirm 里等不到回声后看取消的那一支，这条红（记了被拒、还发了转图）
func TestCancelledWhileWaitingForEchoIsNotRejected(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, bigScript(&f), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	f.out.onGiveUp = func(string, int) { cancel() } // 假出口不认 ctx：在“等回声”返回前取消，等于等的时候被打断
	run := &expansionRun{source: storedMsg(31, nil), ctx: ctx, cancel: cancel}
	part := textAndPics("编的正文", bigPicture(t))
	var res []*types.MediaResource
	for _, s := range mediaSegs(part) {
		res = append(res, s.Media)
	}
	rep, err := f.r.p.deliver(run, retryInput{snapshot: expandSettings(nil)(), result: &linkreader.Result{Platform: "x", CleanURL: "https://x.com/a/status/3101"}}, []types.Part{part}, res)
	if err != nil {
		t.Fatal(err)
	}
	if s := f.out.sent(); len(s) != 1 {
		t.Fatalf("被取消了不换形式：%d 帧 %+v", len(s), s)
	}
	if !f.r.p.verdicts.empty() {
		t.Fatal("被取消不是被拒：不记“这张图被拒”")
	}
	if f.r.p.guards.get("p1", 0, f.clock) != nil {
		t.Fatal("不会再补发，看守不用留")
	}
	if !rep.unsure || rep.landed || rep.fallback {
		t.Fatalf("结果不明、没有一段确认：%+v", rep)
	}
}

// 插件已经关闭以后才到的回声：不再起撤回协程（Close 等的就是这些协程），但要留一行日志，不能静默丢掉。
// 改坏检查（复核问题 5）：goLate 里去掉关闭时那行日志，这条红
func TestLateEchoAfterCloseLogsInsteadOfSilence(t *testing.T) {
	t.Parallel()
	var f *flow
	f = newFlow(t, bigScript(&f), nil)
	f.deliver(nil, textAndPics("编的正文", bigPicture(t)))
	var logs []string
	f.r.p.opt.Logf = func(format string, args ...any) { logs = append(logs, format) }
	f.r.p.mu.Lock()
	f.r.p.closed = true
	f.r.p.mu.Unlock()
	f.out.hook(lateOrig("-90001"))
	if got := f.dupSnapshot(); len(got) != 0 {
		t.Fatalf("插件关闭后不撤：%+v", got)
	}
	found := false
	for _, l := range logs {
		found = found || strings.Contains(l, "插件在关闭")
	}
	if !found {
		t.Fatalf("关闭后晚到的回声要记一行日志：%q", logs)
	}
}
