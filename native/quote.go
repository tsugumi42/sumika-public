package main

// 引用块（主人 2026-11-02："引用样式也还没做吧"）：气泡顶部写被引用的人和那句话的开头（最多两行），点一下跳到原消息并闪一下。
// 数据：后台的 api.MessageView 已经带 ReplyTo（原消息的事件 id）和 Reply（原消息的发言人、文字；读不到是 Unsupported）。
// 引用只认这两个字段，不看 segs 里的 reply 段（它只有平台消息号）；有 reply 段但 ReplyTo 没连上，也写"原消息不在本地"。
// 排法照 chat.ReplyQuote；自写以容纳原消息不可读的状态。背景用消息文字轻染，避免主题自定义后还假定发出的气泡是强调色。
// 嵌套块四边等距，小圆角不超过外层圆角减去间距。

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/egoist/mygo/ui"
	"github.com/tsugumi42/MewTypeUI/content"

	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
	"sumika-native/internal/config"
	"sumika-native/internal/segview"
)

// quote 一条消息引用的那条
type quote struct {
	target  string // 原消息的事件 id；空 = 后台没把引用连到本地的事件
	who     string
	text    string // 已经压成一行的文字
	ok      bool   // 原消息读到了：能显示发言人和文字；否则写"原消息不在本地"
	owner   bool   // 被引用的是主人：名字用强调色，别人的用正文色
	preview bool   // 转发节点的引用只看预览，不跳到树外的原消息。
	state   content.QuoteState
	// thumb 是原消息里第一张能当缩略图的媒体（图片、贴纸、视频封面），来自后台给的引用投影，
	// 凭据走和消息里的图片同一套加载；原消息不在已加载范围里也能取。nil 是没有。
	thumb     *seg
	thumbKind chatui.ThumbKind
}

const (
	quoteGone     = "原消息不在本地"
	quoteNoText   = "[消息]"                  // 原消息读到了，但没有文字（只有图之类）
	quoteMaxRunes = 200                     // 画之前先截：只显示两行，太长的字不必进排版
	flashSpan     = 1500 * time.Millisecond // 旧界面：被选中的底色持续多久

	// 新界面的强调（点引用跳转后）：总长照 Bun 的 --dur-flash（1.6 秒），前一半满强度、后一半淡出（su-flash 的 0%、50%、100%）。
	// flashSettle 是滚动到位后等多久再开始：让人先看到目标落在哪，再被提醒。
	workbenchFlash = 1600 * time.Millisecond
	flashSettle    = 120 * time.Millisecond
)

var rawMention = regexp.MustCompile(`@[0-9]+`)

// 无段的旧 QQ 原文和转发引用不能再查出名字，只将明确的 @号码打码。
func maskRawMentions(text string) string {
	return rawMention.ReplaceAllStringFunc(text, func(at string) string { return "@" + config.MaskID(at[1:]) })
}

// quoteOf 展示 DTO → 引用；没引用返回 nil。引用段在但 ReplyTo 没连上（原消息当时不在库里）也算引用
func quoteOf(v api.MessageView) *quote {
	hasSeg := false
	for _, d := range v.Segments {
		if d.Kind == "reply" {
			hasSeg = true
		}
	}
	if v.ReplyTo == nil && !hasSeg {
		return nil
	}
	q := &quote{}
	if v.ReplyTo != nil {
		q.target = v.ReplyTo.Event.EventID
	}
	if v.Reply.State == api.Ready {
		q.ok, q.who = true, strings.TrimSpace(v.Reply.Value.DisplayName)
		q.text = oneLine(displayPreview(v.Reply.Value.Segments, v.Reply.Value.Text, v.Ref.Conversation.Platform == "qq"))
		q.owner = v.Reply.Value.ActorKind == "owner"
		if th, kind, ok := quoteThumbOf(v.Reply.Value.Segments); ok {
			// 有缩略图：文字写原文第一行；只有图时写"图片"这类短词（Telegram 同样）
			q.thumb, q.thumbKind = &th, kind
			q.text = oneLine(firstTextLine(v.Reply.Value.Segments))
			if q.text == "" {
				q.text = thumbLabel(kind)
			}
		}
		if q.text == "" {
			q.text = quoteNoText
		}
		if q.who == "" {
			q.who = "某人"
		}
	}
	return q
}

// quoteThumbOf 找原消息里第一张能当缩略图的媒体：图片、贴纸、视频封面。动图只取静态档，缩略图不播放。
func quoteThumbOf(ds []api.DisplaySegment) (seg, chatui.ThumbKind, bool) {
	for i, d := range ds {
		switch {
		case d.Kind == "image" && d.Media != nil:
			s := segFromDisplay(d)
			s.position, s.Animated = i, false
			return s, chatui.ThumbImage, true
		case d.Kind == "emoji" && d.Sticker && d.Media != nil:
			s := segFromDisplay(d)
			s.position, s.Animated = i, false
			return s, chatui.ThumbSticker, true
		case d.Kind == "video" && d.Cover != nil:
			s := segFromDisplay(d)
			s.Kind, s.Media, s.Animated, s.position = "image", d.Cover, false, i
			return s, chatui.ThumbVideo, true
		}
	}
	return seg{}, "", false
}

