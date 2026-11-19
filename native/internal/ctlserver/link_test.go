package ctlserver

import (
	"context"
	"strings"
	"testing"

	"sumika-native/internal/ctlproto"
)

type linkFake struct {
	fakeBackend
	urls []string
	args []ctlproto.LinkArgs
	site []string
}

func (f *linkFake) LinkPreview(_ context.Context, u string, a ctlproto.LinkArgs) (ctlproto.LinkPreviewResult, error) {
	f.rec("link-preview")
	f.urls, f.args = append(f.urls, u), append(f.args, a)
	return ctlproto.LinkPreviewResult{Site: "bilibili-video", Source: "fetched", Messages: []string{"甲"}}, nil
}
func (f *linkFake) LinkTemplates(_ context.Context, site string) (ctlproto.LinkTemplates, error) {
	f.rec("link-template")
	f.site = append(f.site, site)
	return ctlproto.LinkTemplates{Sites: []ctlproto.LinkSiteInfo{{Key: "x"}}}, nil
}

// link 是只读命令：后端收到校验过的链接和选项，不写“改状态”的日志
func TestLinkPreviewAndTemplate(t *testing.T) {
	f := &linkFake{}
	var logs []string
	name, _ := start(t, f, Options{Logf: func(format string, args ...any) { logs = append(logs, format) }})
	r := ask(t, name, ctlproto.Request{Cmd: "link", Args: []string{"preview", "https://example.invalid/v/1"}, Link: &ctlproto.LinkArgs{Template: "@封面文字", Site: "bilibili-video"}})
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	if v := dataOf[ctlproto.LinkPreviewResult](t, r); v.Source != "fetched" || len(v.Messages) != 1 {
		t.Fatalf("%+v", v)
	}
	if len(f.urls) != 1 || f.urls[0] != "https://example.invalid/v/1" || f.args[0].Template != "@封面文字" || f.args[0].Site != "bilibili-video" {
		t.Fatalf("后端收到的参数不对：%v %+v", f.urls, f.args)
	}
	r = ask(t, name, ctlproto.Request{Cmd: "link", Args: []string{"template", "x"}})
	if !r.OK || len(f.site) != 1 || f.site[0] != "x" {
		t.Fatalf("link template：%+v %v", r, f.site)
	}
	r = ask(t, name, ctlproto.Request{Cmd: "link", Args: []string{"preview"}, Link: &ctlproto.LinkArgs{Sample: true, Site: "x"}})
	if !r.OK || f.urls[len(f.urls)-1] != "" || !f.args[len(f.args)-1].Sample {
		t.Fatalf("--sample：%+v", r)
	}
	if len(logs) != 0 {
		t.Fatalf("只读命令不该写日志：%v", logs)
	}
}

// 不合格的请求一律 invalid，不碰后端：缺子命令、不认识的子命令、非 http 链接、带空白或换行、过长的模板、link 选项出现在别的命令上
func TestLinkRejectsBadRequests(t *testing.T) {
	f := &linkFake{}
	name, _ := start(t, f, Options{})
	bad := []ctlproto.Request{
		{Cmd: "link"},
		{Cmd: "link", Args: []string{"nope"}},
		{Cmd: "link", Args: []string{"preview"}},
		{Cmd: "link", Args: []string{"preview", "file:///etc/passwd"}},
		{Cmd: "link", Args: []string{"preview", "https://example.invalid/a b"}},
		{Cmd: "link", Args: []string{"preview", "https://example.invalid/a\nb"}},
		{Cmd: "link", Args: []string{"preview", "https://example.invalid/a", "多余"}},
		{Cmd: "link", Args: []string{"preview", "https://example.invalid/a"}, Link: &ctlproto.LinkArgs{Template: strings.Repeat("字", ctlproto.MaxLinkTemplate+1)}},
		{Cmd: "link", Args: []string{"preview", "https://example.invalid/a"}, Link: &ctlproto.LinkArgs{Site: "x\x00"}},
		{Cmd: "link", Args: []string{"preview"}, Link: &ctlproto.LinkArgs{Sample: true}},
		{Cmd: "link", Args: []string{"template", "a", "b"}},
		{Cmd: "link", Args: []string{"template"}, Link: &ctlproto.LinkArgs{Template: "x"}},
		{Cmd: "status", Link: &ctlproto.LinkArgs{Site: "x"}},
	}
	for _, req := range bad {
		if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeInvalid {
			t.Errorf("%+v 应当 invalid：%+v", req, r)
		}
	}
	wantCalls(t, &f.fakeBackend)
}

// 演示模式回 demo；后端没接链接解析回 failed
func TestLinkModes(t *testing.T) {
	req := ctlproto.Request{Cmd: "link", Args: []string{"template"}}
	f := &linkFake{}
	name, _ := start(t, f, Options{Demo: true})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeDemo {
		t.Errorf("演示模式：%+v", r)
	}
	name, _ = start(t, &fakeBackend{}, Options{})
	if r := ask(t, name, req); r.OK || r.Code != ctlproto.CodeFailed {
		t.Errorf("没接链接解析：%+v", r)
	}
}
