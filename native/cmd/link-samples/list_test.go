package main

import (
	"strings"
	"testing"
)

func TestParseListPairsCommentsAndNumbers(t *testing.T) {
	text := "# 清单标题\n# 来源说明\n# bilibili/video/横屏 1080p [库]\nhttps://www.bilibili.com/video/BV1xx411c7mD\n\n" +
		"# x/post//i/web/ 无账号形态 [旧清单]\r\nhttps://x.com/i/web/status/1\n" +
		"https://x.com/a/status/2\n" +
		"# bilibili/dynamic/旧域名 [新找]\nhttps://t.bilibili.com/1\n"
	es := parseList(text)
	if len(es) != 4 {
		t.Fatalf("应有 4 条，得到 %d：%+v", len(es), es)
	}
	want := []entry{
		{URL: "https://www.bilibili.com/video/BV1xx411c7mD", Platform: "bilibili", Kind: "video", Variant: "横屏 1080p", From: "库", Code: "B-01"},
		{URL: "https://x.com/i/web/status/1", Platform: "x", Kind: "post", Variant: "/i/web/ 无账号形态", From: "旧清单", Code: "X-01"},
		{URL: "https://x.com/a/status/2", Code: "U-01"}, // 没有注释的不沿用上一条的标签
		{URL: "https://t.bilibili.com/1", Platform: "bilibili", Kind: "dynamic", Variant: "旧域名", From: "新找", Code: "B-02"},
	}
	for i := range want {
		if es[i] != want[i] {
			t.Errorf("第 %d 条：得到 %+v，要 %+v", i+1, es[i], want[i])
		}
	}
}

func TestParseLabelOddShapes(t *testing.T) {
	if p, k, v, f := parseLabel("随便写的说明"); p != "" || k != "" || v != "随便写的说明" || f != "" {
		t.Fatalf("格式不对的整行当变体：%q %q %q %q", p, k, v, f)
	}
	if _, _, v, f := parseLabel("pixiv/artwork/R18 4 张（预览） [新找]"); v != "R18 4 张（预览）" || f != "新找" {
		t.Fatalf("变体和来源拆错：%q %q", v, f)
	}
}

func TestCodesNeverCollide(t *testing.T) {
	var es []entry
	for _, p := range []string{"bilibili", "x", "bilibili", "foo", "fooo", "xiaohongshu", "xiaoheihe"} {
		es = append(es, entry{Platform: p})
	}
	numberEntries(es)
	seen := map[string]bool{}
	for _, e := range es {
		if seen[e.Code] {
			t.Fatalf("编号重复：%s", e.Code)
		}
		seen[e.Code] = true
	}
	if es[2].Code != "B-02" || es[4].Code != "FO-02" {
		t.Fatalf("同前缀应共用计数：%+v", es)
	}
}

func TestShownKeepsVariantDropsTokens(t *testing.T) {
	got := shown("https://www.bilibili.com/video/BV1Zy4y1K7SH?p=12&vd_source=abc#reply")
	if got != "https://www.bilibili.com/video/BV1Zy4y1K7SH?p=12" {
		t.Fatalf("要留 p、去掉跟踪参数和片段：%s", got)
	}
	got = shown("https://www.xiaohongshu.com/explore/abc?xsec_token=SECRET&xsec_source=pc")
	if strings.Contains(got, "SECRET") || strings.Contains(got, "xsec") {
		t.Fatalf("令牌不能出现在页面上：%s", got)
	}
}

func TestPick(t *testing.T) {
	es := make([]entry, 10)
	for i := range es {
		es[i].URL = string(rune('a' + i))
	}
	got, err := pick(es, "2, 5-7")
	if err != nil || len(got) != 4 || got[0].URL != "b" || got[3].URL != "g" {
		t.Fatalf("pick 结果不对：%+v %v", got, err)
	}
	if _, err := pick(es, "3-1"); err == nil {
		t.Fatal("倒着的区间要报错")
	}
}

func TestMergeSampleKeepsOthersInListOrder(t *testing.T) {
	all := []entry{{Code: "B-01"}, {Code: "B-02"}, {Code: "X-01"}}
	prev := map[string]sampleOut{"B-01": {Code: "B-01", Title: "旧1"}, "B-02": {Code: "B-02", Title: "旧2"}, "X-01": {Code: "X-01", Title: "旧X"}}
	picked := map[string]bool{"B-02": true}
	got := mergeSample(nil, sampleOut{Code: "B-02", Title: "新2"}, prev, picked, all)
	if len(got) != 3 || got[0].Title != "旧1" || got[1].Title != "新2" || got[2].Title != "旧X" {
		t.Fatalf("合并结果不对：%+v", got)
	}
	if got := mergeSample(nil, sampleOut{Code: "B-02"}, nil, picked, all); len(got) != 1 {
		t.Fatalf("没有上一次结果时只追加：%+v", got)
	}
}

func TestRerunKeepsFullRunInfo(t *testing.T) {
	prev := runInfo{At: "全量时间", Theme: "dark", TotalSecond: 808, Deviations: []string{"旧"}}
	got := rerunInfo(prev, runInfo{At: "重跑时间", Theme: "light"}, []string{"GH-06", "XH-01"})
	if got.At != "全量时间" || got.Theme != "dark" || got.TotalSecond != 808 || len(got.Deviations) != 2 || got.Deviations[1] != "重跑时间 用 -pick 重跑过：GH-06、XH-01" {
		t.Fatalf("重跑要沿用全量的运行信息并追加一条：%+v", got)
	}
	if len(prev.Deviations) != 1 {
		t.Fatal("不能改到上一次的切片")
	}
}
