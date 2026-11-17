package mediafetch

import (
	"bytes"
	"context"
	"image/jpeg"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
)

func tinyMP4() []byte { return []byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom") }

// noVideoTools 把这个下载器当成没装 ffmpeg/ffprobe：第一次处理视频时不去查登记表，也不写缺工具警告。
func (f *Fetcher) noVideoTools() {
	f.videoToolsOnce.Do(func() {})
	f.videoTools = videoTools{}
}

// 走真实 HTTP、文件落盘、事务回填，工具缺失不妨碍视频下载；图片与视频分别配刷新地址、分别限额。
func TestVideoDownloadLimitsAndRefresh(t *testing.T) {
	old := findVideoTools
	findVideoTools = func() videoTools { return videoTools{} }
	defer func() { findVideoTools = old }()
	for _, tc := range []struct {
		name      string
		status    int
		body      []byte
		size      int64
		freshSize int64
		chunked   bool
		want      string
	}{
		{name: "mp4", status: 200, body: tinyMP4(), want: store.MediaDone},
		{name: "known-large", status: 200, body: tinyMP4(), size: 51 << 20, want: store.MediaTooBig},
		{name: "declared-large", status: 200, body: append(tinyMP4(), bytes.Repeat([]byte{0}, 1024)...), want: store.MediaTooBig},
		{name: "stream-large", status: 200, body: append(tinyMP4(), bytes.Repeat([]byte{0}, 1024)...), chunked: true, want: store.MediaTooBig},
		{name: "unknown", status: 200, body: []byte("<html>not a video</html>"), want: store.MediaNotVideo},
		{name: "image", status: 200, body: png("not video"), want: store.MediaNotVideo},
		{name: "refresh400", status: 400, want: store.MediaDone},
		{name: "refresh403", status: 403, want: store.MediaDone},
		{name: "refresh404", status: 404, want: store.MediaDone},
		{name: "refresh410", status: 410, want: store.MediaDone},
		{name: "refresh-known-large", status: 403, freshSize: 2048, want: store.MediaTooBig},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, func(o *envOpt) { o.maxBytes = 16 })
			e.f.dl.videoMax = 1024
			e.handle("/v", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				if tc.chunked {
					w.(http.Flusher).Flush()
				}
				w.Write(tc.body)
			})
			e.serve("/new-video", 200, tinyMP4())
			e.refresh.answer([]types.Seg{{Kind: types.SegImage, File: "same", URL: testSegURL("/wrong-image")}, {Kind: types.SegVideo, File: "same", URL: testSegURL("/new-video"), Size: tc.freshSize}}, nil)
			segs := []types.Seg{{Kind: types.SegVideo, File: "same", URL: testSegURL("/v"), Size: tc.size}}
			seq := e.addEvent(segs, "v1")
			before := e.body(seq)
			e.start()
			e.f.Offer(seq, segs)
			e.idle()
			row := e.row(seq, 0)
			if row.State != tc.want {
				t.Fatalf("state=%+v want=%s", row, tc.want)
			}
			if tc.name == "declared-large" && row.Size != int64(len(tc.body)) {
				t.Fatalf("已知完整大小丢失：%+v", row)
			}
			if tc.want == store.MediaDone {
				hash := sha(tinyMP4())
				if e.segHash(seq, 0) != hash || row.Hash != hash || row.Size != int64(len(tinyMP4())) {
					t.Fatalf("视频未回填：%+v", row)
				}
				if strings.Replace(e.body(seq), `,"hash":"`+hash+`"`, "", 1) != before {
					t.Fatal("回填改动了 hash 以外的 JSON")
				}
				if row.CoverHash != "" || row.DurationMS != 0 {
					t.Fatalf("缺工具不该伪造元数据：%+v", row)
				}
				if len(mediaFiles(t, e)) != 1 {
					t.Fatal("视频应只落一个原文件")
				}
			} else if len(mediaFiles(t, e)) != 0 || e.segHash(seq, 0) != "" {
				t.Fatal("拒绝的视频不应落盘或回填")
			}
			if tc.size > 0 && e.nreq("/v") != 0 {
				t.Fatal("已知超限不应发请求")
			}
			if tc.status != 200 {
				wantRequests := 1
				if tc.freshSize > 1024 {
					wantRequests = 0
				}
				if e.refresh.count() != 1 || e.nreq("/new-video") != wantRequests || e.nreq("/wrong-image") != 0 {
					t.Fatal("刷新必须按视频配对，并先检查协议端给的新大小")
				}
			}
		})
	}
	// 已收到的旧视频不因图片补扫顺带下载；图片仍走它自己的限额。
	e := newEnv(t, func(o *envOpt) { o.maxBytes = 16 })
	e.serve("/image", 200, png("超过图片自己的限额"))
	segs := []types.Seg{{Kind: types.SegVideo, URL: testSegURL("/old-video")}, imgSeg("/image")}
	seq := e.addEvent(segs, "old")
	e.start()
	e.f.Resweep()
	waitFor(t, "图片补扫结束", func() bool { return e.f.sweeps.Load() > 0 })
	e.idle()
	if e.nreq("/old-video") != 0 || e.row(seq, 1).State != store.MediaTooBig {
		t.Fatal("视频补扫或图片上限被改变")
	}
}

