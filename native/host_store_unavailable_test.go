package main

import (
	"strings"
	"testing"

	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

// makeHostStoreTooNew 把宿主临时数据目录里的一个平台库改成“太新”。
func makeHostStoreTooNew(t *testing.T, data, platform string) {
	t.Helper()
	p, err := paths.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	db, err := store.OpenChatFile(p.ChatDB(platform), platform)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE sumika_schema SET min_reader = 999`); err != nil {
		db.Close()
		t.Fatal(err)
	}
	db.Close()
}

// W16：宿主把不可用平台的行带进状态（状态页照这份状态写），只看历史启动不被挡住。
func TestHostArchiveCarriesUnavailableStore(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	makeHostStoreTooNew(t, opt.DataDir, "qq")
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	s := log.last()
	if s.Phase != phaseArchive || s.Reader == nil {
		t.Fatalf("一个平台库太新就把只看历史挡住了：%+v", s)
	}
	if len(s.StoreUnavailable) != 1 || !strings.Contains(s.StoreUnavailable[0], "qq 的库没打开") ||
		!strings.Contains(s.StoreUnavailable[0], "sumika-ctl db status") {
		t.Fatalf("宿主状态没带不可用平台的行：%v", s.StoreUnavailable)
	}
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
}

// 所有平台库都不可用：宿主进 blocked，原因里逐个写出每个平台的原因。
func TestHostBlockedNamesEveryUnavailableStore(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	makeHostStoreTooNew(t, opt.DataDir, "qq")
	makeHostStoreTooNew(t, opt.DataDir, "local")
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	s := log.last()
	if s.Phase != phaseBlocked || s.Reader != nil {
		t.Fatalf("所有平台库都不可用时没有被挡住：%+v", s)
	}
	for _, want := range []string{"后台没在运行", "local 的库没打开（too_new）", "qq 的库没打开（too_new）"} {
		if !strings.Contains(s.Reason, want) {
			t.Fatalf("宿主原因少写 %q：%s", want, s.Reason)
		}
	}
	if err := h.shutdown(); err != nil {
		t.Fatal(err)
	}
}
