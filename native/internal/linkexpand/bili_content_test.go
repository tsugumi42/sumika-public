package linkexpand

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
)

func bangumiResult() linkreader.Result {
	r := siteResult("bilibili", "episode", "ep850002")
	r.Title, r.Text = av("纸船物语"), av("这是编的番剧简介。")
	r.Tags = av([]string{"冒险", "校园"})
	r.Published = av(time.Date(2020, 4, 11, 16, 0, 0, 0, time.UTC))
	r.Bangumi = &linkreader.BangumiInfo{Rating: av(9.8), RatingCount: av(int64(21726)), Status: av("已完结，共12话"), Areas: av("日本"), Episode: av("第2话 遇到风浪")}
	r = withMedia(r, goldenImage(1))
	return withStats(r, map[string]int64{"view": 2500000, "danmaku": 39000, "like": 19000, "coin": 23000, "follow": 80000, "share": 2700})
}

func courseResult() linkreader.Result {
	r := siteResult("bilibili", "course", "ss860001")
	r.Title, r.Text = av("纸艺入门课"), av("从零开始学折纸")
	r.Author.Name = av("纸灯实验室")
	r.Course = &linkreader.CourseInfo{Price: av("256 B币"), Discount: av("券后188 B币"), Release: av("已完结 | 共20课时"), Trial: av("6 课时可试看")}
	r = withMedia(r, goldenImage(1))
	return withStats(r, map[string]int64{"view": 1790000, "lessons": 20})
}

func audioResult() linkreader.Result {
	r := siteResult("bilibili", "audio", "au870001")
	r.Title, r.Text = av("纸船谣"), av("这是编的音频简介。")
	r.Author.Name = av("纸灯实验室")
	r.Published = av(time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC))
	audio := linkreader.Media{Type: "audio", URL: linkreader.Field[string]{State: "unsupported", Reason: "playback_not_requested"}, Duration: av(239.0)}
	r = withMedia(r, goldenImage(1), audio)
	return withStats(r, map[string]int64{"view": 13446, "favorite": 375, "reply": 50, "share": 44, "coin": 23})
}

func collectionResult() linkreader.Result {
	r := siteResult("bilibili", "collection", "season:770005:880001")
	r.Title, r.Text = av("折纸教程合集"), av("这是编的合集简介。")
	r.Author.Name = av("纸灯实验室")
	r.Collection = &linkreader.CollectionInfo{Type: av("合集"), Total: av(int64(48)), Items: av([]string{"第一课", "第二课", "第三课"})}
	r = withMedia(r, goldenImage(1))
	return withStats(r, map[string]int64{"videos": 48})
}

// 番剧的卡片现在是可选样式（@卡片），默认是文字版（用户 2026-11-12）；卡片上的数字同样缩写
func TestBiliBangumiCard(t *testing.T) {
	r := bangumiResult()
	d := Build(&r, "", Options{Templates: map[string]string{"bilibili-bangumi": "@卡片"}}, 0)
	c := d.Parts[0].Body[0].Card
	if c == nil || c.Title != "纸船物语\n第2话 遇到风浪" || c.Author != "已完结，共12话" || c.DateDuration != "评分：9.8（2.2万 人评分）｜首播：2020-04-12" {
		t.Fatalf("%+v", c)
	}
	if len(c.Stats) != 2 || c.Stats[0] != "播放 250万｜弹幕 3.9万｜点赞 1.9万" || c.Stats[1] != "投币 2.3万｜追番 8万｜转发 2700" {
		t.Fatalf("%v", c.Stats)
	}
}

// 番剧默认是文字版，结构照视频（用户 2026-11-12），不写平台名、不写简介；@图文 和默认一样
func TestBiliBangumiText(t *testing.T) {
	want := "[图片]\n纸船物语\n第2话 遇到风浪\n评分 9.8 | 已完结，共12话\n播放 250万 | 追番 8万\n首播：2020-04-12\nhttps://example.invalid/bilibili/ep850002"
	for _, o := range []Options{{}, {Templates: map[string]string{"bilibili-bangumi": "@图文"}}} {
		r := bangumiResult()
		d := Build(&r, "", o, 0)
		if got := strings.Join(d.Describe(), "\n---\n"); got != want {
			t.Fatalf("得到：\n%s\n想要：\n%s", got, want)
		}
	}
	// 简介字段还在：自定义模板写上就有
	r := bangumiResult()
	d := Build(&r, "", Options{Templates: map[string]string{"bilibili-bangumi": "$标题\n$简介\n$链接"}}, 0)
	if got := strings.Join(d.Describe(), "\n"); !strings.Contains(got, "这是编的番剧简介。") {
		t.Fatalf("自定义模板写了 $简介 却没有：\n%s", got)
	}
}

