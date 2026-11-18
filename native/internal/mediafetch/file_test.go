package mediafetch

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"sumika-native/internal/api"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

type fileResolverFunc func(context.Context, string, types.Seg) (string, error)

func (fn fileResolverFunc) FileURL(ctx context.Context, conv string, s types.Seg) (string, error) {
	return fn(ctx, conv, s)
}

func TestFileDownloadPolicy(t *testing.T) {
	for _, tc := range []struct {
		name, file, body, state string
		size                    int64
		chunked                 bool
	}{
		{name: "text-at-limit", file: "清单.txt", body: strings.Repeat("a", 1024), state: store.MediaDone},
		{name: "unknown-length-over-limit", file: "清单.txt", body: strings.Repeat("a", 1025), state: store.MediaTooBig, chunked: true},
		{name: "declared-over-limit", file: "清单.txt", body: strings.Repeat("a", 1025), size: 1025, state: store.MediaTooBig},
		{name: "archive", file: "附件.zip", body: "PK\x03\x04data", state: store.MediaManual},
		{name: "executable", file: "附件.exe", body: "MZ\x00payload", state: store.MediaManual},
		{name: "empty-extension", file: "附件", body: "hello", state: store.MediaManual},
		{name: "spoof", file: "附件.png", body: "MZ\x00payload", state: store.MediaManual},
		{name: "pdf", file: "附件.pdf", body: "%PDF-1.7\n编造文档", state: store.MediaDone},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t)
			e.f.dl.videoMax = 1024
			e.handle("/file", func(w http.ResponseWriter, r *http.Request) {
				if tc.chunked {
					w.(http.Flusher).Flush()
				}
				_, _ = w.Write([]byte(tc.body))
			})
			segs := []types.Seg{{Kind: types.SegFile, File: tc.file, URL: testSegURL("/file"), Size: tc.size}}
			seq := e.addEvent(segs, "")
			e.f.Start()
			e.f.Offer(seq, segs)
			e.idle()
			rows, err := e.st.MediaFetchRows(seq)
			if err != nil {
				t.Fatal(err)
			}
			if rows[0].State != tc.state {
				t.Fatalf("row=%+v", rows[0])
			}
			if tc.state == store.MediaDone {
				if e.segHash(seq, 0) != sha([]byte(tc.body)) {
					t.Fatal("实际正文未回填")
				}
			} else if e.segHash(seq, 0) != "" {
				t.Fatal("自动拒绝却落盘回填")
			}
			if tc.size > 1024 || tc.file == "附件.zip" || tc.file == "附件.exe" || tc.file == "附件" {
				if e.nreq("/file") != 0 {
					t.Fatal("第二档或声明超大发了自动请求")
				}
			}
			if tc.state == store.MediaManual || tc.state == store.MediaTooBig {
				if _, err := e.f.RequestVideo(seq, 0, segs); err != nil {
					t.Fatal(err)
				}
				e.idle()
				rows, _ = e.st.MediaFetchRows(seq)
				if rows[0].State != store.MediaDone || e.segHash(seq, 0) != sha([]byte(tc.body)) {
					t.Fatalf("手动没有存原件：%+v", rows[0])
				}
			}
		})
	}
}

func TestFileURLResolutionAndRefresh(t *testing.T) {
	for _, initial := range []string{"", testSegURL("/old")} {
		t.Run(initial, func(t *testing.T) {
			e := newEnv(t)
			calls := 0
			e.f.o.FileResolver = fileResolverFunc(func(_ context.Context, conv string, s types.Seg) (string, error) {
				calls++
				if conv != testConv || s.FileID != "opaque" || s.FileHash != "private-token" {
					return "", errors.New("取址凭据与原事件不一致")
				}
				return testSegURL("/fresh"), nil
			})
			e.serve("/old", 403, []byte("expired"))
			e.serve("/fresh", 200, []byte("编造文本"))
			segs := []types.Seg{{Kind: types.SegFile, File: "清单.txt", URL: initial, FileID: "opaque", FileHash: "private-token"}}
			seq := e.addEvent(segs, "")
			e.f.Start()
			e.f.Offer(seq, segs)
			e.idle()
			if calls != 1 || e.segHash(seq, 0) != sha([]byte("编造文本")) {
				t.Fatalf("未取到新地址：calls=%d logs=%s", calls, e.allLogs())
			}
		})
	}
	e := newEnv(t)
	e.f.o.FileResolver = fileResolverFunc(func(context.Context, string, types.Seg) (string, error) { return "", errors.New("missing") })
	segs := []types.Seg{{Kind: types.SegFile, File: "清单.txt", FileID: "opaque"}}
	seq := e.addEvent(segs, "")
	e.f.Start()
	e.f.Offer(seq, segs)
	e.idle()
	p, err := e.f.videoState(seq, 0, segs[0])
	if err != nil || p.State != api.VideoDownloadFailed || p.Error == "" || len(e.requests()) != 0 {
		t.Fatalf("缺址状态不真实：%+v %v", p, err)
	}
	// 文件接口返回的新地址同样不能绕过主机边界。
	b := newEnv(t)
	b.f.o.FileResolver = fileResolverFunc(func(context.Context, string, types.Seg) (string, error) {
		return "https://untrusted.example.test/file", nil
	})
	blocked := b.addEvent(segs, "")
	b.f.Start()
	b.f.Offer(blocked, segs)
	b.idle()
	blockedRows, err := b.st.MediaFetchRows(blocked)
	if err != nil || blockedRows[0].State != store.MediaBlocked || len(b.requests()) != 0 {
		t.Fatalf("刷新文件地址绕过白名单：%+v %v", blockedRows, err)
	}
}

