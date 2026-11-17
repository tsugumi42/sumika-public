package mediafetch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/mediastore"
	"sumika-native/internal/store"
)

func mediaFiles(t *testing.T, e *env) []string {
	t.Helper()
	var out []string
	root := filepath.Join(e.data, "go", "media", "qq")
	filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info.Mode().IsRegular() {
			rel, _ := filepath.Rel(root, path)
			out = append(out, rel)
		}
		return nil
	})
	return out
}

// M2：成功路径。文件落在 <根>/qq/<aa>/<hash>，hash 等于内容的 sha256，OpenMedia 读得出；
// 事件里该段的 hash 回填，同一行别的字节（url、file、别的段、tags、line、from）逐字节不变；media_fetch 是 done
func TestDownloadSuccess(t *testing.T) {
	e := newEnv(t)
	content := png("甲")
	e.serve("/a", 200, content)
	segs := []types.Seg{{Kind: types.SegText, Text: "编的话 & <>"}, imgSeg("/a"), {Kind: types.SegFace, Ref: "14"}}
	seq := e.addEvent(segs, "7001")
	before := e.body(seq)
	e.start()
	e.f.Offer(seq, segs)
	e.idle()

	hash := sha(content)
	if got := e.segHash(seq, 1); got != hash {
		t.Fatalf("段的 hash：%q，要 %q", got, hash)
	}
	after := e.body(seq)
	if strings.Replace(after, `,"hash":"`+hash+`"`, "", 1) != before || after == before {
		t.Fatalf("前后不只差一个 hash：\n%s\n%s", before, after)
	}
	f, err := mediastore.OpenMedia(e.data, "qq", hash)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(f)
	f.Close()
	if !bytes.Equal(got, content) {
		t.Fatal("读出来的内容不一样")
	}
	if files := mediaFiles(t, e); len(files) != 1 || files[0] != filepath.Join(hash[:2], hash) {
		t.Fatalf("媒体库里的文件：%v", files)
	}
	if r := e.row(seq, 1); r.State != store.MediaDone || r.Hash != hash || r.LastError != "" {
		t.Fatalf("media_fetch：%+v", r)
	}
	if e.nreq("/a") != 1 {
		t.Fatalf("请求了 %d 次", e.nreq("/a"))
	}
}

// M3：两条消息同一张图 —— 一个文件，两个段都回填
func TestSameImageInTwoMessages(t *testing.T) {
	e := newEnv(t)
	content := png("同一张")
	e.serve("/x", 200, content)
	e.serve("/y", 200, content) // 不同地址、不同文件名，内容一样
	s1, s2 := []types.Seg{imgSeg("/x")}, []types.Seg{imgSeg("/y")}
	q1, q2 := e.addEvent(s1, "1"), e.addEvent(s2, "2")
	e.start()
	e.f.Offer(q1, s1)
	e.f.Offer(q2, s2)
	e.idle()
	h := sha(content)
	if e.segHash(q1, 0) != h || e.segHash(q2, 0) != h {
		t.Fatalf("两个段都该回填：%q %q", e.segHash(q1, 0), e.segHash(q2, 0))
	}
	if files := mediaFiles(t, e); len(files) != 1 {
		t.Fatalf("该只有一个文件：%v", files)
	}
}

// M4：不是位图不落盘。HTML 错误页、SVG、压缩包、空内容 → not_image；正文说 expired 的 → expired；404/410 → expired
func TestNotBitmapIsNotStored(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{"HTML 错误页", 200, "<html><body>出错了</body></html>", store.MediaNotImage},
		{"SVG", 200, `<svg xmlns="http://www.w3.org/2000/svg"><script>alert(1)</script></svg>`, store.MediaNotImage},
		{"压缩包", 200, "PK\x03\x04\x14\x00 压缩包", store.MediaNotImage},
		{"空内容", 200, "", store.MediaNotImage},
		{"BMP 头不对", 200, "BM只有两个字母不算", store.MediaNotImage},
		{"正文说过期", 200, "<html>Link expired</html>", store.MediaExpired},
		{"404", 404, "not found", store.MediaExpired},
		{"410", 410, "gone", store.MediaExpired},
		{"403 没有刷新能力", 403, "forbidden", store.MediaExpired},
		{"400 是 QQ 的地址过期", 400, `{"retcode":-5503007,"retmsg":"download url has expired","retryflag":0}`, store.MediaExpired},
		{"别的 4xx 不重试", 401, "unauthorized", store.MediaFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t, func(o *envOpt) { o.noRefresh = true })
			e.serve("/p", c.status, []byte(c.body))
			segs := []types.Seg{imgSeg("/p")}
			seq := e.addEvent(segs, "1")
			e.start()
			e.f.Offer(seq, segs)
			e.idle()
			if r := e.row(seq, 0); r.State != c.want || r.Hash != "" {
				t.Fatalf("状态 %+v，要 %s", r, c.want)
			}
			if e.segHash(seq, 0) != "" {
				t.Fatal("不该回填")
			}
			if files := mediaFiles(t, e); len(files) != 0 {
				t.Fatalf("不该落盘：%v", files)
			}
			if e.nreq("/p") != 1 {
				t.Fatalf("不该重试：请求了 %d 次", e.nreq("/p"))
			}
			// 原段原样留着：地址和文件名还在
			if !strings.Contains(e.body(seq), "rkey="+secret) {
				t.Fatal("下载失败不该动段里的地址")
			}
		})
	}
}

