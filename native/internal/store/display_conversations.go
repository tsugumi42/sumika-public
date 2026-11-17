package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"sort"
	"strings"
)

// DisplayConversation 只保留真实会话身份和排序来源；空日常没有伪造时间。
type DisplayConversation struct {
	Conversation, Title string
	Observers           []string
	LastAt              int64
	PinnedAt            int64
	CreatedAt           int64
	Last                *DisplayRow
	WorkspaceCWD        string // 本地私聊绑定的工作目录和项目根；别的场合永远是空的
	WorkspaceRoot       string
}
type DisplayConversations struct {
	Items      []DisplayConversation
	NextCursor string
	HasMore    bool
}
type conversationCursor struct {
	Character  string
	Characters []string
	After      string
}

// ReadDisplayConversations 将无消息角色日常和已拥有话题纳入目录，不靠发言者反推本地群。
func (s *Set) ReadDisplayConversations(ctx context.Context, characters []string, character, cursor string, limit int) (DisplayConversations, error) {
	if limit <= 0 || limit > 1000 {
		return DisplayConversations{}, ErrDisplayParameter
	}
	if character != "" && !containsCharacter(characters, character) {
		return DisplayConversations{}, ErrDisplayScope
	}
	ids := append([]string(nil), characters...)
	sort.Strings(ids)
	var c conversationCursor
	if cursor != "" {
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(b, &c) != nil || c.Character != character || len(c.Characters) != len(ids) {
			return DisplayConversations{}, ErrDisplayParameter
		}
		for i := range ids {
			if ids[i] != c.Characters[i] {
				return DisplayConversations{}, ErrDisplayParameter
			}
		}
	}
	var all []DisplayConversation
	for _, named := range s.Each() {
		st := named.Store
		candidates := map[string]DisplayConversation{}
		if named.Platform == "local" {
			for _, id := range ids {
				if character == "" || character == id {
					conv := "console:" + id
					candidates[conv] = DisplayConversation{Conversation: conv}
				}
			}
			rows, err := st.Events.QueryContext(ctx, `SELECT id,character_id,COALESCE(title,''),COALESCE(pinned_at,0) FROM local_topics`)
			if err != nil {
				return DisplayConversations{}, err
			}
			for rows.Next() {
				var id, owner, title string
				var pinned int64
				if err := rows.Scan(&id, &owner, &title, &pinned); err != nil {
					rows.Close()
					return DisplayConversations{}, err
				}
				if containsCharacter(ids, owner) && (character == "" || owner == character) {
					conv := "console:" + owner + ":topic:" + id
					candidates[conv] = DisplayConversation{Conversation: conv, Title: title, PinnedAt: pinned}
				}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return DisplayConversations{}, err
			}
			groups, err := st.ReadLocalGroups(ctx, ids, character)
			if err != nil {
				return DisplayConversations{}, err
			}
			for _, g := range groups {
				candidates[g.Conversation()] = DisplayConversation{Conversation: g.Conversation(), Title: g.Name, CreatedAt: g.CreatedAt, Observers: g.CharacterIDs()}
			}
			bound, err := st.ReadLocalWorkspaces(ctx)
			if err != nil {
				return DisplayConversations{}, err
			}
			for conv, w := range bound {
				if _, group := LocalGroupID(conv); group {
					continue // 本地群不能设目录；库里被塞了也不带路径
				}
				if v, ok := candidates[conv]; ok {
					v.WorkspaceCWD, v.WorkspaceRoot = w.CWD, w.Root
					candidates[conv] = v
				}
			}
		} else if named.Platform == "qq" && len(ids) > 0 {
			scope := DisplayScope{Platform: "qq", Characters: ids, Character: character}
			filter, args := displayFilter(scope, "e")
			rows, err := st.Events.QueryContext(ctx, `SELECT DISTINCT e.conversation FROM events e WHERE `+filter, args...)
			if err != nil {
				return DisplayConversations{}, err
			}
			for rows.Next() {
				var conv string
				if err := rows.Scan(&conv); err != nil {
					rows.Close()
					return DisplayConversations{}, err
				}
				candidates[conv] = DisplayConversation{Conversation: conv}
			}
			err = rows.Err()
			rows.Close()
			if err != nil {
				return DisplayConversations{}, err
			}
		}
		for conv, v := range candidates {
			scope := DisplayScope{Platform: named.Platform, Conversation: conv, Characters: ids, Character: character}
			lastScope := scope
			lastScope.ShownOnly = true // 预览是聊天区最后一条能看到的，不是最后一个事件（2026-11-09 主人截图：“一条没认出来的通知”）
			page, err := st.ReadDisplay(ctx, lastScope, "", 1)
			if err != nil {
				if err == ErrDisplayScope {
					continue
				}
				return DisplayConversations{}, err
			}
			if len(page.Items) > 0 {
				last := page.Items[0]
				v.Last = &last
				v.LastAt = last.CreatedAt
			}
			if named.Platform == "qq" {
				filter, args := displayFilter(scope, "e")
				args = append([]any{conv}, args...)
				rows, err := st.Events.QueryContext(ctx, `SELECT DISTINCT o.character_id FROM events e JOIN observations o ON o.event_seq=e.seq WHERE e.conversation=? AND `+filter+` ORDER BY o.character_id`, args...)
				if err != nil {
					return DisplayConversations{}, err
				}
				for rows.Next() {
					var id string
					if err := rows.Scan(&id); err != nil {
						rows.Close()
						return DisplayConversations{}, err
					}
					if containsCharacter(ids, id) {
						v.Observers = append(v.Observers, id)
					}
				}
				err = rows.Err()
				rows.Close()
				if err != nil {
					return DisplayConversations{}, err
				}
			}
			all = append(all, v)
		}
	}
	rank := func(v DisplayConversation) int {
		if len(v.Conversation) >= 8 && v.Conversation[:8] == "console:" {
			if v.PinnedAt > 0 {
				return 1
			}
			for _, id := range ids {
				if v.Conversation == "console:"+id {
					return 0
				}
			}
			return 1
		}
		return 2
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i], all[j]
		ra, rb := rank(a), rank(b)
		if ra != rb {
			return ra < rb
		}
		if ra == 0 {
			return a.Conversation < b.Conversation
		}
		if ra == 1 && a.PinnedAt != b.PinnedAt {
			return a.PinnedAt > b.PinnedAt
		}
		if a.LastAt != b.LastAt {
			return a.LastAt > b.LastAt
		}
		return a.Conversation > b.Conversation
	})
	start := 0
	if cursor != "" {
		found := false
		for i := range all {
			if all[i].Conversation == c.After {
				start = i + 1
				found = true
				break
			}
		}
		if !found {
			return DisplayConversations{}, ErrDisplayParameter
		}
	}
	end := start + limit
	if end > len(all) {
		end = len(all)
	}
	out := DisplayConversations{Items: all[start:end], HasMore: end < len(all)}
	if err := s.fillQQTitles(ctx, out.Items); err != nil {
		return DisplayConversations{}, err
	}
	if out.HasMore {
		b, _ := json.Marshal(conversationCursor{character, ids, all[end-1].Conversation})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(b)
	}
	return out, nil
}

