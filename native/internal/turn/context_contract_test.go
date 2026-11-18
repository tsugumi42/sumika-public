package turn

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 屏障落在钩子已经返回、内核尚未追加的位置；回调里直接检查实例。
func TestContextAcceptedOnlyAfterAppend(t *testing.T) {
	t.Parallel()
	for _, steer := range []bool{false, true} {
		for _, action := range []string{"cancel", "drop", "accept"} {
			t.Run(action+map[bool]string{false: "/start", true: "/steer"}[steer], func(t *testing.T) {
				t.Parallel()
				h := newHarness(t, false, nil)
				ready, release := make(chan struct{}), make(chan struct{})
				accepted, readyCount := 0, 0
				const line = "等接纳的受控资料"
				h.use(contextPlugin("ledger", func(_ context.Context, in ContextIn) (*Contribution, error) {
					if in.Steer != steer {
						if steer {
							h.hub.Steer(h.c.CharacterID, "补一句")
						}
						return nil, nil
					}
					return &Contribution{Lines: []string{line}, OnAccepted: func() {
						accepted++
						l := h.hub.active[convKey(h.c.CharacterID, consoleConv(h.c.CharacterID))]
						found := false
						for _, m := range l.msgs {
							found = found || strings.Contains(m.Text, line)
						}
						if !found {
							t.Error("OnAccepted 早于消息追加")
						}
						if action == "accept" {
							h.hub.running(h.c.CharacterID, consoleConv(h.c.CharacterID)).cancel()
						}
					}}, nil
				}))
				cleanupStarted, cleanupRelease, stopped := make(chan struct{}), make(chan struct{}), make(chan struct{})
				if action == "drop" {
					h.hub.plugins.onCleanup("ledger", func() {
						close(cleanupStarted)
						select {
						case <-cleanupRelease:
						case <-time.After(barrierLimit):
							t.Error("插件清理屏障没有放行")
						}
					})
				}
				h.hub.testHook = func(point string) {
					if point != "contextReady" {
						return
					}
					readyCount++
					if steer && readyCount == 1 {
						return
					}
					close(ready)
					select {
					case <-release:
					case <-time.After(barrierLimit):
						t.Error("接纳屏障没有放行")
					}
				}
				out := h.hub.Send(h.c.CharacterID, "开始")
				select {
				case <-ready:
				case <-time.After(barrierLimit):
					t.Fatal("没有走到追加前屏障")
				}
				if accepted != 0 {
					t.Error("钩子返回就提交了账本")
				}
				switch action {
				case "cancel":
					h.hub.running(h.c.CharacterID, consoleConv(h.c.CharacterID)).cancel()
				case "drop":
					go func() { h.hub.StopPlugin("ledger"); close(stopped) }()
					select {
					case <-cleanupStarted:
					case <-time.After(barrierLimit):
						t.Fatal("插件没有开始停用")
					}
				}
				close(release)
				select {
				case o := <-out:
					if o.Err != nil && action == "drop" {
						t.Fatal(o.Err)
					}
				case <-time.After(barrierLimit):
					t.Fatal("回合没有退出")
				}
				if action == "drop" {
					close(cleanupRelease)
					select {
					case <-stopped:
					case <-time.After(barrierLimit):
						t.Fatal("插件清理没有结束")
					}
				}
				want := 0
				if action == "accept" {
					want = 1
				}
				if accepted != want {
					t.Fatalf("接纳回调次数：got=%d want=%d", accepted, want)
				}
			})
		}
	}
}

func TestContextIDChangesOnlyWithContext(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + textReply + `,` + textReply + `,` + summaryReply + `,` + textReply + `]`, mutate: smallKeep})
	var ids []string
	h.use(contextPlugin("identity", func(_ context.Context, in ContextIn) (*Contribution, error) {
		if in.ContextID == "" || in.Workspace.CWD != h.wd {
			t.Errorf("上下文缺少宿主绑定：%+v", in)
		}
		ids = append(ids, in.ContextID)
		return nil, nil
	}))
	h.seedRounds(6)
	h.send("第一轮")
	h.send("第二轮")
	h.hub.Restart(h.c.CharacterID)
	h.send("重建后")
	c, err := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
	if err != nil || !c.OK {
		t.Fatalf("压缩没成：%+v %v", c, err)
	}
	h.send("压缩后")
	if len(ids) != 4 || ids[0] != ids[1] || ids[1] == ids[2] || ids[2] == ids[3] {
		t.Fatalf("同一实例应复用，重建、压缩应换代：%v", ids)
	}
}

func TestTimedOutContributionIsNeverAccepted(t *testing.T) {
	t.Parallel()
	h := newHarness(t, false, func(c *Config) { c.Hooks.Context = 50 * time.Millisecond })
	returned := make(chan struct{})
	accepted := false
	h.use(contextPlugin("timeout", func(ctx context.Context, _ ContextIn) (*Contribution, error) {
		<-ctx.Done()
		close(returned)
		return &Contribution{Lines: []string{"迟到资料"}, OnAccepted: func() { accepted = true }}, nil
	}))
	h.send("开始")
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("超时钩子未退出")
	}
	if accepted || strings.Contains(h.body(0), "迟到资料") {
		t.Fatal("超时贡献被接纳")
	}
}

