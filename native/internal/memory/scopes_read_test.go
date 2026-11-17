package memory

import (
	"context"
	"database/sql"
	"slices"
	"testing"
)

// 范围组是真源（item_scopes），facts.scope 只放第一个值：读条目时读整组；组里没有的条目退回单值（没迁移，或迁移以后又有别人写的新行）
func TestItemsReadTheScopeGroup(t *testing.T) {
	t.Parallel()
	file := migratedDB(t, func(db *sql.DB) {
		for _, q := range []string{
			`INSERT INTO facts (id, kind, text, visibility, status, created_at, scope) VALUES ('F1', 'fact', '主人在群里说过的事', 'private', 'active', 1, 'owner')`,
			`INSERT INTO facts (id, kind, text, visibility, status, created_at, scope) VALUES ('F2', 'fact', '迁移以后才写进来的行', 'private', 'active', 1, 'owner')`,
			`INSERT INTO episodes (id, conversation, text, visibility, from_seq, to_seq, created_at, scope) VALUES ('E1', 'qq:group:30001', '群里的一段经历', 'private', 1, 5, 1, 'conv:qq:group:30001')`,
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
	})
	// 迁移已经把 F1 的 owner 回填了；再加一个群范围进组。F2 模拟「迁移以后 Bun 又写了新行」：删掉它的组
	db, err := sql.Open("sqlite", "file:"+file)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO item_scopes (ref, scope) VALUES ('f:F1', 'conv:qq:group:30001')`,
		`DELETE FROM item_scopes WHERE ref = 'f:F2'`,
		`INSERT INTO item_scopes (ref, scope) VALUES ('e:E1', 'owner')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()

	for _, writable := range []bool{false, true} {
		m, err := Open(context.Background(), file, nil, nil, Options{Writable: writable, Character: "suzune"})
		if err != nil {
			t.Fatal(err)
		}
		by := map[string]Item{}
		for _, it := range m.Items() {
			by[it.Ref] = it
		}
		m.Close()
		if got := by["f:F1"].Scopes; !slices.Equal(got, []string{"owner", "conv:qq:group:30001"}) { // 第一个值（facts.scope）排前面
			t.Errorf("Writable=%v：F1 该读到整组：%v", writable, got)
		}
		if got := by["f:F2"].Scopes; !slices.Equal(got, []string{"owner"}) {
			t.Errorf("Writable=%v：组里没有的退回单值：%v", writable, got)
		}
		if got := by["e:E1"].Scopes; !slices.Equal(got, []string{"conv:qq:group:30001", "owner"}) {
			t.Errorf("Writable=%v：片段也读整组：%v", writable, got)
		}
	}

	// 范围组保留，但同角色召回不再按范围过滤。
	m, err := Open(context.Background(), file, nil, nil, Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	ctx := context.Background()
	for name, c := range map[string]struct {
		a    Audience
		want bool
	}{"群 A": {inGroup(GA), true}, "主人本地私聊": {ownerHere, true}, "群 B": {inGroup(GB), true}} {
		hits := m.Recall(ctx, c.a, Query{Text: "主人在群里说过的事", Mode: ModeExplicit}).Hits
		found := slices.ContainsFunc(hits, func(h Hit) bool { return h.Ref == "f:F1" })
		if found != c.want {
			t.Errorf("%s：想得起来应该是 %v：%+v", name, c.want, hits)
		}
	}
}
