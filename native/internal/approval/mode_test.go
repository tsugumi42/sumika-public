package approval

// 审批档当场生效：改运行中的默认档、按会话覆盖、子任务沿用派它的会话；不是主人发起的不吃（ADR-269）。

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"sumika-native/internal/jsjson"
	"sumika-native/internal/kernel"
	"sumika-native/internal/policy"
)

func modeWriteCall(t *testing.T, path string, id kernel.Invocation) Call {
	t.Helper()
	args, err := jsjson.Parse(`{"path":` + quote(path) + `,"content":"x"}`)
	if err != nil {
		t.Fatal(err)
	}
	return Call{Identity: id, ID: "c1", Name: "write", Args: args}
}

func TestModeSwitchAppliesNow(t *testing.T) {
	g := NewGate(t.TempDir())
	defer g.Analyzer.Close()
	work := t.TempDir()
	file := filepath.Join(work, "a.txt")
	owner := policy.Audience{Channel: "console", PrivateOk: true}
	asked := 0
	g.Ask = func(context.Context, Request, *Pending) Decision { asked++; return Decision{Kind: "denied"} }
	check := func(conv string, id kernel.Invocation) (blocked bool) {
		block, _ := g.Check(context.Background(), work, "suzu", owner, conv, modeWriteCall(t, file, id))
		return block
	}
	conv := "console:suzu"
	if !check(conv, kernel.Invocation{}) || asked != 1 {
		t.Fatalf("手动档写文件要问：问了 %d 次", asked)
	}
	// 运行中改默认档：不用重建门
	if err := g.SetMode("auto"); err != nil {
		t.Fatal(err)
	}
	if check(conv, kernel.Invocation{}) || asked != 1 {
		t.Fatalf("改成自动档以后工作目录里写文件不该再问：问了 %d 次", asked)
	}
	// 会话自己的覆盖压过默认
	if err := g.SetSessionMode(conv, "manual"); err != nil {
		t.Fatal(err)
	}
	if !check(conv, kernel.Invocation{}) || asked != 2 {
		t.Fatalf("这个会话覆盖成手动档，应当问：%d", asked)
	}
	if got := g.ModeFor("console:suzu:topic:other"); got != "auto" {
		t.Fatalf("别的话题不受影响：%s", got)
	}
	// 去掉覆盖回到默认
	g.SetSessionMode(conv, "")
	if check(conv, kernel.Invocation{}) || asked != 2 {
		t.Fatal("去掉覆盖后回到默认的自动档")
	}
	for _, bad := range []string{"yolo2", "AUTO", " "} {
		if g.SetMode(bad) == nil || g.SetSessionMode(conv, bad) == nil {
			t.Fatalf("不认识的档 %q 被接受了", bad)
		}
	}
	if g.SetSessionMode("", "yolo") == nil {
		t.Fatal("没有会话也能设覆盖")
	}
	if g.DefaultMode() != "auto" {
		t.Fatalf("坏值不该改动默认档：%s", g.DefaultMode())
	}
}

// 子任务沿用派它的会话的档；没有派它的会话就用默认。
func TestModeChildInheritsParent(t *testing.T) {
	g := NewGate(t.TempDir())
	defer g.Analyzer.Close()
	g.ParentOf = func(c string) string {
		if c == "internal:t1" {
			return "console:suzu"
		}
		return ""
	}
	g.SetSessionMode("console:suzu", "yolo")
	if got := g.ModeFor("internal:t1"); got != "yolo" {
		t.Fatalf("子任务应沿用父会话：%s", got)
	}
	if got := g.ModeFor("internal:t2"); got != "manual" {
		t.Fatalf("找不到父会话用默认：%s", got)
	}
	if got := g.ModeFor("console:other"); got != "manual" {
		t.Fatalf("别的会话不受影响：%s", got)
	}
}

// 非主人发起的：覆盖成 yolo 也只问一次、不放行（身份在模式之前判）。
func TestModeOverrideNeverWidensNotOwner(t *testing.T) {
	g := NewGate(t.TempDir())
	defer g.Analyzer.Close()
	work := t.TempDir()
	conv := "console:suzu:group:g"
	g.SetSessionMode(conv, "yolo")
	g.SetMode("yolo")
	asked := 0
	g.Ask = func(_ context.Context, req Request, _ *Pending) Decision {
		asked++
		if len(req.Options) != 0 {
			t.Errorf("非主人发起不给范围档：%v", req.Options)
		}
		return Decision{Kind: "denied"}
	}
	id := kernel.Invocation{Scope: kernel.Scope{Scene: "group", Conversation: conv, ActorKind: "member"}}
	block, _ := g.Check(context.Background(), work, "suzu", policy.Audience{Channel: "console", PrivateOk: true}, conv, modeWriteCall(t, filepath.Join(work, "a.txt"), id))
	if !block || asked != 1 {
		t.Fatalf("yolo 覆盖下群聊也要问：block=%v asked=%d", block, asked)
	}
}

// 读写并发：运行中切档不能出数据竞争（-race 下才有意义）。
func TestModeConcurrentSwitch(t *testing.T) {
	g := NewGate(t.TempDir())
	defer g.Analyzer.Close()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				g.SetMode([]string{"manual", "auto", "yolo"}[j%3])
				g.SetSessionMode("c", "auto")
				g.SetSessionMode("c", "")
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				_ = g.ModeFor("c")
				_ = g.ModeFor("internal:x")
			}
		}()
	}
	wg.Wait()
}
