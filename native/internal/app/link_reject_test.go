package app

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/png"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkplugin"
	"sumika-native/internal/linkreader"
)

// 图发不出去的降级与发送的端到端测试（规格-链接发送改版 A 节；改写了规格-链接解析模板第七节、ADR-333 的阶梯和核实）。
// 假协议端按剧本回包，逐帧记下发了什么；图片字节来自假传输，不连真实网络和 SnowLuma。
// 形状 × 入口表见规格，每格在哪个测试里测了都写在那张表里。

// sentImage 是一帧里的一张图：宽度用来认是哪张，leftBlue 用来认是不是转过 180° 的（测试图左红右蓝）
type sentImage struct {
	Width    int
	LeftBlue bool
	GIF      bool
	MD5      string // 图片字节的 md5（小写十六进制）：假协议端据此在历史里写“图的标识”
}

// sentFrame 是协议端收到的一帧发送：direct 是 send_*_msg，forward 是合并转发
type sentFrame struct {
	Action  string
	Forward bool
	Images  []sentImage
	Texts   []string
}

func decodeImage(t *testing.T, file string) sentImage {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(file, "base64://"))
	if err != nil {
		t.Fatal(err)
	}
	sum := md5.Sum(raw)
	if bytes.HasPrefix(raw, []byte("GIF8")) {
		g, err := gif.DecodeAll(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		return sentImage{Width: g.Config.Width, GIF: true, MD5: hex.EncodeToString(sum[:])}
	}
	img, err := png.Decode(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	_, _, b, _ := img.At(0, 0).RGBA()
	return sentImage{Width: img.Bounds().Dx(), LeftBlue: b > 0x8000, MD5: hex.EncodeToString(sum[:])}
}

func parseSent(t *testing.T, raw string) sentFrame {
	t.Helper()
	var o struct {
		Action string `json:"action"`
		Params struct {
			Message  []segment `json:"message"`
			Messages []struct {
				Data struct {
					Content []segment `json:"content"`
				} `json:"data"`
			} `json:"messages"`
		} `json:"params"`
	}
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatal(err)
	}
	f := sentFrame{Action: o.Action, Forward: strings.Contains(o.Action, "forward")}
	add := func(segs []segment) {
		for _, s := range segs {
			switch s.Type {
			case "text":
				f.Texts = append(f.Texts, s.Data.Text)
			case "image":
				f.Images = append(f.Images, decodeImage(t, s.Data.File))
			}
		}
	}
	add(o.Params.Message)
	for _, m := range o.Params.Messages {
		add(m.Data.Content)
	}
	return f
}

type segment struct {
	Type string `json:"type"`
	Data struct {
		Text string `json:"text"`
		File string `json:"file"`
	} `json:"data"`
}

// 剧本里每一帧的回法
const (
	replyOK          = "ok"          // 确认，给消息号
	replyReject      = "reject"      // failed，措辞是内容审核之类的拒绝
	replyCapability  = "capability"  // failed，说这个场合发不了这种段
	replyUnconfirmed = "unconfirmed" // failed，SnowLuma 的 delivery was not confirmed（result=0、sequence 为空）
	replySilent      = "silent"      // 不回包
	replyTransient   = "transient"   // failed，没有可识别的拒绝措辞（NT 忙、临时 send failed）：不是内容拒绝
	// 回包和 unconfirmed / silent 一样，但消息其实进群了（历史里查得到）
	replyUnconfirmedLanded = "unconfirmed-landed"
	replySilentLanded      = "silent-landed"
)

const unconfirmedWording = "group message delivery was not confirmed: QQ returned no message receipt; check the conversation before retrying"

// rejectPeer 是假协议端：rule 按帧决定怎么回（nil 表示全确认），frames 是收到的发送帧原文
type rejectPeer struct {
	mu     sync.Mutex
	frames []string
	t      *testing.T
	rule   func(sentFrame) string
	// 下面是历史查询（核实“没拿到回执”那一条有没有进群，ADR-333）的剧本
	env        *testEnv      // 源消息的平台消息号在 env.linkSource：历史里要有它（锚点）
	now        time.Time     // 历史里自己号的消息比它晚一秒
	landed     []landedFrame // 进了群的帧（确认的、回执没到但其实到了的）
	histCalls  int
	histSilent bool // 历史查询不回包
	histFail   bool // 历史查询回 failed
	histOpaque bool // 历史里图的标识是读不出 md5 的（NT 的 fileId）
	histNoSrc  bool // 历史里没有源消息（锚点不被认）
	// 下面是回声（SnowLuma 的 message_sent，规格-链接发送改版 A1）和撤回（delete_msg，A4）的剧本
	echoes     bool     // 进了群的帧回一个回声
	echoNoPics bool     // 回声里只剩文字（图被吞）
	echoOpaque bool     // 回声里图的标识读不出 md5
	recalls    []string // 收到的 delete_msg 的 message_id
	recallFail bool     // delete_msg 回失败
}

// landedFrame 是真的进群的一帧
type landedFrame struct {
	id      int
	forward bool
	images  []string // md5
	texts   []string
	private bool
}

func (p *rejectPeer) handle(c *websocket.Conn, data []byte) {
	var frame struct{ Action, Echo string }
	if json.Unmarshal(data, &frame) != nil {
		return
	}
	if strings.HasSuffix(frame.Action, "_msg_history") {
		p.answerHistory(c, frame.Action, frame.Echo, data)
		return
	}
	if frame.Action == "delete_msg" {
		p.answerRecall(c, frame.Echo, data)
		return
	}
	if !isSendAction(string(data)) {
		answerInfo(c, data)
		return
	}
	verdict := replyOK
	p.mu.Lock()
	p.frames = append(p.frames, string(data))
	p.mu.Unlock()
	if frame.Action != "set_msg_emoji_like" && p.rule != nil {
		verdict = p.rule(parseSent(p.t, string(data)))
	}
	okID := -97000 - len(p.snapshot())
	ok := fmt.Sprintf(`{"echo":%q,"status":"ok","retcode":0,"data":{"message_id":%d}}`, frame.Echo, okID)
	var echo *landedFrame
	if frame.Action != "set_msg_emoji_like" && (verdict == replyOK || verdict == replyUnconfirmedLanded || verdict == replySilentLanded) {
		sf := parseSent(p.t, string(data))
		lf := landedFrame{id: 8000 + len(p.landedSnapshot()), forward: sf.Forward, texts: sf.Texts, private: strings.Contains(sf.Action, "private")}
		if verdict == replyOK {
			lf.id = okID // 回包确认的，回声的号和回包里的一样
		}
		echo = &lf
		for _, im := range sf.Images {
			lf.images = append(lf.images, im.MD5)
		}
		p.mu.Lock()
		p.landed = append(p.landed, lf)
		p.mu.Unlock()
	}
	switch verdict {
	case replyUnconfirmedLanded:
		verdict = replyUnconfirmed
	case replySilentLanded:
		verdict = replySilent
	}
	var body string
	switch {
	case frame.Action == "set_msg_emoji_like":
		body = fmt.Sprintf(`{"echo":%q,"status":"ok","retcode":0,"data":null}`, frame.Echo)
	case verdict == replyOK:
		body = ok
	case verdict == replySilent:
		if echo != nil {
			p.pushEcho(c, *echo) // 不回包，但消息到了：回声照样推
		}
		return
	default:
		wording := map[string]string{replyReject: "send group message rejected: content blocked by audit", replyTransient: "send failed", replyCapability: `message element "image" cannot be sent to this contact`, replyUnconfirmed: unconfirmedWording}[verdict]
		body = fmt.Sprintf(`{"echo":%q,"status":"failed","retcode":100,"data":null,"wording":%q}`, frame.Echo, wording)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e := c.Write(ctx, websocket.MessageText, []byte(body)); e != nil {
		panic(e)
	}
	if echo != nil {
		p.pushEcho(c, *echo)
	}
}

// pushEcho 推一个自己号发的消息的回声（SnowLuma 的 message_sent），在回包之后
func (p *rejectPeer) pushEcho(c *websocket.Conn, lf landedFrame) {
	p.mu.Lock()
	on, noPics, opaque := p.echoes, p.echoNoPics, p.echoOpaque
	p.mu.Unlock()
	if !on {
		return
	}
	seg := func(typ, k, v string) string { return fmt.Sprintf(`{"type":%q,"data":{%q:%q}}`, typ, k, v) }
	var segs []string
	if lf.forward {
		segs = append(segs, seg("forward", "id", fmt.Sprintf("fw-echo-%d", lf.id)))
	} else {
		for _, tx := range lf.texts {
			segs = append(segs, seg("text", "text", tx))
		}
		if !noPics {
			for j, h := range lf.images {
				file := h + ".png"
				if opaque {
					file = fmt.Sprintf("opaque-echo-%d-%d", lf.id, j)
				}
				segs = append(segs, seg("image", "file", file))
			}
		}
	}
	var b string
	if lf.private {
		b = fmt.Sprintf(`{"post_type":"message_sent","message_type":"private","time":%d,"self_id":%s,"user_id":%s,"target_id":%s,"message_id":%d,"message":[%s]}`, p.now.Unix()+1, testAccount, testAccount, testOwner, lf.id, strings.Join(segs, ","))
	} else {
		b = fmt.Sprintf(`{"post_type":"message_sent","message_type":"group","time":%d,"self_id":%s,"user_id":%s,"group_id":%s,"message_id":%d,"sender":{"nickname":"编的她"},"message":[%s]}`, p.now.Unix()+1, testAccount, testAccount, testGroup, lf.id, strings.Join(segs, ","))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e := c.Write(ctx, websocket.MessageText, []byte(b)); e != nil {
		panic(e)
	}
}

// answerRecall 回撤回：记下要撤的号，按剧本回成功或失败
func (p *rejectPeer) answerRecall(c *websocket.Conn, echo string, data []byte) {
	var frame struct {
		Params struct {
			MessageID json.RawMessage `json:"message_id"`
		} `json:"params"`
	}
	_ = json.Unmarshal(data, &frame)
	p.mu.Lock()
	p.recalls = append(p.recalls, string(frame.Params.MessageID))
	fail := p.recallFail
	p.mu.Unlock()
	body := fmt.Sprintf(`{"echo":%q,"status":"ok","retcode":0,"data":null}`, echo)
	if fail {
		body = fmt.Sprintf(`{"echo":%q,"status":"failed","retcode":100,"data":null,"wording":"message not found or not retractable"}`, echo)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e := c.Write(ctx, websocket.MessageText, []byte(body)); e != nil {
		panic(e)
	}
}

func (p *rejectPeer) recallSnapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.recalls...)
}

func (p *rejectPeer) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.frames...)
}