// M5：Content-Length 超限不读正文；没有 Content-Length 的无限流在上限处中止，临时文件被删
func TestTooBig(t *testing.T) {
	e := newEnv(t, func(o *envOpt) { o.maxBytes = 1024 })
	var bodyBytes atomic.Int64
	e.handle("/declared", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", "99999")
		w.WriteHeader(200)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // 正文一个字节都不写：客户端看了头就该放弃
	})
	e.handle("/endless", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write(png("无限"))
		chunk := bytes.Repeat([]byte("z"), 512)
		for {
			n, err := w.Write(chunk)
			bodyBytes.Add(int64(n))
			if err != nil || r.Context().Err() != nil {
				return
			}
		}
	})
	e.serve("/exact", 200, append(png(""), bytes.Repeat([]byte("e"), 1024-8)...)) // 刚好 1024
	e.handle("/plus1", func(w http.ResponseWriter, r *http.Request) {             // 不声明长度（chunked），比上限多一个字节
		w.Write(png(""))
		w.(http.Flusher).Flush()
		w.Write(bytes.Repeat([]byte("p"), 1024-8+1))
	})
	segs := []types.Seg{imgSeg("/declared"), imgSeg("/endless"), imgSeg("/exact"), imgSeg("/plus1")}
	seq := e.addEvent(segs, "1")
	e.start()
	e.f.Offer(seq, segs)
	e.idle()
	if r := e.row(seq, 0); r.State != store.MediaTooBig {
		t.Fatalf("声明超限：%+v", r)
	}
	if r := e.row(seq, 1); r.State != store.MediaTooBig {
		t.Fatalf("无限流：%+v", r)
	}
	if bodyBytes.Load() > 4<<20 {
		t.Fatalf("无限流写了太多：%d 字节（连接该在上限处就断开）", bodyBytes.Load())
	}
	if r := e.row(seq, 2); r.State != store.MediaDone {
		t.Fatalf("刚好等于上限要收：%+v", r)
	}
	if r := e.row(seq, 3); r.State != store.MediaTooBig {
		t.Fatalf("多一个字节要拒：%+v", r)
	}
	if files := mediaFiles(t, e); len(files) != 1 {
		t.Fatalf("只有刚好的那张该落盘：%v", files)
	}
}

// M6：服务器一直 500 —— 共试 3 次，间隔 0、30 秒、5 分钟（假钟看），用完记 failed
func TestRetrySchedule(t *testing.T) {
	e := newEnv(t)
	e.serve("/p", 500, []byte("oops"))
	segs := []types.Seg{imgSeg("/p")}
	seq := e.addEvent(segs, "1")
	e.start()
	t0 := e.clk.Now()
	e.f.Offer(seq, segs)
	waitFor(t, "第 1 次", func() bool { return e.nreq("/p") == 1 })
	e.clk.WaitPending(t, 1)
	e.clk.Advance(29 * time.Second)
	time.Sleep(50 * time.Millisecond)
	if e.nreq("/p") != 1 {
		t.Fatal("30 秒没到就重试了")
	}
	e.clk.Advance(time.Second)
	waitFor(t, "第 2 次", func() bool { return e.nreq("/p") == 2 })
	e.clk.WaitPending(t, 1)
	e.clk.Advance(5*time.Minute - time.Second)
	time.Sleep(50 * time.Millisecond)
	if e.nreq("/p") != 2 {
		t.Fatal("5 分钟没到就重试了")
	}
	e.clk.Advance(time.Second)
	e.idle()
	reqs := e.requests()
	if len(reqs) != 3 {
		t.Fatalf("请求了 %d 次", len(reqs))
	}
	if d1, d2 := reqs[1].at.Sub(reqs[0].at), reqs[2].at.Sub(reqs[1].at); reqs[0].at != t0 || d1 != 30*time.Second || d2 != 5*time.Minute {
		t.Fatalf("间隔 %v %v", d1, d2)
	}
	if r := e.row(seq, 0); r.State != store.MediaFailed || r.Tries != 3 || r.LastError != "HTTP 500" {
		t.Fatalf("终局：%+v", r)
	}
	e.clk.Advance(time.Hour)
	time.Sleep(50 * time.Millisecond)
	if e.nreq("/p") != 3 {
		t.Fatal("用完了还在重试")
	}
}

// 超时算网络错误，重试；第二次成功就是 done，tries 记着试过一次
func TestTimeoutThenSuccess(t *testing.T) {
	e := newEnv(t, func(o *envOpt) { o.timeout = 150 * time.Millisecond })
	var n atomic.Int32
	content := png("慢")
	e.handle("/slow", func(w http.ResponseWriter, r *http.Request) {
		if n.Add(1) == 1 {
			<-r.Context().Done() // 第一次一直不回
			return
		}
		w.Write(content)
	})
	segs := []types.Seg{imgSeg("/slow")}
	seq := e.addEvent(segs, "1")
	e.start()
	e.f.Offer(seq, segs)
	e.clk.WaitPending(t, 1)
	if r := e.row(seq, 0); r.State != store.MediaPending || r.Tries != 1 || r.LastError != "超时" || r.NextAt != e.clk.Now().Add(30*time.Second).UnixMilli() {
		t.Fatalf("超时以后：%+v", r)
	}
	e.clk.Advance(30 * time.Second)
	e.idle()
	if r := e.row(seq, 0); r.State != store.MediaDone || r.Tries != 1 || r.Hash != sha(content) {
		t.Fatalf("第二次成功：%+v", r)
	}
}

