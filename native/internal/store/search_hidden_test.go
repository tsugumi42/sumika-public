package store

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/pressly/goose/v3"

	"sumika-native/internal/history"
	"sumika-native/internal/paths"
)

// 定期任务那一轮的话（待分类、[无事]）不进原话搜索索引；分了类改成可见的再补进去，行和原文一直留在 events 里。
func TestHiddenLoopOriginsStayOutOfSearchIndex(t *testing.T) {
	t.Parallel()
	set, _ := goSet(t)
	local, _ := set.Platform("local")
	conv := "console:a"
	add := func(actor, origin, text string) int64 {
		t.Helper()
		seq, err := local.AppendEvent(NewEvent{Channel: "console", Conversation: conv, ActorKind: actor, ActorID: actor, Origin: origin, Kind: "message", Text: text, CreatedAt: 100}, []string{"a"})
		if err != nil {
			t.Fatal(err)
		}
		return seq
	}
	indexed := func(seq int64, text string) (trigram, short bool) {
		t.Helper()
		var n int
		if err := local.Events.QueryRow(`SELECT count(*) FROM events_trigram WHERE events_trigram MATCH ? AND rowid = ?`, `"`+text+`"`, seq).Scan(&n); err != nil {
			t.Fatal(err)
		}
		var m int
		if err := local.Events.QueryRow(`SELECT count(*) FROM events_short WHERE event_seq = ?`, seq).Scan(&m); err != nil {
			t.Fatal(err)
		}
		return n > 0, m > 0
	}
	normal := add("owner", "owner", "普通的一句话")
	task := add("owner", history.OriginLoopPending, "每小时看看天气（定期任务第 1 次）")
	quiet := add("character", history.OriginLoopPending, "[无事]")
	if tr, sh := indexed(normal, "普通的一句话"); !tr || !sh {
		t.Fatalf("普通消息要进索引：%v %v", tr, sh)
	}
	if tr, sh := indexed(task, "每小时看看天气"); tr || sh {
		t.Fatalf("待分类的输入不该进索引：%v %v", tr, sh)
	}
	if tr, sh := indexed(quiet, "[无事]"); tr || sh {
		t.Fatalf("待分类的 [无事] 不该进索引：%v %v", tr, sh)
	}
	// 分类：回了 [无事] 改成永久隐藏，仍然不在索引里；原文都还在
	if n, err := local.RetagOrigin(conv, task-1, 0, history.OriginLoopPending, history.OriginLoopQuiet, ""); err != nil || n != 2 {
		t.Fatalf("改成 [无事]：%d %v", n, err)
	}
	for seq, text := range map[int64]string{task: "每小时看看天气", quiet: "[无事]"} {
		if tr, sh := indexed(seq, text); tr || sh {
			t.Fatalf("永久隐藏的行不该进索引：%q %v %v", text, tr, sh)
		}
	}
	var text string
	if err := local.Events.QueryRow(`SELECT text FROM events WHERE seq = ?`, quiet).Scan(&text); err != nil || text != "[无事]" {
		t.Fatalf("原文要留着：%q %v", text, err)
	}
	// 另一轮有内容：待分类改成可见，补进索引
	task2 := add("owner", history.OriginLoopPending, "每小时看看天气（定期任务第 2 次）")
	reply2 := add("character", history.OriginLoopPending, "今天有雨，记得带伞")
	if n, err := local.RetagOrigin(conv, task2-1, 0, history.OriginLoopPending, "loop", "owner"); err != nil || n != 1 {
		t.Fatalf("输入改成可见：%d %v", n, err)
	}
	if n, err := local.RetagOrigin(conv, task2-1, 0, history.OriginLoopPending, "character", "character"); err != nil || n != 1 {
		t.Fatalf("回答改成可见：%d %v", n, err)
	}
	if tr, sh := indexed(task2, "每小时看看天气"); !tr || !sh {
		t.Fatalf("改成可见后要补进索引：%v %v", tr, sh)
	}
	if tr, sh := indexed(reply2, "记得带伞"); !tr || !sh {
		t.Fatalf("可见的回答要进索引：%v %v", tr, sh)
	}
	// 可见的行改字、改来源后索引跟着走（原来的触发器行为不变）
	if _, err := local.Events.Exec(`UPDATE events SET text = '改过的话' WHERE seq = ?`, reply2); err != nil {
		t.Fatal(err)
	}
	if tr, _ := indexed(reply2, "记得带伞"); tr {
		t.Fatal("改字以后旧字不该还在索引里")
	}
	if tr, sh := indexed(reply2, "改过的话"); !tr || !sh {
		t.Fatalf("改字以后新字要进索引：%v %v", tr, sh)
	}
}