// sent 返回收到的发送帧（解析过），按到达顺序
func (p *rejectPeer) sent() []sentFrame {
	var out []sentFrame
	for _, raw := range p.snapshot() {
		if f := parseSent(p.t, raw); f.Action != "set_msg_emoji_like" {
			out = append(out, f)
		}
	}
	return out
}

// imageFrames 只要带图的帧
func (p *rejectPeer) imageFrames() []sentFrame {
	var out []sentFrame
	for _, f := range p.sent() {
		if len(f.Images) > 0 {
			out = append(out, f)
		}
	}
	return out
}

// text 把所有帧里的文字拼起来，用来找文字版
func (p *rejectPeer) text() string {
	var all []string
	for _, f := range p.sent() {
		all = append(all, f.Texts...)
	}
	return strings.Join(all, "\n")
}

// settle 等一会儿，确认没有多余的帧再冒出来（负载下也够）
func (p *rejectPeer) settle(t *testing.T) []sentFrame {
	t.Helper()
	time.Sleep(600 * time.Millisecond)
	return p.sent()
}

// 编的图：宽 w、高 1，左红右蓝中间绿；转 180° 以后左边是蓝的
func markPicture(t *testing.T, w int) []byte {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, w, 1))
	for x := range w {
		img.Set(x, 0, color.NRGBA{G: 255, A: 255})
	}
	img.Set(0, 0, color.NRGBA{R: 255, A: 255})
	img.Set(w-1, 0, color.NRGBA{B: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func markGIF(t *testing.T, w int) []byte {
	t.Helper()
	img := image.NewPaletted(image.Rect(0, 0, w, 1), []color.Color{color.NRGBA{R: 255, A: 255}, color.NRGBA{B: 255, A: 255}})
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{img}, Delay: []int{1}}); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// picsFetcher 回一条带若干张图的 X 短帖；图的网址里带宽度（-w3.png），假传输据此造图。.gif 结尾的造动图
type picsFetcher struct {
	names  []string
	byID   map[string][]string // 某条帖子单独的图
	noText bool                // 帖子没有正文，只有图
}

func pics(names ...string) picsFetcher { return picsFetcher{names: names} }

func (f picsFetcher) Fetch(_ context.Context, l linkreader.Link) (linkreader.Result, error) {
	avail := func(s string) linkreader.Field[string] { return linkreader.Field[string]{State: "available", Value: s} }
	r := linkreader.Result{Platform: "x", Kind: "post", ID: l.ID, CleanURL: l.Clean, Source: "fake", FetchedAt: time.Now().UTC(),
		Stats: map[string]linkreader.Field[int64]{}, Flags: map[string]linkreader.Field[any]{}}
	// 作者账号跟链接里的账号一致（真实推文就是这样）；发出去的链接按取到的账号写，账号不一致会被改写
	account := "fake_author"
	if rest, ok := strings.CutPrefix(l.Clean, "https://x.com/"); ok {
		if a, _, ok := strings.Cut(rest, "/"); ok && a != "i" {
			account = a
		}
	}
	r.Author.Name, r.Author.Account = avail("编的作者"), avail(account)
	r.Published = linkreader.Field[time.Time]{State: "available", Value: time.Unix(1, 0)}
	r.Text = avail("编的推文正文")
	if f.noText {
		r.Text = avail("")
	}
	r.Article = linkreader.Field[string]{State: "not_returned", Reason: "not_applicable"}
	var media []linkreader.Media
	names := f.names
	if own, ok := f.byID[l.ID]; ok {
		names = own
	}
	for _, n := range names {
		media = append(media, linkreader.Media{Type: "image", URL: avail("https://pbs.twimg.com/media/fiction-" + l.ID + "-" + n)})
	}
	r.Media = linkreader.Field[[]linkreader.Media]{State: "available", Value: media}
	return r, nil
}
func (picsFetcher) Resolve(_ context.Context, l linkreader.Link) (linkreader.Link, error) {
	return l, nil
}

// onePic 回一条带一张图（宽 2）的 X 短帖
func onePic() picsFetcher { return pics("w2.png") }

// pictureFetcher 是旧写法的取数器（图片网址不带宽度，假传输按宽 2 造图），别的测试文件在用
type pictureFetcher struct{}

func (pictureFetcher) Fetch(ctx context.Context, l linkreader.Link) (linkreader.Result, error) {
	r, err := onePic().Fetch(ctx, l)
	r.Media.Value[0].URL.Value = "https://pbs.twimg.com/media/fiction-" + l.ID + ".png"
	return r, err
}
func (pictureFetcher) Resolve(_ context.Context, l linkreader.Link) (linkreader.Link, error) {
	return l, nil
}

type fakeTransport func(*http.Request) (*http.Response, error)

func (f fakeTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

var picName = regexp.MustCompile(`-w(\d+)\.(png|gif)$`)

// newScriptHost 起一台展开主机，开着转 180°；图片下载走假传输
func newScriptHost(t *testing.T, fetcher linkreader.ContentFetcher, rule func(sentFrame) string, extra ...string) (*testEnv, *App, *rejectPeer) {
	t.Helper()
	return newScriptHostRotate(t, fetcher, true, rule, extra...)
}

// newRejectHost 是旧写法：前 failures 帧带图的发送回 failed（wording 是协议端的报错文字），之后都确认
func newRejectHost(t *testing.T, failures int, wording string, rotate bool) (*testEnv, *App, *rejectPeer) {
	t.Helper()
	return newRejectHostFetch(t, failures, wording, rotate, pictureFetcher{})
}

// newRejectHostWith 同 newRejectHost（不拒绝、开旋转），取数用给定的假取数器
func newRejectHostWith(t *testing.T, f linkreader.ContentFetcher) (*testEnv, *App, *rejectPeer) {
	t.Helper()
	return newRejectHostFetch(t, 0, "", true, f)
}
func newRejectHostFetch(t *testing.T, failures int, wording string, rotate bool, fetcher linkreader.ContentFetcher) (*testEnv, *App, *rejectPeer) {
	t.Helper()
	var mu sync.Mutex
	n := 0
	return newScriptHostRotate(t, fetcher, rotate, func(f sentFrame) string {
		if len(f.Images) == 0 {
			return replyOK
		}
		mu.Lock()
		defer mu.Unlock()
		n++
		switch {
		case n > failures:
			return replyOK
		case strings.Contains(wording, "cannot be sent"):
			return replyCapability
		}
		return replyReject
	})
}

func newScriptHostRotate(t *testing.T, fetcher linkreader.ContentFetcher, rotate bool, rule func(sentFrame) string, extra ...string) (*testEnv, *App, *rejectPeer) {
	t.Helper()
	e, a, _, _ := newExpandHost(t, "off", fetcher, append([]string{fmt.Sprintf("    rotateOnReject: %v\n", rotate)}, extra...)...)
	peer := &rejectPeer{t: t, rule: rule, now: e.now, env: e}
	e.peer.onAction = peer.handle
	f, err := linkreader.NewFetcher(linkreader.FetchOptions{Transport: fakeTransport(func(r *http.Request) (*http.Response, error) {
		w, ext := 2, "png"
		if m := picName.FindStringSubmatch(r.URL.Path); m != nil {
			w, _ = strconv.Atoi(m[1])
			ext = m[2]
		}
		body, mime := markPicture(t, w), "image/png"
		if ext == "gif" {
			body, mime = markGIF(t, w), "image/gif"
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{mime}}, Body: io.NopCloser(bytes.NewReader(body))}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	a.linkMedia = linkexpand.NewMedia(f, a.paths.Root)
	return e, a, peer
}

// 入口：QQ 群、QQ 私聊（本地私聊、本地群聊不进展开，见规格表）
const (
	entryGroup   = "QQ 群"
	entryPrivate = "QQ 私聊"
)

// sendLink 是旧写法：群里发一条链接，返回连接（后面还要发别的帧用）
func sendLink(t *testing.T, e *testEnv, a *App, id, url string) *websocket.Conn {
	t.Helper()
	conn := startSend(t, e, a)
	e.peer.send(t, conn, expandFrame(e, id, testOwner, url))
	return conn
}

func scriptLink(t *testing.T, e *testEnv, a *App, conn *websocket.Conn, entry, id, url string) {
	t.Helper()
	e.mu.Lock()
	e.linkSource = id
	e.mu.Unlock()
	if entry == entryPrivate {
		e.peer.send(t, conn, privateFrame(e.now, testAccount, id, testOwner, url))
		return
	}
	e.peer.send(t, conn, expandFrame(e, id, testOwner, url))
}

func eachEntry(t *testing.T, f func(t *testing.T, entry string)) {
	for _, entry := range []string{entryGroup, entryPrivate} {
		t.Run(entry, func(t *testing.T) {
			t.Parallel() // 每个入口自己建夹具，互不相干
			f(t, entry)
		})
	}
}

// 带图的发送帧里，宽度为 w 的图
func hasWidth(f sentFrame, w int) bool {
	for _, im := range f.Images {
		if im.Width == w {
			return true
		}
	}
	return false
}

// rowCount 查运行页读的 task_runs：module=link、class、outcome 的行数
func rowCount(t *testing.T, a *App, class, outcome string) int {
	t.Helper()
	var n int
	if err := a.set.TraceStore().Trace.QueryRow(`SELECT count(*) FROM task_runs WHERE module='link' AND class=? AND outcome=?`, class, outcome).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitRows(t *testing.T, a *App, class, outcome string, want int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("task_runs link · %s（%s）有 %d 行", class, outcome, want), func() bool { return rowCount(t, a, class, outcome) == want })
}

func waitImages(t *testing.T, p *rejectPeer, n int) {
	t.Helper()
	waitFor(t, fmt.Sprintf("协议端收到 %d 个带图的帧", n), func() bool { return len(p.imageFrames()) >= n })
}

func waitTextHas(t *testing.T, p *rejectPeer, want string) {
	t.Helper()
	waitFor(t, "协议端收到文字："+want, func() bool { return strings.Contains(p.text(), want) })
}

// ---- 降级阶梯（规格-链接发送改版 A2：原样 → 整段转 180° 重发一次 → 文字版） ----

// 一张图被明确拒收：直接发并转 180° 再发一次，发出去了就停，不走合并转发、不退文字、不再重发（每种形式只提交一次）
func TestRejectedImageGoesToRotatedDirectOnce(t *testing.T) {
	t.Parallel()
	eachEntry(t, func(t *testing.T, entry string) {
		e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
			if len(f.Images) > 0 && !f.Images[0].LeftBlue {
				return replyReject
			}
			return replyOK
		})
		scriptLink(t, e, a, startSend(t, e, a), entry, "901", "https://x.com/paper/status/71001")
		waitImages(t, peer, 2)
		frames := peer.settle(t)
		images := peer.imageFrames()
		if len(images) != 2 || images[0].Forward || images[1].Forward {
			t.Fatalf("应当先直接发一次被拒、再直接发转过的一次，不走合并转发：%+v", frames)
		}
		if images[0].Images[0].LeftBlue || !images[1].Images[0].LeftBlue || images[1].Images[0].Width != 2 {
			t.Fatal("第一次是原图，第二次是转 180° 的同一张", images)
		}
		if strings.Contains(peer.text(), "没发出去") {
			t.Fatal("转图发出去了，不该再补带原因的文字版")
		}
		if peer.historyCount() != 0 {
			t.Fatal("明确拒收不翻历史")
		}
		waitRows(t, a, "form_degrade", "ok", 1)
		waitRows(t, a, "rotate_retry", "ok", 1)
	})
}

// 原图和转图都被拒：一条文字版，写明原因和试过的形式，附原链接；之后同一张图再来，不再往 QQ 提交
func TestAllFormsRejectedFallsBackToTextThenRemembersImage(t *testing.T) {
	t.Parallel()
	eachEntry(t, func(t *testing.T, entry string) {
		e, a, peer := newScriptHost(t, picsFetcher{names: []string{"w2.png"}, byID: map[string][]string{"71005": {"w3.png"}}}, func(f sentFrame) string {
			if len(f.Images) > 0 {
				return replyReject
			}
			return replyOK
		})
		conn := startSend(t, e, a)
		scriptLink(t, e, a, conn, entry, "903", "https://x.com/paper/status/71003")
		waitTextHas(t, peer, "QQ 拒收了这张图")
		peer.settle(t)
		images := peer.imageFrames()
		if len(images) != 2 || images[1].Forward {
			t.Fatalf("原图、转图直接发各提交一次，不走合并转发：%+v", images)
		}
		text := peer.text()
		if !strings.Contains(text, "图没发出去：QQ 拒收了这张图（直接发、转 180° 直接发都被拒）") || !strings.Contains(text, "https://x.com/paper/status/71003") {
			t.Fatal("文字版缺原因或链接", text)
		}
		waitRows(t, a, "rotate_retry", "failed", 1)

		// 同一张图（同一场合、同样的字节）再来：已知两种都被拒，不再提交，直接文字
		before := len(peer.imageFrames())
		scriptLink(t, e, a, conn, entry, "904", "https://x.com/paper/status/71003")
		waitFor(t, "第二条也有文字版", func() bool { return strings.Count(peer.text(), "QQ 拒收了这张图") == 2 })
		peer.settle(t)
		if got := len(peer.imageFrames()); got != before {
			t.Fatalf("同一张图再来不该再提交：%d → %d", before, got)
		}
		// 别的图不受影响：照常直接发
		scriptLink(t, e, a, conn, entry, "905", "https://x.com/paper/status/71005")
		waitFor(t, "新图照常提交", func() bool { return len(peer.imageFrames()) > before })
	})
}

// 两个模板：一条消息里图文混排；先一条带图的、再一条纯文字的（后一条用来看被连累中止的段有没有原样补发）
const (
	twoMessagesTemplate = "    templates:\n      x: \"$正文\\n$分段\\n后一条 $链接\"\n"
	longPostForward     = "    sites:\n      x: {forward: always}\n"
	historyCheckOn      = "    send:\n      historyCheck: true\n"
)

// 多张图一条消息被拒：整条转图重发一次，仍是一条消息、两张都转了、正文原样；不拆开、不猜是哪一张（A3）
func TestMultiImageDirectRotatesAllTogetherOnWire(t *testing.T) {
	t.Parallel()
	eachEntry(t, func(t *testing.T, entry string) {
		e, a, peer := newScriptHost(t, pics("w2.png", "w3.png"), func(f sentFrame) string {
			if hasWidth(f, 3) && !f.Images[0].LeftBlue {
				return replyReject
			}
			return replyOK
		})
		scriptLink(t, e, a, startSend(t, e, a), entry, "911", "https://x.com/paper/status/72011")
		waitImages(t, peer, 2)
		peer.settle(t)
		images := peer.imageFrames()
		if len(images) != 2 || images[1].Forward || len(images[1].Images) != 2 || !images[1].Images[0].LeftBlue || !images[1].Images[1].LeftBlue {
			t.Fatalf("整条转图重发一次，两张都转：%+v", images)
		}
		if n := strings.Count(peer.text(), "编的推文正文"); n != 2 {
			t.Fatalf("正文在被拒的那条和转图重发的那条里各一次：%d", n)
		}
		if strings.Contains(peer.text(), "没发出去") {
			t.Fatal("转图发出去了，不该有文字版")
		}
	})
}

// 合并转发一张卡被拒：整个转发里的静态图全部转 180° 重发一次，仍是一个合并转发
func TestForwardRejectedRotatesWholeForward(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, pics("w2.png", "w3.png"), func(f sentFrame) string {
		if len(f.Images) > 0 && f.Forward && !f.Images[0].LeftBlue {
			return replyReject
		}
		return replyOK
	}, longPostForward)
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "912", "https://x.com/paper/status/72012")
	waitImages(t, peer, 2)
	peer.settle(t)
	images := peer.imageFrames()
	if len(images) != 2 || !images[1].Forward || len(images[1].Images) != 2 || !images[1].Images[0].LeftBlue || !images[1].Images[1].LeftBlue {
		t.Fatalf("整个转发转图重发一次：%+v", images)
	}
	if strings.Contains(peer.text(), "没发出去") {
		t.Fatal("转 180° 成功了，不该有文字版")
	}
}

// ---- 没确认送达：等回声（A1）。2026-11-09 实测：被卡的图是“没拿到回执”且没进群，转 180° 直接发进群了 ----

// 真实复现（回归用例）：原图回 unconfirmed 且没进群（没有回声）；等不到回声就按没送到，转 180° 直接发一次成功。
// 默认不翻历史；不重复发、不走合并转发、不退文字；换形式的被拒不暂停 QQ 渠道
func TestUnconfirmedAndNotLandedGoesToRotated(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 && !f.Images[0].LeftBlue {
			return replyUnconfirmed
		}
		return replyOK
	})
	peer.echoes = true
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "913", "https://x.com/paper/status/72013")
	waitImages(t, peer, 2)
	waitRows(t, a, "rotate_retry", "ok", 1)
	frames := peer.settle(t)
	images := peer.imageFrames()
	if len(images) != 2 || images[0].Forward || images[1].Forward || images[0].Images[0].LeftBlue || !images[1].Images[0].LeftBlue {
		t.Fatalf("应当原图直接发一次（没回声）、转 180° 直接发一次（成功），别的都不发：%+v", frames)
	}
	if peer.historyCount() != 0 {
		t.Fatal("historyCheck 默认关，不该翻历史")
	}
	if strings.Contains(peer.text(), "没发出去") {
		t.Fatal("转图成功了，不该补文字版")
	}
	waitRows(t, a, "form_degrade", "failed", 2) // 回声：没回声、第一次发的那一行
	if why, paused := a.gate.Paused("qq"); paused {
		t.Fatalf("没回声按“换形式的被拒”算，不该暂停 QQ 渠道：%s", why)
	}
}