// 403（链接过期）先向协议端换新地址；同一条消息的几张图共用一次查询；换来的地址下载成功就是 done
func TestRefreshOnForbidden(t *testing.T) {
	e := newEnv(t)
	ca, cb := png("甲"), png("乙")
	e.serve("/old-a", 403, []byte("forbidden"))
	e.serve("/old-b", 403, []byte("forbidden"))
	e.serve("/new-a", 200, ca)
	e.serve("/new-b", 200, cb)
	segs := []types.Seg{imgSeg("/old-a"), imgSeg("/old-b")}
	// 协议端回来的顺序和文件名都对得上；顺序故意反过来，要按文件名对
	e.refresh.segs = []types.Seg{
		{Kind: types.SegImage, File: segs[1].File, URL: testSegURL("/new-b")},
		{Kind: types.SegImage, File: segs[0].File, URL: testSegURL("/new-a")},
	}
	seq := e.addEvent(segs, "7001")
	e.start()
	e.f.Offer(seq, segs)
	e.idle()
	if e.segHash(seq, 0) != sha(ca) || e.segHash(seq, 1) != sha(cb) {
		t.Fatalf("要按文件名对：%q %q", e.segHash(seq, 0), e.segHash(seq, 1))
	}
	if e.refresh.count() != 1 || e.refresh.calls[0] != testConv+"|7001" {
		t.Fatalf("同一条消息只该问一次：%v", e.refresh.calls)
	}
	// 段里存的还是原来的地址
	if !strings.Contains(e.body(seq), "/old-a?rkey="+secret) {
		t.Fatal("换新地址不该改段里存的地址")
	}
}

// QQ 地址过期回 400（真机上见到的正文）：和 403 一样换新地址再下
func TestRefreshOnQQExpired400(t *testing.T) {
	e := newEnv(t)
	c := png("过期的图")
	e.serve("/old", 400, []byte(`{"retcode":-5503007,"retmsg":"download url has expired","retryflag":0}`))
	e.serve("/new", 200, c)
	segs := []types.Seg{imgSeg("/old")}
	e.refresh.segs = []types.Seg{{Kind: types.SegImage, File: segs[0].File, URL: testSegURL("/new")}}
	seq := e.addEvent(segs, "7002")
	e.start()
	e.f.Offer(seq, segs)
	e.idle()
	if e.segHash(seq, 0) != sha(c) || e.refresh.count() != 1 {
		t.Fatalf("400 要换新地址再下：hash=%q 问了 %d 次", e.segHash(seq, 0), e.refresh.count())
	}
}

// 换新地址的各种对不上：不猜
func TestRefreshDoesNotGuess(t *testing.T) {
	newSeg := func(file, path string) types.Seg {
		return types.Seg{Kind: types.SegImage, File: file, URL: testSegURL(path)}
	}
	cases := []struct {
		name  string
		fresh []types.Seg // 协议端回来的
		noEv  bool        // 事件没有平台消息号
		want  string      // 原来第 0 段的终局状态
		hit   bool        // 新地址该被请求
	}{
		{"文件名对上", []types.Seg{newSeg("a.jpg", "/new")}, false, store.MediaDone, true},
		{"文件名对不上但张数一样，按先后", []types.Seg{newSeg("别的名字.jpg", "/new")}, false, store.MediaDone, true},
		{"文件名对不上张数也不一样", []types.Seg{newSeg("别的.jpg", "/new"), newSeg("又一个.jpg", "/new")}, false, store.MediaExpired, false},
		{"协议端什么图都没有", nil, false, store.MediaExpired, false},
		{"换回来的还是旧地址", []types.Seg{newSeg("a.jpg", "/old")}, false, store.MediaExpired, false},
		{"事件没有平台消息号", []types.Seg{newSeg("a.jpg", "/new")}, true, store.MediaExpired, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.serve("/old", 404, []byte("gone"))
			e.serve("/new", 200, png("新"))
			e.refresh.segs = c.fresh
			segs := []types.Seg{{Kind: types.SegImage, File: "a.jpg", URL: testSegURL("/old")}}
			pid := "7001"
			if c.noEv {
				pid = ""
			}
			seq := e.addEvent(segs, pid)
			e.start()
			e.f.Offer(seq, segs)
			e.idle()
			if r := e.row(seq, 0); r.State != c.want {
				t.Fatalf("状态 %+v，要 %s", r, c.want)
			}
			if (e.nreq("/new") > 0) != c.hit {
				t.Fatalf("新地址请求了 %d 次", e.nreq("/new"))
			}
		})
	}
}

// 换来的新地址仍然 403：记 expired，不再换第二次；协议端报错：记 expired
func TestRefreshOnlyOncePerRun(t *testing.T) {
	e := newEnv(t)
	e.serve("/old", 403, nil)
	e.serve("/new", 403, nil)
	e.refresh.segs = []types.Seg{{Kind: types.SegImage, File: "a.jpg", URL: testSegURL("/new")}}
	segs := []types.Seg{{Kind: types.SegImage, File: "a.jpg", URL: testSegURL("/old")}}
	seq := e.addEvent(segs, "7001")
	e.start()
	e.f.Offer(seq, segs)
	e.idle()
	if r := e.row(seq, 0); r.State != store.MediaExpired {
		t.Fatalf("%+v", r)
	}
	if e.refresh.count() != 1 || e.nreq("/new") != 1 {
		t.Fatalf("问了 %d 次，新地址请求了 %d 次", e.refresh.count(), e.nreq("/new"))
	}
}

// 协议端明确说不行（普通错误）：记 expired。没问成（没连上、超时、取消）：不记任何终局状态，也不改段
func TestRefreshErrorKinds(t *testing.T) {
	for _, c := range []struct {
		name     string
		err      error
		terminal bool
	}{
		{"协议端拒绝", errors.New("get_msg 被协议端拒绝（retcode 100）"), true},
		{"没连上", fmt.Errorf("%w：没连上", ErrRefreshUnavailable), false},
		{"超时", context.DeadlineExceeded, false},
		{"被取消", context.Canceled, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := newEnv(t)
			e.serve("/old", 410, nil)
			e.refresh.err = c.err
			segs := []types.Seg{imgSeg("/old")}
			seq := e.addEvent(segs, "7001")
			e.start()
			e.f.Offer(seq, segs)
			e.idle()
			rows, err := e.st.MediaFetchRows(seq)
			if err != nil {
				t.Fatal(err)
			}
			if c.terminal {
				if r := rows[0]; r.State != store.MediaExpired {
					t.Fatalf("%+v", r)
				}
				return
			}
			if len(rows) != 0 {
				t.Fatalf("没问成不该记任何状态：%+v", rows)
			}
			if got := e.body(seq); !strings.Contains(got, "/old?rkey="+secret) || e.segHash(seq, 0) != "" {
				t.Fatalf("段该原样留着：%s", got)
			}
		})
	}
}

