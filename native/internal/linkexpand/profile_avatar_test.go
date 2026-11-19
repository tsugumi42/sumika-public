package linkexpand

import (
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
)

// #54 BS-08：Bluesky、Misskey、Mastodon、TikTok 的个人主页，取数把头像放在 Author.Avatar、Media 为空，排版以前一张图都没有。
func profileWithAvatar(platform, kind string) linkreader.Result {
	r := siteResult(platform, kind, "90100")
	r.Title, r.Author.Name, r.Author.Account = av("纸云"), av("纸云"), av("paper_cloud")
	r.Author.Avatar = av("https://i0.example.invalid/avatar.png")
	r.Media = av([]linkreader.Media{})
	return r
}

func TestProfilePagesUseAvatarAsImage(t *testing.T) {
	for _, c := range [][2]string{{"bluesky", "profile"}, {"misskey", "user"}, {"mastodon", "account"}, {"tiktok", "user"}} {
		r := profileWithAvatar(c[0], c[1])
		d := Build(&r, "", Options{}, 0)
		var got *Item
		for _, p := range d.Parts {
			for i, it := range p.Body {
				if it.Kind == "image" {
					got = &p.Body[i]
				}
			}
		}
		if got == nil {
			t.Fatalf("%s/%s 的主页没有头像：\n%s", c[0], c[1], strings.Join(d.Describe(), "\n---\n"))
		}
		if got.Media.URL.Value != "https://i0.example.invalid/avatar.png" || !got.Optional {
			t.Fatalf("%s/%s 头像项不对（要可选，下不来不写说明）：%+v", c[0], c[1], got)
		}
	}
}

// 头像只给主页用：帖子、视频的作者头像不冒充封面；主页自己已有图片媒体时不再多一张头像。
func TestAvatarNotUsedElsewhere(t *testing.T) {
	post := siteResult("bluesky", "post", "90101/aaaaaaaaaaaaa")
	post.Text = av("一条没有图的帖子")
	post.Author.Avatar = av("https://i0.example.invalid/avatar.png")
	post.Media = av([]linkreader.Media{})
	if got := describe(t, post); strings.Contains(got, "[图片]") {
		t.Fatalf("帖子不该用头像当图：\n%s", got)
	}
	r := profileWithAvatar("bluesky", "profile")
	r.Media = av([]linkreader.Media{goldenImage(1)})
	d := Build(&r, "", Options{}, 0)
	n := 0
	for _, p := range d.Parts {
		for _, it := range p.Body {
			if it.Kind == "image" {
				n++
				if it.Media.URL.Value == "https://i0.example.invalid/avatar.png" {
					t.Fatalf("已有图片媒体还多了头像")
				}
			}
		}
	}
	if n != 1 {
		t.Fatalf("图数 %d，要 1", n)
	}
}

// 头像是锦上添花：被下载范围拦下、或者别的原因没下来，主页都照常发文字，不补“图片没取到”“图片未下载”。
func TestAvatarLossIsSilent(t *testing.T) {
	r := profileWithAvatar("bluesky", "profile")
	d := Build(&r, "-71", Options{}, 0)
	for name, e := range map[string]error{"静默原因": errUnsafe, "网络错误": errors.New("编的网络错误")} {
		parts := prepareShape(t, r, d, failLoader{fail: map[string]error{"https://i0.example.invalid/avatar.png": e}})
		got := segText(parts[0].Body)
		if countVisual(parts[0].Body) != 0 || strings.Contains(got, "没取到") || strings.Contains(got, "未下载") {
			t.Fatalf("%s：头像丢了不该写说明：%q", name, got)
		}
		if !strings.Contains(got, "纸云") {
			t.Fatalf("%s：文字不见了：%q", name, got)
		}
	}
	parts := prepareShape(t, r, d, failLoader{})
	if countVisual(parts[0].Body) != 1 {
		t.Fatalf("头像下得来就该发出去：%+v", parts[0].Body)
	}
}

// #82 TT-04：官方号 @tiktok 的昵称本来就叫“TikTok”，标题和作者行写了两遍。用户主页的作者行只在账号和昵称不同时补一行 @账号。
func TestTikTokUserPageNoDuplicateName(t *testing.T) {
	r := siteResult("tiktok", "user", "tiktok")
	r.Title, r.Author.Name, r.Author.Account = av("TikTok"), av("TikTok"), av("tiktok")
	r.Text = av("Your next discovery is waiting on TikTok.")
	got := describe(t, r)
	if strings.Count(got, "TikTok") != 2 { // 标题一次、签名里一次
		t.Fatalf("昵称重复了：\n%s", got)
	}
	r2 := siteResult("tiktok", "user", "paper_lamp")
	r2.Title, r2.Author.Name, r2.Author.Account = av("纸灯"), av("纸灯"), av("paper_lamp")
	r2.Text = av("做纸灯")
	if got := describe(t, r2); !strings.Contains(got, "纸灯\n做纸灯\n@paper_lamp") || strings.Contains(got, "纸灯 @paper_lamp") {
		t.Fatalf("昵称和账号不同时补一行 @账号，不再重复昵称：\n%s", got)
	}
	// 视频不受影响：还是“昵称 @账号”
	v := siteResult("tiktok", "video", "90110")
	v.Title, v.Author.Name, v.Author.Account = av("标题"), av("纸灯"), av("paper_lamp")
	if got := describe(t, withMedia(v, goldenVideo())); !strings.Contains(got, "纸灯 @paper_lamp") {
		t.Fatalf("视频的作者行不该变：\n%s", got)
	}
}

// 主页卡片：标题已经是昵称，作者行不再把昵称写第二遍（TT-04、BS-08 卡片）；账号和昵称不同才补 @账号。
func TestProfileCardAuthorNotDuplicated(t *testing.T) {
	for _, c := range [][2]string{{"tiktok", "user"}, {"bluesky", "profile"}, {"misskey", "user"}} {
		r := profileWithAvatar(c[0], c[1])
		r.Author.Name, r.Author.Account, r.Title = av("TikTok"), av("tiktok"), av("TikTok")
		if got := genericCardOf(t, r).Author; got != "" {
			t.Fatalf("%s/%s 昵称和账号相同，作者行该空：%q", c[0], c[1], got)
		}
		r.Author.Name, r.Title = av("纸云"), av("纸云")
		if got := genericCardOf(t, r).Author; got != "@tiktok" {
			t.Fatalf("%s/%s 该补 @账号：%q", c[0], c[1], got)
		}
	}
}