// 用真正编码的短片走完整下载与抽帧路径，验证库中的尺寸、时长和 JPEG 内容。
func TestVideoMetadataFFmpeg(t *testing.T) {
	tools := findVideoTools()
	if tools.ffmpeg == "" || tools.ffprobe == "" {
		t.Skip("没有 ffmpeg/ffprobe")
	}
	e := newEnv(t)
	sample := filepath.Join(t.TempDir(), "编造的短片.mp4")
	if _, err := videoCommand(context.Background(), tools.ffmpeg, 64<<10, "-v", "error", "-nostdin", "-f", "lavfi", "-i", "color=c=red:s=160x90:r=10", "-t", "1.2", "-c:v", "mpeg4", "-threads", "1", sample); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(sample)
	if err != nil {
		t.Fatal(err)
	}
	e.serve("/real", 200, b)
	segs := []types.Seg{{Kind: types.SegVideo, URL: testSegURL("/real")}}
	seq := e.addEvent(segs, "real")
	e.start()
	e.f.Offer(seq, segs)
	e.idle()
	row := e.row(seq, 0)
	if row.State != store.MediaDone || row.Width != 160 || row.Height != 90 || row.DurationMS != 1200 || row.Size != int64(len(b)) {
		t.Fatalf("视频元数据：%+v", row)
	}
	f, err := mediastore.OpenMedia(e.data, "qq", row.CoverHash)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cover, err := jpeg.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if cover.Bounds().Dx() != 160 || cover.Bounds().Dy() != 90 {
		t.Fatalf("封面尺寸：%v", cover.Bounds())
	}
	r, g, bv, _ := cover.At(80, 45).RGBA()
	if r < 50000 || g > 10000 || bv > 10000 {
		t.Fatalf("首帧颜色不对：%d %d %d", r, g, bv)
	}
	t.Logf("短片 %d 字节，%dx%d，%d ms；JPEG %dx%d", row.Size, row.Width, row.Height, row.DurationMS, cover.Bounds().Dx(), cover.Bounds().Dy())
}

func TestVideoProbeBoundary(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		w, h int
		ms   int64
		bad  bool
	}{
		{`{"streams":[{"width":90,"height":160,"side_data_list":[{"rotation":90}]}],"format":{"duration":"1.25"}}`, 160, 90, 1250, false},
		{`{"streams":[{"width":160,"height":90}],"format":{"duration":"NaN"}}`, 160, 90, 0, false},
		{`{"streams":[{"width":160,"height":90}],"format":{"duration":"-1"}}`, 160, 90, 0, false},
		{`{"streams":[{"width":-1,"height":90}]}`, 0, 0, 0, true},
		{`{"streams":[{"width":70000,"height":90}]}`, 0, 0, 0, true},
		{`{"streams":[]}`, 0, 0, 0, true},
		{`invalid`, 0, 0, 0, true},
	} {
		w, h, ms, err := probeVideo([]byte(tc.raw))
		if (err != nil) != tc.bad || w != tc.w || h != tc.h || ms != tc.ms {
			t.Fatalf("%s => %dx%d %d %v", tc.raw, w, h, ms, err)
		}
	}
	out := videoOutput{max: 4}
	if _, err := out.Write([]byte("12345")); err == nil {
		t.Fatal("外部输出必须有上限")
	}
}