// 工人启动时尚未连接，不扫旧图；首次连接、重连通过 Resweep 补回过期地址。
func TestMediaSweepWaitsForConnection(t *testing.T) {
	e := newEnv(t)
	content := png("连上以后才换到")
	e.serve("/old", 403, nil)
	e.serve("/new", 200, content)
	segs := []types.Seg{{Kind: types.SegImage, File: "a.jpg", URL: testSegURL("/old")}}
	seq := e.addEvent(segs, "7001")
	e.start()
	time.Sleep(150 * time.Millisecond)
	if e.nreq("/old") != 0 || e.refresh.count() != 0 {
		t.Fatalf("尚未连接就下载或刷新了旧图：下载 %d 次，刷新 %d 次", e.nreq("/old"), e.refresh.count())
	}
	e.refresh.answer([]types.Seg{{Kind: types.SegImage, File: "a.jpg", URL: testSegURL("/new")}}, nil)
	e.f.Resweep() // 连上了
	waitFor(t, "连上以后的补扫换到新地址", func() bool { return e.segHash(seq, 0) == sha(content) })
	e.idle()
	if r := e.row(seq, 0); r.State != store.MediaDone {
		t.Fatalf("%+v", r)
	}
	if e.refresh.count() != 1 {
		t.Fatalf("连接后该只问一次：%v", e.refresh.calls)
	}
}

// 问协议端的时候补扫来过（连上了）：这个任务当时还占着位置，补扫把它跳过了；问不成放弃时要再请求一遍补扫，不然它没人管
func TestRefreshUnavailableWhileResweepPassed(t *testing.T) {
	e := newEnv(t)
	content := png("补扫错过的")
	e.serve("/old", 403, nil)
	e.serve("/new", 200, content)
	e.refresh.entered = make(chan struct{}, 4)
	e.refresh.gate = make(chan struct{})
	e.refresh.err = fmt.Errorf("%w：没连上", ErrRefreshUnavailable)
	segs := []types.Seg{{Kind: types.SegImage, File: "a.jpg", URL: testSegURL("/old")}}
	seq := e.addEvent(segs, "7001")
	e.start()
	e.f.Resweep()       // 首次连接后开始扫，查询途中又发生重连。
	<-e.refresh.entered // 任务停在"问协议端"，这一问的回答已定：没连上
	e.refresh.answer([]types.Seg{{Kind: types.SegImage, File: "a.jpg", URL: testSegURL("/new")}}, nil)
	e.f.Resweep() // 这时连上了，补扫来过：它挑到这个段，但段还占着位置，跳过
	waitFor(t, "补扫做完一遍", func() bool { return e.f.sweeps.Load() >= 2 })
	close(e.refresh.gate) // 放开：第一问回"没连上"，任务放弃，发现补扫错过了自己，再请求一遍
	waitFor(t, "错过补扫的任务被再补一遍", func() bool { return e.segHash(seq, 0) == sha(content) })
}

// 商城表情的 .gif 下不到（404），换同目录的 raw300.png 再试一次
func TestMarketEmojiFallsBackToPNG(t *testing.T) {
	e := newEnv(t, func(o *envOpt) { o.noRefresh = true })
	content := png("商城")
	e.serve("/club/item/parcel/12/abc/raw300.gif", 404, nil)
	e.serve("/club/item/parcel/12/abc/raw300.png", 200, content)
	segs := []types.Seg{{Kind: types.SegEmoji, URL: "http://gxh.vip.qq.com/club/item/parcel/12/abc/raw300.gif", Ref: "9"}}
	seq := e.addEvent(segs, "1")
	e.start()
	e.f.Offer(seq, segs)
	e.idle()
	if e.segHash(seq, 0) != sha(content) {
		t.Fatalf("没换成 png：%+v", e.row(seq, 0))
	}
	// 别的主机的 raw300.gif 不换
	if _, ok := marketPNG("http://img.example.test/raw300.gif"); ok {
		t.Fatal("只有商城表情的主机才换")
	}
}

