// Package emoji 是软件自带的表情服务：把文字切成“普通字 / 表情序列”，再按序列取 Twemoji 的 SVG。
// 图是社区版 Twemoji（jdecked/twemoji，图形 CC-BY 4.0）的 SVG，目录里文件名是码位（1f44d.svg）；目录由 scripts/update-twemoji.ts 下载到
// native/twemoji/（不进 git，现在是 v17.0.3），正式程序从程序旁的 twemoji/ 读，找目录的办法在上一层 assets 包。
// 这是底层包：chatui 的回应表情、linkcard 的推特卡片、将来给插件的接口都从这里取，不各写一份。
package emoji

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/egoist/mygo/ui"

	"sumika-native/internal/textclean"
)

// Piece 是 Split 切出来的一段：普通字，或一个表情序列。
type Piece struct {
	Text  string
	Emoji bool
}

func regional(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }
func skinTone(r rune) bool { return r >= 0x1F3FB && r <= 0x1F3FF }
func emojiTag(r rune) bool { return r >= 0xE0020 && r <= 0xE007F }

// Split 把字切成普通字和表情序列，拼起来是原文。表情的开头：两个连着的地区指示符（旗帜）、[0-9#*] 加 20E3 的键帽、
// 默认彩色的字（Emoji_Presentation，✨😀）、后面跟 VS16 的符号（㊗️❤️）；不跟 VS16 的 ㊗、❤ 仍按字画。
// 开头之后吃掉 VS16、肤色、20E3、标签符（英格兰旗这类），遇到零宽连接符连同下一个字一起吃（👨‍👩‍👧）。
func Split(s string) []Piece {
	rs := []rune(s)
	var out []Piece
	var text []rune
	flush := func() {
		if len(text) > 0 {
			out = append(out, Piece{Text: string(text)})
			text = text[:0]
		}
	}
	at := func(i int) rune {
		if i < len(rs) {
			return rs[i]
		}
		return 0
	}
	for i := 0; i < len(rs); {
		r := rs[i]
		end := 0 // 表情开头占到哪；0 不是表情
		switch {
		case regional(r) && regional(at(i+1)):
			end = i + 2
		case (r >= '0' && r <= '9' || r == '#' || r == '*') && at(i+1) == 0x20E3:
			end = i + 2
		case (r >= '0' && r <= '9' || r == '#' || r == '*') && at(i+1) == 0xFE0F && at(i+2) == 0x20E3:
			end = i + 3
		case r >= 0x80 && !regional(r) && (textclean.IsEmojiPresentation(r) || at(i+1) == 0xFE0F):
			end = i + 1
		}
		if end == 0 {
			text = append(text, r)
			i++
			continue
		}
		for end < len(rs) {
			n := rs[end]
			if n == 0xFE0F || skinTone(n) || n == 0x20E3 || emojiTag(n) {
				end++
				continue
			}
			if n == 0x200D && end+1 < len(rs) {
				end += 2
				continue
			}
			break
		}
		flush()
		out = append(out, Piece{Text: string(rs[i:end]), Emoji: true})
		i = end
	}
	flush()
	return out
}

// Names 是表情序列在 Twemoji 目录里可能的文件名，按先后试：码位小写十六进制用 - 连；
// 不含零宽连接符的去掉 fe0f（2764.svg、3297.svg）；含零宽连接符的先试带 fe0f 的全名（1f3f3-fe0f-200d-1f308.svg），再试去掉的。
func Names(seq string) []string {
	name := func(s string) string {
		parts := make([]string, 0, 4)
		for _, r := range s {
			parts = append(parts, fmt.Sprintf("%x", r))
		}
		return strings.Join(parts, "-") + ".svg"
	}
	stripped := name(strings.ReplaceAll(seq, "️", ""))
	if !strings.ContainsRune(seq, '‍') {
		return []string{stripped}
	}
	if full := name(seq); full != stripped {
		return []string{full, stripped}
	}
	return []string{stripped}
}

// Set 是一个 Twemoji 目录加上解析好的图（结果都缓存，找不到的也记着）。并发安全。
type Set struct {
	dir   string
	mu    sync.Mutex
	cache map[string]*ui.SVG
}

// NewSet 给目录建一个集合；目录为空的集合什么图都取不到。
func NewSet(dir string) *Set { return &Set{dir: dir} }

var (
	sharedMu sync.Mutex
	shared   = map[string]*Set{}
)

// For 返回这个目录的共享集合：同一个目录到处拿到同一个，缓存不重复。给“目录写在一个全局变量里”的调用方用。
func For(dir string) *Set {
	sharedMu.Lock()
	defer sharedMu.Unlock()
	s := shared[dir]
	if s == nil {
		s = NewSet(dir)
		shared[dir] = s
	}
	return s
}

// Dir 是集合用的目录。
func (e *Set) Dir() string {
	if e == nil {
		return ""
	}
	return e.dir
}

// SVG 返回表情序列的图；没有目录、没有这张图、解析失败都是 nil。e 为 nil 也行（当成没有目录）。
func (e *Set) SVG(seq string) *ui.SVG {
	if e == nil || e.dir == "" || seq == "" {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if v, ok := e.cache[seq]; ok {
		return v
	}
	var svg *ui.SVG
	for _, name := range Names(seq) {
		if data, err := os.ReadFile(filepath.Join(e.dir, name)); err == nil {
			if parsed, err := ui.ParseSVG(data); err == nil {
				svg = parsed
				break
			}
		}
	}
	if e.cache == nil {
		e.cache = map[string]*ui.SVG{}
	}
	e.cache[seq] = svg
	return svg
}

// Markup 返回表情序列的 SVG 文本（给要自己喂 SVG 的调用方，比如给脚本插件用）；没有目录、没有这张图都是 false。不缓存，每次读文件。
func (e *Set) Markup(seq string) (string, bool) {
	if e == nil || e.dir == "" || seq == "" {
		return "", false
	}
	for _, name := range Names(seq) {
		if data, err := os.ReadFile(filepath.Join(e.dir, name)); err == nil && len(data) > 0 {
			return string(data), true
		}
	}
	return "", false
}
