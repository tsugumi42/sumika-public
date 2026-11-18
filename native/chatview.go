package main

import (
	"encoding/hex"
	"math"
	"strings"
	"time"

	"github.com/egoist/mygo/ui"
	"sumika-native/internal/api"
	"sumika-native/internal/chatui"
)

// prepareChatView projects metadata once with the message version. Blocks remain
// the single immutable layout shared by both desktop views.
func prepareChatView(m *msg, platform string, reactions []api.ReactionView) {
	m.chatColor = chatColor(m.color)
	m.chatName = chatNameID(m.actor)
	m.chatNotes = notesOf(m, m.recalled)
	m.chatReactions = chatReactions(platform, reactions)
	prepareChatQuote(m)
}

func chatMessage(m *msg) chatui.Msg {
	v := chatui.Msg{ID: m.id, Sender: m.actor, Self: m.mine, Name: m.who,
		Char: m.chatColor, NameID: m.chatName, At: time.UnixMilli(m.at),
		Text: m.text, Blocks: m.blocks, Markdown: m.md, Notes: m.chatNotes,
		Reactions: m.chatReactions, Quote: m.chatQuote, State: chatui.Sent, Tip: proactiveTip(m)}
	if m.service != "" { // 通知条不属于任何人：发送人每条都不同，前后的气泡不会隔着它连成一组
		v.Service, v.Icon, v.Sender = m.service, m.serviceIcon, "service:"+m.id
		return v
	}
	if m.steps != nil {
		v.Steps, v.Blocks, v.Text = m.steps, nil, ""
	}
	if len(m.chatParts) > 0 { // 分块消息按块画，不画拼起来的正文和第一个引用
		v.Parts, v.Blocks, v.Quote = m.chatParts, nil, nil
	}
	switch {
	case m.liveKind == livePending:
		v.State = chatui.Sending
	case m.liveKind == liveTyping:
		v.Sender = "live-typing"
	case m.recalled || strings.Contains(m.unsent, "已撤回"):
		v.State = chatui.Recalled
	case m.aborted:
		v.State = chatui.Interrupted
	case m.streaming:
		v.State = chatui.Streaming
	case m.unsent != "" && !m.partial:
		v.State = chatui.Failed
	}
	return v
}

// proactiveTip 是主动联系消息时间的悬停补充：她为什么找你；理由是线里第一次开口时她自己写的，只在本机
func proactiveTip(m *msg) string {
	if m.proactive == nil {
		return ""
	}
	if m.proactive.Reason == "" {
		return "她主动来找你的"
	}
	return "她为什么找你：" + m.proactive.Reason
}

func prepareChatQuote(m *msg) {
	m.chatQuote = nil
	if q := m.quote; q != nil {
		m.chatQuote = &chatui.Quote{Name: q.who, Text: q.text, Owner: q.owner, Gone: !q.ok, Target: q.target, Preview: q.preview, Thumb: q.thumbKind}
	}
}

func chatNameID(s string) uint64 {
	const basis uint64 = 14695981039346656037
	h := basis
	for i := range len(s) {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

func chatReactions(platform string, rs []api.ReactionView) []chatui.Reaction {
	var out []chatui.Reaction
	at := make(map[string]int)
	seen := make(map[string]bool)
	for _, r := range rs {
		if r.Ref == "" {
			continue
		}
		who := r.ActorKind + "\x00" + r.ActorID
		if r.ActorID == "" {
			who = "\x00" + r.ID
		}
		key := r.Ref + "\x00" + who
		if seen[key] {
			continue
		}
		seen[key] = true
		i, ok := at[r.Ref]
		if !ok {
			i = len(out)
			at[r.Ref] = i
			p := chatui.Reaction{}
			if emoji, ok := unicodeReaction(r.Ref); ok {
				p.Emoji = emoji
			} else {
				p.Face = r.Ref
			}
			out = append(out, p)
		}
		p := &out[i]
		p.Count++
		p.Mine = p.Mine || r.ActorKind == "owner"
		p.By = append(p.By, chatui.Reactor{ID: who, Name: reactorName(r), NameID: chatNameID(who), Avatar: avatarKeyOfActor(platform, r.ActorKind, r.ActorID, "")})
	}
	return out
}

// chatColor accepts the hexadecimal forms supported by desktop appearance.
// Invalid/empty values preserve the component's NameID fallback.
func chatColor(s string) chatui.OKLCH {
	if !strings.HasPrefix(s, "#") {
		return chatui.OKLCH{}
	}
	s = s[1:]
	var bytes [4]byte
	bytes[3] = 255
	switch len(s) {
	case 3, 4:
		var expanded [8]byte
		for i := range len(s) {
			expanded[2*i] = s[i]
			expanded[2*i+1] = s[i]
		}
		if _, err := hex.Decode(bytes[:], expanded[:2*len(s)]); err != nil {
			return chatui.OKLCH{}
		}
	case 6, 8:
		if _, err := hex.Decode(bytes[:], []byte(s)); err != nil {
			return chatui.OKLCH{}
		}
	default:
		return chatui.OKLCH{}
	}
	return chatOKLCH(ui.RGBA(bytes[0], bytes[1], bytes[2], float32(bytes[3])/255))
}

func chatOKLCH(c ui.Color) chatui.OKLCH {
	linear := func(v uint8) float64 {
		x := float64(v) / 255
		if x <= 0.04045 {
			return x / 12.92
		}
		return math.Pow((x+0.055)/1.055, 2.4)
	}
	r, g, b := linear(c.R), linear(c.G), linear(c.B)
	l := math.Cbrt(0.4122214708*r + 0.5363325363*g + 0.0514459929*b)
	m := math.Cbrt(0.2119034982*r + 0.6806995451*g + 0.1073969566*b)
	s := math.Cbrt(0.0883024619*r + 0.2817188376*g + 0.6299787005*b)
	light := 0.2104542553*l + 0.7936177850*m - 0.0040720468*s
	a := 1.9779984951*l - 2.4285922050*m + 0.4505937099*s
	bb := 0.0259040371*l + 0.7827717662*m - 0.8086757660*s
	hue := math.Atan2(bb, a) * 180 / math.Pi
	if hue < 0 {
		hue += 360
	}
	return chatui.OKLCH{L: light, C: math.Hypot(a, bb), H: hue, A: float64(c.A) / 255}
}