// M7：敌对地址。白名单外的主机、重定向到别处、解析到内网、file/ftp/data、带用户名、非默认端口 —— 全部 blocked
func TestHostileURLs(t *testing.T) {
	e := newEnv(t, func(o *envOpt) { o.noRefresh = true })
	e.serve("/ok", 200, png("ok"))
	e.handle("/to-loopback", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:"+portOf(e.srv.URL)+"/ok", http.StatusFound)
	})
	e.handle("/to-other-scheme", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "ftp://"+testHost+"/ok", http.StatusFound)
	})
	e.handle("/loop", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/loop", http.StatusFound)
	})
	e.mu.Lock()
	e.lookup = func(host string) []net.IP {
		switch host {
		case "private.example.test":
			return []net.IP{net.ParseIP("10.0.0.5")}
		case "mixed.example.test":
			return []net.IP{net.ParseIP("8.8.4.4"), net.ParseIP("127.0.0.1")}
		case "linklocal.example.test":
			return []net.IP{net.ParseIP("169.254.1.1")}
		case "v6loop.example.test":
			return []net.IP{net.ParseIP("::1")}
		case "ula.example.test":
			return []net.IP{net.ParseIP("fd00::1")}
		case "cgnat.example.test":
			return []net.IP{net.ParseIP("100.64.0.9")} // CGNAT（Tailscale 等）
		case "nat64.example.test":
			return []net.IP{net.ParseIP("64:ff9b::7f00:1")} // NAT64 里内嵌 127.0.0.1
		case "reserved.example.test":
			return []net.IP{net.ParseIP("240.0.0.5")}
		case "fakeip.example.test":
			return []net.IP{net.ParseIP("198.18.0.7")} // 代理的假地址，要放行
		}
		return []net.IP{net.ParseIP("8.8.8.8")}
	}
	e.mu.Unlock()
	e.f.dl.hosts.Exact = append(e.f.dl.hosts.Exact, "private.example.test", "mixed.example.test", "linklocal.example.test", "v6loop.example.test", "ula.example.test", "cgnat.example.test", "nat64.example.test", "reserved.example.test", "fakeip.example.test")

	blocked := map[string]string{
		"不在白名单的主机":    "http://evil.example.test/ok",
		"回环地址":        "http://127.0.0.1:" + portOf(e.srv.URL) + "/ok",
		"本机名":         "http://localhost/ok",
		"内网地址":        "http://10.1.2.3/ok",
		"解析到私网":       "http://private.example.test/ok",
		"解析里混了回环":     "http://mixed.example.test/ok",
		"解析到链路本地":     "http://linklocal.example.test/ok",
		"解析到 IPv6 回环": "http://v6loop.example.test/ok",
		"解析到 IPv6 私网": "http://ula.example.test/ok",
		"解析到 CGNAT":   "http://cgnat.example.test/ok",
		"解析到 NAT64":   "http://nat64.example.test/ok",
		"解析到保留段":      "http://reserved.example.test/ok",
		"file":        "file:///C:/Windows/win.ini",
		"ftp":         "ftp://" + testHost + "/ok",
		"data":        "data:image/png;base64,AAAA",
		"带用户名":        "http://user:pw@" + testHost + "/ok",
		"非默认端口":       "http://" + testHost + ":8080/ok",
		"后缀冒充":        "http://" + testHost + ".evil.test/ok",
		"主机名带下划线":     "http://bad_host.example.test/ok",
		"跳转到回环":       "http://" + testHost + "/to-loopback",
		"跳转到别的协议":     "http://" + testHost + "/to-other-scheme",
		"跳转太多":        "http://" + testHost + "/loop",
	}
	for name, raw := range blocked {
		t.Run(name, func(t *testing.T) {
			dials := func() int { e.mu.Lock(); defer e.mu.Unlock(); return e.dials }
			beforeDials, beforeReqs := dials(), len(e.requests())
			_, _, err := e.f.dl.Download(context.Background(), raw, func(r io.Reader, max int64) (string, int64, error) {
				t.Fatal("被拦的地址不该走到存文件")
				return "", 0, nil
			})
			de, ok := err.(*Error)
			if !ok || (de.State != store.MediaBlocked && name != "跳转太多") {
				t.Fatalf("要 blocked：%v", err)
			}
			if name == "跳转太多" && (!ok || de.State != store.MediaFailed || de.Retry) {
				t.Fatalf("跳转太多要记 failed 且不重试：%v", err)
			}
			if strings.Contains(err.Error(), "rkey") || strings.Contains(err.Error(), "pw") {
				t.Fatalf("错误文字里带了不该有的内容：%v", err)
			}
			// 第一跳就被拦的：不拨号、不发请求；跳转类的第一跳是合法的，只有它一个请求
			switch name {
			case "跳转到回环", "跳转到别的协议":
				if len(e.requests())-beforeReqs != 1 {
					t.Fatalf("第一跳之后就该停：请求了 %d 次", len(e.requests())-beforeReqs)
				}
			case "跳转太多":
			default:
				if dials()-beforeDials != 0 || len(e.requests())-beforeReqs != 0 {
					t.Fatalf("被拦的地址不该拨号、不该发请求：拨号 %d 次，请求 %d 个", dials()-beforeDials, len(e.requests())-beforeReqs)
				}
			}
		})
	}
	// 198.18.0.0/15（代理的假地址）放行；大小写不敏感
	for _, raw := range []string{"http://fakeip.example.test/ok", "http://" + strings.ToUpper(testHost) + "/ok"} {
		hash, _, err := e.f.dl.Download(context.Background(), raw, e.f.put)
		if err != nil || hash != sha(png("ok")) {
			t.Fatalf("%s 该放行：%v", raw, err)
		}
	}
}

func portOf(raw string) string { return raw[strings.LastIndex(raw, ":")+1:] }

// 白名单：完全匹配或带点的后缀，后缀本身不算
func TestHostRule(t *testing.T) {
	r := DefaultHosts
	for host, want := range map[string]bool{
		"multimedia.nt.qq.com.cn": true, "gchat.qpic.cn": true, "p.qpic.cn": true, "gxh.vip.qq.com": true,
		"a.b.qpic.cn": true, "xx.nt.qq.com.cn": true,
		"qpic.cn": false, "evilqpic.cn": false, "qpic.cn.evil.com": false, "qq.com": false, "": false, "nt.qq.com.cn": false,
		"vip.qq.com": false, "x.gxh.vip.qq.com": false,
		"qq.ugcimg.cn": true, "tianquan.gtimg.cn": true,
		"gzc-download.ftn.qq.com": true, "szc-download.ftn.qq.com": true, "ftn.qq.com": false, "x.ftn.qq.com.evil.com": false, "evilftn.qq.com": false,
		"ugcimg.cn": false, "x.qq.ugcimg.cn": false, "gtimg.cn": false, "x.gtimg.cn": false, "x.tianquan.gtimg.cn": false, "tianquan.gtimg.cn.evil.com": false,
	} {
		if r.allows(host) != want {
			t.Errorf("%q：allows=%v，要 %v", host, !want, want)
		}
	}
	for ip, want := range map[string]bool{
		"127.0.0.1": true, "10.0.0.1": true, "172.16.5.1": true, "192.168.1.1": true, "169.254.0.5": true, "0.0.0.0": true, "0.1.2.3": true,
		"224.0.0.1": true, "255.255.255.255": true, "::1": true, "fe80::1": true, "fc00::1": true, "::ffff:10.0.0.1": true, "::ffff:127.0.0.1": true,
		"100.64.0.1": true, "100.127.255.254": true, "192.0.0.8": true, "240.0.0.1": true, "203.0.113.7": true, "2001:db8::1": true, "64:ff9b::7f00:1": true, "64:ff9b::a00:1": true, "2002:7f00:1::1": true,
		"198.18.0.1": false, "198.19.255.255": false, "::ffff:198.18.0.1": false, "8.8.8.8": false, "172.32.0.1": false, "2606:4700:4700::1111": false,
		"": true,
	} {
		if blockedIP(net.ParseIP(ip)) != want {
			t.Errorf("%s：blocked=%v，要 %v", ip, !want, want)
		}
	}
}

