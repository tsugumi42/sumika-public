package linkexpand

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/linkreader"
)

func videoWithComments(c linkreader.Field[[]linkreader.Comment]) linkreader.Result {
	r := goldenBili(1)
	r.Comments = c
	return r
}

// 默认的封面文字不写热评和统计（主人 11-09），这些测试守字段本身，所以用显式模板把它们写上。
const biliTextWithHot = "$封面$标题[\n$分P][\n$分P列表][\n[\nUP主：$UP主[｜$充电][｜$时长说明]][\n[播放 $播放][｜点赞 $点赞]][\n[收藏 $收藏][｜投币 $投币]]][\n\n$热评]\n\n$链接$视频"

func textStyle() Options {
	return Options{Templates: map[string]string{"bilibili-video": biliTextWithHot}}
}

func describeWith(t *testing.T, r linkreader.Result, o Options) string {
	t.Helper()
	d := Build(&r, "", o, 0)
	return strings.Join(d.Describe(), "\n---\n")
}

// 图文样式里写热评；评论不公开等情况写明确文案，不写成错误；没取过评论（旧缓存）就不写这一行。
func TestVideoTextShowsComments(t *testing.T) {
	hot := av([]linkreader.Comment{
		{Author: "用户甲", Text: "编的第一条评论", Likes: 12, Top: true},
		{Author: "用户乙", Text: "编的第二条评论", Likes: 3},
	})
	got := describeWith(t, videoWithComments(hot), textStyle())
	for _, want := range []string{"热评：", "1. ［置顶］用户甲：编的第一条评论（赞 12）", "2. 用户乙：编的第二条评论（赞 3）"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺 %q：\n%s", want, got)
		}
	}
	for reason, want := range map[string]string{
		"comments_selected_only": "评论不公开（UP 主精选后才可见）",
		"comments_closed":        "评论区已关闭",
	} {
		got := describeWith(t, videoWithComments(linkreader.Field[[]linkreader.Comment]{State: "not_returned", Reason: reason}), textStyle())
		if !strings.Contains(got, want) || strings.Contains(got, "解析失败") {
			t.Fatalf("%s 想要 %q：\n%s", reason, want, got)
		}
	}
	if got := describeWith(t, videoWithComments(linkreader.Field[[]linkreader.Comment]{State: "not_returned", Reason: "no_comments"}), textStyle()); strings.Contains(got, "评论") {
		t.Fatalf("没人评论不该占一行：%s", got)
	}
	got = describeWith(t, videoWithComments(linkreader.Field[[]linkreader.Comment]{State: "failed", Reason: "risk_control"}), textStyle())
	if !strings.Contains(got, "评论这次没取到（被平台拦了）") {
		t.Fatalf("\n%s", got)
	}
	if got := describeWith(t, goldenBili(1), textStyle()); strings.Contains(got, "评论") && !strings.Contains(got, "评论 8") {
		t.Fatalf("旧缓存没有评论字段，不该写评论行：\n%s", got)
	}
}

// 充电专属视频：图文里写“充电专属（有试看）”，卡片的作者行也带上；普通视频不写。
func TestVideoUpowerMark(t *testing.T) {
	r := goldenBili(1)
	r.Flags["is_upower_exclusive"] = linkreader.Field[any]{State: "available", Value: true}
	r.Flags["is_upower_preview"] = linkreader.Field[any]{State: "available", Value: true}
	got := describeWith(t, r, textStyle())
	if !strings.Contains(got, "充电专属（有试看）") {
		t.Fatalf("\n%s", got)
	}
	d := Build(&r, "", Options{Templates: map[string]string{"bilibili-video": "@卡片"}}, 0)
	if c := d.Parts[0].Body[0].Card; c == nil || !strings.Contains(c.Author, "｜充电专属（有试看）") {
		t.Fatalf("%+v", d.Parts[0].Body[0])
	}
	r.Flags["is_upower_preview"] = linkreader.Field[any]{State: "available", Value: false}
	if got := describeWith(t, r, textStyle()); !strings.Contains(got, "充电专属") || strings.Contains(got, "有试看") {
		t.Fatalf("\n%s", got)
	}
	plain := goldenBili(1)
	plain.Flags["is_upower_exclusive"] = linkreader.Field[any]{State: "available", Value: false}
	if got := describeWith(t, plain, textStyle()); strings.Contains(got, "充电") {
		t.Fatalf("\n%s", got)
	}
}

// 热评默认不写（规格 B1；用户 2026-11-13 复核 B-2：专栏转发首节点不该带热评）；字段还在，自定义模板写 $热评 才出。
func TestArticleCommentsOnlyWhenAsked(t *testing.T) {
	r := articleResult()
	r.Comments = av([]linkreader.Comment{{Author: "用户丙", Text: "写得不错", Likes: 7}})
	for _, forward := range []string{"auto", "always"} {
		d := Build(&r, "", Options{Sites: map[string]SiteSetting{"bilibili-article": {Forward: forward}}}, 0)
		if got := strings.Join(d.Describe(), "\n"); strings.Contains(got, "热评") || strings.Contains(got, "写得不错") || strings.Contains(got, "评论不公开") {
			t.Fatalf("默认不该写热评（%s）：\n%s", forward, got)
		}
	}
	d := Build(&r, "", Options{Templates: map[string]string{"bilibili-article": "$标题\n$热评\n$链接"}}, 0)
	if got := strings.Join(d.Describe(), "\n"); !strings.Contains(got, "热评：") || !strings.Contains(got, "1. 用户丙：写得不错（赞 7）") {
		t.Fatalf("自定义模板写 $热评 应该有：\n%s", got)
	}
}

// 短专栏直发（用户 2026-11-13 复核 B-1）：标题、UP主、数据、正文（文字图片按原顺序）、链接在最后，不写“发布：”，同动态。
func TestArticleDirectLayout(t *testing.T) {
	r := articleResult()
	r.Published = av(time.Date(2026, 11, 1, 10, 30, 0, 0, time.UTC))
	d := Build(&r, "", Options{}, 0)
	got := strings.Join(d.Describe(), "\n")
	if strings.Contains(got, "发布：") || !strings.Contains(got, "UP主：纸灯实验室\n11-01 18:30\n阅读 1200") {
		t.Fatalf("时间不带“发布：”：\n%s", got)
	}
	lines := strings.Split(got, "\n")
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "https://") {
		t.Fatalf("链接应在最后：\n%s", got)
	}
	if i, j := strings.Index(got, "第一段"), strings.Index(got, "https://"); i < 0 || j < i {
		t.Fatalf("正文应在链接前面：\n%s", got)
	}
}