// 没确认送达但其实到了（回声来了，带着图）：按成功算，不补发、不换形式、不退文字、不翻历史
func TestUnconfirmedButLandedIsSuccessNotResent(t *testing.T) {
	t.Parallel()
	for name, reply := range map[string]string{"无回执": replyUnconfirmedLanded, "不回包": replySilentLanded} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
				if len(f.Images) > 0 {
					return reply
				}
				return replyOK
			})
			peer.echoes = true
			scriptLink(t, e, a, startSend(t, e, a), entryGroup, "914", "https://x.com/paper/status/72014")
			waitImages(t, peer, 1)
			frames := peer.settle(t)
			if got := len(peer.imageFrames()); got != 1 {
				t.Fatalf("回声到了就不补发、不换形式：%d 帧 %+v", got, frames)
			}
			if strings.Contains(peer.text(), "没发出去") || peer.historyCount() != 0 {
				t.Fatal("到了不该补文字版，也不翻历史")
			}
			st, _ := a.set.For("qq")
			var n int
			if err := st.Events.QueryRow(`SELECT count(*) FROM events WHERE origin = 'human'`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("带图的回声被认领，不该存成她号上别人发的：%d %v", n, err)
			}
		})
	}
}

// 回声里图的标识读不出（NT 的 fileId）：凭图数和文字也认得出到了，不重复发
func TestUnconfirmedLandedWithOpaqueImageID(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 {
			return replyUnconfirmedLanded
		}
		return replyOK
	})
	peer.echoes, peer.echoOpaque = true, true
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "924", "https://x.com/paper/status/72024")
	waitImages(t, peer, 1)
	peer.settle(t)
	if got := len(peer.imageFrames()); got != 1 {
		t.Fatalf("凭形状对上了就是到了：%d", got)
	}
}

