package desktopread

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"testing"

	"sumika-native/internal/api"
)

func dumpTables(t *testing.T, db *sql.DB) map[string]string {
	t.Helper()
	rows, e := db.Query(`SELECT name FROM sqlite_master WHERE type='table' AND sql NOT LIKE 'CREATE VIRTUAL TABLE%' ORDER BY name`)
	if e != nil {
		t.Fatal(e)
	}
	var names []string
	for rows.Next() {
		var name string
		if e = rows.Scan(&name); e != nil {
			t.Fatal(e)
		}
		names = append(names, name)
	}
	if e = rows.Err(); e != nil {
		t.Fatal(e)
	}
	rows.Close()
	out := map[string]string{}
	for _, name := range names {
		// 全文索引的影子表有 WITHOUT ROWID 的，不按 rowid 排：逐行编码后排序
		r, e := db.Query(fmt.Sprintf(`SELECT * FROM "%s"`, name))
		if e != nil {
			t.Fatal(e)
		}
		cols, e := r.Columns()
		if e != nil {
			t.Fatal(e)
		}
		var all []string
		for r.Next() {
			values := make([]any, len(cols))
			pointers := make([]any, len(cols))
			for i := range values {
				pointers[i] = &values[i]
			}
			if e = r.Scan(pointers...); e != nil {
				t.Fatal(e)
			}
			row, e := json.Marshal(values)
			if e != nil {
				t.Fatal(e)
			}
			all = append(all, string(row))
		}
		if e = r.Err(); e != nil {
			t.Fatal(e)
		}
		r.Close()
		sort.Strings(all)
		encoded, e := json.Marshal(all)
		if e != nil {
			t.Fatal(e)
		}
		out[name] = string(encoded)
	}
	return out
}

func TestUnreadExactThresholdAndProcessBaseline(t *testing.T) {
	t.Parallel()
	s, set := fixture(t)
	qq, _ := set.Platform("qq")
	conv := "qq:group:90002"
	add(t, qq, conv, "message", "human", 10, []string{"a"})
	overview := func() api.Overview {
		t.Helper()
		sub, e := s.WatchOverview(context.Background(), api.OverviewRequest{Limit: 200})
		if e != nil {
			t.Fatal(e)
		}
		v := next(t, sub)
		sub.Close()
		return v.Value
	}
	count := func(v api.Overview) int {
		for _, c := range v.Conversations.Items {
			if c.Key.Conversation == conv {
				return c.Unread
			}
		}
		t.Fatal("缺少会话")
		return -1
	}
	before := dumpTables(t, qq.Events)
	if got := count(overview()); got != 0 {
		t.Fatalf("首次基线未读=%d", got)
	}
	if after := dumpTables(t, qq.Events); !reflect.DeepEqual(before, after) {
		t.Fatal("首次读取修改业务表")
	}
	add(t, qq, conv, "message", "human", 10, []string{"a"})
	add(t, qq, conv, "message", "character", 11, []string{"a"})
	add(t, qq, conv, "notice", "human", 11, []string{"a"})
	add(t, qq, conv, "message", "human", 11, nil)
	for range 99 {
		add(t, qq, conv, "message", "human", 11, []string{"a"})
	}
	before = dumpTables(t, qq.Events)
	if got := count(overview()); got != 99 {
		t.Fatalf("99 条未读=%d", got)
	}
	if after := dumpTables(t, qq.Events); !reflect.DeepEqual(before, after) {
		t.Fatal("重建订阅修改业务表")
	}
	add(t, qq, conv, "message", "human", 11, []string{"a"})
	if got := count(overview()); got != 100 {
		t.Fatalf("100 条未读=%d", got)
	}
	add(t, qq, conv, "message", "human", 11, []string{"a"})
	if got := count(overview()); got != 101 { // 2026-11-02 起不封顶（原来 101 条也只数到 100）
		t.Fatalf("101 条未读=%d", got)
	}
}
