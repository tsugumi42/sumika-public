package store

import (
	"path/filepath"
	"slices"
	"testing"
)

// putRefBlocks 在自己的事务里写一批块（和交回的用法一样）；校验不过就回滚，把错误还给调用方。
func putRefBlocks(t *testing.T, s *Store, blocks []RefBlock) error {
	t.Helper()
	tx, err := s.Events.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := PutRefBlocksTx(tx, blocks); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func openRefStore(t *testing.T) *Store {
	t.Helper()
	db, err := openGo(filepath.Join(t.TempDir(), "events.db"), "migrations/events", "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{Events: db}
}

func refTexts(bs []RefBlock) []string {
	var out []string
	for _, b := range bs {
		out = append(out, b.Text)
	}
	return out
}

// 0006：旧库（迁到 0005）升级后有 ref_blocks 表，老事件不受影响
func TestRefBlocksUpgradeFrom0005(t *testing.T) {
	t.Parallel()
	file := filepath.Join(t.TempDir(), "events.db")
	db, err := open(file, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrate(db, "migrations/events", 5); err != nil {
		db.Close()
		t.Fatal(err)
	}
	if ok, err := tableExists(db, "ref_blocks"); err != nil || ok {
		db.Close()
		t.Fatalf("起点应没有 ref_blocks：%v %v", ok, err)
	}
	db.Close()
	s2, err := openGo(file, "migrations/events", "")
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	s := &Store{Events: s2}
	if ok, err := tableExists(s2, "ref_blocks"); err != nil || !ok {
		t.Fatalf("升级后应有 ref_blocks：%v %v", ok, err)
	}
	if err := putRefBlocks(t, s, []RefBlock{{CharacterID: "c", Conversation: "k", ScopeKey: "public", BeforeSeq: 3, Ordinal: 0, CardHash: "h", MemoryRefsJSON: "[]", Text: "t", CreatedAt: 1}}); err != nil {
		t.Fatal(err)
	}
}

func TestRefBlocksPutListIsolation(t *testing.T) {
	t.Parallel()
	s := openRefStore(t)
	base := RefBlock{CharacterID: "c", Conversation: "k", ScopeKey: "public", CardHash: "h", MemoryRefsJSON: "[]", CreatedAt: 1}
	mk := func(scope string, before int64, ord int, text string) RefBlock {
		b := base
		b.ScopeKey, b.BeforeSeq, b.Ordinal, b.Text = scope, before, ord, text
		return b
	}
	if err := putRefBlocks(t, s, []RefBlock{mk("public", 5, 1, "b5-1"), mk("public", 5, 0, "b5-0"), mk("public", 2, 0, "b2"), mk("owner", 5, 0, "owner-only")}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListRefBlocks("c", "k", "public", 0)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"b2", "b5-0", "b5-1"}; !slices.Equal(refTexts(got), want) {
		t.Fatalf("同档块 %v，要 %v（按 before_seq、ordinal 排）", refTexts(got), want)
	}
	// owner 档只有它自己那一条；别的角色、别的会话、别的档都取不到
	if got, _ := s.ListRefBlocks("c", "k", "owner", 0); !slices.Equal(refTexts(got), []string{"owner-only"}) {
		t.Fatalf("owner 档应只有自己那一条：%v", refTexts(got))
	}
	for _, q := range [][3]string{{"other", "k", "public"}, {"c", "other", "public"}, {"c", "k", "conv:x"}} {
		if got, _ := s.ListRefBlocks(q[0], q[1], q[2], 0); len(got) != 0 {
			t.Fatalf("%v 不该取到块：%v", q, refTexts(got))
		}
	}
}

// 窗口起点正好等于一块的 before_seq 时这块要留：窗口跳变处，少一块就断前缀。
func TestRefBlocksWindowStartInclusive(t *testing.T) {
	t.Parallel()
	s := openRefStore(t)
	base := RefBlock{CharacterID: "c", Conversation: "k", ScopeKey: "public", CardHash: "h", MemoryRefsJSON: "[]", CreatedAt: 1}
	mk := func(before int64, text string) RefBlock {
		b := base
		b.BeforeSeq, b.Text = before, text
		return b
	}
	if err := putRefBlocks(t, s, []RefBlock{mk(2, "b2"), mk(5, "b5"), mk(9, "b9")}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		from int64
		want []string
	}{
		{0, []string{"b2", "b5", "b9"}},
		{2, []string{"b2", "b5", "b9"}},
		{3, []string{"b5", "b9"}},
		{5, []string{"b5", "b9"}},
		{6, []string{"b9"}},
		{10, nil},
	}
	for _, c := range cases {
		got, err := s.ListRefBlocks("c", "k", "public", c.from)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(refTexts(got), c.want) {
			t.Fatalf("窗口起点 %d：得到 %v，要 %v", c.from, refTexts(got), c.want)
		}
	}
}

func TestRefBlocksRejectsAndIsAtomic(t *testing.T) {
	t.Parallel()
	s := openRefStore(t)
	good := RefBlock{CharacterID: "c", Conversation: "k", ScopeKey: "public", BeforeSeq: 1, CardHash: "h", MemoryRefsJSON: "[]", Text: "ok", CreatedAt: 1}
	noScope := good
	noScope.ScopeKey = ""
	badRefs := good
	badRefs.BeforeSeq, badRefs.MemoryRefsJSON = 2, `[{"id":`
	for name, batch := range map[string][]RefBlock{"缺档": {good, noScope}, "记忆引用解析失败": {good, badRefs}} {
		if err := putRefBlocks(t, s, batch); err == nil {
			t.Fatalf("%s 应拒绝", name)
		}
		if got, _ := s.ListRefBlocks("c", "k", "public", 0); len(got) != 0 {
			t.Fatalf("%s 拒绝后不该留下半截块：%v", name, refTexts(got))
		}
	}
	if _, err := s.ListRefBlocks("c", "k", "", 0); err == nil {
		t.Fatal("读时缺档应报错，不当通配")
	}
	// 同一主键重交覆盖（Commit 重做幂等）
	if err := putRefBlocks(t, s, []RefBlock{good}); err != nil {
		t.Fatal(err)
	}
	again := good
	again.Text = "ok-again"
	if err := putRefBlocks(t, s, []RefBlock{again}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListRefBlocks("c", "k", "public", 0); !slices.Equal(refTexts(got), []string{"ok-again"}) {
		t.Fatalf("同主键应覆盖为一条：%v", refTexts(got))
	}
}

// 交回的事务失败回滚时，块跟着回滚：库里不能多出活实例没有的块。
func TestRefBlocksRollbackLeavesNothing(t *testing.T) {
	t.Parallel()
	s := openRefStore(t)
	good := RefBlock{CharacterID: "c", Conversation: "k", ScopeKey: "public", BeforeSeq: 1, CardHash: "h", MemoryRefsJSON: "[]", Text: "ok", CreatedAt: 1}
	tx, err := s.Events.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := PutRefBlocksTx(tx, []RefBlock{good}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListRefBlocks("c", "k", "public", 0); len(got) != 0 {
		t.Fatalf("回滚后不该有块：%v", refTexts(got))
	}
	// 同一事务提交才可见
	tx2, err := s.Events.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := PutRefBlocksTx(tx2, []RefBlock{good}); err != nil {
		t.Fatal(err)
	}
	if err := tx2.Commit(); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListRefBlocks("c", "k", "public", 0); !slices.Equal(refTexts(got), []string{"ok"}) {
		t.Fatalf("提交后应有一块：%v", refTexts(got))
	}
}

// 记忆引用只要不是 [{id,hash}] 数组（含 null、标量、对象、缺字段）整批拒绝；合法的 [] 和带引用的照常写。
func TestRefBlocksRejectsBadMemoryRefs(t *testing.T) {
	t.Parallel()
	s := openRefStore(t)
	good := RefBlock{CharacterID: "c", Conversation: "k", ScopeKey: "public", BeforeSeq: 1, CardHash: "h", MemoryRefsJSON: "[]", Text: "ok", CreatedAt: 1}
	for _, bad := range []string{`null`, `5`, `{}`, `"x"`, `[1]`, `[{"id":"a"}]`, `[{"hash":"h"}]`, `[{"id":"","hash":"h"}]`, `[] []`, `[{"id":`} {
		b := good
		b.BeforeSeq, b.MemoryRefsJSON = 2, bad
		if err := putRefBlocks(t, s, []RefBlock{good, b}); err == nil {
			t.Fatalf("记忆引用 %s 应拒绝", bad)
		}
	}
	if got, _ := s.ListRefBlocks("c", "k", "public", 0); len(got) != 0 {
		t.Fatalf("拒绝后不该留下块：%v", refTexts(got))
	}
	withRef := good
	withRef.BeforeSeq, withRef.MemoryRefsJSON = 2, `[{"id":"m1","hash":"h1"}]`
	if err := putRefBlocks(t, s, []RefBlock{good, withRef}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListRefBlocks("c", "k", "public", 0); len(got) != 2 {
		t.Fatalf("合法引用应写入：%v", refTexts(got))
	}
}

// 空文字不写行（规格 7.6：与 buildContext 返回 "" 一致），同批其他块照写。
func TestRefBlocksEmptyTextNotWritten(t *testing.T) {
	t.Parallel()
	s := openRefStore(t)
	empty := RefBlock{CharacterID: "c", Conversation: "k", ScopeKey: "public", BeforeSeq: 1, CardHash: "h", MemoryRefsJSON: "[]", Text: "", CreatedAt: 1}
	ok := empty
	ok.BeforeSeq, ok.Text = 2, "ok"
	if err := putRefBlocks(t, s, []RefBlock{empty, ok}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListRefBlocks("c", "k", "public", 0); !slices.Equal(refTexts(got), []string{"ok"}) {
		t.Fatalf("空文字不该写行：%v", refTexts(got))
	}
}

// 库里已有引用坏掉的行（绕过写入校验写进去的）：读取时整块跳过，不当成没有引用照插。
func TestRefBlocksReadSkipsBadRefRow(t *testing.T) {
	t.Parallel()
	s := openRefStore(t)
	good := RefBlock{CharacterID: "c", Conversation: "k", ScopeKey: "public", BeforeSeq: 1, CardHash: "h", MemoryRefsJSON: "[]", Text: "ok", CreatedAt: 1}
	if err := putRefBlocks(t, s, []RefBlock{good}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Events.Exec(`INSERT INTO ref_blocks
		(character_id, conversation, scope_key, before_seq, ordinal, card_hash, memory_refs_json, text, created_at)
		VALUES ('c', 'k', 'public', 3, 0, 'h', 'null', 'bad', 1)`); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListRefBlocks("c", "k", "public", 0)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(refTexts(got), []string{"ok"}) {
		t.Fatalf("引用坏掉的行应跳过：%v", refTexts(got))
	}
}

// 窗口起点之前的块清掉（同会话所有档一起）；窗口之后的块和别的会话不动。
func TestRefBlocksPruneBelowWindow(t *testing.T) {
	t.Parallel()
	s := openRefStore(t)
	base := RefBlock{CharacterID: "c", Conversation: "k", ScopeKey: "public", CardHash: "h", MemoryRefsJSON: "[]", CreatedAt: 1}
	mk := func(scope, conv string, before int64, text string) RefBlock {
		b := base
		b.ScopeKey, b.Conversation, b.BeforeSeq, b.Text = scope, conv, before, text
		return b
	}
	if err := putRefBlocks(t, s, []RefBlock{
		mk("public", "k", 2, "p2"), mk("public", "k", 5, "p5"), mk("public", "k", 9, "p9"),
		mk("owner", "k", 2, "o2"), mk("owner", "k", 7, "o7"), mk("public", "other", 2, "x2"),
	}); err != nil {
		t.Fatal(err)
	}
	if err := pruneRefBlocks(t, s, "", "k", 5); err == nil {
		t.Fatal("清理缺角色应报错，不当通配")
	}
	if err := pruneRefBlocks(t, s, "c", "", 5); err == nil {
		t.Fatal("清理缺会话应报错，不当通配")
	}
	if err := pruneRefBlocks(t, s, "c", "k", 5); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListRefBlocks("c", "k", "public", 0); !slices.Equal(refTexts(got), []string{"p5", "p9"}) {
		t.Fatalf("public 窗口起点 5 之前应清掉：%v", refTexts(got))
	}
	if got, _ := s.ListRefBlocks("c", "k", "owner", 0); !slices.Equal(refTexts(got), []string{"o7"}) {
		t.Fatalf("owner 档窗口起点之前也应清掉，之后的留着：%v", refTexts(got))
	}
	if got, _ := s.ListRefBlocks("c", "other", "public", 0); !slices.Equal(refTexts(got), []string{"x2"}) {
		t.Fatalf("别的会话不该被清：%v", refTexts(got))
	}
}

func pruneRefBlocks(t *testing.T, s *Store, characterID, conversation string, fromSeq int64) error {
	t.Helper()
	tx, err := s.Events.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := PruneRefBlocksTx(tx, characterID, conversation, fromSeq); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

// 生产的平台库（local、qq）跑 migrations/chat：每个平台库都要有 ref_blocks，能写能读；
// 它和 OpenFresh 单库用的 migrations/events/0006 是同一张表（建表语句逐字相同，改一处忘了另一处会红）
func TestRefBlocksInPlatformDBs(t *testing.T) {
	t.Parallel()
	s, _ := goSet(t)
	for _, conv := range []string{"console:suzu", "qq:group:30001"} {
		st, err := s.ForConversation(conv)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := tableExists(st.Events, "ref_blocks"); err != nil || !ok {
			t.Fatalf("%s 所在的平台库应有 ref_blocks：%v %v", conv, ok, err)
		}
		b := RefBlock{CharacterID: "c", Conversation: conv, ScopeKey: "public", BeforeSeq: 7, CardHash: "h", MemoryRefsJSON: "[]", Text: "块", CreatedAt: 1}
		if err := st.SaveRefBlocks([]RefBlock{b}, "c", conv, 0); err != nil {
			t.Fatal(err)
		}
		got, err := st.ListRefBlocks("c", conv, "public", 0)
		if err != nil || len(got) != 1 || got[0].Text != "块" {
			t.Fatalf("读回来：%v %v", got, err)
		}
	}
	chat := openRefStoreFrom(t, "migrations/chat")
	events := openRefStoreFrom(t, "migrations/events")
	var a, b string
	q := `SELECT sql FROM sqlite_master WHERE name = 'ref_blocks'`
	if err := chat.Events.QueryRow(q).Scan(&a); err != nil {
		t.Fatal(err)
	}
	if err := events.Events.QueryRow(q).Scan(&b); err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatalf("两处迁移建出来的 ref_blocks 不一样：\nchat   %s\nevents %s", a, b)
	}
}

func openRefStoreFrom(t *testing.T, dir string) *Store {
	t.Helper()
	db, err := openGo(filepath.Join(t.TempDir(), "x.db"), dir, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{Events: db}
}

// SaveRefBlocks 一个事务：写块和清理一起成败；清理缺角色不当通配、整批回滚
func TestSaveRefBlocksAtomic(t *testing.T) {
	t.Parallel()
	s := openRefStore(t)
	mk := func(seq int64, text string) RefBlock {
		return RefBlock{CharacterID: "c", Conversation: "k", ScopeKey: "public", BeforeSeq: seq, CardHash: "h", MemoryRefsJSON: "[]", Text: text, CreatedAt: 1}
	}
	if err := s.SaveRefBlocks([]RefBlock{mk(1, "旧"), mk(5, "新")}, "c", "k", 0); err != nil {
		t.Fatal(err)
	}
	// 写新的同时清窗口起点（3）之前的
	if err := s.SaveRefBlocks([]RefBlock{mk(9, "更新")}, "c", "k", 3); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListRefBlocks("c", "k", "public", 0)
	if !slices.Equal(refTexts(got), []string{"新", "更新"}) {
		t.Fatalf("seq 1 的该清掉：%v", refTexts(got))
	}
	// 清理参数不合法：整批回滚，新块也不留
	if err := s.SaveRefBlocks([]RefBlock{mk(11, "不该留")}, "", "k", 4); err == nil {
		t.Fatal("清理缺角色应当报错")
	}
	got, _ = s.ListRefBlocks("c", "k", "public", 0)
	if !slices.Equal(refTexts(got), []string{"新", "更新"}) {
		t.Fatalf("失败的一批不该留下东西：%v", refTexts(got))
	}
}

// 按记忆清理：只删引用了它的块；角色为空是所有角色；没有引用的块不动
func TestPurgeRefBlocksByMemory(t *testing.T) {
	t.Parallel()
	s := openRefStore(t)
	mk := func(char string, seq int64, refs string) RefBlock {
		return RefBlock{CharacterID: char, Conversation: "k", ScopeKey: "public", BeforeSeq: seq, CardHash: "h", MemoryRefsJSON: refs, Text: char + "块", CreatedAt: 1}
	}
	blocks := []RefBlock{
		mk("a", 1, `[{"id":"f:1","hash":"x"},{"id":"f:2","hash":"y"}]`),
		mk("a", 2, `[]`),
		mk("b", 3, `[{"id":"f:1","hash":"x"}]`),
		mk("a", 4, `[{"id":"f:3","hash":"z"}]`),
	}
	if err := s.SaveRefBlocks(blocks, "a", "k", 0); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeRefBlocksByMemory("a", []string{"f:2"}); err != nil {
		t.Fatal(err)
	}
	left := func(char string) int {
		got, _ := s.ListRefBlocks(char, "k", "public", 0)
		return len(got)
	}
	if left("a") != 2 || left("b") != 1 {
		t.Fatalf("只该删 a 里引用 f:2 的那块：a=%d b=%d", left("a"), left("b"))
	}
	if err := s.PurgeRefBlocksByMemory("", []string{"f:1"}); err != nil {
		t.Fatal(err)
	}
	if left("a") != 2 || left("b") != 0 {
		t.Fatalf("角色为空是所有角色：a=%d b=%d（a 里引用 f:1 的那块已在上一步随 f:2 删了）", left("a"), left("b"))
	}
	if err := s.PurgeRefBlocksByMemory("a", nil); err != nil {
		t.Fatal(err)
	}
	if left("a") != 2 {
		t.Fatal("没有记忆编号什么都不删")
	}
}

// 直接改库留下的坏 JSON 行：按记忆清理不报错、别的行照清（读的时候坏行本来就整块跳过）
func TestPurgeRefBlocksSkipsBadJSON(t *testing.T) {
	t.Parallel()
	s := openRefStore(t)
	good := RefBlock{CharacterID: "a", Conversation: "k", ScopeKey: "public", BeforeSeq: 1, CardHash: "h", MemoryRefsJSON: `[{"id":"f:1","hash":"x"}]`, Text: "好块", CreatedAt: 1}
	if err := s.SaveRefBlocks([]RefBlock{good}, "a", "k", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Events.Exec(`INSERT INTO ref_blocks (character_id, conversation, scope_key, before_seq, ordinal, card_hash, memory_refs_json, text, created_at) VALUES ('a','k','public',2,0,'h','这不是 JSON','坏块',1)`); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeRefBlocksByMemory("a", []string{"f:1"}); err != nil {
		t.Fatalf("坏 JSON 行不该让清理失败：%v", err)
	}
	var texts []string
	rows, err := s.Events.Query(`SELECT text FROM ref_blocks ORDER BY before_seq`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var x string
		rows.Scan(&x)
		texts = append(texts, x)
	}
	if !slices.Equal(texts, []string{"坏块"}) {
		t.Fatalf("好块该清掉、坏行不动：%v", texts)
	}
}