// 位图文件头
func TestSniff(t *testing.T) {
	bmp := make([]byte, 30)
	copy(bmp, "BM")
	bmp[14] = 40
	for name, c := range map[string]struct {
		head []byte
		want bool
	}{
		"png": {png("x"), true}, "jpeg": {[]byte("\xff\xd8\xff\xe0\x00\x10JFIF"), true}, "gif89": {[]byte("GIF89a\x01\x00"), true}, "gif87": {[]byte("GIF87a"), true},
		"webp": {[]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), true}, "bmp": {bmp, true}, "avif": {[]byte("\x00\x00\x00\x1cftypavif\x00\x00\x00\x00"), true},
		"heic": {[]byte("\x00\x00\x00\x18ftypheic\x00\x00\x00\x00"), true},
		"mp4":  {[]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00"), false}, "wav": {[]byte("RIFF\x00\x00\x00\x00WAVEfmt "), false},
		"svg": {[]byte("<svg"), false}, "html": {[]byte("<!DOCTYPE html>"), false}, "zip": {[]byte("PK\x03\x04"), false}, "empty": {nil, false},
		"短的 png 头": {[]byte("\x89PNG"), false},
	} {
		if isBitmap(c.head) != c.want {
			t.Errorf("%s：isBitmap=%v", name, !c.want)
		}
	}
}

// M8（下载器这一层）：不该下的不入队 —— 已有 hash、没有地址、不是图、同一个段重复交、关闭以后
func TestOfferFilters(t *testing.T) {
	e := newEnv(t)
	e.serve("/p", 200, png("p"))
	e.start()
	hashed := imgSeg("/p")
	hashed.Hash = sha(png("p"))
	noURL := types.Seg{Kind: types.SegImage, File: "x.jpg"}
	mface := types.Seg{Kind: types.SegEmoji, Ref: "9"}
	text := types.Seg{Kind: types.SegText, Text: "x", URL: testSegURL("/p")}
	voice := types.Seg{Kind: types.SegVoice, URL: testSegURL("/p")}
	e.f.Offer(1, []types.Seg{hashed, noURL, mface, text, voice})
	e.idle()
	if len(e.requests()) != 0 {
		t.Fatalf("不该请求：%v", e.requests())
	}
	// 同一个段交两次：只下一次（排着或在下的不重复；下完以后才交的是另一回事，库里 hash 有了补扫不会再挑）
	block := make(chan struct{})
	e.handle("/slow", func(w http.ResponseWriter, r *http.Request) {
		<-block
		w.Write(png("slow"))
	})
	segs := []types.Seg{imgSeg("/slow")}
	seq := e.addEvent(segs, "1")
	e.f.Offer(seq, segs)
	waitFor(t, "开始下载", func() bool { return e.nreq("/slow") == 1 })
	e.f.Offer(seq, segs)
	e.f.Offer(seq, segs)
	close(block)
	e.idle()
	if e.nreq("/slow") != 1 {
		t.Fatalf("重复交的请求了 %d 次", e.nreq("/slow"))
	}
	// 关闭以后不收
	e.f.Close()
	e.serve("/late", 200, png("late"))
	late := []types.Seg{imgSeg("/late")}
	lseq := e.addEvent(late, "2")
	e.f.Offer(lseq, late)
	time.Sleep(50 * time.Millisecond)
	if e.nreq("/late") != 0 {
		t.Fatal("关闭以后不该下载")
	}
}

// 队列满了丢掉新的，但不丢任务：队列清空以后补扫自己把丢掉的捡回来
func TestOverflowIsRecoveredBySweep(t *testing.T) {
	e := newEnv(t, func(o *envOpt) { o.workers, o.queueSize = 1, 1 })
	block := make(chan struct{})
	contents := map[string][]byte{}
	var segs []types.Seg
	for _, name := range []string{"/o1", "/o2", "/o3", "/o4", "/o5"} {
		c := png(name)
		contents[name] = c
		e.handle(name, func(w http.ResponseWriter, r *http.Request) {
			<-block
			w.Write(c)
		})
		segs = append(segs, imgSeg(name))
	}
	e.start()
	e.f.Resweep()
	waitFor(t, "起步的补扫做完", func() bool { return e.f.sweeps.Load() >= 1 })
	seq := e.addEvent(segs, "1")
	e.f.Offer(seq, segs) // 一个在下、一个在队里，其余三个被丢
	waitFor(t, "第一个开始下载", func() bool { return len(e.requests()) >= 1 })
	e.f.mu.Lock()
	over := e.f.overflow
	e.f.mu.Unlock()
	if !over {
		t.Fatal("队列满了该记下来")
	}
	close(block)
	waitFor(t, "五张都下完", func() bool {
		for i := range segs {
			if e.segHash(seq, i) == "" {
				return false
			}
		}
		return true
	})
	for i, name := range []string{"/o1", "/o2", "/o3", "/o4", "/o5"} {
		if e.segHash(seq, i) != sha(contents[name]) {
			t.Fatalf("第 %d 张：%q", i, e.segHash(seq, i))
		}
	}
}

