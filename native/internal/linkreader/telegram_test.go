package linkreader

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// 编造的 embed 页，结构照公开频道帖子的样子：作者、头像、图、视频、正文（含换行和链接）、浏览数、时间。
const tgPhotoPage = `<html><body><div class="tgme_widget_message_wrap js-widget_message_wrap"><div class="tgme_widget_message text_not_supported_wrap js-widget_message" data-post="PaperChannel/300" data-view="abc">
<div class="tgme_widget_message_user"><a href="https://t.me/paperchannel"><i class="tgme_widget_message_user_photo bgcolor3"><img src="https://cdn4.cdn-telegram.org/file/avatar.jpg"></i></a></div>
<div class="tgme_widget_message_bubble">
<div class="tgme_widget_message_author accent_color"><a class="tgme_widget_message_owner_name" href="https://t.me/paperchannel"><span dir="auto">纸云频道</span></a></div>
<a class="tgme_widget_message_photo_wrap grouped_media_wrap blured js-message_photo" href="https://t.me/paperchannel/300" style="width:453px;background-image:url('https://cdn4.cdn-telegram.org/file/p1.jpg')"><div class="tgme_widget_message_photo" style="padding-top:100%"></div></a>
<a class="tgme_widget_message_photo_wrap grouped_media_wrap blured js-message_photo" href="https://t.me/paperchannel/301" style="width:453px;background-image:url('https://cdn4.cdn-telegram.org/file/p2.jpg')"></a>
<div class="tgme_widget_message_text js-message_text" dir="auto">第一行<br/>第二行 <a href="https://example.com/very/long/path?a=1" target="_blank" rel="noopener">example.com/very/long/pa…</a> 和 <a href="https://example.org/x">点这里</a> <a href="?q=%23tag">#tag</a></div>
<div class="tgme_widget_message_footer compact js-message_footer"><div class="tgme_widget_message_info short js-message_info"><span class="tgme_widget_message_views">1.2K</span><span class="copyonly"> views</span><span class="tgme_widget_message_meta"><a class="tgme_widget_message_date" href="https://t.me/paperchannel/300"><time datetime="2026-11-01T10:30:00+00:00" class="time">10:30</time></a></span></div></div>
</div></div></div></body></html>`

const tgVideoPage = `<html><body><div class="tgme_widget_message_wrap"><div class="tgme_widget_message" data-post="telegram/300">
<div class="tgme_widget_message_bubble">
<div class="tgme_widget_message_author"><a class="tgme_widget_message_owner_name" href="https://t.me/telegram"><span dir="auto">纸云频道</span></a></div>
<a class="tgme_widget_message_video_player js-message_video_player" href="https://t.me/telegram/300"><i class="tgme_widget_message_video_thumb" style="background-image:url('https://cdn1.telesco.pe/file/thumb.jpg')"></i><video src="https://cdn1.telesco.pe/file/v.mp4" class="tgme_widget_message_video js-message_video"></video><time class="message_video_duration js-message_video_duration">1:05</time></a>
<div class="tgme_widget_message_footer"><span class="tgme_widget_message_views">845</span><span class="tgme_widget_message_meta"><a class="tgme_widget_message_date"><time datetime="2026-11-02T00:00:00+00:00" class="time">00:00</time></a></span></div>
</div></div></div></body></html>`

// 网页版不显示的频道：页面 200，只有一句提示（编造的最小页面）。
const tgHiddenPage = `<html><body><div class="tgme_page_description">This channel can’t be displayed because it was used to spread pornographic content.</div></body></html>`

func tgFetch(t *testing.T, raw, body string, status int) (Result, []string, error) {
	t.Helper()
	var urls []string
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		urls = append(urls, req.URL.String())
		if req.Header.Get("Cookie") != "" || req.Header.Get("Authorization") != "" {
			t.Fatal("telegram request carries credentials")
		}
		return response(status, body), nil
	})
	r, err := f.Fetch(context.Background(), link(t, raw))
	return r, urls, err
}

