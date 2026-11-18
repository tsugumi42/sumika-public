package linkexpand

import (
	"strings"
	"testing"

	"sumika-native/internal/linkreader"
)

// B 站后来补的页面（小店商品、专栏、番剧、课程、音频、合集）：编造的取数结果走一遍默认模板和卡片。
// 样本也交给 style_test 的“每站每样式”检查（biliMoreSamples）。

func workshopResult() linkreader.Result {
	r := siteResult("bilibili", "workshop", "800001")
	r.Title = av("折纸教程合集")
	r.Text = av("这是编的商品介绍。")
	r.Author.Name = av("纸舟的小店")
	r.Tags = av([]string{"折纸", "教程"})
	r.Workshop = &linkreader.WorkshopInfo{
		Price: av("0.10"), OriginalPrice: av("1.00"), Sold: av("2万+"), Status: av("在售"), Delivery: av("线上交付"),
	}
	return withMedia(r, goldenImage(1))
}

func articleResult() linkreader.Result {
	r := siteResult("bilibili", "article", "cv810001")
	r.Title = av("折纸入门笔记")
	r.Author.Name = av("纸灯实验室")
	r.Text = av("第一段：先把纸对折。\n第二段：再折一次。")
	r.Blocks = []linkreader.Block{{Type: "text", Text: "第一段：先把纸对折。"}}
	r = withMedia(r, goldenImage(1))
	r.Blocks = append(r.Blocks, linkreader.Block{Type: "text", Text: "第二段：再折一次。"})
	return withStats(r, map[string]int64{"view": 1200, "like": 52, "comment": 8, "favorite": 31, "coin": 10, "forward": 4})
}

func biliMoreSamples() []linkreader.Result {
	return []linkreader.Result{toyResult(), workshopResult(), articleResult(), bangumiResult(), courseResult(), audioResult(), collectionResult()}
}

func TestBiliWorkshopDefaultText(t *testing.T) {
	got := describe(t, workshopResult())
	want := "（回复原消息）\n[图片]\n折纸教程合集\n店铺：纸舟的小店\n价格：¥0.10（原价 ¥1.00）\n已售：2万+\n状态：在售\n交付：线上交付\n标签：折纸、教程\nhttps://example.invalid/bilibili/800001"
	if got != want {
		t.Fatalf("得到：\n%s\n想要：\n%s", got, want)
	}
}

// 没有优惠价、店铺名、销量时对应行不写，也不写“未返回”。
func TestBiliWorkshopSparse(t *testing.T) {
	r := siteResult("bilibili", "workshop", "800002")
	r.Title = av("只有标题的商品")
	r.Workshop = &linkreader.WorkshopInfo{Price: av("25.99"), OriginalPrice: nr[string](), Sold: nr[string](), Status: nr[string](), Delivery: nr[string]()}
	got := describe(t, r)
	for _, bad := range []string{"未返回", "原价", "已售", "店铺", "状态"} {
		if strings.Contains(got, bad) {
			t.Fatalf("不该出现 %q：\n%s", bad, got)
		}
	}
	if !strings.Contains(got, "价格：¥25.99") {
		t.Fatalf("缺价格：\n%s", got)
	}
}

func TestBiliWorkshopCard(t *testing.T) {
	r := workshopResult()
	d := Build(&r, "", Options{Templates: map[string]string{"bilibili-workshop": "@卡片"}}, 0)
	if len(d.Parts) == 0 || len(d.Parts[0].Body) == 0 || d.Parts[0].Body[0].Card == nil {
		t.Fatalf("没有卡片：%+v", d.Parts)
	}
	c := d.Parts[0].Body[0].Card
	if c.Title != "折纸教程合集" || c.Author != "店铺：纸舟的小店" || c.DateDuration != "价格：¥0.10（原价 ¥1.00）｜状态：在售" || len(c.Stats) != 1 || c.Stats[0] != "已售 2万+" {
		t.Fatalf("%+v", c)
	}
}
