package main

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
)

// 本地图片附件（规格-引用 第十节）：同一份图片缓存取本地文件、气泡里的图和宽高、待发和存好后同一个样子、输入框点开看大图。

func writePNG(t *testing.T, dir, name string, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			img.Set(x, y, color.RGBA{R: uint8(x * 255 / w), G: uint8(y * 255 / h), B: 120, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLocalFileRefRoundTripAndRemoteFallsThrough(t *testing.T) {
	t.Parallel()
	ref := localFileRef(`D:\假目录\图.png`, "")
	if p, ok := localFileOf(ref); !ok || p != `D:\假目录\图.png` {
		t.Fatalf("凭据该能取回路径：%q %v", p, ok)
	}
	if _, ok := localFileOf(api.MediaRef{Hash: "abc", Source: api.EventRef{Event: api.EventKey{Platform: "qq"}}}); ok {
		t.Fatal("别的平台的凭据不是本地文件")
	}
	asked := 0
	fetch := withLocalFiles(func(context.Context, api.MediaRef, int) ([]byte, error) { asked++; return []byte("远端"), nil })
	if b, err := fetch(t.Context(), api.MediaRef{Hash: "abc"}, 256); err != nil || string(b) != "远端" || asked != 1 {
		t.Fatalf("非本地凭据该照旧走后台：%q %v %d", b, err, asked)
	}
	if _, err := fetch(t.Context(), localFileRef(filepath.Join(t.TempDir(), "没有.png"), ""), 256); err == nil {
		t.Fatal("文件不在该报错，界面画占位")
	}
}

func TestReadLocalImageRejectsDirsAndOversize(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, err := readLocalImage(t.Context(), dir); err == nil {
		t.Fatal("目录不是图片")
	}
	big := filepath.Join(dir, "大.bin")
	f, err := os.Create(big)
	if err != nil {
		t.Fatal(err)
	}
	_ = f.Truncate(maxImageBytes + 1)
	_ = f.Close()
	if _, err := readLocalImage(t.Context(), big); err == nil {
		t.Fatal("超过收图上限的读不进来，和发送时的限制一致")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := readLocalImage(ctx, writePNG(t, dir, "a.png", 4, 4)); !errors.Is(err, context.Canceled) {
		t.Fatalf("取消了就别读：%v", err)
	}
}

// 存好的分块消息：图附件配上展示段，气泡按存档的宽高占位；文件附件仍是标签；展示段序号接在原有段后面。
func TestMsgFromViewBindsPartImagesWithDims(t *testing.T) {
	t.Parallel()
	key := api.ConversationKey{Platform: "local", Channel: "console", Scene: "private", Conversation: "console:char00"}
	v := partsView(key)
	v.Parts[0].Attachments[0].Width, v.Parts[0].Attachments[0].Height = 800, 600
	m := msgFromView(key, v, func(string) (character, bool) { return character{}, false })
	if len(m.segs) != 1 || m.segs[0].Kind != "image" || m.segs[0].position != 0 || m.segs[0].Width != 800 || m.segs[0].Height != 600 {
		t.Fatalf("该有一个图片展示段：%+v", m.segs)
	}
	if p, ok := localFileOf(refOf(m.segs[0])); !ok || p != "D:/库/abc.png" {
		t.Fatalf("展示段的凭据该指向附件路径：%+v", refOf(m.segs[0]))
	}
	atts := chatMessage(m).Parts[0].Atts
	if !atts[0].Show || atts[0].Pos != 0 || atts[0].Width != 800 || atts[0].Height != 600 || atts[1].Show {
		t.Fatalf("图附件该画成真图、文件不画：%+v", atts)
	}
}

// 待发气泡和存好后的气泡：同样的附件，算出来的块一模一样（接替时不跳）。
func TestPendingPartsMatchStoredParts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writePNG(t, dir, "图.png", 40, 30)
	blk := &chatui.Block{Text: "看这个", Atts: []chatui.Att{
		{Kind: chatui.AttImage, Name: "图.png", Path: path, Width: 40, Height: 30},
		{Kind: chatui.AttFile, Name: "笔记.txt", Path: filepath.Join(dir, "笔记.txt")},
	}}
	pending := &msg{}
	pending.chatParts = pendingParts([]*chatui.Block{blk})
	pending.bindPartImages(pending.chatParts, pendingPartImages([]*chatui.Block{blk}))

	stored := &msg{}
	view := api.MessageView{Parts: []api.MessagePart{{Text: "看这个", Attachments: []api.LocalAttachment{
		{Kind: "image", Path: path, Name: "图.png", Width: 40, Height: 30},
		{Kind: "file", Path: filepath.Join(dir, "笔记.txt"), Name: "笔记.txt"},
	}}}}
	stored.chatParts = chatParts(view)
	stored.bindPartImages(stored.chatParts, storedPartImages(view))

	if len(pending.chatParts) != 1 || len(stored.chatParts) != 1 {
		t.Fatalf("块数不对：%d %d", len(pending.chatParts), len(stored.chatParts))
	}
	for i, a := range pending.chatParts[0].Atts {
		if a != stored.chatParts[0].Atts[i] {
			t.Fatalf("附件 %d：待发 %+v，存好 %+v", i, a, stored.chatParts[0].Atts[i])
		}
	}
	if len(pending.segs) != 1 || pending.segs[0].Width != 40 || mediaID(pending.segs[0]) != mediaID(stored.segs[0]) {
		t.Fatalf("两边的图该共用同一份缓存：%+v %+v", pending.segs, stored.segs)
	}
}

// 整条取图的路径：消息里的图 -> chatMedia.Image -> 图片缓存读文件 -> 位图；文件不在时是 Failed、不崩。
func TestChatMediaLoadsPartImageFromFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writePNG(t, dir, "图.png", 64, 32)
	m := &msg{id: "m", version: 1}
	view := api.MessageView{Parts: []api.MessagePart{{Attachments: []api.LocalAttachment{
		{Kind: "image", Path: path, Name: "图.png", Width: 64, Height: 32},
		{Kind: "image", Path: filepath.Join(dir, "丢了.png"), Name: "丢了.png", Width: 64, Height: 32},
	}}}}
	m.chatParts = chatParts(view)
	m.bindPartImages(m.chatParts, storedPartImages(view))
	a := &app{msgs: []*msg{m}, gen: 1, srcGen: 1}
	// 结果要排进“界面线程”（这里是测试自己的队列）再落地，和正式程序一样；直接在读图线程里落地会和下面读结果的测试线程抢同一条记录
	q := &uiQueue{}
	st := newImageStore(t.Context(), q.post)
	st.scale = 1
	st.fetch = withLocalFiles(func(context.Context, api.MediaRef, int) ([]byte, error) { return nil, errors.New("不该走后台") })
	r := &chatMedia{a: a, message: m, index: 0, gen: 1, sourceGen: 1, version: 1, images: st}

	deadline := time.Now().Add(uiWaitLimit)
	var ok, bad chatui.MediaResult
	for time.Now().Before(deadline) {
		ok, bad = r.Image("m", 0, false, 128), r.Image("m", 1, false, 128)
		st.wg.Wait()
		q.drain()
		ok, bad = r.Image("m", 0, false, 128), r.Image("m", 1, false, 128)
		if ok.State == chatui.MediaReady && bad.State == chatui.MediaFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ok.State != chatui.MediaReady || ok.Image == nil {
		t.Fatalf("读得到的图该 Ready：%+v", ok)
	}
	if bad.State != chatui.MediaFailed {
		t.Fatalf("文件不在该 Failed：%+v", bad)
	}
}

// 输入框点缩略图：用现成的看大图打开，输入区里读好的图都在里面，从被点的那张开始；还在保存的、没读到图的不进。
func TestOpenComposerImageUsesChatLightbox(t *testing.T) {
	t.Parallel()
	p := chatui.NewComposer()
	src, err := ui.DecodeImage(pngBytes(t, 8, 8))
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]uint64, 3)
	ids[0], _ = p.Attach(nil, chatui.Att{Kind: chatui.AttImage, Name: "甲.png", Path: "D:/假目录/甲.png", Thumb: src, Width: 40, Height: 30})
	p.Attach(nil, chatui.Att{Kind: chatui.AttImage, Name: "保存中", Pending: true})
	ids[1], _ = p.Attach(nil, chatui.Att{Kind: chatui.AttImage, Name: "乙.png", Path: "D:/假目录/乙.png", Thumb: src, Width: 30, Height: 40})
	ids[2], _ = p.Attach(nil, chatui.Att{Kind: chatui.AttImage, Name: "丙.png", Path: "D:/假目录/丙.png", Thumb: src})
	a := &app{gen: 2, srcGen: 3}
	a.openComposerImage(p, ids[1])
	v := a.chatLightbox
	if !v.open || len(v.segments) != 3 || v.at != 1 || v.segments[1].Name != "乙.png" || v.segments[1].Width != 30 {
		t.Fatalf("该开着、三张、从第二张看：%+v", v)
	}
	if path, ok := localFileOf(refOf(v.segments[0])); !ok || path != "D:/假目录/甲.png" {
		t.Fatalf("取原图走本地文件凭据：%+v", refOf(v.segments[0]))
	}
	if !v.alive(a) {
		t.Fatal("同一会话里看大图该一直活着")
	}
}

func pngBytes(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// 设 ATTACH_PERF=1 时量输入框缩略图的解码耗时和内存（在后台线程做，界面线程只收结果）；平时跳过。
func TestPerfAttachThumbDecode(t *testing.T) {
	t.Parallel()
	if os.Getenv("ATTACH_PERF") == "" {
		t.Skip("设 ATTACH_PERF 才量")
	}
	for _, sz := range [][2]int{{1920, 1080}, {4000, 3000}, {8000, 6000}} {
		img := image.NewRGBA(image.Rect(0, 0, sz[0], sz[1]))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = uint8(i>>12), uint8(i>>10), uint8(i>>14), 255
		}
		var buf bytes.Buffer
		enc := png.Encoder{CompressionLevel: png.BestSpeed}
		if err := enc.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		data := buf.Bytes()
		start := time.Now()
		w, h := imageDimsOf(data)
		dims := time.Since(start)
		start = time.Now()
		bmp, err := decodeStatic(data, attachThumbTier)
		if err != nil {
			t.Fatal(err)
		}
		bw, bh := bmp.Size()
		t.Logf("%d×%d（文件 %.1f MB）：量宽高 %v（得到 %d×%d），解 %d 宽的缩略图 %v，位图 %d×%d = %.2f MB（整张解开要 %.0f MB）",
			sz[0], sz[1], float64(len(data))/1e6, dims, w, h, attachThumbTier, time.Since(start), bw, bh, float64(bw*bh*4)/1e6, float64(sz[0]*sz[1]*4)/1e6)
	}
}

// 文件被改写后，同一路径的缓存键要变：凭据带修改时间和大小，取出的路径不变，读到的是新内容。
func TestLocalFileStampSeparatesRewrittenFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writePNG(t, dir, "图.png", 8, 8)
	info1, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	s1 := fileStamp(info1)
	writePNG(t, dir, "图.png", 16, 8) // 别的软件改写：大小不同
	info2, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	s2 := fileStamp(info2)
	if s1 == s2 {
		t.Fatalf("改写后版本该不同：%q %q", s1, s2)
	}
	r1, r2 := localFileRef(path, s1), localFileRef(path, s2)
	if r1.Hash == r2.Hash {
		t.Fatal("同一路径、不同版本，缓存键该不同")
	}
	for _, r := range []api.MediaRef{r1, r2} {
		if p, ok := localFileOf(r); !ok || p != path {
			t.Fatalf("带版本的凭据仍该取回路径：%q %v", p, ok)
		}
	}
	// 经待发气泡：附件上的版本带进展示段的凭据。
	blk := &chatui.Block{Atts: []chatui.Att{{Kind: chatui.AttImage, Name: "图.png", Path: path, Stamp: s2, Width: 16, Height: 8}}}
	m := &msg{}
	m.chatParts = pendingParts([]*chatui.Block{blk})
	m.bindPartImages(m.chatParts, pendingPartImages([]*chatui.Block{blk}))
	if len(m.segs) != 1 || refOf(m.segs[0]).Hash != r2.Hash {
		t.Fatalf("展示段该带版本：%+v", m.segs)
	}
}