func TestRecognizeTelegram(t *testing.T) {
	ok := map[string]string{
		"https://t.me/PaperChannel/300":                      "https://t.me/paperchannel/300",
		"https://t.me/s/paperchannel/300?single":             "https://t.me/paperchannel/300",
		"https://t.me/paperchannel/300?embed=1&mode=tme#x":   "https://t.me/paperchannel/300",
		"http://telegram.me/paperchannel/300?comment=7&t=12": "https://t.me/paperchannel/300",
		"https://t.me/c/1234567890/42":                       "https://t.me/c/1234567890/42",
	}
	for raw, want := range ok {
		l, found := Recognize(raw)
		if !found || l.Platform != "telegram" || l.Kind != "post" || l.Clean != want {
			t.Fatalf("%s: %+v %v", raw, l, found)
		}
	}
	for _, raw := range []string{
		"https://t.me/paperchannel", "https://t.me/s/paperchannel", "https://t.me/share/url?url=x", "https://t.me/joinchat/abcdef",
		"https://t.me/addstickers/foo", "https://t.me/paperchannel/0", "https://t.me/paperchannel/abc", "https://t.me/ab/3",
		"https://t.me/c/abc/3", "https://t.me/paperchannel/300/2", "https://t.me/paper%63hannel/300", "https://user@t.me/paperchannel/300",
		"https://evil.example/t.me/paperchannel/300",
	} {
		if l, found := Recognize(raw); found && l.Platform == "telegram" {
			t.Fatalf("%s 不该被认成 Telegram 帖子: %+v", raw, l)
		}
	}
}

