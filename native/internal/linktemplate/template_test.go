package linktemplate

import (
	"errors"
	"strings"
	"testing"
)

type fakeEnv struct {
	text  map[string]string // 没有的键 = 没取到；值为空 = 取到空值
	slots map[string]bool
}

func (e fakeEnv) Text(id string) (string, bool) { v, ok := e.text[id]; return v, ok }
func (e fakeEnv) Slot(id string) bool           { return e.slots[id] }

func testSet() *Set {
	return NewSet(
		Field{ID: "title", Names: []string{"标题", "title"}},
		Field{ID: "views", Names: []string{"播放", "views"}},
		Field{ID: "likes", Names: []string{"点赞", "likes"}},
		Field{ID: "author", Names: []string{"UP主", "作者", "author"}},
		Field{ID: "url", Names: []string{"链接", "url"}},
		Field{ID: "cover", Names: []string{"封面", "cover"}, Kind: KindSlot},
		Field{ID: "body", Names: []string{"正文", "body"}, Kind: KindSlot},
	)
}

func render(t *testing.T, src string, env fakeEnv) string {
	t.Helper()
	tpl, err := Parse(src, testSet())
	if err != nil {
		t.Fatalf("解析 %q 失败：%v", src, err)
	}
	return Describe(tpl.Render(env), func(id string) string { return id })
}

