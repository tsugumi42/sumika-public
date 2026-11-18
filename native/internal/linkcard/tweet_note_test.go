package linkcard

import (
	"context"
	"strings"
	"testing"
)

// 有译文：Text 是译文，正文下面一行小字“翻译自……”（小一号、次要色，位置在正文和图之间）；没有译文不画这一行。
// 封顶和不封顶、浅色和深色都要对（用户 2026-11-13）：量位置，不靠肉眼。
func TestTweetCardTranslationNote(t *testing.T) {
	for _, cap := range []int{0, 960} {
		for _, theme := range []string{ThemeLight, ThemeDark} {
			r := testRenderer(t, Options{MaxHeight: func() int { return cap }, Theme: func() string { return theme }})
			tw := sampleTweet()
			tw.Text = "这是译文第一行。\n第二行也是译文。"
			tw.Media = []TweetMedia{{Image: swatch(300, 200, sky, leaf)}}
			plain, err := r.RenderTweet(context.Background(), tw)
			if err != nil {
				t.Fatal(err)
			}
			if f := fieldNamed(plain, "translation-note"); f != nil {
				t.Fatalf("没有译文不该有标记：%+v", f)
			}
			tw.TranslationNote = "翻译自日语"
			out, err := r.RenderTweet(context.Background(), tw)
			if err != nil {
				t.Fatal(err)
			}
			body, note, media := fieldOf(t, out, "body"), fieldOf(t, out, "translation-note"), fieldOf(t, out, "media")
			if note.Text != "翻译自日语" || note.Y < body.Y+body.H || note.Y+note.H > media.Y {
				t.Errorf("cap=%d %s：标记该在正文和图之间：正文 %+v 标记 %+v 图 %+v", cap, theme, body, note, media)
			}
			if note.H >= body.H/2 || note.H <= 0 {
				t.Errorf("标记该比正文小一号（一行）：标记高 %d，正文高 %d", note.H, body.H)
			}
			if out.Height <= plain.Height {
				t.Errorf("多一行，卡片该更高：%d → %d", plain.Height, out.Height)
			}
			if !strings.Contains(tw.Semantic(), "翻译自日语") {
				t.Errorf("标记也是画上去的字，要过守卫：%q", tw.Semantic())
			}
			// 长文（Flow）按段落排，不换译文也不画标记
			tw.Flow = []FlowBlock{{Kind: FlowPara, Text: "原文一段"}}
			flowOut, err := r.RenderTweet(context.Background(), tw)
			if cap == 0 && err == nil && fieldNamed(flowOut, "translation-note") != nil {
				t.Errorf("Flow 不画标记")
			}
			if strings.Contains(tw.Semantic(), "翻译自日语") {
				t.Errorf("Flow 不画标记，守卫里也不该有：%q", tw.Semantic())
			}
		}
	}
}

func fieldNamed(out Output, name string) *FieldLayout {
	for i := range out.Layout {
		if out.Layout[i].Name == name {
			return &out.Layout[i]
		}
	}
	return nil
}
