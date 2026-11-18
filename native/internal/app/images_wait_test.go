package app

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/audience"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/kernel"
	"sumika-native/internal/llm/llmtest"
	"sumika-native/internal/store"
	"sumika-native/internal/turn"
)

func (e *testEnv) logSeen(substr string) bool {
	e.logMu.Lock()
	defer e.logMu.Unlock()
	for _, l := range e.logs {
		if strings.Contains(l, substr) {
			return true
		}
	}
	return false
}

func (e *testEnv) lastBody(t *testing.T) string {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	if len(e.bodies) == 0 {
		t.Fatal("模型没收到请求")
	}
	return e.bodies[len(e.bodies)-1]
}

// blockedImage 让 path 上的图卡在服务器里，直到测试关 release；started 在请求到达时给信号。
func blockedImage(is *imgServer, path string, data []byte) (release chan struct{}, started chan struct{}) {
	release, started = make(chan struct{}), make(chan struct{}, 1)
	is.handle(path, func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
			return
		}
		w.Write(data)
	})
	return release, started
}

// ownerImageTurn 存一条主人 QQ 私聊的带图消息、把下载交给真下载器（卡在可控的假服务器上），再按渠道回合的样子交给内核。
// 返回的通道在这一轮回完时有结果。
func ownerImageTurn(t *testing.T, e *testEnv, a *App, path string) (seq int64, done <-chan turn.Outcome) {
	t.Helper()
	conv := "qq:private:" + testAccount + ":" + testOwner
	st, err := a.set.For("qq")
	if err != nil {
		t.Fatal(err)
	}
	segs := []types.Seg{{Kind: types.SegText, Text: "看图"}, {Kind: types.SegImage, URL: imgURL(path), File: "f.jpg"}}
	seq, err = st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "看图",
		Segments: map[string]any{"segs": segs, "line": "看图"}, CreatedAt: e.now.UnixMilli()}, []string{testChar})
	if err != nil {
		t.Fatal(err)
	}
	a.media.Offer(seq, segs)
	aud := audience.Audience{Channel: "qq", Conversation: conv, Scene: "private", PersonIDs: []string{testOwner}, PrivateOK: true, OwnerAsked: true}
	in := turn.Input{CharacterID: testChar, Conversation: conv, Seqs: []int64{seq}, Text: "看图", Audience: aud, Addressed: true, Scope: channel.ScopeOf(aud),
		Identity: kernel.Invocation{Kind: kernel.KindForeground, Scope: kernel.Scope{CharacterID: testChar, Conversation: conv, ScopeID: channel.ScopeOf(aud), Platform: "qq", Channel: "qq", AccountID: testAccount, Scene: "private", ActorKind: "owner", ActorID: "owner"}}}
	return seq, a.hub.Respond(in)
}

// waitEnv 起一个能看图的假模型和一个可控的假图片服务器（下载由测试放行）；wait>0 时把等图上限调成它。
func waitEnv(t *testing.T, wait time.Duration) (*testEnv, *imgServer, *App) {
	t.Helper()
	e := newEnv(t)
	models := []json.RawMessage{json.RawMessage(`{"id":"` + testModel + `","context_length":32768,"modalities":{"input":["text","image"]}}`)}
	srv := httptest.NewServer(llmtest.Handler(llmtest.Script{Models: models, Reply: llmtest.Text("嗯")}, &e.bodies, &e.mu))
	t.Cleanup(srv.Close)
	e.llm = srv
	is := newImgServer(t)
	a := e.openWith(is)
	t.Cleanup(func() { a.Close() })
	if wait > 0 {
		a.imageWait = wait
	}
	return e, is, a
}

