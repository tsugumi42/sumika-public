package linkcard

// 推特卡片的字体（规格-链接发送改版 C1）：文渊圆体为主，缺的字用 Maple Mono NF CN 兜，再缺交给系统的字体兜底链
// （主题字体、系统的彩色表情字体）；表情先用 Twemoji 的图画（emoji.go），没有图才交字体。画字交给 mygo 的字族列表逐字回退；这里另外用 sfnt 读字表，渲染前查整段文字缺哪些字，
// 结果写进 Output.Coverage 和宿主日志，不静默换字体。
// 文渊圆体 48 MB、不进 git（.gitignore native/fonts/*.ttf），正式程序从 exe 旁边的 fonts/ 读，和界面字体同一个地方；
// exe 旁边没有时（go run、样张工具）退到本机开发目录 native/fonts/；找法是公共字体服务（assets/fonts）的，不在这里再写一套。
// Maple 也只在这些素材字体目录里找，不扫系统字体目录和用户另装的字体目录。

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"golang.org/x/image/font/sfnt"

	"sumika-native/internal/assets"
	"sumika-native/internal/assets/emoji"
	"sumika-native/internal/assets/fonts"
)

// 卡片自己注册的字族名，和界面的字族分开，互不影响
const (
	familyPrimary  = "Sumika Card WenYuan"
	familyFallback = "Sumika Card Maple"
)

// fontFace 是一份读进来的字体：注册给 mygo 画字，同时留一份 sfnt 查字表
type fontFace struct {
	path string
	font *sfnt.Font
	buf  sfnt.Buffer
	// cover 只给测试：替代字表说有没有这个字
	cover func(rune) bool
}

func (f *fontFace) has(r rune) bool {
	if f != nil && f.cover != nil {
		return f.cover(r)
	}
	if f == nil || f.font == nil {
		return false
	}
	i, err := f.font.GlyphIndex(&f.buf, r)
	return err == nil && i != 0
}

// fontSet 是卡片用的一套字体；只在渲染 worker 里用（sfnt.Buffer 不能并发）
type fontSet struct {
	primary, fallback *fontFace
	// family 是给 mygo 的字族列表：文渊圆体、Maple、主题字体，后面系统再兜
	family string
	// issues 是加载时出的问题（找不到、注册失败），每次渲染都带进 Coverage，日志里看得到
	issues []string
	// emoji 是 Twemoji 的图；nil 表示没找到目录，表情交字体画
	emoji *emoji.Set
}

// measurer 是量字用的：字族和表情图跟画的时候同一套
func (fs *fontSet) measurer() measurer { return measurer{family: fs.family, emoji: fs.emoji} }

// loadFace 读一个字体文件，注册成 family，并解析字表；sibling 是同族的其他字重文件（Maple 的粗体），找得到就一起注册
func loadFace(path, family string, register func([]byte, string) error, siblings ...string) (*fontFace, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	f, err := sfnt.Parse(b)
	if err != nil {
		return nil, fmt.Errorf("读不了字表：%w", err)
	}
	if err := register(b, family); err != nil {
		return nil, fmt.Errorf("注册失败：%w", err)
	}
	for _, s := range siblings {
		if sb, err := os.ReadFile(s); err == nil {
			_ = register(sb, family) // 粗体注册不上就用常规体合成，不算失败
		}
	}
	return &fontFace{path: path, font: f}, nil
}

// primaryCandidates 是文渊圆体按顺序试的路径：设置给了就只用设置的；没给时用公共字体服务的找法
// （exe 旁的 fonts/ 在前，本机开发目录在后，go run、样张工具这类开发入口 exe 旁没有 fonts/）。
// 少了开发目录这一档，卡片会用 Maple Mono 画正文，整段英文变成等宽字体。
func primaryCandidates(o Options) []string {
	if len(o.FontPaths) > 0 {
		return o.FontPaths
	}
	return fonts.WenYuanCandidates(fonts.Options{})
}

// fallbackCandidates 是 Maple 常规体按顺序试的路径：nil 用公共字体服务的找法（只在素材字体目录里找），空切片表示不用
func fallbackCandidates(o Options) []string {
	if o.FallbackFontPaths != nil {
		return o.FallbackFontPaths
	}
	return fonts.MapleCandidates(fonts.Options{}, 400)
}