// 手动压缩沿用会话的显式绑定；失败后原上下文仍可复用，不能回到全局缺省目录。
func TestCompactionKeepsWorkspaceBinding(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,{"content":["好"],` + usage + `},` + textReply + `]`, mutate: smallKeep})
	h.seedRounds(3)
	workspace := Workspace{ID: "chosen-project", CWD: t.TempDir()}
	var seen []ContextIn
	h.use(contextPlugin("binding", func(_ context.Context, in ContextIn) (*Contribution, error) {
		seen = append(seen, in)
		return nil, nil
	}))
	send := func(text string) {
		in := consoleInput(h.c.CharacterID, h.storeOwner(text), text)
		in.Workspace = workspace
		wait(t, h.hub.Respond(in))
	}
	send("选定目录后")
	before := h.hub.liveOf(h.c.CharacterID).contextID
	c, err := h.hub.Compact(h.c.CharacterID, consoleConv(h.c.CharacterID), "manual", 0)
	if err != nil || c.OK || !strings.Contains(c.Err, "太短") {
		t.Fatalf("应当走到摘要自检失败：%+v %v", c, err)
	}
	send("继续原来的会话")
	if !isPrefix(h.messages(0), h.messages(2)) || len(seen) != 2 || seen[1].ContextID != before || seen[1].Workspace.ID != workspace.ID || seen[1].Workspace.CWD != workspace.CWD {
		t.Fatalf("压缩失败丢了上下文或选定目录：before=%s seen=%+v", before, seen)
	}
}

func TestContextIDChangesOnInPlaceCompact(t *testing.T) {
	t.Parallel()
	h := newHarnessOpts(t, harnessOpts{replies: `[` + textReply + `,` + summaryReply + `,` + textReply + `]`})
	var ids []string
	h.use(contextPlugin("identity", func(_ context.Context, in ContextIn) (*Contribution, error) {
		ids = append(ids, in.ContextID)
		return nil, nil
	}))
	h.nearHard()
	before := h.hub.liveOf(h.c.CharacterID)
	h.send("过了门槛")
	if h.hub.liveOf(h.c.CharacterID) != before || len(ids) != 2 || ids[0] == "" || ids[0] == ids[1] || h.lastRebuild() != "compact" {
		t.Fatalf("原地换上下文也必须换代：ids=%v rebuild=%s", ids, h.lastRebuild())
	}
}

func TestContextAcceptedCanChangePlugins(t *testing.T) {
	t.Parallel()
	h := NewHub(context.Background(), Config{Logf: t.Logf}, nil, nil, nil)
	err := h.Use(contextPlugin("ledger", func(context.Context, ContextIn) (*Contribution, error) {
		return &Contribution{Lines: []string{"资料"}, OnAccepted: func() {
			h.StopPlugin("ledger")
			if err := h.Use(contextPlugin("next", func(context.Context, ContextIn) (*Contribution, error) { return nil, nil })); err != nil {
				t.Error(err)
			}
		}}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	pending := h.plugins.contextContributions(context.Background(), ContextIn{}, func(string) map[string]any { return map[string]any{} })
	done := make(chan struct{})
	go func() {
		h.plugins.acceptContext(context.Background(), pending, func(acceptedContext) {
			// 追加消息也不持注册表锁。
			h.plugins.mu.Lock()
			h.plugins.mu.Unlock()
		})
		close(done)
	}()
	recv(t, done, "接纳回调停用自己并注册新插件")
	h.StopPlugin("next")
}

func TestEmptyContextAcceptedAfterCancelledInput(t *testing.T) {
	t.Parallel()
	for _, steer := range []bool{false, true} {
		t.Run(map[bool]string{false: "start", true: "steer"}[steer], func(t *testing.T) {
			t.Parallel()
			h := newHarness(t, false, nil)
			accepted := 0
			h.use(contextPlugin("input", func(_ context.Context, in ContextIn) (*Contribution, error) {
				if in.Steer != steer {
					h.hub.Steer(h.c.CharacterID, "取消时的插话")
					return nil, nil
				}
				return &Contribution{OnAccepted: func() {
					l := h.hub.liveOf(h.c.CharacterID)
					if !strings.Contains(l.msgs[len(l.msgs)-1].Text, in.Text) || l.msgs[len(l.msgs)-1].Kind != KindSaid {
						t.Error("输入还没追加就接纳了空贡献")
					}
					accepted++
				}}, nil
			}))
			ready := 0
			h.hub.testHook = func(point string) {
				if point == "contextReady" {
					ready++
					if !steer || ready == 2 {
						h.hub.running(h.c.CharacterID, consoleConv(h.c.CharacterID)).cancel()
					}
				}
			}
			recv(t, h.hub.Send(h.c.CharacterID, "取消时的输入"), "取消回合退出")
			if accepted != 1 {
				t.Fatalf("输入仍在实例里，空贡献应接纳一次：%d", accepted)
			}
		})
	}
}
