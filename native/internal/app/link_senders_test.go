package app

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkplugin"
	"sumika-native/internal/linkreader"
)

// senderFetcher 记下取过哪些帖子号（返回和 linkFake 一样的编的结果）
type senderFetcher struct {
	linkFake
	mu  sync.Mutex
	ids []string
}

func (f *senderFetcher) Fetch(ctx context.Context, l linkreader.Link) (linkreader.Result, error) {
	f.mu.Lock()
	f.ids = append(f.ids, l.ID)
	f.mu.Unlock()
	return f.linkFake.Fetch(ctx, l)
}

func (f *senderFetcher) fetched(id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.ids, id)
}

// 群里谁发的链接能触发展开（用户 2026-11-13，linkReader.expand.senders）：从真配置到插件到假协议端走一遍。
// 白名单：名单里的群友、主人能触发；名单外的不取数、一帧都不发。黑名单反过来，主人永远能触发。
// 拦下的那条用事件同步判断：后一条入库时前一条的观察已经做完，再等插件手上没有任务，才看它取没取过数。
func TestLinkSendersEndToEnd(t *testing.T) {
	t.Parallel()
	const listed, other = "30101", "30102"
	cases := []struct {
		name, mode     string
		allow, blocked string
	}{
		{"白名单", "whitelist", listed, other},
		{"黑名单", "blacklist", other, listed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := &senderFetcher{}
			e, a, wire, _ := newExpandHost(t, "off", f, "    senders:\n      mode: "+c.mode+"\n      users: [\""+listed+"\"]\n")
			conn := startSend(t, e, a)
			conv := "qq:group:" + testGroup
			e.peer.send(t, conn, expandFrame(e, "801", c.blocked, "https://x.com/paper/status/80101"))
			e.peer.send(t, conn, expandFrame(e, "802", testOwner, "编的入库屏障"))
			expandStored(t, a, conv, "802")
			waitFor(t, c.name+"插件手上没有任务", func() bool { return len(a.links.Active()) == 0 })
			if f.fetched("80101") || len(wire.snapshot()) != 0 {
				t.Fatalf("%s：不该触发的人发的链接取了数或发了帧：%v、%d 帧", c.name, f.ids, len(wire.snapshot()))
			}
			e.peer.send(t, conn, expandFrame(e, "803", c.allow, "https://x.com/paper/status/80103"))
			waitFor(t, c.name+"能触发的人取数并发出", func() bool { return f.fetched("80103") && len(wire.snapshot()) > 0 })
			e.peer.send(t, conn, expandFrame(e, "804", testOwner, "https://x.com/paper/status/80104"))
			waitFor(t, c.name+"主人取数", func() bool { return f.fetched("80104") })
			// 宿主发送前最后一道门单独也拦得住：按库里的源消息身份查名单（插件那边漏了也发不出去）
			st, err := a.set.For("qq")
			if err != nil {
				t.Fatal(err)
			}
			group := types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
			for id, want := range map[string]bool{"801": false, "803": true, "804": true} {
				ref, ok, err := st.EventByPlatformID("qq", conv, id)
				if err != nil || !ok {
					t.Fatalf("%s 没入库：%v", id, err)
				}
				err = a.checkLinkParts(testChar, group, []types.Part{{SourceSeq: ref.Seq}})
				var blocked *linkplugin.Blocked
				if want && err != nil {
					t.Errorf("%s：%s 应当放行：%v", c.name, id, err)
				}
				if !want && (!errors.As(err, &blocked) || !strings.Contains(blocked.Why, "展开名单")) {
					t.Errorf("%s：%s 应当在最后一道门按名单拦下：%v", c.name, id, err)
				}
			}
		})
	}
}
