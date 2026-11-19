package main

// 宿主启动时读一次 backend.local（规格-本地私聊切到Bun 第二节“只在启动时读”）：之后后台每次重开（回 archive 等）
// 都照那次的决定走，不再自己重读（复核 R1：运行中改了 sumika.yml，宿主按 go 没拉 Bun、后台按 bun 开成“本地归 Bun”，没人管本地）。

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/paths"
)

// setBackendLocal 把配置文件里的 backend 段改成 local: <owner>（运行中手改 sumika.yml）
func setBackendLocal(t *testing.T, dataDir, owner string) {
	t.Helper()
	p, err := paths.Open(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(p.ConfigFile())
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(strings.ReplaceAll(string(b), "backend:\n  local: bun\n", ""), "backend:\n  local: go\n", "")
	if err := os.WriteFile(p.ConfigFile(), []byte(text+"backend:\n  local: "+owner+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

// reopenArchive 走一遍 stopReceive / 接收停了之后的回 archive 路径：后台重开，重读配置的话就会在这里读到新值
func reopenArchive(t *testing.T, h *host) {
	t.Helper()
	h.op.Lock()
	defer h.op.Unlock()
	if err := h.closeCurrent(); err != nil {
		t.Fatal(err)
	}
	h.fallback("测试：重新打开 archive")
	if h.cur == nil {
		t.Fatalf("archive 没能重开：%+v", h.current())
	}
}

func sendLocalErr(t *testing.T, h *host) error {
	t.Helper()
	k := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:probe"}
	return h.desk.Reader.SendLocal(context.Background(), k, "你好", nil)
}

func TestHostKeepsStartupLocalOwnerWhenConfigChangesGoToBun(t *testing.T) {
	t.Parallel()
	opt := hostFixture(t)
	var log stateLog
	h := newHost(opt, log.add)
	h.bunSpawn = func(bunSpec, func(string, ...any)) (bunProc, error) {
		t.Error("宿主启动时是 go，不该拉 Bun")
		return nil, errors.New("x")
	}
	h.start()
	t.Cleanup(func() { _ = h.shutdown() })
	setBackendLocal(t, opt.DataDir, "bun")
	reopenArchive(t, h)
	// 宿主按 go（没拉 Bun、桥不带 Bun），后台也得按 go：本地的写方法不能回“现在由 Bun 管”（那就没人管了）
	if h.localByBun {
		t.Fatal("宿主启动时读到的是 go")
	}
	if err := sendLocalErr(t, h); errors.Is(err, api.ErrLocalOwnedByBun) {
		t.Fatalf("宿主按 go 没拉 Bun，后台却按 bun 开了：本地私聊没人管：%v", err)
	}
	if !errors.Is(sendLocalErr(t, h), api.ErrLocalChatUnsupported) {
		t.Fatalf("go 模式的 archive 该回“请先启动后台”那类错：%v", sendLocalErr(t, h))
	}
}

func TestHostKeepsStartupLocalOwnerWhenConfigChangesBunToGo(t *testing.T) {
	t.Parallel()
	h, log, probe := bunHostFixture(t)
	h.start()
	t.Cleanup(func() { _ = h.shutdown() })
	waitHost(t, "Bun 连上", func() bool { return log.last().LocalOK })
	setBackendLocal(t, h.opt.DataDir, "go")
	reopenArchive(t, h)
	if !h.localByBun || probe.count() != 1 {
		t.Fatalf("宿主启动时读到的是 bun，Bun 子进程在：localByBun=%v 子进程 %d 个", h.localByBun, probe.count())
	}
	// 后台也得按 bun：Go 不能因为配置现在写 go 就拿 local.lock、开本地的写（Bun 还在写，两边同写）
	if err := sendLocalErr(t, h); !errors.Is(err, api.ErrLocalOwnedByBun) {
		t.Fatalf("宿主拉了 Bun，后台就该按 bun 走（写方法回 Bun 管）：%v", err)
	}
}
