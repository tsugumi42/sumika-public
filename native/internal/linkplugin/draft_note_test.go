package linkplugin

// 2026-11-12 复核链接发送改版时找到的装配问题：转发段的“还有 N 条没处理”被丢、两个排版配置键没传进 linkexpand。
// 链接、正文都是编的；出口是假的，不碰协议端

import (
	"context"
	"strings"
	"testing"
	"time"

	"sumika-native/internal/channel"
	"sumika-native/internal/channel/types"
	"sumika-native/internal/config"
	"sumika-native/internal/linkexpand"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/linktemplate"
)

// draftCapture 是会走到发送的展开选项，把交给 Prepare 的草稿存下来（浅拷贝：Prepare 之后插件不再改它，测试只读）
func draftCapture() (*ExpandOptions, <-chan linkexpand.Draft) {
	ch := make(chan linkexpand.Draft, 8)
	e := sendingExpand(confirmed, nil)
	e.Prepare = func(_ context.Context, _ channel.Stored, _ Snapshot, _ *linkreader.Result, d *linkexpand.Draft) (linkexpand.Prepared, error) {
		ch <- *d
		return linkexpand.Prepared{Parts: []types.Part{{Kind: types.PartMessage, Body: types.TextBody("编的展开")}}}, nil
	}
	return e, ch
}

func nextDraft(t *testing.T, ch <-chan linkexpand.Draft) linkexpand.Draft {
	t.Helper()
	select {
	case d := <-ch:
		return d
	case <-time.After(10 * time.Second):
		t.Fatal("等不到准备结果")
		return linkexpand.Draft{}
	}
}

func itemsText(items []linkexpand.Item) string {
	var b strings.Builder
	for _, it := range items {
		if it.Kind == types.BodyText {
			b.WriteString(it.Text)
		}
	}
	return b.String()
}

// bug（expand.go 把“还有 N 条没处理”追加到 Parts[0].Body）：内置转发模板不再有前导句后，第一段就是合并转发，
// 转发段只发 Nodes，这句话静默丢了。改后接在第一个节点里。放回旧代码就红：第一个节点里没有这句、Body 里有
func TestExcessNoteGoesIntoFirstForwardNode(t *testing.T) {
	t.Parallel()
	exp, drafts := draftCapture()
	set := expandSettings(func(e *config.LinkExpand) {
		e.MaxLinks = 1
		e.Sites = map[string]config.LinkSite{"x": {Forward: linktemplate.ForwardAlways}}
		e.TweetCard.TextOnly = false // 假取数给的是纯文字推文，这里要的是文字版（ADR-399 的卡片只直发、不带正文）
	})
	r, _ := newTraceRig(t, &fakeFetcher{}, Options{Settings: set, Expand: exp})
	r.p.OnStored(storedMsg(31, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/3101 https://x.com/a/status/3102"}}))
	d := nextDraft(t, drafts)
	r.idle()
	if len(d.Parts) == 0 || d.Parts[0].Kind != types.PartForward || len(d.Parts[0].Nodes) == 0 {
		t.Fatalf("x 设成总走转发，第一段应当是合并转发：%+v", d.Parts)
	}
	if got := itemsText(d.Parts[0].Nodes[0].Body); !strings.Contains(got, "还有 1 条没处理") {
		t.Fatalf("“还有 1 条没处理”应当在第一个节点里：%q", got)
	}
	if len(d.Parts[0].Body) != 0 {
		t.Fatalf("转发段的 Body 不会发出去，不该往里写：%+v", d.Parts[0].Body)
	}
}

// 第一段是文字消息时照旧接在它后面
func TestExcessNoteStaysOnFirstMessage(t *testing.T) {
	t.Parallel()
	exp, drafts := draftCapture()
	set := expandSettings(func(e *config.LinkExpand) {
		e.MaxLinks = 1
		e.Sites = map[string]config.LinkSite{"x": {Forward: linktemplate.ForwardNever}}
	})
	r, _ := newTraceRig(t, &fakeFetcher{}, Options{Settings: set, Expand: exp})
	r.p.OnStored(storedMsg(32, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/3201 https://x.com/a/status/3202"}}))
	d := nextDraft(t, drafts)
	r.idle()
	if len(d.Parts) == 0 || d.Parts[0].Kind != types.PartMessage || !strings.Contains(itemsText(d.Parts[0].Body), "还有 1 条没处理") {
		t.Fatalf("x 设成总是直发，这句接在第一条文字后面：%+v", d.Parts)
	}
}

// bug（expand.go、app/linkpreview.go 装配 linkexpand.Options 时漏了两个键）：directImageMax、text.stripTrailingHashtags
// 写进了配置和参考页，改了却不生效。放回旧代码就红：关了去 hashtag，结尾的 #标签 照样被去掉
func TestTextSettingsReachDraft(t *testing.T) {
	t.Parallel()
	exp, drafts := draftCapture()
	set := expandSettings(func(e *config.LinkExpand) {
		e.Text.StripTrailingHashtags = false
		e.Sites = map[string]config.LinkSite{"x": {Forward: linktemplate.ForwardNever}}
		e.TweetCard.TextOnly = false // 假取数给的是纯文字推文，这里要的是文字版（ADR-399 的卡片只直发、不带正文）
	})
	r, _ := newTraceRig(t, &fakeFetcher{text: "编的正文 #编的标签"}, Options{Settings: set, Expand: exp})
	r.p.OnStored(storedMsg(33, []types.Seg{{Kind: types.SegText, Text: "https://x.com/a/status/3301"}}))
	d := nextDraft(t, drafts)
	r.idle()
	if len(d.Parts) == 0 || !strings.Contains(itemsText(d.Parts[0].Body), "#编的标签") {
		t.Fatalf("关了去 hashtag，结尾的标签应当留着：%+v", d.Parts)
	}
}

// BuildOptions 把两个排版键都带上（预览也走它）；默认配置对应 linkexpand 的默认
func TestBuildOptionsCarriesTextSettings(t *testing.T) {
	e := config.DefaultLinks().Expand
	if o := BuildOptions(e); o.KeepTrailingHashtags || o.DirectImageMax != config.DefaultDirectImageMax {
		t.Fatalf("默认配置应当去 hashtag、直发 %d 张：%+v", config.DefaultDirectImageMax, o)
	}
	e.DirectImageMax, e.Text.StripTrailingHashtags = 2, false
	e.Templates = map[string]string{"x": "$链接"}
	o := BuildOptions(e)
	if !o.KeepTrailingHashtags || o.DirectImageMax != 2 {
		t.Fatalf("改过的两个键应当传进去：%+v", o)
	}
	o.Templates["x"] = "改过"
	if e.Templates["x"] != "$链接" {
		t.Fatal("预览改模板表不能改到配置里那份")
	}
}
