package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"sumika-native/internal/api"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/sendlock"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/embed"
	"sumika-native/internal/linkreader"
)

type expandPeer struct {
	mu     sync.Mutex
	frames []string
}

func (p *expandPeer) handle(c *websocket.Conn, data []byte) {
	var frame struct{ Action, Echo string }
	if json.Unmarshal(data, &frame) != nil {
		return
	}
	if !isSendAction(string(data)) && frame.Action != "set_msg_emoji_like" {
		answerInfo(c, data)
		return
	}
	p.mu.Lock()
	p.frames = append(p.frames, string(data))
	n := len(p.frames)
	p.mu.Unlock()
	body := fmt.Sprintf(`{"echo":%q,"status":"ok","retcode":0,"data":{"message_id":%d}}`, frame.Echo, -97000-n)
	if frame.Action == "set_msg_emoji_like" {
		body = fmt.Sprintf(`{"echo":%q,"status":"ok","retcode":0,"data":null}`, frame.Echo)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e := c.Write(ctx, websocket.MessageText, []byte(body)); e != nil {
		panic(e)
	}
}
func (p *expandPeer) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.frames...)
}
func newExpandHost(t *testing.T, mode string, fetcher linkreader.ContentFetcher, expandExtra ...string) (*testEnv, *App, *expandPeer, *linkFake) {
	return newExpandHostSend(t, mode, fetcher, true, expandExtra...)
}

