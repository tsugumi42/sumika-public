package linkexpand

import (
	"strings"
	"testing"
)

// #30：用户发的是没账号的 /i/status/ 链接，文字版里的链接按取到的作者账号写，不照抄 /i/status/。
func TestTweetLinkUsesFetchedAuthor(t *testing.T) {
	r := goldenBase("x", "post", "80030")
	r.CleanURL = "https://x.com/i/status/80030"
	r.Author.Name, r.Author.Account = av("纸云"), av("paper_cloud")
	r.Text = av("编的正文")
	got := strings.Join(Build(&r, "", Options{}, 0).Describe(), "\n---\n")
	if !strings.Contains(got, "https://x.com/paper_cloud/status/80030") || strings.Contains(got, "/i/status/") {
		t.Fatalf("\n%s", got)
	}
	// 没取到账号：照旧用原链接
	r.Author.Account = nr[string]()
	got = strings.Join(Build(&r, "", Options{}, 0).Describe(), "\n---\n")
	if !strings.Contains(got, "https://x.com/i/status/80030") {
		t.Fatalf("没账号时：\n%s", got)
	}
}