// 升级：旧库里已经在索引中的待分类和 [无事] 行被摘出来，可见的行不动；降回去再升上来也一致。
func TestSearchSkipHiddenMigration(t *testing.T) {
	t.Parallel()
	p, err := paths.Init(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file := p.ChatDB("local")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	db, err := open(file, false)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := migrate(db, "migrations/chat", 14); err != nil {
		t.Fatal(err)
	}
	insert := func(id, origin, text string) {
		t.Helper()
		if _, err := db.Exec(`INSERT INTO events(id,channel,conversation,actor_kind,actor_id,origin,kind,text,created_at) VALUES(?, 'console', 'console:a', 'character', 'a', ?, 'message', ?, 1)`, id, origin, text); err != nil {
			t.Fatal(err)
		}
	}
	insert("shown", "character", "可见的一句")
	insert("pending", history.OriginLoopPending, "待分类的一句")
	insert("quiet", history.OriginLoopQuiet, "[无事]")
	assertSearchMatch(t, db, "events_trigram", "待分类", "pending", true) // 旧触发器：都进了索引
	assertShortMatch(t, db, "无事", "quiet", true)

	if err := migrate(db, "migrations/chat", 0); err != nil {
		t.Fatal(err)
	}
	assertSearchMatch(t, db, "events_trigram", "可见的", "shown", true)
	assertShortMatch(t, db, "可见", "shown", true)
	assertSearchMatch(t, db, "events_trigram", "待分类", "pending", false)
	assertShortMatch(t, db, "待分", "pending", false)
	assertSearchMatch(t, db, "events_trigram", "[无事]", "quiet", false)
	assertShortMatch(t, db, "无事", "quiet", false)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM events`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("升级不能动 events 的行：%d %v", n, err)
	}
	// 升级后新写入的隐藏行也不进
	insert("pending2", history.OriginLoopPending, "新的待分类")
	assertSearchMatch(t, db, "events_trigram", "新的待", "pending2", false)

	// 降回去：旧触发器回来，隐藏行重新进索引；再升上来又摘掉
	sub, err := fs.Sub(migrations, "migrations/chat")
	if err != nil {
		t.Fatal(err)
	}
	pv, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pv.DownTo(context.Background(), 14); err != nil {
		t.Fatal(err)
	}
	assertSearchMatch(t, db, "events_trigram", "待分类", "pending", true)
	assertShortMatch(t, db, "无事", "quiet", true)
	if _, err := pv.UpTo(context.Background(), 15); err != nil {
		t.Fatal(err)
	}
	assertSearchMatch(t, db, "events_trigram", "待分类", "pending", false)
	assertShortMatch(t, db, "无事", "quiet", false)
	assertSearchMatch(t, db, "events_trigram", "可见的", "shown", true)
}

// 迁移里写死的隐藏来源名要和 history 里的一致（两处各写一份，改一处另一处会漏）。
func TestSearchSkipHiddenMigrationNamesMatchHistory(t *testing.T) {
	t.Parallel()
	for _, dir := range []string{"migrations/chat"} {
		entries, err := fs.ReadDir(migrations, dir)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, e := range entries {
			if !strings.Contains(e.Name(), "search_skip_hidden") {
				continue
			}
			found = true
			b, err := fs.ReadFile(migrations, dir+"/"+e.Name())
			if err != nil {
				t.Fatal(err)
			}
			for _, o := range []string{history.OriginLoopPending, history.OriginLoopQuiet} {
				if !strings.Contains(string(b), "'"+o+"'") {
					t.Fatalf("%s/%s 里没有来源 %q", dir, e.Name(), o)
				}
			}
		}
		if !found {
			t.Fatalf("%s 里没有 search_skip_hidden 迁移", dir)
		}
	}
}
