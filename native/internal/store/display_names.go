package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"
	"unicode"
)

// maxNameRunes 是界面上一个名字（群名、昵称、群名片）最多显示的字数，超出的截断加省略号。
const maxNameRunes = 40

// CleanDisplayName 清理一个外来的名字（群名、昵称、群名片）再给界面显示：
// 换行、制表和各种空白并成一个空格，其余控制字符、零宽和方向控制字符（Unicode 的 Cf 类）去掉，首尾空白去掉，超过 maxNameRunes 个字截断。
// 零宽连接符（U+200D）只在两边都是符号（emoji 拼合）时留着，不然是看不见的占位。清完是空串就表示"这个名字没有可显示的内容"，调用方退到下一个来源。
// 不用 channel.EscapeName：那个是给提示词行防伪造的（换括号、冒号），界面显示不需要。
func CleanDisplayName(s string) string {
	rs := []rune(s)
	out := make([]rune, 0, len(rs))
	pendingSpace := false
	for i, r := range rs {
		switch {
		case unicode.IsSpace(r):
			pendingSpace = len(out) > 0
		case r == 0x200D:
			if len(out) > 0 && !pendingSpace && i+1 < len(rs) && joinable(out[len(out)-1]) && joinable(rs[i+1]) {
				out = append(out, r)
			}
		case unicode.IsControl(r) || unicode.Is(unicode.Cf, r):
		default:
			if pendingSpace {
				out = append(out, ' ')
				pendingSpace = false
			}
			out = append(out, r)
		}
	}
	if len(out) > maxNameRunes {
		out = out[:maxNameRunes]
		for len(out) > 0 && (out[len(out)-1] == 0x200D || out[len(out)-1] == ' ') {
			out = out[:len(out)-1]
		}
		out = append(out, '…')
	}
	return string(out)
}

// joinable：能和零宽连接符拼成一个 emoji 的字（符号，或 emoji 的变体选择符 U+FE0F）。
func joinable(r rune) bool { return unicode.IsSymbol(r) || r == 0xFE0F }

// qqPerson 是 qq_people 里的一行：昵称和各群的群名片。
type qqPerson struct {
	nick  string
	cards map[string]string
}

// queryer 是 *sql.DB 和 *sql.Tx 都有的那一个方法。
type queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

const nameChunk = 400