func TestRenderTable(t *testing.T) {
	env := fakeEnv{text: map[string]string{"title": "纸船试航", "views": "1200", "likes": "52", "author": "纸灯实验室", "url": "https://example.invalid/v"},
		slots: map[string]bool{"cover": true}}
	missing := fakeEnv{text: map[string]string{"title": "纸船试航", "likes": "", "url": "https://example.invalid/v"}}
	cases := []struct {
		name, src string
		env       fakeEnv
		want      string
	}{
		{"中文名", "$标题｜$作者", env, "纸船试航｜纸灯实验室"},
		{"英文名不分大小写", "$Title|$AUTHOR", env, "纸船试航|纸灯实验室"},
		{"别名都能用", "$UP主=$作者", env, "纸灯实验室=纸灯实验室"},
		{"花括号隔开后面的字", "${标题}是好看的", env, "纸船试航是好看的"},
		{"最长匹配不吞后面的字", "$标题是好看的", env, "纸船试航是好看的"},
		{"名字后接数字照样认", "$标题1", env, "纸船试航1"},
		{"真实换行", "$标题\n$链接", env, "纸船试航\nhttps://example.invalid/v"},
		{"反斜杠n换行", "$标题\\n$链接", env, "纸船试航\nhttps://example.invalid/v"},
		{"转义", `价格 \$5 \[限\] \\`, env, `价格 $5 [限] \`},
		{"可选段有值", "播放 $播放[｜点赞 $点赞]", env, "播放 1200｜点赞 52"},
		{"可选段字段没取到整段省略", "$标题[｜播放 $播放]", missing, "纸船试航"},
		{"可选段字段为空整段省略", "$标题[｜点赞 $点赞]", missing, "纸船试航"},
		{"可选段里任一为空都省略", "$标题[ $播放/$点赞]", env, "纸船试航 1200/52"},
		{"嵌套可选段各管各的：只含子段的外层，子段都省略时整个省略", "$标题[ [($播放)][($点赞)]]", missing, "纸船试航"},
		{"子段有一个出现外层就输出", "$标题[ [($播放)][($点赞)]]", fakeEnv{text: map[string]string{"title": "甲", "likes": "3"}}, "甲 (3)"},
		{"整行统计缺了第一项：行首分隔符去掉", "$标题[\n[播放 $播放][｜点赞 $点赞]]", fakeEnv{text: map[string]string{"title": "甲", "likes": "3"}}, "甲\n点赞 3"},
		{"整行统计全缺：整行连换行省略", "$标题[\n[播放 $播放][｜点赞 $点赞]]\n$链接", missing, "纸船试航\nhttps://example.invalid/v"},
		{"嵌套里外层有值内层空", "$标题[ $作者[ ($点赞)]]", fakeEnv{text: map[string]string{"title": "甲", "author": "乙", "likes": ""}}, "甲 乙"},
		{"裸占位符没取到写未返回", "播放 $播放", missing, "播放 未返回"},
		{"裸占位符取到空值输出空", "点赞：$点赞", missing, "点赞："},
		{"槽位单独成行", "$封面\n$标题", env, "[cover]\n纸船试航"},
		{"槽位两侧的换行被去掉", "头\n$封面\n尾", env, "头\n[cover]\n尾"},
		{"可选段里的槽位没内容整段省略", "$标题[\n$正文]", env, "纸船试航"},
		{"可选段里的槽位有内容", "$标题[\n$封面]", env, "纸船试航\n[cover]"},
		{"分段合并转发节点", "甲$分段乙$合并转发丙$节点丁", env, "甲\n──── 另起一条消息 ────\n乙\n──── 以下进合并转发 ────\n丙\n──── 下一个节点 ────\n丁"},
		{"开头结尾的换行去掉", "\n\n$标题\n\n", env, "纸船试航"},
		{"空模板", "", env, ""},
		{"只有字面", "你好，世界", env, "你好，世界"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := render(t, c.src, c.env); got != c.want {
				t.Fatalf("模板 %q\n得到 %q\n应为 %q", c.src, got, c.want)
			}
		})
	}
}

func TestParseErrorsAreFriendlyAndNeverPanic(t *testing.T) {
	cases := []struct {
		name, src string
		pos       int
		contains  string
	}{
		{"未知占位符", "$不存在", 1, "不认识 $不存在"},
		{"错别字给建议", "开头 $播发", 4, "不认识"},
		{"英文拼错", "$titel", 1, "不认识 $titel"},
		{"孤零零的美元符", "价格 $", 4, "$ 后面要接字段名"},
		{"美元符后是空格", "价格 $ 5", 4, "$ 后面要接字段名"},
		{"方括号没配对", "$标题[｜$播放", 4, "没有配对的 ]"},
		{"多余的右括号", "$标题]", 4, "多出一个 ]"},
		{"花括号没配对", "${标题", 1, "没有配对的 }"},
		{"花括号里未知", "${不存在}", 1, "不认识 $不存在"},
		{"未知转义", `甲\x乙`, 2, "不认识的转义"},
		{"结尾反斜杠", `甲\`, 2, "后面没有字"},
		{"嵌套太深", strings.Repeat("[", 9) + strings.Repeat("]", 9), 9, "嵌套太深"},
		{"太长", strings.Repeat("字", MaxRunes+1), MaxRunes + 1, "太长"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(c.src, testSet())
			var pe *ParseError
			if !errors.As(err, &pe) {
				t.Fatalf("应当返回 *ParseError：%v", err)
			}
			if !strings.Contains(pe.Error(), c.contains) {
				t.Fatalf("错误文字缺 %q：%s", c.contains, pe.Error())
			}
			if pe.Pos != c.pos {
				t.Fatalf("位置 %d，应为 %d：%s", pe.Pos, c.pos, pe.Error())
			}
		})
	}
	t.Run("建议里有最像的名字", func(t *testing.T) {
		_, err := Parse("$标", testSet())
		// “标”不是名字，但是“标题”的前缀：建议写 $标题
		if err == nil || !strings.Contains(err.Error(), "$标题") {
			t.Fatalf("应当建议 $标题：%v", err)
		}
	})
}

// 乱写的模板不能崩：随机拼一些容易出事的字符
func TestParseDoesNotPanicOnGarbage(t *testing.T) {
	pieces := []string{"$", "[", "]", "\\", "{", "}", "标", "题", "$标题", "\n", "${", "播放", "$封面", "字"}
	for a := range pieces {
		for b := range pieces {
			for c := range pieces {
				src := pieces[a] + pieces[b] + pieces[c]
				tpl, err := Parse(src, testSet())
				if err == nil {
					tpl.Render(fakeEnv{})
				}
			}
		}
	}
}

// 渲染两遍结果一致（解析结果可以并发复用，不带状态）
func TestRenderIsRepeatable(t *testing.T) {
	tpl, err := Parse("$标题[｜$播放]\n$链接", testSet())
	if err != nil {
		t.Fatal(err)
	}
	env := fakeEnv{text: map[string]string{"title": "甲", "views": "3", "url": "u"}}
	a, b := Describe(tpl.Render(env), nil), Describe(tpl.Render(env), nil)
	if a != b || a != "甲｜3\nu" {
		t.Fatalf("%q %q", a, b)
	}
}

func TestDuplicateNamePanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("名字重复应当 panic（站点表的编写错误）")
		}
	}()
	NewSet(Field{ID: "a", Names: []string{"标题"}}, Field{ID: "b", Names: []string{"标题"}})
}

// 分隔符写在可选段里：前面的段整段省略后落在行首的字面分隔符去掉，字段的值不动
func TestLeadingSeparatorsAreDropped(t *testing.T) {
	env := fakeEnv{text: map[string]string{"likes": "52", "title": "｜标题自带竖线", "url": "u"}}
	cases := []struct{ name, src, want string }{
		{"前面省略后分隔符不落在行首", "[播放 $播放][｜点赞 $点赞]", "点赞 52"},
		{"整行写成可选段，缺了这行消失", "$标题[\n[播放 $播放][｜点赞 $点赞]]\n$链接", "｜标题自带竖线\n点赞 52\nu"},
		{"中间的分隔符保留", "[播放 $播放][｜点赞 $点赞]", "点赞 52"},
		{"字段的值开头的竖线不动", "$标题", "｜标题自带竖线"},
		{"槽位后的行首也算", "$封面[｜$点赞]", "[cover]\n52"},
	}
	for _, c := range cases {
		if got := render(t, c.src, env); got != c.want {
			t.Errorf("%s：%q 得到 %q，应为 %q", c.name, c.src, got, c.want)
		}
	}
}
