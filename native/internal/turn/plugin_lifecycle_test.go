package turn

// #11：请求里的工具绑定当轮注册；同名重启不能换执行体，执行前仍要核听众。
import (
	"context"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"sumika-native/internal/audience"
	"sumika-native/internal/store"
	"sumika-native/internal/tools"
)

func TestPluginToolRegistrationBound(t *testing.T) {
	t.Parallel()
	for _, place := range []string{"本地私聊", "本地群聊", "QQ私聊", "QQ群"} {
		for _, change := range []string{"不换", "换成主窗口工具", "换成同权限工具", "同插件重启", "并行一批"} {
			t.Run(place+"/"+change, func(t *testing.T) {
				t.Parallel()
				var oldCalls, newCalls atomic.Int32
				var h *harness
				reply := callReply("recall_raw", `{"keywords":"x"}`)
				wantCalls := int32(1)
				if change == "并行一批" {
					reply = strings.Replace(reply, `}],`, `},{"id":"c2","name":"recall_raw","args":["{\"keywords\":\"y\"}"]}],`, 1)
					wantCalls = 2
				}
				changed := make(chan error, 1)
				h = newHarnessOpts(t, harnessOpts{replies: `[` + reply + `,` + textReply + `,` + textReply + `]`, onRequest: func(n int) {
					if n != 1 || change == "不换" {
						return
					}
					h.hub.StopPlugin("A")
					id := "B"
					if change == "同插件重启" {
						id = "A"
					}
					var onlyConsole func(audience.Audience) bool
					if change == "换成主窗口工具" {
						onlyConsole = func(a audience.Audience) bool { return a.Channel == "console" && a.PrivateOK }
					}
					changed <- h.hub.Use(toolPlugin{id, []Tool{{Decl: memDecl(t, "recall_raw"), For: onlyConsole, Exec: func(context.Context, ToolCall) (tools.Result, error) {
						newCalls.Add(1)
						return tools.Result{Text: "新插件结果"}, nil
					}}}})
				}})
				h.use(toolPlugin{"A", []Tool{{Decl: memDecl(t, "recall_raw"), Exec: func(context.Context, ToolCall) (tools.Result, error) {
					oldCalls.Add(1)
					return tools.Result{Text: "旧插件结果"}, nil
				}}}})
				conv := consoleConv(h.c.CharacterID)
				aud := audience.Console(conv)
				if place != "本地私聊" {
					conv = groupConv
					aud = groupAud
					if place == "本地群聊" {
						conv = consoleConv(h.c.CharacterID) + ":group:test"
						aud.Channel, aud.PrivateOK = "console", true
					}
					if place == "QQ私聊" {
						conv = "qq:private:200"
						aud.Scene = "private"
					}
				}
				input := Input{CharacterID: h.c.CharacterID, Conversation: conv, Audience: aud, Scope: "test", Addressed: true, Text: "找一下"}
				if aud.Channel == "qq" {
					seq, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "qq:fixture-person", Origin: "user", Kind: "message", Text: input.Text, CreatedAt: h.hub.Cfg.Now().UnixMilli()}, []string{h.c.CharacterID})
					if err != nil {
						t.Fatal(err)
					}
					input.Seqs = []int64{seq}
				}
				wait(t, h.hub.Respond(input))
				body := h.body(1)
				if change == "不换" {
					if oldCalls.Load() != wantCalls || newCalls.Load() != 0 || !strings.Contains(body, "旧插件结果") {
						t.Fatalf("正常注册执行次数或结果不对：A=%d B=%d", oldCalls.Load(), newCalls.Load())
					}
					return
				}
				if err := recv(t, changed, "插件重注册"); err != nil {
					t.Fatal(err)
				}
				var failures int
				if err := h.st.Trace.QueryRow(`SELECT count(*) FROM steps WHERE kind='tool' AND name='recall_raw' AND is_error=1`).Scan(&failures); err != nil {
					t.Fatal(err)
				}
				if oldCalls.Load() != 0 || newCalls.Load() != 0 || failures != int(wantCalls) {
					t.Fatalf("旧回合应拒绝变更注册：A=%d B=%d，失败调用=%d", oldCalls.Load(), newCalls.Load(), failures)
				}
				// 新一轮按 B 的 For 重新给清单。
				input.Text = "下一轮"
				if aud.Channel == "qq" {
					seq, err := h.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "human", ActorID: "qq:fixture-person", Origin: "user", Kind: "message", Text: input.Text, CreatedAt: h.hub.Cfg.Now().UnixMilli()}, []string{h.c.CharacterID})
					if err != nil {
						t.Fatal(err)
					}
					input.Seqs = []int64{seq}
				}
				wait(t, h.hub.Respond(input))
				visible := change != "换成主窗口工具" || place == "本地私聊" || place == "本地群聊"
				if knownNames := h.toolNames(2); slices.Contains(knownNames, "recall_raw") != visible {
					t.Fatalf("新一轮清单不符合 B 的听众：%v", knownNames)
				}
			})
		}
	}
}

func TestPluginToolRechecksAudience(t *testing.T) {
	t.Parallel()
	for _, check := range []string{"For", "Audience"} {
		t.Run(check, func(t *testing.T) {
			t.Parallel()
			var allowed atomic.Bool
			allowed.Store(true)
			var calls atomic.Int32
			var h *harness
			var reg *toolReg
			h = newHarnessOpts(t, harnessOpts{replies: `[` + callReply("recall_raw", `{"keywords":"x"}`) + `,` + textReply + `]`, onRequest: func(n int) {
				if n == 1 {
					if check == "For" {
						allowed.Store(false)
					} else {
						// 模拟清单发出后政策收紧；只动这个注册，不改全局政策表。
						h.hub.plugins.mu.Lock()
						reg.spec.Audience = "owner-console"
						h.hub.plugins.mu.Unlock()
					}
				}
			}})
			h.use(toolPlugin{"A", []Tool{{Decl: memDecl(t, "recall_raw"), For: func(audience.Audience) bool { return allowed.Load() }, Exec: func(context.Context, ToolCall) (tools.Result, error) {
				calls.Add(1)
				return tools.Result{Text: "不该执行"}, nil
			}}}})
			reg = h.hub.plugins.tool("recall_raw")
			wait(t, h.inGroup("找一下", nil))
			var failures int
			if err := h.st.Trace.QueryRow(`SELECT count(*) FROM steps WHERE kind='tool' AND name='recall_raw' AND is_error=1`).Scan(&failures); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 0 || failures != 1 {
				t.Fatalf("执行前应复核 %s：执行=%d，失败调用=%d", check, calls.Load(), failures)
			}
		})
	}
}
