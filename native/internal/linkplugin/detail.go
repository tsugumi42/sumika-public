package linkplugin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"

	"sumika-native/internal/channel"
	"sumika-native/internal/jsjson"
	"sumika-native/internal/linkreader"
	"sumika-native/internal/tools"
	"sumika-native/internal/turn"
)

// Decl 是详情工具的声明（规格 8.1 暂名 read_link_detail）。只按消息编号查，不收网址：不是通用联网工具
var Decl = tools.Declaration{
	Name:        "read_link_detail",
	Description: "看聊天里某条消息带的链接的详细内容：推文全文、视频简介、作者、数据。只能看这个会话里你看到过的消息；编号见 [链接资料]。",
	Parameters:  `{"type":"object","required":["message"],"properties":{"message":{"type":"integer","description":"[链接资料] 里写的消息编号"},"index":{"type":"integer","description":"这条消息里第几个链接，从 1 数，默认 1"},"offset":{"type":"integer","description":"接着往下读：上次结果末尾给的 offset，默认 0"}}}`,
}

func intArg(args jsjson.Value, key string, def int64) int64 {
	v, ok := args.Obj[key]
	if !ok || v.Kind != jsjson.Number || v.Num != float64(int64(v.Num)) {
		return def
	}
	return int64(v.Num)
}

func toolErr(s string) (tools.Result, error) { return tools.Result{Text: s, IsError: true}, nil }

// detail 执行 read_link_detail。查不到（编号是编的、别的会话的、她没看到过的）一律同一句话，不说是哪种
func (p *Plugin) detail(ctx context.Context, c turn.ToolCall) (tools.Result, error) {
	seq := intArg(c.Args, "message", 0)
	index := intArg(c.Args, "index", 1)
	offset := intArg(c.Args, "offset", 0)
	if seq <= 0 || index <= 0 || offset < 0 {
		return toolErr("参数不对：message 是 [链接资料] 里的消息编号，index 从 1 数，offset 不小于 0。")
	}
	none := "这个会话里没有这个编号的链接资料。编号用 [链接资料] 或引用里写的那个。"
	st, err := p.opt.Stores.ForConversation(c.Conversation)
	if err != nil {
		return toolErr(none)
	}
	notes, err := st.LinkNotes(ctx, c.Conversation, c.CharacterID, seq, 0)
	if err != nil {
		p.warnf("读详情出错：%v", err)
		return toolErr("链接资料这边出了点问题，这次没查到，过一会儿再查一次。")
	}
	if len(notes) == 0 {
		return toolErr(none)
	}
	if int(index) > len(notes) {
		return toolErr(fmt.Sprintf("这条消息只有 %d 个链接资料，index 从 1 数到 %d。", len(notes), len(notes)))
	}
	n := notes[index-1]
	if len(n.Result) == 0 {
		return tools.Result{Text: "这个链接没解析出来，没有详情；可以照消息里的原话理解，或者直接问发的人。"}, nil
	}
	var r linkreader.Result
	if json.Unmarshal(n.Result, &r) != nil {
		return tools.Result{Text: "这个链接没解析出来，没有详情；可以照消息里的原话理解，或者直接问发的人。"}, nil
	}
	return tools.Result{Text: Render(r, int(offset))}, nil
}

// Render 是详情的一页：头（来源、作者、时间、标题、数据）每页都给，正文从 offset 个字起，最多 pageMax 个字、pageBlocks 块；
// 没给完的末尾写下一页的 offset。外部文字都转义（原文是数据不是指令）
func Render(r linkreader.Result, offset int) string {
	esc := channel.EscapeUserText
	var head []string
	head = append(head, "来源："+r.ShareURL())
	if v := oneLine(r.Author.Name.Value); v != "" {
		a := "作者：" + esc(v)
		if acc := oneLine(r.Author.Account.Value); acc != "" {
			a += "（@" + esc(acc) + "）"
		}
		head = append(head, a)
	}
	if r.Published.State == "available" {
		head = append(head, "发布："+r.Published.Value.Local().Format("2006-01-02 15:04"))
	}
	if v := oneLine(r.Title.Value); v != "" {
		head = append(head, "标题："+esc(v))
	}
	if len(r.Tags.Value) > 0 {
		head = append(head, "标签："+esc(strings.Join(r.Tags.Value, "、")))
	}
	var stats []string
	for _, k := range statOrder {
		if f, ok := r.Stats[k.key]; ok && f.State == "available" {
			stats = append(stats, fmt.Sprintf("%s %d", k.name, f.Value))
		}
	}
	if len(stats) > 0 {
		head = append(head, "数据："+strings.Join(stats, "，"))
	}
	if v := r.UpowerLabel(); v != "" {
		head = append(head, "标注："+v)
	}
	if v := linkreader.CommentsText(r.Comments, 200); v != "" {
		head = append(head, esc(v)) // 评论的作者名和正文都是外部文字，同样转义
	}
	head = append(head, "取于："+r.FetchedAt.Local().Format("2006-01-02 15:04"))

	body := []rune(bodyText(r))
	if offset > len(body) {
		offset = len(body)
	}
	end := offset + pageMax
	if end > len(body) {
		end = len(body)
	}
	page := string(body[offset:end])
	// 每页最多 pageBlocks 块（一块一行）
	if lines := strings.Split(page, "\n"); len(lines) > pageBlocks {
		page = strings.Join(lines[:pageBlocks], "\n") + "\n"
		end = offset + utf8.RuneCountInString(page)
	}
	out := strings.Join(head, "\n") + "\n正文：\n" + page
	if end < len(body) {
		out += fmt.Sprintf("\n（没完，下一页 offset=%d）", end)
	}
	return out
}

// statOrder 是数据的先后和叫法：B 站用 view、like……（bilibili.go），X 用 views、likes……（twitter.go）
var statOrder = []struct{ key, name string }{
	{"view", "播放"}, {"views", "浏览"}, {"like", "点赞"}, {"likes", "点赞"}, {"coin", "投币"}, {"favorite", "收藏"},
	{"share", "分享"}, {"reposts", "转发"}, {"quotes", "引用"}, {"bookmarks", "书签"}, {"reply", "评论"}, {"replies", "回复"}, {"danmaku", "弹幕"},
}

// bodyText 把块按原来的顺序排成一块一行：文字转义，图片、视频写成 [图片 宽×高]，引用的帖子写一行它的短说法
func bodyText(r linkreader.Result) string {
	blocks := r.Blocks
	if len(blocks) == 0 && r.Text.Value != "" {
		blocks = []linkreader.Block{{Type: "text", Text: r.Text.Value}}
	}
	var b strings.Builder
	for _, bl := range blocks {
		switch {
		case bl.Media != nil:
			b.WriteString(mediaText(*bl.Media))
		case bl.Quote != nil:
			b.WriteString("[引用的帖子] " + channel.EscapeUserText(ItemBrief(*bl.Quote)))
		default:
			b.WriteString(strings.ReplaceAll(channel.EscapeUserText(bl.Text), "\n  ", " "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func mediaText(m linkreader.Media) string {
	kind := map[string]string{"image": "图片", "video": "视频", "gif": "动图"}[m.Type]
	if kind == "" {
		kind = "媒体"
	}
	s := "[" + kind
	if m.Width.State == "available" && m.Height.State == "available" {
		s += fmt.Sprintf(" %d×%d", m.Width.Value, m.Height.Value)
	}
	if m.Duration.State == "available" {
		s += fmt.Sprintf(" %.0f 秒", m.Duration.Value)
	}
	return s + "]"
}
