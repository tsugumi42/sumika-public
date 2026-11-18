package chatui

import (
	"github.com/egoist/mygo/ui"

	"sumika-native/internal/assets/emoji"
)

// EmojiDir 是 Twemoji SVG 的目录（文件名是码位，比如 1f44d.svg；CC-BY 4.0，jdecked/twemoji，ADR-150）。
// 宿主启动时设（找目录用 assets.Twemoji）；空或找不到对应文件时退回用文字画。
// 照 Telegram：表情一律当图片画进正方形框（history_view_reactions.cpp:465、569-581；正文 emoji 也用自带图集），
// 不用系统 emoji 字体——字体的字形在文字框里有各自的偏移，框居中了图形也是歪的。
var EmojiDir string

// EmojiSVG 返回表情的图；没有设目录、没有这张图或解析失败时返回 nil。
// 切序列、按序列找文件名、读图和缓存都在公共的表情服务（assets/emoji）里，和推特卡片是同一份。
func EmojiSVG(s string) *ui.SVG {
	if EmojiDir == "" || s == "" {
		return nil
	}
	return emoji.For(EmojiDir).SVG(s)
}

// Emoji 在边长 size 的正方形框里画一个表情：有图就画图（填满框），没图退回文字、按量出的文字框中心对中心摆。
func (h Host) Emoji(c *ui.Context, key any, s string, size float32) ui.Element {
	box := ui.Box(c.Key(key)).Size(size, size).Shrink(0)
	box.Children(func() {
		if svg := EmojiSVG(s); svg != nil {
			ui.Image(c.Key("img"), svg).Size(size, size)
			return
		}
		fs := round1(size * 0.8)
		aw, _ := c.MeasureText(1e6, ui.Span{Text: s, Size: fs, Font: h.Font})
		if aw > size { // 字比框宽就按比例缩小，不越出框
			fs, aw = round1(fs*size/aw), size
		}
		// 行高固定成框的边长：文字框正好装进框里，不越出去（字体的行高比框高时会越出 1 像素，几何检查查得出）
		ui.Text(c.Key("txt"), s).Font(h.Font).FontSize(fs).FixedLineHeight(size).Height(size).SingleLine().Absolute().Left((size - aw) / 2).Top(0)
	})
	return box
}