// 开了 historyCheck：没回声以后翻一次历史，到了就不补发（运行页记“核实：到了”）
func TestHistoryCheckFindsLanded(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 {
			return replyUnconfirmedLanded
		}
		return replyOK
	}, historyCheckOn)
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "934", "https://x.com/paper/status/72034")
	waitImages(t, peer, 1)
	waitRows(t, a, "form_degrade", "ok", 1)
	peer.settle(t)
	if got := len(peer.imageFrames()); got != 1 || peer.historyCount() != 1 {
		t.Fatalf("核实到了不补发、只翻一次：%d 帧、翻了 %d 次", got, peer.historyCount())
	}
}

// 开了 historyCheck 也查不了（历史不回包、回 failed、锚点不被认）：仍是结果不明，不换形式、不补发、不退文字
func TestUnconfirmedAndHistoryUnavailableStaysUnknown(t *testing.T) {
	t.Parallel()
	for name, set := range map[string]func(*rejectPeer){
		"历史不回包": func(p *rejectPeer) { p.histSilent = true },
		"历史回失败": func(p *rejectPeer) { p.histFail = true },
		"锚点不被认": func(p *rejectPeer) { p.histNoSrc = true },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
				if len(f.Images) > 0 {
					return replyUnconfirmed
				}
				return replyOK
			}, historyCheckOn)
			set(peer)
			scriptLink(t, e, a, startSend(t, e, a), entryGroup, "925", "https://x.com/paper/status/72025")
			waitImages(t, peer, 1)
			waitRows(t, a, "form_degrade", "degraded", 2) // 核实：查不了、直接发：结果不明
			frames := peer.settle(t)
			if got := len(peer.imageFrames()); got != 1 {
				t.Fatalf("查不了只该提交一次，不换形式不补发：%d 帧 %+v", got, frames)
			}
			if strings.Contains(peer.text(), "没发出去") {
				t.Fatal("查不了不该补文字版（图可能已经到了）")
			}
		})
	}
}

// 转 180° 那一步也没拿到回执：同样等回声，到了就停
func TestRotatedStepUnconfirmedButLanded(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		switch {
		case len(f.Images) == 0:
			return replyOK
		case !f.Images[0].LeftBlue:
			return replyReject
		}
		return replyUnconfirmedLanded
	})
	peer.echoes = true
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "926", "https://x.com/paper/status/72026")
	waitImages(t, peer, 2)
	waitRows(t, a, "rotate_retry", "ok", 1)
	peer.settle(t)
	images := peer.imageFrames()
	if len(images) != 2 || images[1].Forward || !images[1].Images[0].LeftBlue {
		t.Fatalf("原图被拒、转图无回执但回声到了：只该两帧：%+v", images)
	}
	if strings.Contains(peer.text(), "没发出去") {
		t.Fatal("不该有文字版")
	}
}

