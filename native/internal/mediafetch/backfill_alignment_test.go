package mediafetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/store"
)

type alignmentZeros struct{}

func (alignmentZeros) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// C03 固定 100 MiB 真边界，视频和文件共用下载设施；流式生成而不分配整个资源。
func TestAlignmentMediaTypesAndLimits(t *testing.T) {
	const limit = int64(100 << 20)
	for _, kind := range []string{types.SegVideo, types.SegFile} {
		for _, tc := range []struct {
			name           string
			size           int64
			known, chunked bool
			want           string
		}{
			{"小一字节", limit - 1, true, false, store.MediaDone}, {"恰好上限", limit, true, false, store.MediaDone},
			{"已知超限", limit + 1, true, false, store.MediaTooBig}, {"未知流超限", limit + 1, false, true, store.MediaTooBig},
		} {
			t.Run(kind+"/"+tc.name, func(t *testing.T) {
				e := newEnv(t)
				e.f.noVideoTools()
				e.handle("/alignment-video", func(w http.ResponseWriter, r *http.Request) {
					if !tc.chunked {
						w.Header().Set("Content-Length", strconv.FormatInt(tc.size, 10))
					} else {
						w.(http.Flusher).Flush()
					}
					header := tinyMP4()
					n, _ := w.Write(header)
					_, _ = io.CopyN(w, alignmentZeros{}, tc.size-int64(n))
				})
				seg := types.Seg{Kind: kind, URL: testSegURL("/alignment-video"), File: "编造视频.mp4"}
				if tc.known {
					seg.Size = tc.size
				}
				seq := e.addEvent([]types.Seg{seg}, "alignment-video")
				e.start()
				start := time.Now()
				e.f.Offer(seq, []types.Seg{seg})
				e.idle()
				row := e.row(seq, 0)
				if row.State != tc.want {
					t.Fatalf("100MiB分档 %+v want=%s", row, tc.want)
				}
				if tc.want == store.MediaDone {
					if row.Size != tc.size || row.Hash == "" {
						t.Fatal("完成大小或hash不实", row)
					}
				} else if len(mediaFiles(t, e)) != 0 {
					t.Fatal("超限留下实体")
				}
				if tc.known && tc.size > limit && e.nreq("/alignment-video") != 0 {
					t.Fatal("已知超限仍联网")
				}
				tmp, err := os.ReadDir(filepath.Join(e.data, "go", "media", "qq", ".tmp"))
				if err != nil && !os.IsNotExist(err) {
					t.Fatal(err)
				}
				if len(tmp) != 0 {
					t.Fatal("终局残留临时文件")
				}
				t.Logf("C03 kind=%s case=%s bytes=%d downloaded=%d requests=%d elapsed=%v", kind, tc.name, tc.size, row.Size, e.nreq("/alignment-video"), time.Since(start))
			})
		}
	}
	e := newEnv(t)
	image := png("编造相同图片")
	e.serve("/alignment-image", 200, image)
	segs := []types.Seg{{Kind: types.SegImage, URL: testSegURL("/alignment-image")}, {Kind: types.SegEmoji, URL: testSegURL("/alignment-image")}}
	seq := e.addEvent(segs, "alignment-images")
	e.start()
	e.f.Offer(seq, segs)
	e.idle()
	if e.row(seq, 0).State != store.MediaDone || e.row(seq, 1).State != store.MediaDone || len(mediaFiles(t, e)) != 1 {
		t.Fatal("图片/表情内容寻址未去重")
	}
	alignmentForwardTargets(t, e, seq)
}