// fillQQTitles 给这一页里没有标题的 QQ 会话补标题：群是 qq_groups 的群名，私聊是对方在 qq_people 的昵称。
// 只补这一页的，每次现查不缓存：群改名、对方改昵称，界面下次刷新就跟上。查不到或清完没有内容的留空，
// 界面那边退回会话号码。私聊的会话号是 qq:private:<她的号>:<对方的号>，标题用对方的，不用她自己的。
func (s *Set) fillQQTitles(ctx context.Context, items []DisplayConversation) error {
	st, ok := s.Platform("qq")
	if !ok {
		return nil // 没接 QQ 平台：没有 QQ 会话要补
	}
	var gids, uids []string
	for _, v := range items {
		if v.Title != "" {
			continue
		}
		parts := strings.Split(v.Conversation, ":")
		switch {
		case len(parts) == 3 && parts[0] == "qq" && parts[1] == "group":
			gids = append(gids, parts[2])
		case len(parts) == 4 && parts[0] == "qq" && parts[1] == "private":
			uids = append(uids, parts[3])
		}
	}
	groups, err := readQQGroupNames(ctx, st.Events, gids)
	if err != nil {
		return err
	}
	people, err := readQQPeople(ctx, st.Events, uids)
	if err != nil {
		return err
	}
	for i, v := range items {
		if v.Title != "" {
			continue
		}
		parts := strings.Split(v.Conversation, ":")
		switch {
		case len(parts) == 3 && parts[0] == "qq" && parts[1] == "group":
			items[i].Title = CleanDisplayName(groups[parts[2]])
		case len(parts) == 4 && parts[0] == "qq" && parts[1] == "private":
			items[i].Title = CleanDisplayName(people[parts[3]].nick)
		}
	}
	return nil
}