// 按图记结论：只记“没回声 / 核实没到”和明确拒绝，不记查不了。
// 没回声：同一张图再来，直接用上次成功的转图（原图不再提交）；查不了：下次仍从原图直接发起
func TestVerdictsRememberAbsentButNotUnknown(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 && !f.Images[0].LeftBlue {
			return replyUnconfirmed
		}
		return replyOK
	})
	conn := startSend(t, e, a)
	scriptLink(t, e, a, conn, entryGroup, "927", "https://x.com/paper/status/72027")
	waitImages(t, peer, 2)
	peer.settle(t)
	before := len(peer.imageFrames())
	scriptLink(t, e, a, conn, entryGroup, "928", "https://x.com/paper/status/72027")
	waitFor(t, "第二条也发出图", func() bool { return len(peer.imageFrames()) > before })
	peer.settle(t)
	images := peer.imageFrames()
	if len(images) != before+1 || !images[before].Images[0].LeftBlue || images[before].Forward {
		t.Fatalf("同一张图上次没回声、转图成功：这次该直接用转图，不再提交原图：%+v", images[before:])
	}

	// 查不了：不记结论，下一条还是从原图起
	e2, a2, peer2 := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 {
			return replyUnconfirmed
		}
		return replyOK
	}, historyCheckOn)
	peer2.histSilent = true
	conn2 := startSend(t, e2, a2)
	scriptLink(t, e2, a2, conn2, entryGroup, "929", "https://x.com/paper/status/72028")
	waitImages(t, peer2, 1)
	waitRows(t, a2, "form_degrade", "degraded", 2) // 核实：查不了、直接发：结果不明
	peer2.settle(t)
	scriptLink(t, e2, a2, conn2, entryGroup, "930", "https://x.com/paper/status/72028")
	waitImages(t, peer2, 2)
	peer2.settle(t)
	for _, f := range peer2.imageFrames() {
		if f.Forward || f.Images[0].LeftBlue {
			t.Fatalf("查不了没有结论可记，两次都该是原图直接发：%+v", peer2.imageFrames())
		}
	}
}

// QQ 私聊没有“未确认送达”的回包（SnowLuma 的 sendPrivate 没有这条分支），但不回包仍可能：同样等回声。
// 回声到了：不补发、不翻历史；没回声：直接发转 180° 成功，不重复发
func TestPrivateSilentLandedAndAbsent(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 {
			return replySilentLanded
		}
		return replyOK
	})
	peer.echoes = true
	scriptLink(t, e, a, startSend(t, e, a), entryPrivate, "932", "https://x.com/paper/status/72032")
	waitImages(t, peer, 1)
	peer.settle(t)
	if got := len(peer.imageFrames()); got != 1 || peer.historyCount() != 0 {
		t.Fatalf("私聊不回包但回声到了：不补发不换形式、不翻历史：%d 帧，翻了 %d 次", got, peer.historyCount())
	}

	e2, a2, peer2 := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 && !f.Images[0].LeftBlue {
			return replySilent
		}
		return replyOK
	})
	peer2.echoes = true
	scriptLink(t, e2, a2, startSend(t, e2, a2), entryPrivate, "933", "https://x.com/paper/status/72033")
	waitImages(t, peer2, 2)
	waitRows(t, a2, "rotate_retry", "ok", 1)
	peer2.settle(t)
	images := peer2.imageFrames()
	if len(images) != 2 || images[0].Forward || images[1].Forward || images[0].Images[0].LeftBlue || !images[1].Images[0].LeftBlue {
		t.Fatalf("私聊不回包且没回声：原图一次、转图直发一次：%+v", images)
	}
}

// 协议端不回包而且（开着 historyCheck）历史也查不了：结果不明，只发一次
func TestSilentPeerWithHistoryUnavailableIsUnknown(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 {
			return replySilent
		}
		return replyOK
	}, historyCheckOn)
	peer.histSilent = true
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "931", "https://x.com/paper/status/72031")
	waitImages(t, peer, 1)
	waitRows(t, a, "form_degrade", "degraded", 2) // 核实：查不了、直接发：结果不明
	peer.settle(t)
	if got := len(peer.imageFrames()); got != 1 {
		t.Fatalf("没回包又查不了，只该提交一次：%d", got)
	}
	if strings.Contains(peer.text(), "没发出去") {
		t.Fatal("没回包不该补文字版")
	}
}

// 前一段结果不明或到了，后一段（从没发出过）照常补发；前一段被拒后转图成功，后一段同样补发，不跟着变形
func TestFollowingPartsAreSentAsTheyAreAfterTrouble(t *testing.T) {
	t.Parallel()
	for name, reply := range map[string]string{"无回执但到了": replyUnconfirmedLanded, "无回执查不了": replyUnconfirmed, "被拒后转图成功": replyReject} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			extra := []string{twoMessagesTemplate}
			if name == "无回执查不了" {
				extra = append(extra, historyCheckOn)
			}
			e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
				if len(f.Images) > 0 && !f.Images[0].LeftBlue {
					return reply
				}
				return replyOK
			}, extra...)
			peer.echoes = name == "无回执但到了"
			peer.histSilent = name == "无回执查不了"
			scriptLink(t, e, a, startSend(t, e, a), entryGroup, "915", "https://x.com/paper/status/72015")
			waitTextHas(t, peer, "后一条")
			peer.settle(t)
			if n := strings.Count(peer.text(), "后一条"); n != 1 {
				t.Fatalf("后一条只该发一次：%d", n)
			}
			if strings.Contains(peer.text(), "没发出去") {
				t.Fatal("不该有文字版")
			}
			if reply != replyReject {
				if got := len(peer.imageFrames()); got != 1 {
					t.Fatalf("到了或查不了，那张图只提交一次：%d", got)
				}
			}
		})
	}
}

// ---- 图被吞（A4）：回声只剩文字 → 撤回那一条 → 转图后图文重发；撤不了只补发转好的图 ----