// C06：近七天文件和图片补扫，本轮新增旧视频 / 文件不受时间窗阻挡。
func TestAlignmentSweepWindowBoundary(t *testing.T) {
	e := newEnv(t)
	e.f.noVideoTools()
	now := e.clk.Now()
	ctx := context.Background()
	cases := []struct {
		name     string
		age      time.Duration
		kind     string
		url      bool
		state    string
		tries    int
		calls    int
		selected bool
		want     string
	}{
		{"七天边界", 7 * 24 * time.Hour, types.SegImage, true, "", 0, 1, true, store.MediaDone},
		{"七天之前", 7*24*time.Hour + time.Millisecond, types.SegImage, true, "", 0, 0, false, ""},
		{"failed恰好24小时", 24 * time.Hour, types.SegImage, true, store.MediaFailed, maxAttempts, 1, true, store.MediaDone},
		{"failed超过24小时", 24*time.Hour + time.Millisecond, types.SegImage, true, store.MediaFailed, maxAttempts, 0, true, store.MediaFailed},
		{"failed已额外试过", time.Hour, types.SegImage, true, store.MediaFailed, maxAttempts + 1, 0, true, store.MediaFailed},
		{"既有过期终局", time.Hour, types.SegImage, true, store.MediaExpired, 0, 0, true, store.MediaExpired},
		{"导入缺URL", time.Hour, types.SegImage, false, "", 0, 0, false, ""},
		{"file七天边界", 7 * 24 * time.Hour, "file", true, "", 0, 1, true, store.MediaDone},
		{"file七天之前", 7*24*time.Hour + time.Millisecond, "file", true, "", 0, 0, false, ""},
	}
	seqs := make([]int64, len(cases))
	wantSelected := map[int64]bool{}
	for i, tc := range cases {
		path := fmt.Sprintf("/boundary-%d", i)
		e.serve(path, 200, png(tc.name))
		seg := types.Seg{Kind: tc.kind}
		if tc.kind == types.SegFile {
			seg.File = "编造图片.png"
		}
		if tc.url {
			seg.URL = testSegURL(path)
		}
		seqs[i] = e.addEventAt([]types.Seg{seg}, fmt.Sprint(2200+i), now.Add(-tc.age).UnixMilli())
		if tc.selected {
			wantSelected[seqs[i]] = true
		}
		if tc.state != "" {
			if err := e.st.PutMediaFetch(store.MediaFetch{EventSeq: seqs[i], Kind: tc.kind, State: tc.state, Tries: tc.tries, URLSig: store.MediaURLSig(seg.URL), UpdatedAt: now.UnixMilli()}); err != nil {
				t.Fatal(err)
			}
		}
	}
	after, err := e.st.BackfillEventHighWater(ctx)
	if err != nil {
		t.Fatal(err)
	}
	oldNew := []types.Seg{{Kind: types.SegImage, URL: testSegURL("/round-old-image")}, {Kind: types.SegVideo, URL: testSegURL("/round-old-video")}, {Kind: "file", File: "编造图片.png", URL: testSegURL("/round-old-file")}}
	e.serve("/round-old-image", 200, png("编造三十天旧图"))
	e.serve("/round-old-video", 200, tinyMP4())
	e.serve("/round-old-file", 200, png("编造三十天旧文件"))
	fresh := make([]int64, len(oldNew))
	for i, seg := range oldNew {
		fresh[i] = e.addEventAt([]types.Seg{seg}, fmt.Sprint(2300+i), now.Add(-30*24*time.Hour).UnixMilli())
		wantSelected[fresh[i]] = true
	}
	e.start()
	for i, seg := range oldNew {
		e.f.Offer(fresh[i], []types.Seg{seg})
	}
	before := e.f.sweeps.Load()
	e.f.Resweep()
	waitFor(t, "实际补扫结束", func() bool { return e.f.sweeps.Load() > before })
	e.idle()
	for i, tc := range cases {
		rows, err := e.st.MediaFetchRows(seqs[i])
		if err != nil {
			t.Fatal(err)
		}
		if rows[0].State != tc.want || e.nreq(fmt.Sprintf("/boundary-%d", i)) != tc.calls {
			t.Fatalf("%s state=%s want=%s calls=%d want=%d", tc.name, rows[0].State, tc.want, e.nreq(fmt.Sprintf("/boundary-%d", i)), tc.calls)
		}
	}
	for i := range 3 {
		rows, err := e.st.MediaFetchRows(fresh[i])
		if err != nil || rows[0].State != store.MediaDone {
			t.Fatal("本轮新入库的三十天旧媒体未下载", i, rows, err)
		}
	}
	targets, err := e.st.AlignmentMediaTargets(ctx, "10000", now.Add(-7*24*time.Hour).UnixMilli(), after, 1<<63-1, func(conv string) bool { return conv == testConv })
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range targets {
		if !wantSelected[target.Seq] {
			t.Fatalf("混入范围外目标 %+v", target)
		}
		delete(wantSelected, target.Seq)
	}
	if len(wantSelected) != 0 {
		t.Fatal("窗口或本轮旧资源遗漏", wantSelected)
	}
	if e.nreq("/round-old-image") != 1 || e.nreq("/round-old-video") != 1 || e.nreq("/round-old-file") != 1 {
		t.Fatal("本轮媒体被漏下或重复下载")
	}
}
func alignmentForwardTargets(t *testing.T, e *env, bodySeq int64) {
	const missing = "alignment-missing"
	t.Helper()
	ctx := context.Background()
	shared := types.Seg{Kind: types.SegImage, URL: testSegURL("/alignment-image")}
	root := types.ForwardTree{ID: "alignment-root", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{shared, {Kind: types.SegForward, Ref: "alignment-inner"}}}}}
	inner := types.ForwardTree{ID: "alignment-inner", Nodes: []types.ForwardMessageNode{{Segs: []types.Seg{shared, {Kind: types.SegVideo, URL: testSegURL("/forward-video")}, {Kind: types.SegFile, File: "编造图片.png", URL: testSegURL("/forward-file")}, {Kind: types.SegImage, URL: testSegURL("/forward-skipped"), Raw: "forward_limit"}, {Kind: types.SegEmoji, URL: shared.URL}}}}}
	e.serve("/forward-video", 200, tinyMP4())
	e.serve("/forward-file", 200, png("编造相同图片"))
	for _, tree := range []types.ForwardTree{root, inner} {
		if err := e.st.PutForward(ctx, tree, e.clk.Now().UnixMilli(), testConv); err != nil {
			t.Fatal(err)
		}
		if err := e.f.OfferForward(tree, testConv); err != nil {
			t.Fatal(err)
		}
	}
	e.addEvent([]types.Seg{{Kind: types.SegForward, Ref: root.ID}, {Kind: types.SegForward, Ref: missing}}, "2404")
	e.idle()
	targets, err := e.st.AlignmentMediaTargets(ctx, "10000", e.clk.Now().Add(-7*24*time.Hour).UnixMilli(), 0, 1<<63-1, func(conv string) bool { return conv == testConv })
	if err != nil {
		t.Fatal(err)
	}
	want := map[BackfillKey]string{{Seq: bodySeq, Index: 0}: store.MediaDone, {Seq: bodySeq, Index: 1}: store.MediaDone, {Forward: root.ID, Node: 0, Index: 0}: store.MediaDone, {Forward: inner.ID, Node: 0, Index: 0}: store.MediaDone, {Forward: inner.ID, Node: 0, Index: 1}: store.MediaDone, {Forward: inner.ID, Node: 0, Index: 2}: store.MediaDone, {Forward: inner.ID, Node: 0, Index: 3}: "skipped", {Forward: inner.ID, Node: 0, Index: 4}: store.MediaDone}
	want[BackfillKey{Forward: missing}] = "unchecked"
	for _, target := range targets {
		key := BackfillKey{Seq: target.Seq, Index: target.Index, Forward: target.Forward, Node: target.Node}
		state, ok := want[key]
		if !ok || target.State != state {
			t.Fatal("转发目标来源或终局不实", target, state, ok)
		}
		if key.Forward == missing && target.Kind != types.SegForward {
			t.Fatal("缺树被混作文件", target)
		}
		delete(want, key)
	}
	if len(want) != 0 {
		t.Fatal("漏统计正文或可见转发段", want)
	}
	if e.nreq("/forward-video") != 1 || e.nreq("/forward-file") != 1 || e.nreq("/forward-skipped") != 0 {
		t.Fatal("转发视频或文件未下载，或预算占位被下载")
	}
	if len(mediaFiles(t, e)) != 2 {
		t.Fatal("正文与转发相同内容没有复用同一实体")
	}
	t.Log("C03 forward image/emoji/file 与正文共用单实体；转发 video 使用现有下载任务，预算占位不下载")
	t.Run("缓存树不越过既有节点预算", func(t *testing.T) {
		tree := types.ForwardTree{ID: "alignment-budget", Nodes: make([]types.ForwardMessageNode, types.ForwardNodes+1)}
		tree.Nodes[types.ForwardNodes].Segs = []types.Seg{{Kind: types.SegImage, URL: testSegURL("/outside-budget")}}
		if err := e.st.PutForward(ctx, tree, e.clk.Now().UnixMilli(), testConv); err != nil {
			t.Fatal(err)
		}
		e.addEvent([]types.Seg{{Kind: types.SegForward, Ref: tree.ID}}, "2405")
		targets, err := e.st.AlignmentMediaTargets(ctx, "10000", e.clk.Now().Add(-7*24*time.Hour).UnixMilli(), 0, 1<<63-1, func(conv string) bool { return conv == testConv })
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range targets {
			if target.Forward == tree.ID {
				t.Fatal("统计越过可见树节点预算", target)
			}
		}
		if e.nreq("/outside-budget") != 0 {
			t.Fatal("为统计下载预算外媒体")
		}
	})
}
