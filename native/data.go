package main

// 取数据（规格-界面接Go后台 1.1、4.x、5.3）：窗口只认 api.Reader。生产由 internal/app 的只读接口提供，
// 演示数据是显式选的假实现（demo_reader.go），标题写"演示数据"。这里只做 DTO → 界面结构的一次转换：
// 后台已整理好 api.DisplaySegment；界面直接读取其种类与展示字段，只附加排版所需的状态。
// 读库、读文件都在后台线程；界面线程只拿转换好的值。

import (
	"encoding/json"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"sumika-native/internal/api"
	"sumika-native/internal/segview"
)

type character struct {
	ID     string
	Name   string
	Color  string
	Model  string
	Status string
	Err    string // 资料读不到的原因；空 = 正常
}

// conv 会话列表的一项；Last 只用来显示最后一句和时间
type conv struct {
	Key       api.ConversationKey
	Kind      string // local / home（本地群聊）/ group / private
	Title     string
	CreatedAt int64 // 空群显示实际创建时间，Last 仍为空
	Last      lastLine
	Unread    int
	HasUnread bool
	Observers []string
	Project   string // 本地私聊给这个会话选了项目目录时的项目名（项目根的文件夹名）；没选是空，列表不写
}

type lastLine struct {
	Text      string
	CreatedAt int64
}

// seg 保留后台展示段，只附加界面使用的位置和下载缓存键，不改写 Kind。
type seg struct {
	api.DisplaySegment
	position    int
	downloadKey downloadKey
}

func (s seg) mediaHash() string {
	if s.Media != nil {
		return s.Media.Hash
	}
	return ""
}

// segmentView copies presentation facts while leaving media credentials with the host.
func segmentView(s seg, position int) segview.Segment {
	v := segview.Segment{
		Kind: s.Kind, Text: s.Text, Target: s.Target, Ref: s.Ref, Position: position,
		HasMedia: s.Media != nil, HasHash: s.Media != nil && s.Media.Hash != "",
		HasCover: s.Cover != nil, HasCoverHash: s.Cover != nil && s.Cover.Hash != "",
		CanDownload: s.Download != nil, DurationMS: s.DurationMS, Size: s.Size,
		Width: s.Width, Height: s.Height, Animated: s.Animated, Sticker: s.Sticker,
		FetchState: s.FetchState, FileKind: s.FileKind, MediaState: s.MediaState,
		Issue: s.Issue, Summary: s.Summary, Name: s.Name, Links: s.Links,
	}
	if s.Card != nil {
		v.Card = &segview.Card{Title: s.Card.Title, Description: s.Card.Description, Source: s.Card.Source, URL: s.Card.URL}
	}
	if s.Forward != nil {
		v.Forward = &segview.Forward{Title: s.Forward.Title, Lines: s.Forward.Lines, Count: s.Forward.Count, Missing: s.Forward.Missing, State: s.Forward.State}
	}
	return v
}

// fileNote 用于会话预览与引用占位；正文使用文件组件。
// 视频走独立组件，语音仍只有占位。
func fileNote(s seg) string {
	name := (segview.Segment{Kind: s.Kind, Name: s.Name, Summary: s.Summary}).DisplayName()
	if s.Size > 0 {
		name += " · " + humanSize(s.Size)
	}
	return "[文件：" + name + "]"
}

// humanSize 是字节数的人话：不到 1 KB 写整数加 B，往上按 1024 进位、一位小数（KB、MB、GB）；四舍五入到 1024.0 时进到下一档
func humanSize(n int64) string {
	if n < 1024 {
		return strconv.FormatInt(max(n, 0), 10) + " B"
	}
	units := []string{"KB", "MB", "GB"}
	v, i := float64(n), -1
	for v >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	if math.Round(v*10)/10 >= 1024 && i < len(units)-1 {
		v /= 1024
		i++
	}
	return strconv.FormatFloat(v, 'f', 1, 64) + " " + units[i]
}

// segFromDisplay 包装只读展示段；非空转发路径只编码一次，供下载状态复用。
func segFromDisplay(d api.DisplaySegment) seg {
	s := seg{DisplaySegment: d}
	if d.Download != nil {
		// nil 和空列表都指正文；非空路径编码一次，避免绘制帧分配及分隔符碰撞。
		path := ""
		if len(d.Download.Forward) > 0 {
			encoded, _ := json.Marshal(d.Download.Forward)
			path = string(encoded)
		}
		s.downloadKey = downloadKey{d.Download.Source, d.Download.Index, d.Download.Node, path}
	}
	return s
}

// charOfConv 本地会话里角色的话没有 actor_id 也认得出是谁：console:<角色>[:topic:<id>]
func charOfConv(k api.ConversationKey) string {
	if k.Platform != "local" || k.Scene == "group" {
		return ""
	}
	id, ok := strings.CutPrefix(k.Conversation, "console:")
	if !ok && k.Scene == "private" { // 演示数据的本地私聊是 local:<角色>，不会出现在真实库里
		id, ok = strings.CutPrefix(k.Conversation, "local:")
	}
	if !ok {
		return ""
	}
	id, _, _ = strings.Cut(id, ":")
	return id
}