// M9：关闭 —— 在途下载被取消、临时文件删掉；下载完了、写库之前关闭，不写库
func TestCloseCancelsAndDoesNotWrite(t *testing.T) {
	t.Run("在途的被取消", func(t *testing.T) {
		e := newEnv(t)
		started := make(chan struct{})
		e.handle("/stuck", func(w http.ResponseWriter, r *http.Request) {
			w.Write(append(png("卡住的前半"), bytes.Repeat([]byte("q"), 600)...))
			w.(http.Flusher).Flush()
			close(started)
			<-r.Context().Done()
		})
		segs := []types.Seg{imgSeg("/stuck")}
		seq := e.addEvent(segs, "1")
		e.start()
		e.f.Offer(seq, segs)
		<-started
		done := make(chan struct{})
		go func() { e.f.Close(); close(done) }()
		select {
		case <-done:
		case <-time.After(envLimit):
			t.Fatal("关闭卡住了")
		}
		if rows, _ := e.st.MediaFetchRows(seq); e.segHash(seq, 0) != "" || len(rows) != 0 {
			t.Fatalf("取消的不该有结果：%+v", rows)
		}
		if files := mediaFiles(t, e); len(files) != 0 {
			t.Fatalf("临时文件该删掉：%v", files)
		}
		e.f.Close() // 可以重复关
	})
	t.Run("下载完到写库之间关闭", func(t *testing.T) {
		hold, released := make(chan struct{}), make(chan struct{})
		e := newEnv(t, func(o *envOpt) {
			o.afterDown = func() {
				close(hold)
				<-released
			}
		})
		e.serve("/p", 200, png("p"))
		segs := []types.Seg{imgSeg("/p")}
		seq := e.addEvent(segs, "1")
		e.start()
		e.f.Offer(seq, segs)
		<-hold
		done := make(chan struct{})
		go func() { e.f.Close(); close(done) }()
		time.Sleep(50 * time.Millisecond) // Close 先把 ctx 取消，再等工人
		close(released)
		<-done
		if e.segHash(seq, 0) != "" {
			t.Fatal("关闭以后不该回填")
		}
		if rows, _ := e.st.MediaFetchRows(seq); len(rows) != 0 {
			t.Fatalf("关闭以后不该写状态：%+v", rows)
		}
	})
}