func TestFetchTelegramPhotosAndText(t *testing.T) {
	r, urls, err := tgFetch(t, "https://t.me/s/PaperChannel/300?single", tgPhotoPage, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(urls) != 1 || urls[0] != "https://t.me/paperchannel/300?embed=1&mode=tme" {
		t.Fatalf("请求地址: %v", urls)
	}
	if r.Platform != "telegram" || r.Author.Name.Value != "纸云频道" || r.Author.Account.Value != "paperchannel" || r.Author.Avatar.Value != "https://cdn4.cdn-telegram.org/file/avatar.jpg" {
		t.Fatalf("作者: %+v", r.Author)
	}
	want := "第一行\n第二行 https://example.com/very/long/path?a=1 和 点这里（https://example.org/x） #tag"
	if r.Text.Value != want {
		t.Fatalf("正文:\n got %q\nwant %q", r.Text.Value, want)
	}
	if r.Title.Value != "第一行" || r.Stats["views"].Value != 1200 {
		t.Fatalf("标题/浏览: %q %+v", r.Title.Value, r.Stats["views"])
	}
	if r.Published.State != "available" || r.Published.Value.UTC().Format("2006-01-02 15:04") != "2026-11-01 10:30" {
		t.Fatalf("时间: %+v", r.Published)
	}
	if len(r.Media.Value) != 2 || r.Media.Value[0].Type != "image" || r.Media.Value[1].URL.Value != "https://cdn4.cdn-telegram.org/file/p2.jpg" {
		t.Fatalf("图片: %+v", r.Media.Value)
	}
}

func TestFetchTelegramVideo(t *testing.T) {
	r, _, err := tgFetch(t, "https://t.me/telegram/300", tgVideoPage, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Media.Value) != 1 || r.Media.Value[0].Type != "video" || r.Media.Value[0].URL.Value != "https://cdn1.telesco.pe/file/v.mp4" ||
		r.Media.Value[0].Duration.Value != 65 || r.Media.Value[0].Thumbnail.Value != "https://cdn1.telesco.pe/file/thumb.jpg" {
		t.Fatalf("视频: %+v", r.Media.Value)
	}
	if r.Text.State == "available" || r.Title.State != "available" || !strings.Contains(r.Title.Value, "#300") || r.Stats["views"].Value != 845 {
		t.Fatalf("纯视频帖: text=%+v title=%q", r.Text, r.Title.Value)
	}
	// 没有视频文件（网页版不给）：退成封面图并标记媒体不完整
	page := strings.Replace(tgVideoPage, `<video src="https://cdn1.telesco.pe/file/v.mp4" class="tgme_widget_message_video js-message_video"></video>`, "", 1)
	r, _, err = tgFetch(t, "https://t.me/telegram/300", page, 200)
	if err != nil || len(r.Media.Value) != 1 || r.Media.Value[0].Type != "image" || !r.Media.Truncated {
		t.Fatalf("没有视频文件: %v %+v trunc=%v", err, r.Media.Value, r.Media.Truncated)
	}
}

func TestFetchTelegramUntrustedMediaHostsDropped(t *testing.T) {
	page := strings.ReplaceAll(tgPhotoPage, "cdn4.cdn-telegram.org/file/p1.jpg", "evil.example/p1.jpg")
	r, _, err := tgFetch(t, "https://t.me/paperchannel/300", page, 200)
	if err != nil || len(r.Media.Value) != 1 || strings.Contains(r.Media.Value[0].URL.Value, "evil") {
		t.Fatalf("非 Telegram 图床的图不该收: %v %+v", err, r.Media.Value)
	}
}

// 网页版不显示的频道：embed 页 200，只有一句提示，没有帖子节点。不能当空帖子发出去。
func TestFetchTelegramHiddenChannelFails(t *testing.T) {
	_, _, err := tgFetch(t, "https://t.me/wtmsd/206009", tgHiddenPage, 200)
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Kind != "not_returned" || fe.Reason != "channel_hidden" {
		t.Fatalf("应当按网页版不显示处理: %v", err)
	}
	// t.me/s/ 写法对这类频道是 302：同样是失败
	f := testFetcher(t, func(req *http.Request) (*http.Response, error) {
		resp := response(302, "")
		resp.Header.Set("Location", "https://t.me/wtmsd")
		return resp, nil
	})
	if _, err = f.Fetch(context.Background(), link(t, "https://t.me/s/wtmsd/206009")); err == nil {
		t.Fatal("302 应当失败")
	}
}

func TestFetchTelegramNotFoundAndMismatch(t *testing.T) {
	_, _, err := tgFetch(t, "https://t.me/paperchannel/9", `<div class="tgme_widget_message_error">Post not found</div>`, 200)
	wantError(t, err, "not_found")
	_, _, err = tgFetch(t, "https://t.me/paperchannel/301", tgPhotoPage, 200)
	wantError(t, err, "identity_mismatch")
	_, _, err = tgFetch(t, "https://t.me/paperchannel/300", "<html>nothing</html>", 200)
	wantError(t, err, "invalid_response")
}

// 私有频道要登录：识别出来，但不发任何请求。
func TestFetchTelegramPrivateChannelNoRequest(t *testing.T) {
	_, urls, err := tgFetch(t, "https://t.me/c/1234567890/42", tgPhotoPage, 200)
	wantError(t, err, "login_required")
	if len(urls) != 0 {
		t.Fatalf("私有频道不该请求: %v", urls)
	}
}

// 真实的屏蔽页（2026-11-10 取回的结构，文字照原样，频道名是编的）：提示写在错误节点 tgme_widget_message_error 里。
// 错误节点存在不等于帖子不存在：要先看提示文字，认出“can't be displayed”才是网页版不显示这个频道。
const tgHiddenErrorNodePage = `<html><body class="widget_frame_base tgme_widget"><div class="tgme_widget_message text_not_supported_wrap err_message js-widget_message">
<div class="tgme_widget_message_user"><i class="tgme_widget_message_user_photo" data-content="!"></i></div>
<div class="tgme_widget_message_bubble"><div class="tgme_widget_message_error" dir="auto">This channel can’t be displayed because it was used to spread pornographic content.</div></div></div></body></html>`

func TestFetchTelegramHiddenChannelInErrorNode(t *testing.T) {
	_, _, err := tgFetch(t, "https://t.me/wtmsd/206009", tgHiddenErrorNodePage, 200)
	var fe *FetchError
	if !errors.As(err, &fe) || fe.Kind != "not_returned" || fe.Reason != "channel_hidden" {
		t.Fatalf("错误节点里写了 can't be displayed，应是 not_returned / channel_hidden，实际 %+v", err)
	}
}

// 老帖子的 embed 页没有浏览数节点：统计里如实写“没返回”，不留空表；有的写法（带逗号、B）也能读。
func TestFetchTelegramViewsMissingAndFormats(t *testing.T) {
	page := strings.Replace(tgVideoPage, `<span class="tgme_widget_message_views">845</span>`, "", 1)
	r, _, err := tgFetch(t, "https://t.me/telegram/300", page, 200)
	if err != nil {
		t.Fatal(err)
	}
	if v, ok := r.Stats["views"]; !ok || v.State == "available" || v.Reason != "not_returned" {
		t.Fatalf("没有浏览数节点应记 not_returned: %+v ok=%v", v, ok)
	}
	for in, want := range map[string]int64{"2.82M": 2820000, "9.35K": 9350, "845": 845, "1,234": 1234, "12 345": 12345} {
		if got, ok := tgViews(in); !ok || got != want {
			t.Errorf("tgViews(%q) = %d %v，要 %d", in, got, ok, want)
		}
	}
}