// 吞图：撤回的号就是回声的号，撤回成功后转 180° 图文一起重发
func TestSwallowedImageRecallsAndResendsOnWire(t *testing.T) {
	t.Parallel()
	eachEntry(t, func(t *testing.T, entry string) {
		e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
			if len(f.Images) > 0 && !f.Images[0].LeftBlue {
				return replyUnconfirmedLanded
			}
			return replyOK
		})
		peer.echoes, peer.echoNoPics = true, true
		scriptLink(t, e, a, startSend(t, e, a), entry, "951", "https://x.com/paper/status/75001")
		waitImages(t, peer, 2)
		peer.settle(t)
		st, _ := a.set.For("qq")
		var planID, echoID string
		var idx int
		if err := st.Events.QueryRow(`SELECT plan_id, idx, platform_id FROM outbound_parts WHERE via = 'echo' AND kind = 'message' AND text LIKE '%"media"%'`).Scan(&planID, &idx, &echoID); err != nil {
			t.Fatal(err)
		}
		recalls := peer.recallSnapshot()
		if len(recalls) != 1 || recalls[0] != echoID {
			t.Fatalf("撤回的应是只剩文字那条回声的号 %s：%v", echoID, recalls)
		}
		images := peer.imageFrames()
		if len(images) != 2 || images[1].Forward || !images[1].Images[0].LeftBlue || len(images[1].Texts) == 0 {
			t.Fatalf("撤回后转图、图文一起重发：%+v", images)
		}

		// 宿主兜底（绕开插件直接调）：号、段、会话、时限任一不对都不调适配器
		conv := linkConv(entry)
		ctx := context.Background()
		for name, call := range map[string]func() error{
			"别的号":  func() error { return a.recallLink(ctx, conv, planID, idx, "-123") },
			"号为空":  func() error { return a.recallLink(ctx, conv, planID, idx, "") },
			"号为 0": func() error { return a.recallLink(ctx, conv, planID, idx, "0") },
			"别的段":  func() error { return a.recallLink(ctx, conv, planID, idx+1, echoID) },
			"别的计划": func() error { return a.recallLink(ctx, conv, "不存在的计划", idx, echoID) },
			"别的会话": func() error { return a.recallLink(ctx, otherConv(entry), planID, idx, echoID) },
			"本地会话": func() error {
				return a.recallLink(ctx, types.Conversation{Channel: "local", Scene: types.SceneGroup, ID: "g1"}, planID, idx, echoID)
			},
		} {
			if err := call(); err == nil || !strings.Contains(err.Error(), "撤回门槛不成立") {
				t.Errorf("%s：宿主应拒绝撤回：%v", name, err)
			}
		}
		// 不是回声认领的（同一段改成回包确认）：不撤
		if _, err := st.Events.Exec(`UPDATE outbound_parts SET via = 'reply' WHERE plan_id = ? AND idx = ?`, planID, idx); err != nil {
			t.Fatal(err)
		}
		if err := a.recallLink(ctx, conv, planID, idx, echoID); err == nil || !strings.Contains(err.Error(), "不是这一段刚认领的回声") {
			t.Errorf("回包确认的段宿主应拒绝：%v", err)
		}
		if _, err := st.Events.Exec(`UPDATE outbound_parts SET via = 'echo' WHERE plan_id = ? AND idx = ?`, planID, idx); err != nil {
			t.Fatal(err)
		}
		// 来源不是链接展开（同一段改成别的来源的计划）：不撤
		if _, err := st.Events.Exec(`UPDATE outbound_plans SET origin = 'character' WHERE id = ?`, planID); err != nil {
			t.Fatal(err)
		}
		if err := a.recallLink(ctx, conv, planID, idx, echoID); err == nil || !strings.Contains(err.Error(), "不是链接展开发的") {
			t.Errorf("别的来源发的段宿主应拒绝：%v", err)
		}
		if _, err := st.Events.Exec(`UPDATE outbound_plans SET origin = ? WHERE id = ?`, linkOrigin, planID); err != nil {
			t.Fatal(err)
		}
		// 发出后这个会话没了发送许可（关了展开；qq.goSend、QQ 通道暂停走同一个 outletMode）：不撤
		was := a.currentLinks()
		off := &LinkRuntime{Values: was.Values.Clone(), Version: was.Version}
		off.Values.Expand.Enabled = false
		a.linkRuntime.Store(off)
		if err := a.recallLink(ctx, conv, planID, idx, echoID); err == nil || !strings.Contains(err.Error(), "没有发送许可") {
			t.Errorf("没有发送许可宿主应拒绝：%v", err)
		}
		a.linkRuntime.Store(was)
		e.nowMu.Lock()
		e.now = e.now.Add(linkplugin.RecallWithin + time.Second)
		e.nowMu.Unlock()
		if err := a.recallLink(ctx, conv, planID, idx, echoID); err == nil || !strings.Contains(err.Error(), "超过 2 分钟") {
			t.Errorf("超过 2 分钟宿主应拒绝：%v", err)
		}
		if got := peer.recallSnapshot(); len(got) != 1 {
			t.Fatalf("门槛不成立不该发 delete_msg：%v", got)
		}
	})
}

// 撤回失败：不再补发图文，只补发转好的图（一条只有图的消息，不带文字），群里不会出现两份文字
func TestRecallFailedSendsImagesOnlyOnWire(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 && !f.Images[0].LeftBlue {
			return replyUnconfirmedLanded
		}
		return replyOK
	})
	peer.echoes, peer.echoNoPics, peer.recallFail = true, true, true
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "952", "https://x.com/paper/status/75002")
	waitImages(t, peer, 2)
	peer.settle(t)
	images := peer.imageFrames()
	if len(peer.recallSnapshot()) != 1 || len(images) != 2 || !images[1].Images[0].LeftBlue || len(images[1].Texts) != 0 {
		t.Fatalf("撤回失败只补发转好的图：撤 %v，%+v", peer.recallSnapshot(), images)
	}
}

func linkConv(entry string) types.Conversation {
	if entry == entryPrivate {
		return types.Conversation{Channel: "qq", Scene: types.ScenePrivate, ID: testOwner, Self: testAccount}
	}
	return types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: testGroup}
}

func otherConv(entry string) types.Conversation {
	if entry == entryPrivate {
		return types.Conversation{Channel: "qq", Scene: types.ScenePrivate, ID: "299", Self: testAccount}
	}
	return types.Conversation{Channel: "qq", Scene: types.SceneGroup, ID: "399"}
}

// 本地私聊、本地群聊不进展开，也撤不了（形状 × 入口表的两列“不可能”）
func TestLocalConversationsNeverExpandOrRecall(t *testing.T) {
	t.Parallel()
	_, a, _ := newScriptHost(t, onePic(), nil)
	for _, conv := range []types.Conversation{{Channel: "local", Scene: types.ScenePrivate, ID: "u1", Self: "me"}, {Channel: "local", Scene: types.SceneGroup, ID: "g1"}} {
		s := channel.Stored{Seq: 1, Character: a.character, Inbound: types.Inbound{Kind: types.InboundMessage, Conv: conv, Account: a.account, PlatformID: "123"}, Actor: channel.Actor{Kind: channel.ActorOwner, ID: testOwner}, Origin: channel.OriginOwner}
		if a.admitLink(s) {
			t.Fatalf("本地会话不能触发展开：%s", conv)
		}
		if why := a.recallRefusal(conv, "p", 0, "123"); why != "不是 QQ 会话" {
			t.Fatalf("本地会话不能撤回：%s", conv)
		}
	}
}

// 能力缺失，但合并转发能发：直接发被说“发不了”，转发发出去了，不退文字，也不记整个场合降级（下一条链接照样先试直接发）
func TestCapabilityMissingDirectButForwardWorks(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 && !f.Forward {
			return replyCapability
		}
		return replyOK
	})
	conn := startSend(t, e, a)
	scriptLink(t, e, a, conn, entryGroup, "916", "https://x.com/paper/status/72016")
	waitFor(t, "转发发出去", func() bool {
		for _, f := range peer.sent() {
			if f.Forward && len(f.Images) > 0 {
				return true
			}
		}
		return false
	})
	peer.settle(t)
	if strings.Contains(peer.text(), "发不了图") || strings.Contains(peer.text(), "没发出去") {
		t.Fatal("转发发出去了，不该有文字版")
	}
	if rowCount(t, a, "rotate_retry", "ok")+rowCount(t, a, "rotate_retry", "failed") != 0 {
		t.Fatal("能力缺失转 180° 帮不上，不该走到那一级")
	}
	// 下一条（图不同）：不降级，仍先试直接发
	before := len(peer.imageFrames())
	scriptLink(t, e, a, conn, entryGroup, "917", "https://x.com/paper/status/72017")
	waitFor(t, "下一条仍先试直接发", func() bool {
		fs := peer.imageFrames()
		return len(fs) > before && !fs[before].Forward
	})
	if strings.Contains(peer.text(), "图片未下载") {
		t.Fatal("合并转发能发时不该把整个场合的图降成文字")
	}
}

// 能力缺失且转发也不行：只发直接和转发各一次（不转 180°，转了也没用），文字版写“这个群暂时发不了图”，
// 文字版确认后记场合降级，下一条链接的图直接退成文字，不再往 QQ 提交
func TestCapabilityMissingEverywhereDegradesScene(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 {
			return replyCapability
		}
		return replyOK
	})
	conn := startSend(t, e, a)
	scriptLink(t, e, a, conn, entryGroup, "918", "https://x.com/paper/status/72018")
	waitTextHas(t, peer, "这个群暂时发不了图")
	peer.settle(t)
	if images := peer.imageFrames(); len(images) != 2 || images[0].Forward || !images[1].Forward {
		t.Fatalf("应当直接发、合并转发各一次：%+v", images)
	}
	scriptLink(t, e, a, conn, entryGroup, "919", "https://x.com/paper/status/72019")
	waitTextHas(t, peer, "图片未下载")
	peer.settle(t)
	if got := len(peer.imageFrames()); got != 2 {
		t.Fatalf("降级后不该再提交图：%d", got)
	}
}

// 动图被拒：不转 180°（动图不转），也不换合并转发，直接文字版
func TestAnimatedImageIsNeverRotated(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, pics("w3.gif"), func(f sentFrame) string {
		if len(f.Images) > 0 {
			return replyReject
		}
		return replyOK
	})
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "920", "https://x.com/paper/status/72020")
	waitTextHas(t, peer, "QQ 拒收了这张动图（直接发都被拒）")
	peer.settle(t)
	images := peer.imageFrames()
	if len(images) != 1 || !images[0].Images[0].GIF {
		t.Fatalf("动图原样只发一次：%+v", images)
	}
	if rowCount(t, a, "rotate_retry", "ok")+rowCount(t, a, "rotate_retry", "failed") != 0 {
		t.Fatal("动图不该走转 180°")
	}
}