// M10：崩溃恢复。入队后进程"死"在回填之前 —— 只有 pending 行、或者连行也没有；重启后补扫补上。
// 补扫只看近 7 天；导入来的没有地址的段、已经有 hash 的、终局状态的不碰；failed 的在不太老时再试一次
func TestSweepRecovery(t *testing.T) {
	e := newEnv(t)
	day := int64(24 * time.Hour / time.Millisecond)
	now := e.clk.Now().UnixMilli()
	for _, p := range []string{"/none", "/pending", "/expired", "/done", "/old", "/failed", "/failed-old", "/imported-ok"} {
		e.serve(p, 200, png(p))
	}
	mk := func(path string, at int64, segs ...types.Seg) int64 {
		if len(segs) == 0 {
			segs = []types.Seg{imgSeg(path)}
		}
		return e.addEventAt(segs, "", at)
	}
	none := mk("/none", now-day)
	pending := mk("/pending", now-day)
	expired := mk("/expired", now-day)
	oldSeq := mk("/old", now-8*day)
	failed := mk("/failed", now-2*time.Hour.Milliseconds())
	failedOld := mk("/failed-old", now-2*day)
	// 陌生人的（没有任何角色看到）：补扫也下载
	e.serve("/stranger", 200, png("/stranger"))
	strangerSeq, err := e.st.AppendEvent(store.NewEvent{Channel: "qq", Conversation: testConv, ActorKind: "human", ActorID: "qq:10009", Origin: "peer", Kind: "message", Text: "t",
		Segments: struct {
			Segs []types.Seg `json:"segs"`
		}{[]types.Seg{imgSeg("/stranger")}}, CreatedAt: now - day}, nil)
	if err != nil {
		t.Fatal(err)
	}
	imported := mk("", now-day, types.Seg{Kind: types.SegImage}) // 导入来的：没有地址
	hashedSeg := imgSeg("/done")
	hashedSeg.Hash = sha(png("/done"))
	doneSeq := mk("", now-day, hashedSeg)
	put := func(seq int64, sig, state string, tries int) {
		if err := e.st.PutMediaFetch(store.MediaFetch{EventSeq: seq, Idx: 0, Kind: "image", URLSig: sig, State: state, Tries: tries, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	put(pending, store.MediaURLSig(testSegURL("/pending")), store.MediaPending, 1)
	put(expired, store.MediaURLSig(testSegURL("/expired")), store.MediaExpired, 0)
	put(failed, store.MediaURLSig(testSegURL("/failed")), store.MediaFailed, 3)
	put(failedOld, store.MediaURLSig(testSegURL("/failed-old")), store.MediaFailed, 3)

	e.start()
	e.f.Resweep() // 连接后恢复积压。
	waitFor(t, "补扫挑出的做完", func() bool {
		return e.segHash(none, 0) != "" && e.segHash(pending, 0) != "" && e.segHash(failed, 0) != ""
	})
	e.idle()
	for _, c := range []struct {
		name string
		seq  int64
	}{{"没有行的", none}, {"pending 的", pending}} {
		if e.segHash(c.seq, 0) == "" {
			t.Errorf("%s 该补上", c.name)
		}
	}
	for path, want := range map[string]int{"/none": 1, "/pending": 1, "/expired": 0, "/done": 0, "/old": 0, "/failed": 1, "/failed-old": 0, "/imported-ok": 0, "/stranger": 1} {
		if e.nreq(path) != want {
			t.Errorf("%s 请求了 %d 次，要 %d", path, e.nreq(path), want)
		}
	}
	_ = oldSeq
	_ = imported
	_ = doneSeq
	if e.segHash(strangerSeq, 0) != sha(png("/stranger")) {
		t.Error("陌生人的图补扫也该下载并回填")
	}
	if e.segHash(failed, 0) == "" {
		// 服务器对 /failed 回 200，再试那一次成功了
		t.Error("failed 的再试一次该成功")
	}
	if r := e.row(failed, 0); r.State != store.MediaDone {
		t.Errorf("failed 再试一次：%+v", r)
	}
	// 再补扫一遍：该做的都做完了，一个请求也不该多
	n := len(e.requests())
	e.f.Resweep()
	time.Sleep(100 * time.Millisecond)
	e.idle()
	if len(e.requests()) != n {
		t.Fatalf("第二遍补扫又请求了 %d 次", len(e.requests())-n)
	}
}

// failed 的最多再试一次：再试还是失败，之后补扫不再碰
func TestFailedRetriedOnlyOnce(t *testing.T) {
	e := newEnv(t)
	e.serve("/f", 500, nil)
	segs := []types.Seg{imgSeg("/f")}
	seq := e.addEvent(segs, "1")
	if err := e.st.PutMediaFetch(store.MediaFetch{EventSeq: seq, Idx: 0, Kind: "image", URLSig: store.MediaURLSig(segs[0].URL), State: store.MediaFailed, Tries: 3, UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	e.start()
	e.f.Resweep()
	waitFor(t, "再试一次", func() bool { return e.row(seq, 0).Tries == 4 })
	e.idle()
	if r := e.row(seq, 0); r.State != store.MediaFailed || r.Tries != 4 {
		t.Fatalf("%+v", r)
	}
	e.f.Resweep()
	time.Sleep(100 * time.Millisecond)
	e.idle()
	if e.nreq("/f") != 1 {
		t.Fatalf("请求了 %d 次", e.nreq("/f"))
	}
}

// M12：日志卫生 —— 编的 rkey 不能出现在日志、last_error、media_fetch 的任何一列里
func TestNoSecretsInLogsOrRows(t *testing.T) {
	e := newEnv(t)
	e.serve("/ok", 200, png("ok"))
	e.serve("/gone", 404, nil)
	e.serve("/boom", 500, nil)
	e.serve("/html", 200, []byte("<html>x</html>"))
	e.serve("/forbidden", 403, nil)
	e.refresh.err = io.ErrUnexpectedEOF
	paths := []string{"/ok", "/gone", "/boom", "/html", "/forbidden", "/unknown"}
	var segs []types.Seg
	for _, p := range paths {
		segs = append(segs, imgSeg(p))
	}
	segs = append(segs, types.Seg{Kind: types.SegImage, URL: "http://evil.example.test/x?rkey=" + secret})
	seq := e.addEvent(segs, "7001")
	e.start()
	e.f.Offer(seq, segs)
	// 500 要等重试用完
	for i := 0; i < 2; i++ {
		waitFor(t, "有重试在等", func() bool {
			rows, _ := e.st.MediaFetchRows(seq)
			return e.clk.Pending() >= 1 || rows[2].State == store.MediaFailed
		})
		e.clk.Advance(6 * time.Minute)
	}
	e.idle()
	logs := e.allLogs()
	if !strings.Contains(logs, "收图") {
		t.Fatal("该有日志")
	}
	if strings.Contains(logs, secret) || strings.Contains(logs, "rkey") {
		t.Fatalf("日志里有 rkey：\n%s", logs)
	}
	rows, err := e.st.Events.Query(`SELECT coalesce(last_error, ''), coalesce(hash, ''), url_sig, state FROM media_fetch`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b, c, d string
		rows.Scan(&a, &b, &c, &d)
		if strings.Contains(a+b+c+d, secret) || strings.Contains(a, "rkey") || strings.Contains(a, "http") {
			t.Fatalf("media_fetch 里有地址：%q %q %q %q", a, b, c, d)
		}
	}
}

// M11（下载器这一层）：下载期间事件里的段被改了（地址变了）或已经被别的路径填过 —— 不回填，文件留着，状态记 done
func TestBackfillConditionsNotMet(t *testing.T) {
	for _, c := range []struct {
		name   string
		change string // 下载完成、回填之前对事件做的修改
	}{
		{"地址变了", `UPDATE events SET segments_json = replace(segments_json, '/p?', '/changed?') WHERE seq = ?`},
		{"别的路径填过了", `UPDATE events SET segments_json = replace(segments_json, '"url"', '"hash":"` + strings.Repeat("c", 64) + `","url"') WHERE seq = ?`},
	} {
		t.Run(c.name, func(t *testing.T) {
			var seqp atomic.Int64
			var e *env
			e = newEnv(t, func(o *envOpt) {
				o.afterDown = func() {
					if _, err := e.st.Events.Exec(c.change, seqp.Load()); err != nil {
						t.Error(err)
					}
				}
			})
			content := png("p")
			e.serve("/p", 200, content)
			segs := []types.Seg{imgSeg("/p")}
			seq := e.addEvent(segs, "1")
			seqp.Store(seq)
			e.start()
			e.f.Offer(seq, segs)
			e.idle()
			after := e.body(seq)
			if strings.Contains(after, sha(content)) {
				t.Fatalf("条件不成立不该回填：%s", after)
			}
			if r := e.row(seq, 0); r.State != store.MediaDone || r.Hash != sha(content) {
				t.Fatalf("文件存下了，状态该是 done：%+v", r)
			}
			if files := mediaFiles(t, e); len(files) != 1 {
				t.Fatalf("文件该留着：%v", files)
			}
		})
	}
}
