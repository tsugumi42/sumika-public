package linkexpand

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
)

// B 站小游戏页：默认排版（封面、标题、作者｜分类、发布时间、链接，行间不空行），统计字段还在、默认不写。

func toyResult() linkreader.Result {
	r := siteResult("bilibili", "toy", "zhizhou-youxi")
	r.Title = av("纸舟小游戏")
	r.Author.Name = av("纸灯实验室")
	r.Category = av("GAME")
	r.Published = av(time.Date(2020, 10, 26, 1, 0, 0, 0, time.UTC)) // 往年：带年份，不随当前年份变
	r = withStats(r, map[string]int64{"play": 51149, "like": 523, "favorite": 667, "comment": 59, "share": 0})
	return withMedia(r, goldenImage(1))
}

func TestBiliToyDefaultText(t *testing.T) {
	got := describe(t, toyResult())
	want := "（回复原消息）\n[图片]\n纸舟小游戏\n作者：纸灯实验室 | 游戏\n2020-10-26 09:00\nhttps://example.invalid/bilibili/zhizhou-youxi"
	if got != want {
		t.Fatalf("得到：\n%s\n想要：\n%s", got, want)
	}
	for _, bad := range []string{"游玩", "点赞", "收藏", "评论", "51149", "523"} {
		if strings.Contains(got, bad) {
			t.Fatalf("默认模板不写统计 %q：\n%s", bad, got)
		}
	}
}

// 分类只有 GAME 翻成“游戏”，别的原样；没有的字段整行整段消失，不留空行和孤立的竖线。
func TestBiliToyCategoryAndMissing(t *testing.T) {
	r := toyResult()
	r.Category = av("TOOL")
	if got := describe(t, r); !strings.Contains(got, "作者：纸灯实验室 | TOOL\n") {
		t.Fatalf("别的分类原样：\n%s", got)
	}
	r.Category = nr[string]()
	r.Published = nr[time.Time]()
	got := describe(t, r)
	if want := "（回复原消息）\n[图片]\n纸舟小游戏\n作者：纸灯实验室\nhttps://example.invalid/bilibili/zhizhou-youxi"; got != want {
		t.Fatalf("得到：\n%s\n想要：\n%s", got, want)
	}
	r.Author.Name = nr[string]()
	got = describe(t, r)
	if strings.Contains(got, "作者") || strings.Contains(got, "\n\n") || strings.Contains(got, "｜") {
		t.Fatalf("作者没有时不留痕迹：\n%q", got)
	}
}

// 游玩、点赞、收藏、评论、分享的字段还在：显式模板写上就有。
func TestBiliToyStatFieldsStillWork(t *testing.T) {
	r := toyResult()
	o := Options{Templates: map[string]string{"bilibili-toy": "$标题\n游玩 $游玩｜点赞 $点赞｜收藏 $收藏｜评论 $评论｜分享 $分享"}}
	d := Build(&r, "", o, 0)
	got := strings.Join(d.Describe(), "\n")
	if !strings.Contains(got, "游玩 5.1万") && !strings.Contains(got, "游玩 51149") {
		t.Fatalf("缺游玩：\n%s", got)
	}
	for _, want := range []string{"点赞 523", "收藏 667", "评论 59", "分享 0"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q：\n%s", want, got)
		}
	}
}

func TestBiliToyCard(t *testing.T) {
	r := toyResult()
	d := Build(&r, "", Options{Templates: map[string]string{"bilibili-toy": "@卡片"}}, 0)
	if len(d.Parts) == 0 || len(d.Parts[0].Body) == 0 || d.Parts[0].Body[0].Card == nil {
		t.Fatalf("没有卡片：%+v", d.Parts)
	}
	c := d.Parts[0].Body[0].Card
	if c.Title != "纸舟小游戏" || c.Author != "作者：纸灯实验室" || !strings.HasPrefix(c.DateDuration, "游戏｜") {
		t.Fatalf("%+v", c)
	}
}
