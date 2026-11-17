package app

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
)

// linkFake 是不联网的取数：X 的帖子给一段编的正文
type linkFake struct{ calls atomic.Int32 }

func (f *linkFake) Fetch(_ context.Context, l linkreader.Link) (linkreader.Result, error) {
	f.calls.Add(1)
	r := linkreader.Result{Platform: l.Platform, Kind: l.Kind, ID: l.ID, CleanURL: l.Clean, Source: "fake", FetchedAt: time.Now().UTC(), Stats: map[string]linkreader.Field[int64]{}}
	r.Author.Name = linkreader.Field[string]{State: "available", Value: "编的作者"}
	r.Text = linkreader.Field[string]{State: "available", Value: "编的推文正文"}
	return r, nil
}

func (f *linkFake) Resolve(_ context.Context, l linkreader.Link) (linkreader.Link, error) {
	return l, nil
}

// 规格-链接解析第十三节第 4 步：群里 @ 她、带一个 X 链接，走真的入库 → 观察者 → 取数 → 闸门 → 内核，模型收到的请求里有这条链接的摘要。
// 关掉 linkReader.context.enabled 就不取、不给
func TestLinkSummaryReachesModel(t *testing.T) {
	t.Parallel()
	for _, on := range []bool{true, false} {
		e := newEnv(t)
		e.expectNoActions(t)
		if !on {
			e.extraConfig = "linkReader:\n  context:\n    enabled: false\n"
			e.writeConfig(testToken)
		}
		f := &linkFake{}
		e.linkFetcher = f
		e.start()
		conn := e.peer.accept(t)
		e.peer.send(t, conn, groupAtFrame(e.now, testAccount, "501", testOwner, "看这个 https://x.com/someone/status/123456"))
		waitFor(t, "模型被调用", func() bool { return e.llmCalls() > 0 })
		e.mu.Lock()
		body := strings.Join(e.bodies, "\n")
		e.mu.Unlock()
		got := strings.Contains(body, "[链接资料]") && strings.Contains(body, "X 编的作者：编的推文正文")
		if on != strings.Contains(body, `"read_link_detail"`) {
			t.Fatalf("开着时模型的工具里要有 read_link_detail，关着没有（开着=%v）", on)
		}
		if on && (!got || f.calls.Load() != 1) {
			t.Fatalf("开着：模型要收到链接摘要、取一次（取了 %d 次）", f.calls.Load())
		}
		if !on && (strings.Contains(body, "[链接资料]") || f.calls.Load() != 0) {
			t.Fatalf("关着：不取、不给（取了 %d 次）", f.calls.Load())
		}
	}
}

// 引用一条带链接的消息：引用那一行带上它的链接摘要和编号（规格 8.1"被引用消息查同一关联"）
func TestQuotedLinkSummary(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	e.expectNoActions(t)
	f := &linkFake{}
	e.linkFetcher = f
	a := e.start()
	conn := e.peer.accept(t)
	e.peer.send(t, conn, groupAtFrame(e.now, testAccount, "501", testOwner, "看这个 https://x.com/someone/status/123456"))
	waitFor(t, "链接取完并关联", func() bool {
		st, _ := a.set.For("qq")
		n, err := st.QuotedLinkNotes(context.Background(), "qq:group:"+testGroup, 1)
		return err == nil && len(n) == 1
	})
	quote := fmt.Sprintf(`{"post_type":"message","message_type":"group","time":%d,"self_id":%s,"user_id":%s,"group_id":%s,"message_id":"502","sender":{"nickname":"编的乙"},"message":[{"type":"reply","data":{"id":"501"}},{"type":"at","data":{"qq":"%s"}},{"type":"text","data":{"text":" 这个怎么样"}}]}`,
		e.now.Unix(), testAccount, testOwner, testGroup, testAccount)
	e.peer.send(t, conn, quote)
	// 排好的那一行存进 segments_json（历史重建也用它）
	waitFor(t, "引用那一行带链接摘要", func() bool {
		st, _ := a.set.For("qq")
		var n int
		err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE segments_json LIKE ?`, "%｜链接：X 编的作者：编的推文正文（编号 1）%").Scan(&n)
		return err == nil && n == 1
	})
}
