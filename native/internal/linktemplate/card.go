package linktemplate

import (
	"strings"
)

// 合并转发卡片外面的文字（规格-链接解析模板第十四节）：标题 source、两行预览 news、底部摘要 summary、外显 prompt。
// 四项都是一小段模板，占位符各站点通用，所以用一个独立的占位符集合，不绑某个站点。

// 卡片各项的显示长度上限，单位是全角字数（半角字母数字算半个）。
// 保守取的，没有在 QQ 客户端上逐项核实：SnowLuma 不截，QQ 超出会自己省略或折行，截短一点更整齐。
const (
	CardSourceLimit  = 16
	CardNewsLimit    = 18
	CardSummaryLimit = 18
	CardPromptLimit  = 16
	// CardNewsLines 是预览最多几行（QQ 的卡片只放得下两行）
	CardNewsLines = 2
	// NodeNameRunes 是节点显示名最多多少个字（和读侧 cardNodeName 一致）
	NodeNameRunes = 40
)

var cardSet = NewPlainSet(
	txt("title", "纸船试航", "标题", "title"),
	txt("chars", "共 1200 字", "字数", "chars"),
	txt("images", "3 张图", "图片数", "图数", "images"),
	txt("author", "纸灯实验室", "UP主", "作者", "author"),
	txt("site", "B站动态", "站点", "site"),
)

// CardSet 是卡片文字能用的占位符（设置页的按钮用）。
func CardSet() *Set { return cardSet }

// ParseCard 解析卡片某一项的模板；空串是合法的，表示用 QQ 默认。
func ParseCard(src string) (*Template, error) {
	if strings.TrimSpace(src) == "" {
		return &Template{}, nil
	}
	return Parse(src, cardSet)
}

// RenderCard 渲染卡片的一项（不截断：长度上限 CardSourceLimit 等由调用方按 linktrunc.Display 截，截断的写法只有一份）。
// 模板里直接写的占位符（不在 [ ] 里的）有一个没取到或为空，就整项不出（返回空串，调用方用 QQ 默认），不留“未返回”的空壳。
// 换行换成空格：卡片上每项只占一行。
func RenderCard(t *Template, env Env) string {
	if t == nil || len(t.nodes) == 0 {
		return ""
	}
	for _, n := range t.nodes {
		if n.kind == nField && n.field.Kind == KindText {
			if v, ok := env.Text(n.field.ID); !ok || strings.TrimSpace(v) == "" {
				return ""
			}
		}
	}
	var b strings.Builder
	for _, p := range t.Render(env) {
		if p.Kind == PieceText {
			b.WriteString(p.Text)
		}
	}
	return strings.Join(strings.Fields(strings.NewReplacer("\r", " ", "\n", " ").Replace(b.String())), " ")
}

// SampleCardEnv 是设置页预览用的示例数据。
func SampleCardEnv() Env { return sampleEnv{cardSet} }