// firstTextLine 原消息文字段的第一行（跳过空行）。
func firstTextLine(ds []api.DisplaySegment) string {
	for _, d := range ds {
		if d.Kind != "text" {
			continue
		}
		for _, line := range strings.Split(segview.CleanText(d.Text), "\n") {
			if strings.TrimSpace(line) != "" {
				return strings.TrimSpace(line)
			}
		}
	}
	return ""
}

func thumbLabel(k chatui.ThumbKind) string {
	switch k {
	case chatui.ThumbVideo:
		return "视频"
	case chatui.ThumbSticker:
		return "表情"
	}
	return "图片"
}

// oneLine 换行和连续空白压成一个空格，截到 quoteMaxRunes 个字
func oneLine(s string) string {
	var sb strings.Builder
	space, n := false, 0
	for _, r := range s {
		if unicode.IsSpace(r) {
			space = sb.Len() > 0
			continue
		}
		if unicode.IsControl(r) {
			continue
		}
		if space {
			sb.WriteByte(' ')
			space = false
		}
		sb.WriteRune(r)
		if n++; n >= quoteMaxRunes {
			break
		}
	}
	return sb.String()
}

// 转发预览只展示；只有主历史里可定位的引用才提供跳转动作。
func (a *app) quoteBlock(c *ui.Context, m *msg) {
	q := m.quote
	data := content.QuoteData{Name: q.who, Summary: q.text}
	label := "引用的消息，点一下跳到原消息"
	var activate func()
	if q.ok && !q.preview {
		activate = func() { a.jumpTo(q.target) }
	}
	if !q.ok {
		data.Status, label = quoteGone, "引用的消息，原消息不在本地"
	}
	root := content.Quote(c, data, &q.state, content.QuoteOptions{Appearance: content.Appearance{Outgoing: m.mine}, Label: label, OnActivate: activate})
	a.probeOf("quote:"+m.id, root)
}

// flashAlpha 是新界面里目标消息此刻的强调强度（Host.Flash 的实现）。
// 起点：目标行已经进入可见范围（滚动到位）之后再等 flashSettle；前一半满强度，后一半按缓出淡到 0；
// 系统要求减少动态效果时不淡出，满强度一段时间后直接消失。时间用窗口帧的时钟，测试里可以控制。
func (a *app) flashAlpha(c *ui.Context) func(string) float32 {
	return func(id string) float32 {
		if a.flashID == "" || a.flashID != id {
			return 0
		}
		first, last := a.list.Visible()
		if i := a.indexOf(id); i < first || i > last {
			return 0 // 还没滚到：不开始计时
		}
		if a.flashAt.IsZero() {
			a.flashAt = c.Now().Add(flashSettle)
		}
		el := c.Now().Sub(a.flashAt)
		if el < 0 {
			c.After(-el)
			return 0
		}
		if el >= workbenchFlash {
			a.flashID = ""
			return 0
		}
		c.After(16 * time.Millisecond)
		frac := float64(el) / float64(workbenchFlash)
		if frac <= 0.5 {
			return 1
		}
		if c.Preferences().ReduceMotion {
			return 1
		}
		t := (frac - 0.5) / 0.5
		return float32((1 - t) * (1 - t)) // 淡出走缓出曲线：一开始降得快、末尾收得慢
	}
}

// ───────────── 跳转 ─────────────

// jumpPages 原消息不在已加载的里面时，最多往上再取几页去找
const jumpPages = 6

// jumpTo 跳到原消息并闪一下。已加载的直接滚过去；不在已加载的，往上翻页找（一次 500 条，最多 jumpPages 页），
// 翻完了还没有就在状态栏写明。换会话时清掉（switchTo）
func (a *app) jumpTo(id string) {
	if id == "" {
		return
	}
	if i := a.indexOf(id); i >= 0 {
		a.jump = jumpState{}
		a.list.ScrollTo(i, ui.Center)
		a.flashID, a.flashAt = id, time.Time{} // 起点由那一行第一次构建时定（message），测试里的时钟和真的不是一个
		a.status = ""
		return
	}
	if !a.hasMore {
		a.status = "原消息不在已有的记录里"
		return
	}
	a.jump = jumpState{id: id, left: jumpPages}
	a.continueJump()
}

// continueJump 往上取一页去找 a.jump.id。取页在后台，取回来由 applyOlder 再调
func (a *app) continueJump() {
	if a.jump.id == "" {
		return
	}
	if !a.hasMore {
		a.jump = jumpState{}
		a.status = "原消息不在已有的记录里"
		return
	}
	if a.jump.left <= 0 {
		a.jump = jumpState{}
		a.status = fmt.Sprintf("往上翻了 %d 条也没找到原消息，可以继续点“加载更早”", jumpPages*500)
		return
	}
	if a.loading {
		return // 正在取的这一页回来时 applyOlder 会再调
	}
	a.jump.left--
	a.olderPage(500)
}

type jumpState struct {
	id   string
	left int
}

func (a *app) indexOf(id string) int {
	for i, m := range a.msgs {
		if m.id == id {
			return i
		}
	}
	return -1
}
