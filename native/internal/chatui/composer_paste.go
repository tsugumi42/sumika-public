package chatui

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/ui"
)

// 大段粘贴折叠成附件（清单 2.3"大段粘贴折叠成附件"，照 Hermes desktop 的 large-paste.ts：超过 3000 字符变附件，
// 小药丸写名字和字数）。折叠后的文字不进输入框，挂在这一块的附件条里，可删除、可点开看全文。
// 发送时拼进这一块的正文（SendText）：后台的文字附件只是占位，模型读不到内容，拼进正文最简单，
// 也保留了顺序（先是用户打的字，再是各段粘贴，按粘贴的先后）。

// FoldPasteRunes 是折叠的门槛：粘贴的文字超过这么多字符（rune，不是字节）才折叠。
const FoldPasteRunes = 3000

// pasteName 是折叠附件的名字，药丸、删除键的读屏名都用它。
const pasteName = "粘贴的内容"

// PasteText 是文字粘贴的唯一入口：输入框里按粘贴和输入框之外按粘贴都走这里。
// 没超过门槛返回 false，调用方照常把文字放进输入框；超过门槛、也挂上了附件返回 true。
// 附件满了或这一块正在发送挂不上时返回 false（原因在 Notice），文字照常放进输入框，不会丢。
func (p *Composer) PasteText(to *Block, text string) bool {
	if utf8.RuneCountInString(text) <= FoldPasteRunes {
		return false
	}
	_, ok := p.Attach(to, Att{Kind: AttText, Name: pasteName, Text: text})
	return ok
}

// PasteInto 是输入框之外粘贴文字：先问 PasteText，没折叠就接在这一块末尾。返回有没有放进去。
func (p *Composer) PasteInto(to *Block, text string) bool {
	if p.PasteText(to, text) {
		return true
	}
	return p.AppendText(to, text)
}

// SendText 是这一块要发出去的正文：用户打的字，后面接各段折叠的粘贴（每段前一行小标题写明字数，让模型知道是粘进来的）。
func (b *Block) SendText() string {
	text := strings.TrimSpace(b.Text)
	for _, a := range b.Atts {
		if a.Kind != AttText {
			continue
		}
		body := strings.TrimSpace(a.Text)
		seg := fmt.Sprintf("【粘贴的内容，%d 字】\n%s", utf8.RuneCountInString(body), body)
		if text == "" {
			text = seg
		} else {
			text += "\n\n" + seg
		}
	}
	return text
}

// PreviewText 同 SendText，但折叠的粘贴只留小标题，不带全文：给待发气泡和回忆历史用，
// 全文进输入框会绕过折叠，铺进气泡也太长。
func (b *Block) PreviewText() string {
	text := strings.TrimSpace(b.Text)
	for _, a := range b.Atts {
		if a.Kind != AttText {
			continue
		}
		seg := fmt.Sprintf("【粘贴的内容，%d 字】", utf8.RuneCountInString(strings.TrimSpace(a.Text)))
		if text == "" {
			text = seg
		} else {
			text += "\n\n" + seg
		}
	}
	return text
}

// textChip 是折叠粘贴的小药丸：图标、名字和字数（点开看全文）、删除键。
func (h ComposerHost) textChip(c *ui.Context, p *Composer, a Att, radius float32) {
	t := h.T
	n := utf8.RuneCountInString(a.Text)
	ui.Row(c.Key(a.ID)).Label(fmt.Sprintf("%s：%d 字", pasteName, n)).Height(t.HSm).Shrink(0).Gap(t.S1).AlignItems(ui.Center).Radius(radius).Background(h.C.Card).Children(func() {
		open := ui.ButtonBase(c.Key("open")).Label(fmt.Sprintf("查看%s，%d 字", pasteName, n)).Height(t.HSm).Padding(0, 0, 0, t.S2).Gap(t.S1).AlignItems(ui.Center).Radius(radius).
			TextColor(h.C.Text).OnClick(func() {
			if h.OnOpenText != nil {
				h.OnOpenText(a)
			}
		})
		open.Background(h.T.Fill(open, h.pressed(open))).Children(func() {
			ui.Icon(c.Key("icon"), Icon("file", false)).Size(t.FS[2], t.FS[2]).Shrink(0).TextColor(h.C.Muted)
			ui.Text(c.Key("name"), fmt.Sprintf("%s · %d 字", pasteName, n)).Font(h.Font).FontSize(t.FS[2]).TextColor(h.C.Text).MaxWidth(attName).SingleLine()
		})
		h.remove(c, p, a, false)
	})
}
