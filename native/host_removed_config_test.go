package main

// 配置里还写着已删的 backend.local: bun（ADR-401）：照常用 Go 跑，状态里带说明。

import (
	"os"
	"strings"
	"testing"

	backend "sumika-native/internal/app"
	"sumika-native/internal/paths"
)

// 往宿主夹具的配置末尾追加一段
func appendConfig(t *testing.T, opt backend.Options, extra string) {
	t.Helper()
	p, err := paths.Open(opt.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p.ConfigFile(), append(b, []byte(extra)...), 0600); err != nil {
		t.Fatal(err)
	}
}

// 配置写 backend.local: bun（连同 bun、server 路径）：照常用 Go 开后台、不 blocked、不起任何子进程；
// 状态里带一行“Bun 后台已移除、现在由 Go 处理”和怎么解除；发送的闸门和没写时完全一样；没写这些键就没有说明
func TestHostConfigLocalBunRunsAsGoWithNote(t *testing.T) {
	t.Parallel()
	plain := hostFixture(t)
	var plainLog stateLog
	hp := newHost(plain, plainLog.add)
	hp.start()
	defer hp.shutdown()
	want := plainLog.last()

	opt := hostFixture(t)
	appendConfig(t, opt, "backend:\n  local: bun\n  bun: 'D:/不存在/bun.exe'\n  server: D:/不存在/server\n")
	var log stateLog
	h := newHost(opt, log.add)
	h.start()
	defer h.shutdown()
	s := log.last()
	if s.Phase == phaseBlocked || s.Phase != want.Phase || s.Mode != want.Mode || s.Reader == nil {
		t.Fatalf("写 bun 该和没写一样用 Go 开起来：%+v，没写时 %+v", s, want)
	}
	if _, ok := s.Reader.(*rpcBridge); !ok {
		t.Fatalf("界面数据源该是 Go 的进程内桥：%T", s.Reader)
	}
	if s.localSendOff() != want.localSendOff() || s.localSendOff() == "" {
		t.Fatalf("发送闸门该和没写时一样（只看历史不能发）：%q 对 %q", s.localSendOff(), want.localSendOff())
	}
	if len(s.ConfigNotes) != 3 {
		t.Fatalf("三个已删键各一行说明：%q", s.ConfigNotes)
	}
	for _, key := range []string{"backend.local", "backend.bun", "backend.server"} {
		found := false
		for _, n := range s.ConfigNotes {
			found = found || (strings.HasPrefix(n, key) && strings.Contains(n, "删掉"))
		}
		if !found {
			t.Errorf("缺 %s 的说明（要点名并写怎么解除）：%q", key, s.ConfigNotes)
		}
	}
	if !strings.Contains(s.ConfigNotes[0], "Bun 后台已移除，现在由 Go 处理") {
		t.Fatalf("backend.local 的说明该写明现在由 Go 处理：%q", s.ConfigNotes[0])
	}
	if len(want.ConfigNotes) != 0 {
		t.Fatalf("没写这些键就不该有说明：%q", want.ConfigNotes)
	}
}