// 关着旋转：被拒后换不转的合并转发，再被拒就退文字
func TestRotateOffStopsAfterForward(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHostRotate(t, onePic(), false, func(f sentFrame) string {
		if len(f.Images) > 0 {
			return replyReject
		}
		return replyOK
	})
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "921", "https://x.com/paper/status/72021")
	waitTextHas(t, peer, "QQ 拒收了这张图（直接发、合并转发都被拒）")
	peer.settle(t)
	if got := len(peer.imageFrames()); got != 2 {
		t.Fatalf("关着旋转只该提交两次：%d", got)
	}
}

// ---- 网站的发图方式（platforms.<网站>.imageMode）：rotate 起步就转图直接发，forward 起步就包成合并转发并转图 ----

const (
	modeRotate  = "    platforms:\n      x: {imageMode: rotate}\n"
	modeForward = "    platforms:\n      x: {imageMode: forward}\n"
)

// rotate：第一帧就是直接发的转 180° 图；成功就停，原图一帧都不发
func TestImageModeRotateStartsAtRotatedDirect(t *testing.T) {
	t.Parallel()
	eachEntry(t, func(t *testing.T, entry string) {
		e, a, peer := newScriptHost(t, onePic(), func(sentFrame) string { return replyOK }, modeRotate)
		scriptLink(t, e, a, startSend(t, e, a), entry, "941", "https://x.com/paper/status/74001")
		waitImages(t, peer, 1)
		peer.settle(t)
		images := peer.imageFrames()
		if len(images) != 1 || images[0].Forward || !images[0].Images[0].LeftBlue {
			t.Fatalf("rotate 应当第一帧就是转过的直接发，只此一帧：%+v", images)
		}
	})
}

// forward：第一帧就是合并转发里转 180° 的图
func TestImageModeForwardStartsAtRotatedForward(t *testing.T) {
	t.Parallel()
	eachEntry(t, func(t *testing.T, entry string) {
		e, a, peer := newScriptHost(t, onePic(), func(sentFrame) string { return replyOK }, modeForward)
		scriptLink(t, e, a, startSend(t, e, a), entry, "942", "https://x.com/paper/status/74002")
		waitImages(t, peer, 1)
		peer.settle(t)
		images := peer.imageFrames()
		if len(images) != 1 || !images[0].Forward || !images[0].Images[0].LeftBlue {
			t.Fatalf("forward 应当第一帧就是合并转发里转过的图，只此一帧：%+v", images)
		}
	})
}

// rotate 起步的那一级被拒：直接退文字（阶梯只有一级换形式）
func TestImageModeRotateRejectedFallsBackToText(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, onePic(), func(f sentFrame) string {
		if len(f.Images) > 0 {
			return replyReject
		}
		return replyOK
	}, modeRotate)
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "943", "https://x.com/paper/status/74003")
	waitTextHas(t, peer, "没发出去")
	peer.settle(t)
	images := peer.imageFrames()
	if len(images) != 1 || images[0].Forward || !images[0].Images[0].LeftBlue {
		t.Fatalf("rotate 被拒后不再换形式：%+v", images)
	}
}

// 动图不转：即使网站选了 rotate，动图仍照常直接发（原样）
func TestImageModeLeavesAnimatedImagesAlone(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, pics("w3.gif"), func(sentFrame) string { return replyOK }, modeRotate)
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "944", "https://x.com/paper/status/74004")
	waitImages(t, peer, 1)
	peer.settle(t)
	images := peer.imageFrames()
	if len(images) != 1 || images[0].Forward || !images[0].Images[0].GIF {
		t.Fatalf("动图不转，照常直接发：%+v", images)
	}
}

// ---- 进度表情：开始 → 撤开始 → 完成 / 失败（规格-链接解析模板第六节） ----

type frameInfo struct {
	Action string `json:"action"`
	Params struct {
		MessageID json.RawMessage `json:"message_id"`
		EmojiID   string          `json:"emoji_id"`
		Set       *bool           `json:"set"`
	} `json:"params"`
}

// reactions 取出表情回应帧，写成 “贴 76”“撤 76” 的序列；sends 是别的发送帧的个数
func reactionTrail(frames []string) (trail []string, sends int) {
	for _, f := range frames {
		var fi frameInfo
		if json.Unmarshal([]byte(f), &fi) != nil {
			continue
		}
		if fi.Action != "set_msg_emoji_like" {
			sends++
			continue
		}
		verb := "贴"
		if fi.Params.Set != nil && !*fi.Params.Set {
			verb = "撤"
		}
		trail = append(trail, verb+" "+fi.Params.EmojiID)
	}
	return trail, sends
}

func waitTrail(t *testing.T, wire *expandPeer, want ...string) {
	t.Helper()
	waitFor(t, "表情序列 "+strings.Join(want, "、"), func() bool {
		trail, _ := reactionTrail(wire.snapshot())
		return strings.Join(trail, "、") == strings.Join(want, "、")
	})
}

// 成功：贴开始表情 → 结果发出（这条假帖不是短帖，整条进合并转发；规格-链接发送改版 B4 起转发前不再另发导语，只有一条）→ 撤开始表情 → 贴完成表情，
// 顺序固定，撤的帧带 set:false
func TestProgressEmojiOnSuccess(t *testing.T) {
	t.Parallel()
	e, a, wire, _ := newExpandHost(t, "off", nil, "    doneEmoji: \"124\"\n    failEmoji: \"9\"\n")
	conn := startSend(t, e, a)
	e.peer.send(t, conn, expandFrame(e, "951", testOwner, "https://x.com/paper/status/72001"))
	waitTrail(t, wire, "贴 76", "撤 76", "贴 124")
	frames := wire.snapshot()
	var order []string
	for _, f := range frames {
		var fi frameInfo
		_ = json.Unmarshal([]byte(f), &fi)
		order = append(order, fi.Action)
	}
	if strings.Join(order, ",") != "set_msg_emoji_like,send_group_forward_msg,set_msg_emoji_like,set_msg_emoji_like" {
		t.Fatal("发送顺序不对：开始表情、结果、撤、完成", order)
	}
	if !strings.Contains(frames[2], `"set":false`) || strings.Contains(frames[3], `"set"`) {
		t.Fatal("撤的帧要带 set:false，贴的帧不带", frames[2], frames[3])
	}
}

// 失败：取数失败不发任何文字，开始表情换成失败表情
func TestProgressEmojiOnFailure(t *testing.T) {
	t.Parallel()
	e, a, wire, _ := newExpandHost(t, "off", failedExpandFetcher{})
	conn := startSend(t, e, a)
	e.peer.send(t, conn, expandFrame(e, "952", testOwner, "https://x.com/paper/status/72002"))
	waitTrail(t, wire, "贴 76", "撤 76", "贴 9")
	if _, sends := reactionTrail(wire.snapshot()); sends != 0 {
		t.Fatal("失败时不该发文字", sends)
	}
}

// 表情回应本身失败（协议端拒绝）只记日志：内容照发，撤不掉也照样换结果表情
func TestProgressEmojiFailureDoesNotBlockContent(t *testing.T) {
	t.Parallel()
	e, a, _, _ := newExpandHost(t, "off", nil)
	peer := &rejectPeer{t: t}
	e.peer.onAction = func(c *websocket.Conn, data []byte) {
		var fi frameInfo
		_ = json.Unmarshal(data, &fi)
		if fi.Action == "set_msg_emoji_like" && fi.Params.Set != nil && !*fi.Params.Set {
			var f struct{ Echo string }
			_ = json.Unmarshal(data, &f)
			peer.mu.Lock()
			peer.frames = append(peer.frames, string(data))
			peer.mu.Unlock()
			_ = c.Write(context.Background(), websocket.MessageText, []byte(fmt.Sprintf(`{"echo":%q,"status":"failed","retcode":100,"data":null,"wording":"message not found"}`, f.Echo)))
			return
		}
		peer.handle(c, data)
	}
	conn := startSend(t, e, a)
	e.peer.send(t, conn, expandFrame(e, "953", testOwner, "https://x.com/paper/status/72003"))
	waitFor(t, "撤失败后仍贴完成表情", func() bool {
		// 撤失败会被出口重试几次，所以不比整串：只要求内容发了、最后一个表情是完成表情。
		// 这条假帖整条进合并转发，B4（规格-链接发送改版）起不另发导语，内容是一条
		trail, sends := reactionTrail(peer.snapshot())
		return sends == 1 && len(trail) >= 3 && trail[0] == "贴 76" && trail[len(trail)-1] == "贴 124"
	})
}

