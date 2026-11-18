package store

import (
	"context"
	"sort"
)

// KnownQQConversations 是本机目录，不以角色观察关系筛选，也不向协议端查询。
func (s *Set) KnownQQConversations(ctx context.Context) ([]DisplayConversation, error) {
	var st *Store
	for _, named := range s.Each() {
		if named.Platform == "qq" {
			st = named.Store
			break
		}
	}
	if st == nil {
		return nil, nil
	}
	hasGroups, e := tableExists(st.Events, "qq_groups")
	if e != nil {
		return nil, e
	}
	hasPeople, e := tableExists(st.Events, "qq_people")
	if e != nil {
		return nil, e
	}
	query := `SELECT DISTINCT conversation FROM events WHERE channel='qq' AND kind='message'`
	if hasGroups {
		query += ` UNION SELECT 'qq:group:'||gid FROM qq_groups`
	}
	rows, e := st.Events.QueryContext(ctx, query)
	if e != nil {
		return nil, e
	}
	var out []DisplayConversation
	for rows.Next() {
		var key string
		if e = rows.Scan(&key); e != nil {
			rows.Close()
			return nil, e
		}
		out = append(out, DisplayConversation{Conversation: key})
	}
	e = rows.Err()
	closeErr := rows.Close()
	if e != nil {
		return nil, e
	}
	if closeErr != nil {
		return nil, closeErr
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Conversation < out[j].Conversation })
	if hasGroups && hasPeople {
		if e = s.fillQQTitles(ctx, out); e != nil {
			return nil, e
		}
	}
	return out, nil
}
