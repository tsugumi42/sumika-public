package main

import (
	"context"
	"errors"
	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sync"
	"testing"
	"time"
)

type transferTestSub struct {
	updates chan api.Snapshot[api.VideoDownloadProgress]
	closed  chan struct{}
	once    sync.Once
}

func (s *transferTestSub) Updates() <-chan api.Snapshot[api.VideoDownloadProgress] { return s.updates }
func (s *transferTestSub) Close()                                                  { s.once.Do(func() { close(s.closed) }) }

type transferTestReader struct {
	api.Reader
	mu          sync.Mutex
	message     api.MessageView
	forward     api.ForwardView
	progress    api.VideoDownloadProgress
	subs        []*transferTestSub
	requestErr  error
	cancels     int
	requestHold chan struct{} // 非 nil 时下载请求发布进度以后等它才返回
	cancelDelay time.Duration // 取消请求先推出“已取消”、再等这么久才返回：模拟负载下请求晚回，界面字样先到
}

func (r *transferTestReader) ReadMessages(ctx context.Context, req api.ConversationRequest, cursor api.Cursor) (api.Result[api.MessagePage], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.Key != r.message.Ref.Conversation {
		return api.Result[api.MessagePage]{}, errors.New("错误会话")
	}
	return api.Result[api.MessagePage]{State: api.Ready, Value: api.MessagePage{List: api.List[api.MessageView]{Items: []api.MessageView{r.message}}}}, nil
}
func (r *transferTestReader) ReadForward(ctx context.Context, req api.ForwardRequest) (api.Result[api.ForwardView], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if req.ID != "inner" || len(req.Via) != 1 || req.Via[0] != "outer" {
		return api.Result[api.ForwardView]{}, errors.New("错误转发路径")
	}
	return api.Result[api.ForwardView]{State: api.Ready, Value: r.forward}, nil
}
func (r *transferTestReader) WatchVideoDownload(ctx context.Context, ref api.VideoDownloadRef) (api.Subscription[api.VideoDownloadProgress], error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s := &transferTestSub{updates: make(chan api.Snapshot[api.VideoDownloadProgress], 16), closed: make(chan struct{})}
	s.updates <- api.Snapshot[api.VideoDownloadProgress]{Result: api.Result[api.VideoDownloadProgress]{State: api.Ready, Value: r.progress}}
	r.subs = append(r.subs, s)
	return s, nil
}
func (r *transferTestReader) RequestVideoDownload(ctx context.Context, ref api.VideoDownloadRef) (api.VideoDownloadProgress, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.requestErr != nil {
		return api.VideoDownloadProgress{}, r.requestErr
	}
	p := api.VideoDownloadProgress{State: api.VideoManualDownloading, Received: 4096, Total: 8192}
	r.publishLocked(p)
	if hold := r.requestHold; hold != nil {
		r.mu.Unlock()
		<-hold
		r.mu.Lock()
	}
	return p, nil
}
func (r *transferTestReader) CancelVideoDownload(ctx context.Context, ref api.VideoDownloadRef) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.cancels++
	r.publishLocked(api.VideoDownloadProgress{State: api.VideoDownloadCanceled})
	if d := r.cancelDelay; d > 0 {
		r.mu.Unlock()
		time.Sleep(d)
		r.mu.Lock()
	}
	return nil
}
func (r *transferTestReader) publishLocked(p api.VideoDownloadProgress) {
	r.progress = p
	for _, s := range r.subs {
		select {
		case <-s.closed:
		default:
			s.updates <- api.Snapshot[api.VideoDownloadProgress]{Result: api.Result[api.VideoDownloadProgress]{State: api.Ready, Value: p}}
		}
	}
}
func (r *transferTestReader) publish(p api.VideoDownloadProgress) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.publishLocked(p)
}

// downloadsIdle：所有下载卡片的请求都回来了（不在“正在下载请求中”“正在取消”）。
// “已取消”这类字样是订阅先推过来的，比取消请求本身返回早；负载重时请求还没回就去点重试，
// changeDownload 会因为还在取消而不理这次点击，测试就等满时限（2026-11-12 合并队列里红过一次）。
// 下一步要点按钮之前，用它等上一个请求真正结束；在测试协程里读，和 q.drain 执行的回调是同一个协程。
// 转发聊天记录里的卡片在 a.forward.downloads，两张表都看。
func downloadsIdle(a *app) bool {
	for _, store := range []*downloadStore{&a.downloads, &a.forward.downloads} {
		for _, d := range store.items {
			if d.busy || d.canceling {
				return false
			}
		}
	}
	return true
}