// 拖入挂附件时记下文件版本。
func TestAttachPathsRecordsStamp(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writePNG(t, dir, "图.png", 8, 8)
	p := chatui.NewComposer()
	a := &app{gen: 1, srcGen: 1}
	a.attachPaths(p, nil, []string{path})
	var got []chatui.Att
	for _, b := range p.Blocks() {
		got = append(got, b.Atts...)
	}
	if len(got) != 1 || got[0].Stamp == "" {
		t.Fatalf("图片附件该记版本：%+v", got)
	}
}

// 缩略图还没量完（宽高 0）就发送：待发气泡提交时读文件头，一开始就是真比例，存好后不跳。
func TestPendingPartImagesMeasuresUnmeasuredImage(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := writePNG(t, dir, "竖.png", 20, 30)
	blk := &chatui.Block{Atts: []chatui.Att{
		{Kind: chatui.AttImage, Name: "竖.png", Path: path},
		{Kind: chatui.AttImage, Name: "丢了.png", Path: filepath.Join(dir, "丢了.png")},
	}}
	got := pendingPartImages([]*chatui.Block{blk})[0]
	if got[0].Width != 20 || got[0].Height != 30 {
		t.Fatalf("该量出 20×30：%+v", got[0])
	}
	if got[1].Width != 0 || got[1].Height != 0 {
		t.Fatalf("读不到的保持 0：%+v", got[1])
	}
}
