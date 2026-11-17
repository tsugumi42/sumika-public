package memsource

// 人名表（ADR-038：昵称、各群名片、曾用名）。记忆包只接 []memory.Person，从 Bun 的事件库读是宿主那一侧的事，
// 装配（internal/app）和考题（cmd/memory-eval）共用这一份，不各写一份。
// 跟 Source.names 按需查 qq_people 不同：那只取一页里用到的几条；这里启动时一次读全，给记忆的别名表用。

import (
	"encoding/json"
	"fmt"
	"sort"

	"sumika-native/internal/memory"
	"sumika-native/internal/store"
)

// QQPeople 是启动时只读的人名快照：记忆用全部别名，渠道按会话选当前显示名。
type QQPeople struct {
	People []memory.Person
	names  map[string]qqName
}

type qqName struct {
	nick  string
	cards map[string]string
}

// Name 群名片优先，其次昵称；空串留给 line.go 按原始号回退。
func (p QQPeople) Name(group, uid string) string {
	n := p.names[uid]
	if group != "" && n.cards[group] != "" {
		return n.cards[group]
	}
	return n.nick
}

// ReadQQPeople 从事件库（Bun 的 shared/events.db）只读打开，读全部的人名。文件打不开、读不了 qq_people 都返回错误，
// 调用方自己决定是"还没有名册"（先看文件在不在）还是真出错。
// 名片按群号排序：map 的遍历顺序随机，同一个库两次读出的别名顺序要一致。
func ReadQQPeople(file string) (QQPeople, error) {
	db, err := store.OpenReadOnlyDB(file)
	if err != nil {
		return QQPeople{}, fmt.Errorf("打开事件库 %s：%w", file, err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT uid, COALESCE(nickname, ''), COALESCE(cards_json, ''), COALESCE(aliases_json, '') FROM qq_people`)
	if err != nil {
		return QQPeople{}, fmt.Errorf("读 %s 的 qq_people：%w", file, err)
	}
	defer rows.Close()
	out := QQPeople{names: map[string]qqName{}}
	for rows.Next() {
		var uid, nick, cards, aliases string
		if err := rows.Scan(&uid, &nick, &cards, &aliases); err != nil {
			return QQPeople{}, fmt.Errorf("读 %s 的 qq_people：%w", file, err)
		}
		p := memory.Person{ID: "qq:" + uid}
		if nick != "" {
			p.Names = append(p.Names, nick)
		}
		var cm map[string]string
		if json.Unmarshal([]byte(cards), &cm) == nil {
			keys := make([]string, 0, len(cm))
			for k := range cm {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				p.Names = append(p.Names, cm[k])
			}
		}
		var al []any
		if json.Unmarshal([]byte(aliases), &al) == nil {
			for _, a := range al {
				if s, ok := a.(string); ok {
					p.Names = append(p.Names, s)
				}
			}
		}
		out.People = append(out.People, p)
		out.names[uid] = qqName{nick: nick, cards: cm}
	}
	if err := rows.Err(); err != nil {
		return QQPeople{}, fmt.Errorf("读 %s 的 qq_people：%w", file, err)
	}
	return out, nil
}
