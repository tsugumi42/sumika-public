package linkexpand

import (
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel/types"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

func caseNamed(t *testing.T, name string) linkreader.Result {
	t.Helper()
	for _, c := range goldenCases() {
		if c.name == name {
			return c.build(t)
		}
	}
	t.Fatalf("没有样本 %s", name)
	return linkreader.Result{}
}

func build(r linkreader.Result, o Options) Draft {
	return Build(&r, "-71", o, 0)
}

func kinds(d Draft) []string {
	var out []string
	for _, p := range d.Parts {
		out = append(out, p.Kind)
	}
	return out
}

func intp(n int) *int { return &n }

// 合并转发的三种设置：always 总是转发，never 总是直发，auto 按代码规则；forwardOver 控制 X 超过多少字折成转发
func TestForwardSettingsDecideShape(t *testing.T) {
	short := caseNamed(t, "x-short-image")
	long := caseNamed(t, "x-long")
	over := caseNamed(t, "x-over300")
	cases := []struct {
		name string
		r    linkreader.Result
		set  SiteSetting
		want string
	}{
		{"短帖 auto 直发", short, SiteSetting{}, "message"},
		{"短帖 always 转发", short, SiteSetting{Forward: "always"}, "forward"},
		{"长帖 auto 转发", long, SiteSetting{}, "forward"},
		{"长帖 never 直发", long, SiteSetting{Forward: "never"}, "message"},
		{"301 个汉字按实发字节仍直发", over, SiteSetting{}, "message"},
		{"301 个汉字把门槛调到 100 字节转发", over, SiteSetting{ForwardOverBytes: intp(100)}, "forward"},
		{"门槛 0 不按字节折", over, SiteSetting{ForwardOverBytes: intp(0)}, "message"},
		{"短帖门槛调到 50 字节转发", short, SiteSetting{ForwardOverBytes: intp(50)}, "forward"},
	}
	for _, c := range cases {
		d := build(c.r, Options{Sites: map[string]SiteSetting{"x": c.set}})
		if got := strings.Join(kinds(d), ","); got != c.want {
			t.Errorf("%s：%s，应为 %s", c.name, got, c.want)
		}
	}
}

// 简介截断字数：pixiv 小说默认 300，可以改，0 不截。默认模板不写简介了（用户 2026-11-12），字段还在：自定义模板写上就照截断字数出
func TestDescLimit(t *testing.T) {
	r := caseNamed(t, "pixiv-novel")
	if d := build(r, Options{}); strings.Contains(d.Semantic(), "简简简") {
		s := d.Semantic()
		t.Fatalf("默认模板不该带简介：%q", s)
	}
	text := func(o Options) string {
		o.Templates = map[string]string{"pixiv-novel": "$标题\n简介：$简介\n$链接", "pixiv-novel-forward": "$标题\n简介：$简介\n$链接"}
		d := build(r, o)
		return d.Semantic()
	}
	if s := text(Options{}); !strings.Contains(s, strings.Repeat("简", 300)+"……\n完整内容见原文") || strings.Contains(s, strings.Repeat("简", 301)) {
		t.Fatal("默认应截到 300 字")
	}
	if s := text(Options{Sites: map[string]SiteSetting{"pixiv-novel": {DescLimit: intp(10)}}}); !strings.Contains(s, strings.Repeat("简", 10)+"……\n完整内容见原文") {
		t.Fatal("改成 10 字没生效")
	}
	if s := text(Options{Sites: map[string]SiteSetting{"pixiv-novel": {DescLimit: intp(0)}}}); !strings.Contains(s, strings.Repeat("简", 320)) || strings.Contains(s, "完整内容见原文") {
		t.Fatal("0 应当不截断")
	}
}

// 图片的位置由模板决定：在前、在后、单独一条消息
func TestTemplateDecidesImagePlacement(t *testing.T) {
	r := goldenBili(1)
	order := func(tpl string) string {
		d := build(r, Options{Templates: map[string]string{"bilibili-video": tpl}})
		var out []string
		for i, p := range d.Parts {
			for _, it := range p.Body {
				if it.Kind == types.BodyText {
					out = append(out, "字")
				} else {
					out = append(out, it.Kind)
				}
			}
			if i < len(d.Parts)-1 {
				out = append(out, "|")
			}
		}
		return strings.Join(out, " ")
	}
	for tpl, want := range map[string]string{
		"$封面$标题":          "image 字",
		"$标题$封面":          "字 image",
		"$标题$分段$封面":       "字 | image",
		"$封面$分段$标题$分段$链接": "image | 字 | 字",
		"$卡片$链接":          "card 字",
	} {
		if got := order(tpl); got != want {
			t.Errorf("模板 %q：%s，应为 %s", tpl, got, want)
		}
	}
	// 引用只挂在第一条消息上
	d := build(r, Options{Templates: map[string]string{"bilibili-video": "$标题$分段$链接"}})
	if len(d.Parts) != 2 || d.Parts[0].Quote != "-71" || d.Parts[1].Quote != "" {
		t.Fatalf("引用的位置：%+v", d.Parts)
	}
}

// 可选段：缺字段的整段连同分隔符一起省略；裸占位符缺字段写“未返回”
func TestTemplateOptionalSections(t *testing.T) {
	r := goldenBili(1)
	r.Stats = map[string]linkreader.Field[int64]{"view": av(int64(7))}
	d := build(r, Options{Templates: map[string]string{"bilibili-video": "$标题[ · 播放 $播放][ · 点赞 $点赞]\n投币 $投币"}})
	if got := (&d).Semantic(); !strings.Contains(got, "纸船试航 · 播放 7\n") || strings.Contains(got, "点赞") || !strings.Contains(got, "投币 未返回") {
		t.Fatalf("%q", got)
	}
}

// 写错的模板（比如升级后字段没了）不会发空消息：退回内置默认
func TestBrokenTemplateFallsBackToBuiltin(t *testing.T) {
	r := caseNamed(t, "github")
	base := build(r, Options{})
	want := base.Semantic()
	for _, bad := range []string{"$不存在", "[没配对", "@不存在的预设", `\x`} {
		dd := build(r, Options{Templates: map[string]string{"github": bad}})
		got := dd.Semantic()
		if got != want {
			t.Errorf("坏模板 %q 没有退回默认：%q", bad, got)
		}
	}
}

// 转发的节点上限、按字节拆节点不受模板影响：自定义转发模板也封顶 80 节点
func TestForwardCapsHoldWithCustomTemplate(t *testing.T) {
	r := caseNamed(t, "x-long")
	for i := 0; i < 120; i++ {
		r.Blocks = append(r.Blocks, linkreader.Block{Type: "image", Media: new(goldenImage(i))})
	}
	d := build(r, Options{Templates: map[string]string{"x-forward": "说明$合并转发$作者$节点$图片$节点$链接"}})
	last := d.Parts[len(d.Parts)-1]
	if last.Kind != types.PartForward || len(last.Nodes) != 80 || !strings.Contains(last.Nodes[79].Body[0].Text, "后面还有") {
		t.Fatalf("节点数 %d：%+v", len(last.Nodes), last.Kind)
	}
	for _, n := range last.Nodes {
		for _, it := range n.Body {
			if it.Kind == types.BodyText && len(it.Text) > linktemplate.DefaultForwardOverBytes {
				t.Fatal("文字节点超过字节上限")
			}
		}
	}
}

// B 站视频的内置默认和 @封面文字 是同一份排版：封面、标题、空行、UP主｜时长、投稿时间、空行、链接；没有站点前缀，统计和热评不写。
func TestCoverTextPresetIsChosenLayout(t *testing.T) {
	r := goldenBili(1)
	want := "纸船试航\n纸灯实验室 | 02:15\n播放 1200 | 点赞 52\n" + shortStamp(r.Published).text + "\nhttps://example.invalid/bilibili/BV1xx411c7mD"
	for name, o := range map[string]Options{"默认": {}, "@封面文字": {Templates: map[string]string{"bilibili-video": "@封面文字"}}, "@图文": {Templates: map[string]string{"bilibili-video": "@图文"}}} {
		d := build(r, o)
		body := d.Parts[0].Body
		if len(body) != 2 || body[0].Kind != types.BodyImage || body[1].Text != want {
			t.Fatalf("%s：%+v", name, body)
		}
	}
}

// 取不到的字段不显示：默认模板里每个统计项、每一行都是可选段，缺了连同分隔符一起省略，不写“未返回”
func TestDefaultTemplatesOmitMissingFields(t *testing.T) {
	r := goldenBili(1)
	r.Stats = map[string]linkreader.Field[int64]{"favorite": av(int64(31)), "coin": av(int64(10))}
	r.Published = linkreader.Field[time.Time]{State: "not_returned"}
	d := build(r, Options{Templates: map[string]string{"bilibili-video": biliTextWithHot}})
	got := d.Semantic()
	for _, bad := range []string{"未返回", "播放", "转发", "评论", "弹幕", "点赞", "上传"} {
		if strings.Contains(got, bad) {
			t.Errorf("不该出现 %q：%q", bad, got)
		}
	}
	for _, want := range []string{"收藏 31｜投币 10\n", "UP主：纸灯实验室｜02:15\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺 %q：%q", want, got)
		}
	}
	if strings.Contains(got, "｜\n") || strings.Contains(got, "\n｜") || strings.Contains(got, "\n\n\n") {
		t.Errorf("留下了孤立的分隔符或空行：%q", got)
	}
	// 卡片的统计行也一样
	card := build(r, Options{Templates: map[string]string{"bilibili-video": "@卡片"}})
	c := card.Parts[0].Body[0].Card
	if c == nil || len(c.Stats) != 2 || strings.Contains(strings.Join(c.Stats, ""), "未返回") || c.DateDuration != "时长：02:15" {
		t.Fatalf("卡片：%+v", c)
	}
	// X 的统计一个都没有时，整行省略
	x := caseNamed(t, "x-short-nostats")
	if s := func() string { dd := build(x, Options{}); return dd.Semantic() }(); strings.Contains(s, "未返回") || strings.Contains(s, "点赞") {
		t.Errorf("X：%q", s)
	}
}

// 简介截断字数对所有站点的“简介”字段都生效，不只 pixiv 小说
func TestDescLimitAppliesToOtherSites(t *testing.T) {
	r := caseNamed(t, "github")
	r.Text = av(strings.Repeat("说", 50))
	d := build(r, Options{Sites: map[string]SiteSetting{"github": {DescLimit: intp(5)}}, Templates: map[string]string{"github": "$标题\n说明：$简介\n$链接"}})
	if s := d.Semantic(); !strings.Contains(s, "说明："+strings.Repeat("说", 5)+"……\n完整内容见原文") || strings.Contains(s, strings.Repeat("说", 6)) {
		t.Fatalf("%q", s)
	}
}