func TestForwardFileTaskIdentity(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.serve("/file", 200, []byte("编造文件"))
	e.f.Start()
	tree := types.ForwardTree{ID: "inner-file", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: types.SegFile, File: "程序.exe", FileID: "opaque", FileHash: "private-token"}}}}}
	if err := e.st.PutForward(ctx, tree, 1000, testConv); err != nil {
		t.Fatal(err)
	}
	registered, err := e.st.ForwardMediaRows(ctx, tree.ID)
	if err != nil || registered[[2]int{0, 0}].State != store.MediaPending {
		t.Fatalf("树提交没有原子登记文件任务：%+v %v", registered, err)
	}
	e.f.o.FileResolver = fileResolverFunc(func(_ context.Context, conv string, s types.Seg) (string, error) {
		expected := ""
		if s.FileID == "group-file" {
			expected = "qq:group:90004"
		}
		if conv != expected {
			return "", errors.New("取址会话不是文件原群或无来源的私聊")
		}
		return testSegURL("/file"), nil
	})
	if err := e.f.OfferForward(tree, testConv); err != nil {
		t.Fatal(err)
	}
	e.idle()
	rows, err := e.st.ForwardMediaRows(ctx, tree.ID)
	if err != nil || rows[[2]int{0, 0}].State != store.MediaManual || len(e.requests()) != 0 {
		t.Fatalf("转发程序被自动下：%+v %v", rows, err)
	}
	sub, err := e.f.WatchForwardVideo(ctx, tree.ID, 0, 0, tree.Nodes[0].Segs[0])
	if err != nil {
		t.Fatal(err)
	}
	defer sub.Close()
	videoUpdate(t, sub, func(p api.VideoDownloadProgress) bool { return p.State == api.VideoAwaitingManual })
	if _, err = e.f.RequestForwardVideo(tree.ID, 0, 0, testConv, tree.Nodes[0].Segs); err != nil {
		t.Fatal(err)
	}
	p := videoUpdate(t, sub, func(p api.VideoDownloadProgress) bool { return p.State == api.VideoDownloaded })
	if p.Hash != sha([]byte("编造文件")) || p.Received != 12 {
		t.Fatalf("完成值错误：%+v", p)
	}
	r, ok, err := e.st.ReadForward(ctx, tree.ID)
	if err != nil || !ok || r.Tree.Nodes[0].Segs[0].Hash != p.Hash {
		t.Fatalf("没有更新对应树：%+v %v", r, err)
	}
	if _, err = e.f.RequestForwardVideo(tree.ID, 0, 0, testConv, r.Tree.Nodes[0].Segs); err != nil || e.nreq("/file") != 1 {
		t.Fatal("重复下已完成文件")
	}
	groupTree := types.ForwardTree{ID: "group-file", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{{Kind: types.SegFile, File: "清单.txt", FileID: "group-file", FileGroup: "90004"}}}}}
	if err := e.st.PutForward(ctx, groupTree, e.clk.Now().UnixMilli(), "qq:group:90002"); err != nil {
		t.Fatal(err)
	}
	if err := e.f.OfferForward(groupTree, "qq:group:90002"); err != nil {
		t.Fatal(err)
	}
	e.idle()
	g, ok, err := e.st.ReadForward(ctx, groupTree.ID)
	if err != nil || !ok || g.Tree.Nodes[0].Segs[0].Hash != sha([]byte("编造文件")) {
		t.Fatalf("明确原群的转发文件没取到：%+v %v", g, err)
	}
}