func loadFonts(o Options, register func([]byte, string) error, themeFont string) *fontSet {
	fs := &fontSet{}
	var tried []string
	for _, p := range primaryCandidates(o) {
		face, err := loadFace(p, familyPrimary, register)
		if err == nil {
			fs.primary = face
			break
		}
		tried = append(tried, filepath.Base(p)+"："+shortErr(err))
	}
	if fs.primary == nil {
		fs.issues = append(fs.issues, "文渊圆体没加载（"+strings.Join(tried, "；")+"），卡片用 Maple Mono 和系统字体画")
	}
	tried = tried[:0]
	for _, p := range fallbackCandidates(o) {
		bold := strings.Replace(p, "-Regular.", "-Bold.", 1) // 同目录的粗体
		face, err := loadFace(p, familyFallback, register, bold)
		if err == nil {
			fs.fallback = face
			break
		}
		tried = append(tried, filepath.Base(p)+"："+shortErr(err))
	}
	if fs.fallback == nil {
		fs.issues = append(fs.issues, "Maple Mono NF CN 没加载（"+strings.Join(tried, "；")+"），文渊圆体缺的字直接交系统字体")
	}
	var fams []string
	switch {
	case fs.primary != nil:
		fams = append(fams, familyPrimary)
		if fs.fallback != nil {
			fams = append(fams, familyFallback)
		}
		if themeFont != "" {
			fams = append(fams, themeFont)
		}
	default:
		// 文渊圆体没加载时 Maple Mono 不能排第一：它是等宽字体，只该兜别的字体缺的字，排第一会把整段正文画成等宽
		if themeFont != "" {
			fams = append(fams, themeFont)
		}
		if fs.fallback != nil {
			fams = append(fams, familyFallback)
		}
	}
	if len(fams) == 0 {
		fams = append(fams, "system-ui")
	}
	fs.family = strings.Join(fams, ", ")
	if dir, tried := assets.Twemoji(o.EmojiDir); dir != "" {
		fs.emoji = emoji.NewSet(dir)
	} else {
		fs.issues = append(fs.issues, "没找到 Twemoji 表情图（找了 "+strings.Join(tried, "；")+"），表情交系统字体画")
	}
	return fs
}

func shortErr(err error) string {
	if os.IsNotExist(err) {
		return "没有这个文件"
	}
	return err.Error()
}

// Coverage 是一张卡片上的字由谁来画：文渊圆体有的、它缺而 Maple 兜住的、两个都缺交给系统字体的（彩色表情单独数）。
// 每个字只数一次；用 Twemoji 的图画的表情一个序列算一个，计进 Emoji，不查字表。Issues 是字体加载的问题。
type Coverage struct {
	Total      int      `json:"total"`
	Primary    int      `json:"primary"`
	Emoji      int      `json:"emoji"`
	ByFallback []rune   `json:"by_fallback"`
	Uncovered  []rune   `json:"uncovered"`
	Missing    []rune   `json:"missing_in_primary"` // 文渊圆体缺的全部（不含表情）
	Issues     []string `json:"issues,omitempty"`
}

// skipRune 是不用字形的字符：空白、控制符、零宽连接、变体选择符、肤色修饰（跟着表情走）
func skipRune(r rune) bool {
	return unicode.IsSpace(r) || unicode.IsControl(r) || r == 0x200D || r == 0x200B || r == 0xFEFF ||
		(r >= 0xFE00 && r <= 0xFE0F) || (r >= 0xE0100 && r <= 0xE01EF) || (r >= 0x1F3FB && r <= 0x1F3FF) || (r >= 0xE0020 && r <= 0xE007F)
}

// emojiRune 是彩色表情一类：前两个字体都没有时交系统的彩色表情字体（这类字不算“缺字”）
func emojiRune(r rune) bool {
	return (r >= 0x1F000 && r <= 0x1FAFF) || (r >= 0x2600 && r <= 0x27BF) || (r >= 0x1F1E6 && r <= 0x1F1FF) || r == 0x2B50 || r == 0x2B55 || r == 0x203C || r == 0x2049
}

// check 查整段文字（所有行、所有字，不只 ASCII）
func (fs *fontSet) check(texts ...string) Coverage {
	cov := Coverage{Issues: fs.issues}
	seen := map[rune]bool{}
	seenEmoji := map[string]bool{}
	for _, t := range texts {
		var plain strings.Builder
		for _, p := range emoji.Split(t) {
			if p.Emoji && fs.emoji.SVG(p.Text) != nil {
				if !seenEmoji[p.Text] {
					seenEmoji[p.Text] = true
					cov.Total++
					cov.Emoji++
				}
				continue
			}
			plain.WriteString(p.Text)
		}
		for _, r := range plain.String() {
			if seen[r] || skipRune(r) {
				continue
			}
			seen[r] = true
			cov.Total++
			// 顺序和画字时的字族列表一样：文渊圆体、Maple，最后才是系统（表情归系统的彩色表情字体）
			switch {
			case fs.primary.has(r):
				cov.Primary++
			case fs.fallback.has(r):
				cov.Missing = append(cov.Missing, r)
				cov.ByFallback = append(cov.ByFallback, r)
			case emojiRune(r):
				cov.Emoji++
			default:
				cov.Missing = append(cov.Missing, r)
				cov.Uncovered = append(cov.Uncovered, r)
			}
		}
	}
	for _, s := range [][]rune{cov.Missing, cov.ByFallback, cov.Uncovered} {
		sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	}
	return cov
}

// Summary 是给日志的一句话；没缺字、没加载问题时为空
func (c Coverage) Summary() string {
	var parts []string
	parts = append(parts, c.Issues...)
	if len(c.Missing) > 0 {
		parts = append(parts, fmt.Sprintf("文渊圆体缺 %d 个字，Maple Mono 兜住 %d 个（%s），剩 %d 个交系统字体（%s）",
			len(c.Missing), len(c.ByFallback), clipRunes(c.ByFallback), len(c.Uncovered), clipRunes(c.Uncovered)))
	}
	return strings.Join(parts, "；")
}

func clipRunes(rs []rune) string {
	if len(rs) == 0 {
		return "无"
	}
	var b strings.Builder
	for i, r := range rs {
		if i == 20 {
			b.WriteString("…")
			break
		}
		fmt.Fprintf(&b, "%c", r)
	}
	return b.String()
}