func TestOwnerImageWaitsForLateDownload(t *testing.T) {
	t.Parallel()
	e, is, a := waitEnv(t, 0)
	release, started := blockedImage(is, "/late", directImagePNG(t))
	_, done := ownerImageTurn(t, e, a, "/late")
	<-started
	waitFor(t, "日志里看得到在等图", func() bool { return e.logSeen("等图 ") })
	if n := e.llmCalls(); n != 0 {
		t.Fatalf("图还没下完就组请求了：%d 次调用", n)
	}
	close(release)
	if o := <-done; o.Err != nil {
		t.Fatal(o.Err)
	}
	if body := e.lastBody(t); strings.Count(body, `"type":"image_url"`) != 1 || strings.Contains(body, "图片没给你看") {
		t.Fatalf("请求应当带一张图、没有占位说明：%s", body)
	}
	if !e.logSeen("等到图了") {
		t.Fatal("等到之后日志里该有一行")
	}
}

func TestOwnerImageTimeoutSaysStillDownloading(t *testing.T) {
	t.Parallel()
	// 图永远不放行，等多久都会超时；上限调小只为不让测试空等
	e, is, a := waitEnv(t, 50*time.Millisecond)
	_, started := blockedImage(is, "/never", directImagePNG(t))
	_, done := ownerImageTurn(t, e, a, "/never")
	<-started
	if o := <-done; o.Err != nil {
		t.Fatal(o.Err)
	}
	body := e.lastBody(t)
	if strings.Contains(body, `"type":"image_url"`) || !strings.Contains(body, "图片没给你看：图片还没下载完") {
		t.Fatalf("超时应当带占位并写明还没下完：%s", body)
	}
}

func TestOwnerImageTooBigSaysSo(t *testing.T) {
	t.Parallel()
	e, is, a := waitEnv(t, 0)
	_, started := blockedImage(is, "/huge", directImagePNG(t))
	seq, done := ownerImageTurn(t, e, a, "/huge")
	<-started
	waitFor(t, "日志里看得到在等图", func() bool { return e.logSeen("等图 ") })
	st, _ := a.set.For("qq")
	// 下载器判定太大：状态写进 media_fetch，等图的一方被叫醒，不用等到上限
	if err := st.PutMediaFetch(store.MediaFetch{EventSeq: seq, Idx: 1, Kind: types.SegImage, State: store.MediaTooBig, UpdatedAt: e.now.UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if o := <-done; o.Err != nil {
		t.Fatal(o.Err)
	}
	body := e.lastBody(t)
	if strings.Contains(body, `"type":"image_url"`) || !strings.Contains(body, "图片没给你看：图片太大，没有下载") {
		t.Fatalf("too_big 应当带对应占位：%s", body)
	}
}

func TestGroupImageDoesNotWait(t *testing.T) {
	t.Parallel()
	e, _, a := waitEnv(t, 0)
	st, _ := a.set.For("qq")
	conv := "qq:group:" + testGroup
	seq, err := st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: conv, ActorKind: "owner", ActorID: "owner", Origin: "owner", Kind: "message", Text: "看图",
		Segments: map[string]any{"segs": []types.Seg{{Kind: types.SegImage}}, "line": "看图"}, CreatedAt: e.now.UnixMilli()}, []string{testChar})
	if err != nil {
		t.Fatal(err)
	}
	in := turn.Input{CharacterID: testChar, Conversation: conv, Seqs: []int64{seq}, Text: "看图", Audience: audience.Audience{Channel: "qq", Scene: "group"},
		Identity: kernel.Invocation{Scope: kernel.Scope{CharacterID: testChar, Conversation: conv, Platform: "qq", Channel: "qq", Scene: "group", ActorKind: "owner", ActorID: "owner"}}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 已经取消的 ctx：要是群聊也在等图，这里会立刻因超时报错并留下日志；不等就原样返回
	text, images := a.prepareImages(ctx, in, imageModel(t, `{"input":["text","image"]}`))
	if len(images) != 0 || text != "看图" || e.logSeen("等图 ") {
		t.Fatalf("群聊不该等图也不该改文字：%q %d", text, len(images))
	}
}