// 发送规则不齐（qq.goSend 没开）：一个表情、一条消息都不发
func TestProgressEmojiSilentWhenSendNotAllowed(t *testing.T) {
	t.Parallel()
	e, a, wire, _ := newExpandHostSend(t, "off", nil, false)
	conn := startSend(t, e, a)
	e.peer.send(t, conn, expandFrame(e, "954", testOwner, "https://x.com/paper/status/72004"))
	time.Sleep(1500 * time.Millisecond)
	if got := wire.snapshot(); len(got) != 0 {
		t.Fatal("发送规则不齐时不该有任何发送帧", got)
	}
}

// 私聊不调表情接口：开始用文字状态，完成靠结果本身
func TestProgressEmojiNeverInPrivate(t *testing.T) {
	t.Parallel()
	e, a, wire, _ := newExpandHost(t, "off", nil)
	conn := startSend(t, e, a)
	e.peer.send(t, conn, fmt.Sprintf(`{"post_type":"message","message_type":"private","sub_type":"friend","time":%d,"self_id":%s,"user_id":%s,"message_id":"955","sender":{"nickname":"主人"},"message":[{"type":"text","data":{"text":"https://x.com/paper/status/72005"}}]}`, e.now.Unix(), testAccount, testOwner))
	waitFor(t, "私聊结果发出", func() bool { _, sends := reactionTrail(wire.snapshot()); return sends >= 2 })
	time.Sleep(500 * time.Millisecond)
	if trail, _ := reactionTrail(wire.snapshot()); len(trail) != 0 {
		t.Fatal("私聊不该调表情接口", trail)
	}
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// 纯图卡片直接发、合并转发、转 180° 三级都被拒：每一级只是内容被拒，不是通道坏了，不能数进通道门的连续失败；
// 否则第三级一失败整个 QQ 就被暂停，文字版也发不出去（旧代码一段失败只算一次，没有这个问题）
func TestLadderRejectionsDoNotPauseQQGate(t *testing.T) {
	t.Parallel()
	eachEntry(t, func(t *testing.T, entry string) {
		e, a, peer := newScriptHost(t, picsFetcher{names: []string{"w2.png"}, noText: true}, func(f sentFrame) string {
			if len(f.Images) > 0 {
				return replyReject
			}
			return replyOK
		}, "    templates:\n      x: \"$正文\"\n")
		scriptLink(t, e, a, startSend(t, e, a), entry, "921", "https://x.com/paper/status/73001")
		waitTextHas(t, peer, "https://x.com/paper/status/73001")
		peer.settle(t)
		if n := len(peer.imageFrames()); n != 2 {
			t.Fatalf("原图、转图各提交一次：%d", n)
		}
		if why, paused := a.gate.Paused("qq"); paused {
			t.Fatalf("换形式被拒不该暂停 QQ 渠道：%s", why)
		}
	})
}

// 两张图都被拒：第一张的转 180° 和第二张的直接发接着失败，也不能把 QQ 暂停
func TestTwoRejectedImagesDoNotPauseQQGate(t *testing.T) {
	t.Parallel()
	e, a, peer := newScriptHost(t, picsFetcher{names: []string{"w2.png", "w3.png"}, noText: true}, func(f sentFrame) string {
		if len(f.Images) > 0 {
			return replyReject
		}
		return replyOK
	}, "    templates:\n      x: \"$正文\"\n")
	scriptLink(t, e, a, startSend(t, e, a), entryGroup, "922", "https://x.com/paper/status/73002")
	waitTextHas(t, peer, "https://x.com/paper/status/73002")
	peer.settle(t)
	if why, paused := a.gate.Paused("qq"); paused {
		t.Fatalf("两张图被拒不该暂停 QQ 渠道：%s", why)
	}
}

// failed 回包没有可识别的拒绝措辞（NT 忙、临时 send failed）不是内容拒绝：出口照旧重试，不换形式，不记这张图被拒；
// 重试成功后图就是直接发出去的
func TestTransientFailureIsRetriedAndNotRemembered(t *testing.T) {
	t.Parallel()
	var mu sync.Mutex
	n := 0
	e, a, peer := newScriptHost(t, pics("w2.png"), func(f sentFrame) string {
		if len(f.Images) == 0 {
			return replyOK
		}
		mu.Lock()
		defer mu.Unlock()
		n++
		if n == 1 {
			return replyTransient
		}
		return replyOK
	})
	conn := startSend(t, e, a)
	scriptLink(t, e, a, conn, entryGroup, "923", "https://x.com/paper/status/73003")
	waitImages(t, peer, 2)
	peer.settle(t)
	for _, f := range peer.imageFrames() {
		if f.Forward {
			t.Fatalf("瞬时失败不该换成合并转发：%+v", f)
		}
	}
	if n := len(peer.imageFrames()); n != 2 {
		t.Fatalf("第一次瞬时失败，重试一次就成：%d 个带图的帧", n)
	}
}

func (p *rejectPeer) landedSnapshot() []landedFrame {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]landedFrame(nil), p.landed...)
}

func (p *rejectPeer) historyCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.histCalls
}

// answerHistory 回历史查询：源消息（主人发的链接）加上所有进了群的自己号的帧，按时间从旧到新
func (p *rejectPeer) answerHistory(c *websocket.Conn, action, echo string, data []byte) {
	p.mu.Lock()
	p.histCalls++
	silent, fail, opaque, noSrc := p.histSilent, p.histFail, p.histOpaque, p.histNoSrc
	p.mu.Unlock()
	if silent {
		return
	}
	var body string
	if fail {
		body = fmt.Sprintf(`{"echo":%q,"status":"failed","retcode":100,"data":null,"wording":"history unavailable"}`, echo)
	} else {
		private := action == "get_friend_msg_history"
		seg := func(typ, k, v string) string { return fmt.Sprintf(`{"type":%q,"data":{%q:%q}}`, typ, k, v) }
		msg := func(id int, from string, sec int64, segs ...string) string {
			if private {
				target := testAccount
				if from == testAccount {
					target = testOwner
				}
				return fmt.Sprintf(`{"message_type":"private","message_id":%d,"message_seq":1,"user_id":%s,"target_id":%s,"time":%d,"message":[%s]}`, id, from, target, sec, strings.Join(segs, ","))
			}
			return fmt.Sprintf(`{"message_type":"group","group_id":%s,"message_id":%d,"message_seq":%d,"user_id":%s,"time":%d,"sender":{"nickname":"编的人"},"message":[%s]}`, testGroup, id, id, from, sec, strings.Join(segs, ","))
		}
		var msgs []string
		if !noSrc {
			p.env.mu.Lock()
			src, _ := strconv.Atoi(p.env.linkSource)
			p.env.mu.Unlock()
			msgs = append(msgs, msg(src, testOwner, p.now.Unix(), seg("text", "text", "编的链接消息")))
		}
		for i, lf := range p.landedSnapshot() {
			if lf.private != private {
				continue
			}
			var segs []string
			if lf.forward {
				segs = append(segs, seg("forward", "id", fmt.Sprintf("fw%d", i)))
			} else {
				for _, tx := range lf.texts {
					segs = append(segs, seg("text", "text", tx))
				}
				for j, h := range lf.images {
					file := h + ".jpg"
					if opaque {
						file = fmt.Sprintf("opaque-%d-%d", i, j)
					}
					segs = append(segs, seg("image", "file", file))
				}
			}
			msgs = append(msgs, msg(lf.id, testAccount, p.now.Unix()+1, segs...))
		}
		body = fmt.Sprintf(`{"echo":%q,"status":"ok","retcode":0,"data":{"messages":[%s]}}`, echo, strings.Join(msgs, ","))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e := c.Write(ctx, websocket.MessageText, []byte(body)); e != nil {
		panic(e)
	}
}