// newExpandHostSend 同 newExpandHost，goSend 可关（发送规则不齐时一帧都不该发）
func newExpandHostSend(t *testing.T, mode string, fetcher linkreader.ContentFetcher, goSend bool, expandExtra ...string) (*testEnv, *App, *expandPeer, *linkFake) {
	t.Helper()
	e := newEnv(t)
	e.goSend = goSend
	e.callTimeout = time.Second // 进程内的假协议端回包是毫秒级；“不回包”的用例只等到这个时限
	f := &linkFake{}
	e.linkFetcher = f
	if fetcher != nil {
		e.linkFetcher = fetcher
	}
	extra := strings.Join(expandExtra, "")
	if !strings.Contains(extra, "tweetCard:") {
		// 假取数大多给纯文字推文、断言文字版正文；纯文字卡片（ADR-399）由专门的用例打开
		extra = "    tweetCard: {textOnly: false}\n" + extra
	}
	e.extraConfig = fmt.Sprintf(`linkReader:
  context: {enabled: false}
  expand:
    enabled: true
    mode: whitelist
    conversations: ["qq:group:%s", "qq:private:%s:%s"]
    startEmoji: "76"
%sreply:
  deny: {"qq:70011": bot}
`, testGroup, testAccount, testOwner, extra)
	e.writeConfigFreq(testToken, "0")
	guardFixture(t, e, true)
	writeSenderIntent(t, e.data, testAccount, sendlock.HolderGo)
	writeSenderRecord(t, e.data, testAccount)
	wire := &expandPeer{}
	e.peer.onAction = wire.handle
	opt := e.options()
	opt.NewEmbed = func(cfg embed.LocalConfig, _ func(string, ...any)) LocalEmbedding {
		return &sendEmbedding{&guardLocal{cfg: cfg}}
	}
	a, err := Open(context.Background(), opt)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.Close() })
	awaitGuardFull(t, a)
	if err = a.set.TraceStore().SetGateMode("qq:group:"+testGroup, mode, e.now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	return e, a, wire, f
}
func expandFrame(e *testEnv, id, actor, text string) string {
	return fmt.Sprintf(`{"post_type":"message","message_type":"group","time":%d,"self_id":%s,"user_id":%s,"group_id":%s,"message_id":%q,"sender":{"nickname":"冒充作者"},"message":[{"type":"text","data":{"text":%q}}]}`, e.now.Unix(), testAccount, actor, testGroup, id, text)
}
func expandStored(t *testing.T, a *App, conv, id string) {
	t.Helper()
	st, e := a.set.For("qq")
	if e != nil {
		t.Fatal(e)
	}
	waitFor(t, "编造消息入库", func() bool { _, ok, e := st.EventByPlatformID("qq", conv, id); return e == nil && ok })
	if e = a.intake.Idle(context.Background()); e != nil {
		t.Fatal(e)
	}
}
func TestExpandStoredMessagesIgnoreRoleMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"speak", "dry", "off"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			e, a, wire, f := newExpandHost(t, mode, nil)
			// 帧数：这条假帖整条进合并转发，规格-链接发送改版 B4 起转发前不另发导语。
			// 群里一次展开 = 贴开始表情、合并转发、撤开始表情、贴完成表情；私聊 = 状态文字、合并转发
			const group, private = 4, 2
			conn := startSend(t, e, a)
			conv := "qq:group:" + testGroup
			link := "https://x.com/paper/status/70001"
			frame := expandFrame(e, "701", testOwner, link)
			e.peer.send(t, conn, frame)
			waitFor(t, "群开始回应和结果", func() bool { return len(wire.snapshot()) == group })
			if f.calls.Load() != 1 {
				t.Fatal("单消息重复取数", f.calls.Load())
			}
			e.peer.send(t, conn, frame)
			e.peer.send(t, conn, expandFrame(e, "702", testOwner, "编的入库屏障"))
			expandStored(t, a, conv, "702")
			if len(wire.snapshot()) != group {
				t.Fatal("同帧重复触发")
			}
			e.peer.send(t, conn, expandFrame(e, "703", testOwner, link))
			waitFor(t, "新消息重复链接仍展开", func() bool { return len(wire.snapshot()) == 2*group })
			if f.calls.Load() != 1 || !strings.Contains(strings.Join(wire.snapshot(), "\n"), "发过 1 次") {
				t.Fatal("缓存或历史次数不正确", f.calls.Load(), wire.snapshot())
			}
			e.peer.send(t, conn, privateFrame(e.now, testAccount, "704", testOwner, link))
			waitFor(t, "私聊状态和独立结果", func() bool { return len(wire.snapshot()) == 2*group+private })
			frames := wire.snapshot()
			for _, raw := range frames[2*group:] {
				if strings.Contains(raw, "set_msg_emoji_like") {
					t.Fatal("私聊发了reaction")
				}
			}
			if !strings.Contains(frames[2*group], "开始解析链接") || !strings.Contains(frames[0], "set_msg_emoji_like") {
				t.Fatal("开始反馈顺序", frames)
			}
			if e.llmCalls() != 0 {
				t.Fatal("展开改变了角色接话或调用了模型", e.llmCalls())
			}
			e.peer.send(t, conn, expandFrame(e, "705", "70011", link))
			expandStored(t, a, conv, "705")
			e.peer.send(t, conn, expandFrame(e, "706", testAccount, link))
			expandStored(t, a, conv, "706")
			if len(wire.snapshot()) != 2*group+private {
				t.Fatal("deny或自己发的消息触发展开")
			}
			d := a.Desktop().Reader
			view, err := d.LinkSettings(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if _, err = d.SetLinkSettings(context.Background(), api.LinkSettingsChange{Key: config.LinkEnabled, Bool: false}, view.Version); err != nil {
				t.Fatal(err)
			}
			e.peer.send(t, conn, expandFrame(e, "707", testOwner, link))
			expandStored(t, a, conv, "707")
			if len(wire.snapshot()) != 2*group+private || f.calls.Load() != 1 {
				t.Fatal("热关闭后仍取数或发送")
			}
			for _, scene := range []string{types.ScenePrivate, types.SceneGroup} {
				a.links.OnStored(channel.Stored{Seq: 123, Observed: true, Character: testChar, Inbound: types.Inbound{Conv: types.Conversation{Channel: "console", Scene: scene, ID: "编的本地会话", Self: func() string {
					if scene == types.ScenePrivate {
						return "owner"
					}
					return ""
				}()}, Segs: []types.Seg{{Kind: types.SegText, Text: link}}}})
			}
			if _, err = a.outlet.Send(context.Background(), testChar, linkOrigin, types.Conversation{Channel: "qq", Scene: "group", ID: testGroup}, []types.Part{types.Message("冒充注册origin")}); err == nil {
				t.Fatal("普通Outbox伪造注册origin成功")
			}
			t.Logf("角色档位=%s，QQ群动作=%d，QQ私聊动作=%d，平台取数=%d，模型调用=%d，重复帧/deny/自己发/热关闭=0", mode, 2*group, private, f.calls.Load(), e.llmCalls())
		})
	}
}

type failedExpandFetcher struct{}

func (failedExpandFetcher) Fetch(context.Context, linkreader.Link) (linkreader.Result, error) {
	return linkreader.Result{}, errors.New("编的上游失败")
}
func (failedExpandFetcher) Resolve(_ context.Context, l linkreader.Link) (linkreader.Link, error) {
	return l, nil
}

func TestExpandFailedLinksKeepSeparateResults(t *testing.T) {
	t.Parallel()
	e, a, wire, _ := newExpandHost(t, "off", failedExpandFetcher{})
	conn := startSend(t, e, a)
	e.peer.send(t, conn, expandFrame(e, "801", testOwner, "https://x.com/paper/status/71001 https://x.com/paper/status/71002"))
	// 开始表情 1 帧 + 撤开始表情 + 失败表情，没有文字
	waitFor(t, "两个失败链接只换表情", func() bool { return len(wire.snapshot()) == 3 })
	texts := 0
	for _, frame := range wire.snapshot() {
		if strings.Contains(frame, "set_msg_emoji_like") {
			continue
		}
		texts++
	}
	if texts != 0 {
		t.Fatal("失败时不该发文字", texts)
	}
	if e.llmCalls() != 0 {
		t.Fatal("失败反馈调用了模型", e.llmCalls())
	}
}
