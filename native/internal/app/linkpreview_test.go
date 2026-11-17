package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"sumika-native/internal/linktemplate"
)

// link preview 的后台部分：示例数据、取数渲染、非法模板和站点键都按 invalid 报
func TestLinkPreviewAndTemplates(t *testing.T) {
	t.Parallel()
	_, a, _, _ := newExpandHost(t, "off", pictureFetcher{})
	d := a.Desktop().Reader
	ctx := context.Background()
	out, err := d.LinkPreview(ctx, LinkPreviewRequest{Sample: true, Site: "x", Template: "$作者｜$统计"})
	if err != nil || out.Source != "sample" || len(out.Messages) != 1 || !strings.Contains(out.Messages[0], "纸云 @paper_cloud｜点赞 1005") {
		t.Fatalf("示例预览：%v %+v", err, out)
	}
	out, err = d.LinkPreview(ctx, LinkPreviewRequest{URL: "https://x.com/paper/status/74001", Template: "$作者｜$链接"})
	if err != nil || out.Source != "fetched" || out.Forward || !strings.Contains(strings.Join(out.Messages, "\n"), "编的作者 @paper｜https://x.com/paper/status/74001") {
		t.Fatalf("取数预览：%v %+v", err, out)
	}
	for name, req := range map[string]LinkPreviewRequest{
		"非法模板":   {URL: "https://x.com/paper/status/74002", Template: "$不存在"},
		"不认识的站点": {Sample: true, Site: "nowhere"},
		"不是链接":   {URL: "不是链接"},
		"站点对不上":  {URL: "https://x.com/paper/status/74003", Site: "github"},
	} {
		if _, err := d.LinkPreview(ctx, req); !errors.Is(err, ErrLinkInvalid) {
			t.Errorf("%s 应当按 invalid 报：%v", name, err)
		}
	}
	sites, err := d.LinkTemplates(ctx, "x")
	if err != nil || len(sites) != 1 || sites[0].Direct != xDefault() || sites[0].ForwardMode != "auto" || sites[0].ForwardOver != 600 || len(sites[0].Fields) == 0 {
		t.Fatalf("link template：%v %+v", err, sites)
	}
	if all, err := d.LinkTemplates(ctx, ""); err != nil || len(all) < 10 || len(all[0].Fields) != 0 {
		t.Fatalf("全部站点：%v %d", err, len(all))
	}
}

func xDefault() string {
	site, _, _ := linktemplate.SiteByKey("x")
	return site.Source(false)
}
