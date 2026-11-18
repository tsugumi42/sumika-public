package linkreader

import "testing"

// 各站原始标记换成纯文字；每条都验证“清两遍 = 清一遍”。
func TestMarkupPlain(t *testing.T) {
	cases := []struct {
		name string
		fn   func(string) string
		in   string
		want string
	}{
		{"bbcode", bbcodePlain,
			"[h1]编的版本说明[/h1]\r\n适配 [i]编的游戏 2[/i]，[b]日记[/b]见下\r\n[img]https://example.com/a.png[/img]\r\n[url=https://example.com/x]编的主页[/url]\r\n[list][*]第一条[*]第二条[/list]\r\n\r\n\r\n\r\n结尾 [img]https://example.com/cut",
			"编的版本说明\n适配 编的游戏 2，日记见下\n\n编的主页 https://example.com/x\n\n· 第一条\n· 第二条\n\n结尾"},
		// 标题收尾后原文本来就换行：只留一个换行，不多出空行；没换行时补一个
		{"bbcode-heading-newline", bbcodePlain, "[h1]编的标题[/h1]\nThis is it[p]一段[/p][p]二段[/p]", "编的标题\nThis is it一段\n二段"},
		{"bbcode-heading-inline", bbcodePlain, "[h2]编的标题[/h2]正文", "编的标题\n正文"},
		{"bbcode-bare-url", bbcodePlain, "看 [url]https://example.com/y[/url] [u]下划线[/u]", "看 https://example.com/y 下划线"},
		{"mfm", misskeyPlain, "🎉 **99**: $[jelly @paper_cloud] $[x2 $[spin.speed=2s 转]] <center>居中</center>", "🎉 99: @paper_cloud 转 居中"},
		{"markdown-link", misskeyPlain, "[编的网站](https://xn--hn0at5e.example/) | [说明书](https://example.com/doc) ?[静默](https://example.com/s)",
			"编的网站 https://纸船.example/ | 说明书 https://example.com/doc 静默 https://example.com/s"},
		{"markdown-same", markdownPlain, "[https://example.com/a](https://example.com/a)", "https://example.com/a"},
		{"bold", markdownPlain, "**《编的游戏》**是一款~~不~~好玩的游戏，__init__ 不动", "《编的游戏》是一款不好玩的游戏，__init__ 不动"},
	}
	for _, c := range cases {
		got := c.fn(c.in)
		if got != c.want {
			t.Errorf("%s：\n得到 %q\n应为 %q", c.name, got, c.want)
		}
		if again := c.fn(got); again != got {
			t.Errorf("%s 清两遍不一样：%q", c.name, again)
		}
	}
}

// 简介里的 \r\n、单独的 \r 换成 \n（Steam 礼包的简介原样带着 \r）。
func TestPreviewFieldNormalizesCR(t *testing.T) {
	s := "编的物品掉落。\r\n\r\n此礼包含\r两款"
	if got := previewField(&s).Value; got != "编的物品掉落。\n\n此礼包含\n两款" {
		t.Fatalf("%q", got)
	}
}

// Bluesky 标题取正文开头：截断时退到空格、加省略号，不截在词中间；中文按字数截
func TestBskyHeadCutsAtWord(t *testing.T) {
	for _, c := range []struct {
		in   string
		n    int
		want string
	}{
		{"Thanks to all the paper boat builders who joined", 18, "Thanks to all the…"},
		{"short", 18, "short"},
		{"纸船纸船纸船纸船纸船", 4, "纸船纸船…"},
		{"averyveryverylongwordwithoutspaces", 10, "averyveryv…"},
	} {
		if got := bskyHead(c.in, c.n); got != c.want {
			t.Errorf("%q：%q，应为 %q", c.in, got, c.want)
		}
	}
}