// msgFromView 把一条展示 DTO 变成界面上的一行（规格-原生界面第二版场合表）：
//   - QQ 私聊、QQ 群：她的号发的（actor_kind=character）在右，主人和群友在左；
//   - 本地私聊、本地群聊：主人（origin=owner）在右，角色在左。
//
// 发送者身份只看 actor_kind / origin，不看名字。角色回复（origin=character）按 Markdown 显示。
func msgFromView(k api.ConversationKey, v api.MessageView, charOf func(id string) (character, bool)) *msg {
	m := &msg{id: v.Ref.Event.EventID, seq: v.Seq, version: v.Version, actor: v.ActorID, text: v.Text, at: v.CreatedAt, recalled: v.Recalled}
	m.source = v.Ref
	m.service = v.Service
	m.service, m.serviceIcon = systemRowText(v, m.service)
	m.proc = v.Process
	m.aborted, m.partial = v.Aborted, v.Partial
	if v.Unsent {
		m.unsent = "未发送"
	}
	m.md = v.Origin == "character"
	m.proactive = v.Proactive
	if k.Platform == "local" {
		m.mine = v.Origin == "owner"
	} else {
		m.mine = v.ActorKind == "character"
	}
	charID := v.ActorID
	if id := charOfConv(k); id != "" && m.md {
		charID = id
	}
	if m.md || v.ActorKind == "character" {
		m.isChar = true
		m.actor = charID
		ch, ok := charOf(charID)
		m.who, m.color = ch.Name, ch.Color
		if !ok || m.who == "" {
			m.who = v.DisplayName
		}
	} else {
		m.who = v.DisplayName
		m.owner = v.ActorKind == "owner" && v.Origin != "goal" && v.Origin != "loop"
	}
	if len(v.Segments) > 0 {
		m.segs = make([]seg, len(v.Segments))
		views := make([]segview.Segment, len(v.Segments))
		for i, d := range v.Segments {
			m.segs[i] = segFromDisplay(d)
			m.segs[i].position = i
			views[i] = segmentView(m.segs[i], i)
		}
		m.blocks = segview.BlocksOf(views)
	} else {
		text := m.text
		if k.Platform == "qq" {
			text = maskRawMentions(text)
		}
		m.blocks = segview.TextBlocks(text)
	}
	m.links = linksOf(m)
	m.quote = quoteOf(v)
	m.chatParts = chatParts(v)
	m.bindPartImages(m.chatParts, storedPartImages(v))
	m.reacts = groupReactions(v.Reactions)
	m.avatar = avatarKeyOfActor(k.Platform, v.ActorKind, v.ActorID, v.ActorUID)
	if len(m.proc) > 0 && m.service != "" {
		// 程序退出时被中断的回合：步骤挂在系统行上，过程气泡画在它前面，名字和颜色用当时说话的角色（系统行自己的 actor 是角色号）
		ch, _ := charOf(v.ActorID)
		m.procRow = processRow(m.id+"#process", v.ActorID, ch.Name, ch.Color, m.at-1, m.proc)
		m.procRow.noEnter, m.procRow.liveKind = true, liveProcess
	}
	prepareChatView(m, k.Platform, v.Reactions)
	return m
}

func displayPreview(ds []api.DisplaySegment, fallback string, qq bool) string {
	if !qq || len(ds) == 0 {
		if qq {
			return maskRawMentions(fallback)
		}
		return fallback
	}
	// 预览不排版、不扫描可点击链接；@ 与正文共用 segview.AtText，不重复构建块。
	var text strings.Builder
	for _, d := range ds {
		switch d.Kind {
		case "text":
			text.WriteString(segview.CleanText(d.Text))
		case "at":
			text.WriteString(segview.AtText(d.Target, d.Name))
		case "face", "superface":
			text.WriteString(segview.FaceFallback(d.Ref, d.Kind))
		case "image", "emoji":
			text.WriteString("[图片]")
		case "video":
			text.WriteString("[视频]")
		case "forward":
			text.WriteString("[合并转发]")
		case "card":
			text.WriteString("[卡片]")
		case "voice":
			text.WriteString("[语音]")
		case "file":
			text.WriteString(fileNote(seg{DisplaySegment: d}))
		case "reply":
		default:
			text.WriteString("[其他消息]")
		}
	}
	return maskRawMentions(text.String())
}

// convKind 会话列表分组用
func convKind(k api.ConversationKey) string {
	switch {
	case k.Platform == "local" && k.Scene == "group":
		return "home"
	case k.Platform == "local":
		return "local"
	}
	return k.Scene
}

func convFromView(v api.ConversationView) conv {
	c := conv{Key: v.Key, Kind: convKind(v.Key), Title: v.Title, Unread: v.Unread, HasUnread: v.HasUnread, Observers: v.Observers, CreatedAt: v.CreatedAt}
	if v.WorkspaceRoot != "" {
		c.Project = filepath.Base(v.WorkspaceRoot)
	}
	if id := charOfConv(v.Key); len(c.Observers) == 0 && id != "" {
		c.Observers = []string{id} // 本地私聊后台不给观察者，角色在会话键里；右栏成员、背景光晕都靠这一份
	}
	if v.Last != nil {
		text := v.Last.Text
		if v.Key.Platform == "qq" {
			text = displayPreview(v.Last.Segments, text, true)
		}
		c.Last = lastLine{Text: text, CreatedAt: v.Last.CreatedAt}
	}
	return c
}

func characterFromResult(r api.Result[api.CharacterView]) character {
	c := character{ID: r.Value.ID, Name: r.Value.Name, Color: r.Value.Color, Model: r.Value.Model, Status: r.Value.Status}
	if r.State != api.Ready {
		c.Err = "读不到"
		if r.Err != nil {
			c.Err = r.Err.Error()
		}
	}
	return c
}
