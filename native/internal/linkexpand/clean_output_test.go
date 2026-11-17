package linkexpand

import (
	"encoding/json"
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

// 发出去的文字里用的必须是清过的链接：每个能展开的站点，拿它的示例链接加上通用追踪参数，
// 识别、排版后整个草稿（直发、转发两种形态）里都不能出现追踪参数。
func TestEverySiteOutputUsesCleanURL(t *testing.T) {
	const marker = "zzdirty"
	checked := 0
	for _, p := range linktemplate.Catalog() {
		for _, k := range p.Kinds {
			if k.Support != linktemplate.SupportExpand || k.Example == "" {
				continue
			}
			sep := "?"
			if strings.Contains(k.Example, "?") {
				sep = "&"
			}
			dirty := k.Example + sep + "utm_source=" + marker + "&fbclid=" + marker
			l, ok := linkreader.Recognize(dirty)
			if !ok {
				t.Errorf("%s/%s：识别不了 %s", p.ID, k.ID, dirty)
				continue
			}
			if strings.Contains(l.Clean, marker) {
				t.Errorf("%s/%s：Recognize.Clean 带着追踪参数：%s", p.ID, k.ID, l.Clean)
				continue
			}
			for _, forward := range []string{"never", "always"} {
				r := goldenBase(l.Platform, l.Kind, l.ID)
				if l.Kind == "mall" {
					r = mallResult(t)
				}
				r.Title, r.Text = av("标题"), av("正文")
				r.CleanURL = l.Clean
				d := Build(&r, "-71", Options{Sites: map[string]SiteSetting{linktemplate.SiteKey(l.Platform, l.Kind): {Forward: forward}}}, 0)
				b, _ := json.Marshal(d)
				if strings.Contains(string(b), marker) {
					t.Errorf("%s/%s（%s）：输出里有追踪参数：%s", p.ID, k.ID, forward, b)
				}
			}
			checked++
		}
	}
	if checked < 10 {
		t.Fatalf("只检查了 %d 个站点页面", checked)
	}
}

// 网易云微信分享页的整串参数（2026-11-10 群里实际发出的样子）洗干净后才进输出。
func TestNeteaseShareURLIsCleanInOutput(t *testing.T) {
	l, ok := linkreader.Recognize("https://music.163.com/song?id=2751093063&fx-wechatnew=t1&fx-wxqd=&fx-wordtest=&fx-listentest=t3&ts-wakeup=t1&ts-wakeup-reversal=&H5_DownloadVIPGift=&playerUIModeId=76001&PlayerStyles_SynchronousSharing=t3")
	if !ok {
		t.Fatal("认不出")
	}
	r := goldenBase("netease", "song", l.ID)
	r.Title = av("歌名")
	r.CleanURL = l.Clean
	d := Build(&r, "-71", Options{}, 0)
	if !strings.Contains(d.Semantic(), "https://music.163.com/song?id=2751093063") || strings.Contains(d.Semantic(), "fx-") || strings.Contains(d.Semantic(), "playerUIModeId") {
		t.Fatalf("输出里的链接没洗干净：%s", d.Semantic())
	}
}