// ss 链接没有“哪一集”，卡片标题只有番名；没有评分人数只写分数。
func TestBiliBangumiNoEpisodeNoCount(t *testing.T) {
	r := bangumiResult()
	r.Bangumi.Episode = linkreader.Field[string]{State: "not_returned", Reason: "not_applicable"}
	r.Bangumi.RatingCount = nr[int64]()
	d := Build(&r, "", Options{Templates: map[string]string{"bilibili-bangumi": "@卡片"}}, 0)
	c := d.Parts[0].Body[0].Card
	if c.Title != "纸船物语" || !strings.HasPrefix(c.DateDuration, "评分：9.8｜") {
		t.Fatalf("%+v", c)
	}
}

func TestBiliCourseDefaultText(t *testing.T) {
	got := describe(t, courseResult())
	want := "（回复原消息）\n[图片]\n纸艺入门课\nUP主：纸灯实验室\n价格：256 B币（券后188 B币）\n已完结 | 共20课时\n试看：6 课时可试看\n播放 179万\nhttps://example.invalid/bilibili/ss860001"
	if got != want {
		t.Fatalf("得到：\n%s\n想要：\n%s", got, want)
	}
}

func TestBiliCourseTrialUnknown(t *testing.T) {
	r := courseResult()
	r.Course.Trial = linkreader.Field[string]{State: "failed", Reason: "risk_control"}
	r.Course.Discount = nr[string]()
	got := describe(t, r)
	if strings.Contains(got, "试看") || strings.Contains(got, "未返回") || !strings.Contains(got, "价格：256 B币\n") {
		t.Fatalf("\n%s", got)
	}
}

func TestBiliAudioDefaultText(t *testing.T) {
	got := describe(t, audioResult())
	want := "（回复原消息）\n[图片]\n纸船谣\nUP主：纸灯实验室\n时长：03:59\n发布：11-01 18:30\n播放 1.3万 | 收藏 375 | 评论 50 | 分享 44 | 投币 23\nhttps://example.invalid/bilibili/au870001"
	if got != want {
		t.Fatalf("得到：\n%s\n想要：\n%s", got, want)
	}
}

func TestBiliCollectionDefaultText(t *testing.T) {
	got := describe(t, collectionResult())
	want := "（回复原消息）\n[图片]\n折纸教程合集\nUP主：纸灯实验室\n共 48 个视频\n最前面：《第一课》《第二课》《第三课》\nhttps://example.invalid/bilibili/season:770005:880001"
	if got != want {
		t.Fatalf("得到：\n%s\n想要：\n%s", got, want)
	}
	r := collectionResult()
	r.Collection.Type = av("系列")
	if got := describe(t, r); !strings.Contains(got, "折纸教程合集") || strings.Contains(got, "B站系列") {
		t.Fatalf("\n%s", got)
	}
}

func TestBiliMoreCards(t *testing.T) {
	for name, c := range map[string]struct {
		r      linkreader.Result
		title  string
		author string
		date   string
	}{
		"课程": {courseResult(), "纸艺入门课", "UP主：纸灯实验室", "价格：256 B币（券后188 B币）"},
		"音频": {audioResult(), "纸船谣", "UP主：纸灯实验室", "时长：03:59｜发布：11-01 18:30"},
		"合集": {collectionResult(), "折纸教程合集", "UP主：纸灯实验室", "合集｜共 48 个视频"},
	} {
		t.Run(name, func(t *testing.T) {
			r := c.r
			site := map[string]string{"课程": "bilibili-course", "音频": "bilibili-audio", "合集": "bilibili-collection"}[name]
			d := Build(&r, "", Options{Templates: map[string]string{site: "@卡片"}}, 0)
			card := d.Parts[0].Body[0].Card
			if card == nil || card.Title != c.title || card.Author != c.author || card.DateDuration != c.date {
				t.Fatalf("%+v", card)
			}
		})
	}
}
