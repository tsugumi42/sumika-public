package app

// 本地私聊的写锁 go/local.lock，和配置里还写着已删的 backend.local: bun（ADR-401）：
//   - receive 拿 backend.lock 和 local.lock，archive 只拿 backend.lock；
//   - 配置写 bun 也照 Go 的方式开：local.db 可写、装齐目标 / 定期任务 / 提醒 / 本地群聊 / 悄悄话，日志记一行“已移除”。

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"sumika-native/internal/datalock"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/paths"
	"sumika-native/internal/store"
)

func localStoreOf(t *testing.T, a *App) *store.Store {
	t.Helper()
	st, ok := a.set.Platform("local")
	if !ok {
		t.Fatal("没有 local 库")
	}
	return st
}

func mustPaths(t *testing.T, data string) paths.Paths {
	t.Helper()
	p, err := paths.Open(data)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// receive：两把锁都拿，local.db 可写，本地的几样功能都装上
func TestReceiveKeepsBothLocksAndWrites(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a, _ := localDesktop(t, e, llmtest.Script{Reply: llmtest.Text("好")})
	if _, err := datalock.LockData(e.data); !errors.Is(err, datalock.ErrDataInUse) {
		t.Fatalf("backend.lock 该被占：%v", err)
	}
	if _, err := datalock.LockLocal(e.data); !errors.Is(err, datalock.ErrLocalInUse) {
		t.Fatalf("receive 该占着 local.lock，别的写本地的程序拿不到：%v", err)
	}
	if localStoreOf(t, a).ReadOnly() {
		t.Fatal("receive 下 local.db 该可写")
	}
	if a.goals == nil || a.loops == nil || a.reminders == nil || a.groups == nil || a.whisper == nil {
		t.Fatal("receive 该装目标、定期任务、提醒、本地群聊、悄悄话")
	}
}

// 配置里还写着 backend.local: bun（连同 bun、server）：和没写时一样的开法，两把锁、可写库、本地功能都在；
// 日志记一行“Bun 后台已移除，现在由 Go 处理”和解除办法；Config 里留着说明给状态页
func TestConfigLocalBunBehavesLikeGo(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	// 同 localDesktop，只是配置里多写已删的 backend 段（localDesktop 自己会重写配置，加不进去）
	script := llmtest.Script{Reply: llmtest.Text("好"), Models: []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768}`)}}
	srv := httptest.NewServer(llmtest.Handler(script, &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	e.configure("tools:\n  workdir: '" + filepath.ToSlash(t.TempDir()) + "'\nbackend:\n  local: bun\n  bun: 'D:/不存在/bun.exe'\n  server: D:/不存在/server\n")
	a := e.open()
	d := a.Desktop()
	t.Cleanup(func() { d.Close(); a.Close() })
	if _, err := datalock.LockLocal(e.data); !errors.Is(err, datalock.ErrLocalInUse) {
		t.Fatalf("写 bun 也该占 local.lock（本地私聊在 Go 里写）：%v", err)
	}
	if localStoreOf(t, a).ReadOnly() {
		t.Fatal("写 bun 也该用可写的 local.db")
	}
	if a.goals == nil || a.loops == nil || a.reminders == nil || a.groups == nil || a.whisper == nil {
		t.Fatal("写 bun 也该装目标、定期任务、提醒、本地群聊、悄悄话")
	}
	if !e.logContains("Bun 后台已移除，现在由 Go 处理") || !e.logContains("删掉") {
		t.Fatalf("日志该记一行已移除和解除办法：%q", e.logSnapshot())
	}
	if len(a.cfg.RemovedNotes) != 3 {
		t.Fatalf("三个已删键各一行说明：%q", a.cfg.RemovedNotes)
	}
}

// 别的程序占着 local.lock（比如 memory-migrate 正在写本地）：receive 起不来并说清是哪把锁，backend.lock 随后放掉
func TestReceiveRefusesWhenLocalLockHeld(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	held, err := datalock.LockLocal(e.data)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Close() })
	_, err = Open(context.Background(), e.options())
	if !errors.Is(err, datalock.ErrLocalInUse) {
		t.Fatalf("local.lock 被占该拒绝启动并报 ErrLocalInUse：%v", err)
	}
	l, err := datalock.LockData(e.data)
	if err != nil {
		t.Fatalf("启动失败后 backend.lock 该放掉：%v", err)
	}
	_ = l.Close()
}

// archive 只读：不拿也不建 local.lock（之后切到 receive 才拿）
func TestArchiveDoesNotTouchLocalLock(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	set, err := store.OpenGoWith(mustPaths(t, e.data), store.DefaultRegistry)
	if err != nil {
		t.Fatal(err)
	}
	_ = set.Close()
	opt := e.options()
	opt.Mode, opt.Account = Archive, ""
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.Close() })
	l, err := datalock.LockLocal(e.data)
	if err != nil {
		t.Fatalf("archive 不该占 local.lock：%v", err)
	}
	_ = l.Close()
}