func pumpTransfer(t *testing.T, a *app, q *uiQueue, tt *ui.Tester, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(uiWaitLimit)
	for time.Now().Before(deadline) {
		q.drain()
		tt.Frame()
		if ready() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("界面未收到下载状态：%q", tt.Texts())
}

// 进度已经显示、下载请求还没回时点取消：点击不能被吞，晚回来的请求也不能把“已取消”盖回去。
func TestMediaDownloadCancelWhileRequestPending(t *testing.T) {
	v := view("download-event", "human", "编造发送者", "", t0)
	v.Ref.Conversation = api.ConversationKey{Platform: "qq", Scene: "group", Conversation: "qq:group:90002"}
	ref := &api.VideoDownloadRef{Source: v.Ref, Index: 1}
	segment := api.DisplaySegment{Kind: "video", Name: "编造附件.bin", Download: ref, Width: 240, Height: 120}
	v.Segments = []api.DisplaySegment{{Kind: "text", Text: "编造说明"}, segment}
	hold := make(chan struct{})
	r := &transferTestReader{message: v, progress: api.VideoDownloadProgress{State: api.VideoAwaitingManual}, requestHold: hold}
	a, q := imgApp(t, &imgNet{orig: map[string][2]int{"downloaded": {240, 120}}})
	a.reader = r
	t.Cleanup(a.downloads.reset)
	a.msgs = []*msg{msgFromView(v.Ref.Conversation, v, charOf)}
	tt := testerOf(900, 800, a.chatView)
	pumpTransfer(t, a, q, tt, func() bool { return tt.HasText("点一下下载") })
	click(t, tt, "下载视频")
	pumpTransfer(t, a, q, tt, func() bool { return tt.HasText("已下载 4.0 KB / 8.0 KB") })
	click(t, tt, "取消视频下载")
	pumpTransfer(t, a, q, tt, func() bool { return tt.HasText("已取消，点一下重新下载") })
	close(hold)
	pumpTransfer(t, a, q, tt, func() bool {
		for _, d := range a.downloads.items {
			return !d.busy && !d.canceling
		}
		return false
	})
	if !tt.HasText("已取消，点一下重新下载") {
		t.Fatalf("晚回来的下载请求把已取消盖掉了：%q", tt.Texts())
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancels != 1 {
		t.Fatalf("取消应该发出去一次：%d", r.cancels)
	}
}

// 下载状态自行驱动界面，不依赖会话版本更新；完成后必须重读实际类型。
func TestMediaDownloadControlsAndCompletedFile(t *testing.T) {
	for _, kind := range []string{"video", "file"} {
		for _, nested := range []bool{false, true} {
			name := kind
			if nested {
				name += "-nested"
			}
			t.Run(name, func(t *testing.T) {
				v := view("download-event", "human", "编造发送者", "", t0)
				v.Ref.Conversation = api.ConversationKey{Platform: "qq", Scene: "group", Conversation: "qq:group:90002"}
				ref := &api.VideoDownloadRef{Source: v.Ref, Index: 1}
				if nested {
					ref.Forward = []string{"outer", "inner"}
					ref.Node = 3
				}
				segment := api.DisplaySegment{Kind: kind, Name: "编造附件.bin", Download: ref, Width: 240, Height: 120}
				v.Segments = []api.DisplaySegment{{Kind: "text", Text: "编造说明"}, segment}
				r := &transferTestReader{message: v, progress: api.VideoDownloadProgress{State: api.VideoAwaitingManual}, cancelDelay: 50 * time.Millisecond}
				a, q := imgApp(t, &imgNet{orig: map[string][2]int{"downloaded": {240, 120}}})
				a.reader = r
				t.Cleanup(a.downloads.reset)
				t.Cleanup(a.stopForward)
				m := msgFromView(v.Ref.Conversation, v, charOf)
				a.msgs = []*msg{m}
				draw := a.chatView
				if nested {
					nodes := make([]api.ForwardNode, 4)
					nodes[3] = api.ForwardNode{UID: "invented", Name: "编造节点", Segments: v.Segments}
					req := api.ForwardRequest{Source: v.Ref, ID: "inner", Via: []string{"outer"}}
					r.forward = api.ForwardView{ID: "inner", State: "ready", Nodes: nodes}
					node := forwardNodeMsg(req, nodes[3], 3)
					layer := &forwardLayer{request: req, view: r.forward, msgs: []*msg{node}, rows: forwardRows([]*msg{node})}
					a.forward = forwardViewState{open: true, stack: []*forwardLayer{layer}}
					draw = a.forwardWindow
				}
				tt := testerOf(900, 800, draw)
				labelKind := "视频"
				suffix := ""
				if kind == "file" {
					labelKind = "文件"
					suffix = "：编造附件.bin"
				}
				download := "下载" + labelKind + suffix
				pumpTransfer(t, a, q, tt, func() bool { return tt.HasText("点一下下载") })
				if kind == "file" {
					name, ok := tt.Find("编造附件.bin")
					if !ok || name.W < 100 {
						t.Fatalf("文件名被压进图标区：%+v found=%v", name, ok)
					}
				}
				click(t, tt, download)
				pumpTransfer(t, a, q, tt, func() bool { return tt.HasText("已下载 4.0 KB / 8.0 KB") && downloadsIdle(a) })
				click(t, tt, "取消"+labelKind+"下载"+suffix)
				pumpTransfer(t, a, q, tt, func() bool { return tt.HasText("已取消，点一下重新下载") && downloadsIdle(a) })
				r.publish(api.VideoDownloadProgress{State: api.VideoDownloadFailed, Error: "编造失败原因"})
				pumpTransfer(t, a, q, tt, func() bool { return tt.HasText("编造失败原因") })
				click(t, tt, "重试下载"+labelKind+suffix)
				pumpTransfer(t, a, q, tt, func() bool { return tt.HasText("已下载 4.0 KB / 8.0 KB") })
				r.publish(api.VideoDownloadProgress{State: api.VideoManualDownloading, Received: 4096})
				pumpTransfer(t, a, q, tt, func() bool { return hasExact(tt, "已下载 4.0 KB") })
				r.mu.Lock()
				completed := segment
				completed.Media = &api.MediaRef{Source: v.Ref, Hash: "downloaded", Forward: ref.Forward}
				if kind == "file" {
					completed.FileKind = "image"
				}
				r.message.Segments = append([]api.DisplaySegment(nil), v.Segments...)
				r.message.Segments[1] = completed
				if nested {
					r.forward.Nodes = append([]api.ForwardNode(nil), r.forward.Nodes...)
					r.forward.Nodes[3].Segments = r.message.Segments
				}
				r.publishLocked(api.VideoDownloadProgress{State: api.VideoDownloaded, Hash: "downloaded"})
				r.mu.Unlock()
				final := "播放视频"
				if kind == "file" {
					final = "图片"
				}
				pumpTransfer(t, a, q, tt, func() bool { _, ok := tt.Find(final); return ok })
				r.mu.Lock()
				cancels := r.cancels
				r.mu.Unlock()
				if cancels != 1 {
					t.Fatalf("显式取消 %d 次，应为1", cancels)
				}
			})
		}
	}
}

func TestDownloadFailureAndRetiredSubscription(t *testing.T) {
	v := view("retired-download", "human", "编造发送者", "", t0)
	ref := &api.VideoDownloadRef{Source: v.Ref, Index: 0}
	v.Segments = []api.DisplaySegment{{Kind: "file", Name: "编造文件.bin", Download: ref}}
	r := &transferTestReader{message: v, progress: api.VideoDownloadProgress{State: api.VideoAwaitingManual}, requestErr: errors.New("已有两个手动媒体下载")}
	a, q := imgApp(t, &imgNet{})
	a.reader = r
	a.msgs = []*msg{msgFromView(v.Ref.Conversation, v, charOf)}
	t.Cleanup(a.downloads.reset)
	tt := testerOf(900, 600, a.chatView)
	pumpTransfer(t, a, q, tt, func() bool { return tt.HasText("点一下下载") })
	click(t, tt, "下载文件：编造文件.bin")
	pumpTransfer(t, a, q, tt, func() bool { return tt.HasText("已有两个手动媒体下载") })
	a.switchTo("other")
	r.mu.Lock()
	subs := append([]*transferTestSub(nil), r.subs...)
	r.mu.Unlock()
	for _, s := range subs {
		select {
		case <-s.closed:
		case <-time.After(uiWaitLimit):
			t.Fatal("切会话未结束订阅")
		}
	}
	r.publish(api.VideoDownloadProgress{State: api.VideoDownloadFailed, Error: "迟到的旧下载错误"})
	q.drain()
	tt.Frame()
	if tt.HasText("迟到的旧下载错误") {
		t.Fatal("迟到错误进入新会话")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancels != 0 {
		t.Fatalf("离开会话取消了后台下载 %d 次", r.cancels)
	}
}