// readQQPeople 按号（裸号，不带 qq: 前缀）批量读 qq_people；表里没有的号不在结果里。每次现读，不缓存：
// 表里的名字被改了（导入或别的程序写）下次读就是新的。cards_json 坏了或某一项不是字符串，只丢那一项。
func readQQPeople(ctx context.Context, q queryer, uids []string) (map[string]qqPerson, error) {
	out := map[string]qqPerson{}
	for start := 0; start < len(uids); start += nameChunk {
		chunk := uids[start:min(start+nameChunk, len(uids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := q.QueryContext(ctx, `SELECT uid,COALESCE(nickname,''),COALESCE(cards_json,'') FROM qq_people WHERE uid IN (`+strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var uid, nick, cards string
			if err := rows.Scan(&uid, &nick, &cards); err != nil {
				rows.Close()
				return nil, err
			}
			p := qqPerson{nick: nick, cards: map[string]string{}}
			var raw map[string]any
			if json.Unmarshal([]byte(cards), &raw) == nil {
				for gid, v := range raw {
					if s, ok := v.(string); ok {
						p.cards[gid] = s
					}
				}
			}
			out[uid] = p
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// readQQGroupNames 按群号批量读 qq_groups.name，同样每次现读。
func readQQGroupNames(ctx context.Context, q queryer, gids []string) (map[string]string, error) {
	out := map[string]string{}
	for start := 0; start < len(gids); start += nameChunk {
		chunk := gids[start:min(start+nameChunk, len(gids))]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		rows, err := q.QueryContext(ctx, `SELECT gid,COALESCE(name,'') FROM qq_groups WHERE gid IN (`+strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")+`)`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var gid, name string
			if err := rows.Scan(&gid, &name); err != nil {
				rows.Close()
				return nil, err
			}
			out[gid] = name
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// qqUID 取发言人的裸号：人的 ActorID 是 "qq:<号>"（channel.Roster.Resolve 这样写，Bun 时代也是），qq_people.uid 是裸号。
// 没有前缀的也认（老测试和手造的数据）；"qq:" 后面是空的（协议端没说是谁）没有号。
func qqUID(actorID string) string { return strings.TrimPrefix(actorID, "qq:") }

// isQQNumber 是不是一个 QQ 号（只有数字）：@ 的 target 和 from.id 是外来的，不是号的不拿去查名字
func isQQNumber(s string) bool {
	return s != "" && strings.Trim(s, "0123456789") == ""
}

// storedFrom 是入库时随事件存下的发言当时的名字（segments 里的 from）；入库的 Go 事件有，导入的旧事件没有。
func storedFrom(segments string) (nick, card string) {
	_, nick, card = fromOf(segments)
	return nick, card
}

// fromOf 同 storedFrom，多给发言人的号：主人的事件 actor_id 是固定的 "owner"，号只在这里
func fromOf(segments string) (uid, nick, card string) {
	if segments == "" {
		return "", "", ""
	}
	var body struct {
		From struct {
			ID   string `json:"id"`
			Nick string `json:"nick"`
			Card string `json:"card"`
		} `json:"from"`
	}
	if json.Unmarshal([]byte(segments), &body) != nil {
		return "", "", ""
	}
	return body.From.ID, body.From.Nick, body.From.Card
}

// atTargets 一条消息里被 @ 的号（不含全体成员），按出现顺序、去重
func atTargets(segments string) []string {
	if segments == "" {
		return nil
	}
	var body struct {
		Segs []struct {
			Kind   string `json:"kind"`
			Target string `json:"target"`
		} `json:"segs"`
	}
	if json.Unmarshal([]byte(segments), &body) != nil {
		return nil
	}
	var out []string
	for _, x := range body.Segs {
		if x.Kind == "at" && isQQNumber(x.Target) && !slices.Contains(out, x.Target) {
			out = append(out, x.Target)
		}
	}
	return out
}

// speakerName 选一个发言人的显示名：发言当时存下的群名片 > 当时存下的昵称 > qq_people 里本群的群名片 > qq_people 昵称。
// Go 入库的事件带着发言当时的名字，那才是最新的：Go 这边没有程序在写 qq_people（只有导入写，导入时冻住），
// 所以名册只给导入来的旧事件（没存名字）兜底，和 memsource 取发言人名字是同一个顺序。
// 私聊（group 为空）只看昵称。每一项先清理，清完是空的就跳到下一项；全没有返回空串，调用方退回号码。
func speakerName(p qqPerson, known bool, group string, r DisplayRow) string {
	nick, card := storedFrom(r.Segments)
	var order []string
	if group != "" {
		order = append(order, card)
	}
	order = append(order, nick)
	if known {
		if group != "" {
			order = append(order, p.cards[group])
		}
		order = append(order, p.nick)
	}
	for _, name := range order {
		if c := CleanDisplayName(name); c != "" {
			return c
		}
	}
	return ""
}

// 本页与合法引用共用一个批量名字查询，不刷新远程名片。
// 主人的事件 actor_id 是 "owner"，号和当时的名字在段里的 from：也按名片、昵称、名册取名（2026-11-02 主人：群里引用显示"主人"不对，要原本的名字），
// 都取不到 DisplayName 留空，读取层再写"主人"。
// 被 @ 的号（段里 kind=at 的 target）的名字放进 AtNames：先用这一页里同一个号发言时的名字，没有再查名册；
// 不为 @ 另外去扫整个会话找名字（大群里按人查没有索引，会拖慢读消息）。
func enrichDisplayNames(ctx context.Context, tx *sql.Tx, scope DisplayScope, items []DisplayRow) error {
	if scope.Platform != "qq" || len(items) == 0 {
		return nil
	}
	uidOf := func(r DisplayRow) string {
		switch r.ActorKind {
		case "character":
			return ""
		case "owner":
			uid, _, _ := fromOf(r.Segments)
			if !isQQNumber(uid) {
				return ""
			}
			return uid
		}
		return qqUID(r.ActorID)
	}
	seen := map[string]bool{}
	var uids []string
	want := func(uid string) {
		if uid != "" && !seen[uid] {
			seen[uid] = true
			uids = append(uids, uid)
		}
	}
	for _, r := range items {
		want(uidOf(r))
		if r.Reply != nil {
			want(uidOf(*r.Reply))
			for _, uid := range atTargets(r.Reply.Segments) {
				want(uid)
			}
		}
		for _, x := range r.Reactions {
			want(uidOf(DisplayRow{ActorKind: x.ActorKind, ActorID: x.ActorID}))
		}
		for _, uid := range atTargets(r.Segments) {
			want(uid)
		}
	}
	people, err := readQQPeople(ctx, tx, uids)
	if err != nil {
		return err
	}
	parts := strings.Split(scope.Conversation, ":")
	group := ""
	if len(parts) == 3 && parts[1] == "group" {
		group = parts[2]
	}
	spoke := map[string]string{} // 这一页里发过言的号 → 显示名，给 @ 用
	name := func(r *DisplayRow) {
		uid := uidOf(*r)
		if uid == "" && r.ActorKind != "owner" {
			return
		}
		p, known := people[uid]
		r.DisplayName = speakerName(p, known, group, *r)
		r.ActorUID = uid // 界面拿它查头像：主人的 actor_id 是 "owner"，不带号
		if uid != "" && r.DisplayName != "" {
			spoke[uid] = r.DisplayName
		}
	}
	for i := range items {
		name(&items[i])
		if items[i].Reply != nil {
			name(items[i].Reply)
		}
		for j := range items[i].Reactions { // 回应是通知，没存发言当时的名字，只查名册
			x := &items[i].Reactions[j]
			if r := (DisplayRow{ActorKind: x.ActorKind, ActorID: x.ActorID}); uidOf(r) != "" {
				p, known := people[uidOf(r)]
				x.DisplayName = speakerName(p, known, group, r)
			}
		}
	}
	fillAt := func(r *DisplayRow) {
		for _, uid := range atTargets(r.Segments) {
			n := spoke[uid]
			if n == "" {
				p, known := people[uid]
				n = speakerName(p, known, group, DisplayRow{})
			}
			if n != "" {
				if r.AtNames == nil {
					r.AtNames = map[string]string{}
				}
				r.AtNames[uid] = n
			}
		}
	}
	for i := range items {
		fillAt(&items[i])
		if items[i].Reply != nil {
			fillAt(items[i].Reply)
		}
	}
	return nil
}
